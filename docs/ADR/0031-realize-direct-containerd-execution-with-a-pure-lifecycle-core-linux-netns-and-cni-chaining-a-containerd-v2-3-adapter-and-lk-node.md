---
title: containerd の直接駆動を、純粋なライフサイクル核・Linux netns と CNI 連鎖・containerd v2.3 アダプタ・lk-node で実現する
status: accepted
date: 2026-11-23
depends-on: ["0001", "0002", "0003", "0014", "0015", "0017", "0027"]
---

# 0031: containerd の直接駆動を、純粋なライフサイクル核・Linux netns と CNI 連鎖・containerd v2.3 アダプタ・lk-node で実現する

## コンテキスト

[[0014]] は、CRI と `PodSandbox` 抽象を経由せず、ノードエージェント `lk-node` から containerd v2.3 LTS の Go クライアント（`github.com/containerd/containerd/v2/client`）を通じて `runc` 単一コンテナを直接駆動し、ネットワーク名前空間（netns）の管理と CNI プラグインの `exec` 呼び出しを自前で行い、初期段階の最小実装を単一コンテナの起動・停止・再起動・終了コード報告および CNI の `bridge` と `host-local` に絞る方針を定めた。また [[0015]] は、すべての外部作用をシミュレーション可能なインターフェースの背後に隔離し、通常の単体試験を実ネットワークや実デーモンに依存させないことを求めている。

[[0014]] を安全に動く実装と機械検証に落とすにあたり、一次資料の調査と既存リポジトリ制約から次の課題を解決する必要がある。

- **netns・CNI・タスク／スナップショットの後始末漏れと逆順ロールバック**：CRI プラグインを使わないため、起動途中の部分失敗や停止要求時に `Task` → `Container/Snapshot` → CNI `DEL`（逆順）→ `Netns` の厳密な LIFO 順序を守らないと、孤立プロセス・FIFO・IP アドレス（`host-local`）・マウントポイントがホストに残留する。
- **containerd v2 クライアントの競合条件**：`task.Start` の後に `task.Wait` を呼ぶと短命プロセスの終了イベントを取りこぼす。またイメージ取得・展開からコンテナ生成までに GC リース（`leases.WithLease`）を張らないとバックグラウンド GC に中間スナップショットを回収され得る。
- **新規 Linux netns のループバックと Go スレッド安全性**：`CLONE_NEWNET` 直後の netns は `lo`（`127.0.0.1`）が DOWN のままであるため、`bridge` + `host-local` の前段に CNI `loopback` を連鎖させる必要がある。また Go のゴルーチン上で `unshare(CLONE_NEWNET)` を呼ぶ際は、OS スレッドが他のゴルーチンへ再利用されてホスト通信を壊さないようスレッド破棄を制御しなければならない。
- **CNI 標準出力エラー仕様と設定スキーマ**：CNI 仕様では非ゼロ終了時の構造化エラー JSON は `stderr` ではなく `stdout` に出力され、`host-local` のトップレベル `subnet` は非推奨で `ranges` 形式が正規である。また人間が書く設定面には YAML を使わず LKAL v0.2 スキーマ（[[0001]]・[[0027]]）で検証し、CNI プラグインへの標準入力のみ外部標準境界として plain JSON を生成する必要がある。
- **非特権単体試験と実デーモン CI 検証の両立**：`core/` 配下ではテストのスキップ（`t.Skip`）を一切禁止しているため、通常の `go test ./...` は非特権・インメモリ偽実装（`testing/synctest` と `nodesim`）で完結させ、root 権限と実 `containerd`・`runc`・CNI プラグインを要する結合試験は専用ビルドタグと専用 CI ジョブに分離する必要がある。

なお、[[0014]] 決定 4 に従い CSI Node gRPC の実装は後続のストレージ実現 ADR に送り、Kubernetes `kind` との差分オラクルは受理済みの [[0017]] 決定 7 に従い専用の差分ハーネス ADR で扱う。

## 決定

1. **ノード実行基盤を 3 層のライブラリ `core/node` と最小実行ファイル `cmd/lk-node` で構成する。**
   - 第 1 層（`core/node/lifecycle`）：時計・乱数・ネットワーク・ディスク・OS システムコールを一切持たない純粋決定論的状態機械（整数ミリ秒のみ、浮動小数点なし）。
   - 第 2 層（`core/node` と `core/node/nodesim`）：外部作用を `Clock`・`Runtime`・`Netns`・`CNI` のインターフェース（[[0015]]）へ隔離した実行ループ、埋め込み LKAL v0.2 スキーマ `core/node/node.schema.lkal`、および単一乱数シードで部分失敗・クラッシュ・再起動バックオフを再現する決定論的シミュレータ `nodesim`。
   - 第 3 層（`core/node/containerd`・`core/node/netns`・`core/node/cni`）：Linux 実環境向けの containerd v2.3 LTS・netns・CNI `exec` アダプタ。
   - `cmd/lk-node`：`-config <file.lkal>` を `lkal.Parse` と `node.Schema()` で検証・既定値補完し、実アダプタを束ねて単一コンテナワークロードを起動・監視し、`SIGINT` / `SIGTERM` 受信時に逆順後始末を完遂して終了コードを報告する CLI 実行ファイル。

2. **純粋核 `core/node/lifecycle` で厳密な LIFO セットアップ／ロールバックと再起動バックオフを定める。**
   - **正順セットアップ**：`CreateNetns` → `AddCNI(0..n-1)`（既定で `[loopback, bridge+host-local]`）→ `EnsureImage` → `CreateContainer` → `StartTask`。
   - **逆順ティアダウンと途中失敗ロールバック**：どの段階で失敗または停止要求が入っても、獲得済み資源のみを厳密な逆順 `StopTask`（`SIGTERM` 送出 → `stopGraceMs` 待機 → 未終了なら `SIGKILL`）→ `DeleteTask` → `DeleteContainer`（スナップショット削除込み）→ `DelCNI(j-1 .. 0)`（完了または試行した CNI ステップの逆順、netns 消失時も IPAM 解放のため必ず実行）→ `RemoveNetns` で解放する。終端 `Stopped` 状態では全資源フラグが偽でなければならない。
   - **再起動方針と指数バックオフ**：再起動方針は `.always`・`.onFailure`（終了コード非 0 のみ再起動）・`.never` の 3 種とする。再起動間隔は飽和整数演算 `min(backoffMaxMs, backoffBaseMs * 2^attempt)` で算出し、連続稼働時間が `backoffResetMs` 以上に達した後の終了では `attempt` を 0 に戻す。再起動時も既存の netns と CNI 割り当てを保持したまま `Container` / `Task` のみを再生成でき、ユニット停止時にまとめて CNI・netns を解放する。

3. **`containerd` v2.3.7 アダプタ・Linux `netns` アダプタ・CNI `exec` アダプタの安全規則を固定する。**
   - **`core/node/containerd`**：`go.mod` の `github.com/containerd/containerd/v2` を LTS の `v2.3.7` に固定する。名前空間付きコンテキスト上で一時 GC リース（`leases.WithLease`）を作成してイメージ確認・Pull（`WithPullUnpack`）と `NewContainer`（ランタイム `"io.containerd.runc.v2"`、`WithNewSnapshot`、`oci.WithImageConfig`、`oci.WithEnv`、`oci.WithMounts`、`oci.WithLinuxNamespace(NetworkNamespace, netnsPath)`）を保護する。タスク起動時は **`task.Wait(ctx)` を必ず `task.Start(ctx)` より先に呼び**、削除時は独立したクリーンアップ上限時間を持つコンテキストで `task.Delete(ctx, client.WithProcessKill)` 完了後に `container.Delete(ctx, client.WithSnapshotCleanup)` を呼ぶ。
   - **`core/node/netns`**：netns 名を厳格な識別子文法とベースディレクトリ境界（末尾 `/` 付き接頭辞検査）で検証する。作成時は専用ゴルーチンで `runtime.LockOSThread()` → `unix.Unshare(unix.CLONE_NEWNET)` → `/proc/self/ns/net` を `/run/netns/<name>` へ `MS_BIND` マウントし、**`UnlockOSThread()` を呼ばずにゴルーチンを終了**させて汚染された OS スレッドを確実に破棄する。削除時は CNI `DEL` 完了後に `MNT_DETACH` アンマウントとファイル削除を冪等に行う（`ENOENT` / `EINVAL` は成功扱い）。
   - **`core/node/cni`**：実行を許可するプラグイン名を固定リスト（`loopback`・`bridge`・`host-local`）に制限し、`CNI_PATH` ディレクトリ境界を検証してから `exec.CommandContext` で呼び出す。設定リスト JSON（`cniVersion = "1.0.0"`）は `loopback` → `bridge`（IPAM `host-local`、非推奨の `subnet` ではなく `ranges` 配列を使用）の順で構成し、`ADD` は正順で `prevResult` を引き継ぎ、`DEL` は逆順かつ冪等に実行する。非ゼロ終了時は CNI 仕様に従い **`stdout` から構造化エラー JSON（`code`・`msg`・`details`）を復号**し、`stderr` は内部診断用に分離保持する。

4. **純粋ライフサイクル核を Lean 4 モデルと DRT で照合し、保護定理を証明する。**
   - `spec/Nayutar/Node.lean` に同一のライフサイクル・逆順ロールバック・整数バックオフ状態機械を記述し、`nayutar-drt node`（`spec/Nayutar/Drt/Node.lean` と `core/drt/node_drt_test.go`）で Go の `core/node/lifecycle` と照合する。
   - `spec/Nayutar/Proofs/Node.lean`（および `Statements/Node.lean`・`comparator.json`）で、資源依存階層不変条件（`Task` 稼働時は `Container`・全 CNI・`Netns` が保持されていること）、部分失敗と停止時の厳密 LIFO 解放順序、CNI `DEL` の逆順実行、終端状態でのリーク不在、`.never` / `.onFailure` / `.always` の終了コード適合性、およびバックオフの有界性・単調性・健全稼働後のリセットを `sorry` なし・許容公理のみで証明する。

5. **実デーモン結合試験を `//go:build linux && containerd` に分離し、CI に専用ジョブを追加する。**
   - `core/node` の通常試験（`go test ./...` / `go test -race ./...`）は `testing/synctest` と `nodesim`・偽アダプタのみで完結させ、`t.Skip` を一切使わない。
   - 実 root + `containerd` + `runc` + Linux `netns` + CNI（`loopback`・`bridge`・`host-local`）の結合試験は `//go:build linux && containerd` の下に置き、前提欠落時は `t.Fatal` で fail-closed に失敗させる。外部レジストリのレート制限や通信揺らぎを排除するため、試験内で最小の静的 Go バイナリから OCI イメージアーカイブを生成して `client.Import` で取り込み、起動・`127.0.0.1` とブリッジ IP 疎通・終了コード `0` / 非 `0`・再起動・`SIGTERM` 停止・netns / CNI IPAM / snapshot 完全解放を検証する。
   - `.github/workflows/ci.yml` に `node-containerd` ジョブ（`ubuntu-24.04`）を追加し、SHA-256 検証付きで `containerd v2.3.7`・`runc v1.5.2`・`cni-plugins v1.9.0` を導入して `-tags containerd`（通常および `-race`）を毎回実行し、`vulncheck` ジョブでも `containerd` タグを含めて検査する。

## 根拠（調査結果・出典）

- **containerd v2.3 LTS と Go クライアント仕様**（https://github.com/containerd/containerd/blob/main/RELEASES.md 、https://github.com/containerd/containerd/releases/tag/v2.3.7 、https://github.com/containerd/containerd/blob/main/docs/getting-started.md 、https://pkg.go.dev/github.com/containerd/containerd/v2/client 、2026-11-23 確認）：
  - `v2.3` は 2028-04-30 まで保守される LTS 系列であり、`v2.3.7`（2026-10-13 公開）は `CVE-2026-95839`・`CVE-2026-53493`・`CVE-2026-95838` をすべて修正済みである。また containerd 2.x の gRPC API（`1.x`）はマイナー版間で互換性を保つため、`v2.3.7` クライアントは `v2.3.x` / `v2.4.x` デーモンの双方と通信できる。
  - 公式手順およびクライアント契約により、`task.Wait` の `task.Start` 先行呼び出し、`leases.WithLease`、`task.Delete(WithProcessKill)` → `container.Delete(WithSnapshotCleanup)` の順序、および `oci.WithLinuxNamespace` が既存 NetworkNamespace 指定を置換する挙動を確認した。
- **runc・CNI 仕様・Go スレッド仕様**（https://github.com/opencontainers/runc/releases/tag/v1.5.2 、https://github.com/containernetworking/cni/blob/main/SPEC.md 、https://github.com/containernetworking/plugins/releases/tag/v1.9.0 、https://pkg.go.dev/runtime#LockOSThread 、2026-11-23 確認）：
  - `runc v1.5.2`（OCI Runtime Spec `1.3.0`）は `CVE-2026-41579` 修正済みである。
  - CNI `SPEC.md` により、非ゼロ終了時の構造化エラー JSON が `stdout` に出力されること、構成リストで `ADD` が正順・`DEL` が逆順かつ不在資源に対して冪等であるべきこと、新規 netns では `loopback` プラグインが `lo` 有効化に必要であること、および `host-local` で `ranges` が正規形式であることを確認した。
  - Go 標準 `runtime.LockOSThread` の仕様により、`UnlockOSThread` を呼ばずに終了したゴルーチンの OS スレッドはランタイムによって破棄され、別ゴルーチンへ `CLONE_NEWNET` 後の名前空間が漏洩しないことを確認した。
- **将来の CSI Node 直接呼び出しに向けた境界整理**（https://github.com/container-storage-interface/spec/blob/master/spec.md 、https://developer.hashicorp.com/nomad/docs/concepts/storage/csi 、2026-11-23 確認）：
  - CSI `v1.13.0` では `staging_target_path` の作成がオーケストレータ側、`target_path` の作成がプラグイン側という非対称契約があり、サイドカーなし運用では双方向マウント伝播（`rshared`）とソケット `Probe` 再試行が要点となることを確認した。本 ADR では `core/node` のマウント指定（OCI `Mounts`）を拡張可能に保ち、CSI gRPC 実装自体は後続 ADR で行う。
- **ライセンスとクリーンルーム境界**：`containerd`（Apache-2.0）、`runc` / `runtime-spec`（Apache-2.0）、`containernetworking/cni` および `plugins`（Apache-2.0）はいずれも許容的ライセンスである。CNI 実行と Linux `netns` 操作はサードパーティ Go ライブラリを増やさず Go 標準ライブラリと `golang.org/x/sys/unix`（BSD-3-Clause）で実装する。なお HashiCorp Nomad は BSL 1.1（非 OSI）であるため、ソースコードは一切参照・流用せず公開仕様書のみからクリーンルームで設計する。
- **手元での確認**（2026-11-23）：
  - `lake build`（Lean `v4.35.0`）で `spec/Nayutar/Node.lean`・`spec/Nayutar/Drt/Node.lean`・`spec/Nayutar/Proofs/Node.lean`（9 定理、`sorry` なし）が警告なしで通り、`#print axioms` で許容公理のみに依存することを確かめた。
  - `nayutar-drt node` との DRT（1,000 件・全バケット・フィールド変異検出・壊した実装の検出）および全 11 スイートの DRT が一致することを確かめた。
  - `node.schema.lkal` の LKAL v0.2 検査、`core/node`（`testing/synctest`・`nodesim` 120 シード障害注入・シンボリックリンク境界検査・`UMOUNT_NOFOLLOW`）と `cmd/lk-node` の `-race` 試験、`-tags containerd` のビルド・静的検査、`golangci-lint`、`govulncheck`、および `zizmor --pedantic --strict-collection --offline .` がすべて通過することを確かめた。

## 検討した代替案

- **`containernetworking/cni/libcni` や `vishvananda/netns` を `go.mod` に追加する**：ラッパーを自分で書かずに済む。しかし `exec` と JSON 入出力および数個の Linux システムコールだけで完結する処理に対して推移的依存と脆弱性検査対象を増やすことになり、`CLONE_NEWNET` スレッド破棄の制御も不透明になるため採らない。
- **実デーモン試験で Docker Hub 等の外部レジストリから毎回 `alpine` / `busybox` を Pull する**：イメージ生成ヘルパーが不要になる。しかし外部ネットワーク障害やレジストリのレート制限で必須 CI チェックが不安定になるため、テスト内で最小 OCI イメージを生成して `client.Import` する方式を採る（Pull 経路自体はローカル OCI レジストリ試験で検証する）。
- **今回の ADR に CSI Node gRPC やクラスタ API Watch ループまで詰め込む**：一度に多くの機能が入る。しかし [[0014]] 決定 4 が初期範囲を `runc` 単一コンテナと CNI `bridge` / `host-local` に明確に限定しており、ワークロード Kind のスキーマ ADR より先に通信ループを固定すると手戻りが生じるため採らない。

## 影響とトレードオフ

- **得るもの**：`PodSandbox` と CRI 変換層を排し、Lean 4 定理と DRT・`nodesim` で逆順解放とリーク不在を機械保証したうえで、実 `containerd v2.3.7`・`runc`・Linux `netns`・CNI（`loopback` + `bridge` + `host-local`）によるコンテナ実行を `core/node` と `cmd/lk-node`、および毎回の CI ジョブで検証できる。
- **失うもの**：netns のバインドマウント管理と CNI 連鎖呼び出しを自前で保守する責務を負う。また初期段階では単一コンテナ単位であり、複数コンテナの netns 共有・CSI ボリューム・イメージガベージコレクション戦略は後続 ADR まで持たない。
- **引き受けるリスク**：`containerd/v2` の追加により `go.mod` の間接依存が増えるため、`govulncheck` による継続監視が不可欠となる。
- **今後の検証ゲート**：本 ADR の Lean 定理・`nayutar-drt node`・`nodesim` 障害注入試験・CI `node-containerd` 実結合試験を通過させる。ワークロード Kind と Kubernetes 差分オラクルハーネス（[[0017]]）を整備した段階で、`kind` 上の Kubernetes v1.37 との状態遷移・終了コード・マウント比較を実施する。

## 関連ADR

- [[0001]]：containerd・OCI・CNI の既存資産活用（前提 4）、Kubernetes のつらさの回避（前提 5）、および人間が書く設定面での LKAL 採用（前提 2・3）を定める。
- [[0002]]：Go 実装と Lean 4 仕様の分離を定める。
- [[0003]]：純粋状態機械の Lean モデル・DRT・保護定理による保証方針を定める。
- [[0014]]：本 ADR が実現する containerd 直接駆動・v2.3 LTS 固定・最小実装範囲の決定を定める。
- [[0015]]：外部作用をシミュレーション用インターフェースの背後に隔離し、`testing/synctest` と決定論的シミュレーションで試験する方針を定める。
- [[0017]]：DRT と定理保護の枠組みを定め、Kubernetes `kind` 差分オラクルを専用の後続 ADR に送る。
- [[0027]]：`node.schema.lkal` に適用する LKAL v0.2 スキーマ検証と既定値補完を定める。
