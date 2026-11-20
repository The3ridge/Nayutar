import Lean.Data.Json
import Nayutar.Refs

open Lean
open Nayutar.Envelope (Value Values Fields)
open Nayutar.Refs

namespace Nayutar.Drt.Refs

inductive Case where
  | write (input : Input)
  | delete (stored : Value)

private def str (j : Json) (field : String) : Except String String := do
  (← j.getObjVal? field).getStr?

private def list (j : Json) (field : String) (parse : Json → Except String α) : Except String (List α) := do
  let arr ← (← j.getObjVal? field).getArr?
  arr.toList.mapM parse

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

private def parseObj (j : Json) : Except String Obj := do
  return {
    group := ← str j "group"
    kind := ← str j "kind"
    name := ← str j "name"
  }

private def parseRule (j : Json) : Except String Rule := do
  let source ← str j "source"
  let path ← list j "path" (fun elem => elem.getStr?)
  let targets ← list j "targets" (fun elem => elem.getStr?)
  return { source := source, path := path, targets := targets }

private def parseAllowEntry (j : Json) : Except String (String × List String) := do
  let path ← str j "path"
  let ref ← list j "ref" (fun elem => elem.getStr?)
  return (path, ref)

private def parseOptValue (j : Json) (field : String) : Except String (Option Value) := do
  let v ← j.getObjVal? field
  match v with
  | .null => pure none
  | _ => pure (some (← parseValue v))

def parseCase (line : String) : Except String Case := do
  let j ← Json.parse line
  match ← str j "op" with
  | "write" =>
    let source ← parseObj (← j.getObjVal? "source")
    let schema ← list j "schema" parseRule
    let stored ← parseOptValue j "stored"
    let next ← parseValue (← j.getObjVal? "next")
    let allowedOccs ← list j "allow" parseAllowEntry
    let presentObjs ← list j "present" parseObj
    return .write {
      source := source
      schema := schema
      stored := stored
      next := next
      allow := fun o => allowedOccs.contains (o.path, o.ref)
      present := fun o => presentObjs.contains o
    }
  | "delete" =>
    let stored ← parseValue (← j.getObjVal? "stored")
    return .delete stored
  | other => throw s!"unknown op: {other}"

private def objLe (a b : Obj) : Bool :=
  let qa := a.qkind
  let qb := b.qkind
  if qa == qb then decide (a.name ≤ b.name) else decide (qa ≤ qb)

private def sortObjs (xs : List Obj) : List Obj :=
  xs.mergeSort objLe

private def objToJson (o : Obj) : Json :=
  Json.mkObj [
    ("group", toJson o.group),
    ("kind", toJson o.kind),
    ("name", toJson o.name)
  ]

private def errName : Error → String
  | .internal => "internal"
  | .malformed => "malformed"
  | .kind => "kind"
  | .denied => "denied"
  | .notFound => "notFound"

def caseToJson (c : Case) : Json :=
  let res := match c with
    | .write i => check i
    | .delete s => deleteDelta s
  match res with
  | .ok d =>
    Json.mkObj [
      ("ok", toJson true),
      ("put", Json.arr ((sortObjs d.put).map objToJson).toArray),
      ("delete", Json.arr ((sortObjs d.delete).map objToJson).toArray),
      ("compare", Json.arr ((sortObjs d.compare).map objToJson).toArray)
    ]
  | .error (err, occOpt) =>
    let (pathJson, refJson) := match occOpt with
      | none => (Json.null, Json.null)
      | some occ => (toJson occ.path, Json.arr (occ.ref.map toJson).toArray)
    Json.mkObj [
      ("ok", toJson false),
      ("error", toJson (errName err)),
      ("path", pathJson),
      ("ref", refJson)
    ]

end Nayutar.Drt.Refs
