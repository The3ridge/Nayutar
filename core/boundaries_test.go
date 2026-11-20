package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoreDoesNotAddRustModelCheckerOrProofToolchains(t *testing.T) {
	root := repoRoot(t)
	bannedExt := map[string]struct{}{
		".qnt": {},
		".rs":  {},
		".tla": {},
		".vpr": {},
	}
	bannedBase := map[string]struct{}{
		"Cargo.toml":          {},
		"Cargo.lock":          {},
		"quint.toml":          {},
		"rust-toolchain":      {},
		"rust-toolchain.toml": {},
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".lake":
				return filepath.SkipDir
			}
			if rel == "docs" || filepath.ToSlash(rel) == agentWorktrees {
				return filepath.SkipDir
			}
			return nil
		}
		if _, banned := bannedExt[strings.ToLower(filepath.Ext(d.Name()))]; banned {
			t.Errorf("found %s", rel)
		}
		if _, banned := bannedBase[d.Name()]; banned {
			t.Errorf("found %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) == 0 {
		t.Fatal("no .github/workflows/*.yml files found")
	}
	targets := []string{"go.mod", "spec/lakefile.toml"}
	for _, wf := range workflows {
		rel, relErr := filepath.Rel(root, wf)
		if relErr != nil {
			t.Fatal(relErr)
		}
		targets = append(targets, filepath.ToSlash(rel))
	}
	bannedToolchains := []string{"verus", "anvil", "welder", "gobra", "perennial"}
	for _, rel := range targets {
		lower := strings.ToLower(readRepoFile(t, rel))
		for _, name := range bannedToolchains {
			if strings.Contains(lower, name) {
				t.Errorf("%s references banned toolchain %s", rel, name)
			}
		}
	}
}

func TestHumanFacingSurfaceHasNoYAML(t *testing.T) {
	root := repoRoot(t)
	sawDocdag := false
	workflowCount := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".lake":
				return filepath.SkipDir
			}
			if rel, err := filepath.Rel(root, path); err == nil && filepath.ToSlash(rel) == agentWorktrees {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case rel == "docdag.yaml":
			sawDocdag = true
		case rel == ".github/dependabot.yml":
		case filepath.ToSlash(filepath.Dir(rel)) == ".github/workflows" && ext == ".yml":
			workflowCount++
		default:
			t.Errorf("unexpected YAML file %s; only docdag.yaml, .github/dependabot.yml and .github/workflows/*.yml are allowed", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawDocdag {
		t.Error("docdag.yaml is missing")
	}
	if workflowCount == 0 {
		t.Error("no .github/workflows/*.yml files found")
	}
}
