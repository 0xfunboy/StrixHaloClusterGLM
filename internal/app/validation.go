package app

import (
	"errors"
	"fmt"
	"math"
)

// Reject malformed client requests BEFORE dispatching either TP2 rank. A normal
// 400 must never turn into an uncertain paired engine generation.
func validateChat(p map[string]any) error {
	return validateChatWithTools(p, false)
}
func validateChatWithTools(p map[string]any, toolsEnabled bool) error {
	return validateChatWithReasoning(p, toolsEnabled, supportedReasoning)
}
func validateChatWithReasoning(p map[string]any, toolsEnabled bool, reasoningOK func(string) bool) error {
	if p == nil {
		return errors.New("chat body must be an object")
	}
	allowed := map[string]bool{"messages": true, "model": true, "stream": true, "stream_options": true, "max_tokens": true, "max_completion_tokens": true, "thinking_token_budget": true, "context_tokens": true, "profile": true, "reasoning_effort": true, "chat_template_kwargs": true, "temperature": true, "top_p": true, "top_k": true, "min_p": true, "presence_penalty": true, "frequency_penalty": true, "repetition_penalty": true, "seed": true, "n": true, "stop": true}
	if toolsEnabled {
		allowed["tools"] = true
		allowed["tool_choice"] = true
		allowed["parallel_tool_calls"] = true
	}
	for key := range p {
		if !allowed[key] {
			return fmt.Errorf("unsupported chat field %s; refusing a silently ignored or unqualified control (tools/Responses are not supported)", key)
		}
	}
	for _, key := range []string{"model", "profile"} {
		if v, ok := p[key]; ok {
			if _, ok := v.(string); !ok {
				return fmt.Errorf("%s must be string", key)
			}
		}
	}
	messages, ok := p["messages"].([]any)
	if !ok || len(messages) == 0 || len(messages) > 512 {
		return errors.New("messages must be a nonempty array (maximum512)")
	}
	for _, v := range messages {
		m, ok := v.(map[string]any)
		if !ok {
			return errors.New("each message must be an object")
		}
		role := stringValue(m["role"])
		if role != "system" && role != "user" && role != "assistant" && !(toolsEnabled && role == "tool") {
			return errors.New("unsupported message role")
		}
		for key := range m {
			if key != "role" && key != "content" && !(toolsEnabled && (key == "tool_calls" || key == "tool_call_id" || key == "name" || key == "reasoning_content")) {
				return fmt.Errorf("unsupported text message field %s", key)
			}
		}
		if _, ok := m["content"].(string); !ok && !(toolsEnabled && role == "assistant" && m["content"] == nil && m["tool_calls"] != nil) {
			return errors.New("this text engine requires string message content; multimedia/tool-call envelopes are not qualified")
		}
	}
	if v, ok := p["stream"]; ok {
		if _, ok := v.(bool); !ok {
			return errors.New("stream must be boolean")
		}
	}
	for key, bounds := range map[string][2]float64{"max_tokens": {1, maxChatOutput}, "max_completion_tokens": {1, maxChatOutput}, "thinking_token_budget": {0, maxChatOutput - 1}, "context_tokens": {256, safeEngineContext}, "top_k": {-1, 200000}, "n": {1, 1}, "seed": {0, 2147483647}} {
		if v, ok := p[key]; ok {
			n, ok := v.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < bounds[0] || n > bounds[1] {
				return errors.New("invalid integer " + key)
			}
		}
	}
	if p["top_k"] == float64(0) {
		return errors.New("top_k must be -1 or a positive integer")
	}
	for key, bounds := range map[string][2]float64{"temperature": {0, 2}, "top_p": {0, 1}, "min_p": {0, 1}, "presence_penalty": {-2, 2}, "frequency_penalty": {-2, 2}, "repetition_penalty": {0.01, 2}} {
		if v, ok := p[key]; ok {
			n, ok := v.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < bounds[0] || n > bounds[1] {
				return errors.New("invalid " + key)
			}
		}
	}
	if p["top_p"] == float64(0) {
		return errors.New("top_p must be greater than0 and at most1")
	}
	if stop, ok := p["stop"]; ok {
		var values []any
		switch v := stop.(type) {
		case string:
			values = []any{v}
		case []any:
			values = v
		case nil:
		default:
			return errors.New("stop must be a string or string array")
		}
		if len(values) > 16 {
			return errors.New("at most16 stop strings")
		}
		for _, v := range values {
			s, ok := v.(string)
			if !ok || s == "" || len(s) > 4096 {
				return errors.New("stop strings must be nonempty, at most4096 bytes")
			}
		}
	}
	if v, ok := p["chat_template_kwargs"]; ok {
		k, ok := v.(map[string]any)
		if !ok {
			return errors.New("chat_template_kwargs must be object")
		}
		if r, ok := k["reasoning_effort"]; ok {
			if !reasoningOK(stringValue(r)) {
				return errors.New("unknown reasoning effort")
			}
		}
	}
	if r, ok := p["reasoning_effort"]; ok && !reasoningOK(stringValue(r)) {
		return errors.New("reasoning_effort is not advertised by this model profile")
	}
	if p["stream_options"] != nil && object(p["stream_options"]) == nil {
		return errors.New("stream_options must be object")
	}
	for key, value := range object(p["stream_options"]) {
		if key != "include_usage" && key != "continuous_usage_stats" {
			return errors.New("unsupported stream_options field")
		}
		if _, ok := value.(bool); !ok {
			return errors.New("stream_options flags must be boolean")
		}
	}
	if p["stream_options"] != nil && p["stream"] != true {
		return errors.New("stream_options requires stream:true")
	}
	if toolsEnabled {
		return validateToolEnvelope(p)
	}
	if p["tools"] != nil || p["functions"] != nil {
		return errors.New("tools/functions envelopes are not qualified; use coding task API")
	}
	return nil
}
