package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"cpa-cache-tracker/abi"
	"cpa-cache-tracker/storage"
	"cpa-cache-tracker/tracker"
	"cpa-cache-tracker/ui"
)

// Dispatcher coordinates CPA lifecycle, correlator, storage, and UI management.
type Dispatcher struct {
	mu         sync.RWMutex
	cfg        abi.PluginConfig
	dataDir    string
	store      *storage.Store
	correlator *tracker.Correlator
	uiHandler  *ui.Handler
	nowFn      func() time.Time
}

// Global default dispatcher for C ABI exports.
var defaultDispatcher = NewDispatcher()

// NewDispatcher creates a new uninitialized Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		cfg: abi.DefaultPluginConfig(),
	}
}

// SetNowFn overrides the time source (useful for deterministic tests).
func (d *Dispatcher) SetNowFn(fn func() time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nowFn = fn
}

func (d *Dispatcher) now() time.Time {
	if d.nowFn != nil {
		return d.nowFn()
	}
	return time.Now()
}

// Store returns the current underlying Store (nil if uninitialized or closed).
func (d *Dispatcher) Store() *storage.Store {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.store
}

// DispatchWithRecover executes dispatcher.HandleCall with a panic recovery guard,
// returning an error envelope if a panic occurs. Pure Go for unit testing.
func DispatchWithRecover(d *Dispatcher, method string, request []byte) (resp []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic in %s: %v", method, r)
			resp = abi.ErrorEnvelope("plugin_panic", err.Error())
		}
	}()
	if d == nil {
		return abi.ErrorEnvelope("dispatcher_nil", "dispatcher is nil"), nil
	}
	return d.HandleCall(method, request)
}

// HandleCall dispatches an incoming CPA RPC method invocation across the Go ABI.
func (d *Dispatcher) HandleCall(method string, reqBytes []byte) ([]byte, error) {
	switch method {
	case abi.MethodPluginRegister:
		return d.handleRegister(reqBytes)

	case abi.MethodPluginReconfigure:
		return d.handleReconfigure(reqBytes)

	case abi.MethodPluginQuiesce:
		return abi.OKEnvelope(struct{}{})

	case abi.MethodPluginShutdown:
		return d.handleShutdown()

	case abi.MethodRequestInterceptBefore:
		return abi.OKEnvelope(abi.RequestInterceptResponse{})

	case abi.MethodRequestInterceptAfter:
		return d.handleInterceptAfter(reqBytes)

	case abi.MethodUsageHandle:
		return d.handleUsage(reqBytes)

	case abi.MethodRequestComplete:
		return d.handleComplete(reqBytes)

	case abi.MethodManagementRegister:
		return d.handleManagementRegister()

	case abi.MethodManagementHandle:
		return d.handleManagementHandle(reqBytes)

	default:
		return abi.ErrorEnvelope("unsupported_method", "unsupported method: "+method), nil
	}
}

func (d *Dispatcher) handleRegister(reqBytes []byte) ([]byte, error) {
	var req abi.LifecycleRequest
	if len(reqBytes) > 0 {
		if err := json.Unmarshal(reqBytes, &req); err != nil {
			return abi.ErrorEnvelope("invalid_request", "failed to parse lifecycle request: "+err.Error()), nil
		}
	}

	cfg := abi.ParseConfigYAML(req.ConfigYAML)
	resolvedDir := resolveDataDir(cfg.DataDir)

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.store != nil {
		_ = d.store.Close()
		d.store = nil
		d.uiHandler = nil
	}

	store, err := storage.Open(resolvedDir, cfg, d.now)
	if err != nil {
		return abi.ErrorEnvelope("storage_error", "failed to open storage: "+err.Error()), nil
	}

	d.store = store
	d.dataDir = resolvedDir
	d.cfg = cfg
	d.correlator = tracker.NewCorrelator(4096, 5*time.Minute, 15*time.Second)
	d.uiHandler = ui.NewHandler(d.store, func() (abi.PluginConfig, bool) {
		d.mu.RLock()
		defer d.mu.RUnlock()
		return d.cfg, true
	})

	reg := d.buildRegistrationLocked()
	return abi.OKEnvelope(reg)
}

func (d *Dispatcher) handleReconfigure(reqBytes []byte) ([]byte, error) {
	var req abi.LifecycleRequest
	if len(reqBytes) > 0 {
		if err := json.Unmarshal(reqBytes, &req); err != nil {
			return abi.ErrorEnvelope("invalid_request", "failed to parse lifecycle request: "+err.Error()), nil
		}
	}

	cfg := abi.ParseConfigYAML(req.ConfigYAML)
	resolvedDir := resolveDataDir(cfg.DataDir)

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.store == nil {
		store, err := storage.Open(resolvedDir, cfg, d.now)
		if err != nil {
			return abi.ErrorEnvelope("storage_error", "failed to open storage: "+err.Error()), nil
		}
		d.store = store
		d.dataDir = resolvedDir
		d.correlator = tracker.NewCorrelator(4096, 5*time.Minute, 15*time.Second)
		d.uiHandler = ui.NewHandler(d.store, func() (abi.PluginConfig, bool) {
			d.mu.RLock()
			defer d.mu.RUnlock()
			return d.cfg, true
		})
	} else if d.dataDir != resolvedDir {
		_ = d.store.Close()
		d.store = nil
		d.uiHandler = nil
		store, err := storage.Open(resolvedDir, cfg, d.now)
		if err != nil {
			return abi.ErrorEnvelope("storage_error", "failed to reopen storage: "+err.Error()), nil
		}
		d.store = store
		d.dataDir = resolvedDir
		d.uiHandler = ui.NewHandler(d.store, func() (abi.PluginConfig, bool) {
			d.mu.RLock()
			defer d.mu.RUnlock()
			return d.cfg, true
		})
	} else {
		d.store.UpdateConfig(cfg)
	}

	d.cfg = cfg
	reg := d.buildRegistrationLocked()
	return abi.OKEnvelope(reg)
}

func (d *Dispatcher) handleShutdown() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.store != nil {
		_ = d.store.Close()
		d.store = nil
	}
	d.correlator = nil
	d.uiHandler = nil
	return abi.OKEnvelope(struct{}{})
}

func (d *Dispatcher) handleInterceptAfter(reqBytes []byte) ([]byte, error) {
	var req abi.RequestInterceptRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		return abi.ErrorEnvelope("invalid_request", "failed to parse intercept request: "+err.Error()), nil
	}

	d.mu.RLock()
	correlator := d.correlator
	traceEnabled := d.cfg.TraceEnabled
	maxMsgHashes := d.cfg.MaxMessageHashes
	d.mu.RUnlock()

	if correlator != nil {
		correlator.RecordInterceptAfter(req, traceEnabled, maxMsgHashes, d.now())
	}
	return abi.OKEnvelope(abi.RequestInterceptResponse{})
}

func (d *Dispatcher) handleUsage(reqBytes []byte) ([]byte, error) {
	var u abi.UsageRecord
	if err := json.Unmarshal(reqBytes, &u); err != nil {
		return abi.ErrorEnvelope("invalid_request", "failed to parse usage record: "+err.Error()), nil
	}

	d.mu.RLock()
	store := d.store
	correlator := d.correlator
	traceEnabled := d.cfg.TraceEnabled
	d.mu.RUnlock()

	if store == nil {
		return abi.ErrorEnvelope("store_unavailable", "storage store not initialized"), nil
	}

	var att *tracker.PendingAttempt
	var ok bool
	now := d.now()
	if correlator != nil {
		att, ok = correlator.MatchUsage(u, now)
	}

	toFormat := ""
	if ok && att != nil {
		toFormat = att.ToFormat
	}
	tokens := tracker.NormalizeUsage(u, toFormat)

	rec := tracker.Record{
		Timestamp:    now,
		TraceID:      u.TraceID,
		RequestID:    u.RequestID,
		Provider:     u.Provider,
		ExecutorType: u.ExecutorType,
		Model:        u.Model,
		Alias:        u.Alias,
		AuthID:       u.AuthID,
		Stream:       u.Stream,
		Failed:       u.Failed,
		LatencyMs:    u.Latency.Milliseconds(),
		TTFTMs:       u.TTFT.Milliseconds(),
		Tokens:       tokens,
		TraceEnabled: traceEnabled,
	}

	if u.Failed {
		rec.StatusCode = u.Failure.StatusCode
		if rec.StatusCode == 0 {
			rec.StatusCode = 500
		}
	} else {
		rec.StatusCode = 200
	}

	if ok && att != nil {
		if rec.TraceID == "" {
			rec.TraceID = att.TraceID
		}
		if rec.RequestID == "" {
			rec.RequestID = att.HandlerReqID
		}
		if rec.AuthID == "" {
			rec.AuthID = att.AuthID
		}
		if rec.Model == "" {
			rec.Model = att.Model
		}
		rec.SessionID = att.CanonicalSessionID
		rec.ParentSessionID = att.ParentSessionID
		rec.LCPAffinitySessionID = att.LCPAffinitySessionID
		rec.IsFork = att.IsFork
		rec.IsCompaction = att.IsCompaction
		rec.RecordKind = att.RecordKind
		rec.HasFingerprint = att.HasFingerprint
		if att.HasFingerprint {
			fpCopy := att.Fingerprint
			rec.Fingerprint = &fpCopy
		}
	} else {
		rec.RecordKind = "main"
	}

	if _, err := store.Append(rec); err != nil {
		return abi.ErrorEnvelope("append_error", "failed to append record: "+err.Error()), nil
	}

	return abi.OKEnvelope(struct{}{})
}

func (d *Dispatcher) handleComplete(reqBytes []byte) ([]byte, error) {
	var comp abi.RequestCompletion
	if err := json.Unmarshal(reqBytes, &comp); err != nil {
		return abi.ErrorEnvelope("invalid_request", "failed to parse completion: "+err.Error()), nil
	}

	d.mu.RLock()
	correlator := d.correlator
	d.mu.RUnlock()

	if correlator != nil {
		correlator.HandleCompletion(comp, d.now())
	}
	return abi.OKEnvelope(struct{}{})
}

func (d *Dispatcher) handleManagementRegister() ([]byte, error) {
	d.mu.RLock()
	uiHandler := d.uiHandler
	d.mu.RUnlock()

	if uiHandler == nil {
		return abi.ErrorEnvelope("ui_unavailable", "management handler not initialized"), nil
	}
	return abi.OKEnvelope(uiHandler.Register())
}

func (d *Dispatcher) handleManagementHandle(reqBytes []byte) ([]byte, error) {
	var req abi.ManagementRequest
	if len(reqBytes) > 0 {
		if err := json.Unmarshal(reqBytes, &req); err != nil {
			return abi.ErrorEnvelope("invalid_request", "failed to parse management request: "+err.Error()), nil
		}
	}

	d.mu.RLock()
	uiHandler := d.uiHandler
	d.mu.RUnlock()

	if uiHandler == nil {
		return abi.ErrorEnvelope("ui_unavailable", "management handler not initialized"), nil
	}

	resp := uiHandler.Handle(req)
	return abi.OKEnvelope(resp)
}

func (d *Dispatcher) buildRegistrationLocked() abi.Registration {
	return abi.Registration{
		SchemaVersion: abi.SchemaVersion,
		Metadata: abi.Metadata{
			Name:             "cpa-cache-tracker",
			Version:          "0.1.0",
			Author:           "Celeste366",
			GitHubRepository: "https://github.com/Celeste366/cpa-cache-tracker",
			Logo:             "",
			ConfigFields: []abi.ConfigField{
				{
					Name:        "trace_enabled",
					Type:        abi.ConfigFieldTypeBoolean,
					Default:     true,
					Description: "Enables content-free payload fingerprinting and same-session cache miss diagnosis.",
				},
				{
					Name:        "retention_days",
					Type:        abi.ConfigFieldTypeInteger,
					Default:     14,
					Description: "Retention period in days for daily usage JSONL files (0 to disable).",
				},
				{
					Name:        "max_disk_mb",
					Type:        abi.ConfigFieldTypeInteger,
					Default:     200,
					Description: "Maximum disk storage in megabytes for usage logs before auto-pruning.",
				},
				{
					Name:        "max_message_hashes",
					Type:        abi.ConfigFieldTypeInteger,
					Default:     512,
					Description: "Maximum message hashes extracted per request before truncation.",
				},
				{
					Name:        "data_dir",
					Type:        abi.ConfigFieldTypeString,
					Default:     ".cache_tracker_data",
					Description: "Directory path for storing daily JSONL usage records.",
				},
			},
		},
		Capabilities: abi.Capabilities{
			UsagePlugin:                      true,
			RequestLifecyclePlugin:           true,
			RequestInterceptor:               true,
			RequestInterceptorOmitBodyBefore: true,
			RequestInterceptorOmitBodyAfter:  !d.cfg.TraceEnabled,
			ManagementAPI:                    true,
		},
	}
}

func resolveDataDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = ".cache_tracker_data"
	}
	if strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, dir[2:])
		}
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	cwd, err := os.Getwd()
	if err == nil && (cwd == "/" || cwd == "") {
		if home, errHome := os.UserHomeDir(); errHome == nil {
			return filepath.Join(home, ".cli-proxy-api", dir)
		}
	}
	return filepath.Clean(dir)
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) (rc C.int) {
	defer func() {
		if r := recover(); r != nil {
			rc = 1
		}
	}()
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(abi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) (rc C.int) {
	methodStr := "<nil>"
	if method != nil {
		methodStr = C.GoString(method)
	}

	defer func() {
		if r := recover(); r != nil {
			rc = 1
			writeResponse(response, abi.ErrorEnvelope("plugin_panic", fmt.Sprintf("panic in %s: %v", methodStr, r)))
		}
	}()

	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, abi.ErrorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := DispatchWithRecover(defaultDispatcher, methodStr, requestBytes)
	if errHandle != nil {
		writeResponse(response, abi.ErrorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	defer func() {
		_ = recover()
	}()
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	defer func() {
		_ = recover()
	}()
	_, _ = DispatchWithRecover(defaultDispatcher, abi.MethodPluginShutdown, nil)
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	defer func() {
		_ = recover()
	}()
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
