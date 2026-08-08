- [ ] **The e2e failures have MIXED causes — do not plan a single systematic
      fix.** Sampled 2026-08-08 after a fragment filed an hour earlier
      speculated that one data-envelope gap might explain most of the 146.
      **It does not.** Four files sampled, at least three distinct causes:

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
