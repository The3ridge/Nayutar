package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
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
		"name: Decode the development etcd declaration",
		`NAYUTAR_REQUIRE_LKAL: "1"`,
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
	if !strings.Contains(job, "go vet -tags etcd ./core/store/... ./core/liveness/... ./core/resource/...") {
		t.Error("store-etcd job must run go vet with the etcd tag on the store, liveness and resource packages")
	}
	if !strings.Contains(job, "go test -count=1 -tags etcd ./core/store/... ./core/liveness/ ./core/resource/\n") {
		t.Error("store-etcd job must run the store, liveness and resource tests with the etcd tag")
	}
	if !strings.Contains(job, "go test -race -count=1 -tags etcd ./core/store/... ./core/liveness/ ./core/resource/\n") {
		t.Error("store-etcd job must run the store, liveness and resource tests with -race and the etcd tag")
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
		"core/drt/store_drt_test.go":                     "//go:build drt",
		"core/drt/store_tenant_drt_test.go":              "//go:build drt",
		"core/drt/watchcache_drt_test.go":                "//go:build drt",
		"core/drt/liveness_drt_test.go":                  "//go:build drt",
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
	} {
		body := readRepoFile(t, rel)
		for _, fn := range fns {
			if !strings.Contains(body, fn) {
				t.Errorf("%s missing %s", rel, fn)
			}
		}
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

func TestResearchMemoPathResolves(t *testing.T) {
	root := repoRoot(t)
	memo := filepath.Join(root, "docs", "research", "poc.md")
	if !fileExists(memo) {
		t.Fatal("docs/research/poc.md is missing")
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".lake":
				return filepath.SkipDir
			}
			if rel, err := filepath.Rel(root, path); err == nil && filepath.ToSlash(rel) == agentWorktrees {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), "](poc.md)") || strings.Contains(string(data), "](./poc.md)") {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				t.Fatal(relErr)
			}
			t.Errorf("%s links to poc.md; the memo is docs/research/poc.md", filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{
		"AGENTS.md",
		"CLAUDE.md",
		".claude/skills/grill-with-docs/SKILL.md",
		".claude/skills/web-researcher/SKILL.md",
	} {
		body := readRepoFile(t, rel)
		if !strings.Contains(body, "docs/research/poc.md") {
			t.Errorf("%s does not reference docs/research/poc.md", rel)
		}
	}
	adr := readRepoFile(t, "docs/ADR/0001-aim-for-a-full-kubernetes-replacement-on-fixed-premises.md")
	if !strings.Contains(adr, "docs/research/poc.md") {
		t.Error("ADR 0001 does not record that the survey memo is docs/research/poc.md")
	}
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
