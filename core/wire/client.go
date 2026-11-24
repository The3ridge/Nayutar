package wire

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/ownership"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

const (
	defaultMaxResponseBytes int64 = 4 << 20
	defaultMaxWatchLine           = 4 << 20
)

// WatchOptions configures a Client.Watch stream request.
type WatchOptions struct {
	FromRev           store.Rev
	SendInitialEvents bool
	Name              string
}

// Client speaks the Nayutar HTTP/2 LKAL-JSON wire protocol.
type Client struct {
	baseURL          string
	hc               *http.Client
	MaxResponseBytes int64
	BeforeRequest    func(*http.Request)
}

// NewClient constructs a Client using hc against baseURL.
func NewClient(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{}
	}
	return &Client{
		baseURL:          strings.TrimRight(baseURL, "/"),
		hc:               hc,
		MaxResponseBytes: defaultMaxResponseBytes,
	}
}

// HTTPClient returns the underlying *http.Client.
func (c *Client) HTTPClient() *http.Client {
	return c.hc
}

// CloseIdleConnections closes any idle HTTP connections on the underlying transport.
func (c *Client) CloseIdleConnections() {
	c.hc.CloseIdleConnections()
}

func (c *Client) maxResponseBytes() int64 {
	if c.MaxResponseBytes > 0 {
		return c.MaxResponseBytes
	}
	return defaultMaxResponseBytes
}

func (c *Client) newRequest(ctx context.Context, method, path string, query url.Values, body []byte, contentType string) (*http.Request, error) {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", MediaTypeResource)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.BeforeRequest != nil {
		c.BeforeRequest(req)
	}
	return req, nil
}

func (c *Client) readResponseBody(resp *http.Response) ([]byte, error) {
	limit := c.maxResponseBytes()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("wire: response body exceeds %d bytes", limit)
	}
	return data, nil
}

func (c *Client) decodeErrorResponse(resp *http.Response) error {
	data, err := c.readResponseBody(resp)
	if err == nil && len(data) > 0 {
		if we, decErr := DecodeError(data); decErr == nil {
			return we
		}
	}
	return &Error{
		Kind:    kindForStatus(resp.StatusCode),
		Status:  resp.StatusCode,
		Message: nonEmpty(http.StatusText(resp.StatusCode), fmt.Sprintf("http status %d", resp.StatusCode)),
	}
}

func collectionPath(tenantName, group, kind string) string {
	return "/t/" + url.PathEscape(tenantName) + "/o/" + url.PathEscape(group) + "/" + url.PathEscape(kind)
}

func objectPath(tenantName string, obj keyspace.Object) string {
	return collectionPath(tenantName, obj.Group, obj.Kind) + "/" + url.PathEscape(obj.Name)
}

// Create creates a new resource object via POST /t/{tenant}/o/{group}/{kind}.
func (c *Client) Create(ctx context.Context, tenantName, group, kind string, candidate lkal.Record) (lkal.Record, error) {
	body, err := resource.Encode(candidate)
	if err != nil {
		return lkal.Record{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, collectionPath(tenantName, group, kind), nil, body, MediaTypeResource)
	if err != nil {
		return lkal.Record{}, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return lkal.Record{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		return lkal.Record{}, c.decodeErrorResponse(resp)
	}
	return c.decodeResourceResponse(resp)
}

// Get reads a single object via GET /t/{tenant}/o/{group}/{kind}/{name}.
func (c *Client) Get(ctx context.Context, tenantName string, obj keyspace.Object, atRev store.Rev) (lkal.Record, error) {
	if atRev < 0 {
		return lkal.Record{}, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    "atRev",
			Message: "atRev must be >= 0",
		}
	}
	var q url.Values
	if atRev > 0 {
		q = url.Values{"atRev": {strconv.FormatInt(int64(atRev), 10)}}
	}
	req, err := c.newRequest(ctx, http.MethodGet, objectPath(tenantName, obj), q, nil, "")
	if err != nil {
		return lkal.Record{}, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return lkal.Record{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return lkal.Record{}, c.decodeErrorResponse(resp)
	}
	return c.decodeResourceResponse(resp)
}

// List reads objects of (group, kind) via GET /t/{tenant}/o/{group}/{kind}.
// When opts.Limit == 0, List automatically paginates across Continue tokens at a single snapshot revision.
func (c *Client) List(ctx context.Context, tenantName, group, kind string, opts resource.ListOptions) (resource.ListResult, error) {
	if opts.AtRev < 0 {
		return resource.ListResult{}, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    "atRev",
			Message: "atRev must be >= 0",
		}
	}
	if opts.Limit < 0 {
		return resource.ListResult{}, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    "limit",
			Message: "limit must be >= 0",
		}
	}
	if opts.Limit > 0 {
		return c.listPage(ctx, tenantName, group, kind, opts)
	}

	first, err := c.listPage(ctx, tenantName, group, kind, opts)
	if err != nil {
		return resource.ListResult{}, err
	}
	if first.Continue == "" {
		return first, nil
	}
	snapRev := first.Revision
	items := first.Items
	cont := first.Continue
	for cont != "" {
		next, err := c.listPage(ctx, tenantName, group, kind, resource.ListOptions{
			AtRev:    snapRev,
			Continue: cont,
		})
		if err != nil {
			return resource.ListResult{}, err
		}
		if next.Continue != "" && next.Continue == cont {
			return resource.ListResult{}, errors.New("wire: list pagination did not advance")
		}
		items = append(items, next.Items...)
		cont = next.Continue
	}
	return resource.ListResult{
		Items:    items,
		Revision: snapRev,
	}, nil
}

func (c *Client) listPage(ctx context.Context, tenantName, group, kind string, opts resource.ListOptions) (resource.ListResult, error) {
	q := url.Values{}
	if opts.AtRev > 0 {
		q.Set("atRev", strconv.FormatInt(int64(opts.AtRev), 10))
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Continue != "" {
		q.Set("continue", opts.Continue)
	}
	req, err := c.newRequest(ctx, http.MethodGet, collectionPath(tenantName, group, kind), q, nil, "")
	if err != nil {
		return resource.ListResult{}, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return resource.ListResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return resource.ListResult{}, c.decodeErrorResponse(resp)
	}
	data, err := c.readResponseBody(resp)
	if err != nil {
		return resource.ListResult{}, err
	}
	return DecodeList(data)
}

// Update replaces spec/labels via PUT /t/{tenant}/o/{group}/{kind}/{name}.
func (c *Client) Update(ctx context.Context, tenantName string, obj keyspace.Object, candidate lkal.Record) (lkal.Record, error) {
	return c.doResourcePut(ctx, objectPath(tenantName, obj), candidate)
}

// UpdateStatus replaces status via PUT /t/{tenant}/o/{group}/{kind}/{name}/status.
func (c *Client) UpdateStatus(ctx context.Context, tenantName string, obj keyspace.Object, candidate lkal.Record) (lkal.Record, error) {
	return c.doResourcePut(ctx, objectPath(tenantName, obj)+"/status", candidate)
}

func (c *Client) doResourcePut(ctx context.Context, path string, candidate lkal.Record) (lkal.Record, error) {
	body, err := resource.Encode(candidate)
	if err != nil {
		return lkal.Record{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPut, path, nil, body, MediaTypeResource)
	if err != nil {
		return lkal.Record{}, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return lkal.Record{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return lkal.Record{}, c.decodeErrorResponse(resp)
	}
	return c.decodeResourceResponse(resp)
}

// Patch applies ops through the spec/labels entrance via PATCH /t/{tenant}/o/{group}/{kind}/{name}.
func (c *Client) Patch(ctx context.Context, tenantName string, obj keyspace.Object, ops []patch.Op) (lkal.Record, error) {
	return c.doPatch(ctx, objectPath(tenantName, obj), ops)
}

// PatchStatus applies ops through the status entrance via PATCH /t/{tenant}/o/{group}/{kind}/{name}/status.
func (c *Client) PatchStatus(ctx context.Context, tenantName string, obj keyspace.Object, ops []patch.Op) (lkal.Record, error) {
	return c.doPatch(ctx, objectPath(tenantName, obj)+"/status", ops)
}

func (c *Client) doPatch(ctx context.Context, path string, ops []patch.Op) (lkal.Record, error) {
	body, err := patch.Encode(ops)
	if err != nil {
		return lkal.Record{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPatch, path, nil, body, MediaTypePatch)
	if err != nil {
		return lkal.Record{}, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return lkal.Record{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return lkal.Record{}, c.decodeErrorResponse(resp)
	}
	return c.decodeResourceResponse(resp)
}

// Delete removes obj via DELETE /t/{tenant}/o/{group}/{kind}/{name}.
func (c *Client) Delete(ctx context.Context, tenantName string, obj keyspace.Object, rev int64) error {
	if rev < 0 {
		return &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    "rev",
			Message: "rev must be >= 0",
		}
	}
	var q url.Values
	if rev > 0 {
		q = url.Values{"rev": {strconv.FormatInt(rev, 10)}}
	}
	req, err := c.newRequest(ctx, http.MethodDelete, objectPath(tenantName, obj), q, nil, "")
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		return c.decodeErrorResponse(resp)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1))
	return nil
}

// Apply declaratively creates or updates an object via PUT /t/{tenant}/o/{group}/{kind}/{name}/apply.
func (c *Client) Apply(ctx context.Context, tenantName string, obj keyspace.Object, declaration lkal.Record, opts resource.ApplyOptions) (resource.ApplyResult, error) {
	return c.doApplyOrPrune(ctx, http.MethodPut, objectPath(tenantName, obj)+"/apply", declaration, opts, true)
}

// Prune conservatively deletes retired created leaves via POST /t/{tenant}/o/{group}/{kind}/{name}/prune.
func (c *Client) Prune(ctx context.Context, tenantName string, obj keyspace.Object, declaration lkal.Record, opts resource.ApplyOptions) (resource.ApplyResult, error) {
	return c.doApplyOrPrune(ctx, http.MethodPost, objectPath(tenantName, obj)+"/prune", declaration, opts, false)
}

func (c *Client) doApplyOrPrune(ctx context.Context, method, path string, declaration lkal.Record, opts resource.ApplyOptions, allowCreated bool) (resource.ApplyResult, error) {
	if opts.ExpectedOwnerRev < 0 {
		return resource.ApplyResult{}, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Code:    ownership.CodeInvalidInput,
			Path:    "ownerRev",
			Message: "ownerRev must be >= 0",
		}
	}
	body, err := resource.Encode(declaration)
	if err != nil {
		return resource.ApplyResult{}, err
	}
	q := url.Values{}
	if opts.Expect != "" {
		q.Set("expect", opts.Expect)
	}
	if opts.ExpectedOwnerRev > 0 {
		q.Set("ownerRev", strconv.FormatInt(int64(opts.ExpectedOwnerRev), 10))
	}
	req, err := c.newRequest(ctx, method, path, q, body, MediaTypeResource)
	if err != nil {
		return resource.ApplyResult{}, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return resource.ApplyResult{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	created := allowCreated && resp.StatusCode == http.StatusCreated
	if resp.StatusCode != http.StatusOK && !created {
		return resource.ApplyResult{}, c.decodeErrorResponse(resp)
	}
	rec, err := c.decodeResourceResponse(resp)
	if err != nil {
		return resource.ApplyResult{}, err
	}
	ownerRev, err := parseOwnerRevHeader(resp.Header.Get(HeaderOwnerRevision))
	if err != nil {
		return resource.ApplyResult{}, err
	}
	return resource.ApplyResult{
		Object:        rec,
		OwnerRevision: ownerRev,
		Created:       created,
	}, nil
}

// Explain inspects ownership state and condition tokens via GET /t/{tenant}/o/{group}/{kind}/{name}/explain.
func (c *Client) Explain(ctx context.Context, tenantName string, obj keyspace.Object, atRev store.Rev) (ownership.Explanation, error) {
	if atRev < 0 {
		return ownership.Explanation{}, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    "atRev",
			Message: "atRev must be >= 0",
		}
	}
	var q url.Values
	if atRev > 0 {
		q = url.Values{"atRev": {strconv.FormatInt(int64(atRev), 10)}}
	}
	req, err := c.newRequest(ctx, http.MethodGet, objectPath(tenantName, obj)+"/explain", q, nil, "")
	if err != nil {
		return ownership.Explanation{}, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return ownership.Explanation{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ownership.Explanation{}, c.decodeErrorResponse(resp)
	}
	data, err := c.readResponseBody(resp)
	if err != nil {
		return ownership.Explanation{}, err
	}
	return DecodeExplain(data)
}

// Adopt explicitly claims a leaf path via POST /t/{tenant}/o/{group}/{kind}/{name}/adopt.
func (c *Client) Adopt(ctx context.Context, tenantName string, obj keyspace.Object, opts resource.ResolveOptions) (ownership.Explanation, error) {
	return c.doResolve(ctx, objectPath(tenantName, obj)+"/adopt", opts)
}

// Release explicitly releases a self-owned leaf path via POST /t/{tenant}/o/{group}/{kind}/{name}/release.
func (c *Client) Release(ctx context.Context, tenantName string, obj keyspace.Object, opts resource.ResolveOptions) (ownership.Explanation, error) {
	return c.doResolve(ctx, objectPath(tenantName, obj)+"/release", opts)
}

func (c *Client) doResolve(ctx context.Context, path string, opts resource.ResolveOptions) (ownership.Explanation, error) {
	body, err := EncodeResolveRequest(opts)
	if err != nil {
		return ownership.Explanation{}, err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, nil, body, MediaTypeResource)
	if err != nil {
		return ownership.Explanation{}, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return ownership.Explanation{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ownership.Explanation{}, c.decodeErrorResponse(resp)
	}
	data, err := c.readResponseBody(resp)
	if err != nil {
		return ownership.Explanation{}, err
	}
	return DecodeExplain(data)
}

func parseOwnerRevHeader(raw string) (store.Rev, error) {
	if raw == "" {
		return 0, errors.New("wire: missing Nayutar-Owner-Revision header")
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("wire: invalid Nayutar-Owner-Revision header %q", raw)
	}
	return store.Rev(n), nil
}

// Watch opens a streaming NDJSON watch via GET /t/{tenant}/o/{group}/{kind}?watch=true.
func (c *Client) Watch(ctx context.Context, tenantName, group, kind string, opts WatchOptions) (*WatchStream, error) {
	if opts.FromRev < 0 {
		return nil, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    "fromRev",
			Message: "fromRev must be >= 0",
		}
	}
	q := url.Values{"watch": {"true"}}
	if opts.FromRev > 0 {
		q.Set("fromRev", strconv.FormatInt(int64(opts.FromRev), 10))
	}
	if opts.SendInitialEvents {
		q.Set("sendInitialEvents", "true")
	}
	if opts.Name != "" {
		q.Set("name", opts.Name)
	}
	req, err := c.newRequest(ctx, http.MethodGet, collectionPath(tenantName, group, kind), q, nil, "")
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, c.decodeErrorResponse(resp)
	}
	maxLine := int(c.maxResponseBytes())
	if maxLine <= 0 {
		maxLine = defaultMaxWatchLine
	}
	return &WatchStream{
		body:       resp.Body,
		reader:     bufio.NewReader(resp.Body),
		maxLine:    maxLine,
		protoMajor: resp.ProtoMajor,
	}, nil
}

func (c *Client) decodeResourceResponse(resp *http.Response) (lkal.Record, error) {
	data, err := c.readResponseBody(resp)
	if err != nil {
		return lkal.Record{}, err
	}
	rec, err := resource.Decode(data)
	if err != nil {
		return lkal.Record{}, err
	}
	if err := envelope.CheckShape(rec); err != nil {
		return lkal.Record{}, err
	}
	return rec, nil
}

// WatchStream reads NDJSON watch events from an active HTTP/2 watch response.
type WatchStream struct {
	body       io.ReadCloser
	reader     *bufio.Reader
	maxLine    int
	protoMajor int

	mu     sync.Mutex
	closed bool
	err    error
}

// ProtoMajor returns the negotiated HTTP major protocol version of the stream.
func (s *WatchStream) ProtoMajor() int {
	return s.protoMajor
}

// Next reads and decodes the next WatchEvent from the stream.
// A terminal EventError closes the stream, records the *Error in Err, and returns false.
func (s *WatchStream) Next() (WatchEvent, bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return WatchEvent{}, false
	}
	s.mu.Unlock()

	line, err := readLineBounded(s.reader, s.maxLine)
	if err != nil {
		if errors.Is(err, io.EOF) && len(line) == 0 {
			_ = s.Close()
			return WatchEvent{}, false
		}
		s.fail(err)
		return WatchEvent{}, false
	}
	ev, err := DecodeWatchEvent(line)
	if err != nil {
		s.fail(err)
		return WatchEvent{}, false
	}
	if ev.Type == EventError {
		s.fail(ev.Error)
		return WatchEvent{}, false
	}
	return ev, true
}

// Err returns the terminal error (including *Error from a terminal .error event), if any.
func (s *WatchStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Close closes the underlying HTTP response body.
func (s *WatchStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.body.Close()
}

func (s *WatchStream) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	already := s.closed
	s.closed = true
	s.mu.Unlock()
	if !already {
		_ = s.body.Close()
	}
}

func readLineBounded(r *bufio.Reader, maxBytes int) ([]byte, error) {
	var line []byte
	for {
		frag, err := r.ReadSlice('\n')
		line = append(line, frag...)
		if len(line) > maxBytes {
			return nil, fmt.Errorf("wire: watch event line exceeds %d bytes", maxBytes)
		}
		if err == nil {
			return bytes.TrimSuffix(line, []byte{'\n'}), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			return line, io.ErrUnexpectedEOF
		}
		return nil, err
	}
}
