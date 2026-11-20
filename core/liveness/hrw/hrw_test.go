package hrw

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"testing"
)

func referenceScore(key, member string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(member))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(key))
	x := h.Sum64()
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	return x
}

func TestPinnedVectors(t *testing.T) {
	vectors := []struct {
		key    string
		member string
		want   uint64
	}{
		{key: "node-1", member: "agg-a", want: 0x80efb97293bb3000},
		{key: "node-1", member: "agg-b", want: 0x522aa5ba6c2eedeb},
		{key: "node-1", member: "agg-c", want: 0x291c66ef043fad3f},
		{key: "", member: "", want: 0xb9034ad37056f5fb},
		{key: "k-alpha/0", member: "m-beta", want: 0x37f505f4e1256dbd},
	}

	for _, v := range vectors {
		got := Score(v.key, v.member)
		ref := referenceScore(v.key, v.member)
		if got != ref {
			t.Fatalf("Score(%q, %q) = %#016x, referenceScore = %#016x", v.key, v.member, got, ref)
		}
		if got != v.want {
			t.Errorf("Score(%q, %q) = %#016x, want pinned %#016x", v.key, v.member, got, v.want)
		}
	}

	gotRanked := Ranked("node-1", []string{"agg-c", "agg-a", "agg-b"})
	wantRanked := []string{"agg-a", "agg-b", "agg-c"}
	if !slices.Equal(gotRanked, wantRanked) {
		t.Fatalf("Ranked(node-1) = %v, want %v", gotRanked, wantRanked)
	}
}

func TestDeterminism(t *testing.T) {
	members := []string{"agg-a", "agg-b", "agg-c", "agg-d", "agg-e"}
	first := Ranked("node-42", members)
	for i := range 20 {
		got := Ranked("node-42", members)
		if !slices.Equal(got, first) {
			t.Fatalf("iteration %d: got %v, want %v", i, got, first)
		}
	}
	for i := range 20 {
		if got := Score("node-42", "agg-c"); got != Score("node-42", "agg-c") {
			t.Fatalf("iteration %d: non-deterministic Score: %#x", i, got)
		}
	}
}

func TestPermutationInvarianceAndNoMutation(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	base := []string{"agg-0", "agg-1", "agg-2", "agg-3", "agg-4", "agg-5", "agg-6"}
	want := Ranked("node-99", base)

	for i := range 50 {
		perm := slices.Clone(base)
		rng.Shuffle(len(perm), func(a, b int) { perm[a], perm[b] = perm[b], perm[a] })
		snapshot := slices.Clone(perm)

		got := Ranked("node-99", perm)
		if !slices.Equal(got, want) {
			t.Fatalf("perm %d (%v): got %v, want %v", i, snapshot, got, want)
		}
		if !slices.Equal(perm, snapshot) {
			t.Fatalf("perm %d mutated input: got %v, original %v", i, perm, snapshot)
		}
	}
}

func TestEmptySingleAndDuplicates(t *testing.T) {
	if got := Ranked("node-1", nil); len(got) != 0 {
		t.Fatalf("Ranked(nil) = %v, want empty", got)
	}
	if got := Ranked("node-1", []string{}); len(got) != 0 {
		t.Fatalf("Ranked([]) = %v, want empty", got)
	}

	single := []string{"agg-only"}
	gotSingle := Ranked("node-1", single)
	if !slices.Equal(gotSingle, []string{"agg-only"}) {
		t.Fatalf("Ranked(single) = %v, want [agg-only]", gotSingle)
	}
	gotSingle[0] = "mutated"
	if single[0] != "agg-only" {
		t.Fatalf("result aliases input slice")
	}

	dups := []string{"agg-b", "agg-a", "agg-b", "agg-c", "agg-a"}
	dupsSnap := slices.Clone(dups)
	gotDups := Ranked("node-1", dups)
	wantDups := Ranked("node-1", []string{"agg-a", "agg-b", "agg-c"})
	if !slices.Equal(gotDups, wantDups) {
		t.Fatalf("Ranked(dups) = %v, want %v", gotDups, wantDups)
	}
	if !slices.Equal(dups, dupsSnap) {
		t.Fatalf("Ranked(dups) mutated input: %v", dups)
	}
}

func TestMinimalDisruption(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	members := []string{"agg-0", "agg-1", "agg-2", "agg-3", "agg-4", "agg-5", "agg-6", "agg-7"}
	removed := "agg-3"
	reduced := []string{"agg-0", "agg-1", "agg-2", "agg-4", "agg-5", "agg-6", "agg-7"}

	for i := range 1000 {
		key := fmt.Sprintf("node-%d-%d", i, rng.Uint64())
		full := Ranked(key, members)
		filtered := make([]string, 0, len(reduced))
		for _, m := range full {
			if m != removed {
				filtered = append(filtered, m)
			}
		}
		after := Ranked(key, reduced)
		if !slices.Equal(after, filtered) {
			t.Fatalf("key %q: after removal %v != filtered %v", key, after, filtered)
		}
	}
}

func TestBalanceTop3(t *testing.T) {
	const (
		numKeys    = 10000
		numMembers = 10
		topK       = 3
	)
	members := make([]string, numMembers)
	for i := range numMembers {
		members[i] = fmt.Sprintf("agg-%02d", i)
	}
	counts := make(map[string]int, numMembers)
	for k := range numKeys {
		key := fmt.Sprintf("node-%05d", k)
		ranked := Ranked(key, members)
		for _, m := range ranked[:topK] {
			counts[m]++
		}
	}

	mean := float64(numKeys*topK) / float64(numMembers)
	lo := mean * 0.80
	hi := mean * 1.20
	for _, m := range members {
		c := float64(counts[m])
		if c < lo || c > hi {
			t.Errorf("member %s count %d outside [%.1f, %.1f] (mean %.1f)", m, counts[m], lo, hi, mean)
		}
	}
}
