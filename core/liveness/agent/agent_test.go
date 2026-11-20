package agent

import (
	"errors"
	"slices"
	"testing"
)

func validConfig() Config {
	return DefaultConfig("node-1", func(node string) []string {
		return []string{"agg-a", "agg-b", "agg-c", "agg-d"}
	})
}

func TestDefaultConfigAndValidate(t *testing.T) {
	ranked := func(node string) []string { return []string{"agg-a"} }
	cfg := DefaultConfig("node-1", ranked)
	if cfg.Node != "node-1" || cfg.Interval != 1000 || cfg.Owners != 3 ||
		cfg.PingTimeout != 800 || cfg.PingInterval != 100 || cfg.Ranked == nil {
		t.Fatalf("unexpected DefaultConfig: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("DefaultConfig.Validate() = %v, want nil", err)
	}

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "empty Node", mutate: func(c *Config) { c.Node = "" }},
		{name: "zero Interval", mutate: func(c *Config) { c.Interval = 0 }},
		{name: "negative Interval", mutate: func(c *Config) { c.Interval = -5 }},
		{name: "zero Owners", mutate: func(c *Config) { c.Owners = 0 }},
		{name: "negative Owners", mutate: func(c *Config) { c.Owners = -1 }},
		{name: "zero PingTimeout", mutate: func(c *Config) { c.PingTimeout = 0 }},
		{name: "negative PingTimeout", mutate: func(c *Config) { c.PingTimeout = -10 }},
		{name: "zero PingInterval", mutate: func(c *Config) { c.PingInterval = 0 }},
		{name: "negative PingInterval", mutate: func(c *Config) { c.PingInterval = -1 }},
		{name: "nil Ranked", mutate: func(c *Config) { c.Ranked = nil }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Validate() = %v, want ErrInvalidConfig", err)
			}
			if a, newErr := New(cfg); newErr == nil || a != nil {
				t.Fatalf("New() = (%v, %v), want (nil, error)", a, newErr)
			}
		})
	}
}

func TestTickHeartbeatsAndInterval(t *testing.T) {
	a, err := New(validConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	// First Tick always sends, even at now == 0.
	got := a.Tick(0)
	wantHB := []Out{
		{Kind: OutHeartbeat, To: "agg-a"},
		{Kind: OutHeartbeat, To: "agg-b"},
		{Kind: OutHeartbeat, To: "agg-c"},
	}
	if !slices.Equal(got, wantHB) {
		t.Fatalf("Tick(0) = %+v, want %+v", got, wantHB)
	}

	// Before next (1000), Tick emits nothing.
	if got := a.Tick(500); len(got) != 0 {
		t.Fatalf("Tick(500) = %+v, want empty", got)
	}
	if got := a.Tick(999); len(got) != 0 {
		t.Fatalf("Tick(999) = %+v, want empty", got)
	}

	// At next == 1000, emits heartbeats and sets next = 2000.
	if got := a.Tick(1000); !slices.Equal(got, wantHB) {
		t.Fatalf("Tick(1000) = %+v, want %+v", got, wantHB)
	}

	// Jump past multiple intervals: only one set of heartbeats, next = 3500 + 1000 = 4500.
	if got := a.Tick(3500); !slices.Equal(got, wantHB) {
		t.Fatalf("Tick(3500) = %+v, want %+v", got, wantHB)
	}
	if got := a.Tick(4499); len(got) != 0 {
		t.Fatalf("Tick(4499) = %+v, want empty", got)
	}
	if got := a.Tick(4500); !slices.Equal(got, wantHB) {
		t.Fatalf("Tick(4500) = %+v, want %+v", got, wantHB)
	}
}

func TestFewerAggregatorsThanOwnersAndDuplicates(t *testing.T) {
	cfg := validConfig()
	cfg.Owners = 4
	cfg.Ranked = func(string) []string {
		return []string{"agg-b", "agg-a", "agg-b"}
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	got := a.Tick(10)
	want := []Out{
		{Kind: OutHeartbeat, To: "agg-b"},
		{Kind: OutHeartbeat, To: "agg-a"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Tick(10) with fewer deduplicated aggregators = %+v, want %+v", got, want)
	}

	// Duplicates before enough unique aggregators are found.
	cfg2 := validConfig()
	cfg2.Owners = 3
	cfg2.Ranked = func(string) []string {
		return []string{"agg-c", "agg-c", "agg-a", "agg-c", "agg-b", "agg-d"}
	}
	a2, err := New(cfg2)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	got2 := a2.Tick(10)
	want2 := []Out{
		{Kind: OutHeartbeat, To: "agg-c"},
		{Kind: OutHeartbeat, To: "agg-a"},
		{Kind: OutHeartbeat, To: "agg-b"},
	}
	if !slices.Equal(got2, want2) {
		t.Fatalf("Tick(10) with duplicates = %+v, want %+v", got2, want2)
	}

	// Zero aggregators available.
	cfg3 := validConfig()
	cfg3.Ranked = func(string) []string { return nil }
	a3, err := New(cfg3)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if got3 := a3.Tick(10); len(got3) != 0 {
		t.Fatalf("Tick(10) with no aggregators = %+v, want empty", got3)
	}
}

func TestPinged(t *testing.T) {
	a, err := New(validConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	gotAgg := a.Pinged(100, "agg-a", false, 77)
	wantAgg := []Out{{Kind: OutAck, To: "agg-a", ToNode: false, Token: 77}}
	if !slices.Equal(gotAgg, wantAgg) {
		t.Fatalf("Pinged(agg) = %+v, want %+v", gotAgg, wantAgg)
	}

	gotNode := a.Pinged(101, "node-9", true, 88)
	wantNode := []Out{{Kind: OutAck, To: "node-9", ToNode: true, Token: 88}}
	if !slices.Equal(gotNode, wantNode) {
		t.Fatalf("Pinged(node) = %+v, want %+v", gotNode, wantNode)
	}
}

func TestProbeRequestedAndAcked(t *testing.T) {
	a, err := New(validConfig())
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	p1 := a.ProbeRequested(100, "agg-a", false, "node-2", 5)
	wantP1 := []Out{{Kind: OutPing, To: "node-2", ToNode: true, Token: 1}}
	if !slices.Equal(p1, wantP1) {
		t.Fatalf("ProbeRequested 1 = %+v, want %+v", p1, wantP1)
	}

	p2 := a.ProbeRequested(110, "node-7", true, "node-3", 9)
	wantP2 := []Out{{Kind: OutPing, To: "node-3", ToNode: true, Token: 2}}
	if !slices.Equal(p2, wantP2) {
		t.Fatalf("ProbeRequested 2 = %+v, want %+v", p2, wantP2)
	}

	// Wrong sender emits nothing and keeps the probe pending.
	if got := a.Acked(120, "node-other", 1); len(got) != 0 {
		t.Fatalf("Acked(wrong sender) = %+v, want empty", got)
	}

	// Right token & sender emits ProbeResult OK=true to original requester (aggregator).
	res1 := a.Acked(150, "node-2", 1)
	wantRes1 := []Out{{
		Kind:   OutProbeResult,
		To:     "agg-a",
		ToNode: false,
		Target: "node-2",
		Round:  5,
		OK:     true,
	}}
	if !slices.Equal(res1, wantRes1) {
		t.Fatalf("Acked(1) = %+v, want %+v", res1, wantRes1)
	}

	// Second Ack for same token emits nothing.
	if resAgain := a.Acked(160, "node-2", 1); len(resAgain) != 0 {
		t.Fatalf("second Acked(1) = %+v, want empty", resAgain)
	}

	// Right token & sender for node requester preserves ToNode=true and Round.
	res2 := a.Acked(200, "node-3", 2)
	wantRes2 := []Out{{
		Kind:   OutProbeResult,
		To:     "node-7",
		ToNode: true,
		Target: "node-3",
		Round:  9,
		OK:     true,
	}}
	if !slices.Equal(res2, wantRes2) {
		t.Fatalf("Acked(2) = %+v, want %+v", res2, wantRes2)
	}
}

func TestLostFirstPingAckedAfterResend(t *testing.T) {
	a, err := New(validConfig()) // PingTimeout 800, PingInterval 100
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	_ = a.Tick(0) // initial heartbeats; next heartbeat at 1000

	first := a.ProbeRequested(100, "agg-a", false, "node-2", 7)
	wantPing := []Out{{Kind: OutPing, To: "node-2", ToNode: true, Token: 1}}
	if !slices.Equal(first, wantPing) {
		t.Fatalf("ProbeRequested = %+v, want %+v", first, wantPing)
	}

	// Before PingInterval (199 < 200), Tick emits nothing.
	if got := a.Tick(199); len(got) != 0 {
		t.Fatalf("Tick(199) = %+v, want empty", got)
	}

	// At 200, Tick re-sends the ping with the same token and ToNode: true.
	got200 := a.Tick(200)
	if !slices.Equal(got200, wantPing) {
		t.Fatalf("Tick(200) = %+v, want %+v", got200, wantPing)
	}

	// Target acks the re-sent ping.
	gotAck := a.Acked(220, "node-2", got200[0].Token)
	wantAck := []Out{{
		Kind:   OutProbeResult,
		To:     "agg-a",
		ToNode: false,
		Target: "node-2",
		Round:  7,
		OK:     true,
	}}
	if !slices.Equal(gotAck, wantAck) {
		t.Fatalf("Acked(220) = %+v, want %+v", gotAck, wantAck)
	}

	// Probe is complete: later ticks neither re-send nor expire it.
	if got := a.Tick(300); len(got) != 0 {
		t.Fatalf("Tick(300) = %+v, want empty", got)
	}
	if got := a.Tick(500); len(got) != 0 {
		t.Fatalf("Tick(500) = %+v, want empty", got)
	}
}

func TestTickOrderHeartbeatsExpiryAndResends(t *testing.T) {
	cfg := validConfig()
	cfg.PingTimeout = 400
	a, err := New(cfg) // Interval 1000, PingTimeout 400, PingInterval 100
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	// First tick at 0 -> heartbeats emitted, next heartbeat at 1000.
	if got := a.Tick(0); len(got) != 3 {
		t.Fatalf("Tick(0) len = %d, want 3", len(got))
	}

	// tok 1 at 100 -> deadline 500
	// tok 2 at 600 -> deadline 1000 (expires on Tick(1000))
	// tok 3 at 600 -> deadline 1000 (expires on Tick(1000))
	// tok 4 at 850 -> nextPing 950, deadline 1250 (re-sent on Tick(1000))
	// tok 5 at 900 -> nextPing 1000, deadline 1300 (re-sent on Tick(1000))
	_ = a.ProbeRequested(100, "agg-a", false, "node-2", 11)
	_ = a.ProbeRequested(600, "node-8", true, "node-3", 12)
	_ = a.ProbeRequested(600, "agg-b", false, "node-4", 13)
	_ = a.ProbeRequested(850, "agg-c", false, "node-5", 14)
	_ = a.ProbeRequested(900, "node-9", true, "node-6", 15)

	// At 500: tok 1 expires; no heartbeats, no other re-sends due.
	got500 := a.Tick(500)
	want500 := []Out{{
		Kind:   OutProbeResult,
		To:     "agg-a",
		ToNode: false,
		Target: "node-2",
		Round:  11,
		OK:     false,
	}}
	if !slices.Equal(got500, want500) {
		t.Fatalf("Tick(500) = %+v, want %+v", got500, want500)
	}

	// Ack at or after deadline (1000 >= 1000) emits nothing.
	if got := a.Acked(1000, "node-3", 2); len(got) != 0 {
		t.Fatalf("Acked at deadline = %+v, want empty", got)
	}

	// At 1000: heartbeats FIRST, then expired probe results (tok 2, 3), then re-sent pings (tok 4, 5).
	got1000 := a.Tick(1000)
	want1000 := []Out{
		{Kind: OutHeartbeat, To: "agg-a"},
		{Kind: OutHeartbeat, To: "agg-b"},
		{Kind: OutHeartbeat, To: "agg-c"},
		{Kind: OutProbeResult, To: "node-8", ToNode: true, Target: "node-3", Round: 12, OK: false},
		{Kind: OutProbeResult, To: "agg-b", ToNode: false, Target: "node-4", Round: 13, OK: false},
		{Kind: OutPing, To: "node-5", ToNode: true, Token: 4},
		{Kind: OutPing, To: "node-6", ToNode: true, Token: 5},
	}
	if !slices.Equal(got1000, want1000) {
		t.Fatalf("Tick(1000) = %+v, want %+v", got1000, want1000)
	}
}
