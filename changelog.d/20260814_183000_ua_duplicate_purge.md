### Added

- New `purge-unknown-author-duplicates` maintenance job (dry-run default):
  soft-deletes "Unknown Author/" books whose EVERY file has a content-verified
  twin outside that tree — identity measured on interior probes (25/50/75%),
  never head/tail where tag blocks differ; the twin must exist on disk and
  belong to a live real-author book. UA-only survivors and size-collisions
  are kept, per the 2026-08-13 mass-reorganize audit's rules.
