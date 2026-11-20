//go:build etcd

package etcdstore_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore/etcdtest"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
)

type clusterGuard struct {
	paused  [etcdtest.Members]bool
	stopped [etcdtest.Members]bool
}

func newClusterGuard(t *testing.T) *clusterGuard {
	t.Helper()
	g := &clusterGuard{}
	t.Cleanup(func() { g.restoreAndWait(t) })
	return g
}

func (g *clusterGuard) pause(member int) error {
	if g.paused[member] {
		return nil
	}
	if err := cluster.PauseMember(member); err != nil {
		return err
	}
	g.paused[member] = true
	return nil
}

func (g *clusterGuard) resume(member int) error {
	if !g.paused[member] {
		return nil
	}
	if err := cluster.ResumeMember(member); err != nil {
		return err
	}
	g.paused[member] = false
	return nil
}

func (g *clusterGuard) stop(member int) error {
	if g.stopped[member] {
		return nil
	}
	if err := cluster.StopMember(member); err != nil {
		return err
	}
	g.stopped[member] = true
	return nil
}

func (g *clusterGuard) start(member int) error {
	if !g.stopped[member] {
		return nil
	}
	if err := cluster.StartMember(member); err != nil {
		return err
	}
	g.stopped[member] = false
	return nil
}

func (g *clusterGuard) restoreAndWait(t *testing.T) {
	t.Helper()
	cluster.HealPartition()
	for i := range etcdtest.Members {
		if err := g.resume(i); err != nil {
			t.Errorf("resume member %d: %v", i, err)
		}
		if err := g.start(i); err != nil {
			t.Errorf("start member %d: %v", i, err)
		}
	}
	waitAllMembersHealthy(t)
}

func waitAllMembersHealthy(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for i, ep := range cluster.Endpoints {
		cl, err := clientv3.New(clientv3.Config{
			Endpoints:   []string{ep},
			DialTimeout: 5 * time.Second,
			Logger:      zap.NewNop(),
		})
		if err != nil {
			t.Fatalf("client for member %d: %v", i, err)
		}
		s := etcdstore.New(cl)
		var lastErr error
		healthy := false
		for ctx.Err() == nil {
			callCtx, callCancel := context.WithTimeout(ctx, 2*time.Second)
			_, err := s.CurrentRev(callCtx)
			callCancel()
			if err == nil {
				healthy = true
				break
			}
			lastErr = err
			if sleepContext(ctx, 100*time.Millisecond) != nil {
				break
			}
		}
		_ = cl.Close()
		if !healthy {
			t.Fatalf("member %d did not answer linearizable CurrentRev within 30s: %v", i, lastErr)
		}
	}
}

type majorityLoss struct {
	name     string
	holdTime time.Duration
	lose     func(g *clusterGuard) error
	restore  func(g *clusterGuard) error
}

var majorityLosses = []majorityLoss{
	{
		name:     "pause two",
		holdTime: 2 * time.Second,
		lose: func(g *clusterGuard) error {
			if err := g.pause(0); err != nil {
				return err
			}
			return g.pause(1)
		},
		restore: func(g *clusterGuard) error {
			if err := g.resume(0); err != nil {
				return err
			}
			return g.resume(1)
		},
	},
	{
		name:     "stop two",
		holdTime: time.Second,
		lose: func(g *clusterGuard) error {
			if err := g.stop(0); err != nil {
				return err
			}
			return g.stop(1)
		},
		restore: func(g *clusterGuard) error {
			if err := g.start(0); err != nil {
				return err
			}
			return g.start(1)
		},
	},
}

type targetRole struct {
	name       string
	pickMember func(leader int) int
}

var targetRoles = []targetRole{
	{
		name:       "follower",
		pickMember: func(leader int) int { return (leader + 1) % etcdtest.Members },
	},
	{
		name:       "leader",
		pickMember: func(leader int) int { return leader },
	},
}

func TestLinearizabilityAcrossMajorityLossAndPartitions(t *testing.T) {
	type linCase struct {
		name    string
		seed    uint64
		disrupt func(ctx context.Context, guard *clusterGuard) error
	}
	cases := []linCase{
		{
			name: "isolate follower",
			seed: 1,
			disrupt: func(ctx context.Context, _ *clusterGuard) error {
				leader, err := waitLeader(ctx, cluster)
				if err != nil {
					return err
				}
				cluster.IsolateMember((leader + 1) % etcdtest.Members)
				if err := sleepContext(ctx, 1500*time.Millisecond); err != nil {
					return err
				}
				cluster.HealPartition()
				_, err = waitLeader(ctx, cluster)
				return err
			},
		},
		{
			name: "isolate leader",
			seed: 2,
			disrupt: func(ctx context.Context, _ *clusterGuard) error {
				leader, err := waitLeader(ctx, cluster)
				if err != nil {
					return err
				}
				cluster.IsolateMember(leader)
				if _, err := waitOtherLeader(ctx, cluster, leader); err != nil {
					return err
				}
				if err := sleepContext(ctx, time.Second); err != nil {
					return err
				}
				cluster.HealPartition()
				_, err = waitLeader(ctx, cluster)
				return err
			},
		},
	}
	for i, loss := range majorityLosses {
		cases = append(cases, linCase{
			name: loss.name,
			seed: uint64(i + 3),
			disrupt: func(ctx context.Context, guard *clusterGuard) error {
				if err := loss.lose(guard); err != nil {
					return err
				}
				if err := sleepContext(ctx, loss.holdTime); err != nil {
					return err
				}
				if err := loss.restore(guard); err != nil {
					return err
				}
				_, err := waitLeader(ctx, cluster)
				return err
			},
		})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard := newClusterGuard(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			w := storetest.Workload{
				Clients: 4,
				Ops:     40,
				Seed:    tc.seed,
				Disrupt: func(dCtx context.Context) error {
					if err := sleepContext(dCtx, 300*time.Millisecond); err != nil {
						return err
					}
					return tc.disrupt(dCtx, guard)
				},
			}
			if err := storetest.CheckLinearizable(ctx, isolated(t), w); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMajorityLossRejectsStrongOperations(t *testing.T) {
	for _, tc := range majorityLosses {
		t.Run(tc.name, func(t *testing.T) {
			guard := newClusterGuard(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			prefix, offset := allocScope(t)
			s := storetest.Scope(etcdstore.New(client), prefix, offset)
			survivor := storetest.Scope(etcdstore.New(newEndpointClient(t, cluster.Endpoints[2])), prefix, offset)
			if _, err := survivor.CurrentRev(ctx); err != nil {
				t.Fatalf("warm survivor connection: %v", err)
			}

			const key = "k"
			ok, modRev, err := s.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: []byte("v1")}})
			if err != nil || !ok {
				t.Fatalf("pre-loss Txn = (%v, %d, %v), want (true, _, nil)", ok, modRev, err)
			}

			if err := tc.lose(guard); err != nil {
				t.Fatal(err)
			}

			confirmCtx, confirmCancel := context.WithTimeout(ctx, 10*time.Second)
			defer confirmCancel()
			confirmed := false
			for confirmCtx.Err() == nil {
				callCtx, callCancel := context.WithTimeout(confirmCtx, time.Second)
				_, _, err := survivor.Txn(callCtx, nil, []store.Op{{Type: store.OpPut, Key: "confirm-loss", Value: []byte("x")}})
				callCancel()
				if err != nil {
					confirmed = true
					break
				}
				if sleepContext(confirmCtx, 50*time.Millisecond) != nil {
					break
				}
			}
			if !confirmed {
				t.Fatal("majority loss was not observed within 10s on survivor")
			}

			callCtx, callCancel := context.WithTimeout(ctx, 2*time.Second)
			if _, _, err := survivor.Txn(callCtx, nil, []store.Op{{Type: store.OpPut, Key: "during-loss", Value: []byte("x")}}); err == nil {
				callCancel()
				t.Fatal("unconditional Txn succeeded during majority loss")
			}
			callCancel()

			callCtx, callCancel = context.WithTimeout(ctx, 2*time.Second)
			if _, _, err := survivor.Get(callCtx, key, 0); err == nil {
				callCancel()
				t.Fatal("Get(atRev=0) succeeded during majority loss")
			}
			callCancel()

			callCtx, callCancel = context.WithTimeout(ctx, 2*time.Second)
			if _, _, _, err := survivor.Range(callCtx, "", 0, 0, ""); err == nil {
				callCancel()
				t.Fatal("Range(atRev=0) succeeded during majority loss")
			}
			callCancel()

			callCtx, callCancel = context.WithTimeout(ctx, 2*time.Second)
			if _, err := survivor.CurrentRev(callCtx); err == nil {
				callCancel()
				t.Fatal("CurrentRev succeeded during majority loss")
			}
			callCancel()

			callCtx, callCancel = context.WithTimeout(ctx, 2*time.Second)
			if _, _, err := survivor.Txn(callCtx, []store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: modRev}}, nil); err == nil {
				callCancel()
				t.Fatal("compare-only Txn succeeded during majority loss")
			}
			callCancel()

			if err := tc.restore(guard); err != nil {
				t.Fatal(err)
			}

			pollCtx, pollCancel := context.WithTimeout(ctx, 30*time.Second)
			defer pollCancel()
			var lastErr error
			recovered := false
			for pollCtx.Err() == nil {
				txnCtx, txnCancel := context.WithTimeout(pollCtx, 2*time.Second)
				ok, _, err := s.Txn(txnCtx, nil, []store.Op{{Type: store.OpPut, Key: "after-recovery", Value: []byte("ok")}})
				txnCancel()
				if err == nil && ok {
					recovered = true
					break
				}
				lastErr = err
				if sleepContext(pollCtx, 100*time.Millisecond) != nil {
					break
				}
			}
			if !recovered {
				t.Fatalf("Txn did not succeed within 30s after restoring majority: %v", lastErr)
			}

			kv, _, err := s.Get(ctx, key, 0)
			if err != nil {
				t.Fatalf("Get after recovery: %v", err)
			}
			if string(kv.Value) != "v1" || kv.ModRev != modRev {
				t.Fatalf("post-recovery Get(%q) = (%q, mod=%d), want (\"v1\", mod=%d)", key, kv.Value, kv.ModRev, modRev)
			}
		})
	}
}

func TestIsolatedMemberDoesNotServeStaleState(t *testing.T) {
	for _, tc := range targetRoles {
		t.Run(tc.name, func(t *testing.T) {
			_ = newClusterGuard(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			leader, err := waitLeader(ctx, cluster)
			if err != nil {
				t.Fatal(err)
			}
			member := tc.pickMember(leader)

			prefix, offset := allocScope(t)
			pinnedRaw, majorityRaw := splitStores(t, member)
			pinned := storetest.Scope(pinnedRaw, prefix, offset)
			majority := storetest.Scope(majorityRaw, prefix, offset)

			const watchPrefix = "p/"
			const key = watchPrefix + "k"
			ok, r1, err := majority.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: []byte("v1")}})
			if err != nil || !ok {
				t.Fatalf("write v1: Txn = (%v, %d, %v), want (true, _, nil)", ok, r1, err)
			}

			kv, rev, err := pinned.Get(ctx, key, 0)
			if err != nil || rev < r1 || string(kv.Value) != "v1" || kv.ModRev != r1 {
				t.Fatalf("pinned pre-partition Get(%q) = ({%q@%d}, rev=%d, %v), want v1@%d", key, kv.Value, kv.ModRev, rev, err, r1)
			}

			cluster.IsolateMember(member)

			writeCtx, writeCancel := context.WithTimeout(ctx, 20*time.Second)
			defer writeCancel()
			var r2 store.Rev
			var writeErr error
			for writeCtx.Err() == nil {
				callCtx, callCancel := context.WithTimeout(writeCtx, 2*time.Second)
				ok, rev, err := majority.Txn(callCtx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: []byte("v2")}})
				callCancel()
				if err == nil && ok {
					r2 = rev
					break
				}
				writeErr = err
				if sleepContext(writeCtx, 100*time.Millisecond) != nil {
					break
				}
			}
			if r2 == 0 {
				t.Fatalf("majority write v2 did not succeed within 20s: %v", writeErr)
			}

			type probe struct {
				name  string
				check func(cCtx context.Context) (answered bool, err error)
			}
			probes := []probe{
				{
					name: "Get(atRev=0)",
					check: func(cCtx context.Context) (bool, error) {
						kv, rev, err := pinned.Get(cCtx, key, 0)
						if err != nil {
							if errors.Is(err, store.ErrNotFound) {
								return true, fmt.Errorf("Get(%q) returned ErrNotFound", key)
							}
							return false, err
						}
						if rev < r2 || string(kv.Value) != "v2" || kv.ModRev != r2 {
							return true, fmt.Errorf("Get(%q) = ({%q@%d}, rev=%d), want (v2@%d, rev>=%d)", key, kv.Value, kv.ModRev, rev, r2, r2)
						}
						return true, nil
					},
				},
				{
					name: "Range(atRev=0)",
					check: func(cCtx context.Context) (bool, error) {
						kvs, rev, next, err := pinned.Range(cCtx, watchPrefix, 0, 0, "")
						if err != nil {
							return false, err
						}
						if next != "" || rev < r2 || len(kvs) != 1 || kvs[0].Key != key || string(kvs[0].Value) != "v2" || kvs[0].ModRev != r2 {
							return true, fmt.Errorf("Range = (kvs=%+v, rev=%d, next=%q), want [v2@%d] and rev>=%d", kvs, rev, next, r2, r2)
						}
						return true, nil
					},
				},
				{
					name: "CurrentRev",
					check: func(cCtx context.Context) (bool, error) {
						rev, err := pinned.CurrentRev(cCtx)
						if err != nil {
							return false, err
						}
						if rev < r2 {
							return true, fmt.Errorf("CurrentRev = %d, want >= %d", rev, r2)
						}
						return true, nil
					},
				},
				{
					name: "compare-only Txn",
					check: func(cCtx context.Context) (bool, error) {
						ok, rev, err := pinned.Txn(cCtx, []store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: r1}}, nil)
						if err != nil {
							return false, err
						}
						if ok || rev < r2 {
							return true, fmt.Errorf("compare-only Txn = (%v, %d), want (false, >=%d)", ok, rev, r2)
						}
						return true, nil
					},
				},
				{
					name: "Watch(fromRev=0)",
					check: func(cCtx context.Context) (bool, error) {
						ws, err := pinned.Watch(cCtx, watchPrefix, 0)
						if err != nil {
							return false, err
						}
						reqDone := make(chan struct{})
						var reqWG sync.WaitGroup
						reqWG.Add(1)
						go func() {
							defer reqWG.Done()
							_ = ws.RequestProgress(cCtx)
							ticker := time.NewTicker(100 * time.Millisecond)
							defer ticker.Stop()
							for {
								select {
								case <-reqDone:
									return
								case <-cCtx.Done():
									return
								case <-ticker.C:
									_ = ws.RequestProgress(cCtx)
								}
							}
						}()
						defer func() {
							close(reqDone)
							reqWG.Wait()
						}()
						for {
							select {
							case ev, open := <-ws.Events():
								if !open {
									if ws.Err() == nil {
										return true, errors.New("Watch closed without an error")
									}
									return false, ws.Err()
								}
								if ev.Type == store.EventProgress && ev.Rev >= r2 {
									return true, nil
								}
								return true, fmt.Errorf("Watch delivered %+v (want progress >= %d)", ev, r2)
							case <-cCtx.Done():
								return false, cCtx.Err()
							}
						}
					},
				},
			}

			probeCtx, probeCancel := context.WithTimeout(ctx, 2*time.Second)
			probeErrs := make([]error, len(probes))
			var probeWG sync.WaitGroup
			for i, p := range probes {
				probeWG.Add(1)
				go func() {
					defer probeWG.Done()
					answered, err := p.check(probeCtx)
					if answered || err == nil {
						probeErrs[i] = fmt.Errorf("%s on isolated %s answered instead of failing (err=%v)", p.name, tc.name, err)
					}
				}()
			}
			probeWG.Wait()
			probeCancel()
			for _, err := range probeErrs {
				if err != nil {
					t.Fatal(err)
				}
			}

			cluster.HealPartition()

			healCtx, healCancel := context.WithTimeout(ctx, 20*time.Second)
			defer healCancel()
			for _, p := range probes {
				var lastErr error
				succeeded := false
				for healCtx.Err() == nil {
					cCtx, cCancel := context.WithTimeout(healCtx, 2*time.Second)
					answered, err := p.check(cCtx)
					cCancel()
					if answered {
						if err != nil {
							t.Fatalf("post-heal %s on %s: %v", p.name, tc.name, err)
						}
						succeeded = true
						break
					}
					lastErr = err
					if sleepContext(healCtx, 100*time.Millisecond) != nil {
						break
					}
				}
				if !succeeded {
					t.Fatalf("post-heal %s on %s did not succeed within 20s: %v", p.name, tc.name, lastErr)
				}
			}
		})
	}
}

func TestWatchOnIsolatedMemberEndsAndResumesElsewhere(t *testing.T) {
	for _, tc := range targetRoles {
		t.Run(tc.name, func(t *testing.T) {
			_ = newClusterGuard(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			leader, err := waitLeader(ctx, cluster)
			if err != nil {
				t.Fatal(err)
			}
			member := tc.pickMember(leader)

			prefix, offset := allocScope(t)
			pinnedRaw, majorityRaw := splitStores(t, member)
			pinned := storetest.Scope(pinnedRaw, prefix, offset)
			majority := storetest.Scope(majorityRaw, prefix, offset)

			cur, err := pinned.CurrentRev(ctx)
			if err != nil {
				t.Fatal(err)
			}

			pinnedWatchCtx, pinnedWatchCancel := context.WithCancel(ctx)
			defer pinnedWatchCancel()
			ws, err := pinned.Watch(pinnedWatchCtx, "", cur+1)
			if err != nil {
				t.Fatalf("pinned Watch(fromRev=%d): %v", cur+1, err)
			}

			stopWriter := make(chan struct{})
			writerStopped := false
			acked, unknown, writerDone, writerErrCh := startWatchWriter(ctx, majority, stopWriter, 0)
			defer func() {
				if !writerStopped {
					close(stopWriter)
				}
				<-writerDone
			}()

			var delivered []store.Event
			lastDeliveredRev := cur
			isolateTimer := time.NewTimer(300 * time.Millisecond)
			defer isolateTimer.Stop()
			for partitioned := false; !partitioned; {
				select {
				case <-isolateTimer.C:
					cluster.IsolateMember(member)
					partitioned = true
				case ev, open := <-ws.Events():
					if !open {
						t.Fatalf("pinned watch ended before isolation: %v", ws.Err())
					}
					if ev.Type != store.EventProgress {
						delivered = append(delivered, ev)
						lastDeliveredRev = ev.Rev
					}
				}
			}

			endTimer := time.NewTimer(20 * time.Second)
			defer endTimer.Stop()
			for open := true; open; {
				select {
				case <-endTimer.C:
					t.Fatalf("pinned watch on isolated %s did not end within 20s", tc.name)
				case ev, ok := <-ws.Events():
					if !ok {
						if ws.Err() == nil {
							t.Fatal("pinned watch ended without an error")
						}
						open = false
						continue
					}
					if ev.Type != store.EventProgress {
						delivered = append(delivered, ev)
						lastDeliveredRev = ev.Rev
					}
				}
			}
			pinnedWatchCancel()
			if len(delivered) == 0 {
				t.Fatal("pinned watch delivered no events before ending")
			}

			majWatchCtx, majWatchCancel := context.WithCancel(ctx)
			resumeRev := lastDeliveredRev + 1
			majWS := reopenWatch(t, majWatchCtx, majority, resumeRev)

			close(stopWriter)
			writerStopped = true
			<-writerDone
			if err := <-writerErrCh; err != nil {
				t.Fatalf("writer: %v", err)
			}

			delivered, kvs := syncWatchToRange(t, ctx, majority, majWS, resumeRev, majWatchCancel, delivered, lastDeliveredRev)
			assertWatchDelivery(t, delivered, acked, unknown, kvs)

			cluster.HealPartition()
		})
	}
}
