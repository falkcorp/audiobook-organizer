<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-082-build-a-report-only-census-of-books-with-a-place.md -->
<!-- version: 1.0.0 -->
<!-- guid: f9754b10-dd9c-4531-852d-6642ee2da958 -->
<!-- last-edited: 2026-08-21 -->

# TASK-082 — Build a report-only census of books with a placeholder author already baked into their organizer-tree path but resolvable metadata (TODO.md L4144)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · maintenance subagent · **Why:** a new whole-library maintenance op with a worker pool, following an existing pattern but requiring correct path-parsing logic (detecting the placeholder baked into FilePath) plus a resolvable-metadata heuristic · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4144 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Organize renames with placeholder metadata — \"Un" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-07.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-082-build-a-report-only-census-of-books-with-a-place" -b agent/maintenance-082-build-a-report-only-census-of-books-with-a-place origin/main
cd "$REPO/.worktrees/maintenance-082-build-a-report-only-census-of-books-with-a-place"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a new report-only maintenance op maintenance.unknown-author-audit that walks every book under the organizer root, flags ones whose current FilePath contains the placeholder author segment ('Unknown Author'), and further classifies each as 'resolvable' (title + narrators/tags present, i.e. would pass HasResolvedAuthor after a metadata fetch — approximate this by checking whether Author is empty/placeholder but Title, Narrator, or external IDs are populated) vs 'genuinely unknown', reporting counts of each population. No mutation — this is audit-only per owner decision 11's counter pattern.

## Background (verify before editing)

- The rename-time gate (HasResolvedAuthor, part 1 of this item) prevents NEW placeholder-baked paths going forward, but does nothing for books already organized under Unknown Author/ before 2026-08-18 — those are the ones this audit must count.
- placeholderAuthor = "Unknown Author" (organizer.go:375) is the exact string to match against FilePath segments.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "const placeholderAuthor" internal/organizer/organizer.go   # 1 hit at L375, value \"Unknown Author\" — the placeholder author constant already exists and is reusable
  grep -n "func (o \*Organizer) HasResolvedAuthor" internal/organizer/organizer.go   # 1 hit at L402 — the resolved-author predicate to reuse for 'carries resolvable metadata' already exists
  grep -n "ID:.*maintenance.dedup-exact-triage\|Apply bool" internal/plugins/maintenance/dedup_triage.go   # hits at L301 (op ID) and L288 (Apply field with 'report-only contract' comment) — an existing report-only op def pattern to copy (apply=false default, counts only)
  ```

### Reuse — don't invent

- Use `Organizer.HasResolvedAuthor` in `internal/organizer/organizer.go` (verify: `grep -n "func (o \*Organizer) HasResolvedAuthor" internal/organizer/organizer.go`) — do NOT write a parallel helper.
- Use `sdk.OperationDef report-only pattern (Apply bool, default false)` in `internal/plugins/maintenance/dedup_triage.go` (verify: `grep -n "Apply bool" internal/plugins/maintenance/dedup_triage.go`) — do NOT write a parallel helper.
- Use `registry.RunItems bounded worker pool for whole-library loops` in `internal/operations/registry/run_items.go` (verify: `grep -n "func RunItems" internal/operations/registry/run_items.go`) — do NOT write a parallel helper.

## Step-by-step

1. Create internal/plugins/maintenance/unknown_author_audit.go modeled on dedup_triage.go's structure: a params struct (no Apply field needed since this has no apply path at all — report-only, full stop), an sdk.OperationDef with ID 'maintenance.unknown-author-audit', DisplayName, Description stating it is report-only, Capabilities: []sdk.Capability{sdk.CapLibraryRead}, and a Run function.
2. In the Run function, fetch all books via a single GetAllBooksCore(0,0) snapshot call (per this repo's established single-page pattern — see L4107), filter to books whose FilePath contains the RootDir-relative segment '/Unknown Author/' (use filepath separators correctly, case-sensitive match on the literal placeholder string), and for each such book classify: resolvable = Title != "" && (has ISBN/ASIN external ID, or non-empty Narrator field, or NormalizedTitle differs from a generic default) vs unresolvable otherwise.
3. Shard the classification loop across a bounded worker pool via registry.RunItems (per CLAUDE.md's mandatory-concurrency rule for whole-library loops) since classification may involve per-book store reads (external IDs).
4. Report via sdk.Reporter: total books scanned, count with Unknown Author baked into path, count of those classified resolvable, count unresolvable, and (bounded, e.g. first 100) a sample list of book IDs+titles in the resolvable bucket for operator review.
5. Register the op in internal/plugins/maintenance/plugin.go's defs slice (append p.unknownAuthorAuditDef() alongside the other RegisterOp calls near line 33-147).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_082.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book with FilePath == "" (never organized) must not false-positive as 'baked Unknown Author' — only match when the placeholder literally appears as a path segment.
- A book already fixed by the L4144-part1 gate (soft-deleted or restored) should be excluded consistent with the standard bookIsSoftDeleted rule.

## Tests

- internal/plugins/maintenance/unknown_author_audit_test.go: TestUnknownAuthorAudit_CountsResolvableAndUnresolvable — seed a fake store with 3 books: one under /Unknown Author/ with a populated ISBN (resolvable), one under /Unknown Author/ with nothing else populated (unresolvable), one NOT under /Unknown Author/ (excluded); assert the report counts are 1/1/1 respectively.
- TestUnknownAuthorAudit_NeverMutates — assert no CreateBook/UpdateBook/DeleteBook call is made on the mock store during the run (report-only contract).

Anti-over-suppression test: `TestUnknownAuthorAudit_CountsResolvableAndUnresolvable (the resolvable bucket must not be empty when a qualifying book exists — guards against an over-broad 'everything is unresolvable' classifier)` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run TestUnknownAuthorAudit passes.
- [ ] POST /api/v1/operations/v2 {"def_id":"maintenance.unknown-author-audit"} against a running dev server returns a report with non-negative counts and does not change any book row (verify via a before/after GetBooksCore diff).
- [ ] Anti-over-suppression test: `TestUnknownAuthorAudit_CountsResolvableAndUnresolvable (the resolvable bucket must not be empty when a qualifying book exists — guards against an over-broad 'everything is unresolvable' classifier)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_082.md`.

## Commit message

```
feat(maintenance): Build a report-only census of books with a placeholder autho (TODO L4144)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/plugins/maintenance/... -run TestUnknownAuthorAudit passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is a NEW report op, not covered verbatim by any owner decision in the scope file, but follows the same 'report ops actionable, mutating ops parked' principle as owner decision 9 (PH-2b) and decision 11 (generateTargetPath counter) — flagging for coordinator confirmation since it is inferred by analogy, not an explicit decision line.
