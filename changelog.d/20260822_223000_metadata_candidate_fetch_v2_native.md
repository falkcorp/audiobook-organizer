### Fixed

- **Interrupted metadata candidate fetches now resume instead of stopping silently.**
  A bulk fetch cut short by a restart previously relied on a hand-written recovery
  pass that only ran at startup. The operations system now owns the resume, and a
  resumed run skips books it already fetched — so recovering a 10,000-book fetch no
  longer risks re-requesting every book from the external metadata services.
- **A cancelled fetch is recorded as cancelled**, not as completed. A run stopped
  part-way used to be indistinguishable from one that finished.

### Changed

- Metadata candidate fetches are now tracked entirely in the current operations
  system rather than being mirrored into the older one. Fetches started before this
  change stay fully visible in the Resume Review picker and the review screens —
  both are read together.
- Candidate fetches now appear with the correct label in the activity feed.
