package keyspace_test

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

const (
	sampleHash  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sampleHash2 = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func maxLabel(ch byte) string {
	return strings.Repeat(string(ch), 63)
}

func maxSubdomain() string {
	return maxLabel('a') + "." + maxLabel('b') + "." + maxLabel('c') + "." + strings.Repeat("d", 61)
}

func TestValidators(t *testing.T) {
	labelCases := []struct {
		in    string
		valid bool
	}{
		{"a", true},
		{"0", true},
		{"a1", true},
		{"1a", true},
		{"a-b", true},
		{"a--1", true},
		{maxLabel('z'), true},
		{"", false},
		{strings.Repeat("a", 64), false},
		{"-", false},
		{"-a", false},
		{"a-", false},
		{"A", false},
		{"aB", false},
		{"a_b", false},
		{".", false},
		{"..", false},
		{"a.b", false},
		{".a", false},
		{"a.", false},
		{"a/b", false},
		{"a%20b", false},
		{"a b", false},
		{"a\x00b", false},
		{"a\x7fb", false},
		{"é", false},
		{"α", false},
		{"a\xffb", false},
	}

	for _, tc := range labelCases {
		for _, v := range []struct {
			name string
			fn   func(string) error
		}{
			{"ValidateTenant", keyspace.ValidateTenant},
			{"ValidateKind", keyspace.ValidateKind},
			{"ValidateVersion", keyspace.ValidateVersion},
		} {
			err := v.fn(tc.in)
			if tc.valid && err != nil {
				t.Errorf("%s(%q) = %v, want nil", v.name, tc.in, err)
			}
			if !tc.valid && !errors.Is(err, keyspace.ErrInvalid) {
				t.Errorf("%s(%q) = %v, want ErrInvalid", v.name, tc.in, err)
			}
		}
	}

	subdomain64Elem := strings.Repeat("a", 64) + ".b"
	subdomain63Elem := maxLabel('a') + ".b"
	subdomain253 := maxSubdomain()
	subdomain254 := subdomain253 + "x"
	dotFree253 := strings.Repeat("a", 253)
	dotFree254 := strings.Repeat("a", 254)
	if len(subdomain253) != 253 {
		t.Fatalf("subdomain253 len = %d, want 253", len(subdomain253))
	}

	nameCases := []struct {
		in    string
		valid bool
	}{
		{"a", true},
		{"a-b.c-d", true},
		{"1.2.3", true},
		{subdomain63Elem, true},
		{subdomain64Elem, true},
		{subdomain253, true},
		{dotFree253, true},
		{"", false},
		{subdomain254, false},
		{dotFree254, false},
		{".", false},
		{"..", false},
		{".a", false},
		{"a.", false},
		{"a..b", false},
		{"-a.b", false},
		{"a-.b", false},
		{"a.-b", false},
		{"a.b-", false},
		{"A.b", false},
		{"a_b", false},
		{"a/b", false},
		{"a%2Fb", false},
		{"a b", false},
		{"a\x00b", false},
		{"a\x7fb", false},
		{"é", false},
		{"α", false},
		{"\xff", false},
	}
	for _, tc := range nameCases {
		for _, v := range []struct {
			name  string
			field string
			fn    func(string) error
		}{
			{"ValidateName", "name", keyspace.ValidateName},
			{"ValidateNode", "node", keyspace.ValidateNode},
		} {
			err := v.fn(tc.in)
			if tc.valid && err != nil {
				t.Errorf("%s(%q) = %v, want nil", v.name, tc.in, err)
			}
			if !tc.valid {
				if !errors.Is(err, keyspace.ErrInvalid) {
					t.Errorf("%s(%q) = %v, want ErrInvalid", v.name, tc.in, err)
				}
				if err != nil && !strings.Contains(err.Error(), v.field) {
					t.Errorf("%s(%q) error = %q, want field name %q", v.name, tc.in, err.Error(), v.field)
				}
			}
		}
	}

	groupCases := []struct {
		in    string
		valid bool
	}{
		{keyspace.CoreGroup, true},
		{"apps.example.com", true},
		{"core.example.com", true},
		{"a.b", true},
		{subdomain63Elem, true},
		{subdomain64Elem, true},
		{strings.Repeat("a", 100) + ".example.com", true},
		{subdomain253, true},
		{"", false},
		{"apps", false},
		{"a", false},
		{"Core", false},
		{"core.", false},
		{".core", false},
		{"a..b", false},
		{subdomain254, false},
		{dotFree253, false},
		{"a_b.c", false},
		{"a/b.c", false},
		{"a%20.b", false},
		{"a .b", false},
		{"a\x00.b", false},
		{"a\x7f.b", false},
		{"é.b", false},
		{"α.b", false},
		{"a\xff.b", false},
	}
	for _, tc := range groupCases {
		err := keyspace.ValidateGroup(tc.in)
		if tc.valid && err != nil {
			t.Errorf("ValidateGroup(%q) = %v, want nil", tc.in, err)
		}
		if !tc.valid && !errors.Is(err, keyspace.ErrInvalid) {
			t.Errorf("ValidateGroup(%q) = %v, want ErrInvalid", tc.in, err)
		}
	}

	hashCases := []struct {
		in    string
		valid bool
	}{
		{sampleHash, true},
		{strings.Repeat("0", 64), true},
		{strings.Repeat("f", 64), true},
		{"", false},
		{sampleHash[:63], false},
		{sampleHash + "0", false},
		{strings.ToUpper(sampleHash), false},
		{strings.Repeat("g", 64), false},
		{sampleHash[:63] + "A", false},
		{sampleHash[:63] + "-", false},
		{sampleHash[:63] + "_", false},
		{sampleHash[:63] + "/", false},
		{sampleHash[:63] + "\x00", false},
		{sampleHash[:63] + "\xff", false},
		{strings.Repeat("α", 32), false},
	}
	for _, tc := range hashCases {
		err := keyspace.ValidateIdentityHash(tc.in)
		if tc.valid && err != nil {
			t.Errorf("ValidateIdentityHash(%q) = %v, want nil", tc.in, err)
		}
		if !tc.valid && !errors.Is(err, keyspace.ErrInvalid) {
			t.Errorf("ValidateIdentityHash(%q) = %v, want ErrInvalid", tc.in, err)
		}
	}
}

func TestQualifiedKind(t *testing.T) {
	validCases := []struct {
		group string
		kind  string
		want  string
	}{
		{keyspace.CoreGroup, "secret", "secret"},
		{keyspace.CoreGroup, "core", "core"},
		{"widgets.example.com", "widget", "widget.widgets.example.com"},
		{"core.example.com", "widget", "widget.core.example.com"},
		{strings.Repeat("g", 100) + ".example.com", "widget", "widget." + strings.Repeat("g", 100) + ".example.com"},
	}
	for _, tc := range validCases {
		got, err := keyspace.QualifiedKind(tc.group, tc.kind)
		if err != nil {
			t.Fatalf("QualifiedKind(%q, %q) error = %v", tc.group, tc.kind, err)
		}
		if got != tc.want {
			t.Errorf("QualifiedKind(%q, %q) = %q, want %q", tc.group, tc.kind, got, tc.want)
		}
		g, k, err := keyspace.ParseQualifiedKind(got)
		if err != nil {
			t.Fatalf("ParseQualifiedKind(%q) error = %v", got, err)
		}
		if g != tc.group || k != tc.kind {
			t.Errorf("ParseQualifiedKind(%q) = (%q, %q), want (%q, %q)", got, g, k, tc.group, tc.kind)
		}
	}

	invalidParse := []string{
		"",
		"widget.apps",
		"x.core",
		".a.b",
		"a.",
		"a.b.",
		"a..b",
		"Widget.a.b",
		"widget.a_b.c",
		"widget.a/b.c",
		maxLabel('a') + "a.b.c",
		strings.Repeat("k", 64) + ".widgets.example.com",
	}
	for _, s := range invalidParse {
		if _, _, err := keyspace.ParseQualifiedKind(s); !errors.Is(err, keyspace.ErrInvalid) {
			t.Errorf("ParseQualifiedKind(%q) error = %v, want ErrInvalid", s, err)
		}
	}

	if _, err := keyspace.QualifiedKind("apps", "widget"); !errors.Is(err, keyspace.ErrInvalid) {
		t.Errorf("QualifiedKind invalid group error = %v, want ErrInvalid", err)
	}
	if _, err := keyspace.QualifiedKind(keyspace.CoreGroup, "a.b"); !errors.Is(err, keyspace.ErrInvalid) {
		t.Errorf("QualifiedKind invalid kind error = %v, want ErrInvalid", err)
	}
	if _, err := keyspace.QualifiedKind("widgets.example.com", strings.Repeat("k", 64)); !errors.Is(err, keyspace.ErrInvalid) {
		t.Errorf("QualifiedKind 64-byte kind error = %v, want ErrInvalid", err)
	}
}

func TestEncodeAndParseCanonical(t *testing.T) {
	cases := []struct {
		name    string
		key     keyspace.Key
		wantAbs string
		wantRel string
		hasRel  bool
	}{
		{
			name: "object core",
			key: keyspace.Key{
				Family: keyspace.FamilyObject,
				Tenant: "acme",
				Object: keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "web"},
			},
			wantAbs: "/t/acme/o/core/workload/web",
			wantRel: "o/core/workload/web",
			hasRel:  true,
		},
		{
			name: "object custom group dotted name",
			key: keyspace.Key{
				Family: keyspace.FamilyObject,
				Tenant: "acme",
				Object: keyspace.Object{Group: "widgets.example.com", Kind: "widget", Name: "w1.prod"},
			},
			wantAbs: "/t/acme/o/widgets.example.com/widget/w1.prod",
			wantRel: "o/widgets.example.com/widget/w1.prod",
			hasRel:  true,
		},
		{
			name: "ownership",
			key: keyspace.Key{
				Family: keyspace.FamilyOwnership,
				Tenant: "acme",
				Object: keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "web"},
			},
			wantAbs: "/t/acme/own/core/workload/web",
			wantRel: "own/core/workload/web",
			hasRel:  true,
		},
		{
			name: "backref core target non-core source",
			key: keyspace.Key{
				Family: keyspace.FamilyBackRef,
				Tenant: "acme",
				Object: keyspace.Object{Group: keyspace.CoreGroup, Kind: "secret", Name: "db-creds"},
				Source: keyspace.Object{Group: "widgets.example.com", Kind: "widget", Name: "w1"},
			},
			wantAbs: "/t/acme/ref/secret/db-creds/widget.widgets.example.com/w1",
			wantRel: "ref/secret/db-creds/widget.widgets.example.com/w1",
			hasRel:  true,
		},
		{
			name: "backref non-core target core source",
			key: keyspace.Key{
				Family: keyspace.FamilyBackRef,
				Tenant: "t-1",
				Object: keyspace.Object{Group: "apps.example.com", Kind: "app", Name: "api.v1"},
				Source: keyspace.Object{Group: keyspace.CoreGroup, Kind: "config", Name: "cfg"},
			},
			wantAbs: "/t/t-1/ref/app.apps.example.com/api.v1/config/cfg",
			wantRel: "ref/app.apps.example.com/api.v1/config/cfg",
			hasRel:  true,
		},
		{
			name: "binding",
			key: keyspace.Key{
				Family:       keyspace.FamilyBinding,
				Tenant:       "acme",
				IdentityHash: sampleHash,
			},
			wantAbs: "/t/acme/bind/" + sampleHash,
			wantRel: "bind/" + sampleHash,
			hasRel:  true,
		},
		{
			name: "schema",
			key: keyspace.Key{
				Family:       keyspace.FamilySchema,
				IdentityHash: sampleHash,
				Version:      "v1alpha1",
			},
			wantAbs: "/x/" + sampleHash + "/schema/v1alpha1",
			hasRel:  false,
		},
		{
			name: "tenant",
			key: keyspace.Key{
				Family: keyspace.FamilyTenant,
				Tenant: "acme",
			},
			wantAbs: "/sys/tenants/acme",
			hasRel:  false,
		},
		{
			name: "node",
			key: keyspace.Key{
				Family: keyspace.FamilyNode,
				Node:   "node-1.rack-a",
			},
			wantAbs: "/sys/nodes/node-1.rack-a",
			hasRel:  false,
		},
		{
			name: "eviction budget",
			key: keyspace.Key{
				Family: keyspace.FamilyEvictionBudget,
			},
			wantAbs: "/sys/liveness/evictions",
			hasRel:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAbs, err := tc.key.Encode()
			if err != nil {
				t.Fatalf("Encode() error = %v", err)
			}
			if gotAbs != tc.wantAbs {
				t.Errorf("Encode() = %q, want %q", gotAbs, tc.wantAbs)
			}
			parsed, err := keyspace.Parse(gotAbs)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", gotAbs, err)
			}
			if parsed != tc.key {
				t.Errorf("Parse(%q) = %+v, want %+v", gotAbs, parsed, tc.key)
			}

			gotRel, relErr := tc.key.Rel()
			if tc.hasRel {
				if relErr != nil {
					t.Fatalf("Rel() error = %v", relErr)
				}
				if gotRel != tc.wantRel {
					t.Errorf("Rel() = %q, want %q", gotRel, tc.wantRel)
				}
				parsedRel, err := keyspace.ParseRel(tc.key.Tenant, gotRel)
				if err != nil {
					t.Fatalf("ParseRel(%q, %q) error = %v", tc.key.Tenant, gotRel, err)
				}
				if parsedRel != tc.key {
					t.Errorf("ParseRel(%q, %q) = %+v, want %+v", tc.key.Tenant, gotRel, parsedRel, tc.key)
				}
			} else if !errors.Is(relErr, keyspace.ErrInvalid) {
				t.Errorf("Rel() error = %v, want ErrInvalid", relErr)
			}
		})
	}
}

func TestEncodeStrictUnusedFieldsAndInvalidTenantRel(t *testing.T) {
	invalidKeys := []struct {
		name string
		key  keyspace.Key
	}{
		{"zero family", keyspace.Key{}},
		{"unknown family", keyspace.Key{Family: 99, Tenant: "acme"}},
		{"object with source", keyspace.Key{
			Family: keyspace.FamilyObject,
			Tenant: "acme",
			Object: keyspace.Object{Group: "core", Kind: "pod", Name: "p1"},
			Source: keyspace.Object{Group: "core", Kind: "pod", Name: "p2"},
		}},
		{"object with hash", keyspace.Key{
			Family:       keyspace.FamilyObject,
			Tenant:       "acme",
			Object:       keyspace.Object{Group: "core", Kind: "pod", Name: "p1"},
			IdentityHash: sampleHash,
		}},
		{"object with version", keyspace.Key{
			Family:  keyspace.FamilyObject,
			Tenant:  "acme",
			Object:  keyspace.Object{Group: "core", Kind: "pod", Name: "p1"},
			Version: "v1",
		}},
		{"object with node", keyspace.Key{
			Family: keyspace.FamilyObject,
			Tenant: "acme",
			Object: keyspace.Object{Group: "core", Kind: "pod", Name: "p1"},
			Node:   "n1",
		}},
		{"ownership with source", keyspace.Key{
			Family: keyspace.FamilyOwnership,
			Tenant: "acme",
			Object: keyspace.Object{Group: "core", Kind: "pod", Name: "p1"},
			Source: keyspace.Object{Name: "x"},
		}},
		{"ownership with node", keyspace.Key{
			Family: keyspace.FamilyOwnership,
			Tenant: "acme",
			Object: keyspace.Object{Group: "core", Kind: "pod", Name: "p1"},
			Node:   "n1",
		}},
		{"backref with hash", keyspace.Key{
			Family:       keyspace.FamilyBackRef,
			Tenant:       "acme",
			Object:       keyspace.Object{Group: "core", Kind: "secret", Name: "s1"},
			Source:       keyspace.Object{Group: "core", Kind: "pod", Name: "p1"},
			IdentityHash: sampleHash,
		}},
		{"backref with version", keyspace.Key{
			Family:  keyspace.FamilyBackRef,
			Tenant:  "acme",
			Object:  keyspace.Object{Group: "core", Kind: "secret", Name: "s1"},
			Source:  keyspace.Object{Group: "core", Kind: "pod", Name: "p1"},
			Version: "v1",
		}},
		{"backref with node", keyspace.Key{
			Family: keyspace.FamilyBackRef,
			Tenant: "acme",
			Object: keyspace.Object{Group: "core", Kind: "secret", Name: "s1"},
			Source: keyspace.Object{Group: "core", Kind: "pod", Name: "p1"},
			Node:   "n1",
		}},
		{"binding with object", keyspace.Key{
			Family:       keyspace.FamilyBinding,
			Tenant:       "acme",
			Object:       keyspace.Object{Group: "core"},
			IdentityHash: sampleHash,
		}},
		{"binding with source", keyspace.Key{
			Family:       keyspace.FamilyBinding,
			Tenant:       "acme",
			Source:       keyspace.Object{Kind: "x"},
			IdentityHash: sampleHash,
		}},
		{"binding with version", keyspace.Key{
			Family:       keyspace.FamilyBinding,
			Tenant:       "acme",
			IdentityHash: sampleHash,
			Version:      "v1",
		}},
		{"binding with node", keyspace.Key{
			Family:       keyspace.FamilyBinding,
			Tenant:       "acme",
			IdentityHash: sampleHash,
			Node:         "n1",
		}},
		{"schema with tenant", keyspace.Key{
			Family:       keyspace.FamilySchema,
			Tenant:       "acme",
			IdentityHash: sampleHash,
			Version:      "v1",
		}},
		{"schema with object", keyspace.Key{
			Family:       keyspace.FamilySchema,
			Object:       keyspace.Object{Name: "n"},
			IdentityHash: sampleHash,
			Version:      "v1",
		}},
		{"schema with source", keyspace.Key{
			Family:       keyspace.FamilySchema,
			Source:       keyspace.Object{Name: "n"},
			IdentityHash: sampleHash,
			Version:      "v1",
		}},
		{"schema with node", keyspace.Key{
			Family:       keyspace.FamilySchema,
			IdentityHash: sampleHash,
			Version:      "v1",
			Node:         "n1",
		}},
		{"tenant with object", keyspace.Key{
			Family: keyspace.FamilyTenant,
			Tenant: "acme",
			Object: keyspace.Object{Name: "n"},
		}},
		{"tenant with source", keyspace.Key{
			Family: keyspace.FamilyTenant,
			Tenant: "acme",
			Source: keyspace.Object{Name: "n"},
		}},
		{"tenant with hash", keyspace.Key{
			Family:       keyspace.FamilyTenant,
			Tenant:       "acme",
			IdentityHash: sampleHash,
		}},
		{"tenant with version", keyspace.Key{
			Family:  keyspace.FamilyTenant,
			Tenant:  "acme",
			Version: "v1",
		}},
		{"tenant with node", keyspace.Key{
			Family: keyspace.FamilyTenant,
			Tenant: "acme",
			Node:   "n1",
		}},
		{"node with tenant", keyspace.Key{
			Family: keyspace.FamilyNode,
			Tenant: "acme",
			Node:   "n1",
		}},
		{"node with object", keyspace.Key{
			Family: keyspace.FamilyNode,
			Object: keyspace.Object{Name: "n"},
			Node:   "n1",
		}},
		{"node with source", keyspace.Key{
			Family: keyspace.FamilyNode,
			Source: keyspace.Object{Name: "n"},
			Node:   "n1",
		}},
		{"node with hash", keyspace.Key{
			Family:       keyspace.FamilyNode,
			IdentityHash: sampleHash,
			Node:         "n1",
		}},
		{"node with version", keyspace.Key{
			Family:  keyspace.FamilyNode,
			Version: "v1",
			Node:    "n1",
		}},
		{"node with empty node", keyspace.Key{
			Family: keyspace.FamilyNode,
		}},
		{"node with invalid node", keyspace.Key{
			Family: keyspace.FamilyNode,
			Node:   "NODE-1",
		}},
		{"eviction budget with tenant", keyspace.Key{
			Family: keyspace.FamilyEvictionBudget,
			Tenant: "acme",
		}},
		{"eviction budget with object", keyspace.Key{
			Family: keyspace.FamilyEvictionBudget,
			Object: keyspace.Object{Name: "n"},
		}},
		{"eviction budget with source", keyspace.Key{
			Family: keyspace.FamilyEvictionBudget,
			Source: keyspace.Object{Name: "n"},
		}},
		{"eviction budget with hash", keyspace.Key{
			Family:       keyspace.FamilyEvictionBudget,
			IdentityHash: sampleHash,
		}},
		{"eviction budget with version", keyspace.Key{
			Family:  keyspace.FamilyEvictionBudget,
			Version: "v1",
		}},
		{"eviction budget with node", keyspace.Key{
			Family: keyspace.FamilyEvictionBudget,
			Node:   "n1",
		}},
		{"object invalid tenant", keyspace.Key{
			Family: keyspace.FamilyObject,
			Tenant: "ACME",
			Object: keyspace.Object{Group: "core", Kind: "pod", Name: "p1"},
		}},
	}

	for _, tc := range invalidKeys {
		if _, err := tc.key.Encode(); !errors.Is(err, keyspace.ErrInvalid) {
			t.Errorf("%s: Encode() error = %v, want ErrInvalid", tc.name, err)
		}
		if _, err := tc.key.Rel(); !errors.Is(err, keyspace.ErrInvalid) {
			t.Errorf("%s: Rel() error = %v, want ErrInvalid", tc.name, err)
		}
	}
}

func TestParseRejections(t *testing.T) {
	rejected := []string{
		"",
		"/",
		"/t",
		"/t/",
		"/t/acme",
		"/t/acme/",
		"/t/acme/o/core/workload/web/",
		"/t/acme/o//workload/web",
		"/t/acme//o/core/workload/web",
		"/t//o/core/workload/web",
		"/t/acme/o/core/workload/../web",
		"/t/acme/o/core/workload/a%2Fb",
		"/t/acme/o/core/workload/web/extra",
		"/t/acme/own/core/workload/web/extra",
		"/t/acme/own/core/workload",
		"/t/acme/zzz/x",
		"/t/acme/o/apps/workload/web",
		"/t/acme/ref/widget.apps/w1/secret/s",
		"/t/acme/ref/x.core/w1/secret/s",
		"/t/acme/ref/secret/db/pod/p1/extra",
		"/t/acme/ref/secret/db/pod",
		"/t/acme/bind/",
		"/t/acme/bind/" + sampleHash + "/",
		"/t/acme/bind/" + sampleHash + "/extra",
		"/sys",
		"/sys/tenants",
		"/sys/tenants/",
		"/sys/tenants/acme/",
		"/sys/tenants/acme/x",
		"/sys/nodes",
		"/sys/nodes/",
		"/sys/nodes/a/b",
		"/sys/nodes/A",
		"/sys/nodes/a/",
		"/sys/nodes/-a",
		"/sys/liveness",
		"/sys/liveness/",
		"/sys/liveness/evictions/",
		"/sys/liveness/evictions/extra",
		"/sys/liveness/other",
		"/sys/liveness/eviction",
		"/sys/other/acme",
		"/x",
		"/x/",
		"/x/" + sampleHash + "/schema",
		"/x/" + sampleHash + "/schema/",
		"/x/" + sampleHash + "/schema/v1/",
		"/x/" + sampleHash + "/schema/v1/extra",
		"/x/" + sampleHash + "/other/v1",
		"/T/acme/o/core/workload/web",
		"t/acme/o/core/workload/web",
		"/t/acme/o/core/workload/web\x00",
		"/t/acme/o/core/workload/web\xff",
	}

	for _, s := range rejected {
		if k, err := keyspace.Parse(s); !errors.Is(err, keyspace.ErrInvalid) {
			t.Errorf("Parse(%q) = (%+v, %v), want ErrInvalid", s, k, err)
		}
	}

	rejectedRel := []struct {
		tenant string
		rel    string
	}{
		{"acme", ""},
		{"acme", "/o/core/workload/web"},
		{"acme", "/bind/" + sampleHash},
		{"ACME", "o/core/workload/web"},
		{"", "o/core/workload/web"},
		{"acme", "o/core/workload/web/"},
		{"acme", "schema/v1"},
		{"acme", "tenants/acme"},
		{"acme", "nodes/n1"},
		{"acme", "liveness/evictions"},
		{"acme", "o/core/workload"},
		{"acme", "o/core/workload/web/extra"},
		{"acme", "bind/" + sampleHash + "/extra"},
	}
	for _, tc := range rejectedRel {
		if k, err := keyspace.ParseRel(tc.tenant, tc.rel); !errors.Is(err, keyspace.ErrInvalid) {
			t.Errorf("ParseRel(%q, %q) = (%+v, %v), want ErrInvalid", tc.tenant, tc.rel, k, err)
		}
	}
}

func TestPrefixes(t *testing.T) {
	if got, err := keyspace.TenantPrefix("acme"); err != nil || got != "/t/acme/" {
		t.Errorf("TenantPrefix(acme) = (%q, %v)", got, err)
	}
	if _, err := keyspace.TenantPrefix("ACME"); !errors.Is(err, keyspace.ErrInvalid) {
		t.Errorf("TenantPrefix(ACME) error = %v, want ErrInvalid", err)
	}

	if got, err := keyspace.SchemaPrefix(sampleHash); err != nil || got != "/x/"+sampleHash+"/schema/" {
		t.Errorf("SchemaPrefix = (%q, %v)", got, err)
	}
	if _, err := keyspace.SchemaPrefix("bad"); !errors.Is(err, keyspace.ErrInvalid) {
		t.Errorf("SchemaPrefix(bad) error = %v, want ErrInvalid", err)
	}

	if got, err := keyspace.ObjectPrefix("core", "pod"); err != nil || got != "o/core/pod/" {
		t.Errorf("ObjectPrefix = (%q, %v)", got, err)
	}
	if _, err := keyspace.ObjectPrefix("apps", "pod"); !errors.Is(err, keyspace.ErrInvalid) {
		t.Errorf("ObjectPrefix(apps, pod) error = %v, want ErrInvalid", err)
	}

	if got, err := keyspace.OwnershipPrefix("apps.example.com", "app"); err != nil || got != "own/apps.example.com/app/" {
		t.Errorf("OwnershipPrefix = (%q, %v)", got, err)
	}
	if _, err := keyspace.OwnershipPrefix("core", "Pod"); !errors.Is(err, keyspace.ErrInvalid) {
		t.Errorf("OwnershipPrefix(core, Pod) error = %v, want ErrInvalid", err)
	}

	if keyspace.BackRefsPrefix != "ref/" {
		t.Errorf("BackRefsPrefix = %q, want %q", keyspace.BackRefsPrefix, "ref/")
	}
	if got, err := keyspace.BackRefPrefix(keyspace.Object{Group: "core", Kind: "secret", Name: "db"}); err != nil || got != "ref/secret/db/" {
		t.Errorf("BackRefPrefix(core) = (%q, %v)", got, err)
	}
	if got, err := keyspace.BackRefPrefix(keyspace.Object{Group: "widgets.example.com", Kind: "widget", Name: "w1.ns"}); err != nil || got != "ref/widget.widgets.example.com/w1.ns/" {
		t.Errorf("BackRefPrefix(custom) = (%q, %v)", got, err)
	}
	if _, err := keyspace.BackRefPrefix(keyspace.Object{Group: "apps", Kind: "widget", Name: "w1"}); !errors.Is(err, keyspace.ErrInvalid) {
		t.Errorf("BackRefPrefix(invalid) error = %v, want ErrInvalid", err)
	}
}

const labelAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789-"

func randElement(rng *rand.Rand, minLen, maxLen int) string {
	n := minLen + rng.IntN(maxLen-minLen+1)
	b := make([]byte, n)
	for i := range n {
		if i == 0 || i == n-1 {
			b[i] = labelAlphabet[rng.IntN(36)]
		} else {
			b[i] = labelAlphabet[rng.IntN(len(labelAlphabet))]
		}
	}
	return string(b)
}

func randLabel(rng *rand.Rand, minLen, maxLen int) string {
	return randElement(rng, minLen, maxLen)
}

func randSubdomain(rng *rand.Rand, minElems int, forceMax bool) string {
	if forceMax {
		if minElems == 1 && rng.IntN(2) == 0 {
			return randElement(rng, 253, 253)
		}
		if rng.IntN(2) == 0 {
			l1 := 64 + rng.IntN(120)
			l2 := 253 - 1 - l1
			return randElement(rng, l1, l1) + "." + randElement(rng, l2, l2)
		}
		return maxSubdomain()
	}
	if minElems == 1 && rng.IntN(4) == 0 {
		if rng.IntN(2) == 0 {
			return randElement(rng, 64, 253)
		}
		return randElement(rng, 1, 63)
	}
	targetElems := max(minElems, 2) + rng.IntN(3)
	parts := make([]string, 0, targetElems)
	longIdx := -1
	if rng.IntN(4) == 0 {
		longIdx = rng.IntN(targetElems)
	}
	total := 0
	for i := range targetElems {
		dot := 0
		if len(parts) > 0 {
			dot = 1
		}
		reserved := (targetElems - i - 1) * 2
		maxElem := 253 - total - dot - reserved
		var elem string
		if i == longIdx && maxElem >= 64 {
			elem = randElement(rng, 64, min(maxElem, 200))
		} else {
			elem = randElement(rng, 1, min(maxElem, 32))
		}
		total += dot + len(elem)
		parts = append(parts, elem)
	}
	return strings.Join(parts, ".")
}

func randGroup(rng *rand.Rand, forceMax bool) string {
	if !forceMax && rng.IntN(3) == 0 {
		return keyspace.CoreGroup
	}
	return randSubdomain(rng, 2, forceMax)
}

func randHash(rng *rand.Rand) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 64)
	for i := range b {
		b[i] = hex[rng.IntN(len(hex))]
	}
	return string(b)
}

func randObject(rng *rand.Rand, forceMax bool) keyspace.Object {
	kindLen := 1 + rng.IntN(63)
	if forceMax {
		kindLen = 63
	}
	return keyspace.Object{
		Group: randGroup(rng, forceMax),
		Kind:  randLabel(rng, kindLen, kindLen),
		Name:  randSubdomain(rng, 1, forceMax),
	}
}

func randValidKey(rng *rand.Rand, fam keyspace.Family, forceMax bool) keyspace.Key {
	tenantLen := 1 + rng.IntN(63)
	if forceMax {
		tenantLen = 63
	}
	switch fam {
	case keyspace.FamilyObject, keyspace.FamilyOwnership:
		return keyspace.Key{
			Family: fam,
			Tenant: randLabel(rng, tenantLen, tenantLen),
			Object: randObject(rng, forceMax),
		}
	case keyspace.FamilyBackRef:
		return keyspace.Key{
			Family: keyspace.FamilyBackRef,
			Tenant: randLabel(rng, tenantLen, tenantLen),
			Object: randObject(rng, forceMax),
			Source: randObject(rng, forceMax),
		}
	case keyspace.FamilyBinding:
		return keyspace.Key{
			Family:       keyspace.FamilyBinding,
			Tenant:       randLabel(rng, tenantLen, tenantLen),
			IdentityHash: randHash(rng),
		}
	case keyspace.FamilySchema:
		verLen := 1 + rng.IntN(63)
		if forceMax {
			verLen = 63
		}
		return keyspace.Key{
			Family:       keyspace.FamilySchema,
			IdentityHash: randHash(rng),
			Version:      randLabel(rng, verLen, verLen),
		}
	case keyspace.FamilyTenant:
		return keyspace.Key{
			Family: keyspace.FamilyTenant,
			Tenant: randLabel(rng, tenantLen, tenantLen),
		}
	case keyspace.FamilyNode:
		return keyspace.Key{
			Family: keyspace.FamilyNode,
			Node:   randSubdomain(rng, 1, forceMax),
		}
	case keyspace.FamilyEvictionBudget:
		return keyspace.Key{
			Family: keyspace.FamilyEvictionBudget,
		}
	default:
		panic("unexpected family")
	}
}

func TestProperties(t *testing.T) {
	rng := rand.New(rand.NewPCG(20261118, 42))
	families := []keyspace.Family{
		keyspace.FamilyObject,
		keyspace.FamilyOwnership,
		keyspace.FamilyBackRef,
		keyspace.FamilyBinding,
		keyspace.FamilySchema,
		keyspace.FamilyTenant,
		keyspace.FamilyNode,
		keyspace.FamilyEvictionBudget,
	}

	for i := range 4000 {
		fam := families[i%len(families)]
		forceMax := i < len(families)*4
		k := randValidKey(rng, fam, forceMax)

		abs, err := k.Encode()
		if err != nil {
			t.Fatalf("iter %d: Encode(%+v) error = %v", i, k, err)
		}

		parsed, err := keyspace.Parse(abs)
		if err != nil {
			t.Fatalf("iter %d: Parse(%q) error = %v", i, abs, err)
		}
		if parsed != k {
			t.Fatalf("iter %d: Parse(%q) = %+v, want %+v", i, abs, parsed, k)
		}
		reencoded, err := parsed.Encode()
		if err != nil || reencoded != abs {
			t.Fatalf("iter %d: re-encode = (%q, %v), want %q", i, reencoded, err, abs)
		}

		switch fam {
		case keyspace.FamilyObject, keyspace.FamilyOwnership, keyspace.FamilyBackRef, keyspace.FamilyBinding:
			if !strings.HasPrefix(abs, "/t/") || strings.HasPrefix(abs, "/x/") || strings.HasPrefix(abs, "/sys/") {
				t.Fatalf("iter %d: tenant key %q has wrong top-level prefix", i, abs)
			}
			rel, err := k.Rel()
			if err != nil {
				t.Fatalf("iter %d: Rel(%+v) error = %v", i, k, err)
			}
			parsedRel, err := keyspace.ParseRel(k.Tenant, rel)
			if err != nil {
				t.Fatalf("iter %d: ParseRel(%q, %q) error = %v", i, k.Tenant, rel, err)
			}
			if parsedRel != k {
				t.Fatalf("iter %d: ParseRel = %+v, want %+v", i, parsedRel, k)
			}
			tPrefix, err := keyspace.TenantPrefix(k.Tenant)
			if err != nil || !strings.HasSuffix(tPrefix, "/") {
				t.Fatalf("iter %d: TenantPrefix(%q) = (%q, %v)", i, k.Tenant, tPrefix, err)
			}
			if abs != tPrefix+rel {
				t.Fatalf("iter %d: abs %q != %q + %q", i, abs, tPrefix, rel)
			}
			switch fam {
			case keyspace.FamilyObject:
				p, err := keyspace.ObjectPrefix(k.Object.Group, k.Object.Kind)
				if err != nil || !strings.HasSuffix(p, "/") || !strings.HasPrefix(rel, p) {
					t.Fatalf("iter %d: ObjectPrefix = (%q, %v), rel = %q", i, p, err, rel)
				}
			case keyspace.FamilyOwnership:
				p, err := keyspace.OwnershipPrefix(k.Object.Group, k.Object.Kind)
				if err != nil || !strings.HasSuffix(p, "/") || !strings.HasPrefix(rel, p) {
					t.Fatalf("iter %d: OwnershipPrefix = (%q, %v), rel = %q", i, p, err, rel)
				}
			case keyspace.FamilyBackRef:
				p, err := keyspace.BackRefPrefix(k.Object)
				if err != nil || !strings.HasSuffix(p, "/") || !strings.HasPrefix(rel, p) {
					t.Fatalf("iter %d: BackRefPrefix = (%q, %v), rel = %q", i, p, err, rel)
				}
			case keyspace.FamilyBinding:
				if !strings.HasSuffix(keyspace.BindingPrefix, "/") || !strings.HasPrefix(rel, keyspace.BindingPrefix) {
					t.Fatalf("iter %d: BindingPrefix %q does not prefix %q", i, keyspace.BindingPrefix, rel)
				}
			}
		case keyspace.FamilySchema:
			if !strings.HasPrefix(abs, "/x/") || strings.HasPrefix(abs, "/t/") || strings.HasPrefix(abs, "/sys/") {
				t.Fatalf("iter %d: schema key %q has wrong top-level prefix", i, abs)
			}
			if _, err := k.Rel(); !errors.Is(err, keyspace.ErrInvalid) {
				t.Fatalf("iter %d: schema Rel() error = %v, want ErrInvalid", i, err)
			}
			p, err := keyspace.SchemaPrefix(k.IdentityHash)
			if err != nil || !strings.HasSuffix(p, "/") || !strings.HasPrefix(abs, p) {
				t.Fatalf("iter %d: SchemaPrefix = (%q, %v), abs = %q", i, p, err, abs)
			}
		case keyspace.FamilyTenant:
			if !strings.HasPrefix(abs, "/sys/") || strings.HasPrefix(abs, "/t/") || strings.HasPrefix(abs, "/x/") {
				t.Fatalf("iter %d: tenant record key %q has wrong top-level prefix", i, abs)
			}
			if _, err := k.Rel(); !errors.Is(err, keyspace.ErrInvalid) {
				t.Fatalf("iter %d: tenant Rel() error = %v, want ErrInvalid", i, err)
			}
			if !strings.HasSuffix(keyspace.TenantsPrefix, "/") || !strings.HasPrefix(abs, keyspace.TenantsPrefix) {
				t.Fatalf("iter %d: TenantsPrefix %q does not prefix %q", i, keyspace.TenantsPrefix, abs)
			}
			if strings.HasPrefix(abs, keyspace.NodesPrefix) || abs == keyspace.EvictionBudgetKey {
				t.Fatalf("iter %d: tenant key %q overlaps other /sys/ shape", i, abs)
			}
		case keyspace.FamilyNode:
			if !strings.HasPrefix(abs, "/sys/") || strings.HasPrefix(abs, "/t/") || strings.HasPrefix(abs, "/x/") {
				t.Fatalf("iter %d: node key %q has wrong top-level prefix", i, abs)
			}
			if _, err := k.Rel(); !errors.Is(err, keyspace.ErrInvalid) {
				t.Fatalf("iter %d: node Rel() error = %v, want ErrInvalid", i, err)
			}
			if !strings.HasSuffix(keyspace.NodesPrefix, "/") || !strings.HasPrefix(abs, keyspace.NodesPrefix) {
				t.Fatalf("iter %d: NodesPrefix %q does not prefix %q", i, keyspace.NodesPrefix, abs)
			}
			if strings.HasPrefix(abs, keyspace.TenantsPrefix) || abs == keyspace.EvictionBudgetKey {
				t.Fatalf("iter %d: node key %q overlaps other /sys/ shape", i, abs)
			}
		case keyspace.FamilyEvictionBudget:
			if !strings.HasPrefix(abs, "/sys/") || strings.HasPrefix(abs, "/t/") || strings.HasPrefix(abs, "/x/") {
				t.Fatalf("iter %d: eviction budget key %q has wrong top-level prefix", i, abs)
			}
			if _, err := k.Rel(); !errors.Is(err, keyspace.ErrInvalid) {
				t.Fatalf("iter %d: eviction budget Rel() error = %v, want ErrInvalid", i, err)
			}
			if abs != keyspace.EvictionBudgetKey {
				t.Fatalf("iter %d: eviction budget key = %q, want %q", i, abs, keyspace.EvictionBudgetKey)
			}
			if strings.HasPrefix(abs, keyspace.TenantsPrefix) || strings.HasPrefix(abs, keyspace.NodesPrefix) {
				t.Fatalf("iter %d: eviction budget key %q overlaps other /sys/ shape", i, abs)
			}
		}
	}

	sysFamilies := []keyspace.Family{
		keyspace.FamilyTenant,
		keyspace.FamilyNode,
		keyspace.FamilyEvictionBudget,
	}
	for range 200 {
		keys := make([]string, len(sysFamilies))
		for idx, fam := range sysFamilies {
			enc, err := randValidKey(rng, fam, false).Encode()
			if err != nil {
				t.Fatal(err)
			}
			keys[idx] = enc
		}
		for i := range keys {
			for j := range keys {
				if i == j {
					continue
				}
				if keys[i] == keys[j] || strings.HasPrefix(keys[i], keys[j]) {
					t.Fatalf("/sys/ family %v key %q has prefix of family %v key %q", sysFamilies[j], keys[j], sysFamilies[i], keys[i])
				}
			}
		}
	}
	for range 200 {
		keys := make([]string, len(families))
		for idx, fam := range families {
			enc, err := randValidKey(rng, fam, false).Encode()
			if err != nil {
				t.Fatal(err)
			}
			keys[idx] = enc
		}
		for i := range keys {
			for j := range keys {
				if i == j {
					continue
				}
				if strings.HasPrefix(keys[i], keys[j]) {
					t.Fatalf("family %v key %q is a prefix of family %v key %q", families[j], keys[j], families[i], keys[i])
				}
			}
		}
	}

	prefixPairs := [][2]string{
		{"a", "ab"},
		{"a", "a-b"},
		{"a", "a0"},
		{"acme", "acme-prod"},
		{strings.Repeat("a", 62), strings.Repeat("a", 63)},
	}
	for i := range 500 {
		t1 := randLabel(rng, 1, 10)
		t2 := t1 + randLabel(rng, 1, 5)
		if i%2 == 0 {
			t2 = randLabel(rng, 1, 63)
			if t1 == t2 {
				continue
			}
		}
		prefixPairs = append(prefixPairs, [2]string{t1, t2})
	}
	for _, pair := range prefixPairs {
		t1, t2 := pair[0], pair[1]
		p1, err := keyspace.TenantPrefix(t1)
		if err != nil {
			t.Fatal(err)
		}
		p2, err := keyspace.TenantPrefix(t2)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(p1, p2) || strings.HasPrefix(p2, p1) {
			t.Fatalf("tenant prefixes %q and %q overlap", p1, p2)
		}
		for _, fam := range []keyspace.Family{
			keyspace.FamilyObject,
			keyspace.FamilyOwnership,
			keyspace.FamilyBackRef,
			keyspace.FamilyBinding,
		} {
			k1 := randValidKey(rng, fam, false)
			k1.Tenant = t1
			enc1, err := k1.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(enc1, p2) {
				t.Fatalf("key %q for tenant %q has prefix %q of tenant %q", enc1, t1, p2, t2)
			}
			k2 := randValidKey(rng, fam, false)
			k2.Tenant = t2
			enc2, err := k2.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(enc2, p1) {
				t.Fatalf("key %q for tenant %q has prefix %q of tenant %q", enc2, t2, p1, t1)
			}
		}
	}
}

func TestSingleByteMutations(t *testing.T) {
	seeds := []string{
		"/t/acme/o/core/workload/web",
		"/t/acme/o/widgets.example.com/widget/w1.prod",
		"/t/acme/own/core/workload/web",
		"/t/acme/ref/secret/db-creds/widget.widgets.example.com/w1",
		"/t/acme/bind/" + sampleHash,
		"/x/" + sampleHash2 + "/schema/v1alpha1",
		"/sys/tenants/acme",
		"/sys/nodes/node-1.rack-a",
		keyspace.EvictionBudgetKey,
	}
	hostile := []byte{'/', '.', '%', 0x00, 'A', '_', 0xff, '-', ' ', 'z', '0', 0x7f}

	rng := rand.New(rand.NewPCG(99, 12345))
	for range 5000 {
		base := seeds[rng.IntN(len(seeds))]
		op := rng.IntN(3)
		var mutated string
		switch op {
		case 0:
			pos := rng.IntN(len(base) + 1)
			b := hostile[rng.IntN(len(hostile))]
			mutated = base[:pos] + string([]byte{b}) + base[pos:]
		case 1:
			pos := rng.IntN(len(base))
			mutated = base[:pos] + base[pos+1:]
		case 2:
			pos := rng.IntN(len(base))
			b := hostile[rng.IntN(len(hostile))]
			mutated = base[:pos] + string([]byte{b}) + base[pos+1:]
		}

		k, err := keyspace.Parse(mutated)
		if err != nil {
			if !errors.Is(err, keyspace.ErrInvalid) {
				t.Fatalf("Parse(%q) error = %v, want ErrInvalid", mutated, err)
			}
			continue
		}
		reencoded, err := k.Encode()
		if err != nil {
			t.Fatalf("Encode(%+v) failed after Parse(%q): %v", k, mutated, err)
		}
		if reencoded != mutated {
			t.Fatalf("Parse(%q) re-encoded to different string %q", mutated, reencoded)
		}
	}
}
