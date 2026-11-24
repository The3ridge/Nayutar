package etcdtest

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/Nayutar/core/sim"
)

type proxyTestEnv struct {
	net      *sim.MemNet
	proxy    *peerProxy
	urls     [Members]string
	backends [Members]*http.Server
	wg       sync.WaitGroup
}

func (e *proxyTestEnv) close() {
	e.proxy.close()
	for _, srv := range e.backends {
		if srv != nil {
			_ = srv.Close()
		}
	}
	_ = e.net.Close()
	e.wg.Wait()
}

func startTestProxy(t *testing.T, handlers [Members]http.Handler) *proxyTestEnv {
	t.Helper()

	mn := sim.NewMemNet()
	env := &proxyTestEnv{net: mn}
	var listenURLs, targetURLs [Members]string

	for i := range Members {
		listenURLs[i] = fmt.Sprintf("http://127.0.0.1:%d", 20000+i)

		ln, err := mn.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			_ = mn.Close()
			t.Fatal(err)
		}
		h := handlers[i]
		if h == nil {
			h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
		}
		srv := &http.Server{
			Handler:  h,
			ErrorLog: log.New(io.Discard, "", 0),
		}
		env.backends[i] = srv
		targetURLs[i] = "http://" + ln.Addr().String()
		env.wg.Add(1)
		go func() {
			defer env.wg.Done()
			_ = srv.Serve(ln)
		}()
	}

	p, err := newPeerProxyWithNet(listenURLs, targetURLs, mn)
	if err != nil {
		for _, srv := range env.backends {
			if srv != nil {
				_ = srv.Close()
			}
		}
		_ = mn.Close()
		env.wg.Wait()
		t.Fatal(err)
	}
	p.setMemberIDs([Members]uint64{1, 2, 3})
	env.proxy = p
	env.urls = listenURLs
	return env
}

func newTestClient(mn *sim.MemNet, timeout time.Duration) (*http.Client, func()) {
	tr := &http.Transport{
		DialContext: mn.DialContext,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
	}, tr.CloseIdleConnections
}

func TestPeerProxyForwardsUncutPOST(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var handlers [Members]http.Handler
		handlers[1] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if r.Method != http.MethodPost || r.URL.Path != "/raft" || r.Header.Get("X-Server-From") != "1" || string(body) != "hello" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = io.WriteString(w, "ok")
		})
		env := startTestProxy(t, handlers)
		defer env.close()

		client, cleanup := newTestClient(env.net, 2*time.Second)
		defer cleanup()

		req, err := http.NewRequest(http.MethodPost, env.urls[1]+"/raft", strings.NewReader("hello"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Server-From", "1")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST /raft failed: %v", err)
		}
		got, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(got) != "ok" {
			t.Fatalf("got status %d body %q, want 200 %q", resp.StatusCode, got, "ok")
		}
	})
}

func TestPeerProxyHoldsCutPOSTAndClosesOnHeal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var backendCalled atomic.Bool
		var handlers [Members]http.Handler
		handlers[1] = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			backendCalled.Store(true)
			w.WriteHeader(http.StatusOK)
		})
		env := startTestProxy(t, handlers)
		defer env.close()

		env.proxy.isolateMember(0)

		client, cleanup := newTestClient(env.net, 2*time.Second)
		defer cleanup()

		type result struct {
			resp *http.Response
			err  error
		}
		done := make(chan result, 1)
		go func() {
			req, err := http.NewRequest(http.MethodPost, env.urls[1]+"/raft", strings.NewReader("msg"))
			if err != nil {
				done <- result{err: err}
				return
			}
			req.Header.Set("X-Server-From", "1")
			resp, err := client.Do(req)
			done <- result{resp: resp, err: err}
		}()

		select {
		case res := <-done:
			if res.resp != nil {
				_ = res.resp.Body.Close()
			}
			t.Fatalf("cut POST finished before heal: resp=%v err=%v", res.resp, res.err)
		case <-time.After(100 * time.Millisecond):
		}
		if backendCalled.Load() {
			t.Fatal("backend was called while link was cut")
		}

		env.proxy.healPartition()

		select {
		case res := <-done:
			if res.resp != nil {
				_ = res.resp.Body.Close()
				t.Fatalf("expected connection close error after heal, got status %d", res.resp.StatusCode)
			}
			if res.err == nil {
				t.Fatal("expected error after heal")
			}
		case <-time.After(time.Second):
			t.Fatal("held POST did not abort after heal")
		}
		if backendCalled.Load() {
			t.Fatal("backend was called for held POST")
		}
	})
}

func TestPeerProxyStopsInFlightStreamWhileCut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var handlers [Members]http.Handler
		handlers[1] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			flusher.Flush()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
					if _, err := io.WriteString(w, "tick\n"); err != nil {
						return
					}
					flusher.Flush()
				}
			}
		})
		env := startTestProxy(t, handlers)
		defer env.close()

		client, cleanup := newTestClient(env.net, 0)
		defer cleanup()

		req, err := http.NewRequest(http.MethodGet, env.urls[1]+"/raft/stream/message/1", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Server-From", "1")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET stream failed: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		lines := make(chan string, 64)
		readErr := make(chan error, 1)
		readerDone := make(chan struct{})
		go func() {
			defer close(readerDone)
			br := bufio.NewReader(resp.Body)
			for {
				line, err := br.ReadString('\n')
				if line != "" {
					lines <- line
				}
				if err != nil {
					readErr <- err
					return
				}
			}
		}()

		for range 2 {
			select {
			case <-lines:
			case err := <-readErr:
				t.Fatalf("stream ended before cut: %v", err)
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for pre-cut stream lines")
			}
		}

		env.proxy.isolateMember(0)
		synctest.Wait()
		for len(lines) > 0 {
			<-lines
		}

		select {
		case line := <-lines:
			t.Fatalf("received stream line %q while cut", line)
		case err := <-readErr:
			t.Fatalf("stream closed while cut: %v", err)
		case <-time.After(200 * time.Millisecond):
		}

		env.proxy.healPartition()

		select {
		case err := <-readErr:
			if err == nil {
				t.Fatal("expected stream read error on heal")
			}
		case <-time.After(time.Second):
			t.Fatal("stream read did not abort on heal")
		}
		<-readerDone
	})
}

func TestPeerProxyKeepsBackendSilentWhenClientClosesDuringCut(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		backendDone := make(chan struct{})
		var handlers [Members]http.Handler
		handlers[1] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "hello\n")
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			close(backendDone)
		})
		env := startTestProxy(t, handlers)
		defer env.close()

		client, cleanup := newTestClient(env.net, 0)
		defer cleanup()

		req, err := http.NewRequest(http.MethodGet, env.urls[1]+"/raft/stream/message/1", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Server-From", "1")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET stream failed: %v", err)
		}

		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		if err != nil || line != "hello\n" {
			_ = resp.Body.Close()
			t.Fatalf("read initial line: line=%q err=%v", line, err)
		}
		<-started

		env.proxy.isolateMember(0)

		_ = resp.Body.Close()
		cleanup()

		select {
		case <-backendDone:
			t.Fatal("backend request context was cancelled while link was cut")
		case <-time.After(200 * time.Millisecond):
		}

		env.proxy.healPartition()

		select {
		case <-backendDone:
		case <-time.After(time.Second):
			t.Fatal("backend request context was not cancelled after heal")
		}
	})
}

func TestPeerProxyFailClosedOnRaftPaths(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var raftCalled atomic.Bool
		var handlers [Members]http.Handler
		handlers[0] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/version" {
				_, _ = io.WriteString(w, "3.6")
				return
			}
			raftCalled.Store(true)
			w.WriteHeader(http.StatusOK)
		})
		env := startTestProxy(t, handlers)
		defer env.close()

		client, cleanup := newTestClient(env.net, 2*time.Second)
		defer cleanup()

		resp, err := client.Get(env.urls[0] + "/raft/stream/message/x")
		if resp != nil {
			_ = resp.Body.Close()
			t.Fatalf("expected aborted connection on raft path without X-Server-From, got status %d", resp.StatusCode)
		}
		if err == nil {
			t.Fatal("expected error on raft path without X-Server-From")
		}
		if raftCalled.Load() {
			t.Fatal("raft backend was called without X-Server-From")
		}

		resp, err = client.Get(env.urls[0] + "/version")
		if err != nil {
			t.Fatalf("GET /version without X-Server-From failed: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || string(body) != "3.6" {
			t.Fatalf("GET /version got status %d body %q, want 200 %q", resp.StatusCode, body, "3.6")
		}
	})
}

func TestPeerProxyAbortsWhenBackendDown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var handlers [Members]http.Handler
		env := startTestProxy(t, handlers)
		defer env.close()
		_ = env.backends[2].Close()

		client, cleanup := newTestClient(env.net, 2*time.Second)
		defer cleanup()

		resp, err := client.Get(env.urls[2] + "/version")
		if resp != nil {
			_ = resp.Body.Close()
			t.Fatalf("expected connection error when backend is down, got status %d", resp.StatusCode)
		}
		if err == nil {
			t.Fatal("expected error when backend is down")
		}
	})
}
