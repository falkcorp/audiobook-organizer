### Fixed

- Dedup: a pin freezes the score. A scanner re-upsert (`UpsertCandidateNew`) and the single-row `UpdateCandidateScore` no longer rewrite the score breakdown, band or formula version on a manual (pinned) candidate, including a scanner row a human pinned. The re-upsert still refreshes the evidence fields (layer, similarity). `UpdateCandidateScore` now returns `ErrManualCandidateProtected` for a pinned row, matching `UpdateCandidateScores`.
- Dedup bulk-link and link-series now check the members of each book's existing version group too, so linking into a group can no longer join a same-path twin or a pinned manual partner that was grouped earlier. A pair already in one group together is not re-checked.
- `dedup.purge-legacy-fp-candidates` now keeps same-path pairs pending (`kept_same_path`). It loads the needed book paths in one paged read.
- `dedup.breakdown-backfill` writes through the guarded `UpdateCandidateScores` (one batch per A-group, one `SyncCandidateWrites` at the end), so a row pinned after the op listed the backlog stays unscored (`skipped_manual_at_write`).

### Added

- Dedup: when the LLM review returns a verdict for a pinned candidate, the verdict is stored on the row as advice (`ai_advice_verdict`, `ai_advice_reason`, `ai_advice_at`) and shown in the candidate drawer as "AI advice (not applied)". It never changes the row's status, band, score or layer and never merges or dismisses. The LLM review still does not submit pinned rows, so advice appears only on rows pinned after their batch was submitted.
