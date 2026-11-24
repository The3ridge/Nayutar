package resource_test

import (
	"bytes"
	"encoding/json/jsontext"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
)

func rec(fields ...lkal.Field) lkal.Record {
	return lkal.Record{Fields: fields}
}

func f(k string, v lkal.Value) lkal.Field {
	return lkal.Field{Key: k, Value: v}
}

func TestEncodeOrderCompactAndPrefix(t *testing.T) {
	doc := rec(
		f("status", rec(f("phase", lkal.String("Ready")))),
		f("spec", rec(f("replicas", lkal.Int(3)))),
		f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("web")))),
	)

	encoded, err := resource.Encode(doc)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	wantPrefix := `{"$lkal":"0.1","meta":`
	if !bytes.HasPrefix(encoded, []byte(wantPrefix)) {
		t.Fatalf("encoded = %s, want prefix %s", encoded, wantPrefix)
	}

	metaIdx := bytes.Index(encoded, []byte(`"meta":`))
	specIdx := bytes.Index(encoded, []byte(`"spec":`))
	statusIdx := bytes.Index(encoded, []byte(`"status":`))
	if metaIdx <= 0 || metaIdx >= specIdx || specIdx >= statusIdx {
		t.Fatalf("unexpected field order in %s", encoded)
	}

	compacted := jsontext.Value(bytes.Clone(encoded))
	if err := compacted.Compact(); err != nil {
		t.Fatalf("jsontext.Compact: %v", err)
	}
	if !bytes.Equal(encoded, compacted) {
		t.Fatalf("encoded contains insignificant whitespace: %s", encoded)
	}

	decoded, err := resource.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !lkal.Equal(decoded, doc) {
		t.Fatalf("round-trip mismatch:\ngot:  %#v\nwant: %#v", decoded, doc)
	}
}

func TestCodecAllNineValueTypesRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(42, 99))
	for i := range 25 {
		doc := genAllTypesDoc(rng, i)
		encoded, err := resource.Encode(doc)
		if err != nil {
			t.Fatalf("case %d Encode: %v", i, err)
		}
		if !bytes.HasPrefix(encoded, []byte(`{"$lkal":"0.1"`)) {
			t.Fatalf("case %d missing $lkal prefix: %s", i, encoded)
		}
		decoded, err := resource.Decode(encoded)
		if err != nil {
			t.Fatalf("case %d Decode(%s): %v", i, encoded, err)
		}
		if !lkal.Equal(decoded, doc) {
			t.Fatalf("case %d round-trip mismatch:\ngot:  %#v\nwant: %#v", i, decoded, doc)
		}
	}
}

func genAllTypesDoc(rng *rand.Rand, seed int) lkal.Record {
	labels := rec(
		f("str", lkal.String("hello\nworld\t\"quoted\"")),
		f("num", lkal.Int(rng.Int64N(10000)-5000)),
	)
	spec := rec(
		f("str", lkal.String("v-"+string(rune('a'+seed%26)))),
		f("int", lkal.Int(rng.Int64())),
		f("dec", lkal.Decimal([]string{"0.5", "-12.25", "100.0"}[seed%3])),
		f("bool", lkal.Bool(seed%2 == 0)),
		f("enum", lkal.Enum([]string{"true", "false", "unknown", "fast_mode"}[seed%4])),
		f("qty", lkal.Quantity{Number: "512", Unit: "Mi"}),
		f("ref", lkal.Ref{"secret", "db-creds", "v1"}),
		f("list_str", lkal.List{lkal.String("a"), lkal.String("b")}),
		f("list_int", lkal.List{lkal.Int(1), lkal.Int(2), lkal.Int(3)}),
		f("list_enum", lkal.List{lkal.Enum("one"), lkal.Enum("two")}),
		f("list_qty", lkal.List{lkal.Quantity{Number: "1", Unit: "Gi"}, lkal.Quantity{Number: "250", Unit: "m"}}),
		f("list_ref", lkal.List{lkal.Ref{"pod", "p1"}, lkal.Ref{"svc", "s1"}}),
		f("list_empty", lkal.List{}),
		f("nested", rec(
			f("a/b", lkal.String("slash-key")),
			f("with space", lkal.Decimal("3.14")),
			f("flag", lkal.Bool(true)),
		)),
	)
	status := rec(
		f("observedGeneration", lkal.Int(int64(seed+1))),
		f("ratio", lkal.Decimal("2.5")),
		f("limit", lkal.Quantity{Number: "100", Unit: "m"}),
		f("tags", lkal.List{lkal.Bool(true), lkal.Bool(false)}),
		f("owner", lkal.Ref{"workload", "parent"}),
		f("condition", rec(
			f("Ready", rec(
				f("state", lkal.Enum("true")),
				f("reason", lkal.String("AllReady")),
				f("since", lkal.String("2026-01-02T03:04:05Z")),
			)),
		)),
	)
	return rec(
		f("meta", rec(
			f("kind", lkal.String("workload")),
			f("name", lkal.String("app")),
			f("labels", labels),
		)),
		f("spec", spec),
		f("status", status),
	)
}

func TestEncodeRejectsInvalidLKAL(t *testing.T) {
	cases := []struct {
		name string
		doc  lkal.Record
	}{
		{
			name: "nil value",
			doc:  rec(f("spec", rec(f("x", nil)))),
		},
		{
			name: "invalid UTF-8 string",
			doc:  rec(f("spec", rec(f("x", lkal.String("\xff\xfe"))))),
		},
		{
			name: "invalid UTF-8 top key",
			doc:  rec(f("\xff", lkal.String("a"))),
		},
		{
			name: "dollar-prefixed top key",
			doc:  rec(f("$lkal", lkal.String("0.1"))),
		},
		{
			name: "unknown top-level key",
			doc:  rec(f("meta", rec()), f("extra", lkal.Int(1))),
		},
		{
			name: "dollar-prefixed nested key",
			doc:  rec(f("spec", rec(f("$bad", lkal.Int(1))))),
		},
		{
			name: "duplicate top-level key",
			doc:  rec(f("meta", rec()), f("meta", rec())),
		},
		{
			name: "duplicate nested key",
			doc:  rec(f("spec", rec(f("a", lkal.Int(1)), f("a", lkal.Int(2))))),
		},
		{
			name: "invalid decimal without fraction",
			doc:  rec(f("spec", rec(f("d", lkal.Decimal("42"))))),
		},
		{
			name: "invalid decimal text",
			doc:  rec(f("spec", rec(f("d", lkal.Decimal("1e5"))))),
		},
		{
			name: "invalid quantity missing unit",
			doc:  rec(f("spec", rec(f("q", lkal.Quantity{Number: "10"})))),
		},
		{
			name: "invalid enum name",
			doc:  rec(f("spec", rec(f("e", lkal.Enum("1bad"))))),
		},
		{
			name: "empty ref",
			doc:  rec(f("spec", rec(f("r", lkal.Ref{})))),
		},
		{
			name: "heterogeneous list",
			doc:  rec(f("spec", rec(f("l", lkal.List{lkal.Int(1), lkal.String("two")})))),
		},
		{
			name: "nested list",
			doc:  rec(f("spec", rec(f("l", lkal.List{lkal.List{lkal.Int(1)}})))),
		},
		{
			name: "record inside list",
			doc:  rec(f("spec", rec(f("l", lkal.List{rec(f("a", lkal.Int(1)))})))),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if b, err := resource.Encode(tc.doc); err == nil {
				t.Fatalf("Encode unexpectedly succeeded: %s", b)
			}
		})
	}
}

func TestDecodeRejectsInvalidJSON(t *testing.T) {
	cases := [][]byte{
		nil,
		[]byte(``),
		[]byte(`{}`),
		[]byte(`{"meta":{}}`),
		[]byte(`{"$lkal":"0.2","meta":{}}`),
		[]byte(`{"$lkal":"0.1","x":null}`),
		[]byte(`{"$lkal":"0.1","x":1}{}`),
		[]byte(`{"$lkal":"0.1","x":[1,"two"]}`),
		[]byte(`{"$lkal":"0.1","x":{"$enum":"1bad"}}`),
		[]byte(`[]`),
		[]byte(strings.Repeat("{", 20)),
	}

	for _, raw := range cases {
		if _, err := resource.Decode(raw); err == nil {
			t.Errorf("Decode(%q) succeeded, want error", raw)
		}
	}
}

func FuzzDecode(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"$lkal":"0.1"}`),
		[]byte(`{"$lkal":"0.1","meta":{"kind":"pod","name":"p1","uid":"01234567-89ab-4def-8123-456789abcdef","generation":1}}`),
		[]byte(`{"$lkal":"0.1","spec":{"s":"x","i":-1,"d":1.5,"b":true,"e":{"$enum":"ok"},"q":{"$qty":"512Mi"},"r":{"$ref":"a/b"},"l":[1,2]}}`),
		[]byte(`{"$lkal":"0.1","status":{"observedGeneration":1,"ratio":0.25}}`),
		[]byte(`{"$lkal":"0.1","other":1}`),
		[]byte(`{}`),
		[]byte(`{"$lkal":"0.2"}`),
		[]byte(`{"$lkal":"0.1","x":null}`),
		[]byte(`garbage`),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		doc, err := resource.Decode(data)
		if err != nil {
			return
		}
		encoded, err := resource.Encode(doc)
		if err != nil {
			return
		}
		roundTrip, err := resource.Decode(encoded)
		if err != nil {
			t.Fatalf("Decode(Encode(Decode(%q))) failed: %v\nencoded: %s", data, err, encoded)
		}
		if !lkal.Equal(roundTrip, doc) {
			t.Fatalf("round-trip not lkal.Equal:\ninput: %q\nencoded: %s\ngot: %#v\nwant: %#v", data, encoded, roundTrip, doc)
		}
	})
}
