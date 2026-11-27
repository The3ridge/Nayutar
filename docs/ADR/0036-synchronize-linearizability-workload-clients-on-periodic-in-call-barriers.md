---
title: ストア線形化可能性検査のクライアントを離脱対応の区間内周期バリアで同期する
status: accepted
date: 2026-11-26
depends-on: ["0003", "0004", "0020", "0022", "0023", "0032", "0035"]
---

# 0036: ストア線形化可能性検査のクライアントを離脱対応の区間内周期バリアで同期する

## コンテキスト

CI Run `45643584796` / Job `139308448904`（`Go core (1.27)`、`go test -race ./...`）において、`core/store/tenant` の `TestFencedLinearizability/seed=1` が `0.02s` で失敗し、`core/store/storetest/linearizability.go:504`（`RunLinearizabilitySynctest` が `CheckLinearizable` の L438 エラーを受けて `t.Fatal(err)` を呼ぶ箇所）で次のエラーを返した。

`workload was effectively sequential: only 105 of 480 operations overlap one of another client (seed 1)`

`storetest.CheckLinearizable` が、全クライアントに開始前の待機も実行中の同期も置かずに `concurrent*4 < len(history)` の重複率検査に失敗する直接の原因は次の 2 点にある。

1. **開始バリアの欠如とタイムスライス未満の実行時間**：`CheckLinearizable` は `w.Clients`（`8`）本のクライアントゴルーチンを順に起動するが、各クライアントが `memstore`・`tenant`・`watchcache` に対して発行する `w.Ops`（`60`）回のインメモリ操作は `-race` 有効時でも合計約 `0.2〜0.3ms` で完了する。これは一般的な OS / ハイパーバイザの 1 タイムスライス（約 `1〜4ms`）より短い。[[0035]] による `livesim` サブテスト並列化後の `go test -race ./...` のような 4-vCPU 負荷集中環境では、OS スレッド（`M`）の起床時刻がわずかにずれるだけで、先行クライアントが 60 回の操作をすべて終えてから後続クライアントが開始する（あるいは早期に終えたクライアントの残余が孤立する）状態が容易に生じる。
2. **`GOMAXPROCS > 1` 下での `runtime.Gosched()` による即時自己再取得**：`CheckLinearizable` は `call := clock.Add(1)` の直後に `runtime.Gosched()` を呼んで他クライアントとの区間重複を図っている。しかし、兄弟 `M` が OS によりプリエンプトされており、かつ現在の `P` のローカルランキューが空であるとき、`goschedImpl` が呼び出し元ゴルーチンをグローバルランキュー（`sched.runq`）へ積んでも、`findRunnable` はワークスティール（`stealWork`）より先に `sched.runq` を参照して同じゴルーチンを即座に取り出して実行する。その結果、他クライアントが割り込む前に `linCall` と `ret := clock.Add(1)` が完了し、`[call, ret]` 区間が重複しない。

この問題の解消にあたり、満たすべき制約は次の 4 つである。

1. `CheckLinearizable` の並行性下限ガード（`concurrent*4 < len(history)`）、シード数（`1..5`）、`Workload` パラメータ（`Clients: 8, Ops: 60`）、および `ErrNotLinearizable` の判定を弱めないこと。
2. 記録される `[call, ret]` 区間が線形化可能性の健全性（`call <= realStart < realEnd <= ret`）を保ち、線形化可能なストア実装に対して偽陽性の `ErrNotLinearizable` を出さないこと。
3. 壊れたストア実装の検出力（`core/store/storetest/linearizability_test.go` の `staleReads`・`tornRange`・`tornTxn`・`splitTxn`・`staleRev`、および `core/store/watchcache/linearizability_test.go` の `staleCacheStore`）、`watchcache` のキャッシュヒット検証（`TestLinearizabilityServesFromCache`）、`testing/synctest` の仮想時刻バブル実行（[[0032]]）、および `porcupine` による検証速度を損なわないこと。
4. `w.Disrupt == nil` でクライアントが呼び出しエラーにより途中終了した場合、`w.Clients <= 1` または `w.Ops == 0` の境界条件、ならびに `w.Disrupt != nil` の障害注入（[[0020]]・[[0023]]）で少数派メンバーに接続したクライアントが `linDisruptCallTimeout`（`3s`）までブロックされる場合のいずれにおいても、デッドロックや不要な停止を起こさないこと。

## 決定

1. **`core/store/storetest` に離脱対応の多者バリア `partyBarrier` を導入する。**
   - `newPartyBarrier(w.Clients)` により初期参加数 `parties` で生成し、`arrive()` と `depart()` を提供する。
   - 各クライアントゴルーチンは開始時に `defer barrier.depart()` を登録する。`depart()` は `b.mu` ロック下で `parties` を 1 減らし、`parties > 0 && arrived >= parties` が成り立てば現在の待機チャネル `b.ch` を閉じたうえで `arrived = 0` とし、`b.ch` を新しい `make(chan struct{})` に置き換えて独立した世代カウンタを持たずにバリアの世代を分離することで、生き残ったクライアントの永続待ちを防ぐ。
   - `arrive()` は `b.mu` を保持したまま `parties <= 1` の判定と現在のチャネル参照 `ch := b.ch` の取得を行い、`parties <= 1` のときはロック解放後ただちに返る（`w.Clients <= 1` および最後の 1 クライアントが残留した場合の無待機保証）。それ以外は最後の到達者が現在のチャネルを閉じて `arrived = 0` と新しい `make(chan struct{})` への置き換えを行ったうえでロックを解放し、非最後の到達者は `b.mu` 解放後に `<-ch` で閉塞待機して全到達者が同時に解放される。
2. **`w.Disrupt == nil` の通常ワークロードでは、`call := clock.Add(1)` と `linCall` の間で `linOverlapStride = 3` 回に 1 回 `barrier.arrive()` で同期する。**
   - `calls%linOverlapStride == 0`（`calls % 3 == 0`）のとき `call := clock.Add(1)` の直後・`linCall` の直前に `barrier.arrive()` を呼ぶ。
   - 残りの `2/3` の呼び出し（`calls%linOverlapStride != 0`）では従来の `runtime.Gosched()` を維持し、各クライアントの非同期な先行・遅延による非対称なインターリービングを残す。
3. **`w.Disrupt != nil` の障害注入ワークロードでは、初回（`calls == 0`）のみ `call := clock.Add(1)` の直後に `barrier.arrive()` で同期する。**
   - `calls > 0` では既存の `5〜10ms` のランダムタイマ待機と `runtime.Gosched()` を維持する。これにより、分断や停止で少数派側に固定されたクライアントが `linDisruptCallTimeout`（`3s`）でブロックされても、健全なクライアントの進行と障害中の継続負荷を止めない。
4. **`concurrent*4 < len(history)` の閾値と検査本体は変更せず、`core/store/storetest` に回帰単体テストを追加する。**
   - 呼び出しエラーによるクライアント途中離脱時の `partyBarrier` 解放、`w.Clients <= 1` / `w.Ops == 0` の境界条件、および `GOMAXPROCS > 1` かつ `testing/synctest` 下での区間重複成立を検証するテストを追加する。

## 根拠（調査結果・出典）

- **区間内バリアによる線形化可能性の健全性（内部不変条件・[[0004]]）**：
  - 各操作の記録手順は `call := clock.Add(1)` → `barrier.arrive()` → `linCall` → `ret := clock.Add(1)` の順であり、実際のストア呼び出し `[realStart, realEnd]` は常に `[call, ret]` に包含される（`call <= realStart < realEnd <= ret`）。
  - したがって `ret_A < call_B` が成り立つ任意の 2 操作 `(A, B)` について `realEnd_A < realStart_B`（`A` が実時間で完了してから `B` が開始した）が必ず成り立つ。Herlihy–Wing の線形化可能性の定義上、記録区間を実呼び出しの外側へ広げることは許容される緩和であり、線形化可能なストアが `ErrNotLinearizable` と誤判定されることはない。
- **最低 `1/3` 重複率の数学的下界とランタイム挙動**：
  - `w.Disrupt == nil`・エラーなし・`w.Clients >= 2`・`w.Ops >= 1` のとき、`calls % 3 == 0` の各ウェーブにおいて全 `P = w.Clients` クライアントが `barrier.arrive()` から解放される前に各自の `call_c` を取得し、全員が解放された後に `ret_c` を取得する。ゆえに `max_c call_c < min_c ret_c` が成立し、そのウェーブに属する全 `P` 個の操作が互いに `[call, ret]` 区間を共有する。
  - 全操作数 `len(history) = Clients * Ops` に対し、重複操作数は常に `concurrent >= Clients * ceil(Ops / 3)` を満たすため、`4 * concurrent >= 4 * Clients * ceil(Ops / 3) > Clients * Ops = len(history)` が、いかなる OS / ハイパーバイザのスケジューリング遅延・プリエンプション下でも常に成立する。
  - また `partyBarrier.arrive()` で最後の到達者がチャネルを `close` すると、Go ランタイムの `goready` が待機中だった兄弟ゴルーチンを実行可能状態へ移し、アクティブな `P` のローカルランキューと他 `P` からのワークスティールを通じて複数の `linCall` が実マルチコア上で並行実行される。チャネル待機は単一 `testing/synctest` バブル内では durable blocking として扱われるため（[[0032]]）、`RunLinearizabilitySynctest` の仮想時刻デッドロック検出とも整合する。
- **欠陥検出力と Porcupine 検証速度の維持（手元実測、2026-11-26）**：
  - 4 コアおよび 2 コア上の `8x` CPU 過負荷環境（`-race`、`Clients: 8, Ops: 60` × 500 回反復）で測定したところ、バリアなし従来実装は `min = 180 / 480`（CI 実測では `105 / 480`）まで低下し、開始時バリアのみでも開始直後の OS プリエンプションで 2 クライアントが直列化し `min = 361 / 480` まで低下した。一方、`linOverlapStride = 3` の区間内周期バリアは通常負荷で `min = 479 / 480`、最悪の単一スレッド総当たりプリエンプションを仮定しても上記の理論下界 `>= 160 / 480`（`160 * 4 = 640 > 480`）を維持した。
  - `memstore`（`8 clients × 60 ops`、シード `1..20`）に対する `porcupine.CheckOperationsTimeout` の実行時間は、`linOverlapStride = 3` で平均 `4.2ms`（最大 `6.6ms`）となり、従来ベースライン（`5.4ms`）と同等でタイムアウト上限 `linCheckLimit = 2m` に対して十分な余裕を保ったまま、既存の壊れたストア実装（`staleReads`・`splitTxn` の `seed = 1` など）を確実に検出した。
- **一次資料 URL**：
  - Go 1.27 `runtime` スケジューラ（`goschedImpl` と `findRunnable` による `sched.runq` 先行取得とワークスティールの順序、BSD-3-Clause）：https://go.dev/src/runtime/proc.go 、https://pkg.go.dev/runtime#Gosched
  - Go 1.27 `testing/synctest`（バブル内チャネルでの durable blocking 仕様）：https://pkg.go.dev/testing/synctest
  - Maurice P. Herlihy and Jeannette M. Wing, "Linearizability: A Correctness Condition for Concurrent Objects", *ACM Transactions on Programming Languages and Systems (TOPLAS)* 12(3), pp. 463–492, 1990：https://doi.org/10.1145/78969.78972
  - `anishathalye/porcupine`（Go 向け線形化可能性検査器、MIT）：https://github.com/anishathalye/porcupine 、https://pkg.go.dev/github.com/anishathalye/porcupine

## 検討した代替案

- **`concurrent*4 < len(history)` ガードを緩和または撤廃する**：実装変更が最小で済む。しかし、実質的に直列実行された履歴をそのまま `porcupine` に渡して合格させてしまい、データ競合や並行バグを見逃すため不採用とする。
- **`CheckLinearizable` 実行中だけ `runtime.GOMAXPROCS(1)` に固定する**：同一プロセス内の他テストへ副作用を与えるうえ、`linCall` 実行中の真のマルチコア並列性とメモリ・ロック競合の検証を失うため不採用とする。
- **ループ開始前（`calls == 0` の直前）に開始バリアのみを置く**：クライアントの起動ずれは解消できるが、開始直後に OS プリエンプションを受けると 1 つの `M` が約 `0.3ms` で 60 回を消化し切り、手元計測で `min = 361 / 480` まで重複が落ち込む（4-vCPU 負荷がさらに強いと `120 / 480` 未満へ割り込む余地が残る）ため不採用とする。
- **周期バリアを `[call, ret]` の外側（`call := clock.Add(1)` の前）に置く**：区間を広げない利点はあるが、各 `P` に 1 クライアントが載った状態で単一 `M` 相当のラウンドロビン・プリエンプションを受けると、バリア間でも各クライアントが `call -> linCall -> ret` を 1 つずつ直列実行し重複数が `0` になり得るため不採用とする。
- **毎回の呼び出し（`calls % 1 == 0`）で同期する、または `w.Disrupt != nil` の `calls > 0` でも周期バリアを張る**：毎回同期するとウェーブ間をまたぐ非対称なずらし（staggered interleaving）が失われるうえ、`porcupine` の検査時間が平均 `14.5ms`・最大 `28.6ms` へと約 3 倍に増大する（`porcupine` 探索木が完全揃いウェーブで膨らみやすい）。また障害注入時の `calls > 0` で全クライアントをバリア同期すると、分断や停止ノードに捕まった 1 クライアントの `3s` タイムアウト待ちで健全クライアントまで総停止するため不採用とする。

## 影響とトレードオフ

- **得るもの**：通常ワークロード（`w.Disrupt == nil`）における `CheckLinearizable` の区間重複率の下界（`>= 1/3 > 1/4`）が OS / ハイパーバイザのスケジューリングジッタに依存せず数学的に保証され、CI（`go test -race ./...`）での `TestFencedLinearizability` などのフレークを解消できる。離脱対応バリアによりエラー途中終了や少数境界でもデッドロックしない。
- **失うもの**：`w.Disrupt == nil` における `1/3` の操作で `[call, ret]` 区間が同期ウェーブの幅まで広がるため、その操作ペア間の「実時間で先行していたかもしれない」順列候補が `porcupine` に残る。ただし `linOverlapStride = 3` により `porcupine` 検査時間は従来と同等（平均 `4.2ms`）に抑えられ、残り `2/3` の `runtime.Gosched()` とともに線形化可能性違反の検出力も維持される。
- **引き受けるリスク**：`partyBarrier` のチャネル置き換えや `defer barrier.depart()` に誤りがあると、エラー発生時や `synctest` バブル終了時にゴルーチンリーク／デッドロックを引き起こす。追加する単体回帰テストおよび `-race`・`synctest` の双方で直接検証する。
- **今後の検証ゲート**：後続の実装において、`core/store/storetest` の新規回帰テストに加え、`TestCheckLinearizableRejectsBrokenStores` の壊れたストア 5 変種すべておよび `TestLinearizabilityCatchesStaleCacheReads` が引き続き通過すること、`go test ./...`・`go test -race ./...`・`go vet ./...`・`docdag validate && docdag lint --strict`・および該当試験（`core/store/...` の `etcd` タグ付き試験や `core/drt`）が通ることを確かめる（[[0003]]・[[0020]]・[[0022]]・[[0023]]・[[0032]]・[[0035]]）。

## 関連ADR

- [[0003]]：実装を Lean モデルとの差分試験で検証する方針。本決定は `core/store` の実装を検証する線形化可能性ハーネスの信頼性を補強する。
- [[0004]]：Lease を除いたストア契約を定めた親 ADR。`storetest.CheckLinearizable` が検査する逐次契約（`Get`・`Range`・`CurrentRev`・`Txn`）の前提を与える。
- [[0020]]：3 メンバー etcd クラスタへの障害・ネットワーク分断注入を定めた ADR。`w.Disrupt != nil` 時の `linDisruptCallTimeout`（`3s`）と障害中の継続負荷制約の根拠となる。
- [[0022]]：厳格なキー文法とフェンスつきテナント登録簿を定めた ADR。今回 CI で失敗が顕在化した `TestFencedLinearizability` の対象である。
- [[0023]]：watch キャッシュの実現方式を定めた ADR。`watchcache` の `TestLinearizability`・`TestLinearizabilityServesFromCache`・`staleCacheStore` の検査前提を与える。
- [[0032]]：`core/sim` と `testing/synctest` の活用を定めた ADR。`RunLinearizabilitySynctest` 内のチャネル同期（durable blocking）の整合条件を与える。
- [[0035]]：`livesim` サブテストの並列化を含む CI 改善を定めた ADR。並列実行増加による 4-vCPU 負荷集中が本事象を顕在化させた背景となる。
