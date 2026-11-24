package envelope

import (
	"strconv"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

func checkShape(doc lkal.Record) *Error {
	if err := checkUnique(doc, ""); err != nil {
		return err
	}

	var metaVal, statusVal lkal.Record
	hasMeta := false
	hasStatus := false
	for _, f := range doc.Fields {
		switch f.Key {
		case "meta":
			rec, ok := f.Value.(lkal.Record)
			if !ok {
				return &Error{Kind: ErrMalformed, Path: "meta", Detail: "must be a record"}
			}
			metaVal = rec
			hasMeta = true
		case "spec":
			if _, ok := f.Value.(lkal.Record); !ok {
				return &Error{Kind: ErrMalformed, Path: "spec", Detail: "must be a record"}
			}
		case "status":
			rec, ok := f.Value.(lkal.Record)
			if !ok {
				return &Error{Kind: ErrMalformed, Path: "status", Detail: "must be a record"}
			}
			statusVal = rec
			hasStatus = true
		default:
			return &Error{Kind: ErrMalformed, Path: f.Key, Detail: "unexpected top-level field"}
		}
	}
	if !hasMeta {
		return &Error{Kind: ErrMalformed, Path: "meta", Detail: "missing required record"}
	}

	if err := checkMeta(metaVal); err != nil {
		return err
	}
	if hasStatus {
		if err := checkStatus(statusVal); err != nil {
			return err
		}
	}
	return nil
}

func checkUnique(v lkal.Value, path string) *Error {
	switch val := v.(type) {
	case lkal.String, lkal.Int, lkal.Decimal, lkal.Bool, lkal.Enum, lkal.Quantity, lkal.Ref:
		return nil
	case lkal.List:
		for i, elem := range val {
			if err := checkUnique(elem, joinPath(path, strconv.Itoa(i))); err != nil {
				return err
			}
		}
		return nil
	case lkal.Record:
		seen := make(map[string]struct{}, len(val.Fields))
		for _, f := range val.Fields {
			fp := joinPath(path, f.Key)
			if _, dup := seen[f.Key]; dup {
				return &Error{Kind: ErrMalformed, Path: fp, Detail: "duplicate key"}
			}
			seen[f.Key] = struct{}{}
			if err := checkUnique(f.Value, fp); err != nil {
				return err
			}
		}
		return nil
	default:
		return &Error{Kind: ErrMalformed, Path: path, Detail: "invalid value"}
	}
}

func checkMeta(meta lkal.Record) *Error {
	var (
		group, kind, name string
		hasGroup          bool
		hasKind, hasName  bool
	)
	for _, f := range meta.Fields {
		switch f.Key {
		case "group":
			s, ok := f.Value.(lkal.String)
			if !ok {
				return &Error{Kind: ErrMalformed, Path: "meta/group", Detail: "must be a string"}
			}
			if string(s) == keyspace.CoreGroup {
				return &Error{Kind: ErrMalformed, Path: "meta/group", Detail: "core group must be omitted"}
			}
			group = string(s)
			hasGroup = true
		case "kind":
			s, ok := f.Value.(lkal.String)
			if !ok {
				return &Error{Kind: ErrMalformed, Path: "meta/kind", Detail: "must be a string"}
			}
			kind = string(s)
			hasKind = true
		case "name":
			s, ok := f.Value.(lkal.String)
			if !ok {
				return &Error{Kind: ErrMalformed, Path: "meta/name", Detail: "must be a string"}
			}
			name = string(s)
			hasName = true
		case "labels":
			rec, ok := f.Value.(lkal.Record)
			if !ok {
				return &Error{Kind: ErrMalformed, Path: "meta/labels", Detail: "must be a record"}
			}
			for _, lf := range rec.Fields {
				if _, ok := lf.Value.(lkal.String); !ok {
					return &Error{Kind: ErrMalformed, Path: "meta/labels/" + lf.Key, Detail: "must be a string"}
				}
			}
		case "uid":
			s, ok := f.Value.(lkal.String)
			if !ok || !isUID(string(s)) {
				return &Error{Kind: ErrMalformed, Path: "meta/uid", Detail: "invalid uid"}
			}
		case "revision":
			n, ok := f.Value.(lkal.Int)
			if !ok || int64(n) < 1 {
				return &Error{Kind: ErrMalformed, Path: "meta/revision", Detail: "must be an integer >= 1"}
			}
		case "generation":
			n, ok := f.Value.(lkal.Int)
			if !ok || int64(n) < 1 {
				return &Error{Kind: ErrMalformed, Path: "meta/generation", Detail: "must be an integer >= 1"}
			}
		default:
			return &Error{Kind: ErrMalformed, Path: "meta/" + f.Key, Detail: "unexpected field"}
		}
	}
	if !hasKind {
		return &Error{Kind: ErrMalformed, Path: "meta/kind", Detail: "missing required string"}
	}
	if !hasName {
		return &Error{Kind: ErrMalformed, Path: "meta/name", Detail: "missing required string"}
	}

	groupForKey := keyspace.CoreGroup
	if hasGroup {
		groupForKey = group
	}
	if _, err := (keyspace.Key{
		Family: keyspace.FamilyObject,
		Tenant: "t",
		Object: keyspace.Object{Group: groupForKey, Kind: kind, Name: name},
	}).Rel(); err != nil {
		path := "meta"
		switch {
		case hasGroup && keyspace.ValidateGroup(group) != nil:
			path = "meta/group"
		case keyspace.ValidateKind(kind) != nil:
			path = "meta/kind"
		case keyspace.ValidateName(name) != nil:
			path = "meta/name"
		}
		return &Error{Kind: ErrMalformed, Path: path, Detail: err.Error()}
	}
	return nil
}

func checkStatus(status lkal.Record) *Error {
	if v, ok := getField(status, "observedGeneration"); ok {
		n, isInt := v.(lkal.Int)
		if !isInt || int64(n) < 1 {
			return &Error{Kind: ErrMalformed, Path: "status/observedGeneration", Detail: "must be an integer >= 1"}
		}
	}
	condVal, hasCond := getField(status, "condition")
	if !hasCond {
		return nil
	}
	condRec, ok := condVal.(lkal.Record)
	if !ok {
		return &Error{Kind: ErrMalformed, Path: "status/condition", Detail: "must be a record"}
	}
	for _, cf := range condRec.Fields {
		condType := cf.Key
		cPath := "status/condition/" + condType
		if !isConditionType(condType) {
			return &Error{Kind: ErrMalformed, Path: cPath, Detail: "invalid condition type"}
		}
		cRec, ok := cf.Value.(lkal.Record)
		if !ok {
			return &Error{Kind: ErrMalformed, Path: cPath, Detail: "must be a record"}
		}
		if err := checkCondition(cRec, cPath); err != nil {
			return err
		}
	}
	return nil
}

func checkCondition(c lkal.Record, cPath string) *Error {
	var (
		hasState, hasReason bool
	)
	for _, f := range c.Fields {
		fp := cPath + "/" + f.Key
		switch f.Key {
		case "state":
			e, ok := f.Value.(lkal.Enum)
			if !ok || (e != "true" && e != "false" && e != "unknown") {
				return &Error{Kind: ErrMalformed, Path: fp, Detail: "must be enum true, false, or unknown"}
			}
			hasState = true
		case "reason":
			s, ok := f.Value.(lkal.String)
			if !ok || !isReason(string(s)) {
				return &Error{Kind: ErrMalformed, Path: fp, Detail: "invalid reason"}
			}
			hasReason = true
		case "since":
			s, ok := f.Value.(lkal.String)
			if !ok || !isTimestamp(string(s)) {
				return &Error{Kind: ErrMalformed, Path: fp, Detail: "invalid timestamp"}
			}
		default:
			return &Error{Kind: ErrMalformed, Path: fp, Detail: "unexpected field"}
		}
	}
	if !hasState {
		return &Error{Kind: ErrMalformed, Path: cPath + "/state", Detail: "missing required enum"}
	}
	if !hasReason {
		return &Error{Kind: ErrMalformed, Path: cPath + "/reason", Detail: "missing required string"}
	}
	return nil
}

func checkStored(doc lkal.Record) *Error {
	if err := checkShape(doc); err != nil {
		return &Error{Kind: ErrInternal, Path: err.Path, Detail: err.Detail}
	}
	meta, _ := getRecord(doc, "meta")
	if _, ok := getField(meta, "uid"); !ok {
		return &Error{Kind: ErrInternal, Path: "meta/uid", Detail: "missing uid"}
	}
	gen, ok := getInt(meta, "generation")
	if !ok {
		return &Error{Kind: ErrInternal, Path: "meta/generation", Detail: "missing generation"}
	}
	if _, ok := getField(meta, "revision"); ok {
		return &Error{Kind: ErrInternal, Path: "meta/revision", Detail: "stored document must not carry revision"}
	}
	status, hasStatus := getRecord(doc, "status")
	if !hasStatus {
		return nil
	}
	if cond, ok := getRecord(status, "condition"); ok {
		for _, cf := range cond.Fields {
			c := cf.Value.(lkal.Record)
			if _, ok := getField(c, "since"); !ok {
				return &Error{Kind: ErrInternal, Path: "status/condition/" + cf.Key + "/since", Detail: "missing since"}
			}
		}
	}
	if obs, ok := getInt(status, "observedGeneration"); ok && obs > gen {
		return &Error{Kind: ErrInternal, Path: "status/observedGeneration", Detail: "observedGeneration exceeds generation"}
	}
	return nil
}

func isUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		b := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if b != '-' {
				return false
			}
			continue
		}
		if (b < '0' || b > '9') && (b < 'a' || b > 'f') {
			return false
		}
	}
	return true
}

func isConditionType(s string) bool {
	if len(s) < 1 || len(s) > 63 {
		return false
	}
	if !isUpperASCII(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isAlphaNumASCII(s[i]) {
			return false
		}
	}
	return true
}

func isReason(s string) bool {
	if len(s) < 1 || len(s) > 1024 {
		return false
	}
	if !isAlphaASCII(s[0]) {
		return false
	}
	last := s[len(s)-1]
	if !isAlphaNumASCII(last) && last != '_' {
		return false
	}
	for i := range len(s) {
		b := s[i]
		if !isAlphaNumASCII(b) && b != '_' && b != ',' && b != ':' {
			return false
		}
	}
	return true
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

func isUpperASCII(b byte) bool {
	return b >= 'A' && b <= 'Z'
}

func isAlphaASCII(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func isAlphaNumASCII(b byte) bool {
	return isAlphaASCII(b) || (b >= '0' && b <= '9')
}

func joinPath(base, seg string) string {
	if base == "" {
		return seg
	}
	return base + "/" + seg
}
