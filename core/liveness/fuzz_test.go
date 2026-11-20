package liveness

import (
	"bytes"
	"errors"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

func FuzzDecodeRecord(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":"pending"}}`),
		[]byte(`{"$lkal":"0.1","evictable":false,"rack":"r1","since":1700000000000,"state":{"$enum":"ready"}}`),
		[]byte(`{"$lkal":"0.1","evictable":true,"rack":"rack-1","since":9007199254740991,"state":{"$enum":"notready"}}`),
		[]byte(`{ "$lkal" : "0.1" , "evictable" : false , "rack" : "r1" , "since" : 0 , "state" : { "$enum" : "ready" } }`),
		[]byte(`{}`),
		[]byte(`{"$lkal":"0.2","evictable":false,"rack":"r1","since":0,"state":{"$enum":"ready"}}`),
		[]byte(`{"$lkal":"0.1","evictable":false,"rack":"r1","since":-1,"state":{"$enum":"ready"}}`),
		[]byte(`{"$lkal":"0.1","evictable":false,"rack":"r1","since":9007199254740992,"state":{"$enum":"ready"}}`),
		[]byte(`{"$lkal":"0.1","evictable":false,"rack":"BAD","since":0,"state":{"$enum":"ready"}}`),
		[]byte(`{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":"other"}}`),
		[]byte(`{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":"ready"}} {}`),
		[]byte(`null`),
		[]byte(``),
		{0xff, 0xfe, 0xfd},
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		rec, err := DecodeRecord("node-1", data, 7)
		if err != nil {
			if !errors.Is(err, ErrCorruptRecord) {
				t.Fatalf("DecodeRecord error = %v, want ErrCorruptRecord", err)
			}
			return
		}
		got, err := encodeRecord(rec)
		if err != nil {
			t.Fatalf("encodeRecord(%+v) failed after DecodeRecord: %v", rec, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("re-encoded bytes = %q, want %q", got, data)
		}
	})
}

func FuzzDecodeBudget(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"$lkal":"0.1","granted":[]}`),
		[]byte(`{"$lkal":"0.1","granted":[0]}`),
		[]byte(`{"$lkal":"0.1","granted":[100,100]}`),
		[]byte(`{"$lkal":"0.1","granted":[1700000000000,1700000000500]}`),
		[]byte(`{"$lkal":"0.1","granted": [1700000000000]}`),
		[]byte(`{}`),
		[]byte(`{"$lkal":"0.1","granted":null}`),
		[]byte(`{"$lkal":"0.1","granted":[200,100]}`),
		[]byte(`{"$lkal":"0.1","granted":[-1]}`),
		[]byte(`{"$lkal":"0.1","granted":[9007199254740992]}`),
		[]byte(`{"$lkal":"0.1","granted":[]} {}`),
		[]byte(`null`),
		[]byte(``),
		{0xff, 0xfe, 0xfd},
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		granted, err := decodeBudget(keyspace.EvictionBudgetKey, data)
		if err != nil {
			if !errors.Is(err, ErrCorruptRecord) {
				t.Fatalf("decodeBudget error = %v, want ErrCorruptRecord", err)
			}
			return
		}
		got, err := encodeBudget(granted)
		if err != nil {
			t.Fatalf("encodeBudget(%v) failed after decodeBudget: %v", granted, err)
		}
		if !bytes.Equal(got, data) {
			t.Fatalf("re-encoded bytes = %q, want %q", got, data)
		}
	})
}
