### Added

#### Adopt the `todo.d/` fragment system for `TODO.md`

New tasks are now added by dropping a uniquely-named Markdown fragment in
`todo.d/` instead of editing `TODO.md` directly, so parallel PRs no longer
collide on the TODO list — the same fragment-per-change model this repo already
uses for `CHANGELOG.md`.

`scripts/assemble_todo.py` folds fragments in below the
`<!-- todo-insert-here -->` marker and deletes the ones it consumed;
`.github/workflows/todo-collect.yml` runs it daily and on `workflow_dispatch`.
The system is add-only (checking a task off stays a direct edit) and opt-in by
presence of `todo.d/todo.ini`.
