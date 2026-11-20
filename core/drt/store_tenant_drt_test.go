//go:build drt

package drt

import (
	"context"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
)

type tenantStoreAdapter struct {
	tenant.Store
	root store.Store
}

func (a tenantStoreAdapter) Compact(ctx context.Context, rev store.Rev) error {
	return a.root.Compact(ctx, rev)
}

func TestTenantViewMatchesLeanModel(t *testing.T) {
	checkStoreAgainstLean(t, "TestTenantViewMatchesLeanModel", func(t *testing.T, _ int) store.Store {
		root := memstore.New()
		view, err := tenant.View(root, "acme")
		if err != nil {
			t.Fatal(err)
		}
		return tenantStoreAdapter{Store: view, root: root}
	})
}

func TestFencedTenantViewMatchesLeanModel(t *testing.T) {
	checkStoreAgainstLean(t, "TestFencedTenantViewMatchesLeanModel", func(t *testing.T, _ int) store.Store {
		root := memstore.New()
		ctx := context.Background()
		reg := tenant.NewRegistry(root)
		if err := reg.Register(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		base, err := root.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		view, err := reg.View(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		return storetest.Scope(tenantStoreAdapter{Store: view, root: root}, "", base-1)
	})
}
