package ui_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/fingerprint"
	"cpa-cache-tracker/storage"
	"cpa-cache-tracker/tracker"
	"cpa-cache-tracker/ui"
)

func setupTestStore(t *testing.T, fixedNow time.Time) (*storage.Store, string, func() (abi.PluginConfig, bool)) {
	t.Helper()
	dataDir := t.TempDir()
	nowFn := func() time.Time { return fixedNow }

	cfg := abi.DefaultPluginConfig()
	cfg.DataDir = dataDir
	cfg.TraceEnabled = true

	store, err := storage.Open(dataDir, cfg, nowFn)
	if err != nil {
		t.Fatalf("storage.Open failed: %v", err)
	}

	liveCfg := cfg
	getConfig := func() (abi.PluginConfig, bool) {
		return liveCfg, true
	}

	return store, dataDir, getConfig
}

func TestRegisterRoutesAndResource(t *testing.T) {
	store, _, getConfig := setupTestStore(t, time.Now())
	defer store.Close()

	h := ui.NewHandler(store, getConfig)
	if h == nil {
		t.Fatal("expected non-nil Handler")
	}

	reg := h.Register()

	// Verify Resource Route
	if len(reg.Resources) != 1 {
		t.Fatalf("expected 1 resource route, got %d", len(reg.Resources))
	}
	res := reg.Resources[0]
	if res.Path != "/panel" {
		t.Errorf("expected resource path /panel, got %q", res.Path)
	}
	if res.Menu != "Cache Tracker" {
		t.Errorf("expected resource menu 'Cache Tracker', got %q", res.Menu)
	}

	// Verify Management Routes
	expectedRoutes := map[string]string{
		"/plugins/cpa-cache-tracker/status":       http.MethodGet,
		"/plugins/cpa-cache-tracker/summary":      http.MethodGet,
		"/plugins/cpa-cache-tracker/records":      http.MethodGet,
		"/plugins/cpa-cache-tracker/records/diff": http.MethodGet,
		"/plugins/cpa-cache-tracker/purge":        http.MethodPost,
	}

	if len(reg.Routes) != len(expectedRoutes) {
		t.Fatalf("expected %d management routes, got %d", len(expectedRoutes), len(reg.Routes))
	}

	for _, route := range reg.Routes {
		expectedMethod, ok := expectedRoutes[route.Path]
		if !ok {
			t.Errorf("unexpected management route declared: %s", route.Path)
			continue
		}
		if route.Method != expectedMethod {
			t.Errorf("route %s expected method %s, got %s", route.Path, expectedMethod, route.Method)
		}
	}
}

func TestHandleResourcePanel(t *testing.T) {
	store, _, getConfig := setupTestStore(t, time.Now())
	defer store.Close()

	h := ui.NewHandler(store, getConfig)

	paths := []string{
		"/v0/resource/plugins/cpa-cache-tracker/panel",
		"/plugins/cpa-cache-tracker/panel",
		"/panel",
	}

	for _, p := range paths {
		req := abi.ManagementRequest{
			Method: http.MethodGet,
			Path:   p,
		}
		resp := h.Handle(req)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("path %s expected status 200, got %d", p, resp.StatusCode)
		}
		contentType := resp.Headers.Get("Content-Type")
		if !strings.HasPrefix(contentType, "text/html") {
			t.Errorf("expected text/html Content-Type, got %q", contentType)
		}
		cacheControl := resp.Headers.Get("Cache-Control")
		if cacheControl != "no-store" {
			t.Errorf("expected Cache-Control: no-store, got %q", cacheControl)
		}

		bodyStr := string(resp.Body)
		if !strings.Contains(bodyStr, "Cache Tracker") {
			t.Errorf("expected panel HTML to contain 'Cache Tracker'")
		}
		if !strings.Contains(bodyStr, "cli-proxy-auth") {
			t.Errorf("expected panel HTML to contain 'cli-proxy-auth'")
		}
		if !strings.Contains(bodyStr, "trace_enabled") {
			t.Errorf("expected panel HTML to contain 'trace_enabled'")
		}
		if !strings.Contains(bodyStr, "cpa-cache-tracker") {
			t.Errorf("expected panel HTML to contain 'cpa-cache-tracker'")
		}

		// Zero CDN check (pure offline)
		if strings.Contains(bodyStr, "<script src=\"http") || strings.Contains(bodyStr, "<script src='http") {
			t.Errorf("panel HTML contains external CDN script reference")
		}
		if strings.Contains(bodyStr, "<link rel=\"stylesheet\" href=\"http") || strings.Contains(bodyStr, "<link rel='stylesheet' href='http") {
			t.Errorf("panel HTML contains external CDN stylesheet reference")
		}
		if strings.Contains(bodyStr, "@import url(http") || strings.Contains(bodyStr, "@import url(\"http") {
			t.Errorf("panel HTML contains external CDN CSS import")
		}
	}
}

func TestHandleStatus(t *testing.T) {
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	dataDir := t.TempDir()
	nowFn := func() time.Time { return fixedNow }

	cfg := abi.DefaultPluginConfig()
	cfg.DataDir = dataDir
	cfg.TraceEnabled = true

	store, err := storage.Open(dataDir, cfg, nowFn)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	liveCfg := cfg
	getConfig := func() (abi.PluginConfig, bool) {
		return liveCfg, true
	}

	h := ui.NewHandler(store, getConfig)

	req := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/status",
	}
	resp := h.Handle(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", resp.StatusCode, string(resp.Body))
	}

	var status storage.StatusResponse
	if err := json.Unmarshal(resp.Body, &status); err != nil {
		t.Fatalf("failed to unmarshal status JSON: %v", err)
	}

	if !status.TraceEnabled {
		t.Errorf("expected TraceEnabled=true")
	}
	if status.OmitBodyAfter {
		t.Errorf("expected OmitBodyAfter=false when TraceEnabled=true")
	}

	// Change live config to TraceEnabled=false
	liveCfg.TraceEnabled = false
	resp2 := h.Handle(req)
	var status2 storage.StatusResponse
	if err := json.Unmarshal(resp2.Body, &status2); err != nil {
		t.Fatalf("failed to unmarshal status JSON: %v", err)
	}
	if status2.TraceEnabled {
		t.Errorf("expected TraceEnabled=false after reconfig")
	}
	if !status2.OmitBodyAfter {
		t.Errorf("expected OmitBodyAfter=true when TraceEnabled=false")
	}
}

func TestHandleSummary(t *testing.T) {
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	store, _, getConfig := setupTestStore(t, fixedNow)
	defer store.Close()

	// Seed records
	rec1 := tracker.Record{
		SessionID: "sess-1",
		Model:     "gpt-4o",
		Provider:  "openai",
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 1000,
			CacheRead:      800,
			UncachedInput:  200,
			Total:          1000,
		},
	}
	if _, err := store.Append(rec1); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	h := ui.NewHandler(store, getConfig)

	// Valid query
	req := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/summary",
		Query: url.Values{
			"range": []string{"today"},
		},
	}
	resp := h.Handle(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(resp.Body))
	}

	var summary storage.SummaryResponse
	if err := json.Unmarshal(resp.Body, &summary); err != nil {
		t.Fatalf("unmarshal summary failed: %v", err)
	}
	if summary.Totals.Requests != 1 {
		t.Errorf("expected 1 request in summary totals, got %d", summary.Totals.Requests)
	}
	if summary.Totals.CacheRead != 800 {
		t.Errorf("expected 800 cache_read, got %d", summary.Totals.CacheRead)
	}

	// Test range variations: 1h, 24h, 7d, 14d
	for _, rng := range []string{"1h", "24h", "7d", "14d"} {
		rReq := abi.ManagementRequest{
			Method: http.MethodGet,
			Path:   "/v0/management/plugins/cpa-cache-tracker/summary",
			Query: url.Values{
				"range": []string{rng},
			},
		}
		rResp := h.Handle(rReq)
		if rResp.StatusCode != http.StatusOK {
			t.Errorf("range %s expected 200, got %d", rng, rResp.StatusCode)
		}
	}

	// Invalid from date should return 400
	badReq := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/summary",
		Query: url.Values{
			"from": []string{"not-a-valid-date"},
		},
	}
	badResp := h.Handle(badReq)
	if badResp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for bad 'from', got %d", badResp.StatusCode)
	}
}

func TestHandleRecordsAndDiff(t *testing.T) {
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	store, _, getConfig := setupTestStore(t, fixedNow)
	defer store.Close()

	rec1 := tracker.Record{
		SessionID: "sess-diff",
		Model:     "claude-3-5-sonnet",
		Provider:  "anthropic",
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 1000,
			CacheRead:      0,
			UncachedInput:  1000,
			Total:          1000,
		},
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			SystemHash:    "sys-1",
			FirstUserHash: "user-1",
			MessageCount:  1,
			MessageHashes: []string{"msg-1"},
		},
	}
	saved1, err := store.Append(rec1)
	if err != nil {
		t.Fatalf("Append 1 failed: %v", err)
	}

	rec2 := tracker.Record{
		SessionID: "sess-diff",
		Model:     "claude-3-5-sonnet",
		Provider:  "anthropic",
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 1500,
			CacheRead:      1000,
			UncachedInput:  500,
			Total:          1500,
		},
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			SystemHash:    "sys-1",
			FirstUserHash: "user-1",
			MessageCount:  3,
			MessageHashes: []string{"msg-1", "msg-2", "msg-3"},
		},
	}
	saved2, err := store.Append(rec2)
	if err != nil {
		t.Fatalf("Append 2 failed: %v", err)
	}

	h := ui.NewHandler(store, getConfig)

	// List records
	listReq := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/records",
		Query: url.Values{
			"limit": []string{"10"},
		},
	}
	listResp := h.Handle(listReq)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("records list expected 200, got %d", listResp.StatusCode)
	}
	var recordsResp storage.RecordsResponse
	if err := json.Unmarshal(listResp.Body, &recordsResp); err != nil {
		t.Fatalf("unmarshal records failed: %v", err)
	}
	if recordsResp.Total != 2 {
		t.Errorf("expected 2 records, got %d", recordsResp.Total)
	}

	// Pagination limit/offset validation
	badLimitReq := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/records",
		Query: url.Values{
			"limit": []string{"-5"},
		},
	}
	if code := h.Handle(badLimitReq).StatusCode; code != http.StatusBadRequest {
		t.Errorf("expected 400 for negative limit, got %d", code)
	}

	badOffsetReq := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/records",
		Query: url.Values{
			"offset": []string{"not-a-number"},
		},
	}
	if code := h.Handle(badOffsetReq).StatusCode; code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-numeric offset, got %d", code)
	}

	// Diff tests
	// 1. Missing id
	diffNoIDReq := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/records/diff",
	}
	if code := h.Handle(diffNoIDReq).StatusCode; code != http.StatusBadRequest {
		t.Errorf("expected 400 for diff without id, got %d", code)
	}

	// 2. Non-existent id
	diffNotFoundReq := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/records/diff",
		Query: url.Values{
			"id": []string{"non-existent-id"},
		},
	}
	if code := h.Handle(diffNotFoundReq).StatusCode; code != http.StatusNotFound {
		t.Errorf("expected 404 for diff non-existent id, got %d", code)
	}

	// 3. Existing record diff with embedded query in path
	diffReq := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/records/diff?id=" + saved2.ID,
	}
	diffResp := h.Handle(diffReq)
	if diffResp.StatusCode != http.StatusOK {
		t.Fatalf("diff expected 200, got %d: %s", diffResp.StatusCode, string(diffResp.Body))
	}
	var diffRespData storage.DiffResponse
	if err := json.Unmarshal(diffResp.Body, &diffRespData); err != nil {
		t.Fatalf("unmarshal diff failed: %v", err)
	}
	if diffRespData.Record == nil || diffRespData.Record.ID != saved2.ID {
		t.Errorf("diff record ID mismatch")
	}
	if diffRespData.Previous == nil || diffRespData.Previous.ID != saved1.ID {
		t.Errorf("diff previous ID mismatch")
	}
	if diffRespData.Comparison.IdenticalLeadingMessages != 1 {
		t.Errorf("expected 1 identical leading message, got %d", diffRespData.Comparison.IdenticalLeadingMessages)
	}
}

func TestHandlePurge(t *testing.T) {
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	store, _, getConfig := setupTestStore(t, fixedNow)
	defer store.Close()

	rec1 := tracker.Record{
		SessionID: "sess-purge",
		Model:     "gpt-4o",
	}
	if _, err := store.Append(rec1); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	h := ui.NewHandler(store, getConfig)

	// 1. Invalid body: not JSON
	reqBadJSON := abi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-cache-tracker/purge",
		Body:   []byte(`not-json`),
	}
	if code := h.Handle(reqBadJSON).StatusCode; code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad JSON in purge, got %d", code)
	}

	// 2. Invalid body: missing both all and older_than_days
	reqEmptyBody := abi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-cache-tracker/purge",
		Body:   []byte(`{}`),
	}
	if code := h.Handle(reqEmptyBody).StatusCode; code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty options in purge, got %d", code)
	}

	// 3. Invalid body: negative older_than_days
	reqNegDays := abi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-cache-tracker/purge",
		Body:   []byte(`{"older_than_days": -5}`),
	}
	if code := h.Handle(reqNegDays).StatusCode; code != http.StatusBadRequest {
		t.Errorf("expected 400 for negative older_than_days, got %d", code)
	}

	// 4. Valid purge: older_than_days: 7
	reqPurge7 := abi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-cache-tracker/purge",
		Body:   []byte(`{"older_than_days": 7}`),
	}
	respPurge7 := h.Handle(reqPurge7)
	if respPurge7.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for purge 7 days, got %d: %s", respPurge7.StatusCode, string(respPurge7.Body))
	}
	var res7 storage.PurgeResult
	if err := json.Unmarshal(respPurge7.Body, &res7); err != nil {
		t.Fatalf("unmarshal purge result failed: %v", err)
	}

	// 5. Valid purge: all: true
	reqPurgeAll := abi.ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-cache-tracker/purge",
		Body:   []byte(`{"all": true}`),
	}
	respPurgeAll := h.Handle(reqPurgeAll)
	if respPurgeAll.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for purge all, got %d", respPurgeAll.StatusCode)
	}
	var resAll storage.PurgeResult
	if err := json.Unmarshal(respPurgeAll.Body, &resAll); err != nil {
		t.Fatalf("unmarshal purge all result failed: %v", err)
	}
	if resAll.RecordCount != 0 {
		t.Errorf("expected 0 records after purge all, got %d", resAll.RecordCount)
	}
}

func TestHandleMethodNotAllowedAndNotFound(t *testing.T) {
	store, _, getConfig := setupTestStore(t, time.Now())
	defer store.Close()

	h := ui.NewHandler(store, getConfig)

	// Wrong methods
	tests405 := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v0/management/plugins/cpa-cache-tracker/status"},
		{http.MethodDelete, "/v0/management/plugins/cpa-cache-tracker/records"},
		{http.MethodPost, "/v0/management/plugins/cpa-cache-tracker/records/diff"},
		{http.MethodGet, "/v0/management/plugins/cpa-cache-tracker/purge"},
		{http.MethodPost, "/v0/resource/plugins/cpa-cache-tracker/panel"},
	}

	for _, tc := range tests405 {
		req := abi.ManagementRequest{
			Method: tc.method,
			Path:   tc.path,
		}
		resp := h.Handle(req)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s %s expected 405, got %d", tc.method, tc.path, resp.StatusCode)
		}
	}

	// Unknown paths -> 404
	tests404 := []string{
		"/v0/management/plugins/cpa-cache-tracker/nonexistent",
		"/plugins/cpa-cache-tracker/foo",
		"/unknown",
	}

	for _, p := range tests404 {
		req := abi.ManagementRequest{
			Method: http.MethodGet,
			Path:   p,
		}
		resp := h.Handle(req)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s expected 404, got %d", p, resp.StatusCode)
		}
	}
}

func TestHandleNilStoreAndEdgeCases(t *testing.T) {
	hNil := ui.NewHandler(nil, nil)

	// Status with nil store
	resp := hNil.Handle(abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/status",
	})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 for status with nil store, got %d", resp.StatusCode)
	}

	// Summary with nil store
	resp = hNil.Handle(abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/summary",
	})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 for summary with nil store, got %d", resp.StatusCode)
	}

	// Records with nil store
	resp = hNil.Handle(abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/records",
	})
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 for records with nil store, got %d", resp.StatusCode)
	}

	// Panel works even with nil store
	resp = hNil.Handle(abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/panel",
	})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for panel with nil store, got %d", resp.StatusCode)
	}
}

func TestHandleQueryMergingAndParsing(t *testing.T) {
	store, _, getConfig := setupTestStore(t, time.Now())
	defer store.Close()

	h := ui.NewHandler(store, getConfig)

	// Query string in Path merged with Query map
	req := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/records?limit=10&status=hit",
		Query: url.Values{
			"kind": []string{"main"},
		},
	}
	resp := h.Handle(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(resp.Body))
	}
	var res storage.RecordsResponse
	if err := json.Unmarshal(resp.Body, &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if res.Limit != 10 {
		t.Errorf("expected limit 10 from path query, got %d", res.Limit)
	}

	// Invalid to timestamp in summary
	badToSummary := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/summary?to=invalid-to-date",
	}
	if code := h.Handle(badToSummary).StatusCode; code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad 'to' in summary, got %d", code)
	}

	// Invalid from timestamp in records
	badFromRecords := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/records?from=invalid-from-date",
	}
	if code := h.Handle(badFromRecords).StatusCode; code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad 'from' in records, got %d", code)
	}

	// Invalid to timestamp in records
	badToRecords := abi.ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-cache-tracker/records?to=invalid-to-date",
	}
	if code := h.Handle(badToRecords).StatusCode; code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad 'to' in records, got %d", code)
	}
}

