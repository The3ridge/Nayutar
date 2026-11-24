package livesim_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/liveness"
	"github.com/Kaikei-e/Nayutar/core/liveness/detector"
	"github.com/Kaikei-e/Nayutar/core/liveness/hrw"
	"github.com/Kaikei-e/Nayutar/core/liveness/livesim"
	simcore "github.com/Kaikei-e/Nayutar/core/sim"
)

// Seeds that fail get appended here, keyed by test name, and always run whatever the default range is.
var regressionSeeds = map[string][]uint64{
	// Cut before any aggregator is self-suspicious: node probers must be asked even then.
	"TestControlPlanePartition": {177, 1445, 1740},
	// Cut healed just as self-suspicion clears: a round must remember it was opened while self-suspicious.
	"TestSmallClusterControlPlanePartition": {19, 40, 121},
	// Aggregators with fast and slow wall clocks must not exceed the permit budget in recorded time.
	"TestEvictionPermitsAreRateLimited": {6, 14, 31},
}

func seeds(t *testing.T, testName string) []uint64 {
	t.Helper()
	out, err := simcore.ConfigFromEnv(simcore.SeedConfig{DefaultCount: defaultSeedCount, RegressionSeeds: regressionSeeds[testName]})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func failSeed(t *testing.T, testName string, seed uint64, msg string) {
	t.Helper()
	t.Fatalf("seed %d failed: %s\nrerun: NAYUTAR_SIM_SEED=%d go test ./core/liveness/livesim/ -run %s",
		seed, msg, seed, testName)
}

func latencyStats(latencies []int64) (maxLat, p99Lat int64) {
	if len(latencies) == 0 {
		return 0, 0
	}
	sorted := slices.Clone(latencies)
	slices.Sort(sorted)
	maxLat = sorted[len(sorted)-1]
	idx := (99*len(sorted)+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return maxLat, sorted[idx]
}

func snapshotStates(sim *livesim.Sim, nodes []livesim.NodeConfig) map[string]detector.State {
	out := make(map[string]detector.State, len(nodes))
	for _, n := range nodes {
		out[n.Name] = sim.Record(n.Name).State
	}
	return out
}

func snapshotRecords(sim *livesim.Sim, nodes []livesim.NodeConfig) map[string]liveness.Record {
	out := make(map[string]liveness.Record, len(nodes))
	for _, n := range nodes {
		out[n.Name] = sim.Record(n.Name)
	}
	return out
}

func mustReadyBy(t *testing.T, sim *livesim.Sim, cfg livesim.Config, deadline int64, testName string) {
	t.Helper()
	sim.RunUntil(deadline)
	for _, n := range cfg.Nodes {
		if st := sim.Record(n.Name).State; st != detector.StateReady {
			failSeed(t, testName, cfg.Seed, fmt.Sprintf("node %s state = %v at t=%d, want Ready", n.Name, st, deadline))
		}
	}
}

func checkNoFalseNotReady(writes []livesim.Write, readyAt30, finalStates map[string]detector.State) error {
	for _, w := range writes {
		if w.Kind == liveness.ProposeNotReady && w.Outcome == liveness.Committed {
			return fmt.Errorf("committed NotReady write for %s at t=%d by %s", w.Node, w.Time, w.Aggregator)
		}
	}
	for node, st := range readyAt30 {
		if st != detector.StateReady {
			return fmt.Errorf("node %s not Ready at 30s (state=%v)", node, st)
		}
	}
	for node, st := range finalStates {
		if st != detector.StateReady {
			return fmt.Errorf("node %s not Ready at end (state=%v)", node, st)
		}
	}
	return nil
}

func checkStoppedDetection(writes []livesim.Write, stopTimes map[string]int64, maxLatency int64) ([]int64, error) {
	detectedAt := make(map[string]int64, len(stopTimes))
	for _, w := range writes {
		if w.Kind != liveness.ProposeNotReady || w.Outcome != liveness.Committed {
			continue
		}
		stopAt, isStopped := stopTimes[w.Node]
		if !isStopped {
			return nil, fmt.Errorf("false NotReady write for running node %s at t=%d by %s", w.Node, w.Time, w.Aggregator)
		}
		if w.Time < stopAt {
			return nil, fmt.Errorf("NotReady write for %s at t=%d before stop at t=%d", w.Node, w.Time, stopAt)
		}
		if _, seen := detectedAt[w.Node]; !seen {
			detectedAt[w.Node] = w.Time
		}
	}

	nodes := make([]string, 0, len(stopTimes))
	for node := range stopTimes {
		nodes = append(nodes, node)
	}
	slices.Sort(nodes)

	latencies := make([]int64, 0, len(nodes))
	for _, node := range nodes {
		det, ok := detectedAt[node]
		if !ok {
			return nil, fmt.Errorf("stopped node %s was never recorded NotReady", node)
		}
		lat := det - stopTimes[node]
		if lat > maxLatency {
			return nil, fmt.Errorf("stopped node %s detection latency %d ms exceeds %d ms", node, lat, maxLatency)
		}
		latencies = append(latencies, lat)
	}
	return latencies, nil
}

func checkRecoveredToReady(writes []livesim.Write, node string, restartTime, maxLatency int64, finalRec liveness.Record) error {
	if finalRec.State != detector.StateReady {
		return fmt.Errorf("node %s state = %v after recovery, want Ready", node, finalRec.State)
	}
	if finalRec.Evictable {
		return fmt.Errorf("node %s evictable = true after recovery, want false", node)
	}
	for _, w := range writes {
		if w.Node == node && w.Kind == liveness.ProposeReady && w.Outcome == liveness.Committed &&
			w.Time >= restartTime && w.Time <= restartTime+maxLatency {
			return nil
		}
	}
	return fmt.Errorf("node %s did not commit Ready within %d ms of restart at t=%d", node, maxLatency, restartTime)
}

func checkEvictionRateLimits(
	writes []livesim.Write,
	grants []int64,
	stopTimes map[string]int64,
	finalRecs map[string]liveness.Record,
	maxNotReadyLatency, evictionGrace, evictionWindow, skewMax int64,
	limit int,
) ([]int64, error) {
	latencies, err := checkStoppedDetection(writes, stopTimes, maxNotReadyLatency)
	if err != nil {
		return nil, err
	}

	notReadyAt := make(map[string]int64, len(stopTimes))
	var grantSimTimes []int64
	for _, w := range writes {
		if w.Outcome != liveness.Committed {
			continue
		}
		switch w.Kind {
		case liveness.ProposeNotReady:
			if _, seen := notReadyAt[w.Node]; !seen {
				notReadyAt[w.Node] = w.Time
			}
		case liveness.ProposeEvictable:
			if _, isStopped := stopTimes[w.Node]; !isStopped {
				return nil, fmt.Errorf("eviction granted for running node %s at t=%d", w.Node, w.Time)
			}
			nrAt, ok := notReadyAt[w.Node]
			if !ok {
				return nil, fmt.Errorf("eviction granted for %s at t=%d before NotReady", w.Node, w.Time)
			}
			if w.Time-nrAt < evictionGrace {
				return nil, fmt.Errorf("eviction granted for %s at t=%d (%d ms after NotReady at t=%d, < grace %d ms)",
					w.Node, w.Time, w.Time-nrAt, nrAt, evictionGrace)
			}
			grantSimTimes = append(grantSimTimes, w.Time)
		}
	}

	names := make([]string, 0, len(finalRecs))
	for name := range finalRecs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		rec := finalRecs[name]
		if _, isStopped := stopTimes[name]; isStopped {
			if !rec.Evictable {
				return nil, fmt.Errorf("stopped node %s is not evictable at end", name)
			}
		} else if rec.Evictable {
			return nil, fmt.Errorf("running node %s is evictable at end", name)
		}
	}

	for i := 1; i < len(grants); i++ {
		if grants[i] < grants[i-1] {
			return nil, fmt.Errorf("recorded grant stamps decreased in commit order: grants[%d]=%d < grants[%d]=%d",
				i, grants[i], i-1, grants[i-1])
		}
	}
	for i := 0; i+limit < len(grants); i++ {
		if span := grants[i+limit] - grants[i]; span < evictionWindow {
			return nil, fmt.Errorf("more than %d grants in %d ms recorded window (%d ms: %v)", limit, evictionWindow, span, grants[i:i+limit+1])
		}
	}

	slices.Sort(grantSimTimes)
	simWindow := evictionWindow - 2*skewMax
	for i := 0; i+limit < len(grantSimTimes); i++ {
		if span := grantSimTimes[i+limit] - grantSimTimes[i]; span < simWindow {
			return nil, fmt.Errorf("more than %d grants in %d ms simulation window (%d ms)", limit, simWindow, span)
		}
	}

	return latencies, nil
}

func checkMassFailure(
	writes []livesim.Write,
	stopTimes map[string]int64,
	maxLatency, evictionGrace int64,
) ([]int64, error) {
	latencies, err := checkStoppedDetection(writes, stopTimes, maxLatency)
	if err != nil {
		return nil, err
	}
	notReadyAt := make(map[string]int64, len(stopTimes))
	permits := 0
	for _, w := range writes {
		if w.Outcome != liveness.Committed {
			continue
		}
		if w.Kind == liveness.ProposeNotReady {
			if _, seen := notReadyAt[w.Node]; !seen {
				notReadyAt[w.Node] = w.Time
			}
		}
		if w.Kind == liveness.ProposeEvictable {
			permits++
			if w.Suspended {
				return nil, fmt.Errorf("permit granted for %s at t=%d while %s was suspended", w.Node, w.Time, w.Aggregator)
			}
			nrAt, ok := notReadyAt[w.Node]
			if !ok || w.Time-nrAt < evictionGrace {
				return nil, fmt.Errorf("permit granted for %s at t=%d before grace %d ms (NotReady at %d)", w.Node, w.Time, evictionGrace, nrAt)
			}
		}
	}
	if permits == 0 {
		return nil, errors.New("expected at least one eviction permit after grace")
	}
	return latencies, nil
}

func checkMajorityCutDamped(writes []livesim.Write, node string, maxWrites int) (int, error) {
	count := 0
	for _, w := range writes {
		if w.Node == node {
			count++
		}
	}
	if count >= maxWrites {
		return count, fmt.Errorf("node %s had %d writes, want < %d", node, count, maxWrites)
	}
	return count, nil
}

func checkNodeProbeFailures(st livesim.Stats, stopTimes map[string]int64, minReports int) error {
	nodes := make([]string, 0, len(stopTimes))
	for node := range stopTimes {
		nodes = append(nodes, node)
	}
	slices.Sort(nodes)
	for _, node := range nodes {
		if got := st.NodeProbeResultsFailed[node]; got < minReports {
			return fmt.Errorf("stopped node %s had %d failed node-probe reports, want >= %d", node, got, minReports)
		}
	}
	return nil
}

func checkNoSuspendedAggregators(st livesim.Stats, aggs []string) error {
	for _, agg := range aggs {
		if got := st.SuspendedTime[agg]; got > 0 {
			return fmt.Errorf("aggregator %s was suspended for %d ms, want 0", agg, got)
		}
	}
	return nil
}

func checkControlPlanePartition(writes []livesim.Write, finalStates map[string]detector.State, roundsOpened int) error {
	if roundsOpened < 1 {
		return errors.New("no rounds opened during control-plane partition")
	}
	for _, w := range writes {
		if w.Outcome == liveness.Committed && (w.Kind == liveness.ProposeNotReady || w.Kind == liveness.ProposeEvictable) {
			return fmt.Errorf("unexpected write kind=%d for %s at t=%d by %s", w.Kind, w.Node, w.Time, w.Aggregator)
		}
	}
	names := make([]string, 0, len(finalStates))
	for name := range finalStates {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if st := finalStates[name]; st != detector.StateReady {
			return fmt.Errorf("node %s state = %v after healing, want Ready", name, st)
		}
	}
	return nil
}

func checkEvictionsSafety(
	writes []livesim.Write,
	grants []int64,
	stopTimes map[string]int64,
	finalRecs map[string]liveness.Record,
	evictionGrace, evictionWindow, skewMax int64,
	limit int,
) (int, error) {
	latestNotReady := make(map[string]int64, len(stopTimes))
	var grantSimTimes []int64
	for _, w := range writes {
		if w.Outcome != liveness.Committed {
			continue
		}
		switch w.Kind {
		case liveness.ProposeNotReady:
			if _, isStopped := stopTimes[w.Node]; !isStopped {
				return 0, fmt.Errorf("false NotReady write for running node %s at t=%d by %s", w.Node, w.Time, w.Aggregator)
			}
			latestNotReady[w.Node] = w.Time
		case liveness.ProposeEvictable:
			if _, isStopped := stopTimes[w.Node]; !isStopped {
				return 0, fmt.Errorf("eviction granted for running node %s at t=%d", w.Node, w.Time)
			}
			nrAt, ok := latestNotReady[w.Node]
			if !ok {
				return 0, fmt.Errorf("eviction granted for %s at t=%d before NotReady", w.Node, w.Time)
			}
			if w.Time-nrAt < evictionGrace {
				return 0, fmt.Errorf("eviction granted for %s at t=%d (%d ms after latest NotReady at t=%d, < grace %d ms)",
					w.Node, w.Time, w.Time-nrAt, nrAt, evictionGrace)
			}
			grantSimTimes = append(grantSimTimes, w.Time)
		}
	}

	names := make([]string, 0, len(finalRecs))
	for name := range finalRecs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if _, isStopped := stopTimes[name]; !isStopped && finalRecs[name].Evictable {
			return 0, fmt.Errorf("running node %s is evictable at end", name)
		}
	}

	for i := 1; i < len(grants); i++ {
		if grants[i] < grants[i-1] {
			return 0, fmt.Errorf("recorded grant stamps decreased in commit order: grants[%d]=%d < grants[%d]=%d",
				i, grants[i], i-1, grants[i-1])
		}
	}
	for i := 0; i+limit < len(grants); i++ {
		if span := grants[i+limit] - grants[i]; span < evictionWindow {
			return 0, fmt.Errorf("more than %d grants in %d ms recorded window (%d ms: %v)", limit, evictionWindow, span, grants[i:i+limit+1])
		}
	}

	slices.Sort(grantSimTimes)
	simWindow := evictionWindow - 2*skewMax
	for i := 0; i+limit < len(grantSimTimes); i++ {
		if span := grantSimTimes[i+limit] - grantSimTimes[i]; span < simWindow {
			return 0, fmt.Errorf("more than %d grants in %d ms simulation window (%d ms)", limit, simWindow, span)
		}
	}

	return len(grantSimTimes), nil
}

func checkLoneAggregatorOwnership(aggs []string, nodes []livesim.NodeConfig, ownersFn func(string) []string) ([]string, error) {
	counts := make(map[string]int, len(aggs))
	for _, n := range nodes {
		for _, agg := range ownersFn(n.Name) {
			counts[agg]++
		}
	}
	var lone []string
	for _, agg := range aggs {
		if counts[agg] == 1 {
			lone = append(lone, agg)
		}
	}
	slices.Sort(lone)
	if len(lone) == 0 {
		return nil, fmt.Errorf("no aggregator owns exactly 1 node (counts=%v)", counts)
	}
	return lone, nil
}

func checkUnownedNodeProbeRequests(st livesim.Stats, aggs []string) (int, error) {
	total := 0
	for _, agg := range aggs {
		total += st.UnownedNodeProbeRequests[agg]
	}
	if total < 1 {
		return 0, fmt.Errorf("expected >= 1 probe request to unowned node probers from %v, got 0", aggs)
	}
	return total, nil
}

func checkNoEvictionPermits(writes []livesim.Write, grants []int64, finalRecs map[string]liveness.Record) error {
	for _, w := range writes {
		if w.Kind == liveness.ProposeEvictable && w.Outcome == liveness.Committed {
			return fmt.Errorf("unexpected eviction permit write for %s at t=%d by %s", w.Node, w.Time, w.Aggregator)
		}
	}
	if len(grants) > 0 {
		return fmt.Errorf("unexpected %d recorded eviction grants, want 0", len(grants))
	}
	names := make([]string, 0, len(finalRecs))
	for name := range finalRecs {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if finalRecs[name].Evictable {
			return fmt.Errorf("node %s is evictable at end, want false", name)
		}
	}
	return nil
}

func distinctRackTopology(numAggs, numNodes int) ([]string, []livesim.NodeConfig) {
	aggs := make([]string, numAggs)
	for i := range numAggs {
		aggs[i] = fmt.Sprintf("agg-%d", i)
	}
	nodes := make([]livesim.NodeConfig, numNodes)
	for i := range numNodes {
		nodes[i] = livesim.NodeConfig{
			Name: fmt.Sprintf("node-%02d", i),
			Rack: fmt.Sprintf("rack-%d", i),
		}
	}
	return aggs, nodes
}

func setControlPlaneCuts(sim *livesim.Sim, aggs []string, nodes []livesim.NodeConfig, cut bool) {
	for _, agg := range aggs {
		aggAddr := liveness.Addr{ID: agg}
		for _, n := range nodes {
			nAddr := liveness.Addr{ID: n.Name, Node: true}
			if cut {
				sim.Cut(aggAddr, nAddr)
				sim.Cut(nAddr, aggAddr)
			} else {
				sim.Uncut(aggAddr, nAddr)
				sim.Uncut(nAddr, aggAddr)
			}
		}
	}
}

func setNodeAggCuts(sim *livesim.Sim, node string, aggs []string, cut bool) {
	nAddr := liveness.Addr{ID: node, Node: true}
	for _, agg := range aggs {
		aggAddr := liveness.Addr{ID: agg}
		if cut {
			sim.Cut(aggAddr, nAddr)
			sim.Cut(nAddr, aggAddr)
		} else {
			sim.Uncut(aggAddr, nAddr)
			sim.Uncut(nAddr, aggAddr)
		}
	}
}

func TestNoFalseNotReady(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestNoFalseNotReady")
	const duration int64 = 300_000
	totalRounds := 0

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestNoFalseNotReady", seed, err.Error())
		}
		sim.ScheduleChaos(livesim.ChaosConfig{
			MaxFaultyAggregators: 2,
			MinInterval:          1_000,
			MaxInterval:          4_000,
			MinDuration:          2_000,
			MaxDuration:          15_000,
		})
		sim.RunUntil(30_000)
		readyAt30 := snapshotStates(sim, cfg.Nodes)
		sim.RunUntil(duration)
		finalStates := snapshotStates(sim, cfg.Nodes)

		if err := checkNoFalseNotReady(sim.Writes(), readyAt30, finalStates); err != nil {
			failSeed(t, "TestNoFalseNotReady", seed, err.Error())
		}
		for _, n := range cfg.Nodes {
			totalRounds += sim.Stats().RoundsOpened[n.Name]
		}
	}
	if totalRounds == 0 {
		t.Fatalf("expected > 0 rounds opened across %d seeds, got 0", len(seedList))
	}
	t.Logf("TestNoFalseNotReady: seeds=%d total_rounds=%d sim_time_ms=%d", len(seedList), totalRounds, duration)
}

func TestDetectsStoppedNodes(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestDetectsStoppedNodes")
	var allLatencies []int64
	var totalSimTime int64
	confirmations := 0

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestDetectsStoppedNodes", seed, err.Error())
		}
		sim.ScheduleChaos(livesim.ChaosConfig{
			MaxFaultyAggregators: 1,
			MinInterval:          1_000,
			MaxInterval:          4_000,
			MinDuration:          2_000,
			MaxDuration:          12_000,
		})
		mustReadyBy(t, sim, cfg, 20_000, "TestDetectsStoppedNodes")

		r := simcore.NewDetRandStream(seed, 0x73746f70)
		rackPerm := simcore.Perm(r, 3)
		node1 := cfg.Nodes[rackPerm[0]*10+simcore.IntN(r, 10)].Name
		node2 := cfg.Nodes[rackPerm[1]*10+simcore.IntN(r, 10)].Name
		t1 := int64(25_000) + r.Int64N(60_000)
		t2 := int64(25_000) + r.Int64N(60_000)

		type stopEvent struct {
			at   int64
			node string
		}
		evts := []stopEvent{{t1, node1}, {t2, node2}}
		slices.SortFunc(evts, func(a, b stopEvent) int {
			if a.at < b.at {
				return -1
			}
			if a.at > b.at {
				return 1
			}
			return strings.Compare(a.node, b.node)
		})

		stopTimes := make(map[string]int64, 2)
		for _, ev := range evts {
			sim.RunUntil(ev.at)
			sim.StopNode(ev.node)
			stopTimes[ev.node] = ev.at
		}
		endAt := max(t1, t2) + 45_000
		sim.RunUntil(endAt)
		totalSimTime = endAt

		writes := sim.Writes()
		lats, err := checkStoppedDetection(writes, stopTimes, 30_000)
		if err != nil {
			failSeed(t, "TestDetectsStoppedNodes", seed, err.Error())
		}
		for _, w := range writes {
			if w.Kind == liveness.ProposeNotReady && w.Outcome == liveness.Committed {
				confirmations++
			}
		}
		allLatencies = append(allLatencies, lats...)
	}
	maxLat, p99Lat := latencyStats(allLatencies)
	t.Logf("TestDetectsStoppedNodes: seeds=%d notready_confirmations=%d max_latency_ms=%d p99_latency_ms=%d sim_time_ms=%d",
		len(seedList), confirmations, maxLat, p99Lat, totalSimTime)
}

func TestRecoversToReady(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestRecoversToReady")
	var lastSimTime int64

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestRecoversToReady", seed, err.Error())
		}
		mustReadyBy(t, sim, cfg, 20_000, "TestRecoversToReady")

		r := simcore.NewDetRandStream(seed, 0x726563)
		target := cfg.Nodes[simcore.IntN(r, len(cfg.Nodes))].Name
		sim.StopNode(target)

		deadline := sim.Now() + 30_000
		for sim.Now() < deadline && sim.Record(target).State != detector.StateNotReady {
			sim.RunUntil(sim.Now() + 500)
		}
		if sim.Record(target).State != detector.StateNotReady {
			failSeed(t, "TestRecoversToReady", seed, fmt.Sprintf("node %s did not reach NotReady before restart", target))
		}

		restartAt := sim.Now()
		sim.StartNode(target)
		sim.RunUntil(restartAt + 10_000)
		lastSimTime = sim.Now()

		if err := checkRecoveredToReady(sim.Writes(), target, restartAt, 10_000, sim.Record(target)); err != nil {
			failSeed(t, "TestRecoversToReady", seed, err.Error())
		}
	}
	t.Logf("TestRecoversToReady: seeds=%d sim_time_ms=%d", len(seedList), lastSimTime)
}

func TestEvictionPermitsAreRateLimited(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestEvictionPermitsAreRateLimited")
	var mu sync.Mutex
	var allLatencies []int64
	var totalSimTime int64

	for _, seed := range seedList {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			t.Parallel()
			cfg := livesim.DefaultConfig(seed)
			nodes := make([]livesim.NodeConfig, 200)
			for i := range 200 {
				nodes[i] = livesim.NodeConfig{
					Name: fmt.Sprintf("node-%03d", i),
					Rack: fmt.Sprintf("rack-%02d", i/10),
				}
			}
			cfg.Nodes = nodes

			sim, err := livesim.New(cfg)
			if err != nil {
				failSeed(t, "TestEvictionPermitsAreRateLimited", seed, err.Error())
			}
			mustReadyBy(t, sim, cfg, 20_000, "TestEvictionPermitsAreRateLimited")

			r := simcore.NewDetRandStream(seed, 0x65766963)
			rackPerm := simcore.Perm(r, 20)
			stopTimes := make(map[string]int64, 12)
			stopAt := sim.Now()
			for i := range 12 {
				node := cfg.Nodes[rackPerm[i]*10+simcore.IntN(r, 10)].Name
				sim.StopNode(node)
				stopTimes[node] = stopAt
			}

			endAt := stopAt + 15*60_000
			sim.RunUntil(endAt)

			lats, err := checkEvictionRateLimits(
				sim.Writes(),
				sim.Grants(),
				stopTimes,
				snapshotRecords(sim, cfg.Nodes),
				30_000,
				detector.DefaultConfig("a", nil).EvictionGrace,
				liveness.EvictionWindow,
				cfg.SkewMax,
				2,
			)
			if err != nil {
				failSeed(t, "TestEvictionPermitsAreRateLimited", seed, err.Error())
			}
			mu.Lock()
			allLatencies = append(allLatencies, lats...)
			totalSimTime = endAt
			mu.Unlock()
		})
	}
	t.Cleanup(func() {
		maxLat, p99Lat := latencyStats(allLatencies)
		t.Logf("TestEvictionPermitsAreRateLimited: seeds=%d max_latency_ms=%d p99_latency_ms=%d sim_time_ms=%d",
			len(seedList), maxLat, p99Lat, totalSimTime)
	})
}

func TestSelfSuspicionWithoutWitnesses(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestSelfSuspicionWithoutWitnesses")
	var totalSimTime int64

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestSelfSuspicionWithoutWitnesses", seed, err.Error())
		}
		mustReadyBy(t, sim, cfg, 20_000, "TestSelfSuspicionWithoutWitnesses")

		agg := cfg.Aggregators[0]
		aggAddr := liveness.Addr{ID: agg}
		for _, n := range cfg.Nodes {
			nAddr := liveness.Addr{ID: n.Name, Node: true}
			sim.Cut(aggAddr, nAddr)
			sim.Cut(nAddr, aggAddr)
		}

		sawSuspended := false
		cutStart := sim.Now()
		for tms := cutStart + 1_000; tms <= cutStart+120_000; tms += 1_000 {
			sim.RunUntil(tms)
			if sim.Suspended(agg) {
				sawSuspended = true
			}
		}
		if !sawSuspended {
			failSeed(t, "TestSelfSuspicionWithoutWitnesses", seed, fmt.Sprintf("%s never reported Suspended() during 120s node cut", agg))
		}

		for _, n := range cfg.Nodes {
			nAddr := liveness.Addr{ID: n.Name, Node: true}
			sim.Uncut(aggAddr, nAddr)
			sim.Uncut(nAddr, aggAddr)
		}
		sim.RunUntil(sim.Now() + 30_000)
		totalSimTime = sim.Now()

		if sim.Suspended(agg) {
			failSeed(t, "TestSelfSuspicionWithoutWitnesses", seed, fmt.Sprintf("%s remained Suspended() 30s after healing", agg))
		}
		if rounds := sim.Stats().RoundsOpenedByAgg[agg]; rounds < 1 {
			failSeed(t, "TestSelfSuspicionWithoutWitnesses", seed, fmt.Sprintf("%s opened %d rounds, want >= 1", agg, rounds))
		}
		for _, w := range sim.Writes() {
			if w.Kind == liveness.ProposeNotReady && w.Outcome == liveness.Committed {
				failSeed(t, "TestSelfSuspicionWithoutWitnesses", seed, fmt.Sprintf("unexpected NotReady write for %s at t=%d", w.Node, w.Time))
			}
		}
	}
	t.Logf("TestSelfSuspicionWithoutWitnesses: seeds=%d sim_time_ms=%d", len(seedList), totalSimTime)
}

func TestMassFailureIsConfirmedByWitnesses(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestMassFailureIsConfirmedByWitnesses")
	var allLatencies []int64
	var totalSimTime int64
	suspendedSeeds := 0
	confirmations := 0

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestMassFailureIsConfirmedByWitnesses", seed, err.Error())
		}
		mustReadyBy(t, sim, cfg, 20_000, "TestMassFailureIsConfirmedByWitnesses")

		stopAt := sim.Now()
		stopTimes := make(map[string]int64, 10)
		for _, n := range cfg.Nodes {
			if n.Rack == "rack-0" {
				sim.StopNode(n.Name)
				stopTimes[n.Name] = stopAt
			}
		}

		endAt := stopAt + 420_000
		sim.RunUntil(endAt)
		totalSimTime = endAt

		st := sim.Stats()
		for _, agg := range cfg.Aggregators {
			if st.SuspendedTime[agg] > 0 {
				suspendedSeeds++
				break
			}
		}

		writes := sim.Writes()
		lats, err := checkMassFailure(
			writes,
			stopTimes,
			45_000,
			detector.DefaultConfig("a", nil).EvictionGrace,
		)
		if err != nil {
			failSeed(t, "TestMassFailureIsConfirmedByWitnesses", seed, err.Error())
		}
		if err := checkNodeProbeFailures(st, stopTimes, 1); err != nil {
			failSeed(t, "TestMassFailureIsConfirmedByWitnesses", seed, err.Error())
		}
		for _, w := range writes {
			if w.Kind == liveness.ProposeNotReady && w.Outcome == liveness.Committed {
				confirmations++
			}
		}
		allLatencies = append(allLatencies, lats...)
	}
	if suspendedSeeds == 0 {
		t.Fatalf("expected at least 1 seed with a suspended aggregator across %d seeds, got 0", len(seedList))
	}
	maxLat, p99Lat := latencyStats(allLatencies)
	t.Logf("TestMassFailureIsConfirmedByWitnesses: seeds=%d suspended_seeds=%d notready_confirmations=%d max_latency_ms=%d p99_latency_ms=%d sim_time_ms=%d",
		len(seedList), suspendedSeeds, confirmations, maxLat, p99Lat, totalSimTime)
}

func TestControlPlanePartition(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestControlPlanePartition")
	var totalSimTime int64

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestControlPlanePartition", seed, err.Error())
		}
		mustReadyBy(t, sim, cfg, 20_000, "TestControlPlanePartition")

		for _, agg := range cfg.Aggregators {
			aggAddr := liveness.Addr{ID: agg}
			for _, n := range cfg.Nodes {
				nAddr := liveness.Addr{ID: n.Name, Node: true}
				sim.Cut(aggAddr, nAddr)
				sim.Cut(nAddr, aggAddr)
			}
		}
		sim.RunUntil(sim.Now() + 180_000)

		stCut := sim.Stats()
		roundsCut := 0
		for _, agg := range cfg.Aggregators {
			if stCut.RoundsOpenedByAgg[agg] < 1 {
				failSeed(t, "TestControlPlanePartition", seed, fmt.Sprintf("%s opened 0 rounds during cut", agg))
			}
			if stCut.SuspendedTime[agg] <= 0 {
				failSeed(t, "TestControlPlanePartition", seed, fmt.Sprintf("%s spent 0 ms suspended during cut", agg))
			}
			roundsCut += stCut.RoundsOpenedByAgg[agg]
		}

		for _, agg := range cfg.Aggregators {
			aggAddr := liveness.Addr{ID: agg}
			for _, n := range cfg.Nodes {
				nAddr := liveness.Addr{ID: n.Name, Node: true}
				sim.Uncut(aggAddr, nAddr)
				sim.Uncut(nAddr, aggAddr)
			}
		}
		sim.RunUntil(sim.Now() + 30_000)
		totalSimTime = sim.Now()

		if err := checkControlPlanePartition(sim.Writes(), snapshotStates(sim, cfg.Nodes), roundsCut); err != nil {
			failSeed(t, "TestControlPlanePartition", seed, err.Error())
		}
	}
	t.Logf("TestControlPlanePartition: seeds=%d sim_time_ms=%d", len(seedList), totalSimTime)
}

func TestPersistentOneWayPartition(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestPersistentOneWayPartition")
	const simDuration int64 = 3_620_000
	var mu sync.Mutex
	minRounds := -1
	maxRounds := 0

	for _, seed := range seedList {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			t.Parallel()
			cfg := livesim.DefaultConfig(seed)
			sim, err := livesim.New(cfg)
			if err != nil {
				failSeed(t, "TestPersistentOneWayPartition", seed, err.Error())
			}
			mustReadyBy(t, sim, cfg, 20_000, "TestPersistentOneWayPartition")

			target := cfg.Nodes[0].Name
			owners := hrw.Ranked(target, cfg.Aggregators)[:3]
			nAddr := liveness.Addr{ID: target, Node: true}
			sim.Cut(nAddr, liveness.Addr{ID: owners[0]})
			sim.Cut(nAddr, liveness.Addr{ID: owners[1]})

			sim.RunUntil(sim.Now() + 3_600_000)

			for _, w := range sim.Writes() {
				if w.Node == target && w.Kind == liveness.ProposeNotReady && w.Outcome == liveness.Committed {
					failSeed(t, "TestPersistentOneWayPartition", seed,
						fmt.Sprintf("false NotReady for %s at t=%d by %s (owners=%v)", target, w.Time, w.Aggregator, owners))
				}
			}
			if st := sim.Record(target).State; st != detector.StateReady {
				failSeed(t, "TestPersistentOneWayPartition", seed, fmt.Sprintf("node %s state = %v at end, want Ready", target, st))
			}
			st := sim.Stats()
			r0 := st.RoundsOpenedByAggNode[owners[0]][target]
			r1 := st.RoundsOpenedByAggNode[owners[1]][target]
			if r0 < 30 || r1 < 30 {
				failSeed(t, "TestPersistentOneWayPartition", seed,
					fmt.Sprintf("cut owners opened (%d, %d) rounds for %s, want >= 30 each", r0, r1, target))
			}
			mu.Lock()
			for _, r := range []int{r0, r1} {
				if minRounds < 0 || r < minRounds {
					minRounds = r
				}
				if r > maxRounds {
					maxRounds = r
				}
			}
			mu.Unlock()
		})
	}
	t.Cleanup(func() {
		t.Logf("TestPersistentOneWayPartition: seeds=%d min_cut_owner_rounds=%d max_cut_owner_rounds=%d sim_time_ms=%d",
			len(seedList), minRounds, maxRounds, simDuration)
	})
}

func TestOwnersSplitTwoToOne(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestOwnersSplitTwoToOne")
	var totalSimTime int64
	totalRounds := 0
	totalWrites := 0

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		cfg.Aggregators = []string{"agg-0", "agg-1", "agg-2"}
		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestOwnersSplitTwoToOne", seed, err.Error())
		}
		mustReadyBy(t, sim, cfg, 20_000, "TestOwnersSplitTwoToOne")

		a0 := liveness.Addr{ID: "agg-0"}
		a1 := liveness.Addr{ID: "agg-1"}
		a2 := liveness.Addr{ID: "agg-2"}
		sim.Cut(a0, a2)
		sim.Cut(a2, a0)
		sim.Cut(a1, a2)
		sim.Cut(a2, a1)
		for _, n := range cfg.Nodes {
			if n.Rack == "rack-0" {
				nAddr := liveness.Addr{ID: n.Name, Node: true}
				sim.Cut(a0, nAddr)
				sim.Cut(nAddr, a0)
				sim.Cut(a1, nAddr)
				sim.Cut(nAddr, a1)
			}
		}

		sim.RunUntil(sim.Now() + 300_000)
		totalSimTime = sim.Now()

		writes := sim.Writes()
		totalWrites += len(writes)
		for _, w := range writes {
			if w.Kind == liveness.ProposeNotReady && w.Outcome == liveness.Committed {
				failSeed(t, "TestOwnersSplitTwoToOne", seed,
					fmt.Sprintf("false NotReady for %s at t=%d by %s", w.Node, w.Time, w.Aggregator))
			}
		}
		st := sim.Stats()
		affectedRounds := 0
		for _, n := range cfg.Nodes {
			if n.Rack == "rack-0" {
				affectedRounds += st.RoundsOpened[n.Name]
			}
		}
		if affectedRounds < 1 {
			failSeed(t, "TestOwnersSplitTwoToOne", seed, "no round opened for any affected rack-0 node")
		}
		totalRounds += affectedRounds
	}
	t.Logf("TestOwnersSplitTwoToOne: seeds=%d affected_rounds=%d writes=%d sim_time_ms=%d",
		len(seedList), totalRounds, totalWrites, totalSimTime)
}

func TestMajorityCutIsDamped(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestMajorityCutIsDamped")
	maxCount := 0
	totalRounds := 0
	var totalSimTime int64

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestMajorityCutIsDamped", seed, err.Error())
		}
		mustReadyBy(t, sim, cfg, 20_000, "TestMajorityCutIsDamped")

		target := cfg.Nodes[0].Name
		owners := hrw.Ranked(target, cfg.Aggregators)[:3]
		keeper := owners[0]
		nAddr := liveness.Addr{ID: target, Node: true}
		for _, agg := range cfg.Aggregators {
			if agg == keeper {
				continue
			}
			aggAddr := liveness.Addr{ID: agg}
			sim.Cut(aggAddr, nAddr)
			sim.Cut(nAddr, aggAddr)
		}

		sim.RunUntil(sim.Now() + 20*60_000)
		totalSimTime = sim.Now()

		rounds := sim.Stats().RoundsOpened[target]
		if rounds < 1 {
			failSeed(t, "TestMajorityCutIsDamped", seed, fmt.Sprintf("no round opened for %s", target))
		}
		totalRounds += rounds

		count, err := checkMajorityCutDamped(sim.Writes(), target, 60)
		if err != nil {
			failSeed(t, "TestMajorityCutIsDamped", seed, err.Error())
		}
		if count > maxCount {
			maxCount = count
		}
	}
	t.Logf("TestMajorityCutIsDamped: seeds=%d total_rounds=%d max_writes=%d sim_time_ms=%d",
		len(seedList), totalRounds, maxCount, totalSimTime)
}

func TestDeterministic(t *testing.T) {
	t.Parallel()
	runScenario := func(seed uint64) (string, []livesim.Write) {
		cfg := livesim.DefaultConfig(seed)
		sim, err := livesim.New(cfg)
		if err != nil {
			t.Fatalf("New(%d): %v", seed, err)
		}
		sim.ScheduleChaos(livesim.ChaosConfig{
			MaxFaultyAggregators: 1,
			MinInterval:          1_000,
			MaxInterval:          3_000,
			MinDuration:          1_000,
			MaxDuration:          5_000,
		})
		sim.RunUntil(15_000)
		sim.StopNode("node-00")
		sim.RunUntil(35_000)
		sim.StartNode("node-00")
		sim.RunUntil(50_000)
		return sim.TraceHash(), sim.Writes()
	}

	seedList, err := simcore.ConfigFromEnv(simcore.SeedConfig{DefaultCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	s := seedList[0]
	h0, w0 := runScenario(s)
	for run := range 100 {
		h, w := runScenario(s)
		if h != h0 || !slices.Equal(w, w0) {
			failSeed(t, "TestDeterministic", s, fmt.Sprintf("non-deterministic run %d: hash0=%s hash=%s", run, h0, h))
		}
	}
	hDiff, _ := runScenario(s ^ 0x9e3779b97f4a7c15)
	if h0 == hDiff {
		failSeed(t, "TestDeterministic", s, fmt.Sprintf("different seeds produced identical hash %s", h0))
	}
	t.Logf("TestDeterministic: seed=%d runs=100 sim_time_ms=50000", s)
}

func TestRackFailureBelowSelfSuspicion(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestRackFailureBelowSelfSuspicion")
	var allLatencies []int64
	var totalSimTime int64
	confirmations := 0

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		nodes := make([]livesim.NodeConfig, 60)
		for i := range 60 {
			nodes[i] = livesim.NodeConfig{
				Name: fmt.Sprintf("node-%02d", i),
				Rack: fmt.Sprintf("rack-%d", i/10),
			}
		}
		cfg.Nodes = nodes

		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestRackFailureBelowSelfSuspicion", seed, err.Error())
		}
		mustReadyBy(t, sim, cfg, 20_000, "TestRackFailureBelowSelfSuspicion")

		stopAt := sim.Now()
		stopTimes := make(map[string]int64, 10)
		for _, n := range cfg.Nodes {
			if n.Rack == "rack-0" {
				sim.StopNode(n.Name)
				stopTimes[n.Name] = stopAt
			}
		}

		endAt := stopAt + 60_000
		sim.RunUntil(endAt)
		totalSimTime = endAt

		st := sim.Stats()
		if err := checkNoSuspendedAggregators(st, cfg.Aggregators); err != nil {
			failSeed(t, "TestRackFailureBelowSelfSuspicion", seed, err.Error())
		}
		if err := checkNodeProbeFailures(st, stopTimes, 1); err != nil {
			failSeed(t, "TestRackFailureBelowSelfSuspicion", seed, err.Error())
		}

		writes := sim.Writes()
		lats, err := checkStoppedDetection(writes, stopTimes, 45_000)
		if err != nil {
			failSeed(t, "TestRackFailureBelowSelfSuspicion", seed, err.Error())
		}
		for _, w := range writes {
			if w.Kind == liveness.ProposeNotReady && w.Outcome == liveness.Committed {
				confirmations++
			}
		}
		allLatencies = append(allLatencies, lats...)
	}
	maxLat, p99Lat := latencyStats(allLatencies)
	t.Logf("TestRackFailureBelowSelfSuspicion: seeds=%d notready_confirmations=%d max_latency_ms=%d p99_latency_ms=%d sim_time_ms=%d",
		len(seedList), confirmations, maxLat, p99Lat, totalSimTime)
}

func TestSmallClusterControlPlanePartition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		aggs  []string
		nodes []livesim.NodeConfig
	}{
		{
			name: "a_3aggs_2nodes",
			aggs: []string{"agg-0", "agg-1", "agg-2"},
			nodes: []livesim.NodeConfig{
				{Name: "node-00", Rack: "rack-0"},
				{Name: "node-01", Rack: "rack-1"},
			},
		},
		{
			name: "b_5aggs_4nodes",
			aggs: []string{"agg-0", "agg-1", "agg-2", "agg-3", "agg-4"},
			nodes: []livesim.NodeConfig{
				{Name: "node-00", Rack: "rack-0"},
				{Name: "node-01", Rack: "rack-0"},
				{Name: "node-02", Rack: "rack-1"},
				{Name: "node-03", Rack: "rack-1"},
			},
		},
	}

	seedList := seeds(t, "TestSmallClusterControlPlanePartition")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			totalRounds := 0
			var totalSimTime int64
			for _, seed := range seedList {
				cfg := livesim.DefaultConfig(seed)
				cfg.Aggregators = tc.aggs
				cfg.Nodes = tc.nodes
				sim, err := livesim.New(cfg)
				if err != nil {
					failSeed(t, "TestSmallClusterControlPlanePartition", seed, err.Error())
				}
				mustReadyBy(t, sim, cfg, 20_000, "TestSmallClusterControlPlanePartition")

				for _, agg := range cfg.Aggregators {
					aggAddr := liveness.Addr{ID: agg}
					for _, n := range cfg.Nodes {
						nAddr := liveness.Addr{ID: n.Name, Node: true}
						sim.Cut(aggAddr, nAddr)
						sim.Cut(nAddr, aggAddr)
					}
				}
				sim.RunUntil(sim.Now() + 120_000)

				stCut := sim.Stats()
				roundsCut := 0
				for _, agg := range cfg.Aggregators {
					roundsCut += stCut.RoundsOpenedByAgg[agg]
				}

				for _, agg := range cfg.Aggregators {
					aggAddr := liveness.Addr{ID: agg}
					for _, n := range cfg.Nodes {
						nAddr := liveness.Addr{ID: n.Name, Node: true}
						sim.Uncut(aggAddr, nAddr)
						sim.Uncut(nAddr, aggAddr)
					}
				}
				sim.RunUntil(sim.Now() + 30_000)
				totalSimTime = sim.Now()

				if err := checkControlPlanePartition(sim.Writes(), snapshotStates(sim, cfg.Nodes), roundsCut); err != nil {
					failSeed(t, "TestSmallClusterControlPlanePartition", seed, fmt.Sprintf("%s: %v", tc.name, err))
				}
				totalRounds += roundsCut
			}
			t.Logf("TestSmallClusterControlPlanePartition/%s: seeds=%d total_rounds=%d sim_time_ms=%d",
				tc.name, len(seedList), totalRounds, totalSimTime)
		})
	}
}

func TestLoneOwnedNodeControlPlanePartition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		numAggs  int
		numNodes int
	}{
		{name: "a_5aggs_2nodes", numAggs: 5, numNodes: 2},
		{name: "b_7aggs_3nodes", numAggs: 7, numNodes: 3},
	}

	seedList := seeds(t, "TestLoneOwnedNodeControlPlanePartition")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			aggs, nodes := distinctRackTopology(tc.numAggs, tc.numNodes)
			totalRounds := 0
			totalUnownedReqs := 0
			var totalSimTime int64

			for _, seed := range seedList {
				cfg := livesim.DefaultConfig(seed)
				cfg.Aggregators = aggs
				cfg.Nodes = nodes
				sim, err := livesim.New(cfg)
				if err != nil {
					failSeed(t, "TestLoneOwnedNodeControlPlanePartition", seed, err.Error())
				}
				loneAggs, err := checkLoneAggregatorOwnership(cfg.Aggregators, cfg.Nodes, sim.Owners)
				if err != nil {
					failSeed(t, "TestLoneOwnedNodeControlPlanePartition", seed, fmt.Sprintf("%s: %v", tc.name, err))
				}
				mustReadyBy(t, sim, cfg, 20_000, "TestLoneOwnedNodeControlPlanePartition")

				setControlPlaneCuts(sim, cfg.Aggregators, cfg.Nodes, true)
				sim.RunUntil(sim.Now() + 120_000)

				stCut := sim.Stats()
				roundsCut := 0
				for _, agg := range cfg.Aggregators {
					roundsCut += stCut.RoundsOpenedByAgg[agg]
				}
				unownedReqs, err := checkUnownedNodeProbeRequests(stCut, loneAggs)
				if err != nil {
					failSeed(t, "TestLoneOwnedNodeControlPlanePartition", seed, fmt.Sprintf("%s: %v", tc.name, err))
				}

				setControlPlaneCuts(sim, cfg.Aggregators, cfg.Nodes, false)
				sim.RunUntil(sim.Now() + 30_000)
				totalSimTime = sim.Now()

				if err := checkControlPlanePartition(sim.Writes(), snapshotStates(sim, cfg.Nodes), roundsCut); err != nil {
					failSeed(t, "TestLoneOwnedNodeControlPlanePartition", seed, fmt.Sprintf("%s: %v", tc.name, err))
				}
				if err := checkNoEvictionPermits(sim.Writes(), sim.Grants(), snapshotRecords(sim, cfg.Nodes)); err != nil {
					failSeed(t, "TestLoneOwnedNodeControlPlanePartition", seed, fmt.Sprintf("%s: %v", tc.name, err))
				}
				totalRounds += roundsCut
				totalUnownedReqs += unownedReqs
			}
			t.Logf("TestLoneOwnedNodeControlPlanePartition/%s: seeds=%d total_rounds=%d unowned_probe_reqs=%d sim_time_ms=%d",
				tc.name, len(seedList), totalRounds, totalUnownedReqs, totalSimTime)
		})
	}
}

func TestLoneOwnedNodeCutFromAllAggregators(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestLoneOwnedNodeCutFromAllAggregators")
	aggs, nodes := distinctRackTopology(5, 2)
	var allLatencies []int64
	var totalSimTime int64
	totalRounds := 0
	totalUnownedReqs := 0

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		cfg.Aggregators = aggs
		cfg.Nodes = nodes
		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestLoneOwnedNodeCutFromAllAggregators", seed, err.Error())
		}
		loneAggs, err := checkLoneAggregatorOwnership(cfg.Aggregators, cfg.Nodes, sim.Owners)
		if err != nil {
			failSeed(t, "TestLoneOwnedNodeCutFromAllAggregators", seed, err.Error())
		}
		target := ""
		for _, n := range cfg.Nodes {
			if slices.Contains(sim.Owners(n.Name), loneAggs[0]) {
				target = n.Name
				break
			}
		}

		mustReadyBy(t, sim, cfg, 20_000, "TestLoneOwnedNodeCutFromAllAggregators")
		readyAt20 := snapshotStates(sim, cfg.Nodes)

		setNodeAggCuts(sim, target, cfg.Aggregators, true)
		sim.RunUntil(sim.Now() + 120_000)

		stCut := sim.Stats()
		rounds := stCut.RoundsOpened[target]
		if rounds < 1 {
			failSeed(t, "TestLoneOwnedNodeCutFromAllAggregators", seed, fmt.Sprintf("no rounds opened for %s during cut", target))
		}
		unownedReqs, err := checkUnownedNodeProbeRequests(stCut, []string{loneAggs[0]})
		if err != nil {
			failSeed(t, "TestLoneOwnedNodeCutFromAllAggregators", seed, err.Error())
		}

		setNodeAggCuts(sim, target, cfg.Aggregators, false)
		sim.RunUntil(sim.Now() + 30_000)
		if err := checkNoFalseNotReady(sim.Writes(), readyAt20, snapshotStates(sim, cfg.Nodes)); err != nil {
			failSeed(t, "TestLoneOwnedNodeCutFromAllAggregators", seed, err.Error())
		}
		if err := checkNoEvictionPermits(sim.Writes(), sim.Grants(), snapshotRecords(sim, cfg.Nodes)); err != nil {
			failSeed(t, "TestLoneOwnedNodeCutFromAllAggregators", seed, err.Error())
		}

		stopAt := sim.Now()
		sim.StopNode(target)
		sim.RunUntil(stopAt + 40_000)
		totalSimTime = sim.Now()

		lats, err := checkStoppedDetection(sim.Writes(), map[string]int64{target: stopAt}, 30_000)
		if err != nil {
			failSeed(t, "TestLoneOwnedNodeCutFromAllAggregators", seed, err.Error())
		}
		allLatencies = append(allLatencies, lats...)
		totalRounds += rounds
		totalUnownedReqs += unownedReqs
	}
	maxLat, p99Lat := latencyStats(allLatencies)
	t.Logf("TestLoneOwnedNodeCutFromAllAggregators: seeds=%d total_rounds=%d unowned_probe_reqs=%d max_latency_ms=%d p99_latency_ms=%d sim_time_ms=%d",
		len(seedList), totalRounds, totalUnownedReqs, maxLat, p99Lat, totalSimTime)
}

func TestLoneOwnedNodeLongPartitionGrantsNoEviction(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestLoneOwnedNodeLongPartitionGrantsNoEviction")
	aggs, nodes := distinctRackTopology(5, 2)
	totalRounds := 0
	totalUnownedReqs := 0
	var totalSimTime int64

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		cfg.Aggregators = aggs
		cfg.Nodes = nodes
		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestLoneOwnedNodeLongPartitionGrantsNoEviction", seed, err.Error())
		}
		loneAggs, err := checkLoneAggregatorOwnership(cfg.Aggregators, cfg.Nodes, sim.Owners)
		if err != nil {
			failSeed(t, "TestLoneOwnedNodeLongPartitionGrantsNoEviction", seed, err.Error())
		}
		mustReadyBy(t, sim, cfg, 20_000, "TestLoneOwnedNodeLongPartitionGrantsNoEviction")

		setControlPlaneCuts(sim, cfg.Aggregators, cfg.Nodes, true)
		sim.RunUntil(sim.Now() + 420_000)
		totalSimTime = sim.Now()

		stCut := sim.Stats()
		roundsCut := 0
		for _, agg := range cfg.Aggregators {
			roundsCut += stCut.RoundsOpenedByAgg[agg]
		}
		unownedReqs, err := checkUnownedNodeProbeRequests(stCut, loneAggs)
		if err != nil {
			failSeed(t, "TestLoneOwnedNodeLongPartitionGrantsNoEviction", seed, err.Error())
		}
		if err := checkControlPlanePartition(sim.Writes(), snapshotStates(sim, cfg.Nodes), roundsCut); err != nil {
			failSeed(t, "TestLoneOwnedNodeLongPartitionGrantsNoEviction", seed, err.Error())
		}
		if err := checkNoEvictionPermits(sim.Writes(), sim.Grants(), snapshotRecords(sim, cfg.Nodes)); err != nil {
			failSeed(t, "TestLoneOwnedNodeLongPartitionGrantsNoEviction", seed, err.Error())
		}
		totalRounds += roundsCut
		totalUnownedReqs += unownedReqs
	}
	t.Logf("TestLoneOwnedNodeLongPartitionGrantsNoEviction: seeds=%d total_rounds=%d unowned_probe_reqs=%d sim_time_ms=%d",
		len(seedList), totalRounds, totalUnownedReqs, totalSimTime)
}

func TestSingleRelayUnderLoss(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestSingleRelayUnderLoss")
	aggs, nodes := distinctRackTopology(3, 2)
	totalRounds := 0
	totalFailedProbeResults := 0
	var totalSimTime int64

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		cfg.Aggregators = aggs
		cfg.Nodes = nodes
		cfg.Loss = 0.05
		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestSingleRelayUnderLoss", seed, err.Error())
		}
		mustReadyBy(t, sim, cfg, 20_000, "TestSingleRelayUnderLoss")
		readyAt20 := snapshotStates(sim, cfg.Nodes)

		cutNode := cfg.Nodes[0].Name
		setNodeAggCuts(sim, cutNode, cfg.Aggregators, true)
		sim.RunUntil(sim.Now() + 20*60_000)
		totalSimTime = sim.Now()

		if err := checkNoFalseNotReady(sim.Writes(), readyAt20, snapshotStates(sim, cfg.Nodes)); err != nil {
			failSeed(t, "TestSingleRelayUnderLoss", seed, err.Error())
		}
		st := sim.Stats()
		rounds := st.RoundsOpened[cutNode]
		if rounds < 1 {
			failSeed(t, "TestSingleRelayUnderLoss", seed, fmt.Sprintf("no rounds opened for %s", cutNode))
		}
		totalRounds += rounds
		totalFailedProbeResults += st.NodeProbeResultsFailed[cutNode]
	}
	t.Logf("TestSingleRelayUnderLoss: seeds=%d total_rounds=%d failed_probe_results=%d sim_time_ms=%d",
		len(seedList), totalRounds, totalFailedProbeResults, totalSimTime)
}

func TestCrashAfterHealedCut(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestCrashAfterHealedCut")
	var allLatencies []int64
	var totalSimTime int64

	for _, seed := range seedList {
		cfg := livesim.DefaultConfig(seed)
		sim, err := livesim.New(cfg)
		if err != nil {
			failSeed(t, "TestCrashAfterHealedCut", seed, err.Error())
		}
		mustReadyBy(t, sim, cfg, 20_000, "TestCrashAfterHealedCut")

		r := simcore.NewDetRandStream(seed, 0x6865616c)
		target := cfg.Nodes[simcore.IntN(r, len(cfg.Nodes))].Name
		owners := hrw.Ranked(target, cfg.Aggregators)[:3]
		nAddr := liveness.Addr{ID: target, Node: true}
		for _, owner := range owners {
			sim.Cut(nAddr, liveness.Addr{ID: owner})
		}

		sim.RunUntil(sim.Now() + 190_000)
		for _, w := range sim.Writes() {
			if w.Kind == liveness.ProposeNotReady && w.Outcome == liveness.Committed {
				failSeed(t, "TestCrashAfterHealedCut", seed,
					fmt.Sprintf("unexpected NotReady write for %s at t=%d during one-way cut", w.Node, w.Time))
			}
		}
		if rounds := sim.Stats().RoundsOpened[target]; rounds < 1 {
			failSeed(t, "TestCrashAfterHealedCut", seed, fmt.Sprintf("no rounds opened for %s during cut", target))
		}

		for _, owner := range owners {
			sim.Uncut(nAddr, liveness.Addr{ID: owner})
		}
		sim.RunUntil(sim.Now() + 3_000)

		stopAt := sim.Now()
		sim.StopNode(target)
		sim.RunUntil(stopAt + 40_000)
		totalSimTime = sim.Now()

		lats, err := checkStoppedDetection(sim.Writes(), map[string]int64{target: stopAt}, 30_000)
		if err != nil {
			failSeed(t, "TestCrashAfterHealedCut", seed, err.Error())
		}
		allLatencies = append(allLatencies, lats...)
	}
	maxLat, p99Lat := latencyStats(allLatencies)
	t.Logf("TestCrashAfterHealedCut: seeds=%d max_latency_ms=%d p99_latency_ms=%d sim_time_ms=%d",
		len(seedList), maxLat, p99Lat, totalSimTime)
}

func TestEvictionsUnderChaos(t *testing.T) {
	t.Parallel()
	seedList := seeds(t, "TestEvictionsUnderChaos")
	var mu sync.Mutex
	minPermits := -1
	maxPermits := 0
	var totalSimTime int64

	for _, seed := range seedList {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			t.Parallel()
			cfg := livesim.DefaultConfig(seed)
			nodes := make([]livesim.NodeConfig, 200)
			for i := range 200 {
				nodes[i] = livesim.NodeConfig{
					Name: fmt.Sprintf("node-%03d", i),
					Rack: fmt.Sprintf("rack-%02d", i/10),
				}
			}
			cfg.Nodes = nodes

			sim, err := livesim.New(cfg)
			if err != nil {
				failSeed(t, "TestEvictionsUnderChaos", seed, err.Error())
			}
			sim.ScheduleChaos(livesim.ChaosConfig{
				MaxFaultyAggregators: 1,
				MinInterval:          1_000,
				MaxInterval:          4_000,
				MinDuration:          2_000,
				MaxDuration:          12_000,
			})
			mustReadyBy(t, sim, cfg, 20_000, "TestEvictionsUnderChaos")

			r := simcore.NewDetRandStream(seed, 0x6368616f)
			rackPerm := simcore.Perm(r, 20)
			stopTimes := make(map[string]int64, 12)
			stopAt := sim.Now()
			for i := range 12 {
				node := cfg.Nodes[rackPerm[i]*10+simcore.IntN(r, 10)].Name
				sim.StopNode(node)
				stopTimes[node] = stopAt
			}

			endAt := stopAt + 15*60_000
			sim.RunUntil(endAt)

			permits, err := checkEvictionsSafety(
				sim.Writes(),
				sim.Grants(),
				stopTimes,
				snapshotRecords(sim, cfg.Nodes),
				detector.DefaultConfig("a", nil).EvictionGrace,
				liveness.EvictionWindow,
				cfg.SkewMax,
				2,
			)
			if err != nil {
				failSeed(t, "TestEvictionsUnderChaos", seed, err.Error())
			}
			mu.Lock()
			if minPermits < 0 || permits < minPermits {
				minPermits = permits
			}
			if permits > maxPermits {
				maxPermits = permits
			}
			totalSimTime = endAt
			mu.Unlock()
		})
	}
	t.Cleanup(func() {
		t.Logf("TestEvictionsUnderChaos: seeds=%d min_permits=%d max_permits=%d sim_time_ms=%d",
			len(seedList), minPermits, maxPermits, totalSimTime)
	})
}

func TestLatencyStats(t *testing.T) {
	makeSeq := func(n int) []int64 {
		out := make([]int64, n)
		for i := range n {
			out[i] = int64(i + 1)
		}
		return out
	}
	cases := []struct {
		n       int
		wantMax int64
		wantP99 int64
	}{
		{n: 1, wantMax: 1, wantP99: 1},
		{n: 100, wantMax: 100, wantP99: 99},
		{n: 101, wantMax: 101, wantP99: 100},
		{n: 200, wantMax: 200, wantP99: 198},
	}
	for _, tc := range cases {
		gotMax, gotP99 := latencyStats(makeSeq(tc.n))
		if gotMax != tc.wantMax || gotP99 != tc.wantP99 {
			t.Fatalf("latencyStats(1..%d) = (%d, %d), want (%d, %d)",
				tc.n, gotMax, gotP99, tc.wantMax, tc.wantP99)
		}
	}
}

func TestSelfCutLinkDropsMessages(t *testing.T) {
	cfg := livesim.Config{
		Seed:         1,
		Aggregators:  []string{"agg-0"},
		Nodes:        []livesim.NodeConfig{{Name: "node-00", Rack: "rack-0"}},
		Loss:         0,
		DelayMin:     200,
		DelayMax:     200,
		TickInterval: 10,
		Epoch:        1_700_000_000_000,
		SkewMax:      0,
		PingTimeout:  400,
		PingInterval: 100,
	}
	det := detector.DefaultConfig("agg-0", nil)
	det.ReadyHeartbeats = 1
	cfg.Detector = &det

	// Cut before send.
	sim1, err := livesim.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sim1.Cut(liveness.Addr{ID: "node-00", Node: true}, liveness.Addr{ID: "agg-0"})
	sim1.RunUntil(5_000)
	if st := sim1.Record("node-00").State; st != detector.StatePending {
		t.Fatalf("pre-cut node state = %v, want Pending", st)
	}
	if len(sim1.Writes()) != 0 {
		t.Fatalf("pre-cut writes = %d, want 0", len(sim1.Writes()))
	}

	// Cut while message is in flight (sent at t in [0, 9], scheduled for delivery at t in [200, 209]).
	sim2, err := livesim.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sim2.RunUntil(50)
	sim2.Cut(liveness.Addr{ID: "node-00", Node: true}, liveness.Addr{ID: "agg-0"})
	sim2.RunUntil(5_000)
	if st := sim2.Record("node-00").State; st != detector.StatePending {
		t.Fatalf("in-flight cut node state = %v, want Pending", st)
	}
	if len(sim2.Writes()) != 0 {
		t.Fatalf("in-flight cut writes = %d, want 0", len(sim2.Writes()))
	}
}

func TestSelfStoppedNodeSendsNothing(t *testing.T) {
	cfg := livesim.DefaultConfig(1)
	cfg.Aggregators = []string{"agg-0", "agg-1", "agg-2"}
	cfg.Nodes = []livesim.NodeConfig{
		{Name: "node-00", Rack: "rack-0"},
		{Name: "node-01", Rack: "rack-0"},
		{Name: "node-02", Rack: "rack-1"},
	}
	cfg.Loss = 0

	sim, err := livesim.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sim.StopNode("node-00")
	sim.RunUntil(20_000)
	if st := sim.Record("node-00").State; st != detector.StatePending {
		t.Fatalf("stopped node state = %v, want Pending", st)
	}
	for _, w := range sim.Writes() {
		if w.Node == "node-00" {
			t.Fatalf("unexpected write for stopped node: %+v", w)
		}
	}
	if sim.Record("node-01").State != detector.StateReady || sim.Record("node-02").State != detector.StateReady {
		t.Fatalf("running nodes did not become Ready")
	}

	// Start node-00, wait Ready, then stop again and verify it neither heartbeats nor acks probes.
	sim.StartNode("node-00")
	sim.RunUntil(30_000)
	if st := sim.Record("node-00").State; st != detector.StateReady {
		t.Fatalf("restarted node state = %v, want Ready", st)
	}
	sim.StopNode("node-00")
	sim.RunUntil(50_000)
	if st := sim.Record("node-00").State; st != detector.StateNotReady {
		t.Fatalf("re-stopped node state = %v, want NotReady", st)
	}

	// Stopping sender while an initial heartbeat is in flight drops it at delivery time.
	inFlightCfg := livesim.Config{
		Seed:         1,
		Aggregators:  []string{"agg-0"},
		Nodes:        []livesim.NodeConfig{{Name: "node-00", Rack: "rack-0"}},
		Loss:         0,
		DelayMin:     200,
		DelayMax:     200,
		TickInterval: 10,
		Epoch:        1_700_000_000_000,
		SkewMax:      0,
		PingTimeout:  400,
		PingInterval: 100,
	}
	det := detector.DefaultConfig("agg-0", nil)
	det.ReadyHeartbeats = 1
	inFlightCfg.Detector = &det
	simStop, err := livesim.New(inFlightCfg)
	if err != nil {
		t.Fatal(err)
	}
	simStop.RunUntil(50)
	simStop.StopNode("node-00")
	simStop.RunUntil(1_000)
	if st := simStop.Record("node-00").State; st != detector.StatePending {
		t.Fatalf("in-flight stop node state = %v, want Pending", st)
	}
	if len(simStop.Writes()) != 0 {
		t.Fatalf("in-flight stop writes = %d, want 0", len(simStop.Writes()))
	}
}

func TestSelfLossZeroAllReady(t *testing.T) {
	cfg := livesim.DefaultConfig(1)
	cfg.Loss = 0
	sim, err := livesim.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sim.RunUntil(30_000)
	readyAt30 := snapshotStates(sim, cfg.Nodes)
	sim.RunUntil(120_000)
	finalStates := snapshotStates(sim, cfg.Nodes)
	if err := checkNoFalseNotReady(sim.Writes(), readyAt30, finalStates); err != nil {
		t.Fatalf("loss=0 failed: %v", err)
	}
}

func TestSelfLossOneNeverReady(t *testing.T) {
	cfg := livesim.DefaultConfig(1)
	cfg.Loss = 1.0
	sim, err := livesim.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sim.RunUntil(30_000)
	if len(sim.Writes()) != 0 {
		t.Fatalf("loss=1.0 writes = %d, want 0", len(sim.Writes()))
	}
	for _, n := range cfg.Nodes {
		if st := sim.Record(n.Name).State; st != detector.StatePending {
			t.Fatalf("loss=1.0 node %s state = %v, want Pending", n.Name, st)
		}
	}
}

func TestSelfScenarioCheckersRejectPlantedViolations(t *testing.T) {
	t.Run("false_notready", func(t *testing.T) {
		writes := []livesim.Write{{
			Time:       10_000,
			Aggregator: "agg-0",
			Kind:       liveness.ProposeNotReady,
			Node:       "node-00",
			Outcome:    liveness.Committed,
		}}
		states := map[string]detector.State{"node-00": detector.StateReady}
		if err := checkNoFalseNotReady(writes, states, states); err == nil {
			t.Fatal("expected checkNoFalseNotReady to fail on planted NotReady write")
		}
	})

	t.Run("stopped_running_node_notready", func(t *testing.T) {
		writes := []livesim.Write{
			{Time: 10_000, Aggregator: "agg-0", Kind: liveness.ProposeNotReady, Node: "node-00", Outcome: liveness.Committed},
			{Time: 10_000, Aggregator: "agg-0", Kind: liveness.ProposeNotReady, Node: "node-01", Outcome: liveness.Committed},
		}
		if _, err := checkStoppedDetection(writes, map[string]int64{"node-00": 5_000}, 30_000); err == nil {
			t.Fatal("expected checkStoppedDetection to fail when a running node is marked NotReady")
		}
	})

	t.Run("stopped_late_detection", func(t *testing.T) {
		writes := []livesim.Write{
			{Time: 40_000, Aggregator: "agg-0", Kind: liveness.ProposeNotReady, Node: "node-00", Outcome: liveness.Committed},
		}
		if _, err := checkStoppedDetection(writes, map[string]int64{"node-00": 5_000}, 30_000); err == nil {
			t.Fatal("expected checkStoppedDetection to fail on late detection")
		}
	})

	t.Run("recover_missing_ready", func(t *testing.T) {
		rec := liveness.Record{Node: "node-00", State: detector.StateNotReady}
		if err := checkRecoveredToReady(nil, "node-00", 1_000, 10_000, rec); err == nil {
			t.Fatal("expected checkRecoveredToReady to fail when node is not Ready")
		}
	})

	t.Run("eviction_third_grant_in_window", func(t *testing.T) {
		stopTimes := map[string]int64{"n0": 1_000, "n1": 1_000, "n2": 1_000}
		writes := []livesim.Write{
			{Time: 10_000, Kind: liveness.ProposeNotReady, Node: "n0", Outcome: liveness.Committed},
			{Time: 10_000, Kind: liveness.ProposeNotReady, Node: "n1", Outcome: liveness.Committed},
			{Time: 10_000, Kind: liveness.ProposeNotReady, Node: "n2", Outcome: liveness.Committed},
			{Time: 320_000, Kind: liveness.ProposeEvictable, Node: "n0", Outcome: liveness.Committed, Wall: 1_000_320_000},
			{Time: 325_000, Kind: liveness.ProposeEvictable, Node: "n1", Outcome: liveness.Committed, Wall: 1_000_325_000},
			{Time: 330_000, Kind: liveness.ProposeEvictable, Node: "n2", Outcome: liveness.Committed, Wall: 1_000_330_000},
		}
		grants := []int64{1_000_320_000, 1_000_325_000, 1_000_330_000}
		recs := map[string]liveness.Record{
			"n0": {Node: "n0", State: detector.StateNotReady, Evictable: true},
			"n1": {Node: "n1", State: detector.StateNotReady, Evictable: true},
			"n2": {Node: "n2", State: detector.StateNotReady, Evictable: true},
		}
		if _, err := checkEvictionRateLimits(writes, grants, stopTimes, recs, 30_000, 300_000, 60_000, 500, 2); err == nil {
			t.Fatal("expected checkEvictionRateLimits to fail on third grant inside window")
		}
	})

	t.Run("eviction_before_grace", func(t *testing.T) {
		stopTimes := map[string]int64{"n0": 1_000}
		writes := []livesim.Write{
			{Time: 10_000, Kind: liveness.ProposeNotReady, Node: "n0", Outcome: liveness.Committed},
			{Time: 250_000, Kind: liveness.ProposeEvictable, Node: "n0", Outcome: liveness.Committed, Wall: 1_000_250_000},
		}
		grants := []int64{1_000_250_000}
		recs := map[string]liveness.Record{
			"n0": {Node: "n0", State: detector.StateNotReady, Evictable: true},
		}
		if _, err := checkEvictionRateLimits(writes, grants, stopTimes, recs, 30_000, 300_000, 60_000, 500, 2); err == nil {
			t.Fatal("expected checkEvictionRateLimits to fail on grant before grace")
		}
	})

	t.Run("eviction_decreasing_grants", func(t *testing.T) {
		stopTimes := map[string]int64{"n0": 1_000, "n1": 1_000}
		writes := []livesim.Write{
			{Time: 10_000, Kind: liveness.ProposeNotReady, Node: "n0", Outcome: liveness.Committed},
			{Time: 10_000, Kind: liveness.ProposeNotReady, Node: "n1", Outcome: liveness.Committed},
			{Time: 320_000, Kind: liveness.ProposeEvictable, Node: "n0", Outcome: liveness.Committed, Wall: 1_000_320_000},
			{Time: 385_000, Kind: liveness.ProposeEvictable, Node: "n1", Outcome: liveness.Committed, Wall: 1_000_319_000},
		}
		grants := []int64{1_000_320_000, 1_000_319_000}
		recs := map[string]liveness.Record{
			"n0": {Node: "n0", State: detector.StateNotReady, Evictable: true},
			"n1": {Node: "n1", State: detector.StateNotReady, Evictable: true},
		}
		if _, err := checkEvictionRateLimits(writes, grants, stopTimes, recs, 30_000, 300_000, 60_000, 500, 2); err == nil {
			t.Fatal("expected checkEvictionRateLimits to fail on decreasing recorded grants")
		}
	})

	t.Run("mass_failure_suspended_grant", func(t *testing.T) {
		stopTimes := map[string]int64{"n0": 1_000}
		writes := []livesim.Write{
			{Time: 10_000, Kind: liveness.ProposeNotReady, Node: "n0", Outcome: liveness.Committed},
			{Time: 350_000, Kind: liveness.ProposeEvictable, Node: "n0", Outcome: liveness.Committed, Suspended: true},
		}
		if _, err := checkMassFailure(writes, stopTimes, 45_000, 300_000); err == nil {
			t.Fatal("expected checkMassFailure to fail on grant while suspended")
		}
	})

	t.Run("majority_cut_undamped", func(t *testing.T) {
		writes := make([]livesim.Write, 60)
		for i := range writes {
			writes[i] = livesim.Write{Node: "node-00"}
		}
		if _, err := checkMajorityCutDamped(writes, "node-00", 60); err == nil {
			t.Fatal("expected checkMajorityCutDamped to fail on >= 60 writes")
		}
	})

	t.Run("node_probe_failures_missing", func(t *testing.T) {
		st := livesim.Stats{NodeProbeResultsFailed: map[string]int{"n0": 1}}
		if err := checkNodeProbeFailures(st, map[string]int64{"n0": 100, "n1": 100}, 1); err == nil {
			t.Fatal("expected checkNodeProbeFailures to fail when n1 has 0 reports")
		}
	})

	t.Run("unexpected_suspended_aggregator", func(t *testing.T) {
		st := livesim.Stats{SuspendedTime: map[string]int64{"agg-0": 500}}
		if err := checkNoSuspendedAggregators(st, []string{"agg-0", "agg-1"}); err == nil {
			t.Fatal("expected checkNoSuspendedAggregators to fail when agg-0 was suspended")
		}
	})

	t.Run("control_plane_partition_no_rounds", func(t *testing.T) {
		states := map[string]detector.State{"n0": detector.StateReady}
		if err := checkControlPlanePartition(nil, states, 0); err == nil {
			t.Fatal("expected checkControlPlanePartition to fail when 0 rounds opened")
		}
	})

	t.Run("control_plane_partition_notready_write", func(t *testing.T) {
		writes := []livesim.Write{{Time: 10_000, Kind: liveness.ProposeNotReady, Node: "n0", Outcome: liveness.Committed}}
		states := map[string]detector.State{"n0": detector.StateReady}
		if err := checkControlPlanePartition(writes, states, 1); err == nil {
			t.Fatal("expected checkControlPlanePartition to fail on committed NotReady write")
		}
	})

	t.Run("control_plane_partition_unhealed_node", func(t *testing.T) {
		states := map[string]detector.State{"n0": detector.StateNotReady}
		if err := checkControlPlanePartition(nil, states, 1); err == nil {
			t.Fatal("expected checkControlPlanePartition to fail when node is not Ready at end")
		}
	})

	t.Run("evictions_safety_running_node", func(t *testing.T) {
		writes := []livesim.Write{{Time: 400_000, Kind: liveness.ProposeEvictable, Node: "running", Outcome: liveness.Committed}}
		if _, err := checkEvictionsSafety(writes, nil, map[string]int64{"stopped": 1_000}, nil, 300_000, 60_000, 500, 2); err == nil {
			t.Fatal("expected checkEvictionsSafety to fail on permit for running node")
		}
	})

	t.Run("evictions_safety_before_latest_notready_grace", func(t *testing.T) {
		stopTimes := map[string]int64{"n0": 1_000}
		writes := []livesim.Write{
			{Time: 10_000, Kind: liveness.ProposeNotReady, Node: "n0", Outcome: liveness.Committed},
			{Time: 200_000, Kind: liveness.ProposeNotReady, Node: "n0", Outcome: liveness.Committed},
			{Time: 450_000, Kind: liveness.ProposeEvictable, Node: "n0", Outcome: liveness.Committed, Wall: 1_000_450_000},
		}
		grants := []int64{1_000_450_000}
		if _, err := checkEvictionsSafety(writes, grants, stopTimes, nil, 300_000, 60_000, 500, 2); err == nil {
			t.Fatal("expected checkEvictionsSafety to fail when permit is < grace after latest NotReady")
		}
	})

	t.Run("evictions_safety_third_grant_in_window", func(t *testing.T) {
		stopTimes := map[string]int64{"n0": 1_000, "n1": 1_000, "n2": 1_000}
		writes := []livesim.Write{
			{Time: 10_000, Kind: liveness.ProposeNotReady, Node: "n0", Outcome: liveness.Committed},
			{Time: 10_000, Kind: liveness.ProposeNotReady, Node: "n1", Outcome: liveness.Committed},
			{Time: 10_000, Kind: liveness.ProposeNotReady, Node: "n2", Outcome: liveness.Committed},
			{Time: 320_000, Kind: liveness.ProposeEvictable, Node: "n0", Outcome: liveness.Committed, Wall: 1_000_320_000},
			{Time: 325_000, Kind: liveness.ProposeEvictable, Node: "n1", Outcome: liveness.Committed, Wall: 1_000_325_000},
			{Time: 330_000, Kind: liveness.ProposeEvictable, Node: "n2", Outcome: liveness.Committed, Wall: 1_000_330_000},
		}
		grants := []int64{1_000_320_000, 1_000_325_000, 1_000_330_000}
		if _, err := checkEvictionsSafety(writes, grants, stopTimes, nil, 300_000, 60_000, 500, 2); err == nil {
			t.Fatal("expected checkEvictionsSafety to fail on third grant inside window")
		}
	})

	t.Run("no_lone_aggregator_ownership", func(t *testing.T) {
		aggs := []string{"agg-0", "agg-1", "agg-2"}
		nodes := []livesim.NodeConfig{{Name: "n0", Rack: "r0"}, {Name: "n1", Rack: "r1"}}
		ownersFn := func(string) []string { return aggs }
		if _, err := checkLoneAggregatorOwnership(aggs, nodes, ownersFn); err == nil {
			t.Fatal("expected checkLoneAggregatorOwnership to fail when no aggregator owns 1 node")
		}
	})

	t.Run("missing_unowned_node_probe_requests", func(t *testing.T) {
		st := livesim.Stats{UnownedNodeProbeRequests: map[string]int{"agg-0": 0}}
		if _, err := checkUnownedNodeProbeRequests(st, []string{"agg-0"}); err == nil {
			t.Fatal("expected checkUnownedNodeProbeRequests to fail when 0 unowned requests were sent")
		}
	})

	t.Run("no_eviction_permits_committed_write", func(t *testing.T) {
		writes := []livesim.Write{{Time: 400_000, Kind: liveness.ProposeEvictable, Node: "n0", Outcome: liveness.Committed}}
		if err := checkNoEvictionPermits(writes, nil, nil); err == nil {
			t.Fatal("expected checkNoEvictionPermits to fail on committed eviction write")
		}
	})

	t.Run("no_eviction_permits_recorded_grant", func(t *testing.T) {
		if err := checkNoEvictionPermits(nil, []int64{1_000_400_000}, nil); err == nil {
			t.Fatal("expected checkNoEvictionPermits to fail on non-empty grants")
		}
	})

	t.Run("no_eviction_permits_evictable_record", func(t *testing.T) {
		recs := map[string]liveness.Record{"n0": {Node: "n0", Evictable: true}}
		if err := checkNoEvictionPermits(nil, nil, recs); err == nil {
			t.Fatal("expected checkNoEvictionPermits to fail when a node record is Evictable")
		}
	})
}
