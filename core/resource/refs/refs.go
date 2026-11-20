// Package refs implements pure reference grammar validation, occurrence collection, schema checking, and index delta calculation.
package refs

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

// MaxTargets is the maximum number of distinct target objects a document's spec may reference.
const MaxTargets = 32

// ErrorKind classifies failures returned by Prepare, Check, and DeleteDelta.
type ErrorKind uint8

const (
	// ErrInternal indicates a stored document violates reference placement, grammar, or target limits.
	ErrInternal ErrorKind = iota + 1
	// ErrMalformed indicates a candidate document violates reference placement, grammar, or target limits.
	ErrMalformed
	// ErrKind indicates the reference schema does not allow the target qualified kind at that path.
	ErrKind
	// ErrDenied indicates the authorization oracle rejected an added occurrence.
	ErrDenied
	// ErrNotFound indicates the existence oracle reported a missing target object.
	ErrNotFound
)

// String returns the canonical name of k.
func (k ErrorKind) String() string {
	switch k {
	case ErrInternal:
		return "internal"
	case ErrMalformed:
		return "malformed"
	case ErrKind:
		return "kind"
	case ErrDenied:
		return "denied"
	case ErrNotFound:
		return "notFound"
	default:
		return fmt.Sprintf("unknown(%d)", k)
	}
}

// Error describes a reference check failure and the smallest offending occurrence when applicable.
type Error struct {
	Kind       ErrorKind
	Occurrence Occurrence
	Detail     string
}

// Error formats the error with its kind, occurrence, and detail.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	msg := "refs: " + e.Kind.String()
	if e.Occurrence.Path != "" {
		msg += ": " + e.Occurrence.Path
	}
	if len(e.Occurrence.Ref) > 0 {
		msg += " &" + strings.Join(e.Occurrence.Ref, "/")
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Occurrence pairs a field path in a document with the reference value found there.
type Occurrence struct {
	Path string
	Ref  lkal.Ref
}

// Target represents a parsed reference target object and optional leaf path inside it.
type Target struct {
	Object keyspace.Object
	Leaf   []string
}

// Parse validates ref against the reference grammar and splits it into a target object and optional leaf path.
func Parse(ref lkal.Ref) (Target, error) {
	if len(ref) < 2 {
		return Target{}, fmt.Errorf("refs: malformed ref: expected at least 2 segments, got %d", len(ref))
	}
	group, kind, err := keyspace.ParseQualifiedKind(ref[0])
	if err != nil {
		return Target{}, fmt.Errorf("refs: invalid target kind %q: %w", ref[0], err)
	}
	if err := keyspace.ValidateName(ref[1]); err != nil {
		return Target{}, fmt.Errorf("refs: invalid target name %q: %w", ref[1], err)
	}

	var leaf []string
	if len(ref) > 2 {
		rest := ref[2:]
		switch rest[0] {
		case "meta", "spec", "status":
		default:
			return Target{}, fmt.Errorf("refs: invalid leaf root %q", rest[0])
		}
		for _, seg := range rest {
			if seg == "" || strings.Contains(seg, "/") || seg[0] == '$' {
				return Target{}, fmt.Errorf("refs: invalid leaf segment %q", seg)
			}
		}
		leaf = slices.Clone(rest)
	}

	return Target{
		Object: keyspace.Object{Group: group, Kind: kind, Name: ref[1]},
		Leaf:   leaf,
	}, nil
}

type collectedOccurrence struct {
	occ    Occurrence
	keys   []string
	badKey bool
}

// Occurrences collects all reference occurrences in doc, sorted and deduplicated by raw key path and ref.
func Occurrences(doc lkal.Record) []Occurrence {
	collected := collectOccurrences(doc)
	if len(collected) == 0 {
		return nil
	}
	out := make([]Occurrence, len(collected))
	for i, c := range collected {
		out[i] = c.occ
	}
	return out
}

func collectOccurrences(doc lkal.Record) []collectedOccurrence {
	var out []collectedOccurrence
	collectRecord(doc, nil, false, &out)
	if len(out) == 0 {
		return nil
	}
	slices.SortFunc(out, compareCollected)
	out = slices.CompactFunc(out, func(a, b collectedOccurrence) bool {
		return slices.Equal(a.keys, b.keys) && slices.Equal(a.occ.Ref, b.occ.Ref)
	})
	return out
}

func collectRecord(rec lkal.Record, keys []string, badKey bool, out *[]collectedOccurrence) {
	for _, f := range rec.Fields {
		nextKeys := make([]string, len(keys)+1)
		copy(nextKeys, keys)
		nextKeys[len(keys)] = f.Key
		nextBad := badKey || f.Key == "" || strings.Contains(f.Key, "/")
		collectValue(f.Value, nextKeys, nextBad, out)
	}
}

func collectValue(v lkal.Value, keys []string, badKey bool, out *[]collectedOccurrence) {
	switch val := v.(type) {
	case lkal.Ref:
		*out = append(*out, collectedOccurrence{
			occ: Occurrence{
				Path: strings.Join(keys, "/"),
				Ref:  slices.Clone(val),
			},
			keys:   keys,
			badKey: badKey,
		})
	case lkal.Record:
		collectRecord(val, keys, badKey, out)
	case lkal.List:
		for _, elem := range val {
			collectValue(elem, keys, badKey, out)
		}
	}
}

func compareCollected(a, b collectedOccurrence) int {
	if c := compareOccurrence(a.occ, b.occ); c != 0 {
		return c
	}
	return slices.Compare(a.keys, b.keys)
}

func compareOccurrence(a, b Occurrence) int {
	if c := strings.Compare(a.Path, b.Path); c != 0 {
		return c
	}
	return slices.Compare(a.Ref, b.Ref)
}

func cloneOccurrence(occ Occurrence) Occurrence {
	return Occurrence{
		Path: occ.Path,
		Ref:  slices.Clone(occ.Ref),
	}
}

func (c collectedOccurrence) wellPlaced() bool {
	return !c.badKey && len(c.keys) >= 2 && c.keys[0] == "spec"
}

func placementDetail(c collectedOccurrence) string {
	if c.badKey {
		return "invalid key in reference path"
	}
	return "reference outside spec"
}

// Rule allows references at Path to any of Targets qualified kinds.
type Rule struct {
	Path    []string
	Targets []string
}

// Schema maps source qualified kinds to their allowed reference rules.
type Schema map[string][]Rule

// Validate checks that all schema keys and targets are valid qualified kinds and all rule paths start with "spec" without empty segments.
func (s Schema) Validate() error {
	keys := make([]string, 0, len(s))
	for k := range s {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	for _, srcQKind := range keys {
		if _, _, err := keyspace.ParseQualifiedKind(srcQKind); err != nil {
			return fmt.Errorf("refs: invalid schema source kind %q: %w", srcQKind, err)
		}
		for i, rule := range s[srcQKind] {
			if len(rule.Path) == 0 {
				return fmt.Errorf("refs: schema %q rule %d has empty path", srcQKind, i)
			}
			if rule.Path[0] != "spec" {
				return fmt.Errorf("refs: schema %q rule %d path must start with \"spec\"", srcQKind, i)
			}
			for j, seg := range rule.Path {
				if seg == "" {
					return fmt.Errorf("refs: schema %q rule %d has empty path segment at %d", srcQKind, i, j)
				}
			}
			for _, targetQKind := range rule.Targets {
				if _, _, err := keyspace.ParseQualifiedKind(targetQKind); err != nil {
					return fmt.Errorf("refs: schema %q rule %d invalid target kind %q: %w", srcQKind, i, targetQKind, err)
				}
			}
		}
	}
	return nil
}

// Allows reports whether sourceQKind may reference targetQKind at path.
func (s Schema) Allows(sourceQKind, path, targetQKind string) bool {
	if path == "" {
		return false
	}
	rules, ok := s[sourceQKind]
	if !ok || len(rules) == 0 {
		return false
	}
	segs := strings.Split(path, "/")
	for _, rule := range rules {
		if !ruleMatches(rule.Path, segs) {
			continue
		}
		if slices.Contains(rule.Targets, targetQKind) {
			return true
		}
	}
	return false
}

func ruleMatches(pattern, segs []string) bool {
	if len(pattern) != len(segs) {
		return false
	}
	for i, p := range pattern {
		if p != "*" && p != segs[i] {
			return false
		}
	}
	return true
}

// Input holds the parameters for checking a document write against reference rules.
type Input struct {
	Source    keyspace.Object
	Schema    Schema
	Stored    lkal.Record
	HasStored bool
	Next      lkal.Record
}

// Plan holds the added occurrences and target sets computed by Prepare for authorization and existence checks.
type Plan struct {
	Added   []Occurrence
	Targets []keyspace.Object

	added     []Occurrence
	addedObjs []keyspace.Object
	put       []keyspace.Object
	del       []keyspace.Object
	compare   []keyspace.Object
}

// Delta describes back-reference index puts, deletes, and target existence/revision comparisons.
type Delta struct {
	Put     []keyspace.Object
	Delete  []keyspace.Object
	Compare []keyspace.Object
}

// Prepare executes phases 1 through 5 of the write reference check and returns a Plan for Check.
func Prepare(in Input) (Plan, error) {
	var storedTargets []keyspace.Object
	var storedCollected []collectedOccurrence
	if in.HasStored {
		var err error
		storedCollected, storedTargets, err = validateStored(in.Stored)
		if err != nil {
			return Plan{}, err
		}
	}

	nextCollected := collectOccurrences(in.Next)
	for _, c := range nextCollected {
		if !c.wellPlaced() {
			return Plan{}, &Error{
				Kind:       ErrMalformed,
				Occurrence: cloneOccurrence(c.occ),
				Detail:     placementDetail(c),
			}
		}
	}

	added := diffWellPlacedOccurrences(nextCollected, storedCollected)
	addedObjs := make([]keyspace.Object, len(added))
	for i, occ := range added {
		target, err := Parse(occ.Ref)
		if err != nil {
			return Plan{}, &Error{
				Kind:       ErrMalformed,
				Occurrence: cloneOccurrence(occ),
				Detail:     err.Error(),
			}
		}
		addedObjs[i] = target.Object
	}

	// Phase 1 verified storedCollected and phase 3 verified added, so all nextCollected refs parse.
	nextTargets, err := checkAndExtractTargets(nextCollected, ErrMalformed)
	if err != nil {
		return Plan{}, err
	}
	if len(nextTargets) > MaxTargets {
		return Plan{}, &Error{
			Kind:   ErrMalformed,
			Detail: fmt.Sprintf("target count %d exceeds limit %d", len(nextTargets), MaxTargets),
		}
	}

	sourceQKind, sourceErr := keyspace.QualifiedKind(in.Source.Group, in.Source.Kind)
	for i, occ := range added {
		targetQKind := objectQKind(addedObjs[i])
		if sourceErr != nil || !in.Schema.Allows(sourceQKind, occ.Path, targetQKind) {
			return Plan{}, &Error{
				Kind:       ErrKind,
				Occurrence: cloneOccurrence(occ),
				Detail:     fmt.Sprintf("target kind %q not allowed at %q", targetQKind, occ.Path),
			}
		}
	}

	compare := distinctObjects(addedObjs)
	put := diffObjects(nextTargets, storedTargets)
	del := diffObjects(storedTargets, nextTargets)

	return Plan{
		Added:     cloneOccurrences(added),
		Targets:   slices.Clone(compare),
		added:     added,
		addedObjs: addedObjs,
		put:       put,
		del:       del,
		compare:   compare,
	}, nil
}

// Check executes phases 6 and 7 using the authorization and existence oracles and returns the index Delta.
func (p Plan) Check(authorize func(Occurrence) bool, exists func(keyspace.Object) bool) (Delta, error) {
	for _, occ := range p.added {
		if authorize == nil || !authorize(cloneOccurrence(occ)) {
			return Delta{}, &Error{
				Kind:       ErrDenied,
				Occurrence: cloneOccurrence(occ),
				Detail:     "reference authorization denied",
			}
		}
	}
	for i, occ := range p.added {
		if exists == nil || !exists(p.addedObjs[i]) {
			return Delta{}, &Error{
				Kind:       ErrNotFound,
				Occurrence: cloneOccurrence(occ),
				Detail:     "target object not found",
			}
		}
	}
	return Delta{
		Put:     slices.Clone(p.put),
		Delete:  slices.Clone(p.del),
		Compare: slices.Clone(p.compare),
	}, nil
}

// DeleteDelta returns the back-reference index deletions for a stored document, or ErrInternal if it violates phase 1.
func DeleteDelta(stored lkal.Record) (Delta, error) {
	_, targets, err := validateStored(stored)
	if err != nil {
		return Delta{}, err
	}
	return Delta{Delete: targets}, nil
}

func validateStored(stored lkal.Record) ([]collectedOccurrence, []keyspace.Object, error) {
	collected := collectOccurrences(stored)
	targets, err := checkAndExtractTargets(collected, ErrInternal)
	if err != nil {
		return nil, nil, err
	}
	if len(targets) > MaxTargets {
		return nil, nil, &Error{
			Kind:   ErrInternal,
			Detail: fmt.Sprintf("target count %d exceeds limit %d", len(targets), MaxTargets),
		}
	}
	return collected, targets, nil
}

func checkAndExtractTargets(collected []collectedOccurrence, errKind ErrorKind) ([]keyspace.Object, error) {
	if len(collected) == 0 {
		return nil, nil
	}
	objs := make([]keyspace.Object, len(collected))
	for i, c := range collected {
		if !c.wellPlaced() {
			return nil, &Error{
				Kind:       errKind,
				Occurrence: cloneOccurrence(c.occ),
				Detail:     placementDetail(c),
			}
		}
		target, err := Parse(c.occ.Ref)
		if err != nil {
			return nil, &Error{
				Kind:       errKind,
				Occurrence: cloneOccurrence(c.occ),
				Detail:     err.Error(),
			}
		}
		objs[i] = target.Object
	}
	return distinctObjects(objs), nil
}

func diffWellPlacedOccurrences(next, stored []collectedOccurrence) []Occurrence {
	if len(next) == 0 {
		return nil
	}
	var out []Occurrence
	i, j := 0, 0
	for i < len(next) && j < len(stored) {
		cmp := compareOccurrence(next[i].occ, stored[j].occ)
		switch {
		case cmp < 0:
			out = append(out, cloneOccurrence(next[i].occ))
			i++
		case cmp > 0:
			j++
		default:
			i++
			j++
		}
	}
	for ; i < len(next); i++ {
		out = append(out, cloneOccurrence(next[i].occ))
	}
	return out
}

func cloneOccurrences(occs []Occurrence) []Occurrence {
	if len(occs) == 0 {
		return nil
	}
	out := make([]Occurrence, len(occs))
	for i, occ := range occs {
		out[i] = cloneOccurrence(occ)
	}
	return out
}

func objectQKind(obj keyspace.Object) string {
	if obj.Group == keyspace.CoreGroup || obj.Group == "" {
		return obj.Kind
	}
	return obj.Kind + "." + obj.Group
}

func compareObject(a, b keyspace.Object) int {
	if c := strings.Compare(objectQKind(a), objectQKind(b)); c != 0 {
		return c
	}
	return strings.Compare(a.Name, b.Name)
}

func distinctObjects(objs []keyspace.Object) []keyspace.Object {
	if len(objs) == 0 {
		return nil
	}
	out := slices.Clone(objs)
	slices.SortFunc(out, compareObject)
	return slices.Compact(out)
}

func diffObjects(a, b []keyspace.Object) []keyspace.Object {
	if len(a) == 0 {
		return nil
	}
	if len(b) == 0 {
		return slices.Clone(a)
	}
	var out []keyspace.Object
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		cmp := compareObject(a[i], b[j])
		switch {
		case cmp < 0:
			out = append(out, a[i])
			i++
		case cmp > 0:
			j++
		default:
			i++
			j++
		}
	}
	out = append(out, a[i:]...)
	return out
}

// Principal identifies the caller requesting a reference write.
type Principal struct {
	Type string
	ID   string
}

// Request describes a single reference authorization query.
type Request struct {
	Tenant       string
	Principal    Principal
	Source       keyspace.Object
	SourcePath   string
	Target       keyspace.Object
	TargetLabels map[string]string
	TargetExists bool
}

// Authorizer evaluates whether a reference Request is permitted.
type Authorizer interface {
	Authorize(Request) bool
}
