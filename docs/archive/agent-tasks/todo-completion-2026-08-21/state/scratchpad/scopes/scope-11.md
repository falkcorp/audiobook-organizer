# Scope 11 — 26 items

## ITEM L5449 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Run the classify pass in prod** and record the numbers.
      `POST /api/v1/operations/v2 {"def_id":"maintenance.missing-file-audit","params":{"classify":true}}`.
      This is the first figure that actually sizes the recoverable population — the
      earlier sample could not, because it is clustered by iteration order. Off by
      default; it doubles the stat load on the NAS, so do not run it during a scan.

## ITEM L5454 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: internal/plugins/maintenance | all_domains_guess: internal/plugins/maintenance

- [ ] **Build the re-point repair.** It must UPDATE `file_path` to the flat name the
      classify pass derived, never delete a row. The tombstone comment at the bottom of
      `internal/plugins/maintenance/missing_file_repair.go` says so at the site. Gate it
      on the classify pass having run clean (controls unresolved) for the rows it touches.

## ITEM L5458 [tier B] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Decide what happens to the 16,265 fully-broken books** (every file entry dead).
      Still untouched, still needs a human call. They are now structurally impossible to
      delete by accident.

## ITEM L5461 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Missing-file audit Phase 1a still has no PR and is not mutation-tested.**
      Committed as `9b43f598` on `feat/persist-missing-file-verdict` (`.worktrees/auditpersist`).
      Either finish it or delete the branch — a committed-but-unmerged change to an op
      that runs against prod is the worst of both states.

## ITEM L5466 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: docs | all_domains_guess: docs

- [ ] **`database.Store` is grouped, not yet unreachable.** `.interface-width-baseline`
      is at 0 and `Store` declares six domain composites instead of forty embeds, but it
      still transitively carries all 398 methods and the six composites are only a
      relabelling. The actual split is still the plan of record —
      `docs/plans/2026-08-19-split-the-pebblestore-surface.md`. Do not read the 0 as
      that job being done.

## ITEM L5494 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: internal/server/handlers | all_domains_guess: internal/server/handlers;docs

- [ ] **AudiobookShelf-compatible API: series are broken, and collections/playlists are
      empty stubs.** Owner report 2026-08-09: *"series are broken on the audioshelf server
      stuff, because all of them report zero books, and when you click on them they just
      give you a random list of books… We need full collection support… Same with
      playlists."* Root causes located in the code below — this is server-side, as the
      owner suspected.

      > **STATUS 2026-08-14:** §1 (series) — the list now embeds `books` (2026-08-13
      > fixes; a residual `numBooks>0` with `books: []` defect on prod is tracked in the
      > 2026-08-14 task breakdown as B20). §3 (playlists) — SHIPPED in #2366
      > (`h.LibraryPlaylists` + `GET /api/playlists/:id`). **Remaining: §2 collections**
      > — still `h.EmptyPage`, the only stub route on the ABS surface; see
      > `docs/reference/abs-implementation-status.md`.

      ## 1. Series report zero books and open the wrong list

      `internal/server/handlers/abs/browse.go:464` `LibrarySeries` builds each series DTO
      with:

      ```go
      "books":         []any{},          // <- ALWAYS EMPTY, hardcoded
      "totalDuration": 0,                // <- likewise
      "numBooks":      counts[s.ID],
      ```

      Two distinct defects, and they explain both halves of the report:

      **(a) `books` is hardcoded empty.** The client is handed a series with no members.
      "Click a series and get a random list of books" is the client doing something
      reasonable with nothing — most ABS clients fall back to an unfiltered library query
      when the series carries no items. The books are not random; they are *the library*.

      **(b) `numBooks` comes from `GetAllSeriesBookCounts()`, whose error path is
      silent:**

      ```go
      counts, err := h.library.GetAllSeriesBookCounts()
      if err != nil {
          // "not worth failing the page over; report 0 books rather than 500"
          counts = map[int]int{}
      }
      ```

      If that call errors, **every** series reports 0 — which is exactly the symptom.
      The fallback is defensible as a design choice but it is **unobservable**: there is no
      log line, so a total failure of the count query looks identical to a library with no
      series members. Whatever the fix, add a `slog.Warn` here; a silent zero is how this
      went unnoticed. (It is also possible the counts are keyed differently from `s.ID` —
      check that before assuming the error path fired.)

      **Do:** populate `books` (at minimum the item IDs/minified items the ABS schema
      expects), fix or instrument the count path, and verify against a real client rather
      than by reading the JSON — the two failure modes look the same from the payload.

      ## 2. Collections are a stub

      `internal/server/handlers/abs/handler.go:386`:

      ```go
      r.GET("/api/libraries/:libraryId/collections", auth, h.EmptyPage)
      ```

      The route exists and answers 200 with an empty page. Nothing behind it.

      **Wanted** (owner): real collections — *"we may want to make a collection of scifi
      books that don't have stupid characters"*. That is a **user-curated, arbitrary set**,
      not a saved query: the membership rule ("no stupid characters") is a judgement the
      user makes per book and cannot be expressed as a filter. So this needs persisted
      membership, not a dynamic query.

      Needs: storage for collection + ordered membership, CRUD endpoints, and the ABS
      collection DTO shape on `GET /api/libraries/:id/collections` (and the single-collection
      and add/remove-item endpoints the clients call).

      ## 3. Playlists are the same stub

      `internal/server/handlers/abs/handler.go:387` — also `h.EmptyPage`.

      **Note the overlap:** `todo.d/20260805_214200_playlists_full_support.md` (already
      folded into `TODO.md`) covers playlists broadly — import of `.m3u`/`.m3u8`, static and

## ITEM L5650 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`book-detail.spec.ts` "soft delete, restore, and purge flow" fails only in the full
      parallel suite, never in isolation.** Surfaced 2026-08-09 as the last remaining
      failure after the e2e repair took the suite to 551 passed / 1 failed / 16 skipped of
      568 across chromium + webkit.

      **This is deliberately NOT fixed.** It is not spec rot — the test passes 6/6 alone —
      so changing the test to tolerate it would be papering over an unknown, and unlike the
      webkit pagination flake there is **no measurement yet establishing the app is
      correct**. Per the no-papering-over rule this gets written up and left red.

      **The failure:**

      ```
      [webkit] › book-detail.spec.ts:423 › soft delete, restore, and purge flow
      Error: expect(page).toHaveURL(expected) failed
        Expected pattern: /\/library$/
        Received string:  "http://127.0.0.1:8484/dashboard"
        - unexpected value "http://127.0.0.1:8484/login"
      ```

      After "Purge Permanently" the test expects `/library`. Instead the page went to
      `/login` and settled on `/dashboard` — the signature of an auth guard firing, not of
      a broken navigation.

      **What has been ruled out (each by measurement, not reasoning):**

      | hypothesis | result |
      |---|---|
      | The test itself is stale / selector drift | **No** — 6/6 passes on webkit in isolation, `--repeat-each=6` |
      | `auth-flow.spec.ts:90` pollutes shared server state by creating an admin account | **No** — that test `test.skip`s itself unless `requires_auth && !has_users`, and it skipped in every run examined. Confirmed by arithmetic: the full run's 16 skips = 7 `test.fixme` × 2 browsers + this bootstrap test × 2 browsers. It never executed, so it mutated nothing |
      | Reproducible by pairing the two specs under parallel workers | **No** — `book-detail` + `auth-flow`, `--repeat-each=4`, webkit: 24 passed / 4 skipped |

      **What is still open.** The suite runs `fullyParallel: true` with `workers: 2`
      (`playwright.config.ts:18-20`) against a **single shared Go server on :8484**. Every
      spec mocks at the browser layer (`page.route` or a `window.fetch` patch), but the
      server underneath is common to all of them. Something in a concurrently-running spec
      plausibly moves real server auth state — but the obvious candidate is now excluded,
      so the actual polluter is unidentified.

      **The artifact was lost, and that is the main obstacle.** Playwright clears
      `test-results/` at the start of every run, so the `error-context.md` and trace from
      the failing run were overwritten by the isolation re-runs before they were read. That
      is the one procedural mistake here: **read the artifact before re-running.** A repeat
      full-suite run with `--trace=retain-on-failure` is the way to recapture it.

      **Next steps, in order:**

      1. Re-run the full suite with `--trace=retain-on-failure` until it reproduces, and
         read `test-results/*book-detail*/error-context.md` **first**. That artifact
         discriminates the two live possibilities and a pass/fail count cannot: was the
         `/login` hop a client-side route guard, or a document load? Did `/auth/status`
         return something different from the mock?
      2. If it is shared-server auth state, the fix is isolation, not tolerance — either a
         per-worker server, or a fixture that asserts the server's auth posture is
         unchanged at test start.
      3. **Frequency: 1 occurrence in 1,136 test executions.** A second full-suite run with
         `--trace=retain-on-failure` came back **552 passed / 0 failed / 16 skipped, exit
         0** — the whole suite green on both browsers, and this test among them. So it did
         not reproduce, no artifact was captured, and the rate is at most ~0.1% of runs of
         this test.

         That changes the priority but not the conclusion. It is rare enough that it should
         **not** block calling the suite green, and rare enough that hunting it by repeated
         full-suite runs is poor value. The right trigger is opportunistic: the next time
         CI or a local full run goes red on this test, **read
         `test-results/*book-detail*/error-context.md` before doing anything else** — that
         is the artifact that was lost the first time and it is what discriminates the
         remaining possibilities.

      **Do not** add a retry, a URL tolerance, or a `test.fixme` to this test on the
      strength of "it passes alone."

## ITEM L5722 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **Change Log rows lost their visible "Compare snapshot" affordance and are
      mouse-only.** `web/src/components/ChangeLog.tsx:135-154` renders each entry as a
      plain `<Box onClick={...}>` that fires `onCompareSnapshot` for `tag_write` /
      `metadata_apply` entries. There is no `role`, no `tabIndex`, no keyboard
      handler, and no label — the old "Compare snapshot" link that used to sit in the
      row was removed. The flow itself still works end-to-end (verified in
      `web/tests/e2e/files-history.spec.ts`: clicking the row does raise
      `snapshot-comparison-banner` in the open format tray), so this is purely a
      discoverability/accessibility gap, not a broken feature. Deciding what replaces
      it is a product call: restore a visible link/button, or keep the row click and
      give it `role="button"` + `tabIndex={0}` + Enter/Space activation + an
      `aria-label`. Note the row already contains a Revert `<Button>` that calls
      `stopPropagation`, so any keyboard handler has to not double-fire there.

## ITEM L5736 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **Dead `expanded` state in `TagComparison`.** `web/src/components/TagComparison.tsx:69`
      is `useState(true)` and `setExpanded` is never called, so the `<Collapse in={expanded}>`
      at line 249 is always open. Either drop the state and the `Collapse`, or wire up the
      toggle that was evidently intended (the e2e suite still had a `tag-comparison-toggle`
      testid assertion for it until 2026-08-09).

## ITEM L5742 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **Delete the unreachable "Bulk Fetch Metadata" dialog and its handler.**
      `web/src/components/library/LibraryDialogs.tsx:920` renders
      `<Dialog open={bulkFetchDialogOpen}>`, but `setBulkFetchDialogOpen(true)` appears
      **nowhere** in `web/src` — the state is initialised to `false` at
      `web/src/pages/Library.tsx:352` and is only ever set back to `false` (by
      `handleCancelBulkFetch`). The dialog can never open. `handleBulkFetchMetadata`
      (`Library.tsx:1218`), the `bulkFetchProgress` state, and the props threaded
      through `LibraryDialogs` for them are reachable only from that dead dialog.
      The flow it belonged to was replaced: **Fetch Selected** now calls
      `batchFetchCandidates` and toasts "Click Review when complete", and a separate
      **Review** button opens the candidates dialog once the cache is populated. Five
      e2e tests covering the old synchronous progress dialog were deleted on
      2026-08-09 rather than rewritten, since rewriting them against the new
      async flow would be new coverage rather than repair. Removing the dead code is
      a separate change from the e2e repair and was deliberately not bundled with it.

## ITEM L5758 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **Audit `setupMockApi` for more branches shadowed by earlier prefix catch-alls.**
      `web/tests/e2e/utils/test-helpers.ts` had `pathname === '/api/v1/audiobooks/batch'`
      sitting *below* `pathname.startsWith('/api/v1/audiobooks/') && method === 'POST'`,
      so every batch update silently got the generic `{ message: 'OK' }` back and
      Library's toast read "Updated metadata for 0 audiobooks." Fixed 2026-08-09 by
      moving the specific branch above the prefix one, but the same ordering hazard
      applies to every other `startsWith` catch-all in that dispatcher — a specific
      branch placed after one is dead and fails silently rather than loudly. Worth one
      pass to confirm no others are shadowed.

## ITEM L5904 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **You cannot sort the library from the UI.** The "Sort by" and "Order"
      comboboxes are gone. `SearchBarProps`
      (`web/src/components/audiobooks/SearchBar.tsx:124-131`) has no `onSortChange`
      prop at all, and `web/src/components/library/LibraryBookGrid.tsx:133` receives
      the handler as `_handleSortChange` — underscore-prefixed to mark it deliberately
      unused. Everything downstream still works: `Library.tsx` holds `sortBy`/`sortOrder`,
      writes them to the URL as `sort`/`order`, restores them on load, and passes them
      to the API. So sorting is fully functional and completely unreachable — the only
      way to change it is to hand-edit the URL.
      `SearchBar.test.tsx:43` asserts "does not render sort controls when `onSortChange`
      is absent", which now passes vacuously since the prop cannot be supplied.
      Four `library-browser.spec.ts` tests were repointed at the URL on 2026-08-09 so
      the sort *behaviour* stays covered while the control is missing.
      **Was this intentional?** If so the dead state and the vacuous unit test should
      be cleaned up; if not, the control needs restoring.

## ITEM L5972 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **Per-field "Use File" / "Use Fetched" one-click apply is gone from Book Detail
      — confirm that was intended.** `web/src/pages/BookDetail.tsx:1014-1015` now renders
      exactly two tabs (Info, Files & History). The old Tags/Compare tab listed every
      metadata field as a row with one-click **Use File** and **Use Fetched** buttons,
      each showing its own inline "Applying…" spinner while only that field's write was
      in flight. Neither string appears anywhere in `web/src` today. Fetched values are
      still *surfaced* — `MetadataEditDialog.tsx:188-198` labels a field's source as
      "Fetched" and pre-fills from `fetched_value` — but applying one now means opening
      the dialog and saving the whole form, so there is no way to accept a single fetched
      field. Two e2e tests covering the old flow were deleted on 2026-08-09 rather than
      left permanently skipped. If the loss was unintentional, this is the third
      capability this session's e2e sweep has found missing from Book Detail (the others:
      version management, and the Change Log "Compare snapshot" link — see
      `todo.d/20260809-changelog-row-compare-affordance.md`).

## ITEM L5987 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **Visual-regression goldens exist only for darwin.**
      `web/tests/e2e/dynamic-ui-interactions.spec.ts-snapshots/` holds
      `scan-button-loading-chromium-darwin.png` and `-webkit-darwin.png` and nothing for
      linux, so `Button loading states visual check` cannot pass on CI runners — it will
      report a missing snapshot. The chromium-darwin golden was regenerated 2026-08-09
      after the spinner was masked; the **webkit-darwin one is now stale** and could not
      be regenerated locally because the webkit browser is not installed on this machine
      (`npx playwright install webkit`). Either commit linux goldens generated in CI, or
      scope this test to a single platform so it stops being a permanent red on the
      nightly e2e workflow.

## ITEM L6133 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: internal/search | all_domains_guess: internal/search;docs

- [ ] **The checked-in `.api-token` no longer authenticates, and it blocked a real
      verification.** Found 2026-08-09 while grounding
      `docs/design/2026-08-09-search-backend-options.md`.

      `.api-token` (the shared per-worktree API key created by the `server-bootstrap`
      skill and documented in `CLAUDE.md`) returns:

      ```
      {"error":"invalid session","code":"UNAUTHORIZED","status":401}
      ```

      while `/api/v1/health` returns 200 — so the server is up and it is the credential
      that is stale, not the endpoint. The file dates from 2026-07-14.

      **Why this matters beyond convenience.** It blocked a specific question that is worth
      answering: **is the Bleve search index complete?** The engine is confirmed *open* in
      production (`msg="Search index opened"` on the current process and every restart back
      to Aug 07), but an index that opens fine while missing books produces confidently
      wrong results. The other route to that answer — reading the index directory — needs
      root, and `sudo` on the prod host requires interactive authentication.

      **Do:**
      1. Regenerate `.api-token` via the documented bootstrap path.
      2. With it, compare a broad search's result count against the same term reached
         through a filter-only path. A large gap means index drift.
      3. Consider whether a *silent* search degradation is acceptable: `Open()` failures
         are downgraded to warnings so the server boots without search
         (`internal/search/register.go`), so a fallback to the O(N) substring scan would
         run indefinitely with only a startup warning to show for it. With `/metrics`
         currently unscraped (see the Prometheus gap), nothing would surface it. That is
         the same failure shape as the six e2e specs that sat disabled for four months.

      See §6 Q1 of `docs/design/2026-08-09-search-backend-options.md`.

## ITEM L6241 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **You can no longer navigate between versions of a book.** Book Detail used to
      have a "Versions" tab listing the group's other versions, each clickable to jump
      to it. `web/src/pages/BookDetail.tsx:1014-1015` now renders only Info and
      Files & History, and `BookDetailVersionGroup.tsx` contains no `RouterLink` — the
      version titles are plain text. `VersionManagement.tsx` (the dialog) has no
      `navigate()` call either. The only per-version action left is
      **"Move to: \<title\>"** (`BookDetailVersionGroup.tsx:457-464`), which moves
      *files* between versions — a destructive operation, not navigation, sitting where
      users previously clicked to browse. Getting from the M4B to the MP3 of the same
      book now means going back to the library and finding the other card.

## ITEM L6252 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **The version-group summary lost its count and its "you are here" marker.**
      `Part of version group with N books.` and `(Current)` appear nowhere in `web/src`.
      All that survives is a bare **"Version Group Linked"** chip
      (`BookDetailHeader.tsx:172`) — it tells you a group exists but not how big it is
      or which member you are looking at.

## ITEM L6258 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend)

- [ ] **The library card's overflow menu button has no accessible name.**
      `web/src/components/audiobooks/AudiobookCard.tsx:183` is an `IconButton` with only
      a `<MoreVertIcon/>` inside — no `aria-label`, no tooltip. Screen readers announce
      it as an unlabelled button, and it is now the **only** route to Manage Versions,
      Edit, Fetch Metadata and Parse with AI. The e2e suite has to locate it via
      `button:has([data-testid="MoreVertIcon"])` because there is nothing else to match on.

Context: `version-management.spec.ts` was repointed at the surviving entry point on
2026-08-09 (4 of 6 tests). The two covering navigation and the group summary were
deleted rather than rewritten, since the capabilities themselves are gone. Related:
`todo.d/20260809-changelog-row-compare-affordance.md`,
`todo.d/20260809-per-field-use-fetched-affordance.md`.

## ITEM L6425 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

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

## ITEM L6484 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **`version-management.spec.ts` (6 failures) — version management MOVED off
      the book detail page. The spec needs a rewrite, not a selector tweak.**
      Fully diagnosed 2026-08-09; no code changed, because the fix is a real
      rewrite and a half-finished one is worse than none.

      **What the tests do:** `openBookDetail()` navigates to `/library/<id>`
      and each test then clicks `getByRole('button', { name: 'Manage
      Versions' })` to open the linking UI.

      **What the app does now:**

      - `pages/BookDetail.tsx` does **not** import `VersionManagement` at all.
        It renders `components/bookdetail/BookDetailVersionGroup.tsx`, which is
        **read-only** — Bitrate, Duration, File, Origin, Path, Sample Rate,
        Size. There is no link/unlink affordance on book detail.
      - The interactive `VersionManagement` component is rendered from
        `components/library/LibraryDialogs.tsx` and `pages/Library.tsx` — i.e.
        from the **Library** page.
      - "Manage Versions" is a **MenuItem inside the card's overflow menu**
        (`components/audiobooks/AudiobookCard.tsx:336`), so its role is
        `menuitem`, not `button`, and the menu must be opened first.

      So the tests are driving a capability that page no longer has. The book
      detail header still shows a "Version Group Linked" chip, which is why the
      page *looks* right in the snapshot — it displays version state but cannot
      change it.

      **The rewrite:** point `openBookDetail()` (5 call sites) at `/library`,
      open the target card's overflow menu, then click the **menuitem**
      "Manage Versions". The dialog interactions after that point are likely
      still valid, since `VersionManagement.tsx` itself was not replaced — only
      relocated.

      **Worth asking before doing it:** is losing version management from book
      detail intentional? Managing versions of the book you are looking at is a
      natural place for it, and it now requires going back to the library and
      finding the card. That is a product question, not a test question.

## ITEM L6701 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: cmd/root.go | all_domains_guess: cmd/root.go;internal/database;docs

- [ ] **⚖️ DECIDE which sort indexes to enable — the design-doc cost estimate was ~10×
      optimistic.** The machinery is built, tested and merged behind
      `enabled_sort_indexes`, defaulting to empty (today's behaviour exactly). What is
      left is choosing what to turn on, and that needs the real number rather than the
      one the decision was originally made on.

      ## What was decided, and on what basis

      On 2026-08-09 the owner selected nine sort fields to index — author, narrator,
      series, created_at, updated_at, year, duration, file_size, bitrate — from a design
      doc that estimated **"tens of MB per sort field"** against ~1.25 GB resident, i.e.
      "low single-digit percent each".

      ## What it actually costs

      Measured, 100,000 books, identical fixture on both sides
      (`TestSortIndexCost`, `internal/database/memdb_sort_index_cost_test.go`):

      | | without | all nine | delta |
      |---|---|---|---|
      | heap per book | 2,645 B | 6,395 B | **+142%** |
      | at 366,916 books | 925.6 MB | 2,237.8 MB | **+1,312 MB** |
      | insert 100K | 335 ms | 935 ms | **2.8× slower** |

      That is **~146 MB per sort key**, not "tens of MB". memdb is already ~1.25 GB
      resident with a 107.9 s warmup, so all nine roughly doubles it.

      **And this is a LOWER bound.** The fixture leaves `Author` and `Series` unset, so
      two of the six physical indexes store the 1-byte "missing" key for nearly every
      row. A library with populated author/series data pays more than this.

      ## Why the estimate was wrong

      The doc reasoned that "a secondary index stores keys and IDs, not books", which is
      true and led to sizing by key length. But go-memdb is an **immutable** radix tree:
      every insert path-copies the nodes from root to leaf. Cost is dominated by node
      allocation, so a short key is not a cheap key. Roughly 417 B per book per index
      regardless of what the key contains.

      ## The decision

      Not "should we index" — the pagination-disabled full-set sort is genuinely bad.
      It is **which fields earn ~146 MB each**, and there is no usage data to answer it:
      nobody has measured which sorts real users pick. Options, cheapest first:

      1. **Enable none for now** (current default). Costs nothing, changes nothing.
      2. **Instrument first** — log `sort_by` values for a week, then enable only the
         fields that actually appear. This is the option that replaces a guess with
         evidence, and the instrumentation is small.
      3. **Enable a chosen subset.** `created_at`/`updated_at` are the most likely to be
         worth it ("what's new" is a real browsing pattern); the numeric triage fields
         (duration/file_size/bitrate) are plausibly rare enough to leave on the slow path.
      4. **Enable all nine** and accept ~2.5 GB resident. Only with headroom confirmed on
         the host, and re-measure warmup — 107.9 s is already not short.

      After enabling anything: re-measure warmup and RSS on prod, because the
      extrapolation from 100K is linear-by-assumption and 366,916 is 3.7× further out.

      ## Also worth knowing

      `CanPushDownSort` consults the **enabled** set, not the known set, so a field that
      is not indexed correctly falls back to the existing path instead of asking memdb
      for an index that was never registered. `SetEnabledSortIndexes` must be called
      before the store opens — it is, from the single `cobra.OnInitialize` hook in
      `cmd/root.go`.

      Related: `docs/design/2026-08-09-search-backend-options.md` §2.3 (which still
      carries the old estimate in its prose and should be corrected to point here).

## ITEM L6988 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Audit every e2e mock handler against whether its app-side reader
      unwraps `body.data`.** Likely the dominant cause of the 146 e2e failures,
      and a systematic fix rather than 22 separate spec refreshes.

      ⚠️ **PARTIALLY DONE 2026-08-09 — the prediction was half right.** The
      envelope gap was indeed the single most common cause and was fixed
      wherever it appeared (#2224, #2226, #2229, #2236). But it was **not** a
      systematic fix: specs that mock by patching `window.fetch` inherit nothing
      from `setupMockApi`, so each one needed its own copy. Note the two
      exceptions that bite in the opposite direction — `getBookTags` and
      `getBookExternalIDs` read the **top-level** body, so enveloping those
      breaks them.

      A related hazard found the same night and worth its own pass: a specific
      branch in `setupMockApi` placed *below* a `startsWith(...)` prefix
      catch-all is unreachable and fails **silently** with a 200. Three separate
      instances existed (`/audiobooks/batch`, `/audiobooks/<id>/files`,
      `/authors*`). See `todo.d/20260809-dead-bulk-fetch-dialog.md`.

      **Confirmed for `dedup.spec.ts` (26 failures, the largest single file).**
      `src/services/api.ts:1402` reads:

          const body = await response.json();
          const data = body.data;
          return { groups: data.groups || [], ... };

      while `test-helpers.ts:825` mocks `/api/v1/authors/duplicates` as:

          jsonResponse({ groups: dedup.groups, needs_refresh: ... })

      — **unwrapped**. So `body.data` is `undefined`, the page renders zero
      groups, and every assertion looking for an author heading fails. The spec
      itself is fine; it passes real fixture groups in.

      **Why this is probably not just dedup.** Wave 2 (#2191) fixed exactly this
      envelope for **eight** endpoints — `/auth/status`, `/import-paths`,
      `/authors`, `/series`, `/audiobooks/soft-deleted`, bare `/audiobooks/:id`,
      `/audiobooks/:id/versions`, `/filesystem/*` — and those spec files are now
      green. But **80 endpoints in `api.ts` unwrap `body.data`**. Eight are
      covered. The remaining ~72 are unaudited, and `/authors/duplicates` being
      broken is the first one anybody checked.

      **⚠️ Confidence, stated honestly.** The dedup cause is *verified* by
      reading both sides. The claim that this explains the other 21 files is
      *plausible and unverified*. This estimate has already been revised twice
      — first "a few cascading root causes", then "22 files of independent
      drift" — so verify a second and third file before planning around it.
      Cheapest check: pick a failing test in `library-browser.spec.ts` (14) and
      `metadata-provenance.spec.ts` (12), find the endpoint its page calls, and
      compare the mock's shape to the reader's.

      **Suggested approach if it holds:** rather than hand-patching handlers one
      at a time, make the envelope the default. A single helper — e.g. wrap
      every `jsonResponse` body as `{ ...body, data: body }` unless the handler
      opts out — matches what wave 2 already did piecemeal in
      `test-helpers.ts` and would cover all ~72 at once. Opt-out matters:
      endpoints that legitimately return bare arrays or non-envelope shapes must
      not be double-wrapped.

      **Do not skip or delete failing specs to make this go away.** Six files
      were disabled-by-accident for four months and that is the incident this
      entire thread exists to prevent.

## ITEM L7254 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **The unified Dedup view has no e2e coverage at all.** Found 2026-08-09
      while repairing `dedup.spec.ts`.

      `/dedup` was redesigned: it now renders a unified candidate surface
      (bands All / Certain / High / Medium / Review, a candidate table, "Find
      Duplicates" / "Rescore" / "Force Full Rescan" actions). The old tabbed
      Books / Authors / Series UI still exists but sits behind a **"Legacy
      View"** toggle persisted as `sessionStorage.dedup_show_legacy`.

      Every test in `dedup.spec.ts` covers the **legacy** view — they now opt
      into it explicitly via `enableLegacyDedupView()`. That was the right fix
      (the legacy features are still shipping and were previously untested by
      accident), but it means the surface a user actually lands on has **zero**
      automated coverage.

      Worth covering: band filtering, the candidate table, merge/dismiss
      actions, and the legacy toggle itself round-tripping through
      sessionStorage.

      Note the gap was invisible for the same reason the last one was: the
      specs did not fail with "this UI no longer exists", they failed with
      `element(s) not found`, which reads identically to a broken selector.

## ITEM L7404 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Stop Deluge writing in-progress downloads directly into the new-books
      import directory.** A torrent that is still downloading is visible to the
      scanner as a book, so a partial file gets imported as if it were complete:
      wrong duration, wrong file size, a truncated or absent intro clip, and a
      transcription/fingerprint pass that runs against bytes which will change
      underneath it.

      Fix: give Deluge a staging directory OUTSIDE the watched tree and have it
      **move** the completed torrent into the import directory only on
      completion. A move within the same filesystem is an atomic rename, so the
      scanner can never observe a half-written book. A copy across filesystems
      is NOT atomic — if staging and import must live on different filesystems,
      copy to a dotfile/temp name inside the import dir and rename into place as
      the final step.

      Deluge supports this natively: set "Download to" = staging path and "Move
      completed to" = import path.

      Also worth adding as defence in depth, since Deluge is not the only way
      files arrive:
      - Scanner ignores partial-download suffixes (`.part`, `.!ut`, `.tmp`) and
        dotfiles.
      - Quarantine a candidate whose size or mtime changed between the scan and
        the import rather than importing it.

      🔴 Suspected to be a real source of existing bad rows — worth measuring how
      many books have a duration or file size inconsistent with their format
      before assuming this is only a forward fix. Silently-truncated books would
      also explain some fraction of the `[SILENCE]` sentinels and short/failed
      intro transcriptions.

## ITEM L7435 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: (unresolved) | all_domains_guess: (unresolved)

- [ ] **Require every operation to support `dry_run`, and enforce it at the
      registry rather than by convention.** Any op that mutates state must be
      runnable in a mode that computes and reports exactly what it WOULD do,
      writing nothing — so it can be tested independently and reviewed before it
      touches prod.

      **Motivating case (2026-08-07).** Three maintenance ops were run in one
      session and they did not agree with each other:

        maintenance.repair-transcribe-status      dry_run, defaults TRUE
        maintenance.intro-migrate-single-file     dry_run, defaults TRUE
        maintenance.transcribe-book-intros        NO dry_run at all

      The first two could be previewed, reconciled bucket-by-bucket against the
      full book count, and gated on real numbers. The third — a reparse that
      rewrites parsed title/author/narrator across the library — had no preview
      mode whatsoever: dispatching it IS applying it. The only reason that was
      acceptable was an unrelated internal guard (reparse only ever upgrades),
      which is luck, not design.

      **What "supported" should mean** — a bare `dry_run` bool is not enough:

      - **Declared, not optional.** Put it on `OperationDef` (e.g.
        `SupportsDryRun bool`, or better, make the param struct embed a shared
        `DryRunParams`). An op declaring `CapLibraryWrite` without dry-run
        support should fail registration, so the gap is caught at startup rather
        than discovered while someone is deciding whether to hit apply.
      - **Default TRUE for destructive ops.** Both ops that had it defaulted to
        dry-run; that is the right default and should not be per-author choice.
      - **Report per-reason counts that RECONCILE.** The value of the two
        previewable ops was that every item landed in exactly one labelled
        bucket and the buckets summed to the population — 11,315 + 19,505 + 0 +
        12,587 + 1,463 + 7 = 44,877 exactly. "would change 30,820" with no
        account of the rest is the shape of report that hides a bug. Consider a
        shared result type so this is structural rather than remembered.
      - **Same code path.** The dry run must execute the identical decision
        logic and diverge only at the write, or it is testing something other
        than what will run. Both existing ops do this correctly (classify, then
        branch on `dryRun` immediately before the store call) — that pattern is
        the one to generalise.

      Related: the write-set/scheduler-conflict work
      (`OperationDef.Writes []Resource`). Both are the same idea — an op should
      DECLARE what it does, and the system should enforce it, instead of every
      author re-deciding by hand.

## ITEM L7549 [tier C] section: Missing-file lane — follow-ups after the report-only change (#2614)
primary_domain_guess: web (frontend) | all_domains_guess: web (frontend);docs

- [ ] **TODO-MUI-1** MUI upgrade Step 1 — `@mui/*` 5.14 → 6.x (brief:
      `docs/plans/2026-08-07-mui-upgrade-path.md`; requires TODO-MUI-0 merged;
      do NOT continue to v7 in the same session/PR)
  - `cd web && npm install @mui/material@6 @mui/icons-material@6`
  - Codemods (run from repo root):
    `npx @mui/codemod@latest v6.0.0/sx-prop web/src` and
    `npx @mui/codemod@latest v6.0.0/theme-v6 web/src/theme.ts`.
    Skip `v6.0.0/grid-v2-props` (we have zero Grid2 — legacy Grid stays as-is
    until v7) and `v6.0.0/list-item-button-prop` (0 `<ListItem button` measured).
  - Expected hand-fixes (from the 2026-08-07 inventory):
    - Test churn from the v6 ripple rework: `fireEvent` interactions on
      Button/Checkbox/Chip/Radio/Switch/Tabs may need
      `await act(async () => fireEvent...)` — fix failing Vitest specs, don't
      skip them.
    - `Typography color=` (405 usages): palette tokens keep working; audit
      only non-palette CSS values (move those into `sx`).
    - Accordion summary now renders a heading/button — check
      `grep -rln Accordion web/src` sites for snapshot/E2E fallout.
  - Do NOT adopt Pigment CSS; Emotion remains the engine.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke of Library, Book Detail, Activity Log, System > Maintenance,
    Dedup tabs; note (don't fix) new MUI deprecation warnings in the PR body.
  - Rollback: `git revert` of this single PR (lockfile reverts with it).

