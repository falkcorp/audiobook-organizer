- [ ] **E2E repair progress — measured 2026-08-09. Supersedes the stale
      "146 failures across 22 files" triage.**

      **Suite: 66 failed / 218 passed / 4 skipped of 288 chromium tests.**
      Down from 146 failed / 138 passed. **80 fixed, 55%**, across 8 merged
      PRs (#2211-#2221). No spec has been deleted or skipped.

      **Fully green now:** `dedup` (was 26), `dedup-operations` (was 8).

      **Current distribution:**

        12  library-browser          3  unified-dedup-tab
        11  transcode-and-counting   3  scan-import-organize
         8  batch-operations         2  search-and-filter
         6  version-management       2  error-handling
         6  dynamic-ui-interactions  1  settings-configuration
         4  metadata-provenance      1  library-enhancements
         4  files-history            1  import-paths
                                     1  diagnostics
                                     1  auth-flow

      **Untouched, and therefore the best value per hour:**
      `version-management` (6), `dynamic-ui-interactions` (6),
      `files-history` (4), `unified-dedup-tab` (3), plus the tail of 1s and 2s.
      Files already worked have had their cheap causes taken; what remains in
      them is harder.

      **THE METHOD, which is the most transferable thing here.** Run ONLY the
      spec you are fixing, so `test-results/` is not buried under other tests'
      directories, then read `test-results/<dir>/error-context.md` BEFORE
      forming a hypothesis. Every genuine cause in this effort was found that
      way:

      - `/dedup` redesigned, tabbed UI behind a "Legacy View" toggle
      - `metadata-provenance` and `scan-import-organize` rendering the LOGIN
        screen because their window.fetch shims never mocked /auth/status
      - book sub-resources returning the BOOK object, crashing on `.length`
      - a fixture saying `library_state: 'import'` where the app checks
        `'imported'`, so a button disabled itself correctly

      Every wasted cycle came from reasoning about what *should* be on the page.
      Two whole cycles were spent that way on `transcode-and-counting` and the
      first pass at `scan-import-organize`.

      **Second lesson: scope fixes narrowly.** Three separate times a correct
      fix applied too broadly made things worse — a blanket `getByLabel` →
      `getByRole` sweep took one file from 5 failures to 7; a blanket
      "Fetch Metadata" rename broke the dialog button, which was never renamed.
      Verify each site rather than pattern-replacing.

      **Known causes are recorded per file** in the other todo.d fragments,
      including two hypotheses already tested and REJECTED for
      `transcode-and-counting` — read those before retrying it.

      **Two open questions that are arguably product issues, not test rot:**
      whether the library is still sortable without switching views (the
      "Sort by" control does not exist anywhere), and whether the "N selected"
      chip rendering twice is intentional.
