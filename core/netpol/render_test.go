package netpol_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/netpol"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/sim"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
)

func twoTenantTenants(t *testing.T, alphaMode netpol.IntraTenantMode) []netpol.TenantPolicy {
	t.Helper()
	return []netpol.TenantPolicy{
		{Tenant: "beta", CIDRs: []netpol.Prefix{mustPrefix(t, "10.41.0.0/24")}},
		{
			Tenant:      "alpha",
			IntraTenant: alphaMode,
			CIDRs:       []netpol.Prefix{mustPrefix(t, "fd00:40::/64"), mustPrefix(t, "10.40.0.0/24")},
			Rules: []netpol.Rule{
				{Order: 20, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindTenant, PeerTenant: "beta", Protocol: netpol.RuleProtoICMP},
				{Order: 10, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "beta", Protocol: netpol.RuleProtoTCP, HasPorts: true, PortMin: 8080, PortMax: 8080},
			},
		},
	}
}

func renderBundle(t *testing.T, tenants []netpol.TenantPolicy) string {
	t.Helper()
	cp, cerr := netpol.Compile(netpol.PolicyBundle{Tenants: tenants})
	if cerr != nil {
		t.Fatalf("Compile: %v", cerr)
	}
	return mustRender(t, cp)
}

func TestRenderNftGoldenWithDenyTenant(t *testing.T) {
	got := renderBundle(t, twoTenantTenants(t, netpol.IntraTenantDeny))
	alphaBr, betaBr := netpol.TenantBridgeName("alpha"), netpol.TenantBridgeName("beta")
	want := strings.NewReplacer("@A@", alphaBr, "@B@", betaBr).Replace(`table inet nayutar
delete table inet nayutar
table bridge nayutar
delete table bridge nayutar
table inet nayutar {
  chain guard_forward {
    type filter hook forward priority -10; policy accept;
    iifname "nbr-*" iifname != { "@A@", "@B@" } drop
    oifname "nbr-*" oifname != { "@A@", "@B@" } drop
    iifname "@A@" ip saddr != { 10.40.0.0/24 } drop
    iifname != "@A@" ip saddr { 10.40.0.0/24 } drop
    oifname != "@A@" ip daddr { 10.40.0.0/24 } drop
    iifname "@A@" ip6 saddr != { fd00:40::/64 } drop
    iifname != "@A@" ip6 saddr { fd00:40::/64 } drop
    oifname != "@A@" ip6 daddr { fd00:40::/64 } drop
    iifname "@B@" ip saddr != { 10.41.0.0/24 } drop
    iifname != "@B@" ip saddr { 10.41.0.0/24 } drop
    oifname != "@B@" ip daddr { 10.41.0.0/24 } drop
    iifname "@B@" meta nfproto ipv6 drop
  }
  chain guard_input {
    type filter hook input priority 0; policy accept;
    iifname "nbr-*" icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert, nd-router-solicit } accept
    iifname "@A@" ip saddr != { 10.40.0.0/24 } drop
    iifname "@A@" ip6 saddr != { fd00:40::/64 } drop
    iifname "@B@" ip saddr != { 10.41.0.0/24 } drop
    iifname "@B@" meta nfproto ipv6 drop
    iifname != { "@A@", "lo" } ip saddr { 10.40.0.0/24 } drop
    iifname != { "@A@", "lo" } ip6 saddr { fd00:40::/64 } drop
    iifname != { "@B@", "lo" } ip saddr { 10.41.0.0/24 } drop
    iifname "nbr-*" ct state { established, related } accept
    iifname "@A@" ip daddr { 10.40.0.0/24 } icmp type echo-request accept
    iifname "@A@" ip6 daddr { fd00:40::/64 } icmpv6 type echo-request accept
    iifname "@B@" ip daddr { 10.41.0.0/24 } icmp type echo-request accept
    iifname "nbr-*" drop
  }
  chain forward {
    type filter hook forward priority 0; policy drop;
    ct state invalid drop
    ct state { established, related } accept
    ip saddr 10.40.0.0/24 jump tenant_alpha
    ip6 saddr fd00:40::/64 jump tenant_alpha
    ip saddr 10.41.0.0/24 jump tenant_beta
    ip daddr 10.40.0.0/24 jump tenant_alpha_in
    ip6 daddr fd00:40::/64 jump tenant_alpha_in
    ip daddr 10.41.0.0/24 jump tenant_beta_in
    ip saddr 10.40.0.0/24 accept
    ip6 saddr fd00:40::/64 accept
    ip saddr 10.41.0.0/24 accept
  }
  chain tenant_alpha {
    ip daddr 10.41.0.0/24 tcp dport 8080 return
    drop
  }
  chain tenant_alpha_in {
    ip saddr 10.41.0.0/24 meta l4proto icmp accept
    drop
  }
  chain tenant_beta {
    ip daddr 10.41.0.0/24 accept
    drop
  }
  chain tenant_beta_in {
    drop
  }
}
table bridge nayutar {
  chain forward {
    type filter hook forward priority 0; policy accept;
    meta ibrname != { "@A@" } accept
    ether type arp accept
    icmpv6 type { nd-router-solicit, nd-router-advert } drop
    icmpv6 type { nd-neighbor-solicit, nd-neighbor-advert } accept
    ether type != { ip, ip6 } drop
    meta ibrname "@A@" ip saddr != { 10.40.0.0/24 } drop
    meta ibrname "@A@" ip daddr != { 10.40.0.0/24 } drop
    meta ibrname "@A@" ip6 saddr != { fd00:40::/64 } drop
    meta ibrname "@A@" ip6 daddr != { fd00:40::/64 } drop
    ct state invalid drop
    ct state { established, related } accept
    ip saddr 10.40.0.0/24 jump tenant_alpha
    ip6 saddr fd00:40::/64 jump tenant_alpha
    ip saddr 10.41.0.0/24 jump tenant_beta
    ip daddr 10.40.0.0/24 jump tenant_alpha_in
    ip6 daddr fd00:40::/64 jump tenant_alpha_in
    ip daddr 10.41.0.0/24 jump tenant_beta_in
    drop
  }
  chain tenant_alpha {
    ip daddr 10.41.0.0/24 tcp dport 8080 return
    drop
  }
  chain tenant_alpha_in {
    ip saddr 10.41.0.0/24 meta l4proto icmp accept
    drop
  }
  chain tenant_beta {
    ip daddr 10.41.0.0/24 accept
    drop
  }
  chain tenant_beta_in {
    drop
  }
}
`)
	if got != want {
		t.Fatalf("rendered batch differs from golden.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderNftBridgeTableOnlyWithDenyTenant(t *testing.T) {
	allow := renderBundle(t, twoTenantTenants(t, netpol.IntraTenantAllow))
	if strings.Contains(allow, "table bridge nayutar {") {
		t.Fatalf("bridge table emitted without a deny tenant:\n%s", allow)
	}
	for _, s := range []string{allow, renderBundle(t, nil)} {
		if !strings.HasPrefix(s, "table inet nayutar\ndelete table inet nayutar\ntable bridge nayutar\ndelete table bridge nayutar\ntable inet nayutar {\n") {
			t.Fatalf("batch must start with both idempotent table/delete pairs:\n%s", s)
		}
		if strings.Contains(s, "flush ruleset") {
			t.Fatal("flush ruleset emitted")
		}
		for _, chain := range []string{"chain guard_forward {", "chain guard_input {"} {
			if !strings.Contains(s, chain) {
				t.Fatalf("missing %q", chain)
			}
		}
	}
	deny := renderBundle(t, twoTenantTenants(t, netpol.IntraTenantDeny))
	if strings.Count(deny, "table bridge nayutar {") != 1 {
		t.Fatalf("expected exactly one bridge table definition:\n%s", deny)
	}
	_, bridgeTable, _ := strings.Cut(deny, "table bridge nayutar {")
	if strings.Contains(bridgeTable, `meta ibrname != "nbr-*"`) || strings.Contains(bridgeTable, netpol.TenantBridgeName("beta")) {
		t.Fatalf("bridge table must be scoped to deny tenants only:\n%s", deny)
	}
}

func TestRenderNftDeterministicUnderShuffle(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	base := twoTenantTenants(t, netpol.IntraTenantDeny)
	base = append(base, netpol.TenantPolicy{
		Tenant: "gamma", IntraTenant: netpol.IntraTenantDeny,
		CIDRs: []netpol.Prefix{mustPrefix(t, "10.42.0.0/25"), mustPrefix(t, "10.42.0.128/25"), mustPrefix(t, "fd00:42::/64")},
		Rules: []netpol.Rule{
			{Order: 1, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindCIDR, PeerCIDR: mustPrefix(t, "198.51.100.0/24")},
			{Order: 2, Direction: netpol.DirectionIngress, PeerKind: netpol.PeerKindTenant, PeerTenant: "alpha"},
			{Order: 3, Direction: netpol.DirectionEgress, PeerKind: netpol.PeerKindTenant, PeerTenant: "alpha"},
		},
	})
	want := renderBundle(t, base)
	for range 50 {
		shuffled := make([]netpol.TenantPolicy, len(base))
		for i, tp := range base {
			tp.CIDRs = slices.Clone(tp.CIDRs)
			tp.Rules = slices.Clone(tp.Rules)
			rng.Shuffle(len(tp.CIDRs), func(a, b int) { tp.CIDRs[a], tp.CIDRs[b] = tp.CIDRs[b], tp.CIDRs[a] })
			rng.Shuffle(len(tp.Rules), func(a, b int) { tp.Rules[a], tp.Rules[b] = tp.Rules[b], tp.Rules[a] })
			shuffled[i] = tp
		}
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		if got := renderBundle(t, shuffled); got != want {
			t.Fatalf("render depends on input order:\n%s\nvs\n%s", got, want)
		}
	}
}

func TestGuardChainNamesNeverCollideWithPolicyChains(t *testing.T) {
	for _, g := range []string{netpol.GuardForwardChain, netpol.GuardInputChain} {
		if g == "forward" || strings.HasPrefix(g, "tenant_") {
			t.Fatalf("guard chain %q can collide with policy chains", g)
		}
	}
	for _, name := range []string{"a", "guard-forward", "guard-input", "forward", "x-in"} {
		for _, c := range []string{netpol.TenantChainName(name), netpol.TenantInChainName(name)} {
			if c == netpol.GuardForwardChain || c == netpol.GuardInputChain || c == "forward" {
				t.Fatalf("tenant %q chain %q collides", name, c)
			}
		}
	}
}

func TestRenderNftRefusesInvalidCompiledPolicy(t *testing.T) {
	hostile := &netpol.CompiledPolicy{Tenants: []netpol.TenantPolicy{{
		Tenant: "a\n}\nflush ruleset\ntable inet x {",
		CIDRs:  []netpol.Prefix{mustPrefix(t, "10.1.0.0/24")},
	}}}
	script, err := netpol.RenderNft(hostile)
	if err == nil || script != "" {
		t.Fatalf("RenderNft(hostile) = %q, %v; want error", script, err)
	}
	var cerr *netpol.CompileError
	if !errors.As(err, &cerr) || cerr.Code != netpol.ErrInvalidTenantName {
		t.Fatalf("err = %v, want invalid_tenant_name", err)
	}
	if _, err := netpol.RenderNft(nil); err == nil {
		t.Fatal("RenderNft(nil) must fail")
	}
	whole := &netpol.CompiledPolicy{Tenants: []netpol.TenantPolicy{{Tenant: "a", CIDRs: []netpol.Prefix{mustPrefix(t, "0.0.0.0/0")}}}}
	var derr *netpol.DeployError
	if _, err := netpol.RenderNft(whole); !errors.As(err, &derr) {
		t.Fatalf("RenderNft(/0) err = %v, want DeployError", err)
	}
}

func TestCheckDeployable(t *testing.T) {
	const collideA, collideB = "t-2754803", "t-7961626"
	if netpol.TenantBridgeName(collideA) != netpol.TenantBridgeName(collideB) {
		t.Fatal("fixture tenants no longer collide")
	}
	tenant := func(name string, cidrs ...string) netpol.TenantPolicy {
		tp := netpol.TenantPolicy{Tenant: name}
		for _, c := range cidrs {
			tp.CIDRs = append(tp.CIDRs, mustPrefix(t, c))
		}
		return tp
	}
	cases := []struct {
		name    string
		tenants []netpol.TenantPolicy
		want    netpol.DeployErrorCode
	}{
		{"ok dual stack", []netpol.TenantPolicy{tenant("alpha", "10.40.0.0/24", "fd00:40::/64")}, ""},
		{"v4 slash 0", []netpol.TenantPolicy{tenant("alpha", "0.0.0.0/0")}, netpol.ErrWholeAddressSpaceCIDR},
		{"v6 slash 0", []netpol.TenantPolicy{tenant("alpha", "::/0")}, netpol.ErrWholeAddressSpaceCIDR},
		{"mapped inside", []netpol.TenantPolicy{tenant("alpha", "::ffff:10.0.0.0/104")}, netpol.ErrIPv4MappedCIDR},
		{"mapped exact", []netpol.TenantPolicy{tenant("alpha", "::ffff:0:0/96")}, netpol.ErrIPv4MappedCIDR},
		{"mapped covering", []netpol.TenantPolicy{tenant("alpha", "::/80")}, netpol.ErrIPv4MappedCIDR},
		{"v6 next to mapped", []netpol.TenantPolicy{tenant("alpha", "::fffe:0:0/96")}, ""},
		{"bridge collision", []netpol.TenantPolicy{tenant(collideA, "10.1.0.0/24"), tenant(collideB, "10.2.0.0/24")}, netpol.ErrBridgeNameCollision},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := netpol.CheckDeployable(tc.tenants)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("CheckDeployable = %v, want nil", err)
				}
				return
			}
			var derr *netpol.DeployError
			if !errors.As(err, &derr) || derr.Code != tc.want {
				t.Fatalf("CheckDeployable = %v, want code %q", err, tc.want)
			}
			var cerr *netpol.CompileError
			if errors.As(err, &cerr) {
				t.Fatalf("deploy error must be distinct from CompileError: %v", err)
			}
		})
	}
}

func TestCompileAcceptsWhatTheDeployCheckRejects(t *testing.T) {
	if _, cerr := netpol.Compile(netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{{Tenant: "a", CIDRs: []netpol.Prefix{mustPrefix(t, "0.0.0.0/0")}}}}); cerr != nil {
		t.Fatalf("Compile must stay free of the deploy check: %v", cerr)
	}
}

func TestEnforcerRejectsUndeployableBundlesBeforeRunner(t *testing.T) {
	fake := &netpol.FakeNftRunner{}
	enf, err := netpol.NewEnforcer(fake, sim.NewMemDisk())
	if err != nil {
		t.Fatal(err)
	}
	bundle := netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{
		{Tenant: "t-2754803", CIDRs: []netpol.Prefix{mustPrefix(t, "10.1.0.0/24")}},
		{Tenant: "t-7961626", CIDRs: []netpol.Prefix{mustPrefix(t, "10.2.0.0/24")}},
	}}
	var derr *netpol.DeployError
	if _, err := enf.Apply(context.Background(), bundle); !errors.As(err, &derr) || derr.Code != netpol.ErrBridgeNameCollision {
		t.Fatalf("Apply = %v, want bridge collision", err)
	}
	cp, _ := netpol.Compile(netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{{Tenant: "a", CIDRs: []netpol.Prefix{mustPrefix(t, "::ffff:0:0/96")}}}})
	if err := enf.ApplyCompiled(context.Background(), cp); !errors.As(err, &derr) || derr.Code != netpol.ErrIPv4MappedCIDR {
		t.Fatalf("ApplyCompiled = %v, want ipv4-mapped", err)
	}
	if fake.CallCount() != 0 {
		t.Fatalf("runner invoked %d times for undeployable bundles", fake.CallCount())
	}
}

func TestDecodeBundleRunsDeployCheck(t *testing.T) {
	const src = `@lkal "0.1"

tenants {
  alpha { cidrs = ["0.0.0.0/0"] }
}
`
	_, err := netpol.DecodeBundle("policy.lkal", []byte(src))
	var derr *netpol.DeployError
	if !errors.As(err, &derr) || derr.Code != netpol.ErrWholeAddressSpaceCIDR {
		t.Fatalf("DecodeBundle = %v, want whole-address-space deploy error", err)
	}
}

func TestEvaluateAndEvalNftDropUnknownConnState(t *testing.T) {
	cp, cerr := netpol.Compile(netpol.PolicyBundle{Tenants: twoTenantTenants(t, netpol.IntraTenantAllow)})
	if cerr != nil {
		t.Fatal(cerr)
	}
	tbl := netpol.CompileNft(cp)
	hi, lo := v4Addr(10, 40, 0, 5)
	_, dlo := v4Addr(10, 40, 0, 6)
	for _, st := range []netpol.ConnState{"", "untracked", "NEW", "Established"} {
		pkt := netpol.Packet{Family: netpol.FamilyIPv4, SrcHi: hi, SrcLo: lo, DstHi: hi, DstLo: dlo, Protocol: netpol.PacketProtoTCP, DstPort: 80, ConnState: st}
		if got := netpol.Evaluate(cp, pkt); got != netpol.VerdictDrop {
			t.Fatalf("Evaluate(%q) = %q", st, got)
		}
		if got := netpol.EvalNft(tbl, pkt); got != netpol.VerdictDrop {
			t.Fatalf("EvalNft(%q) = %q", st, got)
		}
	}
}

func TestCompileUnknownPeerKindHasOwnCode(t *testing.T) {
	_, cerr := netpol.Compile(netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{{
		Tenant: "alpha", CIDRs: []netpol.Prefix{mustPrefix(t, "10.40.0.0/24")},
		Rules: []netpol.Rule{{Order: 1, Direction: netpol.DirectionEgress, PeerKind: "bogus"}},
	}}})
	if cerr == nil || cerr.Code != netpol.ErrInvalidPeerKind {
		t.Fatalf("cerr = %v, want %q", cerr, netpol.ErrInvalidPeerKind)
	}
}

func TestKindSchemaRejectsCIDRs(t *testing.T) {
	ks, err := netpol.KindSchema()
	if err != nil {
		t.Fatal(err)
	}
	objs := resource.NewObjects(memstore.New(), resource.Config{
		Tenant:  "alpha",
		Schemas: resource.Schemas{netpol.QualifiedResourceKind: ks},
		Now:     sim.NewFakeClock(sim.DefaultEpoch).Now,
		Rand:    sim.RandReader(sim.NewDetRand(42)),
	})
	meta := lkal.Field{Key: "meta", Value: lkal.Record{Fields: []lkal.Field{
		{Key: "group", Value: lkal.String(netpol.ResourceGroup)},
		{Key: "kind", Value: lkal.String(netpol.ResourceKind)},
		{Key: "name", Value: lkal.String("default")},
	}}}
	create := func(spec ...lkal.Field) error {
		_, err := objs.Create(context.Background(), refs.Principal{Type: "user", ID: "admin"}, lkal.Record{Fields: []lkal.Field{meta, {Key: "spec", Value: lkal.Record{Fields: spec}}}})
		return err
	}
	if err := create(lkal.Field{Key: "cidrs", Value: lkal.List{lkal.String("0.0.0.0/0")}}); err == nil {
		t.Fatal("networkpolicy object carrying spec.cidrs was accepted")
	}
	if err := create(lkal.Field{Key: "intraTenant", Value: lkal.Enum("deny")}); err != nil {
		t.Fatalf("control: networkpolicy without cidrs rejected: %v", err)
	}
}

func TestCheckNftBinFacts(t *testing.T) {
	rootDirs := []netpol.DirFact{{Path: "/usr/sbin", Mode: os.ModeDir | 0o755}, {Path: "/", Mode: os.ModeDir | 0o755}}
	cases := []struct {
		name string
		mode os.FileMode
		uid  uint32
		dirs []netpol.DirFact
		ok   bool
	}{
		{"root 0755", 0o755, 0, rootDirs, true},
		{"root 0500", 0o500, 0, rootDirs, true},
		{"not root", 0o755, 1000, rootDirs, false},
		{"group writable", 0o775, 0, rootDirs, false},
		{"world writable", 0o757, 0, rootDirs, false},
		{"not executable", 0o644, 0, rootDirs, false},
		{"directory", os.ModeDir | 0o755, 0, rootDirs, false},
		{"symlink mode", os.ModeSymlink | 0o755, 0, rootDirs, false},
		{"parent owned by user", 0o755, 0, []netpol.DirFact{{Path: "/opt/bin", Mode: os.ModeDir | 0o755, UID: 1000}, rootDirs[1]}, false},
		{"parent group writable", 0o755, 0, []netpol.DirFact{{Path: "/opt/bin", Mode: os.ModeDir | 0o775}, rootDirs[1]}, false},
		{"parent world writable", 0o755, 0, []netpol.DirFact{rootDirs[0], {Path: "/opt", Mode: os.ModeDir | 0o757}, rootDirs[1]}, false},
		{"parent sticky world writable", 0o755, 0, []netpol.DirFact{{Path: "/tmp", Mode: os.ModeDir | os.ModeSticky | 0o777}}, false},
		{"root directory owned by user", 0o755, 0, []netpol.DirFact{rootDirs[0], {Path: "/", Mode: os.ModeDir | 0o755, UID: 1000}}, false},
		{"parent is a file", 0o755, 0, []netpol.DirFact{{Path: "/opt/bin", Mode: 0o755}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := netpol.CheckNftBinFacts("/x/nft", tc.mode, tc.uid, 0, tc.dirs)
			if (err == nil) != tc.ok {
				t.Fatalf("CheckNftBinFacts = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	if _, err := netpol.ValidateNftBin(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("missing binary accepted")
	}
}

func TestExecNftRunnerTimesOutHungNft(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "hang-nft")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	clk := sim.NewFakeClock(time.Unix(0, 0))
	const timeout = time.Minute
	runner, err := netpol.NewExecNftRunnerTrustingUID(bin, uint32(os.Geteuid()), netpol.WithClock(clk), netpol.WithTimeout(timeout))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runner.ExecBatch(context.Background(), "x") }()
	for clk.PendingTimers() == 0 {
		select {
		case err := <-done:
			t.Fatalf("ExecBatch returned before the deadline: %v", err)
		default:
			runtime.Gosched()
		}
	}
	clk.Advance(timeout)
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ExecBatch = %v, want deadline exceeded", err)
	}
	if _, err := netpol.NewExecNftRunnerTrustingUID(bin, uint32(os.Geteuid()), netpol.WithTimeout(0)); err == nil {
		t.Fatal("non-positive timeout accepted")
	}
}

func TestRenderNftZeroTenantsDropsEveryTenantBridge(t *testing.T) {
	got := renderBundle(t, nil)
	for _, want := range []string{`    iifname "nbr-*" drop`, `    oifname "nbr-*" drop`} {
		if !strings.Contains(got, want+"\n") {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
}

func TestRenderNftBridgeBindingFamilyWithoutCIDR(t *testing.T) {
	br := netpol.TenantBridgeName("v6only")
	got := renderBundle(t, []netpol.TenantPolicy{{Tenant: "v6only", IntraTenant: netpol.IntraTenantDeny, CIDRs: []netpol.Prefix{mustPrefix(t, "fd00:50::/64")}}})
	for _, want := range []string{
		`meta ibrname "` + br + `" ether type ip drop`,
		`meta ibrname "` + br + `" ip6 saddr != { fd00:50::/64 } drop`,
		`meta ibrname "` + br + `" ip6 daddr != { fd00:50::/64 } drop`,
	} {
		if !strings.Contains(got, "    "+want+"\n") {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "icmp type echo-request") {
		t.Fatalf("v6-only tenant must have no IPv4 echo accept:\n%s", got)
	}
	got = renderBundle(t, []netpol.TenantPolicy{{Tenant: "v4only", IntraTenant: netpol.IntraTenantDeny, CIDRs: []netpol.Prefix{mustPrefix(t, "10.50.0.0/24")}}})
	if !strings.Contains(got, `meta ibrname "`+netpol.TenantBridgeName("v4only")+`" ether type ip6 drop`+"\n") {
		t.Fatalf("v4-only tenant must drop IPv6 on its bridge:\n%s", got)
	}
}

func TestCheckBridgeNetfilter(t *testing.T) {
	const ipt, ip6t = "bridge/bridge-nf-call-iptables", "bridge/bridge-nf-call-ip6tables"
	write := func(d *sim.MemDisk, name, body string) {
		t.Helper()
		if err := d.MkdirAll("bridge", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := d.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		setup   func(d *sim.MemDisk)
		wantErr bool
		sentin  bool
	}{
		{"both absent", func(*sim.MemDisk) {}, false, false},
		{"both zero", func(d *sim.MemDisk) { write(d, ipt, "0\n"); write(d, ip6t, "0\n") }, false, false},
		{"iptables enabled", func(d *sim.MemDisk) { write(d, ipt, "1\n"); write(d, ip6t, "0\n") }, true, true},
		{"ip6tables enabled", func(d *sim.MemDisk) { write(d, ipt, "0\n"); write(d, ip6t, "1\n") }, true, true},
		{"only ip6tables present and enabled", func(d *sim.MemDisk) { write(d, ip6t, "1\n") }, true, true},
		{"garbage", func(d *sim.MemDisk) { write(d, ipt, "yes\n") }, true, true},
		{"empty", func(d *sim.MemDisk) { write(d, ipt, "") }, true, true},
		{"read error", func(d *sim.MemDisk) { write(d, ipt, "0\n"); d.InjectReadError(ipt, errors.New("EIO")) }, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := sim.NewMemDisk()
			tc.setup(d)
			err := netpol.CheckBridgeNetfilter(d)
			if (err != nil) != tc.wantErr {
				t.Fatalf("CheckBridgeNetfilter = %v, wantErr=%v", err, tc.wantErr)
			}
			if err != nil && errors.Is(err, netpol.ErrBridgeNetfilterEnabled) != tc.sentin {
				t.Fatalf("errors.Is(ErrBridgeNetfilterEnabled) = %v, want %v (%v)", !tc.sentin, tc.sentin, err)
			}
			if tc.sentin && !strings.Contains(err.Error(), "set it to 0") {
				t.Fatalf("message must say how to fix it: %v", err)
			}

			fake := &netpol.FakeNftRunner{}
			enf, nerr := netpol.NewEnforcer(fake, d)
			if nerr != nil {
				t.Fatal(nerr)
			}
			bundle := netpol.PolicyBundle{Tenants: []netpol.TenantPolicy{{Tenant: "alpha", CIDRs: []netpol.Prefix{mustPrefix(t, "10.40.0.0/24")}}}}
			_, aerr := enf.Apply(context.Background(), bundle)
			cp, _ := netpol.Compile(bundle)
			cerr := enf.ApplyCompiled(context.Background(), cp)
			if tc.wantErr {
				if aerr == nil || cerr == nil || fake.CallCount() != 0 || enf.Applied() != nil {
					t.Fatalf("policy must be refused without running nft: Apply=%v ApplyCompiled=%v calls=%d", aerr, cerr, fake.CallCount())
				}
				if terr := enf.Teardown(context.Background()); terr != nil || fake.CallCount() != 1 {
					t.Fatalf("Teardown must not be gated: err=%v calls=%d", terr, fake.CallCount())
				}
				return
			}
			if aerr != nil || cerr != nil || fake.CallCount() != 2 {
				t.Fatalf("Apply=%v ApplyCompiled=%v calls=%d", aerr, cerr, fake.CallCount())
			}
		})
	}
	if _, err := netpol.NewEnforcer(&netpol.FakeNftRunner{}, nil); err == nil {
		t.Fatal("nil sysctl disk accepted")
	}
}
