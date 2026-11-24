package wire_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/ownership"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/wire"
)

func ownershipAuthAndResolver() (func(*http.Request) (refs.Principal, error), resource.ScopeResolver) {
	authFn := func(r *http.Request) (refs.Principal, error) {
		switch r.Header.Get("X-Test-Actor") {
		case "alice":
			return refs.Principal{Type: "User", ID: "alice"}, nil
		case "bob":
			return refs.Principal{Type: "User", ID: "bob"}, nil
		case "hpa":
			return refs.Principal{Type: "ServiceAccount", ID: "hpa"}, nil
		case "readonly":
			return refs.Principal{Type: "User", ID: "readonly"}, nil
		default:
			return refs.Principal{}, errors.New("missing actor")
		}
	}
	resolver := func(p refs.Principal, _ keyspace.Object) (ownership.ActorID, ownership.Scope, error) {
		switch p {
		case refs.Principal{Type: "User", ID: "alice"}:
			return "User:alice", ownership.RootScope(ownership.PermAll), nil
		case refs.Principal{Type: "User", ID: "bob"}:
			return "User:bob", ownership.RootScope(ownership.PermAll), nil
		case refs.Principal{Type: "ServiceAccount", ID: "hpa"}:
			r, err := ownership.NewExactRule(ownership.Path{"spec", "replicas"}, ownership.PermSet|ownership.PermDelete|ownership.PermOwn)
			if err != nil {
				return "", ownership.Scope{}, err
			}
			return "ServiceAccount:hpa", ownership.MustNewScope(r), nil
		case refs.Principal{Type: "User", ID: "readonly"}:
			return "User:readonly", ownership.Scope{}, nil
		default:
			return "", ownership.Scope{}, errors.New("denied")
		}
	}
	return authFn, resolver
}

func clientAs(env *testEnv, actor string) *wire.Client {
	c := wire.NewClient(env.baseURL, env.client.HTTPClient())
	c.BeforeRequest = func(r *http.Request) {
		r.Header.Set("X-Test-Actor", actor)
	}
	return c
}

func TestWireOwnershipSubresourcesHTTP2(t *testing.T) {
	for _, tc := range []struct {
		name   string
		useTLS bool
	}{
		{name: "h2c", useTLS: false},
		{name: "tls_h2", useTLS: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				authFn, resolver := ownershipAuthAndResolver()
				env := startTestEnv(t, envOpts{
					useTLS:        tc.useTLS,
					authFn:        authFn,
					scopeResolver: resolver,
				})
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()

				aliceCli := clientAs(env, "alice")
				bobCli := clientAs(env, "bob")
				hpaCli := clientAs(env, "hpa")
				roCli := clientAs(env, "readonly")

				obj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "payments"}

				// 1. PUT .../apply creates absent object with 201 Created and Nayutar-Owner-Revision.
				aliceDecl1 := rec(
					fld("meta", rec(
						fld("kind", lkal.String("workload")),
						fld("name", lkal.String("payments")),
						fld("labels", rec(fld("app.example.com/tier", lkal.String("core")))),
					)),
					fld("spec", rec(
						fld("payload", lkal.String("v1")),
						fld("steps", rec(
							fld("init", rec(fld("order", lkal.Int(10)), fld("cmd", lkal.String("/init")))),
						)),
					)),
				)
				res1, err := aliceCli.Apply(ctx, "acme", obj, aliceDecl1, resource.ApplyOptions{})
				if err != nil {
					t.Fatalf("Apply(create): %v", err)
				}
				if env.proto.Load() != 2 {
					t.Fatalf("expected HTTP/2, got HTTP/%d", env.proto.Load())
				}
				if !res1.Created || res1.OwnerRevision < 1 {
					t.Fatalf("Apply(create) = (created=%v ownerRev=%d)", res1.Created, res1.OwnerRevision)
				}
				spec1, _ := getRecord(res1.Object, "spec")
				if got := getInt(spec1, "replicas"); got != 1 {
					t.Fatalf("default replicas = %d, want 1", got)
				}

				// 2. GET .../explain returns document & per-leaf tokens and states.
				exp1, err := aliceCli.Explain(ctx, "acme", obj, 0)
				if err != nil {
					t.Fatalf("Explain(1): %v", err)
				}
				if exp1.DefaultOwner != "User:alice" || exp1.OwnerRevision != int64(res1.OwnerRevision) {
					t.Fatalf("Explain(1) = %+v", exp1)
				}
				findLeaf := func(exp ownership.Explanation, p string) ownership.LeafExplanation {
					for _, lf := range exp.Leaves {
						if lf.Path.String() == p {
							return lf
						}
					}
					t.Fatalf("leaf %s missing in explain %+v", p, exp.Leaves)
					return ownership.LeafExplanation{}
				}
				if lf := findLeaf(exp1, "spec/replicas"); lf.State != ownership.StateDefaulted {
					t.Fatalf("spec/replicas = %+v, want defaulted", lf)
				}

				// 3. HPA claims defaulted spec/replicas with expect + ownerRev -> 200 OK (Created == false).
				hpaDecl := rec(
					fld("meta", rec(fld("kind", lkal.String("workload")), fld("name", lkal.String("payments")))),
					fld("spec", rec(fld("replicas", lkal.Int(4)))),
				)
				resHPA, err := hpaCli.Apply(ctx, "acme", obj, hpaDecl, resource.ApplyOptions{
					Expect:           exp1.Expect,
					ExpectedOwnerRev: res1.OwnerRevision,
				})
				if err != nil {
					t.Fatalf("Apply(hpa): %v", err)
				}
				if resHPA.Created || resHPA.OwnerRevision <= res1.OwnerRevision {
					t.Fatalf("Apply(hpa) = (created=%v ownerRev=%d)", resHPA.Created, resHPA.OwnerRevision)
				}

				// 4. Error status & K-code mappings:
				// K102 -> 400 Bad Request (nested empty record).
				nestedEmpty := rec(
					fld("meta", rec(fld("kind", lkal.String("workload")), fld("name", lkal.String("payments")))),
					fld("spec", rec(fld("nested", rec()))),
				)
				_, err = aliceCli.Apply(ctx, "acme", obj, nestedEmpty, resource.ApplyOptions{})
				var we *wire.Error
				if !errors.As(err, &we) || we.Status != http.StatusBadRequest || we.Code != ownership.CodeEmptyRecordNotSupported {
					t.Fatalf("nested {} err = %v, want 400 K102", err)
				}

				// K401 -> 403 Forbidden (empty scope).
				_, err = roCli.Apply(ctx, "acme", obj, rec(
					fld("meta", rec(fld("kind", lkal.String("workload")), fld("name", lkal.String("payments")))),
					fld("spec", rec(fld("nested", rec(fld("note", lkal.String("x")))))),
				), resource.ApplyOptions{})
				we = nil
				if !errors.As(err, &we) || we.Status != http.StatusForbidden || we.Code != ownership.CodePolicyDenied {
					t.Fatalf("readonly apply err = %v, want 403 K401", err)
				}

				// K201 -> 409 Conflict with diagnostics (Bob conflicts on Alice's spec/payload).
				_, err = bobCli.Apply(ctx, "acme", obj, rec(
					fld("meta", rec(fld("kind", lkal.String("workload")), fld("name", lkal.String("payments")))),
					fld("spec", rec(fld("payload", lkal.String("v2")))),
				), resource.ApplyOptions{})
				we = nil
				if !errors.As(err, &we) || we.Status != http.StatusConflict || we.Code != ownership.CodeOwnershipConflict || len(we.Diagnostics) == 0 {
					t.Fatalf("bob foreign conflict err = %v, want 409 K201 with diagnostics", err)
				}

				// K203 -> 409 Conflict (stale doc expect token).
				_, err = aliceCli.Apply(ctx, "acme", obj, aliceDecl1, resource.ApplyOptions{Expect: exp1.Expect})
				we = nil
				if !errors.As(err, &we) || we.Status != http.StatusConflict || we.Code != ownership.CodeExternalDrift {
					t.Fatalf("stale expect err = %v, want 409 K203", err)
				}

				// 5. Release & Adopt are metadata-only: verify ZERO watch events on o/!
				exp2, err := aliceCli.Explain(ctx, "acme", obj, 0)
				if err != nil {
					t.Fatalf("Explain(2): %v", err)
				}
				payloadLeaf := findLeaf(exp2, "spec/payload")

				wCtx, cancelW := context.WithCancel(ctx)
				defer cancelW()
				ws, err := aliceCli.Watch(wCtx, "acme", keyspace.CoreGroup, "workload", wire.WatchOptions{
					SendInitialEvents: true,
				})
				if err != nil {
					t.Fatalf("Watch: %v", err)
				}
				defer func() { _ = ws.Close() }()
				for {
					ev, ok := ws.Next()
					if !ok {
						t.Fatalf("watch ended during initial state: %v", ws.Err())
					}
					if ev.Type == wire.EventBookmark && ev.InitialEventsEnd {
						break
					}
				}

				// Alice releases spec/payload -> 200 OK explain.
				afterRel, err := aliceCli.Release(ctx, "acme", obj, resource.ResolveOptions{
					Path:             "spec/payload",
					Expect:           payloadLeaf.Expect,
					ExpectedOwnerRev: store.Rev(exp2.OwnerRevision),
				})
				if err != nil {
					t.Fatalf("Release: %v", err)
				}
				if afterRel.Revision != exp2.Revision {
					t.Fatalf("Release changed o/ revision %d -> %d", exp2.Revision, afterRel.Revision)
				}
				if lf := findLeaf(afterRel, "spec/payload"); lf.State != ownership.StateUnowned {
					t.Fatalf("after release spec/payload = %+v, want unowned", lf)
				}

				// Bob applying different value to unowned spec/payload -> 409 K206!
				_, err = bobCli.Apply(ctx, "acme", obj, rec(
					fld("meta", rec(fld("kind", lkal.String("workload")), fld("name", lkal.String("payments")))),
					fld("spec", rec(fld("payload", lkal.String("v2")))),
				), resource.ApplyOptions{})
				we = nil
				if !errors.As(err, &we) || we.Status != http.StatusConflict || we.Code != ownership.CodeUnownedValueConflict {
					t.Fatalf("unowned mismatch err = %v, want 409 K206", err)
				}

				// Bob adopts spec/payload -> 200 OK explain, still zero o/ change!
				afterAdopt, err := bobCli.Adopt(ctx, "acme", obj, resource.ResolveOptions{
					Path:             "spec/payload",
					Expect:           findLeaf(afterRel, "spec/payload").Expect,
					ExpectedOwnerRev: store.Rev(afterRel.OwnerRevision),
				})
				if err != nil {
					t.Fatalf("Adopt: %v", err)
				}
				if afterAdopt.Revision != exp2.Revision {
					t.Fatalf("Adopt changed o/ revision %d -> %d", exp2.Revision, afterAdopt.Revision)
				}

				// 6. Alice prunes retired ordered-set element spec/steps/init and label -> 200 OK.
				alicePruneDecl := rec(
					fld("meta", rec(fld("kind", lkal.String("workload")), fld("name", lkal.String("payments")))),
					fld("spec", rec()),
				)
				pruned, err := aliceCli.Prune(ctx, "acme", obj, alicePruneDecl, resource.ApplyOptions{})
				if err != nil {
					t.Fatalf("Prune: %v", err)
				}
				specPruned, _ := getRecord(pruned.Object, "spec")
				if _, exists := getField(specPruned, "steps"); exists {
					t.Fatal("spec/steps was not pruned")
				}

				// The first EventPut on the watch stream MUST be the Prune (NOT Release or Adopt!).
				for {
					ev, ok := ws.Next()
					if !ok {
						t.Fatalf("watch ended before Prune event: %v", ws.Err())
					}
					if ev.Type == wire.EventPut {
						pMeta, _ := getRecord(*ev.Object, "meta")
						if gotRev := getInt(pMeta, "revision"); gotRev <= exp2.Revision {
							t.Fatalf("unexpected early EventPut rev=%d (exp2.Revision=%d)", gotRev, exp2.Revision)
						}
						pSpec, _ := getRecord(*ev.Object, "spec")
						if _, exists := getField(pSpec, "steps"); exists {
							t.Fatal("received spurious EventPut before Prune (Release/Adopt leaked o/ write)")
						}
						break
					}
				}
				cancelW()

				// 7. Historical snapshot Explain (?atRev=) reflects pre-Prune ownership.
				histExp, err := aliceCli.Explain(ctx, "acme", obj, store.Rev(exp1.Revision))
				if err != nil {
					t.Fatalf("Explain(atRev): %v", err)
				}
				if lf := findLeaf(histExp, "spec/replicas"); lf.State != ownership.StateDefaulted {
					t.Fatalf("historical explain spec/replicas = %+v, want defaulted", lf)
				}
			})
		})
	}
}

func TestWireOwnershipSecurityAndHygiene(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		authFn, resolver := ownershipAuthAndResolver()
		env := startTestEnv(t, envOpts{
			authFn:        authFn,
			scopeResolver: resolver,
			maxBodyBytes:  256,
		})
		base := env.baseURL + "/t/acme/o/core/workload/web"

		doRaw := func(method, target, contentType string, body []byte) *http.Response {
			t.Helper()
			var rdr io.Reader
			if body != nil {
				rdr = bytes.NewReader(body)
			}
			req, err := http.NewRequest(method, target, rdr)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Accept", wire.MediaTypeResource)
			req.Header.Set("X-Test-Actor", "alice")
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}
			resp, err := env.rawHC.Do(req)
			if err != nil {
				t.Fatalf("Do(%s %s): %v", method, target, err)
			}
			assertSecurityHeaders(t, resp.Header)
			return resp
		}

		// 405 Method Not Allowed with exact Allow headers on all 5 subresources.
		for _, tc := range []struct {
			sub    string
			method string
			allow  string
		}{
			{"/apply", http.MethodPost, "PUT"},
			{"/prune", http.MethodPut, "POST"},
			{"/explain", http.MethodPost, "GET"},
			{"/adopt", http.MethodGet, "POST"},
			{"/release", http.MethodPut, "POST"},
		} {
			resp := doRaw(tc.method, base+tc.sub, "", nil)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s status = %d, want 405", tc.method, tc.sub, resp.StatusCode)
			}
			if got := resp.Header.Get("Allow"); got != tc.allow {
				t.Fatalf("%s %s Allow = %q, want %q", tc.method, tc.sub, got, tc.allow)
			}
			if got := resp.Header.Get("Accept-Patch"); got != "" {
				t.Fatalf("%s %s unexpectedly set Accept-Patch = %q", tc.method, tc.sub, got)
			}
		}

		// Unknown query params, ownerRev=0, empty expect -> 400.
		for _, tc := range []struct {
			method string
			url    string
		}{
			{http.MethodPut, base + "/apply?unknown=1"},
			{http.MethodPut, base + "/apply?ownerRev=0"},
			{http.MethodPut, base + "/apply?expect="},
			{http.MethodPost, base + "/prune?bad=1"},
			{http.MethodGet, base + "/explain?unknown=1"},
			{http.MethodPost, base + "/adopt?extra=1"},
			{http.MethodPost, base + "/release?extra=1"},
		} {
			resp := doRaw(tc.method, tc.url, wire.MediaTypeResource, nil)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("%s %s status = %d, want 400", tc.method, tc.url, resp.StatusCode)
			}
		}

		// 413 Payload Too Large on /apply and /adopt.
		bigBody := []byte(`{"$lkal":"0.1","meta":{"kind":"workload","name":"web"},"spec":{"payload":"` + strings.Repeat("a", 300) + `"}}`)
		resp := doRaw(http.MethodPut, base+"/apply", wire.MediaTypeResource, bigBody)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversize /apply status = %d, want 413", resp.StatusCode)
		}

		// 415 Unsupported Media Type on /adopt.
		resp = doRaw(http.MethodPost, base+"/adopt", "application/json", []byte(`{"$lkal":"0.1"}`))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("bad content-type /adopt status = %d, want 415", resp.StatusCode)
		}

		// 400 Bad Request on malformed resolve body.
		resp = doRaw(http.MethodPost, base+"/adopt", wire.MediaTypeResource, []byte(`{"$lkal":"0.1","path":""}`))
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("malformed /adopt status = %d, want 400", resp.StatusCode)
		}
		we, err := wire.DecodeError(raw)
		if err != nil || we.Code != ownership.CodeInvalidInput {
			t.Fatalf("malformed /adopt error = (%+v, %v), want K101", we, err)
		}
	})
}

type deadlineTxnStore struct {
	store.Store
	failWrites bool
}

func (d *deadlineTxnStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	if d.failWrites {
		return false, 0, context.DeadlineExceeded
	}
	return d.Store.Txn(ctx, cmps, ops)
}

func TestWireOwnershipSanitizesK401K502AndRejectsCorruptOwn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// 1. Raw ScopeResolver callback error must never leak in 403 K401 response body or diagnostics.
		rawSecretErr := "secret-db-dsn=postgres://internal-host/prod"
		envLeak := startTestEnv(t, envOpts{
			authFn: func(_ *http.Request) (refs.Principal, error) {
				return refs.Principal{Type: "User", ID: "alice"}, nil
			},
			scopeResolver: func(_ refs.Principal, _ keyspace.Object) (ownership.ActorID, ownership.Scope, error) {
				return "", ownership.Scope{}, errors.New(rawSecretErr)
			},
		})
		obj := keyspace.Object{Group: keyspace.CoreGroup, Kind: "workload", Name: "web"}
		body, err := resource.Encode(workloadDoc("web", 2))
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, envLeak.baseURL+"/t/acme/o/core/workload/web/apply", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", wire.MediaTypeResource)
		req.Header.Set("Content-Type", wire.MediaTypeResource)
		resp, err := envLeak.rawHC.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw403, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("ScopeResolver error status = %d, want 403 (%s)", resp.StatusCode, raw403)
		}
		if bytes.Contains(raw403, []byte("secret-db-dsn")) || bytes.Contains(raw403, []byte("postgres")) {
			t.Fatalf("403 K401 leaked ScopeResolver error: %s", raw403)
		}
		we403, err := wire.DecodeError(raw403)
		if err != nil || we403.Code != ownership.CodePolicyDenied || we403.Message != "ownership scope denied" {
			t.Fatalf("403 K401 decoded = (%+v, %v), want K401 'ownership scope denied'", we403, err)
		}

		// 2. Indeterminate Txn DeadlineExceeded returns 503 K502 across Create/Update/Patch/Apply/Adopt with zero internal key leakage.
		ds := &deadlineTxnStore{Store: memstore.New()}
		authFn, resolver := ownershipAuthAndResolver()
		env503 := startTestEnv(t, envOpts{
			root:          ds,
			authFn:        authFn,
			scopeResolver: resolver,
		})
		aliceCli := clientAs(env503, "alice")
		created, err := aliceCli.Apply(ctx, "acme", obj, workloadDoc("web", 2), resource.ApplyOptions{})
		if err != nil {
			t.Fatalf("seed Apply: %v", err)
		}
		exp, err := aliceCli.Explain(ctx, "acme", obj, 0)
		if err != nil {
			t.Fatalf("seed Explain: %v", err)
		}

		ds.failWrites = true
		checkK502 := func(name string, method, target, ct string, payload []byte) {
			t.Helper()
			r, reqErr := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
			if reqErr != nil {
				t.Fatalf("%s NewRequest: %v", name, reqErr)
			}
			r.Header.Set("Accept", wire.MediaTypeResource)
			r.Header.Set("Content-Type", ct)
			r.Header.Set("X-Test-Actor", "alice")
			res, doErr := env503.rawHC.Do(r)
			if doErr != nil {
				t.Fatalf("%s Do: %v", name, doErr)
			}
			raw503, _ := io.ReadAll(res.Body)
			_ = res.Body.Close()
			if res.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("%s status = %d, want 503 (%s)", name, res.StatusCode, raw503)
			}
			for _, forbidden := range []string{"o/core/", "own/core/", "/t/acme/", "workload/web"} {
				if bytes.Contains(raw503, []byte(forbidden)) {
					t.Fatalf("%s 503 K502 leaked internal key %q: %s", name, forbidden, raw503)
				}
			}
			we503, decErr := wire.DecodeError(raw503)
			if decErr != nil || we503.Code != ownership.CodeIndeterminateOutcome || we503.Path != "" || we503.Message != "indeterminate transaction outcome" {
				t.Fatalf("%s 503 K502 decoded = (%+v, %v)", name, we503, decErr)
			}
		}

		newDocBytes, _ := resource.Encode(workloadDoc("web2", 2))
		updDocBytes, _ := resource.Encode(workloadDoc("web", 5))
		patchBytes, _ := patch.Encode([]patch.Op{{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(6)}})
		adoptBytes, _ := wire.EncodeResolveRequest(resource.ResolveOptions{
			Path:             "spec/replicas",
			Expect:           exp.Leaves[0].Expect,
			ExpectedOwnerRev: created.OwnerRevision,
		})
		checkK502("Create", http.MethodPost, env503.baseURL+"/t/acme/o/core/workload", wire.MediaTypeResource, newDocBytes)
		checkK502("Update", http.MethodPut, env503.baseURL+"/t/acme/o/core/workload/web", wire.MediaTypeResource, updDocBytes)
		checkK502("Patch", http.MethodPatch, env503.baseURL+"/t/acme/o/core/workload/web", wire.MediaTypePatch, patchBytes)
		checkK502("Apply", http.MethodPut, env503.baseURL+"/t/acme/o/core/workload/web/apply", wire.MediaTypeResource, updDocBytes)
		checkK502("Adopt", http.MethodPost, env503.baseURL+"/t/acme/o/core/workload/web/adopt", wire.MediaTypeResource, adoptBytes)

		// 3. Corrupted /own/ trie (w=true with children c) fails closed with sanitized HTTP 500.
		ds.failWrites = false
		corruptOwn := []byte(`{"$lkal":"0.1","actors":["User:alice","User:bob"],"trie":{"spec":{"s":{"$enum":"owned"},"a":1,"o":{"$enum":"created"},"w":true,"c":{"replicas":{"s":{"$enum":"owned"},"a":0,"o":{"$enum":"created"}}}}}}`)
		if _, _, err := ds.Txn(ctx, nil, []store.Op{{
			Type:  store.OpPut,
			Key:   "/t/acme/own/core/workload/web",
			Value: corruptOwn,
		}}); err != nil {
			t.Fatalf("inject corrupt /own/: %v", err)
		}
		_, err = aliceCli.Explain(ctx, "acme", obj, 0)
		var we500 *wire.Error
		if !errors.As(err, &we500) || we500.Status != http.StatusInternalServerError || we500.Message != "internal server error" || we500.Path != "" || we500.Code != "" || len(we500.Diagnostics) != 0 {
			t.Fatalf("Explain(corrupt own/) = %+v, want sanitized 500", err)
		}
	})
}
