package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
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

extern int blockerPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void blockerPluginFree(void*, size_t);
extern void blockerPluginShutdown(void);

static int blocker_call_host(cliproxy_host_api* api, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (api == NULL || api->call == NULL) {
		return 1;
	}
	return api->call(api->host_ctx, method, request, request_len, response);
}

static void blocker_free_host_buffer(cliproxy_host_api* api, void* ptr, size_t len) {
	if (api != NULL && api->free_buffer != NULL && ptr != NULL) {
		api->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginName    = "cpa-516-blocker"
	pluginVersion = "0.1.0"
)

var state = struct {
	sync.RWMutex
	cfg  blockerConfig
	host *C.cliproxy_host_api
}{
	cfg: defaultBlockerConfig(),
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  capabilities       `json:"capabilities"`
}

type capabilities struct {
	ModelRouter            bool                         `json:"model_router"`
	Executor               bool                         `json:"executor"`
	ExecutorModelScope     pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats   []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats  []string                     `json:"executor_output_formats,omitempty"`
	StreamChunkInterceptor bool                         `json:"response_stream_interceptor"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type hostLogRequest struct {
	Level   string         `json:"level,omitempty"`
	Message string         `json:"message,omitempty"`
	Fields  map[string]any `json:"fields,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	state.Lock()
	state.host = host
	state.Unlock()

	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.blockerPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.blockerPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.blockerPluginShutdown)
	return 0
}

//export blockerPluginCall
func blockerPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required", false, 0))
		return 0
	}

	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}

	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error(), false, 0))
		return 0
	}
	writeResponse(response, raw)
	return 0
}

//export blockerPluginFree
func blockerPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export blockerPluginShutdown
func blockerPluginShutdown() {
	state.Lock()
	state.host = nil
	state.Unlock()
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return handleRegister(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: pluginName})
	case pluginabi.MethodModelRoute:
		return handleModelRoute(request)
	case pluginabi.MethodExecutorExecute, pluginabi.MethodExecutorExecuteStream:
		cfg := currentConfig()
		return retryErrorEnvelope(cfg), nil
	case pluginabi.MethodExecutorCountTokens:
		return okEnvelope(pluginapi.ExecutorResponse{
			Payload: []byte(`{"total_tokens":0}`),
			Headers: http.Header{"Content-Type": []string{"application/json"}},
		})
	case pluginabi.MethodExecutorHTTPRequest:
		cfg := currentConfig()
		return retryErrorEnvelope(cfg), nil
	case pluginabi.MethodResponseInterceptStreamChunk:
		return handleStreamChunk(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, false, 0), nil
	}
}

func handleRegister(request []byte) ([]byte, error) {
	var req lifecycleRequest
	if len(request) > 0 {
		if err := json.Unmarshal(request, &req); err != nil {
			return nil, err
		}
	}

	cfg, errConfig := parseBlockerConfig(req.ConfigYAML)
	if errConfig != nil {
		return nil, fmt.Errorf("parse plugin config: %w", errConfig)
	}

	state.Lock()
	state.cfg = cfg
	state.Unlock()

	return okEnvelope(registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "Esing",
			GitHubRepository: "https://github.com/Zxis233/cpa-516-blocker",
			// Logo:             "https://raw.githubusercontent.com/router-for-me/CLIProxyAPI/main/docs/logo.png",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "target_reasoning_tokens", Type: pluginapi.ConfigFieldTypeInteger, Description: "Reasoning token count that should trigger a retry."},
				{Name: "retry_status", Type: pluginapi.ConfigFieldTypeInteger, Description: "HTTP status used for synthetic retry errors, typically 429 or 503."},
				{Name: "error_code", Type: pluginapi.ConfigFieldTypeString, Description: "Error code returned to clients, also used in the default error message."},
				{Name: "error_message", Type: pluginapi.ConfigFieldTypeString, Description: "Message returned to the client when a match is blocked."},
				{Name: "match_requests", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Route matching requests to this plugin executor and fail them before upstream execution."},
				{Name: "match_stream_responses", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Rewrite matching streaming response chunks into retryable error events."},
			},
		},
		Capabilities: capabilities{
			ModelRouter:            true,
			Executor:               true,
			ExecutorModelScope:     pluginapi.ExecutorModelScopeBoth,
			ExecutorInputFormats:   []string{"openai-response", "chat-completions"},
			ExecutorOutputFormats:  []string{"openai-response", "chat-completions"},
			StreamChunkInterceptor: true,
		},
	})
}

func handleModelRoute(request []byte) ([]byte, error) {
	var req pluginapi.ModelRouteRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	cfg := currentConfig()
	if cfg.MatchRequests && containsReasoningTokens(req.Body, cfg.TargetReasoningTokens) {
		logIntercept("request_route", cfg, map[string]any{
			"source_format":   req.SourceFormat,
			"requested_model": req.RequestedModel,
			"model":           req.RequestedModel,
			"stream":          req.Stream,
			"body_bytes":      len(req.Body),
			"route_target":    "self",
		})
		return okEnvelope(pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetSelf,
			Reason:     fmt.Sprintf("blocked request with reasoning_tokens=%d", cfg.TargetReasoningTokens),
		})
	}
	return okEnvelope(pluginapi.ModelRouteResponse{})
}

func handleStreamChunk(request []byte) ([]byte, error) {
	var req pluginapi.StreamChunkInterceptRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	cfg := currentConfig()
	resp := pluginapi.StreamChunkInterceptResponse{
		Headers: cloneHeader(req.ResponseHeaders),
	}
	if len(req.Body) > 0 {
		resp.Body = append([]byte(nil), req.Body...)
	}
	if cfg.MatchStreamResponses && containsReasoningTokens(req.Body, cfg.TargetReasoningTokens) {
		logIntercept("response_stream", cfg, map[string]any{
			"source_format":   req.SourceFormat,
			"requested_model": req.RequestedModel,
			"model":           req.Model,
			"chunk_index":     req.ChunkIndex,
			"history_chunks":  len(req.HistoryChunks),
			"body_bytes":      len(req.Body),
		})
		resp.Headers.Set("Content-Type", "text/event-stream")
		resp.Body = buildRetryStreamChunk(cfg)
	}
	return okEnvelope(resp)
}

func currentConfig() blockerConfig {
	state.RLock()
	defer state.RUnlock()
	return state.cfg
}

func logIntercept(stage string, cfg blockerConfig, fields map[string]any) {
	if fields == nil {
		fields = make(map[string]any)
	} else {
		fields = cloneAnyMap(fields)
	}
	fields["plugin_id"] = pluginName
	fields["stage"] = stage
	fields["target_reasoning_tokens"] = cfg.TargetReasoningTokens
	fields["retry_status"] = cfg.RetryStatus
	fields["error_code"] = retryCode(cfg)

	_ = writeHostLog(hostLogRequest{
		Level:   "info",
		Message: fmt.Sprintf("%s intercepted reasoning_tokens=%d", pluginName, cfg.TargetReasoningTokens),
		Fields:  fields,
	})
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func writeHostLog(req hostLogRequest) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	rawResp, err := callHost(pluginabi.MethodHostLog, raw)
	if err != nil {
		return err
	}
	if len(rawResp) == 0 {
		return nil
	}
	var resp envelope
	if err := json.Unmarshal(rawResp, &resp); err != nil {
		return err
	}
	if resp.OK {
		return nil
	}
	if resp.Error != nil {
		return fmt.Errorf("host log failed: %s", resp.Error.Message)
	}
	return fmt.Errorf("host log failed")
}

func callHost(method string, payload []byte) ([]byte, error) {
	state.RLock()
	host := state.host
	state.RUnlock()
	if host == nil {
		return nil, fmt.Errorf("host callback is unavailable")
	}

	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var cPayload unsafe.Pointer
	if len(payload) > 0 {
		cPayload = C.CBytes(payload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback payload")
		}
		defer C.free(cPayload)
	}

	var response C.cliproxy_buffer
	rc := C.blocker_call_host(
		host,
		cMethod,
		(*C.uint8_t)(cPayload),
		C.size_t(len(payload)),
		&response,
	)

	var out []byte
	if response.ptr != nil && response.len > 0 {
		out = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.blocker_free_host_buffer(host, response.ptr, response.len)
	}
	if rc != 0 {
		return nil, fmt.Errorf("host callback %s returned %d: %s", method, int(rc), string(out))
	}
	return out, nil
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func retryErrorEnvelope(cfg blockerConfig) []byte {
	return errorEnvelope(retryCode(cfg), cfg.ErrorMessage, true, cfg.RetryStatus)
}

func errorEnvelope(code, message string, retryable bool, status int) []byte {
	raw, _ := json.Marshal(envelope{
		OK: false,
		Error: &envelopeError{
			Code:       code,
			Message:    message,
			Retryable:  retryable,
			HTTPStatus: status,
		},
	})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
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

func cloneHeader(in http.Header) http.Header {
	if in == nil {
		return http.Header{}
	}
	return in.Clone()
}
