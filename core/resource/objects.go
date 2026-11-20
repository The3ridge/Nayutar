package resource

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
)

var (
	// ErrNotFound is returned when the requested resource object does not exist in the store.
	ErrNotFound = errors.New("resource: not found")
	// ErrExists is returned by Create when an object with the same identity already exists.
	ErrExists = errors.New("resource: already exists")
	// ErrCorrupt is returned when a stored value cannot be decoded or violates stored envelope invariants.
	ErrCorrupt = errors.New("resource: stored value is not a valid envelope")
)

const placeholderTenant = "t"

// Config configures clock and randomness sources for an Objects handle.
// Rand is read from every goroutine that calls Create and must be safe for concurrent use (crypto/rand.Reader is).
type Config struct {
	Now  func() time.Time // defaults to time.Now
	Rand io.Reader        // defaults to crypto/rand.Reader
}

// Objects reads and writes envelopes under the object keys of one tenant view.
type Objects struct {
	st   tenant.Store
	now  func() time.Time
	rand io.Reader
}

// NewObjects binds a tenant-scoped store (a tenant view or a registry view; keys are tenant-relative).
func NewObjects(st tenant.Store, cfg Config) *Objects {
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	randReader := cfg.Rand
	if randReader == nil {
		randReader = rand.Reader
	}
	return &Objects{
		st:   st,
		now:  nowFn,
		rand: randReader,
	}
}

func objectKey(obj keyspace.Object) (string, error) {
	return (keyspace.Key{
		Family: keyspace.FamilyObject,
		Tenant: placeholderTenant,
		Object: obj,
	}).Rel()
}

func newUUIDv4(r io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func (o *Objects) readStored(ctx context.Context, obj keyspace.Object, key string) (lkal.Record, store.Rev, error) {
	kv, _, err := o.st.Get(ctx, key, 0)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return lkal.Record{}, 0, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return lkal.Record{}, 0, fmt.Errorf("get %s: %w", key, err)
	}
	doc, err := Decode(kv.Value)
	if err != nil {
		return lkal.Record{}, 0, fmt.Errorf("%w: %s: %v", ErrCorrupt, key, err)
	}
	if err := envelope.CheckStored(doc); err != nil {
		return lkal.Record{}, 0, fmt.Errorf("%w: %s: %v", ErrCorrupt, key, err)
	}
	gotObj, err := envelope.Object(doc)
	if err != nil || gotObj != obj {
		return lkal.Record{}, 0, fmt.Errorf("%w: %s: stored identity %v does not match key", ErrCorrupt, key, gotObj)
	}
	return doc, kv.ModRev, nil
}

func canonicalize(candidate lkal.Record) (lkal.Record, error) {
	encoded, err := Encode(candidate)
	if err != nil {
		return lkal.Record{}, &envelope.Error{Kind: envelope.ErrMalformed, Detail: err.Error()}
	}
	canonical, err := Decode(encoded)
	if err != nil {
		return lkal.Record{}, &envelope.Error{Kind: envelope.ErrMalformed, Detail: err.Error()}
	}
	return canonical, nil
}

// Create validates candidate, assigns a version-4 UUID and generation 1, and stores it if absent.
func (o *Objects) Create(ctx context.Context, candidate lkal.Record) (lkal.Record, error) {
	obj, err := envelope.Object(candidate)
	if err != nil {
		return lkal.Record{}, err
	}
	key, err := objectKey(obj)
	if err != nil {
		return lkal.Record{}, err
	}
	canonical, err := canonicalize(candidate)
	if err != nil {
		return lkal.Record{}, err
	}
	uid, err := newUUIDv4(o.rand)
	if err != nil {
		return lkal.Record{}, err
	}
	doc, err := envelope.Create(canonical, uid)
	if err != nil {
		return lkal.Record{}, err
	}
	val, err := Encode(doc)
	if err != nil {
		return lkal.Record{}, err
	}
	ok, rev, err := o.st.Txn(ctx,
		[]store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: 0}},
		[]store.Op{{Type: store.OpPut, Key: key, Value: val}},
	)
	if err != nil {
		return lkal.Record{}, fmt.Errorf("create %s: %w", key, err)
	}
	if !ok {
		return lkal.Record{}, fmt.Errorf("%w: %s", ErrExists, key)
	}
	return envelope.WithRevision(doc, int64(rev)), nil
}

// Get reads the stored envelope for obj and attaches its current mod revision.
func (o *Objects) Get(ctx context.Context, obj keyspace.Object) (lkal.Record, error) {
	key, err := objectKey(obj)
	if err != nil {
		return lkal.Record{}, err
	}
	doc, modRev, err := o.readStored(ctx, obj, key)
	if err != nil {
		return lkal.Record{}, err
	}
	return envelope.WithRevision(doc, int64(modRev)), nil
}

// Update validates and applies a spec or labels change to an existing object using optimistic concurrency.
func (o *Objects) Update(ctx context.Context, candidate lkal.Record) (lkal.Record, error) {
	return o.applyUpdate(ctx, candidate, envelope.Update)
}

// UpdateStatus validates and applies a status change to an existing object using optimistic concurrency.
func (o *Objects) UpdateStatus(ctx context.Context, candidate lkal.Record) (lkal.Record, error) {
	return o.applyUpdate(ctx, candidate, func(old lkal.Record, modRev int64, canonical lkal.Record) (lkal.Record, error) {
		return envelope.UpdateStatus(old, modRev, canonical, envelope.Timestamp(o.now()))
	})
}

func (o *Objects) applyUpdate(
	ctx context.Context,
	candidate lkal.Record,
	transition func(old lkal.Record, modRev int64, canonical lkal.Record) (lkal.Record, error),
) (lkal.Record, error) {
	obj, err := envelope.Object(candidate)
	if err != nil {
		return lkal.Record{}, err
	}
	key, err := objectKey(obj)
	if err != nil {
		return lkal.Record{}, err
	}
	canonical, err := canonicalize(candidate)
	if err != nil {
		return lkal.Record{}, err
	}
	old, modRev, err := o.readStored(ctx, obj, key)
	if err != nil {
		return lkal.Record{}, err
	}
	next, err := transition(old, int64(modRev), canonical)
	if err != nil {
		return lkal.Record{}, err
	}
	if lkal.Equal(next, old) {
		return envelope.WithRevision(old, int64(modRev)), nil
	}
	val, err := Encode(next)
	if err != nil {
		return lkal.Record{}, err
	}
	ok, newRev, err := o.st.Txn(ctx,
		[]store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: modRev}},
		[]store.Op{{Type: store.OpPut, Key: key, Value: val}},
	)
	if err != nil {
		return lkal.Record{}, fmt.Errorf("update %s: %w", key, err)
	}
	if !ok {
		return lkal.Record{}, &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
	}
	return envelope.WithRevision(next, int64(newRev)), nil
}

// Delete removes obj from the store, requiring its current mod revision to equal rev when rev != 0.
// It reads the key without decoding its value so corrupt entries can still be removed.
func (o *Objects) Delete(ctx context.Context, obj keyspace.Object, rev int64) error {
	key, err := objectKey(obj)
	if err != nil {
		return err
	}
	kv, _, err := o.st.Get(ctx, key, 0)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return fmt.Errorf("delete %s: %w", key, err)
	}
	if rev != 0 && rev != int64(kv.ModRev) {
		return &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "revision mismatch"}
	}
	ok, _, err := o.st.Txn(ctx,
		[]store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: kv.ModRev}},
		[]store.Op{{Type: store.OpDelete, Key: key}},
	)
	if err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	if !ok {
		return &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
	}
	return nil
}
