<!-- file: docs/agent-tasks/dedup-pipeline-hardening/TASK-01-folder-duplicates-getter.md -->
<!-- version: 1.0.0 -->
<!-- guid: 73d9d6dc-a06d-4d60-98aa-ed6555716259 -->
<!-- last-edited: 2026-07-10 -->

# TASK-01 — Implement GetFolderDuplicatesCore on both backends; revive dedup tier 2 (INIT-2 T1)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). EXCEPTIONS: T3's 387k-backlog drain and T6's CONS-10 prod drain are prod-data mutations -> dry-run FIRST, then a real AskUserQuestion apply gate.
**File-ownership:** none for this task's files (INIT-2 owns `internal/dedup/engine.go` and `internal/database/embedding_store.go`, but this task touches neither). Do NOT edit those two files here.

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · store-getter implementation subagent · **Why:** two-backend store logic with fidelity discipline, not mechanical · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-pipeline-hardening-folder-duplicates-getter" -b agent/dedup-pipeline-hardening-folder-duplicates-getter origin/main
cd "$REPO/.worktrees/dedup-pipeline-hardening-folder-duplicates-getter"
git rebase origin/main
```

## Goal

Replace the no-op stub `GetFolderDuplicatesCore()` with a real implementation on BOTH storage
backends — PebbleStore (memdb delegation + paged scan fallback) and the MemStore twin in
`memdb_reads.go` — plus a MockStore function hook, so dedup tier 2 ("same title in same
folder, e.g. M4B + MP3") in `ScanBookDuplicates` and `AudiobookService.GetDuplicateBooks`
starts returning groups. REUSE: `GetAllBooksCore` for the paged scan, the memdb-delegation
shape of `GetBooksBySeriesIDCore`, and `BookCore` (never full `Book`). Do NOT invent a new
normalization helper if one exists — search for an existing title normalizer in
`internal/database` first (`grep -rn "func normalizeTitle" internal/ | head`) and reuse it;
only add a small unexported helper if none exists.

## Background (verify before editing)

- Both stub getters live side by side in `internal/database/pebble_store.go` and `return nil, nil`;
  the doc comment above them says "known-unimplemented stub on both storage backends today".
  This task implements ONLY `GetFolderDuplicatesCore` — the metadata-fuzzy sibling is TASK-02
  (same files, later wave): leave `GetDuplicateBooksByMetadataCore` untouched.
- The interface declaration already exists in `internal/database/iface_book.go` — no interface
  change, no consumer change. Consumers: `internal/dedup/book_dedup.go` (tier 2, logs
  `"folder dedup failed"` and continues on error) and `internal/audiobooks/service_single.go`
  (logs `"folder duplicate detection failed"` and continues). Errors are therefore fail-open —
  return `nil, err` and let callers warn.
- The MemStore twin pattern: `PebbleStore` methods check `p.UseMemDB && p.mem() != nil` and
  delegate to `p.mem().<Method>(...)`; `p.mem()` returns `*MemStore` (defined in
  `memdb_store.go`, read methods in `memdb_reads.go`).
- `MockStore` (`internal/database/mock_store.go`) has a stub `GetFolderDuplicatesCore` with NO
  function hook; its sibling `GetDuplicateBooksByMetadataCore` already has the
  `GetDuplicateBooksByMetadataFunc` hook shape to mirror.
- Semantics of a "folder duplicate group": ≥2 books that (a) have the same normalized title,
  (b) whose files all live in the SAME single parent directory (both books' dirs equal),
  (c) are not marked for deletion, (d) are primary versions (mirror the exclusions the other
  Core getters apply — check what `GetBooksBySeriesIDCore` filters and match it).
- Nil/unknown semantics (spell-out): a book with NO files, or files spread across MULTIPLE
  distinct parent dirs, has UNKNOWN parent dir — it is silently skipped for tier 2, never
  grouped and never an error. An empty/whitespace title is skipped (an empty-title bucket
  would glue unrelated books together).

- **Re-verify these anchors before editing** — line numbers drift:
  All commands use `grep -E` (POSIX extended regex) so alternation (`|`) and escaped
  parens work identically under GNU grep, BSD/macOS grep, and ripgrep shims — do not
  rewrite them into BRE `\|` form.

  ```bash
  # Edit target: the two stubs (~pebble_store.go:1047-1056, 2 hits)
  grep -nE 'func \(p \*PebbleStore\) GetFolderDuplicatesCore|func \(p \*PebbleStore\) GetDuplicateBooksByMetadataCore' internal/database/pebble_store.go
  # Interface decl (no edit needed, >=2 hits)
  grep -nE 'GetFolderDuplicatesCore|GetDuplicateBooksByMetadataCore' internal/database/iface_book.go
  # Copy-from source: memdb delegation shape (>=1 hit)
  grep -nE 'func \(p \*PebbleStore\) GetBooksBySeriesIDCore' internal/database/pebble_store.go
  # Copy-from source: MemStore twin shape (>=1 hit)
  grep -nE 'func \(m \*MemStore\) GetBooksBySeriesIDCore' internal/database/memdb_reads.go
  # MockStore hook shape to mirror (>=2 hits)
  grep -nE 'GetDuplicateBooksByMetadataFunc|func \(m \*MockStore\) GetFolderDuplicatesCore' internal/database/mock_store.go
  # Consumers (context only — do not edit): tier 2 call + audiobooks service call
  grep -n 'GetFolderDuplicatesCore' internal/dedup/book_dedup.go internal/audiobooks/service_single.go
  ```
  Zero hits on any edit-target grep at execution time = STOP and report.

## Step-by-step

1. Run the anchor greps above. Open `internal/database/pebble_store.go`, locate the
   `GetFolderDuplicatesCore` stub (never trust line numbers from this brief).
2. Implement the Pebble path: if `p.UseMemDB && p.mem() != nil`, `return p.mem().GetFolderDuplicatesCore()`
   (mirror `GetBooksBySeriesIDCore`'s delegation exactly). Fallback: page through
   `GetAllBooksCore(limit, offset)` (bounded pages, e.g. 500 — mirror an existing pager in
   the same file), and for each qualifying book resolve its single parent dir from
   `GetBookFiles(bookID)` — all files must share one `filepath.Dir`; else skip the book
   (UNKNOWN dir, non-disqualifying skip, see Background). Bucket by
   `(normalizedTitle, parentDir)`; emit each bucket with ≥2 books as one `[]BookCore` group.
   ONE pass over books — never a per-book title query fan-out (O(N²) shape, forbidden).
3. Implement the MemStore twin `func (m *MemStore) GetFolderDuplicatesCore() ([][]BookCore, error)`
   in `internal/database/memdb_reads.go`, same bucketing over the memdb books table (mirror
   how sibling MemStore readers iterate; reuse their txn/iterator helpers).
4. In `internal/database/mock_store.go`, add a `GetFolderDuplicatesCoreFunc func() ([][]BookCore, error)`
   field and make `GetFolderDuplicatesCore` call it when non-nil (mirror
   `GetDuplicateBooksByMetadataFunc` exactly).
5. Update the now-stale stub doc comments (both the Pebble getter's comment and the
   `iface_book.go` comment if it still says unimplemented) — remove the
   "known-unimplemented stub" claim for THIS getter only; leave the metadata getter's
   stub + comment for TASK-02.
6. Keep the change purely additive elsewhere: do not touch `GetDuplicateBooksByMetadataCore`,
   do not change any signature, do not edit consumers, do not reorder imports beyond gofmt.
7. NEW `internal/database/pebble_store_folder_dups_test.go`: fixture with (a) two books, same
   normalized title, files in the same dir → one group of 2; (b) same title, different dirs →
   no group; (c) single book with a unique title → no group; (d) marked-for-deletion book
   excluded; (e) a book with files in two dirs → skipped, run still returns the other groups
   (non-disqualifying UNKNOWN, anti-over-suppression); run the same fixture through BOTH the
   Pebble scan path and the MemStore twin and assert identical groups.
8. Bump the file header (version + last-edited) on every file you touch; keep existing guids.
9. Run the gates (below). Store-getter discipline: the FULL `go test ./... -short`, never a
   package subset — mocks in unexpected consumer packages fail vacuously otherwise.

## How to test

```bash
make ci
go test ./... -short
```

Caveat (verbatim): staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck
to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -nE 'func \(m \*MemStore\) GetFolderDuplicatesCore' internal/database/memdb_reads.go` hits (twin exists)
- [ ] `grep -n "GetFolderDuplicatesCoreFunc" internal/database/mock_store.go` hits (hook exists)
- [ ] The folder getter's "known-unimplemented stub" comment is gone; TASK-02's metadata stub is untouched (`grep -n 'return nil, nil' internal/database/pebble_store.go` still hits for the metadata getter)
- [ ] Anti-over-suppression: fixture case (e) — a multi-dir book is skipped but the remaining valid group is still returned (test name contains `MultiDirSkippedOthersStillGrouped`)
- [ ] Tests green: `make ci` exits 0 AND full `go test ./... -short` exits 0; vet/lint clean on changed files.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: " <file>` shows 2026-07-10 or later).

## Commit message

```
feat(database): implement GetFolderDuplicatesCore on Pebble + MemStore (INIT-2 T1)

Replaces the known-unimplemented stub on both backends so dedup tier 2
(same-title-same-folder) in ScanBookDuplicates and
AudiobookService.GetDuplicateBooks receives real groups. Single-pass
bucketing by (normalizedTitle, parentDir) — no per-book query fan-out.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dedup-pipeline-hardening-folder-duplicates-getter
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -nE 'func \(m \*MemStore\) GetFolderDuplicatesCore' internal/database/memdb_reads.go` hits AND `grep -n "known-unimplemented stub" internal/database/pebble_store.go` no longer matches the FOLDER getter's doc comment, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; the stubs return and both consumers fall back to today's empty-tier behavior; no data or schema is touched.
