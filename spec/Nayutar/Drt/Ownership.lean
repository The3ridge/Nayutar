import Lean.Data.Json
import Nayutar.Ownership

set_option autoImplicit false

open Lean
open Nayutar.Envelope (Value Values Fields)
open Nayutar.Ownership
open Kisage (Origin PermSet Mode)

namespace Nayutar.Drt.Ownership

inductive CaseOp where
  | project (doc : Fields) (rejectNestedEmpty : Bool)
  | scope (sc : Scope) (path : Path) (req : PermSet)
  | planApply (doc : Fields) (st : OwnerState) (decl : Fields) (actor : ActorID)
      (sc : Scope) (expectMatch ownerRevMatch : Bool)
  | planPrune (doc : Fields) (st : OwnerState) (decl : Fields) (actor : ActorID)
      (sc : Scope) (expectMatch ownerRevMatch : Bool)
  | adopt (doc : Fields) (st : OwnerState) (actor : ActorID) (sc : Scope)
      (path : Path) (expectMatch ownerRevMatch : Bool)
  | release (doc : Fields) (st : OwnerState) (actor : ActorID) (sc : Scope)
      (path : Path) (expectMatch ownerRevMatch : Bool)
  | checkUpdate (oldDoc : Fields) (st : OwnerState) (nextDoc : Fields)
      (actor : ActorID) (sc : Scope)
  | trie (doc : Fields) (st : OwnerState)

structure Case where
  id : Nat
  op : CaseOp

private def str (j : Json) (field : String) : Except String String := do
  (← j.getObjVal? field).getStr?

private def nat (j : Json) (field : String) : Except String Nat := do
  (← j.getObjVal? field).getNat?

private def boolField (j : Json) (field : String) : Except String Bool := do
  (← j.getObjVal? field).getBool?

private def parseDigits (cs : List Char) (acc : Nat) : Except String Nat :=
  match cs with
  | [] => pure acc
  | c :: rest =>
    if '0' <= c && c <= '9' then
      parseDigits rest (acc * 10 + (c.toNat - '0'.toNat))
    else
      throw s!"invalid integer digit: {c}"

private def parseIntStr (s : String) : Except String Int :=
  match s.toList with
  | [] => throw "empty integer"
  | ['-'] => throw "empty integer"
  | '-' :: rest => do
    let n ← parseDigits rest 0
    return -(Int.ofNat n)
  | cs => do
    let n ← parseDigits cs 0
    return Int.ofNat n

private def listToValues : List Value → Values
  | [] => .nil
  | v :: vs => .cons v (listToValues vs)

private def listToFields : List (String × Value) → Fields
  | [] => .nil
  | (k, v) :: rest => .cons k v (listToFields rest)

private partial def parseValue (j : Json) : Except String Value :=
  match j.getObjVal? "s" with
  | .ok v => do
    let s ← v.getStr?
    return .str s
  | .error _ =>
  match j.getObjVal? "i" with
  | .ok v => do
    let s ← v.getStr?
    let n ← parseIntStr s
    return .int n
  | .error _ =>
  match j.getObjVal? "d" with
  | .ok v => do
    let s ← v.getStr?
    return .dec s
  | .error _ =>
  match j.getObjVal? "b" with
  | .ok v => do
    let b ← v.getBool?
    return .bool b
  | .error _ =>
  match j.getObjVal? "e" with
  | .ok v => do
    let name ← v.getStr?
    return .enum name
  | .error _ =>
  match j.getObjVal? "q" with
  | .ok v => do
    let arr ← v.getArr?
    match arr.toList with
    | [nJson, uJson] =>
      let n ← nJson.getStr?
      let u ← uJson.getStr?
      return .qty n u
    | _ => throw "qty must be a 2-element array"
  | .error _ =>
  match j.getObjVal? "r" with
  | .ok v => do
    let arr ← v.getArr?
    let segs ← arr.toList.mapM (fun elem => elem.getStr?)
    return .ref segs
  | .error _ =>
  match j.getObjVal? "l" with
  | .ok v => do
    let arr ← v.getArr?
    let items ← arr.toList.mapM parseValue
    return .list (listToValues items)
  | .error _ =>
  match j.getObjVal? "o" with
  | .ok v => do
    let arr ← v.getArr?
    let fields ← arr.toList.mapM fun elem => do
      let pair ← elem.getArr?
      match pair.toList with
      | [kJson, vJson] =>
        let k ← kJson.getStr?
        let val ← parseValue vJson
        return (k, val)
      | _ => throw "record field must be a 2-element array"
    return .record (listToFields fields)
  | .error _ =>
    throw "invalid value: expected object with one of s, i, d, b, e, q, r, l, o"

private def recField (j : Json) (field : String) : Except String Fields := do
  match ← parseValue (← j.getObjVal? field) with
  | .record fs => pure fs
  | _ => throw s!"{field}: expected record value"

private def parsePathJson (j : Json) : Except String Path := do
  let arr ← j.getArr?
  arr.toList.mapM (fun elem => elem.getStr?)

private def pathField (j : Json) (field : String) : Except String Path := do
  parsePathJson (← j.getObjVal? field)

private def parsePermSet (j : Json) : Except String PermSet := do
  let s ← boolField j "set"
  let d ← boolField j "delete"
  let o ← boolField j "own"
  let r ← boolField j "resolve"
  return { set := s, delete := d, own := o, resolve := r }

private def parseMatchMode (s : String) : Except String MatchMode :=
  match s with
  | "root" => pure .root
  | "subtree" => pure .subtree
  | "exact" => pure .exact
  | other => throw s!"unknown match mode: {other}"

private def parseScopeRule (j : Json) : Except String ScopeRule := do
  let m ← parseMatchMode (← str j "match")
  let pfx ← pathField j "prefix"
  let perms ← parsePermSet (← j.getObjVal? "perms")
  return { matchMode := m, prefixPath := pfx, perms := perms }

private def parseScopeField (j : Json) (field : String) : Except String Scope := do
  let arr ← (← j.getObjVal? field).getArr?
  arr.toList.mapM parseScopeRule

private def parseOrigin (s : String) : Except String Origin :=
  match s with
  | "created" => pure .created
  | "modified" => pure .modified
  | other => throw s!"unknown origin: {other}"

private def parseOwnerEntry (j : Json) : Except String (Path × LeafStatus) := do
  let p ← pathField j "path"
  match ← str j "state" with
  | "owned" =>
    let owner ← str j "owner"
    let orig ← parseOrigin (← str j "origin")
    return (p, .owned owner orig)
  | "unowned" => return (p, .unowned)
  | "defaulted" => return (p, .defaulted)
  | other => throw s!"unknown leaf state: {other}"

private def parseOwnerState (j : Json) : Except String OwnerState := do
  let creator ← str j "creator"
  let arr ← (← j.getObjVal? "entries").getArr?
  let entries ← arr.toList.mapM parseOwnerEntry
  return { creator := creator, entries := sortPairs entries }

private def ownerStateField (j : Json) (field : String) : Except String OwnerState := do
  parseOwnerState (← j.getObjVal? field)

def parseCase (line : String) : Except String Case := do
  let j ← Json.parse line
  let id ← nat j "id"
  let op ← match ← str j "op" with
    | "project" => do
      let doc ← recField j "doc"
      let rej ← boolField j "rejectNestedEmpty"
      pure (.project doc rej)
    | "scope" => do
      let sc ← parseScopeField j "scope"
      let path ← pathField j "path"
      let req ← parsePermSet (← j.getObjVal? "req")
      pure (.scope sc path req)
    | "planApply" => do
      let doc ← recField j "doc"
      let st ← ownerStateField j "state"
      let decl ← recField j "decl"
      let actor ← str j "actor"
      let sc ← parseScopeField j "scope"
      let exp ← boolField j "expectMatch"
      let rev ← boolField j "ownerRevMatch"
      pure (.planApply doc st decl actor sc exp rev)
    | "planPrune" => do
      let doc ← recField j "doc"
      let st ← ownerStateField j "state"
      let decl ← recField j "decl"
      let actor ← str j "actor"
      let sc ← parseScopeField j "scope"
      let exp ← boolField j "expectMatch"
      let rev ← boolField j "ownerRevMatch"
      pure (.planPrune doc st decl actor sc exp rev)
    | "adopt" => do
      let doc ← recField j "doc"
      let st ← ownerStateField j "state"
      let actor ← str j "actor"
      let sc ← parseScopeField j "scope"
      let path ← pathField j "path"
      let exp ← boolField j "expectMatch"
      let rev ← boolField j "ownerRevMatch"
      pure (.adopt doc st actor sc path exp rev)
    | "release" => do
      let doc ← recField j "doc"
      let st ← ownerStateField j "state"
      let actor ← str j "actor"
      let sc ← parseScopeField j "scope"
      let path ← pathField j "path"
      let exp ← boolField j "expectMatch"
      let rev ← boolField j "ownerRevMatch"
      pure (.release doc st actor sc path exp rev)
    | "checkUpdate" => do
      let oldDoc ← recField j "oldDoc"
      let st ← ownerStateField j "state"
      let nextDoc ← recField j "nextDoc"
      let actor ← str j "actor"
      let sc ← parseScopeField j "scope"
      pure (.checkUpdate oldDoc st nextDoc actor sc)
    | "trie" => do
      let doc ← recField j "doc"
      let st ← ownerStateField j "state"
      pure (.trie doc st)
    | other => throw s!"unknown op: {other}"
  return { id := id, op := op }

mutual
  private def valueToJson : Value → Json
    | .str s => Json.mkObj [("s", toJson s)]
    | .int i => Json.mkObj [("i", toJson (toString i))]
    | .dec s => Json.mkObj [("d", toJson s)]
    | .bool b => Json.mkObj [("b", toJson b)]
    | .enum name => Json.mkObj [("e", toJson name)]
    | .qty number unit => Json.mkObj [("q", Json.arr #[toJson number, toJson unit])]
    | .ref segs => Json.mkObj [("r", Json.arr (segs.map toJson).toArray)]
    | .list items => Json.mkObj [("l", Json.arr (valuesToList items).toArray)]
    | .record fields => Json.mkObj [("o", Json.arr (fieldsToList fields).toArray)]

  private def valuesToList : Values → List Json
    | .nil => []
    | .cons head tail => valueToJson head :: valuesToList tail

  private def fieldsToList : Fields → List Json
    | .nil => []
    | .cons k v tail => Json.arr #[toJson k, valueToJson v] :: fieldsToList tail
end

private def pathToJson (p : Path) : Json :=
  Json.arr (p.map toJson).toArray

private def permSetToJson (p : PermSet) : Json :=
  Json.mkObj [
    ("set", toJson p.set),
    ("delete", toJson p.delete),
    ("own", toJson p.own),
    ("resolve", toJson p.resolve)
  ]

private def originStr : Origin → String
  | .created => "created"
  | .modified => "modified"

private def ownerEntryToJson (entry : Path × LeafStatus) : Json :=
  let (p, ls) := entry
  match ls with
  | .owned m orig =>
    Json.mkObj [
      ("path", pathToJson p),
      ("state", toJson "owned"),
      ("owner", toJson m),
      ("origin", toJson (originStr orig))
    ]
  | .unowned =>
    Json.mkObj [
      ("path", pathToJson p),
      ("state", toJson "unowned")
    ]
  | .defaulted =>
    Json.mkObj [
      ("path", pathToJson p),
      ("state", toJson "defaulted")
    ]

private def ownerStateToJson (ow : OwnerState) : Json :=
  Json.mkObj [
    ("creator", toJson ow.creator),
    ("entries", Json.arr ((sortPairs ow.entries).map ownerEntryToJson).toArray)
  ]

private def diagToJson (d : Diagnostic) : Json :=
  Json.mkObj [
    ("code", toJson d.code.codeStr),
    ("path", pathToJson d.path)
  ]

private def leafToJson (entry : Path × Value) : Json :=
  Json.mkObj [
    ("path", pathToJson entry.1),
    ("value", valueToJson entry.2)
  ]

private def errToJson (id : Nat) (code : DiagCode) : Json :=
  Json.mkObj [
    ("id", toJson id),
    ("ok", toJson false),
    ("code", toJson code.codeStr)
  ]

private def planResToJson (id : Nat) (res : Except DiagCode PlanOutput) : Json :=
  match res with
  | .error code => errToJson id code
  | .ok out =>
    let primary := match out.primaryCode with
      | none => ""
      | some c => c.codeStr
    Json.mkObj [
      ("id", toJson id),
      ("ok", toJson true),
      ("blocked", toJson out.blocked),
      ("primaryCode", toJson primary),
      ("doc", valueToJson out.nextDoc),
      ("state", ownerStateToJson out.nextOwner),
      ("diags", Json.arr (out.diagnostics.map diagToJson).toArray)
    ]

private def resolveResToJson (id : Nat) (res : ResolveResult) : Json :=
  if !res.accepted then
    errToJson id (res.code.getD .K101)
  else
    Json.mkObj [
      ("id", toJson id),
      ("ok", toJson true),
      ("state", ownerStateToJson res.nextOwner),
      ("diags", Json.arr (res.notices.map diagToJson).toArray)
    ]

def caseToJson (c : Case) : Json :=
  match c.op with
  | .project doc rej =>
    match projectEntranceSpec (.record doc) rej with
    | .error code => errToJson c.id code
    | .ok leaves =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("leaves", Json.arr (leaves.map leafToJson).toArray)
      ]
  | .scope sc path req =>
    if !sc.valid then errToJson c.id .K101
    else
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("perms", permSetToJson (sc.permissionsFor path)),
        ("allows", toJson (sc.allows path req))
      ]
  | .planApply doc st decl actor sc exp rev =>
    planResToJson c.id (planMulti (.record doc) st (.record decl) actor sc .apply exp rev)
  | .planPrune doc st decl actor sc exp rev =>
    planResToJson c.id (planMulti (.record doc) st (.record decl) actor sc .prune exp rev)
  | .adopt doc st actor sc path exp rev =>
    resolveResToJson c.id (planResolve {
      kind := .adopt, actor := actor, scope := sc, path := path,
      doc := .record doc, owner := st, expectMatch := exp, ownerRevMatch := rev
    })
  | .release doc st actor sc path exp rev =>
    resolveResToJson c.id (planResolve {
      kind := .release, actor := actor, scope := sc, path := path,
      doc := .record doc, owner := st, expectMatch := exp, ownerRevMatch := rev
    })
  | .checkUpdate oldDoc st nextDoc actor sc =>
    match checkImperative actor sc (.record oldDoc) st (.record nextDoc) with
    | .error code => errToJson c.id code
    | .ok nextSt =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("state", ownerStateToJson nextSt)
      ]
  | .trie doc st =>
    if !st.valid then errToJson c.id .K101
    else
      match projectEntranceSpec (.record doc) false with
      | .error code => errToJson c.id code
      | .ok docLeaves =>
        let fullLeaves := docLeaves.map fun (p, _) =>
          (p, (st.get p).getD (.owned st.creator .created))
        let co := encodeTrie st.creator fullLeaves
        match decodeTrie co (docLeaves.map Prod.fst) with
        | .error code => errToJson c.id code
        | .ok decoded =>
          Json.mkObj [
            ("id", toJson c.id),
            ("ok", toJson true),
            ("actors", Json.arr (co.actors.map toJson).toArray),
            ("state", ownerStateToJson decoded)
          ]

end Nayutar.Drt.Ownership
