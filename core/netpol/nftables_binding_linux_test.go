//go:build linux && nftables

package netpol_test

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/netpol"
	"github.com/Kaikei-e/Nayutar/core/node/cni"
	"golang.org/x/sys/unix"
)

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func macInNetns(t *testing.T, netnsPath string) net.HardwareAddr {
	t.Helper()
	var mac net.HardwareAddr
	err := runInNetns(netnsPath, func() error {
		ifc, err := net.InterfaceByName("eth0")
		if err != nil {
			return err
		}
		mac = ifc.HardwareAddr
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return mac
}

func ipInNetns(t *testing.T, netnsPath string, args ...string) string {
	t.Helper()
	var out []byte
	err := runInNetns(netnsPath, func() error {
		var err error
		if out, err = exec.Command("ip", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("ip %s: %v (%s)", strings.Join(args, " "), err, out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func (l *lab) plainChain(name, bridge, subnet, gateway string) *cni.Chain {
	l.t.Helper()
	chain, err := cni.NewBridgeChain(cni.BridgeConfig{
		BinDir:      l.cniBin,
		NetworkName: "net-" + name,
		Bridge:      bridge,
		DataDir:     l.ipamDir,
		Ranges:      []cni.IPRange{{Subnet: subnet, Gateway: gateway}},
	})
	if err != nil {
		l.t.Fatalf("NewBridgeChain(%s): %v", name, err)
	}
	return chain
}

func sendARPReplyInNetns(t *testing.T, netnsPath string, srcMAC, dstMAC net.HardwareAddr, claimIP, targetIP netip.Addr) {
	t.Helper()
	err := runInNetns(netnsPath, func() error {
		ifc, err := net.InterfaceByName("eth0")
		if err != nil {
			return err
		}
		fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(htons(unix.ETH_P_ARP)))
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close(fd) }()
		frame := make([]byte, 0, 42)
		frame = append(frame, dstMAC...)
		frame = append(frame, srcMAC...)
		frame = append(frame, 0x08, 0x06, 0x00, 0x01, 0x08, 0x00, 6, 4, 0x00, 0x02)
		frame = append(frame, srcMAC...)
		c := claimIP.As4()
		frame = append(frame, c[:]...)
		frame = append(frame, dstMAC...)
		tg := targetIP.As4()
		frame = append(frame, tg[:]...)
		sa := &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ARP), Ifindex: ifc.Index, Halen: 6}
		copy(sa.Addr[:], dstMAC)
		return unix.Sendto(fd, frame, 0, sa)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func expectUDP(t *testing.T, label string, recv func(time.Duration) bool, want bool) {
	t.Helper()
	wait := 400 * time.Millisecond
	if want {
		wait = 2 * time.Second
	}
	if got := recv(wait); got != want {
		t.Errorf("%s: delivered=%v, want %v", label, got, want)
	}
}

func TestNftablesBridgeBindingBlocksL2Forgery(t *testing.T) {
	if reexecIsolated(t, "TestNftablesBridgeBindingBlocksL2Forgery") {
		return
	}
	l := newLab(t)
	alphaChain := l.chain("alpha", "10.60.0.0/24", "10.60.0.1")
	a1 := l.endpoint("alpha-1", alphaChain)
	a2 := l.endpoint("alpha-2", alphaChain)
	b1 := l.endpoint("beta-1", l.chain("beta", "10.61.0.0/24", "10.61.0.1"))
	recvA2 := listenUDPInNetns(t, a2.netnsPath, a2.ip, 8080)
	recvB1 := listenUDPInNetns(t, b1.netnsPath, b1.ip, 8080)

	l.apply(
		netpol.TenantPolicy{Tenant: "alpha", IntraTenant: netpol.IntraTenantDeny, CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24")},
			Rules: []netpol.Rule{udpRule(10, netpol.DirectionIngress, "beta", 8080), udpRule(11, netpol.DirectionEgress, "beta", 8080)}},
		netpol.TenantPolicy{Tenant: "beta", CIDRs: []netpol.Prefix{mustCIDR(t, "10.61.0.0/24")},
			Rules: []netpol.Rule{udpRule(10, netpol.DirectionEgress, "alpha", 8080), udpRule(11, netpol.DirectionIngress, "alpha", 8080)}},
	)

	t.Run("spoofed beta source inside the deny bridge", func(t *testing.T) {
		if err := sendUDPInNetns(b1.netnsPath, b1.ip, a2.ip, 8080); err != nil {
			t.Fatal(err)
		}
		expectUDP(t, "control: beta-1 -> alpha-2 routed", recvA2, true)
		if err := sendUDPInNetns(a1.netnsPath, a1.ip, a2.ip, 8080); err != nil {
			t.Fatal(err)
		}
		expectUDP(t, "control: alpha-1 -> alpha-2 own source is denied", recvA2, false)
		addrInNetns(t, a1.netnsPath, "10.61.0.99/32")
		if err := sendUDPInNetns(a1.netnsPath, netip.MustParseAddr("10.61.0.99"), a2.ip, 8080); err != nil {
			t.Fatal(err)
		}
		expectUDP(t, "alpha-1 forging a beta source to alpha-2", recvA2, false)
	})

	t.Run("forged reply into a routed flow", func(t *testing.T) {
		pc := listenUDPInNetnsConn(t, a1.netnsPath, a1.ip, 5555)
		if _, err := pc.WriteTo([]byte("hi"), &net.UDPAddr{IP: b1.ip.AsSlice(), Port: 8080}); err != nil {
			t.Fatal(err)
		}
		expectUDP(t, "control: alpha-1:5555 -> beta-1:8080 routed", recvB1, true)
		got := make(chan bool, 1)
		go func() {
			buf := make([]byte, 64)
			_ = pc.SetReadDeadline(time.Now().Add(1500 * time.Millisecond))
			n, _, err := pc.ReadFrom(buf)
			got <- err == nil && n > 0
		}()
		addrInNetns(t, a2.netnsPath, b1.ip.String()+"/32")
		err := runInNetns(a2.netnsPath, func() error {
			fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				return err
			}
			defer func() { _ = unix.Close(fd) }()
			if err := unix.Bind(fd, &unix.SockaddrInet4{Port: 8080, Addr: b1.ip.As4()}); err != nil {
				return err
			}
			return unix.Sendto(fd, []byte("forged"), 0, &unix.SockaddrInet4{Port: 5555, Addr: a1.ip.As4()})
		})
		if err != nil {
			t.Fatal(err)
		}
		if <-got {
			t.Error("alpha-2 forging beta-1:8080 -> alpha-1:5555 reached alpha-1 through the routed flow's conntrack entry")
		}
	})

	t.Run("ARP poisoned gateway", func(t *testing.T) {
		recvStolen := listenUDPInNetns(t, a2.netnsPath, b1.ip, 8080)
		if err := sendUDPInNetns(a1.netnsPath, a1.ip, b1.ip, 8080); err != nil {
			t.Fatal(err)
		}
		expectUDP(t, "control: alpha-1 -> beta-1 routed", recvB1, true)
		a1MAC, a2MAC := macInNetns(t, a1.netnsPath), macInNetns(t, a2.netnsPath)
		time.Sleep(1100 * time.Millisecond)
		sendARPReplyInNetns(t, a2.netnsPath, a2MAC, a1MAC, netip.MustParseAddr("10.60.0.1"), a1.ip)
		time.Sleep(100 * time.Millisecond)
		if neigh := ipInNetns(t, a1.netnsPath, "neigh", "show", "10.60.0.1"); !strings.Contains(neigh, a2MAC.String()) {
			t.Fatalf("the poisoning did not take effect, so the test proves nothing: %q", neigh)
		}
		if err := sendUDPInNetns(a1.netnsPath, a1.ip, b1.ip, 8080); err != nil {
			t.Fatal(err)
		}
		expectUDP(t, "alpha-1's datagram for beta-1 lands on alpha-2", recvStolen, false)
		expectUDP(t, "beta-1 does not get it either", recvB1, false)
	})
}

func listenUDPInNetnsConn(t *testing.T, netnsPath string, ip netip.Addr, port int) net.PacketConn {
	t.Helper()
	var pc net.PacketConn
	err := runInNetns(netnsPath, func() error {
		var err error
		pc, err = net.ListenPacket("udp4", netip.AddrPortFrom(ip, uint16(port)).String())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc
}

func TestNftablesBridgeNetfilterRefused(t *testing.T) {
	if reexecIsolated(t, "TestNftablesBridgeNetfilterRefused") {
		return
	}
	const sysctlDir = "/proc/sys/net/bridge"
	l := newLab(t)
	deny := netpol.TenantPolicy{Tenant: "alpha", IntraTenant: netpol.IntraTenantDeny, CIDRs: []netpol.Prefix{mustCIDR(t, "10.62.0.0/24")}}
	l.apply(deny)
	if _, err := os.Stat(sysctlDir); errors.Is(err, os.ErrNotExist) {
		t.Logf("%s is absent (br_netfilter not loaded): only the absent-means-allowed branch was exercised", sysctlDir)
		return
	}

	for _, name := range []string{"bridge-nf-call-iptables", "bridge-nf-call-ip6tables"} {
		path := sysctlDir + "/" + name
		if err := os.WriteFile(path, []byte("1\n"), 0o644); err != nil {
			t.Fatalf("set %s: %v", path, err)
		}
		_, err := l.enforcer.Apply(l.ctx, netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{deny}})
		if !errors.Is(err, netpol.ErrBridgeNetfilterEnabled) {
			t.Fatalf("Apply with %s=1: err=%v, want ErrBridgeNetfilterEnabled", name, err)
		}
		if err := os.WriteFile(path, []byte("0\n"), 0o644); err != nil {
			t.Fatalf("reset %s: %v", path, err)
		}
	}
	if _, err := l.enforcer.Apply(l.ctx, netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{deny}}); err != nil {
		t.Fatalf("Apply after resetting both sysctls to 0: %v", err)
	}
}

func TestNftablesHostInputEchoIsBoundToTheTenantGateway(t *testing.T) {
	if reexecIsolated(t, "TestNftablesHostInputEchoIsBoundToTheTenantGateway") {
		return
	}
	l := newLab(t)
	a1 := l.endpoint("alpha-1", l.chain("alpha", "10.80.0.0/24", "10.80.0.1"))
	l.endpoint("beta-1", l.chain("beta", "10.81.0.0/24", "10.81.0.1"))
	runIP(t, "addr", "add", "198.51.100.7/32", "dev", "lo")

	targets := map[string]netip.Addr{
		"own gateway":          netip.MustParseAddr("10.80.0.1"),
		"other tenant gateway": netip.MustParseAddr("10.81.0.1"),
		"non-bridge host addr": netip.MustParseAddr("198.51.100.7"),
	}
	for name, dst := range targets {
		if err := pingInNetns(a1.netnsPath, dst); err != nil {
			t.Fatalf("baseline before policy: ping %s: %v", name, err)
		}
	}
	l.apply(
		netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.80.0.0/24")}},
		netpol.TenantPolicy{Tenant: "beta", CIDRs: []netpol.Prefix{mustCIDR(t, "10.81.0.0/24")}},
	)
	if err := pingInNetns(a1.netnsPath, targets["own gateway"]); err != nil {
		t.Errorf("ping own gateway must work: %v", err)
	}
	for _, name := range []string{"other tenant gateway", "non-bridge host addr"} {
		if err := pingInNetns(a1.netnsPath, targets[name]); err == nil {
			t.Errorf("ping %s must not be answered", name)
		}
	}
}

func TestNftablesUnknownTenantBridgeCannotForward(t *testing.T) {
	if reexecIsolated(t, "TestNftablesUnknownTenantBridgeCannotForward") {
		return
	}
	l := newLab(t)
	a1 := l.endpoint("alpha-1", l.chain("alpha", "10.60.0.0/24", "10.60.0.1"))
	z1 := l.endpoint("stale-1", l.plainChain("stale", "nbr-zzz", "10.99.0.0/24", "10.99.0.1"))
	o1 := l.endpoint("other-1", l.plainChain("other", "brx0", "172.31.0.0/24", "172.31.0.1"))
	recvA1 := listenUDPInNetns(t, a1.netnsPath, a1.ip, 8080)
	recvO1 := listenUDPInNetns(t, o1.netnsPath, o1.ip, 8080)
	recvZ1 := listenUDPInNetns(t, z1.netnsPath, z1.ip, 8080)

	alpha := netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24")}, Rules: []netpol.Rule{
		{Order: 12, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustCIDR(t, "10.99.0.0/24"), Protocol: netpol.RuleProtoUDP, HasPorts: true, PortMin: 8080, PortMax: 8080},
		{Order: 13, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustCIDR(t, "172.31.0.0/24"), Protocol: netpol.RuleProtoUDP, HasPorts: true, PortMin: 8080, PortMax: 8080},
		{Order: 10, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustCIDR(t, "10.99.0.0/24"), Protocol: netpol.RuleProtoUDP, HasPorts: true, PortMin: 8080, PortMax: 8080},
		{Order: 11, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustCIDR(t, "172.31.0.0/24"), Protocol: netpol.RuleProtoUDP, HasPorts: true, PortMin: 8080, PortMax: 8080},
	}}
	l.apply(alpha)

	if err := sendUDPInNetns(o1.netnsPath, o1.ip, a1.ip, 8080); err != nil {
		t.Fatal(err)
	}
	expectUDP(t, "control: untenanted non-nbr bridge allowed by a CIDR rule", recvA1, true)
	if err := sendUDPInNetns(z1.netnsPath, z1.ip, a1.ip, 8080); err != nil {
		t.Fatal(err)
	}
	expectUDP(t, "container on the stale bridge nbr-zzz allowed by a CIDR rule", recvA1, false)
	if err := sendUDPInNetns(a1.netnsPath, a1.ip, o1.ip, 8080); err != nil {
		t.Fatal(err)
	}
	expectUDP(t, "control: tenant -> untenanted non-nbr bridge allowed by a CIDR rule", recvO1, true)
	if err := sendUDPInNetns(a1.netnsPath, a1.ip, z1.ip, 8080); err != nil {
		t.Fatal(err)
	}
	expectUDP(t, "tenant -> container on the stale bridge nbr-zzz allowed by a CIDR rule", recvZ1, false)
}

func TestNftablesDeliveryIsBoundToTheTenantBridge(t *testing.T) {
	if reexecIsolated(t, "TestNftablesDeliveryIsBoundToTheTenantBridge") {
		return
	}
	l := newLab(t)
	a1 := l.endpoint("alpha-1", l.chain("alpha", "10.60.0.0/24", "10.60.0.1"))
	ext := l.endpoint("ext-1", l.plainChain("ext", "ext0", "192.0.2.0/24", "192.0.2.1"))
	o1 := l.endpoint("other-1", l.plainChain("other", "brx0", "172.31.0.0/24", "172.31.0.1"))
	recvA1 := listenUDPInNetns(t, a1.netnsPath, a1.ip, 8080)
	const alphaAddr = "10.60.0.50"
	addrInNetns(t, ext.netnsPath, alphaAddr+"/32")
	recvExt := listenUDPInNetns(t, ext.netnsPath, netip.MustParseAddr(alphaAddr), 8080)
	runIP(t, "route", "add", alphaAddr+"/32", "dev", "ext0")

	l.apply(netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24")}, Rules: []netpol.Rule{
		{Order: 10, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustCIDR(t, "172.31.0.0/24"), Protocol: netpol.RuleProtoUDP, HasPorts: true, PortMin: 8080, PortMax: 8080},
	}})
	if err := sendUDPInNetns(o1.netnsPath, o1.ip, a1.ip, 8080); err != nil {
		t.Fatal(err)
	}
	expectUDP(t, "control: other-1 -> alpha-1 out of the tenant bridge", recvA1, true)
	if err := sendUDPInNetns(o1.netnsPath, o1.ip, netip.MustParseAddr(alphaAddr), 8080); err != nil {
		t.Fatal(err)
	}
	expectUDP(t, "datagram for a tenant address routed out of ext0", recvExt, false)
}

func TestNftablesUnrelatedBridgeStaysUntouchedByTheBridgeTable(t *testing.T) {
	if reexecIsolated(t, "TestNftablesUnrelatedBridgeStaysUntouchedByTheBridgeTable") {
		return
	}
	l := newLab(t)
	l.endpoint("alpha-1", l.chain("alpha", "10.60.0.0/24", "10.60.0.1"))
	o1 := l.endpoint("other-1", l.plainChain("other", "brx0", "172.31.0.0/24", "172.31.0.1"))
	o2 := l.endpoint("other-2", l.plainChain("other", "brx0", "172.31.0.0/24", "172.31.0.1"))
	defer startEchoServerInNetns(t, o2.netnsPath, o2.ip, 8080)()

	l.apply(netpol.TenantPolicy{Tenant: "alpha", IntraTenant: netpol.IntraTenantDeny, CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24")}})
	if !strings.Contains(l.listTables(), "table bridge nayutar") {
		t.Fatal("bridge table must be installed for this test to mean anything")
	}
	expectDelivered(t, "tcp on the unrelated bridge", o1, o1.ip, o2.ip, 8080)
	if err := pingInNetns(o1.netnsPath, o2.ip); err != nil {
		t.Errorf("ping on the unrelated bridge: %v", err)
	}
}

func TestNftablesNonTenantInterfaceCannotInjectTenantSource(t *testing.T) {
	if reexecIsolated(t, "TestNftablesNonTenantInterfaceCannotInjectTenantSource") {
		return
	}
	l := newLab(t)
	a1 := l.endpoint("alpha-1", l.chain("alpha", "10.70.0.0/24", "10.70.0.1"))
	g1 := l.endpoint("gamma-1", l.chain("gamma", "10.72.0.0/24", "10.72.0.1"))
	o1 := l.endpoint("other-1", l.plainChain("other", "brx0", "172.31.0.0/24", "172.31.0.1"))
	recvG1 := listenUDPInNetns(t, g1.netnsPath, g1.ip, 8080)

	l.apply(
		netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.70.0.0/24")}, Rules: []netpol.Rule{udpRule(10, netpol.DirectionEgress, "gamma", 8080)}},
		netpol.TenantPolicy{Tenant: "gamma", CIDRs: []netpol.Prefix{mustCIDR(t, "10.72.0.0/24")}, Rules: []netpol.Rule{udpRule(10, netpol.DirectionIngress, "alpha", 8080)}},
	)
	if err := sendUDPInNetns(a1.netnsPath, a1.ip, g1.ip, 8080); err != nil {
		t.Fatal(err)
	}
	expectUDP(t, "control: alpha-1 -> gamma-1 allowed", recvG1, true)
	if err := sendUDPInNetns(o1.netnsPath, o1.ip, g1.ip, 8080); err != nil {
		t.Fatal(err)
	}
	expectUDP(t, "control: non-tenant source has no allow", recvG1, false)
	spoof := netip.MustParseAddr("10.70.0.99")
	addrInNetns(t, o1.netnsPath, spoof.String()+"/32")
	if err := sendUDPInNetns(o1.netnsPath, spoof, g1.ip, 8080); err != nil {
		t.Fatal(err)
	}
	expectUDP(t, "brx0 namespace forging an alpha source", recvG1, false)
}

func TestNftablesFamilyWithoutCIDRCannotForward(t *testing.T) {
	if reexecIsolated(t, "TestNftablesFamilyWithoutCIDRCannotForward") {
		return
	}
	l := newLab(t)
	alphaBr, betaBr := netpol.TenantBridgeName("alpha"), netpol.TenantBridgeName("beta")
	a1 := l.endpoint("alpha-1", l.chain("alpha", "10.60.0.0/24", "10.60.0.1"))
	b1 := l.endpoint("beta-1", l.chain("beta", "10.61.0.0/24", "10.61.0.1"))
	o1 := l.endpoint("other-1", l.plainChain("other", "brx0", "172.31.0.0/24", "172.31.0.1"))

	runIP(t, "-6", "addr", "add", "fd00:aa::1/64", "dev", alphaBr, "nodad")
	runIP(t, "-6", "addr", "add", "fd00:bb::1/64", "dev", betaBr, "nodad")
	runIP(t, "-6", "addr", "add", "fd00:ab::1/64", "dev", "brx0", "nodad")
	a1v6, b1v6, o1v6 := netip.MustParseAddr("fd00:aa::11"), netip.MustParseAddr("fd00:bb::11"), netip.MustParseAddr("fd00:ab::11")
	for ep, v := range map[endpoint]struct{ addr, gw string }{
		a1: {"fd00:aa::11/64", "fd00:aa::1"}, b1: {"fd00:bb::11/64", "fd00:bb::1"}, o1: {"fd00:ab::11/64", "fd00:ab::1"},
	} {
		addrInNetns(t, ep.netnsPath, v.addr)
		ipInNetns(t, ep.netnsPath, "-6", "route", "add", "default", "via", v.gw, "dev", "eth0")
	}
	defer startEchoServerInNetns(t, b1.netnsPath, b1v6, 8080)()
	// MLD reports must reach the bridge before its multicast snooping floods neighbour solicitations
	time.Sleep(1500 * time.Millisecond)
	expectDelivered(t, "baseline: IPv6 routes between bridges before the policy", o1, o1v6, b1v6, 8080)

	l.apply(
		netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24")}},
		netpol.TenantPolicy{Tenant: "beta", CIDRs: []netpol.Prefix{mustCIDR(t, "fd00:bb::/64")}, Rules: []netpol.Rule{
			{Order: 10, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustCIDR(t, "fd00:aa::/64"), Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 8080, PortMax: 8080},
			{Order: 11, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustCIDR(t, "fd00:ab::/64"), Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 8080, PortMax: 8080},
		}},
	)
	expectDelivered(t, "control: IPv6 from the untenanted bridge is allowed by a CIDR rule", o1, o1v6, b1v6, 8080)
	expectBlocked(t, "IPv6 from a tenant that has only IPv4 CIDRs", a1, a1v6, b1v6, 8080)
}

func TestNftablesHostReachesContainerNeighbourDiscovery(t *testing.T) {
	if reexecIsolated(t, "TestNftablesHostReachesContainerNeighbourDiscovery") {
		return
	}
	l := newLab(t)
	alphaBr := netpol.TenantBridgeName("alpha")
	a1 := l.endpoint("alpha-1", l.chain("alpha", "10.60.0.0/24", "10.60.0.1"))
	runIP(t, "-6", "addr", "add", "fd00:aa::1/64", "dev", alphaBr, "nodad")
	addrInNetns(t, a1.netnsPath, "fd00:aa::11/64")
	time.Sleep(1500 * time.Millisecond)

	l.apply(netpol.TenantPolicy{Tenant: "alpha", CIDRs: []netpol.Prefix{mustCIDR(t, "10.60.0.0/24"), mustCIDR(t, "fd00:aa::/64")}})
	runIP(t, "-6", "neigh", "flush", "dev", alphaBr)
	if out, err := exec.Command("ping", "-6", "-c1", "-W2", "-I", alphaBr, "fd00:aa::11").CombinedOutput(); err != nil {
		t.Fatalf("the host must learn a container's neighbour advertisement: %v (%s)", err, out)
	}
}
