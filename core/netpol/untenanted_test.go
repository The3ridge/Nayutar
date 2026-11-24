package netpol_test

import (
	"strings"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/netpol"
)

func mustCIDRs(t *testing.T, ss ...string) []netpol.Prefix {
	t.Helper()
	out := make([]netpol.Prefix, len(ss))
	for i, s := range ss {
		p, err := netpol.ParsePrefix(s)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = p
	}
	return out
}

func TestUntenantedSubnets(t *testing.T) {
	cp := &netpol.CompiledPolicy{Tenants: []netpol.TenantPolicy{
		{Tenant: "alpha", CIDRs: mustCIDRs(t, "10.40.0.0/24", "fd00:40::/64")},
		{Tenant: "beta", CIDRs: mustCIDRs(t, "10.41.0.0/24")},
	}}
	cases := []struct {
		name    string
		subnets []string
		wantErr string
	}{
		{"disjoint", []string{"10.99.0.0/24", "fd00:99::/64"}, ""},
		{"none", nil, ""},
		{"equal_to_tenant_cidr", []string{"10.40.0.0/24"}, "overlaps tenant \"alpha\""},
		{"inside_tenant_cidr", []string{"10.41.0.128/25"}, "overlaps tenant \"beta\""},
		{"contains_tenant_cidr", []string{"10.40.0.0/16"}, "overlaps tenant \"alpha\""},
		{"ipv6_overlap", []string{"fd00:40::/96"}, "overlaps tenant \"alpha\""},
		{"second_subnet_overlaps", []string{"10.99.0.0/24", "10.41.0.0/24"}, "overlaps"},
		{"invalid", []string{"nope"}, "invalid unit subnet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := cp.ValidateUntenantedSubnets(tc.subnets)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
	var nilCP *netpol.CompiledPolicy
	if err := nilCP.ValidateUntenantedSubnets(nil); err == nil {
		t.Fatal("nil policy must be rejected")
	}
}

func TestUntenantedBridgeNamespace(t *testing.T) {
	if !netpol.IsTenantBridgeName(netpol.TenantBridgeName("alpha")) {
		t.Fatal("TenantBridgeName must lie in the reserved namespace")
	}
	for _, name := range []string{"cni0", "br-nbr-x", ""} {
		if netpol.IsTenantBridgeName(name) {
			t.Fatalf("%q must not be reserved", name)
		}
	}
}
