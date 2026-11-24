//go:build drt

package drt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/node/lifecycle"
)

type nodeSpecWire struct {
	RestartPolicy  string `json:"restart_policy"`
	StopGraceMs    int64  `json:"stop_grace_ms"`
	BackoffBaseMs  int64  `json:"backoff_base_ms"`
	BackoffMaxMs   int64  `json:"backoff_max_ms"`
	BackoffResetMs int64  `json:"backoff_reset_ms"`
	CNICount       int    `json:"cni_count"`
}

type nodeOp struct {
	Op       string
	Kind     string
	NowMs    int64
	CNIIndex int
	ExitCode uint32
	OK       bool
	BaseMs   int64
	MaxMs    int64
	Attempt  int
}

func (o nodeOp) MarshalJSON() ([]byte, error) {
	switch o.Op {
	case "event":
		return json.Marshal(struct {
			Op       string `json:"op"`
			Kind     string `json:"kind"`
			NowMs    int64  `json:"now_ms"`
			CNIIndex int    `json:"cni_index"`
			ExitCode uint32 `json:"exit_code"`
			OK       bool   `json:"ok"`
		}{o.Op, o.Kind, o.NowMs, o.CNIIndex, o.ExitCode, o.OK})
	case "next_step":
		return json.Marshal(struct {
			Op    string `json:"op"`
			NowMs int64  `json:"now_ms"`
		}{o.Op, o.NowMs})
	case "backoff":
		return json.Marshal(struct {
			Op      string `json:"op"`
			BaseMs  int64  `json:"base_ms"`
			MaxMs   int64  `json:"max_ms"`
			Attempt int    `json:"attempt"`
		}{o.Op, o.BaseMs, o.MaxMs, o.Attempt})
	default:
		return nil, fmt.Errorf("unknown node op %q", o.Op)
	}
}

type nodeCase struct {
	ID   int          `json:"id"`
	Spec nodeSpecWire `json:"spec"`
	Ops  []nodeOp     `json:"ops"`
}

type nodeActionWire struct {
	Kind        string `json:"kind"`
	CNIIndex    int    `json:"cni_index"`
	WaitUntilMs int64  `json:"wait_until_ms"`
}

type nodeResourcesWire struct {
	NetnsCreated     bool `json:"netns_created"`
	CNIAdded         int  `json:"cni_added"`
	ImageReady       bool `json:"image_ready"`
	ContainerCreated bool `json:"container_created"`
	TaskCreated      bool `json:"task_created"`
	TaskRunning      bool `json:"task_running"`
	StopSent         bool `json:"stop_sent"`
	KillSent         bool `json:"kill_sent"`
}

type nodeStateWire struct {
	Phase          string            `json:"phase"`
	DesiredRunning bool              `json:"desired_running"`
	Resources      nodeResourcesWire `json:"resources"`
	Failed         bool              `json:"failed"`
	CleanupErrors  int               `json:"cleanup_errors"`
	RestartCount   int               `json:"restart_count"`
	BackoffAttempt int               `json:"backoff_attempt"`
	HasExited      bool              `json:"has_exited"`
	LastExitCode   uint32            `json:"last_exit_code"`
	StartedAtMs    int64             `json:"started_at_ms"`
	StopSentAtMs   int64             `json:"stop_sent_at_ms"`
	NextStartAtMs  int64             `json:"next_start_at_ms"`
}

type nodeStep struct {
	Action     nodeActionWire `json:"action"`
	State      nodeStateWire  `json:"state"`
	WellFormed bool           `json:"well_formed"`
	BackoffMs  int64          `json:"backoff_ms"`
}

type nodeResult struct {
	ID    int        `json:"id"`
	Steps []nodeStep `json:"steps"`
}

var (
	nodeRestartPolicies = []string{"never", "onFailure", "always"}
	nodePhases          = []string{"init", "starting", "running", "backoff", "rolling_back", "stopping", "stopped"}
	nodeActionKinds     = []string{
		"none", "wait", "create_netns", "add_cni", "ensure_image", "create_container",
		"start_task", "term_task", "kill_task", "delete_task", "delete_container",
		"del_cni", "remove_netns",
	}
	nodeEventKinds = []lifecycle.EventKind{
		lifecycle.EvStartRequested,
		lifecycle.EvStopRequested,
		lifecycle.EvNetnsCreated,
		lifecycle.EvNetnsCreateFailed,
		lifecycle.EvCNIAdded,
		lifecycle.EvCNIAddFailed,
		lifecycle.EvImageReady,
		lifecycle.EvImageFailed,
		lifecycle.EvContainerCreated,
		lifecycle.EvContainerCreateFailed,
		lifecycle.EvTaskStarted,
		lifecycle.EvTaskStartFailed,
		lifecycle.EvTermSent,
		lifecycle.EvKillSent,
		lifecycle.EvTaskExited,
		lifecycle.EvTaskDeleted,
		lifecycle.EvContainerDeleted,
		lifecycle.EvCNIDeleted,
		lifecycle.EvNetnsRemoved,
	}
)

func parseNodeRestartPolicy(s string) (lifecycle.RestartPolicy, error) {
	switch s {
	case "never":
		return lifecycle.RestartNever, nil
	case "onFailure":
		return lifecycle.RestartOnFailure, nil
	case "always":
		return lifecycle.RestartAlways, nil
	default:
		return 0, fmt.Errorf("unknown restart policy %q", s)
	}
}

func toLifecycleSpec(w nodeSpecWire) (lifecycle.Spec, error) {
	rp, err := parseNodeRestartPolicy(w.RestartPolicy)
	if err != nil {
		return lifecycle.Spec{}, err
	}
	sp := lifecycle.Spec{
		RestartPolicy:  rp,
		StopGraceMs:    w.StopGraceMs,
		BackoffBaseMs:  w.BackoffBaseMs,
		BackoffMaxMs:   w.BackoffMaxMs,
		BackoffResetMs: w.BackoffResetMs,
		CNICount:       w.CNICount,
	}
	if err := sp.Validate(); err != nil {
		return lifecycle.Spec{}, err
	}
	return sp, nil
}

func parseNodeEventKind(s string) (lifecycle.EventKind, error) {
	for _, k := range nodeEventKinds {
		if k.String() == s {
			return k, nil
		}
	}
	return 0, fmt.Errorf("unknown event kind %q", s)
}

func toNodeActionWire(a lifecycle.Action) nodeActionWire {
	return nodeActionWire{
		Kind:        a.Kind.String(),
		CNIIndex:    a.CNIIndex,
		WaitUntilMs: a.WaitUntilMs,
	}
}

func toNodeStateWire(st lifecycle.State) nodeStateWire {
	return nodeStateWire{
		Phase:          st.Phase.String(),
		DesiredRunning: st.DesiredRunning,
		Resources: nodeResourcesWire{
			NetnsCreated:     st.Resources.NetnsCreated,
			CNIAdded:         st.Resources.CNIAdded,
			ImageReady:       st.Resources.ImageReady,
			ContainerCreated: st.Resources.ContainerCreated,
			TaskCreated:      st.Resources.TaskCreated,
			TaskRunning:      st.Resources.TaskRunning,
			StopSent:         st.Resources.StopSent,
			KillSent:         st.Resources.KillSent,
		},
		Failed:         st.Failed,
		CleanupErrors:  st.CleanupErrors,
		RestartCount:   st.RestartCount,
		BackoffAttempt: st.BackoffAttempt,
		HasExited:      st.HasExited,
		LastExitCode:   st.LastExitCode,
		StartedAtMs:    st.StartedAtMs,
		StopSentAtMs:   st.StopSentAtMs,
		NextStartAtMs:  st.NextStartAtMs,
	}
}

func genNodeSpec(rng *rand.Rand, profile string) nodeSpecWire {
	policy := pick(rng, nodeRestartPolicies)
	stopGrace := int64(rng.IntN(4001))
	if chance(rng, 12) {
		stopGrace = 0
	}
	base := int64(1 + rng.IntN(1000))
	maxMs := base + int64(rng.IntN(12000))
	if chance(rng, 12) {
		maxMs = base
	}
	resetMs := int64(rng.IntN(20001))
	cniCount := rng.IntN(4) // 0..3

	switch profile {
	case "rollback_netns", "rollback_cni0", "rollback_cni1", "rollback_image", "rollback_container", "rollback_task":
		cniCount = 2 + rng.IntN(2) // 2..3
	case "restart_backoff_reset":
		policy = "always"
		base = 100 + int64(rng.IntN(400))
		maxMs = base * 4
		resetMs = 5000 + int64(rng.IntN(5000))
		cniCount = 1 + rng.IntN(3)
	case "restart_on_failure":
		policy = "onFailure"
		base = 100 + int64(rng.IntN(300))
		maxMs = base * 8
		resetMs = 4000
		cniCount = 2
	case "restart_never":
		policy = "never"
		cniCount = 2
	case "sigkill_grace":
		stopGrace = 1000 + int64(rng.IntN(3000))
		cniCount = 2
	case "stop_in_backoff":
		policy = "always"
		base = 500
		maxMs = 4000
		cniCount = 2
	case "stop_in_init", "stop_in_starting_empty", "stop_in_starting_partial":
		cniCount = 2
	}

	return nodeSpecWire{
		RestartPolicy:  policy,
		StopGraceMs:    stopGrace,
		BackoffBaseMs:  base,
		BackoffMaxMs:   maxMs,
		BackoffResetMs: resetMs,
		CNICount:       cniCount,
	}
}

func genNodeBackoffOp(rng *rand.Rand) nodeOp {
	var base, maxMs int64
	var attempt int
	switch rng.IntN(6) {
	case 0:
		base = 0
		maxMs = int64(rng.IntN(1000))
		attempt = rng.IntN(10)
	case 1:
		base = 1 + int64(rng.IntN(1000))
		maxMs = 0
		attempt = rng.IntN(10)
	case 2:
		maxMs = 1 + int64(rng.IntN(500))
		base = maxMs + 1 + int64(rng.IntN(500))
		attempt = rng.IntN(10)
	case 3:
		base = 1 + int64(rng.IntN(500))
		maxMs = base * (1 << uint(rng.IntN(6)))
		attempt = rng.IntN(12)
	case 4:
		base = 1 + int64(rng.IntN(1000))
		maxMs = int64(1)<<50 + rng.Int64N(1<<40)
		attempt = 40 + rng.IntN(35)
	default:
		base = 1 + int64(rng.IntN(2000))
		maxMs = base + int64(rng.IntN(100000))
		attempt = rng.IntN(20)
	}
	return nodeOp{
		Op:      "backoff",
		BaseMs:  base,
		MaxMs:   maxMs,
		Attempt: attempt,
	}
}

func makeEventOp(kind lifecycle.EventKind, nowMs int64, cniIdx int, exitCode uint32, ok bool) nodeOp {
	return nodeOp{
		Op:       "event",
		Kind:     kind.String(),
		NowMs:    nowMs,
		CNIIndex: cniIdx,
		ExitCode: exitCode,
		OK:       ok,
	}
}

func generateNodeCases(seed uint64, n int) []nodeCase {
	rng := rand.New(rand.NewPCG(seed, 0x6e6f64656c696665))
	profiles := []string{
		"rollback_netns",
		"rollback_cni0",
		"rollback_cni1",
		"rollback_image",
		"rollback_container",
		"rollback_task",
		"restart_backoff_reset",
		"restart_on_failure",
		"restart_never",
		"sigkill_grace",
		"stop_in_backoff",
		"stop_in_init",
		"stop_in_starting_empty",
		"stop_in_starting_partial",
		"random",
	}

	cases := make([]nodeCase, n)
	for i := range n {
		var profile string
		if i < len(profiles)*2 {
			profile = profiles[i%len(profiles)]
		} else if chance(rng, 45) {
			profile = pick(rng, profiles[:len(profiles)-1])
		} else {
			profile = "random"
		}

		specWire := genNodeSpec(rng, profile)
		sp, err := toLifecycleSpec(specWire)
		if err != nil {
			panic(fmt.Sprintf("generated invalid node spec: %v", err))
		}

		// Mirror state with lifecycle only to steer generated event sequences into deep states.
		st := lifecycle.InitState()
		now := int64(rng.IntN(100))
		ops := make([]nodeOp, 0, 80)

		emit := func(op nodeOp) {
			ops = append(ops, op)
			if op.Op == "event" {
				k, err := parseNodeEventKind(op.Kind)
				if err != nil {
					panic(err)
				}
				st = lifecycle.ApplyEvent(sp, st, lifecycle.Event{
					Kind:     k,
					NowMs:    op.NowMs,
					CNIIndex: op.CNIIndex,
					ExitCode: op.ExitCode,
					OK:       op.OK,
				})
			}
		}

		emitNext := func(t int64) lifecycle.Action {
			emit(nodeOp{Op: "next_step", NowMs: t})
			return lifecycle.NextStep(sp, st, t)
		}

		drainTeardown := func() {
			for range 25 {
				act := emitNext(now)
				switch act.Kind {
				case lifecycle.ActionNone:
					return
				case lifecycle.ActionTermTask:
					emit(makeEventOp(lifecycle.EvTermSent, now, 0, 0, true))
					now += 10
				case lifecycle.ActionWait:
					if act.WaitUntilMs > now {
						// Probe premature kill_sent before advancing past grace.
						if st.Resources.StopSent && !st.Resources.KillSent && chance(rng, 60) {
							emit(makeEventOp(lifecycle.EvKillSent, act.WaitUntilMs-1, 0, 0, true))
						}
						now = act.WaitUntilMs
					} else {
						now += 20
						emit(makeEventOp(lifecycle.EvTaskExited, now, 0, 137, true))
					}
				case lifecycle.ActionKillTask:
					emit(makeEventOp(lifecycle.EvKillSent, now, 0, 0, true))
					now += 15
					emit(makeEventOp(lifecycle.EvTaskExited, now, 0, 137, true))
				case lifecycle.ActionDeleteTask:
					emit(makeEventOp(lifecycle.EvTaskDeleted, now, 0, 0, chance(rng, 70)))
				case lifecycle.ActionDeleteContainer:
					emit(makeEventOp(lifecycle.EvContainerDeleted, now, 0, 0, chance(rng, 70)))
				case lifecycle.ActionDelCNI:
					emit(makeEventOp(lifecycle.EvCNIDeleted, now, act.CNIIndex, 0, chance(rng, 70)))
				case lifecycle.ActionRemoveNetns:
					emit(makeEventOp(lifecycle.EvNetnsRemoved, now, 0, 0, chance(rng, 70)))
				default:
					return
				}
				now += 10
			}
		}

		driveToRunning := func(failAt lifecycle.ActionKind, failCNI int) bool {
			if st.Phase == lifecycle.PhaseInit {
				emitNext(now)
				emit(makeEventOp(lifecycle.EvStartRequested, now, 0, 0, false))
				now += 10
			}
			for range 20 {
				act := emitNext(now)
				if st.Phase == lifecycle.PhaseRunning {
					return true
				}
				shouldFail := act.Kind == failAt && (act.Kind != lifecycle.ActionAddCNI || act.CNIIndex == failCNI)
				switch act.Kind {
				case lifecycle.ActionCreateNetns:
					if shouldFail {
						emit(makeEventOp(lifecycle.EvNetnsCreateFailed, now, 0, 0, false))
						return false
					}
					emit(makeEventOp(lifecycle.EvNetnsCreated, now, 0, 0, true))
				case lifecycle.ActionAddCNI:
					if shouldFail {
						emit(makeEventOp(lifecycle.EvCNIAddFailed, now, act.CNIIndex, 0, false))
						return false
					}
					emit(makeEventOp(lifecycle.EvCNIAdded, now, act.CNIIndex, 0, true))
				case lifecycle.ActionEnsureImage:
					if shouldFail {
						emit(makeEventOp(lifecycle.EvImageFailed, now, 0, 0, false))
						return false
					}
					emit(makeEventOp(lifecycle.EvImageReady, now, 0, 0, true))
				case lifecycle.ActionCreateContainer:
					if shouldFail {
						emit(makeEventOp(lifecycle.EvContainerCreateFailed, now, 0, 0, false))
						return false
					}
					emit(makeEventOp(lifecycle.EvContainerCreated, now, 0, 0, true))
				case lifecycle.ActionStartTask:
					if shouldFail {
						emit(makeEventOp(lifecycle.EvTaskStartFailed, now, 0, 0, false))
						return false
					}
					emit(makeEventOp(lifecycle.EvTaskStarted, now, 0, 0, true))
					return true
				default:
					return st.Phase == lifecycle.PhaseRunning
				}
				now += 10
			}
			return st.Phase == lifecycle.PhaseRunning
		}

		restartThroughBackoff := func() {
			for range 12 {
				act := emitNext(now)
				switch act.Kind {
				case lifecycle.ActionDeleteTask:
					emit(makeEventOp(lifecycle.EvTaskDeleted, now, 0, 0, chance(rng, 80)))
				case lifecycle.ActionDeleteContainer:
					emit(makeEventOp(lifecycle.EvContainerDeleted, now, 0, 0, chance(rng, 80)))
				case lifecycle.ActionWait:
					if act.WaitUntilMs > now {
						// Probe premature container_created before backoff expires.
						emit(makeEventOp(lifecycle.EvContainerCreated, act.WaitUntilMs-1, 0, 0, true))
						now = act.WaitUntilMs
					} else {
						now += 10
					}
				case lifecycle.ActionCreateContainer:
					emit(makeEventOp(lifecycle.EvContainerCreated, now, 0, 0, true))
				case lifecycle.ActionStartTask:
					emit(makeEventOp(lifecycle.EvTaskStarted, now, 0, 0, true))
					return
				default:
					return
				}
				now += 5
			}
		}

		switch profile {
		case "rollback_netns":
			driveToRunning(lifecycle.ActionCreateNetns, -1)
			drainTeardown()
		case "rollback_cni0":
			driveToRunning(lifecycle.ActionAddCNI, 0)
			drainTeardown()
		case "rollback_cni1":
			driveToRunning(lifecycle.ActionAddCNI, 1)
			drainTeardown()
		case "rollback_image":
			driveToRunning(lifecycle.ActionEnsureImage, -1)
			drainTeardown()
		case "rollback_container":
			driveToRunning(lifecycle.ActionCreateContainer, -1)
			drainTeardown()
		case "rollback_task":
			driveToRunning(lifecycle.ActionStartTask, -1)
			drainTeardown()
		case "stop_in_init":
			emitNext(now)
			emit(makeEventOp(lifecycle.EvStopRequested, now, 0, 0, false))
			emitNext(now)
		case "stop_in_starting_empty":
			emit(makeEventOp(lifecycle.EvStartRequested, now, 0, 0, false))
			emitNext(now)
			emit(makeEventOp(lifecycle.EvStopRequested, now, 0, 0, false))
			emitNext(now)
		case "stop_in_starting_partial":
			emit(makeEventOp(lifecycle.EvStartRequested, now, 0, 0, false))
			emit(makeEventOp(lifecycle.EvNetnsCreated, now, 0, 0, true))
			emit(makeEventOp(lifecycle.EvCNIAdded, now, 0, 0, true))
			if chance(rng, 50) {
				emit(makeEventOp(lifecycle.EvCNIAdded, now, 1, 0, true))
				emit(makeEventOp(lifecycle.EvImageReady, now, 0, 0, true))
			}
			emit(makeEventOp(lifecycle.EvStopRequested, now, 0, 0, false))
			drainTeardown()
		case "sigkill_grace":
			if driveToRunning(lifecycle.ActionNone, -1) {
				emitNext(now)
				now += 100
				emit(makeEventOp(lifecycle.EvStopRequested, now, 0, 0, false))
				emitNext(now)
				emit(makeEventOp(lifecycle.EvTermSent, now, 0, 0, true))
				if sp.StopGraceMs > 0 {
					// Check before grace (clock earlier and just before deadline).
					if now > 0 {
						emitNext(now - 1)
						emit(makeEventOp(lifecycle.EvKillSent, now-1, 0, 0, true))
					}
					emitNext(now + sp.StopGraceMs - 1)
					emit(makeEventOp(lifecycle.EvKillSent, now+sp.StopGraceMs-1, 0, 0, true))
				}
				// Check illegal out-of-order deletes while task is still running.
				emit(makeEventOp(lifecycle.EvTaskDeleted, now, 0, 0, true))
				emit(makeEventOp(lifecycle.EvContainerDeleted, now, 0, 0, true))
				now += sp.StopGraceMs
				emitNext(now)
				emit(makeEventOp(lifecycle.EvKillSent, now, 0, 0, true))
				emitNext(now)
				now += 50
				emit(makeEventOp(lifecycle.EvTaskExited, now, 0, 137, true))
				drainTeardown()
			}
		case "restart_never":
			if driveToRunning(lifecycle.ActionNone, -1) {
				now += 50
				emit(makeEventOp(lifecycle.EvTaskExited, now, 0, 1, true))
				drainTeardown()
			}
		case "restart_on_failure":
			if driveToRunning(lifecycle.ActionNone, -1) {
				if chance(rng, 50) {
					now += 50
					emit(makeEventOp(lifecycle.EvTaskExited, now, 0, 0, true))
					drainTeardown()
				} else {
					now += 50
					emit(makeEventOp(lifecycle.EvTaskExited, now, 0, 2, true))
					restartThroughBackoff()
					now += 50
					emit(makeEventOp(lifecycle.EvTaskExited, now, 0, 0, true))
					drainTeardown()
				}
			}
		case "restart_backoff_reset":
			if driveToRunning(lifecycle.ActionNone, -1) {
				// Four rapid crashes to saturate exponential backoff, then a healthy run to reset.
				for crash := range 4 {
					now += 50
					emit(makeEventOp(lifecycle.EvTaskExited, now, 0, uint32(crash), true))
					restartThroughBackoff()
				}
				now += sp.BackoffResetMs + int64(rng.IntN(1000))
				emit(makeEventOp(lifecycle.EvTaskExited, now, 0, 1, true))
				restartThroughBackoff()
				now += 20
				emit(makeEventOp(lifecycle.EvStopRequested, now, 0, 0, false))
				drainTeardown()
			}
		case "stop_in_backoff":
			if driveToRunning(lifecycle.ActionNone, -1) {
				now += 30
				emit(makeEventOp(lifecycle.EvTaskExited, now, 0, 1, true))
				if chance(rng, 50) {
					emit(makeEventOp(lifecycle.EvTaskDeleted, now, 0, 0, true))
					emit(makeEventOp(lifecycle.EvContainerDeleted, now, 0, 0, true))
				}
				emit(makeEventOp(lifecycle.EvStopRequested, now, 0, 0, false))
				drainTeardown()
			}
		default:
			// Random mix of guided actions, noisy out-of-order events, next_step queries, and backoff ops.
			numOps := 25 + rng.IntN(55)
			for range numOps {
				roll := rng.IntN(100)
				switch {
				case roll < 12:
					emit(genNodeBackoffOp(rng))
				case roll < 32:
					queryNow := now
					if chance(rng, 25) && now > 0 {
						queryNow = rng.Int64N(now)
					} else if chance(rng, 40) {
						queryNow = now + int64(rng.IntN(6000))
					}
					emitNext(queryNow)
				case roll < 70:
					// Guided step matching NextStep so random cases traverse all phases deeply.
					act := lifecycle.NextStep(sp, st, now)
					switch act.Kind {
					case lifecycle.ActionNone:
						if st.Phase == lifecycle.PhaseInit {
							if chance(rng, 85) {
								emit(makeEventOp(lifecycle.EvStartRequested, now, 0, 0, false))
							} else {
								emit(makeEventOp(lifecycle.EvStopRequested, now, 0, 0, false))
							}
						} else {
							emit(makeEventOp(pick(rng, nodeEventKinds), now, rng.IntN(max(1, sp.CNICount+1)), uint32(rng.IntN(4)), chance(rng, 50)))
						}
					case lifecycle.ActionCreateNetns:
						if chance(rng, 80) {
							emit(makeEventOp(lifecycle.EvNetnsCreated, now, 0, 0, true))
						} else {
							emit(makeEventOp(lifecycle.EvNetnsCreateFailed, now, 0, 0, false))
						}
					case lifecycle.ActionAddCNI:
						if chance(rng, 80) {
							emit(makeEventOp(lifecycle.EvCNIAdded, now, act.CNIIndex, 0, true))
						} else {
							emit(makeEventOp(lifecycle.EvCNIAddFailed, now, act.CNIIndex, 0, false))
						}
					case lifecycle.ActionEnsureImage:
						if chance(rng, 80) {
							emit(makeEventOp(lifecycle.EvImageReady, now, 0, 0, true))
						} else {
							emit(makeEventOp(lifecycle.EvImageFailed, now, 0, 0, false))
						}
					case lifecycle.ActionCreateContainer:
						if chance(rng, 80) {
							emit(makeEventOp(lifecycle.EvContainerCreated, now, 0, 0, true))
						} else {
							emit(makeEventOp(lifecycle.EvContainerCreateFailed, now, 0, 0, false))
						}
					case lifecycle.ActionStartTask:
						if chance(rng, 80) {
							emit(makeEventOp(lifecycle.EvTaskStarted, now, 0, 0, true))
						} else {
							emit(makeEventOp(lifecycle.EvTaskStartFailed, now, 0, 0, false))
						}
					case lifecycle.ActionTermTask:
						emit(makeEventOp(lifecycle.EvTermSent, now, 0, 0, true))
					case lifecycle.ActionKillTask:
						emit(makeEventOp(lifecycle.EvKillSent, now, 0, 0, true))
					case lifecycle.ActionDeleteTask:
						emit(makeEventOp(lifecycle.EvTaskDeleted, now, 0, 0, chance(rng, 75)))
					case lifecycle.ActionDeleteContainer:
						emit(makeEventOp(lifecycle.EvContainerDeleted, now, 0, 0, chance(rng, 75)))
					case lifecycle.ActionDelCNI:
						emit(makeEventOp(lifecycle.EvCNIDeleted, now, act.CNIIndex, 0, chance(rng, 75)))
					case lifecycle.ActionRemoveNetns:
						emit(makeEventOp(lifecycle.EvNetnsRemoved, now, 0, 0, chance(rng, 75)))
					case lifecycle.ActionWait:
						if st.Phase == lifecycle.PhaseRunning {
							if chance(rng, 55) {
								if chance(rng, 35) {
									now += sp.BackoffResetMs + int64(rng.IntN(200))
								} else {
									now += int64(rng.IntN(500))
								}
								emit(makeEventOp(lifecycle.EvTaskExited, now, 0, uint32(rng.IntN(3)), true))
							} else {
								emit(makeEventOp(lifecycle.EvStopRequested, now, 0, 0, false))
							}
						} else if act.WaitUntilMs > now {
							now = act.WaitUntilMs
							emitNext(now)
						} else {
							now += 50
							emit(makeEventOp(lifecycle.EvTaskExited, now, 0, 137, true))
						}
					}
					now += int64(rng.IntN(200))
				default:
					// Arbitrary adversarial/stale/out-of-order event.
					var evNow int64
					if chance(rng, 25) && now > 0 {
						evNow = rng.Int64N(now)
					} else {
						evNow = now + int64(rng.IntN(500))
					}
					emit(makeEventOp(
						pick(rng, nodeEventKinds),
						evNow,
						rng.IntN(max(1, sp.CNICount+2)),
						uint32(rng.IntN(4)),
						chance(rng, 50),
					))
				}
			}
		}

		// Probe post-stopped absorption and standalone backoff ops.
		if st.Phase == lifecycle.PhaseStopped {
			emit(makeEventOp(lifecycle.EvStartRequested, now+10, 0, 0, false))
			emitNext(now + 10)
		}
		if chance(rng, 30) {
			emit(genNodeBackoffOp(rng))
		}

		cases[i] = nodeCase{
			ID:   i,
			Spec: specWire,
			Ops:  ops,
		}
	}
	return cases
}

func encodeNodeCases(t *testing.T, cases []nodeCase) [][]byte {
	t.Helper()
	out := make([][]byte, len(cases))
	for i, c := range cases {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal node case %d: %v", c.ID, err)
		}
		out[i] = b
	}
	return out
}

type nodeHooks struct {
	cniAddFailNoCountAttempt          bool
	delCNIForwardOrder                bool
	deleteContainerWhileTaskCreated   bool
	deleteTaskWhileRunning            bool
	removeNetnsBeforeCNIClean         bool
	killBeforeGraceElapsed            bool
	restartNeverRestarts              bool
	restartOnFailureRestartsOnZero    bool
	restartAlwaysStopsOnZero          bool
	backoffNoCap                      bool
	backoffNoResetAfterHealthy        bool
	backoffNoIncrementOnRestart       bool
	stopInBackoffJumpsToStopped       bool
	containerCreateBeforeBackoffWait  bool
	stoppedAcceptsStart               bool
	ignoreCleanupErrorCount           bool
	containerDeleteKeepsImageTeardown bool
	stopInStartingEmptyEntersStopping bool
}

func runNodeCase(tc nodeCase, hooks nodeHooks) (nodeResult, error) {
	sp, err := toLifecycleSpec(tc.Spec)
	if err != nil {
		return nodeResult{}, err
	}
	st := lifecycle.InitState()
	steps := make([]nodeStep, len(tc.Ops))

	for i, op := range tc.Ops {
		switch op.Op {
		case "event":
			kind, err := parseNodeEventKind(op.Kind)
			if err != nil {
				return nodeResult{}, err
			}
			ev := lifecycle.Event{
				Kind:     kind,
				NowMs:    op.NowMs,
				CNIIndex: op.CNIIndex,
				ExitCode: op.ExitCode,
				OK:       op.OK,
			}
			st = applyNodeEventWithHooks(sp, st, ev, hooks)
			steps[i] = nodeStep{
				Action:     toNodeActionWire(lifecycle.Action{Kind: lifecycle.ActionNone}),
				State:      toNodeStateWire(st),
				WellFormed: lifecycle.WellFormed(sp, st),
			}
		case "next_step":
			act := nextNodeStepWithHooks(sp, st, op.NowMs, hooks)
			steps[i] = nodeStep{
				Action:     toNodeActionWire(act),
				State:      toNodeStateWire(st),
				WellFormed: lifecycle.WellFormed(sp, st),
			}
		case "backoff":
			d := backoffWithHooks(op.BaseMs, op.MaxMs, op.Attempt, hooks)
			steps[i] = nodeStep{
				Action:     toNodeActionWire(lifecycle.Action{Kind: lifecycle.ActionNone}),
				State:      toNodeStateWire(st),
				WellFormed: lifecycle.WellFormed(sp, st),
				BackoffMs:  d,
			}
		default:
			return nodeResult{}, fmt.Errorf("unknown op %q", op.Op)
		}
	}

	return nodeResult{ID: tc.ID, Steps: steps}, nil
}

func backoffWithHooks(baseMs, maxMs int64, attempt int, hooks nodeHooks) int64 {
	if hooks.backoffNoCap && baseMs > 0 && maxMs > 0 {
		d := baseMs
		for range min(max(0, attempt), 20) {
			if d > math.MaxInt64/2 {
				break
			}
			d *= 2
		}
		return d
	}
	return lifecycle.BackoffMs(baseMs, maxMs, attempt)
}

func nextNodeStepWithHooks(sp lifecycle.Spec, st lifecycle.State, nowMs int64, hooks nodeHooks) lifecycle.Action {
	if hooks.delCNIForwardOrder && (st.Phase == lifecycle.PhaseStopping || st.Phase == lifecycle.PhaseRollingBack) &&
		!st.Resources.TaskRunning && !st.Resources.TaskCreated && !st.Resources.ContainerCreated && st.Resources.CNIAdded > 0 {
		return lifecycle.Action{Kind: lifecycle.ActionDelCNI, CNIIndex: 0}
	}
	if hooks.killBeforeGraceElapsed && (st.Phase == lifecycle.PhaseStopping || st.Phase == lifecycle.PhaseRollingBack || st.Phase == lifecycle.PhaseBackoff) &&
		st.Resources.TaskRunning && st.Resources.StopSent && !st.Resources.KillSent {
		return lifecycle.Action{Kind: lifecycle.ActionKillTask}
	}
	return lifecycle.NextStep(sp, st, nowMs)
}

func applyNodeEventWithHooks(sp lifecycle.Spec, st lifecycle.State, ev lifecycle.Event, hooks nodeHooks) lifecycle.State {
	prev := st
	if hooks.stoppedAcceptsStart && st.Phase == lifecycle.PhaseStopped && ev.Kind == lifecycle.EvStartRequested {
		st.DesiredRunning = true
		st.Phase = lifecycle.PhaseStarting
		return st
	}
	if hooks.stopInStartingEmptyEntersStopping && st.Phase == lifecycle.PhaseStarting && ev.Kind == lifecycle.EvStopRequested && st.Resources.Empty() {
		st.DesiredRunning = false
		st.Phase = lifecycle.PhaseStopping
		return st
	}
	if hooks.stopInBackoffJumpsToStopped && st.Phase == lifecycle.PhaseBackoff && ev.Kind == lifecycle.EvStopRequested {
		st.DesiredRunning = false
		st.Phase = lifecycle.PhaseStopped
		return st
	}
	if hooks.cniAddFailNoCountAttempt && ev.Kind == lifecycle.EvCNIAddFailed {
		next := lifecycle.ApplyEvent(sp, st, ev)
		if next.Phase == lifecycle.PhaseRollingBack && st.Phase == lifecycle.PhaseStarting {
			next.Resources.CNIAdded = st.Resources.CNIAdded
		}
		return next
	}
	if hooks.deleteTaskWhileRunning && ev.Kind == lifecycle.EvTaskDeleted && st.Resources.TaskRunning && st.Resources.TaskCreated {
		st.Resources.TaskCreated = false
		st.Resources.StopSent = false
		st.Resources.KillSent = false
		return st
	}
	if hooks.deleteContainerWhileTaskCreated && ev.Kind == lifecycle.EvContainerDeleted && st.Resources.TaskCreated && st.Resources.ContainerCreated {
		st.Resources.ContainerCreated = false
		return st
	}
	if hooks.removeNetnsBeforeCNIClean && ev.Kind == lifecycle.EvNetnsRemoved && st.Resources.NetnsCreated && st.Resources.CNIAdded > 0 {
		st.Resources.NetnsCreated = false
		st.Phase = lifecycle.PhaseStopped
		return st
	}
	if hooks.killBeforeGraceElapsed && ev.Kind == lifecycle.EvKillSent &&
		(st.Phase == lifecycle.PhaseStopping || st.Phase == lifecycle.PhaseRollingBack) &&
		st.Resources.TaskRunning && st.Resources.StopSent && !st.Resources.KillSent {
		st.Resources.KillSent = true
		return st
	}
	if hooks.containerCreateBeforeBackoffWait && ev.Kind == lifecycle.EvContainerCreated &&
		st.Phase == lifecycle.PhaseBackoff && st.Resources.NetnsCreated && st.Resources.CNIAdded == sp.CNICount &&
		st.Resources.ImageReady && !st.Resources.ContainerCreated && !st.Resources.TaskCreated && !st.Resources.TaskRunning {
		st.Resources.ContainerCreated = true
		st.Phase = lifecycle.PhaseStarting
		return st
	}
	if ev.Kind == lifecycle.EvTaskExited && st.Phase == lifecycle.PhaseRunning && st.Resources.TaskRunning {
		if hooks.restartNeverRestarts && sp.RestartPolicy == lifecycle.RestartNever {
			alt := sp
			alt.RestartPolicy = lifecycle.RestartAlways
			return lifecycle.ApplyEvent(alt, st, ev)
		}
		if hooks.restartOnFailureRestartsOnZero && sp.RestartPolicy == lifecycle.RestartOnFailure && ev.ExitCode == 0 {
			alt := sp
			alt.RestartPolicy = lifecycle.RestartAlways
			return lifecycle.ApplyEvent(alt, st, ev)
		}
		if hooks.restartAlwaysStopsOnZero && sp.RestartPolicy == lifecycle.RestartAlways && ev.ExitCode == 0 {
			alt := sp
			alt.RestartPolicy = lifecycle.RestartOnFailure
			return lifecycle.ApplyEvent(alt, st, ev)
		}
		if hooks.backoffNoResetAfterHealthy {
			alt := sp
			alt.BackoffResetMs = math.MaxInt64
			return lifecycle.ApplyEvent(alt, st, ev)
		}
	}

	next := lifecycle.ApplyEvent(sp, st, ev)
	if hooks.backoffNoIncrementOnRestart && ev.Kind == lifecycle.EvTaskStarted && next.Phase == lifecycle.PhaseRunning && prev.Phase == lifecycle.PhaseStarting {
		next.BackoffAttempt = prev.BackoffAttempt
	}
	if hooks.ignoreCleanupErrorCount && next.CleanupErrors > prev.CleanupErrors {
		next.CleanupErrors = prev.CleanupErrors
	}
	if hooks.containerDeleteKeepsImageTeardown && ev.Kind == lifecycle.EvContainerDeleted &&
		(prev.Phase == lifecycle.PhaseStopping || prev.Phase == lifecycle.PhaseRollingBack) &&
		prev.Resources.ContainerCreated && !next.Resources.ContainerCreated {
		next.Resources.ImageReady = true
	}
	return next
}

func decodeUint32(raw json.RawMessage) (uint32, error) {
	var n uint32
	if err := decodeStrictJSON(raw, &n); err != nil {
		return 0, err
	}
	return n, nil
}

func decodeNodeAction(raw json.RawMessage) (nodeActionWire, error) {
	obj, err := decodeObject(raw, "kind", "cni_index", "wait_until_ms")
	if err != nil {
		return nodeActionWire{}, err
	}
	kind, err := decodeString(obj["kind"])
	if err != nil {
		return nodeActionWire{}, fmt.Errorf("kind: %w", err)
	}
	if !slices.Contains(nodeActionKinds, kind) {
		return nodeActionWire{}, fmt.Errorf("unknown action kind %q", kind)
	}
	cniIdx, err := decodeNonNegInt(obj["cni_index"])
	if err != nil {
		return nodeActionWire{}, fmt.Errorf("cni_index: %w", err)
	}
	waitUntil, err := decodeNonNegInt64(obj["wait_until_ms"])
	if err != nil {
		return nodeActionWire{}, fmt.Errorf("wait_until_ms: %w", err)
	}
	return nodeActionWire{Kind: kind, CNIIndex: cniIdx, WaitUntilMs: waitUntil}, nil
}

func decodeNodeResources(raw json.RawMessage) (nodeResourcesWire, error) {
	obj, err := decodeObject(raw,
		"netns_created", "cni_added", "image_ready", "container_created",
		"task_created", "task_running", "stop_sent", "kill_sent",
	)
	if err != nil {
		return nodeResourcesWire{}, err
	}
	netns, err := decodeBool(obj["netns_created"])
	if err != nil {
		return nodeResourcesWire{}, fmt.Errorf("netns_created: %w", err)
	}
	cniAdded, err := decodeNonNegInt(obj["cni_added"])
	if err != nil {
		return nodeResourcesWire{}, fmt.Errorf("cni_added: %w", err)
	}
	imageReady, err := decodeBool(obj["image_ready"])
	if err != nil {
		return nodeResourcesWire{}, fmt.Errorf("image_ready: %w", err)
	}
	containerCreated, err := decodeBool(obj["container_created"])
	if err != nil {
		return nodeResourcesWire{}, fmt.Errorf("container_created: %w", err)
	}
	taskCreated, err := decodeBool(obj["task_created"])
	if err != nil {
		return nodeResourcesWire{}, fmt.Errorf("task_created: %w", err)
	}
	taskRunning, err := decodeBool(obj["task_running"])
	if err != nil {
		return nodeResourcesWire{}, fmt.Errorf("task_running: %w", err)
	}
	stopSent, err := decodeBool(obj["stop_sent"])
	if err != nil {
		return nodeResourcesWire{}, fmt.Errorf("stop_sent: %w", err)
	}
	killSent, err := decodeBool(obj["kill_sent"])
	if err != nil {
		return nodeResourcesWire{}, fmt.Errorf("kill_sent: %w", err)
	}
	return nodeResourcesWire{
		NetnsCreated:     netns,
		CNIAdded:         cniAdded,
		ImageReady:       imageReady,
		ContainerCreated: containerCreated,
		TaskCreated:      taskCreated,
		TaskRunning:      taskRunning,
		StopSent:         stopSent,
		KillSent:         killSent,
	}, nil
}

func decodeNodeState(raw json.RawMessage) (nodeStateWire, error) {
	obj, err := decodeObject(raw,
		"phase", "desired_running", "resources", "failed", "cleanup_errors",
		"restart_count", "backoff_attempt", "has_exited", "last_exit_code",
		"started_at_ms", "stop_sent_at_ms", "next_start_at_ms",
	)
	if err != nil {
		return nodeStateWire{}, err
	}
	phase, err := decodeString(obj["phase"])
	if err != nil {
		return nodeStateWire{}, fmt.Errorf("phase: %w", err)
	}
	if !slices.Contains(nodePhases, phase) {
		return nodeStateWire{}, fmt.Errorf("unknown phase %q", phase)
	}
	desired, err := decodeBool(obj["desired_running"])
	if err != nil {
		return nodeStateWire{}, fmt.Errorf("desired_running: %w", err)
	}
	res, err := decodeNodeResources(obj["resources"])
	if err != nil {
		return nodeStateWire{}, fmt.Errorf("resources: %w", err)
	}
	failed, err := decodeBool(obj["failed"])
	if err != nil {
		return nodeStateWire{}, fmt.Errorf("failed: %w", err)
	}
	cleanupErrors, err := decodeNonNegInt(obj["cleanup_errors"])
	if err != nil {
		return nodeStateWire{}, fmt.Errorf("cleanup_errors: %w", err)
	}
	restartCount, err := decodeNonNegInt(obj["restart_count"])
	if err != nil {
		return nodeStateWire{}, fmt.Errorf("restart_count: %w", err)
	}
	backoffAttempt, err := decodeNonNegInt(obj["backoff_attempt"])
	if err != nil {
		return nodeStateWire{}, fmt.Errorf("backoff_attempt: %w", err)
	}
	hasExited, err := decodeBool(obj["has_exited"])
	if err != nil {
		return nodeStateWire{}, fmt.Errorf("has_exited: %w", err)
	}
	lastExitCode, err := decodeUint32(obj["last_exit_code"])
	if err != nil {
		return nodeStateWire{}, fmt.Errorf("last_exit_code: %w", err)
	}
	startedAt, err := decodeNonNegInt64(obj["started_at_ms"])
	if err != nil {
		return nodeStateWire{}, fmt.Errorf("started_at_ms: %w", err)
	}
	stopSentAt, err := decodeNonNegInt64(obj["stop_sent_at_ms"])
	if err != nil {
		return nodeStateWire{}, fmt.Errorf("stop_sent_at_ms: %w", err)
	}
	nextStartAt, err := decodeNonNegInt64(obj["next_start_at_ms"])
	if err != nil {
		return nodeStateWire{}, fmt.Errorf("next_start_at_ms: %w", err)
	}
	return nodeStateWire{
		Phase:          phase,
		DesiredRunning: desired,
		Resources:      res,
		Failed:         failed,
		CleanupErrors:  cleanupErrors,
		RestartCount:   restartCount,
		BackoffAttempt: backoffAttempt,
		HasExited:      hasExited,
		LastExitCode:   lastExitCode,
		StartedAtMs:    startedAt,
		StopSentAtMs:   stopSentAt,
		NextStartAtMs:  nextStartAt,
	}, nil
}

func decodeNodeStep(raw json.RawMessage) (nodeStep, error) {
	obj, err := decodeObject(raw, "action", "state", "well_formed", "backoff_ms")
	if err != nil {
		return nodeStep{}, err
	}
	act, err := decodeNodeAction(obj["action"])
	if err != nil {
		return nodeStep{}, fmt.Errorf("action: %w", err)
	}
	st, err := decodeNodeState(obj["state"])
	if err != nil {
		return nodeStep{}, fmt.Errorf("state: %w", err)
	}
	wf, err := decodeBool(obj["well_formed"])
	if err != nil {
		return nodeStep{}, fmt.Errorf("well_formed: %w", err)
	}
	backoff, err := decodeNonNegInt64(obj["backoff_ms"])
	if err != nil {
		return nodeStep{}, fmt.Errorf("backoff_ms: %w", err)
	}
	return nodeStep{
		Action:     act,
		State:      st,
		WellFormed: wf,
		BackoffMs:  backoff,
	}, nil
}

func decodeNodeResult(line []byte) (nodeResult, error) {
	trimmed := bytes.TrimSpace(line)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rawTop json.RawMessage
	if err := dec.Decode(&rawTop); err != nil {
		return nodeResult{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nodeResult{}, fmt.Errorf("unexpected trailing data in result line: %v", err)
	}
	top, err := decodeObject(rawTop, "id", "steps")
	if err != nil {
		return nodeResult{}, err
	}
	id, err := decodeNonNegInt(top["id"])
	if err != nil {
		return nodeResult{}, fmt.Errorf("id: %w", err)
	}
	var rawSteps []json.RawMessage
	if err := decodeStrictJSON(top["steps"], &rawSteps); err != nil {
		return nodeResult{}, fmt.Errorf("steps: %w", err)
	}
	steps := make([]nodeStep, len(rawSteps))
	for i, rs := range rawSteps {
		s, err := decodeNodeStep(rs)
		if err != nil {
			return nodeResult{}, fmt.Errorf("steps[%d]: %w", i, err)
		}
		steps[i] = s
	}
	return nodeResult{ID: id, Steps: steps}, nil
}

func compareNodeResult(want, got nodeResult) string {
	if want.ID != got.ID {
		return fmt.Sprintf("id mismatch: lean=%d go=%d", want.ID, got.ID)
	}
	if len(want.Steps) != len(got.Steps) {
		return fmt.Sprintf("steps len mismatch: lean=%d go=%d", len(want.Steps), len(got.Steps))
	}
	for i := range want.Steps {
		w, g := want.Steps[i], got.Steps[i]
		switch {
		case w.Action.Kind != g.Action.Kind:
			return fmt.Sprintf("step %d action.kind: lean=%q go=%q", i, w.Action.Kind, g.Action.Kind)
		case w.Action.CNIIndex != g.Action.CNIIndex:
			return fmt.Sprintf("step %d action.cni_index: lean=%d go=%d", i, w.Action.CNIIndex, g.Action.CNIIndex)
		case w.Action.WaitUntilMs != g.Action.WaitUntilMs:
			return fmt.Sprintf("step %d action.wait_until_ms: lean=%d go=%d", i, w.Action.WaitUntilMs, g.Action.WaitUntilMs)
		case w.WellFormed != g.WellFormed:
			return fmt.Sprintf("step %d well_formed: lean=%v go=%v", i, w.WellFormed, g.WellFormed)
		case w.BackoffMs != g.BackoffMs:
			return fmt.Sprintf("step %d backoff_ms: lean=%d go=%d", i, w.BackoffMs, g.BackoffMs)
		case w.State.Phase != g.State.Phase:
			return fmt.Sprintf("step %d state.phase: lean=%q go=%q", i, w.State.Phase, g.State.Phase)
		case w.State.DesiredRunning != g.State.DesiredRunning:
			return fmt.Sprintf("step %d state.desired_running: lean=%v go=%v", i, w.State.DesiredRunning, g.State.DesiredRunning)
		case w.State.Resources.NetnsCreated != g.State.Resources.NetnsCreated:
			return fmt.Sprintf("step %d state.resources.netns_created: lean=%v go=%v", i, w.State.Resources.NetnsCreated, g.State.Resources.NetnsCreated)
		case w.State.Resources.CNIAdded != g.State.Resources.CNIAdded:
			return fmt.Sprintf("step %d state.resources.cni_added: lean=%d go=%d", i, w.State.Resources.CNIAdded, g.State.Resources.CNIAdded)
		case w.State.Resources.ImageReady != g.State.Resources.ImageReady:
			return fmt.Sprintf("step %d state.resources.image_ready: lean=%v go=%v", i, w.State.Resources.ImageReady, g.State.Resources.ImageReady)
		case w.State.Resources.ContainerCreated != g.State.Resources.ContainerCreated:
			return fmt.Sprintf("step %d state.resources.container_created: lean=%v go=%v", i, w.State.Resources.ContainerCreated, g.State.Resources.ContainerCreated)
		case w.State.Resources.TaskCreated != g.State.Resources.TaskCreated:
			return fmt.Sprintf("step %d state.resources.task_created: lean=%v go=%v", i, w.State.Resources.TaskCreated, g.State.Resources.TaskCreated)
		case w.State.Resources.TaskRunning != g.State.Resources.TaskRunning:
			return fmt.Sprintf("step %d state.resources.task_running: lean=%v go=%v", i, w.State.Resources.TaskRunning, g.State.Resources.TaskRunning)
		case w.State.Resources.StopSent != g.State.Resources.StopSent:
			return fmt.Sprintf("step %d state.resources.stop_sent: lean=%v go=%v", i, w.State.Resources.StopSent, g.State.Resources.StopSent)
		case w.State.Resources.KillSent != g.State.Resources.KillSent:
			return fmt.Sprintf("step %d state.resources.kill_sent: lean=%v go=%v", i, w.State.Resources.KillSent, g.State.Resources.KillSent)
		case w.State.Failed != g.State.Failed:
			return fmt.Sprintf("step %d state.failed: lean=%v go=%v", i, w.State.Failed, g.State.Failed)
		case w.State.CleanupErrors != g.State.CleanupErrors:
			return fmt.Sprintf("step %d state.cleanup_errors: lean=%d go=%d", i, w.State.CleanupErrors, g.State.CleanupErrors)
		case w.State.RestartCount != g.State.RestartCount:
			return fmt.Sprintf("step %d state.restart_count: lean=%d go=%d", i, w.State.RestartCount, g.State.RestartCount)
		case w.State.BackoffAttempt != g.State.BackoffAttempt:
			return fmt.Sprintf("step %d state.backoff_attempt: lean=%d go=%d", i, w.State.BackoffAttempt, g.State.BackoffAttempt)
		case w.State.HasExited != g.State.HasExited:
			return fmt.Sprintf("step %d state.has_exited: lean=%v go=%v", i, w.State.HasExited, g.State.HasExited)
		case w.State.LastExitCode != g.State.LastExitCode:
			return fmt.Sprintf("step %d state.last_exit_code: lean=%d go=%d", i, w.State.LastExitCode, g.State.LastExitCode)
		case w.State.StartedAtMs != g.State.StartedAtMs:
			return fmt.Sprintf("step %d state.started_at_ms: lean=%d go=%d", i, w.State.StartedAtMs, g.State.StartedAtMs)
		case w.State.StopSentAtMs != g.State.StopSentAtMs:
			return fmt.Sprintf("step %d state.stop_sent_at_ms: lean=%d go=%d", i, w.State.StopSentAtMs, g.State.StopSentAtMs)
		case w.State.NextStartAtMs != g.State.NextStartAtMs:
			return fmt.Sprintf("step %d state.next_start_at_ms: lean=%d go=%d", i, w.State.NextStartAtMs, g.State.NextStartAtMs)
		}
	}
	return ""
}

func cloneNodeResult(r nodeResult) nodeResult {
	return nodeResult{
		ID:    r.ID,
		Steps: slices.Clone(r.Steps),
	}
}

func leanNodeResults(t *testing.T, cases []nodeCase) ([][]byte, [][]byte, []nodeResult) {
	t.Helper()
	encoded := encodeNodeCases(t, cases)
	lines, err := RunModel("node", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(lines), len(cases))
	}
	results := make([]nodeResult, len(lines))
	for i, line := range lines {
		res, err := decodeNodeResult(line)
		if err != nil {
			t.Fatalf("id=%d decode error: %v\ncase: %s\nlean: %s", cases[i].ID, err, encoded[i], line)
		}
		results[i] = res
	}
	return encoded, lines, results
}

func TestNodeMatchesLeanModel(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateNodeCases(cfg.Seed, cfg.Cases)
	encoded, leanLines, leanResults := leanNodeResults(t, cases)

	repro := fmt.Sprintf("NAYUTAR_DRT_SEED=%d NAYUTAR_DRT_CASES=%d go test -tags drt -run '^TestNodeMatchesLeanModel$' ./core/drt/...", cfg.Seed, cfg.Cases)
	totalSteps := 0
	for i, tc := range cases {
		goRes, err := runNodeCase(tc, nodeHooks{})
		if err != nil {
			t.Fatalf("seed=%d id=%d go error: %v\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, err, encoded[i], leanLines[i], repro)
		}
		if diff := compareNodeResult(leanResults[i], goRes); diff != "" {
			t.Fatalf("seed=%d id=%d mismatch (%s)\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, diff, encoded[i], leanLines[i], repro)
		}
		for j, s := range goRes.Steps {
			if !s.WellFormed {
				t.Fatalf("seed=%d id=%d step=%d violated WellFormed\ncase: %s\nreproduce: %s",
					cfg.Seed, tc.ID, j, encoded[i], repro)
			}
		}
		totalSteps += len(goRes.Steps)
	}
	t.Logf("compared %d cases (%d steps, seed=%d)", len(cases), totalSteps, cfg.Seed)
}

func TestNodeCasesReachEveryOutcome(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateNodeCases(cfg.Seed, cfg.Cases)
	_, _, results := leanNodeResults(t, cases)

	seen := map[string]int{}
	for i, tc := range cases {
		res := results[i]
		seen["policy/"+tc.Spec.RestartPolicy]++
		prev := toNodeStateWire(lifecycle.InitState())
		for j, op := range tc.Ops {
			s := res.Steps[j]
			seen["phase/"+s.State.Phase]++
			if s.State.Failed {
				seen["state/failed"]++
			}
			if s.State.CleanupErrors > 0 {
				seen["state/cleanup_errors>0"]++
			}
			if s.State.RestartCount > 1 {
				seen["state/restart_count>1"]++
			}
			if s.State.BackoffAttempt > 1 {
				seen["state/backoff_attempt>1"]++
			}
			if s.State.Phase == "stopped" && s.State.Resources == (nodeResourcesWire{}) {
				seen["state/stopped_clean"]++
			}
			switch op.Op {
			case "next_step":
				seen["action/"+s.Action.Kind]++
				if s.Action.Kind == "wait" {
					if s.Action.WaitUntilMs == 0 {
						seen["action/wait_indefinite"]++
					} else {
						seen["action/wait_deadline"]++
					}
				}
				if s.Action.Kind == "del_cni" && s.Action.CNIIndex > 0 {
					seen["action/del_cni_nonzero"]++
				}
			case "event":
				seen["event/"+op.Kind]++
				if op.Kind == "task_exited" && prev.BackoffAttempt > 0 && s.State.BackoffAttempt == 0 {
					seen["backoff/reset_after_healthy"]++
				}
				if op.Kind == "kill_sent" && prev.Resources.StopSent && !prev.Resources.KillSent && !s.State.Resources.KillSent {
					seen["kill/rejected_before_grace"]++
				}
				if op.Kind == "kill_sent" && !prev.Resources.KillSent && s.State.Resources.KillSent {
					seen["kill/accepted_after_grace"]++
				}
				if op.Kind == "stop_requested" && prev.Phase == "backoff" && s.State.Phase == "stopping" {
					seen["stop/during_backoff"]++
				}
				if op.Kind == "stop_requested" && prev.Phase == "starting" && s.State.Phase == "stopped" {
					seen["stop/starting_empty"]++
				}
				if op.Kind == "start_requested" && prev.Phase == "stopped" && s.State.Phase == "stopped" {
					seen["stopped/ignored_start"]++
				}
			case "backoff":
				if op.BaseMs == 0 {
					seen["backoff/zero_base"]++
				}
				if op.MaxMs == 0 {
					seen["backoff/zero_max"]++
				}
				if op.BaseMs > op.MaxMs && op.MaxMs > 0 {
					seen["backoff/max_lt_base"]++
				}
				if op.BaseMs > 0 && op.MaxMs > 0 && s.BackoffMs == op.MaxMs {
					seen["backoff/saturated"]++
				}
			}
			prev = s.State
		}
	}

	required := []string{
		"policy/never",
		"policy/onFailure",
		"policy/always",
		"state/failed",
		"state/cleanup_errors>0",
		"state/restart_count>1",
		"state/backoff_attempt>1",
		"state/stopped_clean",
		"action/wait_indefinite",
		"action/wait_deadline",
		"action/del_cni_nonzero",
		"backoff/reset_after_healthy",
		"kill/rejected_before_grace",
		"kill/accepted_after_grace",
		"stop/during_backoff",
		"stop/starting_empty",
		"stopped/ignored_start",
		"backoff/zero_base",
		"backoff/zero_max",
		"backoff/max_lt_base",
		"backoff/saturated",
	}
	for _, p := range nodePhases {
		required = append(required, "phase/"+p)
	}
	for _, a := range nodeActionKinds {
		required = append(required, "action/"+a)
	}
	for _, k := range nodeEventKinds {
		required = append(required, "event/"+k.String())
	}

	for _, item := range required {
		if seen[item] == 0 {
			t.Errorf("no case reached coverage item %q (seed=%d, cases=%d)", item, cfg.Seed, cfg.Cases)
		}
	}
}

func TestNodeComparisonDetectsEveryFieldMutation(t *testing.T) {
	cases := generateNodeCases(1, 120)
	_, leanLines, results := leanNodeResults(t, cases)

	applied := map[string]int{}
	assertDetects := func(kind string, orig nodeResult, mutate func(r *nodeResult)) {
		t.Helper()
		mut := cloneNodeResult(orig)
		mutate(&mut)
		applied[kind]++
		if compareNodeResult(orig, mut) == "" {
			t.Fatalf("mutation %q on case %d was not detected", kind, orig.ID)
		}
	}

	for _, orig := range results {
		assertDetects("id+1", orig, func(r *nodeResult) { r.ID++ })
		if n := len(orig.Steps); n > 0 {
			assertDetects("drop-last-step", orig, func(r *nodeResult) { r.Steps = r.Steps[:n-1] })
			assertDetects("append-dup-step", orig, func(r *nodeResult) { r.Steps = append(r.Steps, r.Steps[n-1]) })
		}
		for j := range orig.Steps {
			assertDetects("action-kind", orig, func(r *nodeResult) {
				if r.Steps[j].Action.Kind == "none" {
					r.Steps[j].Action.Kind = "wait"
				} else {
					r.Steps[j].Action.Kind = "none"
				}
			})
			assertDetects("action-cni_index+1", orig, func(r *nodeResult) { r.Steps[j].Action.CNIIndex++ })
			assertDetects("action-wait_until_ms+1", orig, func(r *nodeResult) { r.Steps[j].Action.WaitUntilMs++ })
			assertDetects("well_formed", orig, func(r *nodeResult) { r.Steps[j].WellFormed = !r.Steps[j].WellFormed })
			assertDetects("backoff_ms+1", orig, func(r *nodeResult) { r.Steps[j].BackoffMs++ })
			assertDetects("state-phase", orig, func(r *nodeResult) {
				if r.Steps[j].State.Phase == "stopped" {
					r.Steps[j].State.Phase = "init"
				} else {
					r.Steps[j].State.Phase = "stopped"
				}
			})
			assertDetects("state-desired_running", orig, func(r *nodeResult) { r.Steps[j].State.DesiredRunning = !r.Steps[j].State.DesiredRunning })
			assertDetects("res-netns_created", orig, func(r *nodeResult) {
				r.Steps[j].State.Resources.NetnsCreated = !r.Steps[j].State.Resources.NetnsCreated
			})
			assertDetects("res-cni_added+1", orig, func(r *nodeResult) { r.Steps[j].State.Resources.CNIAdded++ })
			assertDetects("res-image_ready", orig, func(r *nodeResult) { r.Steps[j].State.Resources.ImageReady = !r.Steps[j].State.Resources.ImageReady })
			assertDetects("res-container_created", orig, func(r *nodeResult) {
				r.Steps[j].State.Resources.ContainerCreated = !r.Steps[j].State.Resources.ContainerCreated
			})
			assertDetects("res-task_created", orig, func(r *nodeResult) { r.Steps[j].State.Resources.TaskCreated = !r.Steps[j].State.Resources.TaskCreated })
			assertDetects("res-task_running", orig, func(r *nodeResult) { r.Steps[j].State.Resources.TaskRunning = !r.Steps[j].State.Resources.TaskRunning })
			assertDetects("res-stop_sent", orig, func(r *nodeResult) { r.Steps[j].State.Resources.StopSent = !r.Steps[j].State.Resources.StopSent })
			assertDetects("res-kill_sent", orig, func(r *nodeResult) { r.Steps[j].State.Resources.KillSent = !r.Steps[j].State.Resources.KillSent })
			assertDetects("state-failed", orig, func(r *nodeResult) { r.Steps[j].State.Failed = !r.Steps[j].State.Failed })
			assertDetects("state-cleanup_errors+1", orig, func(r *nodeResult) { r.Steps[j].State.CleanupErrors++ })
			assertDetects("state-restart_count+1", orig, func(r *nodeResult) { r.Steps[j].State.RestartCount++ })
			assertDetects("state-backoff_attempt+1", orig, func(r *nodeResult) { r.Steps[j].State.BackoffAttempt++ })
			assertDetects("state-has_exited", orig, func(r *nodeResult) { r.Steps[j].State.HasExited = !r.Steps[j].State.HasExited })
			assertDetects("state-last_exit_code+1", orig, func(r *nodeResult) { r.Steps[j].State.LastExitCode++ })
			assertDetects("state-started_at_ms+1", orig, func(r *nodeResult) { r.Steps[j].State.StartedAtMs++ })
			assertDetects("state-stop_sent_at_ms+1", orig, func(r *nodeResult) { r.Steps[j].State.StopSentAtMs++ })
			assertDetects("state-next_start_at_ms+1", orig, func(r *nodeResult) { r.Steps[j].State.NextStartAtMs++ })
		}
	}

	for _, kind := range []string{
		"id+1", "drop-last-step", "append-dup-step",
		"action-kind", "action-cni_index+1", "action-wait_until_ms+1",
		"well_formed", "backoff_ms+1",
		"state-phase", "state-desired_running",
		"res-netns_created", "res-cni_added+1", "res-image_ready", "res-container_created",
		"res-task_created", "res-task_running", "res-stop_sent", "res-kill_sent",
		"state-failed", "state-cleanup_errors+1", "state-restart_count+1", "state-backoff_attempt+1",
		"state-has_exited", "state-last_exit_code+1", "state-started_at_ms+1",
		"state-stop_sent_at_ms+1", "state-next_start_at_ms+1",
	} {
		if applied[kind] == 0 {
			t.Errorf("mutation %q was never applicable across %d cases", kind, len(cases))
		}
	}

	var raw map[string]any
	if err := json.Unmarshal(leanLines[0], &raw); err != nil {
		t.Fatal(err)
	}
	raw["extra"] = 1
	withTopExtra, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}

	const validRes = `{"netns_created":false,"cni_added":0,"image_ready":false,"container_created":false,"task_created":false,"task_running":false,"stop_sent":false,"kill_sent":false}`
	const validState = `{"phase":"init","desired_running":false,"resources":` + validRes + `,"failed":false,"cleanup_errors":0,"restart_count":0,"backoff_attempt":0,"has_exited":false,"last_exit_code":0,"started_at_ms":0,"stop_sent_at_ms":0,"next_start_at_ms":0}`
	const validAct = `{"kind":"none","cni_index":0,"wait_until_ms":0}`

	invalidLines := []struct {
		name string
		line string
	}{
		{"unknown top-level field", string(withTopExtra)},
		{"missing id", `{"steps":[]}`},
		{"missing steps", `{"id":0}`},
		{"null steps", `{"id":0,"steps":null}`},
		{"negative id", `{"id":-1,"steps":[]}`},
		{"trailing object", `{"id":0,"steps":[]}{"id":1,"steps":[]}`},
		{"unknown action kind", `{"id":0,"steps":[{"action":{"kind":"explode","cni_index":0,"wait_until_ms":0},"state":` + validState + `,"well_formed":true,"backoff_ms":0}]}`},
		{"negative cni_index", `{"id":0,"steps":[{"action":{"kind":"add_cni","cni_index":-1,"wait_until_ms":0},"state":` + validState + `,"well_formed":true,"backoff_ms":0}]}`},
		{"unknown phase", `{"id":0,"steps":[{"action":` + validAct + `,"state":{"phase":"bogus","desired_running":false,"resources":` + validRes + `,"failed":false,"cleanup_errors":0,"restart_count":0,"backoff_attempt":0,"has_exited":false,"last_exit_code":0,"started_at_ms":0,"stop_sent_at_ms":0,"next_start_at_ms":0},"well_formed":true,"backoff_ms":0}]}`},
		{"missing backoff_ms", `{"id":0,"steps":[{"action":` + validAct + `,"state":` + validState + `,"well_formed":true}]}`},
		{"negative backoff_ms", `{"id":0,"steps":[{"action":` + validAct + `,"state":` + validState + `,"well_formed":true,"backoff_ms":-1}]}`},
		{"null well_formed", `{"id":0,"steps":[{"action":` + validAct + `,"state":` + validState + `,"well_formed":null,"backoff_ms":0}]}`},
		{"negative cni_added", `{"id":0,"steps":[{"action":` + validAct + `,"state":{"phase":"init","desired_running":false,"resources":{"netns_created":false,"cni_added":-1,"image_ready":false,"container_created":false,"task_created":false,"task_running":false,"stop_sent":false,"kill_sent":false},"failed":false,"cleanup_errors":0,"restart_count":0,"backoff_attempt":0,"has_exited":false,"last_exit_code":0,"started_at_ms":0,"stop_sent_at_ms":0,"next_start_at_ms":0},"well_formed":true,"backoff_ms":0}]}`},
		{"extra state field", `{"id":0,"steps":[{"action":` + validAct + `,"state":{"phase":"init","desired_running":false,"resources":` + validRes + `,"failed":false,"cleanup_errors":0,"restart_count":0,"backoff_attempt":0,"has_exited":false,"last_exit_code":0,"started_at_ms":0,"stop_sent_at_ms":0,"next_start_at_ms":0,"extra":1},"well_formed":true,"backoff_ms":0}]}`},
	}
	for _, tc := range invalidLines {
		if _, err := decodeNodeResult([]byte(tc.line)); err == nil {
			t.Errorf("decodeNodeResult accepted %s: %s", tc.name, tc.line)
		}
	}

	validLine := `{"id":5,"steps":[{"action":{"kind":"del_cni","cni_index":1,"wait_until_ms":0},"state":{"phase":"rolling_back","desired_running":false,"resources":{"netns_created":true,"cni_added":2,"image_ready":false,"container_created":false,"task_created":false,"task_running":false,"stop_sent":false,"kill_sent":false},"failed":true,"cleanup_errors":1,"restart_count":2,"backoff_attempt":1,"has_exited":true,"last_exit_code":137,"started_at_ms":100,"stop_sent_at_ms":200,"next_start_at_ms":300},"well_formed":true,"backoff_ms":2000}]}`
	got, err := decodeNodeResult([]byte(validLine))
	if err != nil {
		t.Fatalf("decodeNodeResult(valid): %v", err)
	}
	want := nodeResult{
		ID: 5,
		Steps: []nodeStep{{
			Action: nodeActionWire{Kind: "del_cni", CNIIndex: 1, WaitUntilMs: 0},
			State: nodeStateWire{
				Phase:          "rolling_back",
				DesiredRunning: false,
				Resources:      nodeResourcesWire{NetnsCreated: true, CNIAdded: 2},
				Failed:         true,
				CleanupErrors:  1,
				RestartCount:   2,
				BackoffAttempt: 1,
				HasExited:      true,
				LastExitCode:   137,
				StartedAtMs:    100,
				StopSentAtMs:   200,
				NextStartAtMs:  300,
			},
			WellFormed: true,
			BackoffMs:  2000,
		}},
	}
	if diff := compareNodeResult(want, got); diff != "" {
		t.Fatalf("decodeNodeResult(valid) differs from expected: %s", diff)
	}
}

func TestNodeDRTCatchesBrokenImplementations(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateNodeCases(cfg.Seed, cfg.Cases)
	_, _, leanResults := leanNodeResults(t, cases)

	variants := []struct {
		name  string
		hooks nodeHooks
	}{
		{"CNI add failure does not count attempted plugin", nodeHooks{cniAddFailNoCountAttempt: true}},
		{"CNI DEL runs forward from index 0", nodeHooks{delCNIForwardOrder: true}},
		{"container deleted while task still created", nodeHooks{deleteContainerWhileTaskCreated: true}},
		{"task deleted while still running", nodeHooks{deleteTaskWhileRunning: true}},
		{"netns removed while CNI still added", nodeHooks{removeNetnsBeforeCNIClean: true}},
		{"SIGKILL escalates before grace period", nodeHooks{killBeforeGraceElapsed: true}},
		{"RestartNever restarts on non-zero exit", nodeHooks{restartNeverRestarts: true}},
		{"RestartOnFailure restarts on exit 0", nodeHooks{restartOnFailureRestartsOnZero: true}},
		{"RestartAlways stops on exit 0", nodeHooks{restartAlwaysStopsOnZero: true}},
		{"backoff omits max cap", nodeHooks{backoffNoCap: true}},
		{"backoff does not reset after healthy run", nodeHooks{backoffNoResetAfterHealthy: true}},
		{"backoff attempt does not increment on restart", nodeHooks{backoffNoIncrementOnRestart: true}},
		{"stop during backoff jumps straight to stopped", nodeHooks{stopInBackoffJumpsToStopped: true}},
		{"container created before backoff deadline", nodeHooks{containerCreateBeforeBackoffWait: true}},
		{"stopped accepts start_requested", nodeHooks{stoppedAcceptsStart: true}},
		{"cleanup errors not counted", nodeHooks{ignoreCleanupErrorCount: true}},
		{"container delete keeps image_ready during teardown", nodeHooks{containerDeleteKeepsImageTeardown: true}},
		{"stop in empty starting enters stopping", nodeHooks{stopInStartingEmptyEntersStopping: true}},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			mismatches := 0
			for i, tc := range cases {
				goRes, err := runNodeCase(tc, v.hooks)
				if err != nil {
					t.Fatalf("id=%d: broken implementation failed instead of answering wrongly: %v", tc.ID, err)
				}
				if compareNodeResult(leanResults[i], goRes) != "" {
					mismatches++
				}
			}
			if mismatches == 0 {
				t.Fatalf("broken implementation %q had 0 mismatches against the Lean model", v.name)
			}
			t.Logf("%d of %d cases differ", mismatches, len(cases))
		})
	}
}

func TestGenerateNodeCasesIsDeterministic(t *testing.T) {
	a := bytes.Join(encodeNodeCases(t, generateNodeCases(1, 100)), []byte("\n"))
	b := bytes.Join(encodeNodeCases(t, generateNodeCases(1, 100)), []byte("\n"))
	c := bytes.Join(encodeNodeCases(t, generateNodeCases(2, 100)), []byte("\n"))

	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different encoded cases")
	}
	if bytes.Equal(a, c) {
		t.Fatal("different seeds produced identical encoded cases")
	}
}

func TestNodeModelRejectsMalformedCase(t *testing.T) {
	valid := `{"id":0,"spec":{"restart_policy":"never","stop_grace_ms":1000,"backoff_base_ms":100,"backoff_max_ms":1000,"backoff_reset_ms":5000,"cni_count":2},"ops":[{"op":"next_step","now_ms":0}]}`
	const prefix = `{"id":1,"spec":{"restart_policy":"never","stop_grace_ms":1000,"backoff_base_ms":100,"backoff_max_ms":1000,"backoff_reset_ms":5000,"cni_count":2},"ops":[`
	for _, badOp := range []string{
		`{"op":"explode"}`,
		`{"op":"event","kind":"bogus","now_ms":0,"cni_index":0,"exit_code":0,"ok":false}`,
		`{"op":"event","kind":"start_requested","now_ms":-1,"cni_index":0,"exit_code":0,"ok":false}`,
		`{"op":"next_step","now_ms":-5}`,
		`{"op":"backoff","base_ms":100,"max_ms":1000,"attempt":"bad"}`,
	} {
		bad := prefix + badOp + `]}`
		cases := [][]byte{[]byte(valid), []byte(bad)}
		_, err := RunModel("node", cases)
		if err == nil {
			t.Errorf("RunModel accepted malformed case %s", bad)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte("line 2")) {
			t.Errorf("error %q does not name line 2 for %s", err.Error(), bad)
		}
	}
}
