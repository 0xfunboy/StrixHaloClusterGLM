package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lifecycleFixture(t *testing.T) (*App, string) {
	t.Helper()
	base := t.TempDir()
	state := filepath.Join(base, "state.json")
	if err := os.WriteFile(state, []byte("{\"state\":\"OFF\",\"preset\":\"dspark-k2-gfx1151\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(base, "lifecycle")
	body := `#!/bin/sh
set -eu
state=` + state + `
case "$1" in
 status) cat "$state" ;;
 on) sleep "${LIFE_SLEEP:-0}"; printf '%s\n' '{"state":"READY","preset":"dspark-k2-gfx1151"}' >"$state" ;;
 off) sleep "${LIFE_SLEEP:-0}"; printf '%s\n' '{"state":"OFF","preset":"dspark-k2-gfx1151"}' >"$state" ;;
 *) exit 2 ;;
esac
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := coreConfig(t)
	cfg.LifecycleCommand = script
	cfg.LifecyclePreset = "dspark-k2-gfx1151"
	a, err := newApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a, state
}

func lifecycleReq(a *App, method, path, body string, auth bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth {
		req.Header.Set("Authorization", "Bearer "+a.currentToken())
	}
	w := httptest.NewRecorder()
	a.routes().ServeHTTP(w, req)
	return w
}

func waitLifecycle(t *testing.T, a *App, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := a.lifecycleStatus(t.Context())["state"].(string)
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("lifecycle never reached %s: %#v", want, a.lifecycleStatus(t.Context()))
}

func TestLifecycleAuthOffNoAutoloadAndExplicitOnOff(t *testing.T) {
	a, _ := lifecycleFixture(t)
	if w := lifecycleReq(a, "GET", "/v1/lifecycle", "", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth GET=%d", w.Code)
	}
	w := lifecycleReq(a, "GET", "/health", "", false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"gateway":"ok"`) || !strings.Contains(w.Body.String(), `"state":"OFF"`) {
		t.Fatalf("gateway health while model off: %d %s", w.Code, w.Body.String())
	}
	w = lifecycleReq(a, "GET", "/v1/lifecycle", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"OFF"`) {
		t.Fatalf("status=%d %s", w.Code, w.Body.String())
	}
	// Chat while OFF must fail before backend dispatch/autoload.
	w = lifecycleReq(a, "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`, true)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "model_not_ready") {
		t.Fatalf("OFF chat=%d %s", w.Code, w.Body.String())
	}
	w = lifecycleReq(a, "POST", "/v1/lifecycle/on", `{"confirm":true}`, true)
	if w.Code != 202 {
		t.Fatalf("ON=%d %s", w.Code, w.Body.String())
	}
	waitLifecycle(t, a, "READY")
	w = lifecycleReq(a, "POST", "/v1/lifecycle/off", `{"confirm":true}`, true)
	if w.Code != 202 {
		t.Fatalf("OFF=%d %s", w.Code, w.Body.String())
	}
	waitLifecycle(t, a, "OFF")
}

func TestLifecycleRequiresConfirmationAndRejectsConcurrentTransition(t *testing.T) {
	a, _ := lifecycleFixture(t)
	if w := lifecycleReq(a, "POST", "/v1/lifecycle/on", `{}`, true); w.Code != 400 {
		t.Fatalf("missing confirmation=%d", w.Code)
	}
	t.Setenv("LIFE_SLEEP", "0.3")
	w := lifecycleReq(a, "POST", "/v1/lifecycle/on", `{"confirm":true}`, true)
	if w.Code != 202 {
		t.Fatalf("first ON=%d %s", w.Code, w.Body.String())
	}
	w = lifecycleReq(a, "POST", "/v1/lifecycle/on", `{"confirm":true}`, true)
	if w.Code != 409 {
		t.Fatalf("concurrent ON should conflict: %d %s", w.Code, w.Body.String())
	}
	waitLifecycle(t, a, "READY")
}

func TestLifecycleStatusResponseContainsNoCredential(t *testing.T) {
	a, _ := lifecycleFixture(t)
	w := lifecycleReq(a, "GET", "/v1/lifecycle", "", true)
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), a.currentToken()) {
		t.Fatal("credential leaked in lifecycle response")
	}
	if v["preset"] != "dspark-k2-gfx1151" {
		t.Fatalf("wrong preset: %#v", v)
	}
}
