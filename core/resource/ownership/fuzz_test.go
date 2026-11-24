package ownership_test

import (
	"bytes"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/ownership"
)

func FuzzDecodeTrie(f *testing.F) {
	f.Add([]byte(`{"$lkal":"0.1","actors":["User:alice"]}`))
	f.Add([]byte(`{"$lkal":"0.1","actors":["User:alice","User:bob"],"trie":{"spec":{"c":{"replicas":{"s":{"$enum":"owned"},"a":1,"o":{"$enum":"modified"}},"mesh":{"s":{"$enum":"owned"},"a":1,"o":{"$enum":"created"},"w":true},"note":{"s":{"$enum":"unowned"}},"def":{"s":{"$enum":"defaulted"}}}}}}`))
	f.Add([]byte(`{"$lkal":"0.1","actors":[]}`))
	f.Add([]byte(`{`))

	doc := objDoc("web",
		&lkal.Record{Fields: []lkal.Field{fld("env", lkal.String("prod"))}},
		&lkal.Record{Fields: []lkal.Field{
			fld("replicas", lkal.Int(3)),
			fld("mesh", rec(fld("a", lkal.Int(1)), fld("b", lkal.Int(2)))),
			fld("note", lkal.String("x")),
			fld("def", lkal.Int(5)),
		}},
	)

	f.Fuzz(func(t *testing.T, data []byte) {
		st, err := ownership.Decode(data, doc)
		if err != nil {
			return
		}
		re, err := ownership.Encode(st, doc)
		if err != nil {
			t.Fatalf("Encode failed after Decode succeeded: %v", err)
		}
		if bytes.ContainsRune(re, '\n') {
			t.Fatalf("Encode emitted newline: %s", re)
		}
		st2, err := ownership.Decode(re, doc)
		if err != nil || !st2.Equal(st) {
			t.Fatalf("Decode/Encode roundtrip mismatch: err=%v\ngot:  %+v\nwant: %+v", err, st2, st)
		}
	})
}

func FuzzParsePath(f *testing.F) {
	f.Add("spec/replicas")
	f.Add(`meta/labels/"app.example.com/name"`)
	f.Add("/spec/a/b")
	f.Add("")
	f.Add("spec/$bad")
	f.Fuzz(func(t *testing.T, s string) {
		p, err := ownership.ParsePath(s)
		if err != nil {
			return
		}
		p2, err := ownership.ParsePath(p.String())
		if err != nil || !p2.Equal(p) {
			t.Fatalf("ParsePath roundtrip mismatch for %q -> %q: %v (%v)", s, p.String(), p2, err)
		}
	})
}
