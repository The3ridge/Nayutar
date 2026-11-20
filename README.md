# Nayutar

**English** | [日本語](README.ja.md)

**Nayutar** is a container orchestrator designed from the ground up to be a **full-scale alternative to Kubernetes** — built by a solo developer together with AI coding agents on machine-checkable formal specifications and differential oracles.

Human-written declarations use [LKAL](https://github.com/Kaikei-e/LKAL) instead of YAML. Field-level collaboration builds on [kisage](https://github.com/Kaikei-e/kisage)'s leaf-path ownership model. Container execution, networking, and storage reuse established OCI, containerd, CNI, and CSI ecosystems rather than reinventing the data plane. Every architectural decision is tracked in [`docs/ADR/`](docs/ADR/) as a verifiable DAG managed by [DocDag](https://github.com/Kaikei-e/DocDag).

> [!IMPORTANT]
> **Project Status — Early / Pre-Release (Verification Gate G0 in progress)**
>
> Nayutar is not yet usable as an end-to-end orchestrator, and the [G0 verification gate](docs/ADR/0018-define-the-g0-verification-gate-pass-criteria.md) is not yet complete. What exists today is a verified foundation of Go libraries and Lean 4 specifications — covering the consensus store contract, etcd v3.6 adapter with fault injection, tenant keyspace and fenced registry, per-tenant watch cache, soft-state node liveness, resource envelope, and write-time reference checking — backed by **34 protected Lean theorems** (`sorry`-free) and **7 Lean–Go differential random test (DRT) suites** that you can run locally today.

---

## Why Nayutar?

Nayutar does not aim to be a minimal orchestrator for small clusters; its goal is to tackle multi-tenant, multi-thousand-node orchestration while leaving behind structural pain points in configuration, ownership, isolation, and control-plane scaling ([ADR 0001](docs/ADR/0001-aim-for-a-full-kubernetes-replacement-on-fixed-premises.md)).

### 1. Typed Declarations, Zero Hand-Written YAML

Human-facing manifests and configuration are written in [LKAL](https://github.com/Kaikei-e/LKAL) (`.lkal`), eliminating implicit type coercion, indentation traps, and `null` ambiguity. Quantities (`512Mi`), enums (`.tcp`), and typed references (`&secret/db-creds/spec/password`) are first-class syntax inside a uniform three-record envelope (`meta`, `spec`, `status`) ([ADR 0009](docs/ADR/0009-fix-the-resource-envelope-to-meta-spec-and-status.md), [ADR 0025](docs/ADR/0025-realize-the-resource-envelope-with-a-lowercase-kind-two-write-entrances-a-pure-transition-core-and-an-object-store-binding.md)):

```lkal
@lkal "0.1"

meta {
  kind = "workload"
  name = "web"
  labels { app = "web" }
}

spec {
  replicas = 3
  container "app" {
    image = "ghcr.io/acme/web:1.5.0"
    port "http" { number = 8080, protocol = .tcp }
    memory = 512Mi
    env "DB_PASSWORD" { from = &secret/db-creds/spec/password }
  }
}
```

- **Strict intent vs. status separation**: Writes enter through two distinct entrances (`Update` for `labels`/`spec` and `UpdateStatus` for `status`). Out-of-scope or server-managed fields (`uid`, `generation`, `revision`, condition `since`) are rejected unless omitted or unchanged — never silently ignored.
- **YAML only at the import boundary**: Existing Kubernetes manifests (Helm, KRM, Compose) are treated strictly as import inputs. Nayutar does not pursue CNCF Kubernetes conformance certification or carry global CRD and patch-semantics baggage.

### 2. Tenant-First Isolation & Write-Time Reference Safety

Where conflating naming hierarchies with security boundaries enables confused-deputy cross-scope reference attacks in controllers, Nayutar places the tenant at the root of the store keyspace (`/t/<tenant>/`) and keeps naming hierarchies inside dot-delimited object names ([ADR 0006](docs/ADR/0006-put-the-tenant-at-the-top-of-the-key-space-and-keep-namespaces-out-of-keys.md), [ADR 0022](docs/ADR/0022-realize-the-key-space-with-a-strict-segment-grammar-a-key-codec-and-a-fenced-tenant-registry.md)):

- **Single-prefix tenant mobility & fencing**: Every object, ownership record, back-reference index entry, and API binding for a tenant lives under `/t/<tenant>/`, enabling clean prefix isolation, fenced two-phase tenant deletion, and future per-tenant store sharding.
- **Fail-closed reference validation at write time**: References (`Ref`) are strictly tenant-relative and may appear only under `spec`. On create or update, newly added `(sourcePath, ref)` pairs are checked against a reference schema table, authorized via [Cedar](https://docs.cedarpolicy.com/) (`Nayutar::Action::"Reference"`), and verified against target `mod_revision` in the **same atomic transaction** that updates the back-reference index ([ADR 0010](docs/ADR/0010-check-references-at-write-time-and-keep-them-inside-a-tenant.md), [ADR 0026](docs/ADR/0026-realize-reference-checking-with-a-pure-check-core-a-reference-schema-table-cedar-authorization-and-a-back-reference-index-in-the-object-transaction.md)).

### 3. Predictable Leaf-Path Ownership Without `force` *(Planned — [ADR 0013](docs/ADR/0013-store-leaf-path-ownership-under-its-own-key-and-update-it-in-the-same-transaction.md))*

Multi-actor collaboration builds on [kisage](https://github.com/Kaikei-e/kisage)'s leaf-path ownership model:

- Ownership metadata is stored under a separate key (`/t/<tenant>/own/...`) and updated in the same compare-and-swap (`Txn`) as the object body, keeping object reads and watch streams free of embedded `managedFields` bloat.
- Ownership conflicts, structural collisions, and external drift fail closed with explicit diagnostics; there is no `--force` flag to silently clobber another actor's fields, and `apply` is separated from conservative `prune`.

### 4. Control Plane Shielded from Heartbeat & Watch Floods

- **Zero steady-state heartbeat writes to consensus**: The internal store contract deliberately omits Leases and TTL keys ([ADR 0004](docs/ADR/0004-fix-a-subset-of-the-etcd-v3-api-without-leases-as-the-store-contract.md)). Node heartbeats stay in memory on liveness aggregators using an exact integer φ accrual failure detector, rendezvous-hashed assignment, indirect peer/witness probing, and local-health self-suspicion. Only confirmed transitions (`Ready` / `NotReady`) and globally rate-limited eviction permits are committed via CAS — scaling consensus writes with actual failures rather than cluster size ([ADR 0008](docs/ADR/0008-keep-node-liveness-as-soft-state-and-write-only-confirmed-transitions-to-the-store.md), [ADR 0024](docs/ADR/0024-realize-node-liveness-with-an-exact-phi-test-a-deterministic-detector-core-a-node-registry-and-a-rate-limited-eviction-permit.md)).
- **Per-(tenant, kind) watch cache**: Reads, exact-revision snapshots, periodic bookmarks, and streaming `watch list` initial syncs are served from memory backed by a persistent path-copying B-tree and closed-revision tracking, preventing noisy tenants from evicting other tenants' watch history ([ADR 0007](docs/ADR/0007-serve-watches-from-a-per-tenant-cache-with-revisions-bookmarks-and-watch-list.md), [ADR 0023](docs/ADR/0023-realize-the-watch-cache-as-a-library-with-a-persistent-b-tree-closed-revisions-and-an-in-process-compaction-bound.md)).

### 5. Proven Data Planes Without Unnecessary Layers

Nayutar starts on a 3-member etcd v3.6 cluster behind a swappable `Store` contract ([ADR 0005](docs/ADR/0005-start-on-a-single-etcd-cluster-with-tenant-key-prefixes.md)) and plans to drive containerd 2.3 LTS directly via its Go client alongside standard CNI plugins, CSI gRPC services, and default-deny `nftables` chains — reusing Linux and cloud-native data planes without routing through kubelet-specific CRI (`PodSandbox`) abstractions ([ADR 0014](docs/ADR/0014-drive-containerd-directly-without-cri.md), [ADR 0016](docs/ADR/0016-enforce-default-deny-network-policy-with-nftables-first.md)).

### 6. Machine-Checkable Assurance for Solo + AI Development

Making a full-scale orchestrator tractable for one person and AI coding agents requires external, mechanical oracles that agents cannot game ([ADR 0002](docs/ADR/0002-implement-the-core-in-go-and-write-specifications-in-lean-4.md), [ADR 0003](docs/ADR/0003-assure-the-implementation-by-differential-testing-against-lean-models-instead-of-proving-it.md), [ADR 0017](docs/ADR/0017-build-the-drt-harness-and-the-theorem-statement-guard-first-and-defer-the-models-the-esr-proof-and-the-kubernetes-oracle.md), [ADR 0021](docs/ADR/0021-guard-the-trust-root-of-the-theorem-statements-on-the-server-and-run-the-lean-and-drt-workflows-on-every-change.md)):

1. **Executable Lean 4 Models & Protected Proofs**: Pure state-machine cores are modeled in `spec/`. Agents may write proofs under `spec/Nayutar/Proofs/`, while theorem statements under `spec/Nayutar/Statements/` and `spec/comparator.json` are owned by the maintainer and verified inside a `bwrap` sandbox via [`lake comparator`](https://github.com/leanprover/comparator).
2. **Differential Random Testing (DRT)**: Seeded Go generators stream thousands of multi-step operation traces over NDJSON to `nayutar-drt` and compare every step's output field-for-field, backed by mutation tests that verify broken implementations are caught.
3. **Linearizability & Fault Injection**: Store backends and watch caches are checked with [Porcupine](https://github.com/anishathalye/porcupine) across leader kills, majority loss, and silent peer-proxy network partitions on real 3-member etcd v3.6 clusters ([ADR 0020](docs/ADR/0020-realize-the-single-etcd-cluster-and-inject-majority-loss-and-network-partitions-into-its-tests.md)).
4. **Deterministic Simulation & External Oracles**: Liveness runs across 19 multi-fault scenarios on a seeded event queue (`livesim`), with system-wide simulation interfaces ([ADR 0015](docs/ADR/0015-route-all-external-effects-through-simulation-interfaces-and-pick-the-simulator-after-evaluating-gosim.md)) and differential testing against Kubernetes v1.37 (`kind` / `kwok`) scheduled for upcoming milestones.

---

## Architecture Overview

### Keyspace Layout

All persistent keys conform to a strict lowercase ASCII codec ([`core/store/keyspace`](core/store/keyspace)) with a single canonical byte representation:

```text
/t/<tenant>/o/<group>/<kind>/<name>               Object envelope (LKAL-JSON)
/t/<tenant>/own/<group>/<kind>/<name>             Leaf-path ownership (planned, ADR 0013)
/t/<tenant>/ref/<tQKind>/<tName>/<sQKind>/<sName> Back-reference index
/t/<tenant>/bind/<identityHash>                   Exported API binding (planned)
/x/<identityHash>/schema/<version>                Exported API schema (planned)
/sys/tenants/<tenant>                             Tenant lifecycle & mod_revision fence
/sys/nodes/<node>                                 Node registration, state & eviction permit
/sys/liveness/evictions                           Cluster-wide rolling eviction budget
```

### Verification-Gated Progression

Nayutar progresses through explicit **verification gates (G0–G4)** rather than shipping intermediate partial products ([ADR 0001](docs/ADR/0001-aim-for-a-full-kubernetes-replacement-on-fixed-premises.md)):

- **G0 (Specifications & Harnesses — current focus, [ADR 0018](docs/ADR/0018-define-the-g0-verification-gate-pass-criteria.md))**: Store/watch and CAS ownership Lean models + DRT, watch invariant proofs, abstract-state-machine ESR proofs, deterministic simulator evaluation, and automated differential tests against Kubernetes v1.37.
- **G1–G4 (Scale, Multi-Tenancy & Ecosystem)**: 10,000-node control-plane latency and 72-hour liveness fault injection (G1), 50,000-node / 1,000-tenant store sharding and `nftables` vs. `cilium/ebpf` benchmarks (G2), and higher-level workload/controller gates.

---

## Current Status

### Implemented & Verified Today

| Area | Go Packages | Lean 4 Spec & Proofs | Verification Today |
| --- | --- | --- | --- |
| **DRT & Theorem Guard** ([ADR 0017](docs/ADR/0017-build-the-drt-harness-and-the-theorem-statement-guard-first-and-defer-the-models-the-esr-proof-and-the-kubernetes-oracle.md), [0021](docs/ADR/0021-guard-the-trust-root-of-the-theorem-statements-on-the-server-and-run-the-lean-and-drt-workflows-on-every-change.md)) | [`core/drt`](core/drt), [`cmd/check-protected-theorems`](cmd/check-protected-theorems) | `Fixture/Register.lean` (3 theorems) | NDJSON DRT runner, payload/implementation mutation checks, sandboxed `lake comparator` |
| **Store Contract & Backends** ([ADR 0004](docs/ADR/0004-fix-a-subset-of-the-etcd-v3-api-without-leases-as-the-store-contract.md), [0005](docs/ADR/0005-start-on-a-single-etcd-cluster-with-tenant-key-prefixes.md), [0020](docs/ADR/0020-realize-the-single-etcd-cluster-and-inject-majority-loss-and-network-partitions-into-its-tests.md)) | [`core/store`](core/store), `memstore`, `etcdstore`, `storetest` | `Store.lean` | Contract suite, Porcupine linearizability, 3-member etcd member kills, majority loss & peer-proxy network partitions, DRT (`store`) |
| **Keyspace & Tenant Registry** ([ADR 0006](docs/ADR/0006-put-the-tenant-at-the-top-of-the-key-space-and-keep-namespaces-out-of-keys.md), [0022](docs/ADR/0022-realize-the-key-space-with-a-strict-segment-grammar-a-key-codec-and-a-fenced-tenant-registry.md)) | [`core/store/keyspace`](core/store/keyspace), [`core/store/tenant`](core/store/tenant) | `Keyspace.lean` | Fuzzing, property tests, `mod_revision`-fenced views, resumable 2-phase tenant deletion on `memstore` & etcd, DRT (`keyspace`) |
| **Watch Cache** ([ADR 0007](docs/ADR/0007-serve-watches-from-a-per-tenant-cache-with-revisions-bookmarks-and-watch-list.md), [0023](docs/ADR/0023-realize-the-watch-cache-as-a-library-with-a-persistent-b-tree-closed-revisions-and-an-in-process-compaction-bound.md)) | [`core/store/watchcache`](core/store/watchcache) (`btree`, `window`) | `WatchCache.lean` (4 theorems) | No-loss, no-duplicates, order-preservation, and bookmark-monotonicity proofs; linearizability across etcd leader failure; DRT (`watchcache`) |
| **Node Liveness** ([ADR 0008](docs/ADR/0008-keep-node-liveness-as-soft-state-and-write-only-confirmed-transitions-to-the-store.md), [0024](docs/ADR/0024-realize-node-liveness-with-an-exact-phi-test-a-deterministic-detector-core-a-node-registry-and-a-rate-limited-eviction-permit.md)) | [`core/liveness`](core/liveness) (`phi`, `hrw`, `detector`, `budget`, `livesim`) | `Liveness.lean` (9 theorems) | Safety proofs, DRT (`liveness`), 19 seeded fault-injection scenarios in `livesim`, `testing/synctest` runtime tests, etcd registry tests |
| **Resource Envelope** ([ADR 0009](docs/ADR/0009-fix-the-resource-envelope-to-meta-spec-and-status.md), [0025](docs/ADR/0025-realize-the-resource-envelope-with-a-lowercase-kind-two-write-entrances-a-pure-transition-core-and-an-object-store-binding.md)) | [`core/resource/envelope`](core/resource/envelope), [`core/resource`](core/resource) | `Envelope.lean` (10 theorems) | Envelope transition & generation/revision proofs, fuzzing, LKAL-JSON store binding on `memstore` & etcd, DRT (`envelope`) |
| **Reference Checking** ([ADR 0010](docs/ADR/0010-check-references-at-write-time-and-keep-them-inside-a-tenant.md), [0026](docs/ADR/0026-realize-reference-checking-with-a-pure-check-core-a-reference-schema-table-cedar-authorization-and-a-back-reference-index-in-the-object-transaction.md)) | [`core/resource/refs`](core/resource/refs), `refcedar`, [`core/resource`](core/resource) | `Refs.lean` (8 theorems) | Write-time reference & index-delta proofs, `cedar-go` authorization, atomic CAS back-reference index tests on `memstore` & etcd, DRT (`refs`) |
| **Local Dev etcd Launcher** ([ADR 0020](docs/ADR/0020-realize-the-single-etcd-cluster-and-inject-majority-loss-and-network-partitions-into-its-tests.md)) | [`cmd/dev-etcd`](cmd/dev-etcd), [`config/dev.lkal`](config/dev.lkal) | — | Launches a local 3-member etcd v3.6.16 cluster declared in `.lkal` |

### Not Implemented Yet

- **HTTP/2 API server (`lk api`)**, client authentication, path-based Patch (`test`/`set`/`del`), and deterministic LKAL-CBOR wire encoding ([ADR 0012](docs/ADR/0012-use-lkal-json-on-the-wire-first-and-add-untagged-deterministic-cbor-later.md)).
- **CAS leaf-path ownership & apply/prune** ([ADR 0013](docs/ADR/0013-store-leaf-path-ownership-under-its-own-key-and-update-it-in-the-same-transaction.md)) and ordered record-set validation ([ADR 0011](docs/ADR/0011-express-ordered-record-sets-with-an-explicit-integer-order-leaf.md)).
- **API Export / `Binding`** (`identityHash`-qualified resolution and controller read-scope enforcement), controller SDK, and **Eventually Stable Reconciliation (ESR)** proofs.
- **Node agent (`lk-node`)**, direct containerd/CNI/CSI execution ([ADR 0014](docs/ADR/0014-drive-containerd-directly-without-cri.md)), default-deny `nftables` policy enforcement ([ADR 0016](docs/ADR/0016-enforce-default-deny-network-policy-with-nftables-first.md)), liveness gRPC transport (`lk live`), and scheduler.
- **System-wide deterministic simulator selection** ([ADR 0015](docs/ADR/0015-route-all-external-effects-through-simulation-interfaces-and-pick-the-simulator-after-evaluating-gosim.md)) and **Kubernetes v1.37 differential test harness** (`kind` / `kwok`).

---

## Quick Start

### Prerequisites

- **Required**: **Go 1.27.3** or newer (runs all default unit tests, linearizability checks, fuzz targets, and `livesim` with zero external dependencies).
- **Optional (Formal Specs & DRT)**: **Lean 4.35.0** via [`elan`](https://github.com/leanprover/elan), plus `bwrap` ([bubblewrap](https://github.com/containers/bubblewrap) with unprivileged user namespaces enabled) for `lake comparator`.
- **Optional (etcd Integration & Fault Injection)**: **etcd v3.6.16** (`etcd` in `PATH` or pointed to by `NAYUTAR_ETCD_BIN`).
- **Optional (Declarations & ADRs)**: [`lkal`](https://github.com/Kaikei-e/LKAL) and [`docdag`](https://github.com/Kaikei-e/DocDag).

### 1. Run Core Tests & Simulations (Go Only)

```sh
go build ./...
go test ./...
go test -race ./...
```

To run the seeded node-liveness fault-injection simulator with more seeds (default is 30 seeds across 19 scenarios):

```sh
NAYUTAR_SIM_SEEDS=100 go test ./core/liveness/livesim/
```

### 2. Build Lean 4 Proofs & Run Differential Random Tests

```sh
# Build all Lean models and sorry-free proofs
(cd spec && lake build)

# Verify that all 34 protected theorem statements and permitted axioms match
go build -o "${TMPDIR:-/tmp}/check-protected-theorems" ./cmd/check-protected-theorems \
  && "${TMPDIR:-/tmp}/check-protected-theorems"
(cd spec && lake comparator)

# Run all 7 Lean-Go differential random test suites (1,000 cases per suite by default)
go test -tags drt ./core/drt/...
```

### 3. Run 3-Member etcd Fault-Injection Suites

```sh
# Contract tests, Porcupine linearizability, member kills, majority loss & network partitions
go test -tags etcd ./core/store/... ./core/liveness/ ./core/resource/

# Run Lean-Go differential random tests directly against a 3-member etcd cluster
go test -tags "drt etcd" ./core/drt/...
```

You can also launch a standalone 3-member local development etcd cluster declared in [`config/dev.lkal`](config/dev.lkal) (requires `lkal` and `etcd` 3.6.16):

```sh
go run ./cmd/dev-etcd
```

---

## Repository Layout & ADRs

```text
cmd/                    CLI tools (dev-etcd launcher, protected-theorem checker)
config/                 Local environment declarations in LKAL (config/dev.lkal)
core/
  drt/                  Lean–Go differential random test runner and suites
  liveness/             Soft-state node liveness, integer phi detector, budget & livesim
  resource/             Resource envelope, write-time reference checks, Cedar & store binding
  store/                Store contract, memstore, etcdstore, keyspace, tenant & watchcache
docs/ADR/               Architecture Decision Records (validated by DocDag)
scripts/agent-harness/  Guard hooks and self-checks for coding agents
spec/                   Independent Lean 4 Lake package (models, statements, proofs, DRT exe)
```

### Architecture Decision Map

Design changes are proposed as ADRs before implementation. Accepted ADRs are immutable (`docdag validate --immutable-since`):

- **Foundations & Verification (`accepted`)**: [0001](docs/ADR/0001-aim-for-a-full-kubernetes-replacement-on-fixed-premises.md) (Fixed premises) · [0002](docs/ADR/0002-implement-the-core-in-go-and-write-specifications-in-lean-4.md) (Go + Lean 4) · [0003](docs/ADR/0003-assure-the-implementation-by-differential-testing-against-lean-models-instead-of-proving-it.md) (DRT & oracles) · [0017](docs/ADR/0017-build-the-drt-harness-and-the-theorem-statement-guard-first-and-defer-the-models-the-esr-proof-and-the-kubernetes-oracle.md) (DRT harness & `lake comparator` guard; expanded in [0021](docs/ADR/0021-guard-the-trust-root-of-the-theorem-statements-on-the-server-and-run-the-lean-and-drt-workflows-on-every-change.md)) · [0018](docs/ADR/0018-define-the-g0-verification-gate-pass-criteria.md) (G0 pass criteria)
- **Store, Keyspace, Watch & Liveness (`accepted` → `proposed` realizations)**: [0004](docs/ADR/0004-fix-a-subset-of-the-etcd-v3-api-without-leases-as-the-store-contract.md) (Lease-free `Store` contract) · [0005](docs/ADR/0005-start-on-a-single-etcd-cluster-with-tenant-key-prefixes.md) → [0020](docs/ADR/0020-realize-the-single-etcd-cluster-and-inject-majority-loss-and-network-partitions-into-its-tests.md) (Single 3-node etcd & fault injection) · [0006](docs/ADR/0006-put-the-tenant-at-the-top-of-the-key-space-and-keep-namespaces-out-of-keys.md) → [0022](docs/ADR/0022-realize-the-key-space-with-a-strict-segment-grammar-a-key-codec-and-a-fenced-tenant-registry.md) (Tenant-top keyspace & fenced registry) · [0007](docs/ADR/0007-serve-watches-from-a-per-tenant-cache-with-revisions-bookmarks-and-watch-list.md) → [0023](docs/ADR/0023-realize-the-watch-cache-as-a-library-with-a-persistent-b-tree-closed-revisions-and-an-in-process-compaction-bound.md) (Per-tenant watch cache) · [0008](docs/ADR/0008-keep-node-liveness-as-soft-state-and-write-only-confirmed-transitions-to-the-store.md) → [0024](docs/ADR/0024-realize-node-liveness-with-an-exact-phi-test-a-deterministic-detector-core-a-node-registry-and-a-rate-limited-eviction-permit.md) (Soft-state node liveness)
- **Resources & References (`accepted` → `proposed` realizations)**: [0009](docs/ADR/0009-fix-the-resource-envelope-to-meta-spec-and-status.md) → [0025](docs/ADR/0025-realize-the-resource-envelope-with-a-lowercase-kind-two-write-entrances-a-pure-transition-core-and-an-object-store-binding.md) (`meta`/`spec`/`status` envelope) · [0010](docs/ADR/0010-check-references-at-write-time-and-keep-them-inside-a-tenant.md) → [0026](docs/ADR/0026-realize-reference-checking-with-a-pure-check-core-a-reference-schema-table-cedar-authorization-and-a-back-reference-index-in-the-object-transaction.md) (Write-time reference checks & Cedar)
- **Upcoming Subsystems (`proposed`)**: [0011](docs/ADR/0011-express-ordered-record-sets-with-an-explicit-integer-order-leaf.md) (Ordered record sets) · [0012](docs/ADR/0012-use-lkal-json-on-the-wire-first-and-add-untagged-deterministic-cbor-later.md) (LKAL-JSON / Patch / CBOR wire format) · [0013](docs/ADR/0013-store-leaf-path-ownership-under-its-own-key-and-update-it-in-the-same-transaction.md) (Transactional leaf-path ownership) · [0014](docs/ADR/0014-drive-containerd-directly-without-cri.md) (Direct containerd without CRI) · [0015](docs/ADR/0015-route-all-external-effects-through-simulation-interfaces-and-pick-the-simulator-after-evaluating-gosim.md) (Simulation I/O interfaces) · [0016](docs/ADR/0016-enforce-default-deny-network-policy-with-nftables-first.md) (Default-deny `nftables` network policy)

---

## Prior Art & Credits

Nayutar builds directly on [LKAL](https://github.com/Kaikei-e/LKAL), [kisage](https://github.com/Kaikei-e/kisage), [DocDag](https://github.com/Kaikei-e/DocDag), [etcd](https://etcd.io/), [Lean 4](https://lean-lang.org/) and [`comparator`](https://github.com/leanprover/comparator), [Cedar (`cedar-go`)](https://github.com/cedar-policy/cedar-go), and [Porcupine](https://github.com/anishathalye/porcupine). Specific design mechanisms also draw on primary specifications and research cited in individual ADRs, including consistent reads from cache ([KEP-2340](https://github.com/kubernetes/enhancements/blob/master/keps/sig-api-machinery/2340-Consistent-reads-from-cache/README.md)) and streaming watch-list ([KEP-3157](https://github.com/kubernetes/enhancements/blob/master/keps/sig-api-machinery/3157-watch-list/README.md)), `APIExport`/`APIBinding` identity hashing ([kcp](https://docs.kcp.io/kcp/main/concepts/apis/exporting-apis/)), analysis of cross-namespace and cluster-scoped operator reference vulnerabilities (["Breaking the Bulkhead", NDSS 2026](https://arxiv.org/abs/2507.03387)), the φ accrual failure detector ([Hayashibara et al., SRDS 2004](https://doi.org/10.1109/RELDIS.2004.1353004) and [Apache Pekko](https://github.com/apache/pekko)), SWIM indirect probing ([Das et al., DSN 2002](https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf)), Lifeguard self-awareness ([Dadgar et al., 2017](https://arxiv.org/abs/1707.00788)), and rely-guarantee controller composition principles ([Welder](https://doi.org/10.1145/3830418.3843868), referenced strictly as literature with no code or toolchain dependency).

## Contributing & Security

- [`AGENTS.md`](AGENTS.md): Repository map, area-specific rules, verification gates, and protected-path guardrails for contributors and coding agents.
- [`SECURITY.md`](SECURITY.md): Supported versions and private vulnerability reporting policy.

## License

Apache License 2.0. See [LICENSE](LICENSE).
