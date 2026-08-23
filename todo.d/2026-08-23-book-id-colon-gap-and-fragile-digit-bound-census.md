- [ ] **PEBBLE-KEY-BOUND-CENSUS** Two related gaps surfaced while fixing
      `VGBACKFILL-BOUNDS-FRAGILE` (#2801):

      **1. Colon-count gap in the version-group backfill's structural filter
      (pre-existing, NOT introduced by #2801).** `BackfillVersionGroupIndex`'s
      loop filter in `internal/database/pebble_store_versiongroup_backfill.go`
      requires `strings.Count(key, ":") != 1` to skip a row — i.e. it assumes
      every book ID contains zero colons, so a primary row is always exactly
      one colon (`book:<id>`). `CreateBook` only mints a ULID
      `if book.ID == ""` (`internal/database/pebble_store.go:2083`), so a
      caller-supplied ID is accepted verbatim and could contain a colon (e.g.
      `book:my:id`, two colons) — such a row is silently skipped by the
      backfill with no error, same as before #2801. #2801 widened the
      iterator's byte-range bounds (fixing the "ID doesn't start with a
      digit" failure mode) but does not touch this separate "ID contains a
      colon" failure mode; both are instances of the same underlying issue —
      book IDs are assumed to be colon-free ULIDs at every consumer, but
      that's never enforced at the one place IDs are created.

      **Recommended fix (root cause, not a patch-each-site fix):** enforce
      "no colon in a book ID" at `CreateBook` itself — reject or normalize a
      caller-supplied `book.ID` containing `:` — rather than assuming the
      invariant holds at every scan/filter site that reads `book:` keys. This
      is the only version of the fix that scales; patching individual
      `strings.Count`/prefix-filter call sites one at a time does not, per
      gap 2 below.

      **2. The exact same fragile `<prefix>:0`..`<prefix>:;` byte-range
      iterator-bound idiom (digit-only lower bound, `;` upper bound scoped to
      the same colon) that #2801 fixed for the version-group backfill exists
      at other call sites across `internal/database`, none of which were
      touched by #2801 (out of scope for that PR). **Two independent regexes
      gave two different counts, and neither is an AST-level census — both
      are lower bounds, not exact totals:**

      - `git grep -nE '\[\]byte\("[a-z_]+:0"\)' -- 'internal/database/*.go'`
        (anchors on the `[]byte`-wrapped lower bound), run against
        `origin/main`: **48** hits, all in non-test files, across 14 files
        (`pebble_store.go` 20; `pebble_store_authors.go`,
        `pebble_store_series.go`, `pebble_store_stats.go` 4 each;
        `pebble_quick_queries.go`, `pebble_store_importpaths.go`,
        `pebble_store_itunes.go`, `pebble_store_scancache.go`,
        `pebble_store_quarantine.go`, `pebble_store_works.go` 2 each;
        `pebble_store_bookfiles.go`, `series_bookref.go`,
        `soft_deleted_count.go`, `pebble_store_versiongroup_backfill.go`
        1 each — the last one is the site #2801 fixed, so 47 remain
        unfixed).
      - `git grep -nE '"[a-z_]+:;"' -- 'internal/database/*.go'` (anchors on
        the bare-string upper bound instead), run against `origin/main`:
        **50** hits — 49 in non-test files, plus 1 in
        `store_invariants_test.go`'s `mustIter(t, ps, "book:0", "book:;")`,
        which the `[]byte`-wrapped regex above misses entirely because that
        helper takes plain strings, not `[]byte`.

      The two disagree because they anchor on different halves of the pair
      (lower-bound `[]byte(...)` form vs. upper-bound bare-string form), not
      because one is right and the other wrong — and both regexes miss any
      bound built by concatenation or `fmt.Sprintf` rather than a single
      string literal (see `pebble_activity_store.go`'s
      `[]byte("act:" + tier + ";")` for an example of that shape elsewhere in
      the package, itself already correct, but a template for how a fragile
      one could hide from grep too). **Re-run both before sizing a sweep, and
      expect the true count to be somewhat higher than either.**

      All of these are latent in the same sense as the version-group backfill
      was: correct today only because every ID minted so far happens to start
      with a digit (ULIDs, or small integer author/series IDs). None are a
      currently observed data-loss bug. Recommend a `/parallel-sweep`-style
      mechanical pass replacing each `[]byte("<prefix>:0")`/`[]byte("<prefix>:;")`
      pair with the true prefix range `[]byte("<prefix>:")`/`[]byte("<prefix>;")`
      (the pattern already used correctly elsewhere in the same files for
      `book_file;`, `metadata_cache;`, `opchange;`, `narrator;`, etc.) — but
      only AFTER (or alongside) fixing gap 1 above, since several of these
      scans have their own structural/type filters downstream that may share
      the same colon-count assumption and would need the same audit
      `VGBACKFILL-BOUNDS-FRAGILE`'s fix got. Fixing gap 1 at `CreateBook` may
      make much of this sweep unnecessary — do that first and re-measure
      blast radius before committing to a 47/50/N-site mechanical sweep.

      ---

      **MEASURED 2026-08-23 (the census finding 8.3 of the #2787 review asked
      for, half-answered).** Finding 8.3 said "do not fix it without
      measuring." Every book ID reachable through the API was enumerated and
      its first byte after `book:` checked against the `'0'`–`'9'` lower
      bound:

      | population | endpoint | ids | leading byte | outside `book:0`..`book:;` |
      |---|---|---:|---|---:|
      | live | `/api/v1/audiobooks` (`show_quarantined=true`) | 56,727 | `'0'` ×56,727 | **0** |
      | soft-deleted | `/api/v1/audiobooks/soft-deleted` | 16,124 | `'0'` ×16,124 | **0** |
      | **total** | | **72,851** | 100% `'0'` | **0** |

      All 72,851 are exactly 26 characters — canonical ULIDs, with no
      caller-supplied UUID or other format anywhere in the live keyspace. The
      digit-only lower bound therefore holds today with a full byte of margin
      (`'0'` vs. the `'9'` ceiling; ULIDs do not reach a leading `'1'` until
      ~2065).

      **This measurement does NOT close gap 1, and only partly closes gap 2:**

      - It measures **IDs**, not **keys**. A row whose value is corrupt enough
        that neither listing decodes it is invisible to this instrument by
        construction — and that population is exactly what the memdb
        known-incomplete work (#2794/#2787) exists to handle. A true answer
        needs a raw Pebble prefix scan on the server, which the API cannot
        express.
      - It says nothing about **colons inside an ID** (gap 1). Both listings
        return IDs as JSON strings; none contained a colon, but `CreateBook`
        still accepts a caller-supplied ID verbatim, so the invariant remains
        unenforced at the one place it could be.
      - Non-book prefixes (`author:`, `series:`, `work:` …) were **not**
        measured. The 47–50 other sites use the same idiom over different
        keyspaces with different ID generators.

      **What this changes about the recommendation:** the sweep is now
      confirmed *not* urgent — there is no live row outside the bound, so this
      is latent, not active. Fix gap 1 at `CreateBook` first as the fragment
      already recommends; that makes the invariant true by construction rather
      than true by luck, and re-measuring after it lands is cheap.
