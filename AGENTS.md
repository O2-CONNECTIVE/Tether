# 共通の開発入口

開始・再開・編集委譲・レビュー時に[共通エージェント開発手順](docs/agent-workflow.md)を読み、担当範囲と検証境界を確認してください。

検証用の worktree・clone・一時ディレクトリは、このリポジトリの `.claude/worktrees/` か、セッションの scratchpad または `$TMPDIR` に作ってください。ホーム直下（`~/tether-*` など）には作りません。使い終えたら `git worktree remove` で片付けてください。
