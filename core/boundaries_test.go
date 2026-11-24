package core

import (
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	gopath "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestCoreDoesNotAddRustModelCheckerOrProofToolchains(t *testing.T) {
	root := repoRoot(t)
	bannedExt := map[string]struct{}{
		".qnt": {},
		".rs":  {},
		".tla": {},
		".vpr": {},
	}
	bannedBase := map[string]struct{}{
		"Cargo.toml":          {},
		"Cargo.lock":          {},
		"quint.toml":          {},
		"rust-toolchain":      {},
		"rust-toolchain.toml": {},
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".lake", ".serena":
				return filepath.SkipDir
			}
			if rel == "docs" || filepath.ToSlash(rel) == agentWorktrees {
				return filepath.SkipDir
			}
			return nil
		}
		if _, banned := bannedExt[strings.ToLower(filepath.Ext(d.Name()))]; banned {
			t.Errorf("found %s", rel)
		}
		if _, banned := bannedBase[d.Name()]; banned {
			t.Errorf("found %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(workflows) == 0 {
		t.Fatal("no .github/workflows/*.yml files found")
	}
	targets := []string{"go.mod", "spec/lakefile.toml"}
	for _, wf := range workflows {
		rel, relErr := filepath.Rel(root, wf)
		if relErr != nil {
			t.Fatal(relErr)
		}
		targets = append(targets, filepath.ToSlash(rel))
	}
	bannedToolchains := []string{"verus", "anvil", "welder", "gobra", "perennial"}
	for _, rel := range targets {
		lower := strings.ToLower(readRepoFile(t, rel))
		for _, name := range bannedToolchains {
			if strings.Contains(lower, name) {
				t.Errorf("%s references banned toolchain %s", rel, name)
			}
		}
	}
}

func TestHumanFacingSurfaceHasNoYAML(t *testing.T) {
	root := repoRoot(t)
	sawDocdag := false
	workflowCount := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".lake", ".serena":
				return filepath.SkipDir
			}
			if rel, err := filepath.Rel(root, path); err == nil && filepath.ToSlash(rel) == agentWorktrees {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case rel == "docdag.yaml":
			sawDocdag = true
		case rel == ".github/dependabot.yml":
		case filepath.ToSlash(filepath.Dir(rel)) == ".github/workflows" && ext == ".yml":
			workflowCount++
		default:
			t.Errorf("unexpected YAML file %s; only docdag.yaml, .github/dependabot.yml and .github/workflows/*.yml are allowed", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawDocdag {
		t.Error("docdag.yaml is missing")
	}
	if workflowCount == 0 {
		t.Error("no .github/workflows/*.yml files found")
	}
}

type nameSet map[string]struct{}

func names(list ...string) nameSet {
	s := make(nameSet, len(list))
	for _, n := range list {
		s[n] = struct{}{}
	}
	return s
}

func (s nameSet) has(n string) bool {
	_, ok := s[n]
	return ok
}

func (s nameSet) sorted() []string {
	out := make([]string, 0, len(s))
	for n := range s {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// effectPolicy decides which package members and methods count as reaching
// the outside world. A package listed in allow permits only the named members
// (everything else is forbidden); a package listed in deny forbids only the
// named members. Members named in bubbleExempt are permitted lexically inside
// a testing/synctest bubble.
type effectPolicy struct {
	bannedImports nameSet
	allow         map[string]func(string) bool
	deny          map[string]nameSet
	bubbleExempt  map[string]nameSet
	methods       nameSet
	zeroTransport bool
	bubbleHelpers nameSet
	bubbleOnly    nameSet
}

func (p effectPolicy) policed(path string) bool {
	_, a := p.allow[path]
	_, d := p.deny[path]
	return a || d
}

func (p effectPolicy) forbids(path, name string) bool {
	if allowed, ok := p.allow[path]; ok {
		return !allowed(name)
	}
	if denied, ok := p.deny[path]; ok {
		return denied.has(name)
	}
	return false
}

const simPackage = "core/sim"

// The allowlists name harmless members only: constants, error values, types,
// and pure helpers. Anything added to os or net by a later Go release is
// forbidden until it is listed here. Process arguments, the environment, the
// standard streams and Exit are not harmless: only files named in an adapter
// allowlist may use them.
var osHarmless = names(
	"File", "FileInfo", "FileMode", "DirEntry", "PathError", "LinkError", "SyscallError",
	"Signal", "Process", "ProcessState", "ProcAttr", "Root",
	"ErrInvalid", "ErrPermission", "ErrExist", "ErrNotExist", "ErrClosed", "ErrNoDeadline",
	"ErrDeadlineExceeded", "ErrProcessDone",
	"O_RDONLY", "O_WRONLY", "O_RDWR", "O_APPEND", "O_CREATE", "O_EXCL", "O_SYNC", "O_TRUNC",
	"SEEK_SET", "SEEK_CUR", "SEEK_END",
	"ModeDir", "ModeAppend", "ModeExclusive", "ModeTemporary", "ModeSymlink", "ModeDevice",
	"ModeNamedPipe", "ModeSocket", "ModeSetuid", "ModeSetgid", "ModeCharDevice", "ModeSticky",
	"ModeIrregular", "ModeType", "ModePerm",
	"PathSeparator", "PathListSeparator", "DevNull",
	"IsExist", "IsNotExist", "IsPermission", "IsTimeout", "IsPathSeparator", "NewSyscallError",
	"SameFile", "Expand", "Interrupt", "Kill",
)

var osStdio = names("Stdin", "Stdout", "Stderr")

var osStdioMethodsOK = names("Write", "WriteString", "Read")

// syscallHarmless admits signal numbers and errno values, which are plain
// constants; every syscall function is forbidden.
func syscallHarmless(name string) bool {
	if name == "Signal" || name == "Errno" {
		return true
	}
	if strings.HasPrefix(name, "SIG") {
		return name == strings.ToUpper(name)
	}
	return len(name) > 1 && name[0] == 'E' && name == strings.ToUpper(name)
}

var netHarmless = names(
	"IP", "IPNet", "IPMask", "IPAddr", "TCPAddr", "UDPAddr", "UnixAddr", "HardwareAddr",
	"Addr", "Conn", "PacketConn", "Listener", "Error", "Buffers", "Flags",
	"OpError", "AddrError", "ParseError", "DNSError", "DNSConfigError", "InvalidAddrError",
	"UnknownNetworkError", "TCPConn", "UDPConn", "UnixConn", "IPConn",
	"ErrClosed", "ErrWriteToConnected",
	"IPv4len", "IPv6len", "IPv4zero", "IPv4bcast", "IPv4allsys", "IPv4allrouter",
	"IPv6zero", "IPv6unspecified", "IPv6loopback", "IPv6interfacelocalallnodes",
	"IPv6linklocalallnodes", "IPv6linklocalallrouters",
	"FlagUp", "FlagBroadcast", "FlagLoopback", "FlagPointToPoint", "FlagMulticast", "FlagRunning",
	"ParseIP", "ParseCIDR", "ParseMAC", "IPv4", "IPv4Mask", "CIDRMask", "JoinHostPort",
	"SplitHostPort", "Pipe",
)

var httpHarmlessNames = names(
	"Client", "Transport", "Request", "Response", "ResponseWriter", "Header", "Handler", "HandlerFunc",
	"Server", "ServeMux", "NewServeMux", "Flusher", "Hijacker", "Pusher", "PushOptions", "RoundTripper",
	"CookieJar", "Cookie", "SetCookie", "SameSite", "ParseCookie", "ParseSetCookie", "ParseHTTPVersion",
	"ParseTime", "Protocols", "HTTP2Config", "MaxBytesError", "MaxBytesReader", "MaxBytesHandler",
	"NoBody", "ResponseController", "NewResponseController", "NewRequest", "NewRequestWithContext",
	"Error", "NotFound", "NotFoundHandler", "Redirect", "RedirectHandler", "StripPrefix", "TimeoutHandler",
	"AllowQuerySemicolons", "StatusText", "CanonicalHeaderKey", "DetectContentType", "TimeFormat",
	"TrailerPrefix", "DefaultMaxHeaderBytes", "DefaultMaxIdleConnsPerHost", "ConnState", "ProxyURL",
	"ServeContent", "LocalAddrContextKey", "ServerContextKey", "File", "FileSystem",
)

// httpHarmless admits types, constants, error values and helpers that touch
// no real resource. Dir, FS, FileServer, ServeFile and the other file-serving
// entry points read the real disk, ProxyFromEnvironment reads the
// environment, and Get, ListenAndServe and the default client and transport
// open real sockets, so they are forbidden by omission.
func httpHarmless(name string) bool {
	for _, prefix := range []string{"Method", "Status", "Err", "State", "SameSite"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return httpHarmlessNames.has(name)
}

var tlsHarmlessNames = names(
	"Config", "Certificate", "Conn", "ConnectionState", "ClientHelloInfo", "CertificateRequestInfo",
	"CipherSuite", "CipherSuites", "InsecureCipherSuites", "CipherSuiteName", "CurveID", "SignatureScheme",
	"ClientAuthType", "NoClientCert", "RequestClientCert", "RequireAnyClientCert", "VerifyClientCertIfGiven",
	"RequireAndVerifyClientCert", "ClientSessionCache", "ClientSessionState", "NewLRUClientSessionCache",
	"RenegotiateNever", "RenegotiateOnceAsClient", "RenegotiateFreelyAsClient", "RenegotiationSupport",
	"RecordHeaderError", "AlertError", "CertificateVerificationError", "EncryptedClientHelloKey",
	"Client", "Server", "NewListener", "X509KeyPair", "VersionName",
)

// tlsHarmless admits configuration types and constants and the helpers that
// wrap an existing connection or parse PEM held in memory. Dial, Listen,
// Dialer and LoadX509KeyPair reach the network or the disk and are forbidden
// by omission.
func tlsHarmless(name string) bool {
	for _, prefix := range []string{"Version", "TLS_", "CurveP", "X25519", "PKCS1", "PSS", "ECDSA", "Ed25519"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return tlsHarmlessNames.has(name)
}

var (
	timeWaiting   = []string{"Sleep", "After", "AfterFunc", "NewTimer", "NewTicker", "Tick"}
	ctxDeadlines  = names("WithTimeout", "WithDeadline", "WithTimeoutCause", "WithDeadlineCause")
	filepathReal  = names("WalkDir", "Walk", "Glob", "EvalSymlinks", "Abs")
	simRealEnv    = names("RealNet", "RealClock", "OSDisk", "NewRootedDisk", "CryptoRand")
	realIOClients = []string{"github.com/containerd/containerd/v2/client", "go.etcd.io/etcd/client/v3", "google.golang.org/grpc"}
)

var productionPolicy = effectPolicy{
	bannedImports: names(append([]string{
		"os/exec", "os/user", "os/signal", "math/rand", "math/rand/v2", "crypto/rand", "io/ioutil",
		"golang.org/x/sys/unix", "golang.org/x/sys/windows", "net/http/httptest", "net/rpc", "hash/maphash",
		"C", "plugin", "net/http/pprof", "net/http/cgi", "net/http/fcgi", "net/smtp", "expvar",
	}, realIOClients...)...),
	allow: map[string]func(string) bool{
		"os":         osHarmless.has,
		"net":        netHarmless.has,
		"syscall":    syscallHarmless,
		"net/http":   httpHarmless,
		"crypto/tls": tlsHarmless,
	},
	deny: map[string]nameSet{
		"time":          names(append([]string{"Now", "Since", "Until", "LoadLocation", "Local"}, timeWaiting...)...),
		"context":       ctxDeadlines,
		"path/filepath": filepathReal,
		"crypto/x509":   names("SystemCertPool"),
		"log":           names("Fatal", "Fatalf", "Fatalln", "Panic", "Panicf", "Panicln"),
	},
	methods:       names("ListenAndServe", "ListenAndServeTLS", "Local"),
	zeroTransport: true,
}

func testPolicy(bubbleHelpers, bubbleOnly nameSet) effectPolicy {
	return effectPolicy{
		bannedImports: names(append([]string{"C", "plugin"}, realIOClients...)...),
		allow: map[string]func(string) bool{
			"os":         osHarmless.has,
			"net":        netHarmless.has,
			"net/http":   httpHarmless,
			"crypto/tls": tlsHarmless,
		},
		deny: map[string]nameSet{
			"time":              names(timeWaiting...),
			"context":           ctxDeadlines,
			"path/filepath":     filepathReal,
			"net/http/httptest": names("NewServer", "NewTLSServer", "NewUnstartedServer"),
			simPackage:          simRealEnv,
		},
		bubbleExempt: map[string]nameSet{
			"time":    names(timeWaiting...),
			"context": ctxDeadlines,
		},
		methods:       names("ListenAndServe", "ListenAndServeTLS"),
		bubbleHelpers: bubbleHelpers,
		bubbleOnly:    bubbleOnly,
	}
}

type effectFinding struct {
	line int
	key  string
	msg  string
}

// exemption lets one file bypass a policy for the listed finding keys only.
type exemption struct {
	reason string
	keys   nameSet
}

type usage map[string]map[string]bool

func unexempted(ex map[string]exemption, used usage, rel string, findings []effectFinding) []effectFinding {
	e, ok := ex[rel]
	if !ok {
		return findings
	}
	var out []effectFinding
	for _, f := range findings {
		if e.keys.has(f.key) {
			if used[rel] == nil {
				used[rel] = map[string]bool{}
			}
			used[rel][f.key] = true
		} else {
			out = append(out, f)
		}
	}
	return out
}

func reportStaleExemptions(t *testing.T, label string, ex map[string]exemption, used usage) {
	t.Helper()
	rels := make([]string, 0, len(ex))
	for rel := range ex {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		e := ex[rel]
		if e.reason == "" || len(e.keys) == 0 {
			t.Errorf("%s allowlist entry %s needs a reason and at least one key", label, rel)
		}
		for _, k := range e.keys.sorted() {
			if !used[rel][k] {
				t.Errorf("%s allowlist entry %s key %q suppresses nothing; remove it", label, rel, k)
			}
		}
	}
}

// canonicalImport collapses the module-qualified sim package to a stable name.
func canonicalImport(impPath string) string {
	if impPath == simPackage || strings.HasSuffix(impPath, "/"+simPackage) {
		return simPackage
	}
	return impPath
}

func localImportName(impPath string) string {
	local := gopath.Base(impPath)
	if len(local) > 1 && local[0] == 'v' && strings.Trim(local[1:], "0123456789") == "" {
		local = gopath.Base(gopath.Dir(impPath))
	}
	return local
}

func importAliases(file *ast.File) map[string]string {
	aliases := make(map[string]string, len(file.Imports))
	for _, imp := range file.Imports {
		impPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		local := localImportName(impPath)
		if imp.Name != nil {
			switch imp.Name.Name {
			case "_", ".":
				continue
			}
			local = imp.Name.Name
		}
		aliases[local] = canonicalImport(impPath)
	}
	return aliases
}

func resolveImports(p effectPolicy, fset *token.FileSet, file *ast.File) (map[string]string, []effectFinding) {
	var out []effectFinding
	for _, imp := range file.Imports {
		impPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		line := fset.Position(imp.Pos()).Line
		if p.bannedImports.has(impPath) {
			out = append(out, effectFinding{line, "import:" + impPath, "imports banned package " + strconv.Quote(impPath)})
		}
		if imp.Name != nil && imp.Name.Name == "." && p.policed(canonicalImport(impPath)) {
			out = append(out, effectFinding{line, "dotimport:" + impPath, "dot-imports " + strconv.Quote(impPath)})
		}
	}
	return importAliases(file), out
}

func packageSelector(aliases map[string]string, n ast.Expr) (string, string, bool) {
	sel, ok := n.(*ast.SelectorExpr)
	if !ok {
		return "", "", false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || ident.Obj != nil {
		return "", "", false
	}
	path, ok := aliases[ident.Name]
	if !ok {
		return "", "", false
	}
	return path, sel.Sel.Name, true
}

func isNilIdent(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name == "nil" && x.Obj == nil
	case *ast.ParenExpr:
		return isNilIdent(x.X)
	case *ast.CallExpr:
		if len(x.Args) != 1 || !isNilIdent(x.Args[0]) {
			return false
		}
		switch f := x.Fun.(type) {
		case *ast.ParenExpr:
			return true
		case *ast.SelectorExpr:
			id, ok := f.X.(*ast.Ident)
			return ok && id.Obj == nil
		}
	}
	return false
}

func hasLiveKey(lit *ast.CompositeLit, keys ...string) bool {
	want := names(keys...)
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && want.has(id.Name) && !isNilIdent(kv.Value) {
			return true
		}
	}
	return false
}

func pkgKey(pkg, name string) string { return pkg + "." + name }

func isBubbleCall(aliases map[string]string, helpers nameSet, pkg string, c *ast.CallExpr) bool {
	if path, name, ok := packageSelector(aliases, c.Fun); ok {
		return path == "testing/synctest" && (name == "Test" || name == "Run")
	}
	id, ok := c.Fun.(*ast.Ident)
	return ok && (id.Obj == nil || id.Obj.Kind == ast.Fun) && helpers.has(pkgKey(pkg, id.Name))
}

func bubbleLiterals(aliases map[string]string, helpers nameSet, pkg string, file *ast.File) map[*ast.FuncLit]bool {
	lits := map[*ast.FuncLit]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && isBubbleCall(aliases, helpers, pkg, c) {
			for _, a := range c.Args {
				if lit, ok := a.(*ast.FuncLit); ok {
					lits[lit] = true
				}
			}
		}
		return true
	})
	return lits
}

func isTestEntry(name string) bool {
	for _, prefix := range []string{"Test", "Benchmark", "Fuzz", "Example"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return name == "init" || name == "main"
}

// dirBubbleInfo decides, from the files of one directory, which functions run
// their function argument inside a synctest bubble (helpers) and which
// functions are only ever called from inside a bubble (only). Packages of the
// same directory are analysed separately and keys carry the package name.
func dirBubbleInfo(files []*ast.File) (helpers, only nameSet) {
	byPkg := map[string][]*ast.File{}
	for _, f := range files {
		byPkg[f.Name.Name] = append(byPkg[f.Name.Name], f)
	}
	helpers, only = nameSet{}, nameSet{}
	for pkg, pf := range byPkg {
		h, o := pkgBubbleInfo(pkg, pf)
		for n := range h {
			helpers[n] = struct{}{}
		}
		for n := range o {
			only[pkgKey(pkg, n)] = struct{}{}
		}
	}
	return helpers, only
}

func pkgBubbleInfo(pkg string, files []*ast.File) (helpers, only nameSet) {
	helpers = nameSet{}
	aliases := make([]map[string]string, len(files))
	for i, f := range files {
		aliases[i] = importAliases(f)
	}
	for changed := true; changed; {
		changed = false
		for i, f := range files {
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || fd.Recv != nil || fd.Body == nil || helpers.has(pkgKey(pkg, fd.Name.Name)) {
					continue
				}
				funcParams := nameSet{}
				for _, field := range fd.Type.Params.List {
					if _, ok := field.Type.(*ast.FuncType); ok {
						for _, n := range field.Names {
							funcParams[n.Name] = struct{}{}
						}
					}
				}
				if len(funcParams) == 0 {
					continue
				}
				uses, bubbled := map[string]int{}, map[string]int{}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.Ident:
						if funcParams.has(x.Name) {
							uses[x.Name]++
						}
					case *ast.CallExpr:
						if isBubbleCall(aliases[i], helpers, pkg, x) {
							for _, a := range x.Args {
								if id, ok := a.(*ast.Ident); ok && funcParams.has(id.Name) {
									bubbled[id.Name]++
								}
							}
						}
					}
					return true
				})
				found := false
				for name := range funcParams {
					if bubbled[name] > 0 && uses[name] == bubbled[name] {
						found = true
					}
				}
				if found {
					helpers[pkgKey(pkg, fd.Name.Name)] = struct{}{}
					changed = true
				}
			}
		}
	}

	candidates := nameSet{}
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && !isTestEntry(fd.Name.Name) && fd.Name.Name != "TestMain" {
				candidates[fd.Name.Name] = struct{}{}
			}
		}
	}
	type site struct {
		inBubble bool
		fn       string
	}
	sites := map[string][]site{}
	escaped := nameSet{}
	for i, f := range files {
		lits := bubbleLiterals(aliases[i], helpers, pkg, f)
		var stack []ast.Node
		depth := 0
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if lit, ok := top.(*ast.FuncLit); ok && lits[lit] {
					depth--
				}
				return true
			}
			if lit, ok := n.(*ast.FuncLit); ok && lits[lit] {
				depth++
			}
			var parent ast.Node
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			stack = append(stack, n)
			id, ok := n.(*ast.Ident)
			if !ok || !candidates.has(id.Name) {
				return true
			}
			switch pp := parent.(type) {
			case *ast.FuncDecl:
				if pp.Name == id {
					return true
				}
			case *ast.SelectorExpr:
				if pp.Sel == id {
					return true
				}
			case *ast.CallExpr:
				if pp.Fun == id {
					fn := ""
					for j := len(stack) - 1; j >= 0; j-- {
						if fd, ok := stack[j].(*ast.FuncDecl); ok {
							if fd.Recv == nil {
								fn = fd.Name.Name
							}
							break
						}
					}
					sites[id.Name] = append(sites[id.Name], site{depth > 0, fn})
					return true
				}
			}
			escaped[id.Name] = struct{}{}
			return true
		})
	}
	only = nameSet{}
	for changed := true; changed; {
		changed = false
		for name := range candidates {
			if only.has(name) || escaped.has(name) || len(sites[name]) == 0 {
				continue
			}
			ok := true
			for _, s := range sites[name] {
				if !s.inBubble && s.fn != name && !only.has(s.fn) {
					ok = false
					break
				}
			}
			if ok {
				only[name] = struct{}{}
				changed = true
			}
		}
	}
	return helpers, only
}

func effectViolations(p effectPolicy, fset *token.FileSet, file *ast.File) []effectFinding {
	aliases, out := resolveImports(p, fset, file)
	lits := bubbleLiterals(aliases, p.bubbleHelpers, file.Name.Name, file)
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:linkname") {
				out = append(out, effectFinding{fset.Position(c.Pos()).Line, "linkname", "uses //go:linkname to reach runtime internals"})
			}
		}
	}
	isHTTPValue := func(e ast.Expr) (string, bool) {
		path, name, ok := packageSelector(aliases, e)
		if ok && path == "net/http" && (name == "Client" || name == "Transport") {
			return name, true
		}
		return "", false
	}
	var entered []bool
	depth := 0
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			if entered[len(entered)-1] {
				depth--
			}
			entered = entered[:len(entered)-1]
			return true
		}
		enter := false
		switch x := n.(type) {
		case *ast.FuncLit:
			enter = lits[x]
		case *ast.FuncDecl:
			enter = x.Recv == nil && p.bubbleOnly.has(pkgKey(file.Name.Name, x.Name.Name))
		}
		entered = append(entered, enter)
		if enter {
			depth++
		}
		switch x := n.(type) {
		case *ast.SelectorExpr:
			line := fset.Position(x.Pos()).Line
			if path, name, ok := packageSelector(aliases, x); ok {
				if p.forbids(path, name) && (depth == 0 || !p.bubbleExempt[path].has(name)) {
					out = append(out, effectFinding{line, path + "." + name, "directly references " + path + "." + name})
				}
			} else if path, name, ok := packageSelector(aliases, x.X); ok && path == "os" && osStdio.has(name) && !osStdioMethodsOK.has(x.Sel.Name) {
				out = append(out, effectFinding{line, "os.File." + x.Sel.Name, "calls os." + name + "." + x.Sel.Name + ", which acts on a real file descriptor"})
			} else if p.methods.has(x.Sel.Name) {
				out = append(out, effectFinding{line, "method:" + x.Sel.Name, "calls method " + x.Sel.Name + ", which opens a real listener"})
			}
		case *ast.CompositeLit:
			if !p.zeroTransport {
				return true
			}
			name, ok := isHTTPValue(x.Type)
			if !ok {
				return true
			}
			line := fset.Position(x.Pos()).Line
			switch {
			case name == "Client" && !hasLiveKey(x, "Transport"):
				out = append(out, effectFinding{line, "net/http.Client{}", "http.Client without a non-nil Transport falls back to the real default transport"})
			case name == "Transport" && !hasLiveKey(x, "DialContext", "Dial"):
				out = append(out, effectFinding{line, "net/http.Transport{}", "http.Transport without a non-nil dial function opens real sockets"})
			}
		case *ast.ValueSpec:
			if !p.zeroTransport || x.Type == nil || len(x.Values) > 0 {
				return true
			}
			if name, ok := isHTTPValue(x.Type); ok {
				out = append(out, effectFinding{fset.Position(x.Pos()).Line, "net/http." + name + "{}", "zero-value http." + name + " uses the real default transport"})
			}
		case *ast.Field:
			if !p.zeroTransport {
				return true
			}
			if name, ok := isHTTPValue(x.Type); ok {
				out = append(out, effectFinding{fset.Position(x.Pos()).Line, "net/http." + name + "{}", "http." + name + " held by value is zero-valued unless built elsewhere"})
			}
		case *ast.CallExpr:
			if !p.zeroTransport {
				return true
			}
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && id.Obj == nil && len(x.Args) == 1 {
				if name, ok := isHTTPValue(x.Args[0]); ok {
					out = append(out, effectFinding{fset.Position(x.Pos()).Line, "net/http." + name + "{}", "new(http." + name + ") uses the real default transport"})
				}
			}
		}
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].line < out[j].line })
	return out
}

var integrationTags = names("etcd", "drt", "containerd", "nftables")

func constraintTags(e constraint.Expr, into nameSet) {
	switch x := e.(type) {
	case *constraint.AndExpr:
		constraintTags(x.X, into)
		constraintTags(x.Y, into)
	case *constraint.OrExpr:
		constraintTags(x.X, into)
		constraintTags(x.Y, into)
	case *constraint.NotExpr:
		constraintTags(x.X, into)
	case *constraint.TagExpr:
		into[x.Tag] = struct{}{}
	}
}

// scannedByDefaultRun reports whether an ordinary run could compile the file.
// Integration tags are off; every other tag may be either, so a file is skipped
// only when no assignment of the other tags satisfies its constraint.
func scannedByDefaultRun(file *ast.File) bool {
	var expr constraint.Expr
	for _, cg := range file.Comments {
		if cg.Pos() > file.Package {
			break
		}
		for _, c := range cg.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			e, err := constraint.Parse(c.Text)
			if err != nil {
				return true
			}
			expr = e
		}
	}
	if expr == nil {
		return true
	}
	all := nameSet{}
	constraintTags(expr, all)
	var free []string
	for _, tag := range all.sorted() {
		if !integrationTags.has(tag) {
			free = append(free, tag)
		}
	}
	if len(free) > 16 {
		return true
	}
	for mask := 0; mask < 1<<len(free); mask++ {
		on := nameSet{}
		for i, tag := range free {
			if mask&(1<<i) != 0 {
				on[tag] = struct{}{}
			}
		}
		if expr.Eval(on.has) {
			return true
		}
	}
	return false
}

// productionEffectAdapters are the only non-test files under core/ and cmd/
// allowed to reach the real environment directly, and only through the listed
// finding keys.
var productionEffectAdapters = map[string]exemption{
	"cmd/check-protected-theorems/main.go":       {reason: "command line entry point reports on stderr and exits with a status", keys: names("os.Stderr", "os.Exit")},
	"cmd/dev-etcd/main.go":                       {reason: "developer tool that spawns etcd, reads its config and writes the real filesystem", keys: names("import:os/exec", "import:os/signal", "os.Exit", "os.Getenv", "os.MkdirAll", "os.ReadFile", "os.Stderr", "os.Stdout", "os.UserHomeDir")},
	"cmd/lk-node/main.go":                        {reason: "command line entry point: arguments, standard streams, exit status and signal handling", keys: names("import:os/signal", "os.Args", "os.Exit", "os.Stderr", "os.Stdout")},
	"core/drt/drt.go":                            {reason: "locates and spawns the Lean oracle process and reads its seed variables", keys: names("import:os/exec", "os.Getenv", "os.Getwd", "os.Stat", "os.UserHomeDir")},
	"core/netpol/enforcer.go":                    {reason: "runs nft against the real kernel and checks the binary", keys: names("import:os/exec", "os.Stat", "path/filepath.EvalSymlinks", "syscall.Stat_t")},
	"core/node/cni/cni.go":                       {reason: "invokes real CNI plugin binaries and resolves their paths", keys: names("import:os/exec", "os.Stat", "path/filepath.EvalSymlinks")},
	"core/node/containerd/adapter.go":            {reason: "the containerd client opens the real containerd unix socket", keys: names("import:github.com/containerd/containerd/v2/client")},
	"core/node/netns/netns_linux.go":             {reason: "creates and pins real network namespaces", keys: names("import:golang.org/x/sys/unix", "os.Getpid", "os.Lstat", "os.MkdirAll", "os.OpenFile", "os.Remove", "os.Stat")},
	"core/sim/interfaces.go":                     {reason: "defines the production Clock, Net, Disk and Rand implementations", keys: names("context.WithTimeout", "import:crypto/rand", "net.Dialer", "net.ListenConfig", "os.MkdirAll", "os.OpenRoot", "os.ReadFile", "os.Remove", "os.Stat", "os.WriteFile", "path/filepath.EvalSymlinks", "time.After", "time.NewTimer", "time.Now")},
	"core/sim/queue.go":                          {reason: "reads the seed environment variables", keys: names("os.Getenv")},
	"core/sim/rand.go":                           {reason: "wraps the deterministic generator behind Rand", keys: names("import:math/rand/v2")},
	"core/store/etcdstore/etcdstore.go":          {reason: "the etcd adapter dials the real etcd cluster", keys: names("import:go.etcd.io/etcd/client/v3")},
	"core/store/etcdstore/etcdtest/etcdtest.go":  {reason: "spawns and polls real etcd processes for the integration harness", keys: names("context.WithTimeout", "import:go.etcd.io/etcd/client/v3", "import:os/exec", "net.Listen", "os.Getenv", "os.Getpid", "os.MkdirAll", "os.MkdirTemp", "os.OpenFile", "os.RemoveAll", "os.TempDir", "syscall.Flock", "syscall.LOCK_EX", "syscall.LOCK_NB", "time.Now", "time.Sleep")},
	"core/store/etcdstore/etcdtest/peerproxy.go": {reason: "falls back to a real loopback proxy when no sim.Net is injected", keys: names("net.Dialer", "net.Listen")},
	"core/store/storetest/contract.go":           {reason: "shared harness also driven by real-time etcd callers", keys: names("context.WithTimeout", "import:math/rand/v2", "time.After", "time.NewTicker", "time.Now")},
	"core/store/storetest/cotenancy.go":          {reason: "measures real latency against real stores", keys: names("time.NewTicker", "time.NewTimer", "time.Now", "time.Since")},
	"core/store/storetest/linearizability.go":    {reason: "shared harness also driven by real-time etcd callers", keys: names("context.WithTimeout", "import:math/rand/v2", "time.NewTimer")},
	"core/theoremguard.go":                       {reason: "reads the repository's Lean sources to guard protected theorems", keys: names("os.ReadFile")},
	"core/wire/client.go":                        {reason: "a nil http.Client falls back to the real default transport", keys: names("net/http.Client{}")},
	"core/wire/http2.go":                         {reason: "the dial function is injected only when a sim.Net is supplied", keys: names("net/http.Transport{}")},
}

// realTimeTestFiles are untagged test files and test-support files allowed to
// touch the real environment, and only through the listed finding keys.
var realTimeTestFiles = map[string]exemption{
	"config/schema_test.go":                        {reason: "reads the schema and declaration files under config", keys: names("os.ReadFile")},
	"core/boundaries_test.go":                      {reason: "repository-structure tests walk the source tree and build a scratch tree under t.TempDir", keys: names("os.MkdirAll", "os.Stat", "os.WriteFile", "path/filepath.Glob", "path/filepath.WalkDir")},
	"core/ci_test.go":                              {reason: "repository-structure tests read the source tree", keys: names("os.Lstat", "os.ReadFile", "path/filepath.WalkDir")},
	"core/repo_test.go":                            {reason: "locates and reads the repository root", keys: names("os.Getwd", "os.ReadFile", "os.Stat")},
	"core/spec_test.go":                            {reason: "repository-structure tests read the Lean sources", keys: names("os.ReadFile", "path/filepath.WalkDir")},
	"core/theoremguard_test.go":                    {reason: "tests the theorem guard against Lean sources written under t.TempDir", keys: names("os.MkdirAll", "os.ReadFile", "os.WriteFile", "path/filepath.Glob")},
	"core/drt/config_test.go":                      {reason: "tests the seed environment variables", keys: names("os.Unsetenv")},
	"core/drt/drt.go":                              {reason: "locates and spawns the Lean oracle process and reads its seed variables", keys: names("os.Getenv", "os.Getwd", "os.Stat", "os.UserHomeDir")},
	"core/liveness/aggregator_test.go":             {reason: "offsetClock.After is a method, so its use inside a bubble cannot be decided syntactically", keys: names("time.After")},
	"core/liveness/registry_test.go":               {reason: "hang-guard deadline that never fires on a passing run", keys: names("context.WithTimeout")},
	"core/netpol/netpol_test.go":                   {reason: "exercises the nft enforcer against fake binaries and symlinks under t.TempDir", keys: names("os.Geteuid", "os.ReadFile", "os.Symlink", "os.WriteFile")},
	"core/netpol/render_test.go":                   {reason: "exercises the nft enforcer against fake binaries under t.TempDir", keys: names("os.Geteuid", "os.WriteFile")},
	"core/node/cni/cni_test.go":                    {reason: "exercises the CNI adapter against fake plugin binaries and symlinks under t.TempDir", keys: names("os.ReadFile", "os.Symlink", "os.WriteFile")},
	"core/node/containerd/adapter_test.go":         {reason: "type-level checks of the containerd adapter; the client is never dialled", keys: names("import:github.com/containerd/containerd/v2/client")},
	"core/node/netns/netns_test.go":                {reason: "exercises the netns adapter against /proc and files under t.TempDir", keys: names("os.Getpid", "os.Stat", "os.Symlink", "os.WriteFile")},
	"core/resource/objects_ownership_test.go":      {reason: "hang-guard deadline that never fires on a passing run", keys: names("context.WithTimeout")},
	"core/resource/objects_patch_test.go":          {reason: "hang-guard deadline that never fires on a passing run", keys: names("context.WithTimeout")},
	"core/resource/objects_refs_test.go":           {reason: "hang-guard deadline that never fires on a passing run", keys: names("context.WithTimeout")},
	"core/resource/objects_test.go":                {reason: "hang-guard deadline that never fires on a passing run", keys: names("context.WithTimeout")},
	"core/sim/clustersim/clustersim_test.go":       {reason: "reads the seed environment variables", keys: names("os.Getenv")},
	"core/sim/sim_test.go":                         {reason: "tests the production Clock, Rand and RootedDisk implementations against t.TempDir; a setup literal stored in a map runs only inside a bubble, which cannot be shown syntactically", keys: names("core/sim.CryptoRand", "core/sim.NewRootedDisk", "core/sim.RealClock", "os.MkdirAll", "os.Symlink", "os.WriteFile", "time.Sleep")},
	"core/store/etcdstore/errtext_test.go":         {reason: "hang-guard deadline on an in-process gRPC call; the file imports the real clients but only over a bufconn listener", keys: names("context.WithTimeout", "import:go.etcd.io/etcd/client/v3", "import:google.golang.org/grpc")},
	"core/store/etcdstore/etcdtest/etcdtest.go":    {reason: "spawns and polls real etcd processes for the integration harness", keys: names("context.WithTimeout", "import:go.etcd.io/etcd/client/v3", "net.Listen", "os.Getenv", "os.Getpid", "os.MkdirAll", "os.MkdirTemp", "os.OpenFile", "os.RemoveAll", "os.TempDir", "time.Sleep")},
	"core/store/etcdstore/etcdtest/peerproxy.go":   {reason: "falls back to a real loopback proxy when no sim.Net is injected", keys: names("net.Dialer", "net.Listen")},
	"core/store/storetest/contract.go":             {reason: "shared harness also driven by real-time etcd callers", keys: names("context.WithTimeout", "time.After", "time.NewTicker")},
	"core/store/storetest/cotenancy.go":            {reason: "measures real latency and busy-loops, so it cannot run in a bubble", keys: names("time.NewTicker", "time.NewTimer")},
	"core/store/storetest/cotenancy_test.go":       {reason: "hang-guard deadline on the real-time latency measurement", keys: names("context.WithTimeout")},
	"core/store/storetest/linearizability.go":      {reason: "shared harness also driven by real-time etcd callers", keys: names("context.WithTimeout", "time.NewTimer")},
	"core/store/storetest/linearizability_test.go": {reason: "store wrapper methods delay calls; they run only under tests that enter a bubble, which a method cannot show syntactically", keys: names("time.Sleep")},
	"core/store/tenant/registry_test.go":           {reason: "hang-guard deadline that never fires on a passing run", keys: names("context.WithTimeout")},
	"core/store/tenant/tenant_test.go":             {reason: "hang-guard deadline that never fires on a passing run", keys: names("context.WithTimeout")},
	"core/store/watchcache/helpers_test.go":        {reason: "wait helpers shared by scenarios that run through runScenario, which enters a bubble only when opts.inSynctest is set", keys: names("time.After")},
	"core/store/watchcache/watchcache_test.go":     {reason: "scenario bodies run through runScenario, which enters a bubble only when opts.inSynctest is set", keys: names("time.After")},
}

// testSupportDirs hold non-test files that only exist to support tests.
var testSupportDirs = []string{
	"core/store/storetest/",
	"core/store/etcdstore/etcdtest/",
	"core/drt/",
}

func isTestSupport(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, d := range testSupportDirs {
		if strings.HasPrefix(rel, d) {
			return true
		}
	}
	return false
}

type goFile struct{ rel, path string }

func skippedDir(root, path string, d os.DirEntry) bool {
	if path == root {
		return false
	}
	name := d.Name()
	if rel, err := filepath.Rel(root, path); err == nil && rel == "spec" {
		return true
	}
	if name == "vendor" || name == "testdata" || name == "node_modules" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return true
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	return err == nil
}

// walkGoFiles visits every Go and assembly file of the module rooted at root,
// skipping hidden, vendor and testdata directories, the Lean package under
// spec and nested modules.
func walkGoFiles(t *testing.T, root string, visit func(goFile)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if skippedDir(root, path, d) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") && !strings.HasSuffix(d.Name(), ".s") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		visit(goFile{filepath.ToSlash(rel), path})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCoreRoutesExternalEffectsThroughSimInterfaces(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	checked := 0
	used := usage{}
	walkGoFiles(t, root, func(gf goFile) {
		if strings.HasSuffix(gf.rel, "_test.go") {
			return
		}
		checked++
		if strings.HasSuffix(gf.rel, ".s") {
			assembly := []effectFinding{{1, "assembly", "is an assembly file, which can reach anything"}}
			for _, f := range unexempted(productionEffectAdapters, used, gf.rel, assembly) {
				t.Errorf("%s:%d %s", gf.rel, f.line, f.msg)
			}
			return
		}
		file, err := parser.ParseFile(fset, gf.path, nil, parser.ParseComments)
		if err != nil {
			t.Errorf("parse %s: %v", gf.rel, err)
			return
		}
		for _, f := range unexempted(productionEffectAdapters, used, gf.rel, effectViolations(productionPolicy, fset, file)) {
			t.Errorf("%s:%d %s; route external effects through core/sim", gf.rel, f.line, f.msg)
		}
	})
	if checked == 0 {
		t.Fatal("checked zero Go files")
	}
	reportStaleExemptions(t, "adapter", productionEffectAdapters, used)
}

func TestUntaggedTestsAvoidRealSocketsAndRealTime(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	byDir := map[string][]goFile{}
	walkGoFiles(t, root, func(gf goFile) {
		if isTestSupport(gf.rel) && strings.HasSuffix(gf.rel, ".go") {
			dir := gopath.Dir(gf.rel)
			byDir[dir] = append(byDir[dir], gf)
		}
	})
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	checked := 0
	used := usage{}
	for _, dir := range dirs {
		var rels []string
		var files []*ast.File
		for _, gf := range byDir[dir] {
			file, err := parser.ParseFile(fset, gf.path, nil, parser.ParseComments)
			if err != nil {
				t.Errorf("parse %s: %v", gf.rel, err)
				continue
			}
			if !scannedByDefaultRun(file) {
				continue
			}
			rels = append(rels, gf.rel)
			files = append(files, file)
		}
		policy := testPolicy(dirBubbleInfo(files))
		for i, file := range files {
			checked++
			for _, f := range unexempted(realTimeTestFiles, used, rels[i], effectViolations(policy, fset, file)) {
				t.Errorf("%s:%d %s; use testing/synctest, sim.FakeClock, sim.MemNet or an in-memory disk, or tag the file for an integration run", rels[i], f.line, f.msg)
			}
		}
	}
	if checked == 0 {
		t.Fatal("checked zero test files")
	}
	reportStaleExemptions(t, "real-environment test", realTimeTestFiles, used)
}

func parseSnippet(t *testing.T, src string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return fset, file
}

func snippetFindings(t *testing.T, prod bool, src string) []effectFinding {
	t.Helper()
	fset, file := parseSnippet(t, src)
	if prod {
		return effectViolations(productionPolicy, fset, file)
	}
	return effectViolations(testPolicy(dirBubbleInfo([]*ast.File{file})), fset, file)
}

func findingKeys(fs []effectFinding) []string {
	keys := make([]string, 0, len(fs))
	for _, f := range fs {
		keys = append(keys, f.key)
	}
	return keys
}

func TestEffectCheckClassifiesSnippets(t *testing.T) {
	const prodHead = "package foo\nimport (\n\"context\"\n\"crypto/tls\"\n\"io/ioutil\"\n\"net\"\n\"net/http\"\n\"os\"\n\"syscall\"\nt \"time\"\n\"golang.org/x/sys/unix\"\n)\n"
	cases := []struct {
		name string
		prod bool
		src  string
		want string
	}{
		{"os.StartProcess", true, `func f() { _, _ = os.StartProcess("a", nil, &os.ProcAttr{}) }`, "os.StartProcess"},
		{"syscall.Exec", true, `func f() { _ = syscall.Exec("a", nil, nil) }`, "syscall.Exec"},
		{"syscall.Socket", true, `func f() { _, _ = syscall.Socket(1, 1, 0) }`, "syscall.Socket"},
		{"syscall.Open", true, `func f() { _, _ = syscall.Open("a", 0, 0) }`, "syscall.Open"},
		{"tls.Dial", true, `func f() { _, _ = tls.Dial("tcp", "a", nil) }`, "crypto/tls.Dial"},
		{"tls.Listen", true, `func f() { _, _ = tls.Listen("tcp", "a", nil) }`, "crypto/tls.Listen"},
		{"ListenAndServe method", true, `func f(srv *http.Server) { _ = srv.ListenAndServe() }`, "method:ListenAndServe"},
		{"ListenAndServeTLS method", true, `func f(srv *http.Server) { _ = srv.ListenAndServeTLS("", "") }`, "method:ListenAndServeTLS"},
		{"zero http.Client", true, `func f() { _, _ = (&http.Client{}).Get("http://a") }`, "net/http.Client{}"},
		{"new http.Client", true, `func f() { _ = new(http.Client) }`, "net/http.Client{}"},
		{"transport without dial", true, `func f() { _ = &http.Transport{} }`, "net/http.Transport{}"},
		{"http.DefaultClient", true, `func f() { _ = http.DefaultClient }`, "net/http.DefaultClient"},
		{"ioutil.ReadFile", true, `func f() { _, _ = ioutil.ReadFile("a") }`, "import:io/ioutil"},
		{"os.OpenRoot", true, `func f() { _, _ = os.OpenRoot("/") }`, "os.OpenRoot"},
		{"os.DirFS", true, `func f() { _ = os.DirFS("/") }`, "os.DirFS"},
		{"os.Chmod", true, `func f() { _ = os.Chmod("a", 0) }`, "os.Chmod"},
		{"os.Truncate", true, `func f() { _ = os.Truncate("a", 0) }`, "os.Truncate"},
		{"os.Link", true, `func f() { _ = os.Link("a", "b") }`, "os.Link"},
		{"os.Readlink", true, `func f() { _, _ = os.Readlink("a") }`, "os.Readlink"},
		{"net.ListenIP", true, `func f() { _, _ = net.ListenIP("ip4:icmp", nil) }`, "net.ListenIP"},
		{"net.DialIP", true, `func f() { _, _ = net.DialIP("ip4:icmp", nil, nil) }`, "net.DialIP"},
		{"net.LookupHost", true, `func f() { _, _ = net.LookupHost("a") }`, "net.LookupHost"},
		{"net.FileListener", true, `func f() { _, _ = net.FileListener(os.Stdin) }`, "net.FileListener"},
		{"net.Resolver", true, `func f() { _ = &net.Resolver{} }`, "net.Resolver"},
		{"unix import", true, `var _ = unix.Getpid`, "import:golang.org/x/sys/unix"},
		{"aliased time.Now", true, `func f() { _ = t.Now() }`, "time.Now"},
		{"function value time.Now", true, `func f() { g := t.Now; _ = g }`, "time.Now"},
		{"context.WithTimeout", true, `func f() { _, c := context.WithTimeout(context.Background(), 1); c() }`, "context.WithTimeout"},
		{"dot import", true, "package foo\nimport . \"time\"\nvar _ = Now", "dotimport:time"},
		{"dot import os", true, "package foo\nimport . \"os\"\nvar _ = ReadFile", "dotimport:os"},
		{"math/rand/v2", true, "package foo\nimport \"math/rand/v2\"\nvar _ = rand.IntN", "import:math/rand/v2"},
		{"aliased crypto/rand", true, "package foo\nimport cr \"crypto/rand\"\nvar _ = cr.Read", "import:crypto/rand"},
		{"os/exec", true, "package foo\nimport \"os/exec\"\nvar _ = exec.Command", "import:os/exec"},
		{"test socket", false, "package foo\nimport \"net\"\nfunc f() { _, _ = net.Listen(\"tcp\", \"127.0.0.1:0\") }", "net.Listen"},
		{"test net.Dial", false, "package foo\nimport \"net\"\nfunc f() { _, _ = net.Dial(\"tcp\", \"a\") }", "net.Dial"},
		{"test httptest.NewServer", false, "package foo\nimport \"net/http/httptest\"\nfunc f() { _ = httptest.NewServer(nil) }", "net/http/httptest.NewServer"},
		{"test NewTLSServer", false, "package foo\nimport \"net/http/httptest\"\nfunc f() { _ = httptest.NewTLSServer(nil) }", "net/http/httptest.NewTLSServer"},
		{"test NewUnstartedServer", false, "package foo\nimport \"net/http/httptest\"\nfunc f() { _ = httptest.NewUnstartedServer(nil) }", "net/http/httptest.NewUnstartedServer"},
		{"test time.Sleep", false, "package foo\nimport \"time\"\nfunc f() { time.Sleep(1) }", "time.Sleep"},
		{"test time.After", false, "package foo\nimport \"time\"\nfunc f() { <-time.After(1) }", "time.After"},
		{"test time.NewTimer", false, "package foo\nimport \"time\"\nfunc f() { time.NewTimer(1) }", "time.NewTimer"},
		{"test time.NewTicker", false, "package foo\nimport \"time\"\nfunc f() { time.NewTicker(1) }", "time.NewTicker"},
		{"test time.Tick", false, "package foo\nimport \"time\"\nfunc f() { _ = time.Tick(1) }", "time.Tick"},
		{"test time.AfterFunc", false, "package foo\nimport \"time\"\nfunc f() { time.AfterFunc(1, nil) }", "time.AfterFunc"},
		{"test aliased sleep", false, "package foo\nimport tm \"time\"\nfunc f() { tm.Sleep(1) }", "time.Sleep"},
		{"test sleep with synctest import only for sockets", false, "package foo\nimport (\"net\"; \"testing/synctest\")\nvar _ = synctest.Test\nfunc f() { _, _ = net.Listen(\"tcp\", \"a\") }", "net.Listen"},
		{"os.CopyFS writes real disk", true, "package foo\nimport (\"os\"; \"embed\")\nvar fsys embed.FS\nfunc f() error { return os.CopyFS(\"/tmp/x\", fsys) }", "os.CopyFS"},
		{"os.Stdin read", true, "package foo\nimport \"os\"\nfunc f(b []byte) { _, _ = os.Stdin.Read(b) }", "os.Stdin"},
		{"os.Stdout chmod", true, "package foo\nimport \"os\"\nfunc f() { _ = os.Stdout.Chmod(0o777) }", "os.File.Chmod"},
		{"os.Stdout chmod names the stream", true, "package foo\nimport \"os\"\nfunc f() { _ = os.Stdout.Chmod(0o777) }", "os.Stdout"},
		{"os.Stdin SyscallConn", true, "package foo\nimport \"os\"\nfunc f() { _, _ = os.Stdin.SyscallConn() }", "os.File.SyscallConn"},
		{"os.Getenv", true, "package foo\nimport \"os\"\nvar _ = os.Getenv(\"HOME\")", "os.Getenv"},
		{"os.LookupEnv", true, "package foo\nimport \"os\"\nvar _, _ = os.LookupEnv(\"HOME\")", "os.LookupEnv"},
		{"os.Exit", true, "package foo\nimport \"os\"\nfunc f() { os.Exit(1) }", "os.Exit"},
		{"os.Args", true, "package foo\nimport \"os\"\nvar _ = os.Args", "os.Args"},
		{"os.Stderr", true, "package foo\nimport \"os\"\nvar _ = os.Stderr", "os.Stderr"},
		{"zero-value http.Client var", true, "package foo\nimport \"net/http\"\nfunc f() { var c http.Client; _, _ = c.Get(\"http://example.com\") }", "net/http.Client{}"},
		{"zero-value http.Transport var", true, "package foo\nimport \"net/http\"\nfunc f() *http.Client { var tr http.Transport; return &http.Client{Transport: &tr} }", "net/http.Transport{}"},
		{"http.Client struct field by value", true, "package foo\nimport \"net/http\"\ntype s struct{ c http.Client }", "net/http.Client{}"},
		{"Transport with nil DialContext", true, "package foo\nimport \"net/http\"\nfunc f() *http.Client { return &http.Client{Transport: &http.Transport{DialContext: nil}} }", "net/http.Transport{}"},
		{"Client with nil Transport", true, "package foo\nimport \"net/http\"\nfunc f() *http.Client { return &http.Client{Transport: nil} }", "net/http.Client{}"},
		{"net.Interface Addrs", true, "package foo\nimport \"net\"\nfunc f() { _, _ = (&net.Interface{Index: 1}).Addrs() }", "net.Interface"},
		{"filepath.WalkDir", true, "package foo\nimport \"path/filepath\"\nfunc f() { _ = filepath.WalkDir(\"/etc\", nil) }", "path/filepath.WalkDir"},
		{"filepath.Walk", true, "package foo\nimport \"path/filepath\"\nfunc f() { _ = filepath.Walk(\"/etc\", nil) }", "path/filepath.Walk"},
		{"filepath.Glob", true, "package foo\nimport \"path/filepath\"\nfunc f() { _, _ = filepath.Glob(\"/etc/*\") }", "path/filepath.Glob"},
		{"filepath.EvalSymlinks", true, "package foo\nimport \"path/filepath\"\nfunc f() { _, _ = filepath.EvalSymlinks(\"/proc/self\") }", "path/filepath.EvalSymlinks"},
		{"filepath.Abs", true, "package foo\nimport \"path/filepath\"\nfunc f() { _, _ = filepath.Abs(\"x\") }", "path/filepath.Abs"},
		{"os/user", true, "package foo\nimport \"os/user\"\nfunc f() { _, _ = user.Current() }", "import:os/user"},
		{"os/signal", true, "package foo\nimport (\"os\"; \"os/signal\")\nfunc f() { signal.Notify(make(chan os.Signal, 1), os.Interrupt) }", "import:os/signal"},
		{"net/rpc Dial", true, "package foo\nimport \"net/rpc\"\nfunc f() { _, _ = rpc.Dial(\"tcp\", \"a:1\") }", "import:net/rpc"},
		{"time.Local", true, "package foo\nimport \"time\"\nfunc f(x time.Time) string { return x.In(time.Local).String() }", "time.Local"},
		{"x509 system pool", true, "package foo\nimport \"crypto/x509\"\nfunc f() { _, _ = x509.SystemCertPool() }", "crypto/x509.SystemCertPool"},
		{"hash/maphash seed", true, "package foo\nimport \"hash/maphash\"\nvar _ = maphash.MakeSeed()", "import:hash/maphash"},
		{"linkname nanotime", true, "package foo\nimport _ \"unsafe\"\n//go:linkname nanotime runtime.nanotime\nfunc nanotime() int64\nvar _ = nanotime()", "linkname"},
		{"containerd client", true, "package foo\nimport \"github.com/containerd/containerd/v2/client\"\nvar _ = client.New", "import:github.com/containerd/containerd/v2/client"},
		{"etcd clientv3", true, "package foo\nimport clientv3 \"go.etcd.io/etcd/client/v3\"\nvar _ = clientv3.New", "import:go.etcd.io/etcd/client/v3"},
		{"grpc", true, "package foo\nimport \"google.golang.org/grpc\"\nvar _ = grpc.NewClient", "import:google.golang.org/grpc"},
		{"test containerd client", false, "package foo\nimport \"github.com/containerd/containerd/v2/client\"\nvar _ = client.New", "import:github.com/containerd/containerd/v2/client"},
		{"test os.WriteFile", false, "package foo\nimport \"os\"\nfunc f() { _ = os.WriteFile(\"/tmp/x\", nil, 0o644) }", "os.WriteFile"},
		{"test os.ReadFile", false, "package foo\nimport \"os\"\nfunc f() { _, _ = os.ReadFile(\"/etc/passwd\") }", "os.ReadFile"},
		{"test os.Getenv", false, "package foo\nimport \"os\"\nvar _ = os.Getenv(\"X\")", "os.Getenv"},
		{"test filepath.WalkDir", false, "package foo\nimport \"path/filepath\"\nfunc f() { _ = filepath.WalkDir(\".\", nil) }", "path/filepath.WalkDir"},
		{"test sleep outside bubble in a file that also uses synctest", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc TestA(t *testing.T) { synctest.Test(t, func(*testing.T) {}) }\nfunc TestB(t *testing.T) { time.Sleep(time.Second) }", "time.Sleep"},
		{"test sleep after a bubble in the same function", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc TestA(t *testing.T) { synctest.Test(t, func(*testing.T) {}); time.Sleep(time.Second) }", "time.Sleep"},
		{"test sleep in a goroutine beside a bubble literal", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc TestA(t *testing.T) { go func() { time.Sleep(1) }(); synctest.Test(t, func(*testing.T) {}) }", "time.Sleep"},
		{"test synctest passed a named function", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc body(*testing.T) { time.Sleep(1) }\nfunc TestA(t *testing.T) { synctest.Test(t, body) }", "time.Sleep"},
		{"test sim.RealNet", false, "package foo\nimport (\"net/http\"; \"x/core/sim\")\nfunc f() { l, _ := sim.RealNet().Listen(\"tcp\", \"127.0.0.1:0\"); _ = (&http.Server{}).Serve(l) }", "core/sim.RealNet"},
		{"test sim.RealClock", false, "package foo\nimport \"x/core/sim\"\nfunc f() { <-sim.RealClock().After(1) }", "core/sim.RealClock"},
		{"test sim.OSDisk", false, "package foo\nimport \"x/core/sim\"\nvar _ = sim.OSDisk()", "core/sim.OSDisk"},
		{"test sim.CryptoRand", false, "package foo\nimport s \"github.com/Kaikei-e/Nayutar/core/sim\"\nvar _ = s.CryptoRand()", "core/sim.CryptoRand"},
		{"test context.WithTimeout real", false, "package foo\nimport (\"context\"; \"time\")\nfunc f() { ctx, c := context.WithTimeout(context.Background(), time.Second); defer c(); <-ctx.Done() }", "context.WithTimeout"},
		{"test context.WithDeadline real", false, "package foo\nimport (\"context\"; \"time\")\nfunc f() { _, c := context.WithDeadline(context.Background(), time.Time{}); c() }", "context.WithDeadline"},
		{"test timeout outside bubble in a file that also uses synctest", false, "package foo\nimport (\"context\"; \"testing\"; \"testing/synctest\")\nfunc TestA(t *testing.T) { synctest.Test(t, func(*testing.T) {}) }\nfunc TestB(t *testing.T) { _, c := context.WithTimeout(context.Background(), 1); c() }", "context.WithTimeout"},
		{"test net.Listen inside a bubble", false, "package foo\nimport (\"net\"; \"testing\"; \"testing/synctest\")\nfunc TestA(t *testing.T) { synctest.Test(t, func(*testing.T) { _, _ = net.Listen(\"tcp\", \"127.0.0.1:0\") }) }", "net.Listen"},
		{"test helper called from outside a bubble", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc TestA(t *testing.T) { synctest.Test(t, func(*testing.T) { wait() }) }\nfunc TestB(t *testing.T) { wait() }\nfunc wait() { time.Sleep(1) }", "time.Sleep"},
		{"test helper referenced as a value", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc TestA(t *testing.T) { synctest.Test(t, func(*testing.T) { wait() }) }\nvar hook = wait\nfunc wait() { time.Sleep(1) }", "time.Sleep"},
		{"test method sleeping", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\ntype w struct{}\nfunc (w) wait() { time.Sleep(1) }\nfunc TestA(t *testing.T) { synctest.Test(t, func(*testing.T) { w{}.wait() }) }", "time.Sleep"},
		{"test conditional bubble helper", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc run(t *testing.T, on bool, fn func(*testing.T)) { if on { synctest.Test(t, fn); return }; fn(t) }\nfunc TestA(t *testing.T) { run(t, false, func(t *testing.T) { time.Sleep(1) }) }", "time.Sleep"},
		{"import C", true, "package foo\n// #include <time.h>\nimport \"C\"\nfunc f() { _ = C.time(nil) }", "import:C"},
		{"import plugin", true, "package foo\nimport \"plugin\"\nfunc f() { _, _ = plugin.Open(\"/x.so\") }", "import:plugin"},
		{"blank pprof import", true, "package foo\nimport _ \"net/http/pprof\"\n", "import:net/http/pprof"},
		{"http.ServeFile", true, "package foo\nimport \"net/http\"\nfunc f(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, \"/etc/passwd\") }", "net/http.ServeFile"},
		{"http.Dir", true, "package foo\nimport \"net/http\"\nvar _ = http.FileServer(http.Dir(\"/\"))", "net/http.Dir"},
		{"http.FileServer", true, "package foo\nimport \"net/http\"\nvar _ = http.FileServer(nil)", "net/http.FileServer"},
		{"http.FS", true, "package foo\nimport (\"net/http\"; \"os\")\nvar _ = http.FS(os.DirFS(\"/\"))", "net/http.FS"},
		{"http.ProxyFromEnvironment", true, "package foo\nimport (\"context\"; \"net\"; \"net/http\")\nfunc f(d func(context.Context, string, string) (net.Conn, error)) *http.Transport { return &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: d} }", "net/http.ProxyFromEnvironment"},
		{"http.ListenAndServe function", true, "package foo\nimport \"net/http\"\nfunc f() { _ = http.ListenAndServe(\":1\", nil) }", "net/http.ListenAndServe"},
		{"http.DefaultServeMux", true, "package foo\nimport \"net/http\"\nvar _ = http.DefaultServeMux", "net/http.DefaultServeMux"},
		{"tls.LoadX509KeyPair", true, "package foo\nimport \"crypto/tls\"\nfunc f() { _, _ = tls.LoadX509KeyPair(\"/etc/a\", \"/etc/b\") }", "crypto/tls.LoadX509KeyPair"},
		{"tls.Dialer method value", true, "package foo\nimport \"crypto/tls\"\nvar _ = (*tls.Dialer).DialContext", "crypto/tls.Dialer"},
		{"Transport with only DialTLSContext", true, "package foo\nimport (\"context\"; \"net\"; \"net/http\")\nfunc f(d func(context.Context, string, string) (net.Conn, error)) *http.Client { return &http.Client{Transport: &http.Transport{DialTLSContext: d}} }", "net/http.Transport{}"},
		{"Transport with only DialTLS", true, "package foo\nimport \"net/http\"\nvar _ = &http.Transport{DialTLS: nil}", "net/http.Transport{}"},
		{"Client with typed nil Transport", true, "package foo\nimport \"net/http\"\nvar _ = &http.Client{Transport: http.RoundTripper(nil)}", "net/http.Client{}"},
		{"Transport with typed nil DialContext", true, "package foo\nimport \"net/http\"\nvar _ = &http.Transport{DialContext: (func())(nil)}", "net/http.Transport{}"},
		{"log.Fatal", true, "package foo\nimport \"log\"\nfunc f() { log.Fatal(\"x\") }", "log.Fatal"},
		{"log.Panicf", true, "package foo\nimport \"log\"\nfunc f() { log.Panicf(\"x\") }", "log.Panicf"},
		{"time.Time.Local method", true, "package foo\nimport \"time\"\nvar _ = time.Unix(0, 0).Local()", "method:Local"},
		{"test http.ServeFile", false, "package foo\nimport \"net/http\"\nfunc f(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, \"x\") }", "net/http.ServeFile"},
		{"test http.Get", false, "package foo\nimport \"net/http\"\nfunc f() { _, _ = http.Get(\"http://a\") }", "net/http.Get"},
		{"test tls.Dial", false, "package foo\nimport \"crypto/tls\"\nfunc f() { _, _ = tls.Dial(\"tcp\", \"a:1\", nil) }", "crypto/tls.Dial"},
		{"test import C", false, "package foo\nimport \"C\"\nfunc f() { _ = C.time(nil) }", "import:C"},
		{"test local var shadows bubble helper", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc inBubble(t *testing.T, fn func(*testing.T)) { synctest.Test(t, fn) }\nfunc TestA(t *testing.T) { inBubble := func(t *testing.T, fn func(*testing.T)) { fn(t) }; inBubble(t, func(t *testing.T) { time.Sleep(time.Second) }) }", "time.Sleep"},
		{"test parameter shadows bubble helper", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc inBubble(t *testing.T, fn func(*testing.T)) { synctest.Test(t, fn) }\nfunc g(inBubble func(*testing.T, func(*testing.T)), t *testing.T) { inBubble(t, func(t *testing.T) { time.Sleep(time.Second) }) }", "time.Sleep"},
		{"test helper cycle without a bubble", false, "package foo\nimport \"time\"\nfunc a() { b() }\nfunc b() { a(); time.Sleep(1) }", "time.Sleep"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := tc.src
			if tc.prod && !strings.HasPrefix(src, "package") {
				src = prodHead + src
			}
			got := snippetFindings(t, tc.prod, src)
			for _, f := range got {
				if f.key == tc.want {
					return
				}
			}
			t.Fatalf("no finding with key %q in %v", tc.want, got)
		})
	}

	clean := []struct {
		name string
		prod bool
		src  string
	}{
		{"sim clock", true, "package foo\nimport \"time\"\nimport \"context\"\ntype C interface{ Now() time.Time }\nfunc f(c C, ctx context.Context) time.Duration { _, cancel := context.WithCancel(ctx); cancel(); return c.Now().Sub(time.Unix(0, 0)) + time.Second }"},
		{"os constants and errors", true, "package foo\nimport \"os\"\nimport \"errors\"\nvar _ = errors.Is(nil, os.ErrNotExist)\nvar _ = os.O_RDONLY | os.O_CREATE\nvar _ os.FileMode = os.ModePerm\nvar _ *os.File\nvar _ = os.IsNotExist"},
		{"net errors and pure helpers", true, "package foo\nimport \"net\"\nvar _ = net.ErrClosed\nvar _ net.IP = net.ParseIP(\"1.2.3.4\")\nvar _ net.Listener\nvar _, _ = net.Pipe()\nvar _, _, _ = net.ParseCIDR(\"1.2.3.0/24\")"},
		{"syscall constants", true, "package foo\nimport \"syscall\"\nvar _ = syscall.SIGTERM\nvar _ error = syscall.ENOSPC\nvar _ syscall.Signal"},
		{"shadowed package name", true, "package foo\nfunc f(time struct{ Now func() int }) int { return time.Now() }"},
		{"test uses net.Pipe", false, "package foo\nimport \"net\"\nvar _, _ = net.Pipe()"},
		{"http with injected transport", true, "package foo\nimport \"net/http\"\nfunc f(d func() error) *http.Client { return &http.Client{Transport: &http.Transport{DialContext: dial}} }\nvar dial func(ctx any, n, a string) (any, error)"},
		{"http client held by pointer", true, "package foo\nimport \"net/http\"\ntype s struct{ c *http.Client }\nfunc f(c *http.Client) *http.Client { var d *http.Client; _ = d; return c }"},
		{"http.Server Serve on an injected listener", true, "package foo\nimport (\"net\"; \"net/http\")\nfunc f(l net.Listener) { _ = (&http.Server{}).Serve(l) }"},
		{"timer Reset on an injected timer", true, "package foo\nimport \"time\"\nfunc f(t *time.Timer) { t.Reset(time.Second) }"},
		{"log.Printf only writes stderr", true, "package foo\nimport \"log\"\nfunc f() { log.Printf(\"x\") }"},
		{"os.File type and Write on an injected file", true, "package foo\nimport \"os\"\nfunc f(f *os.File) { _, _ = f.Write(nil); _ = f.Chmod(0) }"},
		{"filepath pure helpers", true, "package foo\nimport \"path/filepath\"\nvar _ = filepath.Join(\"a\", \"b\")\nvar _ = filepath.Clean(\"a\")\nvar _, _ = filepath.Rel(\"a\", \"b\")"},
		{"grpc status and codes", true, "package foo\nimport (\"google.golang.org/grpc/codes\"; \"google.golang.org/grpc/status\")\nvar _ = status.Error(codes.Unknown, \"x\")"},
		{"plain comment mentioning linkname", true, "package foo\n// go:linkname is discussed here\nvar _ = 1"},
		{"test time.Now busy wait is not policed", false, "package foo\nimport \"time\"\nfunc f() { d := time.Now().Add(time.Second); for time.Now().Before(d) {} }"},
		{"test sleeps inside synctest.Test literal", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc TestA(t *testing.T) { synctest.Test(t, func(t *testing.T) { time.Sleep(1); <-time.After(1); t.Run(\"x\", func(t *testing.T) { go func() { time.Sleep(1) }() }) }) }"},
		{"test timeouts inside a bubble", false, "package foo\nimport (\"context\"; \"time\"; \"testing\"; \"testing/synctest\")\nfunc TestA(t *testing.T) { synctest.Test(t, func(t *testing.T) { ctx, c := context.WithTimeout(context.Background(), time.Second); defer c(); <-ctx.Done() }) }"},
		{"test bubble helper passes its function to synctest.Test", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc inBubble(t *testing.T, fn func(*testing.T)) { t.Helper(); synctest.Test(t, fn) }\nfunc TestA(t *testing.T) { inBubble(t, func(t *testing.T) { time.Sleep(1) }) }"},
		{"test bubble helper layered on a helper", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc inBubble(t *testing.T, fn func(*testing.T)) { synctest.Test(t, fn) }\nfunc scenario(t *testing.T, body func(*testing.T)) { inBubble(t, body) }\nfunc TestA(t *testing.T) { scenario(t, func(t *testing.T) { time.Sleep(1) }) }"},
		{"test helper only called from bubbles", false, "package foo\nimport (\"time\"; \"testing\"; \"testing/synctest\")\nfunc TestA(t *testing.T) { synctest.Test(t, func(*testing.T) { outer() }) }\nfunc outer() { wait() }\nfunc wait() { time.Sleep(1) }"},
		{"test aliased synctest import", false, "package foo\nimport (\"time\"; \"testing\"; st \"testing/synctest\")\nfunc TestA(t *testing.T) { st.Test(t, func(*testing.T) { time.Sleep(1) }) }"},
		{"http pure names", true, "package foo\nimport (\"crypto/tls\"; \"net/http\")\nvar _ = http.StatusOK\nvar _ = http.MethodGet\nvar _ http.Handler = http.HandlerFunc(nil)\nvar _ = http.ErrServerClosed\nvar _ = &tls.Config{MinVersion: tls.VersionTLS13}\nvar _ tls.Certificate\nvar _, _ = http.NewRequestWithContext(nil, \"GET\", \"x\", nil)\nvar _ = http.NewServeMux()"},
		{"http transport with DialContext and TLS", true, "package foo\nimport \"net/http\"\nvar _ = &http.Transport{DialContext: dial, DialTLSContext: dial}\nvar dial func(ctx any, n, a string) (any, error)"},
		{"time.Local-free time use", true, "package foo\nimport \"time\"\nvar _ = time.Unix(0, 0).UTC()"},
		{"test uses http types and httptest recorder", false, "package foo\nimport (\"net/http\"; \"net/http/httptest\")\nvar _ = httptest.NewRecorder()\nvar _ http.Handler\nvar _ = http.StatusOK"},
		{"test non-bubble run in another package of the directory", false, "package foo_test\nimport (\"time\"; \"testing\")\nfunc run(t *testing.T, fn func(*testing.T)) { fn(t) }\nfunc TestA(t *testing.T) { _ = time.Second }"},
		{"test uses net.Pipe and os constants", false, "package foo\nimport (\"net\"; \"os\")\nvar _, _ = net.Pipe()\nvar _ = os.ErrNotExist\nvar _ = os.O_RDONLY"},
	}
	for _, tc := range clean {
		t.Run("clean/"+tc.name, func(t *testing.T) {
			if got := snippetFindings(t, tc.prod, tc.src); len(got) != 0 {
				t.Fatalf("unexpected findings: %v", got)
			}
		})
	}
}

func TestBubbleHelpersAcrossFiles(t *testing.T) {
	const helperFile = "package foo\nimport (\"testing\"; \"testing/synctest\"; \"time\")\nfunc runBubble(t *testing.T, fn func(*testing.T)) { synctest.Test(t, fn) }\nfunc settle() { time.Sleep(1) }\n"
	run := func(testFile string) []string {
		fset := token.NewFileSet()
		var files []*ast.File
		for i, src := range []string{helperFile, testFile} {
			f, err := parser.ParseFile(fset, "f"+strconv.Itoa(i)+".go", src, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		policy := testPolicy(dirBubbleInfo(files))
		var out []string
		for _, f := range files {
			out = append(out, findingKeys(effectViolations(policy, fset, f))...)
		}
		return out
	}
	const head = "package foo\nimport (\"testing\"; \"time\")\nvar _ = time.Second\n"
	if got := run(head + "func TestA(t *testing.T) { runBubble(t, func(t *testing.T) { time.Sleep(1); settle() }) }"); len(got) != 0 {
		t.Errorf("bubble helper and bubble-only helper in other files flagged: %v", got)
	}
	if got := run(head + "func TestA(t *testing.T) { runBubble(t, func(t *testing.T) { settle() }) }\nfunc TestB(t *testing.T) { time.Sleep(1) }"); len(got) != 1 || got[0] != "time.Sleep" {
		t.Errorf("sleep outside the bubble: got %v, want [time.Sleep]", got)
	}
	if got := run(head + "func TestA(t *testing.T) { settle() }"); len(got) != 1 || got[0] != "time.Sleep" {
		t.Errorf("helper called outside a bubble: got %v, want [time.Sleep]", got)
	}
}

func TestBubbleHelpersAreKeyedByPackage(t *testing.T) {
	internal := "package foo\nimport (\"testing\"; \"testing/synctest\")\nfunc run(t *testing.T, fn func(*testing.T)) { synctest.Test(t, fn) }\nfunc TestA(t *testing.T) { run(t, func(t *testing.T) {}) }\n"
	external := "package foo_test\nimport (\"testing\"; \"time\")\nfunc run(t *testing.T, fn func(*testing.T)) { fn(t) }\nfunc TestB(t *testing.T) { run(t, func(t *testing.T) { time.Sleep(time.Second) }) }\n"
	fset := token.NewFileSet()
	var files []*ast.File
	for i, src := range []string{internal, external} {
		f, err := parser.ParseFile(fset, "f"+strconv.Itoa(i)+".go", src, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	policy := testPolicy(dirBubbleInfo(files))
	if got := findingKeys(effectViolations(policy, fset, files[0])); len(got) != 0 {
		t.Errorf("bubble package flagged: %v", got)
	}
	if got := findingKeys(effectViolations(policy, fset, files[1])); len(got) != 1 || got[0] != "time.Sleep" {
		t.Errorf("same-named non-bubble helper in another package: got %v, want [time.Sleep]", got)
	}
}

func TestWalkGoFilesCoversTheWholeModule(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"a.go", "config/c.go", "core/x/asm_amd64.s", "docs/d_test.go",
		".hidden/h.go", "vendor/v.go", "core/testdata/t.go", "_ignored/i.go", "spec/.lake/p/l.go", "spec/s.go",
		"nested/go.mod", "nested/n.go", "pkg/readme.md",
	} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	walkGoFiles(t, root, func(gf goFile) { got = append(got, gf.rel) })
	sort.Strings(got)
	want := []string{"a.go", "config/c.go", "core/x/asm_amd64.s", "docs/d_test.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("walked %v, want %v", got, want)
	}
}

func TestBuildTagScanning(t *testing.T) {
	cases := []struct {
		constraint string
		want       bool
	}{
		{"", true},
		{"race", true},
		{"!race", true},
		{"linux", true},
		{"windows", true},
		{"linux && amd64", true},
		{"linux && windows", true},
		{"ignore", true},
		{"etcd", false},
		{"drt", false},
		{"containerd", false},
		{"nftables", false},
		{"etcd && linux", false},
		{"drt || etcd", false},
		{"etcd || race", true},
		{"!etcd", true},
		{"!etcd && race", true},
		{"etcd && !etcd", false},
		{"race && (etcd || drt)", false},
	}
	for _, tc := range cases {
		src := "package foo\n"
		if tc.constraint != "" {
			src = "// Copyright notice.\n\n//go:build " + tc.constraint + "\n\npackage foo\n"
		}
		_, file := parseSnippet(t, src)
		if got := scannedByDefaultRun(file); got != tc.want {
			t.Errorf("%q: scanned = %v, want %v", tc.constraint, got, tc.want)
		}
	}
	_, file := parseSnippet(t, "package foo\n// go:build etcd\n")
	if !scannedByDefaultRun(file) {
		t.Error("a build line after the package clause must not exclude the file")
	}
}
