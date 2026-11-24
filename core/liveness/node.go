package liveness

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"time"

	"github.com/Kaikei-e/Nayutar/core/liveness/agent"
	"github.com/Kaikei-e/Nayutar/core/liveness/hrw"
	"github.com/Kaikei-e/Nayutar/core/sim"
)

// NodeConfig configures a Node runner.
type NodeConfig struct {
	Agent        agent.Config // Ranked is filled in from Aggregators
	Aggregators  []string
	Transport    Transport
	Clock        Clock
	TickInterval time.Duration // 0 → 100ms
}

// Node drives a NodeEngine against a message transport.
type Node struct {
	engine       *NodeEngine
	transport    Transport
	clock        Clock
	tickInterval time.Duration
	started      atomic.Bool
}

// NewNode validates cfg and constructs a Node runner.
func NewNode(cfg NodeConfig) (*Node, error) {
	if cfg.Transport == nil {
		return nil, errors.New("liveness: transport must be non-nil")
	}
	if cfg.TickInterval < 0 {
		return nil, errors.New("liveness: tick interval must be >= 0")
	}
	tickInterval := cfg.TickInterval
	if tickInterval == 0 {
		tickInterval = defaultTickInterval
	}
	clk := cfg.Clock
	if clk == nil {
		clk = sim.RealClock()
	}
	aggs := slices.Clone(cfg.Aggregators)
	agentCfg := cfg.Agent
	agentCfg.Ranked = func(node string) []string {
		return hrw.Ranked(node, aggs)
	}
	eng, err := NewNodeEngine(agentCfg)
	if err != nil {
		return nil, err
	}
	return &Node{
		engine:       eng,
		transport:    cfg.Transport,
		clock:        clk,
		tickInterval: tickInterval,
	}, nil
}

// Run blocks until ctx is done and returns ctx.Err().
// Run is single-use: a second call returns ErrAlreadyRun immediately.
func (n *Node) Run(ctx context.Context) error {
	if !n.started.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}
	start := n.clock.Now()
	mono := func(t time.Time) int64 {
		return max(0, t.Sub(start).Milliseconds())
	}

	recv := n.transport.Recv()
	tickCh := n.clock.After(n.tickInterval)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case m := <-recv:
			for _, out := range n.engine.Handle(mono(n.clock.Now()), m) {
				n.transport.Send(out)
			}
		case <-tickCh:
			for _, out := range n.engine.Tick(mono(n.clock.Now())) {
				n.transport.Send(out)
			}
			tickCh = n.clock.After(n.tickInterval)
		}
	}
}
