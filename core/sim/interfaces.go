package sim

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Clock abstracts wall/monotonic time queries and timer channels.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// Timer represents a stoppable single-shot timer backed by a Clock.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// TimerClock is an optional extension implemented by Clocks that support stoppable timers.
type TimerClock interface {
	Clock
	NewTimer(d time.Duration) Timer
}

// AfterFuncClock is an optional extension implemented by simulated Clocks that run timer callbacks synchronously.
type AfterFuncClock interface {
	Clock
	AfterFunc(d time.Duration, f func()) func() bool
}

// WithTimeout derives a Context that cancels with context.DeadlineExceeded after d elapses on clock.
// If clock is nil, RealClock() is used. If d <= 0, an already-expired context is returned immediately.
func WithTimeout(parent context.Context, clock Clock, d time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if clock == nil {
		clock = RealClock()
	}
	if _, isReal := clock.(realClock); isReal {
		return context.WithTimeout(parent, d)
	}
	if err := parent.Err(); err != nil {
		tc := &timeoutCtx{
			parent: parent,
			done:   closedCh,
			err:    err,
			closed: true,
		}
		if dl, ok := parent.Deadline(); ok {
			tc.deadline = dl
			tc.hasDeadline = true
		}
		return tc, func() {}
	}
	now := clock.Now()
	deadline := now.Add(d)
	if parentDl, ok := parent.Deadline(); ok && parentDl.Before(deadline) {
		deadline = parentDl
		d = parentDl.Sub(now)
	}
	if d <= 0 {
		tc := &timeoutCtx{
			parent:      parent,
			deadline:    deadline,
			hasDeadline: true,
			done:        closedCh,
			err:         context.DeadlineExceeded,
			closed:      true,
		}
		return tc, func() {}
	}
	tc := &timeoutCtx{
		parent:      parent,
		deadline:    deadline,
		hasDeadline: true,
		done:        make(chan struct{}),
	}
	if afClock, ok := clock.(AfterFuncClock); ok {
		stopTimer := afClock.AfterFunc(d, func() {
			tc.finish(context.DeadlineExceeded)
		})
		stopParent := context.AfterFunc(parent, func() {
			stopTimer()
			tc.finish(parent.Err())
		})
		return tc, func() {
			stopParent()
			stopTimer()
			tc.finish(context.Canceled)
		}
	}
	cancelCh := make(chan struct{})
	if timerClock, ok := clock.(TimerClock); ok {
		tm := timerClock.NewTimer(d)
		go func() {
			select {
			case <-tm.C():
				tc.finish(context.DeadlineExceeded)
			case <-parent.Done():
				tm.Stop()
				tc.finish(parent.Err())
			case <-cancelCh:
				tm.Stop()
				tc.finish(context.Canceled)
			}
		}()
		return tc, func() {
			tc.finish(context.Canceled)
			closeOnce(cancelCh)
			tm.Stop()
		}
	}
	ch := clock.After(d)
	go func() {
		select {
		case <-ch:
			tc.finish(context.DeadlineExceeded)
		case <-parent.Done():
			tc.finish(parent.Err())
		case <-cancelCh:
			tc.finish(context.Canceled)
		}
	}()
	return tc, func() {
		tc.finish(context.Canceled)
		closeOnce(cancelCh)
	}
}

var closedCh = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

type timeoutCtx struct {
	parent      context.Context
	deadline    time.Time
	hasDeadline bool
	done        chan struct{}

	mu     sync.Mutex
	err    error
	closed bool
}

func (c *timeoutCtx) Deadline() (time.Time, bool) { return c.deadline, c.hasDeadline }
func (c *timeoutCtx) Done() <-chan struct{}       { return c.done }
func (c *timeoutCtx) Value(key any) any           { return c.parent.Value(key) }

func (c *timeoutCtx) Err() error {
	c.mu.Lock()
	err := c.err
	c.mu.Unlock()
	return err
}

func (c *timeoutCtx) finish(err error) {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.err = err
		close(c.done)
	}
	c.mu.Unlock()
}

func closeOnce(ch chan struct{}) {
	defer func() { _ = recover() }()
	close(ch)
}

type realTimer struct {
	t *time.Timer
}

func (rt realTimer) C() <-chan time.Time { return rt.t.C }
func (rt realTimer) Stop() bool          { return rt.t.Stop() }

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) NewTimer(d time.Duration) Timer         { return realTimer{t: time.NewTimer(d)} }

// RealClock returns a Clock backed by the Go standard library time package.
func RealClock() Clock { return realClock{} }

// Net abstracts stream connection dialing and listening.
type Net interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	Listen(network, address string) (net.Listener, error)
}

type realNet struct {
	dialer net.Dialer
	listen net.ListenConfig
}

func (n *realNet) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return n.dialer.DialContext(ctx, network, address)
}

func (n *realNet) Listen(network, address string) (net.Listener, error) {
	return n.listen.Listen(context.Background(), network, address)
}

// RealNet returns a Net backed by the OS TCP/Unix network stack.
func RealNet() Net { return &realNet{} }

// Disk abstracts file-system operations so components can run against in-memory storage in tests and simulations.
type Disk interface {
	ReadFile(name string) ([]byte, error)
	WriteFile(name string, data []byte, perm fs.FileMode) error
	MkdirAll(path string, perm fs.FileMode) error
	Remove(name string) error
	Stat(name string) (fs.FileInfo, error)
}

type osDisk struct{}

func (osDisk) ReadFile(name string) ([]byte, error) { return os.ReadFile(name) }
func (osDisk) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return os.WriteFile(name, data, perm)
}
func (osDisk) MkdirAll(path string, perm fs.FileMode) error { return os.MkdirAll(path, perm) }
func (osDisk) Remove(name string) error                     { return os.Remove(name) }
func (osDisk) Stat(name string) (fs.FileInfo, error)        { return os.Stat(name) }

// OSDisk returns an unrooted Disk backed by the host operating system.
func OSDisk() Disk { return osDisk{} }

// RootedDisk is a Disk confined to one directory; Close releases its os.Root.
type RootedDisk struct {
	root *os.Root
}

// NewRootedDisk returns a Disk confined to rootDir by the kernel-enforced os.Root.
func NewRootedDisk(rootDir string) (*RootedDisk, error) {
	if rootDir == "" || !filepath.IsAbs(rootDir) {
		return nil, fmt.Errorf("sim: rootDir %q must be a non-empty absolute path", rootDir)
	}
	resolved, err := filepath.EvalSymlinks(rootDir)
	if err != nil {
		return nil, fmt.Errorf("sim: resolve rootDir %q: %w", rootDir, err)
	}
	clean := filepath.Clean(resolved)
	if clean == "/" {
		return nil, errors.New("sim: rootDir must not be /")
	}
	root, err := os.OpenRoot(clean)
	if err != nil {
		return nil, fmt.Errorf("sim: open root %q: %w", clean, err)
	}
	return &RootedDisk{root: root}, nil
}

// Close releases the underlying os.Root.
func (d *RootedDisk) Close() error { return d.root.Close() }

func (d *RootedDisk) ReadFile(name string) ([]byte, error) { return d.root.ReadFile(name) }

func (d *RootedDisk) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return d.root.WriteFile(name, data, perm)
}

func (d *RootedDisk) MkdirAll(path string, perm fs.FileMode) error {
	return d.root.MkdirAll(path, perm)
}

func (d *RootedDisk) Remove(name string) error {
	if filepath.Clean(name) == "." {
		return errors.New("sim: refusing to remove root directory")
	}
	return d.root.Remove(name)
}

func (d *RootedDisk) Stat(name string) (fs.FileInfo, error) { return d.root.Stat(name) }

// Rand abstracts random number and byte-stream generation with fixed-width operations.
type Rand interface {
	Uint64() uint64
	Int64N(n int64) int64
	Fill(p []byte)
}

type cryptoRand struct{}

func (cryptoRand) Uint64() uint64 {
	var b [8]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic("sim: crypto/rand failure: " + err.Error())
	}
	return binary.LittleEndian.Uint64(b[:])
}

func (c cryptoRand) Int64N(n int64) int64 {
	if n <= 0 {
		panic("sim: Int64N argument must be > 0")
	}
	un := uint64(n)
	max := ^uint64(0) - (^uint64(0) % un)
	for {
		v := c.Uint64()
		if v < max {
			return int64(v % un)
		}
	}
}

func (cryptoRand) Fill(p []byte) {
	if _, err := io.ReadFull(rand.Reader, p); err != nil {
		panic("sim: crypto/rand failure: " + err.Error())
	}
}

// CryptoRand returns a cryptographically secure Rand backed by crypto/rand.
func CryptoRand() Rand { return cryptoRand{} }

type randReader struct {
	r Rand
}

func (rr randReader) Read(p []byte) (int, error) {
	rr.r.Fill(p)
	return len(p), nil
}

// RandReader wraps r as an io.Reader whose Read always fills len(p) bytes via r.Fill.
func RandReader(r Rand) io.Reader {
	if r == nil {
		r = CryptoRand()
	}
	return randReader{r: r}
}
