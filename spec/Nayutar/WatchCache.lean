import Nayutar.Store

namespace Nayutar.WatchCache

open Nayutar.Store (Rev KV Write Event)

/-- How much history a cache keeps: at least `minEvents` changes and at least `minAge` of time,
whichever is more, but never more than `maxEvents` changes. Times are milliseconds. -/
structure Config where
  minEvents : Nat
  minAge : Nat
  maxEvents : Nat
  deriving Repr, DecidableEq

/-- One change received from the store, with the time it was received. -/
structure Entry where
  event : Event
  time : Nat
  deriving Repr, DecidableEq

/-- A cache is the listed state, every change received since, oldest first, and how far it has
forgotten. Forgetting only moves `start` and `floor`: what a cache may forget is expressed by the
errors below.

`rev` is the closed revision: every change up to it has been received. `isOpen` says that changes
of revision `rev + 1` have been received and more of them may follow. -/
structure Cache where
  config : Config
  initial : List KV
  log : List Entry
  start : Nat
  floor : Rev
  rev : Rev
  isOpen : Bool
  deriving Repr, DecidableEq

inductive Error where
  | notFound
  | tooOld
  | notYet
  | invalid
  deriving Repr, DecidableEq

/-- What the store sends on the watch that feeds a cache. -/
inductive Input where
  | event (event : Event) (now : Nat)
  | progress (rev : Rev)
  deriving Repr, DecidableEq

/-- A cache that has just listed `kvs` as of `listRev`. -/
def Cache.init (config : Config) (listRev : Rev) (kvs : List KV) : Cache :=
  { config := config, initial := kvs, log := [], start := 0, floor := listRev, rev := listRev, isOpen := false }

/-- The changes a cache still serves. -/
def Cache.retained (c : Cache) : List Entry :=
  c.log.drop c.start

/-- Forgets the oldest retained revision when the retention rule allows it. A revision is forgotten
as a whole, and never while it is open. -/
def Cache.evictOne (c : Cache) (now : Nat) : Option Cache :=
  match c.retained with
  | [] => none
  | first :: rest =>
    let r := first.event.rev
    if c.rev < r then none
    else
      let group := first :: rest.takeWhile (fun e => e.event.rev = r)
      let newest := match group.getLast? with
        | some e => e.time
        | none => first.time
      let over := c.config.maxEvents < rest.length + 1
      let old := c.config.minEvents + group.length ≤ rest.length + 1 ∧ c.config.minAge < now - newest
      if over ∨ old then some { c with start := c.start + group.length, floor := r } else none

def Cache.evictLoop : Nat → Cache → Nat → Cache
  | 0, c, _ => c
  | fuel + 1, c, now =>
    match c.evictOne now with
    | none => c
    | some c' => Cache.evictLoop fuel c' now

def Cache.evict (c : Cache) (now : Nat) : Cache :=
  Cache.evictLoop c.log.length c now

/-- One input from the store. `none` when the input breaks the store contract: a change at or
below the closed revision, or a progress notification below a revision already seen. -/
def Cache.feed (c : Cache) : Input → Option Cache
  | .event e now =>
    if c.rev < e.rev then
      some (Cache.evict { c with log := c.log ++ [{ event := e, time := now }], rev := e.rev - 1, isOpen := true } now)
    else none
  | .progress p =>
    if (if c.isOpen then c.rev + 1 else c.rev) ≤ p then some { c with rev := p, isOpen := false }
    else none

def Cache.feedAll (c : Cache) : List Input → Option Cache
  | [] => some c
  | i :: inputs =>
    match c.feed i with
    | none => none
    | some c' => c'.feedAll inputs

private def applyEntry (key : String) (acc : Option KV) (e : Entry) : Option KV :=
  match e.event.write with
  | .put k v => if k = key then some { key := k, value := v, modRev := e.event.rev } else acc
  | .delete k => if k = key then none else acc

/-- The key as of revision `r`. -/
def Cache.find? (c : Cache) (r : Rev) (key : String) : Option KV :=
  (c.log.filter (fun e => e.event.rev ≤ r)).foldl (applyEntry key) (c.initial.find? (fun kv => kv.key = key))

/-- The revision a read at `atRev` reflects; `atRev = 0` reads the closed revision. -/
def Cache.readRev (c : Cache) (atRev : Rev) : Except Error Rev :=
  if atRev = 0 then .ok c.rev
  else if c.rev < atRev then .error .notYet
  else if atRev < c.floor then .error .tooOld
  else .ok atRev

def Cache.get (c : Cache) (key : String) (atRev : Rev) : Except Error (Option KV × Rev) :=
  if key = "" then .error .invalid
  else do
    let r ← c.readRev atRev
    return (c.find? r key, r)

private def insertKey (keys : List String) (key : String) : List String :=
  if keys.contains key then keys else key :: keys

private def knownKeys (c : Cache) : List String :=
  c.log.foldl (fun acc e => insertKey acc e.event.write.key) (c.initial.foldl (fun acc kv => insertKey acc kv.key) [])

/-- The keys starting with `keyPrefix` and above `cont`, as of revision `r`, in ascending order. -/
def Cache.list (c : Cache) (r : Rev) (keyPrefix : String) (cont : String) : List KV :=
  let keys := (knownKeys c).filter (fun k => keyPrefix.isPrefixOf k && decide (cont < k))
  (keys.mergeSort (fun a b => decide (a ≤ b))).filterMap (c.find? r)

/-- One page, and the continuation when keys remain after it. -/
def Cache.range (c : Cache) (keyPrefix : String) (atRev : Rev) (limit : Nat) (cont : String) :
    Except Error (List KV × Rev × String) :=
  if cont ≠ "" ∧ ¬ keyPrefix.isPrefixOf cont then .error .invalid
  else do
    let r ← c.readRev atRev
    let all := c.list r keyPrefix cont
    if limit ≠ 0 ∧ all.length > limit then
      let page := all.take limit
      let next := match page.getLast? with
        | some kv => kv.key
        | none => ""
      return (page, r, next)
    else
      return (all, r, "")

/-- The position of one watch: `next` counts the received changes it is past. -/
structure Watcher where
  keyPrefix : String
  next : Nat
  deriving Repr, DecidableEq

def Watcher.matches (w : Watcher) (e : Event) : Bool :=
  w.keyPrefix.isPrefixOf e.write.key

/-- A watch beginning with revision `fromRev`, or just after the closed revision when `fromRev`
is 0. -/
def Cache.watch (c : Cache) (keyPrefix : String) (fromRev : Rev) : Except Error Watcher :=
  let first := if fromRev = 0 then c.rev + 1 else fromRev
  if first ≤ c.floor then .error .tooOld
  else if c.rev + 1 < first then .error .notYet
  else .ok { keyPrefix := keyPrefix, next := (c.log.takeWhile (fun e => e.event.rev < first)).length }

/-- A watch that begins with the state: the state as of the closed revision, that revision, and
a watch of everything after it. It needs the closed revision to have reached `minRev`. -/
def Cache.watchList (c : Cache) (keyPrefix : String) (minRev : Rev) : Except Error (List KV × Rev × Watcher) :=
  if c.rev < minRev then .error .notYet
  else
    .ok (c.list c.rev keyPrefix "", c.rev,
      { keyPrefix := keyPrefix, next := (c.log.takeWhile (fun e => e.event.rev ≤ c.rev)).length })

/-- The changes a watch has not passed yet. -/
def Cache.remaining (c : Cache) (w : Watcher) : List Event :=
  (c.log.drop w.next).map (fun e => e.event)

/-- The changes a watch is still owed. -/
def Cache.pending (c : Cache) (w : Watcher) : List Event :=
  (c.remaining w).filter w.matches

/-- Delivers the next changes of a watch, at most `limit` received changes at a time when `limit`
is not 0. It fails once the cache has forgotten a change the watch has not passed. -/
def Cache.poll (c : Cache) (w : Watcher) (limit : Nat) : Except Error (List Event × Watcher) :=
  if w.next < c.start then .error .tooOld
  else
    let rest := c.remaining w
    let taken := if limit = 0 then rest else rest.take limit
    .ok (taken.filter w.matches, { w with next := w.next + taken.length })

/-- The bookmark a watch may be sent: the closed revision, once no revision is open and the watch
is past every received change. -/
def Cache.bookmark (c : Cache) (w : Watcher) : Option Rev :=
  if w.next = c.log.length ∧ c.isOpen = false then some c.rev else none

/-- What happens during the life of one watch. -/
inductive Step where
  | feed (input : Input)
  | poll (limit : Nat)
  | bookmark
  deriving Repr, DecidableEq

/-- What a watch sends. -/
inductive Out where
  | event (event : Event)
  | bookmark (rev : Rev)
  deriving Repr, DecidableEq

def Out.rev : Out → Rev
  | .event e => e.rev
  | .bookmark r => r

/-- The changes among the outputs, in order. -/
def Out.events : List Out → List Event
  | [] => []
  | .event e :: outs => e :: Out.events outs
  | .bookmark _ :: outs => Out.events outs

/-- The changes the store sends during the steps, in order. -/
def Step.fedEvents : List Step → List Event
  | [] => []
  | .feed (.event e _) :: steps => e :: Step.fedEvents steps
  | _ :: steps => Step.fedEvents steps

/-- What a bookmark promises about an earlier output `a` and a later output `b` of one watch:
nothing sent before a bookmark is above it, and every change sent after it is above it. -/
def Out.ordered : Out → Out → Prop
  | a, .bookmark r => a.rev ≤ r
  | .bookmark r, .event e => r < e.rev
  | .event _, .event _ => True

/-- Runs one watch through the steps and collects what it sends. A bookmark step sends nothing
when no bookmark is due. `none` when an input breaks the store contract or the cache has forgotten
a change the watch has not passed. -/
def session (c : Cache) (w : Watcher) : List Step → Option (Cache × Watcher × List Out)
  | [] => some (c, w, [])
  | .feed input :: steps =>
    match c.feed input with
    | none => none
    | some c' => session c' w steps
  | .poll limit :: steps =>
    match c.poll w limit with
    | .error _ => none
    | .ok (events, w') =>
      match session c w' steps with
      | none => none
      | some (c', w'', outs) => some (c', w'', events.map Out.event ++ outs)
  | .bookmark :: steps =>
    match session c w steps with
    | none => none
    | some (c', w', outs) =>
      match c.bookmark w with
      | none => some (c', w', outs)
      | some r => some (c', w', Out.bookmark r :: outs)

end Nayutar.WatchCache
