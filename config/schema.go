// Package config embeds LKAL v0.2 schemas for repository configuration files.
package config

import (
	_ "embed"
	"slices"

	"github.com/Kaikei-e/LKAL/schema"
)

//go:embed dev.schema.lkal
var devSchemaBytes []byte

var devSchema *schema.Schema

func init() {
	s, err := schema.Compile("config/dev.schema.lkal", devSchemaBytes)
	if err != nil {
		panic("config: embedded dev.schema.lkal failed to compile: " + err.Error())
	}
	devSchema = s
}

// DevSchema returns the compiled LKAL v0.2 schema for config/dev.lkal.
func DevSchema() *schema.Schema {
	return devSchema
}

// DevSchemaBytes returns a copy of the embedded config/dev.schema.lkal source.
func DevSchemaBytes() []byte {
	return slices.Clone(devSchemaBytes)
}
