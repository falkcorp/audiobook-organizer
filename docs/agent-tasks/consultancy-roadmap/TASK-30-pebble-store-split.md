<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-30-pebble-store-split.md -->
<!-- version: 1.0.0 -->
<!-- guid: cf0a92e5-002a-41d8-84d2-4150c3eae799 -->
<!-- last-edited: 2026-07-03 -->

# TASK-30 — Split `pebble_store.go` into per-domain files (consultancy SYS-5)

**Priority:** P3 · **Effort:** L · **Recommended subagent:** Opus · **Wave:** 6
· **Depends on:** TASK-02, TASK-03

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-30-pebble-store-split" -b agent/cr-30-pebble-store-split origin/main
cd "$REPO/.worktrees/cr-30-pebble-store-split"
git rebase origin/main
```

**This task runs LAST (wave 6).** It touches almost every method on
`PebbleStore` and will conflict with anything else editing
`internal/database/pebble_store*.go`. Do not start it until TASK-02 and
TASK-03 (its declared dependencies) have merged, and confirm no other
in-flight branch touches `internal/database/pebble_store.go` before you begin
(`git log --oneline origin/main -20 -- internal/database/pebble_store.go`).

## Goal

Consultancy finding **SYS-5** (`docs/consultancy/01-storage-architecture.md`):
`internal/database/pebble_store.go` is 11,398 lines — the single largest file
in the repo by ~4x, and (per CLAUDE.md's mandated parallel-subagent worktree
workflow) the most likely merge-conflict hotspot across concurrent waves.

This is a **pure mechanical split**: move existing method bodies into new
`pebble_store_<domain>.go` files in the same `database` package. **Zero
behavior change. Zero signature change. Zero logic change.** Do not "improve"
anything you touch — if you spot a bug while moving code, do NOT fix it here;
log it as a follow-up (new TODO.md entry or a one-line note in your final PR
description) and move on. Mixing refactor-with-behavior-change into a
move-only PR makes it unreviewable and unrevertable.

## Background (verify before editing)

- The team has already established the split pattern. These per-domain files
  already exist and should be treated as the naming precedent — **do not
  duplicate or rename them**:
  ```
  internal/database/pebble_store_book_aggregates.go
  internal/database/pebble_store_isbn_index.go
  internal/database/pebble_store_lsh.go
  internal/database/pebble_store_ops_v2.go
  internal/database/pebble_store_mark_import.go
  internal/database/pebble_store_metadata_cache.go
  internal/database/pebble_store_versiongroup_backfill.go
  ```
- **Correction to the task-spec's example file list:** the consultancy brief
  that spawned this task mentions splitting off `..._dedup.go` and
  `..._embeddings.go`. As of this writing, dedup and embeddings are **already
  in separate files** (`internal/database/dedup_label.go`,
  `internal/database/embedding_store.go`, `internal/database/
  hnsw_embedding_store.go`, `internal/database/chromem_embedding_store.go`) —
  none of that logic lives in `pebble_store.go`. Verify this before doing any
  work in that area:
  ```bash
  grep -n "Embedding\|Dedup" internal/database/pebble_store.go
  ```
  If this still returns nothing but comments, skip dedup/embeddings entirely —
  there is nothing to move there. Do not invent new files for domains that
  aren't actually present in `pebble_store.go`.
- As of this writing `pebble_store.go` is 11,398 lines and defines ~388
  methods on `(p *PebbleStore)` / `(s *PebbleStore)` (both receiver names are
  used inconsistently in the existing file — **preserve each method's existing
  receiver name verbatim when moving it**, do not normalize it as part of this
  task). Confirm current scope:
  ```bash
  wc -l internal/database/pebble_store.go
  grep -c -E "^func \([a-zA-Z]+ \*PebbleStore\)" internal/database/pebble_store.go
  ```

### Re-verify the method inventory before planning

Line numbers below are from this writing and **will have drifted** — do not
trust them. Regenerate the authoritative list yourself:

```bash
grep -noE "^func \([a-zA-Z]+ \*PebbleStore\) [A-Za-z0-9_]+" internal/database/pebble_store.go
```

### Recommended domain grouping (starting point, not gospel)

This grouping was derived by reading the method inventory above. Use it as
your starting plan, but you own the final grouping — if two domains are so
entangled that splitting them creates awkward cross-file private-helper
sharing, merge them into one file instead of forcing the split. The goal is
navigability, not a fixed file count.

| New file | Method groups (representative names — regenerate exact list) |
|---|---|
| `pebble_store_authors.go` | `GetAllAuthors`, `GetAuthorByID`, `CreateAuthor`, `DeleteAuthor`, `UpdateAuthorName`, `*AuthorAlias*`, `CreateNarrator`/`*Narrator*`, `GetBookAuthors`/`SetBookAuthors`, `GetAuthorsByBookIDs`, `*AuthorTag*`, `GetAuthorsByTag`, `CreateAuthorTombstone`/`GetAuthorTombstone`/`ResolveTombstoneChains` |
| `pebble_store_series.go` | `GetAllSeries*`, `GetSeriesByID/Name/IDs`, `CreateSeries`, `DeleteSeries`, `UpdateSeriesName`, `GetAllSeriesBookCounts*`, `GetAllSeriesFileCounts`, `*SeriesTag*`, `GetSeriesByTag` |
| `pebble_store_works.go` | `GetAllWorks*`, `GetWorkByID`, `CreateWork`, `UpdateWork`, `DeleteWork`, `GetBooksByWorkID`, `GetAllWorkBookCounts` |
| `pebble_store_books.go` (or split further into `..._books_core.go` / `..._books_versions.go` / `..._books_tags.go` if you judge it too big) | `GetAllBooks*`, `GetBookBy*`, `CreateBook`, `UpdateBook`, `DeleteBook`, `SearchBooks`, `Count*Books`, `GetDuplicateBooks*`, `GetBooksBy*`, `*BookSnapshot*`/`*BookVersion*`, `MergeChapterBooks`, `ListSoftDeletedBooks`, `GetBookUserTags`/`SetBookUserTags`/`*BookAlternativeTitle*`, `*BookSegment*` |
| `pebble_store_bookfiles.go` | everything currently receiver `(s *PebbleStore)` around `getBookFileByID`…`MoveBookFilesToBook`, plus `UpdateBookFileHashes`, `SetBookFileHash`, `ClearAllAcoustIDFingerprints`, `SweepBookFileSegDrop`, `LookupAcoustIDCandidates`, `HasLSHIndex` |
| `pebble_store_importpaths.go` | `GetAllImportPaths*`, `GetImportPathByID/Path`, `CreateImportPath`, `UpdateImportPath`, `DeleteImportPath`, `CountBooksByPathPrefix` |
| `pebble_store_operations.go` | `CreateOperation`, `GetOperationByID`, `*Operation*` (status/error/result/log/summary), `SaveOperationState/Params`, `DeleteOperationState`, `DeleteOperationsByStatus`, `DeleteOperationWithLogs`, `GetInterruptedOperations`, `CreateOperationResult*`, `GetOperationResults*`, `GetRecentCompletedOperations`, `CreateOperationChange`, `GetOperationChanges`, `RevertOperationChanges` |
| `pebble_store_metadata.go` | `metadataStateKey`, `*MetadataFieldState*`, `RecordMetadataChange`, `GetMetadataChangeHistory`, `GetBookChangeHistory`, `AddMetadataRejection`, `GetMetadataRejections`, `DeleteMetadataRejections` |
| `pebble_store_preferences.go` | `GetUserPreference`/`SetUserPreference`/`GetAllUserPreferences*`, `*PreferenceForUser*` |
| `pebble_store_playlists.go` | `CreatePlaylist`, `GetPlaylistByID`, `GetPlaylistBySeriesID`, `*PlaylistItem*`, `*UserPlaylist*` |
| `pebble_store_auth.go` | `CreateUser`/`GetUserBy*`/`UpdateUser`/`ListUsers`/`CountUsers`, `*Role*`, `*APIKey*`, `*Invite*`, `*Session*` |
| `pebble_store_playback.go` | `SetUserPosition`, `GetUserPosition`, `ListUserPositions*`, `ClearUserPositions`, `SetUserBookState`, `GetUserBookState`, `ListUserBookStatesByStatus`, `AddPlaybackEvent`, `ListPlaybackEvents`, `UpdatePlaybackProgress`, `GetPlaybackProgress`, `IncrementBookPlayStats`, `GetBookStats`, `IncrementUserListenStats`, `GetUserStats`, `readIntKey`, `incrementIntKey` |
| `pebble_store_blocklist.go` | `IsHashBlocked`, `AddBlockedHash`, `RemoveBlockedHash`, `GetAllBlockedHashes*`, `GetBlockedHashByHash` |
| `pebble_store_itunes.go` | `SetLastWrittenAt`, `MarkITunesSynced`, `GetITunesPurgePendingBooks`, `GetITunesDirtyBooks`, `ClearITunesPID`, `GetBookFileByPID`, `*DeferredITunesUpdate*` |
| `pebble_store_externalids.go` | `CreateExternalIDMapping`, `GetBookByExternalID`, `GetExternalIDsForBook`, `IsExternalIDTombstoned`, `TombstoneExternalID`, `ReassignExternalID*`, `BulkCreateExternalIDMappings`, `MarkExternalIDRemoved`, `SetExternalIDProvenance`, `GetRemovedExternalIDs` |
| `pebble_store_tags.go` | `AddBookTag*`, `RemoveBookTag*`, `GetBookTags*`, `SetBookTags`, `ListAllTags`, `GetBooksByTag`, `pebbleAddTag`/`pebbleRemoveTag`/`pebbleGetTags*`/`pebbleSetTags`/`pebbleListAllTags`/`pebbleEntitiesByTag` (shared generic-tag helpers used by author/series tag wrappers above — keep these together, they're the implementation the thin author/series wrappers call into) |
| `pebble_store_aijobs.go` | `CreateAIJob`, `GetAIJob*`, `MarkAIJobSubmitted/Completed/Failed`, `ListAIJobs` |
| `pebble_store_activity.go` | `AddSystemActivityLog`, `GetSystemActivityLogs`, `PruneOperationLogs`, `PruneOperationChanges`, `PruneSystemActivityLogs` |
| `pebble_store_scancache.go` | `GetScanCacheMap`, `UpdateScanCache`, `MarkNeedsRescan`, `GetDirtyBookFolders`, `RecordPathChange`, `GetBookPathHistory` |
| `pebble_store_quarantine.go` | `GetQuarantinedBooks`, `CountQuarantinedBooks`, `GetScanFailCount`, `IncrScanFailCount`, `ResetScanFailCount` |
| `pebble_store_stats.go` | `CountFiles`, `CountAuthors`, `CountSeries`, `GetBookCountsByLocation`, `GetBookSizesByLocation`, `GetDashboardStats`, `computeLibraryStats`, `GetDuplicateFilesByHash`, `GetBookFileHashStats`, `GetBookMetadataHashStats`, `GetAcoustIDStats`, `GetFilesWithFingerprintFailures`, `SaveLibraryFingerprint`, `GetLibraryFingerprint` |
| `pebble_store.go` (stays — core/lifecycle) | `type PebbleStore struct`, `NewPebbleStore`, `mem`, `IsMemReady`, `WaitForWarmup`, `SetRootDir`, `InvalidateLibraryStats`, `readCachedLibraryStats`/`writeCachedLibraryStats`, `Close`, `Checkpoint`, `DB`, `nextID`, `migrateImportPathKeys`, `Reset`, `WipeByPrefixes`, `Optimize`, `CountByPrefix`, `SetRaw`/`GetRaw`/`DeleteRaw`/`ScanPrefix`/`CountPrefix`, `KeyCount` — anything that is truly cross-cutting store infrastructure rather than a single domain |

## Step-by-step

1. Regenerate the method inventory (grep command above) and write your own
   final method→file mapping as a plain list (keep it in your PR description
   or a scratch file — it does not need to ship). Diff it against the table
   above; adjust groupings where files would end up too small (<50 lines —
   fold into a neighboring domain) or too large (>1500 lines — split further,
   e.g. `pebble_store_books_core.go` / `pebble_store_books_versions.go` /
   `pebble_store_books_tags.go`).
2. For each target file, create it with the standard file header (bump
   version per `.standards/instructions/file-headers.md`; these are new files
   so start at `version: 1.0.0`), `package database`, and only the imports
   that file's moved methods actually need (run `goimports` or let `go build`
   tell you — do not copy the full original import block into every new
   file).
3. Move method bodies verbatim (cut from `pebble_store.go`, paste into the
   target file) — same signature, same receiver name, same body. Also move
   any unexported helper function or const that is used ONLY by methods in
   that domain (e.g. `isbnIndexKey`-style key-builder helpers, `metadataStateKey`).
   If a helper is shared across domains you're splitting into different
   files, leave it in `pebble_store.go` (or promote it to a small shared
   `pebble_store_keys.go` if there are several) rather than duplicating it.
4. Do the move as a **small number of large commits**, not 388 tiny ones —
   one commit per target file (or a few closely-related files together) is
   the right grain. After each commit, run the full test gate below before
   moving to the next file, so a break is bisectable to a single domain move.
5. After all domain files are extracted, confirm `pebble_store.go` itself has
   shrunk to roughly the "core/lifecycle" scope in the table above — if
   substantial method groups are still left over that don't fit the table,
   add one more domain file for them rather than leaving a second monolith.
6. Do **not** touch `store.go` (the `Store` interface definition) or any
   caller of these methods anywhere else in the repo — this task is a
   same-package file reorganization only; no interface, signature, or call
   site changes.
7. Bump the file header (version + `last-edited`) on `pebble_store.go` itself
   (it's still being edited) and on every new file you create.

## How to test

Run after every domain-file commit, and again at the end:

```bash
go build ./...
go vet ./...
go test ./internal/database/... -count=1
```

Also sanity-check that git still tracks history correctly for moved code
(this doesn't affect correctness but confirms you moved rather than
copy-pasted-and-left-behind):

```bash
git log --oneline --follow -- internal/database/pebble_store_authors.go | head -5
```

Full regression pass before opening the PR:

```bash
make ci
```

## Acceptance criteria

- [ ] `internal/database/pebble_store.go` line count has dropped substantially
      (target: under ~2,000 lines of core/lifecycle code; state the final
      count in the PR description).
- [ ] Every method that existed on `PebbleStore` before this change still
      exists, with the identical signature and receiver name, just in a
      different file (`grep -c -E "^func \([a-zA-Z]+ \*PebbleStore\)"` across
      all `internal/database/pebble_store*.go` files sums to the same total
      as the pre-change count on `pebble_store.go` alone).
- [ ] `go build ./...`, `go vet ./...`, and `go test ./internal/database/...`
      are green after the final commit.
- [ ] `make ci` passes.
- [ ] No caller outside `internal/database` needed to change (verify with
      `git diff origin/main --stat` — no non-`internal/database` files should
      appear, except possibly CHANGELOG.md/TODO.md).
- [ ] No logic, condition, or behavior was changed in any moved method — diffs
      should read as pure cut/paste plus import-list edits and file headers.
      Any bug noticed while moving code is logged as a follow-up, not fixed
      inline.
- [ ] File headers bumped on every touched/created file.
- [ ] CHANGELOG.md and TODO.md updated per repo convention (this is a
      structural change worth one line in each).

## Commit message

Use one commit per domain file (adjust the domain name per commit), e.g.:

```
refactor(database): split author/narrator methods out of pebble_store.go (SYS-5)

pebble_store.go was 11,398 lines — the largest file in the repo and the most
likely merge-conflict site across concurrent parallel-subagent waves per
CLAUDE.md's worktree discipline. Move author, narrator, and author-tag methods
into pebble_store_authors.go, following the existing pebble_store_<domain>.go
split pattern (book_aggregates, isbn_index, lsh, ops_v2). Pure move — no
signature or behavior change.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

And a final summary commit or PR description covering the whole set, e.g.:

```
refactor(database): finish splitting pebble_store.go into per-domain files (SYS-5)

Closes out the SYS-5 navigability/merge-conflict-hotspot finding from the
2026-07 storage-architecture consultancy review. pebble_store.go dropped from
11,398 lines to <final count> lines of core store lifecycle code; the
remaining ~370 methods now live in <N> pebble_store_<domain>.go files
following the pattern already established by pebble_store_book_aggregates.go
et al. No behavior, signature, or call-site changes.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-30-pebble-store-split
gh pr create --fill
gh pr merge <number> --rebase
```

Because this PR touches a very large fraction of `internal/database/`, expect
`gh pr merge --rebase` to require a fresh rebase if anything else landed on
`internal/database/pebble_store*.go` while this was in flight — this is
exactly the wave-6-runs-last precaution called out at the top of this brief.
If it conflicts, re-run `git rebase origin/main`, resolve by re-applying the
same cut/paste (never by re-deriving logic), and re-run the full test gate
before pushing again.

## Idempotency / Rollback

- If `internal/database/pebble_store.go` is already close to the "core"
  scope in the grouping table (roughly under 2,000 lines, with the domain
  method groups already split out), this task is done — verify with
  `wc -l internal/database/pebble_store.go` and
  `ls internal/database/pebble_store_*.go`.
- The dedup/embeddings portion of the original consultancy example file list
  does not apply — verified during Background research that
  dedup (`dedup_label.go`) and embeddings (`embedding_store.go`,
  `hnsw_embedding_store.go`, `chromem_embedding_store.go`) are already
  separate files untouched by `pebble_store.go`. Do not create empty
  `pebble_store_dedup.go` / `pebble_store_embeddings.go` files to satisfy the
  letter of the original ticket text — there is nothing to move.
- Rollback = revert the domain-file commits in reverse order (each commit is
  self-contained: it removes methods from `pebble_store.go` and adds a new
  file, so reverting restores the monolith exactly). Because this is a
  same-package move with no behavior change, a partial rollback (reverting
  only some domain-file commits) is also safe — the remaining split files and
  the shrunk `pebble_store.go` continue to compile together either way.
