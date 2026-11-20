---
paths:
  - ".github/workflows/**"
---

# CI ワークフローの編集

CI は検証ゲートの判定そのものなので、緑が「検査が走って通った」以外の意味を持ってはならない。
Alt では、検査の失敗を握りつぶすステップ（provider 検証や脆弱性スキャンの `|| true`、失敗時に
空の SARIF を書く処理）が「検出 0」「green」のまま数か月通っていた。

## してはいけないこと

- **検査・テスト・スキャンの終了コードを握りつぶさない。** `|| true`、`|| :`、`|| echo`、
  `|| exit 0`、`continue-on-error: true` は、報告専用のステップに限り、直前の行に
  `# swallow-ok: <理由>` を書いて使う。理由が書けないなら握りつぶさない。
- **失敗時に空の結果ファイルを書いて後続を通さない。** 空の SARIF や空の JSON は「検出 0」に見える。
- **対象が 0 件のときに黙って成功しない。** 0 件が正常なら、そのことをログに出す
  （`lkal` ジョブの `no .lkal files` のように）。
- **Lean と DRT のワークフロー、および `ci.yml` に、`if:`（ジョブ単位・ステップ単位とも）や、`push` / `pull_request` の `paths:` / `paths-ignore:` / `types:` フィルタを付けない。** トリガーのブランチは `main` だけにする。必須チェックにするため、毎回走って結果を返す必要がある（ADR 0021）。`core/ci_test.go` が検査する。他のワークフローでも、そのワークフロー自身と検査対象のスクリプトをフィルタで外さない。

## 守る形

既存の `ci.yml` に合わせる。

- top-level の `permissions: {}` と、ジョブごとの最小権限
- `uses:` は 40 桁のコミット SHA で固定し、行末にタグをコメントで書く
- `actions/checkout` には `persist-credentials: false`
- ジョブごとに `timeout-minutes`
- ダウンロードするバイナリは版と SHA-256 を固定して検証する

## セルフチェック

```bash
zizmor --pedantic --strict-collection --offline .
actionlint
```
