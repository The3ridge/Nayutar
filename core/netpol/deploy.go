package netpol

import (
	"errors"
	"fmt"
)

var errNilCompiled = errors.New("netpol: compiled policy must be non-nil")

// DeployErrorCode classifies deployability failures that lie outside the modelled policy semantics.
type DeployErrorCode string

const (
	ErrWholeAddressSpaceCIDR DeployErrorCode = "whole_address_space_cidr"
	ErrIPv4MappedCIDR        DeployErrorCode = "ipv4_mapped_cidr"
	ErrBridgeNameCollision   DeployErrorCode = "bridge_name_collision"
)

// DeployError reports a bundle that compiles but cannot be enforced safely on a node.
type DeployError struct {
	Code    DeployErrorCode
	Message string
}

func (e *DeployError) Error() string {
	if e.Message == "" {
		return "netpol: " + string(e.Code)
	}
	return fmt.Sprintf("netpol: %s: %s", e.Code, e.Message)
}

var ipv4MappedPrefix = Prefix{Family: FamilyIPv6, Lo: 0x0000ffff00000000, Bits: 96}

// CheckDeployable rejects tenant CIDRs that cover the whole address space or touch ::ffff:0:0/96,
// and tenants whose bridge names collide.
func CheckDeployable(tenants []TenantPolicy) error {
	bridges := make(map[string]string, len(tenants))
	for _, t := range tenants {
		for _, c := range t.CIDRs {
			if c.Bits == 0 {
				return &DeployError{
					Code:    ErrWholeAddressSpaceCIDR,
					Message: fmt.Sprintf("tenant %q CIDR %s covers the whole address space", t.Tenant, c.String()),
				}
			}
			if c.Family == FamilyIPv6 && PrefixesOverlap(c, ipv4MappedPrefix) {
				return &DeployError{
					Code:    ErrIPv4MappedCIDR,
					Message: fmt.Sprintf("tenant %q CIDR %s overlaps ::ffff:0:0/96", t.Tenant, c.String()),
				}
			}
		}
		br := TenantBridgeName(t.Tenant)
		if other, dup := bridges[br]; dup {
			return &DeployError{
				Code:    ErrBridgeNameCollision,
				Message: fmt.Sprintf("tenants %q and %q share bridge name %s", other, t.Tenant, br),
			}
		}
		bridges[br] = t.Tenant
	}
	return nil
}

func verifyDeployable(cp *CompiledPolicy) (*CompiledPolicy, error) {
	if cp == nil {
		return nil, errNilCompiled
	}
	verified, cerr := Compile(PolicyBundle{Tenants: cp.Tenants})
	if cerr != nil {
		return nil, cerr
	}
	if err := CheckDeployable(verified.Tenants); err != nil {
		return nil, err
	}
	return verified, nil
}
