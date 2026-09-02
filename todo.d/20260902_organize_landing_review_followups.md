- [ ] **Organize landing follow-ups accepted as non-blocking in the #3051 review** —
  none is a data-loss path; each is a hardening the reviewers agreed can land separately:
  - **Stranded-temp identity is size only.** `internal/organizer/pipeline.go`
    `strandedTempMismatch` adopts a parked `.tmp-rename` temp when its size matches the
    row's `FileSize`; `BookFile.FileHash` is on the row `planPass` reads but is not
    carried into `FileRenameEntry`. The codebase's own `adoptExistingDestination` standard
    says equal size is not identity. Before adding it, confirm which hash field tracks
    on-disk bytes after write-back (tags change the file; a stale hash would refuse every
    resume).
  - **Nonce-named orphan beside a PRESENT source** (`pipeline.go` phase-0 check looks at
    the legacy `<target>.tmp-rename` name only) is proceeded past; it surfaces later only
    through the other entry's source-missing path. Extend the refusal to "any stranded
    temp for this target while its source is present".
  - **`unlinkCreated` can remove an EMPTY target directory another worker's `MkdirAll`
    just created** (`organizer.go`), so that worker's exclusive copy fails ENOENT, takes
    the fatal branch, and the book fails with "failed to write temp file". Transient and
    retry-safe; either retry the `MkdirAll`+open once on ENOENT or skip the directory
    removal when the rollback removed nothing.
  - **`linkMoveExclusive` post-link verification failure** (`saferename.go`) leaves dst
    published but returns an error, so dst is not in `Landing.Created` and the rollback
    cannot see it — an orphan under RootDir. Rare (EIO between `link` and `Lstat`); add
    dst to the rollback set before verifying, or unlink it on verification failure.
  - **`metafetch/service.go` `RunApplyPipelineRenameOnly` reads `GetBookFiles` twice**
    and discards `bf.Missing`; a scan adding a row between the two reads could yield a
    version row outside RootDir, and the Warn for unlanded rows conflates expected
    Missing rows with unexpected ones. Read once, pass the same slice to both uses,
    and split the log by `Missing`.
