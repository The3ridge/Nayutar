// Package detector implements the pure judgment state machine of a single
// liveness aggregator.
package detector

import (
	"errors"
	"slices"

	"github.com/Kaikei-e/Nayutar/core/liveness/phi"
)

const (
	maxRoundIntervalMS     = 600_000
	maxBackoffIntervalMS   = 3_600_000
	maxBackoffExponent     = 16
	evictionMaxRetryMS     = 3_600_000
	maxRoundBase           = uint64(1) << 52
	selfSuspectMaxPercent  = 100
	defaultRoundTimeoutMS  = 1000
	defaultRetryIntervalMS = 1000
	defaultMaxRoundBackoff = 60000
	defaultEvictionRetryMS = 5000
	defaultEvictionGraceMS = 300000
	defaultSelfSuspectPct  = 30
	defaultSelfSuspectMin  = 3
	defaultOwners          = 3
	defaultPeerProbers     = 2
	defaultRackProbers     = 2
	defaultWitnessProbers  = 3
	defaultMinFailedProbes = 2
	defaultReadyHeartbeats = 3
)

// State represents the persisted liveness state of a node in the store.
type State uint8

const (
	// StatePending marks a registered node that has not yet been confirmed Ready.
	StatePending State = iota + 1
	// StateReady marks a healthy node.
	StateReady
	// StateNotReady marks a node confirmed unreachable by an aggregator round.
	StateNotReady
)

// Record is the aggregator's copy of a node record in the store.
type Record struct {
	State     State
	Since     int64
	Rack      string
	Evictable bool
	ModRev    int64
}

// Config configures a detector Core.
type Config struct {
	Self                  string
	Owners                int
	PeerProbers           int
	RackProbers           int
	WitnessProbers        int
	MinFailedProbes       int
	RoundTimeout          int64
	RetryInterval         int64
	MaxRoundBackoff       int64
	EvictionRetryInterval int64
	SelfSuspectPercent    int
	SelfSuspectMinNodes   int
	ReadyHeartbeats       int
	EvictionGrace         int64
	RoundBase             uint64
	Phi                   phi.Config
	Ranked                func(node string) []string
}

// DefaultConfig returns a Config initialized with the standard defaults.
func DefaultConfig(self string, ranked func(node string) []string) Config {
	return Config{
		Self:                  self,
		Owners:                defaultOwners,
		PeerProbers:           defaultPeerProbers,
		RackProbers:           defaultRackProbers,
		WitnessProbers:        defaultWitnessProbers,
		MinFailedProbes:       defaultMinFailedProbes,
		RoundTimeout:          defaultRoundTimeoutMS,
		RetryInterval:         defaultRetryIntervalMS,
		MaxRoundBackoff:       defaultMaxRoundBackoff,
		EvictionRetryInterval: defaultEvictionRetryMS,
		SelfSuspectPercent:    defaultSelfSuspectPct,
		SelfSuspectMinNodes:   defaultSelfSuspectMin,
		ReadyHeartbeats:       defaultReadyHeartbeats,
		EvictionGrace:         defaultEvictionGraceMS,
		Phi:                   phi.DefaultConfig(),
		Ranked:                ranked,
	}
}

// Validate checks that every field of Config is valid.
func (c Config) Validate() error {
	if c.Self == "" {
		return errors.New("detector: self must be non-empty")
	}
	if c.Owners < 1 {
		return errors.New("detector: owners must be >= 1")
	}
	if c.PeerProbers < 0 {
		return errors.New("detector: peer probers must be >= 0")
	}
	if c.RackProbers < 0 {
		return errors.New("detector: rack probers must be >= 0")
	}
	if c.WitnessProbers < 0 {
		return errors.New("detector: witness probers must be >= 0")
	}
	if c.MinFailedProbes < 1 {
		return errors.New("detector: min failed probes must be >= 1")
	}
	if c.RoundTimeout < 1 || c.RoundTimeout > maxRoundIntervalMS {
		return errors.New("detector: round timeout out of range [1, 600000]")
	}
	if c.RetryInterval < 1 || c.RetryInterval > maxRoundIntervalMS {
		return errors.New("detector: retry interval out of range [1, 600000]")
	}
	if c.MaxRoundBackoff < c.RetryInterval || c.MaxRoundBackoff > maxBackoffIntervalMS {
		return errors.New("detector: max round backoff out of range [RetryInterval, 3600000]")
	}
	if c.EvictionRetryInterval < 1 || c.EvictionRetryInterval > evictionMaxRetryMS {
		return errors.New("detector: eviction retry interval out of range [1, 3600000]")
	}
	if c.SelfSuspectPercent < 1 || c.SelfSuspectPercent > selfSuspectMaxPercent {
		return errors.New("detector: self suspect percent out of range [1, 100]")
	}
	if c.SelfSuspectMinNodes < 1 {
		return errors.New("detector: self suspect min nodes must be >= 1")
	}
	if c.ReadyHeartbeats < 1 {
		return errors.New("detector: ready heartbeats must be >= 1")
	}
	if c.EvictionGrace < 0 {
		return errors.New("detector: eviction grace must be >= 0")
	}
	if c.RoundBase > maxRoundBase {
		return errors.New("detector: round base out of range [0, 2^52]")
	}
	if err := c.Phi.Validate(); err != nil {
		return err
	}
	if c.Ranked == nil {
		return errors.New("detector: ranked function must be non-nil")
	}
	return nil
}

// ActionKind identifies the kind of output Action emitted by Core.
type ActionKind uint8

const (
	// ActVoteRequest asks a peer owner to vote on whether a node is suspect.
	ActVoteRequest ActionKind = iota + 1
	// ActVote replies to a peer owner's vote request.
	ActVote
	// ActProbeRequest asks a peer aggregator or node to probe a target node.
	ActProbeRequest
	// ActProposeReady proposes transitioning a node to StateReady.
	ActProposeReady
	// ActProposeNotReady proposes transitioning a node to StateNotReady.
	ActProposeNotReady
	// ActProposeEvictable proposes marking a NotReady node as Evictable.
	ActProposeEvictable
)

// Action describes an outbound message or store proposal emitted by Core.
type Action struct {
	Kind    ActionKind
	To      string
	ToNode  bool
	Node    string
	Round   uint64
	Suspect bool
	Alive   bool
	ModRev  int64
}

// HeartbeatStatus reports how Core.Heartbeat classified an incoming heartbeat.
type HeartbeatStatus uint8

const (
	// HeartbeatAccepted indicates the node is known and owned by Self.
	HeartbeatAccepted HeartbeatStatus = iota + 1
	// HeartbeatUnknownNode indicates the node is not currently known.
	HeartbeatUnknownNode
	// HeartbeatNotOwner indicates Self is not among the node's owners.
	HeartbeatNotOwner
)

// Snapshot is an immutable point-in-time view of Core's tracked state.
type Snapshot struct {
	Suspended bool
	Nodes     []NodeSnapshot
}

// NodeSnapshot is the tracked state for a single node in a Snapshot.
type NodeSnapshot struct {
	Node          string
	Streak        int
	Round         uint64
	Samples       int
	Last          int64
	Backoff       int
	Heard         bool
	NextRoundAt   int64
	NextReadyAt   int64
	NextEvictAt   int64
	NotReadySince int64
	Doubted       bool
}

type prober struct {
	id     string
	isNode bool
}

type round struct {
	id           uint64
	deadline     int64
	modRev       int64
	voters       []string
	voted        []string
	suspectVotes int
	probers      []prober
	failed       []prober
	doubted      bool
}

type track struct {
	window        *phi.Window
	streak        int
	heard         bool
	backoff       int
	notReadySince int64
	round         *round
	nextRoundAt   int64
	nextReadyAt   int64
	nextEvictAt   int64
}

// Core is the deterministic liveness judgment state machine for one aggregator.
type Core struct {
	cfg       Config
	now       int64
	known     map[string]Record
	tracks    map[string]*track
	seq       uint64
	suspended bool
}

// New validates cfg and constructs a new Core.
func New(cfg Config) (*Core, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Core{
		cfg:    cfg,
		known:  make(map[string]Record),
		tracks: make(map[string]*track),
	}, nil
}

func (c *Core) clampNow(now int64) int64 {
	if now < c.now {
		return c.now
	}
	c.now = now
	return now
}

func (c *Core) ranked(node string) []string {
	raw := c.cfg.Ranked(node)
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, id := range raw {
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func (c *Core) ownersFrom(ranked []string) []string {
	return ranked[:min(c.cfg.Owners, len(ranked))]
}

func (c *Core) owns(node string) bool {
	ranked := c.ranked(node)
	return slices.Contains(c.ownersFrom(ranked), c.cfg.Self)
}

func (c *Core) votersFromOwners(owners []string) []string {
	var out []string
	for _, id := range owners {
		if id != c.cfg.Self {
			out = append(out, id)
		}
	}
	return out
}

func (c *Core) peerProbersFrom(ranked, owners, voters []string) []string {
	if c.cfg.PeerProbers == 0 {
		return nil
	}
	out := make([]string, 0, c.cfg.PeerProbers)
	for _, id := range ranked[len(owners):] {
		if id == c.cfg.Self {
			continue
		}
		out = append(out, id)
		if len(out) == c.cfg.PeerProbers {
			return out
		}
	}
	for _, id := range voters {
		out = append(out, id)
		if len(out) == c.cfg.PeerProbers {
			return out
		}
	}
	return out
}

func rotateCandidates(cands []string, node string) []string {
	if len(cands) == 0 {
		return nil
	}
	slices.Sort(cands)
	rot := 0
	for i, m := range cands {
		if m > node {
			rot = i
			break
		}
	}
	if rot == 0 {
		return cands
	}
	rotated := make([]string, 0, len(cands))
	rotated = append(rotated, cands[rot:]...)
	rotated = append(rotated, cands[:rot]...)
	return rotated
}

func (c *Core) rackProbers(node string, now int64) []string {
	if c.cfg.RackProbers == 0 {
		return nil
	}
	nodeRack := c.known[node].Rack
	var cands []string
	for m, rec := range c.known {
		if m == node || rec.Rack != nodeRack || rec.State != StateReady {
			continue
		}
		if tr, tracked := c.tracks[m]; tracked && tr.window.Suspect(now) {
			continue
		}
		cands = append(cands, m)
	}
	cands = rotateCandidates(cands, node)
	if len(cands) > c.cfg.RackProbers {
		cands = cands[:c.cfg.RackProbers]
	}
	return cands
}

func (c *Core) witnessProbers(node string, now int64) []string {
	if c.cfg.WitnessProbers == 0 {
		return nil
	}
	nodeRack := c.known[node].Rack
	var cands []string
	for m, rec := range c.known {
		if m == node || rec.Rack == nodeRack || rec.State != StateReady {
			continue
		}
		if tr, tracked := c.tracks[m]; tracked && (!tr.heard || tr.window.Suspect(now)) {
			continue
		}
		cands = append(cands, m)
	}
	cands = rotateCandidates(cands, node)
	if len(cands) > c.cfg.WitnessProbers {
		cands = cands[:c.cfg.WitnessProbers]
	}
	return cands
}

func (c *Core) probersFrom(node string, now int64, ranked, owners, voters []string) []prober {
	peers := c.peerProbersFrom(ranked, owners, voters)
	racks := c.rackProbers(node, now)
	witnesses := c.witnessProbers(node, now)
	out := make([]prober, 0, len(peers)+len(racks)+len(witnesses))
	for _, id := range peers {
		out = append(out, prober{id: id, isNode: false})
	}
	for _, id := range racks {
		out = append(out, prober{id: id, isNode: true})
	}
	for _, id := range witnesses {
		out = append(out, prober{id: id, isNode: true})
	}
	return out
}

func (c *Core) delay(k int) int64 {
	exp := min(max(k, 0), maxBackoffExponent)
	return min(c.cfg.RetryInterval<<exp, c.cfg.MaxRoundBackoff)
}

func (c *Core) bump(tr *track, now int64) {
	tr.backoff = min(tr.backoff+1, maxBackoffExponent)
	tr.nextRoundAt = max(tr.nextRoundAt, now+c.delay(tr.backoff))
}

// Observed updates Core with a node record observed from the store.
func (c *Core) Observed(now int64, node string, rec Record) {
	now = c.clampNow(now)
	prev, had := c.known[node]
	c.known[node] = rec

	tr, tracked := c.tracks[node]
	if !tracked {
		if c.owns(node) && (rec.State == StateReady || rec.State == StateNotReady) {
			w := phi.New(c.cfg.Phi)
			w.Heartbeat(now)
			c.tracks[node] = &track{
				window:        w,
				notReadySince: now,
			}
		}
		return
	}

	if tr.round != nil && (rec.State != StateReady || rec.ModRev != tr.round.modRev) {
		tr.round = nil
	}
	if rec.State == StateNotReady && (!had || prev.State != StateNotReady || prev.ModRev != rec.ModRev) {
		tr.streak = 0
		tr.notReadySince = now
	}
	if rec.State == StateReady && had && prev.State == StateNotReady && tr.window.Suspect(now) {
		c.bump(tr, now)
	}
}

// Forgotten removes a node from Core's known and tracked sets.
func (c *Core) Forgotten(now int64, node string) {
	_ = c.clampNow(now)
	delete(c.known, node)
	delete(c.tracks, node)
}

// Heartbeat records a heartbeat from node at monotonic time now.
func (c *Core) Heartbeat(now int64, node string) (HeartbeatStatus, []Action) {
	now = c.clampNow(now)
	rec, ok := c.known[node]
	if !ok {
		return HeartbeatUnknownNode, nil
	}
	if !c.owns(node) {
		return HeartbeatNotOwner, nil
	}
	tr, tracked := c.tracks[node]
	if !tracked {
		tr = &track{window: phi.New(c.cfg.Phi)}
		c.tracks[node] = tr
	}
	was := tr.window.Suspect(now)
	tr.window.Heartbeat(now)
	if was {
		tr.streak = 1
	} else {
		tr.streak = min(tr.streak+1, c.cfg.ReadyHeartbeats)
	}
	tr.round = nil
	tr.heard = true
	tr.backoff = 0
	tr.nextRoundAt = 0
	var actions []Action
	if rec.State != StateReady && tr.streak >= c.cfg.ReadyHeartbeats && now >= tr.nextReadyAt {
		actions = append(actions, Action{
			Kind:   ActProposeReady,
			Node:   node,
			ModRev: rec.ModRev,
		})
		tr.nextReadyAt = now + c.cfg.RetryInterval
	}
	return HeartbeatAccepted, actions
}

// Tick advances round evaluation, round initiation, self-suspicion, and eviction checks.
func (c *Core) Tick(now int64) []Action {
	now = c.clampNow(now)

	names := make([]string, 0, len(c.tracks))
	alive := 0
	suspected := 0
	for name, tr := range c.tracks {
		names = append(names, name)
		if c.known[name].State == StateReady {
			alive++
			if tr.window.Suspect(now) {
				suspected++
			}
		}
	}
	slices.Sort(names)

	c.suspended = (suspected >= c.cfg.SelfSuspectMinNodes &&
		suspected*100 >= c.cfg.SelfSuspectPercent*alive) ||
		(alive >= 2 && suspected == alive)

	var actions []Action
	for _, name := range names {
		tr := c.tracks[name]
		rec := c.known[name]

		if tr.round != nil {
			tr.round.doubted = tr.round.doubted || c.suspended
			if now >= tr.round.deadline {
				r := tr.round
				failures := len(r.failed)
				nodeFailures := 0
				for _, p := range r.failed {
					if p.isNode {
						nodeFailures++
					}
				}
				nodeAsked := 0
				for _, p := range r.probers {
					if p.isNode {
						nodeAsked++
					}
				}
				owners := c.ownersFrom(c.ranked(name))
				quorum := len(owners)/2 + 1
				base := rec.State == StateReady && rec.ModRev == r.modRev &&
					1+r.suspectVotes >= quorum
				var confirm bool
				switch {
				case nodeAsked >= 1:
					confirm = base && nodeFailures >= min(c.cfg.MinFailedProbes, nodeAsked)
				case r.doubted:
					confirm = false
				default:
					confirm = base && len(r.probers) >= 1 &&
						failures >= min(c.cfg.MinFailedProbes, len(r.probers))
				}
				if confirm {
					actions = append(actions, Action{
						Kind:   ActProposeNotReady,
						Node:   name,
						ModRev: r.modRev,
					})
				}
				tr.round = nil
				tr.nextRoundAt = now + c.cfg.RetryInterval
			}
		} else if rec.State == StateReady && tr.window.Suspect(now) && now >= tr.nextRoundAt {
			c.seq++
			id := c.cfg.RoundBase + c.seq
			ranked := c.ranked(name)
			owners := c.ownersFrom(ranked)
			voters := c.votersFromOwners(owners)
			probers := c.probersFrom(name, now, ranked, owners, voters)
			tr.round = &round{
				id:       id,
				deadline: now + c.cfg.RoundTimeout,
				modRev:   rec.ModRev,
				voters:   voters,
				probers:  probers,
				doubted:  c.suspended,
			}
			for _, v := range voters {
				actions = append(actions, Action{
					Kind:  ActVoteRequest,
					To:    v,
					Node:  name,
					Round: id,
				})
			}
			for _, p := range probers {
				actions = append(actions, Action{
					Kind:   ActProbeRequest,
					To:     p.id,
					ToNode: p.isNode,
					Node:   name,
					Round:  id,
				})
			}
		}

		if !c.suspended && rec.State == StateNotReady && !rec.Evictable &&
			now-tr.notReadySince >= c.cfg.EvictionGrace &&
			tr.window.Suspect(now) && now >= tr.nextEvictAt {
			actions = append(actions, Action{
				Kind:   ActProposeEvictable,
				Node:   name,
				ModRev: rec.ModRev,
			})
			tr.nextEvictAt = now + c.cfg.EvictionRetryInterval
		}
	}
	return actions
}

// VoteRequested answers an incoming vote request from a peer aggregator.
func (c *Core) VoteRequested(now int64, from, node string, round uint64) []Action {
	now = c.clampNow(now)
	tr, tracked := c.tracks[node]
	rec, known := c.known[node]
	var suspect, alive bool
	if tracked && known && rec.State == StateReady {
		suspect = tr.window.Suspect(now)
		alive = tr.heard && !suspect
	}
	return []Action{{
		Kind:    ActVote,
		To:      from,
		Node:    node,
		Round:   round,
		Suspect: suspect,
		Alive:   alive,
	}}
}

// VoteReceived records a peer aggregator's vote for an active suspect round.
func (c *Core) VoteReceived(now int64, from, node string, round uint64, suspect, alive bool) {
	now = c.clampNow(now)
	tr, tracked := c.tracks[node]
	if !tracked || tr.round == nil || tr.round.id != round {
		return
	}
	r := tr.round
	if !slices.Contains(r.voters, from) {
		return
	}
	if alive {
		tr.round = nil
		c.bump(tr, now)
		return
	}
	if slices.Contains(r.voted, from) {
		return
	}
	r.voted = append(r.voted, from)
	if suspect {
		r.suspectVotes++
	}
}

// ProbeResult records an indirect probe outcome for an active suspect round.
func (c *Core) ProbeResult(now int64, from string, fromNode bool, node string, round uint64, ok bool) {
	now = c.clampNow(now)
	tr, tracked := c.tracks[node]
	if !tracked || tr.round == nil || tr.round.id != round {
		return
	}
	p := prober{id: from, isNode: fromNode}
	r := tr.round
	if !slices.Contains(r.probers, p) {
		return
	}
	if ok {
		tr.round = nil
		c.bump(tr, now)
		return
	}
	if slices.Contains(r.failed, p) {
		return
	}
	r.failed = append(r.failed, p)
}

// Suspended reports whether the last Tick triggered self-suspicion.
func (c *Core) Suspended() bool {
	return c.suspended
}

// Snapshot returns an ordered view of Core's tracked state.
func (c *Core) Snapshot() Snapshot {
	names := make([]string, 0, len(c.tracks))
	for name := range c.tracks {
		names = append(names, name)
	}
	slices.Sort(names)
	nodes := make([]NodeSnapshot, len(names))
	for i, name := range names {
		tr := c.tracks[name]
		var roundID uint64
		var doubted bool
		if tr.round != nil {
			roundID = tr.round.id
			doubted = tr.round.doubted
		}
		nodes[i] = NodeSnapshot{
			Node:          name,
			Streak:        tr.streak,
			Round:         roundID,
			Samples:       len(tr.window.Samples()),
			Last:          tr.window.Last(),
			Backoff:       tr.backoff,
			Heard:         tr.heard,
			NextRoundAt:   tr.nextRoundAt,
			NextReadyAt:   tr.nextReadyAt,
			NextEvictAt:   tr.nextEvictAt,
			NotReadySince: tr.notReadySince,
			Doubted:       doubted,
		}
	}
	return Snapshot{
		Suspended: c.suspended,
		Nodes:     nodes,
	}
}
