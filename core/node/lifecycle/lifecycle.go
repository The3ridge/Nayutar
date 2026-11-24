// Package lifecycle implements the pure deterministic container unit lifecycle,
// strict LIFO setup/rollback/teardown state machine, and saturating integer backoff.
package lifecycle

import (
	"errors"
	"fmt"
)

// RestartPolicy controls whether a unit restarts after its task exits.
type RestartPolicy uint8

const (
	RestartNever RestartPolicy = iota
	RestartOnFailure
	RestartAlways
)

func (p RestartPolicy) String() string {
	switch p {
	case RestartNever:
		return "never"
	case RestartOnFailure:
		return "onFailure"
	case RestartAlways:
		return "always"
	default:
		return fmt.Sprintf("RestartPolicy(%d)", p)
	}
}

// ShouldRestart reports whether a task exiting with exitCode should restart under policy.
func ShouldRestart(policy RestartPolicy, exitCode uint32) bool {
	switch policy {
	case RestartAlways:
		return true
	case RestartOnFailure:
		return exitCode != 0
	default:
		return false
	}
}

// Spec holds immutable lifecycle parameters for one container unit in integer milliseconds.
type Spec struct {
	RestartPolicy  RestartPolicy
	StopGraceMs    int64
	BackoffBaseMs  int64
	BackoffMaxMs   int64
	BackoffResetMs int64
	CNICount       int
}

// Validate checks that s has valid non-negative integer millisecond bounds and restart policy.
func (s Spec) Validate() error {
	switch s.RestartPolicy {
	case RestartNever, RestartOnFailure, RestartAlways:
	default:
		return fmt.Errorf("lifecycle: unknown restart policy %d", s.RestartPolicy)
	}
	if s.StopGraceMs < 0 {
		return errors.New("lifecycle: stop grace ms must be >= 0")
	}
	if s.BackoffBaseMs < 1 {
		return errors.New("lifecycle: backoff base ms must be >= 1")
	}
	if s.BackoffMaxMs < s.BackoffBaseMs {
		return errors.New("lifecycle: backoff max ms must be >= backoff base ms")
	}
	if s.BackoffResetMs < 0 {
		return errors.New("lifecycle: backoff reset ms must be >= 0")
	}
	if s.CNICount < 0 {
		return errors.New("lifecycle: cni count must be >= 0")
	}
	return nil
}

// BackoffMs computes min(maxMs, baseMs * 2^attempt) using overflow-safe integer arithmetic.
func BackoffMs(baseMs, maxMs int64, attempt int) int64 {
	if baseMs <= 0 || maxMs <= 0 {
		return 0
	}
	d := min(baseMs, maxMs)
	for range max(0, attempt) {
		if d >= maxMs {
			return maxMs
		}
		if d > maxMs/2 {
			return maxMs
		}
		d *= 2
	}
	return d
}

// Phase is the lifecycle phase of a unit.
type Phase uint8

const (
	PhaseInit Phase = iota
	PhaseStarting
	PhaseRunning
	PhaseBackoff
	PhaseRollingBack
	PhaseStopping
	PhaseStopped
)

func (p Phase) String() string {
	switch p {
	case PhaseInit:
		return "init"
	case PhaseStarting:
		return "starting"
	case PhaseRunning:
		return "running"
	case PhaseBackoff:
		return "backoff"
	case PhaseRollingBack:
		return "rolling_back"
	case PhaseStopping:
		return "stopping"
	case PhaseStopped:
		return "stopped"
	default:
		return fmt.Sprintf("Phase(%d)", p)
	}
}

// Resources tracks every external resource held (or attempted for CNI) by the unit.
type Resources struct {
	NetnsCreated     bool
	CNIAdded         int
	ImageReady       bool
	ContainerCreated bool
	TaskCreated      bool
	TaskRunning      bool
	StopSent         bool
	KillSent         bool
}

// Empty reports whether no resources are currently held.
func (r Resources) Empty() bool {
	return !r.NetnsCreated &&
		r.CNIAdded == 0 &&
		!r.ImageReady &&
		!r.ContainerCreated &&
		!r.TaskCreated &&
		!r.TaskRunning &&
		!r.StopSent &&
		!r.KillSent
}

// State is the complete pure state of one unit's lifecycle.
type State struct {
	Phase          Phase
	DesiredRunning bool
	Resources      Resources
	Failed         bool
	CleanupErrors  int
	RestartCount   int
	BackoffAttempt int
	HasExited      bool
	LastExitCode   uint32
	StartedAtMs    int64
	StopSentAtMs   int64
	NextStartAtMs  int64
}

// InitState returns the initial idle state with zero held resources.
func InitState() State {
	return State{Phase: PhaseInit}
}

// WellFormed checks the resource dependency hierarchy and phase invariants for st.
func WellFormed(spec Spec, st State) bool {
	r := st.Resources
	if r.KillSent && !r.StopSent {
		return false
	}
	if r.StopSent && !r.TaskCreated {
		return false
	}
	if r.TaskRunning && !r.TaskCreated {
		return false
	}
	if r.TaskCreated && !r.ContainerCreated {
		return false
	}
	if r.ContainerCreated && !r.ImageReady {
		return false
	}
	if r.ImageReady && (!r.NetnsCreated || r.CNIAdded != spec.CNICount) {
		return false
	}
	if r.CNIAdded < 0 || r.CNIAdded > spec.CNICount {
		return false
	}
	if r.CNIAdded > 0 && !r.NetnsCreated {
		return false
	}
	switch st.Phase {
	case PhaseInit, PhaseStopped:
		return r.Empty() && !st.DesiredRunning
	case PhaseStarting:
		return st.DesiredRunning && !r.TaskRunning && !r.TaskCreated
	case PhaseRunning:
		return r.TaskRunning
	case PhaseBackoff:
		return st.DesiredRunning && !r.TaskRunning && r.ImageReady
	case PhaseStopping, PhaseRollingBack:
		return !st.DesiredRunning && r.NetnsCreated
	default:
		return false
	}
}

// ActionKind identifies the next external step the runner must perform.
type ActionKind uint8

const (
	ActionNone ActionKind = iota
	ActionWait
	ActionCreateNetns
	ActionAddCNI
	ActionEnsureImage
	ActionCreateContainer
	ActionStartTask
	ActionTermTask
	ActionKillTask
	ActionDeleteTask
	ActionDeleteContainer
	ActionDelCNI
	ActionRemoveNetns
)

func (k ActionKind) String() string {
	switch k {
	case ActionNone:
		return "none"
	case ActionWait:
		return "wait"
	case ActionCreateNetns:
		return "create_netns"
	case ActionAddCNI:
		return "add_cni"
	case ActionEnsureImage:
		return "ensure_image"
	case ActionCreateContainer:
		return "create_container"
	case ActionStartTask:
		return "start_task"
	case ActionTermTask:
		return "term_task"
	case ActionKillTask:
		return "kill_task"
	case ActionDeleteTask:
		return "delete_task"
	case ActionDeleteContainer:
		return "delete_container"
	case ActionDelCNI:
		return "del_cni"
	case ActionRemoveNetns:
		return "remove_netns"
	default:
		return fmt.Sprintf("ActionKind(%d)", k)
	}
}

// Action is the next step selected by NextStep.
type Action struct {
	Kind        ActionKind
	CNIIndex    int
	WaitUntilMs int64
}

// NextStep returns the next external action required for st at monotonic timestamp nowMs.
func NextStep(spec Spec, st State, nowMs int64) Action {
	r := st.Resources
	switch st.Phase {
	case PhaseInit, PhaseStopped:
		return Action{Kind: ActionNone}
	case PhaseRunning:
		return Action{Kind: ActionWait}
	case PhaseStarting:
		if !r.NetnsCreated {
			return Action{Kind: ActionCreateNetns}
		}
		if r.CNIAdded < spec.CNICount {
			return Action{Kind: ActionAddCNI, CNIIndex: r.CNIAdded}
		}
		if !r.ImageReady {
			return Action{Kind: ActionEnsureImage}
		}
		if !r.ContainerCreated {
			return Action{Kind: ActionCreateContainer}
		}
		if !r.TaskCreated {
			return Action{Kind: ActionStartTask}
		}
		return Action{Kind: ActionWait}
	case PhaseBackoff:
		if r.TaskRunning {
			return stopTaskAction(spec, st, nowMs)
		}
		if r.TaskCreated {
			return Action{Kind: ActionDeleteTask}
		}
		if r.ContainerCreated {
			return Action{Kind: ActionDeleteContainer}
		}
		if nowMs < st.NextStartAtMs {
			return Action{Kind: ActionWait, WaitUntilMs: st.NextStartAtMs}
		}
		return Action{Kind: ActionCreateContainer}
	case PhaseStopping, PhaseRollingBack:
		if r.TaskRunning {
			return stopTaskAction(spec, st, nowMs)
		}
		if r.TaskCreated {
			return Action{Kind: ActionDeleteTask}
		}
		if r.ContainerCreated {
			return Action{Kind: ActionDeleteContainer}
		}
		if r.CNIAdded > 0 {
			return Action{Kind: ActionDelCNI, CNIIndex: r.CNIAdded - 1}
		}
		if r.NetnsCreated {
			return Action{Kind: ActionRemoveNetns}
		}
		return Action{Kind: ActionNone}
	default:
		return Action{Kind: ActionNone}
	}
}

func stopTaskAction(spec Spec, st State, nowMs int64) Action {
	if !st.Resources.StopSent {
		return Action{Kind: ActionTermTask}
	}
	if !st.Resources.KillSent {
		if graceElapsed(nowMs, st.StopSentAtMs, spec.StopGraceMs) {
			return Action{Kind: ActionKillTask}
		}
		return Action{Kind: ActionWait, WaitUntilMs: st.StopSentAtMs + spec.StopGraceMs}
	}
	return Action{Kind: ActionWait}
}

func graceElapsed(nowMs, sentAtMs, graceMs int64) bool {
	return nowMs >= sentAtMs && nowMs-sentAtMs >= graceMs
}

// EventKind identifies a discrete transition input to ApplyEvent.
type EventKind uint8

const (
	EvStartRequested EventKind = iota
	EvStopRequested
	EvNetnsCreated
	EvNetnsCreateFailed
	EvCNIAdded
	EvCNIAddFailed
	EvImageReady
	EvImageFailed
	EvContainerCreated
	EvContainerCreateFailed
	EvTaskStarted
	EvTaskStartFailed
	EvTermSent
	EvKillSent
	EvTaskExited
	EvTaskDeleted
	EvContainerDeleted
	EvCNIDeleted
	EvNetnsRemoved
)

func (k EventKind) String() string {
	switch k {
	case EvStartRequested:
		return "start_requested"
	case EvStopRequested:
		return "stop_requested"
	case EvNetnsCreated:
		return "netns_created"
	case EvNetnsCreateFailed:
		return "netns_create_failed"
	case EvCNIAdded:
		return "cni_added"
	case EvCNIAddFailed:
		return "cni_add_failed"
	case EvImageReady:
		return "image_ready"
	case EvImageFailed:
		return "image_failed"
	case EvContainerCreated:
		return "container_created"
	case EvContainerCreateFailed:
		return "container_create_failed"
	case EvTaskStarted:
		return "task_started"
	case EvTaskStartFailed:
		return "task_start_failed"
	case EvTermSent:
		return "term_sent"
	case EvKillSent:
		return "kill_sent"
	case EvTaskExited:
		return "task_exited"
	case EvTaskDeleted:
		return "task_deleted"
	case EvContainerDeleted:
		return "container_deleted"
	case EvCNIDeleted:
		return "cni_deleted"
	case EvNetnsRemoved:
		return "netns_removed"
	default:
		return fmt.Sprintf("EventKind(%d)", k)
	}
}

// Event is one discrete observation fed into ApplyEvent.
type Event struct {
	Kind     EventKind
	NowMs    int64
	CNIIndex int
	ExitCode uint32
	OK       bool
}

// ApplyEvent advances st deterministically for ev under spec. Invalid or stale events are no-ops.
func ApplyEvent(spec Spec, st State, ev Event) State {
	if st.Phase == PhaseStopped {
		return st
	}
	switch ev.Kind {
	case EvStartRequested:
		if st.Phase != PhaseInit {
			return st
		}
		st.DesiredRunning = true
		st.Phase = PhaseStarting
		return st

	case EvStopRequested:
		switch st.Phase {
		case PhaseInit:
			st.DesiredRunning = false
			st.Phase = PhaseStopped
			return st
		case PhaseStarting:
			st.DesiredRunning = false
			if st.Resources.Empty() {
				st.Phase = PhaseStopped
				return st
			}
			if !st.Resources.ContainerCreated {
				st.Resources.ImageReady = false
			}
			st.Phase = PhaseStopping
			return st
		case PhaseRunning:
			st.DesiredRunning = false
			st.Phase = PhaseStopping
			return st
		case PhaseBackoff:
			st.DesiredRunning = false
			if !st.Resources.ContainerCreated {
				st.Resources.ImageReady = false
			}
			st.Phase = PhaseStopping
			return st
		default:
			return st
		}

	case EvNetnsCreated:
		if st.Phase != PhaseStarting || !st.Resources.Empty() {
			return st
		}
		st.Resources.NetnsCreated = true
		return st

	case EvNetnsCreateFailed:
		if st.Phase != PhaseStarting || !st.Resources.Empty() {
			return st
		}
		st.Failed = true
		st.DesiredRunning = false
		st.Phase = PhaseStopped
		return st

	case EvCNIAdded:
		if st.Phase != PhaseStarting || !st.Resources.NetnsCreated || st.Resources.ImageReady ||
			st.Resources.ContainerCreated || st.Resources.TaskCreated || st.Resources.TaskRunning {
			return st
		}
		if st.Resources.CNIAdded >= spec.CNICount || ev.CNIIndex != st.Resources.CNIAdded {
			return st
		}
		st.Resources.CNIAdded++
		return st

	case EvCNIAddFailed:
		if st.Phase != PhaseStarting || !st.Resources.NetnsCreated || st.Resources.ImageReady ||
			st.Resources.ContainerCreated || st.Resources.TaskCreated || st.Resources.TaskRunning {
			return st
		}
		if st.Resources.CNIAdded >= spec.CNICount || ev.CNIIndex != st.Resources.CNIAdded {
			return st
		}
		// Count the attempted step so rollback issues idempotent DEL for it too.
		st.Resources.CNIAdded++
		st.Failed = true
		st.DesiredRunning = false
		st.Phase = PhaseRollingBack
		return st

	case EvImageReady:
		if st.Phase != PhaseStarting || !st.Resources.NetnsCreated || st.Resources.CNIAdded != spec.CNICount ||
			st.Resources.ImageReady || st.Resources.ContainerCreated || st.Resources.TaskCreated || st.Resources.TaskRunning {
			return st
		}
		st.Resources.ImageReady = true
		return st

	case EvImageFailed:
		if st.Phase != PhaseStarting || !st.Resources.NetnsCreated || st.Resources.CNIAdded != spec.CNICount ||
			st.Resources.ImageReady || st.Resources.ContainerCreated || st.Resources.TaskCreated || st.Resources.TaskRunning {
			return st
		}
		st.Failed = true
		st.DesiredRunning = false
		st.Phase = PhaseRollingBack
		return st

	case EvContainerCreated:
		if !canCreateContainer(spec, st, ev.NowMs) {
			return st
		}
		st.Resources.ContainerCreated = true
		st.Phase = PhaseStarting
		return st

	case EvContainerCreateFailed:
		if !canCreateContainer(spec, st, ev.NowMs) {
			return st
		}
		st.Resources.ImageReady = false
		st.Failed = true
		st.DesiredRunning = false
		st.Phase = PhaseRollingBack
		return st

	case EvTaskStarted:
		if st.Phase != PhaseStarting || !st.Resources.ContainerCreated || st.Resources.TaskCreated || st.Resources.TaskRunning {
			return st
		}
		if st.HasExited {
			st.BackoffAttempt++
		}
		st.Resources.TaskCreated = true
		st.Resources.TaskRunning = true
		st.StartedAtMs = ev.NowMs
		st.Phase = PhaseRunning
		return st

	case EvTaskStartFailed:
		if st.Phase != PhaseStarting || !st.Resources.ContainerCreated || st.Resources.TaskCreated || st.Resources.TaskRunning {
			return st
		}
		st.Failed = true
		st.DesiredRunning = false
		st.Phase = PhaseRollingBack
		return st

	case EvTermSent:
		if !isTeardownPhase(st.Phase) || !st.Resources.TaskRunning || st.Resources.StopSent {
			return st
		}
		st.Resources.StopSent = true
		st.StopSentAtMs = ev.NowMs
		return st

	case EvKillSent:
		if !isTeardownPhase(st.Phase) || !st.Resources.TaskRunning || !st.Resources.StopSent || st.Resources.KillSent {
			return st
		}
		if !graceElapsed(ev.NowMs, st.StopSentAtMs, spec.StopGraceMs) {
			return st
		}
		st.Resources.KillSent = true
		return st

	case EvTaskExited:
		if !st.Resources.TaskRunning {
			return st
		}
		if st.Phase != PhaseRunning && !isTeardownPhase(st.Phase) {
			return st
		}
		st.Resources.TaskRunning = false
		st.HasExited = true
		st.LastExitCode = ev.ExitCode

		if graceElapsed(ev.NowMs, st.StartedAtMs, spec.BackoffResetMs) {
			st.BackoffAttempt = 0
		}

		if st.Phase == PhaseRunning {
			if st.DesiredRunning && ShouldRestart(spec.RestartPolicy, ev.ExitCode) {
				delay := BackoffMs(spec.BackoffBaseMs, spec.BackoffMaxMs, st.BackoffAttempt)
				st.NextStartAtMs = ev.NowMs + delay
				st.RestartCount++
				st.Phase = PhaseBackoff
			} else {
				st.DesiredRunning = false
				st.Phase = PhaseStopping
			}
		}
		return st

	case EvTaskDeleted:
		if st.Phase != PhaseBackoff && !isTeardownPhase(st.Phase) {
			return st
		}
		if st.Resources.TaskRunning || !st.Resources.TaskCreated {
			return st
		}
		st.Resources.TaskCreated = false
		st.Resources.StopSent = false
		st.Resources.KillSent = false
		if !ev.OK {
			st.CleanupErrors++
		}
		return st

	case EvContainerDeleted:
		if st.Phase != PhaseBackoff && !isTeardownPhase(st.Phase) {
			return st
		}
		if st.Resources.TaskRunning || st.Resources.TaskCreated || !st.Resources.ContainerCreated {
			return st
		}
		st.Resources.ContainerCreated = false
		if isTeardownPhase(st.Phase) {
			st.Resources.ImageReady = false
		}
		if !ev.OK {
			st.CleanupErrors++
		}
		return st

	case EvCNIDeleted:
		if !isTeardownPhase(st.Phase) {
			return st
		}
		if st.Resources.TaskRunning || st.Resources.TaskCreated || st.Resources.ContainerCreated ||
			st.Resources.ImageReady || st.Resources.CNIAdded <= 0 || ev.CNIIndex != st.Resources.CNIAdded-1 {
			return st
		}
		st.Resources.CNIAdded--
		if !ev.OK {
			st.CleanupErrors++
		}
		return st

	case EvNetnsRemoved:
		if !isTeardownPhase(st.Phase) {
			return st
		}
		if st.Resources.TaskRunning || st.Resources.TaskCreated || st.Resources.StopSent || st.Resources.KillSent ||
			st.Resources.ContainerCreated || st.Resources.ImageReady || st.Resources.CNIAdded != 0 || !st.Resources.NetnsCreated {
			return st
		}
		st.Resources.NetnsCreated = false
		if !ev.OK {
			st.CleanupErrors++
		}
		st.Phase = PhaseStopped
		return st

	default:
		return st
	}
}

func isTeardownPhase(p Phase) bool {
	return p == PhaseStopping || p == PhaseRollingBack
}

func canCreateContainer(spec Spec, st State, nowMs int64) bool {
	r := st.Resources
	if !r.NetnsCreated || r.CNIAdded != spec.CNICount || !r.ImageReady ||
		r.ContainerCreated || r.TaskCreated || r.TaskRunning {
		return false
	}
	if st.Phase == PhaseStarting {
		return true
	}
	return st.Phase == PhaseBackoff && nowMs >= st.NextStartAtMs
}
