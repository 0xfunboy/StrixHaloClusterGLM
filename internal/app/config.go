package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Profile struct {
	Reasoning     string `json:"reasoning"`
	ContextTokens int    `json:"context_tokens"`
	MaxTokens     int    `json:"max_tokens"`
	MaxRepairs    int    `json:"max_repairs"`
}
type Config struct {
	Listen                string             `json:"listen"`
	Backend               string             `json:"backend"`
	Model                 string             `json:"model"`
	StateDir              string             `json:"state_dir"`
	WorkspaceRoots        []string           `json:"workspace_roots"`
	PairLock              string             `json:"pair_lock"`
	DefaultProfile        string             `json:"default_profile"`
	Profiles              map[string]Profile `json:"profiles"`
	ProfileStatus         string             `json:"profile_status"`
	ContextFormat         string             `json:"context_format"`
	MaxContextTokens      int                `json:"max_context_tokens"`
	MaxFiles              int                `json:"max_files"`
	MaxFileBytes          int                `json:"max_file_bytes"`
	MaxRepoBytes          int                `json:"max_repo_bytes"`
	ModelTimeout          int                `json:"model_timeout"`
	SandboxTimeout        int                `json:"sandbox_timeout"`
	SandboxMemoryBytes    int64              `json:"sandbox_memory_bytes"`
	SandboxTasks          int                `json:"sandbox_tasks"`
	RemoteSSH             string             `json:"remote_ssh"`
	TokenizerEndpoint     string             `json:"tokenizer_endpoint,omitempty"`
	ChatContextTokens     int                `json:"chat_context_tokens,omitempty"`
	ChatDefaultOutput     int                `json:"chat_default_output_tokens,omitempty"`
	ChatMaxOutput         int                `json:"chat_max_output_tokens,omitempty"`
	ThinkingBudget        bool               `json:"thinking_budget_supported,omitempty"`
	ToolCalls             bool               `json:"tool_calls_supported,omitempty"`
	ReasoningModes        []string           `json:"reasoning_modes,omitempty"`
	LifecycleCommand      string             `json:"lifecycle_command,omitempty"`
	LifecyclePreset       string             `json:"lifecycle_preset,omitempty"`
	ClusterConfigPath     string             `json:"cluster_config_path,omitempty"`
	ClusterSharedStateDir string             `json:"cluster_shared_state_dir,omitempty"`
	LifecycleDrainSeconds int                `json:"lifecycle_drain_seconds,omitempty"`
	InferenceEnabled      *bool              `json:"inference_enabled,omitempty"`
}

func loadConfig(path string) (Config, error) {
	var c Config
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	e = json.Unmarshal(b, &c)
	if e != nil {
		return c, e
	}
	if c.Model == "" || c.StateDir == "" || c.MaxFiles < 1 || c.MaxRepoBytes < 1 || c.SandboxTasks < 1 || c.ModelTimeout < 1 || len(c.WorkspaceRoots) == 0 {
		return c, errors.New("incomplete config")
	}
	if _, ok := c.Profiles[c.DefaultProfile]; !ok {
		return c, errors.New("unknown default profile")
	}
	if c.ContextFormat != "" && c.ContextFormat != "json" {
		return c, errors.New("context_format must be json or blank")
	}
	if c.ChatContextTokens < 0 || c.ChatContextTokens > safeEngineContext || c.ChatMaxOutput < 0 || c.ChatMaxOutput > maxChatOutput || c.ChatDefaultOutput < 0 || c.ChatDefaultOutput > maxChatOutput {
		return c, errors.New("invalid chat context/output limits")
	}
	if c.ChatDefaultOutput > 0 && c.ChatMaxOutput > 0 && c.ChatDefaultOutput > c.ChatMaxOutput {
		return c, errors.New("default output exceeds max output")
	}
	if len(c.ReasoningModes) > 0 {
		seen := map[string]bool{}
		for _, mode := range c.ReasoningModes {
			if mode != "none" && mode != "low" && mode != "medium" && mode != "high" && mode != "xhigh" && mode != "max" {
				return c, errors.New("reasoning_modes contains unsupported value")
			}
			if seen[mode] {
				return c, errors.New("reasoning_modes contains duplicate")
			}
			seen[mode] = true
		}
	}
	if c.LifecycleCommand != "" && (!filepath.IsAbs(c.LifecycleCommand) || filepath.Clean(c.LifecycleCommand) != c.LifecycleCommand) {
		return c, errors.New("lifecycle_command must be an absolute clean path")
	}
	if c.LifecycleCommand != "" && c.LifecyclePreset == "" {
		return c, errors.New("lifecycle_preset required with lifecycle_command")
	}
	// Native pair management and an external owner-bound controller are separate drivers.
	if c.ClusterConfigPath != "" && c.LifecycleCommand == "" {
		if !filepath.IsAbs(c.ClusterConfigPath) || filepath.Clean(c.ClusterConfigPath) != c.ClusterConfigPath {
			return c, errors.New("cluster_config_path must be an absolute clean path")
		}
		if c.ClusterSharedStateDir == "" || !filepath.IsAbs(c.ClusterSharedStateDir) || filepath.Clean(c.ClusterSharedStateDir) != c.ClusterSharedStateDir {
			return c, errors.New("cluster_shared_state_dir must be an absolute clean path")
		}
		if c.LifecycleDrainSeconds == 0 {
			c.LifecycleDrainSeconds = 30
		}
		if c.LifecycleDrainSeconds < 5 || c.LifecycleDrainSeconds > 120 {
			return c, errors.New("lifecycle_drain_seconds must be 5..120")
		}
	}
	return c, nil
}
func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	tmp := path + "." + id() + ".tmp"
	if e = os.WriteFile(tmp, append(b, '\n'), 0600); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func cleanRel(p string) error {
	if p == "" || filepath.IsAbs(p) || filepath.Clean(p) != p || strings.Contains(p, "\\") {
		return fmt.Errorf("unsafe path: %q", p)
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." || strings.HasPrefix(s, ".") {
			return fmt.Errorf("hidden/traversal path refused: %q", p)
		}
	}
	return nil
}
func within(path, root string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && r != ".." && !strings.HasPrefix(r, ".."+string(os.PathSeparator))
}
func realPath(p string) (string, error) {
	a, e := filepath.Abs(p)
	if e != nil {
		return "", e
	}
	r, e := filepath.EvalSymlinks(a)
	if e != nil {
		return "", e
	}
	if a != r {
		return "", fmt.Errorf("symlink paths refused: %s", a)
	}
	return a, nil
}

type LogEvent struct {
	Time   string `json:"time"`
	Event  string `json:"event"`
	Detail any    `json:"detail,omitempty"`
}
type Journal struct {
	mu   sync.Mutex
	Path string
}

func (j *Journal) Log(event string, detail any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	b, _ := json.Marshal(LogEvent{time.Now().UTC().Format(time.RFC3339Nano), event, detail})
	f, e := os.OpenFile(j.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e == nil {
		defer f.Close()
		f.Write(append(b, '\n'))
	}
}
