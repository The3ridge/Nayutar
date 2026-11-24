package nodesim_test

import (
	"testing"
	"testing/synctest"

	"github.com/Kaikei-e/Nayutar/core/node/nodesim"
)

func TestSimDeterminismAcrossRuns(t *testing.T) {
	const seed uint64 = 20261123
	var firstHash string
	for i := range 20 {
		synctest.Test(t, func(t *testing.T) {
			rep, err := nodesim.RunSeed(seed)
			if err != nil {
				t.Fatalf("run %d failed: %v", i, err)
			}
			if i == 0 {
				firstHash = rep.TraceHash
				return
			}
			if rep.TraceHash != firstHash {
				t.Fatalf("run %d trace hash %s != first %s", i, rep.TraceHash, firstHash)
			}
		})
	}
}

func TestSimCampaignZeroLeaks(t *testing.T) {
	for seed := uint64(1); seed <= 120; seed++ {
		synctest.Test(t, func(t *testing.T) {
			if _, err := nodesim.RunSeed(seed); err != nil {
				t.Fatalf("seed %d violated invariants: %v", seed, err)
			}
		})
	}
}
