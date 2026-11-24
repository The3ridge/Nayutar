//go:build etcd

package wire_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kaikei-e/LKAL"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore/etcdtest"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache"
	"github.com/Kaikei-e/Nayutar/core/wire"
)

var (
	etcdClient *clientv3.Client
	etcdScopes atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(runEtcdTests(m))
}

func runEtcdTests(m *testing.M) int {
	cluster, err := etcdtest.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer cluster.Stop()
	etcdClient, err = cluster.Client()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = etcdClient.Close() }()
	return m.Run()
}

func isolatedEtcdRoot(t *testing.T) store.Store {
	t.Helper()
	inner := etcdstore.New(etcdClient)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, base, err := inner.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "/nayutar-wire-test/bump"}})
	if err != nil {
		t.Fatalf("bump revision: %v", err)
	}
	return storetest.Scope(inner, fmt.Sprintf("/nayutar-wire-test/%d/", etcdScopes.Add(1)), base-1)
}

func TestEtcdWireHTTP2(t *testing.T) {
	for _, tc := range []struct {
		name   string
		useTLS bool
	}{
		{name: "h2c", useTLS: false},
		{name: "tls_h2", useTLS: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := startTestEnv(t, envOpts{
				root:   isolatedEtcdRoot(t),
				useTLS: tc.useTLS,
				cacheCfg: watchcache.Config{
					MinEvents:        2,
					MaxEvents:        4,
					MinAge:           time.Nanosecond,
					ProgressInterval: 20 * time.Millisecond,
					BookmarkInterval: 200 * time.Millisecond,
					ReadWait:         3 * time.Second,
				},
				maxListLimit: 2,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			created, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "workload", workloadDoc("web", 2))
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if env.proto.Load() != 2 {
				t.Fatalf("expected HTTP/2, got HTTP/%d", env.proto.Load())
			}
			cMeta, _ := getRecord(created, "meta")
			firstRev := store.Rev(getInt(cMeta, "revision"))
			webObj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "web"}

			// WatchList over etcd + live Patch + PatchStatus + Delete.
			wCtx, cancelW := context.WithCancel(ctx)
			defer cancelW()
			ws, err := env.client.Watch(wCtx, "acme", keyspace.CoreGroup, "workload", wire.WatchOptions{
				SendInitialEvents: true,
			})
			if err != nil {
				t.Fatalf("Watch: %v", err)
			}
			defer func() { _ = ws.Close() }()
			if ws.ProtoMajor() != 2 {
				t.Fatalf("WatchStream ProtoMajor = %d, want 2", ws.ProtoMajor())
			}
			for {
				ev, ok := ws.Next()
				if !ok {
					t.Fatalf("watch ended during initial state: %v", ws.Err())
				}
				if ev.Type == wire.EventBookmark && ev.InitialEventsEnd {
					break
				}
			}

			patched, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
				{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(firstRev)},
				{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(5)},
			})
			if err != nil {
				t.Fatalf("Patch: %v", err)
			}
			pMeta, _ := getRecord(patched, "meta")
			if getInt(pMeta, "generation") != 2 {
				t.Fatalf("expected generation 2, got %d", getInt(pMeta, "generation"))
			}

			if _, err := env.client.PatchStatus(ctx, "acme", webObj, []patch.Op{
				{Kind: patch.OpSet, Path: "status/phase", Value: lkal.String("Ready")},
				{Kind: patch.OpSet, Path: "status/observedGeneration", Value: lkal.Int(2)},
			}); err != nil {
				t.Fatalf("PatchStatus: %v", err)
			}

			listRes, err := env.client.List(ctx, "acme", keyspace.CoreGroup, "workload", resource.ListOptions{})
			if err != nil || len(listRes.Items) != 1 {
				t.Fatalf("List: items=%d err=%v", len(listRes.Items), err)
			}

			// Wait for the Patch and PatchStatus Put events on the watch stream.
			puts := 0
			for puts < 2 {
				ev, ok := ws.Next()
				if !ok {
					t.Fatalf("watch ended before live puts: %v", ws.Err())
				}
				if ev.Type == wire.EventPut {
					puts++
				}
			}
			cancelW()

			// Advance beyond MaxEvents and verify 410 Gone on old revision.
			for i := int64(6); i <= 12; i++ {
				if _, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
					{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(i)},
				}); err != nil {
					t.Fatalf("Patch %d: %v", i, err)
				}
			}
			if _, err := env.client.Get(ctx, "acme", webObj, 0); err != nil {
				t.Fatalf("sync Get: %v", err)
			}
			if _, err := env.client.Watch(ctx, "acme", keyspace.CoreGroup, "workload", wire.WatchOptions{FromRev: firstRev}); !errors.Is(err, wire.ErrGone) {
				t.Fatalf("Watch(old rev on etcd) err = %v, want ErrGone", err)
			}

			// Ownership subresources (Apply / Explain / Prune) over 3-member etcd.
			ownObj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "owned"}
			applied, err := env.client.Apply(ctx, "acme", ownObj, rec(
				fld("meta", rec(fld("kind", lkal.String("workload")), fld("name", lkal.String("owned")))),
				fld("spec", rec(fld("payload", lkal.String("v1")))),
			), resource.ApplyOptions{})
			if err != nil || !applied.Created || applied.OwnerRevision < 1 {
				t.Fatalf("Apply(etcd): res=%+v err=%v", applied, err)
			}
			exp, err := env.client.Explain(ctx, "acme", ownObj, 0)
			if err != nil || exp.OwnerRevision != int64(applied.OwnerRevision) {
				t.Fatalf("Explain(etcd): exp=%+v err=%v", exp, err)
			}
			pruned, err := env.client.Prune(ctx, "acme", ownObj, rec(
				fld("meta", rec(fld("kind", lkal.String("workload")), fld("name", lkal.String("owned")))),
				fld("spec", rec()),
			), resource.ApplyOptions{Expect: exp.Expect, ExpectedOwnerRev: applied.OwnerRevision})
			if err != nil {
				t.Fatalf("Prune(etcd): %v", err)
			}
			prunedSpec, _ := getRecord(pruned.Object, "spec")
			if _, exists := getField(prunedSpec, "payload"); exists {
				t.Fatal("Prune(etcd) did not delete retired created leaf")
			}

			if err := env.client.Delete(ctx, "acme", ownObj, 0); err != nil {
				t.Fatalf("Delete(owned): %v", err)
			}
			if err := env.client.Delete(ctx, "acme", webObj, 0); err != nil {
				t.Fatalf("Delete: %v", err)
			}
		})
	}
}
