// Package phi implements a phi accrual failure detector window using exact
// integer arithmetic for its threshold test.
package phi

import (
	"errors"
	"math/bits"
)

const maxTimeMS = 600_000

// zTable stores ceil(1000 * z_k^2) for k = 1..16, where z_k is the upper-tail
// quantile of the standard normal distribution at tail probability 10^-k.
var zTable = [17]uint64{
	0,
	1643, 5412, 9550, 13832,
	18190, 22596, 27034, 31495,
	35974, 40467, 44971, 49484,
	54005, 58533, 63065, 67603,
}

// Config configures a phi accrual failure detector Window.
type Config struct {
	Threshold       int
	MaxSamples      int
	MinStdDev       int64
	AcceptablePause int64
	FirstInterval   int64
	MaxInterval     int64
}

// DefaultConfig returns the default phi Window configuration.
func DefaultConfig() Config {
	return Config{
		Threshold:       8,
		MaxSamples:      1000,
		MinStdDev:       100,
		AcceptablePause: 3000,
		FirstInterval:   1000,
		MaxInterval:     10000,
	}
}

// Validate checks that every Config field is within its permitted range.
func (c Config) Validate() error {
	if c.Threshold < 1 || c.Threshold > 16 {
		return errors.New("phi: threshold out of range [1, 16]")
	}
	if c.MaxSamples < 1 || c.MaxSamples > 1000 {
		return errors.New("phi: max samples out of range [1, 1000]")
	}
	if c.MinStdDev < 0 || c.MinStdDev > maxTimeMS {
		return errors.New("phi: min std dev out of range [0, 600000]")
	}
	if c.AcceptablePause < 0 || c.AcceptablePause > maxTimeMS {
		return errors.New("phi: acceptable pause out of range [0, 600000]")
	}
	if c.FirstInterval < 4 || c.FirstInterval > maxTimeMS {
		return errors.New("phi: first interval out of range [4, 600000]")
	}
	minMaxInterval := c.FirstInterval + c.FirstInterval/4
	if c.MaxInterval < minMaxInterval || c.MaxInterval > maxTimeMS {
		return errors.New("phi: max interval out of range")
	}
	return nil
}

// Window tracks recent heartbeat intervals and evaluates whether the next
// heartbeat is overdue according to a phi accrual threshold.
type Window struct {
	cfg     Config
	buf     []int64
	head    int
	count   int
	sum     int64
	sumSq   int64
	last    int64
	started bool
}

// New constructs a Window for a valid Config.
func New(cfg Config) *Window {
	return &Window{
		cfg: cfg,
		buf: make([]int64, cfg.MaxSamples),
	}
}

// Started reports whether the Window has received at least one heartbeat.
func (w *Window) Started() bool {
	return w.started
}

// Last returns the monotonic timestamp of the last accepted heartbeat, or 0 if not started.
func (w *Window) Last() int64 {
	return w.last
}

// Samples returns a copy of the retained heartbeat intervals, oldest first.
func (w *Window) Samples() []int64 {
	out := make([]int64, w.count)
	if w.count == 0 {
		return out
	}
	first := min(w.count, len(w.buf)-w.head)
	copy(out, w.buf[w.head:w.head+first])
	copy(out[first:], w.buf[:w.count-first])
	return out
}

func (w *Window) push(x int64) {
	if w.count == len(w.buf) {
		oldest := w.buf[w.head]
		w.sum -= oldest
		w.sumSq -= oldest * oldest
		w.buf[w.head] = x
		w.head++
		if w.head == len(w.buf) {
			w.head = 0
		}
	} else {
		idx := w.head + w.count
		if idx >= len(w.buf) {
			idx -= len(w.buf)
		}
		w.buf[idx] = x
		w.count++
	}
	w.sum += x
	w.sumSq += x * x
}

// Heartbeat records a heartbeat at monotonic time now.
func (w *Window) Heartbeat(now int64) {
	if !w.started {
		f := w.cfg.FirstInterval
		w.push(f - f/4)
		w.push(f + f/4)
		w.last = now
		w.started = true
		return
	}
	if now < w.last {
		return
	}
	d := now - w.last
	if !w.Suspect(now) && d <= w.cfg.MaxInterval {
		w.push(d)
	}
	w.last = now
}

// Suspect reports whether the phi value at monotonic time now exceeds Threshold.
func (w *Window) Suspect(now int64) bool {
	if !w.started || now < w.last {
		return false
	}
	t := uint64(now) - uint64(w.last)
	pause := uint64(w.cfg.AcceptablePause)
	if t <= pause {
		return false
	}
	dt := t - pause
	n := uint64(w.count)
	hi, lo := bits.Mul64(n, dt)
	s := uint64(w.sum)
	aLo, borrow := bits.Sub64(lo, s, 0)
	if hi < borrow {
		return false
	}
	aHi := hi - borrow
	// Any A >= 2^40 makes 1000*A^2 >= 1000*2^80, which exceeds the maximum possible Z*V.
	if aHi > 0 || aLo >= 1<<40 {
		return true
	}
	if aLo == 0 {
		return false
	}

	sqHi, sqLo := bits.Mul64(aLo, aLo)
	p0Hi, lhsLo := bits.Mul64(sqLo, 1000)
	_, p1Lo := bits.Mul64(sqHi, 1000)
	lhsHi := p0Hi + p1Lo

	varV := n*uint64(w.sumSq) - s*s
	minTerm := n * uint64(w.cfg.MinStdDev)
	minV := minTerm * minTerm
	v := max(varV, minV)

	rhsHi, rhsLo := bits.Mul64(zTable[w.cfg.Threshold], v)
	return lhsHi > rhsHi || (lhsHi == rhsHi && lhsLo > rhsLo)
}
