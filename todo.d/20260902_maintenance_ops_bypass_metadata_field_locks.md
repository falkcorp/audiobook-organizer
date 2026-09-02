- [ ] **~17 maintenance/regroup ops write `Book` columns without consulting a field
  lock** — the one write-path class the #3054 review's gate table (row 19, M2) found
  unguarded that PR #3054 deliberately did not close: it had already reached 47 changed
  files, past the review's own >40-file stop threshold, so the remaining rows were
  deferred rather than rushed. Every other unguarded row in that table (ISBN
  enrichment, scanner AI nomination, diagnostics AI apply, iTunes reconcile, dedup
  merge, dedup split/series merge, undo/revert) is now guarded.

  These write a lockable column (title, author, series, narrator, publisher, …)
  straight through `UpdateBook`, so a maintenance run silently undoes a user's locked
  edit:
  - `internal/maintenance/jobs/cleanup_series.go:240-242`, `:301-302`
  - `internal/maintenance/jobs/fix_read_by_narrator.go:201-205`
  - `internal/maintenance/jobs/fix_author_narrator_swap.go:85-86`
  - `internal/maintenance/jobs/refetch_missing_authors.go:177-178`
  - `internal/scheduler/extra_ops.go:398`, `:407` + `internal/plugins/maintenance/author.go:233`, `:242`
  - `internal/plugins/maintenance/fs_regroup_xml.go:332`, `:336`
  - `internal/plugins/maintenance/itunes_regroup.go:316`
  - `internal/plugins/maintenance/repair_junk_titles.go:184`
  - `internal/plugins/maintenance/title_backfill.go:150`
  - `internal/plugins/maintenance/title_repair.go:304`
  - `internal/plugins/maintenance/series_denumber_op.go:310`, `:314`
  - `internal/plugins/maintenance/author_conjunction_repair.go:355`
  - `internal/plugins/maintenance/booksig_recovery_audit.go:360`
  - `internal/plugins/maintenance/intro_transcribe.go:370`, `:722`, `:835`
  - `internal/server/entities_ops.go:153`, `:336`, `:363`
  - `internal/server/server_metadata.go:304`
  - `internal/server/handlers/entities/handler.go:469`, `:1095`
  - `internal/server/duplicates_helpers.go:360`, `:728`

  Fix shape: wrap each mutation in `database.ApplyRespectingLocks(store, book, mutate)`
  and count the returned kept-field names into the op's summary, exactly as
  `revert_metadata_fetch.go` does. The interface plumbing is already in place —
  `internal/plugins/maintenance/deps.go`'s `opsHousekeeping` and
  `internal/maintenance/job.go`'s `jobBookStore` both already embed
  `database.MetadataFieldStateReader` — so most sites are a call-shape change, not a
  signature change. Two need care rather than a mechanical wrap: the
  `entities`/`entities_ops` renames are arguably user-authored (a user renaming an
  author *is* the edit), so they likely belong in `database.RecordUserOverrides`
  instead of behind the guard; and `intro_transcribe.go` writes from transcription,
  which is automated and should be guarded.

  Each site needs a test on a non-empty fixture: lock the field, run the op with a
  different value, assert the stored value did not move AND that an unlocked sibling
  field did — an all-locked fixture cannot tell a working guard from an op that writes
  nothing.
