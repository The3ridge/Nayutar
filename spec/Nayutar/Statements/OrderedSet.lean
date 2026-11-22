import Nayutar.OrderedSet

set_option autoImplicit false

namespace Nayutar.OrderedSet

open Nayutar.Envelope (Value Fields)

theorem entries_strictly_sorted (set : Fields) (es : List Entry) (h : entries set = .ok es) :
    List.Pairwise (fun a b => a.order < b.order) es ∧
    List.Pairwise (fun a b => a.key ≠ b.key) es ∧
    ∀ e ∈ es, validKey e.key = true ∧ inInt64 e.order = true := by
  sorry

theorem entries_strips_order (set : Fields) (es : List Entry) (h : entries set = .ok es) :
    ∀ e ∈ es, (e.value.get orderField).isNone = true ∧ (Value.record e.value).unique = true := by
  sorry

theorem entries_complete_and_sound (set : Fields) (es : List Entry) (h : entries set = .ok es) :
    es.length = set.length ∧
    ∀ e ∈ es, ∃ rec, set.get e.key = some (.record rec) ∧
      rec.get orderField = some (.int e.order) ∧ e.value = stripField rec orderField := by
  sorry

theorem entries_reverse_eq_reverse (set : Fields) :
    entriesReverse set = (entries set).map List.reverse := by
  sorry

theorem entries_eqv_invariant (a b : Fields)
    (ha : (Value.record a).unique = true)
    (hb : (Value.record b).unique = true)
    (heqv : Value.eqv (.record a) (.record b) = true) :
    (∀ ea, entries a = .error ea ↔ entries b = .error ea) ∧
    (∀ esa esb, entries a = .ok esa → entries b = .ok esb →
      esa.length = esb.length ∧
      ∀ (i : Nat) (hia : i < esa.length) (hib : i < esb.length),
        (esa[i]'hia).key = (esb[i]'hib).key ∧
        (esa[i]'hia).order = (esb[i]'hib).order ∧
        Value.eqv (.record (esa[i]'hia).value) (.record (esb[i]'hib).value) = true) := by
  sorry

theorem fromItems_entries_roundtrip (items : List Item) (set : Fields)
    (h : fromItems items = .ok set) :
    (Value.record set).unique = true ∧
    ∃ es, entries set = .ok es ∧ es.length = items.length ∧
      ∀ (i : Nat) (hi : i < items.length) (he : i < es.length),
        (es[i]'he).key = (items[i]'hi).key ∧
        (es[i]'he).order = step * (Int.ofNat i + 1) ∧
        (es[i]'he).value = (items[i]'hi).value := by
  sorry

theorem keyed_slice_roundtrip (elems : List Fields) (keyField : String) (set : Fields)
    (h : fromKeyedSlice elems keyField = .ok set) :
    (Value.record set).unique = true ∧
    ∃ out, toKeyedSlice set keyField = .ok out ∧ out.length = elems.length ∧
      ∀ (i : Nat) (hi : i < elems.length) (ho : i < out.length),
        Value.eqv (.record (out[i]'ho)) (.record (elems[i]'hi)) = true := by
  sorry

theorem orderBetween_strict (lo hi : Int) :
    (∀ mid, orderBetween lo hi = .ok mid → lo < mid ∧ mid < hi ∧ inInt64 mid = true) ∧
    (inInt64 lo = true → inInt64 hi = true → lo < hi → hi - lo = 1 →
      orderBetween lo hi = .error .gapExhausted) := by
  sorry

theorem renumber_preserves_order (set set' : Fields) (h : renumber set = .ok set') :
    (Value.record set').unique = true ∧
    ∃ es es', entries set = .ok es ∧ entries set' = .ok es' ∧ es'.length = es.length ∧
      ∀ (i : Nat) (he : i < es.length) (he' : i < es'.length),
        (es'[i]'he').key = (es[i]'he).key ∧
        (es'[i]'he').order = step * (Int.ofNat i + 1) ∧
        (es'[i]'he').value = (es[i]'he).value := by
  sorry

end Nayutar.OrderedSet
