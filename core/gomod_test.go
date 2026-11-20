package core

import (
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestGoModRequiresMinimumWithoutToolchainPin(t *testing.T) {
	version, toolchain, modulePath := parseGoMod(readRepoFile(t, "go.mod"))
	if modulePath != "github.com/Kaikei-e/Nayutar" {
		t.Errorf("module path = %q, want github.com/Kaikei-e/Nayutar", modulePath)
	}
	if version != "1.27.3" {
		t.Errorf("go directive = %q, want 1.27.3", version)
	}
	if toolchain != "" {
		t.Errorf("toolchain directive %q pins a release; keep the 1.27.3 floor and follow supported releases", toolchain)
	}
}

func TestGoVersionAtLeast(t *testing.T) {
	cases := []struct {
		runtime string
		minimum string
		want    bool
	}{
		{runtime: "go1.27.3", minimum: "1.27.3", want: true},
		{runtime: "go1.27.4", minimum: "1.27.3", want: true},
		{runtime: "go1.27.2", minimum: "1.27.3", want: false},
		{runtime: "go1.28.0", minimum: "1.27.3", want: true},
		{runtime: "go1.26.12", minimum: "1.27.3", want: false},
		{runtime: "go1.27", minimum: "1.27.3", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.runtime, func(t *testing.T) {
			if got := goVersionAtLeast(tc.runtime, tc.minimum); got != tc.want {
				t.Fatalf("goVersionAtLeast(%q, %q) = %v, want %v", tc.runtime, tc.minimum, got, tc.want)
			}
		})
	}
}

func TestRuntimeMeetsMinimum(t *testing.T) {
	const minimum = "1.27.3"
	got := runtime.Version()
	if !goVersionAtLeast(got, minimum) {
		t.Fatalf("runtime %s is older than go%s", got, minimum)
	}
}

func parseGoMod(data string) (version, toolchain, modulePath string) {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "module":
			modulePath = fields[1]
		case "go":
			version = fields[1]
		case "toolchain":
			toolchain = fields[1]
		}
	}
	return version, toolchain, modulePath
}

func goVersionAtLeast(runtimeVersion, minimum string) bool {
	got, ok := parseGoVer(runtimeVersion)
	want, okMin := parseGoVer(minimum)
	if !ok || !okMin {
		return false
	}
	if got[0] != want[0] {
		return got[0] > want[0]
	}
	if got[1] != want[1] {
		return got[1] > want[1]
	}
	return got[2] >= want[2]
}

func parseGoVer(v string) ([3]int, bool) {
	v = strings.TrimPrefix(v, "go")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}
