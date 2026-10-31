<!-- file: docs/decisions/2026-10-30-bulk-fetch-gate.md -->
<!-- version: 1.0.0 -->
<!-- guid: 461533cd-8ba5-4503-9889-263eedac183e -->
<!-- last-edited: 2026-10-30 -->

# BULK-FETCH-NO-GATE: retire the endpoint, do not gate it

## Decision

Option (b): `POST /api/v1/metadata/bulk-fetch` is retired and answers
`410 Gone` (the shared `gone()` stub, behind the original
`library.edit_metadata` permission, for one release). Its handler and tests
are deleted.

## Why

The handler applied the search's first non-review-only candidate with no
`applygate` (no score floor, sequence guard or identity check); only the
review-only source rule, field locks and `only_missing` limited it. Gating it
would have meant a second implementation of what `metadata.batch-apply-cached`
already does, with two paths to keep in step.

## Evidence that nothing needs it

Checked at origin/main 33b657980:

- `web/src`: `bulkFetchMetadata` in `services/api.ts` had no caller outside
  its own unit test. The Library page uses `startBulkMetadataFetch`
  (`library.bulk-metadata-fetch`). The dead dialog was removed earlier (TASK-092).
- `web/tests/e2e/utils/test-helpers.ts` only mocked the route.
- `docs/system/api.md` and `docs/AI-REFERENCE.md` listed it; updated.
- `scripts/record_demo.js` and `scripts/api_examples.sh` called it as a demo
  setup step; both now start the `library.bulk-metadata-fetch` operation.
- Everything else under `docs/` mentioning it is under `docs/archive/`.

## Rejected alternative

(a) Route every candidate through `applygate.Evaluate` with the batch apply's
score floor, sequence guard and identity check, reporting refusals per book.
Rejected because the endpoint has no caller and the gated path exists.

## Consequences

- `Server.updateFetchedMetadataState`, the handler's `updateFetchedMetadataState`
  constructor argument, the `MetadataHistoryRecorder` interface and
  `ApplyMetadataSystemTags` on the handler's fetch-service interface, and
  `metafetch.KnownBookAuthors` had no other user and are deleted. The
  `MetadataFetchService` mock was regenerated.
- Some comments in `internal/metafetch` still describe bulk-fetch as the reason
  for anchoring and the `CommitCandidateApply` doc; the behaviour they protect
  (the bulk walk caches hits unseen) still holds, so they were left for a
  separate wording pass.
