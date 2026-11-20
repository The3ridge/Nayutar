import Lean.Data.Json
import Nayutar.WatchCache

open Lean
open Nayutar.Store (KV Write Event)
open Nayutar.WatchCache

namespace Nayutar.Drt.WatchCache

inductive Op where
  | init (rev : Nat) (kvs : List KV)
  | event (event : Event) (now : Nat)
  | progress (rev : Nat)
  | get (key : String) (rev : Nat)
  | range (keyPrefix : String) (rev : Nat) (limit : Nat) (cont : String)
  | watch (keyPrefix : String) (rev : Nat)
  | watchList (keyPrefix : String) (rev : Nat)
  | poll (watch : Nat) (limit : Nat)
  | bookmark (watch : Nat)

structure Case where
  id : Nat
  config : Config
  rev : Nat
  kvs : List KV
  ops : List Op

/-- One step's result, followed by the state of the cache after the step. -/
structure Step where
  err : String := ""
  ok : Bool := false
  rev : Nat := 0
  kvs : List KV := []
  cont : String := ""
  events : List Event := []
  closed : Nat := 0
  isOpen : Bool := false
  floor : Nat := 0
  retained : Nat := 0

private def str (j : Json) (field : String) : Except String String := do
  (← j.getObjVal? field).getStr?

private def nat (j : Json) (field : String) : Except String Nat := do
  (← j.getObjVal? field).getNat?

private def list (j : Json) (field : String) (parse : Json → Except String α) : Except String (List α) := do
  let arr ← (← j.getObjVal? field).getArr?
  arr.toList.mapM parse

private def parseKV (j : Json) : Except String KV := do
  return { key := ← str j "key", value := ← str j "value", modRev := ← nat j "mod" }

private def parseWrite (j : Json) : Except String Write := do
  match ← str j "type" with
  | "put" => return .put (← str j "key") (← str j "value")
  | "delete" => return .delete (← str j "key")
  | other => throw s!"unknown event type: {other}"

private def parseWatch (watches : Nat) (j : Json) : Except String Nat := do
  let watch ← nat j "watch"
  if watch < watches then return watch
  else throw s!"watch {watch} used, but only {watches} exist"

private def parseOp (watches : Nat) (j : Json) : Except String Op := do
  match ← str j "op" with
  | "init" => return .init (← nat j "rev") (← list j "kvs" parseKV)
  | "event" => return .event { rev := ← nat j "rev", write := ← parseWrite j } (← nat j "now")
  | "progress" => return .progress (← nat j "rev")
  | "get" => return .get (← str j "key") (← nat j "rev")
  | "range" => return .range (← str j "prefix") (← nat j "rev") (← nat j "limit") (← str j "cont")
  | "watch" => return .watch (← str j "prefix") (← nat j "rev")
  | "watchlist" => return .watchList (← str j "prefix") (← nat j "rev")
  | "poll" => return .poll (← parseWatch watches j) (← nat j "limit")
  | "bookmark" => return .bookmark (← parseWatch watches j)
  | other => throw s!"unknown op: {other}"

private def parseConfig (j : Json) : Except String Config := do
  return { minEvents := ← nat j "min_events", minAge := ← nat j "min_age", maxEvents := ← nat j "max_events" }

def parseCase (line : String) : Except String Case := do
  let j ← Json.parse line
  let id ← nat j "id"
  let config ← parseConfig (← j.getObjVal? "config")
  let rev ← nat j "rev"
  let kvs ← list j "kvs" parseKV
  let arr ← (← j.getObjVal? "ops").getArr?
  let mut ops : Array Op := #[]
  let mut watches := 0
  for elem in arr do
    let op ← parseOp watches elem
    match op with
    | .watch .. | .watchList .. => watches := watches + 1
    | _ => pure ()
    ops := ops.push op
  return { id := id, config := config, rev := rev, kvs := kvs, ops := ops.toList }

private def errName : Error → String
  | .notFound => "not_found"
  | .tooOld => "too_old"
  | .notYet => "not_yet"
  | .invalid => "invalid"

/-- The cache, its generation, and every watch opened so far with the generation it belongs to.
A watch that failed to open stays in the table as `none`. Listing again starts a new generation,
and the watches of an older one are too old. -/
private structure World where
  cache : Cache
  generation : Nat
  watchers : Array (Option (Watcher × Nat))

private def step (w : World) : Op → Step × World
  | .init rev kvs =>
    ({ ok := true }, { w with cache := Cache.init w.cache.config rev kvs, generation := w.generation + 1 })
  | .event e now =>
    match w.cache.feed (.event e now) with
    | some c => ({ ok := true }, { w with cache := c })
    | none => ({}, w)
  | .progress p =>
    match w.cache.feed (.progress p) with
    | some c => ({ ok := true }, { w with cache := c })
    | none => ({}, w)
  | .get key rev =>
    match w.cache.get key rev with
    | .error e => ({ err := errName e }, w)
    | .ok (some kv, r) => ({ rev := r, kvs := [kv] }, w)
    | .ok (none, r) => ({ err := errName .notFound, rev := r }, w)
  | .range keyPrefix rev limit cont =>
    match w.cache.range keyPrefix rev limit cont with
    | .error e => ({ err := errName e }, w)
    | .ok (kvs, r, next) => ({ rev := r, kvs := kvs, cont := next }, w)
  | .watch keyPrefix rev =>
    match w.cache.watch keyPrefix rev with
    | .error e => ({ err := errName e }, { w with watchers := w.watchers.push none })
    | .ok watcher => ({ ok := true }, { w with watchers := w.watchers.push (some (watcher, w.generation)) })
  | .watchList keyPrefix rev =>
    match w.cache.watchList keyPrefix rev with
    | .error e => ({ err := errName e }, { w with watchers := w.watchers.push none })
    | .ok (kvs, r, watcher) =>
      ({ ok := true, rev := r, kvs := kvs }, { w with watchers := w.watchers.push (some (watcher, w.generation)) })
  | .poll i limit =>
    match w.watchers[i]? with
    | some (some (watcher, generation)) =>
      if generation ≠ w.generation then ({ err := errName .tooOld }, w)
      else
        match w.cache.poll watcher limit with
        | .error e => ({ err := errName e }, w)
        | .ok (events, watcher) =>
          ({ ok := true, events := events }, { w with watchers := w.watchers.set! i (some (watcher, generation)) })
    | _ => ({ err := errName .invalid }, w)
  | .bookmark i =>
    match w.watchers[i]? with
    | some (some (watcher, generation)) =>
      if generation ≠ w.generation then ({}, w)
      else
        match w.cache.bookmark watcher with
        | some r => ({ ok := true, rev := r }, w)
        | none => ({}, w)
    | _ => ({}, w)

private def withState (out : Step) (c : Cache) : Step :=
  { out with closed := c.rev, isOpen := c.isOpen, floor := c.floor, retained := c.retained.length }

def run (c : Case) : List Step :=
  let rec go (w : World) : List Op → List Step
    | [] => []
    | op :: ops =>
      let (out, w) := step w op
      withState out w.cache :: go w ops
  go { cache := Cache.init c.config c.rev c.kvs, generation := 0, watchers := #[] } c.ops

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
    ("closed", toJson out.closed),
    ("open", toJson out.isOpen),
    ("floor", toJson out.floor),
    ("retained", toJson out.retained)
  ]

def caseToJson (c : Case) : Json :=
  Json.mkObj [
    ("id", toJson c.id),
    ("steps", Json.arr ((run c).map stepToJson).toArray)
  ]

end Nayutar.Drt.WatchCache
