//go:build linux && containerd

package node_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/node"
	"github.com/Kaikei-e/Nayutar/core/node/cni"
	ctrd "github.com/Kaikei-e/Nayutar/core/node/containerd"
	"github.com/Kaikei-e/Nayutar/core/node/lifecycle"
	"github.com/Kaikei-e/Nayutar/core/node/netns"
	"github.com/containerd/containerd/v2/core/snapshots"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"
)

const probeProgramSource = `package main

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

type report struct {
	LoopbackOK bool   ` + "`json:\"loopbackOK\"`" + `
	Eth0IP     string ` + "`json:\"eth0IP\"`" + `
	EnvValue   string ` + "`json:\"envValue\"`" + `
	MountInput string ` + "`json:\"mountInput\"`" + `
	Attempt    int    ` + "`json:\"attempt\"`" + `
}

func checkLoopback() bool {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false
	}
	defer ln.Close()
	done := make(chan bool, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- false
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("ok"))
		done <- true
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		return false
	}
	defer c.Close()
	b, err := io.ReadAll(c)
	return err == nil && string(b) == "ok" && <-done
}

func findEth0IP() string {
	iface, err := net.InterfaceByName("eth0")
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if ok && ipNet.IP.To4() != nil {
			return ipNet.IP.To4().String()
		}
	}
	return ""
}

func bumpAttempt(path string) int {
	n := 0
	if raw, err := os.ReadFile(path); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	}
	n++
	_ = os.WriteFile(path, []byte(strconv.Itoa(n)), 0o644)
	return n
}

func main() {
	attempt := bumpAttempt("/out/attempts")
	inputRaw, _ := os.ReadFile("/out/input.txt")
	rep := report{
		LoopbackOK: checkLoopback(),
		Eth0IP:     findEth0IP(),
		EnvValue:   os.Getenv("NAYUTAR_TEST_ENV"),
		MountInput: strings.TrimSpace(string(inputRaw)),
		Attempt:    attempt,
	}
	raw, _ := json.Marshal(rep)
	_ = os.WriteFile("/out/report.json", raw, 0o644)

	switch os.Getenv("PROBE_MODE") {
	case "fail-once":
		if attempt == 1 {
			os.Exit(42)
		}
		os.Exit(0)
	case "wait-term":
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		_ = os.WriteFile("/out/ready", []byte("ready"), 0o644)
		<-sigCh
		_ = os.WriteFile("/out/term", []byte("got-sigterm"), 0o644)
		os.Exit(0)
	default:
		os.Exit(0)
	}
}
`

type probeReport struct {
	LoopbackOK bool   `json:"loopbackOK"`
	Eth0IP     string `json:"eth0IP"`
	EnvValue   string `json:"envValue"`
	MountInput string `json:"mountInput"`
	Attempt    int    `json:"attempt"`
}

func requireContainerdEnv(t *testing.T) (sockPath, cniBinDir string) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatalf("containerd integration tests require root (euid=0), got euid=%d", os.Geteuid())
	}
	sockPath = os.Getenv("NAYUTAR_CONTAINERD_SOCK")
	if sockPath == "" {
		t.Fatal("NAYUTAR_CONTAINERD_SOCK must be set for -tags containerd tests")
	}
	cniBinDir = os.Getenv("NAYUTAR_CNI_BIN_DIR")
	if cniBinDir == "" {
		t.Fatal("NAYUTAR_CNI_BIN_DIR must be set for -tags containerd tests")
	}
	return sockPath, cniBinDir
}

func buildProbeOCIArchive(t *testing.T, imageRef string) []byte {
	t.Helper()
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(srcFile, []byte(probeProgramSource), 0o600); err != nil {
		t.Fatalf("write probe source: %v", err)
	}
	goMod := "module probe\n\ngo 1.24\n"
	if err := os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte(goMod), 0o600); err != nil {
		t.Fatalf("write probe go.mod: %v", err)
	}
	binPath := filepath.Join(srcDir, "probe")
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", binPath, ".")
	cmd.Dir = srcDir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build static probe binary: %v\n%s", err, out)
	}
	binBytes, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("read probe binary: %v", err)
	}

	// Build uncompressed rootfs layer tar containing /bin/probe and /out mount point.
	var layerBuf bytes.Buffer
	ltw := tar.NewWriter(&layerBuf)
	for _, dir := range []string{"bin/", "out/"} {
		if err := ltw.WriteHeader(&tar.Header{
			Name:     dir,
			Typeflag: tar.TypeDir,
			Mode:     0o755,
		}); err != nil {
			t.Fatalf("tar dir %s: %v", dir, err)
		}
	}
	if err := ltw.WriteHeader(&tar.Header{
		Name:     "bin/probe",
		Typeflag: tar.TypeReg,
		Mode:     0o755,
		Size:     int64(len(binBytes)),
	}); err != nil {
		t.Fatalf("tar bin/probe header: %v", err)
	}
	if _, err := ltw.Write(binBytes); err != nil {
		t.Fatalf("tar bin/probe body: %v", err)
	}
	if err := ltw.Close(); err != nil {
		t.Fatalf("close layer tar: %v", err)
	}
	layerBytes := layerBuf.Bytes()
	layerDigest := digest.FromBytes(layerBytes)

	imgCfg := v1.Image{
		Platform: v1.Platform{
			Architecture: runtime.GOARCH,
			OS:           "linux",
		},
		Config: v1.ImageConfig{
			Entrypoint: []string{"/bin/probe"},
			WorkingDir: "/",
		},
		RootFS: v1.RootFS{
			Type:    "layers",
			DiffIDs: []digest.Digest{layerDigest},
		},
	}
	cfgBytes, err := json.Marshal(imgCfg)
	if err != nil {
		t.Fatalf("marshal image config: %v", err)
	}
	cfgDigest := digest.FromBytes(cfgBytes)

	manifest := v1.Manifest{
		Versioned: ocispec.Versioned{SchemaVersion: 2},
		MediaType: v1.MediaTypeImageManifest,
		Config: v1.Descriptor{
			MediaType: v1.MediaTypeImageConfig,
			Digest:    cfgDigest,
			Size:      int64(len(cfgBytes)),
		},
		Layers: []v1.Descriptor{
			{
				MediaType: v1.MediaTypeImageLayer,
				Digest:    layerDigest,
				Size:      int64(len(layerBytes)),
			},
		},
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestDigest := digest.FromBytes(manifestBytes)

	index := v1.Index{
		Versioned: ocispec.Versioned{SchemaVersion: 2},
		MediaType: v1.MediaTypeImageIndex,
		Manifests: []v1.Descriptor{
			{
				MediaType: v1.MediaTypeImageManifest,
				Digest:    manifestDigest,
				Size:      int64(len(manifestBytes)),
				Platform:  &v1.Platform{Architecture: runtime.GOARCH, OS: "linux"},
				Annotations: map[string]string{
					v1.AnnotationRefName: imageRef,
				},
			},
		},
	}
	indexBytes, err := json.Marshal(index)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	layoutBytes := []byte(`{"imageLayoutVersion":"1.0.0"}`)

	var archiveBuf bytes.Buffer
	tw := tar.NewWriter(&archiveBuf)
	addFile := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			Size:     int64(len(data)),
		}); err != nil {
			t.Fatalf("archive header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("archive body %s: %v", name, err)
		}
	}
	addFile("oci-layout", layoutBytes)
	addFile("index.json", indexBytes)
	addFile("blobs/sha256/"+hexDigest(layerBytes), layerBytes)
	addFile("blobs/sha256/"+hexDigest(cfgBytes), cfgBytes)
	addFile("blobs/sha256/"+hexDigest(manifestBytes), manifestBytes)
	if err := tw.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	return archiveBuf.Bytes()
}

func hexDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum[:])
}

func assertFullHostCleanup(t *testing.T, adapter *ctrd.Adapter, nsName, unitID, netnsDir, ipamNetDir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), nsName), 10*time.Second)
	defer cancel()

	// 1. Netns mount point removed.
	if _, err := os.Stat(filepath.Join(netnsDir, unitID)); !os.IsNotExist(err) {
		t.Fatalf("netns file %s still exists: err=%v", filepath.Join(netnsDir, unitID), err)
	}

	// 2. host-local IPAM has no IP file referencing unitID.
	entries, err := os.ReadDir(ipamNetDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read ipam dir %s: %v", ipamNetDir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() || ent.Name() == "lock" || ent.Name() == "last_reserved_ip.0" {
			continue
		}
		raw, _ := os.ReadFile(filepath.Join(ipamNetDir, ent.Name()))
		if strings.Contains(string(raw), unitID) {
			t.Fatalf("host-local IPAM leaked IP %s for %s: %s", ent.Name(), unitID, string(raw))
		}
	}

	// 3. Container removed from containerd.
	if _, err := adapter.Client().LoadContainer(ctx, unitID); !errdefs.IsNotFound(err) {
		t.Fatalf("container %s still exists or unexpected error: %v", unitID, err)
	}

	// 4. Snapshot removed from containerd snapshotter.
	snapSvc := adapter.Client().SnapshotService(ctrd.DefaultSnapshotter)
	var leakedSnaps []string
	err = snapSvc.Walk(ctx, func(_ context.Context, info snapshots.Info) error {
		if strings.HasPrefix(info.Name, unitID+"-snap-") {
			leakedSnaps = append(leakedSnaps, info.Name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk snapshots: %v", err)
	}
	if len(leakedSnaps) > 0 {
		t.Fatalf("leaked snapshots for %s: %v", unitID, leakedSnaps)
	}
}

func TestContainerdDirectExecutionEndToEnd(t *testing.T) {
	sockPath, cniBinDir := requireContainerdEnv(t)

	nsName := fmt.Sprintf("nayutar-it-%d", os.Getpid())
	bridgeName := fmt.Sprintf("nayt%d", os.Getpid()%100000)
	netName := bridgeName
	subnet := netip.MustParsePrefix("10.203.0.0/24")
	imageRef := "oci.local/nayutar-probe:test"

	adapter, err := ctrd.New(ctrd.Config{
		Address:     sockPath,
		Namespace:   nsName,
		Snapshotter: ctrd.DefaultSnapshotter,
	})
	if err != nil {
		t.Fatalf("connect containerd: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(namespaces.WithNamespace(context.Background(), nsName), 10*time.Second)
		defer cancel()
		_ = adapter.Client().ImageService().Delete(ctx, imageRef)
		_ = adapter.Close()
		_ = exec.Command("ip", "link", "del", bridgeName).Run()
	})

	archive := buildProbeOCIArchive(t, imageRef)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	imported, err := adapter.Import(ctx, bytes.NewReader(archive))
	if err != nil {
		t.Fatalf("import OCI archive: %v", err)
	}
	if len(imported) == 0 {
		t.Fatal("import returned zero images")
	}

	netnsDir := filepath.Join(t.TempDir(), "netns")
	ipamDataDir := filepath.Join(t.TempDir(), "ipam")
	nsMgr, err := netns.NewManager(netnsDir)
	if err != nil {
		t.Fatalf("netns.NewManager: %v", err)
	}
	cniChain, err := cni.NewBridgeChain(cni.BridgeConfig{
		BinDir:      cniBinDir,
		NetworkName: netName,
		Bridge:      bridgeName,
		DataDir:     ipamDataDir,
		Ranges: []cni.IPRange{
			{Subnet: subnet.String(), Gateway: "10.203.0.1"},
		},
	})
	if err != nil {
		t.Fatalf("cni.NewBridgeChain: %v", err)
	}
	ipamNetDir := filepath.Join(ipamDataDir, netName)

	t.Run("exit0_loopback_bridge_env_mount_cleanup", func(t *testing.T) {
		unitID := "probe-exit0"
		outDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(outDir, "input.txt"), []byte("hello-bind-mount"), 0o644); err != nil {
			t.Fatalf("write input.txt: %v", err)
		}
		runner, err := node.NewRunner(node.RunnerConfig{
			Unit: node.UnitSpec{
				ID:    unitID,
				Image: imageRef,
				Env:   []string{"NAYUTAR_TEST_ENV=direct-containerd", "PROBE_MODE=exit0"},
				Mounts: []node.Mount{
					{Source: outDir, Destination: "/out", Type: "bind", Options: []string{"rbind", "rw"}},
				},
				Lifecycle: lifecycle.Spec{
					RestartPolicy:  lifecycle.RestartNever,
					StopGraceMs:    3000,
					BackoffBaseMs:  200,
					BackoffMaxMs:   1000,
					BackoffResetMs: 5000,
					CNICount:       2,
				},
			},
			Runtime: adapter,
			Netns:   nsMgr,
			CNI:     cniChain,
		})
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		st, err := runner.Run(ctx)
		if err != nil {
			t.Fatalf("Runner.Run: %v (state=%+v)", err, st)
		}
		if !st.HasExited || st.LastExitCode != 0 || st.RestartCount != 0 {
			t.Fatalf("unexpected state: %+v", st)
		}
		raw, err := os.ReadFile(filepath.Join(outDir, "report.json"))
		if err != nil {
			t.Fatalf("read report.json: %v", err)
		}
		var rep probeReport
		if err := json.Unmarshal(raw, &rep); err != nil {
			t.Fatalf("unmarshal report: %v", err)
		}
		if !rep.LoopbackOK {
			t.Fatalf("127.0.0.1 loopback failed inside container: %+v", rep)
		}
		ip, err := netip.ParseAddr(rep.Eth0IP)
		if err != nil || !subnet.Contains(ip) {
			t.Fatalf("eth0 IP %q not inside subnet %s: %+v", rep.Eth0IP, subnet, rep)
		}
		if rep.EnvValue != "direct-containerd" || rep.MountInput != "hello-bind-mount" {
			t.Fatalf("env or mount mismatch: %+v", rep)
		}
		assertFullHostCleanup(t, adapter, nsName, unitID, netnsDir, ipamNetDir)
	})

	t.Run("nonzero_restart_then_exit0_cleanup", func(t *testing.T) {
		unitID := "probe-restart"
		outDir := t.TempDir()
		runner, err := node.NewRunner(node.RunnerConfig{
			Unit: node.UnitSpec{
				ID:    unitID,
				Image: imageRef,
				Env:   []string{"PROBE_MODE=fail-once"},
				Mounts: []node.Mount{
					{Source: outDir, Destination: "/out", Type: "bind", Options: []string{"rbind", "rw"}},
				},
				Lifecycle: lifecycle.Spec{
					RestartPolicy:  lifecycle.RestartOnFailure,
					StopGraceMs:    3000,
					BackoffBaseMs:  200,
					BackoffMaxMs:   1000,
					BackoffResetMs: 5000,
					CNICount:       2,
				},
			},
			Runtime: adapter,
			Netns:   nsMgr,
			CNI:     cniChain,
		})
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		st, err := runner.Run(ctx)
		if err != nil {
			t.Fatalf("Runner.Run: %v (state=%+v)", err, st)
		}
		if st.RestartCount != 1 || !st.HasExited || st.LastExitCode != 0 {
			t.Fatalf("expected 1 restart then exit 0, got %+v", st)
		}
		raw, err := os.ReadFile(filepath.Join(outDir, "report.json"))
		if err != nil {
			t.Fatalf("read restart report.json: %v", err)
		}
		var rep probeReport
		if err := json.Unmarshal(raw, &rep); err != nil {
			t.Fatalf("unmarshal restart report: %v", err)
		}
		if !rep.LoopbackOK || rep.Attempt != 2 {
			t.Fatalf("unexpected restart probe report: %+v", rep)
		}
		ip, err := netip.ParseAddr(rep.Eth0IP)
		if err != nil || !subnet.Contains(ip) {
			t.Fatalf("restart eth0 IP %q not inside subnet %s: %+v", rep.Eth0IP, subnet, rep)
		}
		assertFullHostCleanup(t, adapter, nsName, unitID, netnsDir, ipamNetDir)
	})

	t.Run("sigterm_graceful_stop_cleanup", func(t *testing.T) {
		unitID := "probe-term"
		outDir := t.TempDir()
		runCtx, runCancel := context.WithCancel(ctx)
		defer runCancel()

		go func() {
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(outDir, "ready")); err == nil {
					runCancel()
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			runCancel()
		}()

		runner, err := node.NewRunner(node.RunnerConfig{
			Unit: node.UnitSpec{
				ID:    unitID,
				Image: imageRef,
				Env:   []string{"PROBE_MODE=wait-term"},
				Mounts: []node.Mount{
					{Source: outDir, Destination: "/out", Type: "bind", Options: []string{"rbind", "rw"}},
				},
				Lifecycle: lifecycle.Spec{
					RestartPolicy:  lifecycle.RestartAlways,
					StopGraceMs:    5000,
					BackoffBaseMs:  200,
					BackoffMaxMs:   1000,
					BackoffResetMs: 5000,
					CNICount:       2,
				},
			},
			Runtime: adapter,
			Netns:   nsMgr,
			CNI:     cniChain,
		})
		if err != nil {
			t.Fatalf("NewRunner: %v", err)
		}
		st, err := runner.Run(runCtx)
		if err != nil {
			t.Fatalf("Runner.Run: %v (state=%+v)", err, st)
		}
		termRaw, err := os.ReadFile(filepath.Join(outDir, "term"))
		if err != nil || strings.TrimSpace(string(termRaw)) != "got-sigterm" {
			t.Fatalf("container did not observe SIGTERM: raw=%q err=%v", string(termRaw), err)
		}
		assertFullHostCleanup(t, adapter, nsName, unitID, netnsDir, ipamNetDir)
	})
}
