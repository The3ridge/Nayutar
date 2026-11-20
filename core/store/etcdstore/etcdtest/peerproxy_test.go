package etcdtest

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func startTestProxy(t *testing.T, handlers [Members]http.Handler) (*peerProxy, [Members]string, [Members]*httptest.Server) {
	t.Helper()

	var listenURLs, targetURLs [Members]string
	var backends [Members]*httptest.Server
	for i := range Members {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		listenURLs[i] = "http://" + addr

		h := handlers[i]
		if h == nil {
			h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
		}
		srv := httptest.NewServer(h)
		backends[i] = srv
		targetURLs[i] = srv.URL
	}
	t.Cleanup(func() {
		for _, srv := range backends {
			srv.Close()
		}
	})

	p, err := newPeerProxy(listenURLs, targetURLs)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	p.setMemberIDs([Members]uint64{1, 2, 3})
	return p, listenURLs, backends
}

func newTestClient(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{
		Transport: tr,
		Timeout:   2 * time.Second,
	}
}

func TestPeerProxyForwardsUncutPOST(t *testing.T) {
	var handlers [Members]http.Handler
	handlers[1] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/raft" || r.Header.Get("X-Server-From") != "1" || string(body) != "hello" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	_, urls, _ := startTestProxy(t, handlers)

	client := newTestClient(t)
	req, err := http.NewRequest(http.MethodPost, urls[1]+"/raft", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Server-From", "1")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /raft failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	got, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(got) != "ok" {
		t.Fatalf("got status %d body %q, want 200 %q", resp.StatusCode, got, "ok")
	}
}

func TestPeerProxyHoldsCutPOSTAndClosesOnHeal(t *testing.T) {
	var backendCalled atomic.Bool
	var handlers [Members]http.Handler
	handlers[1] = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalled.Store(true)
		w.WriteHeader(http.StatusOK)
	})
	p, urls, _ := startTestProxy(t, handlers)

	p.isolateMember(0)

	client := newTestClient(t)
	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, urls[1]+"/raft", strings.NewReader("msg"))
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

	p.healPartition()

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
}

func TestPeerProxyStopsInFlightStreamWhileCut(t *testing.T) {
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
	p, urls, _ := startTestProxy(t, handlers)

	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr}

	req, err := http.NewRequest(http.MethodGet, urls[1]+"/raft/stream/message/1", nil)
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
	go func() {
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

	p.isolateMember(0)

	time.Sleep(20 * time.Millisecond)
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

	p.healPartition()

	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("expected stream read error on heal")
		}
	case <-time.After(time.Second):
		t.Fatal("stream read did not abort on heal")
	}
}

func TestPeerProxyKeepsBackendSilentWhenClientClosesDuringCut(t *testing.T) {
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
	p, urls, _ := startTestProxy(t, handlers)

	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	client := &http.Client{Transport: tr}

	req, err := http.NewRequest(http.MethodGet, urls[1]+"/raft/stream/message/1", nil)
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

	p.isolateMember(0)

	_ = resp.Body.Close()
	tr.CloseIdleConnections()

	select {
	case <-backendDone:
		t.Fatal("backend request context was cancelled while link was cut")
	case <-time.After(200 * time.Millisecond):
	}

	p.healPartition()

	select {
	case <-backendDone:
	case <-time.After(time.Second):
		t.Fatal("backend request context was not cancelled after heal")
	}
}

func TestPeerProxyFailClosedOnRaftPaths(t *testing.T) {
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
	_, urls, _ := startTestProxy(t, handlers)

	client := newTestClient(t)

	resp, err := client.Get(urls[0] + "/raft/stream/message/x")
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

	resp, err = client.Get(urls[0] + "/version")
	if err != nil {
		t.Fatalf("GET /version without X-Server-From failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "3.6" {
		t.Fatalf("GET /version got status %d body %q, want 200 %q", resp.StatusCode, body, "3.6")
	}
}

func TestPeerProxyAbortsWhenBackendDown(t *testing.T) {
	var handlers [Members]http.Handler
	_, urls, backends := startTestProxy(t, handlers)
	backends[2].Close()

	client := newTestClient(t)
	resp, err := client.Get(urls[2] + "/version")
	if resp != nil {
		_ = resp.Body.Close()
		t.Fatalf("expected connection error when backend is down, got status %d", resp.StatusCode)
	}
	if err == nil {
		t.Fatal("expected error when backend is down")
	}
}
