package tracker_test

import (
	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/tracker"
	"testing"
	"time"
)

func TestHostModelCallbackMetadata(t *testing.T) {
	c := tracker.NewCorrelator(10, time.Minute, time.Second)
	now := time.Now()
	c.RecordInterceptAfter(abi.RequestInterceptRequest{TraceID: "t", Model: "m", Metadata: map[string]any{"source": "plugin_host_model_callback"}}, false, 10, now)
	got, ok := c.MatchUsage(abi.UsageRecord{TraceID: "t", Model: "m"}, now)
	if !ok || got.RecordKind != "internal_subcall" {
		t.Fatalf("actual host callback metadata classified as main: %+v", got)
	}
}

func TestNestedSameModelMatchesSelectionID(t *testing.T) {
	c := tracker.NewCorrelator(10, time.Minute, time.Second)
	now := time.Now()
	for _, id := range []string{"outer", "inner"} {
		meta := map[string]any{"selected_auth_id": "a", "selection_id": id}
		if id == "inner" {
			meta["source"] = "plugin_host_model_callback"
		}
		c.RecordInterceptAfter(abi.RequestInterceptRequest{TraceID: "t", Model: "m", Metadata: meta}, false, 10, now)
	}
	for _, item := range []struct{ id, kind string }{{"inner", "internal_subcall"}, {"outer", "main"}} {
		got, ok := c.MatchUsage(abi.UsageRecord{TraceID: "t", AuthID: "a", Model: "m", SelectionID: item.id}, now)
		if !ok || got.RecordKind != item.kind {
			t.Fatalf("selection %s correlated incorrectly: %+v", item.id, got)
		}
	}
}
