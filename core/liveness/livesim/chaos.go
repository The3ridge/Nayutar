package livesim

import (
	"github.com/Kaikei-e/Nayutar/core/liveness"
)

const (
	defaultChaosMinInterval int64 = 1_000
	defaultChaosMaxInterval int64 = 4_000
	defaultChaosMinDuration int64 = 2_000
	defaultChaosMaxDuration int64 = 15_000
)

// ChaosConfig configures randomized fault injection on aggregators.
type ChaosConfig struct {
	MaxFaultyAggregators int
	MinInterval          int64
	MaxInterval          int64
	MinDuration          int64
	MaxDuration          int64
	Until                int64
}

type chaosFaultKind uint8

const (
	chaosStop chaosFaultKind = iota + 1
	chaosIsolate
	chaosStoreCut
	chaosNodeCuts
)

type chaosFault struct {
	kind  chaosFaultKind
	agg   string
	links []link
}

// ScheduleChaos starts a seeded random fault-injection loop for aggregators.
func (s *Sim) ScheduleChaos(cfg ChaosConfig) {
	if cfg.MaxFaultyAggregators <= 0 {
		return
	}
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = defaultChaosMinInterval
	}
	if cfg.MaxInterval < cfg.MinInterval {
		cfg.MaxInterval = max(cfg.MinInterval, defaultChaosMaxInterval)
	}
	if cfg.MinDuration <= 0 {
		cfg.MinDuration = defaultChaosMinDuration
	}
	if cfg.MaxDuration < cfg.MinDuration {
		cfg.MaxDuration = max(cfg.MinDuration, defaultChaosMaxDuration)
	}
	if s.chaosConfigs == nil {
		s.chaosConfigs = make(map[uint64]ChaosConfig)
	}
	s.nextChaosID++
	id := s.nextChaosID
	s.chaosConfigs[id] = cfg

	firstAt := s.now + s.drawBetween(0, cfg.MinInterval)
	if cfg.Until > 0 && firstAt >= cfg.Until {
		return
	}
	s.nextSeq++
	s.queue.push(simEvent{
		time: firstAt,
		seq:  s.nextSeq,
		kind: evChaosStep,
		aux:  id,
	})
}

func (s *Sim) isAggFaulty(id string) bool {
	a := s.aggs[id]
	if a.stopped || a.isolated || a.storeCut {
		return true
	}
	for l := range s.cuts {
		if (!l.from.Node && l.from.ID == id) || (!l.to.Node && l.to.ID == id) {
			return true
		}
	}
	return false
}

func (s *Sim) handleChaosStep(streamID uint64) {
	cfg, ok := s.chaosConfigs[streamID]
	if !ok {
		return
	}
	if cfg.Until > 0 && s.now >= cfg.Until {
		return
	}

	faultyCount := 0
	var healthy []string
	for _, id := range s.aggIDs {
		if s.isAggFaulty(id) {
			faultyCount++
		} else {
			healthy = append(healthy, id)
		}
	}

	if faultyCount < cfg.MaxFaultyAggregators && len(healthy) > 0 {
		agg := healthy[s.rng.IntN(len(healthy))]
		kind := chaosFaultKind(1 + s.rng.IntN(4))
		fault := chaosFault{kind: kind, agg: agg}

		switch kind {
		case chaosStop:
			s.StopAggregator(agg)
		case chaosIsolate:
			s.Isolate(liveness.Addr{ID: agg})
		case chaosStoreCut:
			s.CutStore(agg)
		case chaosNodeCuts:
			k := 1 + s.rng.IntN(len(s.nodeNames))
			perm := s.rng.Perm(len(s.nodeNames))
			twoWay := s.rng.IntN(2) == 0
			aggToNode := s.rng.IntN(2) == 0
			aggAddr := liveness.Addr{ID: agg}
			for _, idx := range perm[:k] {
				nodeAddr := liveness.Addr{ID: s.nodeNames[idx], Node: true}
				if twoWay || aggToNode {
					s.Cut(aggAddr, nodeAddr)
					fault.links = append(fault.links, link{from: aggAddr, to: nodeAddr})
				}
				if twoWay || !aggToNode {
					s.Cut(nodeAddr, aggAddr)
					fault.links = append(fault.links, link{from: nodeAddr, to: aggAddr})
				}
			}
		}

		s.nextChaosID++
		faultID := s.nextChaosID
		s.chaosFaults[faultID] = fault
		dur := s.drawBetween(cfg.MinDuration, cfg.MaxDuration)
		s.nextSeq++
		s.queue.push(simEvent{
			time: s.now + dur,
			seq:  s.nextSeq,
			kind: evChaosHeal,
			aux:  faultID,
		})
	}

	nextAt := s.now + s.drawBetween(cfg.MinInterval, cfg.MaxInterval)
	if cfg.Until > 0 && nextAt >= cfg.Until {
		return
	}
	s.nextSeq++
	s.queue.push(simEvent{
		time: nextAt,
		seq:  s.nextSeq,
		kind: evChaosStep,
		aux:  streamID,
	})
}

func (s *Sim) handleChaosHeal(faultID uint64) {
	fault, ok := s.chaosFaults[faultID]
	if !ok {
		return
	}
	delete(s.chaosFaults, faultID)

	switch fault.kind {
	case chaosStop:
		s.StartAggregator(fault.agg)
	case chaosIsolate:
		s.Heal(liveness.Addr{ID: fault.agg})
	case chaosStoreCut:
		s.HealStore(fault.agg)
	case chaosNodeCuts:
		for _, l := range fault.links {
			s.Uncut(l.from, l.to)
		}
	}
}
