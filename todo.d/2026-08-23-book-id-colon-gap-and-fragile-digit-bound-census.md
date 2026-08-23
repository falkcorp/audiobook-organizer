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
      at 47 other call sites across 13 files in `internal/database`, none of
      which were touched by #2801 (out of scope for that PR). Census (grep
      `\[\]byte\("[a-z_]*:0"\)` across `internal/database/*.go`, excluding
      test files and excluding the fixed file itself, run 2026-08-23 — exact
      counts, not approximate):

      | File | Occurrences | Prefix(es) |
      |---|---|---|
      | `pebble_store.go` | 20 | `book:` (×20 incl. one bare `lower :=` at L638) |
      | `pebble_store_authors.go` | 4 | `book:` ×2, `author:` ×1, `author_alias:` ×1 |
      | `pebble_store_series.go` | 4 | `book:` ×2, `series:` ×1, `book_file:` ×1 |
      | `pebble_store_stats.go` | 4 | `book:` ×2, `author:` ×1, `series:` ×1 |
      | `pebble_quick_queries.go` | 2 | `book:` ×2 |
      | `pebble_store_importpaths.go` | 2 | `book:` ×1, `import_path:` ×1 |
      | `pebble_store_itunes.go` | 2 | `book:` ×2 |
      | `pebble_store_scancache.go` | 2 | `book:` ×2 |
      | `pebble_store_quarantine.go` | 2 | `book:` ×2 |
      | `pebble_store_works.go` | 2 | `book:` ×1, `work:` ×1 |
      | `pebble_store_bookfiles.go` | 1 | `book:` ×1 |
      | `series_bookref.go` | 1 | `book:` ×1 |
      | `soft_deleted_count.go` | 1 | `book:` ×1 |
      | **Total** | **47** | across 7 distinct prefixes: `book:`, `author:`, `series:`, `author_alias:`, `import_path:`, `book_file:`, `work:` |

      A test helper (`internal/database/store_invariants_test.go`'s
      `mustIter(t, ps, "book:0", "book:;")`) also hardcodes the same literal
      bounds as string args rather than `[]byte`, so it did not match the
      grep above — worth including in whatever sweep picks this up.

      All 47 are latent in the same sense as the version-group backfill was:
      correct today only because every ID minted so far happens to start with
      a digit (ULIDs, or small integer author/series IDs). None are a
      currently observed data-loss bug. Recommend a `/parallel-sweep`-style
      mechanical pass replacing each `[]byte("<prefix>:0")`/`[]byte("<prefix>:;")`
      pair with the true prefix range `[]byte("<prefix>:")`/`[]byte("<prefix>;")`
      (the pattern already used correctly elsewhere in the same files for
      `book_file;`, `metadata_cache;`, `opchange;`, `narrator;`, etc.) — but
      only AFTER (or alongside) fixing gap 1 above, since several of these
      scans have their own structural/type filters downstream that may share
      the same colon-count assumption and would need the same audit
      `VGBACKFILL-BOUNDS-FRAGILE`'s fix got.
