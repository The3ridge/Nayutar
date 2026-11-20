---
title: 定理主張の信頼の根をサーバ側でも守り、Lean と DRT のワークフローをすべての変更で走らせる
status: proposed
date: 2026-11-18
depends-on: ["0017"]
---

# 0021: 定理主張の信頼の根をサーバ側でも守り、Lean と DRT のワークフローをすべての変更で走らせる

## コンテキスト

[[0017]] は、定理の主張を主張モジュールと証明モジュールに分け、`lake comparator` で一致を判定し、エージェントハーネスが主張のファイルと `spec/comparator.json` への編集を止める、と決めた。同じ ADR は残るリスクを 2 つ挙げ、どちらも「人間のレビュー」で担保するとした。主張と証明を同じ変更で書き換えること、主張が import するモデル定義を書き換えて主張の意味を変えることである。

main @ `00dd243` の外部レビュー（2026-11-18）は、この「人間のレビュー」を強制する仕組みがどこにも無いことを指摘した。本 ADR の執筆時に次を確かめた。

- GitHub のブランチ一覧 API は `main` を `protected: false` と返す。ルールセットの有無はこの API からは確定できない。
- リポジトリに `CODEOWNERS` が無い。
- ハーネスが止めるのは `spec/comparator.json`、`spec/Nayutar/Statements.lean`、`spec/Nayutar/Statements/` だけである。主張が import する `spec/Nayutar/Fixture/Register.lean` と、どのコードがその名前に解決されるかを決める `spec/lakefile.toml`・`spec/lake-manifest.json`・`spec/lean-toolchain` は編集できる。
- `lake comparator` を走らせる Lean ワークフローと DRT ワークフローは `paths:` フィルタで起動を絞っている。フィルタで起動しなかったワークフローのチェックは Pending のまま残るので、必須チェックにすると、該当パスに触れない PR がマージできなくなる。

つまり、ハーネスを通らない書き込み（シェル経由、フックを持たないエージェント、人間の誤操作）が主張の意味を変えても、それを止める判定はマージの経路に置かれていない。ハーネスのシェル検査はヒューリスティックであり、それだけを信頼の根の守りにはできない。

制約は 2 つある。リポジトリは private で、保守者は 1 人である。private リポジトリでの CODEOWNERS・保護ブランチ・ルールセットは有料プランを要する。

## 決定

1. **信頼の根を次のパスと定める。** 検証ゲートの判定が何を意味するかは、これらが決める。
   - 主張とその判定設定：`spec/Nayutar/Statements.lean`、`spec/Nayutar/Statements/`、`spec/comparator.json`（[[0017]] のとおり）。
   - 主張が import するモデル：`spec/Nayutar/Fixture/` と `spec/Nayutar/Fixture.lean`。
   - Lean のビルド設定：`spec/lakefile.toml`、`spec/lakefile.lean`、`spec/lake-manifest.json`、`spec/lean-toolchain`。`spec/lakefile.lean` はいま存在しないが、置かれると Lake は `lakefile.toml` より優先して読むので、同じ扱いにする。
   - 判定を走らせる側：`.github/`（ワークフロー、CODEOWNERS、Dependabot の設定）、`scripts/agent-harness/`、`.claude/settings.json`、`.codex/hooks.json`、`cmd/check-protected-theorems/`、`core/theoremguard.go`。
   - 判定を走らせる側を固定する試験：`scripts/tests/`（comparator の拒否例とハーネスの試験）、`core/theoremguard_test.go`、`core/ci_test.go`（ワークフローの形を検査する）。
2. **`.github/CODEOWNERS` を置き、1 の「判定を走らせる側」とその試験、および `spec/` 全体をリポジトリ所有者に割り当てる。** `spec/` を丸ごと割り当てるのは、主張が今後 import するモデルを取りこぼさないためである。`.github/` を割り当てるので、CODEOWNERS 自身も所有者を持つ。
3. **エージェントハーネスの保護対象を広げる。** `spec/Nayutar/Fixture/`、`spec/Nayutar/Fixture.lean`、`spec/lakefile.toml`、`spec/lakefile.lean`、`spec/lake-manifest.json`、`spec/lean-toolchain` への編集を、主張のファイルと同じ層（ファイル編集ツール、シェルの検査、`permissions.deny`）で止める。
   - `lake update` と `lake` の `--update` オプションはマニフェストを書き換えるので、シェルの検査で拒否する。
   - `spec/lakefile.lean` が存在しないことを Go の試験でも確かめる。この試験は CI で走るので、ハーネスを通らない書き込みにも効く。
   - [[0017]] は「モデルはエージェントが書く前提なので編集ガードの対象には入れない」とした。本 ADR は、主張がいま import している極小モデルと Lean のビルド設定に限って、これを改める。極小モデルは枠組みを確かめるためのもので、エージェントが書き換える用事が無い。
   - 証明（`spec/Nayutar/Proofs.lean`、`spec/Nayutar/Proofs/`）、DRT の実行ファイル（`spec/Nayutar/Drt.lean`、`spec/Nayutar/Drt/`）、主張が import していないモデル（`spec/Nayutar/Store.lean` など）は、これまでどおりエージェントが書く。
   - 主張が新しいモデルを import するようになるときは、そのモデルを保護対象に加えるかどうかを、その主張を導入する ADR で決める。加えるまでは、[[0017]] の規則（モデル定義の変更は主張の変更として扱い、ユーザーのレビューを受ける）が効く。
   - `.github/workflows/` はローカルのガードでは止めない。CI の変更は日常的に起きるので、2 の CODEOWNERS と 5 のルールセットで守る。
4. **Lean ワークフローと DRT ワークフローから `paths:` フィルタを外す。** どちらも、`main` への PR と `main` への push のすべてで起動する。
   - [[0017]] の決定 3 は、DRT ワークフローの起動条件を `spec/**`・`core/**`・`go.mod` の変更とした。本 ADR はこの起動条件を「すべての変更」に広げる。ケース数（PR で 1,000、`main` への push で 10,000）、ビルドタグ、環境変数は変えない。
   - ジョブの中で変更パスを調べて本体を飛ばす方式は採らない。飛ばされたジョブは成功として報告されるので、「走っていないのに緑」が生まれる。
   - 同じ理由で、Lean・DRT・`ci.yml` のワークフローには、ジョブやステップを飛ばす `if:`、イベントの種類を絞る `types:`、`main` 以外を対象にするブランチ指定を置かない。`core/ci_test.go` がこれを検査する。
5. **`main` にルールセットを設定することを、この決定の成立条件とする。** リポジトリの設定はリポジトリの外にあり、エージェントは変更しない。所有者が次を設定する。
   - `main` への変更は PR を経由する。
   - 必須ステータスチェックに、`ci.yml` の全ジョブ、Lean ワークフローのジョブ、DRT ワークフローのジョブを指定する。
   - force push を禁止し、ブランチの削除を制限する。
   - 承認の必須数とコードオーナーのレビュー必須化は、本 ADR では要件にしない。保守者が 1 人のあいだは、所有者自身の PR を承認できる別の人がいないからである。所有者以外（別の人、またはエージェントの App）が PR を作る運用になった時点で、コードオーナーのレビューを必須にする。
6. **5 が設定されるまで、サーバ側の守りは未成立として扱う。** 2〜4 を実装しても、ルールセットが無ければ `main` への直接の push は止まらない。この状態を、検証ゲートの判定が守られている根拠に数えない。

## 根拠（調査結果・出典）

GitHub Docs は、公開リポジトリ `github/docs` の `main` にある原文（2026-11-18 時点）で確かめた。URL は対応する公開ページである。

- **comparator の前提**（ https://github.com/leanprover/comparator ）：README は保証の前提 1 として、challenge モジュールの推移的な import と `lakefile.toml`／`lakefile.lean` が、利用者の管理下にあるか信頼できることを挙げる。`lake comparator` はこの前提が崩れたことを検出しない。[[0017]] も、モデルのファイルにマクロを 1 行足すと保護対象の定理が `True` になり、`lake comparator` と DRT の両方が通ることを確かめている。
- **CODEOWNERS**（ https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-code-owners ）：コードオーナーは、所有するパスを変える PR でレビューを自動的に依頼される。保護ブランチの「Require review from Code Owners」で、所有者の承認をマージの条件にできる。CODEOWNERS 自身にも所有者を置くことを勧めており、最も安全な方法として `.github/` に置いて `/.github/ @owner_username` と書く形を挙げる。パターンは後ろの行が優先される。構文が誤っている行は読み飛ばされる。
- **利用できるプラン**（同ページと https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/about-rulesets ）：コードオーナー、保護ブランチ、ルールセットは、public リポジトリでは Free でも使える。private リポジトリでは Pro・Team・Enterprise Cloud が要る。このリポジトリのプランは確かめていない。
- **ルールセットの規則**（ https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets ）：PR の必須化（承認は必須でなくてもよい）、必須ステータスチェック、force push の禁止（既定で有効）、削除の制限（既定で選択済み）がある。コードオーナーのレビューを必須にすると、所有者のいる内容を変える PR は、そのコードオーナーの承認が無ければマージできない。
- **パスフィルタと必須チェック**（ https://docs.github.com/en/actions/how-tos/manage-workflow-runs/skip-workflow-runs ）：パスフィルタ・ブランチフィルタ・コミットメッセージで飛ばされたワークフローのチェックは "Pending" のまま残り、そのチェックを必須にしている PR はマージできない。ワークフロー構文のページ（ https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax ）も、マージ前に通ることを必須にするワークフローでは、パスやブランチのフィルタで実行を飛ばすべきでないと書く。
- **飛ばされたジョブは成功として報告される**（ https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-jobs-with-conditions ）：「飛ばされたジョブはステータスを Success と報告する。必須チェックであっても PR のマージを妨げない」。保護ブランチのページ（ https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches ）も、必須チェックが通る状態として `successful`・`skipped`・`neutral` を挙げる。
- **`lakefile.lean` は `lakefile.toml` より優先される**：`spec/` の複製に、別のライブラリだけを宣言する `lakefile.lean` を足して `lake build`（Lake 5.0.0、Lean 4.35.0）を実行すると、`lakefile.toml` の `defaultTargets` は使われず、`lakefile.lean` の内容でビルドされた。
- **内部の規則**（`.claude/rules/ci-workflows.md`）：CI の緑は「検査が走って通った」以外の意味を持ってはならない。決定 4 でジョブ内の判定を採らない理由はこれである。
- **現状の事実**：`scripts/agent-harness/guard_protected_paths.py` の `_is_protected_statement_path` が見るのは `spec/comparator.json`、`spec/Nayutar/Statements.lean`、`spec/Nayutar/Statements/` だけである。`scripts/tests/test-agent-harness.py` は、`spec/Nayutar/Fixture/Register.lean` と `spec/lakefile.toml` への編集が通ることを試験していた。`core/ci_test.go` は、Lean ワークフローが push と pull_request の両方でパスフィルタを持つことを試験していた。
- **確かめていないこと**：PR の作成者が自分の PR を承認できないことは、GitHub の挙動として知られているが、本 ADR の執筆時に一次資料を開けなかった。決定 5 の最後の項はこの挙動を前提にしている。ルールセットを設定したあとの実際の挙動（必須チェックの名前の一致、Dependabot の PR の扱い）も未確認である。マージキューを使う場合に `merge_group` トリガが要るかどうかも確かめていない。3 つのワークフローはこのトリガを持たない。

## 検討した代替案

- **ローカルのガードは変えず、サーバ側だけで守る**：ガードの保守が増えない。しかしサーバ側の守りは有料プランと所有者の設定を要し、効くのは PR の時点である。ガードはコミットの前に止める。極小モデルとビルド設定はエージェントが書き換える用事が無く、止めても作業は減らないため、両方で守る。
- **ビルド設定の 3 ファイルだけをガードに加え、極小モデルは規則のままにする**：モデルをエージェントが書くという [[0017]] の前提を保てる。しかし主張がいま import しているのは極小モデルだけであり、そこが [[0017]] の確かめたすり抜けの入口である。採らない。
- **`spec/` 全体をガードで止める**：取りこぼしが無い。しかし証明、DRT の実行ファイル、製品のモデルはエージェントが書くものであり、止めると [[0017]] の分担が成り立たない。採らない。
- **パスフィルタを残し、必須チェックは `ci.yml` のジョブだけにする**：CI の時間が増えない。しかし `lake comparator` と DRT が必須にならず、信頼の根を判定する検査そのものがマージの条件から外れる。採らない。
- **常時起動し、先頭のジョブで変更パスを見て本体を飛ばす**：CI の時間を増やさずに必須チェックにできる。しかし飛ばされたジョブは成功として報告されるので、判定の誤りがそのまま「走っていないのに緑」になる。採らない。
- **コードオーナーのレビューを今すぐ必須にする**：所有者の承認がマージの条件になる。しかし保守者が 1 人のあいだは所有者自身の PR を承認する人がおらず、バイパスを常用することになる。バイパスが常態なら規則は守りにならないため、PR の作成者が所有者以外になる時点まで待つ。

## 影響とトレードオフ

- **得るもの**：ルールセットを設定すれば、`main` に入るすべての変更が `lake comparator`・DRT・ハーネスの試験を通る。信頼の根に触れる PR では、所有者にレビューが自動で依頼される。主張が import するモデルとビルド設定は、エージェントのファイル編集の時点で止まる。
- **失うもの**：
  - 文書だけの PR でも Lean ワークフローと DRT ワークフローが走る。上限はジョブのタイムアウト（Lean 30 分、DRT 45 分）で、実際の所要時間は最初の実行で測る。private リポジトリなので Actions の利用枠を使う。
  - エージェントは `spec/lakefile.toml`（`lean_lib`・`lean_exe` の追加）、`spec/lake-manifest.json`（kisage の更新）、`spec/lean-toolchain` を変えられない。新しいモデルや DRT スイートを足すときは、この部分をユーザーが書く。
- **引き受けるリスク**：
  - プランが足りなければルールセットを設定できず、決定 5 は成立しない。その場合に残るのは CODEOWNERS によるレビュー依頼の表示（private ではこれも有料プランを要する）とローカルのガードだけである。
  - ガードのシェル検査はヒューリスティックである。`lake update` と `--update` は名指しで拒否するが、`git checkout` や `git apply` で保護対象を書き換える経路、変数やグロブを経由する書き込みは止めない。これは主張のファイルについても同じである。
  - コードオーナーのレビューを必須にしないあいだ、所有者の見落としを止めるものは必須チェックだけである。主張とモデルを同じ PR で書き換える変更は `lake comparator` を通るので、この場合の守りは所有者のレビューに残る。
  - `ci.yml`・Lean・DRT のジョブ名を変えると、必須チェックの指定と食い違い、PR がマージできなくなる。ジョブ名を変える PR では、ルールセットも合わせて直す。
  - `ci.yml` の脆弱性スキャン（govulncheck）は、リポジトリを変えなくても赤になる。このモジュールのコードから届く脆弱性が新しく公開されると、修正版が出るまで失敗し続ける。必須チェックにしているあいだは、その間のすべての PR が止まる。止まったときは、所有者がルールセットをバイパスするか、スキャンを必須チェックから一時的に外す。
  - 本 ADR は [[0017]] の決定の一部（DRT の起動条件、モデルを編集ガードに入れないこと）を変えるが、DocDag には部分的な変更を表す辺が無いので、`depends-on` でつなぎ、変える箇所を本文に書いている。`docdag context 0017` からは、変更があったことが見えない。
- **今後の検証ゲート**：G0 の合格条件は [[0018]] のままである。次のいずれかが起きたら本決定を見直す：(1) プランの都合でルールセットを設定できないとき、(2) 常時起動による CI の所要時間や利用枠が運用の妨げになったとき、(3) PR の作成者が所有者以外になったとき（コードオーナーのレビューを必須にする）、(4) 主張が極小モデル以外のモデルを import するようになったとき。

## 関連ADR

- [[0017]]：定理主張の保護と DRT の枠組みを決めた ADR。本 ADR はその保護対象を広げ、決定 3 の起動条件を広げる。
- [[0003]]：エージェントに定理の主張を変えさせないことを検証方式の 1 つとして決めた ADR。
- [[0018]]：G0 の合格条件。本 ADR は条件を変えない。
