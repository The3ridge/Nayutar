// Package btree is a persistent ordered map from a key to a store.KV.
package btree

import (
	"strings"

	"github.com/Kaikei-e/Nayutar/core/store"
)

const (
	minDegree   = 16
	minKeys     = minDegree - 1
	maxKeys     = 2*minDegree - 1
	maxChildren = 2 * minDegree
)

type node struct {
	items    [maxKeys]store.KV
	children [maxChildren]*node
	nItems   int
	leaf     bool
}

func cloneNode(n *node) *node {
	c := *n
	return &c
}

func (n *node) searchGE(key string) int {
	lo, hi := 0, n.nItems
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if n.items[mid].Key < key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

func (n *node) searchGT(key string) int {
	lo, hi := 0, n.nItems
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if n.items[mid].Key <= key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// Tree is an immutable B-tree keyed by KV.Key, ordered bytewise. The zero
// value is an empty tree. Put and Delete return a new tree that shares
// unchanged nodes with the receiver; the receiver stays valid and unchanged,
// so any Tree value is a snapshot that concurrent readers may use freely.
type Tree struct {
	root   *node
	length int
}

// Len returns the number of entries in the tree.
func (t Tree) Len() int {
	return t.length
}

// Get returns the entry for key and whether it was present.
func (t Tree) Get(key string) (store.KV, bool) {
	cur := t.root
	for cur != nil {
		idx := cur.searchGE(key)
		if idx < cur.nItems && cur.items[idx].Key == key {
			return cur.items[idx], true
		}
		if cur.leaf {
			return store.KV{}, false
		}
		cur = cur.children[idx]
	}
	return store.KV{}, false
}

// Put inserts kv, or replaces the entry with the same key, returning a new tree.
func (t Tree) Put(kv store.KV) Tree {
	if t.root == nil {
		r := &node{nItems: 1, leaf: true}
		r.items[0] = kv
		return Tree{root: r, length: 1}
	}
	left, median, right, split, inserted := putNode(t.root, kv)
	if split {
		newRoot := &node{nItems: 1, leaf: false}
		newRoot.items[0] = median
		newRoot.children[0] = left
		newRoot.children[1] = right
		left = newRoot
	}
	n := t.length
	if inserted {
		n++
	}
	return Tree{root: left, length: n}
}

func putNode(n *node, kv store.KV) (left *node, median store.KV, right *node, split bool, inserted bool) {
	idx := n.searchGE(kv.Key)
	if idx < n.nItems && n.items[idx].Key == kv.Key {
		n2 := cloneNode(n)
		n2.items[idx] = kv
		return n2, store.KV{}, nil, false, false
	}
	if n.leaf {
		if n.nItems < maxKeys {
			n2 := cloneNode(n)
			copy(n2.items[idx+1:n2.nItems+1], n2.items[idx:n2.nItems])
			n2.items[idx] = kv
			n2.nItems++
			return n2, store.KV{}, nil, false, true
		}
		var tmp [maxKeys + 1]store.KV
		copy(tmp[:idx], n.items[:idx])
		tmp[idx] = kv
		copy(tmp[idx+1:], n.items[idx:n.nItems])

		left = &node{nItems: minKeys, leaf: true}
		copy(left.items[:minKeys], tmp[:minKeys])
		right = &node{nItems: minDegree, leaf: true}
		copy(right.items[:minDegree], tmp[minKeys+1:])
		return left, tmp[minKeys], right, true, true
	}

	cLeft, cMed, cRight, cSplit, inserted := putNode(n.children[idx], kv)
	if !cSplit {
		n2 := cloneNode(n)
		n2.children[idx] = cLeft
		return n2, store.KV{}, nil, false, inserted
	}
	if n.nItems < maxKeys {
		n2 := cloneNode(n)
		copy(n2.items[idx+1:n2.nItems+1], n2.items[idx:n2.nItems])
		n2.items[idx] = cMed
		copy(n2.children[idx+2:n2.nItems+2], n2.children[idx+1:n2.nItems+1])
		n2.children[idx] = cLeft
		n2.children[idx+1] = cRight
		n2.nItems++
		return n2, store.KV{}, nil, false, inserted
	}

	var tmpItems [maxKeys + 1]store.KV
	copy(tmpItems[:idx], n.items[:idx])
	tmpItems[idx] = cMed
	copy(tmpItems[idx+1:], n.items[idx:n.nItems])

	var tmpChildren [maxChildren + 1]*node
	copy(tmpChildren[:idx], n.children[:idx])
	tmpChildren[idx] = cLeft
	tmpChildren[idx+1] = cRight
	copy(tmpChildren[idx+2:], n.children[idx+1:n.nItems+1])

	left = &node{nItems: minKeys, leaf: false}
	copy(left.items[:minKeys], tmpItems[:minKeys])
	copy(left.children[:minKeys+1], tmpChildren[:minKeys+1])

	right = &node{nItems: minDegree, leaf: false}
	copy(right.items[:minDegree], tmpItems[minKeys+1:])
	copy(right.children[:minDegree+1], tmpChildren[minKeys+1:])

	return left, tmpItems[minKeys], right, true, inserted
}

// Delete removes key, returning the updated tree and true, or the identical
// receiver and false when key is absent.
func (t Tree) Delete(key string) (Tree, bool) {
	if t.root == nil {
		return t, false
	}
	newRoot, deleted := deleteKey(t.root, key)
	if !deleted {
		return t, false
	}
	if newRoot.nItems == 0 {
		if newRoot.leaf {
			newRoot = nil
		} else {
			newRoot = newRoot.children[0]
		}
	}
	return Tree{root: newRoot, length: t.length - 1}, true
}

func deleteKey(n *node, key string) (*node, bool) {
	idx := n.searchGE(key)
	found := idx < n.nItems && n.items[idx].Key == key
	if n.leaf {
		if !found {
			return n, false
		}
		n2 := cloneNode(n)
		copy(n2.items[idx:n2.nItems-1], n2.items[idx+1:n2.nItems])
		n2.nItems--
		n2.items[n2.nItems] = store.KV{}
		return n2, true
	}

	if found {
		pred, newLeft := deleteMax(n.children[idx])
		n2 := cloneNode(n)
		n2.items[idx] = pred
		n2.children[idx] = newLeft
		fixChildUnderflow(n2, idx)
		return n2, true
	}

	newChild, deleted := deleteKey(n.children[idx], key)
	if !deleted {
		return n, false
	}
	n2 := cloneNode(n)
	n2.children[idx] = newChild
	fixChildUnderflow(n2, idx)
	return n2, true
}

func deleteMax(n *node) (store.KV, *node) {
	n2 := cloneNode(n)
	if n2.leaf {
		kv := n2.items[n2.nItems-1]
		n2.nItems--
		n2.items[n2.nItems] = store.KV{}
		return kv, n2
	}
	kv, newChild := deleteMax(n2.children[n2.nItems])
	n2.children[n2.nItems] = newChild
	fixChildUnderflow(n2, n2.nItems)
	return kv, n2
}

func fixChildUnderflow(parent *node, idx int) {
	child := parent.children[idx]
	if child.nItems >= minKeys {
		return
	}

	if idx > 0 && parent.children[idx-1].nItems > minKeys {
		left := cloneNode(parent.children[idx-1])
		copy(child.items[1:child.nItems+1], child.items[:child.nItems])
		child.items[0] = parent.items[idx-1]
		if !child.leaf {
			copy(child.children[1:child.nItems+2], child.children[:child.nItems+1])
			child.children[0] = left.children[left.nItems]
			left.children[left.nItems] = nil
		}
		child.nItems++
		parent.items[idx-1] = left.items[left.nItems-1]
		left.nItems--
		left.items[left.nItems] = store.KV{}
		parent.children[idx-1] = left
		return
	}

	if idx < parent.nItems && parent.children[idx+1].nItems > minKeys {
		right := cloneNode(parent.children[idx+1])
		child.items[child.nItems] = parent.items[idx]
		if !child.leaf {
			child.children[child.nItems+1] = right.children[0]
			copy(right.children[:right.nItems], right.children[1:right.nItems+1])
			right.children[right.nItems] = nil
		}
		child.nItems++
		parent.items[idx] = right.items[0]
		copy(right.items[:right.nItems-1], right.items[1:right.nItems])
		right.nItems--
		right.items[right.nItems] = store.KV{}
		parent.children[idx+1] = right
		return
	}

	if idx > 0 {
		left := cloneNode(parent.children[idx-1])
		left.items[left.nItems] = parent.items[idx-1]
		copy(left.items[left.nItems+1:], child.items[:child.nItems])
		if !left.leaf {
			copy(left.children[left.nItems+1:], child.children[:child.nItems+1])
		}
		left.nItems += 1 + child.nItems

		copy(parent.items[idx-1:parent.nItems-1], parent.items[idx:parent.nItems])
		copy(parent.children[idx:parent.nItems], parent.children[idx+1:parent.nItems+1])
		parent.children[parent.nItems] = nil
		parent.nItems--
		parent.items[parent.nItems] = store.KV{}
		parent.children[idx-1] = left
		return
	}

	right := parent.children[1]
	child.items[child.nItems] = parent.items[0]
	copy(child.items[child.nItems+1:], right.items[:right.nItems])
	if !child.leaf {
		copy(child.children[child.nItems+1:], right.children[:right.nItems+1])
	}
	child.nItems += 1 + right.nItems

	copy(parent.items[:parent.nItems-1], parent.items[1:parent.nItems])
	copy(parent.children[1:parent.nItems], parent.children[2:parent.nItems+1])
	parent.children[parent.nItems] = nil
	parent.nItems--
	parent.items[parent.nItems] = store.KV{}
}

// Ascend calls fn for the entries whose key starts with prefix and, when
// after is not empty, is strictly greater than after, in ascending key order,
// until fn returns false. It seeks to the first such entry in O(log n) and
// stops at the first key past the prefix.
func (t Tree) Ascend(prefix, after string, fn func(store.KV) bool) {
	if t.root == nil {
		return
	}
	bound := prefix
	strict := false
	if after != "" && after >= prefix {
		// Any key > after already lies past prefix when after >= prefix without having prefix.
		if !strings.HasPrefix(after, prefix) {
			return
		}
		bound = after
		strict = true
	}
	ascendFrom(t.root, prefix, bound, strict, fn)
}

func ascendFrom(n *node, prefix, bound string, strict bool, fn func(store.KV) bool) bool {
	var idx int
	if strict {
		idx = n.searchGT(bound)
	} else {
		idx = n.searchGE(bound)
	}
	if idx == n.nItems {
		if n.leaf {
			return true
		}
		return ascendFrom(n.children[idx], prefix, bound, strict, fn)
	}
	if !n.leaf && !ascendFrom(n.children[idx], prefix, bound, strict, fn) {
		return false
	}
	for i := idx; i < n.nItems; i++ {
		kv := n.items[i]
		if !strings.HasPrefix(kv.Key, prefix) {
			return false
		}
		if !fn(kv) {
			return false
		}
		if !n.leaf && !ascendAll(n.children[i+1], prefix, fn) {
			return false
		}
	}
	return true
}

func ascendAll(n *node, prefix string, fn func(store.KV) bool) bool {
	for i := 0; i < n.nItems; i++ {
		if !n.leaf && !ascendAll(n.children[i], prefix, fn) {
			return false
		}
		kv := n.items[i]
		if !strings.HasPrefix(kv.Key, prefix) {
			return false
		}
		if !fn(kv) {
			return false
		}
	}
	if !n.leaf {
		return ascendAll(n.children[n.nItems], prefix, fn)
	}
	return true
}
