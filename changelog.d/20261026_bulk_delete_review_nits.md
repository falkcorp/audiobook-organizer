### Fixed

- Discarding a trashed book's listening progress no longer reports "progress was erased and the book was kept" when nothing had been erased; a refused delete with no progress cleared now reports the plain refusal.
- The nightly purge now refreshes cached book lists after a run whose only effect was moving listening progress to another copy before a refused delete, as "Purge now" already did.
- Bulk requests without permission information now fail closed for the delete, permanent delete and restore actions as well as for edits: the delete actions need library.delete and a restore of a trashed book needs library.organize. `merge.RestoreFromTrash` takes a judge that runs inside its write on the locked row, so a restore payload that also sets the trash label is judged in both directions. Test fixtures from the review now use Gotham titles and paths.
- Repaired two test helpers that no longer compiled their fixtures against the current store: a retired version now carries its retire time, and the mock store answers the two newer retired-row lookups.
