### Fixed

- **A reflink-only `recover-missing-files` run no longer stands the library scanner
  down — so it can recover files concurrently with a running scan.** The scan
  stand-down (PR #3080) exists to keep a scan from clobbering a write op's **DB-row
  rewrites** (the Branch A in-tree repoint, plus `missing-file-repoint` /
  `mark-missing-files`). A `reflinkOutside`-only run does **no** DB write: it clones
  each missing row's source bytes back to that row's own already-dead `FilePath` and
  never repoints, creating files only at non-existent paths with refuse-on-exist. It
  cannot clobber a concurrent scan, so it now skips the acquire entirely (gated on
  `len(rewrites) > 0`) and runs with no interlock. Runs that do rewrite DB rows still
  acquire the stand-down exactly as before. This also unblocks recovery against a
  scan the stand-down cannot currently quiesce (a scan resumed after a restart, whose
  work runs under a resume execution handle the gate does not target — tracked
  separately).
