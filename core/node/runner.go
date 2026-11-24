package node

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Kaikei-e/Nayutar/core/node/lifecycle"
	"github.com/Kaikei-e/Nayutar/core/sim"
)

// ErrAlreadyRun is returned when Runner.Run is invoked more than once.
var ErrAlreadyRun = errors.New("node: runner already started")

const defaultCleanupTimeout = 30 * time.Second

// RunnerConfig configures a single-unit Runner.
type RunnerConfig struct {
	Unit           UnitSpec
	Runtime        Runtime
	Netns          NetnsManager
	CNI            CNIRunner
	Clock          Clock         // nil -> RealClock()
	CleanupTimeout time.Duration // 0 -> 30s per teardown step
	OnTransition   func(lifecycle.State)
	OnError        func(error)
}

// Runner drives lifecycle.NextStep / ApplyEvent against Runtime, NetnsManager, and CNIRunner.
type Runner struct {
	unit           UnitSpec
	runtime        Runtime
	netns          NetnsManager
	cni            CNIRunner
	clock          Clock
	cleanupTimeout time.Duration
	onTransition   func(lifecycle.State)
	onError        func(error)
	started        atomic.Bool
}

// NewRunner validates cfg and constructs a single-use Runner.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Unit.ID == "" {
		return nil, errors.New("node: unit id must be non-empty")
	}
	if cfg.Unit.Image == "" {
		return nil, errors.New("node: unit image must be non-empty")
	}
	if err := cfg.Unit.Lifecycle.Validate(); err != nil {
		return nil, err
	}
	if cfg.Runtime == nil {
		return nil, errors.New("node: runtime must be non-nil")
	}
	if cfg.Netns == nil {
		return nil, errors.New("node: netns manager must be non-nil")
	}
	if cfg.Unit.Lifecycle.CNICount > 0 {
		if cfg.CNI == nil {
			return nil, errors.New("node: cni runner must be non-nil when cni count > 0")
		}
		if cfg.CNI.Count() != cfg.Unit.Lifecycle.CNICount {
			return nil, fmt.Errorf("node: cni runner count %d != lifecycle cni count %d", cfg.CNI.Count(), cfg.Unit.Lifecycle.CNICount)
		}
	}
	if cfg.CleanupTimeout < 0 {
		return nil, errors.New("node: cleanup timeout must be >= 0")
	}
	cleanupTimeout := cfg.CleanupTimeout
	if cleanupTimeout == 0 {
		cleanupTimeout = defaultCleanupTimeout
	}
	clk := cfg.Clock
	if clk == nil {
		clk = RealClock()
	}
	return &Runner{
		unit:           cfg.Unit,
		runtime:        cfg.Runtime,
		netns:          cfg.Netns,
		cni:            cfg.CNI,
		clock:          clk,
		cleanupTimeout: cleanupTimeout,
		onTransition:   cfg.OnTransition,
		onError:        cfg.OnError,
	}, nil
}

// Run executes the unit lifecycle until terminal PhaseStopped with all acquired resources released.
// Canceling ctx initiates strict LIFO teardown using a detached bounded cleanup context.
func (r *Runner) Run(ctx context.Context) (lifecycle.State, error) {
	if !r.started.CompareAndSwap(false, true) {
		return lifecycle.InitState(), ErrAlreadyRun
	}

	spec := r.unit.Lifecycle
	startTime := r.clock.Now()
	nowMs := func() int64 {
		return max(0, r.clock.Now().Sub(startTime).Milliseconds())
	}
	reportErr := func(err error) {
		if r.onError != nil && err != nil {
			r.onError(err)
		}
	}

	st := lifecycle.InitState()
	apply := func(ev lifecycle.Event) {
		st = lifecycle.ApplyEvent(spec, st, ev)
		if r.onTransition != nil {
			r.onTransition(st)
		}
	}

	stopSignaled := false
	if ctx.Err() != nil {
		apply(lifecycle.Event{Kind: lifecycle.EvStopRequested, NowMs: nowMs()})
		return st, ctx.Err()
	}
	apply(lifecycle.Event{Kind: lifecycle.EvStartRequested, NowMs: nowMs()})

	var (
		netnsPath     string
		cniLastResult []byte
		exitCh        <-chan ExitResult
		setupErr      error
		cleanupErrs   []error
	)

	recordSetupErr := func(err error) {
		if setupErr == nil {
			setupErr = err
		}
		reportErr(err)
	}
	recordCleanupErr := func(err error) {
		cleanupErrs = append(cleanupErrs, err)
		reportErr(err)
	}

	cleanupOpCtx := func() (context.Context, context.CancelFunc) {
		return sim.WithTimeout(context.WithoutCancel(ctx), r.clock, r.cleanupTimeout)
	}

	for st.Phase != lifecycle.PhaseStopped {
		if !stopSignaled {
			select {
			case <-ctx.Done():
				stopSignaled = true
				apply(lifecycle.Event{Kind: lifecycle.EvStopRequested, NowMs: nowMs()})
				continue
			default:
			}
		}
		if exitCh != nil {
			select {
			case res, ok := <-exitCh:
				exitCh = nil
				var code uint32
				if ok {
					code = res.ExitCode
					if res.Err != nil {
						recordCleanupErr(fmt.Errorf("wait task %s: %w", r.unit.ID, res.Err))
					}
				}
				apply(lifecycle.Event{Kind: lifecycle.EvTaskExited, NowMs: nowMs(), ExitCode: code})
				continue
			default:
			}
		}

		now := nowMs()
		act := lifecycle.NextStep(spec, st, now)
		switch act.Kind {
		case lifecycle.ActionNone:
			return st, combineRunErrors(setupErr, cleanupErrs)

		case lifecycle.ActionWait:
			var timerCh <-chan time.Time
			if act.WaitUntilMs > 0 {
				waitMs := act.WaitUntilMs - now
				if waitMs <= 0 {
					continue
				}
				timerCh = r.clock.After(time.Duration(waitMs) * time.Millisecond)
			}
			var stopCh <-chan struct{}
			if !stopSignaled {
				stopCh = ctx.Done()
			}
			select {
			case res, ok := <-exitCh:
				exitCh = nil
				var code uint32
				if ok {
					code = res.ExitCode
					if res.Err != nil {
						recordCleanupErr(fmt.Errorf("wait task %s: %w", r.unit.ID, res.Err))
					}
				}
				apply(lifecycle.Event{Kind: lifecycle.EvTaskExited, NowMs: nowMs(), ExitCode: code})
			case <-stopCh:
				stopSignaled = true
				apply(lifecycle.Event{Kind: lifecycle.EvStopRequested, NowMs: nowMs()})
			case <-timerCh:
			}

		case lifecycle.ActionCreateNetns:
			path, err := r.netns.Create(ctx, r.unit.ID)
			if err != nil {
				recordSetupErr(fmt.Errorf("create netns %s: %w", r.unit.ID, err))
				apply(lifecycle.Event{Kind: lifecycle.EvNetnsCreateFailed, NowMs: nowMs()})
				continue
			}
			netnsPath = path
			apply(lifecycle.Event{Kind: lifecycle.EvNetnsCreated, NowMs: nowMs()})

		case lifecycle.ActionAddCNI:
			res, err := r.cni.Add(ctx, act.CNIIndex, r.unit.ID, netnsPath, cniLastResult)
			if err != nil {
				recordSetupErr(fmt.Errorf("cni add step %d for %s: %w", act.CNIIndex, r.unit.ID, err))
				apply(lifecycle.Event{Kind: lifecycle.EvCNIAddFailed, NowMs: nowMs(), CNIIndex: act.CNIIndex})
				continue
			}
			if len(res) > 0 {
				cniLastResult = res
			}
			apply(lifecycle.Event{Kind: lifecycle.EvCNIAdded, NowMs: nowMs(), CNIIndex: act.CNIIndex})

		case lifecycle.ActionEnsureImage:
			if err := r.runtime.EnsureImage(ctx, r.unit.Image); err != nil {
				recordSetupErr(fmt.Errorf("ensure image %s: %w", r.unit.Image, err))
				apply(lifecycle.Event{Kind: lifecycle.EvImageFailed, NowMs: nowMs()})
				continue
			}
			apply(lifecycle.Event{Kind: lifecycle.EvImageReady, NowMs: nowMs()})

		case lifecycle.ActionCreateContainer:
			err := r.runtime.CreateContainer(ctx, ContainerSpec{
				ID:         r.unit.ID,
				Image:      r.unit.Image,
				Command:    r.unit.Command,
				Args:       r.unit.Args,
				Env:        r.unit.Env,
				Mounts:     r.unit.Mounts,
				NetnsPath:  netnsPath,
				WorkingDir: r.unit.WorkingDir,
			})
			if err != nil {
				recordSetupErr(fmt.Errorf("create container %s: %w", r.unit.ID, err))
				apply(lifecycle.Event{Kind: lifecycle.EvContainerCreateFailed, NowMs: nowMs()})
				continue
			}
			apply(lifecycle.Event{Kind: lifecycle.EvContainerCreated, NowMs: nowMs()})

		case lifecycle.ActionStartTask:
			ch, err := r.runtime.StartTask(ctx, r.unit.ID)
			if err == nil && ch == nil {
				err = errors.New("runtime returned nil exit channel")
			}
			if err != nil {
				recordSetupErr(fmt.Errorf("start task %s: %w", r.unit.ID, err))
				apply(lifecycle.Event{Kind: lifecycle.EvTaskStartFailed, NowMs: nowMs()})
				continue
			}
			exitCh = ch
			apply(lifecycle.Event{Kind: lifecycle.EvTaskStarted, NowMs: nowMs()})

		case lifecycle.ActionTermTask:
			opCtx, cancel := cleanupOpCtx()
			err := r.runtime.SignalTask(opCtx, r.unit.ID, syscall.SIGTERM)
			cancel()
			if err != nil {
				reportErr(fmt.Errorf("signal SIGTERM %s: %w", r.unit.ID, err))
			}
			apply(lifecycle.Event{Kind: lifecycle.EvTermSent, NowMs: nowMs()})

		case lifecycle.ActionKillTask:
			opCtx, cancel := cleanupOpCtx()
			err := r.runtime.SignalTask(opCtx, r.unit.ID, syscall.SIGKILL)
			cancel()
			if err != nil {
				reportErr(fmt.Errorf("signal SIGKILL %s: %w", r.unit.ID, err))
			}
			apply(lifecycle.Event{Kind: lifecycle.EvKillSent, NowMs: nowMs()})

		case lifecycle.ActionDeleteTask:
			opCtx, cancel := cleanupOpCtx()
			err := r.runtime.DeleteTask(opCtx, r.unit.ID)
			cancel()
			if err != nil {
				recordCleanupErr(fmt.Errorf("delete task %s: %w", r.unit.ID, err))
			}
			apply(lifecycle.Event{Kind: lifecycle.EvTaskDeleted, NowMs: nowMs(), OK: err == nil})

		case lifecycle.ActionDeleteContainer:
			opCtx, cancel := cleanupOpCtx()
			err := r.runtime.DeleteContainer(opCtx, r.unit.ID)
			cancel()
			if err != nil {
				recordCleanupErr(fmt.Errorf("delete container %s: %w", r.unit.ID, err))
			}
			apply(lifecycle.Event{Kind: lifecycle.EvContainerDeleted, NowMs: nowMs(), OK: err == nil})

		case lifecycle.ActionDelCNI:
			opCtx, cancel := cleanupOpCtx()
			err := r.cni.Del(opCtx, act.CNIIndex, r.unit.ID, netnsPath, cniLastResult)
			cancel()
			if err != nil {
				recordCleanupErr(fmt.Errorf("cni del step %d for %s: %w", act.CNIIndex, r.unit.ID, err))
			}
			apply(lifecycle.Event{Kind: lifecycle.EvCNIDeleted, NowMs: nowMs(), CNIIndex: act.CNIIndex, OK: err == nil})

		case lifecycle.ActionRemoveNetns:
			opCtx, cancel := cleanupOpCtx()
			err := r.netns.Remove(opCtx, r.unit.ID)
			cancel()
			if err != nil {
				recordCleanupErr(fmt.Errorf("remove netns %s: %w", r.unit.ID, err))
			}
			netnsPath = ""
			cniLastResult = nil
			apply(lifecycle.Event{Kind: lifecycle.EvNetnsRemoved, NowMs: nowMs(), OK: err == nil})
		}
	}

	return st, combineRunErrors(setupErr, cleanupErrs)
}

func combineRunErrors(setupErr error, cleanupErrs []error) error {
	if setupErr == nil && len(cleanupErrs) == 0 {
		return nil
	}
	errs := make([]error, 0, 1+len(cleanupErrs))
	if setupErr != nil {
		errs = append(errs, setupErr)
	}
	errs = append(errs, cleanupErrs...)
	return errors.Join(errs...)
}
