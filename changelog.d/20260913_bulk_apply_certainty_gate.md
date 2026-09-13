### Fixed

- **Bulk metadata apply no longer puts one volume's metadata on another.**
  Bulk apply took the top-scored candidate with no score floor, so a 0.99
  match for "Big Cats 3" could be applied to "Big Cats 1", and with
  `auto_rename_on_apply`/`auto_write_tags_on_apply` on it also retagged and
  moved the files. Every bulk path (`metadata.batch-apply-cached`,
  `/metadata/batch-apply-candidates`, metadata auto-upgrade) now goes through
  one certainty gate (`internal/applygate`): score >= 0.90 (0.85 when the
  transcribed audio confirms the candidate), the cache identity check
  (`ValidateCachedIdentityForBook`, or the fetch-time title/author on the
  op-results path), and a volume-number guard (`internal/seqnum`) that blocks
  when the book's and the candidate's numbers differ, or when the book has one
  and the candidate has none. Years, bitrates and disc/part/track markers are
  not treated as volume numbers. A refused book is not touched and is reported
  with its reason for manual review.

### Added

- **Bulk-apply dry run** (`metadata.bulk-apply-preview`). For each book it
  reports the candidate a bulk apply would take, the gate verdict and reason,
  the per-field changes after locked fields are stripped, and whether the file
  sequel would rename and to which paths, without writing anything. Start it
  with `POST /api/v1/metadata/bulk-apply-preview` (`book_ids`,
  `all_cached: true`, or `operation_id`); read it with
  `GET /api/v1/metadata/bulk-apply-preview/:id` (`limit`, `offset`,
  `verdict`, `reason`, `download=1`), which also returns counts by verdict and
  reason.
- Both bulk-apply endpoints now default to a dry run: a request without
  `"dry_run": false` enqueues the preview and applies nothing. The web UI
  sends `dry_run: false` explicitly.
