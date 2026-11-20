# Nayutar

[English](README.md) | **日本語**

**Nayutar** は、1 人の開発者と AI コーディングエージェントの協働によって **Kubernetes の本格的な代替** を目指すコンテナオーケストレータです。機械判定できる形式仕様と差分オラクルを設計の中心に据えて構築しています。

人間が書く宣言には YAML ではなく型付きの宣言言語 [LKAL](https://github.com/Kaikei-e/LKAL) を用い、複数アクターによるフィールド単位の協調更新には [kisage](https://github.com/Kaikei-e/kisage) の葉パス所有権モデルを採用します。コンテナ実行・ネットワーク・ストレージには OCI・containerd・CNI・CSI などの既存データプレーン資産を活かし、車輪の再発明を行いません。すべての設計判断は [DocDag](https://github.com/Kaikei-e/DocDag) で検証される決定記録（[`docs/ADR/`](docs/ADR/)）として管理しています。

> [!IMPORTANT]
> **現在のステータス — 初期開発・プレリリース（検証ゲート G0 を構築中）**
>
> Nayutar はまだエンドツーエンドのオーケストレータとしては利用できず、[G0 検証ゲート](docs/ADR/0018-define-the-g0-verification-gate-pass-criteria.md)の合格前です。現在は中核となる Go ライブラリ群と Lean 4 の形式仕様（ストア契約、障害注入つきの etcd v3.6 アダプタ、テナント最上位キー空間とフェンスつき登録簿、テナント別 watch キャッシュ、ソフトステート死活監視、リソース封筒、書き込み時の参照検査）が稼働しており、**34 個の保護済み Lean 定理**（`sorry` ゼロ）と **7 本の Lean–Go 差分ランダムテスト（DRT）スイート**を手元で実行して確かめられます。

---

## なぜ Nayutar か？（目的と便益）

Nayutar は「小規模向けの手軽なオーケストレータ」を目標にはしません。マルチテナント・数万ノード規模の本格的なオーケストレーションを射程に入れつつ、設定記述・所有権競合・テナント分離・制御面のスケーリングにおける構造的なつらさを手放すことを目的としています（[ADR 0001](docs/ADR/0001-aim-for-a-full-kubernetes-replacement-on-fixed-premises.md)）。

### 1. 型安全な宣言と、人間が書く面からの YAML 追放

利用者が書く設定や API マニフェストには [LKAL](https://github.com/Kaikei-e/LKAL)（`.lkal`）を採用し、暗黙の型変換・インデント起因の事故・`null` の曖昧さを排除します。単位つき数量（`512Mi`）・列挙値（`.tcp`）・型付き参照（`&secret/db-creds/spec/password`）が構文レベルで一意に定まり、すべてのリソースは統一された 3 区画の封筒（`meta`・`spec`・`status`）を持ちます（[ADR 0009](docs/ADR/0009-fix-the-resource-envelope-to-meta-spec-and-status.md)、[ADR 0025](docs/ADR/0025-realize-the-resource-envelope-with-a-lowercase-kind-two-write-entrances-a-pure-transition-core-and-an-object-store-binding.md)）。

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

- **意図と観測状態の厳格な分離**：書き込みの入口は `Update`（`labels` と `spec`）と `UpdateStatus`（`status`）の 2 つに分かれます。入口の外の欄やサーバ専用欄（`uid`・`generation`・`revision`・条件の `since`）は、省略または現在値と一致しない限り拒否され、黙って無視されることはありません。
- **YAML は取り込み境界に限定**：既存の Kubernetes 資産（Helm・KRM・Compose などの YAML）は取り込み入力としてのみ扱います。Kubernetes の適合認定や、複雑なパッチ意味論・グローバル CRD への追従負担は引き受けません。

### 2. テナント最上位の隔離境界と、書き込み時の参照検査

名前の衝突を防ぐ階層と権限・分離の境界が混ざることで生じるコントローラの越境参照脆弱性を防ぐため、ストアのキー空間の最上位を `/t/<tenant>/` に固定し、命名階層はドット区切りのオブジェクト名の中に閉じ込めています（[ADR 0006](docs/ADR/0006-put-the-tenant-at-the-top-of-the-key-space-and-keep-namespaces-out-of-keys.md)、[ADR 0022](docs/ADR/0022-realize-the-key-space-with-a-strict-segment-grammar-a-key-codec-and-a-fenced-tenant-registry.md)）。

- **単一接頭辞による分離とフェンスつき管理**：オブジェクト本体・所有権・逆参照索引・Binding のすべてが `/t/<tenant>/` 配下に集約されるため、プレフィックス単位の隔離、フェンスつきの再開可能な 2 段階テナント削除、将来のテナント別ストア分割がキー構造レベルで成立します。
- **書き込み時の fail-closed な参照検査**：参照（`Ref`）はテナント内の相対名かつ `spec` 内に限定されます。作成・更新時に追加された `(sourcePath, ref)` ごとに参照スキーマ表、[Cedar](https://docs.cedarpolicy.com/) による認可（`Nayutar::Action::"Reference"`）、参照先の存在と `mod_revision` を検査し、オブジェクト本体および逆参照索引と**同一の原子的トランザクション**で書き込みます（[ADR 0010](docs/ADR/0010-check-references-at-write-time-and-keep-them-inside-a-tenant.md)、[ADR 0026](docs/ADR/0026-realize-reference-checking-with-a-pure-check-core-a-reference-schema-table-cedar-authorization-and-a-back-reference-index-in-the-object-transaction.md)）。

### 3. `force` 上書きのない葉パス所有権モデル *(設計済み・実装予定 — [ADR 0013](docs/ADR/0013-store-leaf-path-ownership-under-its-own-key-and-update-it-in-the-same-transaction.md))*

複数コントローラや利用者による協調更新の中核には [kisage](https://github.com/Kaikei-e/kisage) の葉パス所有権モデルを据えています。

- 葉パス所有権メタデータはオブジェクト本体に埋め込まず独立キー（`/t/<tenant>/own/...`）に分離し、本体と同じ CAS トランザクション（`Txn`）で原子的に更新することで、`managedFields` による本体・watch ペイロードの肥大化を防ぎます。
- 所有権競合・構造衝突・外部ドリフトは診断コードを返して fail-closed で停止し、他者の所有葉を強引に奪う `--force` は設けません。また `apply` と保守的な削除である `prune` を明確に分離します。

### 4. 心拍と Watch の負荷から合意ストアを守る制御面

- **合意ストアへの定常心拍書き込みゼロ**：内部ストア契約から Lease と TTL つきキーを除外しています（[ADR 0004](docs/ADR/0004-fix-a-subset-of-the-etcd-v3-api-without-leases-as-the-store-contract.md)）。ノードの心拍は死活集約器のメモリ上のソフトステートとして保持し、整数演算による φ accrual 障害検知・rendezvous hashing による担当割り当て・中継ノード／他ラック証人による間接確認・自己疑い（self-suspicion）を経て**確定した状態遷移（`Ready` / `NotReady`）とレート制限つきの退避許可だけ**を CAS でストアに書き込みます。合意層の書き込み負荷はノード数ではなく実障害の発生率に比例します（[ADR 0008](docs/ADR/0008-keep-node-liveness-as-soft-state-and-write-only-confirmed-transitions-to-the-store.md)、[ADR 0024](docs/ADR/0024-realize-node-liveness-with-an-exact-phi-test-a-deterministic-detector-core-a-node-registry-and-a-rate-limited-eviction-permit.md)）。
- **テナント×型別のインメモリ Watch キャッシュ**：読み取り・リビジョン指定スナップショット・ブックマーク・`watch list` によるストリーミング初期同期を、パスコピー型永続 B-tree と「閉じたリビジョン」を備えたキャッシュから配信し、特定テナントの大量更新が他テナントの履歴を押し流すことを防ぎます（[ADR 0007](docs/ADR/0007-serve-watches-from-a-per-tenant-cache-with-revisions-bookmarks-and-watch-list.md)、[ADR 0023](docs/ADR/0023-realize-the-watch-cache-as-a-library-with-a-persistent-b-tree-closed-revisions-and-an-in-process-compaction-bound.md)）。

### 5. 実績あるデータプレーンの活用と不要な中間層の排除

初期の強整合ストアには差し替え可能な `Store` 契約の背後に 3 台構成の etcd v3.6 クラスタを用い（[ADR 0005](docs/ADR/0005-start-on-a-single-etcd-cluster-with-tenant-key-prefixes.md)）、データプレーンでは CRI（`PodSandbox`）を介さず containerd 2.3 LTS の Go クライアント・CNI プラグイン・CSI gRPC サービス・既定拒否の `nftables` チェインを直接駆動する構成を採ります（[ADR 0014](docs/ADR/0014-drive-containerd-directly-without-cri.md)、[ADR 0016](docs/ADR/0016-enforce-default-deny-network-policy-with-nftables-first.md)）。

### 6. 個人＋AI エージェント開発を支える機械判定オラクル

少人数と AI エージェントで大規模型システムを破綻なく構築するため、AI が目標を弱めてすり抜けられない外部オラクルで品質を確かめます（[ADR 0002](docs/ADR/0002-implement-the-core-in-go-and-write-specifications-in-lean-4.md)、[ADR 0003](docs/ADR/0003-assure-the-implementation-by-differential-testing-against-lean-models-instead-of-proving-it.md)、[ADR 0017](docs/ADR/0017-build-the-drt-harness-and-the-theorem-statement-guard-first-and-defer-the-models-the-esr-proof-and-the-kubernetes-oracle.md)、[ADR 0021](docs/ADR/0021-guard-the-trust-root-of-the-theorem-statements-on-the-server-and-run-the-lean-and-drt-workflows-on-every-change.md)）。

1. **実行可能な Lean 4 モデルと保護された定理証明**：状態機械の純粋な核を `spec/` に記述します。エージェントは `spec/Nayutar/Proofs/` の証明本体だけを書け、人間が所有する定理の主張（`spec/Nayutar/Statements/` と `spec/comparator.json`）との一致および公理検査を [`lake comparator`](https://github.com/leanprover/comparator) の `bwrap` サンドボックス内で強制します。
2. **Lean–Go 差分ランダムテスト（DRT）**：シードつき乱数で生成した操作列を NDJSON で `nayutar-drt` に流し、各ステップの遷移結果を全フィールドについて Go 実装と照合します。変異させた出力や壊した実装を確実に検出する試験も含みます。
3. **線形化可能性検査と実クラスタ障害注入**：ストアとキャッシュに対し、実物の 3 メンバー etcd v3.6 上でリーダー停止・過半数喪失・ピアプロキシによるネットワーク分断を注入しながら [Porcupine](https://github.com/anishathalye/porcupine) で線形化可能性を検査します（[ADR 0020](docs/ADR/0020-realize-the-single-etcd-cluster-and-inject-majority-loss-and-network-partitions-into-its-tests.md)）。
4. **決定論的シミュレーションと外部差分オラクル**：死活監視はシード固定イベントキュー（`livesim`）上の 19 シナリオで検証しており、後続で系全体のシミュレーション境界（[ADR 0015](docs/ADR/0015-route-all-external-effects-through-simulation-interfaces-and-pick-the-simulator-after-evaluating-gosim.md)）と Kubernetes v1.37（`kind` / `kwok`）に対する外部差分ハーネスを整えます。

---

## アーキテクチャ概要

### キー空間の構造

永続化されるすべてのキーは、小文字 ASCII の厳格なコーデック（[`core/store/keyspace`](core/store/keyspace)）により 1 つの正規バイト列に対応づけられます。

```text
/t/<tenant>/o/<group>/<kind>/<name>               オブジェクト封筒（LKAL-JSON）
/t/<tenant>/own/<group>/<kind>/<name>             葉パス所有権（実装予定、ADR 0013）
/t/<tenant>/ref/<tQKind>/<tName>/<sQKind>/<sName> 逆参照索引
/t/<tenant>/bind/<identityHash>                   Export された API の Binding（実装予定）
/x/<identityHash>/schema/<version>                Export されたスキーマ（実装予定）
/sys/tenants/<tenant>                             テナント登録状態と mod_revision フェンス
/sys/nodes/<node>                                 ノード登録簿・死活状態・退避許可
/sys/liveness/evictions                           クラスタ全体の退避許可レート制限予算
```

### 検証ゲート（G0〜G4）による進行

どの段階も中途半端な「小さな完成品」として閉じず、明確な検証ゲート（G0〜G4）の合格条件を満たしながら段階を進めます（[ADR 0001](docs/ADR/0001-aim-for-a-full-kubernetes-replacement-on-fixed-premises.md)）。

- **G0（仕様とハーネス — 現在の対象、[ADR 0018](docs/ADR/0018-define-the-g0-verification-gate-pass-criteria.md)）**：ストア／watch と CAS 版所有権の Lean モデル＋DRT、watch 4 不変条件の証明、抽象状態機械上の ESR 証明、決定論的シミュレータ評価、および Kubernetes v1.37 に対する自動差分試験。
- **G1〜G4（規模・マルチテナント・エコシステム）**：10,000 ノード規模の制御面遅延計測と 72 時間死活障害注入（G1）、50,000 ノード・1,000 テナントでのストア分割比較と `nftables` 対 `cilium/ebpf` 評価（G2）、および上位ワークロード・コントローラ群の検証。

---

## 現在の実装状況

### 実装・検証済みの領域

| 領域 | Go パッケージ | Lean 4 仕様・保護定理 | 現在の検証内容 |
| --- | --- | --- | --- |
| **DRT 枠組みと定理保護** ([ADR 0017](docs/ADR/0017-build-the-drt-harness-and-the-theorem-statement-guard-first-and-defer-the-models-the-esr-proof-and-the-kubernetes-oracle.md), [0021](docs/ADR/0021-guard-the-trust-root-of-the-theorem-statements-on-the-server-and-run-the-lean-and-drt-workflows-on-every-change.md)) | [`core/drt`](core/drt), [`cmd/check-protected-theorems`](cmd/check-protected-theorems) | `Fixture/Register.lean`（3 定理） | NDJSON DRT 実行基盤、フィールド変異・誤実装の検出試験、サンドボックス内の `lake comparator` |
| **ストア契約とバックエンド** ([ADR 0004](docs/ADR/0004-fix-a-subset-of-the-etcd-v3-api-without-leases-as-the-store-contract.md), [0005](docs/ADR/0005-start-on-a-single-etcd-cluster-with-tenant-key-prefixes.md), [0020](docs/ADR/0020-realize-the-single-etcd-cluster-and-inject-majority-loss-and-network-partitions-into-its-tests.md)) | [`core/store`](core/store), `memstore`, `etcdstore`, `storetest` | `Store.lean` | 契約テスト、Porcupine 線形化可能性検査、3 メンバー etcd での停止・過半数喪失・ピアプロキシ分断注入、DRT（`store`） |
| **キー空間とテナント登録簿** ([ADR 0006](docs/ADR/0006-put-the-tenant-at-the-top-of-the-key-space-and-keep-namespaces-out-of-keys.md), [0022](docs/ADR/0022-realize-the-key-space-with-a-strict-segment-grammar-a-key-codec-and-a-fenced-tenant-registry.md)) | [`core/store/keyspace`](core/store/keyspace), [`core/store/tenant`](core/store/tenant) | `Keyspace.lean` | ファズ・性質試験、`mod_revision` フェンスつきビュー、再開可能な 2 段階テナント削除（`memstore` & etcd）、DRT（`keyspace`） |
| **Watch キャッシュ** ([ADR 0007](docs/ADR/0007-serve-watches-from-a-per-tenant-cache-with-revisions-bookmarks-and-watch-list.md), [0023](docs/ADR/0023-realize-the-watch-cache-as-a-library-with-a-persistent-b-tree-closed-revisions-and-an-in-process-compaction-bound.md)) | [`core/store/watchcache`](core/store/watchcache)（`btree`, `window`） | `WatchCache.lean`（4 定理） | 欠落なし・重複なし・順序保存・ブックマーク単調性の証明、etcd リーダー停止を跨ぐ線形化可能性検査、DRT（`watchcache`） |
| **ノード死活監視** ([ADR 0008](docs/ADR/0008-keep-node-liveness-as-soft-state-and-write-only-confirmed-transitions-to-the-store.md), [0024](docs/ADR/0024-realize-node-liveness-with-an-exact-phi-test-a-deterministic-detector-core-a-node-registry-and-a-rate-limited-eviction-permit.md)) | [`core/liveness`](core/liveness)（`phi`, `hrw`, `detector`, `budget`, `livesim`） | `Liveness.lean`（9 定理） | 安全性 9 定理の証明、DRT（`liveness`）、`livesim` 19 シナリオの障害シミュレーション、`testing/synctest` 実行ループ試験、etcd 登録簿試験 |
| **リソース封筒** ([ADR 0009](docs/ADR/0009-fix-the-resource-envelope-to-meta-spec-and-status.md), [0025](docs/ADR/0025-realize-the-resource-envelope-with-a-lowercase-kind-two-write-entrances-a-pure-transition-core-and-an-object-store-binding.md)) | [`core/resource/envelope`](core/resource/envelope), [`core/resource`](core/resource) | `Envelope.lean`（10 定理） | 封筒構造・世代・楽観ロック更新規則の 10 定理証明、ファズ試験、LKAL-JSON ストア結合（`memstore` & etcd）、DRT（`envelope`） |
| **参照検査と逆参照索引** ([ADR 0010](docs/ADR/0010-check-references-at-write-time-and-keep-them-inside-a-tenant.md), [0026](docs/ADR/0026-realize-reference-checking-with-a-pure-check-core-a-reference-schema-table-cedar-authorization-and-a-back-reference-index-in-the-object-transaction.md)) | [`core/resource/refs`](core/resource/refs), `refcedar`, [`core/resource`](core/resource) | `Refs.lean`（8 定理） | 書き込み時参照検査・索引差分の 8 定理証明、`cedar-go` 認可、同一 CAS `Txn` での逆参照索引試験（`memstore` & etcd）、DRT（`refs`） |
| **開発用 etcd ランチャー** ([ADR 0020](docs/ADR/0020-realize-the-single-etcd-cluster-and-inject-majority-loss-and-network-partitions-into-its-tests.md)) | [`cmd/dev-etcd`](cmd/dev-etcd), [`config/dev.lkal`](config/dev.lkal) | — | `.lkal` で宣言した 3 メンバーのローカル etcd v3.6.16 クラスタを起動 |

### 未実装の領域

- **HTTP/2 API サーバ（`lk api`）**、クライアント認証、パス指定の Patch（`test`/`set`/`del`）、および決定論的 LKAL-CBOR ワイヤ形式（[ADR 0012](docs/ADR/0012-use-lkal-json-on-the-wire-first-and-add-untagged-deterministic-cbor-later.md)）。
- **CAS 版の葉パス所有権モデルと apply / prune**（[ADR 0013](docs/ADR/0013-store-leaf-path-ownership-under-its-own-key-and-update-it-in-the-same-transaction.md)）、および順序付きレコード集合のスキーマ検査（[ADR 0011](docs/ADR/0011-express-ordered-record-sets-with-an-explicit-integer-order-leaf.md)）。
- **API の Export / `Binding`**（`identityHash` つき解決とコントローラの読み取り制限）、コントローラ SDK、および **ESR（Eventually Stable Reconciliation）の証明**。
- **ノードエージェント（`lk-node`）**、containerd・CNI・CSI の直接駆動（[ADR 0014](docs/ADR/0014-drive-containerd-directly-without-cri.md)）、既定拒否 `nftables` ネットワークポリシー（[ADR 0016](docs/ADR/0016-enforce-default-deny-network-policy-with-nftables-first.md)）、死活監視の gRPC 輸送と実行バイナリ（`lk live`）、およびスケジューラ。
- **系全体の決定論的シミュレータ選定**（[ADR 0015](docs/ADR/0015-route-all-external-effects-through-simulation-interfaces-and-pick-the-simulator-after-evaluating-gosim.md)）と **Kubernetes v1.37 に対する外部差分試験ハーネス**（`kind` / `kwok`）。

---

## クイックスタート（ビルドと検証の実行）

### 必要な環境

- **必須**：**Go 1.27.3** 以上（外部ツール不要で、通常の単体テスト・線形化可能性検査・ファズ対象・`livesim` シミュレーションがすべて動作します）。
- **任意（Lean 4 仕様・定理検査・DRT）**：[`elan`](https://github.com/leanprover/elan) 経由の **Lean 4.35.0**、および `lake comparator` 用の `bwrap`（非特権ユーザー名前空間が有効な [bubblewrap](https://github.com/containers/bubblewrap)）。
- **任意（etcd 結合・障害注入試験）**：**etcd v3.6.16**（`PATH` 上の `etcd` または環境変数 `NAYUTAR_ETCD_BIN` で指定）。
- **任意（宣言・ADR の検証）**：[`lkal`](https://github.com/Kaikei-e/LKAL) および [`docdag`](https://github.com/Kaikei-e/DocDag)。

### 1. コアの単体テストとシミュレーションを実行する（Go のみ）

```sh
go build ./...
go test ./...
go test -race ./...
```

ノード死活監視のシード固定シミュレータ（既定は 19 シナリオ×30 シード）のシード数を増やして走らせる場合：

```sh
NAYUTAR_SIM_SEEDS=100 go test ./core/liveness/livesim/
```

### 2. Lean 4 の証明ビルドと差分ランダムテスト（DRT）を実行する

```sh
# Lean モデルと sorry なしの証明をすべてビルド
(cd spec && lake build)

# 保護対象の全 34 定理の主張と許容公理を検査
go build -o "${TMPDIR:-/tmp}/check-protected-theorems" ./cmd/check-protected-theorems \
  && "${TMPDIR:-/tmp}/check-protected-theorems"
(cd spec && lake comparator)

# 全 7 スイートの Lean–Go 差分ランダムテストを実行（既定は各 1,000 ケース）
go test -tags drt ./core/drt/...
```

### 3. 3 メンバー実 etcd クラスタへの障害注入試験を実行する

```sh
# 契約テスト・Porcupine 線形化可能性検査・メンバー停止・過半数喪失・ネットワーク分断注入
go test -tags etcd ./core/store/... ./core/liveness/ ./core/resource/

# Lean–Go DRT を 3 メンバーの etcd クラスタに対しても実行
go test -tags "drt etcd" ./core/drt/...
```

[`config/dev.lkal`](config/dev.lkal) に宣言された開発用の 3 メンバー etcd クラスタをローカルで起動することもできます（`lkal` と `etcd` 3.6.16 が必要）：

```sh
go run ./cmd/dev-etcd
```

---

## ディレクトリ構成と ADR ガイド

```text
cmd/                    CLI ツール（dev-etcd ランチャー、保護定理の事前検査）
config/                 LKAL で記述した開発環境の宣言（config/dev.lkal）
core/
  drt/                  Lean–Go 差分ランダムテストの実行基盤と各スイート
  liveness/             ソフトステート死活監視、整数 φ 検知器、退避予算、livesim
  resource/             リソース封筒、書き込み時参照検査、Cedar 認可、ストア結合
  store/                ストア契約、memstore、etcdstore、keyspace、tenant、watchcache
docs/ADR/               アーキテクチャ決定記録（DocDag で依存と不変性を検証）
scripts/agent-harness/  コーディングエージェント向けの保護ガードと自己検査
spec/                   独立した Lean 4 パッケージ（モデル、主張、証明、nayutar-drt）
```

### 主要な決定記録（ADR）の地図

コードを書き始める前に設計判断を ADR として起票し、受理済み（`accepted`）の既存行は不変（`docdag validate --immutable-since`）として保護しています。

- **前提・言語・検証基盤（`accepted`）**：[0001](docs/ADR/0001-aim-for-a-full-kubernetes-replacement-on-fixed-premises.md)（動かさない前提） · [0002](docs/ADR/0002-implement-the-core-in-go-and-write-specifications-in-lean-4.md)（Go 中核＋Lean 4 仕様） · [0003](docs/ADR/0003-assure-the-implementation-by-differential-testing-against-lean-models-instead-of-proving-it.md)（DRT とオラクルによる保証） · [0017](docs/ADR/0017-build-the-drt-harness-and-the-theorem-statement-guard-first-and-defer-the-models-the-esr-proof-and-the-kubernetes-oracle.md)（DRT 枠組みと定理保護、[0021](docs/ADR/0021-guard-the-trust-root-of-the-theorem-statements-on-the-server-and-run-the-lean-and-drt-workflows-on-every-change.md) で信頼の根の保護を拡張） · [0018](docs/ADR/0018-define-the-g0-verification-gate-pass-criteria.md)（G0 合格条件）
- **ストア・キー空間・Watch・死活監視（`accepted` → 実現方式 `proposed`）**：[0004](docs/ADR/0004-fix-a-subset-of-the-etcd-v3-api-without-leases-as-the-store-contract.md)（Lease なしの `Store` 契約） · [0005](docs/ADR/0005-start-on-a-single-etcd-cluster-with-tenant-key-prefixes.md) → [0020](docs/ADR/0020-realize-the-single-etcd-cluster-and-inject-majority-loss-and-network-partitions-into-its-tests.md)（単一 3 台 etcd と障害注入） · [0006](docs/ADR/0006-put-the-tenant-at-the-top-of-the-key-space-and-keep-namespaces-out-of-keys.md) → [0022](docs/ADR/0022-realize-the-key-space-with-a-strict-segment-grammar-a-key-codec-and-a-fenced-tenant-registry.md)（テナント最上位キー空間とフェンスつき登録簿） · [0007](docs/ADR/0007-serve-watches-from-a-per-tenant-cache-with-revisions-bookmarks-and-watch-list.md) → [0023](docs/ADR/0023-realize-the-watch-cache-as-a-library-with-a-persistent-b-tree-closed-revisions-and-an-in-process-compaction-bound.md)（テナント別 watch キャッシュ） · [0008](docs/ADR/0008-keep-node-liveness-as-soft-state-and-write-only-confirmed-transitions-to-the-store.md) → [0024](docs/ADR/0024-realize-node-liveness-with-an-exact-phi-test-a-deterministic-detector-core-a-node-registry-and-a-rate-limited-eviction-permit.md)（ソフトステート死活監視）
- **リソース封筒・参照検査（`accepted` → 実現方式 `proposed`）**：[0009](docs/ADR/0009-fix-the-resource-envelope-to-meta-spec-and-status.md) → [0025](docs/ADR/0025-realize-the-resource-envelope-with-a-lowercase-kind-two-write-entrances-a-pure-transition-core-and-an-object-store-binding.md)（`meta`・`spec`・`status` 封筒） · [0010](docs/ADR/0010-check-references-at-write-time-and-keep-them-inside-a-tenant.md) → [0026](docs/ADR/0026-realize-reference-checking-with-a-pure-check-core-a-reference-schema-table-cedar-authorization-and-a-back-reference-index-in-the-object-transaction.md)（書き込み時の参照検査と Cedar 認可）
- **今後実装する領域（`proposed`）**：[0011](docs/ADR/0011-express-ordered-record-sets-with-an-explicit-integer-order-leaf.md)（順序付きレコード集合） · [0012](docs/ADR/0012-use-lkal-json-on-the-wire-first-and-add-untagged-deterministic-cbor-later.md)（LKAL-JSON・Patch・CBOR ワイヤ形式） · [0013](docs/ADR/0013-store-leaf-path-ownership-under-its-own-key-and-update-it-in-the-same-transaction.md)（同一 `Txn` の葉パス所有権） · [0014](docs/ADR/0014-drive-containerd-directly-without-cri.md)（CRI を介さない containerd 直接駆動） · [0015](docs/ADR/0015-route-all-external-effects-through-simulation-interfaces-and-pick-the-simulator-after-evaluating-gosim.md)（シミュレーション用 I/O 境界） · [0016](docs/ADR/0016-enforce-default-deny-network-policy-with-nftables-first.md)（既定拒否の `nftables` ネットワークポリシー）

---

## 先行研究・関連プロジェクトへの謝辞

Nayutar は [LKAL](https://github.com/Kaikei-e/LKAL)、[kisage](https://github.com/Kaikei-e/kisage)、[DocDag](https://github.com/Kaikei-e/DocDag)、[etcd](https://etcd.io/)、[Lean 4](https://lean-lang.org/) および [`comparator`](https://github.com/leanprover/comparator)、[Cedar（`cedar-go`）](https://github.com/cedar-policy/cedar-go)、[Porcupine](https://github.com/anishathalye/porcupine) の上に成り立っています。また個々の設計判断では、キャッシュからの一貫読み取り（[KEP-2340](https://github.com/kubernetes/enhancements/blob/master/keps/sig-api-machinery/2340-Consistent-reads-from-cache/README.md)）とストリーミング watch-list（[KEP-3157](https://github.com/kubernetes/enhancements/blob/master/keps/sig-api-machinery/3157-watch-list/README.md)）、`APIExport` / `APIBinding` の `identityHash` 分離（[kcp](https://docs.kcp.io/kcp/main/concepts/apis/exporting-apis/)）、Operator の越境参照脆弱性の分類（["Breaking the Bulkhead", NDSS 2026](https://arxiv.org/abs/2507.03387)）、φ accrual 障害検知器（[Hayashibara et al., SRDS 2004](https://doi.org/10.1109/RELDIS.2004.1353004) および [Apache Pekko](https://github.com/apache/pekko)）、SWIM の間接プローブ（[Das et al., DSN 2002](https://www.cs.cornell.edu/projects/Quicksilver/public_pdfs/SWIM.pdf)）、Lifeguard の自己健全性認識（[Dadgar et al., 2017](https://arxiv.org/abs/1707.00788)）、ならびにコントローラ合成の rely-guarantee 設計原則（[Welder](https://doi.org/10.1145/3830418.3843868)、コード・ツールチェーン依存なしの参照文献）などの一次仕様・研究を参照しています。詳細は各 ADR の根拠節を参照してください。

## コントリビューションとセキュリティ

- [`AGENTS.md`](AGENTS.md)：リポジトリの地図、領域ごとの指示、検証コマンド、およびコーディングエージェント向けの保護ルール。
- [`SECURITY.md`](SECURITY.md)：サポート対象バージョンと脆弱性の非公開報告手順。

## ライセンス

Apache License 2.0。詳細は [LICENSE](LICENSE) を参照してください。
