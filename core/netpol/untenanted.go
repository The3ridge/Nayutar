package netpol

import (
	"errors"
	"fmt"
	"strings"
)

// TenantBridgePrefix is the name prefix shared by every bridge TenantBridgeName returns.
const TenantBridgePrefix = "nbr-"

// IsTenantBridgeName reports whether bridge lies in the namespace reserved for tenant bridges.
func IsTenantBridgeName(bridge string) bool {
	return strings.HasPrefix(bridge, TenantBridgePrefix)
}

// ValidateUntenantedSubnets verifies that no subnet overlaps any tenant CIDR in cp.
func (cp *CompiledPolicy) ValidateUntenantedSubnets(subnets []string) error {
	if cp == nil {
		return errors.New("netpol: compiled policy is nil")
	}
	for _, raw := range subnets {
		sub, err := ParsePrefix(raw)
		if err != nil {
			return fmt.Errorf("netpol: invalid unit subnet %q: %w", raw, err)
		}
		for _, tp := range cp.Tenants {
			for _, tc := range tp.CIDRs {
				if PrefixesOverlap(sub, tc) {
					return fmt.Errorf("netpol: unit subnet %s overlaps tenant %q CIDR %s", sub.String(), tp.Tenant, tc.String())
				}
			}
		}
	}
	return nil
}
