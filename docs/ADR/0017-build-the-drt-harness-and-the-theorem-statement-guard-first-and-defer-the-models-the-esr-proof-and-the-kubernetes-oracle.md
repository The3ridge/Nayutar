---
title: DRT 枠組みと定理主張の保護を先に作り、モデル本体・ESR 証明・Kubernetes オラクルは後続に送る
status: accepted
date: 2026-11-17
depends-on: ["0003"]
---

# 0017: DRT 枠組みと定理主張の保護を先に作り、モデル本体・ESR 証明・Kubernetes オラクルは後続に送る

## コンテキスト

[[0003]] は検証方式として 6 項目を決めた。Go 実装は証明しないこと、実行可能な Lean 4 モデルとの差分ランダムテスト（DRT）、Lean 上の抽象状態機械での ESR 証明、Kubernetes v1.37 系（kind・kwok）の外部オラクル、エージェントに定理の主張を変えさせないこと、Welder からの設計原則の取り込みである。

このうち、[[0003]] を分割した時点で先に実装できるものと、できないものがある。DRT の対象となるストア／watch・所有権／apply の中身は [[0004]]・[[0007]]・[[0013]] が決めるが、分割時点ではいずれも `proposed` で実装もなかった（ストア契約のモデルと DRT は [[0004]] の下で提供し、[[0007]] と [[0013]] は後続に残る）。ESR を証明する相手のコントローラも、Kubernetes と比べる相手の自作系もまだ存在しない。一方、DRT を回す仕組みと、定理の主張を守る仕組みは、対象モデルが何であっても同じ形で必要になり、後続の ADR を実装する時点で動いていなければ、その実装を受け入れ判定できない。

[[0003]] は方式だけを決めており、Lean と Go をどうつなぐか、主張の不変をどう機械判定するかは決めていない。本 ADR は、[[0003]] のうち今回実装する範囲と、その具体的な形、後続に送る範囲を定める。

## 決定

1. **[[0003]] の実現を二段に分ける。** 今回は「DRT 枠組み」と「定理主張の保護」だけを実装する。残りは下の 7 に挙げる後続へ送り、今回の完了をもって [[0003]] の全項目が実装済みとは扱わない。
2. **DRT は Go がケースを生成し、Lean の実行ファイルに NDJSON で渡して結果を突き合わせる。** Go 側（`core/drt`）がシード付きの乱数でケースを作り、Lean の実行ファイル `nayutar-drt <suite>` の標準入力に 1 行 1 ケースで渡す。Lean は入力順に 1 行 1 結果を返し、Go は各ステップの結果（状態遷移列）を全フィールドについて比べる。不正な入力に対して Lean 側は部分的な結果を出さずに失敗する。
3. **DRT はビルドタグ `drt` の下に置き、専用のワークフローで回す。** 素の `go test ./...` は Lean を必要としない。DRT のワークフローは `spec/**`・`core/**`・`go.mod` の変更で起動し、PR では 1,000 ケース、main への push では 10,000 ケースを流す（[[0003]] の初期値）。ケース数とシードは環境変数 `NAYUTAR_DRT_CASES`・`NAYUTAR_DRT_SEED` で与え、不一致の報告にはシードとケースを含めて再現できるようにする。
4. **比較が実際に効いていることを試験で示す。** Lean の結果の各フィールドを 1 つずつ書き換えた変異と、わざと誤らせた Go 実装のそれぞれを、同じ比較関数が不一致として検出することを DRT の試験に含める。
5. **最初のスイートは枠組みを検証するための極小モデルとする。** revision 付きの単一キーレジスタ（`put`・`delete`・`cas`）を `spec/Nayutar/Fixture/` に置き、対応する Go 実装は試験コードとして持つ。これは製品のモデルではなく、[[0004]]・[[0007]]・[[0013]] の内容を先取りしない。
6. **定理の主張は、主張だけのモジュールと証明のモジュールに分け、`lake comparator` で一致を判定する。**
   - 主張は `spec/Nayutar/Statements.lean` と `spec/Nayutar/Statements/` に置き、証明本体は `sorry` とする。ここはユーザーが所有する。
   - 証明は `spec/Nayutar/Proofs.lean` と `spec/Nayutar/Proofs/` に、同じ名前・同じ主張で置く。エージェントが書いてよいのはこちらだけである。
   - 保護する定理名と許可する公理（`propext`・`Quot.sound`・`Classical.choice`）は `spec/comparator.json` に列挙する。
   - CI（Lean ワークフロー）は `lake comparator` の前に、`spec/comparator.json` の各定理が主張モジュールと証明モジュールの両方で、そのモジュール根からの import で到達できる宣言として存在することを検査する（`cmd/check-protected-theorems`）。欠ける定理は `missing protected theorem: <定理名> in solution module <モジュール>`（主張側は `challenge module`）と終了コード 1 で拒否する。
   - その後に `lake comparator` を実行する。サンドボックスを切る `--inadvisably-no-sandbox` は使わない。Lean 4.35.0 では、証明モジュールから保護対象の定理を消すと、サンドボックス内の `leanexport` が `Constant <定理名> not found in environment` で panic して終了コード 134 になり、`lake comparator` 自身は `error: Child exited with 134` を出して終了コード 1 で戻る。これは欠落した定理の診断ではない。その診断は事前検査が終了コード 1 で出す。事前検査が先に失敗するので、CI はこの panic に進まない。
   - エージェントハーネスは、主張のファイルと `spec/comparator.json` への編集を止める。
7. **次の項目は後続に送る。**
   - ストア／watch のモデルと DRT：[[0004]]・[[0007]] の実装時。
   - 所有権／apply（CAS 版）のモデルと DRT、Plan 安全性定理の再証明：[[0013]] の実装時。
   - パス写像で表せる変換のモデル：変換を扱う ADR を起票するとき。
   - ESR の抽象状態機械と証明：対象のコントローラを設計する ADR とともに起票する。
   - Kubernetes v1.37 系の外部オラクル（差分ハーネス）：自作系に比べる対象ができた時点で、差分ハーネスの ADR として起票する。版の固定値は下の根拠に記録する。
   - Welder の 2 原則（スコープ内リクエストの rely-guarantee、トランザクション抽象）：コントローラ SDK を設計する ADR で扱う。

## 根拠（調査結果・出典）

- **kisage の構成を引き継げる**：kisage（`https://github.com/Kaikei-e/kisage`、v0.1.2）は、Lean の実行ファイル `kisage-drt`（`lakefile.toml` の `[[lean_exe]]`、`Kisage/DRT.lean`）が NDJSON を標準出力に出し、Go の試験（`verification/lean_drt_test.go`）が 1,728 件を読んで実装と比べる。結果の各フィールドを壊して検出を確かめる試験（`TestLean4DifferentialRejectsPayloadMutations`）も持つ。ローカル clone で確認した。kisage は Lean 側で有限列挙するが、本 ADR はケース数を 1,000／10,000 と変えられるよう Go 側で乱数生成する。
- **kisage には主張を守る仕組みがまだ無い**：kisage が持つのは `sorry`・`axiom` などの禁止語検査（`TestLean4NoSorryAxioms` とエージェントハーネスのガード）だけで、主張そのものの変更を検出する仕組みは無いことをローカル clone で確認した。主張の保護は Nayutar で新たに作る必要がある。
- **Cedar は入力を実装側で生成し、エラーは構造だけを比べる**：Cedar の DRT は Rust 側が libFuzzer と `arbitrary` で入力を生成し、Lean モデルを FFI で呼ぶ。`cedar-lean-ffi` の README（`https://raw.githubusercontent.com/cedar-policy/cedar-spec/main/cedar-lean-ffi/README.md`）は Lean の FFI と Protobuf を使うと述べる。エラーはメッセージ文字列ではなく、どのポリシーが失敗したかで比べる（`cedar-drt/src/tests.rs`、調査ワーカーの報告。本 ADR の執筆時に一次資料を開いて確かめたのは README のみ）。論文（`https://arxiv.org/abs/2407.01688`）は、生成器が出さない形の入力と、DRT の対象にしていない API でバグを見逃したと報告している。本 ADR が 4 で変異検出を試験に含めるのは、この種の見逃しを枠組みの段階で減らすためである。
- **`lake comparator` は固定済みの Lean に組み込まれている**：Lean 4.35.0 のリリースノート（`https://lean-lang.org/doc/reference/latest/releases/v4.35.0/`、2026-10-12）は、`lake comparator` を「主張を持つ challenge モジュールと solution モジュールを比べる comparator のフロントエンド」として追加し、プロジェクトを信頼しない入力として bubblewrap のサンドボックス内でビルド・export すると述べる。手元のツールチェーン（Lake 5.0.0、Lean 4.35.0）の `lake help comparator` でも、設定ファイル `comparator.json`（`challenge_module`・`solution_module`・`theorem_names`・`permitted_axioms`）と終了コード（0：受理、1：主張の不一致・禁止公理・カーネル拒否、2：`bwrap` や設定の不備）を確認した。
- **サンドボックスは非特権ユーザー名前空間を要する**：開発機（Ubuntu 26.04）の既定（`kernel.apparmor_restrict_unprivileged_userns = 1`）では `bwrap: Creating new namespace failed: Permission denied` で `lake comparator` を実行できなかった。export 済みファイル同士の比較（`--challenge-from-export`・`--solution-from-export`）でも、カーネルの再生がサンドボックス内で走るため同じ結果になった。ユーザーがこの値を 0 にすると、サンドボックスを有効にしたまま約 3 秒で受理された。
- **判定が効くことを負例で確かめた**：証明モジュールだけを書き換え、主張の `≤` を `<` に弱めた場合は証明が `unsolved goals` でコンパイルに失敗して `lake comparator` が終了コード 1、証明を `sorry` にした場合は `Illegal axiom detected: 'sorryAx'` で終了コード 1、独自の公理を証明に使った場合は `Illegal axiom detected` で終了コード 1 で拒否される。保護対象の定理を証明モジュールから消した場合、Lean 4.35.0（Lake 5.0.0）ではサンドボックス内の `leanexport` が `PANIC at LeanExport.dumpConstant`（`Constant Nayutar.Fixture.Register.step_revision_monotone not found in environment`）で終了コード 134 になり、`lake comparator` 自身は `error: Child exited with 134` を出して終了コード 1 で戻る。この panic は欠落の診断ではない。事前検査は comparator を起動せず、`missing protected theorem: Nayutar.Fixture.Register.step_revision_monotone in solution module Nayutar.Proofs` と終了コード 1 で拒否する。いずれの負例も元に戻すと受理される。`statement do not match` は、この弱体化ではコンパイルが先に失敗するため到達しない。
- **DRT が一致し、誤りを検出することを確かめた**：極小モデルに対し、既定の 1,000 ケース（8,017 ステップ）と 10,000 ケース（79,823 ステップ）で Lean と Go が一致した。結果のフィールドを書き換えた 8 種の変異と、わざと誤らせた Go 実装 3 種は、いずれも不一致として検出された。`lake` が無い環境では試験は成功扱いにならず失敗する。
- **Kubernetes オラクルの版（後続のための記録）**：kind v0.33.0 のリリースノート（`https://github.com/kubernetes-sigs/kind/releases/tag/v0.33.0`）は既定のノードイメージを `kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5` とする。kwok の最新リリース v0.8.0（`https://github.com/kubernetes-sigs/kwok/releases`）が載せるクラスタイメージは v1.31〜v1.36 で、v1.37 への対応は確認できていない。Kubernetes の最新パッチが v1.37.2 であることは調査ワーカーの報告で、一次資料は未確認である。

## 検討した代替案

- **[[0003]] の全項目を今回まとめて実装する**：ADR と実装が一対一で対応する。しかしモデルの中身は分割時点で `proposed` だった [[0004]]・[[0007]]・[[0013]] が決めるもので、先に作ると未確定の決定を実装で固定してしまう。ESR と Kubernetes 差分は比べる相手が無く、中身のない型だけになるため採らない。
- **最初のスイートをストア契約（[[0004]]）のモデルにする**：G0 の本題に直接進める。しかし [[0004]] を実質的に先行実装することになり、契約の見直しと枠組みの不具合が同時に出たとき切り分けられないため採らない。
- **kisage と同じく Lean 側でケースを有限列挙する**：Lean 側に JSON の読み取りが要らず単純である。しかしケース数を PR と main で変える運用と、失敗したシードを固定して回帰試験にする運用に合わず、状態空間が有限に収まらないストアや watch のモデルに広げられないため採らない。
- **Lean モデルを FFI で Go に組み込む（Cedar の方式）**：呼び出しが速く、ファジングに向く。しかし cgo と Lean ランタイムの初期化が要り、Go の CI から Lean のビルドを切り離すという [[0002]] の構成を崩す。ケース数が 10,000 程度なら標準入出力で足りるため、いまは採らない。
- **主張の保護を自作の Lean チェッカで行う**：`ConstantInfo.type` と依存公理を比べる小さな実行ファイルなら `bwrap` が不要で、どの環境でも動く。しかし公式ツールが既に同じ判定（型に現れる定数の一致とカーネル再生を含む）を提供しており、Lean の内部 API に対する保守を自分で持つことになるため採らない。
- **PR の base と head で主張を比べる**：主張を二重に書かずに済む。しかし人間が主張を正当に変える PR も失敗し、上書きの仕組みが別に要る。主張の置き場所を分ければ「ここを変えられるのは人間だけ」という規則を単純に保てるため採らない。

## 影響とトレードオフ

- **得るもの**：後続の ADR を実装する時点で、DRT と主張の検査がすでに CI で回っている。モデルを足すときは、スイートを 1 つ加え、主張と証明を所定の場所に置くだけで済む。
- **失うもの**：本 ADR の範囲で導入する DRT が検査するのは極小モデルだけで、製品の正しさについては何も示さない。主張は 2 か所（主張モジュールと証明モジュール）に書くことになる。
- **引き受けるリスク**：
  - `lake comparator` は `bwrap` と非特権ユーザー名前空間を要する。開発機では既定で実行できず、ユーザーが root で制限を外したときだけ動く。GitHub のランナーでも bubblewrap の導入と制限の解除が要り、その手順は初回の実行まで確認できていない。
  - 主張モジュールと証明モジュールを同じ変更で両方書き換えると、`lake comparator` は一致と判定する。これを防ぐのはハーネスの編集ガードと、主張のファイルに対する人間のレビューである。ハーネスはファイル編集ツールしか見ないため、シェル経由の書き換えと、フックを持たないエージェントは止められない。
  - 主張が import するモデル定義を書き換えると、主張のファイルを変えずに主張の意味を変えられる。作業用の複製で、モデルのファイルに `≤` の記法を `True` に展開するマクロを 1 行足すと、保護対象の定理が `True` になり、`lake comparator` も DRT も通ることを確認した。主張と証明は同じモデルを import するので、`lake comparator` はこの差を見ない（証明モジュールの中でモデルを定義し直した場合は拒否される）。モデルはエージェントが書く前提なので編集ガードの対象には入れず、主張が import するファイルの差分も人間のレビュー対象とすることで担保する。
  - 生成器の偏りによる見逃しは残る。4 の変異検出が示すのは比較の網羅であって、生成器の網羅ではない。
- **今後の検証ゲート**：G0 の合格条件は [[0018]] が定める。そのうちストア／watch と所有権 CAS 版の DRT 各 1,000 ケース、kind v1.37 差分 30 シナリオ以上は、7 に挙げた後続の実装で満たす。極小レジスタの DRT では満たしたことにならない。次のいずれかが起きた場合は本決定を見直す：(1) GitHub のランナーで `lake comparator` のサンドボックスを安定して動かせないとき、(2) 標準入出力での受け渡しが DRT の所要時間の主因になったとき、(3) 主張と証明の同時書き換え、またはモデル定義の書き換えによるすり抜けが実際に起きたとき。

## 関連ADR

- [[0002]]: 中核を Go、仕様を Lean 4 とし、Lean のビルドを Go の CI から切り離した ADR。
- [[0003]]: 本 ADR が段階に分けて実現する、検証方式の ADR。
- [[0004]]: ストア契約を定める ADR。ストアのモデルと DRT はこの実装時に加える。
- [[0007]]: watch の意味論と不変条件を定める ADR。
- [[0013]]: 所有権の CAS 版プロトコルを定める ADR。
- [[0015]]: 決定論的シミュレーションとシード固定回帰で本方式を補う ADR。
- [[0018]]: G0 の合格条件を定める ADR。本 ADR の極小レジスタは、その合格件数を満たさない。
