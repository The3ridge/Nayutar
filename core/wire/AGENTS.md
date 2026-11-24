# core/wire — HTTP/2 ワイヤ層

`core/wire/` で作業するときの指示。共通の指示はリポジトリのルートの `AGENTS.md` にあり、ここには繰り返さない。パスはリポジトリのルートからの相対で書いてある。

- 決定は ADR 0012・ADR 0029 および葉パス所有権サブリソースの ADR 0013・ADR 0030（`proposed`）。Go 標準ライブラリの `net/http`（`http.Protocols` の `SetHTTP2` / `SetUnencryptedHTTP2`、`http.HTTP2Config`、`http.ResponseController.Flush`）と `crypto/tls` のみで構成し、`golang.org/x/net/http2` などの外部依存を追加しない。
- ペイロードはすべて空白なしの単一行 `@lkal "0.1"` LKAL-JSON で、先頭メンバーは `"$lkal":"0.1"`。エラー応答・一覧応答・Watch イベント・Explain 応答・Adopt/Release 要求は埋め込みスキーマ（`error.schema.lkal`・`list.schema.lkal`・`watch_event.schema.lkal`・`explain.schema.lkal`・`resolve.schema.lkal`）で検証し、`WatchEvent` は `EncodeWatchEvent` / `DecodeWatchEvent` の双方で種別ごとの相互排他不変条件を強制する。
- メディアタイプはリソース・一覧・Watch イベント・エラー応答が `application/vnd.nayutar.lkal+json`、Patch が `application/vnd.nayutar.lkal-patch+json`（`Content-Type` は省略可能な `charset=utf-8` のみ許可）。パッチ可能なルート（オブジェクトと `/status`）では `405` / `415` を含む全応答に `Accept-Patch` を付与する。所有権サブリソース（`PUT .../apply`・`POST .../prune`・`GET .../explain`・`POST .../adopt`・`POST .../release`）は `Nayutar-Owner-Revision` ヘッダを返し、構造化エラーに任意の `code`（`K101`–`K604`）と順序付きレコード集合 `diagnostics` を載せる（`K101`/`K102`→`400`、`K401`→`403`、`K201`–`K206`→`409`）。全応答にセキュリティヘッダを付与し、未知クエリ引数はすべて `400`、`500` は `"internal server error"` に統一する。
- `watchcache.Manager.Objects` を呼ぶ前に、URL セグメント文法・クエリ引数（`continue` の `keyspace.ParseRel` を含む）・`Schemas` の登録・`tenant.Registry.Get` の `.active` 状態を検査し、未登録 kind や存在しない／削除中テナントに対するキャッシュ無限生成を防ぐ。サーバ側の `GET` 一覧応答は常に 1 頁上限 `MaxListLimit` で有界にし（`?limit=0` は `400`）、`Client.List` は `Limit == 0` のときに単一スナップショットリビジョン固定で `Continue` を自動走査する。
- Watch ストリームは NDJSON（`\n` 区切り、1 イベントごと Flush、`Transfer-Encoding` は付けない）で配信し、開始前の `ErrTooOld` / `ErrCompacted` は HTTP `410 Gone`、配信開始後の追い越し・コンパクションは終端 `type = .error`（`kind = .gone, status = 410`）を送出して閉じる。`name` フィルタは `o/<group>/<kind>/<name>` の完全一致で `EventPut` / `EventDelete` を絞り込み、`EventBookmark` はすべて通す。
- Go 1.27 の `http.Transport` で `http://` の prior-knowledge `h2c` を使うときは `Protocols` で `SetUnencryptedHTTP2(true)` のみを有効化する（`HTTP1` を無効のままにする）。
- 試験のリスナーは必ず `127.0.0.1:0` のみにバインドし、`0.0.0.0` にバインドしない。`h2c` と TLS 1.3 `h2` の双方で `ProtoMajor == 2` を確かめ、3 メンバーの etcd 結合試験はビルドタグ `etcd`（`wire_etcd_test.go`）で走らせる。
- まだ無いもの：G2 のタグなし決定論的 LKAL-CBOR（`+cbor` / `+cbor-seq`）、クライアント認証本体とプリンシパル別レート制限（`Config.Authenticate` と `Config.ScopeResolver` フックのみ用意）。
