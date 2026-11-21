# core/liveness — ノードの死活監視

`core/liveness/` で作業するときの指示。共通の指示はリポジトリのルートの `AGENTS.md` にあり、ここには繰り返さない。パスはリポジトリのルートからの相対で書いてある。

- 決定は ADR 0008（`accepted`）、実現方式は ADR 0024（`proposed`）。ライブラリ `core/liveness` であり、gRPC の輸送と `lk live`・`lk-fakenode` の実行ファイルは無い。集約器の集合は外から渡す（gossip のメンバーシップは無い）。
- 層は 3 つ。純粋な部品（`phi`・`hrw`・`budget`・`relay`・`agent`・`detector`。ゴルーチン・時計・網・ストアを持たない）、外殻（`core/liveness` の登録簿・エンジン・実行ループ）、シミュレーション（`livesim`）。
- 判定の意味論は `detector` と Lean モデル `spec/Nayutar/Liveness.lean` が定める。どちらかを変えるときは両方を変え、DRT（`nayutar-drt liveness`）で一致を確かめる。φ の判定は整数の不等式で、浮動小数点を使わない。時刻はプロセスごとの単調時計のミリ秒で、判定に壁時計は使わない。
- `NotReady` は「ラウンド」で確定する。記録が開いたときの mod revision のまま `ready` で、担当の過半数が疑い、中継したノードから失敗の報告が届いたときだけ提案する。応答の無い中継者は失敗に数えない。集約器からの失敗の報告は、頼めるノードが 1 台も無かったラウンドでだけ数え、開いているあいだに一度でも自己疑い（担当の 30% 以上かつ 3 台以上を疑う、または 2 台以上の担当をすべて疑う）になったラウンドでは数えない。心拍・成功した間接確認・「生きている」票は、先に届いた反対の報告があってもラウンドを終える。
- ノードの記録は `/sys/nodes/<node>` にあり、`liveness.Registry` が扱う。値は `state`（`.pending` / `.ready` / `.notready`）・`since`・`rack`・`evictable` を持つ LKAL-JSON（構造は埋め込みの `node.schema.lkal` で検査する。ADR 0027）で、ADR 0009 の封筒ではない。書き込みは mod revision を比べる `Txn` だけである。登録の無いノードの心拍は無視する。登録と心拍に認証は無い。
- 退避の許可は記録の `evictable` で表す。`NotReady` の確定にはレート制限が無く、制限は許可にかかる（1 分あたり登録ノードの 1%、最低 1 件）。予算は `/sys/liveness/evictions` の 1 キー（構造は埋め込みの `evictions.schema.lkal` で検査する）にあり、許可の `Txn` が記録と予算の両方を比べて書く。許可の時刻は、書く集約器の壁時計と列の最後の時刻の大きいほうである。許可を読んで退避する側はまだ無い。
- 安全性の定理 9 つ（`Nayutar.Liveness`。一覧は ADR 0024 の決定 11）の主張は `spec/Nayutar/Statements/Liveness.lean` と `spec/comparator.json` に、証明は `spec/Nayutar/Proofs/Liveness.lean` にあり、`lake comparator` が検査する。定理が述べるのはモデルで、誤検知が起きないことは述べない。Go の `detector` とは DRT で結ぶ。
- 誤検知と検知の遅れは `livesim` のシナリオ試験で確かめる（`go test ./core/liveness/livesim/`。シードの数は `NAYUTAR_SIM_SEEDS`、1 つを指定するなら `NAYUTAR_SIM_SEED`）。判定の規則や初期値を変えたら、シードを増やして回す。失敗したシードは `regressionSeeds` に固定する。
- 実行ループ（`Aggregator.Run`）は、ストアの呼び出しを別のゴルーチンで時間を区切って行う。ストアが応答しないあいだも投票と ping に答え、失敗は間隔を伸ばしてやり直し、`ctx` が終わるまで戻らない。エラーは `AggregatorConfig.OnError` に知らせる。
- 実行ループは `testing/synctest` で、登録簿はインメモリ実装と 3 メンバーの etcd（ビルドタグ `etcd`）で試験する。実行ループを etcd の上で動かす試験、実機での障害注入、72 時間の連続運転は行っていない。
