---
title: 封筒を、小文字の kind・2 つの書き込みの入口・純粋な遷移の核・オブジェクトのストア結合で実現する
status: proposed
date: 2026-11-21
depends-on: ["0004", "0006", "0009", "0022"]
---

# 0025: 封筒を、小文字の kind・2 つの書き込みの入口・純粋な遷移の核・オブジェクトのストア結合で実現する

## コンテキスト

[[0009]] は、リソースの封筒を `meta`・`spec`・`status` の 3 レコードに固定し、サーバ専用欄（`meta/uid`・`meta/revision`・`meta/generation`・`status/observedGeneration`・`status/condition/<Type>`）への利用者の書き込みを禁じた。[[0009]] が決めたのは封筒の形と禁止の方針までであり、実装に要る次の点は決めていない。

- `meta/kind` の字面と、キーの `<group>`・`<kind>`（[[0006]]・[[0022]]）への対応づけ。[[0022]] は、キーの `<kind>` を小文字のラベルにしたうえで、この対応づけを [[0009]] の側に残した。
- 利用者の文書にサーバ専用欄が含まれていたときの扱い（拒否するか、無視するか）。
- 列挙された欄以外の `status` の葉を、利用者が書けるか。
- `meta/revision` をどこに持つか。ストアの mod revision（[[0004]]）は、書き込む前には分からない。
- `generation`・`observedGeneration`・`since` を、誰がいつ決めるか。
- 各欄の文法（`uid`、`<Type>`、`reason`、`since`）。

本 ADR は [[0009]] の実現方式を決める。HTTP の API サーバ、認可、`Ref` の検査（[[0010]]）、Patch（[[0012]]）、所有権（[[0013]]）、スキーマは扱わない。

## 決定

1. **型は小文字で 1 通りに書く。**
   - `meta/kind` は、キーの `<kind>` と同じ小文字のラベルである（[[0022]] の文法）。`meta/name` はキーの `<name>` と同じ DNS サブドメインである。
   - `meta/group` は任意の欄で、不在は `core` を表す。書く場合は、ドットを含むサブドメインに限る。`group = "core"` と明示した文書は拒否する（`Ref` が `core` を省略でしか書けないのと同じく、表現を 1 つにするため）。
   - キーは `/t/<tenant>/o/<group>/<kind>/<name>` であり、`meta` からの対応づけは恒等である。大文字小文字の変換はどこにも無い。表示用の名前が要るなら、スキーマの側に持つ。
   - [[0009]] の例の `kind = "Workload"` は、受理の前に `kind = "workload"` に直した。
2. **`meta` の欄は閉じた集合とする。** `group`・`kind`・`name`・`labels`・`uid`・`revision`・`generation` 以外のキーを持つ `meta` は拒否する。`labels` は、値がすべて String のレコードである。ラベルのキーと値の文法は、セレクタを決める ADR に残す。
3. **トップレベルは `meta`・`spec`・`status` だけで、どれもレコードである。** `meta` は必須、`spec` と `status` は任意である。不在と `{}` は別の値として扱う。
4. **書き込みの入口を 2 つに分ける。**
   - `Update`（meta と spec の入口）は、`meta` の利用者の欄（`labels`）と `spec` を置き換える。`status` は保存済みのものを引き継ぐ。
   - `UpdateStatus`（status の入口）は、`status` を置き換える。`meta` と `spec` は保存済みのものを引き継ぐ。
   - `Create` は、`status` を持たない文書だけを受け付ける。
   - どの入口を誰が使えるかは、認可（未実装）が決める。本 ADR は入口ごとに変えられる範囲だけを決める。
5. **入口の外の欄と、サーバが決める欄は、「不在か、現在値と同じ」でなければ拒否する。** 黙って無視はしない。
   - 不在は「その欄には触れない」を表す。したがって、読んだ文書を編集してそのまま書き戻せる。
   - `meta/revision` が現在の mod revision と違えば、競合として拒否する。これが楽観ロックであり、[[0012]] の Patch 例の `test meta/revision` と同じ意味である。
   - 作成時は現在値が無いので、`meta/uid`・`meta/revision`・`meta/generation`・`status` を含む文書を拒否する。
   - 同一性（`group` の有無と値・`kind`・`name`）が保存済みの文書と違う書き込みは拒否する。
6. **サーバが決める値は次のとおりとする。**
   - `meta/uid`：作成時に 1 回だけ振る UUID 第 4 版（小文字の正規の表記）。以後は変わらない。
   - `meta/generation`：作成時に 1。`Update` で `spec` が LKAL の `Equal` で変わったときだけ 1 増える。`labels` だけの変更と `UpdateStatus` では変わらない。
   - `meta/revision`：保存しない。読み出すときにキーの mod revision を入れ、書き込む前に取り除く。
   - `status/condition/<Type>/since`：`UpdateStatus` が決める。同じ `<Type>` の保存済みの条件と `state` が同じなら保存済みの値を引き継ぎ、そうでなければ書き込み時刻にする。書き込む側が `since` を指定できるのは、保存済みの値と同じ値を送り返す場合だけである。
7. **`status/observedGeneration` は、書き込む側が決め、範囲をサーバが検査する。** `generation` を超える値、保存済みの値より小さい値、保存済みの値があるのに欄を消す書き込みは拒否する。
8. **状態条件の形を固定する。**
   - `<Type>` は `[A-Z][A-Za-z0-9]*`、1〜63 バイト。Kubernetes の条件型（修飾なし）と 1 対 1 に写る。
   - 条件のレコードは `state`・`reason`・`since` だけを持つ。`state` は Enum の `.true`・`.false`・`.unknown`。`reason` は `[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?` に合う 1〜1024 バイトの String（Kubernetes と同じ）。`since` は `YYYY-MM-DDTHH:MM:SSZ`（UTC、秒まで）の String。
   - 大文字小文字だけが違う `<Type>` の併存は検査しない。
9. **検査の順序を固定する。** 誤りは 5 種類（`internal`・`malformed`・`conflict`・`forbidden`・`observed`）で、次の順に調べ、最初に当たったものを返す：サーバ側の入力（保存済みの文書の形、UID、時刻）→ 候補の形 → 同一性 → `meta/revision` → 入口の外の欄 → `observedGeneration`。
10. **どのレコードも、同じキーを 2 度持てない。** 文書の全階層で検査し、重複があれば形の誤りとして拒否する。
11. **遷移の核を純粋なパッケージ `core/resource/envelope` に置く。** `Create`・`Update`・`UpdateStatus` は、保存済みの文書・リビジョン・候補（と UID か時刻）から新しい保存済みの文書か誤りを返す関数であり、ゴルーチン・時計・乱数・ストアを持たない。値の木には LKAL の Go モジュール（`github.com/Kaikei-e/LKAL`）の型と `Equal` を使う。
12. **ストア結合を `core/resource` に置く。** `Objects` は、テナントのビュー（[[0020]] の素のビューか [[0022]] の登録簿のビュー）の上で、封筒をオブジェクトのキーに読み書きする。
    - 保存形式は LKAL-JSON（空白なし、先頭が `"$lkal":"0.1"`）で、読み書きは LKAL のライブラリだけで行う。
    - 候補は、保存形式に符号化して読み戻した正規形に直してから核に渡す。LKAL は数値（Decimal と Quantity）を正規化するので、こうしないと、同じ候補をもう一度書いたときに `spec` が変わったと判定される。LKAL が受け付けない値（`$` で始まるキー、型の混ざったリスト、レコードを持つリスト）は形の誤りとして返す。
    - 作成は「キーが無い」ことを、更新と削除は「mod revision が読んだ値に等しい」ことを比べる 1 回の `Txn` で行う。比較が通らなければ競合として返し、暗黙の再試行はしない（[[0013]] の決定 2 と同じ）。
    - 結果が保存済みの文書と `Equal` なら、書き込まない。
    - 読めない値、保存済みの形を満たさない値、`meta` の同一性がキーと合わない値は誤りとして返す（fail-closed）。
    - 時計と乱数は設定から受け取る（[[0015]]）。
    - 削除は、キーを消す 1 回の `Txn` である。猶予つきの削除とファイナライザは無い。
13. **核を Lean モデルと照合し、更新規則を定理にする。**
    - `spec/Nayutar/Envelope.lean` に同じ規則を書き、`nayutar-drt` の `envelope` スイートとして実行する。Go 側は、妥当な文書とそれを壊した文書、欄の順序の入れ替え、検査の順序を突く組み合わせを乱数で作り、成否・誤りの種類・結果の文書が Lean と一致することを確かめる。
    - モデルの上の定理 10 個（`Nayutar.Envelope`）を保護の対象にする：`create_is_stored`・`update_is_stored`・`update_status_is_stored`（受け付けた結果は保存済みの形を満たす。したがって `observedGeneration` は `generation` を超えない）、`create_starts_at_generation_one`、`update_keeps_uid_and_status`、`update_bumps_generation_iff_spec_changes`、`update_status_keeps_meta_and_spec`、`update_status_observed_is_monotone`、`update_status_since_follows_state`、`stale_revision_is_rejected`。
    - 定理が述べるのはモデルである。Go の核とは DRT で、ストア結合とは試験で結ぶ。LKAL-JSON の符号化はモデルに含めない。

## 根拠（調査結果・出典）

- **Kind から resource への対応づけは壊れやすい**（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apimachinery/pkg/api/meta/restmapper.go 、2026-11-21 確認）：`UnsafeGuessKindToResource` は Kind を `strings.ToLower` して resource を作り、コメントは「Broken. This method only "sort of" works when used outside of this package. It assumes that Kinds and Resources match and they aren't guaranteed to do so.」と書く。API conventions（ https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md 、2026-11-21 確認）も「Relying on dynamic kind to resource mapping is not safe.」と書く。
- **Kubernetes の kind は大文字小文字を区別して比べられる**（2026-11-21 確認）：CRD の検査（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/validation/validation.go ）は、`kind` を `IsDNS1035Label(strings.ToLower(names.Kind))` で調べ、誤りの文言は「may have mixed case」である。命名コントローラ（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiextensions-apiserver/pkg/controller/status/naming_controller.go ）は、受理済みの `Kind` と `ListKind` を `sets.String` に入れ、文字列の完全一致で衝突を調べる。したがって同じ group に `Foo` と `FOO` を置ける。
- **検証で大文字小文字を無視した比較をしない**（上記 API conventions、2026-11-21 確認）：「Never do case-insensitive comparisons in validation. This is a source of bugs and failed uniqueness assumptions.」。本 ADR が、大小だけが違う `<Type>` の併存を検査しない理由である。
- **型名の大小の揺れが作り直しを起こした実例**（ https://github.com/hashicorp/terraform-provider-azurerm/issues/32778 、2026-07-13 起票、2026-11-21 確認）：Azure の API が返す ID の型のセグメントが `devcenters`、リソースの出力が `devCenters` で、Terraform はこれを別の値と見て置き換えを計画した。
- **1 つの字面に寄せる**（RFC 4343、 https://www.rfc-editor.org/rfc/rfc4343 、2026-11-21 確認）：大小だけが違う名前を別の名前と思い込む混乱を挙げ、ASCII のラベルを「some specific pre-chosen case」に写すことを勧める。小文字の型名だけを使う先行例に Terraform のリソース型（`aws_instance`）、systemd のユニット型の接尾辞がある（委譲したワーカーによる調査。本 ADR の決定はこれらに依存しない）。
- **spec と status の入口を分ける**（上記 API conventions、2026-11-21 確認）：「The PUT and POST verbs on objects MUST ignore the `status` values, to avoid accidentally overwriting the `status` in read-modify-write scenarios. A `/status` subresource MUST be provided to enable system components to update statuses of resources they manage.」。本 ADR は分け方を借り、無視の代わりに拒否する。
- **generation が増える条件は Kubernetes でも型ごとに違う**（2026-11-21 確認）：カスタムリソース（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiextensions-apiserver/pkg/registry/customresource/strategy.go ）は、`/status` があれば `status` を古い値に戻したうえで、「except for the changes to `metadata`, any other changes cause the generation to increment」とする。Deployment（ https://github.com/kubernetes/kubernetes/blob/master/pkg/registry/apps/deployment/strategy.go ）は `Spec` か `Annotations` が変わると増やす。本 ADR は [[0009]] のとおり `spec` だけを条件にする。
- **Kubernetes は利用者が送った generation を黙って捨てる**（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiserver/pkg/registry/rest/update.go 、2026-11-21 確認）：`BeforeUpdate` は「Ensure requests cannot update generation」として古い値に戻す。UID の変更は検査（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apimachinery/pkg/api/validation/objectmeta.go ）が不変の欄として拒否する。本 ADR は両方を拒否にそろえる。
- **リビジョンは値に保存しない**（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiserver/pkg/storage/api_object_versioner.go 、2026-11-21 確認）：`PrepareObjectForStorage` は「clears resourceVersion and selfLink prior to writing to etcd」であり、読み出し時に etcd の `ModRevision` から入れ直す（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiserver/pkg/storage/etcd3/store.go ）。同じファイルは、符号化した結果が保存済みのバイト列と等しければ書き込みを省く（`if !origState.Stale && bytes.Equal(data, origState.Data)`）。
- **条件の文法と遷移時刻**（2026-11-21 確認）：`metav1.Condition`（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apimachinery/pkg/apis/meta/v1/types.go ）は、`status` を `True;False;Unknown`、`reason` を `^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$`・1024 文字以内とする。`SetStatusCondition`（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apimachinery/pkg/api/meta/conditions.go ）は、`existingCondition.Status != newCondition.Status` のときだけ `LastTransitionTime` を変える。API conventions は「Condition types should be named in PascalCase.」と書き、条件の集まりを「treated as a map with a key of `type`」とする。Kubernetes は `type` に `foo.example.com/` の接頭辞も許すが、本 ADR は許さない（`/` が LKAL のパスの区切りだからである）。
- **UID の版**（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apiserver/pkg/registry/rest/meta.go 、2026-11-21 確認）：Kubernetes は作成時に `uuid.NewUUID()` を呼ぶ。これは第 4 版である（ https://github.com/kubernetes/kubernetes/blob/master/staging/src/k8s.io/apimachinery/pkg/util/uuid/uuid.go 。委譲したワーカーによる取得）。RFC 9562（ https://www.rfc-editor.org/rfc/rfc9562 、2026-11-21 確認）は、時刻順でない第 4 版が「poor database-index locality」を持つと書く。UID は索引のキーにしないので、時計を要らず作成時刻も漏らさない第 4 版を採る。
- **LKAL の公開 API だけで読み書きできる**（`../LKAL` の `json.go`・`path.go`・`value.go`、2026-11-21 確認）：`FromJSON` は 9 型の木を返し、`$` で始まるキー（`L205`）と重複したキー（`L201`）を拒否する。`Set` は Record の値を受け取り、`ToJSON(f, false)` が先頭に `$lkal` を置く。`Equal` はレコードの欄の順序を無視する。LKAL の `v0.2.0` リリースに伴い、依存をリリースタグ `v0.2.0` に更新した（[[0027]]）。
- **手元での確認**（2026-11-21）：`lake build` で Lean モデルと 10 定理の証明（`sorry` なし）が通ること、`nayutar-drt envelope` が [[0009]] の例を受け付け、`kind = "Workload"` と `group = "core"` を形の誤りとして拒否することを確かめた。

## 検討した代替案

- **`meta/kind` を CamelCase にし、キーにはその小文字を使う**：[[0009]] の当初の例と Kubernetes の字面に合う。しかし `Workload` と `WorkLoad` が同じキーに写り、対応づけが恒等でなくなる。大小だけの違いは運用の誤りを招くため採らない。
- **CamelCase の kind と、スキーマに登録した小文字の名前を対にする**（Kubernetes の CRD）：Go の型名と字面が合う。しかし発見と対応表が要り、Kubernetes 自身が推測の対応づけを壊れていると書いているため採らない。
- **修飾つきの `meta/type` 1 つにする**（`widget.widgets.example.com`）：`Ref` の修飾つき kind と同じ文字列で、group と kind が食い違わない。しかし 1 つの葉に 2 つの情報が載り、葉パス所有権（[[0013]]）の単位と合わないため採らない。
- **`meta/group` を必須にする**：暗黙の既定が無くなる。しかし組み込みの型でも毎回 `group = "core"` を書くことになり、`Ref` の省略の規則とも食い違うため採らない。
- **サーバ専用欄が含まれていれば常に拒否する**：最も厳格である。しかし読んだ文書を書き戻すたびに欄を取り除く必要があり、`meta/revision` を楽観ロックに使えないため採らない。
- **サーバ専用欄を黙って無視する**（Kubernetes）：互換の感覚に近い。しかし利用者の誤りが表に出ず、LKAL の「推測しない」原則に合わないため採らない。
- **禁止を [[0009]] が列挙した欄だけにする**：字面どおりで制約が最小である。しかし観測状態を利用者が書き換えられ、境界が所有権と認可の実装まで開いたままになるため採らない。
- **`since` を書き込む側に決めさせる**（Kubernetes）：もとの事象の時刻を書ける。しかし時計が書き手ごとに分かれ、「状態が変わったときだけ変わる」をサーバが保証できないため採らない。
- **`meta/revision` を値に保存する**：読み出しが単純になる。しかし mod revision は書き込む前に分からず、値とキーの 2 か所に食い違いうる値を持つことになるため採らない。
- **UUID 第 7 版にする**：時刻順で索引に優しい。しかし UID を索引のキーにしておらず、時計への依存と作成時刻の露出だけが増えるため採らない。
- **`<Type>` を小文字のラベルにする**：大小の揺れを文法で消せる。しかし Kubernetes の条件型を取り込みと差分試験（[[0003]]）のたびに変換することになるため採らない。
- **大小だけが違う `<Type>` の併存を拒否する**：`Ready` と `READY` の併存を防げる。しかし Kubernetes の規約が戒める大小無視の比較を検査に足すことになるため採らない。
- **LKAL-JSON の読み書きを Nayutar の側に書く**：依存が増えない。しかし `$enum`・`$qty`・`$ref` と誤りコードを二重に実装し、LKAL とずれるため採らない。
- **純粋な核だけを作り、ストア結合を後に回す**：差分は小さい。しかし `meta/revision` を mod revision から導く規則と UID の採番が、実物のストアの上で未検証のまま残るため採らない。
- **Lean モデルで欄の順序を固定し、DRT の入力を正規化する**：モデルが単純になる。しかし順序を無視する比較そのものが照合の対象から外れるため採らない。モデルは、欄を名前で引いて比べる。

## 影響とトレードオフ

- **得るもの**：
  - 型の字面が文書・キー・`Ref` で 1 通りになり、大小の揺れを文法で作れない。
  - 読んだ文書をそのまま書き戻せ、`meta/revision` が楽観ロックになる。
  - 更新規則（generation、observedGeneration、since、サーバ専用欄の不変）が Lean の定理になり、Go の核が DRT でそれに結ばれる。
  - 封筒の検査を、後続の `Ref` 検査・Patch・所有権が共通の土台として使える。
- **失うもの**：
  - Kubernetes の `kind: Deployment` は、取り込みのたびに小文字の kind へ変換が要る。変換表は取り込みを決める ADR が持つ。
  - 条件型の接頭辞（`foo.example.com/Ready`）と `message` 欄は表せない。
  - 利用者は、`status` を `Update` で一切変えられない。
- **引き受けるリスク**：
  - 入口は文書全体を置き換える。前提（`meta/revision`）を付けない書き込みは、あいだに入った他の書き込みの結果を上書きしうる。複数の書き手が別々の条件を書くときは、前提を付けるか、[[0012]] の Patch と [[0013]] の所有権が入るまで読み直してやり直す。
  - 前提を付けない書き込みも、読んでから書くまでに他の書き込みが入れば競合で返る。再試行は呼び出し側が行う。
  - エラーで返った `Txn` は結果不明である（[[0020]]）。作成がエラーで返ったあとに呼び直すと「すでにある」と返ることがある。
  - `since` は書き込むプロセスの壁時計である。時計が戻れば、後の遷移が前の遷移より古い時刻を持ちうる。月ごとの日数は検査しない。
  - ラベルのキーと値の文法を決めていない。後から狭めると、保存済みの文書が形の誤りになりうる。
  - 素のテナントビューと、クラスタ全体の `store.Store` を持つコードは、`Objects` を通らずにキーを書ける。そこから書かれた値が保存済みの形を満たさなければ、`Objects` はその読み書きを誤りで返す。
  - 定理が述べるのは Lean モデルである。モデルの定義（`shape`・`stored`・`Value.eqv` など）が主張の意味を決めるので、定義の変更は主張の変更として扱う。
  - 核と Lean モデルの形の検査は、封筒の規則だけを見る。LKAL として妥当な値か（リストの要素の型、`$` で始まるキー、単位の無い Quantity）は検査せず、ストア結合が候補を正規形に直すときに拒否する。核だけを使う呼び出し側は、この検査を受けない。DRT の生成器は LKAL として不正な値も作り、核とモデルの一致をその上でも確かめる。
  - モデルの整数には上限が無い。Go は `int64` であり、`generation` が上限に達した文書の更新を `internal` の誤りで返す。
  - 削除は即時で、依存するオブジェクトの扱いと猶予は無い。
- **今後の検証ゲート**：[[0009]] が G0 に置いた「封筒構造と `generation`／`observedGeneration` の更新規則の検証」は、本 ADR の定理・DRT・ストア結合の試験が担う。Kubernetes との差分試験（[[0003]]）で、取り込みの変換と条件の対応を確かめる。3 区分で表せない必須の要件が見つかれば、[[0009]] とともに見直す。

## 関連ADR

- [[0003]]：実装を Lean モデルとの差分試験で保証する方針を定める。本 ADR の DRT と定理はこれに従う。
- [[0004]]：`Txn` の比較と mod revision を定める。ストア結合の書き込みはこれを使う。
- [[0006]]：オブジェクトのキーの形を定める。
- [[0009]]：本 ADR が実現する封筒を定める。
- [[0010]]：`Ref` の検査を定める提案。封筒の中の `Ref` の値は、本 ADR では検査しない。
- [[0012]]：ワイヤ形式と Patch を定める提案。`test meta/revision` と本 ADR の楽観ロックは同じ意味である。
- [[0013]]：所有権の更新を定める提案。実装されれば、ストア結合の `Txn` に所有権のキーを足す。
- [[0015]]：時計と乱数を外から渡す規則を定める提案。
- [[0020]]：素のテナントビューを定める。
- [[0022]]：キーの文法とコーデック、登録簿のビューを定める。`meta/kind`・`meta/name`・`meta/group` の文法はこれを使う。
- [[0027]]：封筒の共通 LKAL v0.2 スキーマ（`envelope.schema.lkal`）および `core/resource` における種別ごとのスキーマ検証と `default` 補完を定める。
