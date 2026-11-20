package window

import (
	"slices"
	"strings"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
)

type refEntry struct {
	ev store.Event
	at time.Time
}

type refWatcher struct {
	prefix string
	next   int
}

type refCache struct {
	cfg     Config
	initial []store.KV
	log     []refEntry
	start   int
	floor   store.Rev
	rev     store.Rev
	isOpen  bool
}

func newRefCache(cfg Config, listRev store.Rev, kvs []store.KV) *refCache {
	initKVs := make([]store.KV, len(kvs))
	copy(initKVs, kvs)
	return &refCache{
		cfg:     cfg,
		initial: initKVs,
		floor:   listRev,
		rev:     listRev,
	}
}

func (c *refCache) retainedLen() int {
	return len(c.log) - c.start
}

func (c *refCache) evictOne(now time.Time) bool {
	if c.start >= len(c.log) {
		return false
	}
	first := c.log[c.start]
	rest := c.log[c.start+1:]
	r := first.ev.Rev
	if c.rev < r {
		return false
	}
	groupLen := 1
	newest := first.at
	for _, e := range rest {
		if e.ev.Rev != r {
			break
		}
		groupLen++
		newest = e.at
	}
	over := c.cfg.MaxEvents < len(rest)+1
	age := now.Sub(newest)
	if age < 0 {
		age = 0
	}
	old := c.cfg.MinEvents+groupLen <= len(rest)+1 && c.cfg.MinAge < age
	if over || old {
		c.start += groupLen
		c.floor = r
		return true
	}
	return false
}

func (c *refCache) evict(now time.Time) {
	fuel := len(c.log)
	for range fuel {
		if !c.evictOne(now) {
			return
		}
	}
}

func (c *refCache) feedEvent(ev store.Event, now time.Time) error {
	if ev.Type != store.EventPut && ev.Type != store.EventDelete {
		return ErrOutOfOrder
	}
	if ev.Rev <= 0 || ev.Rev <= c.rev {
		return ErrOutOfOrder
	}
	stored := store.Event{Type: ev.Type, Rev: ev.Rev, Key: ev.Key}
	if ev.Type == store.EventPut {
		stored.Value = ev.Value
	}
	c.log = append(c.log, refEntry{ev: stored, at: now})
	c.rev = ev.Rev - 1
	c.isOpen = true
	c.evict(now)
	return nil
}

func (c *refCache) feedProgress(rev store.Rev) error {
	minRev := c.rev
	if c.isOpen {
		minRev = c.rev + 1
	}
	if rev < minRev {
		return ErrOutOfOrder
	}
	c.rev = rev
	c.isOpen = false
	return nil
}

func (c *refCache) findAt(r store.Rev, key string) (store.KV, bool) {
	var cur store.KV
	found := false
	for _, kv := range c.initial {
		if kv.Key == key {
			cur = kv
			found = true
			break
		}
	}
	for _, e := range c.log {
		if e.ev.Rev > r {
			continue
		}
		if e.ev.Key == key {
			if e.ev.Type == store.EventPut {
				cur = store.KV{Key: e.ev.Key, Value: e.ev.Value, ModRev: e.ev.Rev}
				found = true
			} else {
				cur = store.KV{}
				found = false
			}
		}
	}
	return cur, found
}

func (c *refCache) readRev(atRev store.Rev) (store.Rev, error) {
	if atRev < 0 {
		return 0, store.ErrInvalidArgument
	}
	if atRev == 0 {
		return c.rev, nil
	}
	if c.rev < atRev {
		return 0, ErrNotYet
	}
	if atRev < c.floor {
		return 0, ErrTooOld
	}
	return atRev, nil
}

func (c *refCache) get(key string, atRev store.Rev) (store.KV, store.Rev, error) {
	if key == "" {
		return store.KV{}, 0, store.ErrInvalidArgument
	}
	r, err := c.readRev(atRev)
	if err != nil {
		return store.KV{}, 0, err
	}
	kv, ok := c.findAt(r, key)
	if !ok {
		return store.KV{}, r, store.ErrNotFound
	}
	return kv, r, nil
}

func (c *refCache) knownKeys() []string {
	var keys []string
	add := func(k string) {
		if !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	for _, kv := range c.initial {
		add(kv.Key)
	}
	for _, e := range c.log {
		add(e.ev.Key)
	}
	return keys
}

func (c *refCache) listAt(r store.Rev, prefix, cont string) []store.KV {
	var keys []string
	for _, k := range c.knownKeys() {
		if strings.HasPrefix(k, prefix) && k > cont {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var out []store.KV
	for _, k := range keys {
		if kv, ok := c.findAt(r, k); ok {
			out = append(out, kv)
		}
	}
	return out
}

func (c *refCache) rangeAt(prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	if limit < 0 || atRev < 0 || (cont != "" && !strings.HasPrefix(cont, prefix)) {
		return nil, 0, "", store.ErrInvalidArgument
	}
	r, err := c.readRev(atRev)
	if err != nil {
		return nil, 0, "", err
	}
	all := c.listAt(r, prefix, cont)
	if limit != 0 && len(all) > limit {
		page := all[:limit]
		next := page[len(page)-1].Key
		return page, r, next, nil
	}
	return all, r, "", nil
}

func (c *refCache) watch(prefix string, fromRev store.Rev) (*refWatcher, error) {
	if fromRev < 0 {
		return nil, store.ErrInvalidArgument
	}
	first := fromRev
	if fromRev == 0 {
		first = c.rev + 1
	}
	if first <= c.floor {
		return nil, ErrTooOld
	}
	if c.rev+1 < first {
		return nil, ErrNotYet
	}
	next := 0
	for next < len(c.log) && c.log[next].ev.Rev < first {
		next++
	}
	return &refWatcher{prefix: prefix, next: next}, nil
}

func (c *refCache) watchList(prefix string, minRev store.Rev) ([]store.KV, store.Rev, *refWatcher, error) {
	if minRev < 0 {
		return nil, 0, nil, store.ErrInvalidArgument
	}
	if c.rev < minRev {
		return nil, 0, nil, ErrNotYet
	}
	next := 0
	for next < len(c.log) && c.log[next].ev.Rev <= c.rev {
		next++
	}
	return c.listAt(c.rev, prefix, ""), c.rev, &refWatcher{prefix: prefix, next: next}, nil
}

func (c *refCache) poll(w *refWatcher, limit int) ([]store.Event, error) {
	if limit < 0 || w == nil {
		return nil, store.ErrInvalidArgument
	}
	if w.next < c.start {
		return nil, ErrTooOld
	}
	rest := c.log[w.next:]
	taken := rest
	if limit != 0 && len(taken) > limit {
		taken = taken[:limit]
	}
	var out []store.Event
	for _, e := range taken {
		if strings.HasPrefix(e.ev.Key, w.prefix) {
			out = append(out, e.ev)
		}
	}
	w.next += len(taken)
	return out, nil
}

func (c *refCache) drained(w *refWatcher) bool {
	return w != nil && w.next == len(c.log)
}

func (c *refCache) bookmark(w *refWatcher) (store.Rev, bool) {
	if w != nil && w.next == len(c.log) && !c.isOpen {
		return c.rev, true
	}
	return 0, false
}
