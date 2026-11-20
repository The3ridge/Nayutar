# core/store/watchcache — watch キャッシュ

`core/store/watchcache/` で作業するときの指示。共通の指示はリポジトリのルートの `AGENTS.md` にあり、ここには繰り返さない。パスはリポジトリのルートからの相対で書いてある。

- 読み取りと watch のキャッシュは `core/store/watchcache` にある。決定は ADR 0007、実現方式は ADR 0023（`proposed`）。ライブラリであり、HTTP の配信は無い。範囲外のリビジョンは `ErrTooOld` で表す。値はバイト列のまま扱う。
- 3 層に分かれる。`btree`（永続 B-tree。更新は新しい木を返す）、`window`（ゴルーチン・時計・ストアを持たない状態機械）、`Cache`（上流の `Range` と `Watch` で `window` を進める外殻）。意味論は `window` と Lean モデル `spec/Nayutar/WatchCache.lean` が定める。どちらかを変えるときは両方を変え、DRT（`nayutar-drt watchcache`）で一致を確かめる。
- キャッシュは「閉じたリビジョン」（変更をすべて受け取ったと分かっている最大のリビジョン）だけを、読み取り・スナップショット・watch list の初期状態・ブックマークに使う。`WatchStream` はリビジョンの境界を運ばないので、リビジョン `r` は、`r` より大きい変更か `r` 以上の進捗通知が届くまで閉じない。watch の差分イベントは届いたものから配信するので、受け手はトランザクションの途中を見ることがある。
- 一貫読み取り（`atRev = 0`）は `CurrentRev` を読み、閉じたリビジョンがそこに達するまで待つ（`ReadWait`、既定 3 秒）。達しなければストアを直接読む。待つ者がいるあいだだけ、上流に進捗を要求する（`ProgressInterval`、既定 100 ミリ秒に 1 回まで）。そのため、静かな接頭辞で書いた直後の一貫読み取りは、最大でその間隔だけ待つ。保持範囲より古いリビジョンの読み取りもストアに渡す。
- watch はリングバッファ上の位置だけを持ち、watch ごとの待ち行列は無い。保持範囲に追い越された watch と、キャッシュが読み直したときに開いていた watch は `ErrTooOld` で終わるので、受け手は読み直す。受け手が読まないあいだ、watch は送信で止まる。
- 保持の既定は 10,000 件・150 秒・上限 102,400 件（`Config`）。捨てる単位はリビジョンである。
- `Manager` が、テナント×`(group, kind)` ごとのキャッシュ（`o/<group>/<kind>/`。キーはテナントの中の相対キー）を、最初に使われたときに作る。コンパクションは `Manager.Compact` を通す。各キャッシュに追いつく時間（`ReadWait`）を与えたうえで、準備のできたキャッシュの閉じたリビジョンの最小値より先へは進まない。キャッシュが保持する最古のリビジョンは境界にしない（変更の少ないキャッシュがコンパクションを止め続けるため）。この境界はプロセスの中だけのもので、`Store.Compact` を直接呼ぶ者と他のプロセスには及ばない。
- 4 不変条件（欠落なし・重複なし・順序保存・ブックマーク単調性）は Lean モデルの上の定理（`Nayutar.WatchCache` の `watch_no_loss`・`watch_no_duplicates`・`watch_order_preserved`・`bookmark_monotone`）で、主張は `spec/Nayutar/Statements/WatchCache.lean`、証明は `spec/Nayutar/Proofs/WatchCache.lean` にある。定理が述べるのはモデルである。Go の `window` とは DRT で、外殻とは試験と線形化可能性検査（`core/store/watchcache` の `TestLinearizability`、etcd では `TestEtcdLinearizability`）で結ぶ。
- 外殻の試験は、インメモリ実装と 3 メンバーの etcd（ビルドタグ `etcd`）の両方で走る。上流の watch の終了は試験用の上流が注入する。etcd の障害は、リーダーの停止だけを注入する（`TestEtcdLinearizabilityAcrossLeaderFailure`）。過半数喪失と分断をキャッシュの下で注入する試験は無い。
- まだ無いもの：HTTP の配信と 410 への対応づけ、封筒の解釈、認可と `Ref` のフィルタ、所有権・逆参照・Binding のキャッシュ、使われなくなったキャッシュの破棄、複数のプロセスにまたがるコンパクションの境界。
