package storage

import (
	"bufio"
	"bytes"
	"container/list"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/tracker"
)

// SummaryQuery defines query parameters for aggregate KPI summaries.
type SummaryQuery struct {
	Range    string    `json:"range"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Model    string    `json:"model"`
	Provider string    `json:"provider"`
	Kind     string    `json:"kind"`
}

// SummaryTotals aggregates high-level performance and cache metrics.
type SummaryTotals struct {
	Requests       int64   `json:"requests"`
	HitRequests    int64   `json:"hit_requests"`
	MissRequests   int64   `json:"miss_requests"`
	FailedRequests int64   `json:"failed_requests"`
	EffectiveInput int64   `json:"effective_input"`
	CacheRead      int64   `json:"cache_read"`
	CacheWrite     int64   `json:"cache_write"`
	UncachedInput  int64   `json:"uncached_input"`
	TokenHitRate   float64 `json:"token_hit_rate"`
	RequestHitRate float64 `json:"request_hit_rate"`
	ColdStarts     int64   `json:"cold_starts"`
	ColdHitRate    float64 `json:"cold_hit_rate"`
	WarmRequests   int64   `json:"warm_requests"`
	WarmHitRate    float64 `json:"warm_hit_rate"`
	AvgTTFTMs      float64 `json:"avg_ttft_ms"`
	AvgLatencyMs   float64 `json:"avg_latency_ms"`
}

// ModelStats aggregates metrics per display model.
type ModelStats struct {
	DisplayModel   string   `json:"display_model"`
	Providers      []string `json:"providers"`
	Requests       int64    `json:"requests"`
	HitRequests    int64    `json:"hit_requests"`
	FailedRequests int64    `json:"failed_requests"`
	EffectiveInput int64    `json:"effective_input"`
	CacheRead      int64    `json:"cache_read"`
	CacheWrite     int64    `json:"cache_write"`
	TokenHitRate   float64  `json:"token_hit_rate"`
	RequestHitRate float64  `json:"request_hit_rate"`
}

// TimeBucket holds metrics for a single time interval.
type TimeBucket struct {
	Bucket         string  `json:"bucket"`
	DisplayModel   string  `json:"display_model,omitempty"`
	Requests       int64   `json:"requests"`
	HitRequests    int64   `json:"hit_requests"`
	EffectiveInput int64   `json:"effective_input"`
	CacheRead      int64   `json:"cache_read"`
	TokenHitRate   float64 `json:"token_hit_rate"`
	RequestHitRate float64 `json:"request_hit_rate"`
}

// MissReasonStat aggregates counts and token impact per miss reason.
type MissReasonStat struct {
	Reason         string `json:"reason"`
	Count          int64  `json:"count"`
	EffectiveInput int64  `json:"effective_input"`
}

// SummaryResponse returns the complete dashboard aggregation payload.
type SummaryResponse struct {
	Range              string           `json:"range"`
	From               time.Time        `json:"from"`
	To                 time.Time        `json:"to"`
	Granularity        string           `json:"granularity"`
	Totals             SummaryTotals    `json:"totals"`
	Models             []ModelStats     `json:"models"`
	OverallSeries      []TimeBucket     `json:"overall_series"`
	ModelSeries        []TimeBucket     `json:"model_series"`
	MissReasons        []MissReasonStat `json:"miss_reasons"`
	AvailableModels    []string         `json:"available_models"`
	AvailableProviders []string         `json:"available_providers"`
}

// RecordsQuery defines filtering and pagination parameters for request records.
type RecordsQuery struct {
	Range    string    `json:"range"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Model    string    `json:"model"`
	Provider string    `json:"provider"`
	Status   string    `json:"status"` // "all", "hit", "miss", "failed"
	Kind     string    `json:"kind"`
	Query    string    `json:"query"`
	Limit    int       `json:"limit"`
	Offset   int       `json:"offset"`
}

// RecordSummary provides a compact, content-free representation of a tracker.Record.
type RecordSummary struct {
	ID                   string                   `json:"id"`
	Timestamp            time.Time                `json:"timestamp"`
	TraceID              string                   `json:"trace_id,omitempty"`
	RequestID            string                   `json:"request_id,omitempty"`
	Provider             string                   `json:"provider,omitempty"`
	ExecutorType         string                   `json:"executor_type,omitempty"`
	Model                string                   `json:"model,omitempty"`
	Alias                string                   `json:"alias,omitempty"`
	DisplayModel         string                   `json:"display_model,omitempty"`
	AuthID               string                   `json:"auth_id,omitempty"`
	SessionID            string                   `json:"session_id,omitempty"`
	ParentSessionID      string                   `json:"parent_session_id,omitempty"`
	LCPAffinitySessionID string                   `json:"lcp_affinity_session_id,omitempty"`
	EffectiveSessionKey  string                   `json:"effective_session_key,omitempty"`
	IsFork               bool                     `json:"is_fork,omitempty"`
	IsCompaction         bool                     `json:"is_compaction,omitempty"`
	RecordKind           string                   `json:"record_kind,omitempty"`
	Stream               bool                     `json:"stream,omitempty"`
	Failed               bool                     `json:"failed,omitempty"`
	StatusCode           int                      `json:"status_code,omitempty"`
	LatencyMs            int64                    `json:"latency_ms,omitempty"`
	TTFTMs               int64                    `json:"ttft_ms,omitempty"`
	Tokens               tracker.NormalizedTokens `json:"tokens"`
	TraceEnabled         bool                     `json:"trace_enabled,omitempty"`
	HasFingerprint       bool                     `json:"has_fingerprint,omitempty"`
	PrevID               string                   `json:"prev_id,omitempty"`
	MissReason           string                   `json:"miss_reason,omitempty"`
	BodyBytes            int                      `json:"body_bytes,omitempty"`
	MessageCount         int                      `json:"message_count,omitempty"`
	IsContinuation       bool                     `json:"is_continuation,omitempty"`
	ParseSkipped         bool                     `json:"parse_skipped,omitempty"`
}

// RecordsResponse contains paginated record summaries.
type RecordsResponse struct {
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
	Items  []RecordSummary `json:"items"`
}

// DiffResponse contains the current record, previous record, and field-level diff.
type DiffResponse struct {
	Record     *tracker.Record        `json:"record"`
	Previous   *tracker.Record        `json:"previous"`
	Comparison tracker.DiffComparison `json:"comparison"`
}

// PurgeResult returns statistics after deleting records.
type PurgeResult struct {
	DeletedFiles   int   `json:"deleted_files"`
	DeletedRecords int   `json:"deleted_records"`
	UsedBytes      int64 `json:"used_bytes"`
	FileCount      int   `json:"file_count"`
	RecordCount    int   `json:"record_count"`
}

// StatusResponse describes the storage subsystem status.
type StatusResponse struct {
	TraceEnabled     bool   `json:"trace_enabled"`
	OmitBodyAfter    bool   `json:"omit_body_after"`
	RetentionDays    int    `json:"retention_days"`
	MaxDiskMB        int    `json:"max_disk_mb"`
	MaxMessageHashes int    `json:"max_message_hashes"`
	DataDir          string `json:"data_dir"`
	UsedBytes        int64  `json:"used_bytes"`
	FileCount        int    `json:"file_count"`
	RecordCount      int    `json:"record_count"`
}

type sessionModelKey struct {
	SessionKey string
	Model      string
}

// Store provides concurrency-safe JSONL storage, retention enforcement, and aggregation queries.
type Store struct {
	mu sync.RWMutex

	dataDir string
	cfg     abi.PluginConfig
	nowFn   func() time.Time

	appendCount int64
	seq         uint64

	records        []RecordSummary
	recordFileByID map[string]string

	lastBySessionModel map[sessionModelKey]*tracker.Record

	hotLRU    *list.List
	hotLRUMap map[string]*list.Element
	maxHot    int

	done   chan struct{}
	closed bool
}

// Open initializes the store, creates dataDir if needed, enforces retention, and reloads state.
func Open(dataDir string, cfg abi.PluginConfig, nowFn func() time.Time) (*Store, error) {
	if nowFn == nil {
		nowFn = time.Now
	}
	if dataDir == "" {
		dataDir = cfg.DataDir
	}
	if dataDir == "" {
		dataDir = ".cache_tracker_data"
	}
	if strings.HasPrefix(dataDir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dataDir = filepath.Join(home, dataDir[2:])
		}
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create data dir: %w", err)
	}

	s := &Store{
		dataDir:            dataDir,
		cfg:                cfg,
		nowFn:              nowFn,
		records:            make([]RecordSummary, 0),
		recordFileByID:     make(map[string]string),
		lastBySessionModel: make(map[sessionModelKey]*tracker.Record),
		hotLRU:             list.New(),
		hotLRUMap:          make(map[string]*list.Element),
		maxHot:             500,
		done:               make(chan struct{}),
	}

	s.mu.Lock()
	changed := s.enforceRetentionLocked()
	if !changed {
		s.reloadRecordsLocked()
	}
	s.mu.Unlock()

	// Periodic retention ticker (every 10 minutes)
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.mu.Lock()
				s.enforceRetentionLocked()
				s.mu.Unlock()
			case <-s.done:
				return
			}
		}
	}()

	return s, nil
}

// Close gracefully terminates background routines.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.done)
	}
	return nil
}

func (s *Store) now() time.Time {
	if s.nowFn != nil {
		return s.nowFn()
	}
	return time.Now()
}

// UpdateConfig updates runtime configuration and immediately triggers retention enforcement.
func (s *Store) UpdateConfig(cfg abi.PluginConfig) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
	s.enforceRetentionLocked()
}

// EnforceRetention checks age and disk capacity limits, pruning files if necessary.
// It returns true if any files were deleted or trimmed and in-memory state was reloaded.
func (s *Store) EnforceRetention() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enforceRetentionLocked()
}

// Append persists a new record, computes display model, PrevID, MissReason, and updates LRU and indexes.
func (s *Store) Append(rec tracker.Record) (tracker.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now().In(time.Local)
	if rec.Timestamp.IsZero() {
		rec.Timestamp = now
	} else {
		rec.Timestamp = rec.Timestamp.In(time.Local)
	}

	if rec.RecordKind == "" {
		rec.RecordKind = "main"
	}

	rec.DisplayModel = strings.TrimSpace(rec.Alias)
	if rec.DisplayModel == "" {
		rec.DisplayModel = strings.TrimSpace(rec.Model)
	}
	if rec.DisplayModel == "" {
		rec.DisplayModel = "unknown"
	}

	if rec.ID == "" {
		s.seq++
		rec.ID = fmt.Sprintf("%x-%08x", rec.Timestamp.UnixNano(), s.seq)
	}

	rec.EffectiveSessionKey = tracker.ResolveSessionKey(&rec)

	var prev *tracker.Record
	sessKey := sessionModelKey{SessionKey: rec.EffectiveSessionKey, Model: rec.Model}
	if rec.EffectiveSessionKey != "" && rec.Model != "" {
		prev = s.lastBySessionModel[sessKey]
	}

	if !rec.Failed {
		if prev != nil {
			rec.PrevID = prev.ID
		}
		reason, _ := tracker.ClassifyMissReason(&rec, prev)
		rec.MissReason = reason

		if rec.EffectiveSessionKey != "" && rec.Model != "" {
			recCopy := rec
			s.lastBySessionModel[sessKey] = &recCopy
		}
	} else {
		rec.MissReason = ""
	}

	dateStr := rec.Timestamp.In(time.Local).Format("2006-01-02")
	fileName := fmt.Sprintf("usage-%s.jsonl", dateStr)
	filePath := filepath.Join(s.dataDir, fileName)

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return rec, fmt.Errorf("failed to open usage file: %w", err)
	}

	lineBytes, err := json.Marshal(rec)
	if err != nil {
		f.Close()
		return rec, fmt.Errorf("failed to marshal record: %w", err)
	}
	lineBytes = append(lineBytes, '\n')
	if _, err := f.Write(lineBytes); err != nil {
		f.Close()
		return rec, fmt.Errorf("failed to write record: %w", err)
	}
	f.Close()

	summary := recordToSummary(rec)
	s.records = append(s.records, summary)
	s.recordFileByID[rec.ID] = fileName
	s.putHotLRU(&rec)

	s.appendCount++
	if s.appendCount%100 == 0 {
		s.enforceRetentionLocked()
	}

	return rec, nil
}

func recordToSummary(rec tracker.Record) RecordSummary {
	s := RecordSummary{
		ID:                   rec.ID,
		Timestamp:            rec.Timestamp,
		TraceID:              rec.TraceID,
		RequestID:            rec.RequestID,
		Provider:             rec.Provider,
		ExecutorType:         rec.ExecutorType,
		Model:                rec.Model,
		Alias:                rec.Alias,
		DisplayModel:         rec.DisplayModel,
		AuthID:               rec.AuthID,
		SessionID:            rec.SessionID,
		ParentSessionID:      rec.ParentSessionID,
		LCPAffinitySessionID: rec.LCPAffinitySessionID,
		EffectiveSessionKey:  rec.EffectiveSessionKey,
		IsFork:               rec.IsFork,
		IsCompaction:         rec.IsCompaction,
		RecordKind:           rec.RecordKind,
		Stream:               rec.Stream,
		Failed:               rec.Failed,
		StatusCode:           rec.StatusCode,
		LatencyMs:            rec.LatencyMs,
		TTFTMs:               rec.TTFTMs,
		Tokens:               rec.Tokens,
		TraceEnabled:         rec.TraceEnabled,
		HasFingerprint:       rec.HasFingerprint,
		PrevID:               rec.PrevID,
		MissReason:           rec.MissReason,
	}
	if rec.Fingerprint != nil {
		s.BodyBytes = rec.Fingerprint.BodyBytes
		s.MessageCount = rec.Fingerprint.MessageCount
		s.IsContinuation = rec.Fingerprint.IsContinuation
		s.ParseSkipped = rec.Fingerprint.ParseSkipped
	}
	return s
}

func (s *Store) putHotLRU(rec *tracker.Record) {
	if rec == nil || rec.ID == "" {
		return
	}
	if elem, ok := s.hotLRUMap[rec.ID]; ok {
		s.hotLRU.MoveToFront(elem)
		elem.Value = rec
		return
	}
	elem := s.hotLRU.PushFront(rec)
	s.hotLRUMap[rec.ID] = elem
	for s.hotLRU.Len() > s.maxHot {
		oldest := s.hotLRU.Back()
		if oldest == nil {
			break
		}
		s.hotLRU.Remove(oldest)
		oldRec := oldest.Value.(*tracker.Record)
		delete(s.hotLRUMap, oldRec.ID)
	}
}

func (s *Store) getRecordLocked(id string) (*tracker.Record, error) {
	if elem, ok := s.hotLRUMap[id]; ok {
		s.hotLRU.MoveToFront(elem)
		return elem.Value.(*tracker.Record), nil
	}

	fileName := s.recordFileByID[id]
	if fileName != "" {
		rec, err := s.scanFileForID(filepath.Join(s.dataDir, fileName), id)
		if err == nil {
			s.putHotLRU(rec)
			return rec, nil
		}
	}

	// Fallback: search all usage-*.jsonl files in dataDir
	files, err := os.ReadDir(s.dataDir)
	if err == nil {
		for _, f := range files {
			if strings.HasPrefix(f.Name(), "usage-") && strings.HasSuffix(f.Name(), ".jsonl") {
				if f.Name() == fileName {
					continue
				}
				rec, err := s.scanFileForID(filepath.Join(s.dataDir, f.Name()), id)
				if err == nil {
					s.putHotLRU(rec)
					s.recordFileByID[id] = f.Name()
					return rec, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("record not found: %s", id)
}

func (s *Store) scanFileForID(path, id string) (*tracker.Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 4*1024*1024)
	for {
		line, isPrefix, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		for isPrefix {
			var extra []byte
			extra, isPrefix, err = reader.ReadLine()
			if err != nil {
				break
			}
			line = append(line, extra...)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec tracker.Record
		if err := json.Unmarshal(line, &rec); err == nil && rec.ID == id {
			return &rec, nil
		}
	}
	return nil, fmt.Errorf("record not found in file")
}

// Diff returns side-by-side details of the requested record and its prior same-session turn.
func (s *Store) Diff(id string) (*DiffResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cur, err := s.getRecordLocked(id)
	if err != nil {
		return nil, err
	}

	var prev *tracker.Record
	if cur.PrevID != "" {
		prev, _ = s.getRecordLocked(cur.PrevID)
	}

	_, diff := tracker.ClassifyMissReason(cur, prev)
	if cur.MissReason != "" {
		diff.MissReason = cur.MissReason
	}

	return &DiffResponse{
		Record:     cur,
		Previous:   prev,
		Comparison: diff,
	}, nil
}

// Purge removes records older than olderThanDays or all records when all is true.
func (s *Store) Purge(olderThanDays int, all bool) (PurgeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result PurgeResult

	if all {
		files, err := os.ReadDir(s.dataDir)
		if err == nil {
			for _, f := range files {
				if strings.HasPrefix(f.Name(), "usage-") && strings.HasSuffix(f.Name(), ".jsonl") {
					os.Remove(filepath.Join(s.dataDir, f.Name()))
					result.DeletedFiles++
				}
			}
		}
		result.DeletedRecords = len(s.records)
		s.records = make([]RecordSummary, 0)
		s.recordFileByID = make(map[string]string)
		s.lastBySessionModel = make(map[sessionModelKey]*tracker.Record)
		s.hotLRU = list.New()
		s.hotLRUMap = make(map[string]*list.Element)

		result.UsedBytes = s.usedBytesLocked()
		result.FileCount = s.fileCountLocked()
		result.RecordCount = len(s.records)
		return result, nil
	}

	if olderThanDays > 0 {
		now := s.now().In(time.Local)
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		cutoff := today.AddDate(0, 0, -olderThanDays)

		recordsBefore := len(s.records)
		files, err := os.ReadDir(s.dataDir)
		if err == nil {
			for _, f := range files {
				if strings.HasPrefix(f.Name(), "usage-") && strings.HasSuffix(f.Name(), ".jsonl") {
					dateStr := strings.TrimSuffix(strings.TrimPrefix(f.Name(), "usage-"), ".jsonl")
					if fDate, err := time.ParseInLocation("2006-01-02", dateStr, now.Location()); err == nil {
						if fDate.Before(cutoff) {
							os.Remove(filepath.Join(s.dataDir, f.Name()))
							result.DeletedFiles++
						}
					}
				}
			}
		}
		s.reloadRecordsLocked()
		result.DeletedRecords = recordsBefore - len(s.records)
		if result.DeletedRecords < 0 {
			result.DeletedRecords = 0
		}
	}

	result.UsedBytes = s.usedBytesLocked()
	result.FileCount = s.fileCountLocked()
	result.RecordCount = len(s.records)
	return result, nil
}

// Status returns current storage telemetry.
func (s *Store) Status(omitBodyAfter bool) StatusResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return StatusResponse{
		TraceEnabled:     s.cfg.TraceEnabled,
		OmitBodyAfter:    omitBodyAfter,
		RetentionDays:    s.cfg.RetentionDays,
		MaxDiskMB:        s.cfg.MaxDiskMB,
		MaxMessageHashes: s.cfg.MaxMessageHashes,
		DataDir:          s.dataDir,
		UsedBytes:        s.usedBytesLocked(),
		FileCount:        s.fileCountLocked(),
		RecordCount:      len(s.records),
	}
}

func (s *Store) usedBytesLocked() int64 {
	var total int64
	files, err := os.ReadDir(s.dataDir)
	if err == nil {
		for _, f := range files {
			if strings.HasPrefix(f.Name(), "usage-") && strings.HasSuffix(f.Name(), ".jsonl") {
				if fi, err := f.Info(); err == nil {
					total += fi.Size()
				}
			}
		}
	}
	return total
}

func (s *Store) fileCountLocked() int {
	var count int
	files, err := os.ReadDir(s.dataDir)
	if err == nil {
		for _, f := range files {
			if strings.HasPrefix(f.Name(), "usage-") && strings.HasSuffix(f.Name(), ".jsonl") {
				count++
			}
		}
	}
	return count
}

func (s *Store) enforceRetentionLocked() bool {
	now := s.now().In(time.Local)
	changed := false

	// 1. Age-based pruning
	if s.cfg.RetentionDays > 0 {
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		cutoff := today.AddDate(0, 0, -s.cfg.RetentionDays)

		files, err := os.ReadDir(s.dataDir)
		if err == nil {
			for _, f := range files {
				if strings.HasPrefix(f.Name(), "usage-") && strings.HasSuffix(f.Name(), ".jsonl") {
					dateStr := strings.TrimSuffix(strings.TrimPrefix(f.Name(), "usage-"), ".jsonl")
					if fDate, err := time.ParseInLocation("2006-01-02", dateStr, now.Location()); err == nil {
						if fDate.Before(cutoff) {
							if err := os.Remove(filepath.Join(s.dataDir, f.Name())); err == nil {
								changed = true
							}
						}
					}
				}
			}
		}
	}

	// 2. Disk cap pruning
	maxMB := s.cfg.MaxDiskMB
	if maxMB <= 0 {
		maxMB = 200
	}
	maxBytes := int64(maxMB) * 1024 * 1024

	type fInfo struct {
		name string
		size int64
	}

	var files []fInfo
	var totalBytes int64

	dirEntries, err := os.ReadDir(s.dataDir)
	if err == nil {
		for _, f := range dirEntries {
			if strings.HasPrefix(f.Name(), "usage-") && strings.HasSuffix(f.Name(), ".jsonl") {
				if fi, err := f.Info(); err == nil {
					files = append(files, fInfo{name: f.Name(), size: fi.Size()})
					totalBytes += fi.Size()
				}
			}
		}
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].name < files[j].name
	})

	for totalBytes > maxBytes && len(files) > 1 {
		oldest := files[0]
		if err := os.Remove(filepath.Join(s.dataDir, oldest.name)); err == nil {
			changed = true
		}
		totalBytes -= oldest.size
		files = files[1:]
	}

	if len(files) == 1 && totalBytes > maxBytes {
		targetBytes := maxBytes * 80 / 100
		if s.trimSingleFile(filepath.Join(s.dataDir, files[0].name), targetBytes) {
			changed = true
		}
	}

	if changed {
		s.reloadRecordsLocked()
	}
	return changed
}

func (s *Store) trimSingleFile(filePath string, targetBytes int64) bool {
	f, err := os.Open(filePath)
	if err != nil {
		return false
	}

	var lines [][]byte
	reader := bufio.NewReaderSize(f, 4*1024*1024)
	for {
		line, isPrefix, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF {
				break
			}
			f.Close()
			return false
		}
		for isPrefix {
			var extra []byte
			extra, isPrefix, err = reader.ReadLine()
			if err != nil {
				break
			}
			line = append(line, extra...)
		}
		if len(bytes.TrimSpace(line)) > 0 {
			lineCopy := make([]byte, len(line))
			copy(lineCopy, line)
			lines = append(lines, lineCopy)
		}
	}
	f.Close()

	if len(lines) == 0 {
		return false
	}

	var kept [][]byte
	var keptBytes int64
	for i := len(lines) - 1; i >= 0; i-- {
		lineSize := int64(len(lines[i])) + 1
		if keptBytes+lineSize <= targetBytes || len(kept) == 0 {
			kept = append(kept, lines[i])
			keptBytes += lineSize
		} else {
			break
		}
	}

	if len(kept) == len(lines) {
		return false
	}

	// Reverse to original order
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}

	tmp, err := os.CreateTemp(s.dataDir, "trim-*.tmp")
	if err != nil {
		return false
	}
	defer os.Remove(tmp.Name())

	writer := bufio.NewWriter(tmp)
	for _, l := range kept {
		writer.Write(l)
		writer.WriteByte('\n')
	}
	if err := writer.Flush(); err != nil {
		tmp.Close()
		return false
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false
	}
	tmp.Close()

	if err := os.Rename(tmp.Name(), filePath); err != nil {
		return false
	}
	return true
}

func (s *Store) reloadRecordsLocked() {
	s.records = make([]RecordSummary, 0)
	s.recordFileByID = make(map[string]string)
	s.lastBySessionModel = make(map[sessionModelKey]*tracker.Record)
	s.hotLRU = list.New()
	s.hotLRUMap = make(map[string]*list.Element)

	dirEntries, err := os.ReadDir(s.dataDir)
	if err != nil {
		return
	}

	var jsonlNames []string
	for _, f := range dirEntries {
		if strings.HasPrefix(f.Name(), "usage-") && strings.HasSuffix(f.Name(), ".jsonl") {
			jsonlNames = append(jsonlNames, f.Name())
		}
	}
	sort.Strings(jsonlNames)

	for _, fn := range jsonlNames {
		filePath := filepath.Join(s.dataDir, fn)
		f, err := os.Open(filePath)
		if err != nil {
			continue
		}

		reader := bufio.NewReaderSize(f, 4*1024*1024)
		for {
			line, isPrefix, err := reader.ReadLine()
			if err != nil {
				if err == io.EOF {
					break
				}
				break
			}
			for isPrefix {
				var extra []byte
				extra, isPrefix, err = reader.ReadLine()
				if err != nil {
					break
				}
				line = append(line, extra...)
			}
			if len(bytes.TrimSpace(line)) == 0 {
				continue
			}
			var rec tracker.Record
			if err := json.Unmarshal(line, &rec); err != nil {
				continue
			}

			if rec.RecordKind == "" {
				rec.RecordKind = "main"
			}

			rec.DisplayModel = strings.TrimSpace(rec.Alias)
			if rec.DisplayModel == "" {
				rec.DisplayModel = strings.TrimSpace(rec.Model)
			}
			if rec.DisplayModel == "" {
				rec.DisplayModel = "unknown"
			}

			if rec.EffectiveSessionKey == "" {
				rec.EffectiveSessionKey = tracker.ResolveSessionKey(&rec)
			}

			summary := recordToSummary(rec)
			s.records = append(s.records, summary)
			s.recordFileByID[rec.ID] = fn
			s.putHotLRU(&rec)

			if !rec.Failed && rec.EffectiveSessionKey != "" && rec.Model != "" {
				recCopy := rec
				key := sessionModelKey{SessionKey: rec.EffectiveSessionKey, Model: rec.Model}
				s.lastBySessionModel[key] = &recCopy
			}
		}
		f.Close()
	}
}

func (s *Store) resolveRange(r string, from, to, now time.Time) (time.Time, time.Time, string) {
	nowLocal := now.In(time.Local)
	startOfToday := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, nowLocal.Location())
	endOfToday := startOfToday.AddDate(0, 0, 1)

	switch strings.ToLower(strings.TrimSpace(r)) {
	case "today":
		return startOfToday, endOfToday, "today"
	case "yesterday":
		return startOfToday.AddDate(0, 0, -1), startOfToday, "yesterday"
	case "7d":
		return startOfToday.AddDate(0, 0, -6), endOfToday, "7d"
	case "30d":
		return startOfToday.AddDate(0, 0, -29), endOfToday, "30d"
	case "custom":
		if to.IsZero() {
			to = endOfToday
		}
		if from.IsZero() {
			from = startOfToday.AddDate(0, 0, -7)
		}
		return from, to, "custom"
	default:
		if !from.IsZero() || !to.IsZero() {
			if to.IsZero() {
				to = endOfToday
			}
			if from.IsZero() {
				from = startOfToday.AddDate(0, 0, -7)
			}
			return from, to, "custom"
		}
		return startOfToday, endOfToday, "today"
	}
}

// Summary calculates aggregated metrics and time-series series for the dashboard.
func (s *Store) Summary(q SummaryQuery) SummaryResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	from, to, rangeStr := s.resolveRange(q.Range, q.From, q.To, s.now())
	span := to.Sub(from)
	granularity := "day"
	if span <= 48*time.Hour {
		granularity = "hour"
	}

	kind := strings.TrimSpace(q.Kind)
	if kind == "" {
		kind = "main"
	}

	var totals SummaryTotals
	var coldHits, warmHits int64
	var ttftSum, latencySum int64
	var ttftCount, latencyCount int64

	type mAccum struct {
		displayModel   string
		providers      map[string]struct{}
		requests       int64
		hitRequests    int64
		failedRequests int64
		effectiveInput int64
		cacheRead      int64
		cacheWrite     int64
	}

	type tAccum struct {
		bucket         string
		displayModel   string
		requests       int64
		hitRequests    int64
		effectiveInput int64
		cacheRead      int64
	}

	type mrAccum struct {
		reason         string
		count          int64
		effectiveInput int64
	}

	modelMap := make(map[string]*mAccum)
	overallTimeMap := make(map[string]*tAccum)
	type mtKey struct {
		bucket string
		model  string
	}
	modelTimeMap := make(map[mtKey]*tAccum)
	missReasonMap := make(map[string]*mrAccum)

	availModels := make(map[string]struct{})
	availProviders := make(map[string]struct{})

	for _, rec := range s.records {
		if rec.DisplayModel != "" {
			availModels[rec.DisplayModel] = struct{}{}
		}
		if rec.Provider != "" {
			availProviders[rec.Provider] = struct{}{}
		}

		if rec.Timestamp.Before(from) || rec.Timestamp.After(to) {
			continue
		}

		if q.Model != "" && rec.DisplayModel != q.Model && rec.Model != q.Model {
			continue
		}
		if q.Provider != "" && rec.Provider != q.Provider {
			continue
		}
		recKind := rec.RecordKind
		if recKind == "" {
			recKind = "main"
		}
		if kind != "all" && recKind != kind {
			continue
		}

		// Model accumulator setup
		mAcc, ok := modelMap[rec.DisplayModel]
		if !ok {
			mAcc = &mAccum{
				displayModel: rec.DisplayModel,
				providers:    make(map[string]struct{}),
			}
			modelMap[rec.DisplayModel] = mAcc
		}
		if rec.Provider != "" {
			mAcc.providers[rec.Provider] = struct{}{}
		}

		if rec.Failed {
			totals.FailedRequests++
			mAcc.failedRequests++
			continue
		}

		// Non-failed request
		totals.Requests++
		isHit := rec.Tokens.RequestHit || rec.Tokens.CacheRead > 0
		if isHit {
			totals.HitRequests++
		} else {
			totals.MissRequests++
		}

		totals.EffectiveInput += rec.Tokens.EffectiveInput
		totals.CacheRead += rec.Tokens.CacheRead
		totals.CacheWrite += rec.Tokens.CacheWrite
		totals.UncachedInput += rec.Tokens.UncachedInput

		isCold := (rec.PrevID == "" || rec.MissReason == "cold_start")
		if isCold {
			totals.ColdStarts++
			if isHit {
				coldHits++
			}
		} else {
			totals.WarmRequests++
			if isHit {
				warmHits++
			}
		}

		if rec.TTFTMs > 0 {
			ttftSum += rec.TTFTMs
			ttftCount++
		}
		if rec.LatencyMs > 0 {
			latencySum += rec.LatencyMs
			latencyCount++
		}

		// Update model stats
		mAcc.requests++
		if isHit {
			mAcc.hitRequests++
		}
		mAcc.effectiveInput += rec.Tokens.EffectiveInput
		mAcc.cacheRead += rec.Tokens.CacheRead
		mAcc.cacheWrite += rec.Tokens.CacheWrite

		// Time bucket
		var bucketStr string
		if granularity == "hour" {
			bucketStr = rec.Timestamp.In(time.Local).Format("2006-01-02 15:00")
		} else {
			bucketStr = rec.Timestamp.In(time.Local).Format("2006-01-02")
		}

		otAcc, ok := overallTimeMap[bucketStr]
		if !ok {
			otAcc = &tAccum{bucket: bucketStr}
			overallTimeMap[bucketStr] = otAcc
		}
		otAcc.requests++
		if isHit {
			otAcc.hitRequests++
		}
		otAcc.effectiveInput += rec.Tokens.EffectiveInput
		otAcc.cacheRead += rec.Tokens.CacheRead

		mtk := mtKey{bucket: bucketStr, model: rec.DisplayModel}
		mtAcc, ok := modelTimeMap[mtk]
		if !ok {
			mtAcc = &tAccum{bucket: bucketStr, displayModel: rec.DisplayModel}
			modelTimeMap[mtk] = mtAcc
		}
		mtAcc.requests++
		if isHit {
			mtAcc.hitRequests++
		}
		mtAcc.effectiveInput += rec.Tokens.EffectiveInput
		mtAcc.cacheRead += rec.Tokens.CacheRead

		// Miss reason
		if !isHit && rec.Tokens.EffectiveInput > 0 {
			reason := rec.MissReason
			if reason == "" {
				reason = "upstream_cache_miss"
			}
			mr, ok := missReasonMap[reason]
			if !ok {
				mr = &mrAccum{reason: reason}
				missReasonMap[reason] = mr
			}
			mr.count++
			mr.effectiveInput += rec.Tokens.EffectiveInput
		}
	}

	if totals.EffectiveInput > 0 {
		totals.TokenHitRate = (float64(totals.CacheRead) / float64(totals.EffectiveInput)) * 100.0
	}
	if totals.Requests > 0 {
		totals.RequestHitRate = (float64(totals.HitRequests) / float64(totals.Requests)) * 100.0
	}
	if totals.ColdStarts > 0 {
		totals.ColdHitRate = (float64(coldHits) / float64(totals.ColdStarts)) * 100.0
	}
	if totals.WarmRequests > 0 {
		totals.WarmHitRate = (float64(warmHits) / float64(totals.WarmRequests)) * 100.0
	}
	if ttftCount > 0 {
		totals.AvgTTFTMs = float64(ttftSum) / float64(ttftCount)
	}
	if latencyCount > 0 {
		totals.AvgLatencyMs = float64(latencySum) / float64(latencyCount)
	}

	var models []ModelStats
	for _, m := range modelMap {
		var provList []string
		for p := range m.providers {
			provList = append(provList, p)
		}
		sort.Strings(provList)

		ms := ModelStats{
			DisplayModel:   m.displayModel,
			Providers:      provList,
			Requests:       m.requests,
			HitRequests:    m.hitRequests,
			FailedRequests: m.failedRequests,
			EffectiveInput: m.effectiveInput,
			CacheRead:      m.cacheRead,
			CacheWrite:     m.cacheWrite,
		}
		if m.effectiveInput > 0 {
			ms.TokenHitRate = (float64(m.cacheRead) / float64(m.effectiveInput)) * 100.0
		}
		if m.requests > 0 {
			ms.RequestHitRate = (float64(m.hitRequests) / float64(m.requests)) * 100.0
		}
		models = append(models, ms)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].Requests != models[j].Requests {
			return models[i].Requests > models[j].Requests
		}
		return models[i].DisplayModel < models[j].DisplayModel
	})

	var overallSeries []TimeBucket
	for _, t := range overallTimeMap {
		tb := TimeBucket{
			Bucket:         t.bucket,
			Requests:       t.requests,
			HitRequests:    t.hitRequests,
			EffectiveInput: t.effectiveInput,
			CacheRead:      t.cacheRead,
		}
		if t.effectiveInput > 0 {
			tb.TokenHitRate = (float64(t.cacheRead) / float64(t.effectiveInput)) * 100.0
		}
		if t.requests > 0 {
			tb.RequestHitRate = (float64(t.hitRequests) / float64(t.requests)) * 100.0
		}
		overallSeries = append(overallSeries, tb)
	}
	sort.Slice(overallSeries, func(i, j int) bool {
		return overallSeries[i].Bucket < overallSeries[j].Bucket
	})

	var modelSeries []TimeBucket
	for _, t := range modelTimeMap {
		tb := TimeBucket{
			Bucket:         t.bucket,
			DisplayModel:   t.displayModel,
			Requests:       t.requests,
			HitRequests:    t.hitRequests,
			EffectiveInput: t.effectiveInput,
			CacheRead:      t.cacheRead,
		}
		if t.effectiveInput > 0 {
			tb.TokenHitRate = (float64(t.cacheRead) / float64(t.effectiveInput)) * 100.0
		}
		if t.requests > 0 {
			tb.RequestHitRate = (float64(t.hitRequests) / float64(t.requests)) * 100.0
		}
		modelSeries = append(modelSeries, tb)
	}
	sort.Slice(modelSeries, func(i, j int) bool {
		if modelSeries[i].Bucket != modelSeries[j].Bucket {
			return modelSeries[i].Bucket < modelSeries[j].Bucket
		}
		return modelSeries[i].DisplayModel < modelSeries[j].DisplayModel
	})

	var missReasons []MissReasonStat
	for _, mr := range missReasonMap {
		missReasons = append(missReasons, MissReasonStat{
			Reason:         mr.reason,
			Count:          mr.count,
			EffectiveInput: mr.effectiveInput,
		})
	}
	sort.Slice(missReasons, func(i, j int) bool {
		if missReasons[i].Count != missReasons[j].Count {
			return missReasons[i].Count > missReasons[j].Count
		}
		return missReasons[i].Reason < missReasons[j].Reason
	})

	var sortedAvailModels []string
	for m := range availModels {
		sortedAvailModels = append(sortedAvailModels, m)
	}
	sort.Strings(sortedAvailModels)

	var sortedAvailProviders []string
	for p := range availProviders {
		sortedAvailProviders = append(sortedAvailProviders, p)
	}
	sort.Strings(sortedAvailProviders)

	return SummaryResponse{
		Range:              rangeStr,
		From:               from,
		To:                 to,
		Granularity:        granularity,
		Totals:             totals,
		Models:             models,
		OverallSeries:      overallSeries,
		ModelSeries:        modelSeries,
		MissReasons:        missReasons,
		AvailableModels:    sortedAvailModels,
		AvailableProviders: sortedAvailProviders,
	}
}

// Records searches, filters, and paginates request summaries in reverse chronological order.
func (s *Store) Records(q RecordsQuery) RecordsResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var from, to time.Time
	hasTimeFilter := false
	if q.Range != "" || !q.From.IsZero() || !q.To.IsZero() {
		from, to, _ = s.resolveRange(q.Range, q.From, q.To, s.now())
		hasTimeFilter = true
	}

	queryLower := strings.ToLower(strings.TrimSpace(q.Query))
	status := strings.ToLower(strings.TrimSpace(q.Status))
	kind := strings.TrimSpace(q.Kind)

	var filtered []RecordSummary
	for i := len(s.records) - 1; i >= 0; i-- {
		rec := s.records[i]

		if hasTimeFilter {
			if rec.Timestamp.Before(from) || rec.Timestamp.After(to) {
				continue
			}
		}

		if q.Model != "" && rec.DisplayModel != q.Model && rec.Model != q.Model {
			continue
		}
		if q.Provider != "" && rec.Provider != q.Provider {
			continue
		}
		recKind := rec.RecordKind
		if recKind == "" {
			recKind = "main"
		}
		if kind != "" && kind != "all" && recKind != kind {
			continue
		}

		if status != "" && status != "all" {
			isHit := rec.Tokens.RequestHit || rec.Tokens.CacheRead > 0
			switch status {
			case "hit":
				if rec.Failed || !isHit {
					continue
				}
			case "miss":
				if rec.Failed || isHit {
					continue
				}
			case "failed":
				if !rec.Failed {
					continue
				}
			}
		}

		if queryLower != "" {
			matched := strings.Contains(strings.ToLower(rec.ID), queryLower) ||
				strings.Contains(strings.ToLower(rec.TraceID), queryLower) ||
				strings.Contains(strings.ToLower(rec.RequestID), queryLower) ||
				strings.Contains(strings.ToLower(rec.SessionID), queryLower) ||
				strings.Contains(strings.ToLower(rec.AuthID), queryLower) ||
				strings.Contains(strings.ToLower(rec.Model), queryLower) ||
				strings.Contains(strings.ToLower(rec.Alias), queryLower) ||
				strings.Contains(strings.ToLower(rec.DisplayModel), queryLower) ||
				strings.Contains(strings.ToLower(rec.MissReason), queryLower)
			if !matched {
				continue
			}
		}

		filtered = append(filtered, rec)
	}

	total := len(filtered)
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}

	items := filtered[offset:end]
	if items == nil {
		items = make([]RecordSummary, 0)
	}

	return RecordsResponse{
		Total:  total,
		Limit:  limit,
		Offset: offset,
		Items:  items,
	}
}
