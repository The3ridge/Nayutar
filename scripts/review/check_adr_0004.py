#!/usr/bin/env python3
"""Re-check the repository shape against ADR 0004 and the ADRs that narrow it.

A passing check means the files still match the shape recorded by the review.
It does not rerun the store tests, the differential tests, or the linearizability
check, and it is not itself an ADR verdict.
"""

from __future__ import annotations

import pathlib
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]

STORE_METHODS = (
    "Get(ctx context.Context, key string, atRev Rev) (KV, Rev, error)",
    "Range(ctx context.Context, prefix string, atRev Rev, limit int, cont string) ([]KV, Rev, string, error)",
    "Txn(ctx context.Context, cmps []Cmp, ops []Op) (bool, Rev, error)",
    "Watch(ctx context.Context, prefix string, fromRev Rev) (WatchStream, error)",
    "CurrentRev(ctx context.Context) (Rev, error)",
    "Compact(ctx context.Context, rev Rev) error",
)
WATCH_METHODS = (
    "Events() <-chan Event",
    "RequestProgress(ctx context.Context) error",
)
LEASE_TOKENS = ("clientv3.WithLease", "clientv3.NewLease", ".Grant(", "LeaseID", "TTL(")
LEAN_DEFS = (
    "def Store.get",
    "def Store.range",
    "def Store.txn",
    "def Store.compact",
    "def Store.watch",
    "def Store.poll",
)


def read(rel: str) -> str:
    return (ROOT / rel).read_text(encoding="utf-8")


def frontmatter_status(rel: str) -> str:
    lines = read(rel).splitlines()
    if not lines or lines[0].strip() != "---":
        return ""
    for line in lines[1:]:
        if line.strip() == "---":
            break
        if line.startswith("status:"):
            return line.split(":", 1)[1].strip()
    return ""


def tracked_go(under: str) -> list[str]:
    res = subprocess.run(
        ["git", "-C", str(ROOT), "ls-files", "-z", "--", under],
        capture_output=True,
        check=True,
    )
    return [entry for entry in res.stdout.decode("utf-8").split("\x00") if entry.endswith(".go")]


def main() -> int:
    checks: list[tuple[str, bool, str]] = []

    def add(name: str, ok: bool, detail: str) -> None:
        checks.append((name, ok, detail))

    adr = "docs/ADR/0004-fix-a-subset-of-the-etcd-v3-api-without-leases-as-the-store-contract.md"
    add("ADR 0004 is accepted", frontmatter_status(adr) == "accepted", frontmatter_status(adr))
    add(
        "ADR 0017 is accepted and leaves the watch cache with ADR 0007",
        frontmatter_status(
            "docs/ADR/0017-build-the-drt-harness-and-the-theorem-statement-guard-first-and-defer-the-models-the-esr-proof-and-the-kubernetes-oracle.md"
        )
        == "accepted"
        and "ストア契約のモデルと DRT は [[0004]] の下で提供し" in read(
            "docs/ADR/0017-build-the-drt-harness-and-the-theorem-statement-guard-first-and-defer-the-models-the-esr-proof-and-the-kubernetes-oracle.md"
        ),
        "ADR 0017",
    )
    add(
        "ADR 0007 is still proposed",
        frontmatter_status(
            "docs/ADR/0007-serve-watches-from-a-per-tenant-cache-with-revisions-bookmarks-and-watch-list.md"
        )
        == "proposed",
        "ADR 0007",
    )

    contract = read("core/store/store.go")
    for method in STORE_METHODS + WATCH_METHODS:
        add(f"contract declares {method.split('(')[0]}", method in contract, "core/store/store.go")
    add(
        "contract names progress events and excludes leases",
        "EventProgress" in contract and "no leases" in contract and "TTL" in contract,
        "core/store/store.go",
    )

    store_go = tracked_go("core/store")
    leased = []
    for rel in store_go:
        text = read(rel)
        for token in LEASE_TOKENS:
            if token in text:
                leased.append(f"{rel}:{token}")
    add("store packages do not call the etcd lease API", leased == [], " ".join(leased) or "none")

    client_users = []
    res = subprocess.run(
        ["git", "-C", str(ROOT), "grep", "-l", "go.etcd.io/etcd/client/v3", "--", "*.go"],
        capture_output=True,
        check=False,
    )
    if res.returncode not in (0, 1):
        add("etcd client imports are confined to the adapter", False, res.stderr.decode())
    else:
        client_users = [line for line in res.stdout.decode().splitlines() if line]
        add(
            "etcd client imports are confined to the adapter",
            client_users != [] and all(path.startswith("core/store/etcdstore/") for path in client_users),
            " ".join(client_users) or "none",
        )

    for rel in (
        "core/store/memstore/memstore_test.go",
        "core/store/etcdstore/etcdstore_test.go",
    ):
        text = read(rel)
        add(
            f"{rel} runs the shared contract and linearizability checks",
            "storetest.Run(" in text and "storetest.RunLinearizability(" in text,
            rel,
        )

    add(
        "etcd adapter tests are behind the etcd build tag",
        read("core/store/etcdstore/etcdstore_test.go").startswith("//go:build etcd\n"),
        "etcdstore_test.go",
    )
    add(
        "store differential tests are behind the drt build tag",
        read("core/drt/store_drt_test.go").startswith("//go:build drt\n")
        and read("core/drt/store_etcd_drt_test.go").startswith("//go:build drt && etcd\n"),
        "core/drt",
    )
    drt = read("core/drt/store_drt_test.go")
    add(
        "store differential tests compare with the Lean store suite and reject broken stores",
        'RunModel("store"' in drt
        and "func TestStoreMatchesLeanModel(" in drt
        and "func TestStoreDRTCatchesBrokenImplementations(" in drt
        and "t.Skip(" not in drt,
        "store_drt_test.go",
    )

    model = read("spec/Nayutar/Store.lean")
    missing = [name for name in LEAN_DEFS if name not in model]
    add("Lean store model defines the contract operations", missing == [], " ".join(missing) or "spec/Nayutar/Store.lean")
    add(
        "register fixture remains beside the store model",
        (ROOT / "spec/Nayutar/Fixture/Register.lean").is_file()
        and "func stepRegister(" in read("core/drt/register_drt_test.go"),
        "Fixture/Register.lean",
    )

    ci = read(".github/workflows/ci.yml")
    drt_yml = read(".github/workflows/drt.yml")
    add(
        "CI runs the etcd-tagged store tests without narrowing -run",
        "go test -count=1 -tags etcd ./core/store/..." in ci
        and "go test -race -count=1 -tags etcd ./core/store/..." in ci
        and "ETCD_VERSION: v3.6." in ci
        and " -run " not in ci.split("store-etcd:", 1)[1].split("\njobs:", 1)[0],
        "ci.yml store-etcd",
    )
    add(
        "DRT workflow runs 10000 cases on push, including etcd",
        "github.event_name == 'push' && '10000' || '1000'" in drt_yml
        and '-tags "drt etcd" ./core/drt/...' in drt_yml
        and "ETCD_VERSION: v3.6." in drt_yml,
        "drt.yml",
    )

    skipped = []
    for rel in store_go + tracked_go("core/drt"):
        text = read(rel)
        if "t.Skip(" in text or "t.Skipf(" in text or "t.SkipNow(" in text:
            skipped.append(rel)
    add("store and DRT tests do not skip", skipped == [], " ".join(skipped) or "none")

    failed = 0
    for name, ok, detail in checks:
        mark = "PASS" if ok else "FAIL"
        if not ok:
            failed += 1
        print(f"{mark}\t{name}\t{detail}")
    print(f"{len(checks) - failed} passed, {failed} failed")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
