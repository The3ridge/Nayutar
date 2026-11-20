import Nayutar.Keyspace

namespace Nayutar.Envelope

mutual
/-- The nine LKAL value types. A record is an ordered list of fields; equality ignores the order. -/
inductive Value where
  | str (s : String)
  | int (i : Int)
  | dec (s : String)
  | bool (b : Bool)
  | enum (name : String)
  | qty (number : String) (unit : String)
  | ref (segs : List String)
  | list (items : Values)
  | record (fields : Fields)
inductive Values where
  | nil
  | cons (head : Value) (tail : Values)
inductive Fields where
  | nil
  | cons (key : String) (value : Value) (tail : Fields)
end

inductive Error where
  | internal
  | malformed
  | conflict
  | forbidden
  | observed
  deriving DecidableEq, Repr

/-- The value of the first field named `key`. -/
def Fields.get : Fields → String → Option Value
  | .nil, _ => none
  | .cons k v rest, key => if k = key then some v else rest.get key

def Fields.length : Fields → Nat
  | .nil => 0
  | .cons _ _ rest => rest.length + 1

/-- Follow `path` through nested records. -/
def Value.at : Value → List String → Option Value
  | v, [] => some v
  | .record fs, k :: rest =>
    match fs.get k with
    | some v => v.at rest
    | none => none
  | _, _ :: _ => none

mutual
/-- LKAL equality: records compare as maps, lists in order. -/
def Value.eqv : Value → Value → Bool
  | .str a, .str b => a == b
  | .int a, .int b => a == b
  | .dec a, .dec b => a == b
  | .bool a, .bool b => a == b
  | .enum a, .enum b => a == b
  | .qty n u, .qty m w => n == m && u == w
  | .ref a, .ref b => a == b
  | .list as, .list bs => Values.eqv as bs
  | .record as, .record bs => as.length == bs.length && Fields.within as bs
  | _, _ => false
def Values.eqv : Values → Values → Bool
  | .nil, .nil => true
  | .cons a as, .cons b bs => Value.eqv a b && Values.eqv as bs
  | _, _ => false
/-- Every field of the first list has an equal field of the same name in the second. -/
def Fields.within : Fields → Fields → Bool
  | .nil, _ => true
  | .cons k v rest, bs =>
    (match bs.get k with
      | some w => Value.eqv v w
      | none => false) && Fields.within rest bs
end

def optEqv : Option Value → Option Value → Bool
  | none, none => true
  | some a, some b => a.eqv b
  | _, _ => false

def Fields.keysDistinct : Fields → Bool
  | .nil => true
  | .cons k _ rest => (rest.get k).isNone && rest.keysDistinct

mutual
/-- No record at any depth names a key twice. -/
def Value.unique : Value → Bool
  | .list vs => Values.unique vs
  | .record fs => fs.keysDistinct && Fields.unique fs
  | _ => true
def Values.unique : Values → Bool
  | .nil => true
  | .cons v vs => Value.unique v && Values.unique vs
def Fields.unique : Fields → Bool
  | .nil => true
  | .cons _ v rest => Value.unique v && Fields.unique rest
end

def Value.isRecord : Value → Bool
  | .record _ => true
  | _ => false

private def isDigit (c : Char) : Bool :=
  decide (48 ≤ c.toNat ∧ c.toNat ≤ 57)

private def isUpper (c : Char) : Bool :=
  decide (65 ≤ c.toNat ∧ c.toNat ≤ 90)

private def isLower (c : Char) : Bool :=
  decide (97 ≤ c.toNat ∧ c.toNat ≤ 122)

private def isHexLower (c : Char) : Bool :=
  isDigit c || decide (97 ≤ c.toNat ∧ c.toNat ≤ 102)

private def isUIDChars : Nat → List Char → Bool
  | _, [] => true
  | i, c :: cs =>
    (if i == 8 || i == 13 || i == 18 || i == 23 then c == '-' else isHexLower c)
      && isUIDChars (i + 1) cs

/-- A UUID in its canonical lowercase text form. -/
def isUID (s : String) : Bool :=
  s.toList.length == 36 && isUIDChars 0 s.toList

def isConditionType (s : String) : Bool :=
  match s.toList with
  | [] => false
  | c :: cs =>
    isUpper c && decide (cs.length ≤ 62) && cs.all (fun x => isUpper x || isLower x || isDigit x)

private def isReasonChar (c : Char) : Bool :=
  isUpper c || isLower c || isDigit c || c == '_' || c == ',' || c == ':'

private def reasonLastOk : List Char → Bool
  | [] => false
  | [c] => isUpper c || isLower c || isDigit c || c == '_'
  | _ :: cs => reasonLastOk cs

def isReason (s : String) : Bool :=
  match s.toList with
  | [] => false
  | c :: cs =>
    (isUpper c || isLower c) && decide (cs.length ≤ 1023) && (c :: cs).all isReasonChar
      && reasonLastOk (c :: cs)

private def twoDigits (a b : Char) : Option Nat :=
  if isDigit a && isDigit b then some ((a.toNat - 48) * 10 + (b.toNat - 48)) else none

private def inRange (v : Option Nat) (lo hi : Nat) : Bool :=
  match v with
  | some n => decide (lo ≤ n ∧ n ≤ hi)
  | none => false

/-- `YYYY-MM-DDTHH:MM:SSZ` with each field in its range. Days are not checked against the month. -/
def isTimestamp (s : String) : Bool :=
  match s.toList with
  | [y1, y2, y3, y4, d1, m1, m2, d2, a1, a2, t, h1, h2, c1, n1, n2, c2, s1, s2, z] =>
    isDigit y1 && isDigit y2 && isDigit y3 && isDigit y4 && d1 == '-' && d2 == '-' && t == 'T'
      && c1 == ':' && c2 == ':' && z == 'Z'
      && inRange (twoDigits m1 m2) 1 12 && inRange (twoDigits a1 a2) 1 31
      && inRange (twoDigits h1 h2) 0 23 && inRange (twoDigits n1 n2) 0 59
      && inRange (twoDigits s1 s2) 0 59
  | _ => false

def topOk : Fields → Bool
  | .nil => true
  | .cons k v rest => (k == "meta" || k == "spec" || k == "status") && v.isRecord && topOk rest

def metaKeysOk : Fields → Bool
  | .nil => true
  | .cons k _ rest =>
    (k == "group" || k == "kind" || k == "name" || k == "labels" || k == "uid" || k == "revision"
      || k == "generation") && metaKeysOk rest

def allStrings : Fields → Bool
  | .nil => true
  | .cons _ (.str _) rest => allStrings rest
  | .cons _ _ _ => false

def labelsOk : Option Value → Bool
  | none => true
  | some (.record ls) => allStrings ls
  | some _ => false

def optString (v : Option Value) (p : String → Bool) : Bool :=
  match v with
  | none => true
  | some (.str s) => p s
  | some _ => false

def optPositive : Option Value → Bool
  | none => true
  | some (.int i) => decide (1 ≤ i)
  | some _ => false

/-- The object key of the store accepts this group, kind and name. An absent group is `core`. -/
def keyOk (group : Option String) (kind name : String) : Bool :=
  (Nayutar.Keyspace.rel
    { family := .object
      tenant := "t"
      object := { group := group.getD "core", kind := kind, name := name }
      source := { group := "", kind := "", name := "" }
      identityHash := ""
      version := ""
      node := "" }).isSome

def identityOk (m : Fields) : Bool :=
  match m.get "kind", m.get "name" with
  | some (.str kind), some (.str name) =>
    (match m.get "group" with
      | none => keyOk none kind name
      | some (.str g) => g != "core" && keyOk (some g) kind name
      | some _ => false)
  | _, _ => false

def metaOk (m : Fields) : Bool :=
  metaKeysOk m && identityOk m && labelsOk (m.get "labels") && optString (m.get "uid") isUID
    && optPositive (m.get "revision") && optPositive (m.get "generation")

def condKeysOk : Fields → Bool
  | .nil => true
  | .cons k _ rest => (k == "state" || k == "reason" || k == "since") && condKeysOk rest

def stateOk : Option Value → Bool
  | some (.enum n) => n == "true" || n == "false" || n == "unknown"
  | _ => false

def reasonOk : Option Value → Bool
  | some (.str r) => isReason r
  | _ => false

def condOk : Value → Bool
  | .record c =>
    condKeysOk c && stateOk (c.get "state") && reasonOk (c.get "reason")
      && optString (c.get "since") isTimestamp
  | _ => false

def condsOk : Fields → Bool
  | .nil => true
  | .cons t c rest => isConditionType t && condOk c && condsOk rest

def statusOk (st : Fields) : Bool :=
  optPositive (st.get "observedGeneration") &&
    (match st.get "condition" with
      | none => true
      | some (.record cs) => condsOk cs
      | some _ => false)

/-- The envelope shape that every candidate and every stored document satisfies. -/
def shape (doc : Value) : Bool :=
  doc.unique &&
    (match doc with
      | .record top =>
        topOk top &&
          (match top.get "meta" with
            | some (.record m) => metaOk m
            | _ => false) &&
          (match top.get "status" with
            | some (.record st) => statusOk st
            | _ => true)
      | _ => false)

def condsHaveSince : Fields → Bool
  | .nil => true
  | .cons _ c rest => (c.at ["since"]).isSome && condsHaveSince rest

def observedWithin : Option Value → Option Value → Bool
  | some (.int n), some (.int g) => decide (n ≤ g)
  | _, _ => true

/-- What the store holds: the shape, the server-assigned fields, and no revision. -/
def stored (doc : Value) : Bool :=
  shape doc && (doc.at ["meta", "uid"]).isSome && (doc.at ["meta", "generation"]).isSome
    && (doc.at ["meta", "revision"]).isNone
    && (match doc.at ["status", "condition"] with
      | some (.record cs) => condsHaveSince cs
      | _ => true)
    && observedWithin (doc.at ["status", "observedGeneration"]) (doc.at ["meta", "generation"])

def optField (k : String) (v : Option Value) (rest : Fields) : Fields :=
  match v with
  | some x => .cons k x rest
  | none => rest

/-- The meta record of a result: the identity and labels of `m`, then the server-assigned fields. -/
def canonMeta (m : Fields) (uid gen : Value) : Fields :=
  optField "group" (m.get "group")
    (optField "kind" (m.get "kind")
      (optField "name" (m.get "name")
        (optField "labels" (m.get "labels") (.cons "uid" uid (.cons "generation" gen .nil)))))

def mkDoc (m : Fields) (spec status : Option Value) : Value :=
  .record (.cons "meta" (.record m) (optField "spec" spec (optField "status" status .nil)))

def sameIdentity (a b : Fields) : Bool :=
  optEqv (a.get "group") (b.get "group") && optEqv (a.get "kind") (b.get "kind")
    && optEqv (a.get "name") (b.get "name")

/-- The candidate names a value and it is not the current one. An absent value names nothing. -/
def differs (cand current : Option Value) : Bool :=
  match cand with
  | none => false
  | some x => !(optEqv (some x) current)

def staleRevision (m : Fields) (rev : Int) : Bool :=
  match m.get "revision" with
  | some (.int r) => r != rev
  | _ => false

/-- Admit a new document: the candidate must not name any server-assigned field or a status. -/
def create (cand : Value) (uid : String) : Except Error Value :=
  if !isUID uid then .error .internal
  else if !shape cand then .error .malformed
  else
    match cand with
    | .record top =>
      match top.get "meta" with
      | some (.record m) =>
        if (m.get "uid").isSome || (m.get "revision").isSome || (m.get "generation").isSome
            || (top.get "status").isSome then
          .error .forbidden
        else .ok (mkDoc (canonMeta m (.str uid) (.int 1)) (top.get "spec") none)
      | _ => .error .malformed
    | _ => .error .malformed

/-- The entrance that writes meta and spec. The status is carried over from the stored document. -/
def update (old : Value) (rev : Int) (cand : Value) : Except Error Value :=
  if !stored old || decide (rev < 1) then .error .internal
  else if !shape cand then .error .malformed
  else
    match old, cand with
    | .record ot, .record ct =>
      match ot.get "meta", ct.get "meta" with
      | some (.record om), some (.record cm) =>
        if !sameIdentity om cm then .error .forbidden
        else if staleRevision cm rev then .error .conflict
        else if differs (cm.get "uid") (om.get "uid")
            || differs (cm.get "generation") (om.get "generation")
            || differs (ct.get "status") (ot.get "status") then
          .error .forbidden
        else
          match om.get "uid", om.get "generation" with
          | some u, some (.int g) =>
            .ok (mkDoc
              (canonMeta cm u (.int (if optEqv (ot.get "spec") (ct.get "spec") then g else g + 1)))
              (ct.get "spec") (ot.get "status"))
          | _, _ => .error .internal
      | _, _ => .error .internal
    | _, _ => .error .internal

def condsOf (doc : Value) : Option Fields :=
  match doc.at ["status", "condition"] with
  | some (.record cs) => some cs
  | _ => none

def oldCond (oldConds : Option Fields) (t : String) : Option Value :=
  match oldConds with
  | some cs => cs.get t
  | none => none

/-- Some candidate condition names a `since` that is not the stored one. -/
def sinceForbidden (oldConds : Option Fields) : Fields → Bool
  | .nil => false
  | .cons t c rest =>
    (match c.at ["since"] with
      | none => false
      | some s =>
        match oldCond oldConds t with
        | some oc => !(optEqv (oc.at ["since"]) (some s))
        | none => true) || sinceForbidden oldConds rest

/-- The `since` of a written condition: the stored one while the state is unchanged, else `now`. -/
def sinceFor (oldConds : Option Fields) (now : String) (t : String) (c : Value) : Value :=
  match oldCond oldConds t with
  | some oc =>
    if optEqv (oc.at ["state"]) (c.at ["state"]) then (oc.at ["since"]).getD (.str now)
    else .str now
  | none => .str now

def stampCond (oldConds : Option Fields) (now : String) (t : String) (c : Value) : Value :=
  .record
    (optField "state" (c.at ["state"])
      (optField "reason" (c.at ["reason"]) (.cons "since" (sinceFor oldConds now t c) .nil)))

def stampConds (oldConds : Option Fields) (now : String) : Fields → Fields
  | .nil => .nil
  | .cons t c rest => .cons t (stampCond oldConds now t c) (stampConds oldConds now rest)

def stampStatus (oldConds : Option Fields) (now : String) : Fields → Fields
  | .nil => .nil
  | .cons k v rest =>
    if k = "condition" then
      match v with
      | .record cs => .cons k (.record (stampConds oldConds now cs)) rest
      | _ => .cons k v rest
    else .cons k v (stampStatus oldConds now rest)

def stampStatusValue (oldConds : Option Fields) (now : String) : Option Value → Option Value
  | some (.record st) => some (.record (stampStatus oldConds now st))
  | other => other

/-- The written observedGeneration passes the generation, falls behind the stored one, or vanishes. -/
def observedBad (oldObs newObs : Option Value) (gen : Int) : Bool :=
  match newObs with
  | some (.int n) =>
    decide (gen < n) ||
      (match oldObs with
        | some (.int m) => decide (n < m)
        | _ => false)
  | _ => oldObs.isSome

/-- The entrance that writes the status. Meta and spec are carried over from the stored document. -/
def updateStatus (old : Value) (rev : Int) (cand : Value) (now : String) : Except Error Value :=
  if !stored old || decide (rev < 1) || !isTimestamp now then .error .internal
  else if !shape cand then .error .malformed
  else
    match old, cand with
    | .record ot, .record ct =>
      match ot.get "meta", ct.get "meta" with
      | some (.record om), some (.record cm) =>
        if !sameIdentity om cm then .error .forbidden
        else if staleRevision cm rev then .error .conflict
        else if differs (cm.get "uid") (om.get "uid")
            || differs (cm.get "generation") (om.get "generation")
            || differs (cm.get "labels") (om.get "labels")
            || differs (ct.get "spec") (ot.get "spec")
            || sinceForbidden (condsOf old) ((condsOf cand).getD .nil) then
          .error .forbidden
        else
          match om.get "generation" with
          | some (.int g) =>
            if observedBad (old.at ["status", "observedGeneration"])
                (cand.at ["status", "observedGeneration"]) g then
              .error .observed
            else
              .ok (mkDoc om (ot.get "spec") (stampStatusValue (condsOf old) now (ct.get "status")))
          | _ => .error .internal
      | _, _ => .error .internal
    | _, _ => .error .internal

end Nayutar.Envelope
