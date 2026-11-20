package liveness

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Kaikei-e/Nayutar/core/liveness/budget"
	"github.com/Kaikei-e/Nayutar/core/liveness/detector"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

const listPage = 64

// Outcome is the result of a compare-and-swap write.
type Outcome uint8

const (
	// Committed indicates the compare-and-swap write succeeded.
	Committed Outcome = iota + 1
	// Conflict indicates the record or budget changed since it was read; nothing was written.
	Conflict
	// OverBudget indicates GrantEviction exceeded the sliding-window budget; nothing was written.
	OverBudget
	// Stale indicates the record is not in a state the write applies to; nothing was sent.
	Stale
)

// Registry manages cluster-wide node liveness records and the eviction budget in the store.
type Registry struct {
	root store.Store
}

// NewRegistry returns a Registry backed by the cluster-wide store root.
func NewRegistry(root store.Store) *Registry {
	return &Registry{root: root}
}

func nodeKey(node string) (string, error) {
	return (keyspace.Key{Family: keyspace.FamilyNode, Node: node}).Encode()
}

func budgetKey() (string, error) {
	return (keyspace.Key{Family: keyspace.FamilyEvictionBudget}).Encode()
}

// Register creates a pending liveness record for node in rack at wall time if absent.
func (r *Registry) Register(ctx context.Context, node, rack string, wall int64) (Record, error) {
	if err := validateWall(wall); err != nil {
		return Record{}, err
	}
	key, err := nodeKey(node)
	if err != nil {
		return Record{}, err
	}
	rec := Record{
		Node:      node,
		State:     detector.StatePending,
		Since:     wall,
		Rack:      rack,
		Evictable: false,
	}
	val, err := encodeRecord(rec)
	if err != nil {
		return Record{}, err
	}
	ok, rev, err := r.root.Txn(ctx,
		[]store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: 0}},
		[]store.Op{{Type: store.OpPut, Key: key, Value: val}},
	)
	if err != nil {
		return Record{}, err
	}
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrExists, node)
	}
	rec.ModRev = rev
	return rec, nil
}

// Get returns the current liveness record for node.
func (r *Registry) Get(ctx context.Context, node string) (Record, error) {
	key, err := nodeKey(node)
	if err != nil {
		return Record{}, err
	}
	kv, _, err := r.root.Get(ctx, key, 0)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Record{}, fmt.Errorf("%w: %s", ErrNotRegistered, node)
		}
		return Record{}, err
	}
	return DecodeRecord(node, kv.Value, kv.ModRev)
}

// List returns all node liveness records in ascending key order as of one snapshot revision.
func (r *Registry) List(ctx context.Context) ([]Record, store.Rev, error) {
	recs, rev, _, err := r.list(ctx, true, 0)
	return recs, rev, err
}

func (r *Registry) list(ctx context.Context, strict bool, perCallTimeout time.Duration) ([]Record, store.Rev, []error, error) {
	var records []Record
	var decodeErrs []error
	var snapRev store.Rev
	cont := ""
	for {
		rctx := ctx
		var cancel context.CancelFunc
		if perCallTimeout > 0 {
			rctx, cancel = context.WithTimeout(ctx, perCallTimeout)
		}
		kvs, rev, next, err := r.root.Range(rctx, keyspace.NodesPrefix, snapRev, listPage, cont)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			return nil, 0, decodeErrs, err
		}
		if snapRev == 0 {
			snapRev = rev
		}
		for _, kv := range kvs {
			parsed, err := keyspace.Parse(kv.Key)
			if err != nil || parsed.Family != keyspace.FamilyNode {
				if strict {
					return nil, 0, nil, fmt.Errorf("%w: invalid registry key %q", ErrCorruptRecord, kv.Key)
				}
				decodeErrs = append(decodeErrs, fmt.Errorf("list nodes: invalid registry key %q: %w", kv.Key, ErrCorruptRecord))
				continue
			}
			rec, err := DecodeRecord(parsed.Node, kv.Value, kv.ModRev)
			if err != nil {
				if strict {
					return nil, 0, nil, err
				}
				decodeErrs = append(decodeErrs, fmt.Errorf("list nodes: decode record for node %s: %w", parsed.Node, err))
				continue
			}
			records = append(records, rec)
		}
		if next == "" {
			return records, snapRev, decodeErrs, nil
		}
		cont = next
	}
}

// Delete removes the liveness record for node using compare-and-delete retries.
func (r *Registry) Delete(ctx context.Context, node string) error {
	key, err := nodeKey(node)
	if err != nil {
		return err
	}
	for {
		kv, _, err := r.root.Get(ctx, key, 0)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w: %s", ErrNotRegistered, node)
			}
			return err
		}
		ok, _, err := r.root.Txn(ctx,
			[]store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: kv.ModRev}},
			[]store.Op{{Type: store.OpDelete, Key: key}},
		)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
}

func (r *Registry) setState(ctx context.Context, rec Record, state detector.State, wall int64) (Outcome, error) {
	key, err := nodeKey(rec.Node)
	if err != nil {
		return 0, err
	}
	next := Record{
		Node:      rec.Node,
		State:     state,
		Since:     wall,
		Rack:      rec.Rack,
		Evictable: false,
	}
	val, err := encodeRecord(next)
	if err != nil {
		return 0, err
	}
	ok, _, err := r.root.Txn(ctx,
		[]store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: rec.ModRev}},
		[]store.Op{{Type: store.OpPut, Key: key, Value: val}},
	)
	if err != nil {
		return 0, err
	}
	if !ok {
		return Conflict, nil
	}
	return Committed, nil
}

// SetReady transitions a non-ready node record to Ready using compare-and-swap.
func (r *Registry) SetReady(ctx context.Context, rec Record, wall int64) (Outcome, error) {
	if err := validateWall(wall); err != nil {
		return 0, err
	}
	if rec.State == detector.StateReady {
		return Stale, nil
	}
	return r.setState(ctx, rec, detector.StateReady, wall)
}

// SetNotReady transitions a Ready node record to NotReady using compare-and-swap.
func (r *Registry) SetNotReady(ctx context.Context, rec Record, wall int64) (Outcome, error) {
	if err := validateWall(wall); err != nil {
		return 0, err
	}
	if rec.State != detector.StateReady {
		return Stale, nil
	}
	return r.setState(ctx, rec, detector.StateNotReady, wall)
}

// GrantEviction atomically marks a NotReady non-evictable node evictable and records the permit at max(wall, latest stored stamp) so slow wall clocks preserve the window.
func (r *Registry) GrantEviction(ctx context.Context, rec Record, wall int64, totalNodes int) (Outcome, error) {
	if err := validateWall(wall); err != nil {
		return 0, err
	}
	if rec.State != detector.StateNotReady || rec.Evictable {
		return Stale, nil
	}
	nKey, err := nodeKey(rec.Node)
	if err != nil {
		return 0, err
	}
	nextRec := rec
	nextRec.Evictable = true
	recVal, err := encodeRecord(nextRec)
	if err != nil {
		return 0, err
	}

	bKey, err := budgetKey()
	if err != nil {
		return 0, err
	}
	bKV, _, err := r.root.Get(ctx, bKey, 0)
	var granted []int64
	var bModRev store.Rev
	if err == nil {
		granted, err = decodeBudget(bKey, bKV.Value)
		if err != nil {
			return 0, err
		}
		bModRev = bKV.ModRev
	} else if !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}

	stamp := wall
	if len(granted) > 0 && granted[len(granted)-1] > stamp {
		stamp = granted[len(granted)-1]
	}

	nextGranted, withinBudget := budget.Grant(granted, stamp, EvictionWindow, budget.Limit(totalNodes, EvictionPercent))
	if !withinBudget {
		return OverBudget, nil
	}
	budgetVal, err := encodeBudget(nextGranted)
	if err != nil {
		return 0, err
	}

	ok, _, err := r.root.Txn(ctx,
		[]store.Cmp{
			{Key: nKey, Op: store.CmpEqual, ModRev: rec.ModRev},
			{Key: bKey, Op: store.CmpEqual, ModRev: bModRev},
		},
		[]store.Op{
			{Type: store.OpPut, Key: nKey, Value: recVal},
			{Type: store.OpPut, Key: bKey, Value: budgetVal},
		},
	)
	if err != nil {
		return 0, err
	}
	if !ok {
		return Conflict, nil
	}
	return Committed, nil
}
