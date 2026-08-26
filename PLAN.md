<!-- file: PLAN.md -->
<!-- version: 1.0.0 -->
<!-- guid: e9651b18-d6ed-4dcb-bb13-1e1f0d1e98f7 -->
<!-- last-edited: 2026-08-26 -->

# Bound operation-log rendering

## Goal

Keep expanding a library-scan operation from exhausting browser memory by making
the operation-details API and the bell/log panel retain a bounded recent tail.

## Affected files

- `internal/server/handlers/operations_v2.go` — constrain operation-detail log
  responses to the UI-safe tail and expose a stable bound.
- `internal/server/handlers/operations_v2_test.go` or existing handler test —
  prove an oversized request cannot produce an oversized payload.
- `web/src/services/api.ts` — request the UI-safe tail explicitly.
- `web/src/pages/ActivityLog.tsx` — render and retain only the bounded tail.
- `web/src/pages/ActivityLog.test.tsx` — cover the large-log regression.
- `changelog.d/` — record the user-visible reliability repair.

## Steps

1. Add failing backend and frontend regression tests for a large operation log.
2. Apply the smallest API and client bounds that make both tests pass.
3. Run targeted Go and web tests, then the relevant build/lint checks.
4. Commit the repair with a conventional commit and open a pull request.

## Test strategy

- `GOTOOLCHAIN=go1.26.0 go test ./internal/server/... -run 'Operation.*Log|GetOperationV2'`
- `npm test -- --run web/src/pages/ActivityLog.test.tsx`
- `npm run build`

## Rollback

Revert the single repair commit. The change only limits display and response
size; operation execution and stored logs remain intact.
