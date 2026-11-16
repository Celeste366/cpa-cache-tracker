// Package abi defines the wire protocol, RPC schemas, and configuration types
// for CLIProxyAPI (CPA) v8 plugins. It has zero external dependencies (stdlib only).
package abi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// ABIVersion tracks the native C ABI shape (native plugin exports).
	ABIVersion uint32 = 1

	// SchemaVersion tracks the RPC JSON contract exchanged at plugin.register.
	// Version 10 is the current schema supported by CPA v8.
	SchemaVersion uint32 = 10

	// Schema version milestones for reference and capability negotiation.
	SchemaVersionStreamChunkOmitRequestBody  uint32 = 3
	SchemaVersionWebSocketResponseObserver   uint32 = 4
	SchemaVersionStreamChunkOmitHistory      uint32 = 5
	SchemaVersionRawManagementResponse       uint32 = 6
	SchemaVersionRequestInterceptorOmitBody  uint32 = 7
	SchemaVersionFrontendAuthOmitBody        uint32 = 7
	SchemaVersionRequestInterceptorBodyPatch uint32 = 8
	SchemaVersionMultiProviderModels         uint32 = 9
	SchemaVersionMultiAuthProviders          uint32 = 10
)

// CPA RPC Method Names
const (
	MethodPluginRegister    = "plugin.register"
	MethodPluginQuiesce     = "plugin.quiesce"
	MethodPluginReconfigure = "plugin.reconfigure"
	MethodPluginShutdown    = "plugin.shutdown"

	MethodRequestInterceptBefore = "request.intercept_before"
	MethodRequestInterceptAfter  = "request.intercept_after"
	MethodRequestPatchBodyBefore = "request.patch_body_before"
	MethodRequestPatchBodyAfter  = "request.patch_body_after"
	MethodRequestComplete        = "request.complete"

	MethodUsageHandle = "usage.handle"

	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"
)

// PluginConfig defines the runtime configuration loaded from CPA config.yaml
// under plugins.configs.cpa-cache-tracker.
type PluginConfig struct {
	TraceEnabled     bool   `json:"trace_enabled"`
	RetentionDays    int    `json:"retention_days"`
	MaxDiskMB        int    `json:"max_disk_mb"`
	MaxMessageHashes int    `json:"max_message_hashes"`
	DataDir          string `json:"data_dir"`
}

// DefaultPluginConfig returns the default configuration for cpa-cache-tracker.
func DefaultPluginConfig() PluginConfig {
	return PluginConfig{
		TraceEnabled:     true,
		RetentionDays:    14,
		MaxDiskMB:        200,
		MaxMessageHashes: 512,
		DataDir:          ".cache_tracker_data",
	}
}

// ParseConfigYAML parses plugin configuration from raw YAML bytes.
//
// Rules:
// - Comments (#) and blank lines are ignored.
// - Quotes surrounding values are stripped.
// - retention_days: 0 is explicitly preserved (meaning disabled pruning).
// - retention_days < 0 falls back to default 14.
// - max_disk_mb <= 0 falls back to default 200.
// - max_message_hashes <= 0 falls back to default 512.
// - Missing keys retain their default values.
func ParseConfigYAML(raw []byte) PluginConfig {
	cfg := DefaultPluginConfig()
	if len(raw) == 0 {
		return cfg
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		colonIdx := strings.Index(line, ":")
		if colonIdx < 0 {
			continue
		}

		key := strings.TrimSpace(line[:colonIdx])
		val := strings.TrimSpace(line[colonIdx+1:])

		val = stripYAMLComment(val)
		val = unquoteYAMLString(val)

		switch key {
		case "trace_enabled":
			switch strings.ToLower(val) {
			case "true", "1", "yes", "on":
				cfg.TraceEnabled = true
			case "false", "0", "no", "off":
				cfg.TraceEnabled = false
			}
		case "retention_days":
			if n, err := strconv.Atoi(val); err == nil {
				if n < 0 {
					cfg.RetentionDays = 14
				} else {
					cfg.RetentionDays = n
				}
			}
		case "max_disk_mb":
			if n, err := strconv.Atoi(val); err == nil {
				if n <= 0 {
					cfg.MaxDiskMB = 200
				} else {
					cfg.MaxDiskMB = n
				}
			}
		case "max_message_hashes":
			if n, err := strconv.Atoi(val); err == nil {
				if n <= 0 {
					cfg.MaxMessageHashes = 512
				} else {
					cfg.MaxMessageHashes = n
				}
			}
		case "data_dir":
			if val != "" {
				cfg.DataDir = val
			}
		}
	}

	return cfg
}

func stripYAMLComment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if s[0] == '"' || s[0] == '\'' {
		quote := s[0]
		for i := 1; i < len(s); i++ {
			if s[i] == quote && (i == 1 || s[i-1] != '\\') {
				remainder := s[i+1:]
				if hashIdx := strings.Index(remainder, "#"); hashIdx >= 0 {
					return strings.TrimSpace(s[:i+1])
				}
				return s
			}
		}
		return s
	}

	if idx := strings.Index(s, "#"); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return s
}

func unquoteYAMLString(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// Envelope wraps every RPC response returned across the C ABI.
type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *EnvelopeError  `json:"error,omitempty"`
}

// EnvelopeError represents an error payload in an Envelope.
type EnvelopeError struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	Retryable         bool   `json:"retryable,omitempty"`
	HTTPStatus        int    `json:"http_status,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds,omitempty"`
	CredentialScoped  bool   `json:"credential_scoped,omitempty"`
}

// Error alias for EnvelopeError for API flexibility.
type Error = EnvelopeError

// Error implements the standard error interface.
func (e *EnvelopeError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// OKEnvelope serializes a value into a successful RPC Envelope.
func OKEnvelope(v any) ([]byte, error) {
	var raw json.RawMessage
	if v != nil {
		if rm, ok := v.(json.RawMessage); ok {
			raw = rm
		} else if b, ok := v.([]byte); ok {
			raw = json.RawMessage(b)
		} else {
			b, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			raw = json.RawMessage(b)
		}
	}
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	return json.Marshal(Envelope{
		OK:     true,
		Result: raw,
	})
}

// ErrorEnvelope serializes an error into a failed RPC Envelope.
func ErrorEnvelope(code, msg string, httpStatus ...int) []byte {
	status := 0
	if len(httpStatus) > 0 {
		status = httpStatus[0]
	}
	raw, _ := json.Marshal(Envelope{
		OK: false,
		Error: &EnvelopeError{
			Code:       code,
			Message:    msg,
			HTTPStatus: status,
		},
	})
	return raw
}

// LifecycleRequest is sent by CPA on plugin.register and plugin.reconfigure.
type LifecycleRequest struct {
	ConfigYAML         []byte `json:"config_yaml"`
	SchemaVersion      uint32 `json:"schema_version"`
	MultiAuthProviders bool   `json:"multi_auth_providers,omitempty"`
}

// Registration is returned by plugin.register and plugin.reconfigure.
type Registration struct {
	SchemaVersion uint32       `json:"schema_version"`
	Metadata      Metadata     `json:"metadata"`
	Capabilities  Capabilities `json:"capabilities"`
}

// Metadata describes the plugin for CPA registry and management clients.
type Metadata struct {
	Name             string        `json:"name"`
	Version          string        `json:"version"`
	Author           string        `json:"author,omitempty"`
	GitHubRepository string        `json:"github_repository,omitempty"`
	Logo             string        `json:"logo,omitempty"`
	ConfigFields     []ConfigField `json:"config_fields,omitempty"`
}

// ConfigFieldType classifies plugin-owned configuration values.
type ConfigFieldType string

const (
	ConfigFieldTypeString  ConfigFieldType = "string"
	ConfigFieldTypeNumber  ConfigFieldType = "number"
	ConfigFieldTypeInteger ConfigFieldType = "integer"
	ConfigFieldTypeBoolean ConfigFieldType = "boolean"
	ConfigFieldTypeEnum    ConfigFieldType = "enum"
	ConfigFieldTypeArray   ConfigFieldType = "array"
	ConfigFieldTypeObject  ConfigFieldType = "object"
)

// ConfigField describes a single configuration field in metadata.
type ConfigField struct {
	Name        string          `json:"name"`
	Type        ConfigFieldType `json:"type"`
	Default     any             `json:"default,omitempty"`
	EnumValues  []string        `json:"enum_values,omitempty"`
	Description string          `json:"description,omitempty"`
}

// Capabilities declares which integration points this plugin implements.
type Capabilities struct {
	ModelRegistrar                    bool     `json:"model_registrar,omitempty"`
	ModelProvider                     bool     `json:"model_provider,omitempty"`
	AuthProvider                      bool     `json:"auth_provider,omitempty"`
	FrontendAuthProvider              bool     `json:"frontend_auth_provider,omitempty"`
	FrontendAuthProviderExclusive     bool     `json:"frontend_auth_provider_exclusive,omitempty"`
	FrontendAuthProviderOmitBody      bool     `json:"frontend_auth_provider_omit_body,omitempty"`
	FrontendAuthOmitBody              bool     `json:"frontend_auth_omit_body,omitempty"`
	Scheduler                         bool     `json:"scheduler,omitempty"`
	SchedulerAcrossPriorities         bool     `json:"scheduler_across_priorities,omitempty"`
	ModelRouter                       bool     `json:"model_router,omitempty"`
	Executor                          bool     `json:"executor,omitempty"`
	ExecutorModelScope                string   `json:"executor_model_scope,omitempty"`
	ExecutorInputFormats              []string `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats             []string `json:"executor_output_formats,omitempty"`
	RequestTranslator                 bool     `json:"request_translator,omitempty"`
	RequestNormalizer                 bool     `json:"request_normalizer,omitempty"`
	RequestInterceptor                bool     `json:"request_interceptor,omitempty"`
	RequestInterceptorOmitBodyBefore  bool     `json:"request_interceptor_omit_body_before,omitempty"`
	RequestInterceptorOmitBodyAfter   bool     `json:"request_interceptor_omit_body_after"`
	RequestBodyPatcherBefore          bool     `json:"request_body_patcher_before,omitempty"`
	RequestBodyPatcherAfter           bool     `json:"request_body_patcher_after,omitempty"`
	RequestInterceptorPatchBodyBefore bool     `json:"request_interceptor_patch_body_before,omitempty"`
	RequestInterceptorPatchBodyAfter  bool     `json:"request_interceptor_patch_body_after,omitempty"`
	RequestLifecyclePlugin            bool     `json:"request_lifecycle_plugin,omitempty"`
	ResponseTranslator                bool     `json:"response_translator,omitempty"`
	ResponseBeforeTranslator          bool     `json:"response_before_translator,omitempty"`
	ResponseAfterTranslator           bool     `json:"response_after_translator,omitempty"`
	ResponseInterceptor               bool     `json:"response_interceptor,omitempty"`
	StreamChunkInterceptor            bool     `json:"response_stream_interceptor,omitempty"`
	WebSocketResponseObserver         bool     `json:"websocket_response_observer,omitempty"`
	ThinkingApplier                   bool     `json:"thinking_applier,omitempty"`
	UsagePlugin                       bool     `json:"usage_plugin,omitempty"`
	CommandLinePlugin                 bool     `json:"command_line_plugin,omitempty"`
	ManagementAPI                     bool     `json:"management_api,omitempty"`
	QuotaProvider                     bool     `json:"quota_provider,omitempty"`
}

// RequestInterceptRequest is received in request.intercept_before and request.intercept_after.
// Uses PascalCase JSON tags to match sdk/pluginapi/types.go.
type RequestInterceptRequest struct {
	RequestID      string         `json:"RequestID"`
	TraceID        string         `json:"TraceID"`
	SourceFormat   string         `json:"SourceFormat"`
	ToFormat       string         `json:"ToFormat"`
	Model          string         `json:"Model"`
	RequestedModel string         `json:"RequestedModel"`
	Stream         bool           `json:"Stream"`
	Headers        http.Header    `json:"Headers"`
	Body           []byte         `json:"Body"`
	CallerScope    string         `json:"CallerScope"`
	Metadata       map[string]any `json:"Metadata"`
}

// RequestInterceptResponse is returned from request.intercept_before and request.intercept_after.
type RequestInterceptResponse struct {
	Path            string            `json:"path,omitempty"`
	Headers         http.Header       `json:"headers,omitempty"`
	Body            []byte            `json:"body,omitempty"`
	BodyEdits       []RequestBodyEdit `json:"body_edits,omitempty"`
	ClearHeaders    []string          `json:"clear_headers,omitempty"`
	Terminate       bool              `json:"terminate,omitempty"`
	StatusCode      int               `json:"status_code,omitempty"`
	ResponseHeaders http.Header       `json:"response_headers,omitempty"`
	ResponseBody    []byte            `json:"response_body,omitempty"`
}

// RequestBodyEdit describes localized byte-range replacement.
type RequestBodyEdit struct {
	Start            int    `json:"start"`
	End              int    `json:"end"`
	Replacement      string `json:"replacement,omitempty"`
	ReplacementBytes []byte `json:"replacement_bytes,omitempty"`
}

// RequestCompletionOutcome identifies how an intercepted request ended.
type RequestCompletionOutcome string

const (
	RequestCompletionSucceeded RequestCompletionOutcome = "succeeded"
	RequestCompletionFailed    RequestCompletionOutcome = "failed"
	RequestCompletionRejected  RequestCompletionOutcome = "rejected"
	RequestCompletionCanceled  RequestCompletionOutcome = "canceled"
)

// RequestCompletion is received on request.complete.
// Uses PascalCase JSON tags matching sdk/pluginapi/types.go.
type RequestCompletion struct {
	RequestID      string                   `json:"RequestID"`
	TraceID        string                   `json:"TraceID"`
	SourceFormat   string                   `json:"SourceFormat"`
	Model          string                   `json:"Model"`
	RequestedModel string                   `json:"RequestedModel"`
	Stream         bool                     `json:"Stream"`
	Outcome        RequestCompletionOutcome `json:"Outcome"`
	StatusCode     int                      `json:"StatusCode"`
	Error          string                   `json:"Error"`
	StartedAt      time.Time                `json:"StartedAt"`
	CompletedAt    time.Time                `json:"CompletedAt"`
	Metadata       map[string]any           `json:"Metadata"`
}

// UsageRecord is received on usage.handle.
// Uses PascalCase JSON tags matching sdk/pluginapi/types.go.
type UsageRecord struct {
	RequestID            string        `json:"RequestID"`
	TraceID              string        `json:"TraceID"`
	SelectionID          string        `json:"SelectionID"`
	Provider             string        `json:"Provider"`
	BaseURL              string        `json:"BaseURL"`
	ExecutorType         string        `json:"ExecutorType"`
	Model                string        `json:"Model"`
	Alias                string        `json:"Alias"`
	APIKey               string        `json:"APIKey"`
	IsNativeKey          bool          `json:"IsNativeKey"`
	AccessProvider       string        `json:"AccessProvider"`
	SessionID            string        `json:"SessionID"`
	ParentSessionID      string        `json:"ParentSessionID"`
	AuthID               string        `json:"AuthID"`
	AuthIndex            string        `json:"AuthIndex"`
	AuthType             string        `json:"AuthType"`
	Source               string        `json:"Source"`
	ReasoningEffort      string        `json:"ReasoningEffort"`
	ServiceTier          string        `json:"ServiceTier"`
	EffectiveServiceTier string        `json:"EffectiveServiceTier"`
	ResponseServiceTier  string        `json:"ResponseServiceTier"`
	ResponseModel        string        `json:"ResponseModel"`
	Generate             bool          `json:"Generate"`
	Stream               bool          `json:"Stream"`
	RequestedAt          time.Time     `json:"RequestedAt"`
	Latency              time.Duration `json:"Latency"`
	TTFT                 time.Duration `json:"TTFT"`
	Failed               bool          `json:"Failed"`
	Failure              UsageFailure  `json:"Failure"`
	Detail               UsageDetail   `json:"Detail"`
	ResponseHeaders      http.Header   `json:"ResponseHeaders"`
}

// UsageFailure describes upstream or executor failure information.
type UsageFailure struct {
	StatusCode int    `json:"StatusCode"`
	Body       string `json:"Body"`
}

// UsageDetail contains token accounting counters.
// Uses PascalCase JSON tags matching sdk/pluginapi/types.go.
type UsageDetail struct {
	InputTokens         int64 `json:"InputTokens"`
	OutputTokens        int64 `json:"OutputTokens"`
	ReasoningTokens     int64 `json:"ReasoningTokens"`
	CachedTokens        int64 `json:"CachedTokens"`
	CacheReadTokens     int64 `json:"CacheReadTokens"`
	CacheCreationTokens int64 `json:"CacheCreationTokens"`
	TotalTokens         int64 `json:"TotalTokens"`

	TextInputTokens   *int64 `json:"TextInputTokens,omitempty"`
	ImageInputTokens  *int64 `json:"ImageInputTokens,omitempty"`
	AudioInputTokens  *int64 `json:"AudioInputTokens,omitempty"`
	VideoInputTokens  *int64 `json:"VideoInputTokens,omitempty"`
	FileInputTokens   *int64 `json:"FileInputTokens,omitempty"`
	CachedTextTokens  *int64 `json:"CachedTextTokens,omitempty"`
	CachedImageTokens *int64 `json:"CachedImageTokens,omitempty"`
	CachedAudioTokens *int64 `json:"CachedAudioTokens,omitempty"`
	TextOutputTokens  *int64 `json:"TextOutputTokens,omitempty"`
}

// ManagementRegistrationResponse is returned in response to management.register.
type ManagementRegistrationResponse struct {
	Routes    []ManagementRoute `json:"routes,omitempty"`
	Resources []ResourceRoute   `json:"resources,omitempty"`
}

// ManagementRoute declares an authenticated Management API endpoint.
type ManagementRoute struct {
	Method      string `json:"method,omitempty"`
	Path        string `json:"path,omitempty"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

// ResourceRoute declares an unauthenticated browser-navigable resource.
type ResourceRoute struct {
	Path        string `json:"path,omitempty"`
	Menu        string `json:"menu,omitempty"`
	Description string `json:"description,omitempty"`
}

// ManagementRequest describes an incoming management or resource request.
type ManagementRequest struct {
	Method  string      `json:"Method,omitempty"`
	Path    string      `json:"Path,omitempty"`
	Headers http.Header `json:"Headers,omitempty"`
	Query   url.Values  `json:"Query,omitempty"`
	Body    []byte      `json:"Body,omitempty"`
}

// ManagementResponse is returned in response to management.handle.
type ManagementResponse struct {
	StatusCode int         `json:"StatusCode,omitempty"`
	Headers    http.Header `json:"Headers,omitempty"`
	Body       []byte      `json:"Body,omitempty"`
}
