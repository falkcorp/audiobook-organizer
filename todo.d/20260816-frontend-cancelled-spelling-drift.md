- [ ] **The frontend waits for `cancelled`; the backend mints `canceled`. The
      poller never terminates.** `web/src/services/api.ts:2014` ends
      `pollOperation` on `completed | failed | cancelled` (two Ls). The registry
      mints `"canceled"` (one L) — `internal/operations/registry/legacy_op_status.go:61`
      and every awaitStatus in the registry tests. The two spellings never meet,
      so cancelling an operation leaves `pollOperation` looping at 1s forever
      while the UI shows it still running.
      Measured 2026-08-16: `'cancelled'` appears exactly once in `api.ts` — that
      line — against three uses of the correct `'canceled'` in the same file.
      **9 live call sites**: `DedupBookTab.tsx` (×2), `DedupAuthorTab.tsx` (×2),
      `DedupSeriesTab.tsx`, `DedupReconcileTab.tsx` (×2), `dedupHelpers.tsx`,
      and `Library.tsx` via `utils/operationPolling.ts`.
      Two more compare an API-returned status against the same wrong spelling:
      `web/src/components/dedup/dedupHelpers.tsx:26` and
      `web/src/pages/Diagnostics.tsx:193`.
      Not every `'cancelled'` in `web/src` is a bug — `SettingsGeneral.tsx`,
      `PathsSettingsTab.tsx`, `useImportFolderHandlers.ts` and
      `useSettingsHandlers.ts` declare their own local scan-status unions that
      never touch an operation record. Fix only the three that read the API, or
      the rename will look done while the real ones stay broken.
      This is the same two-sided vocabulary drift as the `legacyStatusFor` bug
      fixed in #2502: one side mints a string, the other enumerates them, and
      nothing fails loudly when they disagree. A shared constant — or a
      terminal-status helper exported from `api.ts` and used by all three — is
      what stops it recurring a third time.
