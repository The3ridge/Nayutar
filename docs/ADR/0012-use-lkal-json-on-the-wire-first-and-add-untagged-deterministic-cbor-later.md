---
title: ワイヤ形式はまず LKAL-JSON を使い G2 からタグなし決定論的 CBOR を追加する
status: accepted
date: 2026-11-17
depends-on: ["0009", "0011"]
---

# 0012: ワイヤ形式はまず LKAL-JSON を使い G2 からタグなし決定論的 CBOR を追加する

## コンテキスト

[[0001]] は JSON の使用を認め（前提 3）、その根拠として「API ワイヤは LKAL-JSON、高頻度経路にはバイナリ表現（LKAL-CBOR）を足す」という層の分担を挙げている。[[0009]] でリソース封筒を固定し、[[0011]] で順序付きレコード集合を `order` 葉つきレコード集合で表す規約を定めた上で、API サーバとクライアント間の具体的なワイヤ形式および部分更新（Patch）形式を定める必要がある。LKAL には 9 型すべてを可逆表現できる LKAL-JSON と v0.2 スキーマ言語がある一方、CBOR 仕様はまだ存在しない。

## 決定

1. G0〜G1 の外部ワイヤ形式は HTTP/2 上の LKAL-JSON とし、既存の可逆形式（予約キー `$lkal`・`$enum`・`$qty`・`$ref`）を使用する。
2. G2 から LKAL-CBOR を追加する。データ構造は LKAL-JSON と同一とし、CBOR タグは使わずに決定論的に符号化する。CBOR タグの割り当て・登録は後回しにする。
3. ワイヤ上の Patch は、LKAL の ADR 0006（Get / Set / Delete）の `/` 区切りパスモデルに合わせ、対象パスに対する `.test` / `.set` / `.del` 操作の列とする。LKAL の `List` は単一型スカラーのみを保持するため（`L204`・`L304`）、操作列は [[0011]] の順序付きレコード集合（整数 `order` 葉を持つ `patch` レコード集合）として `@lkal "0.1"` / LKAL-JSON 文書で表す。ラベルによりパスが一意に決まるため、RFC 6902 のような配列添字は使わない。
4. Kubernetes の CBOR（KEP-4222）との互換性は目標にしない。
5. Patch と Diff は LKAL の 9 型モデルと `FromJSON` / `ToJSON` を変えずに `lkal.Record` データモデルと [[0011]] の順序付きレコード集合で表現できるため、LKAL 本体の言語変更は行わない。一方、LKAL 本体に CBOR 仕様はまだないため、(2) の LKAL-CBOR（`Decimal` の無損失表現方法を含む）は Nayutar 側に回避策を作らず、[[0001]] の前提 2 が許容する LKAL 側の変更として G2 で提案する。

```json
{
  "$lkal": "0.1",
  "meta": {
    "kind": "workload",
    "name": "web"
  },
  "spec": {
    "replicas": 3,
    "container": {
      "app": {
        "port": { "http": { "number": 8080, "protocol": { "$enum": "tcp" } } },
        "memory": { "$qty": "512Mi" },
        "env": { "DB_PASSWORD": { "from": { "$ref": "secret/db-creds/spec/password" } } }
      }
    }
  }
}
```

```json
{
  "$lkal": "0.1",
  "patch": {
    "0": { "order": 10, "op": { "$enum": "test" }, "path": "meta/revision", "value": 4812 },
    "1": { "order": 20, "op": { "$enum": "set" }, "path": "spec/replicas", "value": 5 },
    "2": { "order": 30, "op": { "$enum": "del" }, "path": "spec/container/sidecar" }
  }
}
```

## 根拠（調査結果・出典）

- **LKAL の LKAL-JSON 不変条件とリスト制約**（`https://github.com/Kaikei-e/LKAL` の `docs/PRINCIPLE.md` §4・§7、`json.go`、`path.go`、2026-11-22 確認）：
  - `ToJSON` / `FromJSON` が `$lkal`・`$enum`・`$qty`・`$ref` の 4 つの予約キーを用いて 9 型すべてを可逆往復でき、`$` 始まりのユーザキー・ラベルを拒否する（`L205`）ため衝突しない。`Decimal` は引用符なしの 10 進 JSON 数値（`0.25`）として符号化・復号され、引用符付き（`"0.25"`）は `String` になる。
  - LKAL の `List` は単一型スカラーのみを許し、`FromJSON` は JSON 配列内の非スカラー要素（`{"op": ...}` など）を `L304` で拒否する。したがって Patch 操作列を [[0011]] の `order` 葉つきレコード集合で表すことで、LKAL の 9 型不変条件を変えずに `@lkal "0.1"`・LKAL-JSON・将来の LKAL-CBOR で同一構造のまま往復できる。
  - `(*lkal.File).Delete` は構文木上のラベル付きエントリ構造に依存するため、データモデルの等価性（`lkal.Equal`）に対して不変な Patch / Diff は `lkal.Record` データモデル上で定義する（[[0029]]）。
- **RFC 8949（CBOR）の決定論的符号化規定と `Decimal` の制約**（RFC 8949 §4.1・§4.2.1〜§4.2.3・§5.6・§9.5 https://www.rfc-editor.org/rfc/rfc8949.html 、および IETF `draft-ietf-cbor-cde-13` https://datatracker.ietf.org/doc/draft-ietf-cbor-cde/ 、2026-11-22 確認）：
  - RFC 8949 §4.2.1（Core Deterministic Encoding Requirements）は、(1) 整数・長さ・タグ番号の最短表現（Preferred Serialization、§4.1）、(2) 浮動小数点値を誤差なく保持できる最短 IEEE 754 幅（binary16 → binary32 → binary64）、(3) 不定長表現の禁止（確定長のみ）、(4) マップキーの決定論的符号化バイト列のバイト辞書順（MT 3 文字列キー同士では短いキーが先になり §4.2.3 の length-first 順序と一致する）、および §5.6 による重複キー禁止を定める。また MT 0/1（整数）と MT 7（浮動小数）は別型であり、§4.2.1 単体では `1.0` を整数 `1` へ縮約しない。
  - 一方、LKAL の `Decimal` は 10 進小数文字列（最大 100 桁）であるため、タグなし CBOR の IEEE 754 2 進浮動小数（MT 7）だけでは `0.1` の正確な 10 進値や 53 ビット超の精度を無損失往復できない。このため G2 で LKAL-CBOR を上流提案する際には、タグなし予約キー（`{"$dec": "0.1"}` 等）を用いるかタグ・型規約をどう定めるかを LKAL 側の ADR で決定する必要がある。
- **Kubernetes KEP-4222 との比較**（Kubernetes Feature Gates https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/ 、KEP-4222 https://github.com/kubernetes/enhancements/issues/4222 、2026-11-22 確認）：`CBORServingAndStorage` 機能ゲートは v1.32 で Alpha 導入後、v1.37 でも Alpha（既定で無効）であることを確認した。また Kubernetes は etcd 保存時の自動判別のため RFC 8949 Tag 55799（Self-Described CBOR、`0xd9d9f7`）を先頭に付与しており、本 ADR のタグなし方針とは設計目的が異なる。
- **段階導入と Patch 形式（本 ADR の設計判断）**：G0〜G1 は実装済みの LKAL-JSON を用いて HTTP/2 ワイヤと Patch を立ち上げ、タグなし決定論的符号化および `Decimal` 表現の設計を伴う LKAL-CBOR は G2 から追加する。

## 検討した代替案

- **最初から Protobuf をワイヤ形式にする**：バイナリ効率は高い。しかしリソースごとの `.proto` 定義と生成コードの管理が加わり、[[0009]] の汎用 LKAL 文書モデルや差分ハーネスでの直接検査と相性が悪いため採らない。
- **最初から 9 型へ CBOR タグを割り当てる**：`$enum` 等のマップ表現より小さくなる。しかし IANA タグ登録や独自タグ管理のコストを初期から負いデバッグ性も下がるため、まずタグなしで始める。
- **`$` 予約キーを持たない plain JSON を使う**：一般の JSON API に近くなる。しかし Enum・Quantity・Ref と通常の String や Map の区別が消えて LKAL の 9 型情報が失われるため採らない。plain JSON は外部標準との境界に限る。
- **Patch 操作列を JSON 配列 `[{"op": ...}]` で表す**：RFC 6902 に見た目が近い。しかし LKAL は配列要素を単一型スカラーに限っており `lkal.FromJSON` が `L304` で拒否するため、`@lkal "0.1"` 文書および LKAL v0.2 スキーマで統一的に扱えなくなる。[[0011]] の `order` 葉つきレコード集合を使えば 9 型モデルのまま全 9 型の `value` を保持できるため採らない。
- **JSON Patch（RFC 6902）または JSON Merge Patch（RFC 7396）を使う**：既存仕様を流用できる。しかし RFC 7396（ https://www.rfc-editor.org/rfc/rfc7396.html ）は `null` による削除を前提としており [[0009]] の「`null` なし」と矛盾し、RFC 6902（ https://www.rfc-editor.org/rfc/rfc6902.html ）は配列添字に依存するうえ §4.6 の `test` が `1` と `1.0` を同一視して LKAL の `Int` / `Decimal` 区別を壊すため採らない。

## 影響とトレードオフ

- **得るもの**：G0〜G1 は既存の LKAL-JSON と v0.2 スキーマだけでリソース・Patch・一覧・Watch イベント・エラー応答まで単一のデータモデルに統一でき、シリアライズ仕様の追加を待たずに HTTP/2 ワイヤと試験基盤を構築できる。`.test` / `.set` / `.del` の Patch は [[0013]] の葉パス所有権検査へ直接対応づけられる。
- **失うもの**：G0〜G1 ではテキスト JSON の帯域・パース負荷を負い、Patch 操作ごとに `order` 葉と要素ラベルが必要になる。また G2 のタグなし CBOR でも `$enum` 等のマップキー分のオーバーヘッドは残る。
- **引き受けるリスク**：G2 で LKAL 側に CBOR 仕様・実装を追加する際、RFC 8949 §4.2 の決定論的符号化と LKAL `Decimal` の 10 進無損失往復を両立させる表現（`{"$dec": "..."}` 等）を確定する必要がある。
- **今後の検証ゲート**：G0 で LKAL-JSON ワイヤと Patch / Diff 適用の往復性を Lean 4 定理・DRT・性質ベース試験・ファジング（[[0003]]・[[0029]]）で検証する。G1〜G2 でシリアライズの CPU・帯域がボトルネックとなった段階で LKAL-CBOR を導入し、タグなし表現でも容量や速度が要件を満たさない場合にタグ割り当てを再検討する。

## 関連ADR

- [[0001]]：前提 3 で JSON の使用を認め、前提 2 で LKAL 側の変更（G2 の LKAL-CBOR など）を許容している。
- [[0003]]：本ワイヤ形式と Patch 変換の往復性を Lean モデルとの DRT・性質ベース試験・ファジングで検証する方針を定める。
- [[0009]]：本決定が前提とする。運搬対象のリソース封筒と「`null` なし・明示削除」の規約を定める。
- [[0011]]：本決定が前提とする。Patch の操作列を表す `order` 葉つきレコード集合の規約を定める。
- [[0013]]：Patch の `.set` / `.del` 対象となる葉パスに対する所有権管理を定める。
- [[0025]]：封筒の 2 入口（`Update` / `UpdateStatus`）と `meta/revision` の楽観ロックを定める。
- [[0026]]：封筒内の `Ref` 文法（`<修飾つき kind>/<name>[/meta|spec|status/...]`）と書き込み時検査を定める。
- [[0027]]：LKAL v0.2 スキーマによる検証と `spec` 既定値補完を定める。
- [[0028]]：順序付きレコード集合の純粋な核 `core/resource/orderedset` を定める。
- [[0029]]：本決定の G0〜G1 実現方式（`core/resource/patch` の純粋な Patch / Diff 核と Lean 4 モデル・10 定理・DRT、`Objects.Patch` / `PatchStatus` / `List`、および Go 標準 `net/http` による HTTP/2 LKAL-JSON ワイヤ層 `core/wire`）を定める。
