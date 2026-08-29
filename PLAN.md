<!-- file: PLAN.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2f8b6d41-9c07-4e35-a1d8-5b3e7c0a94f2 -->
<!-- last-edited: 2026-08-29 -->

# Bulk CoW snapshot prune

## Goal
Prune `book_ver:` copy-on-write snapshots across the whole library. Today the only
control is `POST /api/v1/audiobooks/:id/cow-versions/prune`, one book at a time;
prod has 59,086 books holding ~7.5M snapshots (~7.65 GB).

Measured 2026-08-29 (40-book sample): min 42, median 91, mean 127.5, max 697
snapshots per book. keep_count=10 deletes 92.2%.

## Files to change

1. `internal/database/pebble_store.go`
   - Rewrite `PruneBookSnapshots` to iterate KEYS ONLY.
     It currently calls `GetBookSnapshots(id, 0)`, which copies every snapshot's
     value (`dataCopy := make(...); copy(...)`) just to read a timestamp that is
     already in the key. A library-wide prune would therefore read and copy all
     7.65 GB of snapshot payloads to discover keys it already had.
   - Add `CountBookSnapshots(id) (int, error)` — keys-only, for a cheap dry run.
2. `internal/database/iface_book.go` — declare `CountBookSnapshots`.
3. `internal/maintenance/job.go` — add `PruneBookSnapshots` to `jobBookWriter`,
   `CountBookSnapshots` to `jobBookReader`. (`ListBookIDs` already present.)
4. `internal/maintenance/jobs/prune_book_snapshots.go` — NEW job.
5. Regenerate mocks (`make mocks`).
6. Tests + changelog fragment.

## Job design

- ID `prune-book-snapshots`, category `cleanup`.
- Params: `keep_count int` (default 10), `dry_run bool`.
- **Refuses keep_count < 1.** 0 would delete every snapshot including the newest;
  this is irreversible prod data, so the destructive extreme is not reachable by
  omitting a field.
- Bounded worker pool: `errgroup` + `SetLimit(runtime.NumCPU())`, per CLAUDE.md's
  concurrency mandate (59k books x a DB write each).
- **Partitioning is inherent, not assumed:** the unit of work is ONE book ID and
  every `book_ver:` key is prefixed by that ID, so two workers can never touch the
  same key. No shared mutable state except counters (atomic).
- Honours `ctx` cancellation between books; reports progress per book so the
  registry watchdog does not flag it as wedged.
- Dry run counts via `CountBookSnapshots` and writes nothing.

## Test strategy
- Store: prune keeps the newest N and deletes the rest; count matches; malformed
  keys are left alone (preserves current behaviour); keep_count >= len is a no-op.
- Job: dry run deletes NOTHING but reports the real number; keep_count is honoured;
  cancellation stops early; keep_count < 1 is refused.
- Mutation-test each new test before trusting it.

## Rollback
The job is additive and opt-in — nothing schedules it. Reverting the commit
removes it. The store change is behaviour-preserving (same keys deleted); its
tests pin that.

## NOT in scope
Deleting anything on prod. This ships the capability; running it is a separate,
explicit decision, and dry-run comes first.
