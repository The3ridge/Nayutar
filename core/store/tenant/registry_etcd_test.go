//go:build etcd

package tenant_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
)

func TestEtcdRegistry(t *testing.T) {
	checkRegistry(t, isolatedRoot)
}

func TestEtcdFencedContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		return mustFencedAdapter(t, isolatedRoot(t), "acme")
	})
}

func TestEtcdFencedLinearizability(t *testing.T) {
	storetest.RunLinearizability(t, func(t *testing.T) store.Store {
		return mustFencedAdapter(t, isolatedRoot(t), "acme")
	})
}

func TestEtcdDeleteLargeKeys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root := isolatedRoot(t)
	reg := tenant.NewRegistry(root)
	if err := reg.Register(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	v, err := reg.View(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	keyBase := strings.Repeat("k", 30000)
	for i := range 64 {
		key := fmt.Sprintf("%s-%02d", keyBase, i)
		if ok, _, err := v.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: []byte("v")}}); err != nil || !ok {
			t.Fatalf("put %d = (%v, %v)", i, ok, err)
		}
	}
	if err := reg.Delete(ctx, "acme"); err != nil {
		t.Fatalf("Delete(acme): %v", err)
	}
	if kvs, _, _, err := root.Range(ctx, "/t/acme/", 0, 0, ""); err != nil || len(kvs) != 0 {
		t.Fatalf("root Range(/t/acme/) = (%+v, %v), want empty", kvs, err)
	}
	if _, err := reg.Get(ctx, "acme"); !errors.Is(err, tenant.ErrNotRegistered) {
		t.Fatalf("Get(acme) = %v, want ErrNotRegistered", err)
	}
}
