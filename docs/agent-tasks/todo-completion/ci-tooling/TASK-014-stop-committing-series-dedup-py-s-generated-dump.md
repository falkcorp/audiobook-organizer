<!-- file: docs/agent-tasks/todo-completion/ci-tooling/TASK-014-stop-committing-series-dedup-py-s-generated-dump.md -->
<!-- version: 1.0.0 -->
<!-- guid: de40b02c-580c-4787-b38c-8f12c854a27e -->
<!-- last-edited: 2026-08-21 -->

# TASK-014 — Stop committing series_dedup.py's generated dump/fix cache files (REPO-SIZE-1)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · ci-tooling subagent · **Why:** git rm + gitignore two lines; investigation already confirmed no downstream Go consumer · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 10632 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**REPO-SIZE-1 decision**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/ci-tooling-014-stop-committing-series-dedup-py-s-generated-dump" -b agent/ci-tooling-014-stop-committing-series-dedup-py-s-generated-dump origin/main
cd "$REPO/.worktrees/ci-tooling-014-stop-committing-series-dedup-py-s-generated-dump"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Remove testdata/series_dump.json and testdata/series_fix.json from git tracking and add them to .gitignore so scripts/series_dedup.py's local run output stops being committed, closing out the 'externalize live large fixtures' step of the owner-approved forward-only Option (d) plan — using the simpler correct fix (gitignore, not a downloader) once investigation showed they are not real test fixtures.

## Background (verify before editing)

- docs/plans/2026-07-10-repo-size-history-rewrite-plan.md:207 proposed 'Externalize live large fixtures (testdata/series_*.json) via a testdata/fetch.go downloader (#1650)' assuming they were Go test fixtures.
- Investigation shows they are actually the DUMP_FILE/FIX_FILE constants in scripts/series_dedup.py (a maintenance script that fetches series data from a running server and writes analysis output locally) — i.e. generated artifacts of running the script, not inputs any test needs.
- No .go file anywhere in the repo references either filename.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n series_dump scripts/series_dedup.py   # 1 hit ~L40, DUMP_FILE = ... — series_dump.json/series_fix.json are script cache output, not go test fixtures
  grep -rln series_dump --include=*.go .   # 0 hits — no Go test consumes them
  git ls-files -s testdata/series_dump.json testdata/series_fix.json   # 2 hits — files are currently tracked and large
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In internal/config/config.go:649, rename the field to `AutoWriteTagsOnFetch bool` and its json tag to `json:"auto_write_tags_on_fetch"`.
2. In internal/config/config.go:1213, change `viper.SetDefault("write_back_metadata", false)` to `viper.SetDefault("auto_write_tags_on_fetch", false)`.
3. In internal/config/config.go around L1660, replace `WriteBackMetadata: viper.GetBool("write_back_metadata")` with a call to a new small helper, e.g. `AutoWriteTagsOnFetch: boolWithDeprecatedAlias("auto_write_tags_on_fetch", "write_back_metadata", false)`, defined near the load site as: if `viper.IsSet(newKey)` return `viper.GetBool(newKey)`; else if `viper.IsSet(oldKey)` log WARN once and return `viper.GetBool(oldKey)`; else return the default. A struct-literal field position cannot hold an if/else, so this MUST be a function call, not inline logic.
4. In internal/config/persistence.go:1084-1086, KEEP the case LABEL `case "write_back_metadata":` (the persisted key string does not change) but change the assignment target to the RENAMED field: `case "write_back_metadata": if b, err := strconv.ParseBool(value); err == nil { c.AutoWriteTagsOnFetch = b }`. ADD a new `case "auto_write_tags_on_fetch": if b, err := strconv.ParseBool(value); err == nil { c.AutoWriteTagsOnFetch = b } }` for newly-saved rows.
5. In internal/metafetch/service_fetch.go:309, change `config.AppConfig.WriteBackMetadata` to `config.AppConfig.AutoWriteTagsOnFetch`.
6. In internal/config/config_unit_test.go:654, rename the test row's key string to `auto_write_tags_on_fetch` and its accessor to `AppConfig.AutoWriteTagsOnFetch`.
7. Add a new unit test proving the alias: set only `write_back_metadata=true` via viper (simulating prod's persisted snapshot), load config, assert `AppConfig.AutoWriteTagsOnFetch == true` and that the WARN log fired once.
8. Grep once more for stragglers before finishing: `grep -rn 'WriteBackMetadata\|write_back_metadata' --include='*.go' .` -- every remaining `c.WriteBackMetadata`/`WriteBackMetadata:` hit outside a string literal or comment is a compile error waiting to happen.

Then, always:
- Keep the change purely removal — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_ci-tooling_014.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If any CI job or doc references these files as committed fixtures, must update it — checked `grep -rn series_dump.json docs/` and only the two repo-size planning docs mention them, which are historical analysis, not a live dependency.

## Tests

- No automated test exists for series_dedup.py (it's an operator script, not covered by `make test`) — verify manually with `python3 scripts/series_dedup.py --help` still runs without error after the gitignore change.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `git ls-files testdata/series_dump.json testdata/series_fix.json` returns no output.
- [ ] `grep -n series_dump.json .gitignore` returns 1 hit.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_ci-tooling_014.md`.

## Commit message

```
fix(ci-tooling): Stop committing series_dedup.py's generated dump/fix cache f (REPO-SIZE-1)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the removed symbol/file is already ABSENT at HEAD (re-run the re-verify greps above: zero hits = already removed) AND the replacement is present, the removal is already done — run acceptance instead. Rollback = `git revert` the commit to restore the file + its call sites; no data or schema is touched.

## Coordinator notes

This deviates from the plan doc's literal 'downloader' suggestion because investigation found the premise (these are fixtures) was wrong — flagging per Fix-It-Right depth rule rather than building an unneeded testdata/fetch.go.
