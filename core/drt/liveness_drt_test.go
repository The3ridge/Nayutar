//go:build drt

package drt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/liveness/budget"
	"github.com/Kaikei-e/Nayutar/core/liveness/detector"
	"github.com/Kaikei-e/Nayutar/core/liveness/phi"
)

type livenessPhiConfig struct {
	Threshold       int   `json:"threshold"`
	MaxSamples      int   `json:"max_samples"`
	MinStdDev       int64 `json:"min_std_dev"`
	AcceptablePause int64 `json:"acceptable_pause"`
	FirstInterval   int64 `json:"first_interval"`
	MaxInterval     int64 `json:"max_interval"`
}

type livenessConfig struct {
	Self                  string            `json:"self"`
	Owners                int               `json:"owners"`
	PeerProbers           int               `json:"peer_probers"`
	RackProbers           int               `json:"rack_probers"`
	WitnessProbers        int               `json:"witness_probers"`
	MinFailedProbes       int               `json:"min_failed_probes"`
	RoundTimeout          int64             `json:"round_timeout"`
	RetryInterval         int64             `json:"retry_interval"`
	MaxRoundBackoff       int64             `json:"max_round_backoff"`
	EvictionRetryInterval int64             `json:"eviction_retry_interval"`
	SelfSuspectPercent    int               `json:"self_suspect_percent"`
	SelfSuspectMinNodes   int               `json:"self_suspect_min_nodes"`
	ReadyHeartbeats       int               `json:"ready_heartbeats"`
	EvictionGrace         int64             `json:"eviction_grace"`
	RoundBase             uint64            `json:"round_base"`
	Phi                   livenessPhiConfig `json:"phi"`
}

type livenessRankedEntry struct {
	Node        string   `json:"node"`
	Aggregators []string `json:"aggregators"`
}

type livenessOp struct {
	Op        string
	Now       int64
	Node      string
	State     string
	Since     int64
	Rack      string
	Evictable bool
	ModRev    int64
	From      string
	FromNode  bool
	Round     uint64
	Suspect   bool
	Alive     bool
	OK        bool
	Granted   []int64
	Window    int64
	Limit     int
}

func (o livenessOp) MarshalJSON() ([]byte, error) {
	switch o.Op {
	case "observed":
		return json.Marshal(struct {
			Op        string `json:"op"`
			Now       int64  `json:"now"`
			Node      string `json:"node"`
			State     string `json:"state"`
			Since     int64  `json:"since"`
			Rack      string `json:"rack"`
			Evictable bool   `json:"evictable"`
			ModRev    int64  `json:"mod_rev"`
		}{o.Op, o.Now, o.Node, o.State, o.Since, o.Rack, o.Evictable, o.ModRev})
	case "forgotten", "heartbeat":
		return json.Marshal(struct {
			Op   string `json:"op"`
			Now  int64  `json:"now"`
			Node string `json:"node"`
		}{o.Op, o.Now, o.Node})
	case "tick":
		return json.Marshal(struct {
			Op  string `json:"op"`
			Now int64  `json:"now"`
		}{o.Op, o.Now})
	case "vote_requested":
		return json.Marshal(struct {
			Op    string `json:"op"`
			Now   int64  `json:"now"`
			From  string `json:"from"`
			Node  string `json:"node"`
			Round uint64 `json:"round"`
		}{o.Op, o.Now, o.From, o.Node, o.Round})
	case "vote_received":
		return json.Marshal(struct {
			Op      string `json:"op"`
			Now     int64  `json:"now"`
			From    string `json:"from"`
			Node    string `json:"node"`
			Round   uint64 `json:"round"`
			Suspect bool   `json:"suspect"`
			Alive   bool   `json:"alive"`
		}{o.Op, o.Now, o.From, o.Node, o.Round, o.Suspect, o.Alive})
	case "probe_result":
		return json.Marshal(struct {
			Op       string `json:"op"`
			Now      int64  `json:"now"`
			From     string `json:"from"`
			FromNode bool   `json:"from_node"`
			Node     string `json:"node"`
			Round    uint64 `json:"round"`
			OK       bool   `json:"ok"`
		}{o.Op, o.Now, o.From, o.FromNode, o.Node, o.Round, o.OK})
	case "budget":
		granted := o.Granted
		if granted == nil {
			granted = []int64{}
		}
		return json.Marshal(struct {
			Op      string  `json:"op"`
			Granted []int64 `json:"granted"`
			Now     int64   `json:"now"`
			Window  int64   `json:"window"`
			Limit   int     `json:"limit"`
		}{o.Op, granted, o.Now, o.Window, o.Limit})
	default:
		return nil, fmt.Errorf("unknown op %q", o.Op)
	}
}

type livenessCase struct {
	ID     int                   `json:"id"`
	Config livenessConfig        `json:"config"`
	Ranked []livenessRankedEntry `json:"ranked"`
	Ops    []livenessOp          `json:"ops"`
}

type livenessAction struct {
	Kind    string `json:"kind"`
	To      string `json:"to"`
	ToNode  bool   `json:"to_node"`
	Node    string `json:"node"`
	Round   uint64 `json:"round"`
	Suspect bool   `json:"suspect"`
	Alive   bool   `json:"alive"`
	ModRev  int64  `json:"mod_rev"`
}

type livenessNodeSnapshot struct {
	Node          string `json:"node"`
	Streak        int    `json:"streak"`
	Round         uint64 `json:"round"`
	Samples       int    `json:"samples"`
	Last          int64  `json:"last"`
	Backoff       int    `json:"backoff"`
	Heard         bool   `json:"heard"`
	NextRoundAt   int64  `json:"next_round_at"`
	NextReadyAt   int64  `json:"next_ready_at"`
	NextEvictAt   int64  `json:"next_evict_at"`
	NotReadySince int64  `json:"not_ready_since"`
	Doubted       bool   `json:"doubted"`
}

type livenessStep struct {
	Status    string                 `json:"status"`
	Actions   []livenessAction       `json:"actions"`
	Suspended bool                   `json:"suspended"`
	Nodes     []livenessNodeSnapshot `json:"nodes"`
	Granted   []int64                `json:"granted"`
	OK        bool                   `json:"ok"`
}

type livenessResult struct {
	ID    int            `json:"id"`
	Steps []livenessStep `json:"steps"`
}

var (
	livenessAggPool  = []string{"a1", "a2", "a3", "a4", "a5", "a6"}
	livenessNodePool = []string{"n01", "n02", "n03", "n04", "n05", "n06", "n07", "n08", "n09", "n10", "n11", "n12"}
	livenessRackPool = []string{"r1", "r2", "r3", "r4"}
	livenessStates   = []string{"pending", "ready", "notready"}
)

type trackedRoundInfo struct {
	node    string
	round   uint64
	modRev  int64
	voters  []string
	voted   []string
	probers []livenessProberTarget
	failed  []livenessProberTarget
}

type livenessProberTarget struct {
	id     string
	isNode bool
}

func genLivenessConfig(rng *rand.Rand) (livenessConfig, []string, []string, []string, string) {
	profile := "normal"
	switch r := rng.IntN(100); {
	case r < 8:
		profile = "backoff_cap"
	case r < 14:
		profile = "doubted_heal"
	case r < 18:
		profile = "doubted_mid"
	case r < 27:
		profile = "one_or_two_nodes"
	case r < 49:
		profile = "large"
	case r < 59:
		profile = "unowned_witness"
	}

	numAggs := 2 + rng.IntN(5)
	if profile == "doubted_heal" || profile == "doubted_mid" {
		numAggs = 3
	} else if profile == "unowned_witness" {
		numAggs = 5
	}
	aggs := slices.Clone(livenessAggPool[:numAggs])
	self := pick(rng, aggs)

	var numNodes int
	switch profile {
	case "backoff_cap":
		numNodes = 1
	case "doubted_heal", "unowned_witness":
		numNodes = 2
	case "doubted_mid":
		numNodes = 3
	case "one_or_two_nodes":
		numNodes = 1 + rng.IntN(2)
	case "large":
		numNodes = 10 + rng.IntN(3)
	default:
		numNodes = 1 + rng.IntN(12)
	}
	nodes := slices.Clone(livenessNodePool[:numNodes])

	numRacks := 2 + rng.IntN(3) // 2..4 racks
	if profile == "doubted_heal" || profile == "doubted_mid" || profile == "unowned_witness" {
		numRacks = 2
	}
	racks := slices.Clone(livenessRackPool[:numRacks])

	firstInterval := 4 + rng.Int64N(1997)
	minMax := firstInterval + firstInterval/4
	var maxInterval int64
	switch r := rng.IntN(100); {
	case r < 15:
		maxInterval = minMax
	case r < 80:
		maxInterval = min(600000, minMax+rng.Int64N(5001))
	default:
		maxInterval = minMax + rng.Int64N(600000-minMax+1)
	}

	maxSamples := 1 + rng.IntN(8)
	if chance(rng, 20) {
		maxSamples = 1000
	}
	minStdDev := rng.Int64N(301)
	if chance(rng, 10) {
		minStdDev = 0
	}
	acceptablePause := rng.Int64N(3001)
	if chance(rng, 10) {
		acceptablePause = 0
	}
	evictionGrace := rng.Int64N(4001)
	if chance(rng, 10) {
		evictionGrace = 0
	}

	retryInterval := 1 + rng.Int64N(1500)
	if chance(rng, 20) {
		retryInterval = 1 + rng.Int64N(40)
	}
	maxBackoffUpper := min(int64(3600000), retryInterval<<17)
	var maxRoundBackoff int64
	switch r := rng.IntN(100); {
	case r < 10:
		maxRoundBackoff = retryInterval
	case r < 30:
		maxRoundBackoff = maxBackoffUpper
	default:
		maxRoundBackoff = retryInterval + rng.Int64N(maxBackoffUpper-retryInterval+1)
	}

	owners := 1 + rng.IntN(3)
	peerProbers := rng.IntN(3)
	rackProbers := rng.IntN(3)
	witnessProbers := rng.IntN(4) // 0..3
	minFailedProbes := 1 + rng.IntN(3)
	roundTimeout := 1 + rng.Int64N(1500)
	selfSuspectPct := 1 + rng.IntN(100)
	selfSuspectMin := 1 + rng.IntN(4)
	threshold := 1 + rng.IntN(16)

	switch profile {
	case "large":
		witnessProbers = 1 + rng.IntN(3)
		selfSuspectPct = 20 + rng.IntN(41)
		selfSuspectMin = 2 + rng.IntN(3)
	case "backoff_cap":
		owners = 1
		peerProbers = 1
		rackProbers = 0
		witnessProbers = 0
		minFailedProbes = 1
		roundTimeout = 100
		retryInterval = 1 + rng.Int64N(25)
		maxRoundBackoff = retryInterval << 17
		firstInterval = 100
		maxInterval = 500
		acceptablePause = 200
		minStdDev = 50
		threshold = 4
	case "doubted_heal":
		owners = 1
		peerProbers = 2
		rackProbers = 2
		witnessProbers = 3
		minFailedProbes = 1 + rng.IntN(2)
		roundTimeout = 1000
		retryInterval = 1000
		maxRoundBackoff = 60000
		selfSuspectPct = 30
		selfSuspectMin = 3
		firstInterval = 1000
		maxInterval = 10000
		acceptablePause = 3000
		minStdDev = 100
		threshold = 8
	case "doubted_mid":
		owners = 1
		peerProbers = 2
		rackProbers = 0
		witnessProbers = 0
		minFailedProbes = 1
		roundTimeout = 5000
		retryInterval = 1000
		maxRoundBackoff = 60000
		selfSuspectPct = 50
		selfSuspectMin = 2
		firstInterval = 1000
		maxInterval = 10000
		acceptablePause = 3000
		minStdDev = 100
		threshold = 8
	case "unowned_witness":
		owners = 1 + rng.IntN(3)
		peerProbers = 2
		rackProbers = 2
		witnessProbers = 1 + rng.IntN(3)
		minFailedProbes = 1 + rng.IntN(2)
		roundTimeout = 1000
		retryInterval = 1000
		maxRoundBackoff = 60000
		selfSuspectPct = 30
		selfSuspectMin = 3
		firstInterval = 1000
		maxInterval = 10000
		acceptablePause = 3000
		minStdDev = 100
		threshold = 8
	case "one_or_two_nodes":
		owners = 1
		peerProbers = 1
		rackProbers = 0
		witnessProbers = 0
		minFailedProbes = 1
		selfSuspectPct = 30
		selfSuspectMin = 3
		if numNodes == 1 {
			selfSuspectMin = 2
		}
	}

	var roundBase uint64
	if chance(rng, 25) {
		roundBase = 1 + uint64(rng.Int64N(1<<40))
	}

	cfg := livenessConfig{
		Self:                  self,
		Owners:                owners,
		PeerProbers:           peerProbers,
		RackProbers:           rackProbers,
		WitnessProbers:        witnessProbers,
		MinFailedProbes:       minFailedProbes,
		RoundTimeout:          roundTimeout,
		RetryInterval:         retryInterval,
		MaxRoundBackoff:       maxRoundBackoff,
		EvictionRetryInterval: 1 + rng.Int64N(500),
		SelfSuspectPercent:    selfSuspectPct,
		SelfSuspectMinNodes:   selfSuspectMin,
		ReadyHeartbeats:       1 + rng.IntN(4),
		EvictionGrace:         evictionGrace,
		RoundBase:             roundBase,
		Phi: livenessPhiConfig{
			Threshold:       threshold,
			MaxSamples:      maxSamples,
			MinStdDev:       minStdDev,
			AcceptablePause: acceptablePause,
			FirstInterval:   firstInterval,
			MaxInterval:     maxInterval,
		},
	}
	return cfg, aggs, nodes, racks, profile
}

func genLivenessRanked(rng *rand.Rand, nodes, aggs []string, self string, profile string) []livenessRankedEntry {
	entries := []livenessRankedEntry{}
	forceOwned := profile == "large" || profile == "backoff_cap" || profile == "one_or_two_nodes" || profile == "doubted_heal" || profile == "doubted_mid"
	for nodeIdx, node := range nodes {
		if profile == "unowned_witness" {
			perm := rng.Perm(len(aggs))
			sub := make([]string, 0, len(aggs))
			for _, idx := range perm {
				if nodeIdx > 0 && aggs[idx] == self {
					continue
				}
				sub = append(sub, aggs[idx])
			}
			if nodeIdx == 0 && sub[0] != self {
				idx := slices.Index(sub, self)
				sub[0], sub[idx] = sub[idx], sub[0]
			}
			entries = append(entries, livenessRankedEntry{Node: node, Aggregators: sub})
			continue
		}
		if !forceOwned && chance(rng, 10) {
			continue
		}
		if !forceOwned && chance(rng, 5) {
			entries = append(entries, livenessRankedEntry{Node: node, Aggregators: []string{}})
			continue
		}
		perm := rng.Perm(len(aggs))
		k := 1 + rng.IntN(len(aggs))
		if profile == "backoff_cap" || profile == "one_or_two_nodes" || profile == "doubted_heal" || profile == "doubted_mid" {
			k = len(aggs)
		}
		sub := make([]string, 0, k+1)
		for _, idx := range perm[:k] {
			sub = append(sub, aggs[idx])
		}
		if forceOwned {
			if !slices.Contains(sub, self) {
				sub = append([]string{self}, sub...)
			} else if (profile == "backoff_cap" || profile == "one_or_two_nodes" || profile == "doubted_heal" || profile == "doubted_mid") && sub[0] != self {
				idx := slices.Index(sub, self)
				sub[0], sub[idx] = sub[idx], sub[0]
			}
		} else if chance(rng, 20) {
			filtered := make([]string, 0, len(sub))
			for _, id := range sub {
				if id != self {
					filtered = append(filtered, id)
				}
			}
			sub = filtered
		}
		if len(sub) > 0 && chance(rng, 20) {
			dup := sub[rng.IntN(len(sub))]
			pos := rng.IntN(len(sub) + 1)
			if (profile == "backoff_cap" || profile == "one_or_two_nodes" || profile == "doubted_heal" || profile == "doubted_mid") && pos == 0 {
				pos = 1
			}
			sub = slices.Insert(sub, pos, dup)
		}
		entries = append(entries, livenessRankedEntry{Node: node, Aggregators: sub})
		if chance(rng, 5) {
			entries = append(entries, livenessRankedEntry{Node: node, Aggregators: []string{}})
		}
	}
	return entries
}

func genLivenessTime(rng *rand.Rand, monoNow int64, cfg livenessConfig, forceStep string) (opNow, nextMono int64) {
	if forceStep == "" && monoNow > 0 && chance(rng, 8) {
		back := 1 + rng.Int64N(min(monoNow, 500))
		return monoNow - back, monoNow
	}
	var dt int64
	switch forceStep {
	case "heartbeat":
		f := cfg.Phi.FirstInterval
		lo := max(int64(1), f-f/5)
		hi := max(lo, f+f/5)
		dt = lo + rng.Int64N(hi-lo+1)
	case "round_timeout":
		dt = cfg.RoundTimeout + rng.Int64N(100)
	case "large":
		dt = cfg.Phi.AcceptablePause + cfg.Phi.MaxInterval + 9*max(cfg.Phi.MaxInterval, cfg.Phi.MinStdDev) + 200
	default:
		switch r := rng.IntN(100); {
		case r < 15:
			dt = 0
		case r < 40:
			dt = 1 + rng.Int64N(50)
		case r < 72:
			f := cfg.Phi.FirstInterval
			lo := max(int64(1), f-f/5)
			hi := max(lo, f+f/5)
			dt = lo + rng.Int64N(hi-lo+1)
		default:
			dt = cfg.Phi.AcceptablePause + cfg.Phi.MaxInterval + 9*max(cfg.Phi.MaxInterval, cfg.Phi.MinStdDev) + 200
		}
	}
	nextMono = monoNow + dt
	return nextMono, nextMono
}

func genLivenessBudgetOp(rng *rand.Rand) livenessOp {
	window := rng.Int64N(101)
	limit := rng.IntN(4)
	now := rng.Int64N(201)
	count := rng.IntN(8)
	granted := make([]int64, 0, count)
	for range count {
		switch rng.IntN(5) {
		case 0:
			granted = append(granted, max(int64(0), now-window))
		case 1:
			granted = append(granted, max(int64(0), now-window+1))
		case 2:
			granted = append(granted, now)
		default:
			granted = append(granted, rng.Int64N(221))
		}
	}
	if chance(rng, 50) {
		slices.Sort(granted)
	} else {
		rng.Shuffle(len(granted), func(i, j int) {
			granted[i], granted[j] = granted[j], granted[i]
		})
	}
	return livenessOp{
		Op:      "budget",
		Granted: granted,
		Now:     now,
		Window:  window,
		Limit:   limit,
	}
}

func generateLivenessCases(seed uint64, n int) []livenessCase {
	rng := rand.New(rand.NewPCG(seed, 0x6c6976656e657373))
	cases := make([]livenessCase, n)
	for i := range n {
		cfg, aggs, nodes, racks, profile := genLivenessConfig(rng)
		ranked := genLivenessRanked(rng, nodes, aggs, cfg.Self, profile)
		tc := livenessCase{
			ID:     i,
			Config: cfg,
			Ranked: ranked,
		}
		detCfg := toDetectorConfig(tc)
		core, err := detector.New(detCfg)
		if err != nil {
			panic(fmt.Sprintf("generated invalid detector config: %v", err))
		}

		rankedMap := make(map[string][]string, len(ranked))
		for _, entry := range ranked {
			if _, exists := rankedMap[entry.Node]; !exists {
				rankedMap[entry.Node] = entry.Aggregators
			}
		}
		ownsNode := func(node string) bool {
			raw := rankedMap[node]
			seen := make(map[string]struct{}, len(raw))
			count := 0
			for _, id := range raw {
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				if id == cfg.Self {
					return true
				}
				count++
				if count >= cfg.Owners {
					return false
				}
			}
			return false
		}

		monoNow := int64(rng.IntN(100))
		modRev := int64(1)
		knownRec := make(map[string]detector.Record, len(nodes))
		activeRounds := make(map[string]trackedRoundInfo, len(nodes))
		var recentRounds []trackedRoundInfo
		var recentEvictNode string
		var recentEvictUntil int64

		numOps := 40 + rng.IntN(101)
		switch profile {
		case "large":
			numOps = 80 + rng.IntN(81)
		case "backoff_cap":
			numOps = 50 + rng.IntN(21)
		}
		ops := make([]livenessOp, 0, numOps)

		initLimit := min(3, len(nodes))
		if profile == "large" || profile == "one_or_two_nodes" || profile == "doubted_heal" || profile == "doubted_mid" || profile == "unowned_witness" {
			initLimit = len(nodes)
		} else if profile == "backoff_cap" {
			initLimit = 1
		}
		var doubtedOpenAt int64
		var unownedOpenAt int64
		unownedSilence := profile == "unowned_witness" && chance(rng, 50)
		unownedWitnessOK := profile == "unowned_witness" && !unownedSilence && chance(rng, 40)

		for stepIdx := range numOps {
			var op livenessOp
			initialObserve := stepIdx < initLimit || (len(knownRec) == 0 && stepIdx < numOps-5)

			switch {
			case initialObserve:
				node := nodes[stepIdx%len(nodes)]
				if stepIdx >= len(nodes) && len(knownRec) > 0 {
					node = pick(rng, nodes)
				}
				var opNow int64
				switch {
				case profile == "doubted_heal" || profile == "unowned_witness":
					opNow = monoNow
				case profile == "doubted_mid":
					if stepIdx == 1 {
						monoNow += 3000
					}
					opNow = monoNow
				default:
					opNow, monoNow = genLivenessTime(rng, monoNow, cfg, "")
				}
				state := pick(rng, livenessStates)
				if profile == "large" || profile == "backoff_cap" || profile == "one_or_two_nodes" || profile == "doubted_heal" || profile == "doubted_mid" || profile == "unowned_witness" {
					state = "ready"
				}
				evictable := state != "notready" && chance(rng, 30)
				if state == "notready" && chance(rng, 20) {
					evictable = true
				}
				op = livenessOp{
					Op:        "observed",
					Now:       opNow,
					Node:      node,
					State:     state,
					Since:     int64(rng.IntN(5000)),
					Rack:      racks[stepIdx%len(racks)],
					Evictable: evictable,
					ModRev:    modRev,
				}
				modRev++

			case profile == "doubted_heal" && stepIdx == 2:
				monoNow += 5404
				doubtedOpenAt = monoNow
				op = livenessOp{
					Op:  "tick",
					Now: monoNow,
				}

			case profile == "doubted_heal" && (stepIdx == 3 || stepIdx == 4):
				rnd := activeRounds["n01"]
				p := rnd.probers[stepIdx-3]
				monoNow += 50
				op = livenessOp{
					Op:       "probe_result",
					Now:      monoNow,
					From:     p.id,
					FromNode: p.isNode,
					Node:     "n01",
					Round:    rnd.round,
					OK:       false,
				}

			case profile == "doubted_heal" && stepIdx == 5:
				monoNow = doubtedOpenAt + cfg.RoundTimeout - 10
				op = livenessOp{
					Op:   "heartbeat",
					Now:  monoNow,
					Node: "n02",
				}

			case profile == "doubted_heal" && stepIdx == 6:
				monoNow = doubtedOpenAt + cfg.RoundTimeout
				op = livenessOp{
					Op:  "tick",
					Now: monoNow,
				}

			case profile == "doubted_mid" && stepIdx == 3:
				monoNow += 2404
				doubtedOpenAt = monoNow
				op = livenessOp{
					Op:  "tick",
					Now: monoNow,
				}

			case profile == "doubted_mid" && stepIdx == 4:
				rnd := activeRounds["n01"]
				p := rnd.probers[0]
				monoNow += 96
				op = livenessOp{
					Op:       "probe_result",
					Now:      monoNow,
					From:     p.id,
					FromNode: p.isNode,
					Node:     "n01",
					Round:    rnd.round,
					OK:       false,
				}

			case profile == "doubted_mid" && stepIdx == 5:
				monoNow = doubtedOpenAt + 3000
				op = livenessOp{
					Op:  "tick",
					Now: monoNow,
				}

			case profile == "doubted_mid" && (stepIdx == 6 || stepIdx == 7):
				if stepIdx == 6 {
					monoNow = doubtedOpenAt + 3596
				}
				node := "n02"
				if stepIdx == 7 {
					node = "n03"
				}
				op = livenessOp{
					Op:   "heartbeat",
					Now:  monoNow,
					Node: node,
				}

			case profile == "doubted_mid" && stepIdx == 8:
				monoNow = doubtedOpenAt + cfg.RoundTimeout
				op = livenessOp{
					Op:  "tick",
					Now: monoNow,
				}

			case profile == "unowned_witness" && stepIdx == 2:
				monoNow += 5404
				unownedOpenAt = monoNow
				op = livenessOp{
					Op:  "tick",
					Now: monoNow,
				}

			case profile == "unowned_witness" && unownedSilence && stepIdx == 3:
				rnd := activeRounds["n01"]
				monoNow += 50
				if len(rnd.voters) > 0 {
					op = livenessOp{
						Op:      "vote_received",
						Now:     monoNow,
						From:    rnd.voters[0],
						Node:    "n01",
						Round:   rnd.round,
						Suspect: true,
						Alive:   false,
					}
				} else {
					op = livenessOp{
						Op:    "vote_requested",
						Now:   monoNow,
						From:  aggs[1],
						Node:  "n01",
						Round: rnd.round,
					}
				}

			case profile == "unowned_witness" && unownedSilence && (stepIdx == 4 || stepIdx == 5):
				rnd := activeRounds["n01"]
				monoNow += 50
				var aggProbers []livenessProberTarget
				for _, p := range rnd.probers {
					if !p.isNode {
						aggProbers = append(aggProbers, p)
					}
				}
				p := aggProbers[stepIdx-4]
				op = livenessOp{
					Op:       "probe_result",
					Now:      monoNow,
					From:     p.id,
					FromNode: false,
					Node:     "n01",
					Round:    rnd.round,
					OK:       false,
				}

			case profile == "unowned_witness" && unownedSilence && stepIdx == 6:
				monoNow = unownedOpenAt + cfg.RoundTimeout
				op = livenessOp{
					Op:  "tick",
					Now: monoNow,
				}

			case profile == "unowned_witness" && !unownedSilence && stepIdx == 3:
				rnd := activeRounds["n01"]
				monoNow += 50
				if len(rnd.voters) > 0 {
					op = livenessOp{
						Op:      "vote_received",
						Now:     monoNow,
						From:    rnd.voters[0],
						Node:    "n01",
						Round:   rnd.round,
						Suspect: true,
						Alive:   false,
					}
				} else {
					op = livenessOp{
						Op:    "vote_requested",
						Now:   monoNow,
						From:  aggs[1],
						Node:  "n01",
						Round: rnd.round,
					}
				}

			case profile == "unowned_witness" && !unownedSilence && stepIdx == 4:
				rnd := activeRounds["n01"]
				monoNow += 50
				op = livenessOp{
					Op:       "probe_result",
					Now:      monoNow,
					From:     "n02",
					FromNode: true,
					Node:     "n01",
					Round:    rnd.round,
					OK:       unownedWitnessOK,
				}

			case profile == "unowned_witness" && !unownedSilence && stepIdx == 5:
				monoNow = unownedOpenAt + cfg.RoundTimeout
				op = livenessOp{
					Op:  "tick",
					Now: monoNow,
				}

			case profile == "backoff_cap" && stepIdx <= 40:
				if rnd, ok := activeRounds["n01"]; ok && len(rnd.probers) > 0 {
					p := rnd.probers[0]
					monoNow++
					op = livenessOp{
						Op:       "probe_result",
						Now:      monoNow,
						From:     p.id,
						FromNode: p.isNode,
						Node:     "n01",
						Round:    rnd.round,
						OK:       true,
					}
				} else {
					snapNow := core.Snapshot()
					targetNow := monoNow
					for _, ns := range snapNow.Nodes {
						if ns.Node == "n01" {
							targetNow = max(targetNow, max(ns.Last+1000, ns.NextRoundAt))
						}
					}
					monoNow = targetNow
					op = livenessOp{
						Op:  "tick",
						Now: monoNow,
					}
				}

			case profile == "one_or_two_nodes" && stepIdx == initLimit:
				var opNow int64
				opNow, monoNow = genLivenessTime(rng, monoNow, cfg, "large")
				op = livenessOp{
					Op:  "tick",
					Now: opNow,
				}

			case profile == "large" && stepIdx < initLimit+len(nodes)/2:
				// Keep a minority of nodes (across racks) heard and healthy so witness probers exist under self-suspicion.
				idx := stepIdx - initLimit
				node := nodes[idx]
				var opNow int64
				opNow, monoNow = genLivenessTime(rng, monoNow, cfg, "heartbeat")
				op = livenessOp{
					Op:   "heartbeat",
					Now:  opNow,
					Node: node,
				}

			case recentEvictNode != "" && monoNow+1 < recentEvictUntil &&
				knownRec[recentEvictNode].State == detector.StateNotReady && chance(rng, 85):
				monoNow++
				op = livenessOp{
					Op:   "heartbeat",
					Now:  monoNow,
					Node: recentEvictNode,
				}

			case len(activeRounds) > 0 && chance(rng, 60):
				roundKeys := make([]string, 0, len(activeRounds))
				for k := range activeRounds {
					roundKeys = append(roundKeys, k)
				}
				slices.Sort(roundKeys)
				rnd := activeRounds[pick(rng, roundKeys)]

				subRoll := rng.IntN(100)
				switch {
				case subRoll < 8:
					// Occasionally update ModRev on an active round's Ready node to trigger rule 3a.
					var opNow int64
					opNow, monoNow = genLivenessTime(rng, monoNow, cfg, "")
					rec := knownRec[rnd.node]
					op = livenessOp{
						Op:        "observed",
						Now:       opNow,
						Node:      rnd.node,
						State:     "ready",
						Since:     rec.Since,
						Rack:      rec.Rack,
						Evictable: rec.Evictable,
						ModRev:    modRev,
					}
					modRev++
				case subRoll < 42:
					var opNow int64
					opNow, monoNow = genLivenessTime(rng, monoNow, cfg, "")
					from := pick(rng, aggs)
					roundID := rnd.round
					alive := chance(rng, 25)
					if len(rnd.voted) > 0 && chance(rng, 40) {
						from = pick(rng, rnd.voted)
						alive = true
					} else if len(rnd.voters) > 0 && chance(rng, 85) {
						from = pick(rng, rnd.voters)
					} else if chance(rng, 50) {
						roundID++
					}
					op = livenessOp{
						Op:      "vote_received",
						Now:     opNow,
						From:    from,
						Node:    rnd.node,
						Round:   roundID,
						Suspect: chance(rng, 75),
						Alive:   alive,
					}
				case subRoll < 80:
					var opNow int64
					opNow, monoNow = genLivenessTime(rng, monoNow, cfg, "")
					from := pick(rng, aggs)
					fromNode := false
					roundID := rnd.round
					okRes := chance(rng, 28)
					if len(rnd.failed) > 0 && chance(rng, 40) {
						p := pick(rng, rnd.failed)
						from = p.id
						fromNode = p.isNode
						okRes = true
					} else if len(rnd.probers) > 0 && chance(rng, 90) {
						p := pick(rng, rnd.probers)
						from = p.id
						fromNode = p.isNode
						if chance(rng, 8) {
							fromNode = !fromNode
						}
					} else if chance(rng, 50) {
						roundID++
					}
					op = livenessOp{
						Op:       "probe_result",
						Now:      opNow,
						From:     from,
						FromNode: fromNode,
						Node:     rnd.node,
						Round:    roundID,
						OK:       okRes,
					}
				default:
					mode := "round_timeout"
					if chance(rng, 25) {
						mode = ""
					}
					var opNow int64
					opNow, monoNow = genLivenessTime(rng, monoNow, cfg, mode)
					op = livenessOp{
						Op:  "tick",
						Now: opNow,
					}
				}

			default:
				roll := rng.IntN(100)
				switch {
				case roll < 18:
					var opNow int64
					opNow, monoNow = genLivenessTime(rng, monoNow, cfg, "")
					knownKeys := make([]string, 0, len(knownRec))
					for k := range knownRec {
						knownKeys = append(knownKeys, k)
					}
					slices.Sort(knownKeys)
					if len(knownKeys) > 0 && chance(rng, 55) {
						node := pick(rng, knownKeys)
						prev := knownRec[node]
						prevState := livenessStates[prev.State-1]
						switch r := rng.IntN(100); {
						case r < 25:
							// Repeated identical record.
							op = livenessOp{
								Op:        "observed",
								Now:       opNow,
								Node:      node,
								State:     prevState,
								Since:     prev.Since,
								Rack:      prev.Rack,
								Evictable: prev.Evictable,
								ModRev:    prev.ModRev,
							}
						case r < 50:
							// State change with unchanged ModRev.
							altStates := make([]string, 0, 2)
							for _, s := range livenessStates {
								if s != prevState {
									altStates = append(altStates, s)
								}
							}
							nextState := pick(rng, altStates)
							op = livenessOp{
								Op:        "observed",
								Now:       opNow,
								Node:      node,
								State:     nextState,
								Since:     prev.Since,
								Rack:      prev.Rack,
								Evictable: prev.Evictable,
								ModRev:    prev.ModRev,
							}
						case prev.State == detector.StateNotReady && r < 80:
							// NotReady re-observed with a new ModRev.
							op = livenessOp{
								Op:        "observed",
								Now:       opNow,
								Node:      node,
								State:     "notready",
								Since:     int64(rng.IntN(5000)),
								Rack:      prev.Rack,
								Evictable: false,
								ModRev:    modRev,
							}
							modRev++
						default:
							state := pick(rng, livenessStates)
							evictable := chance(rng, 25)
							if state == "notready" && chance(rng, 80) {
								evictable = false
							}
							op = livenessOp{
								Op:        "observed",
								Now:       opNow,
								Node:      node,
								State:     state,
								Since:     int64(rng.IntN(5000)),
								Rack:      pick(rng, racks),
								Evictable: evictable,
								ModRev:    modRev,
							}
							modRev++
						}
					} else {
						node := pick(rng, nodes)
						if chance(rng, 10) {
							node = "n99"
						}
						state := pick(rng, livenessStates)
						evictable := chance(rng, 25)
						if state == "notready" && chance(rng, 80) {
							evictable = false
						}
						op = livenessOp{
							Op:        "observed",
							Now:       opNow,
							Node:      node,
							State:     state,
							Since:     int64(rng.IntN(5000)),
							Rack:      pick(rng, racks),
							Evictable: evictable,
							ModRev:    modRev,
						}
						modRev++
					}

				case roll < 21:
					var opNow int64
					opNow, monoNow = genLivenessTime(rng, monoNow, cfg, "")
					node := pick(rng, nodes)
					if chance(rng, 20) {
						node = "n99"
					}
					op = livenessOp{
						Op:   "forgotten",
						Now:  opNow,
						Node: node,
					}

				case roll < 48:
					var ownedKnown, notOwnedKnown, backedOff, notReadyOwned []string
					snapNow := core.Snapshot()
					for _, ns := range snapNow.Nodes {
						if ns.Backoff > 0 {
							backedOff = append(backedOff, ns.Node)
						}
					}
					for k, rec := range knownRec {
						if ownsNode(k) {
							ownedKnown = append(ownedKnown, k)
							if rec.State == detector.StateNotReady {
								notReadyOwned = append(notReadyOwned, k)
							}
						} else {
							notOwnedKnown = append(notOwnedKnown, k)
						}
					}
					slices.Sort(ownedKnown)
					slices.Sort(notOwnedKnown)
					slices.Sort(backedOff)
					slices.Sort(notReadyOwned)

					node := "n99"
					mode := "heartbeat"
					hbRoll := rng.IntN(100)
					switch {
					case len(backedOff) > 0 && hbRoll < 20:
						node = pick(rng, backedOff)
					case len(notReadyOwned) > 0 && hbRoll < 35:
						node = pick(rng, notReadyOwned)
					case hbRoll < 75 && len(ownedKnown) > 0:
						node = pick(rng, ownedKnown)
						if chance(rng, 20) {
							mode = ""
						}
					case hbRoll < 88 && len(notOwnedKnown) > 0:
						node = pick(rng, notOwnedKnown)
					default:
						if chance(rng, 50) {
							node = pick(rng, nodes)
							if _, exists := knownRec[node]; exists {
								node = "n99"
							}
						}
					}
					var opNow int64
					opNow, monoNow = genLivenessTime(rng, monoNow, cfg, mode)
					op = livenessOp{
						Op:   "heartbeat",
						Now:  opNow,
						Node: node,
					}

				case roll < 74:
					mode := ""
					if chance(rng, 45) {
						mode = "large"
					}
					var opNow int64
					opNow, monoNow = genLivenessTime(rng, monoNow, cfg, mode)
					op = livenessOp{
						Op:  "tick",
						Now: opNow,
					}

				case roll < 82:
					var opNow int64
					opNow, monoNow = genLivenessTime(rng, monoNow, cfg, "")
					node := pick(rng, nodes)
					if len(knownRec) > 0 && chance(rng, 70) {
						knownKeys := make([]string, 0, len(knownRec))
						for k := range knownRec {
							knownKeys = append(knownKeys, k)
						}
						slices.Sort(knownKeys)
						node = pick(rng, knownKeys)
					}
					roundID := 1 + uint64(rng.IntN(10))
					if len(recentRounds) > 0 && chance(rng, 50) {
						roundID = recentRounds[rng.IntN(len(recentRounds))].round
					}
					op = livenessOp{
						Op:    "vote_requested",
						Now:   opNow,
						From:  pick(rng, aggs),
						Node:  node,
						Round: roundID,
					}

				case roll < 87:
					var opNow int64
					opNow, monoNow = genLivenessTime(rng, monoNow, cfg, "")
					node := pick(rng, nodes)
					roundID := 1 + uint64(rng.IntN(5))
					if len(recentRounds) > 0 && chance(rng, 60) {
						r := recentRounds[rng.IntN(len(recentRounds))]
						node = r.node
						roundID = r.round
					}
					op = livenessOp{
						Op:      "vote_received",
						Now:     opNow,
						From:    pick(rng, aggs),
						Node:    node,
						Round:   roundID,
						Suspect: chance(rng, 60),
						Alive:   chance(rng, 30),
					}

				case roll < 92:
					var opNow int64
					opNow, monoNow = genLivenessTime(rng, monoNow, cfg, "")
					node := pick(rng, nodes)
					from := pick(rng, aggs)
					fromNode := chance(rng, 40)
					if fromNode {
						from = pick(rng, nodes)
					}
					roundID := 1 + uint64(rng.IntN(5))
					if len(recentRounds) > 0 && chance(rng, 60) {
						r := recentRounds[rng.IntN(len(recentRounds))]
						node = r.node
						roundID = r.round
					}
					op = livenessOp{
						Op:       "probe_result",
						Now:      opNow,
						From:     from,
						FromNode: fromNode,
						Node:     node,
						Round:    roundID,
						OK:       chance(rng, 35),
					}

				default:
					op = genLivenessBudgetOp(rng)
				}
			}

			ops = append(ops, op)
			acts, _, err := applyLivenessOp(core, op, livenessHooks{}, nil, detCfg)
			if err != nil {
				panic(err)
			}
			switch op.Op {
			case "observed":
				st, parseErr := parseDetectorState(op.State)
				if parseErr != nil {
					panic(parseErr)
				}
				knownRec[op.Node] = detector.Record{
					State:     st,
					Since:     op.Since,
					Rack:      op.Rack,
					Evictable: op.Evictable,
					ModRev:    op.ModRev,
				}
				if info, ok := activeRounds[op.Node]; ok && (op.State != "ready" || op.ModRev != info.modRev) {
					delete(activeRounds, op.Node)
				}
			case "forgotten":
				delete(knownRec, op.Node)
				delete(activeRounds, op.Node)
			case "heartbeat":
				delete(activeRounds, op.Node)
				for _, a := range acts {
					if a.Kind == detector.ActProposeReady && a.Node == recentEvictNode {
						recentEvictNode = ""
					}
				}
			case "vote_received":
				if info, ok := activeRounds[op.Node]; ok && info.round == op.Round && slices.Contains(info.voters, op.From) {
					if op.Alive {
						delete(activeRounds, op.Node)
					} else if !slices.Contains(info.voted, op.From) {
						info.voted = append(info.voted, op.From)
						activeRounds[op.Node] = info
					}
				}
			case "probe_result":
				p := livenessProberTarget{id: op.From, isNode: op.FromNode}
				if info, ok := activeRounds[op.Node]; ok && info.round == op.Round && slices.Contains(info.probers, p) {
					if op.OK {
						delete(activeRounds, op.Node)
					} else if !slices.Contains(info.failed, p) {
						info.failed = append(info.failed, p)
						activeRounds[op.Node] = info
					}
				}
			case "tick":
				for _, a := range acts {
					switch a.Kind {
					case detector.ActVoteRequest:
						info := activeRounds[a.Node]
						info.node = a.Node
						info.round = a.Round
						info.modRev = knownRec[a.Node].ModRev
						info.voters = append(info.voters, a.To)
						activeRounds[a.Node] = info
					case detector.ActProbeRequest:
						info := activeRounds[a.Node]
						info.node = a.Node
						info.round = a.Round
						info.modRev = knownRec[a.Node].ModRev
						info.probers = append(info.probers, livenessProberTarget{id: a.To, isNode: a.ToNode})
						activeRounds[a.Node] = info
					case detector.ActProposeEvictable:
						recentEvictNode = a.Node
						recentEvictUntil = monoNow + cfg.EvictionRetryInterval
					}
				}
			}
			snap := core.Snapshot()
			snapRounds := make(map[string]uint64, len(snap.Nodes))
			for _, ns := range snap.Nodes {
				snapRounds[ns.Node] = ns.Round
			}
			for _, ns := range snap.Nodes {
				info, ok := activeRounds[ns.Node]
				if !ok {
					continue
				}
				if ns.Round == 0 {
					delete(activeRounds, ns.Node)
				} else if ns.Round == info.round {
					recentRounds = append(recentRounds, info)
				}
			}
			for node := range activeRounds {
				if snapRounds[node] == 0 {
					delete(activeRounds, node)
				}
			}
		}

		tc.Ops = ops
		cases[i] = tc
	}
	return cases
}

func encodeLivenessCases(t *testing.T, cases []livenessCase) [][]byte {
	t.Helper()
	encoded := make([][]byte, len(cases))
	for i, tc := range cases {
		if tc.Ranked == nil {
			tc.Ranked = []livenessRankedEntry{}
		}
		if tc.Ops == nil {
			tc.Ops = []livenessOp{}
		}
		b, err := json.Marshal(tc)
		if err != nil {
			t.Fatal(err)
		}
		encoded[i] = b
	}
	return encoded
}

func parseDetectorState(state string) (detector.State, error) {
	switch state {
	case "pending":
		return detector.StatePending, nil
	case "ready":
		return detector.StateReady, nil
	case "notready":
		return detector.StateNotReady, nil
	default:
		return 0, fmt.Errorf("unknown state %q", state)
	}
}

func toDetectorConfig(tc livenessCase) detector.Config {
	rankedMap := make(map[string][]string, len(tc.Ranked))
	for _, entry := range tc.Ranked {
		if _, exists := rankedMap[entry.Node]; !exists {
			rankedMap[entry.Node] = slices.Clone(entry.Aggregators)
		}
	}
	return detector.Config{
		Self:                  tc.Config.Self,
		Owners:                tc.Config.Owners,
		PeerProbers:           tc.Config.PeerProbers,
		RackProbers:           tc.Config.RackProbers,
		WitnessProbers:        tc.Config.WitnessProbers,
		MinFailedProbes:       tc.Config.MinFailedProbes,
		RoundTimeout:          tc.Config.RoundTimeout,
		RetryInterval:         tc.Config.RetryInterval,
		MaxRoundBackoff:       tc.Config.MaxRoundBackoff,
		EvictionRetryInterval: tc.Config.EvictionRetryInterval,
		SelfSuspectPercent:    tc.Config.SelfSuspectPercent,
		SelfSuspectMinNodes:   tc.Config.SelfSuspectMinNodes,
		ReadyHeartbeats:       tc.Config.ReadyHeartbeats,
		EvictionGrace:         tc.Config.EvictionGrace,
		RoundBase:             tc.Config.RoundBase,
		Phi: phi.Config{
			Threshold:       tc.Config.Phi.Threshold,
			MaxSamples:      tc.Config.Phi.MaxSamples,
			MinStdDev:       tc.Config.Phi.MinStdDev,
			AcceptablePause: tc.Config.Phi.AcceptablePause,
			FirstInterval:   tc.Config.Phi.FirstInterval,
			MaxInterval:     tc.Config.Phi.MaxInterval,
		},
		Ranked: func(node string) []string {
			return rankedMap[node]
		},
	}
}

func livenessStatusName(st detector.HeartbeatStatus) (string, error) {
	switch st {
	case detector.HeartbeatAccepted:
		return "accepted", nil
	case detector.HeartbeatUnknownNode:
		return "unknown", nil
	case detector.HeartbeatNotOwner:
		return "notowner", nil
	default:
		return "", fmt.Errorf("unexpected heartbeat status %d", st)
	}
}

func livenessActionKindName(k detector.ActionKind) (string, error) {
	switch k {
	case detector.ActVoteRequest:
		return "vote_request", nil
	case detector.ActVote:
		return "vote", nil
	case detector.ActProbeRequest:
		return "probe_request", nil
	case detector.ActProposeReady:
		return "propose_ready", nil
	case detector.ActProposeNotReady:
		return "propose_notready", nil
	case detector.ActProposeEvictable:
		return "propose_evictable", nil
	default:
		return "", fmt.Errorf("unexpected action kind %d", k)
	}
}

type livenessHooks struct {
	cfgMutate                     func(*detector.Config)
	unansweredProbesAsFail        bool
	ignoreAliveVeto               bool
	backoffNotResetByHB           bool
	roundNotClearedOnModRev       bool
	proposeCurrentModRev          bool
	confirmAggFailWithNodeProbers bool
	noWitnessesWhenNotSuspended   bool
	witnessesTrackedOnly          bool
	rackIncludesSuspected         bool
	aggOnlyConfirmsSuspended      bool
	evictionWhileSuspended        bool
	evictionGraceFromZero         bool
	streakNotResetOnNotReady      bool
	votesLieWhileSuspended        bool
	aliveWithoutHeard             bool
	reverseActions                bool
	budgetOverReportsOKTrue       bool
	budgetKeepsWindowBoundary     bool
	budgetAppendsNowAtEnd         bool
	okAfterFailIgnored            bool
	aliveAfterVoteIgnored         bool
	hbNotResetNextRoundAt         bool
	sharedProposeTimer            bool
	graceNotRestartedOnModRev     bool
	allSuspectedNotSelfSuspicion  bool
	doubtedNotSetAtOpen           bool
	doubtedNotAccumulated         bool
}

type livenessShadowRound struct {
	id       uint64
	deadline int64
	modRev   int64
	voters   []string
	voted    []string
	suspects int
	probers  []livenessProberTarget
	failed   []livenessProberTarget
	doubted  bool
}

type livenessShadowState struct {
	now                    int64
	known                  map[string]detector.Record
	rounds                 map[string]*livenessShadowRound
	notReadySince          map[string]int64
	nextEvictAt            map[string]int64
	sharedProposeAt        map[string]int64
	stickyStreak           map[string]int
	stickyBackoff          map[string]int
	stickyNextRoundAt      map[string]int64
	stickyNotReadySince    map[string]int64
	fakeDoubted            map[string]bool
	realModRev             map[string]int64
	overrideSuspendedFalse bool
}

func newLivenessShadowState() *livenessShadowState {
	return &livenessShadowState{
		known:               make(map[string]detector.Record),
		rounds:              make(map[string]*livenessShadowRound),
		notReadySince:       make(map[string]int64),
		nextEvictAt:         make(map[string]int64),
		sharedProposeAt:     make(map[string]int64),
		stickyStreak:        make(map[string]int),
		stickyBackoff:       make(map[string]int),
		stickyNextRoundAt:   make(map[string]int64),
		stickyNotReadySince: make(map[string]int64),
		fakeDoubted:         make(map[string]bool),
		realModRev:          make(map[string]int64),
	}
}

func applyLivenessOp(core *detector.Core, o livenessOp, hooks livenessHooks, shadow *livenessShadowState, cfg detector.Config) ([]detector.Action, livenessStep, error) {
	step := livenessStep{
		Actions: []livenessAction{},
		Nodes:   []livenessNodeSnapshot{},
		Granted: []int64{},
	}
	if shadow != nil && o.Op != "budget" && o.Now > shadow.now {
		shadow.now = o.Now
	}

	var acts []detector.Action
	switch o.Op {
	case "observed":
		st, err := parseDetectorState(o.State)
		if err != nil {
			return nil, step, err
		}
		modRev := o.ModRev
		var prevSnap map[string]livenessNodeSnapshot
		if shadow != nil {
			prevSnap = snapshotMap(core.Snapshot())
			shadow.realModRev[o.Node] = o.ModRev
			if (hooks.roundNotClearedOnModRev || hooks.proposeCurrentModRev) && o.State == "ready" {
				if rnd := shadow.rounds[o.Node]; rnd != nil && o.ModRev != rnd.modRev {
					modRev = rnd.modRev
				}
			}
			prevRec, had := shadow.known[o.Node]
			if _, tracked := prevSnap[o.Node]; !tracked {
				shadow.notReadySince[o.Node] = shadow.now
			} else if st == detector.StateNotReady && (!had || prevRec.State != detector.StateNotReady || prevRec.ModRev != o.ModRev) {
				if hooks.graceNotRestartedOnModRev && had && prevRec.State == detector.StateNotReady && prevRec.ModRev != o.ModRev {
					if _, hasSticky := shadow.stickyNotReadySince[o.Node]; !hasSticky {
						shadow.stickyNotReadySince[o.Node] = shadow.notReadySince[o.Node]
					}
				} else {
					delete(shadow.stickyNotReadySince, o.Node)
					shadow.notReadySince[o.Node] = shadow.now
				}
				if hooks.streakNotResetOnNotReady {
					shadow.stickyStreak[o.Node] = prevSnap[o.Node].Streak
				}
			} else if st != detector.StateNotReady {
				delete(shadow.stickyNotReadySince, o.Node)
			}
			shadow.known[o.Node] = detector.Record{
				State:     st,
				Since:     o.Since,
				Rack:      o.Rack,
				Evictable: o.Evictable,
				ModRev:    o.ModRev,
			}
		}
		core.Observed(o.Now, o.Node, detector.Record{
			State:     st,
			Since:     o.Since,
			Rack:      o.Rack,
			Evictable: o.Evictable,
			ModRev:    modRev,
		})
		if shadow != nil {
			if rnd := shadow.rounds[o.Node]; rnd != nil && (st != detector.StateReady || modRev != rnd.modRev) {
				delete(shadow.rounds, o.Node)
				delete(shadow.fakeDoubted, o.Node)
			}
		}
	case "forgotten":
		core.Forgotten(o.Now, o.Node)
		if shadow != nil {
			delete(shadow.known, o.Node)
			delete(shadow.rounds, o.Node)
			delete(shadow.notReadySince, o.Node)
			delete(shadow.nextEvictAt, o.Node)
			delete(shadow.sharedProposeAt, o.Node)
			delete(shadow.stickyStreak, o.Node)
			delete(shadow.stickyBackoff, o.Node)
			delete(shadow.stickyNextRoundAt, o.Node)
			delete(shadow.stickyNotReadySince, o.Node)
			delete(shadow.fakeDoubted, o.Node)
			delete(shadow.realModRev, o.Node)
		}
	case "heartbeat":
		var prevBackoff int
		var prevNextRoundAt int64
		if shadow != nil && (hooks.backoffNotResetByHB || hooks.hbNotResetNextRoundAt) {
			if ns, ok := snapshotMap(core.Snapshot())[o.Node]; ok {
				prevBackoff = ns.Backoff
				prevNextRoundAt = ns.NextRoundAt
			}
		}
		var st detector.HeartbeatStatus
		st, acts = core.Heartbeat(o.Now, o.Node)
		var err error
		step.Status, err = livenessStatusName(st)
		if err != nil {
			return nil, step, err
		}
		if shadow != nil && st == detector.HeartbeatAccepted {
			delete(shadow.rounds, o.Node)
			if hooks.backoffNotResetByHB && prevBackoff > 0 {
				shadow.stickyBackoff[o.Node] = prevBackoff
			}
			if hooks.hbNotResetNextRoundAt && prevNextRoundAt > 0 {
				shadow.stickyNextRoundAt[o.Node] = prevNextRoundAt
			}
			if hooks.sharedProposeTimer {
				filtered := acts[:0]
				for _, a := range acts {
					if a.Kind == detector.ActProposeReady {
						if shadow.now < shadow.sharedProposeAt[o.Node] {
							continue
						}
						shadow.sharedProposeAt[o.Node] = shadow.now + cfg.RetryInterval
					}
					filtered = append(filtered, a)
				}
				acts = filtered
			}
		}
	case "tick":
		if shadow != nil {
			shadow.overrideSuspendedFalse = false
		}
		if shadow != nil && hooks.unansweredProbesAsFail {
			for node, rnd := range shadow.rounds {
				if shadow.now >= rnd.deadline {
					for _, p := range rnd.probers {
						if !slices.Contains(rnd.failed, p) {
							core.ProbeResult(o.Now, p.id, p.isNode, node, rnd.id, false)
						}
					}
				}
			}
		}
		var expiring map[string]*livenessShadowRound
		var prevSnapTick map[string]livenessNodeSnapshot
		if shadow != nil {
			prevSnapTick = snapshotMap(core.Snapshot())
			expiring = make(map[string]*livenessShadowRound)
			for node, rnd := range shadow.rounds {
				if shadow.now >= rnd.deadline {
					expiring[node] = rnd
				}
			}
		}
		acts = core.Tick(o.Now)
		if shadow != nil {
			for _, rnd := range shadow.rounds {
				rnd.doubted = rnd.doubted || core.Suspended()
			}
			newSnapTick := snapshotMap(core.Snapshot())
			for node, ns := range newSnapTick {
				if ns.Round != 0 && prevSnapTick[node].Round == 0 {
					if hooks.doubtedNotSetAtOpen {
						shadow.fakeDoubted[node] = false
					} else if hooks.doubtedNotAccumulated {
						shadow.fakeDoubted[node] = core.Suspended()
					}
				} else if prevSnapTick[node].Round != 0 {
					if hooks.doubtedNotSetAtOpen {
						shadow.fakeDoubted[node] = shadow.fakeDoubted[node] || core.Suspended()
					}
				}
			}
			if hooks.allSuspectedNotSelfSuspicion {
				snapNodes := snapshotMap(core.Snapshot())
				alive, suspected := 0, 0
				for node := range snapNodes {
					if shadow.known[node].State == detector.StateReady {
						alive++
						if core.VoteRequested(o.Now, "", node, 0)[0].Suspect {
							suspected++
						}
					}
				}
				if alive >= 2 && suspected == alive &&
					!(suspected >= cfg.SelfSuspectMinNodes && suspected*100 >= cfg.SelfSuspectPercent*alive) {
					shadow.overrideSuspendedFalse = true
				}
			}
			// Apply broken Tick behaviors that diverge from Core's clean decisions.
			if hooks.proposeCurrentModRev {
				for i := range acts {
					if acts[i].Kind == detector.ActProposeNotReady {
						acts[i].ModRev = shadow.realModRev[acts[i].Node]
					}
				}
			}
			for node, rnd := range expiring {
				delete(shadow.rounds, node)
				rec := shadow.known[node]
				nodeAsked := 0
				nodeFail := 0
				for _, p := range rnd.probers {
					if p.isNode {
						nodeAsked++
					}
				}
				for _, p := range rnd.failed {
					if p.isNode {
						nodeFail++
					}
				}
				quorum := countQuorum(cfg, node)
				base := rec.State == detector.StateReady && rec.ModRev == rnd.modRev && 1+rnd.suspects >= quorum
				if hooks.confirmAggFailWithNodeProbers && nodeAsked >= 1 && base &&
					len(rnd.failed) >= min(cfg.MinFailedProbes, len(rnd.probers)) &&
					nodeFail < min(cfg.MinFailedProbes, nodeAsked) {
					acts = append(acts, detector.Action{Kind: detector.ActProposeNotReady, Node: node, ModRev: rnd.modRev})
				}
				if hooks.aggOnlyConfirmsSuspended && nodeAsked == 0 && core.Suspended() && base &&
					len(rnd.probers) >= 1 && len(rnd.failed) >= min(cfg.MinFailedProbes, len(rnd.probers)) {
					acts = append(acts, detector.Action{Kind: detector.ActProposeNotReady, Node: node, ModRev: rnd.modRev})
				}
				if (hooks.doubtedNotSetAtOpen || hooks.doubtedNotAccumulated) && nodeAsked == 0 &&
					rnd.doubted && !shadow.fakeDoubted[node] && base &&
					len(rnd.probers) >= 1 && len(rnd.failed) >= min(cfg.MinFailedProbes, len(rnd.probers)) {
					acts = append(acts, detector.Action{Kind: detector.ActProposeNotReady, Node: node, ModRev: rnd.modRev})
				}
			}
			if hooks.noWitnessesWhenNotSuspended && !core.Suspended() {
				filtered := acts[:0]
				for _, a := range acts {
					if a.Kind == detector.ActProbeRequest && a.ToNode && shadow.known[a.To].Rack != shadow.known[a.Node].Rack {
						continue
					}
					filtered = append(filtered, a)
				}
				acts = filtered
			}
			if hooks.witnessesTrackedOnly {
				filtered := acts[:0]
				for _, a := range acts {
					if a.Kind == detector.ActProbeRequest && a.ToNode && shadow.known[a.To].Rack != shadow.known[a.Node].Rack {
						if _, tracked := prevSnapTick[a.To]; !tracked {
							continue
						}
					}
					filtered = append(filtered, a)
				}
				acts = filtered
			}
			if hooks.rackIncludesSuspected && cfg.RackProbers > 0 {
				snapNodes := snapshotMap(core.Snapshot())
				opened := make(map[string]uint64)
				for _, a := range acts {
					if a.Kind == detector.ActVoteRequest || a.Kind == detector.ActProbeRequest {
						opened[a.Node] = a.Round
					}
				}
				for node, rid := range opened {
					targetRack := shadow.known[node].Rack
					for m, rec := range shadow.known {
						if m == node || rec.Rack != targetRack || rec.State != detector.StateReady {
							continue
						}
						if _, tracked := snapNodes[m]; !tracked {
							continue
						}
						if core.VoteRequested(o.Now, "", m, 0)[0].Suspect {
							already := false
							for _, a := range acts {
								if a.Node == node && a.Kind == detector.ActProbeRequest && a.ToNode && a.To == m {
									already = true
									break
								}
							}
							if !already {
								acts = append(acts, detector.Action{Kind: detector.ActProbeRequest, To: m, ToNode: true, Node: node, Round: rid})
								break
							}
						}
					}
				}
			}
			if hooks.evictionWhileSuspended && core.Suspended() {
				snapNodes := snapshotMap(core.Snapshot())
				for node, rec := range shadow.known {
					if _, tracked := snapNodes[node]; !tracked {
						continue
					}
					if rec.State == detector.StateNotReady && !rec.Evictable &&
						shadow.now-shadow.notReadySince[node] >= cfg.EvictionGrace &&
						shadow.now >= shadow.nextEvictAt[node] {
						// Emitting ActProposeEvictable while suspended always diverges from the model.
						acts = append(acts, detector.Action{Kind: detector.ActProposeEvictable, Node: node, ModRev: rec.ModRev})
						shadow.nextEvictAt[node] = shadow.now + cfg.EvictionRetryInterval
					}
				}
			}
			if hooks.evictionGraceFromZero && !core.Suspended() {
				snapNodes := snapshotMap(core.Snapshot())
				for node, rec := range shadow.known {
					if _, tracked := snapNodes[node]; !tracked {
						continue
					}
					if rec.State == detector.StateNotReady && !rec.Evictable &&
						shadow.now >= cfg.EvictionGrace &&
						shadow.now-shadow.notReadySince[node] < cfg.EvictionGrace &&
						shadow.now >= shadow.nextEvictAt[node] {
						acts = append(acts, detector.Action{Kind: detector.ActProposeEvictable, Node: node, ModRev: rec.ModRev})
						shadow.nextEvictAt[node] = shadow.now + cfg.EvictionRetryInterval
					}
				}
			}
			if hooks.sharedProposeTimer {
				filtered := acts[:0]
				for _, a := range acts {
					if a.Kind == detector.ActProposeEvictable {
						if shadow.now < shadow.sharedProposeAt[a.Node] {
							continue
						}
						shadow.sharedProposeAt[a.Node] = shadow.now + cfg.EvictionRetryInterval
					}
					filtered = append(filtered, a)
				}
				acts = filtered
			}
			for _, a := range acts {
				switch a.Kind {
				case detector.ActVoteRequest:
					rnd := shadow.rounds[a.Node]
					if rnd == nil {
						rnd = &livenessShadowRound{
							id:       a.Round,
							deadline: shadow.now + cfg.RoundTimeout,
							modRev:   shadow.known[a.Node].ModRev,
							doubted:  core.Suspended(),
						}
						shadow.rounds[a.Node] = rnd
					}
					rnd.voters = append(rnd.voters, a.To)
				case detector.ActProbeRequest:
					rnd := shadow.rounds[a.Node]
					if rnd == nil {
						rnd = &livenessShadowRound{
							id:       a.Round,
							deadline: shadow.now + cfg.RoundTimeout,
							modRev:   shadow.known[a.Node].ModRev,
							doubted:  core.Suspended(),
						}
						shadow.rounds[a.Node] = rnd
					}
					rnd.probers = append(rnd.probers, livenessProberTarget{id: a.To, isNode: a.ToNode})
				case detector.ActProposeEvictable:
					shadow.nextEvictAt[a.Node] = shadow.now + cfg.EvictionRetryInterval
				}
			}
		}
	case "vote_requested":
		acts = core.VoteRequested(o.Now, o.From, o.Node, o.Round)
		if hooks.votesLieWhileSuspended && core.Suspended() {
			for i := range acts {
				acts[i].Suspect = false
			}
		}
		if hooks.aliveWithoutHeard && shadow != nil {
			snapNodes := snapshotMap(core.Snapshot())
			if _, tracked := snapNodes[o.Node]; tracked && shadow.known[o.Node].State == detector.StateReady {
				for i := range acts {
					acts[i].Alive = !acts[i].Suspect
				}
			}
		}
	case "vote_received":
		alive := o.Alive
		if hooks.ignoreAliveVeto {
			alive = false
		}
		skipCall := false
		if shadow != nil && hooks.aliveAfterVoteIgnored && alive {
			if rnd := shadow.rounds[o.Node]; rnd != nil && rnd.id == o.Round &&
				slices.Contains(rnd.voters, o.From) && slices.Contains(rnd.voted, o.From) {
				skipCall = true
			}
		}
		if !skipCall {
			core.VoteReceived(o.Now, o.From, o.Node, o.Round, o.Suspect, alive)
		}
		if shadow != nil {
			if rnd := shadow.rounds[o.Node]; rnd != nil && rnd.id == o.Round && slices.Contains(rnd.voters, o.From) {
				if alive {
					if !skipCall {
						delete(shadow.rounds, o.Node)
					}
				} else if !slices.Contains(rnd.voted, o.From) {
					rnd.voted = append(rnd.voted, o.From)
					if o.Suspect {
						rnd.suspects++
					}
				}
			}
		}
	case "probe_result":
		p := livenessProberTarget{id: o.From, isNode: o.FromNode}
		skipCall := false
		if shadow != nil && hooks.okAfterFailIgnored && o.OK {
			if rnd := shadow.rounds[o.Node]; rnd != nil && rnd.id == o.Round &&
				slices.Contains(rnd.probers, p) && slices.Contains(rnd.failed, p) {
				skipCall = true
			}
		}
		if !skipCall {
			core.ProbeResult(o.Now, o.From, o.FromNode, o.Node, o.Round, o.OK)
		}
		if shadow != nil {
			if rnd := shadow.rounds[o.Node]; rnd != nil && rnd.id == o.Round && slices.Contains(rnd.probers, p) {
				if o.OK {
					if !skipCall {
						delete(shadow.rounds, o.Node)
					}
				} else if !slices.Contains(rnd.failed, p) {
					rnd.failed = append(rnd.failed, p)
				}
			}
		}
	case "budget":
		window := o.Window
		if hooks.budgetKeepsWindowBoundary {
			window++
		}
		next, ok := budget.Grant(o.Granted, o.Now, window, o.Limit)
		if hooks.budgetOverReportsOKTrue && !ok {
			ok = true
		}
		if hooks.budgetAppendsNowAtEnd && ok {
			kept := make([]int64, 0, len(o.Granted)+1)
			cutoff := o.Now - window
			for _, e := range o.Granted {
				if e > cutoff {
					kept = append(kept, e)
				}
			}
			next = append(kept, o.Now)
		}
		if next != nil {
			step.Granted = next
		}
		step.OK = ok
	default:
		return nil, step, fmt.Errorf("unknown op %q", o.Op)
	}

	if hooks.reverseActions && len(acts) > 1 {
		acts = slices.Clone(acts)
		slices.Reverse(acts)
	}
	for _, a := range acts {
		kind, err := livenessActionKindName(a.Kind)
		if err != nil {
			return nil, step, err
		}
		step.Actions = append(step.Actions, livenessAction{
			Kind:    kind,
			To:      a.To,
			ToNode:  a.ToNode,
			Node:    a.Node,
			Round:   a.Round,
			Suspect: a.Suspect,
			Alive:   a.Alive,
			ModRev:  a.ModRev,
		})
	}

	snap := core.Snapshot()
	step.Suspended = snap.Suspended
	if shadow != nil && hooks.allSuspectedNotSelfSuspicion && shadow.overrideSuspendedFalse {
		step.Suspended = false
	}
	for _, n := range snap.Nodes {
		streak := n.Streak
		backoff := n.Backoff
		nextRoundAt := n.NextRoundAt
		notReadySince := n.NotReadySince
		doubted := n.Doubted
		if shadow != nil {
			if hooks.streakNotResetOnNotReady && shadow.stickyStreak[n.Node] > 0 {
				streak = shadow.stickyStreak[n.Node]
			}
			if hooks.backoffNotResetByHB && shadow.stickyBackoff[n.Node] > 0 && backoff == 0 {
				backoff = shadow.stickyBackoff[n.Node]
			}
			if hooks.hbNotResetNextRoundAt {
				if n.NextRoundAt > 0 || n.Round > 0 {
					delete(shadow.stickyNextRoundAt, n.Node)
				} else if v, ok := shadow.stickyNextRoundAt[n.Node]; ok {
					nextRoundAt = v
				}
			}
			if hooks.graceNotRestartedOnModRev {
				if v, ok := shadow.stickyNotReadySince[n.Node]; ok {
					notReadySince = v
				}
			}
			if hooks.doubtedNotSetAtOpen || hooks.doubtedNotAccumulated {
				if n.Round == 0 {
					delete(shadow.fakeDoubted, n.Node)
					doubted = false
				} else {
					doubted = shadow.fakeDoubted[n.Node]
				}
			}
		}
		step.Nodes = append(step.Nodes, livenessNodeSnapshot{
			Node:          n.Node,
			Streak:        streak,
			Round:         n.Round,
			Samples:       n.Samples,
			Last:          n.Last,
			Backoff:       backoff,
			Heard:         n.Heard,
			NextRoundAt:   nextRoundAt,
			NextReadyAt:   n.NextReadyAt,
			NextEvictAt:   n.NextEvictAt,
			NotReadySince: notReadySince,
			Doubted:       doubted,
		})
	}
	return acts, step, nil
}

func snapshotMap(snap detector.Snapshot) map[string]livenessNodeSnapshot {
	out := make(map[string]livenessNodeSnapshot, len(snap.Nodes))
	for _, n := range snap.Nodes {
		out[n.Node] = livenessNodeSnapshot{
			Node:          n.Node,
			Streak:        n.Streak,
			Round:         n.Round,
			Samples:       n.Samples,
			Last:          n.Last,
			Backoff:       n.Backoff,
			Heard:         n.Heard,
			NextRoundAt:   n.NextRoundAt,
			NextReadyAt:   n.NextReadyAt,
			NextEvictAt:   n.NextEvictAt,
			NotReadySince: n.NotReadySince,
			Doubted:       n.Doubted,
		}
	}
	return out
}

func countQuorum(cfg detector.Config, node string) int {
	raw := cfg.Ranked(node)
	seen := make(map[string]struct{}, len(raw))
	owners := 0
	for _, id := range raw {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		owners++
		if owners == cfg.Owners {
			break
		}
	}
	return owners/2 + 1
}

func runLivenessCase(tc livenessCase, hooks livenessHooks) (livenessResult, error) {
	cfg := toDetectorConfig(tc)
	if hooks.cfgMutate != nil {
		hooks.cfgMutate(&cfg)
	}
	core, err := detector.New(cfg)
	if err != nil {
		return livenessResult{}, err
	}
	var shadow *livenessShadowState
	if hooks.unansweredProbesAsFail || hooks.backoffNotResetByHB ||
		hooks.roundNotClearedOnModRev || hooks.proposeCurrentModRev ||
		hooks.confirmAggFailWithNodeProbers || hooks.noWitnessesWhenNotSuspended ||
		hooks.witnessesTrackedOnly ||
		hooks.rackIncludesSuspected || hooks.aggOnlyConfirmsSuspended ||
		hooks.evictionWhileSuspended || hooks.evictionGraceFromZero ||
		hooks.streakNotResetOnNotReady || hooks.aliveWithoutHeard ||
		hooks.okAfterFailIgnored || hooks.aliveAfterVoteIgnored ||
		hooks.hbNotResetNextRoundAt || hooks.sharedProposeTimer ||
		hooks.graceNotRestartedOnModRev || hooks.allSuspectedNotSelfSuspicion ||
		hooks.doubtedNotSetAtOpen || hooks.doubtedNotAccumulated {
		shadow = newLivenessShadowState()
	}
	res := livenessResult{
		ID:    tc.ID,
		Steps: make([]livenessStep, 0, len(tc.Ops)),
	}
	for i, o := range tc.Ops {
		_, step, err := applyLivenessOp(core, o, hooks, shadow, cfg)
		if err != nil {
			return res, fmt.Errorf("step[%d] %s: %w", i, o.Op, err)
		}
		res.Steps = append(res.Steps, step)
	}
	return res, nil
}

func decodeNonNegInt64(raw json.RawMessage) (int64, error) {
	var n int64
	if err := decodeStrictJSON(raw, &n); err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("negative integer %d", n)
	}
	return n, nil
}

func decodeUint64(raw json.RawMessage) (uint64, error) {
	var n uint64
	if err := decodeStrictJSON(raw, &n); err != nil {
		return 0, err
	}
	return n, nil
}

func decodeBool(raw json.RawMessage) (bool, error) {
	var b bool
	if err := decodeStrictJSON(raw, &b); err != nil {
		return false, err
	}
	return b, nil
}

func decodeLivenessResult(line []byte) (livenessResult, error) {
	trimmed := bytes.TrimSpace(line)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rawTop json.RawMessage
	if err := dec.Decode(&rawTop); err != nil {
		return livenessResult{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return livenessResult{}, fmt.Errorf("unexpected trailing data in result line: %v", err)
	}
	top, err := decodeObject(rawTop, "id", "steps")
	if err != nil {
		return livenessResult{}, err
	}
	id, err := decodeNonNegInt(top["id"])
	if err != nil {
		return livenessResult{}, fmt.Errorf("invalid id: %w", err)
	}
	var rawSteps []json.RawMessage
	if err := decodeStrictJSON(top["steps"], &rawSteps); err != nil {
		return livenessResult{}, fmt.Errorf("invalid steps: %w", err)
	}
	res := livenessResult{ID: id, Steps: make([]livenessStep, len(rawSteps))}
	for i, rs := range rawSteps {
		step, err := decodeLivenessStep(rs)
		if err != nil {
			return livenessResult{}, fmt.Errorf("step[%d]: %w", i, err)
		}
		res.Steps[i] = step
	}
	return res, nil
}

func decodeLivenessStep(raw json.RawMessage) (livenessStep, error) {
	obj, err := decodeObject(raw, "status", "actions", "suspended", "nodes", "granted", "ok")
	if err != nil {
		return livenessStep{}, err
	}
	step := livenessStep{
		Actions: []livenessAction{},
		Nodes:   []livenessNodeSnapshot{},
		Granted: []int64{},
	}
	if step.Status, err = decodeString(obj["status"]); err != nil {
		return livenessStep{}, fmt.Errorf("status: %w", err)
	}
	switch step.Status {
	case "", "accepted", "unknown", "notowner":
	default:
		return livenessStep{}, fmt.Errorf("unknown status %q", step.Status)
	}
	if step.Suspended, err = decodeBool(obj["suspended"]); err != nil {
		return livenessStep{}, fmt.Errorf("suspended: %w", err)
	}
	if step.OK, err = decodeBool(obj["ok"]); err != nil {
		return livenessStep{}, fmt.Errorf("ok: %w", err)
	}

	var rawActions, rawNodes, rawGranted []json.RawMessage
	if err := decodeStrictJSON(obj["actions"], &rawActions); err != nil {
		return livenessStep{}, fmt.Errorf("actions: %w", err)
	}
	for i, ra := range rawActions {
		actObj, err := decodeObject(ra, "kind", "to", "to_node", "node", "round", "suspect", "alive", "mod_rev")
		if err != nil {
			return livenessStep{}, fmt.Errorf("actions[%d]: %w", i, err)
		}
		var a livenessAction
		if a.Kind, err = decodeString(actObj["kind"]); err != nil {
			return livenessStep{}, fmt.Errorf("actions[%d].kind: %w", i, err)
		}
		switch a.Kind {
		case "vote_request", "vote", "probe_request", "propose_ready", "propose_notready", "propose_evictable":
		default:
			return livenessStep{}, fmt.Errorf("actions[%d]: unknown kind %q", i, a.Kind)
		}
		if a.To, err = decodeString(actObj["to"]); err != nil {
			return livenessStep{}, fmt.Errorf("actions[%d].to: %w", i, err)
		}
		if a.ToNode, err = decodeBool(actObj["to_node"]); err != nil {
			return livenessStep{}, fmt.Errorf("actions[%d].to_node: %w", i, err)
		}
		if a.Node, err = decodeString(actObj["node"]); err != nil {
			return livenessStep{}, fmt.Errorf("actions[%d].node: %w", i, err)
		}
		if a.Round, err = decodeUint64(actObj["round"]); err != nil {
			return livenessStep{}, fmt.Errorf("actions[%d].round: %w", i, err)
		}
		if a.Suspect, err = decodeBool(actObj["suspect"]); err != nil {
			return livenessStep{}, fmt.Errorf("actions[%d].suspect: %w", i, err)
		}
		if a.Alive, err = decodeBool(actObj["alive"]); err != nil {
			return livenessStep{}, fmt.Errorf("actions[%d].alive: %w", i, err)
		}
		if a.ModRev, err = decodeNonNegInt64(actObj["mod_rev"]); err != nil {
			return livenessStep{}, fmt.Errorf("actions[%d].mod_rev: %w", i, err)
		}
		step.Actions = append(step.Actions, a)
	}

	if err := decodeStrictJSON(obj["nodes"], &rawNodes); err != nil {
		return livenessStep{}, fmt.Errorf("nodes: %w", err)
	}
	for i, rn := range rawNodes {
		nodeObj, err := decodeObject(rn, "node", "streak", "round", "samples", "last", "backoff", "heard",
			"next_round_at", "next_ready_at", "next_evict_at", "not_ready_since", "doubted")
		if err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d]: %w", i, err)
		}
		var n livenessNodeSnapshot
		if n.Node, err = decodeString(nodeObj["node"]); err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d].node: %w", i, err)
		}
		if n.Streak, err = decodeNonNegInt(nodeObj["streak"]); err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d].streak: %w", i, err)
		}
		if n.Round, err = decodeUint64(nodeObj["round"]); err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d].round: %w", i, err)
		}
		if n.Samples, err = decodeNonNegInt(nodeObj["samples"]); err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d].samples: %w", i, err)
		}
		if n.Last, err = decodeNonNegInt64(nodeObj["last"]); err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d].last: %w", i, err)
		}
		if n.Backoff, err = decodeNonNegInt(nodeObj["backoff"]); err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d].backoff: %w", i, err)
		}
		if n.Heard, err = decodeBool(nodeObj["heard"]); err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d].heard: %w", i, err)
		}
		if n.NextRoundAt, err = decodeNonNegInt64(nodeObj["next_round_at"]); err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d].next_round_at: %w", i, err)
		}
		if n.NextReadyAt, err = decodeNonNegInt64(nodeObj["next_ready_at"]); err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d].next_ready_at: %w", i, err)
		}
		if n.NextEvictAt, err = decodeNonNegInt64(nodeObj["next_evict_at"]); err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d].next_evict_at: %w", i, err)
		}
		if n.NotReadySince, err = decodeNonNegInt64(nodeObj["not_ready_since"]); err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d].not_ready_since: %w", i, err)
		}
		if n.Doubted, err = decodeBool(nodeObj["doubted"]); err != nil {
			return livenessStep{}, fmt.Errorf("nodes[%d].doubted: %w", i, err)
		}
		step.Nodes = append(step.Nodes, n)
	}

	if err := decodeStrictJSON(obj["granted"], &rawGranted); err != nil {
		return livenessStep{}, fmt.Errorf("granted: %w", err)
	}
	for i, rg := range rawGranted {
		v, err := decodeNonNegInt64(rg)
		if err != nil {
			return livenessStep{}, fmt.Errorf("granted[%d]: %w", i, err)
		}
		step.Granted = append(step.Granted, v)
	}
	return step, nil
}

func compareLivenessResult(want, got livenessResult) string {
	if want.ID != got.ID {
		return fmt.Sprintf("id mismatch: lean=%d go=%d", want.ID, got.ID)
	}
	if len(want.Steps) != len(got.Steps) {
		return fmt.Sprintf("steps len mismatch: lean=%d go=%d", len(want.Steps), len(got.Steps))
	}
	for i := range want.Steps {
		w, g := want.Steps[i], got.Steps[i]
		at := fmt.Sprintf("step[%d]", i)
		switch {
		case w.Status != g.Status:
			return fmt.Sprintf("%s.status mismatch: lean=%q go=%q", at, w.Status, g.Status)
		case len(w.Actions) != len(g.Actions):
			return fmt.Sprintf("%s.actions len mismatch: lean=%d go=%d", at, len(w.Actions), len(g.Actions))
		case w.Suspended != g.Suspended:
			return fmt.Sprintf("%s.suspended mismatch: lean=%v go=%v", at, w.Suspended, g.Suspended)
		case len(w.Nodes) != len(g.Nodes):
			return fmt.Sprintf("%s.nodes len mismatch: lean=%d go=%d", at, len(w.Nodes), len(g.Nodes))
		case len(w.Granted) != len(g.Granted):
			return fmt.Sprintf("%s.granted len mismatch: lean=%d go=%d", at, len(w.Granted), len(g.Granted))
		case w.OK != g.OK:
			return fmt.Sprintf("%s.ok mismatch: lean=%v go=%v", at, w.OK, g.OK)
		}
		for j := range w.Actions {
			wa, ga := w.Actions[j], g.Actions[j]
			switch {
			case wa.Kind != ga.Kind:
				return fmt.Sprintf("%s.actions[%d].kind mismatch: lean=%q go=%q", at, j, wa.Kind, ga.Kind)
			case wa.To != ga.To:
				return fmt.Sprintf("%s.actions[%d].to mismatch: lean=%q go=%q", at, j, wa.To, ga.To)
			case wa.ToNode != ga.ToNode:
				return fmt.Sprintf("%s.actions[%d].to_node mismatch: lean=%v go=%v", at, j, wa.ToNode, ga.ToNode)
			case wa.Node != ga.Node:
				return fmt.Sprintf("%s.actions[%d].node mismatch: lean=%q go=%q", at, j, wa.Node, ga.Node)
			case wa.Round != ga.Round:
				return fmt.Sprintf("%s.actions[%d].round mismatch: lean=%d go=%d", at, j, wa.Round, ga.Round)
			case wa.Suspect != ga.Suspect:
				return fmt.Sprintf("%s.actions[%d].suspect mismatch: lean=%v go=%v", at, j, wa.Suspect, ga.Suspect)
			case wa.Alive != ga.Alive:
				return fmt.Sprintf("%s.actions[%d].alive mismatch: lean=%v go=%v", at, j, wa.Alive, ga.Alive)
			case wa.ModRev != ga.ModRev:
				return fmt.Sprintf("%s.actions[%d].mod_rev mismatch: lean=%d go=%d", at, j, wa.ModRev, ga.ModRev)
			}
		}
		for j := range w.Nodes {
			wn, gn := w.Nodes[j], g.Nodes[j]
			switch {
			case wn.Node != gn.Node:
				return fmt.Sprintf("%s.nodes[%d].node mismatch: lean=%q go=%q", at, j, wn.Node, gn.Node)
			case wn.Streak != gn.Streak:
				return fmt.Sprintf("%s.nodes[%d].streak mismatch: lean=%d go=%d", at, j, wn.Streak, gn.Streak)
			case wn.Round != gn.Round:
				return fmt.Sprintf("%s.nodes[%d].round mismatch: lean=%d go=%d", at, j, wn.Round, gn.Round)
			case wn.Samples != gn.Samples:
				return fmt.Sprintf("%s.nodes[%d].samples mismatch: lean=%d go=%d", at, j, wn.Samples, gn.Samples)
			case wn.Last != gn.Last:
				return fmt.Sprintf("%s.nodes[%d].last mismatch: lean=%d go=%d", at, j, wn.Last, gn.Last)
			case wn.Backoff != gn.Backoff:
				return fmt.Sprintf("%s.nodes[%d].backoff mismatch: lean=%d go=%d", at, j, wn.Backoff, gn.Backoff)
			case wn.Heard != gn.Heard:
				return fmt.Sprintf("%s.nodes[%d].heard mismatch: lean=%v go=%v", at, j, wn.Heard, gn.Heard)
			case wn.NextRoundAt != gn.NextRoundAt:
				return fmt.Sprintf("%s.nodes[%d].next_round_at mismatch: lean=%d go=%d", at, j, wn.NextRoundAt, gn.NextRoundAt)
			case wn.NextReadyAt != gn.NextReadyAt:
				return fmt.Sprintf("%s.nodes[%d].next_ready_at mismatch: lean=%d go=%d", at, j, wn.NextReadyAt, gn.NextReadyAt)
			case wn.NextEvictAt != gn.NextEvictAt:
				return fmt.Sprintf("%s.nodes[%d].next_evict_at mismatch: lean=%d go=%d", at, j, wn.NextEvictAt, gn.NextEvictAt)
			case wn.NotReadySince != gn.NotReadySince:
				return fmt.Sprintf("%s.nodes[%d].not_ready_since mismatch: lean=%d go=%d", at, j, wn.NotReadySince, gn.NotReadySince)
			case wn.Doubted != gn.Doubted:
				return fmt.Sprintf("%s.nodes[%d].doubted mismatch: lean=%v go=%v", at, j, wn.Doubted, gn.Doubted)
			}
		}
		for j := range w.Granted {
			if w.Granted[j] != g.Granted[j] {
				return fmt.Sprintf("%s.granted[%d] mismatch: lean=%d go=%d", at, j, w.Granted[j], g.Granted[j])
			}
		}
	}
	return ""
}

func cloneLivenessResult(r livenessResult) livenessResult {
	out := livenessResult{ID: r.ID, Steps: make([]livenessStep, len(r.Steps))}
	for i, s := range r.Steps {
		s.Actions = append([]livenessAction{}, s.Actions...)
		s.Nodes = append([]livenessNodeSnapshot{}, s.Nodes...)
		s.Granted = append([]int64{}, s.Granted...)
		out.Steps[i] = s
	}
	return out
}

func leanLivenessResults(t *testing.T, cases []livenessCase) ([][]byte, [][]byte, []livenessResult) {
	t.Helper()
	encoded := encodeLivenessCases(t, cases)
	lines, err := RunModel("liveness", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(lines), len(cases))
	}
	results := make([]livenessResult, len(lines))
	for i, line := range lines {
		res, err := decodeLivenessResult(line)
		if err != nil {
			t.Fatalf("id=%d decode error: %v\ncase: %s\nlean: %s", cases[i].ID, err, encoded[i], line)
		}
		results[i] = res
	}
	return encoded, lines, results
}

func TestLivenessMatchesLeanModel(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateLivenessCases(cfg.Seed, cfg.Cases)
	encoded, leanLines, leanResults := leanLivenessResults(t, cases)

	repro := fmt.Sprintf("NAYUTAR_DRT_SEED=%d NAYUTAR_DRT_CASES=%d go test -tags drt -run '^TestLivenessMatchesLeanModel$' ./core/drt/...", cfg.Seed, cfg.Cases)
	totalSteps := 0
	for i, tc := range cases {
		goRes, err := runLivenessCase(tc, livenessHooks{})
		if err != nil {
			t.Fatalf("seed=%d id=%d detector error: %v\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, err, encoded[i], leanLines[i], repro)
		}
		if diff := compareLivenessResult(leanResults[i], goRes); diff != "" {
			goJSON, marshalErr := json.Marshal(goRes)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			t.Fatalf("seed=%d id=%d mismatch (%s)\ncase: %s\nlean: %s\ngo:   %s\nreproduce: %s",
				cfg.Seed, tc.ID, diff, encoded[i], leanLines[i], goJSON, repro)
		}
		totalSteps += len(tc.Ops)
	}
	if totalSteps == 0 {
		t.Fatal("zero ops exercised across all cases")
	}
	t.Logf("compared %d cases and %d steps (seed=%d)", len(cases), totalSteps, cfg.Seed)
}

func TestLivenessGeneratorCoverage(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateLivenessCases(cfg.Seed, cfg.Cases)
	seen := map[string]int{}

	for _, tc := range cases {
		if tc.Config.RoundBase > 0 {
			seen["config/round_base>0"]++
		}
		res, err := runLivenessCase(tc, livenessHooks{})
		if err != nil {
			t.Fatalf("id=%d: %v", tc.ID, err)
		}
		monoNow := int64(0)
		prevNodes := map[string]livenessNodeSnapshot{}
		knownState := map[string]string{}
		knownObs := map[string]livenessOp{}
		roundNodeAsked := map[uint64]bool{}
		roundUntrackedNodeAsked := map[uint64]bool{}
		roundUntrackedWitnessAsked := map[uint64]bool{}
		roundAggProbers := map[uint64][]string{}
		roundAggFailed := map[uint64]map[string]bool{}
		roundAllProbers := map[uint64][]livenessProberTarget{}
		roundVoters := map[uint64][]string{}
		roundVoted := map[uint64]map[string]bool{}
		roundSuspectVotes := map[uint64]int{}
		roundFailed := map[uint64]map[livenessProberTarget]bool{}
		roundSuspectVoted := map[uint64]map[string]bool{}
		roundModRev := map[string]int64{}
		okProbeStreak := map[string]int{}
		detCfg := toDetectorConfig(tc)

		for j, o := range tc.Ops {
			if o.Now > monoNow && o.Op != "budget" {
				monoNow = o.Now
			}
			s := res.Steps[j]
			currNodes := make(map[string]livenessNodeSnapshot, len(s.Nodes))
			for _, n := range s.Nodes {
				currNodes[n.Node] = n
				if n.Backoff > 1 {
					seen["node/backoff>1"]++
				}
				if n.Backoff == 16 {
					seen["node/backoff==16"]++
				}
			}

			proposedNotReady := map[string]bool{}
			for _, a := range s.Actions {
				seen["action/"+a.Kind]++
				switch a.Kind {
				case "vote_request":
					roundVoters[a.Round] = append(roundVoters[a.Round], a.To)
				case "probe_request":
					roundAllProbers[a.Round] = append(roundAllProbers[a.Round], livenessProberTarget{id: a.To, isNode: a.ToNode})
					if a.ToNode {
						roundNodeAsked[a.Round] = true
						if _, tracked := currNodes[a.To]; !tracked {
							roundUntrackedNodeAsked[a.Round] = true
							seen["round/untracked-node-prober"]++
							if knownObs[a.To].Rack != knownObs[a.Node].Rack {
								roundUntrackedWitnessAsked[a.Round] = true
								seen["round/untracked-witness-prober"]++
							}
						}
					} else {
						roundAggProbers[a.Round] = append(roundAggProbers[a.Round], a.To)
					}
				case "propose_ready":
					if knownState[a.Node] == "notready" {
						seen["propose_ready/after-notready"]++
					}
					if prev, hadPrev := prevNodes[a.Node]; hadPrev && prev.NextEvictAt > monoNow {
						seen["propose_ready/while-next_evict_at-future"]++
					}
				case "propose_notready":
					proposedNotReady[a.Node] = true
					okProbeStreak[a.Node] = 0
					prev, hadPrev := prevNodes[a.Node]
					if hadPrev && prev.Round != 0 {
						if roundNodeAsked[prev.Round] {
							seen["propose_notready/with-node-probers"]++
						} else {
							seen["propose_notready/agg-only"]++
						}
					}
				}
			}
			switch o.Op {
			case "observed":
				if prevObs, hadObs := knownObs[o.Node]; hadObs {
					if prevObs.State != o.State && prevObs.ModRev == o.ModRev {
						seen["observed/state-change-same-modrev"]++
					}
					if prevObs.State == o.State && prevObs.Since == o.Since && prevObs.Rack == o.Rack &&
						prevObs.Evictable == o.Evictable && prevObs.ModRev == o.ModRev {
						seen["observed/repeated-identical"]++
					}
					if prevObs.State == "notready" && o.State == "notready" && o.ModRev != prevObs.ModRev {
						seen["observed/notready-new-modrev"]++
					}
					if prevObs.State != o.State {
						okProbeStreak[o.Node] = 0
					}
				}
				knownObs[o.Node] = o
				prev, hadPrev := prevNodes[o.Node]
				curr, hasCurr := currNodes[o.Node]
				if o.State == "ready" && hadPrev && hasCurr && prev.Round != 0 && curr.Round == 0 && o.ModRev != roundModRev[o.Node] {
					seen["round/cleared-by-modrev"]++
				}
				knownState[o.Node] = o.State
				for _, n := range s.Nodes {
					if n.Node == o.Node && tc.Config.Phi.MaxSamples == 1 && n.Samples == 1 {
						seen["window/dropped-oldest"]++
					}
				}
			case "forgotten":
				delete(knownState, o.Node)
				delete(knownObs, o.Node)
				delete(okProbeStreak, o.Node)
			case "heartbeat":
				seen["heartbeat/"+s.Status]++
				if s.Status == "accepted" {
					okProbeStreak[o.Node] = 0
					prev, hadPrev := prevNodes[o.Node]
					curr, hasCurr := currNodes[o.Node]
					if hasCurr {
						if hadPrev && prev.Backoff > 0 && curr.Backoff == 0 {
							seen["heartbeat/resets-backoff"]++
						}
						if hadPrev && prev.NextRoundAt > 0 && curr.NextRoundAt == 0 {
							seen["heartbeat/resets-next_round_at"]++
						}
						if tc.Config.Phi.MaxSamples == 1 && curr.Samples == 1 {
							seen["window/dropped-oldest"]++
						} else if hadPrev &&
							prev.Samples == tc.Config.Phi.MaxSamples &&
							tc.Config.ReadyHeartbeats >= 2 &&
							curr.Streak > 1 &&
							monoNow-prev.Last <= tc.Config.Phi.MaxInterval {
							seen["window/dropped-oldest"]++
						}
					}
				}
			case "tick":
				if s.Suspended {
					seen["tick/suspended"]++
				}
				readyTracked := 0
				for _, n := range s.Nodes {
					if knownState[n.Node] == "ready" {
						readyTracked++
					}
					prev := prevNodes[n.Node]
					if prev.Round != 0 && n.Round == 0 {
						okProbeStreak[n.Node] = 0
						if roundUntrackedNodeAsked[prev.Round] {
							seen["round/untracked-node-prober-deadline"]++
						}
						if roundUntrackedWitnessAsked[prev.Round] {
							seen["round/untracked-witness-prober-deadline"]++
						}
					}
					if prev.Round != 0 && n.Round == 0 && !proposedNotReady[n.Node] {
						if roundNodeAsked[prev.Round] && len(roundAggFailed[prev.Round]) >= tc.Config.MinFailedProbes {
							seen["round/node-probers-agg-fails-no-confirm"]++
						}
						if prev.Doubted && !roundNodeAsked[prev.Round] && len(roundAggProbers[prev.Round]) >= 1 &&
							len(roundAggFailed[prev.Round]) >= min(tc.Config.MinFailedProbes, len(roundAggProbers[prev.Round])) &&
							1+roundSuspectVotes[prev.Round] >= countQuorum(detCfg, n.Node) {
							seen["round/doubted-agg-fails-no-confirm"]++
							if !s.Suspended {
								seen["round/doubted-heal-before-deadline"]++
							}
						}
					}
					if n.Round != 0 && prev.Round == 0 {
						// Active round opened at this Tick: fetch latest ModRev from tc.Ops[:j].
						for k := j - 1; k >= 0; k-- {
							if tc.Ops[k].Op == "observed" && tc.Ops[k].Node == n.Node {
								roundModRev[n.Node] = tc.Ops[k].ModRev
								break
							}
						}
					}
				}
				if readyTracked == 1 {
					seen["tick/one-tracked-ready-node"]++
				}
				if readyTracked == 2 && s.Suspended && 2 < tc.Config.SelfSuspectMinNodes {
					seen["tick/two-ready-all-suspected"]++
				}
			case "vote_received":
				if !o.Alive && slices.Contains(roundVoters[o.Round], o.From) {
					if roundVoted[o.Round] == nil {
						roundVoted[o.Round] = map[string]bool{}
					}
					if !roundVoted[o.Round][o.From] {
						roundVoted[o.Round][o.From] = true
						if o.Suspect {
							roundSuspectVotes[o.Round]++
						}
					}
					if o.Suspect {
						if roundSuspectVoted[o.Round] == nil {
							roundSuspectVoted[o.Round] = map[string]bool{}
						}
						roundSuspectVoted[o.Round][o.From] = true
					}
				}
				if o.Alive {
					prev, hadPrev := prevNodes[o.Node]
					curr, hasCurr := currNodes[o.Node]
					if hadPrev && hasCurr && prev.Round != 0 && curr.Round == 0 {
						seen["round/ended-by-alive-vote"]++
						if roundSuspectVoted[prev.Round][o.From] {
							seen["vote_received/suspect-then-alive-same-sender"]++
						}
					}
				}
			case "probe_result":
				p := livenessProberTarget{id: o.From, isNode: o.FromNode}
				if !o.OK && slices.Contains(roundAllProbers[o.Round], p) {
					if roundFailed[o.Round] == nil {
						roundFailed[o.Round] = map[livenessProberTarget]bool{}
					}
					roundFailed[o.Round][p] = true
				}
				if !o.OK && !o.FromNode && slices.Contains(roundAggProbers[o.Round], o.From) {
					if roundAggFailed[o.Round] == nil {
						roundAggFailed[o.Round] = map[string]bool{}
					}
					roundAggFailed[o.Round][o.From] = true
				}
				if o.OK {
					prev, hadPrev := prevNodes[o.Node]
					curr, hasCurr := currNodes[o.Node]
					if hadPrev && hasCurr && prev.Round != 0 && curr.Round == 0 {
						seen["round/ended-by-ok-probe"]++
						if roundFailed[prev.Round][p] {
							seen["probe_result/fail-then-ok-same-sender"]++
						}
						okProbeStreak[o.Node]++
						if okProbeStreak[o.Node] >= 18 {
							seen["round/18+-ok-probes-in-a-row"]++
						}
					}
				}
			case "budget":
				if s.OK {
					seen["budget/ok=true"]++
				} else {
					seen["budget/ok=false"]++
				}
			}
			prevNodes = currNodes
		}
	}

	required := []string{
		"config/round_base>0",
		"action/vote_request",
		"action/vote",
		"action/probe_request",
		"action/propose_ready",
		"action/propose_notready",
		"action/propose_evictable",
		"heartbeat/accepted",
		"heartbeat/unknown",
		"heartbeat/notowner",
		"heartbeat/resets-next_round_at",
		"tick/suspended",
		"tick/one-tracked-ready-node",
		"tick/two-ready-all-suspected",
		"propose_notready/with-node-probers",
		"propose_notready/agg-only",
		"propose_ready/while-next_evict_at-future",
		"round/node-probers-agg-fails-no-confirm",
		"round/doubted-agg-fails-no-confirm",
		"round/doubted-heal-before-deadline",
		"round/untracked-node-prober",
		"round/untracked-node-prober-deadline",
		"round/untracked-witness-prober",
		"round/untracked-witness-prober-deadline",
		"round/ended-by-alive-vote",
		"round/ended-by-ok-probe",
		"round/18+-ok-probes-in-a-row",
		"probe_result/fail-then-ok-same-sender",
		"vote_received/suspect-then-alive-same-sender",
		"observed/state-change-same-modrev",
		"observed/repeated-identical",
		"observed/notready-new-modrev",
		"node/backoff>1",
		"node/backoff==16",
		"round/cleared-by-modrev",
		"propose_ready/after-notready",
		"heartbeat/resets-backoff",
		"budget/ok=true",
		"budget/ok=false",
		"window/dropped-oldest",
	}
	for _, item := range required {
		if seen[item] == 0 {
			t.Errorf("no case reached coverage item %q (seed=%d, cases=%d)", item, cfg.Seed, cfg.Cases)
		}
	}
}

func TestLivenessComparisonDetectsEveryFieldMutation(t *testing.T) {
	cases := generateLivenessCases(1, 200)
	_, leanLines, results := leanLivenessResults(t, cases)

	applied := map[string]int{}
	assertDetects := func(kind string, orig livenessResult, mutate func(r *livenessResult)) {
		t.Helper()
		mut := cloneLivenessResult(orig)
		mutate(&mut)
		applied[kind]++
		if compareLivenessResult(orig, mut) == "" {
			t.Fatalf("mutation %q on case %d was not detected", kind, orig.ID)
		}
	}

	for _, orig := range results {
		assertDetects("id+1", orig, func(r *livenessResult) { r.ID++ })
		if n := len(orig.Steps); n > 0 {
			assertDetects("drop-last-step", orig, func(r *livenessResult) { r.Steps = r.Steps[:n-1] })
			assertDetects("append-duplicate-last-step", orig, func(r *livenessResult) { r.Steps = append(r.Steps, r.Steps[n-1]) })
		}
		for j, s := range orig.Steps {
			if s.Status == "" {
				assertDetects("status-set", orig, func(r *livenessResult) { r.Steps[j].Status = "accepted" })
			} else {
				assertDetects("status-cleared", orig, func(r *livenessResult) { r.Steps[j].Status = "" })
				assertDetects("status-changed", orig, func(r *livenessResult) { r.Steps[j].Status += "x" })
			}
			assertDetects("flip-suspended", orig, func(r *livenessResult) { r.Steps[j].Suspended = !r.Steps[j].Suspended })
			assertDetects("flip-ok", orig, func(r *livenessResult) { r.Steps[j].OK = !r.Steps[j].OK })
			assertDetects("actions-append", orig, func(r *livenessResult) {
				r.Steps[j].Actions = append(r.Steps[j].Actions, livenessAction{Kind: "vote", Node: "z"})
			})
			assertDetects("nodes-append", orig, func(r *livenessResult) {
				r.Steps[j].Nodes = append(r.Steps[j].Nodes, livenessNodeSnapshot{Node: "z"})
			})
			assertDetects("granted-append", orig, func(r *livenessResult) {
				r.Steps[j].Granted = append(r.Steps[j].Granted, 1)
			})

			if n := len(s.Actions); n > 0 {
				assertDetects("actions-drop-last", orig, func(r *livenessResult) {
					r.Steps[j].Actions = r.Steps[j].Actions[:n-1]
				})
				assertDetects("action-kind", orig, func(r *livenessResult) {
					if r.Steps[j].Actions[0].Kind == "vote" {
						r.Steps[j].Actions[0].Kind = "vote_request"
					} else {
						r.Steps[j].Actions[0].Kind = "vote"
					}
				})
				assertDetects("action-to", orig, func(r *livenessResult) { r.Steps[j].Actions[n-1].To += "x" })
				assertDetects("action-to_node", orig, func(r *livenessResult) {
					r.Steps[j].Actions[n-1].ToNode = !r.Steps[j].Actions[n-1].ToNode
				})
				assertDetects("action-node", orig, func(r *livenessResult) { r.Steps[j].Actions[n-1].Node += "x" })
				assertDetects("action-round+1", orig, func(r *livenessResult) { r.Steps[j].Actions[n-1].Round++ })
				assertDetects("action-suspect", orig, func(r *livenessResult) {
					r.Steps[j].Actions[n-1].Suspect = !r.Steps[j].Actions[n-1].Suspect
				})
				assertDetects("action-alive", orig, func(r *livenessResult) {
					r.Steps[j].Actions[n-1].Alive = !r.Steps[j].Actions[n-1].Alive
				})
				assertDetects("action-mod_rev+1", orig, func(r *livenessResult) { r.Steps[j].Actions[n-1].ModRev++ })
			}
			if n := len(s.Actions); n > 1 && s.Actions[0] != s.Actions[n-1] {
				assertDetects("actions-swap", orig, func(r *livenessResult) {
					acts := r.Steps[j].Actions
					acts[0], acts[n-1] = acts[n-1], acts[0]
				})
			}

			if n := len(s.Nodes); n > 0 {
				assertDetects("nodes-drop-last", orig, func(r *livenessResult) {
					r.Steps[j].Nodes = r.Steps[j].Nodes[:n-1]
				})
				assertDetects("node-node", orig, func(r *livenessResult) { r.Steps[j].Nodes[n-1].Node += "x" })
				assertDetects("node-streak+1", orig, func(r *livenessResult) { r.Steps[j].Nodes[n-1].Streak++ })
				assertDetects("node-round+1", orig, func(r *livenessResult) { r.Steps[j].Nodes[n-1].Round++ })
				assertDetects("node-samples+1", orig, func(r *livenessResult) { r.Steps[j].Nodes[n-1].Samples++ })
				assertDetects("node-last+1", orig, func(r *livenessResult) { r.Steps[j].Nodes[n-1].Last++ })
				assertDetects("node-backoff+1", orig, func(r *livenessResult) { r.Steps[j].Nodes[n-1].Backoff++ })
				assertDetects("node-heard", orig, func(r *livenessResult) { r.Steps[j].Nodes[n-1].Heard = !r.Steps[j].Nodes[n-1].Heard })
				assertDetects("node-next_round_at+1", orig, func(r *livenessResult) { r.Steps[j].Nodes[n-1].NextRoundAt++ })
				assertDetects("node-next_ready_at+1", orig, func(r *livenessResult) { r.Steps[j].Nodes[n-1].NextReadyAt++ })
				assertDetects("node-next_evict_at+1", orig, func(r *livenessResult) { r.Steps[j].Nodes[n-1].NextEvictAt++ })
				assertDetects("node-not_ready_since+1", orig, func(r *livenessResult) { r.Steps[j].Nodes[n-1].NotReadySince++ })
				assertDetects("node-doubted", orig, func(r *livenessResult) { r.Steps[j].Nodes[n-1].Doubted = !r.Steps[j].Nodes[n-1].Doubted })
			}
			if n := len(s.Nodes); n > 1 {
				assertDetects("nodes-swap", orig, func(r *livenessResult) {
					ns := r.Steps[j].Nodes
					ns[0], ns[n-1] = ns[n-1], ns[0]
				})
			}

			if n := len(s.Granted); n > 0 {
				assertDetects("granted-drop-last", orig, func(r *livenessResult) {
					r.Steps[j].Granted = r.Steps[j].Granted[:n-1]
				})
				assertDetects("granted-elem+1", orig, func(r *livenessResult) { r.Steps[j].Granted[n-1]++ })
			}
			if n := len(s.Granted); n > 1 && s.Granted[0] != s.Granted[n-1] {
				assertDetects("granted-swap", orig, func(r *livenessResult) {
					gr := r.Steps[j].Granted
					gr[0], gr[n-1] = gr[n-1], gr[0]
				})
			}
		}
	}

	for _, kind := range []string{
		"id+1", "drop-last-step", "append-duplicate-last-step",
		"status-set", "status-cleared", "status-changed",
		"flip-suspended", "flip-ok",
		"actions-append", "actions-drop-last", "actions-swap",
		"action-kind", "action-to", "action-to_node", "action-node", "action-round+1", "action-suspect", "action-alive", "action-mod_rev+1",
		"nodes-append", "nodes-drop-last", "nodes-swap",
		"node-node", "node-streak+1", "node-round+1", "node-samples+1", "node-last+1", "node-backoff+1", "node-heard",
		"node-next_round_at+1", "node-next_ready_at+1", "node-next_evict_at+1", "node-not_ready_since+1", "node-doubted",
		"granted-append", "granted-drop-last", "granted-elem+1", "granted-swap",
	} {
		if applied[kind] == 0 {
			t.Errorf("mutation %q was never applicable across %d cases", kind, len(cases))
		}
	}

	const emptyStep = `{"status":"","actions":[],"suspended":false,"nodes":[],"granted":[],"ok":false}`
	var raw map[string]any
	if err := json.Unmarshal(leanLines[0], &raw); err != nil {
		t.Fatal(err)
	}
	raw["extra"] = 1
	withTopExtra, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	invalidLines := []struct {
		name string
		line string
	}{
		{"unknown top-level field", string(withTopExtra)},
		{"missing id", `{"steps":[]}`},
		{"missing steps", `{"id":0}`},
		{"null steps", `{"id":0,"steps":null}`},
		{"null id", `{"id":null,"steps":[]}`},
		{"array instead of object", `[]`},
		{"trailing object", `{"id":0,"steps":[]} {"id":1,"steps":[]}`},
		{"empty step object", `{"id":0,"steps":[{}]}`},
		{"unknown step field", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[],"granted":[],"ok":false,"extra":1}]}`},
		{"missing ok", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[],"granted":[]}]}`},
		{"uppercase step keys", `{"id":0,"steps":[{"Status":"","Actions":[],"Suspended":false,"Nodes":[],"Granted":[],"OK":false}]}`},
		{"unknown status", `{"id":0,"steps":[{"status":"bad","actions":[],"suspended":false,"nodes":[],"granted":[],"ok":false}]}`},
		{"null status", `{"id":0,"steps":[{"status":null,"actions":[],"suspended":false,"nodes":[],"granted":[],"ok":false}]}`},
		{"null actions", `{"id":0,"steps":[{"status":"","actions":null,"suspended":false,"nodes":[],"granted":[],"ok":false}]}`},
		{"null suspended", `{"id":0,"steps":[{"status":"","actions":[],"suspended":null,"nodes":[],"granted":[],"ok":false}]}`},
		{"null nodes", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":null,"granted":[],"ok":false}]}`},
		{"null granted", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[],"granted":null,"ok":false}]}`},
		{"null ok", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[],"granted":[],"ok":null}]}`},
		{"action missing alive", `{"id":0,"steps":[{"status":"","actions":[{"kind":"vote","to":"a","to_node":false,"node":"n1","round":1,"suspect":false,"mod_rev":0}],"suspended":false,"nodes":[],"granted":[],"ok":false}]}`},
		{"action missing mod_rev", `{"id":0,"steps":[{"status":"","actions":[{"kind":"vote","to":"a","to_node":false,"node":"n1","round":1,"suspect":false,"alive":false}],"suspended":false,"nodes":[],"granted":[],"ok":false}]}`},
		{"action extra field", `{"id":0,"steps":[{"status":"","actions":[{"kind":"vote","to":"a","to_node":false,"node":"n1","round":1,"suspect":false,"alive":false,"mod_rev":0,"extra":1}],"suspended":false,"nodes":[],"granted":[],"ok":false}]}`},
		{"action unknown kind", `{"id":0,"steps":[{"status":"","actions":[{"kind":"explode","to":"a","to_node":false,"node":"n1","round":1,"suspect":false,"alive":false,"mod_rev":0}],"suspended":false,"nodes":[],"granted":[],"ok":false}]}`},
		{"action negative round", `{"id":0,"steps":[{"status":"","actions":[{"kind":"vote","to":"a","to_node":false,"node":"n1","round":-1,"suspect":false,"alive":false,"mod_rev":0}],"suspended":false,"nodes":[],"granted":[],"ok":false}]}`},
		{"action negative mod_rev", `{"id":0,"steps":[{"status":"","actions":[{"kind":"propose_ready","to":"","to_node":false,"node":"n1","round":0,"suspect":false,"alive":false,"mod_rev":-1}],"suspended":false,"nodes":[],"granted":[],"ok":false}]}`},
		{"action null alive", `{"id":0,"steps":[{"status":"","actions":[{"kind":"vote","to":"a","to_node":false,"node":"n1","round":1,"suspect":false,"alive":null,"mod_rev":0}],"suspended":false,"nodes":[],"granted":[],"ok":false}]}`},
		{"node missing heard", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":0,"next_round_at":0,"next_ready_at":0,"next_evict_at":0,"not_ready_since":0,"doubted":false}],"granted":[],"ok":false}]}`},
		{"node missing backoff", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"heard":false,"next_round_at":0,"next_ready_at":0,"next_evict_at":0,"not_ready_since":0,"doubted":false}],"granted":[],"ok":false}]}`},
		{"node missing next_round_at", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":0,"heard":false,"next_ready_at":0,"next_evict_at":0,"not_ready_since":0,"doubted":false}],"granted":[],"ok":false}]}`},
		{"node missing next_ready_at", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":0,"heard":false,"next_round_at":0,"next_evict_at":0,"not_ready_since":0,"doubted":false}],"granted":[],"ok":false}]}`},
		{"node missing next_evict_at", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":0,"heard":false,"next_round_at":0,"next_ready_at":0,"not_ready_since":0,"doubted":false}],"granted":[],"ok":false}]}`},
		{"node missing not_ready_since", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":0,"heard":false,"next_round_at":0,"next_ready_at":0,"next_evict_at":0,"doubted":false}],"granted":[],"ok":false}]}`},
		{"node missing doubted", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":0,"heard":false,"next_round_at":0,"next_ready_at":0,"next_evict_at":0,"not_ready_since":0}],"granted":[],"ok":false}]}`},
		{"node null doubted", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":0,"heard":false,"next_round_at":0,"next_ready_at":0,"next_evict_at":0,"not_ready_since":0,"doubted":null}],"granted":[],"ok":false}]}`},
		{"node extra field", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":0,"heard":false,"next_round_at":0,"next_ready_at":0,"next_evict_at":0,"not_ready_since":0,"doubted":false,"extra":1}],"granted":[],"ok":false}]}`},
		{"node negative streak", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":-1,"round":0,"samples":2,"last":0,"backoff":0,"heard":false,"next_round_at":0,"next_ready_at":0,"next_evict_at":0,"not_ready_since":0,"doubted":false}],"granted":[],"ok":false}]}`},
		{"node negative backoff", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":-1,"heard":false,"next_round_at":0,"next_ready_at":0,"next_evict_at":0,"not_ready_since":0,"doubted":false}],"granted":[],"ok":false}]}`},
		{"node negative next_round_at", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":0,"heard":false,"next_round_at":-1,"next_ready_at":0,"next_evict_at":0,"not_ready_since":0,"doubted":false}],"granted":[],"ok":false}]}`},
		{"node negative next_ready_at", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":0,"heard":false,"next_round_at":0,"next_ready_at":-1,"next_evict_at":0,"not_ready_since":0,"doubted":false}],"granted":[],"ok":false}]}`},
		{"node negative next_evict_at", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":0,"heard":false,"next_round_at":0,"next_ready_at":0,"next_evict_at":-1,"not_ready_since":0,"doubted":false}],"granted":[],"ok":false}]}`},
		{"node negative not_ready_since", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":2,"last":0,"backoff":0,"heard":false,"next_round_at":0,"next_ready_at":0,"next_evict_at":0,"not_ready_since":-1,"doubted":false}],"granted":[],"ok":false}]}`},
		{"node float samples", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[{"node":"n1","streak":0,"round":0,"samples":1.5,"last":0,"backoff":0,"heard":false,"next_round_at":0,"next_ready_at":0,"next_evict_at":0,"not_ready_since":0,"doubted":false}],"granted":[],"ok":false}]}`},
		{"granted negative element", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[],"granted":[-1],"ok":false}]}`},
		{"granted float element", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[],"granted":[1.5],"ok":false}]}`},
		{"granted null element", `{"id":0,"steps":[{"status":"","actions":[],"suspended":false,"nodes":[],"granted":[null],"ok":false}]}`},
	}
	for _, tc := range invalidLines {
		if _, err := decodeLivenessResult([]byte(tc.line)); err == nil {
			t.Errorf("decodeLivenessResult accepted %s: %s", tc.name, tc.line)
		}
	}

	valid := `{"id":3,"steps":[` + emptyStep + `,{"status":"accepted","actions":[{"kind":"probe_request","to":"n2","to_node":true,"node":"n1","round":2,"suspect":false,"alive":false,"mod_rev":5}],"suspended":true,"nodes":[{"node":"n1","streak":1,"round":2,"samples":3,"last":100,"backoff":2,"heard":true,"next_round_at":500,"next_ready_at":600,"next_evict_at":700,"not_ready_since":50,"doubted":true}],"granted":[10,20],"ok":true}]}`
	got, err := decodeLivenessResult([]byte(valid))
	if err != nil {
		t.Fatalf("decodeLivenessResult(%s): %v", valid, err)
	}
	want := livenessResult{
		ID: 3,
		Steps: []livenessStep{
			{Actions: []livenessAction{}, Nodes: []livenessNodeSnapshot{}, Granted: []int64{}},
			{
				Status: "accepted",
				Actions: []livenessAction{{
					Kind: "probe_request", To: "n2", ToNode: true, Node: "n1", Round: 2, ModRev: 5,
				}},
				Suspended: true,
				Nodes: []livenessNodeSnapshot{{
					Node: "n1", Streak: 1, Round: 2, Samples: 3, Last: 100, Backoff: 2, Heard: true,
					NextRoundAt: 500, NextReadyAt: 600, NextEvictAt: 700, NotReadySince: 50, Doubted: true,
				}},
				Granted: []int64{10, 20},
				OK:      true,
			},
		},
	}
	if diff := compareLivenessResult(want, got); diff != "" {
		t.Fatalf("decodeLivenessResult(%s) differs from expected: %s", valid, diff)
	}
}

func TestLivenessDRTCatchesBrokenImplementations(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateLivenessCases(cfg.Seed, cfg.Cases)
	_, _, leanResults := leanLivenessResults(t, cases)

	variants := []struct {
		name  string
		hooks livenessHooks
	}{
		{"counting unanswered probers as failures", livenessHooks{unansweredProbesAsFail: true}},
		{"ignoring the alive veto", livenessHooks{ignoreAliveVeto: true}},
		{"no backoff", livenessHooks{cfgMutate: func(c *detector.Config) { c.MaxRoundBackoff = c.RetryInterval }}},
		{"backoff not reset by heartbeat", livenessHooks{backoffNotResetByHB: true}},
		{"round not cleared on ModRev change", livenessHooks{roundNotClearedOnModRev: true}},
		{"proposing with current record ModRev instead of round ModRev", livenessHooks{proposeCurrentModRev: true}},
		{"confirm on aggregator failures although node probers were asked", livenessHooks{confirmAggFailWithNodeProbers: true}},
		{"no witnesses asked when not suspended", livenessHooks{noWitnessesWhenNotSuspended: true}},
		{"witnesses drawn from tracked nodes only", livenessHooks{witnessesTrackedOnly: true}},
		{"rack probers include tracked-and-suspected rack-mates", livenessHooks{rackIncludesSuspected: true}},
		{"aggregator-only round confirms while suspended", livenessHooks{aggOnlyConfirmsSuspended: true}},
		{"eviction while suspended", livenessHooks{evictionWhileSuspended: true}},
		{"eviction grace measured from 0 instead of notReadySince", livenessHooks{evictionGraceFromZero: true}},
		{"streak not reset on NotReady", livenessHooks{streakNotResetOnNotReady: true}},
		{"votes that lie while suspended (suspect=false)", livenessHooks{votesLieWhileSuspended: true}},
		{"Alive computed without heard", livenessHooks{aliveWithoutHeard: true}},
		{"reverses action order", livenessHooks{reverseActions: true}},
		{"budget reports ok=true when over limit", livenessHooks{budgetOverReportsOKTrue: true}},
		{"budget treats e >= now-window as kept", livenessHooks{budgetKeepsWindowBoundary: true}},
		{"budget appends now at end instead of ascending", livenessHooks{budgetAppendsNowAtEnd: true}},
		{"ok after fail ignored", livenessHooks{okAfterFailIgnored: true}},
		{"alive after vote ignored", livenessHooks{aliveAfterVoteIgnored: true}},
		{"heartbeat not resetting nextRoundAt", livenessHooks{hbNotResetNextRoundAt: true}},
		{"shared propose timer", livenessHooks{sharedProposeTimer: true}},
		{"grace not restarted on a new ModRev", livenessHooks{graceNotRestartedOnModRev: true}},
		{"all-suspected not treated as self-suspicion", livenessHooks{allSuspectedNotSelfSuspicion: true}},
		{"round ids ignoring RoundBase", livenessHooks{cfgMutate: func(c *detector.Config) { c.RoundBase = 0 }}},
		{"doubted not set at open", livenessHooks{doubtedNotSetAtOpen: true}},
		{"doubted not accumulated at later ticks", livenessHooks{doubtedNotAccumulated: true}},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			mismatches := 0
			for i, tc := range cases {
				goRes, err := runLivenessCase(tc, v.hooks)
				if err != nil {
					t.Fatalf("id=%d: broken implementation failed instead of answering wrongly: %v", tc.ID, err)
				}
				if compareLivenessResult(leanResults[i], goRes) != "" {
					mismatches++
				}
			}
			if mismatches == 0 {
				t.Fatalf("broken implementation %q had 0 mismatches against the Lean model", v.name)
			}
			t.Logf("%d of %d cases differ", mismatches, len(cases))
		})
	}
}

func TestGenerateLivenessCasesIsDeterministic(t *testing.T) {
	a := bytes.Join(encodeLivenessCases(t, generateLivenessCases(1, 100)), []byte("\n"))
	b := bytes.Join(encodeLivenessCases(t, generateLivenessCases(1, 100)), []byte("\n"))
	c := bytes.Join(encodeLivenessCases(t, generateLivenessCases(2, 100)), []byte("\n"))

	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different encoded cases")
	}
	if bytes.Equal(a, c) {
		t.Fatal("different seeds produced identical encoded cases")
	}
}

func TestLivenessModelRejectsMalformedCase(t *testing.T) {
	valid := `{"id":0,"config":{"self":"a1","owners":1,"peer_probers":0,"rack_probers":0,"witness_probers":0,"min_failed_probes":1,"round_timeout":100,"retry_interval":100,"max_round_backoff":1000,"eviction_retry_interval":100,"self_suspect_percent":50,"self_suspect_min_nodes":1,"ready_heartbeats":1,"eviction_grace":0,"round_base":0,"phi":{"threshold":8,"max_samples":10,"min_std_dev":100,"acceptable_pause":3000,"first_interval":1000,"max_interval":10000}},"ranked":[],"ops":[{"op":"tick","now":0}]}`
	const prefix = `{"id":1,"config":{"self":"a1","owners":1,"peer_probers":0,"rack_probers":0,"witness_probers":0,"min_failed_probes":1,"round_timeout":100,"retry_interval":100,"max_round_backoff":1000,"eviction_retry_interval":100,"self_suspect_percent":50,"self_suspect_min_nodes":1,"ready_heartbeats":1,"eviction_grace":0,"round_base":0,"phi":{"threshold":8,"max_samples":10,"min_std_dev":100,"acceptable_pause":3000,"first_interval":1000,"max_interval":10000}},"ranked":[],"ops":[`
	for _, badOp := range []string{
		`{"op":"explode"}`,
		`{"op":"heartbeat","now":0}`,
		`{"op":"observed","now":0,"node":"n1","state":"dead","since":0,"rack":"r1","evictable":false,"mod_rev":1}`,
		`{"op":"tick","now":-1}`,
		`{"op":"vote_received","now":0,"from":"b","node":"n1","round":1,"suspect":true}`,
		`{"op":"budget","granted":[1],"now":0,"window":10,"limit":"1"}`,
	} {
		bad := prefix + badOp + `]}`
		cases := [][]byte{[]byte(valid), []byte(bad)}
		_, err := RunModel("liveness", cases)
		if err == nil {
			t.Errorf("RunModel accepted malformed case %s", bad)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte("line 2")) {
			t.Errorf("error %q does not name line 2 for %s", err.Error(), bad)
		}
	}
}
