---
title: 葉パス所有権を、圧縮トライ符号化・純粋な CAS 計画とプロトコル核・オブジェクトストア結合・HTTP/2 サブリソースで実現する
status: accepted
date: 2026-11-23
depends-on: ["0001", "0002", "0003", "0004", "0006", "0009", "0010", "0011", "0012", "0013", "0022", "0025", "0026", "0027", "0028", "0029"]
---

# 0030: 葉パス所有権を、圧縮トライ符号化・純粋な CAS 計画とプロトコル核・オブジェクトストア結合・HTTP/2 サブリソースで実現する

## コンテキスト

[[0013]] は、kisage の葉パス所有権モデル（共有所有状態 $\sigma$：葉パスから Actor・Origin・revision への写像）をオブジェクト本体 `/t/<tenant>/o/<group>/<kind>/<name>` とは別の独立キー `/t/<tenant>/own/<group>/<kind>/<name>`（[[0006]]・[[0022]]）に分離し、両キーの `mod_revision` を比較する単一のストア `Txn`（[[0004]]）で原子的に更新し、競合・構造衝突・外部ドリフトを診断コード `K101`–`K604` で fail-closed に拒否し（`force` なし）、`apply` と `prune` を分離して `Origin = Created` の葉のみを自動削除対象とし、`--expect` と `--owner-revision` を前提条件として引き継ぎ、作成者を既定所有者とした圧縮トライと整数 Actor 辞書で保持する方針を定めた。

これまでにリソース封筒の 2 入口（[[0009]]・[[0025]]）、参照検査と逆参照索引（[[0010]]・[[0026]]）、LKAL v0.2 スキーマと `spec` の既定値補完（[[0027]]）、順序付きレコード集合（[[0011]]・[[0028]]）、および純粋 Patch / Diff 核と HTTP/2 LKAL-JSON ワイヤ層（[[0012]]・[[0029]]）が揃っている。[[0013]] を既存の不変条件と矛盾なく実装・形式検証に落とすには、次の細目を固定する必要がある。

- **2 つの書き込み入口（[[0025]]）と対象パスの境界**：`meta` の同一性欄（`group`・`kind`・`name`）とサーバ管理欄（`uid`・`revision`・`generation`）を葉パス所有権に含めると、2 人目の Actor が有効な封筒を送った時点で同一性欄に `K201` が発生する。また `EntranceStatus`（`UpdateStatus` / `PatchStatus`）はコントローラやノードが高頻度で更新し条件の `since` をサーバが付与する。
- **20% サイズ予算と既存の `Create` / `Update` / `Patch` の整合**：`/own/` に `LastApplied` の葉値そのものを重複保存すると `/own/` の大きさが本体と同等以上になる。本体 `o/` 自体を `LastApplied` の正とするには、`EntranceSpec` へのあらゆる書き込み（既存の `Create`・`Update`・`Patch` を含む）が `/own/` の所有権検査と同一 `Txn` 更新を通る必要がある。
- **`schema.Complete` による `spec` 既定値補完（[[0027]]）との相互作用**：宣言の葉パス射影より前に `schema.Complete` を実行すると、書き手が省略した任意欄まで所有権を主張して偽の `K201` 競合を起こす。逆に射影後に補完した既定値葉を通常の未所有葉（`released` 等と同格）として残すと、別の Actor が後から非既定値を宣言した際に `K206`（未所有値の不一致）が発生してしまう。
- **順序付きレコード集合（[[0028]]）と `Prune` 後の空親レコード**：kisage ファイル版のように葉削除後に空の中間レコード `{}` を残すと、必須の整数 `order` 葉を持つ順序付きレコード集合の要素レコードが `{}` になり、`KindSchema` の検証（`L502`）に落ちる。
- **CAS 前提でのプロトコル縮退と `--owner-revision`**：ファイル版 kisage の 2 段ロック・Intent ステージング・クラッシュ回復（`Pending`・`K205`・`K501`・`K503`・`K601`）は、単一 CAS `Txn` では状態遷移から消滅する。また `/own/` キー自体のストア `mod_revision` は削除・再作成を跨いで単調増加するため、パスごとの永久墓石を持たずに ABA を防げる。

## 決定

1. **所有権管理の対象を `EntranceSpec`（`meta/labels/*` と `spec/**`）に固定する。**
   - 共有所有状態 $\sigma$ の定義域は、`meta/labels/<key>` の葉と `spec` 配下の葉（LKAL の非 `Record` 値：7 種のスカラーと単一型スカラー `List`）のみとする。`meta` の同一性・サーバ管理欄および `status` 配下は $\sigma$ に含めず、`EntranceStatus`（`UpdateStatus` / `PatchStatus`）は従来どおり `o/` の `mod_revision` CAS のみで更新する。
   - 宣言におけるトップレベルの空セクション `meta/labels = {}` および `spec = {}` は空の葉集合を表す正当な入力とし、`Apply` / `Prune` の宣言内部に現れる入れ子の空レコード（`spec/foo = {}` など）は `K102`（`EmptyRecordNotSupported`）で拒否する。

2. **純粋な計画・遷移・トライ符号化の核を `core/resource/ownership` に置く。**
   - **Actor と Scope**：`ActorID` は制御文字を含まない非空文字列とする。`Scope` はパス規則（ルート全体・部分木・完全一致、すべて `/` 区切りセグメント単位）から権限ビット集合 `{Set, Delete, Own, Resolve}` への写像とし、空の `Scope` は全拒否とする。`Resolve` は他の権限から暗黙に導出しない。必要な権限は、葉の設定・新規取得が `Set+Own`、同一値採用・管理解除が `Own`、`Prune` と命令的削除が `Delete+Own`、明示的な `Adopt` と `Release` が `Own+Resolve` とし、`NoOp`・`Deferred`・`Blocker` には権限検査を行わない。
   - **Origin と単一所有者排他**：`Origin` は `.created`（不在または `.defaulted` からの作成で付与され、`Prune` での自動削除権の唯一の根拠）と `.modified`（同一値採用または明示 `Adopt` で付与され、自動削除権を持たない）の 2 値とする。同一パス・真の先祖・真の子孫のいずれかで他 Actor の所有葉と重なる宣言は、値が等しくても `K201`（`OwnershipConflict`）で拒否する。
   - **診断コードと優先順位**：API サーバ版で到達可能な kisage 診断コード `K101`（不正入力）・`K102`（入れ子空レコード）・`K201`（所有権競合 / `ownerRevision` 不一致）・`K202`（葉とレコードの構造衝突）・`K203`（外部ドリフト / `expect` 不一致）・`K204`（自己 `.created` 退役葉との構造衝突による移行要求）・`K206`（未所有葉との値不一致）・`K301` / `K302`（観測不能 / 表現不能）・`K401`（`Scope` 拒否 / Actor 偽称）・`K502`（ストア `Txn` 結果不明）・`K602`（削除権放棄通知）・`K603`（自己 `.modified` 退役に伴う 2 段評価通知）・`K604`（`Prune` 未実行の `.created` 退役葉通知）を採用する。ブロッカーの優先順位は `K301 > K201 > K204 > K202/K302 > K206 > K203 > K401` とし、同一モード内で 1 パスでもブロッカーまたは権限拒否があれば変更を一切適用しない（All-or-Nothing ゲート）。
   - **状態トークン `expect`**：パス単位トークンは `"ksg-tok-v1:" + hex(sha256(group, kind, name, path, ancestorKinds, observedValue))`、文書単位トークンは `"ksg-doc-v1:" + hex(sha256(group, kind, name, canonicalEntranceSpec))` とする。

3. **圧縮トライ形式の LKAL-JSON スキーマ（`ownership.schema.lkal`）で `/own/` を符号化する。**
   - `/t/<tenant>/own/<group>/<kind>/<name>` の値は空白なしの単一行 `@lkal "0.1"` LKAL-JSON とし、埋め込みの再帰スキーマ `core/resource/ownership/ownership.schema.lkal` で検証する。
   - ルートは必須の非空文字列リスト `actors`（`actors[0]` が作成者＝既定所有者）と任意の `.map` `trie` を持つ。各トライ節点は閉じたレコードで、状態 `s`（`.owned`・`.unowned`・`.defaulted`）、整数 Actor 添字 `a`（`s = .owned` のとき必須）、Origin `o`（`.created` / `.modified`、`s = .owned` のとき必須）、部分木圧縮フラグ `w`（`Bool`、現在 `o/` に存在する配下の全葉が同一の `(a, o)` を持つときの略記）、および子セグメント `.map` `c` を持つ。
   - `LastApplied` の葉値は `/own/` に重複保存せず、本体 `o/` の現在値を参照する。`o/` の `EntranceSpec` に存在する葉のうちトライ上で状態指定を持たないものは `(actors[0], .created)` と復号し、`(a = 0, o = .modified)`・`a != 0`・`.unowned`（明示 `Release` や管理解除後の残存葉）・`.defaulted`（スキーマ既定値のみで実体化された葉）は必ずトライに明示記録する。書き込み時には参照されなくなった `actors[1..]` を除去して添字を詰める。

4. **`schema.Complete`（[[0027]]）との結合と `Prune` の空レコード除去を定める。**
   - `Create` と `Apply` では、`schema.Complete` を呼ぶ前の生の正規化候補から宣言葉集合 $D$ を抽出して所有権計画を実行し、計画後の文書に `ks.completeCandidate` を適用する。`completeCandidate` によって新たに実体化された葉はトライ上で `.defaulted` として記録する。`.defaulted` の葉は後続のどの Actor の `Apply` / `Update` / `Patch` からも不在葉と同様に `K206` なしで取得でき（`Origin = .created`）、`Prune` で削除された欄に親レコードが残っていてスキーマ `default` がある場合は既定値が再補完されて `.defaulted` に戻る。
   - `Prune` で `Origin = .created` の葉を削除した際、`meta/labels` および `spec` 配下で全フィールドが空になった中間レコードはボトムアップに除去する（トップレベルの `spec = {}` は封筒規約に合わせて保持可能とする）。これにより [[0028]] の必須 `order` 葉を持つ要素レコードが丸ごと退役したときも `KindSchema` 検証を通過する。

5. **`core/resource.Objects` の全書き込みを `/own/` の単一 CAS `Txn` に統合する。**
   - `Config` に `ScopeResolver`（`(refs.Principal, keyspace.Object) -> (ownership.ActorID, ownership.Scope, error)`）を追加する。`ScopeResolver` が設定されている場合は fail-closed で解決し、未設定のライブラリ単体利用時は非空 `Principal` を `"<Type>:<ID>"`（空 `Principal` は `"system:anonymous"`）に写像して `EntranceSpec` に対する `Set|Delete|Own` を既定付与する。
   - 既存の `Create`・`Update`・`Patch` も `EntranceSpec` の所有権検査と `/own/` 更新を必ず通し、他 Actor 所有葉への変更・削除を `K201` で拒否し、本体検査・スキーマ検査・`checkRefs`（[[0026]]）の通過後に `o/`（`ModRev`）・`own/`（`ModRev`）・追加参照先（`ModRev`）を比較する単一の `Txn` で `o/`・`own/`・逆参照索引を原子的に更新する。`Delete` は `o/`・`own/`・逆参照索引を同一 `Txn` で削除する。
   - 新たに `Objects.Apply`（不在時は `ModRev = 0` CAS で新規作成、既存時は更新）・`Objects.Prune`・`Objects.Explain`・`Objects.Adopt`・`Objects.Release` を追加する。`o/` の値が変化せず `/own/` のみが変化する遷移（`Adopt`・`Release`・同一値採用・管理解除）では、`CmpEqual(oKey, oModRev)` と `CmpEqual(ownKey, ownModRev)` の両方を比較しつつ `OpPut(ownKey, ...)` のみを発行し、`o/` への不要な書き込みと watch イベント発火を抑止する。`ownerRevision` は `own/` キーの `ModRev` とする。

6. **`core/wire` に所有権サブリソースと構造化診断応答を追加する。**
   - `/t/{tenant}/o/{group}/{kind}/{name}` 配下に次の HTTP/2 サブリソースを設ける（[[0029]] と同じく未知クエリ引数は `400`、全セキュリティヘッダと `MaxBodyBytes` を適用する）：
     - `PUT .../apply`（任意の `expect`・`ownerRev` クエリ引数）：宣言文書を `Objects.Apply` に渡し、新規作成時は `201 Created`、更新時は `200 OK` で封筒と `Nayutar-Owner-Revision` ヘッダを返す。
     - `POST .../prune`（任意の `expect`・`ownerRev` クエリ引数）：宣言文書を `Objects.Prune` に渡し、`200 OK` で封筒と `Nayutar-Owner-Revision` ヘッダを返す。
     - `GET .../explain`（任意の `atRev` クエリ引数）：`explain.schema.lkal` に従う `@lkal "0.1"` 文書（`revision`・`ownerRevision`・文書 `expect`・`defaultOwner`・各葉パスの状態とパストークン）を `200 OK` で返す。
     - `POST .../adopt` および `POST .../release`：`resolve.schema.lkal` に従う `@lkal "0.1"` 文書（`path`・`expect`・`ownerRevision`）を受け取り、`200 OK` で更新後の `explain` 文書を返す。
   - `error.schema.lkal` を拡張し、任意の `code`（`"K101"`〜`"K604"`）と順序付きレコード集合 `diagnostics` を構造化エラーに含める。HTTP ステータスは `K101`/`K102` を `400`、`K401` を `403`、`K201`/`K202`/`K203`/`K204`/`K206` を `409 Conflict` に写像する。

7. **CAS 版所有権計画とプロトコルを Lean 4 でモデル化・証明し、DRT で Go 実装と照合する。**
   - `spec/Nayutar/Ownership.lean`（`Kisage`・`Nayutar.Envelope`・`Nayutar.Store` を import）に複数パス葉射影・5 段 `Plan`・`Adopt` / `Release`・圧縮トライ符号化・単一 CAS `Txn` プロトコル遷移を実装し、`nayutar-drt ownership`（`spec/Nayutar/Drt/Ownership.lean` と `core/drt/ownership_drt_test.go`）で Go 実装と照合する。
   - `spec/Nayutar/Proofs/Ownership.lean`（および保護対象の `Statements/Ownership.lean`・`comparator.json`）で、Plan の安全性性質（フレームと権限境界・構造非破壊・`Prune` の `.created` 限定保守的削除・他 Actor / ドリフト / 未所有不一致の上書き禁止・収束不動点・決定性・All-or-Nothing ゲート・`Adopt` / `Release` 安全性・モード間分離）および CAS プロトコル不変条件（単一所有者排他・`.created` 来歴の健全性・競合時の無変更・`o/` と `own/` の原子的対更新・圧縮トライ往復一致）を `sorry` なし・許容公理のみで証明する。

## 根拠（調査結果・出典）

- **内部不変条件（kisage `v0.1.2` の純粋 `Plan` とプロトコル境界）**（`spec/.lake/packages/kisage/` の `Kisage/Model.lean`・`Kisage/Plan.lean`・`Kisage/Theorems.lean`・`Kisage/Protocol.lean`・`docs/PRINCIPLE.md`、Apache-2.0、2026-11-23 確認）：
  - `Kisage/Theorems.lean` の Plan 11 性質は単一ロックやファイル I/O に依存しない純粋計画関数上の性質であり、複数パス木への拡張でもそのまま成立する。一方、`Kisage/Protocol.lean` の `PendingIntent`・`Recover`・`Stabilize` は 2 段ファイルロックと非原子的プロバイダ適用を補う中間状態であり、複数キー CAS `Txn`（[[0004]]）の上では単一遷移に縮退する。
  - トライ復号時に `(actors[0], .created)` の暗黙既定を「トライ指定のない現存葉」に限定し、`actors[0]` であっても `.modified` の葉と `.unowned` / `.defaulted` の葉を明示記録することで、`Adopt` による `.modified` 葉が `.created` へ不正昇格して `Prune` で誤削除される経路を完全に遮断できる。
- **内部不変条件（[[0025]]・[[0026]]・[[0027]]・[[0028]]・[[0029]] との整合）**：
  - `LKAL/schema` `v0.2.0` の `schema.Compile` は `def` 間の `&def/node` 構造再帰を許容するため（直接エイリアス循環 `L603` のみ禁止）、圧縮トライ自体を埋め込み `.schema.lkal` で厳密検証できる。
  - 単一作成者のオブジェクトでは `/own/` は `{"$lkal":"0.1","actors":["User:alice"]}`（約 38 バイト）となり、複数 Actor が協調する典型的なワークロード宣言（800〜1,200 バイト）でも約 13〜18% に収まり、[[0013]] の 20% 初期閾値を満たす。
  - `Objects` の 1 回の更新 `Txn` が持つ比較数・操作数の上限は、本体 1 + 所有権 1 + 参照先最大 32 + テナントフェンス 1 = 最大 35 比較、書き込み最大 66 操作となり、etcd v3 の既定 `MaxOpsPerTxn = 128`（https://etcd.io/docs/v3.5/learning/api/ ）の範囲内に余裕をもって収まる。
- **HTTP セマンティクス**（RFC 9110 https://www.rfc-editor.org/rfc/rfc9110.html 、RFC 5789 https://www.rfc-editor.org/rfc/rfc5789.html 、2026-11-23 確認）：所有権競合・構造衝突・ドリフト・古い `ownerRevision` は対象リソースの現在状態との競合であるため `409 Conflict`、`Scope` 権限不足は `403 Forbidden` とする。
- **ライセンスとクリーンルーム境界**：kisage `v0.1.2` と LKAL `v0.2.0` は本プロジェクトと同一作者の Apache-2.0 成果物であり、他社製品のコードは参照せず Nayutar と kisage の形式仕様のみから実装する。
- **手元での確認**（2026-11-23）：
  - `lake build`（Lean `v4.35.0`）で `spec/Nayutar/Ownership.lean`・`spec/Nayutar/Drt/Ownership.lean`・`spec/Nayutar/Proofs/Ownership.lean`（16 定理、`sorry` なし）が警告なしで通り、`#print axioms` で許容公理（`propext`・`Classical.choice`・`Quot.sound`）のみに依存することを確かめた。
  - `nayutar-drt ownership` との DRT が既定の 1,000 件すべて一致し、43 個の到達バケット、全フィールド変異検出、およびわざと壊した 16 種の実装の検出を通過することを確かめた。
  - 埋め込みスキーマ（`ownership.schema.lkal`・`explain.schema.lkal`・`resolve.schema.lkal`・`error.schema.lkal`・`watch_event.schema.lkal`）の LKAL v0.2 検査、`core/resource/ownership`・`core/resource`・`core/wire` の単体・結合試験（`h2c` および TLS 1.3 `h2`、`-race` 付き、503/403 サニタイズ、不正トライ復号拒否、サイズ比率 ~14.9% 検証）、`golangci-lint`、および `govulncheck` がすべて通過することを確かめた。

## 検討した代替案

- **`EntranceStatus`（`status/**`）も `/own/` の葉パス所有権に含める**：コントローラ間の `status` 欄競合も同じ仕組みで扱える。しかしノードやコントローラの高頻度な状態報告のたびに `/own/` の復号・再符号化・書き込みが倍増し、`condition/<Type>/since` のサーバ自動付与とも干渉するため採らない。
- **`/own/` に各葉の `LastApplied` 値をそのまま保存する**：`Update` や `Patch` が `/own/` を通らなくてもドリフトを検出できる。しかし `/own/` のバイトサイズが本体 `o/` の 100% 以上に膨張し、[[0013]] の「本体の 20% 以下」および `force` なしの fail-closed 原則と両立しないため採らない。
- **`schema.Complete` 後の文書から宣言葉集合を抽出する**：パイプラインの段数は減る。しかし書き手が省略した任意欄まで `Created` 所有してしまい、他 Actor がその欄を設定できなくなる偽の `K201` 競合を生むため採らない。
- **`Prune` 後に空になった中間レコード `{}` をそのまま残す**：ファイル版 kisage の挙動に近い。しかし [[0028]] の順序付きレコード集合（要素レコードに必須の `order` 葉がある）で要素の全葉を `Prune` した際に `KindSchema` 検証エラーとなり削除不能になるため採らない。

## 影響とトレードオフ

- **得るもの**：本体 `o/` と watch ペイロードを一切肥大化させずに、単一 CAS `Txn` の上で kisage の fail-closed な葉パス所有権・保守的 `Prune`・明示的 `Adopt` / `Release` を多テナント API サーバと HTTP/2 ワイヤ層で一貫提供できる。
- **失うもの**：`EntranceSpec` の書き込みごとに `/own/` の読み取り・CAS 比較・必要に応じた書き込みが 1 キー分増える。また異なる Principal が同一オブジェクトの同じ葉を更新するときは、明示的な `Release` または `Adopt` が必要になる。
- **引き受けるリスク**：フィールド数が極端に少ない最小オブジェクト（100 バイト前後）に多数の Actor が個別葉を書き分ける縮退ケースでは `/own/` の比率が 20% を超える（代表的なワークロード宣言に対する G1 実測で継続監視する）。
- **今後の検証ゲート**：[[0018]] の決定 3（所有権 CAS 版の Lean–Go DRT 1,000 ケース以上、マージ後 10,000 ケース）および決定 5（CAS 版プロトコルモデルと Plan 安全性性質の `sorry` なし証明）を本 ADR の実装と保護定理で満たす。G1 で所有権サイズ比率と書き込み p99 遅延増分を実測検証する。

## 関連ADR

- [[0001]]：kisage の葉パス所有権モデルを動かさない前提として定める。
- [[0002]]：Go 実装と `spec/` の Lean 4 仕様（kisage 依存）を定める。
- [[0003]]：Lean 実行可能モデルとの DRT と保護定理による保証方針を定める。
- [[0004]]：複数キーの `mod_revision` CAS `Txn` を持つストア契約を定める。
- [[0006]]：`/t/<tenant>/o/...` と `/t/<tenant>/own/...` のキー分離を定める。
- [[0009]]：リソース封筒（`meta`・`spec`・`status`）を定める。
- [[0010]]：書き込み時の参照検査と逆参照索引の同一 `Txn` 更新を定める。
- [[0011]]：順序付きレコード集合を定める。
- [[0012]]：HTTP/2 上の LKAL-JSON ワイヤ形式と Patch の基本方針を定める。
- [[0013]]：本 ADR が実現する葉パス所有権と単一 CAS `Txn` 更新の決定を定める。
- [[0022]]：`FamilyOwnership` を含むキー空間文法とコーデックを定める。
- [[0025]]：封筒の 2 入口（`EntranceSpec` / `EntranceStatus`）と `Objects` を定める。
- [[0026]]：`refs` 検査・Cedar 認可・逆参照索引の `Txn` 構成を定める。
- [[0027]]：LKAL v0.2 スキーマ検証と `spec` の `default` 補完を定める。
- [[0028]]：順序付きレコード集合の双方向スキーマ検査を定める。
- [[0029]]：純粋 Patch 核と `core/wire` の HTTP/2 ハンドラ・エラー形式を定める。
