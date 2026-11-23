//go:build drt

package drt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/orderedset"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
)

type patchOpWire struct {
	Kind  string     `json:"kind"`
	Path  string     `json:"path"`
	Value *wireValue `json:"value,omitempty"`
}

func toPatchOpWires(ops []patch.Op) []patchOpWire {
	out := make([]patchOpWire, len(ops))
	for i, op := range ops {
		w := patchOpWire{
			Kind: op.Kind.String(),
			Path: op.Path,
		}
		if op.Value != nil {
			v := wireValue{op.Value}
			w.Value = &v
		}
		out[i] = w
	}
	return out
}

type patchCase struct {
	ID       int
	Op       string
	Doc      lkal.Record
	Old      lkal.Record
	Next     lkal.Record
	Ops      []patch.Op
	Entrance patch.Entrance
	Rev      int64
	Now      string
}

func (c patchCase) MarshalJSON() ([]byte, error) {
	switch c.Op {
	case "apply":
		return json.Marshal(struct {
			ID  int           `json:"id"`
			Op  string        `json:"op"`
			Doc wireValue     `json:"doc"`
			Ops []patchOpWire `json:"ops"`
		}{c.ID, c.Op, wireValue{c.Doc}, toPatchOpWires(c.Ops)})
	case "diff":
		return json.Marshal(struct {
			ID   int       `json:"id"`
			Op   string    `json:"op"`
			Old  wireValue `json:"old"`
			Next wireValue `json:"next"`
		}{c.ID, c.Op, wireValue{c.Old}, wireValue{c.Next}})
	case "toRecord":
		return json.Marshal(struct {
			ID  int           `json:"id"`
			Op  string        `json:"op"`
			Ops []patchOpWire `json:"ops"`
		}{c.ID, c.Op, toPatchOpWires(c.Ops)})
	case "fromRecord":
		return json.Marshal(struct {
			ID  int       `json:"id"`
			Op  string    `json:"op"`
			Doc wireValue `json:"doc"`
		}{c.ID, c.Op, wireValue{c.Doc}})
	case "applyEntrance":
		return json.Marshal(struct {
			ID       int           `json:"id"`
			Op       string        `json:"op"`
			Entrance string        `json:"entrance"`
			Old      wireValue     `json:"old"`
			Rev      string        `json:"rev"`
			Ops      []patchOpWire `json:"ops"`
		}{c.ID, c.Op, c.Entrance.String(), wireValue{c.Old}, strconv.FormatInt(c.Rev, 10), toPatchOpWires(c.Ops)})
	case "patch":
		return json.Marshal(struct {
			ID  int           `json:"id"`
			Op  string        `json:"op"`
			Old wireValue     `json:"old"`
			Rev string        `json:"rev"`
			Ops []patchOpWire `json:"ops"`
		}{c.ID, c.Op, wireValue{c.Old}, strconv.FormatInt(c.Rev, 10), toPatchOpWires(c.Ops)})
	case "patchStatus":
		return json.Marshal(struct {
			ID  int           `json:"id"`
			Op  string        `json:"op"`
			Old wireValue     `json:"old"`
			Rev string        `json:"rev"`
			Ops []patchOpWire `json:"ops"`
			Now string        `json:"now"`
		}{c.ID, c.Op, wireValue{c.Old}, strconv.FormatInt(c.Rev, 10), toPatchOpWires(c.Ops), c.Now})
	default:
		return nil, fmt.Errorf("unknown patch op %q", c.Op)
	}
}

type patchResult struct {
	ID  int
	Op  string
	OK  bool
	Err string
	Doc lkal.Record
	Ops []patch.Op
}

var (
	patchErrClasses = []string{
		"internal",
		"malformed",
		"conflict",
		"notFound",
		"forbidden",
		"observed",
	}

	patchRequiredBuckets = []string{
		"apply/ok",
		"apply/malformed",
		"apply/conflict",
		"apply/notFound",
		"diff/ok",
		"diff/malformed",
		"toRecord/ok",
		"toRecord/malformed",
		"fromRecord/ok",
		"fromRecord/malformed",
		"applyEntrance/ok",
		"applyEntrance/internal",
		"applyEntrance/malformed",
		"applyEntrance/conflict",
		"applyEntrance/notFound",
		"applyEntrance/forbidden",
		"patch/ok",
		"patch/internal",
		"patch/malformed",
		"patch/conflict",
		"patch/notFound",
		"patch/forbidden",
		"patchStatus/ok",
		"patchStatus/internal",
		"patchStatus/malformed",
		"patchStatus/conflict",
		"patchStatus/notFound",
		"patchStatus/forbidden",
		"patchStatus/observed",
	}

	patchValidSegs = []string{"a", "b", "c", "m", "z", "replicas", "image", "nested", "only", "items", "café", "日本語", "k0", "a$b"}
	patchBadPaths  = []string{"", "/", "/a", "a/", "a//b", "$a", "a/$b", "a/$/b", "$"}
	patchBadSegs   = []string{"", "$sys", "$", "a/b", "/lead", "trail/"}
)

func genPatchValue(rng *rand.Rand, depth int) lkal.Value {
	limit := 7
	if depth > 0 {
		limit = 9
	}
	switch rng.IntN(limit) {
	case 0:
		return lkal.String(pick(rng, []string{"web", "", "fast", "café", "日本語", "1.0", "true"}))
	case 1:
		return lkal.Int(pick(rng, []int64{math.MinInt64, -42, -1, 0, 1, 3, 42, math.MaxInt64}))
	case 2:
		return lkal.Decimal(pick(rng, []string{"1.0", "0.25", "-1.5", "0.0", "3.14"}))
	case 3:
		return lkal.Bool(rng.IntN(2) == 0)
	case 4:
		return lkal.Enum(pick(rng, []string{"true", "false", "tcp", "udp", "allow"}))
	case 5:
		return lkal.Quantity{
			Number: pick(rng, []string{"512", "100", "2.5"}),
			Unit:   pick(rng, []string{"Mi", "m", "Gi", "s"}),
		}
	case 6:
		if chance(rng, 20) {
			return lkal.Ref{}
		}
		return lkal.Ref{pick(rng, []string{"secret", "config", "pod"}), pick(rng, envNames)}
	case 7:
		n := rng.IntN(3)
		items := make(lkal.List, n)
		for i := range n {
			items[i] = genPatchValue(rng, depth-1)
		}
		return items
	default:
		if chance(rng, 25) {
			return envRec()
		}
		count := 1 + rng.IntN(3)
		keys := slices.Clone(patchValidSegs)
		rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
		fields := make([]lkal.Field, count)
		for i := range count {
			fields[i] = envField(keys[i], genPatchValue(rng, depth-1))
		}
		return lkal.Record{Fields: fields}
	}
}

func genPatchDoc(rng *rand.Rand) lkal.Record {
	fields := []lkal.Field{
		envField("a", lkal.Int(1)),
		envField("scalar", lkal.String(pick(rng, []string{"alpha", "beta", "café"}))),
		envField("list", lkal.List{lkal.Int(1), lkal.Int(2)}),
		envField("nested", envRec(
			envField("keep", lkal.String("ok")),
			envField("drop", lkal.Bool(true)),
		)),
		envField("only", envRec(
			envField("leaf", lkal.Int(7)),
		)),
	}
	if chance(rng, 60) {
		fields = append(fields, envField("z", genPatchValue(rng, 1)))
	}
	if chance(rng, 50) {
		fields = append(fields, envField("日本語", genPatchValue(rng, 0)))
	}
	doc := lkal.Record{Fields: fields}
	if chance(rng, 70) {
		doc = envShuffleRecord(rng, doc)
	}
	return doc
}

func genBadPatchOp(rng *rand.Rand) patch.Op {
	dupRec := envRec(envField("dup", lkal.Int(1)), envField("dup", lkal.Int(2)))
	switch rng.IntN(6) {
	case 0:
		return patch.Op{Kind: patch.OpSet, Path: pick(rng, patchBadPaths), Value: lkal.Int(1)}
	case 1:
		return patch.Op{Kind: patch.OpDel, Path: pick(rng, patchBadPaths)}
	case 2:
		return patch.Op{Kind: patch.OpDel, Path: "a", Value: lkal.Int(1)}
	case 3:
		return patch.Op{Kind: patch.OpSet, Path: "a", Value: nil}
	case 4:
		return patch.Op{Kind: patch.OpTest, Path: "a", Value: nil}
	default:
		badVal := lkal.Value(dupRec)
		if chance(rng, 45) {
			badVal = lkal.List{lkal.String("ok"), dupRec}
		}
		kind := patch.OpSet
		if chance(rng, 50) {
			kind = patch.OpTest
		}
		return patch.Op{Kind: kind, Path: "a", Value: badVal}
	}
}

func genValidPatchOps(rng *rand.Rand, minOps int) []patch.Op {
	n := minOps + rng.IntN(6-minOps)
	ops := make([]patch.Op, n)
	for i := range n {
		seg1 := pick(rng, patchValidSegs)
		p := seg1
		if chance(rng, 55) {
			p += "/" + pick(rng, patchValidSegs)
		}
		switch rng.IntN(3) {
		case 0:
			ops[i] = patch.Op{Kind: patch.OpTest, Path: p, Value: genPatchValue(rng, 1)}
		case 1:
			ops[i] = patch.Op{Kind: patch.OpSet, Path: p, Value: genPatchValue(rng, 1)}
		default:
			ops[i] = patch.Op{Kind: patch.OpDel, Path: p}
		}
	}
	return ops
}

func otherTimestamp(ts string) string {
	for _, candidate := range envTimestamps {
		if candidate != ts {
			return candidate
		}
	}
	return envTimestamps[0]
}

func otherCondState(cur lkal.Enum) lkal.Enum {
	for _, st := range envCondStates {
		if st != cur {
			return st
		}
	}
	return lkal.Enum("false")
}

// buildPatchStoredDoc builds a valid stored envelope with small generation (< 10)
// so Lean's unbounded Int generation arithmetic never diverges from int64.
func buildPatchStoredDoc(rng *rand.Rand, minGen int64, needObs, needCond bool) (lkal.Record, int64) {
	metaFields := []lkal.Field{
		envField("kind", lkal.String(pick(rng, envKinds))),
		envField("name", lkal.String(pick(rng, envNames))),
		envField("labels", envRec(
			envField("env", lkal.String(pick(rng, []string{"dev", "prod"}))),
			envField("tier", lkal.String("frontend")),
		)),
	}
	if g := pick(rng, envGroups); g != "" {
		metaFields = append(metaFields, envField("group", lkal.String(g)))
	}
	spec := envRec(
		envField("replicas", lkal.Int(3)),
		envField("nested", envRec(
			envField("keep", lkal.String("ok")),
			envField("drop", lkal.Bool(true)),
		)),
		envField("only", envRec(
			envField("leaf", lkal.Int(7)),
		)),
	)
	cand := envRec(
		envField("meta", lkal.Record{Fields: metaFields}),
		envField("spec", spec),
	)
	doc, err := envelope.Create(cand, pick(rng, envUIDs))
	if err != nil {
		panic(fmt.Sprintf("envelope.Create failed: %v", err))
	}
	rev := int64(1 + rng.IntN(20))
	gen := int64(1)
	targetGen := minGen + int64(rng.IntN(3))
	if targetGen < 1 {
		targetGen = 1
	}
	for gen < targetGen {
		upd := envCloneRecord(doc)
		curSpec, _ := envGetRecord(upd, "spec")
		curSpec = envSetField(curSpec, "bump", lkal.Int(gen))
		upd = envSetField(upd, "spec", curSpec)
		next, err := envelope.Update(doc, rev, upd)
		if err != nil {
			panic(fmt.Sprintf("envelope.Update failed: %v", err))
		}
		doc = next
		rev++
		gen++
	}
	if needObs || needCond || chance(rng, 75) {
		var stFields []lkal.Field
		if needObs || chance(rng, 70) {
			lo := int64(1)
			if needObs && gen >= 2 {
				lo = 2
			}
			obs := lo
			if gen > lo {
				obs = lo + rng.Int64N(gen-lo+1)
			}
			stFields = append(stFields, envField("observedGeneration", lkal.Int(obs)))
		}
		stFields = append(stFields, envField("phase", lkal.String("Running")))
		if needCond || chance(rng, 80) {
			stFields = append(stFields, envField("condition", envRec(
				envField("Ready", envRec(
					envField("state", pick(rng, envCondStates)),
					envField("reason", lkal.String("Ready")),
				)),
			)))
		}
		stCand := envSetField(envCloneRecord(doc), "status", lkal.Record{Fields: stFields})
		next, err := envelope.UpdateStatus(doc, rev, stCand, pick(rng, envTimestamps))
		if err != nil {
			panic(fmt.Sprintf("envelope.UpdateStatus failed: %v", err))
		}
		doc = next
		rev++
	}
	if chance(rng, 55) {
		doc = envShuffleRecord(rng, doc)
	}
	return doc, rev
}

func getStoredGenAndObs(doc lkal.Record) (gen int64, obs int64, hasObs bool) {
	meta, _ := envGetRecord(doc, "meta")
	gv, _ := envGetField(meta, "generation")
	gen = int64(gv.(lkal.Int))
	if st, ok := envGetRecord(doc, "status"); ok {
		if ov, ok := envGetField(st, "observedGeneration"); ok {
			obs = int64(ov.(lkal.Int))
			hasObs = true
		}
	}
	return gen, obs, hasObs
}

func getStoredReadyCond(doc lkal.Record) (state lkal.Enum, since string, ok bool) {
	st, hasSt := envGetRecord(doc, "status")
	if !hasSt {
		return "", "", false
	}
	cond, hasCond := envGetRecord(st, "condition")
	if !hasCond {
		return "", "", false
	}
	ready, hasReady := envGetRecord(cond, "Ready")
	if !hasReady {
		return "", "", false
	}
	sv, _ := envGetField(ready, "state")
	tv, _ := envGetField(ready, "since")
	return sv.(lkal.Enum), string(tv.(lkal.String)), true
}

func genFromRecordOK(rng *rand.Rand) lkal.Record {
	ops := genValidPatchOps(rng, 0)
	if len(ops) == 0 {
		return envRec(envField(patch.PatchField, envRec()))
	}
	keys := pickDistinctKeys(rng, len(ops))
	orders := pickDistinctOrders(rng, len(ops))
	fields := make([]lkal.Field, len(ops))
	for i, op := range ops {
		entryFields := []lkal.Field{
			envField(orderedset.OrderField, lkal.Int(orders[i])),
			envField("op", lkal.Enum(op.Kind.String())),
			envField("path", lkal.String(op.Path)),
		}
		if op.Value != nil {
			entryFields = append(entryFields, envField("value", envCloneValue(op.Value)))
		}
		entryRec := lkal.Record{Fields: entryFields}
		if chance(rng, 70) {
			entryRec = envShuffleRecord(rng, entryRec)
		}
		fields[i] = envField(keys[i], entryRec)
	}
	setRec := lkal.Record{Fields: fields}
	if len(fields) >= 2 && chance(rng, 85) {
		// Ensure raw field order frequently disagrees with ascending order.
		slices.SortFunc(setRec.Fields, func(a, b lkal.Field) int {
			ao, _ := envGetField(a.Value.(lkal.Record), orderedset.OrderField)
			bo, _ := envGetField(b.Value.(lkal.Record), orderedset.OrderField)
			ai, bi := int64(ao.(lkal.Int)), int64(bo.(lkal.Int))
			if ai > bi {
				return -1
			}
			if ai < bi {
				return 1
			}
			return 0
		})
	} else if chance(rng, 60) {
		setRec = envShuffleRecord(rng, setRec)
	}
	return envRec(envField(patch.PatchField, setRec))
}

func genFromRecordMalformed(rng *rand.Rand) lkal.Record {
	validEntry := envRec(
		envField(orderedset.OrderField, lkal.Int(10)),
		envField("op", lkal.Enum("set")),
		envField("path", lkal.String("spec/a")),
		envField("value", lkal.Int(1)),
	)
	switch rng.IntN(16) {
	case 0:
		return envRec()
	case 1:
		return envRec(envField(patch.PatchField, envRec()), envField("extra", lkal.Int(1)))
	case 2:
		return envRec(envField("other", envRec()))
	case 3:
		return envRec(envField(patch.PatchField, envRec()), envField(patch.PatchField, envRec()))
	case 4:
		return envRec(envField(patch.PatchField, lkal.Int(1)))
	case 5:
		return envRec(envField(patch.PatchField, envRec(envField(pick(rng, patchBadSegs), validEntry))))
	case 6:
		return envRec(envField(patch.PatchField, envRec(envField("0", lkal.String("not-a-record")))))
	case 7:
		return envRec(envField(patch.PatchField, envRec(envField("0", envDropField(validEntry, orderedset.OrderField)))))
	case 8:
		return envRec(envField(patch.PatchField, envRec(envField("0", envSetField(validEntry, orderedset.OrderField, lkal.String("10"))))))
	case 9:
		return envRec(envField(patch.PatchField, envRec(
			envField("0", validEntry),
			envField("1", envCloneRecord(validEntry)),
		)))
	case 10:
		e := envCloneRecord(validEntry)
		if chance(rng, 35) {
			e = envDropField(e, "op")
		} else if chance(rng, 50) {
			e = envSetField(e, "op", lkal.String("set"))
		} else {
			e = envSetField(e, "op", lkal.Enum("noop"))
		}
		return envRec(envField(patch.PatchField, envRec(envField("0", e))))
	case 11:
		e := envCloneRecord(validEntry)
		if chance(rng, 35) {
			e = envDropField(e, "path")
		} else if chance(rng, 50) {
			e = envSetField(e, "path", lkal.Int(1))
		} else {
			e = envSetField(e, "path", lkal.String(pick(rng, patchBadPaths)))
		}
		return envRec(envField(patch.PatchField, envRec(envField("0", e))))
	case 12:
		e := envSetField(validEntry, "op", lkal.Enum("del"))
		return envRec(envField(patch.PatchField, envRec(envField("0", e))))
	case 13:
		kind := "set"
		if chance(rng, 50) {
			kind = "test"
		}
		e := envDropField(envSetField(validEntry, "op", lkal.Enum(kind)), "value")
		return envRec(envField(patch.PatchField, envRec(envField("0", e))))
	case 14:
		e := envSetField(validEntry, "extra", lkal.Bool(true))
		return envRec(envField(patch.PatchField, envRec(envField("0", e))))
	default:
		dupVal := envRec(envField("x", lkal.Int(1)), envField("x", lkal.Int(2)))
		e := envSetField(validEntry, "value", dupVal)
		return envRec(envField(patch.PatchField, envRec(envField("0", e))))
	}
}

func genPatchBucketCase(rng *rand.Rand, id int, bucket string) patchCase {
	op, outcome, _ := strings.Cut(bucket, "/")
	c := patchCase{ID: id, Op: op}

	switch op {
	case "apply":
		doc := genPatchDoc(rng)
		c.Doc = doc
		switch outcome {
		case "ok":
			switch rng.IntN(4) {
			case 0:
				c.Ops = nil
			case 1:
				newVal := genPatchValue(rng, 1)
				c.Ops = []patch.Op{
					{Kind: patch.OpTest, Path: "a", Value: lkal.Int(1)},
					{Kind: patch.OpSet, Path: "a", Value: newVal},
					{Kind: patch.OpTest, Path: "a", Value: envCloneValue(newVal)},
					{Kind: patch.OpDel, Path: "only/leaf"},
					{Kind: patch.OpTest, Path: "only", Value: envRec()},
				}
			case 2:
				nestedVal, _ := envGetRecord(doc, "nested")
				c.Ops = []patch.Op{
					{Kind: patch.OpTest, Path: "nested", Value: envShuffleRecord(rng, nestedVal)},
					{Kind: patch.OpSet, Path: "created/deep/leaf", Value: genPatchValue(rng, 1)},
					{Kind: patch.OpDel, Path: "nested/drop"},
					{Kind: patch.OpSet, Path: "nested/added", Value: genPatchValue(rng, 0)},
				}
			default:
				v := genPatchValue(rng, 1)
				c.Ops = []patch.Op{
					{Kind: patch.OpSet, Path: "temp/child", Value: v},
					{Kind: patch.OpTest, Path: "temp/child", Value: envCloneValue(v)},
					{Kind: patch.OpDel, Path: "temp/child"},
					{Kind: patch.OpSet, Path: "top", Value: genPatchValue(rng, 1)},
				}
			}
		case "malformed":
			switch rng.IntN(4) {
			case 0:
				c.Doc = injectOSDupKey(rng, doc)
				c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "k", Value: lkal.Int(1)}}
			case 1:
				badOp := genBadPatchOp(rng)
				if chance(rng, 65) {
					if chance(rng, 50) {
						c.Ops = []patch.Op{{Kind: patch.OpTest, Path: "missing", Value: lkal.Int(1)}, badOp}
					} else {
						c.Ops = []patch.Op{{Kind: patch.OpDel, Path: "missing"}, badOp}
					}
				} else {
					c.Ops = []patch.Op{badOp}
				}
			case 2:
				c.Ops = []patch.Op{{Kind: patch.OpSet, Path: pick(rng, []string{"a/sub", "scalar/sub", "list/0"}), Value: lkal.Int(1)}}
			default:
				c.Ops = []patch.Op{{Kind: patch.OpDel, Path: pick(rng, []string{"a/sub", "scalar/sub", "list/0"})}}
			}
		case "conflict":
			var prefix []patch.Op
			if chance(rng, 35) {
				prefix = append(prefix, patch.Op{Kind: patch.OpSet, Path: "staged", Value: lkal.Int(99)})
			}
			var failOp patch.Op
			switch rng.IntN(4) {
			case 0:
				failOp = patch.Op{Kind: patch.OpTest, Path: "a", Value: lkal.Decimal("1.0")}
			case 1:
				failOp = patch.Op{Kind: patch.OpTest, Path: "nested/drop", Value: pick(rng, []lkal.Value{lkal.Enum("true"), lkal.String("true")})}
			case 2:
				failOp = patch.Op{Kind: patch.OpTest, Path: "a", Value: lkal.Int(2)}
			default:
				failOp = patch.Op{Kind: patch.OpTest, Path: pick(rng, []string{"missing", "nested/missing", "a/sub"}), Value: lkal.Int(1)}
			}
			c.Ops = append(prefix, failOp)
		case "notFound":
			switch rng.IntN(4) {
			case 0:
				c.Ops = []patch.Op{{Kind: patch.OpDel, Path: "missing"}}
			case 1:
				c.Ops = []patch.Op{{Kind: patch.OpDel, Path: "missing/sub/leaf"}}
			case 2:
				c.Ops = []patch.Op{{Kind: patch.OpDel, Path: "nested/missing"}}
			default:
				c.Ops = []patch.Op{
					{Kind: patch.OpDel, Path: "nested/drop"},
					{Kind: patch.OpDel, Path: "nested/drop"},
				}
			}
		}

	case "diff":
		switch outcome {
		case "ok":
			old := genPatchDoc(rng)
			switch rng.IntN(4) {
			case 0:
				c.Old = old
				c.Next = envShuffleRecord(rng, envCloneRecord(old))
			case 1:
				old = envSetField(old, "z", lkal.Int(10))
				old = envSetField(old, "dropA", lkal.String("bye"))
				old = envSetField(old, "dropZ", lkal.Bool(false))
				next := envCloneRecord(old)
				next = envDropField(next, "dropA")
				next = envDropField(next, "dropZ")
				next = envSetField(next, "a", lkal.Decimal("1.0"))
				next = envSetField(next, "addedB", lkal.String("hi"))
				next = envSetField(next, "addedA", lkal.Int(2))
				c.Old = envShuffleRecord(rng, old)
				c.Next = envShuffleRecord(rng, next)
			case 2:
				old = envSetField(old, "emptyToNon", envRec())
				old = envSetField(old, "nonToEmpty", envRec(envField("x", lkal.Int(9))))
				next := envCloneRecord(old)
				next = envSetField(next, "emptyToNon", envRec(envField("y", lkal.Enum("tcp"))))
				next = envSetField(next, "nonToEmpty", envRec())
				nested, _ := envGetRecord(next, "nested")
				nested = envDropField(nested, "drop")
				nested = envSetField(nested, "keep", lkal.String("changed"))
				next = envSetField(next, "nested", nested)
				if chance(rng, 50) {
					next = envSetField(next, "scalar", envRec(envField("k", lkal.Int(1))))
				}
				c.Old = envShuffleRecord(rng, old)
				c.Next = envShuffleRecord(rng, next)
			default:
				badSubKey := pick(rng, []string{"app.example.com/name", "$reserved"})
				old = envSetField(old, "labels", envRec(envField(badSubKey, lkal.String("old"))))
				next := envSetField(envCloneRecord(old), "labels", envRec(envField(badSubKey, lkal.String("new"))))
				next = envDropField(next, "scalar")
				c.Old = envShuffleRecord(rng, old)
				c.Next = envShuffleRecord(rng, next)
			}
		case "malformed":
			old := genPatchDoc(rng)
			next := genPatchDoc(rng)
			switch rng.IntN(4) {
			case 0:
				old = injectOSDupKey(rng, old)
			case 1:
				next = injectOSDupKey(rng, next)
			case 2:
				old = envSetField(old, pick(rng, patchBadSegs), lkal.Int(1))
			default:
				next = envSetField(next, pick(rng, patchBadSegs), lkal.Int(1))
			}
			c.Old = old
			c.Next = next
		}

	case "toRecord":
		switch outcome {
		case "ok":
			c.Ops = genValidPatchOps(rng, 0)
		case "malformed":
			ops := genValidPatchOps(rng, 0)
			bad := genBadPatchOp(rng)
			idx := rng.IntN(len(ops) + 1)
			ops = slices.Insert(ops, idx, bad)
			c.Ops = ops
		}

	case "fromRecord":
		switch outcome {
		case "ok":
			c.Doc = genFromRecordOK(rng)
		case "malformed":
			c.Doc = genFromRecordMalformed(rng)
		}

	case "applyEntrance":
		old, rev := buildPatchStoredDoc(rng, 1, false, true)
		c.Old = old
		c.Rev = rev
		c.Entrance = patch.EntranceSpec
		if chance(rng, 50) {
			c.Entrance = patch.EntranceStatus
		}
		meta, _ := envGetRecord(old, "meta")
		uidVal, _ := envGetField(meta, "uid")

		switch outcome {
		case "ok":
			var ops []patch.Op
			if chance(rng, 70) {
				ops = append(ops, patch.Op{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(rev)})
			}
			if chance(rng, 40) {
				ops = append(ops, patch.Op{Kind: patch.OpTest, Path: "meta/uid", Value: uidVal})
			}
			if c.Entrance == patch.EntranceSpec {
				switch rng.IntN(3) {
				case 0:
					ops = append(ops,
						patch.Op{Kind: patch.OpSet, Path: "meta/labels/env", Value: lkal.String("prod")},
						patch.Op{Kind: patch.OpDel, Path: "meta/labels/tier"},
					)
				case 1:
					ops = append(ops,
						patch.Op{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(5)},
						patch.Op{Kind: patch.OpDel, Path: "spec/only/leaf"},
					)
				default:
					ops = append(ops, patch.Op{Kind: patch.OpSet, Path: "spec/created/k", Value: genPatchValue(rng, 0)})
				}
			} else {
				switch rng.IntN(3) {
				case 0:
					ops = append(ops, patch.Op{Kind: patch.OpSet, Path: "status/phase", Value: lkal.String("Synced")})
				case 1:
					ops = append(ops, patch.Op{Kind: patch.OpSet, Path: "status/condition/Ready/reason", Value: lkal.String("AllReady")})
				default:
					ops = append(ops, patch.Op{Kind: patch.OpDel, Path: "status/phase"})
				}
			}
			c.Ops = ops

		case "internal":
			if chance(rng, 55) {
				c.Old = corruptStoredDoc(rng, old)
			} else {
				c.Rev = pick(rng, []int64{0, -1, -7, math.MinInt64})
			}
			if chance(rng, 40) {
				c.Ops = []patch.Op{genBadPatchOp(rng)}
			}

		case "malformed":
			switch rng.IntN(3) {
			case 0:
				badOp := genBadPatchOp(rng)
				if chance(rng, 55) {
					c.Ops = []patch.Op{{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(rev + 1)}, badOp}
				} else {
					c.Ops = []patch.Op{badOp}
				}
			case 1:
				if chance(rng, 50) {
					c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "unknown/x", Value: lkal.Int(1)}}
				} else {
					c.Ops = []patch.Op{{Kind: patch.OpDel, Path: "unknown"}}
				}
			default:
				if c.Entrance == patch.EntranceSpec {
					c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "spec/replicas/sub", Value: lkal.Int(1)}}
				} else {
					c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "status/phase/sub", Value: lkal.Int(1)}}
				}
			}

		case "conflict":
			switch rng.IntN(3) {
			case 0:
				c.Ops = []patch.Op{{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(rev + 1)}}
			case 1:
				c.Ops = []patch.Op{{Kind: patch.OpTest, Path: "spec/replicas", Value: lkal.Decimal("3.0")}}
			default:
				c.Ops = []patch.Op{{Kind: patch.OpTest, Path: "meta/uid", Value: lkal.String(differentValidUID(old))}}
			}

		case "notFound":
			if c.Entrance == patch.EntranceSpec {
				c.Ops = []patch.Op{{Kind: patch.OpDel, Path: pick(rng, []string{"spec/missing", "spec/missing/sub", "meta/labels/missing"})}}
			} else {
				c.Ops = []patch.Op{{Kind: patch.OpDel, Path: pick(rng, []string{"status/missing", "status/missing/sub", "status/condition/Missing/reason"})}}
			}

		case "forbidden":
			if c.Entrance == patch.EntranceSpec {
				c.Ops = []patch.Op{pick(rng, []patch.Op{
					{Kind: patch.OpSet, Path: "meta", Value: envRec()},
					{Kind: patch.OpDel, Path: "meta"},
					{Kind: patch.OpSet, Path: "meta/uid", Value: lkal.String(differentValidUID(old))},
					{Kind: patch.OpDel, Path: "meta/uid"},
					{Kind: patch.OpSet, Path: "meta/revision", Value: lkal.Int(99)},
					{Kind: patch.OpDel, Path: "meta/revision"},
					{Kind: patch.OpSet, Path: "meta/generation", Value: lkal.Int(99)},
					{Kind: patch.OpSet, Path: "meta/kind", Value: lkal.String("other")},
					{Kind: patch.OpSet, Path: "meta/name", Value: lkal.String("other")},
					{Kind: patch.OpSet, Path: "meta/group", Value: lkal.String("apps.example.com")},
					{Kind: patch.OpSet, Path: "status/phase", Value: lkal.String("Failed")},
					{Kind: patch.OpDel, Path: "status"},
					{Kind: patch.OpDel, Path: "status/missing"},
				})}
			} else {
				c.Ops = []patch.Op{pick(rng, []patch.Op{
					{Kind: patch.OpSet, Path: "meta/labels/env", Value: lkal.String("prod")},
					{Kind: patch.OpDel, Path: "meta/labels"},
					{Kind: patch.OpSet, Path: "meta/uid", Value: lkal.String(differentValidUID(old))},
					{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(9)},
					{Kind: patch.OpDel, Path: "spec"},
					{Kind: patch.OpSet, Path: "status/condition/Ready/since", Value: lkal.String(envTimestamps[0])},
					{Kind: patch.OpDel, Path: "status/condition/Ready/since"},
					{Kind: patch.OpSet, Path: "status/condition/Ready/since/nested", Value: lkal.Int(1)},
					{Kind: patch.OpDel, Path: "status/condition/Missing/since"},
				})}
			}
		}

	case "patch":
		old, rev := buildPatchStoredDoc(rng, 1, false, false)
		c.Old = old
		c.Rev = rev
		switch outcome {
		case "ok":
			var ops []patch.Op
			if chance(rng, 65) {
				ops = append(ops, patch.Op{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(rev)})
			}
			if chance(rng, 45) {
				// Spec unchanged -> generation kept.
				switch rng.IntN(3) {
				case 0:
					// Test-only or empty.
				case 1:
					ops = append(ops,
						patch.Op{Kind: patch.OpSet, Path: "meta/labels/env", Value: lkal.String("staging")},
						patch.Op{Kind: patch.OpDel, Path: "meta/labels/tier"},
					)
				default:
					ops = append(ops, patch.Op{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(3)})
				}
			} else {
				// Spec changed -> generation bumped.
				switch rng.IntN(4) {
				case 0:
					ops = append(ops, patch.Op{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(5)})
				case 1:
					ops = append(ops, patch.Op{Kind: patch.OpDel, Path: "spec/only/leaf"})
				case 2:
					ops = append(ops, patch.Op{Kind: patch.OpSet, Path: "spec/extra/v", Value: genPatchValue(rng, 0)})
				default:
					ops = append(ops, patch.Op{Kind: patch.OpDel, Path: "spec"})
				}
			}
			c.Ops = ops

		case "internal":
			if chance(rng, 55) {
				c.Old = corruptStoredDoc(rng, old)
			} else {
				c.Rev = pick(rng, []int64{0, -1, -5})
			}

		case "malformed":
			switch rng.IntN(4) {
			case 0:
				c.Ops = []patch.Op{genBadPatchOp(rng)}
			case 1:
				c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "unknown/x", Value: lkal.Int(1)}}
			case 2:
				c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "spec/replicas/sub", Value: lkal.Int(1)}}
			default:
				// Succeeds in ApplyEntrance, rejected by envelope.Update shape check.
				c.Ops = []patch.Op{pick(rng, []patch.Op{
					{Kind: patch.OpSet, Path: "spec", Value: lkal.Int(1)},
					{Kind: patch.OpSet, Path: "meta/labels", Value: lkal.String("bad")},
					{Kind: patch.OpSet, Path: "meta/labels/k", Value: lkal.Int(1)},
				})}
			}

		case "conflict":
			switch rng.IntN(3) {
			case 0:
				c.Ops = []patch.Op{{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(rev + 1)}}
			case 1:
				c.Ops = []patch.Op{{Kind: patch.OpTest, Path: "spec/replicas", Value: lkal.Decimal("3.0")}}
			default:
				c.Ops = []patch.Op{{Kind: patch.OpTest, Path: "spec/missing", Value: lkal.Int(1)}}
			}

		case "notFound":
			c.Ops = []patch.Op{{Kind: patch.OpDel, Path: pick(rng, []string{"spec/missing", "spec/missing/sub", "meta/labels/missing"})}}

		case "forbidden":
			c.Ops = []patch.Op{pick(rng, []patch.Op{
				{Kind: patch.OpSet, Path: "meta/uid", Value: lkal.String(differentValidUID(old))},
				{Kind: patch.OpDel, Path: "meta/uid"},
				{Kind: patch.OpSet, Path: "meta/revision", Value: lkal.Int(rev + 1)},
				{Kind: patch.OpSet, Path: "meta/generation", Value: lkal.Int(99)},
				{Kind: patch.OpSet, Path: "meta/kind", Value: lkal.String("other")},
				{Kind: patch.OpSet, Path: "meta/name", Value: lkal.String("other")},
				{Kind: patch.OpSet, Path: "status/phase", Value: lkal.String("Failed")},
				{Kind: patch.OpDel, Path: "status"},
			})}
		}

	case "patchStatus":
		needObs := outcome == "observed"
		minGen := int64(1)
		if needObs {
			minGen = 2
		}
		old, rev := buildPatchStoredDoc(rng, minGen, needObs, true)
		gen, oldObs, hasOldObs := getStoredGenAndObs(old)
		readyState, readySince, _ := getStoredReadyCond(old)
		c.Old = old
		c.Rev = rev
		c.Now = otherTimestamp(readySince)

		switch outcome {
		case "ok":
			var ops []patch.Op
			if chance(rng, 60) {
				ops = append(ops, patch.Op{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(rev)})
			}
			switch rng.IntN(4) {
			case 0:
				// Same condition state -> since preserved.
				ops = append(ops, patch.Op{Kind: patch.OpSet, Path: "status/condition/Ready/reason", Value: lkal.String("AllReady")})
				if gen >= oldObs {
					ops = append(ops, patch.Op{Kind: patch.OpSet, Path: "status/observedGeneration", Value: lkal.Int(gen)})
				}
			case 1:
				// Different condition state -> since updated to now.
				ops = append(ops, patch.Op{Kind: patch.OpSet, Path: "status/condition/Ready/state", Value: otherCondState(readyState)})
			case 2:
				// Brand-new condition -> since stamped to now.
				ops = append(ops, patch.Op{Kind: patch.OpSet, Path: "status/condition/Degraded", Value: envRec(
					envField("state", lkal.Enum("false")),
					envField("reason", lkal.String("Healthy")),
				)})
			default:
				if !hasOldObs && chance(rng, 50) {
					ops = append(ops, patch.Op{Kind: patch.OpDel, Path: "status"})
				} else {
					ops = append(ops, patch.Op{Kind: patch.OpDel, Path: "status/phase"})
				}
			}
			c.Ops = ops

		case "internal":
			switch rng.IntN(4) {
			case 0:
				c.Old = corruptStoredDoc(rng, old)
			case 1:
				c.Rev = pick(rng, []int64{0, -1, -5})
			default:
				c.Now = pick(rng, envBadTS)
				if chance(rng, 60) {
					c.Ops = []patch.Op{genBadPatchOp(rng)}
				}
			}

		case "malformed":
			switch rng.IntN(4) {
			case 0:
				c.Ops = []patch.Op{genBadPatchOp(rng)}
			case 1:
				c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "unknown/x", Value: lkal.Int(1)}}
			case 2:
				c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "status/phase/sub", Value: lkal.Int(1)}}
			default:
				// Succeeds in ApplyEntrance, rejected by envelope.UpdateStatus shape check.
				c.Ops = []patch.Op{pick(rng, []patch.Op{
					{Kind: patch.OpSet, Path: "status", Value: lkal.Int(1)},
					{Kind: patch.OpSet, Path: "status/observedGeneration", Value: lkal.Int(0)},
					{Kind: patch.OpSet, Path: "status/condition", Value: lkal.List{}},
					{Kind: patch.OpSet, Path: "status/condition/badtype", Value: envRec(
						envField("state", lkal.Enum("true")),
						envField("reason", lkal.String("Ok")),
					)},
					{Kind: patch.OpSet, Path: "status/condition/Ready/state", Value: lkal.Enum("maybe")},
					{Kind: patch.OpSet, Path: "status/condition/Ready/reason", Value: lkal.String("")},
					{Kind: patch.OpDel, Path: "status/condition/Ready/reason"},
					{Kind: patch.OpSet, Path: "status/condition/Ready/extra", Value: lkal.String("x")},
					{Kind: patch.OpSet, Path: "status/condition/NewCond", Value: envRec(
						envField("state", lkal.Enum("true")),
						envField("reason", lkal.String("Ok")),
						envField("since", lkal.String(pick(rng, envBadTS))),
					)},
				})}
			}

		case "conflict":
			switch rng.IntN(3) {
			case 0:
				c.Ops = []patch.Op{{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(rev + 1)}}
			case 1:
				c.Ops = []patch.Op{{Kind: patch.OpTest, Path: "status/phase", Value: lkal.String("Wrong")}}
			default:
				c.Ops = []patch.Op{{Kind: patch.OpTest, Path: "spec/replicas", Value: lkal.Decimal("3.0")}}
			}

		case "notFound":
			c.Ops = []patch.Op{{Kind: patch.OpDel, Path: pick(rng, []string{"status/missing", "status/missing/sub", "status/condition/Missing/reason"})}}

		case "forbidden":
			switch rng.IntN(4) {
			case 0:
				c.Ops = []patch.Op{pick(rng, []patch.Op{
					{Kind: patch.OpSet, Path: "meta/labels/env", Value: lkal.String("prod")},
					{Kind: patch.OpDel, Path: "meta/labels"},
					{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(9)},
					{Kind: patch.OpDel, Path: "spec"},
					{Kind: patch.OpSet, Path: "meta/uid", Value: lkal.String(differentValidUID(old))},
				})}
			case 1:
				c.Ops = []patch.Op{pick(rng, []patch.Op{
					{Kind: patch.OpSet, Path: "status/condition/Ready/since", Value: lkal.String(otherTimestamp(readySince))},
					{Kind: patch.OpDel, Path: "status/condition/Ready/since"},
					{Kind: patch.OpSet, Path: "status/condition/Ready/since/nested", Value: lkal.Int(1)},
				})}
			case 2:
				// Whole-condition replacement with mismatched since caught by envelope.UpdateStatus.
				c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "status/condition/Ready", Value: envRec(
					envField("state", readyState),
					envField("reason", lkal.String("Ready")),
					envField("since", lkal.String(otherTimestamp(readySince))),
				)}}
			default:
				// Brand-new condition carrying since caught by envelope.UpdateStatus.
				c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "status/condition/CustomNew", Value: envRec(
					envField("state", lkal.Enum("false")),
					envField("reason", lkal.String("New")),
					envField("since", lkal.String(envTimestamps[0])),
				)}}
			}

		case "observed":
			switch rng.IntN(5) {
			case 0:
				c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "status/observedGeneration", Value: lkal.Int(gen + 1 + rng.Int64N(3))}}
			case 1:
				c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "status/observedGeneration", Value: lkal.Int(oldObs - 1)}}
			case 2:
				c.Ops = []patch.Op{{Kind: patch.OpDel, Path: "status/observedGeneration"}}
			case 3:
				c.Ops = []patch.Op{{Kind: patch.OpDel, Path: "status"}}
			default:
				c.Ops = []patch.Op{{Kind: patch.OpSet, Path: "status", Value: envRec()}}
			}
		}
	}

	return c
}

func generatePatchCases(seed uint64, count int) []patchCase {
	rng := rand.New(rand.NewPCG(seed, seed^0x7061746368647274))
	cases := make([]patchCase, count)
	var epoch []string
	for i := range count {
		if len(epoch) == 0 {
			epoch = slices.Clone(patchRequiredBuckets)
			rng.Shuffle(len(epoch), func(a, b int) { epoch[a], epoch[b] = epoch[b], epoch[a] })
		}
		bucket := epoch[len(epoch)-1]
		epoch = epoch[:len(epoch)-1]
		cases[i] = genPatchBucketCase(rng, i, bucket)
	}
	return cases
}

func assertValidPatchOpsUTF8(t *testing.T, id int, ops []patch.Op) {
	t.Helper()
	for i, op := range ops {
		assertValidUTF8(t, id, fmt.Sprintf("ops[%d].path", i), op.Path)
		if op.Value != nil {
			assertValidWireValueUTF8(t, id, fmt.Sprintf("ops[%d].value", i), op.Value)
		}
	}
}

func encodePatchCases(t *testing.T, cases []patchCase) [][]byte {
	t.Helper()
	out := make([][]byte, len(cases))
	for i, c := range cases {
		switch c.Op {
		case "apply":
			assertValidWireValueUTF8(t, c.ID, "doc", c.Doc)
			assertValidPatchOpsUTF8(t, c.ID, c.Ops)
		case "diff":
			assertValidWireValueUTF8(t, c.ID, "old", c.Old)
			assertValidWireValueUTF8(t, c.ID, "next", c.Next)
		case "toRecord":
			assertValidPatchOpsUTF8(t, c.ID, c.Ops)
		case "fromRecord":
			assertValidWireValueUTF8(t, c.ID, "doc", c.Doc)
		case "applyEntrance", "patch":
			assertValidWireValueUTF8(t, c.ID, "old", c.Old)
			assertValidPatchOpsUTF8(t, c.ID, c.Ops)
		case "patchStatus":
			assertValidWireValueUTF8(t, c.ID, "old", c.Old)
			assertValidPatchOpsUTF8(t, c.ID, c.Ops)
			assertValidUTF8(t, c.ID, "now", c.Now)
		default:
			t.Fatalf("case id=%d unknown op %q", c.ID, c.Op)
		}
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal patch case %d (%s): %v", c.ID, c.Op, err)
		}
		out[i] = b
	}
	return out
}

type patchHooks struct {
	applyDelPruneEmptyParent      bool
	applyDelIgnoreMissing         bool
	applyTestIntEqDecimal         bool
	applySkipOpsOkUpfront         bool
	diffUnsortedKeys              bool
	diffSetsBeforeDels            bool
	diffRecurseInvalidSubSeg      bool
	toRecordStepOne               bool
	fromRecordUnsortedOrder       bool
	applyEntranceIgnoreMetaRev    bool
	applyEntranceSpecAllowStatus  bool
	applyEntranceSpecAllowUID     bool
	applyEntranceStatusAllowSince bool
	patchAlwaysBumpGen            bool
	patchStatusCheckOpsBeforeNow  bool
	patchStatusAlwaysUpdateSince  bool
	patchStatusAllowObsRegress    bool
}

func patchErrName(err error) (string, error) {
	var pe *patch.Error
	if !errors.As(err, &pe) {
		return "", fmt.Errorf("non-patch error %T: %w", err, err)
	}
	name := pe.Kind.String()
	if !slices.Contains(patchErrClasses, name) {
		return "", fmt.Errorf("unknown patch error kind %q", name)
	}
	return name, nil
}

func pruneEmptyParents(rec lkal.Record, parentSegs []string) lkal.Record {
	if len(parentSegs) == 0 {
		return rec
	}
	k := parentSegs[0]
	v, ok := envGetField(rec, k)
	if !ok {
		return rec
	}
	sub, isRec := v.(lkal.Record)
	if !isRec {
		return rec
	}
	if len(parentSegs) > 1 {
		sub = pruneEmptyParents(sub, parentSegs[1:])
	}
	if len(sub.Fields) == 0 {
		return envDropField(rec, k)
	}
	return envSetField(rec, k, sub)
}

func lookupValueAt(doc lkal.Record, segs []string) (lkal.Value, bool) {
	var cur lkal.Value = doc
	for _, seg := range segs {
		rec, ok := cur.(lkal.Record)
		if !ok {
			return nil, false
		}
		next, found := envGetField(rec, seg)
		if !found {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func intDecimalEqual(a, b lkal.Value) bool {
	if ai, ok := a.(lkal.Int); ok {
		if bd, ok := b.(lkal.Decimal); ok {
			return string(bd) == strconv.FormatInt(int64(ai), 10)+".0"
		}
	}
	if ad, ok := a.(lkal.Decimal); ok {
		if bi, ok := b.(lkal.Int); ok {
			return string(ad) == strconv.FormatInt(int64(bi), 10)+".0"
		}
	}
	return false
}

func runPatchCase(tc patchCase, hooks patchHooks) (patchResult, error) {
	res := patchResult{ID: tc.ID, Op: tc.Op}

	var err error
	switch tc.Op {
	case "apply":
		if hooks.applySkipOpsOkUpfront {
			if _, docErr := patch.Apply(tc.Doc, nil); docErr == nil {
				cur := envCloneRecord(tc.Doc)
				var stepErr error
				for _, op := range tc.Ops {
					next, e := patch.Apply(cur, []patch.Op{op})
					if e != nil {
						stepErr = e
						break
					}
					cur = next
				}
				if stepErr == nil {
					res.OK = true
					res.Doc = cur
					return res, nil
				}
				err = stepErr
				break
			}
		}
		if hooks.applyDelIgnoreMissing {
			if _, docErr := patch.Apply(tc.Doc, nil); docErr == nil {
				if _, opsErr := patch.ToRecord(tc.Ops); opsErr == nil {
					cur := envCloneRecord(tc.Doc)
					var stepErr error
					for _, op := range tc.Ops {
						next, e := patch.Apply(cur, []patch.Op{op})
						if e != nil {
							if op.Kind == patch.OpDel && errors.Is(e, patch.ErrNotFound) {
								continue
							}
							stepErr = e
							break
						}
						cur = next
					}
					if stepErr == nil {
						res.OK = true
						res.Doc = cur
						return res, nil
					}
					err = stepErr
					break
				}
			}
		}
		if hooks.applyTestIntEqDecimal {
			if _, docErr := patch.Apply(tc.Doc, nil); docErr == nil {
				if _, opsErr := patch.ToRecord(tc.Ops); opsErr == nil {
					cur := envCloneRecord(tc.Doc)
					var stepErr error
					for _, op := range tc.Ops {
						if op.Kind == patch.OpTest {
							segs, _ := patch.ParsePath(op.Path)
							if actual, ok := lookupValueAt(cur, segs); ok && intDecimalEqual(actual, op.Value) {
								continue
							}
						}
						next, e := patch.Apply(cur, []patch.Op{op})
						if e != nil {
							stepErr = e
							break
						}
						cur = next
					}
					if stepErr == nil {
						res.OK = true
						res.Doc = cur
						return res, nil
					}
					err = stepErr
					break
				}
			}
		}
		var doc lkal.Record
		doc, err = patch.Apply(tc.Doc, tc.Ops)
		if err == nil {
			if hooks.applyDelPruneEmptyParent {
				for _, op := range tc.Ops {
					if op.Kind == patch.OpDel {
						segs, _ := patch.ParsePath(op.Path)
						if len(segs) >= 2 {
							doc = pruneEmptyParents(doc, segs[:len(segs)-1])
						}
					}
				}
			}
			res.OK = true
			res.Doc = doc
			return res, nil
		}

	case "diff":
		var ops []patch.Op
		ops, err = patch.Diff(tc.Old, tc.Next)
		if err == nil {
			if hooks.diffUnsortedKeys && len(ops) >= 2 {
				slices.Reverse(ops)
			}
			if hooks.diffSetsBeforeDels {
				var sets, dels []patch.Op
				for _, op := range ops {
					if op.Kind == patch.OpDel {
						dels = append(dels, op)
					} else {
						sets = append(sets, op)
					}
				}
				ops = append(sets, dels...)
			}
			if hooks.diffRecurseInvalidSubSeg {
				var mutated []patch.Op
				for _, op := range ops {
					if op.Kind == patch.OpSet {
						if nrec, ok := op.Value.(lkal.Record); ok {
							expanded := false
							for _, fld := range nrec.Fields {
								if !patch.ValidSeg(fld.Key) {
									mutated = append(mutated, patch.Op{
										Kind:  patch.OpSet,
										Path:  op.Path + "/" + fld.Key,
										Value: envCloneValue(fld.Value),
									})
									expanded = true
								}
							}
							if expanded {
								continue
							}
						}
					}
					mutated = append(mutated, op)
				}
				ops = mutated
			}
			res.OK = true
			res.Ops = ops
			return res, nil
		}

	case "toRecord":
		var doc lkal.Record
		doc, err = patch.ToRecord(tc.Ops)
		if err == nil {
			if hooks.toRecordStepOne && len(doc.Fields) == 1 {
				if setRec, ok := doc.Fields[0].Value.(lkal.Record); ok {
					for i, fld := range setRec.Fields {
						r := fld.Value.(lkal.Record)
						setRec.Fields[i].Value = envSetField(r, orderedset.OrderField, lkal.Int(int64(i+1)))
					}
					doc.Fields[0].Value = setRec
				}
			}
			res.OK = true
			res.Doc = doc
			return res, nil
		}

	case "fromRecord":
		var ops []patch.Op
		ops, err = patch.FromRecord(tc.Doc)
		if err == nil {
			if hooks.fromRecordUnsortedOrder && len(ops) >= 2 {
				if setRec, ok := envGetRecord(tc.Doc, patch.PatchField); ok {
					rawOps := make([]patch.Op, 0, len(setRec.Fields))
					for _, fld := range setRec.Fields {
						single := envRec(envField(patch.PatchField, envRec(fld)))
						sub, subErr := patch.FromRecord(single)
						if subErr == nil && len(sub) == 1 {
							rawOps = append(rawOps, sub[0])
						}
					}
					if len(rawOps) == len(ops) {
						ops = rawOps
					}
				}
			}
			res.OK = true
			res.Ops = ops
			return res, nil
		}

	case "applyEntrance":
		if hooks.applyEntranceIgnoreMetaRev && envelope.CheckStored(tc.Old) == nil && tc.Rev >= 1 {
			if _, opsErr := patch.ToRecord(tc.Ops); opsErr == nil {
				for _, op := range tc.Ops {
					if op.Kind == patch.OpTest && op.Path == "meta/revision" {
						res.Err = "conflict"
						return res, nil
					}
				}
			}
		}
		var doc lkal.Record
		doc, err = patch.ApplyEntrance(tc.Entrance, tc.Old, tc.Rev, tc.Ops)
		if err != nil && errors.Is(err, patch.ErrForbidden) {
			bypass := false
			for _, op := range tc.Ops {
				if op.Kind == patch.OpTest {
					continue
				}
				segs, ok := patch.ParsePath(op.Path)
				if !ok || len(segs) == 0 {
					continue
				}
				if hooks.applyEntranceSpecAllowStatus && tc.Entrance == patch.EntranceSpec && segs[0] == "status" {
					bypass = true
				}
				if hooks.applyEntranceSpecAllowUID && tc.Entrance == patch.EntranceSpec && len(segs) >= 2 && segs[0] == "meta" && segs[1] == "uid" {
					bypass = true
				}
				if hooks.applyEntranceStatusAllowSince && tc.Entrance == patch.EntranceStatus &&
					len(segs) >= 4 && segs[0] == "status" && segs[1] == "condition" && segs[3] == "since" {
					bypass = true
				}
			}
			if bypass {
				doc, err = patch.Apply(envelope.WithRevision(tc.Old, tc.Rev), tc.Ops)
			}
		}
		if err == nil {
			res.OK = true
			res.Doc = doc
			return res, nil
		}

	case "patch":
		var doc lkal.Record
		doc, err = patch.Patch(tc.Old, tc.Rev, tc.Ops)
		if err == nil {
			if hooks.patchAlwaysBumpGen {
				oldMeta, _ := envGetRecord(tc.Old, "meta")
				oldGen, _ := envGetField(oldMeta, "generation")
				meta, _ := envGetRecord(doc, "meta")
				meta = envSetField(meta, "generation", lkal.Int(int64(oldGen.(lkal.Int))+1))
				doc = envSetField(doc, "meta", meta)
			}
			res.OK = true
			res.Doc = doc
			return res, nil
		}

	case "patchStatus":
		if hooks.patchStatusCheckOpsBeforeNow && envelope.CheckStored(tc.Old) == nil && tc.Rev >= 1 {
			if _, opsErr := patch.ToRecord(tc.Ops); opsErr != nil {
				res.Err = "malformed"
				return res, nil
			}
		}
		var doc lkal.Record
		doc, err = patch.PatchStatus(tc.Old, tc.Rev, tc.Ops, tc.Now)
		if err != nil && hooks.patchStatusAllowObsRegress && errors.Is(err, patch.ErrObserved) {
			if oldSt, hasOldSt := envGetRecord(tc.Old, "status"); hasOldSt {
				if _, hasOldObs := envGetField(oldSt, "observedGeneration"); hasOldObs {
					loweredOld := envSetField(tc.Old, "status", envSetField(oldSt, "observedGeneration", lkal.Int(1)))
					if altDoc, altErr := patch.PatchStatus(loweredOld, tc.Rev, tc.Ops, tc.Now); altErr == nil {
						doc, err = altDoc, nil
					}
				}
			}
		}
		if err == nil {
			if hooks.patchStatusAlwaysUpdateSince {
				if st, hasSt := envGetRecord(doc, "status"); hasSt {
					if cond, hasCond := envGetRecord(st, "condition"); hasCond {
						for i, cf := range cond.Fields {
							cr := cf.Value.(lkal.Record)
							cond.Fields[i].Value = envSetField(cr, "since", lkal.String(tc.Now))
						}
						doc = envSetField(doc, "status", envSetField(st, "condition", cond))
					}
				}
			}
			res.OK = true
			res.Doc = doc
			return res, nil
		}

	default:
		return patchResult{}, fmt.Errorf("unknown patch op %q", tc.Op)
	}

	errName, mapErr := patchErrName(err)
	if mapErr != nil {
		return patchResult{}, mapErr
	}
	res.Err = errName
	return res, nil
}

func decodePatchOps(raw json.RawMessage) ([]patch.Op, error) {
	var arr []json.RawMessage
	if err := decodeStrictJSON(raw, &arr); err != nil {
		return nil, err
	}
	out := make([]patch.Op, len(arr))
	for i, elem := range arr {
		var obj map[string]json.RawMessage
		if err := decodeStrictJSON(elem, &obj); err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		rawKind, hasKind := obj["kind"]
		rawPath, hasPath := obj["path"]
		if !hasKind || !hasPath {
			return nil, fmt.Errorf("[%d]: patch op missing kind or path: %s", i, elem)
		}
		kindStr, err := decodeString(rawKind)
		if err != nil {
			return nil, fmt.Errorf("[%d].kind: %w", i, err)
		}
		var kind patch.OpKind
		switch kindStr {
		case "test":
			kind = patch.OpTest
		case "set":
			kind = patch.OpSet
		case "del":
			kind = patch.OpDel
		default:
			return nil, fmt.Errorf("[%d].kind: unknown op kind %q", i, kindStr)
		}
		pathStr, err := decodeString(rawPath)
		if err != nil {
			return nil, fmt.Errorf("[%d].path: %w", i, err)
		}
		rawVal, hasVal := obj["value"]
		switch kind {
		case patch.OpDel:
			if hasVal || len(obj) != 2 {
				return nil, fmt.Errorf("[%d]: del op must have exact keys kind, path: %s", i, elem)
			}
			out[i] = patch.Op{Kind: kind, Path: pathStr}
		case patch.OpTest, patch.OpSet:
			if !hasVal || len(obj) != 3 {
				return nil, fmt.Errorf("[%d]: %s op must have exact keys kind, path, value: %s", i, kindStr, elem)
			}
			val, err := decodeWireValue(rawVal)
			if err != nil {
				return nil, fmt.Errorf("[%d].value: %w", i, err)
			}
			out[i] = patch.Op{Kind: kind, Path: pathStr, Value: val}
		}
	}
	return out, nil
}

func decodePatchResult(op string, line []byte) (patchResult, error) {
	trimmed := bytes.TrimSpace(line)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rawTop json.RawMessage
	if err := dec.Decode(&rawTop); err != nil {
		return patchResult{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return patchResult{}, fmt.Errorf("unexpected trailing data in result line: %v", err)
	}
	var top map[string]json.RawMessage
	if err := decodeStrictJSON(rawTop, &top); err != nil {
		return patchResult{}, err
	}
	rawID, hasID := top["id"]
	rawOK, hasOK := top["ok"]
	if !hasID || !hasOK {
		return patchResult{}, fmt.Errorf("result object missing id or ok: %s", rawTop)
	}
	id, err := decodeNonNegInt(rawID)
	if err != nil {
		return patchResult{}, fmt.Errorf("invalid id: %w", err)
	}
	var ok bool
	if err := decodeStrictJSON(rawOK, &ok); err != nil {
		return patchResult{}, fmt.Errorf("ok: %w", err)
	}
	if !ok {
		rawErr, hasErr := top["err"]
		if len(top) != 3 || !hasErr {
			return patchResult{}, fmt.Errorf("ok=false result must have exact keys id, ok, err: %s", rawTop)
		}
		errName, err := decodeString(rawErr)
		if err != nil {
			return patchResult{}, fmt.Errorf("err: %w", err)
		}
		if !slices.Contains(patchErrClasses, errName) {
			return patchResult{}, fmt.Errorf("unknown error class %q", errName)
		}
		return patchResult{ID: id, Op: op, OK: false, Err: errName}, nil
	}

	res := patchResult{ID: id, Op: op, OK: true}
	switch op {
	case "apply", "toRecord", "applyEntrance", "patch", "patchStatus":
		rawDoc, hasDoc := top["doc"]
		if len(top) != 3 || !hasDoc {
			return patchResult{}, fmt.Errorf("%s ok=true must have exact keys id, ok, doc: %s", op, rawTop)
		}
		doc, err := decodeWireRecord(rawDoc)
		if err != nil {
			return patchResult{}, fmt.Errorf("doc: %w", err)
		}
		res.Doc = doc
	case "diff", "fromRecord":
		rawOps, hasOps := top["ops"]
		if len(top) != 3 || !hasOps {
			return patchResult{}, fmt.Errorf("%s ok=true must have exact keys id, ok, ops: %s", op, rawTop)
		}
		ops, err := decodePatchOps(rawOps)
		if err != nil {
			return patchResult{}, fmt.Errorf("ops: %w", err)
		}
		res.Ops = ops
	default:
		return patchResult{}, fmt.Errorf("unknown op %q", op)
	}
	return res, nil
}

func formatWireValue(v lkal.Value) string {
	if v == nil {
		return "<nil>"
	}
	b, err := json.Marshal(wireValue{v})
	if err != nil {
		return fmt.Sprintf("<marshal error: %v>", err)
	}
	return string(b)
}

func comparePatchResult(want, got patchResult) string {
	switch {
	case want.ID != got.ID:
		return fmt.Sprintf("id mismatch: lean=%d go=%d", want.ID, got.ID)
	case want.Op != got.Op:
		return fmt.Sprintf("op mismatch: lean=%q go=%q", want.Op, got.Op)
	case want.OK != got.OK:
		return fmt.Sprintf("ok mismatch: lean=%v (err=%q) go=%v (err=%q)", want.OK, want.Err, got.OK, got.Err)
	case want.Err != got.Err:
		return fmt.Sprintf("err mismatch: lean=%q go=%q", want.Err, got.Err)
	}
	if !want.OK {
		return ""
	}
	switch want.Op {
	case "apply", "toRecord", "applyEntrance", "patch", "patchStatus":
		if !lkal.Equal(want.Doc, got.Doc) {
			return fmt.Sprintf("doc mismatch:\nlean: %s\ngo:   %s", formatWireDoc(want.Doc), formatWireDoc(got.Doc))
		}
	case "diff", "fromRecord":
		if len(want.Ops) != len(got.Ops) {
			return fmt.Sprintf("ops len mismatch: lean=%d go=%d", len(want.Ops), len(got.Ops))
		}
		for i := range want.Ops {
			w, g := want.Ops[i], got.Ops[i]
			if w.Kind != g.Kind {
				return fmt.Sprintf("ops[%d].kind mismatch: lean=%s go=%s", i, w.Kind, g.Kind)
			}
			if w.Path != g.Path {
				return fmt.Sprintf("ops[%d].path mismatch: lean=%q go=%q", i, w.Path, g.Path)
			}
			if (w.Value == nil) != (g.Value == nil) {
				return fmt.Sprintf("ops[%d].value presence mismatch: lean=%s go=%s", i, formatWireValue(w.Value), formatWireValue(g.Value))
			}
			if w.Value != nil && !lkal.Equal(w.Value, g.Value) {
				return fmt.Sprintf("ops[%d].value mismatch: lean=%s go=%s", i, formatWireValue(w.Value), formatWireValue(g.Value))
			}
		}
	}
	return ""
}

func leanPatchResults(t *testing.T, cases []patchCase) ([][]byte, [][]byte, []patchResult) {
	t.Helper()
	encoded := encodePatchCases(t, cases)
	lines, err := RunModel("patch", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(lines), len(cases))
	}
	results := make([]patchResult, len(lines))
	for i, line := range lines {
		res, err := decodePatchResult(cases[i].Op, line)
		if err != nil {
			t.Fatalf("id=%d (%s) decode error: %v\ncase: %s\nlean: %s", cases[i].ID, cases[i].Op, err, encoded[i], line)
		}
		results[i] = res
	}
	return encoded, lines, results
}

func formatPatchBuckets(buckets map[string]int) string {
	parts := make([]string, 0, len(patchRequiredBuckets))
	for _, b := range patchRequiredBuckets {
		parts = append(parts, fmt.Sprintf("%s=%d", b, buckets[b]))
	}
	return strings.Join(parts, " ")
}

func TestPatchMatchesLeanModel(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generatePatchCases(cfg.Seed, cfg.Cases)
	encoded, leanLines, leanResults := leanPatchResults(t, cases)

	repro := fmt.Sprintf("NAYUTAR_DRT_SEED=%d NAYUTAR_DRT_CASES=%d go test -tags drt -run '^TestPatchMatchesLeanModel$' ./core/drt/...", cfg.Seed, cfg.Cases)
	buckets := map[string]int{}
	for i, tc := range cases {
		goRes, err := runPatchCase(tc, patchHooks{})
		if err != nil {
			t.Fatalf("seed=%d id=%d (%s) go error: %v\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, tc.Op, err, encoded[i], leanLines[i], repro)
		}
		if diff := comparePatchResult(leanResults[i], goRes); diff != "" {
			t.Fatalf("seed=%d id=%d (%s) mismatch (%s)\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, tc.Op, diff, encoded[i], leanLines[i], repro)
		}
		if goRes.OK {
			buckets[tc.Op+"/ok"]++
		} else {
			buckets[tc.Op+"/"+goRes.Err]++
		}
	}
	t.Logf("compared %d cases (seed=%d): %s", len(cases), cfg.Seed, formatPatchBuckets(buckets))

	if cfg.Cases >= 100 {
		var missing []string
		for _, b := range patchRequiredBuckets {
			if buckets[b] == 0 {
				missing = append(missing, b)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed=%d empty outcome buckets: %s", cfg.Seed, strings.Join(missing, ", "))
		}
	}
}

func TestPatchCasesReachEveryOutcome(t *testing.T) {
	cases := generatePatchCases(1, 1000)
	_, _, results := leanPatchResults(t, cases)

	seen := map[string]int{}
	for i, tc := range cases {
		res := results[i]
		if res.OK {
			seen[tc.Op+"/ok"]++
			switch tc.Op {
			case "apply":
				if onlyRec, ok := envGetRecord(res.Doc, "only"); ok && len(onlyRec.Fields) == 0 {
					seen["apply/ok-empty-parent"]++
				}
			case "diff":
				if len(res.Ops) == 0 {
					seen["diff/ok-empty"]++
				} else {
					seen["diff/ok-nonempty"]++
				}
			case "applyEntrance":
				if tc.Entrance == patch.EntranceSpec {
					seen["applyEntrance/ok-spec"]++
				} else {
					seen["applyEntrance/ok-status"]++
				}
			case "patch":
				oldMeta, _ := envGetRecord(tc.Old, "meta")
				oldGen, _ := envGetField(oldMeta, "generation")
				newMeta, _ := envGetRecord(res.Doc, "meta")
				newGen, _ := envGetField(newMeta, "generation")
				if newGen == oldGen {
					seen["patch/ok-gen-kept"]++
				} else {
					seen["patch/ok-gen-bumped"]++
				}
			case "patchStatus":
				oldSt, _ := envGetRecord(tc.Old, "status")
				oldCond, _ := envGetRecord(oldSt, "condition")
				if newSt, hasNewSt := envGetRecord(res.Doc, "status"); hasNewSt {
					newCond, _ := envGetRecord(newSt, "condition")
					for _, cf := range newCond.Fields {
						newC := cf.Value.(lkal.Record)
						newSince, _ := envGetField(newC, "since")
						if oldC, ok := envGetRecord(oldCond, cf.Key); ok {
							oldSince, _ := envGetField(oldC, "since")
							if newSince == oldSince {
								seen["patchStatus/ok-since-kept"]++
							} else {
								seen["patchStatus/ok-since-updated"]++
							}
						} else {
							seen["patchStatus/ok-since-new-cond"]++
						}
					}
				}
			}
		} else {
			seen[tc.Op+"/"+res.Err]++
		}
	}

	expected := append(slices.Clone(patchRequiredBuckets),
		"apply/ok-empty-parent",
		"diff/ok-empty",
		"diff/ok-nonempty",
		"applyEntrance/ok-spec",
		"applyEntrance/ok-status",
		"patch/ok-gen-kept",
		"patch/ok-gen-bumped",
		"patchStatus/ok-since-kept",
		"patchStatus/ok-since-updated",
		"patchStatus/ok-since-new-cond",
	)
	var missing []string
	for _, outcome := range expected {
		if seen[outcome] == 0 {
			missing = append(missing, outcome)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("missing outcomes: %s", strings.Join(missing, ", "))
	}
}

func clonePatchResult(r patchResult) patchResult {
	out := patchResult{
		ID:  r.ID,
		Op:  r.Op,
		OK:  r.OK,
		Err: r.Err,
		Doc: envCloneRecord(r.Doc),
	}
	if r.Ops != nil {
		out.Ops = make([]patch.Op, len(r.Ops))
		for i, op := range r.Ops {
			var v lkal.Value
			if op.Value != nil {
				v = envCloneValue(op.Value)
			}
			out.Ops[i] = patch.Op{Kind: op.Kind, Path: op.Path, Value: v}
		}
	}
	return out
}

func TestPatchComparisonDetectsEveryFieldMutation(t *testing.T) {
	cases := generatePatchCases(1, 350)
	_, leanLines, results := leanPatchResults(t, cases)

	applied := map[string]int{}
	assertDetects := func(kind string, orig patchResult, mutate func(r *patchResult)) {
		t.Helper()
		mut := clonePatchResult(orig)
		mutate(&mut)
		applied[kind]++
		if comparePatchResult(orig, mut) == "" {
			t.Fatalf("mutation %q on case %d (%s) was not detected", kind, orig.ID, orig.Op)
		}
	}

	for _, orig := range results {
		assertDetects("id+1", orig, func(r *patchResult) { r.ID++ })
		assertDetects("op-changed", orig, func(r *patchResult) { r.Op += "x" })
		assertDetects("flip-ok", orig, func(r *patchResult) { r.OK = !r.OK })
		if orig.OK {
			assertDetects("err-set", orig, func(r *patchResult) { r.Err = "malformed" })
			switch orig.Op {
			case "apply", "toRecord", "applyEntrance", "patch", "patchStatus":
				assertDetects("doc-changed", orig, func(r *patchResult) {
					r.Doc = envSetField(r.Doc, "mutated", lkal.Bool(true))
				})
			case "diff", "fromRecord":
				assertDetects("ops-len", orig, func(r *patchResult) {
					r.Ops = append(r.Ops, patch.Op{Kind: patch.OpDel, Path: "extra"})
				})
				if len(orig.Ops) > 0 {
					assertDetects("ops-kind", orig, func(r *patchResult) {
						if r.Ops[0].Kind == patch.OpDel {
							r.Ops[0].Kind = patch.OpSet
						} else {
							r.Ops[0].Kind = patch.OpDel
						}
					})
					assertDetects("ops-path", orig, func(r *patchResult) {
						r.Ops[0].Path += "/x"
					})
					if orig.Ops[0].Value != nil {
						assertDetects("ops-value-changed", orig, func(r *patchResult) {
							r.Ops[0].Value = lkal.String("__mutated__")
						})
						assertDetects("ops-value-cleared", orig, func(r *patchResult) {
							r.Ops[0].Value = nil
						})
					} else {
						assertDetects("ops-value-added", orig, func(r *patchResult) {
							r.Ops[0].Value = lkal.Int(1)
						})
					}
				}
			}
		} else {
			assertDetects("err-cleared", orig, func(r *patchResult) { r.Err = "" })
			assertDetects("err-changed", orig, func(r *patchResult) { r.Err += "x" })
		}
	}

	for _, kind := range []string{
		"id+1", "op-changed", "flip-ok", "err-set", "err-cleared", "err-changed",
		"doc-changed", "ops-len", "ops-kind", "ops-path",
		"ops-value-changed", "ops-value-cleared", "ops-value-added",
	} {
		if applied[kind] == 0 {
			t.Errorf("mutation %q was never applicable across %d cases", kind, len(cases))
		}
	}

	var firstOKLine []byte
	var firstOKOp string
	for i, line := range leanLines {
		if bytes.Contains(line, []byte(`"ok":true`)) {
			firstOKLine = line
			firstOKOp = cases[i].Op
			break
		}
	}
	if firstOKLine == nil {
		t.Fatal("no ok=true line in sample results")
	}
	var raw map[string]any
	if err := json.Unmarshal(firstOKLine, &raw); err != nil {
		t.Fatal(err)
	}
	raw["extra"] = 1
	withTopExtra, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}

	invalidLines := []struct {
		name string
		op   string
		line string
	}{
		{"unknown top-level field", firstOKOp, string(withTopExtra)},
		{"missing id", "apply", `{"ok":false,"err":"malformed"}`},
		{"missing ok", "apply", `{"id":0,"err":"malformed"}`},
		{"negative id", "apply", `{"id":-1,"ok":false,"err":"malformed"}`},
		{"ok=false unknown err", "apply", `{"id":0,"ok":false,"err":"boom"}`},
		{"ok=false missing err", "apply", `{"id":0,"ok":false}`},
		{"ok=false extra payload", "apply", `{"id":0,"ok":false,"err":"malformed","doc":{"o":[]}}`},
		{"apply wrong key", "apply", `{"id":0,"ok":true,"ops":[]}`},
		{"apply non-record doc", "apply", `{"id":0,"ok":true,"doc":{"s":"x"}}`},
		{"apply null doc", "apply", `{"id":0,"ok":true,"doc":null}`},
		{"diff wrong key", "diff", `{"id":0,"ok":true,"doc":{"o":[]}}`},
		{"diff null ops", "diff", `{"id":0,"ok":true,"ops":null}`},
		{"diff op missing path", "diff", `{"id":0,"ok":true,"ops":[{"kind":"del"}]}`},
		{"diff op unknown kind", "diff", `{"id":0,"ok":true,"ops":[{"kind":"noop","path":"a"}]}`},
		{"diff del op with value", "diff", `{"id":0,"ok":true,"ops":[{"kind":"del","path":"a","value":{"i":"1"}}]}`},
		{"diff set op missing value", "diff", `{"id":0,"ok":true,"ops":[{"kind":"set","path":"a"}]}`},
		{"diff op extra field", "diff", `{"id":0,"ok":true,"ops":[{"kind":"del","path":"a","x":1}]}`},
		{"diff op bad value", "diff", `{"id":0,"ok":true,"ops":[{"kind":"set","path":"a","value":{"x":"bad"}}]}`},
		{"fromRecord bad ops", "fromRecord", `{"id":0,"ok":true,"ops":[{"kind":"test","path":"a"}]}`},
		{"trailing object", "apply", `{"id":0,"ok":true,"doc":{"o":[]}}{"id":1,"ok":true,"doc":{"o":[]}}`},
	}
	for _, tc := range invalidLines {
		if _, err := decodePatchResult(tc.op, []byte(tc.line)); err == nil {
			t.Errorf("decodePatchResult(%s) accepted %s: %s", tc.op, tc.name, tc.line)
		}
	}

	validDocLine := `{"id":3,"ok":true,"doc":{"o":[["a",{"i":"1"}]]}}`
	gotDoc, err := decodePatchResult("apply", []byte(validDocLine))
	if err != nil {
		t.Fatalf("decodePatchResult(%s): %v", validDocLine, err)
	}
	if diff := comparePatchResult(patchResult{ID: 3, Op: "apply", OK: true, Doc: envRec(envField("a", lkal.Int(1)))}, gotDoc); diff != "" {
		t.Fatalf("decodePatchResult(%s) mismatch: %s", validDocLine, diff)
	}

	validOpsLine := `{"id":4,"ok":true,"ops":[{"kind":"del","path":"a"},{"kind":"set","path":"b","value":{"d":"1.0"}}]}`
	gotOps, err := decodePatchResult("diff", []byte(validOpsLine))
	if err != nil {
		t.Fatalf("decodePatchResult(%s): %v", validOpsLine, err)
	}
	wantOps := patchResult{
		ID: 4,
		Op: "diff",
		OK: true,
		Ops: []patch.Op{
			{Kind: patch.OpDel, Path: "a"},
			{Kind: patch.OpSet, Path: "b", Value: lkal.Decimal("1.0")},
		},
	}
	if diff := comparePatchResult(wantOps, gotOps); diff != "" {
		t.Fatalf("decodePatchResult(%s) mismatch: %s", validOpsLine, diff)
	}
}

func TestPatchDRTCatchesBrokenImplementations(t *testing.T) {
	cases := generatePatchCases(1, 870)
	_, _, leanResults := leanPatchResults(t, cases)

	variants := []struct {
		name  string
		hooks patchHooks
	}{
		{"apply del prunes empty parent", patchHooks{applyDelPruneEmptyParent: true}},
		{"apply del ignores missing path", patchHooks{applyDelIgnoreMissing: true}},
		{"apply test treats int equal to decimal", patchHooks{applyTestIntEqDecimal: true}},
		{"apply skips opsOk upfront check", patchHooks{applySkipOpsOkUpfront: true}},
		{"diff unsorted keys", patchHooks{diffUnsortedKeys: true}},
		{"diff emits sets before dels", patchHooks{diffSetsBeforeDels: true}},
		{"diff recurses into invalid sub-segments", patchHooks{diffRecurseInvalidSubSeg: true}},
		{"toRecord uses step 1", patchHooks{toRecordStepOne: true}},
		{"fromRecord ignores order field", patchHooks{fromRecordUnsortedOrder: true}},
		{"applyEntrance ignores meta/revision", patchHooks{applyEntranceIgnoreMetaRev: true}},
		{"applyEntrance spec allows status edit", patchHooks{applyEntranceSpecAllowStatus: true}},
		{"applyEntrance spec allows meta/uid edit", patchHooks{applyEntranceSpecAllowUID: true}},
		{"applyEntrance status allows since edit", patchHooks{applyEntranceStatusAllowSince: true}},
		{"patch always bumps generation", patchHooks{patchAlwaysBumpGen: true}},
		{"patchStatus checks ops before timestamp", patchHooks{patchStatusCheckOpsBeforeNow: true}},
		{"patchStatus always updates since", patchHooks{patchStatusAlwaysUpdateSince: true}},
		{"patchStatus allows observedGeneration regression", patchHooks{patchStatusAllowObsRegress: true}},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			mismatches := 0
			for i, tc := range cases {
				goRes, err := runPatchCase(tc, v.hooks)
				if err != nil {
					t.Fatalf("id=%d (%s): broken implementation failed instead of answering wrongly: %v", tc.ID, tc.Op, err)
				}
				if comparePatchResult(leanResults[i], goRes) != "" {
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

func TestGeneratePatchCasesIsDeterministic(t *testing.T) {
	a := bytes.Join(encodePatchCases(t, generatePatchCases(1, 100)), []byte("\n"))
	b := bytes.Join(encodePatchCases(t, generatePatchCases(1, 100)), []byte("\n"))
	c := bytes.Join(encodePatchCases(t, generatePatchCases(2, 100)), []byte("\n"))

	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different encoded cases")
	}
	if bytes.Equal(a, c) {
		t.Fatal("different seeds produced identical encoded cases")
	}
}

func TestPatchModelRejectsMalformedCase(t *testing.T) {
	valid := `{"id":0,"op":"apply","doc":{"o":[]},"ops":[]}`
	for _, bad := range []string{
		`{"id":1,"op":"explode"}`,
		`{"id":1,"op":"apply","doc":{"s":"not-a-record"},"ops":[]}`,
		`{"id":1,"op":"apply","doc":{"o":[]},"ops":[{"kind":"noop","path":"a"}]}`,
		`{"id":1,"op":"diff","old":{"o":[]}}`,
		`{"id":1,"op":"toRecord","ops":"bad"}`,
		`{"id":1,"op":"fromRecord","doc":{"i":"1"}}`,
		`{"id":1,"op":"applyEntrance","entrance":"unknown","old":{"o":[]},"rev":"1","ops":[]}`,
		`{"id":1,"op":"patch","old":{"o":[]},"rev":1,"ops":[]}`,
		`{"id":1,"op":"patchStatus","old":{"o":[]},"rev":"1","ops":[]}`,
	} {
		cases := [][]byte{[]byte(valid), []byte(bad)}
		_, err := RunModel("patch", cases)
		if err == nil {
			t.Errorf("RunModel accepted malformed case %s", bad)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte("line 2")) {
			t.Errorf("error %q does not name line 2 for %s", err.Error(), bad)
		}
	}
}
