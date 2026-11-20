package core

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSpecIsIndependentLakeProjectDependingOnKisage(t *testing.T) {
	root := repoRoot(t)
	if fileExists(filepath.Join(root, "lakefile.toml")) || fileExists(filepath.Join(root, "lakefile.lean")) {
		t.Error("Lean project must live under spec/, not the repository root")
	}
	if fileExists(filepath.Join(root, "spec", "lakefile.lean")) {
		t.Error("spec/lakefile.lean must not exist: Lake prefers it over lakefile.toml and would silently replace the reviewed build configuration")
	}
	if fileExists(filepath.Join(root, "spec", "go.mod")) {
		t.Error("spec/ must not be a Go module")
	}

	toolchain := strings.TrimSpace(readRepoFile(t, "spec/lean-toolchain"))
	if toolchain != "leanprover/lean4:v4.35.0" {
		t.Errorf("lean-toolchain = %q, want leanprover/lean4:v4.35.0", toolchain)
	}

	lake := readRepoFile(t, "spec/lakefile.toml")
	for _, needle := range []string{
		`name = "nayutar"`,
		`name = "kisage"`,
		`git = "https://github.com/Kaikei-e/kisage.git"`,
		`rev = "v0.1.2"`,
		`name = "Nayutar"`,
		`name = "nayutar-drt"`,
		`root = "Nayutar.Drt"`,
	} {
		if !strings.Contains(lake, needle) {
			t.Errorf("lakefile.toml missing %s", needle)
		}
	}

	manifest := readRepoFile(t, "spec/lake-manifest.json")
	if !strings.Contains(manifest, `"kisage"`) {
		t.Error("lake-manifest.json does not lock the kisage package")
	}

	sources := leanSources(t, root)
	if len(sources) == 0 {
		t.Fatal("spec/ has no Lean sources")
	}
	imported := false
	seenRel := make(map[string]bool, len(sources))
	for _, path := range sources {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if leanImportsKisage(text) {
			imported = true
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)
		seenRel[rel] = true
		if !isModuleTreeFile(rel, "spec/Nayutar/Statements") {
			if leanUsesSorry(text) {
				t.Errorf("%s uses sorry", rel)
			}
			if leanHasAxiom(text) {
				t.Errorf("%s declares an axiom", rel)
			}
		}
	}
	for _, required := range []string{
		"spec/Nayutar.lean",
		"spec/Nayutar/Spec.lean",
		"spec/Nayutar/Store.lean",
		"spec/Nayutar/Liveness.lean",
		"spec/Nayutar/Drt.lean",
		"spec/Nayutar/Drt/Store.lean",
		"spec/Nayutar/Drt/Liveness.lean",
		"spec/Nayutar/Fixture/Register.lean",
		"spec/Nayutar/Proofs.lean",
		"spec/Nayutar/Proofs/Register.lean",
	} {
		if !seenRel[required] {
			t.Errorf("missing expected Lean source %s", required)
		}
	}
	if !imported {
		t.Error("no Lean module under spec/ imports Kisage")
	}
}

func TestProtectedStatementsMatchComparatorConfig(t *testing.T) {
	var cfg struct {
		ChallengeModule string   `json:"challenge_module"`
		SolutionModule  string   `json:"solution_module"`
		TheoremNames    []string `json:"theorem_names"`
		PermittedAxioms []string `json:"permitted_axioms"`
	}
	if err := json.Unmarshal([]byte(readRepoFile(t, "spec/comparator.json")), &cfg); err != nil {
		t.Fatalf("parse spec/comparator.json: %v", err)
	}
	if cfg.ChallengeModule != "Nayutar.Statements" {
		t.Errorf("challenge_module = %q, want Nayutar.Statements", cfg.ChallengeModule)
	}
	if cfg.SolutionModule != "Nayutar.Proofs" {
		t.Errorf("solution_module = %q, want Nayutar.Proofs", cfg.SolutionModule)
	}
	if len(cfg.TheoremNames) == 0 {
		t.Error("theorem_names must not be empty")
	}
	seenTheorems := make(map[string]bool, len(cfg.TheoremNames))
	for _, name := range cfg.TheoremNames {
		if seenTheorems[name] {
			t.Errorf("duplicate theorem_name %q", name)
		}
		seenTheorems[name] = true
	}

	wantAxioms := map[string]bool{
		"propext":          true,
		"Quot.sound":       true,
		"Classical.choice": true,
	}
	gotAxioms := make(map[string]bool, len(cfg.PermittedAxioms))
	for _, ax := range cfg.PermittedAxioms {
		gotAxioms[ax] = true
	}
	if !maps.Equal(gotAxioms, wantAxioms) {
		t.Errorf("permitted_axioms = %v, want set {propext, Quot.sound, Classical.choice}", cfg.PermittedAxioms)
	}

	root := repoRoot(t)
	if err := CheckProtectedTheorems(root); err != nil {
		t.Fatalf("CheckProtectedTheorems: %v", err)
	}
	var stmtDecls, proofDecls []string
	for _, path := range leanSources(t, root) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)
		isStmt := isModuleTreeFile(rel, "spec/Nayutar/Statements")
		isProof := isModuleTreeFile(rel, "spec/Nayutar/Proofs")
		if !isStmt && !isProof {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if leanHasAxiom(text) {
			t.Errorf("%s declares an axiom", rel)
		}
		if isStmt {
			for _, thm := range scanLeanTheorems(text) {
				stmtDecls = append(stmtDecls, thm.name)
				if !thm.usesSorry {
					t.Errorf("%s theorem %s does not use sorry", rel, thm.name)
				}
			}
		} else {
			proofDecls = append(proofDecls, leanTheoremDecls(text)...)
		}
	}

	if len(stmtDecls) != len(cfg.TheoremNames) {
		t.Errorf("statements theorem count = %d, comparator theorem_names count = %d", len(stmtDecls), len(cfg.TheoremNames))
	}
	for _, listed := range cfg.TheoremNames {
		if n := countTheoremMatches(listed, stmtDecls); n != 1 {
			t.Errorf("theorem_name %q matched %d statements declarations, want 1", listed, n)
		}
		if n := countTheoremMatches(listed, proofDecls); n != 1 {
			t.Errorf("theorem_name %q matched %d proofs declarations, want 1", listed, n)
		}
	}

	nayutar := readRepoFile(t, "spec/Nayutar.lean")
	imports := leanImports(nayutar)
	if !slices.Contains(imports, "Nayutar.Proofs") {
		t.Error("spec/Nayutar.lean must import Nayutar.Proofs")
	}
	for _, imp := range imports {
		if imp == "Nayutar.Statements" || strings.HasPrefix(imp, "Nayutar.Statements.") {
			t.Errorf("spec/Nayutar.lean must not import %s", imp)
		}
	}
}

func isModuleTreeFile(rel, stem string) bool {
	return rel == stem+".lean" || strings.HasPrefix(rel, stem+"/")
}

func TestLeanTheoremDeclsSeesAttributedAndModifiedTheorems(t *testing.T) {
	src := strings.Join([]string{
		"theorem plain : True := trivial",
		"@[simp] theorem a.b : True := trivial",
		"protected theorem c : True := trivial",
		"noncomputable theorem nc : True := trivial",
		"private theorem d : True := trivial",
		"@[simp] private theorem e : True := trivial",
		"private @[simp] theorem f : True := trivial",
		"-- theorem commented",
		"/- theorem block_commented : True := trivial -/",
		"def x := 1 -- see theorem y",
	}, "\n")

	got := leanTheoremDecls(src)
	want := []string{"plain", "a.b", "c", "nc"}
	if !slices.Equal(got, want) {
		t.Fatalf("leanTheoremDecls() = %v, want %v", got, want)
	}
}

func TestLeanSorryAndAxiomScannersIgnoreCommentsAndStrings(t *testing.T) {
	clean := strings.Join([]string{
		"-- sorry and axiom in line comment",
		"/- sorry and axiom in /- nested -/ block comment -/",
		"/--\nDocstring mentioning sorry and axiom.\n-/",
		`def s1 : String := "sorry"`,
		`def s2 : String := "axiom fake : False"`,
		`def s3 : String := s!"{"sorry"} {"axiom fake : False"}"`,
		"def «sorry» : Nat := 0",
		"def «axiom» : Nat := 0",
	}, "\n")
	if leanUsesSorry(clean) {
		t.Fatal("leanUsesSorry matched comment, string, or escaped identifier")
	}
	if leanHasAxiom(clean) {
		t.Fatal("leanHasAxiom matched comment, string, or escaped identifier")
	}

	if !leanUsesSorry("theorem t : True := by sorry") {
		t.Error("leanUsesSorry missed sorry tactic")
	}
	if !leanUsesSorry("theorem t : True := sorryAx True false") {
		t.Error("leanUsesSorry missed sorryAx")
	}
	if !leanHasAxiom("axiom bad : False") {
		t.Error("leanHasAxiom missed axiom declaration")
	}
	if !leanHasAxiom("private axiom bad : False") {
		t.Error("leanHasAxiom missed private axiom declaration")
	}

	mixed := strings.Join([]string{
		"namespace Nayutar.Statements",
		"theorem first : True := by sorry",
		"theorem second : True := trivial",
		"def trailing : True := sorry",
		"end Nayutar.Statements",
	}, "\n")
	got := scanLeanTheorems(mixed)
	want := []leanTheoremDecl{
		{name: "Nayutar.Statements.first", usesSorry: true},
		{name: "Nayutar.Statements.second", usesSorry: false},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("scanLeanTheorems() = %+v, want %+v", got, want)
	}
}

func leanSources(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(root, "spec"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && d.Name() == ".lake" {
			return filepath.SkipDir
		}
		if !d.IsDir() && filepath.Ext(d.Name()) == ".lean" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func leanImportsKisage(src string) bool {
	for _, imp := range leanImports(src) {
		if imp == "Kisage" || strings.HasPrefix(imp, "Kisage.") {
			return true
		}
	}
	return false
}
