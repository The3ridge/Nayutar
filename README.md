# Nayutar

**English** | [日本語](README.ja.md)

Nayutar is a container orchestrator that aims to be a **full alternative to Kubernetes**. It is at an early stage: the foundation is built and verified, and the parts that run containers are not built yet.

> [!IMPORTANT]
> **You cannot deploy workloads with Nayutar yet.** There is no API server, no CLI, no node agent and no scheduler.
>
> Today Nayutar is a set of Go libraries: a store, tenant isolation, a watch cache, node failure detection and a resource model. You can call them from Go and run the checks that verify them. This README shows how.

## Contents

- [Get started](#get-started)
- [Words you will meet](#words-you-will-meet)
- [What exists and what does not](#what-exists-and-what-does-not)
- [How Nayutar is verified](#how-nayutar-is-verified)
- [Roadmap](#roadmap)
- [Repository layout](#repository-layout)
- [Learn more](#learn-more)
- [Prior art](#prior-art)

## Get started

Steps 1 and 2 need only Go 1.27.3 or newer. Steps 3 and 4 are optional and need one more tool each.

### 1. Get the code and run the tests

```sh
git clone https://github.com/Kaikei-e/Nayutar.git
cd Nayutar
go test ./...
```

Every package should end with `ok`. This step needs no etcd and no Lean.

### 2. Store your first objects

This program registers a tenant, declares two kinds of resource, and stores one object of each. It uses the in-memory store, so nothing needs to be running.

Save it as `tour/main.go` inside the clone:

```go
package main

import (
	"context"
	"fmt"
	"log"

	lkal "github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
)

// A schema says what a kind of resource may contain.
const workloadSchema = `@lkal "0.1"
lkal-schema = "0.2"

root {
  field "spec" {
    type = .record
    field "replicas" { type = .int, min = 0, default = 2 }
    field "image" { type = .string, required = true }
    field "creds" { type = .ref, targets = [&secret] }
  }
}
`

const secretSchema = `@lkal "0.1"
lkal-schema = "0.2"

root {
  field "spec" {
    type = .record
    field "data" { type = .string }
  }
}
`

// A declaration is what a person writes.
const secret = `@lkal "0.1"

meta { kind = "secret", name = "db-creds" }
spec { data = "s3cr3t" }
`

const workload = `@lkal "0.1"

meta { kind = "workload", name = "web" }
spec {
  image = "ghcr.io/acme/web:1.5.0"
  creds = &secret/%s
}
`

type allowAll struct{}

func (allowAll) Authorize(refs.Request) bool { return true }

func main() {
	ctx := context.Background()

	registry := tenant.NewRegistry(memstore.New())
	must(registry.Register(ctx, "acme"))
	view, err := registry.View(ctx, "acme")
	must(err)

	objects := resource.NewObjects(view, resource.Config{
		Tenant: "acme",
		Schemas: resource.Schemas{
			"workload": kind("workload.schema.lkal", workloadSchema),
			"secret":   kind("secret.schema.lkal", secretSchema),
		},
		Authorizer: allowAll{},
	})
	alice := refs.Principal{Type: "User", ID: "alice"}

	// A reference to an object that does not exist is rejected.
	_, err = objects.Create(ctx, alice, parse(fmt.Sprintf(workload, "missing")))
	fmt.Println("dangling reference:", err)

	_, err = objects.Create(ctx, alice, parse(secret))
	must(err)
	created, err := objects.Create(ctx, alice, parse(fmt.Sprintf(workload, "db-creds")))
	must(err)

	out, err := resource.Encode(created)
	must(err)
	fmt.Printf("created: %s\n", out)
}

func kind(name, src string) *resource.KindSchema {
	ks, err := resource.CompileKindSchema(name, []byte(src))
	must(err)
	return ks
}

func parse(src string) lkal.Record {
	f, err := lkal.Parse("input.lkal", []byte(src))
	must(err)
	return f.Data()
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
```

Run it:

```sh
go run ./tour
```

```text
dangling reference: refs: notFound: spec/creds &secret/missing: target object not found
created: {"$lkal":"0.1","meta":{"kind":"workload","name":"web","uid":"bad176a3-5652-4b0b-92a2-fe9ad3e8b27c","generation":1,"revision":4},"spec":{"image":"ghcr.io/acme/web:1.5.0","creds":{"$ref":"secret/db-creds"},"replicas":2}}
```

The `uid` differs on every run. Three things happened that the declaration did not ask for:

- **The broken reference was refused.** The first workload pointed at a secret that did not exist, so the write failed and nothing was stored.
- **The default was filled in.** The declaration has no `replicas`. The schema says the default is 2, and the stored object has `replicas: 2`.
- **The server set its own fields.** `uid`, `generation` and `revision` come from the store. A write that tries to set them is rejected.

The example allows every reference. [`core/resource/refcedar`](core/resource/refcedar) provides an authorizer that decides with [Cedar](https://docs.cedarpolicy.com/) policies.

Delete the `tour` directory when you are done. The API is not stable and will change.

### 3. Run the same program on etcd (optional)

The store is a contract, and etcd is one implementation of it. This step needs etcd v3.6.16.

Download etcd and point Nayutar at it (Linux x86-64 shown):

```sh
curl -fsSL https://github.com/etcd-io/etcd/releases/download/v3.6.16/etcd-v3.6.16-linux-amd64.tar.gz | tar xz
export NAYUTAR_ETCD_BIN="$PWD/etcd-v3.6.16-linux-amd64/etcd"
```

Start a local 3-member cluster and leave it running. Its members are declared in [`config/dev.lkal`](config/dev.lkal).

```sh
go run ./cmd/dev-etcd -data-dir "$(mktemp -d)"
```

In `tour/main.go`, replace the `memstore` import with these two:

```go
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore"
	clientv3 "go.etcd.io/etcd/client/v3"
```

and replace the line that creates the registry with:

```go
	client, err := clientv3.New(clientv3.Config{Endpoints: []string{"http://127.0.0.1:12379"}})
	must(err)
	defer client.Close()
	registry := tenant.NewRegistry(etcdstore.New(client))
```

Run `go run ./tour` in another terminal. The output is the same, and the keys are now in etcd:

```sh
etcd-v3.6.16-linux-amd64/etcdctl --endpoints=http://127.0.0.1:12379 get --prefix / --keys-only
```

```text
/sys/tenants/acme
/t/acme/o/core/secret/db-creds
/t/acme/o/core/workload/web
/t/acme/ref/secret/db-creds/workload/web
```

Everything the tenant owns is under `/t/acme/`. The last key is the back-reference index: it records that `web` refers to `db-creds`.

With etcd available you can also run the tests that kill members and partition the network:

```sh
go test -tags etcd ./core/store/... ./core/liveness/ ./core/resource/
```

### 4. Check the specifications (optional)

The behaviour you saw in step 2 is specified in Lean 4. This step needs Lean through [`elan`](https://github.com/leanprover/elan), which installs the version the repository pins.

```sh
# Build the Lean models and proofs
(cd spec && lake build)

# Run random operations on both the Go code and the Lean models, and compare
go test -tags drt ./core/drt/...
```

One more check confirms that every proof proves the theorem as the maintainer stated it. It needs `bwrap` ([bubblewrap](https://github.com/containers/bubblewrap), with unprivileged user namespaces):

```sh
(cd spec && lake comparator)
```

The complete list of verification commands is in [`AGENTS.md`](AGENTS.md#検証コマンド).

## Words you will meet

| Word | Meaning |
| --- | --- |
| **LKAL** | The language for declarations and configuration (`.lkal`). Nayutar uses it where Kubernetes uses YAML. See [LKAL](https://github.com/Kaikei-e/LKAL). |
| **Tenant** | The unit of isolation. Every key a tenant owns is under `/t/<tenant>/`. |
| **Kind** | A type of resource, such as `workload` or `secret`. A schema defines each kind. |
| **Envelope** | The fixed shape of every resource: `meta` (identity), `spec` (what you want) and `status` (what was observed). |
| **Reference** | A typed pointer from one object to another, written `&secret/db-creds`. It is checked when it is written and cannot leave its tenant. |
| **Store contract** | The interface every backend implements: a subset of the etcd v3 API without leases. `memstore` and `etcdstore` implement it. |
| **DRT** | Differential random testing. The same random operations run on the Go code and on the Lean model, and the results must match. |
| **Verification gate** | A stage of the project, G0 to G4, that is passed by meeting measurable criteria. |
| **ADR** | A decision record in [`docs/ADR/`](docs/ADR/). Every design decision is written there before the code. |

## What exists and what does not

These are Go libraries with tests. None is reachable over a network API yet.

| Area | What it does | Code |
| --- | --- | --- |
| Store contract | A subset of the etcd v3 API without leases, with an in-memory implementation and an etcd v3.6 adapter. | [`core/store`](core/store) |
| Tenant keyspace | Puts every key of a tenant under one prefix. A registry fences writes, so a write that arrives after its tenant is deleted is refused. | [`core/store/keyspace`](core/store/keyspace), [`core/store/tenant`](core/store/tenant) |
| Watch cache | Serves reads, snapshots at an exact revision and watches from memory, with one cache per tenant and kind. | [`core/store/watchcache`](core/store/watchcache) |
| Node liveness | Detects failed nodes from in-memory heartbeats and writes only confirmed state changes to the store. | [`core/liveness`](core/liveness) |
| Resource envelope | Fixes the shape of every resource and separates writes of intent from writes of status. | [`core/resource/envelope`](core/resource/envelope) |
| Reference checks | Checks each new reference when it is written, in the same transaction as the object. | [`core/resource/refs`](core/resource/refs) |
| Kind schemas | Validates each kind, fills in defaults and derives the reference rules. | [`core/resource`](core/resource) |

Not built yet:

- **API server and CLI.** There is no way to send a declaration over the network.
- **Field ownership**, `apply` and `prune`, for several actors editing one object.
- **Node agent**: running containers through containerd, networking, storage, network policy.
- **Scheduler and controllers.**
- **YAML import** for Helm charts, Kubernetes manifests and Compose files.

## How Nayutar is verified

One developer builds Nayutar together with AI coding agents. One person cannot review everything the agents write, so the maintainer owns the specifications and machines check the code against them.

| Check | What it does | Scope today |
| --- | --- | --- |
| **Lean 4 proofs** | Models of the core state machines live in [`spec/`](spec), with theorems about them. | 34 theorems |
| **Differential random testing** | Runs random operations on the Go code and the Lean model, and compares every step. | 7 suites, 1,000 cases each by default |
| **Linearizability and fault injection** | Checks operation histories on a real 3-member etcd cluster while members are killed, the majority is lost, or the network is partitioned. | Store, watch cache |
| **Deterministic simulation** | Replays multi-fault scenarios on a seeded event queue. | Node liveness: 19 scenarios |
| **Kubernetes as an oracle** | Compares behaviour with Kubernetes v1.37. | Planned |

Agents may write proofs but not theorem statements. Hooks in [`scripts/agent-harness/`](scripts/agent-harness) stop an agent from editing the statements or other protected files.

## Roadmap

Work is organised by verification gates, not by releases.

| Gate | Focus |
| --- | --- |
| **G0** (current) | Specifications and test harnesses. In progress, not passed yet. |
| **G1** | One cluster at scale: 10,000 simulated nodes |
| **G2** | Many tenants at scale: 50,000 simulated nodes and 1,000 tenants |
| **G3–G4** | Later stages. Their criteria are not written yet. |

The node counts are initial targets and may change.

## Repository layout

```text
cmd/                    dev-etcd launcher, protected-theorem checker
config/                 Development environment declared in LKAL
core/
  drt/                  Differential random tests between Go and Lean
  liveness/             Node liveness: detector, registry, simulator
  resource/             Resource envelope, kind schemas, reference checks
  store/                Store contract, memstore, etcdstore, keyspace, tenant, watchcache
docs/ADR/               Decision records (in Japanese)
scripts/agent-harness/  Guard hooks for coding agents
spec/                   Lean 4 models, theorem statements and proofs
```

## Learn more

- **Why each design was chosen**: the decision records in [`docs/ADR/`](docs/ADR/), starting with [the fixed premises](docs/ADR/0001-aim-for-a-full-kubernetes-replacement-on-fixed-premises.md). They are in Japanese.
- **Working on the code**: [`AGENTS.md`](AGENTS.md) is the map of the repository. It lists the rules for each area and every verification command. It is written for coding agents and is the best starting point for people too.
- **Reporting a vulnerability**: [`SECURITY.md`](SECURITY.md).

## Prior art

Nayutar builds on [LKAL](https://github.com/Kaikei-e/LKAL) for declarations, [kisage](https://github.com/Kaikei-e/kisage) for the ownership model, [DocDag](https://github.com/Kaikei-e/DocDag) for decision records, [etcd](https://etcd.io/), [Lean 4](https://lean-lang.org/) with [`comparator`](https://github.com/leanprover/comparator), [Cedar](https://github.com/cedar-policy/cedar-go) and [Porcupine](https://github.com/anishathalye/porcupine).

Individual designs draw on the following work. The decision records cite it in detail.

| Design | Source |
| --- | --- |
| Consistent reads from the watch cache | [KEP-2340](https://github.com/kubernetes/enhancements/blob/master/keps/sig-api-machinery/2340-Consistent-reads-from-cache/README.md) |
| Streaming watch-list | [KEP-3157](https://github.com/kubernetes/enhancements/blob/master/keps/sig-api-machinery/3157-watch-list/README.md) |
| API export, binding and identity hashes | [kcp](https://docs.kcp.io/kcp/main/concepts/apis/exporting-apis/) |
| Reference attacks across isolation boundaries | ["Breaking the Bulkhead", NDSS 2026](https://arxiv.org/abs/2507.03387) |
| φ accrual failure detector | [Hayashibara et al., SRDS 2004](https://doi.org/10.1109/RELDIS.2004.1353004), [Apache Pekko](https://github.com/apache/pekko) |
| Indirect probing | [SWIM, Das et al., DSN 2002](https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf) |
| Local health awareness | [Lifeguard, Dadgar et al., 2017](https://arxiv.org/abs/1707.00788) |
| Rely-guarantee composition of controllers | [Welder](https://doi.org/10.1145/3830418.3843868) (design principles only, no code or toolchain dependency) |

## License

Apache License 2.0. See [LICENSE](LICENSE).
