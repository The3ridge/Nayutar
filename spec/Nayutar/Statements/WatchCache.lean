import Nayutar.WatchCache

namespace Nayutar.WatchCache

open Nayutar.Store (Rev Event)

/-- No loss: what a watch was owed when the session began, plus the matching changes the store
sent during it, is exactly what the watch was sent followed by what it is still owed. -/
theorem watch_no_loss (c : Cache) (w : Watcher) (steps : List Step) (c' : Cache) (w' : Watcher)
    (outs : List Out) (hw : w.next ≤ c.log.length) (hs : session c w steps = some (c', w', outs)) :
    c.pending w ++ (Step.fedEvents steps).filter w.matches = Out.events outs ++ c'.pending w' := by
  sorry

/-- No duplicates: what a watch was sent is a subsequence of what it was owed, so no change is sent
twice and none is invented. -/
theorem watch_no_duplicates (c : Cache) (w : Watcher) (steps : List Step) (c' : Cache) (w' : Watcher)
    (outs : List Out) (hw : w.next ≤ c.log.length) (hs : session c w steps = some (c', w', outs)) :
    (Out.events outs).Sublist (c.pending w ++ (Step.fedEvents steps).filter w.matches) := by
  sorry

/-- Order: the revisions of the changes a watch is sent never decrease. -/
theorem watch_order_preserved (config : Config) (listRev : Rev) (kvs : List Nayutar.Store.KV)
    (inputs : List Input) (c : Cache) (w : Watcher) (steps : List Step) (c' : Cache) (w' : Watcher)
    (outs : List Out) (hc : (Cache.init config listRev kvs).feedAll inputs = some c)
    (hw : w.next ≤ c.log.length) (hs : session c w steps = some (c', w', outs)) :
    (Out.events outs).Pairwise (fun a b => a.rev ≤ b.rev) := by
  sorry

/-- Bookmark monotonicity: nothing sent before a bookmark is above it, and every change sent after
it is above it. -/
theorem bookmark_monotone (config : Config) (listRev : Rev) (kvs : List Nayutar.Store.KV)
    (inputs : List Input) (c : Cache) (w : Watcher) (steps : List Step) (c' : Cache) (w' : Watcher)
    (outs : List Out) (hc : (Cache.init config listRev kvs).feedAll inputs = some c)
    (hw : w.next ≤ c.log.length) (hs : session c w steps = some (c', w', outs)) :
    outs.Pairwise Out.ordered := by
  sorry

end Nayutar.WatchCache
