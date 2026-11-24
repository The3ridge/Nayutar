# core/node — containerd 直接駆動とノード実行基盤

`core/node/` で作業するときの指示。共通の指示はリポジトリのルートの `AGENTS.md` にあり、ここには繰り返さない。パスはリポジトリのルートからの相対で書いてある。

- 決定は ADR 0014（`proposed`）、実現方式は ADR 0031（`proposed`）。CRI と `PodSandbox` を使わず、`containerd v2.3.7`（LTS）の Go クライアント・Linux `netns`・CNI プラグイン `exec` 連鎖で単一コンテナを直接駆動する。CSI Node gRPC と複数コンテナの netns 共有は後続 ADR の範囲であり、まだ実装していない。
- 層は 3 つ。
  1. 純粋核（`core/node/lifecycle`）：ゴルーチン・時計・網・ディスク・OS システムコール・浮動小数点を持たない決定論的状態機械。Lean 4 モデル `spec/Nayutar/Node.lean` と DRT（`nayutar-drt node`）で 1 対 1 照合する。どちらかを変えるときは両方を変える。
  2. 外殻とシミュレーション（`core/node` と `core/node/nodesim`）：`Clock`・`Runtime`・`NetnsManager`・`CNIRunner` で外部作用を隔離し、埋め込みスキーマ `core/node/node.schema.lkal` で設定を検証・既定値補完する。通常の単体試験は `testing/synctest` と `nodesim` のみで完結し、`t.Skip` は一切使わない。
  3. 実環境アダプタ（`core/node/containerd`・`core/node/netns`・`core/node/cni`）：Linux 上で実デーモン・カーネル・CNI バイナリを駆動する。
- セットアップ順序は `CreateNetns` → `AddCNI(0..n-1)`（既定で `loopback` → `bridge` + `host-local`）→ `EnsureImage` → `CreateContainer` → `StartTask`。部分失敗・停止時の解放は厳密な LIFO（`TermTask` → `stopGraceMs` 待機 → `KillTask` → `DeleteTask` → `DeleteContainer` → `DelCNI(j-1..0)` → `RemoveNetns`）を守り、終端 `PhaseStopped` で全資源フラグが偽になる。CNI `Add` が途中で失敗したときは試行済みステップを含めて逆順 `Del` し、netns が消失していても IPAM 解放のため `Del` を完遂させる。
- `core/node/containerd` では `leases.WithLease` でイメージ展開・取得と `NewContainer` を保護し、**`task.Wait` を必ず `task.Start` より先に呼ぶ**。削除時は独立したクリーンアップコンテキストで `task.Delete(WithProcessKill)` 完了後に `container.Delete(WithSnapshotCleanup)` を呼ぶ。
- `core/node/netns` は厳格な識別子文法とベースディレクトリ境界（末尾 `/` 付き接頭辞検査）を検査し、専用ゴルーチンで `runtime.LockOSThread()` → `CLONE_NEWNET` → `MS_BIND` したあと **`UnlockOSThread()` を呼ばずに終了**させて OS スレッドを破棄する。削除は `MNT_DETACH` + `Remove` を冪等に行う。
- `core/node/cni` はプラグイン名を許可リスト（`loopback`・`bridge`・`host-local`）と `CNI_PATH` ディレクトリ境界に制限し、設定 JSON は `cniVersion = "1.0.0"` と `host-local` の `ranges` 形式を使う。非ゼロ終了時の構造化エラー JSON は `stderr` ではなく **`stdout`** から復号する。
- 実 root + `containerd` + `runc` + CNI バイナリの結合試験は `core/node/containerd_linux_test.go`（`//go:build linux && containerd`）にあり、前提欠落時は `t.Fatal` で失敗させる。CLI 実行ファイルは `cmd/lk-node`。
