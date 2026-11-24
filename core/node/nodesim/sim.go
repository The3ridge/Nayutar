// Package nodesim provides a deterministic single-seed simulator and invariant-checking
// fake NetnsManager, CNIRunner, and Runtime for core/node.
package nodesim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Kaikei-e/Nayutar/core/node"
	"github.com/Kaikei-e/Nayutar/core/node/lifecycle"
	"github.com/Kaikei-e/Nayutar/core/sim"
)

// TaskScript controls one simulated task execution's lifetime and signal behavior.
type TaskScript struct {
	RunDuration   time.Duration // <= 0 means run until signaled
	ExitCode      uint32
	IgnoreSIGTERM bool
	TermDelay     time.Duration
}

// Faults configures deterministic failure injection for World.
type Faults struct {
	FailCreateNetns       bool
	FailAddCNIStep        int  // < 0 disables
	LeakIPBeforeStep1Fail bool // allocates IPAM lease before step 1 fails to test rollback DEL
	FailEnsureImage       bool
	FailCreateContainer   bool
	FailStartTask         bool
	FailDeleteTask        bool
	FailDeleteContainer   bool
	FailDelCNIStep        int // < 0 disables
	FailRemoveNetns       bool
	VanishNetnsAfterStart bool // simulates externally disappeared netns before teardown
	TaskBehavior          func(attempt int, spec node.ContainerSpec) TaskScript
}

// DefaultFaults returns a Faults value with no failures enabled.
func DefaultFaults() Faults {
	return Faults{
		FailAddCNIStep: -1,
		FailDelCNIStep: -1,
	}
}

// OpRecord records one external adapter operation observed by World.
type OpRecord struct {
	TimeMs int64
	Op     string
	Detail string
	OK     bool
}

type simTask struct {
	running bool
	exited  bool
	exitCh  chan node.ExitResult
	sigCh   chan syscall.Signal
	signals []syscall.Signal
}

// World implements node.NetnsManager, node.CNIRunner, and node.Runtime in memory
// while checking strict setup/teardown ordering invariants and leak absence.
type World struct {
	mu       sync.Mutex
	clock    node.Clock
	start    time.Time
	cniCount int
	faults   Faults

	netns         map[string]string
	netnsVanished map[string]bool
	cniSteps      map[string]map[int]bool
	ipamLeases    map[string]string
	images        map[string]bool
	containers    map[string]node.ContainerSpec
	tasks         map[string]*simTask
	startAttempts int
	nextIP        int
	ops           []OpRecord
	violations    []string
	activeWaiters sync.WaitGroup
}

// NewWorld constructs an in-memory simulated environment with cniCount plugins and fault rules.
func NewWorld(clk node.Clock, cniCount int, faults Faults) *World {
	if clk == nil {
		clk = node.RealClock()
	}
	return &World{
		clock:         clk,
		start:         clk.Now(),
		cniCount:      cniCount,
		faults:        faults,
		netns:         make(map[string]string),
		netnsVanished: make(map[string]bool),
		cniSteps:      make(map[string]map[int]bool),
		ipamLeases:    make(map[string]string),
		images:        make(map[string]bool),
		containers:    make(map[string]node.ContainerSpec),
		tasks:         make(map[string]*simTask),
		nextIP:        2,
	}
}

func (w *World) nowMsLocked() int64 {
	return max(0, w.clock.Now().Sub(w.start).Milliseconds())
}

func (w *World) recordLocked(op, detail string, ok bool) {
	w.ops = append(w.ops, OpRecord{
		TimeMs: w.nowMsLocked(),
		Op:     op,
		Detail: detail,
		OK:     ok,
	})
}

func (w *World) violateLocked(msg string) {
	w.violations = append(w.violations, msg)
}

// Ops returns a snapshot copy of all recorded adapter operations.
func (w *World) Ops() []OpRecord {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]OpRecord, len(w.ops))
	copy(out, w.ops)
	return out
}

// Violations returns any safety or ordering invariant violations detected during execution.
func (w *World) Violations() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.violations))
	copy(out, w.violations)
	return out
}

// TraceHash computes a deterministic SHA-256 digest of the operation trace.
func (w *World) TraceHash() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	h := sha256.New()
	var buf []byte
	for _, op := range w.ops {
		buf = buf[:0]
		buf = strconv.AppendInt(buf, op.TimeMs, 10)
		buf = append(buf, '|')
		buf = append(buf, op.Op...)
		buf = append(buf, '|')
		buf = append(buf, op.Detail...)
		buf = append(buf, '|')
		if op.OK {
			buf = append(buf, '1')
		} else {
			buf = append(buf, '0')
		}
		buf = append(buf, '\n')
		h.Write(buf)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// AssertClean waits for task goroutines and checks that no resources leaked and no invariants broke.
func (w *World) AssertClean() error {
	w.activeWaiters.Wait()
	w.mu.Lock()
	defer w.mu.Unlock()

	var problems []string
	problems = append(problems, w.violations...)
	if len(w.netns) > 0 {
		problems = append(problems, fmt.Sprintf("leaked netns: %v", w.netns))
	}
	for id, steps := range w.cniSteps {
		if len(steps) > 0 {
			problems = append(problems, fmt.Sprintf("leaked CNI steps for %s: %v", id, steps))
		}
	}
	if len(w.ipamLeases) > 0 {
		problems = append(problems, fmt.Sprintf("leaked IPAM leases: %v", w.ipamLeases))
	}
	if len(w.containers) > 0 {
		problems = append(problems, fmt.Sprintf("leaked containers: %d", len(w.containers)))
	}
	if len(w.tasks) > 0 {
		problems = append(problems, fmt.Sprintf("leaked tasks: %d", len(w.tasks)))
	}
	if len(problems) > 0 {
		return errors.New("nodesim invariant failure: " + strings.Join(problems, "; "))
	}
	return nil
}

// Create implements node.NetnsManager.
func (w *World) Create(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, exists := w.netns[name]; exists {
		w.violateLocked("duplicate netns create for " + name)
	}
	if w.faults.FailCreateNetns {
		w.recordLocked("create_netns", name, false)
		return "", errors.New("injected create_netns failure")
	}
	path := "/sim/netns/" + name
	w.netns[name] = path
	w.recordLocked("create_netns", path, true)
	return path, nil
}

// Remove implements node.NetnsManager.
func (w *World) Remove(_ context.Context, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.containers[name]; ok {
		w.violateLocked("remove_netns while container still exists: " + name)
	}
	if _, ok := w.tasks[name]; ok {
		w.violateLocked("remove_netns while task still exists: " + name)
	}
	if len(w.cniSteps[name]) > 0 {
		w.violateLocked(fmt.Sprintf("remove_netns while CNI steps still active for %s: %v", name, w.cniSteps[name]))
	}
	if _, ok := w.ipamLeases[name]; ok {
		w.violateLocked("remove_netns while IPAM lease still allocated: " + name)
	}
	if w.faults.FailRemoveNetns {
		w.recordLocked("remove_netns", name, false)
		return errors.New("injected remove_netns failure")
	}
	delete(w.netns, name)
	delete(w.netnsVanished, name)
	w.recordLocked("remove_netns", name, true)
	return nil
}

// Count implements node.CNIRunner.
func (w *World) Count() int {
	return w.cniCount
}

// Add implements node.CNIRunner.
func (w *World) Add(ctx context.Context, step int, containerID, netnsPath string, prevResult []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.netns[containerID] != netnsPath {
		w.violateLocked(fmt.Sprintf("cni_add(%d) without matching netns for %s", step, containerID))
	}
	steps := w.cniSteps[containerID]
	if steps == nil {
		steps = make(map[int]bool)
		w.cniSteps[containerID] = steps
	}
	for i := range step {
		if !steps[i] {
			w.violateLocked(fmt.Sprintf("cni_add(%d) called before step %d for %s", step, i, containerID))
		}
	}
	if step == 1 && w.faults.FailAddCNIStep == 1 && w.faults.LeakIPBeforeStep1Fail {
		w.ipamLeases[containerID] = fmt.Sprintf("10.88.0.%d/24", w.nextIP)
		w.nextIP++
	}
	if step == w.faults.FailAddCNIStep {
		w.recordLocked("add_cni", fmt.Sprintf("%s:%d", containerID, step), false)
		return nil, fmt.Errorf("injected cni add failure at step %d", step)
	}
	steps[step] = true
	resObj := map[string]any{
		"cniVersion": "1.0.0",
		"step":       step,
		"hasPrev":    len(prevResult) > 0,
	}
	if step == 1 {
		ip := fmt.Sprintf("10.88.0.%d/24", w.nextIP)
		w.nextIP++
		w.ipamLeases[containerID] = ip
		resObj["ip"] = ip
	}
	raw, _ := json.Marshal(resObj)
	w.recordLocked("add_cni", fmt.Sprintf("%s:%d", containerID, step), true)
	return raw, nil
}

// Del implements node.CNIRunner.
func (w *World) Del(_ context.Context, step int, containerID, _ string, _ []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.containers[containerID]; ok {
		w.violateLocked(fmt.Sprintf("cni_del(%d) while container still exists: %s", step, containerID))
	}
	if _, ok := w.tasks[containerID]; ok {
		w.violateLocked(fmt.Sprintf("cni_del(%d) while task still exists: %s", step, containerID))
	}
	for higher := step + 1; higher < w.cniCount; higher++ {
		if w.cniSteps[containerID][higher] {
			w.violateLocked(fmt.Sprintf("cni_del(%d) called before higher step %d for %s", step, higher, containerID))
		}
	}
	// IPAM release must succeed even if netns vanished externally.
	if step == 1 {
		delete(w.ipamLeases, containerID)
	}
	if steps := w.cniSteps[containerID]; steps != nil {
		delete(steps, step)
		if len(steps) == 0 {
			delete(w.cniSteps, containerID)
		}
	}
	if step == w.faults.FailDelCNIStep {
		w.recordLocked("del_cni", fmt.Sprintf("%s:%d", containerID, step), false)
		return fmt.Errorf("injected cni del failure at step %d", step)
	}
	w.recordLocked("del_cni", fmt.Sprintf("%s:%d", containerID, step), true)
	return nil
}

// EnsureImage implements node.Runtime.
func (w *World) EnsureImage(ctx context.Context, image string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.faults.FailEnsureImage {
		w.recordLocked("ensure_image", image, false)
		return errors.New("injected ensure_image failure")
	}
	w.images[image] = true
	w.recordLocked("ensure_image", image, true)
	return nil
}

// CreateContainer implements node.Runtime.
func (w *World) CreateContainer(ctx context.Context, spec node.ContainerSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.netnsVanished[spec.ID] {
		w.recordLocked("create_container", spec.ID, false)
		return errors.New("netns disappeared externally")
	}
	if w.netns[spec.ID] != spec.NetnsPath {
		w.violateLocked("create_container without active netns: " + spec.ID)
	}
	if len(w.cniSteps[spec.ID]) != w.cniCount {
		w.violateLocked(fmt.Sprintf("create_container with %d/%d CNI steps for %s", len(w.cniSteps[spec.ID]), w.cniCount, spec.ID))
	}
	if !w.images[spec.Image] {
		w.violateLocked("create_container before ensure_image: " + spec.Image)
	}
	if _, exists := w.containers[spec.ID]; exists {
		w.violateLocked("duplicate create_container: " + spec.ID)
	}
	if w.faults.FailCreateContainer {
		w.recordLocked("create_container", spec.ID, false)
		return errors.New("injected create_container failure")
	}
	w.containers[spec.ID] = spec
	w.recordLocked("create_container", spec.ID, true)
	return nil
}

// StartTask implements node.Runtime.
func (w *World) StartTask(ctx context.Context, containerID string) (<-chan node.ExitResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.mu.Lock()
	spec, ok := w.containers[containerID]
	if !ok {
		w.violateLocked("start_task without container: " + containerID)
	}
	if _, exists := w.tasks[containerID]; exists {
		w.violateLocked("duplicate start_task: " + containerID)
	}
	attempt := w.startAttempts
	w.startAttempts++
	if w.faults.FailStartTask {
		w.recordLocked("start_task", containerID, false)
		w.mu.Unlock()
		return nil, errors.New("injected start_task failure")
	}

	script := TaskScript{RunDuration: 0, ExitCode: 0}
	if w.faults.TaskBehavior != nil {
		script = w.faults.TaskBehavior(attempt, spec)
	}
	tk := &simTask{
		running: true,
		exitCh:  make(chan node.ExitResult, 1),
		sigCh:   make(chan syscall.Signal, 4),
	}
	w.tasks[containerID] = tk
	if w.faults.VanishNetnsAfterStart && attempt == 0 {
		delete(w.netns, containerID)
		w.netnsVanished[containerID] = true
	}
	w.recordLocked("start_task", fmt.Sprintf("%s#%d", containerID, attempt), true)
	w.activeWaiters.Add(1)
	w.mu.Unlock()

	go w.runTask(containerID, tk, script)
	return tk.exitCh, nil
}

func (w *World) runTask(containerID string, tk *simTask, script TaskScript) {
	defer w.activeWaiters.Done()

	var runTimer <-chan time.Time
	if script.RunDuration > 0 {
		runTimer = w.clock.After(script.RunDuration)
	}

	var exitCode uint32
	var termTimer <-chan time.Time
	for {
		select {
		case <-runTimer:
			exitCode = script.ExitCode
			goto finish
		case <-termTimer:
			exitCode = 143
			goto finish
		case sig := <-tk.sigCh:
			if sig == syscall.SIGKILL {
				exitCode = 137
				goto finish
			}
			if sig == syscall.SIGTERM && !script.IgnoreSIGTERM {
				if script.TermDelay <= 0 {
					exitCode = 143
					goto finish
				}
				termTimer = w.clock.After(script.TermDelay)
			}
		}
	}

finish:
	w.mu.Lock()
	tk.running = false
	tk.exited = true
	w.recordLocked("task_exited", fmt.Sprintf("%s:%d", containerID, exitCode), true)
	w.mu.Unlock()

	tk.exitCh <- node.ExitResult{ExitCode: exitCode}
	close(tk.exitCh)
}

// SignalTask implements node.Runtime.
func (w *World) SignalTask(_ context.Context, containerID string, sig syscall.Signal) error {
	w.mu.Lock()
	tk, ok := w.tasks[containerID]
	if !ok {
		w.recordLocked("signal_task", fmt.Sprintf("%s:%d", containerID, sig), true)
		w.mu.Unlock()
		return nil
	}
	tk.signals = append(tk.signals, sig)
	w.recordLocked("signal_task", fmt.Sprintf("%s:%d", containerID, sig), true)
	w.mu.Unlock()

	select {
	case tk.sigCh <- sig:
	default:
	}
	return nil
}

// DeleteTask implements node.Runtime.
func (w *World) DeleteTask(_ context.Context, containerID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	tk, ok := w.tasks[containerID]
	if ok && tk.running {
		w.violateLocked("delete_task while task still running: " + containerID)
	}
	delete(w.tasks, containerID)
	if w.faults.FailDeleteTask {
		w.recordLocked("delete_task", containerID, false)
		return errors.New("injected delete_task failure")
	}
	w.recordLocked("delete_task", containerID, true)
	return nil
}

// DeleteContainer implements node.Runtime.
func (w *World) DeleteContainer(_ context.Context, containerID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.tasks[containerID]; ok {
		w.violateLocked("delete_container while task still exists: " + containerID)
	}
	delete(w.containers, containerID)
	if w.faults.FailDeleteContainer {
		w.recordLocked("delete_container", containerID, false)
		return errors.New("injected delete_container failure")
	}
	w.recordLocked("delete_container", containerID, true)
	return nil
}

// SimReport summarizes one deterministic simulation run.
type SimReport struct {
	Seed       uint64
	FinalState lifecycle.State
	Ops        []OpRecord
	TraceHash  string
	RunErr     error
}

// RunSeed executes a single seed-reproducible workload and fault injection scenario against node.Runner.
// Callers should invoke RunSeed inside testing/synctest.Test so virtual time runs instantly and deterministically.
func RunSeed(seed uint64) (SimReport, error) {
	rng := sim.NewDetRandStream(seed, 0x6e6f646573696d)
	policies := []lifecycle.RestartPolicy{
		lifecycle.RestartNever,
		lifecycle.RestartOnFailure,
		lifecycle.RestartAlways,
	}
	policy := policies[sim.IntN(rng, len(policies))]
	stopGraceMs := int64(50 + sim.IntN(rng, 450))
	backoffBaseMs := int64(20 + sim.IntN(rng, 180))
	backoffMaxMs := backoffBaseMs * int64(1+sim.IntN(rng, 8))
	backoffResetMs := int64(200 + sim.IntN(rng, 1000))

	faults := DefaultFaults()
	switch r := sim.IntN(rng, 100); {
	case r < 10:
		faults.FailCreateNetns = true
	case r < 20:
		faults.FailAddCNIStep = 0
	case r < 30:
		faults.FailAddCNIStep = 1
		faults.LeakIPBeforeStep1Fail = true
	case r < 40:
		faults.FailEnsureImage = true
	case r < 50:
		faults.FailCreateContainer = true
	case r < 60:
		faults.FailStartTask = true
	case r < 70:
		faults.VanishNetnsAfterStart = true
	}

	// Pre-generate deterministic per-attempt task scripts so concurrent goroutines never share rng.
	const maxScripts = 16
	scripts := make([]TaskScript, maxScripts)
	for i := range maxScripts {
		durMs := int64(0)
		if sim.IntN(rng, 100) < 80 {
			if sim.IntN(rng, 100) < 35 {
				durMs = backoffResetMs + int64(sim.IntN(rng, 300))
			} else {
				durMs = int64(10 + sim.IntN(rng, int(max(int64(20), backoffResetMs/2))))
			}
		}
		code := uint32(0)
		if sim.IntN(rng, 100) < 60 {
			code = uint32(1 + sim.IntN(rng, 5))
		}
		scripts[i] = TaskScript{
			RunDuration:   time.Duration(durMs) * time.Millisecond,
			ExitCode:      code,
			IgnoreSIGTERM: sim.IntN(rng, 100) < 40,
			TermDelay:     time.Duration(sim.IntN(rng, 30)) * time.Millisecond,
		}
	}
	faults.TaskBehavior = func(attempt int, _ node.ContainerSpec) TaskScript {
		return scripts[attempt%len(scripts)]
	}

	stopAfter := time.Duration(300+sim.IntN(rng, 3000)) * time.Millisecond
	world := NewWorld(node.RealClock(), 2, faults)

	unit := node.UnitSpec{
		ID:    fmt.Sprintf("unit-%d", seed),
		Image: "oci.local/sim:v1",
		Lifecycle: lifecycle.Spec{
			RestartPolicy:  policy,
			StopGraceMs:    stopGraceMs,
			BackoffBaseMs:  backoffBaseMs,
			BackoffMaxMs:   backoffMaxMs,
			BackoffResetMs: backoffResetMs,
			CNICount:       2,
		},
	}

	var transitionViolations []string
	runner, err := node.NewRunner(node.RunnerConfig{
		Unit:    unit,
		Runtime: world,
		Netns:   world,
		CNI:     world,
		OnTransition: func(st lifecycle.State) {
			if !lifecycle.WellFormed(unit.Lifecycle, st) {
				transitionViolations = append(transitionViolations, fmt.Sprintf("non-well-formed state: %+v", st))
			}
		},
	})
	if err != nil {
		return SimReport{}, err
	}

	ctx, cancel := sim.WithTimeout(context.Background(), node.RealClock(), stopAfter)
	defer cancel()

	finalState, runErr := runner.Run(ctx)
	if err := world.AssertClean(); err != nil {
		return SimReport{Seed: seed, FinalState: finalState, Ops: world.Ops(), TraceHash: world.TraceHash(), RunErr: runErr}, err
	}
	if len(transitionViolations) > 0 {
		return SimReport{Seed: seed, FinalState: finalState, Ops: world.Ops(), TraceHash: world.TraceHash(), RunErr: runErr},
			errors.New(strings.Join(transitionViolations, "; "))
	}
	if finalState.Phase != lifecycle.PhaseStopped || !finalState.Resources.Empty() {
		return SimReport{Seed: seed, FinalState: finalState, Ops: world.Ops(), TraceHash: world.TraceHash(), RunErr: runErr},
			fmt.Errorf("seed %d: final state not cleanly stopped: %+v", seed, finalState)
	}
	return SimReport{
		Seed:       seed,
		FinalState: finalState,
		Ops:        world.Ops(),
		TraceHash:  world.TraceHash(),
		RunErr:     runErr,
	}, nil
}
