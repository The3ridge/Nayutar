//go:build drt

package drt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
)

type storeCmp struct {
	Key string `json:"key"`
	Cmp string `json:"cmp"`
	Mod int64  `json:"mod"`
}

type storeWrite struct {
	Type  string `json:"type"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

type storeOp struct {
	Op     string
	Key    string
	Prefix string
	Rev    int64
	Limit  int
	Cont   string
	Cmps   []storeCmp
	Writes []storeWrite
	Watch  int
}

func (o storeOp) MarshalJSON() ([]byte, error) {
	switch o.Op {
	case "get":
		return json.Marshal(struct {
			Op  string `json:"op"`
			Key string `json:"key"`
			Rev int64  `json:"rev"`
		}{o.Op, o.Key, o.Rev})
	case "range":
		return json.Marshal(struct {
			Op     string `json:"op"`
			Prefix string `json:"prefix"`
			Rev    int64  `json:"rev"`
			Limit  int    `json:"limit"`
			Cont   string `json:"cont"`
		}{o.Op, o.Prefix, o.Rev, o.Limit, o.Cont})
	case "txn":
		cmps, writes := o.Cmps, o.Writes
		if cmps == nil {
			cmps = []storeCmp{}
		}
		if writes == nil {
			writes = []storeWrite{}
		}
		return json.Marshal(struct {
			Op   string       `json:"op"`
			Cmps []storeCmp   `json:"cmps"`
			Ops  []storeWrite `json:"ops"`
		}{o.Op, cmps, writes})
	case "rev":
		return json.Marshal(struct {
			Op string `json:"op"`
		}{o.Op})
	case "compact":
		return json.Marshal(struct {
			Op  string `json:"op"`
			Rev int64  `json:"rev"`
		}{o.Op, o.Rev})
	case "watch":
		return json.Marshal(struct {
			Op     string `json:"op"`
			Prefix string `json:"prefix"`
			Rev    int64  `json:"rev"`
		}{o.Op, o.Prefix, o.Rev})
	case "progress":
		return json.Marshal(struct {
			Op    string `json:"op"`
			Watch int    `json:"watch"`
		}{o.Op, o.Watch})
	default:
		return nil, fmt.Errorf("unknown op %q", o.Op)
	}
}

type storeCase struct {
	ID  int       `json:"id"`
	Ops []storeOp `json:"ops"`
}

type storeKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Mod   int    `json:"mod"`
}

type storeEvent struct {
	Type  string `json:"type"`
	Key   string `json:"key"`
	Value string `json:"value"`
	Rev   int    `json:"rev"`
}

type storeStep struct {
	Err      string       `json:"err"`
	OK       bool         `json:"ok"`
	Rev      int          `json:"rev"`
	KVs      []storeKV    `json:"kvs"`
	Cont     string       `json:"cont"`
	Events   []storeEvent `json:"events"`
	Progress int          `json:"progress"`
}

type storeResult struct {
	ID    int         `json:"id"`
	Steps []storeStep `json:"steps"`
}

var (
	storeKeys     = []string{"a", "a/1", "a/1/x", "a/2", "a/β", "b", "b/1", "α", "α/1"}
	storePrefixes = []string{"", "a", "a/", "a/1", "a/1/", "b", "b/1", "c", "α", "α/"}
	storeValues   = []string{"", "x", "y", "λ", "val/β"}
	storeCmpOps   = map[string]store.CmpOp{
		"eq": store.CmpEqual,
		"ne": store.CmpNotEqual,
		"lt": store.CmpLess,
		"gt": store.CmpGreater,
	}
	storeCmpNames = []string{"eq", "ne", "lt", "gt"}
)

const storeMaxWatches = 4

type rangeChain struct {
	prefix string
	rev    int64
	limit  int
	cont   string
}

func pick[T any](rng *rand.Rand, items []T) T {
	return items[rng.IntN(len(items))]
}

func chance(rng *rand.Rand, percent int) bool {
	return rng.IntN(100) < percent
}

// generateStoreCases steers its choices with an in-memory store, so that
// revisions and mod revisions land near the interesting boundaries. The
// steering store only shapes the cases; it is not the oracle.
func generateStoreCases(seed uint64, n int) []storeCase {
	rng := rand.New(rand.NewPCG(seed, 0x51ed270b6f4a7c15))
	ctx := context.Background()
	cases := make([]storeCase, n)
	for i := range n {
		shadow := memstore.New()
		current := func() int64 {
			rev, err := shadow.CurrentRev(ctx)
			if err != nil {
				panic(err)
			}
			return int64(rev)
		}
		readRev := func() int64 {
			if chance(rng, 40) {
				return 0
			}
			return 1 + rng.Int64N(current()+2)
		}
		var chain *rangeChain
		watches := 0
		dirtyWatch := make([]bool, 0, storeMaxWatches)
		markDirty := func() {
			for w := range dirtyWatch {
				dirtyWatch[w] = true
			}
		}
		numOps := rng.IntN(32)
		ops := make([]storeOp, 0, numOps)
		for range numOps {
			roll := rng.IntN(100)
			if roll >= 88 && watches == 0 {
				roll = 0
			}
			if roll >= 79 && roll < 88 && watches == storeMaxWatches {
				roll = 0
			}
			switch {
			case roll < 45:
				op := storeOp{Op: "txn"}
				for range []int{0, 0, 1, 1, 2, 2, 3, 4}[rng.IntN(8)] {
					key := pick(rng, storeKeys)
					var actual int64
					if kv, _, err := shadow.Get(ctx, key, 0); err == nil {
						actual = int64(kv.ModRev)
					}
					mod := actual
					switch rng.IntN(6) {
					case 0:
						mod = actual + 1
					case 1:
						mod = max(actual-1, 0)
					case 2:
						mod = rng.Int64N(current() + 2)
					}
					if chance(rng, 1) {
						key = ""
					}
					op.Cmps = append(op.Cmps, storeCmp{Key: key, Cmp: pick(rng, storeCmpNames), Mod: mod})
				}
				order := rng.Perm(len(storeKeys))
				for j := range []int{0, 1, 1, 2, 2, 3, 4, 5}[rng.IntN(8)] {
					w := storeWrite{Type: "put", Key: storeKeys[order[j]], Value: pick(rng, storeValues)}
					if chance(rng, 35) {
						w = storeWrite{Type: "delete", Key: w.Key}
					}
					op.Writes = append(op.Writes, w)
				}
				if len(op.Writes) >= 2 && chance(rng, 4) {
					op.Writes[1].Key = op.Writes[0].Key
				}
				if len(op.Writes) >= 1 && chance(rng, 2) {
					op.Writes[0].Key = ""
				}
				ops = append(ops, op)
				_, _, _ = shadow.Txn(ctx, toCmps(op.Cmps), toOps(op.Writes))
				markDirty()
			case roll < 57:
				key := pick(rng, storeKeys)
				if chance(rng, 2) {
					key = ""
				}
				ops = append(ops, storeOp{Op: "get", Key: key, Rev: readRev()})
			case roll < 69:
				var op storeOp
				chained := false
				if chain != nil && chance(rng, 60) {
					op = storeOp{Op: "range", Prefix: chain.prefix, Rev: chain.rev, Limit: chain.limit, Cont: chain.cont}
					chained = true
				} else {
					prefix := pick(rng, storePrefixes)
					op = storeOp{Op: "range", Prefix: prefix, Rev: readRev(), Limit: pick(rng, []int{0, 0, 1, 2, 3})}
					if chance(rng, 45) {
						if chance(rng, 60) {
							var matching []string
							if prefix != "" {
								matching = append(matching, prefix)
							}
							for _, k := range storeKeys {
								if strings.HasPrefix(k, prefix) {
									matching = append(matching, k)
								}
							}
							if len(matching) > 0 {
								op.Cont = pick(rng, matching)
							} else {
								op.Cont = pick(rng, storeKeys)
							}
						} else {
							op.Cont = pick(rng, storeKeys)
						}
					}
				}
				ops = append(ops, op)
				if _, rRev, nextCont, err := shadow.Range(ctx, op.Prefix, store.Rev(op.Rev), op.Limit, op.Cont); err == nil && nextCont != "" {
					chain = &rangeChain{prefix: op.Prefix, rev: int64(rRev), limit: op.Limit, cont: nextCont}
				} else if chained {
					chain = nil
				}
			case roll < 73:
				ops = append(ops, storeOp{Op: "rev"})
			case roll < 79:
				var rev int64
				if !chance(rng, 15) {
					rev = 1 + rng.Int64N(current()+1)
				}
				op := storeOp{Op: "compact", Rev: rev}
				ops = append(ops, op)
				_ = shadow.Compact(ctx, store.Rev(op.Rev))
				markDirty()
			case roll < 88:
				op := storeOp{Op: "watch", Prefix: pick(rng, storePrefixes)}
				if !chance(rng, 35) {
					op.Rev = 1 + rng.Int64N(current()+2)
				}
				ops = append(ops, op)
				watches++
				dirtyWatch = append(dirtyWatch, false)
			default:
				idx := rng.IntN(watches)
				if chance(rng, 75) {
					var dirty []int
					for w, d := range dirtyWatch {
						if d {
							dirty = append(dirty, w)
						}
					}
					if len(dirty) > 0 {
						idx = pick(rng, dirty)
					}
				}
				dirtyWatch[idx] = false
				ops = append(ops, storeOp{Op: "progress", Watch: idx})
			}
		}
		cases[i] = storeCase{ID: i, Ops: ops}
	}
	return cases
}

func toCmps(cmps []storeCmp) []store.Cmp {
	out := make([]store.Cmp, len(cmps))
	for i, c := range cmps {
		out[i] = store.Cmp{Key: c.Key, Op: storeCmpOps[c.Cmp], ModRev: store.Rev(c.Mod)}
	}
	return out
}

func toOps(writes []storeWrite) []store.Op {
	out := make([]store.Op, len(writes))
	for i, w := range writes {
		if w.Type == "put" {
			out[i] = store.Op{Type: store.OpPut, Key: w.Key, Value: []byte(w.Value)}
		} else {
			out[i] = store.Op{Type: store.OpDelete, Key: w.Key}
		}
	}
	return out
}

func encodeStoreCases(t *testing.T, cases []storeCase) [][]byte {
	t.Helper()
	encoded := make([][]byte, len(cases))
	for i, tc := range cases {
		b, err := json.Marshal(tc)
		if err != nil {
			t.Fatal(err)
		}
		encoded[i] = b
	}
	return encoded
}

func storeErrName(err error) (string, error) {
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, store.ErrNotFound):
		return "not_found", nil
	case errors.Is(err, store.ErrCompacted):
		return "compacted", nil
	case errors.Is(err, store.ErrFutureRev):
		return "future_rev", nil
	case errors.Is(err, store.ErrInvalidArgument):
		return "invalid", nil
	default:
		return "", err
	}
}

func toStoreKVs(kvs []store.KV) []storeKV {
	out := make([]storeKV, len(kvs))
	for i, kv := range kvs {
		out[i] = storeKV{Key: kv.Key, Value: string(kv.Value), Mod: int(kv.ModRev)}
	}
	return out
}

type storeWatch struct {
	stream  store.WatchStream
	fromRev store.Rev
}

// pollStoreWatch reports what the watch delivers up to the store revision. A
// store answers no progress request below fromRev, and no change at or above
// fromRev exists yet, so such a watch has nothing to report and the stream is
// left alone; a notification it sent anyway would surface as a stale progress
// revision in a later step.
func pollStoreWatch(ctx context.Context, s store.Store, w storeWatch) (storeStep, error) {
	step := storeStep{KVs: []storeKV{}, Events: []storeEvent{}}
	current, err := s.CurrentRev(ctx)
	if err != nil {
		return step, err
	}
	if w.fromRev > current {
		return step, nil
	}
	events, progress, err := storetest.Sync(ctx, s, w.stream)
	if err != nil {
		step.Err, err = storeErrName(err)
		return step, err
	}
	for _, ev := range events {
		out := storeEvent{Type: "put", Key: ev.Key, Value: string(ev.Value), Rev: int(ev.Rev)}
		if ev.Type == store.EventDelete {
			out.Type = "delete"
		} else if ev.Type != store.EventPut {
			return step, fmt.Errorf("unexpected event type %d", ev.Type)
		}
		step.Events = append(step.Events, out)
	}
	step.Progress = int(progress)
	return step, nil
}

func runStoreCase(tc storeCase, s store.Store) (storeResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res := storeResult{ID: tc.ID, Steps: make([]storeStep, 0, len(tc.Ops))}
	var watches []storeWatch
	for i, o := range tc.Ops {
		step := storeStep{KVs: []storeKV{}, Events: []storeEvent{}}
		var err error
		switch o.Op {
		case "get":
			var kv store.KV
			var rev store.Rev
			kv, rev, err = s.Get(ctx, o.Key, store.Rev(o.Rev))
			if err == nil {
				step.KVs = toStoreKVs([]store.KV{kv})
			}
			if err == nil || errors.Is(err, store.ErrNotFound) {
				step.Rev = int(rev)
			}
			step.Err, err = storeErrName(err)
		case "range":
			var kvs []store.KV
			var rev store.Rev
			var next string
			kvs, rev, next, err = s.Range(ctx, o.Prefix, store.Rev(o.Rev), o.Limit, o.Cont)
			if err == nil {
				step.KVs, step.Rev, step.Cont = toStoreKVs(kvs), int(rev), next
			}
			step.Err, err = storeErrName(err)
		case "txn":
			var ok bool
			var rev store.Rev
			ok, rev, err = s.Txn(ctx, toCmps(o.Cmps), toOps(o.Writes))
			if err == nil {
				step.OK, step.Rev = ok, int(rev)
			}
			step.Err, err = storeErrName(err)
		case "rev":
			var rev store.Rev
			rev, err = s.CurrentRev(ctx)
			step.Rev = int(rev)
		case "compact":
			step.Err, err = storeErrName(s.Compact(ctx, store.Rev(o.Rev)))
		case "watch":
			var stream store.WatchStream
			stream, err = s.Watch(ctx, o.Prefix, store.Rev(o.Rev))
			if err == nil {
				w := storeWatch{stream: stream, fromRev: store.Rev(o.Rev)}
				watches = append(watches, w)
				step, err = pollStoreWatch(ctx, s, w)
			}
		case "progress":
			step, err = pollStoreWatch(ctx, s, watches[o.Watch])
		default:
			err = fmt.Errorf("unknown op %q", o.Op)
		}
		if err != nil {
			return res, fmt.Errorf("step[%d] %s: %w", i, o.Op, err)
		}
		res.Steps = append(res.Steps, step)
	}
	return res, nil
}

func decodeObject(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := decodeStrictJSON(raw, &obj); err != nil {
		return nil, err
	}
	if len(obj) != len(keys) {
		return nil, fmt.Errorf("object must have exact keys %v: %s", keys, raw)
	}
	for _, key := range keys {
		if _, ok := obj[key]; !ok {
			return nil, fmt.Errorf("object must have exact keys %v: %s", keys, raw)
		}
	}
	return obj, nil
}

func decodeString(raw json.RawMessage) (string, error) {
	var s string
	if err := decodeStrictJSON(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

func decodeStoreResult(line []byte) (storeResult, error) {
	trimmed := bytes.TrimSpace(line)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rawTop json.RawMessage
	if err := dec.Decode(&rawTop); err != nil {
		return storeResult{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return storeResult{}, fmt.Errorf("unexpected trailing data in result line: %v", err)
	}
	top, err := decodeObject(rawTop, "id", "steps")
	if err != nil {
		return storeResult{}, err
	}
	id, err := decodeNonNegInt(top["id"])
	if err != nil {
		return storeResult{}, fmt.Errorf("invalid id: %w", err)
	}
	var rawSteps []json.RawMessage
	if err := decodeStrictJSON(top["steps"], &rawSteps); err != nil {
		return storeResult{}, fmt.Errorf("invalid steps: %w", err)
	}
	res := storeResult{ID: id, Steps: make([]storeStep, len(rawSteps))}
	for i, rs := range rawSteps {
		step, err := decodeStoreStep(rs)
		if err != nil {
			return storeResult{}, fmt.Errorf("step[%d]: %w", i, err)
		}
		res.Steps[i] = step
	}
	return res, nil
}

func decodeStoreStep(raw json.RawMessage) (storeStep, error) {
	obj, err := decodeObject(raw, "err", "ok", "rev", "kvs", "cont", "events", "progress")
	if err != nil {
		return storeStep{}, err
	}
	step := storeStep{KVs: []storeKV{}, Events: []storeEvent{}}
	if step.Err, err = decodeString(obj["err"]); err != nil {
		return storeStep{}, fmt.Errorf("err: %w", err)
	}
	switch step.Err {
	case "", "not_found", "compacted", "future_rev", "invalid":
	default:
		return storeStep{}, fmt.Errorf("unknown err %q", step.Err)
	}
	if err := decodeStrictJSON(obj["ok"], &step.OK); err != nil {
		return storeStep{}, fmt.Errorf("ok: %w", err)
	}
	if step.Rev, err = decodeNonNegInt(obj["rev"]); err != nil {
		return storeStep{}, fmt.Errorf("rev: %w", err)
	}
	if step.Cont, err = decodeString(obj["cont"]); err != nil {
		return storeStep{}, fmt.Errorf("cont: %w", err)
	}
	if step.Progress, err = decodeNonNegInt(obj["progress"]); err != nil {
		return storeStep{}, fmt.Errorf("progress: %w", err)
	}
	var rawKVs, rawEvents []json.RawMessage
	if err := decodeStrictJSON(obj["kvs"], &rawKVs); err != nil {
		return storeStep{}, fmt.Errorf("kvs: %w", err)
	}
	for i, rk := range rawKVs {
		kvObj, err := decodeObject(rk, "key", "value", "mod")
		if err != nil {
			return storeStep{}, fmt.Errorf("kvs[%d]: %w", i, err)
		}
		var kv storeKV
		if kv.Key, err = decodeString(kvObj["key"]); err != nil {
			return storeStep{}, fmt.Errorf("kvs[%d].key: %w", i, err)
		}
		if kv.Value, err = decodeString(kvObj["value"]); err != nil {
			return storeStep{}, fmt.Errorf("kvs[%d].value: %w", i, err)
		}
		if kv.Mod, err = decodeNonNegInt(kvObj["mod"]); err != nil {
			return storeStep{}, fmt.Errorf("kvs[%d].mod: %w", i, err)
		}
		step.KVs = append(step.KVs, kv)
	}
	if err := decodeStrictJSON(obj["events"], &rawEvents); err != nil {
		return storeStep{}, fmt.Errorf("events: %w", err)
	}
	for i, re := range rawEvents {
		evObj, err := decodeObject(re, "type", "key", "value", "rev")
		if err != nil {
			return storeStep{}, fmt.Errorf("events[%d]: %w", i, err)
		}
		var ev storeEvent
		if ev.Type, err = decodeString(evObj["type"]); err != nil {
			return storeStep{}, fmt.Errorf("events[%d].type: %w", i, err)
		}
		if ev.Type != "put" && ev.Type != "delete" {
			return storeStep{}, fmt.Errorf("events[%d]: unknown type %q", i, ev.Type)
		}
		if ev.Key, err = decodeString(evObj["key"]); err != nil {
			return storeStep{}, fmt.Errorf("events[%d].key: %w", i, err)
		}
		if ev.Value, err = decodeString(evObj["value"]); err != nil {
			return storeStep{}, fmt.Errorf("events[%d].value: %w", i, err)
		}
		if ev.Rev, err = decodeNonNegInt(evObj["rev"]); err != nil {
			return storeStep{}, fmt.Errorf("events[%d].rev: %w", i, err)
		}
		step.Events = append(step.Events, ev)
	}
	return step, nil
}

func compareStoreResult(want, got storeResult) string {
	if want.ID != got.ID {
		return fmt.Sprintf("id mismatch: lean=%d go=%d", want.ID, got.ID)
	}
	if len(want.Steps) != len(got.Steps) {
		return fmt.Sprintf("steps len mismatch: lean=%d go=%d", len(want.Steps), len(got.Steps))
	}
	for i := range want.Steps {
		w, g := want.Steps[i], got.Steps[i]
		at := fmt.Sprintf("step[%d]", i)
		switch {
		case w.Err != g.Err:
			return fmt.Sprintf("%s.err mismatch: lean=%q go=%q", at, w.Err, g.Err)
		case w.OK != g.OK:
			return fmt.Sprintf("%s.ok mismatch: lean=%v go=%v", at, w.OK, g.OK)
		case w.Rev != g.Rev:
			return fmt.Sprintf("%s.rev mismatch: lean=%d go=%d", at, w.Rev, g.Rev)
		case w.Cont != g.Cont:
			return fmt.Sprintf("%s.cont mismatch: lean=%q go=%q", at, w.Cont, g.Cont)
		case w.Progress != g.Progress:
			return fmt.Sprintf("%s.progress mismatch: lean=%d go=%d", at, w.Progress, g.Progress)
		case len(w.KVs) != len(g.KVs):
			return fmt.Sprintf("%s.kvs len mismatch: lean=%d go=%d", at, len(w.KVs), len(g.KVs))
		case len(w.Events) != len(g.Events):
			return fmt.Sprintf("%s.events len mismatch: lean=%d go=%d", at, len(w.Events), len(g.Events))
		}
		for j := range w.KVs {
			if w.KVs[j] != g.KVs[j] {
				return fmt.Sprintf("%s.kvs[%d] mismatch: lean=%+v go=%+v", at, j, w.KVs[j], g.KVs[j])
			}
		}
		for j := range w.Events {
			if w.Events[j] != g.Events[j] {
				return fmt.Sprintf("%s.events[%d] mismatch: lean=%+v go=%+v", at, j, w.Events[j], g.Events[j])
			}
		}
	}
	return ""
}

func cloneStoreResult(r storeResult) storeResult {
	out := storeResult{ID: r.ID, Steps: make([]storeStep, len(r.Steps))}
	for i, s := range r.Steps {
		s.KVs = append([]storeKV{}, s.KVs...)
		s.Events = append([]storeEvent{}, s.Events...)
		out.Steps[i] = s
	}
	return out
}

func leanStoreResults(t *testing.T, cases []storeCase) ([][]byte, [][]byte, []storeResult) {
	t.Helper()
	encoded := encodeStoreCases(t, cases)
	lines, err := RunModel("store", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(lines), len(cases))
	}
	results := make([]storeResult, len(lines))
	for i, line := range lines {
		res, err := decodeStoreResult(line)
		if err != nil {
			t.Fatalf("id=%d decode error: %v\ncase: %s\nlean: %s", cases[i].ID, err, encoded[i], line)
		}
		results[i] = res
	}
	return encoded, lines, results
}

// checkStoreAgainstLean runs every case on a store from newStore and requires
// each step to equal the Lean model's. newStore returns a store that looks
// empty and sits at revision 1, the model's initial state.
func checkStoreAgainstLean(t *testing.T, testName string, newStore func(t *testing.T, id int) store.Store) {
	t.Helper()
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateStoreCases(cfg.Seed, cfg.Cases)
	encoded, leanLines, leanResults := leanStoreResults(t, cases)

	repro := fmt.Sprintf("NAYUTAR_DRT_SEED=%d NAYUTAR_DRT_CASES=%d go test -tags drt -run '^%s$' ./core/drt/...", cfg.Seed, cfg.Cases, testName)
	totalSteps := 0
	for i, tc := range cases {
		goRes, err := runStoreCase(tc, newStore(t, tc.ID))
		if err != nil {
			t.Fatalf("seed=%d id=%d store error: %v\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, err, encoded[i], leanLines[i], repro)
		}
		if diff := compareStoreResult(leanResults[i], goRes); diff != "" {
			goJSON, marshalErr := json.Marshal(goRes)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			t.Fatalf("seed=%d id=%d mismatch (%s)\ncase: %s\nlean: %s\ngo:   %s\nreproduce: %s",
				cfg.Seed, tc.ID, diff, encoded[i], leanLines[i], goJSON, repro)
		}
		totalSteps += len(tc.Ops)
	}
	if totalSteps == 0 {
		t.Fatal("zero ops exercised across all cases")
	}
	t.Logf("compared %d cases and %d steps (seed=%d)", len(cases), totalSteps, cfg.Seed)
}

func TestStoreMatchesLeanModel(t *testing.T) {
	checkStoreAgainstLean(t, "TestStoreMatchesLeanModel", func(*testing.T, int) store.Store {
		return memstore.New()
	})
}

func TestStoreCasesReachEveryOutcome(t *testing.T) {
	cases := generateStoreCases(1, 1000)
	_, _, results := leanStoreResults(t, cases)

	seen := map[string]int{}
	for i, tc := range cases {
		for j, o := range tc.Ops {
			s := results[i].Steps[j]
			seen[o.Op+"/"+s.Err]++
			if o.Op == "txn" && s.Err == "" {
				seen[fmt.Sprintf("txn/ok=%v", s.OK)]++
				if s.OK && j > 0 && s.Rev == lastRev(results[i].Steps[:j]) {
					seen["txn/applied-without-change"]++
				}
			}
			if o.Op == "range" && s.Cont != "" {
				seen["range/continued"]++
			}
			if o.Op == "range" && o.Cont != "" && len(s.KVs) > 0 {
				seen["range/resumed"]++
			}
			if (o.Op == "watch" || o.Op == "progress") && s.Err == "" {
				if s.Progress == 0 {
					seen["watch/unanswered"]++
				}
				if o.Op == "watch" && len(s.Events) > 0 {
					seen["watch/replayed"]++
				}
				if o.Op == "progress" && len(s.Events) > 0 {
					seen["watch/live"]++
				}
				for k := 1; k < len(s.Events); k++ {
					if s.Events[k].Rev == s.Events[k-1].Rev {
						seen["watch/multi-key-revision"]++
					}
				}
				for _, ev := range s.Events {
					seen["watch/"+ev.Type]++
				}
			}
		}
	}
	for _, outcome := range []string{
		"get/", "get/not_found", "get/compacted", "get/future_rev", "get/invalid",
		"range/", "range/compacted", "range/future_rev", "range/invalid", "range/continued", "range/resumed",
		"txn/", "txn/invalid", "txn/ok=true", "txn/ok=false", "txn/applied-without-change",
		"rev/",
		"compact/", "compact/compacted", "compact/future_rev", "compact/invalid",
		"watch/", "watch/compacted", "progress/", "progress/compacted",
		"watch/unanswered", "watch/replayed", "watch/live", "watch/multi-key-revision", "watch/put", "watch/delete",
	} {
		if seen[outcome] == 0 {
			t.Errorf("no case reaches outcome %q", outcome)
		}
	}
}

func lastRev(steps []storeStep) int {
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Rev != 0 {
			return steps[i].Rev
		}
	}
	return 0
}

func TestStoreComparisonDetectsEveryFieldMutation(t *testing.T) {
	cases := generateStoreCases(1, 200)
	_, leanLines, results := leanStoreResults(t, cases)

	applied := map[string]int{}
	assertDetects := func(kind string, orig storeResult, mutate func(r *storeResult)) {
		t.Helper()
		mut := cloneStoreResult(orig)
		mutate(&mut)
		applied[kind]++
		if compareStoreResult(orig, mut) == "" {
			t.Fatalf("mutation %q on case %d was not detected", kind, orig.ID)
		}
	}

	for _, orig := range results {
		assertDetects("id+1", orig, func(r *storeResult) { r.ID++ })
		if n := len(orig.Steps); n > 0 {
			assertDetects("drop-last-step", orig, func(r *storeResult) { r.Steps = r.Steps[:n-1] })
			assertDetects("append-duplicate-last-step", orig, func(r *storeResult) { r.Steps = append(r.Steps, r.Steps[n-1]) })
		}
		for j, s := range orig.Steps {
			if s.Err == "" {
				assertDetects("err-set", orig, func(r *storeResult) { r.Steps[j].Err = "invalid" })
			} else {
				assertDetects("err-cleared", orig, func(r *storeResult) { r.Steps[j].Err = "" })
				assertDetects("err-changed", orig, func(r *storeResult) { r.Steps[j].Err += "x" })
			}
			assertDetects("flip-ok", orig, func(r *storeResult) { r.Steps[j].OK = !r.Steps[j].OK })
			assertDetects("rev+1", orig, func(r *storeResult) { r.Steps[j].Rev++ })
			assertDetects("cont-changed", orig, func(r *storeResult) { r.Steps[j].Cont += "x" })
			assertDetects("progress+1", orig, func(r *storeResult) { r.Steps[j].Progress++ })
			assertDetects("kvs-append", orig, func(r *storeResult) { r.Steps[j].KVs = append(r.Steps[j].KVs, storeKV{Key: "z"}) })
			assertDetects("events-append", orig, func(r *storeResult) {
				r.Steps[j].Events = append(r.Steps[j].Events, storeEvent{Type: "put", Key: "z"})
			})
			if n := len(s.KVs); n > 0 {
				assertDetects("kvs-drop-last", orig, func(r *storeResult) { r.Steps[j].KVs = r.Steps[j].KVs[:n-1] })
				assertDetects("kv-key", orig, func(r *storeResult) { r.Steps[j].KVs[n-1].Key += "x" })
				assertDetects("kv-value", orig, func(r *storeResult) { r.Steps[j].KVs[n-1].Value += "x" })
				assertDetects("kv-mod+1", orig, func(r *storeResult) { r.Steps[j].KVs[n-1].Mod++ })
			}
			if n := len(s.KVs); n > 1 {
				assertDetects("kvs-swap", orig, func(r *storeResult) {
					kvs := r.Steps[j].KVs
					kvs[0], kvs[n-1] = kvs[n-1], kvs[0]
				})
			}
			if n := len(s.Events); n > 0 {
				assertDetects("events-drop-last", orig, func(r *storeResult) { r.Steps[j].Events = r.Steps[j].Events[:n-1] })
				assertDetects("event-type", orig, func(r *storeResult) {
					if r.Steps[j].Events[0].Type == "put" {
						r.Steps[j].Events[0].Type = "delete"
					} else {
						r.Steps[j].Events[0].Type = "put"
					}
				})
				assertDetects("event-key", orig, func(r *storeResult) { r.Steps[j].Events[n-1].Key += "x" })
				assertDetects("event-value", orig, func(r *storeResult) { r.Steps[j].Events[n-1].Value += "x" })
				assertDetects("event-rev+1", orig, func(r *storeResult) { r.Steps[j].Events[n-1].Rev++ })
			}
		}
	}
	for _, kind := range []string{
		"id+1", "drop-last-step", "append-duplicate-last-step",
		"err-set", "err-cleared", "err-changed", "flip-ok", "rev+1", "cont-changed", "progress+1",
		"kvs-append", "kvs-drop-last", "kv-key", "kv-value", "kv-mod+1", "kvs-swap",
		"events-append", "events-drop-last", "event-type", "event-key", "event-value", "event-rev+1",
	} {
		if applied[kind] == 0 {
			t.Errorf("mutation %q was never applicable across %d cases", kind, len(cases))
		}
	}

	const emptyStep = `{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[],"progress":0}`
	var raw map[string]any
	if err := json.Unmarshal(leanLines[0], &raw); err != nil {
		t.Fatal(err)
	}
	raw["extra"] = 1
	withTopExtra, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	invalidLines := []struct {
		name string
		line string
	}{
		{"unknown top-level field", string(withTopExtra)},
		{"missing id", `{"steps":[]}`},
		{"missing steps", `{"id":0}`},
		{"null steps", `{"id":0,"steps":null}`},
		{"null id", `{"id":null,"steps":[]}`},
		{"array instead of object", `[]`},
		{"trailing object", `{"id":0,"steps":[]} {"id":1,"steps":[]}`},
		{"empty step object", `{"id":0,"steps":[{}]}`},
		{"unknown step field", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[],"progress":0,"extra":1}]}`},
		{"missing progress", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[]}]}`},
		{"uppercase step keys", `{"id":0,"steps":[{"Err":"","OK":false,"Rev":0,"KVs":[],"Cont":"","Events":[],"Progress":0}]}`},
		{"unknown err name", `{"id":0,"steps":[{"err":"boom","ok":false,"rev":0,"kvs":[],"cont":"","events":[],"progress":0}]}`},
		{"null err", `{"id":0,"steps":[{"err":null,"ok":false,"rev":0,"kvs":[],"cont":"","events":[],"progress":0}]}`},
		{"null ok", `{"id":0,"steps":[{"err":"","ok":null,"rev":0,"kvs":[],"cont":"","events":[],"progress":0}]}`},
		{"negative rev", `{"id":0,"steps":[{"err":"","ok":false,"rev":-1,"kvs":[],"cont":"","events":[],"progress":0}]}`},
		{"float progress", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[],"progress":1.5}]}`},
		{"null kvs", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":null,"cont":"","events":[],"progress":0}]}`},
		{"null events", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":null,"progress":0}]}`},
		{"kv without mod", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[{"key":"a","value":""}],"cont":"","events":[],"progress":0}]}`},
		{"kv with extra field", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[{"key":"a","value":"","mod":1,"x":1}],"cont":"","events":[],"progress":0}]}`},
		{"kv with numeric value", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[{"key":"a","value":1,"mod":1}],"cont":"","events":[],"progress":0}]}`},
		{"event without rev", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[{"type":"put","key":"a","value":""}],"progress":0}]}`},
		{"event with unknown type", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[{"type":"progress","key":"","value":"","rev":1}],"progress":0}]}`},
	}
	for _, tc := range invalidLines {
		if _, err := decodeStoreResult([]byte(tc.line)); err == nil {
			t.Errorf("decodeStoreResult accepted %s: %s", tc.name, tc.line)
		}
	}

	valid := `{"id":3,"steps":[` + emptyStep + `,{"err":"not_found","ok":true,"rev":2,"kvs":[{"key":"a","value":"x","mod":2}],"cont":"a","events":[{"type":"delete","key":"b","value":"","rev":3}],"progress":4}]}`
	got, err := decodeStoreResult([]byte(valid))
	if err != nil {
		t.Fatalf("decodeStoreResult(%s): %v", valid, err)
	}
	want := storeResult{ID: 3, Steps: []storeStep{
		{KVs: []storeKV{}, Events: []storeEvent{}},
		{
			Err: "not_found", OK: true, Rev: 2, Cont: "a", Progress: 4,
			KVs:    []storeKV{{Key: "a", Value: "x", Mod: 2}},
			Events: []storeEvent{{Type: "delete", Key: "b", Rev: 3}},
		},
	}}
	if diff := compareStoreResult(want, got); diff != "" {
		t.Fatalf("decodeStoreResult(%s) differs from the expected result: %s", valid, diff)
	}
}

type brokenStore struct {
	store.Store
	get        func(inner store.Store, ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error)
	ranged     func(inner store.Store, ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error)
	txn        func(inner store.Store, ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error)
	currentRev func(inner store.Store, ctx context.Context) (store.Rev, error)
	compact    func(inner store.Store, ctx context.Context, rev store.Rev) error
	watch      func(inner store.Store, ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error)
}

func (b brokenStore) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	if b.get != nil {
		return b.get(b.Store, ctx, key, atRev)
	}
	return b.Store.Get(ctx, key, atRev)
}

func (b brokenStore) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	if b.ranged != nil {
		return b.ranged(b.Store, ctx, prefix, atRev, limit, cont)
	}
	return b.Store.Range(ctx, prefix, atRev, limit, cont)
}

func (b brokenStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	if b.txn != nil {
		return b.txn(b.Store, ctx, cmps, ops)
	}
	return b.Store.Txn(ctx, cmps, ops)
}

func (b brokenStore) CurrentRev(ctx context.Context) (store.Rev, error) {
	if b.currentRev != nil {
		return b.currentRev(b.Store, ctx)
	}
	return b.Store.CurrentRev(ctx)
}

func (b brokenStore) Compact(ctx context.Context, rev store.Rev) error {
	if b.compact != nil {
		return b.compact(b.Store, ctx, rev)
	}
	return b.Store.Compact(ctx, rev)
}

func (b brokenStore) Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
	if b.watch != nil {
		return b.watch(b.Store, ctx, prefix, fromRev)
	}
	return b.Store.Watch(ctx, prefix, fromRev)
}

type filteredWatch struct {
	store.WatchStream
	out chan store.Event
}

func (w filteredWatch) Events() <-chan store.Event { return w.out }

func filterWatch(ctx context.Context, inner store.WatchStream, rewrite func(batch []store.Event) []store.Event) store.WatchStream {
	w := filteredWatch{WatchStream: inner, out: make(chan store.Event)}
	go func() {
		defer close(w.out)
		var batch []store.Event
		for ev := range inner.Events() {
			batch = append(batch, ev)
			if ev.Type != store.EventProgress {
				continue
			}
			for _, out := range rewrite(batch) {
				select {
				case w.out <- out:
				case <-ctx.Done():
					return
				}
			}
			batch = nil
		}
	}()
	return w
}

func TestStoreDRTCatchesBrokenImplementations(t *testing.T) {
	cases := generateStoreCases(1, 1000)
	_, _, leanResults := leanStoreResults(t, cases)

	variants := []struct {
		name  string
		build func() store.Store
	}{
		{"txn ignores its comparisons", func() store.Store {
			return brokenStore{Store: memstore.New(), txn: func(inner store.Store, ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
				if err := store.ValidateTxn(cmps, ops); err != nil {
					return false, 0, err
				}
				return inner.Txn(ctx, nil, ops)
			}}
		}},
		{"txn reports a failed comparison as applied", func() store.Store {
			return brokenStore{Store: memstore.New(), txn: func(inner store.Store, ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
				_, rev, err := inner.Txn(ctx, cmps, ops)
				return err == nil, rev, err
			}}
		}},
		{"txn accepts repeated keys", func() store.Store {
			return brokenStore{Store: memstore.New(), txn: func(inner store.Store, ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
				seen := map[string]bool{}
				var distinct []store.Op
				for _, op := range ops {
					if !seen[op.Key] {
						distinct = append(distinct, op)
					}
					seen[op.Key] = true
				}
				return inner.Txn(ctx, cmps, distinct)
			}}
		}},
		{"get ignores the requested revision", func() store.Store {
			return brokenStore{Store: memstore.New(), get: func(inner store.Store, ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
				return inner.Get(ctx, key, 0)
			}}
		}},
		{"get serves future revisions from the latest state", func() store.Store {
			return brokenStore{Store: memstore.New(), get: func(inner store.Store, ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
				kv, rev, err := inner.Get(ctx, key, atRev)
				if errors.Is(err, store.ErrFutureRev) {
					return inner.Get(ctx, key, 0)
				}
				return kv, rev, err
			}}
		}},
		{"get reports the wrong revision on a missing key", func() store.Store {
			return brokenStore{Store: memstore.New(), get: func(inner store.Store, ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
				kv, rev, err := inner.Get(ctx, key, atRev)
				if errors.Is(err, store.ErrNotFound) {
					return kv, 0, err
				}
				return kv, rev, err
			}}
		}},
		{"current revision lags behind writes", func() store.Store {
			return brokenStore{Store: memstore.New(), currentRev: func(inner store.Store, ctx context.Context) (store.Rev, error) {
				rev, err := inner.CurrentRev(ctx)
				if rev > 1 {
					rev--
				}
				return rev, err
			}}
		}},
		{"range ignores the limit", func() store.Store {
			return brokenStore{Store: memstore.New(), ranged: func(inner store.Store, ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
				return inner.Range(ctx, prefix, atRev, 0, cont)
			}}
		}},
		{"range restarts instead of continuing", func() store.Store {
			return brokenStore{Store: memstore.New(), ranged: func(inner store.Store, ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
				if err := store.ValidateRange(prefix, atRev, limit, cont); err != nil {
					return nil, 0, "", err
				}
				return inner.Range(ctx, prefix, atRev, limit, "")
			}}
		}},
		{"range resumes at the continuation key instead of after it", func() store.Store {
			return brokenStore{Store: memstore.New(), ranged: func(inner store.Store, ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
				kvs, rev, next, err := inner.Range(ctx, prefix, atRev, limit, cont)
				if err != nil || cont == "" {
					return kvs, rev, next, err
				}
				if kv, _, getErr := inner.Get(ctx, cont, rev); getErr == nil {
					kvs = append([]store.KV{kv}, kvs...)
				}
				return kvs, rev, next, nil
			}}
		}},
		{"compact forgets nothing", func() store.Store {
			return brokenStore{Store: memstore.New(), compact: func(inner store.Store, ctx context.Context, rev store.Rev) error {
				return store.ValidateCompact(rev)
			}}
		}},
		{"watch ignores the start revision", func() store.Store {
			return brokenStore{Store: memstore.New(), watch: func(inner store.Store, ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
				return inner.Watch(ctx, prefix, 0)
			}}
		}},
		{"watch ignores the prefix", func() store.Store {
			return brokenStore{Store: memstore.New(), watch: func(inner store.Store, ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
				return inner.Watch(ctx, "", fromRev)
			}}
		}},
		{"watch drops delete events", func() store.Store {
			return brokenStore{Store: memstore.New(), watch: func(inner store.Store, ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
				ws, err := inner.Watch(ctx, prefix, fromRev)
				if err != nil {
					return nil, err
				}
				return filterWatch(ctx, ws, func(batch []store.Event) []store.Event {
					var kept []store.Event
					for _, ev := range batch {
						if ev.Type != store.EventDelete {
							kept = append(kept, ev)
						}
					}
					return kept
				}), nil
			}}
		}},
		{"watch delivers events twice", func() store.Store {
			return brokenStore{Store: memstore.New(), watch: func(inner store.Store, ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
				ws, err := inner.Watch(ctx, prefix, fromRev)
				if err != nil {
					return nil, err
				}
				return filterWatch(ctx, ws, func(batch []store.Event) []store.Event {
					var out []store.Event
					for _, ev := range batch {
						out = append(out, ev)
						if ev.Type != store.EventProgress {
							out = append(out, ev)
						}
					}
					return out
				}), nil
			}}
		}},
		{"watch reverses the changes of one revision", func() store.Store {
			return brokenStore{Store: memstore.New(), watch: func(inner store.Store, ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
				ws, err := inner.Watch(ctx, prefix, fromRev)
				if err != nil {
					return nil, err
				}
				return filterWatch(ctx, ws, func(batch []store.Event) []store.Event {
					for i := 0; i+1 < len(batch); i++ {
						if batch[i].Type != store.EventProgress && batch[i+1].Type != store.EventProgress && batch[i].Rev == batch[i+1].Rev {
							batch[i], batch[i+1] = batch[i+1], batch[i]
							i++
						}
					}
					return batch
				}), nil
			}}
		}},
		{"watch reports the wrong progress revision", func() store.Store {
			return brokenStore{Store: memstore.New(), watch: func(inner store.Store, ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
				ws, err := inner.Watch(ctx, prefix, fromRev)
				if err != nil {
					return nil, err
				}
				return filterWatch(ctx, ws, func(batch []store.Event) []store.Event {
					for i := range batch {
						if batch[i].Type == store.EventProgress {
							batch[i].Rev++
						}
					}
					return batch
				}), nil
			}}
		}},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			mismatches := 0
			for i, tc := range cases {
				goRes, err := runStoreCase(tc, v.build())
				if err != nil {
					t.Fatalf("id=%d: broken implementation failed instead of answering wrongly: %v", tc.ID, err)
				}
				if compareStoreResult(leanResults[i], goRes) != "" {
					mismatches++
				}
			}
			if mismatches == 0 {
				t.Fatalf("broken implementation %q had 0 mismatches against the Lean model", v.name)
			}
			t.Logf("%d of %d cases differ", mismatches, len(cases))
		})
	}
}

func TestGenerateStoreCasesIsDeterministic(t *testing.T) {
	a := bytes.Join(encodeStoreCases(t, generateStoreCases(1, 100)), []byte("\n"))
	b := bytes.Join(encodeStoreCases(t, generateStoreCases(1, 100)), []byte("\n"))
	c := bytes.Join(encodeStoreCases(t, generateStoreCases(2, 100)), []byte("\n"))

	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different encoded cases")
	}
	if bytes.Equal(a, c) {
		t.Fatal("different seeds produced identical encoded cases")
	}
}

func TestStoreModelRejectsMalformedCase(t *testing.T) {
	for _, bad := range []string{
		`{"id":1,"ops":[{"op":"explode"}]}`,
		`{"id":1,"ops":[{"op":"progress","watch":0}]}`,
		`{"id":1,"ops":[{"op":"get","key":"a"}]}`,
		`{"id":1,"ops":[{"op":"txn","cmps":[{"key":"a","cmp":"le","mod":0}],"ops":[]}]}`,
	} {
		cases := [][]byte{[]byte(`{"id":0,"ops":[{"op":"rev"}]}`), []byte(bad)}
		_, err := RunModel("store", cases)
		if err == nil {
			t.Errorf("RunModel accepted malformed case %s", bad)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte("line 2")) {
			t.Errorf("error %q does not name line 2 for %s", err.Error(), bad)
		}
	}
}
