package tracker

import (
	"container/list"
	"strings"
	"sync"
	"time"

	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/fingerprint"
)

// PendingAttempt records an in-flight request intercepted before/after execution,
// awaiting usage correlation or completion.
type PendingAttempt struct {
	SelectionID          string
	TraceID              string
	HandlerReqID         string
	AttemptSeq           int
	AuthID               string
	Model                string
	RequestedModel       string
	SourceFormat         string
	ToFormat             string
	RecordKind           string
	CanonicalSessionID   string
	ParentSessionID      string
	LCPAffinitySessionID string
	IsFork               bool
	IsCompaction         bool
	HasFingerprint       bool
	Fingerprint          fingerprint.Result
	CreatedAt            time.Time
	CanceledAt           time.Time

	// internal fields for queue & global ordering
	key      string
	listElem *list.Element
}

// Correlator maintains a bounded, thread-safe queue of pending attempts keyed by
// TraceID (falling back to RequestID), correlating async usage records and completions.
type Correlator struct {
	mu             sync.Mutex
	maxEntries     int
	ttl            time.Duration
	cancelGrace    time.Duration
	pendingByTrace map[string][]*PendingAttempt
	traceByReqID   map[string]string
	seqByTrace     map[string]int
	globalList     *list.List
	lastNow        time.Time
}

// NewCorrelator creates a new Correlator with capacity cap, TTL, and cancelGrace.
func NewCorrelator(maxEntries int, ttl, cancelGrace time.Duration) *Correlator {
	if maxEntries <= 0 {
		maxEntries = 4096
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if cancelGrace <= 0 {
		cancelGrace = 15 * time.Second
	}
	return &Correlator{
		maxEntries:     maxEntries,
		ttl:            ttl,
		cancelGrace:    cancelGrace,
		pendingByTrace: make(map[string][]*PendingAttempt),
		traceByReqID:   make(map[string]string),
		seqByTrace:     make(map[string]int),
		globalList:     list.New(),
	}
}

// RecordInterceptAfter records a request attempt from request.intercept_after.
func (c *Correlator) RecordInterceptAfter(req abi.RequestInterceptRequest, traceEnabled bool, maxMsgHashes int, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastNow = now
	c.sweepExpired(now)

	key := strings.TrimSpace(req.TraceID)
	if key == "" {
		key = strings.TrimSpace(req.RequestID)
	}
	if key == "" {
		return
	}

	// Enforce max capacity by evicting globally oldest entry
	for c.globalList.Len() >= c.maxEntries {
		oldestElem := c.globalList.Front()
		if oldestElem == nil {
			break
		}
		c.removeAttempt(oldestElem.Value.(*PendingAttempt))
	}

	authID, canSessID, parentSessID, lcpAffSessID, isFork, isCompaction, recordKind := extractMetadata(req)

	c.seqByTrace[key]++
	seq := c.seqByTrace[key]

	attempt := &PendingAttempt{
		SelectionID:          getString(req.Metadata, "selection_id"),
		TraceID:              req.TraceID,
		HandlerReqID:         req.RequestID,
		AttemptSeq:           seq,
		AuthID:               authID,
		Model:                req.Model,
		RequestedModel:       req.RequestedModel,
		SourceFormat:         req.SourceFormat,
		ToFormat:             req.ToFormat,
		RecordKind:           recordKind,
		CanonicalSessionID:   canSessID,
		ParentSessionID:      parentSessID,
		LCPAffinitySessionID: lcpAffSessID,
		IsFork:               isFork,
		IsCompaction:         isCompaction,
		CreatedAt:            now,
		key:                  key,
	}
	if attempt.TraceID == "" {
		attempt.TraceID = key
	}

	if traceEnabled && len(req.Body) > 0 {
		attempt.HasFingerprint = true
		attempt.Fingerprint = fingerprint.Extract(req.SourceFormat, req.ToFormat, req.Body, maxMsgHashes)
	} else {
		attempt.HasFingerprint = false
		attempt.Fingerprint = fingerprint.Result{}
	}

	elem := c.globalList.PushBack(attempt)
	attempt.listElem = elem

	c.pendingByTrace[key] = append(c.pendingByTrace[key], attempt)
	if req.RequestID != "" {
		c.traceByReqID[req.RequestID] = key
	}
}

// MatchUsage correlates an incoming UsageRecord to the best-matching PendingAttempt,
// popping it from the queue in priority order:
// 1. Exact AuthID == u.AuthID && Model == u.Model (earliest FIFO)
// 2. AuthID == u.AuthID (earliest FIFO)
// 3. Model == u.Model (earliest FIFO)
// 4. Earliest FIFO head
func (c *Correlator) MatchUsage(u abi.UsageRecord, now time.Time) (*PendingAttempt, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastNow = now
	c.sweepExpired(now)

	key := c.resolveTraceKey(u.TraceID, u.RequestID)
	attempts := c.pendingByTrace[key]
	if len(attempts) == 0 {
		return nil, false
	}

	uAuthID := strings.TrimSpace(u.AuthID)
	uModel := strings.TrimSpace(u.Model)

	matchIdx := -1
	if selectionID := strings.TrimSpace(u.SelectionID); selectionID != "" {
		for i, att := range attempts {
			if att.SelectionID == selectionID {
				matchIdx = i
				break
			}
		}
	}

	// Priority 1: Exact AuthID == u.AuthID && Model == u.Model (earliest FIFO)
	if matchIdx == -1 && uAuthID != "" && uModel != "" {
		for i, att := range attempts {
			if att.AuthID == uAuthID && (att.Model == uModel || (att.Model == "" && att.RequestedModel == uModel)) {
				matchIdx = i
				break
			}
		}
	}

	// Priority 2: AuthID == u.AuthID (earliest FIFO)
	if matchIdx == -1 && uAuthID != "" {
		for i, att := range attempts {
			if att.AuthID == uAuthID {
				matchIdx = i
				break
			}
		}
	}

	// Priority 3: Model == u.Model (earliest FIFO)
	if matchIdx == -1 && uModel != "" {
		for i, att := range attempts {
			if att.Model == uModel || (att.Model == "" && att.RequestedModel == uModel) {
				matchIdx = i
				break
			}
		}
	}

	// Priority 4: Earliest FIFO head (when neither matched)
	if matchIdx == -1 {
		matchIdx = 0
	}

	matched := attempts[matchIdx]
	c.removeAttempt(matched)
	return matched, true
}

// HandleCompletion processes request completion events (e.g. rejected, canceled).
func (c *Correlator) HandleCompletion(comp abi.RequestCompletion, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lastNow = now
	c.sweepExpired(now)

	key := c.resolveTraceKey(comp.TraceID, comp.RequestID)
	attempts := c.pendingByTrace[key]
	if len(attempts) == 0 {
		return
	}

	reqID := strings.TrimSpace(comp.RequestID)

	switch comp.Outcome {
	case abi.RequestCompletionRejected:
		// If comp.Outcome == "rejected", immediately purge entries matching comp.RequestID
		// (HandlerReqID == comp.RequestID, or all entries for that TraceID if comp.RequestID == "")
		for i := len(attempts) - 1; i >= 0; i-- {
			att := attempts[i]
			if reqID == "" || att.HandlerReqID == reqID {
				c.removeAttempt(att)
			}
		}

	case abi.RequestCompletionCanceled:
		// If comp.Outcome == "canceled", mark matching HandlerReqID entries with CanceledAt = now (if zero)
		for _, att := range attempts {
			if reqID == "" || att.HandlerReqID == reqID {
				if att.CanceledAt.IsZero() {
					att.CanceledAt = now
				}
			}
		}
	}
}

// Len returns the current total number of pending attempts in the correlator.
func (c *Correlator) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.lastNow.IsZero() {
		c.sweepExpired(c.lastNow)
	}
	return c.globalList.Len()
}

func (c *Correlator) resolveTraceKey(traceID, requestID string) string {
	tid := strings.TrimSpace(traceID)
	if tid != "" {
		if _, ok := c.pendingByTrace[tid]; ok {
			return tid
		}
	}
	rid := strings.TrimSpace(requestID)
	if rid != "" {
		if mapped, ok := c.traceByReqID[rid]; ok && mapped != "" {
			return mapped
		}
		if _, ok := c.pendingByTrace[rid]; ok {
			return rid
		}
	}
	if tid != "" {
		return tid
	}
	return rid
}

func (c *Correlator) removeAttempt(att *PendingAttempt) {
	if att.listElem != nil {
		c.globalList.Remove(att.listElem)
		att.listElem = nil
	}
	if att.key != "" {
		attempts := c.pendingByTrace[att.key]
		for i, item := range attempts {
			if item == att {
				attempts = append(attempts[:i], attempts[i+1:]...)
				break
			}
		}
		if len(attempts) == 0 {
			delete(c.pendingByTrace, att.key)
			delete(c.seqByTrace, att.key)
		} else {
			c.pendingByTrace[att.key] = attempts
		}
	}
	if att.HandlerReqID != "" {
		delete(c.traceByReqID, att.HandlerReqID)
	}
}

func (c *Correlator) sweepExpired(now time.Time) {
	for e := c.globalList.Front(); e != nil; {
		next := e.Next()
		att := e.Value.(*PendingAttempt)
		expired := false
		if c.ttl > 0 && now.Sub(att.CreatedAt) > c.ttl {
			expired = true
		} else if !att.CanceledAt.IsZero() && now.Sub(att.CanceledAt) > c.cancelGrace {
			expired = true
		}
		if expired {
			c.removeAttempt(att)
		}
		e = next
	}
}

func extractMetadata(req abi.RequestInterceptRequest) (authID, canSessID, parentSessID, lcpAffSessID string, isFork, isCompaction bool, recordKind string) {
	meta := req.Metadata

	authID = getString(meta, "selected_auth_id")
	canSessID = getString(meta, "canonical_session_id")
	parentSessID = getString(meta, "parent_session_id")
	lcpAffSessID = getString(meta, "lcp_affinity_session_id")
	isFork = getBool(meta, "is_fork")
	isCompaction = getBool(meta, "is_compaction")

	modelExecSource := getString(meta, "source")
	if modelExecSource == "" {
		modelExecSource = getString(meta, "model_execution_source")
	}
	requestPath := getString(meta, "request_path")
	alt := getString(meta, "alt")

	toFormatLower := strings.ToLower(req.ToFormat)
	sourceFormatLower := strings.ToLower(req.SourceFormat)

	if modelExecSource == "plugin_host_model_callback" {
		recordKind = "internal_subcall"
	} else if strings.HasSuffix(requestPath, "/responses/compact") ||
		strings.Contains(toFormatLower, "responses/compact") ||
		strings.Contains(sourceFormatLower, "responses/compact") ||
		alt == "responses/compact" ||
		strings.HasSuffix(alt, "/responses/compact") {
		recordKind = "compact"
	} else {
		recordKind = "main"
	}

	return
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func getBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		lower := strings.ToLower(strings.TrimSpace(b))
		return lower == "true" || lower == "1" || lower == "yes"
	case int:
		return b != 0
	case int64:
		return b != 0
	case float64:
		return b != 0
	}
	return false
}
