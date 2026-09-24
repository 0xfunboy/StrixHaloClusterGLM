package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type App struct {
	publicURL       string
	cfg             Config
	client          *http.Client
	mu              sync.Mutex
	tasks           map[string]*Task
	admission       chan struct{}
	journal         Journal
	token           string
	tokenMu         sync.RWMutex
	retiredTokens   []string
	settingsMu      sync.Mutex
	apiSettings     atomic.Pointer[APISettings]
	lastMetrics     Metrics
	preferred       atomic.Value
	closing         atomic.Bool
	attachmentMu    sync.Mutex
	lifecycle       *modelLifecycle
	lifecycleMu     sync.Mutex
	lifecycleAction string
	lifecycleError  string
}

func newApp(c Config) (*App, error) {
	publicURL, e := validatePublicURL(os.Getenv("HALOCLU_PUBLIC_URL"))
	if e != nil {
		return nil, e
	}
	if e := os.MkdirAll(c.StateDir, 0700); e != nil {
		return nil, e
	}
	key := filepath.Join(c.StateDir, "api-token")
	b, e := os.ReadFile(key)
	if os.IsNotExist(e) {
		b = []byte(id() + id())
		e = os.WriteFile(key, b, 0600)
	}
	if e != nil {
		return nil, e
	}
	if len(bytes.TrimSpace(b)) < 32 {
		return nil, errors.New("invalid API token")
	}
	a := &App{cfg: c, client: &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: 30 * time.Second}}, tasks: map[string]*Task{}, admission: make(chan struct{}, 1), token: strings.TrimSpace(string(b)), journal: Journal{Path: filepath.Join(c.StateDir, "events.jsonl")}}
	a.publicURL = publicURL
	lifecycle, e := newModelLifecycle(c)
	if e != nil {
		return nil, fmt.Errorf("model lifecycle: %w", e)
	}
	a.lifecycle = lifecycle
	a.preferred.Store(c.DefaultProfile)
	if e := a.loadAPISettings(); e != nil {
		return nil, e
	}
	if v, e := os.ReadFile(filepath.Join(c.StateDir, "preferred-profile")); e == nil {
		if _, ok := c.Profiles[string(v)]; ok {
			a.preferred.Store(string(v))
		}
	}
	return a, nil
}
func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (a *App) authorized(w http.ResponseWriter, r *http.Request) bool {
	if len(r.Header.Values("Authorization")) > 0 {
		if exactBearer(r, a.currentToken()) {
			return true
		}
		jsonReply(w, 401, map[string]string{"error": "invalid Bearer API token"})
		return false
	}
	if _, e := a.authenticateBrowserSession(r); e == nil {
		return true
	}
	jsonReply(w, 401, map[string]string{"error": "local Bearer token or valid same-origin browser session required"})
	return false
}
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("Content-Type application/json required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	dec := json.NewDecoder(r.Body)
	if e := dec.Decode(v); e != nil {
		return e
	}
	var extra any
	if e := dec.Decode(&extra); e != io.EOF {
		return errors.New("trailing JSON body")
	}
	return nil
}
func (a *App) health(ctx context.Context) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", a.cfg.Backend+"/health", nil)
	resp, e := a.client.Do(req)
	if e != nil {
		return map[string]any{"status": "degraded", "error": e.Error(), "backend": a.cfg.Backend}
	}
	defer resp.Body.Close()
	v := map[string]any{}
	if e := json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&v); e != nil || v == nil {
		return map[string]any{"status": "degraded", "error": "invalid backend health JSON", "backend": a.cfg.Backend}
	}
	if resp.StatusCode != 200 {
		v["status"] = "degraded"
	}
	v["backend"] = a.cfg.Backend
	return v
}
func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	a.registerBrowserAuthRoutes(mux)
	a.registerWorkspaceRoutes(mux)
	reason := ""
	if !a.cfg.ToolCalls {
		reason = "Function tools disabled until paired protocol qualification"
	}
	a.setWorkspaceToolCapability(a.cfg.ToolCalls, reason)
	a.registerOptionsRoutes(mux)
	a.registerSettingsRoutes(mux)
	a.registerLifecycleRoutes(mux)
	a.registerAttachmentRoutes(mux)
	registerCatalogRoutes(a, mux)
	a.registerOperationRoutes(mux)
	a.registerDownloadRoutes(mux)
	a.registerConversationRoutes(mux)
	mux.Handle("/", publicWebHandler(a.publicURL))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.LifecycleCommand != "" || a.lifecycle != nil {
			l := a.lifecycleStatus(r.Context())
			jsonReply(w, 200, map[string]any{"status": "ok", "gateway": "ok", "model_lifecycle": l})
			return
		}
		h := a.health(r.Context())
		code := 200
		if h["status"] != "ok" {
			code = 503
		}
		jsonReply(w, code, h)
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(w, r) {
			return
		}
		a.mu.Lock()
		m := a.lastMetrics
		active := []string{}
		for id, t := range a.tasks {
			s := t.snapshot()["status"]
			if s == "queued" || s == "running" || s == "draining" || s == "applying" {
				active = append(active, id)
			}
		}
		a.mu.Unlock()
		jsonReply(w, 200, map[string]any{"model": a.cfg.Model, "profile": a.preferred.Load(), "profiles": a.cfg.Profiles, "profile_status": a.cfg.ProfileStatus, "active_request": active, "health": a.health(r.Context()), "lifecycle": a.lifecycleStatus(r.Context()), "metrics": m, "acceptance": m.Acceptance, "context_limit": a.cfg.MaxContextTokens, "nodes": a.nodeStats(r.Context())})
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(w, r) {
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "strixglm_tasks %d\nstrixglm_inference_active %d\n", len(a.tasks), len(a.admission))
		if a.lastMetrics.DecodeTPS != nil {
			fmt.Fprintf(w, "strixglm_last_decode_tps %g\n", *a.lastMetrics.DecodeTPS)
		}
	})
	mux.HandleFunc("POST /v1/profile", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(w, r) {
			return
		}
		var v struct {
			Profile string `json:"profile"`
		}
		if e := decodeBody(w, r, &v); e != nil {
			jsonReply(w, 400, map[string]string{"error": e.Error()})
			return
		}
		if _, ok := a.cfg.Profiles[v.Profile]; !ok {
			jsonReply(w, 400, map[string]string{"error": "unknown profile"})
			return
		}
		a.preferred.Store(v.Profile)
		_ = os.WriteFile(filepath.Join(a.cfg.StateDir, "preferred-profile"), []byte(v.Profile), 0600)
		jsonReply(w, 200, v)
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(w, r) {
			return
		}
		jsonReply(w, 200, map[string]any{"object": "list", "data": []map[string]string{{"id": a.cfg.Model, "object": "model", "owned_by": "local"}}})
	})
	mux.HandleFunc("POST /v1/chat/completions", a.proxyChat)
	mux.HandleFunc("POST /v1/coding/tasks", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(w, r) {
			return
		}
		var s TaskSpec
		if e := decodeBody(w, r, &s); e != nil {
			jsonReply(w, 400, map[string]string{"error": e.Error()})
			return
		}
		if s.Profile == "" {
			s.Profile = a.preferred.Load().(string)
		}
		t, e := a.submit(s)
		if e != nil {
			jsonReply(w, 400, map[string]string{"error": e.Error()})
			return
		}
		jsonReply(w, 202, t.snapshot())
	})
	mux.HandleFunc("GET /v1/coding/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(w, r) {
			return
		}
		id := r.PathValue("id")
		if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
			jsonReply(w, 404, map[string]string{"error": "unknown task"})
			return
		}
		a.mu.Lock()
		t := a.tasks[id]
		a.mu.Unlock()
		if t != nil {
			jsonReply(w, 200, t.snapshot())
			return
		}
		b, e := os.ReadFile(filepath.Join(a.cfg.StateDir, "tasks", id, "result.json"))
		if e != nil {
			jsonReply(w, 404, map[string]string{"error": "unknown task"})
			return
		}
		var saved map[string]any
		_ = json.Unmarshal(b, &saved)
		if s := saved["status"]; s == "queued" || s == "running" || s == "draining" || s == "applying" {
			saved["status"] = "INTERRUPTED"
			saved["error"] = "process restarted; task is NOT automatically replayed"
			if s == "applying" {
				saved["error"] = "process restarted during apply; inspect apply-receipt.json and backups before further edits; NOT replayed"
			}
		}
		jsonReply(w, 200, saved)
	})
	mux.HandleFunc("POST /v1/coding/tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(w, r) {
			return
		}
		a.mu.Lock()
		t := a.tasks[r.PathValue("id")]
		a.mu.Unlock()
		if t == nil {
			jsonReply(w, 404, map[string]string{"error": "unknown live task"})
			return
		}
		t.mu.Lock()
		s := t.Status
		if s == "queued" || s == "running" {
			t.cancel()
			t.Status = "draining"
		}
		t.mu.Unlock()
		t.save()
		jsonReply(w, 202, t.snapshot())
	})
	mux.HandleFunc("POST /v1/coding/tasks/{id}/apply", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(w, r) {
			return
		}
		var v struct {
			Confirm bool `json:"confirm"`
		}
		if decodeBody(w, r, &v) != nil || !v.Confirm {
			jsonReply(w, 400, map[string]string{"error": "explicit confirm:true required"})
			return
		}
		a.mu.Lock()
		t := a.tasks[r.PathValue("id")]
		a.mu.Unlock()
		if t == nil {
			jsonReply(w, 404, map[string]string{"error": "only this process's verified tasks may apply; saved patches remain available"})
			return
		}
		if e := a.applyTask(t); e != nil {
			jsonReply(w, 409, map[string]string{"error": e.Error()})
			return
		}
		t.save()
		jsonReply(w, 200, t.snapshot())
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.closing.Load() && (r.Method == "POST" || r.Method == "PUT" || r.Method == "DELETE") {
			jsonReply(w, 503, map[string]string{"error": "gateway draining for shutdown; no new request accepted"})
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		_, pattern := mux.Handler(r)
		if category := a.disabledAPICategory(pattern, r); category != "" {
			if a.authorized(w, r) {
				jsonReply(w, 403, map[string]string{"error": category + " new requests are paused in Options", "code": "api_disabled"})
			}
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (a *App) proxyChat(w http.ResponseWriter, r *http.Request) {
	if !a.authorized(w, r) {
		return
	}
	a.proxyAuthorizedChat(w, r)
}

// The private Unix Pi bridge has a scoped session capability, not the admin
// credential. Once admitted it must survive unrelated API-token rotation.
func (a *App) proxyAuthorizedChat(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if e := a.requireModelReady(r.Context()); e != nil {
		jsonReply(w, 503, map[string]string{"error": e.Error(), "code": "model_not_ready"})
		return
	}
	var p map[string]any
	if e := decodeBody(w, r, &p); e != nil {
		jsonReply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	history, e := a.prepareConversationChat(p)
	if e != nil {
		jsonReply(w, 409, map[string]string{"error": e.Error()})
		return
	}
	defer history.close()
	if e := a.expandChatAttachments(p); e != nil {
		jsonReply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	if a.cfg.ToolCalls {
		if e := normalizeToolText(p); e != nil {
			jsonReply(w, 400, map[string]string{"error": e.Error()})
			return
		}
	}
	if e := validateChatWithReasoning(p, a.cfg.ToolCalls, a.supportsReasoning); e != nil {
		jsonReply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	settings, e := a.prepareChat(p)
	if e != nil {
		jsonReply(w, 400, map[string]string{"error": e.Error()})
		return
	}
	timing := &PromptTiming{PreparationMS: elapsedMS(started)}
	lockStarted := time.Now()
	release, e := a.modelLock(r.Context())
	if e != nil {
		jsonReply(w, 409, map[string]string{"error": e.Error()})
		return
	}
	defer release()
	timing.AdmissionMS = elapsedMS(lockStarted)
	tokenizeStarted := time.Now()
	if e := a.admitChat(r.Context(), p, &settings); e != nil {
		status := 413
		if errors.Is(e, errTokenizerPreflight) {
			status = 502
		}
		jsonReply(w, status, map[string]string{"error": e.Error()})
		return
	}
	timing.TokenizeMS = elapsedMS(tokenizeStarted)
	timing.PromptTokens = settings.Prompt
	if needsToolAdapter(p) {
		a.proxyToolChat(w, r, p, settings, started)
		return
	}
	if e := history.start(p, settings); e != nil {
		jsonReply(w, 409, map[string]string{"error": e.Error()})
		return
	}
	body, _ := json.Marshal(p)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(a.cfg.ModelTimeout)*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", a.cfg.Backend+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	stream, _ := p["stream"].(bool)
	// Opt-in UI metadata only. Other OpenAI/Pi clients retain the original wire
	// protocol, HTTP errors and headers. No telemetry setting reaches the model.
	telemetry := stream && r.Header.Get("X-HaloClu-Timings") == "1"
	timing.DispatchMS = elapsedMS(started)
	if telemetry {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		setChatHeaders(w, settings)
		w.WriteHeader(http.StatusOK)
		writeChatEvent(w, "haloclu.timing", timing)
	}
	resp, e := a.client.Do(req)
	if e != nil {
		if telemetry {
			writeChatEvent(w, "error", map[string]string{"error": e.Error()})
			history.finish(ModelResult{Metrics: Metrics{PromptTiming: timing}}, false)
		} else {
			jsonReply(w, 502, map[string]string{"error": e.Error()})
		}
		return
	}
	defer resp.Body.Close()
	headersMS := elapsedMS(started) - timing.DispatchMS
	timing.BackendHeadersMS = &headersMS
	if telemetry {
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<20))
			writeChatEvent(w, "error", map[string]string{"error": fmt.Sprintf("backend did not return an SSE success response (HTTP %d)", resp.StatusCode)})
			history.finish(ModelResult{Metrics: Metrics{PromptTiming: timing}}, false)
			return
		}
		writeChatEvent(w, "haloclu.timing", timing)
	} else {
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.Header().Set("Cache-Control", "no-store")
		setChatHeaders(w, settings)
		w.WriteHeader(resp.StatusCode)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") && resp.StatusCode == 200 {
		result := ModelResult{Metrics: Metrics{PromptTiming: timing}}
		detached := false
		streamErr := consumeSSEProgress(resp.Body, started, func(b []byte) {
			if detached {
				return
			}
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, e := w.Write(b); e != nil {
				detached = true
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}, &result, func(m Metrics) {
			if timing.FirstTokenMS == nil && m.TTFTMS != nil {
				timing.FirstTokenMS = m.TTFTMS
				if telemetry && !detached {
					writeChatEvent(w, "haloclu.timing", timing)
				}
			}
			history.progress(result)
		})
		if streamErr != nil {
			a.journal.Log("chat_stream_error", map[string]any{"error": streamErr.Error()})
		}
		result.Metrics.HTTPSeconds = time.Since(started).Seconds()
		history.finish(result, streamErr == nil)
		a.mu.Lock()
		a.lastMetrics = result.Metrics
		a.mu.Unlock()
	} else {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		var p map[string]any
		if resp.StatusCode == 200 && json.Unmarshal(b, &p) == nil {
			m := Metrics{HTTPSeconds: time.Since(started).Seconds()}
			parseMetrics(&m, p)
			a.mu.Lock()
			a.lastMetrics = m
			a.mu.Unlock()
			result := ModelResult{Metrics: m, StreamComplete: true}
			if choices, ok := p["choices"].([]any); ok && len(choices) > 0 {
				choice := object(choices[0])
				msg := object(choice["message"])
				result.Content = stringValue(msg["content"])
				result.Reasoning = stringValue(msg["reasoning_content"])
				if result.Reasoning == "" {
					result.Reasoning = stringValue(msg["reasoning"])
				}
				result.FinishReason = stringValue(choice["finish_reason"])
			}
			history.finish(result, true)
		}
		_, _ = w.Write(b)
	}
}
func (a *App) nodeStats(ctx context.Context) []map[string]any {
	out := make([]map[string]any, 2)
	var wg sync.WaitGroup
	for rank := 0; rank < 2; rank++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			row := map[string]any{"name": fmt.Sprintf("NODE%02d", i+1), "address": fmt.Sprintf("10.55.0.%d", i+1), "health": "unknown"}
			cc, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			script := "awk '/MemTotal:/{t=$2}/MemAvailable:/{a=$2}END{print t*1024, (t-a)*1024}' /proc/meminfo; for p in /sys/class/drm/card*/device/gpu_busy_percent; do if test -r \"$p\"; then head -1 \"$p\"; break; fi; done"
			var cmd *exec.Cmd
			if i == 0 {
				cmd = exec.CommandContext(cc, "bash", "-c", script)
			} else {
				cmd = exec.CommandContext(cc, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=2", a.cfg.RemoteSSH, "bash -c "+shellQuote(script))
			}
			b, e := cmd.Output()
			if e == nil {
				parts := strings.Fields(string(b))
				if len(parts) >= 2 {
					total, _ := strconv.ParseFloat(parts[0], 64)
					used, _ := strconv.ParseFloat(parts[1], 64)
					row["memory_total_bytes"] = total
					row["memory_used_bytes"] = used
					row["health"] = "reachable"
				}
				if len(parts) >= 3 {
					gpu, _ := strconv.ParseFloat(parts[2], 64)
					row["gpu_utilization_percent"] = gpu
				}
			}
			out[i] = row
		}(rank)
	}
	wg.Wait()
	return out
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// Main runs the management CLI, server or isolated sandbox re-exec entrypoint.
// It retains the command's established exit statuses.
func Main() {
	if len(os.Args) > 1 && os.Args[1] == "sandbox-exec" {
		if e := sandboxEntry(os.Args[2:]); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(126)
		}
		return
	}
	if e := entry(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func entry(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: strixglm serve|chat|code|status|models|profile|benchmark|cluster")
	}
	cmd := args[0]
	f := flag.NewFlagSet(cmd, flag.ContinueOnError)
	configPath := f.String("config", "config.json", "configuration file")
	profile := f.String("profile", "", "low/high/max (legacy aliases retained for frozen benchmark protocols)")
	repo := f.String("repo", "", "repository path")
	taskText := f.String("task", "", "coding instruction")
	taskFile := f.String("task-file", "", "instruction file")
	specFile := f.String("spec", "", "task JSON")
	test := f.String("test", "", "test command (quoted argv)")
	build := f.String("build", "", "build command")
	allowed := f.String("allowed", "", "comma-separated editable paths")
	message := f.String("message", "", "chat message")
	apply := f.Bool("apply", false, "explicitly apply verified patch")
	repairs := f.Int("max-repairs", -1, "repair limit")
	ctxTokens := f.Int("context-tokens", 0, "estimated context budget")
	maxTokens := f.Int("max-tokens", 0, "output cap including reasoning; omitted chat uses auto, coding uses the profile")
	timeout := f.Int("timeout", 0, "code-only model timeout seconds; chat uses configured model_timeout")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	maxTokensExplicit := false
	f.Visit(func(option *flag.Flag) {
		if option.Name == "max-tokens" {
			maxTokensExplicit = true
		}
	})
	cfg, e := loadConfig(*configPath)
	if e != nil {
		return e
	}
	if cmd == "serve" {
		a, e := newApp(cfg)
		if e != nil {
			return e
		}
		srv := &http.Server{Addr: cfg.Listen, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		shutdownDone := make(chan struct{})
		go func() {
			defer close(shutdownDone)
			<-ctx.Done()
			a.closing.Store(true)
			shutdown, cancel := context.WithTimeout(context.Background(), time.Duration(max(cfg.ModelTimeout, 1800)+10)*time.Second)
			defer cancel()
			_ = a.shutdownOperations(shutdown)
			_ = a.shutdownDownloads(shutdown)
			_ = a.shutdownWorkspaces(shutdown)
			a.mu.Lock()
			pending := []*Task{}
			for _, t := range a.tasks {
				t.cancel()
				pending = append(pending, t)
			}
			a.mu.Unlock()
			// Per-task overrides may be longer than the default model timeout.
			// Keep draining up to the maximum accepted override, not just default.
			_ = srv.Shutdown(shutdown)
			for _, t := range pending {
				select {
				case <-t.done:
				case <-shutdown.Done():
					return
				}
			}
		}()
		log.Printf("StrixHaloClusterGLM http://%s; token file %s", cfg.Listen, filepath.Join(cfg.StateDir, "api-token"))
		e = srv.ListenAndServe()
		if e == http.ErrServerClosed {
			<-shutdownDone
			return nil
		}
		return e
	}
	if cmd == "benchmark" {
		return runBenchmarkCLI(f.Args())
	}
	if cmd == "cluster" {
		return clusterCLI(cfg, f.Args())
	}
	base := "http://" + cfg.Listen
	token, e := os.ReadFile(filepath.Join(cfg.StateDir, "api-token"))
	if e != nil {
		return e
	}
	client := &http.Client{Timeout: time.Duration(cfg.ModelTimeout*7+60) * time.Second}
	call := func(method, path string, data any) (map[string]any, error) {
		var body []byte
		if data != nil {
			body, _ = json.Marshal(data)
		}
		req, _ := http.NewRequest(method, base+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		r, e := client.Do(req)
		if e != nil {
			return nil, e
		}
		defer r.Body.Close()
		var v map[string]any
		e = json.NewDecoder(r.Body).Decode(&v)
		if r.StatusCode >= 400 {
			return v, fmt.Errorf("HTTP%d: %v", r.StatusCode, v)
		}
		return v, e
	}
	switch cmd {
	case "status", "models":
		path := "/v1/status"
		if cmd == "models" {
			path = "/v1/models"
		}
		v, e := call("GET", path, nil)
		if e == nil {
			printJSON(v)
		}
		return e
	case "profile":
		if len(f.Args()) != 1 {
			return errors.New("profile low|high|max")
		}
		v, e := call("POST", "/v1/profile", map[string]string{"profile": f.Args()[0]})
		if e == nil {
			printJSON(v)
		}
		return e
	case "code":
		var s TaskSpec
		if *specFile != "" {
			b, e := os.ReadFile(*specFile)
			if e != nil {
				return e
			}
			if e = json.Unmarshal(b, &s); e != nil {
				return e
			}
		}
		if *repo != "" {
			s.Repo = *repo
		}
		if *taskText != "" {
			s.Task = *taskText
		}
		if *taskFile != "" {
			b, e := os.ReadFile(*taskFile)
			if e != nil {
				return e
			}
			s.Task = string(b)
		}
		if *test != "" {
			s.TestCommand, e = splitCommand(*test)
			if e != nil {
				return e
			}
		}
		if *build != "" {
			s.BuildCommand, e = splitCommand(*build)
			if e != nil {
				return e
			}
		}
		if *allowed != "" {
			s.AllowedPaths = strings.Split(*allowed, ",")
		}
		if *profile != "" {
			s.Profile = *profile
		}
		if *repairs >= 0 {
			s.MaxRepairs = repairs
		}
		if *ctxTokens > 0 {
			s.ContextTokens = *ctxTokens
		}
		if *maxTokens > 0 {
			s.MaxTokens = *maxTokens
		}
		if *timeout > 0 {
			s.Timeout = *timeout
		}
		s.Apply = *apply
		v, e := call("POST", "/v1/coding/tasks", s)
		if e != nil {
			return e
		}
		id := stringValue(v["id"])
		fmt.Fprintln(os.Stderr, "task", id)
		for {
			status := stringValue(v["status"])
			if status != "queued" && status != "running" && status != "draining" && status != "applying" {
				printJSON(v)
				if status != "PASS" {
					return fmt.Errorf("task %s", status)
				}
				return nil
			}
			time.Sleep(time.Second)
			v, e = call("GET", "/v1/coding/tasks/"+id, nil)
			if e != nil {
				return e
			}
		}
	case "chat":
		messages := []map[string]string{}
		send := func(s string) error {
			messages = append(messages, map[string]string{"role": "user", "content": s})
			p := map[string]any{"model": cfg.Model, "messages": messages, "stream": true, "stream_options": map[string]any{"include_usage": true}}
			if *profile != "" {
				p["profile"] = *profile
			}
			if *ctxTokens > 0 {
				p["context_tokens"] = *ctxTokens
			}
			if maxTokensExplicit {
				p["max_tokens"] = *maxTokens
			}
			b, _ := json.Marshal(p)
			req, _ := http.NewRequest("POST", base+"/v1/chat/completions", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
			resp, e := client.Do(req)
			if e != nil {
				return e
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				b, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("chat HTTP%d %s", resp.StatusCode, b)
			}
			var r ModelResult
			var event []byte
			e = consumeSSE(resp.Body, time.Now(), func(line []byte) {
				event = append(event, line...)
				if bytes.Equal(line, []byte("\n")) {
					for _, l := range strings.Split(string(event), "\n") {
						if strings.HasPrefix(l, "data: ") {
							var p map[string]any
							if json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &p) == nil {
								xs, _ := p["choices"].([]any)
								for _, x := range xs {
									fmt.Print(stringValue(object(object(x)["delta"])["content"]))
								}
							}
						}
					}
					event = nil
				}
			}, &r)
			fmt.Println()
			messages = append(messages, map[string]string{"role": "assistant", "content": r.Content})
			return e
		}
		if *message != "" {
			return send(*message)
		}
		scanner := bufio.NewScanner(os.Stdin)
		for {
			fmt.Print("you> ")
			if !scanner.Scan() {
				break
			}
			s := scanner.Text()
			if s == "/quit" {
				break
			}
			if s == "/clear" {
				messages = nil
				continue
			}
			if e := send(s); e != nil {
				fmt.Fprintln(os.Stderr, e)
			}
		}
		return scanner.Err()
	default:
		return errors.New("unknown command")
	}
}
func printJSON(v any) { b, _ := json.MarshalIndent(v, "", "  "); fmt.Println(string(b)) }
