<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-050-shattered-book-reassembly-match-fragment-file-se.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c1681ae-f7c7-4917-9e75-2692aac6e20d -->
<!-- last-edited: 2026-08-21 -->

# TASK-050 — Shattered-book reassembly: match fragment file-sets against the reference corpus via fpidx containment (TODO.md L10750)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · dedup subagent · **Why:** new matching algorithm (set containment over an LSH index) feeding an auto-regroup decision on prod data; must compose correctly with the existing metadata-based detector rather than replace it · **Depends on:** TASK-021, TASK-049 · **Wave:** 8 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10750 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Fingerprint-confirmed dedup + shattered-book rea" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-050-shattered-book-reassembly-match-fragment-file-se" -b agent/dedup-050-shattered-book-reassembly-match-fragment-file-se origin/main
cd "$REPO/.worktrees/dedup-050-shattered-book-reassembly-match-fragment-file-se"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Extend `DetectSplitBookCandidates` (or add a parallel confirmation step consumed by it) so that, for a candidate group of fragments (author-first shards of a multi-author anthology), the set of each fragment's AcoustID fingerprint is looked up in `fpidx` against the reference-corpus index (built in part 1) to find a source-corpus folder whose file-set CONTAINS the fragment set (`fragments ⊆ source_folder`); metadata (album/iTunes-XML/PID/version-group, i.e. the existing detector's signal) remains the PRIMARY regroup key, and this fingerprint-set match is layered on as a safety CONFIRMATION that makes an auto-regroup safe, per the owner's explicit design constraint.

## Background (verify before editing)

- Scope text: 'match the fragments' per-file fingerprint set against the assembled ORIGINAL source folder (set containment fragments ⊆ source_folder) via the existing fpidx LSH index → the source folder whose file-set contains them identifies the true whole book... Metadata... is the primary regroup key; the fingerprint-set match is the safety confirmation that makes the auto-regroup safe.'
- This depends on part 1 (todo_line 10750 part 1) having already scanned+indexed the reference corpus into fpidx — without it there is no source-folder ground truth to test containment against.
- internal/dedup/split_book_detector.go's `SplitBookCandidate` type (referenced but not dumped here — check its fields via `grep -n 'type SplitBookCandidate struct' -A20 internal/dedup/split_book_detector.go`) is the natural place to add an optional confirmation field (e.g. `FingerprintConfirmed bool` + `MatchedSourceFolder string`).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'AcoustID\|Fingerprint\|fpidx' internal/dedup/split_book_detector.go   # 0 hits — existing split-book detection is metadata/title-shape only, no fingerprint matching
  grep -n 'func.*LSHProbe\|func.*Subprints' internal/database/pebble_store_lsh.go internal/fingerprint/lsh.go   # 2 hits: internal/database/pebble_store_lsh.go:123 (LSHProbe), internal/fingerprint/lsh.go:64 (Subprints) — fpidx LSH probe API exists to build the containment match on (the real symbol is LSHProbe, not Lookup/Query)
  ```

### Reuse — don't invent

- Use `existing SplitBookCandidate type + detection entrypoint to extend rather than replace` in `internal/dedup/split_book_detector.go` (verify: `grep -n 'type SplitBookCandidate' internal/dedup/split_book_detector.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read `type SplitBookCandidate struct` in full (internal/dedup/split_book_detector.go) to find its existing fields before adding new ones — do not guess field names.
2. Write a new function (e.g. `confirmAgainstReferenceCorpus(ctx, candidate SplitBookCandidate) (matchedFolder string, confirmed bool, err error)`) that: for each fragment in the candidate, looks up its AcoustID fingerprint in fpidx restricted to `source=refcorpus`-tagged entries (from part 1), collects the set of reference-corpus folder keys each fragment's fingerprint appears under, and checks whether any single folder's fingerprint set is a SUPERSET of the fragment set.
3. Wire this as a post-filter/annotation step after `DetectSplitBookCandidates` returns its metadata-based candidates — do NOT make containment confirmation a precondition for a candidate to be returned/reported (metadata stays primary per the design constraint); instead set the new confirmation field on candidates where a containing source folder was found.
4. Wherever auto-regroup/apply logic consumes `SplitBookCandidate` (find via `grep -rn SplitBookCandidate internal/dedup/*.go internal/plugins/dedup/*.go`), gate any AUTO-apply path (not the report/review path) on `FingerprintConfirmed == true`, leaving unconfirmed candidates for manual review only — this is the actual safety mechanism the owner's design constraint calls for.
5. Bound the per-candidate fpidx lookup loop with a worker pool if candidate counts are whole-library-scale (mandatory concurrency rule) — check the actual call site's expected candidate volume before deciding a pool is needed vs. candidates being few enough to run serially (a handful of shattered-book groups is plausible; verify before over-engineering).
6. Bump file-header versions.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_050.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A reference-corpus folder that itself contains MORE files than the fragment set (superset, not exact match) — this is expected and should still count as containment (fragments ⊆ source_folder, not fragments == source_folder), per the scope text's own notation.
- Two different reference-corpus folders both contain the fragment set (ambiguous match) — must not silently pick one; either report both as candidates or mark ambiguous/unconfirmed rather than guessing.

## Tests

- internal/dedup/split_book_detector_test.go — add a case: 3 fragments whose fingerprints are all present in a fake reference-corpus folder's fpidx entries → FingerprintConfirmed=true, MatchedSourceFolder set correctly.
- A case where fragments' fingerprints are a SUPERSET of any single reference folder (i.e. no single folder contains them all) → FingerprintConfirmed=false, candidate still returned (metadata-only) for manual review.
- A case with one fragment lacking a fingerprint entirely → must not crash and must not falsely confirm (absent evidence ≠ refuted, but also ≠ confirmed — the containment check should treat a missing fingerprint as 'cannot confirm containment for this fragment', not skip it silently).

Anti-over-suppression test: `test: 'a fragment set that is NOT contained in any reference folder correctly stays unconfirmed and does not block the metadata-only candidate from being reported for manual review'` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/dedup/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go test ./internal/dedup/... -run TestSplitBookDetector` (or actual test name) passes including new fingerprint-confirmation cases.
- [ ] Any auto-apply split-book merge path is verifiably gated on FingerprintConfirmed (grep the apply function for the new field).
- [ ] make ci passes.
- [ ] Anti-over-suppression test: `test: 'a fragment set that is NOT contained in any reference folder correctly stays unconfirmed and does not block the metadata-only candidate from being reported for manual review'` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/dedup/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_050.md`.

## Commit message

```
feat(dedup): Shattered-book reassembly: match fragment file-sets against  (TODO L10750)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (``go test ./internal/dedup/... -run TestSplitBookDetector` (or actual test name) passes including new fingerprint-confirmation cases.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

review_critical=true: feeds an auto-regroup/merge decision on prod data. Hard dependency on part 1 (the reference-corpus scan) landing first — there is no fpidx data to match against otherwise. NEVER mutate the reference-corpus or active iTunes tree from this code path; it only reads fpidx and writes regroup decisions to the organized-library side.
