package tracker

import (
	"strings"

	"cpa-cache-tracker/abi"
)

// Semantics classification constants.
const (
	SemanticsIndependent = "independent"
	SemanticsSubset      = "subset"
)

// NormalizedTokens contains the normalized token counters and metrics.
type NormalizedTokens struct {
	EffectiveInput int64   `json:"effective_input"`
	CacheRead      int64   `json:"cache_read"`
	CacheWrite     int64   `json:"cache_write"`
	UncachedInput  int64   `json:"uncached_input"`
	Output         int64   `json:"output"`
	Reasoning      int64   `json:"reasoning"`
	Total          int64   `json:"total"`
	Semantics      string  `json:"semantics"`
	HitRate        float64 `json:"hit_rate"`
	RequestHit     bool    `json:"request_hit"`
}

// NormalizeUsage normalizes upstream token accounting counters across providers and executor types.
// It resolves provider-specific semantics (independent vs subset) and avoids false cache hits.
func NormalizeUsage(u abi.UsageRecord, toFormat string) NormalizedTokens {
	rawInput := maxInt64(0, u.Detail.InputTokens)
	cacheRead := maxInt64(0, u.Detail.CacheReadTokens)
	cacheWrite := maxInt64(0, u.Detail.CacheCreationTokens)
	output := maxInt64(0, u.Detail.OutputTokens)
	reasoning := maxInt64(0, u.Detail.ReasoningTokens)
	cachedTokens := maxInt64(0, u.Detail.CachedTokens)

	semantics := classifySemantics(u, toFormat, rawInput, cacheRead, cacheWrite, output)

	// CachedTokens fallback: ONLY on "subset" semantics, if cacheRead == 0 && CachedTokens > 0 && cacheWrite == 0.
	// Never fall back on "independent" (prevents Claude cold-start CacheCreationTokens from being counted as reads).
	if semantics == SemanticsSubset {
		if cacheRead == 0 && cachedTokens > 0 && cacheWrite == 0 {
			cacheRead = cachedTokens
		}
	}

	var effectiveInput int64
	var uncachedInput int64

	if semantics == SemanticsIndependent {
		effectiveInput = rawInput + cacheRead + cacheWrite
		uncachedInput = rawInput
	} else {
		effectiveInput = maxInt64(rawInput, cacheRead+cacheWrite)
		uncachedInput = maxInt64(0, effectiveInput-cacheRead-cacheWrite)
	}

	var hitRate float64
	if effectiveInput > 0 {
		hitRate = (float64(cacheRead) / float64(effectiveInput)) * 100.0
		if hitRate < 0 {
			hitRate = 0
		} else if hitRate > 100 {
			hitRate = 100
		}
	}

	requestHit := cacheRead > 0

	var total int64
	if u.Detail.TotalTokens > 0 {
		total = u.Detail.TotalTokens
	} else {
		total = effectiveInput + output
	}

	return NormalizedTokens{
		EffectiveInput: effectiveInput,
		CacheRead:      cacheRead,
		CacheWrite:     cacheWrite,
		UncachedInput:  uncachedInput,
		Output:         output,
		Reasoning:      reasoning,
		Total:          total,
		Semantics:      semantics,
		HitRate:        hitRate,
		RequestHit:     requestHit,
	}
}

func classifySemantics(u abi.UsageRecord, toFormat string, rawInput, cacheRead, cacheWrite, output int64) string {
	executor := strings.ToLower(u.ExecutorType)
	provider := strings.ToLower(u.Provider)
	targetFormat := strings.ToLower(toFormat)

	// 1. If ExecutorType == "OpenAICompatExecutor" (case-insensitive) or strings.HasPrefix(lower(Provider), "openai-compatible-"): "subset".
	if executor == "openaicompatexecutor" || strings.HasPrefix(provider, "openai-compatible-") {
		return SemanticsSubset
	}

	// 2. Else if lower(Provider) == "workbuddy": "subset".
	if provider == "workbuddy" {
		return SemanticsSubset
	}

	// 3. Else if strings.Contains(lower(toFormat), "claude") or strings.Contains(lower(toFormat), "anthropic")
	//    or strings.Contains(lower(Provider), "claude") or strings.Contains(lower(Provider), "anthropic")
	//    or strings.Contains(lower(ExecutorType), "claude") or strings.Contains(lower(ExecutorType), "anthropic"): "independent".
	if strings.Contains(targetFormat, "claude") || strings.Contains(targetFormat, "anthropic") ||
		strings.Contains(provider, "claude") || strings.Contains(provider, "anthropic") ||
		strings.Contains(executor, "claude") || strings.Contains(executor, "anthropic") {
		return SemanticsIndependent
	}

	// 4. Else if Provider or ExecutorType matches "openai", "codex", "gemini", "antigravity", "aistudio",
	//    "vertex", "xai", "grok", "kimi", "qwen", "deepseek", "openrouter": "subset".
	subsetKeywords := []string{
		"openai", "codex", "gemini", "antigravity", "aistudio",
		"vertex", "xai", "grok", "kimi", "qwen", "deepseek", "openrouter",
	}
	for _, kw := range subsetKeywords {
		if strings.Contains(provider, kw) || strings.Contains(executor, kw) {
			return SemanticsSubset
		}
	}

	// 5. Fallback for unrecognized/plugin executors:
	// if rawInput < cacheRead + cacheWrite, or
	// (TotalTokens > 0 && OutputTokens >= 0 && cacheRead + cacheWrite > 0 && rawInput + OutputTokens < TotalTokens && rawInput + cacheRead + cacheWrite + OutputTokens == TotalTokens),
	// classify "independent"; otherwise "subset".
	totalTokens := u.Detail.TotalTokens
	cacheTotal := cacheRead + cacheWrite
	if rawInput < cacheTotal {
		return SemanticsIndependent
	}
	if totalTokens > 0 && output >= 0 && cacheTotal > 0 &&
		(rawInput+output < totalTokens) &&
		(rawInput+cacheTotal+output == totalTokens) {
		return SemanticsIndependent
	}

	return SemanticsSubset
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
