### Fixed

#### Four writers put four different hashes in the one column dedup treats as certainty

`book_files.file_hash` is an identity column: `internal/dedup/collectors_exact.go`
emits a `SigExactFile` signal at **Confidence 1.0 — certainty** whenever two books
share a value in it. A confidence of 1.0 is only defensible if every row in the
column was produced by the same function, and it was not.

`internal/database/file_provenance.go` has specified the algorithm since it was
written: for a file over 100 MB, `SHA-256(first 10 MB ‖ last 10 MB ‖ decimal
size)`; a whole-file digest below that. `internal/scanner` and the
`backfill-file-hashes` maintenance job wrote exactly that. Three other writers
did not:

- `maintenance.extract-wav-clips` hashed the source with a plain whole-file
  SHA-256. Its guard — "write unless the stored hash already matches" — could
  never match above 100 MB, so the overwrite fired for **every** file in exactly
  the population the chunked strategy exists for. Its own `OperationDef`
  description promised the write happened only "(when missing)"; nothing in the
  body implemented that.
- `versions.CreateIngestVersion` hashed newly-ingested files with a whole-file
  SHA-256 and stored it on the `BookFile` row.
- The iTunes track importer stored `ComputeSegmentFileHash` — a SHA-256 of only
  the **first 1 MB**. That one is wrong in both directions: it never equals the
  scanner's value for any real audiobook, and two different tracks that share a
  1 MB opening collide on it, which asserts a duplicate at certainty.

The user-visible effect was silent under-detection. Two byte-identical files
hashed by two different algorithms produce two different strings, so the exact-file
collector never fired: the duplicate was simply never found, with no error and no
log line anywhere.

The algorithm now lives in one place, the new `internal/filehash` leaf package,
and every writer of the column calls `filehash.BookFileHash` (or
`BookFileHashFromFile` for the scanner's single-pass reader). Three hand-written
copies of the chunked algorithm — in `scanner.ComputeFileHash`,
`scanner.computeHashFromReader`, and inline in the plugin — are now one.
`extract-wav-clips` also implements the "when missing" guard its description
always claimed, so it reads file identity rather than owning it.

Two ambiguously-named whole-file hashers that fed the confusion are gone:
`fileops.ComputeFileHash` (zero non-test callers) and `versions.HashFile`.
`fileops.ComputeFileHashAndSize` remains the canonical whole-file digest, for
verifying bytes survived a mutation.

**Rows already written with the wrong algorithm are not repaired by this change.**
You cannot tell a whole-file digest from a chunked one by looking at it — both are
64 hex characters — so a repair has to recompute, and it needs its own design.

Consolidating the algorithm also surfaced three ways the same column could be
given a well-formed but wrong value, all now closed:

- A **truncated read** produced a digest that was not reproducible. The chunked
  path only runs on files over 100 MB, so a short read of a 10 MB window never
  meant "the file ended" — it meant the read was cut short, which happens on the
  NFS/SMB mounts a NAS-backed library lives on. The partial window was folded
  into the digest anyway, so hashing the same unchanged file twice could yield
  two different values. Windows are now filled, and a genuinely short one is an
  error instead of a hash.
- The scanner sized the file with a `stat` of the **path** taken before it opened
  the file, then folded that size into the digest of whatever the handle actually
  pointed at. `WriteTagsSafe` finishes with an atomic rename over that path and
  the organizer renames files under the library root, so the two could disagree.
  It now sizes the open descriptor.
- `BookFileHashFromFile` hashed from the caller's current file offset, which for
  its natural caller was already past the tag header. It now positions the handle
  itself.

#### A failed hash no longer orphans a newly-ingested version

`versions.CreateIngestVersion` created the `book_version` row, then set both the
file's hash and its link back to that version inside the same success branch. If
hashing failed — the file moved by a concurrent organize, a permissions or I/O
error — the link was skipped along with the hash, and the function returned
success. The version existed with nothing pointing at it. Linking is now
unconditional; only the hash is skipped, and it is left for the backfill job.

`maintenance.extract-wav-clips` and the iTunes importer also now count and log
the per-file hash failures they previously discarded, so a run that writes no
hashes at all can no longer report "0 failed". `extract-wav-clips` additionally
counts rows whose stored hash disagrees with the canonical digest — it already
recomputes that digest, so this sizes the repair population at no extra cost.
