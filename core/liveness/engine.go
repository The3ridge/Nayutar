package liveness

import (
	"errors"
	"slices"

	"github.com/Kaikei-e/Nayutar/core/liveness/agent"
	"github.com/Kaikei-e/Nayutar/core/liveness/detector"
	"github.com/Kaikei-e/Nayutar/core/liveness/hrw"
	"github.com/Kaikei-e/Nayutar/core/liveness/relay"
	"github.com/Kaikei-e/Nayutar/core/store"
)

// Addr names an endpoint: an aggregator ID or a node name.
type Addr struct {
	ID   string
	Node bool
}

// MessageKind identifies the kind of peer or node liveness message.
type MessageKind uint8

const (
	// MsgHeartbeat is sent periodically by a node to its owner aggregators.
	MsgHeartbeat MessageKind = iota + 1
	// MsgVoteRequest asks a peer owner aggregator whether a node is suspected.
	MsgVoteRequest
	// MsgVote replies to a MsgVoteRequest with the peer's suspicion and liveness verdict.
	MsgVote
	// MsgProbeRequest asks a peer aggregator or rack-mate node to probe a target node.
	MsgProbeRequest
	// MsgProbeResult reports the outcome of a relayed indirect probe.
	MsgProbeResult
	// MsgPing challenges a target node during an indirect probe.
	MsgPing
	// MsgAck answers a MsgPing with its probe token.
	MsgAck
)

// Message is one envelope exchanged between aggregators and nodes.
type Message struct {
	Kind    MessageKind
	From    Addr
	To      Addr
	Node    string // subject node (heartbeat sender, vote subject, probe target)
	Round   uint64
	Token   uint64
	Suspect bool
	Alive   bool
	OK      bool
}

// ProposalKind identifies the kind of store transition proposed by an aggregator.
type ProposalKind uint8

const (
	// ProposeReady proposes transitioning a node record to Ready.
	ProposeReady ProposalKind = iota + 1
	// ProposeNotReady proposes transitioning a Ready node record to NotReady.
	ProposeNotReady
	// ProposeEvictable proposes marking a NotReady node record evictable.
	ProposeEvictable
)

// Proposal is a store write the aggregator wants; ModRev is the record revision it is based on.
type Proposal struct {
	Kind   ProposalKind
	Node   string
	ModRev store.Rev
}

// AggregatorEngineConfig configures an AggregatorEngine.
type AggregatorEngineConfig struct {
	ID           string
	Aggregators  []string        // the full aggregator set, including ID
	Detector     detector.Config // Self and Ranked are filled in by NewAggregatorEngine
	PingTimeout  int64           // relay timeout, ms (initial 800)
	PingInterval int64           // relay re-send interval, ms (initial 100)
}

// DefaultAggregatorEngineConfig returns an AggregatorEngineConfig with standard defaults.
func DefaultAggregatorEngineConfig(id string, aggregators []string) AggregatorEngineConfig {
	aggs := slices.Clone(aggregators)
	return AggregatorEngineConfig{
		ID:          id,
		Aggregators: aggs,
		Detector: detector.DefaultConfig(id, func(node string) []string {
			return hrw.Ranked(node, aggs)
		}),
		PingTimeout:  800,
		PingInterval: 100,
	}
}

// AggregatorEngine is the pure aggregator-side glue over detector.Core and relay.Relay.
type AggregatorEngine struct {
	core    *detector.Core
	relay   *relay.Relay
	self    Addr
	pending []Proposal
}

// NewAggregatorEngine validates cfg and constructs an AggregatorEngine.
func NewAggregatorEngine(cfg AggregatorEngineConfig) (*AggregatorEngine, error) {
	if cfg.ID == "" {
		return nil, errors.New("liveness: aggregator ID must be non-empty")
	}
	if !slices.Contains(cfg.Aggregators, cfg.ID) {
		return nil, errors.New("liveness: aggregator ID must be in Aggregators")
	}
	if cfg.PingTimeout < 1 {
		return nil, errors.New("liveness: ping timeout must be >= 1")
	}
	if cfg.PingInterval < 1 {
		return nil, errors.New("liveness: ping interval must be >= 1")
	}
	aggs := slices.Clone(cfg.Aggregators)
	detCfg := cfg.Detector
	detCfg.Self = cfg.ID
	detCfg.Ranked = func(node string) []string {
		return hrw.Ranked(node, aggs)
	}
	core, err := detector.New(detCfg)
	if err != nil {
		return nil, err
	}
	if cfg.PingTimeout >= detCfg.RoundTimeout {
		return nil, errors.New("liveness: ping timeout must be < detector round timeout")
	}
	return &AggregatorEngine{
		core:  core,
		relay: relay.New(cfg.PingTimeout, cfg.PingInterval),
		self:  Addr{ID: cfg.ID, Node: false},
	}, nil
}

// Observed feeds a store record update into the detector core.
func (e *AggregatorEngine) Observed(now int64, rec Record) {
	e.core.Observed(now, rec.Node, detector.Record{
		State:     rec.State,
		Since:     rec.Since,
		Rack:      rec.Rack,
		Evictable: rec.Evictable,
		ModRev:    int64(rec.ModRev),
	})
}

// Forgotten removes a deleted node from the detector core.
func (e *AggregatorEngine) Forgotten(now int64, node string) {
	e.core.Forgotten(now, node)
}

// Handle processes one incoming Message and returns any outbound Messages.
func (e *AggregatorEngine) Handle(now int64, m Message) []Message {
	switch m.Kind {
	case MsgHeartbeat:
		_, actions := e.core.Heartbeat(now, m.From.ID)
		for _, act := range actions {
			if act.Kind == detector.ActProposeReady {
				e.pending = append(e.pending, Proposal{
					Kind:   ProposeReady,
					Node:   act.Node,
					ModRev: store.Rev(act.ModRev),
				})
			}
		}
		return nil
	case MsgVoteRequest:
		actions := e.core.VoteRequested(now, m.From.ID, m.Node, m.Round)
		var out []Message
		for _, act := range actions {
			if act.Kind == detector.ActVote {
				out = append(out, Message{
					Kind:    MsgVote,
					From:    e.self,
					To:      m.From,
					Node:    act.Node,
					Round:   act.Round,
					Suspect: act.Suspect,
					Alive:   act.Alive,
				})
			}
		}
		return out
	case MsgVote:
		e.core.VoteReceived(now, m.From.ID, m.Node, m.Round, m.Suspect, m.Alive)
		return nil
	case MsgProbeRequest:
		token := e.relay.Request(now, m.From.ID, m.From.Node, m.Node, m.Round)
		return []Message{{
			Kind:  MsgPing,
			From:  e.self,
			To:    Addr{ID: m.Node, Node: true},
			Token: token,
		}}
	case MsgAck:
		res, ok := e.relay.Ack(now, m.From.ID, m.Token)
		if !ok {
			return nil
		}
		return []Message{{
			Kind:  MsgProbeResult,
			From:  e.self,
			To:    Addr{ID: res.Requester, Node: res.RequesterNode},
			Node:  res.Target,
			Round: res.Round,
			OK:    true,
		}}
	case MsgProbeResult:
		e.core.ProbeResult(now, m.From.ID, m.From.Node, m.Node, m.Round, m.OK)
		return nil
	default:
		return nil
	}
}

// Tick expires and re-sends pending probes, advances the detector core, and returns outbound messages and proposals.
func (e *AggregatorEngine) Tick(now int64) ([]Message, []Proposal) {
	var msgs []Message
	for _, res := range e.relay.Expire(now) {
		msgs = append(msgs, Message{
			Kind:  MsgProbeResult,
			From:  e.self,
			To:    Addr{ID: res.Requester, Node: res.RequesterNode},
			Node:  res.Target,
			Round: res.Round,
			OK:    false,
		})
	}
	for _, p := range e.relay.Resend(now) {
		msgs = append(msgs, Message{
			Kind:  MsgPing,
			From:  e.self,
			To:    Addr{ID: p.Target, Node: true},
			Token: p.Token,
		})
	}

	proposals := e.pending
	e.pending = nil

	for _, act := range e.core.Tick(now) {
		switch act.Kind {
		case detector.ActVoteRequest:
			msgs = append(msgs, Message{
				Kind:  MsgVoteRequest,
				From:  e.self,
				To:    Addr{ID: act.To},
				Node:  act.Node,
				Round: act.Round,
			})
		case detector.ActProbeRequest:
			msgs = append(msgs, Message{
				Kind:  MsgProbeRequest,
				From:  e.self,
				To:    Addr{ID: act.To, Node: act.ToNode},
				Node:  act.Node,
				Round: act.Round,
			})
		case detector.ActProposeReady:
			proposals = append(proposals, Proposal{
				Kind:   ProposeReady,
				Node:   act.Node,
				ModRev: store.Rev(act.ModRev),
			})
		case detector.ActProposeNotReady:
			proposals = append(proposals, Proposal{
				Kind:   ProposeNotReady,
				Node:   act.Node,
				ModRev: store.Rev(act.ModRev),
			})
		case detector.ActProposeEvictable:
			proposals = append(proposals, Proposal{
				Kind:   ProposeEvictable,
				Node:   act.Node,
				ModRev: store.Rev(act.ModRev),
			})
		}
	}
	return msgs, proposals
}

// Suspended reports whether the last Tick triggered aggregator self-suspicion.
func (e *AggregatorEngine) Suspended() bool {
	return e.core.Suspended()
}

// NodeEngine is the pure node-side glue over agent.Agent.
type NodeEngine struct {
	agent *agent.Agent
	self  Addr
}

// NewNodeEngine validates cfg and constructs a NodeEngine.
func NewNodeEngine(cfg agent.Config) (*NodeEngine, error) {
	ag, err := agent.New(cfg)
	if err != nil {
		return nil, err
	}
	return &NodeEngine{
		agent: ag,
		self:  Addr{ID: cfg.Node, Node: true},
	}, nil
}

func (e *NodeEngine) translateOuts(outs []agent.Out) []Message {
	if len(outs) == 0 {
		return nil
	}
	msgs := make([]Message, 0, len(outs))
	for _, out := range outs {
		switch out.Kind {
		case agent.OutHeartbeat:
			msgs = append(msgs, Message{
				Kind: MsgHeartbeat,
				From: e.self,
				To:   Addr{ID: out.To},
				Node: e.self.ID,
			})
		case agent.OutAck:
			msgs = append(msgs, Message{
				Kind:  MsgAck,
				From:  e.self,
				To:    Addr{ID: out.To, Node: out.ToNode},
				Token: out.Token,
			})
		case agent.OutPing:
			msgs = append(msgs, Message{
				Kind:  MsgPing,
				From:  e.self,
				To:    Addr{ID: out.To, Node: true},
				Token: out.Token,
			})
		case agent.OutProbeResult:
			msgs = append(msgs, Message{
				Kind:  MsgProbeResult,
				From:  e.self,
				To:    Addr{ID: out.To, Node: out.ToNode},
				Node:  out.Target,
				Round: out.Round,
				OK:    out.OK,
			})
		}
	}
	return msgs
}

// Handle processes one incoming Message and returns any outbound Messages.
func (e *NodeEngine) Handle(now int64, m Message) []Message {
	switch m.Kind {
	case MsgPing:
		return e.translateOuts(e.agent.Pinged(now, m.From.ID, m.From.Node, m.Token))
	case MsgProbeRequest:
		return e.translateOuts(e.agent.ProbeRequested(now, m.From.ID, m.From.Node, m.Node, m.Round))
	case MsgAck:
		return e.translateOuts(e.agent.Acked(now, m.From.ID, m.Token))
	default:
		return nil
	}
}

// Tick advances the node agent and returns outbound heartbeats, expired probe results, and re-sent pings.
func (e *NodeEngine) Tick(now int64) []Message {
	return e.translateOuts(e.agent.Tick(now))
}
