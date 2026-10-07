### Fixed

- **iTunes write-back no longer drops batches.**
  - Since iTunes was pointed at the organizer's own `.itunes-writeback` library,
    every write-back flush had been rejected by the location-form guard, which
    read the library's ~46k legitimate `.itunes-writeback/` media locations as
    staging leaks. The batcher then dropped the batch. Prod logged 662 dropped
    batches (4,293 book updates, 1 remove) between 2026-09-23 and 2026-10-07.
  - Every writer of the configured library now scopes the guard to that
    library's own media root (`itunes.WritebackRootForLibrary`). This covers the
    batcher's write and its re-read audit, `/itunes/relocate`, `/itunes/rebuild`,
    `/itunes/write-back[-all]`, deferred updates and `PinLastKnownGood`.
  - The queue is durable (store raw KV) and reloaded on start. A failed flush
    keeps the batch and retries with backoff (1 min doubling to 1 h); it is
    never dropped.
  - Removes are tombstoned in the external-id map only after the write lands.
  - Over-cap removes are held for the owner instead of discarded, and are
    released with `POST /api/v1/itunes/writeback/held/release`.
  - New `GET /api/v1/itunes/writeback/status` shows pending, held, failures,
    the last error and the next retry.
  - `.bak-<ts>` backups are taken only before a write that passed validation,
    so rejected attempts no longer churn real history out of the keep-5
    rotation.
- **Removing many tracks from iTunes failed its own safety check.** After
  cutting more playlist entries than the bytes that follow the playlist list
  (about 27 entries), `RepairITLDropDanglingMtphLE` never shortened the
  playlist container, and `container-tiling` rejected the write. It now
  updates the length from the pre-splice offset.
