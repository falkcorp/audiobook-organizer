<!-- file: docs/agent-tasks/bug-techdebt/TASK-07-w5d1-verify-writeback.md -->
<!-- version: 1.0.0 -->
<!-- guid: f2fd4e88-bf6b-4ff8-9e74-0dd1e742db23 -->
<!-- last-edited: 2026-07-10 -->

# TASK-07 — Verify Author/Series survive the CreateOrganizedVersion original-book write-back (STOREFID W5d-1, TODO.md:62)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · verification-test subagent · **Why:** test-only, but the two-outcome protocol and real-PebbleStore fixture need care; a wrong fixture (MemStore vs Pebble, missing WaitForWarmup) invalidates the verification · **Depends on:** none

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY per item (worktree/PR/CI) EXCEPT REPO-SIZE-1 (#1650) which is STOP-FOR-HUMAN: a git-history rewrite is destructive and invalidates every clone/worktree — produce the migration plan (BFG/filter-repo vs LFS options, coordination checklist, backup strategy) as a TASK brief whose ONLY deliverable is the plan document, then STOP.
**File-ownership:** none — this task adds ONE new test file in `internal/organizer/` and touches NO product code. The product FIX (fail-open hydrate, TODO.md:75-83) is explicitly out of scope: it is a decision-carrying change deferred to its own future task.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/bug-techdebt-w5d1-verify-writeback" -b agent/bug-techdebt-w5d1-verify-writeback origin/main
cd "$REPO/.worktrees/bug-techdebt-w5d1-verify-writeback"
git rebase origin/main
```

## Goal

Add regression tests that PROVE, against a real `PebbleStore`, what happens to the
original book's denormalized `Author`/`Series` when `CreateOrganizedVersion`
(`internal/organizer/service.go`) writes the slim, page-derived original back via
`orgSvc.db.UpdateBook(book.ID, book)`. Test-only and additive: no product code
changes, whatever the outcome. Planning-time evidence says the wipe is REAL
(`PebbleStore.UpdateBook`'s STOR-1 guard preserves Description/VersionNotes/BookSig*
but NOT Author/Series), so expect Outcome B below.

## Background (verify before editing)

- TODO.md:62-84 documents the latent bug and the in-code comment above the write-back
  says "Deliberately NOT fixed here ... Tracked as a follow-up". Your job is the
  follow-up's VERIFICATION half only.
- The demotion invariant must hold no matter what: the original book ends up with
  `VersionGroupID` set, `IsPrimaryVersion=false`, `LibraryState="organized_source"`.
  A test proving that is required and expected to PASS today.
- Fixture facts (all verified at planning time):
  - `organizer.Store` is satisfied by `*database.PebbleStore` (compile-time proof
    `var _ Store = (*database.PebbleStore)(nil)` in service.go) — use the REAL store,
    not a mock, or you test nothing about STOR-1 semantics.
  - Tests MUST call `store.WaitForWarmup()` right after `database.NewPebbleStore(t.TempDir())`
    (documented race: memdb warmup drops write-throughs; see the doc comment on
    `WaitForWarmup` in pebble_store.go).
  - `CreateOrganizedVersion(org *Organizer, book *database.Book, newPath string, isDir bool, operationID string, log logger.Logger)`;
    the `org` param goes unused in the write-back path — existing tests build
    `&Organizer{config: &config.Config{}}`.
  - A `noopLogger` implementing `logger.Logger` already exists in the package's
    `unit_test.go` — REUSE it; do NOT declare a second one (duplicate-declaration
    breakage: parallel-test-helper-collision rule).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'CreateOrganizedVersion original-book slim-writeback' TODO.md
  # Expected: 1 hit (~:62) — the tracking item
  grep -n 'func (orgSvc \*Service) CreateOrganizedVersion' internal/organizer/service.go
  # Expected: 1 hit (~:817)
  grep -n 'Deliberately NOT' internal/organizer/service.go
  # Expected: 1 hit (~:931) — the comment above the write-back block under test. (The full
  # phrase "Deliberately NOT fixed here" wraps across a line break in the source, so grep
  # ONLY this prefix — a single-line grep for the full phrase returns 0 hits by artifact.)
  grep -n 'Tracked as a follow-up' internal/organizer/service.go
  # Expected: 1 hit (~:934) — end of the same comment block. Only if BOTH this grep and the
  # one above return 0 hits may a fix have landed: re-read CreateOrganizedVersion before
  # proceeding. (One grep hitting and the other missing = the comment was reworded, not fixed.)
  grep -n 'Preserve fields stripped by stripBookForMemdb (STOR-1)' internal/database/pebble_store.go
  # Expected: 1 hit (~:1419) — read the guarded field list; Author/Series absent at planning time
  grep -n 'noopLogger' internal/organizer/unit_test.go
  # Expected: hits (~:1936) — the logger to reuse
  grep -n 'Organizer{config: &config.Config{}}' internal/organizer/organizer_test.go
  # Expected: ≥1 hit — the Organizer construction to mirror
  grep -n 'func (p \*PebbleStore) WaitForWarmup' internal/database/pebble_store.go
  # Expected: 1 hit — mandatory in the fixture
  ```

## Step-by-step

1. Create `internal/organizer/organized_version_writeback_test.go` (package
   `organizer`, repo-standard 4-line Go header, fresh uuid4).
2. Fixture (shared by both tests): `store, err := database.NewPebbleStore(t.TempDir())`;
   `store.WaitForWarmup()`; `t.Cleanup` closes the store; `svc := NewService(store)`;
   `org := &Organizer{config: &config.Config{}}`. Seed ONE book via the store's create
   path with denormalized `Author` and `Series` populated (inspect the `database.Book`
   struct for the exact field names/types:
   `grep -n 'Author *\*Author\|Series *\*Series' internal/database/store.go`
   — Expected: exactly 2 hits, `Author *Author` ~:295 and `Series *Series` ~:297,
   both inside `type Book struct` which starts ~:120 of `internal/database/store.go`;
   mirror how `internal/database/pebble_book_preserve_test.go` seeds books — that file
   shows the CreateBook pattern to copy).
3. Build the SLIM projection the prod path sends: copy the seeded book by value, set
   its heavy/denormalized fields to nil (`Author`, `Series`, `Description` at
   minimum) — this simulates the GetAllBooksCore→ToBook page-derived input named in
   the in-code comment.
4. `TestCreateOrganizedVersion_OriginalDemotedToNonPrimary`: call
   `svc.CreateOrganizedVersion(org, &slim, filepath.Join(t.TempDir(), "organized.m4b"), false, "", <the reused noopLogger>)`;
   then `got, err := store.GetBookByID(<original ID>)` and assert: `VersionGroupID`
   non-nil/non-empty, `IsPrimaryVersion` non-nil and false, `LibraryState` non-nil and
   `"organized_source"`. Expected: PASSES today. (Nil-pointer semantics: fields are
   pointers — assert non-nil BEFORE dereferencing; a nil here is a failure, not a skip.)
5. `TestCreateOrganizedVersion_AuthorSeriesSurviveOriginalWriteback`: same flow;
   assert the re-fetched original still has non-nil `Author` (and `Series`) matching
   the seeded values. **Two-outcome protocol (locked, spec Decision 4):**
   - **Outcome A — it PASSES:** the TODO concern is disproven at the store layer.
     Keep the test as-is, check off TODO.md:62 (grep above) with
     `✅ verified 2026-07-10+ (TASK-07): no wipe`, and say so in the PR body.
   - **Outcome B — it FAILS (planning-time expectation):** the wipe is CONFIRMED. Do
     NOT fix product code. Convert the survival assertions into a guarded block that
     runs first and, on detecting the wipe, calls
     `t.Skipf("W5d-1 KNOWN BUG CONFIRMED: Author/Series wiped by slim write-back (STOR-1 guard lacks Author/Series) — unskip when the fail-open hydrate fix (TODO.md:75-83) lands")`.
     The test then documents the bug executably and flips to a real assertion the day
     the fix lands. Update the TODO.md:62 item with
     `⚠ wipe CONFIRMED by TASK-07 test (date); fix still open` — do NOT check it off.
     **HARD REQUIREMENT — the task is NOT done on Outcome B until a tracked follow-up
     exists:** file a GitHub issue for the fail-open hydrate fix
     (`gh issue create --repo falkcorp/audiobook-organizer` — title
     "W5d-1 CONFIRMED: CreateOrganizedVersion slim write-back wipes original book's
     Author/Series (fail-open hydrate fix needed)", severity-tagged as prod data-loss,
     body linking TODO.md:75-83, the skipped test name, and the STOR-1 guard evidence)
     and reference the issue number in BOTH the TODO.md annotation and the BLOCKED
     line. A `t.Skipf` + TODO note alone is NOT sufficient closure for a confirmed
     live prod data-loss path. Report the confirmation prominently (BLOCKED line: the
     fix task, with the issue number).
6. Nil/unknown semantics, spelled out twice (here and in acceptance): if `GetBookByID`
   errors or returns nil in either test, that is a test FAILURE (fixture bug), never a
   skip; the ONLY permitted skip is the Outcome-B wipe-confirmed skip in step 5.
7. Bump headers; prepend CHANGELOG.md; TODO.md per outcome above.
8. Run the gate (below).

Anti-over-suppression: N/A (no filter/guard/veto/skip/dedupe path is added to product
code; the only skip is the Outcome-B documented-known-bug skip, whose trigger condition
is itself asserted).

## How to test

```bash
go test ./internal/organizer/ -run 'TestCreateOrganizedVersion' -race -v
# Expected: demotion test PASSES; survival test PASSES (Outcome A) or SKIPs with the W5d-1 message (Outcome B). Any FAIL = broken fixture — fix before proceeding.
go test ./internal/organizer/ ./internal/database/ -short -race
# Expected: green (no product code changed, so any failure is pre-existing — verify against origin/main before blaming your diff)
make ci
```

staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
you changed; the merge gate is Minimal CI green. The `sdkguard` step is ALSO red on
main (#1795, fixed by TASK-03) — a failure listing only `internal/logger` +
`internal/dedup/unified` is pre-existing, not yours.

## Acceptance criteria

- [ ] `git diff --stat origin/main -- internal/organizer/ | grep -v _test.go` shows NO product-file changes (test-only proven mechanically)
- [ ] `TestCreateOrganizedVersion_OriginalDemotedToNonPrimary` green under `-race`
- [ ] `TestCreateOrganizedVersion_AuthorSeriesSurviveOriginalWriteback` is green-or-documented-skip per the two-outcome protocol; PR body states WHICH outcome occurred with the test output pasted
- [ ] Nil semantics honored: no skip on fixture/`GetBookByID` errors (only the Outcome-B wipe skip exists in the file)
- [ ] TODO.md:62 item updated per outcome (checked off on A; annotated-not-checked on B)
- [ ] On Outcome B ONLY: a severity-tagged GitHub issue for the fail-open hydrate fix exists and its number is referenced in the TODO.md annotation AND the BLOCKED line (the skip alone is not closure)
- [ ] Anti-over-suppression: N/A
- [ ] Tests green; vet/lint clean; file headers bumped on every changed file

## Commit message

```
test(organizer): verify Author/Series fate in CreateOrganizedVersion write-back (STOREFID W5d-1)

Regression coverage for the original-book slim write-back (TODO.md:62): demotion
invariants asserted against a real PebbleStore, plus the Author/Series-survival
probe under the locked two-outcome protocol. Test-only; the fail-open hydrate
fix remains a separate, decision-carrying follow-up.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/bug-techdebt-w5d1-verify-writeback
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "TestCreateOrganizedVersion_AuthorSeriesSurviveOriginalWriteback" internal/organizer/organized_version_writeback_test.go`
hits, this task is already applied — run the acceptance checks instead of re-applying.
Rollback = revert the single commit (removes the test file); product behavior was
never touched, in any outcome.
