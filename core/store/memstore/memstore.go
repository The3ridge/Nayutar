// Package memstore is the in-memory reference implementation of the store contract.
//
// It keeps everything in one process and nothing on disk, so it serves tests
// and simulation, not production state.
package memstore

import (
	"bytes"
	"context"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/Kaikei-e/Nayutar/core/store"
)

type version struct {
	rev     store.Rev
	value   []byte
	deleted bool
}

// Store implements store.Store.
type Store struct {
	mu        sync.Mutex
	rev       store.Rev
	compacted store.Rev
	keys      map[string][]version
	log       []store.Event
	watchers  map[*watcher]struct{}
}

var _ store.Store = (*Store)(nil)

// New returns an empty store at revision 1, the revision of an empty etcd cluster.
func New() *Store {
	return &Store{
		rev:      1,
		keys:     make(map[string][]version),
		watchers: make(map[*watcher]struct{}),
	}
}

func (s *Store) readRev(atRev store.Rev) (store.Rev, error) {
	switch {
	case atRev == 0:
		return s.rev, nil
	case atRev > s.rev:
		return 0, store.ErrFutureRev
	case atRev < s.compacted:
		return 0, store.ErrCompacted
	default:
		return atRev, nil
	}
}

func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return bytes.Clone(b)
}

func (s *Store) lookup(key string, rev store.Rev) (store.KV, bool) {
	versions := s.keys[key]
	i := sort.Search(len(versions), func(i int) bool { return versions[i].rev > rev })
	if i == 0 || versions[i-1].deleted {
		return store.KV{}, false
	}
	v := versions[i-1]
	return store.KV{Key: key, Value: cloneBytes(v.value), ModRev: v.rev}, true
}

// Get reads under the store mutex and returns a copy of the value.
func (s *Store) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	if err := store.ValidateGet(key, atRev); err != nil {
		return store.KV{}, 0, err
	}
	if err := ctx.Err(); err != nil {
		return store.KV{}, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rev, err := s.readRev(atRev)
	if err != nil {
		return store.KV{}, 0, err
	}
	kv, ok := s.lookup(key, rev)
	if !ok {
		return store.KV{}, rev, store.ErrNotFound
	}
	return kv, rev, nil
}

// Range scans every key under the store mutex, so its cost grows with the whole key set, not the page.
func (s *Store) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	if err := store.ValidateRange(prefix, atRev, limit, cont); err != nil {
		return nil, 0, "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rev, err := s.readRev(atRev)
	if err != nil {
		return nil, 0, "", err
	}
	var names []string
	for key := range s.keys {
		if strings.HasPrefix(key, prefix) && key > cont {
			names = append(names, key)
		}
	}
	slices.Sort(names)
	var kvs []store.KV
	for _, key := range names {
		kv, ok := s.lookup(key, rev)
		if !ok {
			continue
		}
		if limit > 0 && len(kvs) == limit {
			return kvs, rev, kvs[len(kvs)-1].Key, nil
		}
		kvs = append(kvs, kv)
	}
	return kvs, rev, "", nil
}

// Txn applies under the store mutex and enforces no limit on the number of cmps or ops or on request size.
func (s *Store) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	if err := store.ValidateTxn(cmps, ops); err != nil {
		return false, 0, err
	}
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range cmps {
		var modRev store.Rev
		if kv, ok := s.lookup(c.Key, s.rev); ok {
			modRev = kv.ModRev
		}
		if !c.Holds(modRev) {
			return false, s.rev, nil
		}
	}
	next := s.rev + 1
	var events []store.Event
	for _, op := range ops {
		if op.Type == store.OpPut {
			events = append(events, store.Event{Type: store.EventPut, Rev: next, Key: op.Key, Value: cloneBytes(op.Value)})
			continue
		}
		if _, ok := s.lookup(op.Key, s.rev); ok {
			events = append(events, store.Event{Type: store.EventDelete, Rev: next, Key: op.Key})
		}
	}
	if len(events) == 0 {
		return true, s.rev, nil
	}
	s.rev = next
	for _, ev := range events {
		s.keys[ev.Key] = append(s.keys[ev.Key], version{rev: next, value: ev.Value, deleted: ev.Type == store.EventDelete})
	}
	s.log = append(s.log, events...)
	for w := range s.watchers {
		w.offer(events)
	}
	return true, s.rev, nil
}

// CurrentRev returns the in-memory revision.
func (s *Store) CurrentRev(ctx context.Context) (store.Rev, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rev, nil
}

// Compact drops the history below rev from memory; the state as of rev stays.
func (s *Store) Compact(ctx context.Context, rev store.Rev) error {
	if err := store.ValidateCompact(rev); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rev <= s.compacted {
		return store.ErrCompacted
	}
	if rev > s.rev {
		return store.ErrFutureRev
	}
	s.compacted = rev
	keep := sort.Search(len(s.log), func(i int) bool { return s.log[i].Rev >= rev })
	s.log = slices.Clone(s.log[keep:])
	for key, versions := range s.keys {
		i := sort.Search(len(versions), func(i int) bool { return versions[i].rev > rev })
		if i == 0 {
			continue
		}
		if versions[i-1].deleted {
			versions = versions[i:]
		} else {
			versions = versions[i-1:]
		}
		if len(versions) == 0 {
			delete(s.keys, key)
		} else {
			s.keys[key] = slices.Clone(versions)
		}
	}
	return nil
}

// Watch replays the retained log and then follows new commits; a compaction never ends a started watch.
func (s *Store) Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
	if err := store.ValidateWatch(fromRev); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w := &watcher{
		store:    s,
		watchCtx: ctx,
		prefix:   prefix,
		fromRev:  fromRev,
		out:      make(chan store.Event),
		wake:     make(chan struct{}, 1),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if fromRev != 0 && fromRev < s.compacted {
		w.err = store.ErrCompacted
		close(w.out)
		return w, nil
	}
	w.minRev = fromRev
	if fromRev == 0 {
		w.minRev = s.rev + 1
	}
	w.offer(s.log)
	s.watchers[w] = struct{}{}
	go w.pump(ctx)
	return w, nil
}

type watcher struct {
	store    *Store
	watchCtx context.Context
	prefix   string
	fromRev  store.Rev
	minRev   store.Rev
	out      chan store.Event
	wake     chan struct{}

	mu    sync.Mutex
	queue []store.Event
	err   error
}

// offer queues the events this watch selects. The caller holds the store lock,
// which is what keeps the queue in commit order.
func (w *watcher) offer(events []store.Event) {
	w.mu.Lock()
	for _, ev := range events {
		if ev.Type != store.EventProgress && (ev.Rev < w.minRev || !strings.HasPrefix(ev.Key, w.prefix)) {
			continue
		}
		ev.Value = cloneBytes(ev.Value)
		w.queue = append(w.queue, ev)
	}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *watcher) pump(ctx context.Context) {
	defer func() {
		w.store.mu.Lock()
		w.mu.Lock()
		w.err = ctx.Err()
		w.queue = nil
		w.mu.Unlock()
		delete(w.store.watchers, w)
		w.store.mu.Unlock()
		close(w.out)
	}()
	for {
		w.mu.Lock()
		batch := w.queue
		w.queue = nil
		w.mu.Unlock()
		for _, ev := range batch {
			select {
			case w.out <- ev:
			case <-ctx.Done():
				return
			}
		}
		if len(batch) > 0 {
			continue
		}
		select {
		case <-w.wake:
		case <-ctx.Done():
			return
		}
	}
}

func (w *watcher) Events() <-chan store.Event { return w.out }

func (w *watcher) RequestProgress(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	if _, live := w.store.watchers[w]; !live {
		if err := w.Err(); err != nil {
			return err
		}
		return context.Canceled
	}
	if err := w.watchCtx.Err(); err != nil {
		return err
	}
	if w.store.rev >= w.fromRev {
		w.offer([]store.Event{{Type: store.EventProgress, Rev: w.store.rev}})
	}
	return nil
}

func (w *watcher) Err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}
