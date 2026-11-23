import Nayutar.Envelope
import Nayutar.OrderedSet

set_option autoImplicit false

namespace Nayutar.Patch

open Nayutar.Envelope (Value Values Fields optEqv)

def patchField : String := "patch"

inductive OpKind where
  | test
  | set
  | del
  deriving DecidableEq, Repr

structure Op where
  kind : OpKind
  path : String
  value : Option Value

inductive Entrance where
  | spec
  | status
  deriving DecidableEq, Repr

inductive Error where
  | internal
  | malformed
  | conflict
  | notFound
  | forbidden
  | observed
  deriving DecidableEq, Repr

def validSeg (s : String) : Bool :=
  Nayutar.OrderedSet.validKey s

def splitSlashAux (cur : String) (acc : List String) : List Char → List String
  | [] => (cur :: acc).reverse
  | c :: cs =>
    if c == '/' then splitSlashAux "" (cur :: acc) cs
    else splitSlashAux (cur.push c) acc cs

def splitSlash (s : String) : List String :=
  splitSlashAux "" [] s.toList

def joinSlash (segs : List String) : String :=
  String.intercalate "/" segs

def parsePath (path : String) : Option (List String) :=
  if path == "" then none
  else
    let segs := splitSlash path
    if segs.all validSeg then some segs else none

def opOk (op : Op) : Bool :=
  (parsePath op.path).isSome &&
    match op.kind, op.value with
    | .del, none => true
    | .test, some v => v.unique
    | .set, some v => v.unique
    | _, _ => false

def opsOk (ops : List Op) : Bool :=
  ops.all opOk

def replaceField : Fields → String → Value → Fields
  | .nil, _, _ => .nil
  | .cons k v rest, key, val =>
    if k = key then .cons k val rest
    else .cons k v (replaceField rest key val)

def appendField : Fields → String → Value → Fields
  | .nil, key, val => .cons key val .nil
  | .cons k v rest, key, val => .cons k v (appendField rest key val)

def removeField : Fields → String → Fields
  | .nil, _ => .nil
  | .cons k v rest, key =>
    if k = key then rest
    else .cons k v (removeField rest key)

def setAt : Fields → List String → Value → Except Error Fields
  | _, [], _ => .error .malformed
  | fs, [k], v =>
    if (fs.get k).isSome then .ok (replaceField fs k v)
    else .ok (appendField fs k v)
  | fs, k :: k2 :: rest, v =>
    match fs.get k with
    | none => (setAt .nil (k2 :: rest) v).map (fun sub => appendField fs k (.record sub))
    | some (.record sub) =>
      (setAt sub (k2 :: rest) v).map (fun sub' => replaceField fs k (.record sub'))
    | some _ => .error .malformed

def deleteAt : Fields → List String → Except Error Fields
  | _, [] => .error .malformed
  | fs, [k] =>
    if (fs.get k).isSome then .ok (removeField fs k)
    else .error .notFound
  | fs, k :: k2 :: rest =>
    match fs.get k with
    | none => .error .notFound
    | some (.record sub) =>
      (deleteAt sub (k2 :: rest)).map (fun sub' => replaceField fs k (.record sub'))
    | some _ => .error .malformed

def stepOp (cur : Fields) (op : Op) : Except Error Fields :=
  match parsePath op.path with
  | none => .error .malformed
  | some segs =>
    match op.kind, op.value with
    | .test, some expected =>
      match (Value.record cur).at segs with
      | some actual => if Value.eqv actual expected then .ok cur else .error .conflict
      | none => .error .conflict
    | .set, some v => setAt cur segs v
    | .del, none => deleteAt cur segs
    | _, _ => .error .malformed

def runOps : Fields → List Op → Except Error Fields
  | cur, [] => .ok cur
  | cur, op :: rest =>
    match stepOp cur op with
    | .error err => .error err
    | .ok next => runOps next rest

def apply (doc : Fields) (ops : List Op) : Except Error Fields :=
  if !(Value.record doc).unique || !opsOk ops then .error .malformed
  else runOps doc ops

def topSegsOk : Fields → Bool
  | .nil => true
  | .cons k _ rest => validSeg k && topSegsOk rest

def fieldsKeys : Fields → List String
  | .nil => []
  | .cons k _ rest => k :: fieldsKeys rest

def insertKey (k : String) : List String → List String
  | [] => [k]
  | x :: xs =>
    if k < x then k :: x :: xs
    else x :: insertKey k xs

def sortKeys : List String → List String
  | [] => []
  | k :: ks => insertKey k (sortKeys ks)

def sortedKeys (fs : Fields) : List String :=
  sortKeys (fieldsKeys fs)

mutual
  def valueSize : Value → Nat
    | .list vs => valuesSize vs + 1
    | .record fs => fieldsSize fs + 1
    | _ => 1

  def valuesSize : Values → Nat
    | .nil => 1
    | .cons v vs => valueSize v + valuesSize vs + 1

  def fieldsSize : Fields → Nat
    | .nil => 1
    | .cons _ v rest => valueSize v + fieldsSize rest + 1
end

private theorem fieldsSize_get_lt : ∀ (fs : Fields) (k : String) (v : Value),
    fs.get k = some v → valueSize v < fieldsSize fs
  | .nil, _, _, h => by simp [Fields.get] at h
  | .cons k' v' rest, k, v, h => by
    simp only [Fields.get] at h
    split at h
    · cases h
      simp [fieldsSize]
      omega
    · have ih := fieldsSize_get_lt rest k v h
      simp [fieldsSize]
      omega

private theorem fieldsSize_get_record_lt (fs : Fields) (k : String) (sub : Fields)
    (h : fs.get k = some (.record sub)) : fieldsSize sub < fieldsSize fs := by
  have h1 := fieldsSize_get_lt fs k (.record sub) h
  simp only [valueSize] at h1
  omega

def diffDels (pfx : List String) (next : Fields) : List String → List Op
  | [] => []
  | k :: ks =>
    if (next.get k).isNone then
      { kind := .del, path := joinSlash (pfx ++ [k]), value := none } :: diffDels pfx next ks
    else
      diffDels pfx next ks

mutual
  def diffFields (pfx : List String) (old next : Fields) : List Op :=
    diffDels pfx next (sortedKeys old) ++ diffSets pfx old next (sortedKeys next)
  termination_by (fieldsSize next, 1, 0)

  def diffSets (pfx : List String) (old next : Fields) : List String → List Op
    | [] => []
    | k :: ks =>
      let headOps :=
        match h2 : next.get k with
        | none => []
        | some nv =>
          match old.get k with
          | none => [{ kind := .set, path := joinSlash (pfx ++ [k]), value := some nv }]
          | some ov =>
            if Value.eqv ov nv then []
            else
              match ov, nv with
              | .record osub, .record nsub =>
                if (decide (osub.length > 0) || decide (nsub.length > 0))
                    && topSegsOk osub && topSegsOk nsub then
                  have : fieldsSize nsub < fieldsSize next :=
                    fieldsSize_get_record_lt next k nsub h2
                  diffFields (pfx ++ [k]) osub nsub
                else
                  [{ kind := .set, path := joinSlash (pfx ++ [k]), value := some nv }]
              | _, _ =>
                [{ kind := .set, path := joinSlash (pfx ++ [k]), value := some nv }]
      headOps ++ diffSets pfx old next ks
  termination_by ks => (fieldsSize next, 0, ks.length)
end

def diff (old next : Fields) : Except Error (List Op) :=
  if !(Value.record old).unique || !(Value.record next).unique
      || !topSegsOk old || !topSegsOk next then
    .error .malformed
  else
    .ok (diffFields [] old next)

def opKindName : OpKind → String
  | .test => "test"
  | .set => "set"
  | .del => "del"

def parseOpKind : String → Option OpKind
  | "test" => some .test
  | "set" => some .set
  | "del" => some .del
  | _ => none

def opToFields (op : Op) : Fields :=
  match op.value with
  | none => .cons "op" (.enum (opKindName op.kind)) (.cons "path" (.str op.path) .nil)
  | some v =>
    .cons "op" (.enum (opKindName op.kind))
      (.cons "path" (.str op.path) (.cons "value" v .nil))

def opsToItems (idx : Nat) : List Op → List Nayutar.OrderedSet.Item
  | [] => []
  | op :: rest =>
    { key := toString idx, value := opToFields op } :: opsToItems (idx + 1) rest

def toRecord (ops : List Op) : Except Error Fields :=
  if !opsOk ops then .error .malformed
  else
    match Nayutar.OrderedSet.fromItems (opsToItems 0 ops) with
    | .ok set => .ok (.cons patchField (.record set) .nil)
    | .error _ => .error .malformed

def decodeEntry (e : Nayutar.OrderedSet.Entry) : Option Op :=
  match e.value.get "op", e.value.get "path" with
  | some (.enum kname), some (.str p) =>
    match parseOpKind kname, parsePath p with
    | some kind, some _ =>
      match kind, e.value.get "value" with
      | .del, none =>
        if e.value.length == 2 then some { kind := .del, path := p, value := none }
        else none
      | .test, some v =>
        if e.value.length == 3 then some { kind := .test, path := p, value := some v }
        else none
      | .set, some v =>
        if e.value.length == 3 then some { kind := .set, path := p, value := some v }
        else none
      | _, _ => none
    | _, _ => none
  | _, _ => none

def decodeEntries : List Nayutar.OrderedSet.Entry → Option (List Op)
  | [] => some []
  | e :: rest =>
    match decodeEntry e, decodeEntries rest with
    | some op, some tail => some (op :: tail)
    | _, _ => none

def fromRecord (doc : Fields) : Except Error (List Op) :=
  if !(Value.record doc).unique || doc.length != 1 then .error .malformed
  else
    match doc.get patchField with
    | some (.record set) =>
      match Nayutar.OrderedSet.entries set with
      | .error _ => .error .malformed
      | .ok es =>
        match decodeEntries es with
        | some ops => .ok ops
        | none => .error .malformed
    | _ => .error .malformed

def withRevisionMeta (m : Fields) (rev : Int) : Fields :=
  appendField (Nayutar.OrderedSet.stripField m "revision") "revision" (.int rev)

def withRevision : Fields → Int → Fields
  | .nil, _ => .nil
  | .cons k v rest, rev =>
    if k = "meta" then
      match v with
      | .record m => .cons k (.record (withRevisionMeta m rev)) rest
      | _ => .cons k v (withRevision rest rev)
    else .cons k v (withRevision rest rev)

def entranceAllows (entrance : Entrance) : List String → Except Error Unit
  | [] => .error .malformed
  | root :: rest =>
    if root != "meta" && root != "spec" && root != "status" then
      .error .malformed
    else
      match entrance with
      | .spec =>
        let isMetaLabels := root == "meta" &&
          match rest with
          | "labels" :: _ => true
          | _ => false
        if isMetaLabels || root == "spec" then .ok () else .error .forbidden
      | .status =>
        let isCondSince :=
          match rest with
          | "condition" :: _ :: "since" :: _ => true
          | _ => false
        if root == "status" && !isCondSince then .ok () else .error .forbidden

def stepEntranceOp (entrance : Entrance) (cur : Fields) (op : Op) : Except Error Fields :=
  match parsePath op.path with
  | none => .error .malformed
  | some segs =>
    match op.kind, op.value with
    | .test, some expected =>
      match (Value.record cur).at segs with
      | some actual => if Value.eqv actual expected then .ok cur else .error .conflict
      | none => .error .conflict
    | .set, some v => do
      let _ ← entranceAllows entrance segs
      setAt cur segs v
    | .del, none => do
      let _ ← entranceAllows entrance segs
      deleteAt cur segs
    | _, _ => .error .malformed

def runEntranceOps (entrance : Entrance) : Fields → List Op → Except Error Fields
  | cur, [] => .ok cur
  | cur, op :: rest =>
    match stepEntranceOp entrance cur op with
    | .error err => .error err
    | .ok next => runEntranceOps entrance next rest

def applyEntrance (entrance : Entrance) (old : Fields) (rev : Int) (ops : List Op) :
    Except Error Fields :=
  if !Nayutar.Envelope.stored (.record old) || !Nayutar.OrderedSet.inInt64 rev
      || !decide (1 ≤ rev) then
    .error .internal
  else if !opsOk ops then
    .error .malformed
  else
    runEntranceOps entrance (withRevision old rev) ops

def mapEnvErr : Nayutar.Envelope.Error → Error
  | .internal => .internal
  | .malformed => .malformed
  | .conflict => .conflict
  | .forbidden => .forbidden
  | .observed => .observed

def patch (old : Fields) (rev : Int) (ops : List Op) : Except Error Value := do
  let cand ← applyEntrance .spec old rev ops
  match Nayutar.Envelope.update (.record old) rev (.record cand) with
  | .ok next => .ok next
  | .error e => .error (mapEnvErr e)

def patchStatus (old : Fields) (rev : Int) (ops : List Op) (now : String) : Except Error Value :=
  if !Nayutar.Envelope.stored (.record old) || !Nayutar.OrderedSet.inInt64 rev
      || !decide (1 ≤ rev) || !Nayutar.Envelope.isTimestamp now then
    .error .internal
  else do
    let cand ← applyEntrance .status old rev ops
    match Nayutar.Envelope.updateStatus (.record old) rev (.record cand) now with
    | .ok next => .ok next
    | .error e => .error (mapEnvErr e)

end Nayutar.Patch
