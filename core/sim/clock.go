package sim

import (
	"container/heap"
	"sync"
	"time"
)

// DefaultEpoch is the deterministic starting wall time of FakeClock and Queue.
var DefaultEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// FakeClock is a manually advanced TimerClock that fires timers in deterministic (deadline, seq) order.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	nextSeq uint64
	timers  fakeTimerHeap
}

// NewFakeClock constructs a FakeClock starting at start (or DefaultEpoch when start is zero).
func NewFakeClock(start time.Time) *FakeClock {
	if start.IsZero() {
		start = DefaultEpoch
	}
	return &FakeClock{now: start.UTC()}
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	t := c.now
	c.mu.Unlock()
	return t
}

func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	return c.NewTimer(d).C()
}

func (c *FakeClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return &fakeTimer{clock: c, ch: ch, index: -1, fired: true}
	}
	c.nextSeq++
	ft := &fakeTimer{
		clock:    c,
		deadline: c.now.Add(d),
		seq:      c.nextSeq,
		ch:       ch,
		index:    -1,
	}
	heap.Push(&c.timers, ft)
	return ft
}

// AfterFunc schedules f to execute synchronously inside Advance once d has elapsed.
func (c *FakeClock) AfterFunc(d time.Duration, f func()) func() bool {
	if d <= 0 {
		f()
		return func() bool { return false }
	}
	c.mu.Lock()
	c.nextSeq++
	ft := &fakeTimer{
		clock:    c,
		deadline: c.now.Add(d),
		seq:      c.nextSeq,
		fn:       f,
		index:    -1,
	}
	heap.Push(&c.timers, ft)
	c.mu.Unlock()
	return ft.Stop
}

// Advance moves the clock forward by d and fires all due timers in deterministic (deadline, seq) order.
func (c *FakeClock) Advance(d time.Duration) {
	if d < 0 {
		return
	}
	c.mu.Lock()
	target := c.now.Add(d)
	for len(c.timers) > 0 && !c.timers[0].deadline.After(target) {
		ft := heap.Pop(&c.timers).(*fakeTimer)
		if ft.deadline.After(c.now) {
			c.now = ft.deadline
		}
		ft.fired = true
		now := c.now
		fn := ft.fn
		ch := ft.ch
		if ch != nil {
			ch <- now
		}
		if fn != nil {
			c.mu.Unlock()
			fn()
			c.mu.Lock()
		}
	}
	if target.After(c.now) {
		c.now = target
	}
	c.mu.Unlock()
}

// PendingTimers returns the number of unfired, unstopped timers currently scheduled on c.
func (c *FakeClock) PendingTimers() int {
	c.mu.Lock()
	n := len(c.timers)
	c.mu.Unlock()
	return n
}

type fakeTimer struct {
	clock    *FakeClock
	deadline time.Time
	seq      uint64
	ch       chan time.Time
	fn       func()
	index    int
	fired    bool
	stopped  bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	c := t.clock
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.fired || t.stopped || t.index < 0 {
		return false
	}
	heap.Remove(&c.timers, t.index)
	t.stopped = true
	return true
}

type fakeTimerHeap []*fakeTimer

func (h fakeTimerHeap) Len() int { return len(h) }

func (h fakeTimerHeap) Less(i, j int) bool {
	if !h[i].deadline.Equal(h[j].deadline) {
		return h[i].deadline.Before(h[j].deadline)
	}
	return h[i].seq < h[j].seq
}

func (h fakeTimerHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *fakeTimerHeap) Push(x any) {
	ft := x.(*fakeTimer)
	ft.index = len(*h)
	*h = append(*h, ft)
}

func (h *fakeTimerHeap) Pop() any {
	old := *h
	n := len(old)
	ft := old[n-1]
	old[n-1] = nil
	ft.index = -1
	*h = old[:n-1]
	return ft
}
