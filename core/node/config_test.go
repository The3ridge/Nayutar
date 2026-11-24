package node_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/netpol"
	"github.com/Kaikei-e/Nayutar/core/node"
	"github.com/Kaikei-e/Nayutar/core/node/lifecycle"
)

func TestDecodeConfigDefaultsAndComplete(t *testing.T) {
	src := `
@lkal "0.1"

containerd {}

cni {
  ranges "primary" {
    subnet = "10.88.0.0/16"
    gateway = "10.88.0.1"
  }
}

unit {
  id = "web-1"
  image = "oci.local/web:v1"
  command = ["/bin/web"]
  args = ["--port", "8080"]
  env {
    Z_KEY = "last"
    A_KEY = "first"
  }
  mounts "data" {
    source = "/var/lib/data"
    destination = "/data"
    options = ["rbind", "ro"]
  }
}
`
	cfg, err := node.DecodeConfig("test.lkal", []byte(src))
	if err != nil {
		t.Fatalf("DecodeConfig failed: %v", err)
	}
	if cfg.Containerd.Socket != "/run/containerd/containerd.sock" {
		t.Fatalf("socket = %q", cfg.Containerd.Socket)
	}
	if cfg.Containerd.Namespace != "nayutar" || cfg.Containerd.Snapshotter != "overlayfs" {
		t.Fatalf("containerd defaults not applied: %+v", cfg.Containerd)
	}
	if cfg.CNI.BinDir != "/opt/cni/bin" || cfg.CNI.NetnsDir != "/run/netns" || cfg.CNI.Bridge != "nayutar0" {
		t.Fatalf("cni defaults not applied: %+v", cfg.CNI)
	}
	if cfg.Unit.Lifecycle.RestartPolicy != lifecycle.RestartOnFailure ||
		cfg.Unit.Lifecycle.StopGraceMs != 10000 ||
		cfg.Unit.Lifecycle.BackoffBaseMs != 1000 ||
		cfg.Unit.Lifecycle.BackoffMaxMs != 300000 ||
		cfg.Unit.Lifecycle.BackoffResetMs != 60000 ||
		cfg.Unit.Lifecycle.CNICount != 2 {
		t.Fatalf("lifecycle defaults not applied: %+v", cfg.Unit.Lifecycle)
	}
	wantEnv := []string{"A_KEY=first", "Z_KEY=last"}
	if !reflect.DeepEqual(cfg.Unit.Env, wantEnv) {
		t.Fatalf("env = %v, want %v", cfg.Unit.Env, wantEnv)
	}
	if len(cfg.Unit.Mounts) != 1 || cfg.Unit.Mounts[0].Type != "bind" {
		t.Fatalf("mount default type not applied: %+v", cfg.Unit.Mounts)
	}
}

func TestDecodeConfigRejectsInvalidInputs(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantErr string
	}{
		{
			name: "missing_ranges",
			src: `@lkal "0.1"
containerd {}
cni {}
unit { id = "u1", image = "img:1" }`,
			wantErr: "ranges",
		},
		{
			name: "gateway_outside_subnet",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24", gateway = "10.89.0.1" } }
unit { id = "u1", image = "img:1" }`,
			wantErr: "outside subnet",
		},
		{
			name: "backoff_max_less_than_base",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24" } }
unit { id = "u1", image = "img:1", backoffBaseMs = 5000, backoffMaxMs = 1000 }`,
			wantErr: "backoff max",
		},
		{
			name: "relative_mount_destination",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24" } }
unit {
  id = "u1"
  image = "img:1"
  mounts "m0" { source = "/tmp/a", destination = "relative/path" }
}`,
			wantErr: "clean absolute path",
		},
		{
			name: "root_mount_destination",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24" } }
unit {
  id = "u1"
  image = "img:1"
  mounts "m0" { source = "/tmp/a", destination = "/" }
}`,
			wantErr: "must not be root",
		},
		{
			name: "unclean_mount_destination",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24" } }
unit {
  id = "u1"
  image = "img:1"
  mounts "m0" { source = "/tmp/a", destination = "/data/../etc" }
}`,
			wantErr: "clean absolute path",
		},
		{
			name: "relative_bind_source",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24" } }
unit {
  id = "u1"
  image = "img:1"
  mounts "m0" { source = "rel/src", destination = "/data" }
}`,
			wantErr: "clean absolute path for bind mount",
		},
		{
			name: "unclean_bind_source",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24" } }
unit {
  id = "u1"
  image = "img:1"
  mounts "m0" { source = "/tmp/../etc", destination = "/data" }
}`,
			wantErr: "clean absolute path for bind mount",
		},
		{
			name: "relative_working_dir",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24" } }
unit {
  id = "u1"
  image = "img:1"
  workingDir = "app"
}`,
			wantErr: "workingDir",
		},
		{
			name: "unclean_working_dir",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24" } }
unit {
  id = "u1"
  image = "img:1"
  workingDir = "/app/../root"
}`,
			wantErr: "workingDir",
		},
		{
			name: "dotdot_unit_id",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24" } }
unit { id = "a..b", image = "img:1" }`,
			wantErr: "..",
		},
		{
			name: "root_netns_dir",
			src: `@lkal "0.1"
containerd {}
cni { netnsDir = "/", ranges "r0" { subnet = "10.88.0.0/24" } }
unit { id = "u1", image = "img:1" }`,
			wantErr: "non-root",
		},
		{
			name: "invalid_unit_tenant",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24" } }
unit { id = "u1", tenant = "Bad_Tenant", image = "img:1" }`,
			wantErr: "tenant",
		},
		{
			name: "relative_netpol_policy_file",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24" } }
unit { id = "u1", image = "img:1" }
netpol { policyFile = "rel/policy.lkal" }`,
			wantErr: "policyFile",
		},
		{
			name: "relative_netpol_nft_bin",
			src: `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.88.0.0/24" } }
unit { id = "u1", image = "img:1" }
netpol { policyFile = "/etc/nayutar/policy.lkal", nftBin = "nft" }`,
			wantErr: "nftBin",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := node.DecodeConfig(tc.name+".lkal", []byte(tc.src))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestDecodeConfigTenantBridgeAndNetpol(t *testing.T) {
	t.Run("tenant_selects_deterministic_bridge_when_bridge_omitted", func(t *testing.T) {
		src := `@lkal "0.1"
containerd {}
cni { ranges "r0" { subnet = "10.40.0.0/24", gateway = "10.40.0.1" } }
unit { id = "u1", tenant = "alpha", image = "img:1" }
netpol { policyFile = "/etc/nayutar/netpol.lkal" }
`
		cfg, err := node.DecodeConfig("tenant.lkal", []byte(src))
		if err != nil {
			t.Fatalf("DecodeConfig: %v", err)
		}
		if cfg.Unit.Tenant != "alpha" {
			t.Fatalf("tenant = %q, want alpha", cfg.Unit.Tenant)
		}
		if want := netpol.TenantBridgeName("alpha"); cfg.CNI.Bridge != want {
			t.Fatalf("bridge = %q, want %q", cfg.CNI.Bridge, want)
		}
		if cfg.Netpol == nil || cfg.Netpol.PolicyFile != "/etc/nayutar/netpol.lkal" || cfg.Netpol.NftBin != "/usr/sbin/nft" {
			t.Fatalf("unexpected netpol config: %+v", cfg.Netpol)
		}
	})

	cfgSrc := func(bridge, tenant, np string) string {
		b := ""
		if bridge != "" {
			b = `bridge = "` + bridge + `", `
		}
		tn := ""
		if tenant != "" {
			tn = `, tenant = "` + tenant + `"`
		}
		return `@lkal "0.1"
containerd {}
cni { ` + b + `ranges "r0" { subnet = "10.40.0.0/24", gateway = "10.40.0.1" } }
unit { id = "u1"` + tn + `, image = "img:1" }
` + np
	}
	const np = `netpol { policyFile = "/etc/nayutar/netpol.lkal" }`

	t.Run("explicit_bridge_equal_to_tenant_bridge_accepted", func(t *testing.T) {
		want := netpol.TenantBridgeName("alpha")
		cfg, err := node.DecodeConfig("t.lkal", []byte(cfgSrc(want, "alpha", np)))
		if err != nil {
			t.Fatalf("DecodeConfig: %v", err)
		}
		if cfg.CNI.Bridge != want {
			t.Fatalf("bridge = %q, want %q", cfg.CNI.Bridge, want)
		}
	})

	rejected := []struct {
		name, src, wantErr string
	}{
		{"tenant_without_netpol", cfgSrc("", "alpha", ""), "requires a netpol block"},
		{"tenant_without_netpol_explicit_bridge", cfgSrc(netpol.TenantBridgeName("alpha"), "alpha", ""), "requires a netpol block"},
		{"tenant_with_custom_bridge", cfgSrc("custom0", "alpha", np), "must equal the tenant bridge"},
		{"tenant_with_other_tenant_bridge", cfgSrc(netpol.TenantBridgeName("beta"), "alpha", np), "must equal the tenant bridge"},
		{"tenant_with_default_bridge", cfgSrc("cni0", "alpha", np), "must equal the tenant bridge"},
		{"untenanted_with_tenant_bridge", cfgSrc(netpol.TenantBridgeName("alpha"), "", np), "reserved"},
		{"untenanted_with_tenant_bridge_no_netpol", cfgSrc(netpol.TenantBridgePrefix+"x", "", ""), "reserved"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			_, err := node.DecodeConfig("t.lkal", []byte(tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}

	t.Run("untenanted_custom_and_default_bridge_accepted", func(t *testing.T) {
		for _, br := range []string{"", "custom0"} {
			cfg, err := node.DecodeConfig("t.lkal", []byte(cfgSrc(br, "", "")))
			if err != nil {
				t.Fatalf("bridge %q: %v", br, err)
			}
			if br != "" && cfg.CNI.Bridge != br {
				t.Fatalf("bridge = %q, want %q", cfg.CNI.Bridge, br)
			}
			if netpol.IsTenantBridgeName(cfg.CNI.Bridge) {
				t.Fatalf("default bridge %q is in the tenant namespace", cfg.CNI.Bridge)
			}
		}
	})
}
