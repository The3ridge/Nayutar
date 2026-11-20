# Nayutar

Nayutar is a project, built by one person together with AI coding agents, to
make an orchestrator that is a serious alternative to Kubernetes. Declarations
are built on [LKAL](https://github.com/Kaikei-e/LKAL), and ownership and apply
are built on the leaf-path ownership model of
[kisage](https://github.com/Kaikei-e/kisage). The survey and design notes are in
[`docs/research/poc.md`](docs/research/poc.md); decisions are recorded as ADRs in
[`docs/ADR/`](docs/ADR/).

## Status

Early. The G0 verification gate ([ADR 0018](docs/ADR/0018-define-the-g0-verification-gate-pass-criteria.md))
is not met, and Nayutar is not usable as an orchestrator.

What exists today:

- The store contract (`core/store`), an in-memory reference implementation
  (`memstore`), and an etcd v3.6 adapter (`etcdstore`).
- A tenant view that confines a store to a key prefix (`core/store/tenant`).
- A watch cache library (`core/store/watchcache`) that serves reads and watches
  from memory, with a Lean model, a DRT suite, and proofs of its four watch
  invariants. It has no HTTP serving yet.
- A node liveness library (`core/liveness`): heartbeats stay in memory, a phi
  accrual detector decides with integer arithmetic, and only confirmed
  transitions and rate-limited eviction permits are written to the store. It has
  a Lean model, a DRT suite, safety proofs, and a seeded simulation. It has no
  network transport, no aggregator membership protocol, and no binaries yet.
- A Lean 4 specification package under `spec/`, the Lean-Go differential random
  test (DRT) harness, and a guard for protected theorem statements.
- Tests against a three-member etcd cluster, including member failures, majority
  loss and network partitions, and a linearizability checker.
- A launcher for a three-member development etcd cluster (`cmd/dev-etcd`).

Not implemented yet: the API server, leaf-path ownership and apply, the resource
envelope and wire format, reference checks, the ESR proof, and differential
tests against Kubernetes.

## Requirements

- Go 1.27.3 or newer.
- Optional: etcd 3.6.16 for the `-tags etcd` tests (`NAYUTAR_ETCD_BIN` or `PATH`).
- Optional: Lean 4.35.0 (via elan) for `spec/` and the DRT.
- Optional: bubblewrap (`bwrap`) for `lake comparator`.

## Build and test

```sh
go build ./...
go test ./...
go test -tags drt ./core/drt/...                # needs lake
go test -tags etcd ./core/store/... ./core/liveness/    # needs etcd
go test -tags "drt etcd" ./core/drt/...         # needs lake and etcd
(cd spec && lake build)
```

## More

- [`AGENTS.md`](AGENTS.md): rules for contributors and coding agents.
- [`SECURITY.md`](SECURITY.md): how to report a vulnerability.

## License

Apache License 2.0. See [LICENSE](LICENSE).
