// Package envelope validates resource envelopes and implements the Create, Update, and UpdateStatus admission transitions.
package envelope

import (
	"errors"
	"math"
	"slices"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

var (
	// ErrInternal indicates the stored document or a server-supplied input is not well formed.
	ErrInternal = errors.New("envelope: internal")
	// ErrMalformed indicates the candidate violates the envelope shape.
	ErrMalformed = errors.New("envelope: malformed")
	// ErrConflict indicates the candidate names a revision other than the current one.
	ErrConflict = errors.New("envelope: conflict")
	// ErrForbidden indicates the candidate changes a field its entrance may not change.
	ErrForbidden = errors.New("envelope: forbidden")
	// ErrObserved indicates observedGeneration would regress, vanish, or pass the generation.
	ErrObserved = errors.New("envelope: observed")
)

// Error carries the error class (one of the five sentinels, returned by Unwrap)
// and the "/"-separated path that failed, when there is one.
type Error struct {
	Kind   error
	Path   string
	Detail string
}

// Error formats the error with its kind, path, and detail.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	msg := "envelope: error"
	if e.Kind != nil {
		msg = e.Kind.Error()
	}
	if e.Path != "" {
		msg += ": " + e.Path
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	return msg
}

// Unwrap returns the sentinel error class.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind
}

// CheckShape validates that doc conforms to the envelope shape.
// It checks only the envelope rules and does not verify that the tree is well-formed LKAL (for example, lists of records or mixed types, $-prefixed keys, unitless quantities, and empty refs pass); resource.Objects rejects such values when canonicalizing candidates.
// It returns nil or an *Error with Kind ErrMalformed.
func CheckShape(doc lkal.Record) error {
	if err := checkShape(doc); err != nil {
		return err
	}
	return nil
}

// CheckStored validates that doc conforms to the stored envelope invariants.
// It checks only the envelope rules and does not verify that the tree is well-formed LKAL (for example, lists of records or mixed types, $-prefixed keys, unitless quantities, and empty refs pass); resource.Objects rejects such values when canonicalizing candidates.
// It returns nil or an *Error with Kind ErrInternal.
func CheckStored(doc lkal.Record) error {
	if err := checkStored(doc); err != nil {
		return err
	}
	return nil
}

// Object returns the identity of a shape-valid document, with Group set to
// "core" when meta/group is absent. It returns an *Error with Kind ErrMalformed otherwise.
func Object(doc lkal.Record) (keyspace.Object, error) {
	if err := checkShape(doc); err != nil {
		return keyspace.Object{}, err
	}
	meta, _ := getRecord(doc, "meta")
	group, hasGroup := getString(meta, "group")
	if !hasGroup {
		group = keyspace.CoreGroup
	}
	kind, _ := getString(meta, "kind")
	name, _ := getString(meta, "name")
	return keyspace.Object{Group: group, Kind: kind, Name: name}, nil
}

// Timestamp formats t in UTC as "2006-01-02T15:04:05Z".
func Timestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

// Create validates and admits a new resource envelope with server-assigned uid and generation 1.
func Create(candidate lkal.Record, uid string) (lkal.Record, error) {
	if !isUID(uid) {
		return lkal.Record{}, &Error{Kind: ErrInternal, Path: "uid", Detail: "invalid uid"}
	}
	if err := checkShape(candidate); err != nil {
		return lkal.Record{}, err
	}
	candMeta, _ := getRecord(candidate, "meta")
	if _, ok := getField(candMeta, "uid"); ok {
		return lkal.Record{}, &Error{Kind: ErrForbidden, Path: "meta/uid", Detail: "cannot set uid on create"}
	}
	if _, ok := getField(candMeta, "revision"); ok {
		return lkal.Record{}, &Error{Kind: ErrForbidden, Path: "meta/revision", Detail: "cannot set revision on create"}
	}
	if _, ok := getField(candMeta, "generation"); ok {
		return lkal.Record{}, &Error{Kind: ErrForbidden, Path: "meta/generation", Detail: "cannot set generation on create"}
	}
	if _, ok := getField(candidate, "status"); ok {
		return lkal.Record{}, &Error{Kind: ErrForbidden, Path: "status", Detail: "cannot set status on create"}
	}

	fields := make([]lkal.Field, 0, 2)
	fields = append(fields, lkal.Field{Key: "meta", Value: canonicalMeta(candMeta, uid, 1)})
	if spec, ok := getField(candidate, "spec"); ok {
		fields = append(fields, lkal.Field{Key: "spec", Value: cloneValue(spec)})
	}
	return lkal.Record{Fields: fields}, nil
}

// Update validates and applies a spec/labels update against stored at rev.
func Update(stored lkal.Record, rev int64, candidate lkal.Record) (lkal.Record, error) {
	if err := checkStored(stored); err != nil {
		return lkal.Record{}, err
	}
	if rev < 1 {
		return lkal.Record{}, &Error{Kind: ErrInternal, Path: "revision", Detail: "must be >= 1"}
	}
	if err := checkShape(candidate); err != nil {
		return lkal.Record{}, err
	}

	oldMeta, _ := getRecord(stored, "meta")
	candMeta, _ := getRecord(candidate, "meta")
	if err := checkIdentityRevUIDGen(oldMeta, candMeta, rev); err != nil {
		return lkal.Record{}, err
	}
	oldUID, _ := getString(oldMeta, "uid")
	oldGen, _ := getInt(oldMeta, "generation")
	oldStatus, hasOldStatus := getField(stored, "status")
	if candStatus, hasCandStatus := getField(candidate, "status"); hasCandStatus && !optEqual(oldStatus, hasOldStatus, candStatus, true) {
		return lkal.Record{}, &Error{Kind: ErrForbidden, Path: "status", Detail: "cannot change status"}
	}

	oldSpec, hasOldSpec := getField(stored, "spec")
	candSpec, hasCandSpec := getField(candidate, "spec")
	nextGen := oldGen
	if !optEqual(oldSpec, hasOldSpec, candSpec, hasCandSpec) {
		if oldGen == math.MaxInt64 {
			return lkal.Record{}, &Error{Kind: ErrInternal, Path: "meta/generation", Detail: "generation overflow"}
		}
		nextGen++
	}

	fields := make([]lkal.Field, 0, 3)
	fields = append(fields, lkal.Field{Key: "meta", Value: canonicalMeta(candMeta, oldUID, nextGen)})
	if hasCandSpec {
		fields = append(fields, lkal.Field{Key: "spec", Value: cloneValue(candSpec)})
	}
	if hasOldStatus {
		fields = append(fields, lkal.Field{Key: "status", Value: cloneValue(oldStatus)})
	}
	return lkal.Record{Fields: fields}, nil
}

// UpdateStatus validates and applies a status update against stored at rev using timestamp now.
func UpdateStatus(stored lkal.Record, rev int64, candidate lkal.Record, now string) (lkal.Record, error) {
	if err := checkStored(stored); err != nil {
		return lkal.Record{}, err
	}
	if rev < 1 {
		return lkal.Record{}, &Error{Kind: ErrInternal, Path: "revision", Detail: "must be >= 1"}
	}
	if !isTimestamp(now) {
		return lkal.Record{}, &Error{Kind: ErrInternal, Path: "now", Detail: "invalid timestamp"}
	}
	if err := checkShape(candidate); err != nil {
		return lkal.Record{}, err
	}

	oldMeta, _ := getRecord(stored, "meta")
	candMeta, _ := getRecord(candidate, "meta")
	if err := checkIdentityRevUIDGen(oldMeta, candMeta, rev); err != nil {
		return lkal.Record{}, err
	}
	oldGen, _ := getInt(oldMeta, "generation")
	oldLabels, hasOldLabels := getField(oldMeta, "labels")
	if candLabels, hasCandLabels := getField(candMeta, "labels"); hasCandLabels && !optEqual(oldLabels, hasOldLabels, candLabels, true) {
		return lkal.Record{}, &Error{Kind: ErrForbidden, Path: "meta/labels", Detail: "cannot change labels"}
	}
	oldSpec, hasOldSpec := getField(stored, "spec")
	if candSpec, hasCandSpec := getField(candidate, "spec"); hasCandSpec && !optEqual(oldSpec, hasOldSpec, candSpec, true) {
		return lkal.Record{}, &Error{Kind: ErrForbidden, Path: "spec", Detail: "cannot change spec"}
	}

	oldStatus, hasOldStatus := getRecord(stored, "status")
	var oldCond lkal.Record
	if hasOldStatus {
		oldCond, _ = getRecord(oldStatus, "condition")
	}
	candStatus, hasCandStatus := getRecord(candidate, "status")
	var candCond lkal.Record
	var hasCandCond bool
	if hasCandStatus {
		candCond, hasCandCond = getRecord(candStatus, "condition")
	}
	if hasCandCond {
		for _, cf := range candCond.Fields {
			c := cf.Value.(lkal.Record)
			candSince, hasSince := getString(c, "since")
			if !hasSince {
				continue
			}
			oldC, ok := getRecord(oldCond, cf.Key)
			if !ok {
				return lkal.Record{}, &Error{Kind: ErrForbidden, Path: "status/condition/" + cf.Key + "/since", Detail: "cannot set since on new condition"}
			}
			oldSince, _ := getString(oldC, "since")
			if candSince != oldSince {
				return lkal.Record{}, &Error{Kind: ErrForbidden, Path: "status/condition/" + cf.Key + "/since", Detail: "cannot change since"}
			}
		}
	}

	var oldObs int64
	var hasOldObs bool
	if hasOldStatus {
		oldObs, hasOldObs = getInt(oldStatus, "observedGeneration")
	}
	var candObs int64
	var hasCandObs bool
	if hasCandStatus {
		candObs, hasCandObs = getInt(candStatus, "observedGeneration")
	}
	if hasCandObs {
		if candObs > oldGen {
			return lkal.Record{}, &Error{Kind: ErrObserved, Path: "status/observedGeneration", Detail: "observedGeneration exceeds generation"}
		}
		if hasOldObs && candObs < oldObs {
			return lkal.Record{}, &Error{Kind: ErrObserved, Path: "status/observedGeneration", Detail: "observedGeneration regressed"}
		}
	} else if hasOldObs {
		return lkal.Record{}, &Error{Kind: ErrObserved, Path: "status/observedGeneration", Detail: "observedGeneration cannot be removed"}
	}

	fields := make([]lkal.Field, 0, 3)
	fields = append(fields, lkal.Field{Key: "meta", Value: cloneRecord(oldMeta)})
	if hasOldSpec {
		fields = append(fields, lkal.Field{Key: "spec", Value: cloneValue(oldSpec)})
	}
	if hasCandStatus {
		fields = append(fields, lkal.Field{Key: "status", Value: rewriteStatus(candStatus, oldCond, now)})
	}
	return lkal.Record{Fields: fields}, nil
}

// WithRevision returns stored with its meta record gaining a trailing revision field
// equal to Int(rev) after dropping any existing revision field.
func WithRevision(stored lkal.Record, rev int64) lkal.Record {
	fields := make([]lkal.Field, len(stored.Fields))
	replaced := false
	for i, f := range stored.Fields {
		if f.Key == "meta" && !replaced {
			if meta, ok := f.Value.(lkal.Record); ok {
				replaced = true
				metaFields := make([]lkal.Field, 0, len(meta.Fields)+1)
				for _, mf := range meta.Fields {
					if mf.Key == "revision" {
						continue
					}
					metaFields = append(metaFields, lkal.Field{Key: mf.Key, Value: cloneValue(mf.Value)})
				}
				metaFields = append(metaFields, lkal.Field{Key: "revision", Value: lkal.Int(rev)})
				fields[i] = lkal.Field{Key: "meta", Value: lkal.Record{Fields: metaFields}}
				continue
			}
		}
		fields[i] = lkal.Field{Key: f.Key, Value: cloneValue(f.Value)}
	}
	return lkal.Record{Fields: fields}
}

func rewriteStatus(candStatus, oldCond lkal.Record, now string) lkal.Record {
	fields := make([]lkal.Field, len(candStatus.Fields))
	for i, f := range candStatus.Fields {
		if f.Key != "condition" {
			fields[i] = lkal.Field{Key: f.Key, Value: cloneValue(f.Value)}
			continue
		}
		candCond := f.Value.(lkal.Record)
		condFields := make([]lkal.Field, len(candCond.Fields))
		for j, cf := range candCond.Fields {
			c := cf.Value.(lkal.Record)
			candState, _ := getField(c, "state")
			candReason, _ := getField(c, "reason")
			since := now
			if oldC, ok := getRecord(oldCond, cf.Key); ok {
				if oldState, ok := getField(oldC, "state"); ok && oldState.(lkal.Enum) == candState.(lkal.Enum) {
					since, _ = getString(oldC, "since")
				}
			}
			condFields[j] = lkal.Field{
				Key: cf.Key,
				Value: lkal.Record{
					Fields: []lkal.Field{
						{Key: "state", Value: candState},
						{Key: "reason", Value: candReason},
						{Key: "since", Value: lkal.String(since)},
					},
				},
			}
		}
		fields[i] = lkal.Field{Key: "condition", Value: lkal.Record{Fields: condFields}}
	}
	return lkal.Record{Fields: fields}
}

func canonicalMeta(src lkal.Record, uid string, gen int64) lkal.Record {
	fields := make([]lkal.Field, 0, 6)
	if group, ok := getString(src, "group"); ok {
		fields = append(fields, lkal.Field{Key: "group", Value: lkal.String(group)})
	}
	kind, _ := getString(src, "kind")
	name, _ := getString(src, "name")
	fields = append(fields,
		lkal.Field{Key: "kind", Value: lkal.String(kind)},
		lkal.Field{Key: "name", Value: lkal.String(name)},
	)
	if labels, ok := getRecord(src, "labels"); ok {
		fields = append(fields, lkal.Field{Key: "labels", Value: cloneRecord(labels)})
	}
	fields = append(fields,
		lkal.Field{Key: "uid", Value: lkal.String(uid)},
		lkal.Field{Key: "generation", Value: lkal.Int(gen)},
	)
	return lkal.Record{Fields: fields}
}

func checkIdentityRevUIDGen(oldMeta, candMeta lkal.Record, rev int64) *Error {
	if !sameIdentity(oldMeta, candMeta) {
		return &Error{Kind: ErrForbidden, Path: "meta", Detail: "cannot change identity"}
	}
	if candRev, ok := getInt(candMeta, "revision"); ok && candRev != rev {
		return &Error{Kind: ErrConflict, Path: "meta/revision", Detail: "revision mismatch"}
	}
	oldUID, _ := getString(oldMeta, "uid")
	if candUID, ok := getString(candMeta, "uid"); ok && candUID != oldUID {
		return &Error{Kind: ErrForbidden, Path: "meta/uid", Detail: "cannot change uid"}
	}
	oldGen, _ := getInt(oldMeta, "generation")
	if candGen, ok := getInt(candMeta, "generation"); ok && candGen != oldGen {
		return &Error{Kind: ErrForbidden, Path: "meta/generation", Detail: "cannot change generation"}
	}
	return nil
}

func sameIdentity(a, b lkal.Record) bool {
	ga, hasGA := getString(a, "group")
	gb, hasGB := getString(b, "group")
	if hasGA != hasGB || (hasGA && ga != gb) {
		return false
	}
	ka, _ := getString(a, "kind")
	kb, _ := getString(b, "kind")
	if ka != kb {
		return false
	}
	na, _ := getString(a, "name")
	nb, _ := getString(b, "name")
	return na == nb
}

func optEqual(a lkal.Value, okA bool, b lkal.Value, okB bool) bool {
	if !okA && !okB {
		return true
	}
	if okA != okB {
		return false
	}
	return lkal.Equal(a, b)
}

func getField(rec lkal.Record, key string) (lkal.Value, bool) {
	for _, f := range rec.Fields {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

func getRecord(rec lkal.Record, key string) (lkal.Record, bool) {
	v, ok := getField(rec, key)
	if !ok {
		return lkal.Record{}, false
	}
	r, ok := v.(lkal.Record)
	return r, ok
}

func getString(rec lkal.Record, key string) (string, bool) {
	v, ok := getField(rec, key)
	if !ok {
		return "", false
	}
	s, ok := v.(lkal.String)
	return string(s), ok
}

func getInt(rec lkal.Record, key string) (int64, bool) {
	v, ok := getField(rec, key)
	if !ok {
		return 0, false
	}
	n, ok := v.(lkal.Int)
	return int64(n), ok
}

func cloneRecord(rec lkal.Record) lkal.Record {
	if len(rec.Fields) == 0 {
		return lkal.Record{}
	}
	fields := make([]lkal.Field, len(rec.Fields))
	for i, f := range rec.Fields {
		fields[i] = lkal.Field{Key: f.Key, Value: cloneValue(f.Value)}
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
