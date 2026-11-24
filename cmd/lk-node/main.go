package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/Kaikei-e/Nayutar/core/node"
	"github.com/Kaikei-e/Nayutar/core/node/cni"
	ctrd "github.com/Kaikei-e/Nayutar/core/node/containerd"
	"github.com/Kaikei-e/Nayutar/core/node/netns"
)

type runtimeDeps struct {
	signalContext func(context.Context) (context.Context, context.CancelFunc)
	buildAdapters func(node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error)
	clock         node.Clock
}

func defaultDeps() runtimeDeps {
	return runtimeDeps{
		signalContext: func(parent context.Context) (context.Context, context.CancelFunc) {
			return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
		},
		buildAdapters: buildRealAdapters,
		clock:         node.RealClock(),
	}
}

func buildRealAdapters(cfg node.Config) (node.Runtime, node.NetnsManager, node.CNIRunner, func() error, error) {
	nsMgr, err := netns.NewManager(cfg.CNI.NetnsDir)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	ranges := make([]cni.IPRange, len(cfg.CNI.Ranges))
	for i, r := range cfg.CNI.Ranges {
		ranges[i] = cni.IPRange{
			Subnet:     r.Subnet,
			Gateway:    r.Gateway,
			RangeStart: r.RangeStart,
			RangeEnd:   r.RangeEnd,
		}
	}
	cniChain, err := cni.NewBridgeChain(cni.BridgeConfig{
		BinDir:      cfg.CNI.BinDir,
		NetworkName: cfg.CNI.Network,
		Bridge:      cfg.CNI.Bridge,
		DataDir:     cfg.CNI.DataDir,
		Ranges:      ranges,
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	adapter, err := ctrd.New(ctrd.Config{
		Address:     cfg.Containerd.Socket,
		Namespace:   cfg.Containerd.Namespace,
		Snapshotter: cfg.Containerd.Snapshotter,
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return adapter, nsMgr, cniChain, adapter.Close, nil
}

func main() {
	os.Exit(runMain(context.Background(), os.Args[1:], os.Stdout, os.Stderr, defaultDeps()))
}

func runMain(ctx context.Context, args []string, stdout, stderr io.Writer, d runtimeDeps) int {
	fs := flag.NewFlagSet("lk-node", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to LKAL unit configuration file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *configPath == "" {
		_, _ = fmt.Fprintln(stderr, "lk-node: -config flag is required")
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "lk-node: unexpected positional arguments: %v\n", fs.Args())
		return 2
	}

	code, err := runFile(ctx, *configPath, stdout, stderr, d)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "lk-node: %v\n", err)
		if code == 0 {
			return 1
		}
	}
	return code
}

func runFile(ctx context.Context, configPath string, stdout, stderr io.Writer, d runtimeDeps) (int, error) {
	src, err := os.ReadFile(configPath)
	if err != nil {
		return 1, fmt.Errorf("read %s: %w", configPath, err)
	}
	cfg, err := node.DecodeConfig(configPath, src)
	if err != nil {
		return 1, err
	}

	rt, nsMgr, cniRunner, closeFn, err := d.buildAdapters(cfg)
	if err != nil {
		return 1, err
	}
	if closeFn != nil {
		defer func() {
			if cerr := closeFn(); cerr != nil {
				_, _ = fmt.Fprintf(stderr, "lk-node: close runtime: %v\n", cerr)
			}
		}()
	}

	sigCtx, stopSig := d.signalContext(ctx)
	defer stopSig()

	runner, err := node.NewRunner(node.RunnerConfig{
		Unit:    cfg.Unit,
		Runtime: rt,
		Netns:   nsMgr,
		CNI:     cniRunner,
		Clock:   d.clock,
		OnError: func(e error) {
			_, _ = fmt.Fprintf(stderr, "lk-node: %v\n", e)
		},
	})
	if err != nil {
		return 1, err
	}

	st, runErr := runner.Run(sigCtx)
	_, _ = fmt.Fprintf(stdout, "unit=%s phase=%s restarts=%d exited=%t exitCode=%d\n",
		cfg.Unit.ID, st.Phase, st.RestartCount, st.HasExited, st.LastExitCode)

	if runErr != nil || st.Failed {
		if runErr == nil {
			runErr = errors.New("unit lifecycle failed")
		}
		return 1, runErr
	}
	if st.HasExited {
		if st.LastExitCode > 255 {
			return 1, nil
		}
		return int(st.LastExitCode), nil
	}
	return 0, nil
}
