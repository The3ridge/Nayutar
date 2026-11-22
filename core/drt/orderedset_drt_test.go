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
	"github.com/Kaikei-e/Nayutar/core/resource/orderedset"
)

type orderedSetItemWire struct {
	Key   string    `json:"key"`
	Value wireValue `json:"value"`
}

type orderedSetCase struct {
	ID       int
	Op       string
	Set      lkal.Record
	Items    []orderedset.Item
	Elems    []lkal.Record
	KeyField string
	First    int64
	Last     int64
	Lo       int64
	Hi       int64
}

func (c orderedSetCase) MarshalJSON() ([]byte, error) {
	switch c.Op {
	case "entries", "entriesReverse", "renumber":
		return json.Marshal(struct {
			ID  int       `json:"id"`
			Op  string    `json:"op"`
			Set wireValue `json:"set"`
		}{c.ID, c.Op, wireValue{c.Set}})
	case "fromItems":
		items := make([]orderedSetItemWire, len(c.Items))
		for i, it := range c.Items {
			items[i] = orderedSetItemWire{Key: it.Key, Value: wireValue{it.Value}}
		}
		return json.Marshal(struct {
			ID    int                  `json:"id"`
			Op    string               `json:"op"`
			Items []orderedSetItemWire `json:"items"`
		}{c.ID, c.Op, items})
	case "fromKeyedSlice":
		elems := make([]wireValue, len(c.Elems))
		for i, el := range c.Elems {
			elems[i] = wireValue{el}
		}
		return json.Marshal(struct {
			ID       int         `json:"id"`
			Op       string      `json:"op"`
			KeyField string      `json:"keyField"`
			Elems    []wireValue `json:"elems"`
		}{c.ID, c.Op, c.KeyField, elems})
	case "toKeyedSlice", "toKeyedSliceReverse":
		return json.Marshal(struct {
			ID       int       `json:"id"`
			Op       string    `json:"op"`
			Set      wireValue `json:"set"`
			KeyField string    `json:"keyField"`
		}{c.ID, c.Op, wireValue{c.Set}, c.KeyField})
	case "orderBefore":
		return json.Marshal(struct {
			ID    int    `json:"id"`
			Op    string `json:"op"`
			First string `json:"first"`
		}{c.ID, c.Op, strconv.FormatInt(c.First, 10)})
	case "orderAfter":
		return json.Marshal(struct {
			ID   int    `json:"id"`
			Op   string `json:"op"`
			Last string `json:"last"`
		}{c.ID, c.Op, strconv.FormatInt(c.Last, 10)})
	case "orderBetween":
		return json.Marshal(struct {
			ID int    `json:"id"`
			Op string `json:"op"`
			Lo string `json:"lo"`
			Hi string `json:"hi"`
		}{c.ID, c.Op, strconv.FormatInt(c.Lo, 10), strconv.FormatInt(c.Hi, 10)})
	default:
		return nil, fmt.Errorf("unknown orderedset op %q", c.Op)
	}
}

type orderedSetResult struct {
	ID      int
	Op      string
	OK      bool
	Err     string
	Entries []orderedset.Entry
	Set     lkal.Record
	Elems   []lkal.Record
	Order   int64
}

var (
	orderedSetErrClasses = []string{
		"malformed",
		"duplicateKey",
		"duplicateOrder",
		"reservedField",
		"gapExhausted",
		"overflow",
	}

	orderedSetRequiredBuckets = []string{
		"entries/ok",
		"entries/malformed",
		"entries/duplicateOrder",
		"entriesReverse/ok",
		"entriesReverse/malformed",
		"entriesReverse/duplicateOrder",
		"fromItems/ok",
		"fromItems/malformed",
		"fromItems/reservedField",
		"fromItems/duplicateKey",
		"fromKeyedSlice/ok",
		"fromKeyedSlice/malformed",
		"fromKeyedSlice/reservedField",
		"fromKeyedSlice/duplicateKey",
		"toKeyedSlice/ok",
		"toKeyedSlice/malformed",
		"toKeyedSlice/duplicateOrder",
		"toKeyedSlice/reservedField",
		"toKeyedSliceReverse/ok",
		"toKeyedSliceReverse/malformed",
		"toKeyedSliceReverse/duplicateOrder",
		"toKeyedSliceReverse/reservedField",
		"orderBefore/ok",
		"orderBefore/overflow",
		"orderAfter/ok",
		"orderAfter/overflow",
		"orderBetween/ok",
		"orderBetween/malformed",
		"orderBetween/gapExhausted",
		"renumber/ok",
		"renumber/malformed",
		"renumber/duplicateOrder",
	}

	osValidKeys     = []string{"bridge", "portmap", "fw-pre", "fw-post", "tuning", "migrate", "seed", "app", "café", "日本語", "p0", "p1"}
	osBadKeys       = []string{"", "$sys", "$", "a/b", "/lead", "trail/", "$a/b"}
	osValidKeyFlds  = []string{"name", "id", "label", "plugin-id"}
	osBadKeyFlds    = []string{"", "order", "$name", "a/b", "/"}
	osPayloadFields = []string{"type", "image", "policy", "bridge", "mtu", "tags", "nested", "creds", "timeout", "enabled"}
	osEdgeOrders    = []int64{
		math.MinInt64,
		math.MinInt64 + 1,
		math.MinInt64 + 9,
		math.MinInt64 + 10,
		-1000, -10, -3, -2, -1, 0, 1, 2, 10, 15, 20, 30, 100,
		math.MaxInt64 - 10,
		math.MaxInt64 - 9,
		math.MaxInt64 - 1,
		math.MaxInt64,
	}
)

func genOSLeaf(rng *rand.Rand) lkal.Value {
	switch rng.IntN(7) {
	case 0:
		return lkal.String(pick(rng, []string{"firewall", "bridge", "portmap", "", "café", "日本語"}))
	case 1:
		return lkal.Int(pick(rng, osEdgeOrders))
	case 2:
		return lkal.Decimal(pick(rng, []string{"0.5", "-1.25", "10.0"}))
	case 3:
		return lkal.Bool(rng.IntN(2) == 0)
	case 4:
		return lkal.Enum(pick(rng, []string{"allow", "drop", "tcp"}))
	case 5:
		return lkal.Quantity{Number: pick(rng, []string{"30", "512"}), Unit: pick(rng, []string{"s", "Mi"})}
	default:
		return lkal.Ref{pick(rng, []string{"secret", "configmap"}), pick(rng, []string{"db", "app"})}
	}
}

// genOSPayload builds a valid unique payload record excluding excludeKeys.
func genOSPayload(rng *rand.Rand, excludeKeys ...string) lkal.Record {
	keys := make([]string, 0, len(osPayloadFields))
	for _, k := range osPayloadFields {
		if !slices.Contains(excludeKeys, k) {
			keys = append(keys, k)
		}
	}
	rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	count := rng.IntN(4)
	fields := make([]lkal.Field, count)
	for i := range count {
		var val lkal.Value
		switch rng.IntN(8) {
		case 0, 1, 2, 3, 4:
			val = genOSLeaf(rng)
		case 5:
			n := rng.IntN(3)
			lst := make(lkal.List, n)
			for j := range n {
				if chance(rng, 35) {
					lst[j] = envRec(envField("k", genOSLeaf(rng)), envField("n", lkal.Int(int64(j))))
				} else {
					lst[j] = genOSLeaf(rng)
				}
			}
			val = lst
		default:
			val = envRec(envField("a", genOSLeaf(rng)), envField("b", lkal.Bool(chance(rng, 50))))
		}
		fields[i] = envField(keys[i], val)
	}
	return lkal.Record{Fields: fields}
}

func injectOSDupKey(rng *rand.Rand, r lkal.Record) lkal.Record {
	out := envCloneRecord(r)
	dup := envRec(envField("dup", lkal.Int(1)), envField("dup", lkal.Int(2)))
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

func pickDistinctKeys(rng *rand.Rand, n int) []string {
	pool := slices.Clone(osValidKeys)
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	if n > len(pool) {
		n = len(pool)
	}
	return pool[:n]
}

func pickDistinctOrders(rng *rand.Rand, n int) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, n)
	for len(out) < n {
		var ord int64
		if chance(rng, 45) {
			ord = pick(rng, osEdgeOrders)
		} else {
			ord = int64(rng.IntN(201) - 100)
		}
		if !seen[ord] {
			seen[ord] = true
			out = append(out, ord)
		}
	}
	return out
}

func genValidSet(rng *rand.Rand, minEntries int, excludePayloadKey string) lkal.Record {
	n := minEntries + rng.IntN(5-minEntries)
	if n == 0 {
		return envRec()
	}
	keys := pickDistinctKeys(rng, n)
	orders := pickDistinctOrders(rng, n)
	fields := make([]lkal.Field, n)
	for i := range n {
		payload := genOSPayload(rng, orderedset.OrderField, excludePayloadKey)
		payload.Fields = append(payload.Fields, envField(orderedset.OrderField, lkal.Int(orders[i])))
		if chance(rng, 70) {
			payload = envShuffleRecord(rng, payload)
		}
		fields[i] = envField(keys[i], payload)
	}
	set := lkal.Record{Fields: fields}
	if chance(rng, 75) {
		set = envShuffleRecord(rng, set)
	}
	return set
}

func genMalformedSet(rng *rand.Rand) lkal.Record {
	set := genValidSet(rng, 1, "")
	// Frequently combine with a duplicate order on indices 0..1 while corrupting index >=2 so phase 1 > phase 2 is exercised.
	dupInjected := false
	if len(set.Fields) >= 3 && chance(rng, 55) {
		firstOrd, _ := envGetField(set.Fields[0].Value.(lkal.Record), orderedset.OrderField)
		r1 := set.Fields[1].Value.(lkal.Record)
		set.Fields[1].Value = envSetField(r1, orderedset.OrderField, firstOrd)
		dupInjected = true
	}
	pickIdx := func() int {
		if dupInjected && len(set.Fields) >= 3 {
			return 2 + rng.IntN(len(set.Fields)-2)
		}
		return rng.IntN(len(set.Fields))
	}
	switch rng.IntN(7) {
	case 0:
		// Duplicate top-level key.
		set.Fields = append(set.Fields, envField(set.Fields[0].Key, envCloneValue(set.Fields[0].Value)))
	case 1:
		// Duplicate nested key inside entry or list.
		idx := pickIdx()
		set.Fields[idx].Value = injectOSDupKey(rng, set.Fields[idx].Value.(lkal.Record))
	case 2:
		// Invalid entry key.
		idx := pickIdx()
		set.Fields[idx].Key = pick(rng, osBadKeys)
	case 3:
		// Non-record entry value.
		idx := pickIdx()
		set.Fields[idx].Value = genOSLeaf(rng)
	case 4:
		// Missing order field.
		idx := pickIdx()
		set.Fields[idx].Value = envDropField(set.Fields[idx].Value.(lkal.Record), orderedset.OrderField)
	case 5:
		// Non-int order field (string, decimal, bool, enum, list, record).
		idx := pickIdx()
		badOrd := pick(rng, []lkal.Value{
			lkal.String("10"),
			lkal.Decimal("10.0"),
			lkal.Bool(true),
			lkal.Enum("ten"),
			lkal.List{lkal.Int(10)},
			envRec(envField("v", lkal.Int(10))),
		})
		set.Fields[idx].Value = envSetField(set.Fields[idx].Value.(lkal.Record), orderedset.OrderField, badOrd)
	default:
		// Duplicate order field key inside one entry record.
		idx := pickIdx()
		r := envCloneRecord(set.Fields[idx].Value.(lkal.Record))
		r.Fields = append(r.Fields, envField(orderedset.OrderField, lkal.Int(999)))
		set.Fields[idx].Value = r
	}
	return envShuffleRecord(rng, set)
}

func genDuplicateOrderSet(rng *rand.Rand, excludePayloadKey string) lkal.Record {
	set := genValidSet(rng, 2, excludePayloadKey)
	dupOrd := pick(rng, osEdgeOrders)
	i := rng.IntN(len(set.Fields))
	j := (i + 1 + rng.IntN(len(set.Fields)-1)) % len(set.Fields)
	set.Fields[i].Value = envSetField(set.Fields[i].Value.(lkal.Record), orderedset.OrderField, lkal.Int(dupOrd))
	set.Fields[j].Value = envSetField(set.Fields[j].Value.(lkal.Record), orderedset.OrderField, lkal.Int(dupOrd))
	return envShuffleRecord(rng, set)
}

func genValidItems(rng *rand.Rand, minItems int) []orderedset.Item {
	n := minItems + rng.IntN(5-minItems)
	if n == 0 {
		return []orderedset.Item{}
	}
	keys := pickDistinctKeys(rng, n)
	items := make([]orderedset.Item, n)
	for i := range n {
		items[i] = orderedset.Item{
			Key:   keys[i],
			Value: genOSPayload(rng, orderedset.OrderField),
		}
	}
	return items
}

func genValidKeyedElems(rng *rand.Rand, keyField string, minElems int) []lkal.Record {
	n := minElems + rng.IntN(5-minElems)
	if n == 0 {
		return []lkal.Record{}
	}
	keys := pickDistinctKeys(rng, n)
	elems := make([]lkal.Record, n)
	for i := range n {
		p := genOSPayload(rng, orderedset.OrderField, keyField)
		p.Fields = append(p.Fields, envField(keyField, lkal.String(keys[i])))
		if chance(rng, 65) {
			p = envShuffleRecord(rng, p)
		}
		elems[i] = p
	}
	return elems
}

func genOrderedSetBucketCase(rng *rand.Rand, id int, bucket string) orderedSetCase {
	op, outcome, _ := strings.Cut(bucket, "/")
	c := orderedSetCase{ID: id, Op: op}

	switch op {
	case "entries", "entriesReverse", "renumber":
		switch outcome {
		case "ok":
			c.Set = genValidSet(rng, 0, "")
		case "malformed":
			c.Set = genMalformedSet(rng)
		case "duplicateOrder":
			c.Set = genDuplicateOrderSet(rng, "")
		}

	case "fromItems":
		switch outcome {
		case "ok":
			c.Items = genValidItems(rng, 0)
		case "malformed":
			items := genValidItems(rng, 1)
			// Mix in reservedField and duplicateKey sometimes to verify phase 1 > 2 > 3.
			if len(items) >= 2 && chance(rng, 45) {
				items[0].Value = envSetField(items[0].Value, orderedset.OrderField, lkal.Int(10))
				items[1].Key = items[0].Key
			}
			idx := rng.IntN(len(items))
			if chance(rng, 55) {
				items[idx].Key = pick(rng, osBadKeys)
			} else {
				items[idx].Value = injectOSDupKey(rng, items[idx].Value)
			}
			c.Items = items
		case "reservedField":
			items := genValidItems(rng, 1)
			// Mix in duplicateKey sometimes to verify phase 2 > 3.
			if len(items) >= 2 && chance(rng, 55) {
				items[1].Key = items[0].Key
			}
			idx := rng.IntN(len(items))
			items[idx].Value = envSetField(items[idx].Value, orderedset.OrderField, genOSLeaf(rng))
			c.Items = items
		case "duplicateKey":
			items := genValidItems(rng, 2)
			i := rng.IntN(len(items))
			j := (i + 1 + rng.IntN(len(items)-1)) % len(items)
			items[j].Key = items[i].Key
			c.Items = items
		}

	case "fromKeyedSlice":
		switch outcome {
		case "ok":
			c.KeyField = pick(rng, osValidKeyFlds)
			c.Elems = genValidKeyedElems(rng, c.KeyField, 0)
		case "malformed":
			if chance(rng, 45) {
				if chance(rng, 45) {
					c.KeyField = orderedset.OrderField
				} else {
					c.KeyField = pick(rng, osBadKeyFlds)
				}
				c.Elems = genValidKeyedElems(rng, "name", 0)
				if c.KeyField == orderedset.OrderField && chance(rng, 60) {
					c.Elems = []lkal.Record{envRec(envField("order", lkal.String("a")), envField("x", lkal.Int(1)))}
				}
			} else {
				c.KeyField = pick(rng, osValidKeyFlds)
				elems := genValidKeyedElems(rng, c.KeyField, 1)
				if len(elems) >= 2 && chance(rng, 45) {
					elems[0] = envSetField(elems[0], orderedset.OrderField, lkal.Int(10))
					kv, _ := envGetField(elems[0], c.KeyField)
					elems[1] = envSetField(elems[1], c.KeyField, kv)
				}
				idx := rng.IntN(len(elems))
				switch rng.IntN(4) {
				case 0:
					elems[idx] = envDropField(elems[idx], c.KeyField)
				case 1:
					elems[idx] = envSetField(elems[idx], c.KeyField, lkal.Int(42))
				case 2:
					elems[idx] = envSetField(elems[idx], c.KeyField, lkal.String(pick(rng, osBadKeys)))
				default:
					elems[idx] = injectOSDupKey(rng, elems[idx])
				}
				c.Elems = elems
			}
		case "reservedField":
			c.KeyField = pick(rng, osValidKeyFlds)
			elems := genValidKeyedElems(rng, c.KeyField, 1)
			if len(elems) >= 2 && chance(rng, 55) {
				kv, _ := envGetField(elems[0], c.KeyField)
				elems[1] = envSetField(elems[1], c.KeyField, kv)
			}
			idx := rng.IntN(len(elems))
			elems[idx] = envSetField(elems[idx], orderedset.OrderField, genOSLeaf(rng))
			c.Elems = elems
		case "duplicateKey":
			c.KeyField = pick(rng, osValidKeyFlds)
			elems := genValidKeyedElems(rng, c.KeyField, 2)
			i := rng.IntN(len(elems))
			j := (i + 1 + rng.IntN(len(elems)-1)) % len(elems)
			kv, _ := envGetField(elems[i], c.KeyField)
			elems[j] = envSetField(elems[j], c.KeyField, kv)
			c.Elems = elems
		}

	case "toKeyedSlice", "toKeyedSliceReverse":
		switch outcome {
		case "ok":
			c.KeyField = pick(rng, osValidKeyFlds)
			c.Set = genValidSet(rng, 0, c.KeyField)
		case "malformed":
			if chance(rng, 35) {
				c.KeyField = pick(rng, osBadKeyFlds)
				c.Set = genValidSet(rng, 0, "")
			} else {
				c.KeyField = pick(rng, osValidKeyFlds)
				c.Set = genMalformedSet(rng)
			}
		case "duplicateOrder":
			c.KeyField = pick(rng, osValidKeyFlds)
			c.Set = genDuplicateOrderSet(rng, c.KeyField)
			// Sometimes also add colliding keyField to test phase 2 (duplicateOrder) > phase 3 (reservedField).
			if chance(rng, 50) {
				r0 := c.Set.Fields[0].Value.(lkal.Record)
				c.Set.Fields[0].Value = envSetField(r0, c.KeyField, lkal.String("clash"))
			}
		case "reservedField":
			c.KeyField = pick(rng, osValidKeyFlds)
			c.Set = genValidSet(rng, 1, c.KeyField)
			idx := rng.IntN(len(c.Set.Fields))
			r := c.Set.Fields[idx].Value.(lkal.Record)
			c.Set.Fields[idx].Value = envSetField(r, c.KeyField, genOSLeaf(rng))
		}

	case "orderBefore":
		switch outcome {
		case "ok":
			if chance(rng, 40) {
				c.First = pick(rng, []int64{math.MinInt64 + orderedset.Step, 10, 0, -10, math.MaxInt64})
			} else {
				c.First = int64(rng.IntN(2001) - 1000)
			}
		case "overflow":
			c.First = math.MinInt64 + int64(rng.IntN(int(orderedset.Step)))
		}

	case "orderAfter":
		switch outcome {
		case "ok":
			if chance(rng, 40) {
				c.Last = pick(rng, []int64{math.MaxInt64 - orderedset.Step, -10, 0, 10, math.MinInt64})
			} else {
				c.Last = int64(rng.IntN(2001) - 1000)
			}
		case "overflow":
			c.Last = math.MaxInt64 - int64(rng.IntN(int(orderedset.Step)))
		}

	case "orderBetween":
		switch outcome {
		case "ok":
			switch rng.IntN(6) {
			case 0:
				// Negative odd sums where floor division (-2) differs from truncation toward zero (-1).
				c.Lo = pick(rng, []int64{-3, -5, -11, -101})
				c.Hi = pick(rng, []int64{0, -2, -4})
				if c.Lo >= c.Hi-1 {
					c.Lo = -3
					c.Hi = 0
				}
			case 1:
				c.Lo = math.MinInt64
				c.Hi = math.MaxInt64
			case 2:
				c.Lo = math.MinInt64
				c.Hi = math.MinInt64 + 2 + int64(rng.IntN(50))
			case 3:
				c.Lo = math.MaxInt64 - 2 - int64(rng.IntN(50))
				c.Hi = math.MaxInt64
			case 4:
				c.Lo = 10
				c.Hi = pick(rng, []int64{12, 15, 20, 30})
			default:
				a := int64(rng.IntN(1000) - 500)
				delta := int64(2 + rng.IntN(500))
				c.Lo = a
				c.Hi = a + delta
			}
		case "malformed":
			if chance(rng, 45) {
				v := pick(rng, osEdgeOrders)
				c.Lo, c.Hi = v, v
			} else {
				a := pick(rng, osEdgeOrders)
				b := pick(rng, osEdgeOrders)
				if a < b {
					a, b = b, a
				}
				if a == b {
					a = 20
					b = 10
				}
				c.Lo, c.Hi = a, b
			}
		case "gapExhausted":
			switch rng.IntN(4) {
			case 0:
				c.Lo = math.MinInt64
				c.Hi = math.MinInt64 + 1
			case 1:
				c.Lo = math.MaxInt64 - 1
				c.Hi = math.MaxInt64
			case 2:
				c.Lo = -1
				c.Hi = 0
			default:
				v := int64(rng.IntN(2001) - 1000)
				c.Lo = v
				c.Hi = v + 1
			}
		}
	}

	return c
}

func generateOrderedSetCases(seed uint64, count int) []orderedSetCase {
	rng := rand.New(rand.NewPCG(seed, seed^0x517cc1b727220a95))
	cases := make([]orderedSetCase, count)
	var epoch []string
	for i := range count {
		if len(epoch) == 0 {
			epoch = slices.Clone(orderedSetRequiredBuckets)
			rng.Shuffle(len(epoch), func(a, b int) { epoch[a], epoch[b] = epoch[b], epoch[a] })
		}
		bucket := epoch[len(epoch)-1]
		epoch = epoch[:len(epoch)-1]
		cases[i] = genOrderedSetBucketCase(rng, i, bucket)
	}
	return cases
}

func encodeOrderedSetCases(t *testing.T, cases []orderedSetCase) [][]byte {
	t.Helper()
	out := make([][]byte, len(cases))
	for i, c := range cases {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal orderedset case %d (%s): %v", c.ID, c.Op, err)
		}
		out[i] = b
	}
	return out
}

type orderedSetHooks struct {
	entriesSortDescending            bool
	entriesKeepOrderField            bool
	entriesAllowDuplicateOrder       bool
	entriesDuplicateBeforeMalformed  bool
	entriesReverseNotReversed        bool
	fromItemsOverwriteOrder          bool
	fromItemsDuplicateBeforeReserved bool
	fromItemsStepOne                 bool
	fromKeyedAllowOrderKeyField      bool
	fromKeyedKeepKeyField            bool
	toKeyedOverwriteKeyField         bool
	toKeyedReverseNotReversed        bool
	orderBeforeIgnoreOverflow        bool
	orderAfterIgnoreOverflow         bool
	orderBetweenTruncateZero         bool
	orderBetweenAdjacentReturnsLo    bool
	renumberKeepOldOrders            bool
}

func orderedSetErrName(err error) (string, error) {
	var osErr *orderedset.Error
	if !errors.As(err, &osErr) {
		return "", fmt.Errorf("non-orderedset error %T: %w", err, err)
	}
	name := osErr.Kind.String()
	if !slices.Contains(orderedSetErrClasses, name) {
		return "", fmt.Errorf("unknown orderedset error kind %q", name)
	}
	return name, nil
}

func runOrderedSetCase(tc orderedSetCase, hooks orderedSetHooks) (orderedSetResult, error) {
	res := orderedSetResult{ID: tc.ID, Op: tc.Op}

	var err error
	switch tc.Op {
	case "entries":
		if hooks.entriesDuplicateBeforeMalformed {
			seen := map[int64]bool{}
			for _, fld := range tc.Set.Fields {
				if rec, ok := fld.Value.(lkal.Record); ok {
					if ov, ok := envGetField(rec, orderedset.OrderField); ok {
						if oi, ok := ov.(lkal.Int); ok {
							if seen[int64(oi)] {
								res.Err = "duplicateOrder"
								return res, nil
							}
							seen[int64(oi)] = true
						}
					}
				}
			}
		}
		var es []orderedset.Entry
		if hooks.entriesAllowDuplicateOrder {
			dedup := envCloneRecord(tc.Set)
			seen := map[int64]bool{}
			nextOrd := int64(1)
			for i, fld := range dedup.Fields {
				if rec, ok := fld.Value.(lkal.Record); ok {
					if ov, ok := envGetField(rec, orderedset.OrderField); ok {
						if oi, ok := ov.(lkal.Int); ok {
							if seen[int64(oi)] {
								for seen[nextOrd] {
									nextOrd++
								}
								dedup.Fields[i].Value = envSetField(rec, orderedset.OrderField, lkal.Int(nextOrd))
								seen[nextOrd] = true
							} else {
								seen[int64(oi)] = true
							}
						}
					}
				}
			}
			es, err = orderedset.Entries(dedup)
		} else {
			es, err = orderedset.Entries(tc.Set)
		}
		if err == nil {
			if hooks.entriesSortDescending {
				slices.Reverse(es)
			}
			if hooks.entriesKeepOrderField {
				for i := range es {
					es[i].Value = envSetField(es[i].Value, orderedset.OrderField, lkal.Int(es[i].Order))
				}
			}
			res.OK = true
			res.Entries = es
			return res, nil
		}

	case "entriesReverse":
		var es []orderedset.Entry
		if hooks.entriesReverseNotReversed {
			es, err = orderedset.Entries(tc.Set)
		} else {
			es, err = orderedset.EntriesReverse(tc.Set)
		}
		if err == nil {
			res.OK = true
			res.Entries = es
			return res, nil
		}

	case "fromItems":
		items := tc.Items
		if hooks.fromItemsDuplicateBeforeReserved {
			seen := map[string]bool{}
			for _, it := range items {
				if seen[it.Key] {
					res.Err = "duplicateKey"
					return res, nil
				}
				seen[it.Key] = true
			}
		}
		if hooks.fromItemsOverwriteOrder {
			cleaned := make([]orderedset.Item, len(items))
			for i, it := range items {
				cleaned[i] = orderedset.Item{Key: it.Key, Value: envDropField(it.Value, orderedset.OrderField)}
			}
			items = cleaned
		}
		var set lkal.Record
		set, err = orderedset.FromItems(items)
		if err == nil {
			if hooks.fromItemsStepOne {
				for i, fld := range set.Fields {
					r := fld.Value.(lkal.Record)
					set.Fields[i].Value = envSetField(r, orderedset.OrderField, lkal.Int(int64(i+1)))
				}
			}
			res.OK = true
			res.Set = set
			return res, nil
		}

	case "fromKeyedSlice":
		if hooks.fromKeyedAllowOrderKeyField && tc.KeyField == orderedset.OrderField {
			res.OK = true
			res.Set = envRec()
			return res, nil
		}
		var set lkal.Record
		set, err = orderedset.FromKeyedSlice(tc.Elems, tc.KeyField)
		if err == nil {
			if hooks.fromKeyedKeepKeyField {
				for i, fld := range set.Fields {
					r := fld.Value.(lkal.Record)
					set.Fields[i].Value = envSetField(r, tc.KeyField, lkal.String(fld.Key))
				}
			}
			res.OK = true
			res.Set = set
			return res, nil
		}

	case "toKeyedSlice":
		set := tc.Set
		if hooks.toKeyedOverwriteKeyField {
			cleaned := envCloneRecord(set)
			for i, fld := range cleaned.Fields {
				if r, ok := fld.Value.(lkal.Record); ok {
					cleaned.Fields[i].Value = envDropField(r, tc.KeyField)
				}
			}
			set = cleaned
		}
		var elems []lkal.Record
		elems, err = orderedset.ToKeyedSlice(set, tc.KeyField)
		if err == nil {
			res.OK = true
			res.Elems = elems
			return res, nil
		}

	case "toKeyedSliceReverse":
		var elems []lkal.Record
		if hooks.toKeyedReverseNotReversed {
			elems, err = orderedset.ToKeyedSlice(tc.Set, tc.KeyField)
		} else {
			elems, err = orderedset.ToKeyedSliceReverse(tc.Set, tc.KeyField)
		}
		if err == nil {
			res.OK = true
			res.Elems = elems
			return res, nil
		}

	case "orderBefore":
		if hooks.orderBeforeIgnoreOverflow {
			res.OK = true
			res.Order = tc.First - orderedset.Step
			return res, nil
		}
		var ord int64
		ord, err = orderedset.OrderBefore(tc.First)
		if err == nil {
			res.OK = true
			res.Order = ord
			return res, nil
		}

	case "orderAfter":
		if hooks.orderAfterIgnoreOverflow {
			res.OK = true
			res.Order = tc.Last + orderedset.Step
			return res, nil
		}
		var ord int64
		ord, err = orderedset.OrderAfter(tc.Last)
		if err == nil {
			res.OK = true
			res.Order = ord
			return res, nil
		}

	case "orderBetween":
		if hooks.orderBetweenAdjacentReturnsLo && tc.Lo < tc.Hi && tc.Lo == tc.Hi-1 {
			res.OK = true
			res.Order = tc.Lo
			return res, nil
		}
		var ord int64
		ord, err = orderedset.OrderBetween(tc.Lo, tc.Hi)
		if err == nil {
			if hooks.orderBetweenTruncateZero {
				ord = (tc.Lo + tc.Hi) / 2
			}
			res.OK = true
			res.Order = ord
			return res, nil
		}

	case "renumber":
		var set lkal.Record
		set, err = orderedset.Renumber(tc.Set)
		if err == nil {
			if hooks.renumberKeepOldOrders {
				set = envCloneRecord(tc.Set)
			}
			res.OK = true
			res.Set = set
			return res, nil
		}

	default:
		return orderedSetResult{}, fmt.Errorf("unknown orderedset op %q", tc.Op)
	}

	errName, mapErr := orderedSetErrName(err)
	if mapErr != nil {
		return orderedSetResult{}, mapErr
	}
	res.Err = errName
	return res, nil
}

func decodeOrderedSetEntries(raw json.RawMessage) ([]orderedset.Entry, error) {
	var arr []json.RawMessage
	if err := decodeStrictJSON(raw, &arr); err != nil {
		return nil, err
	}
	out := make([]orderedset.Entry, len(arr))
	for i, elem := range arr {
		var obj map[string]json.RawMessage
		if err := decodeStrictJSON(elem, &obj); err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		rawKey, hasKey := obj["key"]
		rawOrd, hasOrd := obj["order"]
		rawVal, hasVal := obj["value"]
		if len(obj) != 3 || !hasKey || !hasOrd || !hasVal {
			return nil, fmt.Errorf("[%d]: entry must have exact keys key, order, value: %s", i, elem)
		}
		k, err := decodeString(rawKey)
		if err != nil {
			return nil, fmt.Errorf("[%d].key: %w", i, err)
		}
		ord, err := decodeWireIntStr(rawOrd)
		if err != nil {
			return nil, fmt.Errorf("[%d].order: %w", i, err)
		}
		val, err := decodeWireRecord(rawVal)
		if err != nil {
			return nil, fmt.Errorf("[%d].value: %w", i, err)
		}
		out[i] = orderedset.Entry{Key: k, Order: ord, Value: val}
	}
	return out, nil
}

func decodeOrderedSetElems(raw json.RawMessage) ([]lkal.Record, error) {
	var arr []json.RawMessage
	if err := decodeStrictJSON(raw, &arr); err != nil {
		return nil, err
	}
	out := make([]lkal.Record, len(arr))
	for i, elem := range arr {
		rec, err := decodeWireRecord(elem)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out[i] = rec
	}
	return out, nil
}

func decodeOrderedSetResult(op string, line []byte) (orderedSetResult, error) {
	trimmed := bytes.TrimSpace(line)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rawTop json.RawMessage
	if err := dec.Decode(&rawTop); err != nil {
		return orderedSetResult{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return orderedSetResult{}, fmt.Errorf("unexpected trailing data in result line: %v", err)
	}
	var top map[string]json.RawMessage
	if err := decodeStrictJSON(rawTop, &top); err != nil {
		return orderedSetResult{}, err
	}
	rawID, hasID := top["id"]
	rawOK, hasOK := top["ok"]
	if !hasID || !hasOK {
		return orderedSetResult{}, fmt.Errorf("result object missing id or ok: %s", rawTop)
	}
	id, err := decodeNonNegInt(rawID)
	if err != nil {
		return orderedSetResult{}, fmt.Errorf("invalid id: %w", err)
	}
	var ok bool
	if err := decodeStrictJSON(rawOK, &ok); err != nil {
		return orderedSetResult{}, fmt.Errorf("ok: %w", err)
	}
	if !ok {
		rawErr, hasErr := top["err"]
		if len(top) != 3 || !hasErr {
			return orderedSetResult{}, fmt.Errorf("ok=false result must have exact keys id, ok, err: %s", rawTop)
		}
		errName, err := decodeString(rawErr)
		if err != nil {
			return orderedSetResult{}, fmt.Errorf("err: %w", err)
		}
		if !slices.Contains(orderedSetErrClasses, errName) {
			return orderedSetResult{}, fmt.Errorf("unknown error class %q", errName)
		}
		return orderedSetResult{ID: id, Op: op, OK: false, Err: errName}, nil
	}

	res := orderedSetResult{ID: id, Op: op, OK: true}
	switch op {
	case "entries", "entriesReverse":
		rawEntries, hasEntries := top["entries"]
		if len(top) != 3 || !hasEntries {
			return orderedSetResult{}, fmt.Errorf("%s ok=true must have exact keys id, ok, entries: %s", op, rawTop)
		}
		entries, err := decodeOrderedSetEntries(rawEntries)
		if err != nil {
			return orderedSetResult{}, fmt.Errorf("entries: %w", err)
		}
		res.Entries = entries
	case "fromItems", "fromKeyedSlice", "renumber":
		rawSet, hasSet := top["set"]
		if len(top) != 3 || !hasSet {
			return orderedSetResult{}, fmt.Errorf("%s ok=true must have exact keys id, ok, set: %s", op, rawTop)
		}
		set, err := decodeWireRecord(rawSet)
		if err != nil {
			return orderedSetResult{}, fmt.Errorf("set: %w", err)
		}
		res.Set = set
	case "toKeyedSlice", "toKeyedSliceReverse":
		rawElems, hasElems := top["elems"]
		if len(top) != 3 || !hasElems {
			return orderedSetResult{}, fmt.Errorf("%s ok=true must have exact keys id, ok, elems: %s", op, rawTop)
		}
		elems, err := decodeOrderedSetElems(rawElems)
		if err != nil {
			return orderedSetResult{}, fmt.Errorf("elems: %w", err)
		}
		res.Elems = elems
	case "orderBefore", "orderAfter", "orderBetween":
		rawOrd, hasOrd := top["order"]
		if len(top) != 3 || !hasOrd {
			return orderedSetResult{}, fmt.Errorf("%s ok=true must have exact keys id, ok, order: %s", op, rawTop)
		}
		ord, err := decodeWireIntStr(rawOrd)
		if err != nil {
			return orderedSetResult{}, fmt.Errorf("order: %w", err)
		}
		res.Order = ord
	default:
		return orderedSetResult{}, fmt.Errorf("unknown op %q", op)
	}
	return res, nil
}

func compareOrderedSetResult(want, got orderedSetResult) string {
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
	case "entries", "entriesReverse":
		if len(want.Entries) != len(got.Entries) {
			return fmt.Sprintf("entries len mismatch: lean=%d go=%d", len(want.Entries), len(got.Entries))
		}
		for i := range want.Entries {
			w, g := want.Entries[i], got.Entries[i]
			if w.Key != g.Key {
				return fmt.Sprintf("entries[%d].key mismatch: lean=%q go=%q", i, w.Key, g.Key)
			}
			if w.Order != g.Order {
				return fmt.Sprintf("entries[%d].order mismatch: lean=%d go=%d", i, w.Order, g.Order)
			}
			if !lkal.Equal(w.Value, g.Value) {
				return fmt.Sprintf("entries[%d].value mismatch: lean=%s go=%s", i, formatWireDoc(w.Value), formatWireDoc(g.Value))
			}
		}
	case "fromItems", "fromKeyedSlice", "renumber":
		if !lkal.Equal(want.Set, got.Set) {
			return fmt.Sprintf("set mismatch:\nlean: %s\ngo:   %s", formatWireDoc(want.Set), formatWireDoc(got.Set))
		}
	case "toKeyedSlice", "toKeyedSliceReverse":
		if len(want.Elems) != len(got.Elems) {
			return fmt.Sprintf("elems len mismatch: lean=%d go=%d", len(want.Elems), len(got.Elems))
		}
		for i := range want.Elems {
			if !lkal.Equal(want.Elems[i], got.Elems[i]) {
				return fmt.Sprintf("elems[%d] mismatch: lean=%s go=%s", i, formatWireDoc(want.Elems[i]), formatWireDoc(got.Elems[i]))
			}
		}
	case "orderBefore", "orderAfter", "orderBetween":
		if want.Order != got.Order {
			return fmt.Sprintf("order mismatch: lean=%d go=%d", want.Order, got.Order)
		}
	}
	return ""
}

func leanOrderedSetResults(t *testing.T, cases []orderedSetCase) ([][]byte, [][]byte, []orderedSetResult) {
	t.Helper()
	encoded := encodeOrderedSetCases(t, cases)
	lines, err := RunModel("orderedset", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(lines), len(cases))
	}
	results := make([]orderedSetResult, len(lines))
	for i, line := range lines {
		res, err := decodeOrderedSetResult(cases[i].Op, line)
		if err != nil {
			t.Fatalf("id=%d (%s) decode error: %v\ncase: %s\nlean: %s", cases[i].ID, cases[i].Op, err, encoded[i], line)
		}
		results[i] = res
	}
	return encoded, lines, results
}

func formatOrderedSetBuckets(buckets map[string]int) string {
	parts := make([]string, 0, len(orderedSetRequiredBuckets))
	for _, b := range orderedSetRequiredBuckets {
		parts = append(parts, fmt.Sprintf("%s=%d", b, buckets[b]))
	}
	return strings.Join(parts, " ")
}

func TestOrderedSetMatchesLeanModel(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateOrderedSetCases(cfg.Seed, cfg.Cases)
	encoded, leanLines, leanResults := leanOrderedSetResults(t, cases)

	repro := fmt.Sprintf("NAYUTAR_DRT_SEED=%d NAYUTAR_DRT_CASES=%d go test -tags drt -run '^TestOrderedSetMatchesLeanModel$' ./core/drt/...", cfg.Seed, cfg.Cases)
	buckets := map[string]int{}
	for i, tc := range cases {
		goRes, err := runOrderedSetCase(tc, orderedSetHooks{})
		if err != nil {
			t.Fatalf("seed=%d id=%d (%s) go error: %v\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, tc.Op, err, encoded[i], leanLines[i], repro)
		}
		if diff := compareOrderedSetResult(leanResults[i], goRes); diff != "" {
			t.Fatalf("seed=%d id=%d (%s) mismatch (%s)\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, tc.Op, diff, encoded[i], leanLines[i], repro)
		}
		if goRes.OK {
			buckets[tc.Op+"/ok"]++
		} else {
			buckets[tc.Op+"/"+goRes.Err]++
		}
	}
	t.Logf("compared %d cases (seed=%d): %s", len(cases), cfg.Seed, formatOrderedSetBuckets(buckets))

	if cfg.Cases >= 100 {
		var missing []string
		for _, b := range orderedSetRequiredBuckets {
			if buckets[b] == 0 {
				missing = append(missing, b)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed=%d empty outcome buckets: %s", cfg.Seed, strings.Join(missing, ", "))
		}
	}
}

func TestOrderedSetCasesReachEveryOutcome(t *testing.T) {
	cases := generateOrderedSetCases(1, 1000)
	_, _, results := leanOrderedSetResults(t, cases)

	seen := map[string]int{}
	for i, tc := range cases {
		res := results[i]
		if res.OK {
			seen[tc.Op+"/ok"]++
			switch tc.Op {
			case "entries":
				if len(res.Entries) == 0 {
					seen["entries/ok-empty"]++
				}
				if len(res.Entries) >= 2 {
					seen["entries/ok-multi"]++
				}
			case "orderBetween":
				if res.Order < 0 {
					seen["orderBetween/ok-negative"]++
				}
			}
		} else {
			seen[tc.Op+"/"+res.Err]++
		}
	}

	expected := append(slices.Clone(orderedSetRequiredBuckets),
		"entries/ok-empty",
		"entries/ok-multi",
		"orderBetween/ok-negative",
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

func cloneOrderedSetResult(r orderedSetResult) orderedSetResult {
	out := orderedSetResult{
		ID:    r.ID,
		Op:    r.Op,
		OK:    r.OK,
		Err:   r.Err,
		Set:   envCloneRecord(r.Set),
		Order: r.Order,
	}
	if r.Entries != nil {
		out.Entries = make([]orderedset.Entry, len(r.Entries))
		for i, e := range r.Entries {
			out.Entries[i] = orderedset.Entry{
				Key:   e.Key,
				Order: e.Order,
				Value: envCloneRecord(e.Value),
			}
		}
	}
	if r.Elems != nil {
		out.Elems = make([]lkal.Record, len(r.Elems))
		for i, el := range r.Elems {
			out.Elems[i] = envCloneRecord(el)
		}
	}
	return out
}

func TestOrderedSetComparisonDetectsEveryFieldMutation(t *testing.T) {
	cases := generateOrderedSetCases(1, 300)
	_, leanLines, results := leanOrderedSetResults(t, cases)

	applied := map[string]int{}
	assertDetects := func(kind string, orig orderedSetResult, mutate func(r *orderedSetResult)) {
		t.Helper()
		mut := cloneOrderedSetResult(orig)
		mutate(&mut)
		applied[kind]++
		if compareOrderedSetResult(orig, mut) == "" {
			t.Fatalf("mutation %q on case %d (%s) was not detected", kind, orig.ID, orig.Op)
		}
	}

	for _, orig := range results {
		assertDetects("id+1", orig, func(r *orderedSetResult) { r.ID++ })
		assertDetects("op-changed", orig, func(r *orderedSetResult) { r.Op += "x" })
		assertDetects("flip-ok", orig, func(r *orderedSetResult) { r.OK = !r.OK })
		if orig.OK {
			assertDetects("err-set", orig, func(r *orderedSetResult) { r.Err = "malformed" })
			switch orig.Op {
			case "entries", "entriesReverse":
				assertDetects("entries-len", orig, func(r *orderedSetResult) {
					r.Entries = append(r.Entries, orderedset.Entry{Key: "extra", Order: 1})
				})
				if len(orig.Entries) > 0 {
					assertDetects("entries-key", orig, func(r *orderedSetResult) {
						r.Entries[0].Key += "x"
					})
					assertDetects("entries-order", orig, func(r *orderedSetResult) {
						r.Entries[0].Order++
					})
					assertDetects("entries-value", orig, func(r *orderedSetResult) {
						r.Entries[0].Value = envSetField(r.Entries[0].Value, "mutated", lkal.Bool(true))
					})
				}
			case "fromItems", "fromKeyedSlice", "renumber":
				assertDetects("set-changed", orig, func(r *orderedSetResult) {
					r.Set = envSetField(r.Set, "mutated", envRec(envField("order", lkal.Int(1))))
				})
			case "toKeyedSlice", "toKeyedSliceReverse":
				assertDetects("elems-len", orig, func(r *orderedSetResult) {
					r.Elems = append(r.Elems, envRec(envField("name", lkal.String("extra"))))
				})
				if len(orig.Elems) > 0 {
					assertDetects("elems-value", orig, func(r *orderedSetResult) {
						r.Elems[0] = envSetField(r.Elems[0], "mutated", lkal.Bool(true))
					})
				}
			case "orderBefore", "orderAfter", "orderBetween":
				assertDetects("order+1", orig, func(r *orderedSetResult) {
					r.Order++
				})
			}
		} else {
			assertDetects("err-cleared", orig, func(r *orderedSetResult) { r.Err = "" })
			assertDetects("err-changed", orig, func(r *orderedSetResult) { r.Err += "x" })
		}
	}

	for _, kind := range []string{
		"id+1", "op-changed", "flip-ok", "err-set", "err-cleared", "err-changed",
		"entries-len", "entries-key", "entries-order", "entries-value",
		"set-changed", "elems-len", "elems-value", "order+1",
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
		{"missing id", "entries", `{"ok":false,"err":"malformed"}`},
		{"missing ok", "entries", `{"id":0,"err":"malformed"}`},
		{"negative id", "entries", `{"id":-1,"ok":false,"err":"malformed"}`},
		{"ok=false unknown err", "entries", `{"id":0,"ok":false,"err":"boom"}`},
		{"ok=false missing err", "entries", `{"id":0,"ok":false}`},
		{"ok=false extra payload", "entries", `{"id":0,"ok":false,"err":"malformed","entries":[]}`},
		{"entries missing entries key", "entries", `{"id":0,"ok":true,"set":{"o":[]}}`},
		{"entries null entries", "entries", `{"id":0,"ok":true,"entries":null}`},
		{"entries entry extra field", "entries", `{"id":0,"ok":true,"entries":[{"key":"a","order":"10","value":{"o":[]},"x":1}]}`},
		{"entries non-int order", "entries", `{"id":0,"ok":true,"entries":[{"key":"a","order":"abc","value":{"o":[]}}]}`},
		{"entries non-record value", "entries", `{"id":0,"ok":true,"entries":[{"key":"a","order":"10","value":{"s":"x"}}]}`},
		{"fromItems wrong key", "fromItems", `{"id":0,"ok":true,"entries":[]}`},
		{"fromItems non-record set", "fromItems", `{"id":0,"ok":true,"set":{"s":"x"}}`},
		{"toKeyedSlice null elems", "toKeyedSlice", `{"id":0,"ok":true,"elems":null}`},
		{"toKeyedSlice non-record elem", "toKeyedSlice", `{"id":0,"ok":true,"elems":[{"i":"1"}]}`},
		{"orderBetween numeric order", "orderBetween", `{"id":0,"ok":true,"order":15}`},
		{"orderBetween bad int order", "orderBetween", `{"id":0,"ok":true,"order":"99999999999999999999999"}`},
		{"trailing object", "orderBefore", `{"id":0,"ok":true,"order":"0"}{"id":1,"ok":true,"order":"1"}`},
	}
	for _, tc := range invalidLines {
		if _, err := decodeOrderedSetResult(tc.op, []byte(tc.line)); err == nil {
			t.Errorf("decodeOrderedSetResult(%s) accepted %s: %s", tc.op, tc.name, tc.line)
		}
	}
}

func TestOrderedSetDRTCatchesBrokenImplementations(t *testing.T) {
	cases := generateOrderedSetCases(1, 640)
	_, _, leanResults := leanOrderedSetResults(t, cases)

	variants := []struct {
		name  string
		hooks orderedSetHooks
	}{
		{"entries sort descending", orderedSetHooks{entriesSortDescending: true}},
		{"entries keep order field", orderedSetHooks{entriesKeepOrderField: true}},
		{"entries allow duplicate order", orderedSetHooks{entriesAllowDuplicateOrder: true}},
		{"entries duplicateOrder before malformed", orderedSetHooks{entriesDuplicateBeforeMalformed: true}},
		{"entriesReverse not reversed", orderedSetHooks{entriesReverseNotReversed: true}},
		{"fromItems overwrite order", orderedSetHooks{fromItemsOverwriteOrder: true}},
		{"fromItems duplicateKey before reservedField", orderedSetHooks{fromItemsDuplicateBeforeReserved: true}},
		{"fromItems step 1", orderedSetHooks{fromItemsStepOne: true}},
		{"fromKeyedSlice allow order keyField", orderedSetHooks{fromKeyedAllowOrderKeyField: true}},
		{"fromKeyedSlice keep keyField", orderedSetHooks{fromKeyedKeepKeyField: true}},
		{"toKeyedSlice overwrite keyField", orderedSetHooks{toKeyedOverwriteKeyField: true}},
		{"toKeyedSliceReverse not reversed", orderedSetHooks{toKeyedReverseNotReversed: true}},
		{"orderBefore ignore overflow", orderedSetHooks{orderBeforeIgnoreOverflow: true}},
		{"orderAfter ignore overflow", orderedSetHooks{orderAfterIgnoreOverflow: true}},
		{"orderBetween truncate toward zero", orderedSetHooks{orderBetweenTruncateZero: true}},
		{"orderBetween adjacent returns lo", orderedSetHooks{orderBetweenAdjacentReturnsLo: true}},
		{"renumber keep old orders", orderedSetHooks{renumberKeepOldOrders: true}},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			mismatches := 0
			for i, tc := range cases {
				goRes, err := runOrderedSetCase(tc, v.hooks)
				if err != nil {
					t.Fatalf("id=%d (%s): broken implementation failed instead of answering wrongly: %v", tc.ID, tc.Op, err)
				}
				if compareOrderedSetResult(leanResults[i], goRes) != "" {
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

func TestGenerateOrderedSetCasesIsDeterministic(t *testing.T) {
	a := bytes.Join(encodeOrderedSetCases(t, generateOrderedSetCases(1, 100)), []byte("\n"))
	b := bytes.Join(encodeOrderedSetCases(t, generateOrderedSetCases(1, 100)), []byte("\n"))
	c := bytes.Join(encodeOrderedSetCases(t, generateOrderedSetCases(2, 100)), []byte("\n"))

	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different encoded cases")
	}
	if bytes.Equal(a, c) {
		t.Fatal("different seeds produced identical encoded cases")
	}
}

func TestOrderedSetModelRejectsMalformedCase(t *testing.T) {
	valid := `{"id":0,"op":"entries","set":{"o":[]}}`
	for _, bad := range []string{
		`{"id":1,"op":"explode"}`,
		`{"id":1,"op":"entries","set":{"s":"not-a-record"}}`,
		`{"id":1,"op":"fromItems","items":[{"key":"a"}]}`,
		`{"id":1,"op":"fromKeyedSlice","elems":[]}`,
		`{"id":1,"op":"orderBefore","first":10}`,
		`{"id":1,"op":"orderAfter","last":"not-an-int"}`,
		`{"id":1,"op":"orderBetween","lo":"1"}`,
	} {
		cases := [][]byte{[]byte(valid), []byte(bad)}
		_, err := RunModel("orderedset", cases)
		if err == nil {
			t.Errorf("RunModel accepted malformed case %s", bad)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte("line 2")) {
			t.Errorf("error %q does not name line 2 for %s", err.Error(), bad)
		}
	}
}
