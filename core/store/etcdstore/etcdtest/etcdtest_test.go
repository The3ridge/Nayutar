//go:build etcd

package etcdtest_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store/etcdstore/etcdtest"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

func startCluster(t *testing.T) *etcdtest.Cluster {
	t.Helper()
	c, err := etcdtest.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	return c
}

func memberClient(t *testing.T, endpoints ...string) *clientv3.Client {
	t.Helper()
	c, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
		Logger:      zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func otherEndpoints(c *etcdtest.Cluster, exclude int) []string {
	eps := make([]string, 0, etcdtest.Members-1)
	for i, ep := range c.Endpoints {
		if i != exclude {
			eps = append(eps, ep)
		}
	}
	return eps
}

func findLeader(t *testing.T, c *etcdtest.Cluster) int {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		leader, err := c.Leader(ctx)
		cancel()
		if err == nil {
			return leader
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("find leader: %v", lastErr)
	return -1
}

func waitMemberRevision(t *testing.T, c *etcdtest.Cluster, member int, want int64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastRev int64
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		rev, err := c.MemberRevision(ctx, member)
		cancel()
		if err == nil {
			lastRev = rev
			lastErr = nil
			if rev >= want {
				return
			}
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("member %d revision did not reach %d within 15s (last rev=%d, err=%v)", member, want, lastRev, lastErr)
}

func TestIsolatedFollowerStopsReceivingWrites(t *testing.T) {
	c := startCluster(t)
	leader := findLeader(t, c)
	follower := (leader + 1) % etcdtest.Members

	allClient, err := c.Client()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = allClient.Close() })

	var preIsolationRev int64
	for i := range 5 {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		resp, err := allClient.Put(ctx, fmt.Sprintf("pre-%d", i), "v")
		cancel()
		if err != nil {
			t.Fatalf("pre-isolation put %d: %v", i, err)
		}
		preIsolationRev = resp.Header.Revision
	}
	waitMemberRevision(t, c, follower, preIsolationRev)

	c.IsolateMember(follower)

	otherClient := memberClient(t, otherEndpoints(c, follower)...)
	var lastRev int64
	writeEnd := time.Now().Add(7 * time.Second)
	for i := 0; time.Now().Before(writeEnd); i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		resp, err := otherClient.Put(ctx, fmt.Sprintf("isolated-%d", i), "v")
		cancel()
		if err != nil {
			t.Fatalf("majority put %d while follower isolated: %v", i, err)
		}
		lastRev = resp.Header.Revision
		time.Sleep(50 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	rev, err := c.MemberRevision(ctx, follower)
	cancel()
	if err != nil {
		t.Fatalf("isolated follower revision: %v", err)
	}
	if rev != preIsolationRev {
		t.Fatalf("isolated follower revision = %d, want %d", rev, preIsolationRev)
	}

	followerClient := memberClient(t, c.Endpoints[follower])
	stCtx, stCancel := context.WithTimeout(context.Background(), 2*time.Second)
	st, err := followerClient.Status(stCtx, c.Endpoints[follower])
	stCancel()
	if err != nil {
		t.Fatalf("isolated follower status: %v", err)
	}
	if st.Leader != 0 {
		t.Fatalf("isolated follower reports leader %x, want 0", st.Leader)
	}

	c.HealPartition()
	waitMemberRevision(t, c, follower, lastRev)
}

func TestIsolatedLeaderIsReplaced(t *testing.T) {
	c := startCluster(t)
	oldLeader := findLeader(t, c)

	leaderClient := memberClient(t, c.Endpoints[oldLeader])
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	status, err := leaderClient.Status(ctx, c.Endpoints[oldLeader])
	cancel()
	if err != nil {
		t.Fatalf("status on leader %d: %v", oldLeader, err)
	}
	oldLeaderID := status.Header.MemberId

	c.IsolateMember(oldLeader)

	others := otherEndpoints(c, oldLeader)
	otherClient := memberClient(t, others...)

	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	elected := false
	for time.Now().Before(deadline) {
		putCtx, putCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := otherClient.Put(putCtx, "failover-probe", "v")
		putCancel()
		if err != nil {
			lastErr = err
			time.Sleep(100 * time.Millisecond)
			continue
		}

		allReported := true
		for _, ep := range others {
			stCtx, stCancel := context.WithTimeout(context.Background(), 2*time.Second)
			st, err := otherClient.Status(stCtx, ep)
			stCancel()
			if err != nil {
				lastErr = err
				allReported = false
				break
			}
			if st.Leader == 0 || st.Leader == oldLeaderID {
				lastErr = fmt.Errorf("endpoint %s still reports leader %x (old=%x)", ep, st.Leader, oldLeaderID)
				allReported = false
				break
			}
		}
		if allReported {
			elected = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !elected {
		t.Fatalf("remaining members did not elect a new leader within 15s: %v", lastErr)
	}

	var lastRev int64
	for i := range 20 {
		putCtx, putCancel := context.WithTimeout(context.Background(), 3*time.Second)
		resp, err := otherClient.Put(putCtx, fmt.Sprintf("post-failover-%d", i), "v")
		putCancel()
		if err != nil {
			t.Fatalf("post-failover put %d: %v", i, err)
		}
		lastRev = resp.Header.Revision
	}

	revCtx, revCancel := context.WithTimeout(context.Background(), 3*time.Second)
	oldLeaderRev, err := c.MemberRevision(revCtx, oldLeader)
	revCancel()
	if err != nil {
		t.Fatalf("isolated old leader revision: %v", err)
	}
	if oldLeaderRev >= lastRev {
		t.Fatalf("isolated old leader revision = %d, want < %d", oldLeaderRev, lastRev)
	}

	c.HealPartition()
	waitMemberRevision(t, c, oldLeader, lastRev)

	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		getCtx, getCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err = leaderClient.Get(getCtx, "\x00", clientv3.WithCountOnly())
		getCancel()
		if err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("linearizable Get on healed member %d did not succeed within 15s: %v", oldLeader, err)
}
