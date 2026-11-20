package tenant_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
)

func isRFC1123Label(s string) bool {
	if len(s) < 1 || len(s) > 63 {
		return false
	}
	for i, r := range []byte(s) {
		alnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if !alnum && !(r == '-' && i != 0 && i != len(s)-1) {
			return false
		}
	}
	return true
}

func FuzzValidateName(f *testing.F) {
	for _, seed := range []string{
		"", "a", "a1", "1a", "a-b", "a--b", "-", "-a", "a-", "A", "a_b", "a.b", "a b", "a/b", "a\x00",
		"é", "a\xffb", "ａ", "\n", "a\n", "t-1", "xn--bcher-kva",
		strings.Repeat("a", 62), strings.Repeat("a", 63), strings.Repeat("a", 64), strings.Repeat("-", 63),
		"a" + strings.Repeat("-", 61) + "a",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, name string) {
		err := tenant.ValidateName(name)
		if want := isRFC1123Label(name); (err == nil) != want {
			t.Fatalf("ValidateName(%q) = %v, oracle says valid = %v", name, err, want)
		}
		prefix, perr := tenant.Prefix(name)
		if err != nil {
			if !errors.Is(err, tenant.ErrInvalidName) {
				t.Fatalf("ValidateName(%q) error %v does not wrap ErrInvalidName", name, err)
			}
			if perr == nil || prefix != "" {
				t.Fatalf("Prefix(%q) = %q, %v for an invalid name", name, prefix, perr)
			}
			return
		}
		if perr != nil || prefix != "/t/"+name+"/" {
			t.Fatalf("Prefix(%q) = %q, %v", name, prefix, perr)
		}
	})
}

func FuzzRegistryRecordDecode(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"$lkal":"0.1","state":{"$enum":"active"}}`),
		[]byte(`{"$lkal":"0.1","state":{"$enum":"terminating"}}`),
		[]byte(`{ "$lkal" : "0.1" , "state" : { "$enum" : "active" } }`),
		[]byte(`{}`),
		[]byte(`{"$lkal":"0.1"}`),
		[]byte(`{"$lkal":"0.2","state":{"$enum":"active"}}`),
		[]byte(`{"$lkal":"0.1","state":{"$enum":"other"}}`),
		[]byte(`{"$lkal":"0.1","$lkal":"0.1","state":{"$enum":"active"}}`),
		[]byte(`{"$lkal":"0.1","state":{"$enum":"active"}} {}`),
		[]byte(`{"$lkal":"0.1","state":null}`),
		[]byte(`{"$lkal":"0.1","state":{"$enum":"active"},"extra":1}`),
		[]byte(`null`),
		[]byte(``),
		[]byte{0xff, 0xfe, 0xfd},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		ctx := context.Background()
		root := memstore.New()
		if ok, _, err := root.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "/sys/tenants/a", Value: data}}); err != nil || !ok {
			t.Fatalf("Txn put = (%v, %v)", ok, err)
		}
		reg := tenant.NewRegistry(root)
		rec, err := reg.Get(ctx, "a")
		if err != nil {
			if !errors.Is(err, tenant.ErrCorruptRecord) {
				t.Fatalf("Get error = %v, want ErrCorruptRecord", err)
			}
			return
		}
		if rec.Name != "a" || (rec.State != tenant.StateActive && rec.State != tenant.StateTerminating) || rec.ModRev <= 0 {
			t.Fatalf("unexpected decoded record %+v", rec)
		}
	})
}
