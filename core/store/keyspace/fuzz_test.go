package keyspace_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

func FuzzParse(f *testing.F) {
	seeds := []string{
		"",
		"/",
		"/t/acme",
		"/t/acme/",
		"/t/acme/o/core/workload/web",
		"/t/acme/o/widgets.example.com/widget/w1.prod",
		"/t/acme/o/core/workload/web/",
		"/t/acme/o//workload/web",
		"/t/acme/o/core/workload/../web",
		"/t/acme/o/core/workload/a%2Fb",
		"/t/acme/own/core/workload/web",
		"/t/acme/ref/secret/db-creds/widget.widgets.example.com/w1",
		"/t/acme/ref/widget.apps/w1/secret/s",
		"/t/acme/ref/x.core/w1/secret/s",
		"/t/acme/bind/" + sampleHash,
		"/t/acme/zzz/x",
		"/x/" + sampleHash + "/schema/v1alpha1",
		"/x/" + sampleHash + "/schema",
		"/x/" + sampleHash + "/other/v1",
		"/sys/tenants/acme",
		"/sys/tenants/acme/x",
		"/sys/nodes/node-1.rack-a",
		"/sys/nodes/",
		"/sys/nodes/a/b",
		"/sys/liveness/evictions",
		"/sys/liveness/evictions/",
		"/sys/liveness/other",
		"/sys/liveness",
		"/sys/other/acme",
		"/T/acme/o/core/workload/web",
		"t/acme/o/core/workload/web",
		"/t/acme/o/core/workload/web\x00",
		"/t/acme/o/core/workload/web\xff",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		k, err := keyspace.Parse(s)
		if err != nil {
			if !errors.Is(err, keyspace.ErrInvalid) {
				t.Fatalf("Parse(%q) error %v does not wrap ErrInvalid", s, err)
			}
			return
		}
		enc, err := k.Encode()
		if err != nil {
			t.Fatalf("Encode(%+v) failed after Parse(%q): %v", k, s, err)
		}
		if enc != s {
			t.Fatalf("Parse(%q) re-encoded to %q", s, enc)
		}
		switch k.Family {
		case keyspace.FamilyObject, keyspace.FamilyOwnership, keyspace.FamilyBackRef, keyspace.FamilyBinding:
			tp, err := keyspace.TenantPrefix(k.Tenant)
			if err != nil {
				t.Fatalf("TenantPrefix(%q) failed after Parse(%q): %v", k.Tenant, s, err)
			}
			if !strings.HasPrefix(s, tp) {
				t.Fatalf("Parse(%q) tenant prefix %q does not match", s, tp)
			}
			rel, err := k.Rel()
			if err != nil {
				t.Fatalf("Rel(%+v) failed after Parse(%q): %v", k, s, err)
			}
			parsedRel, err := keyspace.ParseRel(k.Tenant, rel)
			if err != nil || parsedRel != k {
				t.Fatalf("ParseRel(%q, %q) = (%+v, %v), want %+v", k.Tenant, rel, parsedRel, err, k)
			}
		case keyspace.FamilySchema, keyspace.FamilyTenant, keyspace.FamilyNode, keyspace.FamilyEvictionBudget:
			if _, err := k.Rel(); !errors.Is(err, keyspace.ErrInvalid) {
				t.Fatalf("Rel(%+v) error = %v, want ErrInvalid", k, err)
			}
		default:
			t.Fatalf("Parse(%q) returned unknown family %d", s, k.Family)
		}
	})
}

func FuzzRelParse(f *testing.F) {
	seeds := []struct {
		tenant string
		rel    string
	}{
		{"acme", "o/core/workload/web"},
		{"acme", "own/core/workload/web"},
		{"acme", "ref/secret/db-creds/widget.widgets.example.com/w1"},
		{"acme", "bind/" + sampleHash},
		{"acme", "/o/core/workload/web"},
		{"acme", ""},
		{"ACME", "o/core/workload/web"},
		{"acme", "schema/v1"},
		{"acme", "tenants/acme"},
		{"acme", "nodes/n1"},
		{"acme", "liveness/evictions"},
		{"acme", "o/core/workload/web\xff"},
	}
	for _, s := range seeds {
		f.Add(s.tenant, s.rel)
	}

	f.Fuzz(func(t *testing.T, tenant, rel string) {
		k, err := keyspace.ParseRel(tenant, rel)
		if err != nil {
			if !errors.Is(err, keyspace.ErrInvalid) {
				t.Fatalf("ParseRel(%q, %q) error %v does not wrap ErrInvalid", tenant, rel, err)
			}
			return
		}
		if strings.HasPrefix(rel, "/") {
			t.Fatalf("ParseRel(%q, %q) accepted leading slash", tenant, rel)
		}
		if k.Tenant != tenant {
			t.Fatalf("ParseRel(%q, %q) tenant = %q", tenant, rel, k.Tenant)
		}
		gotRel, err := k.Rel()
		if err != nil {
			t.Fatalf("Rel(%+v) failed after ParseRel(%q, %q): %v", k, tenant, rel, err)
		}
		if gotRel != rel {
			t.Fatalf("ParseRel(%q, %q) Rel() = %q", tenant, rel, gotRel)
		}
		enc, err := k.Encode()
		if err != nil {
			t.Fatalf("Encode(%+v) failed: %v", k, err)
		}
		parsed, err := keyspace.Parse(enc)
		if err != nil || parsed != k {
			t.Fatalf("Parse(%q) = (%+v, %v), want %+v", enc, parsed, err, k)
		}
	})
}
