package liveness

import (
	"bytes"
	_ "embed"
	"encoding/json/jsontext"
	"errors"
	"fmt"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/LKAL/schema"
	"github.com/Kaikei-e/Nayutar/core/liveness/detector"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

//go:embed node.schema.lkal
var nodeSchemaBytes []byte

//go:embed evictions.schema.lkal
var evictionsSchemaBytes []byte

var (
	nodeSchema      *schema.Schema
	evictionsSchema *schema.Schema
)

func init() {
	ns, err := schema.Compile("node.schema.lkal", nodeSchemaBytes)
	if err != nil {
		panic("liveness: embedded node.schema.lkal failed to compile: " + err.Error())
	}
	nodeSchema = ns

	es, err := schema.Compile("evictions.schema.lkal", evictionsSchemaBytes)
	if err != nil {
		panic("liveness: embedded evictions.schema.lkal failed to compile: " + err.Error())
	}
	evictionsSchema = es
}

const (
	// EvictionWindow is the sliding window duration in milliseconds for eviction permits.
	EvictionWindow = 60000
	// EvictionPercent is the percentage of nodes allowed eviction permits per window.
	EvictionPercent = 1

	emptyLKALJSON = `{"$lkal":"0.1"}`
	maxSafeInt    = int64(1) << 53
)

var (
	// ErrExists is returned by Register when the node is already registered.
	ErrExists = errors.New("liveness: node already registered")
	// ErrNotRegistered is returned when no record exists for the node.
	ErrNotRegistered = errors.New("liveness: node not registered")
	// ErrCorruptRecord is returned when a record key or value cannot be decoded.
	ErrCorruptRecord = errors.New("liveness: unreadable record")
	// ErrInvalidRack is returned when a rack name is not a valid RFC 1123 label.
	ErrInvalidRack = errors.New("liveness: invalid rack")
	// ErrInvalidTime is returned when a wall time argument is outside [0, 2^53).
	ErrInvalidTime = errors.New("liveness: invalid wall time")
)

func validateWall(wall int64) error {
	if wall < 0 || wall >= maxSafeInt {
		return fmt.Errorf("%w: %d out of range [0, 2^53)", ErrInvalidTime, wall)
	}
	return nil
}

// Record is a node's liveness record in the store.
type Record struct {
	Node      string
	State     detector.State // StatePending, StateReady, StateNotReady
	Since     int64          // wall ms of the last state change
	Rack      string
	Evictable bool
	ModRev    store.Rev
}

func validateRack(rack string) error {
	if len(rack) < 1 || len(rack) > 63 {
		return fmt.Errorf("%w: rack length %d must be 1..63", ErrInvalidRack, len(rack))
	}
	for i := range len(rack) {
		b := rack[i]
		alnum := (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
		if alnum {
			continue
		}
		if b == '-' && i > 0 && i < len(rack)-1 {
			continue
		}
		return fmt.Errorf("%w: invalid byte %q at index %d", ErrInvalidRack, b, i)
	}
	return nil
}

func stateString(s detector.State) (string, error) {
	switch s {
	case detector.StatePending:
		return "pending", nil
	case detector.StateReady:
		return "ready", nil
	case detector.StateNotReady:
		return "notready", nil
	default:
		return "", fmt.Errorf("%w: unknown state %d", ErrCorruptRecord, s)
	}
}

func parseState(s string) (detector.State, error) {
	switch s {
	case "pending":
		return detector.StatePending, nil
	case "ready":
		return detector.StateReady, nil
	case "notready":
		return detector.StateNotReady, nil
	default:
		return 0, fmt.Errorf("%w: unknown state %q", ErrCorruptRecord, s)
	}
}

func encodeCompactLKAL(s *schema.Schema, doc lkal.Record) ([]byte, error) {
	if err := s.ValidateValue(doc); err != nil {
		return nil, err
	}
	f, err := lkal.FromJSON([]byte(emptyLKALJSON))
	if err != nil {
		return nil, err
	}
	for _, fld := range doc.Fields {
		if err := f.Set(fld.Key, fld.Value); err != nil {
			return nil, err
		}
	}
	raw, err := lkal.ToJSON(f, false)
	if err != nil {
		return nil, err
	}
	v := jsontext.Value(raw)
	if err := v.Compact(); err != nil {
		return nil, err
	}
	return []byte(v), nil
}

func encodeRecord(rec Record) ([]byte, error) {
	if err := validateRack(rec.Rack); err != nil {
		return nil, err
	}
	st, err := stateString(rec.State)
	if err != nil {
		return nil, err
	}
	doc := lkal.Record{
		Fields: []lkal.Field{
			{Key: "evictable", Value: lkal.Bool(rec.Evictable)},
			{Key: "rack", Value: lkal.String(rec.Rack)},
			{Key: "since", Value: lkal.Int(rec.Since)},
			{Key: "state", Value: lkal.Enum(st)},
		},
	}
	raw, err := encodeCompactLKAL(nodeSchema, doc)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptRecord, err)
	}
	return raw, nil
}

// DecodeRecord decodes the value stored at a node key (used by watchers).
func DecodeRecord(node string, value []byte, modRev store.Rev) (Record, error) {
	key, err := (keyspace.Key{Family: keyspace.FamilyNode, Node: node}).Encode()
	if err != nil {
		return Record{}, err
	}
	f, err := lkal.FromJSON(value)
	if err != nil {
		return Record{}, fmt.Errorf("%w: key %s: %v", ErrCorruptRecord, key, err)
	}
	if err := nodeSchema.Validate(f); err != nil {
		return Record{}, fmt.Errorf("%w: key %s: %v", ErrCorruptRecord, key, err)
	}

	var (
		evictable bool
		rack      string
		since     int64
		stateName string
	)
	for _, fld := range f.Data().Fields {
		switch fld.Key {
		case "evictable":
			evictable = bool(fld.Value.(lkal.Bool))
		case "rack":
			rack = string(fld.Value.(lkal.String))
		case "since":
			since = int64(fld.Value.(lkal.Int))
		case "state":
			stateName = string(fld.Value.(lkal.Enum))
		}
	}
	st, err := parseState(stateName)
	if err != nil {
		return Record{}, fmt.Errorf("%w: key %s: %v", ErrCorruptRecord, key, err)
	}
	rec := Record{
		Node:      node,
		State:     st,
		Since:     since,
		Rack:      rack,
		Evictable: evictable,
		ModRev:    modRev,
	}
	canonical, err := encodeRecord(rec)
	if err != nil || !bytes.Equal(canonical, value) {
		return Record{}, fmt.Errorf("%w: key %s: non-canonical encoding", ErrCorruptRecord, key)
	}
	return rec, nil
}

func encodeBudget(granted []int64) ([]byte, error) {
	items := make(lkal.List, len(granted))
	var prev int64 = -1
	for i, g := range granted {
		if g < prev {
			return nil, fmt.Errorf("%w: key %s: timestamps not ascending", ErrCorruptRecord, keyspace.EvictionBudgetKey)
		}
		prev = g
		items[i] = lkal.Int(g)
	}
	doc := lkal.Record{
		Fields: []lkal.Field{
			{Key: "granted", Value: items},
		},
	}
	raw, err := encodeCompactLKAL(evictionsSchema, doc)
	if err != nil {
		return nil, fmt.Errorf("%w: key %s: %v", ErrCorruptRecord, keyspace.EvictionBudgetKey, err)
	}
	return raw, nil
}

func decodeBudget(key string, value []byte) ([]int64, error) {
	f, err := lkal.FromJSON(value)
	if err != nil {
		return nil, fmt.Errorf("%w: key %s: %v", ErrCorruptRecord, key, err)
	}
	if err := evictionsSchema.Validate(f); err != nil {
		return nil, fmt.Errorf("%w: key %s: %v", ErrCorruptRecord, key, err)
	}

	var list lkal.List
	for _, fld := range f.Data().Fields {
		if fld.Key == "granted" {
			list = fld.Value.(lkal.List)
		}
	}
	granted := make([]int64, len(list))
	var prev int64 = -1
	for i, el := range list {
		g := int64(el.(lkal.Int))
		if g < prev {
			return nil, fmt.Errorf("%w: key %s: timestamps not ascending", ErrCorruptRecord, key)
		}
		prev = g
		granted[i] = g
	}
	canonical, err := encodeBudget(granted)
	if err != nil || !bytes.Equal(canonical, value) {
		return nil, fmt.Errorf("%w: key %s: non-canonical encoding", ErrCorruptRecord, key)
	}
	return granted, nil
}
