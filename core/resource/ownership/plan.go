package ownership

import (
	"fmt"
	"slices"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

// Mode selects declarative Apply vs conservative Prune planning.
type Mode uint8

const (
	ModeApply Mode = 1
	ModePrune Mode = 2
)

func (m Mode) String() string {
	switch m {
	case ModeApply:
		return "apply"
	case ModePrune:
		return "prune"
	default:
		return "invalid"
	}
}

// StateClass is the Scope-independent classification of a path in M_A(d, sigma).
type StateClass uint8

const (
	ClassOutsideDomain StateClass = iota
	ClassConverged
	ClassDeclaredSet
	ClassDeclaredAdoptModified
	ClassDeclaredConflict
	ClassDeferredUntilRelease
	ClassMigrationRequired
	ClassRetiredModifiedKnown
	ClassRetiredModifiedUnknown
	ClassRetiredCreatedAbsent
	ClassRetiredCreatedPrunable
	ClassRetiredCreatedAnomaly
)

// SelectionKind is the Stage 3 mode-selection outcome.
type SelectionKind uint8

const (
	SelectNoOp SelectionKind = iota
	SelectDeferred
	SelectCandidate
	SelectBlocker
)

// ActionKind identifies an executable ownership or environment operation.
type ActionKind uint8

const (
	ActionNone ActionKind = iota
	ActionSet
	ActionDelete
	ActionAdoptModified
	ActionUnmanage
	ActionClearRecord
	ActionExplicitAdopt
	ActionExplicitRelease
)

func (a ActionKind) String() string {
	switch a {
	case ActionNone:
		return "None"
	case ActionSet:
		return "Set"
	case ActionDelete:
		return "Delete"
	case ActionAdoptModified:
		return "AdoptModified"
	case ActionUnmanage:
		return "Unmanage"
	case ActionClearRecord:
		return "ClearRecord"
	case ActionExplicitAdopt:
		return "ExplicitAdopt"
	case ActionExplicitRelease:
		return "ExplicitRelease"
	default:
		return "Invalid"
	}
}

// RequiredPerms returns the exact permissions required by action.
func RequiredPerms(action ActionKind) Perm {
	switch action {
	case ActionSet:
		return PermSet | PermOwn
	case ActionAdoptModified, ActionUnmanage, ActionClearRecord:
		return PermOwn
	case ActionDelete:
		return PermDelete | PermOwn
	case ActionExplicitAdopt, ActionExplicitRelease:
		return PermOwn | PermResolve
	default:
		return PermNone
	}
}

// DecisionKind is the per-path outcome after Authorize and Gate.
type DecisionKind uint8

const (
	DecisionNoOp DecisionKind = iota
	DecisionDeferred
	DecisionAuthorized
	DecisionPolicyDenied
	DecisionBlocker
	DecisionSuppressed
)

// PathInput describes the Scope-independent state for classifying one path p.
type PathInput struct {
	Path                  Path
	Actor                 ActorID
	Declared              bool
	Desired               lkal.Value
	Observed              Observed
	Ancestors             []AncestorObs
	Entry                 Entry
	HasEntry              bool
	HasLastApplied        bool
	LastApplied           lkal.Value
	ForeignOverlaps       []Path
	SelfRetiredCreated    []Path
	SelfRetiredModified   []Path
	DeclaredOverlapTarget Path
}

// PathPlan holds the full 4-stage outcome for one path in M_A(d, sigma).
type PathPlan struct {
	Path          Path
	StateClass    StateClass
	Selection     SelectionKind
	Action        ActionKind
	RequiredPerms Perm
	GrantedPerms  Perm
	Decision      DecisionKind
	PrimaryCode   Code
	Diagnostics   []Diagnostic
	Desired       lkal.Value
	Observed      Observed
	Before        *Entry
	After         *Entry
}

// GateOutcome summarizes the All-or-Nothing Stage 5 batch gate.
type GateOutcome struct {
	Blocked     bool
	Paths       []PathPlan
	Diagnostics []Diagnostic
}

// ClassifyPath performs Stage 2 Scope-independent classification for one path.
func ClassifyPath(in PathInput) PathPlan {
	diags := inspectStructureDiagnostics(in.Path, in.Observed, in.Ancestors)

	if in.Declared {
		return classifyDeclared(in, diags)
	}
	if !in.HasEntry || in.Entry.State != StateOwned || in.Entry.Owner != in.Actor {
		return PathPlan{
			Path:       in.Path.Clone(),
			StateClass: ClassOutsideDomain,
			Observed:   in.Observed,
		}
	}
	return classifyRetired(in, diags)
}

func classifyDeclared(in PathInput, diags []Diagnostic) PathPlan {
	hasForeign := len(in.ForeignOverlaps) > 0
	for _, fp := range in.ForeignOverlaps {
		diags = append(diags, Diagnostic{
			Code:       CodeOwnershipConflict,
			Path:       in.Path.Clone(),
			Reason:     fmt.Sprintf("path %s overlaps foreign-owned path %s", in.Path.String(), fp.String()),
			NextAction: "coordinate ownership release with current owner",
		})
	}

	if len(in.SelfRetiredCreated) > 0 {
		diags = append(diags, Diagnostic{
			Code:       CodeMigrationRequired,
			Path:       in.Path.Clone(),
			Reason:     fmt.Sprintf("declared path %s overlaps retired created path %s", in.Path.String(), in.SelfRetiredCreated[0].String()),
			NextAction: "release -> migrate -> apply",
		})
		diags = SortDiagnostics(diags)
		return PathPlan{
			Path:        in.Path.Clone(),
			StateClass:  ClassMigrationRequired,
			PrimaryCode: SelectPrimaryCode(diags),
			Diagnostics: diags,
			Desired:     cloneValue(in.Desired),
			Observed:    in.Observed,
		}
	}

	if len(in.SelfRetiredModified) > 0 && !hasForeign {
		diags = append(diags, Diagnostic{
			Code:       CodeDeferredUntilRelease,
			Path:       in.Path.Clone(),
			Reason:     fmt.Sprintf("declared path %s overlaps retired modified path %s; deferred until release", in.Path.String(), in.SelfRetiredModified[0].String()),
			NextAction: "re-run apply after retired modified paths are unmanaged",
		})
		diags = SortDiagnostics(diags)
		return PathPlan{
			Path:        in.Path.Clone(),
			StateClass:  ClassDeferredUntilRelease,
			PrimaryCode: CodeDeferredUntilRelease,
			Diagnostics: diags,
			Desired:     cloneValue(in.Desired),
			Observed:    in.Observed,
		}
	}

	selfOwned := in.HasEntry && in.Entry.State == StateOwned && in.Entry.Owner == in.Actor
	if selfOwned {
		eCopy := in.Entry
		switch in.Observed.Kind {
		case ObsAbsent:
			diags = append(diags, Diagnostic{
				Code:       CodeExternalDrift,
				Path:       in.Path.Clone(),
				Reason:     fmt.Sprintf("owned path %s is absent from object", in.Path.String()),
				NextAction: "release -> apply",
			})
		case ObsLeaf:
			if in.HasLastApplied && !lkal.Equal(in.Observed.Value, in.LastApplied) {
				diags = append(diags, Diagnostic{
					Code:       CodeExternalDrift,
					Path:       in.Path.Clone(),
					Reason:     fmt.Sprintf("owned path %s drifted from last applied value", in.Path.String()),
					NextAction: "adopt -> apply",
				})
			} else if len(diags) == 0 && !hasForeign && canSetObserved(in.Observed, in.Ancestors) {
				if lkal.Equal(in.Observed.Value, in.Desired) {
					return PathPlan{
						Path:       in.Path.Clone(),
						StateClass: ClassConverged,
						Desired:    cloneValue(in.Desired),
						Observed:   in.Observed,
						Before:     &eCopy,
						After:      &eCopy,
					}
				}
				after := Entry{State: StateOwned, Owner: in.Actor, Origin: eCopy.Origin}
				return PathPlan{
					Path:       in.Path.Clone(),
					StateClass: ClassDeclaredSet,
					Desired:    cloneValue(in.Desired),
					Observed:   in.Observed,
					Before:     &eCopy,
					After:      &after,
				}
			}
		}
		diags = SortDiagnostics(diags)
		return PathPlan{
			Path:        in.Path.Clone(),
			StateClass:  ClassDeclaredConflict,
			PrimaryCode: SelectPrimaryCode(diags),
			Diagnostics: diags,
			Desired:     cloneValue(in.Desired),
			Observed:    in.Observed,
			Before:      &eCopy,
		}
	}

	// Not self-owned (absent, .defaulted, or .unowned).
	var beforePtr *Entry
	if in.HasEntry {
		ec := in.Entry
		beforePtr = &ec
	}
	if !hasForeign {
		isDefaulted := in.HasEntry && in.Entry.State == StateDefaulted
		if in.Observed.Kind == ObsAbsent && len(diags) == 0 && canSetObserved(in.Observed, in.Ancestors) {
			after := Entry{State: StateOwned, Owner: in.Actor, Origin: OriginCreated}
			return PathPlan{
				Path:       in.Path.Clone(),
				StateClass: ClassDeclaredSet,
				Desired:    cloneValue(in.Desired),
				Observed:   in.Observed,
				Before:     beforePtr,
				After:      &after,
			}
		}
		if in.Observed.Kind == ObsLeaf {
			if isDefaulted {
				if len(diags) == 0 && canSetObserved(in.Observed, in.Ancestors) {
					after := Entry{State: StateOwned, Owner: in.Actor, Origin: OriginCreated}
					return PathPlan{
						Path:       in.Path.Clone(),
						StateClass: ClassDeclaredSet,
						Desired:    cloneValue(in.Desired),
						Observed:   in.Observed,
						Before:     beforePtr,
						After:      &after,
					}
				}
			} else if !lkal.Equal(in.Observed.Value, in.Desired) {
				diags = append(diags, Diagnostic{
					Code:       CodeUnownedValueConflict,
					Path:       in.Path.Clone(),
					Reason:     fmt.Sprintf("unowned path %s has existing value differing from declaration", in.Path.String()),
					NextAction: "adopt -> apply",
				})
			} else if len(diags) == 0 && canSetObserved(in.Observed, in.Ancestors) {
				after := Entry{State: StateOwned, Owner: in.Actor, Origin: OriginModified}
				return PathPlan{
					Path:       in.Path.Clone(),
					StateClass: ClassDeclaredAdoptModified,
					Desired:    cloneValue(in.Desired),
					Observed:   in.Observed,
					Before:     beforePtr,
					After:      &after,
				}
			}
		}
	}
	diags = SortDiagnostics(diags)
	return PathPlan{
		Path:        in.Path.Clone(),
		StateClass:  ClassDeclaredConflict,
		PrimaryCode: SelectPrimaryCode(diags),
		Diagnostics: diags,
		Desired:     cloneValue(in.Desired),
		Observed:    in.Observed,
		Before:      beforePtr,
	}
}

func classifyRetired(in PathInput, diags []Diagnostic) PathPlan {
	eCopy := in.Entry
	if eCopy.Origin == OriginModified {
		if hasUnknownObs(in.Observed, in.Ancestors) {
			uDiags := filterCode(diags, CodeObservationUnavailable)
			uDiags = SortDiagnostics(uDiags)
			return PathPlan{
				Path:        in.Path.Clone(),
				StateClass:  ClassRetiredModifiedUnknown,
				PrimaryCode: CodeObservationUnavailable,
				Diagnostics: uDiags,
				Observed:    in.Observed,
				Before:      &eCopy,
			}
		}
		var after *Entry
		if in.Observed.Kind != ObsAbsent {
			u := Entry{State: StateUnowned}
			after = &u
		}
		return PathPlan{
			Path:       in.Path.Clone(),
			StateClass: ClassRetiredModifiedKnown,
			Observed:   in.Observed,
			Before:     &eCopy,
			After:      after,
		}
	}

	// OriginCreated retired path.
	if len(in.DeclaredOverlapTarget) > 0 {
		if in.Observed.Kind == ObsLeaf && in.HasLastApplied && !lkal.Equal(in.Observed.Value, in.LastApplied) {
			diags = append(diags, Diagnostic{
				Code:       CodeExternalDrift,
				Path:       in.Path.Clone(),
				Reason:     fmt.Sprintf("retired created path %s drifted from last applied value", in.Path.String()),
				NextAction: "release",
			})
		}
		diags = append(diags, Diagnostic{
			Code:       CodeMigrationRequired,
			Path:       in.Path.Clone(),
			Reason:     fmt.Sprintf("retired created path %s overlaps declared path %s", in.Path.String(), in.DeclaredOverlapTarget.String()),
			NextAction: "release -> migrate -> apply",
		})
		diags = SortDiagnostics(diags)
		return PathPlan{
			Path:        in.Path.Clone(),
			StateClass:  ClassMigrationRequired,
			PrimaryCode: SelectPrimaryCode(diags),
			Diagnostics: diags,
			Observed:    in.Observed,
			Before:      &eCopy,
		}
	}

	if in.Observed.Kind == ObsAbsent && len(diags) == 0 {
		return PathPlan{
			Path:       in.Path.Clone(),
			StateClass: ClassRetiredCreatedAbsent,
			Observed:   in.Observed,
			Before:     &eCopy,
			After:      nil,
		}
	}

	if len(diags) == 0 && canDeleteObserved(in.Observed, in.Ancestors) {
		if !in.HasLastApplied || lkal.Equal(in.Observed.Value, in.LastApplied) {
			return PathPlan{
				Path:       in.Path.Clone(),
				StateClass: ClassRetiredCreatedPrunable,
				Observed:   in.Observed,
				Before:     &eCopy,
				After:      nil,
			}
		}
	}

	if in.Observed.Kind == ObsLeaf && in.HasLastApplied && !lkal.Equal(in.Observed.Value, in.LastApplied) {
		diags = append(diags, Diagnostic{
			Code:       CodeExternalDrift,
			Path:       in.Path.Clone(),
			Reason:     fmt.Sprintf("retired created path %s drifted from last applied value", in.Path.String()),
			NextAction: "release",
		})
	}
	diags = SortDiagnostics(diags)
	return PathPlan{
		Path:        in.Path.Clone(),
		StateClass:  ClassRetiredCreatedAnomaly,
		PrimaryCode: SelectPrimaryCode(diags),
		Diagnostics: diags,
		Observed:    in.Observed,
		Before:      &eCopy,
	}
}

// SelectModeForPath applies Stage 3 (11-row kisage mode selection table) to a classified PathPlan.
func SelectModeForPath(pp PathPlan, mode Mode) PathPlan {
	out := pp
	out.Diagnostics = slices.Clone(pp.Diagnostics)

	switch pp.StateClass {
	case ClassOutsideDomain, ClassConverged:
		out.Selection = SelectNoOp
		out.Action = ActionNone
		out.RequiredPerms = PermNone

	case ClassDeclaredSet:
		out.Action = ActionSet
		if mode == ModeApply {
			out.Selection = SelectCandidate
			out.RequiredPerms = RequiredPerms(ActionSet)
		} else {
			out.Selection = SelectDeferred
			out.RequiredPerms = PermNone
		}

	case ClassDeclaredAdoptModified:
		out.Action = ActionAdoptModified
		if mode == ModeApply {
			out.Selection = SelectCandidate
			out.RequiredPerms = RequiredPerms(ActionAdoptModified)
		} else {
			out.Selection = SelectDeferred
			out.RequiredPerms = PermNone
		}

	case ClassDeclaredConflict:
		out.Action = ActionNone
		out.RequiredPerms = PermNone
		if mode == ModeApply {
			out.Selection = SelectBlocker
		} else {
			out.Selection = SelectDeferred
		}

	case ClassDeferredUntilRelease:
		out.Selection = SelectDeferred
		out.Action = ActionNone
		out.RequiredPerms = PermNone

	case ClassRetiredModifiedKnown:
		out.Action = ActionUnmanage
		if mode == ModeApply {
			out.Selection = SelectCandidate
			out.RequiredPerms = RequiredPerms(ActionUnmanage)
		} else {
			out.Selection = SelectDeferred
			out.RequiredPerms = PermNone
		}

	case ClassRetiredModifiedUnknown:
		out.Action = ActionNone
		out.RequiredPerms = PermNone
		if mode == ModeApply {
			out.Selection = SelectBlocker
		} else {
			out.Selection = SelectDeferred
		}

	case ClassRetiredCreatedAbsent:
		out.Selection = SelectCandidate
		out.Action = ActionClearRecord
		out.RequiredPerms = RequiredPerms(ActionClearRecord)

	case ClassRetiredCreatedPrunable:
		out.Action = ActionDelete
		if mode == ModeApply {
			out.Selection = SelectDeferred
			out.RequiredPerms = PermNone
			out.Diagnostics = append(out.Diagnostics, Diagnostic{
				Code:       CodePruneRequired,
				Path:       pp.Path.Clone(),
				Reason:     fmt.Sprintf("retired created leaf %s requires prune for deletion", pp.Path.String()),
				NextAction: "prune",
			})
			out.Diagnostics = SortDiagnostics(out.Diagnostics)
			out.PrimaryCode = CodePruneRequired
		} else {
			out.Selection = SelectCandidate
			out.RequiredPerms = RequiredPerms(ActionDelete)
		}

	case ClassRetiredCreatedAnomaly:
		out.Action = ActionNone
		out.RequiredPerms = PermNone
		if mode == ModeApply {
			out.Selection = SelectDeferred
		} else {
			out.Selection = SelectBlocker
		}

	case ClassMigrationRequired:
		out.Selection = SelectBlocker
		out.Action = ActionNone
		out.RequiredPerms = PermNone

	default:
		out.Selection = SelectBlocker
		out.Action = ActionNone
		out.RequiredPerms = PermNone
	}
	return out
}

// AuthorizePath applies Stage 4 (Scope check on SelectCandidate only).
func AuthorizePath(pp PathPlan, sc Scope) PathPlan {
	out := pp
	out.Diagnostics = slices.Clone(pp.Diagnostics)
	out.GrantedPerms = sc.PermissionsFor(pp.Path)

	switch pp.Selection {
	case SelectNoOp:
		out.Decision = DecisionNoOp
	case SelectDeferred:
		out.Decision = DecisionDeferred
	case SelectBlocker:
		out.Decision = DecisionBlocker
	case SelectCandidate:
		if diag := sc.authorizePerm(pp.Path, pp.Action, pp.RequiredPerms); diag != nil {
			out.Decision = DecisionPolicyDenied
			out.Diagnostics = append(out.Diagnostics, *diag)
			out.Diagnostics = SortDiagnostics(out.Diagnostics)
			out.PrimaryCode = CodePolicyDenied
		} else {
			out.Decision = DecisionAuthorized
		}
	default:
		out.Decision = DecisionBlocker
	}
	return out
}

// GateBatch applies Stage 5 (intra-mode All-or-Nothing gate).
func GateBatch(plans []PathPlan) GateOutcome {
	blocked := false
	for _, p := range plans {
		if p.Decision == DecisionBlocker || p.Decision == DecisionPolicyDenied {
			blocked = true
			break
		}
	}
	out := GateOutcome{
		Blocked: blocked,
		Paths:   make([]PathPlan, len(plans)),
	}
	var diags []Diagnostic
	for i, p := range plans {
		cp := p
		cp.Diagnostics = slices.Clone(p.Diagnostics)
		if blocked && cp.Decision == DecisionAuthorized {
			cp.Decision = DecisionSuppressed
		}
		out.Paths[i] = cp
		diags = append(diags, cp.Diagnostics...)
	}
	out.Diagnostics = SortDiagnostics(diags)
	return out
}

// PlanInput holds all inputs to pure PlanApply and PlanPrune.
type PlanInput struct {
	Object           keyspace.Object
	Actor            ActorID
	Scope            Scope
	CurrentDoc       lkal.Record
	CurrentState     State
	CurrentOwnerRev  int64
	Declaration      lkal.Record
	Expect           string
	ExpectedOwnerRev int64
}

// PlanResult holds the outcome of PlanApply or PlanPrune.
type PlanResult struct {
	Mode        Mode
	Blocked     bool
	EnvChanged  bool
	OwnChanged  bool
	NextDoc     lkal.Record
	NextState   State
	Gate        GateOutcome
	Diagnostics []Diagnostic
}

// Err returns the structured *Error when r is blocked, or nil otherwise.
func (r PlanResult) Err() error {
	if !r.Blocked {
		return nil
	}
	var blocking []Diagnostic
	for _, p := range r.Gate.Paths {
		if p.Decision == DecisionBlocker || p.Decision == DecisionPolicyDenied {
			blocking = append(blocking, p.Diagnostics...)
		}
	}
	if len(blocking) == 0 {
		blocking = r.Diagnostics
	}
	return errorFromDiagnostics(blocking)
}

// Create authorizes initial EntranceSpec leaf creation for a new object and returns its initial State.
func Create(actor ActorID, sc Scope, rawDoc lkal.Record) (State, error) {
	if err := ValidateActorID(actor); err != nil {
		return State{}, err
	}
	if err := sc.Validate(); err != nil {
		return State{}, err
	}
	leaves, err := ProjectEntranceSpec(rawDoc)
	if err != nil {
		return State{}, err
	}
	keys := sortedLeafPaths(leaves)
	var diags []Diagnostic
	for _, p := range keys {
		if d := sc.authorizePerm(p, ActionSet, RequiredPerms(ActionSet)); d != nil {
			diags = append(diags, *d)
		}
	}
	if len(diags) > 0 {
		return State{}, errorFromDiagnostics(diags)
	}
	st := NewState(actor)
	for _, p := range keys {
		st.Set(p, Entry{State: StateOwned, Owner: actor, Origin: OriginCreated})
	}
	return st, nil
}

// MarkCompletedDefaults records every EntranceSpec leaf newly materialized by schema.Complete
// (present in completedDoc and absent from rawDoc) as StateDefaulted, and drops stale defaulted
// entries whose leaves no longer exist in completedDoc.
func MarkCompletedDefaults(st State, rawDoc, completedDoc lkal.Record) State {
	next := st.Clone()
	rawLeaves, err := ProjectEntranceSpec(rawDoc)
	if err != nil {
		return next
	}
	compLeaves, err := ProjectEntranceSpec(completedDoc)
	if err != nil {
		return next
	}
	for key, e := range next.Entries {
		if e.State == StateDefaulted {
			if _, ok := compLeaves[key]; !ok {
				delete(next.Entries, key)
			}
		}
	}
	for key := range compLeaves {
		if _, inRaw := rawLeaves[key]; inRaw {
			continue
		}
		if cur, exists := next.Entries[key]; exists && cur.State == StateOwned {
			continue
		}
		next.Entries[key] = Entry{State: StateDefaulted}
	}
	return next
}

// PlanApply runs the pure CAS Apply planner against CurrentDoc and CurrentState.
func PlanApply(in PlanInput) (PlanResult, error) {
	return runPlan(in, ModeApply)
}

// PlanPrune runs the pure CAS Prune planner against CurrentDoc and CurrentState.
func PlanPrune(in PlanInput) (PlanResult, error) {
	return runPlan(in, ModePrune)
}

func runPlan(in PlanInput, mode Mode) (PlanResult, error) {
	if err := ValidateActorID(in.Actor); err != nil {
		return PlanResult{}, err
	}
	if err := in.Scope.Validate(); err != nil {
		return PlanResult{}, err
	}
	if err := in.CurrentState.Validate(); err != nil {
		return PlanResult{}, err
	}
	if in.CurrentOwnerRev < 0 || in.ExpectedOwnerRev < 0 {
		return PlanResult{}, newError(CodeInvalidInput, nil, "ownerRevision must be >= 0", "")
	}
	if in.ExpectedOwnerRev != 0 && in.ExpectedOwnerRev != in.CurrentOwnerRev {
		return PlanResult{}, newError(CodeOwnershipConflict, nil, fmt.Sprintf("ownerRevision mismatch: expected %d, current %d", in.ExpectedOwnerRev, in.CurrentOwnerRev), "run explain to obtain the current ownerRevision")
	}
	if in.Expect != "" {
		if err := VerifyDocConditionToken(in.Expect, in.Object, in.CurrentDoc); err != nil {
			return PlanResult{}, err
		}
	}
	declLeaves, err := ProjectDeclaration(in.Declaration, false)
	if err != nil {
		return PlanResult{}, err
	}

	// Build M_A(d, sigma) = D U dom(sigma_A) sorted by parsed path segments.
	evalMap := make(map[string]Path, len(declLeaves)+len(in.CurrentState.Entries))
	for k, lf := range declLeaves {
		evalMap[k] = lf.Path
	}
	sigmaAPaths := make([]Path, 0)
	foreignPaths := make([]Path, 0)
	for _, p := range in.CurrentState.SortedPaths() {
		e, _ := in.CurrentState.Get(p)
		if e.State != StateOwned {
			continue
		}
		if e.Owner == in.Actor {
			sigmaAPaths = append(sigmaAPaths, p)
			evalMap[p.String()] = p
		} else {
			foreignPaths = append(foreignPaths, p)
		}
	}
	evalPaths := make([]Path, 0, len(evalMap))
	for _, p := range evalMap {
		evalPaths = append(evalPaths, p)
	}
	slices.SortFunc(evalPaths, func(a, b Path) int { return a.Compare(b) })

	declPaths := sortedLeafPaths(declLeaves)
	planned := make([]PathPlan, 0, len(evalPaths))
	for _, p := range evalPaths {
		key := p.String()
		leaf, isDecl := declLeaves[key]
		obs, ancs := InspectDocPath(in.CurrentDoc, p)
		curEntry, hasEntry := in.CurrentState.Get(p)

		var fOverlaps []Path
		var selfRetCreated, selfRetModified []Path
		var declOverlap Path
		if isDecl {
			for _, fp := range foreignPaths {
				if fp.Overlaps(p) {
					fOverlaps = append(fOverlaps, fp)
				}
			}
			for _, sp := range sigmaAPaths {
				if !sp.Equal(p) && sp.Overlaps(p) {
					se, _ := in.CurrentState.Get(sp)
					if se.Origin == OriginCreated {
						selfRetCreated = append(selfRetCreated, sp)
					} else {
						selfRetModified = append(selfRetModified, sp)
					}
				}
			}
		} else {
			for _, dp := range declPaths {
				if !dp.Equal(p) && dp.Overlaps(p) {
					declOverlap = dp
					break
				}
			}
		}

		cp := ClassifyPath(PathInput{
			Path:                  p,
			Actor:                 in.Actor,
			Declared:              isDecl,
			Desired:               leaf.Value,
			Observed:              obs,
			Ancestors:             ancs,
			Entry:                 curEntry,
			HasEntry:              hasEntry,
			ForeignOverlaps:       fOverlaps,
			SelfRetiredCreated:    selfRetCreated,
			SelfRetiredModified:   selfRetModified,
			DeclaredOverlapTarget: declOverlap,
		})
		sp := SelectModeForPath(cp, mode)
		ap := AuthorizePath(sp, in.Scope)
		planned = append(planned, ap)
	}

	gate := GateBatch(planned)
	res := PlanResult{
		Mode:        mode,
		Blocked:     gate.Blocked,
		NextDoc:     cloneRecord(in.CurrentDoc),
		NextState:   in.CurrentState.Clone(),
		Gate:        gate,
		Diagnostics: gate.Diagnostics,
	}
	if gate.Blocked {
		return res, nil
	}

	nextDoc := cloneRecord(in.CurrentDoc)
	nextState := in.CurrentState.Clone()
	for _, pp := range gate.Paths {
		if pp.Decision != DecisionAuthorized {
			continue
		}
		switch pp.Action {
		case ActionSet:
			updated, err := setLeafInDoc(nextDoc, pp.Path, pp.Desired)
			if err != nil {
				return PlanResult{}, err
			}
			nextDoc = updated
			nextState.Set(pp.Path, *pp.After)
		case ActionDelete:
			nextState.Delete(pp.Path)
			updated, _ := deleteLeafPruneInDoc(nextDoc, pp.Path, &nextState)
			nextDoc = updated
		case ActionAdoptModified:
			nextState.Set(pp.Path, *pp.After)
		case ActionUnmanage:
			if pp.After == nil {
				nextState.Delete(pp.Path)
			} else {
				nextState.Set(pp.Path, *pp.After)
			}
		case ActionClearRecord:
			nextState.Delete(pp.Path)
		}
	}

	res.NextDoc = nextDoc
	res.NextState = nextState
	res.EnvChanged = !lkal.Equal(in.CurrentDoc, nextDoc)
	res.OwnChanged = !in.CurrentState.Equal(nextState)
	return res, nil
}

// LeafExplanation describes the ownership state and path condition token of one leaf.
type LeafExplanation struct {
	Path   Path
	State  LeafState
	Owner  ActorID
	Origin Origin
	Expect string
}

// Explanation is the read-only ownership inspection returned by Explain.
type Explanation struct {
	Revision      int64
	OwnerRevision int64
	Expect        string
	DefaultOwner  ActorID
	Leaves        []LeafExplanation
}

// Explain inspects doc and st at (rev, ownerRev), producing document and per-leaf expect tokens.
func Explain(obj keyspace.Object, doc lkal.Record, st State, rev, ownerRev int64) (Explanation, error) {
	if err := validateObjectIdentity(obj); err != nil {
		return Explanation{}, err
	}
	if err := st.Validate(); err != nil {
		return Explanation{}, err
	}
	if rev < 0 || ownerRev < 0 {
		return Explanation{}, newError(CodeInvalidInput, nil, "revisions must be >= 0", "")
	}
	docTok, err := DocConditionToken(obj, doc)
	if err != nil {
		return Explanation{}, err
	}
	docLeaves, err := ProjectEntranceSpec(doc)
	if err != nil {
		return Explanation{}, err
	}

	pathMap := make(map[string]Path, len(docLeaves)+len(st.Entries))
	for k, lf := range docLeaves {
		pathMap[k] = lf.Path
	}
	for _, p := range st.SortedPaths() {
		pathMap[p.String()] = p
	}
	paths := make([]Path, 0, len(pathMap))
	for _, p := range pathMap {
		paths = append(paths, p)
	}
	slices.SortFunc(paths, func(a, b Path) int { return a.Compare(b) })

	leaves := make([]LeafExplanation, 0, len(paths))
	for _, p := range paths {
		tok, err := PathConditionToken(obj, p, doc)
		if err != nil {
			return Explanation{}, err
		}
		e, ok := st.Get(p)
		if !ok {
			e = Entry{State: StateUnowned}
		}
		leaves = append(leaves, LeafExplanation{
			Path:   p,
			State:  e.State,
			Owner:  e.Owner,
			Origin: e.Origin,
			Expect: tok,
		})
	}
	return Explanation{
		Revision:      rev,
		OwnerRevision: ownerRev,
		Expect:        docTok,
		DefaultOwner:  st.DefaultOwner,
		Leaves:        leaves,
	}, nil
}

// ResolveInput holds inputs to explicit Adopt or Release.
type ResolveInput struct {
	Object           keyspace.Object
	Actor            ActorID
	Scope            Scope
	CurrentDoc       lkal.Record
	CurrentState     State
	CurrentOwnerRev  int64
	Path             Path
	Expect           string
	ExpectedOwnerRev int64
}

// ResolveResult holds the metadata-only transition result of Adopt or Release.
type ResolveResult struct {
	OwnChanged  bool
	NextState   State
	Diagnostics []Diagnostic
}

// Adopt explicitly claims ownership of an existing leaf with Origin = OriginModified (never OriginCreated)
// without modifying CurrentDoc.
func Adopt(in ResolveInput) (ResolveResult, error) {
	if err := validateResolveCommon(in, ActionExplicitAdopt); err != nil {
		return ResolveResult{}, err
	}
	// Reject foreign overlap and self proper ancestor/descendant overlap.
	for _, existing := range in.CurrentState.SortedPaths() {
		e, _ := in.CurrentState.Get(existing)
		if e.State != StateOwned || !existing.Overlaps(in.Path) {
			continue
		}
		if e.Owner != in.Actor {
			return ResolveResult{}, newError(CodeOwnershipConflict, in.Path, fmt.Sprintf("path %s overlaps foreign-owned path %s", in.Path.String(), existing.String()), "coordinate ownership release with current owner")
		}
		if !existing.Equal(in.Path) {
			return ResolveResult{}, newError(CodeOwnershipConflict, in.Path, fmt.Sprintf("cannot adopt %s while overlapping self-owned path %s exists", in.Path.String(), existing.String()), "release overlapping path first")
		}
	}
	if in.ExpectedOwnerRev != in.CurrentOwnerRev {
		return ResolveResult{}, newError(CodeOwnershipConflict, in.Path, fmt.Sprintf("ownerRevision mismatch: expected %d, current %d", in.ExpectedOwnerRev, in.CurrentOwnerRev), "run explain to obtain the current ownerRevision")
	}
	obs, ancs := InspectDocPath(in.CurrentDoc, in.Path)
	structDiags := inspectStructureDiagnostics(in.Path, obs, ancs)
	if len(structDiags) > 0 {
		return ResolveResult{}, errorFromDiagnostics(structDiags)
	}
	if obs.Kind != ObsLeaf || !canDeleteObserved(obs, ancs) {
		return ResolveResult{}, newError(CodeStructureConflict, in.Path, fmt.Sprintf("adopt on %s requires an existing leaf with record ancestors", in.Path.String()), "ensure target path is an existing leaf")
	}
	if err := VerifyPathConditionToken(in.Expect, in.Object, in.Path, in.CurrentDoc); err != nil {
		return ResolveResult{}, err
	}

	next := in.CurrentState.Clone()
	before, hasBefore := in.CurrentState.Get(in.Path)
	after := Entry{State: StateOwned, Owner: in.Actor, Origin: OriginModified}
	next.Set(in.Path, after)

	var diags []Diagnostic
	if hasBefore && before.State == StateOwned && before.Origin == OriginCreated {
		diags = append(diags, Diagnostic{
			Code:       CodeExplicitDeletionRightWaived,
			Path:       in.Path.Clone(),
			Reason:     fmt.Sprintf("adopt downgraded %s from created to modified; automatic prune deletion right waived", in.Path.String()),
			NextAction: "none",
		})
	}
	return ResolveResult{
		OwnChanged:  !in.CurrentState.Equal(next),
		NextState:   next,
		Diagnostics: diags,
	}, nil
}

// Release explicitly relinquishes self-owned path ownership without modifying CurrentDoc.
func Release(in ResolveInput) (ResolveResult, error) {
	if err := validateResolveCommon(in, ActionExplicitRelease); err != nil {
		return ResolveResult{}, err
	}
	before, hasBefore := in.CurrentState.Get(in.Path)
	if !hasBefore || before.State != StateOwned || before.Owner != in.Actor {
		return ResolveResult{}, newError(CodeOwnershipConflict, in.Path, fmt.Sprintf("path %s is not owned by actor %q", in.Path.String(), in.Actor), "only the current owner may release a path")
	}
	if in.ExpectedOwnerRev != in.CurrentOwnerRev {
		return ResolveResult{}, newError(CodeOwnershipConflict, in.Path, fmt.Sprintf("ownerRevision mismatch: expected %d, current %d", in.ExpectedOwnerRev, in.CurrentOwnerRev), "run explain to obtain the current ownerRevision")
	}
	obs, ancs := InspectDocPath(in.CurrentDoc, in.Path)
	if hasUnknownObs(obs, ancs) {
		return ResolveResult{}, newError(CodeObservationUnavailable, in.Path, fmt.Sprintf("observation for %s is unknown", in.Path.String()), "retry once observation is available")
	}
	if err := VerifyPathConditionToken(in.Expect, in.Object, in.Path, in.CurrentDoc); err != nil {
		return ResolveResult{}, err
	}

	next := in.CurrentState.Clone()
	if obs.Kind == ObsAbsent {
		next.Delete(in.Path)
	} else {
		next.Set(in.Path, Entry{State: StateUnowned})
	}

	var diags []Diagnostic
	if before.Origin == OriginCreated {
		diags = append(diags, Diagnostic{
			Code:       CodeExplicitDeletionRightWaived,
			Path:       in.Path.Clone(),
			Reason:     fmt.Sprintf("explicit release removed created ownership of %s", in.Path.String()),
			NextAction: "none",
		})
	}
	return ResolveResult{
		OwnChanged:  !in.CurrentState.Equal(next),
		NextState:   next,
		Diagnostics: diags,
	}, nil
}

func validateResolveCommon(in ResolveInput, action ActionKind) error {
	if err := validateObjectIdentity(in.Object); err != nil {
		return err
	}
	if err := ValidateActorID(in.Actor); err != nil {
		return err
	}
	if err := in.Scope.Validate(); err != nil {
		return err
	}
	if err := ValidateEntranceSpecPath(in.Path); err != nil {
		return err
	}
	if err := in.CurrentState.Validate(); err != nil {
		return err
	}
	if in.CurrentOwnerRev < 0 || in.ExpectedOwnerRev < 0 {
		return newError(CodeInvalidInput, in.Path, "ownerRevision must be >= 0", "")
	}
	if d := in.Scope.authorizePerm(in.Path, action, RequiredPerms(action)); d != nil {
		return errorFromDiagnostics([]Diagnostic{*d})
	}
	return nil
}

// CheckUpdate enforces imperative leaf-path ownership rules for Objects.Update over EntranceSpec.
func CheckUpdate(actor ActorID, sc Scope, oldDoc lkal.Record, oldState State, nextRawDoc lkal.Record) (State, error) {
	return checkImperative(actor, sc, oldDoc, oldState, nextRawDoc)
}

// CheckPatch enforces imperative leaf-path ownership rules for Objects.Patch over EntranceSpec.
func CheckPatch(actor ActorID, sc Scope, oldDoc lkal.Record, oldState State, nextRawDoc lkal.Record) (State, error) {
	return checkImperative(actor, sc, oldDoc, oldState, nextRawDoc)
}

func checkImperative(actor ActorID, sc Scope, oldDoc lkal.Record, oldState State, nextRawDoc lkal.Record) (State, error) {
	if err := ValidateActorID(actor); err != nil {
		return State{}, err
	}
	if err := sc.Validate(); err != nil {
		return State{}, err
	}
	if err := oldState.Validate(); err != nil {
		return State{}, err
	}
	oldLeaves, err := ProjectEntranceSpec(oldDoc)
	if err != nil {
		return State{}, err
	}
	nextLeaves, err := ProjectEntranceSpec(nextRawDoc)
	if err != nil {
		return State{}, err
	}

	nextState := oldState.Clone()
	var diags []Diagnostic

	// 1. Check leaves present in nextRawDoc (unchanged, modified, or added).
	for _, p := range sortedLeafPaths(nextLeaves) {
		key := p.String()
		nextVal := nextLeaves[key].Value
		oldLeaf, existed := oldLeaves[key]
		if existed && lkal.Equal(oldLeaf.Value, nextVal) {
			// Unchanged leaf: preserve existing ownership without demanding permissions.
			continue
		}
		if existed {
			cur, hasCur := oldState.Get(p)
			switch {
			case hasCur && cur.State == StateOwned && cur.Owner != actor:
				diags = append(diags, Diagnostic{
					Code:       CodeOwnershipConflict,
					Path:       p.Clone(),
					Reason:     fmt.Sprintf("path %s is owned by %q", p.String(), cur.Owner),
					NextAction: "coordinate ownership release with current owner",
				})
			case !hasCur || cur.State == StateUnowned:
				diags = append(diags, Diagnostic{
					Code:       CodeUnownedValueConflict,
					Path:       p.Clone(),
					Reason:     fmt.Sprintf("unowned path %s cannot be overwritten without adopt", p.String()),
					NextAction: "adopt -> update",
				})
			case cur.State == StateDefaulted:
				if d := sc.authorizePerm(p, ActionSet, RequiredPerms(ActionSet)); d != nil {
					diags = append(diags, *d)
				} else {
					nextState.Set(p, Entry{State: StateOwned, Owner: actor, Origin: OriginCreated})
				}
			default: // Self-owned leaf modification preserves existing Origin.
				if d := sc.authorizePerm(p, ActionSet, RequiredPerms(ActionSet)); d != nil {
					diags = append(diags, *d)
				} else {
					nextState.Set(p, Entry{State: StateOwned, Owner: actor, Origin: cur.Origin})
				}
			}
			continue
		}

		// Added leaf: check overlapping old entries.
		blocked := false
		addedOrigin := OriginCreated
		for _, op := range oldState.SortedPaths() {
			if !op.Overlaps(p) {
				continue
			}
			oe, _ := oldState.Get(op)
			switch oe.State {
			case StateOwned:
				if oe.Owner != actor {
					diags = append(diags, Diagnostic{
						Code:       CodeOwnershipConflict,
						Path:       p.Clone(),
						Reason:     fmt.Sprintf("path %s overlaps foreign-owned path %s", p.String(), op.String()),
						NextAction: "coordinate ownership release with current owner",
					})
					blocked = true
				} else if op.Equal(p) {
					addedOrigin = oe.Origin
				} else if _, inOld := oldLeaves[op.String()]; !inOld {
					if oe.Origin == OriginCreated {
						diags = append(diags, Diagnostic{
							Code:       CodeMigrationRequired,
							Path:       p.Clone(),
							Reason:     fmt.Sprintf("path %s overlaps retired created path %s", p.String(), op.String()),
							NextAction: "release -> migrate -> update",
						})
					} else {
						diags = append(diags, Diagnostic{
							Code:       CodeOwnershipConflict,
							Path:       p.Clone(),
							Reason:     fmt.Sprintf("path %s overlaps retired modified path %s", p.String(), op.String()),
							NextAction: "release overlapping path before update",
						})
					}
					blocked = true
				}
			case StateUnowned:
				diags = append(diags, Diagnostic{
					Code:       CodeUnownedValueConflict,
					Path:       p.Clone(),
					Reason:     fmt.Sprintf("path %s overlaps unowned path %s", p.String(), op.String()),
					NextAction: "adopt -> update",
				})
				blocked = true
			}
		}
		if !blocked {
			if d := sc.authorizePerm(p, ActionSet, RequiredPerms(ActionSet)); d != nil {
				diags = append(diags, *d)
			} else {
				nextState.Set(p, Entry{State: StateOwned, Owner: actor, Origin: addedOrigin})
			}
		}
	}

	// 2. Check leaves deleted between oldDoc and nextRawDoc.
	for _, p := range sortedLeafPaths(oldLeaves) {
		key := p.String()
		if _, stillPresent := nextLeaves[key]; stillPresent {
			continue
		}
		cur, hasCur := oldState.Get(p)
		switch {
		case hasCur && cur.State == StateOwned && cur.Owner != actor:
			diags = append(diags, Diagnostic{
				Code:       CodeOwnershipConflict,
				Path:       p.Clone(),
				Reason:     fmt.Sprintf("cannot delete foreign-owned path %s owned by %q", p.String(), cur.Owner),
				NextAction: "coordinate ownership release with current owner",
			})
		case !hasCur || cur.State == StateUnowned:
			diags = append(diags, Diagnostic{
				Code:       CodeUnownedValueConflict,
				Path:       p.Clone(),
				Reason:     fmt.Sprintf("cannot delete unowned path %s without adopt", p.String()),
				NextAction: "adopt -> delete",
			})
		case cur.State == StateDefaulted:
			// Omitted or parent-removed defaulted leaf; schema.Complete will restore if parent remains.
			nextState.Delete(p)
		default: // Self-owned deletion requires Delete+Own.
			if d := sc.authorizePerm(p, ActionDelete, RequiredPerms(ActionDelete)); d != nil {
				diags = append(diags, *d)
			} else {
				nextState.Delete(p)
			}
		}
	}

	if len(diags) > 0 {
		return State{}, errorFromDiagnostics(diags)
	}
	return nextState, nil
}

func inspectStructureDiagnostics(target Path, obs Observed, ancs []AncestorObs) []Diagnostic {
	var diags []Diagnostic
	var firstAbsent Path
	for _, a := range ancs {
		switch a.Observed.Kind {
		case ObsUnknown:
			diags = append(diags, Diagnostic{
				Code:       CodeObservationUnavailable,
				Path:       target.Clone(),
				Reason:     fmt.Sprintf("ancestor %s observation is unknown", a.Path.String()),
				NextAction: "retry once observation is available",
			})
		case ObsAbsent:
			if len(firstAbsent) == 0 {
				firstAbsent = a.Path
			}
		case ObsRecord:
			if len(firstAbsent) > 0 {
				diags = append(diags, Diagnostic{
					Code:       CodeStructureConflict,
					Path:       target.Clone(),
					Reason:     fmt.Sprintf("inconsistent observation: ancestor %s present under absent %s", a.Path.String(), firstAbsent.String()),
					NextAction: "re-observe object",
				})
			}
		case ObsLeaf:
			diags = append(diags, Diagnostic{
				Code:       CodeStructureConflict,
				Path:       target.Clone(),
				Reason:     fmt.Sprintf("proper ancestor %s is a leaf; cannot replace leaf with record", a.Path.String()),
				NextAction: "align declaration structure with object",
			})
		case ObsOpaque:
			diags = append(diags, Diagnostic{
				Code:       CodeUnrepresentable,
				Path:       target.Clone(),
				Reason:     fmt.Sprintf("proper ancestor %s is unrepresentable", a.Path.String()),
				NextAction: "inspect unrepresentable ancestor",
			})
		}
	}

	switch obs.Kind {
	case ObsUnknown:
		diags = append(diags, Diagnostic{
			Code:       CodeObservationUnavailable,
			Path:       target.Clone(),
			Reason:     fmt.Sprintf("target %s observation is unknown", target.String()),
			NextAction: "retry once observation is available",
		})
	case ObsRecord:
		diags = append(diags, Diagnostic{
			Code:       CodeStructureConflict,
			Path:       target.Clone(),
			Reason:     fmt.Sprintf("target %s is an existing record; cannot overwrite or delete record as leaf", target.String()),
			NextAction: "declare leaf paths inside the record",
		})
	case ObsOpaque:
		diags = append(diags, Diagnostic{
			Code:       CodeUnrepresentable,
			Path:       target.Clone(),
			Reason:     fmt.Sprintf("target %s is unrepresentable", target.String()),
			NextAction: "inspect unrepresentable target",
		})
	case ObsLeaf:
		if len(firstAbsent) > 0 {
			diags = append(diags, Diagnostic{
				Code:       CodeStructureConflict,
				Path:       target.Clone(),
				Reason:     fmt.Sprintf("inconsistent observation: target %s present under absent %s", target.String(), firstAbsent.String()),
				NextAction: "re-observe object",
			})
		}
	}
	return diags
}

func hasUnknownObs(obs Observed, ancs []AncestorObs) bool {
	if obs.Kind == ObsUnknown {
		return true
	}
	for _, a := range ancs {
		if a.Observed.Kind == ObsUnknown {
			return true
		}
	}
	return false
}

func canSetObserved(obs Observed, ancs []AncestorObs) bool {
	if obs.Kind != ObsAbsent && obs.Kind != ObsLeaf {
		return false
	}
	seenAbsent := false
	for _, a := range ancs {
		if a.Observed.Kind == ObsAbsent {
			seenAbsent = true
			continue
		}
		if a.Observed.Kind != ObsRecord || seenAbsent {
			return false
		}
	}
	return !seenAbsent || obs.Kind == ObsAbsent
}

func canDeleteObserved(obs Observed, ancs []AncestorObs) bool {
	if obs.Kind != ObsLeaf {
		return false
	}
	for _, a := range ancs {
		if a.Observed.Kind != ObsRecord {
			return false
		}
	}
	return true
}

func filterCode(diags []Diagnostic, code Code) []Diagnostic {
	var out []Diagnostic
	for _, d := range diags {
		if d.Code == code {
			out = append(out, d)
		}
	}
	return out
}

func sortedLeafPaths(m map[string]Leaf) []Path {
	out := make([]Path, 0, len(m))
	for _, lf := range m {
		out = append(out, lf.Path)
	}
	slices.SortFunc(out, func(a, b Path) int { return a.Compare(b) })
	return out
}
