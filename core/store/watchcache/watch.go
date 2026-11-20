package watchcache

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache/btree"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache/window"
)

const pollBatchSize = 64

// WatchOptions configures a Cache.Watch stream.
type WatchOptions struct {
	FromRev           store.Rev
	SendInitialEvents bool
}

// EventType identifies the kind of watch Event.
type EventType uint8

const (
	EventPut EventType = iota + 1
	EventDelete
	EventBookmark
)

// Event is one item delivered on a Watch stream.
// For EventBookmark, Rev states that every matching change up to Rev was delivered;
// InitialEventsEnd marks the bookmark that ends the initial state of a watch list.
type Event struct {
	Type             EventType
	Rev              store.Rev
	Key              string
	Value            []byte
	InitialEventsEnd bool
}

// Watch delivers matching changes and periodic bookmarks from a Cache.
type Watch struct {
	cache   *Cache
	ctx     context.Context
	prefix  string
	gen     uint64
	cursor  *window.Cursor
	hasSnap bool
	snap    btree.Tree
	snapRev store.Rev
	out     chan Event

	errMu sync.Mutex
	err   error
}

// Events returns the channel of watch events, closed when the watch ends.
func (w *Watch) Events() <-chan Event {
	return w.out
}

// Err reports why the watch ended once Events is closed.
func (w *Watch) Err() error {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	return w.err
}

func (w *Watch) finish(err error) {
	w.errMu.Lock()
	w.err = err
	w.errMu.Unlock()
	close(w.out)
}

// Watch starts a watch stream for prefix, which must start with the cache prefix.
func (c *Cache) Watch(ctx context.Context, prefix string, opts WatchOptions) (*Watch, error) {
	if err := c.checkClosed(); err != nil {
		return nil, err
	}
	if err := store.ValidateWatch(opts.FromRev); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(prefix, c.prefix) {
		return nil, fmt.Errorf("%w: prefix %q is outside cache prefix %q", store.ErrInvalidArgument, prefix, c.prefix)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var minRev store.Rev
	if opts.SendInitialEvents {
		minRev = opts.FromRev
		if minRev == 0 {
			rev, err := c.src.CurrentRev(ctx)
			if err != nil {
				return nil, err
			}
			minRev = rev
		}
	}

	c.mu.Lock()
	var timerCh <-chan time.Time
	timedOut := false

	var (
		cursor  *window.Cursor
		snap    btree.Tree
		snapRev store.Rev
		hasSnap bool
	)

	for {
		if c.closed {
			c.mu.Unlock()
			return nil, ErrClosed
		}
		if c.ready {
			var err error
			if opts.SendInitialEvents {
				snap, snapRev, cursor, err = c.win.WatchList(prefix, minRev)
				hasSnap = true
			} else {
				cursor, err = c.win.Watch(prefix, opts.FromRev)
			}
			if err == nil {
				break
			}
			if err != window.ErrNotYet {
				c.mu.Unlock()
				return nil, err
			}
		}
		if timedOut {
			ready := c.ready
			c.mu.Unlock()
			if !ready {
				return nil, ErrNotReady
			}
			if opts.SendInitialEvents && opts.FromRev == 0 {
				return nil, ErrNotReady
			}
			return nil, store.ErrFutureRev
		}
		var err error
		timedOut, err = c.waitStepLocked(ctx, &timerCh)
		if err != nil {
			return nil, err
		}
	}

	w := &Watch{
		cache:   c,
		ctx:     ctx,
		prefix:  prefix,
		gen:     c.gen,
		cursor:  cursor,
		hasSnap: hasSnap,
		snap:    snap,
		snapRev: snapRev,
		out:     make(chan Event),
	}
	c.wg.Add(1)
	go w.run()
	c.mu.Unlock()
	return w, nil
}

func (w *Watch) setWaitingLocked(waiting *bool, want bool) {
	if *waiting == want {
		return
	}
	if want {
		w.cache.addWaiterLocked()
	} else {
		w.cache.removeWaiterLocked()
	}
	*waiting = want
}

func (w *Watch) clearWaiting(waiting *bool) {
	if !*waiting {
		return
	}
	w.cache.mu.Lock()
	w.setWaitingLocked(waiting, false)
	w.cache.mu.Unlock()
}

func (w *Watch) sendEvent(ev Event) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if w.cache.ctx.Err() != nil {
		return ErrClosed
	}
	select {
	case w.out <- ev:
		return nil
	case <-w.ctx.Done():
		return w.ctx.Err()
	case <-w.cache.ctx.Done():
		return ErrClosed
	}
}

func (w *Watch) run() {
	c := w.cache
	defer c.wg.Done()

	waiting := false
	defer w.clearWaiting(&waiting)

	if w.hasSnap {
		var sendErr error
		w.snap.Ascend(w.prefix, "", func(kv store.KV) bool {
			ev := Event{
				Type:  EventPut,
				Rev:   kv.ModRev,
				Key:   kv.Key,
				Value: bytes.Clone(kv.Value),
			}
			sendErr = w.sendEvent(ev)
			return sendErr == nil
		})
		if sendErr != nil {
			w.finish(sendErr)
			return
		}
		bm := Event{
			Type:             EventBookmark,
			Rev:              w.snapRev,
			InitialEventsEnd: true,
		}
		if err := w.sendEvent(bm); err != nil {
			w.finish(err)
			return
		}
	}

	nextBM := c.clock.Now().Add(c.cfg.BookmarkInterval)
	var bmTimer <-chan time.Time
	var bmFallbackAt <-chan time.Time
	bookmarkDue := false
	bmSeqRecorded := false
	bmFallbackTimedOut := false
	var bmStartSeq uint64

	for {
		if err := w.ctx.Err(); err != nil {
			w.clearWaiting(&waiting)
			w.finish(err)
			return
		}

		c.mu.Lock()
		if c.closed {
			w.setWaitingLocked(&waiting, false)
			c.mu.Unlock()
			w.finish(ErrClosed)
			return
		}
		if c.gen != w.gen || !c.ready || c.win == nil {
			w.setWaitingLocked(&waiting, false)
			c.mu.Unlock()
			w.finish(ErrTooOld)
			return
		}

		evs, err := c.win.Poll(w.cursor, pollBatchSize)
		if err != nil {
			w.setWaitingLocked(&waiting, false)
			c.mu.Unlock()
			w.finish(ErrTooOld)
			return
		}
		if len(evs) > 0 {
			w.setWaitingLocked(&waiting, false)
			c.mu.Unlock()
			for _, sev := range evs {
				if err := w.sendEvent(convertEvent(sev)); err != nil {
					w.finish(err)
					return
				}
			}
			continue
		}
		if !c.win.Drained(w.cursor) {
			w.setWaitingLocked(&waiting, false)
			c.mu.Unlock()
			continue
		}

		now := c.clock.Now()
		if !bookmarkDue && !now.Before(nextBM) {
			bookmarkDue = true
		}

		if bookmarkDue {
			if !bmSeqRecorded {
				bmStartSeq = c.progressSeq
				bmSeqRecorded = true
				bmFallbackTimedOut = false
				bmFallbackAt = c.clock.After(c.cfg.ReadWait)
			}

			bmRev, ok := c.win.Bookmark(w.cursor)
			if ok && (c.progressSeq > bmStartSeq || bmFallbackTimedOut) {
				w.setWaitingLocked(&waiting, false)
				bookmarkDue = false
				bmSeqRecorded = false
				bmFallbackTimedOut = false
				bmFallbackAt = nil
				nextBM = now.Add(c.cfg.BookmarkInterval)
				bmTimer = nil
				c.mu.Unlock()

				if err := w.sendEvent(Event{Type: EventBookmark, Rev: bmRev}); err != nil {
					w.finish(err)
					return
				}
				continue
			}

			w.setWaitingLocked(&waiting, true)
		} else {
			w.setWaitingLocked(&waiting, false)
			if bmTimer == nil {
				d := nextBM.Sub(now)
				if d <= 0 {
					bookmarkDue = true
					c.mu.Unlock()
					continue
				}
				bmTimer = c.clock.After(d)
			}
		}

		wakeCh := c.wakeCh
		c.mu.Unlock()

		select {
		case <-wakeCh:
		case <-bmTimer:
			bmTimer = nil
			bookmarkDue = true
		case <-bmFallbackAt:
			bmFallbackAt = nil
			bmFallbackTimedOut = true
		case <-w.ctx.Done():
			w.clearWaiting(&waiting)
			w.finish(w.ctx.Err())
			return
		case <-c.ctx.Done():
			w.clearWaiting(&waiting)
			w.finish(ErrClosed)
			return
		}
	}
}

func convertEvent(sev store.Event) Event {
	ev := Event{
		Rev: sev.Rev,
		Key: sev.Key,
	}
	if sev.Type == store.EventPut {
		ev.Type = EventPut
		ev.Value = bytes.Clone(sev.Value)
	} else {
		ev.Type = EventDelete
	}
	return ev
}
