package wire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache"
)

const (
	// DefaultMaxBodyBytes is the default HTTP request body limit (1 MiB).
	DefaultMaxBodyBytes int64 = 1 << 20
	// DefaultMaxListLimit is the default page cap applied to list requests.
	DefaultMaxListLimit = 1000
)

// Config configures a wire Handler.
type Config struct {
	Registry         *tenant.Registry
	Caches           *watchcache.Manager
	Schemas          resource.Schemas
	Authorizer       refs.Authorizer
	Authenticate     func(*http.Request) (refs.Principal, error)
	DefaultPrincipal refs.Principal
	Now              func() time.Time
	Rand             io.Reader
	MaxBodyBytes     int64
	MaxListLimit     int
}

// Handler serves the Nayutar HTTP/2 LKAL-JSON wire API.
type Handler struct {
	registry         *tenant.Registry
	caches           *watchcache.Manager
	schemas          resource.Schemas
	authorizer       refs.Authorizer
	authenticate     func(*http.Request) (refs.Principal, error)
	defaultPrincipal refs.Principal
	now              func() time.Time
	rand             io.Reader
	maxBodyBytes     int64
	maxListLimit     int
}

// New constructs a Handler from cfg.
func New(cfg Config) (*Handler, error) {
	if cfg.Registry == nil {
		return nil, errors.New("wire: nil tenant registry")
	}
	if cfg.Caches == nil {
		return nil, errors.New("wire: nil watchcache manager")
	}
	if cfg.MaxBodyBytes < 0 {
		return nil, errors.New("wire: negative MaxBodyBytes")
	}
	if cfg.MaxListLimit < 0 {
		return nil, errors.New("wire: negative MaxListLimit")
	}
	clonedSchemas := maps.Clone(cfg.Schemas)
	for qkind, ks := range clonedSchemas {
		if _, _, err := keyspace.ParseQualifiedKind(qkind); err != nil {
			return nil, fmt.Errorf("wire: invalid schema kind %q: %w", qkind, err)
		}
		if ks == nil {
			return nil, fmt.Errorf("wire: nil schema for kind %q", qkind)
		}
	}
	maxBody := cfg.MaxBodyBytes
	if maxBody == 0 {
		maxBody = DefaultMaxBodyBytes
	}
	maxList := cfg.MaxListLimit
	if maxList == 0 {
		maxList = DefaultMaxListLimit
	}
	return &Handler{
		registry:         cfg.Registry,
		caches:           cfg.Caches,
		schemas:          clonedSchemas,
		authorizer:       cfg.Authorizer,
		authenticate:     cfg.Authenticate,
		defaultPrincipal: cfg.DefaultPrincipal,
		now:              cfg.Now,
		rand:             cfg.Rand,
		maxBodyBytes:     maxBody,
		maxListLimit:     maxList,
	}, nil
}

type routeKind uint8

const (
	routeCollection routeKind = iota + 1
	routeObject
	routeStatus
)

type parsedRoute struct {
	kind   routeKind
	tenant string
	group  string
	res    string
	name   string
}

func (p parsedRoute) object() keyspace.Object {
	return keyspace.Object{Group: p.group, Kind: p.res, Name: p.name}
}

func parseRoute(escapedPath string) (parsedRoute, bool) {
	if !strings.HasPrefix(escapedPath, "/t/") {
		return parsedRoute{}, false
	}
	parts := strings.Split(escapedPath[1:], "/")
	if len(parts) < 5 || parts[0] != "t" || parts[2] != "o" {
		return parsedRoute{}, false
	}
	switch len(parts) {
	case 5:
		return parsedRoute{kind: routeCollection, tenant: parts[1], group: parts[3], res: parts[4]}, true
	case 6:
		return parsedRoute{kind: routeObject, tenant: parts[1], group: parts[3], res: parts[4], name: parts[5]}, true
	case 7:
		if parts[6] != "status" {
			return parsedRoute{}, false
		}
		return parsedRoute{kind: routeStatus, tenant: parts[1], group: parts[3], res: parts[4], name: parts[5]}, true
	default:
		return parsedRoute{}, false
	}
}

func allowHeader(rk routeKind) string {
	switch rk {
	case routeCollection:
		return "GET, POST"
	case routeObject:
		return "GET, PUT, PATCH, DELETE"
	case routeStatus:
		return "PUT, PATCH"
	default:
		return ""
	}
}

func methodAllowed(rk routeKind, method string) bool {
	switch rk {
	case routeCollection:
		return method == http.MethodGet || method == http.MethodPost
	case routeObject:
		return method == http.MethodGet || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
	case routeStatus:
		return method == http.MethodPut || method == http.MethodPatch
	default:
		return false
	}
}

// ServeHTTP dispatches an HTTP request against the Nayutar LKAL-JSON wire API.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())

	route, ok := parseRoute(r.URL.EscapedPath())
	if !ok {
		if r.Method == http.MethodTrace {
			h.writeError(w, &Error{
				Kind:    KindMethodNotAllowed,
				Status:  http.StatusMethodNotAllowed,
				Message: "method not allowed",
			})
			return
		}
		h.writeError(w, &Error{
			Kind:    KindNotFound,
			Status:  http.StatusNotFound,
			Message: "route not found",
		})
		return
	}

	if route.kind == routeObject || route.kind == routeStatus {
		w.Header().Set("Accept-Patch", MediaTypePatch)
	}

	if !methodAllowed(route.kind, r.Method) {
		w.Header().Set("Allow", allowHeader(route.kind))
		h.writeError(w, &Error{
			Kind:    KindMethodNotAllowed,
			Status:  http.StatusMethodNotAllowed,
			Message: "method not allowed",
		})
		return
	}

	if !acceptsMediaType(r.Header.Values("Accept"), MediaTypeResource) {
		h.writeError(w, &Error{
			Kind:    KindNotAcceptable,
			Status:  http.StatusNotAcceptable,
			Message: "requested media type is not supported",
		})
		return
	}

	principal, authErr := h.authenticateRequest(r)
	if authErr != nil {
		h.writeError(w, authErr)
		return
	}

	if err := validateRouteSegments(route); err != nil {
		h.writeError(w, err)
		return
	}

	query, err := parseStrictQuery(r.URL.RawQuery)
	if err != nil {
		h.writeError(w, err)
		return
	}

	switch route.kind {
	case routeCollection:
		switch r.Method {
		case http.MethodPost:
			h.handleCreate(w, r, route, query, principal)
		case http.MethodGet:
			if _, hasWatch := query["watch"]; hasWatch {
				h.handleWatch(w, r, route, query)
			} else {
				h.handleList(w, r, route, query)
			}
		}
	case routeObject:
		switch r.Method {
		case http.MethodGet:
			h.handleGet(w, r, route, query)
		case http.MethodPut:
			h.handleUpdate(w, r, route, query, principal)
		case http.MethodPatch:
			h.handlePatch(w, r, route, query, principal)
		case http.MethodDelete:
			h.handleDelete(w, r, route, query)
		}
	case routeStatus:
		switch r.Method {
		case http.MethodPut:
			h.handleUpdateStatus(w, r, route, query)
		case http.MethodPatch:
			h.handlePatchStatus(w, r, route, query)
		}
	}
}

func (h *Handler) authenticateRequest(r *http.Request) (refs.Principal, *Error) {
	if h.authenticate == nil {
		// TODO(security): require client authentication and per-principal rate limiting once authn lands.
		return h.defaultPrincipal, nil
	}
	principal, err := h.authenticate(r)
	if err != nil {
		return refs.Principal{}, &Error{
			Kind:    KindUnauthorized,
			Status:  http.StatusUnauthorized,
			Message: "unauthorized",
		}
	}
	return principal, nil
}

func validateRouteSegments(route parsedRoute) *Error {
	if err := keyspace.ValidateTenant(route.tenant); err != nil {
		return &Error{Kind: KindBadRequest, Status: http.StatusBadRequest, Path: "tenant", Message: "invalid tenant segment"}
	}
	if err := keyspace.ValidateGroup(route.group); err != nil {
		return &Error{Kind: KindBadRequest, Status: http.StatusBadRequest, Path: "meta/group", Message: "invalid group segment"}
	}
	if err := keyspace.ValidateKind(route.res); err != nil {
		return &Error{Kind: KindBadRequest, Status: http.StatusBadRequest, Path: "meta/kind", Message: "invalid kind segment"}
	}
	if route.kind != routeCollection {
		if err := keyspace.ValidateName(route.name); err != nil {
			return &Error{Kind: KindBadRequest, Status: http.StatusBadRequest, Path: "meta/name", Message: "invalid name segment"}
		}
	}
	return nil
}

func parseStrictQuery(raw string) (map[string]string, *Error) {
	if raw == "" {
		return nil, nil
	}
	if strings.Contains(raw, ";") {
		return nil, &Error{Kind: KindBadRequest, Status: http.StatusBadRequest, Message: "invalid query syntax"}
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return nil, &Error{Kind: KindBadRequest, Status: http.StatusBadRequest, Message: "invalid query syntax"}
	}
	out := make(map[string]string, len(vals))
	for k, list := range vals {
		if !utf8.ValidString(k) {
			return nil, &Error{
				Kind:    KindBadRequest,
				Status:  http.StatusBadRequest,
				Message: "invalid UTF-8 in query parameter name",
			}
		}
		if len(list) != 1 {
			return nil, &Error{
				Kind:    KindBadRequest,
				Status:  http.StatusBadRequest,
				Path:    k,
				Message: fmt.Sprintf("query parameter %q must not be repeated", k),
			}
		}
		if !utf8.ValidString(list[0]) {
			return nil, &Error{
				Kind:    KindBadRequest,
				Status:  http.StatusBadRequest,
				Path:    k,
				Message: "invalid UTF-8 in query parameter value",
			}
		}
		out[k] = list[0]
	}
	return out, nil
}

func rejectUnknownParams(query map[string]string, allowed ...string) *Error {
	for k := range query {
		known := false
		for _, a := range allowed {
			if k == a {
				known = true
				break
			}
		}
		if !known {
			return &Error{
				Kind:    KindBadRequest,
				Status:  http.StatusBadRequest,
				Path:    k,
				Message: fmt.Sprintf("unknown query parameter %q", k),
			}
		}
	}
	return nil
}

func parseNonNegInt64Param(query map[string]string, name string) (int64, bool, *Error) {
	raw, ok := query[name]
	if !ok {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, false, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    name,
			Message: fmt.Sprintf("query parameter %q must be a non-negative integer", name),
		}
	}
	return n, true, nil
}

func parseBoolParam(query map[string]string, name string) (bool, bool, *Error) {
	raw, ok := query[name]
	if !ok {
		return false, false, nil
	}
	switch raw {
	case "true":
		return true, true, nil
	case "false":
		return false, true, nil
	default:
		return false, false, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    name,
			Message: fmt.Sprintf("query parameter %q must be true or false", name),
		}
	}
}

func (h *Handler) requireEmptyBody(r *http.Request) *Error {
	if r.ContentLength > 0 {
		return &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Message: "request body is not allowed for this method",
		}
	}
	if r.Body == nil {
		return nil
	}
	var buf [1]byte
	n, err := r.Body.Read(buf[:])
	if n > 0 {
		return &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Message: "request body is not allowed for this method",
		}
	}
	if err != nil && err != io.EOF {
		return &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Message: "failed to read request body",
		}
	}
	return nil
}

func (h *Handler) readBody(w http.ResponseWriter, r *http.Request, wantMediaType string) ([]byte, *Error) {
	if !checkContentType(r.Header.Values("Content-Type"), wantMediaType) {
		return nil, &Error{
			Kind:    KindUnsupportedMediaType,
			Status:  http.StatusUnsupportedMediaType,
			Message: fmt.Sprintf("content-type must be %s", wantMediaType),
		}
	}
	if r.Body == nil {
		return nil, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Message: "empty request body",
		}
	}
	limited := http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	body, err := io.ReadAll(limited)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return nil, &Error{
				Kind:    KindTooLarge,
				Status:  http.StatusRequestEntityTooLarge,
				Message: "request body exceeds maximum allowed size",
			}
		}
		return nil, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Message: "failed to read request body",
		}
	}
	return body, nil
}

func (h *Handler) hasKind(group, kind string) bool {
	qkind, err := keyspace.QualifiedKind(group, kind)
	if err != nil {
		return false
	}
	return h.schemas[qkind] != nil
}

func (h *Handler) openReadCache(ctx context.Context, route parsedRoute) (*watchcache.Cache, *Error) {
	if !h.hasKind(route.group, route.res) {
		return nil, &Error{
			Kind:    KindNotFound,
			Status:  http.StatusNotFound,
			Path:    "meta/kind",
			Message: "unknown resource kind",
		}
	}
	rec, err := h.registry.Get(ctx, route.tenant)
	if err != nil {
		return nil, mapBackendError(err)
	}
	if rec.State != tenant.StateActive {
		return nil, &Error{
			Kind:    KindConflict,
			Status:  http.StatusConflict,
			Message: "tenant is terminating",
		}
	}
	cache, err := h.caches.Objects(route.tenant, route.group, route.res)
	if err != nil {
		return nil, mapBackendError(err)
	}
	return cache, nil
}

func (h *Handler) openWriteObjects(ctx context.Context, tenantName string) (*resource.Objects, *Error) {
	view, err := h.registry.View(ctx, tenantName)
	if err != nil {
		return nil, mapBackendError(err)
	}
	return resource.NewObjects(view, resource.Config{
		Now:        h.now,
		Rand:       h.rand,
		Tenant:     tenantName,
		Schemas:    h.schemas,
		Authorizer: h.authorizer,
	}), nil
}

func decodeJSONError(err error) *Error {
	var el *lkal.ErrorList
	if errors.As(err, &el) && len(el.Errors) > 0 {
		first := el.Errors[0]
		msg := first.Code + " " + first.Msg
		if first.Hint != "" {
			msg += ": " + first.Hint
		}
		return &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    first.Path,
			Message: msg,
		}
	}
	return &Error{
		Kind:    KindBadRequest,
		Status:  http.StatusBadRequest,
		Message: "invalid LKAL-JSON body",
	}
}

func patchSchemaError(err error) *Error {
	var el *lkal.ErrorList
	if errors.As(err, &el) && len(el.Errors) > 0 {
		first := el.Errors[0]
		msg := first.Code + " " + first.Msg
		if first.Hint != "" {
			msg += ": " + first.Hint
		}
		return &Error{
			Kind:    KindUnprocessable,
			Status:  http.StatusUnprocessableEntity,
			Path:    first.Path,
			Message: msg,
		}
	}
	return &Error{
		Kind:    KindUnprocessable,
		Status:  http.StatusUnprocessableEntity,
		Message: "malformed patch",
	}
}

func (h *Handler) handleCreate(w http.ResponseWriter, r *http.Request, route parsedRoute, query map[string]string, principal refs.Principal) {
	if err := rejectUnknownParams(query); err != nil {
		h.writeError(w, err)
		return
	}
	body, werr := h.readBody(w, r, MediaTypeResource)
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	cand, err := resource.Decode(body)
	if err != nil {
		h.writeError(w, decodeJSONError(err))
		return
	}
	obj, err := envelope.Object(cand)
	if err != nil {
		h.writeError(w, mapBackendError(err))
		return
	}
	if obj.Group != route.group || obj.Kind != route.res {
		h.writeError(w, &Error{
			Kind:    KindForbidden,
			Status:  http.StatusForbidden,
			Path:    "meta",
			Message: "candidate group and kind must match request path",
		})
		return
	}
	if !h.hasKind(route.group, route.res) {
		h.writeError(w, &Error{
			Kind:    KindUnprocessable,
			Status:  http.StatusUnprocessableEntity,
			Path:    "meta/kind",
			Message: "unknown resource kind",
		})
		return
	}
	objects, werr := h.openWriteObjects(r.Context(), route.tenant)
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	created, err := objects.Create(r.Context(), principal, cand)
	if err != nil {
		h.writeError(w, mapBackendError(err))
		return
	}
	encoded, err := resource.Encode(created)
	if err != nil {
		h.writeError(w, internalWireError())
		return
	}
	h.writeJSON(w, http.StatusCreated, encoded)
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request, route parsedRoute, query map[string]string) {
	if err := h.requireEmptyBody(r); err != nil {
		h.writeError(w, err)
		return
	}
	if err := rejectUnknownParams(query, "atRev"); err != nil {
		h.writeError(w, err)
		return
	}
	atRev, _, werr := parseNonNegInt64Param(query, "atRev")
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	cache, werr := h.openReadCache(r.Context(), route)
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	obj := route.object()
	key, err := (keyspace.Key{
		Family: keyspace.FamilyObject,
		Tenant: route.tenant,
		Object: obj,
	}).Rel()
	if err != nil {
		h.writeError(w, &Error{Kind: KindBadRequest, Status: http.StatusBadRequest, Message: "invalid object key"})
		return
	}
	kv, _, err := cache.Get(r.Context(), key, store.Rev(atRev))
	if err != nil {
		h.writeError(w, mapBackendError(err))
		return
	}
	doc, err := resource.DecodeObject(obj, kv.Value, kv.ModRev)
	if err != nil {
		h.writeError(w, mapBackendError(err))
		return
	}
	encoded, err := resource.Encode(doc)
	if err != nil {
		h.writeError(w, internalWireError())
		return
	}
	h.writeJSON(w, http.StatusOK, encoded)
}

func (h *Handler) handleList(w http.ResponseWriter, r *http.Request, route parsedRoute, query map[string]string) {
	if err := h.requireEmptyBody(r); err != nil {
		h.writeError(w, err)
		return
	}
	if err := rejectUnknownParams(query, "atRev", "limit", "continue"); err != nil {
		h.writeError(w, err)
		return
	}
	atRev, _, werr := parseNonNegInt64Param(query, "atRev")
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	limitVal, hasLimit, werr := parseNonNegInt64Param(query, "limit")
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	if hasLimit && limitVal == 0 {
		h.writeError(w, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    "limit",
			Message: `query parameter "limit" must be >= 1`,
		})
		return
	}
	prefix, err := keyspace.ObjectPrefix(route.group, route.res)
	if err != nil {
		h.writeError(w, &Error{Kind: KindBadRequest, Status: http.StatusBadRequest, Message: "invalid object prefix"})
		return
	}
	cont, hasCont := query["continue"]
	if hasCont {
		parsed, err := keyspace.ParseRel(route.tenant, cont)
		if err != nil || parsed.Family != keyspace.FamilyObject || parsed.Object.Group != route.group || parsed.Object.Kind != route.res {
			h.writeError(w, &Error{
				Kind:    KindBadRequest,
				Status:  http.StatusBadRequest,
				Path:    "continue",
				Message: "invalid continuation token",
			})
			return
		}
	}

	cache, werr := h.openReadCache(r.Context(), route)
	if werr != nil {
		h.writeError(w, werr)
		return
	}

	pageLimit := h.maxListLimit
	if hasLimit && limitVal < int64(pageLimit) {
		pageLimit = int(limitVal)
	}
	kvs, rev, nextCont, err := cache.Range(r.Context(), prefix, store.Rev(atRev), pageLimit, cont)
	if err != nil {
		h.writeError(w, mapBackendError(err))
		return
	}

	items := make([]lkal.Record, 0, len(kvs))
	for _, kv := range kvs {
		parsed, err := keyspace.ParseRel(route.tenant, kv.Key)
		if err != nil || parsed.Family != keyspace.FamilyObject || parsed.Object.Group != route.group || parsed.Object.Kind != route.res {
			h.writeError(w, internalWireError())
			return
		}
		doc, err := resource.DecodeObject(parsed.Object, kv.Value, kv.ModRev)
		if err != nil {
			h.writeError(w, mapBackendError(err))
			return
		}
		items = append(items, doc)
	}

	encoded, err := EncodeList(resource.ListResult{
		Items:    items,
		Revision: rev,
		Continue: nextCont,
	})
	if err != nil {
		h.writeError(w, internalWireError())
		return
	}
	h.writeJSON(w, http.StatusOK, encoded)
}

func (h *Handler) handleWatch(w http.ResponseWriter, r *http.Request, route parsedRoute, query map[string]string) {
	if err := h.requireEmptyBody(r); err != nil {
		h.writeError(w, err)
		return
	}
	if err := rejectUnknownParams(query, "watch", "fromRev", "sendInitialEvents", "name"); err != nil {
		h.writeError(w, err)
		return
	}
	if query["watch"] != "true" {
		h.writeError(w, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    "watch",
			Message: `query parameter "watch" must be true`,
		})
		return
	}
	fromRev, _, werr := parseNonNegInt64Param(query, "fromRev")
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	sendInitial, _, werr := parseBoolParam(query, "sendInitialEvents")
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	prefix, err := keyspace.ObjectPrefix(route.group, route.res)
	if err != nil {
		h.writeError(w, &Error{Kind: KindBadRequest, Status: http.StatusBadRequest, Message: "invalid object prefix"})
		return
	}
	watchPrefix := prefix
	exactKey := ""
	if nameFilter, hasName := query["name"]; hasName {
		if err := keyspace.ValidateName(nameFilter); err != nil {
			h.writeError(w, &Error{
				Kind:    KindBadRequest,
				Status:  http.StatusBadRequest,
				Path:    "name",
				Message: "invalid name filter",
			})
			return
		}
		watchPrefix = prefix + nameFilter
		exactKey = prefix + nameFilter
	}

	cache, werr := h.openReadCache(r.Context(), route)
	if werr != nil {
		h.writeError(w, werr)
		return
	}

	watchCtx, cancelWatch := context.WithCancel(r.Context())
	defer cancelWatch()

	stream, err := cache.Watch(watchCtx, watchPrefix, watchcache.WatchOptions{
		FromRev:           store.Rev(fromRev),
		SendInitialEvents: sendInitial,
	})
	if err != nil {
		h.writeError(w, mapBackendError(err))
		return
	}

	w.Header().Set("Content-Type", MediaTypeResource)
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		return
	}

	writeEvent := func(ev WatchEvent) bool {
		line, err := EncodeWatchEvent(ev)
		if err != nil {
			return false
		}
		if _, err := w.Write(line); err != nil {
			return false
		}
		if _, err := w.Write([]byte{'\n'}); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	for ev := range stream.Events() {
		switch ev.Type {
		case watchcache.EventPut:
			if exactKey != "" && ev.Key != exactKey {
				continue
			}
			parsed, err := keyspace.ParseRel(route.tenant, ev.Key)
			if err != nil || parsed.Family != keyspace.FamilyObject || parsed.Object.Group != route.group || parsed.Object.Kind != route.res {
				writeEvent(WatchEvent{Type: EventError, Error: internalWireError()})
				return
			}
			doc, err := resource.DecodeObject(parsed.Object, ev.Value, ev.Rev)
			if err != nil {
				writeEvent(WatchEvent{Type: EventError, Error: internalWireError()})
				return
			}
			if !writeEvent(WatchEvent{
				Type:     EventPut,
				Revision: ev.Rev,
				Object:   &doc,
			}) {
				return
			}

		case watchcache.EventDelete:
			if exactKey != "" && ev.Key != exactKey {
				continue
			}
			parsed, err := keyspace.ParseRel(route.tenant, ev.Key)
			if err != nil || parsed.Family != keyspace.FamilyObject || parsed.Object.Group != route.group || parsed.Object.Kind != route.res {
				writeEvent(WatchEvent{Type: EventError, Error: internalWireError()})
				return
			}
			if !writeEvent(WatchEvent{
				Type:     EventDelete,
				Revision: ev.Rev,
				Target: &WatchTarget{
					Group: parsed.Object.Group,
					Kind:  parsed.Object.Kind,
					Name:  parsed.Object.Name,
				},
			}) {
				return
			}

		case watchcache.EventBookmark:
			if !writeEvent(WatchEvent{
				Type:             EventBookmark,
				Revision:         ev.Rev,
				InitialEventsEnd: ev.InitialEventsEnd,
			}) {
				return
			}
		}
	}

	if wErr := stream.Err(); wErr != nil && r.Context().Err() == nil {
		writeEvent(WatchEvent{
			Type:  EventError,
			Error: mapBackendError(wErr),
		})
	}
}

func (h *Handler) handleUpdate(w http.ResponseWriter, r *http.Request, route parsedRoute, query map[string]string, principal refs.Principal) {
	h.handleUpdateEntrance(w, r, route, query, func(objects *resource.Objects, cand lkal.Record) (lkal.Record, error) {
		return objects.Update(r.Context(), principal, cand)
	})
}

func (h *Handler) handleUpdateStatus(w http.ResponseWriter, r *http.Request, route parsedRoute, query map[string]string) {
	h.handleUpdateEntrance(w, r, route, query, func(objects *resource.Objects, cand lkal.Record) (lkal.Record, error) {
		return objects.UpdateStatus(r.Context(), cand)
	})
}

func (h *Handler) handleUpdateEntrance(
	w http.ResponseWriter,
	r *http.Request,
	route parsedRoute,
	query map[string]string,
	apply func(*resource.Objects, lkal.Record) (lkal.Record, error),
) {
	if err := rejectUnknownParams(query); err != nil {
		h.writeError(w, err)
		return
	}
	body, werr := h.readBody(w, r, MediaTypeResource)
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	cand, err := resource.Decode(body)
	if err != nil {
		h.writeError(w, decodeJSONError(err))
		return
	}
	obj, err := envelope.Object(cand)
	if err != nil {
		h.writeError(w, mapBackendError(err))
		return
	}
	if obj != route.object() {
		h.writeError(w, &Error{
			Kind:    KindForbidden,
			Status:  http.StatusForbidden,
			Path:    "meta",
			Message: "candidate identity must match request path",
		})
		return
	}
	if !h.hasKind(route.group, route.res) {
		h.writeError(w, &Error{
			Kind:    KindNotFound,
			Status:  http.StatusNotFound,
			Path:    "meta/kind",
			Message: "unknown resource kind",
		})
		return
	}
	objects, werr := h.openWriteObjects(r.Context(), route.tenant)
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	updated, err := apply(objects, cand)
	if err != nil {
		h.writeError(w, mapBackendError(err))
		return
	}
	encoded, err := resource.Encode(updated)
	if err != nil {
		h.writeError(w, internalWireError())
		return
	}
	h.writeJSON(w, http.StatusOK, encoded)
}

func (h *Handler) handlePatch(w http.ResponseWriter, r *http.Request, route parsedRoute, query map[string]string, principal refs.Principal) {
	h.handlePatchEntrance(w, r, route, query, func(objects *resource.Objects, ops []patch.Op) (lkal.Record, error) {
		return objects.Patch(r.Context(), principal, route.object(), ops)
	})
}

func (h *Handler) handlePatchStatus(w http.ResponseWriter, r *http.Request, route parsedRoute, query map[string]string) {
	h.handlePatchEntrance(w, r, route, query, func(objects *resource.Objects, ops []patch.Op) (lkal.Record, error) {
		return objects.PatchStatus(r.Context(), route.object(), ops)
	})
}

func (h *Handler) handlePatchEntrance(
	w http.ResponseWriter,
	r *http.Request,
	route parsedRoute,
	query map[string]string,
	apply func(*resource.Objects, []patch.Op) (lkal.Record, error),
) {
	if err := rejectUnknownParams(query); err != nil {
		h.writeError(w, err)
		return
	}
	body, werr := h.readBody(w, r, MediaTypePatch)
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	f, err := lkal.FromJSON(body)
	if err != nil {
		h.writeError(w, decodeJSONError(err))
		return
	}
	if err := patch.Schema().Validate(f); err != nil {
		h.writeError(w, patchSchemaError(err))
		return
	}
	ops, err := patch.FromRecord(f.Data())
	if err != nil {
		h.writeError(w, mapBackendError(err))
		return
	}
	if !h.hasKind(route.group, route.res) {
		h.writeError(w, &Error{
			Kind:    KindNotFound,
			Status:  http.StatusNotFound,
			Path:    "meta/kind",
			Message: "unknown resource kind",
		})
		return
	}
	objects, werr := h.openWriteObjects(r.Context(), route.tenant)
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	updated, err := apply(objects, ops)
	if err != nil {
		h.writeError(w, mapBackendError(err))
		return
	}
	encoded, err := resource.Encode(updated)
	if err != nil {
		h.writeError(w, internalWireError())
		return
	}
	h.writeJSON(w, http.StatusOK, encoded)
}

func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request, route parsedRoute, query map[string]string) {
	if err := h.requireEmptyBody(r); err != nil {
		h.writeError(w, err)
		return
	}
	if err := rejectUnknownParams(query, "rev"); err != nil {
		h.writeError(w, err)
		return
	}
	revVal, hasRev, werr := parseNonNegInt64Param(query, "rev")
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	if hasRev && revVal == 0 {
		h.writeError(w, &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    "rev",
			Message: `query parameter "rev" must be >= 1`,
		})
		return
	}
	if !h.hasKind(route.group, route.res) {
		h.writeError(w, &Error{
			Kind:    KindNotFound,
			Status:  http.StatusNotFound,
			Path:    "meta/kind",
			Message: "unknown resource kind",
		})
		return
	}
	objects, werr := h.openWriteObjects(r.Context(), route.tenant)
	if werr != nil {
		h.writeError(w, werr)
		return
	}
	if err := objects.Delete(r.Context(), route.object(), revVal); err != nil {
		h.writeError(w, mapBackendError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func setSecurityHeaders(hdr http.Header) {
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Frame-Options", "DENY")
	hdr.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
}

func (h *Handler) writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", MediaTypeResource)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (h *Handler) writeError(w http.ResponseWriter, e *Error) {
	safe := sanitizeWireError(e)
	encoded, err := EncodeError(safe)
	if err != nil {
		safe = internalWireError()
		encoded, _ = EncodeError(safe)
	}
	w.Header().Set("Content-Type", MediaTypeResource)
	w.WriteHeader(safe.Status)
	_, _ = w.Write(encoded)
}
