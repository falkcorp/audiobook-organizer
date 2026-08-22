<!-- file: docs/agent-tasks/todo-completion/config/TASK-020-delete-the-fully-inert-enable-sqlite3-i-know-the.md -->
<!-- version: 1.0.0 -->
<!-- guid: af7eea9a-3840-4012-bb51-c3243c6418f9 -->
<!-- last-edited: 2026-08-21 -->

# TASK-020 — Delete the fully inert --enable-sqlite3-i-know-the-risks flag and EnableSQLite config option (CFG-AUDIT)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · config subagent · **Why:** Pure removal but touches ~8 files (flag registration, config struct, 5 call sites passing the dead param, a test that stubs the function) — needs care to not break the initializeStore signature for its remaining real use (dbType, path). · **Depends on:** none · **Wave:** 4

Source: `TODO.md` line 1317 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**CFG-AUDIT**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/config-020-delete-the-fully-inert-enable-sqlite3-i-know-the" -b agent/config-020-delete-the-fully-inert-enable-sqlite3-i-know-the origin/main
cd "$REPO/.worktrees/config-020-delete-the-fully-inert-enable-sqlite3-i-know-the"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Remove --enable-sqlite3-i-know-the-risks (CLI flag), EnableSQLite (config.go field, viper bind), and the now-pointless third parameter of InitializeStore/initializeStore, since the SQLite backend is gone and this flag has never changed behavior. Also fix or remove the InitializeStore error message's reference to the nonexistent migrate-from-sqlite subcommand.

## Background (verify before editing)

- cmd/root.go:41 — `initializeStore = database.InitializeStore` (function-pointer indirection for testability).
- cmd/root.go:351 — flag registration `rootCmd.PersistentFlags().BoolVar(&enableSQLite, "enable-sqlite3-i-know-the-risks", false, ...)`.
- cmd/root.go:358 — `viper.BindPFlag("enable_sqlite3_i_know_the_risks", ...)`.
- internal/config/config.go:627 — `EnableSQLite bool `json:"enable_sqlite"``, and :1638 `EnableSQLite: viper.GetBool("enable_sqlite3_i_know_the_risks")`, and :2290 `EnableSQLite: false` in ResetToDefaults.
- 5 call sites pass `config.AppConfig.EnableSQLite` / `snap.EnableSQLite` into initializeStore: cmd/child_mode.go:71, cmd/dedup_bench.go:109 area, cmd/seed.go:95, cmd/root.go:86/118/143/179/236.
- cmd/commands_test.go:38,276 — test doubles for `initializeStore` that still accept `enableSQLite bool` in their signature.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func InitializeStore' internal/database/store.go   # 1 hit at L1306, signature ends `_ bool` — InitializeStore discards the flag
  grep -n 'SQLite3 support has been removed' internal/database/store.go   # 1 hit ~L1312 — sqlite path always errors regardless of flag
  grep -rn 'Use("migrate-from-sqlite"\|"migrate-from-sqlite",' cmd/*.go   # 0 hits — migrate-from-sqlite subcommand referenced in the error does not exist
  grep -n 'enable-sqlite3-i-know-the-risks' cmd/root.go   # 2 hits: PersistentFlags().BoolVar(...) and viper.BindPFlag(...) — flag registration site
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. internal/database/store.go:1306 — change signature from `func InitializeStore(dbType, path string, _ bool) (Store, error)` to `func InitializeStore(dbType, path string) (Store, error)`, removing the unused parameter.
2. internal/database/store.go:1311-1312 — improve the error to not reference a nonexistent subcommand: change to `return nil, fmt.Errorf("SQLite3 support has been removed; PebbleDB is the only supported database backend")` (drop the migrate-from-sqlite mention, or replace it with an actual pointer if one exists elsewhere — grep first: `grep -rn 'migrate.from.sqlite\|MigrateFromSQLite' --include=*.go .`).
3. cmd/root.go — remove the `enableSQLite` var, the `PersistentFlags().BoolVar(&enableSQLite, "enable-sqlite3-i-know-the-risks", ...)` registration at L351, and the `viper.BindPFlag("enable_sqlite3_i_know_the_risks", ...)` at L358.
4. Update all 5+ call sites (cmd/root.go:86,118,143,179,236; cmd/child_mode.go:71; cmd/dedup_bench.go; cmd/seed.go:95) to drop the third argument: `initializeStore(dbType, path)`.
5. internal/config/config.go — remove the `EnableSQLite` field (L627), its viper.GetBool load (L1638), and its ResetToDefaults entry (L2290).
6. cmd/commands_test.go:38,276 — update the `initializeStore = func(dbType, path string, enableSQLite bool) (database.Store, error) { ... }` test-double signatures to drop the third parameter, and update all callers within the test file accordingly.
7. Grep .env.example and docs for any mention of this flag/env var and remove or update.

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_config_020.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Anyone with `--enable-sqlite3-i-know-the-risks` in a saved script/systemd unit will get a flag-parse error after this change (`unknown flag`) rather than being silently ignored as before — that is the correct outcome (fail loud on a flag that never did anything) but worth a changelog note.

## Tests

- cmd/commands_test.go — existing tests should continue to pass after the signature change; re-run to confirm no other test double needs updating.

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./cmd/... ./internal/config/... ./internal/database/... ./internal/database/mocks/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go build ./...` succeeds.
- [ ] `grep -rn 'enable.sqlite3.i.know.the.risks\|EnableSQLite' --include=*.go .` returns 0 hits.
- [ ] `go test ./cmd/...` passes.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./cmd/... ./internal/config/... ./internal/database/... ./internal/database/mocks/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_config_020.md`.

## Commit message

```
refactor(config): Delete the fully inert --enable-sqlite3-i-know-the-risks fla (CFG-AUDIT)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

This is a clean removal once decided; the only judgment call is what the error message should say about migration since the referenced subcommand never existed — flag that specific wording choice for the owner rather than inventing a migration story.
