package tenant

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

// State represents the lifecycle state of a tenant registration.
type State uint8

const (
	// StateActive indicates an active tenant whose fenced views accept transactions.
	StateActive State = iota + 1
	// StateTerminating indicates a tenant being drained and deleted.
	StateTerminating
)

// Record describes a tenant registration stored in the registry.
type Record struct {
	Name   string
	State  State
	ModRev store.Rev
}

var (
	// ErrExists is returned by Register when the tenant is already active.
	ErrExists = errors.New("tenant: already registered")
	// ErrNotRegistered is returned when no registration record exists for the tenant.
	ErrNotRegistered = errors.New("tenant: not registered")
	// ErrTerminating is returned when the tenant is currently terminating.
	ErrTerminating = errors.New("tenant: terminating")
	// ErrFenced is returned by a fenced view's Txn when the registration changed after the view was opened.
	ErrFenced = errors.New("tenant: registration changed since the view was opened")
	// ErrCorruptRecord is returned when a registry entry has an invalid key or unreadable value.
	ErrCorruptRecord = errors.New("tenant: unreadable registry record")
)

const (
	activeJSON      = `{"$lkal":"0.1","state":{"$enum":"active"}}`
	terminatingJSON = `{"$lkal":"0.1","state":{"$enum":"terminating"}}`
	lkalVersion     = "0.1"
	listPage        = 64
	drainPage       = 64
)

// Registry manages tenant registrations and opens fenced tenant views.
type Registry struct {
	root Store
}

// NewRegistry returns a Registry backed by the cluster-wide store root.
func NewRegistry(root Store) *Registry {
	return &Registry{root: root}
}

func tenantKey(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return keyspace.Key{Family: keyspace.FamilyTenant, Tenant: name}.Encode()
}

type recordDoc struct {
	Lkal  *string   `json:"$lkal"`
	State *stateDoc `json:"state"`
}

type stateDoc struct {
	Enum *string `json:"$enum"`
}

func decodeRecordValue(key string, data []byte) (State, error) {
	var doc recordDoc
	if err := json.Unmarshal(data, &doc, json.RejectUnknownMembers(true)); err != nil {
		return 0, fmt.Errorf("%w: key %s: %v", ErrCorruptRecord, key, err)
	}
	if doc.Lkal == nil || *doc.Lkal != lkalVersion || doc.State == nil || doc.State.Enum == nil {
		return 0, fmt.Errorf("%w: key %s: unexpected schema or fields", ErrCorruptRecord, key)
	}
	switch *doc.State.Enum {
	case "active":
		return StateActive, nil
	case "terminating":
		return StateTerminating, nil
	default:
		return 0, fmt.Errorf("%w: key %s: unknown state %q", ErrCorruptRecord, key, *doc.State.Enum)
	}
}

func (r *Registry) readRecord(ctx context.Context, name, key string) (Record, error) {
	kv, _, err := r.root.Get(ctx, key, 0)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Record{}, fmt.Errorf("%w: %s", ErrNotRegistered, name)
		}
		return Record{}, err
	}
	state, err := decodeRecordValue(key, kv.Value)
	if err != nil {
		return Record{}, err
	}
	return Record{Name: name, State: state, ModRev: kv.ModRev}, nil
}

// Register creates an active registration for tenant name if absent.
func (r *Registry) Register(ctx context.Context, name string) error {
	key, err := tenantKey(name)
	if err != nil {
		return err
	}
	for {
		ok, _, err := r.root.Txn(ctx,
			[]store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: 0}},
			[]store.Op{{Type: store.OpPut, Key: key, Value: []byte(activeJSON)}},
		)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		rec, err := r.readRecord(ctx, name, key)
		if err != nil {
			if errors.Is(err, ErrNotRegistered) {
				continue
			}
			return err
		}
		switch rec.State {
		case StateActive:
			return fmt.Errorf("%w: %s", ErrExists, name)
		case StateTerminating:
			return fmt.Errorf("%w: %s", ErrTerminating, name)
		}
	}
}

// Get returns the current registration record for tenant name.
func (r *Registry) Get(ctx context.Context, name string) (Record, error) {
	key, err := tenantKey(name)
	if err != nil {
		return Record{}, err
	}
	return r.readRecord(ctx, name, key)
}

// List returns all tenant registrations in ascending name order as of one snapshot revision.
func (r *Registry) List(ctx context.Context) ([]Record, store.Rev, error) {
	var records []Record
	var snapRev store.Rev
	cont := ""
	for {
		kvs, rev, next, err := r.root.Range(ctx, keyspace.TenantsPrefix, snapRev, listPage, cont)
		if err != nil {
			return nil, 0, err
		}
		if snapRev == 0 {
			snapRev = rev
		}
		for _, kv := range kvs {
			parsed, err := keyspace.Parse(kv.Key)
			if err != nil || parsed.Family != keyspace.FamilyTenant {
				return nil, 0, fmt.Errorf("%w: invalid registry key %q", ErrCorruptRecord, kv.Key)
			}
			state, err := decodeRecordValue(kv.Key, kv.Value)
			if err != nil {
				return nil, 0, err
			}
			records = append(records, Record{Name: parsed.Tenant, State: state, ModRev: kv.ModRev})
		}
		if next == "" {
			return records, snapRev, nil
		}
		cont = next
	}
}

// View opens a fenced tenant view confined to tenant name if it is active.
func (r *Registry) View(ctx context.Context, name string) (Store, error) {
	key, err := tenantKey(name)
	if err != nil {
		return nil, err
	}
	rec, err := r.readRecord(ctx, name, key)
	if err != nil {
		return nil, err
	}
	if rec.State == StateTerminating {
		return nil, fmt.Errorf("%w: %s", ErrTerminating, name)
	}
	prefix, err := Prefix(name)
	if err != nil {
		return nil, err
	}
	return &view{
		inner:       r.root,
		prefix:      prefix,
		fenceKey:    key,
		fenceModRev: rec.ModRev,
	}, nil
}

// Delete transitions tenant name to terminating, drains all keys under its prefix, and removes its registration.
// It resumes a terminating registration, returns nil if another deletion finishes first, and returns nil
// without touching an active registration created after the call started.
func (r *Registry) Delete(ctx context.Context, name string) error {
	key, err := tenantKey(name)
	if err != nil {
		return err
	}
	prefix, err := Prefix(name)
	if err != nil {
		return err
	}
	started := false
	sawTerminating := false
	var activeRev store.Rev
	for {
		rec, err := r.readRecord(ctx, name, key)
		if err != nil {
			if started && errors.Is(err, ErrNotRegistered) {
				return nil
			}
			return err
		}
		started = true

		var termRev store.Rev
		switch rec.State {
		case StateActive:
			if sawTerminating || (activeRev != 0 && rec.ModRev != activeRev) {
				return nil
			}
			activeRev = rec.ModRev
			ok, rev, err := r.root.Txn(ctx,
				[]store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: rec.ModRev}},
				[]store.Op{{Type: store.OpPut, Key: key, Value: []byte(terminatingJSON)}},
			)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			termRev = rev
			sawTerminating = true
		case StateTerminating:
			termRev = rec.ModRev
			sawTerminating = true
		}

		done, err := r.drainAndDelete(ctx, key, prefix, termRev)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
}

func (r *Registry) drainAndDelete(ctx context.Context, key, prefix string, termRev store.Rev) (bool, error) {
	page := drainPage
	for {
		kvs, _, _, err := r.root.Range(ctx, prefix, 0, page, "")
		if err != nil {
			return false, err
		}
		if len(kvs) == 0 {
			break
		}
		ops := make([]store.Op, len(kvs))
		for i, kv := range kvs {
			ops[i] = store.Op{Type: store.OpDelete, Key: kv.Key}
		}
		for {
			ok, _, err := r.root.Txn(ctx,
				[]store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: termRev}},
				ops,
			)
			if err != nil {
				if errors.Is(err, store.ErrTooLarge) && len(ops) > 1 {
					ops = ops[:len(ops)/2]
					page = len(ops)
					continue
				}
				return false, err
			}
			if !ok {
				return false, nil
			}
			break
		}
	}
	ok, _, err := r.root.Txn(ctx,
		[]store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: termRev}},
		[]store.Op{{Type: store.OpDelete, Key: key}},
	)
	if err != nil {
		return false, err
	}
	return ok, nil
}
