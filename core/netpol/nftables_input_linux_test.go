//go:build linux && nftables

package netpol_test

import (
	"bytes"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/netpol"
	"golang.org/x/sys/unix"
)

const echoMarker = "REVIEW!"

func holdSpoofAddr(t *testing.T, ns string, addr netip.Addr) {
	t.Helper()
	ipInNetns(t, ns, "addr", "add", addr.String()+"/32", "dev", "lo")
	err := runInNetns(ns, func() error {
		return os.WriteFile("/proc/sys/net/ipv4/conf/all/arp_ignore", []byte("1\n"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func awaitEchoReply(t *testing.T, ns string, wait time.Duration) <-chan bool {
	t.Helper()
	got := make(chan bool, 1)
	ready := make(chan struct{})
	go func() {
		_ = runInNetns(ns, func() error {
			fd, err := unix.Socket(unix.AF_INET, unix.SOCK_RAW, unix.IPPROTO_ICMP)
			if err != nil {
				close(ready)
				got <- false
				return err
			}
			defer func() { _ = unix.Close(fd) }()
			tv := unix.NsecToTimeval(wait.Nanoseconds())
			_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv)
			close(ready)
			buf := make([]byte, 2048)
			for {
				n, _, err := unix.Recvfrom(fd, buf, 0)
				if err != nil {
					got <- false
					return nil
				}
				ihl := int(buf[0]&0x0f) * 4
				if n > ihl && buf[ihl] == 0 && bytes.Contains(buf[:n], []byte(echoMarker)) {
					got <- true
					return nil
				}
			}
		})
	}()
	<-ready
	return got
}

func spoofedPing(t *testing.T, ns string, src, dst netip.Addr) {
	t.Helper()
	_ = runInNetns(ns, func() error {
		_, _ = exec.Command("ping", "-c1", "-W1", "-I", src.String(), "-p", "52455649455721", dst.String()).CombinedOutput()
		return nil
	})
}

func echoReplyReaches(t *testing.T, sender, victim string, spoof, dst netip.Addr) bool {
	t.Helper()
	got := awaitEchoReply(t, victim, 2*time.Second)
	spoofedPing(t, sender, spoof, dst)
	return <-got
}

func TestNftablesInputEchoReflectionAcrossTenantsIsBlocked(t *testing.T) {
	if reexecIsolated(t, "TestNftablesInputEchoReflectionAcrossTenantsIsBlocked") {
		return
	}
	l := newLab(t)
	a1 := l.endpoint("alpha-1", l.chain("alpha", "10.60.0.0/24", "10.60.0.1"))
	b1 := l.endpoint("beta-1", l.chain("beta", "10.61.0.0/24", "10.61.0.1"))
	holdSpoofAddr(t, a1.netnsPath, b1.ip)
	gw := netip.MustParseAddr("10.60.0.1")

	if !echoReplyReaches(t, a1.netnsPath, b1.netnsPath, b1.ip, gw) {
		t.Fatal("baseline before the policy: the reflected echo reply must reach beta-1")
	}
	l.apply(
		netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24")}},
		netpol.TenantPolicy{Tenant: "beta", CIDRs: []netpol.Prefix{mustCIDR(t, "10.61.0.0/24")}},
	)
	if err := pingInNetns(a1.netnsPath, gw); err != nil {
		t.Errorf("control: a genuine ping to the own gateway must be answered: %v", err)
	}
	if echoReplyReaches(t, a1.netnsPath, b1.netnsPath, b1.ip, gw) {
		t.Error("an echo request forged with beta-1's source must not make the host answer beta-1")
	}
}

func TestNftablesInputForgedReplyCannotEnterHostConversation(t *testing.T) {
	if reexecIsolated(t, "TestNftablesInputForgedReplyCannotEnterHostConversation") {
		return
	}
	l := newLab(t)
	a1 := l.endpoint("alpha-1", l.chain("alpha", "10.60.0.0/24", "10.60.0.1"))
	b1 := l.endpoint("beta-1", l.chain("beta", "10.61.0.0/24", "10.61.0.1"))
	l.apply(
		netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24")}},
		netpol.TenantPolicy{Tenant: "beta", CIDRs: []netpol.Prefix{mustCIDR(t, "10.61.0.0/24")}},
	)
	holdSpoofAddr(t, a1.netnsPath, b1.ip)
	ipInNetns(t, a1.netnsPath, "route", "add", "10.61.0.1/32", "via", "10.60.0.1", "dev", "eth0")

	var pc net.PacketConn
	if err := runInNetns(b1.netnsPath, func() error {
		var lerr error
		pc, lerr = net.ListenPacket("udp4", net.JoinHostPort(b1.ip.String(), "9999"))
		return lerr
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 64)
		for {
			_, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo([]byte("genuine"), from)
		}
	}()

	conv, err := net.DialUDP("udp4", &net.UDPAddr{IP: net.IPv4(10, 61, 0, 1), Port: 40002}, &net.UDPAddr{IP: b1.ip.AsSlice(), Port: 9999})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conv.Close() })
	if _, err := conv.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = conv.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conv.Read(buf)
	if err != nil || string(buf[:n]) != "genuine" {
		t.Fatalf("control: the genuine reply from beta-1 must reach the host conversation, got %q (%v)", buf[:max(n, 0)], err)
	}
	err = runInNetns(a1.netnsPath, func() error {
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(fd) }()
		if err := unix.Bind(fd, &unix.SockaddrInet4{Port: 9999, Addr: b1.ip.As4()}); err != nil {
			return err
		}
		return unix.Sendto(fd, []byte("forged"), 0, &unix.SockaddrInet4{Port: 40002, Addr: [4]byte{10, 61, 0, 1}})
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = conv.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	if n, err := conv.Read(buf); err == nil {
		t.Errorf("a datagram forged from alpha-1 must not enter the host conversation, read %q", buf[:n])
	}
}

func TestNftablesHostReachesItsOwnGatewayOverLoopback(t *testing.T) {
	if reexecIsolated(t, "TestNftablesHostReachesItsOwnGatewayOverLoopback") {
		return
	}
	l := newLab(t)
	l.endpoint("alpha-1", l.chain("alpha", "10.60.0.0/24", "10.60.0.1"))
	runIP(t, "link", "set", "lo", "up")
	l.apply(netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24")}})
	ln, err := net.Listen("tcp4", "10.60.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	c, err := net.DialTimeout("tcp4", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("the host must reach its own gateway address over loopback: %v", err)
	}
	_ = c.Close()
}

func TestNftablesInputNonTenantInterfaceCannotReflectIntoTenant(t *testing.T) {
	if reexecIsolated(t, "TestNftablesInputNonTenantInterfaceCannotReflectIntoTenant") {
		return
	}
	l := newLab(t)
	a1 := l.endpoint("alpha-1", l.chain("alpha", "10.60.0.0/24", "10.60.0.1"))
	o1 := l.endpoint("other-1", l.plainChain("other", "brx0", "172.31.0.0/24", "172.31.0.1"))
	holdSpoofAddr(t, o1.netnsPath, a1.ip)
	gw := netip.MustParseAddr("172.31.0.1")

	if !echoReplyReaches(t, o1.netnsPath, a1.netnsPath, a1.ip, gw) {
		t.Fatal("baseline before the policy: the reflected echo reply must reach alpha-1")
	}
	l.apply(netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24")}})
	if err := pingInNetns(o1.netnsPath, gw); err != nil {
		t.Errorf("control: the same ping with its real source must be answered: %v", err)
	}
	if echoReplyReaches(t, o1.netnsPath, a1.netnsPath, a1.ip, gw) {
		t.Error("an echo request with alpha-1's source arriving on a non-tenant interface must not be answered into alpha-1")
	}
}

func waitLinkLocal(t *testing.T, show func() string) netip.Addr {
	t.Helper()
	for range 60 {
		for _, line := range strings.Split(show(), "\n") {
			if strings.Contains(line, "tentative") {
				continue
			}
			for _, f := range strings.Fields(line) {
				if p, err := netip.ParsePrefix(f); err == nil && strings.HasPrefix(f, "fe80::") {
					return p.Addr()
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("no usable link-local address")
	return netip.Addr{}
}

func hostLinkLocal(t *testing.T, dev string) netip.Addr {
	return waitLinkLocal(t, func() string {
		out, _ := exec.Command("ip", "-6", "-o", "addr", "show", "dev", dev, "scope", "link").CombinedOutput()
		return string(out)
	})
}

func nsLinkLocal(t *testing.T, ns string) netip.Addr {
	return waitLinkLocal(t, func() string {
		var out []byte
		_ = runInNetns(ns, func() error {
			out, _ = exec.Command("ip", "-6", "-o", "addr", "show", "dev", "eth0", "scope", "link").CombinedOutput()
			return nil
		})
		return string(out)
	})
}

func pingV6InNetns(ns, dst string) error {
	return runInNetns(ns, func() error {
		if out, err := exec.Command("ping", "-6", "-c1", "-W1", dst).CombinedOutput(); err != nil {
			return &pingError{dst: dst, out: string(out)}
		}
		return nil
	})
}

type pingError struct{ dst, out string }

func (e *pingError) Error() string { return "ping " + e.dst + ": " + strings.TrimSpace(e.out) }

func TestNftablesInputKeepsIPv6NeighbourDiscoveryAndGatewayPing(t *testing.T) {
	if reexecIsolated(t, "TestNftablesInputKeepsIPv6NeighbourDiscoveryAndGatewayPing") {
		return
	}
	l := newLab(t)
	alphaBr, betaBr := netpol.TenantBridgeName("alpha"), netpol.TenantBridgeName("beta")
	alphaChain := l.chain("alpha", "10.60.0.0/24", "10.60.0.1")
	a1 := l.endpoint("alpha-1", alphaChain)
	a2 := l.endpoint("alpha-2", alphaChain)
	b1 := l.endpoint("beta-1", l.chain("beta", "10.61.0.0/24", "10.61.0.1"))
	runIP(t, "-6", "addr", "add", "fd00:60::1/64", "dev", alphaBr, "nodad")
	runIP(t, "-6", "addr", "add", "fd00:61::1/64", "dev", betaBr, "nodad")
	a1v6, a2v6, b1v6 := netip.MustParseAddr("fd00:60::11"), netip.MustParseAddr("fd00:60::12"), netip.MustParseAddr("fd00:61::11")
	addrInNetns(t, a1.netnsPath, "fd00:60::11/64")
	addrInNetns(t, a2.netnsPath, "fd00:60::12/64")
	addrInNetns(t, b1.netnsPath, "fd00:61::11/64")
	alphaLL, betaLL := hostLinkLocal(t, alphaBr), hostLinkLocal(t, betaBr)
	for _, ep := range []endpoint{a1, a2} {
		nsLinkLocal(t, ep.netnsPath)
		ipInNetns(t, ep.netnsPath, "-6", "route", "add", "default", "via", alphaLL.String(), "dev", "eth0")
	}
	nsLinkLocal(t, b1.netnsPath)
	ipInNetns(t, b1.netnsPath, "-6", "route", "add", "default", "via", betaLL.String(), "dev", "eth0")
	defer startEchoServerInNetns(t, a2.netnsPath, a2v6, 8080)()
	defer startEchoServerInNetns(t, b1.netnsPath, b1v6, 8080)()

	l.apply(
		netpol.TenantPolicy{Tenant: "alpha", IntraTenant: netpol.IntraTenantDeny,
			CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24"), mustCIDR(t, "fd00:60::/64")},
			Rules: []netpol.Rule{tcpRule(10, netpol.DirectionEgress, "alpha", 8080), tcpRule(11, netpol.DirectionIngress, "alpha", 8080), tcpRule(12, netpol.DirectionEgress, "beta", 8080)}},
		netpol.TenantPolicy{Tenant: "beta",
			CIDRs: []netpol.Prefix{mustCIDR(t, "10.61.0.0/24"), mustCIDR(t, "fd00:61::/64")},
			Rules: []netpol.Rule{tcpRule(10, netpol.DirectionIngress, "alpha", 8080)}},
	)
	flush := func() {
		runIP(t, "-6", "neigh", "flush", "all")
		for _, ep := range []endpoint{a1, a2, b1} {
			ipInNetns(t, ep.netnsPath, "-6", "neigh", "flush", "all")
		}
	}
	flush()
	if err := pingV6InNetns(a1.netnsPath, "fd00:60::1"); err != nil {
		t.Errorf("IPv6 ping to the own gateway must work: %v", err)
	}
	if err := pingV6InNetns(a1.netnsPath, "fd00:61::1"); err == nil {
		t.Error("IPv6 ping to another tenant's gateway must not be answered")
	}
	flush()
	expectDelivered(t, "IPv6 TCP inside a deny tenant after a neighbour flush", a1, a1v6, a2v6, 8080)
	flush()
	expectDelivered(t, "IPv6 TCP to another tenant via the gateway after a neighbour flush", a1, a1v6, b1v6, 8080)
}

func TestNftablesBridgeDropsRogueRouterAdvertisement(t *testing.T) {
	if reexecIsolated(t, "TestNftablesBridgeDropsRogueRouterAdvertisement") {
		return
	}
	l := newLab(t)
	alphaChain := l.chain("alpha", "10.60.0.0/24", "10.60.0.1")
	a1 := l.endpoint("alpha-1", alphaChain)
	a2 := l.endpoint("alpha-2", alphaChain)
	ll2 := nsLinkLocal(t, a2.netnsPath)
	nsLinkLocal(t, a1.netnsPath)
	hasRARoute := func() bool {
		return strings.Contains(ipInNetns(t, a1.netnsPath, "-6", "route", "show", "default"), "proto ra")
	}
	sendRA := func() {
		err := runInNetns(a2.netnsPath, func() error {
			ifc, err := net.InterfaceByName("eth0")
			if err != nil {
				return err
			}
			fd, err := unix.Socket(unix.AF_INET6, unix.SOCK_RAW, unix.IPPROTO_ICMPV6)
			if err != nil {
				return err
			}
			defer func() { _ = unix.Close(fd) }()
			_ = unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_HOPS, 255)
			_ = unix.SetsockoptInt(fd, unix.IPPROTO_IPV6, unix.IPV6_MULTICAST_IF, ifc.Index)
			if err := unix.Bind(fd, &unix.SockaddrInet6{ZoneId: uint32(ifc.Index), Addr: ll2.As16()}); err != nil {
				return err
			}
			ra := []byte{134, 0, 0, 0, 64, 0, 0x07, 0x08, 0, 0, 0, 0, 0, 0, 0, 0}
			return unix.Sendto(fd, ra, 0, &unix.SockaddrInet6{ZoneId: uint32(ifc.Index), Addr: netip.MustParseAddr("ff02::1").As16()})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sendRA()
	installed := false
	for range 40 {
		if installed = hasRARoute(); installed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !installed {
		t.Fatal("control before the policy: a rogue router advertisement must install a default route")
	}
	ipInNetns(t, a1.netnsPath, "-6", "route", "del", "default")

	l.apply(netpol.TenantPolicy{Tenant: "alpha", IntraTenant: netpol.IntraTenantDeny,
		CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24"), mustCIDR(t, "fd00:60::/64")}})
	sendRA()
	time.Sleep(500 * time.Millisecond)
	if hasRARoute() {
		t.Error("a rogue router advertisement inside a deny tenant must not install a default route")
	}
}

func TestNftablesInputUnboundSourceCannotBounceOffHost(t *testing.T) {
	if reexecIsolated(t, "TestNftablesInputUnboundSourceCannotBounceOffHost") {
		return
	}
	l := newLab(t)
	a1 := l.endpoint("alpha-1", l.chain("alpha", "10.60.0.0/24", "10.60.0.1"))
	o1 := l.endpoint("other-1", l.plainChain("other", "brx0", "172.31.0.0/24", "172.31.0.1"))
	outside := netip.MustParseAddr("198.51.100.7")
	addrInNetns(t, o1.netnsPath, outside.String()+"/32")
	holdSpoofAddr(t, a1.netnsPath, outside)
	runIP(t, "route", "add", outside.String()+"/32", "dev", "brx0")
	gw := netip.MustParseAddr("10.60.0.1")

	if !echoReplyReaches(t, a1.netnsPath, o1.netnsPath, outside, gw) {
		t.Fatal("baseline before the policy: the reflected echo reply must reach the outside host")
	}
	l.apply(netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24")}})
	if err := pingInNetns(a1.netnsPath, gw); err != nil {
		t.Errorf("control: a genuine ping to the own gateway must be answered: %v", err)
	}
	if echoReplyReaches(t, a1.netnsPath, o1.netnsPath, outside, gw) {
		t.Error("an echo request whose source is outside the tenant's CIDRs must not be answered")
	}
}
