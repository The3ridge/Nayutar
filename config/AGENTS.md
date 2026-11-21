# config — 開発環境の宣言と kisage（MCP）

`config/` で作業するときの指示。共通の指示はリポジトリのルートの `AGENTS.md` にあり、ここには繰り返さない。パスはリポジトリのルートからの相対で書いてある。

開発環境の設定は kisage で照合する。宣言は追跡対象の `config/dev.lkal`、反映先はリポジトリ外のローカル状態ファイル。対応づけと権限はホスト設定が決める。kisage は環境変数 `KISAGE_HOST_CONFIG` が指すファイルを読み、未設定なら `/etc/kisage/host.json` を読む。このリポジトリのホスト設定は `~/.config/kisage/nayutar.json` で、MCP サーバの登録がそのパスを渡している（ハーネスのガードも、環境変数が無いときはこのパスを保護する）。シェルから `kisage` を直接使うには、ユーザーがそのシェルで `KISAGE_HOST_CONFIG` を設定しておく必要がある。未設定だと `K101: no such file or directory` と終了コード 1 で失敗する（無いのは `/etc/kisage` で、宣言ファイルではない）。

`config/dev.lkal` の `etcd` は開発用クラスタ（版と 3 メンバーの URL）を宣言し、対になる `config/dev.schema.lkal`（`config` パッケージが埋め込む LKAL v0.2 スキーマ。ADR 0027）が構造を定める。`go run ./cmd/dev-etcd` は外部の `lkal` コマンドを呼ばず、`lkal.Parse` と `config.DevSchema().Validate` で宣言を検査したうえで同じ版の etcd を 3 メンバーで起動する（版が違えば起動しない。メンバーの URL がループバックの IP リテラルでなければ拒否する。平文で認証が無いクラスタだからである。データの既定の置き場所は `$XDG_STATE_HOME/nayutar/dev-etcd`）。

- MCP サーバ `kisage` のツール：`kisage_plan` / `kisage_explain` / `kisage_apply` / `kisage_prune` / `kisage_adopt` / `kisage_release`。
- 変更の前に `kisage_plan` で差分を確認し、`kisage_apply` する。削除は `kisage_prune`（自分が作った葉だけ消える）。
- Scope は `Set` / `Own` / `Delete`。`Resolve` は与えていないので、ドリフトや所有権の衝突（`adopt` / `release`）はユーザーに渡す。`--expect` と `--owner-revision` は `kisage_explain` の現在の出力から取る。
- ホスト設定・`KISAGE_HOST_CONFIG`・ストアは信頼境界である。エージェントは編集しない。`KISAGE_HOST_CONFIG` を自分で設定してコマンドを実行することもしない。
- kisage 0.1.2 の MCP サーバは、`_meta` つきの `tools/call` を `unknown field "_meta"` で拒否する。Claude Code 2.1.327 はこれを付けるので、kisage のツールを呼べない。直るまで、`plan` と `apply` はユーザーが CLI で行う。
- 仕様は `../kisage/docs/PRINCIPLE.md`、`../kisage/docs/ADR/`、`../kisage/docs/cli.md`。
