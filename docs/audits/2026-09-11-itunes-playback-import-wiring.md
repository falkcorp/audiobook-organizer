<!-- file: docs/audits/2026-09-11-itunes-playback-import-wiring.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3486c78b-3023-4cc2-806f-6e0ef5a2cf88 -->
<!-- last-edited: 2026-09-11 -->

# iTunes playback import wiring audit (PLAYBACK-IMPORT, TASK-185)

**Date:** 2026-09-11 · **HEAD audited:** `c2bbf551d` (origin/main) · **Type:** read-only
investigation. No code, config, or iTunes library data was changed.

**Source item:** `TODO.md`, the `**PLAYBACK-IMPORT**` entry ("Listened / in-progress status
is not coming across from iTunes (or from the files)..."). It asks for a report before any
change, and suspects an **unwired pipeline**.

All citations are `path:line` at the HEAD above, taken with `grep -n`. Line numbers drift,
so re-grep before acting on any of them.

---

## Headline finding: PositionSync is fully built and nothing ever calls it

The code that turns iTunes play data into the app's listened / in-progress status
already exists. It is complete, it is constructed on every start, and nothing invokes it.

| Piece | Where | State |
|---|---|---|
| `PositionSync.Sync()` (pull then push) | `internal/itunes/service/position_sync.go:76` | Implemented |
| `pullBookmarks()`: Bookmark → user position + recomputed state | `internal/itunes/service/position_sync.go:85-124` | Implemented |
| `pullBookmarks()`: PlayCount > 0 → status `finished` | `internal/itunes/service/position_sync.go:126-140` | Implemented |
| `pushPositions()`: app position → `ITunesBookmark` / PlayCount bump | `internal/itunes/service/position_sync.go:150-221` | Implemented (see push caveat below) |
| Instantiation: `svc.Positions = newPositionSync(deps.Store, svc.Batcher)` | `internal/itunes/service/service.go:121` | Runs at service construction |
| The op that should call it, `itunes.position-sync` | `internal/plugins/itunes/position_sync.go:16-35` | Registered, `LivenessManual`, **no schedule** |
| Its `Run`, `runPositionSync` | `internal/plugins/itunes/position_sync.go:37-41` | **Stub.** `// TODO: Implement iTunes position sync operation.` (L38), `// This should call p.svc.Positions.Sync().` (L39), `return errNotImplemented(...)` (L40) |

A repo-wide grep for `Positions.Sync` finds no call site. The only hits are the stub's own
TODO comment (`internal/plugins/itunes/position_sync.go:39`) and a comment in
`internal/plugins/itunes/plugin.go:79-80`, which says the implementation the stub should
call "exists and has never been wired to anything".

**The schedule history is documented in the code.**
`internal/plugins/itunes/position_sync.go:23-25` records that the op used to run on a
`*/10 * * * *` cron. It "burned a green no-op op-history row every 10 minutes" until the
schedule was removed on 2026-07-17. Its instruction is to restore the schedule once
position sync is implemented. `internal/plugins/itunes/stub.go:17-24` adds the rest: this
stub had no ID collision, so it "produced no evidence at all". After 2026-07-17 it could
still be triggered by hand and report success. `errNotImplemented`
(`internal/plugins/itunes/stub.go:26`) now makes it fail instead.

**The header comment in the service is stale.**
`internal/itunes/service/position_sync.go:20-21` says "The sync runs as a maintenance task
(`itunes_position_sync`) in the scheduler. It can also be triggered manually from the API."
Neither is true at HEAD. A grep for `itunes_position_sync` hits only that comment. No
maintenance job, scheduler entry, or HTTP route calls `Sync()`. Anyone who reads that
file will conclude the pipeline runs.

---

## Per-signal verdict

Each signal is assigned one of four buckets: **never parsed**, **parsed but never
written**, **written but never surfaced**, or **wired end to end**. Several signals fit a
bucket only for part of their path, so each gets a qualifier.

| Signal | Bucket | Qualifier |
|---|---|---|
| **PlayCount** | **Wired end to end** as a display field | The step from PlayCount to the status `finished` exists only in the uncalled `pullBookmarks` (`internal/itunes/service/position_sync.go:127-140`). |
| **PlayDate** (`ITunesLastPlayed`) | **Wired end to end** as a display field | It never feeds read status. The backfill job uses it only as a timestamp source (`internal/maintenance/jobs/backfill_itunes_positions.go:449`). |
| **Bookmark** | **Wired end to end** as a display field; parsed from **XML only** | It becomes a resume position only through the manually triggered `backfill-itunes-positions` job. Nothing on the import or sync path does it. The ITL binary parser never reads it. See the ITL subsection. |
| **Listened / in-progress status** (`UserBookState`) | **Surfaced, but no iTunes path writes it automatically** | The API and one UI chip exist. The only writers from iTunes data are the uncalled `PositionSync` and the hand-run backfill job. |
| **`Played` flag** (named in the TODO) | **Never parsed** | A grep for `Played` across `internal/itunes` (excluding `LastPlayed`/`PlayDate`) hits only playlist names (`internal/itunes/import.go:516-517`) and a comment. |

The details and citations for each follow.

### Q1: Does the importer read the play-count, `Played`, and bookmark fields?

**PlayCount, PlayDate, and Bookmark are read. The `Played` flag is not.**

`itunes.ParseLibrary` (`internal/itunes/parser.go:71-83`) checks the file's first four bytes. If
they are `hdfm`, it parses the ITL binary format through `ParseITLAsLibrary`. Otherwise it
parses the XML plist.

- **XML plist path:** `internal/itunes/plist_parser.go:49` `PlayCount` (`plist:"Play Count"`),
  `:50` `PlayDate` (`plist:"Play Date"`), `:51` `PlayDateUTC`, `:53` `Bookmark`
  (`plist:"Bookmark"`, milliseconds), and `:54` `Bookmarkable`. These are copied into
  `Track` at `:116-120` and `:179-183`. The streaming parser sets them at `:538`, `:544`,
  and `:554-560`.
- **ITL binary path:** the `ITLTrack` struct (`internal/itunes/itl.go:70-86`) carries
  `PlayCount` (`:81`) and `LastPlayDate` (`:85`) and has **no Bookmark field**. The readers
  decode PlayCount at `internal/itunes/itl_be.go:203` and `internal/itunes/itl_le.go:252`,
  and LastPlayDate at `itl_be.go:206` and `itl_le.go:255`. `ParseITLAsLibrary`
  (`internal/itunes/itl_convert.go:38-63`) copies PlayCount and converts LastPlayDate into
  `PlayDate` (`:61-62`). **It never sets `Bookmark`.** It hardcodes `Bookmarkable: true`
  (`:51`).

### Q2: Is there a write path onto the book rows, and is readstatus or PositionSync called on import?

**The parsed values are written onto `Book` columns. They are not written into the
per-user read-status store.**

The live writer is `internal/itunes/service/importer.go`:

- `Importer.Sync` (`importer.go:835`, parses at `:839`): the update branch writes PlayCount
  (`:942-946`), Rating (`:948-952`), Bookmark (`:954-958`), and LastPlayed (`:960-966`). It
  then loads the full row and calls `UpdateBook` (`:975-984`).
- `buildBookFromAlbumGroup` (`importer.go:1933`) is the new-book path. It sets
  `ITunesPlayCount` (`:2020`) and `ITunesBookmark` (`:2022`), and sets `ITunesLastPlayed`
  from PlayDate (`:2029-2031`).
- `linkITunesMetadata` (`importer.go:1890`, called from `:489` and `:500`) links an import
  to an existing book. It fills PlayCount (`:1896-1897`) and Bookmark (`:1904-1905`) only
  when those fields are empty.

The brief also cites `internal/itunes/import.go:395-404`. **That code is on a dead path.**
`ConvertTrack` (`internal/itunes/import.go:352`) is called only from
`internal/itunes/itunes_test.go` and `internal/itunes/integration_test.go`. No
production code calls it.

Neither `readstatus` nor `PositionSync` is called on import. Every call to
`readstatus.RecomputeUserBookState` or `readstatus.SetManualStatus` from iTunes data goes
through `PositionSync.pullBookmarks`, at `internal/itunes/service/position_sync.go:120`
and `:135`, which never runs. The import path writes `Book.ITunes*` columns and nothing else.

**Neither iTunes op is scheduled.** The server-side `itunes.sync` op
(`internal/server/itunes_ops.go:88`, `LivenessManual`) has no `Schedule`. A grep for
`Schedule` in `internal/server/itunes_ops.go` finds nothing. In `internal/plugins/itunes/*.go`
it finds only the three "no schedule" comments (`path_reconcile.go:17`,
`position_sync.go:23`, `sync.go:24`). So even the Book-column copy is refreshed only when
someone triggers a sync or import by hand.

**The only other writer into the per-user store** is the maintenance job
`backfill-itunes-positions` (`internal/maintenance/jobs/backfill_itunes_positions.go`,
registered at `:25`, ID at `:153`). It turns `Book.ITunesBookmark` into a
`UserPosition` (`:342`) and a `UserBookState` (`:346`). `desiredState` (`:484-515`) sets
`in_progress`, or `finished` when the merged progress says so. The job also runs its work
in parallel through `registry.RunItems` (`:253`). However:

- nothing schedules it. A grep for its ID outside its own file finds no hits, so it runs
  only when triggered by hand through the maintenance dispatcher
  (`internal/server/maintenance_dispatcher.go:82`).
- it never reads `ITunesPlayCount`. A book that was finished in iTunes and has no bookmark
  stays unstarted.

### Q3: Does the audio file itself carry progress, and is it read on scan?

**No read of progress embedded in the file was found.** A grep for
`bookmark|playcount|play_count|position` in `internal/scanner/*.go` hits only
series-position code (for example `scanner.go:1307-1327`) and scan-resume checkpoints
(`service.go:82-129`). A grep for `bookmark` in `internal/metadata` and `internal/mediainfo`
finds nothing.

The scanner's only contact with these fields is `preserveExistingFields`
(`internal/scanner/scanner.go:3234`). On a rescan it copies an existing `ITunesPlayCount`
(`:3291-3292`), `ITunesLastPlayed` (`:3294-3295`), or `ITunesBookmark` (`:3300-3301`)
when the scanned row has none. It keeps an old value and never reads a new one from the file.

### Q4: Does the API or UI surface it?

**The API exists. The UI renders the raw iTunes fields and the status chip on the book
detail page only.**

- **API:** routes are at `internal/server/wire_library_routes.go:76-81`:
  `POST`/`GET /books/:id/position`, `GET /books/:id/state`, `PATCH`/`DELETE /books/:id/status`,
  and `GET /me/:status`. Handlers are in `internal/server/handlers/reading.go`: `SetPosition`
  `:73`, `GetPosition` `:99`, `GetBookState` `:115`, `SetBookStatus` `:131`,
  `ClearBookStatus` `:162`, and `ListByStatus` `:178`.
- **ABS clients:** the ABS handlers read the same per-user store. See
  `internal/server/handlers/abs/item.go:81,86`, `play.go:205,458,525`, and
  `browse.go:683,693`. A position or state row written by the backfill job reaches ABS clients.
- **Web UI, read status:** `web/src/services/readingApi.ts` wraps all six endpoints. Only
  `getBookState` and `setBookStatus` have a caller: `ReadStatusChip`
  (`web/src/components/audiobooks/ReadStatusChip.tsx:40,62`). That component is rendered in one
  place, `web/src/components/bookdetail/BookDetailHeader.tsx:173`.
  **`listByStatus`, `getBookPosition`, and `setBookPosition` have no caller outside
  `readingApi.ts`.** No page lists books by status and no page shows a position.
- **Web UI, raw iTunes fields:** `web/src/components/bookdetail/BookDetailFilesTab.tsx`
  renders `itunes_play_count` (`:259-269`), `itunes_last_played` (`:272-283`), and
  `itunes_bookmark` as a duration (`:303-314`). These fields are typed in
  `web/src/services/api.ts:153-156`.

This explains the owner's report. A book finished in iTunes shows its play count on the
Files tab. The status chip beside the title reads from `UserBookState`, and nothing
fills that store from iTunes automatically, so the chip shows the book as unstarted.

---

## Additional code-read findings (not measured in prod)

### ITL-sourced sync writes Bookmark = 0 over a stored bookmark

This is an inference from reading the code. It was not measured, and prod was not contacted.

1. `ParseLibrary` chooses the ITL parser when the file starts with `hdfm`
   (`internal/itunes/parser.go:81-83`).
2. The ITL path never sets `Track.Bookmark` (`internal/itunes/itl.go:70-86` has no field,
   and `internal/itunes/itl_convert.go:38-63` never assigns it), so it stays 0.
3. `Importer.Sync` builds `newBookmark := new(firstTrack.Bookmark)` and writes it whenever it
   differs from the stored value (`internal/itunes/service/importer.go:954-958`). There is no
   `> 0` guard like the one PlayDate has at `:960`.

**If the configured library file is ITL binary**, a sync writes `ITunesBookmark = 0` over any
bookmark stored earlier from an XML import. New books built from an ITL library get
`ITunesBookmark = 0` (`importer.go:2022`). The XML path is unaffected. This pass did not
determine which format the configured library path uses. This interacts with the future fix
below: `pullBookmarks` skips `ITunesBookmark <= 0` (`position_sync.go:94`), so an
ITL-sourced library would seed no positions even once the op is wired.

### The push direction cannot write Bookmark or PlayCount into the ITL file

`pushPositions` updates `Book.ITunesBookmark` and `ITunesPlayCount` and calls
`enqueuer.Enqueue(book.ID)` (`internal/itunes/service/position_sync.go:188-213`). The header
comment (`:15-18`) says the batcher then writes Bookmark, Play Count, and Played Date to the
ITL. At HEAD, the write-back path has no reference to these fields: a case-insensitive grep
for `bookmark|playcount|lastplay|play_count` over
`internal/itunes/service/writeback_batcher.go`, `internal/itunes/itl_combined_mutate.go`, and
`internal/itunes/itl_le_metadata_update.go` returns zero hits. `ITLOperationSet`
(`internal/itunes/itl_combined_mutate.go:19-24`) carries only Removes, Adds, LocationUpdates,
and MetadataUpdates. `ITLMetadataUpdate` (`internal/itunes/itl_le_metadata_update.go:19`)
carries name, album, artist, genre, kind, composer, and location fields. Wiring the op would
make the **pull** direction work. The push would update app-side columns only until the
write-back grows these fields.

### Carried-forward context (not re-verified as defects in this pass)

- `readstatus` discards read errors in two places:
  `existing, _ := store.GetUserBookState(...)` at `internal/readstatus/readstatus.go:70`
  (in `RecomputeUserBookState`) and `:153` (in `SetManualStatus`). The source item cites
  `:144`. Both current sites are listed here.
- The source item mentions a stale status-index leak in
  `internal/database/pebble_store_playback.go`. It was not re-examined here.

---

## Future fix, gated and out of scope for this task

The fix this report points to:

1. In `internal/plugins/itunes/position_sync.go:37-41`, replace the stub body of
   `runPositionSync` with a call to `p.svc.Positions.Sync()`, reporting `pulled` and `pushed`
   through the reporter. Do not write a parallel helper. The implementation is at
   `internal/itunes/service/position_sync.go:76`.
2. Restore the cron schedule on `positionSyncDef`, as the comment at
   `internal/plugins/itunes/position_sync.go:23-25` directs.
3. Correct the stale header claim at `internal/itunes/service/position_sync.go:20-21`.

**This fix is gated on the silent-failure work (Wave 5).** `pullBookmarks` discards errors on
the reads that decide whether to write:

- `existing, _ := p.store.GetUserPosition(...)` (`position_sync.go:98`)
- `files, _ := p.store.GetBookFiles(...)` (`:104`)
- `state, _ := p.store.GetUserBookState(...)` (`:131`)

A failed read therefore looks the same as "no prior state". Once the op runs on a schedule,
a transient read failure would let it seed a position or force `finished` over the user's
real playback state. `SetManualStatus` also sets `StatusManual=true`, so that status would
then be protected from later recomputes. Those error discards must be fixed first. The
ITL-bookmark-zero issue and the push-direction gap above should also be decided before the
schedule is restored.

A related decision that does not depend on the gate: whether `backfill-itunes-positions`
should also seed `finished` from `ITunesPlayCount`, since it is today's only working writer
into the per-user store.

No repair, backfill, or stub implementation was done in this task.
