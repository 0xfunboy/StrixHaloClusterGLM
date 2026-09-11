package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func runBenchmarkCLI(args []string) error {
	cmd := exec.Command("node", append([]string{"benchmarks/run.mjs"}, args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}
func clusterCLI(appCfg Config, args []string) error {
	if len(args) == 0 {
		return errors.New("cluster status|verify|start|stop|restart|snapshot-legacy PATH|stop-legacy PATH|recover-legacy PATH|restore-legacy PATH|serve-pair")
	}
	var cfg ClusterConfig
	b, e := os.ReadFile("runtime/cluster.json")
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, &cfg); e != nil {
		return e
	}
	c, e := NewClusterController(cfg)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	var result any
	switch args[0] {
	case "status":
		result, e = c.Status(ctx)
	case "verify":
		e = c.Verify(ctx)
	case "start":
		e = c.Start(ctx)
	case "stop":
		e = c.Stop(ctx)
	case "restart":
		e = c.Restart(ctx)
	case "snapshot-legacy", "stop-legacy", "recover-legacy", "restore-legacy":
		if len(args) != 2 {
			return errors.New("explicit snapshot JSON file required")
		}
		switch args[0] {
		case "snapshot-legacy":
			e = c.SnapshotLegacy(ctx, args[1])
		case "stop-legacy":
			e = c.StopLegacy(ctx, args[1])
		case "recover-legacy":
			e = c.RecoverLegacy(ctx, args[1])
		case "restore-legacy":
			e = c.RestoreLegacy(ctx, args[1])
		}
	case "serve-pair":
		return serveNativePair(c.Config, appCfg.ModelTimeout, appCfg.LifecycleDrainSeconds)
	default:
		return errors.New("unknown cluster command")
	}
	if e == nil {
		if result == nil {
			result = map[string]string{"status": "ok", "action": args[0]}
		}
		printJSON(result)
	}
	return e
}
func serveNativePair(cfg ClusterConfig, requestTimeout, drainSeconds int) error {
	b, e := NewPairedBackend(PairedConfig{RankURLs: cfg.RankURLs, Model: "GLM5.3-Flash-CIRU-STRIX-IU4", StateDir: cfg.StateDir, TimeoutSeconds: requestTimeout})
	if e != nil {
		return e
	}
	listener, e := net.Listen("tcp", cfg.FrontendListen)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	server := &http.Server{Addr: cfg.FrontendListen, Handler: b, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	if drainSeconds <= 0 {
		drainSeconds = 30
	}
	return serveNativePairUntil(ctx, server, b, listener, time.Duration(drainSeconds)*time.Second)
}

func serveNativePairUntil(ctx context.Context, server *http.Server, backend *PairedBackend, listener net.Listener, grace time.Duration) error {
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	select {
	case e := <-serveDone:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	case <-ctx.Done():
	}
	backend.stopAdmission()
	shutdown, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	httpDone := make(chan error, 1)
	go func() { httpDone <- server.Shutdown(shutdown) }()
	drainErr := backend.Shutdown(shutdown)
	httpErr := <-httpDone
	if drainErr != nil || httpErr != nil {
		backend.fail("native frontend shutdown deadline; whole-pair restart required")
		_ = server.Close()
		return errors.Join(drainErr, httpErr)
	}
	if e := <-serveDone; e != nil && !errors.Is(e, http.ErrServerClosed) {
		return e
	}
	return nil
}
