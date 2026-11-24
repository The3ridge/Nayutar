package ownership

import (
	_ "embed"
	"encoding/json/jsontext"
	"fmt"
	"slices"
	"strings"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/LKAL/schema"
)

const emptyLKALJSON = `{"$lkal":"0.1"}`

//go:embed ownership.schema.lkal
var ownershipSchemaBytes []byte

var ownershipSchema *schema.Schema

func init() {
	var err error
	ownershipSchema, err = schema.Compile("ownership.schema.lkal", ownershipSchemaBytes)
	if err != nil {
		panic("ownership: embedded ownership.schema.lkal failed to compile: " + err.Error())
	}
}

// Schema returns the compiled LKAL v0.2 schema for compact ownership trie documents.
func Schema() *schema.Schema { return ownershipSchema }

// SchemaBytes returns a copy of the embedded ownership.schema.lkal source.
func SchemaBytes() []byte { return slices.Clone(ownershipSchemaBytes) }

type trieNode struct {
	hasEntry bool
	entry    Entry
	subtree  bool
	children map[string]*trieNode
}

// Encode serializes st relative to the EntranceSpec leaves currently present in doc
// as compact single-line @lkal "0.1" LKAL-JSON validated against ownership.schema.lkal.
func Encode(st State, doc lkal.Record) ([]byte, error) {
	if err := st.Validate(); err != nil {
		return nil, err
	}
	docLeaves, err := ProjectEntranceSpec(doc)
	if err != nil {
		return nil, err
	}

	defaultEntry := Entry{
		State:  StateOwned,
		Owner:  st.DefaultOwner,
		Origin: OriginCreated,
	}

	// Build actor table with actors[0] = DefaultOwner and referenced foreign actors compacted & sorted.
	foreignSet := make(map[ActorID]struct{})
	for _, e := range st.Entries {
		if e.State == StateOwned && e.Owner != st.DefaultOwner {
			foreignSet[e.Owner] = struct{}{}
		}
	}
	foreignActors := make([]ActorID, 0, len(foreignSet))
	for a := range foreignSet {
		foreignActors = append(foreignActors, a)
	}
	slices.Sort(foreignActors)

	actors := make([]ActorID, 0, 1+len(foreignActors))
	actors = append(actors, st.DefaultOwner)
	actors = append(actors, foreignActors...)
	actorIdx := make(map[ActorID]int64, len(actors))
	for i, a := range actors {
		actorIdx[a] = int64(i)
	}

	// Build full trie over all docLeaves plus any orphan entries in st.Entries.
	type fullNode struct {
		hasLeaf  bool
		inDoc    bool
		entry    Entry
		children map[string]*fullNode
	}
	froot := &fullNode{children: make(map[string]*fullNode)}
	insertFull := func(p Path, e Entry, inDoc bool) {
		cur := froot
		for _, seg := range p {
			if cur.children == nil {
				cur.children = make(map[string]*fullNode)
			}
			nxt := cur.children[seg]
			if nxt == nil {
				nxt = &fullNode{}
				cur.children[seg] = nxt
			}
			cur = nxt
		}
		cur.hasLeaf = true
		cur.inDoc = inDoc
		cur.entry = e
	}

	// Populate present docLeaves (missing entry defaults to defaultEntry).
	for key, leaf := range docLeaves {
		e, ok := st.Entries[key]
		if !ok {
			e = defaultEntry
		}
		insertFull(leaf.Path, e, true)
	}
	// Populate any orphan entries not present in docLeaves (must always be emitted explicitly).
	for key, e := range st.Entries {
		if _, inDoc := docLeaves[key]; !inDoc {
			p, err := ParsePath(key)
			if err != nil {
				return nil, err
			}
			insertFull(p, e, false)
		}
	}

	type summary struct {
		docLeafCount int
		hasOrphan    bool
		uniform      bool
		entry        Entry
	}
	var summarize func(n *fullNode) summary
	summarize = func(n *fullNode) summary {
		var s summary
		initialized := false
		addEntry := func(e Entry) {
			if !initialized {
				s.uniform = true
				s.entry = e
				initialized = true
				return
			}
			if !s.entry.Equal(e) {
				s.uniform = false
			}
		}
		if n.hasLeaf {
			if n.inDoc {
				s.docLeafCount++
			} else {
				s.hasOrphan = true
			}
			addEntry(n.entry)
		}
		for _, ch := range n.children {
			cs := summarize(ch)
			s.docLeafCount += cs.docLeafCount
			if cs.hasOrphan {
				s.hasOrphan = true
			}
			if cs.docLeafCount > 0 || cs.hasOrphan {
				if !cs.uniform {
					s.uniform = false
					initialized = true
				} else {
					addEntry(cs.entry)
				}
			}
		}
		return s
	}

	var buildNode func(n *fullNode, depth int) (lkal.Record, bool)
	buildNode = func(n *fullNode, depth int) (lkal.Record, bool) {
		sum := summarize(n)
		if sum.docLeafCount == 0 && !sum.hasOrphan {
			return lkal.Record{}, false
		}
		// Entire subtree is present leaves matching default (actors[0], created): omit completely.
		if !sum.hasOrphan && sum.uniform && sum.entry.Equal(defaultEntry) {
			return lkal.Record{}, false
		}
		// Uniform non-default interior subtree of >= 2 present leaves: compress with w = true.
		if depth >= 1 && !n.hasLeaf && !sum.hasOrphan && sum.uniform && sum.docLeafCount >= 2 {
			return encodeNodeRecord(&sum.entry, true, nil, actorIdx), true
		}

		var selfEntry *Entry
		if n.hasLeaf {
			if !n.inDoc || !n.entry.Equal(defaultEntry) {
				e := n.entry
				selfEntry = &e
			}
		}

		childKeys := make([]string, 0, len(n.children))
		for k := range n.children {
			childKeys = append(childKeys, k)
		}
		slices.Sort(childKeys)

		var childFields []lkal.Field
		for _, k := range childKeys {
			if chRec, keep := buildNode(n.children[k], depth+1); keep {
				childFields = append(childFields, lkal.Field{Key: k, Value: chRec})
			}
		}
		if selfEntry == nil && len(childFields) == 0 {
			return lkal.Record{}, false
		}
		return encodeNodeRecord(selfEntry, false, childFields, actorIdx), true
	}

	actorList := make(lkal.List, len(actors))
	for i, a := range actors {
		actorList[i] = lkal.String(a)
	}

	topKeys := make([]string, 0, len(froot.children))
	for k := range froot.children {
		topKeys = append(topKeys, k)
	}
	slices.Sort(topKeys)

	var trieFields []lkal.Field
	for _, k := range topKeys {
		if chRec, keep := buildNode(froot.children[k], 1); keep {
			trieFields = append(trieFields, lkal.Field{Key: k, Value: chRec})
		}
	}

	f, err := lkal.FromJSON([]byte(emptyLKALJSON))
	if err != nil {
		return nil, newError(CodeInvalidInput, nil, err.Error(), "")
	}
	if err := f.Set("actors", actorList); err != nil {
		return nil, newError(CodeInvalidInput, nil, err.Error(), "")
	}
	if len(trieFields) > 0 {
		if err := f.Set("trie", lkal.Record{Fields: trieFields}); err != nil {
			return nil, newError(CodeInvalidInput, nil, err.Error(), "")
		}
	}
	if err := ownershipSchema.Validate(f); err != nil {
		return nil, newError(CodeInvalidInput, nil, err.Error(), "")
	}
	raw, err := lkal.ToJSON(f, false)
	if err != nil {
		return nil, newError(CodeInvalidInput, nil, err.Error(), "")
	}
	v := jsontext.Value(raw)
	if err := v.Compact(); err != nil {
		return nil, newError(CodeInvalidInput, nil, err.Error(), "")
	}
	return []byte(v), nil
}

func encodeNodeRecord(e *Entry, subtree bool, childFields []lkal.Field, actorIdx map[ActorID]int64) lkal.Record {
	fields := make([]lkal.Field, 0, 5)
	if e != nil {
		fields = append(fields, lkal.Field{Key: "s", Value: lkal.Enum(e.State.String())})
		if e.State == StateOwned {
			fields = append(fields,
				lkal.Field{Key: "a", Value: lkal.Int(actorIdx[e.Owner])},
				lkal.Field{Key: "o", Value: lkal.Enum(e.Origin.String())},
			)
		}
		if subtree {
			fields = append(fields, lkal.Field{Key: "w", Value: lkal.Bool(true)})
		}
	}
	if len(childFields) > 0 {
		fields = append(fields, lkal.Field{Key: "c", Value: lkal.Record{Fields: childFields}})
	}
	return lkal.Record{Fields: fields}
}

// Decode parses and validates a compact ownership trie payload and reconstructs
// the complete State for all EntranceSpec leaves present in doc (plus any explicit orphan leaves).
func Decode(data []byte, doc lkal.Record) (State, error) {
	f, err := lkal.FromJSON(data)
	if err != nil {
		return State{}, newError(CodeInvalidInput, nil, fmt.Sprintf("invalid ownership LKAL-JSON: %v", err), "")
	}
	if err := ownershipSchema.Validate(f); err != nil {
		return State{}, newError(CodeInvalidInput, nil, fmt.Sprintf("invalid ownership schema: %v", err), "")
	}

	top := f.Data()
	actorsVal, ok := findField(top, "actors")
	if !ok {
		return State{}, newError(CodeInvalidInput, nil, "missing required actors list", "")
	}
	actorsList, ok := actorsVal.(lkal.List)
	if !ok || len(actorsList) == 0 {
		return State{}, newError(CodeInvalidInput, nil, "actors must be a non-empty string list", "")
	}
	actors := make([]ActorID, len(actorsList))
	seenActors := make(map[ActorID]struct{}, len(actorsList))
	for i, el := range actorsList {
		s, ok := el.(lkal.String)
		if !ok {
			return State{}, newError(CodeInvalidInput, nil, "actor entry must be a string", "")
		}
		aid := ActorID(s)
		if err := ValidateActorID(aid); err != nil {
			return State{}, err
		}
		if _, dup := seenActors[aid]; dup {
			return State{}, newError(CodeInvalidInput, nil, fmt.Sprintf("duplicate actor %q in actors table", aid), "")
		}
		seenActors[aid] = struct{}{}
		actors[i] = aid
	}

	var parsedTrie map[string]*trieNode
	if trieVal, hasTrie := findField(top, "trie"); hasTrie {
		trieRec, ok := trieVal.(lkal.Record)
		if !ok {
			return State{}, newError(CodeInvalidInput, nil, "trie must be a record", "")
		}
		parsedTrie, err = parseTrieMap(trieRec, actors, nil)
		if err != nil {
			return State{}, err
		}
	}

	docLeaves, err := ProjectEntranceSpec(doc)
	if err != nil {
		return State{}, err
	}

	st := NewState(actors[0])
	defaultEntry := Entry{State: StateOwned, Owner: actors[0], Origin: OriginCreated}

	for key, leaf := range docLeaves {
		entry := resolveInTrie(parsedTrie, leaf.Path, defaultEntry)
		st.Entries[key] = entry
	}

	// Preserve any explicit non-subtree leaf markers in the trie whose path is currently absent from doc.
	var collectOrphans func(m map[string]*trieNode, prefix Path)
	collectOrphans = func(m map[string]*trieNode, prefix Path) {
		for seg, node := range m {
			p := prefix.Append(seg)
			if node.hasEntry && !node.subtree && IsEntranceSpecPath(p) {
				key := p.String()
				if _, inDoc := docLeaves[key]; !inDoc {
					st.Entries[key] = node.entry
				}
			}
			if len(node.children) > 0 {
				collectOrphans(node.children, p)
			}
		}
	}
	collectOrphans(parsedTrie, nil)

	// Reject any overlapping StateOwned entries reconstructed from a corrupt trie.
	ownedPaths := make([]Path, 0, len(st.Entries))
	for _, p := range st.SortedPaths() {
		if e, ok := st.Get(p); ok && e.State == StateOwned {
			ownedPaths = append(ownedPaths, p)
		}
	}
	for i := range len(ownedPaths) - 1 {
		if ownedPaths[i].Overlaps(ownedPaths[i+1]) {
			return State{}, newError(CodeInvalidInput, ownedPaths[i+1],
				fmt.Sprintf("overlapping owned paths %s and %s in ownership trie", ownedPaths[i].String(), ownedPaths[i+1].String()), "")
		}
	}

	return st, nil
}

func parseTrieMap(rec lkal.Record, actors []ActorID, prefix Path) (map[string]*trieNode, error) {
	if len(prefix) > MaxPathDepth {
		return nil, newError(CodeInvalidInput, prefix, "ownership trie exceeds maximum depth", "")
	}
	out := make(map[string]*trieNode, len(rec.Fields))
	for _, fld := range rec.Fields {
		if !ValidSeg(fld.Key) {
			return nil, newError(CodeInvalidInput, prefix, fmt.Sprintf("invalid trie segment %q", fld.Key), "")
		}
		if len(prefix) == 0 && fld.Key != "meta" && fld.Key != "spec" {
			return nil, newError(CodeInvalidInput, Path{fld.Key}, fmt.Sprintf("trie root segment %q outside EntranceSpec", fld.Key), "")
		}
		if len(prefix) == 1 && prefix[0] == "meta" && fld.Key != "labels" {
			return nil, newError(CodeInvalidInput, Path{"meta", fld.Key}, fmt.Sprintf("trie meta segment %q outside meta/labels", fld.Key), "")
		}
		if len(prefix) >= 3 && prefix[0] == "meta" && prefix[1] == "labels" {
			return nil, newError(CodeInvalidInput, prefix.Append(fld.Key), "meta/labels cannot have nested trie children", "")
		}
		nodeRec, ok := fld.Value.(lkal.Record)
		if !ok {
			return nil, newError(CodeInvalidInput, prefix.Append(fld.Key), "trie node must be a record", "")
		}
		node, err := parseTrieNode(nodeRec, actors, prefix.Append(fld.Key))
		if err != nil {
			return nil, err
		}
		out[fld.Key] = node
	}
	return out, nil
}

func parseTrieNode(rec lkal.Record, actors []ActorID, p Path) (*trieNode, error) {
	var (
		hasS, hasA, hasO, hasW, hasC bool
		state                        LeafState
		actorIndex                   int64
		origin                       Origin
		subtree                      bool
		childrenRec                  lkal.Record
	)
	for _, fld := range rec.Fields {
		switch fld.Key {
		case "s":
			e, ok := fld.Value.(lkal.Enum)
			if !ok {
				return nil, newError(CodeInvalidInput, p, "trie node s must be an enum", "")
			}
			switch string(e) {
			case "owned":
				state = StateOwned
			case "unowned":
				state = StateUnowned
			case "defaulted":
				state = StateDefaulted
			default:
				return nil, newError(CodeInvalidInput, p, fmt.Sprintf("invalid trie node state %q", string(e)), "")
			}
			hasS = true
		case "a":
			n, ok := fld.Value.(lkal.Int)
			if !ok {
				return nil, newError(CodeInvalidInput, p, "trie node a must be an int", "")
			}
			actorIndex = int64(n)
			hasA = true
		case "o":
			e, ok := fld.Value.(lkal.Enum)
			if !ok {
				return nil, newError(CodeInvalidInput, p, "trie node o must be an enum", "")
			}
			switch string(e) {
			case "created":
				origin = OriginCreated
			case "modified":
				origin = OriginModified
			default:
				return nil, newError(CodeInvalidInput, p, fmt.Sprintf("invalid trie node origin %q", string(e)), "")
			}
			hasO = true
		case "w":
			b, ok := fld.Value.(lkal.Bool)
			if !ok {
				return nil, newError(CodeInvalidInput, p, "trie node w must be a bool", "")
			}
			subtree = bool(b)
			hasW = true
		case "c":
			sub, ok := fld.Value.(lkal.Record)
			if !ok {
				return nil, newError(CodeInvalidInput, p, "trie node c must be a record", "")
			}
			childrenRec = sub
			hasC = true
		default:
			return nil, newError(CodeInvalidInput, p, fmt.Sprintf("unexpected trie node field %q", fld.Key), "")
		}
	}

	node := &trieNode{}
	if hasS {
		if state == StateOwned {
			if !hasA || !hasO {
				return nil, newError(CodeInvalidInput, p, "owned trie node requires both a and o", "")
			}
			if actorIndex < 0 || actorIndex >= int64(len(actors)) {
				return nil, newError(CodeInvalidInput, p, fmt.Sprintf("actor index %d out of bounds (len=%d)", actorIndex, len(actors)), "")
			}
			node.entry = Entry{State: StateOwned, Owner: actors[actorIndex], Origin: origin}
		} else {
			if hasA || hasO {
				return nil, newError(CodeInvalidInput, p, fmt.Sprintf("%s trie node must not set a or o", state), "")
			}
			node.entry = Entry{State: state}
		}
		node.hasEntry = true
		node.subtree = subtree
		if subtree && hasC {
			return nil, newError(CodeInvalidInput, p, "subtree-compressed trie node (w=true) must not contain children c", "")
		}
	} else {
		if hasA || hasO || hasW {
			return nil, newError(CodeInvalidInput, p, "trie node without s must not set a, o, or w", "")
		}
		if !hasC || len(childrenRec.Fields) == 0 {
			return nil, newError(CodeInvalidInput, p, "empty trie node must set s or non-empty c", "")
		}
	}

	if hasC {
		if len(childrenRec.Fields) == 0 {
			return nil, newError(CodeInvalidInput, p, "trie node children c must not be empty", "")
		}
		ch, err := parseTrieMap(childrenRec, actors, p)
		if err != nil {
			return nil, err
		}
		node.children = ch
	}
	return node, nil
}

func resolveInTrie(trie map[string]*trieNode, p Path, inherited Entry) Entry {
	cur := trie
	eff := inherited
	for i, seg := range p {
		node, ok := cur[seg]
		if !ok {
			return eff
		}
		if i == len(p)-1 {
			if node.hasEntry {
				return node.entry
			}
			return eff
		}
		if node.hasEntry && node.subtree {
			eff = node.entry
		}
		cur = node.children
	}
	return eff
}

func findField(rec lkal.Record, key string) (lkal.Value, bool) {
	for _, f := range rec.Fields {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

func cloneRecord(rec lkal.Record) lkal.Record {
	if len(rec.Fields) == 0 {
		return lkal.Record{}
	}
	fields := make([]lkal.Field, len(rec.Fields))
	for i, f := range rec.Fields {
		fields[i] = lkal.Field{Key: f.Key, Value: cloneValue(f.Value)}
	}
	return lkal.Record{Fields: fields}
}

func cloneValue(v lkal.Value) lkal.Value {
	switch val := v.(type) {
	case lkal.Record:
		return cloneRecord(val)
	case lkal.List:
		if len(val) == 0 {
			return lkal.List(nil)
		}
		out := make(lkal.List, len(val))
		for i, el := range val {
			out[i] = cloneValue(el)
		}
		return out
	case lkal.Ref:
		return lkal.Ref(slices.Clone(val))
	default:
		return v
	}
}

func canonicalValueJSON(v lkal.Value) (string, error) {
	f, err := lkal.FromJSON([]byte(emptyLKALJSON))
	if err != nil {
		return "", err
	}
	if err := f.Set("v", v); err != nil {
		return "", err
	}
	raw, err := lkal.ToJSON(f, false)
	if err != nil {
		return "", err
	}
	j := jsontext.Value(raw)
	if err := j.Compact(); err != nil {
		return "", err
	}
	return string(j), nil
}

func sortRecordKeys(rec lkal.Record) lkal.Record {
	if len(rec.Fields) == 0 {
		return lkal.Record{}
	}
	fields := make([]lkal.Field, len(rec.Fields))
	for i, f := range rec.Fields {
		v := f.Value
		if sub, ok := v.(lkal.Record); ok {
			v = sortRecordKeys(sub)
		} else {
			v = cloneValue(v)
		}
		fields[i] = lkal.Field{Key: f.Key, Value: v}
	}
	slices.SortFunc(fields, func(a, b lkal.Field) int {
		return strings.Compare(a.Key, b.Key)
	})
	return lkal.Record{Fields: fields}
}
