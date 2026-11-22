package resource_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
)

type seqRand struct {
	mu  sync.Mutex
	ctr byte
}

func (s *seqRand) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctr++
	for i := range p {
		p[i] = s.ctr + byte(i)
	}
	return len(p), nil
}

type failRand struct {
	err error
}

func (f failRand) Read([]byte) (int, error) {
	return 0, f.err
}

type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fixedClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

var defaultTestSchemas = func() resource.Schemas {
	workload, err := resource.CompileKindSchema("workload.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"

root {
  field "spec" {
    type = .record
    field "replicas" { type = .int }
    field "ratio" { type = .decimal }
    field "mem" { type = .quantity }
  }
  field "status" {
    type = .record
    field "phase" { type = .string }
    field "ratio" { type = .decimal }
    field "mem" { type = .quantity }
  }
}
`))
	if err != nil {
		panic(err)
	}
	return resource.Schemas{"workload": workload}
}()

func testObjectsConfig() (resource.Config, *fixedClock) {
	clk := &fixedClock{t: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	return resource.Config{
		Now:     clk.Now,
		Rand:    &seqRand{},
		Schemas: defaultTestSchemas,
	}, clk
}

func workloadCand(name string, replicas int64, labels ...lkal.Field) lkal.Record {
	metaFields := []lkal.Field{
		f("kind", lkal.String("workload")),
		f("name", lkal.String(name)),
	}
	if len(labels) > 0 {
		metaFields = append(metaFields, f("labels", rec(labels...)))
	}
	return rec(
		f("meta", rec(metaFields...)),
		f("spec", rec(f("replicas", lkal.Int(replicas)))),
	)
}

func getField(r lkal.Record, key string) (lkal.Value, bool) {
	for _, fld := range r.Fields {
		if fld.Key == key {
			return fld.Value, true
		}
	}
	return nil, false
}

func getRecordField(r lkal.Record, key string) (lkal.Record, bool) {
	v, ok := getField(r, key)
	if !ok {
		return lkal.Record{}, false
	}
	sub, ok := v.(lkal.Record)
	return sub, ok
}

func getStringField(r lkal.Record, key string) string {
	v, ok := getField(r, key)
	if !ok {
		return ""
	}
	s, _ := v.(lkal.String)
	return string(s)
}

func getIntField(r lkal.Record, key string) int64 {
	v, ok := getField(r, key)
	if !ok {
		return 0
	}
	n, _ := v.(lkal.Int)
	return int64(n)
}

func replaceMetaField(doc lkal.Record, key string, val lkal.Value) lkal.Record {
	out := make([]lkal.Field, len(doc.Fields))
	for i, fld := range doc.Fields {
		if fld.Key != "meta" {
			out[i] = fld
			continue
		}
		meta := fld.Value.(lkal.Record)
		mFields := make([]lkal.Field, 0, len(meta.Fields)+1)
		replaced := false
		for _, mf := range meta.Fields {
			if mf.Key == key {
				mFields = append(mFields, f(key, val))
				replaced = true
			} else {
				mFields = append(mFields, mf)
			}
		}
		if !replaced {
			mFields = append(mFields, f(key, val))
		}
		out[i] = f("meta", rec(mFields...))
	}
	return rec(out...)
}

func isCanonicalUUIDv4(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range len(s) {
		b := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if b != '-' {
				return false
			}
			continue
		}
		if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')) {
			return false
		}
	}
	if s[14] != '4' {
		return false
	}
	switch s[19] {
	case '8', '9', 'a', 'b':
		return true
	default:
		return false
	}
}

func checkObjectsCore(t *testing.T, newStore func(*testing.T) tenant.Store) {
	t.Helper()

	t.Run("CreateGetUpdateDeleteLifecycle", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		st := newStore(t)
		cfg, clk := testObjectsConfig()
		objs := resource.NewObjects(st, cfg)

		objID := keyspace.Object{Group: "core", Kind: "workload", Name: "web"}
		if _, err := objs.Get(ctx, objID); !errors.Is(err, resource.ErrNotFound) {
			t.Fatalf("Get(missing) = %v, want ErrNotFound", err)
		}

		created, err := objs.Create(ctx, refs.Principal{}, workloadCand("web", 2, f("env", lkal.String("dev"))))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		meta, _ := getRecordField(created, "meta")
		uid := getStringField(meta, "uid")
		if !isCanonicalUUIDv4(uid) {
			t.Fatalf("uid = %q, want canonical UUIDv4", uid)
		}
		if gen := getIntField(meta, "generation"); gen != 1 {
			t.Fatalf("generation = %d, want 1", gen)
		}
		rev1 := getIntField(meta, "revision")
		if rev1 < 1 {
			t.Fatalf("revision = %d, want >= 1", rev1)
		}

		got, err := objs.Get(ctx, objID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !lkal.Equal(got, created) {
			t.Fatalf("Get != Create:\ngot:  %#v\nwant: %#v", got, created)
		}

		// Duplicate create -> ErrExists.
		if _, err := objs.Create(ctx, refs.Principal{}, workloadCand("web", 5)); !errors.Is(err, resource.ErrExists) {
			t.Fatalf("duplicate Create = %v, want ErrExists", err)
		}

		// Echoing Get result into Update is a no-op and does not move revision.
		curBefore, err := st.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		echoed, err := objs.Update(ctx, refs.Principal{}, got)
		if err != nil {
			t.Fatalf("echo Update: %v", err)
		}
		curAfter, err := st.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if curAfter != curBefore {
			t.Fatalf("no-op Update moved store revision: %d -> %d", curBefore, curAfter)
		}
		if !lkal.Equal(echoed, got) {
			t.Fatalf("echo Update = %#v, want %#v", echoed, got)
		}

		// Labels-only change keeps generation 1 and bumps revision.
		labelsOnly := workloadCand("web", 2, f("env", lkal.String("prod")))
		labelsOnly = replaceMetaField(labelsOnly, "revision", lkal.Int(rev1))
		afterLabels, err := objs.Update(ctx, refs.Principal{}, labelsOnly)
		if err != nil {
			t.Fatalf("labels-only Update: %v", err)
		}
		metaLabels, _ := getRecordField(afterLabels, "meta")
		if getIntField(metaLabels, "generation") != 1 {
			t.Fatalf("labels-only generation = %d, want 1", getIntField(metaLabels, "generation"))
		}
		rev2 := getIntField(metaLabels, "revision")
		if rev2 <= rev1 {
			t.Fatalf("labels-only revision = %d, want > %d", rev2, rev1)
		}

		// Spec change bumps generation to 2 and bumps revision.
		specChange := workloadCand("web", 4, f("env", lkal.String("prod")))
		afterSpec, err := objs.Update(ctx, refs.Principal{}, specChange)
		if err != nil {
			t.Fatalf("spec Update: %v", err)
		}
		metaSpec, _ := getRecordField(afterSpec, "meta")
		if getIntField(metaSpec, "generation") != 2 {
			t.Fatalf("spec Update generation = %d, want 2", getIntField(metaSpec, "generation"))
		}
		rev3 := getIntField(metaSpec, "revision")
		if rev3 <= rev2 {
			t.Fatalf("spec Update revision = %d, want > %d", rev3, rev2)
		}

		// Stale revision -> ErrConflict and writes nothing.
		curBefore, err = st.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		staleCand := workloadCand("web", 99)
		staleCand = replaceMetaField(staleCand, "revision", lkal.Int(rev1))
		if _, err := objs.Update(ctx, refs.Principal{}, staleCand); !errors.Is(err, envelope.ErrConflict) {
			t.Fatalf("stale Update = %v, want ErrConflict", err)
		}
		if curAfter, err := st.CurrentRev(ctx); err != nil || curAfter != curBefore {
			t.Fatalf("stale Update moved store revision: %d -> %d (err=%v)", curBefore, curAfter, err)
		}

		// Forbidden uid / generation / status changes in Update.
		badUID := replaceMetaField(afterSpec, "uid", lkal.String("00000000-0000-4000-8000-000000000000"))
		if _, err := objs.Update(ctx, refs.Principal{}, badUID); !errors.Is(err, envelope.ErrForbidden) {
			t.Fatalf("Update(different uid) = %v, want ErrForbidden", err)
		}
		badGen := replaceMetaField(afterSpec, "generation", lkal.Int(99))
		if _, err := objs.Update(ctx, refs.Principal{}, badGen); !errors.Is(err, envelope.ErrForbidden) {
			t.Fatalf("Update(different generation) = %v, want ErrForbidden", err)
		}
		badStatus := rec(append(slices.Clone(afterSpec.Fields), f("status", rec(f("phase", lkal.String("Bad")))))...)
		if _, err := objs.Update(ctx, refs.Principal{}, badStatus); !errors.Is(err, envelope.ErrForbidden) {
			t.Fatalf("Update(status change) = %v, want ErrForbidden", err)
		}

		// UpdateStatus stamps since from injected clock, keeps generation 2.
		t1 := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
		clk.Set(t1)
		statusCand1 := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
			f("status", rec(
				f("observedGeneration", lkal.Int(2)),
				f("condition", rec(
					f("Ready", rec(f("state", lkal.Enum("false")), f("reason", lkal.String("Progressing")))),
				)),
			)),
		)
		st1, err := objs.UpdateStatus(ctx, statusCand1)
		if err != nil {
			t.Fatalf("UpdateStatus(1): %v", err)
		}
		st1Meta, _ := getRecordField(st1, "meta")
		if getIntField(st1Meta, "generation") != 2 {
			t.Fatalf("UpdateStatus changed generation to %d, want 2", getIntField(st1Meta, "generation"))
		}
		st1Status, _ := getRecordField(st1, "status")
		st1Cond, _ := getRecordField(st1Status, "condition")
		st1Ready, _ := getRecordField(st1Cond, "Ready")
		if gotSince := getStringField(st1Ready, "since"); gotSince != "2026-04-01T12:00:00Z" {
			t.Fatalf("since = %q, want 2026-04-01T12:00:00Z", gotSince)
		}

		// Same state -> keeps since even after clock advances; different reason still triggers write.
		t2 := time.Date(2026, 4, 1, 13, 0, 0, 0, time.UTC)
		clk.Set(t2)
		statusCand2 := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
			f("status", rec(
				f("observedGeneration", lkal.Int(2)),
				f("condition", rec(
					f("Ready", rec(f("state", lkal.Enum("false")), f("reason", lkal.String("StillProgressing")))),
				)),
			)),
		)
		st2, err := objs.UpdateStatus(ctx, statusCand2)
		if err != nil {
			t.Fatalf("UpdateStatus(2): %v", err)
		}
		st2Status, _ := getRecordField(st2, "status")
		st2Cond, _ := getRecordField(st2Status, "condition")
		st2Ready, _ := getRecordField(st2Cond, "Ready")
		if gotSince := getStringField(st2Ready, "since"); gotSince != "2026-04-01T12:00:00Z" {
			t.Fatalf("unchanged state restamped since to %q, want 2026-04-01T12:00:00Z", gotSince)
		}

		// Flipped state -> restamps since to t3.
		t3 := time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC)
		clk.Set(t3)
		statusCand3 := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
			f("status", rec(
				f("observedGeneration", lkal.Int(2)),
				f("condition", rec(
					f("Ready", rec(f("state", lkal.Enum("true")), f("reason", lkal.String("Available")))),
				)),
			)),
		)
		st3, err := objs.UpdateStatus(ctx, statusCand3)
		if err != nil {
			t.Fatalf("UpdateStatus(3): %v", err)
		}
		st3Meta, _ := getRecordField(st3, "meta")
		if getIntField(st3Meta, "generation") != 2 {
			t.Fatalf("UpdateStatus(3) generation = %d, want 2", getIntField(st3Meta, "generation"))
		}
		st3Status, _ := getRecordField(st3, "status")
		st3Cond, _ := getRecordField(st3Status, "condition")
		st3Ready, _ := getRecordField(st3Cond, "Ready")
		if gotSince := getStringField(st3Ready, "since"); gotSince != "2026-04-01T14:00:00Z" {
			t.Fatalf("flipped state since = %q, want 2026-04-01T14:00:00Z", gotSince)
		}

		// observedGeneration above generation, regressing, or vanishing -> ErrObserved.
		obsAbove := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
			f("status", rec(f("observedGeneration", lkal.Int(3)))),
		)
		if _, err := objs.UpdateStatus(ctx, obsAbove); !errors.Is(err, envelope.ErrObserved) {
			t.Fatalf("observedGeneration above generation = %v, want ErrObserved", err)
		}
		obsRegress := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
			f("status", rec(f("observedGeneration", lkal.Int(1)))),
		)
		if _, err := objs.UpdateStatus(ctx, obsRegress); !errors.Is(err, envelope.ErrObserved) {
			t.Fatalf("observedGeneration regressing = %v, want ErrObserved", err)
		}
		obsVanish := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
			f("status", rec(f("phase", lkal.String("Ready")))),
		)
		if _, err := objs.UpdateStatus(ctx, obsVanish); !errors.Is(err, envelope.ErrObserved) {
			t.Fatalf("observedGeneration vanishing = %v, want ErrObserved", err)
		}

		// Delete with stale revision -> ErrConflict.
		if err := objs.Delete(ctx, objID, rev1); !errors.Is(err, envelope.ErrConflict) {
			t.Fatalf("Delete(stale rev) = %v, want ErrConflict", err)
		}

		// Delete with current revision -> succeeds; recreating yields a new uid and generation 1.
		curRev := getIntField(st3Meta, "revision")
		if err := objs.Delete(ctx, objID, curRev); err != nil {
			t.Fatalf("Delete(current rev): %v", err)
		}
		if _, err := objs.Get(ctx, objID); !errors.Is(err, resource.ErrNotFound) {
			t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
		}
		recreated, err := objs.Create(ctx, refs.Principal{}, workloadCand("web", 1))
		if err != nil {
			t.Fatalf("recreate Create: %v", err)
		}
		reMeta, _ := getRecordField(recreated, "meta")
		reUID := getStringField(reMeta, "uid")
		if !isCanonicalUUIDv4(reUID) || reUID == uid {
			t.Fatalf("recreated uid = %q (old %q), want distinct canonical UUIDv4", reUID, uid)
		}
		if getIntField(reMeta, "generation") != 1 {
			t.Fatalf("recreated generation = %d, want 1", getIntField(reMeta, "generation"))
		}
	})

	t.Run("ConcurrentUpdatesSingleWinner", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		st := newStore(t)
		cfg, _ := testObjectsConfig()
		objs := resource.NewObjects(st, cfg)

		created, err := objs.Create(ctx, refs.Principal{}, workloadCand("worker", 1))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		meta, _ := getRecordField(created, "meta")
		baseRev := getIntField(meta, "revision")

		const workers = 12
		errs := make([]error, workers)
		results := make([]lkal.Record, workers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				cand := workloadCand("worker", int64(10+idx))
				cand = replaceMetaField(cand, "revision", lkal.Int(baseRev))
				results[idx], errs[idx] = objs.Update(ctx, refs.Principal{}, cand)
			}(i)
		}
		close(start)
		wg.Wait()

		winners := 0
		conflicts := 0
		for i := range workers {
			switch {
			case errs[i] == nil:
				winners++
				m, _ := getRecordField(results[i], "meta")
				if getIntField(m, "generation") != 2 {
					t.Errorf("winner %d generation = %d, want 2", i, getIntField(m, "generation"))
				}
			case errors.Is(errs[i], envelope.ErrConflict):
				conflicts++
			default:
				t.Errorf("worker %d unexpected err: %v", i, errs[i])
			}
		}
		if winners != 1 || conflicts != workers-1 {
			t.Fatalf("winners=%d conflicts=%d, want winners=1 conflicts=%d", winners, conflicts, workers-1)
		}
	})

	t.Run("OrderedSetRoundTrip", func(t *testing.T) {
		checkObjectsOrderedSetRoundTrip(t, newStore(t))
	})
}

func TestObjects(t *testing.T) {
	checkObjectsCore(t, func(*testing.T) tenant.Store {
		return memstore.New()
	})
}

func TestObjectsRawBytesUIDsAndRandFailure(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()

	fixedBytes := bytes.Repeat([]byte{0xab}, 16)
	objs := resource.NewObjects(st, resource.Config{
		Now:     func() time.Time { return time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC) },
		Rand:    bytes.NewReader(append(slices.Clone(fixedBytes), fixedBytes...)),
		Schemas: defaultTestSchemas,
	})

	// Two objects with identical fixed-byte streams still yield valid v4 UUIDs,
	// and two sequential draws from a normal reader differ.
	c1, err := objs.Create(ctx, refs.Principal{}, workloadCand("one", 1))
	if err != nil {
		t.Fatalf("Create(one): %v", err)
	}
	m1, _ := getRecordField(c1, "meta")
	uid1 := getStringField(m1, "uid")
	if uid1 != "abababab-abab-4bab-abab-abababababab" || !isCanonicalUUIDv4(uid1) {
		t.Fatalf("uid1 = %q, want abababab-abab-4bab-abab-abababababab", uid1)
	}

	rawKey, err := (keyspace.Key{
		Family: keyspace.FamilyObject,
		Tenant: "t",
		Object: keyspace.Object{Group: "core", Kind: "workload", Name: "one"},
	}).Rel()
	if err != nil {
		t.Fatal(err)
	}
	kv, _, err := st.Get(ctx, rawKey, 0)
	if err != nil {
		t.Fatalf("raw store Get: %v", err)
	}
	if !bytes.HasPrefix(kv.Value, []byte(`{"$lkal":"0.1"`)) {
		t.Fatalf("stored bytes = %s, want prefix {\"$lkal\":\"0.1\"", kv.Value)
	}
	if bytes.Contains(kv.Value, []byte(`"revision"`)) {
		t.Fatalf("stored bytes unexpectedly contain revision: %s", kv.Value)
	}

	// Sequential draws from seqRand differ between objects.
	cfg, _ := testObjectsConfig()
	seqObjs := resource.NewObjects(st, cfg)
	a, err := seqObjs.Create(ctx, refs.Principal{}, workloadCand("a", 1))
	if err != nil {
		t.Fatal(err)
	}
	b, err := seqObjs.Create(ctx, refs.Principal{}, workloadCand("b", 1))
	if err != nil {
		t.Fatal(err)
	}
	ma, _ := getRecordField(a, "meta")
	mb, _ := getRecordField(b, "meta")
	uidA := getStringField(ma, "uid")
	uidB := getStringField(mb, "uid")
	if !isCanonicalUUIDv4(uidA) || !isCanonicalUUIDv4(uidB) || uidA == uidB {
		t.Fatalf("uidA=%q uidB=%q, want distinct canonical UUIDv4", uidA, uidB)
	}

	// Failing Rand returns error and writes nothing.
	wantErr := errors.New("entropy exhausted")
	revBefore, err := st.CurrentRev(ctx)
	if err != nil {
		t.Fatal(err)
	}
	failObjs := resource.NewObjects(st, resource.Config{Rand: failRand{err: wantErr}, Schemas: defaultTestSchemas})
	if _, err := failObjs.Create(ctx, refs.Principal{}, workloadCand("never", 1)); !errors.Is(err, wantErr) {
		t.Fatalf("Create with failing Rand = %v, want %v", err, wantErr)
	}
	if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
		t.Fatalf("failing Rand moved store revision: %d -> %d (err=%v)", revBefore, revAfter, err)
	}

	// Short Rand (EOF) also fails without writing.
	shortObjs := resource.NewObjects(st, resource.Config{Rand: io.LimitReader(bytes.NewReader(fixedBytes), 8), Schemas: defaultTestSchemas})
	if _, err := shortObjs.Create(ctx, refs.Principal{}, workloadCand("short", 1)); err == nil {
		t.Fatal("Create with short Rand succeeded, want error")
	}
	if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
		t.Fatalf("short Rand moved store revision: %d -> %d (err=%v)", revBefore, revAfter, err)
	}
}

func TestObjectsCorruptAndDeleteBehavior(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	cfg, _ := testObjectsConfig()
	objs := resource.NewObjects(st, cfg)

	cases := []struct {
		name string
		val  []byte
	}{
		{name: "raw garbage", val: []byte("not-json-at-all")},
		{name: "valid LKAL-JSON missing uid", val: []byte(`{"$lkal":"0.1","meta":{"kind":"workload","name":"bad","generation":1}}`)},
		{name: "valid LKAL-JSON containing stored revision", val: []byte(`{"$lkal":"0.1","meta":{"kind":"workload","name":"bad","uid":"01234567-89ab-4def-8123-456789abcdef","generation":1,"revision":2}}`)},
	}

	obj := keyspace.Object{Group: "core", Kind: "workload", Name: "bad"}
	key, err := (keyspace.Key{Family: keyspace.FamilyObject, Tenant: "t", Object: obj}).Rel()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, rev, err := st.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: tc.val}})
			if err != nil || !ok {
				t.Fatalf("seed corrupt value = (%v, %d, %v)", ok, rev, err)
			}

			if _, err := objs.Get(ctx, obj); !errors.Is(err, resource.ErrCorrupt) {
				t.Fatalf("Get(corrupt) = %v, want ErrCorrupt", err)
			}
			if _, err := objs.Update(ctx, refs.Principal{}, workloadCand("bad", 2)); !errors.Is(err, resource.ErrCorrupt) {
				t.Fatalf("Update(corrupt) = %v, want ErrCorrupt", err)
			}
			statusCand := rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("bad")))),
				f("status", rec(f("phase", lkal.String("Ready")))),
			)
			if _, err := objs.UpdateStatus(ctx, statusCand); !errors.Is(err, resource.ErrCorrupt) {
				t.Fatalf("UpdateStatus(corrupt) = %v, want ErrCorrupt", err)
			}

			// Delete with wrong rev -> ErrConflict; Delete with rev == 0 removes it -> Get returns ErrNotFound.
			if err := objs.Delete(ctx, obj, int64(rev)+10); !errors.Is(err, envelope.ErrConflict) {
				t.Fatalf("Delete(stale rev) = %v, want ErrConflict", err)
			}
			if err := objs.Delete(ctx, obj, 0); err != nil {
				t.Fatalf("Delete(rev=0): %v", err)
			}
			if _, err := objs.Get(ctx, obj); !errors.Is(err, resource.ErrNotFound) {
				t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
			}
			if err := objs.Delete(ctx, obj, 0); !errors.Is(err, resource.ErrNotFound) {
				t.Fatalf("Delete(missing) = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestObjectsIdentityMismatchIsCorrupt(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	cfg, _ := testObjectsConfig()
	objs := resource.NewObjects(st, cfg)

	targetObj := keyspace.Object{Group: "core", Kind: "workload", Name: "web"}
	targetKey, err := (keyspace.Key{Family: keyspace.FamilyObject, Tenant: "t", Object: targetObj}).Rel()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		other keyspace.Object
	}{
		{name: "different kind", other: keyspace.Object{Group: "core", Kind: "job", Name: "web"}},
		{name: "different name", other: keyspace.Object{Group: "core", Kind: "workload", Name: "other"}},
		{name: "different group", other: keyspace.Object{Group: "apps.example.com", Kind: "workload", Name: "web"}},
	}

	const uid = "01234567-89ab-4def-8123-456789abcdef"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metaFields := []lkal.Field{}
			if tc.other.Group != "core" {
				metaFields = append(metaFields, f("group", lkal.String(tc.other.Group)))
			}
			metaFields = append(metaFields,
				f("kind", lkal.String(tc.other.Kind)),
				f("name", lkal.String(tc.other.Name)),
			)
			otherCand := rec(f("meta", rec(metaFields...)), f("spec", rec(f("replicas", lkal.Int(1)))))
			storedDoc, err := envelope.Create(otherCand, uid)
			if err != nil {
				t.Fatalf("envelope.Create: %v", err)
			}
			raw, err := resource.Encode(storedDoc)
			if err != nil {
				t.Fatalf("resource.Encode: %v", err)
			}
			ok, revBefore, err := st.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: targetKey, Value: raw}})
			if err != nil || !ok {
				t.Fatalf("seed mismatched value = (%v, %d, %v)", ok, revBefore, err)
			}

			if _, err := objs.Get(ctx, targetObj); !errors.Is(err, resource.ErrCorrupt) {
				t.Fatalf("Get(mismatch) = %v, want ErrCorrupt", err)
			}
			if _, err := objs.Update(ctx, refs.Principal{}, workloadCand("web", 2)); !errors.Is(err, resource.ErrCorrupt) {
				t.Fatalf("Update(mismatch) = %v, want ErrCorrupt", err)
			}
			statusCand := rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
				f("status", rec(f("phase", lkal.String("Ready")))),
			)
			if _, err := objs.UpdateStatus(ctx, statusCand); !errors.Is(err, resource.ErrCorrupt) {
				t.Fatalf("UpdateStatus(mismatch) = %v, want ErrCorrupt", err)
			}
			if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
				t.Fatalf("store revision moved on mismatched read: %d -> %d (err=%v)", revBefore, revAfter, err)
			}

			if err := objs.Delete(ctx, targetObj, 0); err != nil {
				t.Fatalf("Delete(mismatch): %v", err)
			}
			if _, err := objs.Get(ctx, targetObj); !errors.Is(err, resource.ErrNotFound) {
				t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestObjectsFencedTenantRegistryAndIsolation(t *testing.T) {
	checkObjectsCore(t, func(t *testing.T) tenant.Store {
		t.Helper()
		ctx := context.Background()
		reg := tenant.NewRegistry(memstore.New())
		if err := reg.Register(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		v, err := reg.View(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		return v
	})

	ctx := context.Background()
	root := memstore.New()
	reg := tenant.NewRegistry(root)
	if err := reg.Register(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(ctx, "beta"); err != nil {
		t.Fatal(err)
	}
	vAlpha, err := reg.View(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	vBeta, err := reg.View(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}

	cfg, _ := testObjectsConfig()
	alphaObjs := resource.NewObjects(vAlpha, cfg)
	betaObjs := resource.NewObjects(vBeta, cfg)

	created, err := alphaObjs.Create(ctx, refs.Principal{}, workloadCand("shared", 3))
	if err != nil {
		t.Fatalf("alpha Create: %v", err)
	}
	obj := keyspace.Object{Group: "core", Kind: "workload", Name: "shared"}
	if _, err := betaObjs.Get(ctx, obj); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("beta Get(alpha's object) = %v, want ErrNotFound", err)
	}
	if _, err := betaObjs.Create(ctx, refs.Principal{}, workloadCand("shared", 7)); err != nil {
		t.Fatalf("beta Create(same name): %v", err)
	}

	// Deleting tenant alpha fences subsequent writes through vAlpha.
	if err := reg.Delete(ctx, "alpha"); err != nil {
		t.Fatalf("Delete(alpha): %v", err)
	}
	if _, err := alphaObjs.Create(ctx, refs.Principal{}, workloadCand("after-delete", 1)); !errors.Is(err, tenant.ErrFenced) {
		t.Fatalf("Create after tenant delete = %v, want tenant.ErrFenced", err)
	}

	// Re-registering alpha does not un-fence the old view; Update and Delete return ErrFenced.
	if err := reg.Register(ctx, "alpha"); err != nil {
		t.Fatalf("re-register alpha: %v", err)
	}
	vAlphaNew, err := reg.View(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	alphaNewObjs := resource.NewObjects(vAlphaNew, cfg)
	fresh, err := alphaNewObjs.Create(ctx, refs.Principal{}, workloadCand("shared", 1))
	if err != nil {
		t.Fatalf("new view Create: %v", err)
	}
	freshMeta, _ := getRecordField(fresh, "meta")
	_ = created

	updateCand := workloadCand("shared", 9)
	updateCand = replaceMetaField(updateCand, "revision", lkal.Int(getIntField(freshMeta, "revision")))
	if _, err := alphaObjs.Update(ctx, refs.Principal{}, updateCand); !errors.Is(err, tenant.ErrFenced) {
		t.Fatalf("Update on fenced view = %v, want tenant.ErrFenced", err)
	}
	statusCand := rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("shared")))),
		f("status", rec(f("phase", lkal.String("Ready")))),
	)
	if _, err := alphaObjs.UpdateStatus(ctx, statusCand); !errors.Is(err, tenant.ErrFenced) {
		t.Fatalf("UpdateStatus on fenced view = %v, want tenant.ErrFenced", err)
	}
	if err := alphaObjs.Delete(ctx, obj, 0); !errors.Is(err, tenant.ErrFenced) {
		t.Fatalf("Delete on fenced view = %v, want tenant.ErrFenced", err)
	}

	// Verify alpha's key in root uses /t/alpha/o/... and beta's object is untouched.
	betaGot, err := betaObjs.Get(ctx, obj)
	if err != nil {
		t.Fatalf("beta Get: %v", err)
	}
	spec, _ := getRecordField(betaGot, "spec")
	if getIntField(spec, "replicas") != 7 {
		t.Fatalf("beta replicas = %d, want 7", getIntField(spec, "replicas"))
	}
	_ = strings.Contains
}

type failingStore struct {
	tenant.Store
	getErr error
	txnErr error
}

func (f *failingStore) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	if f.getErr != nil {
		return store.KV{}, 0, f.getErr
	}
	return f.Store.Get(ctx, key, atRev)
}

func (f *failingStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	if f.txnErr != nil {
		return false, 0, f.txnErr
	}
	return f.Store.Txn(ctx, cmps, ops)
}

func TestObjectsStoreErrorsAndValidation(t *testing.T) {
	ctx := context.Background()
	base := memstore.New()
	cfg, _ := testObjectsConfig()
	seeded := resource.NewObjects(base, cfg)
	if _, err := seeded.Create(ctx, refs.Principal{}, workloadCand("item", 1)); err != nil {
		t.Fatal(err)
	}

	wantGetErr := errors.New("boom get")
	wantTxnErr := errors.New("boom txn")
	obj := keyspace.Object{Group: "core", Kind: "workload", Name: "item"}

	errGetObjs := resource.NewObjects(&failingStore{Store: base, getErr: wantGetErr}, cfg)
	if _, err := errGetObjs.Get(ctx, obj); !errors.Is(err, wantGetErr) {
		t.Errorf("Get store err = %v, want %v", err, wantGetErr)
	}
	if _, err := errGetObjs.Update(ctx, refs.Principal{}, workloadCand("item", 2)); !errors.Is(err, wantGetErr) {
		t.Errorf("Update store Get err = %v, want %v", err, wantGetErr)
	}
	if err := errGetObjs.Delete(ctx, obj, 0); !errors.Is(err, wantGetErr) {
		t.Errorf("Delete store Get err = %v, want %v", err, wantGetErr)
	}

	errTxnObjs := resource.NewObjects(&failingStore{Store: base, txnErr: wantTxnErr}, cfg)
	if _, err := errTxnObjs.Create(ctx, refs.Principal{}, workloadCand("other", 1)); !errors.Is(err, wantTxnErr) {
		t.Errorf("Create store Txn err = %v, want %v", err, wantTxnErr)
	}
	if _, err := errTxnObjs.Update(ctx, refs.Principal{}, workloadCand("item", 2)); !errors.Is(err, wantTxnErr) {
		t.Errorf("Update store Txn err = %v, want %v", err, wantTxnErr)
	}
	if err := errTxnObjs.Delete(ctx, obj, 0); !errors.Is(err, wantTxnErr) {
		t.Errorf("Delete store Txn err = %v, want %v", err, wantTxnErr)
	}

	// Invalid object key or malformed candidate passes validation error through.
	badObj := keyspace.Object{Group: "core", Kind: "BadKind", Name: "x"}
	if _, err := seeded.Get(ctx, badObj); !errors.Is(err, keyspace.ErrInvalid) {
		t.Errorf("Get(invalid obj) = %v, want keyspace.ErrInvalid", err)
	}
	if err := seeded.Delete(ctx, badObj, 0); !errors.Is(err, keyspace.ErrInvalid) {
		t.Errorf("Delete(invalid obj) = %v, want keyspace.ErrInvalid", err)
	}
	if _, err := seeded.Create(ctx, refs.Principal{}, rec()); !errors.Is(err, envelope.ErrMalformed) {
		t.Errorf("Create(malformed) = %v, want envelope.ErrMalformed", err)
	}
}

func TestObjectsCandidateCanonicalizationAndLKALRejection(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	cfg, _ := testObjectsConfig()
	objs := resource.NewObjects(st, cfg)

	unnormalizedSpec := rec(
		f("ratio", lkal.Decimal("1.50")),
		f("mem", lkal.Quantity{Number: "1.50", Unit: "Mi"}),
	)
	cand := rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("norm")))),
		f("spec", unnormalizedSpec),
	)

	created, err := objs.Create(ctx, refs.Principal{}, cand)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := objs.Get(ctx, keyspace.Object{Group: "core", Kind: "workload", Name: "norm"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !lkal.Equal(created, got) {
		t.Fatalf("Create != Get after normalization:\ncreate: %#v\nget:    %#v", created, got)
	}

	revBefore, err := st.CurrentRev(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := objs.Update(ctx, refs.Principal{}, cand)
	if err != nil {
		t.Fatalf("Update(same unnormalized candidate): %v", err)
	}
	if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
		t.Fatalf("no-op Update with unnormalized candidate moved store revision: %d -> %d (err=%v)", revBefore, revAfter, err)
	}
	upMeta, _ := getRecordField(updated, "meta")
	if getIntField(upMeta, "generation") != 1 {
		t.Fatalf("generation = %d, want 1", getIntField(upMeta, "generation"))
	}

	statusCand := rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("norm")))),
		f("status", rec(
			f("ratio", lkal.Decimal("1.50")),
			f("mem", lkal.Quantity{Number: "1.50", Unit: "Mi"}),
		)),
	)
	st1, err := objs.UpdateStatus(ctx, statusCand)
	if err != nil {
		t.Fatalf("UpdateStatus(1): %v", err)
	}
	revAfterStatus1, err := st.CurrentRev(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if revAfterStatus1 <= revBefore {
		t.Fatalf("UpdateStatus(1) did not advance revision: %d -> %d", revBefore, revAfterStatus1)
	}
	st2, err := objs.UpdateStatus(ctx, statusCand)
	if err != nil {
		t.Fatalf("UpdateStatus(2): %v", err)
	}
	if revAfterStatus2, err := st.CurrentRev(ctx); err != nil || revAfterStatus2 != revAfterStatus1 {
		t.Fatalf("no-op UpdateStatus with unnormalized candidate moved store revision: %d -> %d (err=%v)", revAfterStatus1, revAfterStatus2, err)
	}
	if !lkal.Equal(st1, st2) {
		t.Fatalf("UpdateStatus(2) != UpdateStatus(1):\nst1: %#v\nst2: %#v", st1, st2)
	}

	badSpecs := []struct {
		name string
		spec lkal.Record
	}{
		{
			name: "dollar-prefixed key in spec",
			spec: rec(f("nested", rec(f("$x", lkal.Int(1))))),
		},
		{
			name: "mixed-type list",
			spec: rec(f("items", lkal.List{lkal.Int(1), lkal.String("two")})),
		},
		{
			name: "list holding a record",
			spec: rec(f("items", lkal.List{rec(f("k", lkal.String("v")))})),
		},
		{
			name: "nil field value",
			spec: rec(f("missing", nil)),
		},
	}

	revBeforeBad, err := st.CurrentRev(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range badSpecs {
		t.Run(tc.name, func(t *testing.T) {
			createCand := rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("bad-lkal")))),
				f("spec", tc.spec),
			)
			if _, err := objs.Create(ctx, refs.Principal{}, createCand); !errors.Is(err, envelope.ErrMalformed) {
				t.Errorf("Create(%s) = %v, want envelope.ErrMalformed", tc.name, err)
			}
			updateCand := rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("norm")))),
				f("spec", tc.spec),
			)
			if _, err := objs.Update(ctx, refs.Principal{}, updateCand); !errors.Is(err, envelope.ErrMalformed) {
				t.Errorf("Update(%s) = %v, want envelope.ErrMalformed", tc.name, err)
			}
			if revAfterBad, err := st.CurrentRev(ctx); err != nil || revAfterBad != revBeforeBad {
				t.Errorf("store revision moved on rejected %s: %d -> %d (err=%v)", tc.name, revBeforeBad, revAfterBad, err)
			}
		})
	}
}
