//go:build linux && nftables

package netpol_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/netpol"
	"github.com/Kaikei-e/Nayutar/core/node/cni"
	"github.com/Kaikei-e/Nayutar/core/node/netns"
	"github.com/Kaikei-e/Nayutar/core/sim"
	"golang.org/x/sys/unix"
)

const isolatedNetnsEnv = "NAYUTAR_NFT_ISOLATED_NETNS"

func requireNftablesEnv(t *testing.T) (cniBinDir, nftBin string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatalf("nftables integration tests require root (euid=0), got euid=%d", os.Geteuid())
	}
	cniBinDir = os.Getenv("NAYUTAR_CNI_BIN_DIR")
	if cniBinDir == "" {
		t.Fatal("NAYUTAR_CNI_BIN_DIR must be set for -tags nftables tests")
	}
	nftBin = os.Getenv("NAYUTAR_NFT_BIN")
	if nftBin == "" {
		nftBin = netpol.DefaultNftBin
	}
	return cniBinDir, nftBin
}

func newSysctlDisk(t *testing.T) *sim.RootedDisk {
	t.Helper()
	d, err := sim.NewRootedDisk(netpol.SysctlNetDir)
	if err != nil {
		t.Fatalf("open %s: %v", netpol.SysctlNetDir, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func newNftRunner(t *testing.T, nftBin string) *netpol.ExecNftRunner {
	t.Helper()
	// Inside an unprivileged user namespace the real root owner is not mapped, so trust the file's own owner here.
	fi, err := os.Stat(nftBin)
	if err != nil {
		t.Fatalf("stat nft binary %q: %v", nftBin, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("no owner information for %q", nftBin)
	}
	runner, err := netpol.NewExecNftRunnerTrustingUID(nftBin, st.Uid)
	if err != nil {
		t.Fatalf("NewExecNftRunner(%q): %v", nftBin, err)
	}
	return runner
}

// reexecIsolated re-runs the calling test inside a fresh network namespace so host firewall state
// never interferes with, or is disturbed by, table inet nayutar. It reports whether the caller is the
// parent process and must return.
func reexecIsolated(t *testing.T, name string) bool {
	t.Helper()
	if os.Getenv(isolatedNetnsEnv) == "1" {
		return false
	}
	cmd := exec.Command(os.Args[0], "-test.v", "-test.run=^"+name+"$")
	cmd.Env = append(os.Environ(), isolatedNetnsEnv+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated netns suite %s failed: %v\n%s", name, err, out)
	}
	t.Logf("%s", out)
	return true
}

func TestNftablesDefaultDenyAndMutualAllow(t *testing.T) {
	cniBinDir, nftBin := requireNftablesEnv(t)

	if reexecIsolated(t, "TestNftablesDefaultDenyAndMutualAllow") {
		return
	}

	// Enable IPv4 forwarding inside the isolated router netns.
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o644); err != nil {
		t.Fatalf("enable ip_forward: %v", err)
	}

	// The policy refuses to apply while br_netfilter is active: force both sysctls to 0 when they exist.
	for _, name := range []string{"bridge-nf-call-iptables", "bridge-nf-call-ip6tables"} {
		writeSysctl(t, "/proc/sys/net/bridge/"+name, "0")
	}

	alphaCIDR, err := netpol.ParsePrefix("10.40.0.0/24")
	if err != nil {
		t.Fatalf("ParsePrefix alpha: %v", err)
	}
	betaCIDR, err := netpol.ParsePrefix("10.41.0.0/24")
	if err != nil {
		t.Fatalf("ParsePrefix beta: %v", err)
	}
	anyV4, err := netpol.ParsePrefix("0.0.0.0/0")
	if err != nil {
		t.Fatalf("ParsePrefix 0.0.0.0/0: %v", err)
	}

	netnsDir := filepath.Join(t.TempDir(), "netns")
	ipamDir := filepath.Join(t.TempDir(), "ipam")
	nsMgr, err := netns.NewManager(netnsDir)
	if err != nil {
		t.Fatalf("netns.NewManager: %v", err)
	}

	alphaChain, err := cni.NewBridgeChain(cni.BridgeConfig{
		BinDir:      cniBinDir,
		NetworkName: "net-alpha",
		Bridge:      netpol.TenantBridgeName("alpha"),
		DataDir:     ipamDir,
		Ranges:      []cni.IPRange{{Subnet: alphaCIDR.String(), Gateway: "10.40.0.1"}},
	})
	if err != nil {
		t.Fatalf("NewBridgeChain(alpha): %v", err)
	}
	betaChain, err := cni.NewBridgeChain(cni.BridgeConfig{
		BinDir:      cniBinDir,
		NetworkName: "net-beta",
		Bridge:      netpol.TenantBridgeName("beta"),
		DataDir:     ipamDir,
		Ranges:      []cni.IPRange{{Subnet: betaCIDR.String(), Gateway: "10.41.0.1"}},
	})
	if err != nil {
		t.Fatalf("NewBridgeChain(beta): %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	type endpoint struct {
		name      string
		netnsPath string
		ip        netip.Addr
	}
	setupEndpoint := func(name string, chain *cni.Chain) endpoint {
		nsPath, err := nsMgr.Create(ctx, name)
		if err != nil {
			t.Fatalf("Create netns %s: %v", name, err)
		}
		res0, err := chain.Add(ctx, 0, name, nsPath, nil)
		if err != nil {
			_ = nsMgr.Remove(context.Background(), name)
			t.Fatalf("CNI Add(0) %s: %v", name, err)
		}
		res1, err := chain.Add(ctx, 1, name, nsPath, res0)
		if err != nil {
			_ = chain.Del(context.Background(), 1, name, nsPath, res0)
			_ = chain.Del(context.Background(), 0, name, nsPath, res0)
			_ = nsMgr.Remove(context.Background(), name)
			t.Fatalf("CNI Add(1) %s: %v", name, err)
		}
		t.Cleanup(func() {
			cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cleanCancel()
			_ = chain.Del(cleanCtx, 1, name, nsPath, res1)
			_ = chain.Del(cleanCtx, 0, name, nsPath, res1)
			_ = nsMgr.Remove(cleanCtx, name)
		})

		parsed, err := cni.ParseResult(res1)
		if err != nil || len(parsed.IPs) == 0 {
			t.Fatalf("ParseResult(%s): err=%v res=%s", name, err, string(res1))
		}
		pfx, err := netip.ParsePrefix(parsed.IPs[0].Address)
		if err != nil {
			t.Fatalf("ParsePrefix(%s %q): %v", name, parsed.IPs[0].Address, err)
		}
		return endpoint{name: name, netnsPath: nsPath, ip: pfx.Addr()}
	}

	alpha1 := setupEndpoint("alpha-1", alphaChain)
	alpha2 := setupEndpoint("alpha-2", alphaChain)
	beta1 := setupEndpoint("beta-1", betaChain)

	stopAlpha2 := startEchoServerInNetns(t, alpha2.netnsPath, alpha2.ip, 8080)
	defer stopAlpha2()
	stopBeta8080 := startEchoServerInNetns(t, beta1.netnsPath, beta1.ip, 8080)
	defer stopBeta8080()
	stopBeta9090 := startEchoServerInNetns(t, beta1.netnsPath, beta1.ip, 9090)
	defer stopBeta9090()

	runner := newNftRunner(t, nftBin)
	enforcer, err := netpol.NewEnforcer(runner, newSysctlDisk(t))
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanCancel()
		_ = enforcer.Teardown(cleanCtx)
	})

	// Baseline: verify inter-bridge L3 routing actually works before nftables policy is installed.
	if err := probeTCPInNetns(alpha1.netnsPath, alpha1.ip, beta1.ip, 8080, 2*time.Second); err != nil {
		t.Fatalf("baseline routing alpha-1 -> beta-1:8080 failed before policy apply: %v", err)
	}

	// 1. Zero rules: intra-tenant allow succeeds; cross-tenant is default-denied.
	defaultDenyBundle := netpol.PolicyBundle{
		Tenants: []netpol.TenantPolicy{
			{Tenant: "alpha", IntraTenant: netpol.IntraTenantAllow, CIDRs: []netpol.Prefix{alphaCIDR}},
			{Tenant: "beta", IntraTenant: netpol.IntraTenantAllow, CIDRs: []netpol.Prefix{betaCIDR}},
		},
	}
	if _, err := enforcer.Apply(ctx, defaultDenyBundle); err != nil {
		t.Fatalf("Apply(defaultDenyBundle): %v", err)
	}
	if err := probeTCPInNetns(alpha1.netnsPath, alpha1.ip, alpha2.ip, 8080, 2*time.Second); err != nil {
		t.Fatalf("intra-tenant alpha-1 -> alpha-2:8080 failed under intraTenant=allow: %v", err)
	}
	if err := probeTCPInNetns(alpha1.netnsPath, alpha1.ip, beta1.ip, 8080, 350*time.Millisecond); err == nil {
		t.Fatal("cross-tenant alpha-1 -> beta-1:8080 unexpectedly succeeded under default-deny")
	}

	// 2. Unilateral alpha 0.0.0.0/0 egress allow must STILL be blocked by beta's default-deny ingress.
	unilateralBundle := netpol.PolicyBundle{
		Tenants: []netpol.TenantPolicy{
			{
				Tenant:      "alpha",
				IntraTenant: netpol.IntraTenantAllow,
				CIDRs:       []netpol.Prefix{alphaCIDR},
				Rules: []netpol.Rule{
					{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: anyV4, Protocol: netpol.RuleProtoAny},
				},
			},
			{Tenant: "beta", IntraTenant: netpol.IntraTenantAllow, CIDRs: []netpol.Prefix{betaCIDR}},
		},
	}
	if _, err := enforcer.Apply(ctx, unilateralBundle); err != nil {
		t.Fatalf("Apply(unilateralBundle): %v", err)
	}
	if err := probeTCPInNetns(alpha1.netnsPath, alpha1.ip, beta1.ip, 8080, 350*time.Millisecond); err == nil {
		t.Fatal("cross-tenant alpha-1 -> beta-1:8080 unexpectedly succeeded with unilateral alpha 0.0.0.0/0 egress")
	}

	// 3. Mutual allow on TCP 8080: port 8080 succeeds; port 9090 remains blocked.
	mutualBundle := netpol.PolicyBundle{
		Tenants: []netpol.TenantPolicy{
			{
				Tenant:      "alpha",
				IntraTenant: netpol.IntraTenantAllow,
				CIDRs:       []netpol.Prefix{alphaCIDR},
				Rules: []netpol.Rule{
					{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "beta", Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 8080, PortMax: 8080},
				},
			},
			{
				Tenant:      "beta",
				IntraTenant: netpol.IntraTenantAllow,
				CIDRs:       []netpol.Prefix{betaCIDR},
				Rules: []netpol.Rule{
					{Order: 10, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindTenant, PeerTenant: "alpha", Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 8080, PortMax: 8080},
				},
			},
		},
	}
	if _, err := enforcer.Apply(ctx, mutualBundle); err != nil {
		t.Fatalf("Apply(mutualBundle): %v", err)
	}
	if err := probeTCPInNetns(alpha1.netnsPath, alpha1.ip, beta1.ip, 8080, 2*time.Second); err != nil {
		t.Fatalf("mutually allowed alpha-1 -> beta-1:8080 failed: %v", err)
	}
	if err := probeTCPInNetns(alpha1.netnsPath, alpha1.ip, beta1.ip, 9090, 350*time.Millisecond); err == nil {
		t.Fatal("unallowed port alpha-1 -> beta-1:9090 unexpectedly succeeded")
	}

	// 4. Atomic revocation of beta ingress immediately blocks new connections.
	revokedBundle := netpol.PolicyBundle{
		Tenants: []netpol.TenantPolicy{
			mutualBundle.Tenants[0],
			{Tenant: "beta", IntraTenant: netpol.IntraTenantAllow, CIDRs: []netpol.Prefix{betaCIDR}},
		},
	}
	if _, err := enforcer.Apply(ctx, revokedBundle); err != nil {
		t.Fatalf("Apply(revokedBundle): %v", err)
	}
	if err := probeTCPInNetns(alpha1.netnsPath, alpha1.ip, beta1.ip, 8080, 350*time.Millisecond); err == nil {
		t.Fatal("alpha-1 -> beta-1:8080 unexpectedly succeeded after revoking beta ingress")
	}

	// 5. Full nft(8) grammar matrix (0-tenant, dual-stack IPv4+IPv6, portless TCP/UDP/ICMP/ICMPv6, port ranges, intraTenant=deny).
	alphaV6, err := netpol.ParsePrefix("fd00:40::/64")
	if err != nil {
		t.Fatalf("ParsePrefix alphaV6: %v", err)
	}
	extV6, err := netpol.ParsePrefix("2001:db8:99::/64")
	if err != nil {
		t.Fatalf("ParsePrefix extV6: %v", err)
	}
	if _, err := enforcer.Apply(ctx, netpol.PolicyBundle{}); err != nil {
		t.Fatalf("Apply(empty 0-tenant bundle): %v", err)
	}
	grammarBundle := netpol.PolicyBundle{
		Tenants: []netpol.TenantPolicy{
			{
				Tenant:      "alpha",
				IntraTenant: netpol.IntraTenantDeny,
				CIDRs:       []netpol.Prefix{alphaCIDR, alphaV6},
				Rules: []netpol.Rule{
					{Order: 1, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "beta", Protocol: netpol.RuleProtoTCP},
					{Order: 2, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "beta", Protocol: netpol.RuleProtoUDP, HasPorts: true, PortMin: 5300, PortMax: 5310},
					{Order: 3, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: anyV4, Protocol: netpol.RuleProtoICMP},
					{Order: 4, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: extV6, Protocol: netpol.RuleProtoICMP},
				},
			},
			{
				Tenant:      "beta",
				IntraTenant: netpol.IntraTenantAllow,
				CIDRs:       []netpol.Prefix{betaCIDR},
				Rules: []netpol.Rule{
					{Order: 1, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindTenant, PeerTenant: "alpha", Protocol: netpol.RuleProtoUDP},
				},
			},
		},
	}
	if _, err := enforcer.Apply(ctx, grammarBundle); err != nil {
		t.Fatalf("Apply(grammarBundle): %v", err)
	}

	// 6. Idempotent Teardown cleanly deletes table inet nayutar.
	if err := enforcer.Teardown(ctx); err != nil {
		t.Fatalf("Teardown(1): %v", err)
	}
	out, err := exec.CommandContext(ctx, runner.BinPath(), "list", "tables").CombinedOutput()
	if err != nil {
		t.Fatalf("nft list tables: %v (%s)", err, string(out))
	}
	if strings.Contains(string(out), "table inet nayutar") {
		t.Fatalf("table inet nayutar still present after Teardown:\n%s", string(out))
	}
	if err := enforcer.Teardown(ctx); err != nil {
		t.Fatalf("Teardown(2, idempotent): %v", err)
	}
	if err := probeTCPInNetns(alpha1.netnsPath, alpha1.ip, beta1.ip, 8080, 2*time.Second); err != nil {
		t.Fatalf("post-teardown alpha-1 -> beta-1:8080 failed: %v", err)
	}
}

// runInNetns runs fn inside netnsPath on a disposable locked OS thread that is never unlocked,
// so the Go runtime terminates the thread rather than returning it to the thread pool.
func runInNetns(netnsPath string, fn func() error) error {
	errCh := make(chan error, 1)
	pid := os.Getpid()
	go func() {
		runtime.LockOSThread()
		if unix.Gettid() == pid {
			defer runtime.UnlockOSThread()
			innerCh := make(chan error, 1)
			go func() {
				runtime.LockOSThread()
				innerCh <- enterAndRun(netnsPath, fn)
			}()
			errCh <- <-innerCh
			return
		}
		errCh <- enterAndRun(netnsPath, fn)
	}()
	return <-errCh
}

func enterAndRun(netnsPath string, fn func() error) error {
	fd, err := unix.Open(netnsPath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open netns %s: %w", netnsPath, err)
	}
	if err := unix.Setns(fd, unix.CLONE_NEWNET); err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("setns %s: %w", netnsPath, err)
	}
	_ = unix.Close(fd)
	return fn()
}

func startEchoServerInNetns(t *testing.T, netnsPath string, ip netip.Addr, port int) func() {
	t.Helper()
	var ln net.Listener
	addr := net.JoinHostPort(ip.String(), strconv.Itoa(port))
	network := "tcp6"
	if ip.Is4() {
		network = "tcp4"
	}
	err := runInNetns(netnsPath, func() error {
		var lerr error
		ln, lerr = net.Listen(network, addr)
		return lerr
	})
	if err != nil {
		t.Fatalf("listen %s in %s: %v", addr, netnsPath, err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write([]byte("pong"))
			}(conn)
		}
	}()
	return func() {
		_ = ln.Close()
		<-done
	}
}

// probeTCPInNetns performs a direct blocking-free non-blocking socket connect + ping/pong
// entirely on the locked setns OS thread so no background goroutines run in the wrong netns.
func probeTCPInNetns(netnsPath string, srcIP, dstIP netip.Addr, port int, timeout time.Duration) error {
	return runInNetns(netnsPath, func() error {
		domain := unix.AF_INET6
		var srcAddr, dstAddr unix.Sockaddr = &unix.SockaddrInet6{Addr: srcIP.As16()}, &unix.SockaddrInet6{Port: port, Addr: dstIP.As16()}
		if dstIP.Is4() {
			domain = unix.AF_INET
			srcAddr, dstAddr = &unix.SockaddrInet4{Addr: srcIP.As4()}, &unix.SockaddrInet4{Port: port, Addr: dstIP.As4()}
		}
		fd, err := unix.Socket(domain, unix.SOCK_STREAM|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("socket: %w", err)
		}
		defer func() { _ = unix.Close(fd) }()

		if err := unix.Bind(fd, srcAddr); err != nil {
			return fmt.Errorf("bind %s: %w", srcIP, err)
		}

		err = unix.Connect(fd, dstAddr)
		if err != nil && !errors.Is(err, unix.EINPROGRESS) {
			return fmt.Errorf("connect %s:%d: %w", dstIP, port, err)
		}

		deadline := time.Now().Add(timeout)
		if errors.Is(err, unix.EINPROGRESS) {
			if err := pollFD(fd, unix.POLLOUT, deadline); err != nil {
				return fmt.Errorf("connect wait %s:%d: %w", dstIP, port, err)
			}
			soErr, gerr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ERROR)
			if gerr != nil {
				return fmt.Errorf("getsockopt SO_ERROR: %w", gerr)
			}
			if soErr != 0 {
				return fmt.Errorf("connect %s:%d: %w", dstIP, port, unix.Errno(soErr))
			}
		}

		msg := []byte("ping")
		for len(msg) > 0 {
			if err := pollFD(fd, unix.POLLOUT, deadline); err != nil {
				return fmt.Errorf("write wait: %w", err)
			}
			n, werr := unix.Write(fd, msg)
			if errors.Is(werr, unix.EAGAIN) || errors.Is(werr, unix.EINTR) {
				continue
			}
			if werr != nil {
				return fmt.Errorf("write: %w", werr)
			}
			msg = msg[n:]
		}

		reply := make([]byte, 4)
		got := 0
		for got < len(reply) {
			if err := pollFD(fd, unix.POLLIN, deadline); err != nil {
				return fmt.Errorf("read wait: %w", err)
			}
			n, rerr := unix.Read(fd, reply[got:])
			if errors.Is(rerr, unix.EAGAIN) || errors.Is(rerr, unix.EINTR) {
				continue
			}
			if rerr != nil {
				return fmt.Errorf("read: %w", rerr)
			}
			if n == 0 {
				return io.ErrUnexpectedEOF
			}
			got += n
		}
		if string(reply) != "pong" {
			return fmt.Errorf("unexpected reply %q", string(reply))
		}
		return nil
	})
}

func pollFD(fd int, events int16, deadline time.Time) error {
	for {
		rem := time.Until(deadline)
		if rem <= 0 {
			return context.DeadlineExceeded
		}
		ms := int(rem.Milliseconds())
		if ms < 1 {
			ms = 1
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: events}}
		n, err := unix.Poll(fds, ms)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return context.DeadlineExceeded
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 && fds[0].Revents&events == 0 {
			return errors.New("socket poll error")
		}
		return nil
	}
}

type endpoint struct {
	name      string
	netnsPath string
	ip        netip.Addr
}

type lab struct {
	t        *testing.T
	ctx      context.Context
	nsMgr    *netns.Manager
	cniBin   string
	ipamDir  string
	runner   *netpol.ExecNftRunner
	enforcer *netpol.Enforcer
}

func writeSysctl(t *testing.T, path, val string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		return
	}
	if err := os.WriteFile(path, []byte(val+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newLab(t *testing.T) *lab {
	t.Helper()
	cniBinDir, nftBin := requireNftablesEnv(t)
	writeSysctl(t, "/proc/sys/net/ipv4/ip_forward", "1")
	writeSysctl(t, "/proc/sys/net/ipv6/conf/all/forwarding", "1")
	writeSysctl(t, "/proc/sys/net/bridge/bridge-nf-call-iptables", "0")
	writeSysctl(t, "/proc/sys/net/bridge/bridge-nf-call-ip6tables", "0")

	nsMgr, err := netns.NewManager(filepath.Join(t.TempDir(), "netns"))
	if err != nil {
		t.Fatalf("netns.NewManager: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	runner := newNftRunner(t, nftBin)
	enforcer, err := netpol.NewEnforcer(runner, newSysctlDisk(t))
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanCancel()
		_ = enforcer.Teardown(cleanCtx)
	})
	return &lab{t: t, ctx: ctx, nsMgr: nsMgr, cniBin: cniBinDir, ipamDir: filepath.Join(t.TempDir(), "ipam"), runner: runner, enforcer: enforcer}
}

func (l *lab) chain(tenant, subnet, gateway string) *cni.Chain {
	l.t.Helper()
	chain, err := cni.NewBridgeChain(cni.BridgeConfig{
		BinDir:      l.cniBin,
		NetworkName: "net-" + tenant,
		Bridge:      netpol.TenantBridgeName(tenant),
		DataDir:     l.ipamDir,
		Ranges:      []cni.IPRange{{Subnet: subnet, Gateway: gateway}},
	})
	if err != nil {
		l.t.Fatalf("NewBridgeChain(%s): %v", tenant, err)
	}
	return chain
}

func (l *lab) endpoint(name string, chain *cni.Chain) endpoint {
	l.t.Helper()
	nsPath, err := l.nsMgr.Create(l.ctx, name)
	if err != nil {
		l.t.Fatalf("Create netns %s: %v", name, err)
	}
	res0, err := chain.Add(l.ctx, 0, name, nsPath, nil)
	if err != nil {
		_ = l.nsMgr.Remove(context.Background(), name)
		l.t.Fatalf("CNI Add(0) %s: %v", name, err)
	}
	res1, err := chain.Add(l.ctx, 1, name, nsPath, res0)
	if err != nil {
		_ = chain.Del(context.Background(), 0, name, nsPath, res0)
		_ = l.nsMgr.Remove(context.Background(), name)
		l.t.Fatalf("CNI Add(1) %s: %v", name, err)
	}
	l.t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanCancel()
		_ = chain.Del(cleanCtx, 1, name, nsPath, res1)
		_ = chain.Del(cleanCtx, 0, name, nsPath, res1)
		_ = l.nsMgr.Remove(cleanCtx, name)
	})
	parsed, err := cni.ParseResult(res1)
	if err != nil || len(parsed.IPs) == 0 {
		l.t.Fatalf("ParseResult(%s): err=%v res=%s", name, err, string(res1))
	}
	pfx, err := netip.ParsePrefix(parsed.IPs[0].Address)
	if err != nil {
		l.t.Fatalf("ParsePrefix(%s %q): %v", name, parsed.IPs[0].Address, err)
	}
	return endpoint{name: name, netnsPath: nsPath, ip: pfx.Addr()}
}

func (l *lab) apply(tenants ...netpol.TenantPolicy) {
	l.t.Helper()
	if _, err := l.enforcer.Apply(l.ctx, netpol.PolicyBundle{Tenants: tenants}); err != nil {
		l.t.Fatalf("Apply: %v", err)
	}
}

func (l *lab) listTables() string {
	l.t.Helper()
	out, err := exec.CommandContext(l.ctx, l.runner.BinPath(), "list", "tables").CombinedOutput()
	if err != nil {
		l.t.Fatalf("nft list tables: %v (%s)", err, out)
	}
	return string(out)
}

func runIP(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
		t.Fatalf("ip %s: %v (%s)", strings.Join(args, " "), err, out)
	}
}

func addrInNetns(t *testing.T, netnsPath string, cidr string) {
	t.Helper()
	args := []string{"addr", "add", cidr, "dev", "eth0"}
	if netip.MustParsePrefix(cidr).Addr().Is6() {
		args = append(args, "nodad")
	}
	err := runInNetns(netnsPath, func() error {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("ip %s: %v (%s)", strings.Join(args, " "), err, out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func pingInNetns(netnsPath string, dst netip.Addr) error {
	return runInNetns(netnsPath, func() error {
		out, err := exec.Command("ping", "-c1", "-W1", dst.String()).CombinedOutput()
		if err != nil {
			return fmt.Errorf("ping %s: %v (%s)", dst, err, out)
		}
		return nil
	})
}

func expectDelivered(t *testing.T, label string, from endpoint, src, dst netip.Addr, port int) {
	t.Helper()
	if err := probeTCPInNetns(from.netnsPath, src, dst, port, 2*time.Second); err != nil {
		t.Errorf("%s: want delivered, got %v", label, err)
	}
}

func expectBlocked(t *testing.T, label string, from endpoint, src, dst netip.Addr, port int) {
	t.Helper()
	if err := probeTCPInNetns(from.netnsPath, src, dst, port, 400*time.Millisecond); err == nil {
		t.Errorf("%s: want blocked, but the connection succeeded", label)
	}
}

func tcpRule(order int64, dir netpol.Direction, peer string, port int) netpol.Rule {
	return netpol.Rule{Order: order, Direction: dir, PeerKind: netpol.PeerKindTenant, PeerTenant: peer, Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: port, PortMax: port}
}

func TestNftablesIntraTenantDenyDualStack(t *testing.T) {
	if reexecIsolated(t, "TestNftablesIntraTenantDenyDualStack") {
		return
	}
	l := newLab(t)
	alphaBr := netpol.TenantBridgeName("alpha")
	alphaChain := l.chain("alpha", "10.60.0.0/24", "10.60.0.1")
	betaChain := l.chain("beta", "10.61.0.0/24", "10.61.0.1")
	a1 := l.endpoint("alpha-1", alphaChain)
	a2 := l.endpoint("alpha-2", alphaChain)
	b1 := l.endpoint("beta-1", betaChain)
	b2 := l.endpoint("beta-2", betaChain)

	runIP(t, "-6", "addr", "add", "fd00:60::1/64", "dev", alphaBr, "nodad")
	a1v6, a2v6 := netip.MustParseAddr("fd00:60::11"), netip.MustParseAddr("fd00:60::12")
	addrInNetns(t, a1.netnsPath, "fd00:60::11/64")
	addrInNetns(t, a2.netnsPath, "fd00:60::12/64")

	for _, port := range []int{8080, 9090} {
		defer startEchoServerInNetns(t, a2.netnsPath, a2.ip, port)()
		defer startEchoServerInNetns(t, a2.netnsPath, a2v6, port)()
	}
	defer startEchoServerInNetns(t, b2.netnsPath, b2.ip, 8080)()

	alphaCIDRs := []netpol.Prefix{mustCIDR(t, "10.60.0.0/24"), mustCIDR(t, "fd00:60::/64")}
	betaTenant := netpol.TenantPolicy{Tenant: "beta", IntraTenant: netpol.IntraTenantAllow, CIDRs: []netpol.Prefix{mustCIDR(t, "10.61.0.0/24")}}

	l.apply(
		netpol.TenantPolicy{Tenant: "alpha", IntraTenant: netpol.IntraTenantDeny, CIDRs: alphaCIDRs},
		betaTenant,
	)
	expectBlocked(t, "deny/no rules v4 8080", a1, a1.ip, a2.ip, 8080)
	expectBlocked(t, "deny/no rules v6 8080", a1, a1v6, a2v6, 8080)
	expectDelivered(t, "allow-mode tenant unaffected", b1, b1.ip, b2.ip, 8080)

	l.apply(
		netpol.TenantPolicy{
			Tenant: "alpha", IntraTenant: netpol.IntraTenantDeny, CIDRs: alphaCIDRs,
			Rules: []netpol.Rule{tcpRule(10, netpol.DirectionEgress, "alpha", 8080), tcpRule(11, netpol.DirectionIngress, "alpha", 8080)},
		},
		betaTenant,
	)
	expectDelivered(t, "deny/egress+ingress v4 8080", a1, a1.ip, a2.ip, 8080)
	expectDelivered(t, "deny/egress+ingress v6 8080", a1, a1v6, a2v6, 8080)
	expectBlocked(t, "deny/other port v4 9090", a1, a1.ip, a2.ip, 9090)
	expectBlocked(t, "deny/other port v6 9090", a1, a1v6, a2v6, 9090)
	expectDelivered(t, "allow-mode tenant unaffected after re-apply", b1, b1.ip, b2.ip, 8080)

	l.apply(
		netpol.TenantPolicy{
			Tenant: "alpha", IntraTenant: netpol.IntraTenantDeny, CIDRs: alphaCIDRs,
			Rules: []netpol.Rule{tcpRule(10, netpol.DirectionEgress, "alpha", 8080)},
		},
		betaTenant,
	)
	expectBlocked(t, "deny/egress only v4 8080", a1, a1.ip, a2.ip, 8080)
}

func mustCIDR(t *testing.T, s string) netpol.Prefix {
	t.Helper()
	p, err := netpol.ParsePrefix(s)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", s, err)
	}
	return p
}

func udpRule(order int64, dir netpol.Direction, peer string, port int) netpol.Rule {
	r := tcpRule(order, dir, peer, port)
	r.Protocol = netpol.RuleProtoUDP
	return r
}

// sendUDPInNetns sends one datagram from src, which need not be the interface's own address.
func sendUDPInNetns(netnsPath string, src, dst netip.Addr, port int) error {
	return runInNetns(netnsPath, func() error {
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(fd) }()
		if err := unix.Bind(fd, &unix.SockaddrInet4{Addr: src.As4()}); err != nil {
			return fmt.Errorf("bind %s: %w", src, err)
		}
		return unix.Sendto(fd, []byte("spoof"), 0, &unix.SockaddrInet4{Port: port, Addr: dst.As4()})
	})
}

func listenUDPInNetns(t *testing.T, netnsPath string, ip netip.Addr, port int) (received func(time.Duration) bool) {
	t.Helper()
	var pc net.PacketConn
	err := runInNetns(netnsPath, func() error {
		var lerr error
		pc, lerr = net.ListenPacket("udp4", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
		return lerr
	})
	if err != nil {
		t.Fatalf("listen udp %s:%d: %v", ip, port, err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	got := make(chan struct{}, 16)
	go func() {
		buf := make([]byte, 64)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
			got <- struct{}{}
		}
	}()
	return func(wait time.Duration) bool {
		select {
		case <-got:
			return true
		case <-time.After(wait):
			return false
		}
	}
}

func TestNftablesSourceSpoofIsBlocked(t *testing.T) {
	if reexecIsolated(t, "TestNftablesSourceSpoofIsBlocked") {
		return
	}
	l := newLab(t)
	a1 := l.endpoint("alpha-1", l.chain("alpha", "10.70.0.0/24", "10.70.0.1"))
	b1 := l.endpoint("beta-1", l.chain("beta", "10.71.0.0/24", "10.71.0.1"))
	c1 := l.endpoint("gamma-1", l.chain("gamma", "10.72.0.0/24", "10.72.0.1"))
	received := listenUDPInNetns(t, c1.netnsPath, c1.ip, 8080)

	l.apply(
		netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.70.0.0/24")}},
		netpol.TenantPolicy{Tenant: "beta", CIDRs: []netpol.Prefix{mustCIDR(t, "10.71.0.0/24")}, Rules: []netpol.Rule{udpRule(10, netpol.DirectionEgress, "gamma", 8080)}},
		netpol.TenantPolicy{Tenant: "gamma", CIDRs: []netpol.Prefix{mustCIDR(t, "10.72.0.0/24")}, Rules: []netpol.Rule{udpRule(10, netpol.DirectionIngress, "beta", 8080)}},
	)
	if err := sendUDPInNetns(b1.netnsPath, b1.ip, c1.ip, 8080); err != nil {
		t.Fatal(err)
	}
	if !received(2 * time.Second) {
		t.Fatal("beta -> gamma is mutually allowed but the datagram was not delivered")
	}
	if err := sendUDPInNetns(a1.netnsPath, a1.ip, c1.ip, 8080); err != nil {
		t.Fatal(err)
	}
	if received(500 * time.Millisecond) {
		t.Fatal("alpha -> gamma with its own address was delivered")
	}

	spoof := netip.MustParseAddr("10.71.0.99")
	addrInNetns(t, a1.netnsPath, "10.71.0.99/32")
	if err := sendUDPInNetns(a1.netnsPath, spoof, c1.ip, 8080); err != nil {
		t.Fatal(err)
	}
	if received(500 * time.Millisecond) {
		t.Fatal("alpha spoofing a beta source address reached gamma")
	}
}

func TestNftablesHostInputGuard(t *testing.T) {
	if reexecIsolated(t, "TestNftablesHostInputGuard") {
		return
	}
	l := newLab(t)
	a1 := l.endpoint("alpha-1", l.chain("alpha", "10.80.0.0/24", "10.80.0.1"))
	l.endpoint("beta-1", l.chain("beta", "10.81.0.0/24", "10.81.0.1"))

	ln, err := net.Listen("tcp4", "10.81.0.1:7000")
	if err != nil {
		t.Fatalf("listen on beta gateway: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_ = c.SetDeadline(time.Now().Add(2 * time.Second))
				buf := make([]byte, 4)
				if _, err := io.ReadFull(c, buf); err == nil {
					_, _ = c.Write([]byte("pong"))
				}
			}(c)
		}
	}()

	betaGW := netip.MustParseAddr("10.81.0.1")
	expectDelivered(t, "baseline: host service reachable before policy", a1, a1.ip, betaGW, 7000)

	l.apply(
		netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.80.0.0/24")}},
		netpol.TenantPolicy{Tenant: "beta", CIDRs: []netpol.Prefix{mustCIDR(t, "10.81.0.0/24")}},
	)
	expectBlocked(t, "container -> host address on another tenant's bridge", a1, a1.ip, betaGW, 7000)
	if err := pingInNetns(a1.netnsPath, netip.MustParseAddr("10.80.0.1")); err != nil {
		t.Errorf("ping own gateway must work: %v", err)
	}
}

func TestNftablesBridgeTableLifecycle(t *testing.T) {
	if reexecIsolated(t, "TestNftablesBridgeTableLifecycle") {
		return
	}
	l := newLab(t)
	deny := netpol.TenantPolicy{Tenant: "alpha", IntraTenant: netpol.IntraTenantDeny, CIDRs: []netpol.Prefix{mustCIDR(t, "10.90.0.0/24")}}
	allow := netpol.TenantPolicy{Tenant: "alpha", IntraTenant: netpol.IntraTenantAllow, CIDRs: []netpol.Prefix{mustCIDR(t, "10.90.0.0/24")}}

	l.apply(deny)
	if tables := l.listTables(); !strings.Contains(tables, "table bridge nayutar") || !strings.Contains(tables, "table inet nayutar") {
		t.Fatalf("deny bundle must define both tables:\n%s", tables)
	}
	l.apply(allow)
	if tables := l.listTables(); strings.Contains(tables, "table bridge nayutar") || !strings.Contains(tables, "table inet nayutar") {
		t.Fatalf("re-applying without a deny tenant must remove the bridge table and keep the inet table:\n%s", tables)
	}
	l.apply(deny)
	if err := l.enforcer.Teardown(l.ctx); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	if tables := l.listTables(); strings.Contains(tables, "nayutar") {
		t.Fatalf("Teardown must remove both tables:\n%s", tables)
	}
}
