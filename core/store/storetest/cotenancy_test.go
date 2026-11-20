package storetest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
)

type failingTxnStore struct {
	store.Store
	failPrefix string
	err        error
}

func (s failingTxnStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	if s.failPrefix == "" || (len(ops) > 0 && strings.HasPrefix(ops[0].Key, s.failPrefix)) {
		return false, 0, s.err
	}
	return s.Store.Txn(ctx, cmps, ops)
}

func TestMeasureCotenancy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	report, err := MeasureCotenancy(ctx, memstore.New(), CotenancyLoad{
		Duration:      200 * time.Millisecond,
		StrongRate:    50,
		HotWriters:    2,
		HotKeys:       8,
		HotValueBytes: 64,
	})
	if err != nil {
		t.Fatalf("MeasureCotenancy: %v", err)
	}
	t.Log("\n" + report.String())

	if report.Baseline.HotWrites != 0 {
		t.Fatalf("Baseline.HotWrites = %d, want 0", report.Baseline.HotWrites)
	}
	if report.Loaded.HotWrites <= 0 {
		t.Fatalf("Loaded.HotWrites = %d, want > 0", report.Loaded.HotWrites)
	}

	for _, tc := range []struct {
		name  string
		phase CotenancyPhase
	}{
		{"Baseline", report.Baseline},
		{"Loaded", report.Loaded},
	} {
		if tc.phase.Txn.Count <= 0 {
			t.Fatalf("%s.Txn.Count = %d, want > 0", tc.name, tc.phase.Txn.Count)
		}
		if tc.phase.Watch.Count != tc.phase.Txn.Count {
			t.Fatalf("%s.Watch.Count = %d, want %d", tc.name, tc.phase.Watch.Count, tc.phase.Txn.Count)
		}
		for _, lat := range []struct {
			kind string
			val  Latencies
		}{
			{"Txn", tc.phase.Txn},
			{"Watch", tc.phase.Watch},
		} {
			if lat.val.Count <= 0 {
				t.Fatalf("%s.%s.Count = %d, want > 0", tc.name, lat.kind, lat.val.Count)
			}
			if lat.val.P50 > lat.val.P99 || lat.val.P99 > lat.val.Max {
				t.Fatalf("%s.%s not ordered: p50=%s p99=%s max=%s",
					tc.name, lat.kind, lat.val.P50, lat.val.P99, lat.val.Max)
			}
		}
	}

	lines := strings.Split(report.String(), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "baseline") || !strings.Contains(lines[1], "loaded") {
		t.Fatalf("unexpected report format:\n%s", report.String())
	}
}

func TestMeasureCotenancyReturnsTxnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wantErr := errors.New("injected txn failure")
	for _, tc := range []struct {
		name       string
		failPrefix string
	}{
		{"strong write failure", ""},
		{"hot write failure", "hot/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := failingTxnStore{Store: memstore.New(), failPrefix: tc.failPrefix, err: wantErr}
			_, err := MeasureCotenancy(ctx, s, CotenancyLoad{
				Duration:      50 * time.Millisecond,
				StrongRate:    20,
				HotWriters:    1,
				HotKeys:       4,
				HotValueBytes: 16,
			})
			if !errors.Is(err, wantErr) {
				t.Fatalf("MeasureCotenancy error = %v, want %v", err, wantErr)
			}
		})
	}
}

func TestSummarizeLatencies(t *testing.T) {
	if got := summarizeLatencies(nil); got != (Latencies{}) {
		t.Fatalf("empty summarizeLatencies = %+v, want zero", got)
	}

	if got := summarizeLatencies([]time.Duration{7 * time.Millisecond}); got != (Latencies{
		Count: 1,
		P50:   7 * time.Millisecond,
		P99:   7 * time.Millisecond,
		Max:   7 * time.Millisecond,
	}) {
		t.Fatalf("single sample summarizeLatencies = %+v", got)
	}

	two := summarizeLatencies([]time.Duration{20 * time.Millisecond, 10 * time.Millisecond})
	if two != (Latencies{Count: 2, P50: 10 * time.Millisecond, P99: 20 * time.Millisecond, Max: 20 * time.Millisecond}) {
		t.Fatalf("two sample summarizeLatencies = %+v", two)
	}

	hundred := make([]time.Duration, 100)
	for i := range hundred {
		hundred[i] = time.Duration(100-i) * time.Millisecond
	}
	got := summarizeLatencies(hundred)
	want := Latencies{
		Count: 100,
		P50:   50 * time.Millisecond,
		P99:   99 * time.Millisecond,
		Max:   100 * time.Millisecond,
	}
	if got != want {
		t.Fatalf("100 sample summarizeLatencies = %+v, want %+v", got, want)
	}
	if hundred[0] != 100*time.Millisecond {
		t.Fatal("summarizeLatencies mutated caller slice")
	}
}
