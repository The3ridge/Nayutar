package config_test

import (
	"bytes"
	"os"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/config"
)

func TestDevSchemaCanonicalAndValidatesDevDeclaration(t *testing.T) {
	rawSchema := config.DevSchemaBytes()
	formatted, err := lkal.Format(rawSchema)
	if err != nil {
		t.Fatalf("lkal.Format(dev.schema.lkal): %v", err)
	}
	if !bytes.Equal(rawSchema, formatted) {
		t.Fatalf("config/dev.schema.lkal is not in canonical form:\n--- got ---\n%s\n--- want ---\n%s", rawSchema, formatted)
	}

	devBytes, err := os.ReadFile("dev.lkal")
	if err != nil {
		t.Fatalf("read dev.lkal: %v", err)
	}
	f, err := lkal.Parse("config/dev.lkal", devBytes)
	if err != nil {
		t.Fatalf("parse dev.lkal: %v", err)
	}
	if err := config.DevSchema().Validate(f); err != nil {
		t.Fatalf("validate dev.lkal against dev.schema.lkal: %v", err)
	}
}
