---
title: ワイヤ形式はまず LKAL-JSON を使い G2 からタグなし決定論的 CBOR を追加する
status: proposed
date: 2026-11-17
depends-on: ["0009"]
---

# 0012: ワイヤ形式はまず LKAL-JSON を使い G2 からタグなし決定論的 CBOR を追加する

## コンテキスト

[[0001]] は JSON の使用を認め（前提 3）、その根拠として「API ワイヤは LKAL-JSON、高頻度経路にはバイナリ表現（LKAL-CBOR）を足す」という層の分担を挙げている。[[0009]] でリソース封筒を固定した上で、API サーバとクライアント間の具体的なワイヤ形式および部分更新（Patch）形式を定める必要がある。LKAL にはすでに 9 型を可逆表現できる LKAL-JSON がある一方、CBOR 仕様と Diff/Patch 仕様はまだ存在しない。

## 決定

1. G0〜G1 の外部ワイヤ形式は HTTP/2 上の LKAL-JSON とし、既存の可逆形式（予約キー `$lkal`・`$enum`・`$qty`・`$ref`）を使用する。
2. G2 から LKAL-CBOR を追加する。データ構造は LKAL-JSON と同一とし、CBOR タグは使わずに決定論的に符号化する。CBOR タグの割り当て・登録は後回しにする。
3. ワイヤ上の Patch は、LKAL の ADR 0006（Get / Set / Delete）に合わせ、`/` 区切りパスに対する `test` / `set` / `del` 操作の列とする。ラベルによりパスが一意に決まるため、RFC 6902 のような配列添字は使わない。
4. Kubernetes の CBOR（KEP-4222）との互換性は目標にしない。
5. LKAL 本体に CBOR と Diff/Patch の仕様がまだないため、(2) と (3) は Nayutar 側に回避策を作らず、[[0001]] の前提 2 が許容する LKAL 側の変更として提案する。

```json
{
  "$lkal": "0.1",
  "spec": {
    "replicas": 3,
    "container": {
      "app": {
        "port": { "http": { "number": 8080, "protocol": { "$enum": "tcp" } } },
        "memory": { "$qty": "512Mi" },
        "env": { "DB_PASSWORD": { "from": { "$ref": "secret/db-creds/password" } } }
      }
    }
  }
}
```

```json
{"$lkal":"0.1","patch":[{"op":"test","path":"meta/revision","value":4812},
 {"op":"set","path":"spec/replicas","value":5},{"op":"del","path":"spec/container/sidecar"}]}
```

## 根拠（調査結果・出典）

- LKAL（`https://github.com/Kaikei-e/LKAL`）の `docs/PRINCIPLE.md`（§7、2026-11-17 確認）：`ToJSON` / `FromJSON` が `$lkal`・`$enum`・`$qty`・`$ref` の 4 つの予約キーを用いて 9 型すべてを可逆往復でき、`$` 始まりのユーザキー・ラベルを拒否するため衝突しないことを確認した。なお `docs/research/draft_20261117_001.md` §2.3 は Decimal を 10 進文字列と記していたが、実際の LKAL-JSON では Decimal は引用符なしの JSON 数値（`0.25`）であり、引用符付き（`"0.25"`）は String になることを確認した。
- LKAL の ADR 0006（accepted）およびリポジトリ状況（2026-11-17 確認）：`/` パスの Get / Set / Delete が規定されている一方、CBOR および Diff/Patch に関する ADR や文書はまだ存在しないことを確認した。
- RFC 8949 の決定論的符号化規定の詳細は未確認である（`docs/research/draft_20261117_001.md` §2.4・§6.4）。
- Kubernetes KEP-4222（https://kubernetes.io/docs/reference/command-line-tools-reference/feature-gates/ ）：`CBORServingAndStorage` 機能ゲートは v1.37 でも Alpha かつ既定で無効であることを確認した。`docs/research/draft_20261117_001.md` §2.3 にあった「索引上 beta/1.37」という食い違いは Alpha で解消した。
- `docs/research/draft_20261117_001.md` §6.4・§6.8・§11：段階導入と Patch 例に基づく。

## 検討した代替案

- **最初から Protobuf をワイヤ形式にする**：バイナリ効率は高い。しかしリソースごとの `.proto` 定義と生成コードの管理が加わり、[[0009]] の汎用 LKAL 文書モデルや差分ハーネスでの直接検査と相性が悪いため採らない。
- **最初から 9 型へ CBOR タグを割り当てる**：`$enum` 等のマップ表現より小さくなる。しかし IANA タグ登録や独自タグ管理のコストを初期から負いデバッグ性も下がるため、まずタグなしで始める。
- **`$` 予約キーを持たない plain JSON を使う**：一般の JSON API に近くなる。しかし Enum・Quantity・Ref と通常の String や Map の区別が消えて LKAL の 9 型情報が失われるため採らない。plain JSON は外部標準との境界に限る。
- **JSON Patch（RFC 6902）または JSON Merge Patch を使う**：既存仕様を流用できる。しかし JSON Merge Patch は `null` による削除を前提としており [[0009]] の「`null` なし」と矛盾し、RFC 6902 は配列添字に依存するためラベルでパスが一意になる LKAL には合わないため採らない。

## 影響とトレードオフ

- **得るもの**：G0〜G1 は実装済みの LKAL-JSON をそのまま使い、シリアライズ仕様の立ち上げを待たずに API サーバと試験基盤を構築できる。`test` / `set` / `del` の Patch は [[0013]] の葉パス所有権検査へ直接対応づけられる。
- **失うもの**：G0〜G1 ではテキスト JSON の帯域・パース負荷を負う。また G2 のタグなし CBOR でも `$enum` 等のマップキー分のオーバーヘッドは残る。
- **引き受けるリスク**：LKAL 側で CBOR と Diff/Patch の仕様・実装を追加する必要があり、RFC 8949 の決定論的符号化の詳細仕様は未確認のまま残る。
- **今後の検証ゲート**：G0 で LKAL-JSON と Patch 適用の往復性を性質ベース試験・ファジング（[[0003]]）で検証する。G1〜G2 でシリアライズの CPU・帯域がボトルネックとなった段階で LKAL-CBOR を導入し、タグなし表現でも容量や速度が要件を満たさない場合にタグ割り当てを再検討する。

## 関連ADR

- [[0001]]：前提 3 で JSON の使用を認め、前提 2 で LKAL 側の変更（Diff/Patch など）を許容している。
- [[0003]]：本ワイヤ形式と Patch 変換の往復性を性質ベース試験・ファジングで検証する方針を定める。
- [[0009]]：本決定が前提とする。運搬対象のリソース封筒と「`null` なし・明示削除」の規約を定める。
- [[0013]]：Patch の `set` / `del` 対象となる葉パスに対する所有権管理を定める。
