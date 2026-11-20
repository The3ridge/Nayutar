// Package window is the deterministic core of the watch cache: it maintains
// the current state, closed-revision snapshots, a bounded history window, and
// cursor positions for watches without goroutines, clocks, or store calls.
package window

import (
	"errors"
	"strings"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache/btree"
)

var (
	// ErrTooOld is returned when a requested revision or lagging cursor is older than the retained window.
	ErrTooOld = errors.New("watchcache: revision is older than the cache retains")
	// ErrNotYet is returned when the cache has not yet reached a requested revision.
	ErrNotYet = errors.New("watchcache: the cache has not reached the revision")
	// ErrOutOfOrder is returned when an upstream event or progress notification violates the store contract.
	ErrOutOfOrder = errors.New("watchcache: input breaks the store contract")
)

// Config controls how much change history a Window retains.
// MinEvents, MinAge, and MaxEvents must all be non-negative.
type Config struct {
	MinEvents int
	MinAge    time.Duration
	MaxEvents int
}

type entry struct {
	ev store.Event
	at time.Time
}

type snapshot struct {
	rev   store.Rev
	tree  btree.Tree
	count int
	last  time.Time
}

type ring[T any] struct {
	buf   []T
	head  int
	count int
}

func (r *ring[T]) len() int {
	return r.count
}

func (r *ring[T]) at(i int) T {
	return r.buf[(r.head+i)&(len(r.buf)-1)]
}

func (r *ring[T]) pushBack(v T) {
	if r.count == len(r.buf) {
		r.grow()
	}
	idx := (r.head + r.count) & (len(r.buf) - 1)
	r.buf[idx] = v
	r.count++
}

func (r *ring[T]) popFront(k int) {
	if k <= 0 {
		return
	}
	var zero T
	mask := len(r.buf) - 1
	for i := range k {
		r.buf[(r.head+i)&mask] = zero
	}
	r.head = (r.head + k) & mask
	r.count -= k
	if r.count == 0 {
		r.head = 0
	}
	if len(r.buf) > 16 && r.count*4 <= len(r.buf) {
		r.shrink()
	}
}

func (r *ring[T]) grow() {
	r.resize(max(len(r.buf)*2, 16))
}

func (r *ring[T]) shrink() {
	newCap := len(r.buf) / 2
	for newCap > 16 && r.count*4 <= newCap {
		newCap /= 2
	}
	r.resize(max(newCap, 16))
}

func (r *ring[T]) resize(newCap int) {
	newBuf := make([]T, newCap)
	if r.count > 0 {
		first := min(r.count, len(r.buf)-r.head)
		copy(newBuf, r.buf[r.head:r.head+first])
		copy(newBuf[first:], r.buf[:r.count-first])
	}
	r.buf = newBuf
	r.head = 0
}

// Window holds the deterministic watch cache state machine.
// Window is not safe for concurrent use; the caller serializes access.
type Window struct {
	cfg      Config
	base     btree.Tree
	applied  btree.Tree
	snaps    ring[snapshot]
	entries  ring[entry]
	startSeq int64
	totalSeq int64
	floor    store.Rev
	rev      store.Rev
	open     bool
	openCnt  int
	openLast time.Time
}

// Cursor tracks the position of a single watch within a Window.
type Cursor struct {
	prefix string
	seq    int64
}

// New constructs a Window initialized to the listed state kvs as of listRev.
// kvs must have pairwise distinct keys.
func New(cfg Config, listRev store.Rev, kvs []store.KV) *Window {
	var t btree.Tree
	for _, kv := range kvs {
		t = t.Put(kv)
	}
	return &Window{
		cfg:     cfg,
		base:    t,
		applied: t,
		floor:   listRev,
		rev:     listRev,
	}
}

// Event feeds one store change event received at time now into the Window.
func (w *Window) Event(ev store.Event, now time.Time) error {
	if ev.Type != store.EventPut && ev.Type != store.EventDelete {
		return ErrOutOfOrder
	}
	if ev.Rev <= 0 || ev.Rev <= w.rev {
		return ErrOutOfOrder
	}

	if w.open && ev.Rev > w.rev+1 {
		w.closeOpen()
	}

	stored := store.Event{
		Type: ev.Type,
		Rev:  ev.Rev,
		Key:  ev.Key,
	}
	if ev.Type == store.EventPut {
		stored.Value = ev.Value
		w.applied = w.applied.Put(store.KV{
			Key:    ev.Key,
			Value:  ev.Value,
			ModRev: ev.Rev,
		})
	} else {
		w.applied, _ = w.applied.Delete(ev.Key)
	}

	w.entries.pushBack(entry{ev: stored, at: now})
	w.totalSeq++
	w.rev = ev.Rev - 1
	w.open = true
	w.openCnt++
	w.openLast = now

	w.evict(now)
	return nil
}

// Progress feeds a store progress notification into the Window, closing any
// open revision at or below rev.
func (w *Window) Progress(rev store.Rev) error {
	minRev := w.rev
	if w.open {
		minRev = w.rev + 1
	}
	if rev < minRev {
		return ErrOutOfOrder
	}

	if w.open {
		w.closeOpen()
	}
	w.rev = rev
	w.open = false
	return nil
}

func (w *Window) closeOpen() {
	w.snaps.pushBack(snapshot{
		rev:   w.rev + 1,
		tree:  w.applied,
		count: w.openCnt,
		last:  w.openLast,
	})
	w.openCnt = 0
}

func (w *Window) evict(now time.Time) {
	for w.entries.len() > 0 {
		r := w.entries.at(0).ev.Rev
		if r > w.rev {
			return
		}
		snap := w.snaps.at(0)
		groupLen := snap.count
		n := w.entries.len()
		over := n > w.cfg.MaxEvents
		age := now.Sub(snap.last)
		if age < 0 {
			age = 0
		}
		old := n-groupLen >= w.cfg.MinEvents && age > w.cfg.MinAge
		if !over && !old {
			return
		}
		w.entries.popFront(groupLen)
		w.startSeq += int64(groupLen)
		w.floor = r
		w.base = snap.tree
		w.snaps.popFront(1)
	}
}

// Rev returns the closed revision.
func (w *Window) Rev() store.Rev {
	return w.rev
}

// Open reports whether changes for revision Rev()+1 have been received without closing yet.
func (w *Window) Open() bool {
	return w.open
}

// Floor returns the oldest revision boundary retained by the Window.
func (w *Window) Floor() store.Rev {
	return w.floor
}

// Retained returns the number of change events currently held in the window.
func (w *Window) Retained() int {
	return w.entries.len()
}

func (w *Window) readRev(atRev store.Rev) (store.Rev, error) {
	if atRev < 0 {
		return 0, store.ErrInvalidArgument
	}
	if atRev == 0 {
		return w.rev, nil
	}
	if atRev > w.rev {
		return 0, ErrNotYet
	}
	if atRev < w.floor {
		return 0, ErrTooOld
	}
	return atRev, nil
}

func (w *Window) snapshotAt(r store.Rev) btree.Tree {
	lo, hi := 0, w.snaps.len()
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if w.snaps.at(mid).rev <= r {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return w.base
	}
	return w.snaps.at(lo - 1).tree
}

func (w *Window) closedTree() btree.Tree {
	if w.snaps.len() > 0 {
		return w.snaps.at(w.snaps.len() - 1).tree
	}
	return w.base
}

// Get returns the value for key as of atRev (or the closed revision when atRev is 0)
// together with the revision the read reflects.
func (w *Window) Get(key string, atRev store.Rev) (store.KV, store.Rev, error) {
	if key == "" {
		return store.KV{}, 0, store.ErrInvalidArgument
	}
	r, err := w.readRev(atRev)
	if err != nil {
		return store.KV{}, 0, err
	}
	kv, ok := w.snapshotAt(r).Get(key)
	if !ok {
		return store.KV{}, r, store.ErrNotFound
	}
	return kv, r, nil
}

// Range returns up to limit keys starting with prefix and strictly after cont
// as of atRev (or the closed revision when atRev is 0), together with the read
// revision and continuation key if more entries remain.
func (w *Window) Range(prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	if limit < 0 || atRev < 0 || (cont != "" && !strings.HasPrefix(cont, prefix)) {
		return nil, 0, "", store.ErrInvalidArgument
	}
	r, err := w.readRev(atRev)
	if err != nil {
		return nil, 0, "", err
	}
	var out []store.KV
	more := false
	w.snapshotAt(r).Ascend(prefix, cont, func(kv store.KV) bool {
		if limit > 0 && len(out) == limit {
			more = true
			return false
		}
		out = append(out, kv)
		return true
	})
	if more {
		return out, r, out[len(out)-1].Key, nil
	}
	return out, r, "", nil
}

// Watch opens a Cursor starting at fromRev, or just after the closed revision when fromRev is 0.
func (w *Window) Watch(prefix string, fromRev store.Rev) (*Cursor, error) {
	if fromRev < 0 {
		return nil, store.ErrInvalidArgument
	}
	first := fromRev
	if fromRev == 0 {
		first = w.rev + 1
	}
	if first <= w.floor {
		return nil, ErrTooOld
	}
	if w.rev+1 < first {
		return nil, ErrNotYet
	}
	lo, hi := 0, w.entries.len()
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if w.entries.at(mid).ev.Rev < first {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return &Cursor{
		prefix: prefix,
		seq:    w.startSeq + int64(lo),
	}, nil
}

// WatchList returns the snapshot as of the closed revision, that revision, and
// a Cursor positioned after it, provided the closed revision has reached minRev.
func (w *Window) WatchList(prefix string, minRev store.Rev) (btree.Tree, store.Rev, *Cursor, error) {
	if minRev < 0 {
		return btree.Tree{}, 0, nil, store.ErrInvalidArgument
	}
	if w.rev < minRev {
		return btree.Tree{}, 0, nil, ErrNotYet
	}
	lo, hi := 0, w.entries.len()
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if w.entries.at(mid).ev.Rev <= w.rev {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return w.closedTree(), w.rev, &Cursor{
		prefix: prefix,
		seq:    w.startSeq + int64(lo),
	}, nil
}

// Poll returns matching events from c's current position, advancing across at
// most limit received changes (or all remaining changes when limit is 0).
func (w *Window) Poll(c *Cursor, limit int) ([]store.Event, error) {
	if limit < 0 || c == nil {
		return nil, store.ErrInvalidArgument
	}
	if c.seq < w.startSeq {
		return nil, ErrTooOld
	}
	off := int(c.seq - w.startSeq)
	count := w.entries.len() - off
	if limit > 0 && count > limit {
		count = limit
	}
	var out []store.Event
	for i := range count {
		ev := w.entries.at(off + i).ev
		if strings.HasPrefix(ev.Key, c.prefix) {
			out = append(out, ev)
		}
	}
	c.seq += int64(count)
	return out, nil
}

// Drained reports whether c has passed every change received by the Window.
func (w *Window) Drained(c *Cursor) bool {
	return c != nil && c.seq == w.totalSeq
}

// Bookmark returns the closed revision and true when c has passed every received
// change and no revision is currently open.
func (w *Window) Bookmark(c *Cursor) (store.Rev, bool) {
	if c != nil && c.seq == w.totalSeq && !w.open {
		return w.rev, true
	}
	return 0, false
}
