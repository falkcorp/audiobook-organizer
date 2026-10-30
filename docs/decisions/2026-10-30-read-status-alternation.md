<!-- file: docs/decisions/2026-10-30-read-status-alternation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5c1e8a3f-7b24-4d96-a0e1-3f8d29b6c471 -->
<!-- last-edited: 2026-10-30 -->

# Library filters= read_status alternation

## Finding

The TODO entry described `(finished|in_progress)` answering 200 with count 0. On
current main that spelling already works: `search.PerUserFilterForTerm` parses it
as one OR-inside filter. Only the bare spelling `finished|in_progress` still
failed, and it failed as a 400 (the whole string is not a status), not an empty 200.

## Decision log

- Chosen: `PerUserFilterForTerm` wraps a bare read_status alternation in
  parentheses before parsing, so the bare and parenthesised forms are one filter
  that ORs inside itself, and every alternative is validated against the status
  set (an unknown one is a 400 naming it). One place feeds both validation and the
  row matcher.
- Rejected: splitting into one FieldFilter per status in filter_date.go. The route
  ANDs filters, so an OR needs the in-filter alternation the matcher already has.
- Rejected: refusing every non-single value with a 400. It would break the
  parenthesised form the search help documents.
- Left alone: `last_played` and `progress_pct` keep their rules (a bare
  alternation there stays a 400).
