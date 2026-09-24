package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const safeEngineContext = 65536 // Runtime is launched at65664; leave128 positions reserved.
const maxChatOutput = 32768
const defaultChatOutput = 16384

var errTokenizerPreflight = errors.New("tokenizer preflight unavailable; no inference dispatched")

func supportedReasoning(s string) bool { return s == "low" || s == "high" || s == "max" }
func (a *App) reasoningModes() []string {
	if len(a.cfg.ReasoningModes) == 0 {
		return []string{"low", "high", "max"}
	}
	return append([]string(nil), a.cfg.ReasoningModes...)
}
func (a *App) supportsReasoning(s string) bool {
	for _, mode := range a.reasoningModes() {
		if s == mode {
			return true
		}
	}
	return false
}
func (a *App) maxOutputTokens() int {
	if a.cfg.ChatMaxOutput > 0 {
		return a.cfg.ChatMaxOutput
	}
	return maxChatOutput
}
func (a *App) chatContextTokens() int {
	if a.cfg.ChatContextTokens > 0 {
		return a.cfg.ChatContextTokens
	}
	return safeEngineContext
}
func (a *App) defaultOutputTokens() int {
	if a.cfg.ChatDefaultOutput > 0 {
		return min(a.cfg.ChatDefaultOutput, a.maxOutputTokens())
	}
	return min(defaultChatOutput, a.maxOutputTokens())
}

func (a *App) registerOptionsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/options", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(w, r) {
			return
		}
		profile, _ := a.resolveProfile(TaskSpec{Profile: a.selectedProfile()})
		note := "Text-only Chat Completions. Function tools are disabled until the paired adapter is qualified; Responses/multimodal are not supported."
		if a.cfg.ToolCalls {
			note = "Text and function tools through a paired raw-token adapter; tool turns are buffered until agreement. Responses/multimodal are not supported."
		}
		jsonReply(w, 200, map[string]any{
			"reasoning_modes": a.reasoningModes(), "default_reasoning": profile.Reasoning,
			"context_options":        []int{4096, 8192, 16384, 32768, 65536},
			"default_context_tokens": a.chatContextTokens(), "engine_context_tokens": safeEngineContext,
			"default_max_tokens": a.defaultOutputTokens(), "max_output_tokens": a.maxOutputTokens(),
			"thinking_budget_supported": a.cfg.ThinkingBudget, "thinking_off_supported": a.supportsReasoning("none"),
			"generation_timeout_seconds": a.cfg.ModelTimeout,
			"context_semantics":          "Input plus output admission window; does not resize the engine KV cache or restart ranks.",
			"output_semantics":           "Maximum total output includes reasoning and final answer. Auto fits the remaining window; EOS may end sooner.",
			"quality_note":               "Compact C++ fixtures qualified. Largest passing context integration:2575 actual prompt tokens. Longer context is available, not quality-qualified.",
			"thinking_note":              "Advertised modes are profile-specific template controls, not quality guarantees. Hiding reasoning does not disable computation. Only this deployment's explicit modes are accepted.",
			"tools_supported":            a.cfg.ToolCalls, "client_note": note,
			"attachment_max_bytes": attachmentMaxFile, "attachment_kinds": []string{"text", "pdf-text", "docx-text", "archive-listing", "binary-inspection"},
		})
	})
}

func wholeNumber(v any, name string, lo, hi int) (int, error) {
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	default:
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < float64(lo) || n > float64(hi) {
		return 0, fmt.Errorf("%s must be in %d..%d", name, lo, hi)
	}
	return int(n), nil
}

type chatSettings struct {
	Reasoning               string
	Context, Output, Prompt int
	Automatic               bool
	CountSource             string
	TokenIDs                []int
}

// Translate explicit client settings to the actual pinned GLM template. Never
// silently map medium/off to max, or discard an explicit client reasoning level.
func (a *App) prepareChat(p map[string]any) (chatSettings, error) {
	s := chatSettings{Context: a.chatContextTokens(), Output: a.defaultOutputTokens(), Automatic: true}
	profileName := stringValue(p["profile"])
	if profileName == "" {
		profileName = a.selectedProfile()
	}
	profile, e := a.resolveProfile(TaskSpec{Profile: profileName})
	if e != nil {
		return s, e
	}
	s.Reasoning = profile.Reasoning
	kwargs := object(p["chat_template_kwargs"])
	if kwargs == nil {
		kwargs = map[string]any{}
	}
	top := stringValue(p["reasoning_effort"])
	nested := stringValue(kwargs["reasoning_effort"])
	if top != "" && nested != "" && top != nested {
		return s, errors.New("conflicting reasoning_effort and chat_template_kwargs.reasoning_effort")
	}
	if nested != "" {
		s.Reasoning = nested
	}
	if top != "" {
		s.Reasoning = top
	}
	if !a.supportsReasoning(s.Reasoning) {
		return s, fmt.Errorf("unsupported reasoning_effort %q for this deployment", s.Reasoning)
	}
	for key, value := range kwargs {
		switch key {
		case "reasoning_effort":
		case "clear_thinking":
			if _, ok := value.(bool); !ok {
				return s, errors.New("clear_thinking must be boolean")
			}
		default:
			return s, fmt.Errorf("unsupported template control %s; refusing a silently ignored setting", key)
		}
	}
	if p["chat_template"] != nil || p["continue_final_message"] != nil || p["add_generation_prompt"] != nil {
		return s, errors.New("custom template/prefill controls are not supported by this gateway")
	}
	if value, ok := p["context_tokens"]; ok {
		s.Context, e = wholeNumber(value, "context_tokens", 256, safeEngineContext)
		if e != nil {
			return s, e
		}
	}
	if legacy, newer := p["max_tokens"], p["max_completion_tokens"]; legacy != nil && newer != nil {
		oldCount, e1 := wholeNumber(legacy, "max_tokens", 1, a.maxOutputTokens())
		newCount, e2 := wholeNumber(newer, "max_completion_tokens", 1, a.maxOutputTokens())
		if e1 != nil || e2 != nil || oldCount != newCount {
			return s, errors.New("conflicting or invalid max_tokens/max_completion_tokens")
		}
	}
	output := p["max_tokens"]
	if output == nil {
		output = p["max_completion_tokens"]
	}
	if output != nil {
		s.Output, e = wholeNumber(output, "max_tokens", 1, a.maxOutputTokens())
		if e != nil {
			return s, e
		}
		s.Automatic = false
	}
	if budget, ok := p["thinking_token_budget"]; ok {
		if !a.cfg.ThinkingBudget {
			return s, errors.New("thinking_token_budget is not verified for the running backend")
		}
		n, e := wholeNumber(budget, "thinking_token_budget", 0, s.Output-1)
		if e != nil {
			return s, e
		}
		p["thinking_token_budget"] = n
	}
	delete(p, "profile")
	delete(p, "context_tokens")
	delete(p, "max_completion_tokens")
	if model := stringValue(p["model"]); model != "" && model != a.cfg.Model {
		return s, errors.New("unknown model")
	}
	p["model"] = a.cfg.Model
	p["reasoning_effort"] = s.Reasoning
	kwargs["reasoning_effort"] = s.Reasoning
	p["chat_template_kwargs"] = kwargs
	if p["seed"] == nil {
		p["seed"] = 1
	}
	if p["temperature"] == nil {
		p["temperature"] = 0
	}
	if stream, _ := p["stream"].(bool); stream {
		opts := object(p["stream_options"])
		if opts == nil {
			opts = map[string]any{}
		}
		opts["include_usage"] = true
		opts["continuous_usage_stats"] = true
		p["stream_options"] = opts
	}
	return s, nil
}

// /tokenize runs only the pinned engine's CPU tokenizer/template renderer. It
// does not enqueue inference, allocate model KV or drive a TP collective. No
// arbitrary rank route/URL comes from a client.
func (a *App) countPrompt(ctx context.Context, p map[string]any) (int, int, error) {
	n, limit, _, e := a.tokenizePrompt(ctx, p)
	return n, limit, e
}
func (a *App) tokenizePrompt(ctx context.Context, p map[string]any) (int, int, []int, error) {
	if a.cfg.TokenizerEndpoint == "" {
		return 0, 0, nil, errors.New("runtime tokenizer endpoint is not configured")
	}
	u, e := url.Parse(a.cfg.TokenizerEndpoint)
	if e != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/tokenize" {
		return 0, 0, nil, errors.New("invalid configured tokenizer endpoint")
	}
	if h := u.Hostname(); h != "10.55.0.1" && h != "10.55.0.2" && h != "127.0.0.1" && h != "localhost" && h != "::1" {
		return 0, 0, nil, errors.New("tokenizer endpoint must be a configured local/private rank")
	}
	data := map[string]any{"model": a.cfg.Model, "messages": p["messages"], "chat_template_kwargs": p["chat_template_kwargs"], "add_generation_prompt": true}
	if p["tools"] != nil {
		data["tools"] = p["tools"]
	}
	b, e := json.Marshal(data)
	if e != nil {
		return 0, 0, nil, e
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "POST", u.String(), bytes.NewReader(b))
	if e != nil {
		return 0, 0, nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	client := *a.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("tokenizer redirect refused") }
	response, e := client.Do(req)
	if e != nil {
		return 0, 0, nil, e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return 0, 0, nil, fmt.Errorf("tokenizer preflight HTTP%d; no inference dispatched", response.StatusCode)
	}
	var result struct {
		Count  int   `json:"count"`
		Max    int   `json:"max_model_len"`
		Tokens []int `json:"tokens"`
	}
	if e = json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&result); e != nil {
		return 0, 0, nil, e
	}
	if result.Count < 1 || result.Count != len(result.Tokens) || result.Max < 1 {
		return 0, 0, nil, errors.New("invalid runtime token count")
	}
	return result.Count, result.Max, result.Tokens, nil
}

func (a *App) admitChat(ctx context.Context, p map[string]any, s *chatSettings) error {
	if a.cfg.TokenizerEndpoint != "" {
		n, limit, tokens, e := a.tokenizePrompt(ctx, p)
		if e != nil {
			return fmt.Errorf("%w: %v", errTokenizerPreflight, e)
		}
		s.Prompt = n
		s.TokenIDs = tokens
		s.CountSource = "runtime tokenizer"
		if s.Context > limit {
			s.Context = limit
		}
	} else {
		// Compatibility for offline fixtures / explicitly unconfigured installations.
		// UTF8 bytes are a conservative text-token ceiling, never a measured count.
		raw, _ := json.Marshal(p["messages"])
		s.Prompt = len(raw) + 256
		s.CountSource = "conservative byte guard; tokenizer unavailable"
	}
	remaining := s.Context - s.Prompt
	if remaining < 1 {
		return fmt.Errorf("prompt %d tokens exceeds request context %d; no inference dispatched", s.Prompt, s.Context)
	}
	if s.Automatic && s.Output > remaining {
		s.Output = remaining
	}
	if s.Output > remaining {
		return fmt.Errorf("prompt %d + output %d exceeds context %d; select a larger window or lower output", s.Prompt, s.Output, s.Context)
	}
	if budget, ok := p["thinking_token_budget"]; ok {
		if n, _ := wholeNumber(budget, "thinking_token_budget", 0, a.maxOutputTokens()); n >= s.Output {
			return errors.New("thinking budget leaves no room for final output in this context")
		}
	}
	p["max_tokens"] = s.Output
	return nil
}

func setChatHeaders(w http.ResponseWriter, s chatSettings) {
	w.Header().Set("X-StrixGLM-Reasoning-Effort", s.Reasoning)
	w.Header().Set("X-StrixGLM-Context-Tokens", strconv.Itoa(s.Context))
	w.Header().Set("X-StrixGLM-Prompt-Tokens", strconv.Itoa(s.Prompt))
	w.Header().Set("X-StrixGLM-Max-Tokens", strconv.Itoa(s.Output))
	w.Header().Set("X-StrixGLM-Token-Count-Source", s.CountSource)
}
