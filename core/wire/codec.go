package wire

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

const (
	// MediaTypeResource is the HTTP media type for LKAL-JSON resource, list, watch event, and error payloads.
	MediaTypeResource = "application/vnd.nayutar.lkal+json"
	// MediaTypePatch is the HTTP media type for LKAL-JSON ordered-set patch payloads.
	MediaTypePatch = "application/vnd.nayutar.lkal-patch+json"
)

// WatchEventType identifies the kind of event delivered on a wire watch stream.
type WatchEventType string

const (
	EventPut      WatchEventType = "put"
	EventDelete   WatchEventType = "delete"
	EventBookmark WatchEventType = "bookmark"
	EventError    WatchEventType = "error"
)

// WatchTarget identifies the deleted object carried by an EventDelete watch event.
type WatchTarget struct {
	Group string
	Kind  string
	Name  string
}

// WatchEvent is one NDJSON record delivered on a wire watch stream.
type WatchEvent struct {
	Type             WatchEventType
	Revision         store.Rev
	InitialEventsEnd bool
	Object           *lkal.Record
	Target           *WatchTarget
	Error            *Error
}

// EncodeList serializes a resource.ListResult into compact single-line LKAL-JSON conforming to list.schema.lkal.
func EncodeList(res resource.ListResult) ([]byte, error) {
	if res.Revision < 0 {
		return nil, fmt.Errorf("wire: negative list revision %d", res.Revision)
	}
	type namedItem struct {
		name string
		rec  lkal.Record
	}
	named := make([]namedItem, 0, len(res.Items))
	seen := make(map[string]struct{}, len(res.Items))
	for _, item := range res.Items {
		obj, err := envelope.Object(item)
		if err != nil {
			return nil, fmt.Errorf("wire: invalid list item: %w", err)
		}
		if _, dup := seen[obj.Name]; dup {
			return nil, fmt.Errorf("wire: duplicate list item name %q", obj.Name)
		}
		seen[obj.Name] = struct{}{}
		named = append(named, namedItem{name: obj.Name, rec: item})
	}
	slices.SortFunc(named, func(a, b namedItem) int {
		return strings.Compare(a.name, b.name)
	})
	itemFields := make([]lkal.Field, len(named))
	for i, it := range named {
		itemFields[i] = lkal.Field{Key: it.name, Value: it.rec}
	}

	fields := make([]lkal.Field, 0, 3)
	fields = append(fields, lkal.Field{Key: "revision", Value: lkal.Int(res.Revision)})
	if res.Continue != "" {
		fields = append(fields, lkal.Field{Key: "continue", Value: lkal.String(res.Continue)})
	}
	fields = append(fields, lkal.Field{Key: "items", Value: lkal.Record{Fields: itemFields}})
	return encodeTopFields(fields, listSchema)
}

// DecodeList parses and validates an LKAL-JSON list response payload.
func DecodeList(data []byte) (resource.ListResult, error) {
	f, err := lkal.FromJSON(data)
	if err != nil {
		return resource.ListResult{}, err
	}
	if err := listSchema.Validate(f); err != nil {
		return resource.ListResult{}, err
	}
	top := f.Data()
	revVal, ok := findIntField(top, "revision")
	if !ok || revVal < 0 {
		return resource.ListResult{}, errors.New("wire: invalid list revision")
	}
	contVal, _ := findStringField(top, "continue")
	itemsRec, ok := findRecordField(top, "items")
	if !ok {
		return resource.ListResult{}, errors.New("wire: missing list items")
	}

	type namedItem struct {
		name string
		rec  lkal.Record
	}
	named := make([]namedItem, 0, len(itemsRec.Fields))
	for _, fld := range itemsRec.Fields {
		itemRec, isRec := fld.Value.(lkal.Record)
		if !isRec {
			return resource.ListResult{}, fmt.Errorf("wire: list item %q must be a record", fld.Key)
		}
		obj, err := envelope.Object(itemRec)
		if err != nil {
			return resource.ListResult{}, fmt.Errorf("wire: list item %q: %w", fld.Key, err)
		}
		if obj.Name != fld.Key {
			return resource.ListResult{}, fmt.Errorf("wire: list item key %q does not match meta/name %q", fld.Key, obj.Name)
		}
		named = append(named, namedItem{name: fld.Key, rec: itemRec})
	}
	slices.SortFunc(named, func(a, b namedItem) int {
		return strings.Compare(a.name, b.name)
	})
	var items []lkal.Record
	if len(named) > 0 {
		items = make([]lkal.Record, len(named))
		for i, it := range named {
			items[i] = it.rec
		}
	}
	return resource.ListResult{
		Items:    items,
		Revision: store.Rev(revVal),
		Continue: contVal,
	}, nil
}

// EncodeWatchEvent validates cross-field invariants and serializes ev into compact single-line LKAL-JSON.
func EncodeWatchEvent(ev WatchEvent) ([]byte, error) {
	fields := make([]lkal.Field, 0, 3)
	switch ev.Type {
	case EventPut:
		if ev.Revision < 1 {
			return nil, fmt.Errorf("wire: put event revision %d must be >= 1", ev.Revision)
		}
		if ev.Object == nil {
			return nil, errors.New("wire: put event requires object")
		}
		if ev.Target != nil || ev.Error != nil || ev.InitialEventsEnd {
			return nil, errors.New("wire: put event must not carry target, error, or initialEventsEnd")
		}
		if err := envelope.CheckShape(*ev.Object); err != nil {
			return nil, fmt.Errorf("wire: put event object: %w", err)
		}
		if meta, ok := findRecordField(*ev.Object, "meta"); ok {
			if objRev, hasRev := findIntField(meta, "revision"); hasRev && objRev != int64(ev.Revision) {
				return nil, fmt.Errorf("wire: put event revision %d differs from meta/revision %d", ev.Revision, objRev)
			}
		}
		fields = append(fields,
			lkal.Field{Key: "type", Value: lkal.Enum(EventPut)},
			lkal.Field{Key: "revision", Value: lkal.Int(ev.Revision)},
			lkal.Field{Key: "object", Value: *ev.Object},
		)

	case EventDelete:
		if ev.Revision < 1 {
			return nil, fmt.Errorf("wire: delete event revision %d must be >= 1", ev.Revision)
		}
		if ev.Target == nil {
			return nil, errors.New("wire: delete event requires target")
		}
		if ev.Object != nil || ev.Error != nil || ev.InitialEventsEnd {
			return nil, errors.New("wire: delete event must not carry object, error, or initialEventsEnd")
		}
		targetRec, err := targetToRecord(*ev.Target)
		if err != nil {
			return nil, err
		}
		fields = append(fields,
			lkal.Field{Key: "type", Value: lkal.Enum(EventDelete)},
			lkal.Field{Key: "revision", Value: lkal.Int(ev.Revision)},
			lkal.Field{Key: "target", Value: targetRec},
		)

	case EventBookmark:
		if ev.Revision < 0 {
			return nil, fmt.Errorf("wire: bookmark event revision %d must be >= 0", ev.Revision)
		}
		if ev.Object != nil || ev.Target != nil || ev.Error != nil {
			return nil, errors.New("wire: bookmark event must not carry object, target, or error")
		}
		fields = append(fields,
			lkal.Field{Key: "type", Value: lkal.Enum(EventBookmark)},
			lkal.Field{Key: "revision", Value: lkal.Int(ev.Revision)},
		)
		if ev.InitialEventsEnd {
			fields = append(fields, lkal.Field{Key: "initialEventsEnd", Value: lkal.Bool(true)})
		}

	case EventError:
		if ev.Error == nil {
			return nil, errors.New("wire: error event requires error")
		}
		if ev.Revision != 0 || ev.InitialEventsEnd || ev.Object != nil || ev.Target != nil {
			return nil, errors.New("wire: error event must not carry revision, initialEventsEnd, object, or target")
		}
		errRec, err := errorToRecord(ev.Error)
		if err != nil {
			return nil, err
		}
		fields = append(fields,
			lkal.Field{Key: "type", Value: lkal.Enum(EventError)},
			lkal.Field{Key: "error", Value: errRec},
		)

	default:
		return nil, fmt.Errorf("wire: unknown watch event type %q", ev.Type)
	}

	return encodeTopFields(fields, watchEventSchema)
}

// DecodeWatchEvent parses, schema-validates, and enforces cross-field invariants on a wire watch event line.
func DecodeWatchEvent(data []byte) (WatchEvent, error) {
	f, err := lkal.FromJSON(data)
	if err != nil {
		return WatchEvent{}, err
	}
	if err := watchEventSchema.Validate(f); err != nil {
		return WatchEvent{}, err
	}
	top := f.Data()
	typeVal, ok := findEnumField(top, "type")
	if !ok {
		return WatchEvent{}, errors.New("wire: missing watch event type")
	}
	revVal, hasRev := findIntField(top, "revision")
	initEnd, hasInitEnd := findBoolField(top, "initialEventsEnd")
	objRec, hasObj := findRecordField(top, "object")
	targetRec, hasTarget := findRecordField(top, "target")
	errRec, hasErr := findRecordField(top, "error")

	evType := WatchEventType(typeVal)
	switch evType {
	case EventPut:
		if !hasRev || revVal < 1 {
			return WatchEvent{}, errors.New("wire: put event requires revision >= 1")
		}
		if !hasObj || hasTarget || hasErr || hasInitEnd {
			return WatchEvent{}, errors.New("wire: put event must carry object and no target, error, or initialEventsEnd")
		}
		if err := envelope.CheckShape(objRec); err != nil {
			return WatchEvent{}, fmt.Errorf("wire: put event object: %w", err)
		}
		if meta, ok := findRecordField(objRec, "meta"); ok {
			if objRev, ok := findIntField(meta, "revision"); ok && objRev != revVal {
				return WatchEvent{}, fmt.Errorf("wire: put event revision %d differs from meta/revision %d", revVal, objRev)
			}
		}
		return WatchEvent{
			Type:     EventPut,
			Revision: store.Rev(revVal),
			Object:   &objRec,
		}, nil

	case EventDelete:
		if !hasRev || revVal < 1 {
			return WatchEvent{}, errors.New("wire: delete event requires revision >= 1")
		}
		if !hasTarget || hasObj || hasErr || hasInitEnd {
			return WatchEvent{}, errors.New("wire: delete event must carry target and no object, error, or initialEventsEnd")
		}
		target, err := targetFromRecord(targetRec)
		if err != nil {
			return WatchEvent{}, err
		}
		return WatchEvent{
			Type:     EventDelete,
			Revision: store.Rev(revVal),
			Target:   &target,
		}, nil

	case EventBookmark:
		if !hasRev || revVal < 0 {
			return WatchEvent{}, errors.New("wire: bookmark event requires revision >= 0")
		}
		if hasObj || hasTarget || hasErr {
			return WatchEvent{}, errors.New("wire: bookmark event must not carry object, target, or error")
		}
		return WatchEvent{
			Type:             EventBookmark,
			Revision:         store.Rev(revVal),
			InitialEventsEnd: hasInitEnd && initEnd,
		}, nil

	case EventError:
		if !hasErr || hasRev || hasInitEnd || hasObj || hasTarget {
			return WatchEvent{}, errors.New("wire: error event must carry only type and error")
		}
		we, err := errorFromRecord(errRec)
		if err != nil {
			return WatchEvent{}, err
		}
		return WatchEvent{
			Type:  EventError,
			Error: we,
		}, nil

	default:
		return WatchEvent{}, fmt.Errorf("wire: unknown watch event type %q", evType)
	}
}

func targetToRecord(t WatchTarget) (lkal.Record, error) {
	if err := keyspace.ValidateKind(t.Kind); err != nil {
		return lkal.Record{}, fmt.Errorf("wire: delete target kind: %w", err)
	}
	if err := keyspace.ValidateName(t.Name); err != nil {
		return lkal.Record{}, fmt.Errorf("wire: delete target name: %w", err)
	}
	fields := make([]lkal.Field, 0, 3)
	if t.Group != "" && t.Group != keyspace.CoreGroup {
		if err := keyspace.ValidateGroup(t.Group); err != nil {
			return lkal.Record{}, fmt.Errorf("wire: delete target group: %w", err)
		}
		fields = append(fields, lkal.Field{Key: "group", Value: lkal.String(t.Group)})
	}
	fields = append(fields,
		lkal.Field{Key: "kind", Value: lkal.String(t.Kind)},
		lkal.Field{Key: "name", Value: lkal.String(t.Name)},
	)
	return lkal.Record{Fields: fields}, nil
}

func targetFromRecord(rec lkal.Record) (WatchTarget, error) {
	var out WatchTarget
	hasGroup := false
	for _, fld := range rec.Fields {
		switch fld.Key {
		case "group":
			s, ok := fld.Value.(lkal.String)
			if !ok {
				return WatchTarget{}, errors.New("wire: target.group must be a string")
			}
			if string(s) == keyspace.CoreGroup {
				return WatchTarget{}, errors.New("wire: target.group must be omitted for core")
			}
			if err := keyspace.ValidateGroup(string(s)); err != nil {
				return WatchTarget{}, err
			}
			out.Group = string(s)
			hasGroup = true
		case "kind":
			s, ok := fld.Value.(lkal.String)
			if !ok {
				return WatchTarget{}, errors.New("wire: target.kind must be a string")
			}
			if err := keyspace.ValidateKind(string(s)); err != nil {
				return WatchTarget{}, err
			}
			out.Kind = string(s)
		case "name":
			s, ok := fld.Value.(lkal.String)
			if !ok {
				return WatchTarget{}, errors.New("wire: target.name must be a string")
			}
			if err := keyspace.ValidateName(string(s)); err != nil {
				return WatchTarget{}, err
			}
			out.Name = string(s)
		default:
			return WatchTarget{}, fmt.Errorf("wire: unexpected target field %q", fld.Key)
		}
	}
	if !hasGroup {
		out.Group = keyspace.CoreGroup
	}
	if out.Kind == "" || out.Name == "" {
		return WatchTarget{}, errors.New("wire: target requires kind and name")
	}
	return out, nil
}

func findField(rec lkal.Record, key string) (lkal.Value, bool) {
	for _, fld := range rec.Fields {
		if fld.Key == key {
			return fld.Value, true
		}
	}
	return nil, false
}

func findRecordField(rec lkal.Record, key string) (lkal.Record, bool) {
	v, ok := findField(rec, key)
	if !ok {
		return lkal.Record{}, false
	}
	r, ok := v.(lkal.Record)
	return r, ok
}

func findStringField(rec lkal.Record, key string) (string, bool) {
	v, ok := findField(rec, key)
	if !ok {
		return "", false
	}
	s, ok := v.(lkal.String)
	return string(s), ok
}

func findIntField(rec lkal.Record, key string) (int64, bool) {
	v, ok := findField(rec, key)
	if !ok {
		return 0, false
	}
	n, ok := v.(lkal.Int)
	return int64(n), ok
}

func findBoolField(rec lkal.Record, key string) (bool, bool) {
	v, ok := findField(rec, key)
	if !ok {
		return false, false
	}
	b, ok := v.(lkal.Bool)
	return bool(b), ok
}

func findEnumField(rec lkal.Record, key string) (string, bool) {
	v, ok := findField(rec, key)
	if !ok {
		return "", false
	}
	e, ok := v.(lkal.Enum)
	return string(e), ok
}
