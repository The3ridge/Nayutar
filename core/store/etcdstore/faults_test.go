//go:build etcd

package etcdstore_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore/etcdtest"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
)

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func waitLeader(ctx context.Context, c *etcdtest.Cluster) (int, error) {
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		leader, err := c.Leader(ctx)
		if err == nil {
			return leader, nil
		}
		lastErr = err
		if err := sleepContext(ctx, 100*time.Millisecond); err != nil {
			return -1, err
		}
	}
	return -1, fmt.Errorf("no leader elected: %w", lastErr)
}

func waitOtherLeader(ctx context.Context, c *etcdtest.Cluster, excluded int) (int, error) {
	var eps []string
	var indices []int
	for i, ep := range c.Endpoints {
		if i != excluded {
			eps = append(eps, ep)
			indices = append(indices, i)
		}
	}
	cl, err := clientv3.New(clientv3.Config{
		Endpoints:   eps,
		DialTimeout: 5 * time.Second,
		Logger:      zap.NewNop(),
	})
	if err != nil {
		return -1, err
	}
	defer func() { _ = cl.Close() }()

	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		var leaderID uint64
		leaderIdx := -1
		var stepErr error
		for idx, ep := range eps {
			subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			st, err := cl.Status(subCtx, ep)
			cancel()
			if err != nil {
				stepErr = err
				break
			}
			if st.Leader == 0 {
				stepErr = fmt.Errorf("member %d has no leader", indices[idx])
				break
			}
			if leaderID == 0 {
				leaderID = st.Leader
			} else if st.Leader != leaderID {
				stepErr = fmt.Errorf("members disagree on leader: %x vs %x", leaderID, st.Leader)
				break
			}
			if st.Header.MemberId == st.Leader {
				leaderIdx = indices[idx]
			}
		}
		if stepErr == nil && leaderIdx >= 0 {
			return leaderIdx, nil
		}
		if stepErr != nil {
			lastErr = stepErr
		} else {
			lastErr = fmt.Errorf("remaining members still report excluded leader %x", leaderID)
		}
		if err := sleepContext(ctx, 100*time.Millisecond); err != nil {
			return -1, err
		}
	}
	return -1, fmt.Errorf("no new leader elected excluding member %d: %w", excluded, lastErr)
}

// disruptCluster kills and restarts one follower, then kills the leader, waits
// for a new election, and restarts the old leader. At most one member is down
// at any time, and all three are running again before it returns.
func disruptCluster(ctx context.Context, c *etcdtest.Cluster) error {
	stopped := -1
	defer func() {
		if stopped >= 0 {
			_ = c.StartMember(stopped)
		}
	}()

	leader, err := waitLeader(ctx, c)
	if err != nil {
		return fmt.Errorf("find initial leader: %w", err)
	}
	follower := (leader + 1) % etcdtest.Members
	if err := c.StopMember(follower); err != nil {
		return fmt.Errorf("stop follower %d: %w", follower, err)
	}
	stopped = follower
	if err := sleepContext(ctx, 400*time.Millisecond); err != nil {
		return err
	}
	if err := c.StartMember(follower); err != nil {
		return fmt.Errorf("restart follower %d: %w", follower, err)
	}
	stopped = -1

	leader, err = waitLeader(ctx, c)
	if err != nil {
		return fmt.Errorf("find leader before failover: %w", err)
	}
	if err := c.StopMember(leader); err != nil {
		return fmt.Errorf("stop leader %d: %w", leader, err)
	}
	stopped = leader
	if _, err := waitLeader(ctx, c); err != nil {
		return fmt.Errorf("wait for election after stopping leader %d: %w", leader, err)
	}
	if err := sleepContext(ctx, 300*time.Millisecond); err != nil {
		return err
	}
	if err := c.StartMember(leader); err != nil {
		return fmt.Errorf("restart old leader %d: %w", leader, err)
	}
	stopped = -1
	return nil
}

func TestLinearizabilityAcrossMemberFailures(t *testing.T) {
	for seed := uint64(1); seed <= 3; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			w := storetest.Workload{
				Clients: 8,
				Ops:     60,
				Seed:    seed,
				Disrupt: func(dCtx context.Context) error {
					return disruptCluster(dCtx, cluster)
				},
			}
			if err := storetest.CheckLinearizable(ctx, isolated(t), w); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func reopenWatch(t *testing.T, watchCtx context.Context, s store.Store, fromRev store.Rev) store.WatchStream {
	t.Helper()
	for {
		ws, err := s.Watch(watchCtx, "", fromRev)
		if err == nil {
			return ws
		}
		if watchCtx.Err() != nil {
			t.Fatalf("Watch(fromRev=%d): %v", fromRev, err)
		}
		if err := sleepContext(watchCtx, 50*time.Millisecond); err != nil {
			t.Fatalf("Watch(fromRev=%d): %v", fromRev, err)
		}
	}
}

type ackedWrite struct {
	key string
	val string
}

func startWatchWriter(ctx context.Context, s store.Store, stop <-chan struct{}, minWrites int) (
	acked map[store.Rev]ackedWrite,
	unknown map[string]string,
	done <-chan struct{},
	errCh <-chan error,
) {
	acked = make(map[store.Rev]ackedWrite)
	unknown = make(map[string]string)
	doneCh := make(chan struct{})
	outErr := make(chan error, 1)
	go func() {
		defer close(doneCh)
		for seq := 1; ; seq++ {
			if seq > minWrites {
				select {
				case <-stop:
					outErr <- nil
					return
				default:
				}
			}
			key := fmt.Sprintf("k/%d", seq%4)
			val := fmt.Sprintf("v-%d", seq)
			callCtx, callCancel := context.WithTimeout(ctx, 3*time.Second)
			ok, rev, err := s.Txn(callCtx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: []byte(val)}})
			callCancel()
			if err != nil {
				if ctx.Err() != nil {
					outErr <- ctx.Err()
					return
				}
				unknown[val] = key
			} else if !ok {
				outErr <- fmt.Errorf("unconditional Txn %d did not apply", seq)
				return
			} else {
				acked[rev] = ackedWrite{key: key, val: val}
			}
			if err := sleepContext(ctx, 10*time.Millisecond); err != nil {
				outErr <- err
				return
			}
		}
	}()
	return acked, unknown, doneCh, outErr
}

func syncWatchToRange(
	t *testing.T,
	ctx context.Context,
	s store.Store,
	ws store.WatchStream,
	fromRev store.Rev,
	watchCancel context.CancelFunc,
	delivered []store.Event,
	lastDeliveredRev store.Rev,
) ([]store.Event, []store.KV) {
	t.Helper()
	defer func() { watchCancel() }()
	for {
		curRev, err := s.CurrentRev(ctx)
		if err != nil {
			if ctx.Err() != nil {
				watchCancel()
				t.Fatalf("CurrentRev: %v", err)
			}
			if err := sleepContext(ctx, 50*time.Millisecond); err != nil {
				watchCancel()
				t.Fatal(err)
			}
			continue
		}
		target := curRev
		// Skip Sync when the reopened watch starts beyond CurrentRev, since RequestProgress is dropped until CurrentRev reaches fromRev.
		if curRev >= fromRev {
			syncEvents, progress, err := storetest.Sync(ctx, s, ws)
			for _, ev := range syncEvents {
				delivered = append(delivered, ev)
				lastDeliveredRev = ev.Rev
			}
			if err != nil {
				if ctx.Err() != nil {
					watchCancel()
					t.Fatalf("Sync: %v", err)
				}
				if err := sleepContext(ctx, 50*time.Millisecond); err != nil {
					watchCancel()
					t.Fatal(err)
				}
				watchCancel()
				var watchCtx context.Context
				watchCtx, watchCancel = context.WithCancel(ctx)
				fromRev = lastDeliveredRev + 1
				ws = reopenWatch(t, watchCtx, s, fromRev)
				continue
			}
			target = progress
		}
		kvs, readRev, next, err := s.Range(ctx, "", 0, 0, "")
		if err != nil {
			watchCancel()
			t.Fatalf("Range: %v", err)
		}
		if next != "" {
			watchCancel()
			t.Fatalf("Range returned unexpected continuation %q", next)
		}
		if readRev == target {
			watchCancel()
			return delivered, kvs
		}
	}
}

func assertWatchDelivery(t *testing.T, delivered []store.Event, acked map[store.Rev]ackedWrite, unknown map[string]string, kvs []store.KV) {
	t.Helper()
	if len(acked) == 0 {
		t.Fatal("no acknowledged writes occurred")
	}

	var prevRev store.Rev
	seenAcked := make(map[store.Rev]bool, len(acked))
	seenUnknown := make(map[string]bool, len(unknown))
	replayed := make(map[string]store.KV)
	for _, ev := range delivered {
		if ev.Type != store.EventPut {
			t.Fatalf("unexpected event type %v at revision %d", ev.Type, ev.Rev)
		}
		if ev.Rev <= prevRev {
			t.Fatalf("event revisions not strictly ascending: %d after %d", ev.Rev, prevRev)
		}
		prevRev = ev.Rev

		if aw, ok := acked[ev.Rev]; ok {
			if seenAcked[ev.Rev] {
				t.Fatalf("duplicate event at acknowledged revision %d", ev.Rev)
			}
			if ev.Key != aw.key || string(ev.Value) != aw.val {
				t.Fatalf("event at revision %d = (%q, %q), want (%q, %q)", ev.Rev, ev.Key, ev.Value, aw.key, aw.val)
			}
			seenAcked[ev.Rev] = true
		} else {
			val := string(ev.Value)
			expectedKey, ok := unknown[val]
			if !ok || ev.Key != expectedKey {
				t.Fatalf("event at revision %d = (%q, %q) matches neither acked nor unknown writes", ev.Rev, ev.Key, val)
			}
			if seenUnknown[val] {
				t.Fatalf("duplicate event for unknown-outcome write %q at revision %d", val, ev.Rev)
			}
			seenUnknown[val] = true
		}
		replayed[ev.Key] = store.KV{Key: ev.Key, Value: ev.Value, ModRev: ev.Rev}
	}
	if len(seenAcked) != len(acked) {
		t.Fatalf("delivered %d of %d acknowledged writes", len(seenAcked), len(acked))
	}

	if len(kvs) != len(replayed) {
		t.Fatalf("final Range has %d keys, replayed events have %d keys", len(kvs), len(replayed))
	}
	for _, kv := range kvs {
		rv, ok := replayed[kv.Key]
		if !ok || rv.ModRev != kv.ModRev || !bytes.Equal(rv.Value, kv.Value) {
			t.Fatalf("key %q mismatch: Range={%q mod=%d}, replay={%q mod=%d, ok=%v}",
				kv.Key, kv.Value, kv.ModRev, rv.Value, rv.ModRev, ok)
		}
	}
}

func TestWatchAcrossMemberFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	s := isolated(t)
	baseRev, err := s.CurrentRev(ctx)
	if err != nil {
		t.Fatal(err)
	}

	watchCtx, watchCancel := context.WithCancel(ctx)
	defer func() { watchCancel() }()
	ws, err := s.Watch(watchCtx, "", baseRev)
	if err != nil {
		t.Fatal(err)
	}

	disruptDone := make(chan struct{})
	disruptErrCh := make(chan error, 1)
	go func() {
		defer close(disruptDone)
		disruptErrCh <- disruptCluster(ctx, cluster)
	}()
	// Never return while disruptCluster may still restart a stopped member.
	defer func() {
		cancel()
		<-disruptDone
	}()

	acked, unknown, writerDone, writerErrCh := startWatchWriter(ctx, s, disruptDone, 50)

	var delivered []store.Event
	lastDeliveredRev := store.Rev(0)
	watchFromRev := baseRev
	reopens := 0
	reopenTimer := time.NewTimer(150 * time.Millisecond)
	defer reopenTimer.Stop()
	for watching := true; watching; {
		select {
		case <-writerDone:
			watching = false
		case <-reopenTimer.C:
			watchCancel()
			for ev := range ws.Events() {
				if ev.Type != store.EventProgress {
					delivered = append(delivered, ev)
					lastDeliveredRev = ev.Rev
				}
			}
			watchCtx, watchCancel = context.WithCancel(ctx)
			watchFromRev = lastDeliveredRev + 1
			ws = reopenWatch(t, watchCtx, s, watchFromRev)
			reopens++
			reopenTimer.Reset(150 * time.Millisecond)
		case ev, open := <-ws.Events():
			if !open {
				err := ws.Err()
				if ctx.Err() != nil {
					t.Fatalf("watch ended while test context done: %v (ctx: %v)", err, ctx.Err())
				}
				if err == nil {
					t.Fatal("watch ended without an error while watch context is active")
				}
				if err := sleepContext(ctx, 50*time.Millisecond); err != nil {
					t.Fatal(err)
				}
				watchCancel()
				watchCtx, watchCancel = context.WithCancel(ctx)
				watchFromRev = lastDeliveredRev + 1
				ws = reopenWatch(t, watchCtx, s, watchFromRev)
				if !reopenTimer.Stop() {
					select {
					case <-reopenTimer.C:
					default:
					}
				}
				reopenTimer.Reset(150 * time.Millisecond)
				continue
			}
			if ev.Type != store.EventProgress {
				delivered = append(delivered, ev)
				lastDeliveredRev = ev.Rev
			}
		}
	}

	if err := <-disruptErrCh; err != nil {
		t.Fatalf("disruptCluster: %v", err)
	}
	if err := <-writerErrCh; err != nil {
		t.Fatalf("writer: %v", err)
	}
	if reopens == 0 {
		t.Fatal("expected at least one deliberate watch reopen")
	}

	delivered, kvs := syncWatchToRange(t, ctx, s, ws, watchFromRev, watchCancel, delivered, lastDeliveredRev)

	t.Logf("reopens=%d acked=%d unknown=%d delivered=%d", reopens, len(acked), len(unknown), len(delivered))

	assertWatchDelivery(t, delivered, acked, unknown, kvs)
}
