### Added

- A dedup signal for chapter structure. Per-chapter boundaries have been stored
  per book for a long time and read by nobody in dedup — `GetChaptersForBook`
  had three callers (the chapters backfill, the ABS mapper, scan-time
  persistence) and there was no `SignalKind` for them at all. A matching chapter
  table is close to a fingerprint of a book's *edition*, because the boundaries
  come from the recording rather than the text.
- `SigChapterStructure` scores 0.85–0.93 (owner decision, 2026-09-22), ranked
  above `SigEmbedMedium` and beneath `SigLSHAcoustID`: structural evidence of
  the same edition is stronger than text similarity and weaker than matching
  audio. A match requires the same chapter count and every boundary within
  1 second; partial agreement earns nothing.
- The signal is wired into `collectPairSignals` but ships **disabled**
  (`dedup.signals.chapter_structure.enabled`). Its confidence feeds a noisy-OR,
  so enabling it moves band assignments library-wide; an operator turns it on,
  runs one `dedup.rescore`, and compares before it influences anything.
- `GetChaptersForBook` is now declared on `database.Store` (via a read-only
  `ChapterReader`). It previously existed only on `*PebbleStore`, and production
  wraps the store in the Bleve `indexedStore` decorator — the shape where a
  capability assertion silently misses in prod. Declaring it makes a decorator
  that fails to forward it break the build instead.
