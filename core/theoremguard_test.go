package core

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCheckProtectedTheoremsAcceptsTheRepository(t *testing.T) {
	if err := CheckProtectedTheorems(repoRoot(t)); err != nil {
		t.Fatal(err)
	}
}

func TestDeletingProtectedTheoremFromProofsFailsWithDiagnostic(t *testing.T) {
	root := copyProtectedSpec(t)
	proof := filepath.Join(root, "spec", "Nayutar", "Proofs", "Register.lean")
	src := readFile(t, proof)
	updated := deleteTheoremBlock(src, "Register.step_revision_monotone")
	if updated == src {
		t.Fatal("proof fixture was not changed")
	}
	writeFile(t, proof, updated)

	const want = "missing protected theorem: Nayutar.Fixture.Register.step_revision_monotone in solution module Nayutar.Proofs"
	err := CheckProtectedTheorems(root)
	if err == nil {
		t.Fatal("deleted proof theorem was accepted")
	}
	if err.Error() != want {
		t.Fatalf("CheckProtectedTheorems() = %q, want %q", err.Error(), want)
	}

	code, out := runProtectedTheoremCheck(t, root)
	if code != 1 {
		t.Fatalf("checker exit = %d, want 1\n%s", code, out)
	}
	if strings.TrimSpace(out) != want {
		t.Fatalf("checker output = %q, want %q", strings.TrimSpace(out), want)
	}
}

func TestDeletingProtectedTheoremFromStatementsFailsWithDiagnostic(t *testing.T) {
	root := copyProtectedSpec(t)
	path := filepath.Join(root, "spec", "Nayutar", "Statements", "Register.lean")
	src := readFile(t, path)
	updated := deleteTheoremBlock(src, "Register.step_revision_monotone")
	writeFile(t, path, updated)

	err := CheckProtectedTheorems(root)
	if err == nil {
		t.Fatal("deleted statement theorem was accepted")
	}
	const want = "missing protected theorem: Nayutar.Fixture.Register.step_revision_monotone in challenge module Nayutar.Statements"
	if err.Error() != want {
		t.Fatalf("CheckProtectedTheorems() = %q, want %q", err.Error(), want)
	}
}

func TestUnimportedProtectedTheoremIsMissing(t *testing.T) {
	root := copyProtectedSpec(t)
	path := filepath.Join(root, "spec", "Nayutar", "Proofs.lean")
	src := readFile(t, path)
	updated := strings.Replace(src, "import Nayutar.Proofs.Register\n", "", 1)
	if updated == src {
		t.Fatal("proof root import was not removed")
	}
	writeFile(t, path, updated)

	err := CheckProtectedTheorems(root)
	if err == nil {
		t.Fatal("unimported proof module was accepted")
	}
	for _, name := range []string{
		"Nayutar.Fixture.Register.step_revision_monotone",
		"Nayutar.Fixture.Register.step_rejected_unchanged",
		"Nayutar.Fixture.Register.step_accepted_bumps",
	} {
		line := "missing protected theorem: " + name + " in solution module Nayutar.Proofs"
		if !strings.Contains(err.Error(), line) {
			t.Errorf("diagnostic missing %q\n%s", line, err.Error())
		}
	}
	if strings.Contains(err.Error(), "challenge module") {
		t.Errorf("statements were reported missing:\n%s", err.Error())
	}
}

func TestWeakenedSorryAndExtraAxiomStayDeclared(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(string) string
	}{
		{
			name: "weakened statement",
			mutate: func(src string) string {
				return strings.Replace(src, "r.revision ≤", "r.revision <", 1)
			},
		},
		{
			name: "sorry",
			mutate: func(src string) string {
				return strings.Replace(src,
					"theorem Register.step_revision_monotone (r : Register) (op : Op) : r.revision ≤ (r.step op).state.revision := by\n  cases op with",
					"theorem Register.step_revision_monotone (r : Register) (op : Op) : r.revision ≤ (r.step op).state.revision := by\n  sorry\n  cases op with",
					1)
			},
		},
		{
			name: "extra axiom",
			mutate: func(src string) string {
				return strings.Replace(src, "namespace Nayutar.Fixture\n", "namespace Nayutar.Fixture\n\naxiom nayutarGuardAx : True\n", 1)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := copyProtectedSpec(t)
			path := filepath.Join(root, "spec", "Nayutar", "Proofs", "Register.lean")
			src := readFile(t, path)
			updated := tc.mutate(src)
			if updated == src {
				t.Fatal("proof fixture was not changed")
			}
			writeFile(t, path, updated)
			if err := CheckProtectedTheorems(root); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWrongNamespaceProtectedTheoremIsRejected(t *testing.T) {
	const want = "missing protected theorem: Nayutar.Fixture.Register.step_revision_monotone in solution module Nayutar.Proofs"

	cases := []struct {
		name        string
		replacement string
	}{
		{
			name:        "short name in wrong namespace",
			replacement: "end Nayutar.Fixture\n\nnamespace Wrong\ntheorem step_revision_monotone : True := trivial\nend Wrong\n",
		},
		{
			name:        "qualified suffix in wrong namespace",
			replacement: "end Nayutar.Fixture\n\nnamespace Wrong\ntheorem Register.step_revision_monotone : True := trivial\nend Wrong\n",
		},
		{
			name:        "qualified suffix outside namespace",
			replacement: "end Nayutar.Fixture\n\ntheorem Register.step_revision_monotone : True := trivial\n",
		},
		{
			name:        "root escape with incomplete name",
			replacement: "theorem _root_.Register.step_revision_monotone : True := trivial\nend Nayutar.Fixture\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := copyProtectedSpec(t)
			proof := filepath.Join(root, "spec", "Nayutar", "Proofs", "Register.lean")
			src := deleteTheoremBlock(readFile(t, proof), "Register.step_revision_monotone")
			updated := strings.Replace(src, "end Nayutar.Fixture", tc.replacement, 1)
			if updated == src {
				t.Fatal("proof fixture was not changed")
			}
			writeFile(t, proof, updated)

			err := CheckProtectedTheorems(root)
			if err == nil {
				t.Fatal("wrong-namespace theorem was accepted")
			}
			if err.Error() != want {
				t.Fatalf("CheckProtectedTheorems() = %q, want %q", err.Error(), want)
			}
		})
	}
}

func TestSameShortNameInDistinctNamespacesIsAccepted(t *testing.T) {
	root := t.TempDir()
	writeSpecTree(t, root, map[string]string{
		"spec/comparator.json": `{
  "challenge_module": "Nayutar.Statements",
  "solution_module": "Nayutar.Proofs",
  "theorem_names": [
    "Nayutar.Watch.step_monotone",
    "Nayutar.Ownership.step_monotone"
  ]
}`,
		"spec/Nayutar/Statements.lean": "import Nayutar.Statements.Watch Nayutar.Statements.Ownership\n",
		"spec/Nayutar/Statements/Watch.lean": strings.Join([]string{
			"namespace Nayutar.Watch",
			"theorem step_monotone : True := by sorry",
			"end Nayutar.Watch",
		}, "\n"),
		"spec/Nayutar/Statements/Ownership.lean": strings.Join([]string{
			"namespace Nayutar.Ownership",
			"theorem step_monotone : True := by sorry",
			"end Nayutar.Ownership",
		}, "\n"),
		"spec/Nayutar/Proofs.lean": "public import Nayutar.Proofs.Watch Nayutar.Proofs.Ownership\n",
		"spec/Nayutar/Proofs/Watch.lean": strings.Join([]string{
			"namespace Nayutar.Watch",
			"theorem step_monotone : True := trivial",
			"end Nayutar.Watch",
		}, "\n"),
		"spec/Nayutar/Proofs/Ownership.lean": strings.Join([]string{
			"namespace Nayutar.Ownership",
			"theorem step_monotone : True := trivial",
			"end Nayutar.Ownership",
		}, "\n"),
	})

	if err := CheckProtectedTheorems(root); err != nil {
		t.Fatalf("CheckProtectedTheorems() = %v, want nil", err)
	}
}

func TestPrivateProtectedTheoremIsRejected(t *testing.T) {
	const want = "missing protected theorem: Nayutar.Fixture.Register.step_revision_monotone in solution module Nayutar.Proofs"

	for _, prefix := range []string{
		"private theorem Register.step_revision_monotone",
		"@[simp] private theorem Register.step_revision_monotone",
		"private @[simp] noncomputable theorem Register.step_revision_monotone",
	} {
		t.Run(prefix, func(t *testing.T) {
			root := copyProtectedSpec(t)
			proof := filepath.Join(root, "spec", "Nayutar", "Proofs", "Register.lean")
			src := readFile(t, proof)
			updated := strings.Replace(src, "theorem Register.step_revision_monotone", prefix, 1)
			if updated == src {
				t.Fatal("proof fixture was not changed")
			}
			writeFile(t, proof, updated)

			err := CheckProtectedTheorems(root)
			if err == nil {
				t.Fatal("private theorem was accepted")
			}
			if err.Error() != want {
				t.Fatalf("CheckProtectedTheorems() = %q, want %q", err.Error(), want)
			}
		})
	}
}

func TestBlockCommentsAndStringsAreIgnored(t *testing.T) {
	const want = "missing protected theorem: Nayutar.Fixture.Register.step_revision_monotone in solution module Nayutar.Proofs"

	cases := []struct {
		name string
		stub string
	}{
		{
			name: "block comment",
			stub: "/- theorem Register.step_revision_monotone : True := trivial -/",
		},
		{
			name: "nested block comment",
			stub: "/- outer /- inner -/\ntheorem Register.step_revision_monotone : True := trivial\n-/",
		},
		{
			name: "string literal with escaped quote",
			stub: `def msg : String := "quote \" theorem Register.step_revision_monotone : True := trivial"`,
		},
		{
			name: "interpolated string literal",
			stub: `def msg : String := s!"{"theorem Register.step_revision_monotone : True := trivial"}"`,
		},
		{
			name: "raw string literal",
			stub: `def msg : String := r#"theorem Register.step_revision_monotone : True := trivial"#`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := copyProtectedSpec(t)
			proof := filepath.Join(root, "spec", "Nayutar", "Proofs", "Register.lean")
			src := deleteTheoremBlock(readFile(t, proof), "Register.step_revision_monotone")
			updated := strings.Replace(src, "namespace Nayutar.Fixture\n", "namespace Nayutar.Fixture\n"+tc.stub+"\n", 1)
			writeFile(t, proof, updated)

			err := CheckProtectedTheorems(root)
			if err == nil {
				t.Fatal("commented or quoted theorem was accepted")
			}
			if err.Error() != want {
				t.Fatalf("CheckProtectedTheorems() = %q, want %q", err.Error(), want)
			}
		})
	}

	t.Run("block-commented import", func(t *testing.T) {
		root := copyProtectedSpec(t)
		proofRoot := filepath.Join(root, "spec", "Nayutar", "Proofs.lean")
		writeFile(t, proofRoot, "/- import Nayutar.Proofs.Register -/\n")
		if err := CheckProtectedTheorems(root); err == nil {
			t.Fatal("block-commented import was accepted")
		}
	})
}

func TestMultilineDocstringDoesNotDuplicateTheorem(t *testing.T) {
	root := copyProtectedSpec(t)
	proof := filepath.Join(root, "spec", "Nayutar", "Proofs", "Register.lean")
	src := readFile(t, proof)
	doc := strings.Join([]string{
		"/--",
		"Proves theorem Register.step_revision_monotone",
		"and mentions theorem Nayutar.Fixture.Register.step_revision_monotone.",
		"-/",
		"theorem Register.step_revision_monotone",
	}, "\n")
	updated := strings.Replace(src, "theorem Register.step_revision_monotone", doc, 1)
	if updated == src {
		t.Fatal("proof fixture was not changed")
	}
	writeFile(t, proof, updated)

	if err := CheckProtectedTheorems(root); err != nil {
		t.Fatalf("CheckProtectedTheorems() = %v, want nil", err)
	}
}

func TestLeanSyntaxFormsScopesAndImportsParseCleanly(t *testing.T) {
	imports := leanImports(strings.Join([]string{
		"/-! Module header doc mentioning import Fake.Module -/",
		"-- import Commented.Out",
		"import Nayutar.A Nayutar.B",
		"public import Nayutar.C Nayutar.D",
		"meta import Nayutar.E",
		"namespace Nayutar",
	}, "\n"))
	wantImports := []string{"Nayutar.A", "Nayutar.B", "Nayutar.C", "Nayutar.D", "Nayutar.E"}
	if !slices.Equal(imports, wantImports) {
		t.Fatalf("leanImports() = %v, want %v", imports, wantImports)
	}

	src := strings.Join([]string{
		"open Unrelated.Namespace",
		"namespace Nayutar.Fixture",
		"def quoteChar : Char := '\"'",
		"def helper (x' : Nat) : Nat := x'",
		"section Helpers",
		"theorem foo(r : Nat) : True := trivial",
		"theorem bar: True := trivial",
		"theorem baz.{u, v} (α : Sort u) (β : Sort v) : True := trivial",
		"theorem",
		"  multiline",
		"  : True := trivial",
		"mutual",
		"  def f : Nat → Nat | 0 => 0 | n + 1 => g n",
		"  def g : Nat → Nat | 0 => 0 | n + 1 => f n",
		"end",
		"@[simp, inline] protected noncomputable theorem prot : True := trivial",
		"private def privHelper : Nat := 0",
		"theorem afterPrivDef : True := trivial",
		"private theorem hidden : True := trivial",
		"theorem _root_.Top.escaped : True := trivial",
		"end Helpers",
		"section",
		"  indentedCustomCommand arg",
		"  theorem inAnonSection : True := trivial",
		"end",
		"  indentedAfterEnd arg",
		"theorem stillInNamespace : True := trivial",
		"end Nayutar.Fixture",
		"theorem atRoot : True := trivial",
	}, "\n")

	got := leanTheoremDecls(src)
	want := []string{
		"Nayutar.Fixture.foo",
		"Nayutar.Fixture.bar",
		"Nayutar.Fixture.baz",
		"Nayutar.Fixture.multiline",
		"Nayutar.Fixture.prot",
		"Nayutar.Fixture.afterPrivDef",
		"Top.escaped",
		"Nayutar.Fixture.inAnonSection",
		"Nayutar.Fixture.stillInNamespace",
		"atRoot",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("leanTheoremDecls() =\n  %v\nwant\n  %v", got, want)
	}
}

func TestAnonymousSectionAndEndIgnoreNextLineIndentedTokens(t *testing.T) {
	src := strings.Join([]string{
		"namespace Outer",
		"section",
		"  CustomCommand x",
		"  theorem inside : True := trivial",
		"end",
		"  AnotherCommand y",
		"theorem afterAnonEnd : True := trivial",
		"end Outer",
		"theorem outside : True := trivial",
	}, "\n")

	got := leanTheoremDecls(src)
	want := []string{"Outer.inside", "Outer.afterAnonEnd", "outside"}
	if !slices.Equal(got, want) {
		t.Fatalf("leanTheoremDecls() = %v, want %v", got, want)
	}
}

func TestComparatorRejectionScriptCoversTheNegativeCases(t *testing.T) {
	script := readRepoFile(t, "scripts/tests/test-comparator-rejections.sh")
	if strings.Contains(script, "--inadvisably-no-sandbox") {
		t.Error("rejection script must not pass --inadvisably-no-sandbox")
	}
	for _, needle := range []string{
		"lake comparator",
		"./cmd/check-protected-theorems",
		"missing protected theorem",
		"weakened",
		"sorry",
		"axiom",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("rejection script missing %s", needle)
		}
	}
}

func copyProtectedSpec(t *testing.T) string {
	t.Helper()
	srcRoot := repoRoot(t)
	dst := t.TempDir()
	rels := []string{
		"spec/comparator.json",
		"spec/Nayutar/Statements.lean",
		"spec/Nayutar/Proofs.lean",
	}
	for _, dir := range []string{"spec/Nayutar/Statements", "spec/Nayutar/Proofs"} {
		matches, err := filepath.Glob(filepath.Join(srcRoot, filepath.FromSlash(dir), "*.lean"))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range matches {
			rels = append(rels, dir+"/"+filepath.Base(m))
		}
	}
	for _, rel := range rels {
		data, err := os.ReadFile(filepath.Join(srcRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

func writeSpecTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		out := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func deleteTheoremBlock(src, decl string) string {
	lines := strings.Split(src, "\n")
	var out []string
	skipping := false
	for _, line := range lines {
		code := line
		if i := strings.Index(code, "--"); i >= 0 {
			code = code[:i]
		}
		if strings.Contains(code, "theorem "+decl) {
			skipping = true
			continue
		}
		if skipping {
			if strings.Contains(code, "theorem ") {
				skipping = false
				out = append(out, line)
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runProtectedTheoremCheck(t *testing.T, root string) (int, string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "check-protected-theorems")
	build := exec.Command("go", "build", "-o", bin, "./cmd/check-protected-theorems")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build checker: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "-root", root)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return 0, string(out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatal(err)
	}
	return exitErr.ExitCode(), string(out)
}
