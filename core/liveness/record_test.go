package liveness

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/liveness/detector"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

func TestRecordEncodeDecodeRoundTrip(t *testing.T) {
	states := []detector.State{
		detector.StatePending,
		detector.StateReady,
		detector.StateNotReady,
	}
	flags := []bool{false, true}
	sinces := []int64{0, 1700000000000, maxSafeInt - 1}
	racks := []string{"r1", "rack-a1", strings.Repeat("a", 63)}

	for _, st := range states {
		for _, evictable := range flags {
			for _, since := range sinces {
				for _, rack := range racks {
					want := Record{
						Node:      "node-1.rack-a",
						State:     st,
						Since:     since,
						Rack:      rack,
						Evictable: evictable,
						ModRev:    42,
					}
					enc, err := encodeRecord(want)
					if err != nil {
						t.Fatalf("encodeRecord(%+v): %v", want, err)
					}
					got, err := DecodeRecord(want.Node, enc, want.ModRev)
					if err != nil {
						t.Fatalf("DecodeRecord(%q): %v", enc, err)
					}
					if got != want {
						t.Fatalf("round trip got %+v, want %+v", got, want)
					}
				}
			}
		}
	}
}

func TestRecordGoldenValues(t *testing.T) {
	tests := []struct {
		name string
		rec  Record
		want string
	}{
		{
			name: "pending",
			rec: Record{
				Node:      "n1",
				State:     detector.StatePending,
				Since:     1700000000000,
				Rack:      "r1",
				Evictable: false,
			},
			want: `{"$lkal":"0.1","evictable":false,"rack":"r1","since":1700000000000,"state":{"$enum":"pending"}}`,
		},
		{
			name: "ready",
			rec: Record{
				Node:      "n1",
				State:     detector.StateReady,
				Since:     1700000000000,
				Rack:      "r1",
				Evictable: false,
			},
			want: `{"$lkal":"0.1","evictable":false,"rack":"r1","since":1700000000000,"state":{"$enum":"ready"}}`,
		},
		{
			name: "notready-evictable",
			rec: Record{
				Node:      "n1",
				State:     detector.StateNotReady,
				Since:     1700000000000,
				Rack:      "r1",
				Evictable: true,
			},
			want: `{"$lkal":"0.1","evictable":true,"rack":"r1","since":1700000000000,"state":{"$enum":"notready"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodeRecord(tc.rec)
			if err != nil {
				t.Fatalf("encodeRecord: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("encodeRecord = %s, want %s", got, tc.want)
			}
			dec, err := DecodeRecord(tc.rec.Node, []byte(tc.want), 9)
			if err != nil {
				t.Fatalf("DecodeRecord: %v", err)
			}
			wantRec := tc.rec
			wantRec.ModRev = 9
			if dec != wantRec {
				t.Fatalf("DecodeRecord = %+v, want %+v", dec, wantRec)
			}
		})
	}
}

func TestDecodeRecordRejections(t *testing.T) {
	badValues := []struct {
		name string
		raw  string
	}{
		{"empty", ``},
		{"null", `null`},
		{"not-json", `not json`},
		{"empty-object", `{}`},
		{"unknown-member", `{"$lkal":"0.1","evictable":false,"extra":1,"rack":"r1","since":0,"state":{"$enum":"ready"}}`},
		{"unknown-state-member", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":"ready","extra":1}}`},
		{"missing-lkal", `{"evictable":false,"rack":"r1","since":0,"state":{"$enum":"ready"}}`},
		{"missing-evictable", `{"$lkal":"0.1","rack":"r1","since":0,"state":{"$enum":"ready"}}`},
		{"missing-rack", `{"$lkal":"0.1","evictable":false,"since":0,"state":{"$enum":"ready"}}`},
		{"missing-since", `{"$lkal":"0.1","evictable":false,"rack":"r1","state":{"$enum":"ready"}}`},
		{"missing-state", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":0}`},
		{"missing-enum", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{}}`},
		{"null-lkal", `{"$lkal":null,"evictable":false,"rack":"r1","since":0,"state":{"$enum":"ready"}}`},
		{"null-evictable", `{"$lkal":"0.1","evictable":null,"rack":"r1","since":0,"state":{"$enum":"ready"}}`},
		{"null-rack", `{"$lkal":"0.1","evictable":false,"rack":null,"since":0,"state":{"$enum":"ready"}}`},
		{"null-since", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":null,"state":{"$enum":"ready"}}`},
		{"null-state", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":null}`},
		{"null-enum", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":null}}`},
		{"wrong-lkal", `{"$lkal":"0.2","evictable":false,"rack":"r1","since":0,"state":{"$enum":"ready"}}`},
		{"unknown-state", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":"active"}}`},
		{"empty-state", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":""}}`},
		{"fractional-since", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":1.5,"state":{"$enum":"ready"}}`},
		{"decimal-zero-since", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":1.0,"state":{"$enum":"ready"}}`},
		{"exponent-since", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":1e2,"state":{"$enum":"ready"}}`},
		{"string-since", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":"0","state":{"$enum":"ready"}}`},
		{"negative-since", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":-1,"state":{"$enum":"ready"}}`},
		{"too-large-since", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":9007199254740992,"state":{"$enum":"ready"}}`},
		{"empty-rack", `{"$lkal":"0.1","evictable":false,"rack":"","since":0,"state":{"$enum":"ready"}}`},
		{"leading-dash-rack", `{"$lkal":"0.1","evictable":false,"rack":"-r1","since":0,"state":{"$enum":"ready"}}`},
		{"trailing-dash-rack", `{"$lkal":"0.1","evictable":false,"rack":"r1-","since":0,"state":{"$enum":"ready"}}`},
		{"uppercase-rack", `{"$lkal":"0.1","evictable":false,"rack":"R1","since":0,"state":{"$enum":"ready"}}`},
		{"dot-rack", `{"$lkal":"0.1","evictable":false,"rack":"r.1","since":0,"state":{"$enum":"ready"}}`},
		{"underscore-rack", `{"$lkal":"0.1","evictable":false,"rack":"r_1","since":0,"state":{"$enum":"ready"}}`},
		{"too-long-rack", `{"$lkal":"0.1","evictable":false,"rack":"` + strings.Repeat("a", 64) + `","since":0,"state":{"$enum":"ready"}}`},
		{"trailing-garbage", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":"ready"}} trailing`},
		{"trailing-object", `{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":"ready"}} {}`},
		{"duplicate-member", `{"$lkal":"0.1","$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":"ready"}}`},
		{"whitespace", `{ "$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":"ready"}}`},
		{"wrong-order", `{"$lkal":"0.1","rack":"r1","evictable":false,"since":0,"state":{"$enum":"ready"}}`},
	}

	for _, tc := range badValues {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeRecord("n1", []byte(tc.raw), 1)
			if !errors.Is(err, ErrCorruptRecord) {
				t.Fatalf("DecodeRecord(%q) = %v, want ErrCorruptRecord", tc.raw, err)
			}
			if !strings.Contains(err.Error(), "/sys/nodes/n1") {
				t.Fatalf("DecodeRecord error %q missing key context", err.Error())
			}
		})
	}

	if _, err := DecodeRecord("BAD", []byte(`{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":"ready"}}`), 1); !errors.Is(err, keyspace.ErrInvalid) {
		t.Fatalf("DecodeRecord with invalid node = %v, want keyspace.ErrInvalid", err)
	}
}

func TestBudgetEncodeDecodeRoundTripAndGolden(t *testing.T) {
	cases := []struct {
		name    string
		granted []int64
		want    string
	}{
		{"empty-nil", nil, `{"$lkal":"0.1","granted":[]}`},
		{"empty-slice", []int64{}, `{"$lkal":"0.1","granted":[]}`},
		{"single", []int64{0}, `{"$lkal":"0.1","granted":[0]}`},
		{"golden", []int64{1700000000000, 1700000000500}, `{"$lkal":"0.1","granted":[1700000000000,1700000000500]}`},
		{"equal-entries", []int64{500, 500, maxSafeInt - 1}, `{"$lkal":"0.1","granted":[500,500,9007199254740991]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc, err := encodeBudget(tc.granted)
			if err != nil {
				t.Fatalf("encodeBudget(%v): %v", tc.granted, err)
			}
			if string(enc) != tc.want {
				t.Fatalf("encodeBudget = %s, want %s", enc, tc.want)
			}
			dec, err := decodeBudget(keyspace.EvictionBudgetKey, enc)
			if err != nil {
				t.Fatalf("decodeBudget(%s): %v", enc, err)
			}
			if len(dec) != len(tc.granted) {
				t.Fatalf("decodeBudget len = %d, want %d", len(dec), len(tc.granted))
			}
			for i := range dec {
				if dec[i] != tc.granted[i] {
					t.Fatalf("decodeBudget[%d] = %d, want %d", i, dec[i], tc.granted[i])
				}
			}
			reenc, err := encodeBudget(dec)
			if err != nil || !bytes.Equal(reenc, enc) {
				t.Fatalf("re-encode = (%s, %v), want %s", reenc, err, enc)
			}
		})
	}
}

func TestDecodeBudgetRejections(t *testing.T) {
	badValues := []struct {
		name string
		raw  string
	}{
		{"empty", ``},
		{"null", `null`},
		{"not-json", `not json`},
		{"empty-object", `{}`},
		{"unknown-member", `{"$lkal":"0.1","extra":1,"granted":[]}`},
		{"missing-lkal", `{"granted":[]}`},
		{"missing-granted", `{"$lkal":"0.1"}`},
		{"null-granted", `{"$lkal":"0.1","granted":null}`},
		{"null-element", `{"$lkal":"0.1","granted":[1,null]}`},
		{"wrong-lkal", `{"$lkal":"0.2","granted":[]}`},
		{"fractional-entry", `{"$lkal":"0.1","granted":[1.5]}`},
		{"decimal-entry", `{"$lkal":"0.1","granted":[1.0]}`},
		{"exponent-entry", `{"$lkal":"0.1","granted":[1e2]}`},
		{"string-entry", `{"$lkal":"0.1","granted":["100"]}`},
		{"negative-entry", `{"$lkal":"0.1","granted":[-1]}`},
		{"too-large-entry", `{"$lkal":"0.1","granted":[9007199254740992]}`},
		{"descending", `{"$lkal":"0.1","granted":[200,100]}`},
		{"trailing-data", `{"$lkal":"0.1","granted":[]} trailing`},
		{"whitespace", `{"$lkal":"0.1","granted": []}`},
		{"wrong-order", `{"granted":[],"$lkal":"0.1"}`},
	}

	for _, tc := range badValues {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeBudget(keyspace.EvictionBudgetKey, []byte(tc.raw))
			if !errors.Is(err, ErrCorruptRecord) {
				t.Fatalf("decodeBudget(%q) = %v, want ErrCorruptRecord", tc.raw, err)
			}
			if !strings.Contains(err.Error(), keyspace.EvictionBudgetKey) {
				t.Fatalf("decodeBudget error %q missing key context", err.Error())
			}
		})
	}
}

func requireRev(*store.Rev) {}

func TestRecordModRevIsStoreRev(t *testing.T) {
	var r Record
	requireRev(&r.ModRev)
}
