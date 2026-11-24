// Package livesim provides a deterministic discrete-event simulator for node liveness.
package livesim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"hash"
	"maps"
	"slices"
	"strconv"

	"github.com/Kaikei-e/Nayutar/core/liveness"
	"github.com/Kaikei-e/Nayutar/core/liveness/agent"
	"github.com/Kaikei-e/Nayutar/core/liveness/detector"
	"github.com/Kaikei-e/Nayutar/core/liveness/hrw"
	"github.com/Kaikei-e/Nayutar/core/sim"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
)

const (
	defaultTickInterval int64 = 100
	defaultEpoch        int64 = 1_700_000_000_000
	defaultSkewMax      int64 = 500
	defaultPingTimeout  int64 = 800
	defaultPingInterval int64 = 100
	skewRedrawMinMS     int64 = 5_000
	skewRedrawMaxMS     int64 = 30_000
)

// NodeConfig configures a simulated node's identity and rack placement.
type NodeConfig struct {
	Name string
	Rack string
}

// Config configures a deterministic liveness simulation.
type Config struct {
	Seed         uint64
	Aggregators  []string
	Nodes        []NodeConfig
	Loss         float64
	DelayMin     int64
	DelayMax     int64
	TickInterval int64
	Epoch        int64
	SkewMax      int64
	Detector     *detector.Config
	Agent        *agent.Config
	PingTimeout  int64
	PingInterval int64
}

// DefaultConfig returns a standard 5-aggregator, 30-node, 3-rack simulation configuration.
func DefaultConfig(seed uint64) Config {
	aggs := []string{"agg-0", "agg-1", "agg-2", "agg-3", "agg-4"}
	nodes := make([]NodeConfig, 30)
	for i := range 30 {
		nodes[i] = NodeConfig{
			Name: fmt.Sprintf("node-%02d", i),
			Rack: fmt.Sprintf("rack-%d", i/10),
		}
	}
	return Config{
		Seed:         seed,
		Aggregators:  aggs,
		Nodes:        nodes,
		Loss:         0.05,
		DelayMin:     1,
		DelayMax:     50,
		TickInterval: defaultTickInterval,
		Epoch:        defaultEpoch,
		SkewMax:      defaultSkewMax,
		PingTimeout:  defaultPingTimeout,
		PingInterval: defaultPingInterval,
	}
}

// Write records one store write attempt executed on behalf of an aggregator.
type Write struct {
	Time       int64
	Aggregator string
	Kind       liveness.ProposalKind
	Node       string
	Outcome    liveness.Outcome
	Suspended  bool
	Wall       int64
}

// Stats summarizes round, probe, and suspension counters over a simulation run.
type Stats struct {
	RoundsOpened             map[string]int
	RoundsOpenedByAgg        map[string]int
	RoundsOpenedByAggNode    map[string]map[string]int
	NodeProbeResultsFailed   map[string]int
	UnownedNodeProbeRequests map[string]int
	SuspendedTime            map[string]int64
}

type link struct {
	from liveness.Addr
	to   liveness.Addr
}

type simAgg struct {
	idx               int32
	id                string
	cfg               liveness.AggregatorEngineConfig
	engine            *liveness.AggregatorEngine
	records           map[string]liveness.Record
	startTime         int64
	skew              int64
	incarnation       uint64
	procGen           uint64
	storeGen          uint64
	lastStoreDelivery int64
	stopped           bool
	isolated          bool
	storeCut          bool

	lastRoundSent        uint64
	roundsOpened         int
	roundsByNode         map[string]int
	owned                map[string]bool
	unownedNodeProbeReqs int
	wasSuspended         bool
	suspendedSince       int64
	suspendedTime        int64
}

type simNode struct {
	idx       int32
	name      string
	rack      string
	cfg       agent.Config
	engine    *liveness.NodeEngine
	startTime int64
	procGen   uint64
	stopped   bool
	isolated  bool
}

type eventKind uint8

const (
	evAggTick eventKind = iota + 1
	evNodeTick
	evDeliverMsg
	evStoreObserved
	evStoreSync
	evSkewRedraw
	evChaosStep
	evChaosHeal
)

type simEvent struct {
	time      int64
	seq       uint64
	kind      eventKind
	targetIdx int32
	aux       uint64
	msg       liveness.Message
	rec       *liveness.Record
}

func (e simEvent) less(other simEvent) bool {
	if e.time != other.time {
		return e.time < other.time
	}
	return e.seq < other.seq
}

type eventHeap []simEvent

func (h *eventHeap) push(ev simEvent) {
	*h = append(*h, ev)
	i := len(*h) - 1
	for i > 0 {
		p := (i - 1) / 2
		if !(*h)[i].less((*h)[p]) {
			break
		}
		(*h)[i], (*h)[p] = (*h)[p], (*h)[i]
		i = p
	}
}

func (h *eventHeap) pop() simEvent {
	s := *h
	top := s[0]
	last := len(s) - 1
	s[0] = s[last]
	s = s[:last]
	*h = s
	i := 0
	for {
		l := 2*i + 1
		if l >= len(s) {
			break
		}
		best := l
		if r := l + 1; r < len(s) && s[r].less(s[l]) {
			best = r
		}
		if !s[best].less(s[i]) {
			break
		}
		s[i], s[best] = s[best], s[i]
		i = best
	}
	return top
}

// Sim runs a deterministic discrete-event simulation of the node liveness protocol.
type Sim struct {
	cfg      Config
	rng      sim.Rand
	store    *memstore.Store
	registry *liveness.Registry

	aggIDs    []string
	nodeNames []string
	aggList   []*simAgg
	nodeList  []*simNode
	aggs      map[string]*simAgg
	nodes     map[string]*simNode

	cuts map[link]bool

	now       int64
	nextSeq   uint64
	queue     eventHeap
	writes    []Write
	grants    []int64
	traceHash hash.Hash
	traceBuf  []byte

	nextChaosID  uint64
	chaosConfigs map[uint64]ChaosConfig
	chaosFaults  map[uint64]chaosFault

	roundsOpened    map[string]int
	nodeProbeFailed map[string]int
}

func validateConfig(cfg Config) error {
	if len(cfg.Aggregators) == 0 {
		return errors.New("livesim: aggregators must be non-empty")
	}
	seenAgg := make(map[string]struct{}, len(cfg.Aggregators))
	for _, id := range cfg.Aggregators {
		if id == "" {
			return errors.New("livesim: aggregator ID must be non-empty")
		}
		if _, dup := seenAgg[id]; dup {
			return fmt.Errorf("livesim: duplicate aggregator %q", id)
		}
		seenAgg[id] = struct{}{}
	}
	if len(cfg.Nodes) == 0 {
		return errors.New("livesim: nodes must be non-empty")
	}
	seenNode := make(map[string]struct{}, len(cfg.Nodes))
	for _, n := range cfg.Nodes {
		if _, dup := seenNode[n.Name]; dup {
			return fmt.Errorf("livesim: duplicate node %q", n.Name)
		}
		seenNode[n.Name] = struct{}{}
	}
	if cfg.Loss < 0 || cfg.Loss > 1 {
		return errors.New("livesim: loss must be in [0, 1]")
	}
	if cfg.DelayMin < 0 || cfg.DelayMax < cfg.DelayMin {
		return errors.New("livesim: invalid delay bounds")
	}
	if cfg.TickInterval < 1 {
		return errors.New("livesim: tick interval must be >= 1")
	}
	if cfg.Epoch < 0 {
		return errors.New("livesim: epoch must be >= 0")
	}
	if cfg.SkewMax < 0 {
		return errors.New("livesim: skew max must be >= 0")
	}
	if cfg.PingTimeout < 1 || cfg.PingInterval < 1 {
		return errors.New("livesim: ping timeout and interval must be >= 1")
	}
	return nil
}

// New validates cfg, registers nodes in the store, and constructs a ready-to-run Sim.
func New(cfg Config) (*Sim, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	aggIDs := slices.Clone(cfg.Aggregators)
	slices.Sort(aggIDs)

	nodesSorted := slices.Clone(cfg.Nodes)
	slices.SortFunc(nodesSorted, func(a, b NodeConfig) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})

	nodeNames := make([]string, len(nodesSorted))
	for i, n := range nodesSorted {
		nodeNames[i] = n.Name
	}

	st := memstore.New()
	reg := liveness.NewRegistry(st)
	ctx := context.Background()

	initialRecords := make([]liveness.Record, len(nodesSorted))
	for i, n := range nodesSorted {
		rec, err := reg.Register(ctx, n.Name, n.Rack, cfg.Epoch)
		if err != nil {
			return nil, err
		}
		initialRecords[i] = rec
	}

	s := &Sim{
		cfg:             cfg,
		rng:             sim.NewDetRand(cfg.Seed),
		store:           st,
		registry:        reg,
		aggIDs:          aggIDs,
		nodeNames:       nodeNames,
		aggList:         make([]*simAgg, len(aggIDs)),
		nodeList:        make([]*simNode, len(nodesSorted)),
		aggs:            make(map[string]*simAgg, len(aggIDs)),
		nodes:           make(map[string]*simNode, len(nodesSorted)),
		cuts:            make(map[link]bool),
		traceHash:       sha256.New(),
		traceBuf:        make([]byte, 0, 128),
		chaosFaults:     make(map[uint64]chaosFault),
		roundsOpened:    make(map[string]int, len(nodesSorted)),
		nodeProbeFailed: make(map[string]int, len(nodesSorted)),
	}

	rankedFn := func(node string) []string {
		return hrw.Ranked(node, aggIDs)
	}

	for idx, id := range aggIDs {
		engCfg := liveness.DefaultAggregatorEngineConfig(id, aggIDs)
		engCfg.PingTimeout = cfg.PingTimeout
		engCfg.PingInterval = cfg.PingInterval
		if cfg.Detector != nil {
			engCfg.Detector = *cfg.Detector
		}
		const initIncarnation uint64 = 1
		engCfg.Detector.RoundBase = initIncarnation << 32
		eng, err := liveness.NewAggregatorEngine(engCfg)
		if err != nil {
			return nil, err
		}
		recs := make(map[string]liveness.Record, len(initialRecords))
		for _, rec := range initialRecords {
			recs[rec.Node] = rec
			eng.Observed(0, rec)
		}
		a := &simAgg{
			idx:          int32(idx),
			id:           id,
			cfg:          engCfg,
			engine:       eng,
			records:      recs,
			skew:         s.drawSkew(),
			incarnation:  initIncarnation,
			roundsByNode: make(map[string]int),
			owned:        make(map[string]bool),
		}
		s.aggList[idx] = a
		s.aggs[id] = a
		phase := s.rng.Int64N(cfg.TickInterval)
		s.schedule(phase, evAggTick, a.idx, a.procGen)
		if cfg.SkewMax > 0 {
			s.schedule(s.drawBetween(skewRedrawMinMS, skewRedrawMaxMS), evSkewRedraw, a.idx, 0)
		}
	}

	for idx, n := range nodesSorted {
		var agCfg agent.Config
		if cfg.Agent != nil {
			agCfg = *cfg.Agent
			agCfg.Node = n.Name
			agCfg.Ranked = rankedFn
		} else {
			agCfg = agent.DefaultConfig(n.Name, rankedFn)
			if cfg.Detector != nil {
				agCfg.Owners = cfg.Detector.Owners
			}
		}
		eng, err := liveness.NewNodeEngine(agCfg)
		if err != nil {
			return nil, err
		}
		sn := &simNode{
			idx:    int32(idx),
			name:   n.Name,
			rack:   n.Rack,
			cfg:    agCfg,
			engine: eng,
		}
		s.nodeList[idx] = sn
		s.nodes[n.Name] = sn
		phase := s.rng.Int64N(cfg.TickInterval)
		s.schedule(phase, evNodeTick, sn.idx, sn.procGen)
	}

	for _, n := range s.nodeList {
		for _, owner := range s.Owners(n.name) {
			s.aggs[owner].owned[n.name] = true
		}
	}

	return s, nil
}

func (s *Sim) schedule(at int64, kind eventKind, targetIdx int32, aux uint64) {
	s.nextSeq++
	s.queue.push(simEvent{
		time:      at,
		seq:       s.nextSeq,
		kind:      kind,
		targetIdx: targetIdx,
		aux:       aux,
	})
}

func (s *Sim) drawBetween(lo, hi int64) int64 {
	if hi <= lo {
		return lo
	}
	return lo + s.rng.Int64N(hi-lo+1)
}

func (s *Sim) drawDelay() int64 {
	return s.drawBetween(s.cfg.DelayMin, s.cfg.DelayMax)
}

func (s *Sim) drawSkew() int64 {
	if s.cfg.SkewMax == 0 {
		return 0
	}
	return s.rng.Int64N(2*s.cfg.SkewMax+1) - s.cfg.SkewMax
}

func (s *Sim) mustAgg(id string) *simAgg {
	a, ok := s.aggs[id]
	if !ok {
		panic(fmt.Sprintf("livesim: unknown aggregator %q", id))
	}
	return a
}

func (s *Sim) mustNode(name string) *simNode {
	n, ok := s.nodes[name]
	if !ok {
		panic(fmt.Sprintf("livesim: unknown node %q", name))
	}
	return n
}

func (s *Sim) mustAddr(addr liveness.Addr) {
	if addr.Node {
		s.mustNode(addr.ID)
	} else {
		s.mustAgg(addr.ID)
	}
}

func (s *Sim) endpointDownOrIsolated(addr liveness.Addr) bool {
	if addr.Node {
		n, ok := s.nodes[addr.ID]
		return !ok || n.stopped || n.isolated
	}
	a, ok := s.aggs[addr.ID]
	return !ok || a.stopped || a.isolated
}

func (s *Sim) canDeliver(from, to liveness.Addr) bool {
	if s.endpointDownOrIsolated(from) || s.endpointDownOrIsolated(to) {
		return false
	}
	if len(s.cuts) > 0 && s.cuts[link{from: from, to: to}] {
		return false
	}
	return true
}

func (s *Sim) sendMessages(msgs []liveness.Message) {
	for _, m := range msgs {
		if (m.Kind == liveness.MsgVoteRequest || m.Kind == liveness.MsgProbeRequest) && !m.From.Node {
			a := s.aggs[m.From.ID]
			if m.Round > a.lastRoundSent {
				a.lastRoundSent = m.Round
				a.roundsOpened++
				a.roundsByNode[m.Node]++
				s.roundsOpened[m.Node]++
			}
		}
		if m.Kind == liveness.MsgProbeRequest && !m.From.Node && m.To.Node {
			a := s.aggs[m.From.ID]
			if !a.owned[m.To.ID] {
				a.unownedNodeProbeReqs++
			}
		}
		if !s.canDeliver(m.From, m.To) {
			continue
		}
		if s.cfg.Loss > 0 && (s.cfg.Loss >= 1 || sim.Float64(s.rng) < s.cfg.Loss) {
			continue
		}
		s.nextSeq++
		s.queue.push(simEvent{
			time: s.now + s.drawDelay(),
			seq:  s.nextSeq,
			kind: evDeliverMsg,
			msg:  m,
		})
	}
}

func (s *Sim) traceFault(op string, a, b liveness.Addr) {
	buf := s.traceBuf[:0]
	buf = append(buf, 'F', '|')
	buf = strconv.AppendInt(buf, s.now, 10)
	buf = append(buf, '|')
	buf = append(buf, op...)
	buf = append(buf, '|')
	if a.Node {
		buf = append(buf, "n:"...)
	} else {
		buf = append(buf, "a:"...)
	}
	buf = append(buf, a.ID...)
	if b.ID != "" {
		buf = append(buf, '|')
		if b.Node {
			buf = append(buf, "n:"...)
		} else {
			buf = append(buf, "a:"...)
		}
		buf = append(buf, b.ID...)
	}
	buf = append(buf, '\n')
	s.traceHash.Write(buf)
	s.traceBuf = buf
}

func (s *Sim) traceWrite(w Write) {
	buf := s.traceBuf[:0]
	buf = append(buf, 'W', '|')
	buf = strconv.AppendInt(buf, w.Time, 10)
	buf = append(buf, '|')
	buf = append(buf, w.Aggregator...)
	buf = append(buf, '|')
	buf = strconv.AppendInt(buf, int64(w.Kind), 10)
	buf = append(buf, '|')
	buf = append(buf, w.Node...)
	buf = append(buf, '|')
	buf = strconv.AppendInt(buf, int64(w.Outcome), 10)
	buf = append(buf, '|')
	if w.Suspended {
		buf = append(buf, '1')
	} else {
		buf = append(buf, '0')
	}
	buf = append(buf, '|')
	buf = strconv.AppendInt(buf, w.Wall, 10)
	buf = append(buf, '\n')
	s.traceHash.Write(buf)
	s.traceBuf = buf
}

// StopNode stops a node so it stops ticking and answering messages.
func (s *Sim) StopNode(node string) {
	n := s.mustNode(node)
	if n.stopped {
		return
	}
	n.stopped = true
	n.procGen++
	s.traceFault("stop_node", liveness.Addr{ID: node, Node: true}, liveness.Addr{})
}

// StartNode restarts a stopped node with a fresh NodeEngine.
func (s *Sim) StartNode(node string) {
	n := s.mustNode(node)
	if !n.stopped {
		return
	}
	eng, err := liveness.NewNodeEngine(n.cfg)
	if err != nil {
		panic(err)
	}
	n.stopped = false
	n.startTime = s.now
	n.procGen++
	n.engine = eng
	phase := s.rng.Int64N(s.cfg.TickInterval)
	s.schedule(s.now+phase, evNodeTick, n.idx, n.procGen)
	s.traceFault("start_node", liveness.Addr{ID: node, Node: true}, liveness.Addr{})
}

func (s *Sim) syncAggSuspended(a *simAgg) {
	cur := !a.stopped && a.engine.Suspended()
	if cur && !a.wasSuspended {
		a.wasSuspended = true
		a.suspendedSince = s.now
	} else if !cur && a.wasSuspended {
		a.suspendedTime += s.now - a.suspendedSince
		a.wasSuspended = false
	}
}

// StopAggregator stops an aggregator and clears its soft state on restart.
func (s *Sim) StopAggregator(id string) {
	a := s.mustAgg(id)
	if a.stopped {
		return
	}
	a.stopped = true
	s.syncAggSuspended(a)
	a.procGen++
	a.storeGen++
	s.traceFault("stop_agg", liveness.Addr{ID: id}, liveness.Addr{})
}

// StartAggregator restarts a stopped aggregator with a fresh engine and current store view.
func (s *Sim) StartAggregator(id string) {
	a := s.mustAgg(id)
	if !a.stopped {
		return
	}
	a.incarnation++
	engCfg := a.cfg
	engCfg.Detector.RoundBase = a.incarnation << 32
	eng, err := liveness.NewAggregatorEngine(engCfg)
	if err != nil {
		panic(err)
	}
	a.stopped = false
	a.startTime = s.now
	a.procGen++
	a.storeGen++
	a.lastStoreDelivery = s.now
	a.lastRoundSent = 0
	a.engine = eng
	a.records = make(map[string]liveness.Record)
	if !a.storeCut {
		recs, _, err := s.registry.List(context.Background())
		if err != nil {
			panic(err)
		}
		for _, rec := range recs {
			a.records[rec.Node] = rec
			a.engine.Observed(0, rec)
		}
	}
	phase := s.rng.Int64N(s.cfg.TickInterval)
	s.schedule(s.now+phase, evAggTick, a.idx, a.procGen)
	s.traceFault("start_agg", liveness.Addr{ID: id}, liveness.Addr{})
}

// Isolate drops all network messages to and from addr until Heal is called.
func (s *Sim) Isolate(addr liveness.Addr) {
	s.mustAddr(addr)
	if addr.Node {
		n := s.nodes[addr.ID]
		if n.isolated {
			return
		}
		n.isolated = true
	} else {
		a := s.aggs[addr.ID]
		if a.isolated {
			return
		}
		a.isolated = true
	}
	s.traceFault("isolate", addr, liveness.Addr{})
}

// Heal restores network connectivity for an isolated endpoint.
func (s *Sim) Heal(addr liveness.Addr) {
	s.mustAddr(addr)
	if addr.Node {
		n := s.nodes[addr.ID]
		if !n.isolated {
			return
		}
		n.isolated = false
	} else {
		a := s.aggs[addr.ID]
		if !a.isolated {
			return
		}
		a.isolated = false
	}
	s.traceFault("heal", addr, liveness.Addr{})
}

// Cut drops all messages sent from a to b until Uncut is called.
func (s *Sim) Cut(a, b liveness.Addr) {
	s.mustAddr(a)
	s.mustAddr(b)
	l := link{from: a, to: b}
	if s.cuts[l] {
		return
	}
	s.cuts[l] = true
	s.traceFault("cut", a, b)
}

// Uncut restores message delivery from a to b.
func (s *Sim) Uncut(a, b liveness.Addr) {
	s.mustAddr(a)
	s.mustAddr(b)
	l := link{from: a, to: b}
	if !s.cuts[l] {
		return
	}
	delete(s.cuts, l)
	s.traceFault("uncut", a, b)
}

// CutStore disconnects an aggregator from reading, writing, or watching the store.
func (s *Sim) CutStore(aggregator string) {
	a := s.mustAgg(aggregator)
	if a.storeCut {
		return
	}
	a.storeCut = true
	a.storeGen++
	s.traceFault("cut_store", liveness.Addr{ID: aggregator}, liveness.Addr{})
}

// HealStore reconnects an aggregator to the store and delivers a full view sync after network delay.
func (s *Sim) HealStore(aggregator string) {
	a := s.mustAgg(aggregator)
	if !a.storeCut {
		return
	}
	a.storeCut = false
	a.storeGen++
	if !a.stopped {
		deliverAt := max(a.lastStoreDelivery, s.now+s.drawDelay())
		a.lastStoreDelivery = deliverAt
		s.schedule(deliverAt, evStoreSync, a.idx, a.storeGen)
	}
	s.traceFault("heal_store", liveness.Addr{ID: aggregator}, liveness.Addr{})
}

// RunUntil advances the simulation clock to t, processing all events with time <= t.
func (s *Sim) RunUntil(t int64) {
	if t <= s.now {
		return
	}
	for len(s.queue) > 0 && s.queue[0].time <= t {
		ev := s.queue.pop()
		s.now = ev.time
		s.dispatch(ev)
	}
	s.now = t
}

func (s *Sim) dispatch(ev simEvent) {
	switch ev.kind {
	case evAggTick:
		a := s.aggList[ev.targetIdx]
		if a.stopped || a.procGen != ev.aux {
			return
		}
		mono := s.now - a.startTime
		msgs, props := a.engine.Tick(mono)
		s.syncAggSuspended(a)
		s.sendMessages(msgs)
		s.executeProposals(a, props)
		s.schedule(s.now+s.cfg.TickInterval, evAggTick, a.idx, a.procGen)

	case evNodeTick:
		n := s.nodeList[ev.targetIdx]
		if n.stopped || n.procGen != ev.aux {
			return
		}
		mono := s.now - n.startTime
		msgs := n.engine.Tick(mono)
		s.sendMessages(msgs)
		s.schedule(s.now+s.cfg.TickInterval, evNodeTick, n.idx, n.procGen)

	case evDeliverMsg:
		m := ev.msg
		if !s.canDeliver(m.From, m.To) {
			return
		}
		if m.Kind == liveness.MsgProbeResult && !m.OK && m.From.Node {
			s.nodeProbeFailed[m.Node]++
		}
		if m.To.Node {
			n := s.nodes[m.To.ID]
			mono := s.now - n.startTime
			s.sendMessages(n.engine.Handle(mono, m))
		} else {
			a := s.aggs[m.To.ID]
			mono := s.now - a.startTime
			s.sendMessages(a.engine.Handle(mono, m))
		}

	case evStoreObserved:
		a := s.aggList[ev.targetIdx]
		if a.stopped || a.storeCut || a.storeGen != ev.aux {
			return
		}
		rec := *ev.rec
		a.records[rec.Node] = rec
		a.engine.Observed(s.now-a.startTime, rec)

	case evStoreSync:
		a := s.aggList[ev.targetIdx]
		if a.stopped || a.storeCut || a.storeGen != ev.aux {
			return
		}
		recs, _, err := s.registry.List(context.Background())
		if err != nil {
			panic(err)
		}
		mono := s.now - a.startTime
		seen := make(map[string]struct{}, len(recs))
		for _, rec := range recs {
			seen[rec.Node] = struct{}{}
			a.records[rec.Node] = rec
			a.engine.Observed(mono, rec)
		}
		var vanished []string
		for name := range a.records {
			if _, ok := seen[name]; !ok {
				vanished = append(vanished, name)
			}
		}
		slices.Sort(vanished)
		for _, name := range vanished {
			delete(a.records, name)
			a.engine.Forgotten(mono, name)
		}

	case evSkewRedraw:
		a := s.aggList[ev.targetIdx]
		a.skew = s.drawSkew()
		s.schedule(s.now+s.drawBetween(skewRedrawMinMS, skewRedrawMaxMS), evSkewRedraw, a.idx, 0)

	case evChaosStep:
		s.handleChaosStep(ev.aux)

	case evChaosHeal:
		s.handleChaosHeal(ev.aux)
	}
}

type budgetDoc struct {
	Lkal    string  `json:"$lkal"`
	Granted []int64 `json:"granted"`
}

func (s *Sim) lastBudgetStamp(ctx context.Context) int64 {
	kv, _, err := s.store.Get(ctx, keyspace.EvictionBudgetKey, 0)
	if err != nil {
		panic(err)
	}
	var doc budgetDoc
	if err := json.Unmarshal(kv.Value, &doc); err != nil {
		panic(err)
	}
	if len(doc.Granted) == 0 {
		panic("livesim: committed eviction grant produced empty budget")
	}
	return doc.Granted[len(doc.Granted)-1]
}

func (s *Sim) executeProposals(a *simAgg, props []liveness.Proposal) {
	if len(props) == 0 || a.storeCut {
		return
	}
	ctx := context.Background()
	wall := s.cfg.Epoch + s.now + a.skew
	totalNodes := len(a.records)
	suspended := a.engine.Suspended()

	for _, p := range props {
		rec, ok := a.records[p.Node]
		if !ok || rec.ModRev != p.ModRev {
			continue
		}
		var outcome liveness.Outcome
		var err error
		switch p.Kind {
		case liveness.ProposeReady:
			outcome, err = s.registry.SetReady(ctx, rec, wall)
		case liveness.ProposeNotReady:
			outcome, err = s.registry.SetNotReady(ctx, rec, wall)
		case liveness.ProposeEvictable:
			outcome, err = s.registry.GrantEviction(ctx, rec, wall, totalNodes)
		default:
			continue
		}
		if err != nil {
			panic(err)
		}
		recordedWall := wall
		if p.Kind == liveness.ProposeEvictable && outcome == liveness.Committed {
			recordedWall = s.lastBudgetStamp(ctx)
			s.grants = append(s.grants, recordedWall)
		}
		w := Write{
			Time:       s.now,
			Aggregator: a.id,
			Kind:       p.Kind,
			Node:       p.Node,
			Outcome:    outcome,
			Suspended:  suspended,
			Wall:       recordedWall,
		}
		s.writes = append(s.writes, w)
		s.traceWrite(w)

		if outcome != liveness.Committed {
			continue
		}
		updated, err := s.registry.Get(ctx, p.Node)
		if err != nil {
			panic(err)
		}
		for _, other := range s.aggList {
			if other.stopped || other.storeCut {
				continue
			}
			// Preserve watch revision order per aggregator across close commits.
			deliverAt := max(other.lastStoreDelivery, s.now+s.drawDelay())
			other.lastStoreDelivery = deliverAt
			recCopy := updated
			s.nextSeq++
			s.queue.push(simEvent{
				time:      deliverAt,
				seq:       s.nextSeq,
				kind:      evStoreObserved,
				targetIdx: other.idx,
				aux:       other.storeGen,
				rec:       &recCopy,
			})
		}
	}
}

// Now returns the current simulation time in milliseconds.
func (s *Sim) Now() int64 {
	return s.now
}

// Owners returns the ordered owner aggregator IDs for node under the cluster's HRW ownership.
func (s *Sim) Owners(node string) []string {
	s.mustNode(node)
	owners := s.aggList[0].cfg.Detector.Owners
	ranked := hrw.Ranked(node, s.aggIDs)
	return ranked[:min(owners, len(ranked))]
}

// Record returns the current store record for node, or the zero Record if absent.
func (s *Sim) Record(node string) liveness.Record {
	rec, err := s.registry.Get(context.Background(), node)
	if err != nil {
		return liveness.Record{}
	}
	return rec
}

// Suspended reports whether aggregator id is currently running and in self-suspension.
func (s *Sim) Suspended(id string) bool {
	a, ok := s.aggs[id]
	if !ok || a.stopped {
		return false
	}
	return a.engine.Suspended()
}

// Writes returns a copy of all store write attempts recorded so far.
func (s *Sim) Writes() []Write {
	return slices.Clone(s.writes)
}

// Grants returns a copy of the wall timestamps recorded by committed eviction permits.
func (s *Sim) Grants() []int64 {
	return slices.Clone(s.grants)
}

// Stats returns a snapshot of the simulation's round, probe, and suspension counters.
func (s *Sim) Stats() Stats {
	byAgg := make(map[string]int, len(s.aggList))
	byAggNode := make(map[string]map[string]int, len(s.aggList))
	unownedProbeReqs := make(map[string]int, len(s.aggList))
	suspTime := make(map[string]int64, len(s.aggList))
	for _, a := range s.aggList {
		byAgg[a.id] = a.roundsOpened
		byAggNode[a.id] = maps.Clone(a.roundsByNode)
		unownedProbeReqs[a.id] = a.unownedNodeProbeReqs
		total := a.suspendedTime
		if a.wasSuspended {
			total += s.now - a.suspendedSince
		}
		suspTime[a.id] = total
	}
	return Stats{
		RoundsOpened:             maps.Clone(s.roundsOpened),
		RoundsOpenedByAgg:        byAgg,
		RoundsOpenedByAggNode:    byAggNode,
		NodeProbeResultsFailed:   maps.Clone(s.nodeProbeFailed),
		UnownedNodeProbeRequests: unownedProbeReqs,
		SuspendedTime:            suspTime,
	}
}

// TraceHash returns the SHA-256 hex digest over the canonical simulation trace.
func (s *Sim) TraceHash() string {
	return hex.EncodeToString(s.traceHash.Sum(nil))
}
