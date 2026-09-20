// Package attrs converts between flattened dotted attribute keys and
// nested structures, following OpenInference-style conventions.
package attrs

import (
	"strconv"
	"strings"
)

// KV is a single flattened attribute.
type KV struct {
	Key   string
	Value any
}

// node is a trie node.
//
// Children live on two tracks:
//
//   - branches: map keys. Digit segments that terminated a path with a
//     scalar value land here ("tags.0" with a scalar leaf).
//   - indices: digit segments that are candidates for array positions.
//     At materialization a node whose children are all indices becomes
//     an array; any branch sibling demotes all indices to branches.
//
// A node may also carry a terminal value alongside a subtree. Both are
// preserved: the value materializes under the node's own key, and the
// subtree materializes under literal dotted keys ("a" valued plus
// "a.b" data yields keys "a" and "a.b" side by side).
type node struct {
	value    any
	hasValue bool
	indices  map[int]*node
	branches map[string]*node
}

func newNode() *node { return &node{} }

// setValue records a terminal value. Digit index candidates demote to
// branch keys: a path ending at this node with a scalar means its last
// digit segment was a map key, not an array position.
func (n *node) setValue(v any) {
	for k, child := range n.indices {
		if n.branches == nil {
			n.branches = map[string]*node{}
		}
		n.branches[strconv.Itoa(k)] = child
	}
	n.indices = nil
	n.value = v
	n.hasValue = true
}

// step advances one segment, creating children as needed. Digit
// segments are reconciled across tracks: a digit that previously
// terminated a scalar path is a branch key, and any digit that later
// extends into a mapping pulls its siblings' shape together at
// materialization time (a branch sibling demotes all indices).
func (n *node) step(seg string, last bool) *node {
	idx, isNum := digitValue(seg)
	if isNum && !last {
		// If this digit already exists as a branch (a prior scalar
		// path ended here), keep using the branch so both paths
		// address the same child.
		if c, ok := n.branches[seg]; ok {
			return c
		}
		// If the node has any branch children already, stay on the
		// branch track for consistency: mixing one index with branch
		// siblings would silently drop data at materialization.
		if len(n.branches) > 0 {
			if n.branches == nil {
				n.branches = map[string]*node{}
			}
			c, ok := n.branches[seg]
			if !ok {
				c = newNode()
				n.branches[seg] = c
			}
			return c
		}
		if n.indices == nil {
			n.indices = map[int]*node{}
		}
		c, ok := n.indices[idx]
		if !ok {
			c = newNode()
			n.indices[idx] = c
		}
		return c
	}
	if n.branches == nil {
		n.branches = map[string]*node{}
	}
	if isNum {
		// A digit terminating this path is a plain key; remove any
		// index candidate recorded earlier so the shapes agree.
		delete(n.indices, idx)
	}
	c, ok := n.branches[seg]
	if !ok {
		c = newNode()
		n.branches[seg] = c
	}
	return c
}

// digitValue parses a canonical non-negative integer ("0", "12") with
// no leading zeros. "00" and "01" therefore normalize: they parse as
// non-canonical and stay plain string keys, while "0" is canonical —
// callers converge "a.00" and "a.0" by canonicalizing digit segments
// before lookup (see canonicalSeg).
func digitValue(s string) (int, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// canonicalSeg normalizes an all-digit segment: "00" -> "0", "12" ->
// "12". Zero-padded forms therefore converge with their canonical
// spelling. Anything containing a non-digit returns unchanged.
func canonicalSeg(s string) string {
	if n, ok := plainInt(s); ok {
		return strconv.Itoa(n)
	}
	return s
}

// plainInt parses s as a base-10 integer, allowing leading zeros.
func plainInt(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}

// add inserts one key-value pair into the trie rooted at root.
func add(root *node, key string, value any) {
	if value == nil {
		return
	}
	segs := segments(key)
	if len(segs) == 0 {
		return
	}
	cur := root
	for i := 0; i < len(segs); i++ {
		segs[i] = canonicalSeg(segs[i])
		cur = cur.step(segs[i], i == len(segs)-1)
	}
	cur.setValue(value)
}

// Unflatten expands flattened dotted keys into nested maps and arrays.
//
// Rules:
//
//   - "arrays only for mappings": a digit segment becomes an array
//     index only when the node's children are exclusively digit
//     segments ("documents.0.content" -> array of maps; "tags.0" with
//     a scalar leaf -> map keyed by "0"; one scalar sibling demotes
//     the whole node to a map).
//   - a valued node with a subtree keeps both: the value under its own
//     key, subtree entries as literal dotted keys beside it.
//   - nil values are skipped; segments are whitespace-trimmed and
//     empty ones dropped; leading zeros normalize ("00" == "0");
//     negative numbers are string keys; last write wins on repeats.
func Unflatten(pairs []KV) map[string]any {
	root := newNode()
	for _, kv := range pairs {
		add(root, kv.Key, kv.Value)
	}
	return materializeRoot(root)
}

// segments splits a dotted key into trimmed, non-empty segments.
func segments(key string) []string {
	parts := strings.Split(key, ".")
	segs := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = trim(p); p != "" {
			segs = append(segs, p)
		}
	}
	return segs
}

func trim(s string) string {
	return strings.TrimSpace(s)
}

// materializeRoot builds the top-level map, flattening any dotted-key
// entries that arose from valued-node conflicts.
func materializeRoot(root *node) map[string]any {
	out := map[string]any{}
	for k, child := range root.branches {
		materializeInto(out, k, child)
	}
	return out
}

// materializeInto places n's materialized form under key k in out,
// expanding valued-node conflicts into dotted keys as needed.
func materializeInto(out map[string]any, k string, n *node) {
	if n.hasValue {
		out[k] = n.value
		// Subtree beneath a valued node: emit as literal dotted keys.
		for bk, bc := range n.branches {
			materializeInto(out, k+"."+bk, bc)
		}
		for _, idx := range sortedIndices(n) {
			materializeInto(out, k+"."+strconv.Itoa(idx), n.indices[idx])
		}
		return
	}
	if arr, ok := asArray(n); ok {
		out[k] = arr
		return
	}
	m := map[string]any{}
	for bk, bc := range n.branches {
		materializeInto(m, bk, bc)
	}
	out[k] = m
}

// asArray reports whether n's children are exclusively index nodes,
// and if so returns the materialized array.
func asArray(n *node) ([]any, bool) {
	if len(n.indices) == 0 || len(n.branches) > 0 {
		return nil, false
	}
	idxs := sortedIndices(n)
	arr := make([]any, 0, len(idxs))
	for _, i := range idxs {
		arr = append(arr, materializeValue(n.indices[i]))
	}
	return arr, true
}

// materializeValue converts a node to a plain value. Subtrees that
// would need dotted keys cannot occur below a non-valued parent here
// because conflicts only arise on valued nodes, handled above.
func materializeValue(n *node) any {
	if arr, ok := asArray(n); ok {
		return arr
	}
	m := map[string]any{}
	for bk, bc := range n.branches {
		materializeInto(m, bk, bc)
	}
	return m
}

func sortedIndices(n *node) []int {
	idxs := make([]int, 0, len(n.indices))
	for i := range n.indices {
		idxs = append(idxs, i)
	}
	for i := 1; i < len(idxs); i++ {
		for j := i; j > 0 && idxs[j] < idxs[j-1]; j-- {
			idxs[j], idxs[j-1] = idxs[j-1], idxs[j]
		}
	}
	return idxs
}
