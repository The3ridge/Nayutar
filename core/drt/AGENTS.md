# core/drt — Lean–Go の差分ランダム試験（DRT）

`core/drt/` で作業するときの指示。共通の指示はリポジトリのルートの `AGENTS.md` にあり、ここには繰り返さない。パスはリポジトリのルートからの相対で書いてある。

- Lean–Go の差分ランダム試験（DRT）は `go test -tags drt ./core/drt/...` で実行する。通常の `go test ./...` では走らず、Lean を必要としない。スイートは `register`・`store`・`keyspace`・`watchcache`・`liveness`・`envelope`・`refs`・`orderedset`・`patch`・`ownership`・`node`・`netpol` の 12 個で、`nayutar-drt <スイート>` が標準入力のケースを 1 行ずつ処理する。
- 件数は `NAYUTAR_DRT_CASES`、乱数シードは `NAYUTAR_DRT_SEED` で指定でき、不一致時は再現用のシードとケースが出力される。

## 対になる Go と Lean

モデルと実装は対で意味論を定める。片方を変えるときはもう片方も変え、該当のスイートで一致を確かめる。

| スイート | Go の実装 | Lean モデル |
| --- | --- | --- |
| `register` | `core/drt` の試験用レジスタ | `spec/Nayutar/Fixture/Register.lean`（ユーザーの所有） |
| `store` | `core/store`（`memstore`・`etcdstore`・テナントのビュー） | `spec/Nayutar/Store.lean` |
| `keyspace` | `core/store/keyspace` | `spec/Nayutar/Keyspace.lean` |
| `watchcache` | `core/store/watchcache/window` | `spec/Nayutar/WatchCache.lean` |
| `liveness` | `core/liveness/detector` | `spec/Nayutar/Liveness.lean` |
| `envelope` | `core/resource/envelope` | `spec/Nayutar/Envelope.lean` |
| `refs` | `core/resource/refs` | `spec/Nayutar/Refs.lean` |
| `orderedset` | `core/resource/orderedset` | `spec/Nayutar/OrderedSet.lean` |
| `patch` | `core/resource/patch` | `spec/Nayutar/Patch.lean` |
| `ownership` | `core/resource/ownership` | `spec/Nayutar/Ownership.lean` |
| `node` | `core/node/lifecycle` | `spec/Nayutar/Node.lean` |
| `netpol` | `core/netpol` | `spec/Nayutar/Netpol.lean` |

Lean 側の入口は `spec/Nayutar/Drt.lean` と `spec/Nayutar/Drt/` にある。スイートを足すときは、両方の側とこの表を更新する。

- `netpol` は `Compile` の受理・拒否とエラーコード、`Evaluate` と `EvalNft` の判定に加え、`CompileNft` が作る表の全体を比べる。Lean の `tableText`（`spec/Nayutar/Drt/Netpol.lean`）と Go の `renderNftTable`（`core/drt/netpol_drt_test.go`）が、チェインの順序と名前・基底チェインの印と既定ポリシー・各規則の一致項目（conntrack・送信元・宛先・プロトコル・ポート・アドレス族）と動作（`jump` の行き先まで）を同じ文字列にし、結果 JSON の `nft_table` で突き合わせる。`nftables` のテキスト（`RenderNft`）、ガードのチェイン（`guard_forward`・`guard_input`）、`bridge` ファミリのミラーは DRT の外にあり、実カーネル試験（`nftables*_linux_test.go`）だけが確かめる。
