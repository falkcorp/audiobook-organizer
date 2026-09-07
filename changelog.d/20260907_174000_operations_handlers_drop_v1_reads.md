### Fixed

- **The `/dashboard` endpoint's recent-operations list was frozen at 2026-08-21**,
  the same two-week freeze as the dashboard panel: it read the retired operations
  keyspace. It now reports current runs.

### Changed

- **"Clear Stale" no longer sweeps the retired operations keyspace.** It kept a
  half that force-failed old records still claiming to be running. Nothing has
  written a record there since 2026-08-23, so that half could only ever act on
  pre-retirement rows, and the storage holding them is being removed. The half
  that repairs current operations — the one added a few days ago to fix runs stuck
  on screen at "199/200" — is untouched and is now the whole button.

  The response still reports `cleared`; the `v1_failed` field is gone.

- **An operation's stored result is now read from one place instead of two.** The
  results endpoint checked the current operations storage and then fell back to the
  retired one, because five operations were still writing their results to the old
  location as recently as 2026-08-23. All five were moved in a prior change and a
  test now prevents them drifting back, so the fallback was removed.

  Results belonging to runs from before 2026-08-23 are no longer retrievable. That
  is intentional and part of retiring the old operations storage.

  One behaviour change worth knowing: if the operations store itself errors while
  looking a result up, the endpoint now reports a server error instead of "not
  found". Reporting a lookup failure as a missing result told people their output
  was gone when the truth was that we could not check.
