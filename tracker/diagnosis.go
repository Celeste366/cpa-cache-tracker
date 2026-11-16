package tracker

import (
	"sort"
	"strings"
	"time"

	"cpa-cache-tracker/fingerprint"
)

// Record represents a persistent cache tracking record for an intercepted LLM request/response.
type Record struct {
	ID                   string              `json:"id"`
	Timestamp            time.Time           `json:"timestamp"`
	TraceID              string              `json:"trace_id,omitempty"`
	RequestID            string              `json:"request_id,omitempty"`
	Provider             string              `json:"provider,omitempty"`
	ExecutorType         string              `json:"executor_type,omitempty"`
	Model                string              `json:"model,omitempty"`
	Alias                string              `json:"alias,omitempty"`
	DisplayModel         string              `json:"display_model,omitempty"`
	AuthID               string              `json:"auth_id,omitempty"`
	SessionID            string              `json:"session_id,omitempty"`
	ParentSessionID      string              `json:"parent_session_id,omitempty"`
	LCPAffinitySessionID string              `json:"lcp_affinity_session_id,omitempty"`
	EffectiveSessionKey  string              `json:"effective_session_key,omitempty"`
	IsFork               bool                `json:"is_fork,omitempty"`
	IsCompaction         bool                `json:"is_compaction,omitempty"`
	RecordKind           string              `json:"record_kind,omitempty"`
	Stream               bool                `json:"stream,omitempty"`
	Failed               bool                `json:"failed,omitempty"`
	StatusCode           int                 `json:"status_code,omitempty"`
	LatencyMs            int64               `json:"latency_ms,omitempty"`
	TTFTMs               int64               `json:"ttft_ms,omitempty"`
	Tokens               NormalizedTokens    `json:"tokens"`
	TraceEnabled         bool                `json:"trace_enabled,omitempty"`
	HasFingerprint       bool                `json:"has_fingerprint,omitempty"`
	Fingerprint          *fingerprint.Result `json:"fingerprint,omitempty"`
	PrevID               string              `json:"prev_id,omitempty"`
	MissReason           string              `json:"miss_reason,omitempty"`
}

// DiffComparison contains side-by-side comparison details between a current record and its predecessor.
type DiffComparison struct {
	AuthChanged              bool     `json:"auth_changed,omitempty"`
	PrevAuthID               string   `json:"prev_auth_id,omitempty"`
	CurAuthID                string   `json:"cur_auth_id,omitempty"`
	ChangedFields            []string `json:"changed_fields,omitempty"`
	ChangedOptions           []string `json:"changed_options,omitempty"`
	IdenticalLeadingMessages int      `json:"identical_leading_messages"`
	PrevMessageCount         int      `json:"prev_message_count"`
	CurMessageCount          int      `json:"cur_message_count"`
	AppendOnly               bool     `json:"append_only,omitempty"`
	FirstChangedMessage      int      `json:"first_changed_message,omitempty"`
	HashesTruncated          bool     `json:"hashes_truncated,omitempty"`
	IsContinuation           bool     `json:"is_continuation,omitempty"`
	MissReason               string   `json:"miss_reason,omitempty"`
}

// ResolveSessionKey returns the effective session key for same-session matching in priority order:
// 1. strings.TrimSpace(rec.SessionID)
// 2. strings.TrimSpace(rec.LCPAffinitySessionID)
// 3. Fallback "anon:<auth_id>:<first_user_hash>:<system_hash>" (only if FirstUserHash != "")
// 4. Empty string "" (indicates no session matching possible)
func ResolveSessionKey(rec *Record) string {
	if rec == nil {
		return ""
	}
	if s := strings.TrimSpace(rec.SessionID); s != "" {
		return s
	}
	if s := strings.TrimSpace(rec.LCPAffinitySessionID); s != "" {
		return s
	}
	if rec.Fingerprint != nil && rec.Fingerprint.FirstUserHash != "" {
		return "anon:" + rec.AuthID + ":" + rec.Fingerprint.FirstUserHash + ":" + rec.Fingerprint.SystemHash
	}
	return ""
}

// CompareRecords compares two records and computes field diffs, message sequence prefix alignment,
// append-only status, and option changes.
func CompareRecords(cur, prev *Record) DiffComparison {
	var diff DiffComparison
	if cur == nil && prev == nil {
		return diff
	}

	if cur != nil {
		diff.CurAuthID = cur.AuthID
		if cur.HasFingerprint && cur.Fingerprint != nil {
			diff.CurMessageCount = cur.Fingerprint.MessageCount
			diff.IsContinuation = cur.Fingerprint.IsContinuation
			diff.HashesTruncated = cur.Fingerprint.HashesTruncated
		}
	}

	if prev == nil {
		return diff
	}

	diff.PrevAuthID = prev.AuthID
	if prev.HasFingerprint && prev.Fingerprint != nil {
		diff.PrevMessageCount = prev.Fingerprint.MessageCount
		diff.HashesTruncated = diff.HashesTruncated || prev.Fingerprint.HashesTruncated
	}

	if cur != nil {
		diff.AuthChanged = (cur.AuthID != prev.AuthID)
	}

	if cur == nil || !cur.HasFingerprint || cur.Fingerprint == nil || !prev.HasFingerprint || prev.Fingerprint == nil {
		return diff
	}

	curFp := cur.Fingerprint
	prevFp := prev.Fingerprint

	// Check system_hash, tools_hash, options_hash, first_user_hash
	if curFp.SystemHash != prevFp.SystemHash {
		diff.ChangedFields = append(diff.ChangedFields, "system_hash")
	}
	if curFp.ToolsHash != prevFp.ToolsHash {
		diff.ChangedFields = append(diff.ChangedFields, "tools_hash")
	}
	if curFp.OptionsHash != prevFp.OptionsHash {
		diff.ChangedFields = append(diff.ChangedFields, "options_hash")
	}
	if curFp.FirstUserHash != prevFp.FirstUserHash {
		diff.ChangedFields = append(diff.ChangedFields, "first_user_hash")
	}

	// ChangedOptions: sorted keys from the union of OptionHashes whose values differ
	allKeys := make(map[string]struct{})
	for k := range curFp.OptionHashes {
		allKeys[k] = struct{}{}
	}
	for k := range prevFp.OptionHashes {
		allKeys[k] = struct{}{}
	}
	for k := range allKeys {
		var curVal, prevVal string
		if curFp.OptionHashes != nil {
			curVal = curFp.OptionHashes[k]
		}
		if prevFp.OptionHashes != nil {
			prevVal = prevFp.OptionHashes[k]
		}
		if curVal != prevVal {
			diff.ChangedOptions = append(diff.ChangedOptions, k)
		}
	}
	sort.Strings(diff.ChangedOptions)

	// Longest common prefix of message hashes
	minHashes := min(len(curFp.MessageHashes), len(prevFp.MessageHashes))
	identical := 0
	for i := 0; i < minHashes; i++ {
		if curFp.MessageHashes[i] == prevFp.MessageHashes[i] {
			identical++
		} else {
			break
		}
	}
	diff.IdenticalLeadingMessages = identical

	// AppendOnly: !HashesTruncated && identical == prev.MessageCount && cur.MessageCount >= prev.MessageCount
	diff.AppendOnly = !diff.HashesTruncated &&
		diff.IdenticalLeadingMessages == prevFp.MessageCount &&
		curFp.MessageCount >= prevFp.MessageCount

	// FirstChangedMessage: 1-based index if not continuation and divergence occurs within minHashes
	if !curFp.IsContinuation && diff.IdenticalLeadingMessages < minHashes {
		diff.FirstChangedMessage = diff.IdenticalLeadingMessages + 1
	}

	return diff
}

// ClassifyMissReason determines why a cache miss occurred according to Spec §5.5 priority order:
// 1. cold_start (prev == nil)
// 2. auth_switched (auth changed)
// 3. trace_disabled (tracing disabled on either record when fingerprint missing) / upstream_cache_miss
// 4. system_changed
// 5. tools_changed
// 6. options_changed
// 7. upstream_cache_miss (is_continuation delta turn)
// 8. history_shortened
// 9. prefix_modified
// 10. upstream_cache_miss (append-only prefix matched upstream miss)
//
// If cur is nil, failed, effective_input <= 0, or cache_read > 0 (hit), returns ("", diff).
func ClassifyMissReason(cur, prev *Record) (string, DiffComparison) {
	diff := CompareRecords(cur, prev)

	if cur == nil || cur.Failed || cur.Tokens.EffectiveInput <= 0 || cur.Tokens.CacheRead > 0 {
		return "", diff
	}

	var reason string

	if prev == nil {
		reason = "cold_start"
	} else if diff.AuthChanged {
		reason = "auth_switched"
	} else if !cur.HasFingerprint || cur.Fingerprint == nil || !prev.HasFingerprint || prev.Fingerprint == nil {
		if !cur.TraceEnabled || !prev.TraceEnabled {
			reason = "trace_disabled"
		} else {
			reason = "upstream_cache_miss"
		}
	} else if cur.Fingerprint.SystemHash != prev.Fingerprint.SystemHash {
		reason = "system_changed"
	} else if cur.Fingerprint.ToolsHash != prev.Fingerprint.ToolsHash {
		reason = "tools_changed"
	} else if cur.Fingerprint.OptionsHash != prev.Fingerprint.OptionsHash {
		reason = "options_changed"
	} else if cur.Fingerprint.IsContinuation {
		reason = "upstream_cache_miss"
	} else if cur.Fingerprint.MessageCount < prev.Fingerprint.MessageCount {
		reason = "history_shortened"
	} else if (!diff.HashesTruncated && diff.IdenticalLeadingMessages < prev.Fingerprint.MessageCount) ||
		(diff.IdenticalLeadingMessages < min(len(cur.Fingerprint.MessageHashes), len(prev.Fingerprint.MessageHashes))) {
		reason = "prefix_modified"
	} else {
		reason = "upstream_cache_miss"
	}

	diff.MissReason = reason
	return reason, diff
}
