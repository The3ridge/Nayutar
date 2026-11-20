# CLAUDE.md

@AGENTS.md

## Claude Code 固有の補足

- ADR を書くときは `/alt-adr-writer` を使う。ADR の参照・検証には docdag プラグインの `docdag:docdag` スキルを使う（プラグインの PostToolUse フックが ADR の編集を検証する）。
- MCP サーバ `antigravity` と `kisage` は `.mcp.json` に登録し、`.claude/settings.local.json` の `enabledMcpjsonServers` で有効にしている。どちらも gitignore 済みの個人設定で、コミットしない。worktree にはこの 2 つを `.worktreeinclude` でコピーする。
- フックと deny ルールは追跡済みの `.claude/settings.json` にある。個人の上書きは `.claude/settings.local.json` に書く。
- 長いセッションやコンテキスト圧縮のあとは `/reload` で AGENTS.md・ルール・有効な ADR を読み直す。
- 実装やレビューを委ねるときは、まず並列に出せるかを考える。設計レビューと差分レビューは Opus のサブエージェントで行い、指摘はコードを実行して確かめる。
