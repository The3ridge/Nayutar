package btree

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/store"
)

func (t Tree) checkInvariants() (int, error) {
	if t.root == nil {
		if t.length != 0 {
			return 0, fmt.Errorf("nil root with length %d", t.length)
		}
		return 0, nil
	}

	leafDepth := -1
	count := 0

	var walk func(n *node, depth int, hasMin bool, minKey string, hasMax bool, maxKey string, isRoot bool) error
	walk = func(n *node, depth int, hasMin bool, minKey string, hasMax bool, maxKey string, isRoot bool) error {
		if n == nil {
			return fmt.Errorf("unexpected nil child at depth %d", depth)
		}
		if isRoot {
			if n.nItems < 1 || n.nItems > maxKeys {
				return fmt.Errorf("root nItems=%d out of [1, %d]", n.nItems, maxKeys)
			}
		} else {
			if n.nItems < minKeys || n.nItems > maxKeys {
				return fmt.Errorf("node at depth %d has nItems=%d out of [%d, %d]", depth, n.nItems, minKeys, maxKeys)
			}
		}

		for i := 0; i < n.nItems; i++ {
			k := n.items[i].Key
			if hasMin && k <= minKey {
				return fmt.Errorf("key %q <= lower bound %q at depth %d", k, minKey, depth)
			}
			if hasMax && k >= maxKey {
				return fmt.Errorf("key %q >= upper bound %q at depth %d", k, maxKey, depth)
			}
			if i > 0 && k <= n.items[i-1].Key {
				return fmt.Errorf("keys out of order: %q after %q at depth %d", k, n.items[i-1].Key, depth)
			}
		}
		for i := n.nItems; i < maxKeys; i++ {
			if n.items[i].Key != "" || n.items[i].Value != nil || n.items[i].ModRev != 0 {
				return fmt.Errorf("uncleared item slot %d at depth %d", i, depth)
			}
		}

		count += n.nItems

		if n.leaf {
			for i := 0; i < maxChildren; i++ {
				if n.children[i] != nil {
					return fmt.Errorf("leaf has non-nil child at index %d", i)
				}
			}
			if leafDepth == -1 {
				leafDepth = depth
			} else if leafDepth != depth {
				return fmt.Errorf("inconsistent leaf depth: got %d, want %d", depth, leafDepth)
			}
			return nil
		}

		for i := n.nItems + 1; i < maxChildren; i++ {
			if n.children[i] != nil {
				return fmt.Errorf("internal node has non-nil unused child %d (nItems=%d)", i, n.nItems)
			}
		}

		for i := 0; i <= n.nItems; i++ {
			cHasMin, cMin := hasMin, minKey
			if i > 0 {
				cHasMin = true
				cMin = n.items[i-1].Key
			}
			cHasMax, cMax := hasMax, maxKey
			if i < n.nItems {
				cHasMax = true
				cMax = n.items[i].Key
			}
			if err := walk(n.children[i], depth+1, cHasMin, cMin, cHasMax, cMax, false); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(t.root, 1, false, "", false, "", true); err != nil {
		return 0, err
	}
	if count != t.length {
		return 0, fmt.Errorf("entry count %d != Len() %d", count, t.length)
	}
	return leafDepth, nil
}

func assertTreeEqualsMap(tb testing.TB, tr Tree, ref map[string]store.KV) {
	tb.Helper()
	if tr.Len() != len(ref) {
		tb.Fatalf("Len() = %d, want %d", tr.Len(), len(ref))
	}
	var got []store.KV
	tr.Ascend("", "", func(kv store.KV) bool {
		got = append(got, kv)
		return true
	})
	if len(got) != len(ref) {
		tb.Fatalf("Ascend yielded %d items, want %d", len(got), len(ref))
	}
	var want []store.KV
	for _, kv := range ref {
		want = append(want, kv)
	}
	slices.SortFunc(want, func(a, b store.KV) int {
		return strings.Compare(a.Key, b.Key)
	})
	for i := range want {
		if !kvEqual(got[i], want[i]) {
			tb.Fatalf("Ascend[%d] = %+v, want %+v", i, got[i], want[i])
		}
		v, ok := tr.Get(want[i].Key)
		if !ok || !kvEqual(v, want[i]) {
			tb.Fatalf("Get(%q) = (%+v, %v), want (%+v, true)", want[i].Key, v, ok, want[i])
		}
	}
}

func kvEqual(a, b store.KV) bool {
	return a.Key == b.Key && a.ModRev == b.ModRev && bytes.Equal(a.Value, b.Value)
}

func cloneRefMap(src map[string]store.KV) map[string]store.KV {
	dst := make(map[string]store.KV, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func TestValueNoCopyAndReplace(t *testing.T) {
	val := []byte("original")
	tr := Tree{}.Put(store.KV{Key: "k", Value: val, ModRev: 1})

	got, ok := tr.Get("k")
	if !ok {
		t.Fatal("expected key to exist")
	}
	if &got.Value[0] != &val[0] {
		t.Fatal("Put copied Value slice instead of storing it as given")
	}

	val2 := []byte("updated")
	tr2 := tr.Put(store.KV{Key: "k", Value: val2, ModRev: 2})
	if tr.Len() != 1 || tr2.Len() != 1 {
		t.Fatalf("unexpected lengths: tr=%d tr2=%d", tr.Len(), tr2.Len())
	}
	got1, _ := tr.Get("k")
	got2, _ := tr2.Get("k")
	if got1.ModRev != 1 || string(got1.Value) != "original" {
		t.Fatalf("old snapshot mutated: %+v", got1)
	}
	if got2.ModRev != 2 || string(got2.Value) != "updated" {
		t.Fatalf("new snapshot wrong: %+v", got2)
	}
}

func TestDeleteAbsentReturnsIdenticalTree(t *testing.T) {
	var empty Tree
	gotEmpty, ok := empty.Delete("missing")
	if ok || gotEmpty.root != nil || gotEmpty.Len() != 0 {
		t.Fatalf("Delete on empty tree = (%+v, %v), want (empty, false)", gotEmpty, ok)
	}

	tr := Tree{}
	for i := range 1000 {
		tr = tr.Put(store.KV{
			Key:    fmt.Sprintf("key-%04d", i*2),
			Value:  []byte{byte(i)},
			ModRev: store.Rev(i + 1),
		})
	}
	origRoot := tr.root
	origLen := tr.Len()

	for _, absent := range []string{"", "key-0001", "key-0999", "key-9999", "aaa", "zzz"} {
		got, ok := tr.Delete(absent)
		if ok {
			t.Fatalf("Delete(%q) returned true for absent key", absent)
		}
		if got.root != origRoot {
			t.Fatalf("Delete(%q) changed root pointer: %p != %p", absent, got.root, origRoot)
		}
		if got.Len() != origLen {
			t.Fatalf("Delete(%q) changed Len: %d != %d", absent, got.Len(), origLen)
		}
	}
}

func TestPropertySmallAlphabet(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 99))
	const alphabetSize = 200
	keys := make([]string, alphabetSize)
	for i := range keys {
		keys[i] = fmt.Sprintf("k%03d", i)
	}

	tr := Tree{}
	ref := make(map[string]store.KV)

	// Mix insert-heavy, balanced, and delete-heavy phases to exercise splits, borrows, and merges.
	phases := []struct {
		steps      int
		putPercent int
	}{
		{2500, 75},
		{5000, 50},
		{3000, 20},
		{2000, 80},
	}

	step := 0
	for _, ph := range phases {
		for range ph.steps {
			step++
			k := keys[rng.IntN(len(keys))]
			if rng.IntN(100) < ph.putPercent {
				kv := store.KV{
					Key:    k,
					Value:  []byte(fmt.Sprintf("v-%d", step)),
					ModRev: store.Rev(step),
				}
				tr = tr.Put(kv)
				ref[k] = kv
				got, ok := tr.Get(k)
				if !ok || !kvEqual(got, kv) {
					t.Fatalf("step %d: Get(%q) after Put = (%+v, %v), want %+v", step, k, got, ok, kv)
				}
			} else {
				prevRoot := tr.root
				var deleted bool
				tr, deleted = tr.Delete(k)
				_, existed := ref[k]
				if deleted != existed {
					t.Fatalf("step %d: Delete(%q) = %v, want %v", step, k, deleted, existed)
				}
				if !existed && tr.root != prevRoot {
					t.Fatalf("step %d: Delete(%q) of absent key changed root pointer", step, k)
				}
				delete(ref, k)
				if _, ok := tr.Get(k); ok {
					t.Fatalf("step %d: key %q still present after Delete", step, k)
				}
			}

			if tr.Len() != len(ref) {
				t.Fatalf("step %d: Len() = %d, want %d", step, tr.Len(), len(ref))
			}
			if step%100 == 0 {
				if _, err := tr.checkInvariants(); err != nil {
					t.Fatalf("step %d: invariant failure: %v", step, err)
				}
				assertTreeEqualsMap(t, tr, ref)
			}
		}
	}

	// Drain all remaining keys in random order to verify shrinking down to empty.
	remaining := make([]string, 0, len(ref))
	for k := range ref {
		remaining = append(remaining, k)
	}
	rng.Shuffle(len(remaining), func(i, j int) {
		remaining[i], remaining[j] = remaining[j], remaining[i]
	})
	for _, k := range remaining {
		var ok bool
		tr, ok = tr.Delete(k)
		if !ok {
			t.Fatalf("drain Delete(%q) returned false", k)
		}
		delete(ref, k)
		if _, err := tr.checkInvariants(); err != nil {
			t.Fatalf("drain invariant failure after deleting %q: %v", k, err)
		}
	}
	if tr.Len() != 0 || tr.root != nil {
		t.Fatalf("after drain: Len=%d root=%v, want empty", tr.Len(), tr.root)
	}
}

func TestPropertyLargeTreeDepthAndMutations(t *testing.T) {
	rng := rand.New(rand.NewPCG(2026, 7))
	tr := Tree{}
	ref := make(map[string]store.KV)

	const initialKeys = 25000
	for i := range initialKeys {
		k := fmt.Sprintf("item/%08x/%04d", rng.Uint32(), i)
		kv := store.KV{Key: k, Value: []byte{byte(i)}, ModRev: store.Rev(i + 1)}
		tr = tr.Put(kv)
		ref[k] = kv
	}

	depth, err := tr.checkInvariants()
	if err != nil {
		t.Fatalf("invariant failure on large tree: %v", err)
	}
	if depth < 3 {
		t.Fatalf("tree depth = %d, want >= 3", depth)
	}
	assertTreeEqualsMap(t, tr, ref)

	// Collect a slice of keys for targeted updates and deletes.
	allKeys := make([]string, 0, len(ref))
	for k := range ref {
		allKeys = append(allKeys, k)
	}

	for step := range 5000 {
		if rng.IntN(2) == 0 {
			var k string
			if rng.IntN(2) == 0 && len(allKeys) > 0 {
				k = allKeys[rng.IntN(len(allKeys))]
			} else {
				k = fmt.Sprintf("item/%08x/new-%d", rng.Uint32(), step)
				allKeys = append(allKeys, k)
			}
			kv := store.KV{Key: k, Value: []byte{byte(step)}, ModRev: store.Rev(initialKeys + step + 1)}
			tr = tr.Put(kv)
			ref[k] = kv
		} else if len(allKeys) > 0 {
			k := allKeys[rng.IntN(len(allKeys))]
			var deleted bool
			tr, deleted = tr.Delete(k)
			_, existed := ref[k]
			if deleted != existed {
				t.Fatalf("step %d: Delete(%q) = %v, want %v", step, k, deleted, existed)
			}
			delete(ref, k)
		}
		if tr.Len() != len(ref) {
			t.Fatalf("step %d: Len() = %d, want %d", step, tr.Len(), len(ref))
		}
	}

	if _, err := tr.checkInvariants(); err != nil {
		t.Fatalf("invariant failure after mutations: %v", err)
	}
	assertTreeEqualsMap(t, tr, ref)
}

func TestPersistenceSnapshots(t *testing.T) {
	rng := rand.New(rand.NewPCG(1337, 7331))
	type snapshot struct {
		tr  Tree
		ref map[string]store.KV
	}

	const keySpace = 300
	keys := make([]string, keySpace)
	for i := range keys {
		keys[i] = fmt.Sprintf("s/%03d", i)
	}

	var snapshots []snapshot
	tr := Tree{}
	ref := make(map[string]store.KV)

	// Record initial empty snapshot too.
	snapshots = append(snapshots, snapshot{tr: tr, ref: cloneRefMap(ref)})

	for step := 1; step <= 4000; step++ {
		k := keys[rng.IntN(len(keys))]
		if rng.IntN(100) < 60 {
			kv := store.KV{
				Key:    k,
				Value:  []byte(fmt.Sprintf("val-%d", step)),
				ModRev: store.Rev(step),
			}
			tr = tr.Put(kv)
			ref[k] = kv
		} else {
			tr, _ = tr.Delete(k)
			delete(ref, k)
		}

		if step%50 == 0 {
			snapshots = append(snapshots, snapshot{
				tr:  tr,
				ref: cloneRefMap(ref),
			})
		}
	}

	for i, snap := range snapshots {
		if _, err := snap.tr.checkInvariants(); err != nil {
			t.Fatalf("snapshot %d invariant failure: %v", i, err)
		}
		assertTreeEqualsMap(t, snap.tr, snap.ref)
	}
}

func TestAscendEdgeCases(t *testing.T) {
	collect := func(tr Tree, prefix, after string, limit int) []string {
		var out []string
		tr.Ascend(prefix, after, func(kv store.KV) bool {
			out = append(out, kv.Key)
			return limit <= 0 || len(out) < limit
		})
		return out
	}

	// Empty tree.
	var empty Tree
	if got := collect(empty, "", "", 0); len(got) != 0 {
		t.Fatalf("empty tree Ascend = %v, want nil", got)
	}
	if got := collect(empty, "a", "a", 0); len(got) != 0 {
		t.Fatalf("empty tree Ascend with prefix/after = %v, want nil", got)
	}

	keys := []string{
		"\x00",
		"\x00\xff",
		"a",
		"a\x00",
		"a\x00x",
		"ab",
		"a\xfe",
		"a\xff",
		"a\xff\x00",
		"a\xff\xff",
		"b",
		"b\x00",
		"\xff",
		"\xff\xff",
	}
	tr := Tree{}
	for i, k := range keys {
		tr = tr.Put(store.KV{Key: k, ModRev: store.Rev(i + 1)})
	}

	tests := []struct {
		name   string
		prefix string
		after  string
		limit  int
		want   []string
	}{
		{
			name:   "all keys",
			prefix: "",
			after:  "",
			want:   keys,
		},
		{
			name:   "prefix empty with after",
			prefix: "",
			after:  "a\xff",
			want:   []string{"a\xff\x00", "a\xff\xff", "b", "b\x00", "\xff", "\xff\xff"},
		},
		{
			name:   "prefix a",
			prefix: "a",
			after:  "",
			want:   []string{"a", "a\x00", "a\x00x", "ab", "a\xfe", "a\xff", "a\xff\x00", "a\xff\xff"},
		},
		{
			name:   "prefix a with after equal to prefix",
			prefix: "a",
			after:  "a",
			want:   []string{"a\x00", "a\x00x", "ab", "a\xfe", "a\xff", "a\xff\x00", "a\xff\xff"},
		},
		{
			name:   "prefix a with after absent between keys",
			prefix: "a",
			after:  "a\x00a",
			want:   []string{"a\x00x", "ab", "a\xfe", "a\xff", "a\xff\x00", "a\xff\xff"},
		},
		{
			name:   "after strictly before prefix",
			prefix: "a\xff",
			after:  "ab",
			want:   []string{"a\xff", "a\xff\x00", "a\xff\xff"},
		},
		{
			name:   "after outside and past prefix",
			prefix: "a\xff",
			after:  "b",
			want:   nil,
		},
		{
			name:   "prefix ending in 0xff",
			prefix: "a\xff",
			after:  "",
			want:   []string{"a\xff", "a\xff\x00", "a\xff\xff"},
		},
		{
			name:   "prefix ending in 0xff with after",
			prefix: "a\xff",
			after:  "a\xff",
			want:   []string{"a\xff\x00", "a\xff\xff"},
		},
		{
			name:   "prefix ending in 0x00",
			prefix: "a\x00",
			after:  "",
			want:   []string{"a\x00", "a\x00x"},
		},
		{
			name:   "prefix 0xff",
			prefix: "\xff",
			after:  "",
			want:   []string{"\xff", "\xff\xff"},
		},
		{
			name:   "early stop",
			prefix: "a",
			after:  "",
			limit:  3,
			want:   []string{"a", "a\x00", "a\x00x"},
		},
		{
			name:   "no matching prefix",
			prefix: "c",
			after:  "",
			want:   nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := collect(tr, tc.prefix, tc.after, tc.limit)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("Ascend(%q, %q) = %q, want %q", tc.prefix, tc.after, got, tc.want)
			}
		})
	}
}

func TestAscendLargeTreeMultiLevelQueries(t *testing.T) {
	rng := rand.New(rand.NewPCG(77, 88))
	tr := Tree{}
	var sorted []string
	const total = 15000
	for i := range total {
		k := fmt.Sprintf("p%02d/k%06d", i%30, i)
		sorted = append(sorted, k)
		tr = tr.Put(store.KV{Key: k, ModRev: store.Rev(i + 1)})
	}
	slices.Sort(sorted)

	depth, err := tr.checkInvariants()
	if err != nil {
		t.Fatalf("invariant failure: %v", err)
	}
	if depth < 3 {
		t.Fatalf("depth = %d, want >= 3", depth)
	}

	prefixes := []string{"", "p00/", "p05/k00", "p29/", "p99/", "p15/k010"}
	afters := []string{"", "p00/", "p05/k005000", "p15/k010000", "p29/k014000", "p99/", "a", "z"}

	for _, prefix := range prefixes {
		for _, after := range afters {
			var want []string
			for _, k := range sorted {
				if strings.HasPrefix(k, prefix) && (after == "" || k > after) {
					want = append(want, k)
				}
			}
			limit := 0
			if rng.IntN(3) == 0 && len(want) > 5 {
				limit = rng.IntN(len(want)) + 1
				want = want[:limit]
			}
			var got []string
			tr.Ascend(prefix, after, func(kv store.KV) bool {
				got = append(got, kv.Key)
				return limit == 0 || len(got) < limit
			})
			if !slices.Equal(got, want) {
				t.Fatalf("Ascend(%q, %q, limit=%d): len(got)=%d len(want)=%d", prefix, after, limit, len(got), len(want))
			}
		}
	}
}

func TestConcurrentReadersAndWriter(t *testing.T) {
	type versionedSnapshot struct {
		tr   Tree
		keys map[string]store.KV
	}

	initial := Tree{}
	initMap := make(map[string]store.KV)
	for i := range 500 {
		k := fmt.Sprintf("c/%03d", i)
		kv := store.KV{Key: k, Value: []byte{byte(i)}, ModRev: 1}
		initial = initial.Put(kv)
		initMap[k] = kv
	}

	var current atomic.Pointer[versionedSnapshot]
	current.Store(&versionedSnapshot{tr: initial, keys: initMap})

	var stop atomic.Bool
	var wg sync.WaitGroup
	errCh := make(chan error, 16)

	// Start 6 concurrent readers reading both the initial snapshot and latest published snapshots.
	for r := range 6 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(id+1), 12345))
			for !stop.Load() {
				snap := current.Load()
				if snap.tr.Len() != len(snap.keys) {
					errCh <- fmt.Errorf("reader %d: Len=%d want %d", id, snap.tr.Len(), len(snap.keys))
					return
				}
				k := fmt.Sprintf("c/%03d", rng.IntN(600))
				got, ok := snap.tr.Get(k)
				want, wantOK := snap.keys[k]
				if ok != wantOK || (ok && !kvEqual(got, want)) {
					errCh <- fmt.Errorf("reader %d: Get(%q)=(%+v,%v) want (%+v,%v)", id, k, got, ok, want, wantOK)
					return
				}
				prefix := fmt.Sprintf("c/%01d", rng.IntN(6))
				var count int
				var prev string
				snap.tr.Ascend(prefix, "", func(kv store.KV) bool {
					if !strings.HasPrefix(kv.Key, prefix) {
						errCh <- fmt.Errorf("reader %d: Ascend(%q) yielded %q", id, prefix, kv.Key)
						return false
					}
					if prev != "" && kv.Key <= prev {
						errCh <- fmt.Errorf("reader %d: out of order %q after %q", id, kv.Key, prev)
						return false
					}
					prev = kv.Key
					count++
					return count < 20
				})

				// Also verify the initial snapshot stays untouched.
				if initial.Len() != 500 {
					errCh <- fmt.Errorf("reader %d: initial snapshot Len=%d want 500", id, initial.Len())
					return
				}
			}
		}(r)
	}

	// Single writer derives new trees while readers run.
	rng := rand.New(rand.NewPCG(999, 888))
	tr := initial
	ref := cloneRefMap(initMap)
	for step := 2; step <= 3000; step++ {
		k := fmt.Sprintf("c/%03d", rng.IntN(600))
		if rng.IntN(100) < 60 {
			kv := store.KV{Key: k, Value: []byte{byte(step)}, ModRev: store.Rev(step)}
			tr = tr.Put(kv)
			ref[k] = kv
		} else {
			tr, _ = tr.Delete(k)
			delete(ref, k)
		}
		if step%10 == 0 {
			current.Store(&versionedSnapshot{
				tr:   tr,
				keys: cloneRefMap(ref),
			})
		}
	}

	stop.Store(true)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}
	assertTreeEqualsMap(t, initial, initMap)
	assertTreeEqualsMap(t, tr, ref)
}
