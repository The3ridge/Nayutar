package liveness_test

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/liveness"
	"github.com/Kaikei-e/Nayutar/core/liveness/agent"
	"github.com/Kaikei-e/Nayutar/core/liveness/detector"
	"github.com/Kaikei-e/Nayutar/core/liveness/hrw"
	"github.com/Kaikei-e/Nayutar/core/store"
)

func TestEngineConfigValidation(t *testing.T) {
	aggs := []string{"agg-a", "agg-b", "agg-c"}
	cfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
	detDefault := detector.DefaultConfig("agg-a", nil)
	if cfg.PingTimeout != 800 || cfg.PingInterval != 100 {
		t.Fatalf("DefaultAggregatorEngineConfig PingTimeout=%d PingInterval=%d, want 800, 100", cfg.PingTimeout, cfg.PingInterval)
	}
	if cfg.Detector.Owners != detDefault.Owners ||
		cfg.Detector.PeerProbers != detDefault.PeerProbers ||
		cfg.Detector.RackProbers != detDefault.RackProbers ||
		cfg.Detector.WitnessProbers != detDefault.WitnessProbers ||
		cfg.Detector.MinFailedProbes != detDefault.MinFailedProbes ||
		cfg.Detector.RoundTimeout != detDefault.RoundTimeout ||
		cfg.Detector.RetryInterval != detDefault.RetryInterval ||
		cfg.Detector.MaxRoundBackoff != detDefault.MaxRoundBackoff ||
		cfg.Detector.EvictionRetryInterval != detDefault.EvictionRetryInterval ||
		cfg.Detector.SelfSuspectPercent != detDefault.SelfSuspectPercent ||
		cfg.Detector.SelfSuspectMinNodes != detDefault.SelfSuspectMinNodes ||
		cfg.Detector.ReadyHeartbeats != detDefault.ReadyHeartbeats ||
		cfg.Detector.EvictionGrace != detDefault.EvictionGrace ||
		cfg.Detector.Phi != detDefault.Phi {
		t.Fatalf("DefaultAggregatorEngineConfig Detector = %+v, want detector.DefaultConfig %+v", cfg.Detector, detDefault)
	}
	if _, err := liveness.NewAggregatorEngine(cfg); err != nil {
		t.Fatalf("NewAggregatorEngine(default): %v", err)
	}

	// Self and Ranked are filled in by NewAggregatorEngine even if zero in Detector.
	custom := liveness.AggregatorEngineConfig{
		ID:           "agg-b",
		Aggregators:  aggs,
		Detector:     detector.DefaultConfig("", nil),
		PingTimeout:  400,
		PingInterval: 100,
	}
	if _, err := liveness.NewAggregatorEngine(custom); err != nil {
		t.Fatalf("NewAggregatorEngine(custom): %v", err)
	}

	invalidAgg := []struct {
		name string
		cfg  liveness.AggregatorEngineConfig
	}{
		{"empty-id", liveness.DefaultAggregatorEngineConfig("", aggs)},
		{"id-not-in-aggregators", liveness.DefaultAggregatorEngineConfig("agg-z", aggs)},
		{"empty-aggregators", liveness.DefaultAggregatorEngineConfig("agg-a", nil)},
		{"zero-ping-timeout", func() liveness.AggregatorEngineConfig {
			c := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			c.PingTimeout = 0
			return c
		}()},
		{"zero-ping-interval", func() liveness.AggregatorEngineConfig {
			c := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			c.PingInterval = 0
			return c
		}()},
		{"bad-detector-owners", func() liveness.AggregatorEngineConfig {
			c := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			c.Detector.Owners = 0
			return c
		}()},
		{"ping-timeout-equal-round-timeout", func() liveness.AggregatorEngineConfig {
			c := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			c.PingTimeout = c.Detector.RoundTimeout
			return c
		}()},
		{"ping-timeout-above-round-timeout", func() liveness.AggregatorEngineConfig {
			c := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			c.PingTimeout = c.Detector.RoundTimeout + 1
			return c
		}()},
	}
	for _, tc := range invalidAgg {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := liveness.NewAggregatorEngine(tc.cfg); err == nil {
				t.Fatalf("NewAggregatorEngine(%s) = nil, want error", tc.name)
			}
		})
	}

	validBoundary := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
	validBoundary.PingTimeout = validBoundary.Detector.RoundTimeout - 1
	if _, err := liveness.NewAggregatorEngine(validBoundary); err != nil {
		t.Fatalf("NewAggregatorEngine(PingTimeout=RoundTimeout-1): %v", err)
	}

	if _, err := liveness.NewNodeEngine(agent.Config{}); err == nil {
		t.Fatal("NewNodeEngine(empty) = nil, want error")
	}
}

func TestAggregatorEngineRoundBasePassthrough(t *testing.T) {
	aggs := []string{"agg-a", "agg-b"}
	for _, tc := range []struct {
		name      string
		roundBase uint64
		wantRound uint64
	}{
		{name: "zero", roundBase: 0, wantRound: 1},
		{name: "explicit", roundBase: 7000, wantRound: 7001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			cfg.Detector.RoundBase = tc.roundBase
			eng, err := liveness.NewAggregatorEngine(cfg)
			if err != nil {
				t.Fatalf("NewAggregatorEngine: %v", err)
			}
			eng.Observed(0, liveness.Record{Node: "n1", State: detector.StateReady, Rack: "r1", ModRev: 1})
			msgs, _ := eng.Tick(10_000)
			var gotRound uint64
			for _, m := range msgs {
				if m.Kind == liveness.MsgVoteRequest {
					gotRound = m.Round
					break
				}
			}
			if gotRound != tc.wantRound {
				t.Fatalf("first round = %d, want %d", gotRound, tc.wantRound)
			}
		})
	}
}

func TestAggregatorEngineMappings(t *testing.T) {
	aggs := []string{"agg-a", "agg-b", "agg-c"}

	t.Run("HeartbeatIgnoredAndBufferedProposeReadyOrder", func(t *testing.T) {
		cfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
		cfg.Detector.Owners = 1
		cfg.Detector.EvictionGrace = 1000
		eng, err := liveness.NewAggregatorEngine(cfg)
		if err != nil {
			t.Fatal(err)
		}

		// Unknown node heartbeat is ignored.
		if msgs := eng.Handle(0, liveness.Message{
			Kind: liveness.MsgHeartbeat,
			From: liveness.Addr{ID: "unknown", Node: true},
			To:   liveness.Addr{ID: "agg-a"},
			Node: "unknown",
		}); len(msgs) != 0 {
			t.Fatalf("unknown heartbeat msgs = %+v, want nil", msgs)
		}

		var ownedNode, otherNode, earlyNotReady string
		for i := range 200 {
			cand := string([]byte{'a', byte('a' + i/26), byte('a' + i%26)})
			if hrw.Ranked(cand, aggs)[0] == "agg-a" {
				earlyNotReady = cand
				break
			}
		}
		for i := range 50 {
			cand := string([]byte{'n', byte('0' + i/10), byte('0' + i%10)})
			top := hrw.Ranked(cand, aggs)[0]
			if top == "agg-a" && ownedNode == "" {
				ownedNode = cand
			}
			if top != "agg-a" && otherNode == "" {
				otherNode = cand
			}
		}
		if ownedNode == "" || otherNode == "" || earlyNotReady == "" {
			t.Fatal("failed to find test nodes")
		}

		eng.Observed(0, liveness.Record{Node: otherNode, State: detector.StatePending, Rack: "r1", ModRev: 1})
		if msgs := eng.Handle(0, liveness.Message{
			Kind: liveness.MsgHeartbeat,
			From: liveness.Addr{ID: otherNode, Node: true},
			To:   liveness.Addr{ID: "agg-a"},
			Node: otherNode,
		}); len(msgs) != 0 {
			t.Fatalf("non-owner heartbeat msgs = %+v, want nil", msgs)
		}

		eng.Observed(0, liveness.Record{Node: earlyNotReady, State: detector.StateNotReady, Since: 0, Rack: "r1", ModRev: 5})
		eng.Observed(0, liveness.Record{Node: ownedNode, State: detector.StatePending, Since: 0, Rack: "r1", ModRev: 9})

		// Send 3 heartbeats for ownedNode; 3rd buffers ProposeReady.
		for i := range 3 {
			ts := int64(i * 1000)
			msgs := eng.Handle(ts, liveness.Message{
				Kind: liveness.MsgHeartbeat,
				From: liveness.Addr{ID: ownedNode, Node: true},
				To:   liveness.Addr{ID: "agg-a"},
				Node: ownedNode,
			})
			if len(msgs) != 0 {
				t.Fatalf("heartbeat %d returned msgs %+v, want nil", i, msgs)
			}
		}

		// At t=10000, earlyNotReady is suspected and now-notReadySince >= EvictionGrace, so Tick emits ProposeEvictable.
		// Buffered ProposeReady from Handle must precede Tick's own ProposeEvictable even though earlyNotReady < ownedNode.
		msgs, props := eng.Tick(10000)
		if len(msgs) != 0 {
			t.Fatalf("unexpected msgs = %+v", msgs)
		}
		wantProps := []liveness.Proposal{
			{Kind: liveness.ProposeReady, Node: ownedNode, ModRev: 9},
			{Kind: liveness.ProposeEvictable, Node: earlyNotReady, ModRev: 5},
		}
		if !slices.Equal(props, wantProps) {
			t.Fatalf("Tick proposals = %+v, want %+v", props, wantProps)
		}

		// Forgotten drops ownedNode so subsequent heartbeats do not propose.
		eng.Forgotten(10100, ownedNode)
		eng.Forgotten(10100, earlyNotReady)
		for i := range 3 {
			if msgs := eng.Handle(10200+int64(i*1000), liveness.Message{
				Kind: liveness.MsgHeartbeat,
				From: liveness.Addr{ID: ownedNode, Node: true},
				To:   liveness.Addr{ID: "agg-a"},
				Node: ownedNode,
			}); len(msgs) != 0 {
				t.Fatalf("heartbeat after Forgotten returned %+v", msgs)
			}
		}
		if _, props := eng.Tick(12300); len(props) != 0 {
			t.Fatalf("proposals after Forgotten = %+v, want none", props)
		}
	})

	t.Run("VoteAliveRelayResendAndTickOrdering", func(t *testing.T) {
		cfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
		cfg.Detector.Owners = 3
		cfg.Detector.PeerProbers = 1
		cfg.Detector.RackProbers = 1
		cfg.Detector.WitnessProbers = 1
		cfg.Detector.RoundTimeout = 500
		cfg.PingTimeout = 400
		cfg.PingInterval = 100
		eng, err := liveness.NewAggregatorEngine(cfg)
		if err != nil {
			t.Fatal(err)
		}

		// MsgPing to aggregator is ignored, unknown kind is ignored.
		if msgs := eng.Handle(0, liveness.Message{
			Kind:  liveness.MsgPing,
			From:  liveness.Addr{ID: "agg-b"},
			To:    liveness.Addr{ID: "agg-a"},
			Token: 1,
		}); len(msgs) != 0 {
			t.Fatalf("MsgPing to aggregator = %+v, want nil", msgs)
		}
		if msgs := eng.Handle(0, liveness.Message{Kind: 99}); len(msgs) != 0 {
			t.Fatalf("unknown kind = %+v, want nil", msgs)
		}

		// Observe suspected targets n1, n2 (rack r1), healthy rack-mate n3 (rack r1),
		// and healthy cross-rack witness w1 (rack r2).
		eng.Observed(0, liveness.Record{Node: "n1", State: detector.StateReady, Rack: "r1", ModRev: 10})
		eng.Observed(0, liveness.Record{Node: "n2", State: detector.StateReady, Rack: "r1", ModRev: 11})
		eng.Observed(0, liveness.Record{Node: "n3", State: detector.StateReady, Rack: "r1", ModRev: 12})
		eng.Observed(0, liveness.Record{Node: "w1", State: detector.StateReady, Rack: "r2", ModRev: 13})

		// Bootstrapped node without heartbeat -> Suspect: false, Alive: false.
		voteBoot := eng.Handle(100, liveness.Message{
			Kind:  liveness.MsgVoteRequest,
			From:  liveness.Addr{ID: "agg-b"},
			To:    liveness.Addr{ID: "agg-a"},
			Node:  "n1",
			Round: 6,
		})
		wantBoot := []liveness.Message{{
			Kind:    liveness.MsgVote,
			From:    liveness.Addr{ID: "agg-a"},
			To:      liveness.Addr{ID: "agg-b"},
			Node:    "n1",
			Round:   6,
			Suspect: false,
			Alive:   false,
		}}
		if !slices.Equal(voteBoot, wantBoot) {
			t.Fatalf("MsgVote(bootstrapped) = %+v, want %+v", voteBoot, wantBoot)
		}

		// After receiving a heartbeat from n1, VoteRequest -> Suspect: false, Alive: true.
		eng.Handle(100, liveness.Message{
			Kind: liveness.MsgHeartbeat,
			From: liveness.Addr{ID: "n1", Node: true},
			To:   liveness.Addr{ID: "agg-a"},
			Node: "n1",
		})
		voteAlive := eng.Handle(200, liveness.Message{
			Kind:  liveness.MsgVoteRequest,
			From:  liveness.Addr{ID: "agg-b"},
			To:    liveness.Addr{ID: "agg-a"},
			Node:  "n1",
			Round: 7,
		})
		wantAlive := []liveness.Message{{
			Kind:    liveness.MsgVote,
			From:    liveness.Addr{ID: "agg-a"},
			To:      liveness.Addr{ID: "agg-b"},
			Node:    "n1",
			Round:   7,
			Suspect: false,
			Alive:   true,
		}}
		if !slices.Equal(voteAlive, wantAlive) {
			t.Fatalf("MsgVote(alive) = %+v, want %+v", voteAlive, wantAlive)
		}

		// ProbeRequest at t=200 -> MsgPing token 1; Tick at t=300 -> re-sent MsgPing token 1; Ack at t=350 -> ProbeResult OK:true.
		pingMsgs := eng.Handle(200, liveness.Message{
			Kind:  liveness.MsgProbeRequest,
			From:  liveness.Addr{ID: "agg-b"},
			To:    liveness.Addr{ID: "agg-a"},
			Node:  "n1",
			Round: 7,
		})
		wantPing := []liveness.Message{{
			Kind:  liveness.MsgPing,
			From:  liveness.Addr{ID: "agg-a"},
			To:    liveness.Addr{ID: "n1", Node: true},
			Token: 1,
		}}
		if !slices.Equal(pingMsgs, wantPing) {
			t.Fatalf("MsgProbeRequest -> %+v, want %+v", pingMsgs, wantPing)
		}
		// Keep n3 and w1 healthy while advancing to t=300.
		for _, id := range []string{"n3", "w1"} {
			eng.Handle(300, liveness.Message{
				Kind: liveness.MsgHeartbeat,
				From: liveness.Addr{ID: id, Node: true},
				To:   liveness.Addr{ID: "agg-a"},
				Node: id,
			})
		}
		resendMsgs, _ := eng.Tick(300)
		if !slices.Equal(resendMsgs, wantPing) {
			t.Fatalf("Tick(300) resend = %+v, want %+v", resendMsgs, wantPing)
		}
		ackRes := eng.Handle(350, liveness.Message{
			Kind:  liveness.MsgAck,
			From:  liveness.Addr{ID: "n1", Node: true},
			To:    liveness.Addr{ID: "agg-a"},
			Token: 1,
		})
		wantAckRes := []liveness.Message{{
			Kind:  liveness.MsgProbeResult,
			From:  liveness.Addr{ID: "agg-a"},
			To:    liveness.Addr{ID: "agg-b"},
			Node:  "n1",
			Round: 7,
			OK:    true,
		}}
		if !slices.Equal(ackRes, wantAckRes) {
			t.Fatalf("MsgAck -> %+v, want %+v", ackRes, wantAckRes)
		}
		// Unknown ack token -> nil.
		if badAck := eng.Handle(350, liveness.Message{
			Kind:  liveness.MsgAck,
			From:  liveness.Addr{ID: "n1", Node: true},
			To:    liveness.Addr{ID: "agg-a"},
			Token: 999,
		}); len(badAck) != 0 {
			t.Fatalf("unknown MsgAck -> %+v, want nil", badAck)
		}

		// Keep n3 (rack-mate) and w1 (cross-rack witness) heartbeating every second up to t=10000
		// so neither is suspected and w1 is heard.
		for ts := int64(1000); ts <= 10000; ts += 1000 {
			for _, id := range []string{"n3", "w1"} {
				eng.Handle(ts, liveness.Message{
					Kind: liveness.MsgHeartbeat,
					From: liveness.Addr{ID: id, Node: true},
					To:   liveness.Addr{ID: "agg-a"},
					Node: id,
				})
			}
		}

		// Two ProbeRequests: token 2 at t=9600 (expires at t=10000) and token 3 at t=9900 (re-sent at t=10000).
		ping2 := eng.Handle(9600, liveness.Message{
			Kind:  liveness.MsgProbeRequest,
			From:  liveness.Addr{ID: "agg-c"},
			To:    liveness.Addr{ID: "agg-a"},
			Node:  "n2",
			Round: 4,
		})
		ping3 := eng.Handle(9900, liveness.Message{
			Kind:  liveness.MsgProbeRequest,
			From:  liveness.Addr{ID: "agg-c"},
			To:    liveness.Addr{ID: "agg-a"},
			Node:  "n1",
			Round: 5,
		})
		if len(ping2) != 1 || ping2[0].Token != 2 || len(ping3) != 1 || ping3[0].Token != 3 {
			t.Fatalf("unexpected probe pings: %+v, %+v", ping2, ping3)
		}

		// At t=10000, both n1 and n2 are suspected -> Suspect: true, Alive: false.
		voteSuspect := eng.Handle(10000, liveness.Message{
			Kind:  liveness.MsgVoteRequest,
			From:  liveness.Addr{ID: "agg-b"},
			To:    liveness.Addr{ID: "agg-a"},
			Node:  "n1",
			Round: 8,
		})
		if len(voteSuspect) != 1 || !voteSuspect[0].Suspect || voteSuspect[0].Alive {
			t.Fatalf("MsgVote(suspect) = %+v, want Suspect:true Alive:false", voteSuspect)
		}

		// Tick at t=10000: order must be [0] Expire (token 2), [1] Resend (token 3), [2..] detector round messages.
		msgs, props := eng.Tick(10000)
		if len(props) != 0 {
			t.Fatalf("Tick(10000) props = %+v, want nil", props)
		}
		if len(msgs) < 4 {
			t.Fatalf("Tick(10000) msgs = %+v, expected expiry + resend + detector msgs", msgs)
		}
		wantExpired := liveness.Message{
			Kind:  liveness.MsgProbeResult,
			From:  liveness.Addr{ID: "agg-a"},
			To:    liveness.Addr{ID: "agg-c"},
			Node:  "n2",
			Round: 4,
			OK:    false,
		}
		wantResent := liveness.Message{
			Kind:  liveness.MsgPing,
			From:  liveness.Addr{ID: "agg-a"},
			To:    liveness.Addr{ID: "n1", Node: true},
			Token: 3,
		}
		if msgs[0] != wantExpired {
			t.Fatalf("msgs[0] = %+v, want relay expiry %+v", msgs[0], wantExpired)
		}
		if msgs[1] != wantResent {
			t.Fatalf("msgs[1] = %+v, want relay resend %+v", msgs[1], wantResent)
		}

		// Verify detector round messages for n1 and n2: each gets peer aggregator prober (To.Node == false),
		// healthy rack prober n3 (To.Node == true), and cross-rack witness w1 (To.Node == true).
		var n1Round, n2Round uint64
		var n1Probers, n2Probers []liveness.Addr
		for _, m := range msgs[2:] {
			switch m.Kind {
			case liveness.MsgVoteRequest:
				if m.Node == "n1" && n1Round == 0 {
					n1Round = m.Round
				}
				if m.Node == "n2" && n2Round == 0 {
					n2Round = m.Round
				}
			case liveness.MsgProbeRequest:
				if m.Node == "n1" {
					n1Probers = append(n1Probers, m.To)
				}
				if m.Node == "n2" {
					n2Probers = append(n2Probers, m.To)
				}
			default:
				t.Fatalf("unexpected detector msg kind in Tick: %+v", m)
			}
		}
		checkProbers := func(node string, got []liveness.Addr) {
			t.Helper()
			if len(got) != 3 {
				t.Fatalf("%s probers = %+v, want 3 (1 peer agg, rack prober n3, witness w1)", node, got)
			}
			if got[0].Node || !slices.Contains(aggs, got[0].ID) || got[0].ID == "agg-a" {
				t.Fatalf("%s prober[0] = %+v, want peer aggregator with Node:false", node, got[0])
			}
			if got[1] != (liveness.Addr{ID: "n3", Node: true}) {
				t.Fatalf("%s prober[1] = %+v, want rack prober n3 with Node:true", node, got[1])
			}
			if got[2] != (liveness.Addr{ID: "w1", Node: true}) {
				t.Fatalf("%s prober[2] = %+v, want witness w1 with Node:true", node, got[2])
			}
		}
		if n1Round == 0 || n2Round == 0 {
			t.Fatalf("rounds missing: n1=%d n2=%d", n1Round, n2Round)
		}
		checkProbers("n1", n1Probers)
		checkProbers("n2", n2Probers)

		// Cancel n2's round using MsgVote with Alive: true!
		eng.Handle(10100, liveness.Message{
			Kind:    liveness.MsgVote,
			From:    liveness.Addr{ID: "agg-b"},
			To:      liveness.Addr{ID: "agg-a"},
			Node:    "n2",
			Round:   n2Round,
			Suspect: false,
			Alive:   true,
		})

		// For n1, provide quorum suspect vote and node probe failures (n3 + w1 satisfy MinFailedProbes=2).
		eng.Handle(10100, liveness.Message{
			Kind:    liveness.MsgVote,
			From:    liveness.Addr{ID: "agg-b"},
			To:      liveness.Addr{ID: "agg-a"},
			Node:    "n1",
			Round:   n1Round,
			Suspect: true,
			Alive:   false,
		})
		for _, p := range n1Probers {
			eng.Handle(10200, liveness.Message{
				Kind:  liveness.MsgProbeResult,
				From:  p,
				To:    liveness.Addr{ID: "agg-a"},
				Node:  "n1",
				Round: n1Round,
				OK:    false,
			})
		}

		// At deadline t=10500, n1 confirms ProposeNotReady; n2 was cancelled by Alive: true.
		_, props = eng.Tick(10500)
		wantNotReady := []liveness.Proposal{{
			Kind:   liveness.ProposeNotReady,
			Node:   "n1",
			ModRev: 10,
		}}
		if !slices.Equal(props, wantNotReady) {
			t.Fatalf("Tick(10500) proposals = %+v, want %+v", props, wantNotReady)
		}
	})

	t.Run("Suspended", func(t *testing.T) {
		cfg := liveness.DefaultAggregatorEngineConfig("agg-a", []string{"agg-a"})
		cfg.Detector.Owners = 1
		eng, err := liveness.NewAggregatorEngine(cfg)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"n1", "n2", "n3"} {
			eng.Observed(0, liveness.Record{Node: n, State: detector.StateReady, Rack: "r1", ModRev: 1})
		}
		if eng.Suspended() {
			t.Fatal("Suspended() initially true")
		}
		_, _ = eng.Tick(10000)
		if !eng.Suspended() {
			t.Fatal("Suspended() = false when all 3 Ready nodes suspected")
		}
	})
}

func TestNodeEngineMappings(t *testing.T) {
	aggs := []string{"agg-a", "agg-b", "agg-c"}
	cfg := agent.DefaultConfig("n1", func(node string) []string {
		return hrw.Ranked(node, aggs)
	})
	cfg.Owners = 2
	eng, err := liveness.NewNodeEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// Tick at t=0 -> MsgHeartbeat to first 2 ranked owners.
	owners := hrw.Ranked("n1", aggs)[:2]
	hbMsgs := eng.Tick(0)
	if len(hbMsgs) != 2 {
		t.Fatalf("Tick(0) len = %d, want 2 (%+v)", len(hbMsgs), hbMsgs)
	}
	for i, owner := range owners {
		want := liveness.Message{
			Kind: liveness.MsgHeartbeat,
			From: liveness.Addr{ID: "n1", Node: true},
			To:   liveness.Addr{ID: owner, Node: false},
			Node: "n1",
		}
		if hbMsgs[i] != want {
			t.Fatalf("hbMsgs[%d] = %+v, want %+v", i, hbMsgs[i], want)
		}
	}

	// Tick at t=500 (< Interval) -> no heartbeats.
	if msgs := eng.Tick(500); len(msgs) != 0 {
		t.Fatalf("Tick(500) = %+v, want nil", msgs)
	}

	// MsgPing from aggregator and from peer node -> MsgAck preserving From.Node in To.Node.
	ackAgg := eng.Handle(500, liveness.Message{
		Kind:  liveness.MsgPing,
		From:  liveness.Addr{ID: "agg-a", Node: false},
		To:    liveness.Addr{ID: "n1", Node: true},
		Token: 11,
	})
	wantAckAgg := []liveness.Message{{
		Kind:  liveness.MsgAck,
		From:  liveness.Addr{ID: "n1", Node: true},
		To:    liveness.Addr{ID: "agg-a", Node: false},
		Token: 11,
	}}
	if !slices.Equal(ackAgg, wantAckAgg) {
		t.Fatalf("Pinged(agg) = %+v, want %+v", ackAgg, wantAckAgg)
	}

	ackNode := eng.Handle(500, liveness.Message{
		Kind:  liveness.MsgPing,
		From:  liveness.Addr{ID: "n2", Node: true},
		To:    liveness.Addr{ID: "n1", Node: true},
		Token: 12,
	})
	wantAckNode := []liveness.Message{{
		Kind:  liveness.MsgAck,
		From:  liveness.Addr{ID: "n1", Node: true},
		To:    liveness.Addr{ID: "n2", Node: true},
		Token: 12,
	}}
	if !slices.Equal(ackNode, wantAckNode) {
		t.Fatalf("Pinged(node) = %+v, want %+v", ackNode, wantAckNode)
	}

	// MsgProbeRequest -> MsgPing addressed to {m.Node, Node: true}.
	pingOut := eng.Handle(600, liveness.Message{
		Kind:  liveness.MsgProbeRequest,
		From:  liveness.Addr{ID: "agg-b", Node: false},
		To:    liveness.Addr{ID: "n1", Node: true},
		Node:  "n3",
		Round: 5,
	})
	wantPing := []liveness.Message{{
		Kind:  liveness.MsgPing,
		From:  liveness.Addr{ID: "n1", Node: true},
		To:    liveness.Addr{ID: "n3", Node: true},
		Token: 1,
	}}
	if !slices.Equal(pingOut, wantPing) {
		t.Fatalf("ProbeRequested -> %+v, want %+v", pingOut, wantPing)
	}

	// Tick at t=700 (PingInterval=100) re-sends MsgPing with token 1.
	resendOut := eng.Tick(700)
	if !slices.Equal(resendOut, wantPing) {
		t.Fatalf("Tick(700) resend = %+v, want %+v", resendOut, wantPing)
	}

	// MsgAck -> MsgProbeResult{OK: true}.
	resOK := eng.Handle(750, liveness.Message{
		Kind:  liveness.MsgAck,
		From:  liveness.Addr{ID: "n3", Node: true},
		To:    liveness.Addr{ID: "n1", Node: true},
		Token: 1,
	})
	wantResOK := []liveness.Message{{
		Kind:  liveness.MsgProbeResult,
		From:  liveness.Addr{ID: "n1", Node: true},
		To:    liveness.Addr{ID: "agg-b", Node: false},
		Node:  "n3",
		Round: 5,
		OK:    true,
	}}
	if !slices.Equal(resOK, wantResOK) {
		t.Fatalf("Acked -> %+v, want %+v", resOK, wantResOK)
	}

	// Another MsgProbeRequest at t=750 (token 2) that expires at t=1550.
	ping2 := eng.Handle(750, liveness.Message{
		Kind:  liveness.MsgProbeRequest,
		From:  liveness.Addr{ID: "agg-c", Node: false},
		To:    liveness.Addr{ID: "n1", Node: true},
		Node:  "n4",
		Round: 9,
	})
	if len(ping2) != 1 || ping2[0].Token != 2 || !ping2[0].To.Node || ping2[0].To.ID != "n4" {
		t.Fatalf("second ProbeRequested = %+v", ping2)
	}

	// Tick at t=1550 emits heartbeats followed by expired ProbeResult{OK: false} (not re-sent because expired).
	tickMsgs := eng.Tick(1550)
	if len(tickMsgs) != 3 {
		t.Fatalf("Tick(1150) len = %d (%+v), want 3", len(tickMsgs), tickMsgs)
	}
	if tickMsgs[0].Kind != liveness.MsgHeartbeat || tickMsgs[1].Kind != liveness.MsgHeartbeat {
		t.Fatalf("expected heartbeats first, got %+v", tickMsgs)
	}
	wantExpired := liveness.Message{
		Kind:  liveness.MsgProbeResult,
		From:  liveness.Addr{ID: "n1", Node: true},
		To:    liveness.Addr{ID: "agg-c", Node: false},
		Node:  "n4",
		Round: 9,
		OK:    false,
	}
	if tickMsgs[2] != wantExpired {
		t.Fatalf("tickMsgs[2] = %+v, want %+v", tickMsgs[2], wantExpired)
	}

	// Unhandled message kinds are ignored.
	for _, kind := range []liveness.MessageKind{liveness.MsgHeartbeat, liveness.MsgVoteRequest, liveness.MsgVote, liveness.MsgProbeResult, 99} {
		if msgs := eng.Handle(1200, liveness.Message{Kind: kind}); len(msgs) != 0 {
			t.Fatalf("NodeEngine.Handle(kind=%d) = %+v, want nil", kind, msgs)
		}
	}
}

func TestEndToEndEngineLoop(t *testing.T) {
	aggIDs := []string{"agg-a", "agg-b", "agg-c"}
	nodeIDs := []string{"n1", "n2", "n3", "n4"}

	type cluster struct {
		aggs    map[string]*liveness.AggregatorEngine
		nodes   map[string]*liveness.NodeEngine
		records map[string]liveness.Record
		rev     store.Rev
		epoch   int64
	}

	newCluster := func(t *testing.T) *cluster {
		t.Helper()
		aggs := make(map[string]*liveness.AggregatorEngine, len(aggIDs))
		for _, id := range aggIDs {
			eng, err := liveness.NewAggregatorEngine(liveness.DefaultAggregatorEngineConfig(id, aggIDs))
			if err != nil {
				t.Fatalf("NewAggregatorEngine(%s): %v", id, err)
			}
			aggs[id] = eng
		}
		nodes := make(map[string]*liveness.NodeEngine, len(nodeIDs))
		for _, id := range nodeIDs {
			cfg := agent.DefaultConfig(id, func(n string) []string {
				return hrw.Ranked(n, aggIDs)
			})
			eng, err := liveness.NewNodeEngine(cfg)
			if err != nil {
				t.Fatalf("NewNodeEngine(%s): %v", id, err)
			}
			nodes[id] = eng
		}
		records := make(map[string]liveness.Record, len(nodeIDs))
		for _, id := range nodeIDs {
			rack := "r1"
			if id == "n4" {
				rack = "r2"
			}
			rec := liveness.Record{
				Node:   id,
				State:  detector.StatePending,
				Since:  1700000000000,
				Rack:   rack,
				ModRev: 1,
			}
			records[id] = rec
			for _, eng := range aggs {
				eng.Observed(0, rec)
			}
		}
		return &cluster{
			aggs:    aggs,
			nodes:   nodes,
			records: records,
			rev:     1,
			epoch:   1700000000000,
		}
	}

	deliver := func(c *cluster, now int64, queue []liveness.Message, drop func(liveness.Message) bool) {
		for len(queue) > 0 {
			m := queue[0]
			queue = queue[1:]
			if drop != nil && drop(m) {
				continue
			}
			if m.To.Node {
				if n, ok := c.nodes[m.To.ID]; ok {
					queue = append(queue, n.Handle(now, m)...)
				}
			} else {
				if a, ok := c.aggs[m.To.ID]; ok {
					queue = append(queue, a.Handle(now, m)...)
				}
			}
		}
	}

	step := func(c *cluster, now int64, drop func(liveness.Message) bool) map[string][]liveness.Proposal {
		var queue []liveness.Message
		for _, id := range nodeIDs {
			queue = append(queue, c.nodes[id].Tick(now)...)
		}
		deliver(c, now, queue, drop)

		propsByAgg := make(map[string][]liveness.Proposal, len(aggIDs))
		var aggMsgs []liveness.Message
		for _, id := range aggIDs {
			msgs, props := c.aggs[id].Tick(now)
			aggMsgs = append(aggMsgs, msgs...)
			if len(props) > 0 {
				propsByAgg[id] = props
			}
		}
		deliver(c, now, aggMsgs, drop)
		return propsByAgg
	}

	t.Run("StartupStopAndPingOnly", func(t *testing.T) {
		c := newCluster(t)
		readyProposals := make(map[string]map[string]bool)
		for _, id := range aggIDs {
			readyProposals[id] = make(map[string]bool)
		}

		for now := int64(0); now <= 2500; now += 100 {
			propsByAgg := step(c, now, nil)
			for aggID, props := range propsByAgg {
				for _, p := range props {
					if p.Kind != liveness.ProposeReady {
						t.Fatalf("unexpected proposal kind %v during startup at t=%d", p, now)
					}
					readyProposals[aggID][p.Node] = true
				}
			}
		}
		for _, aggID := range aggIDs {
			for _, nodeID := range nodeIDs {
				if !readyProposals[aggID][nodeID] {
					t.Fatalf("aggregator %s did not propose Ready for owned node %s after 3 heartbeats", aggID, nodeID)
				}
			}
		}

		// Apply Ready state to all records and feed Observed to all aggregators.
		c.rev++
		for _, id := range nodeIDs {
			rec := c.records[id]
			rec.State = detector.StateReady
			rec.Since = c.epoch + 2500
			rec.ModRev = c.rev
			c.records[id] = rec
			for _, agg := range c.aggs {
				agg.Observed(2500, rec)
			}
		}

		// Stop delivering n1's messages -> some owner proposes NotReady for n1 within 30s and for no other node.
		var detectedN1 bool
		for now := int64(2600); now <= 32600; now += 100 {
			propsByAgg := step(c, now, func(m liveness.Message) bool {
				return (m.From.Node && m.From.ID == "n1") || (m.To.Node && m.To.ID == "n1")
			})
			for _, props := range propsByAgg {
				for _, p := range props {
					if p.Kind == liveness.ProposeNotReady {
						if p.Node != "n1" {
							t.Fatalf("unexpected ProposeNotReady for %s at t=%d", p.Node, now)
						}
						detectedN1 = true
					}
				}
			}
			if detectedN1 {
				break
			}
		}
		if !detectedN1 {
			t.Fatal("no aggregator proposed NotReady for stopped node n1 within 30 s")
		}

		// With n1 still answering pings (only its heartbeats dropped), no ProposeNotReady in 60s.
		c2 := newCluster(t)
		c2.rev++
		for _, id := range nodeIDs {
			rec := c2.records[id]
			rec.State = detector.StateReady
			rec.Since = c2.epoch
			rec.ModRev = c2.rev
			c2.records[id] = rec
			for _, agg := range c2.aggs {
				agg.Observed(0, rec)
			}
		}
		for now := int64(0); now <= 60000; now += 100 {
			propsByAgg := step(c2, now, func(m liveness.Message) bool {
				return m.Kind == liveness.MsgHeartbeat && m.From.Node && m.From.ID == "n1"
			})
			for aggID, props := range propsByAgg {
				for _, p := range props {
					if p.Kind == liveness.ProposeNotReady {
						t.Fatalf("aggregator %s proposed NotReady for %s at t=%d despite ping replies", aggID, p.Node, now)
					}
				}
			}
		}
	})

	t.Run("OneOwnerHeartbeatsWith5PctLoss10Min", func(t *testing.T) {
		c := newCluster(t)
		c.rev++
		for _, id := range nodeIDs {
			rec := c.records[id]
			rec.State = detector.StateReady
			rec.Since = c.epoch
			rec.ModRev = c.rev
			c.records[id] = rec
			for _, agg := range c.aggs {
				agg.Observed(0, rec)
			}
		}

		keptOwner := hrw.Ranked("n1", aggIDs)[0]
		rng := rand.New(rand.NewPCG(1, 2))
		for now := int64(0); now <= 600_000; now += 100 {
			propsByAgg := step(c, now, func(m liveness.Message) bool {
				if m.Kind == liveness.MsgHeartbeat && m.From.Node && m.From.ID == "n1" && m.To.ID != keptOwner {
					return true
				}
				return rng.IntN(20) == 0
			})
			for aggID, props := range propsByAgg {
				for _, p := range props {
					if p.Kind == liveness.ProposeNotReady {
						t.Fatalf("aggregator %s proposed NotReady for %s at t=%d", aggID, p.Node, now)
					}
				}
			}
		}
	})
}
