package wire

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/orderedset"
	"github.com/Kaikei-e/Nayutar/core/resource/ownership"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache"
)

const internalErrorMessage = "internal server error"

// ErrorKind identifies the structured wire error category.
type ErrorKind string

const (
	KindBadRequest           ErrorKind = "badRequest"
	KindUnauthorized         ErrorKind = "unauthorized"
	KindForbidden            ErrorKind = "forbidden"
	KindNotFound             ErrorKind = "notFound"
	KindMethodNotAllowed     ErrorKind = "methodNotAllowed"
	KindNotAcceptable        ErrorKind = "notAcceptable"
	KindConflict             ErrorKind = "conflict"
	KindGone                 ErrorKind = "gone"
	KindTooLarge             ErrorKind = "tooLarge"
	KindUnsupportedMediaType ErrorKind = "unsupportedMediaType"
	KindUnprocessable        ErrorKind = "unprocessable"
	KindObserved             ErrorKind = "observed"
	KindRefKind              ErrorKind = "refKind"
	KindRefNotFound          ErrorKind = "refNotFound"
	KindPathNotFound         ErrorKind = "pathNotFound"
	KindInternal             ErrorKind = "internal"
	KindUnavailable          ErrorKind = "unavailable"
)

var (
	ErrBadRequest           = errors.New("wire: badRequest")
	ErrUnauthorized         = errors.New("wire: unauthorized")
	ErrForbidden            = errors.New("wire: forbidden")
	ErrNotFound             = errors.New("wire: notFound")
	ErrMethodNotAllowed     = errors.New("wire: methodNotAllowed")
	ErrNotAcceptable        = errors.New("wire: notAcceptable")
	ErrConflict             = errors.New("wire: conflict")
	ErrGone                 = errors.New("wire: gone")
	ErrTooLarge             = errors.New("wire: tooLarge")
	ErrUnsupportedMediaType = errors.New("wire: unsupportedMediaType")
	ErrUnprocessable        = errors.New("wire: unprocessable")
	ErrObserved             = errors.New("wire: observed")
	ErrRefKind              = errors.New("wire: refKind")
	ErrRefNotFound          = errors.New("wire: refNotFound")
	ErrPathNotFound         = errors.New("wire: pathNotFound")
	ErrInternal             = errors.New("wire: internal")
	ErrUnavailable          = errors.New("wire: unavailable")
)

func (k ErrorKind) valid() bool {
	return k.sentinel() != nil
}

func (k ErrorKind) sentinel() error {
	switch k {
	case KindBadRequest:
		return ErrBadRequest
	case KindUnauthorized:
		return ErrUnauthorized
	case KindForbidden:
		return ErrForbidden
	case KindNotFound:
		return ErrNotFound
	case KindMethodNotAllowed:
		return ErrMethodNotAllowed
	case KindNotAcceptable:
		return ErrNotAcceptable
	case KindConflict:
		return ErrConflict
	case KindGone:
		return ErrGone
	case KindTooLarge:
		return ErrTooLarge
	case KindUnsupportedMediaType:
		return ErrUnsupportedMediaType
	case KindUnprocessable:
		return ErrUnprocessable
	case KindObserved:
		return ErrObserved
	case KindRefKind:
		return ErrRefKind
	case KindRefNotFound:
		return ErrRefNotFound
	case KindPathNotFound:
		return ErrPathNotFound
	case KindInternal:
		return ErrInternal
	case KindUnavailable:
		return ErrUnavailable
	default:
		return nil
	}
}

func kindForStatus(status int) ErrorKind {
	switch status {
	case http.StatusBadRequest:
		return KindBadRequest
	case http.StatusUnauthorized:
		return KindUnauthorized
	case http.StatusForbidden:
		return KindForbidden
	case http.StatusNotFound:
		return KindNotFound
	case http.StatusMethodNotAllowed:
		return KindMethodNotAllowed
	case http.StatusNotAcceptable:
		return KindNotAcceptable
	case http.StatusConflict:
		return KindConflict
	case http.StatusGone:
		return KindGone
	case http.StatusRequestEntityTooLarge:
		return KindTooLarge
	case http.StatusUnsupportedMediaType:
		return KindUnsupportedMediaType
	case http.StatusUnprocessableEntity:
		return KindUnprocessable
	case http.StatusServiceUnavailable:
		return KindUnavailable
	default:
		return KindInternal
	}
}

// Diagnostic is one structured kisage diagnostic entry carried inside Error.Diagnostics.
type Diagnostic struct {
	Code       ownership.Code
	Path       string
	Reason     string
	NextAction string
}

// Error is the structured wire error returned by HTTP error responses and terminal watch error events.
type Error struct {
	Kind        ErrorKind
	Status      int
	Code        ownership.Code
	Path        string
	Message     string
	Diagnostics []Diagnostic
}

// Equal reports whether e and other carry identical fields and diagnostics.
func (e *Error) Equal(other *Error) bool {
	if e == nil || other == nil {
		return e == other
	}
	return e.Kind == other.Kind &&
		e.Status == other.Status &&
		e.Code == other.Code &&
		e.Path == other.Path &&
		e.Message == other.Message &&
		slices.Equal(e.Diagnostics, other.Diagnostics)
}

// Error formats the wire error for diagnostics.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	msg := fmt.Sprintf("wire: %s (%d)", e.Kind, e.Status)
	if e.Code != "" {
		msg += " [" + string(e.Code) + "]"
	}
	if e.Path != "" {
		msg += ": " + e.Path
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	return msg
}

// Unwrap returns the sentinel error corresponding to e.Kind.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Kind.sentinel()
}

func errorToRecord(e *Error) (lkal.Record, error) {
	if e == nil {
		return lkal.Record{}, errors.New("wire: nil error")
	}
	if !e.Kind.valid() {
		return lkal.Record{}, fmt.Errorf("wire: invalid error kind %q", e.Kind)
	}
	if e.Status < 400 || e.Status > 599 {
		return lkal.Record{}, fmt.Errorf("wire: error status %d must be in 400..599", e.Status)
	}
	if e.Code != "" && !e.Code.IsValid() {
		return lkal.Record{}, fmt.Errorf("wire: invalid error code %q", e.Code)
	}
	if e.Message == "" {
		return lkal.Record{}, errors.New("wire: error message must not be empty")
	}
	fields := make([]lkal.Field, 0, 6)
	fields = append(fields,
		lkal.Field{Key: "kind", Value: lkal.Enum(e.Kind)},
		lkal.Field{Key: "status", Value: lkal.Int(e.Status)},
	)
	if e.Code != "" {
		fields = append(fields, lkal.Field{Key: "code", Value: lkal.String(e.Code)})
	}
	if e.Path != "" {
		fields = append(fields, lkal.Field{Key: "path", Value: lkal.String(e.Path)})
	}
	fields = append(fields, lkal.Field{Key: "message", Value: lkal.String(e.Message)})
	if len(e.Diagnostics) > 0 {
		items := make([]orderedset.Item, len(e.Diagnostics))
		for i, d := range e.Diagnostics {
			if !d.Code.IsValid() {
				return lkal.Record{}, fmt.Errorf("wire: invalid diagnostic code %q", d.Code)
			}
			if d.Reason == "" {
				return lkal.Record{}, errors.New("wire: diagnostic reason must not be empty")
			}
			df := make([]lkal.Field, 0, 4)
			df = append(df, lkal.Field{Key: "code", Value: lkal.String(d.Code)})
			if d.Path != "" {
				df = append(df, lkal.Field{Key: "path", Value: lkal.String(d.Path)})
			}
			df = append(df, lkal.Field{Key: "reason", Value: lkal.String(d.Reason)})
			if d.NextAction != "" {
				df = append(df, lkal.Field{Key: "nextAction", Value: lkal.String(d.NextAction)})
			}
			items[i] = orderedset.Item{
				Key:   strconv.Itoa(i),
				Value: lkal.Record{Fields: df},
			}
		}
		diagRec, err := orderedset.FromItems(items)
		if err != nil {
			return lkal.Record{}, fmt.Errorf("wire: encode diagnostics: %w", err)
		}
		fields = append(fields, lkal.Field{Key: "diagnostics", Value: diagRec})
	}
	return lkal.Record{Fields: fields}, nil
}

func errorFromRecord(rec lkal.Record) (*Error, error) {
	var out Error
	for _, fld := range rec.Fields {
		switch fld.Key {
		case "kind":
			e, ok := fld.Value.(lkal.Enum)
			if !ok {
				return nil, errors.New("wire: error.kind must be an enum")
			}
			out.Kind = ErrorKind(e)
		case "status":
			n, ok := fld.Value.(lkal.Int)
			if !ok {
				return nil, errors.New("wire: error.status must be an int")
			}
			out.Status = int(n)
		case "code":
			s, ok := fld.Value.(lkal.String)
			if !ok {
				return nil, errors.New("wire: error.code must be a string")
			}
			c := ownership.Code(s)
			if !c.IsValid() {
				return nil, fmt.Errorf("wire: unknown error.code %q", string(s))
			}
			out.Code = c
		case "path":
			s, ok := fld.Value.(lkal.String)
			if !ok {
				return nil, errors.New("wire: error.path must be a string")
			}
			out.Path = string(s)
		case "message":
			s, ok := fld.Value.(lkal.String)
			if !ok {
				return nil, errors.New("wire: error.message must be a string")
			}
			out.Message = string(s)
		case "diagnostics":
			dRec, ok := fld.Value.(lkal.Record)
			if !ok {
				return nil, errors.New("wire: error.diagnostics must be a record")
			}
			diags, err := diagnosticsFromRecord(dRec)
			if err != nil {
				return nil, err
			}
			out.Diagnostics = diags
		default:
			return nil, fmt.Errorf("wire: unexpected error field %q", fld.Key)
		}
	}
	if !out.Kind.valid() {
		return nil, fmt.Errorf("wire: unknown error kind %q", out.Kind)
	}
	if out.Status < 400 || out.Status > 599 {
		return nil, fmt.Errorf("wire: invalid error status %d", out.Status)
	}
	if out.Message == "" {
		return nil, errors.New("wire: missing error message")
	}
	return &out, nil
}

func diagnosticsFromRecord(rec lkal.Record) ([]Diagnostic, error) {
	entries, err := orderedset.Entries(rec)
	if err != nil {
		return nil, fmt.Errorf("wire: invalid diagnostics ordered set: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]Diagnostic, 0, len(entries))
	for _, entry := range entries {
		var d Diagnostic
		for _, f := range entry.Value.Fields {
			switch f.Key {
			case "code":
				s, ok := f.Value.(lkal.String)
				if !ok {
					return nil, errors.New("wire: diagnostic.code must be a string")
				}
				c := ownership.Code(s)
				if !c.IsValid() {
					return nil, fmt.Errorf("wire: invalid diagnostic.code %q", string(s))
				}
				d.Code = c
			case "path":
				s, ok := f.Value.(lkal.String)
				if !ok {
					return nil, errors.New("wire: diagnostic.path must be a string")
				}
				d.Path = string(s)
			case "reason":
				s, ok := f.Value.(lkal.String)
				if !ok {
					return nil, errors.New("wire: diagnostic.reason must be a string")
				}
				d.Reason = string(s)
			case "nextAction":
				s, ok := f.Value.(lkal.String)
				if !ok {
					return nil, errors.New("wire: diagnostic.nextAction must be a string")
				}
				d.NextAction = string(s)
			default:
				return nil, fmt.Errorf("wire: unexpected diagnostic field %q", f.Key)
			}
		}
		if !d.Code.IsValid() || d.Reason == "" {
			return nil, errors.New("wire: diagnostic requires valid code and reason")
		}
		out = append(out, d)
	}
	return out, nil
}

// EncodeError serializes e into compact single-line LKAL-JSON conforming to error.schema.lkal.
func EncodeError(e *Error) ([]byte, error) {
	rec, err := errorToRecord(e)
	if err != nil {
		return nil, err
	}
	return encodeTopFields([]lkal.Field{{Key: "error", Value: rec}}, errorSchema)
}

// DecodeError parses and validates an LKAL-JSON wire error payload.
func DecodeError(data []byte) (*Error, error) {
	f, err := lkal.FromJSON(data)
	if err != nil {
		return nil, err
	}
	if err := errorSchema.Validate(f); err != nil {
		return nil, err
	}
	rec, ok := findRecordField(f.Data(), "error")
	if !ok {
		return nil, errors.New("wire: missing error record")
	}
	return errorFromRecord(rec)
}

func internalWireError() *Error {
	return &Error{
		Kind:    KindInternal,
		Status:  http.StatusInternalServerError,
		Message: internalErrorMessage,
	}
}

func sanitizeWireError(e *Error) *Error {
	if e == nil || e.Status == http.StatusInternalServerError || e.Kind == KindInternal {
		return internalWireError()
	}
	cp := *e
	if cp.Status < 400 || cp.Status > 599 {
		return internalWireError()
	}
	if !cp.Kind.valid() {
		return internalWireError()
	}
	if cp.Code != "" && !cp.Code.IsValid() {
		cp.Code = ""
	}
	if !utf8.ValidString(cp.Path) {
		cp.Path = ""
	}
	if cp.Message == "" || !utf8.ValidString(cp.Message) {
		cp.Message = http.StatusText(cp.Status)
		if cp.Message == "" {
			cp.Message = string(cp.Kind)
		}
	}
	if len(cp.Diagnostics) > 0 {
		clean := make([]Diagnostic, 0, len(cp.Diagnostics))
		for _, d := range cp.Diagnostics {
			if !d.Code.IsValid() || d.Reason == "" || !utf8.ValidString(d.Reason) {
				continue
			}
			if !utf8.ValidString(d.Path) {
				d.Path = ""
			}
			if !utf8.ValidString(d.NextAction) {
				d.NextAction = ""
			}
			clean = append(clean, d)
		}
		cp.Diagnostics = clean
	}
	return &cp
}

func nonEmpty(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func toWireDiagnostics(in []ownership.Diagnostic) []Diagnostic {
	if len(in) == 0 {
		return nil
	}
	out := make([]Diagnostic, 0, len(in))
	for _, d := range in {
		if !d.Code.IsValid() || d.Reason == "" {
			continue
		}
		pathStr := ""
		if len(d.Path) > 0 {
			pathStr = d.Path.String()
		}
		out = append(out, Diagnostic{
			Code:       d.Code,
			Path:       pathStr,
			Reason:     d.Reason,
			NextAction: d.NextAction,
		})
	}
	return out
}

func mapBackendError(err error) *Error {
	if err == nil {
		return nil
	}
	var we *Error
	if errors.As(err, &we) {
		return sanitizeWireError(we)
	}
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return &Error{
			Kind:    KindTooLarge,
			Status:  http.StatusRequestEntityTooLarge,
			Message: "request body exceeds maximum allowed size",
		}
	}
	var oe *ownership.Error
	if errors.As(err, &oe) {
		diags := toWireDiagnostics(oe.Diagnostics)
		switch oe.Code {
		case ownership.CodeInvalidInput, ownership.CodeEmptyRecordNotSupported:
			return &Error{
				Kind:        KindBadRequest,
				Status:      http.StatusBadRequest,
				Code:        oe.Code,
				Path:        oe.Path,
				Message:     nonEmpty(oe.Detail, "invalid ownership input"),
				Diagnostics: diags,
			}
		case ownership.CodePolicyDenied:
			return &Error{
				Kind:        KindForbidden,
				Status:      http.StatusForbidden,
				Code:        oe.Code,
				Path:        oe.Path,
				Message:     nonEmpty(oe.Detail, "policy denied"),
				Diagnostics: diags,
			}
		case ownership.CodeOwnershipConflict,
			ownership.CodeStructureConflict,
			ownership.CodeExternalDrift,
			ownership.CodeMigrationRequired,
			ownership.CodeUnownedValueConflict:
			return &Error{
				Kind:        KindConflict,
				Status:      http.StatusConflict,
				Code:        oe.Code,
				Path:        oe.Path,
				Message:     nonEmpty(oe.Detail, "ownership conflict"),
				Diagnostics: diags,
			}
		case ownership.CodeIndeterminateOutcome:
			return &Error{
				Kind:    KindUnavailable,
				Status:  http.StatusServiceUnavailable,
				Code:    oe.Code,
				Message: "indeterminate transaction outcome",
				Diagnostics: []Diagnostic{{
					Code:       ownership.CodeIndeterminateOutcome,
					Reason:     "indeterminate transaction outcome",
					NextAction: "re-read object and ownership state via explain before retrying",
				}},
			}
		default:
			return internalWireError()
		}
	}
	var pe *patch.Error
	if errors.As(err, &pe) {
		switch pe.Kind {
		case patch.Malformed:
			return &Error{
				Kind:    KindUnprocessable,
				Status:  http.StatusUnprocessableEntity,
				Path:    pe.Path,
				Message: nonEmpty(pe.Detail, "malformed patch"),
			}
		case patch.Conflict:
			return &Error{
				Kind:    KindConflict,
				Status:  http.StatusConflict,
				Path:    pe.Path,
				Message: nonEmpty(pe.Detail, "patch conflict"),
			}
		case patch.NotFound:
			return &Error{
				Kind:    KindPathNotFound,
				Status:  http.StatusUnprocessableEntity,
				Path:    pe.Path,
				Message: nonEmpty(pe.Detail, "patch target path not found"),
			}
		case patch.Forbidden:
			return &Error{
				Kind:    KindForbidden,
				Status:  http.StatusForbidden,
				Path:    pe.Path,
				Message: nonEmpty(pe.Detail, "forbidden patch operation"),
			}
		case patch.Observed:
			return &Error{
				Kind:    KindObserved,
				Status:  http.StatusUnprocessableEntity,
				Path:    pe.Path,
				Message: nonEmpty(pe.Detail, "invalid observedGeneration"),
			}
		default:
			return internalWireError()
		}
	}
	var ee *envelope.Error
	if errors.As(err, &ee) {
		switch ee.Kind {
		case envelope.ErrMalformed:
			return &Error{
				Kind:    KindUnprocessable,
				Status:  http.StatusUnprocessableEntity,
				Path:    ee.Path,
				Message: nonEmpty(ee.Detail, "malformed resource envelope"),
			}
		case envelope.ErrConflict:
			return &Error{
				Kind:    KindConflict,
				Status:  http.StatusConflict,
				Path:    ee.Path,
				Message: nonEmpty(ee.Detail, "conflict"),
			}
		case envelope.ErrForbidden:
			return &Error{
				Kind:    KindForbidden,
				Status:  http.StatusForbidden,
				Path:    ee.Path,
				Message: nonEmpty(ee.Detail, "forbidden field modification"),
			}
		case envelope.ErrObserved:
			return &Error{
				Kind:    KindObserved,
				Status:  http.StatusUnprocessableEntity,
				Path:    ee.Path,
				Message: nonEmpty(ee.Detail, "invalid observedGeneration"),
			}
		default:
			return internalWireError()
		}
	}
	var re *refs.Error
	if errors.As(err, &re) {
		switch re.Kind {
		case refs.ErrMalformed:
			return &Error{
				Kind:    KindUnprocessable,
				Status:  http.StatusUnprocessableEntity,
				Path:    re.Occurrence.Path,
				Message: nonEmpty(re.Detail, "malformed reference"),
			}
		case refs.ErrKind:
			return &Error{
				Kind:    KindRefKind,
				Status:  http.StatusUnprocessableEntity,
				Path:    re.Occurrence.Path,
				Message: nonEmpty(re.Detail, "reference target kind not allowed"),
			}
		case refs.ErrDenied:
			return &Error{
				Kind:    KindForbidden,
				Status:  http.StatusForbidden,
				Path:    re.Occurrence.Path,
				Message: nonEmpty(re.Detail, "reference authorization denied"),
			}
		case refs.ErrNotFound:
			return &Error{
				Kind:    KindRefNotFound,
				Status:  http.StatusUnprocessableEntity,
				Path:    re.Occurrence.Path,
				Message: nonEmpty(re.Detail, "referenced target object not found"),
			}
		default:
			return internalWireError()
		}
	}
	switch {
	case errors.Is(err, resource.ErrNotFound), errors.Is(err, store.ErrNotFound):
		return &Error{
			Kind:    KindNotFound,
			Status:  http.StatusNotFound,
			Message: "object not found",
		}
	case errors.Is(err, resource.ErrExists):
		return &Error{
			Kind:    KindConflict,
			Status:  http.StatusConflict,
			Message: "object already exists",
		}
	case errors.Is(err, resource.ErrCorrupt), errors.Is(err, tenant.ErrCorruptRecord):
		return internalWireError()
	case errors.Is(err, tenant.ErrNotRegistered):
		return &Error{
			Kind:    KindNotFound,
			Status:  http.StatusNotFound,
			Message: "tenant not found",
		}
	case errors.Is(err, tenant.ErrTerminating):
		return &Error{
			Kind:    KindConflict,
			Status:  http.StatusConflict,
			Message: "tenant is terminating",
		}
	case errors.Is(err, tenant.ErrFenced):
		return &Error{
			Kind:    KindConflict,
			Status:  http.StatusConflict,
			Message: "tenant registration changed",
		}
	case errors.Is(err, tenant.ErrInvalidName):
		return &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Path:    "tenant",
			Message: "invalid tenant name",
		}
	case errors.Is(err, watchcache.ErrTooOld), errors.Is(err, store.ErrCompacted):
		return &Error{
			Kind:    KindGone,
			Status:  http.StatusGone,
			Message: "requested revision has been compacted or evicted",
		}
	case errors.Is(err, store.ErrFutureRev):
		return &Error{
			Kind:    KindUnprocessable,
			Status:  http.StatusUnprocessableEntity,
			Message: "requested revision is in the future",
		}
	case errors.Is(err, store.ErrTooLarge):
		return &Error{
			Kind:    KindTooLarge,
			Status:  http.StatusRequestEntityTooLarge,
			Message: "request exceeds store limits",
		}
	case errors.Is(err, store.ErrInvalidArgument):
		return &Error{
			Kind:    KindBadRequest,
			Status:  http.StatusBadRequest,
			Message: "invalid argument",
		}
	case errors.Is(err, watchcache.ErrNotReady):
		return &Error{
			Kind:    KindUnavailable,
			Status:  http.StatusServiceUnavailable,
			Message: "watch cache is not ready",
		}
	case errors.Is(err, watchcache.ErrClosed):
		return &Error{
			Kind:    KindUnavailable,
			Status:  http.StatusServiceUnavailable,
			Message: "service is shutting down",
		}
	default:
		return internalWireError()
	}
}
