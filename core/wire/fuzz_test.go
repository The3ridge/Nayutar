package wire_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache"
	"github.com/Kaikei-e/Nayutar/core/wire"
)

func FuzzDecodeError(f *testing.F) {
	f.Add([]byte(`{"$lkal":"0.1","error":{"kind":{"$enum":"badRequest"},"status":400,"message":"bad"}}`))
	f.Add([]byte(`{"$lkal":"0.1","error":{"kind":{"$enum":"pathNotFound"},"status":422,"path":"spec/x","message":"missing"}}`))
	f.Add([]byte(`{"$lkal":"0.1","error":{"kind":{"$enum":"conflict"},"status":409,"code":"K201","path":"spec/replicas","message":"conflict","diagnostics":{"0":{"order":10,"code":"K201","path":"spec/replicas","reason":"owned by other","nextAction":"release"}}}}`))
	f.Add([]byte(`{"$lkal":"0.1"}`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, data []byte) {
		we, err := wire.DecodeError(data)
		if err != nil {
			return
		}
		reEncoded, err := wire.EncodeError(we)
		if err != nil {
			t.Fatalf("EncodeError failed after DecodeError succeeded: %v", err)
		}
		if bytes.ContainsRune(reEncoded, '\n') {
			t.Fatalf("EncodeError emitted newline: %s", reEncoded)
		}
		we2, err := wire.DecodeError(reEncoded)
		if err != nil || !we2.Equal(we) {
			t.Fatalf("DecodeError roundtrip mismatch: %+v vs %+v (err=%v)", we2, we, err)
		}
	})
}

func FuzzDecodeExplainAndResolve(f *testing.F) {
	f.Add([]byte(`{"$lkal":"0.1","revision":2,"ownerRevision":3,"expect":"ksg-doc-v1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","defaultOwner":"User:alice","leaves":{"spec/replicas":{"state":{"$enum":"owned"},"owner":"User:alice","origin":{"$enum":"created"},"expect":"ksg-tok-v1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}}`))
	f.Add([]byte(`{"$lkal":"0.1","path":"spec/replicas","expect":"ksg-tok-v1:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","ownerRevision":1}`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if exp, err := wire.DecodeExplain(data); err == nil {
			re, err := wire.EncodeExplain(exp)
			if err != nil {
				t.Fatalf("EncodeExplain failed after DecodeExplain: %v", err)
			}
			if bytes.ContainsRune(re, '\n') {
				t.Fatalf("EncodeExplain emitted newline: %s", re)
			}
			exp2, err := wire.DecodeExplain(re)
			if err != nil || exp2.Revision != exp.Revision || exp2.OwnerRevision != exp.OwnerRevision || exp2.Expect != exp.Expect || exp2.DefaultOwner != exp.DefaultOwner || len(exp2.Leaves) != len(exp.Leaves) {
				t.Fatalf("DecodeExplain roundtrip mismatch: %+v vs %+v (err=%v)", exp2, exp, err)
			}
		}
		if opts, err := wire.DecodeResolveRequest(data); err == nil {
			re, err := wire.EncodeResolveRequest(opts)
			if err != nil {
				t.Fatalf("EncodeResolveRequest failed after DecodeResolveRequest: %v", err)
			}
			if bytes.ContainsRune(re, '\n') {
				t.Fatalf("EncodeResolveRequest emitted newline: %s", re)
			}
			opts2, err := wire.DecodeResolveRequest(re)
			if err != nil || opts2 != opts {
				t.Fatalf("DecodeResolveRequest roundtrip mismatch: %+v vs %+v (err=%v)", opts2, opts, err)
			}
		}
	})
}

func FuzzDecodeList(f *testing.F) {
	f.Add([]byte(`{"$lkal":"0.1","revision":0,"items":{}}`))
	f.Add([]byte(`{"$lkal":"0.1","revision":5,"continue":"o/core/workload/a","items":{"a":{"meta":{"kind":"workload","name":"a"}}}}`))
	f.Add([]byte(`{"$lkal":"0.1","revision":1,"items":{"wrong":{"meta":{"kind":"workload","name":"a"}}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		res, err := wire.DecodeList(data)
		if err != nil {
			return
		}
		reEncoded, err := wire.EncodeList(res)
		if err != nil {
			t.Fatalf("EncodeList failed after DecodeList succeeded: %v", err)
		}
		if bytes.ContainsRune(reEncoded, '\n') {
			t.Fatalf("EncodeList emitted newline: %s", reEncoded)
		}
		res2, err := wire.DecodeList(reEncoded)
		if err != nil || res2.Revision != res.Revision || res2.Continue != res.Continue || len(res2.Items) != len(res.Items) {
			t.Fatalf("DecodeList roundtrip mismatch: %+v vs %+v (err=%v)", res2, res, err)
		}
	})
}

func FuzzDecodeWatchEvent(f *testing.F) {
	f.Add([]byte(`{"$lkal":"0.1","type":{"$enum":"put"},"revision":1,"object":{"meta":{"kind":"workload","name":"web","revision":1}}}`))
	f.Add([]byte(`{"$lkal":"0.1","type":{"$enum":"delete"},"revision":2,"target":{"kind":"workload","name":"web"}}`))
	f.Add([]byte(`{"$lkal":"0.1","type":{"$enum":"bookmark"},"revision":3,"initialEventsEnd":true}`))
	f.Add([]byte(`{"$lkal":"0.1","type":{"$enum":"error"},"error":{"kind":{"$enum":"gone"},"status":410,"message":"gone"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		ev, err := wire.DecodeWatchEvent(data)
		if err != nil {
			return
		}
		reEncoded, err := wire.EncodeWatchEvent(ev)
		if err != nil {
			t.Fatalf("EncodeWatchEvent failed after DecodeWatchEvent succeeded: %v", err)
		}
		if bytes.ContainsRune(reEncoded, '\n') {
			t.Fatalf("EncodeWatchEvent emitted newline: %s", reEncoded)
		}
		ev2, err := wire.DecodeWatchEvent(reEncoded)
		if err != nil || ev2.Type != ev.Type || ev2.Revision != ev.Revision || ev2.InitialEventsEnd != ev.InitialEventsEnd {
			t.Fatalf("DecodeWatchEvent roundtrip mismatch: %+v vs %+v (err=%v)", ev2, ev, err)
		}
	})
}

func FuzzHandlerRequests(f *testing.F) {
	f.Add(http.MethodGet, "/t/acme/o/core/workload", "", "*/*", "", []byte(nil))
	f.Add(http.MethodPost, "/t/acme/o/core/workload", "", wire.MediaTypeResource, wire.MediaTypeResource, []byte(`{"$lkal":"0.1","meta":{"kind":"workload","name":"w"}}`))
	f.Add(http.MethodPatch, "/t/acme/o/core/workload/w", "", wire.MediaTypeResource, wire.MediaTypePatch, []byte(`{"$lkal":"0.1","patch":{}}`))
	f.Add(http.MethodDelete, "/t/acme/o/core/workload/w", "rev=1", "application/cbor", "", []byte(nil))
	f.Add(http.MethodTrace, "/t/acme/o/core/workload/w/status", "bad=1", "", "", []byte("x"))
	f.Add(http.MethodPut, "/t/acme/o/core/workload/w/apply", "ownerRev=0", wire.MediaTypeResource, wire.MediaTypeResource, []byte(`{"$lkal":"0.1","meta":{"kind":"workload","name":"w"}}`))
	f.Add(http.MethodPost, "/t/acme/o/core/workload/w/prune", "", wire.MediaTypeResource, wire.MediaTypeResource, []byte(`{"$lkal":"0.1","meta":{"kind":"workload","name":"w"}}`))
	f.Add(http.MethodGet, "/t/acme/o/core/workload/w/explain", "atRev=0", wire.MediaTypeResource, "", []byte(nil))
	f.Add(http.MethodPost, "/t/acme/o/core/workload/w/adopt", "", wire.MediaTypeResource, wire.MediaTypeResource, []byte(`{"$lkal":"0.1","path":"spec/replicas","expect":"x","ownerRevision":1}`))
	f.Add(http.MethodPost, "/t/acme/o/core/workload/w/release", "", wire.MediaTypeResource, wire.MediaTypeResource, []byte(`{"$lkal":"0.1","path":"spec/replicas","expect":"x","ownerRevision":1}`))

	root := memstore.New()
	reg := tenant.NewRegistry(root)
	if err := reg.Register(context.Background(), "acme"); err != nil {
		f.Fatalf("register acme: %v", err)
	}
	caches, err := watchcache.NewManager(root, watchcache.Config{})
	if err != nil {
		f.Fatalf("new manager: %v", err)
	}
	f.Cleanup(caches.Close)

	h, err := wire.New(wire.Config{
		Registry:     reg,
		Caches:       caches,
		Schemas:      testSchemas(&testing.T{}),
		MaxBodyBytes: 4096,
	})
	if err != nil {
		f.Fatalf("wire.New: %v", err)
	}

	f.Fuzz(func(t *testing.T, method, path, rawQuery, accept, contentType string, body []byte) {
		// Skip watch=true in httptest fuzzing since a valid watch streams indefinitely.
		if strings.Contains(rawQuery, "watch=true") {
			return
		}
		if method == "" || strings.ContainsAny(method, " \t\r\n") {
			method = http.MethodGet
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		if strings.ContainsAny(path, " \r\n#?") {
			return
		}
		target := path
		if rawQuery != "" && !strings.ContainsAny(rawQuery, " \r\n#") {
			target += "?" + rawQuery
		}
		var bodyReader io.Reader
		if len(body) > 0 {
			bodyReader = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, "http://127.0.0.1"+target, bodyReader)
		if err != nil {
			return
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		resp := rec.Result()
		defer func() { _ = resp.Body.Close() }()
		assertSecurityHeaders(t, resp.Header)

		if resp.StatusCode >= 400 {
			raw, _ := io.ReadAll(resp.Body)
			we, err := wire.DecodeError(raw)
			if err != nil {
				t.Fatalf("status %d emitted invalid wire error (%s): %v", resp.StatusCode, raw, err)
			}
			if we.Status != resp.StatusCode {
				t.Fatalf("error status %d != HTTP status %d", we.Status, resp.StatusCode)
			}
			if resp.StatusCode == http.StatusInternalServerError && (we.Message != "internal server error" || we.Path != "") {
				t.Fatalf("500 response leaked details: %+v", we)
			}
		}
	})
}
