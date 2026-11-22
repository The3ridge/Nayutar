package orderedset_test

import (
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/orderedset"
)

func rec(fields ...lkal.Field) lkal.Record {
	return lkal.Record{Fields: fields}
}

func f(k string, v lkal.Value) lkal.Field {
	return lkal.Field{Key: k, Value: v}
}

func assertOrderErr(t *testing.T, err error, wantKind orderedset.ErrorKind, wantSentinel error) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want %s", wantKind)
	}
	var osErr *orderedset.Error
	if !errors.As(err, &osErr) {
		t.Fatalf("error %T (%v) is not *orderedset.Error", err, err)
	}
	if osErr.Kind != wantKind {
		t.Fatalf("error kind = %s, want %s (err=%v)", osErr.Kind, wantKind, err)
	}
	if !errors.Is(err, wantSentinel) {
		t.Fatalf("errors.Is(%v, %v) = false, want true", err, wantSentinel)
	}
}

func TestErrorKindAndFormatting(t *testing.T) {
	cases := []struct {
		kind     orderedset.ErrorKind
		wantStr  string
		sentinel error
	}{
		{orderedset.Malformed, "malformed", orderedset.ErrMalformed},
		{orderedset.DuplicateKey, "duplicateKey", orderedset.ErrDuplicateKey},
		{orderedset.DuplicateOrder, "duplicateOrder", orderedset.ErrDuplicateOrder},
		{orderedset.ReservedField, "reservedField", orderedset.ErrReservedField},
		{orderedset.GapExhausted, "gapExhausted", orderedset.ErrGapExhausted},
		{orderedset.Overflow, "overflow", orderedset.ErrOverflow},
	}

	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.wantStr {
			t.Errorf("ErrorKind(%d).String() = %q, want %q", tc.kind, got, tc.wantStr)
		}
		err := &orderedset.Error{Kind: tc.kind, Key: "bridge", Detail: "test detail"}
		if !errors.Is(err, tc.sentinel) {
			t.Errorf("errors.Is(%v, %v) = false", err, tc.sentinel)
		}
		for _, sub := range []string{tc.wantStr, "bridge", "test detail"} {
			if !strings.Contains(err.Error(), sub) {
				t.Errorf("Error() = %q, missing %q", err.Error(), sub)
			}
		}
	}

	if got := orderedset.ErrorKind(99).String(); got != "unknown(99)" {
		t.Errorf("unknown ErrorKind.String() = %q, want unknown(99)", got)
	}
	if got := (&orderedset.Error{Kind: orderedset.ErrorKind(99)}).Unwrap(); got != nil {
		t.Errorf("unknown Error.Unwrap() = %v, want nil", got)
	}

	var nilErr *orderedset.Error
	if nilErr.Error() != "<nil>" {
		t.Errorf("nil Error.Error() = %q, want <nil>", nilErr.Error())
	}
	if nilErr.Unwrap() != nil {
		t.Errorf("nil Error.Unwrap() = %v, want nil", nilErr.Unwrap())
	}

	noKey := &orderedset.Error{Kind: orderedset.GapExhausted, Detail: "adjacent"}
	if noKey.Error() != "orderedset: gapExhausted: adjacent" {
		t.Errorf("noKey.Error() = %q", noKey.Error())
	}
}

func TestEntriesCNIConflistForwardAndReverse(t *testing.T) {
	src := `@lkal "0.1"
plugins {
  plugin "fw-post" {
    type = "firewall"
    order = 30
    backend = "nftables"
    ingressPolicy = "drop"
  }
  plugin "bridge" {
    order = 20
    type = "bridge"
    bridge = "lk0"
    isGateway = true
  }
  plugin "portmap" {
    order = -10
    type = "portmap"
    snat = true
  }
  plugin "fw-pre" {
    type = "firewall"
    backend = "nftables"
    order = 10
    ingressPolicy = "allow"
  }
  plugin "tuning" {
    order = 0
    type = "tuning"
    sysctl {
      net_ipv4_ip_forward = "1"
    }
  }
}
`
	file, err := lkal.Parse("cni.lkal", []byte(src))
	if err != nil {
		t.Fatalf("lkal.Parse: %v", err)
	}
	pluginsRec := file.Data().Fields[0].Value.(lkal.Record)
	pluginSet := pluginsRec.Fields[0].Value.(lkal.Record)

	got, err := orderedset.Entries(pluginSet)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}

	wantKeys := []string{"portmap", "tuning", "fw-pre", "bridge", "fw-post"}
	wantOrders := []int64{-10, 0, 10, 20, 30}
	wantTypes := []string{"portmap", "tuning", "firewall", "bridge", "firewall"}

	if len(got) != len(wantKeys) {
		t.Fatalf("len(Entries) = %d, want %d", len(got), len(wantKeys))
	}
	for i, e := range got {
		if e.Key != wantKeys[i] || e.Order != wantOrders[i] {
			t.Fatalf("entry[%d] = (%q, %d), want (%q, %d)", i, e.Key, e.Order, wantKeys[i], wantOrders[i])
		}
		for _, fld := range e.Value.Fields {
			if fld.Key == orderedset.OrderField {
				t.Fatalf("entry %q still contains order field", e.Key)
			}
		}
		if typ := e.Value.Fields[0].Value.(lkal.String); string(typ) != wantTypes[i] {
			t.Fatalf("entry %q first field type = %q, want %q", e.Key, typ, wantTypes[i])
		}
	}

	// Verify remaining field order is preserved when "order" sits in the middle.
	fwPreFields := make([]string, len(got[2].Value.Fields))
	for i, fld := range got[2].Value.Fields {
		fwPreFields[i] = fld.Key
	}
	if !slices.Equal(fwPreFields, []string{"type", "backend", "ingressPolicy"}) {
		t.Fatalf("fw-pre stripped fields = %v, want [type backend ingressPolicy]", fwPreFields)
	}

	// EntriesReverse yields exact reverse (for CNI DEL / GC).
	rev, err := orderedset.EntriesReverse(pluginSet)
	if err != nil {
		t.Fatalf("EntriesReverse: %v", err)
	}
	if len(rev) != len(got) {
		t.Fatalf("len(EntriesReverse) = %d, want %d", len(rev), len(got))
	}
	for i := range got {
		j := len(got) - 1 - i
		if rev[i].Key != got[j].Key || rev[i].Order != got[j].Order || !lkal.Equal(rev[i].Value, got[j].Value) {
			t.Fatalf("rev[%d] = %+v, want %+v", i, rev[i], got[j])
		}
	}

	// Empty set succeeds with empty slice.
	empty, err := orderedset.Entries(lkal.Record{})
	if err != nil || len(empty) != 0 {
		t.Fatalf("Entries(empty) = (%v, %v), want empty slice, nil", empty, err)
	}
	emptyRev, err := orderedset.EntriesReverse(lkal.Record{})
	if err != nil || len(emptyRev) != 0 {
		t.Fatalf("EntriesReverse(empty) = (%v, %v), want empty slice, nil", emptyRev, err)
	}

	// Output mutation does not alias input set.
	got[1].Value.Fields[0].Value = lkal.String("mutated")
	again, err := orderedset.Entries(pluginSet)
	if err != nil {
		t.Fatalf("Entries(again): %v", err)
	}
	if again[1].Value.Fields[0].Value.(lkal.String) != "tuning" {
		t.Fatalf("mutating Entries output mutated input record")
	}
}

func TestFromItemsRoundTripAndErrors(t *testing.T) {
	items := []orderedset.Item{
		{
			Key: "fw-pre",
			Value: rec(
				f("type", lkal.String("firewall")),
				f("policy", lkal.Enum("allow")),
			),
		},
		{
			Key: "bridge",
			Value: rec(
				f("type", lkal.String("bridge")),
				f("mtu", lkal.Int(1500)),
				f("routes", lkal.List{lkal.String("0.0.0.0/0")}),
			),
		},
		{
			Key: "fw-post",
			Value: rec(
				f("type", lkal.String("firewall")),
				f("policy", lkal.Enum("drop")),
			),
		},
		{
			Key:   "empty-payload",
			Value: rec(),
		},
	}

	set, err := orderedset.FromItems(items)
	if err != nil {
		t.Fatalf("FromItems: %v", err)
	}
	if len(set.Fields) != len(items) {
		t.Fatalf("len(set.Fields) = %d, want %d", len(set.Fields), len(items))
	}
	for i, fld := range set.Fields {
		if fld.Key != items[i].Key {
			t.Fatalf("set.Fields[%d].Key = %q, want %q", i, fld.Key, items[i].Key)
		}
		entryRec := fld.Value.(lkal.Record)
		if entryRec.Fields[0].Key != orderedset.OrderField || entryRec.Fields[0].Value != lkal.Int(orderedset.Step*int64(i+1)) {
			t.Fatalf("entry %q leading field = %+v, want order=%d", fld.Key, entryRec.Fields[0], orderedset.Step*int64(i+1))
		}
	}

	entries, err := orderedset.Entries(set)
	if err != nil {
		t.Fatalf("Entries(FromItems): %v", err)
	}
	if len(entries) != len(items) {
		t.Fatalf("len(entries) = %d, want %d", len(entries), len(items))
	}
	for i, e := range entries {
		if e.Key != items[i].Key || e.Order != orderedset.Step*int64(i+1) {
			t.Fatalf("entry[%d] = (%q, %d), want (%q, %d)", i, e.Key, e.Order, items[i].Key, orderedset.Step*int64(i+1))
		}
		if !reflect.DeepEqual(e.Value, items[i].Value) {
			t.Fatalf("entry[%d].Value = %#v, want %#v", i, e.Value, items[i].Value)
		}
	}

	emptySet, err := orderedset.FromItems(nil)
	if err != nil || len(emptySet.Fields) != 0 {
		t.Fatalf("FromItems(nil) = (%#v, %v), want empty record", emptySet, err)
	}

	errCases := []struct {
		name     string
		items    []orderedset.Item
		kind     orderedset.ErrorKind
		sentinel error
	}{
		{
			name:     "empty key",
			items:    []orderedset.Item{{Key: "", Value: rec()}},
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "dollar key",
			items:    []orderedset.Item{{Key: "$lkal", Value: rec()}},
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "slash key",
			items:    []orderedset.Item{{Key: "a/b", Value: rec()}},
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name: "duplicate nested key",
			items: []orderedset.Item{{
				Key:   "a",
				Value: rec(f("x", lkal.Int(1)), f("x", lkal.Int(2))),
			}},
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name: "duplicate key inside list element record",
			items: []orderedset.Item{{
				Key: "a",
				Value: rec(f("xs", lkal.List{
					rec(f("k", lkal.Int(1)), f("k", lkal.Int(2))),
				})),
			}},
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name: "nil value inside record",
			items: []orderedset.Item{{
				Key:   "a",
				Value: rec(f("k", nil)),
			}},
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name: "pre-existing order field",
			items: []orderedset.Item{{
				Key:   "a",
				Value: rec(f("order", lkal.Int(10))),
			}},
			kind:     orderedset.ReservedField,
			sentinel: orderedset.ErrReservedField,
		},
		{
			name: "duplicate key",
			items: []orderedset.Item{
				{Key: "a", Value: rec(f("x", lkal.Int(1)))},
				{Key: "a", Value: rec(f("x", lkal.Int(2)))},
			},
			kind:     orderedset.DuplicateKey,
			sentinel: orderedset.ErrDuplicateKey,
		},
		{
			name: "phase 1 malformed beats phase 2 reservedField and phase 3 duplicateKey",
			items: []orderedset.Item{
				{Key: "a", Value: rec(f("order", lkal.Int(1)))},
				{Key: "a", Value: rec()},
				{Key: "bad/key", Value: rec()},
			},
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name: "phase 2 reservedField beats phase 3 duplicateKey",
			items: []orderedset.Item{
				{Key: "a", Value: rec()},
				{Key: "a", Value: rec()},
				{Key: "b", Value: rec(f("order", lkal.String("even-non-int")))},
			},
			kind:     orderedset.ReservedField,
			sentinel: orderedset.ErrReservedField,
		},
	}

	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := orderedset.FromItems(tc.items)
			assertOrderErr(t, err, tc.kind, tc.sentinel)
		})
	}
}

func TestKeyedSliceKubernetesAssociativeLists(t *testing.T) {
	containers := []lkal.Record{
		rec(
			f("name", lkal.String("migrate")),
			f("image", lkal.String("acme/migrate:v1")),
			f("args", lkal.List{lkal.String("--up")}),
		),
		rec(
			f("image", lkal.String("acme/seed:v1")),
			f("name", lkal.String("seed")),
			f("timeout", lkal.Quantity{Number: "30", Unit: "s"}),
		),
		rec(
			f("name", lkal.String("app")),
			f("image", lkal.String("acme/app:v2")),
			f("creds", lkal.Ref{"secret", "db"}),
		),
	}

	set, err := orderedset.FromKeyedSlice(containers, "name")
	if err != nil {
		t.Fatalf("FromKeyedSlice: %v", err)
	}

	entries, err := orderedset.Entries(set)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	wantNames := []string{"migrate", "seed", "app"}
	for i, e := range entries {
		if e.Key != wantNames[i] || e.Order != orderedset.Step*int64(i+1) {
			t.Fatalf("entry[%d] = (%q, %d), want (%q, %d)", i, e.Key, e.Order, wantNames[i], orderedset.Step*int64(i+1))
		}
		for _, fld := range e.Value.Fields {
			if fld.Key == "name" || fld.Key == orderedset.OrderField {
				t.Fatalf("entry %q unexpectedly kept field %q", e.Key, fld.Key)
			}
		}
	}

	roundTripped, err := orderedset.ToKeyedSlice(set, "name")
	if err != nil {
		t.Fatalf("ToKeyedSlice: %v", err)
	}
	if len(roundTripped) != len(containers) {
		t.Fatalf("len(ToKeyedSlice) = %d, want %d", len(roundTripped), len(containers))
	}
	for i := range containers {
		if roundTripped[i].Fields[0].Key != "name" || roundTripped[i].Fields[0].Value != lkal.String(wantNames[i]) {
			t.Fatalf("roundTripped[%d] leading field = %+v", i, roundTripped[i].Fields[0])
		}
		if !lkal.Equal(roundTripped[i], containers[i]) {
			t.Fatalf("roundTripped[%d] != original:\ngot:  %#v\nwant: %#v", i, roundTripped[i], containers[i])
		}
	}

	revSlice, err := orderedset.ToKeyedSliceReverse(set, "name")
	if err != nil {
		t.Fatalf("ToKeyedSliceReverse: %v", err)
	}
	for i := range roundTripped {
		j := len(roundTripped) - 1 - i
		if !reflect.DeepEqual(revSlice[i], roundTripped[j]) {
			t.Fatalf("revSlice[%d] = %#v, want %#v", i, revSlice[i], roundTripped[j])
		}
	}

	// Empty round-trip.
	emptySet, err := orderedset.FromKeyedSlice(nil, "name")
	if err != nil || len(emptySet.Fields) != 0 {
		t.Fatalf("FromKeyedSlice(nil) = (%#v, %v)", emptySet, err)
	}
	emptyOut, err := orderedset.ToKeyedSlice(emptySet, "name")
	if err != nil || len(emptyOut) != 0 {
		t.Fatalf("ToKeyedSlice(empty) = (%#v, %v)", emptyOut, err)
	}

	fromErrs := []struct {
		name     string
		elems    []lkal.Record
		keyField string
		kind     orderedset.ErrorKind
		sentinel error
	}{
		{
			name:     "empty keyField",
			elems:    nil,
			keyField: "",
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "keyField is order",
			elems:    nil,
			keyField: "order",
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "keyField starts with dollar",
			elems:    nil,
			keyField: "$name",
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "keyField contains slash",
			elems:    nil,
			keyField: "meta/name",
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "missing keyField",
			elems:    []lkal.Record{rec(f("image", lkal.String("x")))},
			keyField: "name",
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "non-string keyField",
			elems:    []lkal.Record{rec(f("name", lkal.Int(1)))},
			keyField: "name",
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "invalid extracted key",
			elems:    []lkal.Record{rec(f("name", lkal.String("bad/name")))},
			keyField: "name",
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "duplicate internal field in elem",
			elems:    []lkal.Record{rec(f("name", lkal.String("a")), f("x", lkal.Int(1)), f("x", lkal.Int(2)))},
			keyField: "name",
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "pre-existing order",
			elems:    []lkal.Record{rec(f("name", lkal.String("a")), f("order", lkal.Int(10)))},
			keyField: "name",
			kind:     orderedset.ReservedField,
			sentinel: orderedset.ErrReservedField,
		},
		{
			name: "duplicate extracted keys",
			elems: []lkal.Record{
				rec(f("name", lkal.String("a")), f("image", lkal.String("1"))),
				rec(f("name", lkal.String("a")), f("image", lkal.String("2"))),
			},
			keyField: "name",
			kind:     orderedset.DuplicateKey,
			sentinel: orderedset.ErrDuplicateKey,
		},
		{
			name: "phase 1 malformed beats phase 2 reservedField and phase 3 duplicateKey",
			elems: []lkal.Record{
				rec(f("name", lkal.String("a")), f("order", lkal.Int(1))),
				rec(f("name", lkal.String("a"))),
				rec(f("image", lkal.String("missing-name"))),
			},
			keyField: "name",
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name: "phase 2 reservedField beats phase 3 duplicateKey",
			elems: []lkal.Record{
				rec(f("name", lkal.String("a"))),
				rec(f("name", lkal.String("a"))),
				rec(f("name", lkal.String("b")), f("order", lkal.Int(5))),
			},
			keyField: "name",
			kind:     orderedset.ReservedField,
			sentinel: orderedset.ErrReservedField,
		},
	}

	for _, tc := range fromErrs {
		t.Run("FromKeyedSlice/"+tc.name, func(t *testing.T) {
			_, err := orderedset.FromKeyedSlice(tc.elems, tc.keyField)
			assertOrderErr(t, err, tc.kind, tc.sentinel)
		})
	}

	toErrs := []struct {
		name     string
		set      lkal.Record
		keyField string
		kind     orderedset.ErrorKind
		sentinel error
	}{
		{
			name:     "invalid keyField on empty set",
			set:      rec(),
			keyField: "order",
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name: "duplicate order beats reservedField",
			set: rec(
				f("a", rec(f("order", lkal.Int(10)), f("name", lkal.String("clash")))),
				f("b", rec(f("order", lkal.Int(10)))),
			),
			keyField: "name",
			kind:     orderedset.DuplicateOrder,
			sentinel: orderedset.ErrDuplicateOrder,
		},
		{
			name: "entry already contains keyField",
			set: rec(
				f("a", rec(f("order", lkal.Int(10)), f("name", lkal.String("clash")))),
				f("b", rec(f("order", lkal.Int(20)))),
			),
			keyField: "name",
			kind:     orderedset.ReservedField,
			sentinel: orderedset.ErrReservedField,
		},
	}

	for _, tc := range toErrs {
		t.Run("ToKeyedSlice/"+tc.name, func(t *testing.T) {
			_, err := orderedset.ToKeyedSlice(tc.set, tc.keyField)
			assertOrderErr(t, err, tc.kind, tc.sentinel)
			_, revErr := orderedset.ToKeyedSliceReverse(tc.set, tc.keyField)
			assertOrderErr(t, revErr, tc.kind, tc.sentinel)
		})
	}
}

func TestEntriesErrorsAndPermutationInvariance(t *testing.T) {
	errCases := []struct {
		name     string
		set      lkal.Record
		kind     orderedset.ErrorKind
		sentinel error
	}{
		{
			name: "duplicate top-level entry key",
			set: rec(
				f("a", rec(f("order", lkal.Int(10)))),
				f("a", rec(f("order", lkal.Int(20)))),
			),
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name: "duplicate nested field key",
			set: rec(
				f("a", rec(f("order", lkal.Int(10)), f("x", lkal.Int(1)), f("x", lkal.Int(2)))),
			),
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name: "duplicate key inside list record",
			set: rec(
				f("a", rec(
					f("order", lkal.Int(10)),
					f("xs", lkal.List{rec(f("k", lkal.Int(1)), f("k", lkal.Int(2)))}),
				)),
			),
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "empty key",
			set:      rec(f("", rec(f("order", lkal.Int(10))))),
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "dollar key",
			set:      rec(f("$meta", rec(f("order", lkal.Int(10))))),
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "slash key",
			set:      rec(f("a/b", rec(f("order", lkal.Int(10))))),
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "non-record entry value",
			set:      rec(f("a", lkal.Int(10))),
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "missing order field",
			set:      rec(f("a", rec(f("image", lkal.String("x"))))),
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "non-int order field (string)",
			set:      rec(f("a", rec(f("order", lkal.String("10"))))),
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name:     "non-int order field (decimal)",
			set:      rec(f("a", rec(f("order", lkal.Decimal("10.0"))))),
			kind:     orderedset.Malformed,
			sentinel: orderedset.ErrMalformed,
		},
		{
			name: "duplicate order values",
			set: rec(
				f("a", rec(f("order", lkal.Int(10)))),
				f("b", rec(f("order", lkal.Int(10)))),
			),
			kind:     orderedset.DuplicateOrder,
			sentinel: orderedset.ErrDuplicateOrder,
		},
		{
			name: "duplicate negative order values",
			set: rec(
				f("a", rec(f("order", lkal.Int(math.MinInt64)))),
				f("b", rec(f("order", lkal.Int(math.MinInt64)))),
			),
			kind:     orderedset.DuplicateOrder,
			sentinel: orderedset.ErrDuplicateOrder,
		},
	}

	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := orderedset.Entries(tc.set)
			assertOrderErr(t, err, tc.kind, tc.sentinel)
			_, revErr := orderedset.EntriesReverse(tc.set)
			assertOrderErr(t, revErr, tc.kind, tc.sentinel)
		})
	}

	// Exhaustive 4! = 24 permutations of a valid set produce identical keys, orders, and lkal.Equal payloads.
	validFields := []lkal.Field{
		f("d", rec(f("order", lkal.Int(math.MaxInt64)), f("v", lkal.String("last")))),
		f("a", rec(f("v", lkal.String("first")), f("order", lkal.Int(math.MinInt64)))),
		f("c", rec(f("order", lkal.Int(0)), f("v", lkal.String("zero")))),
		f("b", rec(f("order", lkal.Int(-5)), f("v", lkal.String("neg")))),
	}
	base, err := orderedset.Entries(lkal.Record{Fields: validFields})
	if err != nil {
		t.Fatalf("base Entries: %v", err)
	}
	forEachPermutation(len(validFields), func(perm []int) {
		fields := make([]lkal.Field, len(perm))
		for i, idx := range perm {
			fields[i] = validFields[idx]
		}
		set := lkal.Record{Fields: fields}
		if !lkal.Equal(set, lkal.Record{Fields: validFields}) {
			t.Fatalf("permutation not lkal.Equal")
		}
		got, err := orderedset.Entries(set)
		if err != nil {
			t.Fatalf("Entries(perm %v): %v", perm, err)
		}
		if len(got) != len(base) {
			t.Fatalf("perm %v len=%d, want %d", perm, len(got), len(base))
		}
		for i := range base {
			if got[i].Key != base[i].Key || got[i].Order != base[i].Order || !lkal.Equal(got[i].Value, base[i].Value) {
				t.Fatalf("perm %v entry[%d]=%+v, want %+v", perm, i, got[i], base[i])
			}
		}
	})

	// Exhaustive 4! = 24 permutations of mixed error set (duplicate order + missing order) MUST always return Malformed.
	mixedErrFields := []lkal.Field{
		f("dup1", rec(f("order", lkal.Int(10)))),
		f("dup2", rec(f("order", lkal.Int(10)))),
		f("ok", rec(f("order", lkal.Int(20)))),
		f("bad", rec(f("note", lkal.String("no-order")))),
	}
	forEachPermutation(len(mixedErrFields), func(perm []int) {
		fields := make([]lkal.Field, len(perm))
		for i, idx := range perm {
			fields[i] = mixedErrFields[idx]
		}
		_, err := orderedset.Entries(lkal.Record{Fields: fields})
		assertOrderErr(t, err, orderedset.Malformed, orderedset.ErrMalformed)
	})
}

func TestSparseOrderHelpersAndRenumber(t *testing.T) {
	beforeCases := []struct {
		in      int64
		want    int64
		wantErr bool
	}{
		{10, 0, false},
		{0, -10, false},
		{-10, -20, false},
		{math.MaxInt64, math.MaxInt64 - 10, false},
		{math.MinInt64 + 10, math.MinInt64, false},
		{math.MinInt64 + 9, 0, true},
		{math.MinInt64, 0, true},
	}
	for _, tc := range beforeCases {
		got, err := orderedset.OrderBefore(tc.in)
		if tc.wantErr {
			assertOrderErr(t, err, orderedset.Overflow, orderedset.ErrOverflow)
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("OrderBefore(%d) = (%d, %v), want (%d, nil)", tc.in, got, err, tc.want)
		}
	}

	afterCases := []struct {
		in      int64
		want    int64
		wantErr bool
	}{
		{0, 10, false},
		{-10, 0, false},
		{math.MinInt64, math.MinInt64 + 10, false},
		{math.MaxInt64 - 10, math.MaxInt64, false},
		{math.MaxInt64 - 9, 0, true},
		{math.MaxInt64, 0, true},
	}
	for _, tc := range afterCases {
		got, err := orderedset.OrderAfter(tc.in)
		if tc.wantErr {
			assertOrderErr(t, err, orderedset.Overflow, orderedset.ErrOverflow)
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("OrderAfter(%d) = (%d, %v), want (%d, nil)", tc.in, got, err, tc.want)
		}
	}

	betweenCases := []struct {
		lo, hi   int64
		want     int64
		kind     orderedset.ErrorKind
		sentinel error
	}{
		{10, 20, 15, 0, nil},
		{10, 15, 12, 0, nil},
		{10, 12, 11, 0, nil},
		{10, 11, 0, orderedset.GapExhausted, orderedset.ErrGapExhausted},
		{-3, 0, -2, 0, nil},
		{-2, 0, -1, 0, nil},
		{-1, 1, 0, 0, nil},
		{-10, -1, -6, 0, nil},
		{-1, 0, 0, orderedset.GapExhausted, orderedset.ErrGapExhausted},
		{-2, -1, 0, orderedset.GapExhausted, orderedset.ErrGapExhausted},
		{math.MinInt64, math.MaxInt64, -1, 0, nil},
		{math.MinInt64, math.MinInt64 + 2, math.MinInt64 + 1, 0, nil},
		{math.MaxInt64 - 2, math.MaxInt64, math.MaxInt64 - 1, 0, nil},
		{math.MinInt64, math.MinInt64 + 1, 0, orderedset.GapExhausted, orderedset.ErrGapExhausted},
		{math.MaxInt64 - 1, math.MaxInt64, 0, orderedset.GapExhausted, orderedset.ErrGapExhausted},
		{10, 10, 0, orderedset.Malformed, orderedset.ErrMalformed},
		{20, 10, 0, orderedset.Malformed, orderedset.ErrMalformed},
		{math.MinInt64, math.MinInt64, 0, orderedset.Malformed, orderedset.ErrMalformed},
		{math.MaxInt64, math.MinInt64, 0, orderedset.Malformed, orderedset.ErrMalformed},
	}
	for _, tc := range betweenCases {
		got, err := orderedset.OrderBetween(tc.lo, tc.hi)
		if tc.sentinel != nil {
			assertOrderErr(t, err, tc.kind, tc.sentinel)
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("OrderBetween(%d, %d) = (%d, %v), want (%d, nil)", tc.lo, tc.hi, got, err, tc.want)
		}
		if tc.lo >= got || got >= tc.hi {
			t.Errorf("OrderBetween(%d, %d) = %d violates strict bounds", tc.lo, tc.hi, got)
		}
	}

	// Simulate repeated midpoint insertions between 10 and 20 until gap exhaustion, then Renumber.
	set, err := orderedset.FromItems([]orderedset.Item{
		{Key: "a", Value: rec(f("type", lkal.String("bridge")))},
		{Key: "b", Value: rec(f("type", lkal.String("portmap")))},
	})
	if err != nil {
		t.Fatalf("FromItems: %v", err)
	}
	hi := int64(20)
	for _, k := range []string{"c", "d", "e"} {
		mid, err := orderedset.OrderBetween(10, hi)
		if err != nil {
			t.Fatalf("OrderBetween(10, %d): %v", hi, err)
		}
		set.Fields = append(set.Fields, f(k, rec(f("order", lkal.Int(mid)), f("type", lkal.String("firewall")))))
		hi = mid
	}
	if hi != 11 {
		t.Fatalf("after 3 midpoint insertions hi = %d, want 11", hi)
	}
	if _, err := orderedset.OrderBetween(10, hi); !errors.Is(err, orderedset.ErrGapExhausted) {
		t.Fatalf("OrderBetween(10, 11) = %v, want ErrGapExhausted", err)
	}

	renumbered, err := orderedset.Renumber(set)
	if err != nil {
		t.Fatalf("Renumber: %v", err)
	}
	entries, err := orderedset.Entries(renumbered)
	if err != nil {
		t.Fatalf("Entries(renumbered): %v", err)
	}
	wantOrder := []string{"a", "e", "d", "c", "b"}
	if len(entries) != len(wantOrder) {
		t.Fatalf("len(renumbered) = %d, want %d", len(entries), len(wantOrder))
	}
	for i, e := range entries {
		if e.Key != wantOrder[i] || e.Order != orderedset.Step*int64(i+1) {
			t.Fatalf("renumbered[%d] = (%q, %d), want (%q, %d)", i, e.Key, e.Order, wantOrder[i], orderedset.Step*int64(i+1))
		}
	}
	if mid, err := orderedset.OrderBetween(entries[0].Order, entries[1].Order); err != nil || mid != 15 {
		t.Fatalf("post-renumber OrderBetween(10, 20) = (%d, %v), want (15, nil)", mid, err)
	}

	// Renumber propagates Entries errors.
	_, err = orderedset.Renumber(rec(f("a", rec(f("order", lkal.Int(1)))), f("b", rec(f("order", lkal.Int(1))))))
	assertOrderErr(t, err, orderedset.DuplicateOrder, orderedset.ErrDuplicateOrder)
	_, err = orderedset.Renumber(rec(f("a", rec())))
	assertOrderErr(t, err, orderedset.Malformed, orderedset.ErrMalformed)
}

func forEachPermutation(n int, fn func([]int)) {
	p := make([]int, n)
	for i := range n {
		p[i] = i
	}
	var gen func(int)
	gen = func(k int) {
		if k == n {
			fn(slices.Clone(p))
			return
		}
		for i := k; i < n; i++ {
			p[k], p[i] = p[i], p[k]
			gen(k + 1)
			p[k], p[i] = p[i], p[k]
		}
	}
	gen(0)
}
