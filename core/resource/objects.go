package resource

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"maps"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/ownership"
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
	anonymousActorID    = ownership.ActorID("system:anonymous")
)

// ScopeResolver binds an authenticated principal and target object to a server-side
// ownership actor ID and path permission scope.
type ScopeResolver func(principal refs.Principal, obj keyspace.Object) (ownership.ActorID, ownership.Scope, error)

// Config configures clock, randomness, tenant identity, per-kind LKAL v0.2 schemas,
// reference authorization, and leaf-path ownership scope resolution for an Objects handle.
// Rand is read from every goroutine that calls Create or Apply and must be safe for concurrent use (crypto/rand.Reader is).
type Config struct {
	Now           func() time.Time // defaults to time.Now
	Rand          io.Reader        // defaults to crypto/rand.Reader
	Tenant        string
	Schemas       Schemas
	Authorizer    refs.Authorizer
	ScopeResolver ScopeResolver
}

// Objects reads and writes envelopes and leaf-path ownership records under one tenant view.
type Objects struct {
	st            tenant.Store
	now           func() time.Time
	rand          io.Reader
	tenant        string
	schemas       Schemas
	references    refs.Schema
	schemaErr     error
	authorizer    refs.Authorizer
	scopeResolver ScopeResolver
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
		st:            st,
		now:           nowFn,
		rand:          randReader,
		tenant:        cfg.Tenant,
		schemas:       clonedSchemas,
		references:    derivedRefs,
		schemaErr:     schemaErr,
		authorizer:    cfg.Authorizer,
		scopeResolver: cfg.ScopeResolver,
	}
}

func (o *Objects) resolveOwnership(principal refs.Principal, obj keyspace.Object) (ownership.ActorID, ownership.Scope, error) {
	if o.scopeResolver != nil {
		actor, sc, err := o.scopeResolver(principal, obj)
		if err != nil {
			var oe *ownership.Error
			if errors.As(err, &oe) {
				return "", ownership.Scope{}, oe
			}
			return "", ownership.Scope{}, &ownership.Error{
				Code:   ownership.CodePolicyDenied,
				Detail: "ownership scope denied",
				Diagnostics: []ownership.Diagnostic{{
					Code:   ownership.CodePolicyDenied,
					Reason: "ownership scope denied",
				}},
			}
		}
		if err := ownership.ValidateActorID(actor); err != nil {
			return "", ownership.Scope{}, err
		}
		if err := sc.Validate(); err != nil {
			return "", ownership.Scope{}, err
		}
		return actor, sc, nil
	}
	actor := anonymousActorID
	if principal.Type != "" && principal.ID != "" {
		actor = ownership.ActorID(principal.Type + ":" + principal.ID)
	}
	if err := ownership.ValidateActorID(actor); err != nil {
		return "", ownership.Scope{}, err
	}
	return actor, ownership.RootScope(ownership.PermRegular), nil
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

func ownershipKey(obj keyspace.Object) (string, error) {
	return (keyspace.Key{
		Family: keyspace.FamilyOwnership,
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

func (o *Objects) readStored(ctx context.Context, obj keyspace.Object, key string, atRev store.Rev) (lkal.Record, store.Rev, store.Rev, error) {
	kv, snapRev, err := o.st.Get(ctx, key, atRev)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return lkal.Record{}, 0, snapRev, fmt.Errorf("%w: %s", ErrNotFound, key)
		}
		return lkal.Record{}, 0, 0, fmt.Errorf("get %s: %w", key, err)
	}
	doc, err := decodeStored(obj, key, kv.Value)
	if err != nil {
		return lkal.Record{}, 0, 0, err
	}
	return doc, kv.ModRev, snapRev, nil
}

func (o *Objects) readOwnership(ctx context.Context, ownKey string, stored lkal.Record, atRev store.Rev) (ownership.State, store.Rev, []byte, error) {
	kv, _, err := o.st.Get(ctx, ownKey, atRev)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ownership.State{}, 0, nil, fmt.Errorf("%w: %s: missing ownership record", ErrCorrupt, ownKey)
		}
		return ownership.State{}, 0, nil, fmt.Errorf("get %s: %w", ownKey, err)
	}
	st, err := ownership.Decode(kv.Value, stored)
	if err != nil {
		return ownership.State{}, 0, nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, ownKey, err)
	}
	return st, kv.ModRev, kv.Value, nil
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
	writeObj bool,
	baseCmp store.Cmp,
	ownKey string,
	ownVal []byte,
	writeOwn bool,
	ownCmp *store.Cmp,
	source keyspace.Object,
	delta refs.Delta,
	infos map[keyspace.Object]targetInfo,
) ([]store.Cmp, []store.Op, error) {
	cmps := make([]store.Cmp, 0, 2+len(delta.Compare))
	cmps = append(cmps, baseCmp)
	if ownCmp != nil {
		cmps = append(cmps, *ownCmp)
	}
	if writeObj {
		for _, target := range delta.Compare {
			if target == source {
				continue
			}
			info := infos[target]
			cmps = append(cmps, store.Cmp{Key: info.key, Op: store.CmpEqual, ModRev: info.modRev})
		}
	}

	ops := make([]store.Op, 0, 2+len(delta.Put)+len(delta.Delete))
	if writeObj {
		ops = append(ops, store.Op{Type: store.OpPut, Key: key, Value: val})
	}
	if writeOwn {
		ops = append(ops, store.Op{Type: store.OpPut, Key: ownKey, Value: ownVal})
	}
	if writeObj {
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
	}
	return cmps, ops, nil
}

// Create authorizes initial leaf ownership, completes declared spec defaults (marking
// newly materialized defaults as .defaulted), validates the schema and references,
// assigns a version-4 UUID and generation 1, and stores o/ and own/ atomically if absent.
func (o *Objects) Create(ctx context.Context, principal refs.Principal, candidate lkal.Record) (lkal.Record, error) {
	obj, err := envelope.Object(candidate)
	if err != nil {
		return lkal.Record{}, err
	}
	key, err := objectKey(obj)
	if err != nil {
		return lkal.Record{}, err
	}
	ownKey, err := ownershipKey(obj)
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
	actor, sc, err := o.resolveOwnership(principal, obj)
	if err != nil {
		return lkal.Record{}, err
	}
	rawState, err := ownership.Create(actor, sc, canonical)
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
	ownState := ownership.MarkCompletedDefaults(rawState, canonical, doc)
	ownVal, err := ownership.Encode(ownState, doc)
	if err != nil {
		return lkal.Record{}, err
	}
	val, err := Encode(doc)
	if err != nil {
		return lkal.Record{}, err
	}
	ownCmp := store.Cmp{Key: ownKey, Op: store.CmpEqual, ModRev: 0}
	cmps, ops, err := buildWriteTxn(key, val, true, store.Cmp{Key: key, Op: store.CmpEqual, ModRev: 0}, ownKey, ownVal, true, &ownCmp, obj, delta, infos)
	if err != nil {
		return lkal.Record{}, err
	}
	ok, rev, err := o.st.Txn(ctx, cmps, ops)
	if err != nil {
		return lkal.Record{}, mapOwnershipTxnErr("create "+key, err)
	}
	if !ok {
		if _, _, getErr := o.st.Get(ctx, key, 0); getErr != nil {
			if errors.Is(getErr, store.ErrNotFound) {
				return lkal.Record{}, &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
			}
			return lkal.Record{}, fmt.Errorf("create %s: %w", key, getErr)
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
	doc, modRev, _, err := o.readStored(ctx, obj, key, 0)
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

// Update enforces leaf-path ownership over EntranceSpec, completes declared spec defaults,
// validates the schema and references, and atomically updates o/, own/, and back-references via CAS.
func (o *Objects) Update(ctx context.Context, principal refs.Principal, candidate lkal.Record) (lkal.Record, error) {
	obj, err := envelope.Object(candidate)
	if err != nil {
		return lkal.Record{}, err
	}
	key, err := objectKey(obj)
	if err != nil {
		return lkal.Record{}, err
	}
	ownKey, err := ownershipKey(obj)
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
	actor, sc, err := o.resolveOwnership(principal, obj)
	if err != nil {
		return lkal.Record{}, err
	}
	old, modRev, snapRev, err := o.readStored(ctx, obj, key, 0)
	if err != nil {
		return lkal.Record{}, err
	}
	if _, err := refs.DeleteDelta(old); err != nil {
		return lkal.Record{}, err
	}
	oldOwn, ownModRev, oldOwnRaw, err := o.readOwnership(ctx, ownKey, old, snapRev)
	if err != nil {
		return lkal.Record{}, err
	}
	completed, err := ks.completeCandidate(canonical)
	if err != nil {
		return lkal.Record{}, err
	}
	next, err := envelope.Update(old, int64(modRev), completed)
	if err != nil {
		return lkal.Record{}, err
	}
	if err := ks.validateSpec(next); err != nil {
		return lkal.Record{}, err
	}
	checkedOwn, err := ownership.CheckUpdate(actor, sc, old, oldOwn, canonical)
	if err != nil {
		return lkal.Record{}, err
	}
	nextOwn := ownership.MarkCompletedDefaults(checkedOwn, canonical, next)
	delta, infos, err := o.checkRefs(ctx, obj, principal, old, true, next)
	if err != nil {
		return lkal.Record{}, err
	}
	envChanged := !lkal.Equal(next, old)
	nextOwnRaw, err := ownership.Encode(nextOwn, next)
	if err != nil {
		return lkal.Record{}, err
	}
	ownChanged := !nextOwn.Equal(oldOwn) || !bytes.Equal(nextOwnRaw, oldOwnRaw)
	if !envChanged && !ownChanged {
		return envelope.WithRevision(old, int64(modRev)), nil
	}
	var val []byte
	if envChanged {
		val, err = Encode(next)
		if err != nil {
			return lkal.Record{}, err
		}
	}
	ownCmp := store.Cmp{Key: ownKey, Op: store.CmpEqual, ModRev: ownModRev}
	cmps, ops, err := buildWriteTxn(key, val, envChanged, store.Cmp{Key: key, Op: store.CmpEqual, ModRev: modRev}, ownKey, nextOwnRaw, ownChanged, &ownCmp, obj, delta, infos)
	if err != nil {
		return lkal.Record{}, err
	}
	ok, newRev, err := o.st.Txn(ctx, cmps, ops)
	if err != nil {
		return lkal.Record{}, mapOwnershipTxnErr("update "+key, err)
	}
	if !ok {
		return lkal.Record{}, &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
	}
	outRev := modRev
	if envChanged {
		outRev = newRev
	}
	return envelope.WithRevision(next, int64(outRev)), nil
}

// UpdateStatus validates and applies a status change to an existing object using optimistic concurrency.
func (o *Objects) UpdateStatus(ctx context.Context, candidate lkal.Record) (lkal.Record, error) {
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
	old, modRev, _, err := o.readStored(ctx, obj, key, 0)
	if err != nil {
		return lkal.Record{}, err
	}
	if _, err := refs.DeleteDelta(old); err != nil {
		return lkal.Record{}, err
	}
	next, err := envelope.UpdateStatus(old, int64(modRev), canonical, envelope.Timestamp(o.now()))
	if err != nil {
		return lkal.Record{}, err
	}
	if err := ks.validateStatus(next); err != nil {
		return lkal.Record{}, err
	}
	delta, infos, err := o.checkRefs(ctx, obj, refs.Principal{}, old, true, next)
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
	cmps, ops, err := buildWriteTxn(key, val, true, store.Cmp{Key: key, Op: store.CmpEqual, ModRev: modRev}, "", nil, false, nil, obj, delta, infos)
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
// enforcing leaf-path ownership, completing declared spec defaults, validating the kind schema
// and references, and committing o/ and own/ with CAS.
func (o *Objects) Patch(ctx context.Context, principal refs.Principal, obj keyspace.Object, ops []patch.Op) (lkal.Record, error) {
	key, err := objectKey(obj)
	if err != nil {
		return lkal.Record{}, &envelope.Error{Kind: envelope.ErrMalformed, Path: "meta", Detail: err.Error()}
	}
	ownKey, err := ownershipKey(obj)
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
	actor, sc, err := o.resolveOwnership(principal, obj)
	if err != nil {
		return lkal.Record{}, err
	}
	old, modRev, snapRev, err := o.readStored(ctx, obj, key, 0)
	if err != nil {
		return lkal.Record{}, err
	}
	if _, err := refs.DeleteDelta(old); err != nil {
		return lkal.Record{}, err
	}
	oldOwn, ownModRev, oldOwnRaw, err := o.readOwnership(ctx, ownKey, old, snapRev)
	if err != nil {
		return lkal.Record{}, err
	}
	patched, err := patch.ApplyEntrance(patch.EntranceSpec, old, int64(modRev), canonicalOps)
	if err != nil {
		return lkal.Record{}, err
	}
	canonical, err := canonicalize(patched)
	if err != nil {
		return lkal.Record{}, err
	}
	completed, err := ks.completeCandidate(canonical)
	if err != nil {
		return lkal.Record{}, err
	}
	next, err := envelope.Update(old, int64(modRev), completed)
	if err != nil {
		return lkal.Record{}, err
	}
	if err := ks.validateSpec(next); err != nil {
		return lkal.Record{}, err
	}
	checkedOwn, err := ownership.CheckPatch(actor, sc, old, oldOwn, canonical)
	if err != nil {
		return lkal.Record{}, err
	}
	nextOwn := ownership.MarkCompletedDefaults(checkedOwn, canonical, next)
	delta, infos, err := o.checkRefs(ctx, obj, principal, old, true, next)
	if err != nil {
		return lkal.Record{}, err
	}
	envChanged := !lkal.Equal(next, old)
	nextOwnRaw, err := ownership.Encode(nextOwn, next)
	if err != nil {
		return lkal.Record{}, err
	}
	ownChanged := !nextOwn.Equal(oldOwn) || !bytes.Equal(nextOwnRaw, oldOwnRaw)
	if !envChanged && !ownChanged {
		return envelope.WithRevision(old, int64(modRev)), nil
	}
	var val []byte
	if envChanged {
		val, err = Encode(next)
		if err != nil {
			return lkal.Record{}, err
		}
	}
	ownCmp := store.Cmp{Key: ownKey, Op: store.CmpEqual, ModRev: ownModRev}
	cmps, storeOps, err := buildWriteTxn(key, val, envChanged, store.Cmp{Key: key, Op: store.CmpEqual, ModRev: modRev}, ownKey, nextOwnRaw, ownChanged, &ownCmp, obj, delta, infos)
	if err != nil {
		return lkal.Record{}, err
	}
	ok, newRev, err := o.st.Txn(ctx, cmps, storeOps)
	if err != nil {
		return lkal.Record{}, mapOwnershipTxnErr("patch "+key, err)
	}
	if !ok {
		return lkal.Record{}, &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
	}
	outRev := modRev
	if envChanged {
		outRev = newRev
	}
	return envelope.WithRevision(next, int64(outRev)), nil
}

// PatchStatus applies an ordered patch sequence through the status entrance to obj,
// validating the status schema and committing with CAS.
func (o *Objects) PatchStatus(ctx context.Context, obj keyspace.Object, ops []patch.Op) (lkal.Record, error) {
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
	old, modRev, _, err := o.readStored(ctx, obj, key, 0)
	if err != nil {
		return lkal.Record{}, err
	}
	if _, err := refs.DeleteDelta(old); err != nil {
		return lkal.Record{}, err
	}
	patched, err := patch.ApplyEntrance(patch.EntranceStatus, old, int64(modRev), canonicalOps)
	if err != nil {
		return lkal.Record{}, err
	}
	canonical, err := canonicalize(patched)
	if err != nil {
		return lkal.Record{}, err
	}
	next, err := envelope.UpdateStatus(old, int64(modRev), canonical, envelope.Timestamp(o.now()))
	if err != nil {
		return lkal.Record{}, err
	}
	if err := ks.validateStatus(next); err != nil {
		return lkal.Record{}, err
	}
	delta, infos, err := o.checkRefs(ctx, obj, refs.Principal{}, old, true, next)
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
	cmps, storeOps, err := buildWriteTxn(key, val, true, store.Cmp{Key: key, Op: store.CmpEqual, ModRev: modRev}, "", nil, false, nil, obj, delta, infos)
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

// ApplyOptions carries optional preconditions for Apply and Prune.
type ApplyOptions struct {
	Expect           string
	ExpectedOwnerRev store.Rev
}

// ApplyResult holds the updated object envelope, ownerRevision, creation flag, and non-blocking notices.
type ApplyResult struct {
	Object        lkal.Record
	OwnerRevision store.Rev
	Created       bool
	Diagnostics   []ownership.Diagnostic
}

// Apply declaratively creates or updates an object's EntranceSpec leaves under leaf-path ownership.
func (o *Objects) Apply(ctx context.Context, principal refs.Principal, declaration lkal.Record, opts ApplyOptions) (ApplyResult, error) {
	if opts.ExpectedOwnerRev < 0 {
		return ApplyResult{}, &ownership.Error{
			Code:   ownership.CodeInvalidInput,
			Path:   "ownerRevision",
			Detail: "ownerRevision must be >= 0",
		}
	}
	obj, err := envelope.Object(declaration)
	if err != nil {
		return ApplyResult{}, err
	}
	key, err := objectKey(obj)
	if err != nil {
		return ApplyResult{}, err
	}
	ownKey, err := ownershipKey(obj)
	if err != nil {
		return ApplyResult{}, err
	}
	canonical, err := canonicalize(declaration)
	if err != nil {
		return ApplyResult{}, err
	}
	ks, err := o.lookupKindSchema(obj)
	if err != nil {
		return ApplyResult{}, err
	}
	actor, sc, err := o.resolveOwnership(principal, obj)
	if err != nil {
		return ApplyResult{}, err
	}

	old, oModRev, snapRev, err := o.readStored(ctx, obj, key, 0)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return o.applyCreate(ctx, principal, obj, key, ownKey, ks, actor, sc, canonical, opts)
		}
		return ApplyResult{}, err
	}
	return o.applyExisting(ctx, principal, obj, key, ownKey, ks, actor, sc, old, oModRev, snapRev, canonical, opts, ownership.ModeApply)
}

// Prune conservatively deletes only the caller's retired Origin=Created leaves under EntranceSpec.
func (o *Objects) Prune(ctx context.Context, principal refs.Principal, declaration lkal.Record, opts ApplyOptions) (ApplyResult, error) {
	if opts.ExpectedOwnerRev < 0 {
		return ApplyResult{}, &ownership.Error{
			Code:   ownership.CodeInvalidInput,
			Path:   "ownerRevision",
			Detail: "ownerRevision must be >= 0",
		}
	}
	obj, err := envelope.Object(declaration)
	if err != nil {
		return ApplyResult{}, err
	}
	key, err := objectKey(obj)
	if err != nil {
		return ApplyResult{}, err
	}
	ownKey, err := ownershipKey(obj)
	if err != nil {
		return ApplyResult{}, err
	}
	canonical, err := canonicalize(declaration)
	if err != nil {
		return ApplyResult{}, err
	}
	ks, err := o.lookupKindSchema(obj)
	if err != nil {
		return ApplyResult{}, err
	}
	actor, sc, err := o.resolveOwnership(principal, obj)
	if err != nil {
		return ApplyResult{}, err
	}

	old, oModRev, snapRev, err := o.readStored(ctx, obj, key, 0)
	if err != nil {
		return ApplyResult{}, err
	}
	return o.applyExisting(ctx, principal, obj, key, ownKey, ks, actor, sc, old, oModRev, snapRev, canonical, opts, ownership.ModePrune)
}

func (o *Objects) applyCreate(
	ctx context.Context,
	principal refs.Principal,
	obj keyspace.Object,
	key, ownKey string,
	ks *KindSchema,
	actor ownership.ActorID,
	sc ownership.Scope,
	canonical lkal.Record,
	opts ApplyOptions,
) (ApplyResult, error) {
	if opts.ExpectedOwnerRev != 0 {
		return ApplyResult{}, &ownership.Error{
			Code:   ownership.CodeOwnershipConflict,
			Detail: fmt.Sprintf("ownerRevision mismatch: expected %d, object does not exist", opts.ExpectedOwnerRev),
			Diagnostics: []ownership.Diagnostic{{
				Code:       ownership.CodeOwnershipConflict,
				Reason:     fmt.Sprintf("ownerRevision mismatch: expected %d, object does not exist", opts.ExpectedOwnerRev),
				NextAction: "omit ownerRevision when creating a new object",
			}},
		}
	}
	if opts.Expect != "" {
		return ApplyResult{}, &ownership.Error{
			Code:   ownership.CodeExternalDrift,
			Detail: "document condition token (expect) cannot match an absent object",
			Diagnostics: []ownership.Diagnostic{{
				Code:       ownership.CodeExternalDrift,
				Reason:     "document condition token (expect) cannot match an absent object",
				NextAction: "omit expect when creating a new object",
			}},
		}
	}
	if _, err := ownership.ProjectDeclaration(canonical, false); err != nil {
		return ApplyResult{}, err
	}
	rawState, err := ownership.Create(actor, sc, canonical)
	if err != nil {
		return ApplyResult{}, err
	}
	completed, err := ks.completeCandidate(canonical)
	if err != nil {
		return ApplyResult{}, err
	}
	uid, err := newUUIDv4(o.rand)
	if err != nil {
		return ApplyResult{}, err
	}
	doc, err := envelope.Create(completed, uid)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := ks.validateSpec(doc); err != nil {
		return ApplyResult{}, err
	}
	delta, infos, err := o.checkRefs(ctx, obj, principal, lkal.Record{}, false, doc)
	if err != nil {
		return ApplyResult{}, err
	}
	ownState := ownership.MarkCompletedDefaults(rawState, canonical, doc)
	ownVal, err := ownership.Encode(ownState, doc)
	if err != nil {
		return ApplyResult{}, err
	}
	val, err := Encode(doc)
	if err != nil {
		return ApplyResult{}, err
	}
	ownCmp := store.Cmp{Key: ownKey, Op: store.CmpEqual, ModRev: 0}
	cmps, ops, err := buildWriteTxn(key, val, true, store.Cmp{Key: key, Op: store.CmpEqual, ModRev: 0}, ownKey, ownVal, true, &ownCmp, obj, delta, infos)
	if err != nil {
		return ApplyResult{}, err
	}
	ok, rev, err := o.st.Txn(ctx, cmps, ops)
	if err != nil {
		return ApplyResult{}, mapOwnershipTxnErr("apply "+key, err)
	}
	if !ok {
		return ApplyResult{}, &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
	}
	return ApplyResult{
		Object:        envelope.WithRevision(doc, int64(rev)),
		OwnerRevision: rev,
		Created:       true,
	}, nil
}

func (o *Objects) applyExisting(
	ctx context.Context,
	principal refs.Principal,
	obj keyspace.Object,
	key, ownKey string,
	ks *KindSchema,
	actor ownership.ActorID,
	sc ownership.Scope,
	old lkal.Record,
	oModRev, snapRev store.Rev,
	canonical lkal.Record,
	opts ApplyOptions,
	mode ownership.Mode,
) (ApplyResult, error) {
	if _, err := refs.DeleteDelta(old); err != nil {
		return ApplyResult{}, err
	}
	// Check immutable/server-managed envelope fields on declaration before planning.
	if _, err := envelope.Update(old, int64(oModRev), canonical); err != nil {
		return ApplyResult{}, err
	}
	oldOwn, ownModRev, oldOwnRaw, err := o.readOwnership(ctx, ownKey, old, snapRev)
	if err != nil {
		return ApplyResult{}, err
	}

	planIn := ownership.PlanInput{
		Object:           obj,
		Actor:            actor,
		Scope:            sc,
		CurrentDoc:       old,
		CurrentState:     oldOwn,
		CurrentOwnerRev:  int64(ownModRev),
		Declaration:      canonical,
		Expect:           opts.Expect,
		ExpectedOwnerRev: int64(opts.ExpectedOwnerRev),
	}
	var planRes ownership.PlanResult
	if mode == ownership.ModeApply {
		planRes, err = ownership.PlanApply(planIn)
	} else {
		planRes, err = ownership.PlanPrune(planIn)
	}
	if err != nil {
		return ApplyResult{}, err
	}
	if planRes.Blocked {
		return ApplyResult{}, planRes.Err()
	}

	completed, err := ks.completeCandidate(planRes.NextDoc)
	if err != nil {
		return ApplyResult{}, err
	}
	next, err := envelope.Update(old, int64(oModRev), completed)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := ks.validateSpec(next); err != nil {
		return ApplyResult{}, err
	}
	nextOwn := ownership.MarkCompletedDefaults(planRes.NextState, planRes.NextDoc, next)
	delta, infos, err := o.checkRefs(ctx, obj, principal, old, true, next)
	if err != nil {
		return ApplyResult{}, err
	}

	envChanged := !lkal.Equal(next, old)
	nextOwnRaw, err := ownership.Encode(nextOwn, next)
	if err != nil {
		return ApplyResult{}, err
	}
	ownChanged := !nextOwn.Equal(oldOwn) || !bytes.Equal(nextOwnRaw, oldOwnRaw)
	if !envChanged && !ownChanged {
		return ApplyResult{
			Object:        envelope.WithRevision(old, int64(oModRev)),
			OwnerRevision: ownModRev,
			Diagnostics:   planRes.Diagnostics,
		}, nil
	}

	var val []byte
	if envChanged {
		val, err = Encode(next)
		if err != nil {
			return ApplyResult{}, err
		}
	}
	ownCmp := store.Cmp{Key: ownKey, Op: store.CmpEqual, ModRev: ownModRev}
	cmps, ops, err := buildWriteTxn(key, val, envChanged, store.Cmp{Key: key, Op: store.CmpEqual, ModRev: oModRev}, ownKey, nextOwnRaw, ownChanged, &ownCmp, obj, delta, infos)
	if err != nil {
		return ApplyResult{}, err
	}
	ok, newRev, err := o.st.Txn(ctx, cmps, ops)
	if err != nil {
		return ApplyResult{}, mapOwnershipTxnErr(mode.String()+" "+key, err)
	}
	if !ok {
		return ApplyResult{}, &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
	}
	outRev := oModRev
	if envChanged {
		outRev = newRev
	}
	outOwnRev := ownModRev
	if ownChanged {
		outOwnRev = newRev
	}
	return ApplyResult{
		Object:        envelope.WithRevision(next, int64(outRev)),
		OwnerRevision: outOwnRev,
		Diagnostics:   planRes.Diagnostics,
	}, nil
}

// Explain reads obj and its ownership record from a single store snapshot at atRev
// and returns the ownership explanation with document and per-leaf condition tokens.
func (o *Objects) Explain(ctx context.Context, obj keyspace.Object, atRev store.Rev) (ownership.Explanation, error) {
	if atRev < 0 {
		return ownership.Explanation{}, store.ErrInvalidArgument
	}
	key, err := objectKey(obj)
	if err != nil {
		return ownership.Explanation{}, err
	}
	ownKey, err := ownershipKey(obj)
	if err != nil {
		return ownership.Explanation{}, err
	}
	doc, oModRev, snapRev, err := o.readStored(ctx, obj, key, atRev)
	if err != nil {
		return ownership.Explanation{}, err
	}
	st, ownModRev, _, err := o.readOwnership(ctx, ownKey, doc, snapRev)
	if err != nil {
		return ownership.Explanation{}, err
	}
	return ownership.Explain(obj, doc, st, int64(oModRev), int64(ownModRev))
}

// ResolveOptions carries required preconditions for Adopt and Release.
type ResolveOptions struct {
	Path             string
	Expect           string
	ExpectedOwnerRev store.Rev
}

// ResolveResult holds the post-resolution ownership explanation and any notices (e.g. K602).
type ResolveResult struct {
	Explanation ownership.Explanation
	Diagnostics []ownership.Diagnostic
}

// Adopt explicitly claims ownership of an existing leaf with Origin=Modified via metadata-only CAS.
func (o *Objects) Adopt(ctx context.Context, principal refs.Principal, obj keyspace.Object, opts ResolveOptions) (ResolveResult, error) {
	return o.runResolve(ctx, principal, obj, opts, true)
}

// Release explicitly relinquishes self-owned leaf ownership via metadata-only CAS.
func (o *Objects) Release(ctx context.Context, principal refs.Principal, obj keyspace.Object, opts ResolveOptions) (ResolveResult, error) {
	return o.runResolve(ctx, principal, obj, opts, false)
}

func (o *Objects) runResolve(ctx context.Context, principal refs.Principal, obj keyspace.Object, opts ResolveOptions, isAdopt bool) (ResolveResult, error) {
	if opts.ExpectedOwnerRev < 0 {
		return ResolveResult{}, &ownership.Error{
			Code:   ownership.CodeInvalidInput,
			Path:   "ownerRevision",
			Detail: "ownerRevision must be >= 0",
		}
	}
	key, err := objectKey(obj)
	if err != nil {
		return ResolveResult{}, err
	}
	ownKey, err := ownershipKey(obj)
	if err != nil {
		return ResolveResult{}, err
	}
	p, err := ownership.ParsePath(opts.Path)
	if err != nil {
		return ResolveResult{}, err
	}
	if err := ownership.ValidateEntranceSpecPath(p); err != nil {
		return ResolveResult{}, err
	}
	actor, sc, err := o.resolveOwnership(principal, obj)
	if err != nil {
		return ResolveResult{}, err
	}
	doc, oModRev, snapRev, err := o.readStored(ctx, obj, key, 0)
	if err != nil {
		return ResolveResult{}, err
	}
	oldOwn, ownModRev, oldOwnRaw, err := o.readOwnership(ctx, ownKey, doc, snapRev)
	if err != nil {
		return ResolveResult{}, err
	}
	in := ownership.ResolveInput{
		Object:           obj,
		Actor:            actor,
		Scope:            sc,
		CurrentDoc:       doc,
		CurrentState:     oldOwn,
		CurrentOwnerRev:  int64(ownModRev),
		Path:             p,
		Expect:           opts.Expect,
		ExpectedOwnerRev: int64(opts.ExpectedOwnerRev),
	}
	var res ownership.ResolveResult
	if isAdopt {
		res, err = ownership.Adopt(in)
	} else {
		res, err = ownership.Release(in)
	}
	if err != nil {
		return ResolveResult{}, err
	}

	nextOwnRaw, err := ownership.Encode(res.NextState, doc)
	if err != nil {
		return ResolveResult{}, err
	}
	ownChanged := res.OwnChanged || !bytes.Equal(nextOwnRaw, oldOwnRaw)
	finalOwnRev := ownModRev
	if ownChanged {
		cmps := []store.Cmp{
			{Key: key, Op: store.CmpEqual, ModRev: oModRev},
			{Key: ownKey, Op: store.CmpEqual, ModRev: ownModRev},
		}
		ops := []store.Op{{Type: store.OpPut, Key: ownKey, Value: nextOwnRaw}}
		ok, newRev, err := o.st.Txn(ctx, cmps, ops)
		if err != nil {
			opName := "release " + key
			if isAdopt {
				opName = "adopt " + key
			}
			return ResolveResult{}, mapOwnershipTxnErr(opName, err)
		}
		if !ok {
			return ResolveResult{}, &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
		}
		finalOwnRev = newRev
	}
	exp, err := ownership.Explain(obj, doc, res.NextState, int64(oModRev), int64(finalOwnRev))
	if err != nil {
		return ResolveResult{}, err
	}
	return ResolveResult{
		Explanation: exp,
		Diagnostics: res.Diagnostics,
	}, nil
}

func mapOwnershipTxnErr(op string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &ownership.Error{
			Code:   ownership.CodeIndeterminateOutcome,
			Detail: "indeterminate transaction outcome",
			Diagnostics: []ownership.Diagnostic{{
				Code:       ownership.CodeIndeterminateOutcome,
				Reason:     "indeterminate transaction outcome",
				NextAction: "re-read object and ownership state via explain before retrying",
			}},
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

func asMalformedPatchErr(err error) error {
	var pe *patch.Error
	if errors.As(err, &pe) && pe.Kind == patch.Malformed {
		return pe
	}
	return &patch.Error{Kind: patch.Malformed, Detail: err.Error()}
}

// Delete removes obj, its ownership record, and its back-reference keys from the store,
// requiring its current mod revision to equal rev when rev != 0.
// Unreadable stored values or invalid reference metadata fall back to scanning the tenant back-reference area.
func (o *Objects) Delete(ctx context.Context, obj keyspace.Object, rev int64) error {
	key, err := objectKey(obj)
	if err != nil {
		return err
	}
	ownKey, err := ownershipKey(obj)
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
			return o.deleteWithDelta(ctx, obj, key, ownKey, bodyCmp, delta)
		}
	}
	return o.deleteScanningBackRefs(ctx, obj, key, ownKey, bodyCmp)
}

func (o *Objects) deleteWithDelta(ctx context.Context, obj keyspace.Object, key, ownKey string, bodyCmp store.Cmp, delta refs.Delta) error {
	ops := make([]store.Op, 0, 2+len(delta.Delete))
	ops = append(ops,
		store.Op{Type: store.OpDelete, Key: key},
		store.Op{Type: store.OpDelete, Key: ownKey},
	)
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

func (o *Objects) deleteScanningBackRefs(ctx context.Context, obj keyspace.Object, key, ownKey string, bodyCmp store.Cmp) error {
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

	ok, _, err := o.st.Txn(ctx, cmps, []store.Op{
		{Type: store.OpDelete, Key: key},
		{Type: store.OpDelete, Key: ownKey},
	})
	if err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	if !ok {
		return &envelope.Error{Kind: envelope.ErrConflict, Path: "meta/revision", Detail: "concurrent modification"}
	}
	return nil
}
