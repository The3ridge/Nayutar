package netpol_test

import (
	"strings"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/netpol"
)

func FuzzParsePrefix(f *testing.F) {
	for _, seed := range []string{
		"0.0.0.0/0",
		"10.40.0.0/24",
		"10.40.0.1/24",
		"192.168.1.1/32",
		"::/0",
		"fd00:40::/64",
		"fd00:40::1/64",
		"fd00:40::1/128",
		"not-a-cidr",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		p, err := netpol.ParsePrefix(raw)
		if err != nil {
			return
		}
		if !p.Valid() {
			t.Fatalf("ParsePrefix(%q) returned invalid prefix %+v", raw, p)
		}
		s := p.String()
		p2, err := netpol.ParsePrefix(s)
		if err != nil {
			t.Fatalf("ParsePrefix(String(%q)=%q) failed: %v", raw, s, err)
		}
		if p != p2 {
			t.Fatalf("round-trip mismatch: %+v != %+v", p, p2)
		}
		if !p.Contains(p.Family, p.Hi, p.Lo) {
			t.Fatalf("prefix %s does not contain its own network address", s)
		}
	})
}

func FuzzCompileAndEvalEquivalence(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{255, 128, 64, 32, 16, 8, 4, 2, 1})

	v4CIDRs := []string{"10.1.0.0/24", "10.2.0.0/24", "10.3.0.0/24"}
	v6CIDRs := []string{"fd00:1::/64", "fd00:2::/64", "fd00:3::/64"}
	names := []string{"alpha", "beta", "gamma"}
	rProtos := []netpol.RuleProto{netpol.RuleProtoAny, netpol.RuleProtoTCP, netpol.RuleProtoUDP, netpol.RuleProtoICMP}
	pProtos := []netpol.PacketProto{netpol.PacketProtoTCP, netpol.PacketProtoUDP, netpol.PacketProtoICMP, netpol.PacketProtoOther}
	states := []netpol.ConnState{netpol.ConnStateNew, netpol.ConnStateEstablished, netpol.ConnStateRelated, netpol.ConnStateInvalid}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 4 {
			return
		}
		pos := 0
		nextByte := func() byte {
			b := data[pos%len(data)]
			pos++
			return b
		}

		nTenants := int(nextByte()%3) + 1
		tenants := make([]netpol.TenantPolicy, nTenants)
		for i := range nTenants {
			mode := netpol.IntraTenantAllow
			if nextByte()%2 == 1 {
				mode = netpol.IntraTenantDeny
			}
			c4, _ := netpol.ParsePrefix(v4CIDRs[i])
			cidrs := []netpol.Prefix{c4}
			if nextByte()%2 == 1 {
				c6, _ := netpol.ParsePrefix(v6CIDRs[i])
				cidrs = append(cidrs, c6)
			}
			nRules := int(nextByte() % 4)
			rules := make([]netpol.Rule, 0, nRules)
			for r := range nRules {
				dir := netpol.DirectionEgress
				if nextByte()%2 == 1 {
					dir = netpol.DirectionIngress
				}
				proto := rProtos[int(nextByte())%len(rProtos)]
				rule := netpol.Rule{
					Order:     int64(r + 1),
					Direction: dir,
					Protocol:  proto,
				}
				if nextByte()%2 == 0 {
					rule.PeerKind = netpol.PeerKindTenant
					rule.PeerTenant = names[int(nextByte())%nTenants]
				} else {
					rule.PeerKind = netpol.PeerKindCIDR
					if nextByte()%2 == 0 {
						rule.PeerCIDR, _ = netpol.ParsePrefix("0.0.0.0/0")
					} else {
						rule.PeerCIDR, _ = netpol.ParsePrefix("::/0")
					}
				}
				if (proto == netpol.RuleProtoTCP || proto == netpol.RuleProtoUDP) && nextByte()%2 == 1 {
					pMin := int(nextByte()) + 1
					pMax := pMin + int(nextByte()%16)
					rule.HasPorts = true
					rule.PortMin = pMin
					rule.PortMax = pMax
				}
				rules = append(rules, rule)
			}
			tenants[i] = netpol.TenantPolicy{
				Tenant:      names[i],
				IntraTenant: mode,
				CIDRs:       cidrs,
				Rules:       rules,
			}
		}

		cp, cerr := netpol.Compile(netpol.PolicyBundle{Tenants: tenants})
		if cerr != nil {
			return
		}
		tbl := netpol.CompileNft(cp)
		script, rerr := netpol.RenderNft(cp)
		if rerr != nil {
			return
		}
		if strings.Contains(script, "flush ruleset") {
			t.Fatalf("RenderNft emitted flush ruleset")
		}

		for range 8 {
			var pkt netpol.Packet
			pkt.ConnState = states[int(nextByte())%len(states)]
			pkt.Protocol = pProtos[int(nextByte())%len(pProtos)]
			pkt.DstPort = int(nextByte()) + 1
			if nextByte()%2 == 0 {
				pkt.Family = netpol.FamilyIPv4
				srcIdx := int(nextByte()) % (nTenants + 1)
				dstIdx := int(nextByte()) % (nTenants + 1)
				if srcIdx < nTenants {
					p, _ := netpol.ParsePrefix(v4CIDRs[srcIdx])
					pkt.SrcLo = p.Lo + uint64(nextByte()%10+1)
				} else {
					pkt.SrcLo = 0xc6336401 // 198.51.100.1
				}
				if dstIdx < nTenants {
					p, _ := netpol.ParsePrefix(v4CIDRs[dstIdx])
					pkt.DstLo = p.Lo + uint64(nextByte()%10+1)
				} else {
					pkt.DstLo = 0xc6336402 // 198.51.100.2
				}
			} else {
				pkt.Family = netpol.FamilyIPv6
				srcIdx := int(nextByte()) % (nTenants + 1)
				dstIdx := int(nextByte()) % (nTenants + 1)
				if srcIdx < nTenants {
					p, _ := netpol.ParsePrefix(v6CIDRs[srcIdx])
					pkt.SrcHi = p.Hi
					pkt.SrcLo = uint64(nextByte()%10 + 1)
				} else {
					pkt.SrcHi = 0x20010db800000000
					pkt.SrcLo = 1
				}
				if dstIdx < nTenants {
					p, _ := netpol.ParsePrefix(v6CIDRs[dstIdx])
					pkt.DstHi = p.Hi
					pkt.DstLo = uint64(nextByte()%10 + 1)
				} else {
					pkt.DstHi = 0x20010db800000000
					pkt.DstLo = 2
				}
			}
			if gotEval, gotNft := netpol.Evaluate(cp, pkt), netpol.EvalNft(tbl, pkt); gotEval != gotNft {
				t.Fatalf("Evaluate=%q != EvalNft=%q for pkt=%+v", gotEval, gotNft, pkt)
			}
		}
	})
}
