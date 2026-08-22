### Changed

#### ABS timeBase field documented as permanent allowance

Added explanatory comment to the hardcoded `timeBase: "1/1000"` value in the ABS file-to-DTO mapper. The real ABS reports ffprobe's actual stream time_base, but this codebase does not capture it at import, and no known client divides by this value. Per owner decision 2026-08-12, the field is set to a fixed placeholder rather than adding an ingest field and backfill for a value nothing consumes. The allowance should be revisited only if a client is found to actually use timeBase.
