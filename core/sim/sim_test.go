package sim_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/Nayutar/core/sim"
)

func TestDetRandDeterminismAndFillAlignment(t *testing.T) {
	r1 := sim.NewDetRand(42)
	r2 := sim.NewDetRand(42)
	rOther := sim.NewDetRandStream(42, 1)

	for i := range 64 {
		u1, u2 := r1.Uint64(), r2.Uint64()
		if u1 != u2 {
			t.Fatalf("Uint64[%d] mismatch: %x vs %x", i, u1, u2)
		}
		if i == 0 && u1 == rOther.Uint64() {
			t.Fatalf("different stream produced identical first word %x", u1)
		}
	}

	// Verify Fill strictly unpacks little-endian Uint64 words without perturbing stream state.
	rWords := sim.NewDetRand(99)
	rBytes := sim.NewDetRand(99)
	w0 := rWords.Uint64()
	w1 := rWords.Uint64()
	w2 := rWords.Uint64()

	buf := make([]byte, 13)
	rBytes.Fill(buf)
	var want13 [16]byte
	binary.LittleEndian.PutUint64(want13[0:8], w0)
	binary.LittleEndian.PutUint64(want13[8:16], w1)
	if !bytes.Equal(buf, want13[:13]) {
		t.Fatalf("Fill(13) = %x, want %x", buf, want13[:13])
	}
	if got := rBytes.Uint64(); got != w2 {
		t.Fatalf("Uint64 after Fill(13) = %x, want %x", got, w2)
	}

	// Verify Int64N range and determinism.
	ri1 := sim.NewDetRand(777)
	ri2 := sim.NewDetRand(777)
	for range 200 {
		a := ri1.Int64N(17)
		b := ri2.Int64N(17)
		if a != b || a < 0 || a >= 17 {
			t.Fatalf("Int64N(17) = (%d, %d)", a, b)
		}
	}
	if got := ri1.Int64N(1); got != 0 {
		t.Fatalf("Int64N(1) = %d, want 0", got)
	}

	// Verify RandReader matches Fill.
	rr1 := sim.RandReader(sim.NewDetRand(1234))
	rDirect := sim.NewDetRand(1234)
	p1 := make([]byte, 23)
	p2 := make([]byte, 23)
	if n, err := io.ReadFull(rr1, p1); err != nil || n != 23 {
		t.Fatalf("RandReader ReadFull = (%d, %v)", n, err)
	}
	rDirect.Fill(p2)
	if !bytes.Equal(p1, p2) {
		t.Fatalf("RandReader mismatch: %x vs %x", p1, p2)
	}

	// Verify CryptoRand and RandReader(nil).
	cr := sim.CryptoRand()
	if got := cr.Int64N(100); got < 0 || got >= 100 {
		t.Fatalf("CryptoRand.Int64N(100) = %d", got)
	}
	cb := make([]byte, 16)
	if n, err := io.ReadFull(sim.RandReader(nil), cb); err != nil || n != 16 {
		t.Fatalf("RandReader(nil) = (%d, %v)", n, err)
	}

	// Verify panic on non-positive Int64N.
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic on Int64N(0)")
			}
		}()
		sim.NewDetRand(1).Int64N(0)
	}()
}

func TestFakeClockDeterministicOrderAndStop(t *testing.T) {
	clk := sim.NewFakeClock(time.Time{})
	if !clk.Now().Equal(sim.DefaultEpoch) {
		t.Fatalf("initial Now = %v, want %v", clk.Now(), sim.DefaultEpoch)
	}

	tmLate := clk.NewTimer(50 * time.Millisecond)
	tm1 := clk.NewTimer(20 * time.Millisecond)
	tmStop := clk.NewTimer(20 * time.Millisecond)
	tm2 := clk.NewTimer(20 * time.Millisecond)

	if !tmStop.Stop() {
		t.Fatal("first Stop() = false, want true")
	}
	if tmStop.Stop() {
		t.Fatal("second Stop() = true, want false")
	}
	if got := clk.PendingTimers(); got != 3 {
		t.Fatalf("PendingTimers = %d, want 3", got)
	}

	clk.Advance(20 * time.Millisecond)
	select {
	case ts := <-tm1.C():
		if !ts.Equal(sim.DefaultEpoch.Add(20 * time.Millisecond)) {
			t.Fatalf("tm1 fired at %v", ts)
		}
	default:
		t.Fatal("tm1 did not fire")
	}
	select {
	case ts := <-tm2.C():
		if !ts.Equal(sim.DefaultEpoch.Add(20 * time.Millisecond)) {
			t.Fatalf("tm2 fired at %v", ts)
		}
	default:
		t.Fatal("tm2 did not fire")
	}
	select {
	case ts := <-tmStop.C():
		t.Fatalf("stopped timer fired at %v", ts)
	default:
	}
	select {
	case ts := <-tmLate.C():
		t.Fatalf("late timer fired early at %v", ts)
	default:
	}

	clk.Advance(30 * time.Millisecond)
	select {
	case ts := <-tmLate.C():
		if !ts.Equal(sim.DefaultEpoch.Add(50 * time.Millisecond)) {
			t.Fatalf("tmLate fired at %v", ts)
		}
	default:
		t.Fatal("tmLate did not fire")
	}
	if got := clk.PendingTimers(); got != 0 {
		t.Fatalf("PendingTimers after advance = %d, want 0", got)
	}
}

type plainClock struct {
	inner sim.Clock
}

func (p plainClock) Now() time.Time                         { return p.inner.Now() }
func (p plainClock) After(d time.Duration) <-chan time.Time { return p.inner.After(d) }

func TestWithTimeoutTimerCleanupAndSynctest(t *testing.T) {
	t.Run("fake_clock_expire_and_cancel", func(t *testing.T) {
		clk := sim.NewFakeClock(time.Time{})

		// Immediate expiry on d <= 0.
		immCtx, immCancel := sim.WithTimeout(context.Background(), clk, 0)
		defer immCancel()
		if !errors.Is(immCtx.Err(), context.DeadlineExceeded) {
			t.Fatalf("d=0 Err = %v, want DeadlineExceeded", immCtx.Err())
		}

		// Cancel cleans up timer before deadline.
		ctx1, cancel1 := sim.WithTimeout(context.Background(), clk, 100*time.Millisecond)
		if clk.PendingTimers() != 1 {
			t.Fatalf("PendingTimers = %d, want 1", clk.PendingTimers())
		}
		cancel1()
		if !errors.Is(ctx1.Err(), context.Canceled) {
			t.Fatalf("canceled Err = %v, want Canceled", ctx1.Err())
		}
		if clk.PendingTimers() != 0 {
			t.Fatalf("PendingTimers after cancel = %d, want 0", clk.PendingTimers())
		}

		// Deadline fires with context.DeadlineExceeded.
		ctx2, cancel2 := sim.WithTimeout(context.Background(), clk, 50*time.Millisecond)
		defer cancel2()
		dl, ok := ctx2.Deadline()
		if !ok || !dl.Equal(sim.DefaultEpoch.Add(50*time.Millisecond)) {
			t.Fatalf("Deadline = (%v, %v)", dl, ok)
		}
		clk.Advance(50 * time.Millisecond)
		<-ctx2.Done()
		if !errors.Is(ctx2.Err(), context.DeadlineExceeded) {
			t.Fatalf("expired Err = %v, want DeadlineExceeded", ctx2.Err())
		}

		// Plain Clock (non-TimerClock) also returns DeadlineExceeded.
		ctx3, cancel3 := sim.WithTimeout(context.Background(), plainClock{inner: clk}, 25*time.Millisecond)
		defer cancel3()
		clk.Advance(25 * time.Millisecond)
		<-ctx3.Done()
		if !errors.Is(ctx3.Err(), context.DeadlineExceeded) {
			t.Fatalf("plain clock expired Err = %v, want DeadlineExceeded", ctx3.Err())
		}
	})

	t.Run("synctest_real_clock", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := sim.WithTimeout(context.Background(), sim.RealClock(), 5*time.Second)
			defer cancel()
			start := time.Now()
			<-ctx.Done()
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("synctest WithTimeout Err = %v, want DeadlineExceeded", ctx.Err())
			}
			if got := time.Since(start); got != 5*time.Second {
				t.Fatalf("synctest elapsed = %v, want 5s", got)
			}
		})
	})
}

func TestMemNetSynctestAndFaultInjection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mn := sim.NewMemNet()
		defer func() { _ = mn.Close() }()

		ln1, err := mn.Listen("tcp", ":0")
		if err != nil {
			t.Fatalf("Listen(:0): %v", err)
		}
		ln2, err := mn.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("Listen(127.0.0.1:0): %v", err)
		}
		if ln1.Addr().String() == ln2.Addr().String() {
			t.Fatalf("ephemeral ports collided: %s", ln1.Addr().String())
		}
		if !strings.HasPrefix(ln1.Addr().String(), "127.0.0.1:") {
			t.Fatalf("unexpected ephemeral addr %s", ln1.Addr().String())
		}
		if _, err := mn.Listen("tcp", ln1.Addr().String()); !errors.Is(err, sim.ErrAddrInUse) {
			t.Fatalf("duplicate Listen err = %v, want ErrAddrInUse", err)
		}

		// Echo server on ln1.
		serverDone := make(chan struct{})
		go func() {
			defer close(serverDone)
			for {
				c, err := ln1.Accept()
				if err != nil {
					return
				}
				go func(conn net.Conn) {
					defer func() { _ = conn.Close() }()
					_, _ = io.Copy(conn, conn)
				}(c)
			}
		}()

		// Basic bidirectional echo over net.Pipe.
		epA := mn.Endpoint("node-a")
		epB := mn.Endpoint("node-b")
		conn, err := epA.DialContext(context.Background(), "tcp", ln1.Addr().String())
		if err != nil {
			t.Fatalf("DialContext: %v", err)
		}
		if _, err := conn.Write([]byte("ping")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		got := make([]byte, 4)
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatalf("ReadFull: %v", err)
		}
		if string(got) != "ping" {
			t.Fatalf("echo = %q, want ping", got)
		}

		// Partition closes active conn and blocks new dials.
		mn.Partition(ln1.Addr().String())
		if _, err := conn.Write([]byte("after-partition")); err == nil {
			t.Fatal("expected Write error after Partition")
		}
		if _, err := epA.DialContext(context.Background(), "tcp", ln1.Addr().String()); !errors.Is(err, sim.ErrPartitioned) {
			t.Fatalf("dial during partition = %v, want ErrPartitioned", err)
		}
		mn.Heal(ln1.Addr().String())

		// Connection refused fault.
		mn.SetRefused(ln1.Addr().String(), true)
		if _, err := epA.DialContext(context.Background(), "tcp", ln1.Addr().String()); !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatalf("dial during refuse = %v, want ECONNREFUSED", err)
		}
		mn.SetRefused(ln1.Addr().String(), false)

		// Directed link cut: node-a -> ln1 cut while node-b -> ln1 succeeds.
		mn.SetLinkCut("node-a", ln1.Addr().String(), true)
		if _, err := epA.DialContext(context.Background(), "tcp", ln1.Addr().String()); !errors.Is(err, sim.ErrPartitioned) {
			t.Fatalf("cut node-a dial = %v, want ErrPartitioned", err)
		}
		connB, err := epB.DialContext(context.Background(), "tcp", ln1.Addr().String())
		if err != nil {
			t.Fatalf("uncut node-b dial failed: %v", err)
		}
		_ = connB.Close()
		mn.SetLinkCut("node-a", ln1.Addr().String(), false)

		// One-way drop: client->server writes are dropped silently while server->client delivers;
		// new dials from dropped endpoint durably block until context deadline inside synctest.
		mn.SetOneWayDrop("node-a", ln2.Addr().String(), true)
		dialCtx, dialCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer dialCancel()
		if _, err := epA.DialContext(dialCtx, "tcp", ln2.Addr().String()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("dial during one-way drop = %v, want DeadlineExceeded", err)
		}
		mn.SetOneWayDrop("node-a", ln2.Addr().String(), false)

		// Verify one-way drop on an active connection.
		srvConnCh := make(chan net.Conn, 1)
		go func() {
			c, err := ln2.Accept()
			if err == nil {
				srvConnCh <- c
			}
		}()
		cliConn, err := epA.DialContext(context.Background(), "tcp", ln2.Addr().String())
		if err != nil {
			t.Fatalf("dial ln2: %v", err)
		}
		srvConn := <-srvConnCh
		mn.SetOneWayDrop("node-a", ln2.Addr().String(), true)
		if n, err := cliConn.Write([]byte("dropped")); err != nil || n != 7 {
			t.Fatalf("one-way dropped Write = (%d, %v), want (7, nil)", n, err)
		}
		// Reverse direction still delivers!
		go func() { _, _ = srvConn.Write([]byte("ok")) }()
		rev := make([]byte, 2)
		if _, err := io.ReadFull(cliConn, rev); err != nil || string(rev) != "ok" {
			t.Fatalf("reverse direction ReadFull = (%q, %v), want ok", rev, err)
		}
		_ = cliConn.Close()
		_ = srvConn.Close()

		_ = ln1.Close()
		_ = ln2.Close()
		<-serverDone
	})
}

func TestMemDiskOperationsAndFaultInjection(t *testing.T) {
	clk := sim.NewFakeClock(time.Time{})
	d := sim.NewMemDiskWithClock(clk)

	if err := d.MkdirAll("/etc/nayutar", 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := d.WriteFile("/etc/nayutar/b.lkal", []byte("two"), 0o600); err != nil {
		t.Fatalf("WriteFile b: %v", err)
	}
	clk.Advance(time.Second)
	if err := d.WriteFile("/etc/nayutar/a.lkal", []byte("one"), 0o644); err != nil {
		t.Fatalf("WriteFile a: %v", err)
	}

	gotEntries := d.Entries()
	wantEntries := []string{"/etc", "/etc/nayutar", "/etc/nayutar/a.lkal", "/etc/nayutar/b.lkal"}
	if !slices.Equal(gotEntries, wantEntries) {
		t.Fatalf("Entries = %v, want %v", gotEntries, wantEntries)
	}

	data, err := d.ReadFile("/etc/nayutar/a.lkal")
	if err != nil || string(data) != "one" {
		t.Fatalf("ReadFile = (%q, %v), want one", data, err)
	}
	data[0] = 'X'
	again, _ := d.ReadFile("/etc/nayutar/a.lkal")
	if string(again) != "one" {
		t.Fatalf("caller mutation leaked into MemDisk: %q", again)
	}

	fi, err := d.Stat("/etc/nayutar/a.lkal")
	if err != nil || fi.Name() != "a.lkal" || fi.Size() != 3 || fi.IsDir() || !fi.ModTime().Equal(sim.DefaultEpoch.Add(time.Second)) {
		t.Fatalf("Stat = (%+v, %v)", fi, err)
	}

	// Fault injections: EIO and ENOSPC.
	d.InjectReadError("/etc/nayutar/a.lkal", syscall.EIO)
	if _, err := d.ReadFile("/etc/nayutar/a.lkal"); !errors.Is(err, syscall.EIO) {
		t.Fatalf("injected read error = %v, want EIO", err)
	}
	d.InjectReadError("/etc/nayutar/a.lkal", nil)

	d.InjectStatError("/etc/nayutar/a.lkal", syscall.EIO)
	if _, err := d.Stat("/etc/nayutar/a.lkal"); !errors.Is(err, syscall.EIO) {
		t.Fatalf("injected stat error = %v, want EIO", err)
	}
	d.InjectStatError("/etc/nayutar/a.lkal", nil)

	d.InjectWriteError("", syscall.ENOSPC)
	if err := d.WriteFile("/etc/nayutar/c.lkal", []byte("x"), 0o600); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("injected write error = %v, want ENOSPC", err)
	}
	d.InjectWriteError("", nil)

	// Quota ENOSPC enforcement.
	d.SetQuotaBytes(8)
	if err := d.WriteFile("/etc/nayutar/big.lkal", []byte("12345"), 0o600); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("quota exceeded error = %v, want ENOSPC", err)
	}
	d.SetQuotaBytes(0)

	// Non-empty directory removal fails, removing files first succeeds.
	if err := d.Remove("/etc/nayutar"); !errors.Is(err, syscall.ENOTEMPTY) {
		t.Fatalf("Remove non-empty dir = %v, want ENOTEMPTY", err)
	}
	if err := d.Remove("/etc/nayutar/a.lkal"); err != nil {
		t.Fatalf("Remove a: %v", err)
	}
	if _, err := d.ReadFile("/etc/nayutar/a.lkal"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile after Remove = %v, want ErrNotExist", err)
	}
}

func TestRootedDiskRejectsSymlinkAndPrefixBoundaryEscapes(t *testing.T) {
	base := t.TempDir()
	sandbox := filepath.Join(base, "sandbox")
	evil := filepath.Join(base, "sandbox-evil")
	if err := os.MkdirAll(sandbox, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(evil, 0o755); err != nil {
		t.Fatal(err)
	}
	evilSecret := filepath.Join(evil, "secret.txt")
	if err := os.WriteFile(evilSecret, []byte("top-secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := sim.NewRootedDisk("relative/path"); err == nil {
		t.Fatal("expected error for relative rootDir")
	}
	if _, err := sim.NewRootedDisk("/"); err == nil {
		t.Fatal("expected error for / rootDir")
	}

	disk, err := sim.NewRootedDisk(sandbox)
	if err != nil {
		t.Fatalf("NewRootedDisk: %v", err)
	}
	t.Cleanup(func() { _ = disk.Close() })

	// Legitimate read/write inside sandbox.
	if err := disk.MkdirAll("sub", 0o755); err != nil {
		t.Fatalf("MkdirAll inside sandbox: %v", err)
	}
	if err := disk.WriteFile("sub/ok.txt", []byte("ok"), 0o600); err != nil {
		t.Fatalf("WriteFile inside sandbox: %v", err)
	}
	got, err := disk.ReadFile("sub/ok.txt")
	if err != nil || string(got) != "ok" {
		t.Fatalf("ReadFile inside sandbox = (%q, %v)", got, err)
	}

	// Prefix-boundary sibling traversal (/sandbox-evil vs /sandbox).
	if _, err := disk.ReadFile("../sandbox-evil/secret.txt"); err == nil {
		t.Fatal("prefix-boundary relative escape succeeded")
	}
	if _, err := disk.ReadFile(evilSecret); err == nil {
		t.Fatal("absolute path escape succeeded")
	}

	// Direct file symlink to /sandbox-evil/secret.txt.
	fileLink := filepath.Join(sandbox, "escape-file")
	if err := os.Symlink(evilSecret, fileLink); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.ReadFile("escape-file"); err == nil {
		t.Fatal("direct symlink escape succeeded")
	}

	// Directory symlink to /sandbox-evil (both existing child and non-existent nested child).
	dirLink := filepath.Join(sandbox, "escape-dir")
	if err := os.Symlink(evil, dirLink); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.ReadFile("escape-dir/secret.txt"); err == nil {
		t.Fatal("directory symlink read escape succeeded")
	}
	if err := disk.WriteFile("escape-dir/new.txt", []byte("pwn"), 0o600); err == nil {
		t.Fatal("directory symlink write escape succeeded")
	}
	if err := disk.MkdirAll("escape-dir/deep/sub", 0o755); err == nil {
		t.Fatal("directory symlink ancestor mkdir escape succeeded")
	}

	// Internal symlink staying inside sandbox is allowed, and Remove unlinks the symlink without deleting target.
	absInternalLink := filepath.Join(sandbox, "abs-internal-link")
	if err := os.Symlink(filepath.Join(sandbox, "sub", "ok.txt"), absInternalLink); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.ReadFile("abs-internal-link"); err == nil {
		t.Fatal("absolute symlink was followed; os.Root must reject it")
	}
	internalLink := filepath.Join(sandbox, "internal-link")
	if err := os.Symlink(filepath.Join("sub", "ok.txt"), internalLink); err != nil {
		t.Fatal(err)
	}
	gotInternal, err := disk.ReadFile("internal-link")
	if err != nil || string(gotInternal) != "ok" {
		t.Fatalf("internal symlink ReadFile = (%q, %v), want ok", gotInternal, err)
	}
	if err := disk.Remove("internal-link"); err != nil {
		t.Fatalf("Remove(internal-link): %v", err)
	}
	if gotTarget, err := disk.ReadFile("sub/ok.txt"); err != nil || string(gotTarget) != "ok" {
		t.Fatalf("Remove(internal-link) deleted target file: (%q, %v)", gotTarget, err)
	}

	if fi, err := disk.Stat("."); err != nil || !fi.IsDir() {
		t.Fatalf("Stat(.) = (%v, %v), want directory", fi, err)
	}
	if err := disk.MkdirAll(".", 0o755); err != nil {
		t.Fatalf("MkdirAll(.): %v", err)
	}
	if err := disk.Remove("."); err == nil || !strings.Contains(err.Error(), "refusing to remove root directory") {
		t.Fatalf("Remove(.) = %v, want refusing to remove root directory", err)
	}
}

func TestQueueDeterministicOrderClockBuggifyTraceAndEnv(t *testing.T) {
	run := func(seed uint64) ([]string, string) {
		q := sim.NewQueue(seed)
		var order []string
		q.ScheduleAt(20, func() {
			order = append(order, "t20-a")
			q.Record("ev", "t20-a")
		})
		cancelID := q.ScheduleAt(10, func() {
			order = append(order, "canceled")
		})
		q.ScheduleAt(10, func() {
			order = append(order, "t10-first")
			if q.Buggify(500) {
				q.Record("buggify", "hit")
			} else {
				q.Record("buggify", "miss")
			}
		})
		q.ScheduleAt(10, func() {
			order = append(order, "t10-second")
			q.Record("ev", "t10-second")
		})
		if !q.Cancel(cancelID) {
			t.Fatal("Cancel returned false")
		}

		clk := q.Clock()
		tm := clk.NewTimer(15 * time.Millisecond)
		tmStopped := clk.NewTimer(12 * time.Millisecond)
		if !tmStopped.Stop() {
			t.Fatal("queue timer Stop() returned false")
		}

		q.RunUntil(25)
		if q.NowMs() != 25 {
			t.Fatalf("NowMs = %d, want 25", q.NowMs())
		}
		select {
		case ts := <-tm.C():
			if !ts.Equal(sim.DefaultEpoch.Add(15 * time.Millisecond)) {
				t.Fatalf("queue timer fired at %v", ts)
			}
		default:
			t.Fatal("queue timer did not fire")
		}
		select {
		case <-tmStopped.C():
			t.Fatal("stopped queue timer fired")
		default:
		}
		return order, q.TraceHash()
	}

	order1, hash1 := run(2026)
	order2, hash2 := run(2026)
	wantOrder := []string{"t10-first", "t10-second", "t20-a"}
	if !slices.Equal(order1, wantOrder) {
		t.Fatalf("order = %v, want %v", order1, wantOrder)
	}
	if !slices.Equal(order1, order2) || hash1 != hash2 {
		t.Fatalf("queue non-deterministic: %v/%s vs %v/%s", order1, hash1, order2, hash2)
	}

	// Verify Record framing distinguishes field boundaries unambiguously.
	qa := sim.NewQueue(1)
	qa.Record("a", "b|c")
	qb := sim.NewQueue(1)
	qb.Record("a", "b", "c")
	if qa.TraceHash() == qb.TraceHash() {
		t.Fatal("Record framing collided on field boundary")
	}

	// Verify Buggify bounds.
	qB := sim.NewQueue(7)
	if qB.Buggify(0) {
		t.Fatal("Buggify(0) returned true")
	}
	if !qB.Buggify(1000) {
		t.Fatal("Buggify(1000) returned false")
	}
	qB.SetBuggifyEnabled(false)
	if qB.Buggify(1000) {
		t.Fatal("disabled Buggify(1000) returned true")
	}

	// Verify ConfigFromEnv precedence and regression seed deduplication.
	t.Setenv(sim.EnvSeed, "")
	t.Setenv(sim.EnvSeeds, "")
	got, err := sim.ConfigFromEnv(sim.SeedConfig{DefaultCount: 3, RegressionSeeds: []uint64{2, 99}})
	if err != nil || !slices.Equal(got, []uint64{1, 2, 3, 99}) {
		t.Fatalf("ConfigFromEnv default = (%v, %v), want [1 2 3 99]", got, err)
	}
	t.Setenv(sim.EnvSeeds, "5")
	got, err = sim.ConfigFromEnv(sim.SeedConfig{DefaultCount: 3, RegressionSeeds: []uint64{4, 77}})
	if err != nil || !slices.Equal(got, []uint64{1, 2, 3, 4, 5, 77}) {
		t.Fatalf("ConfigFromEnv NAYUTAR_SIM_SEEDS = (%v, %v), want [1 2 3 4 5 77]", got, err)
	}
	t.Setenv(sim.EnvSeed, "42")
	got, err = sim.ConfigFromEnv(sim.SeedConfig{DefaultCount: 3, RegressionSeeds: []uint64{4, 77}})
	if err != nil || !slices.Equal(got, []uint64{42}) {
		t.Fatalf("ConfigFromEnv NAYUTAR_SIM_SEED = (%v, %v), want [42]", got, err)
	}
}

func memNetPair(t *testing.T, mn *sim.MemNet, from, to string) (client, server net.Conn) {
	t.Helper()
	ln, err := mn.Listen("tcp", to)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	client, err = mn.Endpoint(from).DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return client, <-accepted
}

func TestMemNetDelayInjection(t *testing.T) {
	const addr = "127.0.0.1:7001"
	const peer = "127.0.0.1:7002"

	sendOne := func(t *testing.T, clk *sim.FakeClock, w, r net.Conn) time.Duration {
		t.Helper()
		start := clk.Now()
		done := make(chan struct{})
		go func() {
			defer close(done)
			buf := make([]byte, 1)
			if _, err := io.ReadFull(r, buf); err != nil {
				t.Errorf("read: %v", err)
			}
		}()
		wrote := make(chan struct{})
		go func() {
			defer close(wrote)
			if _, err := w.Write([]byte{1}); err != nil {
				t.Errorf("write: %v", err)
			}
		}()
		synctest.Wait()
		return advanceUntil(clk, done, start)
	}

	t.Run("zero_delay_unchanged", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			clk := sim.NewFakeClock(time.Unix(0, 0))
			mn := sim.NewMemNetWithClock(clk)
			defer func() { _ = mn.Close() }()
			c, s := memNetPair(t, mn, peer, addr)
			if got := sendOne(t, clk, c, s); got != 0 {
				t.Fatalf("elapsed = %v, want 0", got)
			}
		})
	})

	t.Run("delay_observed_on_fake_clock", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			clk := sim.NewFakeClock(time.Unix(0, 0))
			mn := sim.NewMemNetWithClock(clk)
			defer func() { _ = mn.Close() }()
			mn.SetDelay(40 * time.Millisecond)
			c, s := memNetPair(t, mn, peer, addr)
			if got := sendOne(t, clk, c, s); got != 40*time.Millisecond {
				t.Fatalf("elapsed = %v, want 40ms", got)
			}
		})
	})

	t.Run("link_override_wins_over_default", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			clk := sim.NewFakeClock(time.Unix(0, 0))
			mn := sim.NewMemNetWithClock(clk)
			defer func() { _ = mn.Close() }()
			mn.SetDelay(40 * time.Millisecond)
			mn.SetLinkDelay(peer, addr, 5*time.Millisecond)
			mn.SetLinkDelay(addr, peer, 0)
			c, s := memNetPair(t, mn, peer, addr)
			if got := sendOne(t, clk, c, s); got != 5*time.Millisecond {
				t.Fatalf("forward elapsed = %v, want 5ms", got)
			}
			if got := sendOne(t, clk, s, c); got != 0 {
				t.Fatalf("reverse elapsed = %v, want 0", got)
			}
			mn.SetLinkDelay(peer, addr, -1)
			if got := sendOne(t, clk, c, s); got != 40*time.Millisecond {
				t.Fatalf("after clearing override elapsed = %v, want 40ms", got)
			}
		})
	})

	t.Run("pipelined_writes_arrive_after_one_delay_in_order", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			clk := sim.NewFakeClock(time.Unix(0, 0))
			mn := sim.NewMemNetWithClock(clk)
			defer func() { _ = mn.Close() }()
			mn.SetDelay(10 * time.Millisecond)
			c, s := memNetPair(t, mn, peer, addr)
			start := clk.Now()
			want := []byte("abcdefgh")
			for _, b := range want {
				if _, err := c.Write([]byte{b}); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			if !clk.Now().Equal(start) {
				t.Fatalf("writer clock advanced to %v", clk.Now().Sub(start))
			}
			got := make([]byte, len(want))
			done := make(chan struct{})
			go func() {
				defer close(done)
				if _, err := io.ReadFull(s, got); err != nil {
					t.Errorf("read: %v", err)
				}
			}()
			synctest.Wait()
			elapsed := advanceUntil(clk, done, start)
			if !bytes.Equal(got, want) {
				t.Fatalf("got %q, want %q", got, want)
			}
			if elapsed != 10*time.Millisecond {
				t.Fatalf("elapsed = %v, want 10ms", elapsed)
			}
		})
	})

	t.Run("concurrent_writers_complete", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			clk := sim.NewFakeClock(time.Unix(0, 0))
			mn := sim.NewMemNetWithClock(clk)
			defer func() { _ = mn.Close() }()
			mn.SetDelay(25 * time.Millisecond)
			c, s := memNetPair(t, mn, peer, addr)
			for _, b := range []byte("xyz") {
				go func() {
					if _, err := c.Write([]byte{b}); err != nil {
						t.Errorf("write: %v", err)
					}
				}()
			}
			got := make([]byte, 3)
			done := make(chan struct{})
			go func() {
				defer close(done)
				if _, err := io.ReadFull(s, got); err != nil {
					t.Errorf("read: %v", err)
				}
			}()
			synctest.Wait()
			start := clk.Now()
			if elapsed := advanceUntil(clk, done, start); elapsed != 25*time.Millisecond {
				t.Fatalf("elapsed = %v, want 25ms", elapsed)
			}
			slices.Sort(got)
			if string(got) != "xyz" {
				t.Fatalf("got %q", got)
			}
		})
	})

	t.Run("writer_holding_mutex_while_another_waits_completes", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			clk := sim.NewFakeClock(time.Unix(0, 0))
			mn := sim.NewMemNetWithClock(clk)
			defer func() { _ = mn.Close() }()
			mn.SetDelay(30 * time.Millisecond)
			c, s := memNetPair(t, mn, peer, addr)
			var mu sync.Mutex
			for range 4 {
				go func() {
					mu.Lock()
					defer mu.Unlock()
					if _, err := c.Write([]byte{1}); err != nil {
						t.Errorf("write: %v", err)
					}
				}()
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				if _, err := io.ReadFull(s, make([]byte, 4)); err != nil {
					t.Errorf("read: %v", err)
				}
			}()
			synctest.Wait()
			if elapsed := advanceUntil(clk, done, clk.Now()); elapsed != 30*time.Millisecond {
				t.Fatalf("elapsed = %v, want 30ms", elapsed)
			}
		})
	})

	t.Run("write_deadline_shorter_than_delay_succeeds", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			clk := sim.NewFakeClock(time.Unix(0, 0))
			mn := sim.NewMemNetWithClock(clk)
			defer func() { _ = mn.Close() }()
			mn.SetDelay(40 * time.Millisecond)
			c, s := memNetPair(t, mn, peer, addr)
			if err := c.SetWriteDeadline(clk.Now().Add(5 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Write([]byte{7}); err != nil {
				t.Fatalf("write with short deadline: %v", err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				buf := make([]byte, 1)
				if _, err := io.ReadFull(s, buf); err != nil || buf[0] != 7 {
					t.Errorf("read = (%v, %v)", buf, err)
				}
			}()
			synctest.Wait()
			if elapsed := advanceUntil(clk, done, clk.Now()); elapsed != 40*time.Millisecond {
				t.Fatalf("elapsed = %v, want 40ms", elapsed)
			}
			if _, err := c.Write([]byte{8}); !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("write after deadline err = %v, want deadline exceeded", err)
			}
		})
	})

	t.Run("close_during_flight_drops_data_and_leaves_no_timer", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			clk := sim.NewFakeClock(time.Unix(0, 0))
			mn := sim.NewMemNetWithClock(clk)
			defer func() { _ = mn.Close() }()
			mn.SetDelay(40 * time.Millisecond)
			c, s := memNetPair(t, mn, peer, addr)
			if _, err := c.Write([]byte("lost")); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			if clk.PendingTimers() != 1 {
				t.Fatalf("PendingTimers = %d, want 1", clk.PendingTimers())
			}
			_ = c.Close()
			synctest.Wait()
			if clk.PendingTimers() != 0 {
				t.Fatalf("PendingTimers after close = %d, want 0", clk.PendingTimers())
			}
			if n, err := s.Read(make([]byte, 4)); n != 0 || err == nil {
				t.Fatalf("read after close = (%d, %v), want EOF", n, err)
			}
			if _, err := c.Write([]byte{1}); err == nil {
				t.Fatal("write after close succeeded")
			}
		})
	})

	t.Run("partition_during_flight_drops_data", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			clk := sim.NewFakeClock(time.Unix(0, 0))
			mn := sim.NewMemNetWithClock(clk)
			defer func() { _ = mn.Close() }()
			mn.SetDelay(40 * time.Millisecond)
			c, s := memNetPair(t, mn, peer, addr)
			if _, err := c.Write([]byte("lost")); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			mn.Partition(addr)
			synctest.Wait()
			if clk.PendingTimers() != 0 {
				t.Fatalf("PendingTimers after partition = %d, want 0", clk.PendingTimers())
			}
			if _, err := c.Write([]byte{1}); !errors.Is(err, sim.ErrPartitioned) {
				t.Fatalf("write during partition err = %v, want ErrPartitioned", err)
			}
			if _, err := s.Read(make([]byte, 4)); !errors.Is(err, sim.ErrPartitioned) {
				t.Fatalf("read during partition err = %v, want ErrPartitioned", err)
			}
		})
	})

	t.Run("one_way_drop_during_flight_drops_data", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			clk := sim.NewFakeClock(time.Unix(0, 0))
			mn := sim.NewMemNetWithClock(clk)
			defer func() { _ = mn.Close() }()
			mn.SetDelay(40 * time.Millisecond)
			c, s := memNetPair(t, mn, peer, addr)
			if _, err := c.Write([]byte("lost")); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			mn.SetOneWayDrop(peer, addr, true)
			clk.Advance(40 * time.Millisecond)
			synctest.Wait()
			mn.SetOneWayDrop(peer, addr, false)
			if _, err := c.Write([]byte("k")); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 4)
			done := make(chan struct{})
			var n int
			go func() {
				defer close(done)
				n, _ = s.Read(buf)
			}()
			synctest.Wait()
			advanceUntil(clk, done, clk.Now())
			if string(buf[:n]) != "k" {
				t.Fatalf("got %q, want only the post-drop write", buf[:n])
			}
		})
	})
}

func advanceUntil(clk *sim.FakeClock, done <-chan struct{}, start time.Time) time.Duration {
	for {
		synctest.Wait()
		select {
		case <-done:
			return clk.Now().Sub(start)
		default:
		}
		clk.Advance(time.Millisecond)
	}
}

func TestMemNetDelayRaisedDuringBlockedWriteKeepsOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mn := sim.NewMemNet()
		defer func() { _ = mn.Close() }()
		c, s := memNetPair(t, mn, "127.0.0.1:7602", "127.0.0.1:7601")
		got := make(chan []byte, 1)
		go func() {
			time.Sleep(5 * time.Millisecond)
			b, _ := io.ReadAll(io.LimitReader(s, 2))
			got <- b
		}()
		first := make(chan error, 1)
		go func() { _, err := c.Write([]byte("a")); first <- err }()
		synctest.Wait()
		mn.SetDelay(time.Millisecond)
		if _, err := c.Write([]byte("b")); err != nil {
			t.Fatal(err)
		}
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		if b := <-got; string(b) != "ab" {
			t.Fatalf("got %q, want ab", b)
		}
	})
}

func TestMemNetBlockedWriteKeepsItsDeadlineWhenDelayRises(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mn := sim.NewMemNet()
		defer func() { _ = mn.Close() }()
		c, s := memNetPair(t, mn, "127.0.0.1:7402", "127.0.0.1:7401")
		defer func() { _ = s.Close() }()
		if err := c.SetWriteDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		res := make(chan error, 1)
		start := time.Now()
		go func() { _, err := c.Write([]byte("a")); res <- err }()
		synctest.Wait()
		mn.SetDelay(2 * time.Millisecond)
		go func() { _, _ = c.Write([]byte("b")) }()
		select {
		case err := <-res:
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("err = %v, want deadline exceeded", err)
			}
			if el := time.Since(start); el != 10*time.Millisecond {
				t.Fatalf("returned after %v, want 10ms", el)
			}
		case <-time.After(time.Second):
			t.Fatal("blocked write ignored its 10ms deadline")
		}
	})
}

func TestMemNetOrderUnderDelayToggle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		mn := sim.NewMemNet()
		defer func() { _ = mn.Close() }()
		c, s := memNetPair(t, mn, "127.0.0.1:7302", "127.0.0.1:7301")
		const n = 3000
		stop := make(chan struct{})
		toggled := make(chan struct{})
		go func() {
			defer close(toggled)
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if i%2 == 0 {
					mn.SetDelay(0)
				} else {
					mn.SetDelay(time.Duration(i%3+1) * time.Millisecond)
				}
				time.Sleep(time.Millisecond)
			}
		}()
		errc := make(chan error, 1)
		go func() {
			buf := make([]byte, 4)
			for i := range n {
				if _, err := io.ReadFull(s, buf); err != nil {
					errc <- err
					return
				}
				if got := binary.BigEndian.Uint32(buf); got != uint32(i) {
					errc <- errors.New("out of order")
					return
				}
			}
			errc <- nil
		}()
		buf := make([]byte, 4)
		for i := range n {
			binary.BigEndian.PutUint32(buf, uint32(i))
			if _, err := c.Write(buf); err != nil {
				t.Fatal(err)
			}
			if i%7 == 0 {
				time.Sleep(time.Millisecond)
			}
		}
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
		close(stop)
		<-toggled
	})
}

func TestMemNetConcurrentCloseLeavesNoTimersOrGoroutines(t *testing.T) {
	for range 200 {
		synctest.Test(t, func(t *testing.T) {
			clk := sim.NewFakeClock(time.Time{})
			mn := sim.NewMemNetWithClock(clk)
			mn.SetDelay(3 * time.Millisecond)
			c, s := memNetPair(t, mn, "127.0.0.1:7502", "127.0.0.1:7501")
			var wg sync.WaitGroup
			for _, conn := range []net.Conn{c, s} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range 50 {
						if _, err := conn.Write([]byte("zz")); err != nil {
							return
						}
					}
				}()
			}
			go func() { _, _ = io.Copy(io.Discard, s) }()
			go func() { _, _ = io.Copy(io.Discard, c) }()
			clk.Advance(2 * time.Millisecond)
			go func() { _ = c.Close() }()
			go func() { _ = s.Close() }()
			wg.Wait()
			synctest.Wait()
			if p := clk.PendingTimers(); p != 0 {
				t.Errorf("PendingTimers = %d, want 0", p)
			}
			_ = mn.Close()
			if _, err := c.Write([]byte("q")); err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
				t.Errorf("write after close: %v", err)
			}
		})
	}
}

func TestMemNetDeadlinesJudgedOnInjectedClock(t *testing.T) {
	type env struct {
		now     func() time.Time
		advance func(time.Duration)
		mn      func() *sim.MemNet
	}
	setups := map[string]func() env{
		"fake": func() env {
			clk := sim.NewFakeClock(time.Time{})
			clk.Advance(time.Hour)
			return env{
				now:     clk.Now,
				advance: func(d time.Duration) { clk.Advance(d); synctest.Wait() },
				mn:      func() *sim.MemNet { return sim.NewMemNetWithClock(clk) },
			}
		},
		"real_in_bubble": func() env {
			return env{
				now:     time.Now,
				advance: func(d time.Duration) { time.Sleep(d); synctest.Wait() },
				mn:      sim.NewMemNet,
			}
		},
	}
	for name, setup := range setups {
		for _, delay := range []time.Duration{0, time.Millisecond} {
			t.Run(name+"/"+delay.String(), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					e := setup()
					mn := e.mn()
					defer func() { _ = mn.Close() }()
					mn.SetDelay(delay)
					c, s := memNetPair(t, mn, "127.0.0.1:7802", "127.0.0.1:7801")
					checkTimeout := func(what string, err error) {
						t.Helper()
						var ne net.Error
						if !errors.Is(err, os.ErrDeadlineExceeded) || !errors.As(err, &ne) || !ne.Timeout() {
							t.Fatalf("%s err = %v, want a timeout satisfying os.ErrDeadlineExceeded", what, err)
						}
					}

					if err := c.SetReadDeadline(e.now().Add(time.Minute)); err != nil {
						t.Fatal(err)
					}
					rerr := make(chan error, 1)
					go func() { _, err := c.Read(make([]byte, 1)); rerr <- err }()
					synctest.Wait()
					select {
					case err := <-rerr:
						t.Fatalf("read returned early: %v", err)
					default:
					}
					e.advance(time.Minute)
					checkTimeout("read", <-rerr)

					_ = c.SetReadDeadline(e.now().Add(-time.Minute))
					_, err := c.Read(make([]byte, 1))
					checkTimeout("read in the past", err)
					_ = c.SetReadDeadline(time.Time{})

					if err := c.SetWriteDeadline(e.now().Add(time.Minute)); err != nil {
						t.Fatal(err)
					}
					if delay == 0 {
						werr := make(chan error, 1)
						go func() { _, err := c.Write([]byte("w")); werr <- err }()
						synctest.Wait()
						select {
						case err := <-werr:
							t.Fatalf("write returned early: %v", err)
						default:
						}
						e.advance(time.Minute)
						checkTimeout("write", <-werr)
					} else {
						e.advance(time.Minute)
					}
					_, err = c.Write([]byte("x"))
					checkTimeout("write after deadline", err)
					_ = s.Close()
				})
			})
		}
	}
}

func TestMemNetDeadlineMovedIntoPastUnblocksPendingOps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clk := sim.NewFakeClock(time.Time{})
		mn := sim.NewMemNetWithClock(clk)
		defer func() { _ = mn.Close() }()
		c, s := memNetPair(t, mn, "127.0.0.1:7902", "127.0.0.1:7901")
		defer func() { _ = s.Close() }()
		rerr := make(chan error, 1)
		werr := make(chan error, 1)
		go func() { _, err := c.Read(make([]byte, 1)); rerr <- err }()
		go func() { _, err := c.Write([]byte("w")); werr <- err }()
		synctest.Wait()
		for _, ch := range []chan error{rerr, werr} {
			select {
			case err := <-ch:
				t.Fatalf("returned early: %v", err)
			default:
			}
		}
		if err := c.SetDeadline(clk.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		for name, ch := range map[string]chan error{"read": rerr, "write": werr} {
			var ne net.Error
			err := <-ch
			if !errors.Is(err, os.ErrDeadlineExceeded) || !errors.As(err, &ne) || !ne.Timeout() {
				t.Fatalf("%s err = %v, want timeout", name, err)
			}
		}
		if err := c.SetDeadline(time.Time{}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := io.ReadFull(s, make([]byte, 1)); done <- err }()
		if _, err := c.Write([]byte("z")); err != nil {
			t.Fatalf("write after clearing the deadline: %v", err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
