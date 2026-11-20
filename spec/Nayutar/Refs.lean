import Nayutar.Envelope
import Nayutar.Keyspace

namespace Nayutar.Refs

open Nayutar.Envelope (Value Values Fields)

/-- Maximum number of distinct target objects a document's `spec` may reference. -/
def maxTargets : Nat := 32

/-- A Kubernetes-style object identity with `"core"` for the core group. -/
structure Obj where
  group : String
  kind : String
  name : String
  deriving Repr, DecidableEq

/-- Qualified kind string: `<kind>` for the core group, otherwise `<kind>.<group>`. -/
def Obj.qkind (o : Obj) : String :=
  if o.group == "core" then o.kind else s!"{o.kind}.{o.group}"

/-- A parsed reference: its target object and optional leaf path inside the target document. -/
structure Parsed where
  obj : Obj
  leaf : List String
  deriving Repr, DecidableEq

/-- Qualified kind of the parsed reference's target object. -/
def Parsed.qkind (p : Parsed) : String :=
  p.obj.qkind

def leafSegOk (s : String) : Bool :=
  match s.toList with
  | [] => false
  | c :: cs => c != '$' && c != '/' && cs.all (fun x => x != '/')

def leafOk : List String → Bool
  | [] => true
  | first :: rest =>
    (first == "meta" || first == "spec" || first == "status")
      && (first :: rest).all leafSegOk

/-- Check whether `o` satisfies the Keyspace grammar for qualified kind and name. -/
def validObj (o : Obj) : Bool :=
  match Nayutar.Keyspace.parseRel "t" s!"ref/{o.qkind}/{o.name}/k/n" with
  | some k => k.object.group == o.group && k.object.kind == o.kind && k.object.name == o.name
  | none => false

/-- Parse a `.ref segs` segment list into a target object and optional leaf path. -/
def parse : List String → Option Parsed
  | qkind :: name :: leaf =>
    match Nayutar.Keyspace.parseRel "t" s!"ref/{qkind}/{name}/k/n" with
    | some k =>
      let obj : Obj := { group := k.object.group, kind := k.object.kind, name := k.object.name }
      if leafOk leaf && validObj obj then
        some { obj := obj, leaf := leaf }
      else
        none
    | none => none
  | _ => none

/-- A reference occurrence: field keys from root, and raw segment list. -/
structure Occ where
  keys : List String
  ref : List String
  deriving Repr, DecidableEq

/-- `/`-joined path string of an occurrence. -/
def Occ.path (o : Occ) : String :=
  String.intercalate "/" o.keys

/-- Keys on the path to a reference must be non-empty and contain no `'/'`. -/
def validKeys (ks : List String) : Bool :=
  ks.all (fun k => k != "" && !(k.toList.contains '/'))

mutual
  def collectValue (keys : List String) : Value → List Occ
    | .ref segs => [{ keys := keys, ref := segs }]
    | .list items => collectValues keys items
    | .record fields => collectFields keys fields
    | _ => []

  def collectValues (keys : List String) : Values → List Occ
    | .nil => []
    | .cons head tail => collectValue keys head ++ collectValues keys tail

  def collectFields (keys : List String) : Fields → List Occ
    | .nil => []
    | .cons k v rest => collectValue (keys ++ [k]) v ++ collectFields keys rest
end

/-- Collect all reference occurrences in `doc` recursively. -/
def occurrences (doc : Value) : List Occ :=
  collectValue [] doc

def listLt : List String → List String → Bool
  | [], [] => false
  | [], _ :: _ => true
  | _ :: _, [] => false
  | a :: as, b :: bs =>
    if a == b then listLt as bs else decide (a < b)

def occLt (a b : Occ) : Bool :=
  if a.path == b.path then listLt a.ref b.ref
  else decide (a.path < b.path)

def minOccAux (best : Occ) : List Occ → Occ
  | [] => best
  | o :: rest => minOccAux (if occLt o best then o else best) rest

/-- Minimum occurrence under `(path, ref)` lexicographic order, or `none` if empty. -/
def minOcc : List Occ → Option Occ
  | [] => none
  | o :: rest => some (minOccAux o rest)

def inSpec (o : Occ) : Bool :=
  match o.keys with
  | "spec" :: _ :: _ => true
  | _ => false

def dedup {α : Type} [DecidableEq α] : List α → List α
  | [] => []
  | x :: xs =>
    let rest := dedup xs
    if rest.contains x then rest else x :: rest

def minus {α : Type} [DecidableEq α] (xs ys : List α) : List α :=
  xs.filter (fun x => !(ys.contains x))

/-- Deduplicated target objects referenced by valid refs in `spec` of `doc`. -/
def targets (doc : Value) : List Obj :=
  dedup (((occurrences doc).filter inSpec).filterMap (fun o => (parse o.ref).map Parsed.obj))

/-- A single reference schema rule matching `source` qualified kind and `path` pattern. -/
structure Rule where
  source : String
  path : List String
  targets : List String
  deriving Repr, DecidableEq

abbrev Schema := List Rule

def splitSlashAux (cur : String) (acc : List String) : List Char → List String
  | [] => (cur :: acc).reverse
  | c :: cs =>
    if c == '/' then splitSlashAux "" (cur :: acc) cs
    else splitSlashAux (cur.push c) acc cs

def splitSlash (s : String) : List String :=
  splitSlashAux "" [] s.toList

def pathMatches : List String → List String → Bool
  | [], [] => true
  | pat :: ps, seg :: ss => (pat == "*" || pat == seg) && pathMatches ps ss
  | _, _ => false

/-- Union of allowed target qualified kinds for `source` at occurrence `path`. -/
def allowed (schema : Schema) (source : String) (path : String) : List String :=
  let segs := splitSlash path
  dedup ((schema.filter (fun r => r.source == source && pathMatches r.path segs)).flatMap (fun r => r.targets))

/-- Inputs to the pure reference check. -/
structure Input where
  source : Obj
  schema : Schema
  stored : Option Value
  next : Value
  allow : Occ → Bool
  present : Obj → Bool

/-- Occurrences of the stored document, or `[]` on create. -/
def storedOccs (i : Input) : List Occ :=
  match i.stored with
  | none => []
  | some s => occurrences s

/-- Occurrences in `spec` of `i.next` that do not appear in `i.stored`. -/
def added (i : Input) : List Occ :=
  let prev := storedOccs i
  (occurrences i.next).filter (fun o => inSpec o && !(prev.contains o))

/-- Targets of the stored document, or `[]` on create. -/
def storedTargets (i : Input) : List Obj :=
  match i.stored with
  | none => []
  | some s => targets s

inductive Error where
  | internal
  | malformed
  | kind
  | denied
  | notFound
  deriving Repr, DecidableEq

/-- Back-reference index updates and optimistic target checks produced by a valid write or delete. -/
structure Delta where
  put : List Obj
  delete : List Obj
  compare : List Obj
  deriving Repr, DecidableEq

def phase1 (i : Input) : Option Occ :=
  minOcc ((storedOccs i).filter (fun o => !(inSpec o) || !(validKeys o.keys) || (parse o.ref).isNone))

def phase2 (i : Input) : Option Occ :=
  minOcc ((occurrences i.next).filter (fun o => !(inSpec o) || !(validKeys o.keys)))

def phase3 (i : Input) : Option Occ :=
  minOcc ((added i).filter (fun o => (parse o.ref).isNone))

def phase5 (i : Input) : Option Occ :=
  minOcc ((added i).filter (fun o =>
    match parse o.ref with
    | none => false
    | some t => !(allowed i.schema i.source.qkind o.path).contains t.qkind))

def phase6 (i : Input) : Option Occ :=
  minOcc ((added i).filter (fun o => !(i.allow o)))

def phase7 (i : Input) : Option Occ :=
  minOcc ((added i).filter (fun o =>
    match parse o.ref with
    | none => false
    | some t => !(i.present t.obj)))

/-- Check references on a write (`stored = none` on create) and compute the index delta. -/
def check (i : Input) : Except (Error × Option Occ) Delta :=
  match phase1 i with
  | some occ => .error (.internal, some occ)
  | none =>
    let old := storedTargets i
    if old.length > maxTargets then
      .error (.internal, none)
    else
      match phase2 i with
      | some occ => .error (.malformed, some occ)
      | none =>
        match phase3 i with
        | some occ => .error (.malformed, some occ)
        | none =>
          let nxt := targets i.next
          if nxt.length > maxTargets then
            .error (.malformed, none)
          else
            match phase5 i with
            | some occ => .error (.kind, some occ)
            | none =>
              match phase6 i with
              | some occ => .error (.denied, some occ)
              | none =>
                match phase7 i with
                | some occ => .error (.notFound, some occ)
                | none =>
                  let cmp := dedup ((added i).filterMap (fun o => (parse o.ref).map Parsed.obj))
                  .ok { put := minus nxt old, delete := minus old nxt, compare := cmp }

/-- Compute the back-reference index deletions for deleting `stored`. -/
def deleteDelta (stored : Value) : Except (Error × Option Occ) Delta :=
  let bad := (occurrences stored).filter (fun o => !(inSpec o) || !(validKeys o.keys) || (parse o.ref).isNone)
  match minOcc bad with
  | some occ => .error (.internal, some occ)
  | none =>
    let old := targets stored
    if old.length > maxTargets then
      .error (.internal, none)
    else
      .ok { put := [], delete := old, compare := [] }

end Nayutar.Refs
