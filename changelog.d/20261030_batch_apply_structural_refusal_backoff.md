### Fixed

#### metadata.batch-apply-cached no longer re-offers books whose refusal cannot change

The cached-metadata batch apply now remembers, per book (`apply_refusal:<book_id>`), when the certainty gate refused it for a structural reason (a duplicated file set, an author missing from the path, a candidate that cannot match the transcribed title, and the other file/path/tag/candidate-based reasons). A later run skips that book, without evaluating it, as long as a fingerprint of everything the gate reads (the cached candidate and its score, the book's tags and path, its file set with sizes and durations, the live authors, the transcribed title) is unchanged. Any change, a new build of the gate, or an owner-review pin re-evaluates the book. Transient refusals (store read failures, unreadable authors, apply failures) are never remembered. The run logs one summary line, "skipped N books with unchanged structural refusals", and the count appears in the final summary.

### Added

#### metadata.batch-apply-refusals-reset op

Forgets the remembered refusals so those books are evaluated again. `dry_run` (default true) only counts them; `book_ids` limits the reset to the named books. Design and rejected alternatives: `docs/decisions/2026-10-30-batch-apply-structural-refusal-backoff.md`.
