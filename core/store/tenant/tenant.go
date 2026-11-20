// Package tenant confines a store to a tenant key prefix and keeps the registry of tenants.
package tenant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

// Store is the part of the store contract a tenant view offers. Compaction is
// cluster-wide and is deliberately absent.
type Store interface {
	Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error)
	Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error)
	Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error)
	Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error)
	CurrentRev(ctx context.Context) (store.Rev, error)
}

// ErrInvalidName is returned when a tenant name is not a valid RFC 1123 label.
var ErrInvalidName = errors.New("tenant: invalid name")

// ValidateName checks that name is a lowercase RFC 1123 label: 1..63 bytes of
// [a-z0-9-] with alphanumeric first and last bytes.
func ValidateName(name string) error {
	if err := keyspace.ValidateTenant(name); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidName, err)
	}
	return nil
}

// Prefix returns the tenant key prefix "/t/<name>/".
func Prefix(name string) (string, error) {
	prefix, err := keyspace.TenantPrefix(name)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidName, err)
	}
	return prefix, nil
}

// View returns a store view confined to the prefix of tenant name.
func View(inner Store, name string) (Store, error) {
	prefix, err := Prefix(name)
	if err != nil {
		return nil, err
	}
	return &view{inner: inner, prefix: prefix}, nil
}

type view struct {
	inner       Store
	prefix      string
	fenceKey    string
	fenceModRev store.Rev
}

func (v *view) keyIn(key string) string {
	if key == "" {
		return ""
	}
	return v.prefix + key
}

func (v *view) keyOut(key string) string {
	return strings.TrimPrefix(key, v.prefix)
}

func (v *view) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	kv, rev, err := v.inner.Get(ctx, v.keyIn(key), atRev)
	if err != nil {
		return store.KV{}, rev, err
	}
	kv.Key = v.keyOut(kv.Key)
	return kv, rev, nil
}

func (v *view) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	kvs, rev, next, err := v.inner.Range(ctx, v.prefix+prefix, atRev, limit, v.keyIn(cont))
	if err != nil {
		return nil, 0, "", err
	}
	for i := range kvs {
		kvs[i].Key = v.keyOut(kvs[i].Key)
	}
	return kvs, rev, v.keyOut(next), nil
}

func (v *view) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	innerCmps := make([]store.Cmp, len(cmps), len(cmps)+1)
	for i, c := range cmps {
		innerCmps[i] = store.Cmp{Key: v.keyIn(c.Key), Op: c.Op, ModRev: c.ModRev}
	}
	if v.fenceKey != "" {
		innerCmps = append(innerCmps, store.Cmp{Key: v.fenceKey, Op: store.CmpEqual, ModRev: v.fenceModRev})
	}
	innerOps := make([]store.Op, len(ops))
	for i, op := range ops {
		innerOps[i] = store.Op{Type: op.Type, Key: v.keyIn(op.Key), Value: op.Value}
	}
	ok, rev, err := v.inner.Txn(ctx, innerCmps, innerOps)
	if err != nil || ok || v.fenceKey == "" {
		return ok, rev, err
	}
	kv, _, err := v.inner.Get(ctx, v.fenceKey, 0)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, 0, fmt.Errorf("%w: %s", ErrFenced, v.fenceKey)
		}
		return false, 0, err
	}
	if kv.ModRev != v.fenceModRev {
		return false, 0, fmt.Errorf("%w: %s mod revision %d differs from %d", ErrFenced, v.fenceKey, kv.ModRev, v.fenceModRev)
	}
	return false, rev, nil
}

func (v *view) CurrentRev(ctx context.Context) (store.Rev, error) {
	return v.inner.CurrentRev(ctx)
}

func (v *view) Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
	inner, err := v.inner.Watch(ctx, v.prefix+prefix, fromRev)
	if err != nil {
		return nil, err
	}
	ws := &watchStream{WatchStream: inner, out: make(chan store.Event)}
	go func() {
		defer close(ws.out)
		for ev := range inner.Events() {
			ev.Key = v.keyOut(ev.Key)
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

type watchStream struct {
	store.WatchStream
	out chan store.Event
}

func (w *watchStream) Events() <-chan store.Event { return w.out }
