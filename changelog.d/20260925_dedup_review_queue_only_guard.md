### Fixed

- Dedup: pinned (manual) candidates and same-path pairs are now left alone by every automated pass, including when the pin lands while the pass is running. A new store write, `ReclassifyCandidate`, re-checks the row under the lock the pin takes. Drain-stale, purge-legacy-fp, the triage dismissal and the dataset-backfill dismissal all use it. `UpdateCandidateLLM` refuses pinned and already-decided rows, and `UpdateCandidateScores` skips pinned rows. The shared guard is `dedup.AutomatedResolutionRefusal` / `RecheckAutomatedMerge`.
- Dedup LLM verdict apply: a pair pinned after the OpenAI batch was submitted is no longer auto-merged, and its layer is no longer rewritten to `llm`. Auto-resolve re-checks each pair right before merging.
- Dedup rescore no longer re-bands pinned manual rows (`skipped_manual` in the result). The earlier regression test could not fail and has been rewritten.
- `POST /dedup/candidates/bulk-link` now applies the list's `source` filter, and refuses `both_unmatched` instead of silently widening the set.
- Bulk-link and link-series no longer link a same-path or pinned pair, either directly or through a chain of other links (A–C plus B–C). Refused pairs and clusters are reported as failures.
- The exact-file-hash auto-merge skips a pair that has a pinned manual candidate. Purge-stale no longer deletes a same-path pair through any of its rules, including the shared-version-group rule.
