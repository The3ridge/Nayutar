package node_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/Nayutar/core/node"
	"github.com/Kaikei-e/Nayutar/core/node/lifecycle"
	"github.com/Kaikei-e/Nayutar/core/node/nodesim"
)

func testUnit(policy lifecycle.RestartPolicy) node.UnitSpec {
	return node.UnitSpec{
		ID:    "test-unit",
		Image: "oci.local/app:v1",
		Lifecycle: lifecycle.Spec{
			RestartPolicy:  policy,
			StopGraceMs:    2000,
			BackoffBaseMs:  1000,
			BackoffMaxMs:   8000,
			BackoffResetMs: 10000,
			CNICount:       2,
		},
	}
}

func opSequence(ops []nodesim.OpRecord) []string {
	out := make([]string, len(ops))
	for i, op := range ops {
		out[i] = op.Op + ":" + op.Detail
	}
	return out
}

type waitErrRuntime struct {
	node.Runtime
	waitErr error
}

func (r *waitErrRuntime) StartTask(_ context.Context, _ string) (<-chan node.ExitResult, error) {
	ch := make(chan node.ExitResult, 1)
	ch <- node.ExitResult{ExitCode: 255, Err: r.waitErr}
	close(ch)
	return ch, nil
}

func TestRunnerPartialFailureRollbacks(t *testing.T) {
	cases := []struct {
		name     string
		faults   func() nodesim.Faults
		wantTail []string
	}{
		{
			name: "cni_step0_failure",
			faults: func() nodesim.Faults {
				f := nodesim.DefaultFaults()
				f.FailAddCNIStep = 0
				return f
			},
			wantTail: []string{
				"create_netns:/sim/netns/test-unit",
				"add_cni:test-unit:0",
				"del_cni:test-unit:0",
				"remove_netns:test-unit",
			},
		},
		{
			name: "cni_step1_failure_frees_ipam",
			faults: func() nodesim.Faults {
				f := nodesim.DefaultFaults()
				f.FailAddCNIStep = 1
				f.LeakIPBeforeStep1Fail = true
				return f
			},
			wantTail: []string{
				"create_netns:/sim/netns/test-unit",
				"add_cni:test-unit:0",
				"add_cni:test-unit:1",
				"del_cni:test-unit:1",
				"del_cni:test-unit:0",
				"remove_netns:test-unit",
			},
		},
		{
			name: "create_container_failure",
			faults: func() nodesim.Faults {
				f := nodesim.DefaultFaults()
				f.FailCreateContainer = true
				return f
			},
			wantTail: []string{
				"create_netns:/sim/netns/test-unit",
				"add_cni:test-unit:0",
				"add_cni:test-unit:1",
				"ensure_image:oci.local/app:v1",
				"create_container:test-unit",
				"del_cni:test-unit:1",
				"del_cni:test-unit:0",
				"remove_netns:test-unit",
			},
		},
		{
			name: "start_task_failure",
			faults: func() nodesim.Faults {
				f := nodesim.DefaultFaults()
				f.FailStartTask = true
				return f
			},
			wantTail: []string{
				"create_netns:/sim/netns/test-unit",
				"add_cni:test-unit:0",
				"add_cni:test-unit:1",
				"ensure_image:oci.local/app:v1",
				"create_container:test-unit",
				"start_task:test-unit",
				"delete_container:test-unit",
				"del_cni:test-unit:1",
				"del_cni:test-unit:0",
				"remove_netns:test-unit",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				world := nodesim.NewWorld(node.RealClock(), 2, tc.faults())
				r, err := node.NewRunner(node.RunnerConfig{
					Unit:    testUnit(lifecycle.RestartAlways),
					Runtime: world,
					Netns:   world,
					CNI:     world,
				})
				if err != nil {
					t.Fatalf("NewRunner: %v", err)
				}
				st, runErr := r.Run(context.Background())
				if runErr == nil || !st.Failed || st.Phase != lifecycle.PhaseStopped || !st.Resources.Empty() {
					t.Fatalf("unexpected result: st=%+v err=%v", st, runErr)
				}
				if err := world.AssertClean(); err != nil {
					t.Fatal(err)
				}
				got := opSequence(world.Ops())
				if !reflect.DeepEqual(got, tc.wantTail) {
					t.Fatalf("ops = %v, want %v", got, tc.wantTail)
				}
			})
		})
	}
}

func TestRunnerRestartPoliciesExitZeroAndNonZero(t *testing.T) {
	cases := []struct {
		name         string
		policy       lifecycle.RestartPolicy
		exitCodes    []uint32
		wantRestarts int
		wantLastCode uint32
	}{
		{
			name:         "never_nonzero",
			policy:       lifecycle.RestartNever,
			exitCodes:    []uint32{7},
			wantRestarts: 0,
			wantLastCode: 7,
		},
		{
			name:         "onFailure_zero_stops",
			policy:       lifecycle.RestartOnFailure,
			exitCodes:    []uint32{0},
			wantRestarts: 0,
			wantLastCode: 0,
		},
		{
			name:         "onFailure_nonzero_then_zero",
			policy:       lifecycle.RestartOnFailure,
			exitCodes:    []uint32{2, 3, 0},
			wantRestarts: 2,
			wantLastCode: 0,
		},
		{
			name:         "always_restarts_on_zero",
			policy:       lifecycle.RestartAlways,
			exitCodes:    []uint32{0, 0},
			wantRestarts: 2,
			wantLastCode: 143,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := nodesim.DefaultFaults()
				f.TaskBehavior = func(attempt int, _ node.ContainerSpec) nodesim.TaskScript {
					if attempt < len(tc.exitCodes) {
						return nodesim.TaskScript{RunDuration: 100 * time.Millisecond, ExitCode: tc.exitCodes[attempt]}
					}
					return nodesim.TaskScript{RunDuration: 0}
				}
				world := nodesim.NewWorld(node.RealClock(), 2, f)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				r, err := node.NewRunner(node.RunnerConfig{
					Unit:    testUnit(tc.policy),
					Runtime: world,
					Netns:   world,
					CNI:     world,
					OnTransition: func(st lifecycle.State) {
						if tc.policy == lifecycle.RestartAlways && st.Phase == lifecycle.PhaseRunning && st.RestartCount == tc.wantRestarts {
							cancel()
						}
					},
				})
				if err != nil {
					t.Fatalf("NewRunner: %v", err)
				}
				st, runErr := r.Run(ctx)
				if runErr != nil {
					t.Fatalf("Run failed: %v", runErr)
				}
				if err := world.AssertClean(); err != nil {
					t.Fatal(err)
				}
				if st.RestartCount != tc.wantRestarts || !st.HasExited || st.LastExitCode != tc.wantLastCode {
					t.Fatalf("got restarts=%d hasExited=%v code=%d, want restarts=%d code=%d",
						st.RestartCount, st.HasExited, st.LastExitCode, tc.wantRestarts, tc.wantLastCode)
				}
			})
		})
	}
}

func TestRunnerSIGTERMTimeoutEscalatesToSIGKILL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := nodesim.DefaultFaults()
		f.TaskBehavior = func(_ int, _ node.ContainerSpec) nodesim.TaskScript {
			return nodesim.TaskScript{RunDuration: 0, IgnoreSIGTERM: true}
		}
		world := nodesim.NewWorld(node.RealClock(), 2, f)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		r, err := node.NewRunner(node.RunnerConfig{
			Unit:    testUnit(lifecycle.RestartAlways),
			Runtime: world,
			Netns:   world,
			CNI:     world,
			OnTransition: func(st lifecycle.State) {
				if st.Phase == lifecycle.PhaseRunning {
					cancel()
				}
			},
		})
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		st, runErr := r.Run(ctx)
		if runErr != nil {
			t.Fatalf("Run failed: %v", runErr)
		}
		if err := world.AssertClean(); err != nil {
			t.Fatal(err)
		}
		if st.LastExitCode != 137 {
			t.Fatalf("got exit code %d, want 137 after SIGKILL escalation", st.LastExitCode)
		}

		var termMs, killMs int64 = -1, -1
		for _, op := range world.Ops() {
			if op.Op == "signal_task" && op.Detail == "test-unit:15" {
				termMs = op.TimeMs
			}
			if op.Op == "signal_task" && op.Detail == "test-unit:9" {
				killMs = op.TimeMs
			}
		}
		if termMs < 0 || killMs < 0 {
			t.Fatalf("expected both SIGTERM and SIGKILL, ops=%+v", world.Ops())
		}
		if killMs-termMs < 2000 {
			t.Fatalf("SIGKILL sent too early: term=%d kill=%d grace=2000", termMs, killMs)
		}
	})
}

func TestRunnerBackoffResetAfterHealthyRunAndVanishedNetnsCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := nodesim.DefaultFaults()
		f.VanishNetnsAfterStart = true
		f.TaskBehavior = func(attempt int, _ node.ContainerSpec) nodesim.TaskScript {
			switch attempt {
			case 0:
				return nodesim.TaskScript{RunDuration: 500 * time.Millisecond, ExitCode: 1}
			case 1:
				return nodesim.TaskScript{RunDuration: 500 * time.Millisecond, ExitCode: 1}
			case 2:
				// Run longer than BackoffResetMs (10s) to trigger backoff reset.
				return nodesim.TaskScript{RunDuration: 12 * time.Second, ExitCode: 1}
			case 3:
				return nodesim.TaskScript{RunDuration: 500 * time.Millisecond, ExitCode: 0}
			default:
				return nodesim.TaskScript{RunDuration: 0}
			}
		}
		// Keep netns intact during restarts; only test vanished netns on final shutdown in separate subtest.
		f.VanishNetnsAfterStart = false
		world := nodesim.NewWorld(node.RealClock(), 2, f)
		r, err := node.NewRunner(node.RunnerConfig{
			Unit:    testUnit(lifecycle.RestartOnFailure),
			Runtime: world,
			Netns:   world,
			CNI:     world,
		})
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		st, runErr := r.Run(context.Background())
		if runErr != nil {
			t.Fatalf("Run failed: %v", runErr)
		}
		if err := world.AssertClean(); err != nil {
			t.Fatal(err)
		}
		if st.RestartCount != 3 || st.LastExitCode != 0 {
			t.Fatalf("unexpected state: %+v", st)
		}

		// Verify start timestamps reflect 1s -> 2s -> healthy reset -> 1s backoff intervals.
		var starts []int64
		for _, op := range world.Ops() {
			if op.Op == "start_task" {
				starts = append(starts, op.TimeMs)
			}
		}
		if len(starts) != 4 {
			t.Fatalf("got %d starts (%v), want 4", len(starts), starts)
		}
		// start0=0, exit0=500, start1=1500; exit1=2000, start2=4000; exit2=16000, start3=17000.
		if starts[1]-starts[0] != 1500 || starts[2]-starts[1] != 2500 || starts[3]-starts[2] != 13000 {
			t.Fatalf("unexpected start intervals: starts=%v", starts)
		}
	})

	t.Run("vanished_netns_still_runs_cni_del", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := nodesim.DefaultFaults()
			f.VanishNetnsAfterStart = true
			f.TaskBehavior = func(_ int, _ node.ContainerSpec) nodesim.TaskScript {
				return nodesim.TaskScript{RunDuration: 100 * time.Millisecond, ExitCode: 0}
			}
			world := nodesim.NewWorld(node.RealClock(), 2, f)
			r, err := node.NewRunner(node.RunnerConfig{
				Unit:    testUnit(lifecycle.RestartOnFailure),
				Runtime: world,
				Netns:   world,
				CNI:     world,
			})
			if err != nil {
				t.Fatalf("NewRunner: %v", err)
			}
			if _, err := r.Run(context.Background()); err != nil {
				t.Fatalf("Run failed: %v", err)
			}
			if err := world.AssertClean(); err != nil {
				t.Fatal(err)
			}
		})
	})

	t.Run("wait_transport_error_fails_closed", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			world := nodesim.NewWorld(node.RealClock(), 2, nodesim.DefaultFaults())
			waitErr := errors.New("wait rpc stream lost")
			rt := &waitErrRuntime{Runtime: world, waitErr: waitErr}
			r, err := node.NewRunner(node.RunnerConfig{
				Unit:    testUnit(lifecycle.RestartNever),
				Runtime: rt,
				Netns:   world,
				CNI:     world,
			})
			if err != nil {
				t.Fatalf("NewRunner: %v", err)
			}
			st, runErr := r.Run(context.Background())
			if !errors.Is(runErr, waitErr) {
				t.Fatalf("Run err = %v, want wrapped %v", runErr, waitErr)
			}
			if st.Phase != lifecycle.PhaseStopped || !st.Resources.Empty() || st.LastExitCode != 255 {
				t.Fatalf("unexpected state after wait error: %+v", st)
			}
			if err := world.AssertClean(); err != nil {
				t.Fatal(err)
			}
		})
	})

	t.Run("single_use_runner", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			f := nodesim.DefaultFaults()
			f.TaskBehavior = func(_ int, _ node.ContainerSpec) nodesim.TaskScript {
				return nodesim.TaskScript{RunDuration: 10 * time.Millisecond, ExitCode: 0}
			}
			world := nodesim.NewWorld(node.RealClock(), 2, f)
			r, err := node.NewRunner(node.RunnerConfig{
				Unit:    testUnit(lifecycle.RestartNever),
				Runtime: world,
				Netns:   world,
				CNI:     world,
			})
			if err != nil {
				t.Fatalf("NewRunner: %v", err)
			}
			if _, err := r.Run(context.Background()); err != nil {
				t.Fatalf("first Run failed: %v", err)
			}
			if _, err := r.Run(context.Background()); !errors.Is(err, node.ErrAlreadyRun) {
				t.Fatalf("second Run err = %v, want ErrAlreadyRun", err)
			}
		})
	})
}
