package tracker_test

import (
	"encoding/json"
	"math"
	"testing"

	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/tracker"
)

// 1. TestNormalizeClaudeColdStartDoesNotFakeHit
// Provider: "claude", InputTokens: 1000, CacheReadTokens: 0, CacheCreationTokens: 500, CachedTokens: 500
// -> Semantics == "independent", CacheRead == 0, EffectiveInput == 1500, HitRate == 0, RequestHit == false.
func TestNormalizeClaudeColdStartDoesNotFakeHit(t *testing.T) {
	record := abi.UsageRecord{
		Provider: "claude",
		Detail: abi.UsageDetail{
			InputTokens:         1000,
			CacheReadTokens:     0,
			CacheCreationTokens: 500,
			CachedTokens:        500,
			OutputTokens:        200,
			TotalTokens:         1700,
		},
	}

	norm := tracker.NormalizeUsage(record, "")

	if norm.Semantics != "independent" {
		t.Errorf("expected semantics 'independent', got %q", norm.Semantics)
	}
	if norm.CacheRead != 0 {
		t.Errorf("expected CacheRead 0, got %d", norm.CacheRead)
	}
	if norm.CacheWrite != 500 {
		t.Errorf("expected CacheWrite 500, got %d", norm.CacheWrite)
	}
	if norm.UncachedInput != 1000 {
		t.Errorf("expected UncachedInput 1000, got %d", norm.UncachedInput)
	}
	if norm.EffectiveInput != 1500 {
		t.Errorf("expected EffectiveInput 1500, got %d", norm.EffectiveInput)
	}
	if norm.HitRate != 0 {
		t.Errorf("expected HitRate 0, got %f", norm.HitRate)
	}
	if norm.RequestHit != false {
		t.Errorf("expected RequestHit false, got true")
	}
	if norm.Output != 200 {
		t.Errorf("expected Output 200, got %d", norm.Output)
	}
	if norm.Total != 1700 {
		t.Errorf("expected Total 1700, got %d", norm.Total)
	}
}

// 2. TestNormalizeClaudeCacheHit
// Provider: "claude", InputTokens: 200, CacheReadTokens: 800, CacheCreationTokens: 0
// -> EffectiveInput == 1000, HitRate == 80.0, RequestHit == true.
func TestNormalizeClaudeCacheHit(t *testing.T) {
	record := abi.UsageRecord{
		Provider: "claude",
		Detail: abi.UsageDetail{
			InputTokens:         200,
			CacheReadTokens:     800,
			CacheCreationTokens: 0,
			OutputTokens:        150,
			TotalTokens:         1150,
		},
	}

	norm := tracker.NormalizeUsage(record, "")

	if norm.Semantics != "independent" {
		t.Errorf("expected semantics 'independent', got %q", norm.Semantics)
	}
	if norm.CacheRead != 800 {
		t.Errorf("expected CacheRead 800, got %d", norm.CacheRead)
	}
	if norm.CacheWrite != 0 {
		t.Errorf("expected CacheWrite 0, got %d", norm.CacheWrite)
	}
	if norm.UncachedInput != 200 {
		t.Errorf("expected UncachedInput 200, got %d", norm.UncachedInput)
	}
	if norm.EffectiveInput != 1000 {
		t.Errorf("expected EffectiveInput 1000, got %d", norm.EffectiveInput)
	}
	if math.Abs(norm.HitRate-80.0) > 1e-6 {
		t.Errorf("expected HitRate 80.0, got %f", norm.HitRate)
	}
	if !norm.RequestHit {
		t.Errorf("expected RequestHit true, got false")
	}
	if norm.Output != 150 {
		t.Errorf("expected Output 150, got %d", norm.Output)
	}
	if norm.Total != 1150 {
		t.Errorf("expected Total 1150, got %d", norm.Total)
	}
}

// 3. TestNormalizeOpenAICompatWithClaudeInName
// ExecutorType: "OpenAICompatExecutor", Provider: "my-claude-relay",
// InputTokens: 1000, CacheReadTokens: 600, CachedTokens: 600
// -> Semantics == "subset", EffectiveInput == 1000, HitRate == 60.0.
func TestNormalizeOpenAICompatWithClaudeInName(t *testing.T) {
	record := abi.UsageRecord{
		ExecutorType: "OpenAICompatExecutor",
		Provider:     "my-claude-relay",
		Detail: abi.UsageDetail{
			InputTokens:     1000,
			CacheReadTokens: 600,
			CachedTokens:    600,
			OutputTokens:    100,
			TotalTokens:     1100,
		},
	}

	norm := tracker.NormalizeUsage(record, "")

	if norm.Semantics != "subset" {
		t.Errorf("expected semantics 'subset', got %q", norm.Semantics)
	}
	if norm.CacheRead != 600 {
		t.Errorf("expected CacheRead 600, got %d", norm.CacheRead)
	}
	if norm.CacheWrite != 0 {
		t.Errorf("expected CacheWrite 0, got %d", norm.CacheWrite)
	}
	if norm.EffectiveInput != 1000 {
		t.Errorf("expected EffectiveInput 1000, got %d", norm.EffectiveInput)
	}
	if norm.UncachedInput != 400 {
		t.Errorf("expected UncachedInput 400, got %d", norm.UncachedInput)
	}
	if math.Abs(norm.HitRate-60.0) > 1e-6 {
		t.Errorf("expected HitRate 60.0, got %f", norm.HitRate)
	}
	if !norm.RequestHit {
		t.Errorf("expected RequestHit true, got false")
	}
}

// 4. TestNormalizeWorkBuddyAndCodexAndGemini
// Provider: "workbuddy" / "codex" / "antigravity" -> subset,
// and CachedTokens fallback works when CacheReadTokens == 0 && CacheCreationTokens == 0 && CachedTokens > 0.
func TestNormalizeWorkBuddyAndCodexAndGemini(t *testing.T) {
	providers := []string{"workbuddy", "codex", "antigravity", "gemini", "openai"}

	for _, p := range providers {
		t.Run(p, func(t *testing.T) {
			record := abi.UsageRecord{
				Provider: p,
				Detail: abi.UsageDetail{
					InputTokens:         1000,
					CacheReadTokens:     0,
					CacheCreationTokens: 0,
					CachedTokens:        400,
					OutputTokens:        50,
				},
			}

			norm := tracker.NormalizeUsage(record, "")

			if norm.Semantics != "subset" {
				t.Errorf("[%s] expected semantics 'subset', got %q", p, norm.Semantics)
			}
			if norm.CacheRead != 400 {
				t.Errorf("[%s] expected CacheRead fallback 400, got %d", p, norm.CacheRead)
			}
			if norm.EffectiveInput != 1000 {
				t.Errorf("[%s] expected EffectiveInput 1000, got %d", p, norm.EffectiveInput)
			}
			if norm.UncachedInput != 600 {
				t.Errorf("[%s] expected UncachedInput 600, got %d", p, norm.UncachedInput)
			}
			if math.Abs(norm.HitRate-40.0) > 1e-6 {
				t.Errorf("[%s] expected HitRate 40.0, got %f", p, norm.HitRate)
			}
			if !norm.RequestHit {
				t.Errorf("[%s] expected RequestHit true, got false", p)
			}
			// When TotalTokens is 0, Total = EffectiveInput + Output = 1000 + 50 = 1050
			if norm.Total != 1050 {
				t.Errorf("[%s] expected Total 1050, got %d", p, norm.Total)
			}
		})
	}
}

// 5. TestNormalizeUnknownPluginViaToFormatAndTotalInvariant
// unknown plugin (ExecutorType: "executorAdapter", Provider: "custom-plugin")
// with toFormat == "claude" OR InputTokens + CacheRead + OutputTokens == TotalTokens -> independent.
func TestNormalizeUnknownPluginViaToFormatAndTotalInvariant(t *testing.T) {
	t.Run("ViaToFormatClaude", func(t *testing.T) {
		record := abi.UsageRecord{
			ExecutorType: "executorAdapter",
			Provider:     "custom-plugin",
			Detail: abi.UsageDetail{
				InputTokens:     500,
				CacheReadTokens: 500,
				OutputTokens:    100,
			},
		}

		norm := tracker.NormalizeUsage(record, "claude-3-5-sonnet")
		if norm.Semantics != "independent" {
			t.Errorf("expected semantics 'independent', got %q", norm.Semantics)
		}
		if norm.EffectiveInput != 1000 {
			t.Errorf("expected EffectiveInput 1000, got %d", norm.EffectiveInput)
		}
		if math.Abs(norm.HitRate-50.0) > 1e-6 {
			t.Errorf("expected HitRate 50.0, got %f", norm.HitRate)
		}
	})

	t.Run("ViaTotalTokensInvariant", func(t *testing.T) {
		// rawInput + cacheRead + cacheWrite + OutputTokens == TotalTokens
		// 500 + 500 + 0 + 200 == 1200, and rawInput + output (700) < TotalTokens (1200)
		record := abi.UsageRecord{
			ExecutorType: "executorAdapter",
			Provider:     "custom-plugin",
			Detail: abi.UsageDetail{
				InputTokens:     500,
				CacheReadTokens: 500,
				OutputTokens:    200,
				TotalTokens:     1200,
			},
		}

		norm := tracker.NormalizeUsage(record, "")
		if norm.Semantics != "independent" {
			t.Errorf("expected semantics 'independent', got %q", norm.Semantics)
		}
		if norm.EffectiveInput != 1000 {
			t.Errorf("expected EffectiveInput 1000, got %d", norm.EffectiveInput)
		}
		if math.Abs(norm.HitRate-50.0) > 1e-6 {
			t.Errorf("expected HitRate 50.0, got %f", norm.HitRate)
		}
	})

	t.Run("ViaRawInputLessThanCacheTokens", func(t *testing.T) {
		// rawInput (300) < cacheRead (500) -> independent
		record := abi.UsageRecord{
			ExecutorType: "executorAdapter",
			Provider:     "custom-plugin",
			Detail: abi.UsageDetail{
				InputTokens:     300,
				CacheReadTokens: 500,
				OutputTokens:    100,
			},
		}

		norm := tracker.NormalizeUsage(record, "")
		if norm.Semantics != "independent" {
			t.Errorf("expected semantics 'independent', got %q", norm.Semantics)
		}
		if norm.EffectiveInput != 800 {
			t.Errorf("expected EffectiveInput 800, got %d", norm.EffectiveInput)
		}
	})

	t.Run("StandardSubsetFallback", func(t *testing.T) {
		// Unknown plugin where rawInput (1000) >= cacheRead (300) and TotalTokens == rawInput + OutputTokens (1200)
		record := abi.UsageRecord{
			ExecutorType: "executorAdapter",
			Provider:     "custom-plugin",
			Detail: abi.UsageDetail{
				InputTokens:     1000,
				CacheReadTokens: 300,
				OutputTokens:    200,
				TotalTokens:     1200,
			},
		}

		norm := tracker.NormalizeUsage(record, "")
		if norm.Semantics != "subset" {
			t.Errorf("expected semantics 'subset', got %q", norm.Semantics)
		}
		if norm.EffectiveInput != 1000 {
			t.Errorf("expected EffectiveInput 1000, got %d", norm.EffectiveInput)
		}
		if norm.UncachedInput != 700 {
			t.Errorf("expected UncachedInput 700, got %d", norm.UncachedInput)
		}
		if math.Abs(norm.HitRate-30.0) > 1e-6 {
			t.Errorf("expected HitRate 30.0, got %f", norm.HitRate)
		}
	})
}

// 6. TestNormalizeJSONSerialization
// Verify snake_case JSON tags match spec:
// effective_input, cache_read, cache_write, uncached_input, output, reasoning, total, semantics, hit_rate, request_hit
func TestNormalizeJSONSerialization(t *testing.T) {
	norm := tracker.NormalizedTokens{
		EffectiveInput: 1000,
		CacheRead:      800,
		CacheWrite:     0,
		UncachedInput:  200,
		Output:         150,
		Reasoning:      50,
		Total:          1150,
		Semantics:      "subset",
		HitRate:        80.0,
		RequestHit:     true,
	}

	data, err := json.Marshal(norm)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	expectedKeys := []string{
		"effective_input", "cache_read", "cache_write", "uncached_input",
		"output", "reasoning", "total", "semantics", "hit_rate", "request_hit",
	}

	for _, k := range expectedKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("missing expected JSON key %q in %s", k, string(data))
		}
	}
}

// 7. TestNormalizeDefensiveClampingAndZeroHandling
// Negative values clamped to 0, zero effective input yields 0% hit rate.
func TestNormalizeDefensiveClampingAndZeroHandling(t *testing.T) {
	record := abi.UsageRecord{
		Provider: "openai",
		Detail: abi.UsageDetail{
			InputTokens:         -10,
			CacheReadTokens:     -5,
			CacheCreationTokens: -2,
			OutputTokens:        -20,
			ReasoningTokens:     -1,
			TotalTokens:         -100,
		},
	}

	norm := tracker.NormalizeUsage(record, "")

	if norm.EffectiveInput != 0 {
		t.Errorf("expected EffectiveInput 0, got %d", norm.EffectiveInput)
	}
	if norm.CacheRead != 0 {
		t.Errorf("expected CacheRead 0, got %d", norm.CacheRead)
	}
	if norm.CacheWrite != 0 {
		t.Errorf("expected CacheWrite 0, got %d", norm.CacheWrite)
	}
	if norm.UncachedInput != 0 {
		t.Errorf("expected UncachedInput 0, got %d", norm.UncachedInput)
	}
	if norm.Output != 0 {
		t.Errorf("expected Output 0, got %d", norm.Output)
	}
	if norm.Reasoning != 0 {
		t.Errorf("expected Reasoning 0, got %d", norm.Reasoning)
	}
	if norm.Total != 0 {
		t.Errorf("expected Total 0, got %d", norm.Total)
	}
	if norm.HitRate != 0.0 {
		t.Errorf("expected HitRate 0.0, got %f", norm.HitRate)
	}
	if norm.RequestHit != false {
		t.Errorf("expected RequestHit false, got true")
	}
}

// 8. TestNormalizeOpenAICompatiblePrefix
// strings.HasPrefix(lower(Provider), "openai-compatible-") -> "subset"
func TestNormalizeOpenAICompatiblePrefix(t *testing.T) {
	record := abi.UsageRecord{
		Provider: "openai-compatible-claude-proxy",
		Detail: abi.UsageDetail{
			InputTokens:     1000,
			CacheReadTokens: 500,
			OutputTokens:    100,
		},
	}

	norm := tracker.NormalizeUsage(record, "")
	if norm.Semantics != "subset" {
		t.Errorf("expected semantics 'subset', got %q", norm.Semantics)
	}
	if norm.EffectiveInput != 1000 {
		t.Errorf("expected EffectiveInput 1000, got %d", norm.EffectiveInput)
	}
	if norm.HitRate != 50.0 {
		t.Errorf("expected HitRate 50.0, got %f", norm.HitRate)
	}
}
