import Kisage
import Nayutar.Envelope
import Nayutar.Store

set_option autoImplicit false

namespace Nayutar.Ownership

open Nayutar.Envelope (Value Values Fields optEqv)
open Nayutar.Store (Store Rev Cmp Write)
open Kisage (Origin PermSet Mode SelectionKind ActionKind DecisionKind
  permNone permOwn permSetOwn permDeleteOwn permOwnResolve permRegular permAll
  requiredPermissions authorizeSelection)

abbrev ActorID := String
abbrev Path := List String

def maxPathDepth : Nat := 1000

def isControlChar (c : Char) : Bool :=
  decide (c.toNat < 32) || c.toNat == 127

def validActor (a : ActorID) : Bool :=
  a != "" && a.toList.any (fun c => !c.isWhitespace) && !(a.toList.any isControlChar)

def validSeg (s : String) : Bool :=
  match s.toList with
  | [] => false
  | c :: cs => c != '$' && !(c :: cs).any isControlChar

def validPath (p : Path) : Bool :=
  !p.isEmpty && decide (p.length ≤ maxPathDepth + 1) && p.all validSeg

def inEntranceSpec : Path → Bool
  | ["meta", "labels", k] => validSeg k
  | "spec" :: seg :: rest =>
    decide (rest.length + 2 ≤ maxPathDepth + 1) && (seg :: rest).all validSeg
  | _ => false

def pathLt : Path → Path → Bool
  | [], [] => false
  | [], _ :: _ => true
  | _ :: _, [] => false
  | a :: as, b :: bs => if a == b then pathLt as bs else decide (a < b)

def isPrefixOf : Path → Path → Bool
  | [], _ => true
  | _ :: _, [] => false
  | a :: as, b :: bs => a == b && isPrefixOf as bs

def isProperAncestor (a b : Path) : Bool :=
  !a.isEmpty && decide (a.length < b.length) && isPrefixOf a b

def overlaps (a b : Path) : Bool :=
  a == b || isProperAncestor a b || isProperAncestor b a

def insertSortedPath (p : Path) : List Path → List Path
  | [] => [p]
  | q :: qs =>
    if p == q then q :: qs
    else if pathLt p q then p :: q :: qs
    else q :: insertSortedPath p qs

def sortDedupPaths : List Path → List Path
  | [] => []
  | p :: ps =>
    let tail := sortDedupPaths ps
    insertSortedPath p (tail.filter (fun q => q != p))

def insertSortedPair {α : Type} (p : Path) (v : α) : List (Path × α) → List (Path × α)
  | [] => [(p, v)]
  | (q, w) :: rest =>
    if p == q then (p, v) :: rest
    else if pathLt p q then (p, v) :: (q, w) :: rest
    else (q, w) :: insertSortedPair p v rest

def removePath {α : Type} : List (Path × α) → Path → List (Path × α)
  | [], _ => []
  | (q, v) :: rest, p => if q == p then removePath rest p else (q, v) :: removePath rest p

def sortPairs {α : Type} : List (Path × α) → List (Path × α)
  | [] => []
  | (p, v) :: rest => insertSortedPair p v (removePath (sortPairs rest) p)

def lookupPath {α : Type} : List (Path × α) → Path → Option α
  | [], _ => none
  | (q, v) :: rest, p => if q == p then some v else lookupPath rest p

inductive DiagCode where
  | K101
  | K102
  | K201
  | K202
  | K203
  | K204
  | K206
  | K301
  | K302
  | K401
  | K502
  | K602
  | K603
  | K604
  deriving DecidableEq, Repr, Inhabited

/-- Priority rank for diagnostics: `K301 > K201 > K204 > K202/K302 > K206 > K203 > K401`. -/
def DiagCode.rank : DiagCode → Nat
  | .K301 => 1
  | .K201 => 2
  | .K204 => 3
  | .K202 => 4
  | .K302 => 4
  | .K206 => 5
  | .K203 => 6
  | .K401 => 7
  | .K101 => 8
  | .K102 => 8
  | .K502 => 9
  | .K602 => 10
  | .K603 => 10
  | .K604 => 10

def DiagCode.codeStr : DiagCode → String
  | .K101 => "K101"
  | .K102 => "K102"
  | .K201 => "K201"
  | .K202 => "K202"
  | .K203 => "K203"
  | .K204 => "K204"
  | .K206 => "K206"
  | .K301 => "K301"
  | .K302 => "K302"
  | .K401 => "K401"
  | .K502 => "K502"
  | .K602 => "K602"
  | .K603 => "K603"
  | .K604 => "K604"

structure Diagnostic where
  code : DiagCode
  path : Path
  deriving DecidableEq, Repr, Inhabited

def diagLt (a b : Diagnostic) : Bool :=
  if a.path == b.path then decide (a.code.codeStr < b.code.codeStr)
  else pathLt a.path b.path

def insertDiag (d : Diagnostic) : List Diagnostic → List Diagnostic
  | [] => [d]
  | x :: xs =>
    if d == x then x :: xs
    else if diagLt d x then d :: x :: xs
    else x :: insertDiag d xs

def sortDiags : List Diagnostic → List Diagnostic
  | [] => []
  | d :: ds => insertDiag d (sortDiags ds)

def primaryDiagCode : List Diagnostic → Option DiagCode
  | [] => none
  | d :: rest =>
    some (rest.foldl (fun best cur => if cur.code.rank < best.rank then cur.code else best) d.code)

def primaryCodeOfCodes : List DiagCode → Option DiagCode
  | [] => none
  | c :: rest =>
    some (rest.foldl (fun best cur => if cur.rank < best.rank then cur else best) c)

def permUnion (a b : PermSet) : PermSet :=
  { set := a.set || b.set
    delete := a.delete || b.delete
    own := a.own || b.own
    resolve := a.resolve || b.resolve }

def permNonEmpty (p : PermSet) : Bool :=
  p.set || p.delete || p.own || p.resolve

inductive MatchMode where
  | root
  | subtree
  | exact
  deriving DecidableEq, Repr, Inhabited

structure ScopeRule where
  matchMode : MatchMode
  prefixPath : Path
  perms : PermSet
  deriving DecidableEq, Repr, Inhabited

abbrev Scope := List ScopeRule

def ScopeRule.valid (r : ScopeRule) : Bool :=
  permNonEmpty r.perms &&
    match r.matchMode with
    | .root => r.prefixPath.isEmpty
    | .subtree => validPath r.prefixPath
    | .exact => validPath r.prefixPath

def Scope.valid (s : Scope) : Bool :=
  s.all ScopeRule.valid

def ScopeRule.matches (r : ScopeRule) (p : Path) : Bool :=
  if !r.valid || !inEntranceSpec p then false
  else
    match r.matchMode with
    | .root => true
    | .exact => r.prefixPath == p
    | .subtree => r.prefixPath == p || isProperAncestor r.prefixPath p

def Scope.permissionsFor (s : Scope) (p : Path) : PermSet :=
  if !inEntranceSpec p then permNone
  else s.foldl (fun acc r => if r.matches p then permUnion acc r.perms else acc) permNone

def Scope.allows (s : Scope) (p : Path) (req : PermSet) : Bool :=
  inEntranceSpec p && PermSet.allows (s.permissionsFor p) req

/-! ### EntranceSpec Leaf Projection and Document Tree Operations -/

def scalarKind : Value → Option Nat
  | .str _ => some 1
  | .int _ => some 2
  | .dec _ => some 3
  | .bool _ => some 4
  | .enum _ => some 5
  | .qty _ _ => some 6
  | .ref _ => some 7
  | _ => none

def validScalarListAux (expected : Option Nat) : Values → Bool
  | .nil => true
  | .cons v rest =>
    match scalarKind v with
    | none => false
    | some k =>
      match expected with
      | none => validScalarListAux (some k) rest
      | some ek => ek == k && validScalarListAux (some ek) rest

def validLeafValue : Value → Bool
  | .record _ => false
  | .list vs => validScalarListAux none vs
  | _ => true

mutual
  def projectSpecValue (pfx : Path) (rejectNestedEmpty : Bool) :
      Value → Except DiagCode (List (Path × Value))
    | .record .nil =>
      if rejectNestedEmpty then .error .K102 else .ok []
    | .record fs =>
      if decide (pfx.length > maxPathDepth) then .error .K101
      else projectSpecFields pfx rejectNestedEmpty fs
    | v =>
      if !validLeafValue v then .error .K101
      else .ok [(pfx, v)]

  def projectSpecFields (pfx : Path) (rejectNestedEmpty : Bool) :
      Fields → Except DiagCode (List (Path × Value))
    | .nil => .ok []
    | .cons k v rest =>
      if !validSeg k then .error .K101
      else do
        let head ← projectSpecValue (pfx ++ [k]) rejectNestedEmpty v
        let tail ← projectSpecFields pfx rejectNestedEmpty rest
        .ok (head ++ tail)
end

def projectLabelsFields (rejectNestedEmpty : Bool) :
    Fields → Except DiagCode (List (Path × Value))
  | .nil => .ok []
  | .cons k v rest =>
    if !validSeg k then .error .K101
    else
      match v with
      | .record .nil =>
        if rejectNestedEmpty then .error .K102 else .error .K101
      | .record _ => .error .K101
      | leaf =>
        if !validLeafValue leaf then .error .K101
        else do
          let tail ← projectLabelsFields rejectNestedEmpty rest
          .ok ((["meta", "labels", k], leaf) :: tail)

def projectEntranceSpec (doc : Value) (rejectNestedEmpty : Bool) :
    Except DiagCode (List (Path × Value)) :=
  if !doc.unique then .error .K101
  else
    match doc with
    | .record top => do
      let labelLeaves ← match top.get "meta" with
        | none => .ok []
        | some (.record m) =>
          match m.get "labels" with
          | none => .ok []
          | some (.record .nil) => .ok []
          | some (.record ls) => projectLabelsFields rejectNestedEmpty ls
          | some _ => .error .K101
        | some _ => .error .K101
      let specLeaves ← match top.get "spec" with
        | none => .ok []
        | some (.record .nil) => .ok []
        | some (.record sp) => projectSpecFields ["spec"] rejectNestedEmpty sp
        | some _ => .error .K101
      .ok (sortPairs (labelLeaves ++ specLeaves))
    | _ => .error .K101

inductive Obs where
  | absent
  | leaf (v : Value)
  | record
  | opaque
  | unknown

inductive AncObs where
  | clear
  | nonRecord
  | opaqueAnc
  | unknownAnc
  deriving DecidableEq, Repr, Inhabited

def isObsUnknown (obs : Obs) (anc : AncObs) : Bool :=
  match obs, anc with
  | .unknown, _ => true
  | _, .unknownAnc => true
  | _, _ => false

def isProtectedStructure (obs : Obs) (anc : AncObs) : Bool :=
  match obs, anc with
  | _, .nonRecord => true
  | _, .opaqueAnc => true
  | .record, _ => true
  | .opaque, _ => true
  | _, _ => false

def valueToObs : Value → Obs
  | .record _ => .record
  | v => if validLeafValue v then .leaf v else .opaque

def observeDoc : Value → Path → Obs × AncObs
  | _, [] => (.absent, .clear)
  | .record fs, [k] =>
    match fs.get k with
    | none => (.absent, .clear)
    | some v => (valueToObs v, .clear)
  | .record fs, k :: k2 :: rest =>
    match fs.get k with
    | none => (.absent, .clear)
    | some (.record sub) => observeDoc (.record sub) (k2 :: rest)
    | some v =>
      if validLeafValue v then (.absent, .nonRecord)
      else (.absent, .opaqueAnc)
  | v, _ :: _ =>
    if validLeafValue v then (.absent, .nonRecord)
    else (.absent, .opaqueAnc)

def replaceField : Fields → String → Value → Fields
  | .nil, _, _ => .nil
  | .cons k v rest, key, val =>
    if k = key then .cons k val rest
    else .cons k v (replaceField rest key val)

def appendField : Fields → String → Value → Fields
  | .nil, key, val => .cons key val .nil
  | .cons k v rest, key, val => .cons k v (appendField rest key val)

def upsertField (fs : Fields) (key : String) (val : Value) : Fields :=
  if (fs.get key).isSome then replaceField fs key val else appendField fs key val

def removeField : Fields → String → Fields
  | .nil, _ => .nil
  | .cons k v rest, key =>
    if k = key then rest
    else .cons k v (removeField rest key)

def setFieldsAt : Fields → Path → Value → Fields
  | fs, [], _ => fs
  | fs, [k], v => upsertField fs k v
  | fs, k :: k2 :: rest, v =>
    match fs.get k with
    | some (.record sub) => replaceField fs k (.record (setFieldsAt sub (k2 :: rest) v))
    | none => appendField fs k (.record (setFieldsAt .nil (k2 :: rest) v))
    | some _ => fs

def setDocLeaf (doc : Value) (p : Path) (v : Value) : Value :=
  match doc with
  | .record fs => .record (setFieldsAt fs p v)
  | other => other

/-! ### Ownership State and Bottom-Up Pruning -/

inductive LeafStatus where
  | owned (manager : ActorID) (origin : Origin)
  | unowned
  | defaulted
  deriving DecidableEq, Repr, Inhabited

structure OwnerState where
  creator : ActorID
  entries : List (Path × LeafStatus)
  deriving DecidableEq, Repr, Inhabited

def OwnerState.get (ow : OwnerState) (p : Path) : Option LeafStatus :=
  lookupPath ow.entries p

def OwnerState.set (ow : OwnerState) (p : Path) (st : LeafStatus) : OwnerState :=
  { ow with entries := insertSortedPair p st (removePath ow.entries p) }

def OwnerState.remove (ow : OwnerState) (p : Path) : OwnerState :=
  { ow with entries := removePath ow.entries p }

def OwnerState.actorOwnedPaths (ow : OwnerState) (actor : ActorID) : List Path :=
  ow.entries.filterMap fun
    | (p, .owned m _) => if m == actor then some p else none
    | _ => none

def OwnerState.valid (ow : OwnerState) : Bool :=
  validActor ow.creator &&
    ow.entries.all (fun (p, st) =>
      inEntranceSpec p &&
      match st with
      | .owned m _ => validActor m
      | .unowned => true
      | .defaulted => true)

mutual
  def onlyDefaultedValue (pfx : Path) (ow : OwnerState) : Value → Bool
    | .record fs => onlyDefaultedFields pfx ow fs
    | _ => ow.get pfx == some .defaulted

  def onlyDefaultedFields (pfx : Path) (ow : OwnerState) : Fields → Bool
    | .nil => true
    | .cons k v rest => onlyDefaultedValue (pfx ++ [k]) ow v && onlyDefaultedFields pfx ow rest
end

mutual
  def removeDefaultedValue (pfx : Path) (ow : OwnerState) : Value → OwnerState
    | .record fs => removeDefaultedFields pfx ow fs
    | _ => ow.remove pfx

  def removeDefaultedFields (pfx : Path) (ow : OwnerState) : Fields → OwnerState
    | .nil => ow
    | .cons k v rest => removeDefaultedFields pfx (removeDefaultedValue (pfx ++ [k]) ow v) rest
end

/-- Remove a leaf inside `spec`, pruning emptied or defaulted-only intermediate records bottom-up. -/
def deletePruneSub (pfx : Path) (fs : Fields) (p : Path) (ow : OwnerState) : Fields × OwnerState :=
  match p with
  | [] => (fs, ow)
  | [k] => (removeField fs k, ow)
  | k :: k2 :: rest =>
    match fs.get k with
    | some (.record sub) =>
      let childPfx := pfx ++ [k]
      let (sub', ow1) := deletePruneSub childPfx sub (k2 :: rest) ow
      match sub' with
      | .nil => (removeField fs k, ow1)
      | _ =>
        if onlyDefaultedFields childPfx ow1 sub' then
          (removeField fs k, removeDefaultedFields childPfx ow1 sub')
        else
          (replaceField fs k (.record sub'), ow1)
    | _ => (fs, ow)

/-- Delete a pruned leaf in `EntranceSpec`: under `spec` prunes emptied (or defaulted-only)
    intermediate records while keeping top-level `spec = {}`; under `meta/labels` removes
    `labels` when emptied. -/
def deletePruneDoc (doc : Value) (p : Path) (ow : OwnerState) : Value × OwnerState :=
  match doc, p with
  | .record top, "spec" :: k :: rest =>
    match top.get "spec" with
    | some (.record sp) =>
      let (sp', ow') := deletePruneSub ["spec"] sp (k :: rest) ow
      (.record (replaceField top "spec" (.record sp')), ow')
    | _ => (doc, ow)
  | .record top, ["meta", "labels", k] =>
    match top.get "meta" with
    | some (.record m) =>
      match m.get "labels" with
      | some (.record ls) =>
        let ls' := removeField ls k
        let m' := match ls' with
          | .nil => removeField m "labels"
          | _ => replaceField m "labels" (.record ls')
        (.record (replaceField top "meta" (.record m')), ow)
      | _ => (doc, ow)
    | _ => (doc, ow)
  | _, _ => (doc, ow)

/-! ### Multi-Path 4-Stage Planning Core -/

def inActiveDomain (actor : ActorID) (declared : Option Value) (status : Option LeafStatus) : Bool :=
  match declared, status with
  | some _, _ => true
  | none, some (.owned m _) => m == actor
  | none, _ => false

inductive StateClass where
  | outsideDomain
  | converged
  | declaredSet
  | declaredAdoptModified
  | declaredConflict
  | deferredUntilRelease
  | retiredModifiedKnown
  | retiredModifiedUnknown
  | retiredCreatedAbsent
  | retiredCreatedPrunable
  | retiredCreatedAnomaly
  | migrationRequired
  deriving DecidableEq, Repr, Inhabited

inductive SelfOverlap where
  | none
  | retiredCreated
  | retiredModified
  deriving DecidableEq, Repr, Inhabited

def obsDiags (obs : Obs) (anc : AncObs) : List DiagCode :=
  let ancDs := match anc with
    | .clear => []
    | .nonRecord => [.K202]
    | .opaqueAnc => [.K302]
    | .unknownAnc => [.K301]
  let obsDs := match obs with
    | .absent => []
    | .leaf _ => []
    | .record => [.K202]
    | .opaque => [.K302]
    | .unknown => [.K301]
  ancDs ++ obsDs

def classifyLeaf
    (actor : ActorID)
    (declared : Option Value)
    (obs : Obs)
    (anc : AncObs)
    (status : Option LeafStatus)
    (lastApplied : Option Value)
    (hasForeignOverlap : Bool)
    (selfOverlap : SelfOverlap) : StateClass × List DiagCode :=
  let baseDs := obsDiags obs anc
  match declared with
  | some desired =>
    let foreignAtSelf := match status with
      | some (.owned m _) => m != actor
      | _ => false
    let hasForeign := foreignAtSelf || hasForeignOverlap
    let withForeign := if hasForeign then .K201 :: baseDs else baseDs
    match selfOverlap with
    | .retiredCreated =>
      (.migrationRequired, .K204 :: withForeign)
    | .retiredModified =>
      if hasForeign then (.declaredConflict, withForeign)
      else (.deferredUntilRelease, .K603 :: baseDs)
    | .none =>
      if hasForeign then (.declaredConflict, withForeign)
      else
        match status with
        | some (.owned m _) =>
          if m != actor then (.declaredConflict, .K201 :: baseDs)
          else if isObsUnknown obs anc || isProtectedStructure obs anc then
            (.declaredConflict, baseDs)
          else
            match obs with
            | .leaf actual =>
              let drifted := match lastApplied with
                | none => false
                | some la => !(Value.eqv actual la)
              if drifted then (.declaredConflict, .K203 :: baseDs)
              else if Value.eqv actual desired then (.converged, [])
              else (.declaredSet, [])
            | .absent => (.declaredConflict, .K203 :: baseDs)
            | _ => (.declaredConflict, baseDs)
        | some .defaulted =>
          if isObsUnknown obs anc || isProtectedStructure obs anc then
            (.declaredConflict, baseDs)
          else
            match obs with
            | .absent => (.declaredSet, [])
            | .leaf _ => (.declaredSet, [])
            | _ => (.declaredConflict, baseDs)
        | none | some .unowned =>
          if isObsUnknown obs anc || isProtectedStructure obs anc then
            (.declaredConflict, baseDs)
          else
            match obs with
            | .absent => (.declaredSet, [])
            | .leaf actual =>
              if Value.eqv actual desired then (.declaredAdoptModified, [])
              else (.declaredConflict, .K206 :: baseDs)
            | _ => (.declaredConflict, baseDs)
  | none =>
    match status with
    | some (.owned m orig) =>
      if m != actor then (.outsideDomain, [])
      else
        match orig with
        | .modified =>
          if isObsUnknown obs anc then (.retiredModifiedUnknown, [.K301])
          else (.retiredModifiedKnown, [])
        | .created =>
          if selfOverlap == .retiredCreated then
            let driftDs := match obs, lastApplied with
              | .leaf actual, some la => if Value.eqv actual la then [] else [.K203]
              | _, _ => []
            (.migrationRequired, .K204 :: (driftDs ++ baseDs))
          else if hasForeignOverlap then
            (.retiredCreatedAnomaly, .K201 :: baseDs)
          else if isObsUnknown obs anc || isProtectedStructure obs anc then
            (.retiredCreatedAnomaly, baseDs)
          else
            match obs with
            | .absent => (.retiredCreatedAbsent, [])
            | .leaf actual =>
              let drifted := match lastApplied with
                | none => false
                | some la => !(Value.eqv actual la)
              if drifted then (.retiredCreatedAnomaly, .K203 :: baseDs)
              else (.retiredCreatedPrunable, [])
            | _ => (.retiredCreatedAnomaly, baseDs)
    | _ => (.outsideDomain, [])

def selectModeKind (sc : StateClass) (mode : Mode) : SelectionKind :=
  match sc, mode with
  | .outsideDomain, _ => .noOp
  | .converged, _ => .noOp
  | .declaredSet, .apply => .candidate
  | .declaredSet, .prune => .deferred
  | .declaredAdoptModified, .apply => .candidate
  | .declaredAdoptModified, .prune => .deferred
  | .declaredConflict, .apply => .blocker
  | .declaredConflict, .prune => .deferred
  | .deferredUntilRelease, _ => .deferred
  | .retiredModifiedKnown, .apply => .candidate
  | .retiredModifiedKnown, .prune => .deferred
  | .retiredModifiedUnknown, .apply => .blocker
  | .retiredModifiedUnknown, .prune => .deferred
  | .retiredCreatedAbsent, _ => .candidate
  | .retiredCreatedPrunable, .apply => .deferred
  | .retiredCreatedPrunable, .prune => .candidate
  | .retiredCreatedAnomaly, .apply => .deferred
  | .retiredCreatedAnomaly, .prune => .blocker
  | .migrationRequired, _ => .blocker

def selectModeAction (sc : StateClass) : ActionKind :=
  match sc with
  | .declaredSet => .setLeaf
  | .declaredAdoptModified => .adoptModified
  | .retiredModifiedKnown => .unmanageModified
  | .retiredCreatedAbsent => .clearAbsentRecord
  | .retiredCreatedPrunable => .deleteLeaf
  | _ => .none

def selectModePerms (sc : StateClass) (mode : Mode) : PermSet :=
  match selectModeKind sc mode with
  | .candidate => requiredPermissions (selectModeAction sc)
  | _ => permNone

structure LeafInput where
  path : Path
  actor : ActorID
  declared : Option Value
  obs : Obs
  anc : AncObs
  status : Option LeafStatus
  lastApplied : Option Value := none
  hasForeignOverlap : Bool := false
  selfOverlap : SelfOverlap := .none
  granted : PermSet

inductive DocOp where
  | set (path : Path) (value : Value)
  | delete (path : Path)

def DocOp.path : DocOp → Path
  | .set p _ => p
  | .delete p => p

inductive OwnerOp where
  | createOwned (path : Path) (actor : ActorID)
  | keepOwned (path : Path) (actor : ActorID) (origin : Origin)
  | adoptOwned (path : Path) (actor : ActorID)
  | setUnowned (path : Path)
  | remove (path : Path)
  deriving DecidableEq, Repr, Inhabited

def OwnerOp.path : OwnerOp → Path
  | .createOwned p _ => p
  | .keepOwned p _ _ => p
  | .adoptOwned p _ => p
  | .setUnowned p => p
  | .remove p => p

def buildDocOp (path : Path) (dec : DecisionKind) (act : ActionKind)
    (declared : Option Value) (obs : Obs) : Option DocOp :=
  match dec with
  | .authorized =>
    match act, declared, obs with
    | .setLeaf, some desired, .absent => some (.set path desired)
    | .setLeaf, some desired, .leaf actual =>
      if Value.eqv actual desired then none else some (.set path desired)
    | .deleteLeaf, none, .leaf _ => some (.delete path)
    | _, _, _ => none
  | _ => none

def buildOwnerOp (path : Path) (actor : ActorID) (dec : DecisionKind) (act : ActionKind)
    (status : Option LeafStatus) (obs : Obs) : Option OwnerOp :=
  match dec with
  | .authorized =>
    match act with
    | .setLeaf =>
      match status with
      | some (.owned m orig) =>
        if m == actor then some (.keepOwned path actor orig) else none
      | _ => some (.createOwned path actor)
    | .adoptModified => some (.adoptOwned path actor)
    | .deleteLeaf => some (.remove path)
    | .unmanageModified =>
      match obs with
      | .absent => some (.remove path)
      | _ => some (.setUnowned path)
    | .clearAbsentRecord => some (.remove path)
    | _ => none
  | _ => none

structure LeafPlan where
  input : LeafInput
  inMA : Bool
  stateClass : StateClass
  selection : SelectionKind
  action : ActionKind
  requiredPerms : PermSet
  decision : DecisionKind
  docOp : Option DocOp
  ownerOp : Option OwnerOp
  diagnostics : List Diagnostic
  primaryCode : Option DiagCode

def planLeaf (inp : LeafInput) (mode : Mode) : LeafPlan :=
  let inMA := inActiveDomain inp.actor inp.declared inp.status
  let (sc, rawCodes) := classifyLeaf inp.actor inp.declared inp.obs inp.anc
    inp.status inp.lastApplied inp.hasForeignOverlap inp.selfOverlap
  let sel := selectModeKind sc mode
  let act := selectModeAction sc
  let reqPerms := selectModePerms sc mode
  let dec := authorizeSelection sel reqPerms inp.granted
  let modeCodes :=
    if sc == .retiredCreatedPrunable && mode == .apply then .K604 :: rawCodes
    else rawCodes
  let allCodes :=
    if dec == .policyDenied then .K401 :: modeCodes
    else modeCodes
  let diags := sortDiags (allCodes.map (fun c => { code := c, path := inp.path }))
  let primary :=
    if dec == .policyDenied then some .K401
    else if sc == .retiredCreatedPrunable && mode == .apply then some .K604
    else if sc == .deferredUntilRelease then some .K603
    else primaryDiagCode diags
  { input := inp
    inMA := inMA
    stateClass := sc
    selection := sel
    action := act
    requiredPerms := reqPerms
    decision := dec
    docOp := buildDocOp inp.path dec act inp.declared inp.obs
    ownerOp := buildOwnerOp inp.path inp.actor dec act inp.status inp.obs
    diagnostics := diags
    primaryCode := primary }

def isBlockingDecision (d : DecisionKind) : Bool :=
  d == .blocker || d == .policyDenied

structure GateResult where
  blocked : Bool
  docOps : List DocOp
  ownerOps : List OwnerOp
  diagnostics : List Diagnostic
  primaryCode : Option DiagCode

def gatePlan (plans : List LeafPlan) (expectMatch ownerRevMatch : Bool) : GateResult :=
  let preDiags : List Diagnostic :=
    (if !ownerRevMatch then [{ code := .K201, path := [] }] else []) ++
    (if !expectMatch then [{ code := .K203, path := [] }] else [])
  let planDiags := plans.flatMap LeafPlan.diagnostics
  let allDiags := sortDiags (preDiags ++ planDiags)
  let blockingDiags := sortDiags (preDiags ++
    (plans.filter (fun p => isBlockingDecision p.decision)).flatMap LeafPlan.diagnostics)
  let hasBlocker := !ownerRevMatch || !expectMatch ||
    plans.any (fun p => isBlockingDecision p.decision)
  if hasBlocker then
    { blocked := true
      docOps := []
      ownerOps := []
      diagnostics := allDiags
      primaryCode := primaryDiagCode blockingDiags }
  else
    { blocked := false
      docOps := plans.filterMap LeafPlan.docOp
      ownerOps := plans.filterMap LeafPlan.ownerOp
      diagnostics := allDiags
      primaryCode := primaryDiagCode allDiags }

def computeForeignOverlap (ow : OwnerState) (actor : ActorID) (p : Path) : Bool :=
  ow.entries.any fun
    | (q, .owned m _) => m != actor && overlaps p q
    | _ => false

def computeSelfOverlap (ow : OwnerState) (actor : ActorID) (decl : List (Path × Value))
    (p : Path) : SelfOverlap :=
  match lookupPath decl p with
  | some _ =>
    let hasRetCreated := ow.entries.any fun
      | (q, .owned m .created) => m == actor && q != p && overlaps p q
      | _ => false
    if hasRetCreated then .retiredCreated
    else
      let hasRetModified := ow.entries.any fun
        | (q, .owned m .modified) => m == actor && q != p && overlaps p q
        | _ => false
      if hasRetModified then .retiredModified
      else .none
  | none =>
    if decl.any (fun (q, _) => q != p && overlaps p q) then .retiredCreated
    else .none

def applyDocOwnerStep (state : Value × OwnerState) : DocOp → Value × OwnerState
  | .set p v => (setDocLeaf state.1 p v, state.2)
  | .delete p => deletePruneDoc state.1 p state.2

def applyOwnerOp (ow : OwnerState) : OwnerOp → OwnerState
  | .createOwned p a => ow.set p (.owned a .created)
  | .keepOwned p a orig => ow.set p (.owned a orig)
  | .adoptOwned p a => ow.set p (.owned a .modified)
  | .setUnowned p => ow.set p .unowned
  | .remove p => ow.remove p

def applyOwnerOps (ow : OwnerState) (ops : List OwnerOp) : OwnerState :=
  ops.foldl applyOwnerOp ow

def applyPlanChanges (doc : Value) (ow : OwnerState)
    (docOps : List DocOp) (ownerOps : List OwnerOp) : Value × OwnerState :=
  let ow1 := applyOwnerOps ow ownerOps
  docOps.foldl applyDocOwnerStep (doc, ow1)

structure PlanOutput where
  blocked : Bool
  docOps : List DocOp
  ownerOps : List OwnerOp
  nextDoc : Value
  nextOwner : OwnerState
  diagnostics : List Diagnostic
  primaryCode : Option DiagCode

def planMulti (doc : Value) (ow : OwnerState) (declDoc : Value)
    (actor : ActorID) (scope : Scope) (mode : Mode)
    (expectMatch ownerRevMatch : Bool) : Except DiagCode PlanOutput := do
  if !validActor actor || !scope.valid || !ow.valid || !doc.unique then
    .error .K101
  else if !ownerRevMatch then
    .error .K201
  else if !expectMatch then
    .error .K203
  else
    let decl ← projectEntranceSpec declDoc true
    let evalPaths := sortDedupPaths (decl.map Prod.fst ++ ow.actorOwnedPaths actor)
    let leafPlans := evalPaths.map fun p =>
      let (obs, anc) := observeDoc doc p
      let isDecl := (lookupPath decl p).isSome
      let inp : LeafInput := {
        path := p
        actor := actor
        declared := lookupPath decl p
        obs := obs
        anc := anc
        status := ow.get p
        lastApplied := none
        hasForeignOverlap := if isDecl then computeForeignOverlap ow actor p else false
        selfOverlap := computeSelfOverlap ow actor decl p
        granted := scope.permissionsFor p
      }
      planLeaf inp mode
    let gate := gatePlan leafPlans true true
    if gate.blocked then
      .ok {
        blocked := true
        docOps := []
        ownerOps := []
        nextDoc := doc
        nextOwner := ow
        diagnostics := gate.diagnostics
        primaryCode := gate.primaryCode
      }
    else
      let (nextDoc, nextOwner) := applyPlanChanges doc ow gate.docOps gate.ownerOps
      .ok {
        blocked := false
        docOps := gate.docOps
        ownerOps := gate.ownerOps
        nextDoc := nextDoc
        nextOwner := nextOwner
        diagnostics := gate.diagnostics
        primaryCode := gate.primaryCode
      }

/-! ### Explicit Resolution (`Adopt` and `Release`) -/

inductive ResolveKind where
  | adopt
  | release
  deriving DecidableEq, Repr, Inhabited

structure ResolveRequest where
  kind : ResolveKind
  actor : ActorID
  scope : Scope
  path : Path
  doc : Value
  owner : OwnerState
  expectMatch : Bool
  ownerRevMatch : Bool

structure ResolveResult where
  accepted : Bool
  code : Option DiagCode
  nextDoc : Value
  nextOwner : OwnerState
  notices : List Diagnostic

def hasAdoptOverlap (ow : OwnerState) (actor : ActorID) (p : Path) : Bool :=
  ow.entries.any fun
    | (q, .owned m _) =>
      if m != actor then overlaps p q
      else q != p && overlaps p q
    | _ => false

def planResolve (req : ResolveRequest) : ResolveResult :=
  let reject (c : DiagCode) : ResolveResult :=
    { accepted := false
      code := some c
      nextDoc := req.doc
      nextOwner := req.owner
      notices := [{ code := c, path := req.path }] }
  if !validActor req.actor || !req.scope.valid || !inEntranceSpec req.path
      || !req.owner.valid || !req.doc.unique then
    reject .K101
  else if !req.scope.allows req.path permOwnResolve then
    reject .K401
  else
    let (obs, anc) := observeDoc req.doc req.path
    match req.kind with
    | .adopt =>
      if hasAdoptOverlap req.owner req.actor req.path then
        reject .K201
      else if !req.ownerRevMatch then
        reject .K201
      else
        let structDs := obsDiags obs anc
        match primaryCodeOfCodes structDs with
        | some c => reject c
        | none =>
          match obs with
          | .leaf _ =>
            if !req.expectMatch then reject .K203
            else
              let waived := match req.owner.get req.path with
                | some (.owned m .created) => m == req.actor
                | _ => false
              let notices := if waived then [{ code := .K602, path := req.path }] else []
              { accepted := true
                code := none
                nextDoc := req.doc
                nextOwner := req.owner.set req.path (.owned req.actor .modified)
                notices := notices }
          | _ => reject .K202
    | .release =>
      match req.owner.get req.path with
      | some (.owned m orig) =>
        if m != req.actor then reject .K201
        else if !req.ownerRevMatch then reject .K201
        else if isObsUnknown obs anc then reject .K301
        else if !req.expectMatch then reject .K203
        else
          let nextOw := match obs with
            | .absent => req.owner.remove req.path
            | _ => req.owner.set req.path .unowned
          let notices := if orig == .created then [{ code := .K602, path := req.path }] else []
          { accepted := true
            code := none
            nextDoc := req.doc
            nextOwner := nextOw
            notices := notices }
      | _ => reject .K201

/-! ### Imperative Update / Patch Ownership Check -/

def checkImperative (actor : ActorID) (scope : Scope)
    (oldDoc : Value) (oldOwner : OwnerState) (nextDoc : Value) : Except DiagCode OwnerState := do
  if !validActor actor || !scope.valid || !oldOwner.valid || !oldDoc.unique || !nextDoc.unique then
    .error .K101
  else
    let oldLeaves ← projectEntranceSpec oldDoc false
    let nextLeaves ← projectEntranceSpec nextDoc false
    let stepNext (acc : OwnerState × List Diagnostic) (entry : Path × Value) :
        OwnerState × List Diagnostic :=
      let (ow, diags) := acc
      let (p, nextVal) := entry
      match lookupPath oldLeaves p with
      | some oldVal =>
        if Value.eqv oldVal nextVal then (ow, diags)
        else
          match oldOwner.get p with
          | some (.owned m orig) =>
            if m != actor then (ow, { code := .K201, path := p } :: diags)
            else if scope.allows p permSetOwn then (ow.set p (.owned actor orig), diags)
            else (ow, { code := .K401, path := p } :: diags)
          | some .defaulted =>
            if scope.allows p permSetOwn then (ow.set p (.owned actor .created), diags)
            else (ow, { code := .K401, path := p } :: diags)
          | _ => (ow, { code := .K206, path := p } :: diags)
      | none =>
        let overlapDiags := oldOwner.entries.filterMap fun (op, oe) =>
          if !overlaps op p then none
          else
            match oe with
            | .owned m orig =>
              if m != actor then some { code := .K201, path := p }
              else if op == p then none
              else if (lookupPath oldLeaves op).isNone then
                some { code := if orig == .created then .K204 else .K201, path := p }
              else none
            | .unowned => some { code := .K206, path := p }
            | .defaulted => none
        if !overlapDiags.isEmpty then (ow, overlapDiags ++ diags)
        else if scope.allows p permSetOwn then
          let orig := match oldOwner.get p with
            | some (.owned m o) => if m == actor then o else .created
            | _ => .created
          (ow.set p (.owned actor orig), diags)
        else (ow, { code := .K401, path := p } :: diags)
    let stepDel (acc : OwnerState × List Diagnostic) (entry : Path × Value) :
        OwnerState × List Diagnostic :=
      let (ow, diags) := acc
      let (p, _) := entry
      if (lookupPath nextLeaves p).isSome then (ow, diags)
      else
        match oldOwner.get p with
        | some (.owned m _) =>
          if m != actor then (ow, { code := .K201, path := p } :: diags)
          else if scope.allows p permDeleteOwn then (ow.remove p, diags)
          else (ow, { code := .K401, path := p } :: diags)
        | some .defaulted => (ow.remove p, diags)
        | _ => (ow, { code := .K206, path := p } :: diags)
    let (ow1, d1) := nextLeaves.foldl stepNext (oldOwner, [])
    let (ow2, d2) := oldLeaves.foldl stepDel (ow1, d1)
    let sorted := sortDiags d2
    match primaryDiagCode sorted with
    | some c => .error c
    | none => .ok ow2

/-! ### Compact Trie Codec -/

inductive TrieState where
  | owned (actorIdx : Nat) (origin : Origin)
  | unowned
  | defaulted
  deriving DecidableEq, Repr, Inhabited

mutual
  inductive TrieNode where
    | mk (state : Option TrieState) (whole : Bool) (children : TrieChildren)
  inductive TrieChildren where
    | nil
    | cons (key : String) (child : TrieNode) (tail : TrieChildren)
end

structure CompactOwnership where
  actors : List ActorID
  trie : TrieChildren

def TrieChildren.get : TrieChildren → String → Option TrieNode
  | .nil, _ => none
  | .cons k v rest, key => if k = key then some v else rest.get key

def TrieChildren.replace : TrieChildren → String → TrieNode → TrieChildren
  | .nil, _, _ => .nil
  | .cons k v rest, key, val =>
    if k = key then .cons k val rest else .cons k v (rest.replace key val)

def TrieChildren.append : TrieChildren → String → TrieNode → TrieChildren
  | .nil, key, val => .cons key val .nil
  | .cons k v rest, key, val => .cons k v (rest.append key val)

def TrieChildren.upsert (cs : TrieChildren) (key : String) (val : TrieNode) : TrieChildren :=
  if (cs.get key).isSome then cs.replace key val else cs.append key val

def insertSortedStr (s : String) : List String → List String
  | [] => [s]
  | x :: xs =>
    if s == x then x :: xs
    else if decide (s < x) then s :: x :: xs
    else x :: insertSortedStr s xs

def collectForeignActors (creator : ActorID) : List (Path × LeafStatus) → List ActorID
  | [] => []
  | (_, .owned m _) :: rest =>
    let tail := collectForeignActors creator rest
    if m == creator then tail else insertSortedStr m tail
  | _ :: rest => collectForeignActors creator rest

def buildActorTable (creator : ActorID) (leaves : List (Path × LeafStatus)) : List ActorID :=
  creator :: collectForeignActors creator leaves

def actorIndexOfAux (idx : Nat) (target : ActorID) : List ActorID → Nat
  | [] => idx
  | a :: rest => if a == target then idx else actorIndexOfAux (idx + 1) target rest

def actorIndexOf (actors : List ActorID) (target : ActorID) : Nat :=
  actorIndexOfAux 0 target actors

def toTrieState (actors : List ActorID) : LeafStatus → TrieState
  | .owned m orig => .owned (actorIndexOf actors m) orig
  | .unowned => .unowned
  | .defaulted => .defaulted

def resolveTrieState (actors : List ActorID) : TrieState → Option LeafStatus
  | .owned idx orig => (actors[idx]?).map (fun a => .owned a orig)
  | .unowned => some .unowned
  | .defaulted => some .defaulted

def insertTrie : TrieChildren → Path → TrieState → TrieChildren
  | cs, [], _ => cs
  | cs, [k], ts => cs.upsert k (.mk (some ts) false .nil)
  | cs, k :: k2 :: rest, ts =>
    match cs.get k with
    | some (.mk st w sub) => cs.replace k (.mk st w (insertTrie sub (k2 :: rest) ts))
    | none => cs.append k (.mk none false (insertTrie .nil (k2 :: rest) ts))

def insertAllTrie (actors : List ActorID) : List (Path × LeafStatus) → TrieChildren
  | [] => .nil
  | (p, ls) :: rest => insertTrie (insertAllTrie actors rest) p (toTrieState actors ls)

def combineUniform : Option TrieState → Option TrieState → Option TrieState
  | some a, some b => if a == b then some a else none
  | _, _ => none

mutual
  def compressNode : TrieNode → TrieNode × Option TrieState × Nat
    | .mk st w .nil => (.mk st w .nil, st, 1)
    | .mk st w cs =>
      let (cs', u, cnt) := compressChildren cs
      match st, u with
      | none, some ts =>
        if decide (cnt ≥ 2) then (.mk (some ts) true .nil, some ts, cnt)
        else (.mk none w cs', some ts, cnt)
      | _, _ => (.mk st w cs', none, cnt)

  def compressChildren : TrieChildren → TrieChildren × Option TrieState × Nat
    | .nil => (.nil, none, 0)
    | .cons k child .nil =>
      let (c', u, cnt) := compressNode child
      (.cons k c' .nil, u, cnt)
    | .cons k child rest =>
      let (c', uc, cnt1) := compressNode child
      let (r', ur, cnt2) := compressChildren rest
      (.cons k c' r', combineUniform uc ur, cnt1 + cnt2)
end

mutual
  def dropDefaultNode : TrieNode → Option TrieNode
    | .mk (some (.owned 0 .created)) _ .nil => none
    | .mk st true cs => some (.mk st true cs)
    | .mk st false cs =>
      let cs' := dropDefaultChildren cs
      match st, cs' with
      | none, .nil => none
      | _, _ => some (.mk st false cs')

  def dropDefaultChildren : TrieChildren → TrieChildren
    | .nil => .nil
    | .cons k child rest =>
      match dropDefaultNode child with
      | none => dropDefaultChildren rest
      | some child' => .cons k child' (dropDefaultChildren rest)
end

def encodeTrie (creator : ActorID) (leaves : List (Path × LeafStatus)) : CompactOwnership :=
  let actors := buildActorTable creator leaves
  let raw := insertAllTrie actors leaves
  let (compressed, _, _) := compressChildren raw
  let compact := dropDefaultChildren compressed
  { actors := actors, trie := compact }

def lookupRaw (inh : Option TrieState) (cs : TrieChildren) : Path → Option TrieState
  | [] => inh
  | seg :: rest =>
    match cs.get seg with
    | none => inh
    | some (.mk st w sub) =>
      match rest with
      | [] => match st with | some ts => some ts | none => inh
      | _ :: _ =>
        let nextInh := if w then (match st with | some ts => some ts | none => inh) else inh
        lookupRaw nextInh sub rest

def lookupNodeRaw (inh : Option TrieState) : TrieNode → Path → Option TrieState
  | .mk st _ _, [] => match st with | some ts => some ts | none => inh
  | .mk st w sub, seg :: rest =>
    let nextInh := if w then (match st with | some ts => some ts | none => inh) else inh
    lookupRaw nextInh sub (seg :: rest)

def decodeLeaf (co : CompactOwnership) (p : Path) : Except DiagCode LeafStatus :=
  match co.actors with
  | [] => .error .K101
  | creator :: _ =>
    if !validActor creator then .error .K101
    else
      match lookupRaw none co.trie p with
      | none => .ok (.owned creator .created)
      | some ts =>
        match resolveTrieState co.actors ts with
        | some ls => .ok ls
        | none => .error .K101

mutual
  def validTrieNode : TrieNode → Bool
    | .mk _ true (.cons _ _ _) => false
    | .mk _ _ cs => validTrieChildren cs

  def validTrieChildren : TrieChildren → Bool
    | .nil => true
    | .cons _ child rest => validTrieNode child && validTrieChildren rest
end

def hasOwnedOverlapEntries : List (Path × LeafStatus) → Bool
  | [] => false
  | (p, .owned _ _) :: rest =>
    rest.any (fun (q, st) => match st with | .owned _ _ => overlaps p q | _ => false) ||
      hasOwnedOverlapEntries rest
  | _ :: rest => hasOwnedOverlapEntries rest

def decodeTrie (co : CompactOwnership) (presentLeaves : List Path) : Except DiagCode OwnerState :=
  match co.actors with
  | [] => .error .K101
  | creator :: _ =>
    if !validActor creator || !(co.actors.all validActor) || !validTrieChildren co.trie then
      .error .K101
    else do
      let pairs ← presentLeaves.mapM fun p => do
        let st ← decodeLeaf co p
        pure (p, st)
      let sorted := sortPairs pairs
      if hasOwnedOverlapEntries sorted then .error .K101
      else .ok { creator := creator, entries := sorted }

/-! ### Multi-Object Single-Txn CAS Protocol -/

structure ObjAddr where
  tenant : String
  group : String
  kind : String
  name : String
  deriving DecidableEq, Repr, Inhabited

def validSegNoSlash (s : String) : Bool :=
  s != "" && !(s.toList.contains '/')

def validAddr (a : ObjAddr) : Bool :=
  validSegNoSlash a.tenant && validSegNoSlash a.group
    && validSegNoSlash a.kind && validSegNoSlash a.name

def ObjAddr.oKey (a : ObjAddr) : String :=
  "/t/" ++ a.tenant ++ "/o/" ++ a.group ++ "/" ++ a.kind ++ "/" ++ a.name

def ObjAddr.ownKey (a : ObjAddr) : String :=
  "/t/" ++ a.tenant ++ "/own/" ++ a.group ++ "/" ++ a.kind ++ "/" ++ a.name

structure ObjRecord where
  doc : Value
  owner : OwnerState

abbrev ProvenanceMap := ObjAddr → Path → Option ActorID

structure CASState where
  store : Store
  objects : ObjAddr → Option ObjRecord
  witness : ProvenanceMap

def CASState.empty : CASState :=
  { store := Store.empty
    objects := fun _ => none
    witness := fun _ _ => none }

inductive CASOutcome where
  | committed (rev : Rev)
  | noOp
  | casConflict
  | rejected (code : DiagCode)
  deriving DecidableEq, Repr, Inhabited

def updateWitnessOp (w : Path → Option ActorID) : OwnerOp → (Path → Option ActorID)
  | .createOwned p a => fun q => if q == p then some a else w q
  | .keepOwned _ _ _ => w
  | .adoptOwned p _ => fun q => if q == p then none else w q
  | .setUnowned p => fun q => if q == p then none else w q
  | .remove p => fun q => if q == p then none else w q

def updateWitnessOps (w : Path → Option ActorID) (ops : List OwnerOp) : Path → Option ActorID :=
  ops.foldl updateWitnessOp w

def stepCreate (st : CASState) (addr : ObjAddr) (actor : ActorID) (scope : Scope)
    (doc : Value) : CASOutcome × CASState :=
  if !validAddr addr || !validActor actor || !scope.valid || !doc.unique then
    (.rejected .K101, st)
  else
    match projectEntranceSpec doc true with
    | .error c => (.rejected c, st)
    | .ok decl =>
      if !(decl.all (fun (p, _) => scope.allows p permSetOwn)) then
        (.rejected .K401, st)
      else
        let cmps : List Cmp := [
          { key := addr.oKey, op := .eq, modRev := 0 },
          { key := addr.ownKey, op := .eq, modRev := 0 }
        ]
        let writes : List Write := [.put addr.oKey "o", .put addr.ownKey "own"]
        match st.store.txn cmps writes with
        | (.ok (true, rev), store') =>
          let initEntries := decl.map (fun (p, _) => (p, LeafStatus.owned actor .created))
          let initOwner : OwnerState := { creator := actor, entries := initEntries }
          let objs' : ObjAddr → Option ObjRecord := fun a =>
            if a == addr then some { doc := doc, owner := initOwner } else st.objects a
          let wit' : ProvenanceMap := fun a p =>
            if a == addr then
              if (lookupPath decl p).isSome then some actor else none
            else st.witness a p
          (.committed rev, { store := store', objects := objs', witness := wit' })
        | (.ok (false, _), _) => (.casConflict, st)
        | (.error _, _) => (.rejected .K101, st)

def stepPlanMode (st : CASState) (addr : ObjAddr) (actor : ActorID) (scope : Scope)
    (declDoc : Value) (mode : Mode) (oModRev ownModRev : Rev) (expectMatch : Bool) :
    CASOutcome × CASState :=
  if !validAddr addr then (.rejected .K101, st)
  else
    match st.objects addr with
    | none => (.rejected .K203, st)
    | some rec =>
      match planMulti rec.doc rec.owner declDoc actor scope mode expectMatch true with
      | .error c => (.rejected c, st)
      | .ok plan =>
        if plan.blocked then
          (.rejected (plan.primaryCode.getD .K201), st)
        else if plan.docOps.isEmpty && plan.ownerOps.isEmpty then
          (.noOp, st)
        else
          let cmps : List Cmp := [
            { key := addr.oKey, op := .eq, modRev := oModRev },
            { key := addr.ownKey, op := .eq, modRev := ownModRev }
          ]
          let writes : List Write :=
            if plan.docOps.isEmpty then [.put addr.ownKey "own"]
            else [.put addr.oKey "o", .put addr.ownKey "own"]
          match st.store.txn cmps writes with
          | (.ok (true, rev), store') =>
            let objs' : ObjAddr → Option ObjRecord := fun a =>
              if a == addr then some { doc := plan.nextDoc, owner := plan.nextOwner }
              else st.objects a
            let wit' : ProvenanceMap := fun a =>
              if a == addr then updateWitnessOps (st.witness addr) plan.ownerOps
              else st.witness a
            (.committed rev, { store := store', objects := objs', witness := wit' })
          | (.ok (false, _), _) => (.casConflict, st)
          | (.error _, _) => (.rejected .K101, st)

def stepApply (st : CASState) (addr : ObjAddr) (actor : ActorID) (scope : Scope)
    (declDoc : Value) (oModRev ownModRev : Rev) (expectMatch : Bool) :
    CASOutcome × CASState :=
  stepPlanMode st addr actor scope declDoc .apply oModRev ownModRev expectMatch

def stepPrune (st : CASState) (addr : ObjAddr) (actor : ActorID) (scope : Scope)
    (declDoc : Value) (oModRev ownModRev : Rev) (expectMatch : Bool) :
    CASOutcome × CASState :=
  stepPlanMode st addr actor scope declDoc .prune oModRev ownModRev expectMatch

def stepResolveKind (st : CASState) (kind : ResolveKind) (addr : ObjAddr) (actor : ActorID)
    (scope : Scope) (path : Path) (oModRev ownModRev : Rev) (expectMatch : Bool) :
    CASOutcome × CASState :=
  if !validAddr addr then (.rejected .K101, st)
  else
    match st.objects addr with
    | none => (.rejected .K203, st)
    | some rec =>
      let res := planResolve {
        kind := kind
        actor := actor
        scope := scope
        path := path
        doc := rec.doc
        owner := rec.owner
        expectMatch := expectMatch
        ownerRevMatch := true
      }
      if !res.accepted then
        (.rejected (res.code.getD .K201), st)
      else
        let cmps : List Cmp := [
          { key := addr.oKey, op := .eq, modRev := oModRev },
          { key := addr.ownKey, op := .eq, modRev := ownModRev }
        ]
        let writes : List Write := [.put addr.ownKey "own"]
        match st.store.txn cmps writes with
        | (.ok (true, rev), store') =>
          let objs' : ObjAddr → Option ObjRecord := fun a =>
            if a == addr then some { doc := res.nextDoc, owner := res.nextOwner }
            else st.objects a
          let wit' : ProvenanceMap := fun a p =>
            if a == addr then (if p == path then none else st.witness addr p)
            else st.witness a p
          (.committed rev, { store := store', objects := objs', witness := wit' })
        | (.ok (false, _), _) => (.casConflict, st)
        | (.error _, _) => (.rejected .K101, st)

def stepAdopt (st : CASState) (addr : ObjAddr) (actor : ActorID) (scope : Scope)
    (path : Path) (oModRev ownModRev : Rev) (expectMatch : Bool) :
    CASOutcome × CASState :=
  stepResolveKind st .adopt addr actor scope path oModRev ownModRev expectMatch

def stepRelease (st : CASState) (addr : ObjAddr) (actor : ActorID) (scope : Scope)
    (path : Path) (oModRev ownModRev : Rev) (expectMatch : Bool) :
    CASOutcome × CASState :=
  stepResolveKind st .release addr actor scope path oModRev ownModRev expectMatch

def stepDelete (st : CASState) (addr : ObjAddr) (actor : ActorID) (scope : Scope)
    (oModRev ownModRev : Rev) : CASOutcome × CASState :=
  if !validAddr addr || !validActor actor || !scope.valid then
    (.rejected .K101, st)
  else
    match st.objects addr with
    | none => (.rejected .K203, st)
    | some rec =>
      let foreignPresent := rec.owner.entries.any fun
        | (_, .owned m _) => m != actor
        | _ => false
      if foreignPresent then (.rejected .K201, st)
      else if !(rec.owner.entries.all fun
        | (p, .owned _ _) => scope.allows p permDeleteOwn
        | _ => true) then
        (.rejected .K401, st)
      else
        let cmps : List Cmp := [
          { key := addr.oKey, op := .eq, modRev := oModRev },
          { key := addr.ownKey, op := .eq, modRev := ownModRev }
        ]
        let writes : List Write := [.delete addr.oKey, .delete addr.ownKey]
        match st.store.txn cmps writes with
        | (.ok (true, rev), store') =>
          let objs' : ObjAddr → Option ObjRecord := fun a =>
            if a == addr then none else st.objects a
          let wit' : ProvenanceMap := fun a p =>
            if a == addr then none else st.witness a p
          (.committed rev, { store := store', objects := objs', witness := wit' })
        | (.ok (false, _), _) => (.casConflict, st)
        | (.error _, _) => (.rejected .K101, st)

inductive CASAction where
  | create (addr : ObjAddr) (actor : ActorID) (scope : Scope) (doc : Value)
  | apply (addr : ObjAddr) (actor : ActorID) (scope : Scope) (declDoc : Value)
      (oModRev ownModRev : Rev) (expectMatch : Bool)
  | prune (addr : ObjAddr) (actor : ActorID) (scope : Scope) (declDoc : Value)
      (oModRev ownModRev : Rev) (expectMatch : Bool)
  | adopt (addr : ObjAddr) (actor : ActorID) (scope : Scope) (path : Path)
      (oModRev ownModRev : Rev) (expectMatch : Bool)
  | release (addr : ObjAddr) (actor : ActorID) (scope : Scope) (path : Path)
      (oModRev ownModRev : Rev) (expectMatch : Bool)
  | delete (addr : ObjAddr) (actor : ActorID) (scope : Scope)
      (oModRev ownModRev : Rev)

def stepCAS (st : CASState) : CASAction → CASOutcome × CASState
  | .create addr actor scope doc => stepCreate st addr actor scope doc
  | .apply addr actor scope declDoc oRev ownRev exp =>
    stepApply st addr actor scope declDoc oRev ownRev exp
  | .prune addr actor scope declDoc oRev ownRev exp =>
    stepPrune st addr actor scope declDoc oRev ownRev exp
  | .adopt addr actor scope path oRev ownRev exp =>
    stepAdopt st addr actor scope path oRev ownRev exp
  | .release addr actor scope path oRev ownRev exp =>
    stepRelease st addr actor scope path oRev ownRev exp
  | .delete addr actor scope oRev ownRev =>
    stepDelete st addr actor scope oRev ownRev

def runCAS : CASState → List CASAction → CASState
  | st, [] => st
  | st, act :: rest => runCAS (stepCAS st act).2 rest

/-! ### Protocol Invariant Definitions -/

/-- Well-formedness and single-owner exclusivity of an `OwnerState`. -/
def OwnerStateWF (ow : OwnerState) : Prop :=
  validActor ow.creator = true ∧
  ow.entries.Pairwise (fun a b => a.1 ≠ b.1) ∧
  (∀ p s, (p, s) ∈ ow.entries → inEntranceSpec p = true ∧
    (∀ m o, s = .owned m o → validActor m = true)) ∧
  (∀ p₁ m₁ o₁ p₂ m₂ o₂,
    (p₁, .owned m₁ o₁) ∈ ow.entries →
    (p₂, .owned m₂ o₂) ∈ ow.entries →
    overlaps p₁ p₂ = true →
    p₁ = p₂ ∧ m₁ = m₂ ∧ o₁ = o₂)

/-- Every `.created` ownership entry carries an active creation witness for its manager. -/
def ProvenanceWF (w : Path → Option ActorID) (ow : OwnerState) : Prop :=
  ∀ p m, (p, .owned m .created) ∈ ow.entries → w p = some m

/-- Atomic pairing of `o/` and `own/` keys in the underlying `Nayutar.Store`. -/
def StorePairingWF (st : CASState) : Prop :=
  (∀ c ∈ st.store.commits, c.rev ≤ st.store.rev) ∧
  (∀ addr, validAddr addr = true →
    ((st.store.find? st.store.rev addr.oKey).isSome = (st.objects addr).isSome) ∧
    ((st.store.find? st.store.rev addr.ownKey).isSome = (st.objects addr).isSome))

/-- Combined CAS protocol invariant across arbitrary multi-object step sequences. -/
structure CASInvariant (st : CASState) : Prop where
  pairing : StorePairingWF st
  ownerWF : ∀ addr rec, st.objects addr = some rec → OwnerStateWF rec.owner
  provenance : ∀ addr rec, st.objects addr = some rec → ProvenanceWF (st.witness addr) rec.owner

/-- Prefix-free well-formed leaf list for compact trie encoding. -/
def TrieLeavesWF (leaves : List (Path × LeafStatus)) : Prop :=
  leaves.Pairwise (fun a b => overlaps a.1 b.1 = false) ∧
  (∀ p ls, (p, ls) ∈ leaves → p ≠ [] ∧
    (∀ m o, ls = .owned m o → validActor m = true))

end Nayutar.Ownership
