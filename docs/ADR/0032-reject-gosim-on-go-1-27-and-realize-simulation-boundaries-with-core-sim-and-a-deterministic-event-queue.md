---
title: Go 1.27 で gosim を不採用とし、core/sim の 4 インターフェースと決定論的イベントキューでシミュレーション境界を実現する
status: accepted
date: 2026-11-24
depends-on: ["0001", "0002", "0003", "0004", "0007", "0008", "0012", "0014", "0015", "0023", "0024", "0029", "0031"]
---

# 0032: Go 1.27 で gosim を不採用とし、core/sim の 4 インターフェースと決定論的イベントキューでシミュレーション境界を実現する

## コンテキスト

[[0015]] は、(1) 全コンポーネントが `Clock`・`Net`・`Disk`・`Rand` の 4 インターフェースを経由してのみ外界と接すること、(2) 単体試験を `testing/synctest` と偽ネットワーク実装で行い実ネットワークや実ファイル I/O に依存させないこと、(3) 系全体のシミュレーションを単一乱数シードで全挙動が決まる決定論的イベントキュー上で実行すること、(4) Go 1.27.3 以上での動作・同一シード 100 回実行でのトレースハッシュ一致・最新 2 メジャー版追従コストの 3 基準で `gosim` を評価してシミュレータを決定すること、(5) 失敗シードを回帰試験として固定すること、を定めた。

これまでに `core/liveness/livesim`（[[0024]]）と `core/node/nodesim`（[[0031]]）を個別に整備してきたが、リポジトリ全体の監査と一次資料の調査により次の課題が明らかになった。

- **`gosim` の Go 1.27 不適合**：`jellevandenhooff/gosim` と継続フォーク `neria-cloud/gosim` は Go 1.27（2026-08-19 公開）に未対応であり、Go 1.27 への移植フォーク `djosh34/gosim` も特定のパッチ版 1 つ（Go 1.27.4）にしか対応しない。いずれも AST 書き換え・`//go:linkname`・Go マイナー版別の非公開ランタイムフックに依存するため、[[0002]] が定める Go 1.27.3 下限および最新 2 メジャー版追従方針と両立しない。
- **4 インターフェースの重複と欠落**：`Clock` が `watchcache`・`liveness`・`node` の 3 か所に重複定義され、共通の `Net`・`Disk`・`Rand` パッケージが存在しない。また一部の実行ループ（`liveness.Aggregator`・`liveness.Registry`・`node.Runner`）で標準の `context.WithTimeout` が注入時計を迂回し、`cmd/lk-node` が `os.ReadFile` を直接呼び、`core/wire` の単体試験が実 TCP ループバックソケット（`127.0.0.1:0`）を、`core/store/watchcache` 等の単体試験が実時間の `time.Sleep` を使っている。
- **系全体イベントキューと 100 回一致検証の未整備**：ストア・watch キャッシュ・リソース所有権・死活監視・ノード実行を単一シードで結合駆動する共通イベントキューが無く、`livesim`（2 回比較）や `nodesim`（20 回比較、`regressionSeeds` 未整備）も [[0015]] の同一シード 100 回トレースハッシュ一致基準を満たしていない。

## 決定

1. **`gosim` を不採用とし、外部依存を持たない自作シミュレーション基盤 `core/sim` を採用する。**
   - `jellevandenhooff/gosim` は [[0015]] の評価基準 (a)（Go 1.27.3 以上での動作）および基準 (c)（Go 最新 2 メジャー版への追従コスト）を満たさないため採用しない。
   - ランタイム内部フックや AST 変換は行わず、明示的な効果境界（`Clock`・`Net`・`Disk`・`Rand`）と単一スレッドの離散イベントキューで系全体の決定論性を実現する。

2. **共通パッケージ `core/sim` に `Clock`・`Net`・`Disk`・`Rand` の 4 インターフェースと実環境・模擬実装を定義する。**
   - **`Clock`**：`Now() time.Time` と `After(d time.Duration) <-chan time.Time` を持ち、タイマー解放可能な `Timer` 拡張と `sim.WithTimeout(ctx, clock, d)` を提供する。`watchcache.Clock`・`liveness.Clock`・`node.Clock` は `sim.Clock` の型エイリアスに統一し、`liveness` と `node` 内の `context.WithTimeout` を `sim.WithTimeout` に置き換える。
   - **`Net`**：`DialContext(ctx, network, address)` と `Listen(network, address)` を持つ。模擬実装 `sim.MemNet` は標準の `net.Pipe` 上に仮想アドレス交換機・遅延・切断・分断注入を構築し（遅延は配送の遅れとして注入時計の上で与え、書き手を待たせない）、`testing/synctest` バブル内で durably blocked として機能させる。`core/wire` に `Net` 注入経路を追加し、`core/wire` と `etcdtest/peerproxy` の単体試験から実 TCP ソケットを排除する。
   - **`Disk`**：`ReadFile`・`WriteFile`・`MkdirAll`・`Remove`・`Stat` を持ち、ディレクトリ境界を `os.Root` で守る `RootedDisk`（ルートの外を指すシンボリックリンクと、絶対パスのシンボリックリンクをたどらない） と、I/O 障害注入可能なインメモリ `MemDisk` を提供する。`cmd/lk-node` の設定読み込みを `Disk` 経由に移し、単体試験を `MemDisk` 上で完結させる。
   - **`Rand`**：固定幅の `Uint64()`・`Int64N(n int64)`・`Fill(p []byte)` と `io.Reader` アダプタ `RandReader` を持つ。本番用は `crypto/rand` ベースの `CryptoRand()`、シミュレーション用は C2SP `chacha8rand` 仕様に準拠する `math/rand/v2.ChaCha8` ベースの `NewDetRand(seed)` とする。`ChaCha8.Read` と `Uint64` の混在による内部バッファ順序変化を避けるため、`Fill` は `Uint64()` のリトルエンディアン展開のみで実装する。

3. **離散イベントキュー `sim.Queue` と多コンポーネント結合シミュレータ `core/sim/clustersim` を設ける。**
   - `sim.Queue` は浮動小数点を使わず整数ミリ秒と単調連番 `(atMs int64, seq uint64)` の最小ヒープでイベントを決定論的に発火させ、模擬時計・決定論 PRNG・`Buggify` 確率故障点・SHA-256 `TraceHash()` を一括管理する。
   - `core/sim/clustersim` で `memstore`・`tenant`・`watchcache`・`resource.Objects`（参照・順序付き集合・Patch・葉パス所有権 CAS）・`liveness`・`node/lifecycle` を単一 `sim.Queue` と単一シード上で結合駆動し、通信分断・遅延・ノード故障・CAS 競合の下で不変条件を検証する。
   - `clustersim`・`livesim`・`nodesim` のすべてで、同一シード 100 回実行の `TraceHash` 完全一致試験（[[0015]] 基準 (b)）および `regressionSeeds` と環境変数 `NAYUTAR_SIM_SEED` / `NAYUTAR_SIM_SEEDS` による失敗シード固定・単発再現を実装する。

4. **通常の単体試験から実時間 `Sleep` と実ソケットを排除し、AST 境界検査で逆流を防ぐ。**
   - `core/store/watchcache`・`core/store/memstore`・`core/store/etcdstore/etcdtest/peerproxy_test.go`・`core/wire` の通常試験を `testing/synctest`・`sim.FakeClock`・`sim.MemNet` へ移行する。
   - `core/boundaries_test.go` に AST 検査を追加し、認可された実環境アダプタ・試験ハーネス以外の `core/` 本番コードが `os/exec`・`math/rand`・`crypto/rand` の import や `time.Now`・`time.Sleep`・`time.After`・`time.NewTicker`・`net.Dial`・`net.Listen`・`os.ReadFile` 等の直接呼び出しを行うことを機械的に禁止する。

## 根拠（調査結果・出典）

- **`gosim` の一次資料評価**（https://github.com/jellevandenhooff/gosim 、https://pkg.go.dev/github.com/jellevandenhooff/gosim 、およびフォーク https://github.com/neria-cloud/gosim 、いずれも MIT、2026-11-24 確認）：
  - 上流 `jellevandenhooff/gosim` は実験的段階（SemVer タグ未発行、UDP 未対応等の制限あり）であり、Go 1.27（2026-08-19 公開、https://go.dev/doc/devel/release ）に未対応である。継続保守フォーク `neria-cloud/gosim` でも対応は Go 1.25 / Go 1.26 系までに留まり、Go マイナー版ごとの非公開ランタイム構造プロファイルと AST 変換を要するため、[[0002]] の Go 1.27.3 下限および最新 2 メジャー版追従と整合しない。
  - Go 1.27 への移植フォーク `djosh34/gosim`（https://github.com/djosh34/gosim 、MIT、SemVer タグ未発行、個人による保守）は、README で対応を Go 1.27.4 のみとし、新しい Go ではフックの再確認が要ると明記している。下限の Go 1.27.3 は対応外で、パッチ版ごとに追従が要るので、基準 (a) と (c) を満たさない。
- **Go 1.27 標準 `testing/synctest` と `math/rand/v2.ChaCha8`**（https://go.dev/doc/go1.25 、https://go.dev/doc/go1.26 、https://go.dev/doc/go1.27 、https://pkg.go.dev/testing/synctest 、https://go.dev/blog/synctest 、https://c2sp.org/chacha8rand 、https://go.dev/blog/chacha8rand 、2026-11-24 確認）：
  - `testing/synctest` は Go 1.25 で一般提供となり、Go 1.27 で `synctest.Sleep` とインメモリ偽ネットワーク既定の `httptest.NewTestServer` が追加された。`net.Pipe` は内部でチャネル同期を用いるためバブル内で durably blocked となり、`core/wire` の HTTP/2（`h2c` および TLS 1.3 `h2`）試験を実ソケットなしで実行できる。一方、`synctest` 自体はゴルーチンのスケジューリング順を決定論化しないため、系全体の単一シード再現には単一スレッドの離散イベントキューが必要である。
  - `math/rand/v2.ChaCha8` は C2SP `chacha8rand` 仕様（4-way インターリーブ固定）により CPU アーキテクチャと将来の Go バージョンを跨いでビット一致の `Uint64` 列を保証する。標準実装 `src/math/rand/v2/chacha8.go` が `Read` と `Uint64` の交互呼び出しでバイト順が変わる旨を明記しているため、`Uint64` のみに統一して決定性を守る。
- **決定論的シミュレーションの先例とクリーンルーム境界**（FoundationDB https://apple.github.io/foundationdb/testing.html 、https://github.com/apple/foundationdb Apache-2.0；TigerBeetle VOPR https://github.com/tigerbeetle/tigerbeetle/blob/main/docs/internals/vopr.md Apache-2.0；`porcupine` v1.3.1 https://github.com/anishathalye/porcupine MIT、2026-11-24 確認）：
  - FoundationDB（`Sim2` の `INetwork` / `IAsyncFile` / `deterministicRandom` と `BUGGIFY`）および TigerBeetle（VOPR の単一スレッド離散イベントループと Swarm Testing）はいずれも、ランタイム改変ではなく明示的な I/O 境界の差し替えと単一シード PRNG によって系全体の決定論的故障注入を成立させている。いずれも Apache-2.0 であるがコード流用は行わず、Go 固有のクリーンルーム設計として実装する。なお線形化可能性検査器 `porcupine v1.3.1` はすでに `core/store/storetest` で稼働している。
- **手元での確認**（2026-11-24）：
  - `core/sim`（`FakeClock`・`MemNet`・`MemDisk`・`os.Root` による `RootedDisk` の境界検査・`ChaCha8` 決定論 PRNG・`Queue`）および `core/sim/clustersim`（同一シード 100 回 `TraceHash` 一致・分断／遅延／ノード故障／所有権 CAS 競合下の不変条件検査）が `-race` 付きで通過することを確かめた。
  - `livesim` と `nodesim` の同一シード 100 回一致検証、`watchcache`・`memstore`・`tenant`・`peerproxy`・`wire`・`lk-node` の実ソケット／実時間 `Sleep` 排除（実レイテンシを測る `storetest` の相乗り計測だけは実時間のまま、理由つきの許可リストに置く）、`TestCoreRoutesExternalEffectsThroughSimInterfaces` と `TestUntaggedTestsAvoidRealSocketsAndRealTime` の AST 検査、`golangci-lint`（既定のビルドタグ）が通過することを確かめた。`govulncheck` は Go 1.27.3 の標準ライブラリに `net/http` の GO-2026-6859（Go 1.27.4 で修正）だけを報告し、依存モジュールには検出が無い。

## 検討した代替案

- **Go ツールチェーンを Go 1.26 に下げて `gosim` フォークを採用する**：ゴルーチン交錯まで自動変換できる可能性がある。しかし [[0002]] が確定した Go 1.27.3 下限（および標準ライブラリ脆弱性修正への追従）に反し、半年ごとの Go 更新が `gosim` 側の非公開フック対応待ちでブロックされるため採らない。
- **各サブシステムに個別の `Clock` やシミュレータを残したままにする**：既存ファイルへの変更が減る。しかし [[0015]] 決定 1・3 が求める共通 4 インターフェースと系全体イベントキューを欠き、コンポーネント横断の競合（ストア更新 → watch キャッシュ配信 → リソース CAS → ノード制御）を単一シードで再現できないため採らない。
- **`core/wire` や `watchcache` の単体試験に実ループバックソケットと実時間 `Sleep` を残す**：試験コードの書き換えが不要になる。しかし [[0015]] 決定 2 に反し、負荷変動によるタイマー揺らぎやポート競合の余地を残すため採らない。

## 影響とトレードオフ

- **得るもの**：Go 標準ツールチェーン（Go 1.27.3 以上）のまま外部依存ゼロで、4 インターフェースによる外界隔離・AST 境界検査・実ソケット／実時間 `Sleep` なしの高速単体試験・同一シード 100 回ハッシュ一致の系全体離散イベントシミュレーションが揃う。
- **失うもの**：ゴルーチンを複数立てるコンポーネント単体の並行交錯自体は Go の `-race` と `testing/synctest`・`porcupine` で検出し、単一シード完全再現は離散イベントキュー駆動の状態機械・結合モデルで行う二段構えとなる。
- **引き受けるリスク**：新しいアダプタを追加する際に `sim.Clock` 等を経由し忘れる恐れがあるが、`core/boundaries_test.go` の静的 AST 検査で毎回の `go test ./...` にて自動拒否する。
- **今後の検証ゲート**：`clustersim`・`livesim`・`nodesim` の同一シード 100 回トレースハッシュ一致試験と多シード障害注入試験を毎回の `go test ./...`（および `-race`）で走らせ、後続のスケジューラ・コントローラ追加時も `clustersim` に組み込んで検証する。

## 関連ADR

- [[0001]]：検証ゲートと決定論的オラクルを先行整備する前提を定める。
- [[0002]]：Go 1.27.3 以上・最新 2 メジャー版追従を定める。
- [[0003]]：形式仕様・DRT・決定論的シミュレーションによる品質保証方針を定める。
- [[0004]]：`clustersim` の基盤となるストア契約を定める。
- [[0007]]・[[0023]]：`sim.Clock` を共有し `clustersim` に結合する watch キャッシュを定める。
- [[0008]]・[[0024]]：`sim.Clock` と 100 回一致検証に統合する死活監視と `livesim` を定める。
- [[0012]]・[[0029]]：`sim.MemNet` 上で実ソケットなし単体試験を行う HTTP/2 ワイヤ層を定める。
- [[0014]]・[[0031]]：`sim.Clock`・`sim.Disk`・`nodesim` 100 回一致検証に統合するノード実行基盤を定める。
- [[0015]]：本 ADR が実現・シミュレータ選定を完結させる親決定。
