---
title: リソースの封筒を meta・spec・status の 3 レコードに固定する
status: accepted
date: 2026-11-17
depends-on: ["0001"]
---

# 0009: リソースの封筒を meta・spec・status の 3 レコードに固定する

## コンテキスト

[[0001]] により Nayutar の宣言基盤は LKAL、所有権と apply の中核は kisage の葉パス所有権モデルと定めた。LKAL の `docs/PRINCIPLE.md` では `$` で始まるユーザキー・ラベルが拒否され、`null` が存在せず、List は単一型のスカラーのみを保持する。リソースごとにトップレベル構造や状態条件の表現がばらつくと、葉パス所有権（[[0013]]）、`Ref` 検査（[[0010]]）、ワイヤ変換（[[0012]]）を共通ルールで扱えない。

## 決定

すべてのリソースの封筒構造を、LKAL 言語仕様は変えずに API サーバとスキーマが強制する規約として次のとおり固定する。

1. トップレベルは `meta`・`spec`・`status` の 3 レコードのみとし、`$` 接頭辞を含む予約キーは設けない。
2. `meta/uid`、`meta/revision`、`meta/generation`（`spec` が変化したときのみ増分）、`status/observedGeneration`、`status/condition/<Type>` 配下はサーバ専用欄とし、利用者の書き込みを禁止する。
3. 状態条件はリストではなく `<Type>` をラベルに持つレコード `status/condition/<Type>` とし、`state`・`reason`・`since` を持つ。
4. 未設定はパスの不在、空は `""`・`{}`・`[]` で表し、`null` は使わない。削除は明示的な削除操作として表現する。

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
    env "DB_PASSWORD" { from = &secret/db-creds/password }
  }
}
```

```lkal
status {
  observedGeneration = 7
  condition "Ready" { state = .true, reason = "MinimumReplicasAvailable", since = "2026-11-17T09:00:00Z" }
}
```

## 根拠（調査結果・出典）

- LKAL（`https://github.com/Kaikei-e/LKAL`）の `docs/PRINCIPLE.md`（2026-11-17 確認）：値型は 9 型であり `null` が存在せずキー不在で未設定を表すこと、空レコードは `{}` で空リストは `[]` であり最後の要素削除で `{}` が残ること、`$` 始まりのユーザキー・ラベルが拒否されること（L205）、List は単一型のスカラーのみを保持すること（L204）、ラベル付き項目はネストした Record の糖衣であり名前付きパスで全葉に到達できることを確認した。
- LKAL の ADR 0006（accepted）：`/` 区切りパスに対する Get / Set / Delete を規定していることを確認した。
- Kubernetes API conventions（`kubernetes/community` `api-conventions.md`、`docs/research/poc.md` §7 で引用、今回本文は未確認）：`spec` と `status` の分離、`generation` / `observedGeneration`、conditions による状態報告の枠組みに沿う。
- `docs/research/draft_20261117_001.md` §6.1・§6.3：封筒の 3 区分、サーバ専用欄、conditions のレコード化、未設定と空の整理に基づく。なお同節の LKAL 例には構文不備があったため、`lkal check` を通る構文へ修正して記載した。

## 検討した代替案

- **Kubernetes 型の `apiVersion` / `kind` / `metadata` トップレベルを採用する**：取り込み時の見た目は近くなる。しかしサーバ専用欄と識別情報を `meta` に集約できず、封筒の 3 層対称性が崩れるため採らない。
- **conditions をレコードのリストで表現する**：Kubernetes の `status.conditions` と同じ形になる。しかし LKAL の List はスカラーのみを保持でき Record を要素に持てないため書けず、条件ごとのパス特定もできなくなるため採らない。
- **`$meta` など `$` 接頭辞の予約キーを使う**：利用者データと名前空間を分けやすい。しかし LKAL が `$` 始まりのユーザキーを拒否しているため（エラーコード `L205`）文書として表現できず採らない。
- **`spec` と `status` を分けず平坦な文書にする**：構造が単純になる。しかし利用者の意図と観測状態の所有・権限境界を分離できず、世代番号による収束判定も成り立たないため採らない。

## 影響とトレードオフ

- **得るもの**：全リソースでパス体系とサーバ管理欄が統一され、葉パス所有権（[[0013]]）、`Ref` 検査（[[0010]]）、ワイヤ形式・Patch（[[0012]]）を単一の規約で扱える。
- **失うもの**：Kubernetes YAML の取り込み時に `metadata` から `meta` へ、配列 `conditions` からラベル付きレコードへの変換が常に必要になる。
- **引き受けるリスク**：将来 3 区分に収まらないトップレベル区画が必要になった場合、全リソースの変換規則とスキーマを見直す必要がある。また LKAL スキーマ言語は未実装であり、サーバ専用欄の書き込み禁止をスキーマでどう宣言するかは未確認である。
- **今後の検証ゲート**：G0 の最小 API サーバおよび変換・差分ハーネス（[[0003]]）で封筒構造と `generation` / `observedGeneration` の更新規則を検証する。3 区分で表現できない必須リソース要件が見つかった場合は本決定を再検討する。

## 関連ADR

- [[0001]]：本決定が前提とする。宣言基盤を LKAL、所有権の中核を kisage に固定している。
- [[0003]]：本封筒の変換・往復性を差分試験と性質ベース試験で検証する方針を定める。
- [[0010]]：本封筒内に書かれる `Ref` 値の書き込み時検査を定める。
- [[0012]]：本封筒を運ぶワイヤ形式と `del` 操作を含む Patch 形式を定める。
- [[0013]]：本封筒の各葉パスに対する所有権管理を定める。
