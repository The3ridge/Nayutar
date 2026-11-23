import Lean.Data.Json
import Nayutar.Patch

set_option autoImplicit false

open Lean
open Nayutar.Envelope (Value Values Fields)
open Nayutar.Patch

namespace Nayutar.Drt.Patch

inductive CaseOp where
  | apply (doc : Fields) (ops : List Nayutar.Patch.Op)
  | diff (old next : Fields)
  | toRecord (ops : List Nayutar.Patch.Op)
  | fromRecord (doc : Fields)
  | applyEntrance (entrance : Entrance) (old : Fields) (rev : Int) (ops : List Nayutar.Patch.Op)
  | patch (old : Fields) (rev : Int) (ops : List Nayutar.Patch.Op)
  | patchStatus (old : Fields) (rev : Int) (ops : List Nayutar.Patch.Op) (now : String)

structure Case where
  id : Nat
  op : CaseOp

private def str (j : Json) (field : String) : Except String String := do
  (← j.getObjVal? field).getStr?

private def nat (j : Json) (field : String) : Except String Nat := do
  (← j.getObjVal? field).getNat?

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

private def intStr (j : Json) (field : String) : Except String Int := do
  let s ← str j field
  parseIntStr s

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

private def parseRecord (j : Json) : Except String Fields := do
  match ← parseValue j with
  | .record fs => pure fs
  | _ => throw "expected record value"

private def recField (j : Json) (field : String) : Except String Fields := do
  parseRecord (← j.getObjVal? field)

private def parseKind (s : String) : Except String OpKind :=
  match s with
  | "test" => pure .test
  | "set" => pure .set
  | "del" => pure .del
  | other => throw s!"unknown patch op kind: {other}"

private def parseEntrance (s : String) : Except String Entrance :=
  match s with
  | "spec" => pure .spec
  | "status" => pure .status
  | other => throw s!"unknown entrance: {other}"

private def parsePatchOp (j : Json) : Except String Nayutar.Patch.Op := do
  let kind ← parseKind (← str j "kind")
  let path ← str j "path"
  let val ← match j.getObjVal? "value" with
    | .error _ => pure none
    | .ok Json.null => pure none
    | .ok vj => do
      let v ← parseValue vj
      pure (some v)
  return { kind := kind, path := path, value := val }

private def parsePatchOps (j : Json) (field : String) : Except String (List Nayutar.Patch.Op) := do
  let arr ← (← j.getObjVal? field).getArr?
  arr.toList.mapM parsePatchOp

def parseCase (line : String) : Except String Case := do
  let j ← Json.parse line
  let id ← nat j "id"
  let op ← match ← str j "op" with
    | "apply" => do
      let doc ← recField j "doc"
      let ops ← parsePatchOps j "ops"
      pure (.apply doc ops)
    | "diff" => do
      let old ← recField j "old"
      let next ← recField j "next"
      pure (.diff old next)
    | "toRecord" => do
      let ops ← parsePatchOps j "ops"
      pure (.toRecord ops)
    | "fromRecord" => do
      let doc ← recField j "doc"
      pure (.fromRecord doc)
    | "applyEntrance" => do
      let entrance ← parseEntrance (← str j "entrance")
      let old ← recField j "old"
      let rev ← intStr j "rev"
      let ops ← parsePatchOps j "ops"
      pure (.applyEntrance entrance old rev ops)
    | "patch" => do
      let old ← recField j "old"
      let rev ← intStr j "rev"
      let ops ← parsePatchOps j "ops"
      pure (.patch old rev ops)
    | "patchStatus" => do
      let old ← recField j "old"
      let rev ← intStr j "rev"
      let ops ← parsePatchOps j "ops"
      let now ← str j "now"
      pure (.patchStatus old rev ops now)
    | other => throw s!"unknown op: {other}"
  return { id := id, op := op }

private def errName : Error → String
  | .internal => "internal"
  | .malformed => "malformed"
  | .conflict => "conflict"
  | .notFound => "notFound"
  | .forbidden => "forbidden"
  | .observed => "observed"

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

private def recordToJson (fs : Fields) : Json :=
  valueToJson (.record fs)

private def patchOpToJson (op : Nayutar.Patch.Op) : Json :=
  match op.value with
  | none =>
    Json.mkObj [
      ("kind", toJson (opKindName op.kind)),
      ("path", toJson op.path)
    ]
  | some v =>
    Json.mkObj [
      ("kind", toJson (opKindName op.kind)),
      ("path", toJson op.path),
      ("value", valueToJson v)
    ]

private def errToJson (id : Nat) (err : Error) : Json :=
  Json.mkObj [
    ("id", toJson id),
    ("ok", toJson false),
    ("err", toJson (errName err))
  ]

def caseToJson (c : Case) : Json :=
  match c.op with
  | .apply doc ops =>
    match Nayutar.Patch.apply doc ops with
    | .error err => errToJson c.id err
    | .ok res =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("doc", recordToJson res)
      ]
  | .diff old next =>
    match Nayutar.Patch.diff old next with
    | .error err => errToJson c.id err
    | .ok ops =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("ops", Json.arr (ops.map patchOpToJson).toArray)
      ]
  | .toRecord ops =>
    match Nayutar.Patch.toRecord ops with
    | .error err => errToJson c.id err
    | .ok res =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("doc", recordToJson res)
      ]
  | .fromRecord doc =>
    match Nayutar.Patch.fromRecord doc with
    | .error err => errToJson c.id err
    | .ok ops =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("ops", Json.arr (ops.map patchOpToJson).toArray)
      ]
  | .applyEntrance entrance old rev ops =>
    match Nayutar.Patch.applyEntrance entrance old rev ops with
    | .error err => errToJson c.id err
    | .ok res =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("doc", recordToJson res)
      ]
  | .patch old rev ops =>
    match Nayutar.Patch.patch old rev ops with
    | .error err => errToJson c.id err
    | .ok res =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("doc", valueToJson res)
      ]
  | .patchStatus old rev ops now =>
    match Nayutar.Patch.patchStatus old rev ops now with
    | .error err => errToJson c.id err
    | .ok res =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("doc", valueToJson res)
      ]

end Nayutar.Drt.Patch
