---
title: キー空間を、厳格なセグメント文法・キーコーデック・フェンスつきのテナント登録簿で実現する
status: accepted
date: 2026-11-18
depends-on: ["0004", "0006"]
---

# 0022: キー空間を、厳格なセグメント文法・キーコーデック・フェンスつきのテナント登録簿で実現する

## コンテキスト

[[0006]] は、ストアのキー空間を次の 6 つの形に固定した。

```
/t/<tenant>/o/<group>/<kind>/<name>
/t/<tenant>/own/<group>/<kind>/<name>
/t/<tenant>/ref/<tKind>/<tName>/<sKind>/<sName>
/t/<tenant>/bind/<identityHash>
/x/<identityHash>/schema/<version>
/sys/tenants/<tenant>
```

[[0006]] が決めたのはキーの形までであり、実装に要る次の点は決めていない。

- 各セグメント（`<group>`・`<kind>`・`<name>`・`<identityHash>`・`<version>`）に書ける文字と長さ。セグメントが `/` を含められると、キーを一意に解析できない。
- Kubernetes の core グループ（空文字列）に当たるものの表し方。
- 逆参照索引 `ref/` の `<tKind>`・`<sKind>` には group の欄が無い。別の group に同じ名前の kind があると、索引のキーが衝突する。
- `/sys/tenants/<tenant>` に置く値と、テナントの登録・削除の意味論。

[[0004]] のストア契約では、キーはバイト列であり、`Range` と `Watch` は接頭辞で範囲を取る。範囲削除は無く、`Txn` は比較と書き込みの個数に上限を持ちうる。[[0020]] のテナントビュー（`core/store/tenant`）は `/t/<tenant>/` の付け外しだけを行い、権限の境界ではない。

本 ADR は [[0006]] の実現方式を決める。値の封筒（[[0009]]）、`Ref` の検査（[[0010]]）、所有権の更新規則（[[0013]]）は扱わない。

## 決定

1. **セグメントの文法を ASCII のバイト列で固定する。**
   - ラベル：`[a-z0-9]([a-z0-9-]*[a-z0-9])?`、1〜63 バイト。
   - サブドメイン：`[a-z0-9]([a-z0-9-]*[a-z0-9])?` に合う要素を `.` でつないだもの、全体で 1〜253 バイト。要素ごとの長さの上限は置かない。
   - `<tenant>`・`<kind>`・`<version>` はラベル。`<name>` はサブドメイン。
   - `<group>` は、予約語 `core`（組み込みの種別の group）か、要素を 2 つ以上持つサブドメイン。ドットの無い group は `core` だけである。
   - `<identityHash>` は小文字の 16 進 64 文字（SHA-256 の 16 進表記の形）。何をハッシュするかは本 ADR では決めない。
   - これ以外のバイト（大文字、`_`、`/`、`%`、空白、制御文字、非 ASCII）を含むセグメント、空のセグメント、長さを超えるセグメントは拒否する。
2. **namespace に当たる階層は `<name>` の中のドットで表す。** 階層はキーの構造に現れない。Kubernetes の namespace をどの順で名前に写すかは、取り込みを決める ADR に残す。
3. **`ref/` の kind は修飾つき kind とする。** `<tKind>`・`<sKind>` には、group が `core` なら `<kind>` を、それ以外なら `<kind>.<group>` を書く。kind はドットを持たないので、最初のドットで一意に分解できる。ドットがちょうど 1 つの修飾つき kind（`widget.apps`）は、group がドットを持たないことになるので拒否する。[[0006]] の 5 セグメントの形は変えない。
4. **キーの表現は 1 つだけにする。** 妥当なキーは、ちょうど 1 つのバイト列に対応する。解析はその正規の形だけを受け付け、末尾の `/`、連続した `/`、6 つの形以外（`/t/<tenant>/` の下の未知の区分、`/sys/` の下の `tenants/` 以外、`/x/<identityHash>/` の下の `schema/` 以外）を拒否する。（その後 ADR 0024 が、`/sys/` の下に `nodes/<node>` と `liveness/evictions` の 2 つの形を足した。）
5. **コーデックを `core/store/keyspace` に置く。** 標準ライブラリだけに依存する。
   - キーの組み立て（絶対キーと、テナントビューに渡すテナント相対キー）、解析、`Range`・`Watch` に渡す接頭辞、各セグメントの検査を提供する。
   - 接頭辞は必ず `/` で終える。
   - テナント名の検査はコーデックを唯一の定義とし、`core/store/tenant` はそれを使う。
6. **コーデックを Lean モデルと照合する。** `spec/Nayutar/Keyspace.lean` に同じ文法の組み立てと解析を書き、`nayutar-drt` の `keyspace` スイートとして実行する。Go 側は妥当なキー、妥当なキーを少し壊した文字列、敵対的なセグメントを乱数で作り、組み立てと解析の結果（成否と値）が Lean と一致することを確かめる。固有の Lean 定理は置かない（[[0004]] のストア契約と同じ扱い）。DRT の入力は JSON なので UTF-8 として妥当な文字列に限り、妥当でないバイト列は Go のファズ試験で扱う。
7. **テナント登録簿を `core/store/tenant` に置く。** 登録は `/sys/tenants/<tenant>` のキーである。
   - 値は状態だけを持つ最小の LKAL-JSON とする：`{"$lkal":"0.1","state":{"$enum":"active"}}` と `{"$lkal":"0.1","state":{"$enum":"terminating"}}`。これは [[0009]] の封筒ではない。読み取りは `lkal.FromJSON` と埋め込み LKAL v0.2 スキーマ（`tenant.schema.lkal`）で検査し（[[0027]]）、読めない値は誤りとして扱い、ビューを開かない（fail-closed）。
   - 登録は「キーが無い」ことを比較した `Txn` で行う。すでにあれば拒否する。
   - 一覧は `/sys/tenants/` の `Range` で、1 つのリビジョンの上の全登録を名前順に返す。内部ではページ単位で読む。
8. **登録簿が開くビューは、すべての `Txn` にフェンスを足す。** ビューは、開くときに登録が `active` であることを確かめ、そのときの登録キーの mod revision を覚える。以後のすべての `Txn` に「登録キーの mod revision がその値に等しい」という比較を足す。
   - 登録が消えるか `terminating` になると、登録キーの mod revision が変わり、古いビューの書き込みはそれ以降ひとつも適用されない。同じ名前で登録し直しても、古いビューは使えるようにならない。
   - 比較が成り立たなかった `Txn` について、ビューは登録キーを読み直し、フェンスが破れていれば専用の誤りを返す。破れていなければ、呼び出し側の比較が成り立たなかったものとして返す。
   - 読み取り（`Get`・`Range`・`Watch`・`CurrentRev`）にはフェンスをかけない。
   - 未登録や削除中のテナントのビューは開けない。
   - [[0020]] の素のテナントビュー（`tenant.View`）は残す。フェンスの保証は、登録簿が開いたビューを通る書き込みにだけ及ぶ。
9. **テナントの削除は 2 段階で行う。**
   1. 登録を `terminating` に書き換える（mod revision の比較つき）。
   2. `/t/<tenant>/` の下のキーを、ページ単位の `Range` と、削除だけを持つ `Txn` で消す。この `Txn` にも `terminating` の登録を指すフェンスを足す。`Range` が空になるまで繰り返す。
   3. 登録キーを、mod revision の比較つきで削除する。
   - 途中で止まった削除は、もう一度呼べば続きから進む。
   - 登録を読んだあとの削除は、別の削除に先を越されても成功で終わる。登録が消えたあとに始まった削除は、未登録の誤りを返す。
   - 削除の途中で同じ名前が登録し直されていたら、削除はその新しい登録に手を付けずに成功で終わる。削除は、最初に読んだ `active` の登録の mod revision を覚えておき、それと違う mod revision の `active` を読んだら新しい登録と判断する（`active` の登録は書き換えられないので、mod revision が違えば作り直されたものである）。`terminating` への書き込みの比較が通らなかった直後も同じである。
   - 登録し直された登録を、別の削除がすでに `terminating` にしていた場合は、その削除を引き継いで終わらせることがある。ストアの契約は作成リビジョンを持たず、`terminating` の登録がどの世代のものかを見分けられないためである。`terminating` の登録はビューを開けず、登録もし直せないので、引き継いでも、使える登録や書き込めるキーは失われない。
   - 削除が成功で戻ったあと、登録簿が開いたビューだけが書き込むかぎり、`/t/<tenant>/` の下にキーは残らない。
10. **検査の対象は次のとおりとする。**
    - コーデック：往復（組み立てて解析すると元に戻る。解析できた文字列を組み立て直すと同じ文字列になる）、異なるテナントのキーと接頭辞が互いの接頭辞に入らないこと、`/t/`・`/x/`・`/sys/` の区分が交わらないこと、敵対的なセグメント（`..`、`.`、`/`、`%`、大文字、NUL、空、長すぎるもの、非 ASCII）の拒否。単体試験、性質試験、ファズ試験、Lean との DRT。
    - 登録簿：インメモリ実装と 3 メンバーの etcd（ビルドタグ `etcd`）の両方で、登録・一覧・削除・フェンス・再登録・削除の再開・同時削除を確かめる。削除と書き込みを競わせ、削除の成功のあとにキーが残らず、古いビューの書き込みが適用されないことを確かめる。削除が出す `Txn` の 1 つ 1 つの直前に、別の削除の完了と再登録を割り込ませ、どの位置でも新しい登録とそのキーが残ることを確かめる（インメモリ実装）。

## 根拠（調査結果・出典）

- **名前の文法**（ https://kubernetes.io/docs/concepts/overview/working-with-objects/names/ 、2026-11-18 確認）：Kubernetes は多くのリソース名を DNS サブドメイン名とし、253 文字以内、小文字の英数字と `-`・`.` のみ、先頭と末尾は英数字と定める。path segment name は「`.` と `..` であってはならず、`/` と `%` を含んではならない」とだけ定める。本 ADR は前者を `<name>` に採る。後者は大文字や非 ASCII を許し、[[0020]] がテナント名について退けたのと同じ理由（見分けにくい名前）で採らない。
  - 実装（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apimachinery/pkg/util/validation/validation.go 、2026-11-18 確認）は、ラベルを `[a-z0-9]([-a-z0-9]*[a-z0-9])?`・63 バイト以内とする。サブドメインは、その正規表現を `\.` でつないだものに合い、全体が 253 バイト以内であればよく、要素ごとの 63 バイトの上限は検査しない。文書は RFC 1123 ラベルの先頭を英字と書くが、実装は数字も許す。本 ADR は実装の側と、[[0020]] のテナント名の規則に合わせる。したがってドットの無い 253 バイトの名前も妥当であり、DNS の名前としては使えないものを含む。
- **キーにする前に名前を検査する**（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiserver/pkg/registry/generic/registry/store.go 、2026-11-18 確認）：`NamespaceKeyFunc` と `NoNamespaceKeyFunc` は、名前が空でないことと `content.IsPathSegmentName` を通ることを確かめてから `prefix + "/" + name` を作る。
- **キーの `..` が越境を起こした実例**（CVE-2022-3162、 https://github.com/kubernetes/kubernetes/issues/113756 、2026-11-18 確認）：ある型の namespaced なカスタムリソースをクラスタ全体で list / watch できる利用者が、同じ API グループの別の型のカスタムリソースを権限なく読めた。検出の節は、要求パスに `..` を含む要求を悪用の兆候として挙げる。修正（ https://github.com/kubernetes/kubernetes/pull/113696 。委譲したワーカーによる取得）は、ストレージキーの組み立てを `path.Join` から単純な連結に変え、`..` と `.` を拒否する検査を足した。本 ADR の文法では、`.` だけのセグメントも `..` もラベルの規則で作れない。
- **group はドットを持つ**（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation/validation.go 、2026-11-18 確認）：CRD の group は `IsDNS1123Subdomain` を通ったうえで、ドットで分けた要素が 2 つ未満なら「should be a domain with at least one dot」で拒否される。API conventions（ https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md 。委譲したワーカーによる取得）は、空の group と 1 語だけの group 名を Kubernetes プロジェクト自身の用途に予約する。本 ADR はこの区別を借り、ドットの無い group を `core` だけに限る。
- **kube-apiserver のキーは group の扱いが揃っていない**（ https://github.com/kubernetes/apiserver/blob/master/pkg/server/storage/storage_factory.go 、 https://github.com/kubernetes/apiserver/blob/master/pkg/server/options/etcd.go 。委譲したワーカーによる取得）：組み込みのリソースは既定で `/registry/<resource>/...` と group を省き、CRD は `/registry/<group>/<resource>/...` と group を含める。本 ADR は group を常に 1 セグメントとして持ち、空の group を作らない。
- **修飾つき kind の書き方**：Kubernetes は CRD の名前を `<plural>.<group>` とする（上記の validation.go が、名前と `spec.names.plural + "." + spec.group` の一致を求める。2026-11-18 確認）。本 ADR の `<kind>.<group>` は同じ並びである。
- **LKAL の `Ref`**（`../LKAL/lexer.go` の `lexScanRef` と `lexIsRefSegmentChar`、2026-11-18 確認）：`Ref` は `/` 区切りのセグメント列であり、引用符なしで書けるセグメントのバイトは `[A-Za-z0-9_.-]` である。本 ADR の `<name>` と修飾つき kind はこの範囲に収まるので、`&secret/db-creds.prod/password` や `&widget.widgets.example.com/w1/spec/size` と引用符なしで書ける。名前が `/` を含むと、名前と葉パスの境目が決まらない。
- **identityHash の形**（ https://github.com/kcp-dev/kcp/blob/main/pkg/identity/identity.go 、2026-11-18 確認）：kcp の `IdentityHash` は、identity の秘密鍵に `sha256.Sum256` をかけ、`fmt.Sprintf("%x", …)` で 16 進にする。結果は小文字の 16 進 64 文字である。kcp は bound なリソースの etcd キーにこのハッシュを 1 セグメントとして置く（ https://docs.kcp.io/kcp/main/developers/internals/etcd-structure/ 、2026-11-18 確認）。
- **接頭辞は区切りで終える**（ https://etcd.io/docs/v3.6/learning/api/ 、2026-11-18 確認）：etcd は `range_end` が「key plus one」のとき、その key を接頭辞とするすべてのキーを範囲とする。したがって `/t/a` は `/t/ab` にも一致する。Kubernetes の `PrepareKey` も同じ理由で再帰的な要求のキーを `/` で終える（ https://github.com/kubernetes/apiserver/blob/master/pkg/storage/interfaces.go 。委譲したワーカーによる取得）。
- **フェンスは 1 回の `Txn` で書ける**（ https://etcd.io/docs/v3.6/learning/api/ 、2026-11-18 確認）：etcd の `Txn` の比較は mod revision を対象にでき、「Two different comparisons may apply to the same or different keys」とある。[[0004]] の契約の `Cmp` も任意のキーの mod revision を比べる。リビジョンは単調に増えるので、登録キーが書き換えられたあとに古い mod revision へ戻ることは無い。
- **登録簿の値**：`lkal json` は `state = .active` を `{"$lkal":"0.1","state":{"$enum":"active"}}` に変換する（2026-11-18、手元で実行）。
- **テナントを最上位に置く先行例**（委譲したワーカーによる調査。本 ADR の決定はこれらに依存しない）：CockroachDB はテナントのキーに接頭辞のバイトとテナント ID を前置する（ https://github.com/cockroachdb/cockroach/blob/master/pkg/keys/doc.go ）。TiKV の API v2 はモードの 1 バイトのあとに 3 バイトの keyspace ID を置く（ https://github.com/tikv/rfcs/blob/master/text/0069-api-v2.md ）。FoundationDB のテナントは、互いに交わらない接頭辞を割り当てる（ https://apple.github.io/foundationdb/tenants.html ）。
- **[[0006]] の出典の再確認**（2026-11-18）：NDSS 2026 の論文（ https://arxiv.org/abs/2507.03387 、本文は https://arxiv.org/html/2507.03387 ）で、2,268 件の Operator を解析したこと（§I、§V-A2）、318 件が潜在的に脆弱であること（§V-B1）、「over 14%」「8 confirmations and 7 CVEs by the time of submission」（Abstract）を確認した。§V-C は 7 件の CVE を「割り当て済みまたは割り当て中」と書く。2 つの型の名前は「Insecure Namespace-Scoped Resource Reference」と「Insecure Cluster-Scoped Resource Reference」である。kcp の文書（ https://docs.kcp.io/kcp/main/concepts/apis/exporting-apis/ ）は、`APIExport` と `APIBinding` の例に `apis.kcp.io/v1alpha2` を使い、identity の鍵を秘密に、`status.identityHash` を公開してよいものとして扱う。

## 検討した代替案

- **`<name>` を RFC 1123 のラベル 1 つにする**：最も厳格である。しかし階層をキーに一切持てず、Kubernetes の `foo.example.com` 型の名前を取り込むたびに変換が要るため採らない。
- **`<name>` に `/` を許して複数セグメントにする**：階層の接頭辞走査は自然になる。しかし `ref/` の 5 セグメントを一意に解析できず、LKAL の `Ref` で名前と葉パスの境目が決まらない。[[0006]] が退けた namespace セグメントにも近づくため採らない。
- **`<name>` を path segment name にする**：自由度は高い。しかし大文字や非 ASCII で見分けにくい名前を許し、`Ref` に引用符が要る名前が生まれるため採らない。
- **kind を CamelCase のままキーに使う**：[[0009]] の `meta/kind` の例と字面が合う。しかし `Secret` と `secret` が別のキーになり、`Ref` の `&secret/...` と表記が分かれるため採らない。`meta/kind` との対応づけは [[0009]] の側に残す。
- **組み込みの種別にも DNS 名の group を与える**：規則は一様になる。しかしドメイン名という新しい決定が要り、キーと `Ref` が長くなるため採らない。
- **`ref/` に kind だけを書く**：[[0006]] の字面どおりである。しかし「テナント内で kind 名は group をまたいで一意」という不変条件を別に守る必要があり、守れないと索引が黙って衝突するため採らない。
- **`ref/` の group を独立したセグメントにする**：`o/`・`own/` と対称になる。しかし [[0006]] のキーの形を変え、[[0010]]・[[0013]] の引用も揃え直すことになるため採らない。
- **登録簿の値を空にする**：形式を何も先取りしない。しかし削除中の状態を表せず、2 段階の削除を組めないため採らない。
- **登録簿の値を [[0009]] の封筒にする**：後でリソースとして扱いやすい。しかし [[0009]] が `proposed` のままその形をコードに固定することになるため採らない。
- **ビューを開くときだけ登録を確かめる**：単純で、ストアを分けるときの制約も無い。しかし開いたあとに削除されても古いビューから書き込め、削除のあとにキーが残りうるため採らない。
- **空のテナントだけを削除できるようにする／登録だけを消す**：実装は小さい。しかし前者は掃除を呼び出し側に押しつけ、後者は孤児のデータを残して、同じ名前で登録し直すと古いデータが見えるため採らない。
- **ビューが 6 つの形以外のキーを拒否する**：キー空間の固定を書き込みの経路で強制できる。しかしビューを任意のキーで試す契約テストと DRT が通らなくなる。形の検査は、書き込みを組み立てる上位の層（未実装）がコーデックで行う。

## 影響とトレードオフ

- **得るもの**：
  - キーを一意に解析でき、`..`・`/`・`%` を使った越境をセグメントの文法で作れなくする。
  - 逆参照索引が group をまたいで衝突しない。
  - 削除したテナントのキーが残らず、古いビューが書き込めないことを、ストアの `Txn` だけで保証する。
  - コーデックの正しさを Lean モデルとの差分で確かめられる。
- **失うもの**：
  - 大文字、`_`、非 ASCII を含む名前は使えない。Kubernetes の名前のうち path segment name だけを満たすもの（RBAC の `system:` で始まる名前など）は、取り込み時に変換が要る。
  - 登録簿のビューを通る `Txn` は比較が 1 つ増える。ストアの上限ちょうどの比較を持つ `Txn` は `ErrTooLarge` になる。
  - 比較が成り立たなかった `Txn` は、登録キーの読み直しを 1 回伴う。
- **引き受けるリスク**：
  - フェンスは `/t/<tenant>/` の外のキー（`/sys/tenants/<tenant>`）を比べる。[[0005]] の G2 でテナントを別のストアへ切り出すと、ストアをまたぐ比較はできないので、フェンス用の登録の写しをそのストアに置く設計が要る。
  - 素のテナントビューや、クラスタ全体の `store.Store` を持つコードは、フェンスを通らない。そこからの書き込みは、削除中のテナントにもキーを残せる。
  - 登録簿の値は [[0009]]・[[0012]] より先に決めた。両者が `accepted` になれば、値の形を置き換える ADR が要る。
  - 読み取りにはフェンスが無い。古いビューは、削除されたテナントを空として読む。
  - エラーで返った `Txn` は結果不明である（[[0020]]）。登録がエラーで返ったあとに呼び直すと、自分の書き込みが適用済みで「すでにある」と返ることがある。削除も同じで、最後の `Txn` がエラーで返ったあとに呼び直すと、適用済みで「未登録」と返ることがある。
  - 読めない値を持つ登録は、登録簿の操作では消せない。登録も削除もビューも誤りを返す。直すには、クラスタ全体の `store.Store` でそのキーを書き直す。
  - 削除は 1 回の `Txn` で最大 64 個のキーを消す。ストアが要求の大きさで拒否したら（`ErrTooLarge`）、個数を半分にしてやり直す。キー 1 個の削除も拒否されたら、削除はその誤りで止まる。登録簿のビューを通った書き込みは同じ長さの比較を伴うので、そのキーの削除は書き込みより大きくならない。
  - 削除の `Range` は値も読む。[[0004]] の契約にキーだけを読む操作が無いためである。大きな値を持つテナントの削除は、その分の読み取りを伴う。
- **今後の検証ゲート**：[[0006]] が G0 に置いた越境 `Ref` の否定試験は、`Ref` の解決（[[0010]]）が実装されたときに行う。本 ADR が確かめるのは、キーの段で越境を作れないことまでである。G2 でテナントを別ストアへ切り出すとき、フェンスの置き方を見直す。

## 関連ADR

- [[0004]]：フェンスと登録が使う `Txn` の比較と、キーの順序を定める。
- [[0005]]：G2 でのストア分割を定める。フェンスの置き方に影響する。
- [[0006]]：本 ADR が実現するキー空間を定める。
- [[0009]]：リソースの封筒を定める提案。登録簿の値はこれに従っていない。
- [[0010]]：`Ref` の検査と逆参照索引の更新規則を定める提案。本 ADR の修飾つき kind を索引のキーに使う。
- [[0012]]：ワイヤ形式を定める提案。登録簿の値は LKAL-JSON である。
- [[0013]]：所有権キーの更新規則を定める提案。
- [[0020]]：素のテナントビューを定める。本 ADR はそれを残し、登録簿のビューを足す。
- [[0027]]：登録簿の値の復号・検証に `lkal.FromJSON` と LKAL v0.2 スキーマ（`tenant.schema.lkal`）を適用する。
