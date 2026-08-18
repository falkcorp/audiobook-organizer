### Changed

#### Six more interfaces split, and a note on the ones deliberately left alone

- **`logger.Logger`** (10) → 5: `LevelLogger` (the five leveled methods most
  consumers actually want), `ProgressReporter`, `ChangeRecorder`,
  `CancellationReporter`, `SubLoggerFactory`.
- **`fsRegroupStore`** (11) and **`itunesRegroupStore`** (9) → 5 each, **three of
  them shared**. The file already described them as twins; nine of their methods
  were the same nine, written out twice. They now embed one set of shared
  declarations, so the vocabulary is visible in the type instead of being two
  parallel lists that can drift apart silently.
- **`UserPlaylistStore`** (9) → reader/writer.
- **`BookVersionStore`** (9) → reader / writer / disposition reader.
- **`APIKeyStore`** (9) → reader / writer / lifecycle / usage recorder.

Each original name is retained as the composition of its pieces, so every method
set is byte-identical and no consumer moves; verified by comparing the full
signature set — methods *and* embeds — against the base revision.

Also adds `docs/audits/2026-08-18-interface-width-shapes.md`, which classifies the
remaining findings by structure. Five of them (`database.Store` 40,
`itunes/service.Store` 17, `server.bookHandlerStore` 12, `maintenance.JobStore`
12, `organizer.Store` 9) are compositions of embedded interfaces with **zero
declared methods**. They could all be turned green by regrouping their embeds into
buckets of eight, which would change nothing about what any consumer can reach.
They need narrowing by actual usage instead, and the audit says so where someone
staring at a red build will find it.
