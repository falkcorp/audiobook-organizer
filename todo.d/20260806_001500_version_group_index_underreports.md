<!-- file: todo.d/20260806_001500_version_group_index_underreports.md -->
<!-- version: 1.0.0 -->
<!-- guid: f1a7d520-9c34-4e86-b0d2-73e5814cb96f -->
<!-- last-edited: 2026-08-06 -->

- [ ] 🐛 **`GetBooksByVersionGroup` silently under-reports group membership, which
  breaks the one-primary-per-group invariant.** Found in production 2026-08-06
  while version-grouping the two copies of *The Successors*.

  **Symptom.** Two books both carry `version_group_id =
  01KNDBPNB289W2Y6TMXS2DDSEG`, but `GET /api/v1/version-groups/<gid>` returns
  only ONE member. `PUT /audiobooks/<id>/set-primary` therefore leaves BOTH books
  flagged `is_primary_version = true`, so the library shows two tiles for one
  book. Re-running set-primary does not help — it demotes only what the lookup
  returns.

  **Root cause** (`internal/database/pebble_store.go`, `GetBooksByVersionGroup`).
  The fast path iterates a `book:versiongroup:<gid>:<id>` index, then falls back
  to a full scan **only when the index yields ZERO results**:

      if len(books) > 0 { sortVersions(books); return books, nil }
      // Fallback: full scan for groups whose index hasn't been backfilled yet

  A *partially* populated index — some members indexed, some not — returns the
  partial set and never falls back. The zero-result guard reads like a correct
  fallback and is exactly wrong for partial data.

  The index is only refreshed by `UpdateBook` when `VersionGroupID` **changes**,
  so a book that acquires a group through a path that does not trip that
  comparison never gets an index entry. Re-POSTing
  `/audiobooks/<id>/versions` does not repair it: the group ID is already
  correct, so nothing changes and no index write occurs.

  **Blast radius is wider than the one endpoint.** `ApplyVersionGroup`
  (`internal/plugins/maintenance/regroup_apply.go`) uses the same function to
  "enumerate every current member and demote strays" — the safety net that keeps
  one primary per group when a `regroup.version-group` hold is approved. With a
  partial index that net silently does nothing, so approving a version-group hold
  can leave two primaries behind.

  **Fix directions** (pick after measuring):
  1. Make the fallback trigger on *suspected incompleteness*, not just zero — e.g.
     always cross-check against the authoritative rows, or verify the returned
     count against a group-size counter.
  2. Write the index entry on every `UpdateBook` where `VersionGroupID` is
     non-empty, not only when it changes (idempotent write).
  3. Add a repair op that rebuilds `book:versiongroup:*` from the Book rows, and
     run it once — existing groups are already affected.

  **Also needs an invariant test**: after linking N books into a group and
  setting one primary, exactly one member must have `IsPrimaryVersion == true`.

  Related: [[version-group-acoustic-audit]] (which will read group membership and
  would inherit this under-reporting), [[first-aid-library-validate-repair]].
