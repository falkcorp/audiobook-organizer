### Fixed

#### Locked metadata fields are now actually locked against every fetch, apply and rescan

The edit dialog promises "Edited fields are automatically locked to prevent overwrites
from future fetches". Only one of the eight write paths kept that promise. Every
metafetch apply path — auto-fetch (`FetchMetadataForBook`, `FetchMetadataForBookByTitle`),
manual candidate apply, the `metadata.batch-apply-cached` op, transcription auto-match
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
