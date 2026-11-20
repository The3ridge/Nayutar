package liveness

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"

	"github.com/Kaikei-e/Nayutar/core/liveness/detector"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

const (
	// EvictionWindow is the sliding window duration in milliseconds for eviction permits.
	EvictionWindow = 60000
	// EvictionPercent is the percentage of nodes allowed eviction permits per window.
	EvictionPercent = 1

	lkalVersion = "0.1"
	maxSafeInt  = int64(1) << 53
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

type recordDoc struct {
	Lkal      *string   `json:"$lkal"`
	Evictable *bool     `json:"evictable"`
	Rack      *string   `json:"rack"`
	Since     *int64    `json:"since"`
	State     *stateDoc `json:"state"`
}

type stateDoc struct {
	Enum *string `json:"$enum"`
}

type encodeRecordDoc struct {
	Lkal      string         `json:"$lkal"`
	Evictable bool           `json:"evictable"`
	Rack      string         `json:"rack"`
	Since     int64          `json:"since"`
	State     encodeStateDoc `json:"state"`
}

type encodeStateDoc struct {
	Enum string `json:"$enum"`
}

func encodeRecord(rec Record) ([]byte, error) {
	if err := validateRack(rec.Rack); err != nil {
		return nil, err
	}
	if rec.Since < 0 || rec.Since >= maxSafeInt {
		return nil, fmt.Errorf("%w: since %d out of range [0, 2^53)", ErrCorruptRecord, rec.Since)
	}
	st, err := stateString(rec.State)
	if err != nil {
		return nil, err
	}
	return json.Marshal(encodeRecordDoc{
		Lkal:      lkalVersion,
		Evictable: rec.Evictable,
		Rack:      rec.Rack,
		Since:     rec.Since,
		State:     encodeStateDoc{Enum: st},
	})
}

// DecodeRecord decodes the value stored at a node key (used by watchers).
func DecodeRecord(node string, value []byte, modRev store.Rev) (Record, error) {
	key, err := (keyspace.Key{Family: keyspace.FamilyNode, Node: node}).Encode()
	if err != nil {
		return Record{}, err
	}
	var doc recordDoc
	if err := json.Unmarshal(value, &doc, json.RejectUnknownMembers(true)); err != nil {
		return Record{}, fmt.Errorf("%w: key %s: %v", ErrCorruptRecord, key, err)
	}
	if doc.Lkal == nil || *doc.Lkal != lkalVersion ||
		doc.Evictable == nil || doc.Rack == nil || doc.Since == nil ||
		doc.State == nil || doc.State.Enum == nil {
		return Record{}, fmt.Errorf("%w: key %s: unexpected schema or fields", ErrCorruptRecord, key)
	}
	if *doc.Since < 0 || *doc.Since >= maxSafeInt {
		return Record{}, fmt.Errorf("%w: key %s: since %d out of range [0, 2^53)", ErrCorruptRecord, key, *doc.Since)
	}
	if err := validateRack(*doc.Rack); err != nil {
		return Record{}, fmt.Errorf("%w: key %s: %v", ErrCorruptRecord, key, err)
	}
	st, err := parseState(*doc.State.Enum)
	if err != nil {
		return Record{}, fmt.Errorf("%w: key %s: %v", ErrCorruptRecord, key, err)
	}
	rec := Record{
		Node:      node,
		State:     st,
		Since:     *doc.Since,
		Rack:      *doc.Rack,
		Evictable: *doc.Evictable,
		ModRev:    modRev,
	}
	canonical, err := encodeRecord(rec)
	if err != nil || !bytes.Equal(canonical, value) {
		return Record{}, fmt.Errorf("%w: key %s: non-canonical encoding", ErrCorruptRecord, key)
	}
	return rec, nil
}

type budgetDoc struct {
	Lkal    *string `json:"$lkal"`
	Granted []int64 `json:"granted"`
}

type encodeBudgetDoc struct {
	Lkal    string  `json:"$lkal"`
	Granted []int64 `json:"granted"`
}

func encodeBudget(granted []int64) ([]byte, error) {
	if granted == nil {
		granted = []int64{}
	}
	var prev int64 = -1
	for _, g := range granted {
		if g < 0 || g >= maxSafeInt {
			return nil, fmt.Errorf("%w: key %s: timestamp %d out of range [0, 2^53)", ErrCorruptRecord, keyspace.EvictionBudgetKey, g)
		}
		if g < prev {
			return nil, fmt.Errorf("%w: key %s: timestamps not ascending", ErrCorruptRecord, keyspace.EvictionBudgetKey)
		}
		prev = g
	}
	return json.Marshal(encodeBudgetDoc{
		Lkal:    lkalVersion,
		Granted: granted,
	})
}

func decodeBudget(key string, value []byte) ([]int64, error) {
	var doc budgetDoc
	if err := json.Unmarshal(value, &doc, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("%w: key %s: %v", ErrCorruptRecord, key, err)
	}
	if doc.Lkal == nil || *doc.Lkal != lkalVersion || doc.Granted == nil {
		return nil, fmt.Errorf("%w: key %s: unexpected schema or fields", ErrCorruptRecord, key)
	}
	var prev int64 = -1
	for _, g := range doc.Granted {
		if g < 0 || g >= maxSafeInt {
			return nil, fmt.Errorf("%w: key %s: timestamp %d out of range [0, 2^53)", ErrCorruptRecord, key, g)
		}
		if g < prev {
			return nil, fmt.Errorf("%w: key %s: timestamps not ascending", ErrCorruptRecord, key)
		}
		prev = g
	}
	canonical, err := encodeBudget(doc.Granted)
	if err != nil || !bytes.Equal(canonical, value) {
		return nil, fmt.Errorf("%w: key %s: non-canonical encoding", ErrCorruptRecord, key)
	}
	return doc.Granted, nil
}
