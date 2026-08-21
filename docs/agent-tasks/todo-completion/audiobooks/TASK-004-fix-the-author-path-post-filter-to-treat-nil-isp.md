<!-- file: docs/agent-tasks/todo-completion/audiobooks/TASK-004-fix-the-author-path-post-filter-to-treat-nil-isp.md -->
<!-- version: 1.0.0 -->
<!-- guid: f2b7ee57-a13f-449d-84cb-d45d5b44b82a -->
<!-- last-edited: 2026-08-21 -->

# TASK-004 — Fix the author-path post-filter to treat nil IsPrimaryVersion as primary, matching storage's default (TODO.md L3884)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · audiobooks subagent · **Why:** One-line fix but on a prod-data-shaped read path with subtle nil semantics -- worth a careful reviewer, not pure mechanical. · **Depends on:** none · **Wave:** 3 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 3884 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Decide the single meaning of a nil `IsPrimaryVersi" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-06.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/audiobooks-004-fix-the-author-path-post-filter-to-treat-nil-isp" -b agent/audiobooks-004-fix-the-author-path-post-filter-to-treat-nil-isp origin/main
cd "$REPO/.worktrees/audiobooks-004-fix-the-author-path-post-filter-to-treat-nil-isp"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Change internal/audiobooks/service_query.go line 346 so a nil IsPrimaryVersion is treated as primary (true), matching the storage-layer convention already used by pebble_store.go:953 and the memdb index default, so the author path and library path classify the same book identically.

## Background (verify before editing)

- GetBooksByAuthorIDCore (both the memdb and Pebble-scan implementations) already excludes explicitly-false rows before returning, per its own doc comment: 'Non-primary versions are duplicates of a book already in the list, so it excludes them' (internal/database/memdb_reads.go:510-511). So an explicitly-false book like 01KNDB8NWHXV2DKRQESBA9SDRA (author 42623) never reaches this post-filter at all under ANY is_primary_version query value -- confirmed by the TODO's own measurement (author_id=42623&is_primary_version=false -> 0 rows).
- A nil-flagged book DOES survive into `books` (nil counts as primary at the getter level) and is then handed to the buggy post-filter, which misclassifies it as non-primary when is_primary_version=false is requested -- explaining the TODO's measured author_id=38542&is_primary_version=false -> 1 row, is_primary_version: null.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "bPrimary := b.IsPrimaryVersion" internal/audiobooks/service_query.go   # 1 hit L346 — The author-path post-filter treats nil as false
  grep -n "eff := book.IsPrimaryVersion == nil" internal/database/pebble_store.go   # 1 hit L953 — Storage's PebbleStore filter treats nil as true (primary)
  grep -n "Default: true" internal/database/memdb_schema.go   # 1 hit L165 — memdb IsPrimaryVersion index defaults nil to true
  grep -n "booksCore, err = svc.store.GetBooksByAuthorIDCore" internal/audiobooks/service_query.go   # 1 hit L156 — authorID branch calls GetBooksByAuthorIDCore, which already excludes explicit-false rows before the post-filter runs
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Create internal/audiobooks/service_query_isprimary_conformance_test.go using the same test-store setup helper other service_query_*_test.go files in this package use (check service_query_heavyfilter_sort_test.go for the setup pattern).
2. Seed one Author and three Books all linked to it: bookNil (IsPrimaryVersion left nil), bookTrue (IsPrimaryVersion=boolPtr(true)), bookFalse (IsPrimaryVersion=boolPtr(false)).
3. Call svc.GetAudiobooksWithTotal(ctx, 50, 0, "", nil, nil, ListFilters{IsPrimaryVersion: boolPtr(false)}) (library path, no authorID) and record which of the 3 books come back.
4. Call svc.GetAudiobooksWithTotal(ctx, 50, 0, "", &authorID, nil, ListFilters{IsPrimaryVersion: boolPtr(false)}) (author path) and record which of the 3 books come back.
5. Assert the two result sets are identical for is_primary_version=false, is_primary_version=true, and no filter.
6. Name the test TestIsPrimaryVersion_LibraryAndAuthorPathsAgree.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_audiobooks_004.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book with IsPrimaryVersion explicitly false never reaches this filter (excluded earlier by GetBooksByAuthorIDCore) -- this fix does not change that behavior, and should not attempt to: L3893 (needs_design) is where 'should the author listing expose non-primary books at all' gets decided.

## Tests

- See L3889 in this scope -- the conformance test added there (fixture with nil/true/false rows) is the correctness proof for this fix; do not duplicate a narrower test here.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] grep -n 'bPrimary := b.IsPrimaryVersion == nil' internal/audiobooks/service_query.go returns 1 hit.
- [ ] The L3889 conformance test (once added) passes: go test ./internal/audiobooks/... -run IsPrimaryVersion -v exits 0.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_audiobooks_004.md`.

## Commit message

```
fix(audiobooks): Fix the author-path post-filter to treat nil IsPrimaryVersio (TODO L3884)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

This is a narrow, surgical fix per the owner's own guidance in the TODO text ('Default: true is already the storage answer, so the post-filter is the side that should change'). Do not also change GetBooksByAuthorIDCore's exclusion behavior in the same task -- that's the separate, undecided question in L3893.
