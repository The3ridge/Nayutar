package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type comparatorConfig struct {
	ChallengeModule string   `json:"challenge_module"`
	SolutionModule  string   `json:"solution_module"`
	TheoremNames    []string `json:"theorem_names"`
}

// CheckProtectedTheorems reports whether every theorem listed in
// spec/comparator.json is declared in both the challenge module and the
// solution module. Declarations count only when an import from that module's
// root reaches them. A missing theorem is a distinct error so callers can
// reject it before lake comparator, which panics when the constant is absent.
func CheckProtectedTheorems(root string) error {
	data, err := os.ReadFile(filepath.Join(root, "spec", "comparator.json"))
	if err != nil {
		return fmt.Errorf("read spec/comparator.json: %w", err)
	}
	var cfg comparatorConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse spec/comparator.json: %w", err)
	}
	if cfg.ChallengeModule == "" || cfg.SolutionModule == "" {
		return errors.New("spec/comparator.json challenge_module and solution_module must be set")
	}
	if len(cfg.TheoremNames) == 0 {
		return errors.New("spec/comparator.json theorem_names is empty")
	}
	seen := make(map[string]bool, len(cfg.TheoremNames))
	var errs []error
	for _, name := range cfg.TheoremNames {
		if seen[name] {
			errs = append(errs, fmt.Errorf("duplicate protected theorem: %s", name))
		}
		seen[name] = true
	}
	challenge, err := theoremDeclsInModule(root, cfg.ChallengeModule)
	if err != nil {
		return err
	}
	solution, err := theoremDeclsInModule(root, cfg.SolutionModule)
	if err != nil {
		return err
	}
	reported := map[string]bool{}
	for _, name := range cfg.TheoremNames {
		if countName(name, cfg.TheoremNames) > 1 || reported[name] {
			continue
		}
		reported[name] = true
		errs = append(errs, theoremPresence(name, "challenge module "+cfg.ChallengeModule, challenge)...)
		errs = append(errs, theoremPresence(name, "solution module "+cfg.SolutionModule, solution)...)
	}
	return errors.Join(errs...)
}

func countName(name string, names []string) int {
	n := 0
	for _, other := range names {
		if other == name {
			n++
		}
	}
	return n
}

func theoremPresence(name, where string, decls []string) []error {
	switch n := countTheoremMatches(name, decls); n {
	case 0:
		return []error{fmt.Errorf("missing protected theorem: %s in %s", name, where)}
	case 1:
		return nil
	default:
		return []error{fmt.Errorf("protected theorem %s matched %d declarations in %s, want 1", name, n, where)}
	}
}

func theoremDeclsInModule(root, module string) ([]string, error) {
	var decls []string
	seen := map[string]bool{}
	var walk func(mod string) error
	walk = func(mod string) error {
		if seen[mod] {
			return nil
		}
		seen[mod] = true
		if mod != module && !strings.HasPrefix(mod, module+".") {
			return nil
		}
		path := filepath.Join(root, "spec", filepath.FromSlash(strings.ReplaceAll(mod, ".", "/"))+".lean")
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read module %s: %w", mod, err)
		}
		text := string(data)
		decls = append(decls, leanTheoremDecls(text)...)
		for _, imported := range leanImports(text) {
			if imported == module || strings.HasPrefix(imported, module+".") {
				if err := walk(imported); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(module); err != nil {
		return nil, err
	}
	return decls, nil
}

func countTheoremMatches(listed string, decls []string) int {
	matches := 0
	for _, decl := range decls {
		if decl == listed {
			matches++
		}
	}
	return matches
}

type leanTokenKind int

const (
	tokIdent leanTokenKind = iota
	tokPunct
	tokStr
)

type leanToken struct {
	kind    leanTokenKind
	text    string
	escaped bool
	line    int
	col     int
}

type leanScanner struct {
	src  string
	pos  int
	line int
	col  int
}

func (s *leanScanner) advanceTo(target int) {
	if target > len(s.src) {
		target = len(s.src)
	}
	for s.pos < target {
		r, w := utf8.DecodeRuneInString(s.src[s.pos:])
		if r == '\n' {
			s.line++
			s.col = 1
		} else {
			s.col++
		}
		s.pos += w
	}
}

func tokenizeLean(src string) []leanToken {
	s := leanScanner{src: src, line: 1, col: 1}
	var tokens []leanToken
	n := len(src)
	for s.pos < n {
		r, w := utf8.DecodeRuneInString(src[s.pos:])
		if unicode.IsSpace(r) {
			s.advanceTo(s.pos + w)
			continue
		}
		if s.pos+1 < n && src[s.pos] == '-' && src[s.pos+1] == '-' {
			s.advanceTo(scanLineComment(src, s.pos))
			continue
		}
		if s.pos+1 < n && src[s.pos] == '/' && src[s.pos+1] == '-' {
			s.advanceTo(scanBlockComment(src, s.pos))
			continue
		}
		if endIdx, ok := scanRawString(src, s.pos); ok {
			tokens = append(tokens, leanToken{kind: tokStr, text: src[s.pos:endIdx], line: s.line, col: s.col})
			s.advanceTo(endIdx)
			continue
		}
		if src[s.pos] == '"' {
			endIdx := scanStringLiteral(src, s.pos)
			tokens = append(tokens, leanToken{kind: tokStr, text: src[s.pos:endIdx], line: s.line, col: s.col})
			s.advanceTo(endIdx)
			continue
		}
		if src[s.pos] == '\'' {
			if endIdx, ok := scanCharLiteral(src, s.pos); ok {
				tokens = append(tokens, leanToken{kind: tokStr, text: src[s.pos:endIdx], line: s.line, col: s.col})
				s.advanceTo(endIdx)
				continue
			}
		}
		if s.pos+1 < n && src[s.pos] == '.' && src[s.pos+1] == '{' {
			tokens = append(tokens, leanToken{kind: tokPunct, text: ".{", line: s.line, col: s.col})
			s.advanceTo(s.pos + 2)
			continue
		}
		if s.pos+1 < n && src[s.pos] == ':' && src[s.pos+1] == '=' {
			tokens = append(tokens, leanToken{kind: tokPunct, text: ":=", line: s.line, col: s.col})
			s.advanceTo(s.pos + 2)
			continue
		}
		switch src[s.pos] {
		case '(', ')', '{', '}', '[', ']', ':', ',', ';', '@', '.':
			tokens = append(tokens, leanToken{kind: tokPunct, text: src[s.pos : s.pos+1], line: s.line, col: s.col})
			s.advanceTo(s.pos + 1)
			continue
		}
		if ident, escaped, nextPos, ok := scanIdent(src, s.pos); ok {
			if !escaped && strings.HasSuffix(ident, "!") && !strings.Contains(ident, ".") && nextPos < n && src[nextPos] == '"' {
				endIdx := scanInterpString(src, nextPos)
				tokens = append(tokens, leanToken{kind: tokStr, text: src[s.pos:endIdx], line: s.line, col: s.col})
				s.advanceTo(endIdx)
				continue
			}
			tokens = append(tokens, leanToken{kind: tokIdent, text: ident, escaped: escaped, line: s.line, col: s.col})
			s.advanceTo(nextPos)
			continue
		}
		tokens = append(tokens, leanToken{kind: tokPunct, text: src[s.pos : s.pos+w], line: s.line, col: s.col})
		s.advanceTo(s.pos + w)
	}
	return tokens
}

func scanLineComment(src string, i int) int {
	nl := strings.IndexByte(src[i+2:], '\n')
	if nl < 0 {
		return len(src)
	}
	return i + 2 + nl + 1
}

func scanBlockComment(src string, i int) int {
	i += 2
	depth := 1
	for i < len(src) && depth > 0 {
		if i+1 < len(src) && src[i] == '/' && src[i+1] == '-' {
			depth++
			i += 2
			continue
		}
		if i+1 < len(src) && src[i] == '-' && src[i+1] == '/' {
			depth--
			i += 2
			continue
		}
		_, w := utf8.DecodeRuneInString(src[i:])
		i += w
	}
	return i
}

func scanRawString(src string, i int) (int, bool) {
	if i+2 >= len(src) || src[i] != 'r' || src[i+1] != '#' {
		return 0, false
	}
	hashes := 0
	for i+1+hashes < len(src) && src[i+1+hashes] == '#' {
		hashes++
	}
	openQuote := i + 1 + hashes
	if openQuote >= len(src) || src[openQuote] != '"' {
		return 0, false
	}
	closeDelim := `"` + strings.Repeat("#", hashes)
	idx := strings.Index(src[openQuote+1:], closeDelim)
	if idx < 0 {
		return len(src), true
	}
	return openQuote + 1 + idx + len(closeDelim), true
}

func scanStringLiteral(src string, i int) int {
	i++
	for i < len(src) {
		if src[i] == '\\' {
			if i+1 < len(src) {
				_, w := utf8.DecodeRuneInString(src[i+1:])
				i += 1 + w
			} else {
				i++
			}
			continue
		}
		if src[i] == '"' {
			return i + 1
		}
		_, w := utf8.DecodeRuneInString(src[i:])
		i += w
	}
	return i
}

func scanInterpString(src string, i int) int {
	i++
	for i < len(src) {
		switch src[i] {
		case '\\':
			if i+1 < len(src) {
				_, w := utf8.DecodeRuneInString(src[i+1:])
				i += 1 + w
			} else {
				i++
			}
		case '"':
			return i + 1
		case '{':
			i = skipBalancedBraces(src, i+1)
		default:
			_, w := utf8.DecodeRuneInString(src[i:])
			i += w
		}
	}
	return i
}

func skipBalancedBraces(src string, i int) int {
	depth := 1
	for i < len(src) && depth > 0 {
		if i+1 < len(src) && src[i] == '-' && src[i+1] == '-' {
			i = scanLineComment(src, i)
			continue
		}
		if i+1 < len(src) && src[i] == '/' && src[i+1] == '-' {
			i = scanBlockComment(src, i)
			continue
		}
		if endIdx, ok := scanRawString(src, i); ok {
			i = endIdx
			continue
		}
		if src[i] == '\'' {
			if endIdx, ok := scanCharLiteral(src, i); ok {
				i = endIdx
				continue
			}
		}
		if src[i] == '"' {
			if i > 0 && src[i-1] == '!' {
				i = scanInterpString(src, i)
			} else {
				i = scanStringLiteral(src, i)
			}
			continue
		}
		switch src[i] {
		case '{':
			depth++
			i++
		case '}':
			depth--
			i++
		default:
			_, w := utf8.DecodeRuneInString(src[i:])
			i += w
		}
	}
	return i
}

func scanCharLiteral(src string, i int) (int, bool) {
	if i+2 >= len(src) || src[i] != '\'' {
		return 0, false
	}
	j := i + 1
	if src[j] == '\\' {
		j++
		if j >= len(src) {
			return 0, false
		}
		switch src[j] {
		case 'x':
			if j+3 >= len(src) || !isHexDigit(src[j+1]) || !isHexDigit(src[j+2]) {
				return 0, false
			}
			j += 3
		case 'u':
			if j+5 >= len(src) || !isHexDigit(src[j+1]) || !isHexDigit(src[j+2]) || !isHexDigit(src[j+3]) || !isHexDigit(src[j+4]) {
				return 0, false
			}
			j += 5
		default:
			_, w := utf8.DecodeRuneInString(src[j:])
			j += w
		}
	} else {
		r, w := utf8.DecodeRuneInString(src[j:])
		if r == '\'' || r == '\n' || r == '\r' {
			return 0, false
		}
		j += w
	}
	if j < len(src) && src[j] == '\'' {
		return j + 1, true
	}
	return 0, false
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func scanIdent(src string, i int) (string, bool, int, bool) {
	seg, esc, pos, ok := scanIdentSegment(src, i)
	if !ok {
		return "", false, 0, false
	}
	parts := []string{seg}
	escaped := esc
	for pos < len(src) && src[pos] == '.' {
		if pos+1 < len(src) && src[pos+1] == '{' {
			break
		}
		nextSeg, nextEsc, nextPos, nextOK := scanIdentSegment(src, pos+1)
		if !nextOK {
			break
		}
		parts = append(parts, nextSeg)
		escaped = escaped || nextEsc
		pos = nextPos
	}
	return strings.Join(parts, "."), escaped, pos, true
}

func scanIdentSegment(src string, i int) (string, bool, int, bool) {
	const openGuillemet = "«"
	const closeGuillemet = "»"
	if strings.HasPrefix(src[i:], openGuillemet) {
		start := i + len(openGuillemet)
		end := strings.Index(src[start:], closeGuillemet)
		if end <= 0 {
			return "", false, 0, false
		}
		return src[start : start+end], true, start + end + len(closeGuillemet), true
	}
	r, w := utf8.DecodeRuneInString(src[i:])
	if !isLeanIdentStart(r) {
		return "", false, 0, false
	}
	pos := i + w
	for pos < len(src) {
		rc, wc := utf8.DecodeRuneInString(src[pos:])
		if !isLeanIdentContinue(rc) {
			break
		}
		pos += wc
	}
	return src[i:pos], false, pos, true
}

func isLeanIdentStart(r rune) bool {
	if r < utf8.RuneSelf {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
	}
	return unicode.IsLetter(r)
}

func isLeanIdentContinue(r rune) bool {
	if r < utf8.RuneSelf {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '_' || r == '\'' || r == '?' || r == '!'
	}
	return unicode.IsLetter(r) || unicode.IsNumber(r)
}

func isUnescapedIdent(tok leanToken, want string) bool {
	return tok.kind == tokIdent && !tok.escaped && tok.text == want
}

func isLeanCommandKeyword(s string) bool {
	switch s {
	case "prelude", "import", "public", "meta",
		"namespace", "section", "mutual", "end",
		"open", "export", "variable", "universe", "universes",
		"set_option", "attribute", "include", "omit",
		"private", "protected", "noncomputable", "unsafe", "partial", "scoped", "local",
		"theorem", "axiom", "constant", "opaque", "def", "abbrev",
		"instance", "example", "structure", "class", "inductive", "coinductive",
		"deriving", "where", "syntax", "macro", "macro_rules", "elab", "elab_rules",
		"notation", "prefix", "infix", "infixl", "infixr", "postfix",
		"initialize", "builtin_initialize":
		return true
	default:
		return false
	}
}

func isScopeIdent(tok leanToken) bool {
	return tok.kind == tokIdent && (tok.escaped || !isLeanCommandKeyword(tok.text))
}

func leanImports(src string) []string {
	tokens := tokenizeLean(src)
	var imports []string
	for i := 0; i < len(tokens); {
		if isUnescapedIdent(tokens[i], "prelude") {
			i++
			continue
		}
		j := i
		if j < len(tokens) && isUnescapedIdent(tokens[j], "public") {
			j++
		}
		if j < len(tokens) && isUnescapedIdent(tokens[j], "meta") {
			j++
		}
		if j < len(tokens) && isUnescapedIdent(tokens[j], "import") {
			j++
			if j < len(tokens) && isUnescapedIdent(tokens[j], "all") && j+1 < len(tokens) && isScopeIdent(tokens[j+1]) {
				j++
			}
			for j < len(tokens) && isScopeIdent(tokens[j]) {
				imports = append(imports, tokens[j].text)
				j++
			}
			i = j
			continue
		}
		break
	}
	return imports
}

type leanScope struct {
	header      string
	isNamespace bool
}

type leanTheoremDecl struct {
	name      string
	usesSorry bool
}

func leanTheoremDecls(src string) []string {
	parsed := scanLeanTheorems(src)
	decls := make([]string, len(parsed))
	for i, thm := range parsed {
		decls[i] = thm.name
	}
	return decls
}

func scanLeanTheorems(src string) []leanTheoremDecl {
	tokens := tokenizeLean(src)
	var scopes []leanScope
	var out []leanTheoremDecl
	activeThm := -1
	hasPrivate := false

	for i := 0; i < len(tokens); {
		prevDot := i > 0 && tokens[i-1].kind == tokPunct && tokens[i-1].text == "."
		if !prevDot && tokens[i].kind == tokPunct && tokens[i].text == "@" &&
			i+1 < len(tokens) && tokens[i+1].kind == tokPunct && tokens[i+1].text == "[" {
			activeThm = -1
			i = skipBracketGroup(tokens, i+1)
			continue
		}
		if !prevDot && tokens[i].kind == tokIdent && !tokens[i].escaped {
			switch tokens[i].text {
			case "private":
				activeThm = -1
				hasPrivate = true
				i++
				continue
			case "protected", "noncomputable", "public", "unsafe", "partial":
				activeThm = -1
				i++
				continue
			case "namespace":
				activeThm = -1
				hasPrivate = false
				i++
				if i < len(tokens) && isScopeIdent(tokens[i]) {
					for _, seg := range strings.Split(tokens[i].text, ".") {
						scopes = append(scopes, leanScope{header: seg, isNamespace: true})
					}
					i++
				}
				continue
			case "section":
				activeThm = -1
				hasPrivate = false
				secLine := tokens[i].line
				i++
				if i < len(tokens) && isScopeIdent(tokens[i]) && tokens[i].line == secLine {
					for _, seg := range strings.Split(tokens[i].text, ".") {
						scopes = append(scopes, leanScope{header: seg, isNamespace: false})
					}
					i++
				} else {
					scopes = append(scopes, leanScope{header: "", isNamespace: false})
				}
				continue
			case "mutual":
				activeThm = -1
				hasPrivate = false
				scopes = append(scopes, leanScope{header: "", isNamespace: false})
				i++
				continue
			case "end":
				activeThm = -1
				hasPrivate = false
				endLine := tokens[i].line
				i++
				if i < len(tokens) && isScopeIdent(tokens[i]) && tokens[i].line == endLine {
					parts := strings.Split(tokens[i].text, ".")
					for k := len(parts) - 1; k >= 0; k-- {
						if n := len(scopes); n > 0 && scopes[n-1].header == parts[k] {
							scopes = scopes[:n-1]
						}
					}
					i++
				} else if len(scopes) > 0 {
					scopes = scopes[:len(scopes)-1]
				}
				continue
			case "theorem":
				activeThm = -1
				priv := hasPrivate
				hasPrivate = false
				i++
				if i < len(tokens) && tokens[i].kind == tokIdent {
					if !priv {
						full := resolveLeanDecl(activeNamespace(scopes), tokens[i].text)
						out = append(out, leanTheoremDecl{name: full})
						activeThm = len(out) - 1
					}
					i++
				}
				continue
			case "sorry", "sorryAx":
				hasPrivate = false
				if activeThm >= 0 {
					out[activeThm].usesSorry = true
				}
				i++
				continue
			case "def", "abbrev", "opaque", "axiom", "constant",
				"instance", "example", "structure", "class", "inductive", "coinductive":
				activeThm = -1
				hasPrivate = false
				i++
				continue
			}
		}
		hasPrivate = false
		i++
	}
	return out
}

func skipBracketGroup(tokens []leanToken, i int) int {
	depth := 0
	for i < len(tokens) {
		if tokens[i].kind == tokPunct {
			switch tokens[i].text {
			case "[":
				depth++
			case "]":
				depth--
				if depth <= 0 {
					return i + 1
				}
			}
		}
		i++
	}
	return i
}

func activeNamespace(scopes []leanScope) string {
	var parts []string
	for _, s := range scopes {
		if s.isNamespace && s.header != "" {
			parts = append(parts, s.header)
		}
	}
	return strings.Join(parts, ".")
}

func resolveLeanDecl(ns, decl string) string {
	if trimmed, ok := strings.CutPrefix(decl, "_root_."); ok {
		return trimmed
	}
	if ns == "" {
		return decl
	}
	return ns + "." + decl
}

func leanUsesSorry(src string) bool {
	for _, tok := range tokenizeLean(src) {
		if isUnescapedIdent(tok, "sorry") || isUnescapedIdent(tok, "sorryAx") {
			return true
		}
	}
	return false
}

func leanHasAxiom(src string) bool {
	tokens := tokenizeLean(src)
	for i, tok := range tokens {
		prevDot := i > 0 && tokens[i-1].kind == tokPunct && tokens[i-1].text == "."
		if !prevDot && isUnescapedIdent(tok, "axiom") {
			return true
		}
	}
	return false
}
