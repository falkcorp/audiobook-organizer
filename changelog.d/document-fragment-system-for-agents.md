### Changed

#### Document the changelog/TODO fragment system for AI agents

`CLAUDE.md` and `.github/copilot-instructions.md` now instruct AI agents to use
the `changelog.d/` and `todo.d/` fragment systems instead of editing
`CHANGELOG.md` or the `TODO.md` inbox directly, preventing parallel-PR
collisions on those files.
