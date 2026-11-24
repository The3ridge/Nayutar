package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/Nayutar/core/node"
	"github.com/Kaikei-e/Nayutar/core/node/nodesim"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "unit.lkal")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

func TestRunMainFlagAndConfigValidationErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMain(context.Background(), nil, &stdout, &stderr, defaultDeps()); code != 2 {
		t.Fatalf("missing -config exit code = %d, want 2", code)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runMain(context.Background(), []string{"-config", "/nonexistent/file.lkal"}, &stdout, &stderr, defaultDeps()); code != 1 {
		t.Fatalf("missing file exit code = %d, want 1", code)
	}

	badCfg := writeTempConfig(t, `@lkal "0.1"
containerd {}
cni {}
unit { id = "u1", image = "img:1" }
`)
	stdout.Reset()
	stderr.Reset()
	if code := runMain(context.Background(), []string{"-config", badCfg}, &stdout, &stderr, defaultDeps()); code != 1 {
		t.Fatalf("invalid config exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "ranges") {
		t.Fatalf("expected schema validation message in stderr, got: %s", stderr.String())
	}
}

func TestRunMainExitCodesRestartAndCleanSignalShutdown(t *testing.T) {
	t.Run("exit_code_propagation_never", func(t *testing.T) {
		cfgPath := writeTempConfig(t, `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24", gateway = "10.88.0.1" } }
unit {
  id = "job-1"
  image = "oci.local/job:v1"
  restart = .never
}
`)
		synctest.Test(t, func(t *testing.T) {
			f := nodesim.DefaultFaults()
			f.TaskBehavior = func(_ int, _ node.ContainerSpec) nodesim.TaskScript {
				return nodesim.TaskScript{RunDuration: 50 * time.Millisecond, ExitCode: 42}
			}
			world := nodesim.NewWorld(node.RealClock(), 2, f)
			closed := false
			deps := runtimeDeps{
				signalContext: func(p context.Context) (context.Context, context.CancelFunc) {
					return context.WithCancel(p)
				},
				buildAdapters: func(_ node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error) {
					return world, world, world, func() error { closed = true; return nil }, nil
				},
				clock: node.RealClock(),
			}

			var stdout, stderr bytes.Buffer
			code := runMain(context.Background(), []string{"-config", cfgPath}, &stdout, &stderr, deps)
			if code != 42 {
				t.Fatalf("exit code = %d, want 42 (stderr=%s)", code, stderr.String())
			}
			if !closed {
				t.Fatal("expected adapter closeFn to run")
			}
			if err := world.AssertClean(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(stdout.String(), "unit=job-1 phase=stopped restarts=0 exited=true exitCode=42") {
				t.Fatalf("unexpected stdout: %s", stdout.String())
			}
		})
	})

	t.Run("signal_cancellation_triggers_clean_lifo_teardown", func(t *testing.T) {
		cfgPath := writeTempConfig(t, `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24", gateway = "10.88.0.1" } }
unit {
  id = "srv-1"
  image = "oci.local/srv:v1"
  restart = .always
  stopGraceMs = 1000
}
`)
		synctest.Test(t, func(t *testing.T) {
			f := nodesim.DefaultFaults()
			f.TaskBehavior = func(_ int, _ node.ContainerSpec) nodesim.TaskScript {
				return nodesim.TaskScript{RunDuration: 0, TermDelay: 20 * time.Millisecond}
			}
			world := nodesim.NewWorld(node.RealClock(), 2, f)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()

			deps := runtimeDeps{
				signalContext: func(p context.Context) (context.Context, context.CancelFunc) {
					return context.WithCancel(p)
				},
				buildAdapters: func(_ node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error) {
					return world, world, world, nil, nil
				},
				clock: node.RealClock(),
			}

			var stdout, stderr bytes.Buffer
			code := runMain(ctx, []string{"-config", cfgPath}, &stdout, &stderr, deps)
			if code != 143 {
				t.Fatalf("exit code = %d, want 143 on SIGTERM shutdown (stderr=%s)", code, stderr.String())
			}
			if err := world.AssertClean(); err != nil {
				t.Fatal(err)
			}
		})
	})

	t.Run("setup_failure_returns_code_1_and_rolls_back", func(t *testing.T) {
		cfgPath := writeTempConfig(t, `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24", gateway = "10.88.0.1" } }
unit {
  id = "broken-1"
  image = "oci.local/broken:v1"
}
`)
		synctest.Test(t, func(t *testing.T) {
			f := nodesim.DefaultFaults()
			f.FailAddCNIStep = 1
			f.LeakIPBeforeStep1Fail = true
			world := nodesim.NewWorld(node.RealClock(), 2, f)
			deps := runtimeDeps{
				signalContext: func(p context.Context) (context.Context, context.CancelFunc) {
					return context.WithCancel(p)
				},
				buildAdapters: func(_ node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error) {
					return world, world, world, nil, nil
				},
				clock: node.RealClock(),
			}

			var stdout, stderr bytes.Buffer
			code := runMain(context.Background(), []string{"-config", cfgPath}, &stdout, &stderr, deps)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1 on CNI failure", code)
			}
			if err := world.AssertClean(); err != nil {
				t.Fatal(err)
			}
		})
	})
}
