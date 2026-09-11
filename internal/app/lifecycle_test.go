package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func lifecycleFakeController(t *testing.T, states map[string]string) (*ClusterController, *int) {
	t.Helper()
	base := t.TempDir()
	calls := 0
	c := &ClusterController{Config: ClusterConfig{
		StateDir:       filepath.Join(base, "cluster"),
		LegacyOwner:    filepath.Join(base, "legacy", "pair-owner.json"),
		SharedStateDir: filepath.Join(base, "shared"),
		RankURLs:       [2]string{"http://10.55.0.1:18110", "http://10.55.0.2:18110"},
	}}
	c.run = func(_ context.Context, rank int, args ...string) ([]byte, error) {
		calls++
		if len(args) >= 4 && args[0] == "systemctl" && args[1] == "--user" && args[2] == "show" {
			unit := args[3]
			state := states[unit]
			if state == "UNKNOWN" {
				return nil, errors.New("peer unavailable")
			}
			if state == "ACTIVE" {
				return []byte("LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=123\nInvocationID=11111111111111111111111111111111\nControlGroup=\nEnvironment=CIRU_OWNER_NONCE=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"), nil
			}
			return []byte("LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\nInvocationID=\nControlGroup=\nEnvironment=\n"), nil
		}
		if len(args) >= 2 && args[0] == "cat" && args[1] == "/proc/meminfo" {
			return []byte("MemAvailable: 120000000 kB\nSwapFree: 8000000 kB\n"), nil
		}
		return nil, errors.New("unexpected controller call")
	}
	return c, &calls
}

func lifecycleFake(t *testing.T, states map[string]string) (*modelLifecycle, *int) {
	t.Helper()
	c, calls := lifecycleFakeController(t, states)
	m := &modelLifecycle{
		controller: c,
		clusterCfg: c.Config,
		appCfg: Config{
			LifecycleDrainSeconds: 30,
			Model:                 "GLM5.3-Flash-CIRU-STRIX-IU4",
			Backend:               "http://127.0.0.1:18094",
		},
	}
	m.runLocal = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "show strixglm-pair.service") {
			return []byte("LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0\nInvocationID=\n"), nil
		}
		return nil, errors.New("unexpected local call")
	}
	return m, calls
}

func TestLifecyclePeerUnknownIsNeverOff(t *testing.T) {
	states := map[string]string{
		"strixglm-rank0.service": "OFF",
		"strixglm-rank1.service": "OFF",
		"ds41-rank0.service":     "OFF",
		"ds41-rank1.service":     "UNKNOWN",
	}
	m, _ := lifecycleFake(t, states)
	if e := writeSharedCompute(m.clusterCfg.SharedStateDir, sharedOff("test")); e != nil {
		t.Fatal(e)
	}
	s := m.Snapshot(context.Background())
	if s.State != lifecycleError || s.StartAllowed {
		t.Fatalf("UNKNOWN peer must be ERROR/start-refused, got %+v", s)
	}
	if !strings.Contains(s.Detail, "UNKNOWN") {
		t.Fatalf("missing UNKNOWN detail: %+v", s)
	}
}

func TestLifecycleStaleSharedReceiptBlocksStart(t *testing.T) {
	states := map[string]string{
		"strixglm-rank0.service": "OFF",
		"strixglm-rank1.service": "OFF",
		"ds41-rank0.service":     "OFF",
		"ds41-rank1.service":     "OFF",
	}
	m, calls := lifecycleFake(t, states)
	stale := sharedComputeReceipt{Schema: sharedComputeSchema, Owner: "DS41", State: "RUNNING", Epoch: "old", Ranks: map[string]sharedComputeRank{}}
	if e := writeSharedCompute(m.clusterCfg.SharedStateDir, stale); e != nil {
		t.Fatal(e)
	}
	before := *calls
	m.start()
	_, _, _, lastErr, _ := m.operationState()
	if !strings.Contains(lastErr, "cluster not reconciled OFF") {
		t.Fatalf("unexpected error: %q", lastErr)
	}
	// Only the two DS41 state probes are allowed before the persistent gate.
	if *calls-before != 2 {
		t.Fatalf("unexpected calls past stale receipt gate: %d", *calls-before)
	}
}

func TestLifecycleDuplicateOperationRejected(t *testing.T) {
	m := &modelLifecycle{operation: "start", transient: lifecycleStarting}
	if m.StartAsync() || m.StopAsync() {
		t.Fatal("duplicate lifecycle operation was accepted")
	}
}

func TestLifecycleRoutesRejectUnauthorized(t *testing.T) {
	a := &App{token: strings.Repeat("a", 64), lifecycle: &modelLifecycle{}}
	mux := http.NewServeMux()
	a.registerLifecycleRoutes(mux)
	for _, tc := range []struct {
		method string
		path   string
		auth   string
	}{
		{http.MethodGet, "/v1/model/lifecycle", ""},
		{http.MethodPost, "/v1/model/lifecycle/on", "Bearer wrong"},
		{http.MethodPost, "/v1/model/lifecycle/off", ""},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: expected 401, got %d body=%s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestSharedReceiptRequiresExactEpochAndRankIdentity(t *testing.T) {
	o := ClusterOwner{Epoch: "42", Units: []OwnedUnit{{Rank: 0, Name: "strixglm-rank0", Nonce: strings.Repeat("a", 32), InvocationID: strings.Repeat("1", 32)}, {Rank: 1, Name: "strixglm-rank1", Nonce: strings.Repeat("b", 32), InvocationID: strings.Repeat("2", 32)}}}
	r := sharedFromGLM("RUNNING", o, "test")
	if e := sharedMatchesGLM(r, o); e != nil {
		t.Fatal(e)
	}
	r.Epoch = "41"
	if e := sharedMatchesGLM(r, o); e == nil {
		t.Fatal("stale epoch accepted")
	}
	r = sharedFromGLM("RUNNING", o, "test")
	x := r.Ranks["1"]
	x.InvocationID = strings.Repeat("3", 32)
	r.Ranks["1"] = x
	if e := sharedMatchesGLM(r, o); e == nil {
		t.Fatal("stale rank invocation accepted")
	}
}
