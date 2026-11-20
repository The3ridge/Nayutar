// Package agent implements the node-side liveness state machine without goroutines, clocks, or network calls.
package agent

import (
	"errors"
	"fmt"

	"github.com/Kaikei-e/Nayutar/core/liveness/relay"
)

// ErrInvalidConfig is returned when Config fails validation.
var ErrInvalidConfig = errors.New("agent: invalid config")

// Config configures a node-side liveness Agent.
type Config struct {
	Node         string
	Interval     int64 // heartbeat interval ms >= 1 (initial 1000)
	Owners       int   // >= 1 (initial 3)
	PingTimeout  int64 // ms >= 1 (initial 800)
	PingInterval int64 // ms >= 1 (initial 100)
	Ranked       func(node string) []string
}

// DefaultConfig returns a Config with the default liveness intervals and owner count.
func DefaultConfig(node string, ranked func(node string) []string) Config {
	return Config{
		Node:         node,
		Interval:     1000,
		Owners:       3,
		PingTimeout:  800,
		PingInterval: 100,
		Ranked:       ranked,
	}
}

// Validate checks that Node is non-empty, bounds are positive, and Ranked is non-nil.
func (c Config) Validate() error {
	if c.Node == "" {
		return fmt.Errorf("%w: Node must not be empty", ErrInvalidConfig)
	}
	if c.Interval < 1 {
		return fmt.Errorf("%w: Interval must be >= 1", ErrInvalidConfig)
	}
	if c.Owners < 1 {
		return fmt.Errorf("%w: Owners must be >= 1", ErrInvalidConfig)
	}
	if c.PingTimeout < 1 {
		return fmt.Errorf("%w: PingTimeout must be >= 1", ErrInvalidConfig)
	}
	if c.PingInterval < 1 {
		return fmt.Errorf("%w: PingInterval must be >= 1", ErrInvalidConfig)
	}
	if c.Ranked == nil {
		return fmt.Errorf("%w: Ranked must not be nil", ErrInvalidConfig)
	}
	return nil
}

// OutKind identifies the kind of outbound message emitted by an Agent.
type OutKind uint8

const (
	// OutHeartbeat sends a heartbeat to an owner aggregator in To.
	OutHeartbeat OutKind = iota + 1
	// OutAck answers a ping from the sender in To/ToNode with Token.
	OutAck
	// OutPing sends a relayed probe ping to the target node in To with ToNode true and Token.
	OutPing
	// OutProbeResult reports the outcome of a relayed probe to the requester in To/ToNode.
	OutProbeResult
)

// Out describes one outbound message produced by the Agent state machine.
type Out struct {
	Kind   OutKind
	To     string
	ToNode bool
	Token  uint64
	Target string
	Round  uint64
	OK     bool
}

// Agent is the deterministic node-side liveness state machine.
type Agent struct {
	cfg     Config
	relay   *relay.Relay
	started bool
	next    int64
}

// New constructs an Agent after validating cfg.
func New(cfg Config) (*Agent, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Agent{
		cfg:   cfg,
		relay: relay.New(cfg.PingTimeout, cfg.PingInterval),
	}, nil
}

func (a *Agent) owners() []string {
	ranked := a.cfg.Ranked(a.cfg.Node)
	seen := make(map[string]struct{}, len(ranked))
	out := make([]string, 0, min(a.cfg.Owners, len(ranked)))
	for _, agg := range ranked {
		if _, ok := seen[agg]; ok {
			continue
		}
		seen[agg] = struct{}{}
		out = append(out, agg)
		if len(out) == a.cfg.Owners {
			break
		}
	}
	return out
}

// Tick emits, in this order: a heartbeat to every owner when now >= next (first Tick always sends; then
// next = now + Interval); a ProbeResult{OK:false} for every relayed probe that expired; a Ping for every
// relayed probe due for a re-send.
func (a *Agent) Tick(now int64) []Out {
	var out []Out
	if !a.started || now >= a.next {
		a.started = true
		a.next = now + a.cfg.Interval
		for _, agg := range a.owners() {
			out = append(out, Out{
				Kind: OutHeartbeat,
				To:   agg,
			})
		}
	}
	for _, res := range a.relay.Expire(now) {
		out = append(out, Out{
			Kind:   OutProbeResult,
			To:     res.Requester,
			ToNode: res.RequesterNode,
			Target: res.Target,
			Round:  res.Round,
			OK:     false,
		})
	}
	for _, p := range a.relay.Resend(now) {
		out = append(out, Out{
			Kind:   OutPing,
			To:     p.Target,
			ToNode: true,
			Token:  p.Token,
		})
	}
	return out
}

// Pinged answers a ping addressed to this node.
func (a *Agent) Pinged(now int64, from string, fromNode bool, token uint64) []Out {
	return []Out{{
		Kind:   OutAck,
		To:     from,
		ToNode: fromNode,
		Token:  token,
	}}
}

// ProbeRequested relays a probe: it emits a Ping to target.
func (a *Agent) ProbeRequested(now int64, from string, fromNode bool, target string, round uint64) []Out {
	tok := a.relay.Request(now, from, fromNode, target, round)
	return []Out{{
		Kind:   OutPing,
		To:     target,
		ToNode: true,
		Token:  tok,
	}}
}

// Acked completes a relayed probe: it emits a ProbeResult{OK:true} to the requester, or nothing.
func (a *Agent) Acked(now int64, from string, token uint64) []Out {
	res, ok := a.relay.Ack(now, from, token)
	if !ok {
		return nil
	}
	return []Out{{
		Kind:   OutProbeResult,
		To:     res.Requester,
		ToNode: res.RequesterNode,
		Target: res.Target,
		Round:  res.Round,
		OK:     true,
	}}
}
