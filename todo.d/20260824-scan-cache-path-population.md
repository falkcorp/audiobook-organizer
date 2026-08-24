## Scan cache: size and fix the "no book row at this path" population

`writeBackScanCache` (`internal/scanner/scanner.go`) now counts three previously
silent write-back abandonments. One of them, `scanCacheNoRowCount`, is
structural rather than an error, and needs follow-up work.

### What it measures

`saveBookToDatabase` has two early `return nil` paths for a file that duplicates
an already-version-linked book — one in the single-file dedup branch, one in the
multi-file branch. Neither creates a row at the scanned path. With no row,
`GetBookByFilePath` returns nil, so no scan-cache entry is ever written, so
`GetScanCacheMap` (which skips rows with a nil `LastScanMtime`) never sees the
path, so the file is re-read **and re-hashed** on every scan for the life of the
library. It is self-perpetuating and it selects for the files that are most
expensive to process.

### Do NOT conflate this with the 12.8% figure

The 12.8% of books lacking `last_scan_mtime` was sampled from **book rows**.
Files with no row at all are structurally invisible to that measurement. These
are disjoint populations and fixing one will not move the other. The weekly
`force_update` sweep does not cover this one either: a sweep re-writes cache
entries for files that *get* a row, and these never do.

### Tasks

- [ ] Read `scanCacheNoRowCount` off a completed production scan summary to size
      the population. Until that number exists this is unquantified — do not
      assume it is either negligible or large.
- [x] ~~Decide where scan state for a row-less path should live.~~
      **DECIDED 2026-08-24: a path-keyed scan-cache keyspace, independent of
      book rows — built INSIDE the staged pipeline's enumerate/diff phase, not
      as a standalone change.** The user chose the more correct shape and
      sequenced it deliberately: building it now against the current scanner
      would mean building it twice, because the diff phase needs the same
      path-keyed state. Do NOT create a row for the duplicate path (the rejected
      alternative below) — it changes import semantics and risks regrowing the
      dedup backlog.

      The two candidates as they were weighed:
      - a path-keyed scan-cache keyspace, independent of book rows. This is the
        more correct shape (the scanner walks *files*; the cache is keyed by
        *book rows*, and the mismatch is the root cause) and it has a natural
        home in the staged pipeline's enumerate/diff phase rather than as a
        bolt-on. See `docs/superpowers/specs/2026-08-24-staged-library-scan-design.md`.
      - create a row for the duplicate path. Symmetric with the non-linked
        branch, which *does* create a row — but it changes import semantics,
        surfaces files that are currently invisible in the library, and risks
        regrowing the dedup backlog that is being worked separately. Do not do
        this unilaterally.
- [ ] Also check `scanCacheStatErrCount` and `scanCacheLookupErrCount` on the
      same run. A non-trivial lookup-error count means a store problem that was
      invisible before 2026-08-24 and is a different bug.
- [ ] Note when sizing: version-linking is *a* cause of a row-less path, not
      *the* cause. There is at least a third early `return nil` with the same
      effect — the blocked-hash skip in `saveBookToDatabase`. Any estimate that
      attributes the whole `scanCacheNoRowCount` figure to duplicate files will
      be wrong.
