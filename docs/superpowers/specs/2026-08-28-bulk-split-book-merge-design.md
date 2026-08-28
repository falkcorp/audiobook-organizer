<!-- file: docs/superpowers/specs/2026-08-28-bulk-split-book-merge-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9efde9cd-915c-460f-9eb6-a75f1290a13f -->
<!-- last-edited: 2026-08-28 -->

# Durable bulk split-book merge

## Goal

Allow an operator to submit a JSON file containing reviewed persisted split-book
candidate IDs and have one durable operation merge them safely. The API never
accepts arbitrary book groups: candidate generation and review remain separate
from mutation.

## API contract

`POST /api/v1/dedup/split-book-candidates/bulk-merge` accepts:

```json
{
  "candidate_ids": ["candidate-ulid"],
  "keep_ids": { "candidate-ulid": "book-ulid" },
  "dry_run": false
}
```

`keep_ids` is optional. A missing value selects the candidate's first book ID.
The web client accepts either this object or a JSON array of candidate IDs from
an uploaded file, then sends the object form to the API.

## Safety model

The request handler resolves every candidate before queueing the operation and
rejects the full request when any ID is duplicated or unknown, a candidate has
fewer than two books, a requested keep ID is outside its candidate, or a book
appears in more than one submitted candidate. It snapshots the candidate IDs,
book IDs, chosen keeper, and suggested title into operation parameters so a
later rescan cannot replace queued work.

The operation runs candidates sequentially. Each candidate has an outcome:
merged, dry-run-ready, or failed. A failure does not abort later disjoint
candidates. A candidate is deleted only after all of its source books were
merged without errors; partial failures remain available for review and retry.

The existing single-candidate endpoint uses the same complete-success deletion
rule.

## Detection extension

The current detector remains the first, strict lane. A second preview-only
lane will group sibling records by canonical parent folder and require at least
three near-sequential leading numbers extracted from filename first and title
second, plus a shared normalized stem after the number. It may tolerate
missing/mismatched author and series IDs, but records those discrepancies as
evidence. It never merges automatically. A standalone number-leading title
without a sibling sequence, such as `1984` or `86-Neon`, is not a candidate.

## Verification

Tests prove: duplicate/unknown/overlapping inputs never enqueue; the operation
snapshots candidates; successful candidates are deleted; failed or partial
candidates remain; dry run is non-mutating; filename-first sequence detection
accepts a genuine chapter set and rejects standalone numeric titles.

## Rollback

The operation can be disabled by not invoking the new endpoint. Any queued
operation is cancellable before it starts. Failed candidates persist; no
automatic retry mutates them. A deployed implementation can be reverted
without changing existing candidate data.
