### Added

- `POST /api/v1/dedup/candidates` puts hand-picked book pairs into the dedup
  review queue, so a pair no scanner found can be reviewed in the same list as
  scanner candidates. Body: `{"pairs": [{"book_a", "book_b", "reason"}],
  "dry_run": true}`. A dry run is the default and only an explicit
  `"dry_run": false` writes. Each pair is checked (both books exist and are not
  deleted, different books, not already versions of each other) and reported
  as created, already open, reopened, already decided, or rejected with a
  reason. Sending the same pair again returns the existing candidate. Nothing
  is merged. New candidates carry layer and source `manual`, and
  `GET /dedup/candidates?source=manual` lists them.
- Manual candidates are exempt from every automated pass that deletes,
  dismisses, reclassifies, re-scores or auto-merges candidates. That covers
  purge-stale, unified scoring's suppression, the AcoustID veto and reset,
  drain-stale, purge-legacy-fp, exact-triage, dataset backfill, breakdown
  backfill, auto-resolve and the LLM review. Only a reviewer's merge or dismiss,
  or deleting one of the books, ends one. An open scanner candidate named in a
  request is pinned the same way.
