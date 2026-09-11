package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

type Metrics struct {
	PromptTokens     int            `json:"prompt_tokens"`
	CompletionTokens int            `json:"completion_tokens"`
	ReasoningTokens  int            `json:"reasoning_tokens"`
	FinalTokens      int            `json:"final_tokens"`
	HTTPSeconds      float64        `json:"http_seconds"`
	TTFTMS           *float64       `json:"ttft_ms"`
	ServerTTFTMS     *float64       `json:"server_ttft_ms"`
	DecodeTPS        *float64       `json:"decode_tps"`
	Acceptance       *float64       `json:"acceptance"`
	PromptTiming     *PromptTiming  `json:"prompt_timing,omitempty"`
	Raw              map[string]any `json:"raw,omitempty"`
}
type ModelResult struct {
	Requested      bool    `json:"requested"`
	Content        string  `json:"content"`
	Reasoning      string  `json:"reasoning,omitempty"`
	FinishReason   string  `json:"finish_reason"`
	Metrics        Metrics `json:"metrics"`
	StreamComplete bool    `json:"stream_complete"`
}

func number(v any) float64 {
	if x, ok := v.(float64); ok {
		return x
	}
	return 0
}
func object(v any) map[string]any { x, _ := v.(map[string]any); return x }
func stringValue(v any) string    { x, _ := v.(string); return x }
func parseMetrics(m *Metrics, p map[string]any) {
	if u := object(p["usage"]); u != nil {
		m.PromptTokens = int(number(u["prompt_tokens"]))
		m.CompletionTokens = int(number(u["completion_tokens"]))
		d := object(u["completion_tokens_details"])
		m.ReasoningTokens = int(number(d["reasoning_tokens"]))
		m.FinalTokens = m.CompletionTokens - m.ReasoningTokens
	}
	if v := object(p["metrics"]); v != nil {
		m.Raw = v
		if n, ok := v["time_to_first_token_ms"].(float64); ok {
			m.ServerTTFTMS = &n
		}
		if g := number(v["generation_time_ms"]); g > 0 && m.CompletionTokens > 1 {
			r := float64(m.CompletionTokens-1) * 1000 / g
			m.DecodeTPS = &r
		}
		spec := object(v["speculative_decoding"])
		for _, k := range []string{"draft_acceptance_rate", "acceptance_rate", "acceptance"} {
			if r, ok := spec[k].(float64); ok {
				m.Acceptance = &r
			}
		}
	}
}
func consumeSSE(r io.Reader, start time.Time, emit func([]byte), result *ModelResult) error {
	return consumeSSEProgress(r, start, emit, result, nil)
}
func consumeSSEProgress(r io.Reader, start time.Time, emit func([]byte), result *ModelResult, progress func(Metrics)) error {
	scanner := bufio.NewScanner(io.LimitReader(r, 64<<20))
	scanner.Buffer(make([]byte, 65536), 2<<20)
	var data []string
	event := func() error {
		if len(data) == 0 {
			return nil
		}
		joined := strings.Join(data, "\n")
		data = nil
		if joined == "[DONE]" {
			result.StreamComplete = true
			return nil
		}
		var p map[string]any
		if e := json.Unmarshal([]byte(joined), &p); e != nil {
			return e
		}
		if p["error"] != nil {
			return fmt.Errorf("backend SSE error: %v", p["error"])
		}
		parseMetrics(&result.Metrics, p)
		choices, _ := p["choices"].([]any)
		for _, v := range choices {
			c := object(v)
			d := object(c["delta"])
			content := stringValue(d["content"])
			reasoning := stringValue(d["reasoning"])
			if reasoning == "" {
				reasoning = stringValue(d["reasoning_content"])
			}
			if content != "" || reasoning != "" {
				if result.Metrics.TTFTMS == nil {
					t := float64(time.Since(start).Microseconds()) / 1000
					result.Metrics.TTFTMS = &t
				}
			}
			result.Content += content
			result.Reasoning += reasoning
			if f := stringValue(c["finish_reason"]); f != "" {
				result.FinishReason = f
			}
		}
		result.Metrics.HTTPSeconds = time.Since(start).Seconds()
		if progress != nil {
			progress(result.Metrics)
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if emit != nil {
			emit(append(append([]byte(nil), scanner.Bytes()...), '\n'))
		}
		if line == "" {
			if e := event(); e != nil {
				return e
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if e := scanner.Err(); e != nil {
		return e
	}
	if e := event(); e != nil {
		return e
	}
	if !result.StreamComplete {
		return errors.New("missing SSE DONE")
	}
	return nil
}
func (a *App) modelLock(ctx context.Context) (func(), error) {
	if a.lifecycle != nil && !a.lifecycle.ready() {
		return nil, errors.New("GLM inference is not READY; use the model lifecycle control")
	}
	select {
	case a.admission <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	f, e := os.OpenFile(a.cfg.PairLock, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		<-a.admission
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		<-a.admission
		return nil, errors.New("legacy coder/controller busy")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close(); <-a.admission }, nil
}
func (a *App) generate(ctx context.Context, payload map[string]any, rawPath string) (ModelResult, error) {
	return a.generateProgress(ctx, payload, rawPath, nil)
}
func (a *App) generateProgress(ctx context.Context, payload map[string]any, rawPath string, progress func(Metrics)) (ModelResult, error) {
	start := time.Now()
	var r ModelResult
	release, e := a.modelLock(ctx)
	if e != nil {
		return r, e
	}
	defer release()
	if ctx.Err() != nil {
		return r, ctx.Err()
	}
	timeout := a.cfg.ModelTimeout
	if n, ok := payload["_timeout"].(int); ok {
		timeout = n
	}
	delete(payload, "_timeout")
	if a.cfg.TokenizerEndpoint != "" {
		prompt, limit, err := a.countPrompt(ctx, payload)
		if err != nil {
			return r, fmt.Errorf("tokenizer preflight (no inference): %w", err)
		}
		if limit > safeEngineContext {
			limit = safeEngineContext
		}
		out, err := wholeNumber(payload["max_tokens"], "max_tokens", 1, a.maxOutputTokens())
		if err != nil {
			return r, err
		}
		if prompt+out > limit {
			return r, fmt.Errorf("actual prompt %d + output %d exceeds engine context %d; no inference dispatched", prompt, out, limit)
		}
	}
	payload["stream"] = true
	payload["stream_options"] = map[string]any{"include_usage": true, "continuous_usage_stats": true}
	b, e := json.Marshal(payload)
	if e != nil {
		return r, e
	}
	_ = writeJSON(rawPath+"-request.json", payload)
	a.journal.Log("model_start", map[string]any{"raw": rawPath})
	// Cancellation is cooperative: ALWAYS drain the current paired generation.
	// Do not propagate task cancellation into the TP2 transport mid-collective.
	drainCtx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(drainCtx, "POST", a.cfg.Backend+"/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	r.Requested = true
	resp, e := a.client.Do(req)
	if e != nil {
		return r, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return r, fmt.Errorf("backend HTTP %d: %s", resp.StatusCode, data)
	}
	raw, e := os.OpenFile(rawPath+"-response.sse", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return r, e
	}
	defer raw.Close()
	e = consumeSSEProgress(io.TeeReader(resp.Body, raw), start, nil, &r, progress)
	r.Metrics.HTTPSeconds = time.Since(start).Seconds()
	_ = writeJSON(rawPath+"-response.json", r)
	a.journal.Log("model_end", map[string]any{"raw": rawPath, "metrics": r.Metrics, "error": fmt.Sprint(e)})
	if e != nil {
		return r, e
	}
	return r, nil
}
