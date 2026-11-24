package sim

import (
	"container/heap"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// EnvSeed selects a single simulation seed to run for deterministic reproduction.
	EnvSeed = "NAYUTAR_SIM_SEED"
	// EnvSeeds overrides the number of sequential seeds run in a simulation test.
	EnvSeeds = "NAYUTAR_SIM_SEEDS"
)

type queueEvent struct {
	atMs     int64
	seq      uint64
	fn       func()
	canceled bool
	index    int
}

type queueHeap []*queueEvent

func (h queueHeap) Len() int { return len(h) }

func (h queueHeap) Less(i, j int) bool {
	if h[i].atMs != h[j].atMs {
		return h[i].atMs < h[j].atMs
	}
	return h[i].seq < h[j].seq
}

func (h queueHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *queueHeap) Push(x any) {
	ev := x.(*queueEvent)
	ev.index = len(*h)
	*h = append(*h, ev)
}

func (h *queueHeap) Pop() any {
	old := *h
	n := len(old)
	ev := old[n-1]
	old[n-1] = nil
	ev.index = -1
	*h = old[:n-1]
	return ev
}

// Queue is a single-threaded discrete-event simulation loop ordered by integer
// milliseconds and monotonic sequence numbers (atMs, seq) with zero floating-point math.
type Queue struct {
	seed       uint64
	epoch      time.Time
	nowMs      int64
	nextSeq    uint64
	events     queueHeap
	bySeq      map[uint64]*queueEvent
	rng        Rand
	buggifyOn  bool
	traceHash  hash.Hash
	traceCount uint64
}

// NewQueue constructs a deterministic event queue seeded with seed starting at DefaultEpoch.
func NewQueue(seed uint64) *Queue {
	return NewQueueAt(seed, DefaultEpoch)
}

// NewQueueAt constructs a deterministic event queue seeded with seed starting at epoch.
func NewQueueAt(seed uint64, epoch time.Time) *Queue {
	if epoch.IsZero() {
		epoch = DefaultEpoch
	}
	return &Queue{
		seed:      seed,
		epoch:     epoch.UTC(),
		bySeq:     make(map[uint64]*queueEvent),
		rng:       NewDetRand(seed),
		buggifyOn: true,
		traceHash: sha256.New(),
	}
}

// Seed returns the root seed of the queue.
func (q *Queue) Seed() uint64 { return q.seed }

// NowMs returns the current simulated monotonic timestamp in integer milliseconds.
func (q *Queue) NowMs() int64 { return q.nowMs }

// Rand returns the queue's primary deterministic PRNG.
func (q *Queue) Rand() Rand { return q.rng }

// SubRand derives a deterministic child PRNG stream from (q.seed, stream).
func (q *Queue) SubRand(stream uint64) Rand {
	return NewDetRandStream(q.seed, stream)
}

// SetBuggifyEnabled enables or disables Buggify probabilistic fault triggers.
func (q *Queue) SetBuggifyEnabled(enabled bool) {
	q.buggifyOn = enabled
}

// Buggify returns true with probability permille/1000 using pure integer arithmetic.
func (q *Queue) Buggify(permille int) bool {
	if !q.buggifyOn || permille <= 0 {
		return false
	}
	if permille >= 1000 {
		return true
	}
	return q.rng.Int64N(1000) < int64(permille)
}

// ScheduleAt queues fn to run at atMs (clamped to >= NowMs) and returns its sequence ID.
func (q *Queue) ScheduleAt(atMs int64, fn func()) uint64 {
	if atMs < q.nowMs {
		atMs = q.nowMs
	}
	q.nextSeq++
	ev := &queueEvent{
		atMs:  atMs,
		seq:   q.nextSeq,
		fn:    fn,
		index: -1,
	}
	heap.Push(&q.events, ev)
	q.bySeq[ev.seq] = ev
	return ev.seq
}

// ScheduleAfter queues fn to run delayMs milliseconds after NowMs and returns its sequence ID.
func (q *Queue) ScheduleAfter(delayMs int64, fn func()) uint64 {
	if delayMs < 0 {
		delayMs = 0
	}
	return q.ScheduleAt(q.nowMs+delayMs, fn)
}

// Cancel removes a pending scheduled event by sequence ID.
func (q *Queue) Cancel(seq uint64) bool {
	ev, ok := q.bySeq[seq]
	if !ok {
		return false
	}
	delete(q.bySeq, seq)
	ev.canceled = true
	if ev.index >= 0 {
		heap.Remove(&q.events, ev.index)
	}
	return true
}

// Pending returns the number of active events remaining in the queue.
func (q *Queue) Pending() int { return len(q.events) }

// Step dispatches the earliest scheduled event and returns true, or false if the queue is empty.
func (q *Queue) Step() bool {
	for len(q.events) > 0 {
		ev := heap.Pop(&q.events).(*queueEvent)
		delete(q.bySeq, ev.seq)
		if ev.canceled {
			continue
		}
		if ev.atMs > q.nowMs {
			q.nowMs = ev.atMs
		}
		if ev.fn != nil {
			ev.fn()
		}
		return true
	}
	return false
}

// RunUntil processes every scheduled event with atMs <= targetMs and advances NowMs to targetMs.
func (q *Queue) RunUntil(targetMs int64) {
	if targetMs < q.nowMs {
		return
	}
	for len(q.events) > 0 && q.events[0].atMs <= targetMs {
		ev := heap.Pop(&q.events).(*queueEvent)
		delete(q.bySeq, ev.seq)
		if ev.canceled {
			continue
		}
		if ev.atMs > q.nowMs {
			q.nowMs = ev.atMs
		}
		if ev.fn != nil {
			ev.fn()
		}
	}
	if targetMs > q.nowMs {
		q.nowMs = targetMs
	}
}

// Drain processes all currently and transitively scheduled events until the queue is empty.
func (q *Queue) Drain() {
	for q.Step() {
	}
}

// Record hashes an unambiguous length-prefixed trace entry at NowMs into the simulation trace digest.
func (q *Queue) Record(tag string, fields ...string) {
	var hdr [16]byte
	binary.LittleEndian.PutUint64(hdr[0:8], uint64(q.nowMs))
	binary.LittleEndian.PutUint64(hdr[8:16], uint64(len(tag)))
	_, _ = q.traceHash.Write(hdr[:])
	_, _ = q.traceHash.Write([]byte(tag))

	var countBuf [8]byte
	binary.LittleEndian.PutUint64(countBuf[:], uint64(len(fields)))
	_, _ = q.traceHash.Write(countBuf[:])
	for _, f := range fields {
		binary.LittleEndian.PutUint64(countBuf[:], uint64(len(f)))
		_, _ = q.traceHash.Write(countBuf[:])
		_, _ = q.traceHash.Write([]byte(f))
	}
	q.traceCount++
}

// TraceCount returns how many Record entries have been hashed into the trace.
func (q *Queue) TraceCount() uint64 { return q.traceCount }

// TraceHash returns the hex-encoded SHA-256 digest of all recorded trace entries.
func (q *Queue) TraceHash() string {
	return hex.EncodeToString(q.traceHash.Sum(nil))
}

// Clock returns a TimerClock view driven by q's simulated time and event heap.
func (q *Queue) Clock() TimerClock { return queueClock{q: q} }

type queueClock struct {
	q *Queue
}

func (c queueClock) Now() time.Time {
	return c.q.epoch.Add(time.Duration(c.q.nowMs) * time.Millisecond)
}

func (c queueClock) After(d time.Duration) <-chan time.Time {
	return c.NewTimer(d).C()
}

func (c queueClock) NewTimer(d time.Duration) Timer {
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.Now()
		return &queueTimer{q: c.q, ch: ch, fired: true}
	}
	ms := d.Milliseconds()
	if ms == 0 {
		ms = 1
	}
	qt := &queueTimer{q: c.q, ch: ch}
	qt.seq = c.q.ScheduleAfter(ms, func() {
		qt.fired = true
		ch <- c.Now()
	})
	return qt
}

func (c queueClock) AfterFunc(d time.Duration, f func()) func() bool {
	if d <= 0 {
		f()
		return func() bool { return false }
	}
	ms := d.Milliseconds()
	if ms == 0 {
		ms = 1
	}
	fired := false
	seq := c.q.ScheduleAfter(ms, func() {
		fired = true
		f()
	})
	return func() bool {
		if fired || seq == 0 {
			return false
		}
		return c.q.Cancel(seq)
	}
}

type queueTimer struct {
	q     *Queue
	seq   uint64
	ch    chan time.Time
	fired bool
}

func (t *queueTimer) C() <-chan time.Time { return t.ch }

func (t *queueTimer) Stop() bool {
	if t.fired || t.seq == 0 {
		return false
	}
	return t.q.Cancel(t.seq)
}

// SeedConfig controls simulation seed selection and failure reproduction messages.
type SeedConfig struct {
	DefaultCount    int
	RegressionSeeds []uint64
	PackagePath     string
}

// ConfigFromEnv resolves the deterministic seed list from NAYUTAR_SIM_SEED,
// NAYUTAR_SIM_SEEDS, DefaultCount, and RegressionSeeds.
func ConfigFromEnv(cfg SeedConfig) ([]uint64, error) {
	if raw := strings.TrimSpace(os.Getenv(EnvSeed)); raw != "" {
		s, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid %s %q: %w", EnvSeed, raw, err)
		}
		return []uint64{s}, nil
	}
	count := cfg.DefaultCount
	if count < 1 {
		count = 1
	}
	if raw := strings.TrimSpace(os.Getenv(EnvSeeds)); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid %s %q: must be >= 1", EnvSeeds, raw)
		}
		count = n
	}
	out := make([]uint64, 0, count+len(cfg.RegressionSeeds))
	seen := make(map[uint64]struct{}, count+len(cfg.RegressionSeeds))
	for i := 1; i <= count; i++ {
		s := uint64(i)
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, s := range cfg.RegressionSeeds {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out, nil
}

// SeedT is the subset of testing.TB that RunSeeds needs, declared here so production binaries do not link package testing.
type SeedT interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

// RunSeeds executes fn for every seed resolved by ConfigFromEnv and fails with a clear reproduce command.
func RunSeeds(t SeedT, cfg SeedConfig, testName string, fn func(seed uint64) error) {
	t.Helper()
	seeds, err := ConfigFromEnv(cfg)
	if err != nil {
		t.Fatal(err)
	}
	pkg := cfg.PackagePath
	if pkg == "" {
		pkg = "./..."
	}
	for _, seed := range seeds {
		if err := fn(seed); err != nil {
			t.Fatalf("seed %d failed: %v\nrerun: %s=%d go test %s -run ^%s$",
				seed, err, EnvSeed, seed, pkg, testName)
		}
	}
}
