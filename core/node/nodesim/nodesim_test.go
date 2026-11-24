package nodesim_test

import (
	"testing"
	"testing/synctest"

	"github.com/Kaikei-e/Nayutar/core/node/nodesim"
	"github.com/Kaikei-e/Nayutar/core/sim"
)

// Seeds that fail get appended here and always run whatever the default range is; none have failed yet.
var regressionSeeds []uint64

func TestSimDeterminismAcrossRuns(t *testing.T) {
	seedList, err := sim.ConfigFromEnv(sim.SeedConfig{DefaultCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	seed := seedList[0]
	var firstHash string
	for i := range 100 {
		synctest.Test(t, func(t *testing.T) {
			rep, err := nodesim.RunSeed(seed)
			if err != nil {
				t.Fatalf("seed %d run %d failed: %v", seed, i, err)
			}
			if i == 0 {
				firstHash = rep.TraceHash
				return
			}
			if rep.TraceHash != firstHash {
				t.Fatalf("seed %d run %d trace hash %s != first %s", seed, i, rep.TraceHash, firstHash)
			}
		})
	}
}

func TestSimCampaignZeroLeaks(t *testing.T) {
	cfg := sim.SeedConfig{DefaultCount: 120, RegressionSeeds: regressionSeeds, PackagePath: "./core/node/nodesim/"}
	sim.RunSeeds(t, cfg, "TestSimCampaignZeroLeaks", func(seed uint64) error {
		var err error
		synctest.Test(t, func(t *testing.T) {
			_, err = nodesim.RunSeed(seed)
		})
		return err
	})
}
