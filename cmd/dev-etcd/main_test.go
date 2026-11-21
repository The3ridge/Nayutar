package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Kaikei-e/LKAL"
)

func TestDecodeClusterConfig(t *testing.T) {
	validLKAL := `@lkal "0.1"

etcd {
  version = "3.6.16"
  member "m0" { client-url = "http://127.0.0.1:12379", peer-url = "http://127.0.0.1:12380" }
  member "m1" { client-url = "http://127.0.0.1:22379", peer-url = "http://127.0.0.1:22380" }
  member "m2" { client-url = "http://127.0.0.1:32379", peer-url = "http://127.0.0.1:32380" }
}
`

	tests := []struct {
		name    string
		input   string
		want    clusterConfig
		wantErr string
	}{
		{
			name:  "valid declaration",
			input: validLKAL,
			want: clusterConfig{
				Version: "3.6.16",
				Members: []memberConfig{
					{Name: "m0", ClientURL: "http://127.0.0.1:12379", PeerURL: "http://127.0.0.1:12380"},
					{Name: "m1", ClientURL: "http://127.0.0.1:22379", PeerURL: "http://127.0.0.1:22380"},
					{Name: "m2", ClientURL: "http://127.0.0.1:32379", PeerURL: "http://127.0.0.1:32380"},
				},
			},
		},
		{
			name: "missing version",
			input: `@lkal "0.1"
etcd {
  member "m0" { client-url = "http://127.0.0.1:12379", peer-url = "http://127.0.0.1:12380" }
  member "m1" { client-url = "http://127.0.0.1:22379", peer-url = "http://127.0.0.1:22380" }
  member "m2" { client-url = "http://127.0.0.1:32379", peer-url = "http://127.0.0.1:32380" }
}
`,
			wantErr: "L502",
		},
		{
			name: "whitespace version",
			input: `@lkal "0.1"
etcd {
  version = "  "
  member "m0" { client-url = "http://127.0.0.1:12379", peer-url = "http://127.0.0.1:12380" }
  member "m1" { client-url = "http://127.0.0.1:22379", peer-url = "http://127.0.0.1:22380" }
  member "m2" { client-url = "http://127.0.0.1:32379", peer-url = "http://127.0.0.1:32380" }
}
`,
			wantErr: "L505",
		},
		{
			name: "2 members",
			input: `@lkal "0.1"
etcd {
  version = "3.6.16"
  member "m0" { client-url = "http://127.0.0.1:12379", peer-url = "http://127.0.0.1:12380" }
  member "m1" { client-url = "http://127.0.0.1:22379", peer-url = "http://127.0.0.1:22380" }
}
`,
			wantErr: "at least 3 entries",
		},
		{
			name: "4 members",
			input: `@lkal "0.1"
etcd {
  version = "3.6.16"
  member "m0" { client-url = "http://127.0.0.1:12379", peer-url = "http://127.0.0.1:12380" }
  member "m1" { client-url = "http://127.0.0.1:22379", peer-url = "http://127.0.0.1:22380" }
  member "m2" { client-url = "http://127.0.0.1:32379", peer-url = "http://127.0.0.1:32380" }
  member "m3" { client-url = "http://127.0.0.1:42379", peer-url = "http://127.0.0.1:42380" }
}
`,
			wantErr: "at most 3 entries",
		},
		{
			name: "missing client-url",
			input: `@lkal "0.1"
etcd {
  version = "3.6.16"
  member "m0" { client-url = "http://127.0.0.1:12379", peer-url = "http://127.0.0.1:12380" }
  member "m1" { peer-url = "http://127.0.0.1:22380" }
  member "m2" { client-url = "http://127.0.0.1:32379", peer-url = "http://127.0.0.1:32380" }
}
`,
			wantErr: "client-url",
		},
		{
			name: "missing peer-url",
			input: `@lkal "0.1"
etcd {
  version = "3.6.16"
  member "m0" { client-url = "http://127.0.0.1:12379", peer-url = "http://127.0.0.1:12380" }
  member "m1" { client-url = "http://127.0.0.1:22379", peer-url = "http://127.0.0.1:22380" }
  member "m2" { client-url = "http://127.0.0.1:32379" }
}
`,
			wantErr: "peer-url",
		},
		{
			name: "duplicate client-url across members",
			input: `@lkal "0.1"
etcd {
  version = "3.6.16"
  member "m0" { client-url = "http://127.0.0.1:12379", peer-url = "http://127.0.0.1:12380" }
  member "m1" { client-url = "http://127.0.0.1:12379", peer-url = "http://127.0.0.1:22380" }
  member "m2" { client-url = "http://127.0.0.1:32379", peer-url = "http://127.0.0.1:32380" }
}
`,
			wantErr: "L509",
		},
		{
			name: "unknown field rejected by closed schema",
			input: `@lkal "0.1"
etcd {
  version = "3.6.16"
  extra = true
  member "m0" { client-url = "http://127.0.0.1:12379", peer-url = "http://127.0.0.1:12380" }
  member "m1" { client-url = "http://127.0.0.1:22379", peer-url = "http://127.0.0.1:22380" }
  member "m2" { client-url = "http://127.0.0.1:32379", peer-url = "http://127.0.0.1:32380" }
}
`,
			wantErr: "L503",
		},
		{
			name:    "missing etcd block",
			input:   "@lkal \"0.1\"\n",
			wantErr: "etcd",
		},
		{
			name:    "invalid lkal",
			input:   "{",
			wantErr: "parse",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeClusterConfig("dev.lkal", []byte(tc.input))
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Version != tc.want.Version {
				t.Errorf("Version = %q, want %q", got.Version, tc.want.Version)
			}
			if !slices.Equal(got.Members, tc.want.Members) {
				t.Errorf("Members = %+v, want %+v", got.Members, tc.want.Members)
			}
		})
	}
}

func TestParseEtcdVersion(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    string
		wantErr bool
	}{
		{
			name: "real format",
			output: "etcd Version: 3.6.16\n" +
				"Git SHA: 123456789\n" +
				"Go Version: go1.25.3\n" +
				"Go OS/Arch: linux/amd64\n",
			want: "3.6.16",
		},
		{
			name:   "crlf and extra whitespace",
			output: "etcd Version:   3.6.16  \r\nGit SHA: abc\r\n",
			want:   "3.6.16",
		},
		{
			name:    "missing version line",
			output:  "Git SHA: 123456789\nGo Version: go1.25.3\n",
			wantErr: true,
		},
		{
			name:    "empty version value",
			output:  "etcd Version: \nGit SHA: 123456789\n",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseEtcdVersion(tc.output)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got version %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("version = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMemberArgs(t *testing.T) {
	membersUnsorted := []memberConfig{
		{Name: "m2", ClientURL: "http://127.0.0.1:32379", PeerURL: "http://127.0.0.1:32380"},
		{Name: "m0", ClientURL: "http://127.0.0.1:12379", PeerURL: "http://127.0.0.1:12380"},
		{Name: "m1", ClientURL: "http://127.0.0.1:22379", PeerURL: "http://127.0.0.1:22380"},
	}
	wantInitialCluster := "m0=http://127.0.0.1:12380,m1=http://127.0.0.1:22380,m2=http://127.0.0.1:32380"
	baseDataDir := filepath.Join("/var", "lib", "nayutar", "dev-etcd")

	tests := []struct {
		name   string
		member memberConfig
		want   []string
	}{
		{
			name:   "m0",
			member: membersUnsorted[1],
			want: []string{
				"--name", "m0",
				"--data-dir", filepath.Join(baseDataDir, "m0"),
				"--listen-client-urls", "http://127.0.0.1:12379",
				"--advertise-client-urls", "http://127.0.0.1:12379",
				"--listen-peer-urls", "http://127.0.0.1:12380",
				"--initial-advertise-peer-urls", "http://127.0.0.1:12380",
				"--initial-cluster", wantInitialCluster,
				"--initial-cluster-state", "new",
				"--initial-cluster-token", "nayutar-dev",
			},
		},
		{
			name:   "m1",
			member: membersUnsorted[2],
			want: []string{
				"--name", "m1",
				"--data-dir", filepath.Join(baseDataDir, "m1"),
				"--listen-client-urls", "http://127.0.0.1:22379",
				"--advertise-client-urls", "http://127.0.0.1:22379",
				"--listen-peer-urls", "http://127.0.0.1:22380",
				"--initial-advertise-peer-urls", "http://127.0.0.1:22380",
				"--initial-cluster", wantInitialCluster,
				"--initial-cluster-state", "new",
				"--initial-cluster-token", "nayutar-dev",
			},
		},
		{
			name:   "m2",
			member: membersUnsorted[0],
			want: []string{
				"--name", "m2",
				"--data-dir", filepath.Join(baseDataDir, "m2"),
				"--listen-client-urls", "http://127.0.0.1:32379",
				"--advertise-client-urls", "http://127.0.0.1:32379",
				"--listen-peer-urls", "http://127.0.0.1:32380",
				"--initial-advertise-peer-urls", "http://127.0.0.1:32380",
				"--initial-cluster", wantInitialCluster,
				"--initial-cluster-state", "new",
				"--initial-cluster-token", "nayutar-dev",
			},
		},
	}

	var seenClusters []string
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := memberArgs(tc.member, baseDataDir, membersUnsorted)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("memberArgs = %v, want %v", got, tc.want)
			}
			if slices.Contains(got, "--unsafe-no-fsync") {
				t.Errorf("memberArgs must not pass --unsafe-no-fsync")
			}
			idx := slices.Index(got, "--initial-cluster")
			if idx < 0 || idx+1 >= len(got) {
				t.Fatalf("missing --initial-cluster flag in %v", got)
			}
			seenClusters = append(seenClusters, got[idx+1])
		})
	}
	for i := 1; i < len(seenClusters); i++ {
		if seenClusters[i] != seenClusters[0] {
			t.Errorf("--initial-cluster differed across members: %v", seenClusters)
		}
	}
}

func TestMemberURLsMustBeLoopbackIPLiterals(t *testing.T) {
	accept := []string{
		"http://127.0.0.1:12379",
		"http://127.0.0.1:32380",
		"http://127.0.0.2:2379",
		"http://[::1]:2379",
		"HTTP://127.0.0.1:2379",
		"http://127.0.0.1:2379/",
		"http://[0:0:0:0:0:0:0:1]:2379",
		"http://[::ffff:127.0.0.1]:2379",
	}
	reject := []string{
		"http://0.0.0.0:2379",
		"http://192.168.1.10:2379",
		"http://[::]:2379",
		"http://[fe80::1]:2379",
		"http://localhost:2379",
		"http://example.com:2379",
		"http://127.0.0.1",
		"http://127.0.0.1:",
		"http://127.0.0.1:0",
		"http://127.0.0.1:70000",
		"https://127.0.0.1:2379",
		"tcp://127.0.0.1:2379",
		"unix://127.0.0.1:2379",
		"127.0.0.1:2379",
		"http://user@127.0.0.1:2379",
		"http://127.0.0.1:2379/v3",
		"http://127.0.0.1:2379?x=1",
		"http://127.0.0.1:2379#frag",
		"http://127.0.0.1:2379?",
		"http://[::1%25lo]:2379",
		"://",
		"%zz",
		"garbage",
	}
	for _, field := range []string{"client-url", "peer-url"} {
		decode := func(u string) error {
			client, peer := "http://127.0.0.1:12379", u
			if field == "client-url" {
				client, peer = u, "http://127.0.0.1:12380"
			}
			src := fmt.Sprintf("@lkal \"0.1\"\netcd {\n  version = \"3.6.16\"\n  member \"m0\" { client-url = %s, peer-url = %s }\n  member \"m1\" { client-url = \"http://127.0.0.1:22379\", peer-url = \"http://127.0.0.1:22380\" }\n  member \"m2\" { client-url = \"http://127.0.0.1:42379\", peer-url = \"http://127.0.0.1:42380\" }\n}\n",
				lkal.FormatValue(lkal.String(client)),
				lkal.FormatValue(lkal.String(peer)),
			)
			_, err := decodeClusterConfig("dev.lkal", []byte(src))
			return err
		}
		for _, u := range accept {
			if err := decode(u); err != nil {
				t.Errorf("%s %q: unexpected error: %v", field, u, err)
			}
		}
		for _, u := range reject {
			err := decode(u)
			if err == nil {
				t.Errorf("%s %q: accepted", field, u)
				continue
			}
			if !strings.Contains(err.Error(), `"m0"`) || !strings.Contains(err.Error(), field) {
				t.Errorf("%s %q: error %q does not name the member and the field", field, u, err)
			}
			if strings.HasPrefix(u, "https:") && !strings.Contains(err.Error(), "plaintext and has no TLS configuration") {
				t.Errorf("%s %q: error %q does not explain that the cluster is plaintext", field, u, err)
			}
		}
	}
}

func TestMemberNames(t *testing.T) {
	accept := []string{"m0", "etcd-1", "node_2", "A"}
	reject := []string{"m=0", "m1,x", "a b", "", ".", "..", "a/b", `a\b`, "mé", "m\n0", "m0\n", " m0"}
	for _, name := range accept {
		if _, err := decodeClusterConfig("dev.lkal", memberNamesLKAL(name)); err != nil {
			t.Errorf("name %q: unexpected error: %v", name, err)
		}
	}
	for _, name := range reject {
		err := decodeNames(name)
		if err == nil {
			t.Errorf("name %q: accepted", name)
		} else if !strings.Contains(err.Error(), "L510") {
			t.Errorf("name %q: error %q does not report L510 invalid map key", name, err)
		}
	}
}

func memberNamesLKAL(first string) []byte {
	return []byte(fmt.Sprintf("@lkal \"0.1\"\netcd {\n  version = \"3.6.16\"\n  member %s { client-url = \"http://127.0.0.1:12379\", peer-url = \"http://127.0.0.1:12380\" }\n  member \"m1\" { client-url = \"http://127.0.0.1:22379\", peer-url = \"http://127.0.0.1:22380\" }\n  member \"m2\" { client-url = \"http://127.0.0.1:32379\", peer-url = \"http://127.0.0.1:32380\" }\n}\n",
		strconv.Quote(first),
	))
}

func decodeNames(name string) error {
	_, err := decodeClusterConfig("dev.lkal", memberNamesLKAL(name))
	return err
}

func TestDevDeclarationDecodes(t *testing.T) {
	cfg, err := readClusterConfig(filepath.Join("..", "..", defaultConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Members) != expectedMembers {
		t.Errorf("members = %d, want %d", len(cfg.Members), expectedMembers)
	}
}
