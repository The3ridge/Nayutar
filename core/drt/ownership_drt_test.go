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
	"github.com/Kaikei-e/Nayutar/core/resource/ownership"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

type ownPermWire struct {
	Set     bool `json:"set"`
	Delete  bool `json:"delete"`
	Own     bool `json:"own"`
	Resolve bool `json:"resolve"`
}

func toOwnPermWire(p ownership.Perm) ownPermWire {
	return ownPermWire{
		Set:     p&ownership.PermSet != 0,
		Delete:  p&ownership.PermDelete != 0,
		Own:     p&ownership.PermOwn != 0,
		Resolve: p&ownership.PermResolve != 0,
	}
}

type ownScopeRuleWire struct {
	Match  string      `json:"match"`
	Prefix []string    `json:"prefix"`
	Perms  ownPermWire `json:"perms"`
}

func ownMatchModeStr(m ownership.MatchMode) string {
	switch m {
	case ownership.MatchRoot:
		return "root"
	case ownership.MatchSubtree:
		return "subtree"
	case ownership.MatchExact:
		return "exact"
	default:
		return "invalid"
	}
}

func toOwnScopeWires(rules []ownership.Rule) []ownScopeRuleWire {
	out := make([]ownScopeRuleWire, len(rules))
	for i, r := range rules {
		pfx := []string(r.Prefix)
		if pfx == nil {
			pfx = []string{}
		}
		out[i] = ownScopeRuleWire{
			Match:  ownMatchModeStr(r.Match),
			Prefix: pfx,
			Perms:  toOwnPermWire(r.Perms),
		}
	}
	return out
}

type ownEntryWire struct {
	Path   []string `json:"path"`
	State  string   `json:"state"`
	Owner  string   `json:"owner,omitempty"`
	Origin string   `json:"origin,omitempty"`
}

type ownStateWire struct {
	Creator string         `json:"creator"`
	Entries []ownEntryWire `json:"entries"`
}

func toOwnStateWire(st ownStateInput) ownStateWire {
	entries := make([]ownEntryWire, len(st.Entries))
	for i, e := range st.Entries {
		p := []string(e.Path)
		if p == nil {
			p = []string{}
		}
		w := ownEntryWire{
			Path:  p,
			State: e.Entry.State.String(),
		}
		if e.Entry.State == ownership.StateOwned {
			w.Owner = string(e.Entry.Owner)
			w.Origin = e.Entry.Origin.String()
		}
		entries[i] = w
	}
	return ownStateWire{
		Creator: string(st.Creator),
		Entries: entries,
	}
}

type ownEntryInput struct {
	Path  ownership.Path
	Entry ownership.Entry
}

type ownStateInput struct {
	Creator ownership.ActorID
	Entries []ownEntryInput
}

func (s ownStateInput) toGoState() ownership.State {
	st := ownership.NewState(s.Creator)
	for _, e := range s.Entries {
		st.Entries[e.Path.String()] = e.Entry
	}
	return st
}

func fromGoState(st ownership.State) ownStateWire {
	paths := st.SortedPaths()
	entries := make([]ownEntryWire, 0, len(paths))
	for _, p := range paths {
		e, _ := st.Get(p)
		w := ownEntryWire{
			Path:  []string(p),
			State: e.State.String(),
		}
		if e.State == ownership.StateOwned {
			w.Owner = string(e.Owner)
			w.Origin = e.Origin.String()
		}
		entries = append(entries, w)
	}
	return ownStateWire{
		Creator: string(st.DefaultOwner),
		Entries: entries,
	}
}

type ownDiagWire struct {
	Code string   `json:"code"`
	Path []string `json:"path"`
}

type ownLeafWire struct {
	Path  []string   `json:"path"`
	Value lkal.Value `json:"-"`
}

type ownershipCase struct {
	ID                int
	Op                string
	Doc               lkal.Record
	OldDoc            lkal.Record
	NextDoc           lkal.Record
	Decl              lkal.Record
	State             ownStateInput
	Actor             ownership.ActorID
	ScopeRules        []ownership.Rule
	Path              ownership.Path
	Req               ownership.Perm
	RejectNestedEmpty bool
	ExpectMatch       bool
	OwnerRevMatch     bool
}

func (c ownershipCase) MarshalJSON() ([]byte, error) {
	pathSlice := []string(c.Path)
	if pathSlice == nil {
		pathSlice = []string{}
	}
	switch c.Op {
	case "project":
		return json.Marshal(struct {
			ID                int       `json:"id"`
			Op                string    `json:"op"`
			Doc               wireValue `json:"doc"`
			RejectNestedEmpty bool      `json:"rejectNestedEmpty"`
		}{c.ID, c.Op, wireValue{c.Doc}, c.RejectNestedEmpty})
	case "scope":
		return json.Marshal(struct {
			ID    int                `json:"id"`
			Op    string             `json:"op"`
			Scope []ownScopeRuleWire `json:"scope"`
			Path  []string           `json:"path"`
			Req   ownPermWire        `json:"req"`
		}{c.ID, c.Op, toOwnScopeWires(c.ScopeRules), pathSlice, toOwnPermWire(c.Req)})
	case "planApply", "planPrune":
		return json.Marshal(struct {
			ID            int                `json:"id"`
			Op            string             `json:"op"`
			Doc           wireValue          `json:"doc"`
			State         ownStateWire       `json:"state"`
			Decl          wireValue          `json:"decl"`
			Actor         string             `json:"actor"`
			Scope         []ownScopeRuleWire `json:"scope"`
			ExpectMatch   bool               `json:"expectMatch"`
			OwnerRevMatch bool               `json:"ownerRevMatch"`
		}{c.ID, c.Op, wireValue{c.Doc}, toOwnStateWire(c.State), wireValue{c.Decl},
			string(c.Actor), toOwnScopeWires(c.ScopeRules), c.ExpectMatch, c.OwnerRevMatch})
	case "adopt", "release":
		return json.Marshal(struct {
			ID            int                `json:"id"`
			Op            string             `json:"op"`
			Doc           wireValue          `json:"doc"`
			State         ownStateWire       `json:"state"`
			Actor         string             `json:"actor"`
			Scope         []ownScopeRuleWire `json:"scope"`
			Path          []string           `json:"path"`
			ExpectMatch   bool               `json:"expectMatch"`
			OwnerRevMatch bool               `json:"ownerRevMatch"`
		}{c.ID, c.Op, wireValue{c.Doc}, toOwnStateWire(c.State), string(c.Actor),
			toOwnScopeWires(c.ScopeRules), pathSlice, c.ExpectMatch, c.OwnerRevMatch})
	case "checkUpdate":
		return json.Marshal(struct {
			ID      int                `json:"id"`
			Op      string             `json:"op"`
			OldDoc  wireValue          `json:"oldDoc"`
			State   ownStateWire       `json:"state"`
			NextDoc wireValue          `json:"nextDoc"`
			Actor   string             `json:"actor"`
			Scope   []ownScopeRuleWire `json:"scope"`
		}{c.ID, c.Op, wireValue{c.OldDoc}, toOwnStateWire(c.State), wireValue{c.NextDoc},
			string(c.Actor), toOwnScopeWires(c.ScopeRules)})
	case "trie":
		return json.Marshal(struct {
			ID    int          `json:"id"`
			Op    string       `json:"op"`
			Doc   wireValue    `json:"doc"`
			State ownStateWire `json:"state"`
		}{c.ID, c.Op, wireValue{c.Doc}, toOwnStateWire(c.State)})
	default:
		return nil, fmt.Errorf("unknown ownership op %q", c.Op)
	}
}

type ownershipResult struct {
	ID          int
	Op          string
	OK          bool
	Code        string
	Leaves      []ownLeafWire
	Perms       ownPermWire
	Allows      bool
	Blocked     bool
	PrimaryCode string
	Doc         lkal.Record
	State       ownStateWire
	Diags       []ownDiagWire
	Actors      []string
}

var (
	ownTestObj = keyspace.Object{Group: "apps.example.com", Kind: "workload", Name: "web"}

	ownDiagCodes = []string{
		"K101", "K102",
		"K201", "K202", "K203", "K204", "K206",
		"K301", "K302",
		"K401", "K502",
		"K602", "K603", "K604",
	}

	ownRequiredBuckets = []string{
		"project/ok",
		"project/K101",
		"project/K102",
		"scope/ok-allow",
		"scope/ok-deny",
		"scope/K101",
		"planApply/ok",
		"planApply/blocked-K201",
		"planApply/blocked-K202",
		"planApply/blocked-K203",
		"planApply/blocked-K204",
		"planApply/blocked-K206",
		"planApply/blocked-K401",
		"planApply/err-K101",
		"planApply/err-K102",
		"planApply/err-K201",
		"planApply/err-K203",
		"planPrune/ok",
		"planPrune/blocked-K202",
		"planPrune/blocked-K204",
		"planPrune/blocked-K401",
		"planPrune/err-K102",
		"planPrune/err-K201",
		"planPrune/err-K203",
		"adopt/ok",
		"adopt/K101",
		"adopt/K201",
		"adopt/K202",
		"adopt/K203",
		"adopt/K401",
		"release/ok",
		"release/K101",
		"release/K201",
		"release/K203",
		"release/K401",
		"checkUpdate/ok",
		"checkUpdate/K101",
		"checkUpdate/K201",
		"checkUpdate/K204",
		"checkUpdate/K206",
		"checkUpdate/K401",
		"trie/ok",
		"trie/K101",
	}
)

func genOwnScalar(rng *rand.Rand) lkal.Value {
	switch rng.IntN(7) {
	case 0:
		return lkal.String(pick(rng, []string{"nginx:1.25", "v2", "prod", "dev", "café"}))
	case 1:
		return lkal.Int(pick(rng, []int64{-5, 0, 1, 2, 3, 5, 10, 80, 443}))
	case 2:
		return lkal.Decimal(pick(rng, []string{"0.5", "1.0", "2.25"}))
	case 3:
		return lkal.Bool(rng.IntN(2) == 0)
	case 4:
		return lkal.Enum(pick(rng, []string{"tcp", "udp", "always", "never"}))
	case 5:
		return lkal.Quantity{
			Number: pick(rng, []string{"128", "256", "512"}),
			Unit:   pick(rng, []string{"Mi", "m", "Gi"}),
		}
	default:
		return lkal.List{lkal.String("a"), lkal.String(pick(rng, []string{"b", "c"}))}
	}
}

func otherOwnScalar(v lkal.Value) lkal.Value {
	if s, ok := v.(lkal.String); ok {
		return lkal.String(string(s) + "-other")
	}
	if n, ok := v.(lkal.Int); ok {
		return lkal.Int(n + 1)
	}
	return lkal.String("different-value")
}

func baseOwnDoc(rng *rand.Rand) lkal.Record {
	doc := envRec(
		envField("meta", envRec(
			envField("group", lkal.String(ownTestObj.Group)),
			envField("kind", lkal.String(ownTestObj.Kind)),
			envField("name", lkal.String(ownTestObj.Name)),
			envField("labels", envRec(
				envField("env", lkal.String("prod")),
				envField("app.example.com/tier", lkal.String("web")),
			)),
		)),
		envField("spec", envRec(
			envField("replicas", lkal.Int(3)),
			envField("image", lkal.String("nginx:1.25")),
			envField("defVal", lkal.Int(1)),
			envField("container", envRec(
				envField("app", envRec(
					envField("port", lkal.Int(80)),
					envField("proto", lkal.Enum("tcp")),
				)),
			)),
			envField("plugins", envRec(
				envField("bridge", envRec(
					envField("order", lkal.Int(10)),
					envField("mode", lkal.String("l2")),
				)),
			)),
		)),
	)
	if chance(rng, 50) {
		doc = envShuffleRecord(rng, doc)
	}
	return doc
}

func baseOwnState(creator ownership.ActorID) ownStateInput {
	return ownStateInput{
		Creator: creator,
		Entries: []ownEntryInput{
			{Path: ownership.Path{"meta", "labels", "env"}, Entry: ownership.Entry{State: ownership.StateOwned, Owner: creator, Origin: ownership.OriginCreated}},
			{Path: ownership.Path{"meta", "labels", "app.example.com/tier"}, Entry: ownership.Entry{State: ownership.StateOwned, Owner: creator, Origin: ownership.OriginCreated}},
			{Path: ownership.Path{"spec", "replicas"}, Entry: ownership.Entry{State: ownership.StateOwned, Owner: "controller:hpa", Origin: ownership.OriginModified}},
			{Path: ownership.Path{"spec", "image"}, Entry: ownership.Entry{State: ownership.StateOwned, Owner: creator, Origin: ownership.OriginCreated}},
			{Path: ownership.Path{"spec", "defVal"}, Entry: ownership.Entry{State: ownership.StateDefaulted}},
			{Path: ownership.Path{"spec", "container", "app", "port"}, Entry: ownership.Entry{State: ownership.StateOwned, Owner: "controller:mesh", Origin: ownership.OriginCreated}},
			{Path: ownership.Path{"spec", "container", "app", "proto"}, Entry: ownership.Entry{State: ownership.StateOwned, Owner: "controller:mesh", Origin: ownership.OriginCreated}},
			{Path: ownership.Path{"spec", "plugins", "bridge", "order"}, Entry: ownership.Entry{State: ownership.StateOwned, Owner: creator, Origin: ownership.OriginCreated}},
			{Path: ownership.Path{"spec", "plugins", "bridge", "mode"}, Entry: ownership.Entry{State: ownership.StateOwned, Owner: creator, Origin: ownership.OriginCreated}},
		},
	}
}

func ownSetEntry(st ownStateInput, p ownership.Path, e ownership.Entry) ownStateInput {
	out := ownStateInput{Creator: st.Creator, Entries: slices.Clone(st.Entries)}
	for i := range out.Entries {
		if out.Entries[i].Path.Equal(p) {
			out.Entries[i].Entry = e
			return out
		}
	}
	out.Entries = append(out.Entries, ownEntryInput{Path: p.Clone(), Entry: e})
	return out
}

func ownRootRules(p ownership.Perm) []ownership.Rule {
	return []ownership.Rule{{Match: ownership.MatchRoot, Perms: p}}
}

func genOwnershipBucketCase(rng *rand.Rand, id int, bucket string) ownershipCase {
	op, sub, _ := strings.Cut(bucket, "/")
	c := ownershipCase{
		ID:            id,
		Op:            op,
		Actor:         "user:alice",
		ExpectMatch:   true,
		OwnerRevMatch: true,
	}

	switch op {
	case "project":
		switch sub {
		case "ok":
			c.RejectNestedEmpty = chance(rng, 50)
			switch rng.IntN(4) {
			case 0:
				c.Doc = baseOwnDoc(rng)
			case 1:
				c.Doc = envRec(
					envField("meta", envRec(envField("labels", envRec()))),
					envField("spec", envRec()),
				)
			case 2:
				c.Doc = envRec(
					envField("spec", envRec(envField("empty", envRec()), envField("x", genOwnScalar(rng)))),
				)
				c.RejectNestedEmpty = false
			default:
				c.Doc = envRec(
					envField("meta", envRec(envField("labels", envRec(envField("k/1", lkal.String("v")))))),
					envField("spec", envRec(envField("a", genOwnScalar(rng)))),
					envField("status", envRec(envField("ignored", envRec()))),
				)
			}
		case "K101":
			c.RejectNestedEmpty = chance(rng, 50)
			switch rng.IntN(5) {
			case 0:
				c.Doc = envRec(envField("spec", lkal.Int(1)))
			case 1:
				c.Doc = envRec(envField("meta", envRec(envField("labels", lkal.String("bad")))))
			case 2:
				c.Doc = envRec(envField("spec", envRec(envField("$reserved", lkal.Int(1)))))
			case 3:
				c.Doc = envRec(envField("spec", envRec(envField("mixed", lkal.List{lkal.Int(1), lkal.String("x")}))))
			default:
				c.Doc = envRec(envField("spec", envRec(envField("dup", lkal.Int(1)), envField("dup", lkal.Int(2)))))
			}
		case "K102":
			c.RejectNestedEmpty = true
			if chance(rng, 50) {
				c.Doc = envRec(envField("spec", envRec(envField("nested", envRec()))))
			} else {
				c.Doc = envRec(envField("meta", envRec(envField("labels", envRec(envField("bad", envRec()))))))
			}
		}

	case "scope":
		switch sub {
		case "ok-allow":
			switch rng.IntN(3) {
			case 0:
				c.ScopeRules = ownRootRules(ownership.PermRegular)
				c.Path = ownership.Path{"spec", "replicas"}
				c.Req = ownership.PermSet | ownership.PermOwn
			case 1:
				c.ScopeRules = []ownership.Rule{{Match: ownership.MatchSubtree, Prefix: ownership.Path{"spec", "container"}, Perms: ownership.PermAll}}
				c.Path = ownership.Path{"spec", "container", "app", "port"}
				c.Req = ownership.PermOwn | ownership.PermResolve
			default:
				c.ScopeRules = []ownership.Rule{{Match: ownership.MatchExact, Prefix: ownership.Path{"meta", "labels", "app.example.com/tier"}, Perms: ownership.PermOwn}}
				c.Path = ownership.Path{"meta", "labels", "app.example.com/tier"}
				c.Req = ownership.PermOwn
			}
		case "ok-deny":
			switch rng.IntN(4) {
			case 0:
				c.ScopeRules = nil
				c.Path = ownership.Path{"spec", "replicas"}
				c.Req = ownership.PermOwn
			case 1:
				// Segment boundary check: spec/a must not match spec/ab.
				c.ScopeRules = []ownership.Rule{{Match: ownership.MatchSubtree, Prefix: ownership.Path{"spec", "a"}, Perms: ownership.PermAll}}
				c.Path = ownership.Path{"spec", "ab"}
				c.Req = ownership.PermSet
			case 2:
				// Resolve is never implied by PermRegular.
				c.ScopeRules = ownRootRules(ownership.PermRegular)
				c.Path = ownership.Path{"spec", "replicas"}
				c.Req = ownership.PermOwn | ownership.PermResolve
			default:
				// Outside EntranceSpec denied.
				c.ScopeRules = ownRootRules(ownership.PermAll)
				c.Path = pick(rng, []ownership.Path{{"meta", "uid"}, {"status", "phase"}})
				c.Req = ownership.PermOwn
			}
		case "K101":
			switch rng.IntN(3) {
			case 0:
				c.ScopeRules = []ownership.Rule{{Match: ownership.MatchRoot, Prefix: ownership.Path{"spec"}, Perms: ownership.PermOwn}}
			case 1:
				c.ScopeRules = []ownership.Rule{{Match: ownership.MatchExact, Prefix: ownership.Path{"spec", "$bad"}, Perms: ownership.PermOwn}}
			default:
				c.ScopeRules = []ownership.Rule{{Match: ownership.MatchSubtree, Prefix: ownership.Path{"spec"}, Perms: ownership.PermNone}}
			}
			c.Path = ownership.Path{"spec", "replicas"}
			c.Req = ownership.PermOwn
		}

	case "planApply":
		c.Doc = baseOwnDoc(rng)
		c.State = baseOwnState("user:alice")
		c.ScopeRules = ownRootRules(ownership.PermRegular)
		switch sub {
		case "ok":
			switch rng.IntN(6) {
			case 0:
				// Converged + retired created (K604 notice in Apply, no deletion).
				c.Decl = envRec(envField("spec", envRec(envField("image", lkal.String("nginx:1.25")))))
			case 1:
				// Update self-owned + create new + claim .defaulted with DIFFERENT value.
				c.Decl = envRec(envField("spec", envRec(
					envField("image", lkal.String("nginx:1.26")),
					envField("defVal", lkal.Int(99)),
					envField("extra", genOwnScalar(rng)),
				)))
			case 2:
				// Claim .defaulted with EQUAL value (EnvChanged=false, OwnChanged=true).
				c.Decl = envRec(envField("spec", envRec(
					envField("image", lkal.String("nginx:1.25")),
					envField("defVal", lkal.Int(1)),
				)))
			case 3:
				// Adopt matching unowned leaf + unmanage retired modified leaf.
				c.State = ownSetEntry(c.State, ownership.Path{"spec", "image"}, ownership.Entry{State: ownership.StateUnowned})
				c.State = ownSetEntry(c.State, ownership.Path{"spec", "replicas"}, ownership.Entry{State: ownership.StateOwned, Owner: "user:alice", Origin: ownership.OriginModified})
				c.Decl = envRec(envField("spec", envRec(envField("image", lkal.String("nginx:1.25")))))
			case 4:
				// Clear absent retired created entry + deferred K603 over retired modified overlap.
				c.State = ownSetEntry(c.State, ownership.Path{"spec", "ghost"}, ownership.Entry{State: ownership.StateOwned, Owner: "user:alice", Origin: ownership.OriginCreated})
				c.State = ownSetEntry(c.State, ownership.Path{"spec", "legacy"}, ownership.Entry{State: ownership.StateOwned, Owner: "user:alice", Origin: ownership.OriginModified})
				c.Decl = envRec(envField("spec", envRec(envField("legacy", envRec(envField("sub", lkal.Int(1)))))))
			default:
				// Empty section roots retire all alice's created entries -> deferred K604.
				c.Decl = envRec(
					envField("meta", envRec(envField("labels", envRec()))),
					envField("spec", envRec()),
				)
			}
		case "blocked-K201":
			switch rng.IntN(3) {
			case 0:
				// Same-path foreign conflict even with equal value.
				c.Decl = envRec(envField("spec", envRec(envField("replicas", lkal.Int(3)), envField("extra", lkal.Int(1)))))
			case 1:
				// Proper ancestor overlap with foreign-owned descendants.
				c.Decl = envRec(envField("spec", envRec(envField("container", lkal.String("flat")))))
			default:
				// Proper descendant overlap under foreign-owned leaf.
				c.Decl = envRec(envField("spec", envRec(envField("replicas", envRec(envField("min", lkal.Int(1)))))))
			}
		case "blocked-K202":
			c.State = ownStateInput{Creator: "user:alice"}
			if chance(rng, 50) {
				// Target is an existing record.
				c.Decl = envRec(envField("spec", envRec(envField("container", lkal.String("oops")))))
			} else {
				// Proper ancestor is an existing leaf.
				c.Decl = envRec(envField("spec", envRec(envField("image", envRec(envField("tag", lkal.String("v1")))))))
			}
		case "blocked-K203":
			// DriftDeleted: self-owned leaf missing from doc.
			c.State = ownSetEntry(c.State, ownership.Path{"spec", "missing"}, ownership.Entry{State: ownership.StateOwned, Owner: "user:alice", Origin: ownership.OriginCreated})
			c.Decl = envRec(envField("spec", envRec(envField("missing", lkal.Int(5)))))
		case "blocked-K204":
			// MigrationRequired: declared leaf overlaps retired self-Created leaf.
			c.Decl = envRec(envField("spec", envRec(envField("plugins", lkal.String("none")))))
		case "blocked-K206":
			// Unowned leaf with mismatched value.
			c.State = ownSetEntry(c.State, ownership.Path{"spec", "image"}, ownership.Entry{State: ownership.StateUnowned})
			c.Decl = envRec(envField("spec", envRec(envField("image", lkal.String("busybox:1")))))
		case "blocked-K401":
			c.ScopeRules = []ownership.Rule{{Match: ownership.MatchRoot, Perms: ownership.PermOwn}}
			c.Decl = envRec(envField("spec", envRec(envField("image", lkal.String("nginx:1.26")))))
		case "err-K101":
			if chance(rng, 50) {
				c.Actor = "   "
			} else {
				c.ScopeRules = []ownership.Rule{{Match: ownership.MatchRoot, Prefix: ownership.Path{"spec"}, Perms: ownership.PermOwn}}
			}
			c.Decl = envRec(envField("spec", envRec(envField("image", lkal.String("nginx:1.25")))))
		case "err-K102":
			c.Decl = envRec(envField("spec", envRec(envField("bad", envRec()))))
		case "err-K201":
			c.OwnerRevMatch = false
			c.Decl = envRec(envField("spec", envRec(envField("image", lkal.String("nginx:1.25")))))
		case "err-K203":
			c.ExpectMatch = false
			c.Decl = envRec(envField("spec", envRec(envField("image", lkal.String("nginx:1.25")))))
		}

	case "planPrune":
		c.Doc = baseOwnDoc(rng)
		c.State = baseOwnState("user:alice")
		c.ScopeRules = ownRootRules(ownership.PermRegular)
		switch sub {
		case "ok":
			switch rng.IntN(6) {
			case 0:
				// Prune retired created plugins/bridge/* -> bottom-up removes bridge and plugins, keeps spec!
				c.Decl = envRec(
					envField("meta", envRec(envField("labels", envRec(
						envField("env", lkal.String("prod")),
						envField("app.example.com/tier", lkal.String("web")),
					)))),
					envField("spec", envRec(envField("image", lkal.String("nginx:1.25")))),
				)
			case 1:
				// Prune all meta/labels/* -> removes meta/labels field cleanly!
				c.Decl = envRec(
					envField("spec", envRec(
						envField("image", lkal.String("nginx:1.25")),
						envField("plugins", envRec(envField("bridge", envRec(
							envField("order", lkal.Int(10)),
							envField("mode", lkal.String("l2")),
						)))),
					)),
				)
			case 2:
				// Prune leaves intermediate record containing only .defaulted sibling -> prunes both!
				c.State = ownSetEntry(c.State, ownership.Path{"spec", "plugins", "bridge", "mode"}, ownership.Entry{State: ownership.StateDefaulted})
				c.Decl = envRec(envField("spec", envRec(envField("image", lkal.String("nginx:1.25")))))
			case 3:
				// Cross-mode isolation: declared K201 conflict is deferred in Prune, retired created still prunes!
				c.Decl = envRec(envField("spec", envRec(envField("replicas", lkal.Int(99)))))
			case 4:
				// Conservative deletion: retired self-owned OriginModified leaf is deferred in Prune and never deleted!
				c.State = ownSetEntry(c.State, ownership.Path{"spec", "image"}, ownership.Entry{
					State:  ownership.StateOwned,
					Owner:  "user:alice",
					Origin: ownership.OriginModified,
				})
				c.Decl = envRec(envField("spec", envRec()))
			default:
				// Empty declaration prunes all alice's created leaves; foreign mesh/hpa leaves untouched!
				c.Decl = envRec(envField("spec", envRec()))
			}
		case "blocked-K202":
			// Retired created path is now a record in doc -> K202 anomaly blocks Prune!
			c.State = ownSetEntry(c.State, ownership.Path{"spec", "container"}, ownership.Entry{State: ownership.StateOwned, Owner: "user:alice", Origin: ownership.OriginCreated})
			c.Decl = envRec(envField("spec", envRec(envField("image", lkal.String("nginx:1.25")))))
		case "blocked-K204":
			// Retired created overlaps declared path -> global blocker K204 in Prune too!
			c.Decl = envRec(envField("spec", envRec(envField("plugins", lkal.String("none")))))
		case "blocked-K401":
			c.ScopeRules = []ownership.Rule{{Match: ownership.MatchRoot, Perms: ownership.PermSet | ownership.PermOwn}}
			c.Decl = envRec(envField("spec", envRec()))
		case "err-K102":
			c.Decl = envRec(envField("spec", envRec(envField("empty", envRec()))))
		case "err-K201":
			c.OwnerRevMatch = false
			c.Decl = envRec(envField("spec", envRec()))
		case "err-K203":
			c.ExpectMatch = false
			c.Decl = envRec(envField("spec", envRec()))
		}

	case "adopt":
		c.Doc = baseOwnDoc(rng)
		c.State = baseOwnState("user:alice")
		c.ScopeRules = ownRootRules(ownership.PermAll)
		switch sub {
		case "ok":
			switch rng.IntN(3) {
			case 0:
				// Adopt unowned leaf -> OriginModified.
				c.State = ownSetEntry(c.State, ownership.Path{"spec", "image"}, ownership.Entry{State: ownership.StateUnowned})
				c.Path = ownership.Path{"spec", "image"}
			case 1:
				// Adopt .defaulted leaf -> OriginModified (never Created).
				c.Path = ownership.Path{"spec", "defVal"}
			default:
				// Adopt self-Created leaf -> downgrades to OriginModified + K602 notice.
				c.Path = ownership.Path{"spec", "image"}
			}
		case "K101":
			c.Path = ownership.Path{"meta", "uid"}
		case "K201":
			switch rng.IntN(3) {
			case 0:
				// Foreign-owned leaf.
				c.Path = ownership.Path{"spec", "replicas"}
			case 1:
				// Self proper ancestor/descendant overlap.
				c.State = ownSetEntry(c.State, ownership.Path{"spec", "plugins"}, ownership.Entry{State: ownership.StateOwned, Owner: "user:alice", Origin: ownership.OriginModified})
				c.Path = ownership.Path{"spec", "plugins", "bridge", "order"}
			default:
				// Stale ownerRevision.
				c.Path = ownership.Path{"spec", "defVal"}
				c.OwnerRevMatch = false
			}
		case "K202":
			if chance(rng, 50) {
				c.Path = ownership.Path{"spec", "absentLeaf"}
			} else {
				c.State = ownStateInput{Creator: "user:alice"}
				c.Path = ownership.Path{"spec", "container"}
			}
		case "K203":
			c.Path = ownership.Path{"spec", "defVal"}
			c.ExpectMatch = false
		case "K401":
			// PermRegular lacks PermResolve!
			c.ScopeRules = ownRootRules(ownership.PermRegular)
			c.Path = ownership.Path{"spec", "defVal"}
		}

	case "release":
		c.Doc = baseOwnDoc(rng)
		c.State = baseOwnState("user:alice")
		c.ScopeRules = ownRootRules(ownership.PermAll)
		switch sub {
		case "ok":
			switch rng.IntN(3) {
			case 0:
				// Release self-Created present leaf -> StateUnowned + K602 notice.
				c.Path = ownership.Path{"spec", "image"}
			case 1:
				// Release self-Modified present leaf -> StateUnowned, no K602.
				c.State = ownSetEntry(c.State, ownership.Path{"spec", "image"}, ownership.Entry{State: ownership.StateOwned, Owner: "user:alice", Origin: ownership.OriginModified})
				c.Path = ownership.Path{"spec", "image"}
			default:
				// Release absent self-owned leaf -> deletes entry.
				c.State = ownSetEntry(c.State, ownership.Path{"spec", "ghost"}, ownership.Entry{State: ownership.StateOwned, Owner: "user:alice", Origin: ownership.OriginCreated})
				c.Path = ownership.Path{"spec", "ghost"}
			}
		case "K101":
			c.Path = ownership.Path{"status", "phase"}
		case "K201":
			switch rng.IntN(3) {
			case 0:
				// Foreign-owned leaf.
				c.Path = ownership.Path{"spec", "replicas"}
			case 1:
				// Defaulted (not owned by actor).
				c.Path = ownership.Path{"spec", "defVal"}
			default:
				// Stale ownerRevision.
				c.Path = ownership.Path{"spec", "image"}
				c.OwnerRevMatch = false
			}
		case "K203":
			c.Path = ownership.Path{"spec", "image"}
			c.ExpectMatch = false
		case "K401":
			c.ScopeRules = ownRootRules(ownership.PermRegular)
			c.Path = ownership.Path{"spec", "image"}
		}

	case "checkUpdate":
		oldDoc := baseOwnDoc(rng)
		c.OldDoc = oldDoc
		c.State = baseOwnState("user:alice")
		c.ScopeRules = ownRootRules(ownership.PermRegular)
		switch sub {
		case "ok":
			next := envCloneRecord(oldDoc)
			sp, _ := envGetRecord(next, "spec")
			switch rng.IntN(5) {
			case 0:
				// Unchanged doc (including foreign & defaulted leaves untouched).
				c.NextDoc = envShuffleRecord(rng, next)
			case 1:
				// Modify self-owned leaf + add new leaf.
				sp = envSetField(sp, "image", lkal.String("nginx:1.26"))
				sp = envSetField(sp, "added", genOwnScalar(rng))
				c.NextDoc = envSetField(next, "spec", sp)
			case 2:
				// Claim .defaulted leaf via update -> becomes Created.
				sp = envSetField(sp, "defVal", lkal.Int(42))
				c.NextDoc = envSetField(next, "spec", sp)
			case 3:
				// Re-adding exact retired self-owned OriginModified leaf preserves OriginModified (no laundering).
				c.State = ownSetEntry(c.State, ownership.Path{"spec", "retiredMod"}, ownership.Entry{
					State:  ownership.StateOwned,
					Owner:  "user:alice",
					Origin: ownership.OriginModified,
				})
				sp = envSetField(sp, "retiredMod", lkal.String("restored"))
				c.NextDoc = envSetField(next, "spec", sp)
			default:
				// Delete self-owned leaf and omitted .defaulted leaf.
				sp = envDropField(sp, "image")
				sp = envDropField(sp, "defVal")
				c.NextDoc = envSetField(next, "spec", sp)
			}
		case "K101":
			c.Actor = ""
			c.NextDoc = envCloneRecord(oldDoc)
		case "K201":
			next := envCloneRecord(oldDoc)
			sp, _ := envGetRecord(next, "spec")
			switch rng.IntN(3) {
			case 0:
				sp = envSetField(sp, "replicas", lkal.Int(10))
			case 1:
				sp = envDropField(sp, "replicas")
			default:
				// Adding descendant under retired self-owned OriginModified leaf -> K201.
				c.State = ownSetEntry(c.State, ownership.Path{"spec", "retiredMod"}, ownership.Entry{
					State:  ownership.StateOwned,
					Owner:  "user:alice",
					Origin: ownership.OriginModified,
				})
				sp = envSetField(sp, "retiredMod", envRec(envField("child", lkal.Int(1))))
			}
			c.NextDoc = envSetField(next, "spec", sp)
		case "K204":
			// Adding descendant under retired self-owned OriginCreated leaf -> K204.
			c.State = ownSetEntry(c.State, ownership.Path{"spec", "retiredCreated"}, ownership.Entry{
				State:  ownership.StateOwned,
				Owner:  "user:alice",
				Origin: ownership.OriginCreated,
			})
			next := envCloneRecord(oldDoc)
			sp, _ := envGetRecord(next, "spec")
			sp = envSetField(sp, "retiredCreated", envRec(envField("child", lkal.Int(1))))
			c.NextDoc = envSetField(next, "spec", sp)
		case "K206":
			c.State = ownSetEntry(c.State, ownership.Path{"spec", "image"}, ownership.Entry{State: ownership.StateUnowned})
			next := envCloneRecord(oldDoc)
			sp, _ := envGetRecord(next, "spec")
			if chance(rng, 50) {
				sp = envSetField(sp, "image", otherOwnScalar(lkal.String("nginx:1.25")))
			} else {
				sp = envDropField(sp, "image")
			}
			c.NextDoc = envSetField(next, "spec", sp)
		case "K401":
			next := envCloneRecord(oldDoc)
			sp, _ := envGetRecord(next, "spec")
			if chance(rng, 50) {
				c.ScopeRules = ownRootRules(ownership.PermOwn)
				sp = envSetField(sp, "image", lkal.String("nginx:1.26"))
			} else {
				c.ScopeRules = ownRootRules(ownership.PermSet | ownership.PermOwn)
				sp = envDropField(sp, "image")
			}
			c.NextDoc = envSetField(next, "spec", sp)
		}

	case "trie":
		c.Doc = baseOwnDoc(rng)
		switch sub {
		case "ok":
			switch rng.IntN(5) {
			case 0:
				// Single creator owns every leaf -> empty trie, 1 actor.
				c.State = ownStateInput{Creator: "user:alice"}
			case 1:
				// Multi-actor with uniform >=2 subtree (mesh owns container/app/{port,proto}) + defaulted.
				c.State = baseOwnState("user:alice")
			case 2:
				// Creator with OriginModified must stay Modified (never escalate to Created).
				c.State = ownSetEntry(baseOwnState("user:alice"), ownership.Path{"spec", "image"},
					ownership.Entry{State: ownership.StateOwned, Owner: "user:alice", Origin: ownership.OriginModified})
			case 3:
				// Mix of unowned, defaulted, and creator-modified.
				st := baseOwnState("user:alice")
				st = ownSetEntry(st, ownership.Path{"spec", "image"}, ownership.Entry{State: ownership.StateUnowned})
				st = ownSetEntry(st, ownership.Path{"meta", "labels", "env"}, ownership.Entry{State: ownership.StateOwned, Owner: "user:alice", Origin: ownership.OriginModified})
				c.State = st
			default:
				// Uniform >=2 subtree owned by creator with OriginModified.
				st := baseOwnState("user:alice")
				st = ownSetEntry(st, ownership.Path{"spec", "plugins", "bridge", "order"}, ownership.Entry{State: ownership.StateOwned, Owner: "user:alice", Origin: ownership.OriginModified})
				st = ownSetEntry(st, ownership.Path{"spec", "plugins", "bridge", "mode"}, ownership.Entry{State: ownership.StateOwned, Owner: "user:alice", Origin: ownership.OriginModified})
				c.State = st
			}
		case "K101":
			if chance(rng, 50) {
				c.State = ownStateInput{Creator: ""}
			} else {
				c.State = ownStateInput{
					Creator: "user:alice",
					Entries: []ownEntryInput{{Path: ownership.Path{"meta", "uid"}, Entry: ownership.Entry{State: ownership.StateUnowned}}},
				}
			}
		}
	}
	return c
}

func generateOwnershipCases(seed uint64, count int) []ownershipCase {
	rng := rand.New(rand.NewPCG(seed, seed^0x5a5a5a5a9e3779b9))
	cases := make([]ownershipCase, count)
	for i := range count {
		bucket := ownRequiredBuckets[i%len(ownRequiredBuckets)]
		if i >= len(ownRequiredBuckets) && chance(rng, 35) {
			bucket = pick(rng, ownRequiredBuckets)
		}
		cases[i] = genOwnershipBucketCase(rng, i, bucket)
	}
	return cases
}

func encodeOwnershipCases(t *testing.T, cases []ownershipCase) [][]byte {
	t.Helper()
	out := make([][]byte, len(cases))
	for i, c := range cases {
		b, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal case %d (%s): %v", c.ID, c.Op, err)
		}
		out[i] = b
	}
	return out
}

type ownershipHooks struct {
	projectAllowNestedEmpty    bool
	scopeResolveImplied        bool
	scopeRawStringPrefix       bool
	applyAllowForeignOverwrite bool
	applyDeleteRetiredCreated  bool
	applyDefaultedHitsK206     bool
	applySkipAllOrNothing      bool
	pruneKeepEmptyIntermediate bool
	pruneDeleteRetiredModified bool
	adoptSetsOriginCreated     bool
	adoptSkipsResolvePerm      bool
	adoptMutatesDoc            bool
	releaseAllowsForeign       bool
	checkUpdateAllowsForeign   bool
	trieEscalateCreatorMod     bool
	trieFallbackUnowned        bool
}

func ownershipErrCode(err error) (string, error) {
	var oe *ownership.Error
	if !errors.As(err, &oe) {
		return "", fmt.Errorf("expected *ownership.Error, got %T: %v", err, err)
	}
	return string(oe.Code), nil
}

func dedupOwnDiags(diags []ownership.Diagnostic) []ownDiagWire {
	sorted := ownership.SortDiagnostics(slices.Clone(diags))
	out := make([]ownDiagWire, 0, len(sorted))
	for _, d := range sorted {
		p := []string(d.Path)
		if p == nil {
			p = []string{}
		}
		w := ownDiagWire{Code: string(d.Code), Path: p}
		if len(out) > 0 && out[len(out)-1].Code == w.Code && slices.Equal(out[len(out)-1].Path, w.Path) {
			continue
		}
		out = append(out, w)
	}
	return out
}

func runOwnershipCase(tc ownershipCase, hooks ownershipHooks) (ownershipResult, error) {
	res := ownershipResult{ID: tc.ID, Op: tc.Op}

	switch tc.Op {
	case "project":
		allowNested := !tc.RejectNestedEmpty
		if hooks.projectAllowNestedEmpty {
			allowNested = true
		}
		leaves, err := ownership.ProjectDeclaration(tc.Doc, allowNested)
		if err != nil {
			code, mapErr := ownershipErrCode(err)
			if mapErr != nil {
				return ownershipResult{}, mapErr
			}
			res.Code = code
			return res, nil
		}
		list := make([]ownership.Leaf, 0, len(leaves))
		for _, lf := range leaves {
			list = append(list, lf)
		}
		slices.SortFunc(list, func(a, b ownership.Leaf) int { return a.Path.Compare(b.Path) })
		out := make([]ownLeafWire, len(list))
		for i, lf := range list {
			out[i] = ownLeafWire{Path: []string(lf.Path), Value: lf.Value}
		}
		res.OK = true
		res.Leaves = out
		return res, nil

	case "scope":
		sc, err := ownership.NewScope(tc.ScopeRules...)
		if err != nil {
			code, mapErr := ownershipErrCode(err)
			if mapErr != nil {
				return ownershipResult{}, mapErr
			}
			res.Code = code
			return res, nil
		}
		perms := sc.PermissionsFor(tc.Path)
		if hooks.scopeResolveImplied && perms.Has(ownership.PermRegular) {
			perms |= ownership.PermResolve
		}
		if hooks.scopeRawStringPrefix && ownership.IsEntranceSpecPath(tc.Path) {
			for _, r := range tc.ScopeRules {
				if r.Match == ownership.MatchSubtree && strings.HasPrefix(tc.Path.String(), r.Prefix.String()) {
					perms |= r.Perms
				}
			}
		}
		allows := ownership.IsEntranceSpecPath(tc.Path) && perms.Has(tc.Req)
		res.OK = true
		res.Perms = toOwnPermWire(perms)
		res.Allows = allows
		return res, nil

	case "planApply", "planPrune":
		sc, err := ownership.NewScope(tc.ScopeRules...)
		if err != nil {
			code, mapErr := ownershipErrCode(err)
			if mapErr != nil {
				return ownershipResult{}, mapErr
			}
			res.Code = code
			return res, nil
		}
		expectTok := ""
		if tc.ExpectMatch {
			tok, err := ownership.DocConditionToken(ownTestObj, tc.Doc)
			if err != nil {
				code, mapErr := ownershipErrCode(err)
				if mapErr != nil {
					return ownershipResult{}, mapErr
				}
				res.Code = code
				return res, nil
			}
			expectTok = tok
		} else {
			expectTok = "ksg-doc-v1:0000000000000000000000000000000000000000000000000000000000000000"
		}
		expRev := int64(7)
		if !tc.OwnerRevMatch {
			expRev = 6
		}
		st := tc.State.toGoState()
		if hooks.applyDefaultedHitsK206 && tc.Op == "planApply" {
			for k, e := range st.Entries {
				if e.State == ownership.StateDefaulted {
					st.Entries[k] = ownership.Entry{State: ownership.StateUnowned}
				}
			}
		}
		if hooks.applyAllowForeignOverwrite && tc.Op == "planApply" {
			for k, e := range st.Entries {
				if e.State == ownership.StateOwned && e.Owner != tc.Actor {
					st.Entries[k] = ownership.Entry{State: ownership.StateOwned, Owner: tc.Actor, Origin: e.Origin}
				}
			}
		}
		in := ownership.PlanInput{
			Object:           ownTestObj,
			Actor:            tc.Actor,
			Scope:            sc,
			CurrentDoc:       tc.Doc,
			CurrentState:     st,
			CurrentOwnerRev:  7,
			Declaration:      tc.Decl,
			Expect:           expectTok,
			ExpectedOwnerRev: expRev,
		}
		var pr ownership.PlanResult
		if tc.Op == "planApply" {
			pr, err = ownership.PlanApply(in)
		} else {
			pr, err = ownership.PlanPrune(in)
		}
		if err != nil {
			code, mapErr := ownershipErrCode(err)
			if mapErr != nil {
				return ownershipResult{}, mapErr
			}
			res.Code = code
			return res, nil
		}
		if hooks.applySkipAllOrNothing && tc.Op == "planApply" && pr.Blocked {
			pr.Blocked = false
		}
		if hooks.applyDeleteRetiredCreated && tc.Op == "planApply" && !pr.Blocked {
			if pr2, err2 := ownership.PlanPrune(in); err2 == nil && !pr2.Blocked {
				pr.NextDoc = pr2.NextDoc
			}
		}
		if hooks.pruneKeepEmptyIntermediate && tc.Op == "planPrune" && !pr.Blocked {
			if sp, ok := envGetRecord(pr.NextDoc, "spec"); ok {
				if _, hasPlugins := envGetField(sp, "plugins"); !hasPlugins {
					sp = envSetField(sp, "plugins", envRec())
					pr.NextDoc = envSetField(pr.NextDoc, "spec", sp)
				}
			}
		}
		if hooks.pruneDeleteRetiredModified && tc.Op == "planPrune" && !pr.Blocked {
			for _, p := range st.SortedPaths() {
				if e, ok := st.Get(p); ok && e.State == ownership.StateOwned && e.Owner == tc.Actor && e.Origin == ownership.OriginModified {
					pr.NextState.Delete(p)
				}
			}
		}
		primary := ""
		if pr.Blocked {
			if berr := pr.Err(); berr != nil {
				var oe *ownership.Error
				if errors.As(berr, &oe) {
					primary = string(oe.Code)
				}
			}
		} else {
			primary = string(ownership.SelectPrimaryCode(pr.Diagnostics))
		}
		res.OK = true
		res.Blocked = pr.Blocked
		res.PrimaryCode = primary
		res.Doc = pr.NextDoc
		res.State = fromGoState(pr.NextState)
		res.Diags = dedupOwnDiags(pr.Diagnostics)
		return res, nil

	case "adopt", "release":
		sc, err := ownership.NewScope(tc.ScopeRules...)
		if err != nil {
			code, mapErr := ownershipErrCode(err)
			if mapErr != nil {
				return ownershipResult{}, mapErr
			}
			res.Code = code
			return res, nil
		}
		if hooks.adoptSkipsResolvePerm && tc.Op == "adopt" {
			sc = ownership.RootScope(ownership.PermAll)
		}
		expectTok := "ksg-tok-v1:0000000000000000000000000000000000000000000000000000000000000000"
		if tc.ExpectMatch {
			if tok, tokErr := ownership.PathConditionToken(ownTestObj, tc.Path, tc.Doc); tokErr == nil {
				expectTok = tok
			}
		}
		expRev := int64(7)
		if !tc.OwnerRevMatch {
			expRev = 6
		}
		st := tc.State.toGoState()
		if hooks.releaseAllowsForeign && tc.Op == "release" {
			if e, ok := st.Get(tc.Path); ok && e.State == ownership.StateOwned {
				st.Set(tc.Path, ownership.Entry{State: ownership.StateOwned, Owner: tc.Actor, Origin: e.Origin})
			}
		}
		in := ownership.ResolveInput{
			Object:           ownTestObj,
			Actor:            tc.Actor,
			Scope:            sc,
			CurrentDoc:       tc.Doc,
			CurrentState:     st,
			CurrentOwnerRev:  7,
			Path:             tc.Path,
			Expect:           expectTok,
			ExpectedOwnerRev: expRev,
		}
		var rr ownership.ResolveResult
		if tc.Op == "adopt" {
			rr, err = ownership.Adopt(in)
		} else {
			rr, err = ownership.Release(in)
		}
		if err != nil {
			code, mapErr := ownershipErrCode(err)
			if mapErr != nil {
				return ownershipResult{}, mapErr
			}
			res.Code = code
			return res, nil
		}
		if hooks.adoptSetsOriginCreated && tc.Op == "adopt" {
			rr.NextState.Set(tc.Path, ownership.Entry{State: ownership.StateOwned, Owner: tc.Actor, Origin: ownership.OriginCreated})
		}
		if hooks.adoptMutatesDoc && tc.Op == "adopt" {
			rr.Diagnostics = append(rr.Diagnostics, ownership.Diagnostic{Code: ownership.CodeExplicitDeletionRightWaived, Path: tc.Path})
		}
		res.OK = true
		res.State = fromGoState(rr.NextState)
		res.Diags = dedupOwnDiags(rr.Diagnostics)
		return res, nil

	case "checkUpdate":
		sc, err := ownership.NewScope(tc.ScopeRules...)
		if err != nil {
			code, mapErr := ownershipErrCode(err)
			if mapErr != nil {
				return ownershipResult{}, mapErr
			}
			res.Code = code
			return res, nil
		}
		st := tc.State.toGoState()
		if hooks.checkUpdateAllowsForeign {
			for k, e := range st.Entries {
				if e.State == ownership.StateOwned && e.Owner != tc.Actor {
					st.Entries[k] = ownership.Entry{State: ownership.StateOwned, Owner: tc.Actor, Origin: e.Origin}
				}
			}
		}
		nextSt, err := ownership.CheckUpdate(tc.Actor, sc, tc.OldDoc, st, tc.NextDoc)
		if err != nil {
			code, mapErr := ownershipErrCode(err)
			if mapErr != nil {
				return ownershipResult{}, mapErr
			}
			res.Code = code
			return res, nil
		}
		res.OK = true
		res.State = fromGoState(nextSt)
		return res, nil

	case "trie":
		st := tc.State.toGoState()
		raw, err := ownership.Encode(st, tc.Doc)
		if err != nil {
			code, mapErr := ownershipErrCode(err)
			if mapErr != nil {
				return ownershipResult{}, mapErr
			}
			res.Code = code
			return res, nil
		}
		f, err := lkal.FromJSON(raw)
		if err != nil {
			return ownershipResult{}, fmt.Errorf("FromJSON encoded trie: %w", err)
		}
		actorsVal, _ := envGetField(f.Data(), "actors")
		actorsList := actorsVal.(lkal.List)
		actors := make([]string, len(actorsList))
		for i, el := range actorsList {
			actors[i] = string(el.(lkal.String))
		}
		decoded, err := ownership.Decode(raw, tc.Doc)
		if err != nil {
			code, mapErr := ownershipErrCode(err)
			if mapErr != nil {
				return ownershipResult{}, mapErr
			}
			res.Code = code
			return res, nil
		}
		if hooks.trieEscalateCreatorMod {
			for k, e := range decoded.Entries {
				if e.State == ownership.StateOwned && e.Owner == decoded.DefaultOwner && e.Origin == ownership.OriginModified {
					decoded.Entries[k] = ownership.Entry{State: ownership.StateOwned, Owner: e.Owner, Origin: ownership.OriginCreated}
				}
			}
		}
		if hooks.trieFallbackUnowned {
			for k, e := range decoded.Entries {
				if e.State == ownership.StateUnowned {
					decoded.Entries[k] = ownership.Entry{State: ownership.StateOwned, Owner: decoded.DefaultOwner, Origin: ownership.OriginCreated}
				}
			}
		}
		res.OK = true
		res.Actors = actors
		res.State = fromGoState(decoded)
		return res, nil

	default:
		return ownershipResult{}, fmt.Errorf("unknown ownership op %q", tc.Op)
	}
}

func decodeStringSlice(raw json.RawMessage) ([]string, error) {
	var arr []json.RawMessage
	if err := decodeStrictJSON(raw, &arr); err != nil {
		return nil, err
	}
	out := make([]string, len(arr))
	for i, elem := range arr {
		s, err := decodeString(elem)
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out[i] = s
	}
	return out, nil
}

func decodeOwnPerm(raw json.RawMessage) (ownPermWire, error) {
	obj, err := decodeObject(raw, "set", "delete", "own", "resolve")
	if err != nil {
		return ownPermWire{}, err
	}
	s, err := decodeBool(obj["set"])
	if err != nil {
		return ownPermWire{}, err
	}
	d, err := decodeBool(obj["delete"])
	if err != nil {
		return ownPermWire{}, err
	}
	o, err := decodeBool(obj["own"])
	if err != nil {
		return ownPermWire{}, err
	}
	r, err := decodeBool(obj["resolve"])
	if err != nil {
		return ownPermWire{}, err
	}
	return ownPermWire{Set: s, Delete: d, Own: o, Resolve: r}, nil
}

func decodeOwnState(raw json.RawMessage) (ownStateWire, error) {
	obj, err := decodeObject(raw, "creator", "entries")
	if err != nil {
		return ownStateWire{}, err
	}
	creator, err := decodeString(obj["creator"])
	if err != nil {
		return ownStateWire{}, err
	}
	var arr []json.RawMessage
	if err := decodeStrictJSON(obj["entries"], &arr); err != nil {
		return ownStateWire{}, err
	}
	entries := make([]ownEntryWire, len(arr))
	for i, elem := range arr {
		var m map[string]json.RawMessage
		if err := decodeStrictJSON(elem, &m); err != nil {
			return ownStateWire{}, err
		}
		rawPath, hasPath := m["path"]
		rawState, hasState := m["state"]
		if !hasPath || !hasState {
			return ownStateWire{}, fmt.Errorf("entry[%d]: missing path or state", i)
		}
		p, err := decodeStringSlice(rawPath)
		if err != nil {
			return ownStateWire{}, err
		}
		stStr, err := decodeString(rawState)
		if err != nil {
			return ownStateWire{}, err
		}
		switch stStr {
		case "owned":
			if len(m) != 4 {
				return ownStateWire{}, fmt.Errorf("entry[%d]: owned entry must have exact keys path, state, owner, origin", i)
			}
			owner, err := decodeString(m["owner"])
			if err != nil {
				return ownStateWire{}, err
			}
			origin, err := decodeString(m["origin"])
			if err != nil {
				return ownStateWire{}, err
			}
			if origin != "created" && origin != "modified" {
				return ownStateWire{}, fmt.Errorf("entry[%d]: unknown origin %q", i, origin)
			}
			entries[i] = ownEntryWire{Path: p, State: stStr, Owner: owner, Origin: origin}
		case "unowned", "defaulted":
			if len(m) != 2 {
				return ownStateWire{}, fmt.Errorf("entry[%d]: %s entry must have exact keys path, state", i, stStr)
			}
			entries[i] = ownEntryWire{Path: p, State: stStr}
		default:
			return ownStateWire{}, fmt.Errorf("entry[%d]: unknown state %q", i, stStr)
		}
	}
	return ownStateWire{Creator: creator, Entries: entries}, nil
}

func decodeOwnDiags(raw json.RawMessage) ([]ownDiagWire, error) {
	var arr []json.RawMessage
	if err := decodeStrictJSON(raw, &arr); err != nil {
		return nil, err
	}
	out := make([]ownDiagWire, len(arr))
	for i, elem := range arr {
		obj, err := decodeObject(elem, "code", "path")
		if err != nil {
			return nil, err
		}
		code, err := decodeString(obj["code"])
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ownDiagCodes, code) {
			return nil, fmt.Errorf("diags[%d]: unknown code %q", i, code)
		}
		p, err := decodeStringSlice(obj["path"])
		if err != nil {
			return nil, err
		}
		out[i] = ownDiagWire{Code: code, Path: p}
	}
	return out, nil
}

func decodeOwnLeaves(raw json.RawMessage) ([]ownLeafWire, error) {
	var arr []json.RawMessage
	if err := decodeStrictJSON(raw, &arr); err != nil {
		return nil, err
	}
	out := make([]ownLeafWire, len(arr))
	for i, elem := range arr {
		obj, err := decodeObject(elem, "path", "value")
		if err != nil {
			return nil, err
		}
		p, err := decodeStringSlice(obj["path"])
		if err != nil {
			return nil, err
		}
		v, err := decodeWireValue(obj["value"])
		if err != nil {
			return nil, err
		}
		out[i] = ownLeafWire{Path: p, Value: v}
	}
	return out, nil
}

func decodeOwnershipResult(op string, line []byte) (ownershipResult, error) {
	trimmed := bytes.TrimSpace(line)
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rawTop json.RawMessage
	if err := dec.Decode(&rawTop); err != nil {
		return ownershipResult{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return ownershipResult{}, fmt.Errorf("unexpected trailing data: %v", err)
	}
	var top map[string]json.RawMessage
	if err := decodeStrictJSON(rawTop, &top); err != nil {
		return ownershipResult{}, err
	}
	rawID, hasID := top["id"]
	rawOK, hasOK := top["ok"]
	if !hasID || !hasOK {
		return ownershipResult{}, fmt.Errorf("missing id or ok: %s", rawTop)
	}
	id, err := decodeNonNegInt(rawID)
	if err != nil {
		return ownershipResult{}, err
	}
	ok, err := decodeBool(rawOK)
	if err != nil {
		return ownershipResult{}, err
	}
	if !ok {
		rawCode, hasCode := top["code"]
		if len(top) != 3 || !hasCode {
			return ownershipResult{}, fmt.Errorf("ok=false must have exact keys id, ok, code: %s", rawTop)
		}
		code, err := decodeString(rawCode)
		if err != nil {
			return ownershipResult{}, err
		}
		if !slices.Contains(ownDiagCodes, code) {
			return ownershipResult{}, fmt.Errorf("unknown code %q", code)
		}
		return ownershipResult{ID: id, Op: op, OK: false, Code: code}, nil
	}

	res := ownershipResult{ID: id, Op: op, OK: true}
	switch op {
	case "project":
		rawLeaves, has := top["leaves"]
		if len(top) != 3 || !has {
			return ownershipResult{}, fmt.Errorf("project ok=true must have exact keys id, ok, leaves: %s", rawTop)
		}
		leaves, err := decodeOwnLeaves(rawLeaves)
		if err != nil {
			return ownershipResult{}, err
		}
		res.Leaves = leaves
	case "scope":
		rawPerms, hasP := top["perms"]
		rawAllows, hasA := top["allows"]
		if len(top) != 4 || !hasP || !hasA {
			return ownershipResult{}, fmt.Errorf("scope ok=true must have exact keys id, ok, perms, allows: %s", rawTop)
		}
		perms, err := decodeOwnPerm(rawPerms)
		if err != nil {
			return ownershipResult{}, err
		}
		allows, err := decodeBool(rawAllows)
		if err != nil {
			return ownershipResult{}, err
		}
		res.Perms = perms
		res.Allows = allows
	case "planApply", "planPrune":
		if len(top) != 7 {
			return ownershipResult{}, fmt.Errorf("%s ok=true must have exact 7 keys: %s", op, rawTop)
		}
		for _, k := range []string{"blocked", "primaryCode", "doc", "state", "diags"} {
			if _, has := top[k]; !has {
				return ownershipResult{}, fmt.Errorf("%s missing key %q: %s", op, k, rawTop)
			}
		}
		blocked, err := decodeBool(top["blocked"])
		if err != nil {
			return ownershipResult{}, err
		}
		primary, err := decodeString(top["primaryCode"])
		if err != nil {
			return ownershipResult{}, err
		}
		if primary != "" && !slices.Contains(ownDiagCodes, primary) {
			return ownershipResult{}, fmt.Errorf("unknown primaryCode %q", primary)
		}
		doc, err := decodeWireRecord(top["doc"])
		if err != nil {
			return ownershipResult{}, err
		}
		st, err := decodeOwnState(top["state"])
		if err != nil {
			return ownershipResult{}, err
		}
		diags, err := decodeOwnDiags(top["diags"])
		if err != nil {
			return ownershipResult{}, err
		}
		res.Blocked = blocked
		res.PrimaryCode = primary
		res.Doc = doc
		res.State = st
		res.Diags = diags
	case "adopt", "release":
		if len(top) != 4 {
			return ownershipResult{}, fmt.Errorf("%s ok=true must have exact 4 keys: %s", op, rawTop)
		}
		rawSt, hasSt := top["state"]
		rawDiags, hasD := top["diags"]
		if !hasSt || !hasD {
			return ownershipResult{}, fmt.Errorf("%s missing state or diags: %s", op, rawTop)
		}
		st, err := decodeOwnState(rawSt)
		if err != nil {
			return ownershipResult{}, err
		}
		diags, err := decodeOwnDiags(rawDiags)
		if err != nil {
			return ownershipResult{}, err
		}
		res.State = st
		res.Diags = diags
	case "checkUpdate":
		rawSt, hasSt := top["state"]
		if len(top) != 3 || !hasSt {
			return ownershipResult{}, fmt.Errorf("checkUpdate ok=true must have exact keys id, ok, state: %s", rawTop)
		}
		st, err := decodeOwnState(rawSt)
		if err != nil {
			return ownershipResult{}, err
		}
		res.State = st
	case "trie":
		rawActors, hasA := top["actors"]
		rawSt, hasSt := top["state"]
		if len(top) != 4 || !hasA || !hasSt {
			return ownershipResult{}, fmt.Errorf("trie ok=true must have exact keys id, ok, actors, state: %s", rawTop)
		}
		actors, err := decodeStringSlice(rawActors)
		if err != nil {
			return ownershipResult{}, err
		}
		st, err := decodeOwnState(rawSt)
		if err != nil {
			return ownershipResult{}, err
		}
		res.Actors = actors
		res.State = st
	default:
		return ownershipResult{}, fmt.Errorf("unknown op %q", op)
	}
	return res, nil
}

func compareOwnState(want, got ownStateWire) string {
	if want.Creator != got.Creator {
		return fmt.Sprintf("state.creator mismatch: lean=%q go=%q", want.Creator, got.Creator)
	}
	if len(want.Entries) != len(got.Entries) {
		return fmt.Sprintf("state.entries len mismatch: lean=%d go=%d", len(want.Entries), len(got.Entries))
	}
	for i := range want.Entries {
		w, g := want.Entries[i], got.Entries[i]
		if !slices.Equal(w.Path, g.Path) {
			return fmt.Sprintf("state.entries[%d].path mismatch: lean=%v go=%v", i, w.Path, g.Path)
		}
		if w.State != g.State {
			return fmt.Sprintf("state.entries[%d].state mismatch: lean=%q go=%q", i, w.State, g.State)
		}
		if w.Owner != g.Owner {
			return fmt.Sprintf("state.entries[%d].owner mismatch: lean=%q go=%q", i, w.Owner, g.Owner)
		}
		if w.Origin != g.Origin {
			return fmt.Sprintf("state.entries[%d].origin mismatch: lean=%q go=%q", i, w.Origin, g.Origin)
		}
	}
	return ""
}

func compareOwnDiags(want, got []ownDiagWire) string {
	if len(want) != len(got) {
		return fmt.Sprintf("diags len mismatch: lean=%v go=%v", want, got)
	}
	for i := range want {
		if want[i].Code != got[i].Code || !slices.Equal(want[i].Path, got[i].Path) {
			return fmt.Sprintf("diags[%d] mismatch: lean=%+v go=%+v", i, want[i], got[i])
		}
	}
	return ""
}

func compareOwnershipResult(want, got ownershipResult) string {
	switch {
	case want.ID != got.ID:
		return fmt.Sprintf("id mismatch: lean=%d go=%d", want.ID, got.ID)
	case want.Op != got.Op:
		return fmt.Sprintf("op mismatch: lean=%q go=%q", want.Op, got.Op)
	case want.OK != got.OK:
		return fmt.Sprintf("ok mismatch: lean=%v (code=%q) go=%v (code=%q)", want.OK, want.Code, got.OK, got.Code)
	case want.Code != got.Code:
		return fmt.Sprintf("code mismatch: lean=%q go=%q", want.Code, got.Code)
	}
	if !want.OK {
		return ""
	}
	switch want.Op {
	case "project":
		if len(want.Leaves) != len(got.Leaves) {
			return fmt.Sprintf("leaves len mismatch: lean=%d go=%d", len(want.Leaves), len(got.Leaves))
		}
		for i := range want.Leaves {
			if !slices.Equal(want.Leaves[i].Path, got.Leaves[i].Path) {
				return fmt.Sprintf("leaves[%d].path mismatch: lean=%v go=%v", i, want.Leaves[i].Path, got.Leaves[i].Path)
			}
			if !lkal.Equal(want.Leaves[i].Value, got.Leaves[i].Value) {
				return fmt.Sprintf("leaves[%d].value mismatch: lean=%s go=%s", i, formatWireValue(want.Leaves[i].Value), formatWireValue(got.Leaves[i].Value))
			}
		}
	case "scope":
		if want.Perms != got.Perms {
			return fmt.Sprintf("perms mismatch: lean=%+v go=%+v", want.Perms, got.Perms)
		}
		if want.Allows != got.Allows {
			return fmt.Sprintf("allows mismatch: lean=%v go=%v", want.Allows, got.Allows)
		}
	case "planApply", "planPrune":
		if want.Blocked != got.Blocked {
			return fmt.Sprintf("blocked mismatch: lean=%v go=%v", want.Blocked, got.Blocked)
		}
		if want.PrimaryCode != got.PrimaryCode {
			return fmt.Sprintf("primaryCode mismatch: lean=%q go=%q", want.PrimaryCode, got.PrimaryCode)
		}
		if !lkal.Equal(want.Doc, got.Doc) {
			return fmt.Sprintf("doc mismatch:\nlean: %s\ngo:   %s", formatWireDoc(want.Doc), formatWireDoc(got.Doc))
		}
		if d := compareOwnState(want.State, got.State); d != "" {
			return d
		}
		if d := compareOwnDiags(want.Diags, got.Diags); d != "" {
			return d
		}
	case "adopt", "release":
		if d := compareOwnState(want.State, got.State); d != "" {
			return d
		}
		if d := compareOwnDiags(want.Diags, got.Diags); d != "" {
			return d
		}
	case "checkUpdate":
		if d := compareOwnState(want.State, got.State); d != "" {
			return d
		}
	case "trie":
		if !slices.Equal(want.Actors, got.Actors) {
			return fmt.Sprintf("actors mismatch: lean=%v go=%v", want.Actors, got.Actors)
		}
		if d := compareOwnState(want.State, got.State); d != "" {
			return d
		}
	}
	return ""
}

func ownershipBucketOf(tc ownershipCase, res ownershipResult) string {
	switch tc.Op {
	case "scope":
		if !res.OK {
			return "scope/" + res.Code
		}
		if res.Allows {
			return "scope/ok-allow"
		}
		return "scope/ok-deny"
	case "planApply", "planPrune":
		if !res.OK {
			return tc.Op + "/err-" + res.Code
		}
		if res.Blocked {
			return tc.Op + "/blocked-" + res.PrimaryCode
		}
		return tc.Op + "/ok"
	default:
		if !res.OK {
			return tc.Op + "/" + res.Code
		}
		return tc.Op + "/ok"
	}
}

func leanOwnershipResults(t *testing.T, cases []ownershipCase) ([][]byte, [][]byte, []ownershipResult) {
	t.Helper()
	encoded := encodeOwnershipCases(t, cases)
	lines, err := RunModel("ownership", encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(cases) {
		t.Fatalf("RunModel returned %d lines, want %d", len(lines), len(cases))
	}
	results := make([]ownershipResult, len(lines))
	for i, line := range lines {
		res, err := decodeOwnershipResult(cases[i].Op, line)
		if err != nil {
			t.Fatalf("id=%d (%s) decode error: %v\ncase: %s\nlean: %s", cases[i].ID, cases[i].Op, err, encoded[i], line)
		}
		results[i] = res
	}
	return encoded, lines, results
}

func TestOwnershipMatchesLeanModel(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	cases := generateOwnershipCases(cfg.Seed, cfg.Cases)
	encoded, leanLines, leanResults := leanOwnershipResults(t, cases)

	repro := fmt.Sprintf("NAYUTAR_DRT_SEED=%d NAYUTAR_DRT_CASES=%d go test -tags drt -run '^TestOwnershipMatchesLeanModel$' ./core/drt/...", cfg.Seed, cfg.Cases)
	buckets := map[string]int{}
	for i, tc := range cases {
		goRes, err := runOwnershipCase(tc, ownershipHooks{})
		if err != nil {
			t.Fatalf("seed=%d id=%d (%s) go error: %v\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, tc.Op, err, encoded[i], leanLines[i], repro)
		}
		if diff := compareOwnershipResult(leanResults[i], goRes); diff != "" {
			t.Fatalf("seed=%d id=%d (%s) mismatch (%s)\ncase: %s\nlean: %s\nreproduce: %s",
				cfg.Seed, tc.ID, tc.Op, diff, encoded[i], leanLines[i], repro)
		}
		buckets[ownershipBucketOf(tc, goRes)]++
	}

	if cfg.Cases >= 100 {
		var missing []string
		for _, b := range ownRequiredBuckets {
			if buckets[b] == 0 {
				missing = append(missing, b)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed=%d empty outcome buckets: %s", cfg.Seed, strings.Join(missing, ", "))
		}
	}
}

func TestOwnershipCasesReachEveryOutcome(t *testing.T) {
	cases := generateOwnershipCases(1, 1000)
	_, _, results := leanOwnershipResults(t, cases)

	seen := map[string]int{}
	for i, tc := range cases {
		res := results[i]
		seen[ownershipBucketOf(tc, res)]++
		if res.OK && tc.Op == "planApply" && !res.Blocked {
			for _, d := range res.Diags {
				if d.Code == "K603" {
					seen["planApply/notice-K603"]++
				}
				if d.Code == "K604" {
					seen["planApply/notice-K604"]++
				}
			}
		}
		if res.OK && (tc.Op == "adopt" || tc.Op == "release") {
			for _, d := range res.Diags {
				if d.Code == "K602" {
					seen[tc.Op+"/notice-K602"]++
				}
			}
		}
		if res.OK && tc.Op == "trie" {
			if len(res.Actors) == 1 {
				seen["trie/single-actor"]++
			} else {
				seen["trie/multi-actor"]++
			}
		}
	}

	expected := append(slices.Clone(ownRequiredBuckets),
		"planApply/notice-K603",
		"planApply/notice-K604",
		"adopt/notice-K602",
		"release/notice-K602",
		"trie/single-actor",
		"trie/multi-actor",
	)
	var missing []string
	for _, b := range expected {
		if seen[b] == 0 {
			missing = append(missing, b)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("missing outcomes: %s", strings.Join(missing, ", "))
	}
}

func cloneOwnershipResult(r ownershipResult) ownershipResult {
	out := ownershipResult{
		ID:          r.ID,
		Op:          r.Op,
		OK:          r.OK,
		Code:        r.Code,
		Perms:       r.Perms,
		Allows:      r.Allows,
		Blocked:     r.Blocked,
		PrimaryCode: r.PrimaryCode,
		Doc:         envCloneRecord(r.Doc),
		State: ownStateWire{
			Creator: r.State.Creator,
			Entries: make([]ownEntryWire, len(r.State.Entries)),
		},
		Actors: slices.Clone(r.Actors),
	}
	for i, e := range r.State.Entries {
		out.State.Entries[i] = ownEntryWire{
			Path:   slices.Clone(e.Path),
			State:  e.State,
			Owner:  e.Owner,
			Origin: e.Origin,
		}
	}
	if r.Leaves != nil {
		out.Leaves = make([]ownLeafWire, len(r.Leaves))
		for i, lf := range r.Leaves {
			out.Leaves[i] = ownLeafWire{Path: slices.Clone(lf.Path), Value: envCloneValue(lf.Value)}
		}
	}
	if r.Diags != nil {
		out.Diags = make([]ownDiagWire, len(r.Diags))
		for i, d := range r.Diags {
			out.Diags[i] = ownDiagWire{Code: d.Code, Path: slices.Clone(d.Path)}
		}
	}
	return out
}

func TestOwnershipComparisonDetectsEveryFieldMutation(t *testing.T) {
	cases := generateOwnershipCases(1, 300)
	_, _, results := leanOwnershipResults(t, cases)

	applied := map[string]int{}
	assertDetects := func(kind string, orig ownershipResult, mutate func(r *ownershipResult)) {
		t.Helper()
		mut := cloneOwnershipResult(orig)
		mutate(&mut)
		applied[kind]++
		if compareOwnershipResult(orig, mut) == "" {
			t.Fatalf("mutation %q on case %d (%s) was not detected", kind, orig.ID, orig.Op)
		}
	}

	for _, orig := range results {
		assertDetects("id+1", orig, func(r *ownershipResult) { r.ID++ })
		assertDetects("op-changed", orig, func(r *ownershipResult) { r.Op += "x" })
		assertDetects("flip-ok", orig, func(r *ownershipResult) { r.OK = !r.OK })
		if !orig.OK {
			assertDetects("code-changed", orig, func(r *ownershipResult) { r.Code = "K502" })
			continue
		}
		switch orig.Op {
		case "project":
			assertDetects("leaves-len", orig, func(r *ownershipResult) {
				r.Leaves = append(r.Leaves, ownLeafWire{Path: []string{"spec", "z"}, Value: lkal.Int(1)})
			})
			if len(orig.Leaves) > 0 {
				assertDetects("leaves-path", orig, func(r *ownershipResult) { r.Leaves[0].Path = append(r.Leaves[0].Path, "x") })
				assertDetects("leaves-value", orig, func(r *ownershipResult) { r.Leaves[0].Value = lkal.String("__mut__") })
			}
		case "scope":
			assertDetects("perms-resolve", orig, func(r *ownershipResult) { r.Perms.Resolve = !r.Perms.Resolve })
			assertDetects("allows-flip", orig, func(r *ownershipResult) { r.Allows = !r.Allows })
		case "planApply", "planPrune":
			assertDetects("blocked-flip", orig, func(r *ownershipResult) { r.Blocked = !r.Blocked })
			assertDetects("primaryCode-changed", orig, func(r *ownershipResult) { r.PrimaryCode += "x" })
			assertDetects("doc-changed", orig, func(r *ownershipResult) { r.Doc = envSetField(r.Doc, "mut", lkal.Bool(true)) })
			assertDetects("state-creator", orig, func(r *ownershipResult) { r.State.Creator += "x" })
			if len(orig.State.Entries) > 0 {
				assertDetects("state-entry", orig, func(r *ownershipResult) { r.State.Entries[0].State = "mutated" })
			}
			assertDetects("diags-len", orig, func(r *ownershipResult) {
				r.Diags = append(r.Diags, ownDiagWire{Code: "K502", Path: []string{"spec", "x"}})
			})
		case "adopt", "release":
			assertDetects("resolve-state", orig, func(r *ownershipResult) { r.State.Creator += "x" })
			assertDetects("resolve-diags", orig, func(r *ownershipResult) {
				r.Diags = append(r.Diags, ownDiagWire{Code: "K602", Path: []string{"spec", "x"}})
			})
		case "checkUpdate":
			assertDetects("check-state", orig, func(r *ownershipResult) { r.State.Creator += "x" })
		case "trie":
			assertDetects("trie-actors", orig, func(r *ownershipResult) { r.Actors = append(r.Actors, "user:extra") })
			assertDetects("trie-state", orig, func(r *ownershipResult) { r.State.Creator += "x" })
		}
	}

	for _, kind := range []string{
		"id+1", "op-changed", "flip-ok", "code-changed",
		"leaves-len", "leaves-path", "leaves-value",
		"perms-resolve", "allows-flip",
		"blocked-flip", "primaryCode-changed", "doc-changed", "state-creator", "state-entry", "diags-len",
		"resolve-state", "resolve-diags", "check-state", "trie-actors", "trie-state",
	} {
		if applied[kind] == 0 {
			t.Errorf("mutation %q was never applicable across %d cases", kind, len(cases))
		}
	}

	invalidLines := []struct {
		name string
		op   string
		line string
	}{
		{"missing id", "project", `{"ok":false,"code":"K101"}`},
		{"unknown code", "project", `{"id":0,"ok":false,"code":"K999"}`},
		{"extra field on err", "project", `{"id":0,"ok":false,"code":"K101","extra":1}`},
		{"scope missing allows", "scope", `{"id":0,"ok":true,"perms":{"set":false,"delete":false,"own":false,"resolve":false}}`},
		{"planApply unknown primaryCode", "planApply", `{"id":0,"ok":true,"blocked":true,"primaryCode":"K999","doc":{"o":[]},"state":{"creator":"u","entries":[]},"diags":[]}`},
		{"trie missing actors", "trie", `{"id":0,"ok":true,"state":{"creator":"u","entries":[]}}`},
		{"owned entry missing origin", "checkUpdate", `{"id":0,"ok":true,"state":{"creator":"u","entries":[{"path":["spec","a"],"state":"owned","owner":"u"}]}}`},
		{"trailing object", "project", `{"id":0,"ok":true,"leaves":[]}{"id":1,"ok":true,"leaves":[]}`},
	}
	for _, tc := range invalidLines {
		if _, err := decodeOwnershipResult(tc.op, []byte(tc.line)); err == nil {
			t.Errorf("decodeOwnershipResult(%s) accepted %s: %s", tc.op, tc.name, tc.line)
		}
	}
}

func TestOwnershipDRTCatchesBrokenImplementations(t *testing.T) {
	cases := generateOwnershipCases(1, 500)
	_, _, leanResults := leanOwnershipResults(t, cases)

	variants := []struct {
		name  string
		hooks ownershipHooks
	}{
		{"project allows nested empty record", ownershipHooks{projectAllowNestedEmpty: true}},
		{"scope implies Resolve from Regular", ownershipHooks{scopeResolveImplied: true}},
		{"scope uses raw string prefix match", ownershipHooks{scopeRawStringPrefix: true}},
		{"apply allows foreign overwrite", ownershipHooks{applyAllowForeignOverwrite: true}},
		{"apply deletes retired created leaves", ownershipHooks{applyDeleteRetiredCreated: true}},
		{"apply treats defaulted leaf as K206", ownershipHooks{applyDefaultedHitsK206: true}},
		{"apply bypasses all-or-nothing gate", ownershipHooks{applySkipAllOrNothing: true}},
		{"prune retains emptied intermediate record", ownershipHooks{pruneKeepEmptyIntermediate: true}},
		{"prune removes retired modified entry", ownershipHooks{pruneDeleteRetiredModified: true}},
		{"adopt sets OriginCreated instead of Modified", ownershipHooks{adoptSetsOriginCreated: true}},
		{"adopt skips Resolve permission check", ownershipHooks{adoptSkipsResolvePerm: true}},
		{"adopt emits spurious notice", ownershipHooks{adoptMutatesDoc: true}},
		{"release allows foreign actor", ownershipHooks{releaseAllowsForeign: true}},
		{"checkUpdate allows foreign overwrite", ownershipHooks{checkUpdateAllowsForeign: true}},
		{"trie escalates creator Modified to Created", ownershipHooks{trieEscalateCreatorMod: true}},
		{"trie falls back unowned leaf to creator", ownershipHooks{trieFallbackUnowned: true}},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			mismatches := 0
			for i, tc := range cases {
				goRes, err := runOwnershipCase(tc, v.hooks)
				if err != nil {
					t.Fatalf("id=%d (%s): broken implementation errored: %v", tc.ID, tc.Op, err)
				}
				if compareOwnershipResult(leanResults[i], goRes) != "" {
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

func TestGenerateOwnershipCasesIsDeterministic(t *testing.T) {
	a := bytes.Join(encodeOwnershipCases(t, generateOwnershipCases(1, 100)), []byte("\n"))
	b := bytes.Join(encodeOwnershipCases(t, generateOwnershipCases(1, 100)), []byte("\n"))
	c := bytes.Join(encodeOwnershipCases(t, generateOwnershipCases(2, 100)), []byte("\n"))

	if !bytes.Equal(a, b) {
		t.Fatal("same seed produced different encoded cases")
	}
	if bytes.Equal(a, c) {
		t.Fatal("different seeds produced identical encoded cases")
	}
}

func TestOwnershipModelRejectsMalformedCase(t *testing.T) {
	valid := `{"id":0,"op":"project","doc":{"o":[]},"rejectNestedEmpty":true}`
	for _, bad := range []string{
		`{"id":1,"op":"explode"}`,
		`{"id":1,"op":"project","doc":{"s":"not-a-record"},"rejectNestedEmpty":true}`,
		`{"id":1,"op":"scope","scope":[{"match":"bogus","prefix":[],"perms":{"set":true,"delete":false,"own":false,"resolve":false}}],"path":["spec","a"],"req":{"set":true,"delete":false,"own":false,"resolve":false}}`,
		`{"id":1,"op":"trie","doc":{"o":[]},"state":{"creator":"u","entries":[{"path":["spec","a"],"state":"bogus"}]}}`,
		`{"id":1,"op":"adopt","doc":{"o":[]}}`,
	} {
		cases := [][]byte{[]byte(valid), []byte(bad)}
		_, err := RunModel("ownership", cases)
		if err == nil {
			t.Errorf("RunModel accepted malformed case %s", bad)
			continue
		}
		if !bytes.Contains([]byte(err.Error()), []byte("line 2")) {
			t.Errorf("error %q does not name line 2 for %s", err.Error(), bad)
		}
	}
}
