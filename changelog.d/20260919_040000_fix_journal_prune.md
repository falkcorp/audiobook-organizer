### Added

- Nightly prune of the AI result journal (`maintenance.prune-ai-journal`, scheduler task `ai_journal_prune`, runs in the maintenance window every 24h). It deletes whisper transcription results journalled more than `ai_journal_retention_days` ago (new setting, default `30`, `0` keeps them forever and disables the task, accepts 0 to 36500). Only the whisper kind is touched; other `aijournal:` kinds are left to their owners. Transcripts stored on books are not affected.
- `database.RawKVStore` gains `ScanPrefixPage` (bounded, cursor-paged prefix scan) and `DeleteRawBatch` (many deletes in one synced write).

### Fixed

- `resultjournal.Prune` no longer loads the whole journal into memory or fsyncs once per deleted key: it pages through the whisper keyspace 500 entries at a time, deletes each page's expired and undecodable entries in one batch, stops on context cancellation, and refuses a non-positive retention.
