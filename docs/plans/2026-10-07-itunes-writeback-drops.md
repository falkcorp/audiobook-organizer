<!-- file: docs/plans/2026-10-07-itunes-writeback-drops.md -->
<!-- version: 1.2.0 -->
<!-- guid: 0e2814b6-f75b-4376-803f-8aa1903a0f21 -->
<!-- last-edited: 2026-10-07 -->

# Plan: the iTunes write-back drops every batch

Branch `fix/itunes-writeback-drops`, worktree `aorg-itunes-writeback`, based on
origin/main `0135c2308`. The owner approved "Fix now"; the plan was pre-approved,
so work started at once. Each decision below has its WHY so the owner can check it
afterwards.

## Evidence (read-only, 2026-10-07)

- Prod config: `itunes.library_write_path` = `.itunes-writeback/iTunes Library.itl`,
  `write_back_enabled=true`, `auto_write_back=true`, `write_back_dry_run=false`.
- iTunes last wrote that library on 2026-07-28. About 46,392 of its track
  locations sit under `W:/audiobook-organizer/.itunes-writeback/iTunes Media/`,
  which is where that library keeps its own media.
- Prod journal (retention starts 2026-09-23 01:52): **662** lines
  `iTunes write-back DROPPING batch after repeated failures`, **0** lines
  `operations applied and validated`. The 662 batches held **4,293** book-update
  entries (repeats across batches included), **1** remove (2026-10-06 22:19:27)
  and **0** adds. By day: 09-23 57, 09-24 24, 09-25 1, 09-26 21, 09-27 64,
  09-30 115, 10-02 92, 10-03 64, 10-04 18, 10-05 26, 10-06 6, 10-07 174.
- Every rejection today fails **only** the `location-form` guard. The verdict
  trailer names every failed guard: all 11 drop and retry lines since 11:00 read
  `... and 46,3xx more violation(s) across guards [location-form]`, which is about
  46,367 to 46,392 violations each. The message is `0x0B contains staging marker
  '.itunes-writeback/'`. The K13 identity and K14 magnitude guards passed.
- The `attempts=` values on the 662 drop lines sum to 30,420, about 46 per drop,
  and only 66 lines say "re-enqueued for retry". The failure counter is shared by
  all batches and is reset only by a success. After the first three failures,
  every later batch was dropped on its first failure with no retry at all.
- `.itunes-writeback/` holds exactly 5 `iTunes Library.itl.bak-2026-10-07T…`
  files. The phase-1 notes counted 6, starting at 11:20; that oldest one is gone,
  so keep-5 rotation works. Every one of the 5 is a byte-identical copy of the
  unchanged 07-28 library (all 32,847,402 B), made before a write that was then
  rejected.

## Root causes

1. **Strict location guard on the AO library's own media.** The batcher writes
   through `itunesservice.SafeWriteITL`. That calls
   `itunes.ApplyITLOperations(itl, itl.tmp, ops)` with no contract config, and
   then `itunes.AuditITL` (step 4b), which hard-codes `DefaultContractConfig()`.
   Neither sets `AllowedWritebackRoot`, so the `location-form` guard treats each
   of the library's 46k legitimate `.itunes-writeback/` locations as a
   staging-dir leak and rejects every write. Only `RunRelocateSyncCycle`
   (cmd/pid-census) ever passed the root.
2. **Drop after three failures.** `drainFlush` returned without re-enqueueing once
   `flushFailures >= 3`, logging the counts but not the book ids or PIDs. With the
   counter shared and never reset, this became "drop on the first failure".
3. **Tombstone before write.** `EnqueueRemove` marked the external id removed in
   a goroutine at enqueue time, before any write. A dropped, refused or dry-run
   remove left the DB saying "removed" while the track stayed in iTunes.
4. **Every other early return after the snapshot lost the batch too:** the
   `MaxRemovesPerFlush` refusal, a nil store, write-back not configured, the
   final drain at Stop, and any restart (the queue was memory-only).
5. **Playlist length (itl_le_repair.go).** After splicing out playlist `mtph`
   items, `RepairITLDropDanglingMtphLE` looked up the type-2 msdh in the
   shortened buffer. Its stale `totalLen` then overran the buffer, so
   `findMsdhByType` returned -1 and the length was never decremented. The
   `container-tiling` guard rejects the result (fail-safe), but any removal that
   cuts more playlist bytes than the containers after the playlist list (about 27
   entries on the real libraries) could never be written.
6. **Backups before rejected attempts.** The service wrapper copied the library
   to `.bak-<ts>` before it knew whether the write would pass. A run of rejected
   attempts rotated real history out and replaced it with identical copies.

## Decisions

- **D1. Derive the root from the write target.** `itunes.WritebackRootForLibrary(itlPath)`
  returns `<parent>/.itunes-writeback/` (for prod, `audiobook-organizer/.itunes-writeback/`)
  only when the `.itl` sits directly in a directory named `.itunes-writeback`, and
  `""` (strict) otherwise.
  WHY: there is no separate "write-back root" config key. The configured
  `library_write_path` is the AO library, and its directory is that library's
  media root. This is the same fragment cmd/pid-census uses by default and that
  the contract's doc comment names. A path under books/itunes can never produce
  a root, so the Original library stays strict.
  Limitation: the guard matches by substring. A parent directory whose name
  URL-escapes (spaces, `%`) would not match the 0x0B `file://` form. Prod's
  `audiobook-organizer` does not escape.
- **D2. Apply it in the `itunes` package, not at each caller.**
  `SafeWriteITL`, `ApplyITLOperations`, `ApplyITLOperationsInMemory`,
  `safeWriteOrEncodeToFile` (UpdateITLLocations, InsertITLTracks,
  InsertITLPlaylist and friends) and the rebuild path now fill an empty
  `AllowedWritebackRoot` from the input library's path. The service wrapper also
  passes it explicitly, and step 4b now audits with the same config
  (`AuditITLWithConfig`).
  WHY: every production writer (batcher, `/itunes/relocate`, `/itunes/rebuild`,
  `/itunes/write-back`, `/itunes/write-back-all`, the importer's deferred
  updates) writes `library_write_path`. They all had the same omission, and
  fixing it per caller leaves the next caller broken. An explicit non-empty root
  from a caller still wins.
- **D3. Durable queue in the store's raw KV.** Each pending item is its own key
  (`itunes_writeback:q:book:<id>`, `:remove:<pid>`, `:add:<pid>`), written at
  enqueue time and deleted only after a verified successful write. The queue is
  reloaded once in the batcher's `Start`, which `Container.Start` calls during
  `Server.Start`. It is not reloaded in the constructor, because test servers
  build the batcher on strict mockery stores that panic on unexpected calls.
  WHY: restarts are frequent in prod, so memory-only state loses work. One key
  per item keeps each enqueue a small write. A single JSON blob would be
  rewritten whole on every enqueue and grow without limit during an outage.
  `RawKVStore` is already on `database.Store`, so no new store methods or mocks
  were needed. When the store is nil (old test fixtures), the queue stays
  memory-only.
- **D4. Never drop; back off.** A failed flush puts the whole batch back and waits
  1 min, 2, 4, … capped at 1 h before the next attempt. The attempt counter
  belongs to the queue and resets on success. Each failure logs at ERROR with
  the attempt number, the next retry time and the pending counts.
  WHY: the 3-strike drop was the data loss. The backoff keeps a persistent
  rejection from hot-looping a 30 MB re-encode, and the cap keeps a fixed fault
  retried at least hourly.
- **D5. Remove-cap refusal becomes a hold.** If a flush holds more than
  `MaxRemovesPerFlush` (50) removes, the removes move to a held list
  (`itunes_writeback:held:remove:<pid>`). They are never applied automatically.
  Adds and updates in the same batch go on as normal. The owner releases held
  removes through `POST /api/v1/itunes/writeback/held/release`, up to 50 per
  call.
  WHY: the cap is a circuit breaker against a runaway bulk remove, so
  auto-retrying it would defeat it. Dropping it was data loss, and blocking
  every metadata update behind it was needless.
- **D6. Tombstone only after the write lands.** `MarkExternalIDRemoved` runs for
  each PID in the written set after `SafeWriteITL` succeeds, never at enqueue.
  This also stops dry-run mode from tombstoning PIDs it never wrote.
- **D7. Visibility.** `GET /api/v1/itunes/writeback/status` returns pending
  counts, held removes, consecutive failures, the last error, the last failure
  and last success times, and the next retry. Failures log at ERROR.
  WHY: the brief asked for an API or op status. A web panel is a follow-up, so
  web was not touched.
- **D8. Back up only before a write that will land.** The service wrapper now
  takes its `.bak-<ts>` after the temp file has passed validation and the audit,
  just before the rename.
  WHY: a rejected attempt changes nothing, so it needs no rollback anchor. The
  backup before each attempt was what churned real history out of the keep-5
  rotation. Rotation itself was already bounded (5 in the service,
  `defaultBackupRetention` in `itunes.SafeWriteITL`, `.bak-lkg` and hand-named
  snapshots exempt), so no new limit was added. A test pins that rotation.
- **D10. Write-back off, or dry-run, keeps the batch without a timer.** The
  batch stays queued and durable but is not re-armed. `UpdateConfig` re-arms it
  when write-back is switched on, and so does any new enqueue.
  WHY: retrying on a timer while writes are switched off would only spin.
  Consuming the batch, as before, was the loss. The queue's size is bounded by
  the number of books and PIDs, because keys are per id.
- **D11. A book whose lookup fails stays queued.** A `GetBookByID` store error
  splits that book out of the batch and keeps it with backoff, while the rest is
  written. A nil book (deleted) is still skipped.
  WHY: a store error is not "nothing to write"; it is unknown.
- **D12. A restart gets a prompt attempt.** The failure count and last error are
  restored from the store, but the retry time is not.
  WHY: a restart is usually a deploy that changes code or config, as this one
  does. If the write still fails, the backoff resumes from the restored count.
- **D13. Store I/O stays outside the batcher mutex.**
  `PebbleStore.SetRaw` writes with `pebble.Sync`, which is one fsync per call.
  - Enqueue marks an item pending under `b.mu` and persists it after release.
  - Adds are persisted before they are queued, so a stale add key can never
    re-insert a written track.
  - `completeBatch` deletes outside the lock, then rewrites the key of anything
    re-enqueued meanwhile.
  WHY: library-wide ops enqueue thousands of books from worker pools. One fsync
  per book under one mutex would serialize all of them. The worst race leaves a
  stale book or remove key, and its reload is a diffed no-op.
- **D14. `Server.Start` also calls the batcher's `Start`.** It is idempotent.
  WHY: `Container.Start` calls it when the container built the service. The
  explicit call keeps a reload from depending on that wiring.
- **D9. Playlist fix.** Look up the type-2 msdh in the pre-splice buffer, map its
  offset through the same translation as the `miph` offsets, and subtract only
  the bytes removed inside its span.

## Dropped updates

- The drop log carried counts, not book ids or PIDs, so the 4,293 dropped book
  updates and the one remove **cannot be recovered from the logs**. Journal
  retention starts 09-23, and the library had been rejecting writes since it
  became the target (iTunes last wrote it 07-28), so losses before 09-23 cannot
  even be counted.
- Regenerate them as follows:
  1. Deploy this fix.
  2. `POST /api/v1/itunes/rebuild?dry_run=true` previews the full DB-vs-ITL diff
     (metadata, location, adds, removes).
  3. Re-enqueue the changed books through the fixed batcher. The batcher diffs
     before it writes, so unchanged tracks cost nothing. The owner closes iTunes
     on Windows first.
  4. The contract's 20% mhoh-rewrite cap may reject a very large delta. The
     queue now keeps it and shows it in `/itunes/writeback/status`, so the owner
     can split it.
  5. Never run the rebuild's remove set without reviewing it. That is the bulk
     remove-from-DB shape v5 removed.
- The one dropped remove (2026-10-06 22:19:27) came from op
  `01M4A2F6SMCBENB3R9WG0EFCPT` (dedup.book-merge, 22:18:22). That op merged loser
  `01KXXVAF46CM0NWZMSA44X1XTB` into `01KNDBXRC0N6WPTVZ6RHPSF2HH` and logged
  "merge queued ITL removals for loser count=1". The flush at 22:18:35
  (`removes=1`) was dropped on its first failure. The loser's PID was tombstoned
  at enqueue (root cause 3), so the DB says removed while the track is most
  likely still in iTunes. Its PID is on the loser's external-id rows (GET
  `/audiobooks/01KXXVAF46CM0NWZMSA44X1XTB/external-ids`); re-enqueueing that one
  remove is the owner's call.

## Test strategy

- itunes: an end-to-end synthetic `.itl` built in-test with
  `audiobook-organizer/.itunes-writeback/` locations. Strict write fails; a
  write to `<tmp>/audiobook-organizer/.itunes-writeback/iTunes Library.itl`
  passes with no explicit config. The same file under a non-writeback directory
  stays strict.
- itunes: playlist regression. A synthetic payload with a trailing msdh of known
  size, with removals below and above the threshold. Assert the type-2
  `totalLen` equals its real span and that `container-tiling` passes. Confirmed
  failing before the fix.
- service: the root reaches `ApplyITLOperations` and the audit; a remove stays
  pending and untombstoned when the write fails; a batch is never dropped after
  many failures; backoff grows and caps; the queue survives a restart; the cap
  holds removes; backup is skipped on a rejected write; rotation keeps named
  snapshots.
- `go build ./...`, `go vet`, `go test -race -short` on `internal/itunes/...`
  and every touched package.

## Rollback

- Revert the commit. The queue keys are inert to older binaries: an old binary
  ignores the `itunes_writeback:` prefix and behaves as before.
- Nothing in this change writes to any `.itl` or `.xml` on the server. The first
  real write happens only after deploy, when the batcher flushes.

## Post-deploy risk (owner)

- Deploying this turns write-back on for the first time since July. Close
  iTunes on Windows first. As an option, deploy with `write_back_dry_run=true`,
  check `/itunes/writeback/status` and the dry-run log, then turn dry-run off.
  The queue is kept in dry-run, so nothing is lost.
- Head-of-line blocking: a batch that is always rejected now blocks every later
  write until an owner acts (follow-up ITWB-1 in todo.d).

- The rejection trailer shows `location-form` as the only failed guard, so with
  the root set the contract should pass. A real-library dry run of the fixed
  binary was not possible from this session, because copying the prod `.itl`
  off the server was denied. If the first flush is rejected on identity (the
  sidecar is dated 07-25, the library 07-28), the owner can run
  `POST /api/v1/itunes/adopt-base`.
- Close iTunes on Windows before the first flush. The in-use check only sees
  recent file activity.
