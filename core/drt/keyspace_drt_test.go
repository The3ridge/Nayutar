//go:build drt

package drt

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

type keyspaceKey struct {
	Family  string `json:"family"`
	Tenant  string `json:"tenant"`
	Group   string `json:"group"`
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	SGroup  string `json:"sgroup"`
	SKind   string `json:"skind"`
	SName   string `json:"sname"`
	Hash    string `json:"hash"`
	Version string `json:"version"`
	Node    string `json:"node"`
}

type keyspaceCase struct {
	ID     int
	Op     string
	Key    keyspaceKey
	Tenant string
	Text   string
}

func (c keyspaceCase) MarshalJSON() ([]byte, error) {
	switch c.Op {
	case "encode", "rel":
		return json.Marshal(struct {
			ID  int         `json:"id"`
			Op  string      `json:"op"`
			Key keyspaceKey `json:"key"`
		}{c.ID, c.Op, c.Key})
	case "parse":
		return json.Marshal(struct {
			ID   int    `json:"id"`
			Op   string `json:"op"`
			Text string `json:"text"`
		}{c.ID, c.Op, c.Text})
	case "parserel":
		return json.Marshal(struct {
			ID     int    `json:"id"`
			Op     string `json:"op"`
			Tenant string `json:"tenant"`
			Text   string `json:"text"`
		}{c.ID, c.Op, c.Tenant, c.Text})
	default:
		return nil, fmt.Errorf("unknown op %q", c.Op)
	}
}

type keyspaceResult struct {
	ID   int         `json:"id"`
	OK   bool        `json:"ok"`
	Text string      `json:"text"`
	Key  keyspaceKey `json:"key"`
}

var (
	keyspaceOps         = []string{"encode", "rel", "parse", "parserel"}
	keyspaceFamilyNames = []string{"object", "ownership", "backref", "binding", "schema", "tenant", "node", "eviction_budget"}
	keyspaceRelFamilies = []string{"object", "ownership", "backref", "binding"}
	keyspaceAlnum       = "abcdefghijklmnopqrstuvwxyz0123456789"
	keyspaceHexLower    = "0123456789abcdef"
	keyspaceSampleHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	keyspaceMax253Group = strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	keyspaceMax254Group = strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 62)

	keyspaceHostileValues = []string{
		"",
		".",
		"..",
		"a..b",
		".a",
		"a.",
		"-a",
		"a-",
		"A",
		"a_b",
		"a/b",
		"a%2Fb",
		"a b",
		"a\x00",
		"\x7f",
		"é",
		"α",
		"core",
		"apps",
		"x.core",
		"widget.apps",
		strings.Repeat("a", 64),
		strings.Repeat("a", 254),
		keyspaceMax254Group,
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0",
		"0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		strings.Repeat("0", 31) + "g" + strings.Repeat("0", 32),
	}

	keyspaceBackRefValidQKinds = []string{
		"secret",
		"widget.widgets.example.com",
		"core",
		"a.b.c",
		"widget." + strings.Repeat("a", 64) + ".b",
		"widget." + strings.Repeat("a", 100) + ".example.com",
		strings.Repeat("k", 63),
		strings.Repeat("k", 63) + "." + keyspaceMax253Group,
	}

	keyspaceBackRefNearMissQKinds = []string{
		"widget.apps",
		"x.core",
		"core.core",
		"a.b",
		".widgets.example.com",
		"widget.",
		"widget..example.com",
		"widget.a.b.",
		"Widget.widgets.example.com",
		"widget.Widgets.example.com",
		strings.Repeat("k", 64) + ".widgets.example.com",
		"widget." + keyspaceMax254Group,
	}

	keyspaceBackRefAllQKinds = append(append([]string{}, keyspaceBackRefValidQKinds...), keyspaceBackRefNearMissQKinds...)

	keyspaceBackRefGroupPool = []string{
		"core",
		"apps",
		"x.core",
		"core.core",
		"a.b",
		strings.Repeat("a", 64) + ".b",
		strings.Repeat("a", 100) + ".example.com",
		keyspaceMax253Group,
		keyspaceMax254Group,
	}

	keyspaceMutationRunes = []rune{'a', '0', '-', '.', '/', 'A', '_', '%', ' ', '\x00', '\x7f', 'é', 'α'}

	keyspaceSegmentPool = append([]string{
		"", "t", "x", "sys", "o", "own", "ref", "bind", "schema", "tenants",
		"nodes", "liveness", "evictions",
		"a", "pod", "acme", "v1", "a.b", keyspaceSampleHash,
	}, keyspaceHostileValues...)
)

func genKeyspaceLabelLen(rng *rand.Rand, length int) string {
	b := make([]byte, length)
	for i := range length {
		if i > 0 && i < length-1 && chance(rng, 15) {
			b[i] = '-'
			continue
		}
		b[i] = keyspaceAlnum[rng.IntN(len(keyspaceAlnum))]
	}
	return string(b)
}

func genKeyspaceLabel(rng *rand.Rand) string {
	r := rng.IntN(100)
	switch {
	case r < 15:
		return genKeyspaceLabelLen(rng, 1)
	case r < 30:
		return genKeyspaceLabelLen(rng, 63)
	case r < 80:
		return genKeyspaceLabelLen(rng, 2+rng.IntN(7))
	default:
		return genKeyspaceLabelLen(rng, 9+rng.IntN(54))
	}
}

func genKeyspaceMaxSubdomain(rng *rand.Rand, minElems int) string {
	if minElems == 1 && chance(rng, 35) {
		return genKeyspaceLabelLen(rng, 253)
	}
	if chance(rng, 50) {
		l1 := 64 + rng.IntN(137)
		l2 := 253 - 1 - l1
		return genKeyspaceLabelLen(rng, l1) + "." + genKeyspaceLabelLen(rng, l2)
	}
	rem := 253
	var elems []string
	for rem > 63 {
		maxLen := min(63, rem-2)
		minLen := min(20, maxLen)
		l := minLen + rng.IntN(maxLen-minLen+1)
		elems = append(elems, genKeyspaceLabelLen(rng, l))
		rem -= l + 1
	}
	elems = append(elems, genKeyspaceLabelLen(rng, rem))
	return strings.Join(elems, ".")
}

func genKeyspaceSubdomain(rng *rand.Rand, minElems int) string {
	if minElems == 1 && chance(rng, 25) {
		return genKeyspaceLabel(rng)
	}
	if minElems == 1 && chance(rng, 15) {
		return genKeyspaceLabelLen(rng, 64+rng.IntN(137))
	}
	if chance(rng, 18) {
		return genKeyspaceMaxSubdomain(rng, minElems)
	}
	if chance(rng, 25) {
		count := max(minElems, 2) + rng.IntN(2)
		longIdx := rng.IntN(count)
		elems := make([]string, count)
		for i := range count {
			if i == longIdx {
				elems[i] = genKeyspaceLabelLen(rng, 64+rng.IntN(137))
			} else if chance(rng, 20) {
				elems[i] = genKeyspaceLabelLen(rng, 1)
			} else {
				elems[i] = genKeyspaceLabelLen(rng, 2+rng.IntN(11))
			}
		}
		return strings.Join(elems, ".")
	}
	count := max(minElems, 2) + rng.IntN(4)
	elems := make([]string, count)
	for i := range count {
		if chance(rng, 20) {
			elems[i] = genKeyspaceLabelLen(rng, 1)
		} else {
			elems[i] = genKeyspaceLabelLen(rng, 2+rng.IntN(12))
		}
	}
	return strings.Join(elems, ".")
}

func genKeyspaceGroup(rng *rand.Rand) string {
	if chance(rng, 45) {
		return keyspace.CoreGroup
	}
	return genKeyspaceSubdomain(rng, 2)
}

func genKeyspaceHash(rng *rand.Rand) string {
	b := make([]byte, 64)
	for i := range 64 {
		b[i] = keyspaceHexLower[rng.IntN(len(keyspaceHexLower))]
	}
	return string(b)
}

func genValidKeyspaceKey(rng *rand.Rand, fam string) keyspaceKey {
	switch fam {
	case "object", "ownership":
		return keyspaceKey{
			Family: fam,
			Tenant: genKeyspaceLabel(rng),
			Group:  genKeyspaceGroup(rng),
			Kind:   genKeyspaceLabel(rng),
			Name:   genKeyspaceSubdomain(rng, 1),
		}
	case "backref":
		return keyspaceKey{
			Family: fam,
			Tenant: genKeyspaceLabel(rng),
			Group:  genKeyspaceGroup(rng),
			Kind:   genKeyspaceLabel(rng),
			Name:   genKeyspaceSubdomain(rng, 1),
			SGroup: genKeyspaceGroup(rng),
			SKind:  genKeyspaceLabel(rng),
			SName:  genKeyspaceSubdomain(rng, 1),
		}
	case "binding":
		return keyspaceKey{
			Family: fam,
			Tenant: genKeyspaceLabel(rng),
			Hash:   genKeyspaceHash(rng),
		}
	case "schema":
		return keyspaceKey{
			Family:  fam,
			Hash:    genKeyspaceHash(rng),
			Version: genKeyspaceLabel(rng),
		}
	case "tenant":
		return keyspaceKey{
			Family: fam,
			Tenant: genKeyspaceLabel(rng),
		}
	case "node":
		return keyspaceKey{
			Family: fam,
			Node:   genKeyspaceSubdomain(rng, 1),
		}
	case "eviction_budget":
		return keyspaceKey{
			Family: fam,
		}
	default:
		panic("unknown family: " + fam)
	}
}

type keyspaceField int

const (
	ksFieldTenant keyspaceField = iota
	ksFieldGroup
	ksFieldKind
	ksFieldName
	ksFieldSGroup
	ksFieldSKind
	ksFieldSName
	ksFieldHash
	ksFieldVersion
	ksFieldNode
)

func setKeyspaceField(k *keyspaceKey, f keyspaceField, v string) {
	switch f {
	case ksFieldTenant:
		k.Tenant = v
	case ksFieldGroup:
		k.Group = v
	case ksFieldKind:
		k.Kind = v
	case ksFieldName:
		k.Name = v
	case ksFieldSGroup:
		k.SGroup = v
	case ksFieldSKind:
		k.SKind = v
	case ksFieldSName:
		k.SName = v
	case ksFieldHash:
		k.Hash = v
	case ksFieldVersion:
		k.Version = v
	case ksFieldNode:
		k.Node = v
	}
}

func keyspaceFieldPartition(fam string) (used, unused []keyspaceField) {
	switch fam {
	case "object", "ownership":
		return []keyspaceField{ksFieldTenant, ksFieldGroup, ksFieldKind, ksFieldName},
			[]keyspaceField{ksFieldSGroup, ksFieldSKind, ksFieldSName, ksFieldHash, ksFieldVersion, ksFieldNode}
	case "backref":
		return []keyspaceField{ksFieldTenant, ksFieldGroup, ksFieldKind, ksFieldName, ksFieldSGroup, ksFieldSKind, ksFieldSName},
			[]keyspaceField{ksFieldHash, ksFieldVersion, ksFieldNode}
	case "binding":
		return []keyspaceField{ksFieldTenant, ksFieldHash},
			[]keyspaceField{ksFieldGroup, ksFieldKind, ksFieldName, ksFieldSGroup, ksFieldSKind, ksFieldSName, ksFieldVersion, ksFieldNode}
	case "schema":
		return []keyspaceField{ksFieldHash, ksFieldVersion},
			[]keyspaceField{ksFieldTenant, ksFieldGroup, ksFieldKind, ksFieldName, ksFieldSGroup, ksFieldSKind, ksFieldSName, ksFieldNode}
	case "tenant":
		return []keyspaceField{ksFieldTenant},
			[]keyspaceField{ksFieldGroup, ksFieldKind, ksFieldName, ksFieldSGroup, ksFieldSKind, ksFieldSName, ksFieldHash, ksFieldVersion, ksFieldNode}
	case "node":
		return []keyspaceField{ksFieldNode},
			[]keyspaceField{ksFieldTenant, ksFieldGroup, ksFieldKind, ksFieldName, ksFieldSGroup, ksFieldSKind, ksFieldSName, ksFieldHash, ksFieldVersion}
	case "eviction_budget":
		return nil,
			[]keyspaceField{ksFieldTenant, ksFieldGroup, ksFieldKind, ksFieldName, ksFieldSGroup, ksFieldSKind, ksFieldSName, ksFieldHash, ksFieldVersion, ksFieldNode}
	default:
		panic("unknown family: " + fam)
	}
}

func genNearValidKeyspaceKey(rng *rand.Rand, fam string) keyspaceKey {
	k := genValidKeyspaceKey(rng, fam)
	used, unused := keyspaceFieldPartition(fam)
	if len(used) > 0 && chance(rng, 60) {
		f := pick(rng, used)
		setKeyspaceField(&k, f, pick(rng, keyspaceHostileValues))
		return k
	}
	f := pick(rng, unused)
	var v string
	if chance(rng, 60) {
		switch f {
		case ksFieldHash:
			v = genKeyspaceHash(rng)
		case ksFieldGroup, ksFieldSGroup:
			v = "core"
		default:
			v = "x"
		}
	} else {
		for {
			v = pick(rng, keyspaceHostileValues)
			if v != "" {
				break
			}
		}
	}
	setKeyspaceField(&k, f, v)
	return k
}

func encodeValidKeyspaceKey(k keyspaceKey, rel bool) string {
	typed, err := toKeyspaceKey(k)
	if err != nil {
		panic(err)
	}
	var out string
	if rel {
		out, err = typed.Rel()
	} else {
		out, err = typed.Encode()
	}
	if err != nil {
		panic(err)
	}
	return out
}

func mutateKeyspaceString(rng *rand.Rand, s string, rel bool) string {
	switch rng.IntN(7) {
	case 0:
		runes := []rune(s)
		pos := rng.IntN(len(runes) + 1)
		ins := pick(rng, keyspaceMutationRunes)
		out := make([]rune, 0, len(runes)+1)
		out = append(out, runes[:pos]...)
		out = append(out, ins)
		out = append(out, runes[pos:]...)
		return string(out)
	case 1:
		runes := []rune(s)
		pos := rng.IntN(len(runes))
		return string(append(runes[:pos], runes[pos+1:]...))
	case 2:
		runes := []rune(s)
		pos := rng.IntN(len(runes))
		runes[pos] = pick(rng, keyspaceMutationRunes)
		return string(runes)
	case 3:
		return s + "/"
	case 4:
		var slashes []int
		for i := range len(s) {
			if s[i] == '/' {
				slashes = append(slashes, i)
			}
		}
		if len(slashes) == 0 {
			return "/" + s
		}
		idx := pick(rng, slashes)
		return s[:idx] + "/" + s[idx:]
	case 5:
		segs := strings.Split(s, "/")
		relAreas := []string{"o", "own", "ref", "bind", "schema", "tenants", "other", "", "O"}
		if rel {
			segs[0] = pick(rng, relAreas)
			return strings.Join(segs, "/")
		}
		switch segs[1] {
		case "t":
			if len(segs) > 3 {
				segs[3] = pick(rng, relAreas)
			}
		case "x":
			if len(segs) > 3 {
				segs[3] = pick(rng, []string{"schemas", "o", "bind", "other", ""})
			}
		case "sys":
			if chance(rng, 30) {
				segs[1] = pick(rng, []string{"t", "x", "system", ""})
			} else if len(segs) > 3 && segs[2] == "liveness" && chance(rng, 50) {
				segs[3] = pick(rng, []string{"eviction", "evictions", "other", ""})
			} else if len(segs) > 2 {
				segs[2] = pick(rng, []string{"tenant", "tenants", "nodes", "node", "liveness", "o", "schema", "other", ""})
			}
		}
		return strings.Join(segs, "/")
	default:
		segs := strings.Split(s, "/")
		if chance(rng, 50) && len(segs) > 1 {
			drop := rng.IntN(len(segs))
			return strings.Join(slices.Delete(segs, drop, drop+1), "/")
		}
		pos := rng.IntN(len(segs) + 1)
		ins := pick(rng, keyspaceSegmentPool)
		return strings.Join(slices.Insert(segs, pos, ins), "/")
	}
}

func assembleKeyspaceString(rng *rand.Rand, rel bool) string {
	count := 1 + rng.IntN(6)
	segs := make([]string, count)
	for i := range count {
		segs[i] = pick(rng, keyspaceSegmentPool)
	}
	s := strings.Join(segs, "/")
	if !rel && chance(rng, 70) {
		s = "/" + s
	} else if rel && chance(rng, 20) {
		s = "/" + s
	}
	if chance(rng, 15) {
		s += "/"
	}
	return s
}

func genBackRefEncodeKey(rng *rand.Rand) keyspaceKey {
	pickGroup := func() string {
		if chance(rng, 25) {
			return genKeyspaceSubdomain(rng, 2)
		}
		return pick(rng, keyspaceBackRefGroupPool)
	}
	pickKind := func() string {
		if chance(rng, 80) {
			return genKeyspaceLabel(rng)
		}
		return pick(rng, keyspaceHostileValues)
	}
	pickName := func() string {
		if chance(rng, 85) {
			return genKeyspaceSubdomain(rng, 1)
		}
		return pick(rng, keyspaceHostileValues)
	}
	tenant := genKeyspaceLabel(rng)
	if chance(rng, 10) {
		tenant = pick(rng, keyspaceHostileValues)
	}
	return keyspaceKey{
		Family: "backref",
		Tenant: tenant,
		Group:  pickGroup(),
		Kind:   pickKind(),
		Name:   pickName(),
		SGroup: pickGroup(),
		SKind:  pickKind(),
		SName:  pickName(),
	}
}

func genBackRefName(rng *rand.Rand) string {
	if chance(rng, 80) {
		return genKeyspaceSubdomain(rng, 1)
	}
	return pick(rng, keyspaceHostileValues)
}

func genBackRefQKindPair(rng *rand.Rand) (string, string) {
	r := rng.IntN(100)
	switch {
	case r < 25:
		return pick(rng, keyspaceBackRefValidQKinds), pick(rng, keyspaceBackRefValidQKinds)
	case r < 75:
		valid := pick(rng, keyspaceBackRefValidQKinds)
		near := pick(rng, keyspaceBackRefNearMissQKinds)
		if chance(rng, 50) {
			return near, valid
		}
		return valid, near
	default:
		return pick(rng, keyspaceBackRefAllQKinds), pick(rng, keyspaceBackRefAllQKinds)
	}
}

func genBackRefParseCase(rng *rand.Rand, rel bool) (tenant, text string) {
	if rel && chance(rng, 15) {
		tenant = pick(rng, keyspaceHostileValues)
	} else {
		tenant = genKeyspaceLabel(rng)
	}
	tQKind, sQKind := genBackRefQKindPair(rng)
	tName := genBackRefName(rng)
	sName := genBackRefName(rng)
	relText := "ref/" + tQKind + "/" + tName + "/" + sQKind + "/" + sName
	if rel {
		return tenant, relText
	}
	if chance(rng, 5) {
		tenant = pick(rng, keyspaceHostileValues)
	}
	return "", "/t/" + tenant + "/" + relText
}

func generateKeyspaceCases(seed uint64, n int) []keyspaceCase {
	rng := rand.New(rand.NewPCG(seed, 0x736b657973706163))
	cases := make([]keyspaceCase, n)
	for i := range n {
		op := pick(rng, keyspaceOps)
		tc := keyspaceCase{ID: i, Op: op}
		switch op {
		case "encode":
			r := rng.IntN(100)
			switch {
			case r < 16:
				tc.Key = genBackRefEncodeKey(rng)
			case r < 58:
				tc.Key = genValidKeyspaceKey(rng, pick(rng, keyspaceFamilyNames))
			default:
				tc.Key = genNearValidKeyspaceKey(rng, pick(rng, keyspaceFamilyNames))
			}
		case "rel":
			r := rng.IntN(100)
			switch {
			case r < 16:
				tc.Key = genBackRefEncodeKey(rng)
			case r < 56:
				tc.Key = genValidKeyspaceKey(rng, pick(rng, keyspaceRelFamilies))
			case r < 70:
				tc.Key = genValidKeyspaceKey(rng, pick(rng, []string{"schema", "tenant", "node", "eviction_budget"}))
			default:
				tc.Key = genNearValidKeyspaceKey(rng, pick(rng, keyspaceRelFamilies))
			}
		case "parse":
			r := rng.IntN(100)
			switch {
			case r < 16:
				_, tc.Text = genBackRefParseCase(rng, false)
			case r < 46:
				k := genValidKeyspaceKey(rng, pick(rng, keyspaceFamilyNames))
				tc.Text = encodeValidKeyspaceKey(k, false)
			case r < 80:
				k := genValidKeyspaceKey(rng, pick(rng, keyspaceFamilyNames))
				tc.Text = mutateKeyspaceString(rng, encodeValidKeyspaceKey(k, false), false)
			default:
				tc.Text = assembleKeyspaceString(rng, false)
			}
		case "parserel":
			r := rng.IntN(100)
			switch {
			case r < 16:
				tc.Tenant, tc.Text = genBackRefParseCase(rng, true)
			case r < 46:
				k := genValidKeyspaceKey(rng, pick(rng, keyspaceRelFamilies))
				tc.Tenant = k.Tenant
				tc.Text = encodeValidKeyspaceKey(k, true)
			case r < 80:
				k := genValidKeyspaceKey(rng, pick(rng, keyspaceRelFamilies))
				relText := encodeValidKeyspaceKey(k, true)
				if chance(rng, 20) {
					tc.Tenant = pick(rng, keyspaceHostileValues)
					tc.Text = relText
				} else {
					tc.Tenant = k.Tenant
					tc.Text = mutateKeyspaceString(rng, relText, true)
				}
			default:
				if chance(rng, 40) {
					tc.Tenant = pick(rng, keyspaceHostileValues)
				} else {
					tc.Tenant = genKeyspaceLabel(rng)
				}
				tc.Text = assembleKeyspaceString(rng, true)
			}
		}
		cases[i] = tc
	}
	return cases
}

func assertValidUTF8(t *testing.T, id int, field, val string) {
	t.Helper()
	if !utf8.ValidString(val) {
		t.Fatalf("case id=%d field %s is not valid UTF-8: %q", id, field, val)
	}
}

func encodeKeyspaceCases(t *testing.T, cases []keyspaceCase) [][]byte {
	t.Helper()
	encoded := make([][]byte, len(cases))
	for i, tc := range cases {
		switch tc.Op {
		case "encode", "rel":
			if _, err := keyspaceFamilyFromName(tc.Key.Family); err != nil {
				t.Fatalf("case id=%d invalid family %q", tc.ID, tc.Key.Family)
			}
			assertValidUTF8(t, tc.ID, "key.family", tc.Key.Family)
			assertValidUTF8(t, tc.ID, "key.tenant", tc.Key.Tenant)
			assertValidUTF8(t, tc.ID, "key.group", tc.Key.Group)
			assertValidUTF8(t, tc.ID, "key.kind", tc.Key.Kind)
			assertValidUTF8(t, tc.ID, "key.name", tc.Key.Name)
			assertValidUTF8(t, tc.ID, "key.sgroup", tc.Key.SGroup)
			assertValidUTF8(t, tc.ID, "key.skind", tc.Key.SKind)
			assertValidUTF8(t, tc.ID, "key.sname", tc.Key.SName)
			assertValidUTF8(t, tc.ID, "key.hash", tc.Key.Hash)
			assertValidUTF8(t, tc.ID, "key.version", tc.Key.Version)
			assertValidUTF8(t, tc.ID, "key.node", tc.Key.Node)
		case "parse":
			assertValidUTF8(t, tc.ID, "text", tc.Text)
		case "parserel":
			assertValidUTF8(t, tc.ID, "tenant", tc.Tenant)
			assertValidUTF8(t, tc.ID, "text", tc.Text)
		}
		b, err := json.Marshal(tc)
		if err != nil {
			t.Fatal(err)
		}
		encoded[i] = b
	}
	return encoded
}

func keyspaceFamilyFromName(name string) (keyspace.Family, error) {
	switch name {
	case "object":
		return keyspace.FamilyObject, nil
	case "ownership":
		return keyspace.FamilyOwnership, nil
	case "backref":
		return keyspace.FamilyBackRef, nil
	case "binding":
		return keyspace.FamilyBinding, nil
	case "schema":
		return keyspace.FamilySchema, nil
	case "tenant":
		return keyspace.FamilyTenant, nil
	case "node":
		return keyspace.FamilyNode, nil
	case "eviction_budget":
		return keyspace.FamilyEvictionBudget, nil
	default:
		return 0, fmt.Errorf("unknown family %q", name)
	}
}

func keyspaceFamilyName(f keyspace.Family) string {
	switch f {
	case keyspace.FamilyObject:
		return "object"
	case keyspace.FamilyOwnership:
		return "ownership"
	case keyspace.FamilyBackRef:
		return "backref"
	case keyspace.FamilyBinding:
		return "binding"
	case keyspace.FamilySchema:
		return "schema"
	case keyspace.FamilyTenant:
		return "tenant"
	case keyspace.FamilyNode:
		return "node"
	case keyspace.FamilyEvictionBudget:
		return "eviction_budget"
	default:
		return ""
	}
}

func toKeyspaceKey(k keyspaceKey) (keyspace.Key, error) {
	fam, err := keyspaceFamilyFromName(k.Family)
	if err != nil {
		return keyspace.Key{}, err
	}
	return keyspace.Key{
		Family: fam,
		Tenant: k.Tenant,
		Object: keyspace.Object{
			Group: k.Group,
			Kind:  k.Kind,
			Name:  k.Name,
		},
		Source: keyspace.Object{
			Group: k.SGroup,
			Kind:  k.SKind,
			Name:  k.SName,
		},
		IdentityHash: k.Hash,
		Version:      k.Version,
		Node:         k.Node,
	}, nil
}

func fromKeyspaceKey(k keyspace.Key) keyspaceKey {
	return keyspaceKey{
		Family:  keyspaceFamilyName(k.Family),
		Tenant:  k.Tenant,
		Group:   k.Object.Group,
		Kind:    k.Object.Kind,
		Name:    k.Object.Name,
		SGroup:  k.Source.Group,
		SKind:   k.Source.Kind,
		SName:   k.Source.Name,
		Hash:    k.IdentityHash,
		Version: k.Version,
		Node:    k.Node,
	}
}

func runKeyspaceCase(tc keyspaceCase) (keyspaceResult, error) {
	res := keyspaceResult{ID: tc.ID}
	switch tc.Op {
	case "encode":
		k, err := toKeyspaceKey(tc.Key)
		if err != nil {
			return keyspaceResult{}, err
		}
		text, err := k.Encode()
		if err == nil {
			res.OK = true
			res.Text = text
		}
	case "rel":
		k, err := toKeyspaceKey(tc.Key)
		if err != nil {
			return keyspaceResult{}, err
		}
		text, err := k.Rel()
		if err == nil {
			res.OK = true
			res.Text = text
		}
	case "parse":
		k, err := keyspace.Parse(tc.Text)
		if err == nil {
			res.OK = true
			res.Key = fromKeyspaceKey(k)
		}
	case "parserel":
		k, err := keyspace.ParseRel(tc.Tenant, tc.Text)
		if err == nil {
			res.OK = true
			res.Key = fromKeyspaceKey(k)
		}
	default:
		return keyspaceResult{}, fmt.Errorf("unknown op %q", tc.Op)
	}
	return res, nil
}

func decodeKeyspaceKey(raw json.RawMessage) (keyspaceKey, error) {
	obj, err := decodeObject(raw,
		"family", "tenant", "group", "kind", "name",
		"sgroup", "skind", "sname", "hash", "version", "node",
	)
	if err != nil {
		return keyspaceKey{}, err
	}
	var k keyspaceKey
	if k.Family, err = decodeString(obj["family"]); err != nil {
		return keyspaceKey{}, fmt.Errorf("family: %w", err)
	}
	if k.Tenant, err = decodeString(obj["tenant"]); err != nil {
		return keyspaceKey{}, fmt.Errorf("tenant: %w", err)
	}
	if k.Group, err = decodeString(obj["group"]); err != nil {
		return keyspaceKey{}, fmt.Errorf("group: %w", err)
	}
	if k.Kind, err = decodeString(obj["kind"]); err != nil {
		return keyspaceKey{}, fmt.Errorf("kind: %w", err)
	}
	if k.Name, err = decodeString(obj["name"]); err != nil {
		return keyspaceKey{}, fmt.Errorf("name: %w", err)
	}
	if k.SGroup, err = decodeString(obj["sgroup"]); err != nil {
		return keyspaceKey{}, fmt.Errorf("sgroup: %w", err)
	}
	if k.SKind, err = decodeString(obj["skind"]); err != nil {
		return keyspaceKey{}, fmt.Errorf("skind: %w", err)
	}
	if k.SName, err = decodeString(obj["sname"]); err != nil {
		return keyspaceKey{}, fmt.Errorf("sname: %w", err)
	}
	if k.Hash, err = decodeString(obj["hash"]); err != nil {
		return keyspaceKey{}, fmt.Errorf("hash: %w", err)
	}
	if k.Version, err = decodeString(obj["version"]); err != nil {
		return keyspaceKey{}, fmt.Errorf("version: %w", err)
	}
	if k.Node, err = decodeString(obj["node"]); err != nil {
		return keyspaceKey{}, fmt.Errorf("node: %w", err)
	}
	return k, nil
}

func decodeKeyspaceResult(line []byte) (keyspaceResult, error) {
	trimmed := bytes.TrimSpace(line)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rawTop json.RawMessage
	if err := dec.Decode(&rawTop); err != nil {
		return keyspaceResult{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return keyspaceResult{}, fmt.Errorf("unexpected trailing data in result line: %v", err)
	}
	top, err := decodeObject(rawTop, "id", "ok", "text", "key")
	if err != nil {
		return keyspaceResult{}, err
	}
	id, err := decodeNonNegInt(top["id"])
	if err != nil {
		return keyspaceResult{}, fmt.Errorf("invalid id: %w", err)
	}
	var ok bool
	if err := decodeStrictJSON(top["ok"], &ok); err != nil {
		return keyspaceResult{}, fmt.Errorf("ok: %w", err)
	}
	text, err := decodeString(top["text"])
	if err != nil {
		return keyspaceResult{}, fmt.Errorf("text: %w", err)
	}
	key, err := decodeKeyspaceKey(top["key"])
	if err != nil {
		return keyspaceResult{}, fmt.Errorf("key: %w", err)
	}
	return keyspaceResult{ID: id, OK: ok, Text: text, Key: key}, nil
}

func compareKeyspaceResult(want, got keyspaceResult) string {
	switch {
	case want.ID != got.ID:
		return fmt.Sprintf("id mismatch: lean=%d go=%d", want.ID, got.ID)
	case want.OK != got.OK:
		return fmt.Sprintf("ok mismatch: lean=%v go=%v", want.OK, got.OK)
	case want.Text != got.Text:
		return fmt.Sprintf("text mismatch: lean=%q go=%q", want.Text, got.Text)
	case want.Key.Family != got.Key.Family:
		return fmt.Sprintf("key.family mismatch: lean=%q go=%q", want.Key.Family, got.Key.Family)
	case want.Key.Tenant != got.Key.Tenant:
		return fmt.Sprintf("key.tenant mismatch: lean=%q go=%q", want.Key.Tenant, got.Key.Tenant)
	case want.Key.Group != got.Key.Group:
		return fmt.Sprintf("key.group mismatch: lean=%q go=%q", want.Key.Group, got.Key.Group)
	case want.Key.Kind != got.Key.Kind:
		return fmt.Sprintf("key.kind mismatch: lean=%q go=%q", want.Key.Kind, got.Key.Kind)
	case want.Key.Name != got.Key.Name:
		return fmt.Sprintf("key.name mismatch: lean=%q go=%q", want.Key.Name, got.Key.Name)
	case want.Key.SGroup != got.Key.SGroup:
		return fmt.Sprintf("key.sgroup mismatch: lean=%q go=%q", want.Key.SGroup, got.Key.SGroup)
	case want.Key.SKind != got.Key.SKind:
		return fmt.Sprintf("key.skind mismatch: lean=%q go=%q", want.Key.SKind, got.Key.SKind)
	case want.Key.SName != got.Key.SName:
		return fmt.Sprintf("key.sname mismatch: lean=%q go=%q", want.Key.SName, got.Key.SName)
	case want.Key.Hash != got.Key.Hash:
		return fmt.Sprintf("key.hash mismatch: lean=%q go=%q", want.Key.Hash, got.Key.Hash)
	case want.Key.Version != got.Key.Version:
		return fmt.Sprintf("key.version mismatch: lean=%q go=%q", want.Key.Version, got.Key.Version)
	case want.Key.Node != got.Key.Node:
		return fmt.Sprintf("key.node mismatch: lean=%q go=%q", want.Key.Node, got.Key.Node)
	default:
		return ""
	}
}

func leanKeyspaceResults(t *testing.T, cases []keyspaceCase) ([][]byte, [][]byte, []keyspaceResult) {
	t.Helper()
	encoded := encodeKeyspaceCases(t, cases)
	lines, err := RunModel("keyspace", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(lines), len(cases))
	}
	results := make([]keyspaceResult, len(lines))
	for i, line := range lines {
		res, err := decodeKeyspaceResult(line)
		if err != nil {
			t.Fatalf("id=%d decode error: %v\ncase: %s\nlean: %s", cases[i].ID, err, encoded[i], line)
		}
		results[i] = res
	}
	return encoded, lines, results
}

func TestKeyspaceMatchesLeanModel(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateKeyspaceCases(cfg.Seed, cfg.Cases)
	encoded, leanLines, leanResults := leanKeyspaceResults(t, cases)

	repro := fmt.Sprintf("NAYUTAR_DRT_SEED=%d NAYUTAR_DRT_CASES=%d go test -tags drt -run '^TestKeyspaceMatchesLeanModel$' ./core/drt/...", cfg.Seed, cfg.Cases)
	for i, tc := range cases {
		goRes, err := runKeyspaceCase(tc)
		if err != nil {
			t.Fatalf("seed=%d id=%d go error: %v\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, err, encoded[i], leanLines[i], repro)
		}
		if diff := compareKeyspaceResult(leanResults[i], goRes); diff != "" {
			goJSON, marshalErr := json.Marshal(goRes)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			t.Fatalf("seed=%d id=%d mismatch (%s)\ncase: %s\nlean: %s\ngo:   %s\nreproduce: %s",
				cfg.Seed, tc.ID, diff, encoded[i], leanLines[i], goJSON, repro)
		}
	}
	t.Logf("compared %d cases (seed=%d)", len(cases), cfg.Seed)
}

func isFiveSegmentRefParse(text string) bool {
	segs := strings.Split(text, "/")
	return len(segs) == 8 && segs[0] == "" && segs[1] == "t" && segs[2] != "" && segs[3] == "ref"
}

func TestKeyspaceCasesReachEveryOutcome(t *testing.T) {
	cases := generateKeyspaceCases(1, 2000)
	_, _, results := leanKeyspaceResults(t, cases)

	seen := map[string]int{}
	for i, tc := range cases {
		res := results[i]
		seen[fmt.Sprintf("%s/ok=%v", tc.Op, res.OK)]++
		if res.OK {
			switch tc.Op {
			case "encode", "rel":
				seen[fmt.Sprintf("%s/family=%s", tc.Op, tc.Key.Family)]++
			case "parse", "parserel":
				seen[fmt.Sprintf("%s/family=%s", tc.Op, res.Key.Family)]++
			}
			if tc.Op == "parse" && res.Key.Family == "backref" {
				if res.Key.Group != keyspace.CoreGroup {
					seen["parse/backref-target-noncore"]++
				}
				if res.Key.SGroup != keyspace.CoreGroup {
					seen["parse/backref-source-noncore"]++
				}
			}
		} else if tc.Op == "parse" && strings.Contains(tc.Text, "/ref/") && isFiveSegmentRefParse(tc.Text) {
			seen["parse/ref-five-segments-invalid"]++
		}
	}

	var expected []string
	for _, op := range keyspaceOps {
		expected = append(expected, op+"/ok=true", op+"/ok=false")
	}
	for _, fam := range keyspaceFamilyNames {
		expected = append(expected, "encode/family="+fam, "parse/family="+fam)
	}
	for _, fam := range keyspaceRelFamilies {
		expected = append(expected, "rel/family="+fam, "parserel/family="+fam)
	}
	expected = append(expected,
		"parse/backref-target-noncore",
		"parse/backref-source-noncore",
		"parse/ref-five-segments-invalid",
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

func TestGenerateKeyspaceCasesIsDeterministic(t *testing.T) {
	a := bytes.Join(encodeKeyspaceCases(t, generateKeyspaceCases(1, 100)), []byte("\n"))
	b := bytes.Join(encodeKeyspaceCases(t, generateKeyspaceCases(1, 100)), []byte("\n"))
	c := bytes.Join(encodeKeyspaceCases(t, generateKeyspaceCases(2, 100)), []byte("\n"))

	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different encoded cases")
	}
	if bytes.Equal(a, c) {
		t.Fatal("different seeds produced identical encoded cases")
	}
}

func TestKeyspaceComparisonDetectsEveryFieldMutation(t *testing.T) {
	cases := generateKeyspaceCases(1, 200)
	_, leanLines, results := leanKeyspaceResults(t, cases)

	applied := map[string]int{}
	assertDetects := func(kind string, orig keyspaceResult, mutate func(r *keyspaceResult)) {
		t.Helper()
		mut := orig
		mutate(&mut)
		applied[kind]++
		if compareKeyspaceResult(orig, mut) == "" {
			t.Fatalf("mutation %q on case %d was not detected", kind, orig.ID)
		}
	}

	for _, orig := range results {
		assertDetects("id+1", orig, func(r *keyspaceResult) { r.ID++ })
		assertDetects("flip-ok", orig, func(r *keyspaceResult) { r.OK = !r.OK })
		assertDetects("text-changed", orig, func(r *keyspaceResult) { r.Text += "x" })
		assertDetects("key-family-changed", orig, func(r *keyspaceResult) { r.Key.Family += "x" })
		assertDetects("key-tenant-changed", orig, func(r *keyspaceResult) { r.Key.Tenant += "x" })
		assertDetects("key-group-changed", orig, func(r *keyspaceResult) { r.Key.Group += "x" })
		assertDetects("key-kind-changed", orig, func(r *keyspaceResult) { r.Key.Kind += "x" })
		assertDetects("key-name-changed", orig, func(r *keyspaceResult) { r.Key.Name += "x" })
		assertDetects("key-sgroup-changed", orig, func(r *keyspaceResult) { r.Key.SGroup += "x" })
		assertDetects("key-skind-changed", orig, func(r *keyspaceResult) { r.Key.SKind += "x" })
		assertDetects("key-sname-changed", orig, func(r *keyspaceResult) { r.Key.SName += "x" })
		assertDetects("key-hash-changed", orig, func(r *keyspaceResult) { r.Key.Hash += "x" })
		assertDetects("key-version-changed", orig, func(r *keyspaceResult) { r.Key.Version += "x" })
		assertDetects("key-node-changed", orig, func(r *keyspaceResult) { r.Key.Node += "x" })
	}

	for _, kind := range []string{
		"id+1", "flip-ok", "text-changed",
		"key-family-changed", "key-tenant-changed", "key-group-changed", "key-kind-changed", "key-name-changed",
		"key-sgroup-changed", "key-skind-changed", "key-sname-changed", "key-hash-changed", "key-version-changed", "key-node-changed",
	} {
		if applied[kind] == 0 {
			t.Errorf("mutation %q was never applicable across %d cases", kind, len(cases))
		}
	}

	const emptyKey = `{"family":"","tenant":"","group":"","kind":"","name":"","sgroup":"","skind":"","sname":"","hash":"","version":"","node":""}`
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
		{"missing id", `{"ok":false,"text":"","key":` + emptyKey + `}`},
		{"missing key", `{"id":0,"ok":false,"text":""}`},
		{"null key", `{"id":0,"ok":false,"text":"","key":null}`},
		{"trailing object", `{"id":0,"ok":false,"text":"","key":` + emptyKey + `}{"id":1}`},
		{"empty key object", `{"id":0,"ok":false,"text":"","key":{}}`},
		{"missing node field", `{"id":0,"ok":false,"text":"","key":{"family":"","tenant":"","group":"","kind":"","name":"","sgroup":"","skind":"","sname":"","hash":"","version":""}}`},
		{"unknown key field", `{"id":0,"ok":false,"text":"","key":{"family":"","tenant":"","group":"","kind":"","name":"","sgroup":"","skind":"","sname":"","hash":"","version":"","node":"","extra":1}}`},
		{"non-string node", `{"id":0,"ok":false,"text":"","key":{"family":"","tenant":"","group":"","kind":"","name":"","sgroup":"","skind":"","sname":"","hash":"","version":"","node":1}}`},
	}
	for _, tc := range invalidLines {
		if _, err := decodeKeyspaceResult([]byte(tc.line)); err == nil {
			t.Errorf("decodeKeyspaceResult accepted %s: %s", tc.name, tc.line)
		}
	}

	valid := `{"id":7,"ok":true,"text":"","key":{"family":"node","tenant":"","group":"","kind":"","name":"","sgroup":"","skind":"","sname":"","hash":"","version":"","node":"node-1.rack-a"}}`
	got, err := decodeKeyspaceResult([]byte(valid))
	if err != nil {
		t.Fatalf("decodeKeyspaceResult(%s): %v", valid, err)
	}
	want := keyspaceResult{
		ID: 7,
		OK: true,
		Key: keyspaceKey{
			Family: "node",
			Node:   "node-1.rack-a",
		},
	}
	if diff := compareKeyspaceResult(want, got); diff != "" {
		t.Fatalf("decodeKeyspaceResult(%s) differs from expected: %s", valid, diff)
	}
}
