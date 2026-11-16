package tracker

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"cpa-cache-tracker/abi"
)

func sampleJSONBody(msg string) []byte {
	return []byte(fmt.Sprintf(`{"model":"gpt-4o","messages":[{"role":"user","content":%q}]}`, msg))
}

func TestCorrelatorRetriesAndNestedSubcalls(t *testing.T) {
	c := NewCorrelator(100, 5*time.Minute, 15*time.Second)
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	// 1. Parent main request (TraceID: "t1", AuthID: "auth-a", Model: "m1") intercepts
	reqParent := abi.RequestInterceptRequest{
		RequestID:    "req-parent",
		TraceID:      "t1",
		Model:        "m1",
		SourceFormat: "openai_chat",
		ToFormat:     "openai_chat",
		Body:         sampleJSONBody("parent prompt"),
		Metadata: map[string]any{
			"selected_auth_id":       "auth-a",
			"canonical_session_id":   "sess-main",
			"lcp_affinity_session_id": "lcp-sess-1",
		},
	}
	c.RecordInterceptAfter(reqParent, true, 512, t0)

	if got := c.Len(); got != 1 {
		t.Fatalf("expected Len()=1, got %d", got)
	}

	// 2. Nested host.model.execute intercepts under same TraceID: "t1"
	reqNested := abi.RequestInterceptRequest{
		RequestID:    "req-nested",
		TraceID:      "t1",
		Model:        "m-helper",
		SourceFormat: "openai_chat",
		ToFormat:     "openai_chat",
		Body:         sampleJSONBody("helper subcall prompt"),
		Metadata: map[string]any{
			"selected_auth_id":       "auth-b",
			"model_execution_source": "plugin_host_model_callback",
		},
	}
	c.RecordInterceptAfter(reqNested, true, 512, t0.Add(500*time.Millisecond))

	if got := c.Len(); got != 2 {
		t.Fatalf("expected Len()=2, got %d", got)
	}

	// 3. Nested publishes usage BEFORE parent finishes
	uNested := abi.UsageRecord{
		TraceID: "t1",
		AuthID:  "auth-b",
		Model:   "m-helper",
	}
	attNested, ok := c.MatchUsage(uNested, t0.Add(1*time.Second))
	if !ok || attNested == nil {
		t.Fatalf("expected nested attempt to match usage")
	}
	if attNested.HandlerReqID != "req-nested" {
		t.Errorf("expected HandlerReqID='req-nested', got %q", attNested.HandlerReqID)
	}
	if attNested.RecordKind != "internal_subcall" {
		t.Errorf("expected RecordKind='internal_subcall', got %q", attNested.RecordKind)
	}
	if attNested.AuthID != "auth-b" {
		t.Errorf("expected AuthID='auth-b', got %q", attNested.AuthID)
	}
	if attNested.Model != "m-helper" {
		t.Errorf("expected Model='m-helper', got %q", attNested.Model)
	}
	if !attNested.HasFingerprint || attNested.Fingerprint.FirstUserHash == "" {
		t.Errorf("expected valid fingerprint for nested call")
	}

	// Len should now be 1 (parent still in queue)
	if got := c.Len(); got != 1 {
		t.Fatalf("expected Len()=1 after nested usage matched, got %d", got)
	}

	// 4. Retry intercepts under same TraceID: "t1" (e.g. parent hit 429)
	reqRetry := abi.RequestInterceptRequest{
		RequestID:    "req-retry",
		TraceID:      "t1",
		Model:        "m1",
		SourceFormat: "openai_chat",
		ToFormat:     "openai_chat",
		Body:         sampleJSONBody("parent prompt retry"),
		Metadata: map[string]any{
			"selected_auth_id":       "auth-c",
			"canonical_session_id":   "sess-main",
			"lcp_affinity_session_id": "lcp-sess-1",
		},
	}
	c.RecordInterceptAfter(reqRetry, true, 512, t0.Add(2*time.Second))

	if got := c.Len(); got != 2 {
		t.Fatalf("expected Len()=2 after retry intercepted, got %d", got)
	}

	// 5. Match retry usage (AuthID: "auth-c", Model: "m1")
	uRetry := abi.UsageRecord{
		TraceID: "t1",
		AuthID:  "auth-c",
		Model:   "m1",
	}
	attRetry, ok := c.MatchUsage(uRetry, t0.Add(3*time.Second))
	if !ok || attRetry == nil {
		t.Fatalf("expected retry attempt to match usage")
	}
	if attRetry.HandlerReqID != "req-retry" {
		t.Errorf("expected HandlerReqID='req-retry', got %q", attRetry.HandlerReqID)
	}
	if attRetry.AuthID != "auth-c" {
		t.Errorf("expected AuthID='auth-c', got %q", attRetry.AuthID)
	}
	if attRetry.RecordKind != "main" {
		t.Errorf("expected RecordKind='main', got %q", attRetry.RecordKind)
	}

	// 6. Match parent usage (AuthID: "auth-a", Model: "m1")
	uParent := abi.UsageRecord{
		TraceID: "t1",
		AuthID:  "auth-a",
		Model:   "m1",
	}
	attParent, ok := c.MatchUsage(uParent, t0.Add(4*time.Second))
	if !ok || attParent == nil {
		t.Fatalf("expected parent attempt to match usage")
	}
	if attParent.HandlerReqID != "req-parent" {
		t.Errorf("expected HandlerReqID='req-parent', got %q", attParent.HandlerReqID)
	}
	if attParent.AuthID != "auth-a" {
		t.Errorf("expected AuthID='auth-a', got %q", attParent.AuthID)
	}
	if attParent.RecordKind != "main" {
		t.Errorf("expected RecordKind='main', got %q", attParent.RecordKind)
	}
	if !attParent.HasFingerprint || attParent.Fingerprint.FirstUserHash == "" {
		t.Errorf("expected parent fingerprint to remain intact without being clobbered")
	}

	if got := c.Len(); got != 0 {
		t.Fatalf("expected Len()=0 after all matched, got %d", got)
	}
}

func TestCorrelatorCompletionAndGraceEviction(t *testing.T) {
	cancelGrace := 15 * time.Second
	c := NewCorrelator(100, 5*time.Minute, cancelGrace)
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	// Case 1: Outcome == "rejected" purges immediately
	reqRej := abi.RequestInterceptRequest{
		RequestID: "req-rej",
		TraceID:   "trace-rej",
		Model:     "gpt-4o",
		Body:      sampleJSONBody("will be rejected"),
	}
	c.RecordInterceptAfter(reqRej, true, 512, t0)
	if c.Len() != 1 {
		t.Fatalf("expected Len()=1, got %d", c.Len())
	}

	compRej := abi.RequestCompletion{
		RequestID: "req-rej",
		TraceID:   "trace-rej",
		Outcome:   abi.RequestCompletionRejected,
	}
	c.HandleCompletion(compRej, t0.Add(100*time.Millisecond))
	if got := c.Len(); got != 0 {
		t.Fatalf("expected Len()=0 immediately after rejected, got %d", got)
	}

	// Matching should return false
	_, ok := c.MatchUsage(abi.UsageRecord{TraceID: "trace-rej"}, t0.Add(200*time.Millisecond))
	if ok {
		t.Errorf("expected no match for rejected request")
	}

	// Case 2: Outcome == "canceled" stays matchable within cancelGrace (15s)
	reqCan := abi.RequestInterceptRequest{
		RequestID: "req-can-1",
		TraceID:   "trace-can-1",
		Model:     "gpt-4o",
		Body:      sampleJSONBody("will be canceled but matched"),
		Metadata: map[string]any{
			"selected_auth_id": "auth-can",
		},
	}
	c.RecordInterceptAfter(reqCan, true, 512, t0)
	c.HandleCompletion(abi.RequestCompletion{
		RequestID: "req-can-1",
		TraceID:   "trace-can-1",
		Outcome:   abi.RequestCompletionCanceled,
	}, t0.Add(1*time.Second))

	// Match at t0 + 10s (within 15s grace) -> should succeed
	att, ok := c.MatchUsage(abi.UsageRecord{
		TraceID: "trace-can-1",
		AuthID:  "auth-can",
		Model:   "gpt-4o",
	}, t0.Add(10*time.Second))
	if !ok || att == nil {
		t.Fatalf("expected canceled attempt to be matchable within grace period")
	}
	if att.HandlerReqID != "req-can-1" {
		t.Errorf("expected HandlerReqID='req-can-1', got %q", att.HandlerReqID)
	}

	// Case 3: Outcome == "canceled" swept after cancelGrace expires
	reqCanExpired := abi.RequestInterceptRequest{
		RequestID: "req-can-2",
		TraceID:   "trace-can-2",
		Model:     "gpt-4o",
		Body:      sampleJSONBody("will be canceled and swept"),
	}
	c.RecordInterceptAfter(reqCanExpired, true, 512, t0)
	c.HandleCompletion(abi.RequestCompletion{
		RequestID: "req-can-2",
		TraceID:   "trace-can-2",
		Outcome:   abi.RequestCompletionCanceled,
	}, t0.Add(1*time.Second))

	// Attempt match at t0 + 17s (16s after cancellation, beyond 15s grace)
	_, ok = c.MatchUsage(abi.UsageRecord{TraceID: "trace-can-2"}, t0.Add(17*time.Second))
	if ok {
		t.Fatalf("expected canceled attempt to be swept after cancelGrace expired")
	}
	if got := c.Len(); got != 0 {
		t.Fatalf("expected Len()=0 after swept, got %d", got)
	}
}

func TestCorrelatorOmitBodyMetadataOnly(t *testing.T) {
	c := NewCorrelator(100, 5*time.Minute, 15*time.Second)
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	req := abi.RequestInterceptRequest{
		RequestID:      "req-omit",
		TraceID:        "t-omit",
		SourceFormat:   "openai_chat",
		ToFormat:       "anthropic_messages",
		Model:          "claude-3-7-sonnet",
		RequestedModel: "claude-3-7-sonnet-alias",
		Body:           nil, // omitted body
		Metadata: map[string]any{
			"selected_auth_id":        "auth-user-99",
			"canonical_session_id":    "sess-c-1",
			"parent_session_id":       "sess-p-0",
			"lcp_affinity_session_id": "lcp-aff-77",
			"is_fork":                 true,
			"is_compaction":           false,
		},
	}

	// Record with traceEnabled = false and nil body
	c.RecordInterceptAfter(req, false, 512, t0)

	att, ok := c.MatchUsage(abi.UsageRecord{TraceID: "t-omit"}, t0.Add(time.Second))
	if !ok || att == nil {
		t.Fatalf("expected pending attempt to match even without body")
	}

	if att.HasFingerprint {
		t.Errorf("expected HasFingerprint=false")
	}
	if att.ToFormat != "anthropic_messages" {
		t.Errorf("expected ToFormat='anthropic_messages', got %q", att.ToFormat)
	}
	if att.SourceFormat != "openai_chat" {
		t.Errorf("expected SourceFormat='openai_chat', got %q", att.SourceFormat)
	}
	if att.AuthID != "auth-user-99" {
		t.Errorf("expected AuthID='auth-user-99', got %q", att.AuthID)
	}
	if att.CanonicalSessionID != "sess-c-1" {
		t.Errorf("expected CanonicalSessionID='sess-c-1', got %q", att.CanonicalSessionID)
	}
	if att.ParentSessionID != "sess-p-0" {
		t.Errorf("expected ParentSessionID='sess-p-0', got %q", att.ParentSessionID)
	}
	if att.LCPAffinitySessionID != "lcp-aff-77" {
		t.Errorf("expected LCPAffinitySessionID='lcp-aff-77', got %q", att.LCPAffinitySessionID)
	}
	if !att.IsFork {
		t.Errorf("expected IsFork=true")
	}
	if att.IsCompaction {
		t.Errorf("expected IsCompaction=false")
	}
	if att.RecordKind != "main" {
		t.Errorf("expected RecordKind='main', got %q", att.RecordKind)
	}
}

func TestCorrelatorMaxCapEviction(t *testing.T) {
	maxCap := 5
	c := NewCorrelator(maxCap, 5*time.Minute, 15*time.Second)
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	// Append 10 entries into correlator with cap=5
	for i := 0; i < 10; i++ {
		req := abi.RequestInterceptRequest{
			RequestID: fmt.Sprintf("req-%d", i),
			TraceID:   fmt.Sprintf("trace-%d", i),
			Model:     "gpt-4o",
			Body:      sampleJSONBody(fmt.Sprintf("message %d", i)),
		}
		c.RecordInterceptAfter(req, true, 512, t0.Add(time.Duration(i)*time.Second))
	}

	if got := c.Len(); got != maxCap {
		t.Fatalf("expected Len()=%d, got %d", maxCap, got)
	}

	// The first 5 (trace-0 through trace-4) should have been evicted
	for i := 0; i < 5; i++ {
		traceID := fmt.Sprintf("trace-%d", i)
		if _, ok := c.MatchUsage(abi.UsageRecord{TraceID: traceID}, t0.Add(20*time.Second)); ok {
			t.Errorf("expected traceID %s to have been evicted", traceID)
		}
	}

	// The last 5 (trace-5 through trace-9) should still exist
	for i := 5; i < 10; i++ {
		traceID := fmt.Sprintf("trace-%d", i)
		att, ok := c.MatchUsage(abi.UsageRecord{TraceID: traceID}, t0.Add(20*time.Second))
		if !ok || att == nil {
			t.Errorf("expected traceID %s to be present", traceID)
		} else if att.HandlerReqID != fmt.Sprintf("req-%d", i) {
			t.Errorf("expected HandlerReqID req-%d, got %s", i, att.HandlerReqID)
		}
	}

	if got := c.Len(); got != 0 {
		t.Fatalf("expected Len()=0 after matching remaining entries, got %d", got)
	}
}

func TestCorrelatorMatchPriorityTiers(t *testing.T) {
	c := NewCorrelator(100, 5*time.Minute, 15*time.Second)
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	// Three attempts under the same traceID
	reqs := []struct {
		reqID  string
		authID string
		model  string
	}{
		{"req-1", "auth-alpha", "model-x"},
		{"req-2", "auth-beta", "model-x"},
		{"req-3", "auth-alpha", "model-y"},
	}

	for i, r := range reqs {
		c.RecordInterceptAfter(abi.RequestInterceptRequest{
			RequestID: r.reqID,
			TraceID:   "t-prio",
			Model:     r.model,
			Body:      sampleJSONBody("test"),
			Metadata: map[string]any{
				"selected_auth_id": r.authID,
			},
		}, true, 512, t0.Add(time.Duration(i)*time.Millisecond))
	}

	// 1. Priority 1 match: exact AuthID == "auth-alpha" && Model == "model-y" -> req-3
	att1, ok1 := c.MatchUsage(abi.UsageRecord{
		TraceID: "t-prio",
		AuthID:  "auth-alpha",
		Model:   "model-y",
	}, t0.Add(time.Second))
	if !ok1 || att1.HandlerReqID != "req-3" {
		t.Fatalf("expected Priority 1 match to pick req-3, got %v (ok=%v)", att1, ok1)
	}

	// 2. Priority 2 match: AuthID == "auth-beta", Model is empty -> req-2
	att2, ok2 := c.MatchUsage(abi.UsageRecord{
		TraceID: "t-prio",
		AuthID:  "auth-beta",
		Model:   "",
	}, t0.Add(2*time.Second))
	if !ok2 || att2.HandlerReqID != "req-2" {
		t.Fatalf("expected Priority 2 match to pick req-2, got %v (ok=%v)", att2, ok2)
	}

	// 3. Only req-1 remains (auth-alpha, model-x).
	// An empty usage (AuthID="", Model="") should fall back to Priority 4 (earliest FIFO head) -> req-1
	att3, ok3 := c.MatchUsage(abi.UsageRecord{
		TraceID: "t-prio",
		AuthID:  "",
		Model:   "",
	}, t0.Add(3*time.Second))
	if !ok3 || att3.HandlerReqID != "req-1" {
		t.Fatalf("expected Priority 4 fallback to pick req-1, got %v (ok=%v)", att3, ok3)
	}
}

func TestCorrelatorRecordKindClassification(t *testing.T) {
	c := NewCorrelator(100, 5*time.Minute, 15*time.Second)
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name               string
		req                abi.RequestInterceptRequest
		expectedKind       string
		expectedCompaction bool
	}{
		{
			name: "internal subcall takes precedence",
			req: abi.RequestInterceptRequest{
				RequestID: "r1",
				TraceID:   "t1",
				Metadata: map[string]any{
					"model_execution_source": "plugin_host_model_callback",
					"request_path":           "/v1/responses/compact",
				},
			},
			expectedKind: "internal_subcall",
		},
		{
			name: "compact via request_path suffix",
			req: abi.RequestInterceptRequest{
				RequestID: "r2",
				TraceID:   "t2",
				Metadata: map[string]any{
					"request_path": "/v1/responses/compact",
				},
			},
			expectedKind: "compact",
		},
		{
			name: "compact via ToFormat",
			req: abi.RequestInterceptRequest{
				RequestID: "r3",
				TraceID:   "t3",
				ToFormat:  "openai/responses/compact",
			},
			expectedKind: "compact",
		},
		{
			name: "compact via SourceFormat",
			req: abi.RequestInterceptRequest{
				RequestID:    "r4",
				TraceID:      "t4",
				SourceFormat: "responses/compact",
			},
			expectedKind: "compact",
		},
		{
			name: "compact via alt metadata",
			req: abi.RequestInterceptRequest{
				RequestID: "r5",
				TraceID:   "t5",
				Metadata: map[string]any{
					"alt": "responses/compact",
				},
			},
			expectedKind: "compact",
		},
		{
			name: "LCP compaction hit keeps RecordKind main with IsCompaction true",
			req: abi.RequestInterceptRequest{
				RequestID: "r6",
				TraceID:   "t6",
				Metadata: map[string]any{
					"is_compaction": true,
				},
			},
			expectedKind:       "main",
			expectedCompaction: true,
		},
		{
			name: "standard main request",
			req: abi.RequestInterceptRequest{
				RequestID:    "r7",
				TraceID:      "t7",
				SourceFormat: "openai_chat",
				ToFormat:     "openai_chat",
			},
			expectedKind: "main",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c.RecordInterceptAfter(tc.req, false, 512, t0)
			att, ok := c.MatchUsage(abi.UsageRecord{TraceID: tc.req.TraceID}, t0.Add(time.Second))
			if !ok || att == nil {
				t.Fatalf("failed to match attempt for %s", tc.name)
			}
			if att.RecordKind != tc.expectedKind {
				t.Errorf("expected RecordKind=%q, got %q", tc.expectedKind, att.RecordKind)
			}
			if tc.expectedCompaction && !att.IsCompaction {
				t.Errorf("expected IsCompaction=true for %s", tc.name)
			}
		})
	}
}

func TestCorrelatorTTLExpiration(t *testing.T) {
	ttl := 100 * time.Millisecond
	c := NewCorrelator(100, ttl, 15*time.Second)
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	c.RecordInterceptAfter(abi.RequestInterceptRequest{
		RequestID: "req-ttl",
		TraceID:   "t-ttl",
		Model:     "gpt-4o",
	}, false, 512, t0)

	if c.Len() != 1 {
		t.Fatalf("expected Len()=1, got %d", c.Len())
	}

	// Match at t0 + 50ms (within TTL) should succeed
	// But let's test match at t0 + 150ms (expired)
	_, ok := c.MatchUsage(abi.UsageRecord{TraceID: "t-ttl"}, t0.Add(150*time.Millisecond))
	if ok {
		t.Fatalf("expected attempt to be expired and not matchable")
	}
	if got := c.Len(); got != 0 {
		t.Fatalf("expected Len()=0 after expiration, got %d", got)
	}
}

func TestCorrelatorFallbackToRequestID(t *testing.T) {
	c := NewCorrelator(100, 5*time.Minute, 15*time.Second)
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	// When TraceID is empty, fallback to RequestID
	c.RecordInterceptAfter(abi.RequestInterceptRequest{
		RequestID: "req-solo",
		TraceID:   "",
		Model:     "gpt-4o",
	}, false, 512, t0)

	att, ok := c.MatchUsage(abi.UsageRecord{
		RequestID: "req-solo",
		TraceID:   "",
	}, t0.Add(time.Second))

	if !ok || att == nil {
		t.Fatalf("expected match using RequestID fallback")
	}
	if att.HandlerReqID != "req-solo" {
		t.Errorf("expected HandlerReqID='req-solo', got %q", att.HandlerReqID)
	}
}

func TestCorrelatorConcurrentAccess(t *testing.T) {
	c := NewCorrelator(500, 5*time.Minute, 15*time.Second)
	t0 := time.Now()

	var wg sync.WaitGroup
	workers := 20
	iterations := 50

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				traceID := fmt.Sprintf("trace-%d-%d", workerID, i)
				reqID := fmt.Sprintf("req-%d-%d", workerID, i)
				authID := fmt.Sprintf("auth-%d", workerID)

				c.RecordInterceptAfter(abi.RequestInterceptRequest{
					RequestID: reqID,
					TraceID:   traceID,
					Model:     "gpt-4o",
					Metadata: map[string]any{
						"selected_auth_id": authID,
					},
					Body: sampleJSONBody("concurrent test"),
				}, true, 64, t0)

				_ = c.Len()

				if i%3 == 0 {
					c.HandleCompletion(abi.RequestCompletion{
						RequestID: reqID,
						TraceID:   traceID,
						Outcome:   abi.RequestCompletionRejected,
					}, t0.Add(time.Millisecond))
				} else {
					_, _ = c.MatchUsage(abi.UsageRecord{
						TraceID: traceID,
						AuthID:  authID,
						Model:   "gpt-4o",
					}, t0.Add(time.Millisecond))
				}
			}
		}(w)
	}

	wg.Wait()
}

func TestCorrelatorSeqByTracePruning(t *testing.T) {
	c := NewCorrelator(5, 5*time.Minute, 15*time.Second)
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)

	// 1. MatchUsage prunes seqByTrace entry
	c.RecordInterceptAfter(abi.RequestInterceptRequest{
		RequestID: "req-seq-1",
		TraceID:   "trace-seq-1",
		Model:     "gpt-4o",
	}, false, 512, t0)

	if len(c.seqByTrace) != 1 {
		t.Fatalf("expected len(seqByTrace)=1 after record, got %d", len(c.seqByTrace))
	}

	_, ok := c.MatchUsage(abi.UsageRecord{TraceID: "trace-seq-1"}, t0.Add(time.Second))
	if !ok {
		t.Fatalf("expected match to succeed")
	}
	if len(c.seqByTrace) != 0 {
		t.Errorf("expected len(seqByTrace)=0 after MatchUsage, got %d", len(c.seqByTrace))
	}

	// 2. HandleCompletion (rejected) prunes seqByTrace entry
	c.RecordInterceptAfter(abi.RequestInterceptRequest{
		RequestID: "req-seq-2",
		TraceID:   "trace-seq-2",
		Model:     "gpt-4o",
	}, false, 512, t0)

	if len(c.seqByTrace) != 1 {
		t.Fatalf("expected len(seqByTrace)=1, got %d", len(c.seqByTrace))
	}

	c.HandleCompletion(abi.RequestCompletion{
		RequestID: "req-seq-2",
		TraceID:   "trace-seq-2",
		Outcome:   abi.RequestCompletionRejected,
	}, t0.Add(time.Second))

	if len(c.seqByTrace) != 0 {
		t.Errorf("expected len(seqByTrace)=0 after HandleCompletion rejected, got %d", len(c.seqByTrace))
	}

	// 3. Max capacity eviction prunes seqByTrace for evicted entries
	// Correlator maxEntries = 5. Add 8 entries.
	for i := 0; i < 8; i++ {
		c.RecordInterceptAfter(abi.RequestInterceptRequest{
			RequestID: fmt.Sprintf("req-seq-evict-%d", i),
			TraceID:   fmt.Sprintf("trace-seq-evict-%d", i),
			Model:     "gpt-4o",
		}, false, 512, t0.Add(time.Duration(i)*time.Second))
	}

	// seqByTrace must be bounded by active entries (5)
	if len(c.seqByTrace) > 5 {
		t.Errorf("expected len(seqByTrace) <= 5 after capacity eviction, got %d", len(c.seqByTrace))
	}

	// Match remaining 5 entries: seqByTrace must drop to 0
	for i := 3; i < 8; i++ {
		_, _ = c.MatchUsage(abi.UsageRecord{TraceID: fmt.Sprintf("trace-seq-evict-%d", i)}, t0.Add(20*time.Second))
	}

	if len(c.seqByTrace) != 0 {
		t.Errorf("expected len(seqByTrace)=0 after all matched, got %d", len(c.seqByTrace))
	}
}
