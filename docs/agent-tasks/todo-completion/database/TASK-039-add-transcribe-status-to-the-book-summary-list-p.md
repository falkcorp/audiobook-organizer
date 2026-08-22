<!-- file: docs/agent-tasks/todo-completion/database/TASK-039-add-transcribe-status-to-the-book-summary-list-p.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7b9cb1fa-bcfd-4420-9472-2264dce6ed92 -->
<!-- last-edited: 2026-08-21 -->

# TASK-039 — Add transcribe_status to the book-summary list projection and a frontend quality filter control (TODO.md L10728)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · database subagent · **Why:** touches the database summary projection (2 construction sites) plus a new frontend filter control; must not break the memdb round-trip contract · **Depends on:** TASK-005 · **Wave:** 4

Source: `TODO.md` line 10728 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Transcription quality filter**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-039-add-transcribe-status-to-the-book-summary-list-p" -b agent/database-039-add-transcribe-status-to-the-book-summary-list-p origin/main
cd "$REPO/.worktrees/database-039-add-transcribe-status-to-the-book-summary-list-p"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add `TranscribeStatus *string` to `database.BookSummary` (store.go ~L405, next to TranscribedTitle), populate it at both summary-construction sites (pebble_store.go's `bookToSummary` and memdb_summaries.go's list builder), and add a simple frontend filter control (e.g. a 3-state chip: not-transcribed / unparsed / parsed) to the Library page using the part-1 `only_parsed_transcription` query param plus this new status field for display.

## Background (verify before editing)

- internal/database/store.go:344-350 documents TranscribeStatus's existing values: it 'records the outcome of the most recent transcription ATTEMPT... lets an operator filter books by status to see exactly why transcription is/isn't producing data.'
- internal/plugins/maintenance/intro_transcribe.go:759,818 already writes a `statusUnparsed` value distinguishing 'attempted but no usable title' from other outcomes — this is the exact status value a frontend quality badge would key off.
- web/src/hooks/useColumnConfig.ts and web/src/pages/Library.tsx (the only current useColumnConfig consumer) are the natural home for a new filter control, following the project's existing column-config UI pattern.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'TranscribedTitle\|TranscribeStatus' internal/database/store.go   # TranscribedTitle inside BookSummary (~L405); TranscribeStatus only on the full Book struct (~L350), not BookSummary — BookSummary has TranscribedTitle but not TranscribeStatus
  grep -n 'func bookToSummary' internal/database/pebble_store.go; grep -n 'BookSummary{' internal/database/memdb_summaries.go   # 1 hit ~L1043 and 1 hit ~L220 — two BookSummary construction sites both need updating
  ```

### Reuse — don't invent

- Use `existing TranscribedTitle plumbing as the copy-paste template` in `internal/database/pebble_store.go` (verify: `grep -n 'TranscribedTitle:' internal/database/pebble_store.go internal/database/memdb_summaries.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add `TranscribeStatus *string `json:"transcribe_status,omitempty"`` to `BookSummary` in internal/database/store.go, next to the existing `TranscribedTitle` field (~L405).
2. In internal/database/pebble_store.go's `bookToSummary(b *Book) BookSummary` (~L1043-1064), add `TranscribeStatus: b.TranscribeStatus,` next to the existing `TranscribedTitle: b.TranscribedTitle,` line (~L1064).
3. In internal/database/memdb_summaries.go's summary-builder loop (~L220-240), add the same field assignment.
4. Confirm `strippedMemdbFields` (internal/audiobooks/service_types.go:132-137) does NOT include transcribe_status (it currently only lists description/version_notes/book_sig_v1) — no change needed there, just verify.
5. In web/src/services/api.ts (or wherever the Book/BookSummary TypeScript type mirrors the Go struct), add `transcribe_status?: string` to the type.
6. In web/src/pages/Library.tsx, add a filter chip/dropdown wired to the part-1 `only_parsed_transcription` query param, and optionally render a small status badge per row using the new `transcribe_status` field (reusing an existing Chip-based badge pattern from web/src/components/dedup/dedupHelpers.tsx if one fits).
7. Bump file-header versions on all touched files.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_039.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book that has never been through intro_transcribe.go has TranscribeStatus == nil — the frontend badge must render a distinct 'not attempted' state, not confuse it with 'unparsed'.

## Tests

- internal/database/pebble_store_test.go or an existing BookSummary conformance test — assert TranscribeStatus round-trips through bookToSummary.
- internal/database/memdb_strip_test.go — extend the existing pattern (it already tests TranscribedTitle survives memdb strip, L123-152) with a TranscribeStatus case.
- web/src/pages/__tests__/Library.test.tsx — assert the new filter control renders and toggling it appends `?only_parsed_transcription=true` to the API call.

Anti-over-suppression test: `test: 'TranscribeStatus=nil (never attempted) renders differently from TranscribeStatus=statusUnparsed (attempted, failed to parse)'` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... -count=1 && npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `grep -n TranscribeStatus internal/database/store.go` shows it on both Book and BookSummary.
- [ ] `npm --prefix web run lint && npm --prefix web test` pass.
- [ ] `make test` passes for internal/database.
- [ ] Anti-over-suppression test: `test: 'TranscribeStatus=nil (never attempted) renders differently from TranscribeStatus=statusUnparsed (attempted, failed to parse)'` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/database/... ./internal/database/mocks/... -count=1 && npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_039.md`.

## Commit message

```
feat(database): Add transcribe_status to the book-summary list projection an (TODO L10728)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — ``grep -n TranscribeStatus internal/database/store.go` shows it on both Book and BookSummary.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Depends on part 1 (todo_line 10728 part 1) landing first since the frontend filter control wires to the query param that part adds; can be built in parallel but should merge after.
