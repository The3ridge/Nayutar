package storetest

import (
	"context"
	"strings"

	"github.com/Kaikei-e/Nayutar/core/store"
)

// Scope presents the keys of inner below keyPrefix as a store of their own,
// with every revision shifted down by revOffset. It lets tests share one
// long-lived store, such as an etcd cluster, and still see an empty store that
// starts at a known revision. The caller makes sure nothing else writes to
// inner meanwhile, because revisions and compaction stay global.
func Scope(inner store.Store, keyPrefix string, revOffset store.Rev) store.Store {
	return &scoped{inner: inner, prefix: keyPrefix, offset: revOffset}
}

type scoped struct {
	inner  store.Store
	prefix string
	offset store.Rev
}

func (s *scoped) keyIn(key string) string {
	if key == "" {
		return ""
	}
	return s.prefix + key
}

func (s *scoped) keyOut(key string) string {
	return strings.TrimPrefix(key, s.prefix)
}

func (s *scoped) revIn(rev store.Rev) store.Rev {
	if rev <= 0 {
		return rev
	}
	return rev + s.offset
}

func (s *scoped) revOut(rev store.Rev) store.Rev {
	if rev <= 0 {
		return rev
	}
	return rev - s.offset
}

func (s *scoped) kvOut(kv store.KV) store.KV {
	kv.Key = s.keyOut(kv.Key)
	kv.ModRev = s.revOut(kv.ModRev)
	return kv
}

func (s *scoped) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	kv, rev, err := s.inner.Get(ctx, s.keyIn(key), s.revIn(atRev))
	if err != nil {
		return store.KV{}, s.revOut(rev), err
	}
	return s.kvOut(kv), s.revOut(rev), nil
}

func (s *scoped) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	kvs, rev, next, err := s.inner.Range(ctx, s.prefix+prefix, s.revIn(atRev), limit, s.keyIn(cont))
	if err != nil {
		return nil, 0, "", err
	}
	for i := range kvs {
		kvs[i] = s.kvOut(kvs[i])
	}
	return kvs, s.revOut(rev), s.keyOut(next), nil
}

func (s *scoped) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	innerCmps := make([]store.Cmp, len(cmps))
	for i, c := range cmps {
		innerCmps[i] = store.Cmp{Key: s.keyIn(c.Key), Op: c.Op, ModRev: s.revIn(c.ModRev)}
	}
	innerOps := make([]store.Op, len(ops))
	for i, op := range ops {
		innerOps[i] = store.Op{Type: op.Type, Key: s.keyIn(op.Key), Value: op.Value}
	}
	ok, rev, err := s.inner.Txn(ctx, innerCmps, innerOps)
	return ok, s.revOut(rev), err
}

func (s *scoped) CurrentRev(ctx context.Context) (store.Rev, error) {
	rev, err := s.inner.CurrentRev(ctx)
	return s.revOut(rev), err
}

func (s *scoped) Compact(ctx context.Context, rev store.Rev) error {
	return s.inner.Compact(ctx, s.revIn(rev))
}

func (s *scoped) Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
	inner, err := s.inner.Watch(ctx, s.prefix+prefix, s.revIn(fromRev))
	if err != nil {
		return nil, err
	}
	ws := &scopedWatch{WatchStream: inner, out: make(chan store.Event)}
	go func() {
		defer close(ws.out)
		for ev := range inner.Events() {
			ev.Key = s.keyOut(ev.Key)
			ev.Rev = s.revOut(ev.Rev)
			select {
			case ws.out <- ev:
			case <-ctx.Done():
				for range inner.Events() {
				}
				return
			}
		}
	}()
	return ws, nil
}

type scopedWatch struct {
	store.WatchStream
	out chan store.Event
}

func (w *scopedWatch) Events() <-chan store.Event { return w.out }
