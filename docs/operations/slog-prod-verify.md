<!-- file: docs/operations/slog-prod-verify.md -->
<!-- version: 1.1.0 -->
<!-- guid: 9fce353e-76db-495b-a4ac-475536aea16f -->
<!-- last-edited: 2026-08-11 -->

# SLOG-PROD-VERIFY — metadata fetch smoke test

> **Canonical runbook.** A second, independently-written copy lived at
> `docs/slog-prod-verify.md` until 2026-08-11; its unique content (the verification
> checklist below) was folded in here and the duplicate was archived to
> `docs/archive/slog-prod-verify-toplevel.md`. This file is the one cited by TODO item 28
> and by [`pending-prod-actions.md`](pending-prod-actions.md).

Smoke-test the production metadata-fetch operation from the outside so we can
confirm the full chain is wired up end-to-end:

1. `metadata-fetch` operations must emit `op:<op_id>` in their slog records so
   the log stream can be correlated to the operation ID.
2. `GET /api/v1/operations/:id/activity` must return timeline entries for that
   operation (either from the activity log table or the op-log fallback).

This document collects the commands, assertions, and log checks needed to
satisfy the `SLOG-PROD-VERIFY` TODO entry.

## Requirements

- **Production API credentials** (bearer token) scoped for
  `PermLibraryEditMetadata` (metadata fetch) + `PermOpsRead`/`PermOpsList`.
- **Access to prod logs** (systemd `journalctl`, Loki, CloudWatch, etc.) so you
  can search for `op:<op_id>` and `metadata-fetch` text.
- **Prod base URL** (for example `https://audiobook-organizer.falkcorp.com`).

```bash
PROD_BASE_URL="https://<your-prod-host>"
PROD_TOKEN="<your-prod-api-token>"
```

## Steps

### 1. Pick a book ID (one-off selection)

```bash
BOOK_ID=$(curl -fsS -H "Authorization: Bearer $PROD_TOKEN" \
  "$PROD_BASE_URL/api/v1/audiobooks?limit=1" | jq -r '.entries[0].id')
```

Adapt the query if your prod API exposes a different payload shape. The goal is
just to use a known book ID for the smoke test so the metadata fetch has a
small, bounded scope.

### 2. Trigger the metadata fetch operation

```bash
OP_ID=$(curl -fsS -X POST \
  -H "Authorization: Bearer $PROD_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "def_id": "library.bulk-metadata-fetch",
    "params": {
      "selection": { "book_ids": ["'"$BOOK_ID"'"],
      "skip_cached": true
    }
  }' \
  "$PROD_BASE_URL/api/v1/operations/v2" | jq -r '.op_id')
```

- `skip_cached: true` avoids unnecessary API calls that could hit rate limits.
- The response returns `op_id`; capture it for the remaining steps.

### 3. Wait for the operation to become visible

```bash
curl -fsS -H "Authorization: Bearer $PROD_TOKEN" \
  "$PROD_BASE_URL/api/v1/operations/v2/$OP_ID" | jq '.operation.status'
```

- Poll until `status` is `running` or `completed` (may take a few seconds).
- The operation is resumable so it should not stay in `pending` beyond the first
  progress update.

### 4. Verify the opID appears in the logs

Search the production log stream for `op:<op_id>` plus `metadata-fetch`: this
proves the structured logging pipeline propagated the op context.

- `journalctl` example (change to your log path / service name):

```bash
sudo journalctl -u audiobook-organizer -n 200 --no-pager | rg "op:$OP_ID"
```

- Loki/CloudWatch example: filter on `op:$OP_ID` and `operation_type=metadata-fetch`.
- Look for entries such as `metadata-fetch ops` or `Bulk Metadata Fetch` that
  include `op:<op_id>` as one of the slog attributes.

If the log search returns nothing, check the `logging.OpContext` wiring in the
code (the smoke test is meant to guard against regressions there).

### 5. Hit the operation activity endpoint

```bash
curl -fsS -H "Authorization: Bearer $PROD_TOKEN" \
  "$PROD_BASE_URL/api/v1/operations/$OP_ID/activity?limit=200" | jq '.entries | length'
```

Expect `entries | length` to be at least `1`. If the activity log table has no
entries (the query returns zero rows), the handler falls back to the op-log
history and still returns `entries` (possibly populated with raw `OpLogV2`
lines). You can verify that by asserting:

```bash
curl -fsS -H "Authorization: Bearer $PROD_TOKEN" \
  "$PROD_BASE_URL/api/v1/operations/$OP_ID/activity?limit=200" | jq '.total'
```

- `total` should match the `entries | length` only when there are entries.
- Inspect the first entry to ensure it has `operation_id == $OP_ID` and the
  `operation_type`/`level`/`message` fields you expect.

### 6. (Optional) Cancel/resume for cleanup

If the operation is still running and you just needed to verify telemetry,
cancel it instead of letting it finish:

```bash
curl -fsS -X DELETE -H "Authorization: Bearer $PROD_TOKEN" \
  "$PROD_BASE_URL/api/v1/operations/v2/$OP_ID"
```

The registry respects the cancel request and will log a `cancel` event that also
includes `op:$OP_ID`.

## Verification checklist

Tick all three before considering the run successful:

- [ ] The `metadata-fetch` operation returned an explicit op ID and is visible in the
      structured log stream (`op=<OPID>` / `op:<OPID>`).
- [ ] `/api/v1/operations/<OPID>/activity` returns **multiple** rows for the same op
      (start / progress / completion). If the operation is still streaming, expect the
      first request to show rows only after a few seconds.
- [ ] Activity rows include the metadata domain tags — e.g. `action: metadata-apply`,
      `domain: metadata`, `source: metadata`.

## Failure signals to watch for

- Activity endpoint returning an empty array **and** the log search shows no
  `op:<op_id>` entries. This indicates the operation context didn't reach the
  activity log layer or the fallback op-log store.
- Logs contain `op:<op_id>` but the activity endpoint still shows zero rows even
  after waiting a few seconds. Check that the activity service can read the
  `operation_id` column (schema change) or that the op-log fallback is wired.

## Summary

Documenting this smoke test completes the `SLOG-PROD-VERIFY` story so the next
person can reproduce the verification without digging through the codebase. Do
not mark the TODO as finished until you have run the steps against production
and confirmed both assertions.

Re-run this verification each time the metadata pipeline is redeployed, or when a new
logging change lands, so instrumentation regressions are caught early.
