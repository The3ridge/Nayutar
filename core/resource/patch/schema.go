package patch

import (
	_ "embed"
	"slices"

	"github.com/Kaikei-e/LKAL/schema"
)

//go:embed patch.schema.lkal
var patchSchemaBytes []byte

var patchSchema *schema.Schema

func init() {
	s, err := schema.Compile("patch.schema.lkal", patchSchemaBytes)
	if err != nil {
		panic("patch: embedded patch.schema.lkal failed to compile: " + err.Error())
	}
	patchSchema = s
}

// Schema returns the compiled LKAL v0.2 schema describing ordered-set patch documents.
func Schema() *schema.Schema {
	return patchSchema
}

// SchemaBytes returns a copy of the embedded patch.schema.lkal source.
func SchemaBytes() []byte {
	return slices.Clone(patchSchemaBytes)
}
