package storage_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/fingerprint"
	"cpa-cache-tracker/storage"
	"cpa-cache-tracker/tracker"
)

func TestStoreAppendReloadAndPrevMatchingByResolvedModel(t *testing.T) {
	dataDir := t.TempDir()
	fixedNow := time.Date(2026, 11, 17, 10, 0, 0, 0, time.Local)
	nowFn := func() time.Time { return fixedNow }

	cfg := abi.PluginConfig{
		TraceEnabled:  true,
		RetentionDays: 14,
		MaxDiskMB:     200,
		DataDir:       dataDir,
	}

	store, err := storage.Open(dataDir, cfg, nowFn)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	sessID := "session-user-123"

	// Record 1: pool-1, Model m-a
	rec1 := tracker.Record{
		SessionID: sessID,
		Alias:     "pool-1",
		Model:     "m-a",
		Provider:  "test-provider",
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 1000,
			CacheRead:      0,
			UncachedInput:  1000,
			Total:          1000,
		},
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			SystemHash: "sys-hash",
		},
	}
	saved1, err := store.Append(rec1)
	if err != nil {
		t.Fatalf("Append rec1 failed: %v", err)
	}
	if saved1.DisplayModel != "pool-1" {
		t.Fatalf("expected DisplayModel pool-1, got %q", saved1.DisplayModel)
	}
	if saved1.PrevID != "" {
		t.Fatalf("expected rec1 PrevID empty, got %q", saved1.PrevID)
	}
	if saved1.MissReason != "cold_start" {
		t.Fatalf("expected rec1 MissReason cold_start, got %q", saved1.MissReason)
	}

	// Record 2: pool-1, Model m-b (different underlying model under same alias & session)
	rec2 := tracker.Record{
		SessionID: sessID,
		Alias:     "pool-1",
		Model:     "m-b",
		Provider:  "test-provider",
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 1000,
			CacheRead:      0,
			UncachedInput:  1000,
			Total:          1000,
		},
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			SystemHash: "sys-hash",
		},
	}
	saved2, err := store.Append(rec2)
	if err != nil {
		t.Fatalf("Append rec2 failed: %v", err)
	}
	if saved2.DisplayModel != "pool-1" {
		t.Fatalf("expected DisplayModel pool-1, got %q", saved2.DisplayModel)
	}
	if saved2.PrevID != "" {
		t.Fatalf("expected rec2 PrevID empty (first m-b turn), got %q", saved2.PrevID)
	}

	// Now simulate restart / reload by opening a new store on the same dataDir
	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	storeReloaded, err := storage.Open(dataDir, cfg, nowFn)
	if err != nil {
		t.Fatalf("Open reloaded failed: %v", err)
	}
	defer storeReloaded.Close()

	// Record 3: pool-1, Model m-a (should match rec1's ID across reload!)
	rec3 := tracker.Record{
		SessionID: sessID,
		Alias:     "pool-1",
		Model:     "m-a",
		Provider:  "test-provider",
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 1200,
			CacheRead:      1000,
			RequestHit:     true,
			Total:          1200,
		},
		TraceEnabled:   true,
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			SystemHash: "sys-hash",
		},
	}
	saved3, err := storeReloaded.Append(rec3)
	if err != nil {
		t.Fatalf("Append rec3 failed: %v", err)
	}

	// Crucial check: PrevID must point to saved1.ID (model m-a), NOT saved2.ID (model m-b)!
	if saved3.PrevID != saved1.ID {
		t.Fatalf("expected rec3.PrevID to point to rec1 %q, got %q", saved1.ID, saved3.PrevID)
	}

	// Verify /summary groups under DisplayModel "pool-1"
	summary := storeReloaded.Summary(storage.SummaryQuery{
		Range: "today",
		Kind:  "main",
	})
	if len(summary.Models) != 1 {
		t.Fatalf("expected 1 model stat grouped under alias pool-1, got %d: %+v", len(summary.Models), summary.Models)
	}
	if summary.Models[0].DisplayModel != "pool-1" {
		t.Fatalf("expected DisplayModel pool-1, got %q", summary.Models[0].DisplayModel)
	}
	if summary.Models[0].Requests != 3 {
		t.Fatalf("expected 3 requests for pool-1, got %d", summary.Models[0].Requests)
	}
}

func TestStoreKPIScopingExcludesFailedCompactInternal(t *testing.T) {
	dataDir := t.TempDir()
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	nowFn := func() time.Time { return fixedNow }

	store, err := storage.Open(dataDir, abi.PluginConfig{DataDir: dataDir}, nowFn)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// 1. Main hit: 1000 effective, 800 read
	_, err = store.Append(tracker.Record{
		Model:      "model-main",
		RecordKind: "main",
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 1000,
			CacheRead:      800,
			RequestHit:     true,
		},
	})
	if err != nil {
		t.Fatalf("Append main hit failed: %v", err)
	}

	// 2. Main miss: 1000 effective, 0 read
	_, err = store.Append(tracker.Record{
		Model:      "model-main",
		RecordKind: "main",
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 1000,
			CacheRead:      0,
			RequestHit:     false,
		},
	})
	if err != nil {
		t.Fatalf("Append main miss failed: %v", err)
	}

	// 3. Failed record: Failed == true
	_, err = store.Append(tracker.Record{
		Model:      "model-main",
		RecordKind: "main",
		Failed:     true,
		StatusCode: 500,
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 500,
			CacheRead:      500,
		},
	})
	if err != nil {
		t.Fatalf("Append failed record failed: %v", err)
	}

	// 4. Compact record: RecordKind == "compact"
	_, err = store.Append(tracker.Record{
		Model:      "model-main",
		RecordKind: "compact",
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 2000,
			CacheRead:      2000,
			RequestHit:     true,
		},
	})
	if err != nil {
		t.Fatalf("Append compact record failed: %v", err)
	}

	// 5. Internal subcall: RecordKind == "internal_subcall"
	_, err = store.Append(tracker.Record{
		Model:      "model-main",
		RecordKind: "internal_subcall",
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 3000,
			CacheRead:      3000,
			RequestHit:     true,
		},
	})
	if err != nil {
		t.Fatalf("Append internal subcall failed: %v", err)
	}

	// Default SummaryQuery (Kind: "" or "main")
	summary := store.Summary(storage.SummaryQuery{Range: "today"})

	// Expected Totals for Kind="main":
	// Requests: 2 (hit + miss, failed excluded)
	// HitRequests: 1
	// MissRequests: 1
	// FailedRequests: 1
	// EffectiveInput: 2000 (1000 + 1000)
	// CacheRead: 800
	// TokenHitRate: 800 / 2000 = 40.0%
	// RequestHitRate: 1 / 2 = 50.0%
	if summary.Totals.Requests != 2 {
		t.Fatalf("expected Requests 2, got %d", summary.Totals.Requests)
	}
	if summary.Totals.HitRequests != 1 {
		t.Fatalf("expected HitRequests 1, got %d", summary.Totals.HitRequests)
	}
	if summary.Totals.MissRequests != 1 {
		t.Fatalf("expected MissRequests 1, got %d", summary.Totals.MissRequests)
	}
	if summary.Totals.FailedRequests != 1 {
		t.Fatalf("expected FailedRequests 1, got %d", summary.Totals.FailedRequests)
	}
	if summary.Totals.EffectiveInput != 2000 {
		t.Fatalf("expected EffectiveInput 2000, got %d", summary.Totals.EffectiveInput)
	}
	if summary.Totals.CacheRead != 800 {
		t.Fatalf("expected CacheRead 800, got %d", summary.Totals.CacheRead)
	}
	if summary.Totals.TokenHitRate != 40.0 {
		t.Fatalf("expected TokenHitRate 40.0, got %v", summary.Totals.TokenHitRate)
	}
	if summary.Totals.RequestHitRate != 50.0 {
		t.Fatalf("expected RequestHitRate 50.0, got %v", summary.Totals.RequestHitRate)
	}

	// Verify RecordsQuery filters
	// Query for failed status
	failedResp := store.Records(storage.RecordsQuery{Status: "failed"})
	if failedResp.Total != 1 {
		t.Fatalf("expected 1 failed record, got %d", failedResp.Total)
	}

	// Query for compact kind
	compactResp := store.Records(storage.RecordsQuery{Kind: "compact"})
	if compactResp.Total != 1 {
		t.Fatalf("expected 1 compact record, got %d", compactResp.Total)
	}

	// Query for internal_subcall kind
	internalResp := store.Records(storage.RecordsQuery{Kind: "internal_subcall"})
	if internalResp.Total != 1 {
		t.Fatalf("expected 1 internal record, got %d", internalResp.Total)
	}

	// Query for all
	allResp := store.Records(storage.RecordsQuery{Kind: "all"})
	if allResp.Total != 5 {
		t.Fatalf("expected 5 total records under kind=all, got %d", allResp.Total)
	}
}

func TestStoreRetentionAgePruning(t *testing.T) {
	dataDir := t.TempDir()
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	nowFn := func() time.Time { return fixedNow }

	// Pre-populate usage-2026-11-01.jsonl through usage-2026-11-17.jsonl (17 files)
	for day := 1; day <= 17; day++ {
		fn := fmt.Sprintf("usage-2026-11-%02d.jsonl", day)
		rec := tracker.Record{
			ID:           fmt.Sprintf("rec-day-%02d", day),
			Timestamp:    time.Date(2026, 11, day, 10, 0, 0, 0, time.Local),
			DisplayModel: "test-model",
			Model:        "test-model",
			Tokens: tracker.NormalizedTokens{
				EffectiveInput: 100,
			},
		}
		data, _ := json.Marshal(rec)
		data = append(data, '\n')
		if err := os.WriteFile(filepath.Join(dataDir, fn), data, 0600); err != nil {
			t.Fatalf("failed to write test file %s: %v", fn, err)
		}
	}

	cfg := abi.PluginConfig{
		RetentionDays: 7,
		MaxDiskMB:     200,
		DataDir:       dataDir,
	}

	store, err := storage.Open(dataDir, cfg, nowFn)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// With retention_days = 7 on 2026-11-17:
	// Cutoff is 2026-11-10 (17 - 7 = 10). Files older than 2026-11-10 (i.e. Nov 01 .. Nov 09) must be removed!
	// Days 10..17 (8 files) should remain.
	files, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	var jsonlFiles []string
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "usage-") && strings.HasSuffix(f.Name(), ".jsonl") {
			jsonlFiles = append(jsonlFiles, f.Name())
		}
	}

	if len(jsonlFiles) != 8 {
		t.Fatalf("expected 8 remaining files, got %d: %v", len(jsonlFiles), jsonlFiles)
	}

	// Verify the oldest remaining is 2026-11-10
	if jsonlFiles[0] != "usage-2026-11-10.jsonl" {
		t.Fatalf("expected oldest remaining file to be usage-2026-11-10.jsonl, got %s", jsonlFiles[0])
	}
}

func TestStoreRetentionDiskCapTrim(t *testing.T) {
	dataDir := t.TempDir()
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	nowFn := func() time.Time { return fixedNow }

	// Create single-day file usage-2026-11-17.jsonl exceeding 1 MB (e.g. 1.2 MB)
	targetFileName := filepath.Join(dataDir, "usage-2026-11-17.jsonl")
	f, err := os.OpenFile(targetFileName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatalf("create target file failed: %v", err)
	}

	// Write ~12,000 lines of ~100 bytes each = ~1.2 MB
	numLines := 12000
	for i := 1; i <= numLines; i++ {
		rec := tracker.Record{
			ID:           fmt.Sprintf("rec-%06d", i),
			Timestamp:    fixedNow.Add(time.Duration(i) * time.Second),
			DisplayModel: "test-model",
			Model:        "test-model",
			Tokens: tracker.NormalizedTokens{
				EffectiveInput: 100,
			},
		}
		data, _ := json.Marshal(rec)
		f.Write(data)
		f.WriteString("\n")
	}
	f.Close()

	fi, err := os.Stat(targetFileName)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if fi.Size() <= 1024*1024 {
		t.Fatalf("expected file > 1MB, got %d bytes", fi.Size())
	}

	cfg := abi.PluginConfig{
		RetentionDays: 14,
		MaxDiskMB:     1, // 1 MB cap
		DataDir:       dataDir,
	}

	store, err := storage.Open(dataDir, cfg, nowFn)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// After Open, the file should be trimmed down to <= 80% of 1 MB (~800 KB)
	trimmedFi, err := os.Stat(targetFileName)
	if err != nil {
		t.Fatalf("stat trimmed file failed: %v", err)
	}

	targetCap := int64(1024 * 1024)
	eightyPercent := targetCap * 80 / 100

	if trimmedFi.Size() > eightyPercent {
		t.Fatalf("expected trimmed size <= %d bytes, got %d", eightyPercent, trimmedFi.Size())
	}

	// Verify all remaining lines are valid JSON and non-empty
	trimmedContent, err := os.ReadFile(targetFileName)
	if err != nil {
		t.Fatalf("read trimmed file failed: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(trimmedContent), []byte{'\n'})
	if len(lines) == 0 {
		t.Fatalf("expected remaining lines, got 0")
	}

	var firstRec, lastRec tracker.Record
	if err := json.Unmarshal(lines[0], &firstRec); err != nil {
		t.Fatalf("first line corrupted: %v", err)
	}
	if err := json.Unmarshal(lines[len(lines)-1], &lastRec); err != nil {
		t.Fatalf("last line corrupted: %v", err)
	}

	// Verify newest lines were kept (last line should be the latest rec)
	expectedLastID := fmt.Sprintf("rec-%06d", numLines)
	if lastRec.ID != expectedLastID {
		t.Fatalf("expected last line ID %s, got %s", expectedLastID, lastRec.ID)
	}

	// In-memory index count should match lines count
	status := store.Status(false)
	if status.RecordCount != len(lines) {
		t.Fatalf("expected status RecordCount %d, got %d", len(lines), status.RecordCount)
	}
}

func TestStoreDiffOnDemandLookup(t *testing.T) {
	dataDir := t.TempDir()
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	nowFn := func() time.Time { return fixedNow }

	// Set a small hot LRU or append more than 200 items to push older records out of LRU
	store, err := storage.Open(dataDir, abi.PluginConfig{DataDir: dataDir}, nowFn)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	sessID := "diff-sess-1"

	// First record
	rec1, err := store.Append(tracker.Record{
		SessionID:    sessID,
		Model:        "claude-3-5-sonnet",
		TraceEnabled: true,
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			SystemHash:    "sys-1",
			ToolsHash:     "tools-1",
			MessageCount:  2,
			MessageHashes: []string{"h1", "h2"},
		},
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 1000,
			CacheRead:      0,
		},
	})
	if err != nil {
		t.Fatalf("Append rec1 failed: %v", err)
	}

	// Second record in same session with modified tools
	rec2, err := store.Append(tracker.Record{
		SessionID:    sessID,
		Model:        "claude-3-5-sonnet",
		TraceEnabled: true,
		HasFingerprint: true,
		Fingerprint: &fingerprint.Result{
			SystemHash:    "sys-1",
			ToolsHash:     "tools-2-modified",
			MessageCount:  3,
			MessageHashes: []string{"h1", "h2", "h3"},
		},
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 1200,
			CacheRead:      0,
		},
	})
	if err != nil {
		t.Fatalf("Append rec2 failed: %v", err)
	}

	if rec2.PrevID != rec1.ID {
		t.Fatalf("expected rec2.PrevID == rec1.ID, got %q vs %q", rec2.PrevID, rec1.ID)
	}
	if rec2.MissReason != "tools_changed" {
		t.Fatalf("expected MissReason tools_changed, got %q", rec2.MissReason)
	}

	// Append 300 filler records to evict rec1 & rec2 from any 200-item LRU
	for i := 0; i < 300; i++ {
		_, err := store.Append(tracker.Record{
			SessionID: fmt.Sprintf("filler-sess-%d", i),
			Model:     "claude-3-5-sonnet",
			Tokens: tracker.NormalizedTokens{
				EffectiveInput: 10,
			},
		})
		if err != nil {
			t.Fatalf("filler append failed: %v", err)
		}
	}

	// Now call Diff on rec2 — should load rec2 and rec1 from disk on demand!
	diffResp, err := store.Diff(rec2.ID)
	if err != nil {
		t.Fatalf("Diff failed: %v", err)
	}
	if diffResp == nil || diffResp.Record == nil || diffResp.Previous == nil {
		t.Fatalf("expected non-nil diff response with record and previous: %+v", diffResp)
	}
	if diffResp.Record.ID != rec2.ID {
		t.Fatalf("expected record ID %s, got %s", rec2.ID, diffResp.Record.ID)
	}
	if diffResp.Previous.ID != rec1.ID {
		t.Fatalf("expected previous ID %s, got %s", rec1.ID, diffResp.Previous.ID)
	}
	if diffResp.Comparison.MissReason != "tools_changed" {
		t.Fatalf("expected comparison MissReason tools_changed, got %q", diffResp.Comparison.MissReason)
	}
}

func TestStorePurgeAndStatus(t *testing.T) {
	dataDir := t.TempDir()
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	nowFn := func() time.Time { return fixedNow }

	store, err := storage.Open(dataDir, abi.PluginConfig{
		RetentionDays: 14,
		MaxDiskMB:     200,
		DataDir:       dataDir,
	}, nowFn)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Append a few records
	for i := 0; i < 10; i++ {
		_, err := store.Append(tracker.Record{
			Model: "test-model",
			Tokens: tracker.NormalizedTokens{
				EffectiveInput: 100,
			},
		})
		if err != nil {
			t.Fatalf("Append failed: %v", err)
		}
	}

	status := store.Status(false)
	if status.RecordCount != 10 {
		t.Fatalf("expected RecordCount 10, got %d", status.RecordCount)
	}
	if status.FileCount != 1 {
		t.Fatalf("expected FileCount 1, got %d", status.FileCount)
	}

	// Purge all
	purgeRes, err := store.Purge(0, true)
	if err != nil {
		t.Fatalf("Purge all failed: %v", err)
	}
	if purgeRes.DeletedFiles != 1 || purgeRes.DeletedRecords != 10 {
		t.Fatalf("unexpected purge result: %+v", purgeRes)
	}

	statusAfter := store.Status(false)
	if statusAfter.RecordCount != 0 || statusAfter.FileCount != 0 {
		t.Fatalf("expected 0 records and 0 files after purge all, got %+v", statusAfter)
	}
}

func TestStoreSummaryTimeSeriesAndGranularity(t *testing.T) {
	dataDir := t.TempDir()
	fixedNow := time.Date(2026, 11, 17, 15, 30, 0, 0, time.Local)
	nowFn := func() time.Time { return fixedNow }

	store, err := storage.Open(dataDir, abi.PluginConfig{DataDir: dataDir}, nowFn)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Append records across different hours today
	// Hour 10
	_, _ = store.Append(tracker.Record{
		Timestamp:    time.Date(2026, 11, 17, 10, 15, 0, 0, time.Local),
		Alias:        "sonnet",
		Model:        "claude-3-5-sonnet",
		Provider:     "anthropic",
		LatencyMs:    500,
		TTFTMs:       200,
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 1000,
			CacheRead:      500,
			RequestHit:     true,
		},
	})

	// Hour 11
	_, _ = store.Append(tracker.Record{
		Timestamp:    time.Date(2026, 11, 17, 11, 20, 0, 0, time.Local),
		Alias:        "gpt4o",
		Model:        "gpt-4o",
		Provider:     "openai",
		LatencyMs:    300,
		TTFTMs:       100,
		Tokens: tracker.NormalizedTokens{
			EffectiveInput: 2000,
			CacheRead:      0,
			RequestHit:     false,
		},
	})

	// 1. Query today -> span <= 48h -> granularity == "hour"
	respToday := store.Summary(storage.SummaryQuery{Range: "today"})
	if respToday.Granularity != "hour" {
		t.Fatalf("expected granularity hour for today, got %s", respToday.Granularity)
	}
	if len(respToday.OverallSeries) != 2 {
		t.Fatalf("expected 2 overall time buckets, got %d", len(respToday.OverallSeries))
	}
	if respToday.OverallSeries[0].Bucket != "2026-11-17 10:00" {
		t.Fatalf("expected bucket 2026-11-17 10:00, got %s", respToday.OverallSeries[0].Bucket)
	}
	if respToday.OverallSeries[1].Bucket != "2026-11-17 11:00" {
		t.Fatalf("expected bucket 2026-11-17 11:00, got %s", respToday.OverallSeries[1].Bucket)
	}
	if len(respToday.ModelSeries) != 2 {
		t.Fatalf("expected 2 model series buckets, got %d", len(respToday.ModelSeries))
	}

	// Totals checks:
	// Requests: 2, HitRequests: 1, MissRequests: 1
	// EffectiveInput: 3000, CacheRead: 500
	// TokenHitRate: 500 / 3000 = 16.6666...
	// AvgTTFTMs: (200 + 100) / 2 = 150
	// AvgLatencyMs: (500 + 300) / 2 = 400
	if respToday.Totals.Requests != 2 {
		t.Fatalf("expected 2 requests, got %d", respToday.Totals.Requests)
	}
	if respToday.Totals.AvgTTFTMs != 150.0 {
		t.Fatalf("expected AvgTTFTMs 150.0, got %v", respToday.Totals.AvgTTFTMs)
	}
	if respToday.Totals.AvgLatencyMs != 400.0 {
		t.Fatalf("expected AvgLatencyMs 400.0, got %v", respToday.Totals.AvgLatencyMs)
	}

	// 2. Query 7d -> span > 48h -> granularity == "day"
	resp7d := store.Summary(storage.SummaryQuery{Range: "7d"})
	if resp7d.Granularity != "day" {
		t.Fatalf("expected granularity day for 7d, got %s", resp7d.Granularity)
	}
	if len(resp7d.OverallSeries) != 1 {
		t.Fatalf("expected 1 daily overall bucket, got %d", len(resp7d.OverallSeries))
	}
	if resp7d.OverallSeries[0].Bucket != "2026-11-17" {
		t.Fatalf("expected bucket 2026-11-17, got %s", resp7d.OverallSeries[0].Bucket)
	}
}

func TestStoreRecordsFilteringAndPagination(t *testing.T) {
	dataDir := t.TempDir()
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	nowFn := func() time.Time { return fixedNow }

	store, err := storage.Open(dataDir, abi.PluginConfig{DataDir: dataDir}, nowFn)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Append 25 records
	for i := 1; i <= 25; i++ {
		provider := "anthropic"
		if i%2 == 0 {
			provider = "openai"
		}
		statusFailed := (i == 25)
		hit := (i%3 == 0 && !statusFailed)

		_, err := store.Append(tracker.Record{
			ID:        fmt.Sprintf("req-%03d", i),
			Timestamp: fixedNow.Add(time.Duration(i) * time.Minute),
			Model:     "claude-3-5-sonnet",
			Provider:  provider,
			Failed:    statusFailed,
			Tokens: tracker.NormalizedTokens{
				EffectiveInput: 100,
				CacheRead:      map[bool]int64{true: 80, false: 0}[hit],
				RequestHit:     hit,
			},
		})
		if err != nil {
			t.Fatalf("Append failed: %v", err)
		}
	}

	// Pagination: limit 10, offset 0 -> 10 records
	resp1 := store.Records(storage.RecordsQuery{Limit: 10, Offset: 0})
	if resp1.Total != 25 {
		t.Fatalf("expected Total 25, got %d", resp1.Total)
	}
	if len(resp1.Items) != 10 {
		t.Fatalf("expected 10 items, got %d", len(resp1.Items))
	}
	// Newest first: first item should be req-025
	if resp1.Items[0].ID != "req-025" {
		t.Fatalf("expected newest req-025 first, got %s", resp1.Items[0].ID)
	}

	// Second page: limit 10, offset 10 -> 10 records
	resp2 := store.Records(storage.RecordsQuery{Limit: 10, Offset: 10})
	if len(resp2.Items) != 10 {
		t.Fatalf("expected 10 items on page 2, got %d", len(resp2.Items))
	}
	if resp2.Items[0].ID != "req-015" {
		t.Fatalf("expected req-015, got %s", resp2.Items[0].ID)
	}

	// Third page: limit 10, offset 20 -> 5 records
	resp3 := store.Records(storage.RecordsQuery{Limit: 10, Offset: 20})
	if len(resp3.Items) != 5 {
		t.Fatalf("expected 5 items on page 3, got %d", len(resp3.Items))
	}
	if resp3.Items[4].ID != "req-001" {
		t.Fatalf("expected oldest req-001 at end, got %s", resp3.Items[4].ID)
	}

	// Text query search
	respSearch := store.Records(storage.RecordsQuery{Query: "req-007"})
	if respSearch.Total != 1 || respSearch.Items[0].ID != "req-007" {
		t.Fatalf("expected search match for req-007, got %+v", respSearch)
	}

	// Status filter: "hit"
	respHits := store.Records(storage.RecordsQuery{Status: "hit"})
	for _, item := range respHits.Items {
		if !item.Tokens.RequestHit {
			t.Fatalf("expected all returned items to be hits, got %+v", item)
		}
	}
}

func TestStorePurgeOlderThanDays(t *testing.T) {
	dataDir := t.TempDir()
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	nowFn := func() time.Time { return fixedNow }

	// Create 5 day files: Nov 13, 14, 15, 16, 17
	for day := 13; day <= 17; day++ {
		fn := fmt.Sprintf("usage-2026-11-%02d.jsonl", day)
		rec := tracker.Record{
			ID:           fmt.Sprintf("rec-%02d", day),
			Timestamp:    time.Date(2026, 11, day, 10, 0, 0, 0, time.Local),
			DisplayModel: "test-model",
			Model:        "test-model",
			Tokens: tracker.NormalizedTokens{
				EffectiveInput: 100,
			},
		}
		data, _ := json.Marshal(rec)
		data = append(data, '\n')
		_ = os.WriteFile(filepath.Join(dataDir, fn), data, 0600)
	}

	store, err := storage.Open(dataDir, abi.PluginConfig{DataDir: dataDir, RetentionDays: 0}, nowFn)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	if len(store.Records(storage.RecordsQuery{}).Items) != 5 {
		t.Fatalf("expected 5 records initially")
	}

	// Purge older than 2 days: cutoff is 17 - 2 = 15.
	// Nov 13 and Nov 14 should be deleted. Nov 15, 16, 17 kept.
	purgeRes, err := store.Purge(2, false)
	if err != nil {
		t.Fatalf("Purge failed: %v", err)
	}
	if purgeRes.DeletedFiles != 2 {
		t.Fatalf("expected 2 deleted files, got %d", purgeRes.DeletedFiles)
	}
	if purgeRes.DeletedRecords != 2 {
		t.Fatalf("expected 2 deleted records, got %d", purgeRes.DeletedRecords)
	}
	if purgeRes.FileCount != 3 {
		t.Fatalf("expected 3 remaining files, got %d", purgeRes.FileCount)
	}
}

func TestStoreRetentionMultiFileDiskCap(t *testing.T) {
	dataDir := t.TempDir()
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	nowFn := func() time.Time { return fixedNow }

	// Create 3 daily files: Nov 15 (500KB), Nov 16 (500KB), Nov 17 (500KB) = 1.5MB total
	for _, day := range []int{15, 16, 17} {
		fn := fmt.Sprintf("usage-2026-11-%02d.jsonl", day)
		f, _ := os.OpenFile(filepath.Join(dataDir, fn), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		for i := 0; i < 5000; i++ {
			rec := tracker.Record{
				ID:        fmt.Sprintf("rec-d%d-%04d", day, i),
				Timestamp: time.Date(2026, 11, day, 10, 0, 0, 0, time.Local),
				Model:     "test-model",
				Tokens: tracker.NormalizedTokens{
					EffectiveInput: 100,
				},
			}
			b, _ := json.Marshal(rec)
			f.Write(b)
			f.WriteString("\n")
		}
		f.Close()
	}

	// MaxDiskMB = 1 (1MB cap)
	cfg := abi.PluginConfig{
		RetentionDays: 0,
		MaxDiskMB:     1,
		DataDir:       dataDir,
	}

	store, err := storage.Open(dataDir, cfg, nowFn)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Should delete Nov 15 (oldest), leaving Nov 16 + Nov 17 (1MB), and if still over 1MB, deletes Nov 16 or trims.
	status := store.Status(false)
	if status.UsedBytes > 1024*1024 {
		t.Fatalf("expected total used bytes <= 1MB, got %d", status.UsedBytes)
	}

	// Nov 15 must have been deleted
	if _, err := os.Stat(filepath.Join(dataDir, "usage-2026-11-15.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expected usage-2026-11-15.jsonl to be deleted")
	}
}

func TestStoreConcurrentAppendsAndReads(t *testing.T) {
	dataDir := t.TempDir()
	fixedNow := time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local)
	nowFn := func() time.Time { return fixedNow }

	store, err := storage.Open(dataDir, abi.PluginConfig{DataDir: dataDir}, nowFn)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	const numWorkers = 8
	const numAppends = 50
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		workerID := w
		go func() {
			defer wg.Done()
			for i := 0; i < numAppends; i++ {
				_, err := store.Append(tracker.Record{
					SessionID: fmt.Sprintf("sess-w%d", workerID),
					Model:     "claude-3-5-sonnet",
					Tokens: tracker.NormalizedTokens{
						EffectiveInput: 100,
						CacheRead:      50,
						RequestHit:     true,
					},
				})
				if err != nil {
					t.Errorf("worker %d append %d failed: %v", workerID, i, err)
				}

				// Concurrent summary & records reading
				if i%5 == 0 {
					_ = store.Summary(storage.SummaryQuery{Range: "today"})
					_ = store.Records(storage.RecordsQuery{Limit: 10})
					_ = store.Status(false)
				}
			}
		}()
	}

	wg.Wait()

	status := store.Status(false)
	expectedTotal := numWorkers * numAppends
	if status.RecordCount != expectedTotal {
		t.Fatalf("expected %d total records, got %d", expectedTotal, status.RecordCount)
	}
}
