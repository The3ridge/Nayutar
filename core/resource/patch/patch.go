// Package patch implements pure ordered-set patch operations, diffing, wire coding,
// and two-entrance envelope patch transitions over the LKAL record data model.
package patch

import (
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/orderedset"
)

// PatchField is the single top-level record field holding the ordered set of patch operations.
const PatchField = "patch"

const emptyLKALJSON = `{"$lkal":"0.1"}`

// OpKind identifies one of the three patch operations.
type OpKind uint8

const (
	// OpTest asserts that the target path exists and equals Value under lkal.Equal.
	OpTest OpKind = iota + 1
	// OpSet replaces or creates the value at Path, creating absent intermediate records.
	OpSet
	// OpDel removes the leaf field at Path, retaining empty parent records.
	OpDel
)

// String returns the canonical LKAL enum name of k.
func (k OpKind) String() string {
	switch k {
	case OpTest:
		return "test"
	case OpSet:
		return "set"
	case OpDel:
		return "del"
	default:
		return fmt.Sprintf("unknown(%d)", k)
	}
}

// Op is one operation in an ordered patch sequence.
// Value must be nil for OpDel and non-nil for OpTest and OpSet.
type Op struct {
	Kind  OpKind
	Path  string
	Value lkal.Value
}

// Entrance selects which resource envelope write entrance an ApplyEntrance call enforces.
type Entrance uint8

const (
	// EntranceSpec permits writes under meta/labels and spec.
	EntranceSpec Entrance = iota + 1
	// EntranceStatus permits writes under status except status/condition/<Type>/since.
	EntranceStatus
)

// String returns the canonical name of e.
func (e Entrance) String() string {
	switch e {
	case EntranceSpec:
		return "spec"
	case EntranceStatus:
		return "status"
	default:
		return fmt.Sprintf("unknown(%d)", e)
	}
}

// ErrorKind classifies failures returned by patch operations.
type ErrorKind uint8

const (
	// Internal indicates a server-supplied input (stored envelope, revision, entrance, or timestamp) is invalid.
	Internal ErrorKind = iota + 1
	// Malformed indicates a structurally invalid patch, path, document, or non-record intermediate traversal.
	Malformed
	// Conflict indicates a test operation did not match or a revision mismatch occurred.
	Conflict
	// NotFound indicates a delete operation targeted an absent field or intermediate segment.
	NotFound
	// Forbidden indicates an operation attempted to modify a field outside its entrance or a server-managed field.
	Forbidden
	// Observed indicates observedGeneration would regress, vanish, or exceed generation.
	Observed
)

// String returns the canonical camelCase name of k.
func (k ErrorKind) String() string {
	switch k {
	case Internal:
		return "internal"
	case Malformed:
		return "malformed"
	case Conflict:
		return "conflict"
	case NotFound:
		return "notFound"
	case Forbidden:
		return "forbidden"
	case Observed:
		return "observed"
	default:
		return fmt.Sprintf("unknown(%d)", k)
	}
}

func (k ErrorKind) sentinel() error {
	switch k {
	case Internal:
		return ErrInternal
	case Malformed:
		return ErrMalformed
	case Conflict:
		return ErrConflict
	case NotFound:
		return ErrNotFound
	case Forbidden:
		return ErrForbidden
	case Observed:
		return ErrObserved
	default:
		return nil
	}
}

var (
	// ErrInternal is the sentinel error for Internal failures.
	ErrInternal = errors.New("patch: internal")
	// ErrMalformed is the sentinel error for Malformed failures.
	ErrMalformed = errors.New("patch: malformed")
	// ErrConflict is the sentinel error for Conflict failures.
	ErrConflict = errors.New("patch: conflict")
	// ErrNotFound is the sentinel error for NotFound failures.
	ErrNotFound = errors.New("patch: notFound")
	// ErrForbidden is the sentinel error for Forbidden failures.
	ErrForbidden = errors.New("patch: forbidden")
	// ErrObserved is the sentinel error for Observed failures.
	ErrObserved = errors.New("patch: observed")
)

// Error describes a failure returned by patch operations.
type Error struct {
	Kind   ErrorKind
	Path   string
	Detail string
}

// Error formats the error with its kind, path, and detail.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	msg := "patch: " + e.Kind.String()
	if e.Path != "" {
		msg += ": " + e.Path
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

// ValidSeg reports whether s is a valid single patch path segment:
// non-empty, not starting with '$', and containing no '/'.
func ValidSeg(s string) bool {
	return len(s) > 0 && s[0] != '$' && !strings.Contains(s, "/")
}

// ParsePath splits path on '/' and verifies that every segment satisfies ValidSeg.
func ParsePath(path string) ([]string, bool) {
	if path == "" {
		return nil, false
	}
	segs := strings.Split(path, "/")
	for _, seg := range segs {
		if !ValidSeg(seg) {
			return nil, false
		}
	}
	return segs, true
}

// Apply validates doc and ops upfront, then applies ops sequentially and atomically to a clone of doc.
func Apply(doc lkal.Record, ops []Op) (lkal.Record, error) {
	if !uniqueRecord(doc) {
		return lkal.Record{}, &Error{Kind: Malformed, Detail: "duplicate field key or invalid value in document"}
	}
	if err := validateOps(ops); err != nil {
		return lkal.Record{}, err
	}

	cur := cloneRecord(doc)
	for _, op := range ops {
		segs, _ := ParsePath(op.Path)
		switch op.Kind {
		case OpTest:
			actual, ok := lookupAt(cur, segs)
			if !ok || !lkal.Equal(actual, op.Value) {
				return lkal.Record{}, &Error{Kind: Conflict, Path: op.Path, Detail: "test value mismatch"}
			}
		case OpSet:
			next, err := setAt(cur, segs, cloneValue(op.Value), op.Path)
			if err != nil {
				return lkal.Record{}, err
			}
			cur = next
		case OpDel:
			next, err := deleteAt(cur, segs, op.Path)
			if err != nil {
				return lkal.Record{}, err
			}
			cur = next
		default:
			return lkal.Record{}, &Error{Kind: Malformed, Path: op.Path, Detail: "unknown operation kind"}
		}
	}
	return cur, nil
}

// Diff computes a deterministic, field-order-invariant sequence of OpDel and OpSet operations
// transforming old into a record equal to next under lkal.Equal.
func Diff(old, next lkal.Record) ([]Op, error) {
	if !uniqueRecord(old) {
		return nil, &Error{Kind: Malformed, Detail: "duplicate field key or invalid value in old document"}
	}
	if !uniqueRecord(next) {
		return nil, &Error{Kind: Malformed, Detail: "duplicate field key or invalid value in next document"}
	}
	if !topSegsOk(old) {
		return nil, &Error{Kind: Malformed, Detail: "invalid top-level key in old document"}
	}
	if !topSegsOk(next) {
		return nil, &Error{Kind: Malformed, Detail: "invalid top-level key in next document"}
	}
	return diffFields(nil, old, next), nil
}

func diffFields(prefix []string, old, next lkal.Record) []Op {
	var dels []Op
	for _, k := range sortedKeys(old) {
		if !hasField(next, k) {
			dels = append(dels, Op{Kind: OpDel, Path: joinPath(prefix, k)})
		}
	}

	var sets []Op
	for _, k := range sortedKeys(next) {
		nv, _ := findField(next, k)
		ov, hasOld := findField(old, k)
		if !hasOld {
			sets = append(sets, Op{Kind: OpSet, Path: joinPath(prefix, k), Value: cloneValue(nv)})
			continue
		}
		if lkal.Equal(ov, nv) {
			continue
		}
		osub, oIsRec := ov.(lkal.Record)
		nsub, nIsRec := nv.(lkal.Record)
		if oIsRec && nIsRec && (len(osub.Fields) > 0 || len(nsub.Fields) > 0) && topSegsOk(osub) && topSegsOk(nsub) {
			childPrefix := make([]string, len(prefix)+1)
			copy(childPrefix, prefix)
			childPrefix[len(prefix)] = k
			sets = append(sets, diffFields(childPrefix, osub, nsub)...)
			continue
		}
		sets = append(sets, Op{Kind: OpSet, Path: joinPath(prefix, k), Value: cloneValue(nv)})
	}
	return append(dels, sets...)
}

// ToRecord encodes ops into a single-field LKAL record {"patch": <ordered set>}.
func ToRecord(ops []Op) (lkal.Record, error) {
	if err := validateOps(ops); err != nil {
		return lkal.Record{}, err
	}
	items := make([]orderedset.Item, len(ops))
	for i, op := range ops {
		fields := make([]lkal.Field, 0, 3)
		fields = append(fields,
			lkal.Field{Key: "op", Value: lkal.Enum(op.Kind.String())},
			lkal.Field{Key: "path", Value: lkal.String(op.Path)},
		)
		if op.Value != nil {
			fields = append(fields, lkal.Field{Key: "value", Value: cloneValue(op.Value)})
		}
		items[i] = orderedset.Item{
			Key:   strconv.Itoa(i),
			Value: lkal.Record{Fields: fields},
		}
	}
	setRec, err := orderedset.FromItems(items)
	if err != nil {
		return lkal.Record{}, &Error{Kind: Malformed, Path: PatchField, Detail: err.Error()}
	}
	return lkal.Record{
		Fields: []lkal.Field{{Key: PatchField, Value: setRec}},
	}, nil
}

// FromRecord decodes a single-field LKAL record {"patch": <ordered set>} into its ordered operations.
func FromRecord(doc lkal.Record) ([]Op, error) {
	if !uniqueRecord(doc) {
		return nil, &Error{Kind: Malformed, Detail: "duplicate field key or invalid value in patch document"}
	}
	if len(doc.Fields) != 1 || doc.Fields[0].Key != PatchField {
		return nil, &Error{Kind: Malformed, Detail: "patch document must contain only the patch field"}
	}
	patchSet, ok := doc.Fields[0].Value.(lkal.Record)
	if !ok {
		return nil, &Error{Kind: Malformed, Path: PatchField, Detail: "patch field must be a record"}
	}
	entries, err := orderedset.Entries(patchSet)
	if err != nil {
		return nil, &Error{Kind: Malformed, Path: PatchField, Detail: err.Error()}
	}

	ops := make([]Op, 0, len(entries))
	for _, entry := range entries {
		entryPath := PatchField + "/" + entry.Key
		opVal, hasOp := findField(entry.Value, "op")
		if !hasOp {
			return nil, &Error{Kind: Malformed, Path: entryPath + "/op", Detail: "missing required op"}
		}
		opEnum, ok := opVal.(lkal.Enum)
		if !ok {
			return nil, &Error{Kind: Malformed, Path: entryPath + "/op", Detail: "op must be an enum"}
		}
		var kind OpKind
		switch string(opEnum) {
		case "test":
			kind = OpTest
		case "set":
			kind = OpSet
		case "del":
			kind = OpDel
		default:
			return nil, &Error{Kind: Malformed, Path: entryPath + "/op", Detail: fmt.Sprintf("unknown op %q", string(opEnum))}
		}

		pathVal, hasPath := findField(entry.Value, "path")
		if !hasPath {
			return nil, &Error{Kind: Malformed, Path: entryPath + "/path", Detail: "missing required path"}
		}
		pathStr, ok := pathVal.(lkal.String)
		if !ok {
			return nil, &Error{Kind: Malformed, Path: entryPath + "/path", Detail: "path must be a string"}
		}
		if _, ok := ParsePath(string(pathStr)); !ok {
			return nil, &Error{Kind: Malformed, Path: entryPath + "/path", Detail: fmt.Sprintf("invalid path %q", string(pathStr))}
		}

		val, hasVal := findField(entry.Value, "value")
		switch kind {
		case OpDel:
			if hasVal || len(entry.Value.Fields) != 2 {
				return nil, &Error{Kind: Malformed, Path: entryPath, Detail: "del operation must contain only op and path"}
			}
			ops = append(ops, Op{Kind: OpDel, Path: string(pathStr)})
		case OpTest, OpSet:
			if !hasVal || len(entry.Value.Fields) != 3 {
				return nil, &Error{Kind: Malformed, Path: entryPath, Detail: fmt.Sprintf("%s operation must contain only op, path, and value", kind)}
			}
			ops = append(ops, Op{Kind: kind, Path: string(pathStr), Value: cloneValue(val)})
		}
	}
	return ops, nil
}

// Encode serializes ops into compact single-line LKAL-JSON starting with "$lkal":"0.1".
func Encode(ops []Op) ([]byte, error) {
	rec, err := ToRecord(ops)
	if err != nil {
		return nil, err
	}
	f, err := lkal.FromJSON([]byte(emptyLKALJSON))
	if err != nil {
		return nil, &Error{Kind: Internal, Detail: err.Error()}
	}
	if err := f.Set(PatchField, rec.Fields[0].Value); err != nil {
		return nil, &Error{Kind: Malformed, Path: PatchField, Detail: err.Error()}
	}
	raw, err := lkal.ToJSON(f, false)
	if err != nil {
		return nil, &Error{Kind: Malformed, Detail: err.Error()}
	}
	v := jsontext.Value(raw)
	if err := v.Compact(); err != nil {
		return nil, &Error{Kind: Internal, Detail: err.Error()}
	}
	return []byte(v), nil
}

// Decode parses compact or pretty LKAL-JSON, validates it against Schema(), and decodes its ordered operations.
func Decode(data []byte) ([]Op, error) {
	f, err := lkal.FromJSON(data)
	if err != nil {
		return nil, toMalformedErr(err)
	}
	if err := Schema().Validate(f); err != nil {
		return nil, toMalformedErr(err)
	}
	return FromRecord(f.Data())
}

// ApplyEntrance validates stored at rev, injects meta/revision = rev, and applies ops
// while enforcing the write boundaries of entrance.
func ApplyEntrance(entrance Entrance, stored lkal.Record, rev int64, ops []Op) (lkal.Record, error) {
	if entrance != EntranceSpec && entrance != EntranceStatus {
		return lkal.Record{}, &Error{Kind: Internal, Detail: fmt.Sprintf("invalid entrance %d", entrance)}
	}
	if err := envelope.CheckStored(stored); err != nil {
		return lkal.Record{}, mapEnvelopeError(err)
	}
	if rev < 1 {
		return lkal.Record{}, &Error{Kind: Internal, Path: "meta/revision", Detail: "must be >= 1"}
	}
	if err := validateOps(ops); err != nil {
		return lkal.Record{}, err
	}

	cur := envelope.WithRevision(stored, rev)
	for _, op := range ops {
		segs, _ := ParsePath(op.Path)
		switch op.Kind {
		case OpTest:
			actual, ok := lookupAt(cur, segs)
			if !ok || !lkal.Equal(actual, op.Value) {
				return lkal.Record{}, &Error{Kind: Conflict, Path: op.Path, Detail: "test value mismatch"}
			}
		case OpSet:
			if err := entranceAllows(entrance, segs, op.Path); err != nil {
				return lkal.Record{}, err
			}
			next, err := setAt(cur, segs, cloneValue(op.Value), op.Path)
			if err != nil {
				return lkal.Record{}, err
			}
			cur = next
		case OpDel:
			if err := entranceAllows(entrance, segs, op.Path); err != nil {
				return lkal.Record{}, err
			}
			next, err := deleteAt(cur, segs, op.Path)
			if err != nil {
				return lkal.Record{}, err
			}
			cur = next
		default:
			return lkal.Record{}, &Error{Kind: Malformed, Path: op.Path, Detail: "unknown operation kind"}
		}
	}
	return cur, nil
}

// Patch applies ops through EntranceSpec against stored at rev and admits the resulting envelope via envelope.Update.
func Patch(stored lkal.Record, rev int64, ops []Op) (lkal.Record, error) {
	cand, err := ApplyEntrance(EntranceSpec, stored, rev, ops)
	if err != nil {
		return lkal.Record{}, err
	}
	next, err := envelope.Update(stored, rev, cand)
	if err != nil {
		return lkal.Record{}, mapEnvelopeError(err)
	}
	return next, nil
}

// PatchStatus applies ops through EntranceStatus against stored at rev and admits the resulting envelope via envelope.UpdateStatus.
func PatchStatus(stored lkal.Record, rev int64, ops []Op, now string) (lkal.Record, error) {
	if err := envelope.CheckStored(stored); err != nil {
		return lkal.Record{}, mapEnvelopeError(err)
	}
	if rev < 1 {
		return lkal.Record{}, &Error{Kind: Internal, Path: "meta/revision", Detail: "must be >= 1"}
	}
	if !isTimestamp(now) {
		return lkal.Record{}, &Error{Kind: Internal, Path: "now", Detail: "invalid timestamp"}
	}
	cand, err := ApplyEntrance(EntranceStatus, stored, rev, ops)
	if err != nil {
		return lkal.Record{}, err
	}
	next, err := envelope.UpdateStatus(stored, rev, cand, now)
	if err != nil {
		return lkal.Record{}, mapEnvelopeError(err)
	}
	return next, nil
}

func entranceAllows(entrance Entrance, segs []string, fullPath string) error {
	if len(segs) == 0 {
		return &Error{Kind: Malformed, Path: fullPath, Detail: "empty path"}
	}
	root := segs[0]
	if root != "meta" && root != "spec" && root != "status" {
		return &Error{Kind: Malformed, Path: fullPath, Detail: fmt.Sprintf("unexpected top-level field %q", root)}
	}
	switch entrance {
	case EntranceSpec:
		if (root == "meta" && len(segs) >= 2 && segs[1] == "labels") || root == "spec" {
			return nil
		}
		return &Error{Kind: Forbidden, Path: fullPath, Detail: "spec entrance cannot modify this path"}
	case EntranceStatus:
		if root == "status" && (len(segs) < 4 || segs[1] != "condition" || segs[3] != "since") {
			return nil
		}
		return &Error{Kind: Forbidden, Path: fullPath, Detail: "status entrance cannot modify this path"}
	default:
		return &Error{Kind: Internal, Detail: fmt.Sprintf("invalid entrance %d", entrance)}
	}
}

func validateOps(ops []Op) error {
	for i, op := range ops {
		if _, ok := ParsePath(op.Path); !ok {
			return &Error{Kind: Malformed, Path: op.Path, Detail: fmt.Sprintf("op[%d]: invalid path", i)}
		}
		switch op.Kind {
		case OpDel:
			if op.Value != nil {
				return &Error{Kind: Malformed, Path: op.Path, Detail: fmt.Sprintf("op[%d]: del must not carry a value", i)}
			}
		case OpTest, OpSet:
			if op.Value == nil || !uniqueValue(op.Value) {
				return &Error{Kind: Malformed, Path: op.Path, Detail: fmt.Sprintf("op[%d]: %s requires a unique non-nil value", i, op.Kind)}
			}
		default:
			return &Error{Kind: Malformed, Path: op.Path, Detail: fmt.Sprintf("op[%d]: unknown op kind %d", i, op.Kind)}
		}
	}
	return nil
}

func setAt(rec lkal.Record, segs []string, val lkal.Value, fullPath string) (lkal.Record, error) {
	if len(segs) == 0 {
		return lkal.Record{}, &Error{Kind: Malformed, Path: fullPath, Detail: "empty path"}
	}
	k := segs[0]
	if len(segs) == 1 {
		if hasField(rec, k) {
			return replaceField(rec, k, val), nil
		}
		return appendField(rec, k, val), nil
	}
	rest := segs[1:]
	existing, ok := findField(rec, k)
	if !ok {
		sub, err := setAt(lkal.Record{}, rest, val, fullPath)
		if err != nil {
			return lkal.Record{}, err
		}
		return appendField(rec, k, sub), nil
	}
	subRec, isRec := existing.(lkal.Record)
	if !isRec {
		return lkal.Record{}, &Error{Kind: Malformed, Path: fullPath, Detail: fmt.Sprintf("intermediate segment %q is not a record", k)}
	}
	sub, err := setAt(subRec, rest, val, fullPath)
	if err != nil {
		return lkal.Record{}, err
	}
	return replaceField(rec, k, sub), nil
}

func deleteAt(rec lkal.Record, segs []string, fullPath string) (lkal.Record, error) {
	if len(segs) == 0 {
		return lkal.Record{}, &Error{Kind: Malformed, Path: fullPath, Detail: "empty path"}
	}
	k := segs[0]
	if len(segs) == 1 {
		if hasField(rec, k) {
			return removeField(rec, k), nil
		}
		return lkal.Record{}, &Error{Kind: NotFound, Path: fullPath, Detail: "field not found"}
	}
	rest := segs[1:]
	existing, ok := findField(rec, k)
	if !ok {
		return lkal.Record{}, &Error{Kind: NotFound, Path: fullPath, Detail: fmt.Sprintf("intermediate segment %q not found", k)}
	}
	subRec, isRec := existing.(lkal.Record)
	if !isRec {
		return lkal.Record{}, &Error{Kind: Malformed, Path: fullPath, Detail: fmt.Sprintf("intermediate segment %q is not a record", k)}
	}
	sub, err := deleteAt(subRec, rest, fullPath)
	if err != nil {
		return lkal.Record{}, err
	}
	return replaceField(rec, k, sub), nil
}

func lookupAt(doc lkal.Record, segs []string) (lkal.Value, bool) {
	var cur lkal.Value = doc
	for _, seg := range segs {
		rec, ok := cur.(lkal.Record)
		if !ok {
			return nil, false
		}
		next, found := findField(rec, seg)
		if !found {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func replaceField(rec lkal.Record, key string, val lkal.Value) lkal.Record {
	fields := make([]lkal.Field, len(rec.Fields))
	for i, fld := range rec.Fields {
		if fld.Key == key {
			fields[i] = lkal.Field{Key: key, Value: val}
		} else {
			fields[i] = fld
		}
	}
	return lkal.Record{Fields: fields}
}

func appendField(rec lkal.Record, key string, val lkal.Value) lkal.Record {
	fields := make([]lkal.Field, len(rec.Fields)+1)
	copy(fields, rec.Fields)
	fields[len(rec.Fields)] = lkal.Field{Key: key, Value: val}
	return lkal.Record{Fields: fields}
}

func removeField(rec lkal.Record, key string) lkal.Record {
	out := make([]lkal.Field, 0, len(rec.Fields))
	for _, fld := range rec.Fields {
		if fld.Key == key {
			continue
		}
		out = append(out, fld)
	}
	if len(out) == 0 {
		return lkal.Record{}
	}
	return lkal.Record{Fields: out}
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

func topSegsOk(rec lkal.Record) bool {
	for _, fld := range rec.Fields {
		if !ValidSeg(fld.Key) {
			return false
		}
	}
	return true
}

func sortedKeys(rec lkal.Record) []string {
	keys := make([]string, len(rec.Fields))
	for i, fld := range rec.Fields {
		keys[i] = fld.Key
	}
	slices.Sort(keys)
	return keys
}

func joinPath(prefix []string, leaf string) string {
	if len(prefix) == 0 {
		return leaf
	}
	return strings.Join(prefix, "/") + "/" + leaf
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

func mapEnvelopeError(err error) error {
	var ee *envelope.Error
	if errors.As(err, &ee) {
		var kind ErrorKind
		switch ee.Kind {
		case envelope.ErrInternal:
			kind = Internal
		case envelope.ErrMalformed:
			kind = Malformed
		case envelope.ErrConflict:
			kind = Conflict
		case envelope.ErrForbidden:
			kind = Forbidden
		case envelope.ErrObserved:
			kind = Observed
		default:
			kind = Internal
		}
		return &Error{Kind: kind, Path: ee.Path, Detail: ee.Detail}
	}
	return &Error{Kind: Internal, Detail: err.Error()}
}

func toMalformedErr(err error) error {
	var el *lkal.ErrorList
	if errors.As(err, &el) && len(el.Errors) > 0 {
		first := el.Errors[0]
		detail := first.Code + " " + first.Msg
		if first.Hint != "" {
			detail += ": " + first.Hint
		}
		return &Error{Kind: Malformed, Path: first.Path, Detail: detail}
	}
	return &Error{Kind: Malformed, Detail: err.Error()}
}

func isTimestamp(s string) bool {
	if len(s) != 20 {
		return false
	}
	if s[4] != '-' || s[7] != '-' || s[10] != 'T' || s[13] != ':' || s[16] != ':' || s[19] != 'Z' {
		return false
	}
	for _, idx := range []int{0, 1, 2, 3, 5, 6, 8, 9, 11, 12, 14, 15, 17, 18} {
		if s[idx] < '0' || s[idx] > '9' {
			return false
		}
	}
	month := twoDigits(s[5], s[6])
	day := twoDigits(s[8], s[9])
	hour := twoDigits(s[11], s[12])
	minute := twoDigits(s[14], s[15])
	second := twoDigits(s[17], s[18])
	return month >= 1 && month <= 12 &&
		day >= 1 && day <= 31 &&
		hour >= 0 && hour <= 23 &&
		minute >= 0 && minute <= 59 &&
		second >= 0 && second <= 59
}

func twoDigits(hi, lo byte) int {
	return int(hi-'0')*10 + int(lo-'0')
}
