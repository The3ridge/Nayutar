package ownership

import (
	"fmt"
	"strings"
)

// Perm is a bitset of the four independent kisage permissions.
// PermResolve is never implied by PermSet, PermDelete, or PermOwn.
type Perm uint8

const (
	PermNone    Perm = 0
	PermSet     Perm = 1 << 0
	PermDelete  Perm = 1 << 1
	PermOwn     Perm = 1 << 2
	PermResolve Perm = 1 << 3

	PermRegular Perm = PermSet | PermDelete | PermOwn
	PermAll     Perm = PermSet | PermDelete | PermOwn | PermResolve
)

func (p Perm) IsValid() bool {
	return p&^PermAll == 0
}

func (p Perm) Has(required Perm) bool {
	if !p.IsValid() || !required.IsValid() {
		return false
	}
	return (p & required) == required
}

func (p Perm) Missing(required Perm) Perm {
	return (required & PermAll) &^ (p & PermAll)
}

func (p Perm) String() string {
	if p == PermNone {
		return "None"
	}
	var parts []string
	if p&PermSet != 0 {
		parts = append(parts, "Set")
	}
	if p&PermDelete != 0 {
		parts = append(parts, "Delete")
	}
	if p&PermOwn != 0 {
		parts = append(parts, "Own")
	}
	if p&PermResolve != 0 {
		parts = append(parts, "Resolve")
	}
	return strings.Join(parts, "+")
}

// MatchMode selects how a Scope Rule matches parsed leaf path segments.
type MatchMode uint8

const (
	MatchSubtree MatchMode = iota
	MatchExact
	MatchRoot
)

// Rule grants Perms on target paths matched by parsed segment comparison.
type Rule struct {
	Prefix Path
	Match  MatchMode
	Perms  Perm
}

func NewRootRule(perms Perm) (Rule, error) {
	r := Rule{Match: MatchRoot, Perms: perms}
	if err := r.Validate(); err != nil {
		return Rule{}, err
	}
	return r, nil
}

func NewSubtreeRule(prefix Path, perms Perm) (Rule, error) {
	r := Rule{Prefix: prefix.Clone(), Match: MatchSubtree, Perms: perms}
	if err := r.Validate(); err != nil {
		return Rule{}, err
	}
	return r, nil
}

func NewExactRule(target Path, perms Perm) (Rule, error) {
	r := Rule{Prefix: target.Clone(), Match: MatchExact, Perms: perms}
	if err := r.Validate(); err != nil {
		return Rule{}, err
	}
	return r, nil
}

func (r Rule) Validate() error {
	if !r.Perms.IsValid() || r.Perms == PermNone {
		return newError(CodeInvalidInput, r.Prefix, "scope rule permissions must be non-empty and valid", "")
	}
	switch r.Match {
	case MatchRoot:
		if len(r.Prefix) != 0 {
			return newError(CodeInvalidInput, r.Prefix, "MatchRoot rule must have an empty prefix", "")
		}
		return nil
	case MatchSubtree, MatchExact:
		return ValidatePath(r.Prefix)
	default:
		return newError(CodeInvalidInput, r.Prefix, fmt.Sprintf("invalid scope match mode %d", r.Match), "")
	}
}

func (r Rule) Clone() Rule {
	return Rule{
		Prefix: r.Prefix.Clone(),
		Match:  r.Match,
		Perms:  r.Perms,
	}
}

// Matches reports whether r applies to target leaf path p using segment comparison only.
func (r Rule) Matches(p Path) bool {
	if r.Validate() != nil || !IsEntranceSpecPath(p) {
		return false
	}
	switch r.Match {
	case MatchRoot:
		return true
	case MatchExact:
		return r.Prefix.Equal(p)
	case MatchSubtree:
		return r.Prefix.Equal(p) || r.Prefix.IsProperAncestorOf(p)
	default:
		return false
	}
}

// Scope is an immutable set of path permission rules bound to an actor.
// An empty Scope fails closed and denies all permissions.
type Scope struct {
	rules []Rule
}

func NewScope(rules ...Rule) (Scope, error) {
	cloned := make([]Rule, len(rules))
	for i, r := range rules {
		if err := r.Validate(); err != nil {
			return Scope{}, err
		}
		cloned[i] = r.Clone()
	}
	return Scope{rules: cloned}, nil
}

func MustNewScope(rules ...Rule) Scope {
	sc, err := NewScope(rules...)
	if err != nil {
		panic(err)
	}
	return sc
}

// RootScope builds a validated Scope granting perms across all EntranceSpec leaf paths.
func RootScope(perms Perm) Scope {
	return MustNewScope(Rule{Match: MatchRoot, Perms: perms})
}

func (s Scope) Rules() []Rule {
	if len(s.rules) == 0 {
		return nil
	}
	out := make([]Rule, len(s.rules))
	for i, r := range s.rules {
		out[i] = r.Clone()
	}
	return out
}

func (s Scope) Validate() error {
	for _, r := range s.rules {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (s Scope) PermissionsFor(p Path) Perm {
	if !IsEntranceSpecPath(p) {
		return PermNone
	}
	var granted Perm
	for _, r := range s.rules {
		if r.Matches(p) {
			granted |= r.Perms & PermAll
		}
	}
	return granted
}

func (s Scope) Allows(p Path, required Perm) bool {
	if !required.IsValid() || !IsEntranceSpecPath(p) {
		return false
	}
	return s.PermissionsFor(p).Has(required)
}

func (s Scope) authorizePerm(p Path, action ActionKind, required Perm) *Diagnostic {
	granted := s.PermissionsFor(p)
	if granted.Has(required) {
		return nil
	}
	missing := granted.Missing(required)
	return &Diagnostic{
		Code:       CodePolicyDenied,
		Path:       p.Clone(),
		Reason:     fmt.Sprintf("action %s on %s requires %s permission (granted: %s, missing: %s)", action, p.String(), required, granted, missing),
		NextAction: fmt.Sprintf("grant %s permission on %s", missing, p.String()),
	}
}
