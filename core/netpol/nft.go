package netpol

import (
	"strconv"
	"strings"
)

const (
	guardForwardChain = "guard_forward"
	guardInputChain   = "guard_input"
)

// NftActionKind identifies the terminal or control-flow action of an NftRule.
type NftActionKind string

const (
	NftActionAccept NftActionKind = "accept"
	NftActionDrop   NftActionKind = "drop"
	NftActionReturn NftActionKind = "return"
	NftActionJump   NftActionKind = "jump"
)

// NftAction is the verdict or chain jump taken when an NftRule matches.
type NftAction struct {
	Kind   NftActionKind
	Target string
}

// NftConnMatch matches netfilter conntrack states.
type NftConnMatch string

const (
	NftConnAny                NftConnMatch = ""
	NftConnInvalid            NftConnMatch = "invalid"
	NftConnEstablishedRelated NftConnMatch = "established_related"
)

// NftRule represents one ordered nftables rule in an NftChain.
type NftRule struct {
	ConnMatch NftConnMatch
	HasSrc    bool
	SrcPrefix Prefix
	HasDst    bool
	DstPrefix Prefix
	Protocol  RuleProto
	HasPorts  bool
	PortMin   int
	PortMax   int
	Action    NftAction
}

// NftChain represents either the base forward chain or a tenant regular chain.
type NftChain struct {
	Name          string
	IsBase        bool
	DefaultPolicy Verdict
	Rules         []NftRule
}

// NftTable is the compiled inet nftables table AST.
type NftTable struct {
	Name   string
	Chains []NftChain
}

// ChainCount returns the total number of chains in tbl.
func (tbl NftTable) ChainCount() int {
	return len(tbl.Chains)
}

// ForwardRuleCount returns the number of rules in the base forward chain.
func (tbl NftTable) ForwardRuleCount() int {
	for _, ch := range tbl.Chains {
		if ch.Name == "forward" {
			return len(ch.Rules)
		}
	}
	return 0
}

// CompileNft compiles cp into the 3-stage forward + per-tenant jump nftables table AST.
func CompileNft(cp *CompiledPolicy) NftTable {
	var tenants []TenantPolicy
	if cp != nil {
		tenants = cp.Tenants
	}

	totalCIDRs := 0
	for _, t := range tenants {
		totalCIDRs += len(t.CIDRs)
	}

	forwardRules := make([]NftRule, 0, 2+3*totalCIDRs)
	forwardRules = append(forwardRules,
		NftRule{ConnMatch: NftConnInvalid, Action: NftAction{Kind: NftActionDrop}},
		NftRule{ConnMatch: NftConnEstablishedRelated, Action: NftAction{Kind: NftActionAccept}},
	)
	// Stage 1: source tenant dispatch.
	for _, t := range tenants {
		target := TenantChainName(t.Tenant)
		for _, c := range t.CIDRs {
			forwardRules = append(forwardRules, NftRule{
				HasSrc:    true,
				SrcPrefix: c,
				Action:    NftAction{Kind: NftActionJump, Target: target},
			})
		}
	}
	// Stage 2: destination tenant dispatch.
	for _, t := range tenants {
		target := TenantInChainName(t.Tenant)
		for _, c := range t.CIDRs {
			forwardRules = append(forwardRules, NftRule{
				HasDst:    true,
				DstPrefix: c,
				Action:    NftAction{Kind: NftActionJump, Target: target},
			})
		}
	}
	// Stage 3: source tenant external-egress completion after Stage 1 return and Stage 2 miss.
	for _, t := range tenants {
		for _, c := range t.CIDRs {
			forwardRules = append(forwardRules, NftRule{
				HasSrc:    true,
				SrcPrefix: c,
				Action:    NftAction{Kind: NftActionAccept},
			})
		}
	}

	chains := make([]NftChain, 0, 1+2*len(tenants))
	chains = append(chains, NftChain{
		Name:          "forward",
		IsBase:        true,
		DefaultPolicy: VerdictDrop,
		Rules:         forwardRules,
	})

	for _, t := range tenants {
		var egressRules []NftRule
		if t.IntraTenant == IntraTenantAllow {
			for _, c := range t.CIDRs {
				egressRules = append(egressRules, NftRule{
					HasDst:    true,
					DstPrefix: c,
					Action:    NftAction{Kind: NftActionAccept},
				})
			}
		}
		for _, r := range t.Rules {
			if r.Direction != DirectionEgress {
				continue
			}
			for _, pfx := range expandPeerPrefixes(cp, r) {
				egressRules = append(egressRules, NftRule{
					HasDst:    true,
					DstPrefix: pfx,
					Protocol:  r.Protocol,
					HasPorts:  r.HasPorts,
					PortMin:   r.PortMin,
					PortMax:   r.PortMax,
					Action:    NftAction{Kind: NftActionReturn},
				})
			}
		}
		egressRules = append(egressRules, NftRule{Action: NftAction{Kind: NftActionDrop}})
		chains = append(chains, NftChain{
			Name:  TenantChainName(t.Tenant),
			Rules: egressRules,
		})

		var ingressRules []NftRule
		for _, r := range t.Rules {
			if r.Direction != DirectionIngress {
				continue
			}
			for _, pfx := range expandPeerPrefixes(cp, r) {
				ingressRules = append(ingressRules, NftRule{
					HasSrc:    true,
					SrcPrefix: pfx,
					Protocol:  r.Protocol,
					HasPorts:  r.HasPorts,
					PortMin:   r.PortMin,
					PortMax:   r.PortMax,
					Action:    NftAction{Kind: NftActionAccept},
				})
			}
		}
		ingressRules = append(ingressRules, NftRule{Action: NftAction{Kind: NftActionDrop}})
		chains = append(chains, NftChain{
			Name:  TenantInChainName(t.Tenant),
			Rules: ingressRules,
		})
	}

	return NftTable{
		Name:   "nayutar",
		Chains: chains,
	}
}

func expandPeerPrefixes(cp *CompiledPolicy, r Rule) []Prefix {
	switch r.PeerKind {
	case PeerKindCIDR:
		return []Prefix{r.PeerCIDR}
	case PeerKindTenant:
		if cp == nil {
			return nil
		}
		if pt := findTenantByName(cp, r.PeerTenant); pt != nil {
			return pt.CIDRs
		}
	}
	return nil
}

type chainOutcome int

const (
	outcomeContinue chainOutcome = iota
	outcomeAccept
	outcomeDrop
)

// EvalNft executes the compiled nftables chain machine on pkt.
func EvalNft(tbl NftTable, pkt Packet) Verdict {
	if !pkt.ConnState.known() {
		return VerdictDrop
	}
	forward := findChain(tbl, "forward")
	if forward == nil {
		return VerdictDrop
	}
	switch evalChain(tbl, forward, pkt, 0) {
	case outcomeAccept:
		return VerdictAccept
	case outcomeDrop:
		return VerdictDrop
	default:
		if forward.DefaultPolicy == VerdictAccept {
			return VerdictAccept
		}
		return VerdictDrop
	}
}

func findChain(tbl NftTable, name string) *NftChain {
	for i := range tbl.Chains {
		if tbl.Chains[i].Name == name {
			return &tbl.Chains[i]
		}
	}
	return nil
}

const maxNftJumpDepth = 16

func evalChain(tbl NftTable, ch *NftChain, pkt Packet, depth int) chainOutcome {
	if depth > maxNftJumpDepth {
		return outcomeDrop
	}
	for _, r := range ch.Rules {
		if !nftRuleMatches(r, pkt) {
			continue
		}
		switch r.Action.Kind {
		case NftActionAccept:
			return outcomeAccept
		case NftActionDrop:
			return outcomeDrop
		case NftActionReturn:
			return outcomeContinue
		case NftActionJump:
			target := findChain(tbl, r.Action.Target)
			if target == nil {
				return outcomeDrop
			}
			res := evalChain(tbl, target, pkt, depth+1)
			if res != outcomeContinue {
				return res
			}
		default:
			return outcomeDrop
		}
	}
	return outcomeContinue
}

func nftRuleMatches(r NftRule, pkt Packet) bool {
	switch r.ConnMatch {
	case NftConnAny:
	case NftConnInvalid:
		if pkt.ConnState != ConnStateInvalid {
			return false
		}
	case NftConnEstablishedRelated:
		if pkt.ConnState != ConnStateEstablished && pkt.ConnState != ConnStateRelated {
			return false
		}
	default:
		return false
	}
	if r.HasSrc && !r.SrcPrefix.Contains(pkt.Family, pkt.SrcHi, pkt.SrcLo) {
		return false
	}
	if r.HasDst && !r.DstPrefix.Contains(pkt.Family, pkt.DstHi, pkt.DstLo) {
		return false
	}
	if r.Protocol != "" && !matchesProto(r.Protocol, pkt.Protocol) {
		return false
	}
	if r.HasPorts {
		if pkt.DstPort < r.PortMin || pkt.DstPort > r.PortMax {
			return false
		}
	}
	return true
}

// RenderTeardownNft returns an idempotent atomic batch that deletes both tables owned by nayutar.
func RenderTeardownNft() string {
	return "table inet nayutar\ndelete table inet nayutar\ntable bridge nayutar\ndelete table bridge nayutar\n"
}

// RenderNft validates cp exactly as the Enforcer does and renders an atomic nft -f - replacement
// batch without flushing unrelated tables.
func RenderNft(cp *CompiledPolicy) (string, error) {
	verified, err := verifyDeployable(cp)
	if err != nil {
		return "", err
	}
	return renderVerified(verified), nil
}

func renderVerified(cp *CompiledPolicy) string {
	tbl := CompileNft(cp)
	var b strings.Builder
	b.WriteString("table inet nayutar\ndelete table inet nayutar\ntable bridge nayutar\ndelete table bridge nayutar\n")

	b.WriteString("table inet nayutar {\n")
	writeGuardForward(&b, cp)
	writeGuardInput(&b, cp)
	for _, ch := range tbl.Chains {
		writeChain(&b, ch, ch.Name == "forward")
	}
	b.WriteString("}\n")

	var denyBridges []string
	for _, t := range cp.Tenants {
		if t.IntraTenant == IntraTenantDeny {
			denyBridges = append(denyBridges, TenantBridgeName(t.Tenant))
		}
	}
	if len(denyBridges) > 0 {
		b.WriteString("table bridge nayutar {\n")
		for _, ch := range tbl.Chains {
			if ch.Name == "forward" {
				writeBridgeForward(&b, ch, denyBridges, cp)
				continue
			}
			writeChain(&b, ch, false)
		}
		b.WriteString("}\n")
	}
	return b.String()
}

func writeChain(b *strings.Builder, ch NftChain, base bool) {
	b.WriteString("  chain ")
	b.WriteString(ch.Name)
	b.WriteString(" {\n")
	if base {
		policy := ch.DefaultPolicy
		if policy == "" {
			policy = VerdictDrop
		}
		b.WriteString("    type filter hook forward priority 0; policy ")
		b.WriteString(string(policy))
		b.WriteString(";\n")
	}
	for _, r := range ch.Rules {
		b.WriteString("    ")
		writeNftRule(b, r)
		b.WriteByte('\n')
	}
	b.WriteString("  }\n")
}

func writeBridgeForward(b *strings.Builder, fwd NftChain, denyBridges []string, cp *CompiledPolicy) {
	quoted := make([]string, len(denyBridges))
	for i, n := range denyBridges {
		quoted[i] = `"` + n + `"`
	}
	b.WriteString("  chain forward {\n")
	b.WriteString("    type filter hook forward priority 0; policy accept;\n")
	b.WriteString("    meta ibrname != { " + strings.Join(quoted, ", ") + " } accept\n")
	b.WriteString("    ether type arp accept\n")
	b.WriteString("    icmpv6 type { nd-router-solicit, nd-router-advert } drop\n")
	b.WriteString("    icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert } accept\n")
	b.WriteString("    ether type != { ip, ip6 } drop\n")
	for _, t := range cp.Tenants {
		if t.IntraTenant != IntraTenantDeny {
			continue
		}
		br := `meta ibrname "` + TenantBridgeName(t.Tenant) + `" `
		for _, f := range families {
			set := prefixSet(t.CIDRs, f.fam)
			if set == "{  }" {
				b.WriteString("    " + br + "ether type " + f.ip + " drop\n")
				continue
			}
			b.WriteString("    " + br + f.ip + " saddr != " + set + " drop\n")
			b.WriteString("    " + br + f.ip + " daddr != " + set + " drop\n")
		}
	}
	for _, r := range fwd.Rules {
		// the unconditional per-source accepts only complete external egress at L3
		if r.Action.Kind == NftActionAccept && r.ConnMatch == NftConnAny {
			continue
		}
		b.WriteString("    ")
		writeNftRule(b, r)
		b.WriteByte('\n')
	}
	b.WriteString("    drop\n")
	b.WriteString("  }\n")
}

func prefixSet(prefixes []Prefix, fam Family) string {
	var parts []string
	for _, p := range prefixes {
		if p.Family == fam {
			parts = append(parts, p.String())
		}
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

var families = []struct {
	fam   Family
	ip    string
	proto string
	icmp  string
}{
	{FamilyIPv4, "ip", "ipv4", "icmp"},
	{FamilyIPv6, "ip6", "ipv6", "icmpv6"},
}

func writeGuardForward(b *strings.Builder, cp *CompiledPolicy) {
	b.WriteString("  chain " + guardForwardChain + " {\n")
	b.WriteString("    type filter hook forward priority -10; policy accept;\n")
	known := make([]string, len(cp.Tenants))
	for i, t := range cp.Tenants {
		known[i] = `"` + TenantBridgeName(t.Tenant) + `"`
	}
	for _, dir := range []string{"iifname", "oifname"} {
		wild := dir + ` "` + TenantBridgePrefix + `*"`
		if len(known) == 0 {
			b.WriteString("    " + wild + " drop\n")
			continue
		}
		b.WriteString("    " + wild + " " + dir + " != { " + strings.Join(known, ", ") + " } drop\n")
	}
	for _, t := range cp.Tenants {
		br := TenantBridgeName(t.Tenant)
		for _, f := range families {
			set := prefixSet(t.CIDRs, f.fam)
			if set == "{  }" {
				b.WriteString("    iifname \"" + br + "\" meta nfproto " + f.proto + " drop\n")
				continue
			}
			b.WriteString("    iifname \"" + br + "\" " + f.ip + " saddr != " + set + " drop\n")
			b.WriteString("    iifname != \"" + br + "\" " + f.ip + " saddr " + set + " drop\n")
			b.WriteString("    oifname != \"" + br + "\" " + f.ip + " daddr " + set + " drop\n")
		}
	}
	b.WriteString("  }\n")
}

func writeGuardInput(b *strings.Builder, cp *CompiledPolicy) {
	in := `iifname "` + TenantBridgePrefix + `*"`
	b.WriteString("  chain " + guardInputChain + " {\n")
	b.WriteString("    type filter hook input priority 0; policy accept;\n")
	b.WriteString("    " + in + " icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-solicit } accept\n")
	for _, t := range cp.Tenants {
		br := TenantBridgeName(t.Tenant)
		for _, f := range families {
			set := prefixSet(t.CIDRs, f.fam)
			if set == "{  }" {
				b.WriteString("    iifname \"" + br + "\" meta nfproto " + f.proto + " drop\n")
				continue
			}
			b.WriteString("    iifname \"" + br + "\" " + f.ip + " saddr != " + set + " drop\n")
		}
	}
	for _, t := range cp.Tenants {
		br := TenantBridgeName(t.Tenant)
		for _, f := range families {
			set := prefixSet(t.CIDRs, f.fam)
			if set == "{  }" {
				continue
			}
			b.WriteString("    iifname != { \"" + br + "\", \"lo\" } " + f.ip + " saddr " + set + " drop\n")
		}
	}
	b.WriteString("    " + in + " ct state { established, related } accept\n")
	for _, t := range cp.Tenants {
		br := `iifname "` + TenantBridgeName(t.Tenant) + `"`
		for _, f := range families {
			set := prefixSet(t.CIDRs, f.fam)
			if set == "{  }" {
				continue
			}
			b.WriteString("    " + br + " " + f.ip + " daddr " + set + " " + f.icmp + " type echo-request accept\n")
		}
	}
	b.WriteString("    " + in + " drop\n")
	b.WriteString("  }\n")
}

func writeNftRule(b *strings.Builder, r NftRule) {
	first := true
	writePart := func(s string) {
		if s == "" {
			return
		}
		if !first {
			b.WriteByte(' ')
		}
		first = false
		b.WriteString(s)
	}

	switch r.ConnMatch {
	case NftConnInvalid:
		writePart("ct state invalid")
	case NftConnEstablishedRelated:
		writePart("ct state { established, related }")
	}

	fam := FamilyIPv4
	if r.HasSrc {
		fam = r.SrcPrefix.Family
		if fam == FamilyIPv6 {
			writePart("ip6 saddr " + r.SrcPrefix.String())
		} else {
			writePart("ip saddr " + r.SrcPrefix.String())
		}
	}
	if r.HasDst {
		fam = r.DstPrefix.Family
		if fam == FamilyIPv6 {
			writePart("ip6 daddr " + r.DstPrefix.String())
		} else {
			writePart("ip daddr " + r.DstPrefix.String())
		}
	}

	action := r.Action
	if r.HasPorts {
		portExpr := strconv.Itoa(r.PortMin)
		if r.PortMin != r.PortMax {
			portExpr += "-" + strconv.Itoa(r.PortMax)
		}
		switch r.Protocol {
		case RuleProtoTCP:
			writePart("tcp dport " + portExpr)
		case RuleProtoUDP:
			writePart("udp dport " + portExpr)
		default:
			action = NftAction{Kind: NftActionDrop}
		}
	} else {
		switch r.Protocol {
		case "", RuleProtoAny:
		case RuleProtoTCP:
			writePart("meta l4proto tcp")
		case RuleProtoUDP:
			writePart("meta l4proto udp")
		case RuleProtoICMP:
			if fam == FamilyIPv6 {
				writePart("meta l4proto ipv6-icmp")
			} else {
				writePart("meta l4proto icmp")
			}
		default:
			action = NftAction{Kind: NftActionDrop}
		}
	}

	switch action.Kind {
	case NftActionAccept:
		writePart("accept")
	case NftActionDrop:
		writePart("drop")
	case NftActionReturn:
		writePart("return")
	case NftActionJump:
		writePart("jump " + action.Target)
	default:
		writePart("drop")
	}
}
