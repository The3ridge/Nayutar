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
	"strings"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

type refsObjWire struct {
	Group string `json:"group"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
}

type refsSchemaRuleWire struct {
	Source  string   `json:"source"`
	Path    []string `json:"path"`
	Targets []string `json:"targets"`
}

type refsOccWire struct {
	Path string   `json:"path"`
	Ref  []string `json:"ref"`
}

type refsCase struct {
	Op        string
	Source    keyspace.Object
	Schema    refs.Schema
	Stored    lkal.Record
	HasStored bool
	Next      lkal.Record
	Allow     []refs.Occurrence
	Present   []keyspace.Object
}

func nonNilStrings(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func toRefsObjWire(o keyspace.Object) refsObjWire {
	return refsObjWire{Group: o.Group, Kind: o.Kind, Name: o.Name}
}

func encodeRefsSchema(s refs.Schema) []refsSchemaRuleWire {
	sources := make([]string, 0, len(s))
	for k := range s {
		sources = append(sources, k)
	}
	slices.Sort(sources)

	out := make([]refsSchemaRuleWire, 0)
	for _, src := range sources {
		for _, r := range s[src] {
			out = append(out, refsSchemaRuleWire{
				Source:  src,
				Path:    nonNilStrings(r.Path),
				Targets: nonNilStrings(r.Targets),
			})
		}
	}
	return out
}

func (c refsCase) MarshalJSON() ([]byte, error) {
	switch c.Op {
	case "write":
		var stored *wireValue
		if c.HasStored {
			w := wireValue{c.Stored}
			stored = &w
		}
		allow := make([]refsOccWire, len(c.Allow))
		for i, occ := range c.Allow {
			allow[i] = refsOccWire{
				Path: occ.Path,
				Ref:  nonNilStrings(occ.Ref),
			}
		}
		present := make([]refsObjWire, len(c.Present))
		for i, obj := range c.Present {
			present[i] = toRefsObjWire(obj)
		}
		return json.Marshal(struct {
			Op      string               `json:"op"`
			Source  refsObjWire          `json:"source"`
			Schema  []refsSchemaRuleWire `json:"schema"`
			Stored  *wireValue           `json:"stored"`
			Next    wireValue            `json:"next"`
			Allow   []refsOccWire        `json:"allow"`
			Present []refsObjWire        `json:"present"`
		}{
			Op:      c.Op,
			Source:  toRefsObjWire(c.Source),
			Schema:  encodeRefsSchema(c.Schema),
			Stored:  stored,
			Next:    wireValue{c.Next},
			Allow:   allow,
			Present: present,
		})
	case "delete":
		return json.Marshal(struct {
			Op     string    `json:"op"`
			Stored wireValue `json:"stored"`
		}{
			Op:     c.Op,
			Stored: wireValue{c.Stored},
		})
	default:
		return nil, fmt.Errorf("unknown refs op %q", c.Op)
	}
}

type refsResult struct {
	OK      bool
	Put     []keyspace.Object
	Delete  []keyspace.Object
	Compare []keyspace.Object
	Err     string
	Path    *string
	Ref     []string
}

func toRefsObjWires(objs []keyspace.Object) []refsObjWire {
	out := make([]refsObjWire, len(objs))
	for i, o := range objs {
		out[i] = toRefsObjWire(o)
	}
	return out
}

func (r refsResult) MarshalJSON() ([]byte, error) {
	if r.OK {
		return json.Marshal(struct {
			OK      bool          `json:"ok"`
			Put     []refsObjWire `json:"put"`
			Delete  []refsObjWire `json:"delete"`
			Compare []refsObjWire `json:"compare"`
		}{
			OK:      true,
			Put:     toRefsObjWires(r.Put),
			Delete:  toRefsObjWires(r.Delete),
			Compare: toRefsObjWires(r.Compare),
		})
	}
	return json.Marshal(struct {
		OK    bool     `json:"ok"`
		Error string   `json:"error"`
		Path  *string  `json:"path"`
		Ref   []string `json:"ref"`
	}{
		OK:    false,
		Error: r.Err,
		Path:  r.Path,
		Ref:   r.Ref,
	})
}

var (
	refsErrClasses = []string{"internal", "malformed", "kind", "denied", "notFound"}

	refsRequiredBuckets = []string{
		"write/ok",
		"write/internal",
		"write/malformed",
		"write/kind",
		"write/denied",
		"write/notFound",
		"delete/ok",
		"delete/internal",
	}

	refsSourceObjs = []keyspace.Object{
		{Group: keyspace.CoreGroup, Kind: "workload", Name: "web"},
		{Group: keyspace.CoreGroup, Kind: "pod", Name: "worker-0"},
		{Group: "batch.example.com", Kind: "job", Name: "nightly"},
		{Group: "apps.example.com", Kind: "app", Name: "api.v1"},
	}

	refsTargetKinds = []string{
		"secret",
		"configmap",
		"workload",
		"pod",
		"certificate.cert.example.com",
		"widget.example.com",
		"job.batch.example.com",
	}

	refsSmallNames = []string{"db", "web", "cfg", "worker-0", "tls.v1", "api.v1"}

	refsManyNames = func() []string {
		out := make([]string, 42)
		for i := range out {
			out[i] = fmt.Sprintf("t%02d", i)
		}
		return out
	}()

	refsSpecKeys    = []string{"db", "secret", "volumes", "items", "nested", "extra", "auth", "self", "cfg", "ports"}
	refsBadKeys     = []string{"", "a/b", "db/password", "/lead", "trail/"}
	refsLeafRoots   = []string{"meta", "spec", "status"}
	refsLeafSegs    = []string{"uid", "issuer", "0", "data", "Ready", "host", "port"}
	refsBadLeafSegs = []string{"", "$ref", "$", "a/b", "x/y"}

	refsBrokenValues = []lkal.Ref{
		{},
		{"secret"},
		{"secret.core", "x"},
		{"Secret", "x"},
		{"widget.apps", "x"},
		{"..", "x"},
		{"a/b", "x"},
		{"", "x"},
		{"widget.Apps.example.com", "x"},
		{"secret", "Db"},
		{"secret", "a..b"},
		{"secret", ".."},
		{"secret", "-db"},
		{"secret", "db-"},
		{"secret", ""},
		{"secret", "a/b"},
		{"secret", "a_b"},
		{"secret", "x", "o"},
		{"secret", "x", "o", "core"},
		{"secret", "x", "other", "y"},
		{"secret", "x", "spec", ""},
		{"secret", "x", "meta", "$ref"},
		{"secret", "x", "status", "$"},
		{"secret", "x", "spec", "a/b"},
		{"t", "other", "o", "core", "secret", "x"},
	}
)

func refsObjQKind(o keyspace.Object) string {
	if o.Group == keyspace.CoreGroup || o.Group == "" {
		return o.Kind
	}
	return o.Kind + "." + o.Group
}

func refsParseQKind(qk string) (string, string) {
	if kind, group, ok := strings.Cut(qk, "."); ok {
		return group, kind
	}
	return keyspace.CoreGroup, qk
}

func genRefsValidRef(rng *rand.Rand, source keyspace.Object, useManyNames bool) lkal.Ref {
	if chance(rng, 12) {
		// Self reference (with optional leaf).
		ref := lkal.Ref{refsObjQKind(source), source.Name}
		if chance(rng, 30) {
			ref = append(ref, genRefsValidLeaf(rng)...)
		}
		return ref
	}
	qkind := pick(rng, refsTargetKinds)
	name := pick(rng, refsSmallNames)
	if useManyNames {
		name = pick(rng, refsManyNames)
	}
	ref := lkal.Ref{qkind, name}
	if chance(rng, 35) {
		ref = append(ref, genRefsValidLeaf(rng)...)
	}
	return ref
}

func genRefsValidLeaf(rng *rand.Rand) []string {
	leaf := []string{pick(rng, refsLeafRoots)}
	extra := rng.IntN(3)
	for range extra {
		leaf = append(leaf, pick(rng, refsLeafSegs))
	}
	return leaf
}

func genRefsBrokenRef(rng *rand.Rand) lkal.Ref {
	if chance(rng, 75) {
		return slices.Clone(pick(rng, refsBrokenValues))
	}
	qkind := pick(rng, refsTargetKinds)
	name := pick(rng, refsSmallNames)
	switch rng.IntN(3) {
	case 0:
		return lkal.Ref{qkind, name, "o", pick(rng, refsLeafSegs)}
	case 1:
		return lkal.Ref{qkind, name, pick(rng, refsLeafRoots), pick(rng, refsBadLeafSegs)}
	default:
		return lkal.Ref{qkind, name, "spec", "ok", pick(rng, refsBadLeafSegs)}
	}
}

func genRefsNonRefValue(rng *rand.Rand) lkal.Value {
	switch rng.IntN(6) {
	case 0:
		return lkal.String(pick(rng, []string{"web", "café", ""}))
	case 1:
		return lkal.Int(pick(rng, []int64{-5, 0, 1, 42}))
	case 2:
		return lkal.Decimal("0.25")
	case 3:
		return lkal.Bool(rng.IntN(2) == 0)
	case 4:
		return lkal.Enum(pick(rng, []string{"tcp", "udp"}))
	default:
		return lkal.Quantity{Number: "512", Unit: "Mi"}
	}
}

func genRefsSubtree(rng *rand.Rand, depth int, source keyspace.Object, allowBroken, allowBadKeys, useManyNames bool) lkal.Value {
	if depth >= 3 {
		if chance(rng, 75) {
			if allowBroken && chance(rng, 35) {
				return genRefsBrokenRef(rng)
			}
			return genRefsValidRef(rng, source, useManyNames)
		}
		return genRefsNonRefValue(rng)
	}
	switch rng.IntN(10) {
	case 0, 1, 2, 3:
		if allowBroken && chance(rng, 30) {
			return genRefsBrokenRef(rng)
		}
		return genRefsValidRef(rng, source, useManyNames)
	case 4, 5:
		n := rng.IntN(4)
		items := make(lkal.List, n)
		for i := range n {
			items[i] = genRefsSubtree(rng, depth+1, source, allowBroken, allowBadKeys, useManyNames)
		}
		return items
	case 6, 7, 8:
		keys := slices.Clone(refsSpecKeys)
		rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
		count := 1 + rng.IntN(3)
		fields := make([]lkal.Field, count)
		for i := range count {
			k := keys[i]
			if allowBadKeys && i == 0 && chance(rng, 35) {
				k = pick(rng, refsBadKeys)
			}
			fields[i] = envField(k, genRefsSubtree(rng, depth+1, source, allowBroken, allowBadKeys, useManyNames))
		}
		return lkal.Record{Fields: fields}
	default:
		return genRefsNonRefValue(rng)
	}
}

func genRefsBaseMeta(rng *rand.Rand, source keyspace.Object) lkal.Record {
	fields := []lkal.Field{
		envField("kind", lkal.String(source.Kind)),
		envField("name", lkal.String(source.Name)),
	}
	if source.Group != keyspace.CoreGroup && source.Group != "" {
		fields = append(fields, envField("group", lkal.String(source.Group)))
	}
	if chance(rng, 40) {
		fields = append(fields, envField("labels", genEnvLabels(rng)))
	}
	return lkal.Record{Fields: fields}
}

func genRefsDoc(rng *rand.Rand, source keyspace.Object, allowBroken, allowBadKeys, allowOutsideSpec, useManyNames bool) lkal.Record {
	meta := genRefsBaseMeta(rng, source)
	spec := envRec()
	if specVal, ok := genRefsSubtree(rng, 1, source, allowBroken, allowBadKeys, useManyNames).(lkal.Record); ok {
		spec = specVal
	} else {
		spec = envRec(envField(pick(rng, refsSpecKeys), genRefsValidRef(rng, source, useManyNames)))
	}

	fields := []lkal.Field{
		envField("meta", meta),
		envField("spec", spec),
	}

	if chance(rng, 45) {
		stFields := []lkal.Field{
			envField("observedGeneration", lkal.Int(1+rng.Int64N(3))),
			envField("phase", lkal.String(pick(rng, []string{"Ready", "Active"}))),
		}
		if allowOutsideSpec && chance(rng, 55) {
			stFields = append(stFields, envField("target", genRefsSubtree(rng, 2, source, allowBroken, allowBadKeys, useManyNames)))
		}
		fields = append(fields, envField("status", lkal.Record{Fields: stFields}))
	}

	if allowOutsideSpec && chance(rng, 35) {
		switch rng.IntN(4) {
		case 0:
			meta = envSetField(meta, "bad", genRefsValidRef(rng, source, false))
			fields[0] = envField("meta", meta)
		case 1:
			fields[1] = envField("spec", genRefsValidRef(rng, source, false))
		case 2:
			fields = append(fields, envField("extra", genRefsValidRef(rng, source, false)))
		default:
			fields = append(fields, envField("spec/a", genRefsValidRef(rng, source, false)))
		}
	}

	return envShuffleRecord(rng, lkal.Record{Fields: fields})
}

func genRefsManyTargetsDoc(rng *rand.Rand, source keyspace.Object, targetCount int) lkal.Record {
	specFields := make([]lkal.Field, 0, targetCount+3)
	for i := range targetCount {
		name := refsManyNames[i]
		qkind := "secret"
		if chance(rng, 30) {
			qkind = pick(rng, refsTargetKinds)
		}
		specFields = append(specFields, envField(fmt.Sprintf("f%02d", i), lkal.Ref{qkind, name}))
	}
	// Add duplicates and leaf variations pointing at already-included targets.
	if targetCount > 0 && chance(rng, 60) {
		specFields = append(specFields, envField("dup", lkal.List{
			lkal.Ref{"secret", refsManyNames[0]},
			lkal.Ref{"secret", refsManyNames[0], "spec", "k"},
		}))
	}
	doc := envRec(
		envField("meta", genRefsBaseMeta(rng, source)),
		envField("spec", lkal.Record{Fields: specFields}),
	)
	return envShuffleRecord(rng, doc)
}

type refsCollectedOcc struct {
	occ    refs.Occurrence
	keys   []string
	badKey bool
}

func collectRefsWellFormed(doc lkal.Record) []refsCollectedOcc {
	var all []refsCollectedOcc
	var walkRec func(rec lkal.Record, keys []string, badKey bool)
	var walkVal func(v lkal.Value, keys []string, badKey bool)
	walkRec = func(rec lkal.Record, keys []string, badKey bool) {
		for _, f := range rec.Fields {
			nextKeys := make([]string, len(keys)+1)
			copy(nextKeys, keys)
			nextKeys[len(keys)] = f.Key
			nextBad := badKey || f.Key == "" || strings.Contains(f.Key, "/")
			walkVal(f.Value, nextKeys, nextBad)
		}
	}
	walkVal = func(v lkal.Value, keys []string, badKey bool) {
		switch val := v.(type) {
		case lkal.Ref:
			all = append(all, refsCollectedOcc{
				occ: refs.Occurrence{
					Path: strings.Join(keys, "/"),
					Ref:  slices.Clone(val),
				},
				keys:   keys,
				badKey: badKey,
			})
		case lkal.Record:
			walkRec(val, keys, badKey)
		case lkal.List:
			for _, elem := range val {
				walkVal(elem, keys, badKey)
			}
		}
	}
	walkRec(doc, nil, false)
	slices.SortFunc(all, func(a, b refsCollectedOcc) int {
		if c := strings.Compare(a.occ.Path, b.occ.Path); c != 0 {
			return c
		}
		if c := slices.Compare(a.occ.Ref, b.occ.Ref); c != 0 {
			return c
		}
		return slices.Compare(a.keys, b.keys)
	})
	return slices.CompactFunc(all, func(a, b refsCollectedOcc) bool {
		return slices.Equal(a.keys, b.keys) && slices.Equal(a.occ.Ref, b.occ.Ref)
	})
}

func deriveDocWithMutatedRefs(rng *rand.Rand, base lkal.Record, source keyspace.Object) lkal.Record {
	doc := envCloneRecord(base)
	spec, ok := envGetRecord(doc, "spec")
	if !ok {
		return doc
	}
	fields := slices.Clone(spec.Fields)
	// Remove some fields.
	if len(fields) > 0 && chance(rng, 60) {
		dropIdx := rng.IntN(len(fields))
		fields = slices.Delete(fields, dropIdx, dropIdx+1)
	}
	// Move a field containing a ref to a new key name.
	if len(fields) > 0 && chance(rng, 65) {
		mvIdx := rng.IntN(len(fields))
		newKey := pick(rng, refsSpecKeys)
		for _, f := range fields {
			if f.Key == newKey {
				newKey = newKey + "-moved"
				break
			}
		}
		fields[mvIdx].Key = newKey
	}
	// Add 1 or 2 new ref fields.
	adds := rng.IntN(3)
	for i := range adds {
		k := fmt.Sprintf("%s-%d", pick(rng, refsSpecKeys), i)
		fields = append(fields, envField(k, genRefsValidRef(rng, source, false)))
	}
	doc = envSetField(doc, "spec", lkal.Record{Fields: fields})
	return envShuffleRecord(rng, doc)
}

func buildRulesForCollected(rng *rand.Rand, collected []refsCollectedOcc, allowAll bool) []refs.Rule {
	ruleMap := map[string][]string{}
	var ruleOrder []string
	addRuleTarget := func(patKey string, target string) {
		if _, ok := ruleMap[patKey]; !ok {
			ruleOrder = append(ruleOrder, patKey)
		}
		if !slices.Contains(ruleMap[patKey], target) {
			ruleMap[patKey] = append(ruleMap[patKey], target)
		}
	}

	for _, c := range collected {
		if c.badKey || len(c.keys) < 2 || c.keys[0] != "spec" {
			continue
		}
		parsed, err := refs.Parse(c.occ.Ref)
		if err != nil {
			continue
		}
		tgtQKind := refsObjQKind(parsed.Object)
		pat := slices.Clone(c.keys)
		if len(pat) >= 3 && chance(rng, 45) {
			pat[1+rng.IntN(len(pat)-1)] = "*"
		}
		patKey := strings.Join(pat, "/")
		if allowAll || chance(rng, 75) {
			addRuleTarget(patKey, tgtQKind)
			if chance(rng, 35) {
				addRuleTarget(patKey, pick(rng, refsTargetKinds))
			}
		} else if chance(rng, 50) {
			// Include the path rule, but exclude tgtQKind.
			other := pick(rng, refsTargetKinds)
			if other != tgtQKind {
				addRuleTarget(patKey, other)
			} else if _, ok := ruleMap[patKey]; !ok {
				ruleOrder = append(ruleOrder, patKey)
				ruleMap[patKey] = []string{}
			}
		}
	}

	if len(ruleOrder) == 0 && allowAll {
		ruleOrder = append(ruleOrder, "spec/*")
		ruleMap["spec/*"] = slices.Clone(refsTargetKinds)
	}

	// Sort patterns deterministically, then shuffle or keep ordered.
	slices.Sort(ruleOrder)
	rules := make([]refs.Rule, 0, len(ruleOrder))
	for _, patKey := range ruleOrder {
		tgts := ruleMap[patKey]
		slices.Sort(tgts)
		rules = append(rules, refs.Rule{
			Path:    strings.Split(patKey, "/"),
			Targets: tgts,
		})
	}
	if len(rules) > 1 && chance(rng, 50) {
		rng.Shuffle(len(rules), func(i, j int) { rules[i], rules[j] = rules[j], rules[i] })
	}
	return rules
}

func buildSchemaForDoc(rng *rand.Rand, source keyspace.Object, doc lkal.Record, allowAll bool) refs.Schema {
	srcQKind := refsObjQKind(source)
	collected := collectRefsWellFormed(doc)
	schema := refs.Schema{}

	if !allowAll && chance(rng, 15) {
		// Omit source kind from schema or leave empty rule list.
		if chance(rng, 50) {
			schema[srcQKind] = []refs.Rule{}
		}
	} else {
		schema[srcQKind] = buildRulesForCollected(rng, collected, allowAll)
	}

	// Add rules for other source kinds (including rules allowing the drawn target kinds at doc's paths)
	// so implementations that ignore the source kind in Schema.Allows are caught.
	otherKinds := make([]string, 0, len(refsSourceObjs))
	for _, obj := range refsSourceObjs {
		qk := refsObjQKind(obj)
		if qk != srcQKind && !slices.Contains(otherKinds, qk) {
			otherKinds = append(otherKinds, qk)
		}
	}
	rng.Shuffle(len(otherKinds), func(i, j int) { otherKinds[i], otherKinds[j] = otherKinds[j], otherKinds[i] })
	otherCount := 1 + rng.IntN(len(otherKinds))
	for _, otherQK := range otherKinds[:otherCount] {
		schema[otherQK] = buildRulesForCollected(rng, collected, true)
	}

	return schema
}

func buildAllowAndPresent(rng *rand.Rand, source keyspace.Object, stored lkal.Record, hasStored bool, next lkal.Record, allowPct, presentPct int) ([]refs.Occurrence, []keyspace.Object) {
	var allOccs []refsCollectedOcc
	allOccs = append(allOccs, collectRefsWellFormed(next)...)
	if hasStored {
		allOccs = append(allOccs, collectRefsWellFormed(stored)...)
	}

	var allow []refs.Occurrence
	seenOcc := map[string]bool{}
	targetSet := map[keyspace.Object]bool{source: chance(rng, presentPct)}
	for _, c := range allOccs {
		if c.badKey {
			continue
		}
		key := c.occ.Path + "\x00" + strings.Join(c.occ.Ref, "\x00")
		if !seenOcc[key] {
			seenOcc[key] = true
			if chance(rng, allowPct) {
				allow = append(allow, refs.Occurrence{
					Path: c.occ.Path,
					Ref:  slices.Clone(c.occ.Ref),
				})
			}
		}
		if parsed, err := refs.Parse(c.occ.Ref); err == nil {
			if _, exists := targetSet[parsed.Object]; !exists {
				targetSet[parsed.Object] = chance(rng, presentPct)
			}
		}
	}

	var present []keyspace.Object
	for obj, ok := range targetSet {
		if ok {
			present = append(present, obj)
		}
	}
	slices.SortFunc(present, func(a, b keyspace.Object) int {
		if c := strings.Compare(refsObjQKind(a), refsObjQKind(b)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(allow) > 1 && chance(rng, 50) {
		rng.Shuffle(len(allow), func(i, j int) { allow[i], allow[j] = allow[j], allow[i] })
	}
	if len(present) > 1 && chance(rng, 50) {
		rng.Shuffle(len(present), func(i, j int) { present[i], present[j] = present[j], present[i] })
	}
	return allow, present
}

func genWriteRefsCase(rng *rand.Rand) refsCase {
	source := pick(rng, refsSourceObjs)
	var (
		stored    lkal.Record
		hasStored bool
		next      lkal.Record
		schema    refs.Schema
		allow     []refs.Occurrence
		present   []keyspace.Object
	)

	r := rng.IntN(100)
	switch {
	case r < 24:
		// Happy path (or nearly happy) write with valid docs, schema, allow, present.
		next = genRefsDoc(rng, source, false, false, false, false)
		hasStored = chance(rng, 60)
		if hasStored {
			if chance(rng, 75) {
				stored = deriveDocWithMutatedRefs(rng, next, source)
			} else {
				stored = genRefsDoc(rng, source, false, false, false, false)
			}
		}
		schema = buildSchemaForDoc(rng, source, next, true)
		allow, present = buildAllowAndPresent(rng, source, stored, hasStored, next, 100, 100)
	case r < 38:
		// Phase 1 internal: bad stored doc (outside spec, bad key, broken ref, or >32 targets).
		hasStored = true
		if chance(rng, 35) {
			stored = genRefsManyTargetsDoc(rng, source, 33+rng.IntN(8))
			if chance(rng, 50) {
				// Next reduces targets below 32.
				if chance(rng, 50) {
					next = genRefsDoc(rng, source, false, false, false, false)
				} else {
					next = deriveDocWithMutatedRefs(rng, stored, source)
					if spec, ok := envGetRecord(next, "spec"); ok {
						keep := 5 + rng.IntN(10)
						if len(spec.Fields) > keep {
							spec.Fields = spec.Fields[:keep]
							next = envSetField(next, "spec", spec)
						}
					}
				}
			} else {
				// Next also stays above 32 targets.
				if chance(rng, 50) {
					next = genRefsManyTargetsDoc(rng, source, 33+rng.IntN(8))
				} else {
					next = deriveDocWithMutatedRefs(rng, stored, source)
				}
			}
		} else {
			stored = genRefsDoc(rng, source, true, true, true, false)
			next = genRefsDoc(rng, source, chance(rng, 25), chance(rng, 25), chance(rng, 25), false)
		}
		schema = buildSchemaForDoc(rng, source, next, true)
		allow, present = buildAllowAndPresent(rng, source, stored, hasStored, next, 85, 85)
	case r < 56:
		// Phase 2/3/4 malformed: valid/null stored, bad next (outside spec, bad key, broken ref, or >32 targets).
		if chance(rng, 45) {
			hasStored = true
			stored = genRefsDoc(rng, source, false, false, false, false)
		}
		switch rng.IntN(3) {
		case 0:
			// >32 targets (up to 40) or exact 32 boundary.
			count := 32 + rng.IntN(9)
			next = genRefsManyTargetsDoc(rng, source, count)
			if hasStored && chance(rng, 50) {
				stored = deriveDocWithMutatedRefs(rng, next, source)
				if spec, ok := envGetRecord(stored, "spec"); ok && len(spec.Fields) > 5 {
					spec.Fields = spec.Fields[:5]
					stored = envSetField(stored, "spec", spec)
				}
			}
		case 1:
			// Outside spec or bad keys.
			next = genRefsDoc(rng, source, false, true, true, false)
		default:
			// Broken ref grammar in spec.
			next = genRefsDoc(rng, source, true, false, false, false)
		}
		schema = buildSchemaForDoc(rng, source, next, true)
		allow, present = buildAllowAndPresent(rng, source, stored, hasStored, next, 85, 85)
	case r < 70:
		// Phase 5 kind rejection (plus unchanged legacy ref in stored sometimes).
		next = genRefsDoc(rng, source, false, false, false, false)
		if chance(rng, 55) {
			hasStored = true
			stored = deriveDocWithMutatedRefs(rng, next, source)
		}
		schema = buildSchemaForDoc(rng, source, next, false)
		allow, present = buildAllowAndPresent(rng, source, stored, hasStored, next, 85, 85)
	case r < 85:
		// Phase 6 denied (with mixed present so denied > notFound precedence is tested).
		next = genRefsDoc(rng, source, false, false, false, false)
		if chance(rng, 55) {
			hasStored = true
			stored = deriveDocWithMutatedRefs(rng, next, source)
		}
		schema = buildSchemaForDoc(rng, source, next, true)
		allow, present = buildAllowAndPresent(rng, source, stored, hasStored, next, 35, 55)
	default:
		// Phase 7 notFound (allow all, partial present).
		next = genRefsDoc(rng, source, false, false, false, false)
		if chance(rng, 55) {
			hasStored = true
			stored = deriveDocWithMutatedRefs(rng, next, source)
		}
		schema = buildSchemaForDoc(rng, source, next, true)
		allow, present = buildAllowAndPresent(rng, source, stored, hasStored, next, 100, 45)
	}

	return refsCase{
		Op:        "write",
		Source:    source,
		Schema:    schema,
		Stored:    stored,
		HasStored: hasStored,
		Next:      next,
		Allow:     allow,
		Present:   present,
	}
}

func genDeleteRefsCase(rng *rand.Rand) refsCase {
	source := pick(rng, refsSourceObjs)
	var stored lkal.Record
	switch rng.IntN(100) {
	case 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19:
		// 20%: boundary around 32 targets (31..40).
		stored = genRefsManyTargetsDoc(rng, source, 31+rng.IntN(10))
	default:
		if chance(rng, 55) {
			stored = genRefsDoc(rng, source, false, false, false, false)
		} else {
			stored = genRefsDoc(rng, source, true, true, true, false)
		}
	}
	return refsCase{
		Op:     "delete",
		Stored: stored,
	}
}

func generateRefsCases(seed uint64, n int) []refsCase {
	rng := rand.New(rand.NewPCG(seed, 0x7265667364727431))
	cases := make([]refsCase, n)
	for i := range n {
		if chance(rng, 22) {
			cases[i] = genDeleteRefsCase(rng)
		} else {
			cases[i] = genWriteRefsCase(rng)
		}
	}
	return cases
}

func encodeRefsCases(t *testing.T, cases []refsCase) [][]byte {
	t.Helper()
	encoded := make([][]byte, len(cases))
	for i, tc := range cases {
		switch tc.Op {
		case "write":
			assertValidUTF8(t, i, "source.group", tc.Source.Group)
			assertValidUTF8(t, i, "source.kind", tc.Source.Kind)
			assertValidUTF8(t, i, "source.name", tc.Source.Name)
			if tc.HasStored {
				assertValidWireValueUTF8(t, i, "stored", tc.Stored)
			}
			assertValidWireValueUTF8(t, i, "next", tc.Next)
		case "delete":
			assertValidWireValueUTF8(t, i, "stored", tc.Stored)
		default:
			t.Fatalf("case %d unknown op %q", i, tc.Op)
		}
		b, err := json.Marshal(tc)
		if err != nil {
			t.Fatalf("case %d marshal error: %v", i, err)
		}
		encoded[i] = b
	}
	return encoded
}

type refsHooks struct {
	ignoreStored           bool
	ignoresStoredTargetCap bool
	allowOutsideSpec       bool
	diffByRefValueOnly     bool
	ignoreTargetLimit      bool
	allowAllSchemaKinds    bool
	ignoresSchemaSource    bool
	checkNotFoundFirst     bool
	recheckUnchanged       bool
	omitSelfFromDelta      bool
	ignoreDeleteTargetCap  bool
}

func stripOutsideSpecRefs(doc lkal.Record) lkal.Record {
	var stripVal func(v lkal.Value) lkal.Value
	stripVal = func(v lkal.Value) lkal.Value {
		switch val := v.(type) {
		case lkal.Ref:
			return lkal.String("stripped")
		case lkal.List:
			out := make(lkal.List, len(val))
			for i, item := range val {
				out[i] = stripVal(item)
			}
			return out
		case lkal.Record:
			out := make([]lkal.Field, len(val.Fields))
			for i, f := range val.Fields {
				out[i] = envField(f.Key, stripVal(f.Value))
			}
			return lkal.Record{Fields: out}
		default:
			return v
		}
	}
	out := make([]lkal.Field, len(doc.Fields))
	for i, f := range doc.Fields {
		if f.Key == "spec" {
			out[i] = f
		} else {
			out[i] = envField(f.Key, stripVal(f.Value))
		}
	}
	return lkal.Record{Fields: out}
}

func allowContains(allow []refs.Occurrence, occ refs.Occurrence) bool {
	for _, a := range allow {
		if a.Path == occ.Path && slices.Equal(a.Ref, occ.Ref) {
			return true
		}
	}
	return false
}

func runRefsCase(tc refsCase, hooks refsHooks) (refsResult, error) {
	var (
		delta refs.Delta
		err   error
	)
	switch tc.Op {
	case "write":
		stored := tc.Stored
		hasStored := tc.HasStored
		next := tc.Next
		schema := tc.Schema
		if hooks.ignoreStored {
			hasStored = false
			stored = lkal.Record{}
		}
		if hooks.ignoresStoredTargetCap && hasStored {
			if spec, ok := envGetRecord(stored, "spec"); ok && len(spec.Fields) > refs.MaxTargets {
				spec.Fields = spec.Fields[:refs.MaxTargets]
				stored = envSetField(stored, "spec", spec)
			}
		}
		if hooks.allowOutsideSpec {
			next = stripOutsideSpecRefs(next)
		}
		if hooks.ignoreTargetLimit {
			if spec, ok := envGetRecord(next, "spec"); ok && len(spec.Fields) > refs.MaxTargets {
				spec.Fields = spec.Fields[:refs.MaxTargets]
				next = envSetField(next, "spec", spec)
			}
		}
		if hooks.ignoresSchemaSource {
			srcQK := refsObjQKind(tc.Source)
			var merged []refs.Rule
			keys := make([]string, 0, len(schema))
			for k := range schema {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				merged = append(merged, schema[k]...)
			}
			schema = refs.Schema{srcQK: merged}
		}
		if hooks.allowAllSchemaKinds {
			srcQK := refsObjQKind(tc.Source)
			rules := append([]refs.Rule{}, schema[srcQK]...)
			for depth := 1; depth <= 4; depth++ {
				pat := make([]string, 1+depth)
				pat[0] = "spec"
				for i := 1; i < len(pat); i++ {
					pat[i] = "*"
				}
				rules = append(rules, refs.Rule{Path: pat, Targets: slices.Clone(refsTargetKinds)})
			}
			schema = refs.Schema{srcQK: rules}
		}

		presentSet := make(map[keyspace.Object]bool, len(tc.Present))
		for _, obj := range tc.Present {
			presentSet[obj] = true
		}
		authorize := func(occ refs.Occurrence) bool {
			return allowContains(tc.Allow, occ)
		}
		exists := func(obj keyspace.Object) bool {
			return presentSet[obj]
		}

		if hooks.diffByRefValueOnly && hasStored {
			// Pretend any ref value present anywhere in stored was already at its next path too.
			storedOccs := refs.Occurrences(stored)
			storedRefs := make([]lkal.Ref, len(storedOccs))
			for i, o := range storedOccs {
				storedRefs[i] = o.Ref
			}
			hasRef := func(r lkal.Ref) bool {
				for _, sr := range storedRefs {
					if slices.Equal(sr, r) {
						return true
					}
				}
				return false
			}
			innerAuth := authorize
			authorize = func(occ refs.Occurrence) bool {
				if hasRef(occ.Ref) {
					return true
				}
				return innerAuth(occ)
			}
		}

		if hooks.recheckUnchanged && hasStored {
			// Run Prepare without stored first to trigger checks on unchanged refs too.
			var planNoStored refs.Plan
			planNoStored, err = refs.Prepare(refs.Input{
				Source: tc.Source,
				Schema: schema,
				Next:   next,
			})
			if err == nil {
				_, err = planNoStored.Check(authorize, exists)
			}
			if err == nil {
				var plan refs.Plan
				plan, err = refs.Prepare(refs.Input{
					Source:    tc.Source,
					Schema:    schema,
					Stored:    stored,
					HasStored: hasStored,
					Next:      next,
				})
				if err == nil {
					delta, err = plan.Check(authorize, exists)
				}
			}
		} else {
			var plan refs.Plan
			plan, err = refs.Prepare(refs.Input{
				Source:    tc.Source,
				Schema:    schema,
				Stored:    stored,
				HasStored: hasStored,
				Next:      next,
			})
			if err == nil {
				if hooks.checkNotFoundFirst {
					delta, err = plan.Check(func(refs.Occurrence) bool { return true }, exists)
					if err == nil {
						delta, err = plan.Check(authorize, exists)
					}
				} else {
					delta, err = plan.Check(authorize, exists)
				}
			}
		}
		if err == nil && hooks.omitSelfFromDelta {
			filterSelf := func(objs []keyspace.Object) []keyspace.Object {
				var out []keyspace.Object
				for _, o := range objs {
					if o != tc.Source {
						out = append(out, o)
					}
				}
				return out
			}
			delta.Put = filterSelf(delta.Put)
			delta.Delete = filterSelf(delta.Delete)
			delta.Compare = filterSelf(delta.Compare)
		}

	case "delete":
		stored := tc.Stored
		if hooks.ignoreDeleteTargetCap {
			if spec, ok := envGetRecord(stored, "spec"); ok && len(spec.Fields) > refs.MaxTargets {
				spec.Fields = spec.Fields[:refs.MaxTargets]
				stored = envSetField(stored, "spec", spec)
			}
		}
		delta, err = refs.DeleteDelta(stored)

	default:
		return refsResult{}, fmt.Errorf("unknown refs op %q", tc.Op)
	}

	if err == nil {
		return refsResult{
			OK:      true,
			Put:     slices.Clone(delta.Put),
			Delete:  slices.Clone(delta.Delete),
			Compare: slices.Clone(delta.Compare),
		}, nil
	}

	var re *refs.Error
	if !errors.As(err, &re) {
		return refsResult{}, err
	}
	errName := re.Kind.String()
	if !slices.Contains(refsErrClasses, errName) {
		return refsResult{}, fmt.Errorf("unexpected refs error kind %q", errName)
	}
	res := refsResult{
		OK:  false,
		Err: errName,
	}
	if re.Occurrence.Ref != nil || re.Occurrence.Path != "" {
		p := re.Occurrence.Path
		res.Path = &p
		if re.Occurrence.Ref != nil {
			res.Ref = slices.Clone(re.Occurrence.Ref)
		} else {
			res.Ref = []string{}
		}
	}
	return res, nil
}

func decodeRefsObj(raw json.RawMessage) (keyspace.Object, error) {
	var top map[string]json.RawMessage
	if err := decodeStrictJSON(raw, &top); err != nil {
		return keyspace.Object{}, err
	}
	rawG, hasG := top["group"]
	rawK, hasK := top["kind"]
	rawN, hasN := top["name"]
	if len(top) != 3 || !hasG || !hasK || !hasN {
		return keyspace.Object{}, fmt.Errorf("object must have exact keys group, kind, name: %s", raw)
	}
	g, err := decodeString(rawG)
	if err != nil {
		return keyspace.Object{}, fmt.Errorf("group: %w", err)
	}
	k, err := decodeString(rawK)
	if err != nil {
		return keyspace.Object{}, fmt.Errorf("kind: %w", err)
	}
	n, err := decodeString(rawN)
	if err != nil {
		return keyspace.Object{}, fmt.Errorf("name: %w", err)
	}
	return keyspace.Object{Group: g, Kind: k, Name: n}, nil
}

func decodeRefsObjList(raw json.RawMessage) ([]keyspace.Object, error) {
	var arr []json.RawMessage
	if err := decodeStrictJSON(raw, &arr); err != nil {
		return nil, err
	}
	objs := make([]keyspace.Object, len(arr))
	for i, elem := range arr {
		obj, err := decodeRefsObj(elem)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		objs[i] = obj
	}
	return objs, nil
}

func decodeRefsResult(line []byte) (refsResult, error) {
	trimmed := bytes.TrimSpace(line)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rawTop json.RawMessage
	if err := dec.Decode(&rawTop); err != nil {
		return refsResult{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return refsResult{}, fmt.Errorf("unexpected trailing data in result line: %v", err)
	}
	var top map[string]json.RawMessage
	if err := decodeStrictJSON(rawTop, &top); err != nil {
		return refsResult{}, err
	}
	rawOK, hasOK := top["ok"]
	if !hasOK {
		return refsResult{}, fmt.Errorf("result object missing ok: %s", rawTop)
	}
	var ok bool
	if err := decodeStrictJSON(rawOK, &ok); err != nil {
		return refsResult{}, fmt.Errorf("ok: %w", err)
	}
	if ok {
		rawPut, hasPut := top["put"]
		rawDel, hasDel := top["delete"]
		rawCmp, hasCmp := top["compare"]
		if len(top) != 4 || !hasPut || !hasDel || !hasCmp {
			return refsResult{}, fmt.Errorf("ok=true result must have exact keys ok, put, delete, compare: %s", rawTop)
		}
		put, err := decodeRefsObjList(rawPut)
		if err != nil {
			return refsResult{}, fmt.Errorf("put: %w", err)
		}
		del, err := decodeRefsObjList(rawDel)
		if err != nil {
			return refsResult{}, fmt.Errorf("delete: %w", err)
		}
		cmp, err := decodeRefsObjList(rawCmp)
		if err != nil {
			return refsResult{}, fmt.Errorf("compare: %w", err)
		}
		return refsResult{OK: true, Put: put, Delete: del, Compare: cmp}, nil
	}

	rawErr, hasErr := top["error"]
	rawPath, hasPath := top["path"]
	rawRef, hasRef := top["ref"]
	if len(top) != 4 || !hasErr || !hasPath || !hasRef {
		return refsResult{}, fmt.Errorf("ok=false result must have exact keys ok, error, path, ref: %s", rawTop)
	}
	errName, err := decodeString(rawErr)
	if err != nil {
		return refsResult{}, fmt.Errorf("error: %w", err)
	}
	if !slices.Contains(refsErrClasses, errName) {
		return refsResult{}, fmt.Errorf("unknown error class %q", errName)
	}
	pathNull := isJSONNull(rawPath)
	refNull := isJSONNull(rawRef)
	if pathNull != refNull {
		return refsResult{}, fmt.Errorf("path and ref nullness mismatch: %s", rawTop)
	}
	res := refsResult{OK: false, Err: errName}
	if !pathNull {
		p, err := decodeString(rawPath)
		if err != nil {
			return refsResult{}, fmt.Errorf("path: %w", err)
		}
		var arr []json.RawMessage
		if err := decodeStrictJSON(rawRef, &arr); err != nil {
			return refsResult{}, fmt.Errorf("ref: %w", err)
		}
		ref := make([]string, len(arr))
		for i, elem := range arr {
			s, err := decodeString(elem)
			if err != nil {
				return refsResult{}, fmt.Errorf("ref[%d]: %w", i, err)
			}
			ref[i] = s
		}
		res.Path = &p
		res.Ref = ref
	}
	return res, nil
}

func normalizeRefsJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing JSON data: %v", err)
	}
	return json.Marshal(v)
}

func compareRefsResult(want, got refsResult) string {
	switch {
	case want.OK != got.OK:
		return fmt.Sprintf("ok mismatch: lean=%v (err=%q) go=%v (err=%q)", want.OK, want.Err, got.OK, got.Err)
	case want.Err != got.Err:
		return fmt.Sprintf("error mismatch: lean=%q go=%q", want.Err, got.Err)
	case (want.Path == nil) != (got.Path == nil):
		return fmt.Sprintf("path nullness mismatch: lean=%v go=%v", want.Path, got.Path)
	case want.Path != nil && *want.Path != *got.Path:
		return fmt.Sprintf("path mismatch: lean=%q go=%q", *want.Path, *got.Path)
	case (want.Ref == nil) != (got.Ref == nil):
		return fmt.Sprintf("ref nullness mismatch: lean=%v go=%v", want.Ref, got.Ref)
	case want.Ref != nil && !slices.Equal(want.Ref, got.Ref):
		return fmt.Sprintf("ref mismatch: lean=%v go=%v", want.Ref, got.Ref)
	case !slices.Equal(want.Put, got.Put):
		return fmt.Sprintf("put mismatch: lean=%v go=%v", want.Put, got.Put)
	case !slices.Equal(want.Delete, got.Delete):
		return fmt.Sprintf("delete mismatch: lean=%v go=%v", want.Delete, got.Delete)
	case !slices.Equal(want.Compare, got.Compare):
		return fmt.Sprintf("compare mismatch: lean=%v go=%v", want.Compare, got.Compare)
	default:
		return ""
	}
}

func leanRefsResults(t *testing.T, cases []refsCase) ([][]byte, [][]byte, []refsResult) {
	t.Helper()
	encoded := encodeRefsCases(t, cases)
	lines, err := RunModel("refs", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(lines), len(cases))
	}
	results := make([]refsResult, len(lines))
	for i, line := range lines {
		res, err := decodeRefsResult(line)
		if err != nil {
			t.Fatalf("case %d decode error: %v\ncase: %s\nlean: %s", i, err, encoded[i], line)
		}
		results[i] = res
	}
	return encoded, lines, results
}

func formatRefsBuckets(buckets map[string]int) string {
	parts := make([]string, 0, len(refsRequiredBuckets))
	for _, b := range refsRequiredBuckets {
		parts = append(parts, fmt.Sprintf("%s=%d", b, buckets[b]))
	}
	return strings.Join(parts, " ")
}

func TestRefsMatchesLeanModel(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateRefsCases(cfg.Seed, cfg.Cases)
	encoded, leanLines, leanResults := leanRefsResults(t, cases)

	repro := fmt.Sprintf("NAYUTAR_DRT_SEED=%d NAYUTAR_DRT_CASES=%d go test -tags drt -run '^TestRefsMatchesLeanModel$' ./core/drt/...", cfg.Seed, cfg.Cases)
	buckets := map[string]int{}
	for i, tc := range cases {
		goRes, err := runRefsCase(tc, refsHooks{})
		if err != nil {
			t.Fatalf("seed=%d case=%d go error: %v\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, i, err, encoded[i], leanLines[i], repro)
		}
		goLine, err := json.Marshal(goRes)
		if err != nil {
			t.Fatalf("seed=%d case=%d marshal go result: %v", cfg.Seed, i, err)
		}
		if diff := compareRefsResult(leanResults[i], goRes); diff != "" {
			t.Fatalf("seed=%d case=%d mismatch (%s)\ncase: %s\nlean: %s\ngo:   %s\nreproduce: %s",
				cfg.Seed, i, diff, encoded[i], leanLines[i], goLine, repro)
		}
		leanNorm, err := normalizeRefsJSON(leanLines[i])
		if err != nil {
			t.Fatalf("seed=%d case=%d normalize lean json: %v\nlean: %s", cfg.Seed, i, err, leanLines[i])
		}
		goNorm, err := normalizeRefsJSON(goLine)
		if err != nil {
			t.Fatalf("seed=%d case=%d normalize go json: %v\ngo: %s", cfg.Seed, i, err, goLine)
		}
		if !bytes.Equal(leanNorm, goNorm) {
			t.Fatalf("seed=%d case=%d normalized JSON mismatch\ncase: %s\nlean: %s\ngo:   %s\nreproduce: %s",
				cfg.Seed, i, encoded[i], leanNorm, goNorm, repro)
		}
		if goRes.OK {
			buckets[tc.Op+"/ok"]++
		} else {
			buckets[tc.Op+"/"+goRes.Err]++
		}
	}
	t.Logf("compared %d cases (seed=%d): %s", len(cases), cfg.Seed, formatRefsBuckets(buckets))

	if cfg.Cases >= 100 {
		var missing []string
		for _, b := range refsRequiredBuckets {
			if buckets[b] == 0 {
				missing = append(missing, b)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed=%d empty outcome buckets: %s", cfg.Seed, strings.Join(missing, ", "))
		}
	}
}

func TestRefsCasesReachEveryOutcome(t *testing.T) {
	cases := generateRefsCases(1, 1000)
	_, _, results := leanRefsResults(t, cases)

	seen := map[string]int{}
	for i, tc := range cases {
		res := results[i]
		if res.OK {
			seen[tc.Op+"/ok"]++
			if len(res.Put) > 0 {
				seen[tc.Op+"/ok-put"]++
			}
			if len(res.Delete) > 0 {
				seen[tc.Op+"/ok-delete"]++
			}
			if len(res.Compare) > 0 {
				seen[tc.Op+"/ok-compare"]++
			}
			if tc.Op == "write" && !tc.HasStored {
				seen["write/ok-create"]++
			}
			if tc.Op == "write" && tc.HasStored {
				seen["write/ok-update"]++
			}
			if slices.Contains(res.Put, tc.Source) || slices.Contains(res.Compare, tc.Source) {
				seen["write/ok-self-ref"]++
			}
		} else {
			seen[tc.Op+"/"+res.Err]++
			if res.Path == nil && res.Ref == nil {
				seen[tc.Op+"/"+res.Err+"-target-bound"]++
			}
		}
	}

	expected := append(slices.Clone(refsRequiredBuckets),
		"write/ok-create",
		"write/ok-update",
		"write/ok-put",
		"write/ok-delete",
		"write/ok-compare",
		"write/ok-self-ref",
		"write/internal-target-bound",
		"write/malformed-target-bound",
		"delete/ok-delete",
		"delete/internal-target-bound",
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

func cloneRefsResult(r refsResult) refsResult {
	out := refsResult{
		OK:      r.OK,
		Put:     slices.Clone(r.Put),
		Delete:  slices.Clone(r.Delete),
		Compare: slices.Clone(r.Compare),
		Err:     r.Err,
		Ref:     slices.Clone(r.Ref),
	}
	if r.Path != nil {
		p := *r.Path
		out.Path = &p
	}
	return out
}

func TestRefsComparisonDetectsEveryFieldMutation(t *testing.T) {
	cases := generateRefsCases(1, 200)
	_, leanLines, results := leanRefsResults(t, cases)

	applied := map[string]int{}
	assertDetects := func(kind string, idx int, orig refsResult, mutate func(r *refsResult)) {
		t.Helper()
		mut := cloneRefsResult(orig)
		mutate(&mut)
		applied[kind]++
		if compareRefsResult(orig, mut) == "" {
			t.Fatalf("mutation %q on case %d was not detected", kind, idx)
		}
	}

	for i, orig := range results {
		assertDetects("flip-ok", i, orig, func(r *refsResult) { r.OK = !r.OK })
		if orig.OK {
			assertDetects("err-set", i, orig, func(r *refsResult) { r.Err = "internal" })
			assertDetects("put-changed", i, orig, func(r *refsResult) {
				r.Put = append(r.Put, keyspace.Object{Group: "core", Kind: "secret", Name: "mut"})
			})
			assertDetects("delete-changed", i, orig, func(r *refsResult) {
				r.Delete = append(r.Delete, keyspace.Object{Group: "core", Kind: "secret", Name: "mut"})
			})
			assertDetects("compare-changed", i, orig, func(r *refsResult) {
				r.Compare = append(r.Compare, keyspace.Object{Group: "core", Kind: "secret", Name: "mut"})
			})
		} else {
			assertDetects("err-cleared", i, orig, func(r *refsResult) { r.Err = "" })
			assertDetects("err-changed", i, orig, func(r *refsResult) { r.Err += "x" })
			if orig.Path != nil {
				assertDetects("path-cleared", i, orig, func(r *refsResult) { r.Path = nil })
				assertDetects("path-changed", i, orig, func(r *refsResult) {
					p := *r.Path + "x"
					r.Path = &p
				})
				assertDetects("ref-cleared", i, orig, func(r *refsResult) { r.Ref = nil })
				assertDetects("ref-changed", i, orig, func(r *refsResult) {
					r.Ref = append(r.Ref, "x")
				})
			} else {
				assertDetects("path-set", i, orig, func(r *refsResult) {
					p := "spec/x"
					r.Path = &p
				})
				assertDetects("ref-set", i, orig, func(r *refsResult) {
					r.Ref = []string{"secret", "x"}
				})
			}
		}
	}

	for _, kind := range []string{
		"flip-ok", "err-set", "put-changed", "delete-changed", "compare-changed",
		"err-cleared", "err-changed", "path-cleared", "path-changed", "ref-cleared", "ref-changed",
		"path-set", "ref-set",
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
		{"missing ok", `{"error":"internal","path":null,"ref":null}`},
		{"null ok", `{"ok":null,"error":"internal","path":null,"ref":null}`},
		{"ok=true missing put", `{"ok":true,"delete":[],"compare":[]}`},
		{"ok=true null put", `{"ok":true,"put":null,"delete":[],"compare":[]}`},
		{"ok=true with error", `{"ok":true,"put":[],"delete":[],"compare":[],"error":""}`},
		{"ok=true bad obj", `{"ok":true,"put":[{"group":"core","kind":"secret"}],"delete":[],"compare":[]}`},
		{"ok=false missing ref", `{"ok":false,"error":"internal","path":null}`},
		{"ok=false unknown err", `{"ok":false,"error":"boom","path":null,"ref":null}`},
		{"ok=false path null ref non-null", `{"ok":false,"error":"malformed","path":null,"ref":[]}`},
		{"ok=false path non-null ref null", `{"ok":false,"error":"malformed","path":"spec/a","ref":null}`},
		{"trailing object", `{"ok":false,"error":"internal","path":null,"ref":null}{"ok":true,"put":[],"delete":[],"compare":[]}`},
	}
	for _, tc := range invalidLines {
		if _, err := decodeRefsResult([]byte(tc.line)); err == nil {
			t.Errorf("decodeRefsResult accepted %s: %s", tc.name, tc.line)
		}
	}
}

func TestRefsDRTCatchesBrokenImplementations(t *testing.T) {
	cases := generateRefsCases(1, 500)
	_, _, leanResults := leanRefsResults(t, cases)

	variants := []struct {
		name  string
		hooks refsHooks
	}{
		{"ignores stored", refsHooks{ignoreStored: true}},
		{"ignores_stored_target_limit", refsHooks{ignoresStoredTargetCap: true}},
		{"allows outside spec", refsHooks{allowOutsideSpec: true}},
		{"diffs by ref value only", refsHooks{diffByRefValueOnly: true}},
		{"ignores target limit", refsHooks{ignoreTargetLimit: true}},
		{"allows all schema kinds", refsHooks{allowAllSchemaKinds: true}},
		{"ignores_schema_source", refsHooks{ignoresSchemaSource: true}},
		{"checks notFound before denied", refsHooks{checkNotFoundFirst: true}},
		{"rechecks unchanged refs", refsHooks{recheckUnchanged: true}},
		{"omits self from delta", refsHooks{omitSelfFromDelta: true}},
		{"ignores delete target cap", refsHooks{ignoreDeleteTargetCap: true}},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			mismatches := 0
			for i, tc := range cases {
				goRes, err := runRefsCase(tc, v.hooks)
				if err != nil {
					t.Fatalf("case %d: broken implementation failed instead of answering wrongly: %v", i, err)
				}
				if compareRefsResult(leanResults[i], goRes) != "" {
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

func TestGenerateRefsCasesIsDeterministic(t *testing.T) {
	a := bytes.Join(encodeRefsCases(t, generateRefsCases(1, 100)), []byte("\n"))
	b := bytes.Join(encodeRefsCases(t, generateRefsCases(1, 100)), []byte("\n"))
	c := bytes.Join(encodeRefsCases(t, generateRefsCases(2, 100)), []byte("\n"))

	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different encoded cases")
	}
	if bytes.Equal(a, c) {
		t.Fatal("different seeds produced identical encoded cases")
	}
}

func TestRefsModelRejectsMalformedCase(t *testing.T) {
	valid := `{"op":"delete","stored":{"o":[["meta",{"o":[["kind",{"s":"pod"}],["name",{"s":"web"}]]}]]}}`
	for _, bad := range []string{
		`{"op":"explode"}`,
		`{"op":"delete","stored":{"x":"bad"}}`,
		`{"op":"write","source":{"group":"core","kind":"pod","name":"web"},"schema":[],"stored":null,"next":{"o":[]},"allow":[]}`,
		`{"op":"write","source":{"group":"core","kind":"pod"},"schema":[],"stored":null,"next":{"o":[]},"allow":[],"present":[]}`,
	} {
		cases := [][]byte{[]byte(valid), []byte(bad)}
		_, err := RunModel("refs", cases)
		if err == nil {
			t.Errorf("RunModel accepted malformed case %s", bad)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte("line 2")) {
			t.Errorf("error %q does not name line 2 for %s", err.Error(), bad)
		}
	}
}
