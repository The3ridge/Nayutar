# spec — Lean 仕様

`spec/` で作業するときの指示。共通の指示はリポジトリのルートの `AGENTS.md` にあり、ここには繰り返さない。パスはリポジトリのルートからの相対で書いてある。

- `spec/Nayutar/Statements.lean`、`spec/Nayutar/Statements/`、`spec/comparator.json`、`spec/Nayutar/Fixture.lean`、`spec/Nayutar/Fixture/`、`spec/lakefile.toml`、`spec/lakefile.lean`（両方あるとき Lake は `lakefile.lean` を優先する）、`spec/lake-manifest.json`、`spec/lean-toolchain` はユーザーの所有であり、エージェントは編集しない（ハーネスがブロックする）。
- 証明は `spec/Nayutar/Proofs/` に同じ定理名・同じ主張で書く。`sorry` は Statements 側にだけ置く。
- 主張が import するモデル定義（記法やマクロを含む）を変えると、主張の意味も変わる。`lake comparator` はこの差を検出しないので、そうした変更は主張の変更として扱い、ユーザーに明示してレビューを受ける。主張が今 import しているモデル（`spec/Nayutar/Fixture/` と `spec/Nayutar/Fixture.lean`）と Lean のビルド設定（`spec/lakefile.toml`、`spec/lakefile.lean`、`spec/lake-manifest.json`、`spec/lean-toolchain`）は、ハーネスが編集を止める。主張が今後 import する他のモデル定義には、この既存の規則（主張の変更として扱いレビューを受ける）がそのまま当てはまる。
- 定理の主張の一致と許容公理の判定は、`cmd/check-protected-theorems` をビルドして実行したあと `(cd spec && lake comparator)` を実行する。事前検査は、`spec/comparator.json` の定理が主張モジュールと証明モジュールの両方に宣言されていないとき、`missing protected theorem` と終了コード 1 で拒否する。Lean 4.35.0 の `lake comparator` 自体は、その欠落で子プロセスが panic（終了コード 134）し、`error: Child exited with 134` のあと終了コード 1 で戻る。実行にはユーザー名前空間を使える bubblewrap（`bwrap`）が必要であり、`--inadvisably-no-sandbox` は使わない。
- モデルと Go の実装の対応、DRT の実行は `core/drt/AGENTS.md` にある。
