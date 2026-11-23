# AGENTS.md

Nayutar で作業するコーディングエージェント（Claude Code・Codex ほか）への共通指示。Claude Code 固有の補足は [CLAUDE.md](CLAUDE.md) にある。

このファイルは地図である。どの作業にも要ることだけをここに置き、領域ごとの詳細はそのディレクトリの `AGENTS.md` に置く（[領域ごとの指示](#領域ごとの指示)）。

## Nayutar とは

個人と AI コーディングエージェントで **Kubernetes の本格的な代替** となるオーケストレータを作るプロジェクト。宣言の基盤に [LKAL](https://github.com/Kaikei-e/LKAL)、所有権と apply の中核に [kisage](https://github.com/Kaikei-e/kisage) の葉パス所有権モデルを使う。

- 動かさない前提：[ADR 0001](docs/ADR/0001-aim-for-a-full-kubernetes-replacement-on-fixed-premises.md)。設計判断はこれに反してはならない。前提を変えるときは 0001 を `supersedes` する ADR を書く。
- 現状：中核は Go モジュール（言語版の下限は 1.27.3、toolchain は固定しない）、仕様は [`spec/`](spec/) の独立した Lean 4 パッケージ（ADR 0002）。次のものは実装済みである。決定の ADR はどれも `accepted`、矢印の先は実現方式の ADR で `proposed` である。
  - DRT の枠組み（`core/drt` と Lean の実行ファイル `nayutar-drt`）と定理の主張を守る仕組み：ADR 0017
  - ストア契約（`core/store`）とそのインメモリ参照実装・etcd アダプタ・Lean モデル・DRT・線形化可能性検査：ADR 0004。契約は固有の Lean 定理を持たず、Lean モデルを DRT のオラクルとして使う。
  - G0 検証ゲートの合格条件：ADR 0018
  - G0〜G1 を単一の etcd v3.6 クラスタ（3 台）で運用する：ADR 0005 → ADR 0020（ADR 0019 を置き換える）
  - テナントを最上位に置くキー空間：ADR 0006 → ADR 0022
  - watch キャッシュ：ADR 0007 → ADR 0023
  - ノードの死活監視：ADR 0008 → ADR 0024
  - リソースの封筒：ADR 0009 → ADR 0025
  - 参照の検査と逆参照索引：ADR 0010 → ADR 0026（`Binding` の identityHash つきの解決とコントローラの読み取りの制限は、Export と `Binding` の ADR に残してある）
  - LKAL v0.2 スキーマによる宣言・内部記録・リソース kind・参照規則の検査と既定値補完：ADR 0027（`/x/<identityHash>/schema/<version>` への保存は Export と `Binding` の ADR に残してある）
  - 順序付きレコード集合（`core/resource/orderedset` と `CompileKindSchema` の双方向検査）：ADR 0011 → ADR 0028
  - HTTP/2 LKAL-JSON ワイヤ層（`core/wire`）と 2 入口 Patch / Diff（`core/resource/patch` と `Objects.Patch` / `PatchStatus`）：ADR 0012 → ADR 0029（タグなし決定論的 CBOR は G2 に残してある）
- 所有権のモデル本体、Export と `Binding`、ESR の証明、Kubernetes との差分試験は後続 ADR の範囲であり、まだ実装していない。
- コードを書き始める前に、その決定を ADR として起票する。

## 領域ごとの指示

次のディレクトリには、その領域だけの指示を書いた `AGENTS.md` がある。**その領域のファイルを読むか変える前に、対応する `AGENTS.md` を読む。** 上位のディレクトリのものも読む（`core/store/watchcache/` なら `core/store/AGENTS.md` も）。

| 領域 | 指示 | 内容 |
| --- | --- | --- |
| Lean 仕様 | `spec/AGENTS.md` | ユーザーが所有するファイル、証明の置き場所、モデルの変更の扱い、`lake comparator` |
| Lean–Go の差分ランダム試験 | `core/drt/AGENTS.md` | 実行のしかた、スイート、対になる Go と Lean の表 |
| ストア契約 | `core/store/AGENTS.md` | `Store` の契約、etcd の試験と障害の注入、テナントのビューと登録簿、キーの文法 |
| watch キャッシュ | `core/store/watchcache/AGENTS.md` | 3 層の構成、閉じたリビジョン、保持とコンパクション、4 不変条件 |
| ノードの死活監視 | `core/liveness/AGENTS.md` | 判定の規則、ノードの記録、退避の許可、シミュレーション |
| リソースの封筒 | `core/resource/AGENTS.md` | 封筒の形、2 つの書き込みの入口、更新規則、ストア結合 |
| HTTP/2 ワイヤ層 | `core/wire/AGENTS.md` | HTTP/2 LKAL-JSON ハンドラ・スキーマ・クライアント・`410 Gone`・DoS 防止 |
| 開発環境の宣言と kisage | `config/AGENTS.md` | `config/dev.lkal`、開発用 etcd、kisage の使い方と信頼境界 |
| エージェントハーネス | `scripts/agent-harness/AGENTS.md` | ガードが止めるものの一覧、編集後の検査、自己検査 |

- `.github/workflows/` を変えるときは `.claude/rules/ci-workflows.md` を読む（Claude Code は該当のファイルを開くと読み込む。Codex は自分で開く）。
- 読み込まれ方はツールで違う。Claude Code は、各ディレクトリの `CLAUDE.md`（中身は `@AGENTS.md` の 1 行）を、その領域のファイルを開いたときに読み込む。Codex は、起動したディレクトリまでの `AGENTS.md` しか読み込まないので、ルートから起動したときは上の表のファイルを自分で開く。
- 領域を足すときは、`AGENTS.md` と 1 行の `CLAUDE.md` をそのディレクトリに置き、上の表に行を足す。自己検査（`harness_selfcheck.py`）が、表とファイルの対応、`CLAUDE.md` の取り込み、大きさの上限を検査する。
- ここに書くのは、コードを読んでも分からないことと、例外の無い約束である。コードや ADR を読めば分かることは書かず、場所だけを示す。

## 前提から来る作業ルール

- **人間が書く設定・宣言は LKAL（`.lkal`）で書く。YAML は書かない。** YAML は取り込み入力としてだけ扱う。例外は外部ツールが要求するもの（`docdag.yaml`、ADR の frontmatter、`.github/workflows/`、`.github/dependabot.yml`、スキルとルールの frontmatter）。機械向けのワイヤやデータは JSON（LKAL-JSON）を使ってよい。
- 進め方は検証ゲート G0〜G4（ADR 0001 の決定 7。G0 の合格条件は ADR 0018）単位。段階を製品として完結させない。正解を機械判定できるオラクル（Kubernetes との差分試験・決定論的シミュレーション・形式仕様）を先に用意し、それで品質を確かめる。
- 未実装の機能や、実行していない検証を完了扱いにしない。

## ハーネスが止めるもの

例外の無い約束は、文章ではなくフックで強制している（Claude Code は `.claude/settings.json`、Codex は `.codex/hooks.json`。本体は `scripts/agent-harness/`）。止められたら、迂回せずにユーザーに渡す（TUI の折り返し改行混入を防ぐため、長大な 1 行コマンドや複数スニペットは避け、リポジトリ外の scratch スクリプト＋65 文字以内の冪等な実行ワンライナー 1 本にして提示する）。

- **ユーザーが所有するファイルは編集しない。** 定理の主張（`spec/Nayutar/Statements.lean`、`spec/Nayutar/Statements/`、`spec/comparator.json`）、主張が import するフィクスチャ（`spec/Nayutar/Fixture.lean`、`spec/Nayutar/Fixture/`）、Lean のビルド設定（`spec/lakefile.toml`、`spec/lakefile.lean`、`spec/lake-manifest.json`、`spec/lean-toolchain`）、kisage の信頼境界、`.env` や鍵、`.git/` の内部、許可された以外の `.yaml` / `.yml`。
- **`git push`、PR の作成・マージ、ワークフローの実行・再実行・キャンセルは、ユーザーに実行してもらう。** フックと署名の回避（`--no-verify` など）、`lake comparator --inadvisably-no-sandbox`、`lake update` も拒否する。
- 保護対象を含むシェルコマンドは、書き込みに見えるだけで拒否されることがある。対象が保護されていないなら、Edit / Write ツールで書く。間接実行や難読化でガードを迂回してはならない。
- セッション開始時に `agent_harness_selfcheck_FAILED` が出たら、作業の前に直す。
- 一覧と詳細は `scripts/agent-harness/AGENTS.md` にある。

## 決定記録（DocDag）

ADR は `docs/ADR/` にあり、[DocDag](https://github.com/Kaikei-e/DocDag) で検証する（`docdag.yaml`）。

1. `docdag new "<English title>" [--depends-on NNNN] [--supersedes NNNN]` で起票する。ファイル名と title の英語はこのとき決まる。
2. 本文は `docs/ADR/template.md` の見出し順に **日本語** で書く。frontmatter の `title` も日本語にしてよい。
3. `status` は `proposed` で起票する。`accepted` にするかはユーザーが決める。
4. コミット前に `docdag validate && docdag lint --strict` を通す。
5. `accepted` の ADR の既存行は一切書き換えない（CI と編集後フックが `docdag validate --immutable-since` で検査し、ファイル末尾への追記のみを許す）。決定の変更や誤りの訂正は新しい ADR（`supersedes` か `retracts`）で行う。

「何が有効か」は ADR を全部読まずに `docdag query --binding` と `docdag context NNNN --budget 400` で調べる。

## LKAL

- 書いたら `lkal check FILE` と `lkal fmt -w FILE` を通す。`.schema.lkal` は `lkal schema check` も通し、対になる `<stem>.schema.lkal` と `<stem>.lkal` は `lkal check --schema` で照合する。CI は追跡中の全ファイルにこれらをかける。
- 値の読み書きは `lkal get -r FILE PATH` / `lkal set FILE PATH VALUE`（`/` 区切りのパス）。LKAL の構文を自前でパースしない。JSON が要るときは `lkal json -plain FILE`。
- Go で LKAL の値とスキーマを扱うときは、LKAL の Go モジュール（`github.com/Kaikei-e/LKAL` と `github.com/Kaikei-e/LKAL/schema`）の `Parse`・`FromJSON`・`ToJSON`・`Equal`・`Set`・`schema.Compile`・`Validate`・`Complete` を使い、汎用の LKAL-JSON の読み書きや手書き構造体による代替スキーマを Nayutar に書かない。
- 仕様と設計原則はローカル clone の `../LKAL/docs/PRINCIPLE.md` と `../LKAL/docs/ADR/` を読む。LKAL への変更が要ると判断したら、Nayutar 側で回避策を作らず、LKAL 側の変更として提案する。

## 検証コマンド

```sh
docdag validate && docdag lint --strict
lkal check config/*.lkal && lkal fmt -l config/*.lkal && lkal schema check config/dev.schema.lkal && lkal check --schema config/dev.schema.lkal config/dev.lkal
kisage plan config/dev.lkal -detailed-exitcode   # 0: NoOp, 2: 差分あり, 1: エラー / ブロック。シェルに KISAGE_HOST_CONFIG が要る（ユーザーが設定する）
test -z "$(gofmt -l .)" && go vet ./...
go test ./...
govulncheck -test ./... && govulncheck -test -tags drt,etcd ./...   # 既知脆弱性の検査（標準ライブラリの指摘は最新の 1.27 系でないと消えない。v1.10.0 を 1.27 系の toolchain でビルドして入れる）
go test -tags drt ./core/drt/...   # Lean–Go DRT（lake が必要）
go test -tags etcd ./core/store/... ./core/liveness/ ./core/resource/ ./core/wire/    # 契約テスト・線形化可能性検査・障害注入・相乗り計測・ノード登録簿・封筒・ワイヤ層のストア結合を 3 メンバーの etcd にかける（etcd が必要。バイナリは NAYUTAR_ETCD_BIN か PATH の etcd）
go test -tags "drt etcd" ./core/drt/...        # Lean–Go DRT を 3 メンバーの etcd にもかける（lake と etcd が必要）
(cd spec && lake build)
go build -o "${TMPDIR:-/tmp}/check-protected-theorems" ./cmd/check-protected-theorems && "${TMPDIR:-/tmp}/check-protected-theorems" # 欠落を終了コード 1 で拒否する
(cd spec && lake comparator)          # 定理の主張と公理の検査（bwrap が必要）。欠落時は子プロセスが 134 で panic し、comparator は終了コード 1
bash scripts/tests/test-comparator-rejections.sh
python3 scripts/tests/test-agent-harness.py && python3 scripts/agent-harness/harness_selfcheck.py
```

通常の `go test ./...` は Lean も etcd も必要としない。

## 言語と Git

- 会話は日本語。コード・コメント・docstring・コミットメッセージ・PR タイトルは英語。ADR は日本語。
- コメントは既定で書かない。理由が自明でないときだけ 1 行。Issue 番号やゲート名（G0 など）をコードやコメントに書かない。
- `git push`、PR の作成・マージ、ワークフローの実行・再実行・キャンセルは、その操作を明示的に頼まれたときだけ行う。コミットの依頼を push の許可と読み替えない。
- `--no-verify` などでフックや署名を飛ばさない。

## スキル

- 設計案を ADR と突き合わせて詰めるときは `grill-with-docs`、Web で一次資料を調べるときは `web-researcher` を使う（Codex には `.agents/skills/` から見える）。
