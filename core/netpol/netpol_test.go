package netpol_test

import (
	"context"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/netpol"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/sim"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
)

func mustPrefix(t *testing.T, s string) netpol.Prefix {
	t.Helper()
	p, err := netpol.ParsePrefix(s)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", s, err)
	}
	return p
}

func mustRender(t *testing.T, cp *netpol.CompiledPolicy) string {
	t.Helper()
	script, err := netpol.RenderNft(cp)
	if err != nil {
		t.Fatalf("RenderNft: %v", err)
	}
	return script
}

func v4Addr(a, b, c, d byte) (uint64, uint64) {
	return 0, uint64(binary.BigEndian.Uint32([]byte{a, b, c, d}))
}

func v6Addr(t *testing.T, s string) (uint64, uint64) {
	t.Helper()
	addr := netip.MustParseAddr(s).As16()
	return binary.BigEndian.Uint64(addr[:8]), binary.BigEndian.Uint64(addr[8:])
}

func TestPrefixValidityBoundaryWidthsAndOverlap(t *testing.T) {
	validStrs := []string{
		"0.0.0.0/0",
		"128.0.0.0/1",
		"10.40.0.0/24",
		"10.40.0.2/31",
		"10.40.0.5/32",
		"::/0",
		"8000::/1",
		"fd00::/63",
		"fd00:1::/64",
		"fd00:1:0:0:8000::/65",
		"fd00:1::2/127",
		"fd00:1::3/128",
	}
	for _, s := range validStrs {
		p := mustPrefix(t, s)
		if !p.Valid() {
			t.Fatalf("%s reported invalid: %+v", s, p)
		}
		if got := p.String(); got != s {
			t.Fatalf("String(%s) = %q", s, got)
		}
		if !p.Contains(p.Family, p.Hi, p.Lo) {
			t.Fatalf("%s does not contain its own network address", s)
		}
		if !netpol.PrefixesOverlap(p, p) {
			t.Fatalf("%s does not overlap itself", s)
		}
	}

	invalidPrefixes := []netpol.Prefix{
		{Family: "", Hi: 0, Lo: 0, Bits: 0},
		{Family: netpol.FamilyIPv4, Hi: 1, Lo: 0, Bits: 24},
		{Family: netpol.FamilyIPv4, Hi: 0, Lo: 1 << 32, Bits: 32},
		{Family: netpol.FamilyIPv4, Hi: 0, Lo: 0, Bits: -1},
		{Family: netpol.FamilyIPv4, Hi: 0, Lo: 0, Bits: 33},
		{Family: netpol.FamilyIPv4, Hi: 0, Lo: 1, Bits: 0},
		{Family: netpol.FamilyIPv4, Hi: 0, Lo: 0x0a280001, Bits: 24},
		{Family: netpol.FamilyIPv6, Hi: 0, Lo: 0, Bits: -1},
		{Family: netpol.FamilyIPv6, Hi: 0, Lo: 0, Bits: 129},
		{Family: netpol.FamilyIPv6, Hi: 0, Lo: 1, Bits: 0},
		{Family: netpol.FamilyIPv6, Hi: 0, Lo: 1, Bits: 64},
		{Family: netpol.FamilyIPv6, Hi: 1, Lo: 0, Bits: 63},
		{Family: netpol.FamilyIPv6, Hi: 0, Lo: 1, Bits: 127},
	}
	for i, p := range invalidPrefixes {
		if p.Valid() {
			t.Fatalf("case %d (%+v): Valid() = true, want false", i, p)
		}
		if p.String() != "" {
			t.Fatalf("case %d (%+v): String() = %q, want empty", i, p, p.String())
		}
	}

	if _, err := netpol.ParsePrefix("10.40.0.1/24"); err == nil {
		t.Fatal("expected non-canonical IPv4 CIDR to be rejected")
	}
	if _, err := netpol.ParsePrefix("fd00::1/64"); err == nil {
		t.Fatal("expected non-canonical IPv6 CIDR to be rejected")
	}
	if _, err := netpol.PrefixFromNetip(netip.Prefix{}); err == nil {
		t.Fatal("expected invalid netip.Prefix to be rejected")
	}

	a := mustPrefix(t, "10.40.0.0/24")
	b := mustPrefix(t, "10.40.0.128/25")
	c := mustPrefix(t, "10.41.0.0/24")
	v6 := mustPrefix(t, "fd00:40::/64")
	if !netpol.PrefixesOverlap(a, b) || !netpol.PrefixesOverlap(b, a) {
		t.Fatal("expected 10.40.0.0/24 and 10.40.0.128/25 to overlap")
	}
	if netpol.PrefixesOverlap(a, c) {
		t.Fatal("expected 10.40.0.0/24 and 10.41.0.0/24 to be disjoint")
	}
	if netpol.PrefixesOverlap(a, v6) {
		t.Fatal("expected IPv4 and IPv6 prefixes to be disjoint")
	}
	// Malformed IPv4 packet addresses with non-zero hi or lo >= 2^32 must never match.
	zero4 := mustPrefix(t, "0.0.0.0/0")
	if zero4.Contains(netpol.FamilyIPv4, 1, 0) || zero4.Contains(netpol.FamilyIPv4, 0, 1<<32) {
		t.Fatal("0.0.0.0/0 must reject out-of-range IPv4 address values")
	}
}

func TestCompileAllErrorCodesAndPrecedence(t *testing.T) {
	pAlpha := mustPrefix(t, "10.40.0.0/24")
	pBeta := mustPrefix(t, "10.41.0.0/24")
	pOverlap := mustPrefix(t, "10.40.0.0/25")

	cases := []struct {
		name   string
		bundle netpol.PolicyBundle
		want   netpol.CompileErrorCode
	}{
		{
			name: "invalid_tenant_name_underscore",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{{Tenant: "bad_name", CIDRs: []netpol.Prefix{pAlpha}}},
			},
			want: netpol.ErrInvalidTenantName,
		},
		{
			name: "invalid_tenant_name_uppercase",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{{Tenant: "Alpha", CIDRs: []netpol.Prefix{pAlpha}}},
			},
			want: netpol.ErrInvalidTenantName,
		},
		{
			name: "duplicate_tenant",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{
					{Tenant: "alpha", CIDRs: []netpol.Prefix{pAlpha}},
					{Tenant: "alpha", CIDRs: []netpol.Prefix{pBeta}},
				},
			},
			want: netpol.ErrDuplicateTenant,
		},
		{
			name: "empty_tenant_cidrs",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{{Tenant: "alpha"}},
			},
			want: netpol.ErrEmptyTenantCIDRs,
		},
		{
			name: "invalid_tenant_cidr",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{
					{Tenant: "alpha", CIDRs: []netpol.Prefix{{Family: netpol.FamilyIPv4, Hi: 0, Lo: 1, Bits: 24}}},
				},
			},
			want: netpol.ErrInvalidCIDR,
		},
		{
			name: "duplicate_tenant_cidr_overlap",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{
					{Tenant: "alpha", CIDRs: []netpol.Prefix{pAlpha, pOverlap}},
				},
			},
			want: netpol.ErrDuplicateTenantCIDR,
		},
		{
			name: "overlapping_tenant_cidrs",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{
					{Tenant: "alpha", CIDRs: []netpol.Prefix{pAlpha}},
					{Tenant: "beta", CIDRs: []netpol.Prefix{pOverlap}},
				},
			},
			want: netpol.ErrOverlappingTenantCIDRs,
		},
		{
			name: "duplicate_rule_order",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{
					{
						Tenant: "alpha",
						CIDRs:  []netpol.Prefix{pAlpha},
						Rules: []netpol.Rule{
							{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: pBeta, Protocol: netpol.RuleProtoAny},
							{Order: 10, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: pBeta, Protocol: netpol.RuleProtoAny},
						},
					},
				},
			},
			want: netpol.ErrDuplicateRuleOrder,
		},
		{
			name: "unknown_peer_tenant",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{
					{
						Tenant: "alpha",
						CIDRs:  []netpol.Prefix{pAlpha},
						Rules: []netpol.Rule{
							{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "ghost", Protocol: netpol.RuleProtoTCP},
						},
					},
				},
			},
			want: netpol.ErrUnknownPeerTenant,
		},
		{
			name: "invalid_rule_peer_cidr",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{
					{
						Tenant: "alpha",
						CIDRs:  []netpol.Prefix{pAlpha},
						Rules: []netpol.Rule{
							{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4, Lo: 1, Bits: 24}, Protocol: netpol.RuleProtoTCP},
						},
					},
				},
			},
			want: netpol.ErrInvalidCIDR,
		},
		{
			name: "invalid_port_range_on_icmp",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{
					{
						Tenant: "alpha",
						CIDRs:  []netpol.Prefix{pAlpha},
						Rules: []netpol.Rule{
							{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: pBeta, Protocol: netpol.RuleProtoICMP, HasPorts: true, PortMin: 80, PortMax: 80},
						},
					},
				},
			},
			want: netpol.ErrInvalidPortRange,
		},
		{
			name: "invalid_port_range_inverted",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{
					{
						Tenant: "alpha",
						CIDRs:  []netpol.Prefix{pAlpha},
						Rules: []netpol.Rule{
							{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: pBeta, Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 443, PortMax: 80},
						},
					},
				},
			},
			want: netpol.ErrInvalidPortRange,
		},
		{
			name: "pass4_sorted_rule_order_precedence",
			bundle: netpol.PolicyBundle{
				Tenants: []netpol.TenantPolicy{
					{
						Tenant: "alpha",
						CIDRs:  []netpol.Prefix{pAlpha},
						Rules: []netpol.Rule{
							{Order: 20, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "missing", Protocol: netpol.RuleProtoTCP},
							{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: pBeta, Protocol: netpol.RuleProtoAny, HasPorts: true, PortMin: 80, PortMax: 80},
						},
					},
				},
			},
			want: netpol.ErrInvalidPortRange,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cp, cerr := netpol.Compile(tc.bundle)
			if cerr == nil || cp != nil {
				t.Fatalf("Compile succeeded (%+v), want error %q", cp, tc.want)
			}
			if cerr.Code != tc.want {
				t.Fatalf("Compile error code = %q (%v), want %q", cerr.Code, cerr, tc.want)
			}
		})
	}
}

func TestEvaluateAndEvalNftSemantics(t *testing.T) {
	pAlpha := mustPrefix(t, "10.40.0.0/24")
	pBeta := mustPrefix(t, "10.41.0.0/24")
	pGamma := mustPrefix(t, "10.42.0.0/24")
	pGammaV6 := mustPrefix(t, "fd00:42::/64")
	pExt := mustPrefix(t, "198.51.100.0/24")
	pAnyV4 := mustPrefix(t, "0.0.0.0/0")

	bundle := netpol.PolicyBundle{
		Tenants: []netpol.TenantPolicy{
			{
				Tenant:      "alpha",
				IntraTenant: "", // defaults to allow
				CIDRs:       []netpol.Prefix{pAlpha},
				Rules: []netpol.Rule{
					// Unilateral 0.0.0.0/0 egress must NOT bypass beta's default-deny ingress,
					// but DOES allow egress to external endpoints.
					{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: pAnyV4, Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 443, PortMax: 443},
					{Order: 20, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "beta", Protocol: netpol.RuleProtoUDP, HasPorts: true, PortMin: 5300, PortMax: 5305},
					{Order: 30, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: pExt, Protocol: netpol.RuleProtoICMP},
				},
			},
			{
				Tenant:      "beta",
				IntraTenant: netpol.IntraTenantDeny,
				CIDRs:       []netpol.Prefix{pBeta},
				Rules: []netpol.Rule{
					{Order: 5, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindTenant, PeerTenant: "alpha", Protocol: netpol.RuleProtoUDP, HasPorts: true, PortMin: 5300, PortMax: 5302},
					{Order: 15, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "beta", Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 8080, PortMax: 8080},
					{Order: 25, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindTenant, PeerTenant: "beta", Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 8080, PortMax: 8080},
				},
			},
			{
				Tenant:      "gamma",
				IntraTenant: netpol.IntraTenantAllow,
				CIDRs:       []netpol.Prefix{pGammaV6, pGamma},
			},
		},
	}

	cp, cerr := netpol.Compile(bundle)
	if cerr != nil {
		t.Fatalf("Compile: %v", cerr)
	}
	if cp.Tenants[0].IntraTenant != netpol.IntraTenantAllow {
		t.Fatalf("empty IntraTenant did not default to allow: %q", cp.Tenants[0].IntraTenant)
	}
	// Verify canonical CIDR sorting: IPv4 before IPv6.
	if cp.Tenants[2].CIDRs[0].Family != netpol.FamilyIPv4 || cp.Tenants[2].CIDRs[1].Family != netpol.FamilyIPv6 {
		t.Fatalf("CIDRs not canonically sorted: %+v", cp.Tenants[2].CIDRs)
	}

	tbl := netpol.CompileNft(cp)
	if tbl.ChainCount() != 1+2*3 {
		t.Fatalf("ChainCount = %d, want 7", tbl.ChainCount())
	}
	if tbl.ForwardRuleCount() != 2+3*4 {
		t.Fatalf("ForwardRuleCount = %d, want 14", tbl.ForwardRuleCount())
	}

	a1Hi, a1Lo := v4Addr(10, 40, 0, 2)
	a2Hi, a2Lo := v4Addr(10, 40, 0, 3)
	b1Hi, b1Lo := v4Addr(10, 41, 0, 2)
	b2Hi, b2Lo := v4Addr(10, 41, 0, 3)
	g1Hi, g1Lo := v4Addr(10, 42, 0, 2)
	e1Hi, e1Lo := v4Addr(198, 51, 100, 9)
	e2Hi, e2Lo := v4Addr(203, 0, 113, 7)
	g6aHi, g6aLo := v6Addr(t, "fd00:42::1")
	g6bHi, g6bLo := v6Addr(t, "fd00:42::2")

	checks := []struct {
		name string
		pkt  netpol.Packet
		want netpol.Verdict
	}{
		{
			name: "invalid_always_dropped",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: a1Hi, SrcLo: a1Lo, DstHi: a2Hi, DstLo: a2Lo, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: netpol.ConnStateInvalid},
			want: netpol.VerdictDrop,
		},
		{
			name: "established_always_accepted",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: e1Hi, SrcLo: e1Lo, DstHi: e2Hi, DstLo: e2Lo, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: netpol.ConnStateEstablished},
			want: netpol.VerdictAccept,
		},
		{
			name: "related_always_accepted",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: b1Hi, SrcLo: b1Lo, DstHi: a1Hi, DstLo: a1Lo, Protocol: netpol.PacketProtoICMP, ConnState: netpol.ConnStateRelated},
			want: netpol.VerdictAccept,
		},
		{
			name: "intra_tenant_allow_ipv4",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: a1Hi, SrcLo: a1Lo, DstHi: a2Hi, DstLo: a2Lo, Protocol: netpol.PacketProtoOther, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictAccept,
		},
		{
			name: "intra_tenant_allow_ipv6",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv6, SrcHi: g6aHi, SrcLo: g6aLo, DstHi: g6bHi, DstLo: g6bLo, Protocol: netpol.PacketProtoTCP, DstPort: 9999, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictAccept,
		},
		{
			name: "intra_tenant_deny_blocks_unmatched",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: b1Hi, SrcLo: b1Lo, DstHi: b2Hi, DstLo: b2Lo, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictDrop,
		},
		{
			name: "intra_tenant_deny_allows_mutual_rule",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: b1Hi, SrcLo: b1Lo, DstHi: b2Hi, DstLo: b2Lo, Protocol: netpol.PacketProtoTCP, DstPort: 8080, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictAccept,
		},
		{
			name: "cross_tenant_unilateral_egress_0_0_0_0_dropped",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: a1Hi, SrcLo: a1Lo, DstHi: b1Hi, DstLo: b1Lo, Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictDrop,
		},
		{
			name: "cross_tenant_mutual_allow_port_in_intersection",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: a1Hi, SrcLo: a1Lo, DstHi: b1Hi, DstLo: b1Lo, Protocol: netpol.PacketProtoUDP, DstPort: 5301, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictAccept,
		},
		{
			name: "cross_tenant_port_outside_ingress_range_dropped",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: a1Hi, SrcLo: a1Lo, DstHi: b1Hi, DstLo: b1Lo, Protocol: netpol.PacketProtoUDP, DstPort: 5304, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictDrop,
		},
		{
			name: "cross_tenant_no_rules_dropped",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: g1Hi, SrcLo: g1Lo, DstHi: a1Hi, DstLo: a1Lo, Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictDrop,
		},
		{
			name: "tenant_to_external_allowed",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: a1Hi, SrcLo: a1Lo, DstHi: e2Hi, DstLo: e2Lo, Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictAccept,
		},
		{
			name: "tenant_to_external_wrong_port_dropped",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: a1Hi, SrcLo: a1Lo, DstHi: e2Hi, DstLo: e2Lo, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictDrop,
		},
		{
			name: "external_to_tenant_allowed_icmp",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: e1Hi, SrcLo: e1Lo, DstHi: a1Hi, DstLo: a1Lo, Protocol: netpol.PacketProtoICMP, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictAccept,
		},
		{
			name: "external_to_tenant_unmatched_dropped",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: e2Hi, SrcLo: e2Lo, DstHi: a1Hi, DstLo: a1Lo, Protocol: netpol.PacketProtoICMP, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictDrop,
		},
		{
			name: "external_to_external_transit_always_dropped",
			pkt:  netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: e1Hi, SrcLo: e1Lo, DstHi: e2Hi, DstLo: e2Lo, Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: netpol.ConnStateNew},
			want: netpol.VerdictDrop,
		},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			gotEval := netpol.Evaluate(cp, tc.pkt)
			gotNft := netpol.EvalNft(tbl, tc.pkt)
			if gotEval != tc.want {
				t.Fatalf("Evaluate = %q, want %q", gotEval, tc.want)
			}
			if gotNft != tc.want {
				t.Fatalf("EvalNft = %q, want %q", gotNft, tc.want)
			}
		})
	}
}

func TestRandomPoliciesEvaluateMatchesEvalNftAndCanonicalDeterminism(t *testing.T) {
	var seed [32]byte
	binary.LittleEndian.PutUint64(seed[:8], 20261124)
	rng := rand.New(rand.NewChaCha8(seed))

	v4Pool := []netpol.Prefix{
		mustPrefix(t, "10.10.0.0/24"),
		mustPrefix(t, "10.11.0.0/24"),
		mustPrefix(t, "10.12.0.0/24"),
		mustPrefix(t, "10.13.0.0/24"),
	}
	v6Pool := []netpol.Prefix{
		mustPrefix(t, "fd00:10::/64"),
		mustPrefix(t, "fd00:11::/64"),
		mustPrefix(t, "fd00:12::/64"),
		mustPrefix(t, "fd00:13::/64"),
	}
	extV4 := mustPrefix(t, "198.51.100.0/24")
	extV6 := mustPrefix(t, "2001:db8:99::/64")

	tenantNames := []string{"alpha", "beta", "gamma", "delta"}
	ruleProtos := []netpol.RuleProto{netpol.RuleProtoAny, netpol.RuleProtoTCP, netpol.RuleProtoUDP, netpol.RuleProtoICMP}
	pktProtos := []netpol.PacketProto{netpol.PacketProtoTCP, netpol.PacketProtoUDP, netpol.PacketProtoICMP, netpol.PacketProtoOther}
	connStates := []netpol.ConnState{netpol.ConnStateNew, netpol.ConnStateEstablished, netpol.ConnStateRelated, netpol.ConnStateInvalid}

	for trial := range 200 {
		nTenants := 1 + rng.IntN(4)
		tenants := make([]netpol.TenantPolicy, nTenants)
		for i := range nTenants {
			mode := netpol.IntraTenantAllow
			if rng.IntN(2) == 1 {
				mode = netpol.IntraTenantDeny
			}
			cidrs := []netpol.Prefix{v4Pool[i]}
			if rng.IntN(2) == 1 {
				cidrs = append(cidrs, v6Pool[i])
			}
			nRules := rng.IntN(6)
			rules := make([]netpol.Rule, 0, nRules)
			for r := range nRules {
				dir := netpol.DirectionEgress
				if rng.IntN(2) == 1 {
					dir = netpol.DirectionIngress
				}
				proto := ruleProtos[rng.IntN(len(ruleProtos))]
				rule := netpol.Rule{
					Order:     int64((nRules - r) * 10),
					Direction: dir,
					Protocol:  proto,
				}
				if rng.IntN(2) == 0 {
					rule.PeerKind = netpol.PeerKindTenant
					rule.PeerTenant = tenantNames[rng.IntN(nTenants)]
				} else {
					rule.PeerKind = netpol.PeerKindCIDR
					switch rng.IntN(4) {
					case 0:
						rule.PeerCIDR = extV4
					case 1:
						rule.PeerCIDR = extV6
					case 2:
						rule.PeerCIDR = mustPrefix(t, "0.0.0.0/0")
					default:
						rule.PeerCIDR = mustPrefix(t, "::/0")
					}
				}
				if (proto == netpol.RuleProtoTCP || proto == netpol.RuleProtoUDP) && rng.IntN(2) == 1 {
					pMin := 80 + rng.IntN(20)
					pMax := pMin + rng.IntN(5)
					rule.HasPorts = true
					rule.PortMin = pMin
					rule.PortMax = pMax
				}
				rules = append(rules, rule)
			}
			tenants[i] = netpol.TenantPolicy{
				Tenant:      tenantNames[i],
				IntraTenant: mode,
				CIDRs:       cidrs,
				Rules:       rules,
			}
		}

		cp1, cerr := netpol.Compile(netpol.PolicyBundle{Tenants: tenants})
		if cerr != nil {
			t.Fatalf("trial %d compile failed: %v", trial, cerr)
		}
		// Shuffled input order must compile to the exact same canonical policy and rendered script.
		shuffled := slices.Clone(tenants)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		cp2, cerr := netpol.Compile(netpol.PolicyBundle{Tenants: shuffled})
		if cerr != nil {
			t.Fatalf("trial %d shuffled compile failed: %v", trial, cerr)
		}
		if !reflect.DeepEqual(cp1, cp2) {
			t.Fatalf("trial %d shuffled compile differed", trial)
		}
		if mustRender(t, cp1) != mustRender(t, cp2) {
			t.Fatalf("trial %d rendered nft differed for shuffled input", trial)
		}

		tbl := netpol.CompileNft(cp1)
		for p := range 40 {
			var pkt netpol.Packet
			pkt.ConnState = connStates[rng.IntN(len(connStates))]
			pkt.Protocol = pktProtos[rng.IntN(len(pktProtos))]
			pkt.DstPort = 78 + rng.IntN(30)
			if rng.IntN(2) == 0 {
				pkt.Family = netpol.FamilyIPv4
				srcChoice := rng.IntN(nTenants + 1)
				dstChoice := rng.IntN(nTenants + 1)
				if srcChoice < nTenants {
					pkt.SrcLo = v4Pool[srcChoice].Lo + uint64(1+rng.IntN(5))
				} else {
					pkt.SrcLo = extV4.Lo + uint64(1+rng.IntN(5))
				}
				if dstChoice < nTenants {
					pkt.DstLo = v4Pool[dstChoice].Lo + uint64(1+rng.IntN(5))
				} else {
					pkt.DstLo = extV4.Lo + uint64(1+rng.IntN(5))
				}
			} else {
				pkt.Family = netpol.FamilyIPv6
				srcChoice := rng.IntN(nTenants + 1)
				dstChoice := rng.IntN(nTenants + 1)
				if srcChoice < nTenants {
					pkt.SrcHi = v6Pool[srcChoice].Hi
					pkt.SrcLo = uint64(1 + rng.IntN(5))
				} else {
					pkt.SrcHi = extV6.Hi
					pkt.SrcLo = uint64(1 + rng.IntN(5))
				}
				if dstChoice < nTenants {
					pkt.DstHi = v6Pool[dstChoice].Hi
					pkt.DstLo = uint64(1 + rng.IntN(5))
				} else {
					pkt.DstHi = extV6.Hi
					pkt.DstLo = uint64(1 + rng.IntN(5))
				}
			}
			gotEval := netpol.Evaluate(cp1, pkt)
			gotNft := netpol.EvalNft(tbl, pkt)
			if gotEval != gotNft {
				t.Fatalf("trial %d pkt %d: Evaluate=%q != EvalNft=%q (pkt=%+v)", trial, p, gotEval, gotNft, pkt)
			}
		}
	}
}

func TestRenderNftSyntaxAndBridgeNames(t *testing.T) {
	bundle := netpol.PolicyBundle{
		Tenants: []netpol.TenantPolicy{
			{
				Tenant:      "alpha",
				IntraTenant: netpol.IntraTenantAllow,
				CIDRs:       []netpol.Prefix{mustPrefix(t, "10.40.0.0/24"), mustPrefix(t, "fd00:40::/64")},
				Rules: []netpol.Rule{
					{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "beta", Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 443, PortMax: 443},
					{Order: 20, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustPrefix(t, "198.51.100.0/24"), Protocol: netpol.RuleProtoUDP, HasPorts: true, PortMin: 8000, PortMax: 8080},
					{Order: 30, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustPrefix(t, "fd00:99::/64"), Protocol: netpol.RuleProtoICMP},
					{Order: 40, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustPrefix(t, "198.51.100.0/24"), Protocol: netpol.RuleProtoICMP},
					{Order: 50, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustPrefix(t, "203.0.113.0/24"), Protocol: netpol.RuleProtoTCP},
					{Order: 60, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustPrefix(t, "203.0.113.0/24"), Protocol: netpol.RuleProtoAny},
				},
			},
			{
				Tenant:      "beta",
				IntraTenant: netpol.IntraTenantDeny,
				CIDRs:       []netpol.Prefix{mustPrefix(t, "10.41.0.0/24")},
			},
		},
	}
	cp, cerr := netpol.Compile(bundle)
	if cerr != nil {
		t.Fatalf("Compile: %v", cerr)
	}
	script := mustRender(t, cp)
	if strings.Contains(script, "flush ruleset") {
		t.Fatalf("RenderNft must never emit flush ruleset:\n%s", script)
	}
	wantPrefix := "table inet nayutar\ndelete table inet nayutar\ntable bridge nayutar\ndelete table bridge nayutar\ntable inet nayutar {\n"
	if !strings.HasPrefix(script, wantPrefix) {
		t.Fatalf("RenderNft missing atomic header:\n%s", script)
	}
	for _, needle := range []string{
		"type filter hook forward priority 0; policy drop;",
		"ct state invalid drop",
		"ct state { established, related } accept",
		"ip saddr 10.40.0.0/24 jump tenant_alpha",
		"ip6 saddr fd00:40::/64 jump tenant_alpha",
		"ip daddr 10.41.0.0/24 jump tenant_beta_in",
		"ip saddr 10.40.0.0/24 accept",
		"ip6 saddr fd00:40::/64 accept",
		"ip daddr 10.41.0.0/24 tcp dport 443 return",
		"ip daddr 198.51.100.0/24 udp dport 8000-8080 return",
		"ip6 daddr fd00:99::/64 meta l4proto ipv6-icmp return",
		"ip saddr 198.51.100.0/24 meta l4proto icmp accept",
		"ip saddr 203.0.113.0/24 meta l4proto tcp accept",
		"ip saddr 203.0.113.0/24 accept",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("RenderNft missing %q in:\n%s", needle, script)
		}
	}
	if netpol.RenderTeardownNft() != "table inet nayutar\ndelete table inet nayutar\ntable bridge nayutar\ndelete table bridge nayutar\n" {
		t.Fatalf("unexpected teardown script: %q", netpol.RenderTeardownNft())
	}

	brAlpha := netpol.TenantBridgeName("alpha")
	brBeta := netpol.TenantBridgeName("beta")
	if len(brAlpha) != 15 || !strings.HasPrefix(brAlpha, "nbr-") {
		t.Fatalf("unexpected bridge name %q", brAlpha)
	}
	if brAlpha == brBeta {
		t.Fatalf("distinct tenants collided on bridge name %q", brAlpha)
	}
	if brAlpha != netpol.TenantBridgeName("alpha") {
		t.Fatal("TenantBridgeName is not deterministic")
	}
}

func TestDecodeBundleAndKindSchemaObjectsIntegration(t *testing.T) {
	if netpol.Schema() == nil {
		t.Fatal("Schema() = nil")
	}
	src := `@lkal "0.1"

tenants "beta" {
  intraTenant = .deny
  cidrs = ["10.41.0.0/24"]
  rules "allow-from-alpha" {
    order = 10
    direction = .ingress
    peerTenant = "alpha"
    protocol = .tcp
    ports {
      min = 8080
      max = 8080
    }
  }
}

tenants "alpha" {
  cidrs = ["10.40.0.0/24", "fd00:40::/64"]
  rules "allow-to-beta" {
    order = 20
    direction = .egress
    peerTenant = "beta"
    protocol = .tcp
    ports {
      min = 8080
      max = 8080
    }
  }
  rules "allow-dns" {
    order = 5
    direction = .egress
    peerCIDR = "198.51.100.0/24"
  }
}
`
	bundle, err := netpol.DecodeBundle("policy.lkal", []byte(src))
	if err != nil {
		t.Fatalf("DecodeBundle: %v", err)
	}
	cp, cerr := netpol.Compile(bundle)
	if cerr != nil {
		t.Fatalf("Compile(decoded): %v", cerr)
	}
	if len(cp.Tenants) != 2 || cp.Tenants[0].Tenant != "alpha" || cp.Tenants[0].IntraTenant != netpol.IntraTenantAllow {
		t.Fatalf("unexpected compiled bundle: %+v", cp.Tenants)
	}
	if len(cp.Tenants[0].Rules) != 2 || cp.Tenants[0].Rules[0].Order != 5 || cp.Tenants[0].Rules[0].Protocol != netpol.RuleProtoAny {
		t.Fatalf("unexpected alpha rules: %+v", cp.Tenants[0].Rules)
	}

	// Reject malformed bundles.
	badBundles := []string{
		`@lkal "0.1"
tenants "alpha" {
  cidrs = ["10.40.0.1/24"]
}`,
		`@lkal "0.1"
tenants "alpha" {
  cidrs = ["10.40.0.0/24"]
  rules "r1" {
    order = 10
    direction = .egress
  }
}`,
		`@lkal "0.1"
tenants "alpha" {
  cidrs = ["10.40.0.0/24"]
  rules "r1" {
    order = 10
    direction = .egress
    peerTenant = "alpha"
    peerCIDR = "10.40.0.0/24"
  }
}`,
		`@lkal "0.1"
tenants "alpha" {
  cidrs = ["10.40.0.0/24"]
  rules "r1" { order = 10, direction = .egress, peerTenant = "alpha" }
  rules "r2" { order = 10, direction = .ingress, peerTenant = "alpha" }
}`,
	}
	for i, bad := range badBundles {
		if _, err := netpol.DecodeBundle("bad.lkal", []byte(bad)); err == nil {
			t.Fatalf("bad bundle %d unexpectedly succeeded", i)
		}
	}

	// KindSchema integration with core/resource.Objects.
	ks, err := netpol.KindSchema()
	if err != nil {
		t.Fatalf("KindSchema: %v", err)
	}
	if len(ks.RefRules()) != 0 {
		t.Fatalf("networkpolicy KindSchema must not declare .ref rules for cross-tenant peers, got %+v", ks.RefRules())
	}

	st := memstore.New()
	objs := resource.NewObjects(st, resource.Config{
		Tenant: "alpha",
		Schemas: resource.Schemas{
			netpol.QualifiedResourceKind: ks,
		},
		Now:  sim.NewFakeClock(sim.DefaultEpoch).Now,
		Rand: sim.RandReader(sim.NewDetRand(42)),
	})
	principal := refs.Principal{Type: "user", ID: "admin"}

	cand := lkal.Record{Fields: []lkal.Field{
		{Key: "meta", Value: lkal.Record{Fields: []lkal.Field{
			{Key: "group", Value: lkal.String(netpol.ResourceGroup)},
			{Key: "kind", Value: lkal.String(netpol.ResourceKind)},
			{Key: "name", Value: lkal.String("default")},
		}}},
		{Key: "spec", Value: lkal.Record{Fields: []lkal.Field{
			{Key: "rules", Value: lkal.Record{Fields: []lkal.Field{
				{Key: "allow-ext", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "order", Value: lkal.Int(10)},
					{Key: "direction", Value: lkal.Enum("egress")},
					{Key: "peerCIDR", Value: lkal.String("198.51.100.0/24")},
				}}},
			}}},
		}}},
	}}
	created, err := objs.Create(context.Background(), principal, cand)
	if err != nil {
		t.Fatalf("Objects.Create(networkpolicy): %v", err)
	}
	var specRec lkal.Record
	for _, f := range created.Fields {
		if f.Key == "spec" {
			specRec = f.Value.(lkal.Record)
		}
	}
	tp, err := netpol.DecodeTenantPolicySpec("alpha", specRec, []netpol.Prefix{mustPrefix(t, "10.40.0.0/24")})
	if err != nil {
		t.Fatalf("DecodeTenantPolicySpec: %v", err)
	}
	if tp.IntraTenant != netpol.IntraTenantAllow || len(tp.Rules) != 1 || tp.Rules[0].Protocol != netpol.RuleProtoAny {
		t.Fatalf("defaults not completed via resource.Objects: %+v", tp)
	}
	if _, cerr := netpol.Compile(netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{tp}}); cerr != nil {
		t.Fatalf("Compile(from resource): %v", cerr)
	}

	// Duplicate rule order must be rejected by KindSchema ordered set enforcement.
	dupCand := lkal.Record{Fields: []lkal.Field{
		{Key: "meta", Value: lkal.Record{Fields: []lkal.Field{
			{Key: "group", Value: lkal.String(netpol.ResourceGroup)},
			{Key: "kind", Value: lkal.String(netpol.ResourceKind)},
			{Key: "name", Value: lkal.String("dup-order")},
		}}},
		{Key: "spec", Value: lkal.Record{Fields: []lkal.Field{
			{Key: "rules", Value: lkal.Record{Fields: []lkal.Field{
				{Key: "r1", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "order", Value: lkal.Int(10)},
					{Key: "direction", Value: lkal.Enum("egress")},
					{Key: "peerCIDR", Value: lkal.String("198.51.100.0/24")},
				}}},
				{Key: "r2", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "order", Value: lkal.Int(10)},
					{Key: "direction", Value: lkal.Enum("ingress")},
					{Key: "peerCIDR", Value: lkal.String("198.51.100.0/24")},
				}}},
			}}},
		}}},
	}}
	if _, err := objs.Create(context.Background(), principal, dupCand); !errors.Is(err, envelope.ErrMalformed) {
		t.Fatalf("Create(duplicate rule order) = %v, want envelope.ErrMalformed", err)
	}
}

func TestEnforcerAndExecNftRunnerValidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if _, err := netpol.NewEnforcer(nil, sim.NewMemDisk()); err == nil {
			t.Fatal("expected error for nil runner")
		}
		fake := &netpol.FakeNftRunner{}
		enf, err := netpol.NewEnforcer(fake, sim.NewMemDisk())
		if err != nil {
			t.Fatalf("NewEnforcer: %v", err)
		}

		// Invalid bundle must fail closed before invoking runner.
		bad := netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{{Tenant: "BAD"}}}
		if _, err := enf.Apply(context.Background(), bad); err == nil {
			t.Fatal("expected compile error")
		}
		if fake.CallCount() != 0 {
			t.Fatalf("runner invoked %d times on invalid bundle, want 0", fake.CallCount())
		}

		good := netpol.PolicyBundle{
			Tenants: []netpol.TenantPolicy{
				{Tenant: "alpha", CIDRs: []netpol.Prefix{mustPrefix(t, "10.40.0.0/24")}},
			},
		}
		cp, err := enf.Apply(context.Background(), good)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if fake.CallCount() != 1 || enf.Applied() == nil || mustRender(t, enf.Applied()) != mustRender(t, cp) {
			t.Fatalf("unexpected enforcer state: calls=%d applied=%v", fake.CallCount(), enf.Applied())
		}

		// Failed subsequent batch preserves previously applied policy.
		fake.Err = errors.New("netlink busy")
		if _, err := enf.Apply(context.Background(), netpol.PolicyBundle{}); err == nil {
			t.Fatal("expected runner failure")
		}
		if enf.Applied() == nil || mustRender(t, enf.Applied()) != mustRender(t, cp) {
			t.Fatal("Applied() mutated despite failed batch")
		}

		fake.Err = nil
		if err := enf.Teardown(context.Background()); err != nil {
			t.Fatalf("Teardown: %v", err)
		}
		if enf.Applied() != nil {
			t.Fatal("Applied() not cleared after Teardown")
		}
		if fake.LastScript() != netpol.RenderTeardownNft() {
			t.Fatalf("last script = %q, want teardown", fake.LastScript())
		}
	})

	// Binary path security validation for ExecNftRunner.
	tmp := t.TempDir()
	if _, err := netpol.NewExecNftRunner("relative/nft"); err == nil {
		t.Fatal("expected relative nft path to be rejected")
	}
	if _, err := netpol.NewExecNftRunner(tmp); err == nil {
		t.Fatal("expected directory path to be rejected")
	}
	nonExec := filepath.Join(tmp, "nonexec")
	if err := os.WriteFile(nonExec, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := netpol.NewExecNftRunner(nonExec); err == nil {
		t.Fatal("expected non-executable file to be rejected")
	}

	argsLog := filepath.Join(tmp, "args.txt")
	stdinLog := filepath.Join(tmp, "stdin.txt")
	fakeBin := filepath.Join(tmp, "fake-nft")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"" + argsLog + "\"\ncat > \"" + stdinLog + "\"\n"
	if err := os.WriteFile(fakeBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	symBin := filepath.Join(tmp, "nft-link")
	if err := os.Symlink(fakeBin, symBin); err != nil {
		t.Fatal(err)
	}
	runner, err := netpol.NewExecNftRunnerTrustingUID(symBin, uint32(os.Geteuid()))
	if err != nil {
		t.Fatalf("NewExecNftRunner(symlink): %v", err)
	}
	if runner.BinPath() != fakeBin {
		t.Fatalf("BinPath = %q, want resolved %q", runner.BinPath(), fakeBin)
	}
	batch := netpol.RenderTeardownNft()
	if err := runner.ExecBatch(context.Background(), batch); err != nil {
		t.Fatalf("ExecBatch: %v", err)
	}
	gotArgs, err := os.ReadFile(argsLog)
	if err != nil || strings.TrimSpace(string(gotArgs)) != "-f -" {
		t.Fatalf("argv = %q (err=%v), want \"-f -\"", string(gotArgs), err)
	}
	gotStdin, err := os.ReadFile(stdinLog)
	if err != nil || string(gotStdin) != batch {
		t.Fatalf("stdin = %q (err=%v), want %q", string(gotStdin), err, batch)
	}

	// Error sanitization on failing nft command.
	failBin := filepath.Join(tmp, "fail-nft")
	if err := os.WriteFile(failBin, []byte("#!/bin/sh\nprintf 'syntax error\\nline 2\\x07\\n' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	failRunner, err := netpol.NewExecNftRunnerTrustingUID(failBin, uint32(os.Geteuid()))
	if err != nil {
		t.Fatalf("NewExecNftRunner(failBin): %v", err)
	}
	err = failRunner.ExecBatch(context.Background(), "bad")
	if err == nil || !strings.Contains(err.Error(), "exit 1") || !strings.Contains(err.Error(), "syntax error line 2") {
		t.Fatalf("unexpected error diagnostic: %v", err)
	}
	if strings.ContainsRune(err.Error(), '\x07') {
		t.Fatalf("control char not sanitized from diagnostic: %q", err.Error())
	}

	// Fail-closed enum validation, defensive cloning, and subnet containment checks.
	validCIDR := mustPrefix(t, "10.50.0.0/24")
	if _, cerr := netpol.Compile(netpol.PolicyBundle{
		Tenants: []netpol.TenantPolicy{{Tenant: "alpha", IntraTenant: "bogus", CIDRs: []netpol.Prefix{validCIDR}}},
	}); cerr == nil || cerr.Code != netpol.ErrInvalidIntraTenantMode {
		t.Fatalf("invalid IntraTenant cerr = %v, want %q", cerr, netpol.ErrInvalidIntraTenantMode)
	}
	if _, cerr := netpol.Compile(netpol.PolicyBundle{
		Tenants: []netpol.TenantPolicy{{
			Tenant: "alpha", CIDRs: []netpol.Prefix{validCIDR},
			Rules: []netpol.Rule{{Order: 1, Direction: "sideways", PeerKind: netpol.PeerKindCIDR, PeerCIDR: validCIDR, Protocol: netpol.RuleProtoTCP}},
		}},
	}); cerr == nil || cerr.Code != netpol.ErrInvalidDirection {
		t.Fatalf("invalid Direction cerr = %v, want %q", cerr, netpol.ErrInvalidDirection)
	}
	if _, cerr := netpol.Compile(netpol.PolicyBundle{
		Tenants: []netpol.TenantPolicy{{
			Tenant: "alpha", CIDRs: []netpol.Prefix{validCIDR},
			Rules: []netpol.Rule{{Order: 1, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: validCIDR, Protocol: "sctp"}},
		}},
	}); cerr == nil || cerr.Code != netpol.ErrInvalidProtocol {
		t.Fatalf("invalid Protocol cerr = %v, want %q", cerr, netpol.ErrInvalidProtocol)
	}

	cp, cerr := netpol.Compile(netpol.PolicyBundle{
		Tenants: []netpol.TenantPolicy{{Tenant: "alpha", CIDRs: []netpol.Prefix{validCIDR}}},
	})
	if cerr != nil {
		t.Fatalf("Compile: %v", cerr)
	}
	if err := cp.ValidateUnitSubnets("alpha", []string{"10.50.0.0/25"}); err != nil {
		t.Fatalf("ValidateUnitSubnets(contained): %v", err)
	}
	if err := cp.ValidateUnitSubnets("alpha", []string{"10.51.0.0/24"}); err == nil {
		t.Fatal("ValidateUnitSubnets accepted out-of-range subnet")
	}
	if err := cp.ValidateUnitSubnets("missing", []string{"10.50.0.0/24"}); err == nil {
		t.Fatal("ValidateUnitSubnets accepted undeclared tenant")
	}

	fake := &netpol.FakeNftRunner{}
	enf, _ := netpol.NewEnforcer(fake, sim.NewMemDisk())
	if err := enf.ApplyCompiled(context.Background(), cp); err != nil {
		t.Fatalf("ApplyCompiled(valid): %v", err)
	}
	mutated := enf.Applied()
	mutated.Tenants[0].Tenant = "INVALID_MUTATION"
	if enf.Applied().Tenants[0].Tenant != "alpha" {
		t.Fatal("Enforcer.Applied() exposed mutable internal state")
	}
	if err := enf.ApplyCompiled(context.Background(), mutated); err == nil {
		t.Fatal("ApplyCompiled accepted mutated invalid CompiledPolicy")
	}
}
