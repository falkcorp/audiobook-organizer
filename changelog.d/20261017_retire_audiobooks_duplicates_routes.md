- The seven `/api/v1/audiobooks/duplicates` routes that only the old `/dedup`
  page's Version Groups and Duplicate Scan tabs called now answer `410 Gone`
  for one release (03-PR12, owner decision D9): `GET /audiobooks/duplicates`,
  `GET /audiobooks/duplicates/scan-results`, `POST /audiobooks/duplicates/scan`,
  `POST /audiobooks/duplicates/link` and its alias `/merge`, and
  `POST /audiobooks/duplicates/reject` and its alias `/dismiss`. The body is
  `{"error":"gone","message":"this endpoint has been removed; use GET /api/v1/dedup/candidates (the Review page, Duplicates lane) instead"}`.
  Each stub keeps its old permission guard, so a caller without access still
  gets 401 or 403.
- Before retiring them, every caller was checked: no web page, script, tool,
  runbook or AudioBooth request uses them. `scripts/api_examples.sh` Example 10
  now lists exact-match candidates from `GET /dedup/candidates`.
- Removed with them: the five handlers, `AudiobookService.GetDuplicateBooks`
  and its result type, the `book-duplicates` dedup-cache key, the handler
  package's `AudiobookService` interface and its mock, and the
  dismissed-group save helper. Nothing writes the `dedup_dismissed_groups`
  preference any more; the book-dedup scan and the duplicate-copies fixer still
  honour the groups already stored there. The store's `GetDuplicateBooks`
  stays (the `dedup.book-scan` op uses it), and so does that op.
- The seven paths are removed from `docs/api/openapi.json`.
