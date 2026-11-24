// Package containerd adapts containerd v2.3 LTS directly to node.Runtime without CRI.
package containerd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Kaikei-e/Nayutar/core/node"
	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/leases"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/runtime-spec/specs-go"
)

const (
	// DefaultAddress is the standard containerd Unix socket path.
	DefaultAddress = "/run/containerd/containerd.sock"
	// DefaultNamespace is the default containerd namespace for Nayutar workloads.
	DefaultNamespace = "nayutar"
	// DefaultSnapshotter is the default containerd snapshotter.
	DefaultSnapshotter = "overlayfs"
	// DefaultRuntimeName is the containerd v2 runc shim runtime identifier.
	DefaultRuntimeName = "io.containerd.runc.v2"

	defaultCleanupTimeout = 30 * time.Second
	defaultLeaseTTL       = 5 * time.Minute
)

// Config configures the containerd v2.3 Runtime adapter.
type Config struct {
	Address        string
	Namespace      string
	Snapshotter    string
	RuntimeName    string
	CleanupTimeout time.Duration
	IOCreator      cio.Creator
}

func (c Config) withDefaults() Config {
	if c.Address == "" {
		c.Address = DefaultAddress
	}
	if c.Namespace == "" {
		c.Namespace = DefaultNamespace
	}
	if c.Snapshotter == "" {
		c.Snapshotter = DefaultSnapshotter
	}
	if c.RuntimeName == "" {
		c.RuntimeName = DefaultRuntimeName
	}
	if c.CleanupTimeout <= 0 {
		c.CleanupTimeout = defaultCleanupTimeout
	}
	if c.IOCreator == nil {
		c.IOCreator = cio.NullIO
	}
	return c
}

// Adapter implements node.Runtime directly on top of containerd's v2 Go client.
type Adapter struct {
	client  *client.Client
	cfg     Config
	mu      sync.Mutex
	tasks   map[string]client.Task
	snapSeq atomic.Uint64
}

// New dials containerd at cfg.Address and returns a ready Adapter.
func New(cfg Config) (*Adapter, error) {
	cfg = cfg.withDefaults()
	c, err := client.New(cfg.Address, client.WithDefaultNamespace(cfg.Namespace))
	if err != nil {
		return nil, fmt.Errorf("containerd: dial %s: %w", cfg.Address, err)
	}
	return NewWithClient(c, cfg), nil
}

// NewWithClient wraps an existing *client.Client with cfg.
func NewWithClient(c *client.Client, cfg Config) *Adapter {
	cfg = cfg.withDefaults()
	return &Adapter{
		client: c,
		cfg:    cfg,
		tasks:  make(map[string]client.Task),
	}
}

// Client returns the underlying containerd v2 client.
func (a *Adapter) Client() *client.Client {
	return a.client
}

// Close closes the underlying containerd client connection.
func (a *Adapter) Close() error {
	if a.client == nil {
		return nil
	}
	return a.client.Close()
}

func (a *Adapter) nsContext(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, a.cfg.Namespace)
}

func (a *Adapter) cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	base := a.nsContext(context.WithoutCancel(ctx))
	return context.WithTimeout(base, a.cfg.CleanupTimeout)
}

// withTempLease acquires a short-lived containerd GC lease so content/snapshots cannot be
// reaped between image pull/unpack and container creation.
func (a *Adapter) withTempLease(ctx context.Context) (context.Context, func(), error) {
	nsCtx := a.nsContext(ctx)
	ls := a.client.LeasesService()
	lease, err := ls.Create(nsCtx, leases.WithRandomID(), leases.WithExpiration(defaultLeaseTTL))
	if err != nil {
		return nil, nil, fmt.Errorf("containerd: create GC lease: %w", err)
	}
	leaseCtx := leases.WithLease(nsCtx, lease.ID)
	release := func() {
		relCtx, cancel := a.cleanupContext(context.Background())
		defer cancel()
		_ = ls.Delete(relCtx, lease)
	}
	return leaseCtx, release, nil
}

// Import loads an OCI image archive stream into containerd and unpacks each imported image.
func (a *Adapter) Import(ctx context.Context, r io.Reader, opts ...client.ImportOpt) ([]string, error) {
	leaseCtx, release, err := a.withTempLease(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	imgs, err := a.client.Import(leaseCtx, r, opts...)
	if err != nil {
		return nil, fmt.Errorf("containerd: import archive: %w", err)
	}
	names := make([]string, 0, len(imgs))
	for _, meta := range imgs {
		img := client.NewImage(a.client, meta)
		if err := img.Unpack(leaseCtx, a.cfg.Snapshotter); err != nil {
			return nil, fmt.Errorf("containerd: unpack imported image %s: %w", meta.Name, err)
		}
		names = append(names, meta.Name)
	}
	return names, nil
}

// EnsureImage verifies that ref is present and unpacked in containerd, pulling if absent.
func (a *Adapter) EnsureImage(ctx context.Context, ref string) error {
	if ref == "" {
		return errors.New("containerd: image reference must be non-empty")
	}
	leaseCtx, release, err := a.withTempLease(ctx)
	if err != nil {
		return err
	}
	defer release()

	img, err := a.client.GetImage(leaseCtx, ref)
	if err == nil {
		unpacked, uerr := img.IsUnpacked(leaseCtx, a.cfg.Snapshotter)
		if uerr != nil {
			return fmt.Errorf("containerd: check unpacked %s: %w", ref, uerr)
		}
		if !unpacked {
			if err := img.Unpack(leaseCtx, a.cfg.Snapshotter); err != nil {
				return fmt.Errorf("containerd: unpack %s: %w", ref, err)
			}
		}
		return nil
	}
	if !errdefs.IsNotFound(err) {
		return fmt.Errorf("containerd: get image %s: %w", ref, err)
	}
	_, err = a.client.Pull(leaseCtx, ref,
		client.WithPullUnpack,
		client.WithPullSnapshotter(a.cfg.Snapshotter),
	)
	if err != nil {
		return fmt.Errorf("containerd: pull %s: %w", ref, err)
	}
	return nil
}

// BuildSpecOpts constructs the OCI runtime spec options for spec against img.
func BuildSpecOpts(img client.Image, spec node.ContainerSpec) []oci.SpecOpts {
	opts := []oci.SpecOpts{oci.WithImageConfig(img)}
	if len(spec.Command) > 0 || len(spec.Args) > 0 {
		args := make([]string, 0, len(spec.Command)+len(spec.Args))
		args = append(args, spec.Command...)
		args = append(args, spec.Args...)
		opts = append(opts, oci.WithProcessArgs(args...))
	}
	if len(spec.Env) > 0 {
		opts = append(opts, oci.WithEnv(slices.Clone(spec.Env)))
	}
	if spec.WorkingDir != "" {
		opts = append(opts, oci.WithProcessCwd(spec.WorkingDir))
	}
	if len(spec.Mounts) > 0 {
		mounts := make([]specs.Mount, len(spec.Mounts))
		for i, m := range spec.Mounts {
			mType := m.Type
			if mType == "" {
				mType = "bind"
			}
			mounts[i] = specs.Mount{
				Source:      m.Source,
				Destination: m.Destination,
				Type:        mType,
				Options:     slices.Clone(m.Options),
			}
		}
		opts = append(opts, oci.WithMounts(mounts))
	}
	if spec.NetnsPath != "" {
		opts = append(opts, oci.WithLinuxNamespace(specs.LinuxNamespace{
			Type: specs.NetworkNamespace,
			Path: spec.NetnsPath,
		}))
	}
	return opts
}

// CreateContainer creates a containerd container and snapshot under a temporary GC lease.
func (a *Adapter) CreateContainer(ctx context.Context, spec node.ContainerSpec) error {
	if spec.ID == "" {
		return errors.New("containerd: container ID must be non-empty")
	}
	leaseCtx, release, err := a.withTempLease(ctx)
	if err != nil {
		return err
	}
	defer release()

	img, err := a.client.GetImage(leaseCtx, spec.Image)
	if err != nil {
		return fmt.Errorf("containerd: get image %s: %w", spec.Image, err)
	}
	unpacked, err := img.IsUnpacked(leaseCtx, a.cfg.Snapshotter)
	if err != nil {
		return fmt.Errorf("containerd: check unpacked %s: %w", spec.Image, err)
	}
	if !unpacked {
		if err := img.Unpack(leaseCtx, a.cfg.Snapshotter); err != nil {
			return fmt.Errorf("containerd: unpack %s: %w", spec.Image, err)
		}
	}

	snapKey := fmt.Sprintf("%s-snap-%d", spec.ID, a.snapSeq.Add(1))
	specOpts := BuildSpecOpts(img, spec)
	_, err = a.client.NewContainer(leaseCtx, spec.ID,
		client.WithImage(img),
		client.WithSnapshotter(a.cfg.Snapshotter),
		client.WithNewSnapshot(snapKey, img),
		client.WithRuntime(a.cfg.RuntimeName, nil),
		client.WithNewSpec(specOpts...),
	)
	if err != nil {
		return fmt.Errorf("containerd: new container %s: %w", spec.ID, err)
	}
	return nil
}

// StartTask creates a container task, arms task.Wait strictly BEFORE task.Start, and returns its exit channel.
func (a *Adapter) StartTask(ctx context.Context, containerID string) (<-chan node.ExitResult, error) {
	nsCtx := a.nsContext(ctx)
	ctr, err := a.client.LoadContainer(nsCtx, containerID)
	if err != nil {
		return nil, fmt.Errorf("containerd: load container %s: %w", containerID, err)
	}
	task, err := ctr.NewTask(nsCtx, a.cfg.IOCreator)
	if err != nil {
		return nil, fmt.Errorf("containerd: new task %s: %w", containerID, err)
	}

	// Arm Wait before Start so short-lived processes cannot exit before Wait subscribes.
	waitCtx, waitCancel := context.WithCancel(a.nsContext(context.Background()))
	cleanupTask := func() {
		waitCancel()
		cctx, cancel := a.cleanupContext(context.Background())
		defer cancel()
		_, _ = task.Delete(cctx, client.WithProcessKill)
	}

	waitCh, err := task.Wait(waitCtx)
	if err != nil {
		cleanupTask()
		return nil, fmt.Errorf("containerd: wait task %s: %w", containerID, err)
	}
	if err := task.Start(nsCtx); err != nil {
		cleanupTask()
		return nil, fmt.Errorf("containerd: start task %s: %w", containerID, err)
	}

	a.mu.Lock()
	a.tasks[containerID] = task
	a.mu.Unlock()

	outCh := make(chan node.ExitResult, 1)
	go func() {
		defer waitCancel()
		defer close(outCh)
		status, ok := <-waitCh
		if !ok {
			outCh <- node.ExitResult{ExitCode: 0}
			return
		}
		code, _, werr := status.Result()
		outCh <- node.ExitResult{ExitCode: code, Err: werr}
	}()
	return outCh, nil
}

func (a *Adapter) lookupTask(ctx context.Context, containerID string, remove bool) (client.Task, error) {
	a.mu.Lock()
	task, ok := a.tasks[containerID]
	if ok && remove {
		delete(a.tasks, containerID)
	}
	a.mu.Unlock()
	if ok {
		return task, nil
	}
	ctr, err := a.client.LoadContainer(ctx, containerID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	task, err = ctr.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return task, nil
}

// SignalTask sends sig to every process in containerID's task using client.WithKillAll.
func (a *Adapter) SignalTask(ctx context.Context, containerID string, sig syscall.Signal) error {
	cctx, cancel := a.cleanupContext(ctx)
	defer cancel()

	task, err := a.lookupTask(cctx, containerID, false)
	if err != nil {
		return fmt.Errorf("containerd: lookup task %s: %w", containerID, err)
	}
	if task == nil {
		return nil
	}
	if err := task.Kill(cctx, sig, client.WithKillAll); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("containerd: kill task %s (%v): %w", containerID, sig, err)
	}
	return nil
}

// DeleteTask deletes the task for containerID with a bounded cleanup context and client.WithProcessKill.
func (a *Adapter) DeleteTask(ctx context.Context, containerID string) error {
	cctx, cancel := a.cleanupContext(ctx)
	defer cancel()

	task, err := a.lookupTask(cctx, containerID, true)
	if err != nil {
		return fmt.Errorf("containerd: lookup task %s: %w", containerID, err)
	}
	if task == nil {
		return nil
	}
	if _, err := task.Delete(cctx, client.WithProcessKill); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("containerd: delete task %s: %w", containerID, err)
	}
	return nil
}

// DeleteContainer removes containerID and its snapshot with a bounded cleanup context.
func (a *Adapter) DeleteContainer(ctx context.Context, containerID string) error {
	cctx, cancel := a.cleanupContext(ctx)
	defer cancel()

	ctr, err := a.client.LoadContainer(cctx, containerID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("containerd: load container %s: %w", containerID, err)
	}
	if err := ctr.Delete(cctx, client.WithSnapshotCleanup); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("containerd: delete container %s: %w", containerID, err)
	}
	return nil
}
