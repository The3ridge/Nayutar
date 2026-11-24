package containerd

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/node"
	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/opencontainers/runtime-spec/specs-go"
)

func TestConfigDefaults(t *testing.T) {
	cfg := (Config{}).withDefaults()
	if cfg.Address != DefaultAddress ||
		cfg.Namespace != DefaultNamespace ||
		cfg.Snapshotter != DefaultSnapshotter ||
		cfg.RuntimeName != DefaultRuntimeName ||
		cfg.CleanupTimeout <= 0 ||
		cfg.IOCreator == nil {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestBuildSpecOptsAppliesArgsEnvMountsAndReplacesNetns(t *testing.T) {
	spec := node.ContainerSpec{
		ID:         "u1",
		Image:      "oci.local/app:v1",
		Command:    []string{"/bin/probe"},
		Args:       []string{"--once"},
		Env:        []string{"A=1", "B=2"},
		WorkingDir: "/work",
		NetnsPath:  "/run/netns/u1",
		Mounts: []node.Mount{
			{Source: "/host/data", Destination: "/data", Options: []string{"rbind", "ro"}},
		},
	}

	opts := BuildSpecOpts(nil, spec)
	// Skip opts[0] (oci.WithImageConfig(nil)) and apply the remaining spec options to a base Linux spec.
	var ociSpec oci.Spec
	ociSpec.Process = &specs.Process{}
	ociSpec.Linux = &specs.Linux{
		Namespaces: []specs.LinuxNamespace{
			{Type: specs.PIDNamespace},
			{Type: specs.NetworkNamespace},
		},
	}
	for _, opt := range opts[1:] {
		if err := opt(context.Background(), nil, &containers.Container{ID: "u1"}, &ociSpec); err != nil {
			t.Fatalf("apply spec opt: %v", err)
		}
	}

	if !reflect.DeepEqual(ociSpec.Process.Args, []string{"/bin/probe", "--once"}) {
		t.Fatalf("args = %v", ociSpec.Process.Args)
	}
	if !slices.Contains(ociSpec.Process.Env, "A=1") || !slices.Contains(ociSpec.Process.Env, "B=2") {
		t.Fatalf("env = %v", ociSpec.Process.Env)
	}
	if ociSpec.Process.Cwd != "/work" {
		t.Fatalf("cwd = %q", ociSpec.Process.Cwd)
	}
	if len(ociSpec.Mounts) != 1 || ociSpec.Mounts[0].Type != "bind" || ociSpec.Mounts[0].Destination != "/data" {
		t.Fatalf("mounts = %+v", ociSpec.Mounts)
	}

	netCount := 0
	var netPath string
	for _, ns := range ociSpec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			netCount++
			netPath = ns.Path
		}
	}
	if netCount != 1 || netPath != "/run/netns/u1" {
		t.Fatalf("expected single replaced network namespace with /run/netns/u1, got count=%d path=%q", netCount, netPath)
	}

	// Compile-time check that *Adapter satisfies node.Runtime.
	var _ node.Runtime = NewWithClient((*client.Client)(nil), Config{})
}
