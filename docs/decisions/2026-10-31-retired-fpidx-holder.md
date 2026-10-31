<!-- file: docs/decisions/2026-10-31-retired-fpidx-holder.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3c9e2d71-6b4a-4f08-a5d3-8e1f70c4b296 -->
<!-- last-edited: 2026-10-31 -->

# Retired rows: the `fpidx:` value follows the owner

## Findings

- Every `fpidx:<band><subprint>:<fileID>` row stores the owning BookID (`writeFingerprintLSHIndexes`). `RetireBookFiles`, `UnretireBookFiles` and `RehomeRetiredVersion` re-key the book_file row but wrote nothing to `fpidx:`, so the value kept naming the book the row left.
- No code reads that value. `LSHProbe` and `LookupAcoustIDCandidates` take the file id from the key (offset 16) and callers resolve the row by file id. The staleness was therefore latent stored-data wrongness, not a wrong answer any reader gave today. The brief's premise ("an `LSHProbe` hit reports the shell") is true of the stored value, not of the probe result, which is a file id.
- Re-home had the same bug (the holder changes, the value would name the old holder), so it is fixed with retire and un-retire.

## Decision

Rewrite the values in the same batch that re-keys the row (`stageLSHOwnerRewrite` in `retired_book_files.go`), at all three writers:

- The member list comes from `fpidx_meta:<fileID>`, the side table `DeleteLSHEntries` already uses, so the fingerprint is not re-derived. A row with no meta row (no fingerprint, a legacy-era print, index not built) costs one point read and writes nothing.
- The meta row is read from committed state and watched by the existing `indexCAS` (`watch`, new): `verify` runs under the owner stripes and the single-owner index mutex and fails the attempt (`errBookFileRowVanished`, re-stage) if another writer created, replaced or removed it between the read and the commit. The file stripes are already held (`lockBookFiles`), and a fingerprint change rewrites the row bytes, which `bookFileUnchanged` already compares.
- `PutLSHEntries` (called by `dedup.lsh-index-build` with the book id it read earlier) now takes the file's write stripe and takes the owner from the `book_file_id:` index when the file has a row, so a build that loaded a file before it was retired cannot write the shell back. A file without a row keeps the caller's id.

## Rejected

- **Resolve the owner through the file id in the readers** (the brief's fallback). Batch rewrite is possible, so the fallback was not needed; it would also leave a stored value that is wrong for any future reader.
- **Stop storing a book id in `fpidx:`** (store an empty value like `fpwinidx:`). Cleanest in the long run since nothing reads it, but it changes the documented key format (`lsh_index_v1`, "normative — do not change without bumping") and needs a full rebuild; not what was asked.
- **A rewrite after commit.** Two writes, a window where the value is stale, no CAS. Not "never stale".

## Existing prod data

Retired rows created before this change keep the shell id in their `fpidx:` values. Impact is nil today (nothing reads the value). `dedup.lsh-index-build` does **not** repair them: it skips any file whose `fpidx_meta:` row exists (`HasLSHIndex`). A one-off rewrite of the values of every retired row with a meta row to its `BookID` would fix them; it is not trivial to ship as an op (needs a registered op, a capability, a scan over retired rows), so it is filed as `TODO-RETIRED-FPIDX-PROD-REPAIR` in `todo.d/` rather than written here. Whether retired rows exist in prod was not checked from this worktree.

## Out of scope, noted

Other paths that change a row's `BookID` (non-retired moves in `bookfile_merge.go`, `MoveBookFiles`-style writers) were not audited for the same stale `fpidx:` value.
