//go:build drt

package drt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
)

type wireValue struct {
	v lkal.Value
}

func (w wireValue) MarshalJSON() ([]byte, error) {
	switch val := w.v.(type) {
	case lkal.String:
		return json.Marshal(struct {
			S string `json:"s"`
		}{string(val)})
	case lkal.Int:
		return json.Marshal(struct {
			I string `json:"i"`
		}{strconv.FormatInt(int64(val), 10)})
	case lkal.Decimal:
		return json.Marshal(struct {
			D string `json:"d"`
		}{string(val)})
	case lkal.Bool:
		return json.Marshal(struct {
			B bool `json:"b"`
		}{bool(val)})
	case lkal.Enum:
		return json.Marshal(struct {
			E string `json:"e"`
		}{string(val)})
	case lkal.Quantity:
		return json.Marshal(struct {
			Q [2]string `json:"q"`
		}{[2]string{val.Number, val.Unit}})
	case lkal.Ref:
		segs := []string(val)
		if segs == nil {
			segs = []string{}
		}
		return json.Marshal(struct {
			R []string `json:"r"`
		}{segs})
	case lkal.List:
		items := make([]wireValue, len(val))
		for i, item := range val {
			items[i] = wireValue{item}
		}
		return json.Marshal(struct {
			L []wireValue `json:"l"`
		}{items})
	case lkal.Record:
		pairs := make([][2]any, len(val.Fields))
		for i, f := range val.Fields {
			pairs[i] = [2]any{f.Key, wireValue{f.Value}}
		}
		return json.Marshal(struct {
			O [][2]any `json:"o"`
		}{pairs})
	default:
		return nil, fmt.Errorf("unsupported lkal value type %T", w.v)
	}
}

type envelopeCase struct {
	ID   int
	Op   string
	Old  lkal.Record
	Rev  int64
	Cand lkal.Record
	UID  string
	Now  string
}

func (c envelopeCase) MarshalJSON() ([]byte, error) {
	switch c.Op {
	case "create":
		return json.Marshal(struct {
			ID   int       `json:"id"`
			Op   string    `json:"op"`
			Cand wireValue `json:"cand"`
			UID  string    `json:"uid"`
		}{c.ID, c.Op, wireValue{c.Cand}, c.UID})
	case "update":
		return json.Marshal(struct {
			ID   int       `json:"id"`
			Op   string    `json:"op"`
			Old  wireValue `json:"old"`
			Rev  string    `json:"rev"`
			Cand wireValue `json:"cand"`
		}{c.ID, c.Op, wireValue{c.Old}, strconv.FormatInt(c.Rev, 10), wireValue{c.Cand}})
	case "status":
		return json.Marshal(struct {
			ID   int       `json:"id"`
			Op   string    `json:"op"`
			Old  wireValue `json:"old"`
			Rev  string    `json:"rev"`
			Cand wireValue `json:"cand"`
			Now  string    `json:"now"`
		}{c.ID, c.Op, wireValue{c.Old}, strconv.FormatInt(c.Rev, 10), wireValue{c.Cand}, c.Now})
	default:
		return nil, fmt.Errorf("unknown op %q", c.Op)
	}
}

type envelopeResult struct {
	ID  int         `json:"id"`
	OK  bool        `json:"ok"`
	Doc lkal.Record `json:"doc"`
	Err string      `json:"err"`
}

var (
	envOps = []string{"create", "update", "status"}

	envGroups      = []string{"", "apps.example.com", "batch.example.com"}
	envKinds       = []string{"pod", "workload", "job", "service"}
	envNames       = []string{"web", "api.v1", "worker-0", "db"}
	envLabelKeys   = []string{"app", "env", "tier", "owner"}
	envLabelVals   = []string{"web", "prod", "dev", "v1", "café", ""}
	envCondTypes   = []string{"Ready", "Degraded", "Synced", "Progressing"}
	envCondStates  = []lkal.Enum{"true", "false", "unknown"}
	envReasons     = []string{"Ok", "AllReady:ok,v1_0", "Starting", "ImagePull_Err", "a", "a_"}
	envTimestamps  = []string{"2026-01-02T03:04:05Z", "2026-06-15T12:30:45Z", "2026-11-20T16:02:03Z", "0000-01-01T00:00:00Z", "9999-12-31T23:59:59Z"}
	envUIDs        = []string{"01234567-89ab-cdef-0123-456789abcdef", "fedcba98-7654-3210-fedc-ba9876543210", "00000000-0000-0000-0000-000000000000", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
	envBadUIDs     = []string{"", "bad-uid", "01234567-89ab-cdef-0123-456789abcde", "01234567-89ab-cdef-0123-456789abcdef0", "01234567-89AB-cdef-0123-456789abcdef", "01234567_89ab-cdef-0123-456789abcdef", "01234567-89ab-cdeg-0123-456789abcdef", "01234567-89ab-cdef-0123-456789abcdeé"}
	envBadTS       = []string{"", "bad", "2026-00-01T00:00:00Z", "2026-13-01T00:00:00Z", "2026-01-00T00:00:00Z", "2026-01-32T00:00:00Z", "2026-01-01T24:00:00Z", "2026-01-01T00:60:00Z", "2026-01-01T00:00:60Z", "2026-01-01t00:00:00Z", "2026-01-01T00:00:00z", "2026-01-01T00:00:00+00:00", "2026-01-01T00:00:00é"}
	envBadGroups   = []string{"core", "apps", "", "Apps.example.com", "a..b", ".a.b", "a.b.", "-a.b", "a.café"}
	envBadKinds    = []string{"", "Pod", "a.b", "-pod", "pod-", "a_b", strings.Repeat("a", 64), "pód"}
	envBadNames    = []string{"", "Web", "-web", "web-", "a..b", "a_b", strings.Repeat("a", 254), "wéb"}
	envBadCondType = []string{"", "ready", "1Ready", "Ready_1", "Ready-1", strings.Repeat("A", 64), "Réady"}
	envBadReasons  = []string{"", "1abc", "_abc", ":abc", ",abc", "abc:", "abc,", "abc-def", "abc def", strings.Repeat("a", 1025), "Oké"}

	envErrClasses = []string{"internal", "malformed", "conflict", "forbidden", "observed"}

	envRequiredBuckets = []string{
		"create/ok", "create/internal", "create/malformed", "create/forbidden",
		"update/ok", "update/internal", "update/malformed", "update/conflict", "update/forbidden",
		"status/ok", "status/internal", "status/malformed", "status/conflict", "status/forbidden", "status/observed",
	}
)

type envPoolEntry struct {
	doc lkal.Record
	rev int64
}

func envRec(fields ...lkal.Field) lkal.Record {
	return lkal.Record{Fields: fields}
}

func envField(k string, v lkal.Value) lkal.Field {
	return lkal.Field{Key: k, Value: v}
}

func envCloneValue(v lkal.Value) lkal.Value {
	switch val := v.(type) {
	case lkal.Record:
		return envCloneRecord(val)
	case lkal.List:
		out := make(lkal.List, len(val))
		for i, elem := range val {
			out[i] = envCloneValue(elem)
		}
		return out
	case lkal.Ref:
		return lkal.Ref(slices.Clone(val))
	default:
		return v
	}
}

func envCloneRecord(r lkal.Record) lkal.Record {
	fields := make([]lkal.Field, len(r.Fields))
	for i, f := range r.Fields {
		fields[i] = lkal.Field{Key: f.Key, Value: envCloneValue(f.Value)}
	}
	return lkal.Record{Fields: fields}
}

func envShuffleValue(rng *rand.Rand, v lkal.Value) lkal.Value {
	switch val := v.(type) {
	case lkal.Record:
		return envShuffleRecord(rng, val)
	case lkal.List:
		out := make(lkal.List, len(val))
		for i, elem := range val {
			out[i] = envShuffleValue(rng, elem)
		}
		return out
	case lkal.Ref:
		return lkal.Ref(slices.Clone(val))
	default:
		return v
	}
}

func envShuffleRecord(rng *rand.Rand, r lkal.Record) lkal.Record {
	fields := make([]lkal.Field, len(r.Fields))
	for i, f := range r.Fields {
		fields[i] = envField(f.Key, envShuffleValue(rng, f.Value))
	}
	rng.Shuffle(len(fields), func(i, j int) {
		fields[i], fields[j] = fields[j], fields[i]
	})
	return lkal.Record{Fields: fields}
}

func envGetField(r lkal.Record, key string) (lkal.Value, bool) {
	for _, f := range r.Fields {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

func envGetRecord(r lkal.Record, key string) (lkal.Record, bool) {
	v, ok := envGetField(r, key)
	if !ok {
		return lkal.Record{}, false
	}
	rec, ok := v.(lkal.Record)
	return rec, ok
}

func envSetField(r lkal.Record, key string, val lkal.Value) lkal.Record {
	out := envCloneRecord(r)
	for i, f := range out.Fields {
		if f.Key == key {
			out.Fields[i].Value = envCloneValue(val)
			return out
		}
	}
	out.Fields = append(out.Fields, envField(key, envCloneValue(val)))
	return out
}

func envDropField(r lkal.Record, key string) lkal.Record {
	out := make([]lkal.Field, 0, len(r.Fields))
	for _, f := range r.Fields {
		if f.Key == key {
			continue
		}
		out = append(out, envField(f.Key, envCloneValue(f.Value)))
	}
	return lkal.Record{Fields: out}
}

func genEnvLabels(rng *rand.Rand) lkal.Record {
	if chance(rng, 20) {
		return envRec()
	}
	keys := slices.Clone(envLabelKeys)
	rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	count := 1 + rng.IntN(3)
	fields := make([]lkal.Field, count)
	for i := range count {
		fields[i] = envField(keys[i], lkal.String(pick(rng, envLabelVals)))
	}
	return lkal.Record{Fields: fields}
}

func genEnvLeafValue(rng *rand.Rand) lkal.Value {
	switch rng.IntN(7) {
	case 0:
		return lkal.String(pick(rng, []string{"fast", "", "café", "日本語", "web"}))
	case 1:
		return lkal.Int(pick(rng, []int64{-12, 0, 1, 3, 42}))
	case 2:
		return lkal.Decimal(pick(rng, []string{"0.25", "-1.5", "0.0", "3.14159"}))
	case 3:
		return lkal.Bool(rng.IntN(2) == 0)
	case 4:
		return lkal.Enum(pick(rng, []string{"tcp", "udp", "fast"}))
	case 5:
		return lkal.Quantity{
			Number: pick(rng, []string{"512", "100", "2.5"}),
			Unit:   pick(rng, []string{"Mi", "m", "Gi", ""}),
		}
	default:
		if chance(rng, 20) {
			return lkal.Ref{}
		}
		return lkal.Ref{pick(rng, []string{"secret", "config", "pod"}), pick(rng, envNames)}
	}
}

// genEnvSpec builds small spec trees covering all nine LKAL value types.
func genEnvSpec(rng *rand.Rand) lkal.Record {
	if chance(rng, 35) {
		// Small preset pool so repeated spec equalities happen organically.
		switch rng.IntN(3) {
		case 0:
			return envRec(
				envField("replicas", lkal.Int(3)),
				envField("mode", lkal.Enum("tcp")),
				envField("mem", lkal.Quantity{Number: "512", Unit: "Mi"}),
			)
		case 1:
			return envRec(
				envField("ratio", lkal.Decimal("0.25")),
				envField("enabled", lkal.Bool(true)),
				envField("target", lkal.Ref{"secret", "db"}),
				envField("tags", lkal.List{lkal.String("café"), lkal.Int(-12)}),
			)
		default:
			return envRec(
				envField("nested", envRec(
					envField("items", lkal.List{
						envRec(envField("name", lkal.String("a")), envField("ok", lkal.Bool(false))),
					}),
				)),
			)
		}
	}
	specKeys := []string{"replicas", "mode", "ratio", "enabled", "mem", "target", "items", "nested", "extra"}
	rng.Shuffle(len(specKeys), func(i, j int) { specKeys[i], specKeys[j] = specKeys[j], specKeys[i] })
	count := rng.IntN(5)
	fields := make([]lkal.Field, count)
	for i := range count {
		var val lkal.Value
		switch rng.IntN(10) {
		case 0, 1:
			val = genEnvLeafValue(rng)
		case 2:
			val = lkal.Quantity{Number: "512", Unit: "Mi"}
		case 3:
			val = lkal.Ref{"secret", "db"}
		case 4:
			val = lkal.Decimal("0.25")
		case 5:
			val = lkal.Enum("tcp")
		case 6, 7:
			n := rng.IntN(3)
			items := make(lkal.List, n)
			for j := range n {
				if chance(rng, 45) {
					items[j] = envRec(
						envField("k", genEnvLeafValue(rng)),
						envField("n", lkal.Int(int64(j))),
					)
				} else {
					items[j] = genEnvLeafValue(rng)
				}
			}
			val = items
		default:
			if chance(rng, 25) {
				val = envRec()
			} else {
				val = envRec(
					envField("a", genEnvLeafValue(rng)),
					envField("b", lkal.List{genEnvLeafValue(rng)}),
				)
			}
		}
		fields[i] = envField(specKeys[i], val)
	}
	return lkal.Record{Fields: fields}
}

func genEnvValidCreateCand(rng *rand.Rand) lkal.Record {
	metaFields := []lkal.Field{
		envField("kind", lkal.String(pick(rng, envKinds))),
		envField("name", lkal.String(pick(rng, envNames))),
	}
	if g := pick(rng, envGroups); g != "" {
		metaFields = append(metaFields, envField("group", lkal.String(g)))
	}
	if chance(rng, 55) {
		metaFields = append(metaFields, envField("labels", genEnvLabels(rng)))
	}
	fields := []lkal.Field{envField("meta", lkal.Record{Fields: metaFields})}
	if chance(rng, 70) {
		fields = append(fields, envField("spec", genEnvSpec(rng)))
	}
	doc := lkal.Record{Fields: fields}
	if chance(rng, 60) {
		return envShuffleRecord(rng, doc)
	}
	return doc
}

func buildStoredDoc(rng *rand.Rand, minGen int64, needObs bool) (lkal.Record, int64) {
	cand := genEnvValidCreateCand(rng)
	uid := pick(rng, envUIDs)
	doc, err := envelope.Create(cand, uid)
	if err != nil {
		panic(fmt.Sprintf("Create failed on valid candidate: %v", err))
	}
	rev := int64(1)
	gen := int64(1)
	steps := rng.IntN(4)
	for gen < minGen || steps > 0 {
		steps--
		if gen < minGen || chance(rng, 45) {
			updCand := envCloneRecord(doc)
			meta, _ := envGetRecord(updCand, "meta")
			meta = envDropField(meta, "uid")
			meta = envDropField(meta, "generation")
			if chance(rng, 50) {
				meta = envSetField(meta, "labels", genEnvLabels(rng))
			}
			updCand = envSetField(updCand, "meta", meta)
			// Force a distinct spec when bumping generation toward minGen.
			newSpec := envSetField(genEnvSpec(rng), "bump", lkal.Int(rev))
			updCand = envSetField(updCand, "spec", newSpec)
			next, err := envelope.Update(doc, rev, updCand)
			if err != nil {
				panic(fmt.Sprintf("Update failed building stored doc: %v", err))
			}
			doc = next
			rev++
			gen++
		} else {
			break
		}
	}
	if needObs || chance(rng, 65) {
		stCand := buildValidStatusCand(rng, doc, rev, needObs)
		next, err := envelope.UpdateStatus(doc, rev, stCand, pick(rng, envTimestamps))
		if err != nil {
			panic(fmt.Sprintf("UpdateStatus failed building stored doc: %v", err))
		}
		doc = next
		rev++
	}
	return doc, rev
}

func buildValidStatusCand(rng *rand.Rand, old lkal.Record, rev int64, forceObs bool) lkal.Record {
	oldMeta, _ := envGetRecord(old, "meta")
	oldGenVal, _ := envGetField(oldMeta, "generation")
	oldGen := int64(oldGenVal.(lkal.Int))

	cand := envCloneRecord(old)
	if chance(rng, 50) {
		cand = envelope.WithRevision(cand, rev)
	} else {
		meta, _ := envGetRecord(cand, "meta")
		meta = envDropField(meta, "uid")
		meta = envDropField(meta, "generation")
		cand = envSetField(cand, "meta", meta)
	}
	if chance(rng, 40) {
		cand = envDropField(cand, "spec")
	}
	if chance(rng, 40) {
		meta, _ := envGetRecord(cand, "meta")
		meta = envDropField(meta, "labels")
		cand = envSetField(cand, "meta", meta)
	}

	var oldObs int64
	var hasOldObs bool
	var oldCond lkal.Record
	if oldStatus, ok := envGetRecord(old, "status"); ok {
		if obsVal, ok := envGetField(oldStatus, "observedGeneration"); ok {
			oldObs = int64(obsVal.(lkal.Int))
			hasOldObs = true
		}
		oldCond, _ = envGetRecord(oldStatus, "condition")
	}

	if !forceObs && !hasOldObs && chance(rng, 15) {
		cand = envDropField(cand, "status")
		return cand
	}

	var stFields []lkal.Field
	if forceObs || hasOldObs || chance(rng, 75) {
		lo := int64(1)
		if hasOldObs {
			lo = oldObs
		}
		if forceObs && oldGen >= 2 && lo < 2 {
			lo = 2
		}
		obs := lo
		if oldGen > lo {
			obs = lo + rng.Int64N(oldGen-lo+1)
		}
		stFields = append(stFields, envField("observedGeneration", lkal.Int(obs)))
	}
	if chance(rng, 85) {
		types := slices.Clone(envCondTypes)
		rng.Shuffle(len(types), func(i, j int) { types[i], types[j] = types[j], types[i] })
		count := 1 + rng.IntN(2)
		condFields := make([]lkal.Field, 0, count)
		for i := range count {
			ct := types[i]
			state := pick(rng, envCondStates)
			reason := pick(rng, envReasons)
			cFields := []lkal.Field{
				envField("state", state),
				envField("reason", lkal.String(reason)),
			}
			// Sometimes carry old's exact since when condition already exists.
			if oldC, ok := envGetRecord(oldCond, ct); ok && chance(rng, 45) {
				if oldSince, ok := envGetField(oldC, "since"); ok {
					cFields = append(cFields, envField("since", oldSince))
				}
			}
			condFields = append(condFields, envField(ct, lkal.Record{Fields: cFields}))
		}
		stFields = append(stFields, envField("condition", lkal.Record{Fields: condFields}))
	}
	if chance(rng, 35) {
		stFields = append(stFields, envField("phase", lkal.String(pick(rng, []string{"Active", "Ready", "Degraded"}))))
	}
	cand = envSetField(cand, "status", lkal.Record{Fields: stFields})
	if chance(rng, 50) {
		cand = envShuffleRecord(rng, cand)
	}
	return cand
}

func injectDuplicateKeyInSpec(rng *rand.Rand, spec lkal.Record) lkal.Record {
	dup := envRec(envField("dup", lkal.Int(1)), envField("dup", lkal.Int(2)))
	out := envCloneRecord(spec)
	switch rng.IntN(3) {
	case 0:
		out.Fields = append(out.Fields, envField("x", lkal.Int(1)), envField("x", lkal.Int(2)))
	case 1:
		out.Fields = append(out.Fields, envField("nestedDup", dup))
	default:
		out.Fields = append(out.Fields, envField("listDup", lkal.List{lkal.String("ok"), dup}))
	}
	return out
}

func corruptShape(rng *rand.Rand, doc lkal.Record) lkal.Record {
	out := envCloneRecord(doc)
	meta, hasMeta := envGetRecord(out, "meta")
	if !hasMeta {
		meta = envRec(envField("kind", lkal.String("pod")), envField("name", lkal.String("web")))
	}
	switch rng.IntN(18) {
	case 0:
		// Duplicate top-level key.
		out.Fields = append(out.Fields, envField("meta", envCloneRecord(meta)))
	case 1:
		// Duplicate meta key.
		meta.Fields = append(meta.Fields, envField("kind", lkal.String("pod")))
		out = envSetField(out, "meta", meta)
	case 2:
		// Deep duplicate key inside spec (nested or inside list).
		spec, ok := envGetRecord(out, "spec")
		if !ok {
			spec = envRec()
		}
		out = envSetField(out, "spec", injectDuplicateKeyInSpec(rng, spec))
	case 3:
		// Unexpected top-level field or missing meta.
		if chance(rng, 50) {
			out = envDropField(out, "meta")
		} else {
			out = envSetField(out, "data", envRec())
		}
	case 4:
		// Non-record top-level meta/spec/status.
		switch rng.IntN(3) {
		case 0:
			out = envSetField(out, "meta", lkal.String("bad"))
		case 1:
			out = envSetField(out, "spec", lkal.Int(1))
		default:
			out = envSetField(out, "status", lkal.List{})
		}
	case 5:
		// Bad meta/group (including "core").
		if chance(rng, 50) {
			meta = envSetField(meta, "group", lkal.String("core"))
		} else {
			meta = envSetField(meta, "group", lkal.String(pick(rng, envBadGroups)))
		}
		out = envSetField(out, "meta", meta)
	case 6:
		// Bad or missing meta/kind.
		if chance(rng, 25) {
			meta = envDropField(meta, "kind")
		} else if chance(rng, 25) {
			meta = envSetField(meta, "kind", lkal.Int(1))
		} else {
			meta = envSetField(meta, "kind", lkal.String(pick(rng, envBadKinds)))
		}
		out = envSetField(out, "meta", meta)
	case 7:
		// Bad or missing meta/name.
		if chance(rng, 25) {
			meta = envDropField(meta, "name")
		} else {
			meta = envSetField(meta, "name", lkal.String(pick(rng, envBadNames)))
		}
		out = envSetField(out, "meta", meta)
	case 8:
		// Unexpected meta field.
		meta = envSetField(meta, "namespace", lkal.String("default"))
		out = envSetField(out, "meta", meta)
	case 9:
		// Bad meta/labels.
		if chance(rng, 50) {
			meta = envSetField(meta, "labels", lkal.String("bad"))
		} else {
			meta = envSetField(meta, "labels", envRec(envField("k", lkal.Int(1))))
		}
		out = envSetField(out, "meta", meta)
	case 10:
		// Bad meta/uid, revision, or generation.
		switch rng.IntN(3) {
		case 0:
			meta = envSetField(meta, "uid", lkal.String(pick(rng, envBadUIDs)))
		case 1:
			meta = envSetField(meta, "revision", lkal.Int(pick(rng, []int64{0, -1})))
		default:
			meta = envSetField(meta, "generation", lkal.Int(pick(rng, []int64{0, -2})))
		}
		out = envSetField(out, "meta", meta)
	case 11:
		// Bad status/observedGeneration.
		st, _ := envGetRecord(out, "status")
		st = envSetField(st, "observedGeneration", lkal.Int(pick(rng, []int64{0, -1})))
		out = envSetField(out, "status", st)
	case 12:
		// Non-record condition or invalid condition type.
		st, _ := envGetRecord(out, "status")
		if chance(rng, 40) {
			st = envSetField(st, "condition", lkal.List{})
		} else {
			st = envSetField(st, "condition", envRec(
				envField(pick(rng, envBadCondType), envRec(
					envField("state", lkal.Enum("true")),
					envField("reason", lkal.String("Ok")),
				)),
			))
		}
		out = envSetField(out, "status", st)
	case 13:
		// Condition entry missing state/reason, extra field, or duplicate key in condition.
		c := envRec(envField("state", lkal.Enum("true")), envField("reason", lkal.String("Ok")))
		switch rng.IntN(4) {
		case 0:
			c = envDropField(c, "state")
		case 1:
			c = envDropField(c, "reason")
		case 2:
			c = envSetField(c, "message", lkal.String("extra"))
		default:
			c.Fields = append(c.Fields, envField("state", lkal.Enum("false")))
		}
		st, _ := envGetRecord(out, "status")
		st = envSetField(st, "condition", envRec(envField("Ready", c)))
		out = envSetField(out, "status", st)
	case 14:
		// Condition state wrong type or wrong enum name.
		var stateVal lkal.Value
		if chance(rng, 50) {
			stateVal = lkal.String("true")
		} else {
			stateVal = lkal.Enum("maybe")
		}
		st, _ := envGetRecord(out, "status")
		st = envSetField(st, "condition", envRec(
			envField("Ready", envRec(envField("state", stateVal), envField("reason", lkal.String("Ok")))),
		))
		out = envSetField(out, "status", st)
	case 15:
		// Invalid reason.
		st, _ := envGetRecord(out, "status")
		st = envSetField(st, "condition", envRec(
			envField("Ready", envRec(
				envField("state", lkal.Enum("true")),
				envField("reason", lkal.String(pick(rng, envBadReasons))),
			)),
		))
		out = envSetField(out, "status", st)
	case 16:
		// Invalid since timestamp.
		st, _ := envGetRecord(out, "status")
		st = envSetField(st, "condition", envRec(
			envField("Ready", envRec(
				envField("state", lkal.Enum("true")),
				envField("reason", lkal.String("Ok")),
				envField("since", lkal.String(pick(rng, envBadTS))),
			)),
		))
		out = envSetField(out, "status", st)
	default:
		// Non-string reason or non-record condition entry.
		st, _ := envGetRecord(out, "status")
		st = envSetField(st, "condition", envRec(envField("Ready", lkal.Bool(true))))
		out = envSetField(out, "status", st)
	}
	return out
}

func corruptStoredDoc(rng *rand.Rand, old lkal.Record) lkal.Record {
	out := envCloneRecord(old)
	meta, _ := envGetRecord(out, "meta")
	switch rng.IntN(6) {
	case 0:
		meta = envDropField(meta, "uid")
		return envSetField(out, "meta", meta)
	case 1:
		meta = envDropField(meta, "generation")
		return envSetField(out, "meta", meta)
	case 2:
		meta = envSetField(meta, "revision", lkal.Int(1))
		return envSetField(out, "meta", meta)
	case 3:
		// Drop since from a condition in old.
		st, _ := envGetRecord(out, "status")
		st = envSetField(st, "condition", envRec(
			envField("Ready", envRec(
				envField("state", lkal.Enum("true")),
				envField("reason", lkal.String("Ok")),
			)),
		))
		return envSetField(out, "status", st)
	case 4:
		// observedGeneration above generation in old.
		genVal, _ := envGetField(meta, "generation")
		gen := int64(genVal.(lkal.Int))
		st, _ := envGetRecord(out, "status")
		st = envSetField(st, "observedGeneration", lkal.Int(gen+1))
		return envSetField(out, "status", st)
	default:
		return corruptShape(rng, out)
	}
}

func mutateIdentity(rng *rand.Rand, doc lkal.Record) lkal.Record {
	out := envCloneRecord(doc)
	meta, _ := envGetRecord(out, "meta")
	switch rng.IntN(3) {
	case 0:
		// Flip group presence or pick a different valid group.
		curGroup, hasGroup := envGetField(meta, "group")
		if hasGroup && chance(rng, 50) {
			meta = envDropField(meta, "group")
		} else {
			for _, g := range envGroups[1:] {
				if !hasGroup || lkal.String(g) != curGroup {
					meta = envSetField(meta, "group", lkal.String(g))
					break
				}
			}
		}
	case 1:
		curKind, _ := envGetField(meta, "kind")
		for _, k := range envKinds {
			if lkal.String(k) != curKind {
				meta = envSetField(meta, "kind", lkal.String(k))
				break
			}
		}
	default:
		curName, _ := envGetField(meta, "name")
		for _, n := range envNames {
			if lkal.String(n) != curName {
				meta = envSetField(meta, "name", lkal.String(n))
				break
			}
		}
	}
	return envSetField(out, "meta", meta)
}

func differentValidUID(old lkal.Record) string {
	meta, _ := envGetRecord(old, "meta")
	oldUID, _ := envGetField(meta, "uid")
	for _, u := range envUIDs {
		if lkal.String(u) != oldUID {
			return u
		}
	}
	return envUIDs[0]
}

func makeCandFromOld(rng *rand.Rand, old lkal.Record, rev int64, withRev bool) lkal.Record {
	cand := envCloneRecord(old)
	if withRev {
		cand = envelope.WithRevision(cand, rev)
	}
	if chance(rng, 45) {
		meta, _ := envGetRecord(cand, "meta")
		meta = envDropField(meta, "uid")
		meta = envDropField(meta, "generation")
		cand = envSetField(cand, "meta", meta)
	}
	return cand
}

func genCreateCase(rng *rand.Rand, id int) envelopeCase {
	tc := envelopeCase{
		ID:   id,
		Op:   "create",
		Cand: genEnvValidCreateCand(rng),
		UID:  pick(rng, envUIDs),
	}
	r := rng.IntN(100)
	switch {
	case r < 35:
		// Valid create.
	case r < 52:
		// Internal: bad uid (sometimes also malformed/forbidden cand).
		tc.UID = pick(rng, envBadUIDs)
		if chance(rng, 40) {
			tc.Cand = corruptShape(rng, tc.Cand)
		} else if chance(rng, 30) {
			tc.Cand = envSetField(tc.Cand, "status", envRec())
		}
	case r < 76:
		// Malformed cand (sometimes also forbidden fields).
		if chance(rng, 35) {
			meta, _ := envGetRecord(tc.Cand, "meta")
			meta = envSetField(meta, "uid", lkal.String(pick(rng, envUIDs)))
			tc.Cand = envSetField(tc.Cand, "meta", meta)
			tc.Cand = envSetField(tc.Cand, "status", envRec())
		}
		tc.Cand = corruptShape(rng, tc.Cand)
	default:
		// Forbidden: valid shape with meta/uid, meta/revision, meta/generation, or status.
		meta, _ := envGetRecord(tc.Cand, "meta")
		switch rng.IntN(4) {
		case 0:
			meta = envSetField(meta, "uid", lkal.String(pick(rng, envUIDs)))
			tc.Cand = envSetField(tc.Cand, "meta", meta)
		case 1:
			meta = envSetField(meta, "revision", lkal.Int(1+rng.Int64N(5)))
			tc.Cand = envSetField(tc.Cand, "meta", meta)
		case 2:
			meta = envSetField(meta, "generation", lkal.Int(1+rng.Int64N(5)))
			tc.Cand = envSetField(tc.Cand, "meta", meta)
		default:
			tc.Cand = envSetField(tc.Cand, "status", envRec(
				envField("observedGeneration", lkal.Int(1)),
			))
		}
	}
	if chance(rng, 40) {
		tc.Cand = envShuffleRecord(rng, tc.Cand)
	}
	return tc
}

func genUpdateCase(rng *rand.Rand, id int, pool []envPoolEntry) envelopeCase {
	var base envPoolEntry
	if len(pool) > 0 && chance(rng, 80) {
		base = pick(rng, pool)
	} else {
		base.doc, base.rev = buildStoredDoc(rng, 1, false)
	}
	rev := base.rev + rng.Int64N(3)
	if rev < 1 {
		rev = 1
	}

	tc := envelopeCase{
		ID:  id,
		Op:  "update",
		Old: envCloneRecord(base.doc),
		Rev: rev,
	}

	r := rng.IntN(100)
	switch {
	case r < 32:
		// Valid update.
		tc.Cand = makeCandFromOld(rng, base.doc, rev, chance(rng, 60))
		if chance(rng, 35) {
			// Spec unchanged (maybe reordered), labels optionally modified.
			if chance(rng, 50) {
				meta, _ := envGetRecord(tc.Cand, "meta")
				meta = envSetField(meta, "labels", genEnvLabels(rng))
				tc.Cand = envSetField(tc.Cand, "meta", meta)
			}
		} else {
			// Change, add, or remove spec.
			switch rng.IntN(3) {
			case 0:
				tc.Cand = envDropField(tc.Cand, "spec")
			case 1:
				tc.Cand = envSetField(tc.Cand, "spec", genEnvSpec(rng))
			default:
				spec, _ := envGetRecord(tc.Cand, "spec")
				spec = envSetField(spec, "updatedAt", lkal.Int(rev))
				tc.Cand = envSetField(tc.Cand, "spec", spec)
			}
		}
		if chance(rng, 40) {
			tc.Cand = envDropField(tc.Cand, "status")
		}
	case r < 48:
		// Internal: corrupted old or rev < 1 (sometimes also bad cand).
		tc.Cand = makeCandFromOld(rng, base.doc, rev, true)
		if chance(rng, 60) {
			tc.Old = corruptStoredDoc(rng, base.doc)
		} else {
			tc.Rev = pick(rng, []int64{0, -1, -7})
		}
		if chance(rng, 35) {
			tc.Cand = corruptShape(rng, tc.Cand)
		}
	case r < 66:
		// Malformed cand (sometimes also stale revision / identity change).
		tc.Cand = makeCandFromOld(rng, base.doc, rev+1, true)
		if chance(rng, 30) {
			tc.Cand = mutateIdentity(rng, tc.Cand)
		}
		tc.Cand = corruptShape(rng, tc.Cand)
	case r < 80:
		// Conflict: stale revision (sometimes also forbidden uid/generation/status).
		staleRev := rev + 1
		if rev > 1 && chance(rng, 50) {
			staleRev = rev - 1
		}
		tc.Cand = makeCandFromOld(rng, base.doc, staleRev, true)
		if chance(rng, 40) {
			meta, _ := envGetRecord(tc.Cand, "meta")
			meta = envSetField(meta, "uid", lkal.String(differentValidUID(base.doc)))
			tc.Cand = envSetField(tc.Cand, "meta", meta)
			tc.Cand = envSetField(tc.Cand, "status", envRec(envField("extra", lkal.Int(1))))
		}
	default:
		// Forbidden: identity change (maybe + stale rev), or uid/generation/status change.
		tc.Cand = makeCandFromOld(rng, base.doc, rev, chance(rng, 50))
		switch rng.IntN(4) {
		case 0:
			tc.Cand = mutateIdentity(rng, tc.Cand)
			if chance(rng, 40) {
				tc.Cand = envelope.WithRevision(tc.Cand, rev+2)
			}
		case 1:
			meta, _ := envGetRecord(tc.Cand, "meta")
			meta = envSetField(meta, "uid", lkal.String(differentValidUID(base.doc)))
			tc.Cand = envSetField(tc.Cand, "meta", meta)
		case 2:
			meta, _ := envGetRecord(tc.Cand, "meta")
			genVal, _ := envGetField(meta, "generation")
			if genVal == nil {
				oldMeta, _ := envGetRecord(base.doc, "meta")
				genVal, _ = envGetField(oldMeta, "generation")
			}
			meta = envSetField(meta, "generation", lkal.Int(int64(genVal.(lkal.Int))+1))
			tc.Cand = envSetField(tc.Cand, "meta", meta)
		default:
			tc.Cand = envSetField(tc.Cand, "status", envRec(envField("note", lkal.String("changed"))))
		}
	}

	if chance(rng, 45) {
		tc.Cand = envShuffleRecord(rng, tc.Cand)
	}
	return tc
}

func genStatusCase(rng *rand.Rand, id int, pool []envPoolEntry) envelopeCase {
	var base envPoolEntry
	if len(pool) > 0 && chance(rng, 75) {
		base = pick(rng, pool)
	} else {
		base.doc, base.rev = buildStoredDoc(rng, 1, false)
	}
	rev := base.rev + rng.Int64N(3)
	if rev < 1 {
		rev = 1
	}

	tc := envelopeCase{
		ID:  id,
		Op:  "status",
		Old: envCloneRecord(base.doc),
		Rev: rev,
		Now: pick(rng, envTimestamps),
	}

	r := rng.IntN(100)
	switch {
	case r < 30:
		// Valid status update.
		tc.Cand = buildValidStatusCand(rng, base.doc, rev, false)
	case r < 44:
		// Internal: bad old, rev < 1, or bad now.
		tc.Cand = buildValidStatusCand(rng, base.doc, rev, false)
		switch rng.IntN(3) {
		case 0:
			tc.Old = corruptStoredDoc(rng, base.doc)
		case 1:
			tc.Rev = pick(rng, []int64{0, -1, -5})
		default:
			tc.Now = pick(rng, envBadTS)
		}
		if chance(rng, 30) {
			tc.Cand = corruptShape(rng, tc.Cand)
		}
	case r < 58:
		// Malformed cand (sometimes also stale rev or observed error).
		tc.Cand = buildValidStatusCand(rng, base.doc, rev+1, false)
		tc.Cand = corruptShape(rng, tc.Cand)
	case r < 70:
		// Conflict: stale revision (sometimes also forbidden labels/spec/since or observed error).
		staleRev := rev + 1
		if rev > 1 && chance(rng, 50) {
			staleRev = rev - 1
		}
		tc.Cand = buildValidStatusCand(rng, base.doc, staleRev, false)
		tc.Cand = envelope.WithRevision(tc.Cand, staleRev)
		if chance(rng, 40) {
			tc.Cand = envSetField(tc.Cand, "spec", envRec(envField("changed", lkal.Bool(true))))
		}
	case r < 85:
		// Forbidden: identity, uid/generation, labels, spec, or since (sometimes also observed error).
		tc.Cand = buildValidStatusCand(rng, base.doc, rev, false)
		switch rng.IntN(5) {
		case 0:
			tc.Cand = mutateIdentity(rng, tc.Cand)
			if chance(rng, 40) {
				tc.Cand = envelope.WithRevision(tc.Cand, rev+1)
			}
		case 1:
			meta, _ := envGetRecord(tc.Cand, "meta")
			if chance(rng, 50) {
				meta = envSetField(meta, "uid", lkal.String(differentValidUID(base.doc)))
			} else {
				oldMeta, _ := envGetRecord(base.doc, "meta")
				genVal, _ := envGetField(oldMeta, "generation")
				meta = envSetField(meta, "generation", lkal.Int(int64(genVal.(lkal.Int))+1))
			}
			tc.Cand = envSetField(tc.Cand, "meta", meta)
		case 2:
			meta, _ := envGetRecord(tc.Cand, "meta")
			meta = envSetField(meta, "labels", envRec(envField("forbidden", lkal.String("yes"))))
			tc.Cand = envSetField(tc.Cand, "meta", meta)
		case 3:
			tc.Cand = envSetField(tc.Cand, "spec", envRec(envField("forbidden", lkal.Int(99))))
		default:
			// Bad since: either on a brand-new condition or changed on an existing condition.
			st, _ := envGetRecord(tc.Cand, "status")
			oldSt, _ := envGetRecord(base.doc, "status")
			oldCond, _ := envGetRecord(oldSt, "condition")
			var badSinceTS = "2020-01-01T00:00:00Z"
			if len(oldCond.Fields) > 0 && chance(rng, 50) {
				cf := oldCond.Fields[0]
				c := cf.Value.(lkal.Record)
				if oldSince, ok := envGetField(c, "since"); ok && oldSince == lkal.String(badSinceTS) {
					badSinceTS = "2021-02-02T00:00:00Z"
				}
				st = envSetField(st, "condition", envRec(envField(cf.Key, envRec(
					envField("state", lkal.Enum("true")),
					envField("reason", lkal.String("Ok")),
					envField("since", lkal.String(badSinceTS)),
				))))
			} else {
				st = envSetField(st, "condition", envRec(envField("CustomNewCond", envRec(
					envField("state", lkal.Enum("false")),
					envField("reason", lkal.String("New")),
					envField("since", lkal.String(badSinceTS)),
				))))
			}
			if chance(rng, 50) {
				// Combine forbidden since with observedGeneration > gen to check order.
				oldMeta, _ := envGetRecord(base.doc, "meta")
				genVal, _ := envGetField(oldMeta, "generation")
				st = envSetField(st, "observedGeneration", lkal.Int(int64(genVal.(lkal.Int))+5))
			}
			tc.Cand = envSetField(tc.Cand, "status", st)
		}
	default:
		// Observed error: exceeds generation, regresses, or vanishes.
		mode := rng.IntN(3)
		oldDoc := base.doc
		oldStatus, _ := envGetRecord(oldDoc, "status")
		oldObsVal, hasOldObs := envGetField(oldStatus, "observedGeneration")
		if mode != 0 && (!hasOldObs || int64(oldObsVal.(lkal.Int)) < 2) {
			oldDoc, rev = buildStoredDoc(rng, 2, true)
			tc.Old = envCloneRecord(oldDoc)
			tc.Rev = rev
			oldStatus, _ = envGetRecord(oldDoc, "status")
			oldObsVal, _ = envGetField(oldStatus, "observedGeneration")
		}
		oldMeta, _ := envGetRecord(oldDoc, "meta")
		genVal, _ := envGetField(oldMeta, "generation")
		gen := int64(genVal.(lkal.Int))

		tc.Cand = buildValidStatusCand(rng, oldDoc, rev, false)
		st, _ := envGetRecord(tc.Cand, "status")
		switch mode {
		case 0:
			st = envSetField(st, "observedGeneration", lkal.Int(gen+1+rng.Int64N(3)))
			tc.Cand = envSetField(tc.Cand, "status", st)
		case 1:
			oldObs := int64(oldObsVal.(lkal.Int))
			st = envSetField(st, "observedGeneration", lkal.Int(oldObs-1))
			tc.Cand = envSetField(tc.Cand, "status", st)
		default:
			if chance(rng, 50) {
				tc.Cand = envDropField(tc.Cand, "status")
			} else {
				st = envDropField(st, "observedGeneration")
				tc.Cand = envSetField(tc.Cand, "status", st)
			}
		}
	}

	if chance(rng, 45) {
		tc.Cand = envShuffleRecord(rng, tc.Cand)
	}
	return tc
}

func generateEnvelopeCases(seed uint64, n int) []envelopeCase {
	rng := rand.New(rand.NewPCG(seed, 0x656e76656c6f7065))
	cases := make([]envelopeCase, n)
	const maxPool = 6
	var pool []envPoolEntry

	addPool := func(doc lkal.Record, rev int64) {
		meta, _ := envGetRecord(doc, "meta")
		genVal, _ := envGetField(meta, "generation")
		if int64(genVal.(lkal.Int)) >= 8 {
			return
		}
		entry := envPoolEntry{doc: doc, rev: rev}
		if len(pool) < maxPool {
			pool = append(pool, entry)
			return
		}
		pool[rng.IntN(len(pool))] = entry
	}

	for i := range n {
		var tc envelopeCase
		switch r := rng.IntN(100); {
		case r < 25:
			tc = genCreateCase(rng, i)
			if doc, err := envelope.Create(tc.Cand, tc.UID); err == nil {
				addPool(doc, 1)
			}
		case r < 60:
			tc = genUpdateCase(rng, i, pool)
			if doc, err := envelope.Update(tc.Old, tc.Rev, tc.Cand); err == nil {
				addPool(doc, tc.Rev+1)
			}
		default:
			tc = genStatusCase(rng, i, pool)
			if doc, err := envelope.UpdateStatus(tc.Old, tc.Rev, tc.Cand, tc.Now); err == nil {
				addPool(doc, tc.Rev+1)
			}
		}
		cases[i] = tc
	}
	return cases
}

func assertValidWireValueUTF8(t *testing.T, id int, path string, v lkal.Value) {
	t.Helper()
	switch val := v.(type) {
	case lkal.String:
		assertValidUTF8(t, id, path, string(val))
	case lkal.Decimal:
		assertValidUTF8(t, id, path, string(val))
	case lkal.Enum:
		assertValidUTF8(t, id, path, string(val))
	case lkal.Quantity:
		assertValidUTF8(t, id, path+".number", val.Number)
		assertValidUTF8(t, id, path+".unit", val.Unit)
	case lkal.Ref:
		for i, seg := range val {
			assertValidUTF8(t, id, fmt.Sprintf("%s[%d]", path, i), seg)
		}
	case lkal.List:
		for i, elem := range val {
			assertValidWireValueUTF8(t, id, fmt.Sprintf("%s[%d]", path, i), elem)
		}
	case lkal.Record:
		for _, f := range val.Fields {
			assertValidUTF8(t, id, path+".key", f.Key)
			assertValidWireValueUTF8(t, id, path+"."+f.Key, f.Value)
		}
	}
}

func encodeEnvelopeCases(t *testing.T, cases []envelopeCase) [][]byte {
	t.Helper()
	encoded := make([][]byte, len(cases))
	for i, tc := range cases {
		switch tc.Op {
		case "create":
			assertValidUTF8(t, tc.ID, "uid", tc.UID)
			assertValidWireValueUTF8(t, tc.ID, "cand", tc.Cand)
		case "update":
			assertValidWireValueUTF8(t, tc.ID, "old", tc.Old)
			assertValidWireValueUTF8(t, tc.ID, "cand", tc.Cand)
		case "status":
			assertValidWireValueUTF8(t, tc.ID, "old", tc.Old)
			assertValidWireValueUTF8(t, tc.ID, "cand", tc.Cand)
			assertValidUTF8(t, tc.ID, "now", tc.Now)
		default:
			t.Fatalf("case id=%d unknown op %q", tc.ID, tc.Op)
		}
		b, err := json.Marshal(tc)
		if err != nil {
			t.Fatal(err)
		}
		encoded[i] = b
	}
	return encoded
}

func envelopeErrName(err error) (string, error) {
	switch {
	case err == nil:
		return "", nil
	case errors.Is(err, envelope.ErrInternal):
		return "internal", nil
	case errors.Is(err, envelope.ErrMalformed):
		return "malformed", nil
	case errors.Is(err, envelope.ErrConflict):
		return "conflict", nil
	case errors.Is(err, envelope.ErrForbidden):
		return "forbidden", nil
	case errors.Is(err, envelope.ErrObserved):
		return "observed", nil
	default:
		return "", err
	}
}

type envelopeHooks struct {
	createIgnoreBadUID      bool
	createAllowStatus       bool
	updateIgnoreNonPosRev   bool
	updateConflictFirst     bool
	updateAlwaysBumpGen     bool
	updateAllowStatusChange bool
	statusIgnoreBadNow      bool
	statusObservedFirst     bool
	statusAlwaysUpdateSince bool
	statusAllowObsRegress   bool
}

func runEnvelopeCase(tc envelopeCase, hooks envelopeHooks) (envelopeResult, error) {
	res := envelopeResult{ID: tc.ID}
	var (
		doc lkal.Record
		err error
	)
	switch tc.Op {
	case "create":
		cand := tc.Cand
		uid := tc.UID
		if hooks.createIgnoreBadUID {
			uid = envUIDs[0]
			doc, err = envelope.Create(cand, uid)
		} else if hooks.createAllowStatus {
			if _, hasStatus := envGetField(cand, "status"); hasStatus && envelope.CheckShape(cand) == nil {
				doc, err = envelope.Create(envDropField(cand, "status"), uid)
			} else {
				doc, err = envelope.Create(cand, uid)
			}
		} else {
			doc, err = envelope.Create(cand, uid)
		}
	case "update":
		rev := tc.Rev
		if hooks.updateIgnoreNonPosRev && rev < 1 {
			rev = 1
		}
		if hooks.updateConflictFirst && envelope.CheckStored(tc.Old) == nil && rev >= 1 && envelope.CheckShape(tc.Cand) == nil {
			candMeta, _ := envGetRecord(tc.Cand, "meta")
			if candRev, ok := envGetField(candMeta, "revision"); ok {
				if rInt, isInt := candRev.(lkal.Int); isInt && int64(rInt) != rev {
					res.Err = "conflict"
					return res, nil
				}
			}
		}
		if hooks.updateAllowStatusChange {
			if st, hasOldSt := envGetField(tc.Old, "status"); hasOldSt && envelope.CheckShape(tc.Cand) == nil {
				doc, err = envelope.Update(tc.Old, rev, envSetField(tc.Cand, "status", st))
			} else if envelope.CheckShape(tc.Cand) == nil {
				doc, err = envelope.Update(tc.Old, rev, envDropField(tc.Cand, "status"))
			} else {
				doc, err = envelope.Update(tc.Old, rev, tc.Cand)
			}
		} else {
			doc, err = envelope.Update(tc.Old, rev, tc.Cand)
		}
		if err == nil && hooks.updateAlwaysBumpGen {
			oldMeta, _ := envGetRecord(tc.Old, "meta")
			oldGen, _ := envGetField(oldMeta, "generation")
			meta, _ := envGetRecord(doc, "meta")
			meta = envSetField(meta, "generation", lkal.Int(int64(oldGen.(lkal.Int))+1))
			doc = envSetField(doc, "meta", meta)
		}
	case "status":
		now := tc.Now
		if hooks.statusIgnoreBadNow {
			now = envTimestamps[0]
		}
		if hooks.statusObservedFirst && envelope.CheckStored(tc.Old) == nil && tc.Rev >= 1 && envelope.CheckShape(tc.Cand) == nil {
			oldMeta, _ := envGetRecord(tc.Old, "meta")
			oldGenVal, _ := envGetField(oldMeta, "generation")
			oldGen := int64(oldGenVal.(lkal.Int))
			candSt, _ := envGetRecord(tc.Cand, "status")
			if candObs, ok := envGetField(candSt, "observedGeneration"); ok && int64(candObs.(lkal.Int)) > oldGen {
				res.Err = "observed"
				return res, nil
			}
		}
		if hooks.statusAllowObsRegress && envelope.CheckStored(tc.Old) == nil && tc.Rev >= 1 && envelope.CheckShape(tc.Cand) == nil {
			oldSt, hasOldSt := envGetRecord(tc.Old, "status")
			if hasOldSt {
				if oldObs, ok := envGetField(oldSt, "observedGeneration"); ok {
					candSt, hasCandSt := envGetRecord(tc.Cand, "status")
					if hasCandSt {
						if candObs, ok := envGetField(candSt, "observedGeneration"); ok && int64(candObs.(lkal.Int)) < int64(oldObs.(lkal.Int)) {
							candFix := envSetField(tc.Cand, "status", envSetField(candSt, "observedGeneration", oldObs))
							doc, err = envelope.UpdateStatus(tc.Old, tc.Rev, candFix, now)
							goto afterOp
						}
					}
				}
			}
		}
		doc, err = envelope.UpdateStatus(tc.Old, tc.Rev, tc.Cand, now)
		if err == nil && hooks.statusAlwaysUpdateSince {
			if st, hasSt := envGetRecord(doc, "status"); hasSt {
				if cond, hasCond := envGetRecord(st, "condition"); hasCond {
					for i, cf := range cond.Fields {
						c := cf.Value.(lkal.Record)
						cond.Fields[i].Value = envSetField(c, "since", lkal.String(now))
					}
					doc = envSetField(doc, "status", envSetField(st, "condition", cond))
				}
			}
		}
	default:
		return envelopeResult{}, fmt.Errorf("unknown op %q", tc.Op)
	}

afterOp:
	if err == nil {
		res.OK = true
		res.Doc = doc
		return res, nil
	}
	errName, mapErr := envelopeErrName(err)
	if mapErr != nil {
		return envelopeResult{}, mapErr
	}
	res.Err = errName
	return res, nil
}

func decodeWireIntStr(raw json.RawMessage) (int64, error) {
	s, err := decodeString(raw)
	if err != nil {
		return 0, err
	}
	if s == "" || s == "-" {
		return 0, fmt.Errorf("empty integer string %q", s)
	}
	digits := s
	if digits[0] == '-' {
		digits = digits[1:]
	}
	for i := range len(digits) {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, fmt.Errorf("invalid integer string %q", s)
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

func decodeWireValue(raw json.RawMessage) (lkal.Value, error) {
	var obj map[string]json.RawMessage
	if err := decodeStrictJSON(raw, &obj); err != nil {
		return nil, err
	}
	if len(obj) != 1 {
		return nil, fmt.Errorf("wire value must have exactly 1 key: %s", raw)
	}
	for k, v := range obj {
		switch k {
		case "s":
			s, err := decodeString(v)
			if err != nil {
				return nil, fmt.Errorf("s: %w", err)
			}
			return lkal.String(s), nil
		case "i":
			n, err := decodeWireIntStr(v)
			if err != nil {
				return nil, fmt.Errorf("i: %w", err)
			}
			return lkal.Int(n), nil
		case "d":
			s, err := decodeString(v)
			if err != nil {
				return nil, fmt.Errorf("d: %w", err)
			}
			return lkal.Decimal(s), nil
		case "b":
			var b bool
			if err := decodeStrictJSON(v, &b); err != nil {
				return nil, fmt.Errorf("b: %w", err)
			}
			return lkal.Bool(b), nil
		case "e":
			s, err := decodeString(v)
			if err != nil {
				return nil, fmt.Errorf("e: %w", err)
			}
			return lkal.Enum(s), nil
		case "q":
			var arr []json.RawMessage
			if err := decodeStrictJSON(v, &arr); err != nil {
				return nil, fmt.Errorf("q: %w", err)
			}
			if len(arr) != 2 {
				return nil, fmt.Errorf("q must have 2 elements, got %d", len(arr))
			}
			num, err := decodeString(arr[0])
			if err != nil {
				return nil, fmt.Errorf("q[0]: %w", err)
			}
			unit, err := decodeString(arr[1])
			if err != nil {
				return nil, fmt.Errorf("q[1]: %w", err)
			}
			return lkal.Quantity{Number: num, Unit: unit}, nil
		case "r":
			var arr []json.RawMessage
			if err := decodeStrictJSON(v, &arr); err != nil {
				return nil, fmt.Errorf("r: %w", err)
			}
			segs := make(lkal.Ref, len(arr))
			for i, elem := range arr {
				s, err := decodeString(elem)
				if err != nil {
					return nil, fmt.Errorf("r[%d]: %w", i, err)
				}
				segs[i] = s
			}
			return segs, nil
		case "l":
			var arr []json.RawMessage
			if err := decodeStrictJSON(v, &arr); err != nil {
				return nil, fmt.Errorf("l: %w", err)
			}
			items := make(lkal.List, len(arr))
			for i, elem := range arr {
				item, err := decodeWireValue(elem)
				if err != nil {
					return nil, fmt.Errorf("l[%d]: %w", i, err)
				}
				items[i] = item
			}
			return items, nil
		case "o":
			var arr []json.RawMessage
			if err := decodeStrictJSON(v, &arr); err != nil {
				return nil, fmt.Errorf("o: %w", err)
			}
			fields := make([]lkal.Field, len(arr))
			for i, elem := range arr {
				var pair []json.RawMessage
				if err := decodeStrictJSON(elem, &pair); err != nil {
					return nil, fmt.Errorf("o[%d]: %w", i, err)
				}
				if len(pair) != 2 {
					return nil, fmt.Errorf("o[%d] must have 2 elements, got %d", i, len(pair))
				}
				fk, err := decodeString(pair[0])
				if err != nil {
					return nil, fmt.Errorf("o[%d][0]: %w", i, err)
				}
				fv, err := decodeWireValue(pair[1])
				if err != nil {
					return nil, fmt.Errorf("o[%d][1]: %w", i, err)
				}
				fields[i] = lkal.Field{Key: fk, Value: fv}
			}
			return lkal.Record{Fields: fields}, nil
		default:
			return nil, fmt.Errorf("unknown wire value tag %q", k)
		}
	}
	return nil, errors.New("unreachable")
}

func decodeWireRecord(raw json.RawMessage) (lkal.Record, error) {
	v, err := decodeWireValue(raw)
	if err != nil {
		return lkal.Record{}, err
	}
	rec, ok := v.(lkal.Record)
	if !ok {
		return lkal.Record{}, fmt.Errorf("doc must be a record, got %T", v)
	}
	return rec, nil
}

func decodeEnvelopeResult(line []byte) (envelopeResult, error) {
	trimmed := bytes.TrimSpace(line)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rawTop json.RawMessage
	if err := dec.Decode(&rawTop); err != nil {
		return envelopeResult{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return envelopeResult{}, fmt.Errorf("unexpected trailing data in result line: %v", err)
	}
	var top map[string]json.RawMessage
	if err := decodeStrictJSON(rawTop, &top); err != nil {
		return envelopeResult{}, err
	}
	rawID, hasID := top["id"]
	rawOK, hasOK := top["ok"]
	if !hasID || !hasOK {
		return envelopeResult{}, fmt.Errorf("result object missing id or ok: %s", rawTop)
	}
	id, err := decodeNonNegInt(rawID)
	if err != nil {
		return envelopeResult{}, fmt.Errorf("invalid id: %w", err)
	}
	var ok bool
	if err := decodeStrictJSON(rawOK, &ok); err != nil {
		return envelopeResult{}, fmt.Errorf("ok: %w", err)
	}
	if ok {
		rawDoc, hasDoc := top["doc"]
		if len(top) != 3 || !hasDoc {
			return envelopeResult{}, fmt.Errorf("ok=true result must have exact keys id, ok, doc: %s", rawTop)
		}
		doc, err := decodeWireRecord(rawDoc)
		if err != nil {
			return envelopeResult{}, fmt.Errorf("doc: %w", err)
		}
		return envelopeResult{ID: id, OK: true, Doc: doc}, nil
	}
	rawErr, hasErr := top["err"]
	if len(top) != 3 || !hasErr {
		return envelopeResult{}, fmt.Errorf("ok=false result must have exact keys id, ok, err: %s", rawTop)
	}
	errName, err := decodeString(rawErr)
	if err != nil {
		return envelopeResult{}, fmt.Errorf("err: %w", err)
	}
	if !slices.Contains(envErrClasses, errName) {
		return envelopeResult{}, fmt.Errorf("unknown error class %q", errName)
	}
	return envelopeResult{ID: id, OK: false, Err: errName}, nil
}

func formatWireDoc(doc lkal.Record) string {
	b, err := json.Marshal(wireValue{doc})
	if err != nil {
		return fmt.Sprintf("<marshal error: %v>", err)
	}
	return string(b)
}

func compareEnvelopeResult(want, got envelopeResult) string {
	switch {
	case want.ID != got.ID:
		return fmt.Sprintf("id mismatch: lean=%d go=%d", want.ID, got.ID)
	case want.OK != got.OK:
		return fmt.Sprintf("ok mismatch: lean=%v (err=%q) go=%v (err=%q)", want.OK, want.Err, got.OK, got.Err)
	case want.Err != got.Err:
		return fmt.Sprintf("err mismatch: lean=%q go=%q", want.Err, got.Err)
	case want.OK && !lkal.Equal(want.Doc, got.Doc):
		return fmt.Sprintf("doc mismatch:\nlean doc: %s\ngo   doc: %s", formatWireDoc(want.Doc), formatWireDoc(got.Doc))
	default:
		return ""
	}
}

func leanEnvelopeResults(t *testing.T, cases []envelopeCase) ([][]byte, [][]byte, []envelopeResult) {
	t.Helper()
	encoded := encodeEnvelopeCases(t, cases)
	lines, err := RunModel("envelope", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(lines), len(cases))
	}
	results := make([]envelopeResult, len(lines))
	for i, line := range lines {
		res, err := decodeEnvelopeResult(line)
		if err != nil {
			t.Fatalf("id=%d decode error: %v\ncase: %s\nlean: %s", cases[i].ID, err, encoded[i], line)
		}
		results[i] = res
	}
	return encoded, lines, results
}

func formatEnvelopeBuckets(buckets map[string]int) string {
	var parts []string
	for _, b := range envRequiredBuckets {
		parts = append(parts, fmt.Sprintf("%s=%d", b, buckets[b]))
	}
	return strings.Join(parts, " ")
}

func TestEnvelopeMatchesLeanModel(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateEnvelopeCases(cfg.Seed, cfg.Cases)
	encoded, leanLines, leanResults := leanEnvelopeResults(t, cases)

	repro := fmt.Sprintf("NAYUTAR_DRT_SEED=%d NAYUTAR_DRT_CASES=%d go test -tags drt -run '^TestEnvelopeMatchesLeanModel$' ./core/drt/...", cfg.Seed, cfg.Cases)
	buckets := map[string]int{}
	for i, tc := range cases {
		goRes, err := runEnvelopeCase(tc, envelopeHooks{})
		if err != nil {
			t.Fatalf("seed=%d id=%d go error: %v\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, err, encoded[i], leanLines[i], repro)
		}
		if diff := compareEnvelopeResult(leanResults[i], goRes); diff != "" {
			t.Fatalf("seed=%d id=%d mismatch (%s)\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, diff, encoded[i], leanLines[i], repro)
		}
		if goRes.OK {
			buckets[tc.Op+"/ok"]++
		} else {
			buckets[tc.Op+"/"+goRes.Err]++
		}
	}
	t.Logf("compared %d cases (seed=%d): %s", len(cases), cfg.Seed, formatEnvelopeBuckets(buckets))

	if cfg.Cases >= 100 {
		var missing []string
		for _, b := range envRequiredBuckets {
			if buckets[b] == 0 {
				missing = append(missing, b)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed=%d empty outcome buckets: %s", cfg.Seed, strings.Join(missing, ", "))
		}
	}
}

func TestEnvelopeCasesReachEveryOutcome(t *testing.T) {
	cases := generateEnvelopeCases(1, 1000)
	_, _, results := leanEnvelopeResults(t, cases)

	seen := map[string]int{}
	for i, tc := range cases {
		res := results[i]
		if res.OK {
			seen[tc.Op+"/ok"]++
			switch tc.Op {
			case "update":
				oldMeta, _ := envGetRecord(tc.Old, "meta")
				oldGen, _ := envGetField(oldMeta, "generation")
				newMeta, _ := envGetRecord(res.Doc, "meta")
				newGen, _ := envGetField(newMeta, "generation")
				if newGen == oldGen {
					seen["update/ok-gen-kept"]++
				} else {
					seen["update/ok-gen-bumped"]++
				}
			case "status":
				oldSt, _ := envGetRecord(tc.Old, "status")
				oldCond, _ := envGetRecord(oldSt, "condition")
				newSt, hasNewSt := envGetRecord(res.Doc, "status")
				if hasNewSt {
					newCond, _ := envGetRecord(newSt, "condition")
					for _, cf := range newCond.Fields {
						newC := cf.Value.(lkal.Record)
						newSince, _ := envGetField(newC, "since")
						if oldC, ok := envGetRecord(oldCond, cf.Key); ok {
							oldSince, _ := envGetField(oldC, "since")
							if newSince == oldSince {
								seen["status/ok-since-kept"]++
							} else {
								seen["status/ok-since-updated"]++
							}
						} else {
							seen["status/ok-since-new-cond"]++
						}
					}
				}
			}
		} else {
			seen[tc.Op+"/"+res.Err]++
		}
	}

	expected := append(slices.Clone(envRequiredBuckets),
		"update/ok-gen-kept",
		"update/ok-gen-bumped",
		"status/ok-since-kept",
		"status/ok-since-updated",
		"status/ok-since-new-cond",
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

func TestEnvelopeComparisonDetectsEveryFieldMutation(t *testing.T) {
	cases := generateEnvelopeCases(1, 200)
	_, leanLines, results := leanEnvelopeResults(t, cases)

	applied := map[string]int{}
	assertDetects := func(kind string, orig envelopeResult, mutate func(r *envelopeResult)) {
		t.Helper()
		mut := orig
		mut.Doc = envCloneRecord(orig.Doc)
		mutate(&mut)
		applied[kind]++
		if compareEnvelopeResult(orig, mut) == "" {
			t.Fatalf("mutation %q on case %d was not detected", kind, orig.ID)
		}
	}

	for _, orig := range results {
		assertDetects("id+1", orig, func(r *envelopeResult) { r.ID++ })
		assertDetects("flip-ok", orig, func(r *envelopeResult) { r.OK = !r.OK })
		if orig.OK {
			assertDetects("err-set", orig, func(r *envelopeResult) { r.Err = "internal" })
			assertDetects("doc-kind-changed", orig, func(r *envelopeResult) {
				meta, _ := envGetRecord(r.Doc, "meta")
				meta = envSetField(meta, "kind", lkal.String("other"))
				r.Doc = envSetField(r.Doc, "meta", meta)
			})
			assertDetects("doc-gen+1", orig, func(r *envelopeResult) {
				meta, _ := envGetRecord(r.Doc, "meta")
				g, _ := envGetField(meta, "generation")
				meta = envSetField(meta, "generation", lkal.Int(int64(g.(lkal.Int))+1))
				r.Doc = envSetField(r.Doc, "meta", meta)
			})
			assertDetects("doc-spec-changed", orig, func(r *envelopeResult) {
				r.Doc = envSetField(r.Doc, "spec", envRec(envField("mutated", lkal.Bool(true))))
			})
		} else {
			assertDetects("err-cleared", orig, func(r *envelopeResult) { r.Err = "" })
			assertDetects("err-changed", orig, func(r *envelopeResult) { r.Err += "x" })
		}
	}

	for _, kind := range []string{
		"id+1", "flip-ok", "err-set", "err-cleared", "err-changed",
		"doc-kind-changed", "doc-gen+1", "doc-spec-changed",
	} {
		if applied[kind] == 0 {
			t.Errorf("mutation %q was never applicable across %d cases", kind, len(cases))
		}
	}

	var firstOKLine []byte
	for _, line := range leanLines {
		if bytes.Contains(line, []byte(`"ok":true`)) {
			firstOKLine = line
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
		line string
	}{
		{"unknown top-level field", string(withTopExtra)},
		{"missing id", `{"ok":false,"err":"internal"}`},
		{"missing ok", `{"id":0,"err":"internal"}`},
		{"null id", `{"id":null,"ok":false,"err":"internal"}`},
		{"negative id", `{"id":-1,"ok":false,"err":"internal"}`},
		{"null ok", `{"id":0,"ok":null,"err":"internal"}`},
		{"ok=true missing doc", `{"id":0,"ok":true}`},
		{"ok=true with err field", `{"id":0,"ok":true,"doc":{"o":[]},"err":""}`},
		{"ok=false missing err", `{"id":0,"ok":false}`},
		{"ok=false with doc field", `{"id":0,"ok":false,"err":"internal","doc":{"o":[]}}`},
		{"ok=false unknown err", `{"id":0,"ok":false,"err":"boom"}`},
		{"ok=false null err", `{"id":0,"ok":false,"err":null}`},
		{"ok=true non-record doc", `{"id":0,"ok":true,"doc":{"s":"x"}}`},
		{"ok=true empty value obj", `{"id":0,"ok":true,"doc":{}}`},
		{"ok=true multi-key value obj", `{"id":0,"ok":true,"doc":{"o":[],"s":"x"}}`},
		{"ok=true bad int str", `{"id":0,"ok":true,"doc":{"o":[["x",{"i":"abc"}]]}}`},
		{"ok=true bad qty len", `{"id":0,"ok":true,"doc":{"o":[["x",{"q":["1"]}]]}}`},
		{"ok=true null list", `{"id":0,"ok":true,"doc":{"o":[["x",{"l":null}]]}}`},
		{"ok=true null record", `{"id":0,"ok":true,"doc":{"o":null}}`},
		{"ok=true bad field pair", `{"id":0,"ok":true,"doc":{"o":[["k"]]}}`},
		{"trailing object", `{"id":0,"ok":false,"err":"internal"}{"id":1,"ok":false,"err":"internal"}`},
	}
	for _, tc := range invalidLines {
		if _, err := decodeEnvelopeResult([]byte(tc.line)); err == nil {
			t.Errorf("decodeEnvelopeResult accepted %s: %s", tc.name, tc.line)
		}
	}

	validOK := `{"id":7,"ok":true,"doc":{"o":[["meta",{"o":[["kind",{"s":"pod"}],["name",{"s":"web"}],["uid",{"s":"01234567-89ab-cdef-0123-456789abcdef"}],["generation",{"i":"1"}]]}],["spec",{"o":[["d",{"d":"0.25"}],["b",{"b":true}],["e",{"e":"tcp"}],["q",{"q":["512","Mi"]}],["r",{"r":["secret","db"]}],["l",{"l":[{"i":"-12"}]}]]}]]}}`
	gotOK, err := decodeEnvelopeResult([]byte(validOK))
	if err != nil {
		t.Fatalf("decodeEnvelopeResult(%s): %v", validOK, err)
	}
	wantOK := envelopeResult{
		ID: 7,
		OK: true,
		Doc: envRec(
			envField("meta", envRec(
				envField("kind", lkal.String("pod")),
				envField("name", lkal.String("web")),
				envField("uid", lkal.String("01234567-89ab-cdef-0123-456789abcdef")),
				envField("generation", lkal.Int(1)),
			)),
			envField("spec", envRec(
				envField("d", lkal.Decimal("0.25")),
				envField("b", lkal.Bool(true)),
				envField("e", lkal.Enum("tcp")),
				envField("q", lkal.Quantity{Number: "512", Unit: "Mi"}),
				envField("r", lkal.Ref{"secret", "db"}),
				envField("l", lkal.List{lkal.Int(-12)}),
			)),
		),
	}
	if diff := compareEnvelopeResult(wantOK, gotOK); diff != "" {
		t.Fatalf("decodeEnvelopeResult(%s) differs from expected: %s", validOK, diff)
	}

	validErr := `{"id":8,"ok":false,"err":"observed"}`
	gotErr, err := decodeEnvelopeResult([]byte(validErr))
	if err != nil {
		t.Fatalf("decodeEnvelopeResult(%s): %v", validErr, err)
	}
	if diff := compareEnvelopeResult(envelopeResult{ID: 8, OK: false, Err: "observed"}, gotErr); diff != "" {
		t.Fatalf("decodeEnvelopeResult(%s) differs from expected: %s", validErr, diff)
	}
}

func TestEnvelopeDRTCatchesBrokenImplementations(t *testing.T) {
	cases := generateEnvelopeCases(1, 500)
	_, _, leanResults := leanEnvelopeResults(t, cases)

	variants := []struct {
		name  string
		hooks envelopeHooks
	}{
		{"create ignores bad uid", envelopeHooks{createIgnoreBadUID: true}},
		{"create allows status", envelopeHooks{createAllowStatus: true}},
		{"update ignores rev < 1", envelopeHooks{updateIgnoreNonPosRev: true}},
		{"update checks conflict before identity", envelopeHooks{updateConflictFirst: true}},
		{"update always bumps generation", envelopeHooks{updateAlwaysBumpGen: true}},
		{"update allows changing status", envelopeHooks{updateAllowStatusChange: true}},
		{"status ignores bad now", envelopeHooks{statusIgnoreBadNow: true}},
		{"status checks observed before forbidden", envelopeHooks{statusObservedFirst: true}},
		{"status always updates since", envelopeHooks{statusAlwaysUpdateSince: true}},
		{"status allows observedGeneration regression", envelopeHooks{statusAllowObsRegress: true}},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			mismatches := 0
			for i, tc := range cases {
				goRes, err := runEnvelopeCase(tc, v.hooks)
				if err != nil {
					t.Fatalf("id=%d: broken implementation failed instead of answering wrongly: %v", tc.ID, err)
				}
				if compareEnvelopeResult(leanResults[i], goRes) != "" {
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

func TestGenerateEnvelopeCasesIsDeterministic(t *testing.T) {
	a := bytes.Join(encodeEnvelopeCases(t, generateEnvelopeCases(1, 100)), []byte("\n"))
	b := bytes.Join(encodeEnvelopeCases(t, generateEnvelopeCases(1, 100)), []byte("\n"))
	c := bytes.Join(encodeEnvelopeCases(t, generateEnvelopeCases(2, 100)), []byte("\n"))

	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different encoded cases")
	}
	if bytes.Equal(a, c) {
		t.Fatal("different seeds produced identical encoded cases")
	}
}

func TestEnvelopeModelRejectsMalformedCase(t *testing.T) {
	valid := `{"id":0,"op":"create","cand":{"o":[["meta",{"o":[["kind",{"s":"pod"}],["name",{"s":"web"}]]}]]},"uid":"01234567-89ab-cdef-0123-456789abcdef"}`
	for _, bad := range []string{
		`{"id":1,"op":"explode"}`,
		`{"id":1,"op":"create","cand":{"o":[]}}`,
		`{"id":1,"op":"update","old":{"o":[]},"rev":1,"cand":{"o":[]}}`,
		`{"id":1,"op":"update","old":{"o":[]},"rev":"abc","cand":{"o":[]}}`,
		`{"id":1,"op":"status","old":{"o":[]},"rev":"1","cand":{"o":[]}}`,
		`{"id":1,"op":"create","cand":{"x":"bad"},"uid":"01234567-89ab-cdef-0123-456789abcdef"}`,
	} {
		cases := [][]byte{[]byte(valid), []byte(bad)}
		_, err := RunModel("envelope", cases)
		if err == nil {
			t.Errorf("RunModel accepted malformed case %s", bad)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte("line 2")) {
			t.Errorf("error %q does not name line 2 for %s", err.Error(), bad)
		}
	}
}
