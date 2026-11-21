package envelope_test

import (
	"bytes"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
)

func TestEnvelopeSchemaCanonicalAndEquivalence(t *testing.T) {
	raw := envelope.SchemaBytes()
	formatted, err := lkal.Format(raw)
	if err != nil {
		t.Fatalf("lkal.Format(envelope.schema.lkal): %v", err)
	}
	if !bytes.Equal(raw, formatted) {
		t.Fatalf("envelope.schema.lkal is not in canonical form:\n--- got ---\n%s\n--- want ---\n%s", raw, formatted)
	}

	s := envelope.Schema()
	if got := s.ID(); got != "nayutar:core/envelope/0.1" {
		t.Fatalf("Schema().ID() = %q, want nayutar:core/envelope/0.1", got)
	}

	cases := []struct {
		name  string
		doc   lkal.Record
		valid bool
	}{
		{
			name: "minimal core workload",
			doc: lkal.Record{Fields: []lkal.Field{
				{Key: "meta", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "kind", Value: lkal.String("workload")},
					{Key: "name", Value: lkal.String("web.prod")},
				}}},
			}},
			valid: true,
		},
		{
			name: "full envelope with custom group, labels, spec, status, condition",
			doc: lkal.Record{Fields: []lkal.Field{
				{Key: "meta", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "group", Value: lkal.String("apps.example.com")},
					{Key: "kind", Value: lkal.String("workload")},
					{Key: "name", Value: lkal.String("web")},
					{Key: "labels", Value: lkal.Record{Fields: []lkal.Field{
						{Key: "app.example.com/name", Value: lkal.String("web")},
					}}},
					{Key: "uid", Value: lkal.String("01234567-89ab-4def-8123-456789abcdef")},
					{Key: "revision", Value: lkal.Int(3)},
					{Key: "generation", Value: lkal.Int(2)},
				}}},
				{Key: "spec", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "replicas", Value: lkal.Int(3)},
				}}},
				{Key: "status", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "observedGeneration", Value: lkal.Int(2)},
					{Key: "custom", Value: lkal.String("ok")},
					{Key: "condition", Value: lkal.Record{Fields: []lkal.Field{
						{Key: "Ready", Value: lkal.Record{Fields: []lkal.Field{
							{Key: "state", Value: lkal.Enum("true")},
							{Key: "reason", Value: lkal.String("MinimumReplicasAvailable")},
							{Key: "since", Value: lkal.String("2026-11-21T12:00:00Z")},
						}}},
					}}},
				}}},
			}},
			valid: true,
		},
		{
			name: "explicit core group rejected",
			doc: lkal.Record{Fields: []lkal.Field{
				{Key: "meta", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "group", Value: lkal.String("core")},
					{Key: "kind", Value: lkal.String("workload")},
					{Key: "name", Value: lkal.String("web")},
				}}},
			}},
			valid: false,
		},
		{
			name: "uppercase kind rejected",
			doc: lkal.Record{Fields: []lkal.Field{
				{Key: "meta", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "kind", Value: lkal.String("Workload")},
					{Key: "name", Value: lkal.String("web")},
				}}},
			}},
			valid: false,
		},
		{
			name: "unknown meta field rejected",
			doc: lkal.Record{Fields: []lkal.Field{
				{Key: "meta", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "kind", Value: lkal.String("workload")},
					{Key: "name", Value: lkal.String("web")},
					{Key: "namespace", Value: lkal.String("default")},
				}}},
			}},
			valid: false,
		},
		{
			name: "unknown top-level field rejected",
			doc: lkal.Record{Fields: []lkal.Field{
				{Key: "meta", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "kind", Value: lkal.String("workload")},
					{Key: "name", Value: lkal.String("web")},
				}}},
				{Key: "extra", Value: lkal.Record{}},
			}},
			valid: false,
		},
		{
			name: "invalid condition type rejected",
			doc: lkal.Record{Fields: []lkal.Field{
				{Key: "meta", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "kind", Value: lkal.String("workload")},
					{Key: "name", Value: lkal.String("web")},
				}}},
				{Key: "status", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "condition", Value: lkal.Record{Fields: []lkal.Field{
						{Key: "ready", Value: lkal.Record{Fields: []lkal.Field{
							{Key: "state", Value: lkal.Enum("true")},
							{Key: "reason", Value: lkal.String("Ok")},
						}}},
					}}},
				}}},
			}},
			valid: false,
		},
		{
			name: "invalid condition timestamp rejected",
			doc: lkal.Record{Fields: []lkal.Field{
				{Key: "meta", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "kind", Value: lkal.String("workload")},
					{Key: "name", Value: lkal.String("web")},
				}}},
				{Key: "status", Value: lkal.Record{Fields: []lkal.Field{
					{Key: "condition", Value: lkal.Record{Fields: []lkal.Field{
						{Key: "Ready", Value: lkal.Record{Fields: []lkal.Field{
							{Key: "state", Value: lkal.Enum("true")},
							{Key: "reason", Value: lkal.String("Ok")},
							{Key: "since", Value: lkal.String("2026-13-01T00:00:00Z")},
						}}},
					}}},
				}}},
			}},
			valid: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shapeOK := envelope.CheckShape(tc.doc) == nil
			schemaOK := s.ValidateValue(tc.doc) == nil
			if shapeOK != tc.valid || schemaOK != tc.valid {
				t.Fatalf("CheckShape ok=%v, Schema.ValidateValue ok=%v, want %v", shapeOK, schemaOK, tc.valid)
			}
		})
	}
}

func FuzzEnvelopeSchemaMatchesCheckShape(f *testing.F) {
	seeds := []string{
		`{"$lkal":"0.1","meta":{"kind":"workload","name":"web"}}`,
		`{"$lkal":"0.1","meta":{"group":"apps.example.com","kind":"workload","name":"web.prod","labels":{"a":"b"},"uid":"01234567-89ab-4def-8123-456789abcdef","revision":1,"generation":1},"spec":{"replicas":2},"status":{"observedGeneration":1,"condition":{"Ready":{"state":{"$enum":"true"},"reason":"Ok","since":"2026-11-21T00:00:00Z"}}}}`,
		`{"$lkal":"0.1","meta":{"group":"core","kind":"workload","name":"web"}}`,
		`{"$lkal":"0.1","meta":{"kind":"Workload","name":"web"}}`,
		`{"$lkal":"0.1","meta":{"kind":"workload","name":"web"},"status":{"observedGeneration":0}}`,
		`{"$lkal":"0.1","meta":{"kind":"workload","name":"web"},"extra":{}}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	envSchema := envelope.Schema()
	f.Fuzz(func(t *testing.T, raw []byte) {
		parsed, err := lkal.FromJSON(raw)
		if err != nil {
			return
		}
		doc := parsed.Data()
		shapeErr := envelope.CheckShape(doc)
		schemaErr := envSchema.ValidateValue(doc)
		if (shapeErr == nil) != (schemaErr == nil) {
			t.Fatalf("mismatch on %s:\nCheckShape: %v\nSchema.ValidateValue: %v", raw, shapeErr, schemaErr)
		}
	})
}
