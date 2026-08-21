<!-- file: docs/agent-tasks/todo-completion/metadata/TASK-081-prefix-metadata-apply-activity-summaries-with-th.md -->
<!-- version: 1.0.0 -->
<!-- guid: d69f786f-33b0-4265-b789-9837b0c486b1 -->
<!-- last-edited: 2026-08-21 -->

# TASK-081 — Prefix metadata-apply activity summaries with the book title and render empty old-value as '(none)' (TODO.md L3517)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · metadata subagent · **Why:** single-file, single-function, mechanical string-formatting change with no new types · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 3517 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Metadata-apply activity rows don't NAME the book" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-05.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-081-prefix-metadata-apply-activity-summaries-with-th" -b agent/metadata-081-prefix-metadata-apply-activity-summaries-with-th origin/main
cd "$REPO/.worktrees/metadata-081-prefix-metadata-apply-activity-summaries-with-th"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Change the Summary line built in RecordChangeHistory (internal/metafetch/service_apply.go:236-244) so it leads with the book title and never renders an empty from-value as a dangling arrow: 'The Whispering Night: Applied narrator: Alex Kozlowski → Grant Cartwright' and 'The Whispering Night: Applied audiobook_release_year: (none) → 2021'.

## Background (verify before editing)

- internal/metafetch/service_apply.go:236-244 is the only construction site of this ActivityEntry's Summary field
- book.Title is directly available in RecordChangeHistory's scope (it's the *database.Book parameter)
- truncateActivity(s, maxLen) at internal/metafetch/service.go:674 is a plain length-capper with no empty-string special case

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'Summary: fmt.Sprintf("Applied' internal/metafetch/service_apply.go   # 1 hit, L242 — Summary is built without the book title
  grep -n 'func (mfs \*Service) RecordChangeHistory' internal/metafetch/service_apply.go   # 1 hit, L165, signature takes `book *database.Book` — book *database.Book (with .Title) is already in scope as RecordChangeHistory's parameter
  grep -n -A5 'func truncateActivity' internal/metafetch/service.go   # 1 hit; body is `if len(s) <= maxLen { return s }`, so "" passes through as "" — truncateActivity returns the empty string unchanged for oldVal=""
  grep -n 'BookID:  book.ID' internal/metafetch/service_apply.go   # 1 hit, L241 — BookID is already set on the ActivityEntry so the frontend link target already exists; only the Summary text is missing the title
  ```

### Reuse — don't invent

- Use `truncateActivity` in `internal/metafetch/service.go` (verify: `grep -n 'func truncateActivity' internal/metafetch/service.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add `func displayOrNone(s string) string { if s == "" { return "(none)" }; return s }` directly in internal/metafetch/service_apply.go, next to RecordChangeHistory (do NOT add it to service.go — keep the change confined to the one file already in scope).
2. In service_apply.go's RecordChangeHistory, change the Summary line (currently L242) to: `title := book.Title; if title == "" { title = book.ID }; Summary: fmt.Sprintf("%s: Applied %s: %s → %s", title, c.field, displayOrNone(truncateActivity(c.oldVal, 50)), truncateActivity(c.newVal, 50))`.
3. Bump the version header on internal/metafetch/service_apply.go only.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_metadata_081.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- book.Title == "" — fall back to book.ID rather than emitting a leading ': Applied ...'
- both oldVal and newVal empty (should not occur given the `if c.newVal == "" ... continue` guard at L217, but keep displayOrNone symmetric for safety)

## Tests

- internal/metafetch/service_apply_test.go: new test RecordChangeHistory_SummaryLeadsWithBookTitle asserting the recorded ActivityEntry.Summary starts with the book's Title followed by ': Applied'
- internal/metafetch/service_apply_test.go: new test RecordChangeHistory_EmptyOldValueRendersNone asserting a change whose oldVal is "" (e.g. audiobook_release_year with no prior value) produces a Summary containing '(none) → ' rather than a bare arrow

Anti-over-suppression test: `RecordChangeHistory_SummaryLeadsWithBookTitle (proves the happy-path title-prefixed line still renders correctly, not just the (none) edge case)` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/metafetch/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/metafetch/... -run RecordChangeHistory` passes
- [ ] grep -n 'Summary: fmt.Sprintf("%s: Applied' internal/metafetch/service_apply.go returns 1 hit after the change
- [ ] Anti-over-suppression test: `RecordChangeHistory_SummaryLeadsWithBookTitle (proves the happy-path title-prefixed line still renders correctly, not just the (none) edge case)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/metafetch/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_metadata_081.md`.

## Commit message

```
fix(metadata): Prefix metadata-apply activity summaries with the book title (TODO L3517)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Screenshot-driven bug (2026-08-14 18:03); the field also appears in Details already, so this is purely a display-string fix, no schema change.
