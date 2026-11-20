// Package drt runs executable Lean models so Go tests can compare an implementation against them.
package drt

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Config holds case count and PRNG seed settings for a DRT run.
type Config struct {
	Cases int
	Seed  uint64
}

// ConfigFromEnv loads Config from NAYUTAR_DRT_CASES and NAYUTAR_DRT_SEED.
func ConfigFromEnv() (Config, error) {
	cfg := Config{
		Cases: 1000,
		Seed:  1,
	}
	if raw := os.Getenv("NAYUTAR_DRT_CASES"); raw != "" {
		cases, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("NAYUTAR_DRT_CASES: %w", err)
		}
		if cases < 1 {
			return Config{}, fmt.Errorf("NAYUTAR_DRT_CASES must be >= 1, got %d", cases)
		}
		cfg.Cases = cases
	}
	if raw := os.Getenv("NAYUTAR_DRT_SEED"); raw != "" {
		seed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("NAYUTAR_DRT_SEED: %w", err)
		}
		cfg.Seed = seed
	}
	return cfg, nil
}

var (
	buildOnce sync.Once
	repoRoot  string
	buildErr  error
)

// RunModel builds the Lean executable once per process and runs one suite over the cases.
func RunModel(suite string, cases [][]byte) ([][]byte, error) {
	var stdin bytes.Buffer
	for i, c := range cases {
		if bytes.IndexByte(c, '\n') >= 0 {
			return nil, fmt.Errorf("case %d contains a newline", i)
		}
		stdin.Write(c)
		stdin.WriteByte('\n')
	}

	buildOnce.Do(func() {
		repoRoot, buildErr = findRepoRoot()
		if buildErr != nil {
			return
		}
		lakePath, err := findLake()
		if err != nil {
			buildErr = err
			return
		}
		cmd := exec.Command(lakePath, "build", "nayutar-drt")
		cmd.Dir = filepath.Join(repoRoot, "spec")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("lake build nayutar-drt: %w\n%s", err, strings.TrimSpace(string(out)))
		}
	})
	if buildErr != nil {
		return nil, buildErr
	}

	bin := filepath.Join(repoRoot, "spec", ".lake", "build", "bin", "nayutar-drt")
	cmd := exec.Command(bin, suite)
	var stdout, stderr bytes.Buffer
	cmd.Stdin = &stdin
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("nayutar-drt %s: %w: %s", suite, err, strings.TrimSpace(stderr.String()))
	}

	var lines [][]byte
	for _, line := range bytes.Split(stdout.Bytes(), []byte("\n")) {
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	if len(lines) != len(cases) {
		return nil, fmt.Errorf("nayutar-drt %s: got %d output lines for %d cases", suite, len(lines), len(cases))
	}
	return lines, nil
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if fileExists(filepath.Join(dir, "go.mod")) && fileExists(filepath.Join(dir, "docdag.yaml")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("repository root not found")
		}
		dir = parent
	}
}

func findLake() (string, error) {
	if path, err := exec.LookPath("lake"); err == nil {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err == nil {
		candidate := filepath.Join(home, ".elan", "bin", "lake")
		if fileExists(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("lake executable not found")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
