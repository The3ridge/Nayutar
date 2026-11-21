# core/store — ストア契約

`core/store/` で作業するときの指示。共通の指示はリポジトリのルートの `AGENTS.md` にあり、ここには繰り返さない。パスはリポジトリのルートからの相対で書いてある。

- 契約は `core/store` の `Store` と `WatchStream`。意味論は `core/store/store.go` の doc コメントと Lean モデル `spec/Nayutar/Store.lean` が定める。どちらかを変えるときは両方を変え、DRT で一致を確かめる。
- 実装は `core/store/memstore`（インメモリの参照実装。試験とシミュレーション用で、永続化しない）と `core/store/etcdstore`（etcd v3.6 のアダプタ）。Lease と TTL つきキーは契約に無い。
- ストア実装を足すときは、`storetest.Run`（契約テスト）、`storetest.RunLinearizability`（porcupine による線形化可能性検査）、`core/drt` のストア DRT の 3 つを通す。
- etcd を相手にする試験はビルドタグ `etcd` の下にあり、テストスイートごとに 3 メンバーの etcd クラスタを 1 つ起動し（`etcdtest.Start`）、storetest.Scope で分離する。メンバー 1 台の構成は無い。バイナリは `NAYUTAR_ETCD_BIN` か `PATH` の `etcd` を使い、見つからなければ失敗する。素の `go test ./...` は etcd を必要としない。
- etcd は、メンバー自身の状態だけで答える経路を持つ（書き込みの無い `Txn`、watch の開始リビジョンとコンパクションの判定）。遅れたフォロワーでは古い値になるので、`etcdstore` はこれらを定足数読み取りに通している。アダプタに操作を足すときは、フォロワーを一時停止する回帰テスト（`etcdstore_test.go` の `pauseFollower`）で確かめる。
- 障害の注入は `storetest.Workload.Disrupt`、`etcdstore/faults_test.go`（1 台の停止）、`etcdstore/partition_test.go`（過半数喪失と分断）。エラーで返った `Txn` は結果不明として扱う。片方向の分断、遅延、時計のずれ、ディスク障害は注入していない。
- 分断は `etcdtest` のピアプロキシで作る（`Cluster.IsolateMember` / `HealPartition`）。メンバーは互いにプロキシの URL を広告し、プロキシは `X-Server-From` ヘッダで送信元を見分けて、切った組の通信を黙って止める。プロキシが本当に分断していることは `etcdtest/etcdtest_test.go`（etcd が必要）が、プロキシ単体の挙動は `etcdtest/peerproxy_test.go`（etcd 不要）が確かめる。etcd の版を上げたら、まずこの 2 つを見る。
- テナントは `core/store/tenant` のビューで `/t/<tenant>/` の下に閉じ込める。テナント名は RFC 1123 のラベル（1〜63 バイト）。ビューは `Compact` を持たず、リビジョンはクラスタ全体のものをそのまま見せる。ビューは権限の境界ではない。
- キーの形は ADR 0006（`accepted`）が決め、文法とコーデックは ADR 0022（`proposed`）にある。キーは手で連結せず、`core/store/keyspace` で組み立てて解析する（`Key.Encode` / `Key.Rel` / `Parse` / `ParseRel` と接頭辞の関数）。形は、ADR 0006 の 6 つに、ノードの記録 `/sys/nodes/<node>` と退避の予算 `/sys/liveness/evictions` を足した 8 つである（ADR 0024）。`<node>` は `<name>` と同じ DNS サブドメインである。セグメントは ASCII の小文字だけで、`<tenant>`・`<kind>`・`<version>` は RFC 1123 のラベル、`<name>` は DNS サブドメイン（全体で 253 バイトまで。Kubernetes の実装と同じく、ドットで区切った要素ごとの上限は無い）、`<group>` は `core` かドットを含むサブドメイン、`ref/` の kind は修飾つき（`<kind>` か `<kind>.<group>`）。文法を変えるときは Go と Lean モデル `spec/Nayutar/Keyspace.lean` の両方を変え、DRT（`nayutar-drt keyspace`）で一致を確かめる。
- テナントの登録は `/sys/tenants/<tenant>` のキーで、`tenant.Registry` が扱う（`Register` / `Get` / `List` / `Delete` / `View`）。値は状態（`.active` / `.terminating`）だけを持つ LKAL-JSON（構造は埋め込みの `core/store/tenant/tenant.schema.lkal` で検査する。ADR 0027）で、ADR 0009 の封筒ではない。`Registry.View` が返すビューは、すべての `Txn` に登録キーの mod revision の比較（フェンス）を足し、登録が変わったあとの書き込みを `ErrFenced` で拒む。削除は、`terminating` にしてから `/t/<tenant>/` を空にし、最後に登録を消す。途中で失敗した削除は呼び直せば続きから進む。素の `tenant.View` とクラスタ全体の `Store` はフェンスを通らないので、削除したテナントにキーを残さない保証は `Registry.View` を通る書き込みにだけ及ぶ。読み取りにフェンスは無い。ビューは、コーデックが認める形以外のキーも通す。形の検査は書き込みを組み立てる側がコーデックで行う。`Ref` の検査と `ref/` の逆参照索引の更新は `core/resource` の `Objects` が行う（ADR 0026）。所有権の更新（ADR 0013）はまだ実装していない。
- ホット領域に専用の型は無く、同じ `Store` を使う。相乗りの影響は `storetest.MeasureCotenancy` で測る。閾値は無く、試験は数値で合否を決めない。
- etcd は、watch が追いつくまで進捗要求（`RequestProgress`）を黙って捨てる。契約もそれに合わせてあるので、呼び出し側は必要なリビジョンの進捗通知が届くまで再要求する（`storetest.Sync` が例）。
- 履歴を再送中の watch は、コンパクションに追い越されると `ErrCompacted` で終わることがある（etcd。起きるかはタイミングによる）。インメモリ実装と Lean モデルはこれを起こさない。呼び出し側は状態を読み直す。
- watch キャッシュの指示は `core/store/watchcache/AGENTS.md` にある。
