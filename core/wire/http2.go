package wire

import (
	"crypto/tls"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Kaikei-e/Nayutar/core/sim"
)

const defaultReadHeaderTimeout = 10 * time.Second

// ConfigureServer configures srv to serve HTTP/2 over TLS and, when allowH2C is true, prior-knowledge unencrypted HTTP/2.
func ConfigureServer(srv *http.Server, allowH2C bool) {
	if srv == nil {
		return
	}
	var protocols http.Protocols
	protocols.SetHTTP2(true)
	if allowH2C {
		protocols.SetUnencryptedHTTP2(true)
	}
	srv.Protocols = &protocols
	if srv.HTTP2 == nil {
		srv.HTTP2 = &http.HTTP2Config{}
	}
	if srv.ReadHeaderTimeout == 0 {
		srv.ReadHeaderTimeout = defaultReadHeaderTimeout
	}
	if srv.TLSConfig != nil {
		if srv.TLSConfig.MinVersion == 0 {
			srv.TLSConfig.MinVersion = tls.VersionTLS13
		}
		if !slices.Contains(srv.TLSConfig.NextProtos, "h2") {
			srv.TLSConfig.NextProtos = append([]string{"h2"}, srv.TLSConfig.NextProtos...)
		}
	}
}

// NewH2CClient returns a Client configured for prior-knowledge unencrypted HTTP/2 (h2c) against baseURL.
func NewH2CClient(baseURL string) *Client {
	return NewH2CClientWithNet(baseURL, nil)
}

// NewH2CClientWithNet returns an h2c Client that dials via netImpl when non-nil.
func NewH2CClientWithNet(baseURL string, netImpl sim.Net) *Client {
	var protocols http.Protocols
	// Go 1.27 requires HTTP1 disabled on Transport.Protocols for prior-knowledge h2c over http:// URLs.
	protocols.SetUnencryptedHTTP2(true)
	tr := &http.Transport{
		Protocols: &protocols,
		HTTP2:     &http.HTTP2Config{},
	}
	if netImpl != nil {
		tr.DialContext = netImpl.DialContext
	}
	return NewClient(baseURL, &http.Client{Transport: tr})
}

// NewTLSClient returns a Client configured for TLS HTTP/2 (h2) against baseURL.
func NewTLSClient(baseURL string, tlsCfg *tls.Config) *Client {
	return NewTLSClientWithNet(baseURL, tlsCfg, nil)
}

// NewTLSClientWithNet returns a TLS HTTP/2 Client that dials via netImpl when non-nil.
func NewTLSClientWithNet(baseURL string, tlsCfg *tls.Config, netImpl sim.Net) *Client {
	var protocols http.Protocols
	protocols.SetHTTP2(true)
	var cloned *tls.Config
	if tlsCfg != nil {
		cloned = tlsCfg.Clone()
	} else {
		cloned = &tls.Config{}
	}
	if cloned.MinVersion == 0 {
		cloned.MinVersion = tls.VersionTLS13
	}
	tr := &http.Transport{
		TLSClientConfig: cloned,
		Protocols:       &protocols,
		HTTP2:           &http.HTTP2Config{},
	}
	if netImpl != nil {
		tr.DialContext = netImpl.DialContext
	}
	return NewClient(baseURL, &http.Client{Transport: tr})
}

func checkContentType(values []string, want string) bool {
	if len(values) != 1 {
		return false
	}
	mt, params, err := mime.ParseMediaType(values[0])
	if err != nil || mt != want {
		return false
	}
	for k, v := range params {
		if k != "charset" || !strings.EqualFold(v, "utf-8") {
			return false
		}
	}
	return true
}

// acceptsMediaType reports whether HTTP Accept header values permit want (RFC 9110 §12.5.1).
func acceptsMediaType(values []string, want string) bool {
	if len(values) == 0 {
		return true
	}
	wantType, wantSub, ok := strings.Cut(want, "/")
	if !ok {
		return false
	}

	hasNonEmpty := false
	bestSpec := 0
	bestQ := 0.0

	for _, raw := range values {
		for _, item := range splitHeaderList(raw) {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			hasNonEmpty = true
			spec, q, matched := matchAcceptRange(item, wantType, wantSub)
			if !matched {
				continue
			}
			if spec > bestSpec || (spec == bestSpec && q > bestQ) {
				bestSpec = spec
				bestQ = q
			}
		}
	}
	if !hasNonEmpty {
		return true
	}
	return bestSpec > 0 && bestQ > 0
}

func splitHeaderList(s string) []string {
	var out []string
	start := 0
	inQuotes := false
	escaped := false
	for i := range len(s) {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if inQuotes {
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inQuotes = false
			}
			continue
		}
		switch ch {
		case '"':
			inQuotes = true
		case ',':
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

func matchAcceptRange(item, wantType, wantSub string) (specificity int, q float64, ok bool) {
	mt, params, err := mime.ParseMediaType(item)
	if err != nil {
		return 0, 0, false
	}
	q = 1.0
	for k, v := range params {
		switch k {
		case "q":
			parsed, valid := parseQValue(v)
			if !valid {
				return 0, 0, false
			}
			q = parsed
		case "charset":
			if !strings.EqualFold(v, "utf-8") {
				return 0, 0, false
			}
		default:
			return 0, 0, false
		}
	}
	rType, rSub, hasSlash := strings.Cut(mt, "/")
	if !hasSlash || rType == "" || rSub == "" {
		return 0, 0, false
	}
	switch {
	case rType == wantType && rSub == wantSub:
		return 3, q, true
	case rType == wantType && rSub == "*":
		return 2, q, true
	case rType == "*" && rSub == "*":
		return 1, q, true
	default:
		return 0, 0, false
	}
}

// parseQValue validates RFC 9110 §12.4.2 qvalue = ("0" [ "." 0*3DIGIT ]) / ("1" [ "." 0*3("0") ]).
func parseQValue(s string) (float64, bool) {
	if len(s) == 0 || len(s) > 5 {
		return 0, false
	}
	switch s[0] {
	case '0':
		if len(s) == 1 {
			return 0, true
		}
		if s[1] != '.' {
			return 0, false
		}
		for i := 2; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return 0, false
			}
		}
	case '1':
		if len(s) == 1 {
			return 1, true
		}
		if s[1] != '.' {
			return 0, false
		}
		for i := 2; i < len(s); i++ {
			if s[i] != '0' {
				return 0, false
			}
		}
	default:
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v > 1 {
		return 0, false
	}
	return v, true
}
