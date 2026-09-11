package app

// Linux/systemd controller for one indivisible TP2 pair. No rank-specific public
// operation exists. Engine math and checkpoints are external pinned assets.
import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type ClusterConfig struct {
	StateDir           string    `json:"state_dir"`
	EngineRoot         string    `json:"engine_root"`
	Launcher           string    `json:"launcher"`
	Manifest           string    `json:"manifest"`
	RemoteSSH          string    `json:"remote_ssh"`
	RankURLs           [2]string `json:"rank_urls"`
	MasterPort         int       `json:"master_port"`
	FrontendListen     string    `json:"frontend_listen"`
	LegacyOwner        string    `json:"legacy_owner"`
	LoadTimeoutSeconds int       `json:"load_timeout_seconds"`
	SharedStateDir     string    `json:"shared_state_dir,omitempty"`
}
type OwnedUnit struct {
	Rank         int    `json:"rank"`
	Name         string `json:"name"`
	Nonce        string `json:"nonce"`
	InvocationID string `json:"invocation_id"`
}
type ClusterOwner struct {
	Schema  string      `json:"schema"`
	State   string      `json:"state"`
	Epoch   string      `json:"epoch"`
	Units   []OwnedUnit `json:"units"`
	Error   string      `json:"error,omitempty"`
	Updated string      `json:"updated"`
}
type ClusterController struct {
	Config ClusterConfig
	run    func(context.Context, int, ...string) ([]byte, error)
}

func NewClusterController(cfg ClusterConfig) (*ClusterController, error) {
	if cfg.RemoteSSH == "" {
		cfg.RemoteSSH = "02-evo-x3-tb"
	}
	if cfg.MasterPort == 0 {
		cfg.MasterPort = 29643
	}
	if cfg.FrontendListen == "" {
		cfg.FrontendListen = "127.0.0.1:18094"
	}
	if cfg.LoadTimeoutSeconds == 0 {
		cfg.LoadTimeoutSeconds = 1200
	}
	listenHost, listenPort, listenErr := net.SplitHostPort(cfg.FrontendListen)
	listenNumber, portErr := strconv.Atoi(listenPort)
	if listenErr != nil || portErr != nil || (listenHost != "127.0.0.1" && listenHost != "::1") || listenNumber < 1024 || listenNumber > 65535 || listenNumber == 18091 || listenNumber == 18092 {
		return nil, errors.New("native frontend requires a new loopback port")
	}
	if cfg.RankURLs[0] == "" {
		cfg.RankURLs = [2]string{"http://10.55.0.1:18110", "http://10.55.0.2:18110"}
	}
	for _, p := range []string{cfg.StateDir, cfg.EngineRoot, cfg.Launcher, cfg.Manifest, cfg.LegacyOwner} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return nil, errors.New("controller paths must be absolute, clean and specific")
		}
	}
	if strings.HasPrefix(cfg.RemoteSSH, "-") || strings.ContainsAny(cfg.RemoteSSH, " \t\r\n") {
		return nil, errors.New("invalid SSH host")
	}
	if cfg.MasterPort < 1024 || cfg.MasterPort > 65535 || cfg.LoadTimeoutSeconds < 1 || cfg.LoadTimeoutSeconds > 1800 {
		return nil, errors.New("invalid controller limits")
	}
	if cfg.SharedStateDir != "" && (!filepath.IsAbs(cfg.SharedStateDir) || filepath.Clean(cfg.SharedStateDir) != cfg.SharedStateDir || cfg.SharedStateDir == "/") {
		return nil, errors.New("shared_state_dir must be an absolute clean specific path")
	}
	for rank, raw := range cfg.RankURLs {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "http" || u.Hostname() != fmt.Sprintf("10.55.0.%d", rank+1) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("rank URLs must use fixed USB4 hosts")
		}
		p, e := strconv.Atoi(u.Port())
		if e != nil || p < 1024 || p > 65535 || p == 18100 {
			return nil, errors.New("new rank ports must be distinct from legacy18100")
		}
	}
	c := &ClusterController{Config: cfg}
	c.run = c.command
	return c, nil
}
func shellArg(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (c *ClusterController) command(ctx context.Context, rank int, args ...string) ([]byte, error) {
	if rank != 0 && rank != 1 {
		return nil, errors.New("invalid rank")
	}
	if len(args) == 0 {
		return nil, errors.New("empty command")
	}
	var cmd *exec.Cmd
	if rank == 1 {
		quoted := make([]string, len(args))
		for i, s := range args {
			quoted[i] = shellArg(s)
		}
		cmd = exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", c.Config.RemoteSSH, strings.Join(quoted, " "))
	} else {
		cmd = exec.CommandContext(ctx, args[0], args[1:]...)
	}
	out, e := cmd.CombinedOutput()
	if e != nil {
		return out, fmt.Errorf("rank%d %s: %w: %.2000s", rank, args[0], e, out)
	}
	return out, nil
}
func controllerWrite(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return controllerBytes(path, append(b, '\n'))
}
func controllerBytes(path string, b []byte) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".state-")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e == nil {
		e = os.Rename(name, path)
	}
	if e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// withControllerLock is also used by paired HTTP admission. LOCK_NB means no
// management operation can interrupt an in-flight generation silently.
func withControllerLock(path string, fn func() error) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return fmt.Errorf("pair busy/locked: %w", e)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
func (c *ClusterController) locked(fn func() error) error {
	lockedPair := func() error {
		return withControllerLock(filepath.Join(c.Config.StateDir, "pair.lock"), func() error {
			return withControllerLock(filepath.Join(filepath.Dir(c.Config.LegacyOwner), "pair.lock"), fn)
		})
	}
	if c.Config.SharedStateDir == "" {
		return lockedPair()
	}
	// Cross-model lifecycle mutations serialize on the same fixed control lock
	// used by DS41. Model processes never hold this flock; persistent owner.json
	// is what keeps a crash/unknown peer from becoming a false OFF state.
	return withControllerLock(sharedComputeLockPath(c.Config.SharedStateDir), lockedPair)
}
func (c *ClusterController) ownerPath() string {
	return filepath.Join(c.Config.StateDir, "pair-owner.json")
}
func (c *ClusterController) readOwner() (ClusterOwner, error) {
	var o ClusterOwner
	b, e := os.ReadFile(c.ownerPath())
	if e == nil {
		e = json.Unmarshal(b, &o)
	}
	return o, e
}
func (c *ClusterController) save(o *ClusterOwner) error {
	o.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	return controllerWrite(c.ownerPath(), o)
}
func newNonce() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func (c *ClusterController) unit(ctx context.Context, u OwnedUnit) (map[string]string, error) {
	b, e := c.run(ctx, u.Rank, "systemctl", "--user", "show", u.Name, "-p", "ActiveState", "-p", "SubState", "-p", "MainPID", "-p", "InvocationID", "-p", "Environment", "-p", "LoadState")
	if e != nil {
		return nil, e
	}
	m := map[string]string{}
	for _, s := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(s, "=")
		if ok {
			m[k] = v
		}
	}
	if m["ActiveState"] == "" || m["MainPID"] == "" {
		return nil, errors.New("incomplete unit state")
	}
	return m, nil
}
func unitActive(m map[string]string) bool {
	return m["MainPID"] != "0" || (m["ActiveState"] != "inactive" && m["ActiveState"] != "failed")
}

var controllerHex = regexp.MustCompile(`^[0-9a-f]{32}$`)

func verifyUnit(u OwnedUnit, m map[string]string) error {
	if !unitActive(m) {
		return nil
	}
	if !controllerHex.MatchString(u.Nonce) || !controllerHex.MatchString(m["InvocationID"]) {
		return errors.New("missing ownership nonce/InvocationID")
	}
	found := false
	for _, s := range strings.Fields(m["Environment"]) {
		if strings.Trim(s, "\"") == "CIRU_OWNER_NONCE="+u.Nonce {
			found = true
		}
	}
	if !found || (u.InvocationID != "" && m["InvocationID"] != u.InvocationID) {
		return fmt.Errorf("foreign or replaced unit: %s", u.Name)
	}
	return nil
}
func validUnits(us []OwnedUnit, legacy bool) error {
	seen := map[string]bool{}
	for _, u := range us {
		want := fmt.Sprintf("strixglm-rank%d", u.Rank)
		if legacy {
			want = fmt.Sprintf("ciru-model-rank%d-001", u.Rank)
			if u.Name == "ciru-frontend-001" && u.Rank == 0 {
				want = u.Name
			}
		}
		if u.Rank < 0 || u.Rank > 1 || u.Name != want || seen[u.Name] || !controllerHex.MatchString(u.Nonce) {
			return errors.New("invalid owned unit binding")
		}
		seen[u.Name] = true
	}
	return nil
}
func (c *ClusterController) inspectAll(ctx context.Context, us []OwnedUnit, legacy bool) error {
	if e := validUnits(us, legacy); e != nil {
		return e
	}
	for _, u := range us {
		m, e := c.unit(ctx, u)
		if e != nil {
			return e
		}
		if e = verifyUnit(u, m); e != nil {
			return e
		}
	}
	return nil
}
func (c *ClusterController) stopUnits(ctx context.Context, us []OwnedUnit, legacy bool) error {
	// Validate the WHOLE set before the first mutation; unknown peer => stop none.
	if e := c.inspectAll(ctx, us, legacy); e != nil {
		return e
	}
	for _, u := range us {
		if u.Name == "ciru-frontend-001" {
			m, e := c.unit(ctx, u)
			if e != nil {
				return e
			}
			if e = verifyUnit(u, m); e != nil {
				return e
			}
			if unitActive(m) {
				if _, e = c.run(ctx, 0, "systemctl", "--user", "stop", u.Name); e != nil {
					return e
				}
			}
		}
	}
	if e := c.inspectAll(ctx, us, legacy); e != nil {
		return e
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(us))
	for _, u := range us {
		if u.Name == "ciru-frontend-001" {
			continue
		}
		wg.Add(1)
		go func(u OwnedUnit) {
			defer wg.Done()
			m, e := c.unit(ctx, u)
			if e == nil {
				e = verifyUnit(u, m)
			}
			if e == nil && unitActive(m) {
				_, e = c.run(ctx, u.Rank, "systemctl", "--user", "stop", u.Name)
			}
			if e != nil {
				errs <- e
			}
		}(u)
	}
	wg.Wait()
	close(errs)
	var all []error
	for e := range errs {
		all = append(all, e)
	}
	for _, u := range us {
		m, e := c.unit(ctx, u)
		if e != nil {
			all = append(all, e)
		} else if unitActive(m) {
			all = append(all, fmt.Errorf("not proven stopped: %s", u.Name))
		}
	}
	return errors.Join(all...)
}
func (c *ClusterController) archivePoison() error {
	p := filepath.Join(c.Config.StateDir, "pair-poison.json")
	if _, e := os.Lstat(p); os.IsNotExist(e) {
		return nil
	}
	return os.Rename(p, p+".recovered-"+strconv.FormatInt(time.Now().UnixNano(), 10))
}
func (c *ClusterController) stop(ctx context.Context) error {
	o, e := c.readOwner()
	if e != nil {
		return e
	}
	sharedOwned := false
	sharedValid := false
	if c.Config.SharedStateDir != "" {
		if r, se := readSharedCompute(c.Config.SharedStateDir); se == nil {
			sharedOwned = r.Owner == "GLM"
			if sharedOwned && sharedMatchesGLM(r, o) == nil {
				sharedValid = true
				_ = writeSharedCompute(c.Config.SharedStateDir, sharedFromGLM("STOPPING", o, "stopping both owned GLM ranks"))
			}
		}
	}
	if e = c.stopUnits(ctx, o.Units, false); e != nil {
		o.State = "cleanup_uncertain"
		o.Error = e.Error()
		_ = c.save(&o)
		if c.Config.SharedStateDir != "" && sharedOwned {
			_ = writeSharedCompute(c.Config.SharedStateDir, sharedFromGLM("UNRECONCILED", o, "GLM stop failed or peer became unverifiable: "+e.Error()))
		}
		return e
	}
	o.State = "stopped"
	o.Error = ""
	if e = c.save(&o); e != nil {
		return e
	}
	if c.Config.SharedStateDir != "" && sharedOwned {
		if sharedValid {
			if e = writeSharedCompute(c.Config.SharedStateDir, sharedOff("both GLM ranks verified stopped")); e != nil {
				return e
			}
		} else {
			if e = writeSharedCompute(c.Config.SharedStateDir, sharedFromGLM("UNRECONCILED", o, "GLM ranks stopped but shared owner identity did not match current pair")); e != nil {
				return e
			}
			return errors.New("GLM ranks stopped but shared cluster ownership remains unreconciled")
		}
	}
	return c.archivePoison()
}
func (c *ClusterController) Stop(ctx context.Context) error {
	return c.locked(func() error { return c.stop(ctx) })
}
func (c *ClusterController) Restart(ctx context.Context) error {
	return c.locked(func() error {
		if e := c.stop(ctx); e != nil {
			return e
		}
		return c.start(ctx)
	})
}
func (c *ClusterController) Start(ctx context.Context) error {
	return c.locked(func() error { return c.start(ctx) })
}

func (c *ClusterController) Verify(ctx context.Context) error {
	var m struct {
		Identity map[string]map[string]string `json:"identity"`
		Weights  []struct {
			Rank any    `json:"rank"`
			Path string `json:"path"`
			Size int64  `json:"size"`
		} `json:"weights"`
	}
	b, e := os.ReadFile(c.Config.Manifest)
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, &m); e != nil {
		return e
	}
	if len(m.Weights) != 23 || len(m.Identity) != 2 {
		return errors.New("unexpected runtime manifest shape")
	}
	for rank := 0; rank < 2; rank++ {
		ids := m.Identity[fmt.Sprintf("rank%d", rank)]
		if len(ids) < 10 {
			return errors.New("missing runtime identity")
		}
		keys := make([]string, 0, len(ids))
		for p := range ids {
			if filepath.IsAbs(p) || strings.Contains(p, "..") {
				return errors.New("invalid manifest path")
			}
			keys = append(keys, p)
		}
		sort.Strings(keys)
		args := []string{"sha256sum", "--"}
		for _, p := range keys {
			args = append(args, filepath.Join(c.Config.EngineRoot, p))
		}
		out, e := c.run(ctx, rank, args...)
		if e != nil {
			return e
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) != len(keys) {
			return errors.New("incomplete runtime hash output")
		}
		for i, p := range keys {
			if lines[i] != ids[p]+"  "+filepath.Join(c.Config.EngineRoot, p) {
				return fmt.Errorf("runtime identity mismatch rank%d %s", rank, p)
			}
		}
		for _, w := range m.Weights {
			shared, ok := w.Rank.(string)
			r, rnum := w.Rank.(float64)
			if !(ok && shared == "shared") && !(rnum && int(r) == rank) {
				continue
			}
			if filepath.IsAbs(w.Path) || strings.Contains(w.Path, "..") {
				return errors.New("invalid weight path")
			}
			out, e = c.run(ctx, rank, "stat", "--printf=%s", "--", filepath.Join(c.Config.EngineRoot, w.Path))
			if e != nil {
				return e
			}
			if string(out) != strconv.FormatInt(w.Size, 10) {
				return fmt.Errorf("checkpoint size mismatch: %s", w.Path)
			}
		}
	}
	return nil
}
func (c *ClusterController) start(ctx context.Context) (ret error) {
	if c.Config.SharedStateDir != "" {
		r, e := readSharedCompute(c.Config.SharedStateDir)
		if e != nil {
			return fmt.Errorf("shared cluster receipt unavailable; explicit reconciliation required: %w", e)
		}
		if e = sharedStartAllowed(r); e != nil {
			return e
		}
	}
	if o, e := c.readOwner(); e == nil && o.State != "stopped" {
		return errors.New("previous owned pair must be explicitly stopped")
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	if _, e := os.Lstat(filepath.Join(c.Config.StateDir, "pair-poison.json")); !os.IsNotExist(e) {
		return errors.New("poison marker requires whole-pair stop before start")
	}
	for _, u := range []OwnedUnit{{Rank: 0, Name: "ciru-model-rank0-001"}, {Rank: 1, Name: "ciru-model-rank1-001"}, {Rank: 0, Name: "ciru-frontend-001"}, {Rank: 0, Name: "strixglm-rank0"}, {Rank: 1, Name: "strixglm-rank1"}} {
		m, e := c.unit(ctx, u)
		if e != nil {
			return e
		}
		if unitActive(m) {
			return fmt.Errorf("existing service active; no automatic takeover: %s", u.Name)
		}
	}
	if e := c.Verify(ctx); e != nil {
		return e
	}
	launcherHashes := [2]string{}
	for rank := 0; rank < 2; rank++ {
		lh, e := c.run(ctx, rank, "sha256sum", "--", c.Config.Launcher)
		if e != nil {
			return e
		}
		launcherHashes[rank] = string(lh)
		if rank == 1 && launcherHashes[0] != launcherHashes[1] {
			return errors.New("two-node launcher identities differ")
		}
		listeners, e := c.run(ctx, rank, "ss", "-H", "-ltn")
		if e != nil {
			return e
		}
		ru, _ := url.Parse(c.Config.RankURLs[rank])
		for _, line := range strings.Split(string(listeners), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				address := fields[3]
				at := strings.LastIndex(address, ":")
				if at >= 0 && (address[at+1:] == ru.Port() || address[at+1:] == strconv.Itoa(c.Config.MasterPort)) {
					return fmt.Errorf("rank%d reserved port already busy: %s", rank, address)
				}
			}
		}
		b, e := c.run(ctx, rank, "cat", "/proc/meminfo")
		if e != nil {
			return e
		}
		available := int64(0)
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "MemAvailable:") {
				f := strings.Fields(l)
				if len(f) > 1 {
					available, _ = strconv.ParseInt(f[1], 10, 64)
				}
			}
		}
		if available < 104*1024*1024 {
			return fmt.Errorf("rank%d less than104GiB available", rank)
		}
		if _, e = c.run(ctx, rank, "mkdir", "-p", filepath.Join(c.Config.StateDir, "logs"), filepath.Join(c.Config.StateDir, "settings")); e != nil {
			return e
		}
	}
	o := ClusterOwner{Schema: "strixglm-pair-v1", State: "starting", Epoch: strconv.FormatInt(time.Now().UnixMilli(), 10), Units: []OwnedUnit{}}
	if e := c.save(&o); e != nil {
		return e
	}
	if c.Config.SharedStateDir != "" {
		if e := writeSharedCompute(c.Config.SharedStateDir, sharedFromGLM("STARTING", o, "GLM whole-pair start reserved; no rank identity confirmed yet")); e != nil {
			return e
		}
	}
	defer func() {
		if ret != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if e := c.stopUnits(cleanupCtx, o.Units, false); e != nil {
				o.State = "cleanup_uncertain"
				ret = errors.Join(ret, e)
				if c.Config.SharedStateDir != "" {
					_ = writeSharedCompute(c.Config.SharedStateDir, sharedFromGLM("UNRECONCILED", o, "GLM start failed and cleanup could not be fully verified: "+e.Error()))
				}
			} else {
				o.State = "stopped"
				if c.Config.SharedStateDir != "" {
					_ = writeSharedCompute(c.Config.SharedStateDir, sharedOff("GLM start failed; all launched GLM ranks verified stopped"))
				}
			}
			o.Error = ret.Error()
			_ = c.save(&o)
		}
	}()
	for _, rank := range []int{0, 1} {
		u := OwnedUnit{Rank: rank, Name: fmt.Sprintf("strixglm-rank%d", rank), Nonce: newNonce()}
		o.Units = append(o.Units, u)
		if e := c.save(&o); e != nil {
			return e
		}
		parsed, _ := url.Parse(c.Config.RankURLs[rank])
		args := []string{"bash", c.Config.Launcher, c.Config.EngineRoot, strconv.Itoa(rank), o.Epoch, parsed.Port(), strconv.Itoa(c.Config.MasterPort), filepath.Join(c.Config.StateDir, "settings")}
		if e := c.launch(ctx, &o.Units[len(o.Units)-1], args, filepath.Join(c.Config.StateDir, "logs", o.Epoch+fmt.Sprintf("-rank%d.log", rank)), "124554051584", nil); e != nil {
			return e
		}
		if e := c.save(&o); e != nil {
			return e
		}
		if c.Config.SharedStateDir != "" {
			if e := writeSharedCompute(c.Config.SharedStateDir, sharedFromGLM("STARTING", o, fmt.Sprintf("GLM rank%d identity confirmed", rank))); e != nil {
				return e
			}
		}
	}
	if e := c.waitHealth(ctx, c.Config.RankURLs, o.Units); e != nil {
		return e
	}
	o.State = "ready"
	if e := c.save(&o); e != nil {
		return e
	}
	if c.Config.SharedStateDir != "" {
		if e := writeSharedCompute(c.Config.SharedStateDir, sharedFromGLM("RUNNING", o, "both GLM ranks healthy and identity-confirmed; coordinator/readiness pending")); e != nil {
			return e
		}
	}
	return nil
}
func (c *ClusterController) launch(ctx context.Context, u *OwnedUnit, args []string, log, memory string, extra map[string]string) error {
	// Reservation is written by caller before invoking this potentially ambiguous write.
	m, e := c.unit(ctx, *u)
	if e != nil {
		return e
	}
	if unitActive(m) {
		return fmt.Errorf("unit already active: %s", u.Name)
	}
	if m["ActiveState"] == "failed" {
		if _, e = c.run(ctx, u.Rank, "systemctl", "--user", "reset-failed", u.Name); e != nil {
			return e
		}
	}
	argv := []string{"systemd-run", "--user", "--unit=" + u.Name, "--setenv=CIRU_OWNER_NONCE=" + u.Nonce, "--property=RuntimeMaxSec=infinity", "--property=KillMode=control-group", "--property=TimeoutStopSec=20", "--property=MemorySwapMax=0", "--property=MemoryMax=" + memory, "--property=StandardOutput=append:" + log, "--property=StandardError=inherit"}
	for k, v := range extra {
		argv = append(argv, "--property="+k+"="+v)
	}
	argv = append(argv, args...)
	if _, e = c.run(ctx, u.Rank, argv...); e != nil {
		return e
	}
	m, e = c.unit(ctx, *u)
	if e != nil {
		return e
	}
	if e = verifyUnit(*u, m); e != nil {
		return e
	}
	if !unitActive(m) {
		return errors.New("launched unit exited")
	}
	u.InvocationID = m["InvocationID"]
	return nil
}
func controllerHealth(ctx context.Context, raw string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, "GET", raw+"/health", nil)
	if e != nil {
		return false
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// This one-shot probe owns its transport. Closing idle connections avoids
	// retaining a new pool/readLoop for every UI/load-health poll.
	defer client.CloseIdleConnections()
	r, e := client.Do(req)
	if e != nil {
		return false
	}
	defer r.Body.Close()
	n, e := io.Copy(io.Discard, io.LimitReader(r.Body, 65537))
	return e == nil && n <= 65536 && r.StatusCode == 200
}
func (c *ClusterController) waitHealth(ctx context.Context, urls [2]string, us []OwnedUnit) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.Config.LoadTimeoutSeconds)*time.Second)
	defer cancel()
	for {
		if e := c.inspectAll(ctx, us, len(us) > 0 && strings.HasPrefix(us[0].Name, "ciru-")); e != nil {
			return e
		}
		for _, u := range us {
			m, e := c.unit(ctx, u)
			if e != nil {
				return e
			}
			if !unitActive(m) {
				return fmt.Errorf("unit exited during load: %s", u.Name)
			}
		}
		ok := make(chan bool, 2)
		for _, raw := range urls {
			go func(raw string) { ok <- controllerHealth(ctx, raw) }(raw)
		}
		a, b := <-ok, <-ok
		if a && b {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}
func (c *ClusterController) Status(ctx context.Context) (map[string]any, error) {
	result := map[string]any{"configuration": c.Config, "model": "GLM5.3-Flash-CIRU-STRIX-IU4", "tp": 2, "pp": 1, "dflash_k": 5, "local_draft": 0, "transport": "RCCL Socket/thunderbolt0"}
	o, e := c.readOwner()
	if e == nil {
		result["owner"] = o
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	units := []any{}
	for _, u := range []OwnedUnit{{Rank: 0, Name: "strixglm-rank0"}, {Rank: 1, Name: "strixglm-rank1"}, {Rank: 0, Name: "ciru-model-rank0-001"}, {Rank: 1, Name: "ciru-model-rank1-001"}} {
		m, e := c.unit(ctx, u)
		if e != nil {
			units = append(units, map[string]any{"rank": u.Rank, "name": u.Name, "error": e.Error()})
		} else {
			delete(m, "Environment")
			units = append(units, map[string]any{"rank": u.Rank, "name": u.Name, "state": m})
		}
	}
	result["units"] = units
	_, e = os.Stat(filepath.Join(c.Config.StateDir, "pair-poison.json"))
	result["poisoned"] = e == nil
	return result, nil
}

type LegacyUnit struct {
	OwnedUnit
	Args       []string `json:"args"`
	Log        string   `json:"log"`
	Memory     string   `json:"memory"`
	Definition string   `json:"definition"`
}
type LegacySnapshot struct {
	Schema      string          `json:"schema"`
	Owner       json.RawMessage `json:"owner"`
	OwnerSHA256 string          `json:"owner_sha256"`
	Units       []LegacyUnit    `json:"units"`
	Created     string          `json:"created"`
}

func parseSystemdQuoted(s string) ([]string, error) {
	var args []string
	for s = strings.TrimSpace(s); s != ""; s = strings.TrimSpace(s) {
		if s[0] != '"' {
			return nil, errors.New("unsupported systemd argv format")
		}
		end := -1
		for i := 1; i < len(s); i++ {
			if s[i] == '\\' {
				i++
				continue
			}
			if s[i] == '"' {
				end = i
				break
			}
		}
		if end < 0 {
			return nil, errors.New("unterminated systemd argument")
		}
		v, e := strconv.Unquote(s[:end+1])
		if e != nil {
			return nil, e
		}
		args = append(args, v)
		s = s[end+1:]
	}
	if len(args) == 0 {
		return nil, errors.New("empty systemd command")
	}
	return args, nil
}
func (c *ClusterController) SnapshotLegacy(ctx context.Context, path string) error {
	return c.locked(func() error {
		if _, e := os.Lstat(path); !os.IsNotExist(e) {
			return errors.New("snapshot destination must be new")
		}
		b, e := os.ReadFile(c.Config.LegacyOwner)
		if e != nil {
			return e
		}
		var owner struct {
			Units     []OwnedUnit `json:"units"`
			Depth     int         `json:"depth"`
			Local     int         `json:"local_draft"`
			Safe      bool        `json:"safe_prefill"`
			Canonical bool        `json:"canonical_moe"`
		}
		if e = json.Unmarshal(b, &owner); e != nil {
			return e
		}
		if len(owner.Units) != 3 || owner.Depth != 5 || owner.Local != 0 || !owner.Safe || !owner.Canonical {
			return errors.New("legacy owner not qualified preset")
		}
		if e = c.inspectAll(ctx, owner.Units, true); e != nil {
			return e
		}
		var canonical any
		if e = json.Unmarshal(b, &canonical); e != nil {
			return e
		}
		canonicalBytes, e := json.Marshal(canonical)
		if e != nil {
			return e
		}
		h := sha256.Sum256(canonicalBytes)
		s := LegacySnapshot{Schema: "strixglm-legacy-snapshot-v1", Owner: b, OwnerSHA256: hex.EncodeToString(h[:]), Created: time.Now().UTC().Format(time.RFC3339Nano)}
		for _, u := range owner.Units {
			def, e := c.run(ctx, u.Rank, "systemctl", "--user", "cat", u.Name)
			if e != nil {
				return e
			}
			lu := LegacyUnit{OwnedUnit: u, Definition: string(def)}
			for _, l := range strings.Split(string(def), "\n") {
				if strings.HasPrefix(l, "ExecStart=") && l != "ExecStart=" {
					lu.Args, e = parseSystemdQuoted(strings.TrimPrefix(l, "ExecStart="))
					if e != nil {
						return e
					}
				}
				if strings.HasPrefix(l, "StandardOutput=append:") {
					lu.Log = strings.TrimPrefix(l, "StandardOutput=append:")
				}
				if strings.HasPrefix(l, "MemoryMax=") {
					lu.Memory = strings.TrimPrefix(l, "MemoryMax=")
				}
			}
			if len(lu.Args) == 0 || !filepath.IsAbs(lu.Log) || lu.Memory == "" {
				return errors.New("incomplete legacy unit recipe")
			}
			s.Units = append(s.Units, lu)
		}
		return controllerWrite(path, s)
	})
}
func loadLegacySnapshot(path string) (LegacySnapshot, error) {
	var s LegacySnapshot
	b, e := os.ReadFile(path)
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return s, e
	}
	var canonical any
	if e = json.Unmarshal(s.Owner, &canonical); e != nil {
		return s, e
	}
	canonicalBytes, e := json.Marshal(canonical)
	if e != nil {
		return s, e
	}
	h := sha256.Sum256(canonicalBytes)
	if hex.EncodeToString(h[:]) != s.OwnerSHA256 {
		return s, errors.New("preserved owner checksum mismatch")
	}
	var owner struct {
		Units []OwnedUnit `json:"units"`
	}
	if e = json.Unmarshal(s.Owner, &owner); e != nil {
		return s, e
	}
	if s.Schema != "strixglm-legacy-snapshot-v1" || len(s.Units) != 3 || len(owner.Units) != 3 {
		return s, errors.New("invalid legacy snapshot")
	}
	us := []OwnedUnit{}
	for _, u := range s.Units {
		var definitionArgs []string
		for _, line := range strings.Split(u.Definition, "\n") {
			if strings.HasPrefix(line, "ExecStart=") && line != "ExecStart=" {
				definitionArgs, e = parseSystemdQuoted(strings.TrimPrefix(line, "ExecStart="))
				if e != nil {
					return s, e
				}
			}
		}
		if !equalStrings(definitionArgs, u.Args) {
			return s, errors.New("snapshot argv differs from preserved systemd definition")
		}
		us = append(us, u.OwnedUnit)
	}
	if !sameUnitOwnership(owner.Units, us) {
		return s, errors.New("snapshot recipes differ from preserved owner")
	}
	return s, validUnits(us, true)
}
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func (c *ClusterController) validateLegacyRecipe(s LegacySnapshot) error {
	var owner struct {
		Epoch string `json:"epoch"`
	}
	if e := json.Unmarshal(s.Owner, &owner); e != nil {
		return e
	}
	for _, u := range s.Units {
		want := []string{"/usr/bin/bash", filepath.Join(c.Config.EngineRoot, "scripts/run-rank.sh"), strconv.Itoa(u.Rank), "5", owner.Epoch, "0", "", "1", "1", "0"}
		if u.Name == "ciru-frontend-001" {
			want = []string{filepath.Join(c.Config.EngineRoot, "venv/bin/python"), filepath.Join(c.Config.EngineRoot, "scripts/frontend.py"), "--poison-marker", filepath.Join(filepath.Dir(c.Config.LegacyOwner), "frontend-poison.json")}
		}
		if !equalStrings(u.Args, want) {
			return fmt.Errorf("snapshot is not the exact qualified legacy argv: %s", u.Name)
		}
		if filepath.Clean(u.Log) != u.Log || !strings.HasPrefix(u.Log, filepath.Join(filepath.Dir(c.Config.EngineRoot), "reports")+"/") {
			return errors.New("unexpected legacy log path")
		}
		expectedMemory := "124554051584"
		if u.Name == "ciru-frontend-001" {
			expectedMemory = "2147483648"
		}
		if u.Memory != expectedMemory {
			return errors.New("legacy memory policy changed")
		}
	}
	return nil
}
func (c *ClusterController) verifyLegacyIdentity(ctx context.Context, s LegacySnapshot) error {
	var owner struct {
		Identity map[string]map[string]string `json:"runtime_identity"`
	}
	if e := json.Unmarshal(s.Owner, &owner); e != nil {
		return e
	}
	for rank := 0; rank < 2; rank++ {
		ids := owner.Identity[fmt.Sprintf("rank%d", rank)]
		if len(ids) < 10 || len(ids) > 64 {
			return errors.New("invalid preserved runtime identity")
		}
		keys := make([]string, 0, len(ids))
		for p, digest := range ids {
			if filepath.IsAbs(p) || strings.Contains(p, "..") || len(digest) != 64 {
				return errors.New("invalid legacy identity path/hash")
			}
			keys = append(keys, p)
		}
		sort.Strings(keys)
		args := []string{"sha256sum", "--"}
		for _, p := range keys {
			args = append(args, filepath.Join(c.Config.EngineRoot, p))
		}
		out, e := c.run(ctx, rank, args...)
		if e != nil {
			return e
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) != len(keys) {
			return errors.New("incomplete legacy hash output")
		}
		for i, p := range keys {
			if lines[i] != ids[p]+"  "+filepath.Join(c.Config.EngineRoot, p) {
				return fmt.Errorf("legacy source changed since snapshot: rank%d %s", rank, p)
			}
		}
	}
	return nil
}
func (c *ClusterController) StopLegacy(ctx context.Context, path string) error {
	return c.stopLegacy(ctx, path, false)
}

// RecoverLegacy is an EXPLICIT all-rank recovery action, not admission retry.
// A poisoned/in-flight pair may be terminated only after the same whole-owner
// snapshot checks as a normal stop. Raw uncertainty survives in the journal.
func (c *ClusterController) RecoverLegacy(ctx context.Context, path string) error {
	return c.stopLegacy(ctx, path, true)
}

func (c *ClusterController) stopLegacy(ctx context.Context, path string, recovery bool) error {
	return c.locked(func() error {
		s, e := loadLegacySnapshot(path)
		if e != nil {
			return e
		}
		if e = c.validateLegacyRecipe(s); e != nil {
			return e
		}
		b, e := os.ReadFile(c.Config.LegacyOwner)
		if e != nil {
			return e
		}
		var live struct {
			Units []OwnedUnit `json:"units"`
		}
		if e = json.Unmarshal(b, &live); e != nil {
			return e
		}
		us := []OwnedUnit{}
		for _, u := range s.Units {
			us = append(us, u.OwnedUnit)
		}
		if !sameUnitOwnership(live.Units, us) {
			return errors.New("legacy owner changed since snapshot")
		}
		if e = c.inspectAll(ctx, us, true); e != nil {
			return e
		}
		// Reserve the old frontend's own O_EXCL admission marker BEFORE stopping
		// it. A request racing this management operation can never dispatch.
		marker := filepath.Join(filepath.Dir(c.Config.LegacyOwner), "frontend-poison.json")
		markerInfo, markerErr := os.Lstat(marker)
		if markerErr != nil && !os.IsNotExist(markerErr) {
			return markerErr
		}
		if markerErr == nil {
			if !recovery {
				return errors.New("legacy in-flight/poison marker present; drain or explicit recovery required")
			}
			if !markerInfo.Mode().IsRegular() || markerInfo.Size() > 65536 {
				return errors.New("unsafe legacy poison marker")
			}
			f, err := os.OpenFile(marker, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			raw, err := io.ReadAll(io.LimitReader(f, 65537))
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
			if len(raw) > 65536 {
				return errors.New("legacy poison marker grew beyond limit")
			}
			receipt := map[string]any{"schema": "strixglm-legacy-recovery-v1", "snapshot": path, "units": us, "poison_raw": raw, "started_utc": time.Now().UTC().Format(time.RFC3339Nano), "state": "whole-pair-stop-intent"}
			if e = controllerWrite(filepath.Join(c.Config.StateDir, "legacy-recovery-intent-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".json"), receipt); e != nil {
				return e
			}
		} else {
			maintenance, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return fmt.Errorf("legacy admission became busy: %w", err)
			}
			_, e = maintenance.WriteString("{\"reason\":\"StrixHaloClusterGLM whole-pair maintenance\"}\n")
			if e == nil {
				e = maintenance.Sync()
			}
			closeErr := maintenance.Close()
			if e == nil {
				e = closeErr
			}
			if e != nil {
				return e
			}
		}
		if e = c.stopUnits(ctx, us, true); e != nil {
			return e
		}
		if e = os.Rename(marker, filepath.Join(c.Config.StateDir, "legacy-poison-recovered-"+strconv.FormatInt(time.Now().UnixNano(), 10)+".json")); e != nil && !os.IsNotExist(e) {
			return e
		}
		var m map[string]any
		if e = json.Unmarshal(b, &m); e != nil {
			return e
		}
		m["state"] = "stopped"
		m["strixglm_snapshot"] = path
		if recovery {
			m["strixglm_recovered_utc"] = time.Now().UTC().Format(time.RFC3339Nano)
		}
		return controllerWrite(c.Config.LegacyOwner, m)
	})
}
func sameUnitOwnership(a, b []OwnedUnit) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]OwnedUnit{}
	for _, u := range a {
		m[u.Name] = u
	}
	for _, u := range b {
		if m[u.Name] != u {
			return false
		}
	}
	return true
}

// The stopped-owner receipt must still identify the exact preserved epoch and
// complete unit set. A matching snapshot pathname alone is not ownership.
func (c *ClusterController) checkStoppedLegacyOwner(s LegacySnapshot, path string) error {
	current, e := os.ReadFile(c.Config.LegacyOwner)
	if e != nil {
		return e
	}
	var previous struct {
		State    string      `json:"state"`
		Snapshot string      `json:"strixglm_snapshot"`
		Epoch    string      `json:"epoch"`
		Units    []OwnedUnit `json:"units"`
	}
	if e = json.Unmarshal(current, &previous); e != nil {
		return e
	}
	if previous.State != "stopped" || previous.Snapshot != path {
		return errors.New("legacy was not stopped by this snapshot")
	}
	var preserved struct {
		Epoch string `json:"epoch"`
	}
	if e = json.Unmarshal(s.Owner, &preserved); e != nil {
		return e
	}
	units := make([]OwnedUnit, 0, len(s.Units))
	for _, u := range s.Units {
		units = append(units, u.OwnedUnit)
	}
	if preserved.Epoch == "" || previous.Epoch != preserved.Epoch || !sameUnitOwnership(previous.Units, units) {
		return errors.New("legacy stopped owner identity changed since snapshot")
	}
	return nil
}

func (c *ClusterController) RestoreLegacy(ctx context.Context, path string) error {
	return c.locked(func() error {
		s, e := loadLegacySnapshot(path)
		if e != nil {
			return e
		}
		if e = c.validateLegacyRecipe(s); e != nil {
			return e
		}
		if e = c.checkStoppedLegacyOwner(s, path); e != nil {
			return e
		}
		if o, e := c.readOwner(); e == nil && o.State != "stopped" {
			return errors.New("stop new whole pair before legacy restore")
		} else if e != nil && !os.IsNotExist(e) {
			return e
		}
		for _, u := range []OwnedUnit{{Rank: 0, Name: "strixglm-rank0"}, {Rank: 1, Name: "strixglm-rank1"}, {Rank: 0, Name: "ciru-model-rank0-001"}, {Rank: 1, Name: "ciru-model-rank1-001"}, {Rank: 0, Name: "ciru-frontend-001"}} {
			m, e := c.unit(ctx, u)
			if e != nil {
				return e
			}
			if unitActive(m) {
				return fmt.Errorf("active service prevents restore: %s", u.Name)
			}
		}
		if e = c.Verify(ctx); e != nil {
			return e
		}
		if e = c.verifyLegacyIdentity(ctx, s); e != nil {
			return e
		}
		var owner map[string]any
		if e = json.Unmarshal(s.Owner, &owner); e != nil {
			return e
		}
		// Drop-ins are exact owned records, not broad cleanup targets. Refuse to
		// overwrite a foreign/replaced retention file during rollback.
		if records, ok := owner["retention_dropins"].([]any); ok {
			for _, rec := range records {
				m, ok := rec.(map[string]any)
				if !ok {
					return errors.New("invalid retention record")
				}
				matched := false
				for _, lu := range s.Units {
					if m["name"] == lu.Name {
						matched = true
						p, _ := m["path"].(string)
						expected := fmt.Sprintf("/run/user/%d/systemd/user/%s.service.d/90-ciru-qualified-retention.conf", os.Getuid(), lu.Name)
						if p != expected {
							return errors.New("foreign retention path")
						}
						contents, e := c.run(ctx, lu.Rank, "cat", p)
						if e != nil {
							return e
						}
						want := fmt.Sprintf("# CIRU qualified retention unit=%s rank=%d\n# owner_nonce=%s invocation_id=%s\n[Service]\nRuntimeMaxSec=infinity\n", lu.Name, lu.Rank, lu.Nonce, lu.InvocationID)
						if string(contents) != want {
							return fmt.Errorf("retention contents changed: %s", p)
						}
					}
				}
				if !matched {
					return errors.New("unowned retention record")
				}
			}
		}
		// Re-read after all slow read-only checks, immediately before the first
		// restore mutation/reservation. No launch has occurred at this point.
		if e = c.checkStoppedLegacyOwner(s, path); e != nil {
			return e
		}
		owner["state"] = "starting"
		owner["strixglm_snapshot"] = path
		owner["units"] = []OwnedUnit{}
		if e = controllerWrite(c.Config.LegacyOwner, owner); e != nil {
			return e
		}
		var launched []OwnedUnit
		cleanup := func(cause error) error {
			cc, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			ce := c.stopUnits(cc, launched, true)
			owner["state"] = "stopped"
			if ce != nil {
				owner["state"] = "cleanup_uncertain"
			}
			owner["units"] = launched
			owner["restore_error"] = cause.Error()
			_ = controllerWrite(c.Config.LegacyOwner, owner)
			return errors.Join(cause, ce)
		}
		// Start ranks before frontend, always reserving identity durably first.
		sort.SliceStable(s.Units, func(i, j int) bool {
			if s.Units[i].Name == "ciru-frontend-001" {
				return false
			}
			if s.Units[j].Name == "ciru-frontend-001" {
				return true
			}
			return s.Units[i].Rank > s.Units[j].Rank
		})
		for _, lu := range s.Units {
			if lu.Name == "ciru-frontend-001" {
				if e = c.waitHealth(ctx, [2]string{"http://10.55.0.1:18100", "http://10.55.0.2:18100"}, launched); e != nil {
					return cleanup(e)
				}
			}
			u := lu.OwnedUnit
			u.InvocationID = ""
			launched = append(launched, u)
			owner["units"] = launched
			if e = controllerWrite(c.Config.LegacyOwner, owner); e != nil {
				return cleanup(e)
			}
			if e = c.launch(ctx, &launched[len(launched)-1], lu.Args, lu.Log, lu.Memory, nil); e != nil {
				return cleanup(e)
			}
			owner["units"] = launched
			if e = controllerWrite(c.Config.LegacyOwner, owner); e != nil {
				return cleanup(e)
			}
		}
		// Existing retention files are preserved and rebased to the new InvocationIDs.
		if records, ok := owner["retention_dropins"].([]any); ok {
			for _, rec := range records {
				m, ok := rec.(map[string]any)
				if !ok {
					return cleanup(errors.New("invalid retention record"))
				}
				for _, u := range launched {
					if m["name"] == u.Name {
						m["invocation_id"] = u.InvocationID
						p, _ := m["path"].(string)
						expected := fmt.Sprintf("/run/user/%d/systemd/user/%s.service.d/90-ciru-qualified-retention.conf", os.Getuid(), u.Name)
						if p != expected {
							return cleanup(errors.New("foreign retention path"))
						}
						body := fmt.Sprintf("# CIRU qualified retention unit=%s rank=%d\n# owner_nonce=%s invocation_id=%s\n[Service]\nRuntimeMaxSec=infinity\n", u.Name, u.Rank, u.Nonce, u.InvocationID)
						if _, e = c.run(ctx, u.Rank, "bash", "-c", "printf '%s' \"$1\" > \"$2\"", "retention-restore", body, p); e != nil {
							return cleanup(e)
						}
					}
				}
			}
		}
		healthy := false
		for i := 0; i < 15; i++ {
			if controllerHealth(ctx, "http://127.0.0.1:18091") {
				healthy = true
				break
			}
			select {
			case <-ctx.Done():
				return cleanup(ctx.Err())
			case <-time.After(time.Second):
			}
		}
		if !healthy {
			return cleanup(errors.New("legacy frontend unhealthy after whole-pair restore"))
		}
		owner["state"] = "promoted"
		owner["strixglm_restored_utc"] = time.Now().UTC().Format(time.RFC3339Nano)
		return controllerWrite(c.Config.LegacyOwner, owner)
	})
}
