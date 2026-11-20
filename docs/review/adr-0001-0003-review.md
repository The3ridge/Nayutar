# ADR 0001・0002・0003・0017 適合レビュー

対象は `main` の `f4a5a2c9e610b1555d06db510e08feb7212d310a`（2026-11-17）である。実装コードは変更していない。差分試験と `lake comparator` の負例は作業ツリー上で一時的に入れ、出力を取ったあと `git checkout --` で戻した。戻したあとの `git status --short` は空である。

ADR 0017 は `status: proposed` で、`docdag query --binding` には出ない。0003 を `supersedes` してもいない。このレビューでは、リポジトリ所有者の指示に従い、0017 を 0003 の今回の実装範囲として読む。0017 決定 1 は実現を二段に分け、今回は DRT 枠組みと定理主張の保護だけを実装し、決定 7 の項目は後続へ送る。決定 7 に当たる 0003 の項目は FAIL / PARTIAL ではなく DEFERRED とする。0017 が未受理なので、この先送りは受理待ちである。受理されるまで、形式的には 0003 の本文が有効なまま残る。

## 結論

今回の実装範囲（0017 決定 1〜6）はほぼ満たしている。素の `go test ./...` が DRT を走らせないことは、0017 決定 3 がそう決めており、欠陥ではない。

48 件の判定は **PASS 34、PARTIAL 5、FAIL 2、DEFERRED 6、NOT CHECKABLE 1** である。

0003 と 0017 の今回の範囲で、要求どおりにならなかったのは 1 件である。証明モジュールから保護対象の定理を消すと、`lake comparator` は終了コード 1 ではなく panic の 134 で終わる。プロセスは失敗するので CI は赤になる。根拠節が書いた「終了コード 1」とは一致しない。

## 判定の置き方

- 0001・0002・0003・0017 の決定節の各号を要求にした。1 つの号で結果が分かれる句は分けた。
- 禁止は、リポジトリに違反が無ければ PASS。
- 0017 決定 7 が後続へ送った項目は DEFERRED（ADR 0017 決定 7）。0017 は proposed である。
- まだ開いていない測定（G1 の p99）は NOT CHECKABLE。
- `scripts/review/check_adr_0001_0003.py` はファイル形状の再確認である。スクリプトの PASS は「レビュー時の形状と一致する」という意味であり、ADR 判定の PASS ではない。

## 要求と結果

| ID | ADR の文 | 実行した検査 | 結果 | 証拠 |
|---|---|---|---|---|
| ADR-0001-R1 | 「目標は Kubernetes の本格的な代替とする。『小さくて使われるもの』は目標にしない。」 | `rg -n "^supersedes:" docs/ADR` | PASS | supersedes キーは 0 件。AGENTS.md は「Kubernetes の本格的な代替」と書く。 |
| ADR-0001-R2 | 「宣言の基盤は LKAL とする。LKAL 側の変更（Schema、Diff/Patch、順序付きレコード集合など）は許容する。」 | `lkal check config/dev.lkal`；`lkal fmt -l config/dev.lkal` | PASS | 両方とも終了コード 0、標準出力は空。人間向けの宣言は `config/dev.lkal`。 |
| ADR-0001-R3 | 「所有権と apply の中核には kisage の葉パス所有権モデルを使う。」 | `git ls-files '*.go'` | PARTIAL | テスト以外の Go は `core/doc.go` と `core/drt/drt.go` だけである。所有権の実装は無い。設計は proposed の ADR 0013 にある。0017 は所有権モデルの DRT を決定 7 で後続へ送っており、所有権の中核そのものの先送りとは書いていない。 |
| ADR-0001-R4 | 「JSON は使ってよい。人間が書く面に YAML は使わない。」例外は外部ツールが要求する YAML。 | `git ls-files '*.yml' '*.yaml'` | PASS | YAML は `docdag.yaml` と `.github/workflows/{ci,drt,lean}.yml` だけである。 |
| ADR-0001-R5 | 「OCI・CNI・CSI・containerd などの既存資産を活かす。データプレーンを自前で作り直さない。」 | `git ls-files '*.go'`；ADR 0014・0016 | PARTIAL | 自前データプレーンは無い。containerd / CNI / CSI を呼ぶコードも無い。proposed の 0014 と 0016 は既存の containerd・CNI・CSI・nftables を使う。 |
| ADR-0001-R6 | 「Certified Kubernetes は名乗らず、API 互換はワークロード部分互換の取り込み層に限る。」 | `rg -n "Certified Kubernetes" README.md core spec .github` | PASS | 認定の宣言も、Kubernetes API の実装も無い。 |
| ADR-0001-R7 | 「開発は個人＋AI コーディングエージェントで行う。そのため、品質は機械判定できるオラクルで外から確かめられる形にする。」 | `python3 scripts/tests/test-agent-harness.py` | PARTIAL | ハーネス試験は 94 件成功。この段階のオラクルは DRT 枠と `lake comparator` である。Kubernetes 差分と ESR は 0017 決定 7 で DEFERRED。シミュレーション用インターフェースは無い。 |
| ADR-0001-R8 | 「段階はすべて検証ゲートとして定義し、どの段階も製品として完結させない。」 | README と追跡ファイル | PASS | リリース成果物は無く、README はライセンスの案内だけである。 |
| ADR-0001-R9 | ADR 0001 決定 7（ゲート G0〜G4 の内容の出発点） | ADR 0001 が指していた調査メモの所在確認 | PARTIAL | 当時の確認ではリポジトリ直下に調査メモは無く、別パスに置かれていた（現在はリポジトリに含めない）。AGENTS.md のリンクも古いパスのままだった。 |
| ADR-0001-R10 | 「個々のゲートの合格条件は別の ADR で定める。」影響節は「最初に G0 の合格条件を ADR として定める。」 | `rg -n "^title:" docs/ADR/*.md`；`docdag query --binding` | FAIL | 合格条件を決定本文にした ADR は無い。0017 は G0 の件数を決定 7 の後続実装で満たすと書くが、G0 合格条件そのものの ADR ではない。 |
| ADR-0001-R11 | 「前提を変えるときは、この ADR を `supersedes` する新しい ADR で行う。」 | `rg -n "^supersedes:" docs/ADR`；`git diff 7678320..f4a5a2c -- docs/ADR/0001-*.md` | PASS | supersedes は 0 件。0001 の差分は `status: accepted` の 1 行である。 |
| ADR-0002-R1 | 「中核の実装言語は Go とする。最低対応版は … Go 1.27.3 とし、以後は特定版に固定せず … 最新 2 メジャーリリースに追従する。」 | `cat go.mod`；`go version`；`go env GOTOOLCHAIN`；上流 `go.mod`；`https://go.dev/dl/?mode=json`；CI run 44012939868 | PASS | `go.mod` は `go 1.27.3` のみ。`toolchain` 行は無い。ローカルは `go1.27.3`、`GOTOOLCHAIN=auto`。LKAL の main と kisage v0.1.2 も `go 1.27.3`。stable は `go1.27.4` と `go1.26.11` で、1.26 は下限未満。CI の Go 1.27 と Go 1.27.3 は success。 |
| ADR-0002-R2 | 「`spec/` 配下に独立した Lake プロジェクトを置き、kisage の Lean モジュールを依存として取り込む。」 | `test ! -f lakefile.toml`；`(cd spec && lake build)` | PASS | `spec/lean-toolchain` は `leanprover/lean4:v4.35.0`。kisage v0.1.2 と同じ。`lake build` は `Build completed successfully (20 jobs)`。ログに `Built Kisage` と `Built Nayutar.Spec` がある。 |
| ADR-0002-R3 | 「Lean ビルドは Go の CI から切り離し」 | `rg -n "lake" .github/workflows/ci.yml`；`go test ./...` | PASS | `ci.yml` に `lake` は無い。`go test ./...` は `core` 0.002s、`core/drt` 0.001s。Lean は起動しない。 |
| ADR-0002-R4 | 「`spec/` に変更があるときだけ `lake build` を実行する。」 | `rg -n "lake build|spec/\\*\\*|core/\\*\\*" .github/workflows/lean.yml .github/workflows/drt.yml` | PASS | 仕様の `lake build`（`lean.yml`）は `spec/**` とワークフロー自身だけである。`drt.yml` が `core/**` と `go.mod` でも `lake build nayutar-drt` するのは、0017 決定 3 が DRT ワークフローの起動条件として書いた範囲である。Go の CI から Lean を外す方は ADR-0002-R3 で満たしている。 |
| ADR-0002-R5 | 「Go 実装自体は形式証明しない。」 | `rg -n "verus\|gobra\|perennial" go.mod core spec` | PASS | `go.mod` に require は無い。Go 側に証明ツールチェーンは無い。 |
| ADR-0002-R6 | 「実装と仕様の接続は差分テスト・シミュレーション・Kubernetes 差分で行い、詳細は [[0003]]、シミュレータ方針は [[0015]] で定める。」 | DRT は ADR-0017 の行。`rg -n "type Clock" --glob '*.go'` | PARTIAL | 差分テストの枠は 0017 決定 2〜4 のとおりにある。Kubernetes 差分は 0017 決定 7 で DEFERRED。`Clock` / `Net` / `Disk` / `Rand` は Go に無い。0015 は proposed。 |
| ADR-0002-R7 | 「スケジューラはモデル検査で検証する。ツール（TLA+ または Quint）は未決とし、スケジューラ実装の着手前に別 ADR で決める。」 | `git ls-files '*.tla' 'quint.toml'` | PASS | TLA+ も Quint もスケジューラ実装も無い。 |
| ADR-0002-R8 | 「Rust の使用は 1 箇所に限定する。」条件は G1 の p99 が 10 ms を超え GC が主因のとき、gRPC 境界の外側。 | `git ls-files '*.rs' 'Cargo.toml'` | PASS | Rust のファイルは 0 件。閾値の測定は始まっていない。 |
| ADR-0002-R9 | 「Verus・Anvil・Welder は参照文献として扱い、コードやツールチェーンの依存には入れない。」 | `rg -n "verus\|anvil\|welder" go.mod spec/lakefile.toml .github` | PASS | 依存ファイルにこれらの名前は無い。文書中の参照だけがある。 |
| ADR-0002-R10 | 「G0 で `spec/` の Lean モデルと Go 間の DRT を回し」 | ストア／所有権モデルの有無。フィクスチャの DRT は ADR-0017-R10 | DEFERRED（0017 決定 7、0017 は proposed） | 0017 の影響節は、G0 の件数（ストア／watch と所有権 CAS の各 1,000、kind 差分 30）を決定 7 の後続で満たすと書く。極小レジスタの DRT はこの段階の枠組みであり、その G0 件数ではない。 |
| ADR-0002-R11 | 「G1 でホット領域ストアの p99 遅延と GC 寄与を計測する。」 | ストア実装の有無 | NOT CHECKABLE | ホット領域ストアが無い。G1 は始まっていない。 |
| ADR-0003-R1 | 「Go の実装コード自体は形式証明しない。」 | ADR-0002-R5 と同じ検索 | PASS | Go 実装を証明するツールや注釈は無い。 |
| ADR-0003-R2 | 「ストア／watch、所有権／apply、パス写像で表せる変換のそれぞれに実行可能な Lean 4 モデルを作り、Go 実装と差分ランダムテスト（DRT）で突き合わせる。」 | `rg --files spec/Nayutar` | DEFERRED（0017 決定 7、0017 は proposed） | 3 領域のモデルは無い。0017 決定 7 が、ストア／watch を 0004・0007 の実装時、所有権／apply を 0013 の実装時、パス写像を変換の ADR 起票時へ送っている。枠組みは ADR-0017-R2 以降で PASS。 |
| ADR-0003-R3 | 「ESR は Lean 4 上の抽象状態機械で証明し、Go コード上では証明しない。」 | `rg -n "ESR" spec --glob '!.lake/**'` | DEFERRED（0017 決定 7、0017 は proposed） | ESR の状態機械は無い。0017 決定 7 は、対象コントローラの ADR と一緒に起票すると書く。Go 上に ESR 証明が無いことは、この号の後半と 0017 の先送りの両方に合う。 |
| ADR-0003-R4 | 「Kubernetes v1.37 系（kind および kwok）を外部の差分オラクルとして用いる。」 | `rg -n "kindest\|kwok" .github core spec` | DEFERRED（0017 決定 7、0017 は proposed） | kind も kwok もワークフローに無い。0017 決定 7 は、比べる対象ができた時点で差分ハーネスの ADR を起票すると書く。版の記録（kind v0.33.0 の `v1.37.0`、kwok v0.8.0 は v1.36 まで）は根拠節にある。 |
| ADR-0003-R5 | 「AI エージェントは Lean の証明本体を書いてよいが、定理の主張（statement）を変更してはならない。」 | ADR-0017-R12 から R18 | PASS | この段階の実現は 0017 決定 6 である。主張と証明の分離、comparator、ハーネスは動く。モデル定義の書き換えで主張の意味が変わる穴は、0017 の影響節が引き受けるリスクとして書いてあり、この段階の未実装ではない。 |
| ADR-0003-R6 | 「Welder からは設計原則のみを取り込む。」2 点はスコープ内リクエストと、バージョン競合を避けるトランザクション抽象。 | `rg -n "rely-guarantee" --glob '!docs/**'` | DEFERRED（0017 決定 7、0017 は proposed） | コントローラも SDK も無い。0017 決定 7 は、この 2 原則をコントローラ SDK の ADR で扱うと書く。 |
| ADR-0003-R7 | 「G0 で Lean–Go DRT（ストア／watch・所有権 CAS 版各 1,000 ケース）、… watch 不変条件 4 件、kind v1.37 差分 30 シナリオ以上を検証する。」 | モデルとシナリオの有無。件数の器は ADR-0017-R7 | DEFERRED（0017 決定 7、0017 は proposed） | 対象そのものが決定 7 の後続である。フィクスチャに対する 1,000 / 10,000 は 0017 決定 3 の器であり、この G0 件数の達成ではない。 |
| ADR-0017-R1 | 「[[0003]] の実現を二段に分ける。今回は『DRT 枠組み』と『定理主張の保護』だけを実装する。… 今回の完了をもって [[0003]] の全項目が実装済みとは扱わない。」 | AGENTS.md の現状の節 | PASS | AGENTS.md は、相手が極小モデルだけで、ストア・watch・所有権のモデル、ESR、Kubernetes 差分は未実装だと書く。 |
| ADR-0017-R2 | 「Go 側（`core/drt`）がシード付きの乱数でケースを作り」 | `core/drt/register_drt_test.go` の `generateCases`；`go test -count=1 -tags drt -run TestGenerateCasesIsDeterministic ./core/drt/` | PASS | `rand.NewPCG(seed, …)` で生成する。同じシードの決定性テストは成功（このターンの `go test -tags drt` は `ok … 0.952s`）。 |
| ADR-0017-R3 | 「Lean の実行ファイル `nayutar-drt <suite>` の標準入力に 1 行 1 ケースで渡す。Lean は入力順に 1 行 1 結果を返し」 | `core/drt/drt.go` の `RunModel`；実行ファイルへの直接入力 | PASS | `RunModel` はケースごとに改行を足して `nayutar-drt register` を起動し、出力行数をケース数と比べる。有効な 1 ケースの標準出力は `{"id":0,"steps":[{"ok":true,"revision":1,"value":1}]}` の 1 行だった。 |
| ADR-0017-R4 | 「Go は各ステップの結果（状態遷移列）を全フィールドについて比べる。」 | `compareCaseResult`；Lean の `put` が revision を増やさない一時変異 | PASS | 比べるフィールドは `id`、ステップ数、`ok`、`value`、`revision` である。変異後の `NAYUTAR_DRT_SEED=7 NAYUTAR_DRT_CASES=5 go test -count=1 -tags drt -run TestRegisterMatchesLeanModel ./core/drt/` は終了コード 1。`step[0].revision mismatch: lean=0 go=1`。戻したあとは不一致が消える。 |
| ADR-0017-R5 | 「不正な入力に対して Lean 側は部分的な結果を出さずに失敗する。」 | `printf '%s\n' '{"id":0,"ops":[{"op":"put","value":1}]}' '{"id":1,"ops":[{"op":"explode"}]}' \| spec/.lake/build/bin/nayutar-drt register` | PASS | 終了コード 1。標準出力は 0 バイト。標準エラーは `line 2: unknown op: explode`。1 行目の正常ケースは出力されていない。 |
| ADR-0017-R6 | 「DRT はビルドタグ `drt` の下に置き … 素の `go test ./...` は Lean を必要としない。」 | `go test -list . ./core/drt/`；`go test -tags drt -list . ./core/drt/` | PASS | タグ無しの一覧は `TestConfigFromEnv` だけである。タグ付きは `TestRegisterMatchesLeanModel` を含む。`register_drt_test.go` の先頭は `//go:build drt`。 |
| ADR-0017-R7 | 「DRT のワークフローは `spec/**`・`core/**`・`go.mod` の変更で起動し、PR では 1,000 ケース、main への push では 10,000 ケースを流す。」 | `.github/workflows/drt.yml`；DRT run 44012939867 のログ | PASS | push と pull_request の両方に 4 つの path（上記 3 つと `drt.yml` 自身）がある。式は `github.event_name == 'push' && '10000' \|\| '1000'`。push の起動対象は `main` だけなので、main への push が 10,000 になる。当該ログは `NAYUTAR_DRT_CASES: 10000` のあと `ok github.com/Kaikei-e/Nayutar/core/drt 0.757s`。 |
| ADR-0017-R8 | 「ケース数とシードは環境変数 `NAYUTAR_DRT_CASES`・`NAYUTAR_DRT_SEED` で与え、不一致の報告にはシードとケースを含めて再現できるようにする。」 | 上の seed=7 の変異 | PASS | 失敗ログに `seed=7 id=0`、`case: {"id":0,"ops":[…]}`、`reproduce: NAYUTAR_DRT_SEED=7 NAYUTAR_DRT_CASES=5 go test -tags drt ./core/drt/...` がある。`TestConfigFromEnv` は未設定時 1000 / 1、明示時 10000 / 42 を検査する。 |
| ADR-0017-R9 | 「`lake` が無い環境では試験は成功扱いにならず失敗する。」 | `HOME` を空の一時ディレクトリにし、`PATH` を Go 1.27.3 の toolchain と `/usr/bin:/bin` だけにして `NAYUTAR_DRT_CASES=1 go test -count=1 -tags drt -run TestRegisterMatchesLeanModel ./core/drt/` | PASS | その環境で `command -v lake` は `no-lake`。テストは 0.00s で失敗する。`register_drt_test.go:291: lake executable not found`。`t.Skip` は無い。 |
| ADR-0017-R10 | 「Lean の結果の各フィールドを 1 つずつ書き換えた変異と、わざと誤らせた Go 実装のそれぞれを、同じ比較関数が不一致として検出することを DRT の試験に含める。」 | `go test -count=1 -tags drt -run 'TestRegisterComparisonDetectsEveryFieldMutation\|TestRegisterDRTCatchesBrokenImplementations' ./core/drt/` | PASS | 終了コード 0。前者は `id`、ステップ数、`ok`、`value` の null と数値、`revision` を `compareCaseResult` にかけて 0 件だと失敗する。後者は delete / cas / put を壊した 3 関数を、同じ関数で Lean の出力と比べ、不一致 0 件だと失敗する。 |
| ADR-0017-R11 | 「revision 付きの単一キーレジスタ（`put`・`delete`・`cas`）を `spec/Nayutar/Fixture/` に置き、対応する Go 実装は試験コードとして持つ。これは製品のモデルではなく、[[0004]]・[[0007]]・[[0013]] の内容を先取りしない。」 | ファイル配置 | PASS | モデルは `spec/Nayutar/Fixture/Register.lean`。Go の `stepRegister` は `core/drt/register_drt_test.go` にだけある。`core/drt/drt.go` は実行ファイルを起動するだけで、レジスタの意味は持たない。 |
| ADR-0017-R12 | 「主張は `spec/Nayutar/Statements.lean` と `spec/Nayutar/Statements/` に置き、証明本体は `sorry` とする。」 | ファイルの中身 | PASS | 両方がある。`Statements/Register.lean` の 3 定理は `by sorry`。`spec/Nayutar.lean` は `Nayutar.Statements` を import しない。 |
| ADR-0017-R13 | 「証明は `spec/Nayutar/Proofs.lean` と `spec/Nayutar/Proofs/` に、同じ名前・同じ主張で置く。」 | `(cd spec && lake comparator)` | PASS | 両方がある。クリーンなツリーで comparator は終了コード 0。`Your solution is okay!`。3 定理の名前は主張側と証明側で一致する。 |
| ADR-0017-R14 | 「保護する定理名と許可する公理（`propext`・`Quot.sound`・`Classical.choice`）は `spec/comparator.json` に列挙する。」 | `spec/comparator.json` | PASS | `theorem_names` はレジスタの 3 定理。`permitted_axioms` はその 3 つだけである。 |
| ADR-0017-R15 | 「CI（Lean ワークフロー）で `lake comparator` を実行する。サンドボックスを切る `--inadvisably-no-sandbox` は使わない。」 | `.github/workflows/lean.yml`；Lean run 44012939921 のステップ | PASS | ステップは `run: lake comparator`。`--inadvisably-no-sandbox` はワークフローに無い。main の最新 Lean 実行（`f4a5a2c`、run 44012939921）で、ステップ「Check theorem statements and axioms」の conclusion は success であり、skipped ではない。ログに `Your solution is okay!` がある。 |
| ADR-0017-R16 | 「エージェントハーネスは、主張のファイルと `spec/comparator.json` への編集を止める。」 | `python3 scripts/tests/test-agent-harness.py` | PASS | 94 件成功。`spec/comparator.json`、`Statements.lean`、`Statements/Register.lean`、ネストしたパス、絶対パス、worktree、`spec/` からの相対パスを拒否する。証明ファイルと Fixture は許可する。 |
| ADR-0017-R17 | 根拠節「主張を弱めた場合（`statement do not match`）、証明を `sorry` にした場合（`Illegal axiom detected: 'sorryAx'`）、独自の公理を足した場合（`Illegal axiom detected`）… 終了コード 1 で拒否」 | 証明モジュールだけを変え、そのたびに `(cd spec && lake comparator)` し、`git checkout --` で戻す | PASS | 証明の `≤` を `<` にすると終了コード 1（`unsolved goals` のあと `build failed`。この弱体化はコンパイルが先に落ち、`statement do not match` の文言には到達しない）。`sorry` は終了コード 1 で `Illegal axiom detected: 'sorryAx'`。`axiom nayutarReviewAx` を証明に使うと終了コード 1 で `Illegal axiom detected: 'Nayutar.Fixture.nayutarReviewAx'`。 |
| ADR-0017-R18 | 根拠節「保護対象の定理を消した場合のいずれも終了コード 1 で拒否され、元に戻すと受理される」 | `Proofs/Register.lean` から `step_revision_monotone` だけを削除して `(cd spec && lake comparator)` | FAIL | 終了コードは 1 ではなく 134。`PANIC at LeanExport.dumpConstant … Constant Nayutar.Fixture.Register.step_revision_monotone not found in environment.` と `error: Child exited with 134`。受理はされない。戻すと `Your solution is okay!`（終了コード 0）に戻る。 |
| ADR-0017-R19 | 「次の項目は後続に送る。」ストア／watch、所有権／apply、パス写像、ESR、Kubernetes v1.37、Welder の 2 原則。 | ADR-0003-R2、R3、R4、R6、R7 の不在確認 | PASS | 当該成果物はリポジトリに無い。AGENTS.md も未実装と書く。この号は「今は作らない」という決定なので、不在は違反ではない。中身の期限は上の DEFERRED 行である。 |

## 差分試験

タグ無しの `go test ./...` が Lean を起動しないことは、0017 決定 3 の要求である。比較は `//go:build drt` の `TestRegisterMatchesLeanModel` が行う。`RunModel` は `lake build nayutar-drt` のあと `nayutar-drt register` を起動する。

ローカルのベースライン（変異前）は `compared 1000 cases and 8017 steps (seed=1)` で PASS だった。main への push は 10,000 ケースで success だった（run 44012939867）。`-v` が無いので CI ログに `compared` は出ない。失敗すれば `ok` にはならない。

`lake` を隠した再試験は成功扱いにならず、`lake executable not found` で失敗した。不正入力は 1 行目が正しくても標準出力を空のまま終了コード 1 で止まった。

## 定理の主張

クリーンなツリーの `(cd spec && lake comparator)` は終了コード 0 で `Your solution is okay!` だった。主張側の `sorry` は警告であり、解の側はカーネルが受理する。main の Lean ワークフローでも同じ文言が出て、該当ステップは success だった。

負例の終了コードは、証明の弱体化が 1、`sorry` が 1、追加公理が 1、保護対象定理の削除が 134 である。削除だけが根拠節の「終了コード 1」と違う。134 はプロセスの失敗なので、GitHub Actions のそのステップは失敗する。黙って通る経路ではない。

ハーネスは編集ツールのペイロードを拒否する。シェルのリダイレクトは見ない、と AGENTS.md と 0017 の影響節が書いている。モデルファイルを変えると、主張ファイルを触らずに主張の意味が変わり得る、とも 0017 が書いており、人間のレビューで担保するとしている。このレビューでは、その穴をこの段階の FAIL には数えない。

## そのほかに実行した検査

| コマンド | 終了 | 出力の要点 |
|---|---|---|
| `go test ./...` | 0 | `core` 0.002s、`core/drt` 0.001s（DRT タグ無し） |
| `go vet ./...` | 0 | 出力なし |
| `gofmt -l`（追跡中の `*.go`） | 0 | 出力なし |
| `docdag validate` | 0 | `OK: 17 docs, 22 typed edges, no cycles` |
| `docdag lint --strict` | 0 | `OK: no lint findings` |
| `docdag query --binding` | 0 | `0001` `0002` `0003`。0017 は出ない |
| `python3 scripts/agent-harness/harness_selfcheck.py` | 0 | `agent_harness_selfcheck: OK` |
| `zizmor --pedantic --strict-collection --offline .`（v1.31.1） | 0 | `No findings to report` |
| `actionlint`（v1.7.12） | 0 | 出力なし。CI ジョブには無く、`.claude/rules/ci-workflows.md` のセルフチェックとして実行した |
| `(cd spec && lake build)` | 0 | Lake 5.0.0、Lean 4.35.0 |

`f4a5a2c` は、このレビュー時点の `main` の先頭である。その push の CI（44012939868）、Lean（44012939921）、DRT（44012939867）はいずれも success である。

## ギャップ

### 今回の範囲（0003 の実装として 0017 が書いたもの）で直すもの

1. **中: 保護対象の定理を消すと `lake comparator` が panic する（ADR-0017-R18）。** 終了コード 134。`Constant Nayutar.Fixture.Register.step_revision_monotone not found in environment.` CI は赤になるので、削除した定理が受理されるわけではない。根拠節が確認したと書く「終了コード 1」の診断にはなっていない。修正は、解決モジュールに定理が無いときに panic せず終了コード 1 と定理名を出すことである。挙動の本体は Lean 4.35.0 の `lake comparator` にある。Nayutar 側で直すなら、ワークフローが 134 を失敗のまま残したうえで、欠落を先に検出する薄い検査を足す。このレビューでは実装を変えていない。

### 0017 が先送りしたもの（DEFERRED。0017 は proposed なので、受理されるまでは 0003 の本文が残る）

次は今回の欠陥ではない。0017 を accepted にすると、先送りが有効な決定になる。accepted にしない場合、0003 のこれらの号は未実装のまま有効である。

- ストア／watch、所有権／apply（CAS）、パス写像の実行可能モデルと DRT（ADR-0003-R2、決定 7）。
- ESR の抽象状態機械と証明（ADR-0003-R3、決定 7）。
- Kubernetes v1.37 の kind と kwok（ADR-0003-R4、決定 7）。kwok v0.8.0 に v1.37 イメージが無いことは 0017 の根拠にある。先送りを実装するときに、kind だけへ縮めるなら 0003 を supersede する。
- Welder の 2 原則（ADR-0003-R6、決定 7）。コントローラ SDK の ADR で書く。
- G0 の件数（ストア／watch と所有権 CAS の各 1,000、watch 不変条件 4、kind 差分 30）（ADR-0003-R7、ADR-0002-R10、決定 7）。

### 0017 の先送りではない、残っているもの

2. **中: G0 の合格条件を決める ADR が無い（ADR-0001-R10）。** 0001 はそれを最初に書くと決めた。0017 は件数を後続へ送るだけで、合格条件の ADR ではない。
3. **中: シミュレーションの接続がコードに無い（ADR-0002-R6）。** 0015 は proposed のまま、`Clock`・`Net`・`Disk`・`Rand` は無い。0017 はこのインターフェースを決定 7 に含めていない。
4. **低: ADR 0001 が指していた調査メモのパスが辿れない（ADR-0001-R9）。** 当時は別パスに置かれていた（現在はリポジトリに含めない）。
5. **低: 所有権の中核と、OCI / CNI / CSI / containerd の利用は未着手（ADR-0001-R3、ADR-0001-R5）。** 矛盾する実装は無い。0017 が先送りしたのは所有権モデルの DRT であり、0001 の前提そのものではない。

0017 の影響節が引き受けるリスク（主張と証明の同時書き換え、モデル定義による主張の意味の変化、ハーネスがシェルを見ないこと）は、この段階の未実装としては数えない。comparator とハーネスは、決定 6 が書いた検査としては動いている。
