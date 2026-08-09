<!-- file: todo.d/20260809-library-double-fetch-swallows-clicks.md -->
<!-- version: 2.0.0 -->
<!-- guid: b6e4207f-9c31-4d85-a072-3fe185c9a4b8 -->
<!-- last-edited: 2026-08-09 -->

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
