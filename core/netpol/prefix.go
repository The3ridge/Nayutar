// Package netpol implements default-deny network policy validation, evaluation,
// nftables AST compilation and rendering, LKAL v0.2 schemas, and atomic nft enforcement.
package netpol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

// Family identifies an IP address family.
type Family string

const (
	FamilyIPv4 Family = "ipv4"
	FamilyIPv6 Family = "ipv6"
)

// Prefix represents a canonical IPv4 or IPv6 CIDR prefix with zero host bits.
type Prefix struct {
	Family Family `json:"family"`
	Hi     uint64 `json:"hi"`
	Lo     uint64 `json:"lo"`
	Bits   int    `json:"bits"`
}

// Valid reports whether p is a canonical CIDR prefix with valid width and zero host bits.
func (p Prefix) Valid() bool {
	switch p.Family {
	case FamilyIPv4:
		if p.Hi != 0 || p.Lo >= (uint64(1)<<32) || p.Bits < 0 || p.Bits > 32 {
			return false
		}
		if p.Bits == 0 {
			return p.Lo == 0
		}
		if p.Bits == 32 {
			return true
		}
		hostMask := (uint64(1) << uint(32-p.Bits)) - 1
		return (p.Lo & hostMask) == 0
	case FamilyIPv6:
		if p.Bits < 0 || p.Bits > 128 {
			return false
		}
		switch {
		case p.Bits == 0:
			return p.Hi == 0 && p.Lo == 0
		case p.Bits < 64:
			if p.Lo != 0 {
				return false
			}
			hiMask := (uint64(1) << uint(64-p.Bits)) - 1
			return (p.Hi & hiMask) == 0
		case p.Bits == 64:
			return p.Lo == 0
		case p.Bits < 128:
			loMask := (uint64(1) << uint(128-p.Bits)) - 1
			return (p.Lo & loMask) == 0
		default:
			return true
		}
	default:
		return false
	}
}

func (p Prefix) endAddr() (uint64, uint64) {
	if p.Family == FamilyIPv4 {
		hostMask := (uint64(1) << uint(32-p.Bits)) - 1
		return 0, p.Lo | hostMask
	}
	switch {
	case p.Bits == 0:
		return ^uint64(0), ^uint64(0)
	case p.Bits < 64:
		hiMask := (uint64(1) << uint(64-p.Bits)) - 1
		return p.Hi | hiMask, ^uint64(0)
	case p.Bits == 64:
		return p.Hi, ^uint64(0)
	default:
		loMask := (uint64(1) << uint(128-p.Bits)) - 1
		return p.Hi, p.Lo | loMask
	}
}

func cmp128(aHi, aLo, bHi, bLo uint64) int {
	if aHi < bHi {
		return -1
	}
	if aHi > bHi {
		return 1
	}
	if aLo < bLo {
		return -1
	}
	if aLo > bLo {
		return 1
	}
	return 0
}

// Contains reports whether the address (fam, hi, lo) lies within canonical prefix p.
func (p Prefix) Contains(fam Family, hi, lo uint64) bool {
	if !p.Valid() || p.Family != fam {
		return false
	}
	if p.Family == FamilyIPv4 && (hi != 0 || lo >= (uint64(1)<<32)) {
		return false
	}
	endHi, endLo := p.endAddr()
	return cmp128(hi, lo, p.Hi, p.Lo) >= 0 && cmp128(hi, lo, endHi, endLo) <= 0
}

// ContainsPrefix reports whether canonical prefix sub is wholly contained within canonical prefix p.
func (p Prefix) ContainsPrefix(sub Prefix) bool {
	if !p.Valid() || !sub.Valid() || p.Family != sub.Family || p.Bits > sub.Bits {
		return false
	}
	return p.Contains(sub.Family, sub.Hi, sub.Lo)
}

// PrefixesOverlap reports whether two canonical prefixes share at least one address.
func PrefixesOverlap(a, b Prefix) bool {
	if a.Family != b.Family || !a.Valid() || !b.Valid() {
		return false
	}
	aEndHi, aEndLo := a.endAddr()
	bEndHi, bEndLo := b.endAddr()
	return cmp128(a.Hi, a.Lo, bEndHi, bEndLo) <= 0 && cmp128(b.Hi, b.Lo, aEndHi, aEndLo) <= 0
}

// PrefixFromNetip converts a canonical netip.Prefix into a Prefix, rejecting non-zero host bits.
func PrefixFromNetip(p netip.Prefix) (Prefix, error) {
	if !p.IsValid() {
		return Prefix{}, errors.New("netpol: invalid CIDR prefix")
	}
	if p.Masked() != p {
		return Prefix{}, fmt.Errorf("netpol: non-canonical CIDR %q (host bits must be zero)", p.String())
	}
	addr := p.Addr()
	if addr.Is4() {
		b := addr.As4()
		return Prefix{
			Family: FamilyIPv4,
			Hi:     0,
			Lo:     uint64(binary.BigEndian.Uint32(b[:])),
			Bits:   p.Bits(),
		}, nil
	}
	if addr.Is6() {
		b := addr.As16()
		return Prefix{
			Family: FamilyIPv6,
			Hi:     binary.BigEndian.Uint64(b[:8]),
			Lo:     binary.BigEndian.Uint64(b[8:]),
			Bits:   p.Bits(),
		}, nil
	}
	return Prefix{}, fmt.Errorf("netpol: unsupported address family for %q", p.String())
}

// ParsePrefix parses a canonical CIDR string into a Prefix.
func ParsePrefix(s string) (Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return Prefix{}, fmt.Errorf("netpol: invalid CIDR %q: %w", s, err)
	}
	return PrefixFromNetip(p)
}

// String formats p in canonical CIDR notation, or returns "" if p is invalid.
func (p Prefix) String() string {
	if !p.Valid() {
		return ""
	}
	if p.Family == FamilyIPv4 {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(p.Lo))
		return netip.PrefixFrom(netip.AddrFrom4(b), p.Bits).String()
	}
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], p.Hi)
	binary.BigEndian.PutUint64(b[8:], p.Lo)
	return netip.PrefixFrom(netip.AddrFrom16(b), p.Bits).String()
}

func familyOrder(f Family) int {
	switch f {
	case FamilyIPv4:
		return 0
	case FamilyIPv6:
		return 1
	default:
		return 2
	}
}

func comparePrefix(a, b Prefix) int {
	if fa, fb := familyOrder(a.Family), familyOrder(b.Family); fa != fb {
		if fa < fb {
			return -1
		}
		return 1
	}
	if c := cmp128(a.Hi, a.Lo, b.Hi, b.Lo); c != 0 {
		return c
	}
	if a.Bits < b.Bits {
		return -1
	}
	if a.Bits > b.Bits {
		return 1
	}
	return 0
}
