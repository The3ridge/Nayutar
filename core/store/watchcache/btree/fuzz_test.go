package btree

import (
	"slices"
	"strings"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/store"
)

func readFuzzKey(data []byte, pos *int) (string, bool) {
	if *pos >= len(data) {
		return "", false
	}
	b := data[*pos]
	*pos++
	if b&0x80 == 0 {
		return string([]byte{'k', '0' + (b>>3)&0x07, '0' + b&0x07}), true
	}
	n := int(b & 0x0f)
	if *pos+n > len(data) {
		n = len(data) - *pos
	}
	k := string(data[*pos : *pos+n])
	*pos += n
	return k, true
}

func FuzzTree(f *testing.F) {
	// Seed with sequences exercising puts, replaces, deletes, boundary bytes, and ascends.
	f.Add([]byte{
		0x00, 0x01, 0x11,
		0x00, 0x02, 0x22,
		0x00, 0x03, 0x33,
		0x02, 0x00, 0x00, 0x00,
		0x01, 0x02,
		0x01, 0x7f,
		0x02, 0x80, 0x80, 0x02,
	})
	f.Add([]byte{
		0x00, 0x82, 0x00, 0xff, 0x01,
		0x00, 0x83, 0x00, 0xff, 0x00, 0x02,
		0x00, 0x81, 0xff, 0x03,
		0x02, 0x82, 0x00, 0xff, 0x80, 0x00,
		0x02, 0x82, 0x00, 0xff, 0x81, 0xff, 0x00,
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		type snapshot struct {
			tr  Tree
			ref map[string]store.KV
		}

		tr := Tree{}
		ref := make(map[string]store.KV)
		var snapshots []snapshot

		pos := 0
		var rev store.Rev
		step := 0

		for pos < len(data) {
			op := data[pos]
			pos++
			step++

			switch op % 3 {
			case 0:
				k, ok := readFuzzKey(data, &pos)
				if !ok {
					break
				}
				var val []byte
				if pos < len(data) {
					val = []byte{data[pos]}
					pos++
				}
				rev++
				kv := store.KV{Key: k, Value: val, ModRev: rev}
				tr = tr.Put(kv)
				ref[k] = kv

				got, found := tr.Get(k)
				if !found || !kvEqual(got, kv) {
					t.Fatalf("step %d: Get(%q) after Put = (%+v, %v), want (%+v, true)", step, k, got, found, kv)
				}

			case 1:
				k, ok := readFuzzKey(data, &pos)
				if !ok {
					break
				}
				prevRoot := tr.root
				var deleted bool
				tr, deleted = tr.Delete(k)
				_, existed := ref[k]
				if deleted != existed {
					t.Fatalf("step %d: Delete(%q) = %v, want %v", step, k, deleted, existed)
				}
				if !existed && tr.root != prevRoot {
					t.Fatalf("step %d: Delete(%q) of absent key changed root pointer", step, k)
				}
				delete(ref, k)
				if _, found := tr.Get(k); found {
					t.Fatalf("step %d: key %q still present after Delete", step, k)
				}

			case 2:
				prefix, ok := readFuzzKey(data, &pos)
				if !ok {
					break
				}
				after, ok := readFuzzKey(data, &pos)
				if !ok {
					after = ""
				}
				limit := 0
				if pos < len(data) {
					limit = int(data[pos] & 0x1f)
					pos++
				}

				var want []store.KV
				for _, kv := range ref {
					if strings.HasPrefix(kv.Key, prefix) && (after == "" || kv.Key > after) {
						want = append(want, kv)
					}
				}
				slices.SortFunc(want, func(a, b store.KV) int {
					return strings.Compare(a.Key, b.Key)
				})
				if limit > 0 && len(want) > limit {
					want = want[:limit]
				}

				var got []store.KV
				tr.Ascend(prefix, after, func(kv store.KV) bool {
					got = append(got, kv)
					return limit == 0 || len(got) < limit
				})
				if len(got) != len(want) {
					t.Fatalf("step %d: Ascend(%q, %q, limit=%d) len=%d, want %d", step, prefix, after, limit, len(got), len(want))
				}
				for i := range want {
					if !kvEqual(got[i], want[i]) {
						t.Fatalf("step %d: Ascend[%d]=%+v, want %+v", step, i, got[i], want[i])
					}
				}
			}

			if tr.Len() != len(ref) {
				t.Fatalf("step %d: Len()=%d, want %d", step, tr.Len(), len(ref))
			}
			if step%16 == 0 {
				if _, err := tr.checkInvariants(); err != nil {
					t.Fatalf("step %d: invariant failure: %v", step, err)
				}
			}
			if step%32 == 0 && len(snapshots) < 16 {
				snapshots = append(snapshots, snapshot{tr: tr, ref: cloneRefMap(ref)})
			}
		}

		if _, err := tr.checkInvariants(); err != nil {
			t.Fatalf("final invariant failure: %v", err)
		}
		assertTreeEqualsMap(t, tr, ref)
		for i, snap := range snapshots {
			if _, err := snap.tr.checkInvariants(); err != nil {
				t.Fatalf("snapshot %d invariant failure: %v", i, err)
			}
			assertTreeEqualsMap(t, snap.tr, snap.ref)
		}
	})
}
