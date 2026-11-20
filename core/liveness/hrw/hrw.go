// Package hrw implements rendezvous (highest random weight) hashing for ranking aggregators per node.
package hrw

import (
	"cmp"
	"slices"
)

const (
	fnvOffset64 uint64 = 0xcbf29ce484222325
	fnvPrime64  uint64 = 0x100000001b3
)

type scored struct {
	member string
	score  uint64
}

// Ranked returns members ordered by descending score for key; ties by ascending member name.
// Duplicate members are returned once. The result is a new slice.
func Ranked(key string, members []string) []string {
	seen := make(map[string]struct{}, len(members))
	items := make([]scored, 0, len(members))
	for _, m := range members {
		if _, ok := seen[m]; ok {
			continue
		}
		seen[m] = struct{}{}
		items = append(items, scored{member: m, score: Score(key, m)})
	}
	slices.SortFunc(items, func(a, b scored) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		return cmp.Compare(a.member, b.member)
	})
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.member
	}
	return out
}

// Score returns the 64-bit rendezvous hash score for (key, member).
func Score(key, member string) uint64 {
	h := fnvOffset64
	for i := range len(member) {
		h ^= uint64(member[i])
		h *= fnvPrime64
	}
	h *= fnvPrime64
	for i := range len(key) {
		h ^= uint64(key[i])
		h *= fnvPrime64
	}
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}
