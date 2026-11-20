package liveness_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/Nayutar/core/liveness"
	"github.com/Kaikei-e/Nayutar/core/liveness/agent"
)

func TestNodeConfigValidation(t *testing.T) {
	aggs := []string{"agg-a"}
	hub := newMemHub(aggs, []string{"n1"})
	ep := hub.endpoints[liveness.Addr{ID: "n1", Node: true}]

	if _, err := liveness.NewNode(liveness.NodeConfig{
		Agent:       agent.DefaultConfig("n1", nil),
		Aggregators: aggs,
	}); err == nil {
		t.Fatal("NewNode(nil Transport) = nil, want error")
	}
	if _, err := liveness.NewNode(liveness.NodeConfig{
		Agent:        agent.DefaultConfig("n1", nil),
		Aggregators:  aggs,
		Transport:    ep,
		TickInterval: -time.Millisecond,
	}); err == nil {
		t.Fatal("NewNode(negative TickInterval) = nil, want error")
	}
	if _, err := liveness.NewNode(liveness.NodeConfig{
		Agent:       agent.Config{},
		Aggregators: aggs,
		Transport:   ep,
	}); err == nil {
		t.Fatal("NewNode(invalid Agent) = nil, want error")
	}
}

func TestNodeRunHeartbeatsPingsAndRelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		aggs := []string{"agg-a", "agg-b"}
		nodes := []string{"n1", "n2"}
		hub := newMemHub(aggs, nodes)
		n1Ep := hub.endpoints[liveness.Addr{ID: "n1", Node: true}]
		aggAEp := hub.endpoints[liveness.Addr{ID: "agg-a"}]
		aggBEp := hub.endpoints[liveness.Addr{ID: "agg-b"}]

		n1, err := liveness.NewNode(liveness.NodeConfig{
			Agent:       agent.DefaultConfig("n1", nil),
			Aggregators: aggs,
			Transport:   n1Ep,
		})
		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- n1.Run(ctx)
		}()

		// Wait past the initial 100ms tick so heartbeats to both owners are emitted.
		time.Sleep(150 * time.Millisecond)
		synctest.Wait()

		var gotA, gotB bool
		for len(aggAEp.ch) > 0 {
			m := <-aggAEp.ch
			if m.Kind == liveness.MsgHeartbeat && m.Node == "n1" {
				gotA = true
			}
		}
		for len(aggBEp.ch) > 0 {
			m := <-aggBEp.ch
			if m.Kind == liveness.MsgHeartbeat && m.Node == "n1" {
				gotB = true
			}
		}
		if !gotA || !gotB {
			t.Fatalf("expected initial heartbeats to both owners: agg-a=%v agg-b=%v", gotA, gotB)
		}

		aggAEp.Send(liveness.Message{
			Kind:  liveness.MsgPing,
			From:  liveness.Addr{ID: "agg-a"},
			To:    liveness.Addr{ID: "n1", Node: true},
			Token: 42,
		})
		synctest.Wait()
		var gotAck bool
		for len(aggAEp.ch) > 0 {
			m := <-aggAEp.ch
			if m.Kind == liveness.MsgAck && m.From == (liveness.Addr{ID: "n1", Node: true}) && m.Token == 42 {
				gotAck = true
			}
		}
		if !gotAck {
			t.Fatal("expected MsgAck from n1")
		}

		aggBEp.Send(liveness.Message{
			Kind:  liveness.MsgProbeRequest,
			From:  liveness.Addr{ID: "agg-b"},
			To:    liveness.Addr{ID: "n1", Node: true},
			Node:  "n2",
			Round: 7,
		})
		synctest.Wait()
		n2Ep := hub.endpoints[liveness.Addr{ID: "n2", Node: true}]
		var pingTok uint64
		for len(n2Ep.ch) > 0 {
			m := <-n2Ep.ch
			if m.Kind == liveness.MsgPing && m.To == (liveness.Addr{ID: "n2", Node: true}) {
				pingTok = m.Token
			}
		}
		if pingTok == 0 {
			t.Fatal("expected relayed MsgPing from n1 to n2")
		}
		n2Ep.Send(liveness.Message{
			Kind:  liveness.MsgAck,
			From:  liveness.Addr{ID: "n2", Node: true},
			To:    liveness.Addr{ID: "n1", Node: true},
			Token: pingTok,
		})
		synctest.Wait()
		var gotProbeOK bool
		for len(aggBEp.ch) > 0 {
			m := <-aggBEp.ch
			if m.Kind == liveness.MsgProbeResult && m.Node == "n2" && m.Round == 7 && m.OK {
				gotProbeOK = true
			}
		}
		if !gotProbeOK {
			t.Fatal("expected MsgProbeResult{OK: true} from n1 to agg-b")
		}

		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("Node.Run = %v, want context.Canceled", err)
		}
	})
}

func TestNodeRunSingleUse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		aggs := []string{"agg-a"}
		hub := newMemHub(aggs, []string{"n1"})
		n, err := liveness.NewNode(liveness.NodeConfig{
			Agent:       agent.DefaultConfig("n1", nil),
			Aggregators: aggs,
			Transport:   hub.endpoints[liveness.Addr{ID: "n1", Node: true}],
		})
		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- n.Run(ctx)
		}()
		synctest.Wait()

		if err := n.Run(context.Background()); !errors.Is(err, liveness.ErrAlreadyRun) {
			t.Fatalf("concurrent second Node.Run = %v, want ErrAlreadyRun", err)
		}

		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("first Node.Run = %v, want context.Canceled", err)
		}
		if err := n.Run(context.Background()); !errors.Is(err, liveness.ErrAlreadyRun) {
			t.Fatalf("sequential second Node.Run = %v, want ErrAlreadyRun", err)
		}
	})
}
