import Lean.Data.Json
import Nayutar.OrderedSet

set_option autoImplicit false

open Lean
open Nayutar.Envelope (Value Values Fields)
open Nayutar.OrderedSet

namespace Nayutar.Drt.OrderedSet

inductive Op where
  | entries (set : Fields)
  | entriesReverse (set : Fields)
  | renumber (set : Fields)
  | fromItems (items : List Item)
  | fromKeyedSlice (elems : List Fields) (keyField : String)
  | toKeyedSlice (set : Fields) (keyField : String)
  | toKeyedSliceReverse (set : Fields) (keyField : String)
  | orderBefore (first : Int)
  | orderAfter (last : Int)
  | orderBetween (lo hi : Int)

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

private def parseRecord (j : Json) : Except String Fields := do
  match ← parseValue j with
  | .record fs => pure fs
  | _ => throw "expected record value"

private def recField (j : Json) (field : String) : Except String Fields := do
  parseRecord (← j.getObjVal? field)

private def parseItem (j : Json) : Except String Item := do
  let key ← str j "key"
  let value ← recField j "value"
  return { key := key, value := value }

def parseCase (line : String) : Except String Case := do
  let j ← Json.parse line
  let id ← nat j "id"
  let op ← match ← str j "op" with
    | "entries" => do
      let set ← recField j "set"
      pure (.entries set)
    | "entriesReverse" => do
      let set ← recField j "set"
      pure (.entriesReverse set)
    | "renumber" => do
      let set ← recField j "set"
      pure (.renumber set)
    | "fromItems" => do
      let arr ← (← j.getObjVal? "items").getArr?
      let items ← arr.toList.mapM parseItem
      pure (.fromItems items)
    | "fromKeyedSlice" => do
      let keyField ← str j "keyField"
      let arr ← (← j.getObjVal? "elems").getArr?
      let elems ← arr.toList.mapM parseRecord
      pure (.fromKeyedSlice elems keyField)
    | "toKeyedSlice" => do
      let set ← recField j "set"
      let keyField ← str j "keyField"
      pure (.toKeyedSlice set keyField)
    | "toKeyedSliceReverse" => do
      let set ← recField j "set"
      let keyField ← str j "keyField"
      pure (.toKeyedSliceReverse set keyField)
    | "orderBefore" => do
      let first ← intStr j "first"
      pure (.orderBefore first)
    | "orderAfter" => do
      let last ← intStr j "last"
      pure (.orderAfter last)
    | "orderBetween" => do
      let lo ← intStr j "lo"
      let hi ← intStr j "hi"
      pure (.orderBetween lo hi)
    | other => throw s!"unknown op: {other}"
  return { id := id, op := op }

private def errName : Error → String
  | .malformed => "malformed"
  | .duplicateKey => "duplicateKey"
  | .duplicateOrder => "duplicateOrder"
  | .reservedField => "reservedField"
  | .gapExhausted => "gapExhausted"
  | .overflow => "overflow"

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

private def entryToJson (e : Entry) : Json :=
  Json.mkObj [
    ("key", toJson e.key),
    ("order", toJson (toString e.order)),
    ("value", recordToJson e.value)
  ]

private def errToJson (id : Nat) (err : Error) : Json :=
  Json.mkObj [
    ("id", toJson id),
    ("ok", toJson false),
    ("err", toJson (errName err))
  ]

def caseToJson (c : Case) : Json :=
  match c.op with
  | .entries set =>
    match Nayutar.OrderedSet.entries set with
    | .error err => errToJson c.id err
    | .ok es =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("entries", Json.arr (es.map entryToJson).toArray)
      ]
  | .entriesReverse set =>
    match Nayutar.OrderedSet.entriesReverse set with
    | .error err => errToJson c.id err
    | .ok es =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("entries", Json.arr (es.map entryToJson).toArray)
      ]
  | .renumber set =>
    match Nayutar.OrderedSet.renumber set with
    | .error err => errToJson c.id err
    | .ok res =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("set", recordToJson res)
      ]
  | .fromItems items =>
    match Nayutar.OrderedSet.fromItems items with
    | .error err => errToJson c.id err
    | .ok res =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("set", recordToJson res)
      ]
  | .fromKeyedSlice elems keyField =>
    match Nayutar.OrderedSet.fromKeyedSlice elems keyField with
    | .error err => errToJson c.id err
    | .ok res =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("set", recordToJson res)
      ]
  | .toKeyedSlice set keyField =>
    match Nayutar.OrderedSet.toKeyedSlice set keyField with
    | .error err => errToJson c.id err
    | .ok elems =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("elems", Json.arr (elems.map recordToJson).toArray)
      ]
  | .toKeyedSliceReverse set keyField =>
    match Nayutar.OrderedSet.toKeyedSliceReverse set keyField with
    | .error err => errToJson c.id err
    | .ok elems =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("elems", Json.arr (elems.map recordToJson).toArray)
      ]
  | .orderBefore first =>
    match Nayutar.OrderedSet.orderBefore first with
    | .error err => errToJson c.id err
    | .ok ord =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("order", toJson (toString ord))
      ]
  | .orderAfter last =>
    match Nayutar.OrderedSet.orderAfter last with
    | .error err => errToJson c.id err
    | .ok ord =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("order", toJson (toString ord))
      ]
  | .orderBetween lo hi =>
    match Nayutar.OrderedSet.orderBetween lo hi with
    | .error err => errToJson c.id err
    | .ok ord =>
      Json.mkObj [
        ("id", toJson c.id),
        ("ok", toJson true),
        ("order", toJson (toString ord))
      ]

end Nayutar.Drt.OrderedSet
