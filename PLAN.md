<!-- file: PLAN.md -->
<!-- version: 1.1.0 -->
<!-- guid: e9651b18-d6ed-4dcb-bb13-1e1f0d1e98f7 -->
<!-- last-edited: 2026-08-26 -->

# Bound operation-log rendering

## Goal

Keep expanding a library-scan operation from exhausting browser memory by making
the operation-details API and the bell/log panel retain a bounded recent tail.

## Affected files

- `internal/server/handlers/operations_v2.go` — stream a gzip attachment for
  the complete raw operation log.
- `internal/server/wire_operations_routes.go` — register the protected download
  route before the single-operation route.
- `internal/server/handlers/operations_v2_test.go` — prove the attachment is
  gzip-compressed and contains every requested log line.
- `web/src/components/OperationActivityPanel.tsx` — request and render only a
  UI-safe recent tail, while offering the full-log download.
- `web/src/components/OperationActivityPanel.test.tsx` — cover the large-log
  regression and the download affordance.
- `changelog.d/` — record the user-visible reliability repair.

## Steps

1. Add failing backend and frontend regression tests for a large operation log.
2. Stream a compressed full-log attachment from the server and register it
   behind the existing library-view permission.
3. Lower the bell dialog to a UI-safe recent tail and link the download instead
   of putting the full response into JavaScript memory.
4. Run targeted Go and web tests, then the relevant build/lint checks.
5. Commit the repair with a conventional commit and open a pull request.

## Test strategy

- `GOTOOLCHAIN=go1.26.0 go test ./internal/server/... -run 'Operation.*Log|GetOperationV2'`
- `npm test -- --run web/src/pages/ActivityLog.test.tsx`
- `npm run build`

## Rollback

Revert the single repair commit. The change only limits display and response
size; operation execution and stored logs remain intact.
