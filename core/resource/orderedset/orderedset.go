// Package orderedset implements pure sorting, array conversions, and sparse order helpers for ordered record sets.
package orderedset

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/Kaikei-e/LKAL"
)

const (
	// OrderField is the reserved leaf key holding the integer sort key of each entry.
	OrderField = "order"
	// Step is the default spacing between consecutive generated order values.
	Step int64 = 10
)

// ErrorKind classifies failures returned by orderedset operations.
type ErrorKind uint8

const (
	// Malformed indicates an invalid key, non-unique record tree, non-record entry, invalid or missing order, or invalid range.
	Malformed ErrorKind = iota + 1
	// DuplicateKey indicates two input elements map to the same entry key.
	DuplicateKey
	// DuplicateOrder indicates two entries in a set share the same integer order.
	DuplicateOrder
	// ReservedField indicates an input record already defines the field being synthesized ("order" or keyField).
	ReservedField
	// GapExhausted indicates no integer exists strictly between two adjacent order values.
	GapExhausted
	// Overflow indicates an order computation would exceed the signed 64-bit integer range.
	Overflow
)

// String returns the canonical camelCase name of k.
func (k ErrorKind) String() string {
	switch k {
	case Malformed:
		return "malformed"
	case DuplicateKey:
		return "duplicateKey"
	case DuplicateOrder:
		return "duplicateOrder"
	case ReservedField:
		return "reservedField"
	case GapExhausted:
		return "gapExhausted"
	case Overflow:
		return "overflow"
	default:
		return fmt.Sprintf("unknown(%d)", k)
	}
}

func (k ErrorKind) sentinel() error {
	switch k {
	case Malformed:
		return ErrMalformed
	case DuplicateKey:
		return ErrDuplicateKey
	case DuplicateOrder:
		return ErrDuplicateOrder
	case ReservedField:
		return ErrReservedField
	case GapExhausted:
		return ErrGapExhausted
	case Overflow:
		return ErrOverflow
	default:
		return nil
	}
}

var (
	// ErrMalformed is the sentinel error for Malformed failures.
	ErrMalformed = errors.New("orderedset: malformed")
	// ErrDuplicateKey is the sentinel error for DuplicateKey failures.
	ErrDuplicateKey = errors.New("orderedset: duplicateKey")
	// ErrDuplicateOrder is the sentinel error for DuplicateOrder failures.
	ErrDuplicateOrder = errors.New("orderedset: duplicateOrder")
	// ErrReservedField is the sentinel error for ReservedField failures.
	ErrReservedField = errors.New("orderedset: reservedField")
	// ErrGapExhausted is the sentinel error for GapExhausted failures.
	ErrGapExhausted = errors.New("orderedset: gapExhausted")
	// ErrOverflow is the sentinel error for Overflow failures.
	ErrOverflow = errors.New("orderedset: overflow")
)

// Error describes a failure returned by orderedset operations.
type Error struct {
	Kind   ErrorKind
	Key    string
	Detail string
}

// Error formats the error with its kind, offending key, and detail.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	msg := "orderedset: " + e.Kind.String()
	if e.Key != "" {
		msg += ": " + e.Key
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Unwrap returns the sentinel error matching e.Kind.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind.sentinel()
}

// Item pairs an external element key with its payload record for import.
type Item struct {
	Key   string
	Value lkal.Record
}

// Entry holds an extracted element key, its order integer, and its payload record with "order" stripped.
type Entry struct {
	Key   string
	Order int64
	Value lkal.Record
}

type parsedEntry struct {
	key   string
	order int64
	rec   lkal.Record
}

// Entries validates set, strips "order" from each entry payload, and returns entries sorted strictly ascending by Order.
func Entries(set lkal.Record) ([]Entry, error) {
	if !uniqueRecord(set) {
		return nil, &Error{Kind: Malformed, Detail: "duplicate field key or invalid value"}
	}

	// Scan all entries in phase 1 before checking duplicates so error class is field-order invariant.
	parsed := make([]parsedEntry, len(set.Fields))
	for i, fld := range set.Fields {
		if !validKey(fld.Key) {
			return nil, &Error{Kind: Malformed, Key: fld.Key, Detail: "invalid entry key"}
		}
		rec, ok := fld.Value.(lkal.Record)
		if !ok {
			return nil, &Error{Kind: Malformed, Key: fld.Key, Detail: "entry value must be a record"}
		}
		ordVal, ok := findField(rec, OrderField)
		if !ok {
			return nil, &Error{Kind: Malformed, Key: fld.Key, Detail: "missing order field"}
		}
		ord, ok := ordVal.(lkal.Int)
		if !ok {
			return nil, &Error{Kind: Malformed, Key: fld.Key, Detail: "order field must be an integer"}
		}
		parsed[i] = parsedEntry{key: fld.Key, order: int64(ord), rec: rec}
	}

	seenOrders := make(map[int64]string, len(parsed))
	for _, p := range parsed {
		if prev, dup := seenOrders[p.order]; dup {
			return nil, &Error{
				Kind:   DuplicateOrder,
				Key:    p.key,
				Detail: fmt.Sprintf("order %d duplicates entry %q", p.order, prev),
			}
		}
		seenOrders[p.order] = p.key
	}

	slices.SortFunc(parsed, func(a, b parsedEntry) int {
		return cmp.Compare(a.order, b.order)
	})

	out := make([]Entry, len(parsed))
	for i, p := range parsed {
		out[i] = Entry{
			Key:   p.key,
			Order: p.order,
			Value: stripField(p.rec, OrderField),
		}
	}
	return out, nil
}

// EntriesReverse behaves like Entries and returns entries sorted strictly descending by Order.
func EntriesReverse(set lkal.Record) ([]Entry, error) {
	es, err := Entries(set)
	if err != nil {
		return nil, err
	}
	slices.Reverse(es)
	return es, nil
}

// FromItems builds an ordered record set from items, prepending order = Step * (i + 1) to each entry.
func FromItems(items []Item) (lkal.Record, error) {
	for _, item := range items {
		if !validKey(item.Key) {
			return lkal.Record{}, &Error{Kind: Malformed, Key: item.Key, Detail: "invalid entry key"}
		}
		if !uniqueRecord(item.Value) {
			return lkal.Record{}, &Error{Kind: Malformed, Key: item.Key, Detail: "duplicate field key or invalid value"}
		}
	}
	for _, item := range items {
		if hasField(item.Value, OrderField) {
			return lkal.Record{}, &Error{Kind: ReservedField, Key: item.Key, Detail: "input record already contains order field"}
		}
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, dup := seen[item.Key]; dup {
			return lkal.Record{}, &Error{Kind: DuplicateKey, Key: item.Key, Detail: "duplicate entry key"}
		}
		seen[item.Key] = struct{}{}
	}
	if int64(len(items)) > math.MaxInt64/Step {
		return lkal.Record{}, &Error{Kind: Overflow, Detail: "order value overflows int64"}
	}
	if len(items) == 0 {
		return lkal.Record{}, nil
	}

	fields := make([]lkal.Field, len(items))
	for i, item := range items {
		recFields := make([]lkal.Field, 0, len(item.Value.Fields)+1)
		recFields = append(recFields, lkal.Field{Key: OrderField, Value: lkal.Int(Step * int64(i+1))})
		for _, fld := range item.Value.Fields {
			recFields = append(recFields, lkal.Field{Key: fld.Key, Value: cloneValue(fld.Value)})
		}
		fields[i] = lkal.Field{Key: item.Key, Value: lkal.Record{Fields: recFields}}
	}
	return lkal.Record{Fields: fields}, nil
}

// FromKeyedSlice builds an ordered record set by lifting string field keyField from each element as its map key.
func FromKeyedSlice(elems []lkal.Record, keyField string) (lkal.Record, error) {
	if !validKeyField(keyField) {
		return lkal.Record{}, &Error{Kind: Malformed, Key: keyField, Detail: "invalid key field name"}
	}

	keys := make([]string, len(elems))
	for i, elem := range elems {
		if !uniqueRecord(elem) {
			return lkal.Record{}, &Error{Kind: Malformed, Detail: "duplicate field key or invalid value"}
		}
		kv, ok := findField(elem, keyField)
		if !ok {
			return lkal.Record{}, &Error{Kind: Malformed, Key: keyField, Detail: "missing key field"}
		}
		ks, ok := kv.(lkal.String)
		if !ok {
			return lkal.Record{}, &Error{Kind: Malformed, Key: keyField, Detail: "key field must be a string"}
		}
		if !validKey(string(ks)) {
			return lkal.Record{}, &Error{Kind: Malformed, Key: string(ks), Detail: "invalid entry key"}
		}
		keys[i] = string(ks)
	}
	for i, elem := range elems {
		if hasField(elem, OrderField) {
			return lkal.Record{}, &Error{Kind: ReservedField, Key: keys[i], Detail: "input record already contains order field"}
		}
	}
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if _, dup := seen[k]; dup {
			return lkal.Record{}, &Error{Kind: DuplicateKey, Key: k, Detail: "duplicate entry key"}
		}
		seen[k] = struct{}{}
	}
	if int64(len(elems)) > math.MaxInt64/Step {
		return lkal.Record{}, &Error{Kind: Overflow, Detail: "order value overflows int64"}
	}
	if len(elems) == 0 {
		return lkal.Record{}, nil
	}

	fields := make([]lkal.Field, len(elems))
	for i, elem := range elems {
		recFields := make([]lkal.Field, 0, len(elem.Fields))
		recFields = append(recFields, lkal.Field{Key: OrderField, Value: lkal.Int(Step * int64(i+1))})
		for _, fld := range elem.Fields {
			if fld.Key == keyField {
				continue
			}
			recFields = append(recFields, lkal.Field{Key: fld.Key, Value: cloneValue(fld.Value)})
		}
		fields[i] = lkal.Field{Key: keys[i], Value: lkal.Record{Fields: recFields}}
	}
	return lkal.Record{Fields: fields}, nil
}

// ToKeyedSlice projects set into ascending-order records with keyField restored as a leading string field.
func ToKeyedSlice(set lkal.Record, keyField string) ([]lkal.Record, error) {
	return toKeyedSlice(set, keyField, false)
}

// ToKeyedSliceReverse projects set into descending-order records with keyField restored as a leading string field.
func ToKeyedSliceReverse(set lkal.Record, keyField string) ([]lkal.Record, error) {
	return toKeyedSlice(set, keyField, true)
}

func toKeyedSlice(set lkal.Record, keyField string, reverse bool) ([]lkal.Record, error) {
	if !validKeyField(keyField) {
		return nil, &Error{Kind: Malformed, Key: keyField, Detail: "invalid key field name"}
	}
	var (
		es  []Entry
		err error
	)
	if reverse {
		es, err = EntriesReverse(set)
	} else {
		es, err = Entries(set)
	}
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		if hasField(e.Value, keyField) {
			return nil, &Error{
				Kind:   ReservedField,
				Key:    e.Key,
				Detail: fmt.Sprintf("entry already contains key field %q", keyField),
			}
		}
	}

	out := make([]lkal.Record, len(es))
	for i, e := range es {
		recFields := make([]lkal.Field, 0, len(e.Value.Fields)+1)
		recFields = append(recFields, lkal.Field{Key: keyField, Value: lkal.String(e.Key)})
		recFields = append(recFields, e.Value.Fields...)
		out[i] = lkal.Record{Fields: recFields}
	}
	return out, nil
}

// OrderBefore returns first - Step, or ErrOverflow if the result would underflow int64.
func OrderBefore(first int64) (int64, error) {
	if first < math.MinInt64+Step {
		return 0, &Error{Kind: Overflow, Detail: "order before first underflows int64"}
	}
	return first - Step, nil
}

// OrderAfter returns last + Step, or ErrOverflow if the result would overflow int64.
func OrderAfter(last int64) (int64, error) {
	if last > math.MaxInt64-Step {
		return 0, &Error{Kind: Overflow, Detail: "order after last overflows int64"}
	}
	return last + Step, nil
}

// OrderBetween returns the exact floor midpoint strictly between lo and hi without int64 overflow.
func OrderBetween(lo, hi int64) (int64, error) {
	if lo >= hi {
		return 0, &Error{Kind: Malformed, Detail: "lo must be strictly less than hi"}
	}
	if lo == hi-1 {
		return 0, &Error{Kind: GapExhausted, Detail: "no integer strictly between adjacent orders"}
	}
	// Bitwise floor average avoids signed overflow across the full int64 domain.
	return (lo & hi) + ((lo ^ hi) >> 1), nil
}

// Renumber reassigns sparse orders 10, 20, ... to set while preserving current ascending order and payloads.
func Renumber(set lkal.Record) (lkal.Record, error) {
	es, err := Entries(set)
	if err != nil {
		return lkal.Record{}, err
	}
	items := make([]Item, len(es))
	for i, e := range es {
		items[i] = Item{Key: e.Key, Value: e.Value}
	}
	return FromItems(items)
}

func validKey(s string) bool {
	return len(s) > 0 && s[0] != '$' && !strings.Contains(s, "/")
}

func validKeyField(kf string) bool {
	return validKey(kf) && kf != OrderField
}

func uniqueRecord(rec lkal.Record) bool {
	return uniqueValue(rec)
}

func uniqueValue(v lkal.Value) bool {
	switch val := v.(type) {
	case lkal.String, lkal.Int, lkal.Decimal, lkal.Bool, lkal.Enum, lkal.Quantity, lkal.Ref:
		return true
	case lkal.List:
		for _, elem := range val {
			if !uniqueValue(elem) {
				return false
			}
		}
		return true
	case lkal.Record:
		seen := make(map[string]struct{}, len(val.Fields))
		for _, fld := range val.Fields {
			if _, dup := seen[fld.Key]; dup {
				return false
			}
			seen[fld.Key] = struct{}{}
			if !uniqueValue(fld.Value) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func findField(rec lkal.Record, key string) (lkal.Value, bool) {
	for _, fld := range rec.Fields {
		if fld.Key == key {
			return fld.Value, true
		}
	}
	return nil, false
}

func hasField(rec lkal.Record, key string) bool {
	_, ok := findField(rec, key)
	return ok
}

func stripField(rec lkal.Record, target string) lkal.Record {
	out := make([]lkal.Field, 0, len(rec.Fields))
	for _, fld := range rec.Fields {
		if fld.Key == target {
			continue
		}
		out = append(out, lkal.Field{Key: fld.Key, Value: cloneValue(fld.Value)})
	}
	if len(out) == 0 {
		return lkal.Record{}
	}
	return lkal.Record{Fields: out}
}

func cloneRecord(rec lkal.Record) lkal.Record {
	if len(rec.Fields) == 0 {
		return lkal.Record{}
	}
	fields := make([]lkal.Field, len(rec.Fields))
	for i, fld := range rec.Fields {
		fields[i] = lkal.Field{Key: fld.Key, Value: cloneValue(fld.Value)}
	}
	return lkal.Record{Fields: fields}
}

func cloneValue(v lkal.Value) lkal.Value {
	switch val := v.(type) {
	case lkal.Record:
		return cloneRecord(val)
	case lkal.List:
		if len(val) == 0 {
			return lkal.List(nil)
		}
		out := make(lkal.List, len(val))
		for i, elem := range val {
			out[i] = cloneValue(elem)
		}
		return out
	case lkal.Ref:
		return lkal.Ref(slices.Clone(val))
	default:
		return v
	}
}
