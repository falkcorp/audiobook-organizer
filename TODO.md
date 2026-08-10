<!-- file: TODO.md -->
<!-- version: 10.30.0 -->
<!-- guid: 8e7d5d79-394f-4c91-9c7c-fc4a3a4e84d2 -->
<!-- last-edited: 2026-08-10 -->

# Project TODO — live items only

## 📥 Inbox

Tasks assembled from `todo.d/` fragments. Add a new task by dropping a fragment
file in `todo.d/` rather than editing this section by hand — see
[`todo.d/README.md`](todo.d/README.md). Checking a task off, or promoting it
into one of the curated sections below, is a normal direct edit.

<!-- todo-insert-here -->

- [ ] **AudiobookShelf-compatible API: series are broken, and collections/playlists are
      empty stubs.** Owner report 2026-08-09: *"series are broken on the audioshelf server
      stuff, because all of them report zero books, and when you click on them they just
      give you a random list of books… We need full collection support… Same with
      playlists."* Root causes located in the code below — this is server-side, as the
      owner suspected.

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
      **dynamic** (stored-query) playlists, and their value as grouping evidence. **This
      item is narrower and additive:** whatever that work builds must also be *served over
      the ABS API*, because today the endpoint returns empty regardless of what exists
      internally. Do not duplicate the design — extend it with the API surface.

      ## Shared design note

      Collections and playlists are close cousins (an ordered set of items with a name) and
      the ABS schema treats them similarly. Worth designing the storage once with a
      discriminator rather than twice — but **check the ABS DTOs first**, because clients
      distinguish them (playlists carry playback semantics, collections do not) and
      returning the wrong shape produces exactly the class of silent client-side weirdness
      seen in §1.

      **Acceptance:** in a real ABS client — series show correct counts and open their own
      books; a hand-made collection appears and lists its members; a playlist likewise.
      Verified in the client, not by curling the endpoint.

- [x] **CORRECTED and FIXED — this was reported as an active crash and it was not.**

      ## What the original entry claimed

      > The Authors page crashes on any author record without `aliases`. `Authors.tsx:89`,
      > `:120`, `:121` read `a.aliases.length` unguarded — one bad row takes the whole page
      > to the error boundary. **Reachable from a real API response that omits or nulls the
      > field.**

      The first half is true. **The last sentence is not, and it is the part that made this
      read as urgent.**

      ## What is actually the case

      `Authors.tsx` fetches from exactly one place — `api.getAuthorsWithCounts()` — and the
      handler behind it has guarded the field since **2026-03-10**, five months before this
      was filed (`internal/audiobooks/author_series.go:108`):

      ```go
      aliases := aliasesByAuthor[a.ID]
      if aliases == nil {
          aliases = []database.AuthorAlias{}   // never marshals to null
      }
      ```

      A Go nil slice marshals to JSON `null`, and `null.length` throws — so the concern was
      the right shape. But the only endpoint feeding this page has been returning `[]`
      rather than `null` all along. **The page was not crashing, and there was no "real API
      response" that would make it crash.**

      The original entry was written from reading the frontend and reasoning about what the
      backend *might* send, without checking what it does send. That is the same
      reason-instead-of-measure error that produced four wrong diagnoses during the
      2026-08-09 CI work.

      ## What was still worth fixing

      The frontend fragility is real even though nothing currently triggers it. TypeScript's
      `aliases: AuthorAlias[]` is a **compile-time claim about runtime data from an HTTP
      response** — it validates nothing. One new endpoint returning `AuthorWithCount`
      without that nil guard, or one API shape change, and the page dies at the error
      boundary.

      So the six reads in `Authors.tsx` are now guarded (`a.aliases?.length ?? 0`,
      `(a.aliases ?? []).map(...)`, etc.). Behaviour is identical when the field is present,
      which it always is today.

      ## Corrected elsewhere

      The overstated claim also appears in `docs/audits/2026-08-09-e2e-repair-and-ui-regressions.md`
      (finding 3) and the 2026-08-09 executive summary ("a page that crashes outright if a
      single author record is missing one optional field"). Both are corrected in the same
      change.

      **The lesson worth keeping:** "unguarded field access" is a real code smell, but
      "therefore it crashes" is a claim about the *server*, and needs the server checked.
      Severity asserted from one side of an API boundary is a guess.

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

- [ ] **Dead `expanded` state in `TagComparison`.** `web/src/components/TagComparison.tsx:69`
      is `useState(true)` and `setExpanded` is never called, so the `<Collapse in={expanded}>`
      at line 249 is always open. Either drop the state and the `Collapse`, or wire up the
      toggle that was evidently intended (the e2e suite still had a `tag-comparison-toggle`
      testid assertion for it until 2026-08-09).

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

- [ ] **Audit `setupMockApi` for more branches shadowed by earlier prefix catch-alls.**
      `web/tests/e2e/utils/test-helpers.ts` had `pathname === '/api/v1/audiobooks/batch'`
      sitting *below* `pathname.startsWith('/api/v1/audiobooks/') && method === 'POST'`,
      so every batch update silently got the generic `{ message: 'OK' }` back and
      Library's toast read "Updated metadata for 0 audiobooks." Fixed 2026-08-09 by
      moving the specific branch above the prefix one, but the same ordering hazard
      applies to every other `startsWith` catch-all in that dispatcher — a specific
      branch placed after one is dead and fails silently rather than loudly. Worth one
      pass to confirm no others are shadowed.

- [x] **FIXED (#2267).** **Edit Metadata shows Year and ISBN-13 as empty boxes whatever is stored — and the
      obvious fix corrupts `print_year`.** `mapBookToAudiobook`
      (`web/src/pages/BookDetail.tsx:762`) builds the object handed to
      `MetadataEditDialog` and omits `year`, `isbn10` and `isbn13`. `genre` had the same
      problem and was fixed on 2026-08-09; the other three were deliberately left alone,
      because they are not equally safe.

      `genre` was safe because it does not appear in the payload `handleEditSave` builds,
      so populating it cannot change what a save writes. **Year is not.** The dialog seeds
      its Year box from `audiobook.year`, and `handleEditSave` computes:

      ```ts
      payload.print_year = updated.year || book.print_year;
      ```

      So mapping `year: current.audiobook_release_year` would make every save overwrite
      `print_year` with the audiobook release year — on books the user never touched the
      Year field of. Two genuinely different dates (`print_year`, the original
      publication; `audiobook_release_year`, when the recording came out) collapsing into
      one is silent metadata corruption across the library.

      Fixing the display therefore means untangling that precedence first: decide which
      date the dialog's single "Year" box represents, and have the save path write only
      that one. `Audiobook` already carries `print_year` and `audiobook_release_year` as
      separate fields (`web/src/types/index.ts:16-17`) alongside the legacy `year`, so
      the type is not the obstacle.

      ISBN is a smaller version of the same shape: the payload does
      `isbn: updated.isbn13 || updated.isbn10 || book.isbn`, which currently falls through
      to `book.isbn` precisely *because* the mapped object has neither. Populating them
      changes which field wins.

      `tests/e2e/metadata-provenance.spec.ts` carries a `test.fixme` covering this, so it
      will start failing (loudly, as an unexpected pass) the moment it is fixed.

      > ### What it actually was — worse than a blank box
      >
      > The dialog has ONE "Year" box, declared as `audiobook_release_year` in
      > `FIELD_TO_API`. But `handleEditSave` fed `updated.year` into **two** fields:
      >
      > ```ts
      > audiobook_release_year: … || updated.year || …,
      > print_year:             updated.year || book.print_year || undefined,
      > ```
      >
      > `print_year` is when the **book** was first published; `audiobook_release_year` is
      > when the **recording** came out — decades apart for a classic. So typing a year in
      > that dialog silently replaced the original publication year with the audiobook's.
      > Same corruption class as the 2026-07-13 write-up, still live on this path. The blank
      > box masked it for *display* but not for *writes*.
      >
      > Fixed in the safe order: remove the bad write first (`print_year` is now
      > preserve-only — the dialog has no print-year field, so nothing there should change
      > it), which then makes seeding the box safe. Doing it the other way would have turned
      > a latent corruption into one firing on every save.
      >
      > **And the blank box had a second, separate cause:** the e2e fixture supplied
      > `year: 2024`, a field the Go API never emits (`bookcore.go:44-45` has `print_year`
      > and `audiobook_release_year` only). The dialog was correctly reading
      > `audiobook_release_year` and finding nothing. Mock rot, not app behaviour.
      >
      > `test.fixme('year and ISBN-13 populate in the edit dialog')` is now passing:
      > metadata-provenance 13 passed / 0 failed / 0 skipped, exit 0.

- [x] **The Library fetched page 1 twice on every mount — FIXED.** Found 2026-08-09 while
      chasing three flaky `library-browser.spec.ts` pagination tests on webkit. On a large
      library that is a second full page query for nothing, on every single load.

      **Cause.** `SearchBar` re-parsed its value on mount and handed back a NEW
      `ParsedSearch` object that was semantically identical to the one `Library.tsx` had
      seeded its state with. Storing it changed `parsedSearch`'s *identity* → recreated
      `buildFieldFilters` → recreated `loadAudiobooks` → re-fired the "load when filters
      change" effect. Confirmed by instrumenting the dependency array:
      `DEPCHANGE buildFieldFilters,parsedSearch` fired once after mount on 4 runs of 4, and
      stopped firing entirely once the setter bailed on an unchanged value.

      **Fix** (#2241): `Library.tsx` now wraps the setter so an equal value keeps the
      previous object reference. `/api/v1/audiobooks` is hit exactly once per mount, 4 runs
      of 4.

      This belongs with the other client-side over-fetching in
      `todo.d/20260809-search-drops-filters-and-debounce.md` — that one is ten queries per
      search, this was two per page load.

      ---

      **TWO CORRECTIONS, both worth reading — this fragment was wrong twice.**

      **Correction 1.** The original fragment claimed the duplicate fetch *caused* the
      swallowed pagination click: the re-render from the second response was said to detach
      the button mid-click. Measured like-for-like over 24 webkit runs of the same three
      tests, eliminating the duplicate moved the failure rate **16/24 → 11/24**. A real
      improvement, but the flake survived, so the causal claim was at best incomplete.

      **Correction 2 — and this is the one that matters.** The residual flake is **not a
      product defect at all.** It is a Playwright/webkit harness artifact. Five probes:

      | probe | result |
      |---|---|
      | failing run instrumented | previous-page click → no request, no URL change (swallowed) |
      | click twice | first swallowed, second works — looked like a stale closure |
      | same probe on chromium | first click works; webkit-specific |
      | Playwright click vs in-page DOM `.click()` | **Playwright 4/4 failed, DOM 4/4 passed** |
      | both clicks via in-page DOM | **6/6 clean** |

      The application handles pagination correctly. Playwright's *synthesised pointer
      event* on MUI's `PaginationItem` is what is unreliable on webkit — the same DOM, the
      same handlers, the same React state, driven by a real DOM click, works every time.

      **So the tests were made robust** (`clickPagination` helper in
      `library-browser.spec.ts`): re-check the URL, click, assert, and retry once. This does
      **not** violate the no-papering-over rule, because the measurement establishes there
      is no product bug to paper over. Result: **24/24 on webkit**, against an 11/24 failure
      baseline.

      **What the helper does NOT protect against — stated plainly, because the first draft
      of this fragment claimed the opposite.** It originally said the helper "still fails
      loudly if the app ever genuinely stops responding, since the second click would be
      swallowed too." That is **false**, and falsified by the very probe above: the observed
      webkit signature is *first click swallowed, second click works*. A product regression
      with that same shape — pagination responding only on every other click — would be
      silently masked. The helper is not a detector for that case.

      What it does still catch: a control that is missing, disabled, covered, or entirely
      unresponsive, since both clicks fail and actionability checks are deliberately
      preserved (no `force: true`, no `dispatchEvent`). And to keep the masking observable
      rather than invisible, **every retry logs a `[clickPagination]` warning and pushes a
      `flaky-click-retry` annotation** — so a rising retry rate in CI is the signal that
      something changed, even though the suite stays green.

      **The lesson, which is the reusable part:** three separate causal hypotheses were
      filed here with confident evidence attached, and two of them were wrong. Each was
      killed by a measurement, never by reasoning. Before filing a UI flake as a product
      defect, do the Playwright-click-vs-DOM-click A/B — it is two minutes and it separates
      "the app is broken" from "the driver cannot press this particular button."

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

- [x] **RESOLVED — it was worker contention, not a defect.** This fragment previously
      claimed "a MUI Select's menu does not close on the ubuntu runner — suspected REAL
      defect". **That was wrong.** Kept rather than deleted, because the sequence of wrong
      answers is the useful part.

      ## What it actually was

      Two chromium tests failed on ubuntu and passed on macOS, both 30s `locator.click`
      timeouts with a MUI modal backdrop still intercepting pointer events. Measured in the
      official Playwright linux image pinned to 2 CPUs:

      | configuration | result |
      |---|---|
      | `--workers=2` | `library-browser` + `scan-import` **FAIL** (3 separate runs) |
      | `--workers=1` | **27 passed, 0 failed** |

      Two browser workers plus the Go server on two cores starve the close **transition**,
      so the backdrop outlives any timeout worth setting. The menu does close; the machine
      is simply too busy to animate it. **Neither the app nor the tests are wrong** — a real
      user is not running two headless browsers on two pinned cores.

      **Fix:** `workers: process.env.CI ? 1 : 2` in `playwright.config.ts`, with the
      measurement recorded inline. Costs wall-clock (chromium ~4.5min → ~9min), which is the
      right trade for a gate meant to block merges.

      ## Four wrong answers, and what killed each

      Worth keeping, because every one of them looked well-evidenced at the time:

      | # | hypothesis | killed by |
      |---|---|---|
      | 1 | MUI close-transition race → add `waitForMenuClosed` at all 18 option sites | CI: failure count unchanged at 3, failures merely moved to the new wait |
      | 2 | The Selects are `multiple`, so the menu stays open by design | Reading `FilterSidebar.tsx` — they are single (`:143`, `:181`); the only `multiple` is `:222`, another control |
      | 3 | **"The menu never closes on linux — suspected real defect"** (this fragment's original claim) | A probe in the linux image: menu gone in <500ms and the value lands ("Stormlight Archive") |
      | 4 | The Drawer backdrop is the sole culprit → wait on `.MuiDrawer-modal` | Strict-mode violation — the sidebar renders twice, so the selector matched 2 nodes; and library-browser's blocker was the Select menu, a different overlay |

      **The lesson is about method, not MUI.** Every hypothesis came from reading a call log
      and reasoning about what *should* follow. What finally settled it was changing one
      variable and measuring: workers 2 → 1. The cheap discriminating experiment was
      available from the beginning and was reached fourth.

      **Second lesson: build the repro before iterating.** Three of the four rounds cost a
      ~6-minute CI cycle each because there was no way to run linux locally. Building that
      (Go binary compiled in a `golang` container because CGO/`libtag` blocks
      cross-compilation, then the official Playwright image) took one round and turned the
      loop into seconds. It should have come first.

      Runner script: `<scratchpad>/linux-probe.sh` — copies the tree in, `npm ci` inside
      (the host `node_modules` is a symlink to another worktree full of darwin binaries),
      starts the prebuilt binary, runs Playwright against it with `CI` unset so
      `reuseExistingServer` attaches instead of trying to `go build`.

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

- [x] **FIXED — both halves.** Typing in the library search box silently dropped every
      active filter and the sort order, and queried on every keystroke.

      > ### ✅ The debounce half of this is FIXED (#2264) — and the original diagnosis was wrong
      >
      > This fragment said the search box "is not debounced at all". **A 300ms debounce
      > existed the whole time.** What was actually happening is worse and more specific:
      > `useLibraryQuery.ts:165` reads
      >
      > ```ts
      > const searchText = parsedSearch ? parsedSearch.freeText : debouncedSearch;
      > ```
      >
      > so the moment a search parses — which is always, once you type — the debounced
      > value is **ignored** and the raw parsed value is used. `parsedSearch` also sits in
      > that hook's `useCallback` dep array, so `loadAudiobooks` was recreated per
      > keystroke. The debounce was real, correct, and **dead code on the only path that
      > matters.**
      >
      > Fixed by moving `parsedSearch` and `searchQuery` off the same 300ms timer, rather
      > than debouncing one and leaving the other raw — debouncing only the free text would
      > let it disagree with the field filters mid-flight. `SearchBar`'s own UI still gets
      > the raw value so chips react instantly; `useLibrarySelection` gets the debounced one,
      > because "select all matching" must mean the query that produced the visible rows.
      >
      > `test.fixme('search debounces input to avoid excessive requests')` is now a real
      > passing test (search-and-filter.spec.ts: 11 passed / 1 skipped, exit 0).
      >
      > ### ✅ The filter-dropping half is ALSO fixed (#2265)
      >
      > It was a **branch**, not a missing capability:
      >
      > ```ts
      > searchText
      >   ? api.searchBooksPage(searchText, itemsPerPage, offset, filters.showFailed, signal)
      >   : api.getBooks(itemsPerPage, offset, { sortBy, sortOrder, tags, libraryState, filters, ... })
      > ```
      >
      > Every option lived on the `getBooks` side only, so typing one character crossed to
      > a call that sends four parameters — dropping `library_state`, tags, field filters
      > and the sort order.
      >
      > **The server was never the problem.** `GetAudiobooks` applies the same post-filters
      > on the search path (`service_query.go:226`); it was simply never told about them.
      >
      > Fixed by collapsing the branch rather than adding nine parameters to
      > `searchBooksPage`: `getBooks` hits the same endpoint with the same
      > `is_primary_version`, so it only needed a `search` option. **One code path now** —
      > which also means a future filter cannot be added to one branch and forgotten in the
      > other, which is exactly the class of bug this was.
      >
      > `searchBooksPage` had exactly one production caller (checked); it is now
      > `@deprecated` with the reason rather than removed.
      >
      > `test.fixme('search works with other filters combined')` is a real passing test.
      > Verified: search-and-filter + library-browser, **33 passed / 0 failed / 0 skipped,
      > exit 0.**
      >
      > Lesson worth keeping: "feature X is missing" and "feature X exists but is bypassed"
      > look identical from the outside and have completely different fixes. Grep for the
      > mechanism before concluding it is absent. `useLibraryQuery.ts:192-193` branches on whether there is search text:

      ```ts
      searchText
        ? api.searchBooksPage(searchText, itemsPerPage, offset, filters.showFailed, signal)
        : api.getBooks(itemsPerPage, offset, { sortBy, sortOrder, tags, libraryState, filters, ... })
      ```

      `api.searchBooksPage` (`web/src/services/api.ts:1023-1037`) sends only `search`,
      `limit`, `offset`, `is_primary_version` and optionally `show_quarantined`. **No**
      `library_state`, **no** `filters` (author/series/genre/language), **no** `tags`,
      **no** `sort_by`. So filtering to Organized and then searching an author returns
      matches from every state — while the Filters button keeps showing its count, so the
      filter still looks applied. Same family as the Deleted-filter cache bug fixed in
      #2230: a filter that silently does nothing is indistinguishable from one that
      matched everything. Covered by a `test.fixme` in
      `web/tests/e2e/search-and-filter.spec.ts`.

- [ ] **The library search is not debounced at all.** Measured 2026-08-09: typing the ten
      characters of "Foundation" fires **ten** requests to `/api/v1/audiobooks?search=…`,
      exactly one per keystroke. The e2e test is literally named "search debounces input
      to avoid excessive requests" and asserts `<= 3`; it has been marked `test.fixme` so
      it fails loudly as an unexpected pass once a debounce lands. On a large library each
      of those is a full-text query, so this is directly relevant to the backend-filtering
      work — no amount of server-side improvement helps if the client sends ten queries
      for one search. Related: the richer-backend-filtering TODO item.

- [ ] **Replace library sorting with server-side Go sorting.** Owner decision 2026-08-09:
      *"I want the system to not suck and I want sorting replaced, and done by go."*
      Recorded in full with the code evidence in §0a of
      `docs/design/2026-08-09-search-backend-options.md`.

      ## Where sorting lives today — three places, one of them correct

      | where | what | verdict |
      |---|---|---|
      | `internal/audiobooks/service_filtering.go:130` `applySorting` | Go, server-side, over the full filtered set | **Correct.** Keep and extend |
      | `web/src/components/common/ConfigurableTable.tsx:201` | `[...rows].sort(...)` on the client | **Replace.** Sorts the *current page* |
      | `web/src/services/api.ts` `searchBooksPage` | sends no `sort_by` | **Fix.** Search drops the sort order entirely |

      **Why the client-side one is broken by design, not merely misplaced:** it sorts the
      rows already fetched. On a paginated library that is the 50 books you can see, not
      the library. "Sort by title descending" hands you the *wrong 50 books*, correctly
      ordered among themselves — which looks plausible, which is why it survives.

      ## Scope carefully — not every client sort is wrong

      There are **15** `.sort()` sites in `web/src`. Most are legitimate: a book's own file
      list by track number (`BookDetailFilesTab.tsx:250`), tag clouds by count
      (`TagCloud.tsx:76`), metadata candidates by score. Those sort complete, small,
      already-fetched sets. **The rule is: a sort over a paginated slice of the library is
      wrong; a sort over a complete set the client already holds is fine.**

      ## Two things that must land with it

      1. **Sort must be applied BEFORE pagination**, which is the same defect as filters
         being applied after pagination (§2.2 of the design doc). Moving the sort to Go
         without pushing it into the query would produce a correctly-sorted page of the
         wrong rows — a subtler bug than the one being fixed.
      2. **There is no sort control in the UI at all.** `SearchBarProps`
         (`web/src/components/audiobooks/SearchBar.tsx:124-131`) has no `onSortChange`, and
         `LibraryBookGrid.tsx:133` takes the handler as `_handleSortChange` —
         underscore-prefixed to mark it deliberately unused. The state, URL round-trip and
         API parameter all still work; only the affordance is missing. So "replace sorting"
         is partly **restore the control**, not only move the logic.
         `SearchBar.test.tsx:43` asserts the control is absent and now passes vacuously —
         that assertion has to be inverted, or it will defend the bug.

      **Acceptance:** choosing a sort reorders the whole library (verify by sorting
      descending and checking page 1 holds the true last items, not the reversed first
      page); the sort survives a search; `sort_by` appears on the request; no `.sort()`
      remains over a paginated library slice.

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

- [x] **RESOLVED — all three fixed; the PR gate now blocks.** Superseded by
      `todo.d/20260809-webkit-scan-import-drawer-backdrop.md`, which tracks the single
      remaining webkit failure. Kept for the causes, which were all different.

      **Outcome 2026-08-09, measured on the real runner:**

      | configuration | before | after |
      |---|---|---|
      | chromium (PR path) | 269 passed / 3 failed | **272 passed / 0 failed / 8 skipped** |
      | chromium + webkit | not measured | **543 passed / 1 failed / 16 skipped** |

      | # | blocker | resolution |
      |---|---|---|
      | 1 | missing linux visual golden | Generated for BOTH engines in the Playwright linux image (#2250, #2251). Also found the goldens were **Git LFS pointers** — `*.png filter=lfs` meant CI checked out a text pointer and Playwright reported "Could not decode expected image as PNG", so the test could never have passed on CI for either browser |
      | 2 | `library-browser` click timeout | **Worker contention**, not a defect. `workers: 1` on CI (#2249) |
      | 3 | `scan-import-organize` click timeout | Same cause; fixed on chromium by the same change. **Persists on webkit** → the successor fragment |

      `pull_request` trigger restored and the job made blocking on that path;
      `continue-on-error` is now conditional so the unproven both-engine configuration is
      not handed a green light it has not earned.

      ---

      **Original entry, for the record.** Measured
      2026-08-09 by dispatching the E2E workflow against current `main` — not inferred
      from the nightly, which was stale.

      **The numbers.** CI (ubuntu, chromium): **269 passed / 3 failed / 8 skipped of 280.**
      The same suite locally (macOS, chromium): **272 passed / 8 skipped of 280, 0 failed.**
      So exactly **3 failures exist only on linux.**

      | # | test | symptom |
      |---|---|---|
      | 1 | `dynamic-ui-interactions.spec.ts:449` — Button loading states visual check | `A snapshot doesn't exist at …/scan-button-loading-chromium-linux.png` |
      | 2 | `library-browser.spec.ts:382` — combines multiple filters | `locator.click: Test timeout of 30000ms exceeded` |
      | 3 | `scan-import-organize.spec.ts:259` — complete workflow: add import path → scan → organize | `locator.click: Test timeout of 30000ms exceeded` |

      **#2 and #3 are new information and the important part.** They pass on macOS and hang
      on linux. That is the whole reason this measurement was worth taking: a suite that is
      green locally is not evidence that CI is green, and this project has already been
      burned once by exactly that inference. Do NOT assume they are "just CI slowness"
      without looking — a 30s click timeout is a long time for a mocked page, and both are
      `locator.click`, which is suspicious enough to be a shared cause.

      **#1 is mechanical.** There are only two goldens in the repo, both `-darwin`
      (`scan-button-loading-chromium-darwin.png`, `…-webkit-darwin.png`), and Playwright
      fails rather than writes when `CI=true`. Generating a linux golden needs a container,
      because `playwright.config.ts`'s `webServer` builds the Go binary and that needs CGO +
      `libtag1-dev` — so it is a two-stage build (compile in a Go image, run in the official
      Playwright image), not a one-liner. Alternatively let CI produce it once and upload it
      as an artifact to be committed.

      **Two workflow defects found while measuring, worth fixing in the same PR as the flip:**

      1. **`conclusion: success` on this workflow means nothing.** `continue-on-error: true`
         makes the job succeed no matter how many tests fail. Every nightly to date reports
         green. Anyone glancing at the Actions tab would reasonably conclude the suite is
         passing — this morning's nightly reported `success` with **179 failures**. That is
         a green light attached to a red suite, which is the same shape as the incident this
         work exists to prevent.
      2. **The job name misreports what ran.** It renders
         `E2E (chromium + webkit)` for any non-`pull_request` trigger, including a
         `workflow_dispatch` with `projects=chromium`. The `projects` input *is* honoured by
         the test step — only the label is wrong. A label that does not match what executed
         is precisely how the 2026-08-08 false green was believed.

      **Order of work:** fix #2 and #3 first (they are real and may share a cause), then
      #1, then flip `continue-on-error: false` **and** restore the `pull_request` trigger in
      the same change — the workflow comment is explicit that they go together, because a
      non-blocking check people learn to ignore is worse than no check.

      **Acceptance:** a dispatched run against `main` reports 280 passed / 0 failed for
      chromium, and a PR touching `web/**` or `**.go` gets a blocking E2E check.

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

- [ ] **The version-group summary lost its count and its "you are here" marker.**
      `Part of version group with N books.` and `(Current)` appear nowhere in `web/src`.
      All that survives is a bare **"Version Group Linked"** chip
      (`BookDetailHeader.tsx:172`) — it tells you a group exists but not how big it is
      or which member you are looking at.

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

- [x] **RESOLVED — webkit was marginal on TIMING, and its own 60s budget fixed the
      class.** The nightly now blocks too; `continue-on-error` is a plain `false`.

      **Final measurement on the real runner:**

      | configuration | result |
      |---|---|
      | chromium (PR path) | **272 passed / 0 failed / 8 skipped** |
      | chromium + webkit | **544 passed / 0 failed / 16 skipped** |

      The fix was one line — `timeout: 60 * 1000` on the webkit project only, chromium
      keeping 30s — and it was chosen as the *discriminating experiment* for the
      population hypothesis rather than as a workaround. It came back green, so the
      hypothesis is confirmed: webkit had several tests close to the shared 30s limit and
      roughly one lost per run.

      **Why this is headroom and not blindness:** a genuinely broken test does not finish
      in 60s either. What changed is that a slow-but-correct one stopped being reported as
      a failure. Chromium keeps the tighter budget because it had margin to spare once CI
      dropped to one worker.

      **Cheaper than the plan this fragment originally proposed.** It suggested three
      measurement runs to characterise the failing set first; one config change answered
      the same question and fixed it. Worth remembering: when a hypothesis implies a
      one-line change, the change often IS the measurement.

      ---

      **Original entry, for the record.**

      ## The update that changes the shape of this problem

      The drawer fix landed and **worked** — `scan-import-organize.spec.ts:259` passed on
      CI. But the same both-engine run came back with an identical score and a different
      casualty:

      | run | result | which test failed |
      |---|---|---|
      | before the fix | 543 / 1 / 16 | `[webkit] scan-import-organize.spec.ts:259` |
      | after the fix (#2253) | 543 / 1 / 16 | `[webkit] itunes-bidirectional-sync.spec.ts:121` |

      Different spec file, so the fix cannot have caused it. **The conclusion: webkit under
      CI has several tests sitting close to their timeouts, and roughly one loses per run.**
      Fixing them individually is a treadmill — each fix is real, and the score does not
      move.

      **So do not chase individual webkit failures.** Find out why webkit is marginal as a
      class:

      1. **Measure the margin.** Dispatch the webkit suite on CI 3+ times and collect the
         failing set. If it is large and varies run to run, this is systemic timing, not N
         separate bugs.
      2. **Consider a per-project timeout.** The config uses one 30s `timeout` for both
         engines, but webkit is measurably slower here — chromium stopped failing at
         `workers: 1` and webkit did not. A per-project override is a one-line change that
         would settle whether headroom is all that is missing.
      3. Only then decide whether individual tests need waits.

      ## Original entry — the drawer case, now FIXED (#2253)

      **Measured on the real runner:**

      | configuration | result |
      |---|---|
      | chromium (the PR path) | **272 passed / 0 failed / 8 skipped** |
      | chromium + webkit | **543 passed / 1 failed / 16 skipped** |

      The one failure: `[webkit] scan-import-organize.spec.ts:259` — *complete workflow: add
      import path → scan → organize*. After `page.keyboard.press('Escape')` closes the
      filter drawer, the `Select All` click times out at 30s because MUI's full-page modal
      backdrop is still intercepting pointer events:

      ```
      <div class="MuiBackdrop-root MuiModal-backdrop"> from
      <div aria-hidden="true" class="MuiDrawer-root MuiDrawer-modal MuiModal-root">
      subtree intercepts pointer events
      ```

      `workers: 1` (#2249) fixed the identical failure on chromium. **Webkit is slower and
      it persists there.**

      ## 🚨 Read this before you start: the local container is NOT a valid oracle

      The linux repro container (`<scratchpad>/linux-probe.sh`, `--cpus=2`) is **harsher
      than the GitHub runner** and invents failures CI does not have. Across four runs of
      the same spec it produced four different signatures:

      | attempt | what failed |
      |---|---|
      | baseline | `:259` drawer backdrop (matches CI) |
      | after `toHaveCount(0)` fix | `:259` **plus** `:386` "Cancel Scan" — a test CI passes |
      | after re-run | `:259` plus `:390` |
      | after visible-filter fix | `:259` failing **earlier**, on the Filters button itself |

      Tuning against it is a treadmill — it was exited deliberately, not because the problem
      was solved. **Iterate against a dispatched CI run, or a container with more CPU.**

      ## Three assertion shapes already ruled out, by measurement

      Do not re-try these:

      | shape | why it fails |
      |---|---|
      | `expect(locator).toBeHidden()` | **Strict-mode violation.** Sidebar renders its content twice (temporary Drawer + permanent one), so the selector matches 2 nodes |
      | `expect(locator).toHaveCount(0)` | **Never converges.** Count sits at 2 forever — MUI keeps the backdrop MOUNTED and merely hides it |
      | `.filter({ visible: true })` + `toHaveCount(0)` | Failure moved earlier (to the Filters button) in the container; **unvalidated against CI**, so this one is "unproven", not "disproven" |

      ## Suggested next steps

      1. Re-test the `.filter({ visible: true })` variant **on CI**, not in the container.
         It is the only shape that is semantically right for a hidden-not-unmounted
         backdrop, and it was abandoned because of an unreliable oracle rather than
         evidence against it.
      2. If that is not enough, consider whether the test should dismiss the drawer by
         clicking its close control rather than pressing Escape — a more deterministic
         path than relying on a transition finishing.
      3. **Do not add a blind retry.** The app does close the drawer; the wait is legitimate
         and should assert the closed state, not paper over it.

      **When this is green, `continue-on-error` in `.github/workflows/e2e.yml` becomes a
      plain `false`** and the nightly blocks too. The conditional expression there exists
      only because of this one test.

- [ ] **`scan-import-organize.spec.ts` (7 failures) — Settings tab deep-link
      fixed, but that was NOT the blocker. Count unchanged at 7.**
      Investigated 2026-08-09.

      **Applied and kept (correct, but insufficient):** the tests navigated to
      `/settings` and immediately clicked "Add Import Path". Settings is tabbed
      now and defaults to **Library**; the button is rendered by
      `components/settings/PathsSettingsTab.tsx:229`, mounted from
      `pages/Settings.tsx:832`, i.e. only when the **Paths** tab is active.
      `tabFromHash()` (`Settings.tsx:96`) maps a URL hash to a tab index via
      `TAB_KEYS`, so `'/settings#paths'` is the app's own supported deep link.
      All four navigations now use it.

      **It did not help — still 7 failures**, all still timing out on
      `getByRole('button', { name: 'Add Import Path' })`. So the Paths tab is
      not rendering, or the Settings page is not reaching a usable state at
      all. The change is kept because it is verifiably more correct than
      `/settings`, not because it fixed anything.

      **Next step, and do this before writing any code:** capture the DOM for
      one of these failures specifically. `test-results/` was dominated by
      other tests' directories, so the Settings page snapshot was never
      actually read — which, given that reading the snapshot has found every
      real cause in this effort, is the obvious gap. Run just this spec and
      open `test-results/<dir>/error-context.md` for the workflow test.

      Candidates worth checking once the snapshot is in hand: whether Settings
      renders an error boundary from an unmocked endpoint, whether it redirects
      (auth), and whether the tab panel is lazily mounted such that the
      hash-selected index is applied after the click is attempted.

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

- [x] **`Library.tsx:707` — an `exhaustive-deps` warning whose suggested fix
      would silently undo the URL filter-drop guard.** Introduced 2026-08-10 by
      PR #2271; noticed while linting an unrelated branch. **DONE — PR #2273.**

      `npx eslint .` in `web/` reports:

          707:6  warning  React Hook useEffect has a missing dependency:
                 'searchParams'. Either include it or remove the dependency
                 array   react-hooks/exhaustive-deps

      The omission is deliberate. That effect is the URL **writer**, and #2271
      added a guard at the top of it that reads `searchParams` precisely to
      detect "the URL changed under us since the last commit":

          const currentSearch = searchParams.toString();
          const urlChangedUnderUs = currentSearch !== seenSearch.current;
          if (urlChangedUnderUs && currentSearch !== lastWrittenSearch.current) return;

      Reading a value without depending on it is the whole point — the guard
      needs the *current* URL compared against a ref that a **later** effect
      advances, so effect declaration order is load-bearing. See the comment on
      `seenSearch` and the one inside the write effect.

      **RESOLVED 2026-08-10 (PR #2273).** Suppressed with an explicit
      `// eslint-disable-line react-hooks/exhaustive-deps` on the deps line,
      carrying the reason. The dependency array is byte-identical to before —
      the diff is comments only.

      **Two claims in the original write-up turned out to be wrong; corrected
      here so nobody acts on the stale version:**

      1. It said whether adding the dependency actually breaks the guard was
         "not established". It is now. **With `searchParams` added to the
         array, `library-sidebar-filters.spec.ts` ran 36/36 green on webkit**
         (9 tests × 4 repeats). It does **not** break that spec. It was still
         not adopted, because that effect owns URL writes for the whole Library
         page and one spec file is not evidence about the rest of it — with the
         dep added the writer also re-runs on its own echo and rewrites
         identical params. Anyone wanting that form must verify it page-wide.

      2. It said to use `// eslint-disable-next-line`. **That does not work
         here.** A `-next-line` directive placed above a multi-line explanation
         applies to the *comment*, not to the deps array — the original warning
         survives and lint reports an additional "Unused eslint-disable
         directive". Only `reportUnusedDisableDirectives` made that visible.
         Use `eslint-disable-line` on the deps line itself, which is also what
         the sibling read effect at `Library.tsx:632` already does.

      **Control, re-measured under the pinned Playwright 1.62.1** (the earlier
      "4 of 6 / 24 consecutive" figures were taken in a worktree that had
      silently resolved a stray 1.57.0 from `$HOME`, so they were discarded):

          guard intact,   guard test ×8         8 passed,  exit 0
          guard disabled, guard test ×8         4 failed / 4 passed, exit 1
          guard intact,   whole spec file ×4   36 passed,  exit 0

      Only one test in `library-sidebar-filters.spec.ts` exercises this guard —
      `the filter never disappears from the URL while the effects settle`
      (`:234`, webkit). The two deep-link tests in the same file pass **6 of 6
      with the guard disabled**; they are invariant coverage, not regression
      guards, and are labelled as such in the file. Running those and seeing
      green proves nothing about this dependency array.

      eslint after the change: **24 warnings, 0 errors** (was 25/0 — exactly
      this warning removed, none added). `tsc --noEmit` exit 0.

- [x] **🔴 The search index silently drops updates when its queue fills — 56,537 dropped
      in seven days.** Measured on prod 2026-08-10 from `journalctl`. This was a
      **blocking prerequisite** for pushing filters/sort into Bleve (design doc option
      A1), and it changed the ordering of that plan.

      **✅ FIXED — reconciliation shipped.** Owner chose a dirty-set drained on a ticker,
      persisted to Pebble, with an adaptive batch size. Steps 1, 2 and 4 below are done;
      step 3 (filter/sort pushdown) is now unblocked.

      Implementation: `internal/database/pebble_store_search_dirty.go` (durable set,
      `idx:sidx:dirty:{id}`, mirroring the existing `idx:upl:dirty:` playlist idiom) and
      `internal/server/search_reconciler.go` (ticker + adaptive drain).

      ## 🔑 The root cause was a false comment, not just a missing feature

      Three separate comments — `indexed_store.go:14`, `indexed_store.go:100` and
      `server.go:225` — asserted that "a startup reindex will heal any gaps". **It does
      not.** `buildSearchIndexIfEmpty` opens with `if count > 0 { return }`, so it runs
      only when the index has ZERO documents. On a populated library it has never run.

      The drop was therefore designed as safe *under a guarantee that was never true*.
      That is why all three comments were corrected in place, with the old claim quoted
      and refuted, rather than quietly rewritten: the next person to read the old
      reasoning must not re-derive the same wrong conclusion.

      ## Two things the implementation measured rather than assumed

      1. **`pebble.Sync` on the mark was a latency bug.** The first version synced every
         mark; a test writing 2,500 IDs took **13.9s** (~180/sec). Drops arrive in bursts
         on the write path while `enqueueIndex` holds `indexQueueMu.RLock`, so that would
         have added ~5ms to every write during exactly the overload the drop relieves.
         Switched to `NoSync` (still WAL-backed, survives process crash): the same test
         now takes **0.13s** — 107× faster.
      2. **A 1%-per-tick adaptive drain was too slow to matter.** At 1%, a 56,537 backlog
         drains ~565/tick — indistinguishable from the fixed 500 floor, ~50 minutes total,
         and it decays so the tail is slowest. Implemented at 10% clamped to
         [500, 5000]: the same backlog clears in ~11 ticks (~5.5 min).

      ## The measurement

      ```
      level=WARN msg="search index queue full, dropped (delete)" bookID=01KXXVGZ90PS78ZWJZJY62EFCJ del=false
      ```

      | window | dropped index operations |
      |---|---|
      | last 7 days | **56,537** |
      | days affected | Aug 03 and Aug 07 only |
      | since the Aug 09 10:33 restart | 0 |

      **The zero is not reassuring.** The queue is empty because the process restarted and
      no bulk operation has run since. Both affected days were bulk-operation days; the
      next scan, merge wave or dedup run refills it and drops again.

      ## The mechanism

      `internal/server/indexed_store.go:113-122` — a non-blocking send onto a 1024-deep
      channel, with `default:` as the overflow branch:

      ```go
      select {
      case s.indexQueue <- indexRequest{bookID: bookID, delete: del}:
      default:
          atomic.AddInt32(&s.indexWorkerBusy, -1)
          slog.Warn("search index queue full, dropped (delete)", "bookID", bookID, "del", del)
      }
      ```

      Dropping under pressure is a defensible choice — the alternative is blocking a write
      path on the indexer. **What is not defensible is that nothing reconciles afterwards.**
      A dropped update is lost permanently; there is no retry, no dirty-set, and no
      periodic re-sync. The index diverges from the database and stays diverged until
      something happens to rewrite that book.

      Note the log message says `(delete)` while `del=false` — the label is wrong for the
      upsert case, which makes the warning harder to interpret than it needs to be.

      ## Why this blocks A1

      Today a dropped update means **stale relevance** — a book ranks oddly or misses a
      match. Bad, tolerable, invisible.

      After A1 pushes filters and sort into the Bleve query, a dropped update means
      **wrong rows**. A book whose `library_state` changed to `organized` but whose index
      entry still says `imported` will be *absent from the Organized filter and present in
      Imported*. The user sees a library that is missing books, with no error.

      **This is the difference between an index that is a relevance dependency and one that
      is a correctness dependency** — exactly the risk flagged as open item 3 in
      `docs/design/2026-08-09-search-backend-options.md`, now with a measured failure rate
      attached.

      ## What to do, in order

      1. **Make the drop visible.** A counter and a metric, not just a WARN that scrolls
         past 56,537 times. Right now the only way to know is to grep journald.
      2. **Reconcile.** Any of: a dirty-set of book IDs that failed to enqueue, drained on
         a ticker; a periodic full re-index; or a generation counter per book compared
         against the index on read. A dirty-set is the cheapest and matches the existing
         "cached aggregates + dirty flag" idiom in this codebase.
      3. **Then and only then**, push filters/sort into the index.
      4. Fix the `(delete)` label while touching this.

      **Do not size the queue bigger and call it fixed.** 1024 → 100,000 moves the
      threshold; it does not add reconciliation. The bulk days dropped 56K operations,
      which no reasonable buffer absorbs.

      ## Also settles an open question

      Open item 4 of the design doc asked whether the index is complete. **It is not**, and
      now there is a mechanism and a number rather than a suspicion. The `.api-token` is
      still stale, but this answer did not need it.

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

- [x] **DONE 2026-08-09 — the e2e suite runs in CI and BLOCKS on every trigger.**
      `continue-on-error: false`, `pull_request` trigger live with its paths filter
      (#2258). A change that breaks the browser suite can no longer merge.

      **Final state, measured on the real ubuntu runner — not locally:**

      | configuration | result |
      |---|---|
      | chromium (PR path) | **272 passed / 0 failed / 8 skipped** |
      | chromium + webkit | **544 passed / 0 failed / 16 skipped** |

      Baseline was 146 failed / 138 passed of 288 chromium tests. 26 PRs,
      #2224–#2258.

      **The three sub-items in the original entry are all closed:**

      1. ~~Establish the real number with a full-output run~~ — done; it was 146, not
         the ~4 the fragment guessed.
      2. ~~Triage the failures~~ — done, plus **eleven product regressions** found that
         were not test problems (audit:
         `docs/audits/2026-08-09-e2e-repair-and-ui-regressions.md`).
      3. ~~Flip `continue-on-error` off~~ — done, together with restoring the
         `pull_request` trigger, exactly as the entry required.

      **⚠️ The CI-only failures were ALL environmental, not product defects** — and two
      were filed as product bugs before measurement said otherwise:
      worker contention starving MUI transitions (`workers: 1` on CI), a shared 30s
      timeout too tight for webkit (its own 60s budget), and visual goldens stored as
      **Git LFS pointers** no runner could decode. That last one meant the visual test
      could never have passed on CI, for either browser.

      **🚨 "Green locally" ≠ green on CI.** The suite was 0-failing on macOS while CI
      had 179 failures. `conclusion: success` on that workflow meant nothing while
      `continue-on-error` was true. Dispatch
      `gh workflow run e2e.yml --ref <branch> -f projects=chromium` and read the counts.

      ---

      **Original entry, for the record.** Found
      2026-08-08 while adding sidebar-filter coverage. `grep -rl
      "test-e2e\|test:e2e\|playwright test" .github/workflows/` returns
      **nothing**. The suite exists, is maintained (43 specs were repaired
      across #2185/#2187/#2191 this week), and gates nothing. A regression in
      any of it lands on `main` unnoticed until someone runs `make test-e2e` by
      hand.

      That is exactly how the six spec files broken by the `_page` fixture error
      stayed dead **from April to August 2026** — roughly four months of silent
      rot, only noticed because #2178 happened to unmask it.

      **Two traps to fix at the same time, or CI will lie to you:**

      1. **`reuseExistingServer: !process.env.CI`** in
         `web/tests/e2e/playwright.config.ts`. Locally this attaches to whatever
         already listens on 127.0.0.1:8484 instead of building. On 2026-08-08 a
         server left running since **00:31** was silently reused for hours,
         producing a fully green 130-test suite that had exercised a frontend
         bundle predating the fixes under test — and it was reported as
         verification before the mistake was caught. The flag is already
         disabled under `CI`, so CI itself is safe; the hazard is local runs and
         anyone trusting them. Consider making the config refuse a server older
         than the working tree, or dropping the flag entirely.
      2. **Browser binaries drift per worktree.** A fresh `npm ci` in a new
         worktree installed a Playwright wanting `webkit-2336`, which was not in
         `~/Library/Caches/ms-playwright`, so every webkit test errored with
         "Executable doesn't exist" — which reads like a test failure but is an
         environment failure. CI needs an explicit `npx playwright install
         --with-deps` step, and the distinction should be obvious in the logs.

      **Cost consideration.** A full run is ~20 minutes (chromium + webkit) and
      rebuilds frontend + Go binary, so it does not belong in the fast PR gate
      alongside Minimal CI. Options worth weighing: chromium-only on PRs with
      both engines nightly; or a required-but-slower job that runs in parallel
      with the rest. Decide deliberately rather than defaulting to "everything
      on every PR" and then disabling it when it gets annoying.

      **Acceptance:** a PR that breaks any e2e spec fails a check, and the
      failure names the spec rather than surfacing as a browser-launch error.

- [x] **The e2e suite is roughly HALF RED in a clean environment — 146 failed /
      138 passed. Triage it, then make the CI gate blocking.** Discovered
      2026-08-08 by the first-ever clean-environment run, immediately after
      wiring the suite into CI (#2202).

      ✅ **DONE 2026-08-09 across 17 PRs (#2224–#2244).** Final measurement on
      merged `main`: **552 passed / 0 failed / 16 skipped of 568**, exit 0,
      **both browsers green**. (Intermediate state after the first 14 PRs was
      chromium 272/7/0 and webkit 268/7/4; the webkit tail was closed by #2242
      and #2244.) The 16 skips are 7 `test.fixme` markers × 2 browsers, each
      attached to a real product defect so they report as *unexpected passes*
      once fixed, plus a real-server bootstrap smoke test × 2 that skips itself
      unless the server is un-bootstrapped. **Nothing was deleted or silently
      skipped.** Full audit with file:line evidence:
      `docs/audits/2026-08-09-e2e-repair-and-ui-regressions.md`.
      ✅ Sub-item 3 (flip `continue-on-error` off) is now DONE too — #2258.

      **The webkit tail was not what it looked like.** 3 of the 4 were a
      Playwright/webkit harness artifact, not a product defect: its synthesised
      pointer click on MUI's `PaginationItem` failed 4/4 while an in-page DOM
      click on the identical buttons passed 6/6. Fixed in #2242 (24/24 on
      webkit, from an 11/24 failure baseline) with retries logged so the
      masking stays visible. This had been filed as a product bug **twice**;
      both claims are corrected on the record in
      `todo.d/20260809-library-double-fetch-swallows-clicks.md`.

      **The 4th is deliberately still open and NOT fixed** (1 occurrence in
      1,136 executions, did not reproduce): `book-detail.spec.ts` purge flow.
      It passes 6/6 in isolation, so there is no measurement establishing the
      app is correct, and tolerating it in the test would be papering over an
      unknown. See `todo.d/20260809-book-detail-purge-suite-only-flake.md`.

      **This contradicts what was believed on 2026-08-08 morning.** The
      executive summary
      `docs/executive-summaries/2026-08-08-the-safety-net-that-had-stopped-catching-executive-summary.md`
      states the suite "can be trusted as a gate again" and that "it is now safe
      to require these". That conclusion rested on a local run reporting **130
      passed / 0 failed**, and that run was wrong in two independent ways:

      1. It silently reused a server that had been running for **hours**
         (`reuseExistingServer: !process.env.CI`), so it exercised a stale
         frontend bundle. This part was already retracted in #2198.
      2. **It also reported only 137 of 576 tests as having run** — 130 passed
         + 7 skipped, against 576 collected (288 chromium + 288 webkit).

      **Correction, same day:** the second point was first filed here as a
      "collection gap", i.e. a suspicion that the config collected fewer tests
      locally than in CI. **That is wrong and should not be chased.**
      `npx playwright test --list` locally reports **288 chromium / 576 both**,
      exactly matching CI. Collection is fine.

      What actually happened is worse and simpler: that run was invoked as
      `npm run test:e2e ... | tail -60`, which caused two separate failures of
      observation at once.

      - **The exit code was `tail`'s, not Playwright's.** A shell pipeline
        returns the status of its LAST command, so the "exit code 0" that made
        the run look successful only proved `tail` worked. Use
        `${PIPESTATUS[0]}`, or capture full output to a file and grep the file
        afterwards — never pipe a test command into a truncating filter and
        read the result as a verdict.
      - **The summary header scrolled out of the 60-line window.** What survives
        is the tail of a long list of webkit tests followed by "7 skipped / 130
        passed". The list is almost certainly Playwright's "did not run"
        section, but the header naming it was truncated away, so **why 439
        tests did not run is undetermined from that log** and will need a fresh
        full-output run to establish. Do not guess it from the fragment above.

      **The CI job currently runs NIGHTLY ONLY, with `continue-on-error:
      true`.** It was first wired to run on every PR; that had to be undone
      within the hour. `continue-on-error` stops a job failing the *workflow*
      but the individual check still reports red, so every PR would have
      carried a permanently-failing E2E check. That is worse than no check —
      people habituate to a red they cannot act on, which is the same failure
      that let six specs rot for four months, only louder.

      Nightly gives a daily signal without poisoning every PR. Both the
      `pull_request` trigger (commented out, paths filter preserved) and
      `continue-on-error` should be restored/flipped **together**, once the
      suite is green.

      **Work, in order:**

      1. ~~Re-run the full suite locally with output captured properly.~~
         ✅ **DONE 2026-08-08 18:47.** Fresh build, port 8484 confirmed clear,
         full output to a file, exit code read from Playwright itself
         (`PLAYWRIGHT_EXIT=1`). Result: **146 failed / 138 passed / 4 skipped**
         — *identical to CI*. Local and CI now agree test-for-test, so local
         triage is trustworthy again. The earlier "130 passed" was entirely an
         artifact of the truncating pipe plus the stale server; there was never
         a collection problem.
      2. **Triage the 146 failures.** ⚠️ **The "expect a small number of root
         causes" guess above was wrong and is corrected here.** It is not one
         cascading bug. It is **the same failure CLASS spread across 22 spec
         files that nobody has refreshed yet**:

           26 dedup                 7 scan-import-organize   3 diagnostics
           14 library-browser       7 backup-restore         2 itunes-import
           12 metadata-provenance   6 version-management     2 error-handling
           11 transcode-and-counting 6 dynamic-ui-interactions 2 auth-flow
           11 batch-operations      5 settings-configuration  1 settings-ai-persistence
           10 search-and-filter     4 itunes-bidirectional-sync 1 library-enhancements
            8 dedup-operations      4 files-history           1 import-paths
                                    3 unified-dedup-tab

         Error signatures across all 146: `toBeVisible` 67, `element(s) not
         found` 64, `locator.click` 50, `strict mode violation` 9. That is
         overwhelmingly "the test looks for an affordance the app no longer
         renders" — the exact drift already fixed in waves 1 and 2.

         **The strong evidence that this is tractable:** 13 spec files are
         **fully green**, and they include *every* file repaired in waves 1
         and 2 — `dashboard`, `book-detail`, `file-browser`,
         `import-audiobook-file`, `operation-monitoring` — plus the two new
         specs added 2026-08-08. The repair pattern works and holds; it has
         simply never been applied to the other 22 files. This is 22 files of
         known, mechanical work, not 146 mysteries.

         Suggested order: biggest first (`dedup` 26, `library-browser` 14,
         `metadata-provenance` 12), since shared helpers in those will likely
         drop several files at once — that is how one `{ data: ... }` envelope
         fix cleared 24 of 34 in wave 2.
      3. **Flip `continue-on-error` off** once green, and say so in the PR.
         ⏳ **STILL OPEN.** The workflow was moved to nightly-only rather than
         made blocking, because a permanently-red check on every PR is worse
         than no check. Now that chromium is green this can be reconsidered —
         but the 4 webkit failures and the missing linux visual goldens have to
         be dealt with first or the gate goes red on day one.
      4. **Correct the executive summary** rather than leaving a claim on the
         record that the safety net is restored when half of it is on the floor.
         ✅ **DONE 2026-08-09.** A correction banner was added to
         `2026-08-08-the-safety-net-that-had-stopped-catching-executive-summary.md`
         and the outcome written up in
         `2026-08-09-the-half-red-safety-net-executive-summary.md`.

      **Do not "fix" this by deleting or skipping the failing specs.** Six files
      were disabled-by-accident for four months and that is the incident this
      whole thread of work exists to prevent.

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

- [x] **The e2e failures have MIXED causes — do not plan a single systematic
      fix.** Sampled 2026-08-08 after a fragment filed an hour earlier
      speculated that one data-envelope gap might explain most of the 146.
      **It does not.** Four files sampled, at least three distinct causes:

      ✅ **CONFIRMED CORRECT and closed 2026-08-09.** All four predictions held,
      and the fourth paid off: the hunch that `search-and-filter` was "the only
      one of the four that might indicate a real product defect" was **right**.
      Two real defects sit behind it — `searchBooksPage` sends no
      `library_state`, `filters`, `tags` or `sort_by`, so typing in the search
      box silently discards every active filter and the sort order; and the
      search is not debounced at all (ten requests for a ten-character query).
      Both are in `todo.d/20260809-search-drops-filters-and-debounce.md`, and
      the two tests are `test.fixme` rather than rewritten to match broken
      behaviour. The final cause tally across all 22 files was four shapes, not
      one: missing envelope; a mock branch shadowed by a prefix catch-all; UI
      relocated rather than removed; and mock field name ≠ book field name.

      - **`dedup.spec.ts` (26)** — the data-envelope bug, *verified*.
        `api.ts:1402` reads `body.data.groups`; the mock returns
        `/authors/duplicates` unwrapped. Fix: wrap the handler.
      - **`library-browser.spec.ts` (14)** — genuine affordance drift. The test
        clicks `getByRole('combobox', { name: 'Sort by' })`; no such control
        exists. Nothing to do with data shape.
      - **`metadata-provenance.spec.ts` (12)** — book-detail page renders no
        heading for the fixture book. Could be an envelope gap on a book
        endpoint or a navigation change; not yet traced.
      - **`search-and-filter.spec.ts` (10)** — behavioural, not structural. The
        test searches, then asserts a non-matching book disappears; "The Hobbit"
        stays visible. Either the mock never implements filtering, or search is
        genuinely not filtering. **Worth tracing first** — it is the only one of
        the four that might indicate a real product defect rather than test rot,
        and it sits next to the known server-side filtering weakness (an
        unrecognised filter param returns the entire library with HTTP 200).

      **Estimate history, kept deliberately.** This has now been framed three
      ways in one evening: "a few cascading root causes" → "22 files of
      independent drift" → "probably one envelope gap". The middle one was
      closest. The third came from verifying exactly ONE file and generalising,
      which is the same error each time — concluding from the first sample that
      agreed with a convenient theory. Whoever picks this up should assume mixed
      causes and re-sample rather than trust any single framing, including this
      one.

      **Practical consequence:** budget per-file work, not one sweep. The
      envelope fix is still worth doing (it is cheap and clears the largest
      file), but it will not clear the other 21.

- [ ] **`search-and-filter.spec.ts` (10 failures) is a MOCK gap, not a product
      defect — the e2e mock's `/audiobooks` handler ignores every filter
      param.** Traced 2026-08-08. This downgrades the earlier flag that it
      "might indicate a real product defect"; it does not.

      **The chain, verified end to end:**

      - `api.searchBooksPage()` (`src/services/api.ts:1023`) issues
        `GET /audiobooks?search=<q>&limit=&offset=&is_primary_version=true`.
        It does **not** call `/audiobooks/search`.
      - The mock's `/api/v1/audiobooks` handler
        (`tests/e2e/utils/test-helpers.ts:768`) reads **only** `offset` and
        `limit`. `search`, `filters`, `tags`, `library_state`,
        `fingerprint_status` and the rest are all ignored; it returns
        `mockState.books.slice(offset, offset+limit)`.
      - So a search returns the whole library, the non-matching book stays on
        screen, and `expect(...).not.toBeVisible()` fails. The app is behaving
        correctly; the fake server is not.
      - Dead code worth deleting or wiring up: the mock has a
        `/api/v1/audiobooks/search` handler that filters properly, and
        **nothing in `src/` calls that endpoint**.

      **Explicitly ruled out:** the empty-state fix (#2195), which now preserves
      the last known-good page when a load fails, is NOT implicated. The search
      request succeeds — it just returns everything — so the preserved-list
      behaviour never engages here.

      **Fix:** teach the mock's `/audiobooks` handler to honour the params the
      app actually sends. Minimum for this spec is `search`; doing `filters`
      (the JSON field-filter array) at the same time is worth it, because the
      In Progress / Finished sidebar filters ride on that param and any future
      test of them would hit the identical wall.

      **Note for the real backend work.** This is the mock, so it says nothing
      about the server. But it rhymes with the open server-side task: an
      unrecognised filter param on the real API returns the entire
      44,874-book library with HTTP 200 rather than failing closed. Two
      different layers, same failure mode — a filter that is ignored rather
      than rejected looks exactly like a filter that matched everything.

- [x] **"Browse by Tag" should start collapsed, or show only the top few tags.**
      Reported by the owner 2026-08-08: *"Browse by tag should start minimized
      as we have tons of tags or only show the top 5."* On a library this size
      the tag cloud renders as a wall of chips that pushes the actual book grid
      below the fold.

      **Current behaviour** (`web/src/components/library/TagCloud.tsx`):

      - Line 41: `const [expanded, setExpanded] = useState(true)` — it defaults
        to **open**.
      - It renders `availableTags.map(...)` with **no cap**: every tag in the
        library, every time.
      - The collapse machinery already exists (header row toggles, `Collapse`,
        rotating chevron, correct `aria-label`), so "start minimized" is
        essentially a one-word change.

      **Two options the owner offered; they are not exclusive and the good
      version is both:**

      1. **Start collapsed** — flip line 41 to `useState(false)`. Trivial, and
         it makes the component honest: a disclosure control whose default is
         "already disclosed" is not doing anything.
      2. **Show the top N (5) when collapsed-ish** — render a short preview row
         of the highest-count tags with a "Show all (N)" affordance, so the
         feature is still discoverable without costing a screenful. This is the
         better UX of the two, because a fully collapsed panel gives no hint
         that tags are worth browsing.

      **⚠️ Verify sort order before slicing.** `availableTags` is passed
      straight through from `Library.tsx` (lines 1971 and 1993 — note it feeds
      **both** `TagCloud` and `FilterSidebar`) and **it has not been confirmed
      that it arrives sorted by count descending**. `TagCloud` currently only
      uses `count` for font size, where order does not matter, so a latent
      sort bug would be invisible today and would silently make "top 5" mean
      "first 5 alphabetically". Sort explicitly in the component rather than
      trusting the caller.

      **Persist the open/closed choice** in `localStorage` alongside the other
      Library view preferences (`STORAGE_KEYS`), so someone who opens it does
      not have to re-open it on every visit. Without that, "start collapsed"
      trades one annoyance for another.

      **Acceptance:** on a fresh visit to /library the book grid is visible
      without scrolling past the tag cloud; tags remain reachable in one click;
      and if a top-N preview is used, the tags shown are genuinely the most
      common ones, verified against a library with many tags rather than a
      handful of fixtures.

      **✅ SHIPPED same day.** Implemented as *both* options rather than either:

      - Starts **collapsed** (`useState(readStoredExpanded)`, default false).
      - Collapsed still shows the **top 5 by count** plus a "Show all (N)"
        button, so the feature stays discoverable. A disclosure control that
        reveals nothing tells the user nothing.
      - **Sorted explicitly in the component** (`count` desc, then name), which
        the note above flagged: the caller's order was never guaranteed and
        slicing an unsorted list would have quietly shown "the first five".
      - **Persisted** via `STORAGE_KEYS.LIBRARY_TAG_CLOUD_EXPANDED`, wrapped in
        try/catch so private-browsing storage failures fall back to collapsed
        rather than throwing.
      - The header shows the total tag count while collapsed, so the panel says
        how much is hidden.
      - **Selected tags outside the top 5 are always shown** while collapsed.
        This was not in the original request but is required for correctness:
        hiding an active filter leaves the user looking at a filtered list with
        no visible control to clear it.

- [ ] **The library "Sort by" control no longer exists — 4 e2e tests target a
      surface that is gone.** Found 2026-08-09 while repairing
      `library-browser.spec.ts`.

      **Test side ✅ DONE (#2230); product decision ⏳ STILL OPEN.** The four
      tests now drive sort through the URL (`?sort=…&order=…`), which is the
      only surviving mechanism, so the sort *behaviour* stays covered. What is
      unresolved is whether losing the control was intentional. Hard evidence:
      `SearchBarProps` (`web/src/components/audiobooks/SearchBar.tsx:124-131`)
      has no `onSortChange` prop at all, and
      `web/src/components/library/LibraryBookGrid.tsx:133` receives the handler
      as `_handleSortChange` — underscore-prefixed to mark it deliberately
      unused. `SearchBar.test.tsx:43` asserts "does not render sort controls
      when `onSortChange` is absent", which now passes vacuously because the
      prop cannot be supplied. Either restore the control, or delete the dead
      state and that vacuous unit test.
      Full write-up: `todo.d/20260809-library-sort-control-missing.md`.

      `sorts books by title ascending` / `title descending` / `author` /
      `date added` all do:

          await page.getByRole('combobox', { name: 'Sort by' }).click();
          await page.getByRole('option', { name: 'Title' }).click();

      There is **no such control anywhere in the library UI**. Grepping the
      components turns up no `Sort by` label and no sort dropdown in
      `FilterPanel`, `LibraryToolbar` or `SearchBar`. Sorting now happens
      through the table view's column headers
      (`LibraryBookGrid`'s `handleColumnSortChange` → `ConfigurableTable` /
      `AudiobookList`), which the default grid view does not show at all.

      **This is a rewrite, not a selector tweak**, which is why it was left out
      of the mock-fix PR: the tests must switch to list/table view first and
      then drive column-header sorting, and the assertions about resulting
      order need to match however that view renders.

      The mock now honours `sort_by` / `sort_order` correctly (added
      2026-08-09), so once the tests drive the real control the backend half of
      this is already in place.

      **Check before rewriting:** confirm sorting is genuinely still reachable
      by a user in the default grid view. If it is not, that is a product
      question — "you can no longer sort the library without switching views"
      — and should be raised rather than encoded into a test.

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

- [ ] **4 remaining failures in `metadata-provenance.spec.ts`** (down from 12).
      Diagnosed but not fixed 2026-08-09; stopped deliberately rather than
      keep iterating.

      Fixed in that pass (8 of 12): the spec mocks by patching `window.fetch`
      rather than using `page.route`, so it gets none of the shared handlers
      and needed its own `/auth/status` (without it the app rendered the LOGIN
      screen), its own `{ data: ... }` envelope, and explicit handlers for the
      book sub-resources — the generic "URL contains the book id" branch was
      swallowing `/files`, `/versions`, `/tags`, `/segments` and handing each
      of them the BOOK object, so the page crashed on `.length` of undefined.

      **Still failing, with what is known:**

      1. `dialog opens with all fields populated` — the Author textbox is
         empty. The dialog reads `formData.author`, and the detail page renders
         "Unknown Author", so the payload shape is wrong somewhere between the
         fixture and `Audiobook`. Adding `author_name`/`series_name` alongside
         the short names did NOT fix it, so the mapping is elsewhere — trace
         how `formData` is initialised from the API response rather than
         guessing again.
      2. `locked fields show orange lock icon` — walks the DOM relative to
         `getByLabel('Title *')` (`'..'` → `'..'` → `button`) to reach the lock
         icon. Fragile by construction: it depends on the exact wrapper depth
         MUI renders. Better fixed by giving the lock button a stable
         `data-testid` than by counting parents.
      3. `editing a field automatically locks it` and 4. `year field shows
         error for non-numeric input` — both start from the same field
         locators; likely fall out with (1) and (2).

      **Locator rule established in this file, worth keeping:** to READ or FILL
      a field use `getByRole('textbox', { name, exact: true })` —
      `getByLabel('Title *')` is a strict-mode violation because each field has
      an adjacent lock button labelled "Lock Title *" and getByLabel
      substring-matches. The lock tests still use `getByLabel` on purpose,
      because they traverse relative to it. A blanket sweep converting all of
      them broke passing tests; the note in the spec says so.

- [ ] **8 remaining failures in `batch-operations.spec.ts`** (down from 11), and
      a caution about how they were approached.

      **Fixed (3):** the "N selected" chip is rendered TWICE in the tree, so
      `getByText('1 selected')` was always a strict-mode violation. Assertions
      now use `.first()` — the behaviour under test is that the count shows, not
      how many places show it. *(If that duplication is itself unintended, it is
      a UI question worth asking separately.)*

      **Verified renames applied:** the toolbar button "Fetch Metadata" is now
      **"Fetch Selected"**, and "Deselect All" is now **"Deselect"** — read off
      the app's rendered accessible names, not guessed.

      **⚠️ Trap, hit and recorded:** the confirm button INSIDE the "Bulk Fetch
      Metadata" dialog is **still "Fetch Metadata"** (`LibraryDialogs.tsx`
      renders `Fetching…` / `Fetch Metadata`). A blanket find-and-replace of
      "Fetch Metadata" → "Fetch Selected" therefore breaks the dialog-scoped
      references. Only the toolbar button was renamed. The spec now carries a
      comment at that call site.

      **Still failing (8):** `deselects all books`, the five bulk-fetch tests,
      `batch updates metadata field`, and `disables batch operations when no
      books selected`. The count did NOT move across three separate attempts
      (chip fix, renames, dialog-scope correction), which means the remaining
      cause has not actually been found yet — the failures are almost certainly
      NOT more label drift.

      **Do this next, and do it first:** open the Playwright DOM snapshot for
      one bulk-fetch failure (`test-results/*/error-context.md`) and read what
      is actually on the page at the moment of failure. Every real cause found
      in this repair effort — the dedup page being redesigned behind a "Legacy
      View" toggle, metadata-provenance rendering the LOGIN screen, the book
      sub-resources returning the book object — was found that way, and every
      wrong guess came from reasoning about what *should* be there instead.

- [x] **`transcode-and-counting.spec.ts` (11 failures) — two hypotheses tested
      and REJECTED. Read this before trying either again.**
      Investigated 2026-08-09; no code shipped, because nothing that was tried
      improved the count and one attempt made it worse.

      **What the page actually shows** (from the Playwright DOM snapshot, which
      is the only thing that has reliably told the truth in this effort): the
      Dashboard renders normally but **every count is 0** — Library Books,
      Import Path Books, Authors, Series.

      **Rejected hypothesis 1 — missing `{ data: ... }` envelope on the specs'
      inline `page.route` overrides.** Real in principle: these tests call
      `route.fulfill` directly, bypassing `jsonResponse()` in test-helpers, and
      `api.getSystemStatus()` reads `body.data`. Wrapping all 11 success
      payloads changed the result by **zero**. The envelope is not what is
      breaking this file.

      **Rejected hypothesis 2 — the mock ignoring `is_primary_version`.**
      Reasoning looked sound: `api.getBooks()` always sends
      `is_primary_version=true`, `countBooksFiltered()` reads
      `body.data?.count` from that same endpoint, and the fixture is exactly 2
      primary + 1 non-primary against an expected count of 2. Teaching the mock
      to honour the param took failures from **11 to 12** — a regression. It
      was reverted. Do not re-apply without understanding why it hurt.

      **The one solid lead:** "Library Books" does NOT come from
      `/system/status`, which is what these tests mock. It comes from
      `api.countBooksFiltered()` → `GET /audiobooks?...` → `body.data?.count`
      (`services/api.ts:1058`). So a test that overrides `/system/status` to
      control the dashboard count is mocking the wrong endpoint entirely. That
      is worth confirming as the root cause before writing any more code.

      **Method note.** Every real cause found in this repair effort came from
      reading `test-results/*/error-context.md` and looking at what was on the
      page. Every wrong guess — including both above — came from reasoning
      about what should be there. Look first.

      ✅ **RESOLVED 2026-08-09 (#2229): 11 → 0.** The "one solid lead" recorded
      above was **also wrong**. It concluded that "Library Books" comes from
      `countBooksFiltered` rather than `/system/status`, and therefore that
      these tests mock the wrong endpoint. `Dashboard.tsx:97` reads
      `systemStatus.library_book_count ?? systemStatus.library.book_count`, so
      `/system/status` *is* the right endpoint. What was wrong was the **shape**:
      both overrides returned a flat, un-enveloped body, `api.getSystemStatus`
      returns `body.data`, and Dashboard then threw on `.library` — which is why
      *every* count read 0, including Authors and Series that have their own
      endpoints entirely. That is exactly the misdirection that made rejected
      hypothesis 1 look like it changed nothing: the envelope alone is not
      enough without the nested `library.book_count` shape. Both together took
      it 11 → 9; the rest were the Manage Versions relocation, two
      route-patterns that never matched, and a button that relabels itself to
      "Converting..." mid-assertion.

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

- [x] **Re-run the CPU-node Whisper benchmark in POOL configuration, during the
      day.** ✅ **DONE 2026-08-08.** Full sweep run on U1 (`ssh u1`); raw log
      `/opt/whisper-bench/pool.log`, scripts `bench_pool.py` (phase 1) and
      `bench_pool2.py` (phase 2). Same methodology as the evening run: 10 real
      prod clips, base.en, beam 5, VAD on, mirroring `scripts/whisper_server.py`.

      **Measured (clips/min, and projected days for the 260k-file tier-3 tail):**

        shape        int8_float32              float32
        1 x 48        ~2.04  (~89 days)         2.39  (75.4 days)
        4 x 12        44.13  ( 4.1 days)        —
        8 x 6         63.86  ( 2.8 days)       40.48  (4.5 days)
        12 x 4        67.52  ( 2.7 days)       45.80  (3.9 days)
        16 x 3        67.82  ( 2.7 days)        —
        24 x 2        75.83  ( 2.4 days)  <--  47.91  (3.8 days)
        32 x 1        75.09  ( 2.4 days)        —
        48 x 1        76.31  ( 2.4 days)       47.92  (3.8 days)

      **➡️ Recommended config: 24 workers x 2 threads, `int8_float32`.**
      Throughput saturates at ~75-76 clips/min across 24, 32 and 48 workers —
      three points spanning a 2x range, so this is a real ceiling and not two
      adjacent samples that happened to agree. 24x2 reaches it with the fewest
      processes and the fewest resident models, so it is the cheapest way to
      buy the plateau.

      **The tier-3 tail drops from ~92 days to ~2.4 days.**

      **Corrections to the assumptions this entry previously recorded:**

      - The estimate of "~10-14 clips/min, ~13-18 days" was **~5x pessimistic**.
        Actual is 75.83 and 2.4 days. Pooling did not merely scale linearly off
        the single-process number, because the single-process number was itself
        crippled.
      - "**One process cannot use 48 cores**" — confirmed, and it is the whole
        story. Every row above uses the same 48 total threads. The only variable
        is how they are divided, and it moves throughput **32x** (2.39 -> 76.31).
        This is not a hardware headroom finding; it is ctranslate2 being unable
        to use a wide intra-op thread count.
      - "**int8 buys nothing on this host**" — needs qualifying, because **the
        compute-type winner flips with configuration**. Single-process,
        `float32` is fastest (2.39 vs ~2.04 for int8_float32 vs 1.96 for int8).
        In every pool shape measured, `int8_float32` wins decisively — 63.86 vs
        40.48 at 8x6, 67.52 vs 45.80 at 12x4, 75.83 vs 47.91 at 24x2, and
        76.31 vs 47.92 at 48x1 — a consistent ~50-59% advantage at four
        separate shapes. Both compute types have their OWN plateau, and the
        gap persists there: int8_float32 tops out ~75-76 clips/min while
        float32 tops out ~47.9 (47.91 at 24x2 and 47.92 at 48x1, which is
        about as clean a plateau as this harness can show). Since a pool is
        what would actually ship, the original note is closer to right than a
        single-process comparison suggests. Working hypothesis (NOT measured):
        concurrent workers saturate memory bandwidth and int8 weights halve that
        traffic, while a single 48-thread process is compute-starved by poor
        thread scaling so bandwidth never becomes the limit.

      **Methodology note worth keeping.** The original script mapped the 10
      clips once per config. That cannot measure a 12-worker pool — two workers
      would idle — and at 8 workers each does barely one clip, so the number
      measures pool spin-up and imbalance rather than throughput. Every pool run
      now replicates the clip list to at least 4 tasks per worker. Compute per
      clip is identical on repeat (model resident, audio decode + inference
      still run), so tasks/wall stays a fair throughput figure.

      **⚠️ These are upper bounds for the transcription step alone.** The
      harness reads local WAVs and discards the text. A production run also
      fetches/decodes real audiobook files, writes results to Pebble, and
      updates per-file status — none of which is in these numbers. Treat 2.4
      days as the floor for the compute, not a schedule for the operation.

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

- [ ] **TODO-MUI-2** MUI upgrade Step 2 — `@mui/*` 6.x → 7.x including the
      one-time Grid conversion (brief: `docs/plans/2026-08-07-mui-upgrade-path.md`;
      requires TODO-MUI-1 merged; do NOT continue to v9 in the same session/PR)
  - `cd web && npm install @mui/material@7 @mui/icons-material@7`
  - Grid: convert legacy Grid → new Grid NOW (do not rename to `GridLegacy` —
    it is removed in v9 and we'd pay twice):
    `npx @mui/codemod@latest v7.0.0/grid-props web/src`
    Inventory says 175 `<Grid item` and 35 `<Grid container` across 23 files;
    codemod output is `item xs={12} sm={6}` → `size={{ xs: 12, sm: 6 }}`,
    `xs` → `size="grow"`. After it runs, `grep -rn "<Grid item" web/src`
    must return 0.
  - Hand-verify layout on every Grid file: new Grid spaces with CSS `gap` and
    containers no longer stretch full-width by default — compare against the
    TODO-MUI-0 smoke baseline. Highest-risk files: `web/src/pages/Series.tsx`,
    `web/src/pages/Authors.tsx`, `web/src/pages/Dashboard.tsx`,
    `web/src/components/settings/ITunesImport.tsx`.
  - `npx @mui/codemod@latest v7.0.0/input-label-size-normal-medium web/src`
    (idempotent, cheap).
  - Build must confirm icon path imports still resolve under the v7 package
    layout (TODO-MUI-0 normalized the `.js` suffixes; if `npm run build`
    still errors on `@mui/icons-material/X`, switch those files to named
    barrel imports).
  - Known no-ops for this repo (verified 0 usages 2026-08-07): `Hidden`,
    deep >1-level imports, `createMuiTheme`, `onBackdropClick`, `@mui/lab`,
    `CssVarsProvider` mode behavior.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke with EXTRA attention to spacing/layout on Library, Book
    Detail, Activity Log, System > Maintenance, Dedup tabs.
  - Rollback: `git revert` of this single PR.

- [ ] **TODO-MUI-3** MUI upgrade Step 3 — React 18 → 19 (OPTIONAL but
      recommended; brief: `docs/plans/2026-08-07-mui-upgrade-path.md`; requires
      TODO-MUI-2 merged — MUI v7 supports React 19, v5/v6 pairings are riskier;
      do NOT combine with the v9 bump in the same session/PR)
  - Why: MUI v9 does NOT require React 19 (peers `^17 || ^18 || ^19`), but
    upgrading first deletes the `react-is` override hack, matches the
    combination MUI tests first-class, and pre-positions for the post-v9
    styling-layer refactor.
  - `cd web && npm install react@19 react-dom@19 && npm install -D @types/react@19 @types/react-dom@19`
  - `npx codemod@latest react/19/migration-recipe` (covers
    `ReactDOM.render` → `createRoot`, `react-dom/test-utils` `act` →
    `react`'s `act`, propTypes/defaultProps removal on function components).
  - Hand-check afterwards: `grep -rn "test-utils" web/src`,
    `grep -rn "defaultProps" web/src`, `grep -rn "useRef()" web/src`
    (React 19 `useRef` requires an argument), and Vitest setup files under
    `web/src/test/`.
  - Remove the `react-is` override added in TODO-MUI-0 from
    `web/package.json` (no longer needed on React 19) and `npm install`.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke of Library, Book Detail, Activity Log, System > Maintenance,
    Dedup tabs; zero new console errors.
  - Rollback: `git revert` of this single PR.

- [ ] **TODO-MUI-4** MUI upgrade Step 4 — `@mui/*` 7.x → 9.x (final hop; there
      is NO Material UI v8 — v7 jumps straight to v9 to align with MUI X; brief:
      `docs/plans/2026-08-07-mui-upgrade-path.md`; requires TODO-MUI-2 merged,
      TODO-MUI-3 recommended first; single PR, nothing else in the session)
  - `cd web && npm install @mui/material@9 @mui/icons-material@9`
  - If still on React 18 (TODO-MUI-3 skipped): KEEP the
    `"overrides": { "react-is": "^18.2.0" }` in `web/package.json` — MUI v9
    ships react-is@19 internally and mismatches cause runtime prop-type
    errors on React 18.
  - System props removed from Box/Stack/Typography/Grid/Link/DialogContentText
    — ~381 direct-prop usages measured 2026-08-07. Run the v9 system-props
    codemod (confirm exact name on
    https://mui.com/material-ui/migration/upgrade-to-v9/ or via
    `npx @mui/codemod@latest --help`), converting e.g.
    `<Box mt={2} color="primary.main">` → `<Box sx={{ mt: 2, color: 'primary.main' }}>`.
    Then hand-sweep for leftovers:
    `grep -rnE '<(Box|Stack|Typography|Grid|Link)[^>]*\s(mt|mb|ml|mr|mx|my|m|pt|pb|pl|pr|px|py|p|gap|bgcolor|display)=\{' web/src --include='*.tsx' | grep -v 'sx='`
    Misses fail SILENTLY (ignored prop → styling vanishes), so eyeball the
    smoke pages, don't trust compile success.
  - Slot-prop removals: `InputProps` (24 usages) → `slotProps.input`,
    `componentsProps` (4) → `slotProps`. Run the relevant
    `npx @mui/codemod@latest deprecations/<component>-props web/src` codemods
    for TextField/Input plus anything tsc flags; hand-fix the remainder.
  - Grid checks: `grep -rn "GridLegacy" web/src` must be 0 (TODO-MUI-2
    converted us), and `grep -rnE '<Grid[^>]*direction="column' web/src`
    must be 0 (removed in v9 — replace with Stack).
  - Emotion remains the default engine in v9; no Pigment CSS work.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke of Library, Book Detail + Metadata Review dialog, Activity
    Log, System > Maintenance, Dedup tabs, checking specifically for
    silently-dropped spacing/color styling.
  - Rollback: `git revert` of this single PR.

- [x] **Refresh the remaining content-drift e2e failures unmasked by the `_page` fix.**
      PR #2178 (2026-08-07) fixed the fixture error that had silently killed six
      e2e spec files since April 2026. With the mask gone the suite fails
      honestly: all failures are pre-existing assertion drift — tests assert
      hardcoded UI text the app no longer renders. Wave 1 (2026-08-07) fixed
      Dashboard (6) and Book Detail (3): the api layer's `{ data: ... }`
      response envelope, the unmocked `/api/v1/system/storage` endpoint, the
      `/operations` → `/activity` route rename, and unmocked auth endpoints.
      Wave 2 (2026-08-08) cleared the remaining **34** chromium failures across
      four files — Error Handling 3, File Browser 8, Import Audiobook File 13,
      Operation Monitoring 10. (The per-file counts recorded here originally
      said File Browser 9 / Import 14; the measured baseline was 8 / 13, total
      34.) Root cause for 24 of them was the same missing `{ data: ... }`
      envelope in `web/tests/e2e/utils/test-helpers.ts` — `/auth/status` in
      particular meant `AuthContext` never initialized, degrading every mocked
      page. The rest was renamed-affordance drift. `operation-monitoring.spec.ts`
      needed a full rewrite: its target page was deleted in afe18e8f and
      `/operations` is now a redirect to `/activity`. No product code changed.

      **✅ VERIFIED 2026-08-08 06:48–07:08.** #2191 was merged with its suite
      result explicitly unverified — the agent that wrote the fixes stalled
      before it could run a final full pass. A complete `npm run test:e2e` has
      now been run against `main` at `60030428`:

          130 passed, 7 skipped, 0 failed, 0 flaky  (19.8m)

      The wave-2 counts were measured on **chromium only**, but `test:e2e` runs
      `--project chromium --project webkit`, so the suite is green on both
      engines.

      **⚠️ CORRECTION (2026-08-08 08:00).** The paragraph above originally also
      claimed the run "covers the Library changes merged after #2191 (#2193,
      #2195), confirming neither regressed the e2e suite." **That claim was
      false and has been removed.**

      `playwright.config.ts` sets `reuseExistingServer: !process.env.CI`, so a
      local run attaches to whatever already listens on 127.0.0.1:8484 instead
      of building. The process serving that port had been started at
      **00:31:50** — hours before #2193 (merged 05:11) and #2195. Every local
      e2e run after that point, including the 06:48 "verification" run, served
      a **stale frontend bundle** predating both fixes.

      What survives: **#2191 is still genuinely verified.** Its changes are spec
      and helper files, which Playwright loads from disk rather than from the
      server, so those ran as written. What does not survive is any claim about
      #2193 or #2195 — the served bundle did not contain them.

      **The trap is the lesson.** `reuseExistingServer` fails silently and looks
      exactly like success: a fully green suite that exercised week-old code.
      Anyone verifying a frontend change locally must confirm the server was
      built from their commit — check the listener's start time
      (`ps -o lstart -p $(lsof -ti :8484)`) or kill it first. Consider dropping
      the flag, or having the config refuse to reuse a server older than the
      working tree.

      **What this run does NOT verify** — recorded so the green result is not
      over-read:

      - There is **no e2e test for the In Progress / Finished sidebar filter**
        on `main`. #2193 is covered by unit tests only.

        A spec is drafted on branch `test/e2e-in-progress-filter` (commit
        `a167205e`, deliberately **not merged** — 1 of 5 tests green). The one
        that passes is the decisive one: *"clicking In Progress survives the URL
        settling with page=1"*, run against a **freshly built** app. That is the
        first real-browser evidence that #2193's harder half — the stuck
        `isInternalUpdate` guard that discarded the click — is genuinely fixed.
        The other four fail on `toHaveClass` against a filtered locator, which
        is a test-authoring problem rather than a product bug: MUI nests the
        label so the computed accessible name does not match, and the filtered
        locator does not resolve to the element carrying `Mui-selected`.
        Finishing those four would close the "not verified interactively"
        caveat permanently.
      - There is **no e2e test for the empty-state / warmup recovery** either.
        That acceptance test requires restarting the backend mid-session, which
        the suite does not do.

- [ ] **Move library filtering/search into the Go server as a real, declared
      query engine — and make unknown filters a hard error instead of a silent
      no-op.** Today the browser pulls pages of books and narrows them
      client-side. That does not scale to a 44,874-book / 284,735-file library:
      any filter that is not expressible as one of the handful of query params
      the Go handler happens to recognise either degrades into "fetch a lot and
      filter in JS" or silently does nothing at all. The server has the indexes,
      the memdb, and 48 cores; the browser has one thread and a network hop.

      **The hard constraint is browser memory.** The reporter's requirement is
      blunt and it is the thing to design against: *a single web page must not
      sit on ~10GB of RAM.* Client-side filtering over this library implies
      pulling the library — or a large fraction of it — into the tab, and at
      44,874 books that is not a tuning problem, it is the wrong architecture.
      The browser should never hold more than the page it is displaying plus a
      small window. Which query language wins (below) is genuinely open; this
      constraint is not.

      **Measured on prod 2026-08-08** against `GET /api/v1/audiobooks`
      (envelope is `{"data":{count,items,limit,offset}}`):

        limit=1                              count=44874   <- baseline
        limit=1&library_state=imported       count=18998   <- honoured
        limit=1&library_state=in_progress    count=0       <- honoured, no such value
        limit=1&status=in_progress           count=44874   <- IGNORED
        limit=1&progress=in_progress         count=44874   <- IGNORED
        limit=1&bogus_param_xyz=nonsense     count=44874   <- IGNORED

      The last three are the finding. **An unrecognised filter param returns the
      entire unfiltered library with HTTP 200.** There is no 400, no warning, no
      `applied_filters` echo — so a frontend that sends a param the backend does
      not implement is indistinguishable from a frontend that sends no filter,
      and the bug surfaces to the user as "this button does nothing" (see the
      companion In-Progress filter task). A typo'd param name is equally
      invisible. Note the second failure mode too: `library_state=in_progress`
      IS a recognised param, but `in_progress` is not one of its values, so it
      silently returns zero books rather than rejecting the value.

      **What to build:**

      - **A declared filter schema, server-side.** Enumerate the filterable
        fields, their types, and their legal operators/values in one place, so
        the handler can validate a request against it rather than
        `c.Query("...")` per field scattered through the handler.
      - **Reject what you cannot honour.** Unknown param, unknown field, or
        illegal value for a known field → `400` naming the offending param and
        listing what is valid. Fail closed. The current fail-open behaviour is
        why a broken filter can sit in the UI unnoticed.
      - **Echo what was applied.** Return `applied_filters` (and ideally
        `ignored`) in the response envelope so the client can render active
        filter chips from what the server actually did, not from what the client
        hoped it did.
      - **Composable predicates, not one param per question.** The user's ask
        was "maybe some GraphQL-like thing so we can do stuff dynamically." The
        requirement is dynamic AND/OR over typed fields with comparison
        operators, sorting, and pagination — evaluate a structured POST
        `/audiobooks/query` body (a small typed filter AST) against adopting
        GraphQL wholesale. A filter AST is far less machinery than a GraphQL
        server and keeps the existing REST surface; GraphQL earns its cost only
        if arbitrary client-chosen field selection is also wanted. Decide this
        explicitly and write down why.
      - **Server-side full-text/substring search** over title/author/narrator/
        series, so `search=` is not a client-side scan. Check what the existing
        `search=title:` syntax already supports before adding a second dialect.
      - **Concurrency.** Per CLAUDE.md, any predicate evaluated across the whole
        library must use a bounded worker pool (`errgroup` + `SetLimit` sized to
        `runtime.NumCPU()`), not a plain `for range books`.

      **Acceptance:**

      - Every filter the Library UI can express is computed by the Go server and
        returns a correct `count` for the whole library, not just the fetched
        page.
      - An unsupported filter or illegal value returns `400` rather than the
        full library.
      - No filtering pass over all books runs single-threaded.
      - **Measured:** with any filter or search applied, the browser tab's heap
        stays flat and bounded — take a DevTools heap snapshot with the Library
        page open on the full 44,874-book library and confirm the tab holds one
        page of results, not the library. This is the acceptance criterion the
        reporter actually cares about; the query-language choice is subordinate
        to it.

- [ ] **Fix the Library "In Progress" nav item — the selection highlight never
      moves, and the click is a genuine no-op.**
      🟡 **BOTH BUGS FIXED in #2193 (2026-08-08); two acceptance items remain —
      see "Still open" at the end of this entry.** Reported 2026-08-08, root-caused
      the same night. These are **two independent bugs** that happen to share a
      symptom. The control is not on the Library page: it is the Library sub-nav
      in the sidebar, `web/src/components/layout/Sidebar.tsx:53-62`:

          56: { text: 'All Books',   path: '/library?reset=1', matchPath: '/library' },
          57: { text: 'In Progress', path: '/library?search=read_status:in_progress' },
          58: { text: 'Finished',    path: '/library?search=read_status:finished' },

      **Bug 1 — the highlight can never move.** `Sidebar.tsx:163`:

          selected={location.pathname === (item.matchPath ?? item.path)}

      `location.pathname` never contains the query string. "In Progress" has no
      `matchPath`, so this compares `'/library'` against
      `'/library?search=read_status:in_progress'` — **always false**. "All Books"
      declares `matchPath: '/library'`, so it is **always true on any /library
      URL**. The indicator is therefore permanently pinned to "All Books".
      "Finished" is broken identically.

      Note: the obvious "compare pathname + search" fix is a trap. Once bug 2 is
      fixed the URL settles at `?search=read_status%3Ain_progress&page=1` — the
      write effect re-encodes the colon (`Library.tsx:605`) and unconditionally
      appends `page` (`614`) — so a raw string compare still fails. **Match on
      the decoded `search` param value, not the path string.**

      **Bug 2 — the click is a permanent no-op.** There is no dedicated
      selection state; the filter lives entirely in the URL `?search=` param,
      consumed by `pages/Library.tsx` (`useSearchParams` at 118; `searchQuery`
      seeded at 121/152; `parsedSearch` at 179; URL→state effect at 551-594;
      state→URL effect at 602-627; `isInternalUpdate` ref set at 624, consumed
      at 570-573).

      The ref **gets stuck at `true` after mount and stays true**. react-router
      7.18.2 rebuilds `setSearchParams` whenever `location.search` changes, and
      it is in the write effect's dep array (`Library.tsx:627`), so that effect
      re-fires on URL changes it did not cause. On a plain `/library` load: the
      write effect always appends `page` (614) and sets the flag; the next
      commit re-runs it, producing an identical `page=1` and re-arming the flag;
      because `location.search` is then unchanged, `useSearchParams` returns the
      same object and the sync effect never runs again to clear it.

      Clicking "In Progress" then hits the guard at `Library.tsx:570-572` and
      the incoming `search` is **discarded**, while the write effect rewrites
      the URL back to `page=1`. No `searchQuery`, no `parsedSearch`, no chip, no
      change to the request.

      **The asymmetry corroborates this:** "All Books" works and "In Progress"
      does not *from the same machinery*, because the `reset=1` branch is read
      at line 558 — **before** the guard at 570 — while `search` is read at 576,
      **after** it.

      **Cheap falsifiable checks — run these first; they also give users a
      workaround, and if any fails the diagnosis above is wrong:**

      - "Finished" is broken in exactly the same way.
      - A **hard refresh** of `/library?search=read_status:in_progress` **works**
        (mount-time seeding at 121/179 bypasses both effects).
      - **Dashboard → In Progress works** (Library mounts fresh);
        **/library → In Progress does not.**

      **The backend is fine — do not "fix" it.** Had `parsedSearch` ever been
      populated, `buildFieldFilters` (`Library.tsx:629-641`) would serialize to
      the `filters` param (`useLibraryQuery.ts:140` → `services/api.ts:964`),
      and the Go side splits per-user fields correctly
      (`internal/server/handlers/audiobooks/handler.go:435-448` →
      `internal/audiobooks/service_query.go:356-365`). `in_progress` is spelled
      consistently across `utils/searchParser.ts:59`, `Sidebar.tsx:57`, and
      `internal/audiobooks/service_types.go:124` — **the value-mismatch theory
      was investigated and disproved.**

      **Separate latent hazard, worth fixing while here.** Probing prod
      2026-08-08 showed `GET /api/v1/audiobooks` **fails open on unknown query
      params**: `bogus_param_xyz=nonsense` returned the entire 44,874-book
      library with HTTP 200, as did `status=in_progress` and
      `progress=in_progress`. Meanwhile `library_state=in_progress` is a
      recognised param with no such value and silently returns **zero** books.
      That did not cause this bug, but it is why a filter that silently does
      nothing can ship unnoticed — see the companion backend-filtering task.

      **Acceptance:** clicking the item moves the highlight, adds a filter chip,
      and changes the result count; the count reflects the whole library rather
      than the fetched page; and "Finished" works too. Also render the sub-items
      in collapsed-sidebar mode, where they currently are not rendered at all
      (`Sidebar.tsx:126-139`).

      ---

      **✅ Shipped in #2193 (2026-08-08).** Both root causes above are fixed.
      Bug 1: selection now goes through an exported `isSubItemSelected()` that
      compares the parsed, decoded `search` param instead of `location.pathname`
      — which also sidesteps the percent-encoding/`page=1` trap noted above.
      Bug 2: the stuck one-shot `isInternalUpdate` boolean is replaced by
      `lastWrittenSearch`, which compares the query string actually written;
      being idempotent, repeated identical writes are harmless and a genuinely
      different URL always gets through. "Finished" is fixed by the same change.
      The backend was confirmed not at fault and was not touched. Verified:
      432/432 frontend tests, `tsc --noEmit` clean, eslint 0 errors, plus a new
      `Sidebar.test.tsx` (11 cases) covering the encoded settled URL and a
      one-item-selected invariant.

      **🟡 Still open — do not close this entry yet:**

      1. **Collapsed-sidebar mode still does not render the sub-items at all**
         (`Sidebar.tsx:126-139`). Untouched by #2193, so In Progress/Finished
         remain unreachable when the sidebar is collapsed.
      2. **The result count still reflects the fetched page, not the whole
         library.** That is the companion backend-filtering task, not a sidebar
         concern — close it there rather than duplicating the work here.
      3. **Not verified interactively.** #2193's fix is reasoned from the code
         and covered by unit tests; nobody has driven the real app. The
         falsifiable predictions above double as the manual check: "Finished"
         should now work, and both should work whether arriving from Dashboard
         or from `/library`.

- [ ] **The Library must never show an empty "no items" state unless the
      library is genuinely empty (true first startup). Every other case shows a
      loading state and keeps retrying until books arrive.** Reported
      2026-08-08. Today a transient backend condition renders as "there are no
      books," which is the most alarming possible way to display a temporary
      failure to someone with a 44,874-book library.

      **Why this happens — measured 2026-08-08.** After `make deploy` restarted
      the service, `GET /api/v1/system/status` was **unreachable for roughly 40
      seconds** (curl exit with HTTP `000`, i.e. connection refused / no
      response) before it began returning `200`. The backend does a full memdb
      warmup over 44,874 books and 284,735 files on boot. So there is a
      guaranteed ~40s window on every single deploy during which the frontend's
      requests fail outright. Any UI that renders its empty state on
      `!loading && books.length === 0` will show "no books" during that window,
      because a failed request leaves the list empty without leaving it loading.

      **Root cause, located.** `web/src/components/library/LibraryBookGrid.tsx`
      line 183:

          {audiobooks.length === 0 && !loading && !searchQuery ? (

      That is the predicted bug shape exactly, and there is no error branch
      anywhere near it. The component's props (line 43) carry only
      `loading: boolean` — **there is no error/status prop at all**, so
      `LibraryBookGrid` is structurally incapable of telling "the request
      failed" apart from "the library is empty." The `manualImportError` /
      `bulkOrganizeError` state in `pages/Library.tsx` (lines 343, 372) covers
      import and organize actions, not the book-list fetch. So when the fetch
      fails during warmup, `loading` flips to false, `audiobooks` is empty,
      `searchQuery` is unset, and the page confidently announces an empty
      library.

      Fixing this therefore is not a one-line condition change: a fetch
      status/error has to be threaded from the data layer into this component
      first. Line 335 has the sibling branch for the searched-and-empty case and
      will want the same treatment.

      **The distinction the UI must make.** Three states are currently being
      collapsed into one:

        a) request in flight            -> spinner / skeleton
        b) request failed or server not ready -> "still loading…", keep retrying
        c) request succeeded, count == 0      -> the ONLY case that may say "no books"

      Only (c) is a real empty library, and it should additionally be
      distinguishable as first-run (nothing ever imported) versus "your filters
      matched nothing" — those want different copy and different affordances.

      **What to build:**

      - Gate the empty state on a **successful** response whose `count` is 0 —
        never on `books.length === 0` alone. An errored or not-yet-settled query
        must fall through to the loading branch.
      - **Retry with backoff, indefinitely, while the failure looks transient**
        (network error, 502/503, connection refused). Cap the delay (a few
        seconds) so recovery is prompt after warmup finishes, and surface a
        quiet "reconnecting…" note once the first retry fails rather than
        leaving a silent spinner forever. Do not retry forever on a 4xx — that
        is a real client bug and should surface.
      - Consider a **readiness signal from the Go side**: have the server return
        `503` with a `Retry-After` while memdb warmup is in progress, instead of
        refusing connections or returning an empty 200. An explicit "not ready
        yet" is far easier for the client to handle correctly than a dropped
        connection, and it makes the correct client behaviour obvious. Check
        whether a readiness/health endpoint already distinguishes "process up"
        from "warmup complete" — `systemctl is-active` reported the service
        healthy well before the API answered, so process-liveness is already
        known to be a misleading signal here.
      - Distinguish **first-run empty** from **filtered-to-empty** in copy.

      **Acceptance:** restart the backend with the Library page open. The page
      must show a loading/reconnecting state for the whole warmup window and
      then populate on its own with no user interaction — at no point may it say
      the library is empty.

      ---

      **✅ Shipped in #2195 (2026-08-08).** The core fix is in. `useLibraryQuery`
      no longer calls `setAudiobooks([])` on failure, so a failed refresh keeps
      the last known-good page instead of blanking the shelf. The empty-state
      decision moved out of the JSX into a pure `libraryContentState()` helper
      whose branch ORDER is the fix — `reconnecting` is evaluated before
      `empty`, so only a load that RESOLVED with zero results can claim the
      library is empty. Failed loads now retry with exponential backoff from
      500ms capped at 5s, indefinitely for transient failures (network,
      connection refused, 5xx) and never for a 4xx. Explicit cancel stops the
      loop; the timer is cleared on unmount. Verified: 442/442 frontend tests,
      `tsc --noEmit` clean, eslint 0 errors, plus `libraryContentState.test.ts`
      with an exhaustive sweep asserting `empty` is reachable only from a clean,
      settled, genuinely-zero result.

      **🟡 Still open — do not close this entry yet:**

      1. **The acceptance test above has NOT been run.** The fix is reasoned and
         unit-tested; nobody has restarted the backend with the Library page
         open and watched it recover. Until someone does, this is unverified
         against the actual failure it was written for.
      2. **No readiness signal from the Go side.** The server still refuses
         connections during memdb warmup rather than returning `503` +
         `Retry-After`. The client now copes either way, but an explicit "not
         ready yet" would be far more honest — and `systemctl is-active` is
         still a misleading liveness signal, reporting healthy ~40s before the
         API answers.
      3. **first-run empty vs filtered-to-empty copy is unchanged.** The
         existing state only branches on `importPaths.length === 0`.

- [ ] **Never accumulate more than 10 RCs on a version — cut the stable release
      instead.** Owner directive, 2026-08-08: *"we are never to get above 10
      RCs. Right now we have massive changes all bunched together. Doing it that
      way we have consistent releases."*

      **What triggered this.** On 2026-08-08 the repo was sitting on
      **`v0.217.9-rc.87`** — eighty-seven release candidates on a single
      version — while the last actual stable release was **`v0.217.4`, cut on
      2026-07-06**. A month of work, including several data-loss fixes and a
      library-wide reparse, had piled into one undifferentiated blob that nobody
      could review, bisect, or roll back to a known-good point. Three duplicate
      broken draft releases had also accumulated against the unused `v0.217.9`
      tag. (Cut as `v0.218.0` that night; drafts deleted.)

      **The rule.** When the RC counter for a version reaches **10**, the next
      step is a stable release, not `rc.11`. Every merge to `main` already mints
      an RC via `.github/workflows/prerelease.yml`, so ten RCs is roughly ten
      merged PRs — a reviewable unit.

      **Make it enforced, not remembered.** A rule that depends on someone
      noticing a counter is the same class of failure that let it reach 87:

      - **Fail or warn in CI at the threshold.** Have the prerelease workflow
        check the RC ordinal it just minted and, at `>= 10`, either fail loudly
        or open/refresh a "cut a release" issue. A passive dashboard will be
        ignored; the signal has to appear where the work is happening.
      - **Consider auto-promoting.** If ten RCs are green, cutting the stable
        release is mechanical — `release-prod.yml` already takes
        `release-type` and `previous-version`. Weigh auto-promotion against
        wanting a human gate; if a human gate is kept, the reminder must be
        unmissable.
      - **Clean up RCs on promotion.** `cleanup-rc-releases.yml` exists; verify
        it actually runs on stable promotion and prunes the superseded RC
        releases and tags, or 87 stale prereleases will keep cluttering the
        releases page.
      - **Watch for the duplicate-draft bug.** Three identical broken drafts for
        `v0.217.9` accumulated because the release path created a new draft
        rather than updating the existing one. Fixed for this repo by pinning
        `.github/ghcommon-ref.txt`, but confirm the draft path specifically.

      **Why it matters beyond tidiness.** With one stable release a month, "roll
      back to the last good version" means discarding a month of fixes. With a
      release every ~10 merges, a bad change is bounded by a handful of PRs, the
      release notes are short enough to actually read, and `git bisect` between
      two stable tags is a tractable search rather than 87 candidates.

      **Acceptance:** the RC ordinal never exceeds 10 without either a stable
      release being cut or CI complaining; and the releases page does not
      accumulate superseded RC entries.

- [ ] **~180 "bracketed series" are actually one shattered book each** — found by
  the `maintenance.series-denumber` dry run, 2026-08-06.

  The dry run flagged 198 series names carrying a bracketed number
  (`Dragon Born [04]`, `… Called Peace (12)`). Roughly 18 are genuine series
  positions. The rest are **one novel exploded into per-chunk series rows**:

  | Target base | Rows | Books | Reality |
  |---|---:|---:|---|
  | `Megan E. O'Keefe - Catalyst Gate` | 80 | 80 | one novel |
  | `Listening-to-ClassA-Threat-by-Dan-Sugralinov--Scribd` | 36 | 36 | scraped page titles |
  | `Listening-to-Arcane-Kingdom-Online-Dark-Magic-by-Jakob-Tanner--Scribd` | 27 | 27 | scraped page titles |
  | `The Light We Lost` | 25 | 25 | one novel |
  | `Arkady Martine - A Desolation Called Peace` | 12 | 12 | one novel |
  | `Dragon Born`, `Warbreaker`, `Guardian`, `Otherworld Academy`, … | ~18 | ~24 | **genuine** |

  🔴 **Do not resolve this by applying `applyMedium`.** That would manufacture an
  80-volume "Catalyst Gate" series out of a single book, and a 36-volume series
  out of a Scribd listing page. The denumber op deliberately holds them; the
  parser is behaving correctly, the *shape* is a lie.

  These belong to the **combine-into-one-book** track (The Successors class), not
  the series track: a bracketed `(47)` on a novel title is a disc/chunk marker
  that leaked into the series field. The `Listening-to-…--Scribd` rows are a
  distinct, narrower bug — a web scrape wrote page titles into series names, and
  those need their own cleanup rather than any kind of merge.

  Start from the report:
  `/var/lib/audiobook-organizer/series-denumber-2026-08-06.tsv`
  (`shape=bracketed`, group by `into_name`, anything with >3 rows is suspect).

- [ ] **Give the 466 low-confidence series positions somewhere to go** — deferred
  deliberately on 2026-08-06 (owner decision), revisit after owner items 1 and 2.

  `maintenance.series-denumber` now reports 466 series names carrying a bare
  number (`08. Battle for the Abyss` → position 8, `Station 64: The Doll Dungeon`
  → position 64) covering ~580 books. They are correct often enough to be worth
  surfacing and wrong often enough that they can never apply themselves —
  `86—EIGHTY-SIX` is a real series name in this library with the identical shape.

  Today they exist only in the `reportPath` TSV. Nothing consumes them.

  🔑 **Why no review-queue Kind was built yet:** a new Kind needs frontend
  mapping, and `review_apply_enabled` is OFF in production, so approving such a
  hold would mutate nothing. Wiring these in only makes sense once holds have
  real per-item actions — i.e. after owner item 1 (recommendations) and item 2
  (overrides). Doing it in that order avoids building a producer for a consumer
  that cannot act.

  Note for whoever picks this up: for `08. Battle for the Abyss` the parsed base
  is `Battle for the Abyss` — the BOOK's title. The real series (`Horus Heresy`)
  is not present in the string at all. So the low tier cannot be resolved by
  better parsing; it needs evidence from outside the name (sibling books sharing
  a folder/author with different leading numbers — spec D4, unbuilt), or a human.

  Design: [`docs/specs/2026-08-06-series-embedded-positions-design.md`].

- [ ] **react-router GHSA-qwww-vcr4-c8h2 — accepted, not reachable, do not
  re-litigate.** The v6 → v7.18.2 upgrade (2026-08-06) closed three advisories
  and opened this one. It is **expected**, it was a deliberate trade, and the
  analysis is recorded here so the next person to see the alert does not redo it.

  **The trade.** No single version closes all four. The three originals
  (open redirect via backslash in `<Link>`/`useNavigate`, open-redirect-to-XSS,
  arbitrary constructor injection via `deserializeErrors()`) are first patched at
  **7.18.0**. This one — RSC-mode CSRF bypass, vulnerable range 7.12.0–8.2.0 — is
  only fixed at **8.3.0**.

  **Why we did not go to v8.** It requires `react >= 19.2.7` (repo is on 18.2.0),
  and `react-router-dom` does not exist at 8.x at all (E404), so it would also
  mean rewriting all 49 import sites. That is a React-major migration, not a
  dependency bump.

  **Why the residual is not reachable.** It is an RSC-mode vulnerability and this
  is a plain SPA. Verified zero hits for `@react-router/*`, `react-server`,
  `unstable_RSC`, and `createStaticHandler`. The three closed advisories, by
  contrast, were in `<Link>` and `useNavigate` — code paths used constantly.
  Closing reachable risk while accepting unreachable risk is the right direction
  even though the alert count goes the wrong way.

  Revisit when the app moves to React 19 for its own reasons. Do not take the
  React major *for* this advisory.

- [ ] **Two frontend navigation sinks are unvalidated and safe only by
  accident.** Found 2026-08-06 while auditing the react-router open-redirect
  advisories. Neither is exploitable today. Both rest on an invariant that a
  future change breaks **silently** — nothing fails, nothing warns, the sink just
  becomes live.

  1. `web/src/pages/Login.tsx:78-81` — `location.state?.from` is passed straight
     to `navigate()` with no validation. Safe **only because nothing writes
     `state.from`** (zero writers in the codebase). Wire a `?returnTo=` param
     into it and it is immediately exploitable.
  2. `web/src/pages/BookDetail.tsx:938,968` — `sessionStorage`'s
     `library_return_url` goes to `navigate()` unvalidated. Safe **only because
     the writer runs on the exact routes** `/library` and `/fingerprints`.
     Changing that to `/library/*` makes it exploitable.

  The remedy is to validate at the sink rather than rely on the writer's reach:
  the Go side already does exactly this, and does it well —
  `sanitizeReturn` (`internal/server/handlers/oauth_login.go:260-271`) implements
  the backslash guard the advisory describes, and `abs/openid.go:246-257`
  validates `redirect_uri` before error redirects too. Mirror that on the client.

  🔴 **Do not "fix" [[TODO-SSO-EDGE]] / the OAuth-callback entry at `TODO.md`
  around line 1040 by loosening `sanitizeReturn`.** That entry is a *functional*
  gap — the guard correctly rejecting a custom-scheme return — not a
  vulnerability. Loosening it would convert a working defence into one of these.

- [ ] **The Playwright e2e suite is broken on `main` and gates nothing.** Every
  test dies at fixture collection with `unknown parameter "_page"` — 49 errors.
  Confirmed pre-existing on 2026-08-06: the identical failure reproduces on the
  pre-react-router-v7 tree with unchanged specs, and the v7 PR touched zero files
  under `web/tests/`.

  Why this matters beyond the red: the react-router v6 → v7 upgrade merged with
  **no runtime routing signal at all**. `tsc` was clean and 402 frontend unit
  tests passed, but nothing exercised actual navigation. A routing major landing
  without e2e coverage is precisely the case the suite exists for.

  Fix the fixture signature, then re-run against the v7 tree to retroactively
  confirm the upgrade — and treat `make test-e2e` as a required gate for any
  future routing or auth-flow change.

- [ ] **`UpsertBookToMemDB` holds go-memdb's global writer mutex across Pebble
  I/O.** Found 2026-08-06 while profiling `dedupe-book-file-rows` (fixed in
  #2161). This is a **system-wide ceiling on every `UpdateBook`**, not something
  specific to that op, and it is the natural next performance win.

  go-memdb has a single global writer mutex (`memdb.go:34-35`, `:73-76` — one
  writer at a time, `Txn(true)` takes `db.writer.Lock()`). Inside that lock,
  `UpsertBookToMemDB` performs three Pebble reads: `GetBookAuthors`
  (`memdb_sync.go:72`), `GetBookNarrators` (`:85`), and `loadBookFilesForBookID`
  (`:98` — a full prefix scan that unmarshals every remaining fingerprint-bearing
  row). Every other writer in the process waits on that I/O.

  Fix: fetch first, then take `Txn(true)`. Consequence worth stating — this is
  also why adding worker pools to book-level maintenance ops buys far less than
  `NumCPU×`: the workers serialize here regardless.

- [ ] **`DeleteBookFilesForBook` leaves stale memdb rows behind.** It never calls
  `DeleteBookFileFromMemDB` or `MarkQuickQueryDirty`, so Pebble and memdb diverge
  after it runs. Noticed 2026-08-06 while modelling `DeleteBookFilesByIDs` on it
  (#2161) — the new method does both; its model does not.

  Latent, and it pairs badly with the known "corrected aggregates are invisible
  until memdb refreshes" problem: a divergence here looks exactly like that
  staleness, so the two will be confused during diagnosis.

- [ ] **The 3 dangerous multidisc holds are DUPLICATES, not series — feed them to
  the duplicate-detection track.** Measured 2026-08-06 from a full pre-apply
  snapshot of all 132 pending `regroup.multidisc` holds (4,146 member books,
  zero unreadable).

  `TODO.md` predicted ~9 holds with book-length members, presumed to be series
  the guard would have caught. There are **3**, and all three are two-member
  holds whose members have near-identical runtimes:

  | hold | members |
  |---|---|
  | `01KXF8BNKENR530AKMMKJYD5E1` | `Brother Wulf` 6.30 h + `Brother Wulf - Joseph Delaney` 6.30 h |
  | `01KXF8BNKACGA6ZAEBPCQK09FX` | `Sevenfold Sword` 20.56 h + `Sevenfold Sword` 21.47 h |
  | `01KXF8BNHY7AE56CPZWY9VW9VF` | `The Warring Son` 11.77 h + `The Warring Son` 11.77 h |

  Same title, same runtime, two rows. That is the never-delete / re-associate
  shape, not a series of distinct novels.

  **The recommender emits `separate` for them, and that is the correct default.**
  Separate destroys nothing and leaves them for a signal that can actually
  establish identity. 🔴 Do NOT tune the recommender toward `duplicate-of` on
  runtime similarity — equal runtimes are not identity evidence. Two different
  books can share a runtime, and acting on that would merge distinct works
  through a path that hard-deletes the absorbed row.

  These 3 are a clean, small, real test set for
  [[never-delete-re-associate]] — use them rather than inventing fixtures.

- [ ] **Frontend framework versions — how far behind we actually are, and the
  order to fix it in.** Surveyed 2026-08-06 at owner request ("are we on
  TypeScript 7 and the latest React?"). Answer: **no to both.**

  | Package | Installed | Latest | Behind |
  |---|---|---|---|
  | `typescript` | 5.9.3 | **7.0.2** | 2 majors |
  | `react` / `react-dom` | 18.3.1 | **19.2.8** | 1 major |
  | `@mui/material` | 5.18.0 | **9.3.1** | **4 majors** |
  | `jsdom` | 23.2.0 | **30.0.1** | **7 majors** |
  | `vite` | 7.3.6 | 8.2.1 | 1 major |
  | `eslint` | 9.39.5 | 10.8.0 | 1 major |
  | `zustand` | 4.5.7 | 5.0.14 | 1 major |
  | `react-router` | 7.18.2 | 8.x | 1 major (gated on React 19) |
  | `vitest` | 4.1.10 | 4.1.10 | current |

  **React 19 is worth more than it looks, because it is also a security fix.**
  [[react-router-v8-residual-advisory]] (GHSA-qwww-vcr4-c8h2) is only patched in
  react-router **v8**, which requires `react >= 19.2.7` and does not publish
  `react-router-dom` at all. So "upgrade React" and "close that open high-severity
  alert" are one piece of work, not two. That changes its cost/benefit — do not
  price the React major as pure maintenance.

  **TypeScript 7 is not a version bump.** It is the native Go compiler rewrite —
  roughly 10× faster type-checking, but a different implementation with its own
  compatibility surface. Budget it as a migration.

  **MUI 5 → 9 is the largest single lift.** Four majors, and MUI majors move the
  styling engine and component APIs. `@mui/material` is imported across most of
  the UI, so this is the one that is genuinely days rather than hours.

  Suggested order, cheapest-value-first:
  1. **React 19 + react-router 8** — closes a live advisory, moderate scope.
  2. **jsdom + eslint + zustand + vite** — cheap, can ride along with (1).
  3. **TypeScript 7** — real migration, big payoff in CI time.
  4. **MUI 9** — largest, purely maintenance, do last.

  🔴 **Do not attempt any of this until the e2e suite is fixed.** See
  [[e2e-suite-broken-on-main]] — it currently dies at fixture collection and
  gates nothing, which is why the react-router v6 → v7 upgrade merged with zero
  runtime navigation coverage. A React major without e2e is exactly the change
  that suite exists to catch.

- [ ] **Per-file intro transcription as the primary book-identity signal** — owner
  design 2026-08-06. Storage and the first-file sort fix are **DONE** (PRs #2168);
  the parser, the tiered backfill, and the wiring are open.

  **The idea.** An audiobook opens with a spoken *"&lt;Title&gt; by &lt;Author&gt;, read by
  &lt;Narrator&gt;"* announcement. That announcement marks a book **start**. A file
  without one is a continuation. That is direct identity evidence, where the
  current classifier only has runtime — a proxy.

  **Why it needed per-file storage.** Transcripts lived on `Book`, so only ONE
  file's opening was ever captured and "12 files that are one book" was
  indistinguishable from "12 files that are 12 books". Measured on prod, one
  folder's files read:

  ```
  file 1: "This is a reading of Overlord, Book 7. This part includes the prologue and Chapter 1."
  file 2: "This is a reading of Overlord Volume 7. This part includes Chapter 2."
  file 3: "Hello... This is Overlord Volume 7, Chapter 3."
  ```

  Per-file that sequence is proof of continuation; per-book it is invisible. It
  also explains the measured **45.8%** credit-parse rate across 1,476 review-queue
  members — the op sampled one arbitrary file per book.

  ### Remaining work

  - [x] **Three-outcome parser.** ✅ DONE 2026-08-07 — `ClassifyIntro`
        (`internal/transcribe/classify.go`) returns credits / chapter / prose /
        unknown with a typed reason, confidence, and chapter number. **Position
        is a weight, never a veto**: credits at ordinal >0 IS the shattered-book
        signal, so vetoing it would hide the very finding this was built to
        surface. Both confirmed prod false positives are covered — the *Girls
        with Rebel Souls* case is reclassified as **misfiled** rather than
        mis-parsed (`IsLikelyMisfiled`: the announcement was read correctly, the
        FILE is in the wrong folder), and prose-containing-"by" now fails
        plausibility gates (case-sensitive prose markers, so "Meet **Me** in
        Paradise" survives while "...and **he** wasn't amused" does not).
        The corpus surfaced a larger defect than either: **24.8% of stored
        titles carried a leaked credit verb** ("Awakened Essence 1 Written")
        because the split landed *inside* `written by` — the library's most
        common credit variant (24.1%), absent from the pattern list entirely.
        Backed by a 188-transcript production corpus
        (`internal/transcribe/testdata/intro_corpus.jsonl`), invariant tests, a
        distribution canary, and a fuzz target (165k execs clean).
        🔴 `reparseStoredIntros` now **only upgrades, never clears**: 1.4% of
        987 sampled books (~644 library-wide) hold a parse their *current*
        transcript cannot regenerate, because `applyOutcome` overwrites the
        transcript unconditionally but the parsed fields only on success.
  - [ ] **Tiered backfill.** Naive "every file" is ~284,000 files ≈ 12–14 days of
        GPU. Tiers: **0** single-file books migrate by copy (zero GPU, ~32,600
        books); **1** assembled multi-file books probe the first 3 files only;
        **1b** escalate to the full set if all 3 carry credits — which is what
        makes the cheap tier *safe*, since it cannot silently be wrong; **2**
        bookless/shattered/queue members get every file; **3** a lazy, indefinite
        full sweep so every file eventually has a transcript.
  - [ ] **Wire into the regroup classifier**, outranking runtime where both exist.
        Validate by diffing against the 356 holds already measured under the
        runtime rule.
  - [ ] **Wire into First Aid** as a tier-2 signal beside the duration probe, and
        let the verdict pick the fixer.

  ### Measured facts worth keeping

  - 72.7% of books are single-file; 11.3% have 21+ files and hold most of the
    317,054 rows. The signal is precisely targeted at the fraction that is
    actually ambiguous.
  - **195 of 204** "untranscribed" review-queue members have ZERO `book_file`
    rows — unlinked, not un-transcribed. **Relink before transcribing** or they
    need a second pass. [[first-aid-library-validate-repair]]'s probe already
    found 434 of 1,019 directory-shaped books confidently linkable.
  - The WAV clip cache is keyed by **file path**, so clips already extracted
    survive the per-file move and ffmpeg is skipped on re-run.
  - Book-level transcription is already **saturated** — a full `only_missing` run
    over 221 pages transcribed 0 books. There is no warm-up value left; the
    per-file pass is the entire remaining work.

  🔴 **Absent transcript means "cannot verify", never "continuation".** This
  codebase has now been bitten by absent-value-read-as-evidence four separate
  ways: `DurationSec == 0` read as "short" (disabled the series guard across 97.5%
  of the queue), a 404 body read as "zero files", `memPtr == nil` read as "nothing
  to do" (silently dropped writes for the process lifetime), and an empty
  `intro_transcription` read as "needs transcribing" when it meant "has no file".

- [ ] **Stand up a second Whisper worker on the spare CPU node.** Owner request
  2026-08-06. Host prepared, worker not built. (Host address and credentials are
  fleet-internal — see the private infra notes, not this repo.)

  **Why it is cheap to try:** the transcription backend is already a pluggable
  HTTP service — `WHISPER_REMOTE_URL` points at a faster-whisper instance on the
  GPU host. Adding a second worker is a deployment question, not a code change.
  `internal/transcribe/batch.go:51` reads a single URL today, so the only code work
  is fanning out across several endpoints.

  **The node as measured 2026-08-06:** Ubuntu 26.04, 48 cores, 251 GB RAM,
  **no GPU**. Its Tdarr node registers CPU-only with `transcodegpu: 0`, the Tdarr
  queue is **empty** (`table1Count: 0`), and both node processes sit at 0.0% CPU —
  so nothing needs stopping to free it. Python 3.14.3 with pip 25.1.1 and uv 0.12.2
  (both installed 2026-08-06).

  🔴 **CPU-only is the whole caveat.** faster-whisper with int8 quantisation on 48
  cores is real, but it is **not** a second GPU. **Benchmark against a real clip
  batch before promising throughput** — do not assume it halves the backfill.

  **Prefer an HTTP endpoint over the in-process `uv` path.** `whisper.go` also has
  `runPythonWhisper` (`uv run --with openai-whisper whisper`), and uv is now
  installed so that route works — but `batch.go:54-57` warns it loads the full
  model into RAM and *"reliably OOMs the server"* at batch sizes of 100–200. That
  warning was written about the **web-serving host**; the spare node has 251 GB and
  serves nothing, so the reasoning does not transfer directly. Even so, a second
  HTTP endpoint matches the existing interface, avoids the OOM class entirely, and
  needs no special batch sizing.

  **Point it at tier 3 first.** The lazy full sweep in
  [[per-file-intro-identity-signal]] has no deadline, which makes it the natural
  consumer for a slower worker — "slower than GPU" costs nothing there, while the
  decision-critical tiers keep the GPU.

- [ ] **Investigate: 79% of books with a stored transcript are marked
  `whisper_error`.** Found incidentally while sampling a corpus for the
  three-outcome parser (2026-08-07), not chased — it is out of scope for the
  parser work but nobody will stumble on it otherwise.

  **The measurement.** A random offset-based sample of **987 distinct books that
  all have non-empty `intro_transcription`** breaks down by `transcribe_status`
  as:

  | status | count | share |
  |---|---|---|
  | `whisper_error` | 783 | **79.3%** |
  | `ok` | 177 | 17.9% |
  | `unparsed` | 26 | 2.6% |
  | `empty` | 1 | 0.1% |

  Every one of those 783 rows **has transcript text stored** while its status
  says the transcription failed. Status and content have drifted apart across
  what looks like most of the library.

  **Why it probably happens.** `applyOutcome`
  (`internal/plugins/maintenance/intro_transcribe.go`) writes
  `TranscribeStatus` on every outcome, but only writes `IntroTranscription` when
  the outcome carries a transcript. So a book transcribed successfully once and
  then re-attempted later — after the file moved, the GPU host went away, or the
  batch failed — keeps its old text and acquires a failure status. That is the
  same *shape* as the parse-vs-transcript divergence the parser PR guards
  against, one field over.

  **Why it matters.** Anything filtering on `transcribe_status == "ok"` is
  currently ignoring ~4 out of 5 books that actually have usable transcript text.
  Worth checking whether the tiered backfill's "needs work" query is one of them
  before it is sized — it would massively over-count the work remaining.

  **Do not assume it is a live failure.** The status could be a stale record of a
  historical outage rather than an ongoing one. Check
  `transcribe_attempted_at` vs `intro_transcribed_at` on the affected rows first:
  if attempted is consistently much later than transcribed, this is drift from
  old re-runs, not a currently-failing pipeline. 🔴 The distinction changes the
  fix completely, so measure before concluding.

  Related: [[per-file-intro-identity-signal]].

- [x] 🔴 **Data race: `UpsertBookToMemDB` retains the CALLER's `*Book` and
  dereferences it later on the warmup goroutine.** Caught by the race detector
  on CI during PR #2170 (a parser PR that touches no database file).
  ✅ FIXED 2026-08-07 (fix/memdb-warmup-caller-pointer-race): snapshot copied at
  enqueue time in `UpsertBookToMemDB` and every same-shape sibling upsert
  (BookFile/Author/Series/Narrator/ImportPath/AuthorAlias/BlockedHash + slice
  copies in ReplaceBookAuthors/NarratorsInMemDB). Regression test
  `TestUpsertBookToMemDB_SnapshotsCallerBookAtEnqueue` forces the interleaving
  deterministically under `-race`; verified it fires the race on the unfixed
  code and is green on the fix.

  **The race, verbatim from CI:**

  ```
  WARNING: DATA RACE
  Read at 0x00c000a96388 by goroutine 13725:
    database.stripBookForMemdb()        memdb_strip.go:33      // cp := *src
    database.UpsertBookToMemDB.func1()  memdb_sync.go:123
    database.applyMemSync()             memdb_sync.go:92
    database.publishWarmMemStore()      memdb_pending.go:211
    database.NewPebbleStore.func1()     pebble_store.go:320    // async warmup

  Previous write at 0x00c000a96388 by goroutine 13700:
    database.(*PebbleStore).UpdateBook() pebble_store.go:1827  // book.ID = id
    database.TestBook_TranscribeFields_RoundTrip()
                                        transcribe_stats_test.go:99
  ```

  **Mechanism.** `UpsertBookToMemDB` (`memdb_sync.go:114`) captures the caller's
  `book` pointer in a **closure** and hands it to `p.memSync`. While the store is
  still warming, that closure is not run inline — it is queued as a pending op
  and applied later by `publishWarmMemStore` → `applyMemSync`. So
  `stripBookForMemdb(book)`'s `cp := *src` reads the caller's **live** struct at
  an arbitrary later time. `CreateBook` (`pebble_store.go:1812`) and
  `UpdateBook` (`:2060`) both pass the caller's pointer in, and `UpdateBook`
  itself writes to it (`book.ID = id`, `:1827`).

  **Why it matters beyond the test.** This is not a test-only bug. Any caller
  doing the ordinary

  ```go
  b := &Book{...}
  store.CreateBook(b)
  b.SomeField = x        // caller mutates its own struct
  store.UpdateBook(b.ID, b)
  ```

  races with warmup whenever the store is still warming — which is exactly
  startup, when backfills and migrations run. A torn read here writes a
  half-updated Book projection into memdb. Same family as the memdb warmup
  write-loss fixed in #2166 and [[feedback_memdb_roundtrip_footgun]].

  **The fix (one line, at the enqueue boundary):** snapshot the struct when the
  op is *queued*, not when it is *applied*.

  ```go
  func (p *PebbleStore) UpsertBookToMemDB(ctx context.Context, book *Book) {
      if book == nil { return }
      snapshot := *book // copy NOW — the closure may run much later, on another goroutine
      p.memSync("UpsertBook", func(txn memTxn) error {
          if err := txn.Insert(memTableBooks, stripBookForMemdb(&snapshot)); err != nil {
  ```

  Check the sibling upserts (`UpsertBookFileToMemDB`, author/series equivalents)
  for the same shape before calling it done — the closure-captures-caller-pointer
  pattern is likely repeated.

  **Reproduction is timing-dependent.** The full `internal/database` package
  under `-race` passed locally (0 races, 305s) and `TestBook_TranscribeFields_
  RoundTrip` passed 15/15 in isolation; it fired on CI under coverage
  instrumentation. 🔴 Do NOT treat a green local run as evidence the race is
  gone — the regression test must force the interleaving (e.g. mutate the caller
  struct immediately after `CreateBook` while warmup is still pending) rather
  than hoping to catch it.

- [ ] **Version-group acoustic audit op** — verify that books marked as VERSIONS
  of each other are acoustically close enough to actually be the same work, and
  auto-fix ones that are not. Requested by owner 2026-08-05; not scheduled.

  Structurally different from the rest of the First Aid roster: every other op
  *finds* problems, this one *audits assertions* — including First Aid's own
  writes. Tier 3 creates version groups from duration matching; this re-checks
  them with a signal that took no part in that decision, so a wrong grouping
  becomes findable instead of permanent. Also covers groups created by any other
  path (`ApplyVersionGroup`, manual, historical imports).

  Signals: (1) AcoustID fingerprint similarity across members —
  `BookFile.AcoustIDFingerprint` plus `AcoustIDSeg0..6`; (2) Whisper
  transcription content (owner suggestion) — an *independent* signal, not a
  refinement of the acoustic one, which is what makes agreement meaningful.
  ~96.5% transcribed but ~40% low-quality/unparsed, so filter before trusting.

  🔴 **Absent evidence must mean "cannot verify", never "refuted".** ~65% of
  books were unfingerprinted as of 2026-07-02. Reading a missing fingerprint as
  "not a match" would ungroup correct version groups wholesale — the same failure
  as `DurationSec == 0` silently disabling the regroup series-guard across 97.5%
  of the review queue. Emit verified / refuted / insufficient-evidence.

  Auto-fix is safe here in a way deletion is not: the remedy is to UNGROUP (clear
  `VersionGroupID`, restore `IsPrimaryVersion`), destroying no rows and no files,
  and itself reversible. Still gate behind a confidence threshold and prefer a
  review hold when the two signals disagree.

  Home: tier 2 of the First Aid funnel (expensive, runs only over version-grouped
  books), feeding a tier-3 ungroup fixer. See
  `.worktrees/link-integrity/PLAN.md`.

- [ ] **Verify the server actually returns chapters to clients** — confirm the
  ABS-compatible surface serves chapter data wherever a client expects it, and
  that it is populated rather than an empty array. Owner request 2026-08-05.

  Chapter extraction and persistence shipped in the ABS sync work (Phase 1,
  chapter-extraction + scanner chapter hook), so the plumbing exists — what is
  unverified is the end-to-end path: extracted → persisted → serialized into the
  item payload → rendered by AudioBooth / Absorb.

  Check specifically:
  - the item detail response includes a populated `chapters` array (start/end/
    title), not `[]`, for books that genuinely have chapters
  - single-file M4Bs with embedded chapter atoms
  - multi-file books, where "chapters" and "tracks" are different concepts and
    the client may expect one, the other, or both
  - what a client sees for a book with NO chapter data — a graceful absence, not
    a malformed payload

  ⚠️ An empty array and a missing field are different failures to a client, and
  the ABS conformance harness (`internal/syncapi/conformance`) checks field
  presence and type rather than just values — use it rather than eyeballing JSON.

  Feeds [[chapters-backfill-from-duplicates]]: knowing which books lack chapters
  is the input to deciding which ones to repair.

- [ ] **Backfill chapters into files that lack them, using a duplicate as the
  source of timings** — owner request 2026-08-05. Turn a chapterless M4B into a
  properly chaptered one by borrowing structure from another copy of the same
  book that already encodes it.

  Sources of chapter timings, in preference order:
  1. **Audible/provider chapter data** — check whether the metadata providers we
     already query expose chapter titles WITH start offsets. If they do this is
     by far the cleanest path and needs no duplicate at all.
  2. **A per-chapter duplicate.** A chapterless `Book.m4b` alongside a duplicate
     stored as N mp3s, one per chapter: each file's duration gives a chapter
     length, and the cumulative sum gives the offsets. Filenames often give the
     titles.
  3. **A playlist with timings** (see [[playlists-full-support]]) — cue sheets
     and some playlist formats carry explicit offsets.

  🔴 **GATE ON NEAR-EXACT ACOUSTIC MATCH.** Owner was explicit. Chapter offsets
  borrowed from a *different edition* — different narrator, abridged vs
  unabridged, a remaster with different silence padding — are worse than no
  chapters at all: they read as correct and silently mis-seek. Require an
  AcoustID fingerprint match well above the ordinary dedup threshold, and reject
  on ANY duration mismatch beyond a small tolerance. Absent fingerprint must mean
  "cannot apply", never "assume it matches" — same rule as
  [[version-group-acoustic-audit]].

  Also verify the summed chapter durations reconcile to the target file's total
  runtime before writing; a shortfall means the duplicate is incomplete (the
  Successors debris covered 12 of 13 tracks, which would have silently truncated).

  Write path: chapters go into the M4B container. Treat it as a tag write with
  the usual safety — this repo's dominant incident class is write-back wipes, and
  `books/itunes/**` remains hands-off regardless.

  Depends on [[chapters-served-to-clients]] to know which books lack chapters.

- [ ] **Playlists — implement the whole surface** — owner request 2026-08-05:
  "basically implement everything to do with playlists, dynamic playlists,
  static, etc."

  Scope:
  - **Import** existing playlist files found during scan — `.m3u` / `.m3u8`,
    `.pls`, `.cue`, `.xspf`. Resolve their entries to `book_file` rows rather
    than storing raw paths, so a later reorganise does not break them.
  - **Static playlists** — user-curated, explicit ordered membership.
  - **Dynamic playlists** — a stored query (by author, series, narrator, genre,
    unfinished, recently added, rating…) evaluated at read time.
  - **CRUD + reorder** via API, and expose over the ABS-compatible surface so
    iOS clients see them. Check what ABS calls these and match its shape — the
    conformance harness (`internal/syncapi/conformance`) is the tool for that.
  - **Export** back to `.m3u`.

  Two reasons this is worth more than it looks:
  1. **Cue sheets and some playlists carry explicit timings**, which makes them a
     third source of chapter offsets for [[chapters-backfill-from-duplicates]].
  2. An imported playlist is **evidence about grouping** — a playlist listing 13
     files in order is a human-authored assertion that those files belong
     together, which is exactly the signal the regroup classifier lacks and has
     to infer from filenames.

  ⚠️ Playlist entries pointing at files with no `book_file` row will silently
  drop — 38.2% of books were in that state on 2026-08-05, so sequence this after
  relink or import will look lossy for reasons that have nothing to do with
  playlists.

- [ ] **Reading status and review/rating must sync from the app back to the
  server** — owner request 2026-08-05: set it in the app, it persists server-side.
  Mirror how Audiobookshelf does it rather than inventing a shape.

  Two distinct things:
  - **Reading status** — not-started / in-progress / finished, plus the
    finished-at timestamp. ABS models this as `isFinished` + `finishedAt` on the
    media-progress record, and clients set it both explicitly ("mark finished")
    and implicitly (progress crossing a completion threshold).
  - **Review status** — the user's own rating and/or written review. ABS core
    does NOT have a first-class review object, so check what the iOS clients
    actually send before designing; this may need to be our own field exposed in
    a way clients tolerate.

  Prior art in-repo: Phase 6 ABS progress writes already landed (6 endpoints,
  `hideFromContinueListening` PATCH persistence, bookmarks — PR #2102), and
  `remove-from-continue-listening` was fixed in #2116. Reading status likely
  belongs alongside that media-progress work rather than as a new subsystem —
  look there first.

  Verify against real clients, not just the spec: AudioBooth and Absorb differ in
  which endpoints they call and when. The conformance harness checks field
  presence and type, which is what catches a client silently ignoring a field we
  thought we were sending.

  ⚠️ Round-trip matters more than write-once here. A finished flag that persists
  but never comes back on the next sync reads to the user as data loss, and it is
  the kind of bug that only shows up after reinstalling the app.

- [ ] **Use Deluge as a metadata and identity source** — owner idea 2026-08-05:
  "connect to deluge, see all the audiobooks it has, the titles it has, any other
  information and use that as well as other things to really figure out and match
  a book."

  Deluge's RPC exposes, per torrent: the torrent NAME, the save path, total size,
  the full file list, and dates. That name is often far richer than anything in
  the file's own tags — release names routinely carry author, series, volume
  number, narrator, edition (Unabridged), year, and format, in a structured-ish
  convention.

  Why this is a genuinely different signal from everything we have: every current
  identity source is downstream of the file itself (embedded tags, filename,
  folder, audio fingerprint). The torrent name is an **external, human-authored
  assertion made at acquisition time**, before any of our import processing could
  mangle it. For books whose tags were destroyed by the iTunes import, it may be
  the only surviving record of what the thing actually is.

  Work:
  - Deluge RPC client (read-only), credentials handled like other secrets — env,
    never the config blob.
  - Match torrents to library books by save path first (exact and prefix), then
    by file size, then by fuzzy title.
  - Parse release names into candidate metadata, and treat the result as a
    *scored candidate* feeding the existing matcher — never an authoritative
    overwrite. Scene naming is inconsistent and a confident parse of a wrong name
    would be worse than no parse.

  Pairs with [[deluge-file-parts-grouping-check]], which uses the same connection
  for a different purpose.

- [ ] **Use Deluge's per-torrent file list as ground truth for GROUPING** — owner
  idea 2026-08-05: "Deluge shows you all the file parts, we could easily pull
  that for all torrents and then match them to their files and if some groups are
  wildly wrong we know something is fucked up."

  This is the more valuable half of the Deluge idea, and it is a different kind
  of signal from [[deluge-metadata-source]].

  **A torrent's file list is an externally-authored statement that these N files
  belong together.** Everything the regroup classifier does is an attempt to
  RE-DERIVE exactly that fact from filenames and durations, after the fact, with
  known failure modes — it nearly merged 41 of 43 candidate groups that were
  really separate novels. Where a torrent covers a book, we do not have to infer
  the grouping; we can read it.

  Uses, in increasing ambition:
  1. **Audit** — compare our grouping against torrent membership. A torrent whose
     files we split across many books, or several torrents we merged into one
     book, flags a grouping error. This is a cheap, high-signal correctness check
     over a population we currently have no independent check for.
  2. **Evidence** — feed torrent membership into the regroup classifier as a
     strong positive grouping signal, outranking filename heuristics.
  3. **Repair** — propose regroups directly from torrent membership (review-gated
     like every other regroup proposal; never auto-applied).

  Caveats worth stating up front:
  - Coverage is partial — only books acquired this way, still seeded, still known
    to Deluge. Absent coverage must mean "no opinion", never "wrong".
  - A torrent may contain SEVERAL books (a series pack, an author collection), so
    torrent membership is an upper bound on one book, not proof of one book. Same
    over-merge trap as the folder heuristic — pair it with the duration guard.
  - Files may have been moved or renamed since; match on size and content, not
    only on path.

  Blocked on the same read-only Deluge RPC client as [[deluge-metadata-source]].

- [x] **Give review holds a real recommendation, and let the human override it**
  — owner items 1 and 2 (2026-08-05, shipped 2026-08-06 in PR #2163). Two halves
  of one change, done together as planned.

  **Outcome.** 286 of 356 pending holds now carry a decisive recommendation with
  the numbers behind it. The prerequisite this entry warned about turned out to
  be already met: the 2026-08-05 relink populated `book_file` rows, and a probe
  of all 1,831 member books found 1,593 with a real summed duration. The queue's
  item count had barely moved (367 → 356), which read like nothing had changed —
  but the *evidence* had arrived and the classifier simply was not using it.

  🔴 **A data-loss path was found and closed on the way.** The chosen action was
  originally stored nowhere: `SetReviewItemStatus` wrote status only, and
  `ReplayApprovedItems` re-derived the action from the payload's
  `recommendedAction`. A `combine` hold overridden to `separate` would have been
  recorded as plain `approved` and later **replayed as `combine`**, hard-deleting
  rows for books a human explicitly said to keep apart — with nothing connecting
  the destruction back to the click. Fixed by persisting the decision
  (`ReviewItem.ChosenAction` + `SetReviewItemDecision`, status and action in one
  Pebble batch). Two paired replay tests pin it, and both fail under mutation.

  ⚠️ **Read before flipping [[multidisc-apply-canary]].** Dispatch now keys on
  the chosen action rather than `Kind`. Under `Kind` dispatch `regroup.ambiguous`
  had no handler and could never merge; now an ambiguous hold recommending
  `combine` (24 of 356) reaches `ApplyMultidisc`. Intended, but it widens the
  blast radius of turning `review_apply_enabled` on.

  **The problem.** `proposedAction` is one generic string on **762 of 777** holds
  ("review: flat folder shares a title but ordering is unclear") and
  `survivorTitle` is frequently wrong. A queue where every row says the same
  thing is a queue nobody can work.

  **Recommendation.** Add structured fields to `regroupPayload`:
  `recommendedAction` (`combine` / `separate` / `duplicate-of` /
  `insufficient-evidence`), `recommendationReason`, and
  `recommendationEvidence` — the numbers that produced it (member durations,
  distinct stem count, part/disc marker count, folder shape). The evidence field
  is what makes the queue workable; a reason alone is just a nicer generic string.

  **Override.** `ApproveReviewItem` takes an optional body `{action: "..."}`
  defaulting to `recommendedAction`, and dispatch keys off the CHOSEN action.
  Today `approveOne` (`internal/server/handlers/review/handler.go`) dispatches on
  `item.Kind`, so this is the structural change that makes override possible.
  Keep the four `Kind` strings unchanged — they are load-bearing and the frontend
  maps them verbatim.

  `separate` needs no apply handler: every member is already its own book, so
  "separate into N" is a status transition, and `UpsertReviewItem`'s dedup-key
  idempotency keeps it decided across re-scans.

  **Also fix `deriveSurvivorTitle`**, which reads the folder name only and so
  returns author names ("C. T. Phipps"), "Volume 1", and wrong volume numbers
  ("…Vol. 01" on a folder whose files say Vol. 9). `folderNamedAfterBook` and
  `dominantPrefix` are already computed a few lines above — use the folder name
  when the former is true, the dominant member title when it is not, and emit
  empty rather than a wrong title when neither is trustworthy.

  🔴 **Sequencing.** The decisive signal is member `DurationSec`, which was ZERO
  for 97.5% of the queue because those books had no `book_file` rows. Do this
  AFTER [[relink-unlinked-books]] and a regroup re-run, or the recommendations
  are computed on blank evidence — the same failure that let 41 of 43 "confident"
  candidates propose merging distinct novels.

- [ ] **Canary the multidisc applies behind a before/after snapshot** — owner
  item 3 (2026-08-05). 138 pending `regroup.multidisc` holds; running them
  requires flipping `review_apply_enabled`, which is OFF in prod.

  🔴 **SNAPSHOT TO A FILE ON DISK BEFORE FLIPPING THE FLAG.** Capture, per
  candidate: every member book ID, title, duration, file path, and which ID
  `pickPrimary` will select (smallest ULID —
  `internal/plugins/maintenance/regroup_apply.go`). The apply path **hard-deletes
  absorbed rows**, so post-hoc reconstruction is impossible; the on-disk snapshot
  is the only record.

  That snapshot is not theoretical caution: it is what caught **41 of 43**
  "confident" multidisc candidates that would have merged distinct novels into
  single books. Do not skip it because the classifier looks better now.

  🔴 **Approve by explicit `ids:[...]`, never kind-scoped.** The frontend's
  `handleBulkAction(kind, 'approve')` approves EVERY pending item of a kind — one
  click with the flag on fires 138 `CombineBooks` calls. Start with a handful of
  groups verifiable by ear, diff the snapshot, then widen.

  Note a separate finding worth checking first: a 2026-08-05 measurement found
  **9 of 138** multidisc holds have members that are individually book-length,
  meaning the series-guard would fire on them if it were evaluated. The guard
  only applies to the flat branch — the disc and chapter/edition branches do not
  check it. Those 9 are near-misses still sitting in the queue.

  Depends on [[review-queue-recommendations-and-overrides]] (per-item action
  selection) so approval targets one hold at a time.

- [x] **Series names that are really book numbers** — owner item 4
  (2026-08-05, shipped + applied to production 2026-08-06, PR #2156).

  `maintenance.series-denumber` now reads the embedded shapes as well as the
  trailing ones, each scored by confidence. Applied on production: **25 series
  merged into 21 base series, 52 books given a real series position, 0 failures**;
  a re-run confirmed the high tier drained 25 → 0 with the other tiers untouched.

  🔴 **This was a DATA bug, not a display bug** — the number belongs in the
  series *position* field, not baked into the series *name*. Kept here because
  the owner corrected that reading twice; do not re-derive it.

  What the tiers are for, in the production data:
  - **high** (keyword-vouched, e.g. `Evil Genius: Book 4: …`) — applied.
  - **medium** (bracketed, e.g. `Dragon Born [04]`) — 198 rows, **NOT applied**.
    ~180 of them turned out to be shattered-book debris, not series positions.
    See the follow-up task below.
  - **low** (bare number, e.g. `08. Battle for the Abyss`) — 466 rows, reported
    only, and unappliable by construction. `86—EIGHTY-SIX` is a real series name
    in this library with the identical shape.

  Rollback artefacts on the server:
  `/var/lib/audiobook-organizer/series-denumber-{,APPLY-,VERIFY-}2026-08-06.tsv`.

- [ ] **"First Aid" — one sequenced library validate + repair system** — owner
  design 2026-08-05: *"one big system that basically had a investigation →
  retesting with more advanced situations → fixers."* Architecture and locked
  decisions: [`.claude/notes/2026-08-05-first-aid-architecture.md`].

  **Three tiers, separated by what they can afford PER BOOK:**
  - **Tier 1 — investigation.** All ~44,887 books. Budget: one DB read + one
    `os.Stat`. Cannot afford duration probing, hashing, or cross-book comparison.
  - **Tier 2 — escalation.** Only tier 1's flagged set (thousands), so it CAN
    afford probing real durations, matching a candidate's tracks against other
    books, and fingerprint comparison.
  - **Tier 3 — fixers.** One per confirmed verdict, small and independently
    testable.

  **Convergence is the property that matters.** Rather than hard-coding
  "relink before regroup", run fixers then RE-INVESTIGATE; the next pass sees the
  new durations and reclassifies. Re-run until investigation returns nothing
  actionable — idempotent by construction.

  **Sub-tasks still open:**
  - [ ] Tier-2 duration probe for the **1,019** directory-shaped books that went
    to review purely because `classifyUnlinked` passes `nil` durations. They are
    un-probed, not unknowable.
  - [ ] Duplicate detection + **combine-by-template** + version-group (the
    Successors class) — see [[never-delete-re-associate]] below.
  - [ ] Orchestrator + frontend button, dry-run by default, no schedule.
  - [ ] **Missing-input triggering:** when a check's input is absent, ENQUEUE the
    op that produces it. `OperationDef.Requires` already supports
    `ReqOpCompleted` (with `AllFiles`) and `ReqFieldSet`, with a dependency graph
    and `waiting_deps` parking — but parking WAITS and never enqueues the
    producer. First Aid must own that step. ⚠️ That subsystem shipped flag-OFF
    and dormant (#1442) with `dedup.check-book` as its only consumer; its one
    review caught three real bugs including a promote path that never dispatched.

  **Roster — ops to sequence** (tier 1) `relink-unlinked-books` ·
  `reconcile-scan` · `orphan-book-files-cleanup` · `dedupe-book-file-rows` ·
  `purge-millisecond-durations` · `booksig-recovery-audit`; (tier 2)
  `duration-reextract` · `file-integrity-check` · `malformed-m4b-remux/transcode`;
  (tier 3) `duration-backfill` · `repair-junk-titles` · `title-repair` ·
  `title-backfill` · `series-denumber` · `regroup-shattered-ai`; (tier 4, GATED)
  author/series identity ops → `metadata-refresh` · `isbn-enrichment` ·
  `auto-match-transcribed`.

  **Excluded as janitorial** (server health, not book correctness):
  `purge-deleted` · `tombstone-cleanup` · `temp-file-cleanup` ·
  `cleanup-activity-log` · `purge-old-logs` · `cleanup-old-backups` ·
  `trash-cleanup` · `archive-sweep` · `db-optimize` · `optimize` ·
  `batch-poller` · `bulk-write-back` · `intro-transcribe` · `extract-wav-clips`.

  Dedup subsystem stays SEPARATE but shares the duplicate-matching logic — it has
  its own queue, gold labels and calibrated thresholds, and folding it in
  wholesale is how 57 ops accumulated.

- [ ] **Never delete — re-associate (duplicate resolution)**. Deleting a
  redundant book row is **not idempotent**: rescan regenerates a book for any
  file no `book_file` row claims, so deleted rows come back. `block_hash`
  (`DoNotImport`) suppresses that but makes real audio permanently unrecoverable.
  Resolution: (1) detect that a group's tracks map onto a better-assembled book;
  (2) combine the debris into one book using that book's track list as a
  **template**, matching by duration instead of guessing boundaries from
  filenames; (3) version-group them, primary = most complete (ties to earliest
  ULID). Debris is not always a clean copy — The Successors debris was 11 rows /
  17 files covering 12 of 13 tracks with 5 internally-redundant files.

- [x] **Warm the metadata-results build at boot** — owner item 6 (2026-08-05,
  shipped 2026-08-06).

  The metadata-results build took **34 s cold**. It was memoised (60 s TTL, PR
  #2142) but not warmed at startup, so the first person to open the match UI
  after a restart ate the full 34 s.

  Shipped as `warmMetadataResultsCache`
  ([`internal/server/metadata_results_warmer.go`](internal/server/metadata_results_warmer.go)),
  enrolled in `startCacheWarmers` alongside the authors/series/facets/library-list
  warmers, with `metadata_results_warmer_test.go` asserting both that it degrades
  rather than panics on a nil store and that the cache is genuinely populated
  afterwards.

  ⚠️ Note for anyone auditing this later: the stale-while-revalidate work merged
  the same night (PRs #2153/#2154, 46× measured on prod, 28.9 s → 0.63 s) is a
  **different** fix for the same symptom. SWR keeps a warm cache from going cold
  under load; it does not help the first request after a restart. Both were
  needed, and only the warmer closes this item.

- [x] **Relink unlinked books — detector + repair op** — owner item 5
  (2026-08-05). Op `maintenance.relink-unlinked-books` shipped in PR #2147.

  **The measurement.** A whole-library survey found **17,149 of 44,887 books
  (38.2%)** own ZERO `book_file` rows — not the ~1,300 originally estimated.
  Disk check of every one of those paths: **16,027 resolve to a real file, 1,029
  to a directory, 93 are genuinely missing.** They are **unlinked, not orphaned**
  — the remedy is to relink, never to delete.

  **Why no existing op saw them.** `maintenance.reconcile-scan` flags a book only
  when `os.Stat` on its path FAILS. These all stat fine, so it walked past every
  one and reported the library healthy.

  🔴 **Why this blocked everything else.** `regroup-shattered-ai` derives
  `DurationSec` by summing `book_file` rows, and its `membersAreBookLength`
  series-guard — the check that stops distinct novels being merged — cannot fire
  when that sum is zero. With **97.5% of the review queue** made of these books,
  the guard was inert and the queue was built on blank evidence.

  ⚠️ **Do not measure this with `Book.duration`.** It is a snapshot and is
  populated (16,596 of the 17,149 have `duration > 0`), so coverage looks ~85%
  when the classifier's real coverage was ~2.5%. Measuring the wrong field is how
  this stayed invisible. `total_file_count` on the LIST DTO is a validated proxy
  (100% agreement vs per-book `/files` across 4,774 books); the single-book
  endpoint does not populate it.

- [ ] **Remaining after the first apply:** 1,019 directory-shaped books held for
  review and 93 missing reported only (already `reconcile-scan`'s remit; some may
  be offline mounts rather than deleted audio).

  **The tier-2 probe now exists and has been measured** — `maintenance.probe-directory-books`,
  PR #2162, dry-run against production 2026-08-06 (op `01KZC8A30Z22B81R8NKDHBFZFX`):

  ```
  examined=1019  actioned=0  skipped=1019  errors=0
  actions: link=434  review=585
  ```

  **434 of the 1,019 are confidently linkable.** They were in review only because
  `classifyUnlinked` passed `nil` durations, so `ClassifyDir`'s series guard could
  never fire — the classifier existed and was correct, it was simply being called
  with the one argument that disables it. 585 correctly remain in review.

  What is left here is the **apply**, which needs a human gate. The 93 missing are
  untouched by this and stay with `reconcile-scan`.

- [ ] **Re-run `regroup-shattered-ai` after relink and re-measure the queue.**
  With durations present the series-guard becomes live for the first time across
  most of the queue. Baseline to compare against: 357 pending holds — 217
  ambiguous / 138 multidisc / 1 anthology / 1 version-group. This measurement
  tells us how much of owner item 1 was a DATA problem rather than a classifier
  problem, and should be taken before investing in recommendation tuning.

- [ ] 🐛 **`GetBooksByVersionGroup` silently under-reports group membership, which
  breaks the one-primary-per-group invariant.** Found in production 2026-08-06
  while version-grouping the two copies of *The Successors*.

  **Symptom.** Two books both carry `version_group_id =
  01KNDBPNB289W2Y6TMXS2DDSEG`, but `GET /api/v1/version-groups/<gid>` returns
  only ONE member. `PUT /audiobooks/<id>/set-primary` therefore leaves BOTH books
  flagged `is_primary_version = true`, so the library shows two tiles for one
  book. Re-running set-primary does not help — it demotes only what the lookup
  returns.

  **Root cause** (`internal/database/pebble_store.go`, `GetBooksByVersionGroup`).
  The fast path iterates a `book:versiongroup:<gid>:<id>` index, then falls back
  to a full scan **only when the index yields ZERO results**:

      if len(books) > 0 { sortVersions(books); return books, nil }
      // Fallback: full scan for groups whose index hasn't been backfilled yet

  A *partially* populated index — some members indexed, some not — returns the
  partial set and never falls back. The zero-result guard reads like a correct
  fallback and is exactly wrong for partial data.

  The index is only refreshed by `UpdateBook` when `VersionGroupID` **changes**,
  so a book that acquires a group through a path that does not trip that
  comparison never gets an index entry. Re-POSTing
  `/audiobooks/<id>/versions` does not repair it: the group ID is already
  correct, so nothing changes and no index write occurs.

  **Blast radius is wider than the one endpoint.** `ApplyVersionGroup`
  (`internal/plugins/maintenance/regroup_apply.go`) uses the same function to
  "enumerate every current member and demote strays" — the safety net that keeps
  one primary per group when a `regroup.version-group` hold is approved. With a
  partial index that net silently does nothing, so approving a version-group hold
  can leave two primaries behind.

  **Fix directions** (pick after measuring):
  1. Make the fallback trigger on *suspected incompleteness*, not just zero — e.g.
     always cross-check against the authoritative rows, or verify the returned
     count against a group-size counter.
  2. Write the index entry on every `UpdateBook` where `VersionGroupID` is
     non-empty, not only when it changes (idempotent write).
  3. Add a repair op that rebuilds `book:versiongroup:*` from the Book rows, and
     run it once — existing groups are already affected.
  4. *(added 2026-08-10)* **Read through memdb instead.**
     `internal/database/memdb_schema.go:176` already declares
     `memIdxVersionGroupID` over `Book.VersionGroupID`, memdb stores full
     `*Book` values (`memdb_reads.go:606,622`), and `GetBooksByVersionGroup`
     **never calls `p.mem()`** — it goes straight to Pebble. That index is
     complete by construction and costs O(|group|), so it needs neither a
     completeness heuristic (1) nor a backfill (3). Not adopted unreviewed: it
     necessarily returns MORE members than today, and `metafetch`
     (`service_apply.go:303`, `service_writeback.go:872`) enumerates siblings
     through this call before writing to them. Returning the correct set is the
     point, but it widens what those write paths touch, so it wants an owner's
     eyes rather than an autonomous merge.

  **REPRODUCED 2026-08-10** (deterministically, in a throwaway probe — see
  "why this is not committed" below). Create two books in one group, then
  `pebble.Delete` exactly ONE `book:versiongroup:<gid>:<id>` row, leaving both
  authoritative `book:<id>` rows untouched:

  | Index state | `GetBooksByVersionGroup` |
  |---|---|
  | both members indexed | **2** ✓ |
  | **one** row dropped | **1** ✗ — the second book vanishes |
  | **both** rows dropped | **2** ✓ — the documented fallback engages |

  **Losing more index data produces a more correct answer.** That third row is
  the crux: the `len(books) > 0` guard cannot distinguish "found everything"
  from "found something", so an empty index is safe and a partial one is not.
  Damage in the range 1..n-1 is the only damage that is invisible. The probe
  also confirmed the authoritative row still carried the right
  `VersionGroupID` throughout — the truth was present and simply not consulted.

  **That third row also discriminates between the four fix directions**, and is
  the most decision-relevant thing measured here. Because a fully-empty index
  returns the correct set, direction 3 (rebuild `book:versiongroup:*`) is
  *provably sufficient against the read path exactly as it stands* — it needs no
  code change at all to a function that `metafetch` writes through, only a repair
  run. Directions 1 and 4 change that read path's results; direction 2 fixes only
  future writes. So the real question for the owner is narrower than four
  options: **is a one-off prod repair enough, or does the read path also need to
  stop trusting a non-empty index?** Anything that can drop index rows again
  (or any group that acquires members through a path not tripping the
  `VersionGroupID`-changed comparison) re-opens the hole, which argues for doing
  3 now and 2 or 4 as the durable guard.

  Confirmed unaffected by memdb warmth: the enumeration reads `p.db` directly,
  so the repro does not depend on warmup timing.

  **Why this is not committed as a test yet.** It is red against `main`, and a
  knowingly-red test on `main` is the same class of "green means nothing" defect
  this backlog keeps turning up. It belongs in
  `internal/database/pebble_store_index_consistency_test.go` — which already has
  the `store.(*PebbleStore)` / `ps.db` raw-index pattern and the sibling
  soft-delete cases — landing in the SAME PR as whichever fix direction is
  chosen. Ready to paste:

  ```go
  vg := "VG0000000000000000000PROBE"
  a, _ := store.CreateBook(&Book{Title: "Alpha", FilePath: "/probe/a.mp3", VersionGroupID: strPtr(vg)})
  b, _ := store.CreateBook(&Book{Title: "Beta",  FilePath: "/probe/b.mp3", VersionGroupID: strPtr(vg)})
  ps := store.(*PebbleStore)
  // Simulate partial backfill: drop ONE member's index row.
  _ = ps.db.Delete([]byte(fmt.Sprintf("book:versiongroup:%s:%s", vg, b.ID)), pebble.Sync)
  got, _ := store.GetBooksByVersionGroup(vg)
  if len(got) != 2 {
      t.Errorf("live book %s absent from its own version-group listing: got %d %v", b.ID, len(got), titles(got))
  }
  _ = a
  ```

  Note `dbtest.AssertStoreInvariants` invariant (b) — "a LIVE book must be
  discoverable by its own version-group listing" — is *already* exactly this
  assertion and passes everywhere it is called. It cannot catch this: its
  package doc states it uses only the exported Store surface and so "cannot see
  raw secondary-index rows", meaning no caller has ever constructed a partial
  index for it to inspect. The invariant was never wrong; nothing ever put it in
  front of the failing state.

  **Also needs an invariant test**: after linking N books into a group and
  setting one primary, exactly one member must have `IsPrimaryVersion == true`.

  Related: [[version-group-acoustic-audit]] (which will read group membership and
  would inherit this under-reporting), [[first-aid-library-validate-repair]].

- [x] **PERF: `maintenance.dedupe-book-file-rows` spends ~45 seconds per book, and
      that is enough to blow its own 2-hour timeout.**
      RESOLVED 2026-08-06 in PR #2161 — but almost every premise below was wrong,
      so read this correction before trusting the analysis that follows it.

      **It never hit the 2-hour `Timeout`.** It hit the 5-minute `ProgressTimeout`
      watchdog, at book 19/194. Both liveness bugs were already fixed on main
      (heartbeat `1908396b`, worker pool `df20b8d6`).

      **Total work is ~1.3 h, not 2.4 h.** Real denominator from the scan line:
      `redundant_rows=2901` across 194 books. The "194 × 45 s" extrapolation
      double-counted skew — books process in sorted-ID order and the first 19
      happened to carry 22–47 duplicates against a mean of 15.

      **The unit is per-ROW, not per-book:** ~1.35 s fixed per deleted row, +~7 ms
      per remaining file. Proven twice — the dry run of the identical read loop over
      all 194 books took 2.2 s total, and the per-row delta stays flat
      (1.85/1.94/1.42/1.66/1.54 s) as `total_files` falls 65 → 34, which rules out
      the O(R²) re-read hypothesis below.

      **Actual cause:** `DeleteBookFile` fires `notifyBookFileChange` per row, and
      each runs the full `RecomputeBookAggregates` → `UpdateBook` chain: two
      `pebble.Sync` commits, a full copy-on-write `book_ver` snapshot of the entire
      old Book, and two global go-memdb write transactions. Hypothesis 1 below was
      right about the *structure* and wrong about the cost (`InvalidateLibraryStats`
      is a lazy single NoSync delete, not a recompute); hypothesis 2 was refuted
      outright — `RecomputeBookAggregates` reads only the book's own files.

      **Fix:** `DeleteBookFilesByIDs` — one batch, one Sync, one notify per affected
      book. Salvage deliberately NOT folded into the batch: rescued keeper fields
      must commit *before* donors are deleted, and an atomic batch would remove the
      skip-on-failure escape.

      Measured on the full production run (2026-08-04, op
      `01KZ6W1H46696CZDBHCZF10W6C`): 9 books in ~7 minutes, steady. Extrapolated over
      the 194 affected books that is **~2.4 hours against a `Timeout: 2 * time.Hour`**
      declared in `dedupeBookFileRowsDef()`, so the op cancels itself with roughly
      the last 40 books unprocessed and needs a second invocation to finish.

      Not a correctness problem — each book is committed independently and the op is
      idempotent, so a re-run simply picks up the remainder. But an op that cannot
      complete its own workload in one pass is mis-sized, and it will get worse, not
      better, as the library grows.

      **~45s to delete ~15 rows from one book is the anomaly worth explaining.** The
      per-book work is small: one `GetBookFiles` (Pebble-direct), a handful of
      `DeleteBookFile` calls, one `RecomputeBookAggregates`. Suspects, cheapest to
      check first:

      - `DeleteBookFile` → `notifyBookFileChange` may trigger a library-stats
        invalidation and full recompute **per row deleted**, not per book.
      - `RecomputeBookAggregates` re-reads the book's files; if it re-reads the whole
        library-level aggregate instead, that is the 5.6s full-scan class of bug
        already seen in `CountPrimaryBooks` (see
        [[project_countprimarybooks_cpu_fix]] — same shape, different caller).
      - The book loop is sequential. Per `CLAUDE.md`'s concurrency rule this is
        exactly a whole-library-scale loop doing meaningful per-item DB work, so it
        should have been a bounded `errgroup` pool from the start. Partition by book
        ID — books are disjoint, so parallel workers cannot touch the same row.

      Fixing the per-book cost is the real answer; raising the timeout only hides it.

- [ ] **Corrected book aggregates are invisible until memdb refreshes.**
      Observed on the first `maintenance.dedupe-book-file-rows` canary
      (2026-08-03): 338 redundant rows were deleted from 10 books and every
      duration was **unchanged** immediately afterwards. `total_file_count` still
      read 50 for a book whose files endpoint already returned 26. A service
      restart surfaced the corrected values — e.g. "Defending the Lost"
      158.00h → **12.15h** — so the data in Pebble was right the whole time and
      only the memdb-backed read was stale.

      Where to look: `DeleteBookFile`
      (`internal/database/pebble_store_bookfiles.go:730`) does the right things in
      the right order — Pebble delete, `DeleteBookFileFromMemDB`, then
      `notifyBookFileChange`. The suspect is
      `RecomputeBookAggregates`
      (`internal/database/pebble_store_book_aggregates.go:131-134`), which
      **early-returns without calling `UpdateBook`** when the recomputed values
      equal the stored ones. `UpdateBook` is what triggers `UpsertBookToMemDB`,
      and that is the call which reloads `book_files` from Pebble
      (`internal/database/memdb_sync.go:53-55`). Skip the write and memdb keeps
      the stale file set.

      Why it matters beyond this op: any caller that deletes book_files and
      relies on the aggregate being visible has the same blind spot, and the
      library list computes duration from the memdb file map, not the stored
      field.

      Until it is fixed, `dedupe-book-file-rows` says so in its completion
      message rather than letting an operator conclude the run did nothing.

      **Traced 2026-08-10 — the stated suspect does not fit the symptom. Read
      this before spending time on `RecomputeBookAggregates`.** Four things were
      verified by reading the code at `65e63135`; **none of this is a
      reproduction**, and the bug is NOT explained yet.

      1. **The op does not call `DeleteBookFile`.** `dedupe-book-file-rows` uses
         the batched `store.DeleteBookFilesByIDs`
         (`internal/plugins/maintenance/dedupe_book_file_rows.go:368`). The entry
         above says "where to look: `DeleteBookFile`" — that is a different code
         path from the one the canary actually ran.
      2. **The batched path already does the memdb delete.**
         `DeleteBookFilesByIDs` (`pebble_store_bookfiles.go:990`) calls
         `s.DeleteBookFilesFromMemDB(resolvedIDs)` at :1073 and then
         `notifyBookFileChange(bookID)` per affected book at :1078. So the
         book_file rows ARE removed from memdb on the delete path, independently
         of whether any later `UpdateBook` runs.
      3. **`total_file_count` is not a stored field**, so a skipped `UpdateBook`
         cannot stale it. It is derived at read time —
         `enriched[i].TotalFileCount = len(files)`
         (`internal/server/audiobooks_helpers.go:95`, and again at
         `internal/server/handlers/audiobooks/handler.go:387`) — from
         `FetchBookFilesForBooks` → `GetBookFilesForIDsCore`, whose memdb
         implementation (`memdb_reads.go:917`) reads `memTableBookFiles` by
         `memIdxBookID`.
      4. Consistent with that, `RecomputeBookAggregates` never touches
         `TotalFileCount` at all — its early return at
         `pebble_store_book_aggregates.go:131-134` compares only `Duration` and
         `FileSize`.

      Taken together: if the delete path removes the rows from memdb (2) and the
      count is derived from memdb at read time (3), then the early return in
      `RecomputeBookAggregates` cannot be what left `total_file_count` at 50.
      Something else kept those rows visible.

      **Where to look next**, in rough order of suspicion — all unverified:
      `DeleteBookFilesFromMemDB` routes through `memSync`, which during warmup
      either buffers or, on buffer overflow, abandons memdb entirely
      (`memdb_pending.go`). The canary ran against a production-sized library
      where warmup takes ~2 minutes, so a delete landing in that window is the
      first thing to rule in or out — including whether a warmup snapshot taken
      before the delete could be published after it. Note the observed fix was a
      **service restart**, which is consistent with a memdb-population problem
      and not with a missed `UpdateBook`.

      **To reproduce**, the shape that matters is a delete concurrent with
      warmup, not a delete on a quiet store — a quiet-store test will likely pass
      and prove nothing, the same way `dbtest` invariant (b) passes everywhere
      while the version-group under-report is real.

- [x] ~~**Restore the duration on `The Trapped Mind Project`**~~ **RETRACTED
      2026-08-04 — nothing to restore.** The original claim here was that the
      canary kept a fingerprinted row whose `Duration` was 0 and deleted the 129
      twins holding the real value. Probing the audio disproves it: the book's
      entire content is a 13.5-second, 91,958-byte MP3, and the surviving row
      (`file_size=91958`, `duration=13`) matches it exactly. 0.00h is simply what
      13 seconds looks like. The op behaved correctly; the error was reading a
      rounded display value as evidence of loss without checking the file.

- [x] **Flaky: `TestApplyPIDRepairSameFile`** (`internal/itunes`) failed
      `Minimal CI / Go Tests (short, race)` on PR #2126 — a PR that touches only
      `internal/server/server_maintenance_deps.go` and cannot affect the iTunes
      package.
      Verified as a flake, not a regression: **10 consecutive passes on the PR
      branch and 10 on `main`**, both with `-race` exactly as CI runs it.
      This is the **second** flake found on 2026-08-03; see
      [[2026-08-03-flaky-backfill-syncids-race-sanity]]. Two independent flaky
      tests blocking unrelated PRs in one evening suggests a shared cause worth
      one investigation rather than two: both are concurrency tests, both pass
      locally, both fail only under CI load. Suspect a shared fixture, a fixed
      sleep, or an unsynchronised goroutine handoff that only loses the race on
      a slower/contended runner.
      Do NOT keep re-running them — that is how a flake becomes permanent and
      how a real regression eventually gets waved through. Related:
      [[project_ci_gotests_intermittent_stalls]].

      **CLOSED 2026-08-10. The "shared cause" guess was right.** This test builds
      its store with `newRepairTestStore`, which was one of the three helpers
      that skipped `WaitForWarmup`; its sibling flake used `newSyncPebbleStore`,
      another of the three. That helper now carries the reason in a comment:
      *"Without this the repair tests read back a book_file that is in Pebble but
      missing from memdb."* Both helpers were fixed in #2131, and the underlying
      write-loss was structurally eliminated in `587b2fd0` (2026-08-06) — writes
      arriving during warmup are buffered and replayed before memdb publishes
      (`memdb_pending.go`), so the window no longer drops anything.

      **Evidence for closing** (gathered 2026-08-10):

      - `make test-short` runs `go test ./... -short -race`, so this test executes
        on every `Coverage Floor` and `Go Tests (short, race)` run. Neither this
        test nor its sibling has a `testing.Short()` guard — checked, so the runs
        below genuinely exercised them rather than skipping them.
      - **50 completed `Continuous Integration` runs since `587b2fd0`, 0 failures**
        (10 further runs cancelled by `cancel-in-progress`; those are evidence of
        nothing either way and are not counted).
      - The fix is covered by a 6-test acceptance suite,
        `internal/database/memdb_warmup_writeloss_test.go`, which pins the
        invariant in both directions (dropped create, phantom after dropped
        delete, concurrent writers, buffer-overflow degrades loudly, Reset not
        undone) and guards against vacuous passes by skipping when the warmup
        window was too narrow to exercise.

      **What is NOT claimed:** this particular flake was never reproduced red, and
      its mechanism is inferred from the shared helper rather than observed. The
      case rests on mechanism + fix + regression suite + streak, not on a
      reproduction. If it recurs, reopen — do not re-run it.

- [x] **Flaky: `TestBackfillSyncIDsJob_ConcurrentRaceSanity`** (`internal/maintenance/jobs`)
      failed the Coverage Floor gate on PR #2123, a PR that touches only
      `internal/server/middleware/absauth.go` and cannot affect this package.
      Verified as a flake, not a regression: **10 consecutive passes on the PR
      branch and 10 on `main`** locally. It fails only under CI load, which fits
      a timing-sensitive concurrency assertion.
      Do not just keep re-running it — find the timing assumption (likely a
      fixed sleep or an unsynchronised goroutine handoff) and make the test wait
      on a condition instead of a duration. Related: [[project_ci_gotests_intermittent_stalls]].

      **Update 2026-08-04 — a sibling test failed the same way, and there is now a
      concrete mechanism to test.** `TestBackfillSyncIDsJob_FreshLibrary` (same
      file) failed CI on PR #2129, which touches only `internal/plugins/maintenance`
      and docs — a different package entirely. It seeds 20 books and then asserts
      each has a syncID; one did not:

      ```
      backfill_sync_ids_test.go:102: Should be true
        Messages: book 01KZ6QV6AZPW2AE93P7M0TRVFN has no syncID
      ```

      25/25 passes locally with `-race`, so it is timing-dependent like its sibling.

      **Mechanism — CONFIRMED by reading the warmup path; fix shipped in #2131.**
      The job enumerates with `store.ListBookIDs()`, and its comment
      (`backfill_sync_ids.go:61-64`) correctly rules out the `GetAllBooksFrom`
      pagination cap — but `ListBookIDs` still takes the memdb fast path
      (`pebble_store.go:594`). `NewPebbleStore` starts warmup in a goroutine and
      publishes only at the very end (`memPtr.Store(memStore)`, `pebble_store.go:291`).
      Until it does, `mem()` is nil — which makes *reads* safe, since they fall back
      to Pebble, but silently no-ops every *write's* memdb write-through. A test
      seeding books in that window leaves them in Pebble while the published memdb
      never saw them, so those books are never enumerated and never get a syncID.

      `PebbleStore.WaitForWarmup` documents this as mandatory for tests
      (`pebble_store.go:147-152`) and three helpers were skipping it —
      `newSyncPebbleStore`, `newPebbleTestStore`, `newRepairTestStore`. #2131 adds
      the call to all three.

      **Keep this item open until a green CI streak earns closing it.** The fix rests
      on the documented invariant plus a matching failure signature, *not* on a
      reproduced red test: on an empty temp DB the window is sub-millisecond, and 40
      iterations under 20× CPU contention would not force it. Calling `WaitForWarmup`
      is correct regardless of whether it proves to be the whole story.

      Production is not affected — warmup is a one-time startup affair there and
      reads fall back to Pebble until it publishes.

      Note this is a *different* mechanism from
      `todo.d/2026-08-01-assignorphanvgs-offset-pagination.md`, which is about offset
      arithmetic over a swapping snapshot. Same underlying async-warmup design, two
      distinct failure modes; a fix should consider both.

      **CLOSED 2026-08-10 — the green streak this item was waiting on has been
      earned, and the cause is gone rather than merely quiet.**

      `WaitForWarmup` in the three helpers (#2131) was the first half. The second
      half landed in `587b2fd0` (2026-08-06): writes arriving during warmup are
      now buffered and replayed before memdb publishes (`memdb_pending.go`), so
      the lost-update window is structurally closed rather than avoided. The
      `WaitForWarmup` doc comment records the demotion — it is *"no longer
      required for CORRECTNESS"*, only for making tests deterministic about which
      read path they exercise.

      **Evidence** (gathered 2026-08-10):

      - **50 completed `Continuous Integration` runs since `587b2fd0`, 0 failures.**
        10 more were cancelled by `cancel-in-progress`; those prove nothing and are
        excluded.
      - Both this test and `TestBackfillSyncIDsJob_FreshLibrary` run under
        `go test ./... -short -race` with no `testing.Short()` guard, so every one
        of those runs actually executed them.
      - `internal/database/memdb_warmup_writeloss_test.go` pins the invariant in
        six shapes, including the mirror cases the original write-up did not
        cover: phantom rows after a dropped delete, buffer overflow refusing to
        publish and logging at ERROR, and Reset not being undone by an in-flight
        warmup. It also guards against vacuous passes — each test skips rather
        than passes when the warmup window closed before any write landed.

      The open sibling item above (`TestApplyPIDRepairSameFile`) is closed on the
      same evidence; its helper was one of the same three.

## DATA: BookFile rows are duplicated 2× AND their durations are milliseconds, not seconds

Found 2026-08-02 while chasing why the app showed **Hyperion at "0%, 48h 31m
remaining"** on its Continue Listening shelf. Two independent defects compound, and
either alone corrupts every duration-derived number on the ABS surface.

### Measured, on `01KNDBK4MM369VJXA1QKQ6YR8S` ("Hyperion")

```
total BookFile rows: 298
distinct tracks:     149   | tracks with >1 row: 148
duplication factor:  2.00x

duration min=521  max=1803755
rows >50000 (impossible as SECONDS for one track — that is >13h): 297 of 298
sum as-is       = 41276.8 h      <- what the code computes today
sum if ms       =    41.3 h
halved + ms     =    20.6 h      <- Hyperion's actual length ✓
```

### Defect 1 — every track has two BookFile rows

One from the organized tree, one from the iTunes tree:

```
464039s track=1  data/books/audiobook-organizer/Dan Simmons/Hyperion/Hyperion
464065s track=1  /iTunes Media/Audiobooks/Dan Simmons/01 Hyperion 001-149.mp3
```

The pair's durations differ by ~26 ms, so they are the same audio measured twice —
not two genuine files.

### Defect 2 — durations are stored in milliseconds

`BookFile.Duration` is **seconds** by contract: the committed oracle fixture uses
`Duration: 9975` for a 9975-second book, and `seedOracleLibrary` uses `1662` for
~27-minute tracks. But 297 of these 298 rows are 6–7 digit values that only make
sense as ms. Track 144 is the smoking gun — it carries **both** forms:

```
521534s   track=144   (milliseconds)
   521s   track=144   (seconds — same value, correct unit)
```

### Why it matters

`durationFor` (`abs/userdata.go`) and the mapper both sum `BookFile.Duration` as
seconds, and §5b makes that sum the ONE authoritative duration for `media.duration`,
the play session, `startOffset`, synthesized chapters, and the progress fraction. With
a ~2000× inflated denominator, `currentTime / duration` rounds to zero — which is
exactly the reported **"0%"** — and the remaining-time readout is nonsense.

### Scope — MEASURED 2026-08-03

Both defects were measured library-wide, and the two turned out to have very
different shapes than this single-book sample suggested.

**Defect 2 (units) is small and was mostly a _display_ bug, not stored corruption.**
Only ~2% of rows actually hold milliseconds. The library-wide symptom — 25,938 books
showing absurd totals — came from `service_filtering.go`, which divided **every**
duration by 1000 unconditionally while summing, and truncated each row to an integer
before adding. Correct second-valued rows were the ones being destroyed, on read.
Fixed in #2125 by routing the sum through `database.NormalizeDurationSec`, which
classifies **per row** from the bitrate the file size implies — exactly the
idempotent, per-row test this entry demanded. Zero of 843 multi-file books still show
the symptom.

**Defect 1 (duplication) is real, larger, and is NOT a uniform 2×.** The "2.00x"
figure was an artifact of the one book sampled. The true shape is a single file
duplicated up to **130 times**: `The Trapped Mind Project` had 130 rows for one file,
and one m4b's runtime was being counted 26 times (`568,802s = 26 × 21,877` exactly).
Addressed by a new dry-run-by-default op, `maintenance.dedupe-book-file-rows`
(#2128), which finds candidates on the cheap memdb path and then re-reads each group
Pebble-direct before deciding, because the memdb projection strips
`AcoustIDFingerprint`.

### Do not fix blind

- Deduping BookFile rows is a **destructive prod mutation** — it needs a dry-run and
  an explicit decision, and it interacts with the dedup subsystem and with
  `books/itunes/**` being HANDS-OFF.
- A units migration must be **idempotent and detectable**: track 144 proves both units
  already coexist, so a blanket `/1000` would corrupt the rows that are already
  correct. Any repair has to classify per row, not per book.
- Fixing units without deduping (or vice versa) leaves the duration wrong by 2×,
  which is still enough to misplace every chapter boundary.

### Status 2026-08-04

- [x] **Defect 2 — units.** Fixed on the read path (#2125) plus 798 stored durations
      corrected. `NormalizeDurationSec` classifies per row, so it is idempotent and
      cannot corrupt already-correct rows.
- [x] **Dry-run op for Defect 1.** `maintenance.dedupe-book-file-rows` shipped
      (#2128), dry-run by default, mirroring `maintenance.title-repair`'s `Apply=false`.
- [x] **Canary applied — 10 books, 338 rows deleted.** Every corrected total verified
      after restart (`Defending the Lost` 158.00h → 12.15h, `San Kuo` 294.05h → 19.66h)
      with `fingerprinted_file_count` unchanged on all 10.
- [x] **~~Canary defect — keeper lost data.~~ RETRACTED 2026-08-04 — there was no
      data loss.** The claim was that `The Trapped Mind Project` dropped to 0.00h
      because ranking kept a fingerprinted row whose `Duration` was 0. Checking the
      actual audio disproves it: that book's entire content is a **13.5-second,
      91,958-byte MP3**, and both the surviving row and the file on disk agree.

      ```
      iTunes copy       91958 bytes   duration=13.485s   bit_rate=54554
      surviving DB row  file_size=91958                  duration=13
      ```

      130 rows × 13s ≈ 1,690s ≈ 0.47h inflated → 13s after dedupe. **0.00h is the
      correct answer for a 13-second file**, and the op behaved exactly as designed.
      The error was reading "0.00h" as lost data without checking the audio.
- [x] **Keeper field-merge shipped anyway (#2129).** It is still right on its own
      merits — ranking selects a whole *row*, so a keeper genuinely can lack a field a
      twin holds, and merging is strictly additive. But it is **hardening against a
      latent hazard, not a repair of an observed loss**; no such loss has been
      demonstrated.
- [x] **DONE 2026-08-04 — duplicate `book_file` rows are gone library-wide.** Final
      verification dry run, after a restart so memdb was warm:

      ```
      314,153 rows scanned, 0 books affected, 0 redundant rows, would delete 0,
      failed 0
      ```

      Total across all runs: **204 books, 3,239 redundant rows deleted, 0 failures**,
      and "salvaged fields on 0 keepers" every time — no keeper anywhere was missing a
      field one of its twins held, which is the third independent confirmation that the
      data-loss finding was correctly retracted.

      The run needed three attempts for reasons worth remembering:
      1. cancelled at book 19/194 by the stuck-op watchdog (progress reported once per
         book, one book took >5m) → fixed in #2133;
      2. hit the op's own 2-hour `Timeout` at book 78/176 running sequentially at
         ~1.7 min/book;
      3. finished **95 books in 9.5 minutes** once the book loop was parallelised
         (#2135) — the same work the sequential pass took two hours to half-finish.

- [x] **⚠️ Duplicate rows were only half the inflation.** Deduping fixed 8 of the 10
      sampled books (`Shades of Glory` 144.71h → 12.06h, `The Undying Illusionist`
      261.61h → 17.26h, `Darkness Rises` 205.41h → 14.78h). **Two did not**, because
      their stored durations are milliseconds, not seconds:

      ```
      dur=241110   size=1600709   → 0.1 kbps as seconds |  53.1 kbps as ms
      dur=1307193  size=7997209   → 0.0 kbps as seconds |  48.9 kbps as ms
      ```

      Every row lands at 48–53 kbps read as ms — a spoken-word MP3 — and
      9,906h ÷ 1000 ≈ 9.9h, a real audiobook. #2125 fixed the **display** path via
      `NormalizeDurationSec`; the **stored** rows were never rewritten. Measured
      prevalence from a 2,733-row sample: **1.9% (53 rows)**, so roughly 6,000
      library-wide.

      **DONE 2026-08-04 (#2137).** Fixed in two parts:

      1. **`UpdateBookFile` now normalises to seconds.** It was the *last* write path
         that did not — `CreateBookFile`, `UpsertBookFile` and `BatchUpsertBookFiles`
         all did — so an update could reintroduce the very corruption those three
         exist to prevent. This also closes the tracked "unguarded `UpdateBookFile`"
         defect. The unit invariant now holds at the store, not per caller.
      2. **`maintenance.purge-millisecond-durations`** backfilled the historical rows.

      ```
      apply : 314,153 rows scanned, 214 books affected, 1,384 ms rows,
              converted 1,384, recomputed 214 books,
              skipped 9,352 (already seconds), failed 0
      verify: 314,153 rows scanned, 0 millisecond durations found — nothing to do
      ```

      The two books that survived deduping are now right:
      `01KNDB9V04D7MBTFVDKYWX286E` 19,294.11h → 9,906.11h → **9.90h**, and
      `01KNDB9ZHJSMBY7D98Y82PQTK0` 15,556.96h → 8,049.06h → **8.05h**. All ten sampled
      books now read 8–17h.

      ⚠️ **Correct the earlier estimate:** the "1.9% ≈ 6,000 rows" figure extrapolated
      from a 2,733-row sample was **wrong by ~4×**. The real count is **1,384 rows
      (0.44%)** — that sample was a targeted dump, not a random one, so its rate did
      not generalise. Prefer a full scan over an extrapolated sample for anything
      load-bearing.

      The 9,352 skipped rows are the reassuring part: they sit *inside* the same 214
      affected books and were correctly left alone, so the predicate discriminates per
      row, not per book.
- [ ] **`The Trapped Mind Project` is a 13-second stub, not an audiobook**
      (`01KNDB97CWFSMSEY68P82VDRBF`). Nothing to restore — but two things about it are
      still wrong and worth chasing as a class:
      its book-level `file_size` reads **532,805,172** (532 MB) for a 91 KB file, and
      the API reports `file_exists: true` for a `file_path` that is absent from disk.
      Both are book-level fields disagreeing with the underlying file. See the
      duration/filesize aggregation item — same family of defect.
- [ ] **5 books are multi-copy, not row-duplicated** — distinct paths for the same
      book (`Wind and Truth` 426 files, `Ajax's Ascension` 272). Deduping rows is the
      wrong tool; these need regrouping and should surface in the review queue.
- [ ] **`Call to Arms` (9,957h)** — 96 *distinct* files, unchanged by the dedupe run.
      A third shape, not yet diagnosed.
- [ ] **Corrected aggregates are invisible until memdb refreshes** — see the
      2026-08-04 entry on `RecomputeBookAggregates`. Not a duration bug, but it makes
      every duration fix look like a no-op until a restart.

## BUG: `AssignOrphanVGs` can silently skip books — offset pagination over an async memdb snapshot

**Severity:** correctness bug in a full-library maintenance op. Surfaces as a CI
flake, but the same defect skips real books in production.

`internal/reconcile/reconcile.go:1292` enumerates with offset arithmetic:

```go
for offset := 0; ; offset += pageSize {
    books, err := store.GetAllBooksCore(pageSize, offset)
```

and `GetAllBooksCore` (`internal/database/pebble_store.go:439`) reads **memdb**
when `UseMemDB` is set:

```go
if p.UseMemDB && p.mem() != nil {
    return p.mem().GetAllBooksCore(limit, offset, nil)
}
```

The memdb snapshot is republished **asynchronously** (`memdb warmup starting
(async)` → `memdb warmup published`). Offset pagination is only sound over a
stable collection: if the snapshot is swapped between page N and page N+1, the
offset no longer refers to the same position and rows are skipped or repeated.

**Observed**, CI run 30702594886, `TestAssignOrphanVGs_RealStoreConcurrent`:

```
reconcile_orphanvg_test.go:213: Assigned = 39, want 40
reconcile_orphanvg_test.go:226: book 01KYYSX09WES7849SHVVBN8H4N VersionGroupID not set
... assign-orphan-vgs summary total_checked=39 assigned=39 skipped=0 errors=0
```

`total_checked=39` for 40 books is the tell: the book was never **enumerated**,
so this is not a write race or a lost update — the op simply never saw it. It
therefore reports success while having skipped work, which is the dangerous
shape: no error, no retry, no signal.

Does not reproduce locally (5/5 passes) — it needs the scheduling pressure of a
loaded CI runner to land the snapshot swap mid-iteration.

**Fix:** enumerate with `ListBookIDs` + `registry.RunItems` rather than
offset-paging a mutable snapshot. This is the pattern the repo already mandates
for full-library jobs, for exactly this reason — see
[[feedback_getallbooksfrom_memdb_cap]] ("cursor pagination silently capped at
2×limit on prod memdb path", fixed in #1647) and the concurrency section of
CLAUDE.md. An ID list is a stable set; paging positions in a snapshot that can
be replaced underneath you is not.

**Also worth auditing:** every other `GetAllBooksCore(pageSize, offset)` caller
that walks the whole library has the same exposure. Grep for the offset-loop
shape before assuming this is the only one.

## ⚠️ DEPLOY GATE: /metrics now requires auth — configure Prometheus BEFORE the next deploy

**PR #2092 is merged but NOT deployed.** Deploying it without doing the below breaks
metrics collection silently.

There is a **live Prometheus + Grafana on the origin host**, scraping
`http://127.0.0.1:8484/metrics` every 15s with **1 year / 500GB retention**
(`--storage.tsdb.path=/mnt/cache/metrics/metrics2/`). It was found only by checking
`ps` — nothing in this repo references it, and `deploy/prometheus/` is documented as
"examples/snippets… nothing in this repo scrapes it", which is now false.

Since #2092 gates `/metrics` behind authentication, the next `make deploy` makes every
scrape 401 and leaves a gap in the series. Prometheus does not alert on its own scrape
failing unless a rule exists for it.

### Do this first (needs interactive sudo — that is why it was not done unattended)

1. Mint an API key in the UI: **Settings → API keys**. It looks like `abk_…`.
2. Install it readable only by Prometheus:
   ```bash
   sudo install -m 0600 -o prometheus -g prometheus /dev/null /etc/prometheus/abo.token
   printf '%s' 'abk_…' | sudo tee /etc/prometheus/abo.token >/dev/null
   ```
3. Add to the audiobook-organizer job in `/etc/prometheus/prometheus.yml`:
   ```yaml
       authorization:
         type: Bearer
         credentials_file: /etc/prometheus/abo.token
   ```
   Use the `_file` form: Prometheus re-reads it each scrape, so rotating the key needs
   no reload and the secret never lands in `prometheus.yml`.
4. `sudo systemctl reload prometheus`
5. Confirm the target is UP in Prometheus → Status → Targets, THEN deploy.

### Verify after deploying

```bash
curl -ksS -o /dev/null -w '%{http_code}\n' https://<server>:8484/metrics            # want 401
curl -ksS -o /dev/null -w '%{http_code}\n' -H 'Authorization: Bearer abk_…' \
     https://<server>:8484/metrics                                                  # want 200
```

### Also update

`deploy/prometheus/README.md` claims nothing in this repo scrapes `/metrics`. A real
scraper exists on the production host; the sentence is misleading and should say so.

## LATENT: web OAuth callback silently discards a custom-scheme `return`, falling back to `/`

**Severity:** latent. No shipped client currently exercises this path — see
"Why this is not urgent" below. Filed so it is not rediscovered from scratch.

`internal/server/handlers/oauth_login.go:145` picks the post-login destination:

```go
dest := "/"
if payload.Return != "" { dest = payload.Return }
http.Redirect(c.Writer, c.Request, dest, http.StatusFound)
```

`payload.Return` was set at `Start` via `sanitizeReturn(c.Query("return"))`, and
`sanitizeReturn` requires a single leading slash:

```go
if ret == "" || !strings.HasPrefix(ret, "/") { return "" }
```

So a native-app deep link such as `audiobooth://oauth` becomes `""`, `dest`
falls back to `"/"`, and the caller is sent to the web SPA root. **No error is
raised and nothing is logged** — the redirect target is simply replaced. A client
expecting to be handed back to its own URL scheme instead lands on the web UI,
which surfaces as an opaque "it logged me into the website" rather than as a
failure.

### Why this is not urgent

Production logs over 7 days show **zero** requests to `/auth/oauth/*` — the web
provider flow is reached only by the SPA's login buttons, which legitimately want
same-site paths. Audiobookshelf clients use `/auth/openid` +
`/auth/openid/callback` (`internal/server/handlers/abs/openid.go`) instead, and
that path already handles custom schemes correctly via `oidcRedirectAllowed` and
`oidcRedirect`.

This was misdiagnosed on 2026-08-01 as the cause of the AudioBooth login failure.
It was not — the real cause was Cloudflare Access intercepting
`/auth/openid/callback` before it reached the origin, fixed with a scoped Access
bypass on that single path. Recording the distinction here so the next
investigation does not repeat it: **a redirect-to-web-root symptom has two
plausible causes, and only traffic logs distinguish them.**

### Fix, if a client ever needs it

Do **not** loosen `sanitizeReturn` — it is the open-redirect guard and the reason
`d87cbf37` (account takeover via unregistered `redirect_uri`) cannot recur here.

Instead mirror the ABS path: on an allowlisted deep link, mint a single-use
PKCE-bound code via the `abs` package's existing code store and 302 to
`audiobooth://oauth?code=…&state=…`, letting the client redeem it at the existing
`/auth/openid/callback`. Two constraints that a naive implementation gets wrong:

1. **Gate on `redirect_uri` AND `code_challenge` together.**
   `/auth/oauth/:provider/start` is the unauthenticated web login endpoint; if a
   bare `redirect_uri` could trigger a 400, anyone could break web login by
   appending a query param to a link.
2. **There are two distinct PKCE exchanges** — server↔IdP (verifier already in
   `StatePayload.Verifier`) and app↔server (the app's own challenge). Conflating
   them either breaks the upstream token exchange or issues codes with no
   app-side proof of possession.

Unverified assumption to settle before building: whether
`ASWebAuthenticationSession` returns the `SameSite=Lax` `oauth_state` cookie on
the hop back from the IdP. If it does not, `Callback` dies at
`oauth_state_missing` regardless. Only a real-device test can answer it.

## GAP: only ~19.5% of books have cover art, so most ABS clients show placeholders

**Severity:** cosmetic but pervasive. Not a code defect — `GET /api/items/:id/cover`
behaves as designed.

Observed 2026-08-02: AudioBooth's library grid rendered, and every cover request in
the sample 404'd:

```
GET /api/items/cb6e44f7-…/cover  → 404
GET /api/items/7840afbd-…/cover  → 404      (5 of 5 in the window)
```

On prod, `/mnt/bigdata/books/audiobook-organizer/covers/` holds **7,885** files
against a library of roughly **40,400** books — about **19.5%** coverage.

### Why this is not a bug

`Handler.ItemCover` resolves via `metadata.CoverPathForBook`, which globs
`<RootDir>/covers/<bookID>.{jpg,jpeg,png,webp,gif}` and returns `""` when nothing
matches. The handler then answers 404, and its own comment records that as intended:
*"A 404 here is correct and harmless: both clients fall back to a placeholder."*

**Not yet confirmed:** whether those 5 specific items lack cover files, or whether the
sync-UUID → Book-ULID resolution is picking the wrong ID. With 19.5% coverage, 5
consecutive misses has a ~34% chance of being pure luck, so this is *likely* a data
gap but has NOT been proven. Verify by resolving one of those sync IDs to its Book
ULID and checking for `covers/<ULID>.*` before investing in a backfill — a mapping bug
and an empty directory look identical from the client.

### If it is the data gap

A cover backfill over ~32,500 books is a full-library maintenance op and must be
written to the repo's concurrency rules from the start (CLAUDE.md): bounded worker
pool, `registry.RunItems`, never a plain `for range books`. Network-bound if it
fetches from a metadata provider, so size concurrency to that provider's rate limits
rather than `runtime.NumCPU()`.

Look for an existing parallel sibling before writing a new loop — the acoustid
backfill (`internal/plugins/acoustid/backfill.go`) is the established pattern.

## UNSPECIFIED: play counts and listening history have no designed ABS surface

Raised while building the Phase 6 write half (2026-08-02). The owner's goal statement
names "play counts" as one of "all the backend features the application expects."
**The design spec defines no endpoint for them**, so nothing was invented — this
records the gap rather than guessing at a shape.

### What exists today

- `UserBookState.TotalListenedSeconds` accumulates per (user, book) and is written by
  the ABS sync path.
- `IncrementBookPlayStats` / `IncrementUserListenStats` /
  `GetBookStats` / `GetUserStats` exist in `pebble_store_playback.go` but are **not**
  wired to the ABS surface.
- `Book.ITunesPlayCount` is an imported scalar from iTunes, unrelated to listening
  recorded by this server.

### What real ABS exposes (and why we currently 404 it deliberately)

`GET /api/me/listening-stats` and `GET /api/me/item/listening-sessions/:id` are the
surfaces a client asks for. Both are **intentionally 404** today per spec §1.8.6: they
carry ~12 non-optional fields, callers wrap them in `try?`, and a half-correct body is
worse than none. AudioBooth polled `/api/me/listening-stats` 7 times in the 2026-08-01
window and tolerated every 404 without user-visible breakage.

### Decision needed before building

1. Is a play *count* even the right primitive here, or is `TotalListenedSeconds`
   (already recorded) what the owner actually wants surfaced?
2. If the ABS-shaped endpoints are to be implemented, all ~12 fields must be produced —
   a partial body is a regression from the current honest 404.
3. `POST /api/session/local[-all]` (offline replay) is the other half of an honest
   listening history and is itself unbuilt; `progress.MergeOfflineReplay` exists and is
   tested but has no HTTP caller.

**Do not implement piecemeal.** Half a stats surface reads to a client as a broken
server rather than an absent feature.

## MISSING: ABS progress-mutation endpoints — "reset progress" and "remove from continue listening" do nothing

**Severity:** user-visible feature gap, not a regression. Reported from AudioBooth
on 2026-08-02 immediately after the client reached a fully working state (SSO login,
library browse, and playback all confirmed the same night).

Observed in production:

```
01:13:17  GET /api/me/progress/44669fab-6544-4414-ae2d-fa8eba7c52f3  → 404
```

`remove-from-continue-listening` was reported as also not working. Its call does not
appear in the log window that was checked, so it is recorded here from the spec
rather than from an observation — confirm the exact path and method against
AudioBooth before implementing.

### This is planned work, not a defect

`docs/specs/2026-07-29-abs-sync-api-design.md:839` puts all of it in **Phase 6**:

> Progress + bookmarks: adapt playback store, `/api/me`, `PATCH /api/me/progress/:id`,
> `/api/me/progress`, bookmarks CRUD (new), remove-from-continue-listening; §5 merge
> policy

Phase 6's read half shipped — `/api/me` and `POST /api/authorize` both serve the
complete `mediaProgress` list from `UserDataProvider`. The **write** half was never
built, so every client-side progress mutation 404s.

### Endpoints to add

- `PATCH /api/me/progress/:id` — update progress for one item
- `GET`/`DELETE` on `/api/me/progress/:id` — AudioBooth issued a `GET`; check whether
  reset is a `DELETE` and the `GET` is only a pre-read
- `/api/me/progress` — batch
- `…/remove-from-continue-listening`
- bookmarks CRUD

### Constraints that already apply

- **`absReservedPaths`.** `/api/me/` is already a reserved *prefix*, so these inherit
  the exclusion and will not 301 into `/api/v1`. No new reservation needed — unlike
  `/api/authorize`, which needed an exact-path entry (see PR #2100).
- **§1.8.1 still governs the read side.** Any handler that returns a user payload must
  return the COMPLETE `mediaProgress` list or a 5xx. A mutation endpoint that responds
  with a truncated user object destroys local progress exactly as `/api/me` would.
- **`…/remove-from-continue-listening` needs a non-empty body** — `{}` suffices
  (spec:318). An empty `200` is fatal to these decoders (§1.8.6).
- **§5 merge policy** applies to writes: device↔device sync is explicitly out of scope
  for the phase, but the merge rules for a single device's updates are specified.

### Not a bug, do not "fix"

`GET /api/me/listening-stats` → 404 and `GET /api/me/item/listening-sessions/:id` →
404 are **correct**. The spec prefers 404 for the stats endpoints (~12 non-optional
fields; callers use `try?`), and a half-correct body is worse than none.

## MISSING: no book in the library has stored chapters — extraction only ever runs during a scan, and no scan has run

Reported by the owner 2026-08-02: "don't we extract chapters from the files that have
them and then use the tracks for others? I'm not seeing the chapters in the app."

The extraction code **is** implemented and correct. It has simply never run against the
existing library.

### Evidence chain (all four links verified 2026-08-02)

1. **`SaveChaptersForBook` has exactly one caller:**
   `scanner.PersistChaptersForBook` (`internal/scanner/process_file.go:259`).
2. **That function is only invoked from a scan** — `internal/scanner/scanner.go:851`
   and `:1035`, both inside the per-book scan worker. Nothing else calls it.
3. **`library.scan` has not run in 14 days.** All 31 occurrences of `id=library.scan`
   in the journal are the op-*registration* line emitted at startup; there are zero
   run records. **There is also no chapter backfill op** — no registered op id
   contains "chapter" except the unrelated `dedup.quarantine-chapter-artifacts`.
   (Phase 4 of the ABS spec called for a `registry.RunItems` backfill; it was never
   built.)
4. **So `GetChaptersForBook` always returns empty**, and
   `abs/mapper.go:loadChapters` falls through to synthesizing chapters on the fly.

### 🔑 The important part: a backfill only helps SINGLE-FILE books

This is the non-obvious bit, and it decides whether a backfill is worth building.

| Book shape | Stored (scan) path | Live fallback (today) | Visible difference |
|---|---|---|---|
| **single-file** (m4b w/ embedded markers) | `probeSingleFileChapters` → the file's **real** embedded chapters | `SynthesizeChapters` over 1 track → **one** chapter for the whole book | 🔴 **Large.** 6 real chapters vs. 1. |
| **multi-file** (mp3 set) | `synthesizeMultiFileChapters` → `SynthesizeChapters`, one per file | `SynthesizeChapters`, one per file | ⚪ **None.** Same count, same titles; only sub-second boundaries differ (re-probed unrounded duration vs. stored `DurationSec`). |

Both paths call the **same** `audioutil.SynthesizeChapters`. So for a multi-file book a
backfill is a no-op as far as the user can see.

⚠️ **The book the owner was actually playing (`44669fab-6544-4414-ae2d-fa8eba7c52f3`)
is multi-file** — production traffic shows it streaming `/public/session/…/track/1`
and `/track/2`. **A backfill would change nothing for that book.**

### Decision needed

1. **Populate chapters** — pick one:
   - run `library.scan` (populates as a side effect, but does a great deal else, and
     has not run in 14 days for reasons nobody has written down); or
   - build the dedicated bounded-pool backfill op the Phase 4 spec called for
     (`registry.RunItems`, one ffprobe per single-file book).
   Either way, scope it to **single-file books** — that is where the entire visible
   gain is, and it avoids ~40k pointless ffprobe calls.
2. **Decide whether multi-file books should use their per-file embedded chapters.**
   `synthesizeMultiFileChapters` deliberately ignores them ("never from that file's own
   embedded sub-chapters, even when present — real ABS ground truth, spec §1.8.5").
   `audioutil.ShiftChapters` exists precisely to rebase them onto the whole-book
   timeline and is **unused** on this path. If the owner wants real chapters inside a
   multi-file audiobook, that is a **separate feature**, not a backfill — and it means
   deliberately diverging from real-ABS behaviour.

**Do not run a whole-library backfill without answering (1) first** — a scan touches
far more than chapters.

- [ ] **TODO-ABS-MODEB** A Cloudflare **service-token** assertion is rejected as
      invalid, so the documented "Mode B" (edge service token + our own bearer
      token) cannot work at all. A `non_identity` Access JWT carries
      `common_name` and **no `email` claim**, so
      `internal/oauth/cfaccess.go:59-60` fails it, and
      `internal/server/middleware/absauth.go:166-171` turns *any* Verify error
      into a terminal 401 that deliberately never falls through to the bearer
      path — so the request 401s **even when it also carries a valid ABS bearer
      token**, and `internal/server/handlers/abs/login.go:53-55` makes password
      login unreachable too. Fix: have `Verify` distinguish a cryptographically
      *valid* but non-identity assertion (sig/iss/aud/exp all pass, no email)
      from an invalid one via a typed sentinel (`ErrNonIdentityAssertion`), and
      map only that sentinel to a `(nil, nil)` fall-through in
      `ResolveCFAssertion` — every other Verify failure must stay a terminal
      401. Tests: (a) forged assertion still 401; (b) valid non-identity + valid
      bearer → 200 via jwt mode; (c) valid non-identity, no bearer → 401
      `no-credential`; (d) login with non-identity assertion + password body
      reaches the password path. Revert-validate (b) and (d).

- [ ] **TODO-SSO-EDGE** Neither native-app auth mode is actually configured at
      the Cloudflare edge, despite both being fully written up in
      `jdfalk/cloudflare-one` `access/audiobook-app-policies.md`. Measured via
      the CF API on 2026-07-31: the `books.jdfalk.com` Access app has exactly
      **one** policy (precedence 1, `allow`, email allowlist) — there is **no
      `non_identity` service-token policy** and **no service tokens exist on the
      account at all**; app-level `allow_authenticate_via_warp` is unset and
      org-level is `false`; and no cover-art bypass app exists (confirmed live —
      the cover path 302s to Access instead of reaching the origin). That fully
      explains the measured `service_token_status:false, is_warp:false,
      auth_status:NONE`. So `scripts/setup-audiobook-apps.sh` never ran against
      this account, or was rolled back — the doc describes a **design**, not the
      live state. Recommended path is **Mode C (WARP)**: it delivers a real
      identity JWT with an `email` claim, which satisfies `cf` mode exactly as
      already coded — no app changes, no `/status` change, no password. Mode B
      additionally needs TODO-ABS-MODEB fixed before it can work.

- [ ] **TODO-DEPS-VULN** GitHub reports 5 Dependabot vulnerabilities on the
      default branch (2 high, 3 moderate). Triage and bump.

- [ ] **TODO-SEC-BIND** The service binds every interface
      (`ExecStart=… serve --host 0.0.0.0 --port 8484`), so anything on the LAN
      reaches the origin directly and **Cloudflare Access is not a boundary** —
      the edge is only enforced for traffic that arrives through the tunnel.
      Bind loopback (or the tunnel-facing interface only) in
      `deploy/local.conf` so Access becomes the single front door, then verify
      the tunnel still serves `books.jdfalk.com`. Note in the PR that
      direct-to-LAN verification is no longer possible **by design** after this.
      The tunnel connector runs on rpi1-3, not on the origin host, so the
      loopback bind must account for that hop.

- [ ] **TODO-SEC-JWT** Rotate `ABS_JWT_SECRET` — it was pasted in plaintext into
      a chat transcript on 2026-07-31. It signs every ABS session token. Rotate
      it in `deploy/local.conf` (gitignored — never commit or print it; redact
      with `sed -E 's/(SECRET|TOKEN|KEY)=[^ ]*/\1=<redacted>/g'` when dumping a
      unit), redeploy, and confirm previously-issued tokens are rejected.

- [ ] **TODO-SEC-SYSTEMD** The unit has `User=audiobook`, `NoNewPrivileges`,
      `ProtectKernelTunables`, `ProtectControlGroups` and `PrivateTmp`, but no
      `ProtectSystem=strict`, no `ReadWritePaths`, no `CapabilityBoundingSet`,
      no `SystemCallFilter` and **no egress restriction**. `IPAddressDeny=any`
      plus a narrow allowlist is what stops a compromised process reaching the
      rest of the LAN. It needs the Whisper host on `:19847` and Ollama on
      `:11434`, plus outbound HTTPS for OpenLibrary/AcoustID — an over-tight
      rule silently breaks metadata and transcription, so test before claiming
      it works.

- [ ] **TODO-SRVTIMEOUT** Split or speed up the `internal/server` test package —
      it runs 434–480 s against Go's 600 s default per-package timeout, leaving
      under 30% headroom. Any concurrent load on the machine tips the whole
      package into a timeout that is indistinguishable from a deadlock: the
      panic dump names whichever goroutine happened to be mid-teardown
      (`operations/registry.(*Registry).Shutdown` blocked on `sync.WaitGroup.Wait`
      at `registry.go:1030` in the observed case), which reads as a real hang and
      sent a 2026-07-31 investigation down a false trail on PR #2083. Verified
      not a deadlock: the same commit passes in 480 s when run without competing
      load. Either shard the package, or set an explicit generous `-timeout` in
      the Makefile test targets so a slow run fails as "too slow" rather than
      masquerading as a lock bug.

      **The `-timeout` half is DONE.** #2270 put `-timeout 25m` on the Makefile's
      four `./...` targets (with a comment above `coverage:` explaining this exact
      masquerade); #2278 did the last live invocation that lacked it,
      `scripts/run-all-tests.sh`. A repo-wide sweep found no other. A bare
      `go test ./internal/server/` still runs on Go's 10m-per-package default.

      **Measured 2026-08-10 — the premise has drifted and the proposed fix is
      aimed at the wrong thing.**

      - Runtime is now **543 s** solo (`real 553 s`), not 434–480 s. Headroom
        against the 600 s default is **9.5%**, not "under 30%" — it has gotten
        worse, and that is *without* the `./...` contention the entry blames.
      - **~85% of wall time is idle**: `user 40.7 s + sys 40.0 s ≈ 81 s` CPU
        against 553 s wall. The package is not compute-bound; it is waiting.
      - **There is no slow test to fix.** 855 top-level tests summing to 540.5 s
        — so the time is inside tests, not compile or global fixture. The
        distribution: **4** tests ≥5 s (slowest `TestServerStartGracefulShutdown`
        at 14.1 s), **296** at 1–5 s, 89 at 0.1–1 s, 466 under 0.1 s. The top 25
        tests account for only ~85 s of 543 s.
      - **The cost is a fixed per-test fixture charge.** `setupTestServer` +
        `cleanup`, timed directly over 10 iterations, is **1.44 s mean**. There
        are **261 static call sites** (250 `setupTestServer`, 11
        `setupTestServerWithStore`), so ≈ **376 s, about 69% of the package**.
        That matches the 296-test 1–5 s band independently.

      **Which phase costs what** — measured per iteration over 5 iterations by
      timing each step of the fixture separately. The phases sum to **1.4425 s**,
      independently reproducing the 1.44 s figure above:

      | Phase | Mean | Share |
      |---|---|---|
      | `RunMigrations` | **828 ms** | **57.4%** |
      | `NewServer` (hub, queue, write-back batcher, fileIO pool) | 473 ms | 32.8% |
      | `NewPebbleStore` (disk-backed) | 134 ms | 9.3% |
      | `pools + store.Close` | 5.0 ms | 0.3% |
      | `RemoveAll` | 1.4 ms | 0.1% |
      | `MkdirTemp` | 0.44 ms | 0.0% |
      | `opRegistry.Shutdown` | **0.30 ms** | **0.0%** |
      | `opRegistry.Start` | 0.08 ms | 0.0% |

      **The registry teardown is NOT the cost.** It is tempting to connect the
      slowness to `opRegistry.Shutdown` blocking on `sync.WaitGroup.Wait`, since
      that is the goroutine the #2083 panic dump named — and an earlier draft of
      this entry asserted exactly that. The measurement refutes it: `Shutdown` is
      **297 µs**, four orders of magnitude below the fixture cost. The #2083
      panic dump named a goroutine that is normally free and only blocks under
      the contention that caused the timeout. Slowness and the deadlock-shaped
      panic are two separate phenomena that happen to name the same symbol.

      **This redirects the fix**, and not where sharding points. Sharding
      redistributes a 69% fixture charge without removing it; each shard still
      pays 1.44 s per test. **90% of that charge is `RunMigrations` +
      `NewServer`,** so those are the levers:

      1. **Migrations (57%)** — every test replays the FULL migration chain onto
         an empty store, producing a byte-identical result every time. Build one
         migrated Pebble directory once per package and copy/clone it per test,
         or share a migrated store where tests do not mutate global state.
      2. **`NewServer` (33%)** — construct the hub/queue/batcher/fileIO pool
         lazily, or let tests that only exercise handlers skip the parts they
         never touch.
      3. **Pebble open (9%)** — tmpfs or an in-memory VFS; worth doing only after
         the first two.

      Skipping `opRegistry.Start` — a lever an earlier draft proposed — would
      save **0.08 ms** and is not worth doing. Any of 1–3 is an
      isolation-sensitive refactor across ~260 call sites (`setupTestServer` also
      sets `database.SetGlobalStore`, so shared state is exactly where isolation
      would break) and wants its own plan, not a drive-by.

      *Not claimed:* the 543 s figure is a **single sample** on one idle Mac
      (the 1.44 s fixture cost has two independent samples that agree); 261 is
      **static call sites**, not dynamic invocations; and the phase table is one
      run of 5 iterations, so treat the shares as approximate rather than the
      millisecond values as exact.

      **🔑 CORRECTION 2026-08-10 — the package is not slow. The Mac's temp
      filesystem is. Lever 3 was ranked last and is actually the whole thing.**

      Same commit (`62b43c4e`), identical command
      (`go test ./internal/server/ -count=1`), three runs:

      | Run | Go package time | real | user | sys |
      |---|---|---|---|---|
      | Mac, normal `TMPDIR` (APFS) | **532.524 s** | 538.72 | 18.36 | 42.32 |
      | Mac, `TMPDIR` on a RAM disk | **33.704 s** | 36.32 | 6.82 | 7.59 |
      | U1 (Linux, 48-core) | **35.453 s** | 50.66 | 114.42 | 30.96 |

      **A 15.8× speedup from one environment variable**, landing within 2 s of
      an independently-measured Linux box. The Mac spent ~61 s of CPU across
      538 s of wall clock — **11% utilisation**. It was blocked, not computing,
      and `sys` fell 42.32 s → 7.59 s.

      This does not overturn the phase table above; it explains it. Migrations
      dominate *because* they write, and the write is what is expensive on
      APFS. The three levers were ranked by share of a cost that is itself an
      artifact of where the temp directory lives:

      - Levers 1 and 2 (migration snapshotting, lazy `NewServer`) are an
        isolation-sensitive refactor across ~260 call sites, and would buy less
        than moving the temp dir.
      - **Lever 3 — "tmpfs or an in-memory VFS; worth doing only after the
        first two" — is the fix, not the afterthought.** It was ranked at 9%
        because that is Pebble *open* time alone, but a memory-backed temp dir
        removes the durability cost from every phase that writes, migrations
        included.
      - **Sharding the package remains the wrong target.** It redistributes a
        cost that is not CPU-bound in the first place.

      *Not claimed:* this measures **the temp filesystem**, not `F_FULLFSYNC`
      specifically — the syscall was never isolated, so macOS full-barrier
      fsync is the *likely* mechanism, not a verified one. The cross-platform
      `user`-time comparison is also not trustworthy (rusage attribution for
      grandchildren differs between macOS and Linux); the argument rests on
      wall clock and Go's own package timer. Each row is a single sample, but
      the effect is far larger than any plausible run-to-run variance.

      **The `-timeout 25m` work is NOT invalidated** — it remains correct for
      CI and for any macOS developer without a RAM disk. What changes is that
      the timeout is a guard, not a workaround for something unfixable.

      **Open question for the owner (do NOT decide alone):** whether to make
      this the default — a `TMPDIR`-on-tmpfs Makefile target, or test-only
      Pebble sync settings. Both alter shared test infrastructure and one
      weakens durability guarantees in tests, so they are a judgement call, not
      a drive-by. The measurement is the deliverable here; the policy is yours.

## SEC: origin is reachable from the LAN — "bind loopback" is NOT achievable as specified

**Status:** finding, not yet fixed. Needs an owner decision between two options.

The origin listens on `*:8484`, so anything on the LAN reaches it directly and
Cloudflare Access is not a boundary for those callers. The standing task says to
"bind loopback instead of `0.0.0.0`". **That specific change cannot work here**, and
it is worth writing down why so nobody tries it again:

`cloudflared` does not run on the origin host. It runs on rpi1-3 and dials the origin
over the LAN. So the listener must be reachable from another machine by definition.
Binding `127.0.0.1` makes the tunnel unable to connect at all — the site goes down.
And binding the host's LAN address instead of `0.0.0.0` is **exactly as exposed**:
both accept connections from anywhere on the LAN. There is no bind address that is
simultaneously "not reachable from the LAN" and "reachable from rpi1-3 over the LAN."

Two options actually accomplish the intent. Both are host-level changes outside
`deploy/local.conf`, and both need interactive-sudo, so neither was applied:

1. **Firewall the port** (recommended, smallest change). An nftables/ufw rule
   restricting `:8484` to the rpi source addresses. Keeps the current topology; the
   origin stops answering everything else on the LAN. Care required: touch only 8484,
   never 22, or you lock yourself out of the box.
2. **Move `cloudflared` onto the origin host.** Then `127.0.0.1:8484` is genuinely
   correct and the port disappears from the LAN entirely. Larger change — it moves
   the tunnel off the rpi fleet and changes where tunnel outages come from.

**Note for whoever does this:** after either change, verifying the origin by curling
it directly from a workstation stops working *by design*. That is the success
condition, not a regression. Verify through `books.jdfalk.com` instead.

- [ ] **ABS-SYNC (Phase 6, DATA LOSS if skipped): wire a `UserDataProvider` into the
  ABS auth handler.** `internal/server/handlers/abs` currently constructs with
  `UserData: nil` (`internal/server/wire_abs_routes.go`), so `/api/me`, `/login` and
  `/auth/refresh` report `mediaProgress: []`. That is correct **only** while the server
  holds zero ABS progress records — §1.8.1 of the design spec: AudioBooth *deletes*
  every local progress row absent from the server's list, so the moment Phase 6 starts
  persisting progress without wiring the provider, every device loses its listening
  positions on the next home-screen refresh. The interface is already defined
  (`MediaProgress`/`Bookmarks`, both must return the COMPLETE list; returning an error
  makes the handler answer 5xx rather than serve a truncated list). A startup
  `slog.Warn` flags the gap until it is wired.

- [ ] **ABS-SYNC: exempt the ABS surface from `BasicAuth()` when `basic_auth_enabled`
  is on.** The ABS group hangs off `s.router`, so it inherits the global
  `servermiddleware.BasicAuth()`. With basic auth enabled (off by default) every ABS
  client would need to send `Authorization: Basic …`, which collides with the ABS
  bearer token on the same header — the clients would be unable to connect and the
  cause would be invisible. Either exempt the ABS paths in `basicauth.go` or document
  that the two features are mutually exclusive.

- [ ] **ABS-SYNC: prune expired `abs_sess:` records on a schedule.**
  `PebbleStore.DeleteExpiredABSSessions` exists and is tested but has no caller. Add it
  to the same maintenance sweep that calls `DeleteExpiredSessions` for the browser
  keyspace, or revoked/expired ABS sessions accumulate forever.

- [ ] **ABS-SYNC: consolidate the two DRM detection paths, and wire one into the
  scanner.** PR #2067 adds extension-based `DetectDRM` in `internal/audioutil/drm.go`,
  but `internal/diagnosis/probe.go` already has an unrelated, richer mediainfo-based
  probe (`HasActiveDRM`). Two DRM code paths will drift. Decide which is authoritative,
  then wire it into the scanner so Audible AAX/AAXC files surface as
  **unplayable-with-reason** instead of importing and failing at play time. Note the live
  bug this fixes: `.aax`/`.aaxc` are **already** in the default `SupportedExtensions`
  (`internal/config/config.go` ~:2016) with zero DRM awareness. Caution: ffmpeg's `aax`
  demuxer is **CRIWARE game audio, not Audible** — do not key detection off it.

- [ ] **ABS-SYNC TASK-12 (P1, data-loss class): close the three identity gaps so §4.3's
  ID-durability claim is actually true.** Owner decided (2026-07-30) to hook **all three**
  paths, not just the worst one. Today only `merge.Service.MergeBooks` repoints sync IDs;
  these three still orphan a device's listening position:
  1. **`dedup.MergeBooks`** (`internal/dedup/book_dedup.go:395`) — a separate, still-live
     path used by `internal/reconcile/itunes_heal.go` that **HARD-DELETES**. An
     unrepointed sync ID here is unrecoverable: there is no surviving row to repoint later.
  2. **`CombineBooks`** — same file as the hooked merge, unhooked.
  3. **Untagged move** — `internal/scanner/scanner.go` (~2078-2099) mints a fresh Book
     ULID via `CreateBook` + version-link and never calls `RepointSyncItem`.
  Primitives already exist and are merged (`RepointSyncItem` in #2070,
  `RepointSyncFile` in #2068). Note `internal/merge/serialize.go` already provides a
  process-wide `mergeSerializeMu`, so no extra book-ID partitioning is needed — run
  inside that existing critical section. Requires a `-race` test exercising concurrent
  merges (`MergeBooks` has a prior race history in this repo).

- [ ] **ABS-SYNC: wave 2 — scanner + merge wiring.** Briefs in
  `docs/agent-tasks/abs-sync/`. TASK-03 (merge-follow hook into
  `merge.Service.MergeBooks`), TASK-07 (extract + persist chapters at scan time via
  `internal/scanner/process_file.go`), TASK-09 (bookmarks CRUD — no bookmark feature
  exists today). Wave 1 merged: #2070, #2068, #2069.
- [ ] **ABS-SYNC: wave 3 — backfill + survival proof.** TASK-04 (idempotent sync-ID
  backfill over the existing library; MUST use a bounded worker pool per the CLAUDE.md
  concurrency rule), TASK-05 (ID-survival suite: rename / move tagged+untagged / retag /
  merge / file-replace). TASK-05 is the acceptance bar for §4.
- [ ] **ABS-SYNC: TASK-11 — auth core, both credential modes.** Brief not yet written.
  Unified identity resolution per spec §3.0.1: verified `Cf-Access-Jwt-Assertion` →
  user, else our own JWT, else 401. Mode B needs JWT + DB-backed sessions + **30d**
  access TTL (NOT 1h — see §1.6) + argon2id; Modes C/A trust the CF assertion with JIT
  provisioning against the allowlist, fail closed. Mandated test: the ABS router group
  must NOT inherit the `/api/v1` fail-open `cfaccess` behaviour — that would be an
  authentication bypass. Only this task may touch `go.mod`.
- [ ] **ABS-SYNC: Phase 3 — DTO mapping + library browse.** Depends on waves 1–2 and
  TASK-11. Must honour the verified client contract (§1.7–1.8): `publishedYear` as a
  **String**, non-null `userDefaultLibraryId`, **never paginate `user.mediaProgress`**
  (it deletes client-side progress), integer `total`/`numBooks`, real JSON booleans,
  flat `authorName`/`narratorName`, and never an empty `audioTracks: []` (omit the key
  instead). Gated by the merged conformance harness.
- [ ] **ABS-SYNC: Phase 5b — playback routes.** `POST /api/items/:id/play`,
  `GET /api/items/:id/file/:ino`, and the **unauthenticated**
  `GET /public/session/:id/track/:index` that AudioBooth streams from (§1.8.3). Uses the
  merged `internal/httputil` Range helper. Direct play only; HLS must degrade cleanly.
- [ ] **ABS-SYNC: Phase 7 — socket.io (Absorb only).** AudioBooth needs no websocket at
  all (verified against its `Package.swift`), but Absorb goes offline after 5 failed
  reconnects, and expects `emit('auth', <raw token string>)`. Deprioritized: the primary
  client ships without it.
- [ ] **ABS-SYNC: Phase 8 — topology, runbook, migration guide.** Cloudflare Access
  service token in a **dedicated Service Auth policy ordered FIRST** (the trap that bit
  users in both clients' issue trackers), the cover/image bypass (§1.9.5), tunnel-level
  JWT enforcement, and the client compatibility matrix. Runbook must record: never trust
  an app's reachability checkmark (Access returns HTTP 200 with HTML, so failures look
  like JSON decode errors), and AudioBooth's first-server-add cover bug is upstream, not
  ours.

- [ ] **REGROUP-PARTCHAPTER-PARSER** The Mistborn-style "Ambiguous folder" case
      (`01 P0-C0.mp3`, `07 P1-C6.mp3` — Part/Chapter naming, non-contiguous numbers)
      has no parser and stays classified as ambiguous (unaffected by the disc/track
      fix). Consider a Part→disc / Chapter→track parser as a fast-follow so these
      collapse with correct numbering too.

- [ ] **iTunes 2-way-sync P3 (cleanup) — decision: MEASURE-AND-STOP, no removal machinery.**
  The P0 cleanup provenance census ran on prod (97,999 `.itl` tracks): **provable merge
  orphans = 1, SHA-gated removable = 0** (`pid-census --merge-provenance`). P3 retires the
  unsafe `cleanup_merged.go` handler as a guarded no-op; do NOT build bulk removal. The
  count is a floor — prod has no durable merge-provenance trail (`merge.Service.MergeBooks`
  writes neither the `AutoMergeJournalEntry` journal nor `MergedIntoBookID`; the journal is
  empty). FOLLOW-ONS (not blocking): (1) if provenance-anchored cleanup is ever wanted, FIRST
  make the merge path record losers durably, THEN re-run this census; also a latent
  unmerge/audit gap. (2) Classify the 13,464 `no_live_owner` tracks by audiobook genre to
  separate the user's non-AO music/podcasts from severed orphans (doesn't change the P3
  decision). See `docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F4.
- [ ] **iTunes 2-way-sync — remaining P0 measurements.** (a) Cross-type PID collisions
  (audiobook vs non-audiobook sharing a PID) — confirm PID-on-multiple-primaries stays 0
  post pid-repair. (b) Bookmark/field-preservation byte-proof: run a relocate AND a
  track-remove through `SafeWriteITL` on a ZFS clone, byte-compare every untouched track's
  record, assert ZERO changes. Then P1 (partitioned count-refresh, re-derive PID sample) /
  P2 (relocate-only sync-cycle op + oracle = MVP end).

- [ ] **iTunes 2-way-sync P2 — relocate-only sync cycle (MVP end).** All prerequisites are
  merged: 4-state `LibrarySet` config (#2040), cleanup census → P3 no-op (#2041),
  cross-type + preservation proofs (#2042), relocate oracle `VerifyRelocateWrite` (#2043),
  P1 `RefreshLibraryIdentity`+`PartitionedTrackCount` (#2044), F7 guard scope
  `ContractConfig.AllowedWritebackRoot` (#2045). Compose the cycle: (1) read AO `.itl` +
  `RefreshLibraryIdentity` → ExpectedIdentity; (2) plan relocate from DB `book_file`
  locations vs `.itl` 0x0D (existing relocate op → `[]ITLLocationUpdate`, 0 adds/0 removes);
  (3) `SafeWriteITL` with `ContractConfig{AllowedWritebackRoot:<AO media root>,
  ExpectedIdentity:<refreshed>, ExpectedTrackCount: PartitionedTrackCount →
  planAudiobook+liveNonAudiobook, Force:false}` + `.bak` + bounded-delta capped at
  `len(LocationUpdates)`; (4) `VerifyRelocateWrite(before,after,relocatedPIDs)` BEFORE the
  atomic rename; (5) oracle OK → rename, else restore `.bak` + alert. Single-flight lock; never
  concurrent with manual relocate/pid-repair/cleanup. Wire `AllowedWritebackRoot` from the AO
  library's own media root (LibrarySet). See `docs/specs/2026-07-23-itunes-2way-p0-findings.md`
  (P0 status table) + `docs/specs/2026-07-23-itunes-2way-sync-system-design.md` §4–6.

- [ ] **`isAudiobookITL` under-classifies audiobooks (fail-safe, but fix carefully).**
  P0 cross-type census (§F5) found it misses `Audio Book`/`audio book` (it checks the
  substring `"audiobook"` with NO space — 705 tracks on prod) and every literary-genre
  audiobook (Science Fiction, Fantasy, Suspense, Comedy, …) — 3,436 AO-owned audiobooks
  total classified non-audiobook. Impact: for `GuardRebuildTarget` this is FAIL-SAFE
  (inflates the non-audiobook count → guard more likely to block), so no urgent safety bug.
  But: (a) never use `isAudiobookITL` as a relocate/cleanup targeting filter; (b) if fixing
  the heuristic (add the space variant, broaden genres), it LOWERS the non-audiobook count
  and could drop a real library below `GuardRebuildTarget`'s "looks real" threshold — so
  re-derive those thresholds in the SAME PR and re-test the guard. See
  `internal/itunes/library_shape.go:35` + `docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F5.

- [ ] **🚧 P2 BLOCKER — location-form guard rejects the entire live AO library (F7).** The
  `location-form` safety guard (`internal/itunes/itl_safety_contract.go:562`) rejects any
  `SafeWriteITL` when a track's 0x0D/0x0B contains `.itunes-writeback/`. On the live AO
  library that is **82,976 tracks** — because the AO library physically lives at
  `W:\audiobook-organizer\.itunes-writeback\` so its iTunes media folder legitimately is
  `…\.itunes-writeback\iTunes Media\`. The guard was built to catch a staging path leaking
  into the hands-off Original library (damaged-4); in the hard-cutover design (iTunes pointed
  AT the AO library) the substring is correct and unavoidable. Result: the P2 relocate op
  **cannot write the library at all** (`Force` does not override location-form — only the
  bounded-delta guard). FIX (owner decision): (1, preferred) scope the staging-marker check to
  the write TARGET using the P0 4-state `LibrarySet` mode facts — reject `.itunes-writeback/`
  only when writing the Original library, or only when the path's `.itunes-writeback/` root
  differs from the AO library's own root; or (2) physically move the AO library + media out
  from under a `.itunes-writeback/` dir (invasive). Reproduced by
  `TestITLRelocateContractStatus` (env-gated). See
  `docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F7.

- [ ] **iTunes 2-way-sync — continuation (P3 redefine + reverse sync + footgun audit).**
  P1 relocate is applied+verified on prod (6,414). Still open, per
  `docs/plans/2026-07-23-itunes-2way-sync-continuation.md`: (1) redefine the P3
  merged-track removal to provable-duplicates-only (version_group/MergedIntoBookID
  linkage) — current `IsPrimaryVersion==false` criterion is UNSAFE (would delete real
  chapter files); explain the 4,298 shared-PID oddity. (2) Build the reverse sync
  (iTunes → writeback → AO) so media added/played/playlisted in iTunes syncs back once
  it's used full-time; decide the source-of-truth model + import from the writeback
  library not `books/itunes/`. (3) Guard/deprecate the destructive `/rebuild` +
  `/rebuild-full` against the now-real library; define the adopt-base steady-state.
  Dry-run + sample + owner sign-off before any destructive apply.

- [ ] **iTunes book_file PID uniqueness — apply the backfill repair (gated).** Forward
  invariant shipped (`CreateBookFile` transfers a duplicate PID to the new row) + census
  (`GET /itunes/pid-integrity`) + repair (`POST /itunes/pid-repair`) endpoints built and
  tested. Prod census: 8,987 duplicate PIDs (8,762 same-file, 225 diff-file, 94 on multiple
  primaries). Repair dry-run auto-resolves 8,984 (3 ambiguous left for review), clears 9,050
  redundant PID copies, DB-field-only (no file/row deletion). REMAINING: deploy → run
  `pid-repair?dry_run=true` on prod to confirm → owner runs the apply with `!` → re-run the
  census to confirm 0 duplicates → review the 3 ambiguous groups by hand. See
  `docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md` §1.5b.

- [ ] **iTunes 2-way sync writeback (edit-in-place, preserve play-state).** The deployed
  `rebuild-full` writeback regenerates the library (12,193 tracks / 14 playlists) vs the real
  97,782 / 356 — valid but lossy (no play counts, ratings, playback bookmarks, music/podcasts,
  user playlists). Redirect to surgical edit-in-place via `UpdateITLLocations`, scope-gated by
  `IsAudiobook`, per `docs/specs/2026-07-22-itunes-2way-sync-writeback-design.md` (draft PR #2033).
  Phased P0–P4; resolve §8 open decisions (PID persistence, bookmark mhod, read-back scope, base
  selection, cadence) before implementation. Discard the current 2 MB prototype library.

The 2026-H1 TODO history (3,220 lines) is frozen verbatim at
[`docs/archive/todo-2026-H1.md`](docs/archive/todo-2026-H1.md).
Source anchors below (`H1:NNN`) cite line numbers of the **original** TODO.md;
in the frozen archive copy add 6 (banner block) to each number.

This file lists the 49 items confirmed ACTIVE by the 2026-07-17 docs audit, plus
the 2026-07-17 multi-discipline review-findings backlog (crash-recovery record,
last section).
Everything shipped or obsolete was dropped, including every stale 380K/384K/387K
dedup-candidate figure — the real backlog is **15,269 pending / 9,074
exact-pending** (see [`docs/dedup/STATUS.md`](docs/dedup/STATUS.md)).
Corrections applied per the audit: review-queue **PR-B2 is MERGED (#1953)**;
INIT completion is **~46/50 briefs** (not "35 remaining"); the managed
tool-lifecycle **IS built** (`internal/tools/*`, `/api/v1/tools`, Settings → Tools).

Companion docs:
- Run-on-prod queue: [`docs/operations/pending-prod-actions.md`](docs/operations/pending-prod-actions.md)
- Human-decision queue: [`docs/plans/DECISIONS-PENDING.md`](docs/plans/DECISIONS-PENDING.md)
- Dedup state: [`docs/dedup/STATUS.md`](docs/dedup/STATUS.md)
- 2026-07-17 multi-discipline findings: [`docs/audits/2026-07-17-multi-discipline-review.md`](docs/audits/2026-07-17-multi-discipline-review.md)

## Dedup (10)

1. **CONS-10 / INIT-2 T6 — prod drain/triage of the exact-candidate backlog** (H1:983;
   [plan](docs/plans/2026-07-10-dedup-pipeline-hardening.md)) — code merged, run NOT
   executed; operator-gated; validate on the dedup sandbox first (private runbook in
   falkcorp/infra-docs). Real backlog ~15,269 pending.
2. **PH-2 — run `maintenance.dedup-exact-triage` on prod + review populations; PH-2b
   per-population purge wave** (H1:916) — never blanket-purge; four residual
   populations (see `docs/dedup/STATUS.md`). **Apply path now exists** (T03-BUILD):
   `maintenance.dedup-exact-triage {"apply":true}` dismisses purgeable classes
   (stub/title_leak) via `UpdateCandidateStatus(id, "dismissed")` — dry-run
   (`apply=false`, the default) is unchanged report-only. Unblocks brief T03's
   sandbox purge wave.
3. **REVIEW-band candidate producer for the review queue** (H1:35) — B2 fast-follow;
   no commit yet.
4. **Flip `review_apply_enabled` ON in prod** — apply path merged (#1953) but default
   OFF (6f2f7ce0); gated human decision (see DECISIONS-PENDING).
5. **C8 — auto-file issues per `not_dup` cluster** (H1:1332; INIT-10 T5) — deferred.
6. **INIT-1 T05 follow-up — per-kind confidence field in `DedupSignalConfig`** (H1:250)
   — **persistence scaffolding DONE** (2026-07-18): `config.DedupSignalConfig.Confidence`
   + `unified.SetKindConfidenceOverrides` (mirrors `SetBandThresholds`) + `registry_wire.go`
   wiring, so a per-kind confidence bound now survives `UpdateConfig`/restart. **Still
   blocked**: `unified.ComposeScore` ignores `cfg.Signals[kind]` bounds entirely (reads
   `Signal.Confidence` verbatim), so the field has no effect on live scoring yet, and
   `dedup.calibrate-composite`'s Round 2 sweep still doesn't write it — decision needed
   on whether `ComposeScore` should clamp against it (see
   [`docs/plans/DECISIONS-PENDING.md`](docs/plans/DECISIONS-PENDING.md) row 10).
7. **Async breakdown-refresh for bulk/cluster dismiss** (H1:1877) — per-pair synchronous
   refresh may need an async variant at scale (latency note).
8. **Omnibus detection + dedup** — spec-only
   ([`docs/superpowers/specs/`](docs/superpowers/specs/) 2026-05-31); not started.
9. **Regression tests for the 2 untested deluge hydrate sites** (H1:568) — optional.
10. **Hide system-sourced tags from the Browse-by-Tag cloud** (H1:433) — UX preference,
    not a bug.

## Identification / metadata (5)

11. **AI-enrichment tier for the ambiguous regroup pile** (H1:35) — B2 fast-follow;
    blocked on local Ollama capacity.
12. **Cover recovery fast-follow** (H1:35) — B2 fast-follow.
13. **Community audiobook fingerprint index (INIT-8)** — spec-only
    ([spec](docs/specs/2026-07-10-community-fingerprint-index-design.md));
    STOP-FOR-HUMAN brainstorm/review session required.
14. **Description fetch campaign — ~29,083 books without descriptions** (H1:790).
15. **LLM/embeddings backend-mode toggle** (extracted from the archived 2026-07-02
    status doc) — config enum + FE selector (disable-all / OpenAI-only / local-only /
    OpenAI+local-fallback) + model-download prompt; local target qwen2.5:7b-instruct
    on the GPU box. Status unverified — check before building.

## Pipeline (8)

16. ~~**Library heavy-filter + non-title-sort returns 0 books** (H1:301-330)~~ —
    **FIXED** (fix/library-filter-zero-results): root cause was `GetAudiobooks`
    re-applying an already-pushed-down filter against BookSummary→Book
    projections missing fields like Language/Genre/FingerprintStatus; the
    re-check silently dropped every row. Now skips the redundant re-filter and
    sort+paginates the pushdown result directly. Left a new backlog item (16b)
    for the separately-discovered author/series-by-name FieldFilter gap found
    during this investigation.
16b. ~~**Advanced-search `FieldFilters` on `Field: "author"`/`"series"` always
    return 0 books** (found during #16's investigation)~~ — **FIXED**
    (fix/fieldfilter-author-series-hydration): confirmed root cause —
    `fieldMatchesValue` (`internal/audiobooks/service_filtering.go:274`) reads
    `book.Author.Name`/`book.Series.Name`, but per `database.Book`'s own doc
    comment those are "Related objects (populated via joins, not stored in
    DB)" — the memdb-resident `*Book` never carries them (only
    AuthorID/SeriesID), and even the Pebble `GetBookByID` raw-JSON fallback
    doesn't hydrate them either, so every author/series FieldFilter compared
    against `""` and rejected every row. Fix: `buildAuthorSeriesNameMaps`
    fetches all authors/series once per query (cheap — small, fully in-memory
    collections, same `GetAllAuthors`/`GetAllSeries` accessor
    `author_series.go`'s `ListSeriesWithCounts` already uses) and
    `hydrateAuthorSeriesNames` populates a per-book copy's Author/Series from
    those maps before `fieldMatchesValue` runs, at the single choke point
    (`matchesFieldFiltersWithStrippedFallback`) both the memdb pushdown
    predicate and the mock/non-pushdown post-filter path go through — no
    per-book store call. `CountAudiobooksFiltered` shares the same predicate
    builder so the paginated total is fixed too.
17. **iTunes path-heal residuals** (H1:899-906) — 3,720 ambiguous / 5,349 not-found /
    4,734 doubled-path records still unresolved.
18. **AP-1b — physically co-locate survivor's files after Combine** (H1:936) — inside
    RootDir only.
19. **AP-3 duration-reextract ~721-book tail** (H1:949) — re-enqueue apply
    (see pending-prod-actions).
20. ~~**AP-3b — consolidate the 3 duration extractors into one** (H1:954).~~ DONE —
    `internal/audioutil.ProbeDurationSeconds` is now the single ffprobe
    implementation shared by `internal/mediainfo`, `internal/fingerprint`, and
    `internal/transcode`; each call site keeps its own unit/error contract.
21. **CONS-18 Part 2 — file-tag duration write-back** (H1:1019; spec 2026-06-19 DRAFT)
    — config-gated; deferred until dedup re-scope settles.
22. **Torrent relocation INIT-5 T2–T7** ([plan](docs/plans/2026-07-10-torrent-relocation.md))
    — T1 shipped (18570a39); T2 = human-gated Deluge spike blocks T3–T7.
23. **Fingerprint UI verifications ×2** (H1:1383-1384) — [hold] verify the 14K
    false-positive purge is visible in dedup UI; book-sig coverage % renders.

## Workflow / ops (4)

24. **Workflow system WF-0/2/3/4/5 (INIT-6)** (H1:1128-1133;
    [plan](docs/plans/2026-07-10-workflow-system.md)) — STOP-FOR-HUMAN spec review;
    WF-6 closed NOT-DOING. Implementation plan (owner-approved 2026-07-18, PR #1935):
    [`docs/plans/2026-07-13-workflow-system-implementation-plan.md`](docs/plans/2026-07-13-workflow-system-implementation-plan.md)
    — grounds the spec against HEAD; recommends **build WF-2, defer WF-3/WF-4/WF-5**
    (INIT-1 T5+T6 shipped, so WF-3's headline use case exists without it; the spec's
    completeness gate is blind to the nested-config `label_refinement` family).
25. **PD-1 — subprocess isolation via parent-RPC bridge + MDA3 `Isolate:false` revert**
    (H1:1554-1561, 1435-1438; [spec](docs/specs/subprocess-isolation-rpc.md)) — [hold].
26. **INTERNAL-SERVER-PKG-STALL structural decision** (H1:849-877) — leak fixed;
    residual needs an owner decision: raise timeout / split package / migrate ~60 call
    sites to `newTestServer` (see DECISIONS-PENDING).
27. **Responses-API migration AI-RESP-A/B/E/F (INIT-7)** (H1:2596-2603) — [hold,
    do-not-start-without-greenlight]. AI-RESP-C/D closed.

## Logging / verification / security-ops (5)

28. **SLOG-PROD-VERIFY** (H1:2038; [runbook](docs/operations/slog-prod-verify.md)) —
    live prod smoke test of the op-activity chain.
29. **SLOG-W13 residual ~1,346 raw slog calls** (H1:2037) — remaining calls enumerated
    out-of-scope (no-ctx funcs, lifecycle, background); candidate to CLOSE with a
    scope note.
30. **SEC-AUDIT-11 — CodeQL bulk-dismissal rationales** (H1:2267) — GitHub-console
    action.
31. **PD-3 — post-deploy prod verification checklist** (H1:1568-1574;
    [checklist](docs/pd3-prod-verification.md)) — checklist exists, never filled in.
32. **I1 + I6 — prod pprof verification** (H1:1515, 1538) — measure chromem-lazy
    effect + heap re-audit; measurement only.

## Infra (5)

37. **CPU busy-loop: `CountPrimaryBooks` full-scan on the 5s metrics ticker** — ✅ DONE
    (2026-07-18): the server burned ~2 cores continuously while idle because
    `CountPrimaryBooks` (`internal/database/pebble_store.go`) full-scans + `json.Unmarshal`s
    all ~44K books (~5.6s) and the 5s status ticker
    (`internal/server/server_lifecycle.go`) called it every tick, running scans
    back-to-back (presented as ~189% CPU with only `sweep tick waiting_count=0` logs; also
    made `/api/v1/health` ~5.6s). Fixed with a 30s in-memory TTL cache + recompute gate on
    `CountPrimaryBooks` (regression test `TestPebbleCountPrimaryBooksTTLCache`). Diagnosed
    while health-checking the (now torn-down) dedup sandbox.

33. **REPO-SIZE-1 decision** ([plan](docs/plans/2026-07-10-repo-size-history-rewrite-plan.md),
    [package](docs/plans/2026-07-12-repo-size-targeted-purge-package.md)) —
    STOP-FOR-HUMAN; plan recommends Option (d) forward-only + GitHub Support gc.
34. **Execution-manifest human gates**
    ([manifest](docs/plans/2026-07-10-execution-manifest.md)) — the residual gated
    tasks: INIT-5 T2 spike sign-off, INIT-6 spec review, INIT-7 greenlight, INIT-8
    review, REPO-SIZE-1.
35. **Consultancy wave 4+ residuals** ([roadmap](docs/consultancy/00-ROADMAP.md)) —
    unverified; needs a close-out sweep against shipped work.
36. **Op-progress Prometheus metric (T12 follow-up)** — ✅ DONE (PR #2014,
    2026-07-18): added `audiobook_organizer_op_items_processed{op_id,op_type}`
    + companion `audiobook_organizer_op_items_total{op_id,op_type}` gauges
    (`internal/metrics/metrics.go`, `SetOpProgress`/`ClearOpProgress`), set on
    every `dbReporter.UpdateProgress` call
    (`internal/operations/registry/reporter_db.go`) and deleted on every
    terminal transition via `registry.publishOpTerminal`
    (`internal/operations/registry/registry.go`) so stale op_ids never
    accumulate. Uncommented + finalized the "op stalled" alert in
    `deploy/prometheus/alert-rules.yml` (`AudiobookOrganizerOpStalled`,
    `rate(audiobook_organizer_op_items_processed[30m]) == 0` for 30m —
    existence of the series itself proxies "op is active" since it's deleted
    at terminal, so no separate `op_active` gauge was needed). Closes the
    observability gap behind the 3+ hour `dedup.full-scan` hang and the 9hr
    Pebble write-stall freeze — both were only noticed by a human watching
    the UI.

## UX (4)

36. **1.16 — resizable/sortable columns** (H1:2750) — remaining: dedup results,
    activity log, iTunes write-back preview, metadata review.
37. **1.17 — product rename/branding sweep** (H1:2751) — blocked on name decision.
38. **3.8 Plex-style media server API; 3.9 LLM series detection; 3.10 AI cover art**
    (H1:2772-2774) — all [hold].
39. **Fleet-tasks reconciliation** — real residuals = 030/031/036 (≡ 4.10/4.8/1.16);
    032–035 are stale-shipped and need closing in the fleet tracker.

## Other / close-out (10)

40. **4.8 — Store ISP sweep** (H1:2787) — **RE-SCOPED 2026-07-18; the "~38-file + 18
    noop" count below was pre-reorg and is WRONG.** Re-audit found `database.Store` is a
    field/param in **~151 prod + 35 test files** (a package reorg since the April plan
    split `internal/server` into `internal/audiobooks|metafetch|merge|organizer|
    maintenance/jobs|server/handlers/*`, obsoleting the file lists in
    `docs/archive/superpowers/plans/2026-04-17-store-iface-sweep.md` — whose COMPLETE
    stamp reflected a deliberate "diminishing returns on the hubs" stop that STILL holds
    post-reorg). **Decision 2026-07-18: do the DI-seams + shallow-consumer subset only**
    (narrow the 8 `internal/server/handlers/*/interfaces.go` + `internal/server/
    interfaces.go`, plus genuinely-shallow post-April consumers; leave hubs/bootstrap/
    wiring/decorators wide with justification comments) — NOT the full 151-file sweep.
    Type-only change (no runtime/data impact); existing `mocks.Store` already satisfies
    every sub-interface so no wave triggers a mockery regen. Old sweep tooling
    (`scripts/{check_store_noops,narrow_struct_services,apply_narrowing}.py`) survives but
    its hardcoded file lists must be regenerated. **Not started; deferred behind the
    dedup+review consolidation work (items 50–52).**
41. ~~**4.10 — MergeService mock-store unit tests** (H1:2789)~~ — DONE: `internal/merge`
    coverage 70.3%→96.6%. Added 34 tests across external-ID reassignment, ITL-removal
    enqueue, loser soft-delete, nil/empty-override wipe-safety, version-group integrity
    (incl. a real bug found: `MergeBooks` didn't de-dupe `bookIDs`, so a caller passing
    the primary twice — the exact class PR #2007 patched only at one caller — silently
    demoted the winner to non-primary with no soft-delete; fixed defensively in
    `Service.MergeBooks` itself), CombineBooks file-transfer/author-override error paths,
    and the merge-family serialization lock helpers.
42. **2026-05-01 re-audit block close-out pass** (H1:3137-3177) — TEST-2, DEP-1a-e,
    DEAD-1, CTX-4, LOG-5, R-9, R-10 mostly stale: DEP-1 0 non-test hits, DEP-1e moot
    (post-SQLite removal), PERF-1 OBSOLETE as scoped (Jul-16 truncation fix made
    whole-library ops deliberately unbounded). Needs a checkbox-level close-out.
43. **WaitForWarmup hazard note** (H1:3118) — latent create-then-read-memdb test
    hazard; document or fix.
44. **GFO-4 — graceful-file-ops sub-op phase tracking** — last open graceful-file-ops
    item.
45. **Performance items #1/#2/#6** (2026-04-14 set) — still open.
46. **Duration/filesize aggregation** — Book fields show snapshots instead of sums;
    likely stale (F5-T026 shipped) — verify then close.
    - ~~**46b. `/audiobooks` LIST endpoint mis-serializes `duration`** (found
      2026-07-19)~~ **DONE 2026-08-03 (#2125).** The reported symptom — list says
      `duration: 4` where the detail endpoint says `4680` — was the arithmetic itself:
      `4680 / 1000` truncates to `4`. `service_filtering.go:923` divided every
      duration by 1000 unconditionally while aggregating, so the rows it corrupted
      were the *correct* second-valued ones. Now routed through
      `database.NormalizeDurationSec`, which classifies per row from the implied
      bitrate. Same fix applied to `handlers/versions.go` and
      `handlers/audiobooks/handler_files.go` (×2). Far from low-severity: it affected
      25,938 books.
47. **Library centralization backlog** — needs a brainstorming session; future work.
48. **Transcription quality filter** — ~40% of transcripts low-quality/unparsed; list
    endpoint omits transcription fields.
49. **iTunes heal Layer-6 re-trigger** (H1:897) — re-run after path-heal residuals
    shrink (see pending-prod-actions).

## Dedup + review consolidation (3) — 2026-07-18 owner request

Owner directive (2026-07-18) while reviewing the live dedup/review experience: the
current dedup page is too heavy, the review UI is poor, and obvious near-identical
duplicates (same file, differing by a character or two) should be auto-confirmed by
audio fingerprint. Investigate read-only first (dedup page vs review page component
boundaries; current review-queue flow) and present a plan before building — this is
frontend + backend feature work, not a mechanical change.

> **2026-07-19 — item 50 is now folded into a full design spec:**
> [`docs/specs/2026-07-19-fingerprint-driven-reconciliation-design.md`](specs/2026-07-19-fingerprint-driven-reconciliation-design.md)
> (DRAFT) — fingerprint-driven library reconciliation via a 3-signal (fingerprint /
> source-folder ground-truth / Whisper) convergence loop; use-cases = shattered-book
> reassembly, dedup-on-import, iTunes decommission, near-dupe confirm. Verified live:
> 94% fp coverage, the 39-way *Aces Abroad* shatter. Items 51–52 (review UX +
> dedup-page consolidation) remain as scoped below.

50. **Fingerprint-confirmed dedup + shattered-book reassembly against the original
    source** (GROUNDED 2026-07-19 via read-only prod verification). Two related tests,
    added as signals on existing candidates — not a new pipeline:
    - **(a) Acoustic confirm** — where both sides of a candidate pair are fingerprinted,
      use `WholeFileSimilarity` closeness as a *confirming* signal to auto-promote the
      "same file, one extra character" title-leak near-dupes to auto-merge; distinct
      pairs fall back to today's scoring. Per-file acoustic signals already feed scoring
      (`exact_acoustid`/`lsh_acoustid`); this extends them + strengthens the
      `auto_resolve` gate (behind the existing `AutoResolveEnabled` kill-switch).
    - **(b) Shattered-book reassembly** — for a book split into many fragments (author-
      first shards of a multi-author anthology), match the fragments' per-file
      fingerprint **set** against the assembled ORIGINAL source folder (set containment
      `fragments ⊆ source_folder`) via the existing `fpidx` LSH index → the source
      folder whose file-set contains them identifies the true whole book. Metadata
      (album/iTunes-XML/PID/version-group) is the primary regroup key; the fingerprint-
      set match is the safety confirmation that makes the auto-regroup safe.
    - **Design constraints (owner, 2026-07-19):** dedup AGAINST the original source as the
      identity reference, but keep the organized (primary) copy canonical; reflink new
      files on import. **NEVER mutate the active iTunes tree** — read-only at most (see
      [[feedback_itunes_active_library_hands_off]]).
    - **VERIFIED (prod, read-only, 2026-07-19):** file-level raw-fingerprint coverage is
      **94%** (296,010 / 315,013 files; zero-duration count == 0, so the old Seg0
      over-count worry is moot — the "~65%" figure was stale/pair-level, NOT a current
      file-level blocker). **PREREQUISITE / the one real gap:** the assembled source-
      download root is NOT a configured scan path, so its folders are on disk but not in
      the DB (title search for a known source book = 0 hits). **Step 1 = scan + fpcalc-
      fingerprint the source root as a read-only REFERENCE corpus** (cheap — reflinks;
      distinct root from iTunes so the guardrail holds) and index into `fpidx`; only then
      does (b) have ground truth to match against. See
      [[project_dedup_assembled_source_ground_truth]].
    - Cross-ref: `internal/dedup/engine.go`, `internal/dedup/unified/auto_resolve.go`,
      `internal/dedup/split_book_detector.go`, `internal/fingerprint/`,
      `internal/plugins/acoustid/`.
51. **Overhaul the review interface ("make it not suck")** — the review page UX is a
    pain point. Needs a concrete redesign spec: read-only audit of the current review
    page (what it shows today, interaction friction, per-hold actions) → propose
    redesign. Ties to the review-queue track (A1/A2/B1 shipped; B2 apply path merged
    #1953, default OFF — see [[project_review_queue_regroup]]). Prereq for item 52.
52. **Consolidate the dedup page into the review page** — slim the dedup page down to
    run-control only (start/stop dedup runs + run status/progress); move ALL candidate
    and result display + review actions into the review page so there is one place to
    review everything. Depends on item 51 (the review UI must be good enough to absorb
    the dedup results first). Investigate current dedup-page vs review-page component
    boundaries before committing to a plan.

## 2026-07-17 review findings — remaining (post-fix-wave)

The 2026-07-17 multi-discipline review produced 66 findings
([`docs/audits/2026-07-17-multi-discipline-review.md`](docs/audits/2026-07-17-multi-discipline-review.md)).
The same-day fix wave closed most of them across PRs #1972–#1986 — see
[`docs/status/2026-07-17-error-correction-session.md`](docs/status/2026-07-17-error-correction-session.md)
for the PR↔finding map and the sandbox verification results. **Remaining work is
specified as weak-model-proof task briefs T01–T13 in
[`docs/agent-tasks/error-correction-2026-07/TASKS.md`](docs/agent-tasks/error-correction-2026-07/TASKS.md)**
— work from the briefs, tick lines here as they land.

### Fixed (2026-07-17 → 07-18 waves — do not re-fix)

**2026-07-17 wave:** F1 (#1973) · F2 (#1976) · F3/F4/F5/C7 (#1977) ·
title-repair op (#1978) · R-2/C-3/C-2/C-4/C-5/C-1 (#1980) · C1/C6/C4/C5/C3 (#1981) ·
breakdown-backfill op + title-leak relax (#1982) · devops IP-scrub/template/hook/
smoke (#1983) · DL-5/C-6/C-7/M5/M6 (#1984) · R-4/H5/R-5/H6/DL-4/M8 (#1985) ·
DL-1/DL-2/DL-3/M4 (#1986).

**2026-07-18 coordination wave (T05–T12):** R-1 (T06) + R-3/R-7/P-2 (T08) (#2002) ·
devops follow-ups T12 (#2001) · F7/R-9/R-8 (T11) (#2004) · R-6 orphan-VG pool (T07) (#2003) ·
dep-fail SSE publisher (T06-fu) (#2005) · C2/H7 reporter threading (T09) (#2006) ·
F6 legacy book-merge rerouted off hard-delete → soft-delete + external-ID reassignment
+ ITL removal (T10) (#2007) · triage purge-apply op (T03-BUILD) (#2008) ·
H1/H2/H3/H4/H8/H9/M1/M2/M3/M7 logging batch (T05) (#2010).

### Remaining — execution state (briefs)

- [x] **T01** — organizer data-loss fixes landed (#1986)
- [x] **T02** — sandbox triage measured: purgeable **7,878** (title_leak) / genuine 278 /
      fragment 392 / unknown 1,756 of 10,304 (was purgeable=1, unknown=9,950 pre-work —
      the title-repair → breakdown-backfill → relaxed-triage chain is proven). Formal
      doc recording folded into T13.
- [ ] **T03** — sandbox purge wave: `maintenance.dedup-exact-triage {"apply":true}` (dismiss
      ~7,878 purgeable, op merged in #2008) → purge-stale → full-scan → measure vs 9,074
      baseline. Needs sandbox redeploy with current main first. NOT yet run.
- [ ] **T04** — prod deploy (nothing deployed since 2026-07-17) + prod dry-runs + ⚠️ HUMAN-GATED apply
- [x] **T05** — logging H/M batch: H1 H2 H3 H4 H8 H9 M1 M2 M3 M7 (#2010)
- [x] **T06** — R-1: `op.terminal` SSE backend publisher (#2002) + dep-fail publisher (#2005)
- [x] **T07** — R-6: AssignOrphanVGs worker pool + VG clobber guard (#2003)
- [x] **T08** — R-3 (reporter logBuf cap) · R-7 (dead scan-checkpoint deleted) · P-2 (RunItems completion counter) (#2002)
- [x] **T09** — C2 (remux/transcode reporter threading + fail-on-error) · H7 (external-id backfill) (#2006)
- [x] **T10** — F6: legacy book-merge rerouted off hard-delete to soft-delete + external-ID reassignment + ITL removal (#2007)
- [x] **T11** — F7 (quarantine → RunItems) · R-9 (path_repair pool + 3 concurrency hazards) · R-8 (unknown-duration group guard) (#2004)
- [x] **T12** — devops: 8 IP-scrub scripts · op-stall alert (commented; metric TBD, Infra #36) · coverage floor on PR gate · systemd dedupe · credential entropy (#2001)
- [ ] **T13** — docs truth-up with measured sandbox/prod numbers (dedup/STATUS.md, pending-prod-actions.md, exec summary) — in progress
