package ownership

import (
	"fmt"

	"github.com/Kaikei-e/LKAL"
)

// Leaf pairs a parsed EntranceSpec leaf path with its non-Record LKAL value.
type Leaf struct {
	Path  Path
	Value lkal.Value
}

// ObsKind classifies what is observed at a path in a target document.
type ObsKind uint8

const (
	ObsAbsent ObsKind = iota
	ObsLeaf
	ObsRecord
	ObsOpaque
	ObsUnknown
)

func (k ObsKind) String() string {
	switch k {
	case ObsAbsent:
		return "Absent"
	case ObsLeaf:
		return "Leaf"
	case ObsRecord:
		return "Record"
	case ObsOpaque:
		return "Opaque"
	case ObsUnknown:
		return "Unknown"
	default:
		return "Invalid"
	}
}

// Observed describes the observed node state at a path.
type Observed struct {
	Kind   ObsKind
	Value  lkal.Value
	Reason string
}

// AncestorObs pairs a proper ancestor path q < p with its observed state.
type AncestorObs struct {
	Path     Path
	Observed Observed
}

// ProjectEntranceSpec extracts all EntranceSpec leaves (meta/labels/* and spec/**) from doc,
// allowing empty nested records (for stored documents and imperative Update/Patch).
func ProjectEntranceSpec(doc lkal.Record) (map[string]Leaf, error) {
	return ProjectDeclaration(doc, true)
}

// ProjectDeclaration extracts EntranceSpec leaves from doc.
// Top-level meta/labels = {} and spec = {} are valid empty section roots.
// When allowNestedEmpty is false (Apply and Prune), nested empty records under spec/** fail with K102.
func ProjectDeclaration(doc lkal.Record, allowNestedEmpty bool) (map[string]Leaf, error) {
	out := make(map[string]Leaf)
	seenTop := make(map[string]struct{}, len(doc.Fields))
	for _, f := range doc.Fields {
		if _, dup := seenTop[f.Key]; dup {
			return nil, newError(CodeInvalidInput, Path{f.Key}, fmt.Sprintf("duplicate top-level key %q", f.Key), "")
		}
		seenTop[f.Key] = struct{}{}
		switch f.Key {
		case "meta":
			metaRec, ok := f.Value.(lkal.Record)
			if !ok {
				return nil, newError(CodeInvalidInput, Path{"meta"}, "meta must be a record", "")
			}
			if err := projectMetaLabels(metaRec, allowNestedEmpty, out); err != nil {
				return nil, err
			}
		case "spec":
			specRec, ok := f.Value.(lkal.Record)
			if !ok {
				return nil, newError(CodeInvalidInput, Path{"spec"}, "spec must be a record", "")
			}
			if err := projectRecordLeaves(specRec, Path{"spec"}, allowNestedEmpty, out); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func projectMetaLabels(meta lkal.Record, allowNestedEmpty bool, out map[string]Leaf) error {
	seenMeta := make(map[string]struct{}, len(meta.Fields))
	for _, mf := range meta.Fields {
		if _, dup := seenMeta[mf.Key]; dup {
			return newError(CodeInvalidInput, Path{"meta", mf.Key}, fmt.Sprintf("duplicate meta key %q", mf.Key), "")
		}
		seenMeta[mf.Key] = struct{}{}
		if mf.Key != "labels" {
			continue
		}
		labelsRec, ok := mf.Value.(lkal.Record)
		if !ok {
			return newError(CodeInvalidInput, Path{"meta", "labels"}, "meta/labels must be a record", "")
		}
		seenLabels := make(map[string]struct{}, len(labelsRec.Fields))
		for _, lf := range labelsRec.Fields {
			if !ValidSeg(lf.Key) {
				return newError(CodeInvalidInput, Path{"meta", "labels"}, fmt.Sprintf("invalid label key %q", lf.Key), "")
			}
			p := Path{"meta", "labels", lf.Key}
			if _, dup := seenLabels[lf.Key]; dup {
				return newError(CodeInvalidInput, p, fmt.Sprintf("duplicate label key %q", lf.Key), "")
			}
			seenLabels[lf.Key] = struct{}{}
			if subRec, isRec := lf.Value.(lkal.Record); isRec {
				if len(subRec.Fields) == 0 && !allowNestedEmpty {
					return newError(CodeEmptyRecordNotSupported, p, "nested empty record is not supported in declaration", "remove empty record or declare leaf fields")
				}
				return newError(CodeInvalidInput, p, "label value must be a leaf scalar", "")
			}
			if err := validateLeafValue(lf.Value, p); err != nil {
				return err
			}
			out[p.String()] = Leaf{Path: p, Value: cloneValue(lf.Value)}
		}
	}
	return nil
}

func projectRecordLeaves(rec lkal.Record, prefix Path, allowNestedEmpty bool, out map[string]Leaf) error {
	if len(prefix) > MaxPathDepth {
		return newError(CodeInvalidInput, prefix, "record exceeds maximum nesting depth", "")
	}
	seen := make(map[string]struct{}, len(rec.Fields))
	for _, fld := range rec.Fields {
		if !ValidSeg(fld.Key) {
			return newError(CodeInvalidInput, prefix, fmt.Sprintf("invalid field segment %q", fld.Key), "")
		}
		p := prefix.Append(fld.Key)
		if _, dup := seen[fld.Key]; dup {
			return newError(CodeInvalidInput, p, fmt.Sprintf("duplicate field key %q", fld.Key), "")
		}
		seen[fld.Key] = struct{}{}
		if sub, isRec := fld.Value.(lkal.Record); isRec {
			if len(sub.Fields) == 0 {
				if !allowNestedEmpty {
					return newError(CodeEmptyRecordNotSupported, p, fmt.Sprintf("empty record at %s is not supported in declaration", p.String()), "remove empty record or declare leaf fields")
				}
				continue
			}
			if err := projectRecordLeaves(sub, p, allowNestedEmpty, out); err != nil {
				return err
			}
			continue
		}
		if err := validateLeafValue(fld.Value, p); err != nil {
			return err
		}
		out[p.String()] = Leaf{Path: p, Value: cloneValue(fld.Value)}
	}
	return nil
}

func validateLeafValue(v lkal.Value, p Path) error {
	switch val := v.(type) {
	case lkal.String, lkal.Int, lkal.Decimal, lkal.Bool, lkal.Enum, lkal.Quantity, lkal.Ref:
		return nil
	case lkal.List:
		var elemKind string
		for _, el := range val {
			k, ok := scalarKind(el)
			if !ok {
				return newError(CodeInvalidInput, p, "list elements must be LKAL scalars", "")
			}
			if elemKind == "" {
				elemKind = k
			} else if elemKind != k {
				return newError(CodeInvalidInput, p, "list elements must have a single scalar type", "")
			}
		}
		return nil
	default:
		return newError(CodeInvalidInput, p, "unsupported leaf value", "")
	}
}

func scalarKind(v lkal.Value) (string, bool) {
	switch v.(type) {
	case lkal.String:
		return "string", true
	case lkal.Int:
		return "int", true
	case lkal.Decimal:
		return "decimal", true
	case lkal.Bool:
		return "bool", true
	case lkal.Enum:
		return "enum", true
	case lkal.Quantity:
		return "quantity", true
	case lkal.Ref:
		return "ref", true
	default:
		return "", false
	}
}

func isValidLeafValue(v lkal.Value) bool {
	return validateLeafValue(v, nil) == nil
}

// InspectDocPath observes p and all of its proper ancestors in doc.
func InspectDocPath(doc lkal.Record, p Path) (Observed, []AncestorObs) {
	if len(p) == 0 {
		return Observed{Kind: ObsRecord}, nil
	}
	ancs := make([]AncestorObs, 0, len(p)-1)
	var cur lkal.Value = doc
	present := true
	for i := range len(p) - 1 {
		ancPath := p[:i+1].Clone()
		if !present {
			ancs = append(ancs, AncestorObs{Path: ancPath, Observed: Observed{Kind: ObsAbsent}})
			continue
		}
		rec, ok := cur.(lkal.Record)
		if !ok {
			present = false
			ancs = append(ancs, AncestorObs{Path: ancPath, Observed: Observed{Kind: ObsAbsent}})
			continue
		}
		val, found := findField(rec, p[i])
		if !found {
			present = false
			ancs = append(ancs, AncestorObs{Path: ancPath, Observed: Observed{Kind: ObsAbsent}})
			continue
		}
		obs := valueToObserved(val)
		ancs = append(ancs, AncestorObs{Path: ancPath, Observed: obs})
		if obs.Kind == ObsRecord {
			cur = val
		} else {
			present = false
		}
	}

	if !present {
		return Observed{Kind: ObsAbsent}, ancs
	}
	rec, ok := cur.(lkal.Record)
	if !ok {
		return Observed{Kind: ObsAbsent}, ancs
	}
	val, found := findField(rec, p[len(p)-1])
	if !found {
		return Observed{Kind: ObsAbsent}, ancs
	}
	return valueToObserved(val), ancs
}

func valueToObserved(v lkal.Value) Observed {
	if _, isRec := v.(lkal.Record); isRec {
		return Observed{Kind: ObsRecord}
	}
	if isValidLeafValue(v) {
		return Observed{Kind: ObsLeaf, Value: cloneValue(v)}
	}
	return Observed{Kind: ObsOpaque, Reason: "non-representable value"}
}

func setLeafInDoc(doc lkal.Record, p Path, v lkal.Value) (lkal.Record, error) {
	next, err := setAtRec(doc, p, cloneValue(v), p)
	if err != nil {
		return lkal.Record{}, err
	}
	return next, nil
}

func setAtRec(rec lkal.Record, segs []string, val lkal.Value, full Path) (lkal.Record, error) {
	k := segs[0]
	if len(segs) == 1 {
		return upsertField(rec, k, val), nil
	}
	existing, found := findField(rec, k)
	if !found {
		sub, err := setAtRec(lkal.Record{}, segs[1:], val, full)
		if err != nil {
			return lkal.Record{}, err
		}
		return upsertField(rec, k, sub), nil
	}
	subRec, isRec := existing.(lkal.Record)
	if !isRec {
		return lkal.Record{}, newError(CodeStructureConflict, full, fmt.Sprintf("ancestor %q is not a record", k), "")
	}
	sub, err := setAtRec(subRec, segs[1:], val, full)
	if err != nil {
		return lkal.Record{}, err
	}
	return upsertField(rec, k, sub), nil
}

// deleteLeafPruneInDoc deletes leaf p from doc and prunes emptied (or defaulted-only) intermediate
// records bottom-up. Any orphaned StateDefaulted entries removed with an emptied intermediate record
// are also deleted from st.
// Under spec/**, intermediate records (depth >= 2) are removed while top-level spec = {} is kept.
// Under meta/labels/*, meta/labels itself is removed when its last label is deleted.
func deleteLeafPruneInDoc(doc lkal.Record, p Path, st *State) (lkal.Record, bool) {
	if len(p) == 3 && p[0] == "meta" && p[1] == "labels" {
		metaVal, ok := findField(doc, "meta")
		if !ok {
			return doc, false
		}
		metaRec, ok := metaVal.(lkal.Record)
		if !ok {
			return doc, false
		}
		labelsVal, ok := findField(metaRec, "labels")
		if !ok {
			return doc, false
		}
		labelsRec, ok := labelsVal.(lkal.Record)
		if !ok {
			return doc, false
		}
		nextLabels, removed := removeField(labelsRec, p[2])
		if !removed {
			return doc, false
		}
		var nextMeta lkal.Record
		if len(nextLabels.Fields) == 0 {
			nextMeta, _ = removeField(metaRec, "labels")
		} else {
			nextMeta = upsertField(metaRec, "labels", nextLabels)
		}
		return upsertField(doc, "meta", nextMeta), true
	}
	if len(p) >= 2 && p[0] == "spec" {
		specVal, ok := findField(doc, "spec")
		if !ok {
			return doc, false
		}
		specRec, ok := specVal.(lkal.Record)
		if !ok {
			return doc, false
		}
		nextSpec, removed := deletePruneSubRec(specRec, p[1:], Path{"spec"}, st)
		if !removed {
			return doc, false
		}
		return upsertField(doc, "spec", nextSpec), true
	}
	return doc, false
}

func deletePruneSubRec(rec lkal.Record, segs []string, prefix Path, st *State) (lkal.Record, bool) {
	k := segs[0]
	if len(segs) == 1 {
		return removeField(rec, k)
	}
	existing, ok := findField(rec, k)
	if !ok {
		return rec, false
	}
	subRec, ok := existing.(lkal.Record)
	if !ok {
		return rec, false
	}
	childPrefix := prefix.Append(k)
	nextSub, removed := deletePruneSubRec(subRec, segs[1:], childPrefix, st)
	if !removed {
		return rec, false
	}
	if len(nextSub.Fields) == 0 || (st != nil && onlyDefaultedLeaves(nextSub, childPrefix, *st)) {
		if st != nil {
			removeDefaultedSubtree(nextSub, childPrefix, st)
		}
		out, _ := removeField(rec, k)
		return out, true
	}
	return upsertField(rec, k, nextSub), true
}

func onlyDefaultedLeaves(rec lkal.Record, prefix Path, st State) bool {
	for _, f := range rec.Fields {
		p := prefix.Append(f.Key)
		if sub, isRec := f.Value.(lkal.Record); isRec {
			if !onlyDefaultedLeaves(sub, p, st) {
				return false
			}
			continue
		}
		e, ok := st.Get(p)
		if !ok || e.State != StateDefaulted {
			return false
		}
	}
	return true
}

func removeDefaultedSubtree(rec lkal.Record, prefix Path, st *State) {
	for _, f := range rec.Fields {
		p := prefix.Append(f.Key)
		if sub, isRec := f.Value.(lkal.Record); isRec {
			removeDefaultedSubtree(sub, p, st)
			continue
		}
		st.Delete(p)
	}
}

func upsertField(rec lkal.Record, key string, val lkal.Value) lkal.Record {
	fields := make([]lkal.Field, len(rec.Fields))
	replaced := false
	for i, f := range rec.Fields {
		if f.Key == key {
			fields[i] = lkal.Field{Key: key, Value: val}
			replaced = true
		} else {
			fields[i] = f
		}
	}
	if !replaced {
		fields = append(fields, lkal.Field{Key: key, Value: val})
	}
	return lkal.Record{Fields: fields}
}

func removeField(rec lkal.Record, key string) (lkal.Record, bool) {
	out := make([]lkal.Field, 0, len(rec.Fields))
	removed := false
	for _, f := range rec.Fields {
		if f.Key == key {
			removed = true
			continue
		}
		out = append(out, f)
	}
	if !removed {
		return rec, false
	}
	if len(out) == 0 {
		return lkal.Record{}, true
	}
	return lkal.Record{Fields: out}, true
}
