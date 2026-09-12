### Fixed

- `dedup.llm-review` now declares a `ConcurrencyKey`, so the scheduler can no longer run two LLM review passes at once while both hold library-write capability. It was the only write-declaring dedup operation without one.
