package cni

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestResolvePluginPathAllowlistAndBoundary(t *testing.T) {
	dir := t.TempDir()
	for _, allowed := range []string{"loopback", "bridge", "host-local"} {
		writeExecutable(t, dir, allowed, "#!/bin/sh\nexit 0\n")
		got, err := ResolvePluginPath(dir, allowed)
		if err != nil {
			t.Fatalf("ResolvePluginPath(%s) failed: %v", allowed, err)
		}
		if got != filepath.Join(dir, allowed) {
			t.Fatalf("got %q, want %q", got, filepath.Join(dir, allowed))
		}
	}

	writeExecutable(t, dir, "portmap", "#!/bin/sh\nexit 0\n")
	rejected := []string{"portmap", "../bridge", "/bin/sh", "sub/bridge", "", ".", ".."}
	for _, name := range rejected {
		if _, err := ResolvePluginPath(dir, name); err == nil {
			t.Fatalf("ResolvePluginPath(%q) succeeded, want rejection", name)
		}
	}

	if _, err := ResolvePluginPath("relative/bin", "bridge"); err == nil {
		t.Fatal("expected rejection for relative binDir")
	}

	// Symlink pointing outside binDir must be rejected.
	outsideDir := t.TempDir()
	outsideBin := writeExecutable(t, outsideDir, "evil", "#!/bin/sh\nexit 0\n")
	escapeDir := t.TempDir()
	if err := os.Symlink(outsideBin, filepath.Join(escapeDir, "loopback")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := ResolvePluginPath(escapeDir, "loopback"); err == nil {
		t.Fatal("expected rejection for symlink escaping binDir")
	}
}

func TestBridgeChainConfigUsesRangesNotDeprecatedSubnet(t *testing.T) {
	dir := t.TempDir()
	ch, err := NewBridgeChain(BridgeConfig{
		BinDir:      dir,
		NetworkName: "net0",
		Bridge:      "br0",
		DataDir:     filepath.Join(dir, "ipam"),
		Ranges: []IPRange{
			{Subnet: "10.88.0.0/24", Gateway: "10.88.0.1"},
		},
	})
	if err != nil {
		t.Fatalf("NewBridgeChain: %v", err)
	}
	steps := ch.Steps()
	if len(steps) != 2 || steps[0].Plugin != "loopback" || steps[1].Plugin != "bridge" {
		t.Fatalf("unexpected steps: %+v", steps)
	}

	var brCfg map[string]any
	if err := json.Unmarshal(steps[1].Config, &brCfg); err != nil {
		t.Fatalf("unmarshal bridge config: %v", err)
	}
	if brCfg["cniVersion"] != CNIVersion {
		t.Fatalf("cniVersion = %v, want %s", brCfg["cniVersion"], CNIVersion)
	}
	if brCfg["isGateway"] != true || brCfg["isDefaultGateway"] != true {
		t.Fatalf("expected isGateway and isDefaultGateway true, got %v / %v", brCfg["isGateway"], brCfg["isDefaultGateway"])
	}
	ipam := brCfg["ipam"].(map[string]any)
	if _, deprecated := ipam["subnet"]; deprecated {
		t.Fatalf("bridge ipam must not use deprecated top-level subnet: %s", steps[1].Config)
	}
	if _, ok := ipam["ranges"]; !ok {
		t.Fatalf("bridge ipam missing ranges: %s", steps[1].Config)
	}
}

func TestChainAddDelPrevResultAndMissingNetns(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "calls.log")

	loopbackScript := `#!/bin/sh
stdin=$(cat)
echo "loopback|$CNI_COMMAND|$CNI_CONTAINERID|$CNI_NETNS|$CNI_IFNAME|$stdin" >> "` + logFile + `"
if [ "$CNI_COMMAND" = "ADD" ]; then
  printf '{"cniVersion":"1.0.0","interfaces":[{"name":"lo","sandbox":"%s"}],"ips":[{"interface":0,"address":"127.0.0.1/8"}]}' "$CNI_NETNS"
fi
`
	bridgeScript := `#!/bin/sh
stdin=$(cat)
echo "bridge|$CNI_COMMAND|$CNI_CONTAINERID|$CNI_NETNS|$CNI_IFNAME|$stdin" >> "` + logFile + `"
if [ "$CNI_COMMAND" = "ADD" ]; then
  printf '{"cniVersion":"1.0.0","interfaces":[{"name":"eth0","sandbox":"%s"}],"ips":[{"interface":0,"address":"10.88.0.2/24","gateway":"10.88.0.1"}]}' "$CNI_NETNS"
fi
`
	writeExecutable(t, dir, "loopback", loopbackScript)
	writeExecutable(t, dir, "bridge", bridgeScript)

	ch, err := NewBridgeChain(BridgeConfig{
		BinDir: dir,
		Ranges: []IPRange{{Subnet: "10.88.0.0/24", Gateway: "10.88.0.1"}},
	})
	if err != nil {
		t.Fatalf("NewBridgeChain: %v", err)
	}

	ctx := context.Background()
	nsPath := filepath.Join(dir, "vanished-netns")
	res0, err := ch.Add(ctx, 0, "unit-1", nsPath, nil)
	if err != nil {
		t.Fatalf("Add(0): %v", err)
	}
	res1, err := ch.Add(ctx, 1, "unit-1", nsPath, res0)
	if err != nil {
		t.Fatalf("Add(1): %v", err)
	}
	parsed, err := ParseResult(res1)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}
	if len(parsed.IPs) != 1 || parsed.IPs[0].Address != "10.88.0.2/24" {
		t.Fatalf("unexpected parsed result: %+v", parsed)
	}

	// Reverse DEL must succeed even though nsPath does not exist on disk.
	if err := ch.Del(ctx, 1, "unit-1", nsPath, res1); err != nil {
		t.Fatalf("Del(1): %v", err)
	}
	if err := ch.Del(ctx, 0, "unit-1", nsPath, res1); err != nil {
		t.Fatalf("Del(0): %v", err)
	}

	rawLog, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(rawLog)), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d log lines, want 4:\n%s", len(lines), rawLog)
	}
	if !strings.HasPrefix(lines[0], "loopback|ADD|unit-1|"+nsPath+"|lo|") {
		t.Fatalf("line 0 mismatch: %s", lines[0])
	}
	if !strings.HasPrefix(lines[1], "bridge|ADD|unit-1|"+nsPath+"|eth0|") || !strings.Contains(lines[1], `"prevResult"`) {
		t.Fatalf("line 1 missing prevResult or wrong env: %s", lines[1])
	}
	if !strings.HasPrefix(lines[2], "bridge|DEL|unit-1|"+nsPath+"|eth0|") || !strings.Contains(lines[2], `"prevResult"`) {
		t.Fatalf("line 2 mismatch: %s", lines[2])
	}
	if !strings.HasPrefix(lines[3], "loopback|DEL|unit-1|"+nsPath+"|lo|") {
		t.Fatalf("line 3 mismatch: %s", lines[3])
	}
}

func TestStructuredErrorDecodedFromStdoutOnly(t *testing.T) {
	dir := t.TempDir()
	// Emit valid CNI error JSON on stdout and decoy JSON on stderr to ensure strict separation.
	writeExecutable(t, dir, "loopback", `#!/bin/sh
printf '{"cniVersion":"1.0.0","code":11,"msg":"try again later","details":"pool busy"}'
printf '{"cniVersion":"1.0.0","code":99,"msg":"wrong stderr json"}' >&2
exit 1
`)
	// Emit JSON ONLY on stderr and plain text on stdout: must NOT parse stderr as CNI Error JSON.
	writeExecutable(t, dir, "bridge", `#!/bin/sh
printf 'plain stdout failure'
printf '{"cniVersion":"1.0.0","code":7,"msg":"decoy stderr"}' >&2
exit 2
`)

	ch, err := NewBridgeChain(BridgeConfig{
		BinDir: dir,
		Ranges: []IPRange{{Subnet: "10.88.0.0/24"}},
	})
	if err != nil {
		t.Fatalf("NewBridgeChain: %v", err)
	}

	_, err = ch.Add(context.Background(), 0, "u1", "/run/netns/u1", nil)
	var cniErr *Error
	if !errors.As(err, &cniErr) {
		t.Fatalf("expected *cni.Error, got %T: %v", err, err)
	}
	if cniErr.Code != 11 || cniErr.Msg != "try again later" || cniErr.Details != "pool busy" {
		t.Fatalf("wrong stdout CNI error decoded: %+v", cniErr)
	}
	if !strings.Contains(cniErr.Stderr, "wrong stderr json") {
		t.Fatalf("stderr diagnostic missing: %+v", cniErr)
	}

	_, err = ch.Add(context.Background(), 1, "u1", "/run/netns/u1", nil)
	if !errors.As(err, &cniErr) {
		t.Fatalf("expected *cni.Error, got %T: %v", err, err)
	}
	if cniErr.Code != 0 || cniErr.Msg == "decoy stderr" {
		t.Fatalf("stderr JSON was mistakenly parsed as CNI error: %+v", cniErr)
	}
	if cniErr.Details != "plain stdout failure" || !strings.Contains(cniErr.Stderr, "decoy stderr") {
		t.Fatalf("unexpected diagnostics: %+v", cniErr)
	}

	// Concatenated {Result}{Error} on stdout (emitted when skel.PluginMain reports a post-ADD error)
	// must still surface the trailing structured CNI Error code and message.
	writeExecutable(t, dir, "loopback", `#!/bin/sh
printf '{"cniVersion":"1.0.0","interfaces":[{"name":"lo"}],"ips":[{"address":"127.0.0.1/8"}]}'
printf '{"code":8,"msg":"plugin'\''s netns and netns from CNI_NETNS should not be the same"}'
exit 1
`)
	_, err = ch.Add(context.Background(), 0, "u1", "/run/netns/u1", nil)
	if !errors.As(err, &cniErr) {
		t.Fatalf("expected *cni.Error for concatenated stdout, got %T: %v", err, err)
	}
	if cniErr.Code != 8 || !strings.Contains(cniErr.Msg, "CNI_NETNS") {
		t.Fatalf("concatenated stdout CNI error not decoded: %+v", cniErr)
	}
}
