package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestLeanBuildIsSeparateFromGoCI(t *testing.T) {
	ci, ciErr := os.ReadFile(repoFile(t, ".github/workflows/ci.yml"))
	lean, leanErr := os.ReadFile(repoFile(t, ".github/workflows/lean.yml"))
	if ciErr != nil {
		t.Error(ciErr)
	}
	if leanErr != nil {
		t.Error(leanErr)
	}
	if ciErr == nil {
		body := string(ci)
		if strings.Contains(body, "lake") {
			t.Error("Go CI must not run lake; Lean builds belong in the Lean workflow")
		}
		if strings.Contains(body, "continue-on-error") || strings.Contains(body, "|| true") {
			t.Error("ci.yml must not swallow job or step failures")
		}
		requireRunsOnEveryChange(t, "ci.yml", body)
		decisions := extractJob(body, "decisions:")
		if decisions == "" {
			t.Error("ci.yml has no decisions job")
		} else {
			for _, needle := range []string{
				`--immutable-since "origin/${GITHUB_BASE_REF}"`,
				`--immutable-since "${BEFORE_SHA}"`,
			} {
				if !strings.Contains(decisions, needle) {
					t.Errorf("decisions job missing %s", needle)
				}
			}
		}
		harness := extractJob(body, "agent-harness:")
		if harness == "" {
			t.Error("ci.yml has no agent-harness job")
		} else {
			for _, needle := range []string{
				"python3 scripts/tests/test-agent-harness.py",
				"python3 scripts/agent-harness/harness_selfcheck.py",
			} {
				if !strings.Contains(harness, needle) {
					t.Errorf("agent-harness job missing %s", needle)
				}
			}
		}
		job := extractJob(body, "go:")
		if job == "" {
			t.Error("ci.yml has no go job")
		} else {
			hasTimeout20 := false
			for _, line := range strings.Split(job, "\n") {
				if stripYAMLComment(line) == "    timeout-minutes: 20" {
					hasTimeout20 = true
					break
				}
			}
			if !hasTimeout20 {
				t.Error("go job must set job-level timeout-minutes: 20")
			}
			if !strings.Contains(job, "go vet ./...") {
				t.Error("go job must run go vet ./...")
			}
			if !strings.Contains(job, "go test ./...") {
				t.Error("go job must run go test ./...")
			}
			if !strings.Contains(job, "go test -race ./...") {
				t.Error("go job must run go test -race ./...")
			}
			if !strings.Contains(job, "1.27.3") {
				t.Error("go job must test the 1.27.3 floor")
			}
			if !strings.Contains(job, `"1.27"`) {
				t.Error(`go job must track the newest 1.27 release with "1.27"`)
			}
			if strings.Contains(job, "lake") {
				t.Error("go job must not build Lean")
			}
		}
	}
	if leanErr == nil {
		body := string(lean)
		if !strings.Contains(body, "lake build") {
			t.Error("lean workflow must run lake build")
		}
		if !strings.Contains(body, "lake comparator") {
			t.Error("lean workflow must run lake comparator")
		}
		if strings.Contains(body, "--inadvisably-no-sandbox") {
			t.Error("lean workflow must not pass --inadvisably-no-sandbox")
		}
		checkIdx := strings.Index(body, "go build -o \"${RUNNER_TEMP}/check-protected-theorems\" ./cmd/check-protected-theorems")
		compIdx := strings.Index(body, "lake comparator")
		if checkIdx < 0 || compIdx < 0 || checkIdx > compIdx {
			t.Error("lean workflow must run check-protected-theorems before lake comparator")
		}
		if !strings.Contains(body, "bash scripts/tests/test-comparator-rejections.sh") {
			t.Error("lean workflow must run the comparator rejection cases")
		}
		if !strings.Contains(body, "working-directory: spec") {
			t.Error("lake build must run in spec/")
		}
		requireRunsOnEveryChange(t, "lean.yml", body)
		if strings.Contains(body, "continue-on-error") || strings.Contains(body, "|| true") {
			t.Error("lean workflow must not swallow lake build failures")
		}
		if !strings.Contains(body, "sha256sum --check") {
			t.Error("lean workflow must verify downloaded toolchain binaries")
		}
	}
}

func TestLkalJobDecodesTheDevelopmentDeclaration(t *testing.T) {
	job := extractJob(readRepoFile(t, ".github/workflows/ci.yml"), "lkal:")
	if job == "" {
		t.Fatal("ci.yml has no lkal job")
	}
	for _, needle := range []string{
		"go install github.com/Kaikei-e/LKAL/cmd/lkal@v0.2.0",
		"lkal schema check",
		"lkal check --schema",
		"name: Decode the development etcd declaration",
		"set -euo pipefail",
	} {
		if !strings.Contains(job, needle) {
			t.Errorf("lkal job missing %s", needle)
		}
	}
	for _, want := range []string{
		`go test -count=1 -v -run '^TestDevDeclarationDecodes$' ./cmd/dev-etcd | tee "${RUNNER_TEMP}/dev-declaration.log"`,
		`grep -q '^--- PASS: TestDevDeclarationDecodes' "${RUNNER_TEMP}/dev-declaration.log"`,
	} {
		if !hasTrimmedLine(job, want) {
			t.Errorf("lkal job must contain the exact line %q", want)
		}
	}
	step := job[strings.Index(job, "name: Decode the development etcd declaration"):]
	if !strings.Contains(step, "set -euo pipefail") {
		t.Error("the decode step must run with set -euo pipefail")
	}
	if !strings.Contains(readRepoFile(t, "cmd/dev-etcd/main_test.go"), "func TestDevDeclarationDecodes(") {
		t.Error("cmd/dev-etcd/main_test.go must define TestDevDeclarationDecodes; the lkal job runs it by name")
	}
}

func TestVulnerabilityScanIsPinnedAndNotSwallowed(t *testing.T) {
	body := readRepoFile(t, ".github/workflows/ci.yml")
	job := extractJob(body, "vulncheck:")
	if job == "" {
		t.Fatal("ci.yml has no vulncheck job")
	}
	for _, needle := range []string{
		`go-version: "1.27"`,
		"check-latest: true",
		"persist-credentials: false",
		"timeout-minutes",
		"contents: read",
	} {
		if !strings.Contains(job, needle) {
			t.Errorf("vulncheck job missing %s", needle)
		}
	}
	for _, want := range []string{
		"run: go install golang.org/x/vuln/cmd/govulncheck@v1.10.0",
		"run: govulncheck -test ./...",
		"run: govulncheck -test -tags drt,etcd ./...",
		"run: govulncheck -test -tags drt,etcd,containerd,nftables ./...",
	} {
		if !hasTrimmedLine(job, want) {
			t.Errorf("vulncheck job must contain the exact line %q", want)
		}
	}
	if strings.Contains(job, "@latest") {
		t.Error("vulncheck job must pin govulncheck to a version")
	}
	for _, swallow := range []string{"continue-on-error", "|| true", "|| :", "|| echo", "|| exit 0"} {
		if strings.Contains(job, swallow) {
			t.Errorf("vulncheck job must not swallow findings (%s)", swallow)
		}
	}
	for _, mode := range []string{"-format", "-json"} {
		if strings.Contains(job, mode) {
			t.Errorf("vulncheck job must keep the default text output; %s exits 0 on findings", mode)
		}
	}
}

func TestDependabotPinsContainerdTo23LTS(t *testing.T) {
	if err := checkDependabotContainerdPin(readRepoFile(t, ".github/dependabot.yml")); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDependabotContainerdPin(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr bool
	}{
		{
			name: "multiline list under unquoted gomod",
			content: `version: 2
updates:
  - package-ecosystem: gomod # Go dependencies
    directory: /
    ignore:
      # Pin containerd to the 2.3 LTS line.
      - dependency-name: "github.com/containerd/containerd/v2"
        update-types:
          - "version-update:semver-major"
          - "version-update:semver-minor"
  - package-ecosystem: github-actions
    directory: /
`,
		},
		{
			name: "inline list under double-quoted gomod with unrelated ignore rule",
			content: `version: 2
updates:
  - package-ecosystem: "gomod"
    ignore:
      - dependency-name: "example.com/other"
      - dependency-name: 'github.com/containerd/containerd/v2'
        update-types: ["version-update:semver-major", "version-update:semver-minor"]
`,
		},
		{
			name: "single-quoted gomod and unquoted module",
			content: `version: 2
updates:
  - package-ecosystem: 'gomod'
    ignore:
      - dependency-name: github.com/containerd/containerd/v2
        update-types: [version-update:semver-minor, version-update:semver-major]
`,
		},
		{
			name: "commented out rule",
			content: `version: 2
updates:
  - package-ecosystem: gomod
    ignore:
      # - dependency-name: "github.com/containerd/containerd/v2"
      #   update-types:
      #     - "version-update:semver-major"
      #     - "version-update:semver-minor"
`,
			wantErr: true,
		},
		{
			name: "ignore placed under github-actions instead of gomod",
			content: `version: 2
updates:
  - package-ecosystem: gomod
    directory: /
  - package-ecosystem: github-actions
    ignore:
      - dependency-name: "github.com/containerd/containerd/v2"
        update-types: ["version-update:semver-major", "version-update:semver-minor"]
`,
			wantErr: true,
		},
		{
			name: "dependency-name and update-types split across separate items",
			content: `version: 2
updates:
  - package-ecosystem: gomod
    ignore:
      - dependency-name: "github.com/containerd/containerd/v2"
      - update-types:
          - "version-update:semver-major"
          - "version-update:semver-minor"
`,
			wantErr: true,
		},
		{
			name: "dependency-name without update-types blocks patches",
			content: `version: 2
updates:
  - package-ecosystem: gomod
    ignore:
      - dependency-name: "github.com/containerd/containerd/v2"
`,
			wantErr: true,
		},
		{
			name: "semver-minor missing",
			content: `version: 2
updates:
  - package-ecosystem: gomod
    ignore:
      - dependency-name: "github.com/containerd/containerd/v2"
        update-types:
          - "version-update:semver-major"
`,
			wantErr: true,
		},
		{
			name: "semver-patch included",
			content: `version: 2
updates:
  - package-ecosystem: gomod
    ignore:
      - dependency-name: "github.com/containerd/containerd/v2"
        update-types:
          - "version-update:semver-major"
          - "version-update:semver-minor"
          - "version-update:semver-patch"
`,
			wantErr: true,
		},
		{
			name: "versions field included",
			content: `version: 2
updates:
  - package-ecosystem: gomod
    ignore:
      - dependency-name: "github.com/containerd/containerd/v2"
        update-types:
          - "version-update:semver-major"
          - "version-update:semver-minor"
        versions:
          - ">= 2.4.0"
`,
			wantErr: true,
		},
		{
			name: "partial module name match",
			content: `version: 2
updates:
  - package-ecosystem: gomod
    ignore:
      - dependency-name: "github.com/containerd/containerd/v2/client"
        update-types: ["version-update:semver-major", "version-update:semver-minor"]
`,
			wantErr: true,
		},
		{
			name: "ignore indented under commit-message",
			content: `version: 2
updates:
  - package-ecosystem: gomod
    commit-message:
      prefix: "chore(deps)"
      ignore:
        - dependency-name: "github.com/containerd/containerd/v2"
          update-types: ["version-update:semver-major", "version-update:semver-minor"]
`,
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkDependabotContainerdPin(tc.content)
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

type dependabotIgnoreItem struct {
	dependencyName string
	hasUpdateTypes bool
	updateTypes    []string
	hasVersions    bool
}

func checkDependabotContainerdPin(content string) error {
	const targetModule = "github.com/containerd/containerd/v2"

	var items []dependabotIgnoreItem
	var current *dependabotIgnoreItem
	flushItem := func() {
		if current != nil {
			items = append(items, *current)
			current = nil
		}
	}

	inUpdates := false
	inGomod := false
	inIgnore := false
	inUpdateTypes := false

	applyItemField := func(item *dependabotIgnoreItem, key, val string) {
		inUpdateTypes = false
		switch key {
		case "dependency-name":
			item.dependencyName = unquoteYAML(val)
		case "versions":
			item.hasVersions = true
		case "update-types":
			item.hasUpdateTypes = true
			if val == "" {
				inUpdateTypes = true
				return
			}
			if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
				inner := strings.TrimSpace(val[1 : len(val)-1])
				if inner != "" {
					for elem := range strings.SplitSeq(inner, ",") {
						if u := unquoteYAML(elem); u != "" {
							item.updateTypes = append(item.updateTypes, u)
						}
					}
				}
			}
		}
	}

	for rawLine := range strings.SplitSeq(content, "\n") {
		line := stripYAMLComment(rawLine)
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingSpaces(line)

		if indent == 0 {
			flushItem()
			inGomod = false
			inIgnore = false
			inUpdateTypes = false
			inUpdates = line == "updates:"
			continue
		}
		if !inUpdates {
			continue
		}

		if strings.HasPrefix(line, "  - ") {
			flushItem()
			inGomod = false
			inIgnore = false
			inUpdateTypes = false
			if k, v, ok := strings.Cut(strings.TrimSpace(line[len("  - "):]), ":"); ok {
				if strings.TrimSpace(k) == "package-ecosystem" && unquoteYAML(v) == "gomod" {
					inGomod = true
				}
			}
			continue
		}
		if indent <= 2 {
			flushItem()
			inGomod = false
			inIgnore = false
			inUpdateTypes = false
			continue
		}
		if !inGomod {
			continue
		}

		if indent == 4 {
			flushItem()
			inUpdateTypes = false
			inIgnore = line == "    ignore:"
			continue
		}
		if !inIgnore {
			continue
		}

		if line == "      -" || strings.HasPrefix(line, "      - ") {
			flushItem()
			inUpdateTypes = false
			current = &dependabotIgnoreItem{}
			if len(line) > len("      - ") {
				if k, v, ok := strings.Cut(strings.TrimSpace(line[len("      - "):]), ":"); ok {
					applyItemField(current, strings.TrimSpace(k), strings.TrimSpace(v))
				}
			}
			continue
		}
		if current == nil {
			continue
		}

		if indent == 8 && !strings.HasPrefix(line[8:], "- ") {
			if k, v, ok := strings.Cut(line[8:], ":"); ok {
				applyItemField(current, strings.TrimSpace(k), strings.TrimSpace(v))
			}
			continue
		}
		if inUpdateTypes && indent >= 8 {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") {
				if u := unquoteYAML(trimmed[2:]); u != "" {
					current.updateTypes = append(current.updateTypes, u)
				}
			}
		}
	}
	flushItem()

	wantTypes := []string{"version-update:semver-major", "version-update:semver-minor"}
	found := false
	for _, item := range items {
		if item.dependencyName != targetModule {
			continue
		}
		if item.hasVersions {
			return fmt.Errorf("dependabot gomod ignore item for %s must not specify versions", targetModule)
		}
		if !item.hasUpdateTypes {
			return fmt.Errorf("dependabot gomod ignore item for %s must specify update-types", targetModule)
		}
		got := slices.Clone(item.updateTypes)
		slices.Sort(got)
		if !slices.Equal(got, wantTypes) {
			return fmt.Errorf("dependabot gomod ignore update-types for %s = %v, want %v", targetModule, got, wantTypes)
		}
		found = true
	}
	if !found {
		return fmt.Errorf("dependabot gomod ignore block missing exact rule for %s", targetModule)
	}
	return nil
}

func stripYAMLComment(line string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
				return strings.TrimRight(line[:i], " \t\r")
			}
		}
	}
	return strings.TrimRight(line, " \t\r")
}

func unquoteYAML(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func leadingSpaces(s string) int {
	n := 0
	for n < len(s) && s[n] == ' ' {
		n++
	}
	return n
}

func TestAgentHarnessPostEditChecksAreWired(t *testing.T) {
	if !fileExists(repoFile(t, "scripts/agent-harness/post_edit_checks.py")) {
		t.Error("scripts/agent-harness/post_edit_checks.py is missing")
	}
	for _, rel := range []string{".claude/settings.json", ".codex/hooks.json"} {
		var cfg struct {
			Hooks struct {
				PostToolUse []struct {
					Hooks []struct {
						Type    string `json:"type"`
						Command string `json:"command"`
					} `json:"hooks"`
				} `json:"PostToolUse"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal([]byte(readRepoFile(t, rel)), &cfg); err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		wired := false
		for _, group := range cfg.Hooks.PostToolUse {
			for _, hook := range group.Hooks {
				if hook.Type == "command" && strings.Contains(hook.Command, "scripts/agent-harness/post_edit_checks.py") {
					wired = true
				}
			}
		}
		if !wired {
			t.Errorf("%s does not wire scripts/agent-harness/post_edit_checks.py under PostToolUse", rel)
		}
	}
}

func TestDifferentialTestsRunInTheirOwnWorkflow(t *testing.T) {
	raw, err := os.ReadFile(repoFile(t, ".github/workflows/drt.yml"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	for _, needle := range []string{
		`go vet -tags "drt etcd" ./core/drt/...`,
		"go test -count=1 -tags drt ./core/drt/...",
		"lake build nayutar-drt",
		"working-directory: spec",
		"NAYUTAR_DRT_CASES",
		"'10000'",
		"'1000'",
		`NAYUTAR_DRT_CASES: ${{ github.event_name == 'push' && '10000' || '1000' }}`,
		"sha256sum --check",
		"permissions: {}",
		"persist-credentials: false",
		"timeout-minutes",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("drt workflow missing %s", needle)
		}
	}
	requireRunsOnEveryChange(t, "drt.yml", body)
	if strings.Contains(body, "continue-on-error") || strings.Contains(body, "|| true") {
		t.Error("drt workflow must not swallow test failures")
	}
}

func TestDifferentialTestsStayBehindTheDrtBuildTag(t *testing.T) {
	reg := readRepoFile(t, "core/drt/register_drt_test.go")
	firstLine, _, _ := strings.Cut(reg, "\n")
	if firstLine != "//go:build drt" {
		t.Errorf("core/drt/register_drt_test.go first line = %q, want %q", firstLine, "//go:build drt")
	}
	for _, fn := range []string{
		"func TestRegisterMatchesLeanModel(",
		"func TestRegisterComparisonDetectsEveryFieldMutation(",
		"func TestRegisterDRTCatchesBrokenImplementations(",
	} {
		if !strings.Contains(reg, fn) {
			t.Errorf("core/drt/register_drt_test.go missing %s", fn)
		}
	}

	cfg := readRepoFile(t, "core/drt/config_test.go")
	for _, line := range strings.Split(cfg, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//go:build") {
			t.Errorf("core/drt/config_test.go must not have a //go:build line: %s", line)
		}
	}
}

func TestStoreChecksRunAgainstPinnedEtcd(t *testing.T) {
	const install = `echo "${ETCD_SHA256}  ${RUNNER_TEMP}/${archive}" | sha256sum --check --strict`

	job := extractJob(readRepoFile(t, ".github/workflows/ci.yml"), "store-etcd:")
	if job == "" {
		t.Fatal("ci.yml has no store-etcd job")
	}
	drt := readRepoFile(t, ".github/workflows/drt.yml")
	for name, body := range map[string]string{"ci.yml store-etcd job": job, "drt.yml": drt} {
		for _, needle := range []string{
			"ETCD_VERSION: v3.6.",
			"ETCD_SHA256: ",
			install,
			"NAYUTAR_ETCD_BIN: ${{ runner.temp }}/etcd/etcd",
		} {
			if !strings.Contains(body, needle) {
				t.Errorf("%s missing %s", name, needle)
			}
		}
	}
	if !strings.Contains(job, "go vet -tags etcd ./core/store/... ./core/liveness/... ./core/resource/... ./core/wire/...") {
		t.Error("store-etcd job must run go vet with the etcd tag on the store, liveness, resource and wire packages")
	}
	if !strings.Contains(job, "go test -count=1 -tags etcd ./core/store/... ./core/liveness/ ./core/resource/ ./core/wire/\n") {
		t.Error("store-etcd job must run the store, liveness, resource and wire tests with the etcd tag")
	}
	if !strings.Contains(job, "go test -race -count=1 -tags etcd ./core/store/... ./core/liveness/ ./core/resource/ ./core/wire/\n") {
		t.Error("store-etcd job must run the store, liveness, resource and wire tests with -race and the etcd tag")
	}
	if !strings.Contains(drt, `-tags "drt etcd" ./core/drt/...`) {
		t.Error("drt workflow must run the differential tests against etcd")
	}
	for _, body := range []string{job, drt} {
		if strings.Contains(body, " -run ") {
			t.Error("etcd test steps must not narrow the run with -run; a filter that matches nothing still passes")
		}
	}

	for rel, want := range map[string]string{
		"core/store/etcdstore/etcdstore_test.go":         "//go:build etcd",
		"core/store/etcdstore/faults_test.go":            "//go:build etcd",
		"core/store/etcdstore/cotenancy_test.go":         "//go:build etcd",
		"core/store/etcdstore/partition_test.go":         "//go:build etcd",
		"core/store/etcdstore/etcdtest/etcdtest_test.go": "//go:build etcd",
		"core/store/tenant/tenant_etcd_test.go":          "//go:build etcd",
		"core/store/watchcache/watchcache_etcd_test.go":  "//go:build etcd",
		"core/liveness/registry_etcd_test.go":            "//go:build etcd",
		"core/resource/objects_etcd_test.go":             "//go:build etcd",
		"core/wire/wire_etcd_test.go":                    "//go:build etcd",
		"core/drt/store_drt_test.go":                     "//go:build drt",
		"core/drt/store_tenant_drt_test.go":              "//go:build drt",
		"core/drt/keyspace_drt_test.go":                  "//go:build drt",
		"core/drt/watchcache_drt_test.go":                "//go:build drt",
		"core/drt/liveness_drt_test.go":                  "//go:build drt",
		"core/drt/envelope_drt_test.go":                  "//go:build drt",
		"core/drt/refs_drt_test.go":                      "//go:build drt",
		"core/drt/orderedset_drt_test.go":                "//go:build drt",
		"core/drt/patch_drt_test.go":                     "//go:build drt",
		"core/drt/ownership_drt_test.go":                 "//go:build drt",
		"core/drt/node_drt_test.go":                      "//go:build drt",
		"core/drt/netpol_drt_test.go":                    "//go:build drt",
		"core/node/containerd_linux_test.go":             "//go:build linux && containerd",
		"core/netpol/nftables_linux_test.go":             "//go:build linux && nftables",
		"core/drt/store_etcd_drt_test.go":                "//go:build drt && etcd",
	} {
		firstLine, _, _ := strings.Cut(readRepoFile(t, rel), "\n")
		if firstLine != want {
			t.Errorf("%s first line = %q, want %q", rel, firstLine, want)
		}
	}
	for rel, fns := range map[string][]string{
		"core/store/etcdstore/etcdstore_test.go":          {"func TestServerIsEtcd36(", "func TestContract(", "func TestLinearizability(", "func TestEmptyPrefixCoversEveryKey(", "func TestCompareOnlyTxnIsLinearizable(", "func TestWatchFromNowStartsAfterTheCall(", "func TestWatchFromNowAnswersProgressOnAnIdleStore(", "func TestWatchBelowCompactionEndsCompactedOnALaggingFollower("},
		"core/store/etcdstore/faults_test.go":             {"func TestLinearizabilityAcrossMemberFailures(", "func TestWatchAcrossMemberFailures("},
		"core/store/etcdstore/cotenancy_test.go":          {"func TestCotenancyMeasurement("},
		"core/store/etcdstore/partition_test.go":          {"func TestLinearizabilityAcrossMajorityLossAndPartitions(", "func TestMajorityLossRejectsStrongOperations(", "func TestIsolatedMemberDoesNotServeStaleState(", "func TestWatchOnIsolatedMemberEndsAndResumesElsewhere("},
		"core/store/etcdstore/etcdtest/etcdtest_test.go":  {"func TestIsolatedFollowerStopsReceivingWrites(", "func TestIsolatedLeaderIsReplaced("},
		"core/store/etcdstore/etcdtest/peerproxy_test.go": {"func TestPeerProxyForwardsUncutPOST(", "func TestPeerProxyHoldsCutPOSTAndClosesOnHeal(", "func TestPeerProxyStopsInFlightStreamWhileCut(", "func TestPeerProxyKeepsBackendSilentWhenClientClosesDuringCut(", "func TestPeerProxyFailClosedOnRaftPaths(", "func TestPeerProxyAbortsWhenBackendDown("},
		"core/store/memstore/memstore_test.go":            {"func TestContract(", "func TestLinearizability("},
		"core/store/storetest/cotenancy_test.go":          {"func TestMeasureCotenancy(", "func TestMeasureCotenancyReturnsTxnError(", "func TestSummarizeLatencies("},
		"core/store/tenant/tenant_test.go":                {"func TestValidateName(", "func TestViewHasNoCompact(", "func TestIsolation(", "func TestContract(", "func TestLinearizability("},
		"core/store/tenant/tenant_etcd_test.go":           {"func TestEtcdIsolation(", "func TestEtcdContract(", "func TestEtcdLinearizability("},
		"core/store/watchcache/linearizability_test.go":   {"func TestLinearizability(", "func TestLinearizabilityServesFromCache(", "func TestLinearizabilityCatchesStaleCacheReads("},
		"core/store/watchcache/manager_test.go":           {"func TestManagerCompaction(", "func TestManagerCompactLaggingCache(", "func TestManagerTenantIsolationAndRelativeKeys("},
		"core/store/watchcache/watchcache_etcd_test.go":   {"func TestEtcdReadYourWrites(", "func TestEtcdQuietPrefix(", "func TestEtcdFallback(", "func TestEtcdExactRevisions(", "func TestEtcdWatchFromRev(", "func TestEtcdWatchList(", "func TestEtcdBookmarks(", "func TestEtcdTooOld(", "func TestEtcdRelist(", "func TestEtcdStress(", "func TestEtcdLinearizability(", "func TestEtcdLinearizabilityAcrossLeaderFailure(", "func TestEtcdManagerCompaction(", "func TestEtcdManagerLaggingCache("},
		"core/drt/store_etcd_drt_test.go":                 {"func TestEtcdStoreMatchesLeanModel("},
		"core/drt/store_tenant_drt_test.go":               {"func TestTenantViewMatchesLeanModel("},
		"core/drt/store_drt_test.go": {
			"func TestStoreMatchesLeanModel(",
			"func TestStoreCasesReachEveryOutcome(",
			"func TestStoreComparisonDetectsEveryFieldMutation(",
			"func TestStoreDRTCatchesBrokenImplementations(",
		},
		"core/drt/keyspace_drt_test.go": {
			"func TestKeyspaceMatchesLeanModel(",
			"func TestKeyspaceCasesReachEveryOutcome(",
			"func TestKeyspaceComparisonDetectsEveryFieldMutation(",
		},
		"core/drt/watchcache_drt_test.go": {
			"func TestWatchCacheMatchesLeanModel(",
			"func TestWatchCacheCasesReachEveryOutcome(",
			"func TestWatchCacheComparisonDetectsEveryFieldMutation(",
			"func TestWatchCacheDRTCatchesBrokenImplementations(",
		},
		"core/drt/liveness_drt_test.go": {
			"func TestLivenessMatchesLeanModel(",
			"func TestLivenessGeneratorCoverage(",
			"func TestLivenessComparisonDetectsEveryFieldMutation(",
			"func TestLivenessDRTCatchesBrokenImplementations(",
		},
		"core/drt/envelope_drt_test.go": {
			"func TestEnvelopeMatchesLeanModel(",
			"func TestEnvelopeCasesReachEveryOutcome(",
			"func TestEnvelopeComparisonDetectsEveryFieldMutation(",
			"func TestEnvelopeDRTCatchesBrokenImplementations(",
		},
		"core/drt/refs_drt_test.go": {
			"func TestRefsMatchesLeanModel(",
			"func TestRefsCasesReachEveryOutcome(",
			"func TestRefsComparisonDetectsEveryFieldMutation(",
			"func TestRefsDRTCatchesBrokenImplementations(",
		},
		"core/drt/orderedset_drt_test.go": {
			"func TestOrderedSetMatchesLeanModel(",
			"func TestOrderedSetCasesReachEveryOutcome(",
			"func TestOrderedSetComparisonDetectsEveryFieldMutation(",
			"func TestOrderedSetDRTCatchesBrokenImplementations(",
		},
		"core/drt/patch_drt_test.go": {
			"func TestPatchMatchesLeanModel(",
			"func TestPatchCasesReachEveryOutcome(",
			"func TestPatchComparisonDetectsEveryFieldMutation(",
			"func TestPatchDRTCatchesBrokenImplementations(",
		},
		"core/drt/ownership_drt_test.go": {
			"func TestOwnershipMatchesLeanModel(",
			"func TestOwnershipCasesReachEveryOutcome(",
			"func TestOwnershipComparisonDetectsEveryFieldMutation(",
			"func TestOwnershipDRTCatchesBrokenImplementations(",
		},
		"core/drt/node_drt_test.go": {
			"func TestNodeMatchesLeanModel(",
			"func TestNodeCasesReachEveryOutcome(",
			"func TestNodeComparisonDetectsEveryFieldMutation(",
			"func TestNodeDRTCatchesBrokenImplementations(",
		},
		"core/drt/netpol_drt_test.go": {
			"func TestNetpolMatchesLeanModel(",
			"func TestNetpolCasesReachEveryOutcome(",
			"func TestNetpolComparisonDetectsEveryFieldMutation(",
			"func TestNetpolDRTCatchesBrokenImplementations(",
		},
		"core/netpol/nftables_linux_test.go": {
			"func TestNftablesDefaultDenyAndMutualAllow(",
		},
	} {
		body := readRepoFile(t, rel)
		for _, fn := range fns {
			if !strings.Contains(body, fn) {
				t.Errorf("%s missing %s", rel, fn)
			}
		}
	}
}

func TestNetpolNftablesJobRunsWithoutSkipping(t *testing.T) {
	job := extractJob(readRepoFile(t, ".github/workflows/ci.yml"), "netpol-nftables:")
	if job == "" {
		t.Fatal("ci.yml has no netpol-nftables job")
	}
	for _, needle := range []string{
		"timeout-minutes: 10",
		"contents: read",
		"persist-credentials: false",
		"CNI_PLUGINS_SHA256:",
		"sha256sum --check --strict",
		"nftables",
		"go vet -tags nftables ./core/netpol/...",
		"go test -count=1 -tags nftables ./core/netpol/...",
		"go test -race -count=1 -tags nftables ./core/netpol/...",
	} {
		if !strings.Contains(job, needle) {
			t.Errorf("netpol-nftables job missing %s", needle)
		}
	}
	if strings.Contains(job, " -run ") {
		t.Error("netpol-nftables test steps must not narrow the run with -run")
	}
}

func TestCorePackagesNeverSkipTests(t *testing.T) {
	root := repoRoot(t)
	err := filepath.WalkDir(filepath.Join(root, "core"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "core/ci_test.go" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		text := string(data)
		for _, banned := range []string{"t.Skip(", "t.Skipf(", "t.SkipNow(", ".Skip(", ".Skipf(", ".SkipNow("} {
			if strings.Contains(text, banned) {
				t.Errorf("%s calls %s; missing dependencies or oracles must fail", rel, banned)
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestStoreContractHasNoLeases(t *testing.T) {
	contract := readRepoFile(t, "core/store/store.go")
	for _, needle := range []string{
		"Get(ctx context.Context, key string, atRev Rev) (KV, Rev, error)",
		"Range(ctx context.Context, prefix string, atRev Rev, limit int, cont string) ([]KV, Rev, string, error)",
		"Txn(ctx context.Context, cmps []Cmp, ops []Op) (bool, Rev, error)",
		"Watch(ctx context.Context, prefix string, fromRev Rev) (WatchStream, error)",
		"CurrentRev(ctx context.Context) (Rev, error)",
		"Compact(ctx context.Context, rev Rev) error",
		"Events() <-chan Event",
		"RequestProgress(ctx context.Context) error",
	} {
		if !strings.Contains(contract, needle) {
			t.Errorf("store contract missing %s", needle)
		}
	}
	root := repoRoot(t)
	err := filepath.WalkDir(filepath.Join(root, "core", "store"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return walkErr
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		for _, banned := range []string{"clientv3.WithLease", "clientv3.NewLease", ".Grant(", "LeaseID", "TTL("} {
			if strings.Contains(string(data), banned) {
				t.Errorf("%s uses %s; the store contract has no leases", filepath.ToSlash(rel), banned)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNoReferencesToResearchNotes(t *testing.T) {
	root := repoRoot(t)
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files -z: %v", err)
	}
	for _, entry := range bytes.Split(out, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		rel := filepath.ToSlash(string(entry))
		if rel == ".gitignore" || rel == "core/ci_test.go" {
			continue
		}
		abs := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("lstat %s: %v", rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || info.IsDir() {
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		for _, lineNum := range researchNotesRefLines(rel, string(data)) {
			t.Errorf("%s:%d: references removed research notes", rel, lineNum)
		}
	}
}

func TestResearchNotesRefLines(t *testing.T) {
	cases := []struct {
		name    string
		rel     string
		content string
		want    []int
	}{
		{
			name: "match",
			rel:  "AGENTS.md",
			content: "line one\n" +
				"see docs/research/poc.md\n" +
				"and poc.md\n" +
				"and draft_20261117_001.md\n",
			want: []int{2, 3, 4},
		},
		{
			name:    "no match",
			rel:     "README.md",
			content: "clean line\nanother clean line\n",
			want:    nil,
		},
		{
			name: "accepted ADR skipped",
			rel:  "docs/ADR/0001-example.md",
			content: "---\n" +
				"title: Example\n" +
				"status: accepted\n" +
				"---\n\n" +
				"Historical note: docs/research/poc.md\n",
			want: nil,
		},
		{
			name: "proposed ADR checked",
			rel:  "docs/ADR/0099-example.md",
			content: "---\n" +
				"title: Example\n" +
				"status: proposed\n" +
				"---\n\n" +
				"status: accepted\n" +
				"Ref: docs/research/poc.md\n",
			want: []int{7},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := researchNotesRefLines(tc.rel, tc.content)
			if !slices.Equal(got, tc.want) {
				t.Errorf("researchNotesRefLines(%q, ...) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}

var researchNoteNeedles = []string{
	"docs/research",
	"poc.md",
	"draft_20261117_001",
}

func researchNotesRefLines(rel, content string) []int {
	rel = filepath.ToSlash(rel)
	if rel == ".gitignore" || rel == "core/ci_test.go" || isAcceptedADR(rel, content) {
		return nil
	}
	var lines []int
	for i, line := range strings.Split(content, "\n") {
		for _, needle := range researchNoteNeedles {
			if strings.Contains(line, needle) {
				lines = append(lines, i+1)
				break
			}
		}
	}
	return lines
}

func isAcceptedADR(rel, content string) bool {
	if path.Dir(rel) != "docs/ADR" || !strings.HasSuffix(rel, ".md") {
		return false
	}
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return false
	}
	accepted := false
	for _, line := range lines[1:] {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "---" {
			return accepted
		}
		if k, v, ok := strings.Cut(trimmed, ":"); ok {
			if strings.TrimSpace(k) == "status" && strings.Trim(strings.TrimSpace(v), `"'`) == "accepted" {
				accepted = true
			}
		}
	}
	return false
}

func TestAgentInstructionsMatchTheGoAndLeanSplit(t *testing.T) {
	body := readRepoFile(t, "AGENTS.md")
	if strings.Contains(body, "実装言語・ストア構成・拡張モデルは未決定") {
		t.Error("AGENTS.md still says the implementation language is undecided")
	}
	if !strings.Contains(body, "go test ./...") {
		t.Error("AGENTS.md verification commands must include go test ./...")
	}
	if !strings.Contains(body, "lake build") {
		t.Error("AGENTS.md verification commands must include lake build")
	}
	for _, needle := range []string{
		"go test -tags drt ./core/drt/...",
		"go test -tags etcd ./core/store/... ./core/liveness/ ",
		`go test -tags "drt etcd" ./core/drt/...`,
		"NAYUTAR_ETCD_BIN",
		"lake comparator",
		"spec/Nayutar/Statements",
	} {
		if !strings.Contains(body, needle) {
			t.Errorf("AGENTS.md must document %s", needle)
		}
	}
}

var (
	yamlIfKey      = regexp.MustCompile(`^\s*["']?if["']?\s*:`)
	yamlFilterKey  = regexp.MustCompile(`^\s*["']?(paths|paths-ignore|types|tags|tags-ignore)["']?\s*:`)
	yamlBranchLine = regexp.MustCompile(`^\s*-\s+main\s*$`)
)

// requireRunsOnEveryChange keeps a workflow's checks reportable as required
// status checks: a skipped workflow, job or step leaves them pending or falsely green.
func requireRunsOnEveryChange(t *testing.T, name, body string) {
	t.Helper()
	for _, trigger := range []string{"push:", "pull_request:"} {
		block := extractJob(body, trigger)
		if block == "" {
			t.Errorf("%s has no %s trigger", name, trigger)
			continue
		}
		var rest []string
		for _, line := range strings.Split(block, "\n")[1:] {
			if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				rest = append(rest, line)
			}
		}
		if len(rest) != 2 || strings.TrimSpace(rest[0]) != "branches:" || !yamlBranchLine.MatchString(rest[1]) {
			t.Errorf("%s %s must list exactly branches: [main] and nothing else (no paths, types or other filters), got %q", name, trigger, rest)
		}
		for _, line := range strings.Split(block, "\n") {
			if yamlFilterKey.MatchString(line) {
				t.Errorf("%s %s must not filter events: %q", name, trigger, strings.TrimSpace(line))
			}
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if yamlIfKey.MatchString(line) {
			t.Errorf("%s must not skip a job or step with an if: key: %q", name, strings.TrimSpace(line))
		}
	}
}

func hasTrimmedLine(body, want string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

func extractJob(body, header string) string {
	lines := strings.Split(body, "\n")
	start := -1
	for i, line := range lines {
		if line == "  "+header {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if line != "" && line[0] != ' ' && line[0] != '#' {
			end = i
			break
		}
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(line, ":") {
			end = i
			break
		}
	}
	return strings.Join(lines[start:end], "\n")
}

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join(repoRoot(t), rel)
}
