import Lean.Data.Json
import Nayutar.Envelope

open Lean
open Nayutar.Envelope

namespace Nayutar.Drt.Envelope

inductive Op where
  | create (cand : Value) (uid : String)
  | update (old : Value) (rev : Int) (cand : Value)
  | status (old : Value) (rev : Int) (cand : Value) (now : String)

structure Case where
  id : Nat
  op : Op

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

private def val (j : Json) (field : String) : Except String Value := do
  parseValue (← j.getObjVal? field)

def parseCase (line : String) : Except String Case := do
  let j ← Json.parse line
  let id ← nat j "id"
  let op ← match ← str j "op" with
    | "create" => do
      let cand ← val j "cand"
      let uid ← str j "uid"
      pure (.create cand uid)
    | "update" => do
      let old ← val j "old"
      let rev ← intStr j "rev"
      let cand ← val j "cand"
      pure (.update old rev cand)
    | "status" => do
      let old ← val j "old"
      let rev ← intStr j "rev"
      let cand ← val j "cand"
      let now ← str j "now"
      pure (.status old rev cand now)
    | other => throw s!"unknown op: {other}"
  return { id := id, op := op }

private def errName : Error → String
  | .internal => "internal"
  | .malformed => "malformed"
  | .conflict => "conflict"
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

private def evalOp : Op → Except Error Value
  | .create cand uid => Nayutar.Envelope.create cand uid
  | .update old rev cand => Nayutar.Envelope.update old rev cand
  | .status old rev cand now => Nayutar.Envelope.updateStatus old rev cand now

def caseToJson (c : Case) : Json :=
  match evalOp c.op with
  | .ok doc =>
    Json.mkObj [
      ("id", toJson c.id),
      ("ok", toJson true),
      ("doc", valueToJson doc)
    ]
  | .error err =>
    Json.mkObj [
      ("id", toJson c.id),
      ("ok", toJson false),
      ("err", toJson (errName err))
    ]

end Nayutar.Drt.Envelope
