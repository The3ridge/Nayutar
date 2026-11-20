package storetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anishathalye/porcupine"

	"github.com/Kaikei-e/Nayutar/core/store"
)

// Workload sizes one linearizability run: Clients goroutines issue Ops calls
// each (or at least Ops calls until Disrupt returns), chosen by a generator
// seeded with Seed. When Disrupt is non-nil, failed Txn calls are recorded
// with an unknown outcome and failed reads are dropped.
type Workload struct {
	Clients int
	Ops     int
	Seed    uint64
	Disrupt func(ctx context.Context) error
}

// ErrNotLinearizable is returned by CheckLinearizable when no sequential order
// of the recorded calls explains their results.
var ErrNotLinearizable = errors.New("history is not linearizable")

const (
	linCheckLimit         = 2 * time.Minute
	linDisruptCallTimeout = 3 * time.Second
)

var (
	linKeys     = [...]string{"j/0", "k/0", "k/1"}
	linPrefixes = []string{"", "j/", "k/"}
)

type linKind uint8

const (
	linGet linKind = iota
	linRange
	linRev
	linTxn
)

type linInput struct {
	kind   linKind
	key    string
	prefix string
	limit  int
	cont   string
	cmps   []store.Cmp
	ops    []store.Op
}

type linOutput struct {
	unknown bool
	applied bool
	rev     store.Rev
	kvs     []store.KV
	cont    string
}

type linCell struct {
	present bool
	value   string
	mod     store.Rev
}

type linState struct {
	rev   store.Rev
	cells [len(linKeys)]linCell
}

func (st linState) expect(selects func(key string) bool) []store.KV {
	var kvs []store.KV
	for i, key := range linKeys {
		if c := st.cells[i]; c.present && selects(key) {
			kvs = append(kvs, store.KV{Key: key, Value: []byte(c.value), ModRev: c.mod})
		}
	}
	return kvs
}

func sameKVs(a, b []store.KV) bool {
	return slices.EqualFunc(a, b, func(x, y store.KV) bool {
		return x.Key == y.Key && x.ModRev == y.ModRev && bytes.Equal(x.Value, y.Value)
	})
}

// linStep is the sequential contract for the linearizable calls: reads at the
// store revision, CurrentRev and Txn.
func linStep(st linState, in linInput, out linOutput) (bool, linState) {
	switch in.kind {
	case linGet:
		return out.rev == st.rev && sameKVs(out.kvs, st.expect(func(key string) bool { return key == in.key })), st
	case linRange:
		all := st.expect(func(key string) bool { return strings.HasPrefix(key, in.prefix) && key > in.cont })
		wantCont := ""
		if in.limit > 0 && len(all) > in.limit {
			all = all[:in.limit]
			wantCont = all[len(all)-1].Key
		}
		return out.rev == st.rev && out.cont == wantCont && sameKVs(out.kvs, all), st
	case linRev:
		return out.rev == st.rev, st
	}
	for _, c := range in.cmps {
		if !c.Holds(st.cells[slices.Index(linKeys[:], c.Key)].mod) {
			if out.unknown {
				return true, st
			}
			return !out.applied && out.rev == st.rev, st
		}
	}
	next := st
	for _, op := range in.ops {
		i := slices.Index(linKeys[:], op.Key)
		switch {
		case op.Type == store.OpPut:
			next.cells[i] = linCell{present: true, value: string(op.Value), mod: st.rev + 1}
		case st.cells[i].present:
			next.cells[i] = linCell{}
		}
	}
	if next != st {
		next.rev = st.rev + 1
	}
	if out.unknown {
		return true, next
	}
	return out.applied && out.rev == next.rev, next
}

func describeLinOp(input, output any) string {
	in, out := input.(linInput), output.(linOutput)
	kvs := func() string {
		parts := make([]string, len(out.kvs))
		for i, kv := range out.kvs {
			parts[i] = fmt.Sprintf("%s=%q@%d", kv.Key, kv.Value, kv.ModRev)
		}
		return "[" + strings.Join(parts, " ") + "]"
	}
	switch in.kind {
	case linGet:
		return fmt.Sprintf("get(%s) -> %s rev=%d", in.key, kvs(), out.rev)
	case linRange:
		return fmt.Sprintf("range(%q, limit=%d, cont=%q) -> %s cont=%q rev=%d", in.prefix, in.limit, in.cont, kvs(), out.cont, out.rev)
	case linRev:
		return fmt.Sprintf("rev() -> %d", out.rev)
	}
	var b strings.Builder
	b.WriteString("txn(")
	for _, c := range in.cmps {
		fmt.Fprintf(&b, "if mod(%s) %s %d; ", c.Key, [...]string{"?", "==", "!=", "<", ">"}[c.Op], c.ModRev)
	}
	for _, op := range in.ops {
		if op.Type == store.OpPut {
			fmt.Fprintf(&b, "put %s=%q; ", op.Key, op.Value)
		} else {
			fmt.Fprintf(&b, "delete %s; ", op.Key)
		}
	}
	if out.unknown {
		b.WriteString(") -> unknown")
	} else {
		fmt.Fprintf(&b, ") -> applied=%v rev=%d", out.applied, out.rev)
	}
	return b.String()
}

func linCall(ctx context.Context, s store.Store, in linInput) (linOutput, error) {
	switch in.kind {
	case linGet:
		kv, rev, err := s.Get(ctx, in.key, 0)
		if errors.Is(err, store.ErrNotFound) {
			return linOutput{rev: rev}, nil
		}
		return linOutput{rev: rev, kvs: []store.KV{kv}}, err
	case linRange:
		kvs, rev, next, err := s.Range(ctx, in.prefix, 0, in.limit, in.cont)
		return linOutput{rev: rev, kvs: kvs, cont: next}, err
	case linRev:
		rev, err := s.CurrentRev(ctx)
		return linOutput{rev: rev}, err
	default:
		applied, rev, err := s.Txn(ctx, in.cmps, in.ops)
		return linOutput{applied: applied, rev: rev}, err
	}
}

// linClient generates one client's calls. It remembers the mod revisions it
// has seen, so that most of its comparisons are the optimistic-concurrency
// kind that only one of several racing writers may win.
type linClient struct {
	id         int
	rng        *rand.Rand
	known      [len(linKeys)]store.Rev
	lastPrefix string
	lastCont   string
	seq        int
}

func (c *linClient) next() linInput {
	switch roll := c.rng.IntN(100); {
	case roll < 25:
		return linInput{kind: linGet, key: linKeys[c.rng.IntN(len(linKeys))]}
	case roll < 42:
		if c.lastCont != "" && c.rng.IntN(2) == 0 {
			in := linInput{
				kind:   linRange,
				prefix: c.lastPrefix,
				limit:  [...]int{0, 1, 2}[c.rng.IntN(3)],
				cont:   c.lastCont,
			}
			c.lastCont = ""
			return in
		}
		prefix := linPrefixes[c.rng.IntN(len(linPrefixes))]
		limit := [...]int{0, 0, 1, 2}[c.rng.IntN(4)]
		cont := ""
		if c.rng.IntN(4) == 0 {
			for _, k := range linKeys {
				if strings.HasPrefix(k, prefix) {
					cont = k
					break
				}
			}
		}
		return linInput{kind: linRange, prefix: prefix, limit: limit, cont: cont}
	case roll < 50:
		return linInput{kind: linRev}
	}
	in := linInput{kind: linTxn}
	order := c.rng.Perm(len(linKeys))
	for _, i := range order[:1+c.rng.IntN(2)] {
		if c.rng.IntN(4) == 0 {
			in.ops = append(in.ops, store.Op{Type: store.OpDelete, Key: linKeys[i]})
			continue
		}
		c.seq++
		in.ops = append(in.ops, store.Op{Type: store.OpPut, Key: linKeys[i], Value: fmt.Appendf(nil, "c%d-%d", c.id, c.seq)})
	}
	switch roll := c.rng.IntN(100); {
	case roll < 40:
		i := slices.Index(linKeys[:], in.ops[0].Key)
		in.cmps = []store.Cmp{{Key: linKeys[i], Op: store.CmpEqual, ModRev: c.known[i]}}
	case roll < 65:
		i, j := order[0], order[1]
		secondOp := store.CmpEqual
		if c.rng.IntN(4) == 0 {
			secondOp = [...]store.CmpOp{store.CmpNotEqual, store.CmpLess, store.CmpGreater}[c.rng.IntN(3)]
		}
		in.cmps = []store.Cmp{
			{Key: linKeys[i], Op: store.CmpEqual, ModRev: c.known[i]},
			{Key: linKeys[j], Op: secondOp, ModRev: c.known[j]},
		}
	case roll < 80:
		i := c.rng.IntN(len(linKeys))
		op := [...]store.CmpOp{store.CmpNotEqual, store.CmpLess, store.CmpGreater}[c.rng.IntN(3)]
		in.cmps = []store.Cmp{{Key: linKeys[i], Op: op, ModRev: c.known[i]}}
	}
	return in
}

func (c *linClient) learn(in linInput, out linOutput) {
	switch in.kind {
	case linGet:
		c.known[slices.Index(linKeys[:], in.key)] = 0
		for _, kv := range out.kvs {
			c.known[slices.Index(linKeys[:], kv.Key)] = kv.ModRev
		}
	case linRange:
		for _, kv := range out.kvs {
			c.known[slices.Index(linKeys[:], kv.Key)] = kv.ModRev
		}
		if out.cont != "" {
			c.lastPrefix, c.lastCont = in.prefix, out.cont
		}
	case linTxn:
		if !out.applied {
			return
		}
		for _, op := range in.ops {
			i := slices.Index(linKeys[:], op.Key)
			if op.Type == store.OpPut {
				c.known[i] = out.rev
			} else {
				c.known[i] = 0
			}
		}
	}
}

// CheckLinearizable runs the workload against s, which must hold no keys and
// have no other writer, and checks the recorded history against the
// sequential contract. It returns an error wrapping ErrNotLinearizable when
// the history has no valid order, and a different error when a call fails,
// when fewer than a quarter of the calls overlap a call of another client, or
// when the check does not finish.
func CheckLinearizable(ctx context.Context, s store.Store, w Workload) error {
	base, err := s.CurrentRev(ctx)
	if err != nil {
		return err
	}

	workCtx, cancelWork := context.WithCancel(ctx)
	defer cancelWork()

	disruptDone := make(chan struct{})
	var disruptErr error
	if w.Disrupt != nil {
		go func() {
			defer close(disruptDone)
			disruptErr = w.Disrupt(workCtx)
			if disruptErr != nil {
				cancelWork()
			}
		}()
	} else {
		close(disruptDone)
	}

	// A shared counter orders calls and returns. An operation whose return
	// tick precedes another's call tick really finished before the other
	// began, which is all the checker may assume. Yielding after the call
	// tick only widens a recorded interval around the real call, and lets
	// other clients start theirs inside it even on one CPU.
	var clock atomic.Int64
	histories := make([][]porcupine.Operation, w.Clients)
	errs := make([]error, w.Clients)
	var wg sync.WaitGroup
	for id := range w.Clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &linClient{id: id, rng: rand.New(rand.NewPCG(w.Seed, uint64(id)))}
			for calls := 0; ; calls++ {
				if w.Disrupt == nil {
					if calls >= w.Ops {
						return
					}
				} else {
					if calls >= w.Ops {
						select {
						case <-disruptDone:
							return
						default:
						}
					}
					if calls > 0 {
						delay := 5*time.Millisecond + time.Duration(client.rng.IntN(5001))*time.Microsecond
						timer := time.NewTimer(delay)
						select {
						case <-workCtx.Done():
							timer.Stop()
							return
						case <-timer.C:
						}
					}
				}
				in := client.next()
				call := clock.Add(1)
				runtime.Gosched()
				callCtx := workCtx
				var callCancel context.CancelFunc
				if w.Disrupt != nil {
					callCtx, callCancel = context.WithTimeout(workCtx, linDisruptCallTimeout)
				}
				out, err := linCall(callCtx, s, in)
				if callCancel != nil {
					callCancel()
				}
				ret := clock.Add(1)
				if err != nil {
					if w.Disrupt == nil {
						errs[id] = fmt.Errorf("client %d: %s: %w", id, describeLinOp(in, out), err)
						return
					}
					if workCtx.Err() != nil {
						return
					}
					if in.kind == linTxn {
						histories[id] = append(histories[id], porcupine.Operation{
							ClientId: id,
							Input:    in,
							Call:     call,
							Output:   linOutput{unknown: true},
						})
					}
					continue
				}
				client.learn(in, out)
				histories[id] = append(histories[id], porcupine.Operation{ClientId: id, Input: in, Call: call, Output: out, Return: ret})
			}
		}()
	}
	wg.Wait()
	<-disruptDone
	if disruptErr != nil {
		return disruptErr
	}
	if w.Disrupt != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}

	endTick := clock.Add(1)
	history := slices.Concat(histories...)
	for i := range history {
		if history[i].Output.(linOutput).unknown {
			history[i].Return = endTick
		}
	}
	if w.Disrupt == nil {
		if len(history) != w.Clients*w.Ops || len(history) == 0 {
			return fmt.Errorf("recorded %d operations, want %d", len(history), w.Clients*w.Ops)
		}
	} else if len(history) == 0 {
		return errors.New("recorded 0 operations")
	}
	if concurrent := countConcurrent(history); concurrent*4 < len(history) {
		return fmt.Errorf("workload was effectively sequential: only %d of %d operations overlap one of another client (seed %d)", concurrent, len(history), w.Seed)
	}
	model := porcupine.Model{
		Init: func() any { return linState{rev: base} },
		Step: func(state, input, output any) (bool, any) {
			return linStep(state.(linState), input.(linInput), output.(linOutput))
		},
		Equal:             func(a, b any) bool { return a.(linState) == b.(linState) },
		DescribeOperation: describeLinOp,
	}
	switch result := porcupine.CheckOperationsTimeout(model, history, linCheckLimit); result {
	case porcupine.Ok:
		return nil
	case porcupine.Illegal:
		slices.SortFunc(history, func(a, b porcupine.Operation) int { return int(a.Call - b.Call) })
		var b strings.Builder
		for _, op := range history {
			fmt.Fprintf(&b, "\n  [%d..%d] client %d: %s", op.Call, op.Return, op.ClientId, describeLinOp(op.Input, op.Output))
		}
		return fmt.Errorf("%w (%d operations from revision %d, seed %d):%s", ErrNotLinearizable, len(history), base, w.Seed, b.String())
	default:
		return fmt.Errorf("linearizability check did not finish within %s (%d operations, seed %d)", linCheckLimit, len(history), w.Seed)
	}
}

// countConcurrent counts the operations whose interval overlaps that of an
// operation of another client.
func countConcurrent(history []porcupine.Operation) int {
	n := 0
	for _, a := range history {
		for _, b := range history {
			if a.ClientId != b.ClientId && a.Call < b.Return && b.Call < a.Return {
				n++
				break
			}
		}
	}
	return n
}

// RunLinearizability checks stores made by newStore under concurrent clients.
func RunLinearizability(t *testing.T, newStore Factory) {
	for seed := uint64(1); seed <= 5; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			w := Workload{Clients: 8, Ops: 60, Seed: seed}
			if err := CheckLinearizable(ctx, newStore(t), w); err != nil {
				t.Fatal(err)
			}
		})
	}
}
