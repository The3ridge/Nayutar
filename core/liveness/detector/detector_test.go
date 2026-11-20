package detector

import (
	"slices"
	"testing"
)

func fixedRanked(table map[string][]string) func(string) []string {
	return func(node string) []string {
		return table[node]
	}
}

func TestConfigValidateAndNew(t *testing.T) {
	ranked := fixedRanked(nil)
	valid := DefaultConfig("a", ranked)
	if valid.WitnessProbers != 3 {
		t.Fatalf("DefaultConfig.WitnessProbers = %d, want 3", valid.WitnessProbers)
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("DefaultConfig.Validate() = %v, want nil", err)
	}
	if _, err := New(valid); err != nil {
		t.Fatalf("New(valid) = %v, want nil", err)
	}

	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"empty self", func(c *Config) { c.Self = "" }, true},
		{"nil ranked", func(c *Config) { c.Ranked = nil }, true},
		{"owners 0", func(c *Config) { c.Owners = 0 }, true},
		{"owners 1", func(c *Config) { c.Owners = 1 }, false},
		{"peer probers -1", func(c *Config) { c.PeerProbers = -1 }, true},
		{"peer probers 0", func(c *Config) { c.PeerProbers = 0 }, false},
		{"rack probers -1", func(c *Config) { c.RackProbers = -1 }, true},
		{"rack probers 0", func(c *Config) { c.RackProbers = 0 }, false},
		{"witness probers -1", func(c *Config) { c.WitnessProbers = -1 }, true},
		{"witness probers 0", func(c *Config) { c.WitnessProbers = 0 }, false},
		{"min failed probes 0", func(c *Config) { c.MinFailedProbes = 0 }, true},
		{"min failed probes 1", func(c *Config) { c.MinFailedProbes = 1 }, false},
		{"round timeout 0", func(c *Config) { c.RoundTimeout = 0 }, true},
		{"round timeout 1", func(c *Config) { c.RoundTimeout = 1 }, false},
		{"round timeout 600000", func(c *Config) { c.RoundTimeout = 600000 }, false},
		{"round timeout 600001", func(c *Config) { c.RoundTimeout = 600001 }, true},
		{"retry interval 0", func(c *Config) { c.RetryInterval = 0 }, true},
		{"retry interval 1", func(c *Config) { c.RetryInterval = 1 }, false},
		{"retry interval 600000", func(c *Config) { c.RetryInterval = 600000; c.MaxRoundBackoff = 600000 }, false},
		{"retry interval 600001", func(c *Config) { c.RetryInterval = 600001; c.MaxRoundBackoff = 700000 }, true},
		{"max round backoff below retry interval", func(c *Config) { c.MaxRoundBackoff = c.RetryInterval - 1 }, true},
		{"max round backoff equal retry interval", func(c *Config) { c.MaxRoundBackoff = c.RetryInterval }, false},
		{"max round backoff 3600000", func(c *Config) { c.MaxRoundBackoff = 3600000 }, false},
		{"max round backoff 3600001", func(c *Config) { c.MaxRoundBackoff = 3600001 }, true},
		{"eviction retry interval 0", func(c *Config) { c.EvictionRetryInterval = 0 }, true},
		{"eviction retry interval 1", func(c *Config) { c.EvictionRetryInterval = 1 }, false},
		{"eviction retry interval 3600000", func(c *Config) { c.EvictionRetryInterval = 3600000 }, false},
		{"eviction retry interval 3600001", func(c *Config) { c.EvictionRetryInterval = 3600001 }, true},
		{"self suspect percent 0", func(c *Config) { c.SelfSuspectPercent = 0 }, true},
		{"self suspect percent 1", func(c *Config) { c.SelfSuspectPercent = 1 }, false},
		{"self suspect percent 100", func(c *Config) { c.SelfSuspectPercent = 100 }, false},
		{"self suspect percent 101", func(c *Config) { c.SelfSuspectPercent = 101 }, true},
		{"self suspect min nodes 0", func(c *Config) { c.SelfSuspectMinNodes = 0 }, true},
		{"self suspect min nodes 1", func(c *Config) { c.SelfSuspectMinNodes = 1 }, false},
		{"ready heartbeats 0", func(c *Config) { c.ReadyHeartbeats = 0 }, true},
		{"ready heartbeats 1", func(c *Config) { c.ReadyHeartbeats = 1 }, false},
		{"eviction grace -1", func(c *Config) { c.EvictionGrace = -1 }, true},
		{"eviction grace 0", func(c *Config) { c.EvictionGrace = 0 }, false},
		{"round base 0", func(c *Config) { c.RoundBase = 0 }, false},
		{"round base max", func(c *Config) { c.RoundBase = 1 << 52 }, false},
		{"round base above max", func(c *Config) { c.RoundBase = (1 << 52) + 1 }, true},
		{"invalid phi", func(c *Config) { c.Phi.Threshold = 99 }, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid
			tc.mutate(&cfg)
			err := cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr=%v", err, tc.wantErr)
			}
			_, newErr := New(cfg)
			if (newErr != nil) != tc.wantErr {
				t.Fatalf("New() err = %v, wantErr=%v", newErr, tc.wantErr)
			}
		})
	}
}

func TestObservedRulesAndBootstrap(t *testing.T) {
	cfg := DefaultConfig("a", fixedRanked(map[string][]string{
		"n1": {"a", "b", "c"},
		"n2": {"b", "c", "d"}, // not owned by a
		"n3": {"a", "b", "c"},
		"n4": {"a", "b", "c"},
	}))
	cfg.EvictionGrace = 1000
	core, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Rule 2: Pending does not bootstrap; not-owned does not bootstrap; Ready/NotReady bootstrap with heard=false, notReadySince=now.
	core.Observed(100, "n1", Record{State: StatePending, ModRev: 1})
	core.Observed(100, "n2", Record{State: StateReady, ModRev: 1})
	core.Observed(100, "n3", Record{State: StateReady, ModRev: 2})
	core.Observed(100, "n4", Record{State: StateNotReady, ModRev: 3})

	snap := core.Snapshot()
	if len(snap.Nodes) != 2 {
		t.Fatalf("Snapshot nodes = %+v, want n3 and n4 only", snap.Nodes)
	}
	if snap.Nodes[0].Node != "n3" || snap.Nodes[0].Samples != 2 || snap.Nodes[0].Last != 100 ||
		snap.Nodes[0].Streak != 0 || snap.Nodes[0].Heard || snap.Nodes[0].Backoff != 0 {
		t.Fatalf("n3 bootstrap mismatch: %+v", snap.Nodes[0])
	}
	if snap.Nodes[1].Node != "n4" || snap.Nodes[1].Samples != 2 || snap.Nodes[1].Last != 100 ||
		snap.Nodes[1].Streak != 0 || snap.Nodes[1].Heard || snap.Nodes[1].Backoff != 0 {
		t.Fatalf("n4 bootstrap mismatch: %+v", snap.Nodes[1])
	}

	// Bootstrapped NotReady uses notReadySince = 100 (mono bootstrap time), so at 5504 (>= 100+1000) it proposes Evictable.
	acts := core.Tick(5504)
	if !slices.Contains(acts, Action{Kind: ActProposeEvictable, Node: "n4", ModRev: 3}) {
		t.Fatalf("expected bootstrapped NotReady eviction proposal at 5504, got %+v", acts)
	}
	// n3 started round 1 with modRev=2.
	if core.Snapshot().Nodes[0].Round != 1 {
		t.Fatalf("expected active round 1 for n3, got %+v", core.Snapshot().Nodes[0])
	}

	// Rule 3a: Observing Ready with same ModRev preserves active round.
	core.Observed(5505, "n3", Record{State: StateReady, ModRev: 2})
	if core.Snapshot().Nodes[0].Round != 1 {
		t.Fatalf("Observing Ready with same ModRev cleared round: %+v", core.Snapshot().Nodes[0])
	}

	// Rule 3a: Observing Ready with changed ModRev clears active round (nextRoundAt unchanged).
	core.Observed(5506, "n3", Record{State: StateReady, ModRev: 3})
	if core.Snapshot().Nodes[0].Round != 0 {
		t.Fatalf("Observing Ready with new ModRev did not clear round: %+v", core.Snapshot().Nodes[0])
	}
	// Since nextRoundAt is unchanged, next Tick starts round 2 immediately.
	core.Tick(5507)
	if core.Snapshot().Nodes[0].Round != 2 {
		t.Fatalf("expected round 2 immediately after ModRev-cleared round, got %+v", core.Snapshot().Nodes[0])
	}

	// Rule 3a + 3b + 3c: Observing NotReady clears active round, sets notReadySince, and flipping NotReady->Ready while suspect bumps backoff.
	core.Observed(5508, "n3", Record{State: StateNotReady, ModRev: 4})
	if core.Snapshot().Nodes[0].Round != 0 || core.Snapshot().Nodes[0].Backoff != 0 {
		t.Fatalf("Observing NotReady mismatch: %+v", core.Snapshot().Nodes[0])
	}
	// Flipping NotReady -> Ready while still suspect bumps backoff to 1 and delays nextRoundAt to 5509 + 2000 = 7509.
	core.Observed(5509, "n3", Record{State: StateReady, ModRev: 5})
	if core.Snapshot().Nodes[0].Backoff != 1 {
		t.Fatalf("NotReady->Ready while suspect did not bump backoff: %+v", core.Snapshot().Nodes[0])
	}
	if acts := core.Tick(7508); len(acts) != 0 {
		t.Fatalf("Tick at 7508 started round before bumped nextRoundAt=7509: %+v", acts)
	}
	core.Tick(7509)
	if core.Snapshot().Nodes[0].Round != 3 {
		t.Fatalf("expected round 3 at 7509 after bump, got %+v", core.Snapshot().Nodes[0])
	}

	// Rule 3c must NOT bump when window is not suspecting.
	core.Heartbeat(7510, "n3") // backoff resets to 0, window not suspect
	core.Observed(7511, "n3", Record{State: StateNotReady, ModRev: 6})
	core.Observed(7512, "n3", Record{State: StateReady, ModRev: 7})
	if core.Snapshot().Nodes[0].Backoff != 0 {
		t.Fatalf("NotReady->Ready when not suspect should not bump backoff: %+v", core.Snapshot().Nodes[0])
	}
}

func TestStreakResetOnNewNotReady(t *testing.T) {
	cfg := DefaultConfig("a", fixedRanked(map[string][]string{
		"n1": {"a", "b", "c"},
	}))
	cfg.ReadyHeartbeats = 3
	core, _ := New(cfg)

	core.Observed(100, "n1", Record{State: StatePending, ModRev: 1})
	core.Heartbeat(100, "n1")  // streak = 1
	core.Heartbeat(1100, "n1") // streak = 2

	// Transition Pending -> NotReady resets streak = 0 and sets notReadySince = 1200.
	core.Observed(1200, "n1", Record{State: StateNotReady, ModRev: 2})
	if snap := core.Snapshot().Nodes[0]; snap.Streak != 0 || snap.NotReadySince != 1200 {
		t.Fatalf("after new NotReady = %+v, want Streak=0 NotReadySince=1200", snap)
	}
	// Re-observing NotReady with the same ModRev must NOT reset streak or notReadySince.
	core.Heartbeat(2000, "n1") // streak = 1
	core.Heartbeat(3000, "n1") // streak = 2
	core.Observed(3500, "n1", Record{State: StateNotReady, ModRev: 2})
	if snap := core.Snapshot().Nodes[0]; snap.Streak != 2 || snap.NotReadySince != 1200 {
		t.Fatalf("NotReady with same ModRev = %+v, want Streak=2 NotReadySince=1200", snap)
	}
	// Updating NotReady with a new ModRev resets streak = 0 and restarts notReadySince.
	core.Observed(3600, "n1", Record{State: StateNotReady, ModRev: 3})
	if snap := core.Snapshot().Nodes[0]; snap.Streak != 0 || snap.NotReadySince != 3600 {
		t.Fatalf("NotReady with new ModRev = %+v, want Streak=0 NotReadySince=3600", snap)
	}
	// Needs 3 fresh heartbeats after the ModRev change to propose Ready.
	core.Heartbeat(4000, "n1")
	if _, acts := core.Heartbeat(5000, "n1"); len(acts) != 0 {
		t.Fatalf("2nd heartbeat after reset should not propose Ready, got %+v", acts)
	}
	if _, acts := core.Heartbeat(6000, "n1"); len(acts) != 1 || acts[0].Kind != ActProposeReady {
		t.Fatalf("3rd heartbeat should propose Ready, got %+v", acts)
	}
}

func TestForgottenAndNowClamp(t *testing.T) {
	cfg := DefaultConfig("a", fixedRanked(map[string][]string{
		"n1": {"a", "b", "c"},
	}))
	core, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	core.Observed(1000, "n1", Record{State: StateReady, ModRev: 1})
	// Clamp now: passing now=500 must use c.now=1000.
	st, _ := core.Heartbeat(500, "n1")
	if st != HeartbeatAccepted || core.Snapshot().Nodes[0].Last != 1000 || !core.Snapshot().Nodes[0].Heard {
		t.Fatalf("Heartbeat(500) did not clamp to 1000: status=%v snap=%+v", st, core.Snapshot())
	}
	// Clamp in VoteRequested: c.now advances to 8000, then Tick(7000) clamps to 8000.
	vr := core.VoteRequested(8000, "b", "n1", 9)
	if len(vr) != 1 || !vr[0].Suspect || vr[0].Alive {
		t.Fatalf("VoteRequested(8000) = %+v, want Suspect=true Alive=false", vr)
	}
	// Tick(7000) clamps to 8000 and starts round 1 with deadline 9000.
	acts := core.Tick(7000)
	if len(acts) == 0 || core.Snapshot().Nodes[0].Round != 1 {
		t.Fatalf("Tick(7000) did not clamp to 8000: acts=%+v snap=%+v", acts, core.Snapshot())
	}
	// ProbeResult(10000, ok=true) clamps c.now to 10000, bumps backoff to 1, sets nextRoundAt = 12000.
	core.ProbeResult(10000, "b", false, "n1", 1, true)
	if acts := core.Tick(11999); len(acts) != 0 {
		t.Fatalf("Tick(11999) started round before bumped nextRoundAt=12000: %+v", acts)
	}
	if acts := core.Tick(12000); len(acts) == 0 || core.Snapshot().Nodes[0].Round != 2 {
		t.Fatalf("Tick(12000) did not start round 2: %+v", acts)
	}

	// Forgotten deletes both known and tracks.
	core.Forgotten(13000, "n1")
	if len(core.Snapshot().Nodes) != 0 {
		t.Fatalf("Snapshot after Forgotten not empty: %+v", core.Snapshot())
	}
	if st, acts := core.Heartbeat(13001, "n1"); st != HeartbeatUnknownNode || acts != nil {
		t.Fatalf("Heartbeat after Forgotten = (%v, %+v), want Unknown", st, acts)
	}
	if vr := core.VoteRequested(13001, "b", "n1", 1); len(vr) != 1 || vr[0].Suspect || vr[0].Alive {
		t.Fatalf("VoteRequested after Forgotten = %+v, want Suspect=false Alive=false", vr)
	}
	// Earlier Observed(500) clamps to c.now=13001.
	core.Observed(500, "n1", Record{State: StateReady, ModRev: 2})
	if core.Snapshot().Nodes[0].Last != 13001 {
		t.Fatalf("Observed(500) after Forgotten(13001) had Last=%d, want 13001", core.Snapshot().Nodes[0].Last)
	}
}

func TestHeartbeatUnknownNotOwnerAndRecovery(t *testing.T) {
	cfg := DefaultConfig("a", fixedRanked(map[string][]string{
		"owned":    {"a", "b", "c"},
		"notowned": {"b", "c", "d"},
		"notready": {"a", "b", "c"},
	}))
	cfg.ReadyHeartbeats = 3
	cfg.RetryInterval = 1000
	core, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if st, acts := core.Heartbeat(100, "missing"); st != HeartbeatUnknownNode || acts != nil {
		t.Fatalf("unknown heartbeat = (%v, %v)", st, acts)
	}
	core.Observed(100, "notowned", Record{State: StatePending, ModRev: 1})
	if st, acts := core.Heartbeat(200, "notowned"); st != HeartbeatNotOwner || acts != nil {
		t.Fatalf("not-owned heartbeat = (%v, %v)", st, acts)
	}

	core.Observed(100, "owned", Record{State: StatePending, ModRev: 10})
	if st, acts := core.Heartbeat(100, "owned"); st != HeartbeatAccepted || len(acts) != 0 {
		t.Fatalf("hb 1 = (%v, %+v)", st, acts)
	}
	if st, acts := core.Heartbeat(1100, "owned"); st != HeartbeatAccepted || len(acts) != 0 {
		t.Fatalf("hb 2 = (%v, %+v)", st, acts)
	}
	// Suspect heartbeat resets streak to 1.
	if st, acts := core.Heartbeat(7000, "owned"); st != HeartbeatAccepted || len(acts) != 0 {
		t.Fatalf("hb suspect = (%v, %+v)", st, acts)
	}
	if got := core.Snapshot().Nodes[0].Streak; got != 1 {
		t.Fatalf("streak after suspect heartbeat = %d, want 1", got)
	}
	core.Heartbeat(8000, "owned")
	st, acts := core.Heartbeat(9000, "owned")
	want := []Action{{Kind: ActProposeReady, Node: "owned", ModRev: 10}}
	if st != HeartbeatAccepted || !slices.Equal(acts, want) {
		t.Fatalf("hb 3 = (%v, %+v), want (%v, %+v)", st, acts, HeartbeatAccepted, want)
	}
	if _, acts := core.Heartbeat(9999, "owned"); len(acts) != 0 {
		t.Fatalf("hb at 9999 proposed early: %+v", acts)
	}
	if _, acts := core.Heartbeat(10000, "owned"); !slices.Equal(acts, want) {
		t.Fatalf("hb at 10000 = %+v, want %+v", acts, want)
	}
	core.Observed(10100, "owned", Record{State: StateReady, ModRev: 11})
	if _, acts := core.Heartbeat(11100, "owned"); len(acts) != 0 {
		t.Fatalf("Ready node should not propose Ready: %+v", acts)
	}

	core.Observed(1000, "notready", Record{State: StateNotReady, ModRev: 20})
	core.Heartbeat(12100, "notready")
	core.Heartbeat(13100, "notready")
	_, acts = core.Heartbeat(14100, "notready")
	wantNR := []Action{{Kind: ActProposeReady, Node: "notready", ModRev: 20}}
	if !slices.Equal(acts, wantNR) {
		t.Fatalf("NotReady recovery = %+v, want %+v", acts, wantNR)
	}
}

func TestExplicitFailuresAndRoundConfirmation(t *testing.T) {
	ranked := fixedRanked(map[string][]string{
		"n1": {"a", "b", "c", "d", "e"},
	})

	t.Run("timeouts alone never confirm even with quorum suspect votes", func(t *testing.T) {
		cfg := DefaultConfig("a", ranked)
		core, _ := New(cfg)
		core.Observed(0, "n1", Record{State: StateReady, ModRev: 7})
		core.Tick(5404)
		core.VoteReceived(5500, "b", "n1", 1, true, false)
		core.VoteReceived(5500, "c", "n1", 1, true, false)
		// Zero probe results arrive before deadline.
		if acts := core.Tick(6404); len(acts) != 0 {
			t.Fatalf("timeouts alone must never confirm NotReady, got %+v", acts)
		}
	})

	t.Run("fewer than MinFailedProbes failures never confirm", func(t *testing.T) {
		cfg := DefaultConfig("a", ranked) // MinFailedProbes=2, PeerProbers=2 (d, e)
		core, _ := New(cfg)
		core.Observed(0, "n1", Record{State: StateReady, ModRev: 7})
		core.Tick(5404)
		core.VoteReceived(5500, "b", "n1", 1, true, false)
		core.ProbeResult(5600, "d", false, "n1", 1, false)
		// Duplicate failure from same prober d must not double count!
		core.ProbeResult(5601, "d", false, "n1", 1, false)
		if acts := core.Tick(6404); len(acts) != 0 {
			t.Fatalf("1 unique failure < MinFailedProbes(2) must not confirm, got %+v", acts)
		}
	})

	t.Run("two explicit failures with quorum suspect confirm with round ModRev", func(t *testing.T) {
		cfg := DefaultConfig("a", ranked)
		core, _ := New(cfg)
		core.Observed(0, "n1", Record{State: StateReady, ModRev: 7})
		core.Tick(5404)
		core.VoteReceived(5500, "b", "n1", 1, true, false)
		core.ProbeResult(5600, "d", false, "n1", 1, false)
		core.ProbeResult(5601, "e", false, "n1", 1, false)
		acts := core.Tick(6404)
		want := []Action{{Kind: ActProposeNotReady, Node: "n1", ModRev: 7}}
		if !slices.Equal(acts, want) {
			t.Fatalf("deadline acts = %+v, want %+v", acts, want)
		}
	})

	t.Run("single prober confirms with 1 failure when MinFailedProbes=2 via min(MinFailedProbes, len(probers))", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a", "b", "c", "d"},
		}))
		cfg.PeerProbers = 1 // only d
		cfg.RackProbers = 0
		cfg.MinFailedProbes = 2
		core, _ := New(cfg)
		core.Observed(0, "n1", Record{State: StateReady, ModRev: 12})
		core.Tick(5404)
		core.VoteReceived(5500, "b", "n1", 1, true, false)
		core.ProbeResult(5600, "d", false, "n1", 1, false)
		acts := core.Tick(6404)
		want := []Action{{Kind: ActProposeNotReady, Node: "n1", ModRev: 12}}
		if !slices.Equal(acts, want) {
			t.Fatalf("single prober with 1 failure should confirm, got %+v", acts)
		}
	})

	t.Run("zero probers never confirm", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{"n1": {"a"}}))
		cfg.Owners = 1
		core, _ := New(cfg)
		core.Observed(0, "n1", Record{State: StateReady, ModRev: 7})
		core.Tick(5404)
		if acts := core.Tick(6404); len(acts) != 0 {
			t.Fatalf("zero probers must never confirm, got %+v", acts)
		}
	})

	t.Run("fewer than quorum suspect votes never confirm", func(t *testing.T) {
		cfg := DefaultConfig("a", ranked)
		core, _ := New(cfg)
		core.Observed(0, "n1", Record{State: StateReady, ModRev: 7})
		core.Tick(5404)
		core.VoteReceived(5500, "b", "n1", 1, false, false)
		core.VoteReceived(5500, "c", "n1", 1, false, false)
		core.ProbeResult(5600, "d", false, "n1", 1, false)
		core.ProbeResult(5601, "e", false, "n1", 1, false)
		if acts := core.Tick(6404); len(acts) != 0 {
			t.Fatalf("without quorum must not confirm, got %+v", acts)
		}
	})

	t.Run("heartbeat cancels active round", func(t *testing.T) {
		cfg := DefaultConfig("a", ranked)
		core, _ := New(cfg)
		core.Observed(0, "n1", Record{State: StateReady, ModRev: 7})
		core.Tick(5404)
		core.VoteReceived(5500, "b", "n1", 1, true, false)
		core.ProbeResult(5550, "d", false, "n1", 1, false)
		core.ProbeResult(5560, "e", false, "n1", 1, false)
		core.Heartbeat(5600, "n1")
		if core.Snapshot().Nodes[0].Round != 0 {
			t.Fatal("heartbeat did not cancel round")
		}
		if acts := core.Tick(6404); len(acts) != 0 {
			t.Fatalf("cancelled round emitted %+v", acts)
		}
	})
}

func TestAliveVoteVetoAndExponentialBackoff(t *testing.T) {
	ranked := fixedRanked(map[string][]string{
		"n1": {"a", "b", "c", "d", "e"},
	})
	cfg := DefaultConfig("a", ranked)
	cfg.RetryInterval = 1000
	cfg.MaxRoundBackoff = 10000
	core, _ := New(cfg)
	core.Observed(0, "n1", Record{State: StateReady, ModRev: 1})

	// Round 1 starts at 5404. Alive vote (even with suspect=true!) ends round and bumps backoff to 1 (delay = 2000).
	core.Tick(5404)
	// Non-voter and wrong-round alive votes are ignored.
	core.VoteReceived(5500, "x", "n1", 1, false, true)
	core.VoteReceived(5500, "b", "n1", 99, false, true)
	if core.Snapshot().Nodes[0].Round != 1 {
		t.Fatal("ignored alive votes ended round")
	}
	// Voter c sends alive=true (and suspect=true): alive takes precedence!
	core.VoteReceived(5502, "c", "n1", 1, true, true)
	if snap := core.Snapshot().Nodes[0]; snap.Round != 0 || snap.Backoff != 1 {
		t.Fatalf("alive vote did not end round and bump backoff: %+v", snap)
	}
	// Next round blocked until 5502 + 2000 = 7502.
	if acts := core.Tick(7501); len(acts) != 0 {
		t.Fatalf("Tick at 7501 started round early: %+v", acts)
	}
	core.Tick(7502)
	if core.Snapshot().Nodes[0].Round != 2 {
		t.Fatalf("expected round 2 at 7502, got %+v", core.Snapshot().Nodes[0])
	}

	// Ok-probe growth: backoff 1->2 (4000), 2->3 (8000), 3->4 (capped at MaxRoundBackoff=10000).
	core.ProbeResult(7600, "d", false, "n1", 2, true)
	if snap := core.Snapshot().Nodes[0]; snap.Round != 0 || snap.Backoff != 2 {
		t.Fatalf("ok probe 1 backoff = %+v, want Round=0 Backoff=2", snap)
	}
	if acts := core.Tick(11599); len(acts) != 0 {
		t.Fatalf("Tick at 11599 started early (want 11600): %+v", acts)
	}
	core.Tick(11600) // round 3
	core.ProbeResult(11700, "d", false, "n1", 3, true)
	if snap := core.Snapshot().Nodes[0]; snap.Backoff != 3 {
		t.Fatalf("ok probe 2 backoff = %d, want 3", snap.Backoff)
	}
	if acts := core.Tick(19699); len(acts) != 0 {
		t.Fatalf("Tick at 19699 started early (want 19700): %+v", acts)
	}
	core.Tick(19700) // round 4
	core.ProbeResult(19800, "d", false, "n1", 4, true)
	if snap := core.Snapshot().Nodes[0]; snap.Backoff != 4 {
		t.Fatalf("ok probe 3 backoff = %d, want 4", snap.Backoff)
	}
	// 1000 * 2^4 = 16000, capped at MaxRoundBackoff = 10000 -> next round at 29800.
	if acts := core.Tick(29799); len(acts) != 0 {
		t.Fatalf("Tick at 29799 started early (want 29800): %+v", acts)
	}
	core.Tick(29800) // round 5
	if core.Snapshot().Nodes[0].Round != 5 {
		t.Fatalf("expected round 5 at 29800, got %+v", core.Snapshot().Nodes[0])
	}

	// Heartbeat resets backoff to 0.
	core.Heartbeat(29900, "n1")
	if snap := core.Snapshot().Nodes[0]; snap.Backoff != 0 || snap.Round != 0 {
		t.Fatalf("heartbeat did not reset backoff and round: %+v", snap)
	}
}

func TestWitnessProbersSelfSuspicionAndHonestVotes(t *testing.T) {
	cfg := DefaultConfig("a", fixedRanked(map[string][]string{
		// 1 owner (a), 1 peer prober (b) -> easy to isolate aggregator vs node prober counts.
		"n1":  {"a", "b"},
		"n2":  {"a", "b"},
		"n3":  {"a", "b"},
		"n4":  {"a", "b"},
		"nr1": {"a", "b"},
		"w1":  {"a", "b"},
		"w2":  {"a", "b"},
		"w3":  {"a", "b"},
		"w4":  {"a", "b"},
	}))
	cfg.Owners = 1
	cfg.PeerProbers = 1
	cfg.RackProbers = 1
	cfg.WitnessProbers = 2
	cfg.MinFailedProbes = 2
	cfg.SelfSuspectPercent = 40
	cfg.SelfSuspectMinNodes = 3
	cfg.EvictionGrace = 1000
	cfg.EvictionRetryInterval = 5000
	core, _ := New(cfg)

	// Target rack "rA": n1, n2, n3, n4 (n4 is same-rack Ready mate), plus NotReady nr1 on "rA".
	core.Observed(0, "n1", Record{State: StateReady, Rack: "rA", ModRev: 1})
	core.Observed(0, "n2", Record{State: StateReady, Rack: "rA", ModRev: 1})
	core.Observed(4000, "n3", Record{State: StateReady, Rack: "rA", ModRev: 1})
	core.Observed(4000, "n4", Record{State: StateReady, Rack: "rA", ModRev: 1})
	core.Observed(0, "nr1", Record{State: StateNotReady, Rack: "rA", ModRev: 1})

	// Other-rack witness candidates on "rB":
	// w1: heard & not suspect, Ready, name "w1" > "n1" -> eligible
	// w2: NOT heard (bootstrapped only) -> ineligible
	// w3: heard & not suspect, Ready -> eligible
	// w4: heard & not suspect, NotReady -> ineligible
	core.Observed(4000, "w1", Record{State: StateReady, Rack: "rB", ModRev: 1})
	core.Heartbeat(5000, "w1")
	core.Observed(4000, "w2", Record{State: StateReady, Rack: "rB", ModRev: 1})
	core.Observed(4000, "w3", Record{State: StateReady, Rack: "rB", ModRev: 1})
	core.Heartbeat(5000, "w3")
	core.Observed(4000, "w4", Record{State: StateReady, Rack: "rB", ModRev: 1})
	core.Heartbeat(5000, "w4")
	core.Observed(5000, "w4", Record{State: StateNotReady, Rack: "rB", ModRev: 2})

	// Keep n3, n4, w1, w3 alive; only n1 and n2 become suspect at 5404 (2 < minNodes 3 -> NOT suspended).
	acts := core.Tick(5404)
	if core.Suspended() {
		t.Fatal("should not be suspended with 2 suspected")
	}
	// Even when NOT suspended, round 1 for n1 asks witness probers (w1, w3) after peer b and rack n3
	// (n2 is tracked-and-suspected so rackProbers skips n2 and picks n3!).
	var n1Probes []Action
	for _, a := range acts {
		if a.Node == "n1" && a.Kind == ActProbeRequest {
			n1Probes = append(n1Probes, a)
		}
	}
	wantN1Probes := []Action{
		{Kind: ActProbeRequest, To: "b", ToNode: false, Node: "n1", Round: 1},
		{Kind: ActProbeRequest, To: "n3", ToNode: true, Node: "n1", Round: 1},
		{Kind: ActProbeRequest, To: "w1", ToNode: true, Node: "n1", Round: 1},
		{Kind: ActProbeRequest, To: "w3", ToNode: true, Node: "n1", Round: 1},
	}
	if !slices.Equal(n1Probes, wantN1Probes) {
		t.Fatalf("non-suspended round n1 probes = %+v, want %+v", n1Probes, wantN1Probes)
	}

	// VoteRequested checks:
	// - n1 (heard=false, suspect=true) -> Suspect=true, Alive=false
	// - w1 (heard=true, suspect=false) -> Suspect=false, Alive=true
	// - w2 (heard=false, suspect=false) -> Suspect=false, Alive=false
	// - nr1 (NotReady) -> Suspect=false, Alive=false
	if got := core.VoteRequested(5404, "b", "n1", 10)[0]; !got.Suspect || got.Alive {
		t.Fatalf("VoteRequested(n1) = %+v, want Suspect=true Alive=false", got)
	}
	if got := core.VoteRequested(5404, "b", "w1", 10)[0]; got.Suspect || !got.Alive {
		t.Fatalf("VoteRequested(w1) = %+v, want Suspect=false Alive=true", got)
	}
	if got := core.VoteRequested(5404, "b", "w2", 10)[0]; got.Suspect || got.Alive {
		t.Fatalf("VoteRequested(w2) = %+v, want Suspect=false Alive=false", got)
	}
	if got := core.VoteRequested(5404, "b", "nr1", 10)[0]; got.Suspect || got.Alive {
		t.Fatalf("VoteRequested(nr1) = %+v, want Suspect=false Alive=false", got)
	}

	// Round 1 for n1 asked node probers (nodeAsked=3 >= 1) and peer b:
	// failing peer b + 1 node n3 is only 1 node failure < min(2, 3)=2, so it MUST NOT confirm at 6404!
	core.ProbeResult(5500, "b", false, "n1", 1, false)
	core.ProbeResult(5500, "n3", true, "n1", 1, false)
	// Also test n2 (round 2): fail only peer b (0 node failures) -> must not confirm at 6404.
	core.ProbeResult(5500, "b", false, "n2", 2, false)
	acts6404 := core.Tick(6404)
	for _, a := range acts6404 {
		if a.Kind == ActProposeNotReady {
			t.Fatalf("round with nodeAsked=3 and <2 node failures must not confirm: %+v", a)
		}
	}

	// Keep w1 and w3 alive so they remain witnesses. At 9404, n3 and n4 also become suspect
	// (4 of 7 Ready tracked nodes suspected -> suspended=true).
	core.Heartbeat(9000, "w1")
	core.Heartbeat(9000, "w3")
	acts = core.Tick(9404)
	if !core.Suspended() {
		t.Fatal("expected suspended=true at 9404")
	}
	for _, a := range acts {
		if a.Kind == ActProposeEvictable {
			t.Fatalf("must not propose evictable while suspended: %+v", a)
		}
	}
	// VoteRequested answers honestly even while suspended!
	if got := core.VoteRequested(9404, "b", "w1", 11)[0]; got.Suspect || !got.Alive {
		t.Fatalf("VoteRequested(w1) while suspended = %+v, want Suspect=false Alive=true", got)
	}
	if got := core.VoteRequested(9404, "b", "n3", 11)[0]; !got.Suspect || got.Alive {
		t.Fatalf("VoteRequested(n3) while suspended = %+v, want Suspect=true Alive=false", got)
	}

	// Check n3's round opened at 9404: n4 is now tracked-and-suspected so rackProbers skips n4!
	// Only peer b and witnesses w1, w3 are asked.
	var n3Round uint64
	var n3Probes []Action
	for _, a := range acts {
		if a.Node == "n3" && a.Kind == ActProbeRequest {
			n3Round = a.Round
			n3Probes = append(n3Probes, a)
		}
	}
	wantN3Probes := []Action{
		{Kind: ActProbeRequest, To: "b", ToNode: false, Node: "n3", Round: n3Round},
		{Kind: ActProbeRequest, To: "w1", ToNode: true, Node: "n3", Round: n3Round},
		{Kind: ActProbeRequest, To: "w3", ToNode: true, Node: "n3", Round: n3Round},
	}
	if !slices.Equal(n3Probes, wantN3Probes) {
		t.Fatalf("n3 probes (skipping suspected rack-mate n4) = %+v, want %+v", n3Probes, wantN3Probes)
	}

	// Confirm rule with nodeAsked >= 1: aggregator failure (b) + 1 node failure (w1) < min(2, 2)=2 -> MUST NOT confirm!
	core.ProbeResult(9500, "b", false, "n3", n3Round, false)
	core.ProbeResult(9500, "w1", true, "n3", n3Round, false)
	// For n4's round: 2 node failures (w1, w3) -> DOES confirm!
	var n4Round uint64
	for _, a := range acts {
		if a.Node == "n4" && a.Kind == ActProbeRequest {
			n4Round = a.Round
			break
		}
	}
	core.ProbeResult(9500, "w1", true, "n4", n4Round, false)
	core.ProbeResult(9500, "w3", true, "n4", n4Round, false)

	core.Heartbeat(10300, "w1")
	core.Heartbeat(10300, "w3")
	acts = core.Tick(10404)
	for _, a := range acts {
		if a.Kind == ActProposeNotReady && a.Node == "n3" {
			t.Fatalf("n3 with 1 node failure + 1 peer failure must not confirm: %+v", a)
		}
	}
	if !slices.Contains(acts, Action{Kind: ActProposeNotReady, Node: "n4", ModRev: 1}) {
		t.Fatalf("n4 with 2 node failures should confirm, got %+v", acts)
	}
}

func TestProberSelectionAndConfirmationRules(t *testing.T) {
	t.Run("nodeAsked>=1 ignores aggregator failures even when not suspended", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a", "b", "c", "d", "e"},
			"w1": {"a"},
		}))
		cfg.Owners = 1
		cfg.PeerProbers = 2 // d, e
		cfg.RackProbers = 0
		cfg.WitnessProbers = 2
		cfg.MinFailedProbes = 1
		core, _ := New(cfg)
		core.Observed(0, "n1", Record{State: StateReady, Rack: "rA", ModRev: 7})
		core.Observed(4000, "w1", Record{State: StateReady, Rack: "rB", ModRev: 1})
		core.Heartbeat(5000, "w1")

		acts := core.Tick(5404)
		// Probes: d (agg), e (agg), w1 (node) -> nodeAsked = 1.
		if core.Suspended() || len(acts) != 3 {
			t.Fatalf("expected non-suspended 3 probes, suspended=%v acts=%+v", core.Suspended(), acts)
		}
		// Both aggregator probers fail, node prober w1 times out -> MUST NOT confirm!
		core.ProbeResult(5500, "d", false, "n1", 1, false)
		core.ProbeResult(5500, "e", false, "n1", 1, false)
		if deadActs := core.Tick(6404); len(deadActs) != 0 {
			t.Fatalf("aggregator failures alone must not confirm when node probers asked, got %+v", deadActs)
		}
	})

	t.Run("nodeAsked=1 with MinFailedProbes=2 confirms on 1 node failure", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a", "b"},
			"w1": {"a"},
		}))
		cfg.Owners = 1
		cfg.PeerProbers = 1
		cfg.RackProbers = 0
		cfg.WitnessProbers = 1
		cfg.MinFailedProbes = 2
		core, _ := New(cfg)
		core.Observed(0, "n1", Record{State: StateReady, Rack: "rA", ModRev: 9})
		core.Observed(4000, "w1", Record{State: StateReady, Rack: "rB", ModRev: 1})
		core.Heartbeat(5000, "w1")

		core.Tick(5404)
		core.ProbeResult(5500, "w1", true, "n1", 1, false)
		acts := core.Tick(6404)
		want := []Action{{Kind: ActProposeNotReady, Node: "n1", ModRev: 9}}
		if !slices.Equal(acts, want) {
			t.Fatalf("nodeAsked=1 with MinFailedProbes=2 acts = %+v, want %+v", acts, want)
		}
	})

	t.Run("aggregator-only round confirms when not suspended and suppresses when suspended", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a", "b", "c"},
			"n2": {"a", "b", "c"},
			"n3": {"a", "b", "c"},
		}))
		cfg.Owners = 1
		cfg.PeerProbers = 2 // b, c
		cfg.RackProbers = 0
		cfg.WitnessProbers = 0
		cfg.MinFailedProbes = 2
		cfg.SelfSuspectPercent = 50
		cfg.SelfSuspectMinNodes = 2
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateReady, ModRev: 1})
		core.Observed(4000, "n2", Record{State: StateReady, ModRev: 2})
		core.Observed(4000, "n3", Record{State: StateReady, ModRev: 3})

		// At 5404: only n1 suspected (1 < 2 -> not suspended). Round 1 has 2 aggregator probers, 0 node probers.
		core.Tick(5404)
		core.ProbeResult(5500, "b", false, "n1", 1, false)
		core.ProbeResult(5500, "c", false, "n1", 1, false)
		core.Heartbeat(5000, "n2")
		core.Heartbeat(5000, "n3")
		acts := core.Tick(6404)
		if !slices.Contains(acts, Action{Kind: ActProposeNotReady, Node: "n1", ModRev: 1}) {
			t.Fatalf("aggregator-only round when not suspended should confirm, got %+v", acts)
		}

		// Keep n2, n3 on steady 1000ms heartbeats through 11000 while n1 becomes suspect again at 12404.
		core.Heartbeat(7000, "n1")
		for ts := int64(6000); ts <= 11000; ts += 1000 {
			core.Heartbeat(ts, "n2")
			core.Heartbeat(ts, "n3")
		}
		// At 12404: n1 is suspect (12404-7000=5404), n2/n3 are not (12404-11000=1404) -> not suspended -> n1 opens round.
		acts = core.Tick(12404)
		var rID uint64
		for _, a := range acts {
			if a.Node == "n1" && a.Kind == ActProbeRequest {
				rID = a.Round
				break
			}
		}
		if rID == 0 {
			t.Fatalf("expected aggregator-only round to open for n1 at 12404, got %+v", acts)
		}
		core.ProbeResult(12500, "b", false, "n1", rID, false)
		core.ProbeResult(12500, "c", false, "n1", rID, false)
		// Advance to 20000 so n2 and n3 are also suspect (20000-11000=9000) at deadline -> suspended = true!
		acts = core.Tick(20000)
		if !core.Suspended() {
			t.Fatal("expected suspended=true at 20000")
		}
		for _, a := range acts {
			if a.Kind == ActProposeNotReady && a.Node == "n1" {
				t.Fatalf("aggregator-only round must not confirm while suspended: %+v", a)
			}
		}
	})

	t.Run("rack probers skip tracked-and-suspected rack-mates and keep untracked ones", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1":          {"a"},
			"r_ok":        {"a"},
			"r_sus":       {"a"},
			"r_untracked": {"b"}, // not owned by a -> untracked
		}))
		cfg.Owners = 1
		cfg.PeerProbers = 0
		cfg.RackProbers = 3
		cfg.WitnessProbers = 0
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateReady, Rack: "rA", ModRev: 1})
		core.Observed(0, "r_sus", Record{State: StateReady, Rack: "rA", ModRev: 1})
		core.Observed(4000, "r_ok", Record{State: StateReady, Rack: "rA", ModRev: 1})
		core.Observed(0, "r_untracked", Record{State: StateReady, Rack: "rA", ModRev: 1})

		acts := core.Tick(5404)
		var n1Probes []Action
		for _, a := range acts {
			if a.Node == "n1" && a.Kind == ActProbeRequest {
				n1Probes = append(n1Probes, a)
			}
		}
		want := []Action{
			{Kind: ActProbeRequest, To: "r_ok", ToNode: true, Node: "n1", Round: 1},
			{Kind: ActProbeRequest, To: "r_untracked", ToNode: true, Node: "n1", Round: 1},
		}
		if !slices.Equal(n1Probes, want) {
			t.Fatalf("rack probers = %+v, want %+v", n1Probes, want)
		}
	})
}

func TestWitnessRotationAndEvictionRetryInterval(t *testing.T) {
	cfg := DefaultConfig("a", fixedRanked(map[string][]string{
		"a1": {"a"},
		"b1": {"a"},
		"m":  {"a"},
		"n1": {"a"},
		"nr": {"a"},
		"z1": {"a"},
	}))
	cfg.Owners = 1
	cfg.PeerProbers = 0
	cfg.RackProbers = 0
	cfg.WitnessProbers = 2
	cfg.SelfSuspectPercent = 1
	cfg.SelfSuspectMinNodes = 1
	cfg.EvictionGrace = 2000
	cfg.RetryInterval = 1000
	cfg.EvictionRetryInterval = 5000
	core, _ := New(cfg)

	// Witnesses in rack "r2", sorted: "a1", "b1", "n1", "z1". Target "m" is between "b1" and "n1",
	// so rotate(["a1","b1","n1","z1"], "m") = ["n1","z1","a1","b1"], taking ["n1","z1"].
	for _, w := range []string{"a1", "b1", "n1", "z1"} {
		core.Observed(4000, w, Record{State: StateReady, Rack: "r2", ModRev: 1})
	}
	core.Observed(4000, "m", Record{State: StateReady, Rack: "r1", ModRev: 1})
	for _, w := range []string{"a1", "b1", "n1", "z1"} {
		core.Heartbeat(5000, w)
	}
	for _, w := range []string{"a1", "b1", "n1", "z1"} {
		core.Heartbeat(8000, w)
	}
	acts := core.Tick(9404)
	want := []Action{
		{Kind: ActProbeRequest, To: "n1", ToNode: true, Node: "m", Round: 1},
		{Kind: ActProbeRequest, To: "z1", ToNode: true, Node: "m", Round: 1},
	}
	if !slices.Equal(acts, want) {
		t.Fatalf("witness rotation acts = %+v, want %+v", acts, want)
	}
}

func TestEvictionGraceAndRetryInterval(t *testing.T) {
	cfg := DefaultConfig("a", fixedRanked(map[string][]string{
		"nr": {"a"},
	}))
	cfg.Owners = 1
	cfg.EvictionGrace = 2000
	cfg.RetryInterval = 1000
	cfg.EvictionRetryInterval = 5000
	core, _ := New(cfg)

	core.Observed(10000, "nr", Record{State: StatePending, ModRev: 1})
	core.Heartbeat(10000, "nr")
	core.Observed(10100, "nr", Record{State: StateNotReady, ModRev: 2}) // notReadySince = 10100

	// At 12099 (< 10100 + 2000): grace not elapsed.
	if acts := core.Tick(12099); len(acts) != 0 {
		t.Fatalf("eviction proposed early: %+v", acts)
	}
	// At 15404: window suspects and 15404 - 10100 >= 2000 -> proposes Evictable, sets nextEvictAt = 15404 + 5000 = 20404.
	acts := core.Tick(15404)
	if !slices.Equal(acts, []Action{{Kind: ActProposeEvictable, Node: "nr", ModRev: 2}}) {
		t.Fatalf("first eviction proposal = %+v", acts)
	}
	// At 16404 (1000ms = RetryInterval, < EvictionRetryInterval 5000): must NOT propose again!
	if acts := core.Tick(16404); len(acts) != 0 {
		t.Fatalf("eviction used RetryInterval instead of EvictionRetryInterval: %+v", acts)
	}
	// At 20403: still blocked; at 20404: proposes again.
	if acts := core.Tick(20403); len(acts) != 0 {
		t.Fatalf("eviction proposed at 20403: %+v", acts)
	}
	acts = core.Tick(20404)
	if !slices.Equal(acts, []Action{{Kind: ActProposeEvictable, Node: "nr", ModRev: 2}}) {
		t.Fatalf("second eviction proposal at 20404 = %+v", acts)
	}
}

func TestFailThenOKFromSameProberEndsRound(t *testing.T) {
	t.Run("aggregator prober", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a", "b"},
		}))
		cfg.Owners = 1
		cfg.PeerProbers = 1
		cfg.RackProbers = 0
		cfg.WitnessProbers = 0
		cfg.RetryInterval = 1000
		cfg.MaxRoundBackoff = 10000
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateReady, ModRev: 1})
		core.Tick(5404)
		core.ProbeResult(5500, "b", false, "n1", 1, false)
		core.ProbeResult(5501, "b", false, "n1", 1, true)

		snap := core.Snapshot().Nodes[0]
		if snap.Round != 0 || snap.Backoff != 1 || snap.NextRoundAt != 7501 {
			t.Fatalf("after fail-then-ok = %+v, want Round=0 Backoff=1 NextRoundAt=7501", snap)
		}
		if acts := core.Tick(6404); len(acts) != 0 {
			t.Fatalf("Tick at deadline after ended round emitted %+v", acts)
		}
	})

	t.Run("node prober", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a"},
			"n2": {"a"},
		}))
		cfg.Owners = 1
		cfg.PeerProbers = 0
		cfg.RackProbers = 1
		cfg.WitnessProbers = 0
		cfg.RetryInterval = 1000
		cfg.MaxRoundBackoff = 10000
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateReady, Rack: "r1", ModRev: 1})
		core.Observed(4000, "n2", Record{State: StateReady, Rack: "r1", ModRev: 1})
		core.Tick(5404)
		core.ProbeResult(5500, "n2", true, "n1", 1, false)
		core.ProbeResult(5501, "n2", true, "n1", 1, true)

		snap := core.Snapshot().Nodes[0]
		if snap.Round != 0 || snap.Backoff != 1 || snap.NextRoundAt != 7501 {
			t.Fatalf("after node fail-then-ok = %+v, want Round=0 Backoff=1 NextRoundAt=7501", snap)
		}
	})
}

func TestProbeResultMatchesProberKindAndDeduplicatesFailures(t *testing.T) {
	cfg := DefaultConfig("a", fixedRanked(map[string][]string{
		"n1": {"a", "b"},
		"n2": {"a"},
	}))
	cfg.Owners = 1
	cfg.PeerProbers = 1
	cfg.RackProbers = 1
	cfg.WitnessProbers = 0
	cfg.MinFailedProbes = 1
	core, _ := New(cfg)

	core.Observed(0, "n1", Record{State: StateReady, Rack: "r1", ModRev: 1})
	core.Observed(4000, "n2", Record{State: StateReady, Rack: "r1", ModRev: 1})
	core.Tick(5404)

	// Matching id with wrong fromNode must be ignored for both ok and failure results.
	core.ProbeResult(5500, "b", true, "n1", 1, true)
	core.ProbeResult(5501, "n2", false, "n1", 1, false)
	if snap := core.Snapshot().Nodes[0]; snap.Round != 1 || snap.Backoff != 0 {
		t.Fatalf("wrong-kind probe results mutated round: %+v", snap)
	}
	if acts := core.Tick(6404); len(acts) != 0 {
		t.Fatalf("wrong-kind probe failure confirmed round: %+v", acts)
	}
}

func TestSuspectThenAliveFromSameVoterEndsRound(t *testing.T) {
	t.Run("suspect then alive ends round and bumps", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a", "b", "c", "d"},
		}))
		cfg.Owners = 3
		cfg.PeerProbers = 1
		cfg.RackProbers = 0
		cfg.WitnessProbers = 0
		cfg.MinFailedProbes = 1
		cfg.RetryInterval = 1000
		cfg.MaxRoundBackoff = 10000
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateReady, ModRev: 1})
		core.Tick(5404)
		core.VoteReceived(5500, "b", "n1", 1, true, false)
		core.VoteReceived(5501, "b", "n1", 1, false, true)

		snap := core.Snapshot().Nodes[0]
		if snap.Round != 0 || snap.Backoff != 1 || snap.NextRoundAt != 7501 {
			t.Fatalf("after suspect-then-alive = %+v, want Round=0 Backoff=1 NextRoundAt=7501", snap)
		}
		if acts := core.Tick(6404); len(acts) != 0 {
			t.Fatalf("Tick at deadline after ended round emitted %+v", acts)
		}
	})

	t.Run("duplicate suspect vote from same voter counted once", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a", "b", "c", "d", "e", "f"},
		}))
		cfg.Owners = 5 // quorum = 3 (self + 2)
		cfg.PeerProbers = 1
		cfg.RackProbers = 0
		cfg.WitnessProbers = 0
		cfg.MinFailedProbes = 1
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateReady, ModRev: 1})
		core.Tick(5404)
		core.VoteReceived(5500, "b", "n1", 1, true, false)
		core.VoteReceived(5501, "b", "n1", 1, true, false)
		core.ProbeResult(5502, "f", false, "n1", 1, false)
		if acts := core.Tick(6404); len(acts) != 0 {
			t.Fatalf("duplicate suspect vote reached quorum: %+v", acts)
		}
	})
}

func TestAllSuspectedTriggersSelfSuspicionOnlyWithTwoOrMoreReadyNodes(t *testing.T) {
	t.Run("two tracked Ready nodes both suspected triggers self-suspicion", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a", "b"},
			"n2": {"a", "b"},
		}))
		cfg.Owners = 1
		cfg.PeerProbers = 1
		cfg.RackProbers = 0
		cfg.WitnessProbers = 0
		cfg.MinFailedProbes = 1
		cfg.SelfSuspectMinNodes = 3
		cfg.SelfSuspectPercent = 30
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateReady, ModRev: 1})
		core.Observed(0, "n2", Record{State: StateReady, ModRev: 1})
		core.Tick(5404)
		if !core.Suspended() {
			t.Fatal("expected Suspended() == true when all 2 tracked Ready nodes are suspected")
		}
		core.ProbeResult(5500, "b", false, "n1", 1, false)
		acts := core.Tick(6404)
		for _, a := range acts {
			if a.Kind == ActProposeNotReady {
				t.Fatalf("must not confirm on aggregator failures when all 2 Ready nodes are suspected: %+v", acts)
			}
		}
	})

	t.Run("one tracked Ready node suspected can still confirm when below min nodes", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a", "b"},
		}))
		cfg.Owners = 1
		cfg.PeerProbers = 1
		cfg.RackProbers = 0
		cfg.WitnessProbers = 0
		cfg.MinFailedProbes = 1
		cfg.SelfSuspectMinNodes = 2
		cfg.SelfSuspectPercent = 30
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateReady, ModRev: 7})
		core.Tick(5404)
		if core.Suspended() {
			t.Fatal("expected Suspended() == false for 1 tracked Ready node when SelfSuspectMinNodes=2")
		}
		core.ProbeResult(5500, "b", false, "n1", 1, false)
		acts := core.Tick(6404)
		want := []Action{{Kind: ActProposeNotReady, Node: "n1", ModRev: 7}}
		if !slices.Equal(acts, want) {
			t.Fatalf("Tick(6404) = %+v, want %+v", acts, want)
		}
	})
}

func TestHeartbeatResetsNextRoundAtAfterBackoff(t *testing.T) {
	cfg := DefaultConfig("a", fixedRanked(map[string][]string{
		"n1": {"a", "b"},
	}))
	cfg.Owners = 1
	cfg.PeerProbers = 1
	cfg.RackProbers = 0
	cfg.WitnessProbers = 0
	cfg.RetryInterval = 5000
	cfg.MaxRoundBackoff = 100000
	core, _ := New(cfg)

	core.Observed(0, "n1", Record{State: StateReady, ModRev: 1})
	now := int64(5404)
	for i := range 3 {
		core.Tick(now)
		rid := uint64(i + 1)
		core.ProbeResult(now+1, "b", false, "n1", rid, true)
		now = core.Snapshot().Nodes[0].NextRoundAt
	}
	oldNextRoundAt := now
	if snap := core.Snapshot().Nodes[0]; snap.Backoff != 3 || snap.NextRoundAt <= 40000 {
		t.Fatalf("unexpected pre-heartbeat backoff state: %+v", snap)
	}

	hbAt := oldNextRoundAt - 30000
	core.Heartbeat(hbAt, "n1")
	if snap := core.Snapshot().Nodes[0]; snap.NextRoundAt != 0 || snap.Backoff != 0 {
		t.Fatalf("heartbeat did not reset NextRoundAt and Backoff: %+v", snap)
	}

	// Silence after heartbeat: once window suspects again (well before oldNextRoundAt), Tick opens a round.
	suspectAt := hbAt + 10000
	if suspectAt >= oldNextRoundAt {
		t.Fatalf("test setup error: suspectAt=%d >= oldNextRoundAt=%d", suspectAt, oldNextRoundAt)
	}
	acts := core.Tick(suspectAt)
	if len(acts) != 1 || acts[0].Kind != ActProbeRequest || acts[0].Round != 4 {
		t.Fatalf("expected new round 4 at %d after heartbeat reset, got %+v", suspectAt, acts)
	}
}

func TestReadyProposalNotDelayedByEvictionProposal(t *testing.T) {
	t.Run("Ready proposal not delayed by preceding eviction proposal", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a"},
		}))
		cfg.Owners = 1
		cfg.EvictionGrace = 0
		cfg.EvictionRetryInterval = 10000
		cfg.RetryInterval = 1000
		cfg.ReadyHeartbeats = 2
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateNotReady, ModRev: 1})
		acts := core.Tick(5404)
		if !slices.Equal(acts, []Action{{Kind: ActProposeEvictable, Node: "n1", ModRev: 1}}) {
			t.Fatalf("Tick(5404) = %+v, want ActProposeEvictable", acts)
		}
		if snap := core.Snapshot().Nodes[0]; snap.NextEvictAt != 15404 || snap.NextReadyAt != 0 {
			t.Fatalf("unexpected proposal timers after eviction: %+v", snap)
		}

		core.Heartbeat(5500, "n1")
		_, acts = core.Heartbeat(6500, "n1")
		want := []Action{{Kind: ActProposeReady, Node: "n1", ModRev: 1}}
		if !slices.Equal(acts, want) {
			t.Fatalf("Heartbeat(6500) = %+v, want %+v (delayed by eviction timer)", acts, want)
		}
		if snap := core.Snapshot().Nodes[0]; snap.NextReadyAt != 7500 || snap.NextEvictAt != 15404 {
			t.Fatalf("unexpected proposal timers after Ready: %+v", snap)
		}
	})

	t.Run("eviction proposal not delayed by preceding Ready proposal", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a"},
		}))
		cfg.Owners = 1
		cfg.EvictionGrace = 0
		cfg.EvictionRetryInterval = 1000
		cfg.RetryInterval = 10000
		cfg.ReadyHeartbeats = 2
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateNotReady, ModRev: 1})
		core.Heartbeat(1000, "n1")
		_, acts := core.Heartbeat(2000, "n1")
		if len(acts) != 1 || acts[0].Kind != ActProposeReady {
			t.Fatalf("Heartbeat(2000) = %+v, want ActProposeReady", acts)
		}
		if snap := core.Snapshot().Nodes[0]; snap.NextReadyAt != 12000 || snap.NextEvictAt != 0 {
			t.Fatalf("unexpected proposal timers after Ready: %+v", snap)
		}
		// At 7404 (< NextReadyAt 12000), node is suspect again and should propose Evictable immediately.
		acts = core.Tick(7404)
		want := []Action{{Kind: ActProposeEvictable, Node: "n1", ModRev: 1}}
		if !slices.Equal(acts, want) {
			t.Fatalf("Tick(7404) = %+v, want %+v (delayed by Ready timer)", acts, want)
		}
	})
}

func TestNotReadyGraceAndStreakRestartOnModRevChange(t *testing.T) {
	cfg := DefaultConfig("a", fixedRanked(map[string][]string{
		"n1": {"a"},
	}))
	cfg.Owners = 1
	cfg.EvictionGrace = 5000
	cfg.ReadyHeartbeats = 3
	core, _ := New(cfg)

	core.Observed(1000, "n1", Record{State: StateNotReady, ModRev: 2})
	core.Heartbeat(2000, "n1")
	core.Heartbeat(3000, "n1")

	// Same ModRev does not restart grace or streak.
	core.Observed(4000, "n1", Record{State: StateNotReady, ModRev: 2})
	if snap := core.Snapshot().Nodes[0]; snap.NotReadySince != 1000 || snap.Streak != 2 {
		t.Fatalf("same ModRev NotReady = %+v, want NotReadySince=1000 Streak=2", snap)
	}

	// New ModRev restarts grace and streak.
	core.Observed(5000, "n1", Record{State: StateNotReady, ModRev: 3})
	if snap := core.Snapshot().Nodes[0]; snap.NotReadySince != 5000 || snap.Streak != 0 {
		t.Fatalf("new ModRev NotReady = %+v, want NotReadySince=5000 Streak=0", snap)
	}

	// At 9999 (last HB 3000 -> suspect; 9999-1000 >= 5000, but 9999-5000 < 5000): must NOT propose evictable.
	if acts := core.Tick(9999); len(acts) != 0 {
		t.Fatalf("Tick(9999) proposed evictable before restarted grace elapsed: %+v", acts)
	}
	acts := core.Tick(10000)
	want := []Action{{Kind: ActProposeEvictable, Node: "n1", ModRev: 3}}
	if !slices.Equal(acts, want) {
		t.Fatalf("Tick(10000) = %+v, want %+v", acts, want)
	}
}

func TestRoundBaseStartsIDsAndIgnoresOtherBase(t *testing.T) {
	const base uint64 = 1_000_000
	cfg := DefaultConfig("a", fixedRanked(map[string][]string{
		"n1": {"a", "b", "c", "d"},
	}))
	cfg.Owners = 3
	cfg.PeerProbers = 1
	cfg.RackProbers = 0
	cfg.WitnessProbers = 0
	cfg.MinFailedProbes = 1
	cfg.RetryInterval = 1000
	cfg.RoundBase = base
	core, _ := New(cfg)

	core.Observed(0, "n1", Record{State: StateReady, ModRev: 1})
	acts := core.Tick(5404)
	want := []Action{
		{Kind: ActVoteRequest, To: "b", Node: "n1", Round: base + 1},
		{Kind: ActVoteRequest, To: "c", Node: "n1", Round: base + 1},
		{Kind: ActProbeRequest, To: "d", ToNode: false, Node: "n1", Round: base + 1},
	}
	if !slices.Equal(acts, want) || core.Snapshot().Nodes[0].Round != base+1 {
		t.Fatalf("first round acts=%+v snap=%+v, want base+1=%d", acts, core.Snapshot().Nodes[0], base+1)
	}

	// Results from base 0 (round 1) or base itself must be ignored.
	core.VoteReceived(5500, "b", "n1", 1, true, true)
	core.ProbeResult(5500, "d", false, "n1", 1, true)
	core.VoteReceived(5500, "b", "n1", base, true, false)
	core.ProbeResult(5500, "d", false, "n1", base, false)
	if core.Snapshot().Nodes[0].Round != base+1 {
		t.Fatalf("wrong-base results cleared round: %+v", core.Snapshot().Nodes[0])
	}

	if acts := core.Tick(6404); len(acts) != 0 {
		t.Fatalf("wrong-base failure confirmed round: %+v", acts)
	}
	acts = core.Tick(7404)
	if core.Snapshot().Nodes[0].Round != base+2 || len(acts) != 3 || acts[0].Round != base+2 {
		t.Fatalf("second round = %+v snap=%+v, want Round=%d", acts, core.Snapshot().Nodes[0], base+2)
	}
}

func TestBackoffExponentAndCapReach16(t *testing.T) {
	cfg := DefaultConfig("a", fixedRanked(map[string][]string{
		"n1": {"a", "b"},
	}))
	cfg.Owners = 1
	cfg.PeerProbers = 1
	cfg.RackProbers = 0
	cfg.WitnessProbers = 0
	cfg.RetryInterval = 10
	cfg.MaxRoundBackoff = 3_600_000
	core, _ := New(cfg)

	core.Observed(0, "n1", Record{State: StateReady, ModRev: 1})
	now := int64(5404)
	for i := 1; i <= 17; i++ {
		core.Tick(now)
		rid := uint64(i)
		core.ProbeResult(now+1, "b", false, "n1", rid, true)
		snap := core.Snapshot().Nodes[0]
		wantBackoff := min(i, 16)
		wantNext := now + 1 + (10 << wantBackoff)
		if snap.Backoff != wantBackoff || snap.NextRoundAt != wantNext {
			t.Fatalf("iter %d: snap=%+v, want Backoff=%d NextRoundAt=%d", i, snap, wantBackoff, wantNext)
		}
		now = snap.NextRoundAt
	}
}

func TestObservedStateChangeSameModRevClearsRound(t *testing.T) {
	for _, nextState := range []State{StateNotReady, StatePending} {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a", "b"},
		}))
		cfg.Owners = 1
		cfg.PeerProbers = 1
		cfg.RackProbers = 0
		cfg.WitnessProbers = 0
		cfg.MinFailedProbes = 1
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateReady, ModRev: 7})
		core.Tick(5404)
		core.ProbeResult(5500, "b", false, "n1", 1, false)
		core.Observed(5600, "n1", Record{State: nextState, ModRev: 7})
		if snap := core.Snapshot().Nodes[0]; snap.Round != 0 {
			t.Fatalf("state %v with same ModRev did not clear round: %+v", nextState, snap)
		}
		if acts := core.Tick(6404); len(acts) != 0 {
			t.Fatalf("cleared round for state %v emitted %+v", nextState, acts)
		}
	}
}

func TestDoubtedRoundRefusesAggregatorConfirmationAfterSelfSuspicionClears(t *testing.T) {
	t.Run("round opened while suspended stays doubted after heartbeat clears self-suspicion", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"nA": {"a", "b", "c"},
			"nB": {"a", "b", "c"},
		}))
		cfg.Owners = 3
		cfg.PeerProbers = 2
		cfg.RackProbers = 2
		cfg.WitnessProbers = 3
		cfg.MinFailedProbes = 2
		cfg.RoundTimeout = 1000
		cfg.SelfSuspectMinNodes = 3
		cfg.SelfSuspectPercent = 30
		core, _ := New(cfg)

		core.Observed(0, "nA", Record{State: StateReady, Rack: "rA", ModRev: 1})
		core.Observed(0, "nB", Record{State: StateReady, Rack: "rB", ModRev: 1})

		acts := core.Tick(5404)
		if !core.Suspended() {
			t.Fatal("expected Suspended() == true when both tracked Ready nodes are suspected")
		}
		for _, a := range acts {
			if a.Node == "nA" && a.Kind == ActProbeRequest && a.ToNode {
				t.Fatalf("expected no node probers for nA, got %+v", a)
			}
		}
		if snap := core.Snapshot().Nodes[0]; snap.Node != "nA" || snap.Round != 1 || !snap.Doubted {
			t.Fatalf("nA snapshot at open = %+v, want Round=1 Doubted=true", snap)
		}

		core.VoteReceived(5500, "b", "nA", 1, true, false)
		core.VoteReceived(5500, "c", "nA", 1, true, false)
		core.ProbeResult(5500, "b", false, "nA", 1, false)
		core.ProbeResult(5500, "c", false, "nA", 1, false)

		core.Heartbeat(6394, "nB")
		acts = core.Tick(6404)
		if core.Suspended() {
			t.Fatal("expected Suspended() == false after nB heartbeat")
		}
		for _, a := range acts {
			if a.Kind == ActProposeNotReady && a.Node == "nA" {
				t.Fatalf("doubted aggregator-only round must not confirm after self-suspicion clears: %+v", acts)
			}
		}
		if snap := core.Snapshot().Nodes[0]; snap.Round != 0 || snap.Doubted {
			t.Fatalf("nA snapshot after deadline = %+v, want Round=0 Doubted=false", snap)
		}
	})

	t.Run("round opened while not suspended becomes doubted on later tick", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a", "b", "c"},
			"n2": {"a", "b", "c"},
			"n3": {"a", "b", "c"},
		}))
		cfg.Owners = 1
		cfg.PeerProbers = 2
		cfg.RackProbers = 2
		cfg.WitnessProbers = 3
		cfg.MinFailedProbes = 2
		cfg.RoundTimeout = 5000
		cfg.SelfSuspectMinNodes = 2
		cfg.SelfSuspectPercent = 50
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateReady, Rack: "r1", ModRev: 1})
		core.Observed(3000, "n2", Record{State: StateReady, Rack: "r2", ModRev: 1})
		core.Observed(3000, "n3", Record{State: StateReady, Rack: "r3", ModRev: 1})

		core.Tick(5404)
		if core.Suspended() {
			t.Fatal("expected Suspended() == false at 5404")
		}
		if snap := core.Snapshot().Nodes[0]; snap.Node != "n1" || snap.Round != 1 || snap.Doubted {
			t.Fatalf("n1 snapshot at open = %+v, want Round=1 Doubted=false", snap)
		}

		core.ProbeResult(5500, "b", false, "n1", 1, false)
		core.ProbeResult(5500, "c", false, "n1", 1, false)

		core.Tick(8404)
		if !core.Suspended() {
			t.Fatal("expected Suspended() == true at 8404")
		}
		if snap := core.Snapshot().Nodes[0]; snap.Round != 1 || !snap.Doubted {
			t.Fatalf("n1 snapshot at 8404 = %+v, want Round=1 Doubted=true", snap)
		}

		core.Heartbeat(9000, "n2")
		core.Heartbeat(9000, "n3")

		acts := core.Tick(10404)
		if core.Suspended() {
			t.Fatal("expected Suspended() == false at 10404")
		}
		for _, a := range acts {
			if a.Kind == ActProposeNotReady && a.Node == "n1" {
				t.Fatalf("round doubted mid-flight must not confirm on aggregator reports: %+v", acts)
			}
		}
		if snap := core.Snapshot().Nodes[0]; snap.Round != 0 || snap.Doubted {
			t.Fatalf("n1 snapshot after deadline = %+v, want Round=0 Doubted=false", snap)
		}
	})

	t.Run("undoubted aggregator-only round still confirms", func(t *testing.T) {
		cfg := DefaultConfig("a", fixedRanked(map[string][]string{
			"n1": {"a", "b"},
		}))
		cfg.Owners = 1
		cfg.PeerProbers = 1
		cfg.RackProbers = 0
		cfg.WitnessProbers = 0
		cfg.MinFailedProbes = 1
		cfg.RoundTimeout = 1000
		cfg.SelfSuspectMinNodes = 2
		core, _ := New(cfg)

		core.Observed(0, "n1", Record{State: StateReady, ModRev: 7})
		core.Tick(5404)
		if snap := core.Snapshot().Nodes[0]; snap.Round != 1 || snap.Doubted {
			t.Fatalf("n1 snapshot at open = %+v, want Round=1 Doubted=false", snap)
		}
		core.ProbeResult(5500, "b", false, "n1", 1, false)
		acts := core.Tick(6404)
		want := []Action{{Kind: ActProposeNotReady, Node: "n1", ModRev: 7}}
		if !slices.Equal(acts, want) {
			t.Fatalf("Tick(6404) = %+v, want %+v", acts, want)
		}
		if snap := core.Snapshot().Nodes[0]; snap.Round != 0 || snap.Doubted {
			t.Fatalf("n1 snapshot after deadline = %+v, want Round=0 Doubted=false", snap)
		}
	})
}

func TestUntrackedWitnessProbers(t *testing.T) {
	ranked := fixedRanked(map[string][]string{
		"nodeA":   {"a", "b", "c", "d", "e"},
		"nodeB":   {"b", "c", "d", "e", "a"}, // not owned by a when Owners=3
		"tracked": {"a", "b", "c"},
		"pending": {"b", "c", "d"},
		"dead":    {"b", "c", "d"},
	})

	newSetup := func(t *testing.T) *Core {
		t.Helper()
		cfg := DefaultConfig("a", ranked)
		cfg.Owners = 3
		cfg.PeerProbers = 2
		cfg.RackProbers = 2
		cfg.WitnessProbers = 3
		cfg.MinFailedProbes = 2
		core, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		core.Observed(0, "nodeA", Record{State: StateReady, Rack: "r1", ModRev: 1})
		core.Observed(0, "nodeB", Record{State: StateReady, Rack: "r2", ModRev: 1})
		return core
	}

	t.Run("untracked other-rack Ready node is asked and required to confirm", func(t *testing.T) {
		core := newSetup(t)
		snap := core.Snapshot()
		if len(snap.Nodes) != 1 || snap.Nodes[0].Node != "nodeA" {
			t.Fatalf("tracked nodes = %+v, want only nodeA", snap.Nodes)
		}

		acts := core.Tick(5404)
		if core.Suspended() {
			t.Fatal("single tracked node must not trigger self-suspicion")
		}
		want := []Action{
			{Kind: ActVoteRequest, To: "b", Node: "nodeA", Round: 1},
			{Kind: ActVoteRequest, To: "c", Node: "nodeA", Round: 1},
			{Kind: ActProbeRequest, To: "d", ToNode: false, Node: "nodeA", Round: 1},
			{Kind: ActProbeRequest, To: "e", ToNode: false, Node: "nodeA", Round: 1},
			{Kind: ActProbeRequest, To: "nodeB", ToNode: true, Node: "nodeA", Round: 1},
		}
		if !slices.Equal(acts, want) {
			t.Fatalf("Tick(5404) = %+v, want %+v", acts, want)
		}

		// Quorum suspect vote and aggregator failures alone must not confirm while nodeB has not reported.
		core.VoteReceived(5500, "b", "nodeA", 1, true, false)
		core.ProbeResult(5500, "d", false, "nodeA", 1, false)
		core.ProbeResult(5500, "e", false, "nodeA", 1, false)
		if dead := core.Tick(6404); len(dead) != 0 {
			t.Fatalf("aggregator failures alone confirmed without nodeB report: %+v", dead)
		}
	})

	t.Run("failure report from untracked witness with quorum confirms", func(t *testing.T) {
		core := newSetup(t)
		core.Tick(5404)
		core.VoteReceived(5500, "b", "nodeA", 1, true, false)
		core.ProbeResult(5500, "nodeB", true, "nodeA", 1, false)
		acts := core.Tick(6404)
		want := []Action{{Kind: ActProposeNotReady, Node: "nodeA", ModRev: 1}}
		if !slices.Equal(acts, want) {
			t.Fatalf("Tick(6404) = %+v, want %+v", acts, want)
		}
	})

	t.Run("ok from untracked witness ends round", func(t *testing.T) {
		core := newSetup(t)
		core.Tick(5404)
		core.VoteReceived(5500, "b", "nodeA", 1, true, false)
		core.ProbeResult(5500, "nodeB", true, "nodeA", 1, true)
		snap := core.Snapshot().Nodes[0]
		if snap.Round != 0 || snap.Backoff != 1 {
			t.Fatalf("after ok from nodeB = %+v, want Round=0 Backoff=1", snap)
		}
		if acts := core.Tick(6404); len(acts) != 0 {
			t.Fatalf("deadline Tick after ended round emitted %+v", acts)
		}
	})

	t.Run("tracked unheard or suspected and untracked non-Ready other-rack nodes are skipped", func(t *testing.T) {
		cfg := DefaultConfig("a", ranked)
		cfg.Owners = 3
		cfg.PeerProbers = 0
		cfg.RackProbers = 0
		cfg.WitnessProbers = 5
		core, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		core.Observed(0, "nodeA", Record{State: StateReady, Rack: "r1", ModRev: 1})
		core.Observed(0, "nodeB", Record{State: StateReady, Rack: "r2", ModRev: 1})
		core.Observed(0, "pending", Record{State: StatePending, Rack: "r2", ModRev: 1})
		core.Observed(0, "dead", Record{State: StateNotReady, Rack: "r2", ModRev: 1})
		core.Observed(4000, "tracked", Record{State: StateReady, Rack: "r2", ModRev: 1})

		acts := core.Tick(5404)
		var probes []Action
		for _, a := range acts {
			if a.Node == "nodeA" && a.Kind == ActProbeRequest {
				probes = append(probes, a)
			}
		}
		want := []Action{
			{Kind: ActProbeRequest, To: "nodeB", ToNode: true, Node: "nodeA", Round: 1},
		}
		if !slices.Equal(probes, want) {
			t.Fatalf("unheard tracked + non-Ready untracked: probes = %+v, want %+v", probes, want)
		}

		// Heartbeat both owned nodes so tracked becomes heard, then advance until both are suspected.
		core.Heartbeat(5500, "nodeA")
		core.Heartbeat(5500, "tracked")
		acts = core.Tick(12000)
		probes = nil
		for _, a := range acts {
			if a.Node == "nodeA" && a.Kind == ActProbeRequest {
				probes = append(probes, a)
			}
		}
		want = []Action{
			{Kind: ActProbeRequest, To: "nodeB", ToNode: true, Node: "nodeA", Round: 2},
		}
		if !slices.Equal(probes, want) {
			t.Fatalf("suspected tracked + non-Ready untracked: probes = %+v, want %+v", probes, want)
		}
	})
}
