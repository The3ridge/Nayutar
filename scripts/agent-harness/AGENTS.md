# scripts/agent-harness — エージェントハーネス

`scripts/agent-harness/` で作業するときの指示。共通の指示はリポジトリのルートの `AGENTS.md` にあり、ここには繰り返さない。パスはリポジトリのルートからの相対で書いてある。

Claude Code（`.claude/settings.json`）と Codex（`.codex/hooks.json`）に同じガードを配線している。本体は `scripts/agent-harness/`。

- **保護パス**（`guard_protected_paths.py`）：シンボリックリンクの実体も解決したうえで、`.git/` の内部、`.env`（`.example` / `.template` / `.sample` を除く）、`*.key` / `*.pem`、`secrets/`、`spec/comparator.json`、`spec/Nayutar/Statements.lean`、`spec/Nayutar/Statements/`、`spec/Nayutar/Fixture.lean`、`spec/Nayutar/Fixture/`、`spec/lakefile.toml`、`spec/lakefile.lean`（Lake は両方あるとき `lakefile.lean` を優先する）、`spec/lake-manifest.json`、`spec/lean-toolchain`、kisage の信頼境界（ホスト設定と、そこに書かれたストア・ロック・反映先）、およびリポジトリ内の `.yaml` / `.yml`（`docdag.yaml`、`.github/dependabot.yml`、`.github/workflows/*.{yml,yaml}` を除く）への編集を止める。フィクスチャのモデルと Lean のビルド設定を保護し、Lean と DRT のワークフローを変更のたびに走らせる決定は ADR 0021（`proposed`）に記録している。
- **push 系・検証回避コマンド**（`guard_push_commands.py`）：`git push`、`git remote add|set-url|update`、`gh pr create|merge`、`gh workflow run`、`gh run rerun|cancel`、`gh release create|upload`、`gh repo sync`（必要ならユーザーに実行してもらう）に加え、フック・署名の回避（`git commit --no-verify` / `-n` / `--no-gpg-sign`、`core.hooksPath`、`commit.gpgsign=false`）および `lake comparator --inadvisably-no-sandbox`、`lake update` と `lake` の `--update` オプション（どちらも `spec/lake-manifest.json` を書き換える）を拒否する。
- **編集後の検査**（`post_edit_checks.py`）：`.lkal` は `lkal fmt -w` と `lkal check` を自動でかける。`HEAD` で確定済みの ADR の既存行を書き換えると `docdag validate --immutable-since HEAD` で止める（ファイル末尾への追記のみ可）。
- セッション開始時に `harness_selfcheck.py` が配線を確かめる。`agent_harness_selfcheck_FAILED` が出たら、作業の前に直す。
- 自己検査は指示ファイルの構成も検査する。ルートの `AGENTS.md` は 16,384 バイト・200 行まで、ルートから各ディレクトリまでの `AGENTS.md` の合計は 32,768 バイトまで（Codex は起動したディレクトリまでの `AGENTS.md` を連結し、合計が 32 KiB に達すると残りを読み込まない。ルートを半分に抑えるのは、利用者のグローバルな指示と入れ子のファイルの余地を残すためである）。入れ子の `AGENTS.md` は、ルートの表に載っていて、隣に `@AGENTS.md` を取り込む `CLAUDE.md` がなければならない（Claude Code は、ルートに `CLAUDE.md` があると入れ子の `AGENTS.md` を自分では読まない）。
- シェル経由の保護対象書き換え（リダイレクトや `sed -i`、`perl -i`、`tee`、`cp`、`mv`、`rm`、`python -c` など）も `guard_push_commands.py` が拒否するが、シェルの検査はヒューリスティックな多層防御にすぎない。間接実行や難読化などで保護対象のガードを迂回してはならない。
- Codex は追跡済みの `.codex/hooks.json` を、初回に `/hooks` でユーザーが確認・承認してから使う。
- ハーネスを変えたら `scripts/tests/test-agent-harness.py` にテストを足す。
