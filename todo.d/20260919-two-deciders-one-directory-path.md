- [ ] **Find what put TWO deciders on one directory path within a single scan run.**
      Prod holds eight book rows at one iTunes shelf path, minted in two bursts
      within one run each (3 rows in 2m07s on 2026-08-16, 5 rows in 2m56s on
      2026-08-17). The check-then-create gap in `saveBookToDatabase` is closed
      (FilePath-keyed stripe, `internal/scanner/version_link.go` `lockBookPath`),
      which is what turned two deciders into two rows — but what produced two
      deciders is NOT confirmed in source. Ruled out by reading: the
      per-directory scan pool in `scanner.go` dispatches over a `dirs` slice
      whose entries are distinct, and `groupFilesIntoBooks` emits at most one
      directory book per directory, so one pass over one directory is one
      decision. Candidates not yet checked: `registerDirectory` admitting both a
      real directory and a symlink that resolves to it (both land in the same
      `dirs` slice); two overlapping scan runs in one process (a boot resume
      sweep reviving an interrupted scan alongside a fresh one); two configured
      scan roots that contain each other. The stripe is process-local, so the
      first two are covered by it and the third would be too; a second PROCESS
      is not. Worth settling before any data repair of the 2,409 directory paths
      that carry more than one book row.
