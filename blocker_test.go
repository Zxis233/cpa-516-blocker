package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestContainsReasoningTokensInNestedJSON(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"usage":{"output_tokens_details":{"reasoning_tokens":516}}}}`)
	if !containsReasoningTokens(raw, 516) {
		t.Fatal("expected nested reasoning_tokens=516 to match")
	}
	if containsReasoningTokens(raw, 515) {
		t.Fatal("did not expect reasoning_tokens=515 to match")
	}
}

func TestContainsReasoningTokensInSSEData(t *testing.T) {
	raw := []byte("event: response.completed\ndata: {\"response\":{\"usage\":{\"reasoning_tokens\":516}}}\n\n")
	if !containsReasoningTokens(raw, 516) {
		t.Fatal("expected SSE data reasoning_tokens=516 to match")
	}
}

func TestParseBlockerConfigDefaultsMessageFromTarget(t *testing.T) {
	cfg, err := parseBlockerConfig([]byte("target_reasoning_tokens: 777\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ErrorCode != "RETRY_REQUIRED_REASONING_777" {
		t.Fatalf("error code = %q", cfg.ErrorCode)
	}
	wantMessage := "[RETRY_REQUIRED_REASONING_777] reasoning_tokens=777; discard this response and resend the request."
	if cfg.ErrorMessage != wantMessage {
		t.Fatalf("error message = %q, want %q", cfg.ErrorMessage, wantMessage)
	}
	if !cfg.MatchRequests || !cfg.MatchStreamResponses {
		t.Fatalf("match defaults not preserved: %+v", cfg)
	}
}

func TestHandleModelRouteRoutesMatchingRequestToSelf(t *testing.T) {
	state.Lock()
	state.cfg = defaultBlockerConfig()
	state.Unlock()

	req := pluginapi.ModelRouteRequest{
		Body: []byte(`{"reasoning_tokens":516,"model":"gpt-5"}`),
	}
	rawReq, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := handleMethod(pluginabi.MethodModelRoute, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !env.OK {
		t.Fatalf("route envelope not ok: %s", raw)
	}
	var resp pluginapi.ModelRouteResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Handled || resp.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("unexpected route response: %+v", resp)
	}
}

func TestHandleStreamChunkRewritesMatchingChunkToError(t *testing.T) {
	state.Lock()
	state.cfg = defaultBlockerConfig()
	state.Unlock()

	req := pluginapi.StreamChunkInterceptRequest{
		Body: []byte("event: response.completed\ndata: {\"response\":{\"usage\":{\"output_tokens_details\":{\"reasoning_tokens\":516}}}}\n\n"),
	}
	rawReq, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := handleMethod(pluginabi.MethodResponseInterceptStreamChunk, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp pluginapi.StreamChunkInterceptResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	body := string(resp.Body)
	if !strings.Contains(body, `"type":"error"`) || !strings.Contains(body, `"status":429`) || !strings.Contains(body, `RETRY_REQUIRED_REASONING_516`) {
		t.Fatalf("expected retryable error chunk, got %q", body)
	}
}

func TestExecutorReturnsRetryableHTTPStatus(t *testing.T) {
	state.Lock()
	state.cfg = defaultBlockerConfig()
	state.Unlock()

	raw, err := handleMethod(pluginabi.MethodExecutorExecuteStream, nil)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.OK || env.Error == nil {
		t.Fatalf("expected error envelope, got %s", raw)
	}
	if env.Error.HTTPStatus != 429 || !env.Error.Retryable {
		t.Fatalf("unexpected executor error: %+v", env.Error)
	}
	if env.Error.Code != "RETRY_REQUIRED_REASONING_516" {
		t.Fatalf("error code = %q", env.Error.Code)
	}
	if !strings.Contains(env.Error.Message, "[RETRY_REQUIRED_REASONING_516]") {
		t.Fatalf("error message = %q", env.Error.Message)
	}
}
