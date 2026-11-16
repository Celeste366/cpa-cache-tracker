package abi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseConfigYAMLAndDefaults(t *testing.T) {
	cfg := ParseConfigYAML(nil)
	if !cfg.TraceEnabled || cfg.RetentionDays != 14 || cfg.MaxDiskMB != 200 || cfg.MaxMessageHashes != 512 || cfg.DataDir != ".cache_tracker_data" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	raw := []byte("enabled: true\npriority: 0\ntrace_enabled: false\nretention_days: 7\nmax_disk_mb: 50\nmax_message_hashes: 128\ndata_dir: \"/tmp/ct\"\n# comment\n")
	cfg = ParseConfigYAML(raw)
	if cfg.TraceEnabled || cfg.RetentionDays != 7 || cfg.MaxDiskMB != 50 || cfg.MaxMessageHashes != 128 || cfg.DataDir != "/tmp/ct" {
		t.Fatalf("unexpected parsed config: %+v", cfg)
	}
}

func TestUsageRecordJSONCompatibility(t *testing.T) {
	sample := `{"RequestID":"req-1","TraceID":"abcd1234","Provider":"claude","ExecutorType":"ClaudeExecutor","Model":"claude-sonnet","Alias":"sonnet","SessionID":"sess-1","AuthID":"auth-1","Stream":true,"Generate":true,"RequestedAt":"2026-11-17T00:00:00Z","Failed":false,"Detail":{"InputTokens":100,"OutputTokens":20,"CachedTokens":80,"CacheReadTokens":80,"CacheCreationTokens":0,"TotalTokens":200}}`
	var rec UsageRecord
	if err := json.Unmarshal([]byte(sample), &rec); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if rec.TraceID != "abcd1234" || rec.Detail.CacheReadTokens != 80 || !rec.Generate {
		t.Fatalf("unexpected usage record: %+v", rec)
	}
}

func TestParseConfigYAMLEdgeCases(t *testing.T) {
	// Controller ruling 2: retention_days: 0 preserved as 0; negative falls back to 14.
	// max_disk_mb <= 0 falls back to 200; max_message_hashes <= 0 falls back to 512.
	yamlZero := []byte(`
retention_days: 0
max_disk_mb: 0
max_message_hashes: 0
`)
	cfg := ParseConfigYAML(yamlZero)
	if cfg.RetentionDays != 0 {
		t.Fatalf("expected retention_days 0, got %d", cfg.RetentionDays)
	}
	if cfg.MaxDiskMB != 200 {
		t.Fatalf("expected max_disk_mb 200 fallback, got %d", cfg.MaxDiskMB)
	}
	if cfg.MaxMessageHashes != 512 {
		t.Fatalf("expected max_message_hashes 512 fallback, got %d", cfg.MaxMessageHashes)
	}

	yamlNeg := []byte(`
retention_days: -5
max_disk_mb: -100
max_message_hashes: -10
`)
	cfgNeg := ParseConfigYAML(yamlNeg)
	if cfgNeg.RetentionDays != 14 {
		t.Fatalf("expected retention_days 14 fallback for negative, got %d", cfgNeg.RetentionDays)
	}
	if cfgNeg.MaxDiskMB != 200 {
		t.Fatalf("expected max_disk_mb 200 fallback for negative, got %d", cfgNeg.MaxDiskMB)
	}
	if cfgNeg.MaxMessageHashes != 512 {
		t.Fatalf("expected max_message_hashes 512 fallback for negative, got %d", cfgNeg.MaxMessageHashes)
	}

	// Quotes, comments, and boolean formats
	yamlComplex := []byte(`
# Header comment
data_dir: '/custom/path with spaces' # inline comment
trace_enabled: yes # boolean yes
`)
	cfgComplex := ParseConfigYAML(yamlComplex)
	if cfgComplex.DataDir != "/custom/path with spaces" {
		t.Fatalf("expected custom path with spaces, got %q", cfgComplex.DataDir)
	}
	if !cfgComplex.TraceEnabled {
		t.Fatalf("expected trace_enabled true for 'yes'")
	}

	yamlOff := []byte(`
trace_enabled: "off"
`)
	cfgOff := ParseConfigYAML(yamlOff)
	if cfgOff.TraceEnabled {
		t.Fatalf("expected trace_enabled false for 'off'")
	}
}

func TestEnvelopes(t *testing.T) {
	// Test OKEnvelope with data
	type dummy struct {
		Name string `json:"name"`
	}
	raw, err := OKEnvelope(dummy{Name: "tracker"})
	if err != nil {
		t.Fatalf("OKEnvelope failed: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("Unmarshal Envelope failed: %v", err)
	}
	if !env.OK || env.Error != nil {
		t.Fatalf("unexpected envelope: %+v", env)
	}
	if !strings.Contains(string(env.Result), `"name":"tracker"`) {
		t.Fatalf("unexpected result: %s", string(env.Result))
	}

	// Test OKEnvelope with nil
	rawNil, err := OKEnvelope(nil)
	if err != nil {
		t.Fatalf("OKEnvelope(nil) failed: %v", err)
	}
	var envNil Envelope
	if err := json.Unmarshal(rawNil, &envNil); err != nil {
		t.Fatalf("Unmarshal nil Envelope failed: %v", err)
	}
	if !envNil.OK || string(envNil.Result) != "{}" {
		t.Fatalf("unexpected nil result: %s", string(envNil.Result))
	}

	// Test ErrorEnvelope
	errBytes := ErrorEnvelope("rate_limit", "too many requests", http.StatusTooManyRequests)
	var errEnv Envelope
	if err := json.Unmarshal(errBytes, &errEnv); err != nil {
		t.Fatalf("Unmarshal ErrorEnvelope failed: %v", err)
	}
	if errEnv.OK || errEnv.Error == nil {
		t.Fatalf("expected OK=false and non-nil error: %+v", errEnv)
	}
	if errEnv.Error.Code != "rate_limit" || errEnv.Error.HTTPStatus != 429 || errEnv.Error.Message != "too many requests" {
		t.Fatalf("unexpected error payload: %+v", errEnv.Error)
	}
	if errEnv.Error.Error() != "too many requests" {
		t.Fatalf("unexpected Error() string: %s", errEnv.Error.Error())
	}
}

func TestRegistrationJSONTags(t *testing.T) {
	reg := Registration{
		SchemaVersion: SchemaVersion,
		Metadata: Metadata{
			Name:    "cpa-cache-tracker",
			Version: "1.0.0",
			ConfigFields: []ConfigField{
				{
					Name:        "trace_enabled",
					Type:        ConfigFieldTypeBoolean,
					Default:     true,
					Description: "Record content-free request hashes for cache miss diagnosis",
				},
			},
		},
		Capabilities: Capabilities{
			UsagePlugin:                      true,
			RequestInterceptor:               true,
			RequestInterceptorOmitBodyBefore: true,
			RequestInterceptorOmitBodyAfter:  false,
			RequestLifecyclePlugin:           true,
			ManagementAPI:                    true,
		},
	}

	b, err := json.Marshal(reg)
	if err != nil {
		t.Fatalf("marshal registration failed: %v", err)
	}
	s := string(b)
	expectedSubstrings := []string{
		`"schema_version":10`,
		`"name":"cpa-cache-tracker"`,
		`"trace_enabled"`,
		`"type":"boolean"`,
		`"usage_plugin":true`,
		`"request_interceptor":true`,
		`"request_interceptor_omit_body_before":true`,
		`"request_interceptor_omit_body_after":false`,
		`"request_lifecycle_plugin":true`,
		`"management_api":true`,
	}
	for _, sub := range expectedSubstrings {
		if !strings.Contains(s, sub) {
			t.Errorf("registration JSON missing %s in: %s", sub, s)
		}
	}
}

func TestRequestInterceptAndCompletion(t *testing.T) {
	req := RequestInterceptRequest{
		RequestID:      "r-1",
		TraceID:        "t-1",
		SourceFormat:   "openai",
		ToFormat:       "claude",
		Model:          "claude-3-5-sonnet",
		RequestedModel: "gpt-4o",
		Stream:         true,
		Headers:        http.Header{"X-Test": []string{"val"}},
		Body:           []byte(`{"messages":[]}`),
		CallerScope:    "user-1",
		Metadata:       map[string]any{"key": "val"},
	}

	rawReq, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal RequestInterceptRequest failed: %v", err)
	}
	var req2 RequestInterceptRequest
	if err := json.Unmarshal(rawReq, &req2); err != nil {
		t.Fatalf("unmarshal RequestInterceptRequest failed: %v", err)
	}
	if req2.RequestID != "r-1" || req2.Headers.Get("X-Test") != "val" {
		t.Fatalf("unexpected unmarshaled req: %+v", req2)
	}

	comp := RequestCompletion{
		RequestID:   "r-1",
		TraceID:     "t-1",
		Outcome:     RequestCompletionSucceeded,
		StatusCode:  200,
		StartedAt:   time.Now().Add(-time.Second),
		CompletedAt: time.Now(),
	}
	rawComp, err := json.Marshal(comp)
	if err != nil {
		t.Fatalf("marshal RequestCompletion failed: %v", err)
	}
	var comp2 RequestCompletion
	if err := json.Unmarshal(rawComp, &comp2); err != nil {
		t.Fatalf("unmarshal RequestCompletion failed: %v", err)
	}
	if comp2.Outcome != RequestCompletionSucceeded || comp2.StatusCode != 200 {
		t.Fatalf("unexpected comp: %+v", comp2)
	}
}

func TestManagementTypes(t *testing.T) {
	resp := ManagementRegistrationResponse{
		Routes: []ManagementRoute{
			{Method: "GET", Path: "/status", Description: "Plugin status"},
		},
		Resources: []ResourceRoute{
			{Path: "/panel", Menu: "Cache Tracker", Description: "Cache tracker panel"},
		},
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal ManagementRegistrationResponse failed: %v", err)
	}
	var resp2 ManagementRegistrationResponse
	if err := json.Unmarshal(raw, &resp2); err != nil {
		t.Fatalf("unmarshal ManagementRegistrationResponse failed: %v", err)
	}
	if len(resp2.Routes) != 1 || resp2.Routes[0].Path != "/status" {
		t.Fatalf("unexpected routes: %+v", resp2.Routes)
	}
	if len(resp2.Resources) != 1 || resp2.Resources[0].Menu != "Cache Tracker" {
		t.Fatalf("unexpected resources: %+v", resp2.Resources)
	}
}
