package lifecycle

import (
	"math"
	"math/rand/v2"
	"reflect"
	"testing"
)

func defaultSpec() Spec {
	return Spec{
		RestartPolicy:  RestartOnFailure,
		StopGraceMs:    5000,
		BackoffBaseMs:  1000,
		BackoffMaxMs:   16000,
		BackoffResetMs: 60000,
		CNICount:       2,
	}
}

func TestSpecValidate(t *testing.T) {
	if err := defaultSpec().Validate(); err != nil {
		t.Fatalf("default spec failed validation: %v", err)
	}
	bad := []Spec{
		{RestartPolicy: RestartPolicy(99), BackoffBaseMs: 1, BackoffMaxMs: 1},
		{RestartPolicy: RestartNever, StopGraceMs: -1, BackoffBaseMs: 1, BackoffMaxMs: 1},
		{RestartPolicy: RestartNever, BackoffBaseMs: 0, BackoffMaxMs: 1},
		{RestartPolicy: RestartNever, BackoffBaseMs: 10, BackoffMaxMs: 5},
		{RestartPolicy: RestartNever, BackoffBaseMs: 1, BackoffMaxMs: 1, BackoffResetMs: -1},
		{RestartPolicy: RestartNever, BackoffBaseMs: 1, BackoffMaxMs: 1, CNICount: -1},
	}
	for i, sp := range bad {
		if err := sp.Validate(); err == nil {
			t.Fatalf("case %d: expected validation error for %+v", i, sp)
		}
	}
}

func TestBackoffMsBoundedMonotoneAndOverflowSafe(t *testing.T) {
	base, maxMs := int64(1000), int64(10000)
	want := []int64{1000, 2000, 4000, 8000, 10000, 10000}
	for attempt, w := range want {
		got := BackoffMs(base, maxMs, attempt)
		if got != w {
			t.Fatalf("attempt %d: got %d, want %d", attempt, got, w)
		}
	}
	// Overflow safety with large maxMs and large attempt.
	bigMax := int64(math.MaxInt64 - 1)
	prev := int64(0)
	for attempt := range 80 {
		got := BackoffMs(1000, bigMax, attempt)
		if got < 1000 || got > bigMax {
			t.Fatalf("attempt %d: out of range %d", attempt, got)
		}
		if got < prev {
			t.Fatalf("attempt %d: non-monotone %d < %d", attempt, got, prev)
		}
		prev = got
	}
	if got := BackoffMs(0, 1000, 3); got != 0 {
		t.Fatalf("zero base: got %d, want 0", got)
	}
	if got := BackoffMs(1000, 500, 0); got != 500 {
		t.Fatalf("max < base: got %d, want 500", got)
	}
}

func TestForwardSetupAndCleanStopWithSIGKILLEscalation(t *testing.T) {
	sp := defaultSpec()
	st := InitState()
	assertWellFormed(t, sp, st)

	st = ApplyEvent(sp, st, Event{Kind: EvStartRequested, NowMs: 100})
	expectAction(t, sp, st, 100, Action{Kind: ActionCreateNetns})
	st = ApplyEvent(sp, st, Event{Kind: EvNetnsCreated, NowMs: 110})
	expectAction(t, sp, st, 110, Action{Kind: ActionAddCNI, CNIIndex: 0})
	st = ApplyEvent(sp, st, Event{Kind: EvCNIAdded, NowMs: 120, CNIIndex: 0})
	expectAction(t, sp, st, 120, Action{Kind: ActionAddCNI, CNIIndex: 1})
	st = ApplyEvent(sp, st, Event{Kind: EvCNIAdded, NowMs: 130, CNIIndex: 1})
	expectAction(t, sp, st, 130, Action{Kind: ActionEnsureImage})
	st = ApplyEvent(sp, st, Event{Kind: EvImageReady, NowMs: 140})
	expectAction(t, sp, st, 140, Action{Kind: ActionCreateContainer})
	st = ApplyEvent(sp, st, Event{Kind: EvContainerCreated, NowMs: 150})
	expectAction(t, sp, st, 150, Action{Kind: ActionStartTask})
	st = ApplyEvent(sp, st, Event{Kind: EvTaskStarted, NowMs: 160})
	if st.Phase != PhaseRunning {
		t.Fatalf("got phase %s, want running", st.Phase)
	}
	expectAction(t, sp, st, 160, Action{Kind: ActionWait})

	st = ApplyEvent(sp, st, Event{Kind: EvStopRequested, NowMs: 1000})
	expectAction(t, sp, st, 1000, Action{Kind: ActionTermTask})
	st = ApplyEvent(sp, st, Event{Kind: EvTermSent, NowMs: 1000})

	// Before grace expires, SIGKILL must not be issued or accepted.
	expectAction(t, sp, st, 5999, Action{Kind: ActionWait, WaitUntilMs: 6000})
	stPremature := ApplyEvent(sp, st, Event{Kind: EvKillSent, NowMs: 5999})
	if stPremature.Resources.KillSent {
		t.Fatal("premature SIGKILL was accepted before StopGraceMs elapsed")
	}

	// At exact grace boundary, SIGKILL is issued.
	expectAction(t, sp, st, 6000, Action{Kind: ActionKillTask})
	st = ApplyEvent(sp, st, Event{Kind: EvKillSent, NowMs: 6000})
	expectAction(t, sp, st, 6000, Action{Kind: ActionWait})

	st = ApplyEvent(sp, st, Event{Kind: EvTaskExited, NowMs: 6050, ExitCode: 137})
	expectAction(t, sp, st, 6050, Action{Kind: ActionDeleteTask})
	st = ApplyEvent(sp, st, Event{Kind: EvTaskDeleted, NowMs: 6060, OK: true})
	expectAction(t, sp, st, 6060, Action{Kind: ActionDeleteContainer})
	st = ApplyEvent(sp, st, Event{Kind: EvContainerDeleted, NowMs: 6070, OK: true})
	expectAction(t, sp, st, 6070, Action{Kind: ActionDelCNI, CNIIndex: 1})
	st = ApplyEvent(sp, st, Event{Kind: EvCNIDeleted, NowMs: 6080, CNIIndex: 1, OK: true})
	expectAction(t, sp, st, 6080, Action{Kind: ActionDelCNI, CNIIndex: 0})
	st = ApplyEvent(sp, st, Event{Kind: EvCNIDeleted, NowMs: 6090, CNIIndex: 0, OK: true})
	expectAction(t, sp, st, 6090, Action{Kind: ActionRemoveNetns})
	st = ApplyEvent(sp, st, Event{Kind: EvNetnsRemoved, NowMs: 6100, OK: true})

	if st.Phase != PhaseStopped || !st.Resources.Empty() {
		t.Fatalf("terminal state leaked resources: %+v", st)
	}
	if !st.HasExited || st.LastExitCode != 137 {
		t.Fatalf("exit status not preserved: %+v", st)
	}
	assertWellFormed(t, sp, st)
}

func TestPartialFailureRollbackStrictLIFO(t *testing.T) {
	sp := defaultSpec()
	cases := []struct {
		name         string
		failAt       ActionKind
		failCNIIndex int
		wantTeardown []Action
	}{
		{
			name:         "netns_fail",
			failAt:       ActionCreateNetns,
			wantTeardown: nil,
		},
		{
			name:         "cni_step0_fail",
			failAt:       ActionAddCNI,
			failCNIIndex: 0,
			wantTeardown: []Action{
				{Kind: ActionDelCNI, CNIIndex: 0},
				{Kind: ActionRemoveNetns},
			},
		},
		{
			name:         "cni_step1_fail",
			failAt:       ActionAddCNI,
			failCNIIndex: 1,
			wantTeardown: []Action{
				{Kind: ActionDelCNI, CNIIndex: 1},
				{Kind: ActionDelCNI, CNIIndex: 0},
				{Kind: ActionRemoveNetns},
			},
		},
		{
			name:   "image_fail",
			failAt: ActionEnsureImage,
			wantTeardown: []Action{
				{Kind: ActionDelCNI, CNIIndex: 1},
				{Kind: ActionDelCNI, CNIIndex: 0},
				{Kind: ActionRemoveNetns},
			},
		},
		{
			name:   "container_fail",
			failAt: ActionCreateContainer,
			wantTeardown: []Action{
				{Kind: ActionDelCNI, CNIIndex: 1},
				{Kind: ActionDelCNI, CNIIndex: 0},
				{Kind: ActionRemoveNetns},
			},
		},
		{
			name:   "start_task_fail",
			failAt: ActionStartTask,
			wantTeardown: []Action{
				{Kind: ActionDeleteContainer},
				{Kind: ActionDelCNI, CNIIndex: 1},
				{Kind: ActionDelCNI, CNIIndex: 0},
				{Kind: ActionRemoveNetns},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := ApplyEvent(sp, InitState(), Event{Kind: EvStartRequested, NowMs: 0})
			now := int64(10)
			failed := false
			var teardown []Action

			for range 30 {
				assertWellFormed(t, sp, st)
				act := NextStep(sp, st, now)
				if act.Kind == ActionNone {
					break
				}
				if failed {
					teardown = append(teardown, act)
				}
				shouldFail := !failed && act.Kind == tc.failAt && (act.Kind != ActionAddCNI || act.CNIIndex == tc.failCNIIndex)
				switch act.Kind {
				case ActionCreateNetns:
					if shouldFail {
						failed = true
						st = ApplyEvent(sp, st, Event{Kind: EvNetnsCreateFailed, NowMs: now})
					} else {
						st = ApplyEvent(sp, st, Event{Kind: EvNetnsCreated, NowMs: now})
					}
				case ActionAddCNI:
					if shouldFail {
						failed = true
						st = ApplyEvent(sp, st, Event{Kind: EvCNIAddFailed, NowMs: now, CNIIndex: act.CNIIndex})
					} else {
						st = ApplyEvent(sp, st, Event{Kind: EvCNIAdded, NowMs: now, CNIIndex: act.CNIIndex})
					}
				case ActionEnsureImage:
					if shouldFail {
						failed = true
						st = ApplyEvent(sp, st, Event{Kind: EvImageFailed, NowMs: now})
					} else {
						st = ApplyEvent(sp, st, Event{Kind: EvImageReady, NowMs: now})
					}
				case ActionCreateContainer:
					if shouldFail {
						failed = true
						st = ApplyEvent(sp, st, Event{Kind: EvContainerCreateFailed, NowMs: now})
					} else {
						st = ApplyEvent(sp, st, Event{Kind: EvContainerCreated, NowMs: now})
					}
				case ActionStartTask:
					if shouldFail {
						failed = true
						st = ApplyEvent(sp, st, Event{Kind: EvTaskStartFailed, NowMs: now})
					} else {
						st = ApplyEvent(sp, st, Event{Kind: EvTaskStarted, NowMs: now})
					}
				case ActionDeleteContainer:
					st = ApplyEvent(sp, st, Event{Kind: EvContainerDeleted, NowMs: now, OK: true})
				case ActionDelCNI:
					// Even when CNI DEL reports an error, rollback must continue through all steps.
					st = ApplyEvent(sp, st, Event{Kind: EvCNIDeleted, NowMs: now, CNIIndex: act.CNIIndex, OK: false})
				case ActionRemoveNetns:
					st = ApplyEvent(sp, st, Event{Kind: EvNetnsRemoved, NowMs: now, OK: true})
				default:
					t.Fatalf("unexpected action %+v", act)
				}
				now += 10
			}

			if st.Phase != PhaseStopped || !st.Resources.Empty() || !st.Failed {
				t.Fatalf("unexpected final state: %+v", st)
			}
			if !reflect.DeepEqual(teardown, tc.wantTeardown) {
				t.Fatalf("teardown order = %+v, want %+v", teardown, tc.wantTeardown)
			}
		})
	}
}

func TestRestartPoliciesAndBackoffResetAfterHealthyRun(t *testing.T) {
	// RestartNever does not restart on non-zero exit.
	spNever := defaultSpec()
	spNever.RestartPolicy = RestartNever
	st := runToRunning(t, spNever, 0)
	st = ApplyEvent(spNever, st, Event{Kind: EvTaskExited, NowMs: 500, ExitCode: 1})
	if st.Phase != PhaseStopping || st.RestartCount != 0 {
		t.Fatalf("RestartNever exited: %+v", st)
	}

	// RestartOnFailure stops on exit 0 and restarts on non-zero.
	spFail := defaultSpec()
	spFail.RestartPolicy = RestartOnFailure
	st = runToRunning(t, spFail, 0)
	st = ApplyEvent(spFail, st, Event{Kind: EvTaskExited, NowMs: 500, ExitCode: 0})
	if st.Phase != PhaseStopping || st.RestartCount != 0 {
		t.Fatalf("RestartOnFailure exit 0: %+v", st)
	}

	// RestartAlways restarts on exit 0 with exponential backoff and resets after healthy window.
	spAlways := defaultSpec()
	spAlways.RestartPolicy = RestartAlways
	st = runToRunning(t, spAlways, 1000)

	// Crash 1 (quick): attempt 0 -> delay 1000ms.
	st = ApplyEvent(spAlways, st, Event{Kind: EvTaskExited, NowMs: 1500, ExitCode: 0})
	if st.Phase != PhaseBackoff || st.RestartCount != 1 || st.BackoffAttempt != 0 || st.NextStartAtMs != 2500 {
		t.Fatalf("after exit 1: %+v", st)
	}
	st = restartFromBackoff(t, spAlways, st, 2500)

	// Crash 2 (quick): attempt 1 -> delay 2000ms.
	st = ApplyEvent(spAlways, st, Event{Kind: EvTaskExited, NowMs: 3000, ExitCode: 2})
	if st.Phase != PhaseBackoff || st.RestartCount != 2 || st.BackoffAttempt != 1 || st.NextStartAtMs != 5000 {
		t.Fatalf("after exit 2: %+v", st)
	}
	st = restartFromBackoff(t, spAlways, st, 5000)

	// Crash 3 (quick): attempt 2 -> delay 4000ms.
	st = ApplyEvent(spAlways, st, Event{Kind: EvTaskExited, NowMs: 5500, ExitCode: 3})
	if st.Phase != PhaseBackoff || st.RestartCount != 3 || st.BackoffAttempt != 2 || st.NextStartAtMs != 9500 {
		t.Fatalf("after exit 3: %+v", st)
	}
	st = restartFromBackoff(t, spAlways, st, 9500)

	// Healthy run (>= 60000ms) then exit -> backoff resets to 0 and delay returns to base (1000ms).
	st = ApplyEvent(spAlways, st, Event{Kind: EvTaskExited, NowMs: 69500, ExitCode: 1})
	if st.Phase != PhaseBackoff || st.RestartCount != 4 || st.BackoffAttempt != 0 || st.NextStartAtMs != 70500 {
		t.Fatalf("after healthy exit: %+v", st)
	}
	st = restartFromBackoff(t, spAlways, st, 70500)
	// Next quick crash uses attempt 1 -> delay 2000ms.
	st = ApplyEvent(spAlways, st, Event{Kind: EvTaskExited, NowMs: 71000, ExitCode: 1})
	if st.BackoffAttempt != 1 || st.NextStartAtMs != 73000 {
		t.Fatalf("after post-reset crash: %+v", st)
	}
}

func TestRandomEventSequencesPreserveInvariants(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 99))
	policies := []RestartPolicy{RestartNever, RestartOnFailure, RestartAlways}
	for range 200 {
		base := int64(1 + rng.IntN(500))
		sp := Spec{
			RestartPolicy:  policies[rng.IntN(len(policies))],
			StopGraceMs:    int64(rng.IntN(1000)),
			BackoffBaseMs:  base,
			BackoffMaxMs:   base + int64(rng.IntN(5000)),
			BackoffResetMs: int64(rng.IntN(5000)),
			CNICount:       rng.IntN(4),
		}
		st := InitState()
		now := int64(0)
		for range 80 {
			assertWellFormed(t, sp, st)
			now += int64(rng.IntN(500))
			ev := Event{
				Kind:     EventKind(rng.IntN(int(EvNetnsRemoved) + 1)),
				NowMs:    now,
				CNIIndex: rng.IntN(max(1, sp.CNICount+1)),
				ExitCode: uint32(rng.IntN(3)),
				OK:       rng.IntN(2) == 0,
			}
			prevRestarts := st.RestartCount
			st = ApplyEvent(sp, st, ev)
			if st.RestartCount < prevRestarts {
				t.Fatalf("restart count decreased: %d -> %d", prevRestarts, st.RestartCount)
			}
			if st.Phase == PhaseStopped && !st.Resources.Empty() {
				t.Fatalf("PhaseStopped has leaked resources: %+v", st)
			}
		}
	}
}

func runToRunning(t *testing.T, sp Spec, startMs int64) State {
	t.Helper()
	st := ApplyEvent(sp, InitState(), Event{Kind: EvStartRequested, NowMs: startMs})
	st = ApplyEvent(sp, st, Event{Kind: EvNetnsCreated, NowMs: startMs})
	for i := range sp.CNICount {
		st = ApplyEvent(sp, st, Event{Kind: EvCNIAdded, NowMs: startMs, CNIIndex: i})
	}
	st = ApplyEvent(sp, st, Event{Kind: EvImageReady, NowMs: startMs})
	st = ApplyEvent(sp, st, Event{Kind: EvContainerCreated, NowMs: startMs})
	st = ApplyEvent(sp, st, Event{Kind: EvTaskStarted, NowMs: startMs})
	if st.Phase != PhaseRunning {
		t.Fatalf("expected running, got %s", st.Phase)
	}
	return st
}

func restartFromBackoff(t *testing.T, sp Spec, st State, startAtMs int64) State {
	t.Helper()
	// Restart must reuse Netns + CNI + Image and only delete/recreate Task + Container.
	expectAction(t, sp, st, startAtMs-1, Action{Kind: ActionDeleteTask})
	st = ApplyEvent(sp, st, Event{Kind: EvTaskDeleted, NowMs: startAtMs - 1, OK: true})
	expectAction(t, sp, st, startAtMs-1, Action{Kind: ActionDeleteContainer})
	st = ApplyEvent(sp, st, Event{Kind: EvContainerDeleted, NowMs: startAtMs - 1, OK: true})
	expectAction(t, sp, st, startAtMs-1, Action{Kind: ActionWait, WaitUntilMs: startAtMs})
	expectAction(t, sp, st, startAtMs, Action{Kind: ActionCreateContainer})
	st = ApplyEvent(sp, st, Event{Kind: EvContainerCreated, NowMs: startAtMs})
	expectAction(t, sp, st, startAtMs, Action{Kind: ActionStartTask})
	st = ApplyEvent(sp, st, Event{Kind: EvTaskStarted, NowMs: startAtMs})
	if st.Phase != PhaseRunning || !st.Resources.NetnsCreated || st.Resources.CNIAdded != sp.CNICount {
		t.Fatalf("restart did not preserve netns/CNI: %+v", st)
	}
	return st
}

func expectAction(t *testing.T, sp Spec, st State, nowMs int64, want Action) {
	t.Helper()
	got := NextStep(sp, st, nowMs)
	if got != want {
		t.Fatalf("NextStep(phase=%s, now=%d) = %+v, want %+v", st.Phase, nowMs, got, want)
	}
}

func assertWellFormed(t *testing.T, sp Spec, st State) {
	t.Helper()
	if !WellFormed(sp, st) {
		t.Fatalf("state violated WellFormed invariant: %+v", st)
	}
}
