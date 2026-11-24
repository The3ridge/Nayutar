package netns

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestValidateName(t *testing.T) {
	valid := []string{"a", "0", "web-1", "unit_01.prod", "a1-b2_c3.d4"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Fatalf("ValidateName(%q) failed: %v", name, err)
		}
	}

	invalid := []string{
		"",
		".",
		"..",
		"a..b",
		"a/b",
		"/abs",
		"-lead",
		"trail-",
		".lead",
		"trail.",
		"Upper",
		"space name",
		"null\x00byte",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 64 bytes
	}
	for _, name := range invalid {
		if err := ValidateName(name); err == nil {
			t.Fatalf("ValidateName(%q) succeeded, want error", name)
		}
	}
}

func TestResolvePathBoundary(t *testing.T) {
	got, err := ResolvePath("/run/netns/", "unit-1")
	if err != nil {
		t.Fatalf("ResolvePath failed: %v", err)
	}
	if got != "/run/netns/unit-1" {
		t.Fatalf("ResolvePath = %q, want /run/netns/unit-1", got)
	}

	if _, err := ResolvePath("relative/netns", "unit-1"); err == nil {
		t.Fatal("expected error for relative baseDir")
	}
	if _, err := ResolvePath("/", "unit-1"); err == nil {
		t.Fatal("expected error for root baseDir")
	}
	if _, err := ResolvePath("/run/netns", "../escape"); err == nil {
		t.Fatal("expected error for traversal name")
	}
}

func TestCreateAndRemoveLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		return
	}
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	var (
		unshareFlags int
		unshareTID   int
		mountTID     int
		mountSrc     string
		mountSrcOK   bool
		mountDst     string
		mountFlags   uintptr
		unmountFlags int
	)
	mgr.unshare = func(flags int) error {
		unshareFlags = flags
		unshareTID = unix.Gettid()
		return nil
	}
	mgr.mount = func(src, dst, _ string, flags uintptr, _ string) error {
		mountTID = unix.Gettid()
		mountSrc = src
		if _, err := os.Stat(src); err == nil {
			mountSrcOK = true
		}
		mountDst = dst
		mountFlags = flags
		return nil
	}
	mgr.unmount = func(_ string, flags int) error {
		unmountFlags = flags
		return nil
	}

	path, err := mgr.Create(context.Background(), "unit-1")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantPath := filepath.Join(dir, "unit-1")
	wantSrc := fmt.Sprintf("/proc/%d/task/%d/ns/net", os.Getpid(), mountTID)
	if path != wantPath || unshareFlags != unix.CLONE_NEWNET || unshareTID != mountTID || mountTID == os.Getpid() || mountSrc != wantSrc || !mountSrcOK || mountDst != wantPath || mountFlags != unix.MS_BIND {
		t.Fatalf("unexpected create syscalls: path=%q unshare=%d utid=%d mtid=%d pid=%d src=%q srcOK=%v dst=%q flags=%d",
			path, unshareFlags, unshareTID, mountTID, os.Getpid(), mountSrc, mountSrcOK, mountDst, mountFlags)
	}
	if strings.Contains(mountSrc, "/proc/self/") {
		t.Fatalf("mountSrc must never use /proc/self/ns/net: %q", mountSrc)
	}

	if err := mgr.Remove(context.Background(), "unit-1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if unmountFlags != unix.MNT_DETACH|unix.UMOUNT_NOFOLLOW {
		t.Fatalf("unmount flags = %d, want MNT_DETACH|UMOUNT_NOFOLLOW", unmountFlags)
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatalf("netns file still exists after Remove: %v", err)
	}

	// Symlink at target path must be rejected without calling unmount.
	targetFile := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(targetFile, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Symlink(targetFile, filepath.Join(dir, "sym-ns")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	unmountCalled := false
	mgr.unmount = func(_ string, _ int) error {
		unmountCalled = true
		return nil
	}
	if err := mgr.Remove(context.Background(), "sym-ns"); err == nil {
		t.Fatal("expected Remove to reject symlink")
	}
	if unmountCalled {
		t.Fatal("Remove called unmount on a symlink")
	}

	// Second Remove is idempotent on missing path.
	if err := mgr.Remove(context.Background(), "unit-1"); err != nil {
		t.Fatalf("second Remove failed: %v", err)
	}

	// EINVAL from unmount on an unmounted plain file is ignored idempotently.
	plain := filepath.Join(dir, "plain-ns")
	if err := os.WriteFile(plain, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	mgr.unmount = func(_ string, _ int) error { return unix.EINVAL }
	if err := mgr.Remove(context.Background(), "plain-ns"); err != nil {
		t.Fatalf("Remove(EINVAL) failed: %v", err)
	}
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Fatalf("plain file still exists: %v", err)
	}

	// Failed mount must clean up the created mount-point file.
	mgr.mount = func(_, _, _ string, _ uintptr, _ string) error { return errors.New("mount boom") }
	if _, err := mgr.Create(context.Background(), "fail-ns"); err == nil {
		t.Fatal("expected mount failure")
	}
	if _, err := os.Stat(filepath.Join(dir, "fail-ns")); !os.IsNotExist(err) {
		t.Fatalf("failed Create leaked mount-point file: %v", err)
	}
}

func TestCreateAlwaysUsesNonLeaderWorkerThread(t *testing.T) {
	if runtime.GOOS != "linux" {
		return
	}
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	pid := os.Getpid()
	mgr.unshare = func(_ int) error { return nil }
	mgr.mount = func(src, _, _ string, _ uintptr, _ string) error {
		tid := unix.Gettid()
		if tid == pid {
			t.Errorf("mount ran on process leader thread m0 (pid=%d, tid=%d)", pid, tid)
		}
		want := fmt.Sprintf("/proc/%d/task/%d/ns/net", pid, tid)
		if src != want {
			t.Errorf("mount src = %q, want %q", src, want)
		}
		if _, err := os.Stat(src); err != nil {
			t.Errorf("stat %s failed: %v", src, err)
		}
		return nil
	}
	mgr.unmount = func(_ string, _ int) error { return nil }

	for i := range 20 {
		name := fmt.Sprintf("worker-%d", i)
		if _, err := mgr.Create(context.Background(), name); err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
		if err := mgr.Remove(context.Background(), name); err != nil {
			t.Fatalf("Remove(%s): %v", name, err)
		}
	}
}
