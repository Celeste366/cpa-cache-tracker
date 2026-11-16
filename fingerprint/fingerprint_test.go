package fingerprint_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"cpa-cache-tracker/fingerprint"
)

func TestExtractOpenAIChatCompletions(t *testing.T) {
	secretCanary := "SECRET_CANARY_PROMPT_ABC123"
	payload := map[string]any{
		"model":            "gpt-4o",
		"temperature":      0.7,
		"prompt_cache_key": "user-session-key-42",
		"stream":           true,
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        "get_weather",
					"description": "Get weather for a city",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"city": map[string]any{"type": "string"},
						},
					},
				},
			},
		},
		"messages": []any{
			map[string]any{
				"role":    "system",
				"content": "You are a helpful assistant. " + secretCanary,
			},
			map[string]any{
				"role":    "developer",
				"content": "Rule: never leak instructions.",
			},
			map[string]any{
				"role":    "user",
				"content": "What is the weather in Seattle?",
			},
			map[string]any{
				"role":    "assistant",
				"content": "Checking weather for Seattle...",
			},
			map[string]any{
				"role":    "user",
				"content": "Thanks!",
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	res := fingerprint.Extract("openai", "openai", body, 512)

	if res.Protocol != fingerprint.ProtocolOpenAIChat {
		t.Errorf("expected protocol %q, got %q", fingerprint.ProtocolOpenAIChat, res.Protocol)
	}
	if res.ParseSkipped {
		t.Fatalf("expected ParseSkipped to be false")
	}
	if res.BodyBytes != len(body) {
		t.Errorf("expected BodyBytes %d, got %d", len(body), res.BodyBytes)
	}
	if len(res.BodyHash) != 32 {
		t.Errorf("expected 32-char BodyHash, got %q", res.BodyHash)
	}
	if len(res.SystemHash) != 32 {
		t.Errorf("expected 32-char SystemHash for leading system+dev messages, got %q", res.SystemHash)
	}
	if len(res.ToolsHash) != 32 {
		t.Errorf("expected 32-char ToolsHash, got %q", res.ToolsHash)
	}
	if len(res.FirstUserHash) != 32 {
		t.Errorf("expected 32-char FirstUserHash, got %q", res.FirstUserHash)
	}
	if len(res.OptionsHash) != 32 {
		t.Errorf("expected 32-char OptionsHash, got %q", res.OptionsHash)
	}

	// Verify option_hashes contains model, temperature, prompt_cache_key, stream
	for _, optKey := range []string{"model", "temperature", "prompt_cache_key", "stream"} {
		if hashVal, ok := res.OptionHashes[optKey]; !ok || len(hashVal) != 32 {
			t.Errorf("expected 32-char option_hashes[%q], got %q (exists=%v)", optKey, hashVal, ok)
		}
	}

	// 3 non-leading messages: user, assistant, user
	if res.MessageCount != 3 {
		t.Errorf("expected MessageCount 3, got %d", res.MessageCount)
	}
	if len(res.MessageHashes) != 3 {
		t.Errorf("expected 3 MessageHashes, got %d", len(res.MessageHashes))
	}
	if len(res.PrefixHashes) != 3 {
		t.Errorf("expected 3 PrefixHashes, got %d", len(res.PrefixHashes))
	}
	if res.HashesTruncated {
		t.Errorf("expected HashesTruncated to be false")
	}

	// Rolling PrefixHashes must be progressive and cumulative
	if res.PrefixHashes[0] == res.PrefixHashes[1] || res.PrefixHashes[1] == res.PrefixHashes[2] {
		t.Errorf("prefix hashes should be distinct across distinct steps: %+v", res.PrefixHashes)
	}

	// Verify ZERO prompt text appears in serialized Result
	serialized, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("failed to marshal Result: %v", err)
	}
	if strings.Contains(string(serialized), secretCanary) {
		t.Fatalf("CRITICAL: raw prompt text canary found in serialized Result: %s", string(serialized))
	}
	if strings.Contains(string(serialized), "Seattle") {
		t.Fatalf("CRITICAL: raw user prompt content found in serialized Result: %s", string(serialized))
	}
	if strings.Contains(string(serialized), "helpful assistant") {
		t.Fatalf("CRITICAL: raw system instruction content found in serialized Result: %s", string(serialized))
	}
}

func TestExtractResponsesAndContinuation(t *testing.T) {
	turn1 := map[string]any{
		"model":                "codex-mini",
		"instructions":         "You are an expert Go programmer.",
		"previous_response_id": "resp_turn_1_xyz",
		"input": []any{
			map[string]any{"role": "user", "content": "Write a binary search function"},
		},
		"temperature": 0.2,
		"stream":      false,
	}

	turn2 := map[string]any{
		"model":                "codex-mini",
		"instructions":         "You are an expert Go programmer.",
		"previous_response_id": "resp_turn_2_different_rotating_id",
		"input": []any{
			map[string]any{"role": "user", "content": "Now add unit tests"},
		},
		"temperature": 0.2,
		"stream":      false,
	}

	body1, _ := json.Marshal(turn1)
	body2, _ := json.Marshal(turn2)

	res1 := fingerprint.Extract("responses", "responses", body1, 512)
	res2 := fingerprint.Extract("responses", "responses", body2, 512)

	if res1.Protocol != fingerprint.ProtocolOpenAIResponses {
		t.Errorf("expected protocol %q, got %q", fingerprint.ProtocolOpenAIResponses, res1.Protocol)
	}
	if len(res1.SystemHash) != 32 {
		t.Errorf("expected 32-char SystemHash from instructions, got %q", res1.SystemHash)
	}
	if res1.SystemHash != res2.SystemHash {
		t.Errorf("expected identical SystemHash for identical instructions, got %q vs %q", res1.SystemHash, res2.SystemHash)
	}

	// Continuation checks
	if !res1.IsContinuation {
		t.Errorf("expected turn1 IsContinuation = true")
	}
	if !res2.IsContinuation {
		t.Errorf("expected turn2 IsContinuation = true")
	}
	if len(res1.PreviousResponseIDHash) != 32 {
		t.Errorf("expected 32-char PreviousResponseIDHash for turn1, got %q", res1.PreviousResponseIDHash)
	}
	if len(res2.PreviousResponseIDHash) != 32 {
		t.Errorf("expected 32-char PreviousResponseIDHash for turn2, got %q", res2.PreviousResponseIDHash)
	}
	if res1.PreviousResponseIDHash == res2.PreviousResponseIDHash {
		t.Errorf("expected different PreviousResponseIDHash for different rotating IDs")
	}

	// has_previous_response_id indicator in option_hashes
	if res1.OptionHashes["has_previous_response_id"] != "true" {
		t.Errorf("expected OptionHashes[has_previous_response_id] = 'true', got %q", res1.OptionHashes["has_previous_response_id"])
	}
	if res2.OptionHashes["has_previous_response_id"] != "true" {
		t.Errorf("expected OptionHashes[has_previous_response_id] = 'true', got %q", res2.OptionHashes["has_previous_response_id"])
	}
	if _, ok := res1.OptionHashes["previous_response_id"]; ok {
		t.Errorf("rotating previous_response_id string must be excluded from OptionHashes")
	}
	if _, ok := res2.OptionHashes["previous_response_id"]; ok {
		t.Errorf("rotating previous_response_id string must be excluded from OptionHashes")
	}

	// Two consecutive turns with different previous_response_id values must yield the exact same OptionsHash
	if res1.OptionsHash != res2.OptionsHash {
		t.Fatalf("expected identical OptionsHash between turns differing only by rotating previous_response_id: %q != %q", res1.OptionsHash, res2.OptionsHash)
	}

	// Message hashes from input
	if len(res1.MessageHashes) != 1 || len(res2.MessageHashes) != 1 {
		t.Errorf("expected 1 message hash per turn input, got %d and %d", len(res1.MessageHashes), len(res2.MessageHashes))
	}
}

func TestExtractClaudeMessages(t *testing.T) {
	payload := map[string]any{
		"model":      "claude-3-7-sonnet-20250219",
		"max_tokens": 4096,
		"system": []any{
			map[string]any{
				"type": "text",
				"text": "You are a helpful AI assistant.",
				"cache_control": map[string]any{
					"type": "ephemeral",
				},
			},
		},
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": 2048,
		},
		"tools": []any{
			map[string]any{
				"name":        "search",
				"description": "Web search",
				"input_schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string"},
					},
				},
			},
		},
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "text",
						"text": "Explain quantum computing briefly.",
					},
				},
			},
			map[string]any{
				"role":    "assistant",
				"content": "Quantum computing harnesses quantum mechanics...",
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed to marshal payload: %v", err)
	}

	res := fingerprint.Extract("claude", "claude", body, 512)

	if res.Protocol != fingerprint.ProtocolClaudeMessages {
		t.Errorf("expected protocol %q, got %q", fingerprint.ProtocolClaudeMessages, res.Protocol)
	}
	if len(res.SystemHash) != 32 {
		t.Errorf("expected 32-char SystemHash for Claude system array with cache_control, got %q", res.SystemHash)
	}
	if len(res.ToolsHash) != 32 {
		t.Errorf("expected 32-char ToolsHash, got %q", res.ToolsHash)
	}
	if len(res.FirstUserHash) != 32 {
		t.Errorf("expected 32-char FirstUserHash, got %q", res.FirstUserHash)
	}
	if len(res.OptionHashes["thinking"]) != 32 {
		t.Errorf("expected 32-char OptionHashes[thinking], got %q", res.OptionHashes["thinking"])
	}
	if len(res.OptionHashes["max_tokens"]) != 32 {
		t.Errorf("expected 32-char OptionHashes[max_tokens], got %q", res.OptionHashes["max_tokens"])
	}
	if res.MessageCount != 2 {
		t.Errorf("expected MessageCount 2, got %d", res.MessageCount)
	}
	if len(res.MessageHashes) != 2 {
		t.Errorf("expected 2 MessageHashes, got %d", len(res.MessageHashes))
	}
	if len(res.PrefixHashes) != 2 {
		t.Errorf("expected 2 PrefixHashes, got %d", len(res.PrefixHashes))
	}
}

func TestExtractGeminiContents(t *testing.T) {
	// 1. Top-level camelCase
	topLevelPayload := map[string]any{
		"contents": []any{
			map[string]any{
				"role": "user",
				"parts": []any{
					map[string]any{"text": "Hello Gemini!"},
				},
			},
			map[string]any{
				"role": "model",
				"parts": []any{
					map[string]any{"text": "Hello! How can I assist you today?"},
				},
			},
		},
		"systemInstruction": map[string]any{
			"parts": []any{
				map[string]any{"text": "You are a concise Gemini model."},
			},
		},
		"generationConfig": map[string]any{
			"temperature":     0.4,
			"maxOutputTokens": 1024,
		},
		"tools": []any{
			map[string]any{
				"functionDeclarations": []any{
					map[string]any{"name": "fetch_doc"},
				},
			},
		},
	}

	body1, _ := json.Marshal(topLevelPayload)
	res1 := fingerprint.Extract("gemini", "gemini", body1, 512)

	if res1.Protocol != fingerprint.ProtocolGeminiContents {
		t.Errorf("expected protocol %q, got %q", fingerprint.ProtocolGeminiContents, res1.Protocol)
	}
	if len(res1.SystemHash) != 32 {
		t.Errorf("expected 32-char SystemHash, got %q", res1.SystemHash)
	}
	if len(res1.ToolsHash) != 32 {
		t.Errorf("expected 32-char ToolsHash, got %q", res1.ToolsHash)
	}
	if len(res1.FirstUserHash) != 32 {
		t.Errorf("expected 32-char FirstUserHash, got %q", res1.FirstUserHash)
	}
	if len(res1.OptionHashes["generationConfig"]) != 32 {
		t.Errorf("expected 32-char OptionHashes[generationConfig], got %q", res1.OptionHashes["generationConfig"])
	}
	if res1.MessageCount != 2 {
		t.Errorf("expected MessageCount 2, got %d", res1.MessageCount)
	}

	// 2. Wrapped inside {request: {...}} with snake_case
	wrappedPayload := map[string]any{
		"request": map[string]any{
			"contents": []any{
				map[string]any{
					"role": "user",
					"parts": []any{
						map[string]any{"text": "Hello Gemini!"},
					},
				},
				map[string]any{
					"role": "model",
					"parts": []any{
						map[string]any{"text": "Hello! How can I assist you today?"},
					},
				},
			},
			"system_instruction": map[string]any{
				"parts": []any{
					map[string]any{"text": "You are a concise Gemini model."},
				},
			},
			"generation_config": map[string]any{
				"temperature":     0.4,
				"maxOutputTokens": 1024,
			},
			"tool_config": map[string]any{
				"function_calling_config": map[string]any{"mode": "AUTO"},
			},
		},
	}

	body2, _ := json.Marshal(wrappedPayload)
	res2 := fingerprint.Extract("antigravity", "antigravity", body2, 512)

	if res2.Protocol != fingerprint.ProtocolGeminiContents {
		t.Errorf("expected protocol %q, got %q", fingerprint.ProtocolGeminiContents, res2.Protocol)
	}
	if res2.SystemHash != res1.SystemHash {
		t.Errorf("expected identical SystemHash between camelCase and snake_case system_instruction: %q != %q", res2.SystemHash, res1.SystemHash)
	}
	if len(res2.ToolsHash) != 32 {
		t.Errorf("expected 32-char ToolsHash for tool_config, got %q", res2.ToolsHash)
	}
	if res2.FirstUserHash != res1.FirstUserHash {
		t.Errorf("expected identical FirstUserHash: %q != %q", res2.FirstUserHash, res1.FirstUserHash)
	}
	if res2.OptionHashes["generationConfig"] != res1.OptionHashes["generationConfig"] {
		t.Errorf("expected identical generationConfig hash: %q != %q", res2.OptionHashes["generationConfig"], res1.OptionHashes["generationConfig"])
	}
}

func TestExtractOversizedAndMalformed(t *testing.T) {
	// Malformed JSON
	malformed := []byte(`{not-json-at-all: true,`)
	resMalformed := fingerprint.Extract("openai", "openai", malformed, 512)
	if !resMalformed.ParseSkipped {
		t.Errorf("expected ParseSkipped = true for malformed JSON")
	}
	if resMalformed.BodyBytes != len(malformed) {
		t.Errorf("expected BodyBytes = %d, got %d", len(malformed), resMalformed.BodyBytes)
	}
	if len(resMalformed.BodyHash) != 32 {
		t.Errorf("expected non-empty 32-char BodyHash, got %q", resMalformed.BodyHash)
	}

	// Oversized body (> MaxInspectBodyBytes)
	oversizedLen := fingerprint.MaxInspectBodyBytes + 1024
	oversized := bytes.Repeat([]byte("a"), oversizedLen)
	resOversized := fingerprint.Extract("openai", "openai", oversized, 512)
	if !resOversized.ParseSkipped {
		t.Errorf("expected ParseSkipped = true for oversized body")
	}
	if resOversized.BodyBytes != oversizedLen {
		t.Errorf("expected BodyBytes = %d, got %d", oversizedLen, resOversized.BodyBytes)
	}
	if len(resOversized.BodyHash) != 32 {
		t.Errorf("expected non-empty 32-char BodyHash, got %q", resOversized.BodyHash)
	}

	// Empty body
	resEmpty := fingerprint.Extract("openai", "openai", nil, 512)
	if !resEmpty.ParseSkipped {
		t.Errorf("expected ParseSkipped = true for empty body")
	}
	if resEmpty.BodyBytes != 0 {
		t.Errorf("expected BodyBytes = 0, got %d", resEmpty.BodyBytes)
	}
}

func TestExtractMaxMessageHashesCap(t *testing.T) {
	totalMsgs := 20
	capLimit := 5

	messages := make([]any, totalMsgs)
	for i := 0; i < totalMsgs; i++ {
		messages[i] = map[string]any{
			"role":    "user",
			"content": strings.Repeat("x", i+1),
		}
	}

	payload := map[string]any{
		"model":    "gpt-4o",
		"messages": messages,
	}
	body, _ := json.Marshal(payload)

	res := fingerprint.Extract("openai", "openai", body, capLimit)

	if res.MessageCount != totalMsgs {
		t.Errorf("expected MessageCount %d, got %d", totalMsgs, res.MessageCount)
	}
	if len(res.MessageHashes) != capLimit {
		t.Errorf("expected len(MessageHashes) == %d, got %d", capLimit, len(res.MessageHashes))
	}
	if len(res.PrefixHashes) != capLimit {
		t.Errorf("expected len(PrefixHashes) == %d, got %d", capLimit, len(res.PrefixHashes))
	}
	if !res.HashesTruncated {
		t.Errorf("expected HashesTruncated = true when message count exceeds cap")
	}

	// Non-capped request
	resUncapped := fingerprint.Extract("openai", "openai", body, 100)
	if resUncapped.HashesTruncated {
		t.Errorf("expected HashesTruncated = false when under cap")
	}
	if len(resUncapped.MessageHashes) != totalMsgs {
		t.Errorf("expected %d message hashes, got %d", totalMsgs, len(resUncapped.MessageHashes))
	}
	if len(resUncapped.PrefixHashes) != totalMsgs {
		t.Errorf("expected %d prefix hashes, got %d", totalMsgs, len(resUncapped.PrefixHashes))
	}
}

func TestCanonicalJSONKeyOrdering(t *testing.T) {
	// JSON with key order 1
	json1 := []byte(`{
		"model": "gpt-4o",
		"temperature": 0.7,
		"stream": true,
		"messages": [
			{"content": "hello", "role": "user"}
		]
	}`)

	// JSON with scrambled key order and different whitespace
	json2 := []byte(`{
		"messages": [
			{"role": "user", "content": "hello"}
		],
		"stream": true,
		"temperature": 0.7,
		"model": "gpt-4o"
	}`)

	res1 := fingerprint.Extract("openai", "openai", json1, 512)
	res2 := fingerprint.Extract("openai", "openai", json2, 512)

	if res1.OptionsHash != res2.OptionsHash {
		t.Errorf("expected identical OptionsHash despite key order differences: %q != %q", res1.OptionsHash, res2.OptionsHash)
	}
	if res1.FirstUserHash != res2.FirstUserHash {
		t.Errorf("expected identical FirstUserHash: %q != %q", res1.FirstUserHash, res2.FirstUserHash)
	}
	if len(res1.MessageHashes) != 1 || len(res2.MessageHashes) != 1 {
		t.Fatalf("expected 1 message hash each")
	}
	if res1.MessageHashes[0] != res2.MessageHashes[0] {
		t.Errorf("expected identical MessageHashes[0]: %q != %q", res1.MessageHashes[0], res2.MessageHashes[0])
	}
	if res1.PrefixHashes[0] != res2.PrefixHashes[0] {
		t.Errorf("expected identical PrefixHashes[0]: %q != %q", res1.PrefixHashes[0], res2.PrefixHashes[0])
	}
}

func TestExtractResultJSONTags(t *testing.T) {
	res := fingerprint.Result{
		Protocol:               fingerprint.ProtocolOpenAIChat,
		BodyBytes:              100,
		BodyHash:               "11111111111111111111111111111111",
		SystemHash:             "22222222222222222222222222222222",
		FirstUserHash:          "33333333333333333333333333333333",
		ToolsHash:              "44444444444444444444444444444444",
		OptionsHash:            "55555555555555555555555555555555",
		OptionHashes:           map[string]string{"model": "66666666666666666666666666666666"},
		MessageCount:           2,
		MessagesHash:           "77777777777777777777777777777777",
		MessageHashes:          []string{"88888888888888888888888888888888"},
		PrefixHashes:           []string{"99999999999999999999999999999999"},
		HashesTruncated:        true,
		IsContinuation:         true,
		PreviousResponseIDHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ParseSkipped:           false,
	}

	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("failed to marshal Result: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("failed to unmarshal JSON into map: %v", err)
	}

	expectedKeys := []string{
		"protocol",
		"body_bytes",
		"body_hash",
		"system_hash",
		"first_user_hash",
		"tools_hash",
		"options_hash",
		"option_hashes",
		"message_count",
		"messages_hash",
		"message_hashes",
		"prefix_hashes",
		"hashes_truncated",
		"is_continuation",
		"previous_response_id_hash",
	}

	for _, k := range expectedKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("expected json key %q in serialized Result", k)
		}
	}
}

func TestExtractEdgeCases(t *testing.T) {
	// 1. OpenAI chat with no system message
	chatNoSys := []byte(`{
		"model": "gpt-4o",
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	resChat := fingerprint.Extract("openai", "openai", chatNoSys, 0)
	if resChat.SystemHash != "" {
		t.Errorf("expected empty SystemHash when no system message, got %q", resChat.SystemHash)
	}
	if resChat.MessageCount != 1 {
		t.Errorf("expected MessageCount 1, got %d", resChat.MessageCount)
	}

	// 2. Responses with string input
	respStrInput := []byte(`{
		"model": "codex",
		"input": "Write some code"
	}`)
	resResp := fingerprint.Extract("responses", "responses", respStrInput, -1)
	if len(resResp.FirstUserHash) != 32 {
		t.Errorf("expected 32-char FirstUserHash for string input, got %q", resResp.FirstUserHash)
	}
	if resResp.MessageCount != 1 {
		t.Errorf("expected MessageCount 1 for string input, got %d", resResp.MessageCount)
	}

	// 3. Claude with string system
	claudeStrSys := []byte(`{
		"model": "claude-3-opus",
		"system": "Plain text system instruction",
		"metadata": {"user_id": "u-12345"},
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	resClaude := fingerprint.Extract("claude", "claude", claudeStrSys, 512)
	if len(resClaude.SystemHash) != 32 {
		t.Errorf("expected 32-char SystemHash for plain string system, got %q", resClaude.SystemHash)
	}
	if len(resClaude.OptionHashes["metadata.user_id"]) != 32 {
		t.Errorf("expected 32-char OptionHashes[metadata.user_id], got %q", resClaude.OptionHashes["metadata.user_id"])
	}

	// 4. Non-object valid JSON (array or primitive)
	resArray := fingerprint.Extract("openai", "openai", []byte(`[1, 2, 3]`), 512)
	if !resArray.ParseSkipped {
		t.Errorf("expected ParseSkipped = true for top-level JSON array")
	}
	resPrimitive := fingerprint.Extract("openai", "openai", []byte(`"just a string"`), 512)
	if !resPrimitive.ParseSkipped {
		t.Errorf("expected ParseSkipped = true for top-level JSON string")
	}
}

func TestExtractCrossProtocolPrecedence(t *testing.T) {
	// 1. sourceFormat="claude", toFormat="codex" with Claude payload (top-level system + messages)
	// Must extract as claude_messages, NOT misroute to openai_responses
	claudeBody, _ := json.Marshal(map[string]any{
		"model":  "claude-3-7-sonnet",
		"system": "You are Claude acting in a proxy.",
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello from Claude"},
			map[string]any{"role": "assistant", "content": "Hello user"},
		},
	})
	res1 := fingerprint.Extract("claude", "codex", claudeBody, 512)
	if res1.Protocol != fingerprint.ProtocolClaudeMessages {
		t.Fatalf("expected protocol %q, got %q (misrouted by toFormat hint)", fingerprint.ProtocolClaudeMessages, res1.Protocol)
	}
	if len(res1.SystemHash) != 32 {
		t.Errorf("expected 32-char SystemHash for Claude system, got %q", res1.SystemHash)
	}
	if res1.MessageCount != 2 {
		t.Errorf("expected MessageCount 2, got %d", res1.MessageCount)
	}

	// 2. sourceFormat="claude", toFormat="openai" with already-translated OpenAI payload (messages with role: "system")
	// Must extract as openai_chat, NOT misroute to claude_messages
	openaiBody, _ := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a converted OpenAI assistant."},
			map[string]any{"role": "user", "content": "Hello OpenAI"},
		},
	})
	res2 := fingerprint.Extract("claude", "openai", openaiBody, 512)
	if res2.Protocol != fingerprint.ProtocolOpenAIChat {
		t.Fatalf("expected protocol %q, got %q (misrouted by sourceFormat hint)", fingerprint.ProtocolOpenAIChat, res2.Protocol)
	}
	if len(res2.SystemHash) != 32 {
		t.Errorf("expected 32-char SystemHash from OpenAI leading system message, got %q", res2.SystemHash)
	}
	if res2.MessageCount != 1 {
		t.Errorf("expected MessageCount 1 (excluding leading system), got %d", res2.MessageCount)
	}

	// 3. sourceFormat="openai", toFormat="gemini" with OpenAI payload
	// Must extract as openai_chat, NOT misroute to gemini_contents
	openaiBodyDev, _ := json.Marshal(map[string]any{
		"model": "gpt-4o",
		"messages": []any{
			map[string]any{"role": "developer", "content": "Developer instructions."},
			map[string]any{"role": "user", "content": "Hello from developer chat"},
		},
	})
	res3 := fingerprint.Extract("openai", "gemini", openaiBodyDev, 512)
	if res3.Protocol != fingerprint.ProtocolOpenAIChat {
		t.Fatalf("expected protocol %q, got %q (misrouted by toFormat hint)", fingerprint.ProtocolOpenAIChat, res3.Protocol)
	}
	if len(res3.SystemHash) != 32 {
		t.Errorf("expected 32-char SystemHash from leading developer message, got %q", res3.SystemHash)
	}
	if res3.MessageCount != 1 {
		t.Errorf("expected MessageCount 1, got %d", res3.MessageCount)
	}
}
