//go:build etcd

package resource_test

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
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
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

func TestEtcdObjects(t *testing.T) {
	newTenantStore := func(t *testing.T) tenant.Store {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		reg := tenant.NewRegistry(isolatedRoot(t))
		if err := reg.Register(ctx, "acme"); err != nil {
			t.Fatalf("Register(acme): %v", err)
		}
		v, err := reg.View(ctx, "acme")
		if err != nil {
			t.Fatalf("View(acme): %v", err)
		}
		return v
	}
	checkObjectsCore(t, newTenantStore)
	checkObjectsPatchAndList(t, newTenantStore)
	checkObjectsRefsSuite(t, isolatedRoot)
	checkObjectsPatchRefs(t, isolatedRoot)
}
