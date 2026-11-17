package main_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	main "cpa-cache-tracker"
	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/storage"
)

func TestPluginEndToEndLifecycle(t *testing.T) {
	tempDir := t.TempDir()
	d := main.NewDispatcher()
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	d.SetNowFn(func() time.Time { return fixedNow })

	// =========================================================================
	// 1. plugin.register with trace_enabled: true
	// =========================================================================
	regConfigYAML := fmt.Sprintf(`
data_dir: %q
trace_enabled: true
retention_days: 14
max_disk_mb: 200
max_message_hashes: 512
`, tempDir)

	regReqBytes, _ := json.Marshal(abi.LifecycleRequest{
		ConfigYAML:    []byte(regConfigYAML),
		SchemaVersion: 10,
	})

	regRespBytes, err := d.HandleCall(abi.MethodPluginRegister, regReqBytes)
	if err != nil {
		t.Fatalf("plugin.register call failed: %v", err)
	}

	var regEnv abi.Envelope
	if err := json.Unmarshal(regRespBytes, &regEnv); err != nil {
		t.Fatalf("failed to unmarshal register envelope: %v", err)
	}
	if !regEnv.OK {
		t.Fatalf("register envelope not OK: %+v", regEnv.Error)
	}

	var reg abi.Registration
	if err := json.Unmarshal(regEnv.Result, &reg); err != nil {
		t.Fatalf("failed to unmarshal registration result: %v", err)
	}

	if reg.SchemaVersion != 10 {
		t.Errorf("expected schema_version 10, got %d", reg.SchemaVersion)
	}
	if !reg.Capabilities.RequestInterceptor {
		t.Errorf("expected request_interceptor true")
	}
	if !reg.Capabilities.RequestInterceptorOmitBodyBefore {
		t.Errorf("expected request_interceptor_omit_body_before true")
	}
	if reg.Capabilities.RequestInterceptorOmitBodyAfter {
		t.Errorf("expected request_interceptor_omit_body_after false when trace_enabled: true")
	}
	if !reg.Capabilities.RequestLifecyclePlugin {
		t.Errorf("expected request_lifecycle_plugin true")
	}
	if !reg.Capabilities.UsagePlugin {
		t.Errorf("expected usage_plugin true")
	}
	if !reg.Capabilities.ManagementAPI {
		t.Errorf("expected management_api true")
	}
	if reg.Metadata.Name != "cpa-cache-tracker" {
		t.Errorf("expected metadata.name cpa-cache-tracker, got %s", reg.Metadata.Name)
	}

	// =========================================================================
	// 2. management.register
	// =========================================================================
	mgmtRegRespBytes, err := d.HandleCall(abi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("management.register call failed: %v", err)
	}
	var mgmtRegEnv abi.Envelope
	if err := json.Unmarshal(mgmtRegRespBytes, &mgmtRegEnv); err != nil {
		t.Fatalf("failed to unmarshal management.register envelope: %v", err)
	}
	if !mgmtRegEnv.OK {
		t.Fatalf("management.register envelope not OK: %+v", mgmtRegEnv.Error)
	}
	var mgmtReg abi.ManagementRegistrationResponse
	if err := json.Unmarshal(mgmtRegEnv.Result, &mgmtReg); err != nil {
		t.Fatalf("failed to unmarshal management registration result: %v", err)
	}

	if len(mgmtReg.Resources) == 0 || mgmtReg.Resources[0].Path != "/panel" {
		t.Errorf("expected /panel resource route, got %+v", mgmtReg.Resources)
	}

	expectedRoutes := map[string]bool{
		"/plugins/cpa-cache-tracker/status":       false,
		"/plugins/cpa-cache-tracker/summary":      false,
		"/plugins/cpa-cache-tracker/records":      false,
		"/plugins/cpa-cache-tracker/records/diff": false,
		"/plugins/cpa-cache-tracker/purge":        false,
	}
	for _, r := range mgmtReg.Routes {
		if _, ok := expectedRoutes[r.Path]; ok {
			expectedRoutes[r.Path] = true
		}
	}
	for routePath, found := range expectedRoutes {
		if !found {
			t.Errorf("expected route %s not found in registration", routePath)
		}
	}

	// Test GET /panel via management.handle
	panelResp := callManagement(t, d, http.MethodGet, "/v0/resource/plugins/cpa-cache-tracker/panel", nil)
	if panelResp.StatusCode != http.StatusOK {
		t.Errorf("expected panel status 200, got %d", panelResp.StatusCode)
	}
	if len(panelResp.Body) == 0 {
		t.Errorf("expected non-empty panel HTML body")
	}

	// Test GET /status via management.handle
	statusResp := callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/status", nil)
	if statusResp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", statusResp.StatusCode)
	}
	var statusObj storage.StatusResponse
	if err := json.Unmarshal(statusResp.Body, &statusObj); err != nil {
		t.Fatalf("failed to unmarshal status body: %v", err)
	}
	if !statusObj.TraceEnabled || statusObj.OmitBodyAfter {
		t.Errorf("expected trace_enabled=true and omit_body_after=false, got trace_enabled=%v, omit_body_after=%v",
			statusObj.TraceEnabled, statusObj.OmitBodyAfter)
	}

	// =========================================================================
	// 3. Simulated two-turn conversation:
	//    Turn 1: Cold start
	//    Turn 2: Modified tools -> tools_changed
	// =========================================================================

	// Turn 1 intercept_after
	turn1Body := []byte(`{
		"messages": [
			{"role": "system", "content": "You are a helpful coding assistant."},
			{"role": "user", "content": "Hello world"}
		],
		"tools": [
			{"type": "function", "function": {"name": "fetch_file", "parameters": {"type": "object"}}}
		]
	}`)
	interceptReq1 := abi.RequestInterceptRequest{
		RequestID:    "req-turn-1",
		TraceID:      "trace-turn-1",
		SourceFormat: "openai_chat",
		ToFormat:     "openai_chat",
		Model:        "gpt-4o",
		Body:         turn1Body,
		Metadata: map[string]any{
			"selected_auth_id":     "auth-user-1",
			"canonical_session_id": "sess-turn-conv",
		},
	}
	interceptBytes1, _ := json.Marshal(interceptReq1)
	if _, err := d.HandleCall(abi.MethodRequestInterceptAfter, interceptBytes1); err != nil {
		t.Fatalf("turn 1 intercept_after failed: %v", err)
	}

	// Turn 1 usage.handle
	usageReq1 := abi.UsageRecord{
		Generate:  true,
		RequestID: "req-turn-1",
		TraceID:   "trace-turn-1",
		AuthID:    "auth-user-1",
		Model:     "gpt-4o",
		Provider:  "openai",
		Detail: abi.UsageDetail{
			InputTokens:     100,
			OutputTokens:    25,
			CacheReadTokens: 0,
			TotalTokens:     125,
		},
		Latency: 500 * time.Millisecond,
		TTFT:    100 * time.Millisecond,
	}
	usageBytes1, _ := json.Marshal(usageReq1)
	if _, err := d.HandleCall(abi.MethodUsageHandle, usageBytes1); err != nil {
		t.Fatalf("turn 1 usage.handle failed: %v", err)
	}

	// Turn 1 request.complete
	compReq1 := abi.RequestCompletion{
		RequestID: "req-turn-1",
		TraceID:   "trace-turn-1",
		Outcome:   abi.RequestCompletionSucceeded,
	}
	compBytes1, _ := json.Marshal(compReq1)
	if _, err := d.HandleCall(abi.MethodRequestComplete, compBytes1); err != nil {
		t.Fatalf("turn 1 request.complete failed: %v", err)
	}

	// Verify Turn 1 record in storage via management records
	recordsResp1 := callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/records", nil)
	var recs1 storage.RecordsResponse
	if err := json.Unmarshal(recordsResp1.Body, &recs1); err != nil {
		t.Fatalf("failed to unmarshal records: %v", err)
	}
	if recs1.Total != 1 {
		t.Fatalf("expected 1 record after turn 1, got %d", recs1.Total)
	}
	turn1Rec := recs1.Items[0]
	if turn1Rec.MissReason != "cold_start" {
		t.Errorf("expected turn 1 miss_reason cold_start, got %s", turn1Rec.MissReason)
	}

	// Turn 2 intercept_after with changed tools
	turn2Body := []byte(`{
		"messages": [
			{"role": "system", "content": "You are a helpful coding assistant."},
			{"role": "user", "content": "Hello world"},
			{"role": "assistant", "content": "Hello! How can I help you today?"},
			{"role": "user", "content": "Can you check my git repo?"}
		],
		"tools": [
			{"type": "function", "function": {"name": "fetch_file_v2", "parameters": {"type": "object"}}}
		]
	}`)
	interceptReq2 := abi.RequestInterceptRequest{
		RequestID:    "req-turn-2",
		TraceID:      "trace-turn-2",
		SourceFormat: "openai_chat",
		ToFormat:     "openai_chat",
		Model:        "gpt-4o",
		Body:         turn2Body,
		Metadata: map[string]any{
			"selected_auth_id":     "auth-user-1",
			"canonical_session_id": "sess-turn-conv",
		},
	}
	interceptBytes2, _ := json.Marshal(interceptReq2)
	if _, err := d.HandleCall(abi.MethodRequestInterceptAfter, interceptBytes2); err != nil {
		t.Fatalf("turn 2 intercept_after failed: %v", err)
	}

	// Turn 2 usage.handle (miss due to tools changed)
	usageReq2 := abi.UsageRecord{
		Generate:  true,
		RequestID: "req-turn-2",
		TraceID:   "trace-turn-2",
		AuthID:    "auth-user-1",
		Model:     "gpt-4o",
		Provider:  "openai",
		Detail: abi.UsageDetail{
			InputTokens:     200,
			OutputTokens:    50,
			CacheReadTokens: 0,
			TotalTokens:     250,
		},
		Latency: 600 * time.Millisecond,
		TTFT:    120 * time.Millisecond,
	}
	usageBytes2, _ := json.Marshal(usageReq2)
	if _, err := d.HandleCall(abi.MethodUsageHandle, usageBytes2); err != nil {
		t.Fatalf("turn 2 usage.handle failed: %v", err)
	}

	// Turn 2 request.complete
	compReq2 := abi.RequestCompletion{
		RequestID: "req-turn-2",
		TraceID:   "trace-turn-2",
		Outcome:   abi.RequestCompletionSucceeded,
	}
	compBytes2, _ := json.Marshal(compReq2)
	if _, err := d.HandleCall(abi.MethodRequestComplete, compBytes2); err != nil {
		t.Fatalf("turn 2 request.complete failed: %v", err)
	}

	// Verify Turn 2 record and diff
	recordsResp2 := callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/records", nil)
	var recs2 storage.RecordsResponse
	if err := json.Unmarshal(recordsResp2.Body, &recs2); err != nil {
		t.Fatalf("failed to unmarshal records: %v", err)
	}
	if recs2.Total != 2 {
		t.Fatalf("expected 2 records after turn 2, got %d", recs2.Total)
	}
	// Records are returned newest first
	turn2Rec := recs2.Items[0]
	if turn2Rec.MissReason != "tools_changed" {
		t.Errorf("expected turn 2 miss_reason tools_changed, got %s", turn2Rec.MissReason)
	}
	if turn2Rec.PrevID != turn1Rec.ID {
		t.Errorf("expected turn 2 prev_id %s, got %s", turn1Rec.ID, turn2Rec.PrevID)
	}

	// Verify Diff endpoint via management.handle
	diffResp := callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/records/diff?id="+turn2Rec.ID, nil)
	if diffResp.StatusCode != http.StatusOK {
		t.Errorf("expected diff status 200, got %d: %s", diffResp.StatusCode, string(diffResp.Body))
	}
	var diffObj storage.DiffResponse
	if err := json.Unmarshal(diffResp.Body, &diffObj); err != nil {
		t.Fatalf("failed to unmarshal diff response: %v", err)
	}
	if diffObj.Comparison.MissReason != "tools_changed" {
		t.Errorf("expected diff comparison miss_reason tools_changed, got %s", diffObj.Comparison.MissReason)
	}
	foundToolsChanged := false
	for _, f := range diffObj.Comparison.ChangedFields {
		if f == "tools_hash" {
			foundToolsChanged = true
			break
		}
	}
	if !foundToolsChanged {
		t.Errorf("expected tools_hash in changed_fields, got %+v", diffObj.Comparison.ChangedFields)
	}

	// Verify Summary endpoint
	summaryResp := callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/summary", nil)
	if summaryResp.StatusCode != http.StatusOK {
		t.Errorf("expected summary status 200, got %d", summaryResp.StatusCode)
	}
	var summaryObj storage.SummaryResponse
	if err := json.Unmarshal(summaryResp.Body, &summaryObj); err != nil {
		t.Fatalf("failed to unmarshal summary response: %v", err)
	}
	if summaryObj.Totals.Requests != 2 {
		t.Errorf("expected 2 total requests in summary, got %d", summaryObj.Totals.Requests)
	}
	if summaryObj.Totals.MissRequests != 2 {
		t.Errorf("expected 2 miss requests in summary, got %d", summaryObj.Totals.MissRequests)
	}

	// =========================================================================
	// 4. plugin.reconfigure with trace_enabled: false (Approach B)
	// =========================================================================
	reconfYAML := fmt.Sprintf(`
data_dir: %q
trace_enabled: false
retention_days: 14
max_disk_mb: 200
max_message_hashes: 512
`, tempDir)

	reconfReqBytes, _ := json.Marshal(abi.LifecycleRequest{
		ConfigYAML:    []byte(reconfYAML),
		SchemaVersion: 10,
	})

	reconfRespBytes, err := d.HandleCall(abi.MethodPluginReconfigure, reconfReqBytes)
	if err != nil {
		t.Fatalf("plugin.reconfigure call failed: %v", err)
	}

	var reconfEnv abi.Envelope
	if err := json.Unmarshal(reconfRespBytes, &reconfEnv); err != nil {
		t.Fatalf("failed to unmarshal reconfigure envelope: %v", err)
	}
	if !reconfEnv.OK {
		t.Fatalf("reconfigure envelope not OK: %+v", reconfEnv.Error)
	}

	var reconfReg abi.Registration
	if err := json.Unmarshal(reconfEnv.Result, &reconfReg); err != nil {
		t.Fatalf("failed to unmarshal reconfigure registration: %v", err)
	}

	// CRITICAL Approach B check: RequestInterceptorOmitBodyAfter must be true when trace_enabled: false
	if !reconfReg.Capabilities.RequestInterceptorOmitBodyAfter {
		t.Errorf("expected RequestInterceptorOmitBodyAfter == true after reconfigure with trace_enabled: false")
	}

	// Check status reflects trace_enabled: false and omit_body_after: true
	statusRespAfter := callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/status", nil)
	var statusAfter storage.StatusResponse
	if err := json.Unmarshal(statusRespAfter.Body, &statusAfter); err != nil {
		t.Fatalf("failed to unmarshal status: %v", err)
	}
	if statusAfter.TraceEnabled || !statusAfter.OmitBodyAfter {
		t.Errorf("expected trace_enabled=false and omit_body_after=true, got trace_enabled=%v, omit_body_after=%v",
			statusAfter.TraceEnabled, statusAfter.OmitBodyAfter)
	}

	// Send metadata-only intercept_after (no body)
	metaInterceptReq := abi.RequestInterceptRequest{
		RequestID:    "req-meta-only",
		TraceID:      "trace-meta-only",
		SourceFormat: "claude-messages",
		ToFormat:     "claude-messages",
		Model:        "claude-3-5-sonnet",
		Body:         nil, // Body omitted by host!
		Metadata: map[string]any{
			"selected_auth_id":     "auth-meta-1",
			"canonical_session_id": "sess-meta",
		},
	}
	metaBytes, _ := json.Marshal(metaInterceptReq)
	if _, err := d.HandleCall(abi.MethodRequestInterceptAfter, metaBytes); err != nil {
		t.Fatalf("metadata-only intercept_after failed: %v", err)
	}

	// Send usage with cache read
	metaUsage := abi.UsageRecord{
		Generate:  true,
		RequestID: "req-meta-only",
		TraceID:   "trace-meta-only",
		AuthID:    "auth-meta-1",
		Model:     "claude-3-5-sonnet",
		Provider:  "anthropic",
		Detail: abi.UsageDetail{
			InputTokens:         50,
			CacheReadTokens:     100,
			CacheCreationTokens: 0,
			OutputTokens:        30,
		},
	}
	metaUsageBytes, _ := json.Marshal(metaUsage)
	if _, err := d.HandleCall(abi.MethodUsageHandle, metaUsageBytes); err != nil {
		t.Fatalf("metadata-only usage.handle failed: %v", err)
	}

	// Verify metadata-only record preserved ToFormat and AuthID
	diffRespMeta := callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/records?limit=1", nil)
	var recsMeta storage.RecordsResponse
	if err := json.Unmarshal(diffRespMeta.Body, &recsMeta); err != nil {
		t.Fatalf("failed to unmarshal records: %v", err)
	}
	if len(recsMeta.Items) == 0 {
		t.Fatalf("expected at least 1 record")
	}
	metaRec := recsMeta.Items[0]
	if metaRec.AuthID != "auth-meta-1" {
		t.Errorf("expected auth-meta-1, got %s", metaRec.AuthID)
	}
	if metaRec.Tokens.CacheRead != 100 {
		t.Errorf("expected cache_read=100, got %d", metaRec.Tokens.CacheRead)
	}
	if metaRec.HasFingerprint {
		t.Errorf("expected HasFingerprint=false for metadata-only turn")
	}

	// =========================================================================
	// 5. Compact and internal_subcall exclusion from main KPIs
	// =========================================================================
	// Internal subcall
	subcallIntercept := abi.RequestInterceptRequest{
		RequestID: "req-subcall",
		TraceID:   "trace-subcall",
		Model:     "gpt-4o",
		Metadata: map[string]any{
			"model_execution_source": "plugin_host_model_callback",
		},
	}
	scBytes, _ := json.Marshal(subcallIntercept)
	_, _ = d.HandleCall(abi.MethodRequestInterceptAfter, scBytes)
	scUsageBytes, _ := json.Marshal(abi.UsageRecord{
		Generate:  true,
		RequestID: "req-subcall",
		TraceID:   "trace-subcall",
		Detail:    abi.UsageDetail{InputTokens: 500, OutputTokens: 100},
	})
	_, _ = d.HandleCall(abi.MethodUsageHandle, scUsageBytes)

	// Compact call
	compactIntercept := abi.RequestInterceptRequest{
		RequestID: "req-compact",
		TraceID:   "trace-compact",
		Model:     "gpt-4o",
		Metadata: map[string]any{
			"request_path": "/v0/responses/compact",
		},
	}
	ccBytes, _ := json.Marshal(compactIntercept)
	_, _ = d.HandleCall(abi.MethodRequestInterceptAfter, ccBytes)
	ccUsageBytes, _ := json.Marshal(abi.UsageRecord{
		Generate:  true,
		RequestID: "req-compact",
		TraceID:   "trace-compact",
		Detail:    abi.UsageDetail{InputTokens: 600, OutputTokens: 150},
	})
	_, _ = d.HandleCall(abi.MethodUsageHandle, ccUsageBytes)

	// Verify main summary excludes subcall & compact
	mainSummaryResp := callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/summary?kind=main", nil)
	var mainSum storage.SummaryResponse
	json.Unmarshal(mainSummaryResp.Body, &mainSum)
	// 3 main requests: turn1, turn2, metaRec
	if mainSum.Totals.Requests != 3 {
		t.Errorf("expected 3 requests in kind=main summary, got %d", mainSum.Totals.Requests)
	}

	// All summary includes subcall & compact
	allSummaryResp := callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/summary?kind=all", nil)
	var allSum storage.SummaryResponse
	json.Unmarshal(allSummaryResp.Body, &allSum)
	if allSum.Totals.Requests != 5 {
		t.Errorf("expected 5 requests in kind=all summary, got %d", allSum.Totals.Requests)
	}

	// =========================================================================
	// 6. plugin.shutdown: clean shutdown and idempotency
	// =========================================================================
	shutBytes, err := d.HandleCall(abi.MethodPluginShutdown, nil)
	if err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	var shutEnv abi.Envelope
	json.Unmarshal(shutBytes, &shutEnv)
	if !shutEnv.OK {
		t.Errorf("expected shutdown OK, got %+v", shutEnv)
	}

	// Second shutdown should also succeed (idempotent)
	shutBytes2, err := d.HandleCall(abi.MethodPluginShutdown, nil)
	if err != nil {
		t.Fatalf("second shutdown failed: %v", err)
	}
	var shutEnv2 abi.Envelope
	json.Unmarshal(shutBytes2, &shutEnv2)
	if !shutEnv2.OK {
		t.Errorf("expected second shutdown OK, got %+v", shutEnv2)
	}
}

func TestPluginConcurrencyRace(t *testing.T) {
	tempDir := t.TempDir()
	d := main.NewDispatcher()

	regConfigYAML := fmt.Sprintf(`
data_dir: %q
trace_enabled: true
retention_days: 14
max_disk_mb: 200
max_message_hashes: 512
`, tempDir)

	regReqBytes, _ := json.Marshal(abi.LifecycleRequest{
		ConfigYAML:    []byte(regConfigYAML),
		SchemaVersion: 10,
	})
	if _, err := d.HandleCall(abi.MethodPluginRegister, regReqBytes); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	var wg sync.WaitGroup
	workers := 10
	iterations := 20

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				traceID := fmt.Sprintf("trace-%d-%d", workerID, j)
				reqID := fmt.Sprintf("req-%d-%d", workerID, j)

				// Intercept
				interceptReq := abi.RequestInterceptRequest{
					RequestID:    reqID,
					TraceID:      traceID,
					SourceFormat: "openai_chat",
					ToFormat:     "openai_chat",
					Model:        "gpt-4o",
					Body:         []byte(`{"messages":[{"role":"user","content":"test"}]}`),
					Metadata: map[string]any{
						"selected_auth_id":     fmt.Sprintf("auth-%d", workerID),
						"canonical_session_id": fmt.Sprintf("sess-%d", workerID),
					},
				}
				b, _ := json.Marshal(interceptReq)
				_, _ = d.HandleCall(abi.MethodRequestInterceptAfter, b)

				// Usage
				usage := abi.UsageRecord{
					Generate:  true,
					RequestID: reqID,
					TraceID:   traceID,
					AuthID:    fmt.Sprintf("auth-%d", workerID),
					Model:     "gpt-4o",
					Detail: abi.UsageDetail{
						InputTokens:     100,
						CacheReadTokens: int64(j * 10),
						OutputTokens:    20,
					},
				}
				ub, _ := json.Marshal(usage)
				_, _ = d.HandleCall(abi.MethodUsageHandle, ub)

				// Complete
				comp := abi.RequestCompletion{
					RequestID: reqID,
					TraceID:   traceID,
					Outcome:   abi.RequestCompletionSucceeded,
				}
				cb, _ := json.Marshal(comp)
				_, _ = d.HandleCall(abi.MethodRequestComplete, cb)

				// Management read
				_, _ = d.HandleCall(abi.MethodManagementHandle, []byte(`{"Method":"GET","Path":"/v0/management/plugins/cpa-cache-tracker/summary"}`))
			}
		}(i)
	}

	wg.Wait()
	_, _ = d.HandleCall(abi.MethodPluginShutdown, nil)
}

func callManagement(t *testing.T, d *main.Dispatcher, method, rawPath string, body []byte) abi.ManagementResponse {
	t.Helper()
	u, err := url.Parse(rawPath)
	if err != nil {
		t.Fatalf("failed to parse path %s: %v", rawPath, err)
	}

	req := abi.ManagementRequest{
		Method: method,
		Path:   u.Path,
		Query:  u.Query(),
		Body:   body,
	}
	reqBytes, _ := json.Marshal(req)

	respBytes, err := d.HandleCall(abi.MethodManagementHandle, reqBytes)
	if err != nil {
		t.Fatalf("management call %s %s failed: %v", method, rawPath, err)
	}

	var env abi.Envelope
	if err := json.Unmarshal(respBytes, &env); err != nil {
		t.Fatalf("failed to unmarshal management envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("management envelope returned error: %+v", env.Error)
	}

	var mgmtResp abi.ManagementResponse
	if err := json.Unmarshal(env.Result, &mgmtResp); err != nil {
		t.Fatalf("failed to unmarshal management response: %v", err)
	}

	return mgmtResp
}

func TestRegistrationMatchesCPAHostExpectations(t *testing.T) {
	tempDir := t.TempDir()
	d := main.NewDispatcher()
	regConfigYAML := fmt.Sprintf(`
data_dir: %q
trace_enabled: true
retention_days: 14
max_disk_mb: 200
max_message_hashes: 512
`, tempDir)
	regReqBytes, _ := json.Marshal(abi.LifecycleRequest{
		ConfigYAML:    []byte(regConfigYAML),
		SchemaVersion: 10,
	})
	respBytes, err := d.HandleCall(abi.MethodPluginRegister, regReqBytes)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	var env abi.Envelope
	json.Unmarshal(respBytes, &env)
	t.Logf("Result: %s", string(env.Result))

	type rpcMetadata struct {
		Name             string
		Version          string
		Author           string
		GitHubRepository string
		Logo             string
	}
	type rpcCapabilities struct {
		RequestInterceptor     bool `json:"request_interceptor"`
		RequestLifecyclePlugin bool `json:"request_lifecycle_plugin"`
		UsagePlugin            bool `json:"usage_plugin"`
		ManagementAPI          bool `json:"management_api"`
	}
	type rpcRegistration struct {
		SchemaVersion uint32          `json:"schema_version"`
		Metadata      rpcMetadata     `json:"metadata"`
		Capabilities  rpcCapabilities `json:"capabilities"`
	}

	var hostReg rpcRegistration
	if err := json.Unmarshal(env.Result, &hostReg); err != nil {
		t.Fatalf("unmarshal hostReg failed: %v", err)
	}
	t.Logf("HostReg: %+v", hostReg)
	if hostReg.Metadata.Name == "" || hostReg.Metadata.Version == "" || hostReg.Metadata.Author == "" || hostReg.Metadata.GitHubRepository == "" {
		t.Errorf("Metadata incomplete: %+v", hostReg.Metadata)
	}
	if !hostReg.Capabilities.RequestInterceptor || !hostReg.Capabilities.UsagePlugin || !hostReg.Capabilities.ManagementAPI {
		t.Errorf("Capabilities incomplete: %+v", hostReg.Capabilities)
	}
}

func TestDispatchWithRecoverPanicGuard(t *testing.T) {
	// 1. Nil dispatcher returns dispatcher_nil error envelope without panicking
	respBytes, err := main.DispatchWithRecover(nil, "some.method", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var env abi.Envelope
	if err := json.Unmarshal(respBytes, &env); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if env.OK || env.Error == nil || env.Error.Code != "dispatcher_nil" {
		t.Errorf("expected dispatcher_nil envelope, got %+v", env)
	}

	// 2. Dispatcher with simulated panic in nowFn during execution is recovered
	tempDir := t.TempDir()
	d := main.NewDispatcher()
	regBytes, _ := json.Marshal(abi.LifecycleRequest{
		ConfigYAML:    []byte(fmt.Sprintf("data_dir: %q\n", tempDir)),
		SchemaVersion: 10,
	})
	if _, err := d.HandleCall(abi.MethodPluginRegister, regBytes); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	d.SetNowFn(func() time.Time {
		panic("simulated clock panic")
	})

	reqBytes, _ := json.Marshal(abi.RequestInterceptRequest{
		RequestID: "req-panic",
		TraceID:   "trace-panic",
	})
	respBytes2, err2 := main.DispatchWithRecover(d, abi.MethodRequestInterceptAfter, reqBytes)
	if err2 == nil {
		t.Fatalf("expected error from panic, got nil")
	}
	var env2 abi.Envelope
	if err := json.Unmarshal(respBytes2, &env2); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if env2.OK || env2.Error == nil || env2.Error.Code != "plugin_panic" {
		t.Errorf("expected plugin_panic error code, got %+v", env2)
	}
	if !strings.Contains(env2.Error.Message, "simulated clock panic") {
		t.Errorf("expected panic message in error, got %s", env2.Error.Message)
	}
}

func TestReconfigureInvalidDataDirClearsStore(t *testing.T) {
	tempDir := t.TempDir()
	validDir := filepath.Join(tempDir, "valid_data")
	d := main.NewDispatcher()

	regConfigYAML := fmt.Sprintf(`
data_dir: %q
trace_enabled: true
retention_days: 14
max_disk_mb: 200
max_message_hashes: 512
`, validDir)

	regReqBytes, _ := json.Marshal(abi.LifecycleRequest{
		ConfigYAML:    []byte(regConfigYAML),
		SchemaVersion: 10,
	})

	regRespBytes, err := d.HandleCall(abi.MethodPluginRegister, regReqBytes)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}
	var regEnv abi.Envelope
	json.Unmarshal(regRespBytes, &regEnv)
	if !regEnv.OK {
		t.Fatalf("register not OK: %+v", regEnv.Error)
	}
	if d.Store() == nil {
		t.Fatalf("expected store to be initialized after register")
	}

	// Create a regular file that will collide with MkdirAll on reconfigure
	invalidDirAsFile := filepath.Join(tempDir, "colliding_file")
	if err := os.WriteFile(invalidDirAsFile, []byte("i am a file"), 0600); err != nil {
		t.Fatalf("failed to create colliding file: %v", err)
	}
	// Try to create a nested directory under this regular file -> os.MkdirAll will fail!
	invalidDir := filepath.Join(invalidDirAsFile, "nested_dir")

	reconfConfigYAML := fmt.Sprintf(`
data_dir: %q
trace_enabled: true
retention_days: 14
max_disk_mb: 200
max_message_hashes: 512
`, invalidDir)

	reconfReqBytes, _ := json.Marshal(abi.LifecycleRequest{
		ConfigYAML:    []byte(reconfConfigYAML),
		SchemaVersion: 10,
	})

	reconfRespBytes, err := d.HandleCall(abi.MethodPluginReconfigure, reconfReqBytes)
	if err != nil {
		t.Fatalf("reconfigure returned Go error: %v", err)
	}
	var reconfEnv abi.Envelope
	json.Unmarshal(reconfRespBytes, &reconfEnv)
	if reconfEnv.OK {
		t.Fatalf("expected reconfigure to fail on invalid data_dir, but got OK")
	}
	if reconfEnv.Error == nil || reconfEnv.Error.Code != "storage_error" {
		t.Fatalf("expected storage_error code, got %+v", reconfEnv.Error)
	}

	// CRITICAL ASSERTION: d.Store() must be nil (no stale pointer to closed store)
	if d.Store() != nil {
		t.Errorf("expected d.Store() == nil after failed reconfigure, but got non-nil pointer")
	}

	// Subsequent calls must fail gracefully without panicking
	usageBytes, _ := json.Marshal(abi.UsageRecord{
		Generate:  true,
		RequestID: "req-fail",
		TraceID:   "trace-fail",
		Detail:    abi.UsageDetail{InputTokens: 10},
	})
	usageRespBytes, err := d.HandleCall(abi.MethodUsageHandle, usageBytes)
	if err != nil {
		t.Fatalf("usage.handle returned unexpected Go error: %v", err)
	}
	var usageEnv abi.Envelope
	json.Unmarshal(usageRespBytes, &usageEnv)
	if usageEnv.OK || usageEnv.Error == nil || usageEnv.Error.Code != "store_unavailable" {
		t.Errorf("expected store_unavailable error envelope, got %+v", usageEnv)
	}

	mgmtBytes, _ := json.Marshal(abi.ManagementRequest{
		Method: "GET",
		Path:   "/v0/management/plugins/cpa-cache-tracker/status",
	})
	mgmtRespBytes, err := d.HandleCall(abi.MethodManagementHandle, mgmtBytes)
	if err != nil {
		t.Fatalf("management.handle returned unexpected Go error: %v", err)
	}
	var mgmtEnv abi.Envelope
	json.Unmarshal(mgmtRespBytes, &mgmtEnv)
	if mgmtEnv.OK || mgmtEnv.Error == nil || mgmtEnv.Error.Code != "ui_unavailable" {
		t.Errorf("expected ui_unavailable error envelope, got %+v", mgmtEnv)
	}
}

func TestUsageFailedDefaultStatusCode500(t *testing.T) {
	tempDir := t.TempDir()
	d := main.NewDispatcher()

	regConfigYAML := fmt.Sprintf(`
data_dir: %q
trace_enabled: true
retention_days: 14
max_disk_mb: 200
max_message_hashes: 512
`, tempDir)

	regReqBytes, _ := json.Marshal(abi.LifecycleRequest{
		ConfigYAML:    []byte(regConfigYAML),
		SchemaVersion: 10,
	})
	if _, err := d.HandleCall(abi.MethodPluginRegister, regReqBytes); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// Intercept
	interceptBytes, _ := json.Marshal(abi.RequestInterceptRequest{
		RequestID: "req-err-zero",
		TraceID:   "trace-err-zero",
		Model:     "gpt-4o",
	})
	d.HandleCall(abi.MethodRequestInterceptAfter, interceptBytes)

	// Usage with Failed: true and StatusCode: 0 (unspecified)
	usageBytes, _ := json.Marshal(abi.UsageRecord{
		Generate:  true,
		RequestID: "req-err-zero",
		TraceID:   "trace-err-zero",
		Model:     "gpt-4o",
		Failed:    true,
		Failure:   abi.UsageFailure{StatusCode: 0, Body: "upstream connection dropped"},
		Detail:    abi.UsageDetail{InputTokens: 50},
	})
	d.HandleCall(abi.MethodUsageHandle, usageBytes)

	// Fetch record and assert StatusCode was defaulted to 500
	recordsResp := callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/records?limit=1", nil)
	var recs storage.RecordsResponse
	if err := json.Unmarshal(recordsResp.Body, &recs); err != nil {
		t.Fatalf("unmarshal records failed: %v", err)
	}
	if len(recs.Items) != 1 {
		t.Fatalf("expected 1 record, got %d", len(recs.Items))
	}
	if recs.Items[0].StatusCode != 500 {
		t.Errorf("expected StatusCode 500 when failure StatusCode was 0, got %d", recs.Items[0].StatusCode)
	}
	if !recs.Items[0].Failed {
		t.Errorf("expected Failed == true")
	}
}

func TestUsageSessionIDPreservation(t *testing.T) {
	tempDir := t.TempDir()
	d := main.NewDispatcher()

	regConfigYAML := fmt.Sprintf(`
data_dir: %q
trace_enabled: true
retention_days: 14
max_disk_mb: 200
max_message_hashes: 512
`, tempDir)

	regReqBytes, _ := json.Marshal(abi.LifecycleRequest{
		ConfigYAML:    []byte(regConfigYAML),
		SchemaVersion: 10,
	})
	if _, err := d.HandleCall(abi.MethodPluginRegister, regReqBytes); err != nil {
		t.Fatalf("register failed: %v", err)
	}

	// Case 1: No intercept at all (att == nil).
	// UsageRecord has non-empty SessionID and ParentSessionID.
	usage1Bytes, _ := json.Marshal(abi.UsageRecord{
		Generate:        true,
		RequestID:       "req-no-intercept",
		TraceID:         "trace-no-intercept",
		Model:           "gpt-4o",
		SessionID:       "sess-fallback-1",
		ParentSessionID: "sess-parent-1",
		Detail:          abi.UsageDetail{InputTokens: 100},
	})
	if _, err := d.HandleCall(abi.MethodUsageHandle, usage1Bytes); err != nil {
		t.Fatalf("usage1 failed: %v", err)
	}

	// Case 2: Intercept exists, but att.CanonicalSessionID is empty.
	// UsageRecord has non-empty SessionID and ParentSessionID, which must NOT be wiped by empty intercept metadata.
	intercept2Bytes, _ := json.Marshal(abi.RequestInterceptRequest{
		RequestID: "req-empty-intercept-sess",
		TraceID:   "trace-empty-intercept-sess",
		Model:     "gpt-4o",
		Metadata:  map[string]any{}, // no canonical_session_id
	})
	if _, err := d.HandleCall(abi.MethodRequestInterceptAfter, intercept2Bytes); err != nil {
		t.Fatalf("intercept2 failed: %v", err)
	}

	usage2Bytes, _ := json.Marshal(abi.UsageRecord{
		Generate:        true,
		RequestID:       "req-empty-intercept-sess",
		TraceID:         "trace-empty-intercept-sess",
		Model:           "gpt-4o",
		SessionID:       "sess-fallback-2",
		ParentSessionID: "sess-parent-2",
		Detail:          abi.UsageDetail{InputTokens: 100},
	})
	if _, err := d.HandleCall(abi.MethodUsageHandle, usage2Bytes); err != nil {
		t.Fatalf("usage2 failed: %v", err)
	}

	// Case 3: Intercept has canonical_session_id, which should overwrite UsageRecord's SessionID.
	intercept3Bytes, _ := json.Marshal(abi.RequestInterceptRequest{
		RequestID: "req-with-canonical",
		TraceID:   "trace-with-canonical",
		Model:     "gpt-4o",
		Metadata: map[string]any{
			"canonical_session_id": "sess-canonical-override",
		},
	})
	if _, err := d.HandleCall(abi.MethodRequestInterceptAfter, intercept3Bytes); err != nil {
		t.Fatalf("intercept3 failed: %v", err)
	}

	usage3Bytes, _ := json.Marshal(abi.UsageRecord{
		Generate:        true,
		RequestID:       "req-with-canonical",
		TraceID:         "trace-with-canonical",
		Model:           "gpt-4o",
		SessionID:       "sess-initial-3",
		ParentSessionID: "sess-parent-3",
		Detail:          abi.UsageDetail{InputTokens: 100},
	})
	if _, err := d.HandleCall(abi.MethodUsageHandle, usage3Bytes); err != nil {
		t.Fatalf("usage3 failed: %v", err)
	}

	// Query records and verify each case
	recordsResp := callManagement(t, d, http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/records?limit=10", nil)
	var recs storage.RecordsResponse
	if err := json.Unmarshal(recordsResp.Body, &recs); err != nil {
		t.Fatalf("unmarshal records failed: %v", err)
	}
	if len(recs.Items) != 3 {
		t.Fatalf("expected 3 records, got %d", len(recs.Items))
	}

	// Build map by RequestID
	byReq := make(map[string]storage.RecordSummary)
	for _, item := range recs.Items {
		byReq[item.RequestID] = item
	}

	// Verify Case 1
	item1, ok := byReq["req-no-intercept"]
	if !ok {
		t.Fatalf("missing item1")
	}
	if item1.SessionID != "sess-fallback-1" {
		t.Errorf("case 1: expected SessionID 'sess-fallback-1', got %q", item1.SessionID)
	}
	if item1.RecordKind != "main" {
		t.Errorf("case 1: expected RecordKind 'main', got %q", item1.RecordKind)
	}

	// Verify Case 2
	item2, ok := byReq["req-empty-intercept-sess"]
	if !ok {
		t.Fatalf("missing item2")
	}
	if item2.SessionID != "sess-fallback-2" {
		t.Errorf("case 2: expected SessionID 'sess-fallback-2', got %q", item2.SessionID)
	}

	// Verify Case 3
	item3, ok := byReq["req-with-canonical"]
	if !ok {
		t.Fatalf("missing item3")
	}
	if item3.SessionID != "sess-canonical-override" {
		t.Errorf("case 3: expected SessionID 'sess-canonical-override', got %q", item3.SessionID)
	}
}

func TestResolveDataDirCrossPlatform(t *testing.T) {
	cases := []struct {
		name string
		dir  string
		cwd  string
		home string
		want string
	}{
		{
			name: "empty dir defaults on non-root unix cwd",
			dir:  "   ",
			cwd:  "/home/user/.cli-proxy-api",
			home: "/home/user",
			want: ".cache_tracker_data",
		},
		{
			name: "unix tilde subpath",
			dir:  "~/custom_dir/cache",
			cwd:  "/",
			home: "/home/user",
			want: "/home/user/custom_dir/cache",
		},
		{
			name: "windows tilde backslash subpath",
			dir:  `~\custom_dir\cache`,
			cwd:  `C:\`,
			home: `C:\Users\user`,
			want: `C:\Users\user\custom_dir\cache`,
		},
		{
			name: "bare tilde expands to home",
			dir:  "~",
			cwd:  "/tmp",
			home: "/home/user",
			want: "/home/user",
		},
		{
			name: "unix root cwd falls back to home .cli-proxy-api",
			dir:  ".cache_tracker_data",
			cwd:  "/",
			home: "/home/cpa",
			want: "/home/cpa/.cli-proxy-api/.cache_tracker_data",
		},
		{
			name: "empty cwd falls back to home .cli-proxy-api",
			dir:  ".cache_tracker_data",
			cwd:  "",
			home: "/home/cpa",
			want: "/home/cpa/.cli-proxy-api/.cache_tracker_data",
		},
		{
			name: "windows drive backslash root cwd falls back to home .cli-proxy-api",
			dir:  ".cache_tracker_data",
			cwd:  `C:\`,
			home: `C:\Users\user`,
			want: `C:\Users\user\.cli-proxy-api\.cache_tracker_data`,
		},
		{
			name: "windows drive forward-slash root cwd falls back to home .cli-proxy-api",
			dir:  ".cache_tracker_data",
			cwd:  "C:/",
			home: `C:\Users\user`,
			want: `C:\Users\user\.cli-proxy-api\.cache_tracker_data`,
		},
		{
			name: "windows secondary drive root cwd falls back to home .cli-proxy-api",
			dir:  ".cache_tracker_data",
			cwd:  `D:\`,
			home: `C:\Users\user`,
			want: `C:\Users\user\.cli-proxy-api\.cache_tracker_data`,
		},
		{
			name: "windows system32 cwd falls back to home .cli-proxy-api",
			dir:  ".cache_tracker_data",
			cwd:  `C:\Windows\System32`,
			home: `C:\Users\user`,
			want: `C:\Users\user\.cli-proxy-api\.cache_tracker_data`,
		},
		{
			name: "windows syswow64 lowercase cwd falls back to home .cli-proxy-api",
			dir:  ".cache_tracker_data",
			cwd:  `c:\windows\syswow64`,
			home: `C:\Users\user`,
			want: `C:\Users\user\.cli-proxy-api\.cache_tracker_data`,
		},
		{
			name: "normal non-root unix cwd keeps relative cleaned",
			dir:  "./.cache_tracker_data/",
			cwd:  "/srv/cpa",
			home: "/home/cpa",
			want: ".cache_tracker_data",
		},
		{
			name: "normal non-root windows cwd keeps relative cleaned",
			dir:  "./.cache_tracker_data",
			cwd:  `D:\apps\cpa`,
			home: `C:\Users\user`,
			want: ".cache_tracker_data",
		},
		{
			name: "windows backslash absolute path preserved",
			dir:  `C:\data\cache`,
			cwd:  `C:\Windows\System32`,
			home: `C:\Users\user`,
			want: `C:\data\cache`,
		},
		{
			name: "windows forward-slash absolute path preserved",
			dir:  "D:/cache",
			cwd:  "C:/",
			home: `C:\Users\user`,
			want: "D:/cache",
		},
		{
			name: "windows UNC path preserved",
			dir:  `\\nas\share\cache`,
			cwd:  `C:\`,
			home: `C:\Users\user`,
			want: `\\nas\share\cache`,
		},
		{
			name: "unix absolute path preserved",
			dir:  "/var/lib/cpa/cache",
			cwd:  "/",
			home: "/home/cpa",
			want: "/var/lib/cpa/cache",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := main.ResolveDataDirForOS(tc.dir, tc.cwd, tc.home)
			if got != tc.want {
				t.Errorf("ResolveDataDirForOS(%q, %q, %q) = %q, want %q", tc.dir, tc.cwd, tc.home, got, tc.want)
			}
		})
	}
}
