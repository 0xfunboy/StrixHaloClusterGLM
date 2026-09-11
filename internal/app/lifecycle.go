package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	lifecycleOff      = "OFF"
	lifecycleStarting = "STARTING"
	lifecycleReady    = "READY"
	lifecycleStopping = "STOPPING"
	lifecycleError    = "ERROR"
	presenceActive    = "ACTIVE"
	presenceOff       = "OFF_VERIFIED"
	presenceUnknown   = "UNKNOWN"
)

type lifecycleNode struct {
	Rank              int    `json:"rank"`
	Host              string `json:"host"`
	EngineState       string `json:"engine_state"`
	EnginePID         string `json:"engine_pid,omitempty"`
	InvocationID      string `json:"invocation_id,omitempty"`
	MemAvailableBytes int64  `json:"mem_available_bytes,omitempty"`
	SwapFreeBytes     int64  `json:"swap_free_bytes,omitempty"`
	Error             string `json:"error,omitempty"`
}

type lifecycleSnapshot struct {
	State         string          `json:"state"`
	Detail        string          `json:"detail,omitempty"`
	Operation     string          `json:"operation,omitempty"`
	ClusterOwner  string          `json:"cluster_owner,omitempty"`
	StartAllowed  bool            `json:"start_allowed"`
	DrainSeconds  int             `json:"drain_deadline_seconds"`
	Coordinator   string          `json:"coordinator"`
	Readiness     string          `json:"readiness"`
	Nodes         []lifecycleNode `json:"nodes"`
	Updated       string          `json:"updated"`
	LastError     string          `json:"last_error,omitempty"`
	LastReadiness string          `json:"last_readiness,omitempty"`
}

type modelLifecycle struct {
	mu         sync.Mutex
	controller *ClusterController
	clusterCfg ClusterConfig
	appCfg     Config
	client     *http.Client
	runLocal   func(context.Context, string, ...string) ([]byte, error)

	operation     string
	transient     string
	detail        string
	lastError     string
	lastReadiness string
}

func newModelLifecycle(appCfg Config) (*modelLifecycle, error) {
	if appCfg.ClusterConfigPath == "" {
		return nil, nil
	}
	b, e := os.ReadFile(appCfg.ClusterConfigPath)
	if e != nil {
		return nil, e
	}
	var cc ClusterConfig
	if e = json.Unmarshal(b, &cc); e != nil {
		return nil, e
	}
	if cc.SharedStateDir == "" {
		cc.SharedStateDir = appCfg.ClusterSharedStateDir
	}
	if appCfg.ClusterSharedStateDir == "" || cc.SharedStateDir != appCfg.ClusterSharedStateDir {
		return nil, errors.New("gateway/controller shared cluster state directories must match")
	}
	controller, e := NewClusterController(cc)
	if e != nil {
		return nil, e
	}
	m := &modelLifecycle{
		controller: controller,
		clusterCfg: cc,
		appCfg:     appCfg,
		client: &http.Client{Transport: &http.Transport{Proxy: nil, MaxIdleConns: 4, MaxIdleConnsPerHost: 2}, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
	m.runLocal = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		out, e := cmd.CombinedOutput()
		if e != nil {
			return out, fmt.Errorf("%s: %w: %.2000s", name, e, out)
		}
		return out, nil
	}
	return m, nil
}

func parseSystemdShow(raw []byte) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out
}

func lifecycleUnitRunning(v map[string]string) bool {
	return v["LoadState"] != "not-found" && v["MainPID"] != "0" && v["MainPID"] != "" && v["ActiveState"] == "active"
}

// unitPresence is deliberately fail-closed. A failed SSH/systemd query,
// incomplete output or unverifiable cgroup is UNKNOWN, never OFF.
func (m *modelLifecycle) unitPresence(ctx context.Context, rank int, unit string) (string, map[string]string, error) {
	b, e := m.controller.run(ctx, rank, "systemctl", "--user", "show", unit,
		"-p", "LoadState", "-p", "ActiveState", "-p", "SubState", "-p", "MainPID", "-p", "InvocationID", "-p", "ControlGroup", "-p", "Environment")
	if e != nil {
		return presenceUnknown, nil, e
	}
	v := parseSystemdShow(b)
	for _, key := range []string{"LoadState", "ActiveState", "MainPID", "ControlGroup"} {
		if _, ok := v[key]; !ok {
			return presenceUnknown, v, fmt.Errorf("incomplete %s state: missing %s", unit, key)
		}
	}
	pids := ""
	if cg := v["ControlGroup"]; cg != "" {
		cb, ce := m.controller.run(ctx, rank, "cat", filepath.Join("/sys/fs/cgroup", cg, "cgroup.procs"))
		if ce != nil {
			return presenceUnknown, v, fmt.Errorf("cannot verify %s cgroup: %w", unit, ce)
		}
		pids = strings.TrimSpace(string(cb))
	}
	if v["MainPID"] != "0" || v["ActiveState"] == "active" || v["ActiveState"] == "activating" || v["ActiveState"] == "deactivating" || pids != "" {
		return presenceActive, v, nil
	}
	if (v["ActiveState"] == "inactive" || v["ActiveState"] == "failed") && v["MainPID"] == "0" && pids == "" {
		return presenceOff, v, nil
	}
	return presenceUnknown, v, fmt.Errorf("ambiguous %s state", unit)
}

func parseMeminfo(raw []byte) (available, swapFree int64, e error) {
	values := map[string]int64{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		if key != "MemAvailable" && key != "SwapFree" {
			continue
		}
		v, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, 0, err
		}
		values[key] = v * 1024
	}
	if values["MemAvailable"] <= 0 {
		return 0, 0, errors.New("MemAvailable missing")
	}
	return values["MemAvailable"], values["SwapFree"], nil
}

func (m *modelLifecycle) nodeMemory(ctx context.Context, rank int) (int64, int64, error) {
	var b []byte
	var e error
	if rank == 0 {
		b, e = os.ReadFile("/proc/meminfo")
	} else {
		b, e = m.controller.run(ctx, 1, "cat", "/proc/meminfo")
	}
	if e != nil {
		return 0, 0, e
	}
	return parseMeminfo(b)
}

func (m *modelLifecycle) ds41State(ctx context.Context, rank int) (string, error) {
	state, _, e := m.unitPresence(ctx, rank, fmt.Sprintf("ds41-rank%d.service", rank))
	return state, e
}

func (m *modelLifecycle) pairState(ctx context.Context) (map[string]string, error) {
	b, e := m.runLocal(ctx, "/usr/bin/systemctl", "--user", "show", "strixglm-pair.service",
		"-p", "LoadState", "-p", "ActiveState", "-p", "SubState", "-p", "MainPID", "-p", "InvocationID")
	if e != nil {
		return nil, e
	}
	return parseSystemdShow(b), nil
}

func (m *modelLifecycle) backendHealth(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, m.appCfg.Backend+"/health", nil)
	resp, e := m.client.Do(req)
	if e != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
	return resp.StatusCode == http.StatusOK
}

func (m *modelLifecycle) readinessInference(ctx context.Context) (string, error) {
	payload := map[string]any{"model": m.appCfg.Model, "prompt": "1", "temperature": 0, "max_tokens": 1, "seed": 1, "stream": false}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, m.appCfg.Backend+"/v1/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, e := m.client.Do(req)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	var result struct {
		Choices []json.RawMessage `json:"choices"`
		Usage   map[string]any    `json:"usage"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); e != nil {
		return "", e
	}
	if resp.StatusCode != 200 || len(result.Choices) == 0 {
		return "", fmt.Errorf("readiness inference HTTP%d choices=%d", resp.StatusCode, len(result.Choices))
	}
	return fmt.Sprintf("HTTP200 choices=%d", len(result.Choices)), nil
}

func (m *modelLifecycle) operationState() (operation, transient, detail, lastError, readiness string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.operation, m.transient, m.detail, m.lastError, m.lastReadiness
}
func (m *modelLifecycle) setTransient(op, state, detail string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.operation != "" {
		return false
	}
	m.operation, m.transient, m.detail, m.lastError = op, state, detail, ""
	return true
}
func (m *modelLifecycle) setDetail(detail string) { m.mu.Lock(); m.detail = detail; m.mu.Unlock() }
func (m *modelLifecycle) finishOperation(detail, lastError, readiness string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.operation, m.transient, m.detail, m.lastError = "", "", detail, lastError
	if readiness != "" {
		m.lastReadiness = readiness
	}
}

func (m *modelLifecycle) ready() bool {
	op, transient, _, lastErr, _ := m.operationState()
	if op != "" || transient != "" || lastErr != "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, e := readSharedCompute(m.clusterCfg.SharedStateDir)
	if e != nil || r.Owner != "GLM" || r.State != "READY" {
		return false
	}
	p, e := m.pairState(ctx)
	return e == nil && lifecycleUnitRunning(p) && m.backendHealth(ctx)
}

func (m *modelLifecycle) Snapshot(ctx context.Context) lifecycleSnapshot {
	op, transient, detail, lastErr, readiness := m.operationState()
	s := lifecycleSnapshot{State: lifecycleError, Detail: detail, Operation: op, DrainSeconds: m.appCfg.LifecycleDrainSeconds, Coordinator: "OFF", Readiness: "NOT_READY", Updated: time.Now().UTC().Format(time.RFC3339Nano), LastError: lastErr, LastReadiness: readiness}
	if transient != "" {
		s.State = transient
	}

	shared, sharedErr := readSharedCompute(m.clusterCfg.SharedStateDir)
	if sharedErr != nil {
		s.ClusterOwner = "UNKNOWN"
		s.LastError = "shared cluster receipt unavailable: " + sharedErr.Error()
	} else {
		s.ClusterOwner = shared.Owner
	}
	pair, pairErr := m.pairState(ctx)
	if pairErr == nil && lifecycleUnitRunning(pair) {
		s.Coordinator = "RUNNING"
	}

	owner, ownerErr := m.controller.readOwner()
	ownerUnits := map[int]OwnedUnit{}
	if ownerErr == nil {
		for _, u := range owner.Units {
			ownerUnits[u.Rank] = u
		}
	}
	allEngineOff := true
	allEngineReady := ownerErr == nil && owner.State == "ready"
	anyUnknown := false
	peerUnknown := false
	for rank := 0; rank < 2; rank++ {
		n := lifecycleNode{Rank: rank, Host: fmt.Sprintf("0%d-EVO-X3", rank+1), EngineState: presenceUnknown}
		var presence string
		var raw map[string]string
		var e error
		if rank == 1 && peerUnknown {
			presence, e = presenceUnknown, errors.New("NODE02 unavailable earlier in this lifecycle snapshot")
		} else {
			presence, raw, e = m.unitPresence(ctx, rank, fmt.Sprintf("strixglm-rank%d.service", rank))
			if rank == 1 && e != nil {
				peerUnknown = true
			}
		}
		n.EngineState = presence
		if raw != nil {
			n.EnginePID, n.InvocationID = raw["MainPID"], raw["InvocationID"]
		}
		if e != nil {
			n.Error = e.Error()
		}
		if presence == presenceUnknown {
			anyUnknown = true
		}
		allEngineOff = allEngineOff && presence == presenceOff
		if presence == presenceActive {
			u, ok := ownerUnits[rank]
			if !ok || raw == nil || verifyUnit(u, raw) != nil {
				allEngineReady = false
				if n.Error == "" {
					n.Error = "active GLM unit does not match current owner receipt"
				}
			} else {
				allEngineReady = allEngineReady && true
			}
		} else {
			allEngineReady = false
		}
		if rank == 0 || !peerUnknown {
			if mem, swap, me := m.nodeMemory(ctx, rank); me == nil {
				n.MemAvailableBytes, n.SwapFreeBytes = mem, swap
			} else if n.Error == "" {
				n.Error = me.Error()
				if rank == 1 {
					peerUnknown = true
				}
			}
		}
		s.Nodes = append(s.Nodes, n)
	}

	ds0, e0 := m.ds41State(ctx, 0)
	ds1, e1 := presenceUnknown, errors.New("NODE02 unavailable earlier in this lifecycle snapshot")
	if !peerUnknown {
		ds1, e1 = m.ds41State(ctx, 1)
	}
	if e0 != nil || e1 != nil || ds0 == presenceUnknown || ds1 == presenceUnknown {
		anyUnknown = true
	}
	foreignActive := ds0 == presenceActive || ds1 == presenceActive
	if foreignActive {
		s.ClusterOwner = "DS41"
	}
	if anyUnknown {
		if transient == "" {
			s.State = lifecycleError
		}
		s.Detail = "cluster peer or model ownership is UNKNOWN; start/false-OFF refused"
		return s
	}
	if sharedErr != nil {
		return s
	}
	if transient != "" {
		return s
	}

	if foreignActive {
		if shared.Owner != "DS41" || allEngineOff == false {
			s.State = lifecycleError
			s.Detail = "DS41 is active but persistent shared ownership is inconsistent"
			return s
		}
		s.State = lifecycleOff
		s.Detail = "GLM OFF; cluster occupied by DS41"
		return s
	}
	if shared.State == "UNRECONCILED" {
		s.State = lifecycleError
		s.Detail = "cluster ownership is UNRECONCILED; verify both nodes before any start"
		return s
	}
	backendReady := pairErr == nil && lifecycleUnitRunning(pair) && m.backendHealth(ctx)
	if allEngineReady && backendReady && shared.Owner == "GLM" && shared.State == "READY" && sharedMatchesGLM(shared, owner) == nil {
		s.State, s.Readiness, s.StartAllowed = lifecycleReady, "HEALTHY+READINESS_INFERENCE_PASSED", false
		return s
	}
	if allEngineOff && (pairErr != nil || !lifecycleUnitRunning(pair)) && shared.Owner == "NONE" && shared.State == "OFF" {
		s.State, s.StartAllowed = lifecycleOff, true
		s.Detail = "GLM inference unloaded; gateway remains available"
		return s
	}
	if shared.Owner == "GLM" && (shared.State == "STARTING" || shared.State == "RUNNING") {
		s.State = lifecycleStarting
		s.Detail = "GLM ranks/coordinator are not yet fully READY"
		return s
	}
	s.State = lifecycleError
	if s.LastError == "" {
		s.LastError = fmt.Sprintf("partial lifecycle state: shared=%s/%s coordinator=%s", shared.Owner, shared.State, s.Coordinator)
	}
	return s
}

func (m *modelLifecycle) StartAsync() bool {
	if !m.setTransient("start", lifecycleStarting, "validating DS41 and persistent cluster ownership") {
		return false
	}
	go m.start()
	return true
}
func (m *modelLifecycle) start() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.clusterCfg.LoadTimeoutSeconds+120)*time.Second)
	defer cancel()
	for rank := 0; rank < 2; rank++ {
		state, e := m.ds41State(ctx, rank)
		if e != nil {
			m.finishOperation(fmt.Sprintf("GLM start refused: DS41 rank%d=%s", rank, state), e.Error(), "")
			return
		}
		if state != presenceOff {
			m.finishOperation(fmt.Sprintf("GLM start refused: DS41 rank%d=%s", rank, state), "DS41 peer is not OFF_VERIFIED", "")
			return
		}
	}
	shared, e := readSharedCompute(m.clusterCfg.SharedStateDir)
	if e != nil {
		m.finishOperation("GLM start refused: shared cluster receipt unavailable", e.Error(), "")
		return
	}
	if gateErr := sharedStartAllowed(shared); gateErr != nil {
		m.finishOperation("GLM start refused: shared cluster state is not reconciled OFF", gateErr.Error(), "")
		return
	}
	m.setDetail("loading both GLM ranks")
	if e = m.controller.Start(ctx); e != nil {
		m.finishOperation("whole-pair load failed", e.Error(), "")
		return
	}
	m.setDetail("starting paired coordinator")
	if _, e = m.runLocal(ctx, "/usr/bin/systemctl", "--user", "start", "strixglm-pair.service"); e != nil {
		_ = m.controller.Stop(context.Background())
		m.finishOperation("coordinator start failed; GLM unloaded", e.Error(), "")
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for !m.backendHealth(ctx) && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	if !m.backendHealth(ctx) {
		_ = m.stopPair(context.Background())
		_ = m.controller.Stop(context.Background())
		m.finishOperation("coordinator health did not become ready; GLM unloaded", "backend health timeout", "")
		return
	}
	m.setDetail("running minimal paired readiness inference")
	readyCtx, readyCancel := context.WithTimeout(ctx, 90*time.Second)
	receipt, e := m.readinessInference(readyCtx)
	readyCancel()
	if e != nil {
		_ = m.stopPair(context.Background())
		_ = m.controller.Stop(context.Background())
		m.finishOperation("readiness inference failed; GLM unloaded", e.Error(), "")
		return
	}
	o, e := m.controller.readOwner()
	if e != nil {
		m.finishOperation("readiness passed but controller owner receipt is unavailable", e.Error(), "")
		return
	}
	if e = withControllerLock(sharedComputeLockPath(m.clusterCfg.SharedStateDir), func() error {
		cur, re := readSharedCompute(m.clusterCfg.SharedStateDir)
		if re != nil {
			return re
		}
		if re = sharedMatchesGLM(cur, o); re != nil {
			return re
		}
		return writeSharedCompute(m.clusterCfg.SharedStateDir, sharedFromGLM("READY", o, "both ranks, coordinator health and minimal readiness inference passed"))
	}); e != nil {
		m.finishOperation("readiness passed but shared READY receipt failed", e.Error(), "")
		return
	}
	m.finishOperation("both ranks and coordinator READY", "", receipt)
}

func (m *modelLifecycle) StopAsync() bool {
	if !m.setTransient("stop", lifecycleStopping, fmt.Sprintf("blocking new inference and draining coordinator for up to %ds", m.appCfg.LifecycleDrainSeconds)) {
		return false
	}
	go m.stop()
	return true
}
func (m *modelLifecycle) stopPair(ctx context.Context) error {
	_, e := m.runLocal(ctx, "/usr/bin/systemctl", "--user", "stop", "strixglm-pair.service")
	return e
}
func (m *modelLifecycle) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	snap := m.Snapshot(ctx)
	cancel()
	if snap.State == lifecycleOff && snap.Coordinator == "OFF" {
		m.finishOperation("GLM already OFF; no model process touched", "", "")
		return
	}
	pairCtx, pairCancel := context.WithTimeout(context.Background(), time.Duration(m.appCfg.LifecycleDrainSeconds+20)*time.Second)
	pairErr := m.stopPair(pairCtx)
	pairCancel()
	m.setDetail("stopping and verifying both owned GLM ranks")
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 120*time.Second)
	stopErr := m.controller.Stop(stopCtx)
	stopCancel()
	if stopErr != nil {
		m.finishOperation("GLM stop is partial/UNKNOWN; no false OFF", stopErr.Error(), "")
		return
	}
	pair, e := m.pairState(context.Background())
	if e == nil && lifecycleUnitRunning(pair) {
		m.finishOperation("coordinator still active after rank stop", "coordinator stop not proven", "")
		return
	}
	if pairErr != nil {
		m.finishOperation("GLM ranks verified OFF after coordinator drain deadline/cancellation", pairErr.Error(), "")
		return
	}
	m.finishOperation("GLM OFF; coordinator and both ranks verified stopped", "", "")
}

func (a *App) registerLifecycleRoutes(mux *http.ServeMux) {
	if a.lifecycle == nil {
		return
	}
	mux.HandleFunc("GET /v1/model/lifecycle", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		jsonReply(w, 200, a.lifecycle.Snapshot(ctx))
	})
	mux.HandleFunc("POST /v1/model/lifecycle/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(w, r) {
			return
		}
		var accepted bool
		switch r.PathValue("action") {
		case "on":
			accepted = a.lifecycle.StartAsync()
		case "off":
			accepted = a.lifecycle.StopAsync()
		default:
			jsonReply(w, 404, map[string]string{"error": "unknown lifecycle action"})
			return
		}
		if !accepted {
			jsonReply(w, 409, map[string]any{"error": "another lifecycle operation is already running", "lifecycle": a.lifecycle.Snapshot(r.Context())})
			return
		}
		jsonReply(w, http.StatusAccepted, a.lifecycle.Snapshot(r.Context()))
	})
}
