package ui

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/storage"
)

// PluginID identifies this plugin in CPA route normalization.
const PluginID = "cpa-cache-tracker"

//go:embed panel.html
var panelHTML []byte

// Handler serves management APIs and embedded UI resources for cpa-cache-tracker.
type Handler struct {
	store     *storage.Store
	getConfig func() (abi.PluginConfig, bool)
}

// NewHandler constructs a Handler with the given Store and live config getter.
func NewHandler(store *storage.Store, getConfig func() (abi.PluginConfig, bool)) *Handler {
	return &Handler{
		store:     store,
		getConfig: getConfig,
	}
}

// Register returns the declared Management routes and Resource routes for CPA.
func (h *Handler) Register() abi.ManagementRegistrationResponse {
	return abi.ManagementRegistrationResponse{
		Resources: []abi.ResourceRoute{
			{
				Path:        "/panel",
				Menu:        "Cache Tracker",
				Description: "Cache Tracker Web Visualization Panel",
			},
		},
		Routes: []abi.ManagementRoute{
			{
				Method:      http.MethodGet,
				Path:        "/plugins/cpa-cache-tracker/status",
				Description: "Cache Tracker Status",
			},
			{
				Method:      http.MethodGet,
				Path:        "/plugins/cpa-cache-tracker/summary",
				Description: "Cache Tracker Summary KPIs and Time Series",
			},
			{
				Method:      http.MethodGet,
				Path:        "/plugins/cpa-cache-tracker/records",
				Description: "Cache Tracker Request Records",
			},
			{
				Method:      http.MethodGet,
				Path:        "/plugins/cpa-cache-tracker/records/diff",
				Description: "Cache Tracker Same-Session Request Diff",
			},
			{
				Method:      http.MethodPost,
				Path:        "/plugins/cpa-cache-tracker/purge",
				Description: "Purge Cache Tracker Records",
			},
		},
	}
}

// Handle dispatches an incoming ManagementRequest to the corresponding route handler.
func (h *Handler) Handle(req abi.ManagementRequest) abi.ManagementResponse {
	query := make(url.Values)
	for k, vs := range req.Query {
		query[k] = append([]string(nil), vs...)
	}

	cleanPath := req.Path
	if idx := strings.Index(cleanPath, "?"); idx >= 0 {
		rawQuery := cleanPath[idx+1:]
		cleanPath = cleanPath[:idx]
		if parsedQuery, err := url.ParseQuery(rawQuery); err == nil {
			for k, vs := range parsedQuery {
				for _, v := range vs {
					query.Add(k, v)
				}
			}
		}
	}

	for _, pfx := range []string{
		"/v0/resource/plugins/" + PluginID,
		"/v0/management/plugins/" + PluginID,
		"/resource/plugins/" + PluginID,
		"/management/plugins/" + PluginID,
		"/plugins/" + PluginID,
	} {
		if strings.HasPrefix(cleanPath, pfx) {
			cleanPath = strings.TrimPrefix(cleanPath, pfx)
			break
		}
	}

	cleanPath = strings.TrimRight(cleanPath, "/")
	if cleanPath == "" {
		cleanPath = "/"
	}

	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}

	switch cleanPath {
	case "/panel":
		if method != http.MethodGet {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return h.handlePanel()

	case "/status":
		if method != http.MethodGet {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return h.handleStatus()

	case "/summary":
		if method != http.MethodGet {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return h.handleSummary(query)

	case "/records/diff":
		if method != http.MethodGet {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return h.handleRecordsDiff(query)

	case "/records":
		if method != http.MethodGet {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return h.handleRecords(query)

	case "/purge":
		if method != http.MethodPost {
			return jsonError(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		}
		return h.handlePurge(req.Body)

	default:
		return jsonError(http.StatusNotFound, "not_found", "route not found")
	}
}

func (h *Handler) handlePanel() abi.ManagementResponse {
	return abi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"text/html; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: panelHTML,
	}
}

func (h *Handler) handleStatus() abi.ManagementResponse {
	if h.store == nil {
		return jsonError(http.StatusInternalServerError, "store_unavailable", "storage store not available")
	}

	traceEnabled := true
	retentionDays := 14
	maxDiskMB := 200
	maxMessageHashes := 512

	if h.getConfig != nil {
		if cfg, ok := h.getConfig(); ok {
			traceEnabled = cfg.TraceEnabled
			retentionDays = cfg.RetentionDays
			maxDiskMB = cfg.MaxDiskMB
			maxMessageHashes = cfg.MaxMessageHashes
		}
	}

	omitBodyAfter := !traceEnabled
	status := h.store.Status(omitBodyAfter)
	status.TraceEnabled = traceEnabled
	status.OmitBodyAfter = omitBodyAfter
	status.RetentionDays = retentionDays
	status.MaxDiskMB = maxDiskMB
	status.MaxMessageHashes = maxMessageHashes

	return jsonOK(status)
}

func (h *Handler) handleSummary(query url.Values) abi.ManagementResponse {
	if h.store == nil {
		return jsonError(http.StatusInternalServerError, "store_unavailable", "storage store not available")
	}

	fromStr := query.Get("from")
	toStr := query.Get("to")
	rangeParam := strings.TrimSpace(query.Get("range"))
	model := query.Get("model")
	provider := query.Get("provider")
	kind := query.Get("kind")
	if kind == "" {
		kind = "main"
	}

	var fromTime, toTime time.Time
	if fromStr != "" {
		t, err := parseTimestamp(fromStr)
		if err != nil {
			return jsonError(http.StatusBadRequest, "bad_request", "invalid from timestamp: "+err.Error())
		}
		fromTime = t
	}
	if toStr != "" {
		t, err := parseTimestamp(toStr)
		if err != nil {
			return jsonError(http.StatusBadRequest, "bad_request", "invalid to timestamp: "+err.Error())
		}
		toTime = t
	}

	now := time.Now()
	if fromTime.IsZero() && toTime.IsZero() {
		switch strings.ToLower(rangeParam) {
		case "1h":
			fromTime = now.Add(-1 * time.Hour)
			toTime = now
		case "24h":
			fromTime = now.Add(-24 * time.Hour)
			toTime = now
		case "14d":
			fromTime = now.AddDate(0, 0, -14)
			toTime = now
		}
	}

	q := storage.SummaryQuery{
		Range:    rangeParam,
		From:     fromTime,
		To:       toTime,
		Model:    model,
		Provider: provider,
		Kind:     kind,
	}

	summary := h.store.Summary(q)
	if rangeParam != "" && (rangeParam == "1h" || rangeParam == "24h" || rangeParam == "14d") {
		summary.Range = rangeParam
	}

	return jsonOK(summary)
}

func (h *Handler) handleRecords(query url.Values) abi.ManagementResponse {
	if h.store == nil {
		return jsonError(http.StatusInternalServerError, "store_unavailable", "storage store not available")
	}

	rangeParam := strings.TrimSpace(query.Get("range"))
	fromStr := query.Get("from")
	toStr := query.Get("to")
	model := query.Get("model")
	provider := query.Get("provider")
	status := query.Get("status")
	kind := query.Get("kind")
	if kind == "" {
		kind = "main"
	}
	qSearch := query.Get("q")
	if qSearch == "" {
		qSearch = query.Get("query")
	}

	limit := 50
	if limitStr := query.Get("limit"); limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err != nil || l < 0 {
			return jsonError(http.StatusBadRequest, "bad_request", "invalid limit")
		}
		if l > 1000 {
			l = 1000
		}
		limit = l
	}

	offset := 0
	if offsetStr := query.Get("offset"); offsetStr != "" {
		off, err := strconv.Atoi(offsetStr)
		if err != nil || off < 0 {
			return jsonError(http.StatusBadRequest, "bad_request", "invalid offset")
		}
		offset = off
	}

	var fromTime, toTime time.Time
	if fromStr != "" {
		t, err := parseTimestamp(fromStr)
		if err != nil {
			return jsonError(http.StatusBadRequest, "bad_request", "invalid from timestamp: "+err.Error())
		}
		fromTime = t
	}
	if toStr != "" {
		t, err := parseTimestamp(toStr)
		if err != nil {
			return jsonError(http.StatusBadRequest, "bad_request", "invalid to timestamp: "+err.Error())
		}
		toTime = t
	}

	now := time.Now()
	if fromTime.IsZero() && toTime.IsZero() {
		switch strings.ToLower(rangeParam) {
		case "1h":
			fromTime = now.Add(-1 * time.Hour)
			toTime = now
		case "24h":
			fromTime = now.Add(-24 * time.Hour)
			toTime = now
		case "14d":
			fromTime = now.AddDate(0, 0, -14)
			toTime = now
		}
	}

	q := storage.RecordsQuery{
		Range:    rangeParam,
		From:     fromTime,
		To:       toTime,
		Model:    model,
		Provider: provider,
		Status:   status,
		Kind:     kind,
		Query:    qSearch,
		Limit:    limit,
		Offset:   offset,
	}

	records := h.store.Records(q)
	return jsonOK(records)
}

func (h *Handler) handleRecordsDiff(query url.Values) abi.ManagementResponse {
	if h.store == nil {
		return jsonError(http.StatusInternalServerError, "store_unavailable", "storage store not available")
	}

	id := strings.TrimSpace(query.Get("id"))
	if id == "" {
		return jsonError(http.StatusBadRequest, "bad_request", "id parameter is required")
	}

	diff, err := h.store.Diff(id)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return jsonError(http.StatusNotFound, "not_found", err.Error())
		}
		return jsonError(http.StatusInternalServerError, "internal_error", err.Error())
	}

	return jsonOK(diff)
}

func (h *Handler) handlePurge(bodyBytes []byte) abi.ManagementResponse {
	if h.store == nil {
		return jsonError(http.StatusInternalServerError, "store_unavailable", "storage store not available")
	}

	if len(bytes.TrimSpace(bodyBytes)) == 0 {
		return jsonError(http.StatusBadRequest, "bad_request", "request body is required")
	}

	var req struct {
		OlderThanDays *int  `json:"older_than_days"`
		All           *bool `json:"all"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		return jsonError(http.StatusBadRequest, "bad_request", "invalid json body: "+err.Error())
	}

	all := req.All != nil && *req.All
	olderThanDays := -1
	if req.OlderThanDays != nil {
		olderThanDays = *req.OlderThanDays
	}

	if !all && olderThanDays < 0 {
		return jsonError(http.StatusBadRequest, "bad_request", "either all: true or older_than_days >= 0 is required")
	}

	res, err := h.store.Purge(olderThanDays, all)
	if err != nil {
		return jsonError(http.StatusInternalServerError, "internal_error", err.Error())
	}

	return jsonOK(res)
}

func parseTimestamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e11 {
			return time.UnixMilli(n), nil
		}
		return time.Unix(n, 0), nil
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, fmtStr := range formats {
		if t, err := time.ParseInLocation(fmtStr, s, time.Local); err == nil {
			return t, nil
		}
		if t, err := time.Parse(fmtStr, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp format: %s", s)
}

func jsonOK(v any) abi.ManagementResponse {
	b, err := json.Marshal(v)
	if err != nil {
		return jsonError(http.StatusInternalServerError, "internal_error", err.Error())
	}
	return abi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: b,
	}
}

func jsonError(statusCode int, code, msg string) abi.ManagementResponse {
	payload := map[string]string{
		"error":   code,
		"message": msg,
	}
	b, _ := json.Marshal(payload)
	return abi.ManagementResponse{
		StatusCode: statusCode,
		Headers: http.Header{
			"Content-Type":  []string{"application/json; charset=utf-8"},
			"Cache-Control": []string{"no-store"},
		},
		Body: b,
	}
}
