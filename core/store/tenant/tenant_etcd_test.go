//go:build etcd

package tenant_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore/etcdtest"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
)

var (
	client *clientv3.Client
	scopes atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	cluster, err := etcdtest.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer cluster.Stop()
	client, err = cluster.Client()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = client.Close() }()
	return m.Run()
}

func isolatedRoot(t *testing.T) store.Store {
	t.Helper()
	inner := etcdstore.New(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bump := []store.Op{{Type: store.OpPut, Key: "/nayutar-test/bump"}}
	_, base, err := inner.Txn(ctx, nil, bump)
	if err != nil {
		t.Fatalf("bump revision: %v", err)
	}
	return storetest.Scope(inner, fmt.Sprintf("/nayutar-test/%d/", scopes.Add(1)), base-1)
}

func TestEtcdIsolation(t *testing.T) {
	checkIsolation(t, isolatedRoot(t))
}

func TestEtcdContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		return mustAdapter(t, isolatedRoot(t), "acme")
	})
}

func TestEtcdLinearizability(t *testing.T) {
	storetest.RunLinearizability(t, func(t *testing.T) store.Store {
		return mustAdapter(t, isolatedRoot(t), "acme")
	})
}
