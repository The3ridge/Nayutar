package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

var leanSeeds = []string{
	"",
	"theorem foo : True := trivial",
	"import Mathlib\nimport all Nayutar.Store\npublic import Foo\nmeta import Bar\ntheorem t : 1 = 1 := rfl",
	"namespace A.B\ntheorem x : True := sorry\nend A.B",
	"namespace A\nsection S\ntheorem y : True := by sorry\nend S\nend A",
	"mutual\ntheorem a : True := trivial\nend",
	"private theorem p : True := sorry\ntheorem q : True := trivial",
	"@[simp] theorem s : True := trivial\n@[simp, inline]\ntheorem s2 : True := sorryAx _",
	"theorem _root_.Top.t : True := trivial",
	"axiom ax : False\ndef d := sorry",
	"-- theorem c : True := sorry\ntheorem real : True := trivial",
	"/- theorem c : True := sorry /- nested -/ still comment -/ theorem r : True := trivial",
	"/- unterminated block comment theorem u : True := sorry",
	"\"unterminated string theorem u : True := sorry",
	"\"a \\\" quoted \\\\ string\" theorem t : True := trivial",
	"r#\"raw \"string\"# theorem t : True := trivial",
	"r#\"unterminated raw",
	"r##\"two # hashes \"# still\"## theorem t : True := trivial",
	"s!\"interp {x + \"}\"} {y}\" theorem t : True := trivial",
	"s!\"unterminated {interp",
	"f!\"{ -- comment }\n}\" theorem t : True := trivial",
	"'a' '\\n' '\\x41' '\\u0041' '\\'' ''' 'a",
	"«theorem» «sorry» foo.«bar».baz «» «",
	"theorem «x y» : True := trivial",
	".{u} foo.{u, v} @foo x.1 a := b : c",
	"\xff\xfe theorem \xc0\x80 t : True := sorry\xed\xa0\x80",
	"αβγ theorem θ : ∀ x, x = x := fun _ => rfl",
	"theorem t : True := sorry\r\n-- trailing\r\n",
	"theorem\ntheorem\n",
	"end\nend\nend\nnamespace\nsection\nmutual",
	"@[",
	"@[[[",
	"private private private",
}

func FuzzTokenizeLean(f *testing.F) {
	for _, s := range leanSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		tokens := tokenizeLean(src)
		if len(tokens) > len(src) {
			t.Fatalf("%d tokens from %d bytes", len(tokens), len(src))
		}

		type pos struct{ line, col int }
		offsets := map[pos]int{}
		line, col := 1, 1
		for off := 0; off < len(src); {
			offsets[pos{line, col}] = off
			r, w := utf8.DecodeRuneInString(src[off:])
			if r == '\n' {
				line, col = line+1, 1
			} else {
				col++
			}
			off += w
		}

		prev := -1
		for i, tok := range tokens {
			if tok.text == "" {
				t.Fatalf("token %d has empty text", i)
			}
			off, ok := offsets[pos{tok.line, tok.col}]
			if !ok {
				t.Fatalf("token %d %+v is at a line/column that is no rune start of %q", i, tok, src)
			}
			if off <= prev {
				t.Fatalf("token %d %+v starts at byte %d, not after the previous token at %d", i, tok, off, prev)
			}
			prev = off
			if tok.kind != tokIdent && !strings.HasPrefix(src[off:], tok.text) {
				t.Fatalf("token %d %+v is not the source text at byte %d of %q", i, tok, off, src)
			}
			if tok.escaped && tok.kind != tokIdent {
				t.Fatalf("token %d %+v is escaped but not an identifier", i, tok)
			}
		}
	})
}

func FuzzLeanScanners(f *testing.F) {
	for _, s := range leanSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		decls := scanLeanTheorems(src)
		names := leanTheoremDecls(src)
		if len(names) != len(decls) {
			t.Fatalf("leanTheoremDecls returned %d names for %d declarations", len(names), len(decls))
		}
		for i, d := range decls {
			if d.name == "" || names[i] != d.name {
				t.Fatalf("declaration %d: name %q, listed as %q", i, d.name, names[i])
			}
		}
		for _, imp := range leanImports(src) {
			if imp == "" {
				t.Fatalf("empty import in %q", src)
			}
		}

		usesSorry, hasAxiom := leanUsesSorry(src), leanHasAxiom(src)
		if usesSorry && !strings.Contains(src, "sorry") {
			t.Fatalf("leanUsesSorry reports sorry in a source without the word: %q", src)
		}
		if hasAxiom && !strings.Contains(src, "axiom") {
			t.Fatalf("leanHasAxiom reports an axiom in a source without the word: %q", src)
		}
		for _, d := range decls {
			if d.usesSorry && !usesSorry {
				t.Fatalf("declaration %q uses sorry but leanUsesSorry is false for %q", d.name, src)
			}
		}

		// Text after the end of a line comment cannot change what the earlier source declares.
		const tail = "\n-- sorry axiom theorem x : True := sorry\n"
		if leanUsesSorry(src+tail) != usesSorry || leanHasAxiom(src+tail) != hasAxiom {
			t.Fatalf("a trailing comment changed the result for %q", src)
		}
		moreDecls := scanLeanTheorems(src + tail)
		if len(moreDecls) != len(decls) {
			t.Fatalf("a trailing comment changed the declarations of %q: %v vs %v", src, decls, moreDecls)
		}
		for i := range decls {
			if moreDecls[i] != decls[i] {
				t.Fatalf("a trailing comment changed declaration %d of %q: %v vs %v", i, src, decls[i], moreDecls[i])
			}
		}
	})
}
