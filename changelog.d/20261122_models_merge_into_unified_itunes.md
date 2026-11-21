### Changed

- Merged `internal/models` into `internal/dedup/unified` (the dedup score types and provenance helpers) and `internal/itunes` (`Audiobook`, `Author`, `Series`); the package is deleted and the unused request/response types went with it. Stored candidate rows and the score JSON are byte-identical (pinned by golden tests added first).
