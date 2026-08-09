<!-- file: todo.d/20260809-library-double-fetch-swallows-clicks.md -->
<!-- version: 1.1.0 -->
<!-- guid: b6e4207f-9c31-4d85-a072-3fe185c9a4b8 -->
<!-- last-edited: 2026-08-09 -->

- [ ] **The Library sometimes fetches page 1 twice on mount, and when it does, the very
      next click is swallowed.** Found 2026-08-09 while chasing three flaky
      `library-browser.spec.ts` pagination tests on webkit. This is a product defect, not
      test rot, so the tests were left alone per the no-papering-over rule — they are
      honestly flaky because the app is.

      **Evidence.** A probe that records every `/api/v1/audiobooks` request, loads
      `/library`, waits for `networkidle`, clicks "next page", then samples for 3s.
      Three runs, and the third diverges:

      | run | requests before click | after click |
      |---|---|---|
      | 1 | `offset=0` | URL → `?page=2`, `offset=20` fetched, page-1 book gone ✅ |
      | 2 | `offset=0` | URL → `?page=2`, `offset=20` fetched, page-1 book gone ✅ |
      | 3 | `offset=0`, **`offset=0` again** | URL stays `?page=1`, **no request**, page-1 book still shown ❌ |

      The click is not slow — it is **gone**. Nothing happens at 200 ms, 600 ms, 1.5 s or
      3 s. The distinguishing feature of the failing run is the **duplicate initial
      fetch**: two identical `offset=0` requests instead of one.

      **CORRECTION 2026-08-09 (later the same day).** The causal claim below —
      that the duplicate fetch causes the swallowed click — is **wrong**, or at
      least incomplete. The duplicate was found and eliminated (see the fix note
      at the bottom); measured like-for-like over 24 webkit runs of the same
      three tests, the failure rate went **16/24 → 11/24**. A real improvement,
      but the flake survives, so something else is also at work. The remaining
      failures have also shifted shape: `navigates to previous page` now fails
      with "Test Book 1 **not found**" after going next-then-previous, i.e.
      page 1 does not come back — the inverse of the original symptom. Whoever
      picks this up should treat the two as separate defects.

      **Two problems, and the first was wrongly assumed to cause the second:**

      1. **A wasted duplicate query on mount.** On a large library that is a second full
         page query for nothing. It belongs with the other client-side over-fetching
         already filed in `todo.d/20260809-search-drops-filters-and-debounce.md` — that
         one is ten queries per search, this is two per page load.
      2. **The re-render from that second response detaches the pagination button
         mid-click**, so the handler never runs and the interaction is silently lost. A
         user would see this as "I clicked next page and nothing happened."

      **Why it is webkit-flaky rather than constant:** the duplicate only sometimes lands
      in the window where the user (or the test) is clicking. Chromium's timing usually
      misses it. It is not a webkit bug — webkit just widens the window.

      **Suspected cause,** same neighbourhood as
      `todo.d/20260809-library-url-transient-filter-drop.md`: the paired effects in
      `web/src/pages/Library.tsx` (~lines 596-644) that read `searchParams` into state and
      write state back to `searchParams`. A mount that writes the URL once and reads it
      back can re-run `loadAudiobooks` with identical arguments. `useLibraryQuery` has a
      results cache, but the second call is issued before the first resolves, so the cache
      cannot dedupe it — an in-flight map keyed by the request signature would.

      **Reproduce:**

      ```bash
      cd web && npx playwright test -c tests/e2e/playwright.config.ts --project=webkit \
        tests/e2e/library-browser.spec.ts -g "navigates to (next|previous) page" \
        --repeat-each=3 --workers=1
      ```

      Observed 5 failures across 3 repeats of the file (`navigates to previous page` 2/3,
      `navigates to next page` 2/3, `jumps to specific page` 1/3).

      **Acceptance:** one `offset=0` request per Library mount, and the three pagination
      tests pass 8/8 on webkit with `--repeat-each=8`.

      **Half of that is now met.** The duplicate mount fetch is fixed: `SearchBar`
      re-parses its value on mount and hands back a NEW `ParsedSearch` object that is
      semantically identical to the one `Library.tsx` seeded its state with. Storing it
      changed `parsedSearch`'s identity → recreated `buildFieldFilters` → recreated
      `loadAudiobooks` → re-fired the "load when filters change" effect. Confirmed by
      instrumenting the dependency array: `DEPCHANGE buildFieldFilters,parsedSearch`
      fired once after mount on 4 runs of 4, and stopped firing entirely once the setter
      bailed on an unchanged value. `/api/v1/audiobooks` is now hit exactly once per
      mount, 4 runs of 4.

      **The pagination flake is still open** and needs its own investigation — start from
      the shifted symptom above rather than from the duplicate fetch, which is no longer
      present.
