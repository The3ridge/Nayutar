---
title: 中核を Go で実装し、仕様と証明を Lean 4 で書く
status: accepted
date: 2026-11-17
depends-on: ["0001"]
---

# 0002: 中核を Go で実装し、仕様と証明を Lean 4 で書く

## コンテキスト

[[0001]] で定めた「個人＋AI コーディングエージェントで、外部オラクルを中心に据えて開発する」前提のもとで、実装言語と仕様記述言語を統一する必要がある。計画書（`docs/research/draft_20261117_001.md` §1・§4）は Go と Rust のどちらを軸にするかを比較したが、宣言基盤の LKAL と所有権モデルの kisage がすでに Go で書かれており、言語の未決が後続設計を止めていた。

なお、`docs/research/draft_20261117_001.md` 冒頭で挙げていた「Anvil コントローラの AI 支援 Verus 証明成功率は 40% 未満」という論拠は、その後の公開リーダーボード結果により崩れた（根拠節参照）。それでも、既存 Go 資産、Go 本流の依存群、実装言語に依存しない外部オラクルの存在、Verus の変更頻度を踏まえ、Go 中核＋Lean 4 仕様の採用を維持する。

## 決定

1. **中核の実装言語は Go とする。** 最低対応版は LKAL と kisage の `go.mod` が要求する Go 1.27.3 とし、以後は特定版に固定せず Go がサポートする最新 2 メジャーリリースに追従する。
2. **仕様と証明は Lean 4 で書く。** リポジトリ内の `spec/` 配下に独立した Lake プロジェクトを置き、kisage の Lean モジュールを依存として取り込む。Lean ビルドは Go の CI から切り離し、`spec/` に変更があるときだけ `lake build` を実行する。
3. **Go 実装自体は形式証明しない。** 実装と仕様の接続は差分テスト・シミュレーション・Kubernetes 差分で行い、詳細は [[0003]]、シミュレータ方針は [[0015]] で定める。
4. **スケジューラはモデル検査で検証する。** ツール（TLA+ または Quint）は未決とし、スケジューラ実装の着手前に別 ADR で決める。
5. **Rust の使用は 1 箇所に限定する。** G1 でホット領域ストアの p99 書き込み遅延が 10 ms を超え、プロファイルで GC が主因と示された場合に限り、ストア契約の gRPC 境界の外側で Rust 実装を許す（10 ms は初期値とする）。
6. **Verus・Anvil・Welder は参照文献として扱い、コードやツールチェーンの依存には入れない。**

## 根拠（調査結果・出典）

- **既存資産（Go＋Lean＋DRT）**：LKAL（`https://github.com/Kaikei-e/LKAL`、Apache-2.0）は Go 参照実装であり `go.mod` で `go 1.27.3` を要求すること（LKAL の ADR 0008 が `encoding/json/jsontext` のため引き上げた）を確認した。kisage（`https://github.com/Kaikei-e/kisage`、v0.1.2、Apache-2.0）も `go 1.27.3` で書かれ、Lean toolchain v4.35.0 上に Plan 安全性定理 11 個（`sorry` 0）・プロトコル不変条件 9 個・1,728 件の Lean–Go 差分試験を備えることを確認した（ただし kisage の ADR は全件 `proposed` であり、ESR 自体は未証明である）。
- **Go 依存群と追従方針**：etcd（v3.6.16 等）・containerd（2.3.7 LTS 等）・`cel-go`（v0.32.0）・`go-control-plane`（v0.14.0）・`cilium/ebpf`（v0.22.0）など主要依存が Go で保守されている（`docs/research/draft_20261117_001.md` §2.3・§4）。Go は、新しいメジャーリリースが 2 つ出るまで各リリースをサポートする（`https://go.dev/doc/devel/release`）。Go 1.27.0 は 2026-08-19 に公開され、最新は 1.27.4（2026-11-10）である。`cedar-go` v1.9.0 は Go 1.26 以上を要求し、直近 2 つの Go メジャー版をサポートする（`https://github.com/cedar-policy/cedar-go/releases/tag/v1.9.0`、最新は v1.9.2）。いずれも確認した。
- **オラクル独立性と反復速度**：Carlini の事例（`https://www.anthropic.com/engineering/building-c-compiler`、2026-02-05）は、GCC という既知の正しいオラクルと厳密な検証器があれば AI エージェント群が 10 万行規模のコンパイラを構築できることを示した。Nayutar のオラクル（Kubernetes と Lean モデル）も実装言語から独立しているため、型検査の厳しさよりビルド・試行回数を稼げる Go が有利と判断した。Cedar（`https://arxiv.org/abs/2407.01688`）も Lean 仕様＋差分ランダムテスト型を採っている。
- **反証事実（VeruSAGE）と Verus の更新頻度**：VeruSAGE 論文（`https://arxiv.org/abs/2512.18436`）では Anvil Controller 課題（63 件）の Sonnet 4.5 成功率が 37%（全 849 件では 81%）だった一方、公式リーダーボード（`https://raw.githubusercontent.com/microsoft/verus-proof-synthesis/main/leaderboard/data/verusage-bench-results.json`、2026-01-21 更新）では Opus 4.5 が全体 797/849（93.9%）・Anvil Controller 52/63（82.5%）に達していることを確認した。この事実は本決定に不利に働く。Verus は `0.YYYY.MM.DD.<sha>` 形式で週次にリリースされており（`https://github.com/verus-lang/verus/releases`、例 `0.2026.11.02`）、追従の負担は残る。ただしこれだけでは Verus を退ける理由として弱く、本決定は上の 3 点（既存資産、依存群、オラクルの独立性）に立っている。
- **関連ツール**：Gobra（`https://github.com/viperproject/gobra/releases`、v26.09 等）の本プロジェクトへの適用可否は未確認である。Perennial（`https://github.com/mit-pdos/perennial`）は Lean 4（`iris-lean`）版へ移行中だが、実用 Go システム全体の検証事例は未確認である。Quint（v0.33.1、`https://github.com/quint-co/quint/releases`）は Apalache に加え v0.33.0 から TLC バックエンドも備える。

## 検討した代替案

- **すべて Rust で実装する**：型安全性が高く `openraft`・`aya`・`turmoil`・Verus を使える。しかし Go で書かれた LKAL・kisage・1,728 件の DRT をすべて作り直す必要があり、containerd・CEL・xDS の Go 本流から外れるため採らない。
- **中核を Rust、周辺を Go で実装する**：データ構造・ストア境界の両側に依存が割れ、FFI やプロセス境界を越えた型定義とテスト基盤の二重管理が生じるうえ、Verus 追従の負担も残るため採らない。
- **Lean を使わず Go のみで開発する**：言語とツールチェーンは単一になる。しかし所有者一意性や watch の順序保存といった不変条件をテストケースの手書きだけで網羅するのは難しく、[[0001]] の「機械判定できるオラクル」が仕様側で弱くなるため採らない。
- **Go 1.27 に版を固定する**：`docs/research/draft_20261117_001.md` §4 では 1.27 固定を提案したが、Go 本体と `cedar-go` は直近 2 メジャー版しかサポートしないため、固定するとセキュリティ更新と依存追従が止まる。よって 1.27.3 を下限として追従する方針に改めた。

## 影響とトレードオフ

- **得るもの**：LKAL・kisage のコードと Lean 資産を無変換で取り込め、Go の高速なコンパイルと `spec/` 分離により AI エージェントの試行速度を維持できる。
- **失うもの**：Anvil・Welder の Rust 製検証済みコントローラをそのまま流用できない。Go 側の型システムだけでは防げないバグが増えるため、試験の比重が上がる。
- **引き受けるリスク**：Go の GC による尾部遅延、および「実装自体は未証明」であることから来る DRT・シミュレーションでのバグ見逃しリスクを引き受ける。
- **今後の検証ゲート**：G0 で `spec/` の Lean モデルと Go 間の DRT を回し、G1 でホット領域ストアの p99 遅延と GC 寄与を計測する。G1 で条件付き Rust 化の閾値（10 ms、初期値）を超えた場合は該当部品を切り出し、Perennial の Lean 4 版が実用 Go システムを検証できる水準に達した場合や DRT が重大バグを見逃し続けた場合は、[[0003]] とあわせて言語・検証方式を再検討する。

## 関連ADR

- [[0001]]: 本決定の前提となる「個人＋AI」「外部オラクル中心」「LKAL・kisage 基盤」を定めた ADR。
- [[0003]]: Go 実装を直接証明せず Lean モデルとの差分試験で担保する検証方式を定める ADR。
- [[0015]]: Go コンポーネントの外部作用をインターフェース化し、決定論的シミュレータを選定する ADR。
