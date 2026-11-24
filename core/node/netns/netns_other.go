//go:build !linux

package netns

import "context"

// Manager is a non-Linux stub returning ErrUnsupported on runtime operations.
type Manager struct {
	baseDir string
}

// NewManager validates baseDir and returns a stub Manager on non-Linux platforms.
func NewManager(baseDir string) (*Manager, error) {
	if baseDir == "" {
		baseDir = DefaultDir
	}
	cleanDir, err := ValidateBaseDir(baseDir)
	if err != nil {
		return nil, err
	}
	return &Manager{baseDir: cleanDir}, nil
}

// BaseDir returns the cleaned base directory configured on m.
func (m *Manager) BaseDir() string {
	return m.baseDir
}

// Create always returns ErrUnsupported on non-Linux platforms.
func (m *Manager) Create(_ context.Context, name string) (string, error) {
	if _, err := ResolvePath(m.baseDir, name); err != nil {
		return "", err
	}
	return "", ErrUnsupported
}

// Remove always returns ErrUnsupported on non-Linux platforms.
func (m *Manager) Remove(_ context.Context, name string) error {
	if _, err := ResolvePath(m.baseDir, name); err != nil {
		return err
	}
	return ErrUnsupported
}
