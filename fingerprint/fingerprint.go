package fingerprint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// MaxInspectBodyBytes is the maximum request payload size inspected for fingerprinting (32 MiB).
const MaxInspectBodyBytes = 32 * 1024 * 1024

// Protocol identifier constants.
const (
	ProtocolOpenAIChat      = "openai_chat"
	ProtocolOpenAIResponses = "openai_responses"
	ProtocolClaudeMessages  = "claude_messages"
	ProtocolGeminiContents  = "gemini_contents"
	ProtocolUnknown         = "unknown"
)

// Result holds the content-free hashes and metadata extracted from a request payload.
type Result struct {
	Protocol               string            `json:"protocol,omitempty"`
	BodyBytes              int               `json:"body_bytes"`
	BodyHash               string            `json:"body_hash,omitempty"`
	SystemHash             string            `json:"system_hash,omitempty"`
	FirstUserHash          string            `json:"first_user_hash,omitempty"`
	ToolsHash              string            `json:"tools_hash,omitempty"`
	OptionsHash            string            `json:"options_hash,omitempty"`
	OptionHashes           map[string]string `json:"option_hashes,omitempty"`
	MessageCount           int               `json:"message_count"`
	MessagesHash           string            `json:"messages_hash,omitempty"`
	MessageHashes          []string          `json:"message_hashes,omitempty"`
	PrefixHashes           []string          `json:"prefix_hashes,omitempty"`
	HashesTruncated        bool              `json:"hashes_truncated,omitempty"`
	IsContinuation         bool              `json:"is_continuation,omitempty"`
	PreviousResponseIDHash string            `json:"previous_response_id_hash,omitempty"`
	ParseSkipped           bool              `json:"parse_skipped,omitempty"`
}

// hashBytes returns the first 16 bytes of sha256(b) as 32 lowercase hex characters.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// canonicalJSON returns deterministic, key-sorted JSON bytes without unnecessary whitespace.
func canonicalJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func addOption(res *Result, key, value string) {
	if res.OptionHashes == nil {
		res.OptionHashes = make(map[string]string)
	}
	res.OptionHashes[key] = value
}

// computePrefixHashes computes progressive rolling hashes across message sequence
// incorporating system_hash and tools_hash.
func computePrefixHashes(systemHash, toolsHash string, msgHashes []string) []string {
	if len(msgHashes) == 0 {
		return nil
	}
	prefixHashes := make([]string, len(msgHashes))
	h := sha256.New()
	h.Write([]byte(systemHash))
	h.Write([]byte{':'})
	h.Write([]byte(toolsHash))
	prev := hex.EncodeToString(h.Sum(nil)[:16])

	for i, mHash := range msgHashes {
		step := sha256.New()
		step.Write([]byte(prev))
		step.Write([]byte{':'})
		step.Write([]byte(mHash))
		prev = hex.EncodeToString(step.Sum(nil)[:16])
		prefixHashes[i] = prev
	}
	return prefixHashes
}

// Extract parses a pre-executor request body across 4 protocols and returns content-free fingerprints.
func Extract(sourceFormat, toFormat string, body []byte, maxMsgHashes int) Result {
	if maxMsgHashes <= 0 {
		maxMsgHashes = 512
	}

	var res Result
	res.BodyBytes = len(body)
	if len(body) == 0 {
		res.ParseSkipped = true
		return res
	}

	res.BodyHash = hashBytes(body)
	if len(body) > MaxInspectBodyBytes {
		res.ParseSkipped = true
		return res
	}

	var parsed any
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := d.Decode(&parsed); err != nil {
		res.ParseSkipped = true
		return res
	}

	root, ok := parsed.(map[string]any)
	if !ok {
		res.ParseSkipped = true
		return res
	}

	proto := detectProtocol(sourceFormat, toFormat, root)
	res.Protocol = proto

	switch proto {
	case ProtocolGeminiContents:
		extractGemini(&res, root, maxMsgHashes)
	case ProtocolOpenAIResponses:
		extractOpenAIResponses(&res, root, maxMsgHashes)
	case ProtocolClaudeMessages:
		extractClaudeMessages(&res, root, maxMsgHashes)
	case ProtocolOpenAIChat:
		extractOpenAIChat(&res, root, maxMsgHashes)
	default:
		// Unknown protocol with valid JSON
		if len(res.OptionHashes) > 0 {
			res.OptionsHash = hashBytes(canonicalJSON(res.OptionHashes))
		}
	}

	return res
}

func hasOpenAISystemRole(msgs []any) bool {
	for _, item := range msgs {
		if m, ok := item.(map[string]any); ok {
			if role, _ := m["role"].(string); role == "system" || role == "developer" {
				return true
			}
		}
	}
	return false
}

func detectProtocol(sourceFormat, toFormat string, root map[string]any) string {
	sf := strings.ToLower(strings.TrimSpace(sourceFormat))
	tf := strings.ToLower(strings.TrimSpace(toFormat))

	// 1. Structural shape checks (must take precedence over format hints)
	// Gemini: contents or request.contents / request.system_instruction
	if _, ok := root["contents"]; ok {
		return ProtocolGeminiContents
	}
	if req, ok := root["request"].(map[string]any); ok {
		if _, okContents := req["contents"]; okContents {
			return ProtocolGeminiContents
		}
		if _, okSys := req["system_instruction"]; okSys {
			return ProtocolGeminiContents
		}
		if _, okSysCamel := req["systemInstruction"]; okSysCamel {
			return ProtocolGeminiContents
		}
	}

	// OpenAI Responses / Codex: input
	if _, ok := root["input"]; ok {
		return ProtocolOpenAIResponses
	}

	// Messages payload: Claude Messages vs OpenAI Chat
	if msgsRaw, ok := root["messages"].([]any); ok {
		// Claude structural markers: top-level system, thinking, or anthropic_version
		if _, okSys := root["system"]; okSys {
			return ProtocolClaudeMessages
		}
		if _, okThinking := root["thinking"]; okThinking {
			return ProtocolClaudeMessages
		}
		if _, okAnthropic := root["anthropic_version"]; okAnthropic {
			return ProtocolClaudeMessages
		}

		// OpenAI structural markers: leading/embedded system or developer messages
		if hasOpenAISystemRole(msgsRaw) {
			return ProtocolOpenAIChat
		}

		// Shape is ambiguous (e.g. messages with only user/assistant roles).
		// Break ties with format hints (preferring sourceFormat first).
		if strings.Contains(sf, "claude") || strings.Contains(sf, "anthropic") {
			return ProtocolClaudeMessages
		}
		if strings.Contains(sf, "openai") || strings.Contains(sf, "chat") {
			return ProtocolOpenAIChat
		}
		if strings.Contains(tf, "claude") || strings.Contains(tf, "anthropic") {
			return ProtocolClaudeMessages
		}
		if strings.Contains(tf, "openai") || strings.Contains(tf, "chat") {
			return ProtocolOpenAIChat
		}

		// Default for messages payload
		return ProtocolOpenAIChat
	}

	// 2. Fallback to format hints when shape has no contents/input/messages
	for _, hint := range []string{sf, tf} {
		if hint == "" {
			continue
		}
		if strings.Contains(hint, "gemini") || strings.Contains(hint, "antigravity") {
			return ProtocolGeminiContents
		}
		if strings.Contains(hint, "response") || strings.Contains(hint, "codex") {
			return ProtocolOpenAIResponses
		}
		if strings.Contains(hint, "claude") || strings.Contains(hint, "anthropic") {
			return ProtocolClaudeMessages
		}
		if strings.Contains(hint, "openai") || strings.Contains(hint, "chat") {
			return ProtocolOpenAIChat
		}
	}

	return ProtocolUnknown
}

func extractOpenAIChat(res *Result, root map[string]any, maxMsgHashes int) {
	// 1. messages
	var leadingSystem []any
	var historyMsgs []any
	foundNonSystem := false

	if msgsRaw, ok := root["messages"].([]any); ok {
		for _, item := range msgsRaw {
			m, ok := item.(map[string]any)
			if !foundNonSystem && ok && (m["role"] == "system" || m["role"] == "developer") {
				leadingSystem = append(leadingSystem, item)
			} else {
				foundNonSystem = true
				historyMsgs = append(historyMsgs, item)
			}
			if res.FirstUserHash == "" && ok && m["role"] == "user" {
				res.FirstUserHash = hashBytes(canonicalJSON(m))
			}
		}
	}

	if len(leadingSystem) > 0 {
		res.SystemHash = hashBytes(canonicalJSON(leadingSystem))
	}

	// 2. tools
	toolsVal := root["tools"]
	if toolsVal == nil {
		toolsVal = root["functions"]
	}
	if toolsVal != nil {
		res.ToolsHash = hashBytes(canonicalJSON(toolsVal))
	}

	// 3. options
	optionsKeys := []string{
		"model", "temperature", "top_p", "reasoning_effort", "tool_choice",
		"response_format", "max_tokens", "max_completion_tokens", "prompt_cache_key",
		"service_tier", "stream", "seed", "stop", "frequency_penalty", "presence_penalty", "n",
	}
	for _, k := range optionsKeys {
		if val, exists := root[k]; exists && val != nil {
			addOption(res, k, hashBytes(canonicalJSON(val)))
		}
	}
	if len(res.OptionHashes) > 0 {
		res.OptionsHash = hashBytes(canonicalJSON(res.OptionHashes))
	}

	// 4. message hashes and rolling prefix hashes
	res.MessageCount = len(historyMsgs)
	capLimit := len(historyMsgs)
	if capLimit > maxMsgHashes {
		capLimit = maxMsgHashes
		res.HashesTruncated = true
	}
	if capLimit > 0 {
		res.MessageHashes = make([]string, capLimit)
		for i := 0; i < capLimit; i++ {
			res.MessageHashes[i] = hashBytes(canonicalJSON(historyMsgs[i]))
		}
		res.PrefixHashes = computePrefixHashes(res.SystemHash, res.ToolsHash, res.MessageHashes)
	}
	if len(historyMsgs) > 0 {
		res.MessagesHash = hashBytes(canonicalJSON(historyMsgs))
	}
}

func extractOpenAIResponses(res *Result, root map[string]any, maxMsgHashes int) {
	// 1. instructions and leading system items
	instructions := root["instructions"]

	var inputItems []any
	if inputVal, ok := root["input"]; ok && inputVal != nil {
		if slice, ok := inputVal.([]any); ok {
			inputItems = slice
		} else {
			inputItems = []any{inputVal}
		}
	}

	var leadingSystem []any
	var historyItems []any
	foundNonSystem := false
	for _, item := range inputItems {
		m, ok := item.(map[string]any)
		if !foundNonSystem && ok && (m["role"] == "system" || m["role"] == "developer") {
			leadingSystem = append(leadingSystem, item)
		} else {
			foundNonSystem = true
			historyItems = append(historyItems, item)
		}
		if res.FirstUserHash == "" {
			if ok && m["role"] == "user" {
				res.FirstUserHash = hashBytes(canonicalJSON(m))
			} else if !ok {
				res.FirstUserHash = hashBytes(canonicalJSON(item))
			}
		}
	}
	if res.FirstUserHash == "" && len(historyItems) > 0 {
		res.FirstUserHash = hashBytes(canonicalJSON(historyItems[0]))
	}

	if instructions != nil && len(leadingSystem) > 0 {
		combo := map[string]any{"instructions": instructions, "leading": leadingSystem}
		res.SystemHash = hashBytes(canonicalJSON(combo))
	} else if instructions != nil {
		res.SystemHash = hashBytes(canonicalJSON(instructions))
	} else if len(leadingSystem) > 0 {
		res.SystemHash = hashBytes(canonicalJSON(leadingSystem))
	}

	// 2. tools
	if toolsVal := root["tools"]; toolsVal != nil {
		res.ToolsHash = hashBytes(canonicalJSON(toolsVal))
	}

	// 3. previous_response_id and continuation
	if prevID, ok := root["previous_response_id"]; ok && prevID != nil {
		prevIDStr := fmt.Sprint(prevID)
		if prevIDStr != "" {
			res.IsContinuation = true
			res.PreviousResponseIDHash = hashBytes([]byte(prevIDStr))
			addOption(res, "has_previous_response_id", "true")
		}
	}

	// 4. options
	optionsKeys := []string{
		"model", "reasoning", "tool_choice", "text", "max_output_tokens",
		"prompt_cache_key", "truncation", "service_tier", "stream",
		"temperature", "top_p",
	}
	for _, k := range optionsKeys {
		if val, exists := root[k]; exists && val != nil {
			addOption(res, k, hashBytes(canonicalJSON(val)))
		}
	}
	if len(res.OptionHashes) > 0 {
		res.OptionsHash = hashBytes(canonicalJSON(res.OptionHashes))
	}

	// 5. message hashes and rolling prefix hashes
	res.MessageCount = len(historyItems)
	capLimit := len(historyItems)
	if capLimit > maxMsgHashes {
		capLimit = maxMsgHashes
		res.HashesTruncated = true
	}
	if capLimit > 0 {
		res.MessageHashes = make([]string, capLimit)
		for i := 0; i < capLimit; i++ {
			res.MessageHashes[i] = hashBytes(canonicalJSON(historyItems[i]))
		}
		res.PrefixHashes = computePrefixHashes(res.SystemHash, res.ToolsHash, res.MessageHashes)
	}
	if len(historyItems) > 0 {
		res.MessagesHash = hashBytes(canonicalJSON(historyItems))
	}
}

func extractClaudeMessages(res *Result, root map[string]any, maxMsgHashes int) {
	// 1. system
	if sysVal, ok := root["system"]; ok && sysVal != nil {
		res.SystemHash = hashBytes(canonicalJSON(sysVal))
	}

	// 2. tools
	if toolsVal := root["tools"]; toolsVal != nil {
		res.ToolsHash = hashBytes(canonicalJSON(toolsVal))
	}

	// 3. messages
	var messages []any
	if msgsRaw, ok := root["messages"].([]any); ok {
		messages = msgsRaw
		for _, item := range messages {
			if m, ok := item.(map[string]any); ok && res.FirstUserHash == "" && m["role"] == "user" {
				res.FirstUserHash = hashBytes(canonicalJSON(m))
			}
		}
	}

	// 4. options
	optionsKeys := []string{
		"model", "max_tokens", "temperature", "top_p", "thinking", "tool_choice", "stream",
	}
	for _, k := range optionsKeys {
		if val, exists := root[k]; exists && val != nil {
			addOption(res, k, hashBytes(canonicalJSON(val)))
		}
	}
	if meta, ok := root["metadata"].(map[string]any); ok {
		if userID, ok := meta["user_id"]; ok && userID != nil {
			addOption(res, "metadata.user_id", hashBytes(canonicalJSON(userID)))
		}
	}
	if len(res.OptionHashes) > 0 {
		res.OptionsHash = hashBytes(canonicalJSON(res.OptionHashes))
	}

	// 5. message hashes and rolling prefix hashes
	res.MessageCount = len(messages)
	capLimit := len(messages)
	if capLimit > maxMsgHashes {
		capLimit = maxMsgHashes
		res.HashesTruncated = true
	}
	if capLimit > 0 {
		res.MessageHashes = make([]string, capLimit)
		for i := 0; i < capLimit; i++ {
			res.MessageHashes[i] = hashBytes(canonicalJSON(messages[i]))
		}
		res.PrefixHashes = computePrefixHashes(res.SystemHash, res.ToolsHash, res.MessageHashes)
	}
	if len(messages) > 0 {
		res.MessagesHash = hashBytes(canonicalJSON(messages))
	}
}

func extractGemini(res *Result, root map[string]any, maxMsgHashes int) {
	isWrapped := false
	target := root
	if reqObj, ok := root["request"].(map[string]any); ok && reqObj != nil {
		target = reqObj
		isWrapped = true
	}

	lookup := func(keys ...string) any {
		for _, k := range keys {
			if v, ok := target[k]; ok && v != nil {
				return v
			}
		}
		if isWrapped {
			for _, k := range keys {
				if v, ok := root[k]; ok && v != nil {
					return v
				}
			}
		}
		return nil
	}

	// 1. systemInstruction / system_instruction
	sysVal := lookup("systemInstruction", "system_instruction")
	if sysVal != nil {
		res.SystemHash = hashBytes(canonicalJSON(sysVal))
	}

	// 2. tools and toolConfig / tool_config
	toolsVal := lookup("tools")
	toolCfgVal := lookup("toolConfig", "tool_config")
	if toolsVal != nil && toolCfgVal != nil {
		combo := map[string]any{"tools": toolsVal, "toolConfig": toolCfgVal}
		res.ToolsHash = hashBytes(canonicalJSON(combo))
	} else if toolsVal != nil {
		res.ToolsHash = hashBytes(canonicalJSON(toolsVal))
	} else if toolCfgVal != nil {
		res.ToolsHash = hashBytes(canonicalJSON(toolCfgVal))
	}

	// 3. contents
	var contents []any
	if contentsRaw, ok := lookup("contents").([]any); ok {
		contents = contentsRaw
		for _, item := range contents {
			if m, ok := item.(map[string]any); ok && res.FirstUserHash == "" {
				if m["role"] == "user" || len(contents) == 1 {
					res.FirstUserHash = hashBytes(canonicalJSON(m))
				}
			}
		}
		if res.FirstUserHash == "" && len(contents) > 0 {
			res.FirstUserHash = hashBytes(canonicalJSON(contents[0]))
		}
	}

	// 4. options
	modelVal := lookup("model")
	if modelVal != nil {
		addOption(res, "model", hashBytes(canonicalJSON(modelVal)))
	}
	genConfig := lookup("generationConfig", "generation_config")
	if genConfig != nil {
		addOption(res, "generationConfig", hashBytes(canonicalJSON(genConfig)))
	}
	if len(res.OptionHashes) > 0 {
		res.OptionsHash = hashBytes(canonicalJSON(res.OptionHashes))
	}

	// 5. message hashes and rolling prefix hashes
	res.MessageCount = len(contents)
	capLimit := len(contents)
	if capLimit > maxMsgHashes {
		capLimit = maxMsgHashes
		res.HashesTruncated = true
	}
	if capLimit > 0 {
		res.MessageHashes = make([]string, capLimit)
		for i := 0; i < capLimit; i++ {
			res.MessageHashes[i] = hashBytes(canonicalJSON(contents[i]))
		}
		res.PrefixHashes = computePrefixHashes(res.SystemHash, res.ToolsHash, res.MessageHashes)
	}
	if len(contents) > 0 {
		res.MessagesHash = hashBytes(canonicalJSON(contents))
	}
}
