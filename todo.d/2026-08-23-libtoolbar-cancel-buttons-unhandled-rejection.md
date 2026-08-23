- [ ] **Two `cancelOperation` call sites in `LibraryToolbar.tsx` have no
      try/catch and are unhandled rejection risks.** `onClick={() =>
      api.cancelOperation(activeOrganizeOp.id)}` and the matching
      `activeScanOp` button (`web/src/components/library/LibraryToolbar.tsx`,
      near the "Organizing:"/"Scanning:" progress rows) call
      `cancelOperation` without `await` or a catch, unlike every other caller
      of that function (`OperationsIndicator.tsx`, `ActivityLog.tsx`,
      `ITunesImport.tsx`, `useImportFolderHandlers.ts`, all of which
      try/catch it). `cancelOperation` already threw on any non-2xx response
      before TASK-115 (registry-not-initialized 500, etc.), but TASK-115 made
      the 404 path far more reachable — cancelling an op that already
      finished on its own (a race any user hitting the button after the
      progress bar completes can trigger) now throws where it previously
      returned 204 silently. Add a try/catch at both call sites, mirroring
      the pattern in the other four callers (e.g. a toast on failure), so a
      stale-id cancel click doesn't surface as an unhandled promise
      rejection in the console.
