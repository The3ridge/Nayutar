import Nayutar.Envelope

set_option autoImplicit false

namespace Nayutar.OrderedSet

open Nayutar.Envelope (Value Values Fields)

def orderField : String := "order"

def step : Int := 10

def minInt64 : Int := -9223372036854775808

def maxInt64 : Int := 9223372036854775807

def inInt64 (n : Int) : Bool :=
  decide (minInt64 ≤ n ∧ n ≤ maxInt64)

def validKey (s : String) : Bool :=
  match s.toList with
  | [] => false
  | c :: cs => c != '$' && c != '/' && cs.all (fun x => x != '/')

def validKeyField (kf : String) : Bool :=
  validKey kf && kf != orderField

def stripField : Fields → String → Fields
  | .nil, _ => .nil
  | .cons k v rest, target =>
    if k = target then stripField rest target
    else .cons k v (stripField rest target)

inductive Error where
  | malformed
  | duplicateKey
  | duplicateOrder
  | reservedField
  | gapExhausted
  | overflow
  deriving DecidableEq, Repr

structure Item where
  key : String
  value : Fields

structure Entry where
  key : String
  order : Int
  value : Fields

def hasDup {α : Type} [DecidableEq α] : List α → Bool
  | [] => false
  | x :: xs => xs.contains x || hasDup xs

def insertEntry (e : Entry) : List Entry → List Entry
  | [] => [e]
  | x :: xs =>
    if e.order ≤ x.order then e :: x :: xs
    else x :: insertEntry e xs

def sortEntries : List Entry → List Entry
  | [] => []
  | e :: es => insertEntry e (sortEntries es)

def extractEntries : Fields → Option (List Entry)
  | .nil => some []
  | .cons k v rest =>
    if !validKey k then none
    else
      match v with
      | .record rec =>
        match rec.get orderField with
        | some (.int ord) =>
          if !inInt64 ord then none
          else
            match extractEntries rest with
            | some tail => some ({ key := k, order := ord, value := stripField rec orderField } :: tail)
            | none => none
        | _ => none
      | _ => none

def entries (set : Fields) : Except Error (List Entry) :=
  if !(Value.record set).unique then .error .malformed
  else
    match extractEntries set with
    | none => .error .malformed
    | some raw =>
      if hasDup (raw.map Entry.order) then .error .duplicateOrder
      else .ok (sortEntries raw)

def entriesReverse (set : Fields) : Except Error (List Entry) :=
  (entries set).map List.reverse

def buildFromItems (idx : Nat) : List Item → Fields
  | [] => .nil
  | it :: rest =>
    .cons it.key (.record (.cons orderField (.int (step * (Int.ofNat idx + 1))) it.value))
      (buildFromItems (idx + 1) rest)

def fromItems (items : List Item) : Except Error Fields :=
  if !(items.all (fun it => validKey it.key && (Value.record it.value).unique)) then
    .error .malformed
  else if !(items.all (fun it => (it.value.get orderField).isNone)) then
    .error .reservedField
  else if hasDup (items.map Item.key) then
    .error .duplicateKey
  else if !decide (step * Int.ofNat items.length ≤ maxInt64) then
    .error .overflow
  else
    .ok (buildFromItems 0 items)

def extractKeyedItems (keyField : String) : List Fields → Option (List Item)
  | [] => some []
  | el :: rest =>
    if !(Value.record el).unique then none
    else
      match el.get keyField with
      | some (.str k) =>
        if !validKey k then none
        else
          match extractKeyedItems keyField rest with
          | some tail => some ({ key := k, value := stripField el keyField } :: tail)
          | none => none
      | _ => none

def fromKeyedSlice (elems : List Fields) (keyField : String) : Except Error Fields :=
  if !validKeyField keyField then .error .malformed
  else
    match extractKeyedItems keyField elems with
    | none => .error .malformed
    | some items => fromItems items

def toKeyedSliceFromEntries (es : List Entry) (keyField : String) : Except Error (List Fields) :=
  if !(es.all (fun e => (e.value.get keyField).isNone)) then .error .reservedField
  else .ok (es.map (fun e => .cons keyField (.str e.key) e.value))

def toKeyedSlice (set : Fields) (keyField : String) : Except Error (List Fields) :=
  if !validKeyField keyField then .error .malformed
  else
    match entries set with
    | .error err => .error err
    | .ok es => toKeyedSliceFromEntries es keyField

def toKeyedSliceReverse (set : Fields) (keyField : String) : Except Error (List Fields) :=
  if !validKeyField keyField then .error .malformed
  else
    match entriesReverse set with
    | .error err => .error err
    | .ok es => toKeyedSliceFromEntries es keyField

def orderBefore (first : Int) : Except Error Int :=
  if !inInt64 first then .error .malformed
  else if first < minInt64 + step then .error .overflow
  else .ok (first - step)

def orderAfter (last : Int) : Except Error Int :=
  if !inInt64 last then .error .malformed
  else if last > maxInt64 - step then .error .overflow
  else .ok (last + step)

def orderBetween (lo hi : Int) : Except Error Int :=
  if !inInt64 lo || !inInt64 hi then .error .malformed
  else if decide (lo ≥ hi) then .error .malformed
  else if decide (lo = hi - 1) then .error .gapExhausted
  else .ok ((lo + hi) / 2)

def renumber (set : Fields) : Except Error Fields := do
  let es ← entries set
  fromItems (es.map (fun e => { key := e.key, value := e.value }))

end Nayutar.OrderedSet
