//go:build drt

package drt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/netpol"
)

type netpolCase struct {
	ID      int                 `json:"id"`
	Bundle  netpol.PolicyBundle `json:"bundle"`
	Packets []netpol.Packet     `json:"packets"`
}

type netpolResult struct {
	ID               int      `json:"id"`
	OK               bool     `json:"ok"`
	Error            string   `json:"error"`
	ChainCount       int      `json:"chain_count"`
	ForwardRuleCount int      `json:"forward_rule_count"`
	Verdicts         []string `json:"verdicts"`
	NftMatchesSpec   bool     `json:"nft_matches_spec"`
	NftTable         string   `json:"nft_table"`
}

var (
	netpolErrorCodes = []string{
		string(netpol.ErrInvalidTenantName),
		string(netpol.ErrDuplicateTenant),
		string(netpol.ErrEmptyTenantCIDRs),
		string(netpol.ErrInvalidCIDR),
		string(netpol.ErrDuplicateTenantCIDR),
		string(netpol.ErrOverlappingTenantCIDRs),
		string(netpol.ErrDuplicateRuleOrder),
		string(netpol.ErrUnknownPeerTenant),
		string(netpol.ErrInvalidPortRange),
	}
	netpolVerdicts      = []string{string(netpol.VerdictAccept), string(netpol.VerdictDrop)}
	netpolRuleProtos    = []netpol.RuleProto{netpol.RuleProtoAny, netpol.RuleProtoTCP, netpol.RuleProtoUDP, netpol.RuleProtoICMP}
	netpolPacketProtos  = []netpol.PacketProto{netpol.PacketProtoTCP, netpol.PacketProtoUDP, netpol.PacketProtoICMP, netpol.PacketProtoOther}
	netpolConnStates    = []netpol.ConnState{netpol.ConnStateNew, netpol.ConnStateEstablished, netpol.ConnStateRelated, netpol.ConnStateInvalid}
	netpolTenantCatalog = []string{"alpha", "beta", "gamma", "delta", "svc-1", "a", "z9"}
)

func ipv4Prefix(a, b, c, d uint64, bits int) netpol.Prefix {
	lo := (a << 24) | (b << 16) | (c << 8) | d
	return netpol.Prefix{Family: netpol.FamilyIPv4, Hi: 0, Lo: lo, Bits: bits}
}

func ipv4Addr(a, b, c, d uint64) (uint64, uint64) {
	return 0, (a << 24) | (b << 16) | (c << 8) | d
}

func ipv6Prefix(hi, lo uint64, bits int) netpol.Prefix {
	return netpol.Prefix{Family: netpol.FamilyIPv6, Hi: hi, Lo: lo, Bits: bits}
}

func makeNonNilBundle(b netpol.PolicyBundle) netpol.PolicyBundle {
	if b.Tenants == nil {
		b.Tenants = []netpol.TenantPolicy{}
	}
	for i := range b.Tenants {
		if b.Tenants[i].CIDRs == nil {
			b.Tenants[i].CIDRs = []netpol.Prefix{}
		}
		if b.Tenants[i].Rules == nil {
			b.Tenants[i].Rules = []netpol.Rule{}
		}
		for j := range b.Tenants[i].Rules {
			if b.Tenants[i].Rules[j].PeerCIDR.Family == "" {
				b.Tenants[i].Rules[j].PeerCIDR.Family = netpol.FamilyIPv4
			}
		}
	}
	return b
}

func sampleAddrInPrefix(rng *rand.Rand, p netpol.Prefix) (uint64, uint64) {
	if p.Family == netpol.FamilyIPv4 {
		if p.Bits == 32 {
			return 0, p.Lo
		}
		if p.Bits == 0 {
			return 0, rng.Uint64() & 0xffffffff
		}
		span := uint64(1) << uint(32-p.Bits)
		return 0, p.Lo + (rng.Uint64() % span)
	}
	switch {
	case p.Bits == 128:
		return p.Hi, p.Lo
	case p.Bits == 64:
		return p.Hi, rng.Uint64()
	case p.Bits > 64:
		span := uint64(1) << uint(128-p.Bits)
		return p.Hi, p.Lo + (rng.Uint64() % span)
	case p.Bits == 0:
		return rng.Uint64(), rng.Uint64()
	default:
		hiSpan := uint64(1) << uint(64-p.Bits)
		return p.Hi + (rng.Uint64() % hiSpan), rng.Uint64()
	}
}

func makeValidDisjointBundle(rng *rand.Rand, numTenants int, dualStack bool) netpol.PolicyBundle {
	names := slices.Clone(netpolTenantCatalog)
	rng.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
	if numTenants > len(names) {
		numTenants = len(names)
	}
	chosen := names[:numTenants]
	tenants := make([]netpol.TenantPolicy, numTenants)
	for i, name := range chosen {
		// Disjoint /24 IPv4 blocks under 10.(10+i).0.0/24 and optional second /28 under 10.(10+i).1.0/28.
		cidrs := []netpol.Prefix{ipv4Prefix(10, uint64(10+i), 0, 0, 24)}
		if chance(rng, 45) {
			cidrs = append(cidrs, ipv4Prefix(10, uint64(10+i), 1, 0, 28))
		}
		if dualStack || chance(rng, 55) {
			hi := uint64(0xfd00000000000000) | uint64(i+1)
			cidrs = append(cidrs, ipv6Prefix(hi, 0, 64))
			if chance(rng, 35) {
				cidrs = append(cidrs, ipv6Prefix(hi|0x100, 0x0000000000000010, 124))
			}
		}
		rng.Shuffle(len(cidrs), func(a, b int) { cidrs[a], cidrs[b] = cidrs[b], cidrs[a] })
		mode := netpol.IntraTenantAllow
		if chance(rng, 35) {
			mode = netpol.IntraTenantDeny
		}
		tenants[i] = netpol.TenantPolicy{
			Tenant:      name,
			IntraTenant: mode,
			CIDRs:       cidrs,
			Rules:       []netpol.Rule{},
		}
	}
	return netpol.PolicyBundle{Tenants: tenants}
}

func randomRuleForTenant(rng *rand.Rand, tenants []netpol.TenantPolicy, order int64) netpol.Rule {
	dir := netpol.DirectionEgress
	if chance(rng, 50) {
		dir = netpol.DirectionIngress
	}
	proto := pick(rng, netpolRuleProtos)
	hasPorts := false
	portMin, portMax := 0, 0
	if (proto == netpol.RuleProtoTCP || proto == netpol.RuleProtoUDP) && chance(rng, 60) {
		hasPorts = true
		p1 := pick(rng, []int{1, 53, 80, 443, 8000, 8080, 65535})
		p2 := p1
		if p1 < 65535 && chance(rng, 55) {
			p2 = min(65535, p1+rng.IntN(200))
		}
		portMin, portMax = p1, p2
	}

	if len(tenants) > 0 && chance(rng, 55) {
		peer := pick(rng, tenants)
		return netpol.Rule{
			Order:      order,
			Direction:  dir,
			PeerKind:   netpol.PeerKindTenant,
			PeerTenant: peer.Tenant,
			PeerCIDR:   netpol.Prefix{Family: netpol.FamilyIPv4},
			Protocol:   proto,
			HasPorts:   hasPorts,
			PortMin:    portMin,
			PortMax:    portMax,
		}
	}

	peerCIDRs := []netpol.Prefix{
		ipv4Prefix(0, 0, 0, 0, 0),
		ipv4Prefix(198, 51, 100, 0, 24),
		ipv4Prefix(203, 0, 113, 7, 32),
		ipv6Prefix(0, 0, 0),
		ipv6Prefix(0x20010db800000000, 0, 32),
		ipv6Prefix(0x20010db800010000, 0x0000000000000001, 128),
	}
	if len(tenants) > 0 && chance(rng, 35) {
		peer := pick(rng, tenants)
		peerCIDRs = append(peerCIDRs, pick(rng, peer.CIDRs))
	}
	return netpol.Rule{
		Order:      order,
		Direction:  dir,
		PeerKind:   netpol.PeerKindCIDR,
		PeerTenant: "",
		PeerCIDR:   pick(rng, peerCIDRs),
		Protocol:   proto,
		HasPorts:   hasPorts,
		PortMin:    portMin,
		PortMax:    portMax,
	}
}

func sampleEndpoint(rng *rand.Rand, b netpol.PolicyBundle, fam netpol.Family) (uint64, uint64) {
	var matching []netpol.Prefix
	for _, t := range b.Tenants {
		for _, c := range t.CIDRs {
			if c.Family == fam && c.Valid() {
				matching = append(matching, c)
			}
		}
	}
	if len(matching) > 0 && chance(rng, 70) {
		return sampleAddrInPrefix(rng, pick(rng, matching))
	}
	if fam == netpol.FamilyIPv4 {
		switch rng.IntN(4) {
		case 0:
			return ipv4Addr(198, 51, 100, uint64(1+rng.IntN(250)))
		case 1:
			return ipv4Addr(203, 0, 113, 7)
		case 2:
			return ipv4Addr(192, 0, 2, uint64(1+rng.IntN(250)))
		default:
			return ipv4Addr(8, 8, 8, 8)
		}
	}
	switch rng.IntN(3) {
	case 0:
		return 0x20010db800000000, uint64(1 + rng.IntN(200))
	case 1:
		return 0x20010db800010000, 1
	default:
		return 0x2606470047000000, 0x1111
	}
}

func generatePacketsForBundle(rng *rand.Rand, b netpol.PolicyBundle, count int) []netpol.Packet {
	pkts := make([]netpol.Packet, 0, count)
	ports := []int{0, 1, 53, 79, 80, 442, 443, 444, 8000, 8080, 65535}
	for range count {
		fam := netpol.FamilyIPv4
		if chance(rng, 40) {
			fam = netpol.FamilyIPv6
		}
		srcHi, srcLo := sampleEndpoint(rng, b, fam)
		dstHi, dstLo := sampleEndpoint(rng, b, fam)
		cs := netpol.ConnStateNew
		if chance(rng, 25) {
			cs = pick(rng, netpolConnStates)
		}
		pkts = append(pkts, netpol.Packet{
			Family:    fam,
			SrcHi:     srcHi,
			SrcLo:     srcLo,
			DstHi:     dstHi,
			DstLo:     dstLo,
			Protocol:  pick(rng, netpolPacketProtos),
			DstPort:   pick(rng, ports),
			ConnState: cs,
		})
	}
	return pkts
}

func generateNetpolCases(seed uint64, n int) []netpolCase {
	rng := rand.New(rand.NewPCG(seed, 0x6e6574706f6c6472))
	profiles := []string{
		"err_invalid_tenant_name",
		"err_duplicate_tenant",
		"err_empty_tenant_cidrs",
		"err_invalid_cidr_tenant",
		"err_invalid_cidr_peer",
		"err_duplicate_tenant_cidr",
		"err_overlapping_tenant_cidrs",
		"err_duplicate_rule_order",
		"err_unknown_peer_tenant",
		"err_invalid_port_range",
		"empty_bundle",
		"default_deny_no_rules",
		"cross_unilateral_egress",
		"cross_unilateral_ingress",
		"cross_mutual_allow",
		"intra_deny_rules",
		"external_ingress_egress",
		"dual_stack_edges",
		"random",
	}

	cases := make([]netpolCase, n)
	for i := range n {
		var profile string
		if i < len(profiles)*2 {
			profile = profiles[i%len(profiles)]
		} else if chance(rng, 45) {
			profile = pick(rng, profiles[:len(profiles)-1])
		} else {
			profile = "random"
		}

		var bundle netpol.PolicyBundle
		var pkts []netpol.Packet

		switch profile {
		case "err_invalid_tenant_name":
			badNames := []string{
				"",
				"Alpha",
				"bad_name",
				"-leading",
				"trailing-",
				strings.Repeat("a", 64),
				"a.b",
			}
			bundle = makeValidDisjointBundle(rng, 1+rng.IntN(2), false)
			bundle.Tenants[0].Tenant = pick(rng, badNames)
			pkts = generatePacketsForBundle(rng, bundle, 2)

		case "err_duplicate_tenant":
			bundle = makeValidDisjointBundle(rng, 2, false)
			bundle.Tenants[1].Tenant = bundle.Tenants[0].Tenant
			pkts = generatePacketsForBundle(rng, bundle, 2)

		case "err_empty_tenant_cidrs":
			bundle = makeValidDisjointBundle(rng, 2, false)
			bundle.Tenants[rng.IntN(2)].CIDRs = []netpol.Prefix{}
			pkts = generatePacketsForBundle(rng, bundle, 2)

		case "err_invalid_cidr_tenant":
			bundle = makeValidDisjointBundle(rng, 2, true)
			badCIDRs := []netpol.Prefix{
				{Family: netpol.FamilyIPv4, Hi: 1, Lo: 0, Bits: 24},
				{Family: netpol.FamilyIPv4, Hi: 0, Lo: uint64(1) << 32, Bits: 32},
				{Family: netpol.FamilyIPv4, Hi: 0, Lo: 0, Bits: 33},
				ipv4Prefix(10, 0, 0, 1, 24),
				ipv4Prefix(0, 0, 0, 1, 0),
				{Family: netpol.FamilyIPv6, Hi: 0, Lo: 0, Bits: 129},
				{Family: netpol.FamilyIPv6, Hi: 0, Lo: 1, Bits: 0},
				{Family: netpol.FamilyIPv6, Hi: 0xfd00000000000001, Lo: 1, Bits: 64},
				{Family: netpol.FamilyIPv6, Hi: 0xfd00000000000001, Lo: 0, Bits: 48},
				{Family: netpol.FamilyIPv6, Hi: 0xfd00000000000001, Lo: 3, Bits: 126},
			}
			bundle.Tenants[0].CIDRs = append(bundle.Tenants[0].CIDRs, pick(rng, badCIDRs))
			pkts = generatePacketsForBundle(rng, bundle, 2)

		case "err_invalid_cidr_peer":
			bundle = makeValidDisjointBundle(rng, 2, false)
			bundle.Tenants[0].Rules = []netpol.Rule{{
				Order:     10,
				Direction: netpol.DirectionEgress,
				PeerKind:  netpol.PeerKindCIDR,
				PeerCIDR:  ipv4Prefix(198, 51, 100, 5, 24),
				Protocol:  netpol.RuleProtoTCP,
			}}
			pkts = generatePacketsForBundle(rng, bundle, 2)

		case "err_duplicate_tenant_cidr":
			bundle = makeValidDisjointBundle(rng, 2, true)
			if chance(rng, 50) {
				// Subset overlap within IPv4 tenant CIDR.
				bundle.Tenants[0].CIDRs = []netpol.Prefix{
					ipv4Prefix(10, 50, 0, 0, 24),
					ipv4Prefix(10, 50, 0, 128, 25),
				}
			} else {
				// Exact duplicate or subset within IPv6 tenant CIDR.
				bundle.Tenants[0].CIDRs = []netpol.Prefix{
					ipv6Prefix(0xfd00000000000010, 0, 64),
					ipv6Prefix(0xfd00000000000010, 0x1000, 120),
				}
			}
			pkts = generatePacketsForBundle(rng, bundle, 2)

		case "err_overlapping_tenant_cidrs":
			bundle = makeValidDisjointBundle(rng, 2, true)
			if chance(rng, 50) {
				bundle.Tenants[0].CIDRs = []netpol.Prefix{ipv4Prefix(10, 60, 0, 0, 24)}
				bundle.Tenants[1].CIDRs = []netpol.Prefix{ipv4Prefix(10, 60, 0, 64, 26)}
			} else {
				bundle.Tenants[0].CIDRs = []netpol.Prefix{ipv6Prefix(0xfd00000000000020, 0, 60)}
				bundle.Tenants[1].CIDRs = []netpol.Prefix{ipv6Prefix(0xfd00000000000021, 0, 64)}
			}
			pkts = generatePacketsForBundle(rng, bundle, 2)

		case "err_duplicate_rule_order":
			bundle = makeValidDisjointBundle(rng, 2, false)
			ord := int64(rng.IntN(5) - 2)
			bundle.Tenants[0].Rules = []netpol.Rule{
				{Order: ord, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: bundle.Tenants[1].Tenant, PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4}, Protocol: netpol.RuleProtoAny},
				{Order: ord, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindTenant, PeerTenant: bundle.Tenants[1].Tenant, PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4}, Protocol: netpol.RuleProtoTCP},
			}
			pkts = generatePacketsForBundle(rng, bundle, 2)

		case "err_unknown_peer_tenant":
			bundle = makeValidDisjointBundle(rng, 2, false)
			bundle.Tenants[0].Rules = []netpol.Rule{{
				Order:      10,
				Direction:  netpol.DirectionEgress,
				PeerKind:   netpol.PeerKindTenant,
				PeerTenant: "nonexistent",
				PeerCIDR:   netpol.Prefix{Family: netpol.FamilyIPv4},
				Protocol:   netpol.RuleProtoAny,
			}}
			pkts = generatePacketsForBundle(rng, bundle, 2)

		case "err_invalid_port_range":
			bundle = makeValidDisjointBundle(rng, 2, false)
			badRules := []netpol.Rule{
				{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: ipv4Prefix(0, 0, 0, 0, 0), Protocol: netpol.RuleProtoICMP, HasPorts: true, PortMin: 80, PortMax: 80},
				{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: ipv4Prefix(0, 0, 0, 0, 0), Protocol: netpol.RuleProtoAny, HasPorts: true, PortMin: 80, PortMax: 80},
				{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: ipv4Prefix(0, 0, 0, 0, 0), Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 0, PortMax: 80},
				{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: ipv4Prefix(0, 0, 0, 0, 0), Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 443, PortMax: 80},
				{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: ipv4Prefix(0, 0, 0, 0, 0), Protocol: netpol.RuleProtoUDP, HasPorts: true, PortMin: 80, PortMax: 65536},
			}
			bundle.Tenants[0].Rules = []netpol.Rule{pick(rng, badRules)}
			pkts = generatePacketsForBundle(rng, bundle, 2)

		case "empty_bundle":
			bundle = netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{}}
			sHi, sLo := ipv4Addr(198, 51, 100, 1)
			dHi, dLo := ipv4Addr(203, 0, 113, 2)
			for _, cs := range netpolConnStates {
				pkts = append(pkts, netpol.Packet{
					Family: netpol.FamilyIPv4, SrcHi: sHi, SrcLo: sLo, DstHi: dHi, DstLo: dLo,
					Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: cs,
				})
			}

		case "default_deny_no_rules":
			bundle = netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{
				{Tenant: "beta", IntraTenant: netpol.IntraTenantDeny, CIDRs: []netpol.Prefix{ipv4Prefix(10, 2, 0, 0, 24)}, Rules: []netpol.Rule{}},
				{Tenant: "alpha", IntraTenant: netpol.IntraTenantAllow, CIDRs: []netpol.Prefix{ipv4Prefix(10, 1, 0, 0, 24)}, Rules: []netpol.Rule{}},
			}}
			a1H, a1L := ipv4Addr(10, 1, 0, 2)
			a2H, a2L := ipv4Addr(10, 1, 0, 3)
			b1H, b1L := ipv4Addr(10, 2, 0, 2)
			b2H, b2L := ipv4Addr(10, 2, 0, 3)
			e1H, e1L := ipv4Addr(198, 51, 100, 9)
			e2H, e2L := ipv4Addr(203, 0, 113, 9)
			pkts = []netpol.Packet{
				{Family: netpol.FamilyIPv4, SrcHi: a1H, SrcLo: a1L, DstHi: a2H, DstLo: a2L, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: b1H, SrcLo: b1L, DstHi: b2H, DstLo: b2L, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: a1H, SrcLo: a1L, DstHi: b1H, DstLo: b1L, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: a1H, SrcLo: a1L, DstHi: e1H, DstLo: e1L, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: e1H, SrcLo: e1L, DstHi: a1H, DstLo: a1L, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: e1H, SrcLo: e1L, DstHi: e2H, DstLo: e2L, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: netpol.ConnStateNew},
			}

		case "cross_unilateral_egress":
			bundle = netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{
				{
					Tenant:      "alpha",
					IntraTenant: netpol.IntraTenantAllow,
					CIDRs:       []netpol.Prefix{ipv4Prefix(10, 1, 0, 0, 24)},
					Rules: []netpol.Rule{
						{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: ipv4Prefix(0, 0, 0, 0, 0), Protocol: netpol.RuleProtoAny},
					},
				},
				{
					Tenant:      "beta",
					IntraTenant: netpol.IntraTenantAllow,
					CIDRs:       []netpol.Prefix{ipv4Prefix(10, 2, 0, 0, 24)},
					Rules:       []netpol.Rule{},
				},
			}}
			aH, aL := ipv4Addr(10, 1, 0, 10)
			bH, bL := ipv4Addr(10, 2, 0, 20)
			eH, eL := ipv4Addr(198, 51, 100, 10)
			pkts = []netpol.Packet{
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: eH, DstLo: eL, Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: netpol.ConnStateNew},
			}

		case "cross_unilateral_ingress":
			bundle = netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{
				{
					Tenant:      "alpha",
					IntraTenant: netpol.IntraTenantAllow,
					CIDRs:       []netpol.Prefix{ipv4Prefix(10, 1, 0, 0, 24)},
					Rules:       []netpol.Rule{},
				},
				{
					Tenant:      "beta",
					IntraTenant: netpol.IntraTenantAllow,
					CIDRs:       []netpol.Prefix{ipv4Prefix(10, 2, 0, 0, 24)},
					Rules: []netpol.Rule{
						{Order: 10, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindTenant, PeerTenant: "alpha", PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4}, Protocol: netpol.RuleProtoAny},
					},
				},
			}}
			aH, aL := ipv4Addr(10, 1, 0, 10)
			bH, bL := ipv4Addr(10, 2, 0, 20)
			pkts = []netpol.Packet{
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoUDP, DstPort: 53, ConnState: netpol.ConnStateNew},
			}

		case "cross_mutual_allow":
			bundle = netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{
				{
					Tenant:      "beta",
					IntraTenant: netpol.IntraTenantAllow,
					CIDRs:       []netpol.Prefix{ipv4Prefix(10, 2, 0, 0, 24)},
					Rules: []netpol.Rule{
						{Order: 20, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindTenant, PeerTenant: "alpha", PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4}, Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 8000, PortMax: 8080},
						{Order: -10, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindTenant, PeerTenant: "alpha", PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4}, Protocol: netpol.RuleProtoICMP},
					},
				},
				{
					Tenant:      "alpha",
					IntraTenant: netpol.IntraTenantAllow,
					CIDRs:       []netpol.Prefix{ipv4Prefix(10, 1, 0, 0, 24)},
					Rules: []netpol.Rule{
						{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "beta", PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4}, Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 8000, PortMax: 8080},
						{Order: 5, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "beta", PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4}, Protocol: netpol.RuleProtoICMP},
					},
				},
			}}
			aH, aL := ipv4Addr(10, 1, 0, 5)
			bH, bL := ipv4Addr(10, 2, 0, 9)
			pkts = []netpol.Packet{
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoTCP, DstPort: 7999, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoTCP, DstPort: 8000, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoTCP, DstPort: 8080, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoTCP, DstPort: 8081, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoUDP, DstPort: 8000, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoICMP, DstPort: 0, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoOther, DstPort: 8000, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoTCP, DstPort: 9999, ConnState: netpol.ConnStateEstablished},
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoTCP, DstPort: 9999, ConnState: netpol.ConnStateRelated},
				{Family: netpol.FamilyIPv4, SrcHi: aH, SrcLo: aL, DstHi: bH, DstLo: bL, Protocol: netpol.PacketProtoTCP, DstPort: 8000, ConnState: netpol.ConnStateInvalid},
			}

		case "intra_deny_rules":
			bundle = netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{
				{
					Tenant:      "isolated",
					IntraTenant: netpol.IntraTenantDeny,
					CIDRs:       []netpol.Prefix{ipv4Prefix(10, 9, 0, 0, 24)},
					Rules: []netpol.Rule{
						{Order: 1, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "isolated", PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4}, Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 443, PortMax: 443},
						{Order: 2, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindTenant, PeerTenant: "isolated", PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4}, Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 443, PortMax: 443},
						{Order: 3, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "isolated", PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4}, Protocol: netpol.RuleProtoUDP, HasPorts: true, PortMin: 53, PortMax: 53},
					},
				},
			}}
			sH, sL := ipv4Addr(10, 9, 0, 2)
			dH, dL := ipv4Addr(10, 9, 0, 3)
			pkts = []netpol.Packet{
				{Family: netpol.FamilyIPv4, SrcHi: sH, SrcLo: sL, DstHi: dH, DstLo: dL, Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: sH, SrcLo: sL, DstHi: dH, DstLo: dL, Protocol: netpol.PacketProtoUDP, DstPort: 53, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: sH, SrcLo: sL, DstHi: dH, DstLo: dL, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: netpol.ConnStateNew},
			}

		case "external_ingress_egress":
			bundle = netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{
				{
					Tenant:      "edge",
					IntraTenant: netpol.IntraTenantAllow,
					CIDRs:       []netpol.Prefix{ipv4Prefix(10, 8, 0, 0, 24)},
					Rules: []netpol.Rule{
						{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: ipv4Prefix(198, 51, 100, 0, 24), Protocol: netpol.RuleProtoUDP, HasPorts: true, PortMin: 53, PortMax: 53},
						{Order: 20, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: ipv4Prefix(203, 0, 113, 0, 24), Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 443, PortMax: 443},
					},
				},
			}}
			tH, tL := ipv4Addr(10, 8, 0, 4)
			dnsH, dnsL := ipv4Addr(198, 51, 100, 53)
			cliH, cliL := ipv4Addr(203, 0, 113, 88)
			othH, othL := ipv4Addr(192, 0, 2, 1)
			pkts = []netpol.Packet{
				{Family: netpol.FamilyIPv4, SrcHi: tH, SrcLo: tL, DstHi: dnsH, DstLo: dnsL, Protocol: netpol.PacketProtoUDP, DstPort: 53, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: tH, SrcLo: tL, DstHi: othH, DstLo: othL, Protocol: netpol.PacketProtoUDP, DstPort: 53, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: cliH, SrcLo: cliL, DstHi: tH, DstLo: tL, Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: othH, SrcLo: othL, DstHi: tH, DstLo: tL, Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: cliH, SrcLo: cliL, DstHi: dnsH, DstLo: dnsL, Protocol: netpol.PacketProtoUDP, DstPort: 53, ConnState: netpol.ConnStateNew},
			}

		case "dual_stack_edges":
			bundle = netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{
				{
					Tenant:      "v6a",
					IntraTenant: netpol.IntraTenantAllow,
					CIDRs: []netpol.Prefix{
						ipv6Prefix(0xfd00000000000001, 0, 64),
						ipv4Prefix(10, 70, 0, 1, 32),
					},
					Rules: []netpol.Rule{
						{Order: 1, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "v6b", PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4}, Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 1, PortMax: 65535},
						{Order: 2, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: ipv6Prefix(0, 0, 0), Protocol: netpol.RuleProtoICMP},
					},
				},
				{
					Tenant:      "v6b",
					IntraTenant: netpol.IntraTenantDeny,
					CIDRs: []netpol.Prefix{
						ipv6Prefix(0xfd00000000000002, 0xffffffffffffffff, 128),
						ipv4Prefix(10, 70, 0, 2, 32),
					},
					Rules: []netpol.Rule{
						{Order: 1, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindTenant, PeerTenant: "v6a", PeerCIDR: netpol.Prefix{Family: netpol.FamilyIPv4}, Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 443, PortMax: 443},
					},
				},
			}}
			v4aH, v4aL := ipv4Addr(10, 70, 0, 1)
			v4bH, v4bL := ipv4Addr(10, 70, 0, 2)
			pkts = []netpol.Packet{
				{Family: netpol.FamilyIPv6, SrcHi: 0xfd00000000000001, SrcLo: 1, DstHi: 0xfd00000000000001, DstLo: 2, Protocol: netpol.PacketProtoUDP, DstPort: 1234, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv6, SrcHi: 0xfd00000000000001, SrcLo: 1, DstHi: 0xfd00000000000002, DstLo: 0xffffffffffffffff, Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv6, SrcHi: 0xfd00000000000001, SrcLo: 1, DstHi: 0xfd00000000000002, DstLo: 0xffffffffffffffff, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv6, SrcHi: 0xfd00000000000001, SrcLo: 1, DstHi: 0x20010db800000000, DstLo: 9, Protocol: netpol.PacketProtoICMP, DstPort: 0, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv6, SrcHi: 0xfd00000000000001, SrcLo: 1, DstHi: 0xfd00000000000002, DstLo: 0xffffffffffffffff, Protocol: netpol.PacketProtoICMP, DstPort: 0, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: v4aH, SrcLo: v4aL, DstHi: v4bH, DstLo: v4bL, Protocol: netpol.PacketProtoTCP, DstPort: 443, ConnState: netpol.ConnStateNew},
				{Family: netpol.FamilyIPv4, SrcHi: v4aH, SrcLo: v4aL, DstHi: 0, DstLo: 0x08080808, Protocol: netpol.PacketProtoICMP, DstPort: 0, ConnState: netpol.ConnStateNew},
			}

		default:
			numTenants := rng.IntN(4)
			bundle = makeValidDisjointBundle(rng, numTenants, chance(rng, 50))
			for ti := range bundle.Tenants {
				numRules := rng.IntN(5)
				usedOrders := make(map[int64]bool, numRules)
				for range numRules {
					var ord int64
					for {
						ord = int64(rng.IntN(201) - 100)
						if !usedOrders[ord] {
							usedOrders[ord] = true
							break
						}
					}
					bundle.Tenants[ti].Rules = append(bundle.Tenants[ti].Rules, randomRuleForTenant(rng, bundle.Tenants, ord))
				}
			}
			pkts = generatePacketsForBundle(rng, bundle, 6+rng.IntN(10))
		}

		cases[i] = netpolCase{
			ID:      i,
			Bundle:  makeNonNilBundle(bundle),
			Packets: pkts,
		}
	}
	return cases
}

func encodeNetpolCases(t *testing.T, cases []netpolCase) [][]byte {
	t.Helper()
	out := make([][]byte, len(cases))
	for i, c := range cases {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal netpol case %d: %v", c.ID, err)
		}
		out[i] = b
	}
	return out
}

type netpolHooks struct {
	allowUnilateralEgress   bool
	allowUnilateralIngress  bool
	acceptInvalidConn       bool
	dropEstablishedConn     bool
	dropRelatedConn         bool
	allowExternalTransit    bool
	skipPortRangeCheck      bool
	portMaxExclusive        bool
	skipProtoCheck          bool
	ignoreIntraTenantDeny   bool
	skipCrossCIDROverlap    bool
	skipIntraCIDROverlap    bool
	skipDupRuleOrder        bool
	skipUnknownPeerTenant   bool
	skipInvalidPortRange    bool
	wrongNftJumpFallthrough bool
	corruptTable            func(tbl *netpol.NftTable)
}

func runNetpolCase(tc netpolCase, hooks netpolHooks) netpolResult {
	bundle := tc.Bundle
	if hooks.skipIntraCIDROverlap {
		for i, t := range bundle.Tenants {
			if len(t.CIDRs) >= 2 && netpol.PrefixesOverlap(t.CIDRs[0], t.CIDRs[1]) {
				patched := makeNonNilBundle(netpol.PolicyBundle{Tenants: slices.Clone(bundle.Tenants)})
				patched.Tenants[i].CIDRs = []netpol.Prefix{t.CIDRs[0]}
				if cp, cerr := netpol.Compile(patched); cerr == nil {
					tbl := netpol.CompileNft(cp)
					verdicts := make([]string, len(tc.Packets))
					for pi, pkt := range tc.Packets {
						verdicts[pi] = string(netpol.Evaluate(cp, pkt))
					}
					return netpolResult{
						ID:               tc.ID,
						OK:               true,
						Error:            "",
						ChainCount:       tbl.ChainCount(),
						ForwardRuleCount: tbl.ForwardRuleCount(),
						Verdicts:         verdicts,
						NftMatchesSpec:   true,
						NftTable:         renderNftTable(tbl),
					}
				}
			}
		}
	}
	if hooks.skipCrossCIDROverlap && len(bundle.Tenants) >= 2 {
		if _, cerr := netpol.Compile(bundle); cerr != nil && cerr.Code == netpol.ErrOverlappingTenantCIDRs {
			return netpolResult{
				ID:               tc.ID,
				OK:               true,
				Error:            "",
				ChainCount:       1 + 2*len(bundle.Tenants),
				ForwardRuleCount: 2,
				Verdicts:         make([]string, len(tc.Packets)),
				NftMatchesSpec:   true,
			}
		}
	}
	if hooks.skipDupRuleOrder {
		if _, cerr := netpol.Compile(bundle); cerr != nil && cerr.Code == netpol.ErrDuplicateRuleOrder {
			return netpolResult{
				ID:               tc.ID,
				OK:               true,
				Error:            "",
				ChainCount:       1 + 2*len(bundle.Tenants),
				ForwardRuleCount: 2,
				Verdicts:         make([]string, len(tc.Packets)),
				NftMatchesSpec:   true,
			}
		}
	}
	if hooks.skipUnknownPeerTenant {
		if _, cerr := netpol.Compile(bundle); cerr != nil && cerr.Code == netpol.ErrUnknownPeerTenant {
			return netpolResult{
				ID:               tc.ID,
				OK:               true,
				Error:            "",
				ChainCount:       1 + 2*len(bundle.Tenants),
				ForwardRuleCount: 2,
				Verdicts:         make([]string, len(tc.Packets)),
				NftMatchesSpec:   true,
			}
		}
	}
	if hooks.skipInvalidPortRange {
		if _, cerr := netpol.Compile(bundle); cerr != nil && cerr.Code == netpol.ErrInvalidPortRange {
			return netpolResult{
				ID:               tc.ID,
				OK:               true,
				Error:            "",
				ChainCount:       1 + 2*len(bundle.Tenants),
				ForwardRuleCount: 2,
				Verdicts:         make([]string, len(tc.Packets)),
				NftMatchesSpec:   true,
			}
		}
	}

	cp, cerr := netpol.Compile(bundle)
	if cerr != nil {
		return netpolResult{
			ID:               tc.ID,
			OK:               false,
			Error:            string(cerr.Code),
			ChainCount:       0,
			ForwardRuleCount: 0,
			Verdicts:         []string{},
			NftMatchesSpec:   true,
		}
	}

	tbl := netpol.CompileNft(cp)
	verdicts := make([]string, len(tc.Packets))
	nftMatches := true
	for i, pkt := range tc.Packets {
		v := evaluateWithHooks(cp, pkt, hooks)
		verdicts[i] = string(v)
		nftV := netpol.EvalNft(tbl, pkt)
		if hooks.wrongNftJumpFallthrough && pkt.ConnState == netpol.ConnStateNew && nftV == netpol.VerdictDrop {
			nftV = netpol.VerdictAccept
		}
		if nftV != v {
			nftMatches = false
		}
	}

	rendered := tbl
	if hooks.corruptTable != nil {
		rendered = cloneNftTable(tbl)
		hooks.corruptTable(&rendered)
	}
	return netpolResult{
		ID:               tc.ID,
		OK:               true,
		Error:            "",
		ChainCount:       tbl.ChainCount(),
		ForwardRuleCount: tbl.ForwardRuleCount(),
		Verdicts:         verdicts,
		NftMatchesSpec:   nftMatches,
		NftTable:         renderNftTable(rendered),
	}
}

func renderNftPrefix(has bool, p netpol.Prefix) string {
	if !has {
		return "-"
	}
	return fmt.Sprintf("%s:%d:%d/%d", p.Family, p.Hi, p.Lo, p.Bits)
}

func renderNftRule(r netpol.NftRule) string {
	conn := string(r.ConnMatch)
	if conn == "" {
		conn = "-"
	}
	proto := string(r.Protocol)
	if proto == "" {
		proto = "-"
	}
	ports := "-"
	if r.HasPorts {
		ports = fmt.Sprintf("%d-%d", r.PortMin, r.PortMax)
	}
	action := string(r.Action.Kind)
	if r.Action.Kind == netpol.NftActionJump {
		action += ":" + r.Action.Target
	}
	return fmt.Sprintf("  rule ct=%s src=%s dst=%s proto=%s ports=%s action=%s\n",
		conn, renderNftPrefix(r.HasSrc, r.SrcPrefix), renderNftPrefix(r.HasDst, r.DstPrefix), proto, ports, action)
}

func renderNftTable(tbl netpol.NftTable) string {
	var b strings.Builder
	b.WriteString("table " + tbl.Name + "\n")
	for _, ch := range tbl.Chains {
		base, policy := 0, "-"
		if ch.IsBase {
			base, policy = 1, string(ch.DefaultPolicy)
		}
		fmt.Fprintf(&b, "chain %s base=%d policy=%s\n", ch.Name, base, policy)
		for _, r := range ch.Rules {
			b.WriteString(renderNftRule(r))
		}
	}
	return b.String()
}

func cloneNftTable(tbl netpol.NftTable) netpol.NftTable {
	out := netpol.NftTable{Name: tbl.Name, Chains: slices.Clone(tbl.Chains)}
	for i := range out.Chains {
		out.Chains[i].Rules = slices.Clone(out.Chains[i].Rules)
	}
	return out
}

func evaluateWithHooks(cp *netpol.CompiledPolicy, pkt netpol.Packet, hooks netpolHooks) netpol.Verdict {
	if hooks.acceptInvalidConn && pkt.ConnState == netpol.ConnStateInvalid {
		return netpol.VerdictAccept
	}
	if hooks.dropEstablishedConn && pkt.ConnState == netpol.ConnStateEstablished {
		return netpol.VerdictDrop
	}
	if hooks.dropRelatedConn && pkt.ConnState == netpol.ConnStateRelated {
		return netpol.VerdictDrop
	}
	if pkt.ConnState != netpol.ConnStateNew {
		return netpol.Evaluate(cp, pkt)
	}

	if hooks.ignoreIntraTenantDeny {
		cloned := &netpol.CompiledPolicy{Tenants: slices.Clone(cp.Tenants)}
		for i := range cloned.Tenants {
			cloned.Tenants[i].IntraTenant = netpol.IntraTenantAllow
		}
		return netpol.Evaluate(cloned, pkt)
	}
	if hooks.skipPortRangeCheck {
		cloned := cloneCompiledPolicy(cp)
		for i := range cloned.Tenants {
			for j := range cloned.Tenants[i].Rules {
				cloned.Tenants[i].Rules[j].HasPorts = false
			}
		}
		return netpol.Evaluate(cloned, pkt)
	}
	if hooks.portMaxExclusive {
		cloned := cloneCompiledPolicy(cp)
		for i := range cloned.Tenants {
			for j := range cloned.Tenants[i].Rules {
				if cloned.Tenants[i].Rules[j].HasPorts && cloned.Tenants[i].Rules[j].PortMax > cloned.Tenants[i].Rules[j].PortMin {
					cloned.Tenants[i].Rules[j].PortMax--
				}
			}
		}
		return netpol.Evaluate(cloned, pkt)
	}
	if hooks.skipProtoCheck {
		cloned := cloneCompiledPolicy(cp)
		for i := range cloned.Tenants {
			for j := range cloned.Tenants[i].Rules {
				cloned.Tenants[i].Rules[j].Protocol = netpol.RuleProtoAny
			}
		}
		return netpol.Evaluate(cloned, pkt)
	}

	srcIdx := findTenantIdx(cp, pkt.Family, pkt.SrcHi, pkt.SrcLo)
	dstIdx := findTenantIdx(cp, pkt.Family, pkt.DstHi, pkt.DstLo)
	if hooks.allowExternalTransit && srcIdx < 0 && dstIdx < 0 {
		return netpol.VerdictAccept
	}
	if hooks.allowUnilateralEgress && srcIdx >= 0 && dstIdx >= 0 && srcIdx != dstIdx {
		if matchesTenantDir(cp, srcIdx, netpol.DirectionEgress, pkt) {
			return netpol.VerdictAccept
		}
	}
	if hooks.allowUnilateralIngress && srcIdx >= 0 && dstIdx >= 0 && srcIdx != dstIdx {
		if matchesTenantDir(cp, dstIdx, netpol.DirectionIngress, pkt) {
			return netpol.VerdictAccept
		}
	}
	return netpol.Evaluate(cp, pkt)
}

func cloneCompiledPolicy(cp *netpol.CompiledPolicy) *netpol.CompiledPolicy {
	out := &netpol.CompiledPolicy{Tenants: make([]netpol.TenantPolicy, len(cp.Tenants))}
	for i, t := range cp.Tenants {
		out.Tenants[i] = netpol.TenantPolicy{
			Tenant:      t.Tenant,
			IntraTenant: t.IntraTenant,
			CIDRs:       slices.Clone(t.CIDRs),
			Rules:       slices.Clone(t.Rules),
		}
	}
	return out
}

func findTenantIdx(cp *netpol.CompiledPolicy, fam netpol.Family, hi, lo uint64) int {
	for i, t := range cp.Tenants {
		for _, c := range t.CIDRs {
			if c.Contains(fam, hi, lo) {
				return i
			}
		}
	}
	return -1
}

func matchesTenantDir(cp *netpol.CompiledPolicy, tenantIdx int, dir netpol.Direction, pkt netpol.Packet) bool {
	// Synthesize a single-endpoint external probe by evaluating with an external opposite address
	// if peer is CIDR, or test directly against rules.
	t := cp.Tenants[tenantIdx]
	var peerHi, peerLo uint64
	if dir == netpol.DirectionEgress {
		peerHi, peerLo = pkt.DstHi, pkt.DstLo
	} else {
		peerHi, peerLo = pkt.SrcHi, pkt.SrcLo
	}
	for _, r := range t.Rules {
		if r.Direction != dir {
			continue
		}
		peerOK := false
		if r.PeerKind == netpol.PeerKindCIDR {
			peerOK = r.PeerCIDR.Contains(pkt.Family, peerHi, peerLo)
		} else if r.PeerKind == netpol.PeerKindTenant {
			for _, pt := range cp.Tenants {
				if pt.Tenant == r.PeerTenant {
					for _, c := range pt.CIDRs {
						if c.Contains(pkt.Family, peerHi, peerLo) {
							peerOK = true
							break
						}
					}
					break
				}
			}
		}
		if !peerOK {
			continue
		}
		protoOK := r.Protocol == netpol.RuleProtoAny || string(r.Protocol) == string(pkt.Protocol)
		if !protoOK {
			continue
		}
		if r.HasPorts && (pkt.DstPort < r.PortMin || pkt.DstPort > r.PortMax) {
			continue
		}
		return true
	}
	return false
}

func decodeNetpolResult(line []byte) (netpolResult, error) {
	trimmed := bytes.TrimSpace(line)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rawTop json.RawMessage
	if err := dec.Decode(&rawTop); err != nil {
		return netpolResult{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return netpolResult{}, fmt.Errorf("unexpected trailing data in result line: %v", err)
	}
	top, err := decodeObject(rawTop, "id", "ok", "error", "chain_count", "forward_rule_count", "verdicts", "nft_matches_spec", "nft_table")
	if err != nil {
		return netpolResult{}, err
	}
	id, err := decodeNonNegInt(top["id"])
	if err != nil {
		return netpolResult{}, fmt.Errorf("id: %w", err)
	}
	okVal, err := decodeBool(top["ok"])
	if err != nil {
		return netpolResult{}, fmt.Errorf("ok: %w", err)
	}
	errCode, err := decodeString(top["error"])
	if err != nil {
		return netpolResult{}, fmt.Errorf("error: %w", err)
	}
	if okVal {
		if errCode != "" {
			return netpolResult{}, fmt.Errorf("ok=true with non-empty error %q", errCode)
		}
	} else {
		if !slices.Contains(netpolErrorCodes, errCode) {
			return netpolResult{}, fmt.Errorf("unknown error code %q", errCode)
		}
	}
	chainCount, err := decodeNonNegInt(top["chain_count"])
	if err != nil {
		return netpolResult{}, fmt.Errorf("chain_count: %w", err)
	}
	fwdRuleCount, err := decodeNonNegInt(top["forward_rule_count"])
	if err != nil {
		return netpolResult{}, fmt.Errorf("forward_rule_count: %w", err)
	}
	verdicts, err := decodeStringSlice(top["verdicts"])
	if err != nil {
		return netpolResult{}, fmt.Errorf("verdicts: %w", err)
	}
	for i, v := range verdicts {
		if !slices.Contains(netpolVerdicts, v) {
			return netpolResult{}, fmt.Errorf("verdicts[%d]: unknown verdict %q", i, v)
		}
	}
	nftMatches, err := decodeBool(top["nft_matches_spec"])
	if err != nil {
		return netpolResult{}, fmt.Errorf("nft_matches_spec: %w", err)
	}
	nftTable, err := decodeString(top["nft_table"])
	if err != nil {
		return netpolResult{}, fmt.Errorf("nft_table: %w", err)
	}
	if okVal != (nftTable != "") {
		return netpolResult{}, fmt.Errorf("ok=%v with nft_table length %d", okVal, len(nftTable))
	}
	return netpolResult{
		ID:               id,
		OK:               okVal,
		Error:            errCode,
		ChainCount:       chainCount,
		ForwardRuleCount: fwdRuleCount,
		Verdicts:         verdicts,
		NftMatchesSpec:   nftMatches,
		NftTable:         nftTable,
	}, nil
}

func compareNetpolResult(want, got netpolResult) string {
	switch {
	case want.ID != got.ID:
		return fmt.Sprintf("id mismatch: lean=%d go=%d", want.ID, got.ID)
	case want.OK != got.OK:
		return fmt.Sprintf("ok mismatch: lean=%v go=%v", want.OK, got.OK)
	case want.Error != got.Error:
		return fmt.Sprintf("error mismatch: lean=%q go=%q", want.Error, got.Error)
	case want.ChainCount != got.ChainCount:
		return fmt.Sprintf("chain_count mismatch: lean=%d go=%d", want.ChainCount, got.ChainCount)
	case want.ForwardRuleCount != got.ForwardRuleCount:
		return fmt.Sprintf("forward_rule_count mismatch: lean=%d go=%d", want.ForwardRuleCount, got.ForwardRuleCount)
	case want.NftMatchesSpec != got.NftMatchesSpec:
		return fmt.Sprintf("nft_matches_spec mismatch: lean=%v go=%v", want.NftMatchesSpec, got.NftMatchesSpec)
	case want.NftTable != got.NftTable:
		return "nft_table mismatch: " + firstLineDiff(want.NftTable, got.NftTable)
	case len(want.Verdicts) != len(got.Verdicts):
		return fmt.Sprintf("verdicts len mismatch: lean=%d go=%d", len(want.Verdicts), len(got.Verdicts))
	}
	for i := range want.Verdicts {
		if want.Verdicts[i] != got.Verdicts[i] {
			return fmt.Sprintf("verdicts[%d] mismatch: lean=%q go=%q", i, want.Verdicts[i], got.Verdicts[i])
		}
	}
	return ""
}

func firstLineDiff(lean, goText string) string {
	l, g := strings.Split(lean, "\n"), strings.Split(goText, "\n")
	for i := 0; i < max(len(l), len(g)); i++ {
		var lv, gv string
		if i < len(l) {
			lv = l[i]
		}
		if i < len(g) {
			gv = g[i]
		}
		if lv != gv {
			return fmt.Sprintf("line %d differs\n  lean: %q\n  go:   %q", i+1, lv, gv)
		}
	}
	return "identical"
}

func cloneNetpolResult(r netpolResult) netpolResult {
	return netpolResult{
		ID:               r.ID,
		OK:               r.OK,
		Error:            r.Error,
		ChainCount:       r.ChainCount,
		ForwardRuleCount: r.ForwardRuleCount,
		Verdicts:         slices.Clone(r.Verdicts),
		NftMatchesSpec:   r.NftMatchesSpec,
		NftTable:         r.NftTable,
	}
}

func leanNetpolResults(t *testing.T, cases []netpolCase) ([][]byte, [][]byte, []netpolResult) {
	t.Helper()
	encoded := encodeNetpolCases(t, cases)
	lines, err := RunModel("netpol", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(lines), len(cases))
	}
	results := make([]netpolResult, len(lines))
	for i, line := range lines {
		res, err := decodeNetpolResult(line)
		if err != nil {
			t.Fatalf("id=%d decode error: %v\ncase: %s\nlean: %s", cases[i].ID, err, encoded[i], line)
		}
		results[i] = res
	}
	return encoded, lines, results
}

func TestNetpolMatchesLeanModel(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateNetpolCases(cfg.Seed, cfg.Cases)
	encoded, leanLines, leanResults := leanNetpolResults(t, cases)

	repro := fmt.Sprintf("NAYUTAR_DRT_SEED=%d NAYUTAR_DRT_CASES=%d go test -tags drt -run '^TestNetpolMatchesLeanModel$' ./core/drt/...", cfg.Seed, cfg.Cases)
	totalPackets := 0
	for i, tc := range cases {
		goRes := runNetpolCase(tc, netpolHooks{})
		if diff := compareNetpolResult(leanResults[i], goRes); diff != "" {
			goJSON, marshalErr := json.Marshal(goRes)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			t.Fatalf("seed=%d id=%d mismatch (%s)\ncase: %s\nlean: %s\ngo:   %s\nreproduce: %s",
				cfg.Seed, tc.ID, diff, encoded[i], leanLines[i], goJSON, repro)
		}
		if !goRes.NftMatchesSpec {
			t.Fatalf("seed=%d id=%d Go EvalNft diverged from Evaluate\ncase: %s\nreproduce: %s",
				cfg.Seed, tc.ID, encoded[i], repro)
		}
		totalPackets += len(goRes.Verdicts)
	}
	t.Logf("compared %d cases (%d evaluated packets, seed=%d)", len(cases), totalPackets, cfg.Seed)
}

func TestNetpolCasesReachEveryOutcome(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateNetpolCases(cfg.Seed, cfg.Cases)
	_, _, results := leanNetpolResults(t, cases)

	seen := map[string]int{}
	for i, tc := range cases {
		res := results[i]
		if !res.OK {
			seen["error/"+res.Error]++
			continue
		}
		seen["compile/ok"]++
		cp, cerr := netpol.Compile(tc.Bundle)
		if cerr != nil {
			t.Fatalf("case %d compiled in Lean but failed in Go: %v", tc.ID, cerr)
		}
		if len(cp.Tenants) == 0 {
			seen["compile/empty_bundle"]++
		}
		for _, tenant := range cp.Tenants {
			for _, r := range tenant.Rules {
				seen["rule_proto/"+string(r.Protocol)]++
				seen["peer_kind/"+string(r.PeerKind)]++
				if r.HasPorts {
					seen["rule/has_ports"]++
				}
			}
		}
		for j, pkt := range tc.Packets {
			v := res.Verdicts[j]
			seen["family/"+string(pkt.Family)]++
			seen["conn/"+string(pkt.ConnState)]++
			seen["pkt_proto/"+string(pkt.Protocol)]++
			seen["verdict/"+v]++

			if pkt.ConnState != netpol.ConnStateNew {
				continue
			}
			sIdx := findTenantIdx(cp, pkt.Family, pkt.SrcHi, pkt.SrcLo)
			dIdx := findTenantIdx(cp, pkt.Family, pkt.DstHi, pkt.DstLo)
			switch {
			case sIdx >= 0 && dIdx >= 0 && sIdx == dIdx:
				if cp.Tenants[sIdx].IntraTenant == netpol.IntraTenantAllow {
					if v == string(netpol.VerdictAccept) {
						seen["eval/intra_allow_accept"]++
					}
				} else {
					if v == string(netpol.VerdictAccept) {
						seen["eval/intra_deny_accept"]++
					} else {
						seen["eval/intra_deny_drop"]++
					}
				}
			case sIdx >= 0 && dIdx >= 0 && sIdx != dIdx:
				egr := matchesTenantDir(cp, sIdx, netpol.DirectionEgress, pkt)
				ing := matchesTenantDir(cp, dIdx, netpol.DirectionIngress, pkt)
				switch {
				case egr && ing && v == string(netpol.VerdictAccept):
					seen["eval/cross_mutual_accept"]++
				case egr && !ing && v == string(netpol.VerdictDrop):
					seen["eval/cross_unilateral_egress_drop"]++
				case !egr && ing && v == string(netpol.VerdictDrop):
					seen["eval/cross_unilateral_ingress_drop"]++
				case !egr && !ing && v == string(netpol.VerdictDrop):
					seen["eval/cross_default_deny_drop"]++
				}
			case sIdx >= 0 && dIdx < 0:
				if v == string(netpol.VerdictAccept) {
					seen["eval/external_egress_accept"]++
				} else {
					seen["eval/external_egress_drop"]++
				}
			case sIdx < 0 && dIdx >= 0:
				if v == string(netpol.VerdictAccept) {
					seen["eval/external_ingress_accept"]++
				} else {
					seen["eval/external_ingress_drop"]++
				}
			default:
				if v == string(netpol.VerdictDrop) {
					seen["eval/external_transit_drop"]++
				}
			}
		}
	}

	required := []string{
		"compile/ok",
		"compile/empty_bundle",
		"family/ipv4",
		"family/ipv6",
		"peer_kind/tenant",
		"peer_kind/cidr",
		"rule/has_ports",
		"verdict/accept",
		"verdict/drop",
		"eval/intra_allow_accept",
		"eval/intra_deny_accept",
		"eval/intra_deny_drop",
		"eval/cross_mutual_accept",
		"eval/cross_unilateral_egress_drop",
		"eval/cross_unilateral_ingress_drop",
		"eval/cross_default_deny_drop",
		"eval/external_egress_accept",
		"eval/external_egress_drop",
		"eval/external_ingress_accept",
		"eval/external_ingress_drop",
		"eval/external_transit_drop",
	}
	for _, code := range netpolErrorCodes {
		required = append(required, "error/"+code)
	}
	for _, cs := range netpolConnStates {
		required = append(required, "conn/"+string(cs))
	}
	for _, rp := range netpolRuleProtos {
		required = append(required, "rule_proto/"+string(rp))
	}
	for _, pp := range netpolPacketProtos {
		required = append(required, "pkt_proto/"+string(pp))
	}

	for _, item := range required {
		if seen[item] == 0 {
			t.Errorf("no case reached coverage item %q (seed=%d, cases=%d)", item, cfg.Seed, cfg.Cases)
		}
	}
}

func TestNetpolComparisonDetectsEveryFieldMutation(t *testing.T) {
	cases := generateNetpolCases(1, 80)
	_, leanLines, results := leanNetpolResults(t, cases)

	applied := map[string]int{}
	assertDetects := func(kind string, orig netpolResult, mutate func(r *netpolResult)) {
		t.Helper()
		mut := cloneNetpolResult(orig)
		mutate(&mut)
		applied[kind]++
		if compareNetpolResult(orig, mut) == "" {
			t.Fatalf("mutation %q on case %d was not detected", kind, orig.ID)
		}
	}

	for _, orig := range results {
		assertDetects("id+1", orig, func(r *netpolResult) { r.ID++ })
		assertDetects("flip-ok", orig, func(r *netpolResult) { r.OK = !r.OK })
		assertDetects("error", orig, func(r *netpolResult) {
			if r.Error == "" {
				r.Error = string(netpol.ErrInvalidCIDR)
			} else {
				r.Error = ""
			}
		})
		assertDetects("chain_count+1", orig, func(r *netpolResult) { r.ChainCount++ })
		assertDetects("forward_rule_count+1", orig, func(r *netpolResult) { r.ForwardRuleCount++ })
		assertDetects("flip-nft_matches_spec", orig, func(r *netpolResult) { r.NftMatchesSpec = !r.NftMatchesSpec })
		assertDetects("nft_table", orig, func(r *netpolResult) { r.NftTable += "chain extra base=0 policy=-\n" })
		if n := len(orig.Verdicts); n > 0 {
			assertDetects("drop-last-verdict", orig, func(r *netpolResult) { r.Verdicts = r.Verdicts[:n-1] })
			assertDetects("append-verdict", orig, func(r *netpolResult) { r.Verdicts = append(r.Verdicts, "drop") })
			assertDetects("flip-verdict", orig, func(r *netpolResult) {
				if r.Verdicts[0] == "accept" {
					r.Verdicts[0] = "drop"
				} else {
					r.Verdicts[0] = "accept"
				}
			})
		}
	}

	for _, kind := range []string{
		"id+1", "flip-ok", "error", "chain_count+1", "forward_rule_count+1",
		"flip-nft_matches_spec", "nft_table", "drop-last-verdict", "append-verdict", "flip-verdict",
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

	invalidLines := []struct {
		name string
		line string
	}{
		{"unknown top-level field", string(withTopExtra)},
		{"missing id", `{"ok":true,"error":"","chain_count":1,"forward_rule_count":2,"verdicts":[],"nft_matches_spec":true,"nft_table":"table nayutar\n"}`},
		{"missing nft_matches_spec", `{"id":0,"ok":true,"error":"","chain_count":1,"forward_rule_count":2,"verdicts":[]}`},
		{"missing nft_table", `{"id":0,"ok":true,"error":"","chain_count":1,"forward_rule_count":2,"verdicts":[],"nft_matches_spec":true}`},
		{"ok=true with empty nft_table", `{"id":0,"ok":true,"error":"","chain_count":1,"forward_rule_count":2,"verdicts":[],"nft_matches_spec":true,"nft_table":""}`},
		{"null verdicts", `{"id":0,"ok":true,"error":"","chain_count":1,"forward_rule_count":2,"verdicts":null,"nft_matches_spec":true,"nft_table":"table nayutar\n"}`},
		{"negative chain_count", `{"id":0,"ok":true,"error":"","chain_count":-1,"forward_rule_count":2,"verdicts":[],"nft_matches_spec":true,"nft_table":"table nayutar\n"}`},
		{"negative forward_rule_count", `{"id":0,"ok":true,"error":"","chain_count":1,"forward_rule_count":-1,"verdicts":[],"nft_matches_spec":true,"nft_table":"table nayutar\n"}`},
		{"ok=true with error", `{"id":0,"ok":true,"error":"invalid_cidr","chain_count":1,"forward_rule_count":2,"verdicts":[],"nft_matches_spec":true,"nft_table":"table nayutar\n"}`},
		{"ok=false with unknown error", `{"id":0,"ok":false,"error":"bogus","chain_count":0,"forward_rule_count":0,"verdicts":[],"nft_matches_spec":true,"nft_table":"table nayutar\n"}`},
		{"unknown verdict", `{"id":0,"ok":true,"error":"","chain_count":1,"forward_rule_count":2,"verdicts":["allow"],"nft_matches_spec":true,"nft_table":"table nayutar\n"}`},
		{"trailing object", `{"id":0,"ok":true,"error":"","chain_count":1,"forward_rule_count":2,"verdicts":[],"nft_matches_spec":true,"nft_table":"table nayutar\n"}{"id":1}`},
	}
	for _, tc := range invalidLines {
		if _, err := decodeNetpolResult([]byte(tc.line)); err == nil {
			t.Errorf("decodeNetpolResult accepted %s: %s", tc.name, tc.line)
		}
	}

	validLine := `{"id":7,"ok":true,"error":"","chain_count":3,"forward_rule_count":5,"verdicts":["accept","drop"],"nft_matches_spec":true,"nft_table":"table nayutar\n"}`
	got, err := decodeNetpolResult([]byte(validLine))
	if err != nil {
		t.Fatalf("decodeNetpolResult(valid): %v", err)
	}
	want := netpolResult{
		ID:               7,
		OK:               true,
		Error:            "",
		ChainCount:       3,
		ForwardRuleCount: 5,
		Verdicts:         []string{"accept", "drop"},
		NftMatchesSpec:   true,
		NftTable:         "table nayutar\n",
	}
	if diff := compareNetpolResult(want, got); diff != "" {
		t.Fatalf("decodeNetpolResult(valid) differs from expected: %s", diff)
	}
}

func tenantChains(tbl *netpol.NftTable) []*netpol.NftChain {
	var out []*netpol.NftChain
	for i := range tbl.Chains {
		if !tbl.Chains[i].IsBase {
			out = append(out, &tbl.Chains[i])
		}
	}
	return out
}

func firstRule(tbl *netpol.NftTable, pred func(r netpol.NftRule) bool) *netpol.NftRule {
	for i := range tbl.Chains {
		for j := range tbl.Chains[i].Rules {
			if pred(tbl.Chains[i].Rules[j]) {
				return &tbl.Chains[i].Rules[j]
			}
		}
	}
	return nil
}

func dropTenantChainRule(tbl *netpol.NftTable) {
	for _, ch := range tenantChains(tbl) {
		if len(ch.Rules) >= 2 {
			ch.Rules = slices.Delete(slices.Clone(ch.Rules), 0, 1)
			return
		}
	}
}

func dropForwardRule(tbl *netpol.NftTable) {
	ch := &tbl.Chains[0]
	if len(ch.Rules) > 2 {
		ch.Rules = slices.Delete(slices.Clone(ch.Rules), len(ch.Rules)-1, len(ch.Rules))
	}
}

func swapChainRules(tbl *netpol.NftTable) {
	for i := range tbl.Chains {
		rules := tbl.Chains[i].Rules
		for j := 0; j+1 < len(rules); j++ {
			if renderNftRule(rules[j]) != renderNftRule(rules[j+1]) {
				rules[j], rules[j+1] = rules[j+1], rules[j]
				return
			}
		}
	}
}

func wrongJumpTarget(tbl *netpol.NftTable) {
	r := firstRule(tbl, func(r netpol.NftRule) bool { return r.Action.Kind == netpol.NftActionJump })
	if r == nil {
		return
	}
	for _, ch := range tbl.Chains[1:] {
		if ch.Name != r.Action.Target {
			r.Action.Target = ch.Name
			return
		}
	}
}

func acceptInsteadOfReturn(tbl *netpol.NftTable) {
	r := firstRule(tbl, func(r netpol.NftRule) bool { return r.Action.Kind == netpol.NftActionReturn })
	if r != nil {
		r.Action.Kind = netpol.NftActionAccept
	}
}

func widenPortBound(tbl *netpol.NftTable) {
	if r := firstRule(tbl, func(r netpol.NftRule) bool { return r.HasPorts }); r != nil {
		r.PortMax++
	}
}

func narrowPortBound(tbl *netpol.NftTable) {
	if r := firstRule(tbl, func(r netpol.NftRule) bool { return r.HasPorts }); r != nil {
		r.PortMin++
	}
}

func wrongRuleFamily(tbl *netpol.NftTable) {
	r := firstRule(tbl, func(r netpol.NftRule) bool { return r.HasDst })
	if r == nil {
		return
	}
	if r.DstPrefix.Family == netpol.FamilyIPv4 {
		r.DstPrefix.Family = netpol.FamilyIPv6
	} else {
		r.DstPrefix.Family = netpol.FamilyIPv4
	}
}

func swapChains(tbl *netpol.NftTable) {
	if len(tbl.Chains) >= 3 {
		tbl.Chains[1], tbl.Chains[2] = tbl.Chains[2], tbl.Chains[1]
	}
}

func acceptBasePolicy(tbl *netpol.NftTable) {
	tbl.Chains[0].DefaultPolicy = netpol.VerdictAccept
}

func dropProtocolMatch(tbl *netpol.NftTable) {
	if r := firstRule(tbl, func(r netpol.NftRule) bool { return r.Protocol != "" && r.Protocol != netpol.RuleProtoAny }); r != nil {
		r.Protocol = netpol.RuleProtoAny
	}
}

func TestNetpolDRTCatchesBrokenImplementations(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateNetpolCases(cfg.Seed, cfg.Cases)
	_, _, leanResults := leanNetpolResults(t, cases)

	variants := []struct {
		name  string
		hooks netpolHooks
	}{
		{"unilateral egress allows cross-tenant", netpolHooks{allowUnilateralEgress: true}},
		{"unilateral ingress allows cross-tenant", netpolHooks{allowUnilateralIngress: true}},
		{"accepts invalid conntrack state", netpolHooks{acceptInvalidConn: true}},
		{"drops established conntrack state", netpolHooks{dropEstablishedConn: true}},
		{"drops related conntrack state", netpolHooks{dropRelatedConn: true}},
		{"allows external-to-external transit", netpolHooks{allowExternalTransit: true}},
		{"skips port range check", netpolHooks{skipPortRangeCheck: true}},
		{"uses exclusive port upper bound", netpolHooks{portMaxExclusive: true}},
		{"skips L4 protocol check", netpolHooks{skipProtoCheck: true}},
		{"ignores intra-tenant deny mode", netpolHooks{ignoreIntraTenantDeny: true}},
		{"skips cross-tenant CIDR overlap check", netpolHooks{skipCrossCIDROverlap: true}},
		{"skips intra-tenant CIDR overlap check", netpolHooks{skipIntraCIDROverlap: true}},
		{"skips duplicate rule order check", netpolHooks{skipDupRuleOrder: true}},
		{"skips unknown peer tenant check", netpolHooks{skipUnknownPeerTenant: true}},
		{"skips invalid port range check", netpolHooks{skipInvalidPortRange: true}},
		{"nftables jump fallthrough diverges from spec", netpolHooks{wrongNftJumpFallthrough: true}},
		{"table drops a rule from a tenant chain", netpolHooks{corruptTable: dropTenantChainRule}},
		{"table drops a rule from the forward chain", netpolHooks{corruptTable: dropForwardRule}},
		{"table swaps two rules inside a chain", netpolHooks{corruptTable: swapChainRules}},
		{"table jumps to the wrong chain", netpolHooks{corruptTable: wrongJumpTarget}},
		{"table accepts where an egress rule returns", netpolHooks{corruptTable: acceptInsteadOfReturn}},
		{"table widens a port bound", netpolHooks{corruptTable: widenPortBound}},
		{"table narrows a port bound", netpolHooks{corruptTable: narrowPortBound}},
		{"table emits a rule in the wrong address family", netpolHooks{corruptTable: wrongRuleFamily}},
		{"table reorders chains", netpolHooks{corruptTable: swapChains}},
		{"table changes the base chain policy", netpolHooks{corruptTable: acceptBasePolicy}},
		{"table drops the protocol match", netpolHooks{corruptTable: dropProtocolMatch}},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			mismatches := 0
			for i, tc := range cases {
				goRes := runNetpolCase(tc, v.hooks)
				if compareNetpolResult(leanResults[i], goRes) != "" {
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

func TestGenerateNetpolCasesIsDeterministic(t *testing.T) {
	a := bytes.Join(encodeNetpolCases(t, generateNetpolCases(1, 100)), []byte("\n"))
	b := bytes.Join(encodeNetpolCases(t, generateNetpolCases(1, 100)), []byte("\n"))
	c := bytes.Join(encodeNetpolCases(t, generateNetpolCases(2, 100)), []byte("\n"))

	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different encoded cases")
	}
	if bytes.Equal(a, c) {
		t.Fatal("different seeds produced identical encoded cases")
	}
}

func TestNetpolModelRejectsMalformedCase(t *testing.T) {
	valid := `{"id":0,"bundle":{"tenants":[]},"packets":[]}`
	for _, bad := range []string{
		`{"id":1,"bundle":{"tenants":[{"tenant":"a","intra_tenant":"bogus","cidrs":[],"rules":[]}]},"packets":[]}`,
		`{"id":1,"bundle":{"tenants":[{"tenant":"a","intra_tenant":"allow","cidrs":[{"family":"ipx","hi":0,"lo":0,"bits":0}],"rules":[]}]},"packets":[]}`,
		`{"id":1,"bundle":{"tenants":[{"tenant":"a","intra_tenant":"allow","cidrs":[{"family":"ipv4","hi":0,"lo":0,"bits":-1}],"rules":[]}]},"packets":[]}`,
		`{"id":1,"bundle":{"tenants":[]},"packets":[{"family":"ipv4","src_hi":0,"src_lo":0,"dst_hi":0,"dst_lo":0,"protocol":"tcp","dst_port":80,"conn_state":"bogus"}]}`,
		`{"id":1,"bundle":{"tenants":[]},"packets":[{"family":"ipv4","src_hi":0,"src_lo":0,"dst_hi":0,"dst_lo":0,"protocol":"sctp","dst_port":80,"conn_state":"new"}]}`,
	} {
		cases := [][]byte{[]byte(valid), []byte(bad)}
		_, err := RunModel("netpol", cases)
		if err == nil {
			t.Errorf("RunModel accepted malformed case %s", bad)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte("line 2")) {
			t.Errorf("error %q does not name line 2 for %s", err.Error(), bad)
		}
	}
}
