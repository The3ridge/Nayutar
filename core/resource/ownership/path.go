package ownership

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxPathDepth is the maximum nesting depth for LKAL records.
const MaxPathDepth = 1000

// Path is a parsed sequence of record field segments within EntranceSpec.
type Path []string

// ValidSeg reports whether s is a valid path segment (non-empty, valid UTF-8, no control chars, no leading '$').
func ValidSeg(s string) bool {
	if s == "" || s[0] == '$' || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ValidatePath validates a non-empty parsed path sequence.
func ValidatePath(p Path) error {
	if len(p) == 0 {
		return newError(CodeInvalidInput, nil, "path must not be empty", "")
	}
	if len(p)-1 > MaxPathDepth {
		return newError(CodeInvalidInput, p, "path exceeds maximum nesting depth", "")
	}
	for _, seg := range p {
		if !ValidSeg(seg) {
			return newError(CodeInvalidInput, p, fmt.Sprintf("invalid path segment %q", seg), "")
		}
	}
	return nil
}

// IsEntranceSpecPath reports whether p is a valid leaf path under meta/labels/<key> or spec/**.
func IsEntranceSpecPath(p Path) bool {
	if ValidatePath(p) != nil {
		return false
	}
	if p[0] == "spec" {
		return len(p) >= 2
	}
	if p[0] == "meta" {
		return len(p) == 3 && p[1] == "labels"
	}
	return false
}

// ValidateEntranceSpecPath validates that p is a legal EntranceSpec leaf path.
func ValidateEntranceSpecPath(p Path) error {
	if err := ValidatePath(p); err != nil {
		return err
	}
	if !IsEntranceSpecPath(p) {
		return newError(CodeInvalidInput, p, fmt.Sprintf("path %s is outside EntranceSpec (meta/labels/* and spec/**)", p.String()), "")
	}
	return nil
}

// ParsePath parses an LKAL segment path (with optional single leading '/').
// Bare segments match [A-Za-z0-9_.-]+; other segments (including label keys containing '/') must be double-quoted.
func ParsePath(s string) (Path, error) {
	if s == "" || s == "/" {
		return nil, newError(CodeInvalidInput, nil, "path must not be empty", "")
	}
	body := strings.TrimPrefix(s, "/")
	segs, ok := parseSegments(body)
	if !ok || len(segs) == 0 {
		return nil, newError(CodeInvalidInput, nil, fmt.Sprintf("malformed path %q", s), "")
	}
	p := Path(segs)
	if err := ValidatePath(p); err != nil {
		return nil, err
	}
	return p, nil
}

// MustParsePath parses s with ParsePath and panics on error.
func MustParsePath(s string) Path {
	p, err := ParsePath(s)
	if err != nil {
		panic(err)
	}
	return p
}

func (p Path) Clone() Path {
	if p == nil {
		return nil
	}
	return slices.Clone(p)
}

func (p Path) Append(seg string) Path {
	out := make(Path, len(p)+1)
	copy(out, p)
	out[len(p)] = seg
	return out
}

func (p Path) Equal(other Path) bool {
	return slices.Equal(p, other)
}

func (p Path) Compare(other Path) int {
	return slices.Compare(p, other)
}

// IsProperAncestorOf reports whether p is a strict prefix of other.
func (p Path) IsProperAncestorOf(other Path) bool {
	return len(p) > 0 && len(p) < len(other) && slices.Equal(p, other[:len(p)])
}

// IsProperDescendantOf reports whether other is a strict prefix of p.
func (p Path) IsProperDescendantOf(other Path) bool {
	return other.IsProperAncestorOf(p)
}

// Overlaps reports whether p and other are equal or one is a proper ancestor of the other.
func (p Path) Overlaps(other Path) bool {
	return p.Equal(other) || p.IsProperAncestorOf(other) || other.IsProperAncestorOf(p)
}

// Ancestors returns all proper non-root ancestors of p from shortest to parent.
func (p Path) Ancestors() []Path {
	if len(p) <= 1 {
		return nil
	}
	out := make([]Path, len(p)-1)
	for i := 1; i < len(p); i++ {
		out[i-1] = slices.Clone(p[:i])
	}
	return out
}

// String formats p using canonical LKAL path segments without a leading '/'.
func (p Path) String() string {
	if len(p) == 0 {
		return ""
	}
	var b strings.Builder
	for i, seg := range p {
		if i > 0 {
			b.WriteByte('/')
		}
		if isBareSeg(seg) {
			b.WriteString(seg)
		} else {
			b.WriteString(quoteSeg(seg))
		}
	}
	return b.String()
}

func isSegChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') ||
		(b >= 'a' && b <= 'z') ||
		(b >= '0' && b <= '9') ||
		b == '_' || b == '.' || b == '-'
}

func isBareSeg(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if !isSegChar(s[i]) {
			return false
		}
	}
	return true
}

func isHexByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func parseSegments(src string) ([]string, bool) {
	if src == "" {
		return nil, false
	}
	var segs []string
	i := 0
	for {
		if i >= len(src) {
			return nil, false
		}
		var (
			seg  string
			next int
			ok   bool
		)
		if src[i] == '"' {
			seg, next, ok = scanQuotedSeg(src, i)
		} else if isSegChar(src[i]) {
			next = i
			for next < len(src) && isSegChar(src[next]) {
				next++
			}
			seg = src[i:next]
			ok = next > i
		}
		if !ok {
			return nil, false
		}
		segs = append(segs, seg)
		i = next
		if i == len(src) {
			return segs, true
		}
		if src[i] != '/' {
			return nil, false
		}
		i++
	}
}

func scanQuotedSeg(src string, off int) (string, int, bool) {
	var b strings.Builder
	i := off + 1
	for i < len(src) {
		if src[i] == '"' {
			return b.String(), i + 1, true
		}
		if src[i] == '\\' {
			if i+1 >= len(src) {
				return "", i, false
			}
			switch src[i+1] {
			case '"':
				b.WriteByte('"')
				i += 2
			case '\\':
				b.WriteByte('\\')
				i += 2
			case 'n':
				b.WriteByte('\n')
				i += 2
			case 'r':
				b.WriteByte('\r')
				i += 2
			case 't':
				b.WriteByte('\t')
				i += 2
			case 'u':
				r, next, ok := scanUnicodeEscape(src, i+2)
				if !ok {
					return "", i, false
				}
				b.WriteRune(r)
				i = next
			default:
				return "", i, false
			}
			continue
		}
		r, sz := utf8.DecodeRuneInString(src[i:])
		if (r == utf8.RuneError && sz == 1) || r < 0x20 || r == 0x7f {
			return "", i, false
		}
		b.WriteRune(r)
		i += sz
	}
	return "", i, false
}

func scanUnicodeEscape(src string, off int) (rune, int, bool) {
	if off >= len(src) || src[off] != '{' {
		return 0, off, false
	}
	i := off + 1
	start := i
	for i < len(src) && isHexByte(src[i]) {
		i++
	}
	if i >= len(src) || src[i] != '}' || i == start || i-start > 6 {
		return 0, i, false
	}
	val, err := strconv.ParseUint(src[start:i], 16, 32)
	if err != nil || val > 0x10FFFF || (val >= 0xD800 && val <= 0xDFFF) {
		return 0, i, false
	}
	return rune(val), i + 1, true
}

func quoteSeg(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
