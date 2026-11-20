import Lean.Data.Json
import Nayutar.Store

open Lean
open Nayutar.Store

namespace Nayutar.Drt.Store

inductive Op where
  | get (key : String) (rev : Nat)
  | range (keyPrefix : String) (rev : Nat) (limit : Nat) (cont : String)
  | txn (cmps : List Cmp) (writes : List Write)
  | rev
  | compact (rev : Nat)
  | watch (keyPrefix : String) (rev : Nat)
  | progress (watch : Nat)

structure Case where
  id : Nat
  ops : List Op

structure Step where
  err : String := ""
  ok : Bool := false
  rev : Nat := 0
  kvs : List KV := []
  cont : String := ""
  events : List Event := []
  progress : Nat := 0

private def str (j : Json) (field : String) : Except String String := do
  (← j.getObjVal? field).getStr?

private def nat (j : Json) (field : String) : Except String Nat := do
  (← j.getObjVal? field).getNat?

private def list (j : Json) (field : String) (parse : Json → Except String α) : Except String (List α) := do
  let arr ← (← j.getObjVal? field).getArr?
  arr.toList.mapM parse

private def parseCmp (j : Json) : Except String Cmp := do
  let op ← match ← str j "cmp" with
    | "eq" => pure CmpOp.eq
    | "ne" => pure CmpOp.ne
    | "lt" => pure CmpOp.lt
    | "gt" => pure CmpOp.gt
    | other => throw s!"unknown cmp: {other}"
  return { key := ← str j "key", op := op, modRev := ← nat j "mod" }

private def parseWrite (j : Json) : Except String Write := do
  match ← str j "type" with
  | "put" => return .put (← str j "key") (← str j "value")
  | "delete" => return .delete (← str j "key")
  | other => throw s!"unknown write type: {other}"

private def parseOp (watches : Nat) (j : Json) : Except String Op := do
  match ← str j "op" with
  | "get" => return .get (← str j "key") (← nat j "rev")
  | "range" => return .range (← str j "prefix") (← nat j "rev") (← nat j "limit") (← str j "cont")
  | "txn" => return .txn (← list j "cmps" parseCmp) (← list j "ops" parseWrite)
  | "rev" => return .rev
  | "compact" => return .compact (← nat j "rev")
  | "watch" => return .watch (← str j "prefix") (← nat j "rev")
  | "progress" =>
    let watch ← nat j "watch"
    if watch < watches then return .progress watch
    else throw s!"progress on watch {watch}, but only {watches} exist"
  | other => throw s!"unknown op: {other}"

def parseCase (line : String) : Except String Case := do
  let j ← Json.parse line
  let id ← nat j "id"
  let arr ← (← j.getObjVal? "ops").getArr?
  let mut ops : Array Op := #[]
  let mut watches := 0
  for elem in arr do
    let op ← parseOp watches elem
    if let .watch .. := op then watches := watches + 1
    ops := ops.push op
  return { id := id, ops := ops.toList }

private def errName : Error → String
  | .notFound => "not_found"
  | .compacted => "compacted"
  | .futureRev => "future_rev"
  | .invalid => "invalid"

private def pollStep (s : Nayutar.Store.Store) (w : Watcher) : Step × Watcher :=
  match s.poll w with
  | (.error e, w) => ({ err := errName e }, w)
  | (.ok (events, progress), w) => ({ events := events, progress := progress.getD 0 }, w)

private def step (s : Nayutar.Store.Store) (watchers : Array Watcher) :
    Op → Step × Nayutar.Store.Store × Array Watcher
  | .get key rev =>
    match s.get key rev with
    | .error e => ({ err := errName e }, s, watchers)
    | .ok (some kv, r) => ({ rev := r, kvs := [kv] }, s, watchers)
    | .ok (none, r) => ({ err := errName .notFound, rev := r }, s, watchers)
  | .range keyPrefix rev limit cont =>
    match s.range keyPrefix rev limit cont with
    | .error e => ({ err := errName e }, s, watchers)
    | .ok (kvs, r, next) => ({ rev := r, kvs := kvs, cont := next }, s, watchers)
  | .txn cmps writes =>
    match s.txn cmps writes with
    | (.error e, s) => ({ err := errName e }, s, watchers)
    | (.ok (ok, r), s) => ({ ok := ok, rev := r }, s, watchers)
  | .rev => ({ rev := s.rev }, s, watchers)
  | .compact rev =>
    match s.compact rev with
    | (.error e, s) => ({ err := errName e }, s, watchers)
    | (.ok (), s) => ({}, s, watchers)
  | .watch keyPrefix rev =>
    let (out, w) := pollStep s (s.watch keyPrefix rev)
    (out, s, watchers.push w)
  | .progress i =>
    match watchers[i]? with
    | some w =>
      let (out, w) := pollStep s w
      (out, s, watchers.set! i w)
    | none => ({ err := errName .invalid }, s, watchers)

def run (ops : List Op) : List Step :=
  let rec go (s : Nayutar.Store.Store) (watchers : Array Watcher) : List Op → List Step
    | [] => []
    | op :: ops =>
      let (out, s, watchers) := step s watchers op
      out :: go s watchers ops
  go Nayutar.Store.Store.empty #[] ops

private def kvToJson (kv : KV) : Json :=
  Json.mkObj [("key", toJson kv.key), ("value", toJson kv.value), ("mod", toJson kv.modRev)]

private def eventToJson (ev : Event) : Json :=
  match ev.write with
  | .put key value =>
    Json.mkObj [("type", "put"), ("key", toJson key), ("value", toJson value), ("rev", toJson ev.rev)]
  | .delete key =>
    Json.mkObj [("type", "delete"), ("key", toJson key), ("value", ""), ("rev", toJson ev.rev)]

private def stepToJson (out : Step) : Json :=
  Json.mkObj [
    ("err", toJson out.err),
    ("ok", toJson out.ok),
    ("rev", toJson out.rev),
    ("kvs", Json.arr (out.kvs.map kvToJson).toArray),
    ("cont", toJson out.cont),
    ("events", Json.arr (out.events.map eventToJson).toArray),
    ("progress", toJson out.progress)
  ]

def caseToJson (c : Case) : Json :=
  Json.mkObj [
    ("id", toJson c.id),
    ("steps", Json.arr ((run c.ops).map stepToJson).toArray)
  ]

end Nayutar.Drt.Store
