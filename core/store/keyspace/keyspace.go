// Package keyspace encodes, validates, and parses store keys across the canonical key families.
package keyspace

import (
	"fmt"
	"strings"
)

// TenantsPrefix is the Range/Watch prefix for all tenant registry records.
const TenantsPrefix = "/sys/tenants/"

// NodesPrefix is the Range/Watch prefix for all node records.
const NodesPrefix = "/sys/nodes/"

// EvictionBudgetKey is the canonical store key for the cluster eviction budget.
const EvictionBudgetKey = "/sys/liveness/evictions"

// BindingPrefix is the tenant-relative Range/Watch prefix for all bindings.
const BindingPrefix = "bind/"

// BackRefsPrefix is the tenant-relative Range/Watch prefix for all back-references.
const BackRefsPrefix = "ref/"

// Family identifies one of the canonical store key families.
type Family uint8

const (
	// FamilyObject identifies resource object keys.
	FamilyObject Family = iota + 1
	// FamilyOwnership identifies leaf-path ownership keys.
	FamilyOwnership
	// FamilyBackRef identifies reference back-index keys.
	FamilyBackRef
	// FamilyBinding identifies tenant API binding keys.
	FamilyBinding
	// FamilySchema identifies cluster-wide schema keys.
	FamilySchema
	// FamilyTenant identifies cluster-wide tenant registry keys.
	FamilyTenant
	// FamilyNode identifies cluster-wide node liveness keys.
	FamilyNode
	// FamilyEvictionBudget identifies the cluster-wide eviction budget key.
	FamilyEvictionBudget
)

// Object identifies a resource object by group, kind, and name.
type Object struct{ Group, Kind, Name string }

// Key represents a canonical store key in one of the eight key families.
type Key struct {
	Family       Family
	Tenant       string
	Object       Object
	Source       Object
	IdentityHash string
	Version      string
	Node         string
}

// TenantPrefix returns the absolute prefix "/t/<tenant>/".
func TenantPrefix(tenant string) (string, error) {
	if err := ValidateTenant(tenant); err != nil {
		return "", err
	}
	return "/t/" + tenant + "/", nil
}

// SchemaPrefix returns the absolute prefix "/x/<identityHash>/schema/".
func SchemaPrefix(identityHash string) (string, error) {
	if err := ValidateIdentityHash(identityHash); err != nil {
		return "", err
	}
	return "/x/" + identityHash + "/schema/", nil
}

// ObjectPrefix returns the tenant-relative prefix "o/<group>/<kind>/".
func ObjectPrefix(group, kind string) (string, error) {
	return groupKindPrefix("o/", group, kind)
}

// OwnershipPrefix returns the tenant-relative prefix "own/<group>/<kind>/".
func OwnershipPrefix(group, kind string) (string, error) {
	return groupKindPrefix("own/", group, kind)
}

func groupKindPrefix(area, group, kind string) (string, error) {
	if err := ValidateGroup(group); err != nil {
		return "", err
	}
	if err := ValidateKind(kind); err != nil {
		return "", err
	}
	return area + group + "/" + kind + "/", nil
}

// BackRefPrefix returns the tenant-relative prefix "ref/<targetQKind>/<targetName>/".
func BackRefPrefix(target Object) (string, error) {
	qKind, err := QualifiedKind(target.Group, target.Kind)
	if err != nil {
		return "", err
	}
	if err := ValidateName(target.Name); err != nil {
		return "", err
	}
	return "ref/" + qKind + "/" + target.Name + "/", nil
}

// Encode formats k as a canonical absolute store key.
func (k Key) Encode() (string, error) {
	switch k.Family {
	case FamilyObject, FamilyOwnership, FamilyBackRef, FamilyBinding:
		rel, err := k.Rel()
		if err != nil {
			return "", err
		}
		return "/t/" + k.Tenant + "/" + rel, nil
	case FamilySchema:
		if k.Tenant != "" || k.Object != (Object{}) || k.Source != (Object{}) || k.Node != "" {
			return "", fmt.Errorf("%w: unexpected non-zero fields for family %d", ErrInvalid, k.Family)
		}
		if err := ValidateIdentityHash(k.IdentityHash); err != nil {
			return "", err
		}
		if err := ValidateVersion(k.Version); err != nil {
			return "", err
		}
		return "/x/" + k.IdentityHash + "/schema/" + k.Version, nil
	case FamilyTenant:
		if k.Object != (Object{}) || k.Source != (Object{}) || k.IdentityHash != "" || k.Version != "" || k.Node != "" {
			return "", fmt.Errorf("%w: unexpected non-zero fields for family %d", ErrInvalid, k.Family)
		}
		if err := ValidateTenant(k.Tenant); err != nil {
			return "", err
		}
		return TenantsPrefix + k.Tenant, nil
	case FamilyNode:
		if k.Tenant != "" || k.Object != (Object{}) || k.Source != (Object{}) || k.IdentityHash != "" || k.Version != "" {
			return "", fmt.Errorf("%w: unexpected non-zero fields for family %d", ErrInvalid, k.Family)
		}
		if err := ValidateNode(k.Node); err != nil {
			return "", err
		}
		return NodesPrefix + k.Node, nil
	case FamilyEvictionBudget:
		if k.Tenant != "" || k.Object != (Object{}) || k.Source != (Object{}) || k.IdentityHash != "" || k.Version != "" || k.Node != "" {
			return "", fmt.Errorf("%w: unexpected non-zero fields for family %d", ErrInvalid, k.Family)
		}
		return EvictionBudgetKey, nil
	default:
		return "", fmt.Errorf("%w: unknown family %d", ErrInvalid, k.Family)
	}
}

// Rel formats k as a tenant-relative store key. It fails for FamilySchema, FamilyTenant, FamilyNode, and FamilyEvictionBudget.
func (k Key) Rel() (string, error) {
	switch k.Family {
	case FamilyObject:
		return encodeObjectRel("o", k)
	case FamilyOwnership:
		return encodeObjectRel("own", k)
	case FamilyBackRef:
		return encodeBackRefRel(k)
	case FamilyBinding:
		return encodeBindingRel(k)
	case FamilySchema, FamilyTenant, FamilyNode, FamilyEvictionBudget:
		return "", fmt.Errorf("%w: family %d has no tenant-relative key", ErrInvalid, k.Family)
	default:
		return "", fmt.Errorf("%w: unknown family %d", ErrInvalid, k.Family)
	}
}

func validateObject(obj Object) error {
	if err := ValidateGroup(obj.Group); err != nil {
		return err
	}
	if err := ValidateKind(obj.Kind); err != nil {
		return err
	}
	return ValidateName(obj.Name)
}

func encodeObjectRel(area string, k Key) (string, error) {
	if k.Source != (Object{}) || k.IdentityHash != "" || k.Version != "" || k.Node != "" {
		return "", fmt.Errorf("%w: unexpected non-zero fields for family %d", ErrInvalid, k.Family)
	}
	if err := ValidateTenant(k.Tenant); err != nil {
		return "", err
	}
	if err := validateObject(k.Object); err != nil {
		return "", err
	}
	return area + "/" + k.Object.Group + "/" + k.Object.Kind + "/" + k.Object.Name, nil
}

func encodeBackRefRel(k Key) (string, error) {
	if k.IdentityHash != "" || k.Version != "" || k.Node != "" {
		return "", fmt.Errorf("%w: unexpected non-zero fields for family %d", ErrInvalid, k.Family)
	}
	if err := ValidateTenant(k.Tenant); err != nil {
		return "", err
	}
	tQKind, err := QualifiedKind(k.Object.Group, k.Object.Kind)
	if err != nil {
		return "", err
	}
	if err := ValidateName(k.Object.Name); err != nil {
		return "", err
	}
	sQKind, err := QualifiedKind(k.Source.Group, k.Source.Kind)
	if err != nil {
		return "", err
	}
	if err := ValidateName(k.Source.Name); err != nil {
		return "", err
	}
	return "ref/" + tQKind + "/" + k.Object.Name + "/" + sQKind + "/" + k.Source.Name, nil
}

func encodeBindingRel(k Key) (string, error) {
	if k.Object != (Object{}) || k.Source != (Object{}) || k.Version != "" || k.Node != "" {
		return "", fmt.Errorf("%w: unexpected non-zero fields for family %d", ErrInvalid, k.Family)
	}
	if err := ValidateTenant(k.Tenant); err != nil {
		return "", err
	}
	if err := ValidateIdentityHash(k.IdentityHash); err != nil {
		return "", err
	}
	return BindingPrefix + k.IdentityHash, nil
}

// Parse parses a canonical absolute store key.
func Parse(key string) (Key, error) {
	switch {
	case strings.HasPrefix(key, "/t/"):
		tenant, rel, ok := strings.Cut(key[len("/t/"):], "/")
		if !ok {
			return Key{}, fmt.Errorf("%w: key %q missing tenant-relative part", ErrInvalid, key)
		}
		return ParseRel(tenant, rel)
	case strings.HasPrefix(key, "/x/"):
		hash, area, ver, ok := split3(key[len("/x/"):])
		if !ok || area != "schema" {
			return Key{}, fmt.Errorf("%w: invalid schema key %q", ErrInvalid, key)
		}
		if err := ValidateIdentityHash(hash); err != nil {
			return Key{}, err
		}
		if err := ValidateVersion(ver); err != nil {
			return Key{}, err
		}
		return Key{Family: FamilySchema, IdentityHash: hash, Version: ver}, nil
	case strings.HasPrefix(key, TenantsPrefix):
		tenant := key[len(TenantsPrefix):]
		if err := ValidateTenant(tenant); err != nil {
			return Key{}, err
		}
		return Key{Family: FamilyTenant, Tenant: tenant}, nil
	case strings.HasPrefix(key, NodesPrefix):
		node := key[len(NodesPrefix):]
		if err := ValidateNode(node); err != nil {
			return Key{}, err
		}
		return Key{Family: FamilyNode, Node: node}, nil
	case key == EvictionBudgetKey:
		return Key{Family: FamilyEvictionBudget}, nil
	default:
		return Key{}, fmt.Errorf("%w: unknown key prefix in %q", ErrInvalid, key)
	}
}

// ParseRel parses a canonical tenant-relative store key for the given tenant.
func ParseRel(tenant, rel string) (Key, error) {
	if err := ValidateTenant(tenant); err != nil {
		return Key{}, err
	}
	if rel == "" || rel[0] == '/' {
		return Key{}, fmt.Errorf("%w: invalid relative key %q", ErrInvalid, rel)
	}
	area, rest, ok := strings.Cut(rel, "/")
	if !ok {
		return Key{}, fmt.Errorf("%w: relative key %q missing area separator", ErrInvalid, rel)
	}
	switch area {
	case "o":
		return parseObjectRel(FamilyObject, tenant, rest)
	case "own":
		return parseObjectRel(FamilyOwnership, tenant, rest)
	case "ref":
		return parseBackRefRel(tenant, rest)
	case "bind":
		if err := ValidateIdentityHash(rest); err != nil {
			return Key{}, err
		}
		return Key{Family: FamilyBinding, Tenant: tenant, IdentityHash: rest}, nil
	default:
		return Key{}, fmt.Errorf("%w: unknown tenant-relative area %q", ErrInvalid, area)
	}
}

func parseObjectRel(fam Family, tenant, rest string) (Key, error) {
	group, kind, name, ok := split3(rest)
	if !ok {
		return Key{}, fmt.Errorf("%w: invalid object key segments %q", ErrInvalid, rest)
	}
	obj := Object{Group: group, Kind: kind, Name: name}
	if err := validateObject(obj); err != nil {
		return Key{}, err
	}
	return Key{Family: fam, Tenant: tenant, Object: obj}, nil
}

func parseBackRefRel(tenant, rest string) (Key, error) {
	tQKind, tName, sQKind, sName, ok := split4(rest)
	if !ok {
		return Key{}, fmt.Errorf("%w: invalid back-reference segments %q", ErrInvalid, rest)
	}
	tGroup, tKind, err := ParseQualifiedKind(tQKind)
	if err != nil {
		return Key{}, err
	}
	if err := ValidateName(tName); err != nil {
		return Key{}, err
	}
	sGroup, sKind, err := ParseQualifiedKind(sQKind)
	if err != nil {
		return Key{}, err
	}
	if err := ValidateName(sName); err != nil {
		return Key{}, err
	}
	return Key{
		Family: FamilyBackRef,
		Tenant: tenant,
		Object: Object{Group: tGroup, Kind: tKind, Name: tName},
		Source: Object{Group: sGroup, Kind: sKind, Name: sName},
	}, nil
}

func split3(s string) (a, b, c string, ok bool) {
	a, rest, ok := strings.Cut(s, "/")
	if !ok {
		return "", "", "", false
	}
	b, c, ok = strings.Cut(rest, "/")
	if !ok || strings.IndexByte(c, '/') >= 0 {
		return "", "", "", false
	}
	return a, b, c, true
}

func split4(s string) (a, b, c, d string, ok bool) {
	a, rest, ok := strings.Cut(s, "/")
	if !ok {
		return "", "", "", "", false
	}
	b, c, d, ok = split3(rest)
	return a, b, c, d, ok
}
