package sim

import (
	"bytes"
	"io/fs"
	"path"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

type memNode struct {
	isDir   bool
	data    []byte
	mode    fs.FileMode
	modTime time.Time
}

type memFileInfo struct {
	name    string
	size    int64
	mode    fs.FileMode
	modTime time.Time
	isDir   bool
}

func (fi memFileInfo) Name() string       { return fi.name }
func (fi memFileInfo) Size() int64        { return fi.size }
func (fi memFileInfo) Mode() fs.FileMode  { return fi.mode }
func (fi memFileInfo) ModTime() time.Time { return fi.modTime }
func (fi memFileInfo) IsDir() bool        { return fi.isDir }
func (fi memFileInfo) Sys() any           { return nil }

// MemDisk is an in-memory Disk implementation with deterministic sorted entries and fault injection.
type MemDisk struct {
	mu          sync.Mutex
	clock       Clock
	nodes       map[string]*memNode
	totalBytes  int64
	quotaBytes  int64
	readFaults  map[string]error
	writeFaults map[string]error
	statFaults  map[string]error
}

// NewMemDisk constructs an empty MemDisk rooted at "/" and ".".
func NewMemDisk() *MemDisk {
	return NewMemDiskWithClock(nil)
}

// NewMemDiskWithClock constructs an empty MemDisk stamping ModTime from clock (or DefaultEpoch if nil).
func NewMemDiskWithClock(clock Clock) *MemDisk {
	ts := DefaultEpoch
	if clock != nil {
		ts = clock.Now()
	}
	return &MemDisk{
		clock: clock,
		nodes: map[string]*memNode{
			"/": {isDir: true, mode: fs.ModeDir | 0o755, modTime: ts},
			".": {isDir: true, mode: fs.ModeDir | 0o755, modTime: ts},
		},
		readFaults:  make(map[string]error),
		writeFaults: make(map[string]error),
		statFaults:  make(map[string]error),
	}
}

func cleanMemPath(name string) string {
	if name == "" {
		return ""
	}
	return path.Clean(strings.ReplaceAll(name, "\\", "/"))
}

func (d *MemDisk) nowLocked() time.Time {
	if d.clock != nil {
		return d.clock.Now()
	}
	return DefaultEpoch
}

func (d *MemDisk) faultForLocked(faults map[string]error, clean string) error {
	if err := faults[clean]; err != nil {
		return err
	}
	return faults[""]
}

// InjectReadError configures an error returned by ReadFile for name (or all paths when name == "").
// Pass nil to clear the injected error.
func (d *MemDisk) InjectReadError(name string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := ""
	if name != "" {
		key = cleanMemPath(name)
	}
	if err == nil {
		delete(d.readFaults, key)
	} else {
		d.readFaults[key] = err
	}
}

// InjectWriteError configures an error returned by WriteFile/MkdirAll/Remove for name (or all paths when name == "").
func (d *MemDisk) InjectWriteError(name string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := ""
	if name != "" {
		key = cleanMemPath(name)
	}
	if err == nil {
		delete(d.writeFaults, key)
	} else {
		d.writeFaults[key] = err
	}
}

// InjectStatError configures an error returned by Stat for name (or all paths when name == "").
func (d *MemDisk) InjectStatError(name string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := ""
	if name != "" {
		key = cleanMemPath(name)
	}
	if err == nil {
		delete(d.statFaults, key)
	} else {
		d.statFaults[key] = err
	}
}

// SetQuotaBytes caps total stored file bytes; writes exceeding limit fail with syscall.ENOSPC.
// A limit <= 0 disables quota enforcement.
func (d *MemDisk) SetQuotaBytes(limit int64) {
	d.mu.Lock()
	d.quotaBytes = limit
	d.mu.Unlock()
}

func (d *MemDisk) ReadFile(name string) ([]byte, error) {
	clean := cleanMemPath(name)
	if clean == "" {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.faultForLocked(d.readFaults, clean); err != nil {
		return nil, &fs.PathError{Op: "read", Path: name, Err: err}
	}
	n, ok := d.nodes[clean]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if n.isDir {
		return nil, &fs.PathError{Op: "read", Path: name, Err: syscall.EISDIR}
	}
	return bytes.Clone(n.data), nil
}

func (d *MemDisk) WriteFile(name string, data []byte, perm fs.FileMode) error {
	clean := cleanMemPath(name)
	if clean == "" || clean == "/" || clean == "." {
		return &fs.PathError{Op: "open", Path: name, Err: syscall.EISDIR}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.faultForLocked(d.writeFaults, clean); err != nil {
		return &fs.PathError{Op: "write", Path: name, Err: err}
	}
	parent := path.Dir(clean)
	pNode, ok := d.nodes[parent]
	if !ok {
		return &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	if !pNode.isDir {
		return &fs.PathError{Op: "open", Path: name, Err: syscall.ENOTDIR}
	}
	var oldSize int64
	if existing, exists := d.nodes[clean]; exists {
		if existing.isDir {
			return &fs.PathError{Op: "open", Path: name, Err: syscall.EISDIR}
		}
		oldSize = int64(len(existing.data))
	}
	newTotal := d.totalBytes - oldSize + int64(len(data))
	if d.quotaBytes > 0 && newTotal > d.quotaBytes {
		return &fs.PathError{Op: "write", Path: name, Err: syscall.ENOSPC}
	}
	d.nodes[clean] = &memNode{
		isDir:   false,
		data:    bytes.Clone(data),
		mode:    perm & fs.ModePerm,
		modTime: d.nowLocked(),
	}
	d.totalBytes = newTotal
	return nil
}

func (d *MemDisk) MkdirAll(p string, perm fs.FileMode) error {
	clean := cleanMemPath(p)
	if clean == "" {
		return &fs.PathError{Op: "mkdir", Path: p, Err: fs.ErrInvalid}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.faultForLocked(d.writeFaults, clean); err != nil {
		return &fs.PathError{Op: "mkdir", Path: p, Err: err}
	}
	if clean == "/" || clean == "." {
		return nil
	}
	isAbs := strings.HasPrefix(clean, "/")
	parts := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	cur := ""
	if isAbs {
		cur = "/"
	}
	ts := d.nowLocked()
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		switch cur {
		case "":
			cur = part
		case "/":
			cur = "/" + part
		default:
			cur = cur + "/" + part
		}
		if existing, ok := d.nodes[cur]; ok {
			if !existing.isDir {
				return &fs.PathError{Op: "mkdir", Path: p, Err: syscall.ENOTDIR}
			}
			continue
		}
		d.nodes[cur] = &memNode{
			isDir:   true,
			mode:    fs.ModeDir | (perm & fs.ModePerm),
			modTime: ts,
		}
	}
	return nil
}

func (d *MemDisk) Remove(name string) error {
	clean := cleanMemPath(name)
	if clean == "" || clean == "/" || clean == "." {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrInvalid}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.faultForLocked(d.writeFaults, clean); err != nil {
		return &fs.PathError{Op: "remove", Path: name, Err: err}
	}
	n, ok := d.nodes[clean]
	if !ok {
		return &fs.PathError{Op: "remove", Path: name, Err: fs.ErrNotExist}
	}
	if n.isDir {
		prefix := clean + "/"
		for k := range d.nodes {
			if strings.HasPrefix(k, prefix) {
				return &fs.PathError{Op: "remove", Path: name, Err: syscall.ENOTEMPTY}
			}
		}
	} else {
		d.totalBytes -= int64(len(n.data))
	}
	delete(d.nodes, clean)
	return nil
}

func (d *MemDisk) Stat(name string) (fs.FileInfo, error) {
	clean := cleanMemPath(name)
	if clean == "" {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.faultForLocked(d.statFaults, clean); err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	n, ok := d.nodes[clean]
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
	}
	return memFileInfo{
		name:    path.Base(clean),
		size:    int64(len(n.data)),
		mode:    n.mode,
		modTime: n.modTime,
		isDir:   n.isDir,
	}, nil
}

// Entries returns all stored file and directory paths in deterministic sorted order.
func (d *MemDisk) Entries() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.nodes))
	for k := range d.nodes {
		if k == "/" || k == "." {
			continue
		}
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
