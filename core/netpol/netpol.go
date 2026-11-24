package netpol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

// IntraTenantMode controls default traffic handling between endpoints within the same tenant.
type IntraTenantMode string

const (
	IntraTenantAllow IntraTenantMode = "allow"
	IntraTenantDeny  IntraTenantMode = "deny"
)

// Direction identifies whether a policy rule governs ingress or egress traffic.
type Direction string

const (
	DirectionIngress Direction = "ingress"
	DirectionEgress  Direction = "egress"
)

// PeerKind identifies whether a rule targets another tenant or an explicit CIDR prefix.
type PeerKind string

const (
	PeerKindTenant PeerKind = "tenant"
	PeerKindCIDR   PeerKind = "cidr"
)

// RuleProto identifies the L4 protocol selector on a policy rule.
type RuleProto string

const (
	RuleProtoAny  RuleProto = "any"
	RuleProtoTCP  RuleProto = "tcp"
	RuleProtoUDP  RuleProto = "udp"
	RuleProtoICMP RuleProto = "icmp"
)

// PacketProto identifies the L4 protocol of an evaluated packet.
type PacketProto string

const (
	PacketProtoTCP   PacketProto = "tcp"
	PacketProtoUDP   PacketProto = "udp"
	PacketProtoICMP  PacketProto = "icmp"
	PacketProtoOther PacketProto = "other"
)

// ConnState identifies the netfilter conntrack state of a packet.
type ConnState string

const (
	ConnStateNew         ConnState = "new"
	ConnStateEstablished ConnState = "established"
	ConnStateRelated     ConnState = "related"
	ConnStateInvalid     ConnState = "invalid"
)

func (c ConnState) known() bool {
	switch c {
	case ConnStateNew, ConnStateEstablished, ConnStateRelated, ConnStateInvalid:
		return true
	}
	return false
}

// Verdict is the packet decision produced by Evaluate and EvalNft.
type Verdict string

const (
	VerdictAccept Verdict = "accept"
	VerdictDrop   Verdict = "drop"
)

// Rule is one ordered allow rule within a TenantPolicy.
type Rule struct {
	Order      int64     `json:"order"`
	Direction  Direction `json:"direction"`
	PeerKind   PeerKind  `json:"peer_kind"`
	PeerTenant string    `json:"peer_tenant"`
	PeerCIDR   Prefix    `json:"peer_cidr"`
	Protocol   RuleProto `json:"protocol"`
	HasPorts   bool      `json:"has_ports"`
	PortMin    int       `json:"port_min"`
	PortMax    int       `json:"port_max"`
}

// TenantPolicy describes one tenant's non-overlapping CIDRs, intra-tenant mode, and allow rules.
type TenantPolicy struct {
	Tenant      string          `json:"tenant"`
	IntraTenant IntraTenantMode `json:"intra_tenant"`
	CIDRs       []Prefix        `json:"cidrs"`
	Rules       []Rule          `json:"rules"`
}

// PolicyBundle is the unvalidated policy input across all tenants on a node.
type PolicyBundle struct {
	Tenants []TenantPolicy `json:"tenants"`
}

// CompiledPolicy is a validated, canonically ordered policy bundle.
type CompiledPolicy struct {
	Tenants []TenantPolicy
}

// Clone returns a deep copy of cp so callers cannot mutate compiled state in place.
func (cp *CompiledPolicy) Clone() *CompiledPolicy {
	if cp == nil {
		return nil
	}
	tenants := make([]TenantPolicy, len(cp.Tenants))
	for i, t := range cp.Tenants {
		tenants[i] = TenantPolicy{
			Tenant:      t.Tenant,
			IntraTenant: t.IntraTenant,
			CIDRs:       slices.Clone(t.CIDRs),
			Rules:       slices.Clone(t.Rules),
		}
	}
	return &CompiledPolicy{Tenants: tenants}
}

// ValidateUnitSubnets verifies that tenant is declared in cp and every subnet CIDR lies within tenant's CIDRs.
func (cp *CompiledPolicy) ValidateUnitSubnets(tenant string, subnets []string) error {
	if cp == nil {
		return errors.New("netpol: compiled policy is nil")
	}
	tp := findTenantByName(cp, tenant)
	if tp == nil {
		return fmt.Errorf("netpol: unit tenant %q is not declared in policy bundle", tenant)
	}
	for _, raw := range subnets {
		sub, err := ParsePrefix(raw)
		if err != nil {
			return fmt.Errorf("netpol: invalid unit subnet %q: %w", raw, err)
		}
		covered := false
		for _, tc := range tp.CIDRs {
			if tc.ContainsPrefix(sub) {
				covered = true
				break
			}
		}
		if !covered {
			return fmt.Errorf("netpol: unit subnet %s is outside tenant %q policy CIDRs", sub.String(), tenant)
		}
	}
	return nil
}

// CompileErrorCode classifies PolicyBundle validation failures.
type CompileErrorCode string

const (
	ErrInvalidTenantName      CompileErrorCode = "invalid_tenant_name"
	ErrDuplicateTenant        CompileErrorCode = "duplicate_tenant"
	ErrEmptyTenantCIDRs       CompileErrorCode = "empty_tenant_cidrs"
	ErrInvalidCIDR            CompileErrorCode = "invalid_cidr"
	ErrDuplicateTenantCIDR    CompileErrorCode = "duplicate_tenant_cidr"
	ErrOverlappingTenantCIDRs CompileErrorCode = "overlapping_tenant_cidrs"
	ErrDuplicateRuleOrder     CompileErrorCode = "duplicate_rule_order"
	ErrUnknownPeerTenant      CompileErrorCode = "unknown_peer_tenant"
	ErrInvalidPortRange       CompileErrorCode = "invalid_port_range"
	ErrInvalidIntraTenantMode CompileErrorCode = "invalid_intra_tenant_mode"
	ErrInvalidDirection       CompileErrorCode = "invalid_direction"
	ErrInvalidProtocol        CompileErrorCode = "invalid_protocol"
	ErrInvalidPeerKind        CompileErrorCode = "invalid_peer_kind"
)

// CompileError reports a PolicyBundle validation error with its machine-readable code.
type CompileError struct {
	Code    CompileErrorCode
	Message string
}

func (e *CompileError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return "netpol: " + string(e.Code)
	}
	return fmt.Sprintf("netpol: %s: %s", e.Code, e.Message)
}

// Packet represents the L3/L4/conntrack fields evaluated against a policy.
type Packet struct {
	Family    Family      `json:"family"`
	SrcHi     uint64      `json:"src_hi"`
	SrcLo     uint64      `json:"src_lo"`
	DstHi     uint64      `json:"dst_hi"`
	DstLo     uint64      `json:"dst_lo"`
	Protocol  PacketProto `json:"protocol"`
	DstPort   int         `json:"dst_port"`
	ConnState ConnState   `json:"conn_state"`
}

// TenantChainName returns the egress chain name for tenant.
func TenantChainName(tenant string) string {
	return "tenant_" + tenant
}

// TenantInChainName returns the ingress chain name for tenant.
func TenantInChainName(tenant string) string {
	return "tenant_" + tenant + "_in"
}

// TenantBridgeName derives a deterministic 15-byte Linux bridge name for tenant.
func TenantBridgeName(tenant string) string {
	sum := sha256.Sum256([]byte(tenant))
	return TenantBridgePrefix + hex.EncodeToString(sum[:])[:11]
}

// Compile validates b in canonical 4-pass order and returns a canonical CompiledPolicy.
func Compile(b PolicyBundle) (*CompiledPolicy, *CompileError) {
	seenTenants := make(map[string]struct{}, len(b.Tenants))
	tenants := make([]TenantPolicy, 0, len(b.Tenants))

	// Pass 1: tenant names, duplicates, non-empty canonical CIDRs, and intra-tenant CIDR overlap.
	for _, t := range b.Tenants {
		if err := keyspace.ValidateTenant(t.Tenant); err != nil {
			return nil, &CompileError{
				Code:    ErrInvalidTenantName,
				Message: fmt.Sprintf("tenant %q: %v", t.Tenant, err),
			}
		}
		if _, dup := seenTenants[t.Tenant]; dup {
			return nil, &CompileError{
				Code:    ErrDuplicateTenant,
				Message: fmt.Sprintf("duplicate tenant %q", t.Tenant),
			}
		}
		seenTenants[t.Tenant] = struct{}{}

		if len(t.CIDRs) == 0 {
			return nil, &CompileError{
				Code:    ErrEmptyTenantCIDRs,
				Message: fmt.Sprintf("tenant %q must declare at least one CIDR", t.Tenant),
			}
		}
		cidrs := make([]Prefix, 0, len(t.CIDRs))
		for _, c := range t.CIDRs {
			if !c.Valid() {
				return nil, &CompileError{
					Code:    ErrInvalidCIDR,
					Message: fmt.Sprintf("tenant %q has invalid CIDR (%s,%d,%d/%d)", t.Tenant, c.Family, c.Hi, c.Lo, c.Bits),
				}
			}
			for _, prev := range cidrs {
				if PrefixesOverlap(prev, c) {
					return nil, &CompileError{
						Code:    ErrDuplicateTenantCIDR,
						Message: fmt.Sprintf("tenant %q has overlapping CIDRs %s and %s", t.Tenant, prev.String(), c.String()),
					}
				}
			}
			cidrs = append(cidrs, c)
		}

		intra := t.IntraTenant
		switch intra {
		case "":
			intra = IntraTenantAllow
		case IntraTenantAllow, IntraTenantDeny:
		default:
			return nil, &CompileError{
				Code:    ErrInvalidIntraTenantMode,
				Message: fmt.Sprintf("tenant %q has invalid intra-tenant mode %q", t.Tenant, t.IntraTenant),
			}
		}
		tenants = append(tenants, TenantPolicy{
			Tenant:      t.Tenant,
			IntraTenant: intra,
			CIDRs:       cidrs,
			Rules:       slices.Clone(t.Rules),
		})
	}

	// Pass 2: canonical byte-lexicographical tenant ordering.
	slices.SortFunc(tenants, func(a, b TenantPolicy) int {
		return strings.Compare(a.Tenant, b.Tenant)
	})

	// Pass 3: cross-tenant pairwise CIDR disjointness, then canonical per-tenant CIDR sort.
	for i := range tenants {
		for j := i + 1; j < len(tenants); j++ {
			for _, ci := range tenants[i].CIDRs {
				for _, cj := range tenants[j].CIDRs {
					if PrefixesOverlap(ci, cj) {
						return nil, &CompileError{
							Code: ErrOverlappingTenantCIDRs,
							Message: fmt.Sprintf("tenants %q (%s) and %q (%s) have overlapping CIDRs",
								tenants[i].Tenant, ci.String(), tenants[j].Tenant, cj.String()),
						}
					}
				}
			}
		}
	}
	for i := range tenants {
		slices.SortFunc(tenants[i].CIDRs, comparePrefix)
	}

	// Pass 4: per-tenant rule order uniqueness, ascending sort, peer and port validation.
	for i := range tenants {
		t := &tenants[i]
		seenOrders := make(map[int64]struct{}, len(t.Rules))
		for _, r := range t.Rules {
			if _, dup := seenOrders[r.Order]; dup {
				return nil, &CompileError{
					Code:    ErrDuplicateRuleOrder,
					Message: fmt.Sprintf("tenant %q has duplicate rule order %d", t.Tenant, r.Order),
				}
			}
			seenOrders[r.Order] = struct{}{}
		}
		slices.SortFunc(t.Rules, func(a, b Rule) int {
			if a.Order < b.Order {
				return -1
			}
			if a.Order > b.Order {
				return 1
			}
			return 0
		})
		for j := range t.Rules {
			r := &t.Rules[j]
			if r.Direction != DirectionIngress && r.Direction != DirectionEgress {
				return nil, &CompileError{
					Code:    ErrInvalidDirection,
					Message: fmt.Sprintf("tenant %q rule order %d has invalid direction %q", t.Tenant, r.Order, r.Direction),
				}
			}
			if r.Protocol == "" {
				r.Protocol = RuleProtoAny
			}
			switch r.Protocol {
			case RuleProtoAny, RuleProtoTCP, RuleProtoUDP, RuleProtoICMP:
			default:
				return nil, &CompileError{
					Code:    ErrInvalidProtocol,
					Message: fmt.Sprintf("tenant %q rule order %d has invalid protocol %q", t.Tenant, r.Order, r.Protocol),
				}
			}
			switch r.PeerKind {
			case PeerKindTenant:
				if _, ok := seenTenants[r.PeerTenant]; !ok {
					return nil, &CompileError{
						Code:    ErrUnknownPeerTenant,
						Message: fmt.Sprintf("tenant %q rule order %d references unknown peer tenant %q", t.Tenant, r.Order, r.PeerTenant),
					}
				}
			case PeerKindCIDR:
				if !r.PeerCIDR.Valid() {
					return nil, &CompileError{
						Code:    ErrInvalidCIDR,
						Message: fmt.Sprintf("tenant %q rule order %d has invalid peer CIDR", t.Tenant, r.Order),
					}
				}
			default:
				return nil, &CompileError{
					Code:    ErrInvalidPeerKind,
					Message: fmt.Sprintf("tenant %q rule order %d has unknown peer kind %q", t.Tenant, r.Order, r.PeerKind),
				}
			}
			if r.HasPorts {
				if (r.Protocol != RuleProtoTCP && r.Protocol != RuleProtoUDP) ||
					r.PortMin < 1 || r.PortMin > r.PortMax || r.PortMax > 65535 {
					return nil, &CompileError{
						Code:    ErrInvalidPortRange,
						Message: fmt.Sprintf("tenant %q rule order %d has invalid port range %d-%d for protocol %q", t.Tenant, r.Order, r.PortMin, r.PortMax, r.Protocol),
					}
				}
			}
		}
	}

	return &CompiledPolicy{Tenants: tenants}, nil
}

// Evaluate decides whether pkt is accepted or dropped under cp using declarative mutual-allow semantics.
func Evaluate(cp *CompiledPolicy, pkt Packet) Verdict {
	if cp == nil {
		return VerdictDrop
	}
	if !pkt.ConnState.known() {
		return VerdictDrop
	}
	switch pkt.ConnState {
	case ConnStateInvalid:
		return VerdictDrop
	case ConnStateEstablished, ConnStateRelated:
		return VerdictAccept
	}

	srcT := findTenantForAddr(cp, pkt.Family, pkt.SrcHi, pkt.SrcLo)
	dstT := findTenantForAddr(cp, pkt.Family, pkt.DstHi, pkt.DstLo)

	switch {
	case srcT != nil && dstT != nil:
		if srcT.Tenant == dstT.Tenant && srcT.IntraTenant == IntraTenantAllow {
			return VerdictAccept
		}
		if matchesTenantRules(cp, srcT, DirectionEgress, pkt) && matchesTenantRules(cp, dstT, DirectionIngress, pkt) {
			return VerdictAccept
		}
		return VerdictDrop
	case srcT != nil && dstT == nil:
		if matchesTenantRules(cp, srcT, DirectionEgress, pkt) {
			return VerdictAccept
		}
		return VerdictDrop
	case srcT == nil && dstT != nil:
		if matchesTenantRules(cp, dstT, DirectionIngress, pkt) {
			return VerdictAccept
		}
		return VerdictDrop
	default:
		return VerdictDrop
	}
}

func findTenantForAddr(cp *CompiledPolicy, fam Family, hi, lo uint64) *TenantPolicy {
	for i := range cp.Tenants {
		for _, c := range cp.Tenants[i].CIDRs {
			if c.Contains(fam, hi, lo) {
				return &cp.Tenants[i]
			}
		}
	}
	return nil
}

func findTenantByName(cp *CompiledPolicy, name string) *TenantPolicy {
	for i := range cp.Tenants {
		if cp.Tenants[i].Tenant == name {
			return &cp.Tenants[i]
		}
	}
	return nil
}

func matchesTenantRules(cp *CompiledPolicy, t *TenantPolicy, dir Direction, pkt Packet) bool {
	for _, r := range t.Rules {
		if matchesRule(cp, r, dir, pkt) {
			return true
		}
	}
	return false
}

func matchesRule(cp *CompiledPolicy, r Rule, dir Direction, pkt Packet) bool {
	if r.Direction != dir {
		return false
	}
	var peerHi, peerLo uint64
	if dir == DirectionEgress {
		peerHi, peerLo = pkt.DstHi, pkt.DstLo
	} else {
		peerHi, peerLo = pkt.SrcHi, pkt.SrcLo
	}

	peerMatched := false
	switch r.PeerKind {
	case PeerKindCIDR:
		peerMatched = r.PeerCIDR.Contains(pkt.Family, peerHi, peerLo)
	case PeerKindTenant:
		if pt := findTenantByName(cp, r.PeerTenant); pt != nil {
			for _, c := range pt.CIDRs {
				if c.Contains(pkt.Family, peerHi, peerLo) {
					peerMatched = true
					break
				}
			}
		}
	}
	if !peerMatched {
		return false
	}

	if !matchesProto(r.Protocol, pkt.Protocol) {
		return false
	}
	if r.HasPorts {
		if pkt.DstPort < r.PortMin || pkt.DstPort > r.PortMax {
			return false
		}
	}
	return true
}

func matchesProto(rp RuleProto, pp PacketProto) bool {
	switch rp {
	case RuleProtoAny, "":
		return true
	case RuleProtoTCP:
		return pp == PacketProtoTCP
	case RuleProtoUDP:
		return pp == PacketProtoUDP
	case RuleProtoICMP:
		return pp == PacketProtoICMP
	default:
		return false
	}
}
