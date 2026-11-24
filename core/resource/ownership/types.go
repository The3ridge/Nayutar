// Package ownership implements the pure CAS leaf-path ownership planner,
// explicit resolution transitions, condition tokens, and compact trie codec.
package ownership

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

// Code identifies a kisage diagnostic or error code reachable under CAS ownership.
type Code string

const (
	CodeInvalidInput                Code = "K101"
	CodeEmptyRecordNotSupported     Code = "K102"
	CodeOwnershipConflict           Code = "K201"
	CodeStructureConflict           Code = "K202"
	CodeExternalDrift               Code = "K203"
	CodeMigrationRequired           Code = "K204"
	CodeUnownedValueConflict        Code = "K206"
	CodeObservationUnavailable      Code = "K301"
	CodeUnrepresentable             Code = "K302"
	CodePolicyDenied                Code = "K401"
	CodeIndeterminateOutcome        Code = "K502"
	CodeExplicitDeletionRightWaived Code = "K602"
	CodeDeferredUntilRelease        Code = "K603"
	CodePruneRequired               Code = "K604"
)

var (
	ErrInvalidInput           = errors.New("ownership: K101 invalid input")
	ErrEmptyRecord            = errors.New("ownership: K102 empty record not supported")
	ErrOwnershipConflict      = errors.New("ownership: K201 ownership conflict")
	ErrStructureConflict      = errors.New("ownership: K202 structure conflict")
	ErrExternalDrift          = errors.New("ownership: K203 external drift")
	ErrMigrationRequired      = errors.New("ownership: K204 migration required")
	ErrUnownedValueConflict   = errors.New("ownership: K206 unowned value conflict")
	ErrObservationUnavailable = errors.New("ownership: K301 observation unavailable")
	ErrUnrepresentable        = errors.New("ownership: K302 unrepresentable")
	ErrPolicyDenied           = errors.New("ownership: K401 policy denied")
	ErrIndeterminateOutcome   = errors.New("ownership: K502 indeterminate outcome")
)

// IsValid reports whether c is one of the reachable kisage diagnostic codes.
func (c Code) IsValid() bool {
	switch c {
	case CodeInvalidInput, CodeEmptyRecordNotSupported,
		CodeOwnershipConflict, CodeStructureConflict, CodeExternalDrift,
		CodeMigrationRequired, CodeUnownedValueConflict,
		CodeObservationUnavailable, CodeUnrepresentable,
		CodePolicyDenied, CodeIndeterminateOutcome,
		CodeExplicitDeletionRightWaived, CodeDeferredUntilRelease, CodePruneRequired:
		return true
	default:
		return false
	}
}

func (c Code) sentinel() error {
	switch c {
	case CodeInvalidInput:
		return ErrInvalidInput
	case CodeEmptyRecordNotSupported:
		return ErrEmptyRecord
	case CodeOwnershipConflict:
		return ErrOwnershipConflict
	case CodeStructureConflict:
		return ErrStructureConflict
	case CodeExternalDrift:
		return ErrExternalDrift
	case CodeMigrationRequired:
		return ErrMigrationRequired
	case CodeUnownedValueConflict:
		return ErrUnownedValueConflict
	case CodeObservationUnavailable:
		return ErrObservationUnavailable
	case CodeUnrepresentable:
		return ErrUnrepresentable
	case CodePolicyDenied:
		return ErrPolicyDenied
	case CodeIndeterminateOutcome:
		return ErrIndeterminateOutcome
	default:
		return nil
	}
}

// DiagnosticPriority returns the blocker precedence rank (smaller number = higher priority):
// K301 > K201 > K204 > K202/K302 > K206 > K203 > K401.
func DiagnosticPriority(c Code) int {
	switch c {
	case CodeObservationUnavailable:
		return 1
	case CodeOwnershipConflict:
		return 2
	case CodeMigrationRequired:
		return 3
	case CodeStructureConflict, CodeUnrepresentable:
		return 4
	case CodeUnownedValueConflict:
		return 5
	case CodeExternalDrift:
		return 6
	case CodePolicyDenied:
		return 7
	case CodeInvalidInput, CodeEmptyRecordNotSupported:
		return 8
	case CodeIndeterminateOutcome:
		return 9
	default:
		return 10
	}
}

// SelectPrimaryCode returns the highest-priority diagnostic code from diags, or "" if empty.
func SelectPrimaryCode(diags []Diagnostic) Code {
	if len(diags) == 0 {
		return ""
	}
	best := diags[0].Code
	bestRank := DiagnosticPriority(best)
	for _, d := range diags[1:] {
		if r := DiagnosticPriority(d.Code); r < bestRank {
			best = d.Code
			bestRank = r
		}
	}
	return best
}

// Diagnostic is a structured per-path diagnostic emitted by ownership operations.
type Diagnostic struct {
	Code       Code
	Path       Path
	Reason     string
	NextAction string
}

// Error formats d as a human-readable diagnostic string.
func (d Diagnostic) Error() string {
	if len(d.Path) > 0 {
		return fmt.Sprintf("%s %s: %s", d.Code, d.Path.String(), d.Reason)
	}
	return fmt.Sprintf("%s: %s", d.Code, d.Reason)
}

// CompareDiagnostics orders diagnostics deterministically by path segments, code, reason, and nextAction.
func CompareDiagnostics(a, b Diagnostic) int {
	if c := a.Path.Compare(b.Path); c != 0 {
		return c
	}
	if a.Code != b.Code {
		return strings.Compare(string(a.Code), string(b.Code))
	}
	if a.Reason != b.Reason {
		return strings.Compare(a.Reason, b.Reason)
	}
	return strings.Compare(a.NextAction, b.NextAction)
}

// SortDiagnostics sorts and deduplicates diagnostics deterministically in place.
func SortDiagnostics(diags []Diagnostic) []Diagnostic {
	if len(diags) <= 1 {
		return diags
	}
	slices.SortFunc(diags, CompareDiagnostics)
	return slices.CompactFunc(diags, func(a, b Diagnostic) bool {
		return CompareDiagnostics(a, b) == 0
	})
}

// Error is the structured error returned when an ownership check or transition fails.
type Error struct {
	Code        Code
	Path        string
	Detail      string
	Diagnostics []Diagnostic
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	msg := fmt.Sprintf("ownership: %s", e.Code)
	if e.Path != "" {
		msg += ": " + e.Path
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Code.sentinel()
}

func newError(code Code, p Path, detail string, nextAction string) *Error {
	pathStr := ""
	if len(p) > 0 {
		pathStr = p.String()
	}
	diag := Diagnostic{
		Code:       code,
		Path:       p.Clone(),
		Reason:     detail,
		NextAction: nextAction,
	}
	return &Error{
		Code:        code,
		Path:        pathStr,
		Detail:      detail,
		Diagnostics: []Diagnostic{diag},
	}
}

func errorFromDiagnostics(diags []Diagnostic) *Error {
	sorted := SortDiagnostics(slices.Clone(diags))
	if len(sorted) == 0 {
		return newError(CodeInvalidInput, nil, "ownership operation blocked", "")
	}
	primary := SelectPrimaryCode(sorted)
	chosen := sorted[0]
	for _, d := range sorted {
		if d.Code == primary {
			chosen = d
			break
		}
	}
	pathStr := ""
	if len(chosen.Path) > 0 {
		pathStr = chosen.Path.String()
	}
	return &Error{
		Code:        primary,
		Path:        pathStr,
		Detail:      chosen.Reason,
		Diagnostics: sorted,
	}
}

// ActorID identifies a server-bound execution principal.
type ActorID string

// ValidateActorID verifies id is non-empty, valid UTF-8, and free of ASCII control characters (0x00..0x1F, 0x7F).
func ValidateActorID(id ActorID) error {
	s := string(id)
	if s == "" || strings.TrimSpace(s) == "" {
		return newError(CodeInvalidInput, nil, "actor ID must not be empty", "")
	}
	if !utf8.ValidString(s) {
		return newError(CodeInvalidInput, nil, "actor ID contains invalid UTF-8", "")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return newError(CodeInvalidInput, nil, "actor ID must not contain control characters", "")
		}
	}
	return nil
}

// Origin represents the provenance of an owned leaf.
type Origin uint8

const (
	OriginCreated  Origin = 1
	OriginModified Origin = 2
)

func (o Origin) String() string {
	switch o {
	case OriginCreated:
		return "created"
	case OriginModified:
		return "modified"
	default:
		return "invalid"
	}
}

func (o Origin) IsValid() bool {
	return o == OriginCreated || o == OriginModified
}

// HasAutoDeleteRight reports whether o grants automatic deletion rights during Prune.
func (o Origin) HasAutoDeleteRight() bool {
	return o == OriginCreated
}

// LeafState distinguishes owned, explicitly unowned, and schema-defaulted leaves.
type LeafState uint8

const (
	StateUnowned   LeafState = 0
	StateOwned     LeafState = 1
	StateDefaulted LeafState = 2
)

func (s LeafState) String() string {
	switch s {
	case StateUnowned:
		return "unowned"
	case StateOwned:
		return "owned"
	case StateDefaulted:
		return "defaulted"
	default:
		return "invalid"
	}
}

func (s LeafState) IsValid() bool {
	return s == StateUnowned || s == StateOwned || s == StateDefaulted
}

// Entry records the ownership state of one EntranceSpec leaf path.
type Entry struct {
	State  LeafState
	Owner  ActorID
	Origin Origin
}

func (e Entry) Validate() error {
	if !e.State.IsValid() {
		return newError(CodeInvalidInput, nil, fmt.Sprintf("invalid leaf state %d", e.State), "")
	}
	if e.State == StateOwned {
		if err := ValidateActorID(e.Owner); err != nil {
			return err
		}
		if !e.Origin.IsValid() {
			return newError(CodeInvalidInput, nil, fmt.Sprintf("owned entry requires valid origin, got %d", e.Origin), "")
		}
		return nil
	}
	if e.Owner != "" || e.Origin != 0 {
		return newError(CodeInvalidInput, nil, fmt.Sprintf("%s entry must not specify owner or origin", e.State), "")
	}
	return nil
}

func (e Entry) Equal(other Entry) bool {
	return e.State == other.State && e.Owner == other.Owner && e.Origin == other.Origin
}

// State is the decoded per-object leaf ownership map sigma plus its default creator actor.
type State struct {
	DefaultOwner ActorID
	Entries      map[string]Entry
}

// NewState constructs a State with DefaultOwner and an initialized Entries map.
func NewState(defaultOwner ActorID) State {
	return State{
		DefaultOwner: defaultOwner,
		Entries:      make(map[string]Entry),
	}
}

// Clone returns a deep copy of s.
func (s State) Clone() State {
	return State{
		DefaultOwner: s.DefaultOwner,
		Entries:      maps.Clone(s.Entries),
	}
}

// Equal reports whether s and other have the same DefaultOwner and identical Entries.
func (s State) Equal(other State) bool {
	if s.DefaultOwner != other.DefaultOwner || len(s.Entries) != len(other.Entries) {
		return false
	}
	for k, v := range s.Entries {
		ov, ok := other.Entries[k]
		if !ok || !v.Equal(ov) {
			return false
		}
	}
	return true
}

// Validate checks DefaultOwner and every path and Entry in s.
func (s State) Validate() error {
	if err := ValidateActorID(s.DefaultOwner); err != nil {
		return err
	}
	for k, e := range s.Entries {
		p, err := ParsePath(k)
		if err != nil {
			return err
		}
		if err := ValidateEntranceSpecPath(p); err != nil {
			return err
		}
		if p.String() != k {
			return newError(CodeInvalidInput, p, fmt.Sprintf("non-canonical path key %q", k), "")
		}
		if err := e.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Get returns the Entry for p if present.
func (s State) Get(p Path) (Entry, bool) {
	if s.Entries == nil {
		return Entry{}, false
	}
	e, ok := s.Entries[p.String()]
	return e, ok
}

// Set stores e at p.
func (s *State) Set(p Path, e Entry) {
	if s.Entries == nil {
		s.Entries = make(map[string]Entry)
	}
	s.Entries[p.String()] = e
}

// Delete removes p from s.Entries.
func (s *State) Delete(p Path) {
	if s.Entries == nil {
		return
	}
	delete(s.Entries, p.String())
}

// SortedPaths returns all paths in s.Entries ordered lexicographically by parsed segments.
func (s State) SortedPaths() []Path {
	out := make([]Path, 0, len(s.Entries))
	for k := range s.Entries {
		if p, err := ParsePath(k); err == nil {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b Path) int { return a.Compare(b) })
	return out
}
