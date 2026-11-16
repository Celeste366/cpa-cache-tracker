package storage_test

import (
	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/storage"
	"cpa-cache-tracker/tracker"
	"testing"
)

func TestZeroInputDoesNotDiluteHitRates(t *testing.T) {
	s, err := storage.Open(t.TempDir(), abi.DefaultPluginConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tokens := range []tracker.NormalizedTokens{{EffectiveInput: 100, CacheRead: 100, RequestHit: true}, {}} {
		if _, err := s.Append(tracker.Record{Model: "m", RecordKind: "main", Tokens: tokens}); err != nil {
			t.Fatal(err)
		}
	}
	got := s.Summary(storage.SummaryQuery{Range: "today"})
	if got.Totals.RequestHitRate != 100 || got.Models[0].RequestHitRate != 100 || got.OverallSeries[0].RequestHitRate != 100 {
		t.Fatalf("zero input diluted cache hit rates: %+v", got)
	}
	if s.Records(storage.RecordsQuery{Kind: "all"}).Total != 2 {
		t.Fatal("zero-input records must remain inspectable")
	}
}
