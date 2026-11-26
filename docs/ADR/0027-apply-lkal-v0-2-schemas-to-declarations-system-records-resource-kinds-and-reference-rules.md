---
title: LKAL v0.2 スキーマを環境宣言・内部記録・リソースの種別検査と既定値補完・参照規則の導出に適用する
status: accepted
date: 2026-11-21
depends-on: ["0001", "0009", "0010", "0011", "0020", "0022", "0024", "0025", "0026"]
---

# 0027: LKAL v0.2 スキーマを環境宣言・内部記録・リソースの種別検査と既定値補完・参照規則の導出に適用する

## コンテキスト

[[0001]] の前提 2 で宣言基盤に据えた LKAL に、v0.2 スキーマ検証パッケージ（`github.com/Kaikei-e/LKAL/schema`、`lkal-schema = "0.2"`）および `lkal` コマンドの `schema check`・`check --schema`・`set --schema` を含むタグ付きリリース `v0.2.0` が公開された。

これまで LKAL にスキーマとリリースタグが無かったため、実装済みの各領域には次の暫定措置が残っていた。

- `go.mod` と CI（`.github/workflows/ci.yml`）がタグの無い擬似バージョン（`v0.0.0-20261114132324-790bdd412dd7`）を参照していた（[[0025]]）。
- 開発環境の宣言 `config/dev.lkal` にスキーマが無く、`cmd/dev-etcd` は外部の `lkal json -plain` サブプロセスと `encoding/json` の構造体で未知欄を通したまま復号していた（[[0020]]）。
- システム管理キーの記録（[[0022]] の `/sys/tenants/<tenant>`、[[0024]] の `/sys/nodes/<node>` と `/sys/liveness/evictions`）は LKAL-JSON を保存形式としながら、`encoding/json/v2` の専用構造体と手書きの欄検査で読み書きしていた。
- リソース封筒のストア結合 `core/resource`（[[0025]]・[[0026]]）は封筒の形だけを検査し、種別ごとの `spec`・`status` の型検査、宣言的な `default` の補完、[[0011]] の順序付きレコード集合の `order` 一意性検査を持たず、参照の許可規則も Go の表（`refs.Schema`）を手で渡していた。

一方で、[[0025]] の `core/resource/envelope` と [[0026]] の `core/resource/refs` はそれぞれ Lean モデル（`spec/Nayutar/Envelope.lean`・`spec/Nayutar/Refs.lean`）および保護された定理（計 18 個）と DRT で 1 対 1 に結ばれており、純粋な核の意味論と誤り順序は壊してはならない。また、スキーマのストア保存（`/x/<identityHash>/schema/<version>`）は Export と `Binding` の設計と不可分である。

本 ADR は、`/x/` へのスキーマの保存を Export と `Binding` の ADR に残したうえで、既存の全箇所に LKAL `v0.2.0` と v0.2 スキーマを適用する方式を決める。

## 決定

1. **LKAL の依存と CLI を `v0.2.0` に更新し、CI と編集後フックでスキーマを検査する。**
   - `go.mod` の `github.com/Kaikei-e/LKAL` と `.github/workflows/ci.yml` の `lkal` 導入版を `v0.2.0` に固定する。
   - スキーマ文書の拡張子を `.schema.lkal` とし、同じ stem を持つ `<name>.lkal` と `<name>.schema.lkal` が隣り合う場合は対として扱う。
   - CI の `lkal` ジョブと `scripts/agent-harness/post_edit_checks.py` は、すべての `.lkal` に対する `lkal check`・`lkal fmt` に加え、`*.schema.lkal` に `lkal schema check` を、対になるスキーマを持つ `.lkal`（`config/dev.lkal` を含む）に `lkal check --schema` を実行する。
2. **開発環境の宣言に `config/dev.schema.lkal` を与え、`cmd/dev-etcd` は Go 内で直接検証・読込する。**
   - `config/dev.schema.lkal` で `etcd` レコード、`version`（空白を含まない非空文字列）、`member` マップ（ちょうど 3 要素、キー `[A-Za-z0-9_-]+`、`client-url` と `peer-url` の必須文字列および `unique-by = ["client-url", "peer-url"]`）を宣言し、`config` パッケージに埋め込む。
   - `cmd/dev-etcd` は外部の `lkal` 実行ファイルを呼ばず、`lkal.Parse` と埋め込みスキーマの `Validate` で検証してから構造を読み取る。URL が平文 `http` のループバック IP リテラルであることの検査は引き続き Go 側で行う。
3. **システム内部の LKAL-JSON 記録を、埋め込み v0.2 スキーマと `lkal` パッケージで読み書きする。**
   - `core/store/tenant` に `tenant.schema.lkal`（`state` が `.active`・`.terminating` の必須 Enum）、`core/liveness` に `node.schema.lkal`（`evictable`、`rack` 1〜63 文字の RFC 1123 ラベル、`since` `0..2^53-1`、`state` `.pending`・`.ready`・`.notready`）と `evictions.schema.lkal`（`granted` が `0..2^53-1` の整数リスト）を埋め込む。
   - `encoding/json/v2` の専用構造体を廃止し、`lkal.FromJSON` と `schema.Validate`（書き込みは `ValidateValue` と `lkal.ToJSON` の空白なし正準形）で扱う。`core/liveness` の正準バイト列一致検査と `granted` の非減少順検査はそのまま維持する。
4. **封筒の共通 v0.2 スキーマを `core/resource/envelope/envelope.schema.lkal` に置く。**
   - [[0009]]・[[0025]] の封筒の形（`meta`・`spec`・`status`、小文字の `kind`、ドットを含む `group`、UUID、条件の `<Type>`・`state`・`reason`・`since`）を v0.2 スキーマとして記述し、`envelope.Schema()` として公開する。
   - 純粋な核 `envelope` の関数と Lean モデルは変えず、妥当な LKAL 値の木の上で `envelope.Schema().ValidateValue` と `envelope.CheckShape` の成否が一致することを試験とファジングで確かめる。
5. **`core/resource` に種別ごとの LKAL v0.2 スキーマ（`KindSchema`）を導入し、既定値補完・値検証・参照規則の導出を一本化する。**
   - `CompileKindSchema` は `lkal-schema = "0.2"` の文書を `schema.CompileFile` でコンパイルしたうえで、リソース種別スキーマとしての不変条件を検査する。
     - `def` の構造的な再帰（レコード・マップ・リストを介した自己到達）を拒否する。
     - `root` は `open = false` とし、宣言できる欄を `meta`・`spec`・`status`（いずれもレコードまたはマップ）に限る。
     - `meta` と `status` の部分木には `.ref` 型と `default` を置けない（[[0025]] の入口分離、および [[0026]] 決定 2 の「`Ref` は `spec` の中にだけ置ける」と一致させるため）。
     - `spec` の部分木に現れるすべての `.ref`（スカラー共用体・リスト要素・マップ値を含む）は、[[0022]] の修飾つき kind 1 セグメント（`&secret`、`&widget.apps.example.com` など）からなる非空の `targets` を持たなければならない。`.ref` に至るパスのレコード欄名に空文字列・`/`・`*` は使えない。
     - `status` が `open = false` のレコードとして宣言されている場合、予約（`reserved`）されていなければ封筒の `observedGeneration` と `condition` を自動で許可する。
   - `CompileKindSchema` は `spec` の部分木を走査し、レコード欄名をそのまま、マップの値を `*`、リスト要素をリスト欄自身のパスとして `[]refs.Rule` を導出する。`open = true` や `type = .any` の箇所からは規則を導出しない（そこへ置かれた `Ref` は `refs` 核が fail-closed で拒否する）。
   - `resource.Config` は修飾つき kind をキーとする `Schemas` を受け取る（従来の `Config.References` は廃止し、`KindSchema` から導出した `refs.Schema` を内部で組み立てる。純粋な核 `core/resource/refs` の `refs.Schema` 型と API は変えない）。
   - `Objects` の書き込み（`Create`・`Update`・`UpdateStatus`）は `Schemas` に対して fail-closed とする。設定自体に不正な修飾つき kind や `nil` があれば `internal` を返し、未登録の kind への書き込みは `malformed`（`meta/kind`）で拒否する。`Get` と `Delete` はスキーマの有無や変更に依存せず保存済みのオブジェクトを読み出し・削除できる。
   - `Create` と `Update` では、候補を正規化したあと、`envelope` の遷移と `refs` の検査に入る前に `spec` に対して `schema.Complete` で宣言的 `default` を補完する。これにより、`generation` は補完後の `spec` が変わったときだけ増え、`default` が `Ref` を含む場合も追加された出現として Cedar 認可・存在確認・逆参照索引の同一 `Txn` 更新を必ず通る。`UpdateStatus` では `default` 補完を行わない。
   - 種別スキーマによる値検証（`Create`・`Update` は `spec`、`UpdateStatus` は `status`）は、候補の形の検査として `envelope` の遷移の直後（保存済みの `Ref` 検査のあと、候補の `Ref` 検査の前）に行う。`L508`（参照先の不一致）以外の検証エラー（`L501`〜`L507`、`L509`〜`L510`。[[0011]] の `unique-by = ["order"]` を含む）は `envelope.ErrMalformed` として返し、参照の文法・上限・参照先 `kind`（`refs.ErrKind`）・認可・存在の判定と誤り順序は [[0026]] の `refs` 核にそのまま委ねる。

## 根拠（調査結果・出典）

- **LKAL `v0.2.0` の公開 API と不変条件**（`https://github.com/Kaikei-e/LKAL`、`docs/PRINCIPLE.md` §8、`schema/meta.lkal`、`schema/schema.go`、`schema/validate.go`、`schema/complete.go`、2026-11-21 確認）：
  - `schema` サブパッケージは片方向依存で隔離されており、`Parse`・`Format`・`ToJSON`・`FromJSON`・`Equal` の意味論を一切変えない。
  - `(*Schema).Complete(v)` は親の `Record` が存在するときに限り省略された任意フィールドの `default` を非破壊・冪等に補完し、欠落している親レコードは勝手に作らない。また `required = true` と `default` の併用や循環する `default` 展開はコンパイル時に `L606` で拒否される。
  - `.ref` 制約の `targets` は `&<prefix>` のリストであり、`.map` は `key` 制約と `unique-by`（`L509`）を持つ。[[0011]] が求めた順序付きレコード集合の `order` 必須（`L502`）と一意性（`L509`）は、追加の拡張なしで `unique-by = ["order"]` によりそのまま記述できる。
  - `.string` とマップ `key` の `min-length`・`max-length` は UTF-8 コードポイント数（`utf8.RuneCountInString`）で判定されるが、`pattern` は `\A(?:...)\z` で全体一致するため、ASCII 文字集合の正規表現と組み合わせるとバイト長とコードポイント長が一致し、[[0022]]・[[0024]]・[[0025]] の ASCII 文法と厳密に一致する。
- **内部不変条件（[[0025]]・[[0026]] の誤り順序と定理の保全）**：
  - [[0025]] の 10 定理と [[0026]] の 8 定理はそれぞれ `envelope` と `refs` の純粋な核に対応する。`spec` の `default` 補完を遷移前に行い、種別スキーマの `L508` を `refs.Prepare` の段 5（追加された出現のみの検査 → `refs.ErrKind`）に委ねることで、「既存の `Ref` は再検査しない」「不正な `Ref` 文法は段 3 で `refs.ErrMalformed` になる」「`default` で入った `Ref` も認可と逆参照索引を通る」という既存の全不変条件が保たれる。

## 検討した代替案

- **`core/resource` の種別スキーマを見送り、`config/dev.lkal` と内部記録だけに適用する**：変更は小さい。しかし [[0026]] がスキーマ未実装ゆえの暫定策とした Go の `refs.Schema` 直書きが残り、同じリソース種別の型制約と参照規則が二重管理になるため採らない。
- **`refs` 核を廃止して `schema.ValidateValue` の `L508` だけで参照先 `kind` を検査する**：導出処理が要らなくなる。しかし `schema.ValidateValue` は文書全体を毎回検査するため、[[0010]]・[[0026]] の「追加された出現だけを検査し、既存 `Ref` は再検査しない」規則および段 3（文法誤り）と段 5（`ErrKind`）の誤り順序と食い違い、`open = true` や `.any` の下に置かれた未宣言の `Ref` も素通しするため採らない。
- **`Create` / `Update` で `schema.Complete` を呼ばず検証のみにする**：補完後の値の扱いを考えずに済む。しかし v0.2 スキーマに書いた `default` が保存値に反映されず、同じ省略入力の再送やコントローラの読み取りでスキーマの宣言と実データが乖離するため採らない。
- **`status` にも `default` 補完を許す**：対称に見える。しかし `Update` の入口で `status` を補完すると保存済みの `status` と食い違って `ErrForbidden` を誘発し、観測状態である `status` に宣言的既定値を入れる意味も薄いため、`default` は `spec` に限る。
- **`Config.Schemas` が未設定のときはスキーマ検査を飛ばして任意の kind を通す**：既存テストの書き換えが減る。しかし設定漏れで未検証のオブジェクトが黙って保存され fail-closed に反するため採らない。

## 影響とトレードオフ

- **得るもの**：
  - 環境宣言・システム内部記録・リソース封筒・種別ごとの `spec`/`status`・参照許可規則・順序付きレコード集合（[[0011]]）のすべてが LKAL `v0.2.0` の単一のスキーマ言語で検証される。
  - `cmd/dev-etcd` が外部 `lkal` バイナリへの実行時依存を持たなくなる。
  - `tenant` と `liveness` から `encoding/json/v2` の手書き構造体が消え、LKAL-JSON の読み書きが LKAL モジュールに統一される。
  - `Create` / `Update` で `default` が正準に補完されつつ、[[0025]]・[[0026]] の 18 定理と DRT がそのまま維持される。
- **失うもの**：
  - `Objects` を使う呼び出し側（試験を含む）は、書き込む対象 kind の `KindSchema` を設定に渡す必要がある。
  - リソース種別スキーマでは再帰的な `def` や `targets` の無い `.ref` を使えない。
- **引き受けるリスク**：
  - [[0013]]（葉パス所有権）を実装する際、`Create` / `Update` で `schema.Complete` が補完した葉の所有者（Actor / Origin）をどう記録するか、また利用者が明示的に削除した任意欄に `default` が再補完される挙動と `prune` の関係を [[0013]] の実現 ADR で厳密に定める必要がある。
  - スキーマ自体のストア保存（`/x/<identityHash>/schema/<version>`）と版管理はまだ無く、プロセス起動時のインメモリ構成にとどまる。
- **今後の検証ゲート**：G0 において、CI の `lkal` ジョブと編集後フックで全 `.schema.lkal` と宣言ファイルの検査を通し、封筒スキーマと `envelope.CheckShape` の一致ファジング、種別スキーマの既定値補完・`unique-by`・参照規則導出の単体／ストア結合試験（インメモリおよび 3 メンバー etcd）、ならびに全 7 スイートの DRT と 34 個の保護定理の検査を通す。[[0013]] の実装時に `Complete` と葉パス所有権の相互作用を形式モデルに含めて検証する。

## 関連ADR

- [[0001]]：宣言基盤を LKAL とし、LKAL 側の Schema 拡張を許容する前提を定める。
- [[0009]]：封筒構造を API サーバとスキーマが強制すると定める。本 ADR はその v0.2 スキーマを与える。
- [[0010]]：書き込み時の参照先 `kind` のスキーマ検査を定める。
- [[0011]]：順序付きレコード集合の `order` 必須・一意性のスキーマ検査を定める。本 ADR は `unique-by = ["order"]` でこれを実現する。
- [[0013]]：葉パス所有権モデルを定める提案。`schema.Complete` が補完した葉の所有権の扱いは同 ADR の実現時に定める。
- [[0020]]：`config/dev.lkal` と `cmd/dev-etcd` を定める。本 ADR は `config/dev.schema.lkal` と Go 内直接検証へ更新する。
- [[0022]]：修飾つき kind の文法と `/sys/tenants/<tenant>` の記録を定める。
- [[0024]]：`/sys/nodes/<node>` と `/sys/liveness/evictions` の記録を定める。
- [[0025]]：封筒の純粋な核と `core/resource` のストア結合を定める。
- [[0026]]：参照検査の純粋な核 `core/resource/refs` とストア結合を定める。本 ADR は `Config.References` を `KindSchema` からの導出に置き換える。
