// Package node implements the direct containerd node unit runner, LKAL v0.2
// configuration schema, and simulation interfaces isolating external effects.
package node

import (
	"context"
	"syscall"
	"time"
)

// Clock is the time source used by Runner.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// RealClock returns a Clock backed by the Go standard time package.
func RealClock() Clock {
	return realClock{}
}

// Mount describes a container filesystem mount.
type Mount struct {
	Source      string
	Destination string
	Type        string
	Options     []string
}

// ContainerSpec describes a single OCI container to create in Runtime.
type ContainerSpec struct {
	ID         string
	Image      string
	Command    []string
	Args       []string
	Env        []string
	Mounts     []Mount
	NetnsPath  string
	WorkingDir string
}

// ExitResult reports a container task's exit code and any wait transport error.
type ExitResult struct {
	ExitCode uint32
	Err      error
}

// Runtime abstracts containerd image, container, and task operations.
type Runtime interface {
	EnsureImage(ctx context.Context, image string) error
	CreateContainer(ctx context.Context, spec ContainerSpec) error
	StartTask(ctx context.Context, containerID string) (<-chan ExitResult, error)
	SignalTask(ctx context.Context, containerID string, sig syscall.Signal) error
	DeleteTask(ctx context.Context, containerID string) error
	DeleteContainer(ctx context.Context, containerID string) error
}

// NetnsManager creates and removes Linux network namespaces.
type NetnsManager interface {
	Create(ctx context.Context, name string) (string, error)
	Remove(ctx context.Context, name string) error
}

// CNIRunner executes one CNI plugin chain step in ADD or DEL mode.
type CNIRunner interface {
	Count() int
	Add(ctx context.Context, step int, containerID, netnsPath string, prevResult []byte) ([]byte, error)
	Del(ctx context.Context, step int, containerID, netnsPath string, prevResult []byte) error
}
