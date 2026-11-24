// Package cni executes allowlisted CNI plugins via exec with strict path checks,
// forward ADD chaining, reverse idempotent DEL, and stdout structured error decoding.
package cni

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CNIVersion is the CNI specification version emitted in plugin configuration JSON.
const CNIVersion = "1.0.0"

const (
	maxPluginOutputBytes = 1 << 20
	cleanExecPath        = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

var allowedPlugins = map[string]struct{}{
	"loopback":   {},
	"bridge":     {},
	"host-local": {},
}

// IsAllowedPlugin reports whether name is in the fixed CNI plugin allowlist.
func IsAllowedPlugin(name string) bool {
	_, ok := allowedPlugins[name]
	return ok
}

// ResolvePluginPath validates plugin against the allowlist and enforces the binDir/ boundary
// after resolving symlinks so no symlink inside binDir can execute a binary outside binDir.
func ResolvePluginPath(binDir, plugin string) (string, error) {
	if !IsAllowedPlugin(plugin) {
		return "", fmt.Errorf("cni: plugin %q is not in allowlist", plugin)
	}
	if filepath.Base(plugin) != plugin || strings.ContainsAny(plugin, "/\\") || plugin == "." || plugin == ".." {
		return "", fmt.Errorf("cni: invalid plugin name %q", plugin)
	}
	if binDir == "" || !filepath.IsAbs(binDir) {
		return "", fmt.Errorf("cni: binDir %q must be an absolute path", binDir)
	}
	cleanDir := filepath.Clean(binDir)
	realDir, err := filepath.EvalSymlinks(cleanDir)
	if err != nil {
		return "", fmt.Errorf("cni: resolve binDir %q: %w", cleanDir, err)
	}
	if realDir == "/" {
		return "", errors.New("cni: binDir must not resolve to root \"/\"")
	}
	prefix := realDir + string(os.PathSeparator)
	cleanPath := filepath.Clean(filepath.Join(realDir, plugin))
	if !strings.HasPrefix(cleanPath, prefix) || filepath.Dir(cleanPath) != realDir {
		return "", fmt.Errorf("cni: plugin path %q escapes binDir %q", cleanPath, realDir)
	}
	realPath, err := filepath.EvalSymlinks(cleanPath)
	if err != nil {
		return "", fmt.Errorf("cni: resolve plugin %s: %w", cleanPath, err)
	}
	if !strings.HasPrefix(realPath, prefix) || filepath.Dir(realPath) != realDir {
		return "", fmt.Errorf("cni: plugin symlink target %q escapes binDir %q", realPath, realDir)
	}
	fi, err := os.Stat(realPath)
	if err != nil {
		return "", fmt.Errorf("cni: stat plugin %s: %w", realPath, err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("cni: plugin path %s is a directory", realPath)
	}
	if fi.Mode()&0o111 == 0 {
		return "", fmt.Errorf("cni: plugin %s is not executable", realPath)
	}
	return realPath, nil
}

// IPRange describes one non-deprecated host-local IPAM range entry.
type IPRange struct {
	Subnet     string `json:"subnet"`
	Gateway    string `json:"gateway,omitempty"`
	RangeStart string `json:"rangeStart,omitempty"`
	RangeEnd   string `json:"rangeEnd,omitempty"`
}

// BridgeConfig configures the standard [loopback, bridge + host-local] CNI chain.
type BridgeConfig struct {
	BinDir         string
	NetworkName    string
	Bridge         string
	DataDir        string
	Ranges         []IPRange
	DisableGateway bool
	IPMasq         bool
	MTU            int
}

// Step defines one plugin invocation in a Chain.
type Step struct {
	Plugin string
	IfName string
	Config []byte
}

// Result represents a CNI 1.0.0 ADD result payload decoded from stdout.
type Result struct {
	CNIVersion string        `json:"cniVersion"`
	Interfaces []Interface   `json:"interfaces,omitempty"`
	IPs        []IPConfig    `json:"ips,omitempty"`
	Routes     []RouteConfig `json:"routes,omitempty"`
}

// Interface is one CNI 1.0.0 interface entry.
type Interface struct {
	Name    string `json:"name"`
	Mac     string `json:"mac,omitempty"`
	Sandbox string `json:"sandbox,omitempty"`
}

// IPConfig is one CNI 1.0.0 IP allocation entry.
type IPConfig struct {
	Interface *int   `json:"interface,omitempty"`
	Address   string `json:"address"`
	Gateway   string `json:"gateway,omitempty"`
}

// RouteConfig is one CNI 1.0.0 route entry.
type RouteConfig struct {
	Dst string `json:"dst"`
	GW  string `json:"gw,omitempty"`
}

// ParseResult decodes a CNI 1.0.0 Result JSON object from plugin stdout.
func ParseResult(raw []byte) (Result, error) {
	var res Result
	if err := json.Unmarshal(raw, &res); err != nil {
		return Result{}, fmt.Errorf("cni: decode result JSON: %w", err)
	}
	return res, nil
}

// Error is a structured CNI error decoded from plugin stdout on non-zero exit.
// Stderr is kept strictly for diagnostics and is never parsed as CNI Error JSON.
type Error struct {
	CNIVersion string `json:"cniVersion,omitempty"`
	Code       uint   `json:"code"`
	Msg        string `json:"msg"`
	Details    string `json:"details,omitempty"`
	Plugin     string `json:"-"`
	Command    string `json:"-"`
	ExitCode   int    `json:"-"`
	Stderr     string `json:"-"`
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "cni %s %s failed (exit %d, code %d): %s", e.Command, e.Plugin, e.ExitCode, e.Code, e.Msg)
	if e.Details != "" {
		fmt.Fprintf(&b, " (%s)", e.Details)
	}
	if e.Stderr != "" {
		fmt.Fprintf(&b, " [stderr: %s]", e.Stderr)
	}
	return b.String()
}

// Chain executes an ordered sequence of allowlisted CNI plugins.
type Chain struct {
	binDir string
	steps  []Step
}

// NewChain validates binDir and every step's plugin name against the allowlist.
func NewChain(binDir string, steps []Step) (*Chain, error) {
	if binDir == "" || !filepath.IsAbs(binDir) {
		return nil, fmt.Errorf("cni: binDir %q must be an absolute path", binDir)
	}
	cleanDir := filepath.Clean(binDir)
	copied := make([]Step, len(steps))
	for i, s := range steps {
		if !IsAllowedPlugin(s.Plugin) {
			return nil, fmt.Errorf("cni: step %d plugin %q is not in allowlist", i, s.Plugin)
		}
		if s.IfName == "" || strings.ContainsAny(s.IfName, "/\x00 \t\n") {
			return nil, fmt.Errorf("cni: step %d invalid ifname %q", i, s.IfName)
		}
		var obj map[string]any
		if err := json.Unmarshal(s.Config, &obj); err != nil || obj == nil {
			return nil, fmt.Errorf("cni: step %d config must be a JSON object", i)
		}
		copied[i] = Step{
			Plugin: s.Plugin,
			IfName: s.IfName,
			Config: bytes.Clone(s.Config),
		}
	}
	return &Chain{binDir: cleanDir, steps: copied}, nil
}

// NewBridgeChain builds the default 2-step chain: [0] loopback (lo), [1] bridge + host-local (eth0).
func NewBridgeChain(cfg BridgeConfig) (*Chain, error) {
	if cfg.NetworkName == "" {
		cfg.NetworkName = "nayutar0"
	}
	if cfg.Bridge == "" {
		cfg.Bridge = "nayutar0"
	}
	if len(cfg.Ranges) == 0 {
		return nil, errors.New("cni: at least one IP range is required")
	}
	for i, r := range cfg.Ranges {
		if _, err := netip.ParsePrefix(r.Subnet); err != nil {
			return nil, fmt.Errorf("cni: ranges[%d] invalid subnet %q: %w", i, r.Subnet, err)
		}
	}

	loopbackCfg, err := json.Marshal(map[string]any{
		"cniVersion": CNIVersion,
		"name":       cfg.NetworkName,
		"type":       "loopback",
	})
	if err != nil {
		return nil, err
	}

	ipam := map[string]any{
		"type":   "host-local",
		"ranges": [][]IPRange{cfg.Ranges},
	}
	if cfg.DataDir != "" {
		ipam["dataDir"] = cfg.DataDir
	}

	bridgeMap := map[string]any{
		"cniVersion":       CNIVersion,
		"name":             cfg.NetworkName,
		"type":             "bridge",
		"bridge":           cfg.Bridge,
		"isGateway":        !cfg.DisableGateway,
		"isDefaultGateway": !cfg.DisableGateway,
		"ipMasq":           cfg.IPMasq,
		"ipam":             ipam,
	}
	if cfg.MTU > 0 {
		bridgeMap["mtu"] = cfg.MTU
	}
	bridgeCfg, err := json.Marshal(bridgeMap)
	if err != nil {
		return nil, err
	}

	return NewChain(cfg.BinDir, []Step{
		{Plugin: "loopback", IfName: "lo", Config: loopbackCfg},
		{Plugin: "bridge", IfName: "eth0", Config: bridgeCfg},
	})
}

// Count returns the number of CNI steps in the chain.
func (c *Chain) Count() int {
	return len(c.steps)
}

// Steps returns a defensive copy of the configured CNI steps.
func (c *Chain) Steps() []Step {
	out := make([]Step, len(c.steps))
	for i, s := range c.steps {
		out[i] = Step{Plugin: s.Plugin, IfName: s.IfName, Config: bytes.Clone(s.Config)}
	}
	return out
}

// Add executes step in CNI ADD mode, injecting prevResult when provided.
func (c *Chain) Add(ctx context.Context, step int, containerID, netnsPath string, prevResult []byte) ([]byte, error) {
	if netnsPath == "" {
		return nil, errors.New("cni: netnsPath must be non-empty for ADD")
	}
	return c.execStep(ctx, "ADD", step, containerID, netnsPath, prevResult)
}

// Del executes step in CNI DEL mode. It runs even if netnsPath no longer exists so IPAM frees IPs.
func (c *Chain) Del(ctx context.Context, step int, containerID, netnsPath string, prevResult []byte) error {
	_, err := c.execStep(ctx, "DEL", step, containerID, netnsPath, prevResult)
	return err
}

func (c *Chain) execStep(ctx context.Context, command string, step int, containerID, netnsPath string, prevResult []byte) ([]byte, error) {
	if step < 0 || step >= len(c.steps) {
		return nil, fmt.Errorf("cni: step index %d out of range [0, %d)", step, len(c.steps))
	}
	if containerID == "" || strings.ContainsAny(containerID, "/\x00 \t\n") {
		return nil, fmt.Errorf("cni: invalid container ID %q", containerID)
	}
	st := c.steps[step]
	pluginPath, err := ResolvePluginPath(c.binDir, st.Plugin)
	if err != nil {
		return nil, err
	}

	stdinPayload, err := buildStdinConfig(st.Config, prevResult)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, pluginPath)
	cmd.Stdin = bytes.NewReader(stdinPayload)
	var stdout, stderr limitedBuffer
	stdout.limit = maxPluginOutputBytes
	stderr.limit = maxPluginOutputBytes
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = []string{
		"PATH=" + cleanExecPath,
		"CNI_COMMAND=" + command,
		"CNI_CONTAINERID=" + containerID,
		"CNI_NETNS=" + netnsPath,
		"CNI_IFNAME=" + st.IfName,
		"CNI_PATH=" + filepath.Dir(pluginPath),
	}

	err = cmd.Run()
	outBytes := bytes.TrimSpace(stdout.Bytes())
	errText := strings.TrimSpace(stderr.String())
	if err != nil {
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		// Per CNI SPEC.md, structured error JSON is emitted on stdout, never stderr.
		// Some plugins emit a Result JSON object before skel.PluginMain appends an Error JSON object.
		if cniErr, ok := decodeStdoutCNIError(outBytes); ok {
			cniErr.Plugin = st.Plugin
			cniErr.Command = command
			cniErr.ExitCode = exitCode
			cniErr.Stderr = errText
			return nil, &cniErr
		}
		return nil, &Error{
			Plugin:   st.Plugin,
			Command:  command,
			ExitCode: exitCode,
			Msg:      err.Error(),
			Details:  string(outBytes),
			Stderr:   errText,
		}
	}

	if command == "ADD" {
		if len(outBytes) == 0 {
			return nil, fmt.Errorf("cni: %s ADD produced empty stdout", st.Plugin)
		}
		if !json.Valid(outBytes) {
			return nil, fmt.Errorf("cni: %s ADD produced invalid JSON on stdout", st.Plugin)
		}
		return outBytes, nil
	}
	return nil, nil
}

func decodeStdoutCNIError(outBytes []byte) (Error, bool) {
	if len(outBytes) == 0 {
		return Error{}, false
	}
	dec := json.NewDecoder(bytes.NewReader(outBytes))
	var (
		matched Error
		found   bool
	)
	for dec.More() {
		var raw map[string]json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return Error{}, false
		}
		_, hasCode := raw["code"]
		_, hasMsg := raw["msg"]
		if !hasCode && !hasMsg {
			continue
		}
		blob, err := json.Marshal(raw)
		if err != nil {
			return Error{}, false
		}
		var cand Error
		if err := json.Unmarshal(blob, &cand); err != nil {
			return Error{}, false
		}
		if cand.Code != 0 || cand.Msg != "" {
			matched = cand
			found = true
		}
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return Error{}, false
	}
	return matched, found
}

func buildStdinConfig(baseConfig, prevResult []byte) ([]byte, error) {
	trimmedPrev := bytes.TrimSpace(prevResult)
	if len(trimmedPrev) == 0 {
		return baseConfig, nil
	}
	if !json.Valid(trimmedPrev) {
		return nil, errors.New("cni: prevResult is not valid JSON")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(baseConfig, &obj); err != nil {
		return nil, fmt.Errorf("cni: unmarshal base config: %w", err)
	}
	obj["prevResult"] = json.RawMessage(trimmedPrev)
	return json.Marshal(obj)
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	rem := l.limit - l.buf.Len()
	if rem > 0 {
		if len(p) <= rem {
			l.buf.Write(p)
		} else {
			l.buf.Write(p[:rem])
		}
	}
	return len(p), nil
}

func (l *limitedBuffer) Bytes() []byte  { return l.buf.Bytes() }
func (l *limitedBuffer) String() string { return l.buf.String() }
