package window

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
)

func kvSlicesEqual(a, b []store.KV) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].ModRev != b[i].ModRev || !bytes.Equal(a[i].Value, b[i].Value) {
			return false
		}
	}
	return true
}

func eventSlicesEqual(a, b []store.Event) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Rev != b[i].Rev || a[i].Key != b[i].Key || !bytes.Equal(a[i].Value, b[i].Value) {
			return false
		}
		if a[i].Type == store.EventDelete && (a[i].Value != nil || b[i].Value != nil) {
			return false
		}
	}
	return true
}

func TestRandomizedDifferential(t *testing.T) {
	const numScripts = 4000
	const stepsPerScript = 100

	keys := []string{"a", "aa", "ab", "ac", "b", "ba", "c", "d"}
	prefixes := []string{"", "a", "ab", "b", "z"}
	conts := []string{"", "a", "aa", "ab", "b", "z"}

	for seed := uint64(1); seed <= numScripts; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))

		maxEv := rng.IntN(12) + 1
		if rng.IntN(10) == 0 {
			maxEv = 0
		}
		cfg := Config{
			MinEvents: rng.IntN(7),
			MinAge:    time.Duration(rng.IntN(51)) * time.Millisecond,
			MaxEvents: maxEv,
		}
		listRev := store.Rev(rng.IntN(6))

		var initKVs []store.KV
		for _, k := range keys {
			if rng.IntN(3) == 0 {
				var modRev store.Rev
				if listRev > 0 {
					modRev = store.Rev(rng.IntN(int(listRev)) + 1)
				}
				initKVs = append(initKVs, store.KV{
					Key:    k,
					Value:  []byte(fmt.Sprintf("init-%d", rng.IntN(100))),
					ModRev: modRev,
				})
			}
		}
		rng.Shuffle(len(initKVs), func(i, j int) { initKVs[i], initKVs[j] = initKVs[j], initKVs[i] })

		w := New(cfg, listRev, initKVs)
		ref := newRefCache(cfg, listRev, initKVs)

		type watchPair struct {
			c *Cursor
			r *refWatcher
		}
		var watches []watchPair
		now := time.Unix(1_700_000_000, 0)
		var script []string
		script = append(script, fmt.Sprintf("init(cfg=%+v, listRev=%d, kvs=%v)", cfg, listRev, initKVs))

		fail := func(step int, msg string) {
			t.Fatalf("seed=%d step=%d: %s\nscript:\n%s", seed, step, msg, strings.Join(script, "\n"))
		}

		checkState := func(step int) {
			if w.Rev() != ref.rev {
				fail(step, fmt.Sprintf("Rev mismatch: got %d, want %d", w.Rev(), ref.rev))
			}
			if w.Open() != ref.isOpen {
				fail(step, fmt.Sprintf("Open mismatch: got %v, want %v", w.Open(), ref.isOpen))
			}
			if w.Floor() != ref.floor {
				fail(step, fmt.Sprintf("Floor mismatch: got %d, want %d", w.Floor(), ref.floor))
			}
			if w.Retained() != ref.retainedLen() {
				fail(step, fmt.Sprintf("Retained mismatch: got %d, want %d", w.Retained(), ref.retainedLen()))
			}
		}
		checkState(-1)

		pickRev := func() store.Rev {
			switch rng.IntN(7) {
			case 0:
				return 0
			case 1:
				return -1
			case 2:
				return max(0, ref.floor-1)
			case 3:
				return ref.floor
			case 4:
				if ref.rev > ref.floor {
					return ref.floor + store.Rev(rng.IntN(int(ref.rev-ref.floor+1)))
				}
				return ref.rev
			case 5:
				return ref.rev + 1
			default:
				return ref.rev + 2
			}
		}

		for step := range stepsPerScript {
			// Advance time randomly: forward, zero, or occasionally backwards.
			switch rng.IntN(10) {
			case 0, 1:
				// unchanged
			case 2:
				now = now.Add(-time.Duration(rng.IntN(15)+1) * time.Millisecond)
			default:
				now = now.Add(time.Duration(rng.IntN(30)+1) * time.Millisecond)
			}

			op := rng.IntN(8)
			switch op {
			case 0, 1: // Event
				var evRev store.Rev
				switch rng.IntN(6) {
				case 0:
					if ref.isOpen {
						evRev = ref.rev + 1
					} else {
						evRev = ref.rev + 1
					}
				case 1:
					evRev = ref.rev + store.Rev(rng.IntN(4)+1)
				case 2:
					evRev = ref.rev // contract violation: at closed rev
				case 3:
					evRev = max(0, ref.rev-1) // contract violation: below closed rev
				default:
					evRev = ref.rev + 2
				}
				evType := store.EventPut
				switch rng.IntN(5) {
				case 0:
					evType = store.EventDelete
				case 1:
					if rng.IntN(4) == 0 {
						evType = store.EventProgress
					}
				case 2:
					if rng.IntN(8) == 0 {
						evType = 0
					}
				}
				key := keys[rng.IntN(len(keys))]
				var val []byte
				if evType == store.EventPut {
					val = []byte(fmt.Sprintf("v-%d-%d", step, rng.IntN(100)))
				}
				ev := store.Event{Type: evType, Rev: evRev, Key: key, Value: val}
				script = append(script, fmt.Sprintf("Event(ev=%+v, now=%d)", ev, now.UnixMilli()))
				gotErr := w.Event(ev, now)
				wantErr := ref.feedEvent(ev, now)
				if !errors.Is(gotErr, wantErr) {
					fail(step, fmt.Sprintf("Event err mismatch: got %v, want %v", gotErr, wantErr))
				}

			case 2: // Progress
				var p store.Rev
				switch rng.IntN(5) {
				case 0:
					p = max(0, ref.rev-1)
				case 1:
					p = ref.rev
				case 2:
					p = ref.rev + 1
				default:
					p = ref.rev + store.Rev(rng.IntN(4))
				}
				script = append(script, fmt.Sprintf("Progress(%d)", p))
				gotErr := w.Progress(p)
				wantErr := ref.feedProgress(p)
				if !errors.Is(gotErr, wantErr) {
					fail(step, fmt.Sprintf("Progress err mismatch: got %v, want %v", gotErr, wantErr))
				}

			case 3: // Get
				key := keys[rng.IntN(len(keys))]
				if rng.IntN(10) == 0 {
					key = ""
				}
				atRev := pickRev()
				script = append(script, fmt.Sprintf("Get(key=%q, atRev=%d)", key, atRev))
				gotKV, gotRev, gotErr := w.Get(key, atRev)
				wantKV, wantRev, wantErr := ref.get(key, atRev)
				if !errors.Is(gotErr, wantErr) || gotRev != wantRev ||
					gotKV.Key != wantKV.Key || gotKV.ModRev != wantKV.ModRev || !bytes.Equal(gotKV.Value, wantKV.Value) {
					fail(step, fmt.Sprintf("Get mismatch: got (%+v, %d, %v), want (%+v, %d, %v)",
						gotKV, gotRev, gotErr, wantKV, wantRev, wantErr))
				}

			case 4: // Range
				prefix := prefixes[rng.IntN(len(prefixes))]
				atRev := pickRev()
				limit := []int{-1, 0, 1, 2, 3, 10}[rng.IntN(6)]
				cont := conts[rng.IntN(len(conts))]
				script = append(script, fmt.Sprintf("Range(prefix=%q, atRev=%d, limit=%d, cont=%q)", prefix, atRev, limit, cont))
				gotKVs, gotRev, gotCont, gotErr := w.Range(prefix, atRev, limit, cont)
				wantKVs, wantRev, wantCont, wantErr := ref.rangeAt(prefix, atRev, limit, cont)
				if !errors.Is(gotErr, wantErr) || gotRev != wantRev || gotCont != wantCont || !kvSlicesEqual(gotKVs, wantKVs) {
					fail(step, fmt.Sprintf("Range mismatch: got (%v, %d, %q, %v), want (%v, %d, %q, %v)",
						gotKVs, gotRev, gotCont, gotErr, wantKVs, wantRev, wantCont, wantErr))
				}

			case 5: // Watch / WatchList
				prefix := prefixes[rng.IntN(len(prefixes))]
				rev := pickRev()
				if rng.IntN(2) == 0 {
					script = append(script, fmt.Sprintf("Watch(prefix=%q, fromRev=%d)", prefix, rev))
					gotC, gotErr := w.Watch(prefix, rev)
					wantW, wantErr := ref.watch(prefix, rev)
					if !errors.Is(gotErr, wantErr) || (gotErr == nil) != (gotC != nil) {
						fail(step, fmt.Sprintf("Watch mismatch: got (%v, %v), want (%v, %v)", gotC, gotErr, wantW, wantErr))
					}
					if gotErr == nil {
						watches = append(watches, watchPair{c: gotC, r: wantW})
					}
				} else {
					script = append(script, fmt.Sprintf("WatchList(prefix=%q, minRev=%d)", prefix, rev))
					gotTree, gotRev, gotC, gotErr := w.WatchList(prefix, rev)
					wantKVs, wantRev, wantW, wantErr := ref.watchList(prefix, rev)
					if !errors.Is(gotErr, wantErr) || gotRev != wantRev || (gotErr == nil) != (gotC != nil) {
						fail(step, fmt.Sprintf("WatchList mismatch: got rev=%d err=%v, want rev=%d err=%v", gotRev, gotErr, wantRev, wantErr))
					}
					if gotErr == nil {
						var gotKVs []store.KV
						gotTree.Ascend(prefix, "", func(kv store.KV) bool {
							gotKVs = append(gotKVs, kv)
							return true
						})
						if !kvSlicesEqual(gotKVs, wantKVs) {
							fail(step, fmt.Sprintf("WatchList kvs mismatch: got %v, want %v", gotKVs, wantKVs))
						}
						watches = append(watches, watchPair{c: gotC, r: wantW})
					}
				}

			case 6: // Poll
				if len(watches) == 0 {
					continue
				}
				wi := rng.IntN(len(watches))
				limit := []int{-1, 0, 1, 2, 5, 100}[rng.IntN(6)]
				script = append(script, fmt.Sprintf("Poll(watch=%d, limit=%d)", wi, limit))
				gotEvs, gotErr := w.Poll(watches[wi].c, limit)
				wantEvs, wantErr := ref.poll(watches[wi].r, limit)
				if !errors.Is(gotErr, wantErr) || !eventSlicesEqual(gotEvs, wantEvs) {
					fail(step, fmt.Sprintf("Poll mismatch: got (%v, %v), want (%v, %v)", gotEvs, gotErr, wantEvs, wantErr))
				}

			case 7: // Drained / Bookmark
				if len(watches) == 0 {
					continue
				}
				wi := rng.IntN(len(watches))
				script = append(script, fmt.Sprintf("Drained+Bookmark(watch=%d)", wi))
				gotDrained := w.Drained(watches[wi].c)
				wantDrained := ref.drained(watches[wi].r)
				if gotDrained != wantDrained {
					fail(step, fmt.Sprintf("Drained mismatch: got %v, want %v", gotDrained, wantDrained))
				}
				gotB, gotOK := w.Bookmark(watches[wi].c)
				wantB, wantOK := ref.bookmark(watches[wi].r)
				if gotB != wantB || gotOK != wantOK {
					fail(step, fmt.Sprintf("Bookmark mismatch: got (%d, %v), want (%d, %v)", gotB, gotOK, wantB, wantOK))
				}
			}

			checkState(step)
		}
	}
}

func TestMultiChangeRevisionVisibilityAndClose(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	cfg := Config{MinEvents: 10, MinAge: time.Minute, MaxEvents: 100}

	t.Run("closed by later event", func(t *testing.T) {
		w := New(cfg, 5, []store.KV{{Key: "k0", Value: []byte("v0"), ModRev: 3}})
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 6, Key: "k1", Value: []byte("v1")}, t0); err != nil {
			t.Fatal(err)
		}
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 6, Key: "k2", Value: []byte("v2")}, t0); err != nil {
			t.Fatal(err)
		}
		if w.Rev() != 5 || !w.Open() {
			t.Fatalf("got Rev=%d Open=%v, want 5, true", w.Rev(), w.Open())
		}
		if _, _, err := w.Get("k1", 6); !errors.Is(err, ErrNotYet) {
			t.Fatalf("Get(k1, 6) err = %v, want ErrNotYet", err)
		}
		if _, rev, err := w.Get("k1", 0); !errors.Is(err, store.ErrNotFound) || rev != 5 {
			t.Fatalf("Get(k1, 0) = (%d, %v), want (5, ErrNotFound)", rev, err)
		}
		kvs, rev, cont, err := w.Range("", 0, 0, "")
		if err != nil || rev != 5 || cont != "" || len(kvs) != 1 || kvs[0].Key != "k0" {
			t.Fatalf("Range(0) = (%v, %d, %q, %v)", kvs, rev, cont, err)
		}

		// A change at rev 8 closes rev 6 and advances closed rev to 7.
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 8, Key: "k3", Value: []byte("v3")}, t0.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if w.Rev() != 7 || !w.Open() {
			t.Fatalf("got Rev=%d Open=%v, want 7, true", w.Rev(), w.Open())
		}
		for _, r := range []store.Rev{6, 7, 0} {
			kv, gotRev, err := w.Get("k2", r)
			if err != nil || string(kv.Value) != "v2" || kv.ModRev != 6 {
				t.Fatalf("Get(k2, %d) = (%+v, %d, %v)", r, kv, gotRev, err)
			}
		}
		if _, _, err := w.Get("k3", 7); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("Get(k3, 7) err = %v, want ErrNotFound", err)
		}
		if _, _, err := w.Get("k3", 8); !errors.Is(err, ErrNotYet) {
			t.Fatalf("Get(k3, 8) err = %v, want ErrNotYet", err)
		}
	})

	t.Run("closed by progress", func(t *testing.T) {
		w := New(cfg, 5, nil)
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 6, Key: "a", Value: []byte("1")}, t0); err != nil {
			t.Fatal(err)
		}
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 6, Key: "b", Value: []byte("2")}, t0); err != nil {
			t.Fatal(err)
		}
		if err := w.Progress(6); err != nil {
			t.Fatal(err)
		}
		if w.Rev() != 6 || w.Open() {
			t.Fatalf("got Rev=%d Open=%v, want 6, false", w.Rev(), w.Open())
		}
		kvs, rev, _, err := w.Range("", 0, 0, "")
		if err != nil || rev != 6 || len(kvs) != 2 {
			t.Fatalf("Range(0) = (%v, %d, %v)", kvs, rev, err)
		}
	})
}

func TestRejectedInputsLeaveStateUnchanged(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	w := New(Config{MinEvents: 5, MaxEvents: 10}, 10, []store.KV{{Key: "a", Value: []byte("v"), ModRev: 10}})
	if err := w.Event(store.Event{Type: store.EventPut, Rev: 12, Key: "b", Value: []byte("v2")}, t0); err != nil {
		t.Fatal(err)
	}

	// Open revision is 12, closed is 11.
	cases := []struct {
		name string
		fn   func() error
	}{
		{"progress below open rev", func() error { return w.Progress(11) }},
		{"progress much below open rev", func() error { return w.Progress(5) }},
		{"event at closed rev", func() error { return w.Event(store.Event{Type: store.EventPut, Rev: 11, Key: "c"}, t0) }},
		{"event below closed rev", func() error { return w.Event(store.Event{Type: store.EventDelete, Rev: 9, Key: "a"}, t0) }},
		{"event rev 0", func() error { return w.Event(store.Event{Type: store.EventPut, Rev: 0, Key: "c"}, t0) }},
		{"event invalid type progress", func() error { return w.Event(store.Event{Type: store.EventProgress, Rev: 13}, t0) }},
		{"event invalid type zero", func() error { return w.Event(store.Event{Type: 0, Rev: 13, Key: "c"}, t0) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); !errors.Is(err, ErrOutOfOrder) {
				t.Fatalf("err = %v, want ErrOutOfOrder", err)
			}
			if w.Rev() != 11 || !w.Open() || w.Floor() != 10 || w.Retained() != 1 {
				t.Fatalf("state mutated: Rev=%d Open=%v Floor=%d Retained=%d", w.Rev(), w.Open(), w.Floor(), w.Retained())
			}
		})
	}
}

func TestEvictionRules(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)

	t.Run("never splits revision and never drops open revision", func(t *testing.T) {
		w := New(Config{MinEvents: 0, MinAge: 0, MaxEvents: 2}, 1, nil)
		for i := range 4 {
			if err := w.Event(store.Event{Type: store.EventPut, Rev: 2, Key: fmt.Sprintf("k%d", i), Value: []byte("v")}, t0); err != nil {
				t.Fatal(err)
			}
		}
		if w.Retained() != 4 || w.Floor() != 1 {
			t.Fatalf("open revision should not be evicted: Retained=%d Floor=%d", w.Retained(), w.Floor())
		}
		// Rev 3 has 1 event, so rev 2 (4 events) closes and is evicted as a whole.
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 3, Key: "k4", Value: []byte("v")}, t0); err != nil {
			t.Fatal(err)
		}
		if w.Retained() != 1 || w.Floor() != 2 {
			t.Fatalf("got Retained=%d Floor=%d, want 1, 2", w.Retained(), w.Floor())
		}
	})

	t.Run("max events evicts young revisions", func(t *testing.T) {
		w := New(Config{MinEvents: 10, MinAge: time.Hour, MaxEvents: 3}, 1, nil)
		for r := store.Rev(2); r <= 6; r++ {
			if err := w.Event(store.Event{Type: store.EventPut, Rev: r, Key: "k", Value: []byte("v")}, t0); err != nil {
				t.Fatal(err)
			}
		}
		if w.Retained() != 3 || w.Floor() != 3 {
			t.Fatalf("got Retained=%d Floor=%d, want 3, 3", w.Retained(), w.Floor())
		}
	})

	t.Run("min count and min age keeps whichever is more", func(t *testing.T) {
		// MinEvents=3, MinAge=10ms, MaxEvents=100
		w := New(Config{MinEvents: 3, MinAge: 10 * time.Millisecond, MaxEvents: 100}, 1, nil)
		// Rev 2 at t0, Rev 3 at t0+20ms, Rev 4 at t0+20ms, Rev 5 at t0+20ms -> 4 retained, rev 2 is 20ms old > 10ms and 3 remain -> rev 2 evicted.
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 2, Key: "k2", Value: []byte("2")}, t0); err != nil {
			t.Fatal(err)
		}
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 3, Key: "k3", Value: []byte("3")}, t0.Add(20*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 4, Key: "k4", Value: []byte("4")}, t0.Add(20*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		// Still only 3 retained (2,3,4), evicting rev 2 would leave 2 < MinEvents(3), so rev 2 stays even though age=20ms > 10ms.
		if w.Retained() != 3 || w.Floor() != 1 {
			t.Fatalf("MinEvents should keep rev 2: Retained=%d Floor=%d", w.Retained(), w.Floor())
		}
		// Adding rev 5 makes 4 retained: rev 2 can be evicted leaving 3 >= MinEvents(3). Rev 3 has age 0 <= 10ms so stays.
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 5, Key: "k5", Value: []byte("5")}, t0.Add(20*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if w.Retained() != 3 || w.Floor() != 2 {
			t.Fatalf("expected rev 2 evicted: Retained=%d Floor=%d", w.Retained(), w.Floor())
		}
		// At t0+35ms, rev 3 has age 15ms > 10ms, but only 3 retained -> MinEvents keeps rev 3.
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 6, Key: "k6", Value: []byte("6")}, t0.Add(35*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		// Now 4 retained: rev 3 has age 15ms > 10ms and leaves 3 -> evicted; rev 4 has age 15ms > 10ms but leaves 2 < 3 -> kept.
		if w.Retained() != 3 || w.Floor() != 3 {
			t.Fatalf("expected rev 3 evicted: Retained=%d Floor=%d", w.Retained(), w.Floor())
		}
	})

	t.Run("negative age counts as zero and group age uses last entry", func(t *testing.T) {
		w := New(Config{MinEvents: 0, MinAge: 0, MaxEvents: 100}, 1, nil)
		// Rev 2 has two entries: t0+100ms then t0+50ms. Last entry time is t0+50ms.
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 2, Key: "a", Value: []byte("1")}, t0.Add(100*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 2, Key: "b", Value: []byte("2")}, t0.Add(50*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		// Rev 3 arrives at t0+40ms (earlier than t0+50ms -> negative age -> 0, not > MinAge 0).
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 3, Key: "c", Value: []byte("3")}, t0.Add(40*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if w.Floor() != 1 || w.Retained() != 3 {
			t.Fatalf("negative age must not evict: Floor=%d Retained=%d", w.Floor(), w.Retained())
		}
		// Rev 4 arrives at t0+51ms: rev 2 age is 1ms > 0 and rev 3 age is 11ms > 0 -> both evicted.
		if err := w.Event(store.Event{Type: store.EventPut, Rev: 4, Key: "d", Value: []byte("4")}, t0.Add(51*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if w.Floor() != 3 || w.Retained() != 1 {
			t.Fatalf("expected rev 2 and 3 evicted at t0+51ms: Floor=%d Retained=%d", w.Floor(), w.Retained())
		}
	})
}

func TestExactRevisionReadsAndRangePaging(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	w := New(Config{MinEvents: 2, MinAge: time.Minute, MaxEvents: 50}, 10, []store.KV{
		{Key: "a1", Value: []byte("0"), ModRev: 5},
		{Key: "a2", Value: []byte("0"), ModRev: 8},
		{Key: "b1", Value: []byte("0"), ModRev: 9},
	})
	if err := w.Event(store.Event{Type: store.EventPut, Rev: 12, Key: "a3", Value: []byte("12")}, t0); err != nil {
		t.Fatal(err)
	}
	if err := w.Event(store.Event{Type: store.EventPut, Rev: 15, Key: "a1", Value: []byte("15")}, t0); err != nil {
		t.Fatal(err)
	}
	if err := w.Progress(16); err != nil {
		t.Fatal(err)
	}

	// Read between snapshots: rev 11 (before 12), rev 13 (between 12 and 15), rev 16 (after 15).
	kv, rev, err := w.Get("a1", 13)
	if err != nil || rev != 13 || string(kv.Value) != "0" || kv.ModRev != 5 {
		t.Fatalf("Get(a1, 13) = (%+v, %d, %v)", kv, rev, err)
	}
	kv, rev, err = w.Get("a3", 11)
	if !errors.Is(err, store.ErrNotFound) || rev != 11 {
		t.Fatalf("Get(a3, 11) = (%+v, %d, %v)", kv, rev, err)
	}

	// Range paging at rev 13 while new mutations happen at rev 17, 18.
	page1, r1, cont1, err := w.Range("a", 13, 2, "")
	if err != nil || r1 != 13 || cont1 != "a2" || len(page1) != 2 {
		t.Fatalf("page1 = (%v, %d, %q, %v)", page1, r1, cont1, err)
	}

	// Mutate keys while paging.
	if err := w.Event(store.Event{Type: store.EventDelete, Rev: 17, Key: "a3"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := w.Event(store.Event{Type: store.EventPut, Rev: 18, Key: "a4", Value: []byte("18")}, t0); err != nil {
		t.Fatal(err)
	}
	if err := w.Progress(18); err != nil {
		t.Fatal(err)
	}

	page2, r2, cont2, err := w.Range("a", r1, 2, cont1)
	if err != nil || r2 != 13 || cont2 != "" || len(page2) != 1 || page2[0].Key != "a3" || page2[0].ModRev != 12 {
		t.Fatalf("page2 = (%v, %d, %q, %v)", page2, r2, cont2, err)
	}
}

func TestCursorTooOldAndBookmarkAndWatchList(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	w := New(Config{MinEvents: 0, MinAge: 0, MaxEvents: 2}, 1, nil)

	c, err := w.Watch("k", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !w.Drained(c) {
		t.Fatal("expected cursor to be initially drained")
	}
	if b, ok := w.Bookmark(c); !ok || b != 1 {
		t.Fatalf("initial Bookmark = (%d, %v), want (1, true)", b, ok)
	}

	// Feed rev 2 (open): drained is false, bookmark is false.
	if err := w.Event(store.Event{Type: store.EventPut, Rev: 2, Key: "k1", Value: []byte("1")}, t0); err != nil {
		t.Fatal(err)
	}
	if w.Drained(c) {
		t.Fatal("expected Drained=false before poll")
	}
	if _, ok := w.Bookmark(c); ok {
		t.Fatal("expected Bookmark=false when undrained and open")
	}
	evs, err := w.Poll(c, 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Poll = (%v, %v)", evs, err)
	}
	if !w.Drained(c) {
		t.Fatal("expected Drained=true after poll")
	}
	// Still open at rev 2, so Bookmark must still be false.
	if _, ok := w.Bookmark(c); ok {
		t.Fatal("expected Bookmark=false while revision is open")
	}
	if err := w.Progress(3); err != nil {
		t.Fatal(err)
	}
	b1, ok := w.Bookmark(c)
	if !ok || b1 != 3 {
		t.Fatalf("Bookmark = (%d, %v), want (3, true)", b1, ok)
	}

	// WatchList snapshot stays intact across later changes and evictions.
	tree, wlRev, wlCursor, err := w.WatchList("k", 3)
	if err != nil || wlRev != 3 {
		t.Fatalf("WatchList = (rev=%d, err=%v)", wlRev, err)
	}

	// Lagging cursor overtaken by MaxEvents=2 eviction.
	lag, err := w.Watch("k", 2)
	if err != nil {
		t.Fatal(err)
	}
	for r := store.Rev(4); r <= 7; r++ {
		if err := w.Event(store.Event{Type: store.EventPut, Rev: r, Key: fmt.Sprintf("k%d", r), Value: []byte("v")}, t0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Poll(lag, 0); !errors.Is(err, ErrTooOld) {
		t.Fatalf("Poll(lag) err = %v, want ErrTooOld", err)
	}
	if _, err := w.Poll(lag, 1); !errors.Is(err, ErrTooOld) {
		t.Fatalf("second Poll(lag) err = %v, want ErrTooOld", err)
	}
	if w.Drained(lag) {
		t.Fatal("overtaken cursor must not be drained")
	}
	if _, ok := w.Bookmark(lag); ok {
		t.Fatal("overtaken cursor must not produce bookmark")
	}

	// WatchList tree still has only k1 at ModRev 2.
	var snapKVs []store.KV
	tree.Ascend("k", "", func(kv store.KV) bool {
		snapKVs = append(snapKVs, kv)
		return true
	})
	if len(snapKVs) != 1 || snapKVs[0].Key != "k1" || snapKVs[0].ModRev != 2 {
		t.Fatalf("WatchList snapshot mutated: %+v", snapKVs)
	}

	// Poll wlCursor and check Bookmark monotonicity after Progress.
	if err := w.Progress(7); err != nil {
		t.Fatal(err)
	}
	if b2, ok := w.Bookmark(wlCursor); ok {
		t.Fatalf("unexpected bookmark %d before draining", b2)
	}
	// wlCursor started at rev 3, which is still retained? Let's check: MaxEvents=2 retained rev 6 and 7, Floor=5, rev 4 was evicted!
	// Wait: wlCursor was positioned after rev 3 (at index of rev 4), and rev 4 was evicted, so wlCursor gets ErrTooOld.
	if _, err := w.Poll(wlCursor, 0); !errors.Is(err, ErrTooOld) {
		t.Fatalf("Poll(wlCursor) err = %v, want ErrTooOld", err)
	}

	// Poll the caught-up cursor c (which was at rev 3, wait: c was already at index after rev 3 before rev 4..7 arrived -> also overtaken!).
	if _, err := w.Poll(c, 0); !errors.Is(err, ErrTooOld) {
		t.Fatalf("Poll(c) err = %v, want ErrTooOld", err)
	}

	// Fresh cursor from closed rev 7 + Progress(8) tests Bookmark non-decreasing.
	c2, err := w.Watch("k", 0)
	if err != nil {
		t.Fatal(err)
	}
	b2, ok := w.Bookmark(c2)
	if !ok || b2 < b1 || b2 != 7 {
		t.Fatalf("Bookmark(c2) = (%d, %v), want (7, true)", b2, ok)
	}
	if err := w.Progress(9); err != nil {
		t.Fatal(err)
	}
	b3, ok := w.Bookmark(c2)
	if !ok || b3 != 9 || b3 < b2 {
		t.Fatalf("Bookmark(c2) after progress = (%d, %v), want (9, true)", b3, ok)
	}
}

func TestBoundedMemory(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	w := New(Config{MinEvents: 4, MinAge: 0, MaxEvents: 16}, 1, nil)

	const total = 100_000
	for i := range total {
		rev := store.Rev(i + 2)
		key := fmt.Sprintf("k/%02d", i%32)
		var err error
		if i%5 == 0 {
			err = w.Event(store.Event{Type: store.EventDelete, Rev: rev, Key: key}, t0.Add(time.Duration(i)*time.Millisecond))
		} else {
			err = w.Event(store.Event{Type: store.EventPut, Rev: rev, Key: key, Value: make([]byte, 64)}, t0.Add(time.Duration(i)*time.Millisecond))
		}
		if err != nil {
			t.Fatal(err)
		}
	}

	if w.Retained() > 16 {
		t.Fatalf("Retained = %d, want <= 16", w.Retained())
	}
	if len(w.entries.buf) > 32 {
		t.Fatalf("entries ring buffer cap = %d, want <= 32", len(w.entries.buf))
	}
	if w.snaps.len() > 16 || len(w.snaps.buf) > 32 {
		t.Fatalf("snaps len=%d cap=%d, want bounded <= 16 / <= 32", w.snaps.len(), len(w.snaps.buf))
	}

	checkUnusedCleared := func() {
		t.Helper()
		em := len(w.entries.buf) - 1
		for i := w.entries.count; i < len(w.entries.buf); i++ {
			e := w.entries.buf[(w.entries.head+i)&em]
			if e.ev.Type != 0 || e.ev.Rev != 0 || e.ev.Key != "" || e.ev.Value != nil || !e.at.IsZero() {
				t.Fatalf("uncleared entries slot %d: %+v", i, e)
			}
		}
		sm := len(w.snaps.buf) - 1
		for i := w.snaps.count; i < len(w.snaps.buf); i++ {
			s := w.snaps.buf[(w.snaps.head+i)&sm]
			if s.rev != 0 || s.tree.Len() != 0 || s.count != 0 || !s.last.IsZero() {
				t.Fatalf("uncleared snaps slot %d: %+v", i, s)
			}
		}
	}
	checkUnusedCleared()

	// Spike: large open revision grows the ring, then closing and evicting shrinks it back down.
	for i := range 2000 {
		if err := w.Event(store.Event{Type: store.EventPut, Rev: total + 2, Key: fmt.Sprintf("spike/%d", i), Value: []byte("x")}, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Event(store.Event{Type: store.EventPut, Rev: total + 3, Key: "after", Value: []byte("y")}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(w.entries.buf) > 64 {
		t.Fatalf("entries ring buffer failed to shrink after spike: cap=%d", len(w.entries.buf))
	}
	checkUnusedCleared()

	// Verify snapshots keys match retained closed revisions.
	var snapRevs []store.Rev
	for i := range w.snaps.len() {
		snapRevs = append(snapRevs, w.snaps.at(i).rev)
	}
	if !slices.IsSorted(snapRevs) {
		t.Fatalf("snapshot revisions not sorted: %v", snapRevs)
	}
}

func TestErrorsAndEdgeCases(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	w := New(Config{MinEvents: 0, MinAge: 0, MaxEvents: 1}, 5, []store.KV{
		{Key: "a/1", Value: []byte("v1"), ModRev: 3},
	})
	if err := w.Event(store.Event{Type: store.EventPut, Rev: 6, Key: "a/2", Value: []byte("v2")}, t0); err != nil {
		t.Fatal(err)
	}
	if err := w.Event(store.Event{Type: store.EventPut, Rev: 7, Key: "a/3", Value: []byte("v3")}, t0); err != nil {
		t.Fatal(err)
	}
	if err := w.Progress(8); err != nil {
		t.Fatal(err)
	}
	// Trigger eviction of rev 6 -> Floor becomes 6, Rev=8, Open=false.
	if err := w.Event(store.Event{Type: store.EventPut, Rev: 9, Key: "b/1", Value: []byte("b1")}, t0); err != nil {
		t.Fatal(err)
	}

	// Get: empty key, negative rev, too old, not yet, absent with read rev.
	if _, _, err := w.Get("", 0); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Get empty key err = %v, want ErrInvalidArgument", err)
	}
	if _, _, err := w.Get("a/1", -1); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Get negative rev err = %v, want ErrInvalidArgument", err)
	}
	if _, _, err := w.Get("a/1", 5); !errors.Is(err, ErrTooOld) {
		t.Fatalf("Get rev 5 (< Floor 6) err = %v, want ErrTooOld", err)
	}
	if _, _, err := w.Get("a/1", 9); !errors.Is(err, ErrNotYet) {
		t.Fatalf("Get rev 9 (> Rev 8) err = %v, want ErrNotYet", err)
	}
	if _, rev, err := w.Get("missing", 0); !errors.Is(err, store.ErrNotFound) || rev != 8 {
		t.Fatalf("Get absent = (rev=%d, err=%v), want (8, ErrNotFound)", rev, err)
	}

	// Range: invalid limit, negative atRev, cont not having prefix (takes precedence over tooOld/notYet).
	if _, _, _, err := w.Range("a/", 0, -1, ""); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Range limit=-1 err = %v, want ErrInvalidArgument", err)
	}
	if _, _, _, err := w.Range("a/", -1, 0, ""); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Range atRev=-1 err = %v, want ErrInvalidArgument", err)
	}
	if _, _, _, err := w.Range("a/", 5, 0, "b/1"); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Range invalid cont at tooOld rev err = %v, want ErrInvalidArgument", err)
	}
	if _, _, _, err := w.Range("a/", 5, 0, ""); !errors.Is(err, ErrTooOld) {
		t.Fatalf("Range tooOld err = %v, want ErrTooOld", err)
	}
	if _, _, _, err := w.Range("a/", 9, 0, ""); !errors.Is(err, ErrNotYet) {
		t.Fatalf("Range notYet err = %v, want ErrNotYet", err)
	}

	// Watch / WatchList boundaries.
	if _, err := w.Watch("a/", -1); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Watch negative err = %v, want ErrInvalidArgument", err)
	}
	if _, err := w.Watch("a/", 6); !errors.Is(err, ErrTooOld) {
		t.Fatalf("Watch at Floor err = %v, want ErrTooOld", err)
	}
	if _, err := w.Watch("a/", 10); !errors.Is(err, ErrNotYet) {
		t.Fatalf("Watch at Rev+2 err = %v, want ErrNotYet", err)
	}
	// Watch at Rev+1 (the open revision 9) succeeds and delivers rev 9 event.
	c9, err := w.Watch("b/", 9)
	if err != nil {
		t.Fatalf("Watch at open rev 9 err = %v", err)
	}
	evs, err := w.Poll(c9, 0)
	if err != nil || len(evs) != 1 || evs[0].Rev != 9 {
		t.Fatalf("Poll(c9) = (%+v, %v)", evs, err)
	}

	if _, _, _, err := w.WatchList("a/", -1); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("WatchList negative err = %v, want ErrInvalidArgument", err)
	}
	if _, _, _, err := w.WatchList("a/", 9); !errors.Is(err, ErrNotYet) {
		t.Fatalf("WatchList above closed rev err = %v, want ErrNotYet", err)
	}

	// Poll invalid args.
	if _, err := w.Poll(c9, -1); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Poll negative limit err = %v, want ErrInvalidArgument", err)
	}
	if _, err := w.Poll(nil, 0); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("Poll nil cursor err = %v, want ErrInvalidArgument", err)
	}
}
