// Package resource binds validated resource envelopes to the tenant-scoped state store.
package resource

import (
	"encoding/json/jsontext"
	"fmt"

	"github.com/Kaikei-e/LKAL"
)

const emptyLKALJSON = `{"$lkal":"0.1"}`

var canonicalTopOrder = []string{"meta", "spec", "status"}

// Encode returns the stored form of doc: compact LKAL-JSON (no insignificant whitespace) whose first member is "$lkal":"0.1".
func Encode(doc lkal.Record) ([]byte, error) {
	byKey := make(map[string]lkal.Value, len(canonicalTopOrder))
	for _, field := range doc.Fields {
		switch field.Key {
		case "meta", "spec", "status":
		default:
			return nil, fmt.Errorf("resource: unexpected top-level key %q", field.Key)
		}
		if _, dup := byKey[field.Key]; dup {
			return nil, fmt.Errorf("resource: duplicate top-level key %q", field.Key)
		}
		byKey[field.Key] = field.Value
	}

	f, err := lkal.FromJSON([]byte(emptyLKALJSON))
	if err != nil {
		return nil, err
	}

	for _, key := range canonicalTopOrder {
		if val, ok := byKey[key]; ok {
			if err := f.Set(key, val); err != nil {
				return nil, err
			}
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

// Decode parses LKAL-JSON into its top-level record.
func Decode(data []byte) (lkal.Record, error) {
	f, err := lkal.FromJSON(data)
	if err != nil {
		return lkal.Record{}, err
	}
	return f.Data(), nil
}
