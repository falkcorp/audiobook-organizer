- [ ] **Fix the Library "In Progress" filter — the selection highlight does not
      move, and the filter appears to do nothing.** Reported 2026-08-08. From
      the Library view with "All Books" active, clicking "In Progress":

      1. **The active-selection highlight stays on "All Books."** The clicked
         option never becomes visually selected, so there is no feedback that
         anything was chosen.
      2. **No filtering appears to happen and no filter chip is added** — the
         same books remain listed. The reporter flagged this as needing
         verification; the backend half is now verified below.

      **Backend evidence (prod, 2026-08-08)** — `GET /api/v1/audiobooks`:

        limit=1                              count=44874   <- baseline
        limit=1&library_state=imported       count=18998   <- honoured
        limit=1&library_state=in_progress    count=0       <- honoured, invalid value
        limit=1&status=in_progress           count=44874   <- silently IGNORED
        limit=1&progress=in_progress         count=44874   <- silently IGNORED
        limit=1&bogus_param_xyz=nonsense     count=44874   <- silently IGNORED

      So there are two distinct ways this button can be dead, and both are
      silent. If the client sends an unrecognised param name, the server returns
      the **entire unfiltered library with HTTP 200** — indistinguishable from
      no filter at all, which matches the reported symptom exactly. If instead
      the client sends `library_state=in_progress`, the server recognises the
      param but not the value and returns **zero books**. Neither case produces
      an error the UI could surface.

      **Investigate in this order:**

      - Confirm what the click handler actually sends (devtools Network tab on
        the click) — no param, an ignored param name, or `library_state=
        in_progress`. That single observation splits the two failure modes.
      - The highlight-not-moving symptom is characteristic of an uncontrolled
        component or a value mismatch: the option's `value` not matching what
        state holds (e.g. `"in_progress"` vs `"inProgress"` vs `"In Progress"`),
        so `selected`/`aria-selected` never evaluates true even though state
        changed. Check that the selector is controlled and that the comparison
        is against the same casing/spelling the options declare.
      - Establish what "In Progress" is even meant to mean — partially
        transcribed? partially imported? mid-organize? It may be that no backend
        field expresses it yet, in which case the honest fix is to remove the
        option until the backend supports it rather than ship a button that
        cannot work.

      **Do not fix this only in the client.** Whatever "In Progress" means, the
      count must be computed server-side over the whole library, not by
      filtering a fetched page — see the companion backend-filtering task. The
      root enabler of this bug class is that the server fails open on unknown
      filters; fixing that (400 instead of full library) would have made this
      button fail loudly on day one instead of looking merely useless.

      **Acceptance:** clicking the option visibly moves the selection highlight,
      adds a filter chip, changes the result count, and the count reflects the
      whole library rather than the current page.
