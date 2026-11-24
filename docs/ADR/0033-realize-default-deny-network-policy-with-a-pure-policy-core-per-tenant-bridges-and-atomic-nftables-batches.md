---
title: 既定拒否ネットワークポリシーを、純粋なポリシー核・テナント別ブリッジ・原子的 nftables バッチで実現する
status: accepted
date: 2026-11-24
depends-on: ["0001", "0002", "0003", "0006", "0009", "0011", "0014", "0015", "0016", "0017", "0022", "0025", "0027", "0028", "0031", "0032"]
---

# 0033: 既定拒否ネットワークポリシーを、純粋なポリシー核・テナント別ブリッジ・原子的 nftables バッチで実現する

## コンテキスト

[[0016]] は、(1) G1 までの既定拒否ネットワークポリシーを `nftables` で強制すること、(2) フックに接続されたベースチェインに `policy drop` を置き、そこから許可ルールを保持するテナントごとの通常チェインへ `jump` する構造を採ること、(3) Cilium の非 Kubernetes 経路に依存しないこと、(4) G2 で `cilium/ebpf` と比較評価すること、を定めた。

[[0016]] を [[0006]]（テナント隔離）・[[0011]] / [[0028]]（順序付きレコード集合）・[[0027]]（LKAL v0.2 スキーマ）・[[0031]]（containerd と CNI `bridge` 直接駆動）・[[0032]]（シミュレーション境界）と整合する実装と形式検証に落とすにあたり、一次資料の調査から次の技術的課題を解決する必要がある。

- **単一 Linux ブリッジと `table inet ... hook forward` の乖離**：[[0031]] の既定構成はコンテナを単一の Linux ブリッジ（`nayutar0`）へ接続する。しかし Linux カーネルの Netfilter 仕様上、同一ブリッジ上のポート間 L2 転送（`NF_BR_FORWARD`）は `br_netfilter`（`net.bridge.bridge-nf-call-iptables=1`）を有効にしない限りホストの `table inet ... hook forward`（`NF_INET_FORWARD`）を一切通過しない。さらに `br_netfilter` はブリッジ内の転送を IP 層のフックにも通す設定であり、同じネットワーク名前空間の他のブリッジの挙動まで変えるため、`br_netfilter` に依存せずテナント越境通信を確実にホスト L3 `inet forward` に通すデータプレーン構成が不可欠である。
- **`jump` のフォールスルーと片側許可によるテナント越境の防止**：`nftables` の `jump` は呼び出し先チェインで終端判定に一致しないときベースチェインの次ルールへ戻るため、テナント間の CIDR 重複があると意図しないチェインへ評価が漏れる。またテナント A が外部向けに `0.0.0.0/0` への送信（egress）を許可しただけでテナント B へ到達できてしまうと、テナント B の既定拒否境界が破られる。
- **パケットリーク窓と削除済みチェイン残留のない原子的更新**：`flush ruleset` はホスト上の無関係なテーブルまで破壊し、`flush table` は削除済みテナントの空チェインをカーネルに残留させ、単体の `delete table` は初回起動時（テーブル未存在時）に `ENOENT` で失敗する。リーク窓ゼロかつ冪等な単一バッチ置換手順を固定する必要がある。
- **外部 Go 依存とライセンス境界**：`nftables` ユーザー空間（`GPL-2.0-only`）との CGo リンクは避け、また非公式 Netlink ABI と過去のバイト順脆弱性（`CVE-2024-6284`）を抱えるサードパーティ Netlink ライブラリを増やさずに駆動する必要がある。

## 決定

1. **テナント別 Linux ブリッジと非重複テナント CIDR により、`br_netfilter` 非依存で全テナント越境通信をホスト L3 `inet forward` に通す。**
   - テナント名（[[0022]] の小文字 RFC 1123 ラベル）から決定論的ブリッジ名 `nbr-<sha256(tenant)[:11]>`（15 文字、Linux `IFNAMSIZ` 上限内）を導出する。`core/node` の `node.schema.lkal` に任意の `unit.tenant` 欄と `netpol` 設定を追加し、`unit.tenant` 指定時（かつ `cni.bridge` 未明示時）はテナント専用ブリッジへ接続する。
   - 各テナントには互いに素（非重複）な正準 IPv4 / IPv6 CIDR を割り当て、CNI `bridge` をゲートウェイ（`isGateway: true`）として構成する。これにより異なるテナント間の通信は必ずホストの L3 ルーティングを通過し、`br_netfilter` 無しで `table inet nayutar` の `chain forward` によって捕捉される。

2. **純粋なポリシー検査・評価・`nftables` コンパイラ核を `core/netpol` に置く。**
   - **テナント束と非重複検査**：`PolicyBundle` はテナントごとの `CIDRs`（ホストビット 0 の正準 IPv4 / IPv6 プレフィックス）・同一テナント内既定動作 `IntraTenant`（既定 `.allow`、`.deny` へ切替可）・順序付き許可ルール集合 `Rules`（必須の整数 `order` 葉で昇順整列、[[0028]]）を持つ。コンパイラはテナント名文法、CIDR 正準性、同一テナント内および異テナント間の CIDR 完全非重複、相手テナント存在、ポート範囲（`1..65535`、`tcp` / `udp` のみ許可）を検査し、違反時は fail-closed で拒否する。
   - **双方向合意によるテナント隔離モデル**：`ct state invalid` は無条件 `drop`、`ct state { established, related }` は `accept` とする。新規接続（`.new`）について、同一テナント内は `IntraTenant = .allow` なら許可（`.deny` なら送信側の `egress` 許可と受信側の `ingress` 許可の双方を要求）、**異なるテナント $A \to B$ 間の越境通信は $A$ の `egress` 許可ルールと $B$ の `ingress` 許可ルールの双方が一致したときのみ許可**し、片側のみの許可（例：$A$ の `0.0.0.0/0` egress 許可）では必ず拒否する。テナント→外部 CIDR は送信テナントの `egress` 許可、外部 CIDR→テナントは受信テナントの `ingress` 許可を要求し、外部→外部の中継は常に拒否する。
   - **`jump` による 2 段チェイン構造**：[[0016]] 決定 2 に従い、`table inet nayutar` のベースチェイン `forward`（`type filter hook forward priority 0; policy drop;`）から各テナントの通常チェインへ `jump` する。
     - 第 1 段（送信検査）：送信元がテナント $T$ の CIDR に一致するとき `jump tenant_<T>` する。`tenant_<T>` は `IntraTenant = .allow` なら自テナント宛を `accept` し、一致する `egress` ルールがあれば `return` でベースチェインへ復帰し、末尾の無条件 `drop` で未許可送信を即座に遮断する。
     - 第 2 段（受信検査）：宛先がテナント $U$ の CIDR に一致するとき `jump tenant_<U>_in` する。`tenant_<U>_in` は一致する `ingress` ルールがあれば `accept` し、末尾の無条件 `drop` で未許可着信を遮断する。
     - 第 3 段（外部宛送信完了）：第 1 段の `return` と第 2 段（宛先がいずれのテナントでもない）を通過したテナント送信元パケットを `accept` し、それ以外はベースチェインの `policy drop` で破棄する。テナント名は `_` を含まない `[a-z0-9-]` であるため、`"forward"`・`"tenant_<T>"`・`"tenant_<T>_in"` は全単射で決して衝突しない。

3. **LKAL v0.2 スキーマ（`netpol.schema.lkal`）と `os/exec` による原子的 `nft -f -` 実行器を実装する。**
   - 埋め込みスキーマ `core/netpol/netpol.schema.lkal`（[[0027]]）でスタンドアロンのポリシー束と `core/resource` 向け `KindSchema`（グループ `"net.nayutar"`、kind `"networkpolicy"`、[[0025]]・[[0028]]）の双方を提供する。[[0010]] のテナント内参照制約と衝突しないよう、相手テナント識別子は `.ref` ではなく検証済み `.string` とする。
   - Go からはサードパーティ Netlink 依存を追加せず、`NftRunner` インターフェース（[[0032]]）の背後に隔離した実環境アダプタが、シンボリックリンク解決と実行可能通常ファイル検査を経た `nft` バイナリ（既定 `/usr/sbin/nft`）を `exec.CommandContext(ctx, nftBin, "-f", "-")` で起動する。
   - 標準入力には単一トランザクションバッチとして `table inet nayutar` → `delete table inet nayutar` → `table inet nayutar { ... }` を渡す。これにより初回起動時の冪等性、削除済みテナントチェインの完全除去、カーネル RCU 世代切替によるパケットリーク窓ゼロ、およびホスト上の他テーブル非干渉（`flush ruleset` 不使用）を同時に満たす。L4 プロトコルのみの一致には `nft(8)` 正準構文の `meta l4proto tcp|udp|icmp|ipv6-icmp` を、ポート範囲付き一致には `tcp|udp dport <port|min-max>` を出力する。

4. **純粋ポリシー核と `nftables` チェインコンパイルを Lean 4 で形式証明し、DRT と CI 実カーネル試験で検証する。**
   - `spec/Nayutar/Netpol.lean` に 32 ビット IPv4 / 128 ビット IPv6 の正準プレフィックス区間演算、ポリシーコンパイラ、宣言的評価器 `evaluate`、およびコンパイル済み `nftables` チェイン機械 `evalNft` を記述し、`nayutar-drt netpol`（`spec/Nayutar/Drt/Netpol.lean` と `core/drt/netpol_drt_test.go`）で Go 実装と 1 対 1 照合する。
   - `spec/Nayutar/Proofs/Netpol.lean`（および保護対象の `Statements/Netpol.lean`・`comparator.json`）で、テナント間 CIDR 非重複の保証、ルール空時の既定拒否、異テナント越境における双方向許可の必要十分性（片側許可での遮断）、`ct state` 優先規則、チェイン名の単射性、入力順非依存の決定性、および **コンパイル済み `nftables` チェイン実行結果と宣言的評価器 `evaluate` の全パケット完全一致定理**を `sorry` なし・許容公理のみで証明する。
   - 通常の `go test ./...` は非特権の模擬ランナーと `testing/synctest` で完結させ（`t.Skip` 不使用）、実 root + Linux `netns` + CNI テナント別ブリッジ + 実 `nftables` による越境遮断・双方向許可疎通・原子的更新試験を `//go:build linux && nftables`（`core/netpol/nftables_linux_test.go`）と CI の専用ジョブ `netpol-nftables`（`ubuntu-24.04`、`br_netfilter` 非依存確認付き）に分離して毎回実行する。

## 根拠（調査結果・出典）

- **`nftables` 公式仕様と原子的バッチ置換**（https://wiki.nftables.org/wiki-nftables/index.php/Configuring_chains 、https://wiki.nftables.org/wiki-nftables/index.php/Atomic_rule_replacement 、https://www.netfilter.org/projects/nftables/manpage.html 、2026-11-24 確認）：
  - ベースチェインのみが `policy`（`accept` / `drop`）を持ち、`jump` は子チェイン末尾の暗黙 `return` で呼び出し元ベースチェインの次ルールへ戻ることを確認した。したがってテナント CIDR の非重複保証と子チェイン末尾の明示 `drop` を組み合わせることで、[[0016]] 決定 2 の `jump` 構造を保ったままフォールスルーを完全に排除できる。
  - `nft -f -` は入力全体を単一 Netlink バッチトランザクションとしてカーネルの世代切替で原子適用する。`table inet nayutar` → `delete table inet nayutar` → `table inet nayutar { ... }` の 3 段構成は引用した wiki には載っておらず、netfilter のメーリングリスト（https://spinics.net/lists/netfilter/msg59426.html ）で使われている慣用句である。手元（2026-11-24、Linux 7.0、`nft` 1.1.6）で、単体の `delete table` がテーブル未存在時に失敗すること、3 段構成が初回・再適用・撤去 2 回のどれでも成功することを実測した。他のカーネル版と `nft` 版での挙動は CI の `netpol-nftables`（Ubuntu 24.04）で確かめる。
- **Linux ブリッジと Netfilter フック境界**（https://docs.kernel.org/networking/ip-sysctl.html 、https://wiki.nftables.org/wiki-nftables/index.php/Netfilter_hooks 、https://wiki.nftables.org/wiki-nftables/index.php/Bridge_filtering 、https://github.com/containernetworking/plugins/blob/main/plugins/main/bridge/README.md 、2026-11-24 確認）：
  - 同一 Linux ブリッジ内の L2 転送が `br_netfilter` 無しでは `NF_INET_FORWARD` を通過しないことは、引用したページに明記されておらず、手元（2026-11-24、Linux 7.0、`bridge-nf-call-iptables=0`）で実測して確かめた。`nftables` の wiki はブリッジ族を `br_netfilter` の代替と位置づけている。テナントごとに CNI `bridge`（`isGateway: true`）と非重複サブネットを分離することで、`br_netfilter` 無しで全テナント越境通信が確実にホスト L3 `inet forward` を通過する。
- **版・Go 統合手段・ライセンス境界**（https://www.netfilter.org/projects/nftables/downloads.html 、https://www.netfilter.org/licensing.html 、https://pkg.go.dev/github.com/google/nftables 、https://github.com/kubernetes-sigs/knftables 、https://pkg.go.dev/github.com/cilium/ebpf?tab=versions 、2026-11-24 確認）：
  - `nftables` 最新版は `1.1.7`（2026-09-01 公開、`GPL-2.0-only`、Ubuntu 24.04 標準は `1.0.9`）、G2 比較対象の `github.com/cilium/ebpf` 最新版は `v0.22.0`（2026-06-29 公開、`MIT`）であることを確認した。
  - `os/exec` で `nft -f -` を外部プロセス実行する方式は新規 Go 依存を増やさず（[[0031]] と整合）、`GPL-2.0-only` のリンク境界も清潔に保てる（Kubernetes `sigs.k8s.io/knftables` Apache-2.0 も同じ理由で CLI 駆動を採用しているが、コードは参照せず `nft(8)` 仕様からクリーンルーム実装する）。
- **手元での確認**（2026-11-24）：
  - `lake build`（Lean `v4.35.0`）で `spec/Nayutar/Netpol.lean`・`spec/Nayutar/Drt/Netpol.lean`・`spec/Nayutar/Proofs/Netpol.lean`（10 定理、`sorry` なし）が警告なしで通り、`#print axioms` で許容公理のみに依存することを確かめた。
  - `nayutar-drt netpol` との DRT（1,000 件・全エラーコードおよび評価バケット・フィールド変異検出・16 種の壊した実装の検出）が一致することを確かめた。
  - `netpol.schema.lkal`・`kind.schema.lkal`・`node.schema.lkal` の LKAL v0.2 検査、`core/netpol`（ファジング・fail-closed 列挙検査含む）・`core/node`・`cmd/lk-node` の `-race` 試験、`-tags nftables` のビルドと静的検査、`golangci-lint`、および `zizmor --pedantic --strict-collection --offline .` がすべて通過することを確かめた。`govulncheck` は Go 1.27.3 の標準ライブラリに `net/http` の GO-2026-6859（Go 1.27.4 で修正）だけを報告し、依存モジュールには検出が無い。

## 検討した代替案

- **単一共有ブリッジのまま `table bridge nayutar` と `table inet nayutar` を二重管理する**：ブリッジが 1 つで済む。しかし ARP（`ether type arp accept`）や IPv6 NDP の手動許可と `CONFIG_NF_CONNTRACK_BRIDGE` が必要になり、L2 と L3 に同一ポリシーを二重展開する複雑さが増すため採らない。
- **各コンテナの `netns` 内に `table inet`（`input` / `output`）を配置する**：L2 / L3 を問わず捕捉できる。しかし [[0016]] 決定 2 のテナント別チェイン `jump` 構造から外れ、ポリシー更新時に全コンテナ `netns` へのファンアウトが必要になり、コンテナ内の `CAP_NET_ADMIN` に対するホスト側防御境界も弱まるため採らない。
- **`github.com/google/nftables`（`v0.3.0`）を `go.mod` に追加して生 Netlink で実装する**：外部コマンド実行が不要になる。しかし非公式な低水準 Netlink 式構築への依存と間接依存 17 パッケージを増やし、過去にバイト順エンコード脆弱性（`CVE-2024-6284`）も発生しているため採らない。

## 影響とトレードオフ

- **得るもの**：`br_netfilter` や外部 CNI 制御面に依存せず、Lean 4 定理と DRT で宣言的ポリシーと `nftables` チェインの意味論一致・片側越境不可・フォールスルー不在を機械保証したうえで、実カーネル上の既定拒否テナント隔離を CI で毎回検証できる。
- **失うもの**：ノード上でアクティブなテナント数分の Linux ブリッジ（`nbr-*`）を作成する。また初期実装はテナント CIDR・相手テナント・L4 プロトコル・ポート範囲を対象とし、ラベルセレクタ単位の細粒度 Pod 間隔離や L7 ポリシーはワークロード Kind と G2 の eBPF 比較（[[0016]] 決定 4）で扱う。
- **引き受けるリスク**：テナント数やルール数が数千規模に増えた際の `nft -f -` バッチ適用時間は、G1 後半〜G2 で `cilium/ebpf` v0.22.0 と実測比較して評価する。
- **今後の検証ゲート**：本 ADR の Lean 保護定理・`nayutar-drt netpol`・CI `netpol-nftables` 実カーネル否定／肯定試験を通過させ、G2 で `cilium/ebpf`（TC / cgroup フック）との性能・更新遅延比較を実施する。

## 関連ADR

- [[0001]]：既存 Linux データプレーン資産の活用（前提 4）、LKAL による宣言（前提 2・3）、検証ゲート単位の判断（前提 7）を定める。
- [[0002]]・[[0003]]：Go 実装と Lean 4 モデル・保護定理・DRT による保証方針を定める。
- [[0006]]・[[0022]]：テナントを最上位の隔離単位とし、テナント名の厳格な文法を定める。
- [[0009]]・[[0025]]：`networkpolicy` Kind スキーマが従う封筒構造を定める。
- [[0011]]・[[0028]]：ポリシー許可ルールの順序付きレコード集合を定める。
- [[0014]]・[[0031]]：テナント別ブリッジを統合する `core/node`・CNI `bridge`・`cmd/lk-node` を定める。
- [[0015]]・[[0032]]：`NftRunner` と単体試験が従うシミュレーション境界を定める。
- [[0034]]：本 ADR の定理が前提にする「送信元アドレスどおりのテナントから `forward` チェインに届く」を実カーネルで成り立たせるガードと、ブリッジ族への写像を定める。
- [[0016]]：本 ADR が実現する `nftables` 既定拒否ネットワークポリシーの親決定。
- [[0017]]：DRT と保護定理検査の枠組みを定める。
- [[0027]]：`netpol.schema.lkal` と拡張 `node.schema.lkal` に適用する LKAL v0.2 スキーマ検証を定める。
