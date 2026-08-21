<!-- file: docs/agent-tasks/todo-completion/audiobooks/TASK-005-add-a-conformance-test-asserting-the-library-pat.md -->
<!-- version: 1.0.0 -->
<!-- guid: ecd41168-5364-4efd-87a2-3d9b6e6bfbb3 -->
<!-- last-edited: 2026-08-21 -->

# TASK-005 — Add a conformance test asserting the library path and author path classify nil/true/false IsPrimaryVersion identically (TODO.md L3889)

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · audiobooks subagent · **Why:** Requires understanding both call paths (library pushdown vs authorID branch + post-filter) well enough to build a fixture that actually exercises the divergent nil handling -- a naive fixture without a nil-flagged row would not catch the bug per the TODO's own warning. · **Depends on:** TASK-004 · **Wave:** 4 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 3889 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "Add a conformance test in the shape used by #2406/" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-06.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/audiobooks-005-add-a-conformance-test-asserting-the-library-pat" -b agent/audiobooks-005-add-a-conformance-test-asserting-the-library-pat origin/main
cd "$REPO/.worktrees/audiobooks-005-add-a-conformance-test-asserting-the-library-pat"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a new test file that seeds one author with three books -- nil IsPrimaryVersion, explicit true, explicit false -- and asserts svc.GetAudiobooksWithTotal(...) returns the identical primary/non-primary classification whether called via the library path (authorID=nil, is_primary_version filter set) or the author path (authorID=that author, same filter).

## Background (verify before editing)

- TODO.md:3889-3892 explicitly requires 'one fixture containing a nil-flag book, an explicit-true book and an explicit-false book' and warns 'a fixture without a nil-flag row cannot catch this.'
- This test should be written to FAIL against pre-L3884 code (nil-as-false bug at service_query.go:346) and PASS after L3884's one-line fix -- write it before or alongside L3884 as the regression proof.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "^func Test" internal/database/book_visibility_conformance_test.go   # >=1 hit e.g. TestGetAllBooksCore_MemDBAndPebbleAgree — Existing conformance-test naming/structure precedent to follow
  grep -n "func (svc \*AudiobookService) GetAudiobooksWithTotal" internal/audiobooks/service_query.go   # 1 hit L41 — GetAudiobooksWithTotal is the single entry point exercising both the library (authorID=nil) and author (authorID!=nil) branches
  ```

### Reuse — don't invent

- Use `GetAudiobooksWithTotal` in `internal/audiobooks/service_query.go` (verify: `grep -n "func (svc \*AudiobookService) GetAudiobooksWithTotal" internal/audiobooks/service_query.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add `OnlyParsedTranscription bool` to `ListFilters` in internal/audiobooks/service_types.go:78-96 (near FingerprintStatus).
2. In internal/server/handlers/audiobooks/handler.go, parse `only_parsed_transcription` via `httputil.ParseQueryBoolPtr` or the plain-bool helper used for `show_quarantined` at L595 (note: ListFilters.OnlyParsedTranscription is a plain bool, so parse as `c.Query("only_parsed_transcription") == "true"`, matching the show_quarantined pattern, not the *bool pattern used for IsPrimaryVersion) into the `filters := audiobookspkg.ListFilters{...}` literal at ~L513-523.
3. Add `"only_parsed_transcription": {}` to `bareParamAllowList` (L256-259) with a one-line comment explaining why.
4. In internal/audiobooks/service_filtering.go buildBookSummaryFilterWithLookupCount (~L994-1039): change line 994's `hasFPFilters := f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil` to also include `|| f.OnlyParsedTranscription`, THEN add the predicate: if `f.OnlyParsedTranscription && (b.TranscribedTitle == nil || strings.TrimSpace(*b.TranscribedTitle) == "")`, exclude the book -- copy the exact predicate from metadata_ops.go:524-527 verbatim.
5. In internal/audiobooks/service_query.go: change line 71's `hasFingerprintingFilters := f.FingerprintStatus != "" || f.CoveragePercentMin != nil || f.CoveragePercentMax != nil` to also include `|| f.OnlyParsedTranscription`, THEN add the same predicate in the fingerprinting-filters application block (~L400-415) as the sibling of the FingerprintStatus check.
6. Bump file-header versions on all 4 touched files.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_audiobooks_005.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Book present via BOTH the legacy Book.AuthorID field and a BookAuthor junction row for the same author -- must not be double-counted (mirrors the existing dedup logic in getBooksByAuthorID).

## Tests

- internal/audiobooks/service_query_isprimary_conformance_test.go TestIsPrimaryVersion_LibraryAndAuthorPathsAgree -- the test itself IS the deliverable for this item.

Anti-over-suppression test: `TestIsPrimaryVersion_LibraryAndAuthorPathsAgree itself -- it is the anti-regression check for L3884's fix.` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/audiobooks/... -run TestIsPrimaryVersion_LibraryAndAuthorPathsAgree -v exits 0 AFTER L3884's fix is applied.
- [ ] Temporarily reverting L3884's one-line change and re-running the test must FAIL (confirms the test actually exercises the bug).
- [ ] Anti-over-suppression test: `TestIsPrimaryVersion_LibraryAndAuthorPathsAgree itself -- it is the anti-regression check for L3884's fix.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_audiobooks_005.md`.

## Commit message

```
feat(audiobooks): Add a conformance test asserting the library path and author (TODO L3889)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/audiobooks/... -run TestIsPrimaryVersion_LibraryAndAuthorPathsAgree -v exits 0 AFTER L3884's fix is applied.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Write this alongside or just before L3884 so it can be run against the buggy code first to confirm it actually fails (mutation-test discipline).
