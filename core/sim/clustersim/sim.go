// Package clustersim runs a single-seed deterministic multi-component cluster simulation
// coupling memstore, tenant isolation, watchcache, resource ownership/refs/orderedset/patch,
// node liveness detection/eviction budgets, and node container lifecycle state machines.
package clustersim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/liveness"
	"github.com/Kaikei-e/Nayutar/core/liveness/agent"
	"github.com/Kaikei-e/Nayutar/core/liveness/budget"
	"github.com/Kaikei-e/Nayutar/core/liveness/detector"
	"github.com/Kaikei-e/Nayutar/core/liveness/hrw"
	"github.com/Kaikei-e/Nayutar/core/node/lifecycle"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/ownership"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/sim"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache"
)

// Config parameterizes a deterministic multi-component cluster simulation.
type Config struct {
	Seed            uint64
	Tenants         []string
	Aggregators     []string
	Nodes           []string
	TickIntervalMs  int64
	WorkloadStepMs  int64
	FaultIntervalMs int64
	MinDelayMs      int64
	MaxDelayMs      int64
	DropPermille    int
	BuggifyPermille int
	EvictionGraceMs int64
	// LongFaultPermille is the chance that an injected node stop or isolation lasts long
	// enough for liveness to mark the node NotReady and possibly evict it.
	LongFaultPermille int
	// Mutations injects deliberate misbehaviour into node control so tests can prove the
	// invariants have teeth. The zero value changes nothing.
	Mutations Mutations
	// Settle blocks until every goroutine inside the watch caches and their consumers is
	// idle. Inside a testing/synctest bubble it is synctest.Wait.
	Settle func()
}

// Mutations are test-only hooks that make the simulated node control misbehave; every
// field's zero value leaves the simulation untouched.
type Mutations struct {
	// DropCacheEventsForNode discards every watch-cache event delivered to the named node.
	DropCacheEventsForNode string
	// SkipStopOnEviction leaves units running when their node is evicted.
	SkipStopOnEviction bool
	// SkipStopOnNodeStop leaves units running when their node stops.
	SkipStopOnNodeStop bool
	// StopOnlyFirstUnitOnFault stops a single unit when a fault takes a node down.
	StopOnlyFirstUnitOnFault bool
	// IgnoreDesired starts units without looking at the desired state learned from the cache.
	IgnoreDesired bool
	// IgnoreUpdates makes node control keep the first value it learned of each workload.
	IgnoreUpdates bool
	// IgnoreDeleteEvents makes node control disregard watch-cache delete events.
	IgnoreDeleteEvents bool
	// SkipHealFaults leaves every injected fault in place when convergence is verified.
	SkipHealFaults bool
	// SkipResumeUpstream leaves a stalled cache upstream stalled when faults are healed.
	SkipResumeUpstream bool
	// DiscardEventsWhileDown makes a stopped or isolated node discard the watch-cache
	// events it receives instead of learning them on recovery.
	DiscardEventsWhileDown bool
	// SkipStartUnitsOnRestart leaves units idle when a stopped node starts again.
	SkipStartUnitsOnRestart bool
	// NeverReviveStoppedUnits keeps a stopped unit stopped for good.
	NeverReviveStoppedUnits bool
	// StaleCurrentRev makes the store revision the watch caches read fall back to the first
	// revision while the upstream is stalled, so a consistent read is answered from a stale window.
	StaleCurrentRev bool
	// StaleStoreFallback makes the store read a cache falls back to during a stall return
	// the state as of the moment the stall began.
	StaleStoreFallback bool
	// RejectEveryOwnerRev rejects every ownership operation that carries an owner
	// revision as if the revision were stale.
	RejectEveryOwnerRev bool
}

// DefaultConfig returns a balanced multi-tenant, multi-node cluster simulation config.
func DefaultConfig(seed uint64) Config {
	return Config{
		Seed:            seed,
		Tenants:         []string{"alpha", "beta"},
		Aggregators:     []string{"agg-0", "agg-1", "agg-2"},
		Nodes:           []string{"node-0", "node-1", "node-2", "node-3"},
		TickIntervalMs:  100,
		WorkloadStepMs:  250,
		FaultIntervalMs: 1_500,
		MinDelayMs:      2,
		MaxDelayMs:      40,
		DropPermille:    40,
		BuggifyPermille: 120,
		EvictionGraceMs: 2_000,

		LongFaultPermille: 400,
	}
}

// Stats captures deterministic summary metrics from a simulation run.
type Stats struct {
	ApplySucceeded     int
	ApplyConflicts     int
	PruneOps           int
	ReleaseAdoptOps    int
	PatchOps           int
	PatchStatusOps     int
	LivenessReady      int
	LivenessNotReady   int
	LivenessEvictions  int
	UnitsStarted       int
	UnitsStopped       int
	CacheVerifications int
	CachePauses        int
	// WatchesBehind counts watch verifications that found a watch lagging the store history.
	WatchesBehind int
	// CacheVerificationsPaused counts cache verifications that ran while the cache upstream
	// was paused, so the consistent read had to fall back to the store.
	CacheVerificationsPaused int
	PartitionsInjected       int
	NodeStopsInjected        int

	DeleteOps            int
	StaleTokenOps        int
	PatchRejected        int
	PruneConflicts       int
	OrderedSetRejections int
	CacheEventsConsumed  int
	UnitsStoppedByFault  int
	UnitsRevived         int
}

type simTenant struct {
	name      string
	objects   *resource.Objects
	cache     *watchcache.Cache
	secCache  *watchcache.Cache
	workloads []string
	maxOwnRev map[string]store.Rev
	prefix    string
	ref       *refLog
}

// refLog is an independent observer of the store's own history for a tenant's workloads.
type refLog struct {
	mu     sync.Mutex
	events []store.Event
}

func (r *refLog) drain(w store.WatchStream) {
	for ev := range w.Events() {
		if ev.Type == store.EventProgress {
			continue
		}
		r.mu.Lock()
		r.events = append(r.events, ev)
		r.mu.Unlock()
	}
}

func (r *refLog) snapshot() []store.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// pausableStore delays every watch event of the store it wraps while paused, which models
// an upstream stall under the watch caches.
type pausableStore struct {
	store.Store
	mu        sync.Mutex
	gate      chan struct{}
	frozenRev store.Rev
	staleRev  bool
	staleRead bool
}

func (p *pausableStore) pause() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gate != nil {
		return false
	}
	p.gate = make(chan struct{})
	p.frozenRev, _ = p.Store.CurrentRev(context.Background())
	return true
}

func (p *pausableStore) frozen() (store.Rev, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.frozenRev, p.gate != nil
}

func (p *pausableStore) CurrentRev(ctx context.Context) (store.Rev, error) {
	if _, paused := p.frozen(); paused && p.staleRev {
		return 1, nil
	}
	return p.Store.CurrentRev(ctx)
}

func (p *pausableStore) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	if frozen, paused := p.frozen(); paused && p.staleRead && atRev == 0 {
		atRev = frozen
	}
	return p.Store.Range(ctx, prefix, atRev, limit, cont)
}

func (p *pausableStore) resume() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gate != nil {
		close(p.gate)
		p.gate = nil
	}
}

func (p *pausableStore) paused() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gate != nil
}

func (p *pausableStore) current() chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.gate
}

type gatedWatch struct {
	store.WatchStream
	ctx context.Context
	out chan store.Event
}

func (g *gatedWatch) Events() <-chan store.Event { return g.out }

func (g *gatedWatch) Err() error {
	if err := g.WatchStream.Err(); err != nil {
		return err
	}
	return g.ctx.Err()
}

func (p *pausableStore) Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
	inner, err := p.Store.Watch(ctx, prefix, fromRev)
	if err != nil {
		return nil, err
	}
	g := &gatedWatch{WatchStream: inner, ctx: ctx, out: make(chan store.Event)}
	go func() {
		defer close(g.out)
		for {
			select {
			case ev, ok := <-inner.Events():
				if !ok {
					return
				}
				if gate := p.current(); gate != nil {
					select {
					case <-gate:
					case <-ctx.Done():
						return
					}
				}
				select {
				case g.out <- ev:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return g, nil
}

type nodeWatch struct {
	tenant  string
	watch   *watchcache.Watch
	mu      sync.Mutex
	inbox   []watchcache.Event
	pending []watchcache.Event
	all     []watchcache.Event
	err     error
	done    bool
}

type simAgg struct {
	id          string
	engine      *liveness.AggregatorEngine
	records     map[string]liveness.Record
	isolated    bool
	storeCut    bool
	incarnation uint64
}

type simUnit struct {
	id         string
	node       string
	tenant     string
	workload   string
	spec       lifecycle.Spec
	state      lifecycle.State
	inFlight   bool
	generation uint64
	faultStop  bool
}

type simNode struct {
	name     string
	rack     string
	cfg      agent.Config
	engine   *liveness.NodeEngine
	stopped  bool
	isolated bool
	evicted  bool
	units    []*simUnit
	watches  []*nodeWatch
	desired  map[string]desiredEntry
}

// desiredEntry is the last revision and value of a workload a node learned from the cache.
type desiredEntry struct {
	rev   store.Rev
	value []byte
}

func (n *simNode) down() bool { return n.stopped || n.evicted }

// Sim couples all core subsystems on a single sim.Queue and single root PRNG seed.
type Sim struct {
	cfg       Config
	q         *sim.Queue
	store     *memstore.Store
	tenReg    *tenant.Registry
	cacheClk  *sim.FakeClock
	cacheMs   int64
	cancel    context.CancelFunc
	tearDown  bool
	granted   map[string]bool
	caches    *watchcache.Manager
	liveReg   *liveness.Registry
	schemas   resource.Schemas
	tenants   []*simTenant
	aggs      []*simAgg
	aggByID   map[string]*simAgg
	nodes     []*simNode
	nodeByID  map[string]*simNode
	allUnits  []*simUnit
	cuts      map[[2]string]bool
	grants    []int64
	stats     Stats
	closed    bool
	invErrors []error
	reported  map[string]bool
	upstream  *pausableStore
	faultsOff bool
	quiet     bool
}

const (
	stepOrderSpan = 6
	cacheReadWait = 250 * time.Millisecond
	cacheClockMs  = 10
)

var (
	actorAlice = refs.Principal{Type: "User", ID: "alice"}
	actorBob   = refs.Principal{Type: "User", ID: "bob"}
	actorHPA   = refs.Principal{Type: "ServiceAccount", ID: "hpa"}
)

func compileClusterSchemas() (resource.Schemas, error) {
	workloadSchema, err := resource.CompileKindSchema("workload.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"

root {
  field "spec" {
    type = .record
    field "replicas" {
      type = .int
      default = 1
    }
    field "payload" {
      type = .string
    }
    field "steps" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        field "order" {
          type = .int
          required = true
        }
        field "cmd" {
          type = .string
          required = true
        }
      }
    }
    field "secretRef" {
      type = .ref
      targets = [&secret]
    }
  }
  field "status" {
    type = .record
    field "phase" {
      type = .string
    }
    field "readyReplicas" {
      type = .int
    }
  }
}
`))
	if err != nil {
		return nil, err
	}
	secretSchema, err := resource.CompileKindSchema("secret.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"

root {
  field "spec" {
    type = .record
    field "data" {
      type = .string
    }
  }
}
`))
	if err != nil {
		return nil, err
	}
	return resource.Schemas{
		"workload": workloadSchema,
		"secret":   secretSchema,
	}, nil
}

func clusterScopeResolver(p refs.Principal, _ keyspace.Object) (ownership.ActorID, ownership.Scope, error) {
	switch p {
	case actorAlice:
		return "User:alice", ownership.RootScope(ownership.PermAll), nil
	case actorBob:
		return "User:bob", ownership.RootScope(ownership.PermAll), nil
	case actorHPA:
		rule, err := ownership.NewExactRule(ownership.Path{"spec", "replicas"}, ownership.PermSet|ownership.PermDelete|ownership.PermOwn)
		if err != nil {
			return "", ownership.Scope{}, err
		}
		return "ServiceAccount:hpa", ownership.MustNewScope(rule), nil
	default:
		return "", ownership.Scope{}, errors.New("unknown actor")
	}
}

type clusterAuthorizer struct{}

func (clusterAuthorizer) Authorize(req refs.Request) bool {
	if req.Principal != actorAlice && req.Principal != actorBob {
		return false
	}
	return req.Target.Group == keyspace.CoreGroup && req.Target.Kind == "secret" && req.Target.Name == "db-creds"
}

func fld(k string, v lkal.Value) lkal.Field { return lkal.Field{Key: k, Value: v} }
func rec(fields ...lkal.Field) lkal.Record  { return lkal.Record{Fields: fields} }

// New initializes all coupled cluster components under a single sim.Queue.
func New(cfg Config) (*Sim, error) {
	if len(cfg.Tenants) == 0 || len(cfg.Aggregators) == 0 || len(cfg.Nodes) == 0 {
		return nil, errors.New("clustersim: tenants, aggregators, and nodes must be non-empty")
	}
	if cfg.TickIntervalMs < 1 || cfg.WorkloadStepMs < 1 || cfg.FaultIntervalMs < 1 {
		return nil, errors.New("clustersim: intervals must be >= 1")
	}
	if cfg.MinDelayMs < 0 || cfg.MaxDelayMs < cfg.MinDelayMs {
		return nil, errors.New("clustersim: invalid delay bounds")
	}
	if cfg.Settle == nil {
		return nil, errors.New("clustersim: Settle is required")
	}

	schemas, err := compileClusterSchemas()
	if err != nil {
		return nil, err
	}

	q := sim.NewQueue(cfg.Seed)
	st := memstore.New()
	tenReg := tenant.NewRegistry(st)
	cacheClk := sim.NewFakeClock(sim.DefaultEpoch)
	upstream := &pausableStore{Store: st, staleRev: cfg.Mutations.StaleCurrentRev, staleRead: cfg.Mutations.StaleStoreFallback}
	caches, err := watchcache.NewManager(upstream, watchcache.Config{
		Clock:            cacheClk,
		ReadWait:         cacheReadWait,
		ProgressInterval: 10 * time.Millisecond,
		BookmarkInterval: time.Hour,
	})
	if err != nil {
		return nil, err
	}
	liveReg := liveness.NewRegistryWithClock(st, q.Clock())

	s := &Sim{
		cfg:      cfg,
		q:        q,
		store:    st,
		tenReg:   tenReg,
		cacheClk: cacheClk,
		caches:   caches,
		liveReg:  liveReg,
		schemas:  schemas,
		aggByID:  make(map[string]*simAgg, len(cfg.Aggregators)),
		nodeByID: make(map[string]*simNode, len(cfg.Nodes)),
		cuts:     make(map[[2]string]bool),
		granted:  make(map[string]bool),
		reported: make(map[string]bool),
		upstream: upstream,
	}

	ctx := context.Background()
	wPrefix, err := keyspace.ObjectPrefix(keyspace.CoreGroup, "workload")
	if err != nil {
		caches.Close()
		return nil, err
	}
	tenantNames := slices.Clone(cfg.Tenants)
	slices.Sort(tenantNames)
	for ti, tName := range tenantNames {
		if err := tenReg.Register(ctx, tName); err != nil {
			caches.Close()
			return nil, fmt.Errorf("register tenant %s: %w", tName, err)
		}
		view, err := tenReg.View(ctx, tName)
		if err != nil {
			caches.Close()
			return nil, err
		}
		objs := resource.NewObjects(view, resource.Config{
			Now:           q.Clock().Now,
			Rand:          sim.RandReader(q.SubRand(uint64(100 + ti))),
			Tenant:        tName,
			Schemas:       schemas,
			Authorizer:    clusterAuthorizer{},
			ScopeResolver: clusterScopeResolver,
		})
		// Seed a target secret per tenant so workload secretRef exercises refs & reverse-ref index.
		secretDoc := rec(
			fld("meta", rec(
				fld("kind", lkal.String("secret")),
				fld("name", lkal.String("db-creds")),
			)),
			fld("spec", rec(fld("data", lkal.String("secret-"+tName)))),
		)
		if _, err := objs.Create(ctx, actorAlice, secretDoc); err != nil {
			caches.Close()
			return nil, fmt.Errorf("seed secret for %s: %w", tName, err)
		}
		wCache, err := caches.Objects(tName, keyspace.CoreGroup, "workload")
		if err != nil {
			caches.Close()
			return nil, err
		}
		sCache, err := caches.Objects(tName, keyspace.CoreGroup, "secret")
		if err != nil {
			caches.Close()
			return nil, err
		}
		s.tenants = append(s.tenants, &simTenant{
			name:      tName,
			objects:   objs,
			cache:     wCache,
			secCache:  sCache,
			workloads: []string{"api", "worker", "cron", "queue"},
			maxOwnRev: make(map[string]store.Rev),
			prefix:    wPrefix,
		})
	}

	aggIDs := slices.Clone(cfg.Aggregators)
	slices.Sort(aggIDs)
	nodeNames := slices.Clone(cfg.Nodes)
	slices.Sort(nodeNames)

	initialRecs := make([]liveness.Record, 0, len(nodeNames))
	wall0 := q.Clock().Now().UnixMilli()
	for i, nName := range nodeNames {
		rack := fmt.Sprintf("rack-%d", i%2)
		r, err := liveReg.Register(ctx, nName, rack, wall0)
		if err != nil {
			caches.Close()
			return nil, err
		}
		initialRecs = append(initialRecs, r)
	}

	rankedFn := func(node string) []string {
		return hrw.Ranked(node, aggIDs)
	}

	for _, id := range aggIDs {
		engCfg := liveness.DefaultAggregatorEngineConfig(id, aggIDs)
		engCfg.Detector.RoundBase = uint64(1) << 32
		if cfg.EvictionGraceMs > 0 {
			engCfg.Detector.EvictionGrace = cfg.EvictionGraceMs
		}
		eng, err := liveness.NewAggregatorEngine(engCfg)
		if err != nil {
			caches.Close()
			return nil, err
		}
		recs := make(map[string]liveness.Record, len(initialRecs))
		for _, r := range initialRecs {
			recs[r.Node] = r
			eng.Observed(0, r)
		}
		a := &simAgg{
			id:          id,
			engine:      eng,
			records:     recs,
			incarnation: 1,
		}
		s.aggs = append(s.aggs, a)
		s.aggByID[id] = a
		phase := q.Rand().Int64N(cfg.TickIntervalMs)
		s.scheduleAggTick(a, phase)
	}

	unitSpec := lifecycle.Spec{
		RestartPolicy:  lifecycle.RestartOnFailure,
		StopGraceMs:    300,
		BackoffBaseMs:  100,
		BackoffMaxMs:   800,
		BackoffResetMs: 2_000,
		CNICount:       2,
	}

	for i, nName := range nodeNames {
		rack := fmt.Sprintf("rack-%d", i%2)
		agCfg := agent.DefaultConfig(nName, rankedFn)
		eng, err := liveness.NewNodeEngine(agCfg)
		if err != nil {
			caches.Close()
			return nil, err
		}
		sn := &simNode{
			name:    nName,
			rack:    rack,
			cfg:     agCfg,
			engine:  eng,
			desired: make(map[string]desiredEntry),
		}
		// Attach two managed container units per (node, tenant) pair, each for another workload.
		for _, stTen := range s.tenants {
			for k := range 2 {
				w := stTen.workloads[(i+k)%len(stTen.workloads)]
				u := &simUnit{
					id:       nName + "-" + stTen.name + "-" + w,
					node:     nName,
					tenant:   stTen.name,
					workload: w,
					spec:     unitSpec,
					state:    lifecycle.InitState(),
				}
				sn.units = append(sn.units, u)
				s.allUnits = append(s.allUnits, u)
			}
		}
		s.nodes = append(s.nodes, sn)
		s.nodeByID[nName] = sn
		phase := q.Rand().Int64N(cfg.TickIntervalMs)
		s.scheduleNodeTick(sn, phase)
	}

	// Schedule periodic workload/ownership operations and fault injection on the shared queue.
	s.scheduleWorkloadStep(cfg.WorkloadStepMs)
	s.scheduleFaultStep(cfg.FaultIntervalMs)

	wctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	for _, stTen := range s.tenants {
		view, err := tenReg.View(ctx, stTen.name)
		if err != nil {
			s.Close()
			return nil, err
		}
		w, err := view.Watch(wctx, stTen.prefix, 1)
		if err != nil {
			s.Close()
			return nil, fmt.Errorf("reference watch %s: %w", stTen.name, err)
		}
		stTen.ref = &refLog{}
		go stTen.ref.drain(w)
	}
	cfg.Settle()
	for _, sn := range s.nodes {
		for _, stTen := range s.tenants {
			w, err := stTen.cache.Watch(wctx, stTen.prefix, watchcache.WatchOptions{SendInitialEvents: true})
			if err != nil {
				s.Close()
				return nil, fmt.Errorf("watch %s on %s: %w", stTen.name, sn.name, err)
			}
			nw := &nodeWatch{tenant: stTen.name, watch: w}
			sn.watches = append(sn.watches, nw)
			go nw.drain()
		}
	}
	cfg.Settle()

	return s, nil
}

func (nw *nodeWatch) drain() {
	for ev := range nw.watch.Events() {
		nw.mu.Lock()
		nw.inbox = append(nw.inbox, ev)
		nw.all = append(nw.all, ev)
		nw.mu.Unlock()
	}
	nw.mu.Lock()
	nw.err = nw.watch.Err()
	nw.done = true
	nw.mu.Unlock()
}

// Close releases background watchcache goroutines cleanly.
func (s *Sim) Close() {
	if s.closed {
		return
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
	}
	s.caches.Close()
}

// pumpCache hands the events each node's watches received to node control in a fixed order.
func (s *Sim) pumpCache() {
	for _, n := range s.nodes {
		for _, nw := range n.watches {
			nw.mu.Lock()
			nw.pending = append(nw.pending, nw.inbox...)
			nw.inbox = nw.inbox[:0]
			done, err := nw.done, nw.err
			nw.mu.Unlock()
			if done && err != nil && !errors.Is(err, context.Canceled) {
				s.recordOnce("watch_ended/"+n.name+"/"+nw.tenant, fmt.Errorf("watch of %s on %s ended: %w", nw.tenant, n.name, err))
			}
			if n.stopped || n.isolated {
				if s.cfg.Mutations.DiscardEventsWhileDown {
					nw.pending = nw.pending[:0]
				}
				continue
			}
			for _, ev := range nw.pending {
				s.consumeCacheEvent(n, nw.tenant, ev)
			}
			nw.pending = nw.pending[:0]
		}
	}
}

func (s *Sim) consumeCacheEvent(n *simNode, tenantName string, ev watchcache.Event) {
	isDelete := ev.Type == watchcache.EventDelete
	if (ev.Type != watchcache.EventPut && !isDelete) || s.cfg.Mutations.DropCacheEventsForNode == n.name ||
		(isDelete && s.cfg.Mutations.IgnoreDeleteEvents) {
		return
	}
	var prefix string
	for _, t := range s.tenants {
		if t.name == tenantName {
			prefix = t.prefix
		}
	}
	name := strings.TrimPrefix(ev.Key, prefix)
	s.stats.CacheEventsConsumed++
	s.q.Record("cache_ev", n.name, tenantName, name, strconv.FormatInt(int64(ev.Rev), 10), strconv.Itoa(len(ev.Value)), strconv.Itoa(int(ev.Type)))
	if _, known := n.desired[tenantName+"/"+name]; known && !isDelete && s.cfg.Mutations.IgnoreUpdates {
		return
	}
	if isDelete {
		delete(n.desired, tenantName+"/"+name)
	} else {
		n.desired[tenantName+"/"+name] = desiredEntry{rev: ev.Rev, value: bytes.Clone(ev.Value)}
	}
	for _, u := range n.units {
		s.reconcileUnit(n, u)
	}
}

// reconcileUnit brings a unit of a healthy node toward the desired state it learned from the cache.
func (s *Sim) reconcileUnit(n *simNode, u *simUnit) {
	if s.tearDown || n.stopped || n.evicted {
		return
	}
	if u.state.Phase == lifecycle.PhaseStopped && !s.cfg.Mutations.NeverReviveStoppedUnits {
		u.state = lifecycle.InitState()
		u.generation++
		u.inFlight = false
		u.faultStop = false
		s.stats.UnitsRevived++
		s.q.Record("unit_revive", u.id)
	}
	_, want := n.desired[u.tenant+"/"+u.workload]
	want = want || s.cfg.Mutations.IgnoreDesired
	switch {
	case want && u.state.Phase == lifecycle.PhaseInit:
		s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvStartRequested, NowMs: s.q.NowMs()})
		s.driveUnit(u)
	case !want && (u.state.Phase == lifecycle.PhaseStarting || u.state.Phase == lifecycle.PhaseRunning || u.state.Phase == lifecycle.PhaseBackoff):
		s.q.Record("unit_undesired", u.id)
		s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvStopRequested, NowMs: s.q.NowMs()})
		s.driveUnit(u)
	}
}

func (s *Sim) stopUnitsForFault(n *simNode, cause string) {
	for _, u := range n.units {
		if u.state.Phase == lifecycle.PhaseStopped {
			continue
		}
		u.faultStop = true
		s.q.Record("fault_stop_unit", u.id, cause)
		s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvStopRequested, NowMs: s.q.NowMs()})
		s.driveUnit(u)
		if s.cfg.Mutations.StopOnlyFirstUnitOnFault {
			return
		}
	}
}

// afterStep moves the cache clock up to queue time. Verification may have pushed the cache
// clock ahead of the queue; the clock then simply waits for the queue to catch up.
func (s *Sim) afterStep() {
	if d := s.q.NowMs() - s.cacheMs; d > 0 {
		s.cacheClk.Advance(time.Duration(d) * time.Millisecond)
		s.cacheMs = s.q.NowMs()
	}
	s.cfg.Settle()
	s.pumpCache()
	s.checkNodeControl()
}

// checkNodeControl verifies after every step that node control follows what the cache told
// it: nothing runs on a node that is stopped or evicted, and nothing runs for a workload the
// node has not learned about.
func (s *Sim) checkNodeControl() {
	for _, n := range s.nodes {
		for _, u := range n.units {
			if u.state.Phase != lifecycle.PhaseRunning {
				continue
			}
			if n.down() {
				s.recordOnce("run_down/"+u.id, fmt.Errorf("unit %s is running on %s while the node is stopped or evicted (t=%dms)", u.id, n.name, s.q.NowMs()))
			}
			if _, ok := n.desired[u.tenant+"/"+u.workload]; !ok {
				s.recordOnce("run_undesired/"+u.id, fmt.Errorf("unit %s is running on %s without the desired workload %s/%s (t=%dms)", u.id, n.name, u.tenant, u.workload, s.q.NowMs()))
			}
		}
	}
}

// TraceHash returns the deterministic SHA-256 hash of all recorded cluster events.
func (s *Sim) TraceHash() string { return s.q.TraceHash() }

// Stats returns a snapshot of counters collected during the simulation.
func (s *Sim) Stats() Stats { return s.stats }

// StopNode forcefully halts a simulated node so liveness detectors can observe NotReady and eviction.
func (s *Sim) StopNode(name string) {
	if n := s.nodeByID[name]; n != nil {
		s.stopNode(n)
	}
}

// RunUntil advances the single-threaded event queue to targetMs and returns any invariant violation.
func (s *Sim) RunUntil(targetMs int64) error {
	reached := false
	s.q.ScheduleAt(targetMs, func() { reached = true })
	for !reached && s.q.Step() {
		s.afterStep()
	}
	if len(s.invErrors) > 0 {
		return errors.Join(s.invErrors...)
	}
	return nil
}

func (s *Sim) healFaults() {
	if s.cfg.Mutations.SkipHealFaults {
		return
	}
	if !s.cfg.Mutations.SkipResumeUpstream {
		s.upstream.resume()
	}
	for _, n := range s.nodes {
		n.isolated = false
		if n.stopped {
			s.startNode(n)
		}
	}
	for _, a := range s.aggs {
		a.isolated = false
		a.storeCut = false
	}
	clear(s.cuts)
}

// PauseCacheUpstream stalls every watch event flowing into the watch caches until the fault
// heals, so consistent reads must fall back to the store.
func (s *Sim) PauseCacheUpstream() {
	if s.upstream.pause() {
		s.stats.CachePauses++
		s.q.Record("pause_cache_upstream")
	}
}

// VerifyConvergence heals every injected fault, stops injecting new faults and new workload
// operations, lets the cluster settle for horizonMs, and then checks that nothing is still
// faulted, that every node learned exactly the store's workloads at the store's revisions and
// values, that every healthy node runs exactly the units its desired set calls for, and that
// every watch delivered exactly the store's history.
func (s *Sim) VerifyConvergence(horizonMs int64) error {
	s.faultsOff = true
	s.quiet = true
	s.healFaults()
	s.q.Record("heal_all")
	if err := s.RunUntil(s.q.NowMs() + horizonMs); err != nil {
		return err
	}
	if err := s.verifyHealed(); err != nil {
		return err
	}
	if err := s.verifyDesiredMatchesStore(); err != nil {
		return err
	}
	if err := s.verifyUnitsFollowDesired(); err != nil {
		return err
	}
	return s.verifyAllInvariants(true)
}

func (s *Sim) verifyHealed() error {
	if s.upstream.paused() {
		return errors.New("the cache upstream is still stalled after the faults were healed")
	}
	for _, n := range s.nodes {
		if n.stopped || n.isolated {
			return fmt.Errorf("node %s is still stopped=%t isolated=%t after the faults were healed", n.name, n.stopped, n.isolated)
		}
	}
	for _, a := range s.aggs {
		if a.isolated || a.storeCut {
			return fmt.Errorf("aggregator %s is still isolated=%t storeCut=%t after the faults were healed", a.id, a.isolated, a.storeCut)
		}
	}
	if len(s.cuts) != 0 {
		return fmt.Errorf("%d links are still cut after the faults were healed", len(s.cuts))
	}
	return nil
}

func (s *Sim) verifyDesiredMatchesStore() error {
	ctx := context.Background()
	for _, stTen := range s.tenants {
		view, err := s.tenReg.View(ctx, stTen.name)
		if err != nil {
			return err
		}
		kvs, _, _, err := view.Range(ctx, stTen.prefix, 0, 0, "")
		if err != nil {
			return fmt.Errorf("store Range %s: %w", stTen.name, err)
		}
		for _, n := range s.nodes {
			learned := 0
			for k := range n.desired {
				if strings.HasPrefix(k, stTen.name+"/") {
					learned++
				}
			}
			if learned != len(kvs) {
				return fmt.Errorf("node %s desired %d workloads of %s after heal, store has %d", n.name, learned, stTen.name, len(kvs))
			}
			for _, kv := range kvs {
				name := strings.TrimPrefix(kv.Key, stTen.prefix)
				d, ok := n.desired[stTen.name+"/"+name]
				switch {
				case !ok:
					return fmt.Errorf("node %s never learned workload %s/%s that the store holds after heal", n.name, stTen.name, name)
				case d.rev != kv.ModRev:
					return fmt.Errorf("node %s holds workload %s/%s at revision %d, the store at %d", n.name, stTen.name, name, d.rev, kv.ModRev)
				case !bytes.Equal(d.value, kv.Value):
					return fmt.Errorf("node %s holds a stale value of workload %s/%s at revision %d", n.name, stTen.name, name, kv.ModRev)
				}
			}
		}
	}
	return nil
}

// verifyUnitsFollowDesired demands that, once faults are healed and the cluster has settled,
// every healthy node runs a unit exactly when the node's desired set holds its workload. A unit
// that is not running yet is acceptable only while the lifecycle is still working toward it:
// starting, backing off before a retry or tearing down with a step or timer pending. A node
// that liveness has evicted is skipped, because it stays down until liveness readmits it.
func (s *Sim) verifyUnitsFollowDesired() error {
	for _, n := range s.nodes {
		if n.stopped || n.isolated || n.evicted {
			continue
		}
		for _, u := range n.units {
			_, want := n.desired[u.tenant+"/"+u.workload]
			ph := u.state.Phase
			working := u.inFlight
			switch {
			case want && ph == lifecycle.PhaseRunning:
			case want && working && (ph == lifecycle.PhaseStarting || ph == lifecycle.PhaseBackoff || ph == lifecycle.PhaseRollingBack || ph == lifecycle.PhaseStopping):
			case want:
				return fmt.Errorf("unit %s is %s on healthy node %s although workload %s/%s is desired (in flight: %t)", u.id, ph, n.name, u.tenant, u.workload, working)
			case !want && (ph == lifecycle.PhaseInit || ph == lifecycle.PhaseStopped):
			case !want && working && (ph == lifecycle.PhaseStopping || ph == lifecycle.PhaseRollingBack):
			default:
				return fmt.Errorf("unit %s is %s on healthy node %s although workload %s/%s is not desired", u.id, ph, n.name, u.tenant, u.workload)
			}
		}
	}
	return nil
}

// StopAllUnitsAndVerifyTeardown requests graceful stop on every managed unit, drives
// teardown to completion, and verifies zero leaked container/CNI/netns resources.
func (s *Sim) StopAllUnitsAndVerifyTeardown(horizonMs int64) error {
	s.tearDown = true
	s.healFaults()

	for _, u := range s.allUnits {
		s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvStopRequested, NowMs: s.q.NowMs()})
		s.driveUnit(u)
	}
	if err := s.RunUntil(s.q.NowMs() + horizonMs); err != nil {
		return err
	}
	for _, u := range s.allUnits {
		if !lifecycle.WellFormed(u.spec, u.state) {
			return fmt.Errorf("unit %s violated WellFormed after teardown: %+v", u.id, u.state)
		}
		if u.state.Phase != lifecycle.PhaseStopped {
			return fmt.Errorf("unit %s phase = %s, want stopped", u.id, u.state.Phase)
		}
		if !u.state.Resources.Empty() {
			return fmt.Errorf("unit %s leaked resources after stop: %+v", u.id, u.state.Resources)
		}
	}
	s.q.Record("teardown_verified", strconv.Itoa(len(s.allUnits)))
	return nil
}

// VerifyAllInvariants checks tenant isolation, ownership invariants, watchcache convergence
// with memstore, liveness budget bounds, and unit lifecycle well-formedness.
func (s *Sim) VerifyAllInvariants() error { return s.verifyAllInvariants(!s.upstream.paused()) }

func (s *Sim) verifyAllInvariants(exactWatches bool) error {
	if len(s.invErrors) > 0 {
		return errors.Join(s.invErrors...)
	}
	if err := s.verifyWatchCacheAndTenantIsolation(); err != nil {
		return err
	}
	if err := s.verifyWatchStreams(exactWatches); err != nil {
		return err
	}
	if err := s.verifyOwnershipInvariants(); err != nil {
		return err
	}
	if err := s.verifyLivenessBudgetAndRecords(); err != nil {
		return err
	}
	for _, u := range s.allUnits {
		if !lifecycle.WellFormed(u.spec, u.state) {
			return fmt.Errorf("unit %s not well-formed: %+v", u.id, u.state)
		}
	}
	return nil
}

func (s *Sim) recordErr(err error) {
	if err != nil {
		s.invErrors = append(s.invErrors, err)
	}
}

func (s *Sim) recordOnce(key string, err error) {
	if s.reported[key] {
		return
	}
	s.reported[key] = true
	s.recordErr(err)
}

func (s *Sim) drawDelay() int64 {
	span := s.cfg.MaxDelayMs - s.cfg.MinDelayMs + 1
	if span <= 1 {
		return s.cfg.MinDelayMs
	}
	return s.cfg.MinDelayMs + s.q.Rand().Int64N(span)
}

func addrKey(a liveness.Addr) string {
	if a.Node {
		return "n:" + a.ID
	}
	return "a:" + a.ID
}

func (s *Sim) canDeliver(from, to liveness.Addr) bool {
	if from.Node {
		n := s.nodeByID[from.ID]
		if n == nil || n.stopped || n.isolated {
			return false
		}
	} else {
		a := s.aggByID[from.ID]
		if a == nil || a.isolated {
			return false
		}
	}
	if to.Node {
		n := s.nodeByID[to.ID]
		if n == nil || n.stopped || n.isolated {
			return false
		}
	} else {
		a := s.aggByID[to.ID]
		if a == nil || a.isolated {
			return false
		}
	}
	if s.cuts[[2]string{addrKey(from), addrKey(to)}] {
		return false
	}
	return true
}

func (s *Sim) sendLivenessMsgs(msgs []liveness.Message) {
	for _, m := range msgs {
		if !s.canDeliver(m.From, m.To) {
			continue
		}
		if s.cfg.DropPermille > 0 && s.q.Rand().Int64N(1000) < int64(s.cfg.DropPermille) {
			continue
		}
		msg := m
		delay := s.drawDelay()
		s.q.ScheduleAfter(delay, func() {
			if !s.canDeliver(msg.From, msg.To) {
				return
			}
			if msg.To.Node {
				n := s.nodeByID[msg.To.ID]
				if n == nil || n.stopped {
					return
				}
				outs := n.engine.Handle(s.q.NowMs(), msg)
				s.sendLivenessMsgs(outs)
				return
			}
			a := s.aggByID[msg.To.ID]
			if a == nil {
				return
			}
			outs := a.engine.Handle(s.q.NowMs(), msg)
			s.sendLivenessMsgs(outs)
		})
	}
}

func (s *Sim) scheduleAggTick(a *simAgg, delayMs int64) {
	s.q.ScheduleAfter(delayMs, func() {
		msgs, props := a.engine.Tick(s.q.NowMs())
		s.sendLivenessMsgs(msgs)
		if !a.storeCut {
			s.executeProposals(a, props)
		}
		s.scheduleAggTick(a, s.cfg.TickIntervalMs)
	})
}

func (s *Sim) executeProposals(a *simAgg, props []liveness.Proposal) {
	ctx := context.Background()
	wall := s.q.Clock().Now().UnixMilli()
	totalNodes := len(s.nodes)
	for _, p := range props {
		cur, ok := a.records[p.Node]
		if !ok || cur.ModRev != p.ModRev {
			continue
		}
		var (
			out liveness.Outcome
			err error
		)
		switch p.Kind {
		case liveness.ProposeReady:
			out, err = s.liveReg.SetReady(ctx, cur, wall)
			if out == liveness.Committed {
				s.stats.LivenessReady++
				if sn := s.nodeByID[p.Node]; sn != nil && sn.evicted {
					sn.evicted = false
					s.q.Record("node_readmitted", sn.name)
					for _, u := range sn.units {
						s.reconcileUnit(sn, u)
					}
				}
			}
		case liveness.ProposeNotReady:
			out, err = s.liveReg.SetNotReady(ctx, cur, wall)
			if out == liveness.Committed {
				s.stats.LivenessNotReady++
			}
		case liveness.ProposeEvictable:
			out, err = s.liveReg.GrantEviction(ctx, cur, wall, totalNodes)
			if out == liveness.Committed {
				s.stats.LivenessEvictions++
				s.grants = append(s.grants, wall)
				s.granted[p.Node] = true
				// Evicting a node tears down its managed units.
				if sn := s.nodeByID[p.Node]; sn != nil {
					sn.evicted = true
					if !s.cfg.Mutations.SkipStopOnEviction {
						s.stopUnitsForFault(sn, "eviction")
					}
				}
			}
		}
		if err != nil {
			s.recordErr(fmt.Errorf("liveness proposal %d on %s: %w", p.Kind, p.Node, err))
			continue
		}
		s.q.Record("live_prop", a.id, strconv.Itoa(int(p.Kind)), p.Node, strconv.Itoa(int(out)))
		if out == liveness.Committed {
			updated, getErr := s.liveReg.Get(ctx, p.Node)
			if getErr != nil {
				s.recordErr(getErr)
				continue
			}
			s.broadcastRecord(updated)
		}
	}
}

func (s *Sim) broadcastRecord(rec liveness.Record) {
	for _, a := range s.aggs {
		if a.storeCut {
			continue
		}
		agg := a
		r := rec
		delay := s.drawDelay()
		s.q.ScheduleAfter(delay, func() {
			if agg.storeCut {
				return
			}
			if prev, ok := agg.records[r.Node]; ok && prev.ModRev >= r.ModRev {
				return
			}
			agg.records[r.Node] = r
			agg.engine.Observed(s.q.NowMs(), r)
		})
	}
}

func (s *Sim) scheduleNodeTick(n *simNode, delayMs int64) {
	s.q.ScheduleAfter(delayMs, func() {
		if !n.stopped {
			msgs := n.engine.Tick(s.q.NowMs())
			s.sendLivenessMsgs(msgs)
		}
		s.scheduleNodeTick(n, s.cfg.TickIntervalMs)
	})
}

func (s *Sim) stopNode(n *simNode) {
	if n.stopped {
		return
	}
	n.stopped = true
	s.stats.NodeStopsInjected++
	s.q.Record("stop_node", n.name)
	if !s.cfg.Mutations.SkipStopOnNodeStop {
		s.stopUnitsForFault(n, "node_failure")
	}
}

func (s *Sim) startNode(n *simNode) {
	if !n.stopped {
		return
	}
	eng, err := liveness.NewNodeEngine(n.cfg)
	if err != nil {
		s.recordErr(err)
		return
	}
	n.engine = eng
	n.stopped = false
	s.q.Record("start_node", n.name)
	if s.cfg.Mutations.SkipStartUnitsOnRestart {
		return
	}
	for _, u := range n.units {
		s.reconcileUnit(n, u)
	}
}

func (s *Sim) applyUnitEvent(u *simUnit, ev lifecycle.Event) {
	prevPhase := u.state.Phase
	u.state = lifecycle.ApplyEvent(u.spec, u.state, ev)
	if !lifecycle.WellFormed(u.spec, u.state) {
		s.recordErr(fmt.Errorf("unit %s broke WellFormed after %s: %+v", u.id, ev.Kind, u.state))
	}
	if u.state.Phase == lifecycle.PhaseStopped && !u.state.Resources.Empty() {
		s.recordErr(fmt.Errorf("unit %s entered stopped with held resources: %+v", u.id, u.state.Resources))
	}
	if prevPhase != lifecycle.PhaseRunning && u.state.Phase == lifecycle.PhaseRunning {
		s.stats.UnitsStarted++
	}
	if prevPhase != lifecycle.PhaseStopped && u.state.Phase == lifecycle.PhaseStopped {
		s.stats.UnitsStopped++
		if u.faultStop {
			s.stats.UnitsStoppedByFault++
		}
	}
	s.q.Record("unit_ev", u.id, ev.Kind.String(), u.state.Phase.String())
	if prevPhase != lifecycle.PhaseStopped && u.state.Phase == lifecycle.PhaseStopped {
		if n := s.nodeByID[u.node]; n != nil {
			s.reconcileUnit(n, u)
		}
	}
}

func (s *Sim) driveUnit(u *simUnit) {
	if u.inFlight {
		return
	}
	act := lifecycle.NextStep(u.spec, u.state, s.q.NowMs())
	switch act.Kind {
	case lifecycle.ActionNone:
		return
	case lifecycle.ActionWait:
		if act.WaitUntilMs > s.q.NowMs() {
			u.inFlight = true
			gen := u.generation
			s.q.ScheduleAt(act.WaitUntilMs, func() {
				if u.generation != gen {
					return
				}
				u.inFlight = false
				s.driveUnit(u)
			})
		} else if u.state.Phase == lifecycle.PhaseRunning && s.q.Buggify(s.cfg.BuggifyPermille/2) {
			// Inject occasional container task crash while running to exercise backoff & restart.
			u.inFlight = true
			gen := u.generation
			exitCode := uint32(1 + s.q.Rand().Int64N(2))
			s.q.ScheduleAfter(50+s.drawDelay(), func() {
				if u.generation != gen {
					return
				}
				u.inFlight = false
				if u.state.Phase != lifecycle.PhaseRunning {
					s.driveUnit(u)
					return
				}
				s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvTaskExited, NowMs: s.q.NowMs(), ExitCode: exitCode})
				s.driveUnit(u)
			})
		}
		return
	}

	u.inFlight = true
	gen := u.generation
	delay := s.drawDelay()
	s.q.ScheduleAfter(delay, func() {
		if u.generation != gen {
			return
		}
		u.inFlight = false
		fail := s.q.Buggify(s.cfg.BuggifyPermille)
		now := s.q.NowMs()
		switch act.Kind {
		case lifecycle.ActionCreateNetns:
			if fail {
				s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvNetnsCreateFailed, NowMs: now})
			} else {
				s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvNetnsCreated, NowMs: now})
			}
		case lifecycle.ActionAddCNI:
			if fail {
				s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvCNIAddFailed, NowMs: now, CNIIndex: act.CNIIndex})
			} else {
				s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvCNIAdded, NowMs: now, CNIIndex: act.CNIIndex})
			}
		case lifecycle.ActionEnsureImage:
			if fail {
				s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvImageFailed, NowMs: now})
			} else {
				s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvImageReady, NowMs: now})
			}
		case lifecycle.ActionCreateContainer:
			if fail {
				s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvContainerCreateFailed, NowMs: now})
			} else {
				s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvContainerCreated, NowMs: now})
			}
		case lifecycle.ActionStartTask:
			if fail {
				s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvTaskStartFailed, NowMs: now})
			} else {
				s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvTaskStarted, NowMs: now})
			}
		case lifecycle.ActionTermTask:
			s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvTermSent, NowMs: now})
			if !fail {
				s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvTaskExited, NowMs: now, ExitCode: 0})
			}
		case lifecycle.ActionKillTask:
			s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvKillSent, NowMs: now})
			s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvTaskExited, NowMs: now, ExitCode: 137})
		case lifecycle.ActionDeleteTask:
			s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvTaskDeleted, NowMs: now, OK: !fail})
		case lifecycle.ActionDeleteContainer:
			s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvContainerDeleted, NowMs: now, OK: !fail})
		case lifecycle.ActionDelCNI:
			s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvCNIDeleted, NowMs: now, CNIIndex: act.CNIIndex, OK: !fail})
		case lifecycle.ActionRemoveNetns:
			s.applyUnitEvent(u, lifecycle.Event{Kind: lifecycle.EvNetnsRemoved, NowMs: now, OK: !fail})
		}
		s.driveUnit(u)
	})
}

func (s *Sim) scheduleWorkloadStep(delayMs int64) {
	s.q.ScheduleAfter(delayMs, func() {
		s.runWorkloadOp()
		s.scheduleWorkloadStep(s.cfg.WorkloadStepMs)
	})
}

func (s *Sim) runWorkloadOp() {
	if s.quiet {
		return
	}
	ctx := context.Background()
	stTen := s.tenants[s.q.Rand().Int64N(int64(len(s.tenants)))]
	wName := stTen.workloads[s.q.Rand().Int64N(int64(len(stTen.workloads)))]
	obj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: wName}

	choice := s.q.Rand().Int64N(8)
	switch choice {
	case 0, 1, 6:
		// Multi-actor Apply (alice, bob, or hpa) creating or contending on leaves.
		actors := []refs.Principal{actorAlice, actorBob, actorHPA}
		actor := actors[s.q.Rand().Int64N(int64(len(actors)))]
		var decl lkal.Record
		if actor == actorHPA {
			replicas := 1 + s.q.Rand().Int64N(5)
			decl = rec(
				fld("meta", rec(fld("kind", lkal.String("workload")), fld("name", lkal.String(wName)))),
				fld("spec", rec(fld("replicas", lkal.Int(replicas)))),
			)
		} else {
			stepOrder := 10 + s.q.Rand().Int64N(stepOrderSpan)
			decl = rec(
				fld("meta", rec(fld("kind", lkal.String("workload")), fld("name", lkal.String(wName)))),
				fld("spec", rec(
					fld("payload", lkal.String(fmt.Sprintf("%s-%d", actor.ID, s.q.NowMs()))),
					fld("secretRef", lkal.Ref{"secret", "db-creds"}),
					fld("steps", rec(
						fld("run", rec(fld("order", lkal.Int(stepOrder)), fld("cmd", lkal.String("/bin/run")))),
					)),
				)),
			)
		}
		opts := resource.ApplyOptions{}
		token := tokenNone
		if exp, err := stTen.objects.Explain(ctx, obj, 0); err == nil {
			if !s.q.Buggify(s.cfg.BuggifyPermille) {
				opts.Expect = exp.Expect
				opts.ExpectedOwnerRev = store.Rev(exp.OwnerRevision)
				token = tokenFresh
			} else if exp.OwnerRevision > 1 {
				// Inject stale owner revision CAS conflict.
				opts.ExpectedOwnerRev = store.Rev(exp.OwnerRevision - 1)
				token = tokenStale
				s.stats.StaleTokenOps++
			}
		}
		res, err := s.apply(ctx, stTen, actor, decl, opts)
		if err == nil && token == tokenStale {
			s.recordErr(fmt.Errorf("apply on %s/%s by %s succeeded with a stale owner revision", stTen.name, wName, actor.ID))
		}
		if err != nil {
			if kind := s.classify("apply", stTen.name, wName, actor.ID, token, err); kind == errConflict {
				s.stats.ApplyConflicts++
			}
		} else {
			s.stats.ApplySucceeded++
			if prev, ok := stTen.maxOwnRev[wName]; ok && res.OwnerRevision < prev {
				s.recordErr(fmt.Errorf("owner revision regressed on %s/%s: %d < %d", stTen.name, wName, res.OwnerRevision, prev))
			}
			stTen.maxOwnRev[wName] = res.OwnerRevision
			s.q.Record("apply_ok", stTen.name, wName, actor.ID, strconv.FormatInt(int64(res.OwnerRevision), 10))
		}

	case 2:
		// Prune an owned field for alice or bob.
		actor := actorAlice
		if s.q.Rand().Int64N(2) == 1 {
			actor = actorBob
		}
		if exp, err := stTen.objects.Explain(ctx, obj, 0); err == nil {
			decl := rec(
				fld("meta", rec(fld("kind", lkal.String("workload")), fld("name", lkal.String(wName)))),
				fld("spec", rec(fld("secretRef", lkal.Ref{"secret", "db-creds"}))),
			)
			pruneOpts := resource.ApplyOptions{
				Expect:           exp.Expect,
				ExpectedOwnerRev: store.Rev(exp.OwnerRevision),
			}
			var res resource.ApplyResult
			pruneErr := s.rejectOwnerRev(pruneOpts.ExpectedOwnerRev)
			if pruneErr == nil {
				res, pruneErr = stTen.objects.Prune(ctx, actor, decl, pruneOpts)
			}
			if pruneErr != nil {
				if kind := s.classify("prune", stTen.name, wName, actor.ID, tokenFresh, pruneErr); kind == errConflict {
					s.stats.PruneConflicts++
				}
			} else {
				s.stats.PruneOps++
				if prev, ok := stTen.maxOwnRev[wName]; ok && res.OwnerRevision < prev {
					s.recordErr(fmt.Errorf("prune owner revision regressed on %s/%s: %d < %d", stTen.name, wName, res.OwnerRevision, prev))
				}
				stTen.maxOwnRev[wName] = res.OwnerRevision
				s.q.Record("prune_ok", stTen.name, wName, actor.ID, strconv.FormatInt(int64(res.OwnerRevision), 10))
			}
		}

	case 3:
		// Release + Adopt handoff on spec/payload between alice and bob.
		if exp, err := stTen.objects.Explain(ctx, obj, 0); err == nil {
			for _, lf := range exp.Leaves {
				if lf.Path.String() != "spec/payload" || lf.State != ownership.StateOwned {
					continue
				}
				var curOwner, nextOwner refs.Principal
				switch lf.Owner {
				case "User:alice":
					curOwner, nextOwner = actorAlice, actorBob
				case "User:bob":
					curOwner, nextOwner = actorBob, actorAlice
				default:
					continue
				}
				relOpts := resource.ResolveOptions{
					Path:             "spec/payload",
					Expect:           lf.Expect,
					ExpectedOwnerRev: store.Rev(exp.OwnerRevision),
				}
				var relRes resource.ResolveResult
				relErr := s.rejectOwnerRev(relOpts.ExpectedOwnerRev)
				if relErr == nil {
					relRes, relErr = stTen.objects.Release(ctx, curOwner, obj, relOpts)
				}
				if relErr != nil {
					s.classify("release", stTen.name, wName, curOwner.ID, tokenFresh, relErr)
					break
				}
				exp2, expErr := stTen.objects.Explain(ctx, obj, 0)
				if expErr != nil {
					s.recordErr(expErr)
					break
				}
				var leafToken string
				for _, lf2 := range exp2.Leaves {
					if lf2.Path.String() == "spec/payload" {
						leafToken = lf2.Expect
						break
					}
				}
				adoptOpts := resource.ResolveOptions{
					Path:             "spec/payload",
					Expect:           leafToken,
					ExpectedOwnerRev: store.Rev(relRes.Explanation.OwnerRevision),
				}
				var adoptRes resource.ResolveResult
				adoptErr := s.rejectOwnerRev(adoptOpts.ExpectedOwnerRev)
				if adoptErr == nil {
					adoptRes, adoptErr = stTen.objects.Adopt(ctx, nextOwner, obj, adoptOpts)
				}
				if adoptErr != nil {
					s.recordErr(fmt.Errorf("adopt after release failed on %s/%s: %w", stTen.name, wName, adoptErr))
					break
				}
				adoptRev := store.Rev(adoptRes.Explanation.OwnerRevision)
				if adoptRev < stTen.maxOwnRev[wName] {
					s.recordErr(fmt.Errorf("adopt owner rev regressed on %s/%s", stTen.name, wName))
				}
				stTen.maxOwnRev[wName] = adoptRev
				s.stats.ReleaseAdoptOps++
				s.q.Record("handoff", stTen.name, wName, curOwner.ID, nextOwner.ID, strconv.FormatInt(int64(adoptRev), 10))
				break
			}
		}

	case 4:
		// Patch orderedset step via Patch entrance.
		if _, err := stTen.objects.Get(ctx, obj); err == nil {
			order := 10 + s.q.Rand().Int64N(stepOrderSpan)
			_, patchErr := stTen.objects.Patch(ctx, actorAlice, obj, []patch.Op{{
				Kind:  patch.OpSet,
				Path:  "spec/steps/check",
				Value: rec(fld("order", lkal.Int(order)), fld("cmd", lkal.String("/bin/check"))),
			}})
			if patchErr != nil {
				if kind := s.classify("patch", stTen.name, wName, actorAlice.ID, tokenNone, patchErr); kind != errNone {
					s.stats.PatchRejected++
				}
			} else {
				s.stats.PatchOps++
				s.q.Record("patch_ok", stTen.name, wName, strconv.FormatInt(order, 10))
			}
		}

	case 7:
		// Delete the workload so desired sets shrink and a node that missed events is visible.
		if _, err := stTen.objects.Get(ctx, obj); err == nil {
			if delErr := stTen.objects.Delete(ctx, obj, 0); delErr != nil {
				s.classify("delete", stTen.name, wName, "", tokenNone, delErr)
			} else {
				s.stats.DeleteOps++
				delete(stTen.maxOwnRev, wName)
				s.q.Record("delete_ok", stTen.name, wName)
			}
		}

	case 5:
		// Status update via PatchStatus entrance reflecting active unit count.
		if _, err := stTen.objects.Get(ctx, obj); err == nil {
			readyCount := int64(0)
			for _, u := range s.allUnits {
				if u.tenant == stTen.name && u.workload == wName && u.state.Phase == lifecycle.PhaseRunning {
					readyCount++
				}
			}
			phase := "Pending"
			if readyCount > 0 {
				phase = "Running"
			}
			_, stErr := stTen.objects.PatchStatus(ctx, obj, []patch.Op{
				{Kind: patch.OpSet, Path: "status/phase", Value: lkal.String(phase)},
				{Kind: patch.OpSet, Path: "status/readyReplicas", Value: lkal.Int(readyCount)},
			})
			if stErr != nil {
				s.classify("patch_status", stTen.name, wName, "", tokenNone, stErr)
			} else {
				s.stats.PatchStatusOps++
				s.q.Record("status_ok", stTen.name, wName, phase, strconv.FormatInt(readyCount, 10))
			}
		}
	}
}

type errKind int

const (
	errNone errKind = iota
	errConflict
	errRejected
)

// tokenKind says what concurrency token an operation carried. Only a deliberately injected
// stale owner revision may be rejected for being stale.
type tokenKind int

const (
	tokenNone tokenKind = iota
	tokenFresh
	tokenStale
)

const staleOwnerRevDetail = "ownerRevision mismatch"

func (s *Sim) apply(ctx context.Context, t *simTenant, actor refs.Principal, decl lkal.Record, opts resource.ApplyOptions) (resource.ApplyResult, error) {
	if err := s.rejectOwnerRev(opts.ExpectedOwnerRev); err != nil {
		return resource.ApplyResult{}, err
	}
	return t.objects.Apply(ctx, actor, decl, opts)
}

// rejectOwnerRev is the mutation hook that makes every operation carrying an owner revision
// fail the way a stale one does.
func (s *Sim) rejectOwnerRev(rev store.Rev) error {
	if !s.cfg.Mutations.RejectEveryOwnerRev || rev == 0 {
		return nil
	}
	return &ownership.Error{Code: ownership.CodeOwnershipConflict, Detail: fmt.Sprintf("%s: expected %d, current %d", staleOwnerRevDetail, rev, rev)}
}

// isStaleOwnerRev recognises the rejection of a stale owner revision: an ownership conflict
// that names no leaf, unlike a conflict with another actor's leaf.
func isStaleOwnerRev(oe *ownership.Error) bool {
	if strings.Contains(oe.Detail, staleOwnerRevDetail) {
		return true
	}
	return oe.Code == ownership.CodeOwnershipConflict && oe.Path == "" &&
		!slices.ContainsFunc(oe.Diagnostics, func(d ownership.Diagnostic) bool { return len(d.Path) > 0 })
}

// expectedFailures lists, per operation, what a racing but otherwise well-formed call may
// legitimately return. The operations run synchronously on one queue, so a lost compare-and-set
// race cannot happen; every other failure is an invariant violation. A rejection for a stale
// owner revision is a legitimate outcome only for an operation that was given a stale one on
// purpose; with a fresh token it means the token check is wrong.
//
//   - apply: another actor owns a declared leaf (ownership conflict), a declared leaf holds a
//     value nobody owns after a prune unmanaged it (unowned value conflict), or the declared
//     step reuses an order (ordered-set rejection).
//   - prune: the declared leaf is owned by another actor (ownership conflict).
//   - patch: the step reuses an order (ordered-set rejection).
//   - delete, release, adopt and status patches are issued with fresh tokens on objects and
//     leaves the caller owns, so they have no legitimate failure.
var expectedFailures = map[string]struct {
	conflicts  []ownership.Code
	orderedSet bool
}{
	"apply": {conflicts: []ownership.Code{ownership.CodeOwnershipConflict, ownership.CodeUnownedValueConflict}, orderedSet: true},
	"prune": {conflicts: []ownership.Code{ownership.CodeOwnershipConflict}},
	"patch": {orderedSet: true},
}

// classify counts the errors op may legitimately meet and fails the simulation on any other.
func (s *Sim) classify(op, tenantName, workload, actor string, token tokenKind, err error) errKind {
	want := expectedFailures[op]
	var oe *ownership.Error
	var kind errKind
	var detail string
	switch {
	case want.orderedSet && isOrderedSetRejection(err):
		s.stats.OrderedSetRejections++
		kind, detail = errRejected, "duplicate_order"
	case errors.As(err, &oe) && isStaleOwnerRev(oe):
		if token != tokenStale {
			s.recordErr(fmt.Errorf("%s (%s/%s by %s) was rejected for a stale owner revision although its token was not stale: %w", op, tenantName, workload, actor, err))
			return errNone
		}
		kind, detail = errConflict, "stale_owner_revision"
	case errors.As(err, &oe) && slices.Contains(want.conflicts, oe.Code):
		kind, detail = errConflict, string(oe.Code)
	default:
		s.recordErr(fmt.Errorf("unexpected %s error (%s/%s by %s): %w", op, tenantName, workload, actor, err))
		return errNone
	}
	s.q.Record(op+"_rejected", tenantName, workload, actor, detail)
	return kind
}

// isOrderedSetRejection recognises the schema's duplicate-value rejection by the code token
// that leads the envelope error detail.
func isOrderedSetRejection(err error) bool {
	var ee *envelope.Error
	if !errors.As(err, &ee) || ee.Kind != envelope.ErrMalformed {
		return false
	}
	code, _, _ := strings.Cut(ee.Detail, " ")
	return code == "L509"
}

func (s *Sim) scheduleFaultStep(delayMs int64) {
	s.q.ScheduleAfter(delayMs, func() {
		s.injectFault()
		s.scheduleFaultStep(s.cfg.FaultIntervalMs)
	})
}

func (s *Sim) faultDuration(base, span int64) int64 {
	if s.q.Buggify(s.cfg.LongFaultPermille) {
		return 5_000 + s.q.Rand().Int64N(7_000)
	}
	return base + s.q.Rand().Int64N(span)
}

func (s *Sim) injectFault() {
	if s.faultsOff {
		return
	}
	switch s.q.Rand().Int64N(5) {
	case 0:
		// Temporarily isolate a node and heal later.
		n := s.nodes[s.q.Rand().Int64N(int64(len(s.nodes)))]
		if !n.isolated {
			n.isolated = true
			s.stats.PartitionsInjected++
			s.q.Record("isolate_node", n.name)
			dur := s.faultDuration(400, 800)
			s.q.ScheduleAfter(dur, func() {
				n.isolated = false
				s.q.Record("heal_node", n.name)
			})
		}
	case 1:
		// Temporarily stop and restart a node.
		n := s.nodes[s.q.Rand().Int64N(int64(len(s.nodes)))]
		if !n.stopped {
			s.stopNode(n)
			dur := s.faultDuration(600, 1_200)
			s.q.ScheduleAfter(dur, func() {
				s.startNode(n)
			})
		}
	case 2:
		// Temporarily cut a directed aggregator-node link.
		a := s.aggs[s.q.Rand().Int64N(int64(len(s.aggs)))]
		n := s.nodes[s.q.Rand().Int64N(int64(len(s.nodes)))]
		edge := [2]string{addrKey(liveness.Addr{ID: n.name, Node: true}), addrKey(liveness.Addr{ID: a.id})}
		if !s.cuts[edge] {
			s.cuts[edge] = true
			s.stats.PartitionsInjected++
			s.q.Record("cut_link", n.name, a.id)
			dur := 500 + s.q.Rand().Int64N(900)
			s.q.ScheduleAfter(dur, func() {
				delete(s.cuts, edge)
				s.q.Record("uncut_link", n.name, a.id)
			})
		}
	case 3:
		// Temporarily cut one aggregator from the store, then resync on heal.
		a := s.aggs[s.q.Rand().Int64N(int64(len(s.aggs)))]
		if !a.storeCut {
			a.storeCut = true
			s.stats.PartitionsInjected++
			s.q.Record("cut_store", a.id)
			dur := 500 + s.q.Rand().Int64N(700)
			s.q.ScheduleAfter(dur, func() {
				a.storeCut = false
				recs, _, err := s.liveReg.List(context.Background())
				if err != nil {
					s.recordErr(err)
					return
				}
				for _, r := range recs {
					a.records[r.Node] = r
					a.engine.Observed(s.q.NowMs(), r)
				}
				s.q.Record("heal_store", a.id)
			})
		}
	case 4:
		// Stall the upstream of every watch cache for a while, then let it catch up.
		if s.upstream.pause() {
			s.stats.CachePauses++
			s.q.Record("pause_cache_upstream")
			s.q.ScheduleAfter(300+s.q.Rand().Int64N(1_200), func() {
				s.upstream.resume()
				s.q.Record("resume_cache_upstream")
			})
		}
	}
}

func (s *Sim) syncCacheRange(c *watchcache.Cache, prefix string) ([]store.KV, store.Rev, error) {
	floor, err := s.store.CurrentRev(context.Background())
	if err != nil {
		return nil, 0, err
	}
	type rangeRes struct {
		kvs []store.KV
		rev store.Rev
		err error
	}
	done := make(chan rangeRes, 1)
	go func() {
		kvs, rev, _, err := c.Range(context.Background(), prefix, 0, 0, "")
		done <- rangeRes{kvs: kvs, rev: rev, err: err}
	}()
	for range 4 * int(cacheReadWait/time.Millisecond) / cacheClockMs {
		s.cfg.Settle()
		select {
		case res := <-done:
			if res.err == nil && res.rev < floor {
				return nil, 0, fmt.Errorf("consistent read answered at revision %d, but the store was already at %d", res.rev, floor)
			}
			return res.kvs, res.rev, res.err
		default:
		}
		s.cacheClk.Advance(cacheClockMs * time.Millisecond)
		s.cacheMs += cacheClockMs
	}
	return nil, 0, errors.New("consistent cache read did not complete")
}

func (s *Sim) verifyWatchCacheAndTenantIsolation() error {
	ctx := context.Background()
	wPrefix, err := keyspace.ObjectPrefix(keyspace.CoreGroup, "workload")
	if err != nil {
		return err
	}
	sPrefix, err := keyspace.ObjectPrefix(keyspace.CoreGroup, "secret")
	if err != nil {
		return err
	}
	for _, stTen := range s.tenants {
		view, err := s.tenReg.View(ctx, stTen.name)
		if err != nil {
			return err
		}
		for _, pair := range []struct {
			prefix string
			cache  *watchcache.Cache
		}{
			{prefix: wPrefix, cache: stTen.cache},
			{prefix: sPrefix, cache: stTen.secCache},
		} {
			gotKVs, gotRev, err := s.syncCacheRange(pair.cache, pair.prefix)
			if err != nil {
				return fmt.Errorf("cache Range %s %s: %w", stTen.name, pair.prefix, err)
			}
			wantKVs, wantRev, _, err := view.Range(ctx, pair.prefix, gotRev, 0, "")
			if err != nil {
				return fmt.Errorf("store Range %s %s@%d: %w", stTen.name, pair.prefix, gotRev, err)
			}
			if wantRev != gotRev || len(gotKVs) != len(wantKVs) {
				return fmt.Errorf("watchcache mismatch for %s %s: got %d@%d, store %d@%d",
					stTen.name, pair.prefix, len(gotKVs), gotRev, len(wantKVs), wantRev)
			}
			for i := range gotKVs {
				if gotKVs[i].Key != wantKVs[i].Key || !bytes.Equal(gotKVs[i].Value, wantKVs[i].Value) {
					return fmt.Errorf("watchcache entry %d mismatch for %s: cache=%s store=%s",
						i, stTen.name, gotKVs[i].Key, wantKVs[i].Key)
				}
				// Verify strict tenant isolation: no foreign tenant secret leaks across views.
				if pair.prefix == sPrefix && !bytes.Contains(gotKVs[i].Value, []byte("secret-"+stTen.name)) {
					return fmt.Errorf("tenant %s secret leaked cross-tenant data: %s", stTen.name, gotKVs[i].Value)
				}
			}
			s.stats.CacheVerifications++
			if s.upstream.paused() {
				s.stats.CacheVerificationsPaused++
			}
			s.q.Record("cache_ok", stTen.name, pair.prefix, strconv.Itoa(len(gotKVs)))
		}
	}
	return nil
}

func (s *Sim) tenantByName(name string) *simTenant {
	for _, t := range s.tenants {
		if t.name == name {
			return t
		}
	}
	return nil
}

// verifyWatchStreams compares what every node watch delivered against the store's own
// history: revisions strictly increase and no event is missing, duplicated or reordered. A
// stalled upstream may leave a watch behind, so callers demand completeness only without one.
func (s *Sim) verifyWatchStreams(exact bool) error {
	for _, n := range s.nodes {
		for _, nw := range n.watches {
			nw.mu.Lock()
			all := slices.Clone(nw.all)
			nw.mu.Unlock()
			want := s.tenantByName(nw.tenant).ref.snapshot()
			behind, err := checkWatchStream(all, want, exact)
			if err != nil {
				return fmt.Errorf("watch of %s on %s: %w", nw.tenant, n.name, err)
			}
			if behind {
				s.stats.WatchesBehind++
			}
		}
	}
	return nil
}

func checkWatchStream(all []watchcache.Event, history []store.Event, exact bool) (behind bool, err error) {
	end := slices.IndexFunc(all, func(ev watchcache.Event) bool { return ev.InitialEventsEnd })
	if end < 0 {
		return false, errors.New("initial events never ended")
	}
	snapRev := all[end].Rev
	var got []watchcache.Event
	last, bookmark := snapRev, store.Rev(0)
	for _, ev := range all[end+1:] {
		if ev.Type == watchcache.EventBookmark {
			if ev.Rev < bookmark {
				return false, fmt.Errorf("bookmark regressed from %d to %d", bookmark, ev.Rev)
			}
			bookmark = ev.Rev
			continue
		}
		if ev.Rev <= last {
			return false, fmt.Errorf("revision %d delivered after %d", ev.Rev, last)
		}
		if ev.Rev <= bookmark {
			return false, fmt.Errorf("revision %d delivered after a bookmark at %d", ev.Rev, bookmark)
		}
		last = ev.Rev
		got = append(got, ev)
	}
	var want []store.Event
	for _, ev := range history {
		if ev.Rev > snapRev {
			want = append(want, ev)
		}
	}
	for i, ev := range got {
		if i >= len(want) {
			return false, fmt.Errorf("delivered %d events but the store history has %d (extra %s@%d)", len(got), len(want), ev.Key, ev.Rev)
		}
		w := want[i]
		if !watchEventMatches(ev, w) {
			return false, fmt.Errorf("event %d is %s@%d, the store history has %s@%d", i, ev.Key, ev.Rev, w.Key, w.Rev)
		}
	}
	if len(got) < len(want) {
		if exact {
			return false, fmt.Errorf("delivered %d events, the store history has %d (first missing %s@%d)", len(got), len(want), want[len(got)].Key, want[len(got)].Rev)
		}
		return true, nil
	}
	return false, nil
}

func watchEventMatches(ev watchcache.Event, w store.Event) bool {
	switch {
	case ev.Type == watchcache.EventPut && w.Type == store.EventPut:
		return ev.Rev == w.Rev && ev.Key == w.Key && bytes.Equal(ev.Value, w.Value)
	case ev.Type == watchcache.EventDelete && w.Type == store.EventDelete:
		return ev.Rev == w.Rev && ev.Key == w.Key
	}
	return false
}

func (s *Sim) verifyOwnershipInvariants() error {
	ctx := context.Background()
	for _, stTen := range s.tenants {
		for _, wName := range stTen.workloads {
			obj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: wName}
			exp, err := stTen.objects.Explain(ctx, obj, 0)
			if errors.Is(err, resource.ErrNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("Explain(%s/%s): %w", stTen.name, wName, err)
			}
			if exp.OwnerRevision < 1 {
				return fmt.Errorf("invalid OwnerRevision %d for %s/%s", exp.OwnerRevision, stTen.name, wName)
			}
			seenPaths := make(map[string]struct{}, len(exp.Leaves))
			for _, lf := range exp.Leaves {
				p := lf.Path.String()
				if _, dup := seenPaths[p]; dup {
					return fmt.Errorf("duplicate leaf path %s on %s/%s", p, stTen.name, wName)
				}
				seenPaths[p] = struct{}{}
				if lf.State == ownership.StateOwned && lf.Owner == "" {
					return fmt.Errorf("owned leaf %s has empty owner on %s/%s", p, stTen.name, wName)
				}
				if lf.State != ownership.StateOwned && lf.Owner != "" {
					return fmt.Errorf("non-owned leaf %s (%s) still carries owner %q", p, lf.State, lf.Owner)
				}
			}
		}
	}
	return nil
}

func (s *Sim) verifyLivenessBudgetAndRecords() error {
	ctx := context.Background()
	recs, _, err := s.liveReg.List(ctx)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if r.Evictable && r.State != detector.StateNotReady {
			return fmt.Errorf("node %s marked evictable while state=%v", r.Node, r.State)
		}
	}
	for _, r := range recs {
		if r.Evictable && !s.granted[r.Node] {
			return fmt.Errorf("node %s evictable in the store without a grant observed by the simulation", r.Node)
		}
	}
	limit := budget.Limit(len(s.nodes), liveness.EvictionPercent)
	for i := range s.grants {
		inWin := 0
		for j := i; j >= 0 && s.grants[i]-s.grants[j] < liveness.EvictionWindow; j-- {
			inWin++
		}
		if inWin > limit {
			return fmt.Errorf("eviction budget exceeded: %d grants in window, limit=%d", inWin, limit)
		}
	}
	return nil
}
