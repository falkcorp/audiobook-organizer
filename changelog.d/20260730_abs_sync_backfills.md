<!-- file: changelog.d/20260730_abs_sync_backfills.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e08af99-a4da-425d-a8f3-0e1b3f58d550 -->
<!-- last-edited: 2026-07-30 -->

### Added

#### `backfill-sync-ids` — idempotent ABS identity backfill for the whole library

New maintenance job that mints a `sync_item` syncID for every Book and a `sync_file`
syncFileID for every BookFile that does not have one yet, in a single pass. Both
keyspaces already mint on first encounter at request time, so this is not required for
correctness — it exists so the entire library is identity-consistent *before* any
Audiobookshelf-compatible client connects: no first-request latency spike, and no window
where half the library has stable IDs and half does not.

Idempotent by construction rather than by checkpoint. `MintOrGetSyncID` and
`MintOrGetSyncFileID` are each independently idempotent, so re-running the job from book 0
after an interruption is both correct and cheap (an already-minted book or file is a single
point-get skip). `CanResume()` therefore returns `false` on purpose — there is no index
worth persisting, unlike `backfill_file_hashes.go`'s `resumeIndex` — and a regression test
snapshots every minted ID after run 1 and asserts run 2 produces a byte-identical set.

The per-book work runs through `registry.RunItems` with `Concurrency: runtime.NumCPU()`.
A bare `for range books` loop of exactly this shape is what took a `dedup.full-scan` run
silent for 3+ hours at 100% CPU on a single core on 2026-07-05, and
`RunItemsOptions.Concurrency` treats both 0 and 1 as sequential — so the value is a named
function a test asserts is `> 1`, guarding against a future edit silently reverting to the
default. Book IDs come from `ListBookIDs()`, never a paginated `GetAllBooksFrom` walk,
whose memdb fast path silently caps a page at 2× the requested limit and can therefore
miss books. `ErrMode: ErrModeCollect` keeps a handful of unreadable books from cancelling
the remaining tens of thousands.

#### `backfill-itunes-positions` — carry existing Apple Books listening positions across

New maintenance job that migrates each book's saved `Book.ITunesBookmark` into the
per-user progress store (`UserPosition` + `UserBookState`), so moving off iTunes/Apple
Books does not start from a blank slate. `ITunesBookmark` is **milliseconds** and a single
scalar per book representing the *farthest position read* — a whole-book resume offset,
not a per-track value and not a bookmark collection — so it is converted to float seconds
and placed on the cumulative timeline.

**Every write is routed through `progress.MergeIncoming`, never written bare.** That is
what makes §5's forward-only rule apply: a user who has already listened further on a real
device can never be rewound by stale iTunes data, while an iTunes position that *is*
further along still advances. Two extra safeguards back that up. The incoming timestamp is
derived from real data (`ITunesLastPlayed`, then `ITunesDateAdded`, then the book's own row
timestamps) and never `time.Now()` — a fresh stamp would win the newer-wins branch
outright and overwrite a device's newer position, and would also beat AudioBooth's own
truncated-seconds comparison client-side. It is then clamped to the server's existing
stamp, so when a stored record exists the merge can only ever accept via the forward-only
branch. The server-side position fed to the merge is `max(PositionSeconds)` across all
existing segment rows rather than the most recent one, because legacy rows are opaque
per-device segments that are not directly comparable to a whole-book scalar.

`isFinished` is derived with §5b's ≥2 s tolerance via `IsWithinFinishedTolerance`, against
**one** authoritative duration per book — the sum of track durations, the timeline clients
actually seek within, falling back to `Book.Duration`. The same book legitimately reports
three different durations (container `9975.480544`, last-chapter-end `9975.428000`,
track-sum `9975.431111` on the measured Odyssey fixture), and with a tight epsilon a fully
listened book never auto-marks finished and sits at 99% forever. `progress` is derived as a
0.0–1.0 fraction — exported as `ITunesPositionProgressFraction` so the future DTO layer
cannot re-derive it as a percentage — and an unfinished book is never rendered as 100%.
`lastUpdate` is stored as an exact integer ms epoch on `UserBookState.LastActivityAt`
(`UserPosition.UpdatedAt` cannot serve that role: the store stamps it `time.Now()`
unconditionally); an absent `updatedAt` would make the server permanently lose every
conflict, since clients compare it against their own wall clock.

A nil or non-positive bookmark is **skipped, never zeroed** — a spurious 0 reads as "start
over" to a client and would fabricate progress the user never had. A manual status override
(`StatusManual`) is preserved, and `TotalListenedSeconds` is left untouched, since a
position is not an amount of audio listened. Re-running is a true no-op when nothing
changed, and a state row that drifted from its stored position is repaired without the
position ever being touched, so a run interrupted between the two writes self-heals.
Per-book failures log the book ID and continue; the job reports exact
migrated / skipped / failed / no-duration counts rather than "done".

As a courtesy, one named bookmark (`Imported from Apple Books`) is dropped at the migrated
offset so the position is visible and jumpable, not just an invisible resume point. Exactly
one, and only on an accepted migration — the ABS named-bookmark keyspace is an independent
system clients populate themselves, and there is nothing in a single resume scalar to
synthesize a bookmark list from. It is best-effort: a bookmark-store failure is logged and
the book still counts as migrated, because the resume position is the requirement.

Which user the positions belong to is a genuinely ambiguous mapping — iTunes has no user
concept — so it is resolved explicitly and never guessed silently:
`ABS_ITUNES_POSITION_BACKFILL_USER_ID` if set (an ID matching no user is a hard error, not
a silent fallback), otherwise the only user, otherwise the earliest-created account with a
`WARN` naming the choice and the override.

**This job is invoke-only and must not be run before the ABS media-progress provider is
wired.** Maintenance jobs run solely via `POST /maintenance/jobs/:job_id` and there is no
cron path to this one, which is deliberate: AudioBooth's `MediaProgress.syncFromAPI`
*deletes* any local progress row absent from `/api/me`'s `mediaProgress` list, and that
provider is still nil. Real progress records existing server-side while `/api/me` reports
an empty list would make a connecting client destroy its own local progress.
