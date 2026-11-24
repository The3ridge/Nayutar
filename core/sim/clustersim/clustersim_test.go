package clustersim_test

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/Kaikei-e/Nayutar/core/sim"
	"github.com/Kaikei-e/Nayutar/core/sim/clustersim"
)

// Failing seeds outside the default range are appended here.
var regressionSeeds = []uint64{42}

func testConfig(seed uint64) clustersim.Config {
	cfg := clustersim.DefaultConfig(seed)
	cfg.Settle = synctest.Wait
	return cfg
}

func runSingleSeed(seed uint64, durationMs int64) (string, clustersim.Stats, error) {
	s, err := clustersim.New(testConfig(seed))
	if err != nil {
		return "", clustersim.Stats{}, err
	}
	defer s.Close()

	if err := s.RunUntil(durationMs); err != nil {
		return "", s.Stats(), err
	}
	if err := s.VerifyAllInvariants(); err != nil {
		return "", s.Stats(), err
	}
	if err := s.VerifyConvergence(convergenceMs); err != nil {
		return "", s.Stats(), err
	}
	if err := s.StopAllUnitsAndVerifyTeardown(5_000); err != nil {
		return "", s.Stats(), err
	}
	if err := s.VerifyAllInvariants(); err != nil {
		return "", s.Stats(), err
	}
	return s.TraceHash(), s.Stats(), nil
}

func determinismSeed(t *testing.T) uint64 {
	t.Helper()
	seed := uint64(42)
	if raw := strings.TrimSpace(os.Getenv(sim.EnvSeed)); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			t.Fatalf("invalid %s %q: %v", sim.EnvSeed, raw, err)
		}
		seed = parsed
	}
	return seed
}

func TestDeterminism100Runs(t *testing.T) {
	seed := determinismSeed(t)
	const durationMs int64 = 8_000

	synctest.Test(t, func(t *testing.T) {
		wantHash, stats, err := runSingleSeed(seed, durationMs)
		if err != nil {
			t.Fatalf("seed %d baseline failed: %v", seed, err)
		}
		if stats.ApplySucceeded == 0 || stats.CacheVerifications == 0 || stats.UnitsStarted == 0 ||
			stats.UnitsStopped == 0 || stats.CacheEventsConsumed == 0 {
			t.Fatalf("simulation did not exercise all coupled subsystems: %+v", stats)
		}

		for run := 1; run < 100; run++ {
			gotHash, _, err := runSingleSeed(seed, durationMs)
			if err != nil {
				t.Fatalf("run %d failed: %v", run, err)
			}
			if gotHash != wantHash {
				t.Fatalf("non-deterministic trace hash on run %d for seed %d: got %s, want %s", run, seed, gotHash, wantHash)
			}
		}
		t.Logf("seed=%d runs=100 hash=%s stats=%+v", seed, wantHash[:16], stats)
	})
}

func TestDistinctSeedsDiverge(t *testing.T) {
	seed := determinismSeed(t)
	synctest.Test(t, func(t *testing.T) {
		hash, _, err := runSingleSeed(seed, 8_000)
		if err != nil {
			t.Fatalf("seed %d failed: %v", seed, err)
		}
		otherHash, _, err := runSingleSeed(seed^0x9e3779b97f4a7c15, 8_000)
		if err != nil {
			t.Fatalf("divergent seed failed: %v", err)
		}
		if otherHash == hash {
			t.Fatalf("distinct seeds produced identical trace hash %s", hash)
		}
	})
}

func TestClusterInvariantsAcrossSeeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sim.RunSeeds(t, seedConfig(), "TestClusterInvariantsAcrossSeeds", func(seed uint64) error {
			_, _, err := runSingleSeed(seed, longRunMs)
			return err
		})
	})
}

const (
	longRunMs     int64 = 20_000
	convergenceMs int64 = 3_000
)

func seedConfig() sim.SeedConfig {
	return sim.SeedConfig{
		DefaultCount:    10,
		RegressionSeeds: regressionSeeds,
		PackagePath:     "./core/sim/clustersim",
	}
}

// aggregateSeeds is independent of the seed environment variables so the coverage totals
// stay comparable from run to run.
const aggregateSeeds = 40

func TestFaultInjectionReachesEveryLayer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var total clustersim.Stats
		for seed := uint64(1); seed <= aggregateSeeds; seed++ {
			_, st, err := runSingleSeed(seed, longRunMs)
			if err != nil {
				t.Fatalf("seed %d failed: %v", seed, err)
			}
			total.ApplySucceeded += st.ApplySucceeded
			total.ApplyConflicts += st.ApplyConflicts
			total.PatchOps += st.PatchOps
			total.PatchStatusOps += st.PatchStatusOps
			total.UnitsStarted += st.UnitsStarted
			total.PruneOps += st.PruneOps
			total.ReleaseAdoptOps += st.ReleaseAdoptOps
			total.OrderedSetRejections += st.OrderedSetRejections
			total.PartitionsInjected += st.PartitionsInjected
			total.NodeStopsInjected += st.NodeStopsInjected
			total.LivenessNotReady += st.LivenessNotReady
			total.LivenessEvictions += st.LivenessEvictions
			total.UnitsStoppedByFault += st.UnitsStoppedByFault
			total.CacheEventsConsumed += st.CacheEventsConsumed
			total.CachePauses += st.CachePauses
			total.CacheVerifications += st.CacheVerifications
			total.DeleteOps += st.DeleteOps
			total.StaleTokenOps += st.StaleTokenOps
		}
		t.Logf("aggregate stats over %d seeds: %+v", aggregateSeeds, total)
		for name, c := range map[string]struct{ got, perSeed int }{
			"ApplyConflicts":       {total.ApplyConflicts, 1},
			"PatchOps":             {total.PatchOps, 1},
			"PatchStatusOps":       {total.PatchStatusOps, 1},
			"UnitsStarted":         {total.UnitsStarted, 1},
			"PruneOps":             {total.PruneOps, 1},
			"ReleaseAdoptOps":      {total.ReleaseAdoptOps, 1},
			"OrderedSetRejections": {total.OrderedSetRejections, 1},
			"PartitionsInjected":   {total.PartitionsInjected, 1},
			"NodeStopsInjected":    {total.NodeStopsInjected, 1},
			"CachePauses":          {total.CachePauses, 1},
			"CacheEventsConsumed":  {total.CacheEventsConsumed, 1},
			"DeleteOps":            {total.DeleteOps, 1},
			"StaleTokenOps":        {total.StaleTokenOps, 1},
			"UnitsStoppedByFault":  {total.UnitsStoppedByFault, 1},
			"LivenessNotReady":     {total.LivenessNotReady, 1},
			"LivenessEvictions":    {total.LivenessEvictions, 1},
		} {
			if want := c.perSeed * aggregateSeeds / 8; c.got < want {
				t.Errorf("%s = %d over %d seeds, want at least %d", name, c.got, aggregateSeeds, want)
			}
		}
	})
}

func TestClusterEvictionBudgetAndNodeTeardown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := testConfig(99)
		cfg.EvictionGraceMs = 2_000
		cfg.FaultIntervalMs = 1_000_000 // disable random restarts so stopped nodes stay down through eviction
		s, err := clustersim.New(cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer s.Close()

		if err := s.RunUntil(6_000); err != nil {
			t.Fatalf("warmup RunUntil: %v", err)
		}
		// Budget limit for 4 nodes is 1 eviction per 60s window.
		s.StopNode("node-0")
		s.StopNode("node-1")
		if err := s.RunUntil(22_000); err != nil {
			t.Fatalf("post-stop RunUntil: %v", err)
		}
		if err := s.VerifyAllInvariants(); err != nil {
			t.Fatalf("VerifyAllInvariants: %v", err)
		}
		st := s.Stats()
		if st.LivenessReady < 4 || st.LivenessNotReady < 2 || st.LivenessEvictions != 1 {
			t.Fatalf("unexpected liveness progression under budget limit 1: %+v", st)
		}
		if st.UnitsStoppedByFault == 0 {
			t.Fatalf("node failure did not stop units: %+v", st)
		}
		if err := s.StopAllUnitsAndVerifyTeardown(5_000); err != nil {
			t.Fatalf("StopAllUnitsAndVerifyTeardown: %v", err)
		}
	})
}

const sabotageSeeds = 12

// run drives one seed the way the invariant tests do. With stall set it also stalls the
// cache upstream for a while and verifies the invariants while the stall lasts.
func run(cfg clustersim.Config, stall bool) error {
	s, err := clustersim.New(cfg)
	if err != nil {
		return err
	}
	defer s.Close()
	if stall {
		if err := s.RunUntil(7_000); err != nil {
			return err
		}
		s.PauseCacheUpstream()
		if err := s.RunUntil(9_000); err != nil {
			return err
		}
		if err := s.VerifyAllInvariants(); err != nil {
			return err
		}
	}
	if err := s.RunUntil(longRunMs); err != nil {
		return err
	}
	if err := s.VerifyAllInvariants(); err != nil {
		return err
	}
	return s.VerifyConvergence(convergenceMs)
}

func firstViolation(m clustersim.Mutations, stall bool) (uint64, error) {
	for seed := uint64(1); seed <= sabotageSeeds; seed++ {
		cfg := testConfig(seed)
		cfg.Mutations = m
		if err := run(cfg, stall); err != nil {
			return seed, err
		}
	}
	return 0, nil
}

func TestInvariantsCatchSabotage(t *testing.T) {
	cases := []struct {
		name  string
		m     clustersim.Mutations
		stall bool
	}{
		{"drop cache events for a node", clustersim.Mutations{DropCacheEventsForNode: "node-1"}, false},
		{"skip stop on eviction", clustersim.Mutations{SkipStopOnEviction: true}, false},
		{"skip stop on node stop", clustersim.Mutations{SkipStopOnNodeStop: true}, false},
		{"stop only the first unit on a fault", clustersim.Mutations{StopOnlyFirstUnitOnFault: true}, false},
		{"ignore desired", clustersim.Mutations{IgnoreDesired: true}, false},
		{"ignore updates", clustersim.Mutations{IgnoreUpdates: true}, false},
		{"ignore delete events", clustersim.Mutations{IgnoreDeleteEvents: true}, false},
		{"do not heal faults", clustersim.Mutations{SkipHealFaults: true}, false},
		{"do not resume the upstream", clustersim.Mutations{SkipResumeUpstream: true}, true},
		{"discard events while down", clustersim.Mutations{DiscardEventsWhileDown: true}, false},
		{"do not start units on restart", clustersim.Mutations{SkipStartUnitsOnRestart: true}, false},
		{"never revive stopped units", clustersim.Mutations{NeverReviveStoppedUnits: true}, false},
		{"stale store revision under the cache", clustersim.Mutations{StaleCurrentRev: true}, true},
		{"stale store fallback read", clustersim.Mutations{StaleStoreFallback: true}, true},
		{"reject every owner revision", clustersim.Mutations{RejectEveryOwnerRev: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				seed, err := firstViolation(tc.m, tc.stall)
				if err == nil {
					t.Fatalf("no invariant noticed the mutation over %d seeds", sabotageSeeds)
				}
				t.Logf("caught at seed %d: %v", seed, err)
			})
		})
	}
	t.Run("no mutation", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			for _, stall := range []bool{false, true} {
				if seed, err := firstViolation(clustersim.Mutations{}, stall); err != nil {
					t.Fatalf("seed %d (stall %t) violated an invariant without any mutation: %v", seed, stall, err)
				}
			}
		})
	})
}

func TestConsistentReadFallsBackToStoreWhileUpstreamStalls(t *testing.T) {
	stalled := func(m clustersim.Mutations) (clustersim.Stats, error) {
		var (
			st  clustersim.Stats
			err error
		)
		synctest.Test(t, func(t *testing.T) {
			cfg := testConfig(7)
			cfg.FaultIntervalMs = 1_000_000
			cfg.Mutations = m
			s, newErr := clustersim.New(cfg)
			if newErr != nil {
				t.Fatalf("New: %v", newErr)
			}
			defer s.Close()
			if err = s.RunUntil(4_000); err != nil {
				t.Fatalf("warmup: %v", err)
			}
			s.PauseCacheUpstream()
			if err = s.RunUntil(6_000); err != nil {
				t.Fatalf("stalled run: %v", err)
			}
			st = s.Stats()
			err = s.VerifyAllInvariants()
			if err == nil {
				st = s.Stats()
			}
		})
		return st, err
	}

	st, err := stalled(clustersim.Mutations{})
	if err != nil {
		t.Fatalf("VerifyAllInvariants while stalled: %v", err)
	}
	if st.CacheVerificationsPaused == 0 {
		t.Fatalf("no cache read ran while the upstream was stalled: %+v", st)
	}
	for name, m := range map[string]clustersim.Mutations{
		"stale revision under the cache": {StaleCurrentRev: true},
		"stale store fallback":           {StaleStoreFallback: true},
	} {
		if _, err := stalled(m); err == nil {
			t.Errorf("a consistent read served stale data (%s) during the stall and no invariant noticed", name)
		}
	}
}
