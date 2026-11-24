// Package netns manages Linux network namespace bind-mounts with strict path validation.
package netns

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// DefaultDir is the standard Linux directory for named network namespace bind-mounts.
const DefaultDir = "/run/netns"

// ErrUnsupported is returned on non-Linux operating systems.
var ErrUnsupported = errors.New("netns: unsupported platform")

// ValidateName enforces a safe DNS-label-like identifier ([a-z0-9._-], no path separators or "..").
func ValidateName(name string) error {
	if len(name) < 1 || len(name) > 63 {
		return fmt.Errorf("netns: name %q length must be 1..63", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("netns: name %q must not contain \"..\"", name)
	}
	for i := range len(name) {
		b := name[i]
		alnum := (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
		if alnum {
			continue
		}
		if (b == '-' || b == '_' || b == '.') && i > 0 && i < len(name)-1 {
			continue
		}
		return fmt.Errorf("netns: invalid byte %q in name %q at index %d", b, name, i)
	}
	return nil
}

// ValidateBaseDir checks that baseDir is a cleanable absolute non-root path.
func ValidateBaseDir(baseDir string) (string, error) {
	if baseDir == "" {
		return "", errors.New("netns: base directory must be non-empty")
	}
	if !filepath.IsAbs(baseDir) {
		return "", fmt.Errorf("netns: base directory %q must be absolute", baseDir)
	}
	cleanDir := filepath.Clean(baseDir)
	if cleanDir == "/" {
		return "", errors.New("netns: base directory must not be root \"/\"")
	}
	return cleanDir, nil
}

// ResolvePath validates name and baseDir and enforces that the resulting path stays inside baseDir/.
func ResolvePath(baseDir, name string) (string, error) {
	cleanDir, err := ValidateBaseDir(baseDir)
	if err != nil {
		return "", err
	}
	if err := ValidateName(name); err != nil {
		return "", err
	}
	cleanPath := filepath.Clean(filepath.Join(cleanDir, name))
	if !strings.HasPrefix(cleanPath, cleanDir+"/") || filepath.Dir(cleanPath) != cleanDir {
		return "", fmt.Errorf("netns: resolved path %q escapes base directory %q", cleanPath, cleanDir)
	}
	return cleanPath, nil
}
