//go:build linux

package netns

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// Manager creates and removes bind-mounted Linux network namespaces under baseDir.
type Manager struct {
	baseDir string
	unshare func(int) error
	mount   func(source, target, fstype string, flags uintptr, data string) error
	unmount func(target string, flags int) error
}

// NewManager validates baseDir (defaulting to DefaultDir when empty) and returns a Manager.
func NewManager(baseDir string) (*Manager, error) {
	if baseDir == "" {
		baseDir = DefaultDir
	}
	cleanDir, err := ValidateBaseDir(baseDir)
	if err != nil {
		return nil, err
	}
	return &Manager{
		baseDir: cleanDir,
		unshare: unix.Unshare,
		mount:   unix.Mount,
		unmount: unix.Unmount,
	}, nil
}

// BaseDir returns the cleaned base directory used for netns bind-mounts.
func (m *Manager) BaseDir() string {
	return m.baseDir
}

// Create creates an isolated Linux network namespace and bind-mounts it under baseDir/<name>.
func (m *Manager) Create(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path, err := ResolvePath(m.baseDir, name)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(m.baseDir, 0o755); err != nil {
		return "", fmt.Errorf("netns: mkdir %s: %w", m.baseDir, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDONLY, 0o444)
	if err != nil {
		return "", fmt.Errorf("netns: create mount target %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("netns: close mount target %s: %w", path, err)
	}

	if err := runOnDisposableWorkerThread(func(pid, tid int) error {
		if err := m.unshare(unix.CLONE_NEWNET); err != nil {
			return fmt.Errorf("netns: unshare CLONE_NEWNET: %w", err)
		}
		// Never use /proc/self/ns/net in a multi-threaded Go process: /proc/self
		// resolves to the thread-group leader (pid), not the calling worker thread (tid).
		threadNetns := fmt.Sprintf("/proc/%d/task/%d/ns/net", pid, tid)
		if err := m.mount(threadNetns, path, "none", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("netns: bind-mount %s from %s: %w", path, threadNetns, err)
		}
		return nil
	}); err != nil {
		_ = os.Remove(path)
		return "", err
	}

	hostFI, herr := os.Stat(fmt.Sprintf("/proc/%d/ns/net", os.Getpid()))
	targetFI, terr := os.Stat(path)
	if herr == nil && terr == nil && os.SameFile(hostFI, targetFI) {
		_ = m.unmount(path, unix.MNT_DETACH|unix.UMOUNT_NOFOLLOW)
		_ = os.Remove(path)
		return "", fmt.Errorf("netns: mounted namespace %s matches process host netns", path)
	}
	return path, nil
}

// runOnDisposableWorkerThread executes fn on a locked non-leader OS thread (tid != pid)
// and returns without calling UnlockOSThread on that worker so the Go runtime terminates
// the tainted OS thread instead of reusing it or mutating the process leader thread (m0).
func runOnDisposableWorkerThread(fn func(pid, tid int) error) error {
	pid := os.Getpid()
	errCh := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if unix.Gettid() == pid {
			// Hold m0 locked while spawning a child goroutine so the Go scheduler
			// must place the child on a distinct worker OS thread (tid != pid).
			defer runtime.UnlockOSThread()
			innerCh := make(chan error, 1)
			go func() {
				runtime.LockOSThread()
				innerCh <- fn(pid, unix.Gettid())
			}()
			errCh <- <-innerCh
			return
		}
		errCh <- fn(pid, unix.Gettid())
	}()
	return <-errCh
}

// Remove idempotently unmounts (MNT_DETACH|UMOUNT_NOFOLLOW) and deletes the bind-mounted netns at baseDir/<name>.
func (m *Manager) Remove(_ context.Context, name string) error {
	path, err := ResolvePath(m.baseDir, name)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("netns: lstat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("netns: refusing to unmount symlink %s", path)
	}
	if err := m.unmount(path, unix.MNT_DETACH|unix.UMOUNT_NOFOLLOW); err != nil && !errors.Is(err, unix.ENOENT) && !errors.Is(err, unix.EINVAL) {
		return fmt.Errorf("netns: unmount %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("netns: remove %s: %w", path, err)
	}
	return nil
}
