---
title: ノードの死活情報はソフトステートとして保持し、確定した状態遷移だけをストアに書く
status: accepted
date: 2026-11-17
depends-on: ["0004"]
---

# 0008: ノードの死活情報はソフトステートとして保持し、確定した状態遷移だけをストアに書く

## コンテキスト

Kubernetes では各 kubelet が 10 秒ごとに API サーバ経由で `kube-node-lease` の Lease オブジェクトを更新するため、規模が拡大すると心拍（heartbeat）の書き込みが合意ストアの負荷を支配する。一方、[[0004]] はストア契約を compare-and-swap（`Txn`）中心の etcd v3 API サブセットに固定し、Lease および TTL 付き鍵を持たない設計を採った。そのため Nayutar は、ノードの死活監視を合意ストアの外に出しつつ、部分的なネットワーク分断や監視側の遅延によって誤った `NotReady` 判定と連鎖的なワークロード退避を起こさない仕組みを必要とする。

Kubernetes の既定値は `--node-monitor-grace-period` が 50 秒（v1.32 以降）、ゾーン単位の退避レート制限が `--node-eviction-rate` 0.1/s・`--secondary-node-eviction-rate` 0.01/s（`--unhealthy-zone-threshold` 0.55、`--large-cluster-size-threshold` 50）、さらに `DefaultTolerationSeconds` による `not-ready` / `unreachable` の退避猶予が 300 秒である。本決定はこれらに相当する死活判定・誤検知抑制・退避猶予の初期構成を定める。

## 決定

ノードの死活監視は死活集約器（`lk live`）上で次のとおり行う。以下の数値はすべて検証ゲートで調整する初期値とする。

1. 心拍は合意ストアに一切書かず、集約器のメモリ上のソフトステートとして保持する。
2. 各ノードはハッシュで決まる 3 台の集約器へ、1 秒ごと（初期値）に心拍を送る。
3. 各集約器は φ accrual 障害検知器（閾値 `φ=8` を初期値とする）で疑いを出し、別の集約器 2 台と対象ノードと同一ラックのノード 2 台（いずれも初期値）へ SWIM の `ping-req` 相当の間接確認を依頼する。
4. 3 台のうち 2 台以上の集約器が疑いを出し、間接確認が 1 つも成功せず、かつ中継したノードから失敗の報告が届いた場合にのみ `NotReady` を確定し、確定した状態遷移だけを CAS（`Txn`）で合意ストアへ書く。応答の無い中継者は、失敗の報告として数えない。中継を頼めるノードが 1 台も無いときに限り、集約器からの失敗の報告で代える。
5. 集約器が自身の担当ノードの 30% 以上（初期値）を同時に疑っているあいだは、自身側の障害を疑う。担当ノードが少ないときにこの割合をどう数えるかは、実現方式の ADR が定める。そのあいだと、そのあいだに開いていた確認については、ノードから失敗の報告が届いた場合にだけ `NotReady` を確定し、集約器からの報告だけでは確定しない。自身側の障害を疑っているあいだは、退避の許可も出さない。`NotReady` の確定そのものには、クラスタ全体のレート制限をかけない。
6. `NotReady` の確定とワークロードの退避は分離して判断する。退避は、確定から既定で 5 分（初期値）の猶予を置いたうえで、クラスタ全体で 1 分あたり全ノードの 1% まで（初期値）に制限して許可する。
7. SWIM の gossip は集約器間（数十〜数百台規模）に限定して使う。

## 根拠（調査結果・出典）

- 内部契約との整合：[[0004]] でストア契約から Lease と TTL 付き鍵を除外したため、定常的な心拍を合意ストアに書かない設計が必須となる。状態遷移の確定だけを CAS で書き込むことで、合意層の書き込み負荷をノード数ではなく障害発生率に比例させられる（`docs/research/draft_20261117_001.md` §5.2）。
- Kubernetes の心拍と大規模負荷：kubelet が `kube-node-lease` の Lease を 10 秒ごとに更新すること、`--node-monitor-grace-period` の既定値が v1.32 以降 50 秒であること、および退避レート制限パラメータ（0.1/s、0.01/s、0.55、50）を一次資料で確認した（https://kubernetes.io/docs/concepts/architecture/nodes/ 、https://kubernetes.io/docs/reference/command-line-tools-reference/kube-controller-manager/ ）。また `DefaultTolerationSeconds` が 300 秒の toleration を付与することを確認した（https://kubernetes.io/docs/reference/access-authn-authz/admission-controllers/ ）。`k8s-1m` では 100 万ノード規模で Lease 更新が毎秒約 10 万件に達し、etcd をインメモリの `mem_etcd` に置き換えたことを確認した（https://github.com/bchess/k8s-1m ）。
- φ accrual と間接確認・自己疑い：Hayashibara ら（SRDS 2004, https://doi.org/10.1109/RELDIS.2004.1353004 ）の φ accrual 障害検知器は連続値 φ で疑いの度合いを表し、Akka の既定閾値 8.0（https://doc.akka.io/libraries/akka-core/current/typed/failure-detector.html ）と Cassandra の `phi_convict_threshold` 既定値 8（https://cassandra.apache.org/doc/latest/cassandra/configuration/cass_yaml_file.html ）を確認した。また SWIM（Das ら、DSN 2002, https://doi.org/10.1109/DSN.2002.1028914 ）の `ping-req` による間接プローブと疑いプロトコル、および Lifeguard（https://arxiv.org/abs/1707.00788 、https://github.com/hashicorp/memberlist ）のローカル健全性認識による自己抑制を確認した。1 秒・3 台・φ=8・間接確認 2+2・30% 停止・1%/分制限・5 分猶予は初期値である。
- レート制限の対象：Kubernetes のノードライフサイクルコントローラは、ハートビートが途絶えたノードの `Ready` 条件を `Unknown` にする更新と `NoSchedule` の taint にはレート制限をかけず、`NoExecute` の taint（退避）だけをゾーンごとのトークンバケットで制限する。制限の状態はコントローラのメモリにあり、etcd には無い（https://github.com/kubernetes/kubernetes/blob/master/pkg/controller/nodelifecycle/node_lifecycle_controller.go 、https://kubernetes.io/docs/concepts/architecture/nodes/ ）。`NotReady` の確定を制限すると、相関した実障害（ラックの停止など）で記録が遅れ、そのあいだスケジューラが停止したノードを `Ready` と見る。そのため制限は退避の許可にかける。

## 検討した代替案

- **Kubernetes と同様に Lease オブジェクトを合意ストアに書く**：ストア契約が単純で集約器の状態喪失も起きない。しかしノード数に比例した書き込みが合意層を圧迫し、[[0004]] の Lease 非採用とも反するため採らない。
- **全ノード間で SWIM gossip を行う**：階層となる集約器が不要になる。しかし数万ノード規模では背景トラフィックと障害伝播時間が膨らみ、誰が確定遷移を CAS で書くかの調停も複雑になるため採らない。
- **ノードあたり単一の集約器にだけ心拍を送る**：通信量と集約器の計算量が最小になる。しかし集約器 1 台の停止や片方向分断で誤 `NotReady` や検知漏れが起きやすいため採らない。
- **すべての心拍を別のホットストアへ書き込む**：合意ストアから分離しつつ心拍履歴を永続化できる。しかし 1 秒ごとの全ノード書き込みをディスク（WAL）に流す負担が残り、死活判定に永続履歴は不要なため採らない。

## 影響とトレードオフ

- **得るもの**：合意ストアへの定常的な心拍書き込みがゼロになり、クラスタ規模が増えても合意層の負荷を抑えられる。複数集約器・間接確認・自己疑いにより、部分分断や集約器側の過負荷による誤 `NotReady` を抑えられる。退避の猶予とレート制限により、誤 `NotReady` が残っても連鎖的な退避を抑えられる。相関した実障害でも、レート制限のために `NotReady` の記録が遅れることは無い。集約器と制御面がノードの網から切り離された場合は、失敗を報告できるノードがいないので、`NotReady` を確定しない。
- **失うもの**：心拍履歴をメモリ上にしか持たないため、集約器再起動直後は φ 計算の標本が失われ、安定した検知までに再学習の時間を要する。
- **引き受けるリスク**：φ 閾値や間接確認条件が厳しすぎると停止検知が遅れ、緩すぎると誤 `NotReady` と連鎖退避が起きる。`NotReady` の確定にレート制限が無いので、系統的な誤検知が起きると `NotReady` の記録が一度に増える（退避は猶予と制限で遅らせる）。また、初期値とした各定数が実負荷や時計ずれ下で妥当かは検証前である。
- **今後の検証ゲート**：G1（`docs/research/draft_20261117_001.md` §8.2）で検証する。[[0015]] のシミュレータでの 10,000 シード（集約器の分断・停止、時計ずれ ±500ms、損失 5%）と、`lk-fakenode` 等を用いた模擬・実機 100 ノードの 72 時間障害注入で、誤 `NotReady` 0 件かつ停止から確定までの p99 が 30 秒以内であることを合格条件とする。誤 `NotReady` が残る場合は、確定状態だけをホット層に書く方式へ戻し、本決定を再検討する。

## 関連ADR

- [[0004]]：ストア契約から Lease と TTL 付き鍵を除いた前提であり、心拍を合意ストア外に置く直接の根拠。
- [[0015]]：決定論的シミュレーションの入出力抽象を定める ADR であり、G1 の 10,000 シード障害注入試験の基盤。
