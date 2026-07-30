<!-- file: docs/agent-tasks/abs-sync/TASK-04-syncid-backfill.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0d6ee191-16c1-4100-8543-1f6fe9d92cb0 -->
<!-- last-edited: 2026-07-30 -->

# TASK-04 — Idempotent sync-ID backfill over the existing library (ABS-SYNC-ID-4)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Sonnet-class go-backend subagent, concurrency-aware · **Why:** a library-scale loop over "tens of thousands of books" per the workstream's own scale — this is exactly the shape CLAUDE.md's concurrency rule targets, and this repo has a real 3-hour single-core incident on record for skipping it · **Depends on:** TASK-01, TASK-02

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI). Additive/backfill-only against keys nothing else reads yet; safe to re-run any number of times.
**File-ownership:** owns a new file under `internal/maintenance/jobs/` (this task's exclusive file — no collision with TASK-01/02/03/05). **Do not touch `internal/database/pebble_store_syncid.go`** (TASK-01/02's file) — only call its exported methods via `database.AsSyncIdentityStore`/`database.AsSyncFileStore`.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/abs-sync-syncid-backfill" -b agent/abs-sync-syncid-backfill origin/main
cd "$REPO/.worktrees/abs-sync-syncid-backfill"
git rebase origin/main
```

Confirm TASK-01 and TASK-02 have merged before writing code:
```bash
grep -n "func (p \*PebbleStore) MintOrGetSyncID\|func (p \*PebbleStore) MintOrGetSyncFileID" internal/database/pebble_store_syncid.go
# Expected: 2 hits. If either is missing, stop and wait — this task only calls
# those methods, it does not reimplement them.
```

Module path is `github.com/falkcorp/audiobook-organizer` (**falkcorp**, not jdfalk).

## Goal

Assign a `syncID` (TASK-01) to every existing Book, and a `syncFileID` (TASK-02) to
every existing `BookFile`, so a day-one ABS client sees a fully-populated,
stable-identity library instead of minting IDs one-by-one on first request (which
would still work, per TASK-01/02's mint-on-first-encounter design, but a backfill
means the whole library is consistent before any client ever connects — no
first-request latency spike, no partially-identified library mid-rollout). Must be
**idempotent**: safe to run repeatedly, never re-mints an ID that already exists,
resumable after a restart.

## Background (verify before editing)

- **CLAUDE.md's concurrency rule, verbatim reason this matters:** a `dedup.full-scan`
  run went silent for 3+ hours at 100% CPU on a single core on 2026-07-05 because a
  "unified scoring" pass was a plain `for range books` loop with no worker pool (re-verify:
  `grep -n "went silent for 3+ hours" CLAUDE.md` — expected: 1 hit). This backfill
  iterates "tens of thousands of books" (per this workstream's own README) — exactly
  the whole-library scale the rule targets. **A bare `for _, id := range bookIDs`
  calling `MintOrGetSyncID` per book is the forbidden pattern here.** Use a bounded
  worker pool sized to `runtime.NumCPU()` (this is local Pebble I/O — CPU/disk-bound,
  not network-bound, so the CPU-count sizing applies, not a small fixed network
  concurrency knob).
- **Use `ListBookIDs()`, not a paginated full-book fetch.** Project memory records a
  real bug class here: `GetAllBooksFrom`'s memdb path silently caps at 2× the
  requested limit on the prod memdb fast path, so a naive paginated loop can miss
  books. `ListBookIDs()` (re-verify:
  `grep -n "^func (p \*PebbleStore) ListBookIDs" internal/database/pebble_store.go`
  — expected: 1 hit, ~line 593) returns the complete ID set directly (delegates to
  memdb's own non-paginated `ListBookIDs` when available, or walks the raw Pebble
  key range otherwise) with no pagination cap. Then fetch each book's data via
  `GetBookByID(id)` inside the worker (or skip fetching the full `Book` at all —
  `MintOrGetSyncID` only needs the ID string, not the Book struct; only
  `GetBookFiles(bookID)` needs the ID, also not the full Book).
- **Do NOT use the plugin-sdk pattern** (`internal/plugins/acoustid/backfill.go`) —
  it requires a full `sdk.OperationDef`/`sdk.Reporter` registration inside a plugin,
  which is unnecessary machinery for a one-shot library backfill and this task is
  not a plugin. **Use the lighter pattern already in this codebase for a
  plain-loop-with-a-worker-pool that isn't a plugin:**
  `internal/server/embedding_backfill.go`'s `embeddingBackfillReporter` (re-verify:
  ```bash
  grep -n "embeddingBackfillReporter is a minimal registry.Reporter adapter" internal/server/embedding_backfill.go
  # Expected: 1 hit, ~line 30 — its own comment explains exactly why: "this loop
  # runs as a plain background goroutine ... rather than through the operation
  # registry ... there is no Reporter already in scope"
  ```
  ) — a tiny struct implementing all 7 methods of `registry.Reporter`
  (`internal/operations/registry/reporter.go`, re-verify:
  `grep -n "^type Reporter interface" -A 9 internal/operations/registry/reporter.go`
  — expected: 9-line block listing `UpdateProgress/Log/Logger/Checkpoint/IsCanceled/RunPhase/Trigger/SetCurrentItem`),
  most of them thin/no-op, so `registry.RunItems[T]` can drive the concurrency.
- **`registry.RunItems`'s `Concurrency` field** (re-verify:
  `grep -n "Concurrency int" internal/operations/registry/run_items.go` — expected: 1
  hit, with the doc comment "0 or 1 = sequential (default)") — you must set it
  explicitly to `runtime.NumCPU()`; the zero value is sequential, which would
  silently reproduce the exact anti-pattern this task exists to avoid.
- **Register as a `maintenance.MaintenanceJob`** so it's runnable/observable from
  the existing ops UI, matching this repo's convention for one-shot library
  backfills. Interface (re-verify: `grep -n "^type MaintenanceJob interface" -A 15 internal/maintenance/job.go`
  — expected: 15-line block): `ID() string`, `Name() string`, `Description()
  string`, `Category() string`, `DefaultParams() any`, `CanResume() bool`,
  `Run(ctx context.Context, store database.Store, reporter ProgressReporter, dryRun
  bool) error`. Registration is `init() { maintenance.Register(&yourJob{}) }`
  (re-verify: `grep -n "func Register(j MaintenanceJob)" internal/maintenance/registry.go`
  — expected: 1 hit). **`maintenance.ProgressReporter` (3 methods: `SetTotal`,
  `Increment`, `Log`) is a DIFFERENT, simpler interface than `registry.Reporter` (7
  methods)** — your job's `Run` receives the former; wrap it in an
  `embeddingBackfillReporter`-style adapter to drive `registry.RunItems` with the
  latter.
- **What NOT to copy:** `internal/maintenance/jobs/backfill_file_hashes.go` (re-verify:
  `grep -n "for i := resumeIndex; i < len(files); i++" internal/maintenance/jobs/backfill_file_hashes.go`
  — expected: 1 hit, ~line 46) is a plain sequential loop with no worker pool — it
  is useful ONLY for its job-registration/checkpoint-boilerplate shape (the
  `init()`/`ID()`/`Category()`/`opID := maintenance.OperationIDFromCtx(ctx)` /
  `operations.SaveCheckpoint` pattern), not for its loop concurrency, which is
  itself an unaddressed instance of the exact anti-pattern this task must avoid.
  Combine that file's registration/checkpoint scaffolding with
  `embedding_backfill.go`'s concurrency pattern — do not reproduce
  `backfill_file_hashes.go`'s bare `for` loop.
- **Idempotency check for `sync_file`:** use TASK-02's `ListSyncFilesForBook(bookID)`
  to see which `BookFile.ID`s on a book already have a `sync_file` entry before
  minting, so a re-run's per-file work is a cheap point-get skip rather than
  redundant writes (still correct either way since `MintOrGetSyncFileID` itself is
  idempotent — this is a performance/logging-clarity nicety, not a correctness
  requirement).
- **Why this task backfills BOTH keyspaces** (`sync_item` from TASK-01 AND
  `sync_file` from TASK-02, even though the workstream README describes it in one
  line as "sync-ID backfill"): the wave table lists this task as depending on
  **both** TASK-01 and TASK-02 — if it only backfilled `sync_item`, that dependency
  on TASK-02 would be unused. A book with a `syncID` but whose files have no
  `syncFileID` yet is only half cut-over; a day-one client requesting that book's
  file list would still hit the mint-on-first-encounter path for every file. Do
  both in the same pass over the library (one `ListBookIDs()` walk, and inside each
  book's worker, also loop its `GetBookFiles(bookID)`).

## Step-by-step

1. Create `internal/maintenance/jobs/backfill_sync_ids.go` with the standard header.
2. Define the job struct and registration:
   ```go
   func init() { maintenance.Register(&backfillSyncIDsJob{}) }

   type backfillSyncIDsJob struct{}

   func (j *backfillSyncIDsJob) ID() string         { return "backfill-sync-ids" }
   func (j *backfillSyncIDsJob) Name() string       { return "Backfill ABS Sync IDs" }
   func (j *backfillSyncIDsJob) Category() string   { return "library" }
   func (j *backfillSyncIDsJob) DefaultParams() any { return struct{}{} }
   func (j *backfillSyncIDsJob) Description() string {
   	return "Mints durable ABS sync_item/sync_file identities for every book and book file that doesn't have one yet"
   }
   func (j *backfillSyncIDsJob) CanResume() bool { return false } // idempotent re-run IS the resume story; see below
   ```
   `CanResume() bool` returns `false` deliberately: unlike
   `backfill_file_hashes.go`'s index-based checkpoint/resume, this job's
   correctness does not depend on resuming mid-list — because every mint call is
   independently idempotent, simply re-running the whole job from book 0 after an
   interruption is correct and cheap (already-minted books/files are a fast
   point-get skip, not re-work). Say this in a comment so a reviewer doesn't
   read `false` as "doesn't support restart safety."
3. Implement `Run`:
   ```go
   func (j *backfillSyncIDsJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
   	syncStore := database.AsSyncIdentityStore(store)
   	fileStore := database.AsSyncFileStore(store)
   	if syncStore == nil || fileStore == nil {
   		return fmt.Errorf("store does not implement sync-identity interfaces (TASK-01/02 not present)")
   	}

   	ids, err := store.ListBookIDs()
   	if err != nil {
   		return fmt.Errorf("list book ids: %w", err)
   	}
   	reporter.SetTotal(len(ids))

   	var minted, filesMinted, errs int64
   	rep := &registryReporterAdapter{ctx: ctx, inner: reporter}
   	runErr := registry.RunItems(ctx, rep, ids, func(ctx context.Context, bookID string) error {
   		if dryRun {
   			return nil
   		}
   		if _, err := syncStore.MintOrGetSyncID(bookID); err != nil {
   			atomic.AddInt64(&errs, 1)
   			slog.Warn("backfill-sync-ids: mint syncID", "book", bookID, "err", err)
   			return nil // best-effort: one bad book must not stop the whole run
   		}
   		atomic.AddInt64(&minted, 1)

   		files, ferr := store.GetBookFiles(bookID)
   		if ferr != nil {
   			atomic.AddInt64(&errs, 1)
   			slog.Warn("backfill-sync-ids: list book files", "book", bookID, "err", ferr)
   			return nil
   		}
   		for _, f := range files {
   			if _, err := fileStore.MintOrGetSyncFileID(bookID, f.ID); err != nil {
   				atomic.AddInt64(&errs, 1)
   				slog.Warn("backfill-sync-ids: mint syncFileID", "book", bookID, "file", f.ID, "err", err)
   				continue
   			}
   			atomic.AddInt64(&filesMinted, 1)
   		}
   		return nil
   	}, registry.RunItemsOptions{
   		Concurrency: runtime.NumCPU(),
   		ErrMode:     registry.ErrModeCollect,
   		Label: func(i, total int) string { return fmt.Sprintf("book %d/%d", i+1, total) },
   	})
   	if runErr != nil {
   		return runErr
   	}
   	reporter.Log("info", fmt.Sprintf("Backfill complete: %d books, %d files minted, %d errors", minted, filesMinted, errs), nil)
   	return nil
   }
   ```
   Note `ErrMode: registry.ErrModeCollect` — a handful of unreadable books/files must
   not abort the whole library's backfill (`ErrModeFail`, the default, would cancel
   remaining items on the first error). Per-item errors are already swallowed
   (`return nil`) with a warn log and an error counter, so `ErrModeCollect` here is
   belt-and-suspenders for any error `RunItems` itself might surface (e.g. context
   cancellation).
4. Implement the reporter adapter in the same file, modeled directly on
   `embeddingBackfillReporter` (re-verify: `grep -n "func (r \*embeddingBackfillReporter)" internal/server/embedding_backfill.go`
   — expected: 8 hits, one per method):
   ```go
   // registryReporterAdapter bridges maintenance.ProgressReporter (this job's
   // Run signature) to registry.Reporter (what registry.RunItems needs to drive
   // the worker pool), mirroring internal/server/embedding_backfill.go's
   // embeddingBackfillReporter for the same reason: a plain maintenance job,
   // like that background goroutine, has no registry.Reporter already in scope.
   type registryReporterAdapter struct {
   	ctx   context.Context
   	inner maintenance.ProgressReporter
   }

   func (r *registryReporterAdapter) UpdateProgress(current, total int, message string) error {
   	r.inner.Increment()
   	return nil
   }
   func (r *registryReporterAdapter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
   	slog.Default().LogAttrs(context.Background(), level, message, attrs...)
   	return nil
   }
   func (r *registryReporterAdapter) Logger() *slog.Logger { return slog.Default() }
   func (r *registryReporterAdapter) Checkpoint(state any) error { return nil }
   func (r *registryReporterAdapter) IsCanceled() bool { return r.ctx != nil && r.ctx.Err() != nil }
   func (r *registryReporterAdapter) RunPhase(ctx context.Context, name string, fn func(context.Context, registry.Reporter) error) error {
   	return fn(ctx, r)
   }
   func (r *registryReporterAdapter) Trigger(ctx context.Context, eventName string, payload any) error { return nil }
   func (r *registryReporterAdapter) SetCurrentItem(label string) {}
   ```
5. Add the file header (fresh guid).
6. Add a `changelog.d/` fragment: `changelog.d/abs-sync-syncid-backfill.md` (guid
   `7b549608-7fdf-4682-9a76-9325a2d6cd0a`), category `Added`.

## How to test (TDD — write these first, run red, then implement)

Create `internal/maintenance/jobs/backfill_sync_ids_test.go`:

1. **Fresh library:** seed N books (use a small N like 20, not library-scale — the
   test proves correctness, not throughput) each with 1-3 `BookFile`s via a real
   `database.NewPebbleStore(t.TempDir())`. Run the job. Assert every book has a
   `syncID` (`GetSyncIDForBook`) and every file has a `syncFileID`
   (`GetSyncFileID`).
2. **Idempotent re-run:** run the job twice. Assert the SAME `syncID`/`syncFileID`
   values result both times (capture the full ID set after run 1, diff against run
   2 — must be byte-identical, not just "still present").
3. **Partial pre-existing state:** manually call `MintOrGetSyncID` for half the
   books before running the job (simulating some books already hit by
   mint-on-first-encounter via a live request). Run the job. Assert those books'
   pre-existing IDs are unchanged (not re-minted) and the other half now have IDs
   too.
4. **`dryRun: true`:** run with `dryRun` true; assert zero `sync_item`/`sync_file`
   keys exist afterward (`GetSyncIDForBook` returns `false` for every book).
5. **Concurrency sanity:** with `Concurrency: runtime.NumCPU()` and ≥2 CPUs
   available in CI, run with `-race` over ~50 books; assert no race and that every
   book still ends up with exactly one `syncID` (parallel workers must not each
   mint a different one for the same never-before-seen book — this exercises
   TASK-01's own mint-race mutex from the caller side, not a new lock in this
   file).
6. **Bounded pool, not sequential:** assert `registry.RunItemsOptions.Concurrency`
   is set to a value `> 1` in the actual call (grep the source in the test, or
   structure the call so `Concurrency` is a named package-level var/const the test
   can read) — this guards against a future edit silently reverting to the
   `Concurrency: 0` sequential default.

## Acceptance criteria

- [ ] `internal/maintenance/jobs/backfill_sync_ids.go` registers a
      `backfillSyncIDsJob` via `init()`
- [ ] `Run` uses `store.ListBookIDs()` (not a paginated `GetAllBooksFrom` call) and
      `registry.RunItems` with `Concurrency: runtime.NumCPU()`
- [ ] Backfills BOTH `sync_item` (books) and `sync_file` (book files) in one pass
- [ ] All 6 tests in "How to test" pass, including the `-race` concurrency test
- [ ] `dryRun: true` mints nothing
- [ ] Re-running the job is a byte-identical no-op on already-minted IDs
- [ ] `go build ./...`, `gofmt -l`, `go vet ./internal/maintenance/...` clean
- [ ] File headers present/bumped
- [ ] `changelog.d/abs-sync-syncid-backfill.md` added

## Commit message

```
feat(abs-sync): idempotent backfill for sync_item + sync_file (ABS-SYNC-ID-4)

Mints TASK-01's syncID and TASK-02's syncFileID for every existing book
and book file so a day-one ABS client sees a fully identity-consistent
library instead of relying purely on mint-on-first-encounter. Registered
as an internal/maintenance job; the per-book work runs through
registry.RunItems with Concurrency: runtime.NumCPU() -- a bare
sequential loop over "tens of thousands of books" is the exact pattern
that caused a documented 3-hour single-core stall in this repo
(2026-07-05, dedup.full-scan). Reporter bridging follows
embedding_backfill.go's adapter pattern rather than the heavier
plugin-sdk path, since this is a one-shot maintenance job, not a plugin.
Every mint call is independently idempotent, so re-running after an
interruption from book 0 is the resume story -- no separate checkpoint
index is needed.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01J29y3VpN7FTczJmLeUJimt
```

## PR + merge

```bash
git push -u origin agent/abs-sync-syncid-backfill
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `internal/maintenance/jobs/backfill_sync_ids.go` already exists with a registered
`backfill-sync-ids` job ID, the transform is already done — run the acceptance
checks instead of re-adding. Rollback = revert the single commit (delete the new
file); no other code calls it yet, and any `sync_item`/`sync_file` keys it already
wrote on a prior run remain valid (they are not undone by reverting the job code —
this backfill is additive data, not a migration that needs an "undo").
