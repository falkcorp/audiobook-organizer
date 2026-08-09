<!-- file: TODO.md -->
<!-- version: 10.20.0 -->
<!-- guid: 8e7d5d79-394f-4c91-9c7c-fc4a3a4e84d2 -->
<!-- last-edited: 2026-08-09 -->

# Project TODO — live items only

## 📥 Inbox

Tasks assembled from `todo.d/` fragments. Add a new task by dropping a fragment
file in `todo.d/` rather than editing this section by hand — see
[`todo.d/README.md`](todo.d/README.md). Checking a task off, or promoting it
into one of the curated sections below, is a normal direct edit.

<!-- todo-insert-here -->

- [ ] **Nothing runs the e2e suite automatically — wire it into CI.** Found
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
      Sub-item 3 (flip `continue-on-error` off) remains — broken out below.

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

<!-- file: todo.d/20260807_194500_deluge_must_not_write_into_import_dir.md -->
<!-- version: 1.0.0 -->
<!-- guid: c9e4b7d2-51a8-4f36-b0e7-2d84a1f6c093 -->
<!-- last-edited: 2026-08-07 -->

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

<!-- file: todo.d/20260807_203000_all_ops_must_support_dry_run.md -->
<!-- version: 1.0.0 -->
<!-- guid: a7f28c15-9364-4e0b-b8d2-46c1e0937fa5 -->
<!-- last-edited: 2026-08-07 -->

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

<!-- file: todo.d/20260807_204800_u1_daytime_pool_benchmark.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b3a9e17-2c64-4f80-b1d9-7e05c8a3f264 -->
<!-- last-edited: 2026-08-07 -->

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

<!-- file: todo.d/20260806_093000_bracketed_series_are_shattered_books.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9d3f621c-47ea-4b05-8c93-2f1a7de04b58 -->
<!-- last-edited: 2026-08-06 -->

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

<!-- file: todo.d/20260806_093100_series_denumber_low_tier_consumer.md -->
<!-- version: 1.0.0 -->
<!-- guid: c1e84b76-3d29-4f05-a917-6b2508fd3e14 -->
<!-- last-edited: 2026-08-06 -->

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

<!-- file: todo.d/20260806_150000_react_router_v8_residual_advisory.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4d81e9c3-7a26-4f50-b83d-19e6c07af241 -->
<!-- last-edited: 2026-08-06 -->

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

<!-- file: todo.d/20260806_150100_frontend_open_redirect_invariants.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9e34a7b1-05df-4c82-a6e9-3b7150d2f8ce -->
<!-- last-edited: 2026-08-06 -->

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

<!-- file: todo.d/20260806_150200_e2e_suite_broken_on_main.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2c7f480a-6b13-49de-95a1-8e4d3b6f0721 -->
<!-- last-edited: 2026-08-06 -->

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

<!-- file: todo.d/20260806_150300_memdb_write_path_followups.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7b09fd52-3e84-4c17-a1b6-5f2d80e94c63 -->
<!-- last-edited: 2026-08-06 -->

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

<!-- file: todo.d/20260806_150400_multidisc_holds_are_duplicates.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5a1e83c7-42b9-4dc6-8e07-6c39f2a1b5d8 -->
<!-- last-edited: 2026-08-06 -->

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

<!-- file: todo.d/20260806_153000_frontend_framework_versions.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c14e620-9b7d-4a35-b0f2-73de5a91c4e7 -->
<!-- last-edited: 2026-08-06 -->

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

<!-- file: todo.d/20260806_220000_per_file_intro_identity_signal.md -->
<!-- version: 1.1.0 -->
<!-- guid: 3a7c2e94-5b18-4d60-9f27-c8140b6e3d52 -->
<!-- last-edited: 2026-08-07 -->

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

<!-- file: todo.d/20260806_220100_whisper_second_worker_u1.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f31d4b8-92ae-4c07-b5f1-08e3a7d26c94 -->
<!-- last-edited: 2026-08-06 -->

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

<!-- file: todo.d/20260807_012000_transcribe_status_content_drift.md -->
<!-- version: 1.0.0 -->
<!-- guid: b471e5c9-2f68-4a03-95d1-0e37c8b2a6d4 -->
<!-- last-edited: 2026-08-07 -->

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

<!-- file: todo.d/20260807_020500_memdb_warmup_caller_pointer_race.md -->
<!-- version: 1.0.0 -->
<!-- guid: d3f81a56-9c47-4e20-b7d8-52069fe1c4a3 -->
<!-- last-edited: 2026-08-07 -->

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

<!-- file: todo.d/20260805_213000_version_group_acoustic_audit.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9f26a740-5c83-4b1e-a207-e5348d19cb6f -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260805_214000_chapters_served_to_clients.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1b7d02c4-9e35-4a68-83f1-6d0947ac2e15 -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260805_214100_chapters_backfill_from_duplicates.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5c9e13ab-70d2-4f86-b451-2a86e0f37d94 -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260805_214200_playlists_full_support.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8f31a05d-4c72-4e19-9b06-3d5827ea16bc -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260805_214300_reading_review_status_sync.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3a68d1f9-2b54-4c07-86e3-91f4c05db27a -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260805_214400_deluge_metadata_source.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6d2f84b0-31ac-4e75-92f8-08b7139ce5a3 -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260805_214500_deluge_file_parts_grouping_check.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0e5b3792-a641-4d38-bc09-27f4e816a0df -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260805_220000_review_queue_recommendations_and_overrides.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e91b7c2-06d8-4a35-9f17-b3820e5cd641 -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260805_220100_multidisc_apply_canary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c3d580a-92e4-4b16-8f05-1d47a209e3bf -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260805_220200_series_names_that_are_book_numbers.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2f57e91b-8c04-4d73-a6e8-95b013fc287d -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260805_220300_first_aid_library_validate_repair.md -->
<!-- version: 1.0.0 -->
<!-- guid: a8140e6f-5b27-4c93-81da-7f2e0693b5ca -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260805_220400_metadata_results_cold_start.md -->
<!-- version: 1.0.0 -->
<!-- guid: d3690b58-1e7a-4f24-a905-62c8f7bd031e -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260805_220500_relink_unlinked_books.md -->
<!-- version: 1.0.0 -->
<!-- guid: b52c7e04-a319-4d86-90f7-8e14036b2a97 -->
<!-- last-edited: 2026-08-05 -->

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

<!-- file: todo.d/20260806_001500_version_group_index_underreports.md -->
<!-- version: 1.0.0 -->
<!-- guid: f1a7d520-9c34-4e86-b0d2-73e5814cb96f -->
<!-- last-edited: 2026-08-06 -->

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

  **Also needs an invariant test**: after linking N books into a group and
  setting one primary, exactly one member must have `IsPrimaryVersion == true`.

  Related: [[version-group-acoustic-audit]] (which will read group membership and
  would inherit this under-reporting), [[first-aid-library-validate-repair]].

<!-- file: todo.d/2026-08-04-dedupe-op-45s-per-book.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8333f42a-bd0d-4a2f-8221-403d11576e7c -->
<!-- last-edited: 2026-08-04 -->

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

<!-- file: todo.d/2026-08-04-recompute-aggregates-stale-memdb.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4a29d7e1-83b6-4c50-9f27-1e08b5c3a64d -->
<!-- last-edited: 2026-08-04 -->

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

- [x] ~~**Restore the duration on `The Trapped Mind Project`**~~ **RETRACTED
      2026-08-04 — nothing to restore.** The original claim here was that the
      canary kept a fingerprinted row whose `Duration` was 0 and deleted the 129
      twins holding the real value. Probing the audio disproves it: the book's
      entire content is a 13.5-second, 91,958-byte MP3, and the surviving row
      (`file_size=91958`, `duration=13`) matches it exactly. 0.00h is simply what
      13 seconds looks like. The op behaved correctly; the error was reading a
      rounded display value as evidence of loss without checking the file.

<!-- file: todo.d/2026-08-03-flaky-apply-pid-repair-same-file.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6f10b7e4-9c25-4d83-a0f6-14b7e29d3c05 -->
<!-- last-edited: 2026-08-03 -->

- [ ] **Flaky: `TestApplyPIDRepairSameFile`** (`internal/itunes`) failed
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

<!-- file: todo.d/2026-08-03-flaky-backfill-syncids-race-sanity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2e58c9a1-7b34-4f60-a812-3d90f6c47b25 -->
<!-- last-edited: 2026-08-03 -->

- [ ] **Flaky: `TestBackfillSyncIDsJob_ConcurrentRaceSanity`** (`internal/maintenance/jobs`)
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

<!-- file: todo.d/2026-08-02-bookfile-duplication-and-duration-units.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9b41c7e2-05d8-4a63-b7f0-3e26c8149ad5 -->
<!-- last-edited: 2026-08-02 -->

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

<!-- file: todo.d/2026-08-01-assignorphanvgs-offset-pagination.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c40f2e7-6b19-4d83-a05c-71fe9b3d5a42 -->
<!-- last-edited: 2026-08-01 -->

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

<!-- file: todo.d/2026-08-01-metrics-auth-deploy-gate.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9b3cf479-67b4-4532-a76c-d2a8b5fd4b94 -->
<!-- last-edited: 2026-08-01 -->

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

<!-- file: todo.d/2026-08-01-oauth-login-deeplink-return.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9a4f1e58-2d70-4b93-8c16-e05d7b3a92c1 -->
<!-- last-edited: 2026-08-01 -->

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

<!-- file: todo.d/2026-08-02-abs-cover-art-coverage.md -->
<!-- version: 1.0.0 -->
<!-- guid: e2c81f76-4b90-4d35-a617-9f0c53b8e2a4 -->
<!-- last-edited: 2026-08-02 -->

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

<!-- file: todo.d/2026-08-02-abs-play-counts-listening-history.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3a91c705-8de2-4f46-b0c3-1d75e29f4b83 -->
<!-- last-edited: 2026-08-02 -->

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

<!-- file: todo.d/2026-08-02-abs-progress-mutation-endpoints.md -->
<!-- version: 1.0.0 -->
<!-- guid: b57d2409-8e13-4c6a-9f25-30ab8e17c4d2 -->
<!-- last-edited: 2026-08-02 -->

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

<!-- file: todo.d/2026-08-02-chapters-never-backfilled.md -->
<!-- version: 1.0.0 -->
<!-- guid: c8d0451f-72a9-4e63-b514-9f3e6a07c2d8 -->
<!-- last-edited: 2026-08-02 -->

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

<!-- file: todo.d/2026-07-31-abs-mode-b-nonidentity-assertion.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1a7c4e92-5d38-4b60-9f21-8c3e6a0b7d45 -->
<!-- last-edited: 2026-07-31 -->

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

<!-- file: todo.d/2026-07-31-ios-sso-edge-config-drift.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8c5f1a37-9b24-4e08-a761-2d0e6b8c4f19 -->
<!-- last-edited: 2026-07-31 -->

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

<!-- file: todo.d/2026-07-31-origin-security-hardening.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6e2b8d05-3f41-4a97-8c50-7d1a9b4e2c68 -->
<!-- last-edited: 2026-07-31 -->

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

<!-- file: todo.d/2026-08-01-origin-lan-exposure-finding.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4f1a8c73-52be-4d09-9a67-e3b05c8d217f -->
<!-- last-edited: 2026-08-01 -->

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

<!-- file: todo.d/abs-sync-auth-core-followups.md -->
<!-- version: 1.0.0 -->
<!-- guid: a8c0a4eb-d71c-43ae-9a5a-c0d59bb61bc1 -->
<!-- last-edited: 2026-07-30 -->

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

<!-- file: todo.d/abs-sync-drm-consolidation.md -->
<!-- version: 1.0.0 -->
<!-- guid: af93e202-2439-4b45-aade-7e2c309ee62f -->
<!-- last-edited: 2026-07-30 -->

- [ ] **ABS-SYNC: consolidate the two DRM detection paths, and wire one into the
  scanner.** PR #2067 adds extension-based `DetectDRM` in `internal/audioutil/drm.go`,
  but `internal/diagnosis/probe.go` already has an unrelated, richer mediainfo-based
  probe (`HasActiveDRM`). Two DRM code paths will drift. Decide which is authoritative,
  then wire it into the scanner so Audible AAX/AAXC files surface as
  **unplayable-with-reason** instead of importing and failing at play time. Note the live
  bug this fixes: `.aax`/`.aaxc` are **already** in the default `SupportedExtensions`
  (`internal/config/config.go` ~:2016) with zero DRM awareness. Caution: ffmpeg's `aax`
  demuxer is **CRIWARE game audio, not Audible** — do not key detection off it.

<!-- file: todo.d/abs-sync-identity-gap.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7ed6a106-3ea2-4798-a979-33f0360e0d3a -->
<!-- last-edited: 2026-07-30 -->

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

<!-- file: todo.d/abs-sync-remaining-phases.md -->
<!-- version: 1.0.0 -->
<!-- guid: 95b9132b-ca92-432a-8629-7d98ef59a38b -->
<!-- last-edited: 2026-07-30 -->

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

<!-- file: todo.d/itunes-2way-p0-cleanup-census.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8e2b5a41-6c93-4d07-9f18-3a1c7e6b0d52 -->
<!-- last-edited: 2026-07-24 -->

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

<!-- file: todo.d/itunes-2way-p2-sync-cycle.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7f2c9a81-6b54-4d60-9e18-3c7b5a0e1d72 -->
<!-- last-edited: 2026-07-24 -->

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

<!-- file: todo.d/itunes-isaudiobookitl-underclassifies.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7c3a9e51-4b62-4d08-8f19-2a6c1b7e0d43 -->
<!-- last-edited: 2026-07-24 -->

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

<!-- file: todo.d/itunes-location-form-guard-blocks-ao-library.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f9c1e08-7b52-4d64-a1f8-2c6b5a0e9d47 -->
<!-- last-edited: 2026-07-24 -->

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

<!-- file: todo.d/itunes-2way-sync-continuation.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2165368b-70dd-48b0-b2d3-7288bbea666f -->
<!-- last-edited: 2026-07-23 -->

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

<!-- file: todo.d/itunes-pid-uniqueness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9a2c4e07-1b63-4d85-8f20-5c7e3a1b0d49 -->
<!-- last-edited: 2026-07-23 -->

- [ ] **iTunes book_file PID uniqueness — apply the backfill repair (gated).** Forward
  invariant shipped (`CreateBookFile` transfers a duplicate PID to the new row) + census
  (`GET /itunes/pid-integrity`) + repair (`POST /itunes/pid-repair`) endpoints built and
  tested. Prod census: 8,987 duplicate PIDs (8,762 same-file, 225 diff-file, 94 on multiple
  primaries). Repair dry-run auto-resolves 8,984 (3 ambiguous left for review), clears 9,050
  redundant PID copies, DB-field-only (no file/row deletion). REMAINING: deploy → run
  `pid-repair?dry_run=true` on prod to confirm → owner runs the apply with `!` → re-run the
  census to confirm 0 duplicates → review the 3 ambiguous groups by hand. See
  `docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md` §1.5b.

<!-- file: todo.d/itunes-2way-sync-writeback.md -->
<!-- version: 0.1.0 -->
<!-- guid: 7b1c9e34-2a5d-4f81-9c0e-3d6a1f8b2e07 -->
<!-- last-edited: 2026-07-22 -->

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
