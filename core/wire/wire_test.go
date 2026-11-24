package wire_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/cedar-policy/cedar-go"

	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/orderedset"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
	"github.com/Kaikei-e/Nayutar/core/resource/refcedar"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/sim"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache"
	"github.com/Kaikei-e/Nayutar/core/wire"
)

func fld(key string, val lkal.Value) lkal.Field {
	return lkal.Field{Key: key, Value: val}
}

func rec(fields ...lkal.Field) lkal.Record {
	return lkal.Record{Fields: fields}
}

func getField(r lkal.Record, key string) (lkal.Value, bool) {
	for _, f := range r.Fields {
		if f.Key == key {
			return f.Value, true
		}
	}
	return nil, false
}

func getRecord(r lkal.Record, key string) (lkal.Record, bool) {
	v, ok := getField(r, key)
	if !ok {
		return lkal.Record{}, false
	}
	sub, ok := v.(lkal.Record)
	return sub, ok
}

func getString(r lkal.Record, key string) string {
	v, _ := getField(r, key)
	s, _ := v.(lkal.String)
	return string(s)
}

func getInt(r lkal.Record, key string) int64 {
	v, _ := getField(r, key)
	n, _ := v.(lkal.Int)
	return int64(n)
}

func testSchemas(t *testing.T) resource.Schemas {
	t.Helper()
	workloadSchema, err := resource.CompileKindSchema("workload.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"

root {
  field "spec" {
    type = .record
    field "replicas" {
      type = .int
      default = 1
    }
    field "nested" {
      type = .record
      field "tier" {
        type = .string
        default = "standard"
      }
      field "note" { type = .string }
    }
    field "steps" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        field "order" {
          type = .int
          required = true
        }
        field "cmd" {
          type = .string
          required = true
        }
      }
    }
    field "secretRef" {
      type = .ref
      targets = [&secret]
    }
    field "payload" { type = .string }
  }
  field "status" {
    type = .record
    field "phase" { type = .string }
    field "readyReplicas" { type = .int }
  }
}
`))
	if err != nil {
		t.Fatalf("compile workload schema: %v", err)
	}
	secretSchema, err := resource.CompileKindSchema("secret.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"

root {
  field "spec" {
    type = .record
    field "data" { type = .string }
  }
}
`))
	if err != nil {
		t.Fatalf("compile secret schema: %v", err)
	}
	widgetSchema, err := resource.CompileKindSchema("widget.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"

root {
  field "spec" {
    type = .record
    field "size" { type = .int }
  }
}
`))
	if err != nil {
		t.Fatalf("compile widget schema: %v", err)
	}
	return resource.Schemas{
		"workload":               workloadSchema,
		"secret":                 secretSchema,
		"widget.apps.example.io": widgetSchema,
	}
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

func (c *fixedClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func generateLoopbackTLS(t *testing.T) (serverTLS, clientTLS *tls.Config) {
	t.Helper()
	rng := sim.RandReader(sim.NewDetRand(2))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rng)
	if err != nil {
		t.Fatalf("generate ecdsa key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rng, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)

	serverTLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
	clientTLS = &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS13,
		ServerName: "127.0.0.1",
	}
	return serverTLS, clientTLS
}

type testEnv struct {
	root    store.Store
	reg     *tenant.Registry
	caches  *watchcache.Manager
	handler *wire.Handler
	clock   *fixedClock
	net     *sim.MemNet
	baseURL string
	client  *wire.Client
	rawHC   *http.Client
	useTLS  bool
	proto   *atomic.Int32
}

type envOpts struct {
	root          store.Store
	useTLS        bool
	cacheCfg      watchcache.Config
	authFn        func(*http.Request) (refs.Principal, error)
	authorizer    refs.Authorizer
	scopeResolver resource.ScopeResolver
	maxBodyBytes  int64
	maxListLimit  int
}

func startTestEnv(t *testing.T, opts envOpts) *testEnv {
	t.Helper()
	root := opts.root
	if root == nil {
		root = memstore.New()
	}
	reg := tenant.NewRegistry(root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := reg.Register(ctx, "acme"); err != nil {
		t.Fatalf("register acme: %v", err)
	}

	cacheCfg := opts.cacheCfg
	if cacheCfg.ProgressInterval == 0 {
		cacheCfg.ProgressInterval = 10 * time.Millisecond
	}
	if cacheCfg.BookmarkInterval == 0 {
		cacheCfg.BookmarkInterval = 100 * time.Millisecond
	}
	if cacheCfg.ReadWait == 0 {
		cacheCfg.ReadWait = 2 * time.Second
	}
	caches, err := watchcache.NewManager(root, cacheCfg)
	if err != nil {
		t.Fatalf("new watchcache manager: %v", err)
	}
	t.Cleanup(caches.Close)

	clk := &fixedClock{t: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}
	h, err := wire.New(wire.Config{
		Registry:      reg,
		Caches:        caches,
		Schemas:       testSchemas(t),
		Authorizer:    opts.authorizer,
		ScopeResolver: opts.scopeResolver,
		Authenticate:  opts.authFn,
		Now:           clk.Now,
		Rand:          sim.RandReader(sim.NewDetRand(1)),
		MaxBodyBytes:  opts.maxBodyBytes,
		MaxListLimit:  opts.maxListLimit,
	})
	if err != nil {
		t.Fatalf("wire.New: %v", err)
	}

	var proto atomic.Int32
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proto.Store(int32(r.ProtoMajor))
		h.ServeHTTP(w, r)
	})

	mn := sim.NewMemNet()
	ln, err := mn.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = mn.Close()
		t.Fatalf("memnet listen 127.0.0.1:0: %v", err)
	}
	srv := &http.Server{Handler: wrapped}

	var (
		baseURL string
		client  *wire.Client
	)
	if opts.useTLS {
		srvTLS, cliTLS := generateLoopbackTLS(t)
		srv.TLSConfig = srvTLS
		wire.ConfigureServer(srv, false)
		go func() { _ = srv.ServeTLS(ln, "", "") }()
		baseURL = "https://" + ln.Addr().String()
		client = wire.NewTLSClientWithNet(baseURL, cliTLS, mn)
	} else {
		wire.ConfigureServer(srv, true)
		go func() { _ = srv.Serve(ln) }()
		baseURL = "http://" + ln.Addr().String()
		client = wire.NewH2CClientWithNet(baseURL, mn)
	}

	t.Cleanup(func() {
		client.CloseIdleConnections()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = srv.Shutdown(shutCtx)
		_ = mn.Close()
	})

	return &testEnv{
		root:    root,
		reg:     reg,
		caches:  caches,
		handler: h,
		clock:   clk,
		net:     mn,
		baseURL: baseURL,
		client:  client,
		rawHC:   client.HTTPClient(),
		useTLS:  opts.useTLS,
		proto:   &proto,
	}
}

func workloadDoc(name string, replicas int64, labels ...lkal.Field) lkal.Record {
	metaFields := []lkal.Field{
		fld("kind", lkal.String("workload")),
		fld("name", lkal.String(name)),
	}
	if len(labels) > 0 {
		metaFields = append(metaFields, fld("labels", rec(labels...)))
	}
	return rec(
		fld("meta", rec(metaFields...)),
		fld("spec", rec(fld("replicas", lkal.Int(replicas)))),
	)
}

func widgetDoc(name string, size int64) lkal.Record {
	return rec(
		fld("meta", rec(
			fld("group", lkal.String("apps.example.io")),
			fld("kind", lkal.String("widget")),
			fld("name", lkal.String(name)),
		)),
		fld("spec", rec(fld("size", lkal.Int(size)))),
	)
}

func assertSecurityHeaders(t *testing.T, hdr http.Header) {
	t.Helper()
	checks := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Cache-Control":           "no-store",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	}
	for k, want := range checks {
		if got := hdr.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
}

func TestSchemasAndCodecs(t *testing.T) {
	if wire.ErrorSchema() == nil || len(wire.ErrorSchemaBytes()) == 0 {
		t.Fatal("ErrorSchema not initialized")
	}
	if wire.ListSchema() == nil || len(wire.ListSchemaBytes()) == 0 {
		t.Fatal("ListSchema not initialized")
	}
	if wire.WatchEventSchema() == nil || len(wire.WatchEventSchemaBytes()) == 0 {
		t.Fatal("WatchEventSchema not initialized")
	}

	allKinds := []struct {
		kind     wire.ErrorKind
		sentinel error
		status   int
	}{
		{wire.KindBadRequest, wire.ErrBadRequest, 400},
		{wire.KindUnauthorized, wire.ErrUnauthorized, 401},
		{wire.KindForbidden, wire.ErrForbidden, 403},
		{wire.KindNotFound, wire.ErrNotFound, 404},
		{wire.KindMethodNotAllowed, wire.ErrMethodNotAllowed, 405},
		{wire.KindNotAcceptable, wire.ErrNotAcceptable, 406},
		{wire.KindConflict, wire.ErrConflict, 409},
		{wire.KindGone, wire.ErrGone, 410},
		{wire.KindTooLarge, wire.ErrTooLarge, 413},
		{wire.KindUnsupportedMediaType, wire.ErrUnsupportedMediaType, 415},
		{wire.KindUnprocessable, wire.ErrUnprocessable, 422},
		{wire.KindObserved, wire.ErrObserved, 422},
		{wire.KindRefKind, wire.ErrRefKind, 422},
		{wire.KindRefNotFound, wire.ErrRefNotFound, 422},
		{wire.KindPathNotFound, wire.ErrPathNotFound, 422},
		{wire.KindInternal, wire.ErrInternal, 500},
		{wire.KindUnavailable, wire.ErrUnavailable, 503},
	}
	for _, tc := range allKinds {
		in := &wire.Error{Kind: tc.kind, Status: tc.status, Path: "spec/replicas", Message: "test message"}
		raw, err := wire.EncodeError(in)
		if err != nil {
			t.Fatalf("EncodeError(%s): %v", tc.kind, err)
		}
		if !bytes.HasPrefix(raw, []byte(`{"$lkal":"0.1",`)) || bytes.ContainsRune(raw, '\n') {
			t.Fatalf("EncodeError(%s) not compact single-line LKAL-JSON: %s", tc.kind, raw)
		}
		got, err := wire.DecodeError(raw)
		if err != nil {
			t.Fatalf("DecodeError(%s): %v", tc.kind, err)
		}
		if !got.Equal(in) {
			t.Fatalf("error roundtrip mismatch: got %+v, want %+v", got, in)
		}
		if !errors.Is(got, tc.sentinel) {
			t.Fatalf("errors.Is(%v, %v) = false", got, tc.sentinel)
		}
	}

	if _, err := wire.EncodeError(&wire.Error{Kind: "bogus", Status: 400, Message: "x"}); err == nil {
		t.Fatal("expected error for unknown kind")
	}
	if _, err := wire.EncodeError(&wire.Error{Kind: wire.KindBadRequest, Status: 200, Message: "x"}); err == nil {
		t.Fatal("expected error for status outside 400..599")
	}
	if _, err := wire.EncodeError(&wire.Error{Kind: wire.KindBadRequest, Status: 400, Message: ""}); err == nil {
		t.Fatal("expected error for empty message")
	}

	// List roundtrip (empty + multi-item out-of-order input -> sorted output).
	emptyRaw, err := wire.EncodeList(resource.ListResult{Revision: 0})
	if err != nil {
		t.Fatalf("EncodeList(empty): %v", err)
	}
	emptyDec, err := wire.DecodeList(emptyRaw)
	if err != nil || emptyDec.Revision != 0 || len(emptyDec.Items) != 0 {
		t.Fatalf("DecodeList(empty) = %+v, %v", emptyDec, err)
	}

	itemB := workloadDoc("b.example", 2)
	itemA := workloadDoc("a.example", 1)
	listRaw, err := wire.EncodeList(resource.ListResult{
		Revision: 9,
		Continue: "o/core/workload/b.example",
		Items:    []lkal.Record{itemB, itemA},
	})
	if err != nil {
		t.Fatalf("EncodeList: %v", err)
	}
	if !bytes.HasPrefix(listRaw, []byte(`{"$lkal":"0.1",`)) || bytes.ContainsRune(listRaw, '\n') {
		t.Fatalf("EncodeList not compact single-line: %s", listRaw)
	}
	listDec, err := wire.DecodeList(listRaw)
	if err != nil {
		t.Fatalf("DecodeList: %v", err)
	}
	if listDec.Revision != 9 || listDec.Continue != "o/core/workload/b.example" || len(listDec.Items) != 2 {
		t.Fatalf("unexpected decoded list: %+v", listDec)
	}
	meta0, _ := getRecord(listDec.Items[0], "meta")
	meta1, _ := getRecord(listDec.Items[1], "meta")
	if getString(meta0, "name") != "a.example" || getString(meta1, "name") != "b.example" {
		t.Fatalf("expected items sorted by name, got %q and %q", getString(meta0, "name"), getString(meta1, "name"))
	}
	// Reject mismatched map key vs meta/name.
	badKeyList := []byte(`{"$lkal":"0.1","revision":1,"items":{"wrong":{"meta":{"kind":"workload","name":"real"}}}}`)
	if _, err := wire.DecodeList(badKeyList); err == nil {
		t.Fatal("expected DecodeList error on key != meta/name")
	}

	// WatchEvent roundtrip and cross-field invariants.
	putObj := rec(
		fld("meta", rec(
			fld("kind", lkal.String("workload")),
			fld("name", lkal.String("web")),
			fld("revision", lkal.Int(5)),
		)),
		fld("spec", rec(fld("replicas", lkal.Int(3)))),
	)
	validEvents := []wire.WatchEvent{
		{Type: wire.EventPut, Revision: 5, Object: &putObj},
		{Type: wire.EventDelete, Revision: 6, Target: &wire.WatchTarget{Group: keyspace.CoreGroup, Kind: "workload", Name: "web"}},
		{Type: wire.EventDelete, Revision: 7, Target: &wire.WatchTarget{Group: "apps.example.io", Kind: "widget", Name: "w1"}},
		{Type: wire.EventBookmark, Revision: 0},
		{Type: wire.EventBookmark, Revision: 8, InitialEventsEnd: true},
		{Type: wire.EventError, Error: &wire.Error{Kind: wire.KindGone, Status: 410, Message: "compacted"}},
	}
	for i, ev := range validEvents {
		raw, err := wire.EncodeWatchEvent(ev)
		if err != nil {
			t.Fatalf("EncodeWatchEvent[%d]: %v", i, err)
		}
		if !bytes.HasPrefix(raw, []byte(`{"$lkal":"0.1",`)) || bytes.ContainsRune(raw, '\n') {
			t.Fatalf("EncodeWatchEvent[%d] not compact single-line: %s", i, raw)
		}
		got, err := wire.DecodeWatchEvent(raw)
		if err != nil {
			t.Fatalf("DecodeWatchEvent[%d]: %v", i, err)
		}
		if got.Type != ev.Type || got.Revision != ev.Revision || got.InitialEventsEnd != ev.InitialEventsEnd {
			t.Fatalf("WatchEvent[%d] mismatch: got %+v, want %+v", i, got, ev)
		}
		if ev.Target != nil && (got.Target == nil || *got.Target != *ev.Target) {
			t.Fatalf("WatchEvent[%d] target mismatch: got %+v, want %+v", i, got.Target, ev.Target)
		}
		if ev.Error != nil && (got.Error == nil || !got.Error.Equal(ev.Error)) {
			t.Fatalf("WatchEvent[%d] error mismatch: got %+v, want %+v", i, got.Error, ev.Error)
		}
	}

	// Reject cross-field violations in EncodeWatchEvent.
	badEncodeEvents := []wire.WatchEvent{
		{Type: wire.EventPut, Revision: 1},
		{Type: wire.EventPut, Revision: 0, Object: &putObj},
		{Type: wire.EventPut, Revision: 99, Object: &putObj}, // disagrees with meta/revision 5
		{Type: wire.EventPut, Revision: 5, Object: &putObj, InitialEventsEnd: true},
		{Type: wire.EventPut, Revision: 5, Object: &putObj, Target: &wire.WatchTarget{Group: "core", Kind: "workload", Name: "web"}},
		{Type: wire.EventDelete, Revision: 1},
		{Type: wire.EventDelete, Revision: 0, Target: &wire.WatchTarget{Group: "core", Kind: "workload", Name: "web"}},
		{Type: wire.EventDelete, Revision: 1, Target: &wire.WatchTarget{Group: "core", Kind: "workload", Name: "web"}, Object: &putObj},
		{Type: wire.EventBookmark, Revision: 1, Object: &putObj},
		{Type: wire.EventBookmark, Revision: 1, Error: &wire.Error{Kind: wire.KindGone, Status: 410, Message: "x"}},
		{Type: wire.EventError},
		{Type: wire.EventError, Revision: 1, Error: &wire.Error{Kind: wire.KindGone, Status: 410, Message: "x"}},
		{Type: wire.EventError, InitialEventsEnd: true, Error: &wire.Error{Kind: wire.KindGone, Status: 410, Message: "x"}},
	}
	for i, ev := range badEncodeEvents {
		if _, err := wire.EncodeWatchEvent(ev); err == nil {
			t.Errorf("EncodeWatchEvent invalid[%d] unexpectedly succeeded", i)
		}
	}

	// Reject cross-field violations in DecodeWatchEvent.
	badDecodeLines := []string{
		`{"$lkal":"0.1","type":{"$enum":"put"},"revision":1}`,
		`{"$lkal":"0.1","type":{"$enum":"put"},"revision":1,"object":{"meta":{"kind":"workload","name":"web"}},"initialEventsEnd":false}`,
		`{"$lkal":"0.1","type":{"$enum":"put"},"revision":2,"object":{"meta":{"kind":"workload","name":"web","revision":1}}}`,
		`{"$lkal":"0.1","type":{"$enum":"delete"},"revision":1}`,
		`{"$lkal":"0.1","type":{"$enum":"delete"},"revision":1,"target":{"group":"core","kind":"workload","name":"web"}}`,
		`{"$lkal":"0.1","type":{"$enum":"bookmark"}}`,
		`{"$lkal":"0.1","type":{"$enum":"bookmark"},"revision":1,"target":{"kind":"workload","name":"web"}}`,
		`{"$lkal":"0.1","type":{"$enum":"error"}}`,
		`{"$lkal":"0.1","type":{"$enum":"error"},"revision":1,"error":{"kind":{"$enum":"gone"},"status":410,"message":"gone"}}`,
		`{"$lkal":"0.1","type":{"$enum":"error"},"initialEventsEnd":true,"error":{"kind":{"$enum":"gone"},"status":410,"message":"gone"}}`,
	}
	for i, raw := range badDecodeLines {
		if _, err := wire.DecodeWatchEvent([]byte(raw)); err == nil {
			t.Errorf("DecodeWatchEvent invalid[%d] (%s) unexpectedly succeeded", i, raw)
		}
	}
}

func TestHTTP2CRUDPatchListWatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		useTLS bool
	}{
		{name: "h2c", useTLS: false},
		{name: "tls_h2", useTLS: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) { runProtocolSuite(t, tc.useTLS) })
		})
	}
}

func runProtocolSuite(t *testing.T, useTLS bool) {
	t.Helper()
	env := startTestEnv(t, envOpts{useTLS: useTLS, maxListLimit: 2})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Create core workload omitting spec/replicas so schema default (1) materializes.
	cand := rec(
		fld("meta", rec(
			fld("kind", lkal.String("workload")),
			fld("name", lkal.String("web")),
		)),
		fld("spec", rec(
			fld("nested", rec(fld("note", lkal.String("hello")))),
		)),
	)
	created, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "workload", cand)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := env.proto.Load(); got != 2 {
		t.Fatalf("server saw HTTP/%d, want HTTP/2", got)
	}
	cMeta, _ := getRecord(created, "meta")
	cSpec, _ := getRecord(created, "spec")
	cNested, _ := getRecord(cSpec, "nested")
	rev1 := getInt(cMeta, "revision")
	if rev1 < 1 || getInt(cMeta, "generation") != 1 || getString(cMeta, "uid") == "" {
		t.Fatalf("unexpected created meta: %+v", cMeta)
	}
	if getInt(cSpec, "replicas") != 1 || getString(cNested, "tier") != "standard" {
		t.Fatalf("expected schema defaults materialized, got spec=%+v", cSpec)
	}

	// Duplicate Create -> 409 Conflict.
	if _, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "workload", cand); !errors.Is(err, wire.ErrConflict) {
		t.Fatalf("duplicate Create err = %v, want ErrConflict", err)
	}

	// POST with mismatched URL group/kind -> 403 Forbidden.
	if _, err := env.client.Create(ctx, "acme", "apps.example.io", "widget", cand); !errors.Is(err, wire.ErrForbidden) {
		t.Fatalf("mismatched POST err = %v, want ErrForbidden", err)
	}

	// Get via watchcache.
	webObj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "web"}
	gotDoc, err := env.client.Get(ctx, "acme", webObj, 0)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !lkal.Equal(gotDoc, created) {
		t.Fatalf("Get mismatch: got %+v, want %+v", gotDoc, created)
	}

	// Update with stale meta/revision -> 409 Conflict.
	staleCand := rec(
		fld("meta", rec(
			fld("kind", lkal.String("workload")),
			fld("name", lkal.String("web")),
			fld("revision", lkal.Int(rev1+999)),
		)),
		fld("spec", rec(fld("replicas", lkal.Int(3)))),
	)
	if _, err := env.client.Update(ctx, "acme", webObj, staleCand); !errors.Is(err, wire.ErrConflict) {
		t.Fatalf("stale Update err = %v, want ErrConflict", err)
	}

	// PUT with mismatched URL name -> 403 Forbidden.
	otherObj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "other"}
	if _, err := env.client.Update(ctx, "acme", otherObj, staleCand); !errors.Is(err, wire.ErrForbidden) {
		t.Fatalf("mismatched PUT err = %v, want ErrForbidden", err)
	}

	// Patch: test meta/revision + set replicas + set orderedset steps + set labels.
	stepsRec, err := orderedset.FromItems([]orderedset.Item{
		{Key: "init", Value: rec(fld("cmd", lkal.String("echo init")))},
		{Key: "serve", Value: rec(fld("cmd", lkal.String("echo serve")))},
	})
	if err != nil {
		t.Fatalf("orderedset.FromItems: %v", err)
	}
	patched, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
		{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(rev1)},
		{Kind: patch.OpSet, Path: "meta/labels/env", Value: lkal.String("prod")},
		{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(5)},
		{Kind: patch.OpSet, Path: "spec/steps", Value: stepsRec},
		{Kind: patch.OpSet, Path: "spec/nested/tier", Value: lkal.String("premium")},
	})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	pMeta, _ := getRecord(patched, "meta")
	pSpec, _ := getRecord(patched, "spec")
	rev2 := getInt(pMeta, "revision")
	if rev2 <= rev1 || getInt(pMeta, "generation") != 2 || getInt(pSpec, "replicas") != 5 {
		t.Fatalf("unexpected patched doc: meta=%+v spec=%+v", pMeta, pSpec)
	}

	// Patch: swap orderedset orders atomically + del spec/nested/tier (re-materializes default "standard").
	swapped, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
		{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(rev2)},
		{Kind: patch.OpSet, Path: "spec/steps/init/order", Value: lkal.Int(20)},
		{Kind: patch.OpSet, Path: "spec/steps/serve/order", Value: lkal.Int(10)},
		{Kind: patch.OpDel, Path: "spec/nested/tier"},
	})
	if err != nil {
		t.Fatalf("Patch swap+del: %v", err)
	}
	sSpec, _ := getRecord(swapped, "spec")
	sNested, _ := getRecord(sSpec, "nested")
	if getString(sNested, "tier") != "standard" {
		t.Fatalf("expected del on optional field with default to re-materialize \"standard\", got %q", getString(sNested, "tier"))
	}
	sSteps, _ := getRecord(sSpec, "steps")
	entries, err := orderedset.Entries(sSteps)
	if err != nil || len(entries) != 2 || entries[0].Key != "serve" || entries[1].Key != "init" {
		t.Fatalf("expected swapped order [serve, init], got entries=%+v err=%v", entries, err)
	}

	// Patch: test meta/revision mismatch -> 409 Conflict.
	if _, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
		{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(rev1)},
		{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(9)},
	}); !errors.Is(err, wire.ErrConflict) {
		t.Fatalf("Patch stale test err = %v, want ErrConflict", err)
	}

	// Patch: del missing path -> 422 ErrPathNotFound (RFC 5789 §2.2).
	if _, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
		{Kind: patch.OpDel, Path: "spec/doesnotexist"},
	}); !errors.Is(err, wire.ErrPathNotFound) {
		t.Fatalf("Patch del missing err = %v, want ErrPathNotFound", err)
	} else {
		var we *wire.Error
		if !errors.As(err, &we) || we.Status != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 *wire.Error, got %+v", we)
		}
	}

	// Patch: forbidden status path through spec entrance -> 403 Forbidden.
	if _, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
		{Kind: patch.OpSet, Path: "status/phase", Value: lkal.String("Running")},
	}); !errors.Is(err, wire.ErrForbidden) {
		t.Fatalf("Patch status through spec entrance err = %v, want ErrForbidden", err)
	}

	// PatchStatus: set phase, observedGeneration, and condition.
	stDoc, err := env.client.PatchStatus(ctx, "acme", webObj, []patch.Op{
		{Kind: patch.OpSet, Path: "status/phase", Value: lkal.String("Running")},
		{Kind: patch.OpSet, Path: "status/observedGeneration", Value: lkal.Int(3)},
		{Kind: patch.OpSet, Path: "status/condition/Ready", Value: rec(
			fld("state", lkal.Enum("true")),
			fld("reason", lkal.String("AllReady")),
		)},
	})
	if err != nil {
		t.Fatalf("PatchStatus: %v", err)
	}
	stStatus, _ := getRecord(stDoc, "status")
	if getString(stStatus, "phase") != "Running" || getInt(stStatus, "observedGeneration") != 3 {
		t.Fatalf("unexpected status after PatchStatus: %+v", stStatus)
	}

	// PatchStatus: direct modification of since -> 403 Forbidden.
	if _, err := env.client.PatchStatus(ctx, "acme", webObj, []patch.Op{
		{Kind: patch.OpSet, Path: "status/condition/Ready/since", Value: lkal.String("2026-06-01T12:00:00Z")},
	}); !errors.Is(err, wire.ErrForbidden) {
		t.Fatalf("PatchStatus since err = %v, want ErrForbidden", err)
	}

	// PatchStatus: observedGeneration regression -> 422 ErrObserved.
	if _, err := env.client.PatchStatus(ctx, "acme", webObj, []patch.Op{
		{Kind: patch.OpSet, Path: "status/observedGeneration", Value: lkal.Int(2)},
	}); !errors.Is(err, wire.ErrObserved) {
		t.Fatalf("PatchStatus observed regression err = %v, want ErrObserved", err)
	}

	// UpdateStatus via PUT.
	env.clock.Advance(time.Minute)
	putStatusCand := rec(
		fld("meta", rec(
			fld("kind", lkal.String("workload")),
			fld("name", lkal.String("web")),
		)),
		fld("status", rec(
			fld("phase", lkal.String("Scaled")),
			fld("observedGeneration", lkal.Int(3)),
			fld("readyReplicas", lkal.Int(5)),
		)),
	)
	updatedStatus, err := env.client.UpdateStatus(ctx, "acme", webObj, putStatusCand)
	if err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	usStatus, _ := getRecord(updatedStatus, "status")
	if getString(usStatus, "phase") != "Scaled" || getInt(usStatus, "readyReplicas") != 5 {
		t.Fatalf("unexpected status after UpdateStatus: %+v", usStatus)
	}

	// Create 2 more workloads to test List pagination (maxListLimit = 2, total = 3).
	for _, nm := range []string{"api", "worker"} {
		if _, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "workload", workloadDoc(nm, 2)); err != nil {
			t.Fatalf("Create(%s): %v", nm, err)
		}
	}

	// Raw HTTP GET without limit is bounded by MaxListLimit (2 items + non-empty Continue).
	rawReq, err := http.NewRequestWithContext(ctx, http.MethodGet, env.baseURL+"/t/acme/o/core/workload", nil)
	if err != nil {
		t.Fatalf("NewRequest raw list: %v", err)
	}
	rawResp, err := env.rawHC.Do(rawReq)
	if err != nil {
		t.Fatalf("Do raw list: %v", err)
	}
	rawBody, err := io.ReadAll(rawResp.Body)
	_ = rawResp.Body.Close()
	if err != nil || rawResp.StatusCode != http.StatusOK {
		t.Fatalf("raw list status=%d err=%v body=%s", rawResp.StatusCode, err, rawBody)
	}
	clampedRes, err := wire.DecodeList(rawBody)
	if err != nil || len(clampedRes.Items) != 2 || clampedRes.Continue == "" {
		t.Fatalf("expected server to clamp omitted limit to MaxListLimit=2 with continue, got count=%d continue=%q err=%v", len(clampedRes.Items), clampedRes.Continue, err)
	}

	// Client.List with Limit == 0 transparently auto-paginates all items at a single snapshot rev (3 items > maxListLimit 2).
	allRes, err := env.client.List(ctx, "acme", keyspace.CoreGroup, "workload", resource.ListOptions{})
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(allRes.Items) != 3 || allRes.Continue != "" || allRes.Revision < 1 {
		t.Fatalf("unexpected List(all): count=%d continue=%q rev=%d", len(allRes.Items), allRes.Continue, allRes.Revision)
	}

	// Paginated List limit=2 then continue at snapshot rev.
	page1, err := env.client.List(ctx, "acme", keyspace.CoreGroup, "workload", resource.ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("List(page1): %v", err)
	}
	if len(page1.Items) != 2 || page1.Continue == "" {
		t.Fatalf("expected 2 items + continue, got count=%d continue=%q", len(page1.Items), page1.Continue)
	}
	page2, err := env.client.List(ctx, "acme", keyspace.CoreGroup, "workload", resource.ListOptions{
		AtRev:    page1.Revision,
		Limit:    2,
		Continue: page1.Continue,
	})
	if err != nil {
		t.Fatalf("List(page2): %v", err)
	}
	if len(page2.Items) != 1 || page2.Continue != "" {
		t.Fatalf("expected 1 item + empty continue, got count=%d continue=%q", len(page2.Items), page2.Continue)
	}

	// WatchList (sendInitialEvents=true) + live Put/Delete + non-core group.
	watchCtx, cancelWatch := context.WithCancel(ctx)
	defer cancelWatch()
	ws, err := env.client.Watch(watchCtx, "acme", keyspace.CoreGroup, "workload", wire.WatchOptions{
		SendInitialEvents: true,
	})
	if err != nil {
		t.Fatalf("Watch(SendInitialEvents): %v", err)
	}
	defer func() { _ = ws.Close() }()
	if ws.ProtoMajor() != 2 {
		t.Fatalf("WatchStream ProtoMajor = %d, want 2", ws.ProtoMajor())
	}

	initialNames := make([]string, 0, 3)
	var snapRev store.Rev
	for {
		ev, ok := ws.Next()
		if !ok {
			t.Fatalf("watch ended early during initial events: err=%v", ws.Err())
		}
		if ev.Type == wire.EventPut {
			m, _ := getRecord(*ev.Object, "meta")
			initialNames = append(initialNames, getString(m, "name"))
		} else if ev.Type == wire.EventBookmark && ev.InitialEventsEnd {
			snapRev = ev.Revision
			break
		}
	}
	if strings.Join(initialNames, ",") != "api,web,worker" || snapRev < 1 {
		t.Fatalf("unexpected WatchList initial state: names=%v snapRev=%d", initialNames, snapRev)
	}

	// Trigger live Put and conditional Delete, verify events arrive on stream.
	createdJob, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "workload", workloadDoc("job", 1))
	if err != nil {
		t.Fatalf("Create(job): %v", err)
	}
	jobMeta, _ := getRecord(createdJob, "meta")
	jobRev := getInt(jobMeta, "revision")
	jobObj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "job"}

	// Delete with wrong rev -> 409 Conflict.
	if err := env.client.Delete(ctx, "acme", jobObj, jobRev+99); !errors.Is(err, wire.ErrConflict) {
		t.Fatalf("Delete stale rev err = %v, want ErrConflict", err)
	}
	if err := env.client.Delete(ctx, "acme", jobObj, jobRev); err != nil {
		t.Fatalf("Delete(job): %v", err)
	}

	sawPut, sawDel := false, false
	for !sawPut || !sawDel {
		ev, ok := ws.Next()
		if !ok {
			t.Fatalf("watch ended before live events: err=%v", ws.Err())
		}
		switch ev.Type {
		case wire.EventPut:
			m, _ := getRecord(*ev.Object, "meta")
			if getString(m, "name") == "job" {
				sawPut = true
			}
		case wire.EventDelete:
			if ev.Target != nil && ev.Target.Group == keyspace.CoreGroup && ev.Target.Kind == "workload" && ev.Target.Name == "job" {
				sawDel = true
			}
		}
	}
	cancelWatch()

	// Non-core group CRUD + Delete watch target includes group.
	wWatchCtx, cancelWWatch := context.WithCancel(ctx)
	defer cancelWWatch()
	wws, err := env.client.Watch(wWatchCtx, "acme", "apps.example.io", "widget", wire.WatchOptions{SendInitialEvents: true})
	if err != nil {
		t.Fatalf("Watch(widget): %v", err)
	}
	defer func() { _ = wws.Close() }()
	// Consume InitialEventsEnd bookmark.
	if ev, ok := wws.Next(); !ok || ev.Type != wire.EventBookmark || !ev.InitialEventsEnd {
		t.Fatalf("expected InitialEventsEnd bookmark for empty widget list, got %+v ok=%v err=%v", ev, ok, wws.Err())
	}
	if _, err := env.client.Create(ctx, "acme", "apps.example.io", "widget", widgetDoc("w1", 7)); err != nil {
		t.Fatalf("Create(widget): %v", err)
	}
	if err := env.client.Delete(ctx, "acme", keyspace.Object{Group: "apps.example.io", Kind: "widget", Name: "w1"}, 0); err != nil {
		t.Fatalf("Delete(widget): %v", err)
	}
	sawWPut, sawWDel := false, false
	for !sawWPut || !sawWDel {
		ev, ok := wws.Next()
		if !ok {
			t.Fatalf("widget watch ended early: %v", wws.Err())
		}
		if ev.Type == wire.EventPut {
			sawWPut = true
		} else if ev.Type == wire.EventDelete && ev.Target != nil && ev.Target.Group == "apps.example.io" && ev.Target.Kind == "widget" && ev.Target.Name == "w1" {
			sawWDel = true
		}
	}
	cancelWWatch()
}

func TestWatchNameFilterAndBookmarks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		env := startTestEnv(t, envOpts{
			cacheCfg: watchcache.Config{
				ProgressInterval: 10 * time.Millisecond,
				BookmarkInterval: 30 * time.Millisecond,
				ReadWait:         time.Second,
			},
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		for _, nm := range []string{"web", "web2", "web-staging"} {
			if _, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "workload", workloadDoc(nm, 1)); err != nil {
				t.Fatalf("Create(%s): %v", nm, err)
			}
		}

		wCtx, cancelW := context.WithCancel(ctx)
		defer cancelW()
		ws, err := env.client.Watch(wCtx, "acme", keyspace.CoreGroup, "workload", wire.WatchOptions{
			SendInitialEvents: true,
			Name:              "web",
		})
		if err != nil {
			t.Fatalf("Watch(name=web): %v", err)
		}
		defer func() { _ = ws.Close() }()

		var gotInit []string
		for {
			ev, ok := ws.Next()
			if !ok {
				t.Fatalf("watch ended early: %v", ws.Err())
			}
			if ev.Type == wire.EventPut {
				m, _ := getRecord(*ev.Object, "meta")
				gotInit = append(gotInit, getString(m, "name"))
			} else if ev.Type == wire.EventBookmark && ev.InitialEventsEnd {
				break
			}
		}
		if strings.Join(gotInit, ",") != "web" {
			t.Fatalf("name filter initial events = %v, want [web]", gotInit)
		}

		// Mutate prefix-colliding names web2 & web-staging, then mutate and delete web.
		for _, nm := range []string{"web2", "web-staging"} {
			obj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: nm}
			if _, err := env.client.Patch(ctx, "acme", obj, []patch.Op{
				{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(9)},
			}); err != nil {
				t.Fatalf("Patch(%s): %v", nm, err)
			}
			if err := env.client.Delete(ctx, "acme", obj, 0); err != nil {
				t.Fatalf("Delete(%s): %v", nm, err)
			}
		}
		webObj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "web"}
		if _, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
			{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(4)},
		}); err != nil {
			t.Fatalf("Patch(web): %v", err)
		}
		if err := env.client.Delete(ctx, "acme", webObj, 0); err != nil {
			t.Fatalf("Delete(web): %v", err)
		}

		sawPut, sawDel, sawBM := false, false, false
		for !sawPut || !sawDel || !sawBM {
			ev, ok := ws.Next()
			if !ok {
				t.Fatalf("watch ended early: %v", ws.Err())
			}
			switch ev.Type {
			case wire.EventPut:
				m, _ := getRecord(*ev.Object, "meta")
				if nm := getString(m, "name"); nm != "web" {
					t.Fatalf("name filter leaked Put for %q", nm)
				}
				sawPut = true
			case wire.EventDelete:
				if ev.Target == nil || ev.Target.Name != "web" {
					t.Fatalf("name filter leaked Delete for %+v", ev.Target)
				}
				sawDel = true
			case wire.EventBookmark:
				if !ev.InitialEventsEnd {
					sawBM = true
				}
			}
		}
	})
}

func TestWatchGoneInitialAndMidStream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		env := startTestEnv(t, envOpts{
			cacheCfg: watchcache.Config{
				MinEvents:        2,
				MaxEvents:        4,
				MinAge:           time.Nanosecond,
				ProgressInterval: 5 * time.Millisecond,
				BookmarkInterval: time.Hour,
				ReadWait:         200 * time.Millisecond,
			},
		})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// Create and warm the cache.
		created, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "workload", workloadDoc("web", 1))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		m, _ := getRecord(created, "meta")
		firstRev := store.Rev(getInt(m, "revision"))
		webObj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "web"}

		if _, err := env.client.Get(ctx, "acme", webObj, 0); err != nil {
			t.Fatalf("warm cache Get: %v", err)
		}

		// Produce > MaxEvents mutations so firstRev is evicted from ring and compacted.
		var latestRev store.Rev
		for i := int64(2); i <= 10; i++ {
			updated, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
				{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(i)},
			})
			if err != nil {
				t.Fatalf("Patch %d: %v", i, err)
			}
			um, _ := getRecord(updated, "meta")
			latestRev = store.Rev(getInt(um, "revision"))
		}
		if _, err := env.caches.Compact(ctx, latestRev); err != nil {
			t.Fatalf("Manager.Compact: %v", err)
		}

		// Pre-stream Watch from evicted/compacted rev -> HTTP 410 Gone.
		if _, err := env.client.Watch(ctx, "acme", keyspace.CoreGroup, "workload", wire.WatchOptions{FromRev: firstRev}); !errors.Is(err, wire.ErrGone) {
			t.Fatalf("Watch(old rev) err = %v, want ErrGone", err)
		} else {
			var we *wire.Error
			if !errors.As(err, &we) || we.Status != http.StatusGone {
				t.Fatalf("expected 410 *wire.Error, got %+v", we)
			}
		}

		// Historical Get at compacted rev -> HTTP 410 Gone.
		if _, err := env.client.Get(ctx, "acme", webObj, firstRev); !errors.Is(err, wire.ErrGone) {
			t.Fatalf("Get(compacted rev) err = %v, want ErrGone", err)
		}

		// Future revision Watch -> 422 Unprocessable Content.
		if _, err := env.client.Watch(ctx, "acme", keyspace.CoreGroup, "workload", wire.WatchOptions{FromRev: latestRev + 1000}); !errors.Is(err, wire.ErrUnprocessable) {
			t.Fatalf("Watch(future rev) err = %v, want ErrUnprocessable", err)
		}

		// Mid-stream .gone terminal error when watcher lags behind MaxEvents.
		// Use a client with small HTTP/2 receive window and large payloads so server writes stall
		// while the cache ring evicts the watcher's cursor.
		var h2cProto http.Protocols
		h2cProto.SetUnencryptedHTTP2(true)
		slowClient := wire.NewClient(env.baseURL, &http.Client{
			Transport: &http.Transport{
				DialContext: env.net.DialContext,
				Protocols:   &h2cProto,
				HTTP2: &http.HTTP2Config{
					MaxReceiveBufferPerStream:     64 << 10,
					MaxReceiveBufferPerConnection: 64 << 10,
				},
			},
		})
		defer slowClient.CloseIdleConnections()

		wCtx, cancelW := context.WithCancel(ctx)
		defer cancelW()
		slowStream, err := slowClient.Watch(wCtx, "acme", keyspace.CoreGroup, "workload", wire.WatchOptions{
			SendInitialEvents: true,
		})
		if err != nil {
			t.Fatalf("slow Watch: %v", err)
		}
		defer func() { _ = slowStream.Close() }()

		// Drain initial events until InitialEventsEnd so the watch stream is live.
		for {
			ev, ok := slowStream.Next()
			if !ok {
				t.Fatalf("slowStream ended during initial state: %v", slowStream.Err())
			}
			if ev.Type == wire.EventBookmark && ev.InitialEventsEnd {
				break
			}
		}

		// Pause reading slowStream while writing large updates exceeding both the HTTP/2 stream window and MaxEvents.
		big := strings.Repeat("x", 32<<10)
		for i := 0; i < 20; i++ {
			if _, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
				{Kind: patch.OpSet, Path: "spec/payload", Value: lkal.String(fmt.Sprintf("%02d-%s", i, big))},
			}); err != nil {
				t.Fatalf("burst Patch %d: %v", i, err)
			}
			// Ensure cache syncLoop advances through each revision.
			if _, err := env.client.Get(ctx, "acme", webObj, 0); err != nil {
				t.Fatalf("sync Get %d: %v", i, err)
			}
		}

		// Now drain slowStream; it must terminate with wire.ErrGone from the terminal .error NDJSON event.
		for {
			_, ok := slowStream.Next()
			if !ok {
				break
			}
		}
		if !errors.Is(slowStream.Err(), wire.ErrGone) {
			t.Fatalf("lagging watcher Err = %v, want wire.ErrGone", slowStream.Err())
		}

		// Closed watchcache manager -> 503 Service Unavailable.
		env.caches.Close()
		if _, err := env.client.Watch(ctx, "acme", keyspace.CoreGroup, "workload", wire.WatchOptions{}); !errors.Is(err, wire.ErrUnavailable) {
			t.Fatalf("Watch(closed manager) err = %v, want ErrUnavailable", err)
		}
	})
}

func TestAuthAndCedarRefAuthorization(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ps, err := cedar.NewPolicySetFromBytes("policy.cedar", []byte(`
	permit(
	  principal == Nayutar::User::"alice",
	  action == Nayutar::Action::"Reference",
	  resource
	) when {
	  context.tenant == "acme" && resource.kind == "secret"
	};
	`))
		if err != nil {
			t.Fatalf("parse cedar policy: %v", err)
		}
		authz, err := refcedar.New(ps, nil)
		if err != nil {
			t.Fatalf("refcedar.New: %v", err)
		}

		authFn := func(r *http.Request) (refs.Principal, error) {
			switch r.Header.Get("X-Test-User") {
			case "alice":
				return refs.Principal{Type: "Nayutar::User", ID: "alice"}, nil
			case "bob":
				return refs.Principal{Type: "Nayutar::User", ID: "bob"}, nil
			default:
				return refs.Principal{}, errors.New("missing or invalid token")
			}
		}

		env := startTestEnv(t, envOpts{
			authFn:     authFn,
			authorizer: authz,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Unauthenticated requests -> 401 Unauthorized on reads, writes, and watches.
		if _, err := env.client.List(ctx, "acme", keyspace.CoreGroup, "workload", resource.ListOptions{}); !errors.Is(err, wire.ErrUnauthorized) {
			t.Fatalf("unauth List err = %v, want ErrUnauthorized", err)
		}
		if _, err := env.client.Watch(ctx, "acme", keyspace.CoreGroup, "workload", wire.WatchOptions{}); !errors.Is(err, wire.ErrUnauthorized) {
			t.Fatalf("unauth Watch err = %v, want ErrUnauthorized", err)
		}
		if _, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "workload", workloadDoc("w", 1)); !errors.Is(err, wire.ErrUnauthorized) {
			t.Fatalf("unauth Create err = %v, want ErrUnauthorized", err)
		}

		var currentUser string
		env.client.BeforeRequest = func(req *http.Request) {
			if currentUser != "" {
				req.Header.Set("X-Test-User", currentUser)
			}
		}

		// Alice creates secret/db and workload/web.
		currentUser = "alice"
		secretDoc := rec(
			fld("meta", rec(fld("kind", lkal.String("secret")), fld("name", lkal.String("db")))),
			fld("spec", rec(fld("data", lkal.String("pw")))),
		)
		if _, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "secret", secretDoc); err != nil {
			t.Fatalf("Create secret: %v", err)
		}
		if _, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "workload", workloadDoc("web", 1)); err != nil {
			t.Fatalf("Create workload: %v", err)
		}
		webObj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "web"}

		// Bob tries to patch a reference to secret/db -> Cedar denies -> 403 Forbidden.
		currentUser = "bob"
		if _, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
			{Kind: patch.OpSet, Path: "spec/secretRef", Value: lkal.Ref{"secret", "db"}},
		}); !errors.Is(err, wire.ErrForbidden) {
			t.Fatalf("bob Patch ref err = %v, want ErrForbidden", err)
		}

		// Alice tries to reference disallowed target kind -> 422 ErrRefKind.
		currentUser = "alice"
		if _, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
			{Kind: patch.OpSet, Path: "spec/secretRef", Value: lkal.Ref{"workload", "web"}},
		}); !errors.Is(err, wire.ErrRefKind) {
			t.Fatalf("alice Patch wrong ref kind err = %v, want ErrRefKind", err)
		}

		// Alice tries to reference missing secret -> 422 ErrRefNotFound (Cedar permits? Wait: resource.kind doesn't exist when target absent -> denied! Check missing secret with policy allowing missing target or check allowed target).
		// Alice references existing secret/db -> succeeds!
		if _, err := env.client.Patch(ctx, "acme", webObj, []patch.Op{
			{Kind: patch.OpSet, Path: "spec/secretRef", Value: lkal.Ref{"secret", "db"}},
		}); err != nil {
			t.Fatalf("alice Patch allowed ref: %v", err)
		}
	})
}

func TestRefNotFoundErrorMapping(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ps, err := cedar.NewPolicySetFromBytes("allow-all.cedar", []byte(`
	permit(principal, action == Nayutar::Action::"Reference", resource);
	`))
		if err != nil {
			t.Fatalf("parse policy: %v", err)
		}
		authz, err := refcedar.New(ps, nil)
		if err != nil {
			t.Fatalf("refcedar.New: %v", err)
		}
		env := startTestEnv(t, envOpts{
			authorizer: authz,
			authFn: func(*http.Request) (refs.Principal, error) {
				return refs.Principal{Type: "Nayutar::User", ID: "alice"}, nil
			},
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		doc := rec(
			fld("meta", rec(fld("kind", lkal.String("workload")), fld("name", lkal.String("web")))),
			fld("spec", rec(fld("secretRef", lkal.Ref{"secret", "missing"}))),
		)
		if _, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "workload", doc); !errors.Is(err, wire.ErrRefNotFound) {
			t.Fatalf("Create with missing ref err = %v, want ErrRefNotFound", err)
		}
	})
}

func TestProtocolHardeningAndHeaders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		env := startTestEnv(t, envOpts{maxBodyBytes: 256})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		doRaw := func(method, path string, headers map[string]string, body []byte) (*http.Response, []byte) {
			t.Helper()
			var r io.Reader
			if body != nil {
				r = bytes.NewReader(body)
			}
			req, err := http.NewRequestWithContext(ctx, method, env.baseURL+path, r)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			for k, v := range headers {
				req.Header.Set(k, v)
			}
			resp, err := env.rawHC.Do(req)
			if err != nil {
				t.Fatalf("Do(%s %s): %v", method, path, err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.ProtoMajor != 2 {
				t.Fatalf("expected HTTP/2, got HTTP/%d", resp.ProtoMajor)
			}
			assertSecurityHeaders(t, resp.Header)
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			return resp, data
		}

		// 405 Method Not Allowed with exact Allow & Accept-Patch headers (including TRACE).
		resp, data := doRaw(http.MethodTrace, "/t/acme/o/core/workload", nil, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, POST" || resp.Header.Get("Accept-Patch") != "" {
			t.Fatalf("TRACE collection: status=%d Allow=%q Accept-Patch=%q body=%s", resp.StatusCode, resp.Header.Get("Allow"), resp.Header.Get("Accept-Patch"), data)
		}
		resp, _ = doRaw(http.MethodPost, "/t/acme/o/core/workload/web", nil, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, PUT, PATCH, DELETE" || resp.Header.Get("Accept-Patch") != wire.MediaTypePatch {
			t.Fatalf("POST object: status=%d Allow=%q Accept-Patch=%q", resp.StatusCode, resp.Header.Get("Allow"), resp.Header.Get("Accept-Patch"))
		}
		resp, _ = doRaw(http.MethodGet, "/t/acme/o/core/workload/web/status", nil, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "PUT, PATCH" || resp.Header.Get("Accept-Patch") != wire.MediaTypePatch {
			t.Fatalf("GET status: status=%d Allow=%q Accept-Patch=%q", resp.StatusCode, resp.Header.Get("Allow"), resp.Header.Get("Accept-Patch"))
		}
		resp, _ = doRaw(http.MethodTrace, "/unknown", nil, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("TRACE unknown: status=%d, want 405", resp.StatusCode)
		}

		// 406 Not Acceptable negotiation (RFC 9110 §12.5.1).
		for _, badAccept := range []string{
			"application/cbor",
			"text/plain",
			wire.MediaTypeResource + ";q=0",
			wire.MediaTypeResource + ";q=0, */*;q=1",
			"*/*;q=0",
			"application/*;q=0",
			wire.MediaTypeResource + ";version=2",
		} {
			resp, data := doRaw(http.MethodGet, "/t/acme/o/core/workload", map[string]string{"Accept": badAccept}, nil)
			if resp.StatusCode != http.StatusNotAcceptable {
				t.Fatalf("Accept=%q -> status %d (%s), want 406", badAccept, resp.StatusCode, data)
			}
			we, err := wire.DecodeError(data)
			if err != nil || !errors.Is(we, wire.ErrNotAcceptable) {
				t.Fatalf("Accept=%q decode err=%v we=%+v", badAccept, err, we)
			}
		}
		for _, goodAccept := range []string{
			"",
			"*/*",
			"application/*",
			wire.MediaTypeResource,
			wire.MediaTypeResource + "; charset=utf-8",
			"application/cbor, " + wire.MediaTypeResource + ";q=0.5",
		} {
			hdr := map[string]string{}
			if goodAccept != "" {
				hdr["Accept"] = goodAccept
			}
			resp, data := doRaw(http.MethodGet, "/t/acme/o/core/workload", hdr, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("Accept=%q -> status %d (%s), want 200", goodAccept, resp.StatusCode, data)
			}
		}

		// 415 Unsupported Media Type (including Accept-Patch on patchable routes).
		validCand, _ := resource.Encode(workloadDoc("web", 1))
		validPatch, _ := patch.Encode([]patch.Op{})
		for _, ct := range []string{"", "application/json", wire.MediaTypePatch, wire.MediaTypeResource + "; charset=latin1", wire.MediaTypeResource + "; foo=bar"} {
			hdr := map[string]string{}
			if ct != "" {
				hdr["Content-Type"] = ct
			}
			resp, _ := doRaw(http.MethodPost, "/t/acme/o/core/workload", hdr, validCand)
			if resp.StatusCode != http.StatusUnsupportedMediaType {
				t.Fatalf("POST Content-Type=%q -> %d, want 415", ct, resp.StatusCode)
			}
		}
		resp, _ = doRaw(http.MethodPatch, "/t/acme/o/core/workload/web", map[string]string{"Content-Type": wire.MediaTypeResource}, validPatch)
		if resp.StatusCode != http.StatusUnsupportedMediaType || resp.Header.Get("Accept-Patch") != wire.MediaTypePatch {
			t.Fatalf("PATCH wrong CT -> status=%d Accept-Patch=%q", resp.StatusCode, resp.Header.Get("Accept-Patch"))
		}
		// Charset=utf-8 allowed on POST and PATCH.
		resp, data = doRaw(http.MethodPost, "/t/acme/o/core/workload", map[string]string{"Content-Type": wire.MediaTypeResource + "; charset=UTF-8"}, validCand)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST charset=UTF-8 -> %d (%s), want 201", resp.StatusCode, data)
		}
		resp, data = doRaw(http.MethodPatch, "/t/acme/o/core/workload/web", map[string]string{"Content-Type": wire.MediaTypePatch + "; charset=utf-8"}, validPatch)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Accept-Patch") != wire.MediaTypePatch {
			t.Fatalf("PATCH charset=utf-8 -> %d (%s), want 200", resp.StatusCode, data)
		}

		// 413 Content Too Large when exceeding MaxBodyBytes (256).
		oversized := append([]byte(`{"$lkal":"0.1","meta":{"kind":"workload","name":"big"},"spec":{"payload":"`), bytes.Repeat([]byte("a"), 300)...)
		oversized = append(oversized, []byte(`"}}`)...)
		resp, data = doRaw(http.MethodPost, "/t/acme/o/core/workload", map[string]string{"Content-Type": wire.MediaTypeResource}, oversized)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversized POST -> %d (%s), want 413", resp.StatusCode, data)
		}
		if we, err := wire.DecodeError(data); err != nil || !errors.Is(we, wire.ErrTooLarge) {
			t.Fatalf("413 body decode: err=%v we=%+v", err, we)
		}

		// GET and DELETE reject non-empty bodies with 400 Bad Request.
		resp, _ = doRaw(http.MethodGet, "/t/acme/o/core/workload/web", nil, []byte("x"))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET with body -> %d, want 400", resp.StatusCode)
		}
		resp, _ = doRaw(http.MethodDelete, "/t/acme/o/core/workload/web", nil, []byte("x"))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("DELETE with body -> %d, want 400", resp.StatusCode)
		}

		// Unknown query parameters rejected with 400 on every route.
		for _, tc := range []struct {
			method string
			path   string
			ct     string
			body   []byte
		}{
			{http.MethodGet, "/t/acme/o/core/workload?unknown=1", "", nil},
			{http.MethodGet, "/t/acme/o/core/workload?%FF=1", "", nil},
			{http.MethodGet, "/t/acme/o/core/workload?%FF=1&%FF=2", "", nil},
			{http.MethodGet, "/t/acme/o/core/workload?atRev=%FF", "", nil},
			{http.MethodGet, "/t/acme/o/core/workload?limit=0", "", nil},
			{http.MethodGet, "/t/acme/o/core/workload?watch=true&atRev=1", "", nil},
			{http.MethodGet, "/t/acme/o/core/workload?watch=false", "", nil},
			{http.MethodGet, "/t/acme/o/core/workload?continue=", "", nil},
			{http.MethodGet, "/t/acme/o/core/workload?continue=bad/prefix", "", nil},
			{http.MethodGet, "/t/acme/o/core/workload?continue=o/core/workload/", "", nil},
			{http.MethodGet, "/t/acme/o/core/workload?continue=o/core/workload/%00", "", nil},
			{http.MethodGet, "/t/acme/o/core/workload?continue=o/core/secret/a", "", nil},
			{http.MethodGet, "/t/acme/o/core/workload/web?limit=1", "", nil},
			{http.MethodPost, "/t/acme/o/core/workload?foo=bar", wire.MediaTypeResource, validCand},
			{http.MethodPut, "/t/acme/o/core/workload/web?foo=bar", wire.MediaTypeResource, validCand},
			{http.MethodPatch, "/t/acme/o/core/workload/web?foo=bar", wire.MediaTypePatch, validPatch},
			{http.MethodPut, "/t/acme/o/core/workload/web/status?foo=bar", wire.MediaTypeResource, validCand},
			{http.MethodPatch, "/t/acme/o/core/workload/web/status?foo=bar", wire.MediaTypePatch, validPatch},
			{http.MethodDelete, "/t/acme/o/core/workload/web?foo=bar", "", nil},
			{http.MethodDelete, "/t/acme/o/core/workload/web?rev=0", "", nil},
		} {
			hdr := map[string]string{}
			if tc.ct != "" {
				hdr["Content-Type"] = tc.ct
			}
			resp, data := doRaw(tc.method, tc.path, hdr, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("%s %s -> %d (%s), want 400", tc.method, tc.path, resp.StatusCode, data)
			}
			if we, err := wire.DecodeError(data); err != nil || !errors.Is(we, wire.ErrBadRequest) {
				t.Fatalf("%s %s error decode: err=%v we=%+v", tc.method, tc.path, err, we)
			}
		}

		// PATCH invalid JSON syntax -> 400; valid LKAL-JSON violating patch schema -> 422.
		resp, _ = doRaw(http.MethodPatch, "/t/acme/o/core/workload/web", map[string]string{"Content-Type": wire.MediaTypePatch}, []byte(`{broken`))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("PATCH invalid JSON -> %d, want 400", resp.StatusCode)
		}
		resp, _ = doRaw(http.MethodPatch, "/t/acme/o/core/workload/web", map[string]string{"Content-Type": wire.MediaTypePatch}, []byte(`{"$lkal":"0.1","wrong":{}}`))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("PATCH bad schema -> %d, want 422", resp.StatusCode)
		}

		// DELETE 204 omits Content-Type and has empty body.
		resp, data = doRaw(http.MethodDelete, "/t/acme/o/core/workload/web", nil, nil)
		if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Content-Type") != "" || len(data) != 0 {
			t.Fatalf("DELETE 204: status=%d CT=%q body=%q", resp.StatusCode, resp.Header.Get("Content-Type"), data)
		}
	})
}

type countingStore struct {
	store.Store
	watches atomic.Int64
	ranges  atomic.Int64
}

func (c *countingStore) Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
	c.watches.Add(1)
	return c.Store.Watch(ctx, prefix, fromRev)
}

func (c *countingStore) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	c.ranges.Add(1)
	return c.Store.Range(ctx, prefix, atRev, limit, cont)
}

func TestDoSProtectionBeforeWatchcache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cs := &countingStore{Store: memstore.New()}
		env := startTestEnv(t, envOpts{root: cs})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Seed a terminating tenant directly.
		if err := env.reg.Register(ctx, "dying"); err != nil {
			t.Fatalf("Register(dying): %v", err)
		}
		_, _, err := cs.Txn(ctx, nil, []store.Op{{
			Type:  store.OpPut,
			Key:   "/sys/tenants/dying",
			Value: []byte(`{"$lkal":"0.1","state":{"$enum":"terminating"}}`),
		}})
		if err != nil {
			t.Fatalf("set dying terminating: %v", err)
		}

		// Unknown kind -> 404 on GET/List/Watch/Delete, 422 on POST.
		if _, err := env.client.List(ctx, "acme", keyspace.CoreGroup, "unknownkind", resource.ListOptions{}); !errors.Is(err, wire.ErrNotFound) {
			t.Fatalf("List unknown kind err = %v, want ErrNotFound", err)
		}
		if _, err := env.client.Watch(ctx, "acme", keyspace.CoreGroup, "unknownkind", wire.WatchOptions{}); !errors.Is(err, wire.ErrNotFound) {
			t.Fatalf("Watch unknown kind err = %v, want ErrNotFound", err)
		}
		if _, err := env.client.Get(ctx, "acme", keyspace.Object{Group: "core", Kind: "unknownkind", Name: "x"}, 0); !errors.Is(err, wire.ErrNotFound) {
			t.Fatalf("Get unknown kind err = %v, want ErrNotFound", err)
		}
		if err := env.client.Delete(ctx, "acme", keyspace.Object{Group: "core", Kind: "unknownkind", Name: "x"}, 0); !errors.Is(err, wire.ErrNotFound) {
			t.Fatalf("Delete unknown kind err = %v, want ErrNotFound", err)
		}
		unknownCand := rec(
			fld("meta", rec(fld("kind", lkal.String("unknownkind")), fld("name", lkal.String("x")))),
		)
		if _, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "unknownkind", unknownCand); !errors.Is(err, wire.ErrUnprocessable) {
			t.Fatalf("Create unknown kind err = %v, want ErrUnprocessable", err)
		}

		// Unknown tenant -> 404.
		if _, err := env.client.List(ctx, "ghost", keyspace.CoreGroup, "workload", resource.ListOptions{}); !errors.Is(err, wire.ErrNotFound) {
			t.Fatalf("List ghost tenant err = %v, want ErrNotFound", err)
		}
		if _, err := env.client.Watch(ctx, "ghost", keyspace.CoreGroup, "workload", wire.WatchOptions{}); !errors.Is(err, wire.ErrNotFound) {
			t.Fatalf("Watch ghost tenant err = %v, want ErrNotFound", err)
		}

		// Terminating tenant -> 409 Conflict on reads, watches, and writes.
		if _, err := env.client.List(ctx, "dying", keyspace.CoreGroup, "workload", resource.ListOptions{}); !errors.Is(err, wire.ErrConflict) {
			t.Fatalf("List terminating tenant err = %v, want ErrConflict", err)
		}
		if _, err := env.client.Watch(ctx, "dying", keyspace.CoreGroup, "workload", wire.WatchOptions{}); !errors.Is(err, wire.ErrConflict) {
			t.Fatalf("Watch terminating tenant err = %v, want ErrConflict", err)
		}
		if _, err := env.client.Create(ctx, "dying", keyspace.CoreGroup, "workload", workloadDoc("w", 1)); !errors.Is(err, wire.ErrConflict) {
			t.Fatalf("Create terminating tenant err = %v, want ErrConflict", err)
		}

		// Malformed segments and invalid continue tokens -> 400 Bad Request without warming watchcache.
		if _, err := env.client.List(ctx, "INVALID_TENANT", keyspace.CoreGroup, "workload", resource.ListOptions{}); !errors.Is(err, wire.ErrBadRequest) {
			t.Fatalf("List bad tenant err = %v, want ErrBadRequest", err)
		}
		for _, badCont := range []string{"o/core/workload/", "o/core/workload/\x00", "o/core/secret/a", "bad/prefix"} {
			if _, err := env.client.List(ctx, "acme", keyspace.CoreGroup, "workload", resource.ListOptions{Continue: badCont}); !errors.Is(err, wire.ErrBadRequest) {
				t.Fatalf("List invalid continue %q err = %v, want ErrBadRequest", badCont, err)
			}
		}

		// Zero watchcache goroutines / store Range or Watch calls should have been spawned!
		if cs.watches.Load() != 0 || cs.ranges.Load() != 0 {
			t.Fatalf("DoS check failed: watchcache spawned store Watch=%d Range=%d calls for invalid requests", cs.watches.Load(), cs.ranges.Load())
		}
		if _, ok := env.caches.CompactBound(); ok {
			t.Fatal("DoS check failed: expected no ready caches in Manager")
		}
	})
}

type failingStore struct {
	store.Store
	failReads atomic.Bool
}

func (f *failingStore) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	if f.failReads.Load() && strings.HasPrefix(key, "/t/") {
		return store.KV{}, 0, errors.New("secret-backend-failure on key " + key)
	}
	return f.Store.Get(ctx, key, atRev)
}

func TestInternalErrorRedaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fs := &failingStore{Store: memstore.New()}
		env := startTestEnv(t, envOpts{root: fs})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Write corrupt bytes directly under a valid object key.
		corruptKey := "/t/acme/o/core/workload/corrupt-obj"
		if _, _, err := fs.Txn(ctx, nil, []store.Op{{
			Type:  store.OpPut,
			Key:   corruptKey,
			Value: []byte(`{"not":"valid lkal"}`),
		}}); err != nil {
			t.Fatalf("inject corrupt value: %v", err)
		}

		checkRedacted := func(label string, err error) {
			t.Helper()
			var we *wire.Error
			if !errors.As(err, &we) {
				t.Fatalf("%s: expected *wire.Error, got %T (%v)", label, err, err)
			}
			if we.Status != http.StatusInternalServerError || we.Kind != wire.KindInternal {
				t.Fatalf("%s: got status=%d kind=%s, want 500 internal", label, we.Status, we.Kind)
			}
			if we.Message != "internal server error" || we.Path != "" {
				t.Fatalf("%s: leaked internal details: msg=%q path=%q", label, we.Message, we.Path)
			}
		}

		corruptObj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "corrupt-obj"}
		_, err := env.client.Get(ctx, "acme", corruptObj, 0)
		checkRedacted("GET corrupt", err)

		_, err = env.client.List(ctx, "acme", keyspace.CoreGroup, "workload", resource.ListOptions{})
		checkRedacted("List corrupt", err)

		_, err = env.client.Patch(ctx, "acme", corruptObj, []patch.Op{{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(2)}})
		checkRedacted("Patch corrupt", err)

		// Watch encountering corrupt stored object emits terminal .error (500, redacted) and stops cleanly.
		ws, err := env.client.Watch(ctx, "acme", keyspace.CoreGroup, "workload", wire.WatchOptions{SendInitialEvents: true})
		if err != nil {
			t.Fatalf("Watch(corrupt): %v", err)
		}
		defer func() { _ = ws.Close() }()
		if ev, ok := ws.Next(); ok {
			t.Fatalf("expected corrupt object to terminate watch with error, got event %+v", ev)
		}
		checkRedacted("Watch corrupt", ws.Err())

		// Store I/O error with sensitive key path -> redacted 500.
		fs.failReads.Store(true)
		_, err = env.client.Update(ctx, "acme", corruptObj, workloadDoc("corrupt-obj", 2))
		checkRedacted("PUT backend error", err)
	})
}

func TestClientRejectsNegativeArguments(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		env := startTestEnv(t, envOpts{})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if _, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "workload", workloadDoc("keep-me", 1)); err != nil {
			t.Fatalf("Create: %v", err)
		}
		obj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "keep-me"}

		assertBadReq := func(label string, err error) {
			t.Helper()
			var we *wire.Error
			if !errors.As(err, &we) || we.Status != http.StatusBadRequest || !errors.Is(err, wire.ErrBadRequest) {
				t.Fatalf("%s err = %v, want 400 *wire.Error(ErrBadRequest)", label, err)
			}
		}

		assertBadReq("Delete(rev=-1)", env.client.Delete(ctx, "acme", obj, -1))
		// Object must still exist after rejected negative-rev Delete.
		if _, err := env.client.Get(ctx, "acme", obj, 0); err != nil {
			t.Fatalf("object was deleted by negative-rev Delete: %v", err)
		}

		_, err := env.client.Get(ctx, "acme", obj, -1)
		assertBadReq("Get(atRev=-1)", err)

		_, err = env.client.List(ctx, "acme", keyspace.CoreGroup, "workload", resource.ListOptions{AtRev: -1})
		assertBadReq("List(AtRev=-1)", err)

		_, err = env.client.List(ctx, "acme", keyspace.CoreGroup, "workload", resource.ListOptions{Limit: -1})
		assertBadReq("List(Limit=-1)", err)

		_, err = env.client.Watch(ctx, "acme", keyspace.CoreGroup, "workload", wire.WatchOptions{FromRev: -1})
		assertBadReq("Watch(FromRev=-1)", err)
	})
}

func TestConcurrentH2CRequestsOverDelayedMemNet(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		env := startTestEnv(t, envOpts{})
		const delay = 50 * time.Millisecond
		const n = 4
		env.net.SetDelay(delay)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		start := time.Now()
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := env.client.Create(ctx, "acme", keyspace.CoreGroup, "workload", workloadDoc(fmt.Sprintf("web%d", i), 1)); err != nil {
					t.Errorf("Create(web%d): %v", i, err)
				}
			}()
		}
		wg.Wait()
		elapsed := time.Since(start)
		if elapsed < 2*delay {
			t.Fatalf("elapsed = %v, want at least one round trip (%v)", elapsed, 2*delay)
		}
		if elapsed > 4*delay {
			t.Fatalf("elapsed = %v, want about one round trip, not %d serialised ones", elapsed, n)
		}
	})
}
