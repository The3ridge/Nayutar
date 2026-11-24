package netpol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Kaikei-e/Nayutar/core/sim"
)

// DefaultNftBin is the default absolute path to the nftables CLI binary.
const DefaultNftBin = "/usr/sbin/nft"

const (
	maxNftOutputBytes = 1 << 20
	maxNftErrExcerpt  = 512
	cleanExecPath     = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

	// DefaultNftTimeout bounds one nft invocation.
	DefaultNftTimeout = 30 * time.Second
	nftKillGrace      = time.Second
)

// NftRunner submits an atomic nftables script to the kernel or a simulation double.
type NftRunner interface {
	ExecBatch(ctx context.Context, script string) error
}

// FakeNftRunner is an in-memory NftRunner for unprivileged unit and synctest suites.
type FakeNftRunner struct {
	mu      sync.Mutex
	scripts []string
	Err     error
	OnExec  func(ctx context.Context, script string) error
}

// ExecBatch records script and invokes OnExec or returns Err if configured.
func (f *FakeNftRunner) ExecBatch(ctx context.Context, script string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.scripts = append(f.scripts, script)
	onExec := f.OnExec
	err := f.Err
	f.mu.Unlock()
	if onExec != nil {
		return onExec(ctx, script)
	}
	return err
}

// Scripts returns a copy of all batches passed to ExecBatch.
func (f *FakeNftRunner) Scripts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.scripts)
}

// LastScript returns the most recent batch passed to ExecBatch, or "" if none.
func (f *FakeNftRunner) LastScript() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.scripts) == 0 {
		return ""
	}
	return f.scripts[len(f.scripts)-1]
}

// CallCount returns the number of ExecBatch invocations recorded.
func (f *FakeNftRunner) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.scripts)
}

// ExecNftRunner invokes a validated nft binary via exec with fixed arguments "-f -".
type ExecNftRunner struct {
	binPath    string
	trustedUID uint32
	skipDirs   bool
	clock      sim.Clock
	timeout    time.Duration
}

// ExecOption configures an ExecNftRunner.
type ExecOption func(*ExecNftRunner)

// WithClock sets the clock that measures the per-invocation timeout.
func WithClock(c sim.Clock) ExecOption {
	return func(r *ExecNftRunner) { r.clock = c }
}

// WithTimeout sets the per-invocation timeout; d must be positive.
func WithTimeout(d time.Duration) ExecOption {
	return func(r *ExecNftRunner) { r.timeout = d }
}

// ValidateNftBin resolves symlinks and verifies that binPath is an absolute, root-owned,
// executable regular file that is not writable by group or others, and that every directory
// from its parent up to / is root-owned and not writable by group or others.
func ValidateNftBin(binPath string) (string, error) {
	return validateNftBin(binPath, 0, false)
}

func validateNftBin(binPath string, trustedUID uint32, skipDirs bool) (string, error) {
	if binPath == "" || !filepath.IsAbs(binPath) {
		return "", fmt.Errorf("netpol: nft binary path %q must be an absolute path", binPath)
	}
	clean := filepath.Clean(binPath)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", fmt.Errorf("netpol: resolve nft binary %q: %w", clean, err)
	}
	resolved = filepath.Clean(resolved)
	if !filepath.IsAbs(resolved) || resolved == "/" {
		return "", fmt.Errorf("netpol: invalid resolved nft binary path %q", resolved)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("netpol: stat nft binary %q: %w", resolved, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("netpol: cannot determine owner of nft binary %q", resolved)
	}
	var dirs []dirFact
	for dir := filepath.Dir(resolved); !skipDirs; dir = filepath.Dir(dir) {
		di, err := os.Stat(dir)
		if err != nil {
			return "", fmt.Errorf("netpol: stat nft binary directory %q: %w", dir, err)
		}
		dst, ok := di.Sys().(*syscall.Stat_t)
		if !ok {
			return "", fmt.Errorf("netpol: cannot determine owner of nft binary directory %q", dir)
		}
		dirs = append(dirs, dirFact{path: dir, mode: di.Mode(), uid: dst.Uid})
		if dir == "/" {
			break
		}
	}
	if err := checkNftBinFacts(resolved, fi.Mode(), st.Uid, trustedUID, dirs); err != nil {
		return "", err
	}
	return resolved, nil
}

type dirFact struct {
	path string
	mode fs.FileMode
	uid  uint32
}

func checkNftBinFacts(path string, mode fs.FileMode, uid, trustedUID uint32, dirs []dirFact) error {
	if !mode.IsRegular() {
		return fmt.Errorf("netpol: nft binary %q must be a regular file", path)
	}
	if mode&0o111 == 0 {
		return fmt.Errorf("netpol: nft binary %q is not executable", path)
	}
	if uid != trustedUID {
		return fmt.Errorf("netpol: nft binary %q is owned by uid %d, want %d", path, uid, trustedUID)
	}
	if mode&0o022 != 0 {
		return fmt.Errorf("netpol: nft binary %q must not be writable by group or others (mode %o)", path, mode.Perm())
	}
	for _, d := range dirs {
		if !d.mode.IsDir() {
			return fmt.Errorf("netpol: nft binary parent %q is not a directory", d.path)
		}
		if d.uid != trustedUID {
			return fmt.Errorf("netpol: nft binary directory %q is owned by uid %d, want %d", d.path, d.uid, trustedUID)
		}
		if d.mode&0o022 != 0 {
			return fmt.Errorf("netpol: nft binary directory %q must not be writable by group or others (mode %o)", d.path, d.mode.Perm())
		}
	}
	return nil
}

// NewExecNftRunner validates binPath (defaulting to DefaultNftBin when empty) and constructs an ExecNftRunner.
func NewExecNftRunner(binPath string, opts ...ExecOption) (*ExecNftRunner, error) {
	return newExecNftRunner(binPath, 0, false, opts)
}

func newExecNftRunner(binPath string, trustedUID uint32, skipDirs bool, opts []ExecOption) (*ExecNftRunner, error) {
	if binPath == "" {
		binPath = DefaultNftBin
	}
	resolved, err := validateNftBin(binPath, trustedUID, skipDirs)
	if err != nil {
		return nil, err
	}
	r := &ExecNftRunner{binPath: resolved, trustedUID: trustedUID, skipDirs: skipDirs, clock: sim.RealClock(), timeout: DefaultNftTimeout}
	for _, o := range opts {
		o(r)
	}
	if r.timeout <= 0 {
		return nil, fmt.Errorf("netpol: nft timeout %s must be positive", r.timeout)
	}
	return r, nil
}

// BinPath returns the resolved absolute path of the nft binary.
func (r *ExecNftRunner) BinPath() string {
	return r.binPath
}

// ExecBatch feeds script to "nft -f -" over stdin using a clean environment and bounded output buffers.
func (r *ExecNftRunner) ExecBatch(ctx context.Context, script string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resolved, err := validateNftBin(r.binPath, r.trustedUID, r.skipDirs)
	if err != nil {
		return err
	}
	ctx, cancel := sim.WithTimeout(ctx, r.clock, r.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, resolved, "-f", "-")
	cmd.WaitDelay = nftKillGrace
	cmd.Env = []string{"PATH=" + cleanExecPath}
	cmd.Stdin = strings.NewReader(script)
	stdout := &limitedBuffer{limit: maxNftOutputBytes}
	stderr := &limitedBuffer{limit: maxNftOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("netpol: nft -f - aborted: %w", ctxErr)
		}
		detail := sanitizeDiagnostic(stderr.String())
		if detail == "" {
			detail = sanitizeDiagnostic(stdout.String())
		}
		if detail != "" {
			return fmt.Errorf("netpol: nft -f - failed (exit %d): %s", exitCode, detail)
		}
		return fmt.Errorf("netpol: nft -f - failed (exit %d): %w", exitCode, err)
	}
	return nil
}

// Enforcer compiles PolicyBundles and submits atomic nftables batches via an NftRunner.
type Enforcer struct {
	runner  NftRunner
	sysctl  sim.Disk
	mu      sync.Mutex
	applied *CompiledPolicy
}

// NewEnforcer constructs an Enforcer backed by runner. sysctl must serve BridgeNetfilterSysctls
// relative to SysctlNetDir (a sim.RootedDisk on SysctlNetDir in production).
func NewEnforcer(runner NftRunner, sysctl sim.Disk) (*Enforcer, error) {
	if runner == nil {
		return nil, errors.New("netpol: nft runner must be non-nil")
	}
	if sysctl == nil {
		return nil, errors.New("netpol: sysctl disk must be non-nil")
	}
	return &Enforcer{runner: runner, sysctl: sysctl}, nil
}

// SysctlNetDir is the directory a production sysctl Disk must be rooted at.
const SysctlNetDir = "/proc/sys/net"

// BridgeNetfilterSysctls are the sysctl files, relative to SysctlNetDir, that must read 0 (or be absent).
var BridgeNetfilterSysctls = []string{"bridge/bridge-nf-call-iptables", "bridge/bridge-nf-call-ip6tables"}

// ErrBridgeNetfilterEnabled reports that bridged frames would traverse the inet forward hook.
var ErrBridgeNetfilterEnabled = errors.New("netpol: br_netfilter is enabled")

// CheckBridgeNetfilter refuses to proceed unless the bridge-nf-call sysctls are 0 or absent.
func CheckBridgeNetfilter(d sim.Disk) error {
	for _, name := range BridgeNetfilterSysctls {
		raw, err := d.ReadFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("netpol: read sysctl net.%s: %w", strings.ReplaceAll(name, "/", "."), err)
		}
		if v := strings.TrimSpace(string(raw)); v != "0" {
			return fmt.Errorf("%w: net.%s = %q; set it to 0", ErrBridgeNetfilterEnabled, strings.ReplaceAll(name, "/", "."), v)
		}
	}
	return nil
}

// Apply validates, compiles, and atomically enforces bundle. Invalid bundles fail closed without invoking runner.
func (e *Enforcer) Apply(ctx context.Context, bundle PolicyBundle) (*CompiledPolicy, error) {
	cp, cerr := Compile(bundle)
	if cerr != nil {
		return nil, cerr
	}
	if err := e.ApplyCompiled(ctx, cp); err != nil {
		return nil, err
	}
	return cp, nil
}

// ApplyCompiled re-validates, renders, and atomically applies cp.
func (e *Enforcer) ApplyCompiled(ctx context.Context, cp *CompiledPolicy) error {
	verified, err := verifyDeployable(cp)
	if err != nil {
		return err
	}
	if err := CheckBridgeNetfilter(e.sysctl); err != nil {
		return err
	}
	script := renderVerified(verified)
	if err := e.runner.ExecBatch(ctx, script); err != nil {
		return err
	}
	e.mu.Lock()
	e.applied = verified.Clone()
	e.mu.Unlock()
	return nil
}

// Teardown idempotently deletes both nayutar tables without affecting other host tables.
func (e *Enforcer) Teardown(ctx context.Context) error {
	if err := e.runner.ExecBatch(ctx, RenderTeardownNft()); err != nil {
		return err
	}
	e.mu.Lock()
	e.applied = nil
	e.mu.Unlock()
	return nil
}

// Applied returns a defensive copy of the last policy successfully applied by e, or nil if none is active.
func (e *Enforcer) Applied() *CompiledPolicy {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.applied.Clone()
}

func sanitizeDiagnostic(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(min(len(s), maxNftErrExcerpt))
	for _, r := range s {
		if b.Len() >= maxNftErrExcerpt {
			b.WriteString("...")
			break
		}
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
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

func (l *limitedBuffer) String() string { return l.buf.String() }
