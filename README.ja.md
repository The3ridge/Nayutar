# Nayutar

[English](README.md) | **日本語**

Nayutar は、**Kubernetes の本格的な代替**を目指すコンテナオーケストレータです。まだ初期の段階です。中核のライブラリ群と単一ユニットのコンテナ実行は作って検証してありますが、コントロールプレーンのサーバ、スケジューラ、コントローラはこれから作ります。

> [!IMPORTANT]
> **Nayutar でクラスタにワークロードをデプロイすることは、まだできません。** API サーバの実行ファイル、クラスタ操作用の CLI、スケジューラ、コントローラはありません。
>
> いまの Nayutar は Go のライブラリ群（ストア、テナントの隔離、watch キャッシュ、ノードの障害検知、葉パス所有権つきのリソースモデル、HTTP/2 ワイヤ層、シミュレーション境界、containerd の直接駆動、既定拒否の `nftables` ネットワークポリシー）と、手元の `.lkal` ファイルから 1 つのコンテナユニットを動かす `lk-node` です。Go からライブラリを呼び出すことと、それらを検証する検査を実行することができます。この README はその手順を示します。

## 目次

- [使い始める](#使い始める)
- [出てくる言葉](#出てくる言葉)
- [あるものと、まだ無いもの](#あるものとまだ無いもの)
- [検証のしかた](#検証のしかた)
- [ロードマップ](#ロードマップ)
- [ディレクトリ構成](#ディレクトリ構成)
- [さらに知るには](#さらに知るには)
- [先行研究と関連プロジェクト](#先行研究と関連プロジェクト)

## 使い始める

手順 1 と 2 に必要なのは、Go 1.27.3 以上だけです。手順 3 と 4 は任意で、それぞれツールがもう 1 つ要ります。

### 1. コードを取得して試験を走らせる

```sh
git clone https://github.com/Kaikei-e/Nayutar.git
cd Nayutar
go test ./...
```

すべてのパッケージが `ok` で終われば成功です。この手順に etcd と Lean は要りません。

### 2. 最初のオブジェクトを保存する

次のプログラムは、テナントを登録し、リソースの kind を 2 つ宣言し、それぞれのオブジェクトを 1 つずつ保存します。インメモリのストアを使うので、何も起動しておく必要はありません。

clone したディレクトリの中に、`tour/main.go` として保存します。

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

実行します。

```sh
go run ./tour
```

```text
dangling reference: refs: notFound: spec/creds &secret/missing: target object not found
created: {"$lkal":"0.1","meta":{"kind":"workload","name":"web","uid":"bad176a3-5652-4b0b-92a2-fe9ad3e8b27c","generation":1,"revision":4},"spec":{"image":"ghcr.io/acme/web:1.5.0","creds":{"$ref":"secret/db-creds"},"replicas":2}}
```

`uid` は実行のたびに変わります。ここでは、宣言に書いていないことが 3 つ起きています。

- **壊れた参照は拒否されました。** 最初の workload は存在しない secret を指していたので、書き込みは失敗し、何も保存されませんでした。
- **既定値が補われました。** 宣言に `replicas` はありません。スキーマが既定値を 2 と定めているので、保存されたオブジェクトは `replicas: 2` を持ちます。
- **サーバが決める欄が入りました。** `uid`・`generation`・`revision` はストアが決めます。これらを設定しようとする書き込みは拒否されます。

この例は、すべての参照を許可しています。[`core/resource/refcedar`](core/resource/refcedar) に、[Cedar](https://docs.cedarpolicy.com/) のポリシーで判定する認可の実装があります。

終わったら `tour` ディレクトリを消してください。API は安定しておらず、これから変わります。

### 3. 同じプログラムを etcd で動かす（任意）

ストアは契約であり、etcd はその実装の 1 つです。この手順には etcd v3.6.16 が要ります。

etcd をダウンロードし、その場所を Nayutar に伝えます（Linux x86-64 の例）。

```sh
curl -fsSL https://github.com/etcd-io/etcd/releases/download/v3.6.16/etcd-v3.6.16-linux-amd64.tar.gz | tar xz
export NAYUTAR_ETCD_BIN="$PWD/etcd-v3.6.16-linux-amd64/etcd"
```

3 メンバーのクラスタを手元で起動し、そのまま動かしておきます。メンバーは [`config/dev.lkal`](config/dev.lkal) に宣言してあります。

```sh
go run ./cmd/dev-etcd -data-dir "$(mktemp -d)"
```

`tour/main.go` の `memstore` の import を、次の 2 行に置き換えます。

```go
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore"
	clientv3 "go.etcd.io/etcd/client/v3"
```

登録簿を作っている行を、次のように置き換えます。

```go
	client, err := clientv3.New(clientv3.Config{Endpoints: []string{"http://127.0.0.1:12379"}})
	must(err)
	defer client.Close()
	registry := tenant.NewRegistry(etcdstore.New(client))
```

別のターミナルで `go run ./tour` を実行します。出力は同じで、キーは etcd に入っています。

```sh
etcd-v3.6.16-linux-amd64/etcdctl --endpoints=http://127.0.0.1:12379 get --prefix / --keys-only
```

```text
/sys/tenants/acme
/t/acme/o/core/secret/db-creds
/t/acme/o/core/workload/web
/t/acme/own/core/secret/db-creds
/t/acme/own/core/workload/web
/t/acme/ref/secret/db-creds/workload/web
```

テナントが持つものは、すべて `/t/acme/` の下にあります。`o/` がオブジェクト本体、`own/` が同じトランザクションで書かれた葉パスごとの所有権、`ref/` が `web` から `db-creds` への参照を記録する逆参照索引です。

etcd があれば、メンバーを停止させたりネットワークを分断したりする試験も実行できます。

```sh
go test -tags etcd ./core/store/... ./core/liveness/ ./core/resource/ ./core/wire/
```

### 4. 仕様を検査する（任意）

手順 2 で見た振る舞いは、Lean 4 で仕様として書いてあります。この手順には、[`elan`](https://github.com/leanprover/elan) で入れた Lean が要ります。elan は、リポジトリが固定している版を自動で入れます。

```sh
# Lean のモデルと証明をビルドする
(cd spec && lake build)

# 乱数で作った操作を Go のコードと Lean のモデルの両方で実行し、結果を比べる
go test -tags drt ./core/drt/...
```

もう 1 つの検査は、それぞれの証明が、メンテナが書いたとおりの定理を証明していることを確かめます。これには `bwrap`（非特権ユーザー名前空間を使える [bubblewrap](https://github.com/containers/bubblewrap)）が要ります。

```sh
(cd spec && lake comparator)
```

検証コマンドの全体は [`AGENTS.md`](AGENTS.md#検証コマンド) にあります。

## 出てくる言葉

| 言葉 | 意味 |
| --- | --- |
| **LKAL** | 宣言と設定を書く言語（`.lkal`）です。Kubernetes が YAML を使うところで、Nayutar は LKAL を使います。[LKAL](https://github.com/Kaikei-e/LKAL) を参照してください。 |
| **テナント** | 隔離の単位です。テナントが持つキーは、すべて `/t/<tenant>/` の下にあります。 |
| **kind** | リソースの種類です。`workload` や `secret` などがあり、それぞれをスキーマで定義します。 |
| **封筒** | すべてのリソースに共通の形です。`meta`（識別）、`spec`（望む状態）、`status`（観測した状態）から成ります。 |
| **参照** | あるオブジェクトから別のオブジェクトを指す、型のついたポインタです。`&secret/db-creds` と書きます。書き込み時に検査され、テナントの外は指せません。 |
| **葉パス所有権** | `meta/labels` と `spec` の末端の欄ごとに所有者を最大 1 つ記録する仕組みです。`/t/<tenant>/own/...` に分けて保存し、複数のアクターが互いの欄を上書きせずに `apply` や `prune` できるようにします。 |
| **ストア契約** | すべてのバックエンドが実装するインターフェースです。etcd v3 API から Lease を除いたサブセットで、`memstore` と `etcdstore` が実装しています。 |
| **DRT** | 差分ランダム試験です。同じランダムな操作を Go のコードと Lean のモデルの両方で実行し、結果が一致することを確かめます。 |
| **検証ゲート** | プロジェクトの段階です。G0 から G4 まであり、測定できる合格条件を満たすと合格です。 |
| **ADR** | [`docs/ADR/`](docs/ADR/) にある決定記録です。設計判断はすべて、コードを書く前にここに書きます。 |

## あるものと、まだ無いもの

次のものは、試験つきの Go ライブラリと、手元の `.lkal` ファイルから単一のコンテナユニットを動かす `lk-node` です。API サーバの実行ファイルはまだありません。

| 領域 | 何をするか | コード |
| --- | --- | --- |
| ストア契約 | etcd v3 API から Lease を除いたサブセットです。インメモリの実装と etcd v3.6 のアダプタがあります。 | [`core/store`](core/store) |
| テナントのキー空間 | テナントのキーをすべて 1 つの接頭辞の下に置きます。登録簿が書き込みをフェンスするので、テナントの削除後に届いた書き込みは拒否されます。 | [`core/store/keyspace`](core/store/keyspace)、[`core/store/tenant`](core/store/tenant) |
| watch キャッシュ | 読み取り、リビジョン指定のスナップショット、watch をメモリから返します。キャッシュはテナントと kind の組ごとに 1 つです。 | [`core/store/watchcache`](core/store/watchcache) |
| ノードの死活監視 | メモリに持った心拍から故障したノードを検知し、確定した状態の変化だけをストアに書きます。 | [`core/liveness`](core/liveness) |
| リソースの封筒と Patch | すべてのリソースの形（`meta`・`spec`・`status`）を固定し、意図の書き込みと状態の書き込みを分け、両方の入口に対するパス単位の Patch と Diff を備えます。 | [`core/resource/envelope`](core/resource/envelope)、[`core/resource/patch`](core/resource/patch) |
| kind のスキーマと順序付き集合 | LKAL v0.2 スキーマで kind ごとに内容を検査し、既定値を補い、参照の規則を導き、順序付きレコード集合（`order` 葉）を検査します。 | [`core/resource`](core/resource)、[`core/resource/orderedset`](core/resource/orderedset) |
| 参照の検査 | 追加された参照（および任意の Cedar ポリシー）を書き込み時に検査し、オブジェクト本体および逆参照索引と同じトランザクションで書きます。 | [`core/resource/refs`](core/resource/refs)、[`core/resource/refcedar`](core/resource/refcedar) |
| 葉パス所有権 | 葉パスごとの所有権を圧縮トライとして `/t/<tenant>/own/...` に格納し、本体・所有権・逆参照を 1 回の CAS トランザクションで更新します（`apply`・`prune`・`adopt`・`release`・`explain`）。 | [`core/resource/ownership`](core/resource/ownership)、[`core/resource`](core/resource) |
| HTTP/2 ワイヤ層 | リソース、Patch、所有権サブリソースの読み書きと watch を、単一行 LKAL-JSON の HTTP/2（`h2` と prior-knowledge `h2c`）で扱うハンドラと Go クライアントです。 | [`core/wire`](core/wire) |
| ノードのコンテナ実行 | CRI と `PodSandbox` を使わず containerd v2.3 を直接駆動し、Linux `netns` と CNI プラグインの連鎖を管理し、失敗時は逆順（LIFO）で解放します。 | [`core/node`](core/node)、[`cmd/lk-node`](cmd/lk-node) |
| 既定拒否のネットワークポリシー | `br_netfilter` に頼らずテナント別ブリッジ（`nbr-*`）でテナントを分け、異テナント間は送信・受信の双方向許可を求め、`nft -f -` で `nftables` の規則（送信元・ブリッジ族・ホスト入力のガードを含む）を原子的に適用します。 | [`core/netpol`](core/netpol) |
| シミュレーション境界 | 時計・通信・ディスク・乱数を `core/sim` のインターフェースに通し（AST 検査で強制）、決定論的イベントキューで複数サブシステムの障害シナリオを再生します。 | [`core/sim`](core/sim)、[`core/sim/clustersim`](core/sim/clustersim) |

まだ作っていないもの：

- **API サーバの実行ファイル、認証、クラスタ操作用の CLI。** HTTP/2 のハンドラとクライアント（`core/wire`）および手元の `.lkal` ファイルを読む `lk-node` はありますが、コントロールプレーンのサーバ実行ファイル、クライアント認証、`/x/` へのスキーマ保存、CLI はまだありません。
- **テナント間の `Export` と `Binding`**（`identityHash` の検証つき）と、コントローラの読み取り範囲の制限。
- **スケジューラ、ワークロードの kind、コントローラ**（および終端状態到達性の証明）、ならびに `lk-node` とコントロールプレーンの接続。
- **ノードのストレージ（`CSI Node`）、複数コンテナでの `netns` 共有、複数ノード間の経路制御。**
- **YAML の取り込み**：Helm チャート、Kubernetes のマニフェスト、Compose ファイル。

## 検証のしかた

Nayutar は、1 人の開発者が AI コーディングエージェントと一緒に作っています。エージェントが書いたものを 1 人ですべてレビューすることはできないので、仕様はメンテナが所有し、コードが仕様に合っているかは機械が検査します。

| 検査 | 内容 | いまの範囲 |
| --- | --- | --- |
| **Lean 4 の証明** | 中核の状態機械のモデルを [`spec/`](spec) に置き、それについての定理を証明します。 | 88 定理 |
| **差分ランダム試験** | ランダムな操作を Go のコードと Lean のモデルの両方で実行し、各ステップの結果を比べます。 | 12 スイート、既定で各 1,000 ケース |
| **線形化可能性検査と障害の注入** | 実物の 3 メンバー etcd クラスタで、メンバーの停止、過半数の喪失、ネットワークの分断を起こしながら、操作の履歴を検査します。 | ストア、watch キャッシュ、ノード登録簿、リソースのオブジェクト、HTTP/2 ワイヤ層 |
| **決定論的シミュレーション** | `core/sim` を通してシード固定のイベントキュー上で複合障害シナリオを再生し、同一シード 100 回実行でのトレースハッシュ一致を確かめます。 | クラスタ結合（`clustersim`）、ノードの死活監視（`livesim`）、ノードライフサイクル（`nodesim`） |
| **実カーネル・ランタイム結合試験** | CI の Linux 上で実物の `containerd` v2.3 + `runc`、Linux `netns`、CNI ブリッジ、`nftables`（送信元詐称・ブリッジ内 L2・ホスト入力ガードを含む）を動かして確かめます。 | ノード実行（`containerd`）、ネットワークポリシー（`nftables`） |
| **Kubernetes をオラクルにする** | 振る舞いを Kubernetes v1.37 と比べます。 | これから |

エージェントは証明を書けますが、定理の主張は書けません。[`scripts/agent-harness/`](scripts/agent-harness) のフックが、エージェントによる主張やほかの保護対象のファイルの編集を止めます。

## ロードマップ

作業の単位はリリースではなく、検証ゲートです。

| ゲート | 主題 |
| --- | --- |
| **G0**（現在） | 仕様と試験のハーネス。進行中で、まだ合格していません。 |
| **G1** | 1 クラスタの規模：模擬 10,000 ノード |
| **G2** | 多数のテナントでの規模：模擬 50,000 ノードと 1,000 テナント |
| **G3〜G4** | その先の段階。合格条件はまだ書いていません。 |

ノード数は初期値で、変わることがあります。

## ディレクトリ構成

```text
cmd/                    dev-etcd の起動、lk-node の単体実行、保護対象の定理の検査
config/                 LKAL で宣言した開発環境
core/
  drt/                  Go と Lean の差分ランダム試験
  liveness/             ノードの死活監視：判定、登録簿、シミュレータ
  netpol/               既定拒否のネットワークポリシー、LKAL スキーマ、nftables 適用
  node/                 containerd v2.3 の直接駆動、Linux netns、CNI 連鎖、シミュレータ
  resource/             封筒、kind スキーマ、参照検査、順序付き集合、Patch、葉パス所有権
  sim/                  シミュレーション境界（Clock・Net・Disk・Rand）、イベントキュー、clustersim
  store/                ストア契約、memstore、etcdstore、keyspace、tenant、watchcache
  wire/                 HTTP/2 LKAL-JSON ハンドラとクライアント
docs/ADR/               決定記録（日本語）
scripts/agent-harness/  コーディングエージェント向けのガードのフック
spec/                   Lean 4 のモデル、定理の主張、証明
```

## さらに知るには

- **それぞれの設計を選んだ理由**：[`docs/ADR/`](docs/ADR/) の決定記録にあります。最初に読むのは[動かさない前提](docs/ADR/0001-aim-for-a-full-kubernetes-replacement-on-fixed-premises.md)です。日本語で書いてあります。
- **コードに手を入れる**：[`AGENTS.md`](AGENTS.md) がリポジトリの地図です。領域ごとの決まりと、検証コマンドの全体が載っています。コーディングエージェント向けに書いてありますが、人が読み始める場所としても最適です。
- **脆弱性を報告する**：[`SECURITY.md`](SECURITY.md)。

## 先行研究と関連プロジェクト

Nayutar は、宣言の [LKAL](https://github.com/Kaikei-e/LKAL)、所有権モデルの [kisage](https://github.com/Kaikei-e/kisage)、決定記録の [DocDag](https://github.com/Kaikei-e/DocDag)、[etcd](https://etcd.io/)、[containerd](https://containerd.io/)、[CNI](https://www.cni.dev/)、[nftables](https://www.netfilter.org/projects/nftables/)、[Lean 4](https://lean-lang.org/) と [`comparator`](https://github.com/leanprover/comparator)、[Cedar](https://github.com/cedar-policy/cedar-go)、[Porcupine](https://github.com/anishathalye/porcupine) の上に成り立っています。

個々の設計は、次の仕様と研究を参照しています。出典の詳細は決定記録にあります。

| 設計 | 出典 |
| --- | --- |
| watch キャッシュからの一貫した読み取り | [KEP-2340](https://github.com/kubernetes/enhancements/blob/master/keps/sig-api-machinery/2340-Consistent-reads-from-cache/README.md) |
| ストリーミングの watch list | [KEP-3157](https://github.com/kubernetes/enhancements/blob/master/keps/sig-api-machinery/3157-watch-list/README.md) |
| API の Export、Binding、identityHash | [kcp](https://docs.kcp.io/kcp/main/concepts/apis/exporting-apis/) |
| 隔離の境界を越える参照の攻撃 | ["Breaking the Bulkhead"、NDSS 2026](https://arxiv.org/abs/2507.03387) |
| φ accrual 障害検知器 | [Hayashibara ほか、SRDS 2004](https://doi.org/10.1109/RELDIS.2004.1353004)、[Apache Pekko](https://github.com/apache/pekko) |
| 間接的な確認 | [SWIM、Das ほか、DSN 2002](https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf) |
| 自分の健全性の認識 | [Lifeguard、Dadgar ほか、2017](https://arxiv.org/abs/1707.00788) |
| 再現可能な ChaCha8 乱数ストリーム | [C2SP `chacha8rand`](https://c2sp.org/chacha8rand) |
| 決定論的シミュレーションと障害注入 | [FoundationDB simulation testing](https://apple.github.io/foundationdb/testing.html)、[TigerBeetle VOPR](https://github.com/tigerbeetle/tigerbeetle/blob/main/docs/internals/vopr.md)（設計原則の参照だけで、クリーンルームの Go 実装） |
| コントローラの rely-guarantee 合成 | [Welder](https://doi.org/10.1145/3830418.3843868)（設計原則の参照だけで、コードにもツールチェーンにも依存しません） |

## ライセンス

Apache License 2.0。[LICENSE](LICENSE) を参照してください。
