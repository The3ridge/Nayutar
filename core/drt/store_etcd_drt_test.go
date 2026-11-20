//go:build drt && etcd

package drt

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore/etcdtest"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
)

func TestEtcdStoreMatchesLeanModel(t *testing.T) {
	cluster, err := etcdtest.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	versions, err := cluster.Versions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != etcdtest.Members {
		t.Fatalf("got %d member versions, want %d", len(versions), etcdtest.Members)
	}
	for i, version := range versions {
		if !strings.HasPrefix(version, "3.6.") {
			t.Fatalf("member %d version = %s, want 3.6.x", i, version)
		}
	}
	client, err := cluster.Client()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	inner := etcdstore.New(client)

	// Each case gets its own key prefix on the shared cluster. The write moves
	// the revision past any compaction of an earlier case, and the revision it
	// returns becomes the model's revision 1.
	checkStoreAgainstLean(t, "TestEtcdStoreMatchesLeanModel", func(t *testing.T, id int) store.Store {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, base, err := inner.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "/drt/base"}})
		if err != nil {
			t.Fatalf("id=%d: set the base revision: %v", id, err)
		}
		return storetest.Scope(inner, fmt.Sprintf("/drt/case/%d/", id), base-1)
	})
	t.Logf("etcd cluster versions %v", versions)
}
