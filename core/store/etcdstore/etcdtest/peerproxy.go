package etcdtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errPartitionAborted = errors.New("partition aborted")

type linkCtxKey struct{}

type linkCtx struct {
	st  *linkState
	seq uint64
}

type linkState struct {
	cut      bool
	seq      uint64
	healedCh chan struct{}
	cancels  map[uint64]context.CancelFunc
}

type peerProxy struct {
	transport *http.Transport
	servers   [Members]*http.Server

	mu           sync.Mutex
	closed       bool
	nextReqID    uint64
	memberHexIDs [Members]string
	links        [Members]map[string]*linkState
}

func newPeerProxy(listenURLs, targetURLs [Members]string) (*peerProxy, error) {
	tr := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
	}
	p := &peerProxy{
		transport: tr,
	}
	for i := range Members {
		p.links[i] = make(map[string]*linkState)

		listenURL, err := url.Parse(listenURLs[i])
		if err != nil {
			p.close()
			return nil, fmt.Errorf("parse proxy listen URL %q: %w", listenURLs[i], err)
		}
		targetURL, err := url.Parse(targetURLs[i])
		if err != nil {
			p.close()
			return nil, fmt.Errorf("parse proxy target URL %q: %w", targetURLs[i], err)
		}
		ln, err := net.Listen("tcp", listenURL.Host)
		if err != nil {
			p.close()
			return nil, fmt.Errorf("listen peer proxy %d on %s: %w", i, listenURL.Host, err)
		}
		rp := &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(targetURL)
			},
			FlushInterval:  -1,
			Transport:      tr,
			ErrorLog:       log.New(io.Discard, "", 0),
			ModifyResponse: p.modifyResponse,
			ErrorHandler:   p.errorHandler,
		}
		srv := &http.Server{
			Handler:  p.handler(i, rp),
			ErrorLog: log.New(io.Discard, "", 0),
		}
		p.servers[i] = srv
		go func() {
			_ = srv.Serve(ln)
		}()
	}
	return p, nil
}

func (p *peerProxy) setMemberIDs(ids [Members]uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, id := range ids {
		p.memberHexIDs[i] = strconv.FormatUint(id, 16)
	}
}

func (p *peerProxy) linkLocked(to int, fromHex string) *linkState {
	st := p.links[to][fromHex]
	if st == nil {
		st = &linkState{}
		p.links[to][fromHex] = st
	}
	return st
}

func (p *peerProxy) cutLinkLocked(to int, fromHex string) {
	st := p.linkLocked(to, fromHex)
	if !st.cut {
		st.cut = true
		st.seq++
		st.healedCh = make(chan struct{})
	}
}

func (p *peerProxy) isolateMember(i int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for j := range Members {
		if j == i {
			continue
		}
		p.cutLinkLocked(i, p.memberHexIDs[j])
		p.cutLinkLocked(j, p.memberHexIDs[i])
	}
}

func (p *peerProxy) healLinksLocked(includeUncut bool) []context.CancelFunc {
	var cancels []context.CancelFunc
	for to := range Members {
		for _, st := range p.links[to] {
			if !st.cut && !includeUncut {
				continue
			}
			if st.cut {
				st.cut = false
				st.seq++
				close(st.healedCh)
				st.healedCh = nil
			}
			for _, cancel := range st.cancels {
				cancels = append(cancels, cancel)
			}
			st.cancels = nil
		}
	}
	return cancels
}

func (p *peerProxy) healPartition() {
	p.mu.Lock()
	cancels := p.healLinksLocked(false)
	p.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}

func (p *peerProxy) close() {
	p.mu.Lock()
	p.closed = true
	cancels := p.healLinksLocked(true)
	p.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, srv := range p.servers {
		if srv != nil {
			_ = srv.Close()
		}
	}
	p.transport.CloseIdleConnections()
}

func isRaftPath(path string) bool {
	return path == "/raft" || path == "/raft/snapshot" || strings.HasPrefix(path, "/raft/stream/")
}

func (p *peerProxy) handler(to int, rp *httputil.ReverseProxy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Health probes lack X-Server-From and share keep-alive connections with Raft requests, so inspect every request.
		fromHex := r.Header.Get("X-Server-From")
		if fromHex == "" {
			// Fail closed on Raft paths if etcd ever drops or renames X-Server-From, while letting probing and metadata pass.
			if isRaftPath(r.URL.Path) {
				panic(http.ErrAbortHandler)
			}
			rp.ServeHTTP(w, r)
			return
		}

		reqCtx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		defer cancel()

		st, reqID, healedCh, seq, closed := p.enterRequest(to, fromHex, cancel)
		if closed {
			panic(http.ErrAbortHandler)
		}
		if healedCh != nil {
			// Hold without closing so peers experience silence until raft timeouts expire, then abort on heal so they reconnect.
			select {
			case <-healedCh:
			case <-r.Context().Done():
			}
			panic(http.ErrAbortHandler)
		}
		defer p.leaveRequest(st, reqID)

		stop := context.AfterFunc(r.Context(), func() {
			p.mu.Lock()
			cut := st.cut
			p.mu.Unlock()
			if !cut {
				cancel()
			}
		})
		defer stop()

		reqCtx = context.WithValue(reqCtx, linkCtxKey{}, linkCtx{st: st, seq: seq})
		outReq := r.WithContext(reqCtx)
		if outReq.Body != nil && outReq.Body != http.NoBody {
			outReq.Body = &cutReadCloser{
				inner: outReq.Body,
				proxy: p,
				st:    st,
				seq:   seq,
				ctx:   reqCtx,
			}
		}
		rp.ServeHTTP(w, outReq)
	})
}

func (p *peerProxy) modifyResponse(resp *http.Response) error {
	lc, ok := resp.Request.Context().Value(linkCtxKey{}).(linkCtx)
	if !ok {
		return nil
	}
	if err := p.waitIfCut(resp.Request.Context(), lc.st, lc.seq); err != nil {
		return err
	}
	resp.Body = &cutReadCloser{
		inner: resp.Body,
		proxy: p,
		st:    lc.st,
		seq:   lc.seq,
		ctx:   resp.Request.Context(),
	}
	return nil
}

func (p *peerProxy) errorHandler(_ http.ResponseWriter, req *http.Request, _ error) {
	if lc, ok := req.Context().Value(linkCtxKey{}).(linkCtx); ok && req.Context().Err() == nil {
		_ = p.waitIfCut(req.Context(), lc.st, lc.seq)
	}
	panic(http.ErrAbortHandler)
}

func (p *peerProxy) enterRequest(to int, fromHex string, cancel context.CancelFunc) (st *linkState, reqID uint64, healedCh <-chan struct{}, seq uint64, closed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, 0, nil, 0, true
	}
	st = p.linkLocked(to, fromHex)
	if st.cut {
		return st, 0, st.healedCh, st.seq, false
	}
	p.nextReqID++
	reqID = p.nextReqID
	if st.cancels == nil {
		st.cancels = make(map[uint64]context.CancelFunc)
	}
	st.cancels[reqID] = cancel
	return st, reqID, nil, st.seq, false
}

func (p *peerProxy) leaveRequest(st *linkState, reqID uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(st.cancels, reqID)
}

func (p *peerProxy) waitIfCut(ctx context.Context, st *linkState, seq uint64) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errPartitionAborted
	}
	if st.cut {
		healedCh := st.healedCh
		p.mu.Unlock()
		select {
		case <-healedCh:
			return errPartitionAborted
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	changed := st.seq != seq
	p.mu.Unlock()
	if changed {
		return errPartitionAborted
	}
	return nil
}

type cutReadCloser struct {
	inner io.ReadCloser
	proxy *peerProxy
	st    *linkState
	seq   uint64
	ctx   context.Context
}

func (c *cutReadCloser) Read(p []byte) (int, error) {
	if err := c.proxy.waitIfCut(c.ctx, c.st, c.seq); err != nil {
		return 0, err
	}
	n, err := c.inner.Read(p)
	// A Read that entered before the cut must discard bytes received after the cut begins rather than leaking them across the partition.
	if waitErr := c.proxy.waitIfCut(c.ctx, c.st, c.seq); waitErr != nil {
		return 0, waitErr
	}
	return n, err
}

func (c *cutReadCloser) Close() error {
	return c.inner.Close()
}
