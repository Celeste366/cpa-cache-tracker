package tracker_test

import (
	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/tracker"
	"testing"
)

func TestWorkBuddyClaudeFormatUsesInclusiveInput(t *testing.T) {
	for _, u := range []abi.UsageRecord{
		{Provider: " workbuddy ", ExecutorType: "executorAdapter"},
		{Provider: "workbuddy-custom", ExecutorType: "executorAdapter"},
		{Provider: "plugin", ExecutorType: "WorkBuddyExecutor"},
	} {
		u.Detail = abi.UsageDetail{InputTokens: 1000, CacheReadTokens: 600}
		got := tracker.NormalizeUsage(u, " claude ")
		if got.Semantics != "subset" || got.EffectiveInput != 1000 || got.HitRate != 60 {
			t.Errorf("WorkBuddy inclusive accounting: %+v", got)
		}
	}
}
