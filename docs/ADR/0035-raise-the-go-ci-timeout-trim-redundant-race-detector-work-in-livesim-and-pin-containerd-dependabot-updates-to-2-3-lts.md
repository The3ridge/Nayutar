---
title: Go CI の時間上限を引き上げ、livesim の重複する race 負荷と直列待ちを削り、Dependabot の containerd 更新を 2.3 LTS に制限する
status: accepted
date: 2026-11-25
depends-on: ["0008", "0014", "0015", "0021", "0024", "0031", "0032"]
---

# 0035: Go CI の時間上限を引き上げ、livesim の重複する race 負荷と直列待ちを削り、Dependabot の containerd 更新を 2.3 LTS に制限する

## コンテキスト

`.github/workflows/ci.yml` の `go` ジョブ（`Go core`、`go-version` マトリクス `"1.27.3"` と `"1.27"`、`cache: false`）は、`timeout-minutes: 10` の下で `go vet ./...`・`go test ./...`・`go test -race ./...` を直列実行している。機能追加（`core/resource`・`core/wire`・`core/node`・`core/netpol`・`core/sim/clustersim` など）に伴い総所要時間が増え、速い GitHub-hosted runner 個体でも 7m13s〜7m44s に達し、CPU 性能が約 1.5〜1.6 倍遅いランナー個体を引くと 10m0s で強制キャンセルされる事象が `main` と PR の直近 7 回中 5 回で発生した（Run `45616915826` では `Go core (1.27.3)` が 7m44s で通過した一方、`Go core (1.27)` が `Vet` 1m18s・`Test` 3m46s を経て `Test with race detector` の `livesim.test` 実行中に 10m0s で中断した。Run `45308928216` では逆に `Go core (1.27)` が 6m35s で通過し、`Go core (1.27.3)` が 10m08s で中断した）。

所要時間の約 65〜70% を `core/liveness/livesim` が占めている（速いランナーでも `Test` 131.6s + `Test with race detector` 177.0s = 5m09s、遅いランナーでは `Test` だけで 209.1s）。`livesim.Sim` は単一ゴルーチンの離散イベントキュー（[[0024]]・[[0032]]）であり、`seeds_race_test.go` で `-race` 時の既定シード数を `defaultSeedCount = 5` に絞っているにもかかわらず、`-race` のほうが所要時間が長い原因は次の 3 点にある。

- **`TestDeterministic` の直列反復**：`TestDeterministic` は `DefaultCount: 1` を用いており（`NAYUTAR_SIM_SEEDS=1` の影響を受けない）、1 ゴルーチン上で 100 回の同一シード反復＋別シード 1 回の比較をすべて直列実行しているため、手元実測で通常 1.96s に対し `-race` は 8.00s を要する。
- **200 ノード×920 秒シナリオの重さ**：`TestEvictionsUnderChaos` と `TestEvictionPermitsAreRateLimited` は 1 シードあたり手元で通常 2.2〜2.9s に対し `-race` で 8.2〜10.8s かかり、`defaultSeedCount = 5` に加えて `TestEvictionPermitsAreRateLimited` の `regressionSeeds` 3 件があるため合計 13 シードが `-race` で走る。
- **シードの直列実行**：19 シナリオのうち 16 シナリオ（`TestDeterministic` および `TestSmallClusterControlPlanePartition`・`TestLoneOwnedNodeControlPlanePartition` ほか）が 1 ゴルーチン内で全シード（または全反復）を直列実行しており、マルチコアを使い切れていない（シード単位の `t.Parallel()` を持つのは `TestEvictionPermitsAreRateLimited`・`TestPersistentOneWayPartition`・`TestEvictionsUnderChaos` の 3 つのみ）。

同時に、Dependabot が `github.com/containerd/containerd/v2` を `v2.3.7` から非 LTS の `v2.4.2` へ上げる PR（#33）を作成し、`.github/dependabot.yml` の `gomod` 用 `open-pull-requests-limit: 3` の 1 枠を消費した。これは [[0014]] 決定 2 および [[0031]] 決定 3 の「依存する containerd を LTS 系列である 2.3 系に固定する」決定と食い違う。

満たすべき制約は次の 5 つである。

1. [[0024]] 決定 12 が定める通常実行の検証強度（既定 30 シード＋`regressionSeeds` 全件）を落とさないこと。
2. [[0015]] 決定 4(b) および [[0032]] 決定 3・検証ゲートが定める「同一シード 100 回実行での `TraceHash` 一致検証」を、毎回の `go test ./...` と `go test -race ./...` の双方で回数削減なしに維持すること。
3. [[0021]] の必須ステータスチェック指定および `core/ci_test.go` が検査する `ci.yml` の `go` ジョブの構成（マトリクス `"1.27.3"` と `"1.27"`、`go vet ./...`・`go test ./...`・`go test -race ./...`）とジョブ名を変えないこと。
4. `.claude/rules/ci-workflows.md` が禁じる失敗の握りつぶし、`if:` による条件付きスキップ、`paths:` / `types:` フィルタを持ち込まないこと。
5. [[0014]]・[[0031]] の containerd 2.3 LTS 固定方針を保ちつつ、`2.3.x` のパッチ更新は引き続き受け取れるようにすること。

## 決定

1. **`.github/workflows/ci.yml` の `go` ジョブの `timeout-minutes` を `10` から `20` に引き上げる。**
   - `go-version` マトリクス（`"1.27.3"` と `"1.27"`）、`cache: false`、単一ジョブ内での `go vet ./...`・`go test ./...`・`go test -race ./...` の直列構成、およびジョブ名（`go` / `Go core`）は変えない。

2. **`core/liveness/livesim` の通常実行（`!race`）と `TestDeterministic` 100 回検証を保ったまま、`-race` ビルド（`race`）のシード重複負荷を削る。**
   - `seeds_norace_test.go`（`!race`）では `defaultSeedCount = 30`（および各シナリオの `regressionSeeds` 全件）をそのまま保つ。
   - `seeds_race_test.go`（`race`）では `defaultSeedCount = 2` とする（複数シードの並行実行と各シナリオの `regressionSeeds` 全件は維持する）。
   - `TestDeterministic` の同一シード反復回数（100 回）は `!race`・`race` のいずれでも削減せず、双方で 100 回すべてを実行する。

3. **`core/liveness/livesim` の全シナリオを、シードまたは反復単位の並行サブテストに統一する。**
   - シード列を回す全 18 シナリオ（既存の 3 本＋新規変換の 15 本）は、各シードを `t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) { t.Parallel() ... })` で並行実行する。表駆動テスト 2 本（`TestSmallClusterControlPlanePartition` と `TestLoneOwnedNodeControlPlanePartition`）では、各 `tc.name` サブテストの内側に `seed_%d` サブテストを入れ子にし、ケースごとの `t.Cleanup` で集計を検証・記録する。
   - `TestDeterministic` は対象 1 シードについて基準値 `(h0, w0)` と別シード比較を実行したうえで、100 回の反復比較を並行サブテスト `t.Run(fmt.Sprintf("run_%d", run), func(t *testing.T) { t.Parallel() ... })` で実行する。これにより複数コアで壁時計時間を短縮するとともに、複数の `livesim.Sim` インスタンスが可変グローバル状態を共有していないことを `-race` 下でも直接検証できる。
   - 複数シードにまたがる統計・発火確認の集計は `sync.Mutex` で保護し、全並行サブテスト完了後に実行される `t.Cleanup` で検証・記録する。`t.Cleanup` 内の集計アサーションは `t.Fatalf`（`FailNow` / `runtime.Goexit`）ではなく必ず `t.Errorf` を使う（並行サブテストを持つ親テスト（`len(t.sub) > 0`）の `t.Cleanup` で `t.Fatalf` を呼ぶと、`testing.tRunner` が `checkRaces` および `report` より前に終了し `--- FAIL:` 診断行が欠落するため）。また、先行するシードサブテストが失敗しているときは `t.Cleanup` で集計アサーションを重ねて原因を曖昧にしない。
   - シードごとに長さが変わるシミュレーション時刻の記録は、最後の書き込みが勝つ代入ではなく、実行順に依存しない最大値（`max`）で集計する。

4. **`.github/dependabot.yml` の `gomod` 設定に `github.com/containerd/containerd/v2` のマイナー・メジャー更新を除外する規則を置く。**
   - `ignore` に `dependency-name: "github.com/containerd/containerd/v2"` と `update-types: ["version-update:semver-major", "version-update:semver-minor"]` を追加し、バージョン更新 PR は `2.3.x` のパッチ更新のみを受け付ける。

5. **`core/ci_test.go` に構造化された契約検査を追加する。**
   - `.github/workflows/ci.yml` の `go` ジョブがコメントアウトされていないジョブレベルの `timeout-minutes: 20` 行を持つことを検査する。
   - `.github/dependabot.yml` の `gomod` 設定について `ignore:` 項目の構造を解析し、対象の `dependency-name` が正確に `"github.com/containerd/containerd/v2"` であること、`versions:` を持たないこと、および `update-types` が `["version-update:semver-major", "version-update:semver-minor"]` の厳密な集合であることを検査し、否定ケースの単体試験も添える。

## 根拠（調査結果・出典）

- **CI ログとランナー性能差に基づく所要時間の見積もり**：
  - 速いランナー個体での `go` ジョブ実測合計が 7m13s〜7m44s（`Test` 内の `livesim` が 131.6s、`Test -race` 内の `livesim` が 177.0s）であり、遅いランナー個体（`Test` 内の `livesim` が 209.1s、約 1.59 倍遅い）に換算すると、現状の総所要時間は約 11m30s〜12m00s になる。したがって `timeout-minutes: 15` では外乱に対する余裕が乏しく、`20` 分への引き上げが妥当である。
  - ジョブ全体の `timeout-minutes` を 20 分に延ばしても、パッケージ単位の無限待ちやデッドロックは `go test` 既定のパッケージ別 10 分タイムアウトが引き続き停止させる。
- **内部不変条件（[[0015]]・[[0024]]・[[0032]]）との整合**：
  - `livesim.Sim` はゴルーチンを持たない単一スレッドの離散イベントキューであり、並行実行時の競合対象はシミュレータ内部の状態遷移ではなく、テストハーネスの初期化と複数シード間の統計集計に限られる。そのため `-race` 実行で `defaultSeedCount` を `5` から `2` に絞っても、ハーネスとパッケージ初期化の競合検出および `regressionSeeds` 全件の検査は保たれ、機能的退行（誤検知・検出遅れ）は通常実行（`!race`）の 30 シード＋全 `regressionSeeds` が網羅する。
  - 一方、[[0015]] 決定 4(b) と [[0032]] 決定 3・検証ゲートが要求する `TestDeterministic` の同一シード 100 回実行は回数削減せず、`run_%d` サブテストの並列化によって壁時計時間だけを抑える。
  - 全 18 本のシード駆動シナリオと `TestDeterministic` 100 回反復を `t.Parallel()` へ展開することで、複数コア上で並列実行でき、通常実行と `-race` 実行の双方で壁時計時間を短縮できる。
- **Dependabot `ignore` / `update-types` の適用範囲と既存検証との関係**：
  - 公式リファレンス（下記 URL）によれば、`ignore` の `update-types`（`version-update:semver-major`・`version-update:semver-minor`）が抑止するのは Dependabot の **version updates** のみであり、リポジトリ設定で管理される **security updates** には適用されない。
  - したがって、`2.3.x` に修正版が無い脆弱性が公開された場合でも、セキュリティ更新の側では `2.4+` への PR が立ちうる。また `ci.yml` の `vulncheck`（`govulncheck`）はシンボル到達可能な脆弱性のみで失敗し、呼び出されないモジュール単位の CVE では失敗しない。さらに `node-containerd` ジョブの `CONTAINERD_VERSION`（`2.3.7`）は Dependabot 管理外の手動ピン留めであるため、`go.mod` が新しい `2.3.x` パッチへ進んでもデーモンバイナリは手動更新するまで従来の `2.3.x` 版のまま留まる（containerd 2.x gRPC API はマイナー版・パッチ版間で互換であることを [[0031]] で確認済み）。
- **一次資料 URL**：
  - GitHub Actions ワークフロー構文 `jobs.<job_id>.timeout-minutes`（ジョブの最大実行分数）：https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax
  - Dependabot 設定リファレンス `ignore` / `update-types`（`version-update:semver-major`・`version-update:semver-minor`、および `ignore` が version updates を対象とする仕様）：https://docs.github.com/en/code-security/reference/supply-chain-security/dependabot-options-reference
  - Go 標準ライブラリ `testing` パッケージの並行サブテストと `T.Cleanup`（`t.Cleanup` はすべてのサブテスト完了後に登録逆順で実行される保証、および並行サブテストを持つ親テスト `tRunner` における `FailNow` 挙動）：https://pkg.go.dev/testing#hdr-Subtests_and_Sub_benchmarks 、https://pkg.go.dev/testing#T.Cleanup 、https://go.dev/src/testing/testing.go

## 検討した代替案

- **通常実行（`!race`）のシード数も 30 未満に減らす**：壁時計時間をさらに削れる。しかし [[0024]] 決定 12 が定めた「既定 30 シード」の検証強度を直接引き下げるため採らない。
- **`TestDeterministic` の `-race` 時反復回数を 100 未満（例：5 回）に減らす**：`-race` の所要時間を減らせる。しかし [[0015]] 決定 4(b) および [[0032]] 決定 3・検証ゲートが定める「`go test -race ./...` を含む 100 回実行での `TraceHash` 一致検証」と衝突するため採らない。100 回の反復を `run_%d` サブテストで並列化することで、検証強度を落とさずに壁時計時間の問題を解消できる。
- **`go` ジョブを `go-test` と `go-race` の別ジョブに分割する**：各ジョブを 10 分以内に収めやすい。しかし [[0021]] に基づくリポジトリルールセットの必須ステータスチェック名および `core/ci_test.go` の検査対象を変更する必要があり、チェックアウト・`setup-go`・`cache: false` 下でのモジュール取得とコンパイルも 2 倍になるため、現段階では採らない。
- **`livesim` パッケージ全体を `//go:build !race` で `-race` から除外する**：`-race` 実行を最も速くできる。しかし `go test -race ./...` で `livesim` のテストファイルが 0 件になり、パッケージ初期化や並行サブテスト集計におけるデータ競合の検査まで失われるため採らない。
- **`actions/setup-go` のキャッシュを有効化する**：`go` ジョブのビルド・ダウンロード時間を短縮できる。しかしクリーン環境での再現性を保つ既存の `cache: false` 方針を変えずに、シード並列化・`-race` のシード重複削減・タイムアウト引き上げで十分解決できるため採らない。
- **`containerd/v2` を Dependabot の提案どおり `v2.4.2` に引き上げる**：#33 をそのまま取り込める。しかし [[0014]] 決定 2 と [[0031]] 決定 3 の「2.3 LTS 固定（約 4 か月ごとのマイナー版追従負担を避ける）」に反するため採らない。

## 影響とトレードオフ

- **得るもの**：`go` ジョブが遅いランナー個体でもタイムアウトで落ちにくくなり、シードおよび反復単位の並列化と `-race` の重複シード削減によって通常・`-race` 双方の実行時間も短縮される。また Dependabot が非 LTS の `containerd` 2.4+ バージョン更新 PR を開かなくなり、`open-pull-requests-limit: 3` の枠とレビュー負荷を `2.3.x` パッチ更新や他の依存更新に保てる。
- **失うもの**：`go` ジョブのハングをジョブ単位タイムアウトで切る上限が 10 分から 20 分に延びる（パッケージ単位の `go test` 10 分タイムアウトは残る）。また `-race` 下での `livesim` ランダムシード探索範囲は 5 から 2 に縮む（`!race` 下の 30 シード、`TestDeterministic` 100 回実行、および全 `regressionSeeds` は不変）。
- **引き受けるリスク**：
  - Dependabot の `ignore` 設定により、`containerd/v2` の `2.4+` へのバージョン更新 PR は自動生成されなくなる。`2.3` LTS のサポート期限（2028-04-30、[[0014]]・[[0031]] で確認済み）に近づいた時点では、人間が明示的に新 ADR を起票して追従系列を引き上げる必要がある。
  - `update-types` による `ignore` はバージョン更新のみに効き、セキュリティ更新には効かない。リポジトリで Dependabot security updates が有効であり `2.3.x` に修正版が無い脆弱性が公開された場合、`2.4+` へのセキュリティ更新 PR が開かれる可能性があり、その際は [[0014]]・[[0031]] を置き換えるかどうかの人間の判断が必要になる。
  - `ci.yml` の `vulncheck`（`govulncheck`）は呼び出し経路が到達する脆弱性のみで失敗し、未到達のモジュール単位 CVE は失敗させない。また `node-containerd` ジョブの `CONTAINERD_VERSION`（`2.3.7`）は手動ピン留めであるため、`go.mod` が `2.3.x` のパッチ更新を受けてもデーモン側バイナリは手動で追従させるまで元の `2.3.x` 版のままである（2.x 系の gRPC 互換性は保たれるが、デーモン側の修正は別途確認・更新する）。
  - `livesim` の全シナリオと `TestDeterministic` 100 回反復を並行実行するため、集計変数の保護漏れや可変グローバル状態の混入、あるいは `t.Cleanup` 境界の誤りがあると `-race` 自身で即座に顕在化する（これは並行ハーネスの正しさを検証できる利点でもある）。
- **今後の検証ゲート**：後続の実装において、`go test ./...`、`go test -race ./...`（`core/ci_test.go` の新検査と `livesim` 全並行サブテスト・`TestDeterministic` 100 回並行検証を含む）、`docdag validate && docdag lint --strict`、および CI の `go` ジョブ（`"1.27.3"` と `"1.27"`）が通過することを確かめる。

## 関連ADR

- [[0008]]：ノードの死活監視の基本決定。`livesim` が検証する対象の意味論を定める。
- [[0014]]：containerd を直接駆動し、依存系列を 2.3 LTS に固定すると決めた親 ADR。本決定の Dependabot 制限はこの方針を維持する。
- [[0015]]：シミュレーション境界と同一シード 100 回実行でのトレースハッシュ一致基準を定めた親 ADR。本決定は `-race` 下でも 100 回検証を維持し、並列サブテストで高速化する。
- [[0021]]：`ci.yml` の必須ステータスチェックとパスフィルタ・`if:` 禁止、および `core/ci_test.go` による検査を定めた ADR。本決定は `go` ジョブ名と構成を維持する前提に従う。
- [[0024]]：ノード死活監視の実現方式と `livesim`（既定 30 シード＋`regressionSeeds`・単一スレッドイベントキュー）を定めた ADR。本決定はその検証強度を保ったまま `-race` 重複負荷と直列待ちを削る。
- [[0031]]：containerd v2.3.7 固定の実現方式と `node-containerd` CI ジョブを定めた ADR。
- [[0032]]：`core/sim` と単一ゴルーチンの決定論的イベントキュー・100 回 `TraceHash` 一致検証を定めた ADR。`livesim.Sim` が単一スレッドである根拠と 100 回検証維持の制約を与える。
