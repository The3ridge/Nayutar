package main

import (
	"bytes"
	"context"
	"errors"
	"path"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/Nayutar/core/netpol"
	"github.com/Kaikei-e/Nayutar/core/node"
	"github.com/Kaikei-e/Nayutar/core/node/nodesim"
	"github.com/Kaikei-e/Nayutar/core/sim"
)

func writeMemFile(t *testing.T, disk *sim.MemDisk, filePath, body string) {
	t.Helper()
	if err := disk.MkdirAll(path.Dir(filePath), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path.Dir(filePath), err)
	}
	if err := disk.WriteFile(filePath, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", filePath, err)
	}
}

func TestRunMainFlagAndConfigValidationErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		disk := sim.NewMemDisk()
		deps := defaultDeps()
		deps.disk = disk

		var stdout, stderr bytes.Buffer
		if code := runMain(context.Background(), nil, &stdout, &stderr, deps); code != 2 {
			t.Fatalf("missing -config exit code = %d, want 2", code)
		}

		stdout.Reset()
		stderr.Reset()
		if code := runMain(context.Background(), []string{"-config", "/nonexistent/file.lkal"}, &stdout, &stderr, deps); code != 1 {
			t.Fatalf("missing file exit code = %d, want 1", code)
		}

		const badCfg = "/sim/bad.lkal"
		writeMemFile(t, disk, badCfg, `@lkal "0.1"
containerd {}
cni {}
unit { id = "u1", image = "img:1" }
`)
		stdout.Reset()
		stderr.Reset()
		if code := runMain(context.Background(), []string{"-config", badCfg}, &stdout, &stderr, deps); code != 1 {
			t.Fatalf("invalid config exit code = %d, want 1", code)
		}
		if !strings.Contains(stderr.String(), "ranges") {
			t.Fatalf("expected schema validation message in stderr, got: %s", stderr.String())
		}
	})
}

func TestRunMainExitCodesRestartAndCleanSignalShutdown(t *testing.T) {
	t.Run("exit_code_propagation_never", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			disk := sim.NewMemDisk()
			const cfgPath = "/sim/unit.lkal"
			writeMemFile(t, disk, cfgPath, `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24", gateway = "10.88.0.1" } }
unit {
  id = "job-1"
  image = "oci.local/job:v1"
  restart = .never
}
`)
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
				disk:  disk,
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
		synctest.Test(t, func(t *testing.T) {
			disk := sim.NewMemDisk()
			const cfgPath = "/sim/srv.lkal"
			writeMemFile(t, disk, cfgPath, `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24", gateway = "10.88.0.1" } }
unit {
  id = "srv-1"
  image = "oci.local/srv:v1"
  restart = .always
  stopGraceMs = 1000
}
`)
			f := nodesim.DefaultFaults()
			f.TaskBehavior = func(_ int, _ node.ContainerSpec) nodesim.TaskScript {
				return nodesim.TaskScript{RunDuration: 0, TermDelay: 20 * time.Millisecond}
			}
			world := nodesim.NewWorld(node.RealClock(), 2, f)
			ctx, cancel := sim.WithTimeout(context.Background(), node.RealClock(), 200*time.Millisecond)
			defer cancel()

			deps := runtimeDeps{
				signalContext: func(p context.Context) (context.Context, context.CancelFunc) {
					return context.WithCancel(p)
				},
				buildAdapters: func(_ node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error) {
					return world, world, world, nil, nil
				},
				clock: node.RealClock(),
				disk:  disk,
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
		synctest.Test(t, func(t *testing.T) {
			disk := sim.NewMemDisk()
			const cfgPath = "/sim/broken.lkal"
			writeMemFile(t, disk, cfgPath, `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24", gateway = "10.88.0.1" } }
unit {
  id = "broken-1"
  image = "oci.local/broken:v1"
}
`)
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
				disk:  disk,
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

	t.Run("netpol_applied_before_unit_start_and_tenant_bridge_selected", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			disk := sim.NewMemDisk()
			const cfgPath = "/sim/tenant-unit.lkal"
			const policyPath = "/sim/netpol.lkal"
			writeMemFile(t, disk, policyPath, `@lkal "0.1"
tenants "alpha" {
  cidrs = ["10.40.0.0/24"]
}
tenants "beta" {
  cidrs = ["10.41.0.0/24"]
}
`)
			writeMemFile(t, disk, cfgPath, `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.40.0.0/24", gateway = "10.40.0.1" } }
unit {
  id = "alpha-1"
  tenant = "alpha"
  image = "oci.local/app:v1"
  restart = .never
}
netpol {
  policyFile = "/sim/netpol.lkal"
}
`)
			f := nodesim.DefaultFaults()
			f.TaskBehavior = func(_ int, _ node.ContainerSpec) nodesim.TaskScript {
				return nodesim.TaskScript{RunDuration: 10 * time.Millisecond, ExitCode: 0}
			}
			world := nodesim.NewWorld(node.RealClock(), 2, f)
			fakeNft := &netpol.FakeNftRunner{}
			var seenBridge string
			var appliedBeforeAdapters bool

			deps := runtimeDeps{
				signalContext: func(p context.Context) (context.Context, context.CancelFunc) {
					return context.WithCancel(p)
				},
				buildNetpol: func(nc node.NetpolConfig) (netpol.NftRunner, error) {
					if nc.NftBin != netpol.DefaultNftBin {
						t.Errorf("nftBin = %q, want %q", nc.NftBin, netpol.DefaultNftBin)
					}
					return fakeNft, nil
				},
				buildAdapters: func(cfg node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error) {
					seenBridge = cfg.CNI.Bridge
					appliedBeforeAdapters = fakeNft.CallCount() == 1
					return world, world, world, nil, nil
				},
				clock:  node.RealClock(),
				disk:   disk,
				sysctl: sim.NewMemDisk(),
			}

			var stdout, stderr bytes.Buffer
			code := runMain(context.Background(), []string{"-config", cfgPath}, &stdout, &stderr, deps)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr=%s)", code, stderr.String())
			}
			if !appliedBeforeAdapters {
				t.Fatal("expected netpol to be applied before building container adapters")
			}
			if want := netpol.TenantBridgeName("alpha"); seenBridge != want {
				t.Fatalf("CNI bridge = %q, want %q", seenBridge, want)
			}
			if !strings.Contains(fakeNft.LastScript(), "ip saddr 10.40.0.0/24 jump tenant_alpha") {
				t.Fatalf("unexpected nft script:\n%s", fakeNft.LastScript())
			}
			if err := world.AssertClean(); err != nil {
				t.Fatal(err)
			}
		})
	})

	t.Run("netpol_apply_failure_fails_closed_before_container_start", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			disk := sim.NewMemDisk()
			const cfgPath = "/sim/tenant-unit.lkal"
			const policyPath = "/sim/netpol.lkal"
			writeMemFile(t, disk, policyPath, `@lkal "0.1"
tenants "alpha" {
  cidrs = ["10.40.0.0/24"]
}
`)
			writeMemFile(t, disk, cfgPath, `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.40.0.0/24", gateway = "10.40.0.1" } }
unit {
  id = "alpha-1"
  tenant = "alpha"
  image = "oci.local/app:v1"
}
netpol {
  policyFile = "/sim/netpol.lkal"
}
`)
			adaptersCalled := false
			deps := runtimeDeps{
				signalContext: func(p context.Context) (context.Context, context.CancelFunc) {
					return context.WithCancel(p)
				},
				buildNetpol: func(_ node.NetpolConfig) (netpol.NftRunner, error) {
					return &netpol.FakeNftRunner{Err: errors.New("nft unavailable")}, nil
				},
				buildAdapters: func(_ node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error) {
					adaptersCalled = true
					return nil, nil, nil, nil, nil
				},
				clock:  node.RealClock(),
				disk:   disk,
				sysctl: sim.NewMemDisk(),
			}

			var stdout, stderr bytes.Buffer
			code := runMain(context.Background(), []string{"-config", cfgPath}, &stdout, &stderr, deps)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if adaptersCalled {
				t.Fatal("container adapters must not be constructed when netpol apply fails")
			}
			if !strings.Contains(stderr.String(), "nft unavailable") {
				t.Fatalf("stderr missing netpol error: %s", stderr.String())
			}
		})
	})

	t.Run("netpol_refused_when_bridge_netfilter_is_enabled", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			disk := sim.NewMemDisk()
			writeMemFile(t, disk, "/sim/netpol.lkal", `@lkal "0.1"
tenants "alpha" {
  cidrs = ["10.40.0.0/24"]
}
`)
			writeMemFile(t, disk, "/sim/cfg.lkal", `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.40.0.0/24", gateway = "10.40.0.1" } }
unit {
  id = "alpha-1"
  tenant = "alpha"
  image = "oci.local/app:v1"
}
netpol {
  policyFile = "/sim/netpol.lkal"
}
`)
			sysctl := sim.NewMemDisk()
			writeMemFile(t, sysctl, netpol.BridgeNetfilterSysctls[0], "1\n")
			fakeNft := &netpol.FakeNftRunner{}
			adaptersCalled := false
			deps := runtimeDeps{
				signalContext: func(p context.Context) (context.Context, context.CancelFunc) {
					return context.WithCancel(p)
				},
				buildNetpol: func(_ node.NetpolConfig) (netpol.NftRunner, error) { return fakeNft, nil },
				buildAdapters: func(_ node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error) {
					adaptersCalled = true
					return nil, nil, nil, nil, nil
				},
				clock:  node.RealClock(),
				disk:   disk,
				sysctl: sysctl,
			}
			var stdout, stderr bytes.Buffer
			code := runMain(context.Background(), []string{"-config", "/sim/cfg.lkal"}, &stdout, &stderr, deps)
			if code != 1 || fakeNft.CallCount() != 0 || adaptersCalled {
				t.Fatalf("code=%d nftCalls=%d adaptersCalled=%v, want fail-closed", code, fakeNft.CallCount(), adaptersCalled)
			}
			if !strings.Contains(stderr.String(), "br_netfilter") {
				t.Fatalf("unexpected stderr: %s", stderr.String())
			}
		})
	})

	t.Run("netpol_rejects_unit_subnet_outside_tenant_cidr", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			disk := sim.NewMemDisk()
			const cfgPath = "/sim/mismatch.lkal"
			const policyPath = "/sim/netpol.lkal"
			writeMemFile(t, disk, policyPath, `@lkal "0.1"
tenants "alpha" {
  cidrs = ["10.40.0.0/24"]
}
`)
			writeMemFile(t, disk, cfgPath, `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.99.0.0/24", gateway = "10.99.0.1" } }
unit {
  id = "alpha-1"
  tenant = "alpha"
  image = "oci.local/app:v1"
}
netpol {
  policyFile = "/sim/netpol.lkal"
}
`)
			fakeNft := &netpol.FakeNftRunner{}
			adaptersCalled := false
			deps := runtimeDeps{
				signalContext: func(p context.Context) (context.Context, context.CancelFunc) {
					return context.WithCancel(p)
				},
				buildNetpol: func(_ node.NetpolConfig) (netpol.NftRunner, error) {
					return fakeNft, nil
				},
				buildAdapters: func(_ node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error) {
					adaptersCalled = true
					return nil, nil, nil, nil, nil
				},
				clock:  node.RealClock(),
				disk:   disk,
				sysctl: sim.NewMemDisk(),
			}

			var stdout, stderr bytes.Buffer
			code := runMain(context.Background(), []string{"-config", cfgPath}, &stdout, &stderr, deps)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if fakeNft.CallCount() != 0 || adaptersCalled {
				t.Fatalf("expected fail-closed before nft/adapters, got nftCalls=%d adaptersCalled=%v", fakeNft.CallCount(), adaptersCalled)
			}
			if !strings.Contains(stderr.String(), "outside tenant") {
				t.Fatalf("unexpected stderr: %s", stderr.String())
			}
		})
	})
	t.Run("untenanted_unit_subnet_overlapping_tenant_cidr_is_rejected", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			disk := sim.NewMemDisk()
			writeMemFile(t, disk, "/sim/netpol.lkal", `@lkal "0.1"
tenants "alpha" {
  cidrs = ["10.40.0.0/24"]
}
`)
			writeMemFile(t, disk, "/sim/untenanted.lkal", `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.40.0.0/25", gateway = "10.40.0.1" } }
unit {
  id = "plain-1"
  image = "oci.local/app:v1"
}
netpol {
  policyFile = "/sim/netpol.lkal"
}
`)
			fakeNft := &netpol.FakeNftRunner{}
			adaptersCalled := false
			deps := runtimeDeps{
				signalContext: func(p context.Context) (context.Context, context.CancelFunc) {
					return context.WithCancel(p)
				},
				buildNetpol: func(_ node.NetpolConfig) (netpol.NftRunner, error) {
					return fakeNft, nil
				},
				buildAdapters: func(_ node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error) {
					adaptersCalled = true
					return nil, nil, nil, nil, nil
				},
				clock:  node.RealClock(),
				disk:   disk,
				sysctl: sim.NewMemDisk(),
			}
			var stdout, stderr bytes.Buffer
			code := runMain(context.Background(), []string{"-config", "/sim/untenanted.lkal"}, &stdout, &stderr, deps)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if fakeNft.CallCount() != 0 || adaptersCalled {
				t.Fatalf("expected fail-closed before nft/adapters, got nftCalls=%d adaptersCalled=%v", fakeNft.CallCount(), adaptersCalled)
			}
			if !strings.Contains(stderr.String(), "overlaps tenant") {
				t.Fatalf("unexpected stderr: %s", stderr.String())
			}
		})
	})

	t.Run("untenanted_unit_with_disjoint_subnet_runs", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			disk := sim.NewMemDisk()
			writeMemFile(t, disk, "/sim/netpol.lkal", `@lkal "0.1"
tenants "alpha" {
  cidrs = ["10.40.0.0/24"]
}
`)
			writeMemFile(t, disk, "/sim/untenanted.lkal", `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.99.0.0/24", gateway = "10.99.0.1" } }
unit {
  id = "plain-1"
  image = "oci.local/app:v1"
  restart = .never
}
netpol {
  policyFile = "/sim/netpol.lkal"
}
`)
			f := nodesim.DefaultFaults()
			f.TaskBehavior = func(_ int, _ node.ContainerSpec) nodesim.TaskScript {
				return nodesim.TaskScript{RunDuration: 10 * time.Millisecond, ExitCode: 0}
			}
			world := nodesim.NewWorld(node.RealClock(), 2, f)
			deps := runtimeDeps{
				signalContext: func(p context.Context) (context.Context, context.CancelFunc) {
					return context.WithCancel(p)
				},
				buildNetpol: func(_ node.NetpolConfig) (netpol.NftRunner, error) {
					return &netpol.FakeNftRunner{}, nil
				},
				buildAdapters: func(_ node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error) {
					return world, world, world, nil, nil
				},
				clock:  node.RealClock(),
				disk:   disk,
				sysctl: sim.NewMemDisk(),
			}
			var stdout, stderr bytes.Buffer
			code := runMain(context.Background(), []string{"-config", "/sim/untenanted.lkal"}, &stdout, &stderr, deps)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0 (stderr=%s)", code, stderr.String())
			}
		})
	})

	t.Run("tenant_without_netpol_is_rejected_before_any_adapter", func(t *testing.T) {
		disk := sim.NewMemDisk()
		writeMemFile(t, disk, "/sim/nonp.lkal", `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.40.0.0/24", gateway = "10.40.0.1" } }
unit {
  id = "alpha-1"
  tenant = "alpha"
  image = "oci.local/app:v1"
}
`)
		adaptersCalled := false
		netpolBuilt := false
		deps := runtimeDeps{
			signalContext: func(p context.Context) (context.Context, context.CancelFunc) {
				return context.WithCancel(p)
			},
			buildNetpol: func(_ node.NetpolConfig) (netpol.NftRunner, error) {
				netpolBuilt = true
				return &netpol.FakeNftRunner{}, nil
			},
			buildAdapters: func(_ node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error) {
				adaptersCalled = true
				return nil, nil, nil, nil, nil
			},
			clock:  node.RealClock(),
			disk:   disk,
			sysctl: sim.NewMemDisk(),
		}
		var stdout, stderr bytes.Buffer
		code := runMain(context.Background(), []string{"-config", "/sim/nonp.lkal"}, &stdout, &stderr, deps)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if adaptersCalled || netpolBuilt {
			t.Fatalf("adaptersCalled=%v netpolBuilt=%v, want neither", adaptersCalled, netpolBuilt)
		}
		if !strings.Contains(stderr.String(), "requires a netpol block") {
			t.Fatalf("unexpected stderr: %s", stderr.String())
		}
	})
}
