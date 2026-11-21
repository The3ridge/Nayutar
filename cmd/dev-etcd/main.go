package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/config"
)

const (
	defaultConfigPath = "config/dev.lkal"
	etcdBinaryEnv     = "NAYUTAR_ETCD_BIN"
	clusterToken      = "nayutar-dev"
	expectedMembers   = 3
)

type memberConfig struct {
	Name      string
	ClientURL string
	PeerURL   string
}

type clusterConfig struct {
	Version string
	Members []memberConfig
}

func main() {
	configPath := flag.String("config", defaultConfigPath, "path to LKAL configuration file")
	dataDir := flag.String("data-dir", "", "base directory for etcd member data")
	flag.Parse()

	if err := run(*configPath, *dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}

func run(configPath, dataDir string) error {
	cfg, err := readClusterConfig(configPath)
	if err != nil {
		return err
	}

	resolvedDataDir, err := resolveDataDir(dataDir)
	if err != nil {
		return err
	}

	etcdBin, err := findEtcdBinary()
	if err != nil {
		return err
	}
	if err := checkEtcdVersion(etcdBin, cfg.Version); err != nil {
		return err
	}

	return runCluster(etcdBin, resolvedDataDir, cfg)
}

func readClusterConfig(configPath string) (clusterConfig, error) {
	src, err := os.ReadFile(configPath)
	if err != nil {
		return clusterConfig{}, err
	}
	return decodeClusterConfig(configPath, src)
}

func decodeClusterConfig(name string, src []byte) (clusterConfig, error) {
	f, err := lkal.Parse(name, src)
	if err != nil {
		return clusterConfig{}, fmt.Errorf("parse %s: %w", name, err)
	}
	if err := config.DevSchema().Validate(f); err != nil {
		return clusterConfig{}, fmt.Errorf("validate %s: %w", name, err)
	}

	etcdRec, _ := recordField(f.Data(), "etcd")
	version, _ := stringField(etcdRec, "version")
	memberRec, _ := recordField(etcdRec, "member")

	members := make([]memberConfig, 0, len(memberRec.Fields))
	for _, mf := range memberRec.Fields {
		mName := mf.Key
		mRec := mf.Value.(lkal.Record)
		clientURL, _ := stringField(mRec, "client-url")
		peerURL, _ := stringField(mRec, "peer-url")
		if err := validateLoopbackURL(clientURL); err != nil {
			return clusterConfig{}, fmt.Errorf("etcd member %q: client-url %q: %w", mName, clientURL, err)
		}
		if err := validateLoopbackURL(peerURL); err != nil {
			return clusterConfig{}, fmt.Errorf("etcd member %q: peer-url %q: %w", mName, peerURL, err)
		}
		members = append(members, memberConfig{
			Name:      mName,
			ClientURL: clientURL,
			PeerURL:   peerURL,
		})
	}
	slices.SortFunc(members, func(a, b memberConfig) int {
		return strings.Compare(a.Name, b.Name)
	})

	return clusterConfig{
		Version: version,
		Members: members,
	}, nil
}

func recordField(rec lkal.Record, key string) (lkal.Record, bool) {
	for _, f := range rec.Fields {
		if f.Key == key {
			r, ok := f.Value.(lkal.Record)
			return r, ok
		}
	}
	return lkal.Record{}, false
}

func stringField(rec lkal.Record, key string) (string, bool) {
	for _, f := range rec.Fields {
		if f.Key == key {
			s, ok := f.Value.(lkal.String)
			return string(s), ok
		}
	}
	return "", false
}

// The development cluster is plaintext and unauthenticated, so members may only listen on loopback IP literals.
func validateLoopbackURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
	case "https":
		return errors.New("scheme must be http: the development cluster is plaintext and has no TLS configuration")
	default:
		return errors.New("scheme must be http")
	}
	if u.Opaque != "" || u.User != nil || strings.ContainsAny(raw, "?#") || (u.Path != "" && u.Path != "/") {
		return errors.New("must be scheme://host:port without userinfo, path, query or fragment")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("must have an explicit port between 1 and 65535")
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil || addr.Zone() != "" {
		return errors.New("host must be an IP literal")
	}
	if !addr.IsLoopback() {
		return errors.New("host must be a loopback address")
	}
	return nil
}

func parseEtcdVersion(output string) (string, error) {
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "etcd Version:"); ok {
			v = strings.TrimSpace(v)
			if v == "" {
				return "", errors.New("empty etcd Version line")
			}
			return v, nil
		}
	}
	return "", errors.New("etcd --version output did not contain an \"etcd Version:\" line")
}

func memberArgs(member memberConfig, dataDir string, allMembers []memberConfig) []string {
	sorted := slices.Clone(allMembers)
	slices.SortFunc(sorted, func(a, b memberConfig) int {
		return strings.Compare(a.Name, b.Name)
	})
	clusterPeers := make([]string, len(sorted))
	for i, m := range sorted {
		clusterPeers[i] = m.Name + "=" + m.PeerURL
	}
	return []string{
		"--name", member.Name,
		"--data-dir", filepath.Join(dataDir, member.Name),
		"--listen-client-urls", member.ClientURL,
		"--advertise-client-urls", member.ClientURL,
		"--listen-peer-urls", member.PeerURL,
		"--initial-advertise-peer-urls", member.PeerURL,
		"--initial-cluster", strings.Join(clusterPeers, ","),
		"--initial-cluster-state", "new",
		"--initial-cluster-token", clusterToken,
	}
}

func resolveDataDir(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "nayutar", "dev-etcd"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for default data-dir: %w", err)
	}
	return filepath.Join(home, ".local", "state", "nayutar", "dev-etcd"), nil
}

func findEtcdBinary() (string, error) {
	if bin := os.Getenv(etcdBinaryEnv); bin != "" {
		return bin, nil
	}
	found, err := exec.LookPath("etcd")
	if err != nil {
		return "", fmt.Errorf("etcd binary not found: set %s or put etcd on PATH", etcdBinaryEnv)
	}
	return found, nil
}

func checkEtcdVersion(etcdBin, wantVersion string) error {
	out, err := exec.Command(etcdBin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("run %s --version: %w", etcdBin, err)
	}
	gotVersion, err := parseEtcdVersion(string(out))
	if err != nil {
		return fmt.Errorf("parse %s --version: %w", etcdBin, err)
	}
	if gotVersion != wantVersion {
		return fmt.Errorf("etcd version mismatch: binary is %s, declaration requires %s", gotVersion, wantVersion)
	}
	return nil
}

type memberProc struct {
	name string
	cmd  *exec.Cmd
}

type procExit struct {
	name string
	err  error
}

func runCluster(etcdBin, dataDir string, cfg clusterConfig) error {
	sigCtx, stopSig := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stopSig()

	procs := make([]memberProc, 0, len(cfg.Members))
	for _, m := range cfg.Members {
		memberDir := filepath.Join(dataDir, m.Name)
		if err := os.MkdirAll(memberDir, 0o700); err != nil {
			signalTerminate(procs)
			waitStarted(procs)
			return fmt.Errorf("create data directory for %s: %w", m.Name, err)
		}
		cmd := exec.Command(etcdBin, memberArgs(m, dataDir, cfg.Members)...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			signalTerminate(procs)
			waitStarted(procs)
			return fmt.Errorf("start etcd member %s: %w", m.Name, err)
		}
		procs = append(procs, memberProc{name: m.Name, cmd: cmd})
	}

	exited := make(chan procExit, len(procs))
	for _, p := range procs {
		go func(p memberProc) {
			exited <- procExit{name: p.name, err: p.cmd.Wait()}
		}(p)
	}

	select {
	case <-sigCtx.Done():
		stopSig()
		signalTerminate(procs)
		for range len(procs) {
			<-exited
		}
		return nil
	case res := <-exited:
		stopSig()
		signalTerminate(procs)
		for range len(procs) - 1 {
			<-exited
		}
		if res.err != nil {
			return fmt.Errorf("etcd member %s exited unexpectedly: %w", res.name, res.err)
		}
		return fmt.Errorf("etcd member %s exited unexpectedly", res.name)
	}
}

func signalTerminate(procs []memberProc) {
	for _, p := range procs {
		if p.cmd.Process != nil {
			_ = p.cmd.Process.Signal(syscall.SIGTERM)
		}
	}
}

func waitStarted(procs []memberProc) {
	for _, p := range procs {
		_ = p.cmd.Wait()
	}
}
