package sim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"
)

var (
	// ErrPartitioned is returned when a virtual endpoint or link is cut or partitioned.
	ErrPartitioned = errors.New("sim: network partitioned")
	// ErrAddrInUse is returned when Listen binds an address already registered on MemNet.
	ErrAddrInUse = errors.New("sim: address already in use")
)

type linkPair struct {
	from string
	to   string
}

// MemNet is an in-memory Net implementation built on net.Pipe so connections
// remain durably blocked inside testing/synctest bubbles with zero OS sockets.
type MemNet struct {
	mu           sync.Mutex
	closed       bool
	nextPort     int
	listeners    map[string]*memListener
	conns        map[*memConn]struct{}
	refused      map[string]bool
	partitioned  map[string]bool
	cuts         map[linkPair]bool
	oneWayDrops  map[linkPair]bool
	delay        time.Duration
	linkDelays   map[linkPair]time.Duration
	clock        Clock
	faultChanged chan struct{}
}

// NewMemNet constructs an empty in-memory virtual network whose injected delays run on the real clock.
func NewMemNet() *MemNet { return NewMemNetWithClock(RealClock()) }

// NewMemNetWithClock constructs an empty in-memory virtual network whose injected delays run on clock.
func NewMemNetWithClock(clock Clock) *MemNet {
	return &MemNet{
		clock:        clock,
		linkDelays:   make(map[linkPair]time.Duration),
		nextPort:     10000,
		listeners:    make(map[string]*memListener),
		conns:        make(map[*memConn]struct{}),
		refused:      make(map[string]bool),
		partitioned:  make(map[string]bool),
		cuts:         make(map[linkPair]bool),
		oneWayDrops:  make(map[linkPair]bool),
		faultChanged: make(chan struct{}),
	}
}

// Endpoint returns a Net view that tags outbound dials with localAddr for directed fault rules.
func (m *MemNet) Endpoint(localAddr string) Net {
	return &memNetEndpoint{net: m, localAddr: localAddr}
}

type memNetEndpoint struct {
	net       *MemNet
	localAddr string
}

func (e *memNetEndpoint) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return e.net.dialFrom(ctx, e.localAddr, network, address)
}

func (e *memNetEndpoint) Listen(network, address string) (net.Listener, error) {
	return e.net.Listen(network, address)
}

func (m *MemNet) notifyFaultChangeLocked() {
	close(m.faultChanged)
	m.faultChanged = make(chan struct{})
}

func (m *MemNet) allocPortLocked() int {
	for {
		p := m.nextPort
		m.nextPort++
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(p))
		if _, exists := m.listeners[addr]; !exists {
			return p
		}
	}
}

func (m *MemNet) parseListenAddrLocked(network, address string) (string, net.Addr, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
		host, portStr, err := net.SplitHostPort(address)
		if err != nil {
			return "", nil, &net.OpError{Op: "listen", Net: network, Err: err}
		}
		if host == "" || host == "localhost" || host == "0.0.0.0" || host == "::" {
			host = "127.0.0.1"
		}
		ip := net.ParseIP(host)
		if ip == nil {
			ip = net.IPv4(127, 0, 0, 1)
		}
		var port int
		if portStr == "" || portStr == "0" {
			port = m.allocPortLocked()
		} else {
			port, err = strconv.Atoi(portStr)
			if err != nil || port < 1 || port > 65535 {
				return "", nil, &net.OpError{Op: "listen", Net: network, Err: fmt.Errorf("invalid port %q", portStr)}
			}
		}
		canon := net.JoinHostPort(host, strconv.Itoa(port))
		return canon, &net.TCPAddr{IP: ip, Port: port}, nil
	case "unix":
		if address == "" {
			return "", nil, &net.OpError{Op: "listen", Net: network, Err: errors.New("empty unix address")}
		}
		return address, &net.UnixAddr{Name: address, Net: "unix"}, nil
	default:
		return "", nil, &net.OpError{Op: "listen", Net: network, Err: net.UnknownNetworkError(network)}
	}
}

func normalizeDialAddr(network, address string) string {
	switch network {
	case "tcp", "tcp4", "tcp6":
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return address
		}
		if host == "" || host == "localhost" {
			return net.JoinHostPort("127.0.0.1", port)
		}
		return net.JoinHostPort(host, port)
	default:
		return address
	}
}

// Listen binds a virtual listener on address; port 0 assigns an ephemeral 127.0.0.1:<port>.
func (m *MemNet) Listen(network, address string) (net.Listener, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, &net.OpError{Op: "listen", Net: network, Err: net.ErrClosed}
	}
	canon, nAddr, err := m.parseListenAddrLocked(network, address)
	if err != nil {
		return nil, err
	}
	if _, exists := m.listeners[canon]; exists {
		return nil, &net.OpError{Op: "listen", Net: network, Addr: nAddr, Err: ErrAddrInUse}
	}
	ln := &memListener{
		net:      m,
		key:      canon,
		addr:     nAddr,
		acceptCh: make(chan net.Conn),
		closedCh: make(chan struct{}),
	}
	m.listeners[canon] = ln
	return ln, nil
}

// DialContext connects to a virtual listener registered on m using net.Pipe.
func (m *MemNet) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return m.dialFrom(ctx, "", network, address)
}

func (m *MemNet) dialFrom(ctx context.Context, fromKey, network, address string) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canon := normalizeDialAddr(network, address)

	for {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return nil, &net.OpError{Op: "dial", Net: network, Err: net.ErrClosed}
		}
		if m.refused[canon] || m.refused[address] {
			m.mu.Unlock()
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		}
		if m.isCutLocked(fromKey, canon) || m.isCutLocked(canon, fromKey) {
			m.mu.Unlock()
			return nil, &net.OpError{Op: "dial", Net: network, Err: ErrPartitioned}
		}
		if m.isDroppedLocked(fromKey, canon) || m.isDroppedLocked(canon, fromKey) {
			ch := m.faultChanged
			m.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, &net.OpError{Op: "dial", Net: network, Err: ctx.Err()}
			}
		}
		ln := m.listeners[canon]
		if ln == nil {
			ln = m.listeners[address]
		}
		if ln == nil {
			m.mu.Unlock()
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		}
		clientPort := m.allocPortLocked()
		localKey := fromKey
		var cliNetAddr net.Addr
		if network == "unix" {
			if localKey == "" {
				localKey = fmt.Sprintf("unix-client-%d", clientPort)
			}
			cliNetAddr = &net.UnixAddr{Name: localKey, Net: "unix"}
		} else {
			tcpAddr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: clientPort}
			if localKey == "" {
				localKey = tcpAddr.String()
			}
			cliNetAddr = tcpAddr
		}

		c1, c2 := net.Pipe()
		clientConn := newMemConn(c1, m, localKey, ln.key, cliNetAddr, ln.addr)
		serverConn := newMemConn(c2, m, ln.key, localKey, ln.addr, cliNetAddr)
		m.conns[clientConn] = struct{}{}
		m.conns[serverConn] = struct{}{}
		m.mu.Unlock()

		select {
		case ln.acceptCh <- serverConn:
			return clientConn, nil
		case <-ln.closedCh:
			_ = clientConn.Close()
			_ = serverConn.Close()
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		case <-ctx.Done():
			_ = clientConn.Close()
			_ = serverConn.Close()
			return nil, &net.OpError{Op: "dial", Net: network, Err: ctx.Err()}
		}
	}
}

func (m *MemNet) isCutLocked(from, to string) bool {
	if m.partitioned[to] || (from != "" && m.partitioned[from]) {
		return true
	}
	if m.cuts[linkPair{from: from, to: to}] {
		return true
	}
	if from != "" && m.cuts[linkPair{from: "", to: to}] {
		return true
	}
	return false
}

func (m *MemNet) isDroppedLocked(from, to string) bool {
	if m.oneWayDrops[linkPair{from: from, to: to}] {
		return true
	}
	if from != "" && m.oneWayDrops[linkPair{from: "", to: to}] {
		return true
	}
	return false
}

// SetRefused toggles immediate ECONNREFUSED on new dials to addr.
func (m *MemNet) SetRefused(addr string, refuse bool) {
	m.mu.Lock()
	canon := normalizeDialAddr("tcp", addr)
	if refuse {
		m.refused[canon] = true
		m.refused[addr] = true
	} else {
		delete(m.refused, canon)
		delete(m.refused, addr)
	}
	m.notifyFaultChangeLocked()
	m.mu.Unlock()
}

// Partition isolates addr, closing active connections involving addr and rejecting new dials.
func (m *MemNet) Partition(addr string) {
	m.mu.Lock()
	canon := normalizeDialAddr("tcp", addr)
	m.partitioned[canon] = true
	m.partitioned[addr] = true
	toClose := m.matchingConnsLocked(func(c *memConn) bool {
		return c.localKey == canon || c.remoteKey == canon || c.localKey == addr || c.remoteKey == addr
	})
	m.notifyFaultChangeLocked()
	m.mu.Unlock()
	for _, c := range toClose {
		_ = c.Close()
	}
}

// Heal removes a Partition on addr.
func (m *MemNet) Heal(addr string) {
	m.mu.Lock()
	canon := normalizeDialAddr("tcp", addr)
	delete(m.partitioned, canon)
	delete(m.partitioned, addr)
	m.notifyFaultChangeLocked()
	m.mu.Unlock()
}

// SetLinkCut toggles a bidirectional or directed cut between from and to and closes active matching connections.
func (m *MemNet) SetLinkCut(from, to string, cut bool) {
	m.mu.Lock()
	cf := from
	if from != "" {
		cf = normalizeDialAddr("tcp", from)
	}
	ct := normalizeDialAddr("tcp", to)
	lp := linkPair{from: cf, to: ct}
	var toClose []*memConn
	if cut {
		m.cuts[lp] = true
		toClose = m.matchingConnsLocked(func(c *memConn) bool {
			return (cf == "" || c.localKey == cf || c.remoteKey == cf) && (c.remoteKey == ct || c.localKey == ct)
		})
	} else {
		delete(m.cuts, lp)
	}
	m.notifyFaultChangeLocked()
	m.mu.Unlock()
	for _, c := range toClose {
		_ = c.Close()
	}
}

// SetOneWayDrop toggles silent byte/connect dropping strictly in direction from -> to.
func (m *MemNet) SetOneWayDrop(from, to string, drop bool) {
	m.mu.Lock()
	cf := from
	if from != "" {
		cf = normalizeDialAddr("tcp", from)
	}
	ct := normalizeDialAddr("tcp", to)
	lp := linkPair{from: cf, to: ct}
	if drop {
		m.oneWayDrops[lp] = true
	} else {
		delete(m.oneWayDrops, lp)
	}
	m.notifyFaultChangeLocked()
	m.mu.Unlock()
}

// SetDelay sets the default one-way delivery latency of every write on every connection; writes do not block for it.
func (m *MemNet) SetDelay(d time.Duration) {
	m.mu.Lock()
	m.delay = max(d, 0)
	m.mu.Unlock()
}

// SetLinkDelay overrides the default latency for writes strictly in direction from -> to, including a zero override.
// A negative d removes the override.
func (m *MemNet) SetLinkDelay(from, to string, d time.Duration) {
	m.mu.Lock()
	cf := from
	if from != "" {
		cf = normalizeDialAddr("tcp", from)
	}
	lp := linkPair{from: cf, to: normalizeDialAddr("tcp", to)}
	if d < 0 {
		delete(m.linkDelays, lp)
	} else {
		m.linkDelays[lp] = d
	}
	m.mu.Unlock()
}

func (m *MemNet) delayLocked(from, to string) time.Duration {
	if d, ok := m.linkDelays[linkPair{from: from, to: to}]; ok {
		return d
	}
	if from != "" {
		if d, ok := m.linkDelays[linkPair{from: "", to: to}]; ok {
			return d
		}
	}
	return m.delay
}

func (m *MemNet) matchingConnsLocked(pred func(*memConn) bool) []*memConn {
	var out []*memConn
	for c := range m.conns {
		if pred(c) {
			out = append(out, c)
		}
	}
	return out
}

// Close closes all registered listeners and active connections cleanly.
func (m *MemNet) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	listeners := make([]*memListener, 0, len(m.listeners))
	for _, ln := range m.listeners {
		listeners = append(listeners, ln)
	}
	clear(m.listeners)
	conns := make([]*memConn, 0, len(m.conns))
	for c := range m.conns {
		conns = append(conns, c)
	}
	clear(m.conns)
	m.notifyFaultChangeLocked()
	m.mu.Unlock()

	for _, ln := range listeners {
		ln.closeInternal()
	}
	var errs []error
	for _, c := range conns {
		errs = append(errs, c.closeInner())
	}
	return errors.Join(errs...)
}

type memListener struct {
	net      *MemNet
	key      string
	addr     net.Addr
	acceptCh chan net.Conn
	closedCh chan struct{}
	once     sync.Once
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.acceptCh:
		return c, nil
	case <-l.closedCh:
		return nil, net.ErrClosed
	}
}

func (l *memListener) closeInternal() {
	l.once.Do(func() {
		close(l.closedCh)
	})
}

func (l *memListener) Close() error {
	l.net.mu.Lock()
	if cur, ok := l.net.listeners[l.key]; ok && cur == l {
		delete(l.net.listeners, l.key)
	}
	l.net.mu.Unlock()
	l.closeInternal()
	return nil
}

func (l *memListener) Addr() net.Addr { return l.addr }

type memConn struct {
	inner      net.Conn
	net        *MemNet
	localKey   string
	remoteKey  string
	localAddr  net.Addr
	remoteAddr net.Addr

	closedCh  chan struct{}
	closeOnce sync.Once

	rdl *deadline
	wdl *deadline

	qmu     sync.Mutex
	queue   []*inflight
	pumping bool
	werr    error
}

type inflight struct {
	data      []byte
	at        time.Time
	done      chan struct{}
	err       error
	writing   bool
	finished  bool
	cancelled bool
}

func newMemConn(inner net.Conn, m *MemNet, localKey, remoteKey string, localAddr, remoteAddr net.Addr) *memConn {
	c := &memConn{
		inner:      inner,
		net:        m,
		localKey:   localKey,
		remoteKey:  remoteKey,
		localAddr:  localAddr,
		remoteAddr: remoteAddr,
		closedCh:   make(chan struct{}),
	}
	c.rdl = newDeadline(m.clock, c.closedCh, func(expired bool) {
		if expired {
			_ = inner.SetReadDeadline(aLongTimeAgo)
		} else {
			_ = inner.SetReadDeadline(time.Time{})
		}
	})
	c.wdl = newDeadline(m.clock, c.closedCh, nil)
	return c
}

var aLongTimeAgo = time.Unix(1, 0)

// deadline judges a net.Conn deadline on the injected clock. Waiters select on expired(), which
// is closed once the deadline has passed; moving the deadline into the past closes it at once.
type deadline struct {
	clock    Clock
	closedCh <-chan struct{}
	sync     func(expired bool)

	mu    sync.Mutex
	gen   uint64
	stop  chan struct{}
	fired chan struct{}
}

func newDeadline(clock Clock, closedCh <-chan struct{}, sync func(expired bool)) *deadline {
	return &deadline{clock: clock, closedCh: closedCh, sync: sync, fired: make(chan struct{})}
}

func (d *deadline) expired() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.fired
}

func (d *deadline) fireLocked() {
	close(d.fired)
	if d.sync != nil {
		d.sync(true)
	}
}

func (d *deadline) rearmLocked() {
	d.fired = make(chan struct{})
	if d.sync != nil {
		d.sync(false)
	}
}

func (d *deadline) set(t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gen++
	if d.stop != nil {
		close(d.stop)
		d.stop = nil
	}
	wasFired := false
	select {
	case <-d.fired:
		wasFired = true
	default:
	}
	if t.IsZero() {
		if wasFired {
			d.rearmLocked()
		}
		return
	}
	dur := t.Sub(d.clock.Now())
	if dur <= 0 {
		if !wasFired {
			d.fireLocked()
		}
		return
	}
	if wasFired {
		d.rearmLocked()
	}
	stop := make(chan struct{})
	d.stop = stop
	gen := d.gen
	var timerC <-chan time.Time
	var tm Timer
	if tc, ok := d.clock.(TimerClock); ok {
		tm = tc.NewTimer(dur)
		timerC = tm.C()
	} else {
		timerC = d.clock.After(dur)
	}
	go func() {
		select {
		case <-timerC:
			d.mu.Lock()
			if d.gen == gen {
				d.fireLocked()
			}
			d.mu.Unlock()
		case <-stop:
			if tm != nil {
				tm.Stop()
			}
		case <-d.closedCh:
			if tm != nil {
				tm.Stop()
			}
		}
	}()
}

func (c *memConn) closeInner() error {
	c.closeOnce.Do(func() { close(c.closedCh) })
	return c.inner.Close()
}

func (c *memConn) Read(b []byte) (int, error) {
	c.net.mu.Lock()
	cut := c.net.isCutLocked(c.remoteKey, c.localKey) || c.net.isCutLocked(c.localKey, c.remoteKey)
	c.net.mu.Unlock()
	if cut {
		return 0, ErrPartitioned
	}
	return c.inner.Read(b)
}

// Write never waits for a nonzero delay: it enqueues a copy of b for in-order delivery and returns.
// With a zero delay it waits, on channels only, until the pump has handed its bytes to the pipe
// (the peer has read them), the connection closes, or the write deadline passes.
// Only the pump ever calls inner.Write.
func (c *memConn) Write(b []byte) (int, error) {
	c.net.mu.Lock()
	cut := c.net.isCutLocked(c.localKey, c.remoteKey) || c.net.isCutLocked(c.remoteKey, c.localKey)
	drop := c.net.isDroppedLocked(c.localKey, c.remoteKey)
	delay := c.net.delayLocked(c.localKey, c.remoteKey)
	c.net.mu.Unlock()
	if cut {
		return 0, ErrPartitioned
	}
	if drop {
		return len(b), nil
	}

	c.qmu.Lock()
	if c.werr != nil {
		err := c.werr
		c.qmu.Unlock()
		return 0, err
	}
	select {
	case <-c.closedCh:
		c.qmu.Unlock()
		return 0, net.ErrClosed
	default:
	}
	expired := c.wdl.expired()
	select {
	case <-expired:
		c.qmu.Unlock()
		return 0, os.ErrDeadlineExceeded
	default:
	}
	if len(b) == 0 {
		c.qmu.Unlock()
		return 0, nil
	}
	it := &inflight{data: bytes.Clone(b), at: c.net.clock.Now().Add(delay)}
	if delay == 0 {
		it.done = make(chan struct{})
	}
	c.queue = append(c.queue, it)
	if !c.pumping {
		c.pumping = true
		go c.pump()
	}
	c.qmu.Unlock()
	if delay != 0 {
		return len(b), nil
	}

	select {
	case <-it.done:
	case <-c.closedCh:
		select {
		case <-it.done:
		default:
			return 0, net.ErrClosed
		}
	case <-expired:
		c.qmu.Lock()
		if !it.finished {
			it.cancelled = true
			if it.writing {
				_ = c.inner.SetWriteDeadline(aLongTimeAgo)
			}
			c.qmu.Unlock()
			return 0, os.ErrDeadlineExceeded
		}
		c.qmu.Unlock()
	}
	c.qmu.Lock()
	err := it.err
	c.qmu.Unlock()
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *memConn) finishLocked(it *inflight, err error) {
	it.finished = true
	it.err = err
	if it.done != nil {
		close(it.done)
	}
}

// pump delivers queued writes in order and is the only caller of inner.Write and the only owner of
// its write deadline (apart from a timed-out writer aborting its own in-flight item under qmu).
// It blocks only on channels and the injected clock's cancellable timer, so it is durably blocked
// inside a synctest bubble.
func (c *memConn) pump() {
	for {
		c.qmu.Lock()
		if len(c.queue) == 0 {
			c.pumping = false
			c.qmu.Unlock()
			return
		}
		it := c.queue[0]
		c.queue = c.queue[1:]
		c.qmu.Unlock()

		if d := it.at.Sub(c.net.clock.Now()); d > 0 {
			if !c.waitDelay(d) {
				c.abortPump(net.ErrClosed)
				return
			}
		}

		c.net.mu.Lock()
		discard := c.net.isCutLocked(c.localKey, c.remoteKey) || c.net.isCutLocked(c.remoteKey, c.localKey) ||
			c.net.isDroppedLocked(c.localKey, c.remoteKey)
		c.net.mu.Unlock()

		c.qmu.Lock()
		if it.cancelled || discard {
			c.finishLocked(it, nil)
			c.qmu.Unlock()
			continue
		}
		it.writing = true
		_ = c.inner.SetWriteDeadline(time.Time{})
		c.qmu.Unlock()

		_, err := c.inner.Write(it.data)

		c.qmu.Lock()
		if err != nil && it.cancelled {
			err = nil
		}
		c.finishLocked(it, err)
		c.qmu.Unlock()
		if err != nil {
			c.abortPump(err)
			return
		}
	}
}

func (c *memConn) waitDelay(d time.Duration) bool {
	if tc, ok := c.net.clock.(TimerClock); ok {
		tm := tc.NewTimer(d)
		select {
		case <-tm.C():
			return true
		case <-c.closedCh:
			tm.Stop()
			return false
		}
	}
	select {
	case <-c.net.clock.After(d):
		return true
	case <-c.closedCh:
		return false
	}
}

func (c *memConn) abortPump(err error) {
	c.qmu.Lock()
	c.werr = err
	for _, it := range c.queue {
		if !it.finished {
			c.finishLocked(it, err)
		}
	}
	c.queue = nil
	c.pumping = false
	c.qmu.Unlock()
}

func (c *memConn) Close() error {
	c.net.mu.Lock()
	delete(c.net.conns, c)
	c.net.mu.Unlock()
	return c.closeInner()
}

func (c *memConn) LocalAddr() net.Addr  { return c.localAddr }
func (c *memConn) RemoteAddr() net.Addr { return c.remoteAddr }

func (c *memConn) SetReadDeadline(t time.Time) error {
	select {
	case <-c.closedCh:
		return net.ErrClosed
	default:
	}
	c.rdl.set(t)
	return nil
}

func (c *memConn) SetWriteDeadline(t time.Time) error {
	select {
	case <-c.closedCh:
		return net.ErrClosed
	default:
	}
	c.wdl.set(t)
	return nil
}

func (c *memConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}
