package envelope

import (
	_ "embed"
	"slices"

	"github.com/Kaikei-e/LKAL/schema"
)

//go:embed envelope.schema.lkal
var envelopeSchemaBytes []byte

var envelopeSchema *schema.Schema

func init() {
	s, err := schema.Compile("envelope.schema.lkal", envelopeSchemaBytes)
	if err != nil {
		panic("envelope: embedded envelope.schema.lkal failed to compile: " + err.Error())
	}
	envelopeSchema = s
}

// Schema returns the compiled LKAL v0.2 schema describing the common resource envelope.
func Schema() *schema.Schema {
	return envelopeSchema
}

// SchemaBytes returns a copy of the embedded envelope.schema.lkal source.
func SchemaBytes() []byte {
	return slices.Clone(envelopeSchemaBytes)
}
