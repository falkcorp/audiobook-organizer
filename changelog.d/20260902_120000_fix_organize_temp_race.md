### Fixed

#### Organizing two books into the same destination can no longer corrupt, replace, or misattribute either file

Two books that resolve to the same organized path (same author + title — the common
shape of an unmerged duplicate) and are organized at the same time, which the parallel
organize op does routinely, could end up with one book's row pointing at the other
book's audio, a mixed file under a correct name and size, or the first arrival silently
replaced by the second. Five separate holes, all closed in one pass (#3046):

- **Shared temp file in `copyFile`.** Every writer used `<dest>.tmp` with truncate, so
  two concurrent copies interleaved into one temp. A 30-iteration probe corrupted the
  destination 30/30. Each copy now writes a per-call `O_EXCL` temp
  (`<dest>.<nonce>.tmp`); a temp-name collision is an ordinary error, deliberately NOT
  `fs.ErrExist`, because that error means "destination taken" and triggers adoption.
- **Blind adoption on `EEXIST`.** The loser recorded the other book's file as its own.
  It adopts only a proven same-inode or same-content file; otherwise it leaves the row
  alone and logs both paths.
- **Rows followed a recomputed plan, not what landed.** `CreateOrganizedVersion`
  re-derived every target and adopted whatever file sat there. It now takes a
  `Landing` — the target, the source→organized map of files that actually landed, the
  files this run *created*, and the present files that did not land — and points rows
  only at `Landing.Files`. An unlanded row keeps its source path even when a foreign
  file occupies its planned target. A single-file landing for a multi-row book fails
  closed instead of stamping every row with one path.
- **Rollback deleted a whole directory.** On a failed record write the rollback did
  `os.RemoveAll(targetDir)` — including another book's files sharing that directory,
  and an earlier organized copy this run had only adopted. It now removes
  `Landing.Created` only (contained under RootDir), then the directory if that emptied
  it.
- **`rename(2)` on the move path replaces silently.** `RenameFiles` (two-phase
  rename) and `ReOrganizeInPlace` both checked-then-renamed; two workers passed the
  check and the second replaced the first. `RenameFiles` now parks each file on a
  per-call unique temp and publishes with `finalizeExclusive` (`link(2)` + unlink —
  atomic, `EEXIST` on an occupant, verified by size); `ReOrganizeInPlace` uses
  `moveExclusive`. Filesystems that refuse hard links (`EPERM`/`ENOTSUP`/`EOPNOTSUPP`/
  `EXDEV`/`EMLINK`/`ENOSYS`) fall back to the previous rename path with a warning.
  Stranded temps of either name generation are resumed; two stranded temps for one
  target refuse rather than guess. Glob metacharacters in library paths are escaped.

One decision, not three. `OrganizeOneBook` is now the only place that decides in-root
→ `ReOrganizeInPlace`, `>1 book_file rows` or a directory → directory path, else
single file. The HTTP handler and the batch-save op each had a drifted copy (the handler
used `os.Stat` only, so a consolidated book whose `FilePath` is its first chapter went
down the single-file path); both are deleted and the folder-autoscan op uses it too.

Tests plant the other book's bytes at the contested path before asserting, and check
that both files are readable afterwards, not just that one call errored. Mutation table
`scripts/mutation-tables/organize-landing.muts` (12 mutants, results in the PR body; #3051 adds four more).
Three "no temp left behind" checks that had gone blind (they stat'd the old fixed temp
name) glob the new names instead.
