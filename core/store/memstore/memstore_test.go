package memstore_test

import (
	"testing"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.RunSynctest(t, func(*testing.T) store.Store { return memstore.New() })
}

func TestLinearizability(t *testing.T) {
	storetest.RunLinearizabilitySynctest(t, func(*testing.T) store.Store { return memstore.New() })
}
