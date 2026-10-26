### Fixed

- Discarding a trashed book's listening progress no longer reports "progress was erased and the book was kept" when nothing had been erased; a refused delete with no progress cleared now reports the plain refusal.
- The nightly purge now refreshes cached book lists after a run whose only effect was moving listening progress to another copy before a refused delete, as "Purge now" already did.
- Corrected the bulk-delete executive summary: the no-permission-information refusal covers bulk edits only, not the separate delete and restore actions. Test fixtures from the review now use Gotham titles and paths.
- Repaired two test helpers that no longer compiled their fixtures against the current store: a retired version now carries its retire time, and the mock store answers the two newer retired-row lookups.
