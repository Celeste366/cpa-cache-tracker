package tracker

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"cpa-cache-tracker/fingerprint"
)

func TestResolveSessionKey(t *testing.T) {
	// 1. nil record
	if got := ResolveSessionKey(nil); got != "" {
		t.Fatalf("expected empty key for nil record, got %q", got)
	}

	// 2. SessionID has top priority
	rec := &Record{
		SessionID:            "sess-main",
		LCPAffinitySessionID: "lcp-aff",
		AuthID:               "auth-1",
		Fingerprint: &fingerprint.Result{
			FirstUserHash: "user-hash",
			SystemHash:    "sys-hash",
		},
	}
	if got := ResolveSessionKey(rec); got != "sess-main" {
		t.Fatalf("expected %q, got %q", "sess-main", got)
	}

	// 3. Trim whitespace on SessionID
	rec = &Record{
		SessionID:            "   ",
		LCPAffinitySessionID: "lcp-aff",
	}
	if got := ResolveSessionKey(rec); got != "lcp-aff" {
		t.Fatalf("expected fallback to lcp-aff, got %q", got)
	}

	// 4. LCPAffinitySessionID priority over anon fallback
	rec = &Record{
		LCPAffinitySessionID: "lcp-aff",
		AuthID:               "auth-1",
		Fingerprint: &fingerprint.Result{
			FirstUserHash: "user-hash",
			SystemHash:    "sys-hash",
		},
	}
	if got := ResolveSessionKey(rec); got != "lcp-aff" {
		t.Fatalf("expected %q, got %q", "lcp-aff", got)
	}

	// 5. Trim whitespace on LCPAffinitySessionID
	rec = &Record{
		LCPAffinitySessionID: " \t ",
		AuthID:               "auth-1",
		Fingerprint: &fingerprint.Result{
			FirstUserHash: "user-hash",
			SystemHash:    "sys-hash",
		},
	}
	expectedAnon := "anon:auth-1:user-hash:sys-hash"
	if got := ResolveSessionKey(rec); got != expectedAnon {
		t.Fatalf("expected %q, got %q", expectedAnon, got)
	}

	// 6. Anon fallback requires FirstUserHash != ""
	rec = &Record{
		AuthID: "auth-1",
		Fingerprint: &fingerprint.Result{
			FirstUserHash: "",
			SystemHash:    "sys-hash",
		},
	}
	if got := ResolveSessionKey(rec); got != "" {
		t.Fatalf("expected empty key when FirstUserHash is empty, got %q", got)
	}

	// 7. Anon fallback with nil Fingerprint
	rec = &Record{
		AuthID:      "auth-1",
		Fingerprint: nil,
	}
	if got := ResolveSessionKey(rec); got != "" {
		t.Fatalf("expected empty key when Fingerprint is nil, got %q", got)
	}
}

func TestCompareRecords_NilSafe(t *testing.T) {
	// Both nil
	diff := CompareRecords(nil, nil)
	if diff.AuthChanged || diff.MissReason != "" {
		t.Fatalf("unexpected diff for both nil: %+v", diff)
	}

	// Cur nil, prev non-nil
	prev := &Record{AuthID: "auth-1"}
	diff = CompareRecords(nil, prev)
	if diff.AuthChanged {
		t.Fatalf("unexpected auth changed when cur is nil: %+v", diff)
	}

	// Cur non-nil, prev nil
	cur := &Record{
		AuthID:         "auth-1",
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			MessageCount:   3,
			IsContinuation: true,
		},
	}
	diff = CompareRecords(cur, nil)
	if diff.CurAuthID != "auth-1" || diff.CurMessageCount != 3 || !diff.IsContinuation {
		t.Fatalf("expected Cur metadata populated on prev nil: %+v", diff)
	}
	if diff.AuthChanged {
		t.Fatalf("auth_changed should be false when prev is nil: %+v", diff)
	}
}

func TestCompareRecords_AuthChanged(t *testing.T) {
	prev := &Record{AuthID: "auth-user-1"}
	cur := &Record{AuthID: "auth-user-2"}

	diff := CompareRecords(cur, prev)
	if !diff.AuthChanged {
		t.Fatalf("expected AuthChanged to be true")
	}
	if diff.PrevAuthID != "auth-user-1" || diff.CurAuthID != "auth-user-2" {
		t.Fatalf("unexpected auth IDs: prev=%q, cur=%q", diff.PrevAuthID, diff.CurAuthID)
	}

	// Same auth
	cur.AuthID = "auth-user-1"
	diff = CompareRecords(cur, prev)
	if diff.AuthChanged {
		t.Fatalf("expected AuthChanged to be false when auth matches")
	}
}

func TestCompareRecords_ChangedFieldsAndOptions(t *testing.T) {
	prev := &Record{
		AuthID:         "auth-1",
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			SystemHash:    "sys-1",
			ToolsHash:     "tools-1",
			OptionsHash:   "opt-1",
			FirstUserHash: "user-1",
			OptionHashes: map[string]string{
				"model":       "hash-gpt-4o",
				"temperature": "hash-0.7",
				"stop":        "hash-stop-prev",
			},
			MessageCount:  2,
			MessageHashes: []string{"m1", "m2"},
		},
	}

	cur := &Record{
		AuthID:         "auth-1",
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			SystemHash:    "sys-2",
			ToolsHash:     "tools-2",
			OptionsHash:   "opt-2",
			FirstUserHash: "user-2",
			OptionHashes: map[string]string{
				"model":            "hash-gpt-4o",
				"temperature":      "hash-0.2",
				"prompt_cache_key": "hash-key-cur",
			},
			MessageCount:  3,
			MessageHashes: []string{"m1", "m2", "m3"},
		},
	}

	diff := CompareRecords(cur, prev)

	expectedFields := []string{"system_hash", "tools_hash", "options_hash", "first_user_hash"}
	if !reflect.DeepEqual(diff.ChangedFields, expectedFields) {
		t.Fatalf("expected changed fields %v, got %v", expectedFields, diff.ChangedFields)
	}

	// Option diff union:
	// "model": same in both -> not in diff
	// "temperature": differing -> in diff
	// "stop": present in prev only -> in diff
	// "prompt_cache_key": present in cur only -> in diff
	// Sorted: ["prompt_cache_key", "stop", "temperature"]
	expectedOptions := []string{"prompt_cache_key", "stop", "temperature"}
	if !reflect.DeepEqual(diff.ChangedOptions, expectedOptions) {
		t.Fatalf("expected changed options %v, got %v", expectedOptions, diff.ChangedOptions)
	}
}

func TestCompareRecords_LeadingMessagesAndAppendOnly(t *testing.T) {
	// Case 1: Append-only turn
	prev := &Record{
		AuthID:         "auth-1",
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			MessageCount:  2,
			MessageHashes: []string{"msg-1", "msg-2"},
		},
	}
	cur := &Record{
		AuthID:         "auth-1",
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			MessageCount:  4,
			MessageHashes: []string{"msg-1", "msg-2", "msg-3", "msg-4"},
		},
	}

	diff := CompareRecords(cur, prev)
	if diff.IdenticalLeadingMessages != 2 {
		t.Fatalf("expected IdenticalLeadingMessages = 2, got %d", diff.IdenticalLeadingMessages)
	}
	if !diff.AppendOnly {
		t.Fatalf("expected AppendOnly = true")
	}
	if diff.FirstChangedMessage != 0 {
		t.Fatalf("expected FirstChangedMessage = 0 for append-only, got %d", diff.FirstChangedMessage)
	}

	// Case 2: Prefix modified at message 2
	cur.Fingerprint.MessageHashes = []string{"msg-1", "msg-2-modified", "msg-3", "msg-4"}
	diff = CompareRecords(cur, prev)
	if diff.IdenticalLeadingMessages != 1 {
		t.Fatalf("expected IdenticalLeadingMessages = 1, got %d", diff.IdenticalLeadingMessages)
	}
	if diff.AppendOnly {
		t.Fatalf("expected AppendOnly = false")
	}
	if diff.FirstChangedMessage != 2 {
		t.Fatalf("expected FirstChangedMessage = 2 (1-based), got %d", diff.FirstChangedMessage)
	}

	// Case 3: Truncated hashes
	cur.Fingerprint.HashesTruncated = true
	cur.Fingerprint.MessageHashes = []string{"msg-1", "msg-2"}
	diff = CompareRecords(cur, prev)
	if diff.AppendOnly {
		t.Fatalf("AppendOnly must be false when HashesTruncated is true")
	}

	// Case 4: Continuation skips FirstChangedMessage
	cur.Fingerprint.HashesTruncated = false
	cur.Fingerprint.IsContinuation = true
	cur.Fingerprint.MessageHashes = []string{"delta-msg"}
	diff = CompareRecords(cur, prev)
	if diff.FirstChangedMessage != 0 {
		t.Fatalf("FirstChangedMessage must be 0 for continuation, got %d", diff.FirstChangedMessage)
	}
}

func TestClassifyMissReason_NonMiss(t *testing.T) {
	// 1. nil cur
	reason, _ := ClassifyMissReason(nil, nil)
	if reason != "" {
		t.Fatalf("expected empty reason for nil cur, got %q", reason)
	}

	// 2. Failed request
	cur := &Record{
		Failed: true,
		Tokens: NormalizedTokens{EffectiveInput: 100, CacheRead: 0},
	}
	reason, diff := ClassifyMissReason(cur, nil)
	if reason != "" || diff.MissReason != "" {
		t.Fatalf("expected empty reason for failed request, got %q", reason)
	}

	// 3. Zero effective input
	cur = &Record{
		Tokens: NormalizedTokens{EffectiveInput: 0, CacheRead: 0},
	}
	reason, diff = ClassifyMissReason(cur, nil)
	if reason != "" || diff.MissReason != "" {
		t.Fatalf("expected empty reason for zero effective input, got %q", reason)
	}

	// 4. Cache hit (CacheRead > 0)
	cur = &Record{
		Tokens: NormalizedTokens{EffectiveInput: 100, CacheRead: 80},
	}
	reason, diff = ClassifyMissReason(cur, nil)
	if reason != "" || diff.MissReason != "" {
		t.Fatalf("expected empty reason for cache hit, got %q", reason)
	}
}

func TestClassifyMissReason_ColdStart(t *testing.T) {
	// Prev is nil -> cold_start
	cur := &Record{
		AuthID:         "auth-1",
		TraceEnabled:   true,
		HasFingerprint: true,
		Tokens:         NormalizedTokens{EffectiveInput: 200, CacheRead: 0},
		Fingerprint: &fingerprint.Result{
			MessageCount: 1,
		},
	}

	reason, diff := ClassifyMissReason(cur, nil)
	if reason != "cold_start" {
		t.Fatalf("expected %q, got %q", "cold_start", reason)
	}
	if diff.MissReason != "cold_start" {
		t.Fatalf("expected diff.MissReason = %q, got %q", "cold_start", diff.MissReason)
	}

	// cold_start even when TraceEnabled is false on turn 1
	cur.TraceEnabled = false
	cur.HasFingerprint = false
	cur.Fingerprint = nil
	reason, diff = ClassifyMissReason(cur, nil)
	if reason != "cold_start" {
		t.Fatalf("expected cold_start on turn 1 without tracing, got %q", reason)
	}
}

func TestClassifyMissReason_AuthSwitched(t *testing.T) {
	prev := &Record{
		AuthID: "auth-cred-A",
		Tokens: NormalizedTokens{EffectiveInput: 100, CacheRead: 0},
	}
	cur := &Record{
		AuthID:       "auth-cred-B",
		TraceEnabled: false, // works even without tracing!
		Tokens:       NormalizedTokens{EffectiveInput: 150, CacheRead: 0},
	}

	reason, diff := ClassifyMissReason(cur, prev)
	if reason != "auth_switched" {
		t.Fatalf("expected %q, got %q", "auth_switched", reason)
	}
	if !diff.AuthChanged {
		t.Fatalf("expected diff.AuthChanged = true")
	}
}

func TestClassifyMissReason_TraceDisabledAndMissingFingerprint(t *testing.T) {
	prev := &Record{
		AuthID:       "auth-1",
		TraceEnabled: true,
		Tokens:       NormalizedTokens{EffectiveInput: 100, CacheRead: 0},
	}
	cur := &Record{
		AuthID:       "auth-1",
		TraceEnabled: false,
		Tokens:       NormalizedTokens{EffectiveInput: 150, CacheRead: 0},
	}

	// Tracing disabled on cur -> trace_disabled
	reason, _ := ClassifyMissReason(cur, prev)
	if reason != "trace_disabled" {
		t.Fatalf("expected %q, got %q", "trace_disabled", reason)
	}

	// Tracing enabled on both, but fingerprint missing (e.g. parse skipped) -> upstream_cache_miss
	cur.TraceEnabled = true
	prev.TraceEnabled = true
	cur.HasFingerprint = false
	prev.HasFingerprint = false
	reason, _ = ClassifyMissReason(cur, prev)
	if reason != "upstream_cache_miss" {
		t.Fatalf("expected %q for missing fingerprint with tracing on, got %q", "upstream_cache_miss", reason)
	}
}

func TestClassifyMissReason_StructuralChanges(t *testing.T) {
	baseFP := fingerprint.Result{
		SystemHash:    "sys-common",
		ToolsHash:     "tools-common",
		OptionsHash:   "opt-common",
		MessageCount:  2,
		MessageHashes: []string{"m1", "m2"},
	}

	prev := &Record{
		AuthID:         "auth-1",
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint:    &baseFP,
		Tokens:         NormalizedTokens{EffectiveInput: 100, CacheRead: 0},
	}

	// 1. system_changed
	curFP := baseFP
	curFP.SystemHash = "sys-different"
	cur := &Record{
		AuthID:         "auth-1",
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint:    &curFP,
		Tokens:         NormalizedTokens{EffectiveInput: 120, CacheRead: 0},
	}
	reason, _ := ClassifyMissReason(cur, prev)
	if reason != "system_changed" {
		t.Fatalf("expected system_changed, got %q", reason)
	}

	// 2. tools_changed
	curFP = baseFP
	curFP.ToolsHash = "tools-different"
	cur.Fingerprint = &curFP
	reason, _ = ClassifyMissReason(cur, prev)
	if reason != "tools_changed" {
		t.Fatalf("expected tools_changed, got %q", reason)
	}

	// 3. options_changed
	curFP = baseFP
	curFP.OptionsHash = "opt-different"
	curFP.OptionHashes = map[string]string{"temperature": "0.5"}
	prev.Fingerprint.OptionHashes = map[string]string{"temperature": "0.7"}
	cur.Fingerprint = &curFP
	reason, diff := ClassifyMissReason(cur, prev)
	if reason != "options_changed" {
		t.Fatalf("expected options_changed, got %q", reason)
	}
	if !reflect.DeepEqual(diff.ChangedOptions, []string{"temperature"}) {
		t.Fatalf("expected ChangedOptions = [temperature], got %v", diff.ChangedOptions)
	}
}

func TestClassifyMissReason_HistoryShortenedAndPrefixModified(t *testing.T) {
	baseFP := fingerprint.Result{
		SystemHash:    "sys-1",
		ToolsHash:     "tools-1",
		OptionsHash:   "opt-1",
		MessageCount:  3,
		MessageHashes: []string{"m1", "m2", "m3"},
	}

	prev := &Record{
		AuthID:         "auth-1",
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint:    &baseFP,
		Tokens:         NormalizedTokens{EffectiveInput: 100, CacheRead: 0},
	}

	// 1. History shortened: cur has 2 messages vs prev 3 messages
	shortFP := baseFP
	shortFP.MessageCount = 2
	shortFP.MessageHashes = []string{"m1", "m2"}
	cur := &Record{
		AuthID:         "auth-1",
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint:    &shortFP,
		Tokens:         NormalizedTokens{EffectiveInput: 80, CacheRead: 0},
	}
	reason, _ := ClassifyMissReason(cur, prev)
	if reason != "history_shortened" {
		t.Fatalf("expected history_shortened, got %q", reason)
	}

	// 2. Prefix modified: cur has 3 messages, but message 2 is modified
	modFP := baseFP
	modFP.MessageHashes = []string{"m1", "m2-altered", "m3"}
	cur.Fingerprint = &modFP
	reason, diff := ClassifyMissReason(cur, prev)
	if reason != "prefix_modified" {
		t.Fatalf("expected prefix_modified, got %q", reason)
	}
	if diff.FirstChangedMessage != 2 {
		t.Fatalf("expected FirstChangedMessage = 2, got %d", diff.FirstChangedMessage)
	}
}

func TestClassifyMissReason_UpstreamCacheMiss(t *testing.T) {
	baseFP := fingerprint.Result{
		SystemHash:    "sys-1",
		ToolsHash:     "tools-1",
		OptionsHash:   "opt-1",
		MessageCount:  2,
		MessageHashes: []string{"m1", "m2"},
	}

	prev := &Record{
		AuthID:         "auth-1",
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint:    &baseFP,
		Tokens:         NormalizedTokens{EffectiveInput: 100, CacheRead: 0},
	}

	// Cur is strictly append-only, everything matches, but cache_read == 0
	curFP := baseFP
	curFP.MessageCount = 3
	curFP.MessageHashes = []string{"m1", "m2", "m3"}
	cur := &Record{
		AuthID:         "auth-1",
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint:    &curFP,
		Tokens:         NormalizedTokens{EffectiveInput: 150, CacheRead: 0},
	}

	reason, diff := ClassifyMissReason(cur, prev)
	if reason != "upstream_cache_miss" {
		t.Fatalf("expected upstream_cache_miss, got %q", reason)
	}
	if !diff.AppendOnly {
		t.Fatalf("expected AppendOnly = true")
	}
}

func TestDiagnoseContinuationMiss(t *testing.T) {
	prevFP := fingerprint.Result{
		SystemHash:    "sys-1",
		ToolsHash:     "tools-1",
		OptionsHash:   "opt-1",
		MessageCount:  5,
		MessageHashes: []string{"m1", "m2", "m3", "m4", "m5"},
	}

	prev := &Record{
		AuthID:         "auth-1",
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint:    &prevFP,
		Tokens:         NormalizedTokens{EffectiveInput: 500, CacheRead: 0},
	}

	// Cur is a continuation turn: only delta input (1 message vs 5 previously)
	curFP := fingerprint.Result{
		SystemHash:     "sys-1",
		ToolsHash:      "tools-1",
		OptionsHash:    "opt-1",
		IsContinuation: true,
		MessageCount:   1,
		MessageHashes:  []string{"delta-msg"},
	}

	cur := &Record{
		AuthID:         "auth-1",
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint:    &curFP,
		Tokens:         NormalizedTokens{EffectiveInput: 600, CacheRead: 0},
	}

	// Should skip history_shortened / prefix_modified and classify as upstream_cache_miss
	reason, diff := ClassifyMissReason(cur, prev)
	if reason != "upstream_cache_miss" {
		t.Fatalf("expected upstream_cache_miss for continuation turn, got %q", reason)
	}
	if diff.FirstChangedMessage != 0 {
		t.Fatalf("expected FirstChangedMessage = 0 for continuation, got %d", diff.FirstChangedMessage)
	}

	// But if options changed on continuation, options_changed takes precedence
	curFP2 := curFP
	curFP2.OptionsHash = "opt-changed"
	curFP2.OptionHashes = map[string]string{"temperature": "0.1"}
	cur.Fingerprint = &curFP2
	reason, diff = ClassifyMissReason(cur, prev)
	if reason != "options_changed" {
		t.Fatalf("expected options_changed on continuation when options differ, got %q", reason)
	}

	// And if auth switched on continuation, auth_switched takes precedence
	cur.AuthID = "auth-switched"
	reason, _ = ClassifyMissReason(cur, prev)
	if reason != "auth_switched" {
		t.Fatalf("expected auth_switched on continuation when auth differs, got %q", reason)
	}
}

func TestRecordAndDiffComparison_JSONTags(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	rec := Record{
		ID:                   "rec-123",
		Timestamp:            now,
		TraceID:              "trace-abc",
		RequestID:            "req-def",
		Provider:             "anthropic",
		ExecutorType:         "ClaudeMessagesExecutor",
		Model:                "claude-3-7-sonnet",
		Alias:                "sonnet",
		DisplayModel:         "sonnet",
		AuthID:               "auth-1",
		SessionID:            "sess-1",
		ParentSessionID:      "sess-parent",
		LCPAffinitySessionID: "lcp-aff",
		EffectiveSessionKey:  "sess-1",
		IsFork:               false,
		IsCompaction:         false,
		RecordKind:           "main",
		Stream:               true,
		Failed:               false,
		StatusCode:           200,
		LatencyMs:            120,
		TTFTMs:               45,
		Tokens: NormalizedTokens{
			EffectiveInput: 1000,
			CacheRead:      800,
			HitRate:        80.0,
			RequestHit:     true,
		},
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			Protocol:     fingerprint.ProtocolClaudeMessages,
			MessageCount: 3,
		},
		PrevID:     "rec-122",
		MissReason: "",
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("failed to marshal Record: %v", err)
	}

	jsonStr := string(data)
	expectedSubstrings := []string{
		`"id":"rec-123"`,
		`"trace_id":"trace-abc"`,
		`"request_id":"req-def"`,
		`"provider":"anthropic"`,
		`"executor_type":"ClaudeMessagesExecutor"`,
		`"model":"claude-3-7-sonnet"`,
		`"alias":"sonnet"`,
		`"display_model":"sonnet"`,
		`"auth_id":"auth-1"`,
		`"session_id":"sess-1"`,
		`"parent_session_id":"sess-parent"`,
		`"lcp_affinity_session_id":"lcp-aff"`,
		`"effective_session_key":"sess-1"`,
		`"record_kind":"main"`,
		`"tokens":`,
		`"effective_input":1000`,
		`"prev_id":"rec-122"`,
	}
	for _, substr := range expectedSubstrings {
		if !strings.Contains(jsonStr, substr) {
			t.Errorf("expected JSON to contain %s, got: %s", substr, jsonStr)
		}
	}

	diff := DiffComparison{
		AuthChanged:              true,
		PrevAuthID:               "auth-1",
		CurAuthID:                "auth-2",
		ChangedFields:            []string{"system_hash"},
		ChangedOptions:           []string{"temperature"},
		IdenticalLeadingMessages: 2,
		PrevMessageCount:         2,
		CurMessageCount:          3,
		AppendOnly:               false,
		FirstChangedMessage:      3,
		HashesTruncated:          false,
		IsContinuation:           false,
		MissReason:               "auth_switched",
	}

	diffData, err := json.Marshal(diff)
	if err != nil {
		t.Fatalf("failed to marshal DiffComparison: %v", err)
	}

	diffStr := string(diffData)
	expectedDiffSubstrings := []string{
		`"auth_changed":true`,
		`"prev_auth_id":"auth-1"`,
		`"cur_auth_id":"auth-2"`,
		`"changed_fields":["system_hash"]`,
		`"changed_options":["temperature"]`,
		`"identical_leading_messages":2`,
		`"prev_message_count":2`,
		`"cur_message_count":3`,
		`"first_changed_message":3`,
		`"miss_reason":"auth_switched"`,
	}
	for _, substr := range expectedDiffSubstrings {
		if !strings.Contains(diffStr, substr) {
			t.Errorf("expected DiffComparison JSON to contain %s, got: %s", substr, diffStr)
		}
	}
}
