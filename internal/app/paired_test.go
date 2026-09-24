package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func pairedTestBackend(t *testing.T, h0, h1 http.HandlerFunc) *PairedBackend {
	t.Helper()
	a := httptest.NewServer(h0)
	b := httptest.NewServer(h1)
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	p, e := NewPairedBackend(PairedConfig{RankURLs: [2]string{"http://10.55.0.1:18110", "http://10.55.0.2:18110"}, Model: "glm", StateDir: t.TempDir(), TimeoutSeconds: 2})
	if e != nil {
		t.Fatal(e)
	}
	p.Config.RankURLs = [2]string{a.URL, b.URL}
	t.Cleanup(p.client.CloseIdleConnections)
	return p
}
func pairedResponse(text string) string {
	return fmt.Sprintf(`{"id":"irrelevant","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"completion_tokens":2}}`, text)
}
func pairedTestRequest(p *PairedBackend, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	return w
}
func TestPairedIdenticalInputsAndMetadataIgnored(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	handler := func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		count := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, strings.Replace(pairedResponse("ok"), "irrelevant", fmt.Sprint(count), 1))
	}
	p := pairedTestBackend(t, handler, handler)
	w := pairedTestRequest(p, `{"model":"glm","messages":[]}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatal("rank request bytes differ")
	}
	var v map[string]any
	_ = json.Unmarshal(bodies[0], &v)
	if v["seed"] == nil {
		t.Fatal("seed not normalized")
	}
	if _, e := os.Stat(p.marker()); !os.IsNotExist(e) {
		t.Fatal("successful marker not cleared")
	}
}
func TestPairedMismatchPoisons(t *testing.T) {
	h := func(text string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, pairedResponse(text))
		}
	}
	p := pairedTestBackend(t, h("one"), h("two"))
	w := pairedTestRequest(p, `{"messages":[],"seed":1}`)
	if w.Code != 502 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = pairedTestRequest(p, `{"messages":[]}`)
	if w.Code != 503 {
		t.Fatal("poison did not block subsequent generation")
	}
	if _, e := os.Stat(p.marker()); e != nil {
		t.Fatal("missing durable poison")
	}
}
func TestPairedRejectsInvalidInputBeforeDispatch(t *testing.T) {
	handler := func(w http.ResponseWriter, r *http.Request) { t.Error("invalid input reached rank") }
	p := pairedTestBackend(t, handler, handler)
	for _, body := range []string{`{"seed":-1}`, `{"seed":true}`, `{"seed":1.2}`, `{"stream":1}`, `{"model":"other"}`, `[]`, `{} {}`} {
		if w := pairedTestRequest(p, body); w.Code != 400 {
			t.Fatalf("accepted %s: %d", body, w.Code)
		}
	}
}
func TestPairedCancelDrainsAndBusyAdmission(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	handler := func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, pairedResponse("done"))
	}
	p := pairedTestBackend(t, handler, handler)
	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"seed":1}`)).WithContext(ctx)
	done := make(chan struct{})
	go func() { p.ServeHTTP(httptest.NewRecorder(), r); close(done) }()
	<-started
	<-started
	cancel()
	<-done
	if w := pairedTestRequest(p, `{"seed":1}`); w.Code != 409 {
		t.Fatalf("admitted work while cancelled pair still draining: %d", w.Code)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		p.mu.Lock()
		busy := p.busy
		poison := p.poison
		p.mu.Unlock()
		if !busy {
			if poison != "" {
				t.Fatal(poison)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pair did not finish draining")
		}
		time.Sleep(time.Millisecond)
	}
}
func streamFrames(text string) string {
	return `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"` + text + `"},"finish_reason":null}]}` + "\n\n" + `data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"
}
func TestPairedStreamTerminalWaitsForBoth(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	h0 := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, streamFrames("hello"))
		w.(http.Flusher).Flush()
	}
	h1 := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, streamFrames("hello"))
		w.(http.Flusher).Flush()
		close(started)
		<-release
	}
	p := pairedTestBackend(t, h0, h1)
	server := httptest.NewServer(p)
	defer server.Close()
	resp, e := http.Post(server.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"stream":true,"seed":1}`))
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	<-started
	first := make([]byte, 4096)
	n, e := resp.Body.Read(first)
	if e != nil || !strings.Contains(string(first[:n]), "hello") || strings.Contains(string(first[:n]), "[DONE]") || strings.Contains(string(first[:n]), `"finish_reason":"stop"`) {
		t.Fatalf("terminal escaped early: %q %v", first[:n], e)
	}
	close(release)
	rest, e := io.ReadAll(resp.Body)
	if e != nil || !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("missing successful terminal %q %v", rest, e)
	}
}
func TestPairedStreamTruncationPoisons(t *testing.T) {
	h := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, strings.ReplaceAll(streamFrames("hello"), "data: [DONE]\n\n", ""))
	}
	p := pairedTestBackend(t, h, h)
	w := pairedTestRequest(p, `{"stream":true,"seed":1}`)
	if strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatal("invented successful terminal")
	}
	p.mu.Lock()
	poison := p.poison
	p.mu.Unlock()
	if poison == "" {
		t.Fatal("truncated stream not poisoned")
	}
}
func TestPairedPersistentMarkerRejectsStartupAdmission(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "pair-poison.json"), []byte(`{"reason":"orphan"}`), 0600); e != nil {
		t.Fatal(e)
	}
	p, e := NewPairedBackend(PairedConfig{RankURLs: [2]string{"http://10.55.0.1:18110", "http://10.55.0.2:18110"}, Model: "glm", StateDir: dir})
	if e != nil {
		t.Fatal(e)
	}
	if w := pairedTestRequest(p, `{}`); w.Code != 503 {
		t.Fatal("orphaned request admitted")
	}
}
func TestPairedStreamSemanticChunking(t *testing.T) {
	a := &pairedStream{choices: map[int]map[string]any{}}
	b := &pairedStream{choices: map[int]map[string]any{}}
	for _, s := range []*pairedStream{a, b} {
		if _, e := s.feed([]byte(`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`)); e != nil {
			t.Fatal(e)
		}
	}
	for _, chunk := range []string{"ab", "cd"} {
		_, _ = a.feed([]byte(fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, chunk)))
	}
	_, _ = b.feed([]byte(`{"choices":[{"index":0,"delta":{"content":"abcd"}}]}`))
	for _, s := range []*pairedStream{a, b} {
		_, _ = s.feed([]byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`))
		_, _ = s.feed([]byte(`[DONE]`))
	}
	x, e := a.finish()
	if e != nil {
		t.Fatal(e)
	}
	y, e := b.finish()
	if e != nil {
		t.Fatal(e)
	}
	xb, _ := json.Marshal(x)
	yb, _ := json.Marshal(y)
	if !bytes.Equal(xb, yb) {
		t.Fatal("equivalent chunking differs")
	}
}

func TestPairedGenerationReusesPersistentConnections(t *testing.T) {
	var mu sync.Mutex
	addresses := [2][]string{}
	h := func(rank int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			mu.Lock()
			addresses[rank] = append(addresses[rank], r.RemoteAddr)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, pairedResponse("ok"))
		}
	}
	p := pairedTestBackend(t, h(0), h(1))
	for i := 0; i < 3; i++ {
		if w := pairedTestRequest(p, `{"seed":1}`); w.Code != 200 {
			t.Fatalf("request%d: %d %s", i, w.Code, w.Body.String())
		}
	}
	for rank := 0; rank < 2; rank++ {
		if len(addresses[rank]) != 3 || addresses[rank][0] != addresses[rank][1] || addresses[rank][1] != addresses[rank][2] {
			t.Fatalf("rank%d did not reuse its drained connection: %v", rank, addresses[rank])
		}
	}
}

func TestPairedMirrorUnexpectedEOFIsRankedAndNotRetried(t *testing.T) {
	var mu sync.Mutex
	calls := [2]int{}
	h0 := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[0]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, pairedResponse("ok"))
	}
	h1 := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		calls[1]++
		number := calls[1]
		mu.Unlock()
		if number == 1 {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, pairedResponse("ok"))
			return
		}
		conn, buf, e := w.(http.Hijacker).Hijack()
		if e != nil {
			t.Error(e)
			return
		}
		fmt.Fprint(buf, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1000\r\n\r\n{\"choices\":[")
		_ = buf.Flush()
		_ = conn.Close()
	}
	p := pairedTestBackend(t, h0, h1)
	if first := pairedTestRequest(p, `{"seed":1}`); first.Code != 200 {
		t.Fatalf("warm connection request failed: %d %s", first.Code, first.Body.String())
	}
	w := pairedTestRequest(p, `{"seed":1}`)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "rank1") || !strings.Contains(w.Body.String(), "unexpected EOF") {
		t.Fatalf("missing specific mirror error: %d %s", w.Code, w.Body.String())
	}
	if w = pairedTestRequest(p, `{"seed":1}`); w.Code != 503 {
		t.Fatal("ambiguous mirrored response was retried")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != [2]int{2, 2} {
		t.Fatalf("more than one dispatch per rank: %v", calls)
	}
}

func TestPairedHealthDuringGenerationDoesNotReleaseAdmission(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/health" {
			w.WriteHeader(200)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, pairedResponse("done"))
	}
	p := pairedTestBackend(t, h, h)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- pairedTestRequest(p, `{"seed":1}`) }()
	<-started
	<-started
	health := p.Health(context.Background())
	if health["status"] != "ok" || health["busy"] != true || health["ranks"] != [2]bool{true, true} {
		t.Fatalf("incorrect concurrent health: %+v", health)
	}
	if w := pairedTestRequest(p, `{"seed":2}`); w.Code != 409 {
		t.Fatal("health released active generation")
	}
	once.Do(func() { close(release) })
	if w := <-done; w.Code != 200 {
		t.Fatalf("health interfered with paired output: %d %s", w.Code, w.Body.String())
	}
}

func TestPairedSSEPreservesRank0MetricsWithoutComparingTimings(t *testing.T) {
	h := func(rank int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			frames := strings.Replace(streamFrames("ok"), "data: [DONE]\n\n", "", 1)
			fmt.Fprint(w, frames)
			fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"completion_tokens\":%d},\"metrics\":{\"decode_tps\":%d,\"speculative_decoding\":{\"acceptance_rate\":0.75}}}\n\n", 10+rank, 25+rank)
			fmt.Fprint(w, "data: [DONE]\n\n")
		}
	}
	p := pairedTestBackend(t, h(0), h(1))
	w := pairedTestRequest(p, `{"stream":true,"seed":1}`)
	body := w.Body.String()
	for _, want := range []string{`"completion_tokens":10`, `"decode_tps":25`, `"acceptance_rate":0.75`, `[DONE]`} {
		if !strings.Contains(body, want) {
			t.Fatalf("rank0 telemetry missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "event: error") {
		t.Fatal("timing/usage differences mistaken for generated-output divergence")
	}
}

func TestPairedDeadlinePoisonsAndCancelsBothSockets(t *testing.T) {
	ended := make(chan struct{}, 2)
	h := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(1500 * time.Millisecond):
		}
		ended <- struct{}{}
	}
	p := pairedTestBackend(t, h, h)
	p.Config.TimeoutSeconds = 1
	started := time.Now()
	w := pairedTestRequest(p, `{"seed":1}`)
	if w.Code != 502 || time.Since(started) > 1500*time.Millisecond {
		t.Fatalf("deadline not enforced: %d %s", w.Code, w.Body.String())
	}
	for i := 0; i < 2; i++ {
		select {
		case <-ended:
		case <-time.After(time.Second):
			t.Fatal("rank socket not cancelled")
		}
	}
	if w = pairedTestRequest(p, `{"seed":1}`); w.Code != 503 {
		t.Fatal("deadline uncertainty did not persist")
	}
}

func TestOneShotHealthClosesItsIdleConnection(t *testing.T) {
	closed := make(chan struct{}, 8)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	server.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateClosed {
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	server.Start()
	defer server.Close()
	if !controllerHealth(context.Background(), server.URL) {
		t.Fatal("health probe failed")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("one-shot health transport leaked an idle connection")
	}
}

func TestMergePairedPrefillMetricsUsesCriticalRank(t *testing.T) {
	a := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"metrics":{"prefill_engine_ms":120.5,"prompt_tokens_computed":2048,"prompt_tokens_cached":0}}`)
	b := []byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"metrics":{"prefill_engine_ms":125.25,"prompt_tokens_computed":2048,"prompt_tokens_cached":0}}`)
	out, err := mergePairedTimingMetrics(a, b)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if json.Unmarshal(out, &v) != nil {
		t.Fatal("bad json")
	}
	m := v["metrics"].(map[string]any)
	if m["pair_prefill_engine_ms_max"] != 125.25 || m["pair_prefill_scope"] != "max_rank" {
		t.Fatalf("bad merge %#v", m)
	}
}

func TestMergePairedSSETerminalPrefillMetrics(t *testing.T) {
	a := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"metrics\":{\"prefill_engine_ms\":100,\"prompt_tokens_computed\":4096,\"prompt_tokens_cached\":0}}\n\ndata: [DONE]\n\n")
	b := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"metrics\":{\"prefill_engine_ms\":108,\"prompt_tokens_computed\":4096,\"prompt_tokens_cached\":0}}\n\ndata: [DONE]\n\n")
	out, err := mergePairedSSETerminalMetrics(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"pair_prefill_engine_ms_max":108`) || !strings.Contains(string(out), `data: [DONE]`) {
		t.Fatalf("bad terminal merge %s", out)
	}
}
