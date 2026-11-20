---
title: etcd v3 API の Lease を除いたサブセットを内部ストア契約として固定する
status: accepted
date: 2026-11-17
depends-on: ["0001", "0003"]
---

# 0004: etcd v3 API の Lease を除いたサブセットを内部ストア契約として固定する

## コンテキスト

[[0001]] の前提 6・7 により、状態ストアは検証ゲート（G0〜G2）を通じて性能・規模を比較し、実装を差し替えられる構造にする必要がある。[[0005]] で初期ストアに選ぶ etcd から G2 で別実装へ比較・移行する際、上位の API サーバやコントローラが特定クライアント API に密結合していると差し替えができない。

また [[0008]] でノード死活を合意ストア外のソフトステートに置いたため、TTL つきの鍵や Lease は不要となる。一方で、[[0007]] の watch キャッシュが更新のない静かなプレフィックスでも最新リビジョンへ追いついたと判定するには、`docs/research/draft_20261117_001.md` §5.2 のインターフェース案に欠けていた watch の進捗通知操作を契約に含める必要がある。

## 決定

1. 内部のストア契約 `Store` を etcd v3 API の固定サブセット（`Get`、`Range`、`Txn`、`Watch`、`CurrentRev`、`Compact`、および watch の進捗通知）に限定する。
2. Lease および TTL つきキーは契約に含めない。
3. すべてのストア実装は同一の契約テスト、[[0003]] に基づく Lean 4 抽象モデルとの差分テスト（DRT）、および線形化可能性検査を通過しなければならない。etcd 本体は信頼の根として扱う。

```go
type Store interface {
	Get(ctx context.Context, key string, atRev Rev) (KV, Rev, error) // atRev=0 は quorum
	Range(ctx context.Context, prefix string, atRev Rev, limit int, cont string) ([]KV, Rev, string, error)
	Txn(ctx context.Context, cmps []Cmp, ops []Op) (bool, Rev, error) // modRev の CAS
	Watch(ctx context.Context, prefix string, fromRev Rev) (WatchStream, error)
	CurrentRev(ctx context.Context) (Rev, error) // 一貫読み取りの前提
	Compact(ctx context.Context, rev Rev) error
}

type WatchStream interface {
	Events() <-chan Event // Put / Delete に加えて進捗通知種別（Progress）を含む
	RequestProgress(ctx context.Context) error
}
```

`WatchStream` および進捗イベント種別の正確な Go 型定義は実装時に確定する。

## 根拠（調査結果・出典）

- 内部不変条件：`mod_revision` の比較と同一 `Txn` での原子的更新が揃えば、[[0013]] の葉パス所有権更新および [[0010]] の逆参照索引更新をオブジェクト本体と一貫して記録できる。
- etcd v3 API 仕様（ https://etcd.io/docs/v3.5/learning/api/ ）：`KV.Range`（`revision=0` で線形化可能な最新読み取り、各レスポンスヘッダに `revision`）、`KV.Txn`（`mod_revision` 等の比較）、`Watch`（`start_revision`、`progress_notify`、コンパクション済みリビジョン指定時の `compact_revision` 通知）、`KV.Compact` が存在することを確認した。
- KEP-2340（ https://github.com/kubernetes/enhancements/blob/master/keps/sig-api-machinery/2340-consistent-reads-from-cache/README.md ）：キャッシュからの一貫読み取りには書込みのない区間でキャッシュを進めるための `RequestProgress`（etcd >= 3.4.31 または >= 3.5.13 が必要）が不可欠であることを確認した。
- `k8s-1m`（ https://github.com/bchess/k8s-1m ）：etcd を `mem_etcd` に置き換える際、条件付き `Txn-Put` 中心の最小サブセットに契約を絞る設計を確認した（ただし同事例は 100 万ノード向けに約 100k Lease writes/s を前提としており、Lease 除外は Nayutar 固有の判断である）。
- `kine`（ https://github.com/k3s-io/kine ）：SQL バックエンド上で etcd v3 gRPC API のサブセット（`KV Put/Get/Delete/Compact/Txn`、`Watch`、`RequestProgress`、`Lease Grant` 等）を互換境界として実装していることを確認した。
- 線形化可能性検査：Go 向けの履歴検査ツールとして `porcupine`（ https://github.com/anishathalye/porcupine ）が利用できることを確認した。

## 検討した代替案

- **契約を設けず etcd クライアント API 全体を直接使う**：初期実装は速いが、呼び出し側が Lease・Member・Auth 等に依存してしまい、G2 でのストア比較（[[0005]]）や Lean モデルとの DRT（[[0003]]）が困難になるため採らない。
- **etcd と無関係な独自 KV 契約を定義する**：初期実装の etcd v3.6 との間に厚い変換層が要り、G0 の立ち上げ速度と信頼の根としての etcd の利点を損なうため採らない。
- **契約に Lease（TTL つきキー）を含める**：kine や k8s-1m と同型になるが、死活監視を [[0008]] で合意ストアから外したため不要な TTL 状態管理とテスト対象を増やすだけになり、採らない。
- **FoundationDB 型の汎用トランザクション契約にする**：1 トランザクション最大 10,000,000 バイト（1 MB 未満推奨）・5 秒超で `transaction_too_old` となる制約（ https://apple.github.io/foundationdb/known-limitations.html ）や単一キー watch のみの前提が上位契約に漏れ、etcd 側の実装が不自然になるため採らない。

## 影響とトレードオフ

- **得るもの**：[[0005]] の初期 etcd から G2 の候補比較まで呼び出し側を変更せずに差し替えられる。契約が小さいため Lean 抽象モデルと DRT・線形化可能性検査の網羅が容易になる。
- **失うもの**：TTL 自動失効や任意のマルチキー結合はストア外（[[0008]] のソフトステート集約や [[0013]] の同一 `Txn` 設計）で引き受ける必要がある。
- **引き受けるリスク**：`RequestProgress` やコンパクション境界の細かな意味論がバックエンド間でずれ、G2 の候補実装が契約を満たせない恐れがある。契約にない操作が後から要れば契約改訂が必要になる。
- **今後の検証ゲート**：G0 で Lean 4 ストア契約モデルに対する DRT 1,000 ケース以上と線形化可能性検査の通過を検証する。G2 で候補実装すべてに同一契約テストを課し、満たせない実装や契約の不足が見つかれば本 ADR を再検討する。

## 関連ADR

- [[0001]]：機械判定可能なオラクル（前提 6）および検証ゲート単位の進行（前提 7）を定める前提 ADR。
- [[0003]]：本契約の Go 実装を Lean 抽象モデルとの DRT および線形化可能性検査で検証する方針を定める。
- [[0005]]：本契約を満たす初期ストア（単一 etcd クラスタ）および G2 比較候補を定める。
- [[0007]]：本契約の `CurrentRev` と `RequestProgress` を用いて一貫読み取りと watch キャッシュを実装する。
- [[0008]]：ノード死活を合意ストア外のソフトステートに置くことで、本契約から Lease を除外する前提を与える。
- [[0010]]：逆参照索引の更新をオブジェクト本体と同一の `Txn` で実行する。
- [[0013]]：葉パス所有権の更新をオブジェクト本体と同一の `Txn` で実行する。
