package resource

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
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

const (
	placeholderTenant   = "t"
	deleteBackRefBatch  = 64
	defaultListPageSize = 512
)

// Config configures clock, randomness, tenant identity, per-kind LKAL v0.2 schemas, and authorization for an Objects handle.
// Rand is read from every goroutine that calls Create and must be safe for concurrent use (crypto/rand.Reader is).
type Config struct {
	Now        func() time.Time // defaults to time.Now
	Rand       io.Reader        // defaults to crypto/rand.Reader
	Tenant     string
	Schemas    Schemas
	Authorizer refs.Authorizer
}

// Objects reads and writes envelopes under the object keys of one tenant view.
type Objects struct {
	st         tenant.Store
	now        func() time.Time
	rand       io.Reader
	tenant     string
	schemas    Schemas
	references refs.Schema
	schemaErr  error
	authorizer refs.Authorizer
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
	clonedSchemas := maps.Clone(cfg.Schemas)
	derivedRefs, schemaErr := validateSchemasMap(clonedSchemas)
	return &Objects{
		st:         st,
		now:        nowFn,
		rand:       randReader,
		tenant:     cfg.Tenant,
		schemas:    clonedSchemas,
		references: derivedRefs,
		schemaErr:  schemaErr,
		authorizer: cfg.Authorizer,
	}
}

func (o *Objects) lookupKindSchema(obj keyspace.Object) (*KindSchema, error) {
	if o.schemaErr != nil {
		return nil, &refs.Error{
			Kind:   refs.ErrInternal,
			Detail: o.schemaErr.Error(),
		}
	}
	qkind, err := keyspace.QualifiedKind(obj.Group, obj.Kind)
	if err != nil {
		return nil, &envelope.Error{Kind: envelope.ErrMalformed, Path: "meta/kind", Detail: err.Error()}
	}
	ks := o.schemas[qkind]
	if ks == nil {
		return nil, &envelope.Error{
			Kind:   envelope.ErrMalformed,
			Path:   "meta/kind",
			Detail: fmt.Sprintf("unknown resource kind %q", qkind),
		}
	}
	return ks, nil
}

func objectKey(obj keyspace.Object) (string, error) {
	return (keyspace.Key{
		Family: keyspace.FamilyObject,
		Tenant: placeholderTenant,
		Object: obj,
	}).Rel()
}

func backRefKey(target, source keyspace.Object) (string, error) {
	return (keyspace.Key{
		Family: keyspace.FamilyBackRef,
		Tenant: placeholderTenant,
		Object: target,
		Source: source,
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

// DecodeObject validates a stored object value against obj and stored envelope invariants,
// returning the decoded envelope with meta/revision set to modRev.
func DecodeObject(obj keyspace.Object, value []byte, modRev store.Rev) (lkal.Record, error) {
	key, err := objectKey(obj)
	if err != nil {
		return lkal.Record{}, err
	}
	if modRev < 1 {
		return lkal.Record{}, fmt.Errorf("%w: %s: mod revision %d must be >= 1", ErrCorrupt, key, modRev)
	}
	doc, err := decodeStored(obj, key, value)
	if err != nil {
		return lkal.Record{}, err
	}
	return envelope.WithRevision(doc, int64(modRev)), nil
}

func decodeStored(obj keyspace.Object, key string, value []byte) (lkal.Record, error) {
	doc, err := Decode(value)
	if err != nil {
		return lkal.Record{}, fmt.Errorf("%w: %s: %v", ErrCorrupt, key, err)
	}
	if err := envelope.CheckStored(doc); err != nil {
		return lkal.Record{}, fmt.Errorf("%w: %s: %v", ErrCorrupt, key, err)
	}
	gotObj, err := envelope.Object(doc)
	if err != nil || gotObj != obj {
		return lkal.Record{}, fmt.Errorf("%w: %s: stored identity %v does not match key", ErrCorrupt, key, gotObj)
	}
	return doc, nil
}

func (o *Objects) readStored(ctx context.Context, obj keyspace.Object, key string) (lkal.Record, store.Rev, error) {
	kv, _, err := o.st.Get(ctx, key, 0)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return lkal.Record{}, 0, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return lkal.Record{}, 0, fmt.Errorf("get %s: %w", key, err)
	}
	doc, err := decodeStored(obj, key, kv.Value)
	if err != nil {
		return lkal.Record{}, 0, err
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

type targetState uint8

const (
	targetAbsent targetState = iota
	targetPresent
	targetUnreadable
)

type targetInfo struct {
	key    string
	state  targetState
	modRev store.Rev
	labels map[string]string
	err    error
}

type authCacheKey struct {
	path   string
	target keyspace.Object
}

func extractLabels(doc lkal.Record) map[string]string {
	labels := make(map[string]string)
	for _, f := range doc.Fields {
		if f.Key != "meta" {
			continue
		}
		meta, ok := f.Value.(lkal.Record)
		if !ok {
			return labels
		}
		for _, mf := range meta.Fields {
			if mf.Key != "labels" {
				continue
			}
			lrec, ok := mf.Value.(lkal.Record)
			if !ok {
				return labels
			}
			for _, lf := range lrec.Fields {
				if s, ok := lf.Value.(lkal.String); ok {
					labels[lf.Key] = string(s)
				}
			}
			return labels
		}
		return labels
	}
	return labels
}

func (o *Objects) readTargets(ctx context.Context, targets []keyspace.Object) (map[keyspace.Object]targetInfo, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	infos := make(map[keyspace.Object]targetInfo, len(targets))
	for _, target := range targets {
		key, err := objectKey(target)
		if err != nil {
			return nil, err
		}
		kv, _, err := o.st.Get(ctx, key, 0)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				infos[target] = targetInfo{key: key, state: targetAbsent}
				continue
			}
			return nil, fmt.Errorf("get target %s: %w", key, err)
		}
		doc, err := decodeStored(target, key, kv.Value)
		if err != nil {
			infos[target] = targetInfo{key: key, state: targetUnreadable, modRev: kv.ModRev, err: err}
			continue
		}
		infos[target] = targetInfo{
			key:    key,
			state:  targetPresent,
			modRev: kv.ModRev,
			labels: extractLabels(doc),
		}
	}
	return infos, nil
}

func (o *Objects) checkRefs(
	ctx context.Context,
	source keyspace.Object,
	principal refs.Principal,
	stored lkal.Record,
	hasStored bool,
	next lkal.Record,
) (refs.Delta, map[keyspace.Object]targetInfo, error) {
	if o.schemaErr != nil {
		return refs.Delta{}, nil, &refs.Error{
			Kind:   refs.ErrInternal,
			Detail: o.schemaErr.Error(),
		}
	}
	plan, err := refs.Prepare(refs.Input{
		Source:    source,
		Schema:    o.references,
		Stored:    stored,
		HasStored: hasStored,
		Next:      next,
	})
	if err != nil {
		return refs.Delta{}, nil, err
	}
	if o.authorizer != nil && len(plan.Added) > 0 {
		if err := keyspace.ValidateTenant(o.tenant); err != nil {
			return refs.Delta{}, nil, &refs.Error{
				Kind:   refs.ErrInternal,
				Detail: fmt.Sprintf("invalid tenant %q: %v", o.tenant, err),
			}
		}
	}
	infos, err := o.readTargets(ctx, plan.Targets)
	if err != nil {
		return refs.Delta{}, nil, err
	}

	authCache := make(map[authCacheKey]bool)
	authorize := func(occ refs.Occurrence) bool {
		if o.authorizer == nil || principal.Type == "" || principal.ID == "" {
			return false
		}
		parsed, err := refs.Parse(occ.Ref)
		if err != nil {
			return false
		}
		ck := authCacheKey{path: occ.Path, target: parsed.Object}
		if allowed, cached := authCache[ck]; cached {
			return allowed
		}
		info := infos[parsed.Object]
		targetExists := info.state == targetPresent
		var targetLabels map[string]string
		if targetExists {
			targetLabels = maps.Clone(info.labels)
		}
		allowed := o.authorizer.Authorize(refs.Request{
			Tenant:       o.tenant,
			Principal:    principal,
			Source:       source,
			SourcePath:   occ.Path,
			Target:       parsed.Object,
			TargetLabels: targetLabels,
			TargetExists: targetExists,
		})
		authCache[ck] = allowed
		return allowed
	}

	exists := func(obj keyspace.Object) bool {
		info := infos[obj]
		return info.state == targetPresent || info.state == targetUnreadable
	}

	delta, err := plan.Check(authorize, exists)
	if err != nil {
		return refs.Delta{}, nil, err
	}
	for _, target := range delta.Compare {
		if info := infos[target]; info.state == targetUnreadable {
			return refs.Delta{}, nil, &refs.Error{
				Kind:   refs.ErrInternal,
				Detail: fmt.Sprintf("unreadable target %s: %v", info.key, info.err),
			}
		}
	}
	return delta, infos, nil
}

func buildWriteTxn(
	key string,
	val []byte,
	baseCmp store.Cmp,
	source keyspace.Object,
	delta refs.Delta,
	infos map[keyspace.Object]targetInfo,
) ([]store.Cmp, []store.Op, error) {
	cmps := make([]store.Cmp, 0, 1+len(delta.Compare))
	cmps = append(cmps, baseCmp)
	for _, target := range delta.Compare {
		if target == source {
			continue
		}
		info := infos[target]
		cmps = append(cmps, store.Cmp{Key: info.key, Op: store.CmpEqual, ModRev: info.modRev})
	}

	ops := make([]store.Op, 0, 1+len(delta.Put)+len(delta.Delete))
	ops = append(ops, store.Op{Type: store.OpPut, Key: key, Value: val})
	for _, target := range delta.Put {
		brKey, err := backRefKey(target, source)
		if err != nil {
			return nil, nil, err
		}
		ops = append(ops, store.Op{Type: store.OpPut, Key: brKey, Value: []byte{}})
	}
	for _, target := range delta.Delete {
		brKey, err := backRefKey(target, source)
		if err != nil {
			return nil, nil, err
		}
		ops = append(ops, store.Op{Type: store.OpDelete, Key: brKey})
	}
	return cmps, ops, nil
}

// Create completes declared spec defaults, validates candidate against the kind's LKAL v0.2 schema,
// checks references, assigns a version-4 UUID and generation 1, and stores it if absent.
func (o *Objects) Create(ctx context.Context, principal refs.Principal, candidate lkal.Record) (lkal.Record, error) {
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
	ks, err := o.lookupKindSchema(obj)
	if err != nil {
		return lkal.Record{}, err
	}
	completed, err := ks.completeCandidate(canonical)
	if err != nil {
		return lkal.Record{}, err
	}
	uid, err := newUUIDv4(o.rand)
	if err != nil {
		return lkal.Record{}, err
	}
	doc, err := envelope.Create(completed, uid)
	if err != nil {
		return lkal.Record{}, err
	}
	if err := ks.validateSpec(doc); err != nil {
		return lkal.Record{}, err
	}
	delta, infos, err := o.checkRefs(ctx, obj, principal, lkal.Record{}, false, doc)
	if err != nil {
		return lkal.Record{}, err
	}
	val, err := Encode(doc)
	if err != nil {
		return lkal.Record{}, err
	}
	cmps, ops, err := buildWriteTxn(key, val, store.Cmp{Key: key, Op: store.CmpEqual, ModRev: 0}, obj, delta, infos)
	if err != nil {
		return lkal.Record{}, err
	}
	ok, rev, err := o.st.Txn(ctx, cmps, ops)
	if err != nil {
		return lkal.Record{}, fmt.Errorf("create %s: %w", key, err)
	}
	if !ok {
		if len(cmps) > 1 {
			if _, _, getErr := o.st.Get(ctx, key, 0); getErr != nil {
				if errors.Is(getErr, store.ErrNotFound) {
					return lkal.Record{}, &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
				}
				return lkal.Record{}, fmt.Errorf("create %s: %w", key, getErr)
			}
		}
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

// ListOptions configures a snapshot or paginated List call.
type ListOptions struct {
	AtRev    store.Rev
	Limit    int
	Continue string
}

// ListResult holds the decoded envelopes, snapshot revision, and pagination cursor of a List call.
type ListResult struct {
	Items    []lkal.Record
	Revision store.Rev
	Continue string
}

// List reads objects of (group, kind) from the store, decoding and validating each stored envelope.
// When opts.Limit == 0, List paginates all remaining pages at a single snapshot revision and returns Continue == "".
// When opts.Limit > 0, List returns a single page of up to opts.Limit items along with Continue if more items remain.
func (o *Objects) List(ctx context.Context, group, kind string, opts ListOptions) (ListResult, error) {
	prefix, err := keyspace.ObjectPrefix(group, kind)
	if err != nil {
		path := "meta/kind"
		if keyspace.ValidateGroup(group) != nil {
			path = "meta/group"
		}
		return ListResult{}, &envelope.Error{Kind: envelope.ErrMalformed, Path: path, Detail: err.Error()}
	}
	if err := store.ValidateRange(prefix, opts.AtRev, opts.Limit, opts.Continue); err != nil {
		return ListResult{}, err
	}

	var (
		kvs  []store.KV
		rev  store.Rev
		cont string
	)
	if opts.Limit > 0 {
		kvs, rev, cont, err = o.st.Range(ctx, prefix, opts.AtRev, opts.Limit, opts.Continue)
		if err != nil {
			return ListResult{}, fmt.Errorf("list %s: %w", prefix, err)
		}
	} else {
		page, snapRev, nextCont, err := o.st.Range(ctx, prefix, opts.AtRev, defaultListPageSize, opts.Continue)
		if err != nil {
			return ListResult{}, fmt.Errorf("list %s: %w", prefix, err)
		}
		rev = snapRev
		kvs = append(kvs, page...)
		for nextCont != "" {
			page, _, nextCont, err = o.st.Range(ctx, prefix, rev, defaultListPageSize, nextCont)
			if err != nil {
				return ListResult{}, fmt.Errorf("list %s: %w", prefix, err)
			}
			kvs = append(kvs, page...)
		}
	}

	items := make([]lkal.Record, 0, len(kvs))
	for _, kv := range kvs {
		parsed, err := keyspace.ParseRel(placeholderTenant, kv.Key)
		if err != nil || parsed.Family != keyspace.FamilyObject || parsed.Object.Group != group || parsed.Object.Kind != kind {
			return ListResult{}, fmt.Errorf("%w: unexpected key %q under prefix %q", ErrCorrupt, kv.Key, prefix)
		}
		doc, err := DecodeObject(parsed.Object, kv.Value, kv.ModRev)
		if err != nil {
			return ListResult{}, err
		}
		items = append(items, doc)
	}
	return ListResult{
		Items:    items,
		Revision: rev,
		Continue: cont,
	}, nil
}

// Update completes declared spec defaults, validates and applies a spec or labels change to an existing object using optimistic concurrency.
func (o *Objects) Update(ctx context.Context, principal refs.Principal, candidate lkal.Record) (lkal.Record, error) {
	return o.applyUpdate(
		ctx,
		principal,
		candidate,
		func(ks *KindSchema, canonical lkal.Record) (lkal.Record, error) {
			return ks.completeCandidate(canonical)
		},
		envelope.Update,
		func(ks *KindSchema, next lkal.Record) error {
			return ks.validateSpec(next)
		},
	)
}

// UpdateStatus validates and applies a status change to an existing object using optimistic concurrency.
func (o *Objects) UpdateStatus(ctx context.Context, candidate lkal.Record) (lkal.Record, error) {
	return o.applyUpdate(
		ctx,
		refs.Principal{},
		candidate,
		func(_ *KindSchema, canonical lkal.Record) (lkal.Record, error) {
			return canonical, nil
		},
		func(old lkal.Record, modRev int64, canonical lkal.Record) (lkal.Record, error) {
			return envelope.UpdateStatus(old, modRev, canonical, envelope.Timestamp(o.now()))
		},
		func(ks *KindSchema, next lkal.Record) error {
			return ks.validateStatus(next)
		},
	)
}

func (o *Objects) applyUpdate(
	ctx context.Context,
	principal refs.Principal,
	candidate lkal.Record,
	prepareCandidate func(ks *KindSchema, canonical lkal.Record) (lkal.Record, error),
	transition func(old lkal.Record, modRev int64, prepared lkal.Record) (lkal.Record, error),
	validateNext func(ks *KindSchema, next lkal.Record) error,
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
	ks, err := o.lookupKindSchema(obj)
	if err != nil {
		return lkal.Record{}, err
	}
	old, modRev, err := o.readStored(ctx, obj, key)
	if err != nil {
		return lkal.Record{}, err
	}
	if _, err := refs.DeleteDelta(old); err != nil {
		return lkal.Record{}, err
	}
	prepared, err := prepareCandidate(ks, canonical)
	if err != nil {
		return lkal.Record{}, err
	}
	next, err := transition(old, int64(modRev), prepared)
	if err != nil {
		return lkal.Record{}, err
	}
	if err := validateNext(ks, next); err != nil {
		return lkal.Record{}, err
	}
	delta, infos, err := o.checkRefs(ctx, obj, principal, old, true, next)
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
	cmps, ops, err := buildWriteTxn(key, val, store.Cmp{Key: key, Op: store.CmpEqual, ModRev: modRev}, obj, delta, infos)
	if err != nil {
		return lkal.Record{}, err
	}
	ok, newRev, err := o.st.Txn(ctx, cmps, ops)
	if err != nil {
		return lkal.Record{}, fmt.Errorf("update %s: %w", key, err)
	}
	if !ok {
		return lkal.Record{}, &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
	}
	return envelope.WithRevision(next, int64(newRev)), nil
}

// Patch applies an ordered patch sequence through the spec/labels entrance to obj,
// completing declared spec defaults, validating the kind schema and references, and committing with CAS.
func (o *Objects) Patch(ctx context.Context, principal refs.Principal, obj keyspace.Object, ops []patch.Op) (lkal.Record, error) {
	return o.applyPatch(
		ctx,
		principal,
		obj,
		patch.EntranceSpec,
		ops,
		func(ks *KindSchema, canonical lkal.Record) (lkal.Record, error) {
			return ks.completeCandidate(canonical)
		},
		envelope.Update,
		func(ks *KindSchema, next lkal.Record) error {
			return ks.validateSpec(next)
		},
	)
}

// PatchStatus applies an ordered patch sequence through the status entrance to obj,
// validating the status schema and committing with CAS.
func (o *Objects) PatchStatus(ctx context.Context, obj keyspace.Object, ops []patch.Op) (lkal.Record, error) {
	return o.applyPatch(
		ctx,
		refs.Principal{},
		obj,
		patch.EntranceStatus,
		ops,
		func(_ *KindSchema, canonical lkal.Record) (lkal.Record, error) {
			return canonical, nil
		},
		func(old lkal.Record, modRev int64, canonical lkal.Record) (lkal.Record, error) {
			return envelope.UpdateStatus(old, modRev, canonical, envelope.Timestamp(o.now()))
		},
		func(ks *KindSchema, next lkal.Record) error {
			return ks.validateStatus(next)
		},
	)
}

func (o *Objects) applyPatch(
	ctx context.Context,
	principal refs.Principal,
	obj keyspace.Object,
	entrance patch.Entrance,
	ops []patch.Op,
	prepareCandidate func(ks *KindSchema, canonical lkal.Record) (lkal.Record, error),
	transition func(old lkal.Record, modRev int64, prepared lkal.Record) (lkal.Record, error),
	validateNext func(ks *KindSchema, next lkal.Record) error,
) (lkal.Record, error) {
	key, err := objectKey(obj)
	if err != nil {
		return lkal.Record{}, &envelope.Error{Kind: envelope.ErrMalformed, Path: "meta", Detail: err.Error()}
	}
	encodedOps, err := patch.Encode(ops)
	if err != nil {
		return lkal.Record{}, asMalformedPatchErr(err)
	}
	canonicalOps, err := patch.Decode(encodedOps)
	if err != nil {
		return lkal.Record{}, asMalformedPatchErr(err)
	}
	ks, err := o.lookupKindSchema(obj)
	if err != nil {
		return lkal.Record{}, err
	}
	old, modRev, err := o.readStored(ctx, obj, key)
	if err != nil {
		return lkal.Record{}, err
	}
	if _, err := refs.DeleteDelta(old); err != nil {
		return lkal.Record{}, err
	}
	patched, err := patch.ApplyEntrance(entrance, old, int64(modRev), canonicalOps)
	if err != nil {
		return lkal.Record{}, err
	}
	canonical, err := canonicalize(patched)
	if err != nil {
		return lkal.Record{}, err
	}
	prepared, err := prepareCandidate(ks, canonical)
	if err != nil {
		return lkal.Record{}, err
	}
	next, err := transition(old, int64(modRev), prepared)
	if err != nil {
		return lkal.Record{}, err
	}
	if err := validateNext(ks, next); err != nil {
		return lkal.Record{}, err
	}
	delta, infos, err := o.checkRefs(ctx, obj, principal, old, true, next)
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
	cmps, storeOps, err := buildWriteTxn(key, val, store.Cmp{Key: key, Op: store.CmpEqual, ModRev: modRev}, obj, delta, infos)
	if err != nil {
		return lkal.Record{}, err
	}
	ok, newRev, err := o.st.Txn(ctx, cmps, storeOps)
	if err != nil {
		return lkal.Record{}, fmt.Errorf("patch %s: %w", key, err)
	}
	if !ok {
		return lkal.Record{}, &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
	}
	return envelope.WithRevision(next, int64(newRev)), nil
}

func asMalformedPatchErr(err error) error {
	var pe *patch.Error
	if errors.As(err, &pe) && pe.Kind == patch.Malformed {
		return pe
	}
	return &patch.Error{Kind: patch.Malformed, Detail: err.Error()}
}

// Delete removes obj and its back-reference keys from the store, requiring its current mod revision to equal rev when rev != 0.
// Unreadable stored values or invalid reference metadata fall back to scanning the tenant back-reference area.
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
	bodyCmp := store.Cmp{Key: key, Op: store.CmpEqual, ModRev: kv.ModRev}

	if stored, err := decodeStored(obj, key, kv.Value); err == nil {
		if delta, err := refs.DeleteDelta(stored); err == nil {
			return o.deleteWithDelta(ctx, obj, key, bodyCmp, delta)
		}
	}
	return o.deleteScanningBackRefs(ctx, obj, key, bodyCmp)
}

func (o *Objects) deleteWithDelta(ctx context.Context, obj keyspace.Object, key string, bodyCmp store.Cmp, delta refs.Delta) error {
	ops := make([]store.Op, 0, 1+len(delta.Delete))
	ops = append(ops, store.Op{Type: store.OpDelete, Key: key})
	for _, target := range delta.Delete {
		brKey, err := backRefKey(target, obj)
		if err != nil {
			return err
		}
		ops = append(ops, store.Op{Type: store.OpDelete, Key: brKey})
	}
	ok, _, err := o.st.Txn(ctx, []store.Cmp{bodyCmp}, ops)
	if err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	if !ok {
		return &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
	}
	return nil
}

func (o *Objects) deleteScanningBackRefs(ctx context.Context, obj keyspace.Object, key string, bodyCmp store.Cmp) error {
	var refKeys []string
	var snapRev store.Rev
	cont := ""
	for {
		kvs, rev, next, err := o.st.Range(ctx, keyspace.BackRefsPrefix, snapRev, deleteBackRefBatch, cont)
		if err != nil {
			return fmt.Errorf("delete %s: %w", key, err)
		}
		if snapRev == 0 {
			snapRev = rev
		}
		for _, rkv := range kvs {
			parsed, err := keyspace.ParseRel(placeholderTenant, rkv.Key)
			if err == nil && parsed.Family == keyspace.FamilyBackRef && parsed.Source == obj {
				refKeys = append(refKeys, rkv.Key)
			}
		}
		if next == "" {
			break
		}
		cont = next
	}

	cmps := []store.Cmp{bodyCmp}
	batchSize := deleteBackRefBatch
	for len(refKeys) > 0 {
		n := min(batchSize, len(refKeys))
		ops := make([]store.Op, n)
		for i := range n {
			ops[i] = store.Op{Type: store.OpDelete, Key: refKeys[i]}
		}
		ok, _, err := o.st.Txn(ctx, cmps, ops)
		if err != nil {
			if errors.Is(err, store.ErrTooLarge) && n > 1 {
				batchSize = n / 2
				continue
			}
			return fmt.Errorf("delete %s: %w", key, err)
		}
		if !ok {
			return &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
		}
		refKeys = refKeys[n:]
	}

	ok, _, err := o.st.Txn(ctx, cmps, []store.Op{{Type: store.OpDelete, Key: key}})
	if err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	if !ok {
		return &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
	}
	return nil
}
