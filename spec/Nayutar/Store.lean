namespace Nayutar.Store

abbrev Rev := Nat

structure KV where
  key : String
  value : String
  modRev : Rev
  deriving Repr, DecidableEq

inductive Write where
  | put (key : String) (value : String)
  | delete (key : String)
  deriving Repr, DecidableEq

def Write.key : Write → String
  | .put key _ => key
  | .delete key => key

/-- The effective changes of one transaction, in the order of its writes. -/
structure Commit where
  rev : Rev
  writes : List Write
  deriving Repr, DecidableEq

/-- A store is its revision, its compaction revision and every commit so far, oldest first.
Compaction only moves `compacted`: what a store may forget is expressed by the errors below. -/
structure Store where
  rev : Rev
  compacted : Rev
  commits : List Commit
  deriving Repr, DecidableEq

def Store.empty : Store := { rev := 1, compacted := 0, commits := [] }

inductive Error where
  | notFound
  | compacted
  | futureRev
  | invalid
  deriving Repr, DecidableEq

inductive CmpOp where
  | eq
  | ne
  | lt
  | gt
  deriving Repr, DecidableEq

structure Cmp where
  key : String
  op : CmpOp
  modRev : Rev
  deriving Repr, DecidableEq

def Cmp.holds (c : Cmp) (modRev : Rev) : Bool :=
  match c.op with
  | .eq => modRev == c.modRev
  | .ne => modRev != c.modRev
  | .lt => modRev < c.modRev
  | .gt => modRev > c.modRev

private def applyWrite (rev : Rev) (key : String) (acc : Option KV) : Write → Option KV
  | .put k v => if k = key then some { key := k, value := v, modRev := rev } else acc
  | .delete k => if k = key then none else acc

/-- The key as of revision `r`. -/
def Store.find? (s : Store) (r : Rev) (key : String) : Option KV :=
  s.commits.foldl
    (fun acc c => if c.rev ≤ r then c.writes.foldl (applyWrite c.rev key) acc else acc)
    none

def Store.modRev (s : Store) (key : String) : Rev :=
  match s.find? s.rev key with
  | some kv => kv.modRev
  | none => 0

/-- The revision a read at `atRev` reflects; `atRev = 0` reads the store revision. -/
def Store.readRev (s : Store) (atRev : Rev) : Except Error Rev :=
  if atRev = 0 then .ok s.rev
  else if atRev > s.rev then .error .futureRev
  else if atRev < s.compacted then .error .compacted
  else .ok atRev

/-- `Get`. An absent key reports the read revision next to `notFound`. -/
def Store.get (s : Store) (key : String) (atRev : Rev) : Except Error (Option KV × Rev) :=
  if key = "" then .error .invalid
  else do
    let r ← s.readRev atRev
    return (s.find? r key, r)

private def insertKey (keys : List String) (key : String) : List String :=
  if keys.contains key then keys else key :: keys

private def writtenKeys (s : Store) : List String :=
  s.commits.foldl (fun acc c => c.writes.foldl (fun acc w => insertKey acc w.key) acc) []

/-- `Range`: one page, and the continuation when keys remain after it. -/
def Store.range (s : Store) (keyPrefix : String) (atRev : Rev) (limit : Nat) (cont : String) :
    Except Error (List KV × Rev × String) :=
  if cont ≠ "" ∧ ¬ keyPrefix.isPrefixOf cont then .error .invalid
  else do
    let r ← s.readRev atRev
    let keys := (writtenKeys s).filter (fun k => keyPrefix.isPrefixOf k && decide (cont < k))
    let sorted := keys.mergeSort (fun a b => decide (a ≤ b))
    let all := sorted.filterMap (s.find? r)
    if limit ≠ 0 ∧ all.length > limit then
      let page := all.take limit
      let next := match page.getLast? with
        | some kv => kv.key
        | none => ""
      return (page, r, next)
    else
      return (all, r, "")

private def distinctKeys : List String → Bool
  | [] => true
  | k :: ks => !ks.contains k && distinctKeys ks

/-- `Txn`: whether the comparisons held, and the store revision afterwards. -/
def Store.txn (s : Store) (cmps : List Cmp) (writes : List Write) : Except Error (Bool × Rev) × Store :=
  if cmps.any (fun c => c.key == "") || writes.any (fun w => w.key == "")
      || !distinctKeys (writes.map Write.key) then
    (.error .invalid, s)
  else if !cmps.all (fun c => c.holds (s.modRev c.key)) then
    (.ok (false, s.rev), s)
  else
    let effective := writes.filter fun
      | .put _ _ => true
      | .delete key => (s.find? s.rev key).isSome
    if effective.isEmpty then
      (.ok (true, s.rev), s)
    else
      let rev := s.rev + 1
      (.ok (true, rev), { s with rev := rev, commits := s.commits ++ [{ rev := rev, writes := effective }] })

/-- `Compact`. -/
def Store.compact (s : Store) (rev : Rev) : Except Error Unit × Store :=
  if rev = 0 then (.error .invalid, s)
  else if rev ≤ s.compacted then (.error .compacted, s)
  else if rev > s.rev then (.error .futureRev, s)
  else (.ok (), { s with compacted := rev })

structure Event where
  rev : Rev
  write : Write
  deriving Repr, DecidableEq

/-- The position of one watch: `next` is the first revision it has not delivered. -/
structure Watcher where
  keyPrefix : String
  fromRev : Rev
  next : Rev
  compacted : Bool
  deriving Repr, DecidableEq

/-- `Watch`. A `fromRev` below the compaction revision ends the watch at once.
The model describes a watch that has caught up. A store may additionally end a lagging watch
with `compacted` when a compaction passes changes it has not delivered; the model leaves that out. -/
def Store.watch (s : Store) (keyPrefix : String) (fromRev : Rev) : Watcher :=
  { keyPrefix := keyPrefix
    fromRev := fromRev
    next := if fromRev = 0 then s.rev + 1 else fromRev
    compacted := fromRev ≠ 0 ∧ fromRev < s.compacted }

/-- What a watch has delivered once a progress request is answered: the pending changes and
the progress revision. Below `fromRev` the store answers no request, so there is no progress
revision and, since nothing at or above `fromRev` exists yet, no change either. -/
def Store.poll (s : Store) (w : Watcher) : Except Error (List Event × Option Rev) × Watcher :=
  if w.compacted then (.error .compacted, w)
  else if s.rev < w.fromRev then (.ok ([], none), w)
  else
    let pending := s.commits.filter (fun c => w.next ≤ c.rev)
    let events := pending.flatMap fun c =>
      (c.writes.filter (fun wr => w.keyPrefix.isPrefixOf wr.key)).map (fun wr => { rev := c.rev, write := wr })
    (.ok (events, some s.rev), { w with next := s.rev + 1 })

end Nayutar.Store
