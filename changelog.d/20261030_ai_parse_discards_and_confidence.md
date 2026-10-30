### Fixed

- The `ai parse summary` no longer reports a fully-green `N/N book(s) parsed` for a batch where nothing was written. Rows the saver cannot resolve (deleted, merged, files gone) are counted as `N discarded` instead of parsed, named in the operation record, and logged at warn level. Rows whose save errored are no longer also counted as parsed.

### Added

- The model's self-reported parse confidence is now read: the summary shows `confidence high N / medium N / low N`, the per-row debug log carries it, and AI-filled fields are recorded in change history with source `scan.ai-parse:<confidence>`. Saves are not gated on it; see `docs/decisions/2026-10-30-ai-parse-confidence.md`.
