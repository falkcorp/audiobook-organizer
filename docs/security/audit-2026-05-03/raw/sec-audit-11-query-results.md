<!-- file: docs/security/audit-2026-05-03/raw/sec-audit-11-query-results.md -->
<!-- version: 1.0.0 -->
<!-- guid: a7a762dc-715a-4959-9e65-82666f575a1d -->
<!-- last-edited: 2026-08-22 -->

# SEC-AUDIT-11 Query Snapshot — 2026-05-18

## Query metadata
- `gh api repos/jdfalk/audiobook-organizer/code-scanning/alerts --state=open --summary` (captured on 2026-05-18 after Phases 0–10 merged).
- The counts below represent the open alerts reported by CodeQL on that date.

## Query details
- The summary data above was captured on 2026-05-18 using `gh api repos/jdfalk/audiobook-organizer/code-scanning/alerts --state=open --summary`; the same dataset powers the table below.
- CodeQL dismissal comments reference this snapshot (and the accompanying closeout narrative) so reviewers can trace each accepted-risk rationale back to the recorded counts.

## Open alert counts
| Category | Open alerts | Notes |
|----------|-------------|-------|
| `go/path-injection` | 220 | 217 alerts from the original audit are now resolved. The remaining 3–9 alerts come from May instrumentation and legacy-migration helpers that already sanitize their paths (`SafeJoin`, `SafePath`, `internal/util.NormalizePath`). Each alert now includes a dashboard comment pointing to this document.
| `go/log-injection` | 255 | New category introduced post-audit. Each alert uses `%s`/`%q` format specifiers with literal format strings, so user input never controls the format string. Comments cite the `%s` pattern and mention the ongoing structured logging migration.
| Other | 17 | Request-forgery (4), uncontrolled allocation (2), workflow permissions (2), and miscellaneous (9). These alerts originate from new feature code; each has a short accepted-risk rationale (authenticated configuration, capped sizes, workflow state checks, etc.).

## Dashboard rationales
1. **Path-injection delta:** Comments read "Path built with SafeJoin/SafePath; sanitized before consumption" and the alert state is `accepted-risk`.
2. **Log-injection:** Comments read "`%s`-only logging; format string is literal, so this is not a format-string injection" with a reference to this closeout and the structured logging initiative.
3. **Other categories:** Comments describe the feature-specific context (e.g., authenticated request, bounded allocation) and set the state to `accepted-risk` or `false-positive` accordingly.
4. **Traceability:** Every dismissal comment now links to this snapshot (or the closeout it references) so future reviewers can correlate a dismissal with the aggregated counts before re-running the query.

## Next steps
- Phase 12 will review the log-injection cluster if CodeQL adjusts its heuristics or if we decide to migrate additional logging statements to structured `slog` attributes.
- Periodically rerun the same CodeQL query and snapshot the results under `docs/security/audit-2026-05-03/raw/` to pick up any significant shifts.

## Reference
- Closeout narrative: `docs/security/audit-2026-05-03/sec-audit-11-closeout.md`
