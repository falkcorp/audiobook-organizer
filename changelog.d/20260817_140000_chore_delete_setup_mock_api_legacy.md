### Removed

- **`setupMockApiLegacy`** — deleted 1,003 lines of dead E2E mock helper from
  `web/tests/e2e/utils/test-helpers.ts`. It had zero callers repo-wide.

  It was worse than merely unused: it carried its own
  `/api/v1/operations/{scan,organize,active}` handlers, so anyone auditing the
  mocks for a stale endpoint found two copies and reasonably updated both. That
  happened during the 2026-08-16 v2-endpoint repair, where the second copy was
  identified as dead only after a grep.

  Verified before deletion: one repo-wide match, its own definition. Verified
  after: `tsc --noEmit` clean (0 errors, same as the pre-deletion baseline), and
  `check-spec-discovery` still reports 38 spec files contributing 287 chromium
  tests — every spec transitively imports this module, so a broken export would
  have surfaced there.
