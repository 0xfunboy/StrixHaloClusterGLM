package app

// Native paired OpenAI coordinator. Two HTTP inputs drive ONE TP2 inference;
// both must consume identical bytes, and both outputs must agree semantically.
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type PairedConfig struct {
	RankURLs       [2]string `json:"rank_urls"`
	Model          string    `json:"model"`
	StateDir       string    `json:"state_dir"`
	TimeoutSeconds int       `json:"timeout_seconds"`
	BodyLimit      int64     `json:"body_limit"`
	ResponseLimit  int64     `json:"response_limit"`
}
type PairedBackend struct {
	Config  PairedConfig
	client  *http.Client
	mu      sync.Mutex
	busy    bool
	poison  string
	closing bool
	idle    chan struct{}
}

func NewPairedBackend(cfg PairedConfig) (*PairedBackend, error) {
	if cfg.TimeoutSeconds == 0 {
		cfg.TimeoutSeconds = 600
	}
	if cfg.BodyLimit == 0 {
		cfg.BodyLimit = 2 << 20
	}
	if cfg.ResponseLimit == 0 {
		cfg.ResponseLimit = 16 << 20
	}
	if cfg.Model == "" || !filepath.IsAbs(cfg.StateDir) || filepath.Clean(cfg.StateDir) != cfg.StateDir || cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 1800 || cfg.BodyLimit < 1 || cfg.BodyLimit > 2<<20 || cfg.ResponseLimit < 1 || cfg.ResponseLimit > 16<<20 {
		return nil, errors.New("invalid paired backend configuration")
	}
	for rank, raw := range cfg.RankURLs {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "http" || u.Hostname() != fmt.Sprintf("10.55.0.%d", rank+1) || u.Port() == "" || u.Port() == "18100" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("paired ranks require fixed private USB4 URLs")
		}
	}
	if e := os.MkdirAll(cfg.StateDir, 0700); e != nil {
		return nil, e
	}
	p := &PairedBackend{Config: cfg, client: &http.Client{Transport: &http.Transport{Proxy: nil, MaxIdleConns: 8, MaxIdleConnsPerHost: 4}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	p.idle = make(chan struct{})
	close(p.idle)
	if _, e := os.Lstat(p.marker()); e == nil {
		p.poison = "persistent in-flight/poison marker: whole-pair restart required"
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	return p, nil
}
func (p *PairedBackend) marker() string { return filepath.Join(p.Config.StateDir, "pair-poison.json") }
func (p *PairedBackend) stopAdmission() { p.mu.Lock(); p.closing = true; p.mu.Unlock() }
func (p *PairedBackend) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	p.closing = true
	idle := p.idle
	p.mu.Unlock()
	select {
	case <-idle:
		p.client.CloseIdleConnections()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *PairedBackend) fail(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.poison = reason
	_ = controllerWrite(p.marker(), map[string]any{"reason": reason, "time": time.Now().UTC().Format(time.RFC3339Nano), "pid": os.Getpid()})
}
func pairedJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (p *PairedBackend) Health(ctx context.Context) map[string]any {
	type check struct {
		rank int
		ok   bool
	}
	done := make(chan check, 2)
	for rank, raw := range p.Config.RankURLs {
		go func(rank int, raw string) { done <- check{rank, controllerHealth(ctx, raw)} }(rank, raw)
	}
	ranks := [2]bool{}
	for i := 0; i < 2; i++ {
		v := <-done
		ranks[v.rank] = v.ok
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	state := "ok"
	if p.poison != "" || !ranks[0] || !ranks[1] {
		state = "unhealthy"
	}
	if p.closing {
		state = "stopping"
	}
	return map[string]any{"status": state, "ranks": ranks, "busy": p.busy, "poison": p.poison, "backend": "native-go-tp2", "model": p.Config.Model}
}
func (p *PairedBackend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && r.URL.Path == "/health" {
		v := p.Health(r.Context())
		status := 200
		if v["status"] != "ok" {
			status = 503
		}
		pairedJSON(w, status, v)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/v1/models" {
		pairedJSON(w, 200, map[string]any{"object": "list", "data": []any{map[string]any{"id": p.Config.Model, "object": "model", "owned_by": "local"}}})
		return
	}
	if r.Method != "POST" || (r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/v1/completions") {
		pairedJSON(w, 404, map[string]string{"error": "unsupported route"})
		return
	}
	if r.URL.RawQuery != "" {
		pairedJSON(w, 400, map[string]string{"error": "query parameters are not supported"})
		return
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, p.Config.BodyLimit+1))
	if e != nil || int64(len(b)) > p.Config.BodyLimit {
		pairedJSON(w, 413, map[string]string{"error": "request body limit"})
		return
	}
	payload, e := pairedObject(b)
	if e != nil {
		pairedJSON(w, 400, map[string]string{"error": "invalid JSON object"})
		return
	}
	if model, ok := payload["model"]; ok && model != p.Config.Model {
		pairedJSON(w, 400, map[string]string{"error": "unknown model"})
		return
	}
	payload["model"] = p.Config.Model
	if _, ok := payload["seed"]; !ok {
		v, _ := strconv.ParseInt(newNonce()[:8], 16, 64)
		payload["seed"] = json.Number(strconv.FormatInt(v&0x7fffffff, 10))
	}
	seed, ok := payload["seed"].(json.Number)
	if !ok {
		pairedJSON(w, 400, map[string]string{"error": "seed must be a non-negative int64"})
		return
	}
	n, e := strconv.ParseInt(string(seed), 10, 64)
	if e != nil || n < 0 {
		pairedJSON(w, 400, map[string]string{"error": "seed must be a non-negative int64"})
		return
	}
	stream := false
	if v, ok := payload["stream"]; ok {
		stream, ok = v.(bool)
		if !ok {
			pairedJSON(w, 400, map[string]string{"error": "stream must be boolean"})
			return
		}
	}
	body, e := json.Marshal(payload)
	if e != nil || int64(len(body)) > p.Config.BodyLimit {
		pairedJSON(w, 413, map[string]string{"error": "normalized request exceeds limit"})
		return
	}
	lock, status, e := p.admit()
	if e != nil {
		pairedJSON(w, status, map[string]string{"error": e.Error()})
		return
	}
	job := &pairedJob{backend: p, path: r.URL.Path, body: body, stream: stream, frames: make(chan []byte, 128), done: make(chan struct{}), lock: lock}
	go job.run() // Detached from client cancellation: BOTH ranks must drain.
	if !stream {
		select {
		case <-r.Context().Done():
			job.detach()
			return
		case <-job.done:
		}
		if job.err != nil {
			pairedJSON(w, 502, map[string]string{"error": job.err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(job.body0)
		return
	}
	// Do not send success headers until data exists. DONE/finish frames are held
	// until both streams terminate successfully and their generated outputs agree.
	started := false
	for {
		select {
		case <-r.Context().Done():
			job.detach()
			return
		case frame, ok := <-job.frames:
			if !ok {
				if job.err != nil {
					if !started {
						pairedJSON(w, 502, map[string]string{"error": job.err.Error()})
					} else {
						b, _ := json.Marshal(map[string]string{"error": job.err.Error()})
						_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", b)
					}
				}
				return
			}
			if !started {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.WriteHeader(200)
				started = true
			}
			rc := http.NewResponseController(w)
			_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, e = w.Write(frame); e != nil {
				job.detach()
				return
			}
			if e = rc.Flush(); e != nil {
				job.detach()
				return
			}
		}
	}
}
func (p *PairedBackend) admit() (*os.File, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closing {
		return nil, 503, errors.New("paired backend shutting down; no new admission")
	}
	if p.poison != "" {
		return nil, 503, errors.New(p.poison)
	}
	if p.busy {
		return nil, 409, errors.New("pair busy")
	}
	lock, e := os.OpenFile(filepath.Join(p.Config.StateDir, "pair.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, 503, e
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		return nil, 409, errors.New("pair management/admission locked")
	}
	f, e := os.OpenFile(p.marker(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		lock.Close()
		p.poison = "persistent marker/cannot reserve pair; whole-pair restart required"
		return nil, 503, errors.New(p.poison)
	}
	b, _ := json.Marshal(map[string]any{"reason": "in-flight pair; if orphaned, restart BOTH ranks", "pid": os.Getpid(), "time": time.Now().UTC().Format(time.RFC3339Nano)})
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		d, de := os.Open(p.Config.StateDir)
		if de == nil {
			e = d.Sync()
			d.Close()
		} else {
			e = de
		}
	}
	if e != nil {
		lock.Close()
		p.poison = "durable pair reservation failed"
		return nil, 503, e
	}
	p.busy = true
	p.idle = make(chan struct{})
	return lock, 200, nil
}

type pairedJob struct {
	backend    *PairedBackend
	path       string
	body       []byte
	stream     bool
	frames     chan []byte
	done       chan struct{}
	lock       *os.File
	mu         sync.Mutex
	detached   bool
	clientSlow bool
	err        error
	body0      []byte
}
type pairedRankResult struct {
	rank     int
	body     []byte
	output   any
	terminal []byte
	err      error
}

func (j *pairedJob) detach() { j.mu.Lock(); j.detached = true; j.mu.Unlock() }
func (j *pairedJob) emit(b []byte) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.detached {
		return
	}
	select {
	case j.frames <- b:
	default:
		j.detached = true
		j.clientSlow = true
	}
}
func (j *pairedJob) run() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(j.backend.Config.TimeoutSeconds)*time.Second)
	defer cancel()
	results := make(chan pairedRankResult, 2)
	for rank := 0; rank < 2; rank++ {
		go func(rank int) { results <- j.drain(ctx, rank) }(rank)
	}
	var rs [2]pairedRankResult
	for i := 0; i < 2; i++ {
		select {
		case v := <-results:
			rs[v.rank] = v
			if v.err != nil {
				j.backend.fail(v.err.Error())
			}
		case <-ctx.Done():
			j.err = errors.New("paired generation deadline; whole-pair restart required")
			j.backend.fail(j.err.Error())
			goto finish
		}
	}
	for _, v := range rs {
		if v.err != nil {
			j.err = v.err
			break
		}
	}
	if j.err == nil && !reflect.DeepEqual(rs[0].output, rs[1].output) {
		j.err = errors.New("rank generated outputs differ; whole-pair restart required")
	}
	if j.err != nil {
		j.backend.fail(j.err.Error())
	} else {
		// A shutdown deadline can poison an otherwise draining job. Serialize
		// successful marker removal with fail(): no late completion clears poison.
		j.backend.mu.Lock()
		if j.backend.poison != "" {
			j.err = errors.New(j.backend.poison)
		} else if e := os.Remove(j.backend.marker()); e != nil {
			j.err = fmt.Errorf("cannot clear completed pair marker: %w", e)
		} else {
			d, e := os.Open(j.backend.Config.StateDir)
			if e == nil {
				e = d.Sync()
				d.Close()
			}
			if e != nil {
				j.err = e
			}
		}
		j.backend.mu.Unlock()
		if j.err != nil {
			j.backend.fail(j.err.Error())
		}
		if j.err == nil {
			if !j.stream {
				merged, e := mergePairedTimingMetrics(rs[0].body, rs[1].body)
				if e != nil {
					j.err = e
				} else {
					j.body0 = merged
				}
			} else {
				j.body0 = rs[0].body
			}
			if j.err == nil && len(rs[0].terminal) > 0 {
				terminal := rs[0].terminal
				if j.stream && len(rs[1].terminal) > 0 {
					merged, e := mergePairedSSETerminalMetrics(rs[0].terminal, rs[1].terminal)
					if e != nil {
						j.err = e
					} else {
						terminal = merged
					}
				}
				if j.err == nil {
					j.emit(terminal)
				}
			}
		}
	}
finish:
	j.mu.Lock()
	j.detached = true
	if j.clientSlow && j.err == nil {
		j.err = errors.New("slow client detached; pair drained, response incomplete")
	}
	j.mu.Unlock()
	_ = syscall.Flock(int(j.lock.Fd()), syscall.LOCK_UN)
	_ = j.lock.Close()
	j.backend.mu.Lock()
	j.backend.busy = false
	close(j.backend.idle)
	j.backend.mu.Unlock()
	close(j.frames)
	close(j.done)
}
func (j *pairedJob) drain(ctx context.Context, rank int) (out pairedRankResult) {
	out.rank = rank
	defer func() {
		if out.err != nil {
			out.err = fmt.Errorf("rank%d: %w", rank, out.err)
		}
	}()
	req, e := http.NewRequestWithContext(ctx, "POST", j.backend.Config.RankURLs[rank]+j.path, bytes.NewReader(j.body))
	if e != nil {
		out.err = e
		return
	}
	req.Header.Set("Content-Type", "application/json")
	r, e := j.backend.client.Do(req)
	if e != nil {
		out.err = fmt.Errorf("request: %w", e)
		return
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, j.backend.Config.ResponseLimit))
		out.err = fmt.Errorf("HTTP%d; whole-pair restart required", r.StatusCode)
		return
	}
	eventStream := strings.Contains(r.Header.Get("Content-Type"), "text/event-stream")
	if eventStream != j.stream {
		out.err = errors.New("response stream mismatch")
		return
	}
	limited := io.LimitReader(r.Body, j.backend.Config.ResponseLimit+1)
	if !j.stream {
		out.body, e = io.ReadAll(limited)
		if e == nil && int64(len(out.body)) > j.backend.Config.ResponseLimit {
			e = errors.New("response limit exceeded")
		}
		if e == nil {
			out.output, e = pairedJSONOutput(out.body)
		}
		out.err = e
		return
	}
	semantic := &pairedStream{choices: map[int]map[string]any{}}
	reader := bufio.NewReaderSize(limited, 65536)
	var frame bytes.Buffer
	total := int64(0)
	holding := false
	for {
		line, re := reader.ReadString('\n')
		total += int64(len(line))
		if total > j.backend.Config.ResponseLimit {
			out.err = errors.New("stream response limit exceeded")
			return
		}
		frame.WriteString(line)
		if line == "\n" || line == "\r\n" {
			raw := append([]byte(nil), frame.Bytes()...)
			frame.Reset()
			var data []string
			eventError := false
			for _, l := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
				if strings.HasPrefix(l, "data:") {
					data = append(data, strings.TrimSpace(l[5:]))
				}
				if strings.TrimSpace(l) == "event: error" || strings.TrimSpace(l) == "event:error" {
					eventError = true
				}
			}
			if eventError {
				out.err = errors.New("upstream SSE error event")
				return
			}
			if len(data) > 0 {
				terminal, e := semantic.feed([]byte(strings.Join(data, "\n")))
				if e != nil {
					out.err = e
					return
				}
				holding = holding || terminal
				if holding {
					out.terminal = append(out.terminal, raw...)
				} else if rank == 0 {
					j.emit(raw)
				}
			}
		}
		if re != nil {
			if re != io.EOF {
				out.err = re
				return
			}
			if strings.TrimSpace(frame.String()) != "" {
				out.err = errors.New("truncated SSE frame")
				return
			}
			break
		}
	}
	out.output, out.err = semantic.finish()
	return
}

func mergeMetricMaps(ma, mb map[string]any) error {
	for _, key := range []string{"prompt_tokens_computed", "prompt_tokens_cached", "prompt_tokens_local_cache", "prompt_tokens_external_cache", "prompt_tokens_cache_creation"} {
		if ma[key] != nil && mb[key] != nil && !reflect.DeepEqual(ma[key], mb[key]) {
			return fmt.Errorf("rank prefill accounting differs for %s", key)
		}
	}
	fa, oka := pairedFloat(ma["prefill_engine_ms"])
	fb, okb := pairedFloat(mb["prefill_engine_ms"])
	if oka && okb {
		if fb > fa {
			fa = fb
		}
		ma["pair_prefill_engine_ms_max"] = fa
		ma["pair_prefill_scope"] = "max_rank"
	}
	return nil
}
func mergePairedTimingMetrics(a, b []byte) ([]byte, error) {
	va, e := pairedObject(a)
	if e != nil {
		return nil, e
	}
	vb, e := pairedObject(b)
	if e != nil {
		return nil, e
	}
	ma, _ := va["metrics"].(map[string]any)
	mb, _ := vb["metrics"].(map[string]any)
	if ma == nil || mb == nil {
		return a, nil
	}
	if e = mergeMetricMaps(ma, mb); e != nil {
		return nil, e
	}
	return json.Marshal(va)
}
func sseFrameData(frame string) string {
	var data []string
	for _, line := range strings.Split(strings.ReplaceAll(frame, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(line[5:]))
		}
	}
	return strings.Join(data, "\n")
}
func terminalMetrics(blob []byte) map[string]any {
	for _, frame := range strings.Split(string(blob), "\n\n") {
		data := sseFrameData(frame)
		if data == "" || data == "[DONE]" {
			continue
		}
		v, e := pairedObject([]byte(data))
		if e != nil {
			continue
		}
		if m, ok := v["metrics"].(map[string]any); ok {
			return m
		}
	}
	return nil
}
func mergePairedSSETerminalMetrics(a, b []byte) ([]byte, error) {
	mb := terminalMetrics(b)
	if mb == nil {
		return a, nil
	}
	frames := strings.Split(string(a), "\n\n")
	for i, frame := range frames {
		data := sseFrameData(frame)
		if data == "" || data == "[DONE]" {
			continue
		}
		v, e := pairedObject([]byte(data))
		if e != nil {
			continue
		}
		ma, ok := v["metrics"].(map[string]any)
		if !ok {
			continue
		}
		if e = mergeMetricMaps(ma, mb); e != nil {
			return nil, e
		}
		enc, e := json.Marshal(v)
		if e != nil {
			return nil, e
		}
		frames[i] = "data: " + string(enc)
		return []byte(strings.Join(frames, "\n\n")), nil
	}
	return a, nil
}
func pairedFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, e := strconv.ParseFloat(string(n), 64)
		return f, e == nil
	case float64:
		return n, true
	}
	return 0, false
}

func pairedObject(b []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v map[string]any
	if e := d.Decode(&v); e != nil {
		return nil, e
	}
	if v == nil {
		return nil, errors.New("JSON must be object")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	if v["error"] != nil || v["type"] == "error" || v["type"] == "response.failed" || v["status"] == "failed" {
		return nil, errors.New("upstream error payload")
	}
	return v, nil
}
func pairedIndex(v any) (int, error) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, errors.New("noninteger index")
	}
	i, e := strconv.Atoi(string(n))
	if e != nil || i < 0 {
		return 0, errors.New("invalid index")
	}
	return i, nil
}
func pairedChoices(v any) ([]map[string]any, error) {
	choices, ok := v.([]any)
	if !ok || len(choices) == 0 {
		return nil, errors.New("missing generated choices")
	}
	out := []map[string]any{}
	seen := map[int]bool{}
	for _, cv := range choices {
		c, ok := cv.(map[string]any)
		if !ok {
			return nil, errors.New("invalid choice")
		}
		i, e := pairedIndex(c["index"])
		if e != nil || seen[i] {
			return nil, errors.New("invalid/duplicate choice index")
		}
		seen[i] = true
		finish, ok := c["finish_reason"].(string)
		if !ok || finish == "" {
			return nil, errors.New("missing finish reason")
		}
		_, text := c["text"].(string)
		_, message := c["message"].(map[string]any)
		if !text && !message {
			return nil, errors.New("choice has no generated output")
		}
		result := map[string]any{}
		for _, k := range []string{"index", "text", "message", "finish_reason", "stop_reason", "token_ids"} {
			if c[k] != nil {
				result[k] = c[k]
			}
		}
		out = append(out, result)
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := pairedIndex(out[i]["index"])
		b, _ := pairedIndex(out[j]["index"])
		return a < b
	})
	return out, nil
}
func pairedJSONOutput(b []byte) (any, error) {
	v, e := pairedObject(b)
	if e != nil {
		return nil, e
	}
	return pairedChoices(v["choices"])
}

type pairedStream struct {
	choices map[int]map[string]any
	done    bool
}

func appendGenerated(m map[string]any, key string, v any) error {
	if v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return errors.New("nonstring generated delta")
	}
	old, _ := m[key].(string)
	m[key] = old + s
	return nil
}
func (s *pairedStream) feed(b []byte) (bool, error) {
	if s.done {
		return false, errors.New("event after DONE")
	}
	if string(b) == "[DONE]" {
		s.done = true
		return true, nil
	}
	v, e := pairedObject(b)
	if e != nil {
		return false, e
	}
	choices, ok := v["choices"].([]any)
	if !ok {
		return false, errors.New("missing stream choices")
	}
	terminal := false
	for _, cv := range choices {
		c, ok := cv.(map[string]any)
		if !ok {
			return false, errors.New("invalid stream choice")
		}
		i, e := pairedIndex(c["index"])
		if e != nil {
			return false, e
		}
		state := s.choices[i]
		if state == nil {
			state = map[string]any{"index": c["index"]}
			s.choices[i] = state
		}
		if e = appendGenerated(state, "text", c["text"]); e != nil {
			return false, e
		}
		if dv, exists := c["delta"]; exists && dv != nil {
			delta, ok := dv.(map[string]any)
			if !ok {
				return false, errors.New("invalid delta")
			}
			m, ok := state["message"].(map[string]any)
			if !ok {
				m = map[string]any{}
				state["message"] = m
			}
			for key, value := range delta {
				if value == nil {
					continue
				}
				switch key {
				case "content", "reasoning", "reasoning_content", "refusal":
					if e = appendGenerated(m, key, value); e != nil {
						return false, e
					}
				case "role":
					role, ok := value.(string)
					if !ok || (m[key] != nil && m[key] != role) {
						return false, errors.New("inconsistent role")
					}
					m[key] = role
				case "tool_calls":
					calls, ok := value.([]any)
					if !ok {
						return false, errors.New("invalid tool deltas")
					}
					tools, ok := m[key].(map[int]map[string]any)
					if !ok {
						tools = map[int]map[string]any{}
						m[key] = tools
					}
					for _, tv := range calls {
						call, ok := tv.(map[string]any)
						if !ok {
							return false, errors.New("invalid tool delta")
						}
						index, e := pairedIndex(call["index"])
						if e != nil {
							return false, e
						}
						target := tools[index]
						if target == nil {
							target = map[string]any{}
							tools[index] = target
						}
						for _, field := range []string{"id", "type"} {
							if call[field] != nil {
								if target[field] != nil && !reflect.DeepEqual(target[field], call[field]) {
									return false, errors.New("inconsistent tool identifier")
								}
								target[field] = call[field]
							}
						}
						if call["function"] != nil {
							f, ok := call["function"].(map[string]any)
							if !ok {
								return false, errors.New("invalid tool function")
							}
							dest, ok := target["function"].(map[string]any)
							if !ok {
								dest = map[string]any{}
								target["function"] = dest
							}
							for _, field := range []string{"name", "arguments"} {
								if e = appendGenerated(dest, field, f[field]); e != nil {
									return false, e
								}
							}
						}
					}
				default:
					return false, fmt.Errorf("unsupported generated delta field: %s", key)
				}
			}
		}
		for _, key := range []string{"finish_reason", "stop_reason"} {
			if c[key] != nil {
				if state[key] != nil && !reflect.DeepEqual(state[key], c[key]) {
					return false, errors.New("conflicting choice termination")
				}
				state[key] = c[key]
				if key == "finish_reason" {
					terminal = true
				}
			}
		}
		if c["token_ids"] != nil {
			ids, ok := c["token_ids"].([]any)
			if !ok {
				return false, errors.New("invalid token ids")
			}
			for _, id := range ids {
				if _, e = pairedIndex(id); e != nil {
					return false, e
				}
			}
			old, _ := state["token_ids"].([]any)
			state["token_ids"] = append(old, ids...)
		}
	}
	return terminal, nil
}
func (s *pairedStream) finish() (any, error) {
	if !s.done {
		return nil, errors.New("stream ended without DONE")
	}
	choices := []any{}
	for _, c := range s.choices {
		choices = append(choices, c)
	}
	return pairedChoices(choices)
}
