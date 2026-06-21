package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type blockerConfig struct {
	TargetReasoningTokens int    `yaml:"target_reasoning_tokens"`
	RetryStatus           int    `yaml:"retry_status"`
	ErrorCode             string `yaml:"error_code"`
	ErrorMessage          string `yaml:"error_message"`
	MatchRequests         bool   `yaml:"match_requests"`
	MatchStreamResponses  bool   `yaml:"match_stream_responses"`
}

type rawBlockerConfig struct {
	TargetReasoningTokens *int    `yaml:"target_reasoning_tokens"`
	RetryStatus           *int    `yaml:"retry_status"`
	ErrorCode             *string `yaml:"error_code"`
	ErrorMessage          *string `yaml:"error_message"`
	MatchRequests         *bool   `yaml:"match_requests"`
	MatchStreamResponses  *bool   `yaml:"match_stream_responses"`
}

func defaultBlockerConfig() blockerConfig {
	target := 516
	cfg := blockerConfig{
		TargetReasoningTokens: target,
		RetryStatus:           http.StatusTooManyRequests,
		ErrorCode:             defaultErrorCode(target),
		ErrorMessage:          defaultErrorMessage(target, defaultErrorCode(target)),
		MatchRequests:         true,
		MatchStreamResponses:  true,
	}
	cfg.normalize()
	return cfg
}

func parseBlockerConfig(rawYAML []byte) (blockerConfig, error) {
	var raw rawBlockerConfig
	if len(bytes.TrimSpace(rawYAML)) > 0 {
		if err := yaml.Unmarshal(rawYAML, &raw); err != nil {
			return blockerConfig{}, err
		}
	}

	cfg := defaultBlockerConfig()
	if raw.TargetReasoningTokens != nil {
		cfg.TargetReasoningTokens = *raw.TargetReasoningTokens
	}
	if raw.RetryStatus != nil {
		cfg.RetryStatus = *raw.RetryStatus
	}
	cfg.ErrorCode = ""
	cfg.ErrorMessage = ""
	if raw.ErrorCode != nil {
		cfg.ErrorCode = *raw.ErrorCode
	}
	if raw.ErrorMessage != nil {
		cfg.ErrorMessage = *raw.ErrorMessage
	}
	if raw.MatchRequests != nil {
		cfg.MatchRequests = *raw.MatchRequests
	}
	if raw.MatchStreamResponses != nil {
		cfg.MatchStreamResponses = *raw.MatchStreamResponses
	}
	cfg.normalize()
	return cfg, nil
}

func (c *blockerConfig) normalize() {
	defaults := defaultBlockerConfigNoNormalize(c.TargetReasoningTokens)
	if c.TargetReasoningTokens <= 0 {
		c.TargetReasoningTokens = defaults.TargetReasoningTokens
	}
	if c.RetryStatus < 400 || c.RetryStatus > 599 {
		c.RetryStatus = defaults.RetryStatus
	}
	if strings.TrimSpace(c.ErrorCode) == "" {
		c.ErrorCode = defaultErrorCode(c.TargetReasoningTokens)
	} else {
		c.ErrorCode = strings.TrimSpace(c.ErrorCode)
	}
	if strings.TrimSpace(c.ErrorMessage) == "" {
		c.ErrorMessage = defaultErrorMessage(c.TargetReasoningTokens, c.ErrorCode)
	}
}

func defaultBlockerConfigNoNormalize(target int) blockerConfig {
	if target <= 0 {
		target = 516
	}
	code := defaultErrorCode(target)
	return blockerConfig{
		TargetReasoningTokens: target,
		RetryStatus:           http.StatusTooManyRequests,
		ErrorCode:             code,
		ErrorMessage:          defaultErrorMessage(target, code),
		MatchRequests:         true,
		MatchStreamResponses:  true,
	}
}

func containsReasoningTokens(raw []byte, target int) bool {
	if target <= 0 || !bytes.Contains(raw, []byte("reasoning_tokens")) {
		return false
	}
	for _, payload := range jsonPayloadsFromMaybeSSE(raw) {
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.UseNumber()
		var v any
		if err := decoder.Decode(&v); err != nil {
			continue
		}
		if valueContainsReasoningTokens(v, target) {
			return true
		}
	}
	return false
}

func jsonPayloadsFromMaybeSSE(raw []byte) [][]byte {
	var out [][]byte
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	if json.Valid(trimmed) {
		out = append(out, bytes.Clone(trimmed))
	}

	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) {
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			line = bytes.TrimSpace(line[len("data:"):])
		}
		if len(line) == 0 || bytes.Equal(line, []byte("[DONE]")) || !json.Valid(line) {
			continue
		}
		if !containsPayload(out, line) {
			out = append(out, bytes.Clone(line))
		}
	}
	return out
}

func containsPayload(payloads [][]byte, candidate []byte) bool {
	for _, existing := range payloads {
		if bytes.Equal(existing, candidate) {
			return true
		}
	}
	return false
}

func valueContainsReasoningTokens(v any, target int) bool {
	switch typed := v.(type) {
	case map[string]any:
		for key, value := range typed {
			if key == "reasoning_tokens" && numberEquals(value, target) {
				return true
			}
			if valueContainsReasoningTokens(value, target) {
				return true
			}
		}
	case []any:
		for _, value := range typed {
			if valueContainsReasoningTokens(value, target) {
				return true
			}
		}
	}
	return false
}

func numberEquals(value any, target int) bool {
	switch typed := value.(type) {
	case json.Number:
		i, err := typed.Int64()
		if err == nil {
			return i == int64(target)
		}
		f, err := typed.Float64()
		return err == nil && floatEqualsInt(f, target)
	case float64:
		return floatEqualsInt(typed, target)
	case int:
		return typed == target
	case int64:
		return typed == int64(target)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		return err == nil && parsed == target
	default:
		return false
	}
}

func floatEqualsInt(value float64, target int) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value == float64(target)
}

func buildRetryStreamChunk(cfg blockerConfig) []byte {
	payload := map[string]any{
		"type":    "error",
		"status":  cfg.RetryStatus,
		"code":    retryCode(cfg),
		"message": cfg.ErrorMessage,
		"error": map[string]any{
			"type":    retryErrorType(cfg.RetryStatus),
			"code":    retryCode(cfg),
			"message": cfg.ErrorMessage,
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte(`{"type":"error","status":429,"code":"RETRY_REQUIRED_REASONING_516","message":"[RETRY_REQUIRED_REASONING_516] reasoning_tokens=516; discard this response and resend the request."}`)
	}
	return []byte("event: error\ndata: " + string(raw) + "\n\n")
}

func retryCode(cfg blockerConfig) string {
	code := strings.TrimSpace(cfg.ErrorCode)
	if code == "" {
		return defaultErrorCode(cfg.TargetReasoningTokens)
	}
	return code
}

func defaultErrorCode(target int) string {
	return fmt.Sprintf("RETRY_REQUIRED_REASONING_%d", target)
}

func defaultErrorMessage(target int, code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		code = defaultErrorCode(target)
	}
	return fmt.Sprintf("[%s] reasoning_tokens=%d; discard this response and resend the request.", code, target)
}

func retryErrorType(status int) string {
	switch status {
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusRequestTimeout:
		return "request_timeout"
	default:
		if status >= 500 {
			return "server_error"
		}
		return "invalid_request_error"
	}
}
