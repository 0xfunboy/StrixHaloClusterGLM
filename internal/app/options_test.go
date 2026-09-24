package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func chatFixture() map[string]any {
	return map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Hello"}}}
}

func TestExplicitChatSettings(t *testing.T) {
	a, _ := newApp(coreConfig(t))
	p := chatFixture()
	p["reasoning_effort"] = "high"
	p["max_completion_tokens"] = float64(24000)
	p["stream"] = true
	if err := validateChat(p); err != nil {
		t.Fatal(err)
	}
	s, err := a.prepareChat(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Reasoning != "high" || s.Output != 24000 || s.Automatic || object(p["chat_template_kwargs"])["reasoning_effort"] != "high" || p["max_completion_tokens"] != nil {
		t.Fatalf("lost explicit controls: %+v %#v", s, p)
	}
	if object(p["stream_options"])["continuous_usage_stats"] != true {
		t.Fatal("real streaming usage not requested")
	}
	for _, extra := range []map[string]any{
		{"reasoning_effort": "medium"}, {"reasoning_effort": "none"},
		{"reasoning_effort": "high", "chat_template_kwargs": map[string]any{"reasoning_effort": "low"}},
		{"chat_template_kwargs": map[string]any{"enable_thinking": false}},
		{"max_tokens": 512., "max_completion_tokens": 1024.},
		{"thinking_token_budget": 128.},
	} {
		p := chatFixture()
		for k, v := range extra {
			p[k] = v
		}
		if _, e := a.prepareChat(p); e == nil {
			t.Errorf("silently accepted %#v", extra)
		}
	}
}

func TestRuntimeTokenAdmission(t *testing.T) {
	var dispatched atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tokenize" {
			var p map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			if p["add_generation_prompt"] != true {
				t.Error("template mismatch")
			}
			jsonReply(w, 200, map[string]any{"count": 300, "max_model_len": 65664, "tokens": make([]int, 300)})
			return
		}
		dispatched.Add(1)
		var p map[string]any
		json.NewDecoder(r.Body).Decode(&p)
		if p["max_tokens"] != float64(212) || p["reasoning_effort"] != "high" {
			t.Errorf("forwarded settings %#v", p)
		}
		jsonReply(w, 200, map[string]any{"choices": []any{}})
	}))
	defer backend.Close()
	cfg := coreConfig(t)
	cfg.Backend = backend.URL
	cfg.TokenizerEndpoint = backend.URL + "/tokenize"
	a, _ := newApp(cfg)
	for _, tt := range []struct {
		extra  string
		status int
	}{{`,"reasoning_effort":"high"`, 200}, {`,"max_tokens":213`, 413}} {
		request := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}],"context_tokens":512`+tt.extra+`}`))
		request.Header.Set("Authorization", "Bearer "+a.token)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		a.proxyChat(response, request)
		if response.Code != tt.status {
			t.Fatalf("HTTP%d %s", response.Code, response.Body.String())
		}
		if response.Code == 200 && (response.Header().Get("X-StrixGLM-Prompt-Tokens") != "300" || response.Header().Get("X-StrixGLM-Max-Tokens") != "212") {
			t.Fatal("missing actual admission headers")
		}
	}
	if dispatched.Load() != 1 {
		t.Fatal("overflow was dispatched to target")
	}
}

func TestTokenizerFailureNeverDispatches(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Redirect(w, r, "http://example.invalid/private", 302)
	}))
	defer server.Close()
	cfg := coreConfig(t)
	cfg.TokenizerEndpoint = server.URL + "/tokenize"
	a, _ := newApp(cfg)
	if _, _, e := a.countPrompt(context.Background(), chatFixture()); e == nil {
		t.Fatal("redirect accepted")
	}
	a.cfg.TokenizerEndpoint = "http://example.invalid/tokenize"
	if _, _, e := a.countPrompt(context.Background(), chatFixture()); e == nil {
		t.Fatal("external endpoint accepted")
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected network calls")
	}
}

func TestThinkingBudgetExplicitOptIn(t *testing.T) {
	cfg := coreConfig(t)
	cfg.ThinkingBudget = true
	a, _ := newApp(cfg)
	p := chatFixture()
	p["thinking_token_budget"] = 0.
	s, e := a.prepareChat(p)
	if e != nil || p["thinking_token_budget"] != 0 {
		t.Fatalf("zero budget failed %v", e)
	}
	if e = a.admitChat(context.Background(), p, &s); e != nil {
		t.Fatal(e)
	}
	zero := 0
	pr, e := a.resolveProfile(TaskSpec{ReasoningEffort: "max", MaxTokens: 24000, ThinkingTokenBudget: &zero})
	if e != nil || pr.Reasoning != "max" || pr.MaxTokens != 24000 {
		t.Fatalf("coding controls: %+v %v", pr, e)
	}
}

func TestOptionsReflectPreferenceAndConfiguredMax(t *testing.T) {
	cfg := coreConfig(t)
	cfg.ChatMaxOutput = 4096
	cfg.Profiles["high"] = Profile{Reasoning: "high", ContextTokens: 4096, MaxTokens: 4096, MaxRepairs: 2}
	a, _ := newApp(cfg)
	a.preferred.Store("high")
	if a.defaultOutputTokens() != 4096 {
		t.Fatal("automatic output exceeds configured max")
	}
	r := httptest.NewRequest("GET", "/v1/options", nil)
	r.Header.Set("Authorization", "Bearer "+a.token)
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, r)
	var p map[string]any
	json.Unmarshal(w.Body.Bytes(), &p)
	if p["default_reasoning"] != "high" {
		t.Fatal("advertised preference differs from actual default")
	}
	p = chatFixture()
	p["stream_options"] = map[string]any{"include_usage": true}
	if validateChat(p) == nil {
		t.Fatal("nonstream options reach backend")
	}
}

func TestStreamingProgressUsesReportedCounts(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"many many words\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3},\"choices\":[]}\n\ndata: [DONE]\n\n"
	var counts []int
	var result ModelResult
	if e := consumeSSEProgress(strings.NewReader(stream), time.Now(), nil, &result, func(m Metrics) { counts = append(counts, m.CompletionTokens) }); e != nil {
		t.Fatal(e)
	}
	if len(counts) != 2 || counts[0] != 0 || counts[1] != 3 {
		t.Fatalf("invented token count: %v", counts)
	}
}

func TestDS41ReasoningNoneIsDeploymentScoped(t *testing.T) {
	glm, _ := newApp(coreConfig(t))
	p := chatFixture()
	p["reasoning_effort"] = "none"
	if _, err := glm.prepareChat(p); err == nil {
		t.Fatal("default GLM-compatible config accepted none")
	}

	cfg := coreConfig(t)
	cfg.ReasoningModes = []string{"none", "low", "high", "max"}
	ds, _ := newApp(cfg)
	p = chatFixture()
	p["reasoning_effort"] = "none"
	if err := validateChatWithReasoning(p, false, ds.supportsReasoning); err != nil {
		t.Fatal(err)
	}
	s, err := ds.prepareChat(p)
	if err != nil {
		t.Fatal(err)
	}
	if s.Reasoning != "none" || object(p["chat_template_kwargs"])["reasoning_effort"] != "none" {
		t.Fatalf("none not preserved: %+v %#v", s, p)
	}
}
