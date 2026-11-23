---
title: ワイヤ形式と Patch を、順序付きレコード集合の純粋 Patch 核・2 入口のストア結合・HTTP/2 LKAL-JSON ハンドラで実現する
status: proposed
date: 2026-11-22
depends-on: ["0001", "0003", "0006", "0007", "0009", "0010", "0011", "0012", "0022", "0023", "0025", "0026", "0027", "0028"]
---

# 0029: ワイヤ形式と Patch を、順序付きレコード集合の純粋 Patch 核・2 入口のストア結合・HTTP/2 LKAL-JSON ハンドラで実現する

## コンテキスト

[[0012]] は、G0〜G1 の外部ワイヤ形式を HTTP/2 上の LKAL-JSON とし、G2 からタグなし決定論的 CBOR を追加し、ワイヤ上の Patch を `/` 区切りパスに対する `.test` / `.set` / `.del` 操作の順序付きレコード集合（[[0011]]）として表す方針を定めた。これまでにキー空間とテナント登録簿（[[0006]]・[[0022]]）、watch キャッシュ（[[0007]]・[[0023]]）、リソース封筒の純粋核とストア結合（[[0009]]・[[0025]]）、参照検査と逆参照索引（[[0010]]・[[0026]]）、LKAL v0.2 スキーマと `spec` 既定値補完（[[0027]]）、順序付きレコード集合（[[0011]]・[[0028]]）が揃っているが、[[0012]] を動く実装と形式仕様に落とすには次の点を決める必要がある。

- **構文木編集（`(*lkal.File).Set` / `Delete`）とデータモデル不変性の乖離**：LKAL `v0.2.0` の `(*lkal.File).Delete` は構文木上のエントリ単位で動作するため、`a { b { c = 1 } }` から `a/b/c` を消すと `{ a = { b = {} } }` が残る一方、ラベル圧縮された `a "b" "c" = 1`（および空ファイルへの `Set("a/b/c", 1)` の結果）から `a/b/c` を消すとエントリごと消えて `{}` になる。構文木を介して Patch を適用すると、`lkal.Equal` で等しい 2 文書に対する結果の一致や操作列連結の結合律が成り立たない。
- **2 つの書き込み入口（[[0025]]）と `del` の衝突**：`envelope.Update` と `envelope.UpdateStatus` は、候補文書で省略された入口外の欄やサーバ管理欄を「変更しない（保存値を引き継ぐ）」と解釈する。そのため、保存文書にそのまま汎用 Patch を当てて `envelope.Update` に渡すと、`del "status"` や `del "meta/uid"` がエラーにならず黙って保存値に復元され、[[0025]] 決定 5 の「黙って無視はしない」原則に反する。また `meta/revision` はストア値に保存されないため、`.test` 操作の評価対象には現在の `mod_revision` を注入した封筒を使う必要がある。
- **`spec` 既定値補完（[[0027]]）との順序**：種別スキーマで `default` を持つ任意欄を `Patch` で `.del` したとき、親レコードが残っている場合の再補完の扱いと `generation` 増分条件を `Objects.Update` と一致させる必要がある。
- **HTTP/2 上のメディアタイプ・URL・一覧・Watch 配信・誤り応答**：リソース本体と Patch のメディアタイプ、一覧応答（LKAL の `L304` により配列にレコードを置けない）、`watchcache`（[[0023]]）の `EventPut` / `EventDelete` / `EventBookmark`（`InitialEventsEnd` を含む）および `ErrTooOld`（開始前の HTTP `410 Gone` と配信開始後の終端エラーイベント）のフレーミング、未登録テナントや未登録 kind によるキャッシュ無限生成 DoS の防止、ならびに内部実装詳細を漏らさない構造化 LKAL-JSON エラー応答を固定する必要がある。

本 ADR は [[0012]] の G0〜G1 範囲の実現方式を定める。G2 の LKAL-CBOR、クライアント認証・RBAC、`/x/` へのスキーマ保存、および葉パス所有権（[[0013]]）は本 ADR の範囲外とする。

## 決定

1. **Patch 文書のスキーマと符号化を `core/resource/patch` に置く。**
   - Patch 文書は `@lkal "0.1"` / LKAL-JSON 文書とし、閉じたルートに必須の `.map` 欄 `patch`（`unique-by = ["order"]`）のみを持つ（埋め込みスキーマ `core/resource/patch/patch.schema.lkal` で検証する）。空の操作列は `{"$lkal":"0.1","patch":{}}` とする。
   - `patch` の各要素は `orderedset`（[[0028]]）の最小キー文法（非空・`'/'` を含まない・先頭 `'$'` でない）を満たすラベルを持ち、閉じたレコードとして次の欄を持つ：
     - `order`：必須の `Int`（`orderedset.Entries` により狭義昇順で評価順を決める）。
     - `op`：必須の `Enum`（`.test`・`.set`・`.del`）。
     - `path`：必須の非空 `String`。`'/'` で分割したすべてのセグメントが非空・`'/'` を含まない・先頭 `'$'` でない文字列であること（`ValidSeg`）。
     - `value`：`op` が `.test` または `.set` のときは必須の任意の LKAL 値（9 型すべて可）、`op` が `.del` のときは不在でなければならない。
   - `ToRecord` / `FromRecord` で `[]Op` と `lkal.Record` を相互変換し（`ToRecord` はキー `"0", "1", ...` と `order = 10, 20, ...` を付与する）、`Encode` / `Decode` で空白なしの正準 LKAL-JSON バイト列と相互変換する。

2. **純粋なデータモデル上の `Apply` と `Diff` を `lkal.Record` 上で定義する。**
   - `(*lkal.File)` の構文木は経由せず、純粋な `lkal.Record` 木の上で定義する。
   - **`Apply(doc lkal.Record, ops []Op) (lkal.Record, error)`**：
     - 第 1 段（構造検査）：`doc` 全階層のキー一意性（`Value.unique`）と、すべての `op` の種別・`path` 文法・`value` の有無および `Value.unique` を検査し、違反があれば一切適用せず `malformed` を返す。
     - 第 2 段（左から右への逐次・原子的評価）：
       - `.test`：現在木の `path` の値が存在し `lkal.Equal` で `op.Value` と等しければ通過し、不在または不一致なら `conflict` で中断する。
       - `.set`：中間セグメントが不在なら新しい `Record` を作り、既存の `Record` なら辿り、スカラーや `List` に当たれば `malformed` で中断する。最終セグメントが既存ならその値を置き換え、不在なら追加する。
       - `.del`：中間セグメントが不在なら `notFound`、スカラーや `List` なら `malformed` で中断する。最終セグメントが親 `Record` に存在すればその欄だけを取り除き（結果として親が空レコード `{}` になっても親自体は保持する。[[0009]]）、存在しなければ `notFound` で中断する。
   - **`Diff(old, next lkal.Record) ([]Op, error)`**：
     - `old` と `next` の `Value.unique` およびトップレベル欄名の `ValidSeg` を検査し、違反があれば `malformed` を返す。
     - 各階層で欄名を辞書順に走査し、`old` にのみある欄に `.del` を、`next` に追加された欄に `.set` を出し、両方に存在して `!lkal.Equal` の欄は両方とも全キーが `ValidSeg` を満たす `Record` のとき再帰的に差分を取り、それ以外は `.set` で置き換える。これにより `Diff` の出力はレコードの欄記述順に対して決定論的かつ不変になり、`Apply(old, Diff(old, next))` は `next` と `lkal.Equal` で一致する。

3. **封筒の 2 入口に対応する純粋遷移 `ApplyEntrance`・`Patch`・`PatchStatus` を提供する。**
   - 入口は `EntranceSpec`（`meta/labels` と `spec`）および `EntranceStatus`（`status`）の 2 つとする。
   - `ApplyEntrance(entrance, stored, rev, ops)` は、(1) `envelope.CheckStored(stored)` と `rev >= 1` を検査し（違反は `internal`）、(2) `ops` の構造検査を行い（違反は `malformed`）、(3) `cur = envelope.WithRevision(stored, rev)` を初期値として各操作を順に評価する：
     - `.test` は `meta/revision`・`meta/uid`・`spec/...`・`status/...` を含む任意の有効パスを参照でき、不一致または不在で `conflict` を返す。
     - `.set` と `.del` は、先頭セグメントが `"meta"`・`"spec"`・`"status"` 以外なら `malformed` を返す。`EntranceSpec` では `meta/labels`（および `meta/labels/<key>`）と `spec`（および `spec/...`）のみを許可し、それ以外の `meta` パス（`meta` 自体・同一性・`uid`・`revision`・`generation`）および `status` パスへの書き込みを `forbidden` で拒否する。`EntranceStatus` では `status`（および `status/...`）のみを許可しつつ、`status/condition/<Type>/since`（およびその配下）への直接の `.set` / `.del` と `meta`・`spec` パスへの書き込みを `forbidden` で拒否する。
   - 純粋関数 `Patch(stored, rev, ops)` は `ApplyEntrance(EntranceSpec, ...)` の結果を `envelope.Update(stored, rev, patched)` に渡し、`PatchStatus(stored, rev, ops, now)` は `ApplyEntrance(EntranceStatus, ...)` の結果を `envelope.UpdateStatus(stored, rev, patched, now)` に渡す（条件レコード一括 `.set` による不正な `since` 指定は `forbidden`、`observedGeneration` の逆行・超過・削除は `observed` として拒否され、`generation` と `since` の更新規則は [[0025]] と完全に一致する）。
   - 誤り分類は `internal`・`malformed`・`conflict`・`notFound`・`forbidden`・`observed` の 6 種類とする。

4. **`core/resource` に `DecodeObject`・`Objects.List`・`Objects.Patch`・`Objects.PatchStatus` を追加する。**
   - `DecodeObject(obj, value, modRev)` は保存バイト列を復号し、`envelope.CheckStored` とキー同一性を検査したうえで `envelope.WithRevision` を付与する（不正値は `ErrCorrupt` で fail-closed に拒否する）。
   - `Objects.List(ctx, group, kind, opts)` は `o/<group>/<kind>/` を `Range` 走査し、各要素を `DecodeObject` で検証・復号して返す。
   - `Objects.Patch` と `Objects.PatchStatus` は `ops` を正準化したうえで保存オブジェクトを読み、`refs.DeleteDelta(old)` 検査 → `patch.ApplyEntrance` → `canonicalize` →（`Objects.Patch` のみ `ks.completeCandidate` で `spec` の宣言的 `default` を補完）→ `envelope.Update` / `UpdateStatus` → 種別スキーマ検証（`validateSpec` / `validateStatus`）→ `checkRefs`（Cedar 認可・存在確認・逆参照差分）を実行し、`lkal.Equal(next, old)` ならストア書き込みを省き、変化があるときだけ本体 `modRev` と追加参照先 `modRev` を比較する単一の CAS `Txn` で原子的に更新する。
   - これにより、種別スキーマで `default` を持つ任意の `spec` 欄を `.del` した場合、親レコードが残っていれば既定値が再補完され、親レコード自体を `.del` すれば親ごと削除される（[[0027]] の不変条件を維持する）。また、順序付きレコード集合（[[0028]]）の複数要素の `order` 入れ替えも、1 つの Patch 内の複数 `.set` の完了後に `unique-by = ["order"]` が検証されるため中間衝突なしに行える。

5. **HTTP/2 上の LKAL-JSON ワイヤ層（サーバハンドラとクライアント）を `core/wire` に置く。**
   - Go 1.27 標準ライブラリの `net/http`（`http.Protocols` の `SetHTTP2` / `SetUnencryptedHTTP2`、`http.HTTP2Config`、`http.ResponseController.Flush`）のみで構成し、外部パッケージ依存を追加しない。TLS 上の `h2` とループバック試験等向けの prior-knowledge `h2c` の双方に対応する。
   - **メディアタイプとヘッダ**：
     - リソース・一覧・Watch イベント・エラー応答：`application/vnd.nayutar.lkal+json`
     - Patch 要求：`application/vnd.nayutar.lkal-patch+json`
     - `Content-Type` 不一致は `415 Unsupported Media Type`、`Accept` 不一致（`application/cbor` のみの要求など）は `406 Not Acceptable`、許可外の HTTP メソッドは `405 Method Not Allowed`（`Allow` 付与）で拒否する。パッチ可能なルートおよびその `405` / `415` 応答には `Accept-Patch: application/vnd.nayutar.lkal-patch+json`（RFC 5789 §3.1）を付与する。
     - すべての応答に `X-Content-Type-Options: nosniff`、`Cache-Control: no-store`、`X-Frame-Options: DENY`、`Content-Security-Policy: default-src 'none'; frame-ancestors 'none'` を付与し、要求ボディには `MaxBodyBytes`（既定 1 MiB、超過時 `413 Content Too Large`）の上限を課す。
   - **URL ルートと操作**：キー空間（[[0006]]・[[0022]]）と同じ階層 `/t/{tenant}/o/{group}/{kind}[/{name}[/status]]` を使う（未知のクエリ引数はすべて `400 Bad Request` で拒否する）。
     - `POST /t/{tenant}/o/{group}/{kind}`：`Objects.Create`（URL の `{group}`・`{kind}` と候補の `meta` 同一性が一致しなければ `403 Forbidden`、成功時 `201 Created`）。
     - `GET /t/{tenant}/o/{group}/{kind}`（`watch` なし）：`watchcache.Manager` 経由の一貫読み取りまたはスナップショット一覧（`atRev`・`limit`・`continue`）。1 応答の件数は `MaxListLimit` を上限とし（`limit` 省略時は `MaxListLimit`、`Client.List` で `Limit == 0` のときは最初のページの `Revision` に固定して `Continue` を自動ページングする）、非空の `continue` は `watchcache` に触れる前に `keyspace.ParseRel` で検証する。応答は `list.schema.lkal` に従う `@lkal "0.1"` 文書（`revision`、任意の `continue`、および `<name>` をキーとし封筒を値とする `.map` の `items`）とする。
     - `GET /t/{tenant}/o/{group}/{kind}?watch=true`（任意の `fromRev`・`sendInitialEvents`・単一オブジェクト名フィルタ `name`）：`watchcache.Manager` 経由の Watch / WatchList ストリーム。HTTP/2 `DATA` フレーム上に 1 イベント 1 行の空白なし LKAL-JSON（NDJSON、`watch_event.schema.lkal` で検証：`type` は `.put` / `.delete` / `.bookmark` / `.error`）を逐次 Flush 送出する。開始時点で保持範囲外（`watchcache.ErrTooOld` / `store.ErrCompacted`）なら HTTP `410 Gone` を返し、配信開始後に `ErrTooOld` で終了した場合は終端イベント `type = .error`（`kind = .gone, status = 410`）を 1 行送出してストリームを閉じる。
     - `GET /t/{tenant}/o/{group}/{kind}/{name}`（任意の `atRev`）：`watchcache.Manager` 経由の単一オブジェクト取得（`200 OK`）。
     - `PUT /t/{tenant}/o/{group}/{kind}/{name}`：`Objects.Update`（`200 OK`）。
     - `PATCH /t/{tenant}/o/{group}/{kind}/{name}`：`Objects.Patch`（`200 OK`）。
     - `DELETE /t/{tenant}/o/{group}/{kind}/{name}`（任意の `rev`）：`Objects.Delete`（成功時 `204 No Content`）。
     - `PUT /t/{tenant}/o/{group}/{kind}/{name}/status`：`Objects.UpdateStatus`（`200 OK`）。
     - `PATCH /t/{tenant}/o/{group}/{kind}/{name}/status`：`Objects.PatchStatus`（`200 OK`）。
   - **キャッシュ生成 DoS の防止と認可フック**：`watchcache.Manager.Objects` を呼ぶ前に、`Schemas` に当該修飾つき kind が登録されていることと、`tenant.Registry.Get` で当該テナントが `.active` であることを検査し、未登録の kind や存在しない／削除中テナントに対するキャッシュゴルーチンの生成を防ぐ。`Config.Authenticate` フックが設定されている場合はリクエストから `refs.Principal` を取り出し、失敗時は `401 Unauthorized` で拒否する。
   - **構造化エラー応答（`error.schema.lkal`）**：エラー応答もすべて `application/vnd.nayutar.lkal+json` の LKAL-JSON（`error { kind = .<kind>, status = <code>, [path = "...",] message = "..." }`）に統一する。既存オブジェクトに対する Patch の対象パス不在（`patch.ErrNotFound`）は URL 自体の不在（`404`）と区別して `422 Unprocessable Content`（RFC 5789 §2.2）とし、`500 Internal Server Error` では内部ストレージキーや実装エラー文字列を外部へ出さない。

6. **純粋な Patch 核を Lean 4 モデルと DRT で照合し、10 個の性質を定理として証明する。**
   - `spec/Nayutar/Patch.lean` に `Nayutar.Envelope` と `Nayutar.OrderedSet` の型を再利用して同一の意味論を記述し、`nayutar-drt patch`（`spec/Nayutar/Drt/Patch.lean` と `core/drt/patch_drt_test.go`）で全操作（`apply`・`diff`・`toRecord`・`fromRecord`・`applyEntrance`・`patch`・`patchStatus`）の成否・誤り種別・結果値を照合する。
   - `spec/Nayutar/Proofs/Patch.lean` で次の 10 定理（`Nayutar.Patch` 名前空間、`sorry` なし、許容公理のみ）を証明する：
     - `apply_empty`：`Value.unique` を満たす文書への空 Patch 適用は元の文書をそのまま返す。
     - `apply_preserves_unique`：`apply` が受理した結果は `Value.unique` を満たす。
     - `apply_append`：構造的に妥当な 2 つの操作列 `ops₁`・`ops₂` に対し、`apply doc (ops₁ ++ ops₂)` は `ops₁` 適用後に `ops₂` を適用する逐次合成と一致する。
     - `apply_eqv_invariant`：`Value.unique` を満たし `Value.eqv` で等しい 2 文書に対し、`apply` の失敗時の誤り種別は一致し、成功時は `Value.eqv` で等しい結果を返す。
     - `diff_refl`：`Value.unique` とトップレベルキー文法を満たす文書 `doc` に対し、`diff doc doc = .ok []`。
     - `diff_apply_roundtrip`：`diff old next = .ok ops` ならば、`apply old ops` は成功し、`Value.unique` を満たしかつ `next` と `Value.eqv` で等しい文書を返す。
     - `toRecord_fromRecord_roundtrip`：`toRecord ops = .ok rec` ならば、`rec` は `Value.unique` を満たし、`fromRecord rec = .ok ops` が成り立つ。
     - `patch_is_stored`：`patch old rev ops = .ok next` ならば、`next` は `Envelope.stored` を満たし、`meta/uid` と `status` を保存文書から保つ。
     - `patch_bumps_generation_iff_spec_changes`：`patch old rev ops = .ok next` ならば、`meta/generation` は `spec` が `optEqv` で変化したときかつそのときに限り 1 増える。
     - `patchStatus_is_stored`：`patchStatus old rev ops now = .ok next` ならば、`next` は `Envelope.stored` を満たし、`meta` と `spec` を保存文書から保つ。

## 根拠（調査結果・出典）

- **内部不変条件（LKAL `v0.2.0` のデータモデル・`FromJSON`・`path.go`）**（`github.com/Kaikei-e/LKAL` v0.2.0 の `docs/PRINCIPLE.md`・`json.go`・`path.go`・`schema/complete.go`、2026-11-22 確認）：
  - `FromJSON` は配列内の非スカラー要素を `L304` で拒否するため、Patch 操作列と一覧応答の `items` をそれぞれ `order` 葉つき `.map`（[[0028]]）と `<name>` キーつき `.map` で表すことで、すべてのワイヤペイロードが `@lkal "0.1"` および LKAL v0.2 スキーマの閉じた集合に収まる。
  - `(*lkal.File).Delete` は構文木上のラベル圧縮エントリ構造に依存するため、データモデル `lkal.Record` 上で `.set` / `.del` を直接定義することが `apply_eqv_invariant` と `apply_append` の成立要件である。
  - `(*schema.Schema).Complete` は親 `Record` が存在するときに限り欠落した任意欄へ `default` を注入し、不在の親 `Record` は生成しない。決定 4 により、`Objects.Update` と `Objects.Patch` で保存後の既定値不変条件が一致する。
- **内部不変条件（[[0023]]・[[0025]]・[[0026]]・[[0028]] との整合）**：
  - [[0023]]：`watchcache.Cache` は閉じたリビジョンのみをスナップショット・ブックマークに使い、保持範囲超過や再 list 時に `ErrTooOld` を返す。決定 5 の開始前 `410 Gone` と配信中 `.error`（`kind = .gone`）はこの契約を HTTP/2 ストリームへ 1 対 1 に写す。また `EventDelete` は値を運ばず相対キーとリビジョンのみを運ぶため、Watch の削除イベントはオブジェクト識別子とリビジョンを送出する。
  - [[0025]]：入口外の欄への `.set` / `.del` を `ApplyEntrance` の段階で `forbidden` として拒否することで、`envelope.Update` / `UpdateStatus` の「候補で不在の欄は保存値を引き継ぐ」規則によって削除操作が黙って握り潰される不具合を防ぐ。
  - [[0026]]・[[0028]]：Patch パスのセグメント文法 `ValidSeg`（非空・`'/'` なし・先頭 `'$'` なし）は `refs` の葉パスセグメントおよび `orderedset` の要素キー文法と一致する。
- **HTTP/2・HTTP PATCH・メディアタイプ仕様**（RFC 9110 https://www.rfc-editor.org/rfc/rfc9110.html 、RFC 9113 https://www.rfc-editor.org/rfc/rfc9113.html 、RFC 5789 https://www.rfc-editor.org/rfc/rfc5789.html 、RFC 6838 https://www.rfc-editor.org/rfc/rfc6838.html 、RFC 6839 https://www.rfc-editor.org/rfc/rfc6839.html 、2026-11-22 確認）：
  - RFC 9113 §3.1・§3.3・§8.2.2 により、HTTP/1.1 の `Upgrade: h2c` は非推奨であり平文 HTTP/2 は prior knowledge を用い、`Transfer-Encoding: chunked` 等の接続固有ヘッダは生成してはならず HTTP/2 `DATA` フレームを直接 Flush する。Go 1.27 標準 `net/http` の `Protocols`（`SetHTTP2` / `SetUnencryptedHTTP2`）はこれらを標準ライブラリだけで提供する（クライアント `Transport.Protocols` で `http://` の prior-knowledge `h2c` を使う際は `HTTP1` を無効化する）。
  - RFC 5789 §2・§2.2・§3.1 により、`PATCH` は全体を原子的に適用しなければならず、`Accept-Patch` 応答ヘッダで対応 Patch メディアタイプを広告し、状態競合には `409 Conflict`、非対応メディアタイプには `415 Unsupported Media Type`、構文は妥当だが現在状態に適用不能な操作（存在しない欄への `.del` 等）には `422 Unprocessable Content` を返す。
  - RFC 6838 §3.2 および RFC 6839 §3.1 により、ベンダーツリーと `+json` 構造化サフィックスを用いた `application/vnd.nayutar.lkal+json` および `application/vnd.nayutar.lkal-patch+json` を採る（将来の G2 CBOR では RFC 8949 §9.5 の `+cbor`・RFC 8742 https://www.rfc-editor.org/rfc/rfc8742.html の `+cbor-seq` を対にできる）。
- **JSON Patch / Merge Patch および Kubernetes Watch・CBOR との比較**（RFC 6902 https://www.rfc-editor.org/rfc/rfc6902.html 、RFC 7396 https://www.rfc-editor.org/rfc/rfc7396.html 、RFC 7464 https://www.rfc-editor.org/rfc/rfc7464.html 、RFC 9457 https://www.rfc-editor.org/rfc/rfc9457.html 、Kubernetes API Concepts https://kubernetes.io/docs/reference/using-api/api-concepts/ 、KEP-4222 https://github.com/kubernetes/enhancements/issues/4222 、2026-11-22 確認）：
  - RFC 6902 §4.6 の `test` は `1` と `1.0` を数値的に等価とみなすが、本 ADR の `.test` は `lkal.Equal` を使うため `Int` と `Decimal` を厳密に区別する。
  - LKAL-JSON の正準出力（`core/resource.Encode` 等）は改行を含まない単一行 JSON であるため、Watch ストリームは `0x1E` 前置の RFC 7464 ではなく 1 イベント 1 行の改行区切り LKAL-JSON（NDJSON）で一意に境界が決まる。
- **ライセンスとクリーンルーム境界**：LKAL `v0.2.0` と kisage `v0.1.2` は本プロジェクトと同一作者の Apache-2.0 モジュールであり、Go 標準ライブラリは BSD-3-Clause である。IETF 各 RFC と Kubernetes 文書はプロトコル規定および比較対象の仕様確認にのみ参照し、`core/resource/patch`・`core/wire`・`spec/Nayutar/Patch.lean` はすべて Nayutar の内部不変条件から独立に実装する。
- **手元での確認**（2026-11-22）：
  - `lake build` で `spec/Nayutar/Patch.lean`・`spec/Nayutar/Drt/Patch.lean`・`spec/Nayutar/Proofs/Patch.lean`（10 定理、`sorry` なし）が警告なしで通り、`#print axioms` で許容公理（`propext`・`Classical.choice`・`Quot.sound`）のみに依存すること、ならびに `spec/Nayutar/Statements/Patch.lean` と `spec/comparator.json` への 10 定理の追加後に `check-protected-theorems` と `lake comparator`（全 53 定理受理）が通過することを確かめた。
  - `nayutar-drt patch` との DRT が既定の 1,000 件および 5,000 件すべて一致し、到達可能な全 29 個の `(操作, 結果)` バケット、結果フィールドの変異検出、およびわざと壊した 17 種の実装の検出を通過し、全 9 スイートの DRT が通過することを確かめた。
  - 埋め込みの 4 スキーマ（`patch.schema.lkal`・`error.schema.lkal`・`list.schema.lkal`・`watch_event.schema.lkal`）に対する `lkal check`・`lkal fmt`・`lkal schema check`、`core/resource/patch`・`core/resource`・`core/wire` の単体試験・結合試験（`h2c` および TLS 1.3 `h2` で `ProtoMajor == 2`、Watch / WatchList と開始前 `410 Gone`・配信中 `.gone`、Cedar 認可、`-race` 付き）・ファジング、および `zizmor --pedantic --strict-collection --offline .` がすべて通過することを確かめた（手元環境に `etcd` バイナリが無いため、`-tags etcd` は `go vet` と静的検査まで確認し、3 メンバー etcd 上の実行は CI の `store-etcd` ジョブに委ねる）。

## 検討した代替案

- **`(*lkal.File).Set` / `Delete` をそのまま呼んで Patch を実装する**：コード量は少ない。しかし構文木上のラベル圧縮の有無によって `.del` 後に空親レコードが残るかどうかが変わり、`lkal.Equal` 不変性（`apply_eqv_invariant`）と操作列連結（`apply_append`）が壊れるため採らない。
- **HTTP `PATCH` だけ `"$lkal":"0.1"` を持たない独自 JSON 配列 `{"patch":[...]}` にする**：配列で順序を表せる。しかし `.lkal` 文書や LKAL v0.2 スキーマで検証できず、`value` 欄の復号のために手書きの JSON 分解層を増やすことになり `AGENTS.md` の方針に反するため採らない。
- **不在パスへの `.del` をエラーにせず NoOp にする**：冪等にはなる。しかしタイポによるパス誤り（`spec/contanier/sidecar` など）が黙って成功してしまい、LKAL の「推測しない」原則（`L402`）と食い違うため採らない。
- **エラー応答に RFC 9457 `application/problem+json`（plain JSON）を使う**：一般的な HTTP クライアントには馴染みがある。しかしクライアントが成功時と失敗時で 2 系統の JSON パーサ・スキーマを持つことになり、LKAL-JSON で統一する利点が薄れるため採らない。
- **Watch ストリームに RFC 7464 `application/json-seq`（`0x1E` 区切り）を使う**：複数行 JSON を混在させられる。しかし正準 LKAL-JSON は常に単一行であり、`0x1E` 制御文字を扱わない標準的な行単位リーダでのデバッグ性を損なうため NDJSON を採る。
- **`golang.org/x/net/http2` を直接依存に追加して `h2c` を構成する**：Go 1.24 以前の定番構成である。しかし Go 1.27 標準 `net/http.Protocols` が `h2` と `h2c` をネイティブに備えており、外部依存を増やす理由が無いため採らない。

## 影響とトレードオフ

- **得るもの**：
  - リソース・Patch・一覧・Watch イベント・エラー応答のすべてが `@lkal "0.1"` / LKAL-JSON と v0.2 スキーマに統一され、単一のデコーダで扱える。
  - `Apply` と `Diff` の往復性、`lkal.Equal` 不変性、操作連結、および 2 入口の境界と `generation` 更新規則が Lean 4 の 10 定理と DRT で機械的に保証される。
  - Go 標準ライブラリだけで HTTP/2（`h2` / `h2c`）上の CRUD・2 入口 Patch・Watch / WatchList ストリームと `410 Gone` 応答が完結する。
- **失うもの**：
  - Patch パスは `'/'` を区切り文字として扱うため、欄名自体に `'/'` や空文字列を含むレコード要素（`meta/labels` 内のスラッシュ入りキーなど）は個別葉として指定できず、親レコード `meta/labels` 単位での `.set` が必要になる。
  - G0〜G1 では CBOR を受理せず `Accept: application/cbor` 単独の要求は `406 Not Acceptable` になる。
- **引き受けるリスク**：
  - トランスポート層のクライアント認証・TLS 証明書運用・レート制限は別 ADR まで未実装であり、`core/wire` を信頼できないネットワークへ直接公開してはならない（テストはループバック `127.0.0.1` のみにバインドする）。
  - [[0013]]（葉パス所有権）を実装する際、Patch の `.set` / `.del` が触れた葉パス集合から所有権検査・更新を導出する規則を [[0013]] の実現 ADR で結合する必要がある。
- **今後の検証ゲート**：[[0012]] が G0 に置いた「LKAL-JSON と Patch 適用の往復性」を、本 ADR の Lean 10 定理・`nayutar-drt patch`・ファジング、および `core/resource` と `core/wire` の結合試験（インメモリ・3 メンバー etcd、`h2` / `h2c`、`-race` 付き）で満たす。G2 で LKAL 側に CBOR 仕様（`Decimal` の無損失表現を含む）を導入した際、`core/wire` のコンテンツネゴシエーションに `+cbor` / `+cbor-seq` を追加する。

## 関連ADR

- [[0001]]：宣言基盤を LKAL とし、ワイヤでの JSON 利用を認める前提を定める。
- [[0003]]：純粋な核を Lean 4 実行可能モデルとの DRT と保護定理で保証する方針を定める。
- [[0006]]：テナントを最上位に置くキー空間（`/t/<tenant>/o/<group>/<kind>/<name>`）を定める。HTTP/2 の URL 階層はこれに合わせる。
- [[0007]]：テナント別 watch キャッシュ・ブックマーク・watch list・`410 Gone` の方針を定める。
- [[0009]]：リソース封筒（`meta`・`spec`・`status`）と「`null` なし・最後の要素削除で `{}` が残る」規約を定める。
- [[0010]]：書き込み時の `Ref` 検査を定める。`Objects.Patch` はこれを通す。
- [[0011]]：順序付きレコード集合の `order` 葉規約を定める。Patch 操作列はこれを使う。
- [[0012]]：本 ADR が実現するワイヤ形式と Patch の決定を定める。
- [[0013]]：後続で `Objects.Patch` の `.set` / `.del` と結合する葉パス所有権モデルを定める提案。
- [[0022]]：キー空間の厳格なセグメント文法・コーデック・フェンスつきテナント登録簿を定める。
- [[0023]]：`watchcache.Cache` と `Manager`、`ErrTooOld`、`InitialEventsEnd` を定める。
- [[0025]]：封筒の純粋な核 `core/resource/envelope`（2 入口と誤り順序）および `core/resource.Objects` を定める。
- [[0026]]：参照検査の純粋な核 `core/resource/refs`、Cedar 認可、逆参照索引の同一 `Txn` 更新を定める。
- [[0027]]：LKAL v0.2 スキーマによる検証と `spec` の `default` 補完を定める。
- [[0028]]：順序付きレコード集合の純粋な核 `core/resource/orderedset` を定める。
