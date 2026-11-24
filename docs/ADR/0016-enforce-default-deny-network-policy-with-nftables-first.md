---
title: 既定拒否のネットワークポリシーはまず nftables で強制し、G2 で eBPF と比較する
status: accepted
date: 2026-11-17
depends-on: ["0001"]
---

# 0016: 既定拒否のネットワークポリシーはまず nftables で強制し、G2 で eBPF と比較する

## コンテキスト

Nayutar は [[0006]] のとおりテナントを最上位キーかつ隔離単位としており、許可されていないテナント越境通信を既定で遮断するネットワーク隔離が不可欠である。隔離単位をテナントに置く以上、API 面だけでなく実行面でも既定拒否（default-deny）を前提にする必要がある。

一方で [[0001]] は既存のデータプレーン資産を活かす前提（前提 4）と、段階ごとに検証ゲートを設けて設計判断を確定する前提（前提 7）を置いている。パケットフィルタ基盤には `nftables` と eBPF（`cilium/ebpf` による TC / cgroup フック）があるが、初期段階から eBPF や外部制御面に踏み込むと、カーネル・BTF 依存や検証範囲が膨らむ。まず単純で確実な機構で既定拒否を成立させ、大規模検証の段階で eBPF と実測比較する段取りが必要である。

## 決定

1. G1 までの既定拒否ネットワークポリシーは `nftables` で強制する。
2. `nftables` では `policy drop` を設定できるベースチェイン（フックに接続されたチェイン）に `policy drop` を置き、そこから許可ルールを保持するテナントごとの通常チェインへ `jump` する構造を採る。
3. Cilium の非 Kubernetes 経路は詳細が未確認であるため依存しない。
4. G2 で `cilium/ebpf`（TC / cgroup フック）との比較評価を行い、後続の ADR でデータプレーンを最終決定する。

以下の `nftables` 設定は上記構造を示すための例示であり、一致条件の詳細は実装時に定める。

```nft
table inet nayutar {
  chain forward {
    type filter hook forward priority 0; policy drop;
    ip saddr 10.10.0.0/16 jump tenant_a
  }
  chain tenant_a {
    ip daddr 10.10.0.0/16 accept
  }
}
```

## 根拠（調査結果・出典）

- 内部前提との整合：[[0001]] の前提 4・7 に従い、Linux カーネル標準の `nftables` でまず最小構成の既定拒否を実装し、G2 で性能と運用性を比較した上で後続を判断する。これにより [[0006]] が定めるテナント境界をデータプレーンでも担保する。
- `nftables` のチェイン・原子的置換・ブリッジ境界の仕様：`nftables`（最新版 `1.1.7`、Ubuntu 24.04 標準 `1.0.9`、`GPL-2.0-only`）では `policy`（`accept` / `drop`）はフックに接続されたベースチェインにのみ設定でき、通常チェインには `policy` が存在せず `jump` 未一致時は呼び出し元ベースチェインへ戻ることを一次資料で確認した（https://wiki.nftables.org/wiki-nftables/index.php/Configuring_chains 、https://www.netfilter.org/projects/nftables/manpage.html ）。また単一 `nft -f -` バッチによるテーブル再構築がリーク窓ゼロで原子適用されること（https://wiki.nftables.org/wiki-nftables/index.php/Atomic_rule_replacement ）を確認した。同一 Linux ブリッジ内の L2 通信が `br_netfilter` 無しでは `NF_INET_FORWARD` を通過しないことは、引用できる一次資料に明記が無く、[[0033]] と [[0034]] の実測で確かめた（フックの配置は https://wiki.nftables.org/wiki-nftables/index.php/Netfilter_hooks 、`bridge-nf-call-iptables` は https://docs.kernel.org/networking/ip-sysctl.html ）。このためテナント別ブリッジ＋非重複 CIDR の L3 ルーティング構成（[[0033]]）で `inet forward` に通す。
- eBPF との比較材料：`cilium/ebpf` の最新リリースが 2026-11-24 時点でも v0.22.0（MIT）であることを一次資料で確認した（https://pkg.go.dev/github.com/cilium/ebpf?tab=versions ）。本 ADR では BTF 等の環境依存や検証コストのリスクを見込み、G2 で実測比較する。

## 検討した代替案

- **最初から eBPF（`cilium/ebpf`）だけで実装する**：TC / cgroup フックにより高スループットやソケット層の制御を狙える。しかし G1 の段階から BTF やカーネル版依存の検証負担を負うことになり、比較基準もないため採らない。
- **Cilium をそのままポリシーエンジンとして組み込む**：L3/L4/L7 にわたる既存の機能群を利用できる。しかし Kubernetes 前提の制御面や運用複雑性を取り込む恐れがあるため採らない。
- **`iptables` を使う**：既存の事例やツールが多い。しかし `nft -f -` による単一 Netlink トランザクションでのテーブル原子置換や IPv4/IPv6 統合 `inet` ファミリ・テナント別チェイン構成は `nftables` のほうが明確に表せるため採らない。
- **G2 までポリシーを実装せず既定許可で進める**：G1 のデータプレーン実装が軽くなる。しかしテナント隔離を中核とする設計に反し、越境通信の否定試験を早期に回せないため採らない。

## 影響とトレードオフ

- **得るもの**：外部の重いデータプレーン制御面に依存せず、G1 の時点からテナント間の既定拒否を検証可能な形で強制できる。G2 で eBPF と公平に比較するための基準線も確保できる。
- **失うもの**：cgroup フック等によるソケット層での最適化や、大規模ルール集合における eBPF 特有の高速化は G2 の比較完了まで得られない。
- **引き受けるリスク**：テナント数とエンドポイント数の増加により、`nftables` のルール更新時間やパケット処理遅延が G1 後半〜G2 でボトルネックになる可能性がある。また G2 で eBPF へ切り替える場合、ポリシー適用部の書き換えコストが発生する。
- **今後の検証ゲート**：G1 でテナント越境通信が既定で拒否される否定試験を通し、G2 で `nftables` と `cilium/ebpf` v0.22.0（TC / cgroup フック）の性能・更新遅延・保守負荷を比較する（検証時期 G1・G2 の割り当ては本 ADR の初期値）。G1 の段階で `nftables` の規模・性能が要件を満たさない場合、または G2 の比較結果が eBPF 移行を裏付けた場合に、後続 ADR で切り替える。

## 関連ADR

- [[0001]]：既存資産の活用（前提 4）および検証ゲート単位の段階的判断（前提 7）を定めた前提 ADR。
- [[0006]]：テナントを最上位キーかつ隔離単位とした決定であり、本 ADR はその隔離境界をデータプレーンで強制する。
- [[0033]]：本決定の初期実現方式（純粋ポリシー核 `core/netpol` と Lean 10 定理・DRT、テナント別ブリッジ、`os/exec` による `nft -f -` 原子バッチ、CI `netpol-nftables` 結合試験）を定める ADR。
- [[0034]]：[[0033]] の実データプレーン上の穴（送信元の詐称・同一ブリッジ内の L2 転送・ホスト宛の通信）を塞ぐ ADR。
