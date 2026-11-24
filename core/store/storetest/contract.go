// Package storetest holds the checks every store.Store implementation must pass.
package storetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
)

// Factory returns a store that holds no keys and that nothing else writes to
// while the test runs. Its revision may be anything.
type Factory func(t *testing.T) store.Store

const (
	waitLimit  = 20 * time.Second
	syncRetry  = 20 * time.Millisecond
	quietCheck = 300 * time.Millisecond
)

// Run runs the contract tests against stores made by newStore.
func Run(t *testing.T, newStore Factory) {
	runContract(t, newStore, false)
}

// RunSynctest runs every contract subtest inside a testing/synctest bubble with virtual time.
func RunSynctest(t *testing.T, newStore Factory) {
	runContract(t, newStore, true)
}

func runContract(t *testing.T, newStore Factory, useSynctest bool) {
	tests := []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"GetReturnsWhatTxnWrote", testGetReturnsWhatTxnWrote},
		{"GetAtRevisionReadsHistory", testGetAtRevisionReadsHistory},
		{"GetRejectsFutureRevision", testGetRejectsFutureRevision},
		{"ValuesNormalizeEmptyAndIsolateCallerSlices", testValuesNormalizeEmptyAndIsolateCallerSlices},
		{"TxnAssignsOneRevisionToAllChanges", testTxnAssignsOneRevisionToAllChanges},
		{"TxnWithoutChangesKeepsRevision", testTxnWithoutChangesKeepsRevision},
		{"TxnComparesModRevisions", testTxnComparesModRevisions},
		{"TxnFailedComparisonChangesNothing", testTxnFailedComparisonChangesNothing},
		{"TxnRejectsUndefinedRequests", testTxnRejectsUndefinedRequests},
		{"OversizedRequestsFailWhole", testOversizedRequestsFailWhole},
		{"RangeListsPrefixInKeyOrder", testRangeListsPrefixInKeyOrder},
		{"RangePaginatesOnOneSnapshot", testRangePaginatesOnOneSnapshot},
		{"RangeRejectsUndefinedRequests", testRangeRejectsUndefinedRequests},
		{"BoundaryByteKeysAndPrefixes", testBoundaryByteKeysAndPrefixes},
		{"CurrentRevFollowsWrites", testCurrentRevFollowsWrites},
		{"CompactHidesOlderRevisions", testCompactHidesOlderRevisions},
		{"CompactRejectsRepeatedAndFutureRevisions", testCompactRejectsRepeatedAndFutureRevisions},
		{"WatchDeliversChangesInOrder", testWatchDeliversChangesInOrder},
		{"WatchReplaysFromRevision", testWatchReplaysFromRevision},
		{"WatchFromZeroSeesOnlyLaterChanges", testWatchFromZeroSeesOnlyLaterChanges},
		{"WatchFromFutureRevisionSkipsEarlierChanges", testWatchFromFutureRevisionSkipsEarlierChanges},
		{"WatchFiltersByPrefix", testWatchFiltersByPrefix},
		{"WatchProgressOnQuietPrefix", testWatchProgressOnQuietPrefix},
		{"WatchIsolatesProgressAcrossStreams", testWatchIsolatesProgressAcrossStreams},
		{"WatchBelowCompactionEndsCompacted", testWatchBelowCompactionEndsCompacted},
		{"WatchAtCompactionRevisionSeesItsChanges", testWatchAtCompactionRevisionSeesItsChanges},
		{"WatchOvertakenByCompactionEndsCleanly", testWatchOvertakenByCompactionEndsCleanly},
		{"WatchEndsWithContext", testWatchEndsWithContext},
		{"EndedWatchReportsItsError", testEndedWatchReportsItsError},
		{"ConcurrentWritersWatchersAndCompaction", testConcurrentWritersWatchersAndCompaction},
		{"CallsRejectNegativeRevisions", testCallsRejectNegativeRevisions},
		{"CallsHonorCanceledContext", testCallsHonorCanceledContext},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if useSynctest {
				synctest.Test(t, func(t *testing.T) {
					tc.fn(t, newStore(t))
				})
				return
			}
			tc.fn(t, newStore(t))
		})
	}
}

func put(key, value string) store.Op {
	return store.Op{Type: store.OpPut, Key: key, Value: []byte(value)}
}

func del(key string) store.Op {
	return store.Op{Type: store.OpDelete, Key: key}
}

func ctxFor(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	t.Cleanup(cancel)
	return ctx
}

func currentRev(t *testing.T, s store.Store) store.Rev {
	t.Helper()
	rev, err := s.CurrentRev(ctxFor(t))
	if err != nil {
		t.Fatalf("CurrentRev: %v", err)
	}
	return rev
}

func mustTxn(t *testing.T, s store.Store, cmps []store.Cmp, ops ...store.Op) store.Rev {
	t.Helper()
	ok, rev, err := s.Txn(ctxFor(t), cmps, ops)
	if err != nil {
		t.Fatalf("Txn: %v", err)
	}
	if !ok {
		t.Fatalf("Txn did not apply: cmps=%+v", cmps)
	}
	return rev
}

func mustGet(t *testing.T, s store.Store, key string, atRev store.Rev) (store.KV, store.Rev) {
	t.Helper()
	kv, rev, err := s.Get(ctxFor(t), key, atRev)
	if err != nil {
		t.Fatalf("Get(%q, %d): %v", key, atRev, err)
	}
	return kv, rev
}

func wantAbsent(t *testing.T, s store.Store, key string, atRev, wantRev store.Rev) {
	t.Helper()
	_, rev, err := s.Get(ctxFor(t), key, atRev)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get(%q, %d) error = %v, want ErrNotFound", key, atRev, err)
	}
	if rev != wantRev {
		t.Fatalf("Get(%q, %d) on an absent key returned revision %d, want %d", key, atRev, rev, wantRev)
	}
}

func wantKV(t *testing.T, got store.KV, key, value string, modRev store.Rev) {
	t.Helper()
	if got.Key != key || !bytes.Equal(got.Value, []byte(value)) || got.ModRev != modRev {
		t.Fatalf("got {%q %q mod=%d}, want {%q %q mod=%d}", got.Key, got.Value, got.ModRev, key, value, modRev)
	}
}

func wantErr(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s error = %v, want %v", what, err, want)
	}
}

func mustRange(t *testing.T, s store.Store, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string) {
	t.Helper()
	kvs, rev, next, err := s.Range(ctxFor(t), prefix, atRev, limit, cont)
	if err != nil {
		t.Fatalf("Range(%q, %d, %d, %q): %v", prefix, atRev, limit, cont, err)
	}
	return kvs, rev, next
}

func keysOf(kvs []store.KV) []string {
	keys := make([]string, len(kvs))
	for i, kv := range kvs {
		keys[i] = kv.Key
	}
	return keys
}

func wantKeys(t *testing.T, kvs []store.KV, want ...string) {
	t.Helper()
	got := keysOf(kvs)
	if len(got) != len(want) {
		t.Fatalf("keys = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keys = %q, want %q", got, want)
		}
	}
}

func mustWatch(t *testing.T, s store.Store, prefix string, fromRev store.Rev) store.WatchStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws, err := s.Watch(ctx, prefix, fromRev)
	if err != nil {
		t.Fatalf("Watch(%q, %d): %v", prefix, fromRev, err)
	}
	return ws
}

// Sync returns the changes a watch delivers up to the current store revision,
// and the revision of the progress notification that confirms them. A store
// drops progress requests while the watch is catching up, so Sync repeats the
// request and skips notifications older than the revision it waits for. When
// the watch ends first it returns the stream error.
func Sync(ctx context.Context, s store.Store, ws store.WatchStream) ([]store.Event, store.Rev, error) {
	target, err := s.CurrentRev(ctx)
	if err != nil {
		return nil, 0, err
	}
	retry := time.NewTicker(syncRetry)
	defer retry.Stop()
	requestErr := ws.RequestProgress(ctx)
	var events []store.Event
	for {
		select {
		case ev, open := <-ws.Events():
			if !open {
				if err := ws.Err(); err != nil {
					return events, 0, err
				}
				return events, 0, errors.New("watch ended without an error")
			}
			if ev.Type != store.EventProgress {
				events = append(events, ev)
			} else if ev.Rev >= target {
				return events, ev.Rev, nil
			}
		case <-retry.C:
			requestErr = ws.RequestProgress(ctx)
		case <-ctx.Done():
			if requestErr != nil {
				return events, 0, fmt.Errorf("no progress notification up to revision %d: RequestProgress: %w", target, requestErr)
			}
			return events, 0, fmt.Errorf("no progress notification up to revision %d: %w", target, ctx.Err())
		}
	}
}

func mustSync(t *testing.T, s store.Store, ws store.WatchStream) ([]store.Event, store.Rev) {
	t.Helper()
	events, rev, err := Sync(ctxFor(t), s, ws)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return events, rev
}

func wantEvents(t *testing.T, got []store.Event, want ...store.Event) {
	t.Helper()
	equal := len(got) == len(want)
	for i := 0; equal && i < len(want); i++ {
		g, w := got[i], want[i]
		equal = g.Type == w.Type && g.Rev == w.Rev && g.Key == w.Key && bytes.Equal(g.Value, w.Value)
	}
	if !equal {
		t.Fatalf("events = %s, want %s", formatEvents(got), formatEvents(want))
	}
}

func formatEvents(events []store.Event) string {
	out := "["
	for i, ev := range events {
		if i > 0 {
			out += " "
		}
		switch ev.Type {
		case store.EventPut:
			out += fmt.Sprintf("put(%q=%q@%d)", ev.Key, ev.Value, ev.Rev)
		case store.EventDelete:
			out += fmt.Sprintf("delete(%q@%d)", ev.Key, ev.Rev)
		case store.EventProgress:
			out += fmt.Sprintf("progress(@%d)", ev.Rev)
		default:
			out += fmt.Sprintf("unknown(%d)", ev.Type)
		}
	}
	return out + "]"
}

func putEvent(key, value string, rev store.Rev) store.Event {
	return store.Event{Type: store.EventPut, Rev: rev, Key: key, Value: []byte(value)}
}

func deleteEvent(key string, rev store.Rev) store.Event {
	return store.Event{Type: store.EventDelete, Rev: rev, Key: key}
}

func testGetReturnsWhatTxnWrote(t *testing.T, s store.Store) {
	base := currentRev(t, s)
	wantAbsent(t, s, "a", 0, base)

	rev := mustTxn(t, s, nil, put("a", "1"))
	if rev != base+1 {
		t.Fatalf("first write returned revision %d, want %d", rev, base+1)
	}
	kv, readRev := mustGet(t, s, "a", 0)
	wantKV(t, kv, "a", "1", rev)
	if readRev != rev {
		t.Fatalf("Get returned revision %d, want %d", readRev, rev)
	}

	rev2 := mustTxn(t, s, nil, put("a", ""))
	kv, _ = mustGet(t, s, "a", 0)
	wantKV(t, kv, "a", "", rev2)

	rev3 := mustTxn(t, s, nil, del("a"))
	if rev3 != rev2+1 {
		t.Fatalf("delete returned revision %d, want %d", rev3, rev2+1)
	}
	wantAbsent(t, s, "a", 0, rev3)

	_, _, err := s.Get(ctxFor(t), "", 0)
	wantErr(t, "Get with an empty key", err, store.ErrInvalidArgument)
}

func testGetAtRevisionReadsHistory(t *testing.T, s store.Store) {
	base := currentRev(t, s)
	r1 := mustTxn(t, s, nil, put("a", "1"))
	r2 := mustTxn(t, s, nil, put("a", "2"), put("b", "x"))
	r3 := mustTxn(t, s, nil, del("a"))

	wantAbsent(t, s, "a", base, base)
	kv, rev := mustGet(t, s, "a", r1)
	wantKV(t, kv, "a", "1", r1)
	if rev != r1 {
		t.Fatalf("Get at %d returned revision %d", r1, rev)
	}
	kv, _ = mustGet(t, s, "a", r2)
	wantKV(t, kv, "a", "2", r2)
	wantAbsent(t, s, "a", r3, r3)
	kv, _ = mustGet(t, s, "b", r3)
	wantKV(t, kv, "b", "x", r2)
}

func testGetRejectsFutureRevision(t *testing.T, s store.Store) {
	rev := mustTxn(t, s, nil, put("a", "1"))
	_, _, err := s.Get(ctxFor(t), "a", rev+1)
	wantErr(t, "Get above the store revision", err, store.ErrFutureRev)
	_, _, _, err = s.Range(ctxFor(t), "", rev+1, 0, "")
	wantErr(t, "Range above the store revision", err, store.ErrFutureRev)
}

func testTxnAssignsOneRevisionToAllChanges(t *testing.T, s store.Store) {
	base := currentRev(t, s)
	mustTxn(t, s, nil, put("c", "old"))
	rev := mustTxn(t, s, nil, put("a", "1"), put("b", "2"), del("c"))
	if rev != base+2 {
		t.Fatalf("multi-key transaction returned revision %d, want %d", rev, base+2)
	}
	kv, _ := mustGet(t, s, "a", 0)
	wantKV(t, kv, "a", "1", rev)
	kv, _ = mustGet(t, s, "b", 0)
	wantKV(t, kv, "b", "2", rev)
	wantAbsent(t, s, "c", 0, rev)
	if got := currentRev(t, s); got != rev {
		t.Fatalf("CurrentRev = %d, want %d", got, rev)
	}
}

func testTxnWithoutChangesKeepsRevision(t *testing.T, s store.Store) {
	rev := mustTxn(t, s, nil, put("a", "1"))

	if got := mustTxn(t, s, nil); got != rev {
		t.Fatalf("empty transaction returned revision %d, want %d", got, rev)
	}
	if got := mustTxn(t, s, nil, del("missing")); got != rev {
		t.Fatalf("deleting an absent key returned revision %d, want %d", got, rev)
	}
	if got := currentRev(t, s); got != rev {
		t.Fatalf("CurrentRev = %d after transactions without changes, want %d", got, rev)
	}

	next := mustTxn(t, s, nil, del("missing"), put("b", "2"))
	if next != rev+1 {
		t.Fatalf("transaction with one effective change returned revision %d, want %d", next, rev+1)
	}
	same := mustTxn(t, s, nil, put("b", "2"))
	if same != next+1 {
		t.Fatalf("rewriting the same value returned revision %d, want %d", same, next+1)
	}
}

func testTxnComparesModRevisions(t *testing.T, s store.Store) {
	rev := mustTxn(t, s, nil, put("present", "1"))

	cases := []struct {
		key    string
		op     store.CmpOp
		modRev store.Rev
		want   bool
	}{
		{"present", store.CmpEqual, rev, true},
		{"present", store.CmpEqual, rev - 1, false},
		{"present", store.CmpEqual, 0, false},
		{"present", store.CmpNotEqual, rev, false},
		{"present", store.CmpNotEqual, 0, true},
		{"present", store.CmpLess, rev + 1, true},
		{"present", store.CmpLess, rev, false},
		{"present", store.CmpGreater, rev - 1, true},
		{"present", store.CmpGreater, rev, false},
		{"absent", store.CmpEqual, 0, true},
		{"absent", store.CmpEqual, rev, false},
		{"absent", store.CmpNotEqual, 0, false},
		{"absent", store.CmpLess, 1, true},
		{"absent", store.CmpLess, 0, false},
		{"absent", store.CmpGreater, 0, false},
	}
	for _, tc := range cases {
		cmp := store.Cmp{Key: tc.key, Op: tc.op, ModRev: tc.modRev}
		ok, got, err := s.Txn(ctxFor(t), []store.Cmp{cmp}, nil)
		if err != nil {
			t.Fatalf("Txn(%+v): %v", cmp, err)
		}
		if ok != tc.want {
			t.Errorf("Txn(%+v) applied = %v, want %v", cmp, ok, tc.want)
		}
		if got != rev {
			t.Errorf("Txn(%+v) returned revision %d, want %d", cmp, got, rev)
		}
	}

	both := []store.Cmp{
		{Key: "present", Op: store.CmpEqual, ModRev: rev},
		{Key: "absent", Op: store.CmpEqual, ModRev: rev},
	}
	ok, _, err := s.Txn(ctxFor(t), both, []store.Op{put("x", "1")})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("transaction applied although one of two comparisons failed")
	}

	tombCreated := mustTxn(t, s, nil, put("tomb", "v1"))
	tombDeleted := mustTxn(t, s, nil, del("tomb"))
	for _, tc := range []struct {
		op     store.CmpOp
		modRev store.Rev
		want   bool
	}{
		{store.CmpEqual, 0, true},
		{store.CmpEqual, tombCreated, false},
		{store.CmpEqual, tombDeleted, false},
		{store.CmpGreater, 0, false},
		{store.CmpLess, 1, true},
	} {
		cmp := store.Cmp{Key: "tomb", Op: tc.op, ModRev: tc.modRev}
		ok, got, err := s.Txn(ctxFor(t), []store.Cmp{cmp}, nil)
		if err != nil {
			t.Fatalf("tombstone Txn(%+v): %v", cmp, err)
		}
		if ok != tc.want || got != tombDeleted {
			t.Fatalf("tombstone Txn(%+v) = (%v, %d), want (%v, %d)", cmp, ok, got, tc.want, tombDeleted)
		}
	}
	tombRecreated := mustTxn(t, s, []store.Cmp{{Key: "tomb", Op: store.CmpEqual, ModRev: 0}}, put("tomb", "v2"))
	kv, _ := mustGet(t, s, "tomb", 0)
	wantKV(t, kv, "tomb", "v2", tombRecreated)

	mr1 := mustTxn(t, s, nil, put("multi", "1"))
	mr2 := mustTxn(t, s, nil, put("multi", "2"))
	mr3 := mustTxn(t, s, nil, put("spacer", "x"))
	inWindow := []store.Cmp{
		{Key: "multi", Op: store.CmpGreater, ModRev: mr1},
		{Key: "multi", Op: store.CmpLess, ModRev: mr3},
	}
	ok, _, err = s.Txn(ctxFor(t), inWindow, nil)
	if err != nil || !ok {
		t.Fatalf("satisfiable multi-Cmp range on one key = (%v, %v), want (true, nil)", ok, err)
	}
	for _, bad := range [][]store.Cmp{
		{{Key: "multi", Op: store.CmpGreater, ModRev: mr1}, {Key: "multi", Op: store.CmpLess, ModRev: mr2}},
		{{Key: "multi", Op: store.CmpGreater, ModRev: mr2}, {Key: "multi", Op: store.CmpLess, ModRev: mr3}},
	} {
		ok, _, err = s.Txn(ctxFor(t), bad, []store.Op{put("multi", "3")})
		if err != nil || ok {
			t.Fatalf("unsatisfiable multi-Cmp %+v = (%v, %v), want (false, nil)", bad, ok, err)
		}
	}
}

func testTxnFailedComparisonChangesNothing(t *testing.T, s store.Store) {
	rev := mustTxn(t, s, nil, put("a", "1"))
	stale := []store.Cmp{{Key: "a", Op: store.CmpEqual, ModRev: rev - 1}}
	ok, got, err := s.Txn(ctxFor(t), stale, []store.Op{put("a", "2"), put("b", "2")})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("transaction applied on a stale mod revision")
	}
	if got != rev {
		t.Fatalf("failed transaction returned revision %d, want %d", got, rev)
	}
	kv, _ := mustGet(t, s, "a", 0)
	wantKV(t, kv, "a", "1", rev)
	wantAbsent(t, s, "b", 0, rev)

	fresh := []store.Cmp{{Key: "a", Op: store.CmpEqual, ModRev: rev}}
	next := mustTxn(t, s, fresh, put("a", "2"))
	if next != rev+1 {
		t.Fatalf("compare-and-write returned revision %d, want %d", next, rev+1)
	}
	ok, _, err = s.Txn(ctxFor(t), fresh, []store.Op{put("a", "3")})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("the same compare-and-write applied twice")
	}
}

func testTxnRejectsUndefinedRequests(t *testing.T, s store.Store) {
	rev := mustTxn(t, s, nil, put("a", "1"))
	cases := []struct {
		name string
		cmps []store.Cmp
		ops  []store.Op
	}{
		{"two puts on one key", nil, []store.Op{put("k", "1"), put("k", "2")}},
		{"put and delete on one key", nil, []store.Op{put("k", "1"), del("k")}},
		{"empty key in an op", nil, []store.Op{put("", "1")}},
		{"unknown op type", nil, []store.Op{{Key: "k"}}},
		{"empty key in a comparison", []store.Cmp{{Op: store.CmpEqual}}, nil},
		{"unknown comparison operator", []store.Cmp{{Key: "k"}}, nil},
		{"negative comparison revision", []store.Cmp{{Key: "k", Op: store.CmpEqual, ModRev: -1}}, nil},
		{"duplicate hidden behind a failing comparison", []store.Cmp{{Key: "a", Op: store.CmpEqual, ModRev: 0}}, []store.Op{put("k", "1"), put("k", "2")}},
	}
	for _, tc := range cases {
		_, _, err := s.Txn(ctxFor(t), tc.cmps, tc.ops)
		if !errors.Is(err, store.ErrInvalidArgument) {
			t.Errorf("%s: error = %v, want ErrInvalidArgument", tc.name, err)
		}
	}
	if got := currentRev(t, s); got != rev {
		t.Fatalf("rejected transactions moved the revision from %d to %d", rev, got)
	}
	wantAbsent(t, s, "k", 0, rev)
}

func testOversizedRequestsFailWhole(t *testing.T, s store.Store) {
	many := make([]store.Op, 200)
	for i := range many {
		many[i] = put(fmt.Sprintf("many/%03d", i), fmt.Sprint(i))
	}
	wantAppliedWholeOrTooLarge(t, s, "a transaction of 200 puts", "many/", nil, many)

	manyCmps := make([]store.Cmp, 200)
	for i := range manyCmps {
		manyCmps[i] = store.Cmp{Key: fmt.Sprintf("cmp/%03d", i), Op: store.CmpEqual, ModRev: 0}
	}
	wantAppliedWholeOrTooLarge(t, s, "a transaction of 200 comparisons", "cmp/", manyCmps, []store.Op{put("cmp/applied", "1")})

	for _, size := range []int{7 << 18, 4 << 20} {
		value := bytes.Repeat([]byte("x"), size)
		what := fmt.Sprintf("a put of a %d KiB value", size>>10)
		wantAppliedWholeOrTooLarge(t, s, what, fmt.Sprintf("big/%d/", size), nil, []store.Op{{Type: store.OpPut, Key: fmt.Sprintf("big/%d/value", size), Value: value}})
	}
}

// wantAppliedWholeOrTooLarge runs ops, which put distinct keys under prefix
// on a store holding none there, and accepts either all of them at one new
// revision or ErrTooLarge with nothing changed.
func wantAppliedWholeOrTooLarge(t *testing.T, s store.Store, what, prefix string, cmps []store.Cmp, ops []store.Op) {
	t.Helper()
	base := currentRev(t, s)
	ok, rev, err := s.Txn(ctxFor(t), cmps, ops)
	switch {
	case errors.Is(err, store.ErrTooLarge):
		if got := currentRev(t, s); got != base {
			t.Fatalf("%s failed with ErrTooLarge but moved the revision from %d to %d", what, base, got)
		}
		kvs, _, _ := mustRange(t, s, prefix, 0, 0, "")
		if len(kvs) != 0 {
			t.Fatalf("%s failed with ErrTooLarge but left %d keys behind", what, len(kvs))
		}
	case err != nil:
		t.Fatalf("%s: error = %v, want success or ErrTooLarge", what, err)
	case !ok || rev != base+1:
		t.Fatalf("%s returned applied=%v revision %d, want true and %d", what, ok, rev, base+1)
	default:
		kvs, readRev, _ := mustRange(t, s, prefix, 0, 0, "")
		if readRev != rev || len(kvs) != len(ops) {
			t.Fatalf("%s: read %d keys at revision %d, want %d at %d", what, len(kvs), readRev, len(ops), rev)
		}
		for i, kv := range kvs {
			op := ops[i]
			if kv.Key != op.Key || kv.ModRev != rev || !bytes.Equal(kv.Value, op.Value) {
				t.Fatalf("%s: key %d is {%q mod=%d, %d bytes}, want {%q mod=%d, %d bytes}",
					what, i, kv.Key, kv.ModRev, len(kv.Value), op.Key, rev, len(op.Value))
			}
		}
	}
}

func testRangeListsPrefixInKeyOrder(t *testing.T, s store.Store) {
	base := currentRev(t, s)
	kvs, rev, next := mustRange(t, s, "", 0, 0, "")
	if len(kvs) != 0 || rev != base || next != "" {
		t.Fatalf("Range on an empty store = %v, %d, %q", kvs, rev, next)
	}

	r1 := mustTxn(t, s, nil, put("b/2", "v"), put("a", "v"), put("b/1", "v"), put("b", "v"), put("c", "v"))
	r2 := mustTxn(t, s, nil, del("b/1"), put("b/3", "w"))

	kvs, rev, next = mustRange(t, s, "", 0, 0, "")
	wantKeys(t, kvs, "a", "b", "b/2", "b/3", "c")
	if rev != r2 || next != "" {
		t.Fatalf("Range returned revision %d and continuation %q, want %d and none", rev, next, r2)
	}
	wantKV(t, kvs[2], "b/2", "v", r1)
	wantKV(t, kvs[3], "b/3", "w", r2)

	kvs, _, _ = mustRange(t, s, "b", 0, 0, "")
	wantKeys(t, kvs, "b", "b/2", "b/3")
	kvs, _, _ = mustRange(t, s, "b/", 0, 0, "")
	wantKeys(t, kvs, "b/2", "b/3")
	kvs, _, _ = mustRange(t, s, "zz", 0, 0, "")
	wantKeys(t, kvs)

	kvs, rev, _ = mustRange(t, s, "b/", r1, 0, "")
	wantKeys(t, kvs, "b/1", "b/2")
	if rev != r1 {
		t.Fatalf("Range at %d returned revision %d", r1, rev)
	}
}

func testRangePaginatesOnOneSnapshot(t *testing.T, s store.Store) {
	snapshot := mustTxn(t, s, nil, put("k1", "v"), put("k2", "v"), put("k3", "v"), put("k4", "v"), put("k5", "v"), put("other", "v"))

	page, rev, next := mustRange(t, s, "k", 0, 2, "")
	wantKeys(t, page, "k1", "k2")
	if rev != snapshot || next != "k2" {
		t.Fatalf("first page returned revision %d and continuation %q, want %d and %q", rev, next, snapshot, "k2")
	}

	mustTxn(t, s, nil, del("k3"), put("k25", "new"), put("k9", "new"))

	page, rev, next = mustRange(t, s, "k", snapshot, 2, next)
	wantKeys(t, page, "k3", "k4")
	if rev != snapshot || next != "k4" {
		t.Fatalf("second page returned revision %d and continuation %q, want %d and %q", rev, next, snapshot, "k4")
	}
	page, _, next = mustRange(t, s, "k", snapshot, 2, next)
	wantKeys(t, page, "k5")
	if next != "" {
		t.Fatalf("last page returned continuation %q", next)
	}

	page, _, next = mustRange(t, s, "k", snapshot, 5, "")
	wantKeys(t, page, "k1", "k2", "k3", "k4", "k5")
	if next != "" {
		t.Fatalf("a page that exactly holds the rest returned continuation %q", next)
	}

	page, _, next = mustRange(t, s, "k", 0, 3, "k2")
	wantKeys(t, page, "k25", "k4", "k5")
	if next != "k5" {
		t.Fatalf("continuation on the latest revision = %q, want %q", next, "k5")
	}
	page, _, next = mustRange(t, s, "k", 0, 0, "k5")
	wantKeys(t, page, "k9")
	if next != "" {
		t.Fatalf("unlimited page returned continuation %q", next)
	}

	mustTxn(t, s, nil, put("k", "exact"), put("solo", "only"))
	page, _, next = mustRange(t, s, "k", 0, 2, "k")
	wantKeys(t, page, "k1", "k2")
	if next != "k2" {
		t.Fatalf("continuation equal to prefix returned next=%q, want %q", next, "k2")
	}
	page, _, next = mustRange(t, s, "solo", 0, 0, "solo")
	wantKeys(t, page)
	if next != "" {
		t.Fatalf("continuation equal to sole matching key returned next=%q", next)
	}
	for _, past := range []string{"k9", "kz", "k\xff"} {
		page, _, next = mustRange(t, s, "k", 0, 1, past)
		wantKeys(t, page)
		if next != "" {
			t.Fatalf("continuation %q past all matching keys returned next=%q", past, next)
		}
	}
}

func testRangeRejectsUndefinedRequests(t *testing.T, s store.Store) {
	mustTxn(t, s, nil, put("a/1", "v"))
	_, _, _, err := s.Range(ctxFor(t), "a/", 0, -1, "")
	wantErr(t, "Range with a negative limit", err, store.ErrInvalidArgument)
	_, _, _, err = s.Range(ctxFor(t), "a/", 0, 0, "b/1")
	wantErr(t, "Range with a continuation outside the prefix", err, store.ErrInvalidArgument)
	_, _, _, err = s.Range(ctxFor(t), "a/", 0, 0, "a")
	wantErr(t, "Range with a continuation shorter than the prefix", err, store.ErrInvalidArgument)
}

func testCurrentRevFollowsWrites(t *testing.T, s store.Store) {
	base := currentRev(t, s)
	if again := currentRev(t, s); again != base {
		t.Fatalf("CurrentRev moved from %d to %d without a write", base, again)
	}
	for i := range 3 {
		rev := mustTxn(t, s, nil, put("a", fmt.Sprint(i)))
		want := base + store.Rev(i) + 1
		if rev != want {
			t.Fatalf("write %d returned revision %d, want %d", i, rev, want)
		}
		if got := currentRev(t, s); got != want {
			t.Fatalf("CurrentRev = %d after write %d, want %d", got, i, want)
		}
	}
}

func testCompactHidesOlderRevisions(t *testing.T, s store.Store) {
	r1 := mustTxn(t, s, nil, put("a", "1"), put("gone", "x"))
	r2 := mustTxn(t, s, nil, put("a", "2"), del("gone"))
	r3 := mustTxn(t, s, nil, put("a", "3"))

	if err := s.Compact(ctxFor(t), r2); err != nil {
		t.Fatalf("Compact(%d): %v", r2, err)
	}

	_, _, err := s.Get(ctxFor(t), "a", r1)
	wantErr(t, "Get below the compaction revision", err, store.ErrCompacted)
	_, _, _, err = s.Range(ctxFor(t), "", r1, 0, "")
	wantErr(t, "Range below the compaction revision", err, store.ErrCompacted)

	kv, _ := mustGet(t, s, "a", r2)
	wantKV(t, kv, "a", "2", r2)
	wantAbsent(t, s, "gone", r2, r2)
	kvs, _, _ := mustRange(t, s, "", r2, 0, "")
	wantKeys(t, kvs, "a")
	kv, _ = mustGet(t, s, "a", 0)
	wantKV(t, kv, "a", "3", r3)
	if got := currentRev(t, s); got != r3 {
		t.Fatalf("Compact moved the revision from %d to %d", r3, got)
	}

	if err := s.Compact(ctxFor(t), r3); err != nil {
		t.Fatalf("Compact at the store revision: %v", err)
	}
	kv, _ = mustGet(t, s, "a", r3)
	wantKV(t, kv, "a", "3", r3)
	_, _, err = s.Get(ctxFor(t), "a", r2)
	wantErr(t, "Get below the second compaction revision", err, store.ErrCompacted)
}

func testCompactRejectsRepeatedAndFutureRevisions(t *testing.T, s store.Store) {
	r1 := mustTxn(t, s, nil, put("a", "1"))
	r2 := mustTxn(t, s, nil, put("a", "2"))

	wantErr(t, "Compact above the store revision", s.Compact(ctxFor(t), r2+1), store.ErrFutureRev)
	wantErr(t, "Compact(0)", s.Compact(ctxFor(t), 0), store.ErrInvalidArgument)
	wantErr(t, "Compact(-1)", s.Compact(ctxFor(t), -1), store.ErrInvalidArgument)

	if err := s.Compact(ctxFor(t), r2); err != nil {
		t.Fatalf("Compact(%d): %v", r2, err)
	}
	wantErr(t, "Compact at the compaction revision", s.Compact(ctxFor(t), r2), store.ErrCompacted)
	wantErr(t, "Compact below the compaction revision", s.Compact(ctxFor(t), r1), store.ErrCompacted)
}

func testWatchDeliversChangesInOrder(t *testing.T, s store.Store) {
	ws := mustWatch(t, s, "", 0)
	r1 := mustTxn(t, s, nil, put("a", "1"))
	r2 := mustTxn(t, s, nil, put("c", "3"), put("b", "2"), del("a"), del("missing"))
	r3 := mustTxn(t, s, nil, put("b", ""))
	if ok, _, err := s.Txn(ctxFor(t), []store.Cmp{{Key: "b", Op: store.CmpEqual, ModRev: 0}}, []store.Op{put("d", "4")}); err != nil || ok {
		t.Fatalf("Txn with a failing comparison = %v, %v", ok, err)
	}

	events, progress := mustSync(t, s, ws)
	wantEvents(t, events,
		putEvent("a", "1", r1),
		putEvent("c", "3", r2),
		putEvent("b", "2", r2),
		deleteEvent("a", r2),
		putEvent("b", "", r3),
	)
	if progress != r3 {
		t.Fatalf("progress revision = %d, want %d", progress, r3)
	}
}

func testWatchReplaysFromRevision(t *testing.T, s store.Store) {
	mustTxn(t, s, nil, put("a", "1"))
	r2 := mustTxn(t, s, nil, put("a", "2"), put("b", "2"))
	r3 := mustTxn(t, s, nil, del("b"))

	ws := mustWatch(t, s, "", r2)
	events, progress := mustSync(t, s, ws)
	wantEvents(t, events, putEvent("a", "2", r2), putEvent("b", "2", r2), deleteEvent("b", r3))
	if progress != r3 {
		t.Fatalf("progress revision = %d, want %d", progress, r3)
	}

	r4 := mustTxn(t, s, nil, put("c", "4"))
	events, progress = mustSync(t, s, ws)
	wantEvents(t, events, putEvent("c", "4", r4))
	if progress != r4 {
		t.Fatalf("progress revision = %d, want %d", progress, r4)
	}
}

func testWatchFromZeroSeesOnlyLaterChanges(t *testing.T, s store.Store) {
	mustTxn(t, s, nil, put("a", "1"))
	ws := mustWatch(t, s, "", 0)
	r2 := mustTxn(t, s, nil, put("a", "2"))
	events, _ := mustSync(t, s, ws)
	wantEvents(t, events, putEvent("a", "2", r2))
}

func testWatchFromFutureRevisionSkipsEarlierChanges(t *testing.T, s store.Store) {
	base := mustTxn(t, s, nil, put("a", "1"))
	ws := mustWatch(t, s, "", base+3)
	mustTxn(t, s, nil, put("a", "2"))
	mustTxn(t, s, nil, put("a", "3"))

	deadline := time.After(quietCheck)
	retry := time.NewTicker(syncRetry)
	defer retry.Stop()
	for waiting := true; waiting; {
		if err := ws.RequestProgress(ctxFor(t)); err != nil {
			t.Fatalf("RequestProgress: %v", err)
		}
		select {
		case ev, open := <-ws.Events():
			t.Fatalf("a watch from revision %d delivered %s (open=%v) while the store is at %d",
				base+3, formatEvents([]store.Event{ev}), open, base+2)
		case <-retry.C:
		case <-deadline:
			waiting = false
		}
	}

	r3 := mustTxn(t, s, nil, put("a", "4"))
	r4 := mustTxn(t, s, nil, del("a"))
	events, progress := mustSync(t, s, ws)
	wantEvents(t, events, putEvent("a", "4", r3), deleteEvent("a", r4))
	if progress != r4 {
		t.Fatalf("progress revision = %d, want %d", progress, r4)
	}
}

func testWatchFiltersByPrefix(t *testing.T, s store.Store) {
	inside := mustWatch(t, s, "in/", 0)
	exact := mustWatch(t, s, "in/a", 0)
	r1 := mustTxn(t, s, nil, put("out", "x"), put("in/a", "1"), put("in", "x"), put("in/b", "2"))
	mustTxn(t, s, nil, put("out", "y"))
	r3 := mustTxn(t, s, nil, del("in/a"), del("out"))

	events, _ := mustSync(t, s, inside)
	wantEvents(t, events, putEvent("in/a", "1", r1), putEvent("in/b", "2", r1), deleteEvent("in/a", r3))
	events, _ = mustSync(t, s, exact)
	wantEvents(t, events, putEvent("in/a", "1", r1), deleteEvent("in/a", r3))
}

func testWatchProgressOnQuietPrefix(t *testing.T, s store.Store) {
	base := currentRev(t, s)
	ws := mustWatch(t, s, "quiet/", 0)
	events, progress := mustSync(t, s, ws)
	wantEvents(t, events)
	if progress != base {
		t.Fatalf("progress revision on an idle store = %d, want %d", progress, base)
	}

	mustTxn(t, s, nil, put("busy/a", "1"))
	rev := mustTxn(t, s, nil, put("busy/a", "2"))
	target := currentRev(t, s)
	events, progress = mustSync(t, s, ws)
	wantEvents(t, events)
	if progress < target || progress != rev {
		t.Fatalf("progress revision = %d, want %d (CurrentRev %d)", progress, rev, target)
	}
}

func testWatchBelowCompactionEndsCompacted(t *testing.T, s store.Store) {
	r1 := mustTxn(t, s, nil, put("a", "1"))
	r2 := mustTxn(t, s, nil, put("a", "2"))
	if err := s.Compact(ctxFor(t), r2); err != nil {
		t.Fatal(err)
	}

	ws := mustWatch(t, s, "", r1)
	events, _, err := Sync(ctxFor(t), s, ws)
	wantErr(t, "a watch below the compaction revision", err, store.ErrCompacted)
	if len(events) != 0 {
		t.Fatalf("a compacted watch delivered %s", formatEvents(events))
	}
	wantErr(t, "WatchStream.Err", ws.Err(), store.ErrCompacted)
	if _, open := <-ws.Events(); open {
		t.Fatal("Events stays open after the watch ended")
	}
}

func testWatchAtCompactionRevisionSeesItsChanges(t *testing.T, s store.Store) {
	mustTxn(t, s, nil, put("a", "1"), put("b", "1"))
	r2 := mustTxn(t, s, nil, del("a"), put("b", "2"))
	r3 := mustTxn(t, s, nil, put("c", "3"))
	if err := s.Compact(ctxFor(t), r2); err != nil {
		t.Fatal(err)
	}

	ws := mustWatch(t, s, "", r2)
	events, progress := mustSync(t, s, ws)
	wantEvents(t, events, deleteEvent("a", r2), putEvent("b", "2", r2), putEvent("c", "3", r3))
	if progress != r3 {
		t.Fatalf("progress revision = %d, want %d", progress, r3)
	}

	live := mustWatch(t, s, "", 0)
	r4 := mustTxn(t, s, nil, put("d", "4"))
	if err := s.Compact(ctxFor(t), r4); err != nil {
		t.Fatal(err)
	}
	events, _ = mustSync(t, s, live)
	wantEvents(t, events, putEvent("d", "4", r4))
}

func testWatchOvertakenByCompactionEndsCleanly(t *testing.T, s store.Store) {
	var want []store.Event
	for i := range 50 {
		key, value := fmt.Sprintf("k/%02d", i), fmt.Sprint(i)
		want = append(want, putEvent(key, value, mustTxn(t, s, nil, put(key, value))))
	}
	last := want[len(want)-1].Rev

	ws := mustWatch(t, s, "", want[0].Rev)
	if err := s.Compact(ctxFor(t), last); err != nil {
		t.Fatalf("Compact(%d): %v", last, err)
	}
	events, progress, err := Sync(ctxFor(t), s, ws)
	switch {
	case err == nil:
		wantEvents(t, events, want...)
		if progress != last {
			t.Fatalf("progress revision = %d, want %d", progress, last)
		}
	case errors.Is(err, store.ErrCompacted):
		if len(events) > len(want) {
			t.Fatalf("an overtaken watch delivered %d events, more than the %d changes", len(events), len(want))
		}
		wantEvents(t, events, want[:len(events)]...)
		wantErr(t, "WatchStream.Err", ws.Err(), store.ErrCompacted)
		if _, open := <-ws.Events(); open {
			t.Fatal("Events stays open after the watch ended")
		}
	default:
		t.Fatalf("Sync: %v (after %d events), want the changes or ErrCompacted", err, len(events))
	}
}

func testWatchEndsWithContext(t *testing.T, s store.Store) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws, err := s.Watch(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	mustTxn(t, s, nil, put("a", "1"))
	if _, _, err := Sync(ctxFor(t), s, ws); err != nil {
		t.Fatal(err)
	}
	cancel()

	deadline := time.After(waitLimit)
	for open := true; open; {
		select {
		case _, open = <-ws.Events():
		case <-deadline:
			t.Fatal("Events was not closed after the watch context was canceled")
		}
	}
	wantErr(t, "WatchStream.Err", ws.Err(), context.Canceled)
	if err := ws.RequestProgress(ctxFor(t)); err == nil {
		t.Fatal("RequestProgress succeeded on an ended watch")
	}
}

func testCallsRejectNegativeRevisions(t *testing.T, s store.Store) {
	_, _, err := s.Get(ctxFor(t), "a", -1)
	wantErr(t, "Get(-1)", err, store.ErrInvalidArgument)
	_, _, _, err = s.Range(ctxFor(t), "", -1, 0, "")
	wantErr(t, "Range(-1)", err, store.ErrInvalidArgument)
	_, err = s.Watch(ctxFor(t), "", -1)
	wantErr(t, "Watch(-1)", err, store.ErrInvalidArgument)
}

func testCallsHonorCanceledContext(t *testing.T, s store.Store) {
	rev := mustTxn(t, s, nil, put("a", "1"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := s.Get(ctx, "a", 0); err == nil {
		t.Error("Get succeeded on a canceled context")
	}
	if _, _, _, err := s.Range(ctx, "", 0, 0, ""); err == nil {
		t.Error("Range succeeded on a canceled context")
	}
	if _, _, err := s.Txn(ctx, nil, []store.Op{put("a", "2")}); err == nil {
		t.Error("Txn succeeded on a canceled context")
	}
	if _, err := s.CurrentRev(ctx); err == nil {
		t.Error("CurrentRev succeeded on a canceled context")
	}
	if err := s.Compact(ctx, rev); err == nil {
		t.Error("Compact succeeded on a canceled context")
	}
	if _, err := s.Watch(ctx, "", 0); err == nil {
		t.Error("Watch succeeded on a canceled context")
	}
	kv, _ := mustGet(t, s, "a", 0)
	wantKV(t, kv, "a", "1", rev)
}

func testValuesNormalizeEmptyAndIsolateCallerSlices(t *testing.T, s store.Store) {
	ws1 := mustWatch(t, s, "", 0)
	ws2 := mustWatch(t, s, "", 0)

	emptyRev := mustTxn(t, s, nil,
		store.Op{Type: store.OpPut, Key: "empty/nil", Value: nil},
		store.Op{Type: store.OpPut, Key: "empty/slice", Value: []byte{}},
	)
	for _, key := range []string{"empty/nil", "empty/slice"} {
		kv, _ := mustGet(t, s, key, 0)
		if kv.Value != nil || kv.ModRev != emptyRev {
			t.Fatalf("Get(%q) = {Value nil=%v len=%d mod=%d}, want nil Value at %d", key, kv.Value == nil, len(kv.Value), kv.ModRev, emptyRev)
		}
	}
	kvs, _, _ := mustRange(t, s, "empty/", 0, 0, "")
	if len(kvs) != 2 || kvs[0].Value != nil || kvs[1].Value != nil {
		t.Fatalf("Range(empty/) = %+v, want two nil-Value KVs", kvs)
	}
	evEmpty1, _ := mustSync(t, s, ws1)
	if len(evEmpty1) != 2 || evEmpty1[0].Value != nil || evEmpty1[1].Value != nil {
		t.Fatalf("Watch empty values = %+v, want nil Event.Value for both puts", evEmpty1)
	}
	mustSync(t, s, ws2)

	in := []byte("alpha")
	op := store.Op{Type: store.OpPut, Key: "iso", Value: in}
	isoRev := mustTxn(t, s, nil, op)
	op.Value[0] ^= 0xff

	kv, _ := mustGet(t, s, "iso", 0)
	wantKV(t, kv, "iso", "alpha", isoRev)
	kv.Value[0] ^= 0xff

	ranged, _, _ := mustRange(t, s, "iso", 0, 0, "")
	if len(ranged) != 1 {
		t.Fatalf("Range(iso) = %d keys, want 1", len(ranged))
	}
	wantKV(t, ranged[0], "iso", "alpha", isoRev)
	ranged[0].Value[0] ^= 0xff

	evs1, _ := mustSync(t, s, ws1)
	wantEvents(t, evs1, putEvent("iso", "alpha", isoRev))
	evs1[0].Value[0] ^= 0xff

	evs2, _ := mustSync(t, s, ws2)
	wantEvents(t, evs2, putEvent("iso", "alpha", isoRev))

	kvAfter, _ := mustGet(t, s, "iso", 0)
	wantKV(t, kvAfter, "iso", "alpha", isoRev)
}

func testBoundaryByteKeysAndPrefixes(t *testing.T, s store.Store) {
	sortedKeys := []string{
		"\x00",
		"\x00a",
		"a",
		"a\x00",
		"a\x00b",
		"a\xff",
		"a\xff\x00",
		"b",
		"\xff",
		"\xff\x00",
		"\xff\xff",
		"\xff\xffa",
	}
	prefixes := []string{"\x00", "a\x00", "a\xff", "\xff", "\xff\xff"}
	watches := make(map[string]store.WatchStream, len(prefixes))
	for _, p := range prefixes {
		watches[p] = mustWatch(t, s, p, 0)
	}

	// Write in reverse order so storage order is not an artifact of insertion order.
	ops := make([]store.Op, len(sortedKeys))
	for i, key := range sortedKeys {
		ops[len(sortedKeys)-1-i] = put(key, fmt.Sprintf("v-%d", i))
	}
	rev := mustTxn(t, s, nil, ops...)

	for i, key := range sortedKeys {
		kv, _ := mustGet(t, s, key, 0)
		wantKV(t, kv, key, fmt.Sprintf("v-%d", i), rev)
	}

	all, _, next := mustRange(t, s, "", rev, 0, "")
	wantKeys(t, all, sortedKeys...)
	if next != "" {
		t.Fatalf("Range(\"\") next = %q, want empty", next)
	}

	for _, p := range prefixes {
		var want []string
		for _, k := range sortedKeys {
			if strings.HasPrefix(k, p) {
				want = append(want, k)
			}
		}
		got, _, pNext := mustRange(t, s, p, rev, 0, "")
		wantKeys(t, got, want...)
		if pNext != "" {
			t.Fatalf("Range(%q) next = %q, want empty", p, pNext)
		}

		var paged []string
		for cont, guard := "", 0; ; guard++ {
			page, _, more := mustRange(t, s, p, rev, 1, cont)
			paged = append(paged, keysOf(page)...)
			if more == "" {
				break
			}
			if guard > len(want) {
				t.Fatalf("Range(%q) pagination did not terminate", p)
			}
			cont = more
		}
		if !slices.Equal(paged, want) {
			t.Fatalf("paged Range(%q) = %q, want %q", p, paged, want)
		}

		afterSelf, _, afterNext := mustRange(t, s, p, rev, 0, p)
		wantKeys(t, afterSelf, want[1:]...)
		if afterNext != "" {
			t.Fatalf("Range(%q, cont=%q) next = %q, want empty", p, p, afterNext)
		}

		evs, prog := mustSync(t, s, watches[p])
		if len(evs) != len(want) || prog != rev {
			t.Fatalf("Watch(%q) delivered %d events with progress %d, want %d at %d", p, len(evs), prog, len(want), rev)
		}
		for _, ev := range evs {
			if !strings.HasPrefix(ev.Key, p) {
				t.Fatalf("Watch(%q) delivered non-matching key %q", p, ev.Key)
			}
		}
	}
}

func testWatchIsolatesProgressAcrossStreams(t *testing.T, s store.Store) {
	base := mustTxn(t, s, nil, put("a", "1"))
	caughtUp := mustWatch(t, s, "", 0)
	future := mustWatch(t, s, "", base+4)

	r1 := mustTxn(t, s, nil, put("a", "2"))
	r2 := mustTxn(t, s, nil, put("b", "2"))

	evs, prog := mustSync(t, s, caughtUp)
	wantEvents(t, evs, putEvent("a", "2", r1), putEvent("b", "2", r2))
	if prog != r2 {
		t.Fatalf("caught-up progress = %d, want %d", prog, r2)
	}

	deadline := time.After(quietCheck)
	retry := time.NewTicker(syncRetry)
	defer retry.Stop()
	sendProbes := func() {
		if err := caughtUp.RequestProgress(ctxFor(t)); err != nil {
			t.Fatalf("caughtUp.RequestProgress: %v", err)
		}
		if err := future.RequestProgress(ctxFor(t)); err != nil {
			t.Fatalf("future.RequestProgress: %v", err)
		}
	}
	sendProbes()
	for waiting := true; waiting; {
		select {
		case ev, open := <-future.Events():
			t.Fatalf("future watch from %d received %s (open=%v) while store is at %d",
				base+4, formatEvents([]store.Event{ev}), open, r2)
		case <-caughtUp.Events():
		case <-retry.C:
			sendProbes()
		case <-deadline:
			waiting = false
		}
	}

	mustTxn(t, s, nil, put("a", "3"))
	r4 := mustTxn(t, s, nil, put("a", "4"))
	r5 := mustTxn(t, s, nil, put("b", "5"))
	fEvs, fProg := mustSync(t, s, future)
	wantEvents(t, fEvs, putEvent("a", "4", r4), putEvent("b", "5", r5))
	if fProg != r5 {
		t.Fatalf("future watch progress = %d, want %d", fProg, r5)
	}
}

func testEndedWatchReportsItsError(t *testing.T, s store.Store) {
	canceledCtx, cancelNow := context.WithCancel(context.Background())
	cancelNow()

	giveUp := time.Now().Add(waitLimit)
	for opened := 0; opened < 50; {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		ws, err := s.Watch(ctx, "", 0)
		if errors.Is(err, context.DeadlineExceeded) {
			// The deadline passed before the watch opened; a store rejects a call whose context is already done.
			cancel()
			if time.Now().After(giveUp) {
				t.Fatalf("opened %d of 50 watches before their deadline within %v", opened, waitLimit)
			}
			continue
		}
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		opened++
		<-ctx.Done()
		for err == nil {
			err = ws.RequestProgress(context.Background())
			if err == nil {
				<-time.After(time.Millisecond)
			}
		}
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("RequestProgress on a watch past its deadline = %v, want DeadlineExceeded", err)
		}
		if err := ws.RequestProgress(canceledCtx); !errors.Is(err, context.Canceled) {
			t.Fatalf("RequestProgress(canceledCtx) on a deadline-ended watch = %v, want Canceled", err)
		}
	}

	r1 := mustTxn(t, s, nil, put("a", "1"))
	r2 := mustTxn(t, s, nil, put("a", "2"))
	if err := s.Compact(ctxFor(t), r2); err != nil {
		t.Fatal(err)
	}
	compactedWatch := mustWatch(t, s, "", r1)
	deadline := time.After(waitLimit)
	for open := true; open; {
		select {
		case _, open = <-compactedWatch.Events():
		case <-deadline:
			t.Fatal("compacted watch did not close Events")
		}
	}
	wantErr(t, "RequestProgress on an ended compacted watch", compactedWatch.RequestProgress(ctxFor(t)), store.ErrCompacted)
	wantErr(t, "RequestProgress(canceledCtx) on an ended compacted watch", compactedWatch.RequestProgress(canceledCtx), context.Canceled)
}

func testConcurrentWritersWatchersAndCompaction(t *testing.T, s store.Store) {
	ctx := ctxFor(t)
	base := currentRev(t, s)
	keys := []string{"w/0", "w/1", "w/2", "w/3", "w/4", "w/5", "other/0", "other/1"}

	watchAll0 := mustWatch(t, s, "", 0)
	watchAll1 := mustWatch(t, s, "", 0)
	watchPrefix := mustWatch(t, s, "w/", 0)

	writersDone := make(chan struct{})
	type watchOut struct {
		events []store.Event
		state  map[string]store.KV
		prog   store.Rev
		err    error
	}
	runWatcher := func(ws store.WatchStream, prefix string, out *watchOut, wg *sync.WaitGroup) {
		defer wg.Done()
		state := make(map[string]store.KV)
		var events []store.Event
		var prevRev store.Rev
		seenInRev := make(map[string]bool)
		apply := func(ev store.Event) error {
			if ev.Rev < prevRev {
				return fmt.Errorf("event revision regressed from %d to %d", prevRev, ev.Rev)
			}
			if ev.Type == store.EventProgress {
				prevRev = ev.Rev
				return nil
			}
			if !strings.HasPrefix(ev.Key, prefix) {
				return fmt.Errorf("watch(%q) delivered key %q", prefix, ev.Key)
			}
			if ev.Rev > prevRev {
				prevRev = ev.Rev
				clear(seenInRev)
			}
			if seenInRev[ev.Key] {
				return fmt.Errorf("duplicate key %q at revision %d", ev.Key, ev.Rev)
			}
			seenInRev[ev.Key] = true
			events = append(events, ev)
			if ev.Type == store.EventPut {
				state[ev.Key] = store.KV{Key: ev.Key, Value: bytes.Clone(ev.Value), ModRev: ev.Rev}
			} else {
				delete(state, ev.Key)
			}
			return nil
		}
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		for draining := true; draining; {
			select {
			case ev, open := <-ws.Events():
				if !open {
					out.err = fmt.Errorf("watch(%q) closed early: %w", prefix, ws.Err())
					return
				}
				if err := apply(ev); err != nil {
					out.err = err
					return
				}
			case <-ticker.C:
				if err := ws.RequestProgress(ctx); err != nil {
					out.err = fmt.Errorf("RequestProgress(%q): %w", prefix, err)
					return
				}
			case <-writersDone:
				draining = false
			}
		}
		tail, prog, err := Sync(ctx, s, ws)
		if err != nil {
			out.err = fmt.Errorf("Sync(%q): %w", prefix, err)
			return
		}
		for _, ev := range tail {
			if err := apply(ev); err != nil {
				out.err = err
				return
			}
		}
		if prog < prevRev {
			out.err = fmt.Errorf("progress %d below last event revision %d", prog, prevRev)
			return
		}
		out.events, out.state, out.prog = events, state, prog
	}

	var watcherWG sync.WaitGroup
	var resAll0, resAll1, resPrefix watchOut
	watcherWG.Add(3)
	go runWatcher(watchAll0, "", &resAll0, &watcherWG)
	go runWatcher(watchAll1, "", &resAll1, &watcherWG)
	go runWatcher(watchPrefix, "w/", &resPrefix, &watcherWG)

	const numWriters = 4
	const opsPerWriter = 25
	writerErrs := make([]error, numWriters)
	var writerWG sync.WaitGroup
	for id := range numWriters {
		writerWG.Add(1)
		go func() {
			defer writerWG.Done()
			rng := rand.New(rand.NewPCG(0x9e3779b97f4a7c15, uint64(id)))
			for seq := range opsPerWriter {
				k1 := keys[rng.IntN(len(keys))]
				val := fmt.Appendf(nil, "w%d-%d", id, seq)
				if rng.IntN(3) == 0 {
					var mod store.Rev
					kv, _, err := s.Get(ctx, k1, 0)
					if err != nil && !errors.Is(err, store.ErrNotFound) {
						writerErrs[id] = err
						return
					}
					if err == nil {
						mod = kv.ModRev
					}
					op := store.Op{Type: store.OpPut, Key: k1, Value: val}
					if rng.IntN(4) == 0 {
						op = store.Op{Type: store.OpDelete, Key: k1}
					}
					if _, _, err := s.Txn(ctx, []store.Cmp{{Key: k1, Op: store.CmpEqual, ModRev: mod}}, []store.Op{op}); err != nil {
						writerErrs[id] = err
						return
					}
					continue
				}
				k2 := keys[(rng.IntN(len(keys)-1)+slices.Index(keys, k1)+1)%len(keys)]
				ops := []store.Op{{Type: store.OpPut, Key: k1, Value: val}}
				if rng.IntN(3) == 0 {
					ops = append(ops, store.Op{Type: store.OpDelete, Key: k2})
				} else {
					ops = append(ops, store.Op{Type: store.OpPut, Key: k2, Value: val})
				}
				if _, _, err := s.Txn(ctx, nil, ops); err != nil {
					writerErrs[id] = err
					return
				}
			}
		}()
	}

	var maxCompacted atomic.Int64
	var compactErr error
	writersFinished := make(chan struct{})
	var compactWG sync.WaitGroup
	compactWG.Add(1)
	go func() {
		defer compactWG.Done()
		for {
			select {
			case <-writersFinished:
				return
			default:
			}
			rev, err := s.CurrentRev(ctx)
			if err != nil {
				compactErr = err
				return
			}
			target := rev - 3
			if target > base && int64(target) > maxCompacted.Load() {
				err := s.Compact(ctx, target)
				if err != nil && !errors.Is(err, store.ErrCompacted) {
					compactErr = err
					return
				}
				if err == nil {
					maxCompacted.Store(int64(target))
				}
			}
			runtime.Gosched()
		}
	}()

	writerWG.Wait()
	close(writersFinished)
	compactWG.Wait()
	if err := errors.Join(append(writerErrs, compactErr)...); err != nil {
		close(writersDone)
		watcherWG.Wait()
		t.Fatal(err)
	}

	finalRev := currentRev(t, s)
	if maxCompacted.Load() == 0 && finalRev > base+1 {
		target := finalRev - 1
		if err := s.Compact(ctx, target); err != nil {
			close(writersDone)
			watcherWG.Wait()
			t.Fatalf("Compact(%d): %v", target, err)
		}
		maxCompacted.Store(int64(target))
	}

	close(writersDone)
	watcherWG.Wait()
	for _, wErr := range []error{resAll0.err, resAll1.err, resPrefix.err} {
		if wErr != nil {
			t.Fatal(wErr)
		}
	}

	wantEvents(t, resAll0.events, resAll1.events...)
	if resAll0.prog != finalRev || resAll1.prog != finalRev || resPrefix.prog != finalRev {
		t.Fatalf("progress revisions = (%d, %d, %d), want all %d", resAll0.prog, resAll1.prog, resPrefix.prog, finalRev)
	}

	checkSnapshot := func(prefix string, state map[string]store.KV) {
		kvs, snapRev, _ := mustRange(t, s, prefix, finalRev, 0, "")
		if snapRev != finalRev || len(kvs) != len(state) {
			t.Fatalf("snapshot(%q) = %d keys at %d, watch materialized %d keys", prefix, len(kvs), snapRev, len(state))
		}
		for _, kv := range kvs {
			got, ok := state[kv.Key]
			if !ok || got.ModRev != kv.ModRev || !bytes.Equal(got.Value, kv.Value) {
				t.Fatalf("snapshot(%q) key %q = %+v, watch materialized %+v", prefix, kv.Key, kv, got)
			}
		}
	}
	checkSnapshot("", resAll0.state)
	checkSnapshot("w/", resPrefix.state)

	if compacted := store.Rev(maxCompacted.Load()); compacted > base {
		_, _, err := s.Get(ctx, "w/0", compacted-1)
		wantErr(t, "Get below concurrent compaction revision", err, store.ErrCompacted)
	}
}
