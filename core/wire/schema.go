// Package wire implements the HTTP/2 LKAL-JSON wire handler, codecs, schemas, and client.
package wire

import (
	_ "embed"
	"encoding/json/jsontext"
	"slices"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/LKAL/schema"
)

const emptyLKALJSON = `{"$lkal":"0.1"}`

//go:embed error.schema.lkal
var errorSchemaBytes []byte

//go:embed list.schema.lkal
var listSchemaBytes []byte

//go:embed watch_event.schema.lkal
var watchEventSchemaBytes []byte

var (
	errorSchema      *schema.Schema
	listSchema       *schema.Schema
	watchEventSchema *schema.Schema
)

func init() {
	var err error
	errorSchema, err = schema.Compile("error.schema.lkal", errorSchemaBytes)
	if err != nil {
		panic("wire: embedded error.schema.lkal failed to compile: " + err.Error())
	}
	listSchema, err = schema.Compile("list.schema.lkal", listSchemaBytes)
	if err != nil {
		panic("wire: embedded list.schema.lkal failed to compile: " + err.Error())
	}
	watchEventSchema, err = schema.Compile("watch_event.schema.lkal", watchEventSchemaBytes)
	if err != nil {
		panic("wire: embedded watch_event.schema.lkal failed to compile: " + err.Error())
	}
}

// ErrorSchema returns the compiled LKAL v0.2 schema for wire error responses.
func ErrorSchema() *schema.Schema { return errorSchema }

// ErrorSchemaBytes returns a copy of the embedded error.schema.lkal source.
func ErrorSchemaBytes() []byte { return slices.Clone(errorSchemaBytes) }

// ListSchema returns the compiled LKAL v0.2 schema for wire list responses.
func ListSchema() *schema.Schema { return listSchema }

// ListSchemaBytes returns a copy of the embedded list.schema.lkal source.
func ListSchemaBytes() []byte { return slices.Clone(listSchemaBytes) }

// WatchEventSchema returns the compiled LKAL v0.2 schema for wire watch events.
func WatchEventSchema() *schema.Schema { return watchEventSchema }

// WatchEventSchemaBytes returns a copy of the embedded watch_event.schema.lkal source.
func WatchEventSchemaBytes() []byte { return slices.Clone(watchEventSchemaBytes) }

func encodeTopFields(fields []lkal.Field, s *schema.Schema) ([]byte, error) {
	f, err := lkal.FromJSON([]byte(emptyLKALJSON))
	if err != nil {
		return nil, err
	}
	for _, fld := range fields {
		if err := f.Set(fld.Key, fld.Value); err != nil {
			return nil, err
		}
	}
	if s != nil {
		if err := s.Validate(f); err != nil {
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
