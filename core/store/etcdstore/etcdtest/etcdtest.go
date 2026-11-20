// Package etcdtest starts a throwaway three-member etcd cluster for tests.
package etcdtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"
)

// Members is the fixed number of members in a test cluster.
const Members = 3

// BinaryEnv names the environment variable that points at the etcd binary.
// Without it the binary is looked up on PATH.
const BinaryEnv = "NAYUTAR_ETCD_BIN"

var tokenSeq atomic.Int64

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type member struct {
	name      string
	clientURL string
	peerURL   string
	proxyURL  string
	dataDir   string
	cmd       *exec.Cmd
	output    *syncBuffer
}

// Cluster is a running three-member etcd cluster with one data directory per member.
type Cluster struct {
	Endpoints []string

	bin            string
	rootDir        string
	portLock       *os.File
	token          string
	initialCluster string
	proxy          *peerProxy
	mu             sync.Mutex
	members        [Members]member
}

// Start runs a three-member etcd cluster on free loopback ports using static
// bootstrap and waits until every member serves a linearizable read on its own endpoint.
func Start() (*Cluster, error) {
	bin := os.Getenv(BinaryEnv)
	if bin == "" {
		found, err := exec.LookPath("etcd")
		if err != nil {
			return nil, fmt.Errorf("etcd binary not found: set %s or put etcd on PATH", BinaryEnv)
		}
		bin = found
	}
	urls, portLock, err := claimPortBlock(3 * Members)
	if err != nil {
		return nil, err
	}
	rootDir, err := os.MkdirTemp("", "nayutar-etcd-")
	if err != nil {
		_ = portLock.Close()
		return nil, err
	}

	c := &Cluster{
		Endpoints: make([]string, Members),
		bin:       bin,
		rootDir:   rootDir,
		portLock:  portLock,
		token:     fmt.Sprintf("nayutar-test-%d-%d", os.Getpid(), tokenSeq.Add(1)),
	}
	var proxyURLs, peerURLs [Members]string
	clusterPeers := make([]string, Members)
	for i := range Members {
		name := fmt.Sprintf("m%d", i)
		clientURL := urls[3*i]
		peerURL := urls[3*i+1]
		proxyURL := urls[3*i+2]
		c.Endpoints[i] = clientURL
		c.members[i] = member{
			name:      name,
			clientURL: clientURL,
			peerURL:   peerURL,
			proxyURL:  proxyURL,
			dataDir:   filepath.Join(rootDir, name),
			output:    &syncBuffer{},
		}
		proxyURLs[i] = proxyURL
		peerURLs[i] = peerURL
		clusterPeers[i] = name + "=" + proxyURL
	}
	c.initialCluster = strings.Join(clusterPeers, ",")

	proxy, err := newPeerProxy(proxyURLs, peerURLs)
	if err != nil {
		_ = os.RemoveAll(rootDir)
		_ = portLock.Close()
		return nil, err
	}
	c.proxy = proxy

	for i := range Members {
		cmd := exec.Command(c.bin, c.memberArgs(i)...)
		cmd.Stdout = c.members[i].output
		cmd.Stderr = c.members[i].output
		if err := cmd.Start(); err != nil {
			c.Stop()
			return nil, fmt.Errorf("start %s (member %d): %w", c.bin, i, err)
		}
		c.members[i].cmd = cmd
	}

	deadline := time.Now().Add(30 * time.Second)
	errs := make([]error, Members)
	var wg sync.WaitGroup
	for i := range Members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.waitMemberReady(i, deadline); err != nil {
				errs[i] = fmt.Errorf("%w\n%s", err, c.members[i].output.String())
			}
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		c.Stop()
		return nil, err
	}

	idCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	ids, err := c.readMemberIDs(idCtx)
	cancel()
	if err != nil {
		c.Stop()
		return nil, err
	}
	c.proxy.setMemberIDs(ids)
	return c, nil
}

func (c *Cluster) memberArgs(i int) []string {
	m := c.members[i]
	return []string{
		"--name", m.name,
		"--data-dir", m.dataDir,
		"--listen-client-urls", m.clientURL,
		"--advertise-client-urls", m.clientURL,
		"--listen-peer-urls", m.peerURL,
		"--initial-advertise-peer-urls", m.proxyURL,
		"--initial-cluster", c.initialCluster,
		"--initial-cluster-state", "new",
		"--initial-cluster-token", c.token,
		"--unsafe-no-fsync",
		"--log-level", "warn",
	}
}

// Client returns a new client configured with all member endpoints. The caller
// closes it. The client does not log: tests provoke rejected requests on
// purpose, and the etcd client would report each of them.
func (c *Cluster) Client() (*clientv3.Client, error) {
	return clientv3.New(clientv3.Config{
		Endpoints:   slices.Clone(c.Endpoints),
		DialTimeout: 5 * time.Second,
		Logger:      zap.NewNop(),
	})
}

// Versions returns the version reported by each member, indexed by member.
func (c *Cluster) Versions(ctx context.Context) ([]string, error) {
	client, err := c.Client()
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	versions := make([]string, len(c.Endpoints))
	for i, ep := range c.Endpoints {
		status, err := client.Status(ctx, ep)
		if err != nil {
			return nil, fmt.Errorf("status %s: %w", ep, err)
		}
		versions[i] = status.Version
	}
	return versions, nil
}

// Leader returns the index of the current Raft leader.
func (c *Cluster) Leader(ctx context.Context) (int, error) {
	client, err := c.Client()
	if err != nil {
		return -1, err
	}
	defer func() { _ = client.Close() }()

	subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var (
		mu     sync.Mutex
		leader = -1
		errs   = make([]error, len(c.Endpoints))
		wg     sync.WaitGroup
	)
	for i, ep := range c.Endpoints {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, err := client.Status(subCtx, ep)
			if err != nil {
				errs[i] = fmt.Errorf("member %d (%s): %w", i, ep, err)
				return
			}
			if status.Leader == 0 {
				errs[i] = fmt.Errorf("member %d (%s): no leader elected", i, ep)
				return
			}
			if status.Header.MemberId == status.Leader {
				mu.Lock()
				if leader < 0 {
					leader = i
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if leader >= 0 {
		return leader, nil
	}
	if err := errors.Join(errs...); err != nil {
		return -1, err
	}
	return -1, errors.New("no current leader reported by cluster")
}

// StopMember sends SIGKILL to member i and keeps its data directory.
func (c *Cluster) StopMember(i int) error {
	if i < 0 || i >= Members {
		return fmt.Errorf("member index %d out of range [0, %d)", i, Members)
	}
	c.mu.Lock()
	cmd := c.members[i].cmd
	if cmd == nil || cmd.Process == nil {
		c.mu.Unlock()
		return fmt.Errorf("member %d is not running", i)
	}
	c.members[i].cmd = nil
	c.mu.Unlock()

	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		_ = cmd.Wait()
		return fmt.Errorf("kill member %d: %w", i, err)
	}
	_ = cmd.Wait()
	return nil
}

// PauseMember sends SIGSTOP to member i.
func (c *Cluster) PauseMember(i int) error {
	return c.signalMember(i, syscall.SIGSTOP)
}

// ResumeMember sends SIGCONT to member i.
func (c *Cluster) ResumeMember(i int) error {
	return c.signalMember(i, syscall.SIGCONT)
}

func (c *Cluster) signalMember(i int, sig syscall.Signal) error {
	if i < 0 || i >= Members {
		return fmt.Errorf("member index %d out of range [0, %d)", i, Members)
	}
	c.mu.Lock()
	cmd := c.members[i].cmd
	c.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return fmt.Errorf("member %d is not running", i)
	}
	if err := cmd.Process.Signal(sig); err != nil {
		return fmt.Errorf("signal %s to member %d: %w", sig, i, err)
	}
	return nil
}

// StartMember restarts member i on its existing data directory and URLs and
// waits until it serves a linearizable read on its own endpoint.
func (c *Cluster) StartMember(i int) error {
	if i < 0 || i >= Members {
		return fmt.Errorf("member index %d out of range [0, %d)", i, Members)
	}
	c.mu.Lock()
	if c.members[i].cmd != nil {
		c.mu.Unlock()
		return fmt.Errorf("member %d is already running", i)
	}
	c.members[i].output.Reset()
	cmd := exec.Command(c.bin, c.memberArgs(i)...)
	cmd.Stdout = c.members[i].output
	cmd.Stderr = c.members[i].output
	if err := cmd.Start(); err != nil {
		c.mu.Unlock()
		return fmt.Errorf("start member %d: %w", i, err)
	}
	c.members[i].cmd = cmd
	c.mu.Unlock()

	if err := c.waitMemberReady(i, time.Now().Add(30*time.Second)); err != nil {
		_ = c.StopMember(i)
		return fmt.Errorf("%w\n%s", err, c.members[i].output.String())
	}
	return nil
}

// IsolateMember cuts peer traffic between member i and both other members.
func (c *Cluster) IsolateMember(i int) {
	c.proxy.isolateMember(i)
}

// HealPartition removes every peer cut and aborts held or stalled peer requests.
func (c *Cluster) HealPartition() {
	c.proxy.healPartition()
}

// MemberRevision returns the revision currently held by member i via a serializable read.
func (c *Cluster) MemberRevision(ctx context.Context, i int) (int64, error) {
	if i < 0 || i >= Members {
		return 0, fmt.Errorf("member index %d out of range [0, %d)", i, Members)
	}
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{c.Endpoints[i]},
		DialTimeout: 5 * time.Second,
		Logger:      zap.NewNop(),
	})
	if err != nil {
		return 0, err
	}
	defer func() { _ = client.Close() }()
	resp, err := client.Get(ctx, "\x00", clientv3.WithSerializable(), clientv3.WithCountOnly())
	if err != nil {
		return 0, err
	}
	return resp.Header.Revision, nil
}

// Stop kills all running members, closes the peer proxies, and removes the cluster's data directories.
func (c *Cluster) Stop() {
	c.mu.Lock()
	cmds := make([]*exec.Cmd, Members)
	for i := range c.members {
		cmds[i] = c.members[i].cmd
		c.members[i].cmd = nil
	}
	proxy := c.proxy
	c.proxy = nil
	portLock := c.portLock
	c.portLock = nil
	c.mu.Unlock()

	for _, cmd := range cmds {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}
	if proxy != nil {
		proxy.close()
	}
	_ = os.RemoveAll(c.rootDir)
	if portLock != nil {
		_ = portLock.Close()
	}
}

func (c *Cluster) readMemberIDs(ctx context.Context) ([Members]uint64, error) {
	var ids [Members]uint64
	client, err := c.Client()
	if err != nil {
		return ids, err
	}
	defer func() { _ = client.Close() }()

	for {
		subCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		resp, err := client.MemberList(subCtx)
		cancel()
		if err == nil {
			clear(ids[:])
			for _, m := range resp.Members {
				for i := range Members {
					if m.Name == c.members[i].name {
						ids[i] = m.ID
					}
				}
			}
			if !slices.Contains(ids[:], 0) {
				return ids, nil
			}
			err = errors.New("member list missing member IDs")
		}
		if ctx.Err() != nil {
			return ids, fmt.Errorf("read member IDs: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (c *Cluster) waitMemberReady(i int, deadline time.Time) error {
	ep := c.members[i].clientURL
	var lastErr error
	for time.Now().Before(deadline) {
		lastErr = probeEndpoint(ep)
		if lastErr == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("etcd member %d at %s was not ready: %w", i, ep, lastErr)
}

func probeEndpoint(endpoint string) error {
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{endpoint},
		DialTimeout: 5 * time.Second,
		Logger:      zap.NewNop(),
	})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = client.Get(ctx, "\x00", clientv3.WithCountOnly())
	return err
}

const (
	portBlockMin   = 24000
	portBlockStep  = 10
	portBlockCount = (32000 - portBlockMin) / portBlockStep
)

// Ports sit below Linux's ephemeral range so port-0 allocations cannot steal
// them between probe and bind or during member restarts, and flock serializes
// clusters across parallel test processes.
func claimPortBlock(n int) ([]string, *os.File, error) {
	lockDir := filepath.Join(os.TempDir(), "nayutar-etcdtest-ports")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, nil, err
	}
	start := rand.IntN(portBlockCount)
	for offset := range portBlockCount {
		base := portBlockMin + ((start+offset)%portBlockCount)*portBlockStep
		lockPath := filepath.Join(lockDir, fmt.Sprintf("%d.lock", base))
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, nil, err
		}
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = f.Close()
			if errors.Is(err, syscall.EWOULDBLOCK) {
				continue
			}
			return nil, nil, err
		}
		urls, ok := probePortBlock(base, n)
		if !ok {
			_ = f.Close()
			continue
		}
		return urls, f, nil
	}
	return nil, nil, fmt.Errorf("no free etcd port block in [%d, %d]", portBlockMin, portBlockMin+(portBlockCount-1)*portBlockStep)
}

func probePortBlock(base, n int) ([]string, bool) {
	urls := make([]string, n)
	for i := range n {
		port := base + i
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return nil, false
		}
		_ = l.Close()
		urls[i] = fmt.Sprintf("http://127.0.0.1:%d", port)
	}
	return urls, true
}
