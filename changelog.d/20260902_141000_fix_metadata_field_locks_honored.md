### Fixed

#### Locked metadata fields are now actually locked against every automated write path

The edit dialog promises "Edited fields are automatically locked to prevent overwrites
from future fetches". Only one of the eight write paths then known kept that promise —
and that count of eight was itself short (see below). Every metafetch apply path —
auto-fetch (`FetchMetadataForBook`, `FetchMetadataForBookByTitle`), manual candidate
apply, the `metadata.batch-apply-cached` op, transcription auto-match
and the metadata upgrade job — wrote straight over a user-locked column, because
`ApplyMetadataToBook`/`ApplyMetadataCandidate` never read `MetadataFieldState`. The
provenance panel layered the override back on read, so the UI showed the user's value
while every list view, search index, write-back tag and organize path used the
overwritten one. Separately, the scanner's rescan guard consulted the keys `author`,
`series` and `series_sequence` — keys nothing ever wrote (the writers store
`author_name`, `series_name`, `series_position`) — so a rescan clobbered every curated
author, series and position while its own test, which locked the guard's keys rather
than the writer's, passed.

There is now ONE vocabulary, `database.UserLockableFields` (13 keys, each tied to the
`Book` column it protects), and ONE guard, `database.LockedUserFields`, which reads the
per-field rows and falls back read-only to the pre-migration user-preference blob so a
lock set before the rows existed still holds. Both metafetch apply functions funnel
through it, so every caller inherits the check; the scanner guard and the bulk-fetch
handler's `shouldApply` are rewired onto the same constants, and the drifted private
key lists are deleted. Locked fields are reported, not silently dropped:
`FetchMetadataResponse.SkippedLockedFields` names them, and the batch-apply op counts
and logs books that kept a locked field in its completion summary. The guard fails
CLOSED — a lock read error makes the apply return `ErrFieldLocksUnavailable` and write
nothing, and the scanner treats every lockable column as locked for that book.

Conformance tests iterate the writer's key list end to end: each key is in the
vocabulary, each is blocked in both apply functions against a fixture that provably
changes when unlocked, and the scanner test spells the writer's keys as literals.
Four deliberate regressions (guard bypassed, one strip case deleted, skipped list
discarded, fail-open on error) were each caught.

A second pass enumerated every `UpdateBook` in the codebase instead of trusting the
first pass's count of eight, and found twenty write paths, seven of them still
consulting no lock at all. All seven now do: the ISBN/ASIN enrichment metafetch queues
from inside its own guarded apply (so every guarded apply was scheduling an unguarded
one for the same book), the scanner's AI-nomination apply,
the diagnostics AI-suggestion apply, the iTunes reconcile merge, the dedup book merge,
the dedup split-book and series merges, and undo/revert. Undo and revert are not
silently skipped: restoring a pre-fetch value over a newer user edit is the exact
overwrite the lock exists to stop, so those fields are left as the user set them and
the operation reports which ones, rather than reporting a completed revert that was
partial.

Three paths write on the user's own behalf and so are exempt from the guard, but were
recording no lock row — meaning the edit they just made was unprotected against the
very next fetch. Bulk edits, batch operations and a merge's `CombineOverride` now
record one per edited field (resolving `author_id`/`series_id` to the name the
vocabulary locks), and a failure to record the lock fails that item rather than
reporting a protected edit that isn't. The single-book edit path was projecting only
9 of the 13 lockable fields; `asin`, `genre`, `description` and `series_position` are
now projected and locked, `series_position` gained the top-level payload key it never
had, and a conformance test now fails in both directions if the vocabulary and the
extractors drift apart again. `syncMetadataToLibraryCopy` honours the library copy's
own locks — it is a separate book row with its own locks, and was a back door into a
book whose locks had just refused the same values.

Retiring the pre-migration blob closed an inert-unlock bug: nothing ever deleted it, so
unlocking every field deleted the rows, the next read fell through to the blob, and the
field came back locked. Preferences are now deleted rather than overwritten with an
empty string, which left a tombstone indistinguishable from a value the user set blank.

Still unguarded, and tracked separately: roughly seventeen maintenance and regroup
repair ops (title repair, junk-title repair, series denumbering, author conjunction
repair and their siblings) write `Book` columns without consulting a lock. The
interface plumbing they need is already in place.
