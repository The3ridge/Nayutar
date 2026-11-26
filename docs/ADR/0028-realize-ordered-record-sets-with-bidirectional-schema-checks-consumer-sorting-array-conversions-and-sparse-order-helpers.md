---
title: 順序付きレコード集合を、双方向のスキーマ検査・消費者側ソート・2 種の配列往復変換・疎な採番ヘルパで実現する
status: accepted
date: 2026-11-22
depends-on: ["0001", "0003", "0009", "0011", "0025", "0026", "0027"]
---

# 0028: 順序付きレコード集合を、双方向のスキーマ検査・消費者側ソート・2 種の配列往復変換・疎な採番ヘルパで実現する

## コンテキスト

[[0011]] は、LKAL に新しい型を追加せず、順序付きレコード集合を既存のラベル付きレコードの集合（`.map`）と明示的な整数葉 `order`（初期値は `10, 20, ...` の疎な整数）で表し、スキーマで `order` の存在と一意性を検査し、消費者側が `order` を読んで整列する方針を定めた。また [[0027]] は、リソース種別ごとの LKAL v0.2 スキーマ（`core/resource` の `KindSchema`）を導入し、`.map` の `unique-by = ["order"]` で一意性を検査できるようにした。

しかし、[[0011]] を完結した実装と形式仕様に落とすには、次の点が未決定のまま残っている。

- **スキーマ定義の抜け穴**：LKAL v0.2 の `schema.CompileFile` は `unique-by` を汎用の一意性制約として扱うため、(1) `field "order"` に `required = true` が無く `order` 欠落要素が `unique-by` を素通りするスキーマ、(2) `open = true` の要素レコードで `field "order"` 自体を宣言しないスキーマ、(3) `order` を非 `Int` や共用体型（`types = [.int, .decimal]` では `10` と `10.0` が別型タグとなり衝突しない）にするスキーマ、(4) 任意の `order` に `default = 10` を置いて 2 要素以上で `Complete` 後に `L509` を起こすスキーマ、(5) 逆に要素レコードに `field "order"` を書きながら `.map` 側の `unique-by = ["order"]` を書き忘れたスキーマ、がいずれも `CompileKindSchema` を通ってしまう。
- **要素ラベルの最小文法**：LKAL の `Record` キー自体は空文字列や `/` を含められるが、`/` 区切りのパス操作（`lkal get` / `set`、[[0012]] の Patch 提案）や [[0026]] の `Ref` 出現パス、[[0013]] の葉パス所有権と組み合わせるには、要素ラベルが満たすべき最小の文法を固定する必要がある。
- **消費者側の整列と `order` 葉の扱い**：CNI ネットワーク設定リスト（`.conflist`）の `plugins` 配列のように、追加・検査では昇順、削除・回収では逆順の走査を要し、かつ未知フィールドである `order` を出力オブジェクトに残せない消費者に対して、純粋な抽出・整列関数の入出力と誤り順序を固定する必要がある。
- **外部配列との往復変換の 2 形態**：外部の順序付き配列には、CNI の `plugins` のように要素内部に一意な識別子欄を持たない配列と、Kubernetes の連想リスト（`containers`・`initContainers`・`volumes` など）のように各要素が内部の文字列欄（`name` など）で識別される配列の 2 種類がある。[[0011]] が G0 の検証ゲートに置いた「配列形式の取り込みと消費者側ソートの往復性」を機械検証するには、両形態の変換規則と境界条件を定める必要がある。
- **疎な整数の挿入・再採番と `int64` 境界**：先頭前への挿入（`0` や負数を含む）、末尾後への追加、2 要素間の中点挿入、間隔枯渇時（差が 1）の検出と再採番について、Go の `int64` と Lean の多倍長 `Int` の間で溢れの扱いを一致させる必要がある。

本 ADR は [[0011]] の実現方式を定める。ストア上の葉パス所有権（`/t/<tenant>/own/...`）のプロトコル自体は [[0013]] の範囲であり、本 ADR では扱わない。

## 決定

1. **`core/resource` の `CompileKindSchema` で、順序付きレコード集合のスキーマ不変条件を双方向に強制する。**
   - `root`（`meta`・`spec`・`status`）およびすべての `def`（`use = &def/...` の解決後を含む）に現れるすべての `.map`（共用体 `types` に `.map` を含むものを含む）を検査する。
   - **順方向**：`.map` の `unique-by` に `"order"` が含まれるとき、`value` は単一の `type = .record` に解決され、その `field` に `"order"` が明示宣言されていなければならない（`open = true` であっても省略不可）。さらに `field "order"` は `required = true` であり、`default` を持たず、解決後の型が単一の `type = .int`（共用体型不可）でなければならない。
   - **逆方向**：`.map` の `value` がレコードを許し、その `field` に `"order"` を宣言しているとき、当該 `.map` の `unique-by` は必ず `"order"` を含まなければならない（そのうえで順方向の規則も適用する）。`"order"` 以外の欄に対する `unique-by`（`unique-by = ["client-url", "peer-url"]` など）にはこの制約を課さない。
2. **純粋な核を `core/resource/orderedset` に置き、定数と最小キー文法を固定する。**
   - 順序葉の欄名は `OrderField = "order"`、初期および標準の間隔は `Step int64 = 10`（0 始まりの添字 `i` に対し `10 * (i + 1)`、すなわち `10, 20, 30, ...`）とする。`order` の値域は符号付き 64 ビット整数全体（`[-2^63, 2^63 - 1]`、`0` と負数を含む）とし、正数のみに縛る場合は各種別スキーマの `min` / `max` で宣言する。
   - 要素ラベル（マップのキー）は、空でなく、`'/'` を含まず、`'$'` で始まらない文字列（[[0026]] の葉パスセグメント文法と同じ）に限る。より狭い文字種制約（RFC 1123 ラベルなど）は各種別スキーマの `.map` の `key` 制約に委ねる。
   - キー引き上げ変換の識別欄名 `keyField` は、要素ラベルと同じ最小文法を満たし、かつ `"order"` でない文字列に限る。
3. **消費者側の抽出・整列関数 `Entries` と `EntriesReverse` を提供する。**
   - 入力 `set`（`lkal.Record`）を受け取り、成功時は `[]Entry`（`Key string`、`Order int64`、`Value lkal.Record`）を返す。
   - 返却する各 `Entry.Value` からは `"order"` 欄を取り除き、残りの欄を保持する。
   - `Entries` は `Order` の狭義昇順、`EntriesReverse` は `Order` の狭義降順（`Entries` の逆順）で返す。`Order` の重複は拒否するため、同値タイブレークは存在せず、入力レコードの欄順序に依存しない一意な全順序が決まる。
4. **外部配列との往復変換を 2 形態で提供する。**
   - **外部ラベル列モード（`FromItems`）**：`[]Item`（`Key string`、`Value lkal.Record`）を受け取り、各要素に `order = 10 * (i + 1)` を先頭欄として付与した `.map` 形式の `lkal.Record` を返す。入力の `Item.Value` がすでに `"order"` 欄を持つ場合は、黙って上書きせず `reservedField` で拒否する（`Entries` が `"order"` を取り除くため、上書きを許すと往復性が壊れる）。
   - **キー欄引き上げモード（`FromKeyedSlice` / `ToKeyedSlice` / `ToKeyedSliceReverse`）**：
     - `FromKeyedSlice(elems []lkal.Record, keyField string)` は、各要素レコードから文字列欄 `keyField` を要素ラベルとして取り出し、`keyField` を要素レコードから取り除いたうえで `FromItems` と同じく `order = 10 * (i + 1)` を付与した `lkal.Record` を返す。`"order"` をすでに持つ要素は `reservedField` で拒否する。
     - `ToKeyedSlice(set lkal.Record, keyField string)`（および逆順の `ToKeyedSliceReverse`）は、`Entries`（または `EntriesReverse`）で整列・`"order"` 除去を行ったあと、除去後の要素レコードがすでに `keyField` を持っていれば `reservedField` で拒否し、先頭に `keyField = lkal.String(Key)` を戻した `[]lkal.Record` を返す。
5. **疎な整数の挿入・再採番ヘルパを純粋関数として提供する。**
   - `OrderBefore(first int64) (int64, error)`：`first - 10` を返す。`int64` 下限を下回るときは `overflow` を返す。`first = 10` なら `0`、`first = 0` なら `-10` となり、既存要素を再採番せずに先頭前へ挿入できる。
   - `OrderAfter(last int64) (int64, error)`：`last + 10` を返す。`int64` 上限を上回るときは `overflow` を返す。
   - `OrderBetween(lo, hi int64) (int64, error)`：`lo >= hi` なら `malformed`、`lo == hi - 1`（間に整数が存在しない）なら `gapExhausted` を返し、それ以外は溢れを起こさずに床中点 `⌊(lo + hi) / 2⌋`（Go では `(lo & hi) + ((lo ^ hi) >> 1)`）を返す。
   - `Renumber(set lkal.Record) (lkal.Record, error)`：`Entries(set)` で現在の `order` 昇順に整列したうえで、`FromItems` により要素順と各要素の本体を保ったまま `order` を `10, 20, ...` に振り直した `lkal.Record` を返す。
6. **誤りの分類と検査順序を固定し、入力レコードの欄順序に対して不変にする。**
   - 誤りは 6 種類（`malformed`・`duplicateKey`・`duplicateOrder`・`reservedField`・`gapExhausted`・`overflow`）とする。
   - 各操作は次の固定順序で全要素を走査してから次の段へ進む。これにより、`Value.unique` を満たし `lkal.Equal` で等しい 2 つのレコードは、成功時の出力だけでなく失敗時の誤り種別も一致する。
     - `Entries` / `EntriesReverse`：(1) `set` 全階層のキー一意性（`Value.unique`）、各要素キーの最小文法、各要素値が `Record` であること、`"order"` が `int64` 範囲の `Int` として存在すること → いずれか違反で `malformed`、(2) 要素間の `order` 重複 → `duplicateOrder`。
     - `ToKeyedSlice` / `ToKeyedSliceReverse`：(1) `keyField` の文法（`"order"` 不可を含む）→ `malformed`、(2) `Entries` の検査（`malformed` → `duplicateOrder`）、(3) `"order"` 除去後の要素レコードに `keyField` が残存 → `reservedField`。
     - `FromItems`：(1) 各 `Item.Key` の最小文法と各 `Item.Value` 全階層のキー一意性 → `malformed`、(2) いずれかの `Item.Value` が `"order"` を持つ → `reservedField`、(3) `Item.Key` の重複 → `duplicateKey`、(4) `10 * (i + 1)` の `int64` 上限超過 → `overflow`。
     - `FromKeyedSlice`：(1) `keyField` の文法、各要素全階層のキー一意性、`keyField` が有効な要素ラベル文字列として存在すること → `malformed`、(2) いずれかの要素が `"order"` を持つ → `reservedField`、(3) 取り出した要素ラベルの重複 → `duplicateKey`、(4) `10 * (i + 1)` の `int64` 上限超過 → `overflow`。
     - `Renumber`：`Entries` の検査（`malformed` → `duplicateOrder`）に続けて `FromItems` の上限検査（`overflow`）。
7. **純粋な核を Lean 4 モデルと DRT で照合し、9 個の性質を定理として証明する。**
   - `spec/Nayutar/OrderedSet.lean` に `Nayutar.Envelope` の `Value` 木を再利用して同一の意味論（`int64` 境界検査 `inInt64` を含む）を記述し、`nayutar-drt orderedset` と `core/drt/orderedset_drt_test.go` で全操作（`entries`・`entriesReverse`・`fromItems`・`fromKeyedSlice`・`toKeyedSlice`・`toKeyedSliceReverse`・`orderBefore`・`orderAfter`・`orderBetween`・`renumber`）の成否・誤り種別・結果値を照合する。
   - `spec/Nayutar/Proofs/OrderedSet.lean` で次の 9 定理（`Nayutar.OrderedSet` 名前空間、`sorry` なし、許容公理のみ）を証明し、保護対象とする：
     - `entries_strictly_sorted`：`entries` が受理した列は `order` が狭義単調増加し、キーが互いに異なり、各キーと `order` が文法と `int64` 範囲を満たす。
     - `entries_strips_order`：`entries` が返す各要素レコードは `"order"` を持たず、`Value.unique` を満たす。
     - `entries_complete_and_sound`：`entries` が返す列の長さは入力フィールド数に等しく、各要素は入力レコード中の同一キーの要素から `"order"` を取り除いた値に一致する。
     - `entries_reverse_eq_reverse`：`entriesReverse set = (entries set).map List.reverse`。
     - `entries_eqv_invariant`：`Value.unique` を満たし `Value.eqv` で等しい 2 つのレコードに対し、`entries` の成否と誤り種別は一致し、成功時は同じ長さ・同じキー列・同じ `order` 列・要素ごとに `Value.eqv` で等しい本体列を返す。
     - `fromItems_entries_roundtrip`：`fromItems` が受理した集合は `Value.unique` を満たし、その `entries` は元の `items` と同じ長さ・同じキー列・`10 * (i + 1)` の `order` 列・同一の本体列を返す。
     - `keyed_slice_roundtrip`：`fromKeyedSlice` が受理した集合は `Value.unique` を満たし、`toKeyedSlice` で戻した配列は元の配列と同じ長さで各要素が `Value.eqv` で等しい。
     - `orderBetween_strict`：`orderBetween lo hi` が返した中点 `mid` は `lo < mid < hi` かつ `inInt64 mid` を満たし、`inInt64 lo`・`inInt64 hi`・`lo < hi`・`hi - lo = 1` のときは必ず `gapExhausted` を返す。
     - `renumber_preserves_order`：`renumber` が受理した集合の `entries` は、元の集合の `entries` と同じキー列・同じ本体列を保ち、`order` だけを `10 * (i + 1)` に置き換える。

## 根拠（調査結果・出典）

- **内部不変条件（LKAL `v0.2.0` のデータモデルとスキーマ検証）**（`../LKAL` の `parser.go`・`value.go`・`json.go`・`schema/schema.go`・`schema/validate.go`・`schema/complete.go`、2026-11-22 確認）：
  - `plugin "bridge" { order = 10, ... }` は入れ子の `lkal.Record` に展開され、`lkal.Equal` は全階層でフィールド記述順を無視する。したがって、消費者側整列がレコードのフィールド順に対して不変であること（`entries_eqv_invariant`）が、[[0001]]・[[0011]] の不変条件から直接要求される。
  - `lkal.Int` は `int64` 全域を `Format` ↔ `Parse` および `ToJSON` ↔ `FromJSON` で無損失に往復する。一方、Lean の `Int` は非有界であるため、`OrderBefore` / `OrderAfter` / `OrderBetween` / `FromItems` の溢れ境界を Lean モデル側でも `inInt64` として明示することで DRT の完全一致が成り立つ。
  - `schema/validate.go` の `validateMap` は、`unique-by` 対象欄が要素レコードに存在しないとき `continue` で検査を飛ばし、また共用体型では型タグ付きの比較キー（`I10;` と `D4:10.0` など）を使う。`schema/complete.go` は `.map` の各既存要素に `default` を独立適用するため、任意の `order` に `default = 10` があると 2 要素以上で衝突する。決定 1 の `CompileKindSchema` 検査は、これらの抜け穴を種別スキーマのコンパイル時点で塞ぐためのものである。
- **内部不変条件（[[0025]]・[[0026]]・[[0013]] との整合）**：
  - [[0025]]：`order` 葉の変更は `spec` の `lkal.Equal` を変えるため `meta/generation` が 1 増え、JSON / LKAL 上のフィールド記述順だけの入れ替えでは `lkal.Equal` が保たれて `generation` もストア書き込みも動かない。また、入力要素がすでに `"order"` を持つ場合に黙って上書きせず拒否する規則は、[[0025]] 決定 5 の「黙って無視しない」方針および往復定理の前提と一致する。
  - [[0026]]：順序付きレコード集合の各要素は配列添字ではなく安定な要素ラベル（`spec/init/migrate/creds` など）の下に置かれるため、兄弟要素の挿入や `order` 変更によって `Ref` の `sourcePath` がずれず、無関係な既存 `Ref` の再認可が発生しない。要素ラベルから空文字列・`'/'`・先頭 `'$'` を除く決定 2 は、[[0026]] のパス一意性規則と一致する。
  - [[0013]]（`spec/.lake/packages/kisage` v0.1.2 の `projection/projection.go`・`plan/classify.go`・`Kisage/Plan.lean`、2026-11-22 確認）：`.../order` は通常の `Int` 葉として射影され、他アクタによる並び替えは `K201` で阻止され、同一アクタによる複数要素の `order` 交換は単一 CAS `Txn` で中間の重複状態を経ずに遷移できる。
- **CNI 仕様における実行順と未知フィールドの扱い**（CNI Specification v1.0.0 https://github.com/containernetworking/cni/blob/v1.0.0/SPEC.md 、v1.1.0 https://github.com/containernetworking/cni/blob/v1.1.0/SPEC.md 、https://www.cni.dev/docs/spec/ 、2026-11-22 確認）：
  - CNI v1.0.0 以降は `plugins` 配列を持つ Network Configuration List が必須であり、`ADD` と `CHECK` は配列の正順、`DEL` と v1.1.0 の `GC` は逆順（"in reverse order"）で実行する。決定 3 が正順と逆順の両方を提供する根拠である。
  - 同仕様は同一 `type` の複数プラグインを禁じておらず、かつ未知フィールドについてランタイムに保持を義務づける一方でプラグインがエラーを返すことを認めている（"Plugins may define additional fields that they accept and may generate an error if called with unknown fields. Runtimes must preserve unknown fields in plugin configuration objects when transforming for execution."）。要素ラベルを `type` 欄と切り離して保持し（決定 4 の外部ラベル列モード）、消費者抽出時に `order` 葉を除去する（決定 3）根拠である。
- **Kubernetes 連想リストと Server-Side Apply の順序合成との比較**（Kubernetes API Conventions https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md 、Server-Side Apply https://kubernetes.io/docs/reference/using-api/server-side-apply/ 、KEP-555 https://github.com/kubernetes/enhancements/tree/master/keps/sig-api-machinery/555-server-side-apply 、`sigs.k8s.io/structured-merge-diff` `fieldpath` https://pkg.go.dev/sigs.k8s.io/structured-merge-diff/v6/fieldpath および `typed/merge.go` https://github.com/kubernetes-sigs/structured-merge-diff/blob/master/typed/merge.go 、2026-11-22 確認）：
  - Kubernetes の `x-kubernetes-list-type: map` は `name` 等の内部キー欄で要素を識別する配列であり、SSA の `managedFields` は要素キー（`k:{"name":...}`）ごとの所有権だけを記録し、並び順そのものの所有権を持たない（マージ時は `sharedOrder` 等で順序を合成する。過去には `initContainers` の挿入順序崩れ https://github.com/kubernetes/kubernetes/issues/104641 も報告された）。Nayutar は内部表現を `order` 葉つきの `.map` とし、KRM 取り込み・書き出しの境界を `FromKeyedSlice` / `ToKeyedSlice`（決定 4）で結ぶ。
- **疎な符号付き整数優先度と一意性の先例**（nftables `nft(8)` https://www.netfilter.org/projects/nftables/manpage.html 、nftables Wiki https://wiki.nftables.org/wiki-nftables/index.php/Configuring_chains 、Kubernetes API Priority and Fairness https://kubernetes.io/docs/concepts/cluster-administration/flow-control/ 、`systemd.unit(5)` https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html 、2026-11-22 確認）：
  - `nftables` は符号付き整数と疎な標準値（`-300, -150, ..., 100`）により既存定義を書き換えずに前後へ挿入できる一方、同一優先度時の順序を未定義としている。Kubernetes の `FlowSchema` は整数優先度の衝突を名前辞書順でタイブレークするが、公式文書自身がタイブレークに頼らず一意な値を設定するよう推奨している。`systemd` の drop-in はファイル名の辞書順であるため桁揃えが必要で負数を扱いにくい。単一リソース内の集合に対して符号付き整数と `unique-by = ["order"]` による厳密一意性を組み合わせる決定 1〜3・5 は、先頭前挿入を許しつつ未定義順・暗黙の名前依存・桁揃え問題を同時に避ける。
- **ライセンスとクリーンルーム境界**：LKAL `v0.2.0` と kisage `v0.1.2` は本プロジェクトと同一作者の Apache-2.0 モジュールである。CNI 仕様・Kubernetes 文書・nftables 文書は外部データ形式および比較対象の仕様確認にのみ参照し、`core/resource/orderedset` と `spec/Nayutar/OrderedSet.lean` は Nayutar 自身の不変条件から独立に実装した。
- **手元での確認**（2026-11-22）：
  - `lake build` で `spec/Nayutar/OrderedSet.lean`・`spec/Nayutar/Drt/OrderedSet.lean`・`spec/Nayutar/Proofs/OrderedSet.lean`（9 定理、`sorry` なし）が警告なしで通り、`#print axioms` で許容公理（`propext`・`Classical.choice`・`Quot.sound`）のみに依存することを確かめた。`spec/Nayutar/Statements/OrderedSet.lean` と `spec/comparator.json` への 9 定理の追加後に、`check-protected-theorems`・`lake comparator`（全 43 定理受理）・`scripts/tests/test-comparator-rejections.sh` がすべて通過することも確かめた。
  - `nayutar-drt orderedset` との DRT が、既定の 1,000 件および 4 つのシードで各 1 万件（計 4 万件）すべて一致し、到達可能な全 32 個の `(操作, 結果)` バケット、結果フィールドの変異検出、およびわざと壊した 17 種の実装の検出を通過した。
  - `core/resource/orderedset` の単体試験・`math/big` 照合つきファジング、および `core/resource` の `CompileKindSchema` 双方向検査と `Objects` ストア往復試験（インメモリ・テナント登録簿ビュー、`-race` 付き）がすべて通過した（手元環境に `etcd` バイナリが無いため、`-tags etcd` は `go vet` まで確認し、3 メンバー etcd 上の実行は CI の `store-etcd` ジョブに委ねる）。

## 検討した代替案

- **`CompileKindSchema` で `unique-by` に `"order"` がある片方向だけを検査し、`unique-by` 無しの `field "order"` を許す**：通常の整数欄として `"order"` という名前を使える。しかしリソーススキーマで `field "order" { type = .int, required = true }` まで書きながら `.map` に `unique-by = ["order"]` を付け忘れる誤りをコンパイル時に検出できず、`Entries` 実行時まで重複がすり抜けるため採らない。
- **`CompileKindSchema` に追加規則を置かず LKAL 標準の `schema.CompileFile` のみに委ねる**：`core/resource` のコードが増えない。しかし `required` 省略による `order` 欠落の素通り、共用体型による `10` と `10.0` の併存、`default` による 2 要素目での衝突を防げないため採らない。
- **外部ラベル列変換とキー欄引き上げ変換の片方だけを提供する**：API が小さくなる。しかし CNI の `plugins` には内部キー欄が無く、逆に Kubernetes の `containers` 等は内部の `name` 欄を要素ラベルへ引き上げる必要があるため、どちらか一方では G0 の往復変換に不足する。
- **挿入・再採番ヘルパを後続に送る**：初期の関数数は減る。しかし [[0011]] がリスクに挙げた間隔枯渇（`hi - lo == 1`）と中点挿入の `int64` 境界条件がモデルと DRT で未検証のまま残るため、純粋関数として今回含める。
- **`order` を正の整数（`min = 1`）に固定する**：負数を考えずに済む。しかし初期値 `10` の前に要素を挿入するたびに全要素の再採番が必要になり、他アクタが所有する既存要素の `.../order` 葉まで書き換えて [[0013]] の所有権競合を誘発するため採らない。
- **`order` が重複したときに要素ラベルの辞書順でタイブレークする**：重複エラーが起きない。しかし [[0011]] 決定 3 の一意性規則に反し、リネームで実行順が暗黙に変わる副作用を持ち込むため採らない。
- **要素ラベルに空文字列や `'/'` を許す**：`lkal.Record` の生のキー集合と一致する。しかし [[0012]] の `/` 区切り Patch パスや [[0026]] の `Ref` 出現パスで区切りと区別できなくなるため採らない。

## 影響とトレードオフ

- **得るもの**：
  - 種別スキーマのコンパイル時点で、順序付きレコード集合の `order` 必須・単一 `Int` 型・`default` 禁止・`unique-by` 指定漏れがすべて機械的に検出される。
  - CNI 型の外部ラベル列と Kubernetes 型のキー欄引き上げ配列の双方について、取り込みと正順・逆順抽出の往復性とフィールド順序不変性が Lean の定理と DRT で保証される。
  - 先頭前挿入（`0`・負数）、中点挿入、間隔枯渇検出、再採番が `int64` 全域で溢れなく動作する。
- **失うもの**：
  - リソース種別スキーマの `.map` 要素レコードでは、順序付きレコード集合以外の用途で `"order"` という欄名を使えない（別の欄名を使う必要がある）。
  - 要素ラベルに空文字列・`'/'`・先頭 `'$'` を持つレコードは `orderedset` で扱えない。
- **引き受けるリスク**：
  - 連続する中点挿入は最大 $\lceil \log_2(\text{間隔}) \rceil$ 回（間隔 10 なら 3 回：`10, 20` → `15` → `12` → `11`）で `gapExhausted` に達し、`Renumber` が必要になる。複数アクタが同一集合内の異なる要素を所有している状態での `Renumber` は、他アクタ所有の `.../order` 葉に触れるため、[[0013]] の権限（`adopt` または協調更新）を要する。
  - [[0013]] の API サーバ版 `prune` を実装する際、要素の全葉を削除したあとに空レコード（`plugin "bridge" {}`）を残すと `field "order" { required = true }`（`L502`）に違反するため、[[0013]] の実現 ADR で空になったマップ要素レコード自体の除去規則を定める必要がある。
- **今後の検証ゲート**：[[0011]] が G0 に置いた「配列形式の取り込みと消費者側ソートの往復性」の性質ベース試験・差分試験を、本 ADR の 9 定理・`nayutar-drt orderedset`・ファジングおよび `core/resource` のストア結合試験（インメモリ・3 メンバー etcd）で満たす。[[0013]] の実装時に、複数アクタ下での要素削除（`prune`）と再採番の所有権遷移を検証する。

## 関連ADR

- [[0001]]：LKAL を宣言基盤とし、YAML を取り込み入力に限る前提を定める。
- [[0003]]：純粋な核を Lean 4 の実行可能モデルとの DRT と定理保護で保証する方針を定める。
- [[0009]]：リソース封筒（`meta`・`spec`・`status`）を定める。
- [[0011]]：本 ADR が実現する順序付きレコード集合の表現規約を定める。
- [[0012]]：`/` 区切りパスの Patch 形式を定める提案。要素ラベルの `'/'` 禁止はこれと整合する。
- [[0013]]：葉パス所有権モデルを定める提案。`.../order` 葉の所有と `prune` 時の空要素レコード除去は同 ADR の実現範囲である。
- [[0025]]：封筒の純粋な核と `core/resource` のストア結合を定める。
- [[0026]]：`Ref` の出現パス文法（空文字列・`'/'`・先頭 `'$'` の禁止）を定める。
- [[0027]]：`core/resource` の `KindSchema` と `unique-by = ["order"]` の検証基盤を定める。本 ADR は `CompileKindSchema` に双方向の不変条件検査を追加する。
