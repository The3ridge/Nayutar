//go:build drt

package drt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache/window"
)

type watchCacheConfig struct {
	MinEvents int `json:"min_events"`
	MinAge    int `json:"min_age"`
	MaxEvents int `json:"max_events"`
}

type watchCacheOp struct {
	Op     string
	Rev    int64
	KVs    []storeKV
	Type   string
	Key    string
	Value  string
	Now    int64
	Prefix string
	Limit  int
	Cont   string
	Watch  int
}

func (o watchCacheOp) MarshalJSON() ([]byte, error) {
	switch o.Op {
	case "init":
		kvs := o.KVs
		if kvs == nil {
			kvs = []storeKV{}
		}
		return json.Marshal(struct {
			Op  string    `json:"op"`
			Rev int64     `json:"rev"`
			KVs []storeKV `json:"kvs"`
		}{o.Op, o.Rev, kvs})
	case "event":
		return json.Marshal(struct {
			Op    string `json:"op"`
			Type  string `json:"type"`
			Key   string `json:"key"`
			Value string `json:"value"`
			Rev   int64  `json:"rev"`
			Now   int64  `json:"now"`
		}{o.Op, o.Type, o.Key, o.Value, o.Rev, o.Now})
	case "progress":
		return json.Marshal(struct {
			Op  string `json:"op"`
			Rev int64  `json:"rev"`
		}{o.Op, o.Rev})
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
	case "watch", "watchlist":
		return json.Marshal(struct {
			Op     string `json:"op"`
			Prefix string `json:"prefix"`
			Rev    int64  `json:"rev"`
		}{o.Op, o.Prefix, o.Rev})
	case "poll":
		return json.Marshal(struct {
			Op    string `json:"op"`
			Watch int    `json:"watch"`
			Limit int    `json:"limit"`
		}{o.Op, o.Watch, o.Limit})
	case "bookmark":
		return json.Marshal(struct {
			Op    string `json:"op"`
			Watch int    `json:"watch"`
		}{o.Op, o.Watch})
	default:
		return nil, fmt.Errorf("unknown op %q", o.Op)
	}
}

type watchCacheCase struct {
	ID     int              `json:"id"`
	Config watchCacheConfig `json:"config"`
	Rev    int64            `json:"rev"`
	KVs    []storeKV        `json:"kvs"`
	Ops    []watchCacheOp   `json:"ops"`
}

type watchCacheStep struct {
	Err      string       `json:"err"`
	OK       bool         `json:"ok"`
	Rev      int          `json:"rev"`
	KVs      []storeKV    `json:"kvs"`
	Cont     string       `json:"cont"`
	Events   []storeEvent `json:"events"`
	Closed   int          `json:"closed"`
	Open     bool         `json:"open"`
	Floor    int          `json:"floor"`
	Retained int          `json:"retained"`
}

type watchCacheResult struct {
	ID    int              `json:"id"`
	Steps []watchCacheStep `json:"steps"`
}

var (
	watchCacheKeys     = []string{"a", "a/x", "a/y", "ab", "b", "b/x"}
	watchCachePrefixes = []string{"", "a", "a/", "b", "c"}
	watchCacheValues   = []string{"", "v1", "v2", "v3"}
)

func genWatchCacheInitKVs(rng *rand.Rand, rev int64) []storeKV {
	count := rng.IntN(5)
	kvs := make([]storeKV, count)
	perm := rng.Perm(len(watchCacheKeys))
	for i := range count {
		var mod int
		if rev > 0 {
			mod = 1 + int(rng.Int64N(rev))
		}
		kvs[i] = storeKV{
			Key:   watchCacheKeys[perm[i]],
			Value: pick(rng, watchCacheValues),
			Mod:   mod,
		}
	}
	return kvs
}

func genWatchCacheReadRev(rng *rand.Rand, closedRev, lastRev int64) int64 {
	r := rng.IntN(100)
	switch {
	case r < 30:
		return 0
	case r < 60:
		base := max(1, closedRev-2)
		return base + rng.Int64N(3)
	case r < 80:
		if closedRev <= 1 {
			return 1
		}
		return 1 + rng.Int64N(closedRev)
	default:
		return lastRev + 1 + rng.Int64N(4)
	}
}

func genWatchCacheRangeCont(rng *rand.Rand, prefix string) string {
	r := rng.IntN(100)
	if r < 50 {
		return ""
	}
	var matching, nonMatching []string
	for _, k := range watchCacheKeys {
		if strings.HasPrefix(k, prefix) {
			matching = append(matching, k)
		} else {
			nonMatching = append(nonMatching, k)
		}
	}
	if r < 85 && len(matching) > 0 {
		return pick(rng, matching)
	}
	if len(nonMatching) > 0 {
		return pick(rng, nonMatching)
	}
	return pick(rng, watchCacheKeys)
}

func generateWatchCacheCases(seed uint64, n int) []watchCacheCase {
	rng := rand.New(rand.NewPCG(seed, 0x7761746368636163))
	cases := make([]watchCacheCase, n)
	for i := range n {
		minEvents := rng.IntN(7)
		maxEvents := rng.IntN(13)
		if chance(rng, 15) {
			minEvents = 1 + rng.IntN(6)
			maxEvents = rng.IntN(minEvents)
		}
		cfg := watchCacheConfig{
			MinEvents: minEvents,
			MinAge:    rng.IntN(51),
			MaxEvents: maxEvents,
		}
		initRev := int64(rng.IntN(6))
		initKVs := genWatchCacheInitKVs(rng, initRev)

		closedRev := initRev
		lastRev := initRev
		isOpen := false
		pendingInRev := 0
		now := int64(100 + rng.IntN(100))
		watches := 0
		var currentGenWatches []int
		initCount := 0

		numOps := 20 + rng.IntN(41)
		ops := make([]watchCacheOp, 0, numOps)
		for stepIdx := range numOps {
			roll := rng.IntN(100)
			if watches == 0 && roll >= 80 && roll < 97 {
				roll = 0
			}
			if roll >= 97 && (initCount >= 2 || stepIdx < 6) {
				roll = 0
			}

			switch {
			case roll < 34:
				stepTime := rng.IntN(100)
				switch {
				case stepTime < 75:
					now += 1 + rng.Int64N(55)
				case stepTime < 90:
				default:
					now = max(0, now-(1+rng.Int64N(30)))
				}

				var evRev int64
				if pendingInRev > 0 && !chance(rng, 10) {
					evRev = lastRev
					pendingInRev--
				} else if chance(rng, 10) {
					evRev = rng.Int64N(closedRev + 1)
				} else {
					evRev = lastRev + 1
					if chance(rng, 30) {
						evRev += 1 + rng.Int64N(3)
					}
					runLen := 1 + rng.IntN(4)
					closedRev = evRev - 1
					lastRev = evRev
					isOpen = true
					pendingInRev = runLen - 1
				}

				evType := "put"
				val := pick(rng, watchCacheValues)
				if chance(rng, 35) {
					evType = "delete"
					val = ""
				}
				ops = append(ops, watchCacheOp{
					Op:    "event",
					Type:  evType,
					Key:   pick(rng, watchCacheKeys),
					Value: val,
					Rev:   evRev,
					Now:   now,
				})

			case roll < 48:
				var p int64
				if chance(rng, 10) && (isOpen || closedRev > 0) {
					if isOpen {
						p = rng.Int64N(lastRev)
					} else {
						p = rng.Int64N(closedRev)
					}
				} else {
					minValid := closedRev
					if isOpen {
						minValid = lastRev
					}
					p = minValid + rng.Int64N(4)
					closedRev = p
					lastRev = p
					isOpen = false
					pendingInRev = 0
				}
				ops = append(ops, watchCacheOp{Op: "progress", Rev: p})

			case roll < 58:
				key := pick(rng, watchCacheKeys)
				if chance(rng, 5) {
					key = ""
				}
				ops = append(ops, watchCacheOp{
					Op:  "get",
					Key: key,
					Rev: genWatchCacheReadRev(rng, closedRev, lastRev),
				})

			case roll < 70:
				prefix := pick(rng, watchCachePrefixes)
				ops = append(ops, watchCacheOp{
					Op:     "range",
					Prefix: prefix,
					Rev:    genWatchCacheReadRev(rng, closedRev, lastRev),
					Limit:  pick(rng, []int{0, 1, 1, 2, 2, 3}),
					Cont:   genWatchCacheRangeCont(rng, prefix),
				})

			case roll < 76:
				var rev int64
				r := rng.IntN(100)
				switch {
				case r < 25:
					rev = 0
				case r < 45:
					rev = closedRev + 1
				case r < 65:
					rev = max(1, closedRev-rng.Int64N(3))
				case r < 80:
					if closedRev <= 1 {
						rev = 1
					} else {
						rev = 1 + rng.Int64N(closedRev)
					}
				default:
					rev = lastRev + 2 + rng.Int64N(5)
				}
				ops = append(ops, watchCacheOp{
					Op:     "watch",
					Prefix: pick(rng, watchCachePrefixes),
					Rev:    rev,
				})
				currentGenWatches = append(currentGenWatches, watches)
				watches++

			case roll < 80:
				var rev int64
				r := rng.IntN(100)
				switch {
				case r < 35:
					rev = 0
				case r < 70:
					rev = max(0, closedRev-rng.Int64N(3))
				default:
					rev = lastRev + 1 + rng.Int64N(5)
				}
				ops = append(ops, watchCacheOp{
					Op:     "watchlist",
					Prefix: pick(rng, watchCachePrefixes),
					Rev:    rev,
				})
				currentGenWatches = append(currentGenWatches, watches)
				watches++

			case roll < 91:
				idx := rng.IntN(watches)
				if len(currentGenWatches) > 0 && chance(rng, 75) {
					idx = pick(rng, currentGenWatches)
				}
				ops = append(ops, watchCacheOp{
					Op:    "poll",
					Watch: idx,
					Limit: rng.IntN(4),
				})

			case roll < 97:
				idx := rng.IntN(watches)
				if len(currentGenWatches) > 0 && chance(rng, 80) {
					idx = pick(rng, currentGenWatches)
				}
				ops = append(ops, watchCacheOp{
					Op:    "bookmark",
					Watch: idx,
				})

			default:
				newRev := rng.Int64N(closedRev + 6)
				ops = append(ops, watchCacheOp{
					Op:  "init",
					Rev: newRev,
					KVs: genWatchCacheInitKVs(rng, newRev),
				})
				closedRev = newRev
				lastRev = newRev
				isOpen = false
				pendingInRev = 0
				currentGenWatches = nil
				initCount++
			}
		}

		cases[i] = watchCacheCase{
			ID:     i,
			Config: cfg,
			Rev:    initRev,
			KVs:    initKVs,
			Ops:    ops,
		}
	}
	return cases
}

func encodeWatchCacheCases(t *testing.T, cases []watchCacheCase) [][]byte {
	t.Helper()
	encoded := make([][]byte, len(cases))
	for i, tc := range cases {
		if tc.KVs == nil {
			tc.KVs = []storeKV{}
		}
		if tc.Ops == nil {
			tc.Ops = []watchCacheOp{}
		}
		b, err := json.Marshal(tc)
		if err != nil {
			t.Fatal(err)
		}
		encoded[i] = b
	}
	return encoded
}

func watchCacheErrName(err error) (string, error) {
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, store.ErrNotFound):
		return "not_found", nil
	case errors.Is(err, window.ErrTooOld):
		return "too_old", nil
	case errors.Is(err, window.ErrNotYet):
		return "not_yet", nil
	case errors.Is(err, store.ErrInvalidArgument):
		return "invalid", nil
	default:
		return "", err
	}
}

func toWatchCacheWindowKVs(kvs []storeKV) []store.KV {
	out := make([]store.KV, len(kvs))
	for i, kv := range kvs {
		out[i] = store.KV{
			Key:    kv.Key,
			Value:  []byte(kv.Value),
			ModRev: store.Rev(kv.Mod),
		}
	}
	return out
}

func toWatchCacheEvents(events []store.Event) ([]storeEvent, error) {
	out := make([]storeEvent, len(events))
	for i, ev := range events {
		typ := "put"
		if ev.Type == store.EventDelete {
			typ = "delete"
		} else if ev.Type != store.EventPut {
			return nil, fmt.Errorf("unexpected event type %d", ev.Type)
		}
		out[i] = storeEvent{
			Type:  typ,
			Key:   ev.Key,
			Value: string(ev.Value),
			Rev:   int(ev.Rev),
		}
	}
	return out, nil
}

type watchCacheSlot struct {
	cursor     *window.Cursor
	generation int
}

type watchCacheHooks struct {
	allowOpenBookmark     bool
	readZeroSeesOpen      bool
	ignoreEvictionInState bool
	ignorePollLimit       bool
	watchZeroSkipsOpen    bool
	watchIgnoresPrefix    bool
	initSkipsGeneration   bool
	rangeIgnoresCont      bool
}

func runWatchCacheCase(tc watchCacheCase, hooks watchCacheHooks) (watchCacheResult, error) {
	cfg := window.Config{
		MinEvents: tc.Config.MinEvents,
		MinAge:    time.Duration(tc.Config.MinAge) * time.Millisecond,
		MaxEvents: tc.Config.MaxEvents,
	}
	w := window.New(cfg, store.Rev(tc.Rev), toWatchCacheWindowKVs(tc.KVs))
	generation := 0
	var slots []watchCacheSlot
	initFloor := int(tc.Rev)
	fedEvents := 0

	res := watchCacheResult{
		ID:    tc.ID,
		Steps: make([]watchCacheStep, 0, len(tc.Ops)),
	}

	for i, o := range tc.Ops {
		step := watchCacheStep{KVs: []storeKV{}, Events: []storeEvent{}}
		var err error

		switch o.Op {
		case "init":
			w = window.New(cfg, store.Rev(o.Rev), toWatchCacheWindowKVs(o.KVs))
			if !hooks.initSkipsGeneration {
				generation++
			}
			initFloor = int(o.Rev)
			fedEvents = 0
			step.OK = true

		case "event":
			ev := store.Event{
				Type:  store.EventPut,
				Rev:   store.Rev(o.Rev),
				Key:   o.Key,
				Value: []byte(o.Value),
			}
			if o.Type == "delete" {
				ev.Type = store.EventDelete
				ev.Value = nil
			} else if o.Type != "put" {
				err = fmt.Errorf("unknown event type %q", o.Type)
				break
			}
			feedErr := w.Event(ev, time.UnixMilli(o.Now))
			if feedErr == nil {
				step.OK = true
				fedEvents++
			} else if !errors.Is(feedErr, window.ErrOutOfOrder) {
				err = feedErr
			}

		case "progress":
			feedErr := w.Progress(store.Rev(o.Rev))
			if feedErr == nil {
				step.OK = true
			} else if !errors.Is(feedErr, window.ErrOutOfOrder) {
				err = feedErr
			}

		case "get":
			kv, rev, getErr := w.Get(o.Key, store.Rev(o.Rev))
			if getErr == nil {
				step.KVs = []storeKV{{Key: kv.Key, Value: string(kv.Value), Mod: int(kv.ModRev)}}
				step.Rev = int(rev)
			} else if errors.Is(getErr, store.ErrNotFound) {
				step.Rev = int(rev)
			}
			if hooks.readZeroSeesOpen && o.Rev == 0 && w.Open() && (getErr == nil || errors.Is(getErr, store.ErrNotFound)) {
				step.Rev = int(w.Rev()) + 1
			}
			step.Err, err = watchCacheErrName(getErr)

		case "range":
			cont := o.Cont
			if hooks.rangeIgnoresCont && cont != "" && strings.HasPrefix(cont, o.Prefix) {
				cont = ""
			}
			kvs, rev, next, rangeErr := w.Range(o.Prefix, store.Rev(o.Rev), o.Limit, cont)
			if rangeErr == nil {
				step.KVs = toStoreKVs(kvs)
				step.Rev = int(rev)
				step.Cont = next
				if hooks.readZeroSeesOpen && o.Rev == 0 && w.Open() {
					step.Rev = int(w.Rev()) + 1
				}
			}
			step.Err, err = watchCacheErrName(rangeErr)

		case "watch":
			prefix := o.Prefix
			if hooks.watchIgnoresPrefix {
				prefix = ""
			}
			wasOpen := w.Open()
			c, watchErr := w.Watch(prefix, store.Rev(o.Rev))
			if watchErr != nil {
				slots = append(slots, watchCacheSlot{})
				step.Err, err = watchCacheErrName(watchErr)
			} else {
				if hooks.watchZeroSkipsOpen && o.Rev == 0 && wasOpen {
					_, _ = w.Poll(c, 0)
				}
				slots = append(slots, watchCacheSlot{cursor: c, generation: generation})
				step.OK = true
			}

		case "watchlist":
			tree, rev, c, wlErr := w.WatchList(o.Prefix, store.Rev(o.Rev))
			if wlErr != nil {
				slots = append(slots, watchCacheSlot{})
				step.Err, err = watchCacheErrName(wlErr)
			} else {
				kvs := []storeKV{}
				tree.Ascend(o.Prefix, "", func(kv store.KV) bool {
					kvs = append(kvs, storeKV{Key: kv.Key, Value: string(kv.Value), Mod: int(kv.ModRev)})
					return true
				})
				slots = append(slots, watchCacheSlot{cursor: c, generation: generation})
				step.OK = true
				step.Rev = int(rev)
				step.KVs = kvs
			}

		case "poll":
			if o.Watch < 0 || o.Watch >= len(slots) || slots[o.Watch].cursor == nil {
				step.Err = "invalid"
				break
			}
			slot := slots[o.Watch]
			if slot.generation != generation {
				step.Err = "too_old"
				break
			}
			limit := o.Limit
			if hooks.ignorePollLimit {
				limit = 0
			}
			events, pollErr := w.Poll(slot.cursor, limit)
			if pollErr == nil {
				step.OK = true
				step.Events, err = toWatchCacheEvents(events)
			} else {
				step.Err, err = watchCacheErrName(pollErr)
			}

		case "bookmark":
			if o.Watch < 0 || o.Watch >= len(slots) || slots[o.Watch].cursor == nil || slots[o.Watch].generation != generation {
				break
			}
			slot := slots[o.Watch]
			rev, ok := w.Bookmark(slot.cursor)
			if ok {
				step.OK = true
				step.Rev = int(rev)
			} else if hooks.allowOpenBookmark && w.Open() && w.Drained(slot.cursor) {
				step.OK = true
				step.Rev = int(w.Rev())
			}

		default:
			err = fmt.Errorf("unknown op %q", o.Op)
		}

		if err != nil {
			return res, fmt.Errorf("step[%d] %s: %w", i, o.Op, err)
		}

		step.Closed = int(w.Rev())
		step.Open = w.Open()
		if hooks.ignoreEvictionInState {
			step.Floor = initFloor
			step.Retained = fedEvents
		} else {
			step.Floor = int(w.Floor())
			step.Retained = w.Retained()
		}
		res.Steps = append(res.Steps, step)
	}

	return res, nil
}

func decodeWatchCacheResult(line []byte) (watchCacheResult, error) {
	trimmed := bytes.TrimSpace(line)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rawTop json.RawMessage
	if err := dec.Decode(&rawTop); err != nil {
		return watchCacheResult{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return watchCacheResult{}, fmt.Errorf("unexpected trailing data in result line: %v", err)
	}
	top, err := decodeObject(rawTop, "id", "steps")
	if err != nil {
		return watchCacheResult{}, err
	}
	id, err := decodeNonNegInt(top["id"])
	if err != nil {
		return watchCacheResult{}, fmt.Errorf("invalid id: %w", err)
	}
	var rawSteps []json.RawMessage
	if err := decodeStrictJSON(top["steps"], &rawSteps); err != nil {
		return watchCacheResult{}, fmt.Errorf("invalid steps: %w", err)
	}
	res := watchCacheResult{ID: id, Steps: make([]watchCacheStep, len(rawSteps))}
	for i, rs := range rawSteps {
		step, err := decodeWatchCacheStep(rs)
		if err != nil {
			return watchCacheResult{}, fmt.Errorf("step[%d]: %w", i, err)
		}
		res.Steps[i] = step
	}
	return res, nil
}

func decodeWatchCacheStep(raw json.RawMessage) (watchCacheStep, error) {
	obj, err := decodeObject(raw, "err", "ok", "rev", "kvs", "cont", "events", "closed", "open", "floor", "retained")
	if err != nil {
		return watchCacheStep{}, err
	}
	step := watchCacheStep{KVs: []storeKV{}, Events: []storeEvent{}}
	if step.Err, err = decodeString(obj["err"]); err != nil {
		return watchCacheStep{}, fmt.Errorf("err: %w", err)
	}
	switch step.Err {
	case "", "not_found", "too_old", "not_yet", "invalid":
	default:
		return watchCacheStep{}, fmt.Errorf("unknown err %q", step.Err)
	}
	if err := decodeStrictJSON(obj["ok"], &step.OK); err != nil {
		return watchCacheStep{}, fmt.Errorf("ok: %w", err)
	}
	if step.Rev, err = decodeNonNegInt(obj["rev"]); err != nil {
		return watchCacheStep{}, fmt.Errorf("rev: %w", err)
	}
	if step.Cont, err = decodeString(obj["cont"]); err != nil {
		return watchCacheStep{}, fmt.Errorf("cont: %w", err)
	}
	if step.Closed, err = decodeNonNegInt(obj["closed"]); err != nil {
		return watchCacheStep{}, fmt.Errorf("closed: %w", err)
	}
	if err := decodeStrictJSON(obj["open"], &step.Open); err != nil {
		return watchCacheStep{}, fmt.Errorf("open: %w", err)
	}
	if step.Floor, err = decodeNonNegInt(obj["floor"]); err != nil {
		return watchCacheStep{}, fmt.Errorf("floor: %w", err)
	}
	if step.Retained, err = decodeNonNegInt(obj["retained"]); err != nil {
		return watchCacheStep{}, fmt.Errorf("retained: %w", err)
	}
	var rawKVs, rawEvents []json.RawMessage
	if err := decodeStrictJSON(obj["kvs"], &rawKVs); err != nil {
		return watchCacheStep{}, fmt.Errorf("kvs: %w", err)
	}
	for i, rk := range rawKVs {
		kvObj, err := decodeObject(rk, "key", "value", "mod")
		if err != nil {
			return watchCacheStep{}, fmt.Errorf("kvs[%d]: %w", i, err)
		}
		var kv storeKV
		if kv.Key, err = decodeString(kvObj["key"]); err != nil {
			return watchCacheStep{}, fmt.Errorf("kvs[%d].key: %w", i, err)
		}
		if kv.Value, err = decodeString(kvObj["value"]); err != nil {
			return watchCacheStep{}, fmt.Errorf("kvs[%d].value: %w", i, err)
		}
		if kv.Mod, err = decodeNonNegInt(kvObj["mod"]); err != nil {
			return watchCacheStep{}, fmt.Errorf("kvs[%d].mod: %w", i, err)
		}
		step.KVs = append(step.KVs, kv)
	}
	if err := decodeStrictJSON(obj["events"], &rawEvents); err != nil {
		return watchCacheStep{}, fmt.Errorf("events: %w", err)
	}
	for i, re := range rawEvents {
		evObj, err := decodeObject(re, "type", "key", "value", "rev")
		if err != nil {
			return watchCacheStep{}, fmt.Errorf("events[%d]: %w", i, err)
		}
		var ev storeEvent
		if ev.Type, err = decodeString(evObj["type"]); err != nil {
			return watchCacheStep{}, fmt.Errorf("events[%d].type: %w", i, err)
		}
		if ev.Type != "put" && ev.Type != "delete" {
			return watchCacheStep{}, fmt.Errorf("events[%d]: unknown type %q", i, ev.Type)
		}
		if ev.Key, err = decodeString(evObj["key"]); err != nil {
			return watchCacheStep{}, fmt.Errorf("events[%d].key: %w", i, err)
		}
		if ev.Value, err = decodeString(evObj["value"]); err != nil {
			return watchCacheStep{}, fmt.Errorf("events[%d].value: %w", i, err)
		}
		if ev.Rev, err = decodeNonNegInt(evObj["rev"]); err != nil {
			return watchCacheStep{}, fmt.Errorf("events[%d].rev: %w", i, err)
		}
		step.Events = append(step.Events, ev)
	}
	return step, nil
}

func compareWatchCacheResult(want, got watchCacheResult) string {
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
		case w.Closed != g.Closed:
			return fmt.Sprintf("%s.closed mismatch: lean=%d go=%d", at, w.Closed, g.Closed)
		case w.Open != g.Open:
			return fmt.Sprintf("%s.open mismatch: lean=%v go=%v", at, w.Open, g.Open)
		case w.Floor != g.Floor:
			return fmt.Sprintf("%s.floor mismatch: lean=%d go=%d", at, w.Floor, g.Floor)
		case w.Retained != g.Retained:
			return fmt.Sprintf("%s.retained mismatch: lean=%d go=%d", at, w.Retained, g.Retained)
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

func cloneWatchCacheResult(r watchCacheResult) watchCacheResult {
	out := watchCacheResult{ID: r.ID, Steps: make([]watchCacheStep, len(r.Steps))}
	for i, s := range r.Steps {
		s.KVs = append([]storeKV{}, s.KVs...)
		s.Events = append([]storeEvent{}, s.Events...)
		out.Steps[i] = s
	}
	return out
}

func leanWatchCacheResults(t *testing.T, cases []watchCacheCase) ([][]byte, [][]byte, []watchCacheResult) {
	t.Helper()
	encoded := encodeWatchCacheCases(t, cases)
	lines, err := RunModel("watchcache", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(lines), len(cases))
	}
	results := make([]watchCacheResult, len(lines))
	for i, line := range lines {
		res, err := decodeWatchCacheResult(line)
		if err != nil {
			t.Fatalf("id=%d decode error: %v\ncase: %s\nlean: %s", cases[i].ID, err, encoded[i], line)
		}
		results[i] = res
	}
	return encoded, lines, results
}

func TestWatchCacheMatchesLeanModel(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateWatchCacheCases(cfg.Seed, cfg.Cases)
	encoded, leanLines, leanResults := leanWatchCacheResults(t, cases)

	repro := fmt.Sprintf("NAYUTAR_DRT_SEED=%d NAYUTAR_DRT_CASES=%d go test -tags drt -run '^TestWatchCacheMatchesLeanModel$' ./core/drt/...", cfg.Seed, cfg.Cases)
	totalSteps := 0
	for i, tc := range cases {
		goRes, err := runWatchCacheCase(tc, watchCacheHooks{})
		if err != nil {
			t.Fatalf("seed=%d id=%d window error: %v\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, err, encoded[i], leanLines[i], repro)
		}
		if diff := compareWatchCacheResult(leanResults[i], goRes); diff != "" {
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

func TestWatchCacheCasesReachEveryOutcome(t *testing.T) {
	cases := generateWatchCacheCases(1, 1000)
	_, _, results := leanWatchCacheResults(t, cases)

	seen := map[string]int{}
	for i, tc := range cases {
		gen := 0
		var watchGen []int
		for j, o := range tc.Ops {
			s := results[i].Steps[j]
			seen[o.Op+"/"+s.Err]++
			if s.Err == "not_yet" {
				seen["any/not_yet"]++
			}
			switch o.Op {
			case "init":
				gen++
			case "event":
				if s.OK {
					seen["event/ok"]++
				} else {
					seen["event/rejected"]++
				}
				if j > 0 && s.Floor > results[i].Steps[j-1].Floor {
					seen["eviction/floor-moved"]++
				}
			case "progress":
				if s.OK {
					seen["progress/ok"]++
				} else {
					seen["progress/rejected"]++
				}
			case "get", "range":
				if s.Err == "too_old" {
					seen["read/too_old"]++
				}
				if o.Op == "range" && s.Err == "" && s.Cont != "" {
					seen["range/continued"]++
				}
				if o.Op == "range" && o.Cont != "" && s.Err == "" && len(s.KVs) > 0 {
					seen["range/resumed"]++
				}
			case "watch":
				watchGen = append(watchGen, gen)
			case "watchlist":
				watchGen = append(watchGen, gen)
				if s.OK && len(s.KVs) > 0 {
					seen["watchlist/non-empty"]++
				}
			case "poll":
				if s.Err == "too_old" {
					if watchGen[o.Watch] == gen {
						seen["poll/too_old-overtaken"]++
					} else {
						seen["poll/too_old-generation"]++
					}
				}
				if s.OK {
					for k := 1; k < len(s.Events); k++ {
						if s.Events[k].Rev == s.Events[k-1].Rev {
							seen["poll/multi-change-rev"]++
						}
					}
					for _, ev := range s.Events {
						seen["poll/"+ev.Type]++
					}
				}
			case "bookmark":
				if s.OK {
					seen["bookmark/ok"]++
				} else {
					seen["bookmark/rejected"]++
				}
			}
		}
	}

	for _, outcome := range []string{
		"init/",
		"event/ok", "event/rejected", "eviction/floor-moved",
		"progress/ok", "progress/rejected",
		"get/", "get/not_found", "get/invalid",
		"range/", "range/invalid", "range/continued", "range/resumed",
		"read/too_old", "any/not_yet",
		"watch/", "watch/too_old", "watch/not_yet",
		"watchlist/", "watchlist/not_yet", "watchlist/non-empty",
		"poll/", "poll/invalid", "poll/too_old-overtaken", "poll/too_old-generation",
		"poll/multi-change-rev", "poll/put", "poll/delete",
		"bookmark/ok", "bookmark/rejected",
	} {
		if seen[outcome] == 0 {
			t.Errorf("no case reaches outcome %q", outcome)
		}
	}
}

func TestWatchCacheComparisonDetectsEveryFieldMutation(t *testing.T) {
	cases := generateWatchCacheCases(1, 200)
	_, leanLines, results := leanWatchCacheResults(t, cases)

	applied := map[string]int{}
	assertDetects := func(kind string, orig watchCacheResult, mutate func(r *watchCacheResult)) {
		t.Helper()
		mut := cloneWatchCacheResult(orig)
		mutate(&mut)
		applied[kind]++
		if compareWatchCacheResult(orig, mut) == "" {
			t.Fatalf("mutation %q on case %d was not detected", kind, orig.ID)
		}
	}

	for _, orig := range results {
		assertDetects("id+1", orig, func(r *watchCacheResult) { r.ID++ })
		if n := len(orig.Steps); n > 0 {
			assertDetects("drop-last-step", orig, func(r *watchCacheResult) { r.Steps = r.Steps[:n-1] })
			assertDetects("append-duplicate-last-step", orig, func(r *watchCacheResult) { r.Steps = append(r.Steps, r.Steps[n-1]) })
		}
		for j, s := range orig.Steps {
			if s.Err == "" {
				assertDetects("err-set", orig, func(r *watchCacheResult) { r.Steps[j].Err = "invalid" })
			} else {
				assertDetects("err-cleared", orig, func(r *watchCacheResult) { r.Steps[j].Err = "" })
				assertDetects("err-changed", orig, func(r *watchCacheResult) { r.Steps[j].Err += "x" })
			}
			assertDetects("flip-ok", orig, func(r *watchCacheResult) { r.Steps[j].OK = !r.Steps[j].OK })
			assertDetects("rev+1", orig, func(r *watchCacheResult) { r.Steps[j].Rev++ })
			assertDetects("cont-changed", orig, func(r *watchCacheResult) { r.Steps[j].Cont += "x" })
			assertDetects("closed+1", orig, func(r *watchCacheResult) { r.Steps[j].Closed++ })
			assertDetects("flip-open", orig, func(r *watchCacheResult) { r.Steps[j].Open = !r.Steps[j].Open })
			assertDetects("floor+1", orig, func(r *watchCacheResult) { r.Steps[j].Floor++ })
			assertDetects("retained+1", orig, func(r *watchCacheResult) { r.Steps[j].Retained++ })
			assertDetects("kvs-append", orig, func(r *watchCacheResult) { r.Steps[j].KVs = append(r.Steps[j].KVs, storeKV{Key: "z"}) })
			assertDetects("events-append", orig, func(r *watchCacheResult) {
				r.Steps[j].Events = append(r.Steps[j].Events, storeEvent{Type: "put", Key: "z"})
			})
			if n := len(s.KVs); n > 0 {
				assertDetects("kvs-drop-last", orig, func(r *watchCacheResult) { r.Steps[j].KVs = r.Steps[j].KVs[:n-1] })
				assertDetects("kv-key", orig, func(r *watchCacheResult) { r.Steps[j].KVs[n-1].Key += "x" })
				assertDetects("kv-value", orig, func(r *watchCacheResult) { r.Steps[j].KVs[n-1].Value += "x" })
				assertDetects("kv-mod+1", orig, func(r *watchCacheResult) { r.Steps[j].KVs[n-1].Mod++ })
			}
			if n := len(s.KVs); n > 1 {
				assertDetects("kvs-swap", orig, func(r *watchCacheResult) {
					kvs := r.Steps[j].KVs
					kvs[0], kvs[n-1] = kvs[n-1], kvs[0]
				})
			}
			if n := len(s.Events); n > 0 {
				assertDetects("events-drop-last", orig, func(r *watchCacheResult) { r.Steps[j].Events = r.Steps[j].Events[:n-1] })
				assertDetects("event-type", orig, func(r *watchCacheResult) {
					if r.Steps[j].Events[0].Type == "put" {
						r.Steps[j].Events[0].Type = "delete"
					} else {
						r.Steps[j].Events[0].Type = "put"
					}
				})
				assertDetects("event-key", orig, func(r *watchCacheResult) { r.Steps[j].Events[n-1].Key += "x" })
				assertDetects("event-value", orig, func(r *watchCacheResult) { r.Steps[j].Events[n-1].Value += "x" })
				assertDetects("event-rev+1", orig, func(r *watchCacheResult) { r.Steps[j].Events[n-1].Rev++ })
			}
		}
	}

	for _, kind := range []string{
		"id+1", "drop-last-step", "append-duplicate-last-step",
		"err-set", "err-cleared", "err-changed", "flip-ok", "rev+1", "cont-changed",
		"closed+1", "flip-open", "floor+1", "retained+1",
		"kvs-append", "kvs-drop-last", "kv-key", "kv-value", "kv-mod+1", "kvs-swap",
		"events-append", "events-drop-last", "event-type", "event-key", "event-value", "event-rev+1",
	} {
		if applied[kind] == 0 {
			t.Errorf("mutation %q was never applicable across %d cases", kind, len(cases))
		}
	}

	const emptyStep = `{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[],"closed":0,"open":false,"floor":0,"retained":0}`
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
		{"unknown step field", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[],"closed":0,"open":false,"floor":0,"retained":0,"extra":1}]}`},
		{"missing retained", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[],"closed":0,"open":false,"floor":0}]}`},
		{"uppercase step keys", `{"id":0,"steps":[{"Err":"","OK":false,"Rev":0,"KVs":[],"Cont":"","Events":[],"Closed":0,"Open":false,"Floor":0,"Retained":0}]}`},
		{"unknown err name", `{"id":0,"steps":[{"err":"boom","ok":false,"rev":0,"kvs":[],"cont":"","events":[],"closed":0,"open":false,"floor":0,"retained":0}]}`},
		{"null err", `{"id":0,"steps":[{"err":null,"ok":false,"rev":0,"kvs":[],"cont":"","events":[],"closed":0,"open":false,"floor":0,"retained":0}]}`},
		{"null ok", `{"id":0,"steps":[{"err":"","ok":null,"rev":0,"kvs":[],"cont":"","events":[],"closed":0,"open":false,"floor":0,"retained":0}]}`},
		{"null open", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[],"closed":0,"open":null,"floor":0,"retained":0}]}`},
		{"negative closed", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[],"closed":-1,"open":false,"floor":0,"retained":0}]}`},
		{"negative floor", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[],"closed":0,"open":false,"floor":-1,"retained":0}]}`},
		{"float retained", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[],"closed":0,"open":false,"floor":0,"retained":1.5}]}`},
		{"null kvs", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":null,"cont":"","events":[],"closed":0,"open":false,"floor":0,"retained":0}]}`},
		{"null events", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":null,"closed":0,"open":false,"floor":0,"retained":0}]}`},
		{"kv without mod", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[{"key":"a","value":""}],"cont":"","events":[],"closed":0,"open":false,"floor":0,"retained":0}]}`},
		{"kv with extra field", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[{"key":"a","value":"","mod":1,"x":1}],"cont":"","events":[],"closed":0,"open":false,"floor":0,"retained":0}]}`},
		{"event without rev", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[{"type":"put","key":"a","value":""}],"closed":0,"open":false,"floor":0,"retained":0}]}`},
		{"event with unknown type", `{"id":0,"steps":[{"err":"","ok":false,"rev":0,"kvs":[],"cont":"","events":[{"type":"progress","key":"","value":"","rev":1}],"closed":0,"open":false,"floor":0,"retained":0}]}`},
	}
	for _, tc := range invalidLines {
		if _, err := decodeWatchCacheResult([]byte(tc.line)); err == nil {
			t.Errorf("decodeWatchCacheResult accepted %s: %s", tc.name, tc.line)
		}
	}

	valid := `{"id":3,"steps":[` + emptyStep + `,{"err":"not_found","ok":true,"rev":2,"kvs":[{"key":"a","value":"x","mod":2}],"cont":"a","events":[{"type":"delete","key":"b","value":"","rev":3}],"closed":4,"open":true,"floor":1,"retained":2}]}`
	got, err := decodeWatchCacheResult([]byte(valid))
	if err != nil {
		t.Fatalf("decodeWatchCacheResult(%s): %v", valid, err)
	}
	want := watchCacheResult{ID: 3, Steps: []watchCacheStep{
		{KVs: []storeKV{}, Events: []storeEvent{}},
		{
			Err: "not_found", OK: true, Rev: 2, Cont: "a",
			Closed: 4, Open: true, Floor: 1, Retained: 2,
			KVs:    []storeKV{{Key: "a", Value: "x", Mod: 2}},
			Events: []storeEvent{{Type: "delete", Key: "b", Rev: 3}},
		},
	}}
	if diff := compareWatchCacheResult(want, got); diff != "" {
		t.Fatalf("decodeWatchCacheResult(%s) differs from the expected result: %s", valid, diff)
	}
}

func TestWatchCacheDRTCatchesBrokenImplementations(t *testing.T) {
	cases := generateWatchCacheCases(1, 1000)
	_, _, leanResults := leanWatchCacheResults(t, cases)

	variants := []struct {
		name  string
		hooks watchCacheHooks
	}{
		{"bookmark allowed while a revision is open", watchCacheHooks{allowOpenBookmark: true}},
		{"reads at revision 0 see the open revision", watchCacheHooks{readZeroSeesOpen: true}},
		{"eviction ignored in floor and retained reporting", watchCacheHooks{ignoreEvictionInState: true}},
		{"poll ignores the limit", watchCacheHooks{ignorePollLimit: true}},
		{"watch at revision 0 starts after open revision changes", watchCacheHooks{watchZeroSkipsOpen: true}},
		{"watch ignores the prefix", watchCacheHooks{watchIgnoresPrefix: true}},
		{"init does not advance generation", watchCacheHooks{initSkipsGeneration: true}},
		{"range ignores continuation key", watchCacheHooks{rangeIgnoresCont: true}},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			mismatches := 0
			for i, tc := range cases {
				goRes, err := runWatchCacheCase(tc, v.hooks)
				if err != nil {
					t.Fatalf("id=%d: broken implementation failed instead of answering wrongly: %v", tc.ID, err)
				}
				if compareWatchCacheResult(leanResults[i], goRes) != "" {
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

func TestGenerateWatchCacheCasesIsDeterministic(t *testing.T) {
	a := bytes.Join(encodeWatchCacheCases(t, generateWatchCacheCases(1, 100)), []byte("\n"))
	b := bytes.Join(encodeWatchCacheCases(t, generateWatchCacheCases(1, 100)), []byte("\n"))
	c := bytes.Join(encodeWatchCacheCases(t, generateWatchCacheCases(2, 100)), []byte("\n"))

	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different encoded cases")
	}
	if bytes.Equal(a, c) {
		t.Fatal("different seeds produced identical encoded cases")
	}
}

func TestWatchCacheModelRejectsMalformedCase(t *testing.T) {
	valid := `{"id":0,"config":{"min_events":0,"min_age":0,"max_events":0},"rev":0,"kvs":[],"ops":[{"op":"progress","rev":0}]}`
	for _, bad := range []string{
		`{"id":1,"config":{"min_events":0,"min_age":0,"max_events":0},"rev":0,"kvs":[],"ops":[{"op":"explode"}]}`,
		`{"id":1,"config":{"min_events":0,"min_age":0,"max_events":0},"rev":0,"kvs":[],"ops":[{"op":"poll","watch":0,"limit":0}]}`,
		`{"id":1,"config":{"min_events":0,"min_age":0,"max_events":0},"rev":0,"kvs":[],"ops":[{"op":"get","key":"a"}]}`,
		`{"id":1,"config":{"min_events":0,"min_age":0,"max_events":0},"rev":0,"kvs":[],"ops":[{"op":"event","type":"progress","key":"a","value":"","rev":1,"now":1}]}`,
		`{"id":1,"config":{"min_events":0,"min_age":0,"max_events":0},"rev":0,"kvs":[],"ops":[{"op":"progress","rev":-1}]}`,
	} {
		cases := [][]byte{[]byte(valid), []byte(bad)}
		_, err := RunModel("watchcache", cases)
		if err == nil {
			t.Errorf("RunModel accepted malformed case %s", bad)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte("line 2")) {
			t.Errorf("error %q does not name line 2 for %s", err.Error(), bad)
		}
	}
}
