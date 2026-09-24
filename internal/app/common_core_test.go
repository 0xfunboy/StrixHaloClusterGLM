package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommonLifecycleAliasesRequireConfirmation(t *testing.T) {
	a, _ := lifecycleFixture(t)
	for _, prefix := range []string{"/v1/lifecycle", "/v1/model/lifecycle"} {
		for _, action := range []string{"on", "off"} {
			w := lifecycleReq(a, "POST", prefix+"/"+action, `{}`, true)
			if w.Code != 400 {
				t.Fatalf("confirmation missing: %s %s: %d", prefix, action, w.Code)
			}
		}
		w := lifecycleReq(a, "GET", prefix, "", true)
		if w.Code != 200 {
			t.Fatalf("status alias: %s: %d", prefix, w.Code)
		}
	}
}

func TestCommonDisabledProfileCannotStartOrInfer(t *testing.T) {
	a, _ := lifecycleFixture(t)
	enabled := false
	a.cfg.InferenceEnabled = &enabled
	if a.requireModelReady(context.Background()) == nil {
		t.Fatal("disabled inference admitted")
	}
	if a.startLifecycleAction("on") == nil {
		t.Fatal("disabled profile started")
	}
	for _, prefix := range []string{"/v1/lifecycle", "/v1/model/lifecycle"} {
		if w := lifecycleReq(a, "POST", prefix+"/on", `{"confirm":true}`, true); w.Code != 409 {
			t.Fatal(w.Code)
		}
	}
}

func TestCommonReasoningModesRemainProfileBound(t *testing.T) {
	a := &App{cfg: Config{ReasoningModes: []string{"low", "medium", "xhigh"}}}
	for _, mode := range []string{"low", "medium", "xhigh"} {
		if !a.supportsReasoning(mode) {
			t.Fatal(mode)
		}
	}
	for _, mode := range []string{"none", "high", "max", "unknown"} {
		if a.supportsReasoning(mode) {
			t.Fatal("unadvertised mode admitted", mode)
		}
	}
	if supportedReasoning("medium") || supportedReasoning("xhigh") {
		t.Fatal("legacy defaults were broadened")
	}
}

func TestCommonExternalControllerDoesNotConstructGLMDriver(t *testing.T) {
	c := Config{LifecycleCommand: "/fixed/controller", ClusterConfigPath: "runtime/cluster.ds41-k2.json"}
	native, err := newModelLifecycle(c)
	if err != nil || native != nil {
		t.Fatalf("external config entered GLM driver: %v", err)
	}
}

func TestCommonNativeLifecycleAliasesRejectUnauthorized(t *testing.T) {
	a := &App{token: strings.Repeat("a", 64), lifecycle: &modelLifecycle{}}
	mux := http.NewServeMux()
	a.registerLifecycleRoutes(mux)
	for _, path := range []string{"/v1/model/lifecycle/on", "/v1/lifecycle/on"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"confirm":true}`)))
		if w.Code != 401 {
			t.Fatal(path, w.Code)
		}
	}
}

func TestCommonExternalRelativeClusterConfigCompatibility(t *testing.T) {
	c := Config{Model: "fixture", StateDir: t.TempDir(), MaxFiles: 1, MaxRepoBytes: 1024, SandboxTasks: 8, ModelTimeout: 30, WorkspaceRoots: []string{t.TempDir()}, DefaultProfile: "xhigh", Profiles: map[string]Profile{"xhigh": {Reasoning: "xhigh"}}, ReasoningModes: []string{"low", "medium", "xhigh"}, LifecycleCommand: "/fixed/controller", LifecyclePreset: "fixture", ClusterConfigPath: "runtime/cluster.fixture.json"}
	b, _ := json.Marshal(c)
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(p); err != nil {
		t.Fatal(err)
	}
	c.LifecycleCommand = ""
	b, _ = json.Marshal(c)
	_ = os.WriteFile(p, b, 0600)
	if _, err := loadConfig(p); err == nil {
		t.Fatal("relative native config admitted")
	}
}
