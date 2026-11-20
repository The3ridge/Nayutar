---
title: CRI を経由せず containerd の Go クライアントをノードエージェントから直接駆動する
status: proposed
date: 2026-11-17
depends-on: ["0001"]
---

# 0014: CRI を経由せず containerd の Go クライアントをノードエージェントから直接駆動する

## コンテキスト

[[0001]] の前提 4 は OCI・CNI・CSI・containerd などの既存資産を活かしてデータプレーンを自前で作り直さないことを定め、前提 5 は Kubernetes のつらさを引き受けないことを定めている。Kubernetes では kubelet が CRI（Container Runtime Interface）を介してランタイムを操作するが、CRI は `PodSandbox` 抽象を中心に据えた kubelet 専用の境界であり、Nayutar のワークロード意味論と 1 対 1 に対応しない。

CRI を経由すると、独自モデルから `PodSandbox` への変換層と Kubernetes 由来の制約をノードエージェント側に抱え込むことになる。一方で containerd を直接使う場合でも、約 4 か月ごとにマイナー版が出る追従負担を抑え、初期段階の実装範囲と後回しにする機能を明確に区切る必要がある。

## 決定

1. ノードエージェント `lk-node` は CRI を使用せず、containerd の Go クライアント（`github.com/containerd/containerd/v2/client`）を通じて containerd を直接駆動する。
2. 依存する containerd は LTS 系列である 2.3 系に固定する。
3. サンドボックスおよびネットワーク名前空間（netns）の管理は `lk-node` 自身が行い、CNI プラグインは exec で直接呼び出し、CSI Node サービスは gRPC で直接呼び出す。
4. 初期段階の最小実装は `runc` を用いた単一コンテナの起動・停止・再起動・終了コード報告、および CNI の `bridge` と `host-local` に限定する。
5. `runwasi`、Kata Containers、Firecracker、Cloud Hypervisor（いずれも適合性は未確認である）、User Namespaces、およびコンテナの in-place 更新は後回しとする。

## 根拠（調査結果・出典）

- 内部前提との整合：[[0001]] の前提 4 に従いコンテナ実行・ネットワーク・ストレージのデータプレーンには containerd・CNI・CSI を再利用しつつ、前提 5 に従い kubelet の Pod 抽象への追従は避ける（`docs/research/draft_20261117_001.md` §5.4）。また [[0002]] により中核実装を Go に統一しているため、containerd の Go クライアントをそのまま組み込める。
- CRI の役割：CRI は kubelet がクラスタコンポーネントを再コンパイルせずに多様なコンテナランタイムを使うためのプラグインインターフェースであり、`RuntimeService`（`PodSandbox` とコンテナのライフサイクル）および `ImageService` を定義していることを一次資料で確認した（https://kubernetes.io/docs/concepts/architecture/cri/ ）。
- containerd の版と Go クライアント：containerd は最新が v2.4.2 であり、v2.3 系が LTS ライン（v2.3.7 が 2026-10-13 公開、2028-04-30 までサポート）であること、約 4 か月ごとにマイナー版が出荷されることを一次資料で確認した（https://github.com/containerd/containerd/blob/main/RELEASES.md ）。また `github.com/containerd/containerd/v2/client` が containerd を直接駆動する公式手順として案内されていることを確認した（https://github.com/containerd/containerd/blob/main/docs/getting-started.md ）。
- CNI・CSI・User Namespaces：CNI プラグインは環境変数（`CNI_COMMAND` として `ADD`/`DEL`/`CHECK`/`GC`/`VERSION`、`CNI_CONTAINERID`、`CNI_NETNS`、`CNI_IFNAME`、`CNI_PATH`）と標準入力 JSON で呼ぶ実行ファイルであり、構成リストの `plugins` 配列を `ADD` 時は正順、`DEL` 時は逆順で連鎖することを一次資料で確認した（https://github.com/containernetworking/cni/blob/main/SPEC.md ）。CSI は Identity・Controller・Node の gRPC サービスを定義し、Kubernetes の CSI サイドカー群は Kubernetes API オブジェクトを watch するため再利用できないことを一次資料で確認した（https://github.com/container-storage-interface/spec ）。なお Nomad がサイドカーなしで CSI を直接呼ぶ設計の具体的教訓は未確認である。また Kubernetes の Pod User Namespaces（`hostUsers: false`）は v1.36 で GA となっている（`docs/research/draft_20261117_001.md` §2.4 の「v1.37 で Beta」は誤り）ことを確認しているが、初期範囲には含めない。

## 検討した代替案

- **CRI クライアントを実装し containerd の CRI プラグインや CRI-O を使う**：サンドボックスと CNI 呼び出しをランタイム側へ委譲できる。しかし Nayutar のモデルを `PodSandbox` 意味論へ無理に写像する必要が生じ、[[0001]] の前提 5 に反するため採らない。
- **kubelet を組み込む・流用する**：既存のコンテナ実行や CNI・CSI 連携をそのまま使える。しかし kubelet は Kubernetes API と Pod モデルに密結合しており、不要な複雑さと追従義務を引き受けるため採らない。
- **containerd を介さず `runc` を直接操作する**：ランタイム依存を 1 層減らせる。しかしイメージの取得・展開・スナップショット管理を自前実装することになり、[[0001]] の前提 4 に反するため採らない。
- **Podman や Quadlet を使う**：systemd 連携や rootless 実行を借りられる。しかし実行面の制御モデルが Podman/systemd に強く依存し、オーケストレータから CNI・CSI・シム層を統一的に制御しにくいため採らない（詳細の比較検証は未確認である）。

## 影響とトレードオフ

- **得るもの**：`PodSandbox` 抽象に縛られず、Nayutar のワークロード意味論に合わせて containerd・CNI・CSI を直接制御できる。containerd を 2.3 LTS に固定することで、約 4 か月ごとに出るマイナー版への追従負担を抑えられる。
- **失うもの**：CRI プラグインが担っていたサンドボックス生成、netns のライフサイクル管理、CNI 呼び出し順序制御を `lk-node` で自前実装・保守する手間が増える。
- **引き受けるリスク**：netns やプロセスの後始末漏れ、および CRI 実装が過去に解決してきたエッジケースを踏み直すリスクがある。`runwasi`・Kata・Firecracker・Cloud Hypervisor は未確認のまま先送りしているため、将来ランタイムを多様化する際に抽象境界の修正が必要になる可能性がある。
- **今後の検証ゲート**：G0〜G1（`docs/research/draft_20261117_001.md` §5.4・§7.2・§8.2）で検証する。[[0003]] に基づき `kind` 上の Kubernetes を差分オラクルとして、コンテナの状態遷移列・再起動回数・環境変数・マウント構成を比較する。サンドボックスおよび netns 管理の不具合や保守負荷が過大であると判明した場合は、CRI 採用の可否を再検討する。

## 関連ADR

- [[0001]]：OCI・CNI・CSI・containerd の再利用（前提 4）と Kubernetes のつらさを引き受けない線引き（前提 5）を定めた前提 ADR。
- [[0002]]：中核の実装言語を Go に固定した決定であり、containerd の Go クライアントを直接利用する前提。
- [[0003]]：`kind` 等を差分オラクルにして実装の正しさを検証する方針であり、`lk-node` の状態遷移・マウント比較の根拠。
