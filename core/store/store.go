// Package store defines the contract every Nayutar state store implements.
//
// The contract is a fixed subset of the etcd v3 API: revisioned reads, a
// compare-and-write transaction on mod revisions, prefix watches with progress
// notifications, and compaction. It has no leases and no keys with a TTL.
package store

import (
	"context"
	"errors"
)

// Rev is a store revision. Every transaction that changes at least one key
// advances the store revision by exactly one, and all its changes carry that
// revision. Revision 0 is never assigned to a change.
type Rev int64

// KV is a key with its value and the revision of its last change.
type KV struct {
	Key    string
	Value  []byte
	ModRev Rev
}

// CmpOp is the relation a Cmp requires between a key's mod revision and Cmp.ModRev.
type CmpOp uint8

const (
	CmpEqual CmpOp = iota + 1
	CmpNotEqual
	CmpLess
	CmpGreater
)

// Cmp holds when the mod revision of Key stands in relation Op to ModRev.
// An absent key has mod revision 0, so {Key, CmpEqual, 0} means "Key is absent".
type Cmp struct {
	Key    string
	Op     CmpOp
	ModRev Rev
}

// Holds reports whether the comparison is satisfied by a key whose mod revision is modRev.
func (c Cmp) Holds(modRev Rev) bool {
	switch c.Op {
	case CmpEqual:
		return modRev == c.ModRev
	case CmpNotEqual:
		return modRev != c.ModRev
	case CmpLess:
		return modRev < c.ModRev
	case CmpGreater:
		return modRev > c.ModRev
	default:
		return false
	}
}

// OpType is the kind of write an Op performs.
type OpType uint8

const (
	OpPut OpType = iota + 1
	OpDelete
)

// Op is one write of a transaction. Value is ignored for OpDelete.
type Op struct {
	Type  OpType
	Key   string
	Value []byte
}

// EventType is the kind of an Event.
type EventType uint8

const (
	EventPut EventType = iota + 1
	EventDelete
	EventProgress
)

// Event is one element of a watch stream.
//
// EventPut and EventDelete report one changed key at revision Rev; Value is
// set for EventPut only. EventProgress carries no key: it states that every
// change matching the watch with a revision up to and including Rev was
// delivered before it.
type Event struct {
	Type  EventType
	Rev   Rev
	Key   string
	Value []byte
}

var (
	// ErrNotFound is returned by Get when the key is absent at the read revision.
	ErrNotFound = errors.New("store: key not found")
	// ErrCompacted is returned when a requested revision is older than the
	// compaction revision. It also ends a watch that compaction overtook.
	ErrCompacted = errors.New("store: revision has been compacted")
	// ErrFutureRev is returned when a requested revision is newer than the store revision.
	ErrFutureRev = errors.New("store: revision is in the future")
	// ErrInvalidArgument is returned for requests the contract does not define.
	ErrInvalidArgument = errors.New("store: invalid argument")
	// ErrTooLarge is returned for a request beyond the limits of the store.
	ErrTooLarge = errors.New("store: request exceeds the store's limits")
)

// Store is the state store contract.
//
// Keys are non-empty strings ordered bytewise. Reads with atRev 0, CurrentRev
// and Txn are linearizable. Reads with atRev > 0 return the state as of that
// revision; they fail with ErrFutureRev above the store revision and with
// ErrCompacted below the compaction revision.
//
// A store may bound the number of cmps and ops of a Txn and the byte size of a
// request. A request beyond its limits fails with ErrTooLarge and changes
// nothing.
type Store interface {
	// Get returns the key as of atRev together with the revision the read
	// reflects: atRev itself, or the store revision when atRev is 0. An absent
	// key yields ErrNotFound and still returns that revision.
	Get(ctx context.Context, key string, atRev Rev) (KV, Rev, error)

	// Range returns the keys starting with prefix in ascending order as of
	// atRev, and the revision the read reflects. A limit of 0 means no limit.
	// When more keys remain, the returned continuation is the last key of the
	// page; passing it back as cont, with the returned revision as atRev,
	// resumes strictly after it on the same snapshot. Otherwise it is empty.
	Range(ctx context.Context, prefix string, atRev Rev, limit int, cont string) ([]KV, Rev, string, error)

	// Txn applies ops atomically when every comparison holds, and reports
	// whether it did. All changes of one transaction share one new revision,
	// which is returned. Deleting an absent key changes nothing; a transaction
	// that changes nothing, or whose comparisons fail, returns the unchanged
	// store revision. Ops must name pairwise distinct keys.
	Txn(ctx context.Context, cmps []Cmp, ops []Op) (bool, Rev, error)

	// Watch streams the changes of keys starting with prefix, beginning with
	// revision fromRev, or with the first change after the call when fromRev
	// is 0. The stream ends when ctx is done. A positive fromRev below the
	// compaction revision always ends the watch with ErrCompacted. In
	// addition, a store may end a watch with ErrCompacted when a compaction
	// passes changes the watch has not delivered yet; the caller then reads
	// the state again.
	Watch(ctx context.Context, prefix string, fromRev Rev) (WatchStream, error)

	// CurrentRev returns the store revision.
	CurrentRev(ctx context.Context) (Rev, error)

	// Compact discards the history before rev. The state as of rev itself
	// stays readable and watchable. It fails with ErrCompacted when rev is not
	// above the compaction revision and with ErrFutureRev when rev is above
	// the store revision.
	Compact(ctx context.Context, rev Rev) error
}

// WatchStream is one watch.
type WatchStream interface {
	// Events delivers changes in ascending revision order, without gaps or
	// duplicates. Changes of one transaction arrive consecutively in the order
	// of its ops. The channel is closed when the watch ends.
	Events() <-chan Event

	// RequestProgress asks for an EventProgress carrying the store revision.
	// The store answers only once the watch has caught up: it has delivered
	// every matching change up to the store revision, and the store revision
	// has reached fromRev. Until then it drops the request without an error,
	// so callers repeat it until a notification with a sufficient revision
	// arrives. If ctx is done it returns ctx.Err(); otherwise, once the watch
	// has ended, it fails with the error that ended the watch.
	RequestProgress(ctx context.Context) error

	// Err reports why the watch ended, once Events is closed: ErrCompacted
	// for a fromRev below the compaction revision, or when the store ended
	// the watch because a compaction passed changes it had not delivered;
	// otherwise the error of the watch context or of the store.
	Err() error
}
