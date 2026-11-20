package main

import (
	"net"
	"net/url"
	"slices"
	"strings"
	"testing"
)

const nameAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_"

func FuzzDecodeClusterConfig(f *testing.F) {
	for _, seed := range []string{
		`{"etcd":{"version":"3.6.16","member":{"m0":{"client-url":"http://127.0.0.1:12379","peer-url":"http://127.0.0.1:12380"},"m1":{"client-url":"http://127.0.0.1:22379","peer-url":"http://127.0.0.1:22380"},"m2":{"client-url":"http://[::1]:32379","peer-url":"http://127.0.0.1:32380/"}}}}`,
		`{"etcd":{"version":"3.6.16","member":{"m2":{"client-url":"http://127.0.0.1:1","peer-url":"http://127.0.0.1:2"},"m1":{"client-url":"http://127.0.0.1:3","peer-url":"http://127.0.0.1:4"},"m0":{"client-url":"http://127.0.0.1:5","peer-url":"http://127.0.0.1:6"}}}}`,
		`{"etcd":{"version":"3.6.16","member":{"m0":{"client-url":"http://0.0.0.0:1","peer-url":"http://127.0.0.1:2"},"m1":{},"m2":{}}}}`,
		`{"etcd":{"version":"","member":{}}}`,
		`{"etcd":{"version":"3.6.16","member":{"..":{"client-url":"http://127.0.0.1:1","peer-url":"http://127.0.0.1:2"},"a/b":{"client-url":"http://127.0.0.1:3","peer-url":"http://127.0.0.1:4"},"\\":{"client-url":"http://127.0.0.1:5","peer-url":"http://127.0.0.1:6"}}}}`,
		`{"etcd":null}`,
		`{"etcd":[]}`,
		`{}`,
		`[`,
		``,
		`{"etcd":{"version":"3.6.16","member":{"m0":{"client-url":"http://[0:0:0:0:0:0:0:1]:1","peer-url":"http://[0::1]:2"},"m1":{"client-url":"http://[::ffff:127.0.0.1]:3","peer-url":"http://[::ffff:7f00:1]:4"},"m2":{"client-url":"HTTP://127.0.0.1:5","peer-url":"http://127.0.0.1:6"}}}}`,
		`{"etcd":{"version":"3.6.16","member":{"m=0":{"client-url":"http://127.0.0.1:1","peer-url":"http://127.0.0.1:2"},"m1,x":{"client-url":"http://127.0.0.1:3","peer-url":"http://127.0.0.1:4"},"a b":{"client-url":"http://127.0.0.1:5","peer-url":"http://127.0.0.1:6"}}}}`,
		`{"etcd":{"version":"3.6.16","member":{"m0":{"client-url":"https://127.0.0.1:1","peer-url":"http://127.0.0.1:2"},"m1":{},"m2":{}}}}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := decodeClusterConfig(data)
		if err != nil {
			return
		}
		if strings.TrimSpace(cfg.Version) == "" {
			t.Fatalf("accepted an empty version: %q", data)
		}
		if len(cfg.Members) != expectedMembers {
			t.Fatalf("accepted %d members: %q", len(cfg.Members), data)
		}
		names := make([]string, len(cfg.Members))
		for i, m := range cfg.Members {
			names[i] = m.Name
			if m.Name == "" || strings.Trim(m.Name, nameAlphabet) != "" {
				t.Fatalf("accepted member name %q", m.Name)
			}
			for field, raw := range map[string]string{"client-url": m.ClientURL, "peer-url": m.PeerURL} {
				u, err := url.Parse(raw)
				if err != nil {
					t.Fatalf("member %q %s %q does not parse: %v", m.Name, field, raw, err)
				}
				if !strings.EqualFold(u.Scheme, "http") || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
					t.Fatalf("member %q accepted %s %q", m.Name, field, raw)
				}
				if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() || strings.Contains(u.Hostname(), "%") {
					t.Fatalf("member %q accepted non-loopback %s %q", m.Name, field, raw)
				}
			}
		}
		if !slices.IsSorted(names) {
			t.Fatalf("members not sorted by name: %v", names)
		}
		if len(slices.Compact(slices.Clone(names))) != len(names) {
			t.Fatalf("duplicate member names: %v", names)
		}
	})
}
