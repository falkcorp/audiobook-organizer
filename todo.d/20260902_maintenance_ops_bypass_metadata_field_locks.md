- [ ] **~17 maintenance/regroup ops write `Book` columns without consulting a field
  lock** — the one write-path class the #3054 review's gate table (row 19, M2) found
  unguarded that PR #3054 deliberately did not close: it had already reached 72 changed
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

- [ ] **Field-lock LOW items deferred from PR #3054's round-2 review** — none of these
  lose data; they are clarity and drift risks the review (L1–L4) named:
  - **L1: a third field vocabulary.** `internal/metafetch/service_apply.go`'s
    `fields`/`allowed` apply-selection allowlist uses `author`, `series`, `year`,
    `isbn`, `cover_url` — caller-supplied *selection* names, not lock names, so it is
    legitimately a different vocabulary. But it is spelled close enough to the lock
    keys to be misread as one, and `RecordChangeHistory` (`series`) and
    `persistFetchedMetadata` (`print_year`, `cover_url`) add two more spellings.
    Either give the selection vocabulary its own named constants or document at each
    site that it is deliberately not `database.FieldKey*`.
  - **L2: the UI locks 12 keys, the backend 13.** `FIELD_TO_API` has no `asin`, so a
    user cannot lock ASIN from the edit dialog even though the backend honours it.
    Decide whether to expose it or to document the asymmetry in the conformance test,
    which currently pins the UI list as literals without saying why it is short.
  - **L3: `series_position` can be locked while `series_name` is not.**
    `FieldLocks.Apply` protects `SeriesSequence` when `series_name` is locked, so the
    pair is consistent in that direction; the reverse (position locked, name free) lets
    a fetch move the book to a different series while pinning its number. Decide
    whether that combination should be rejected at write time or is intentional.
  - **L4: the hand-written `database.MockStore` reads as "nothing locked."**
    `GetMetadataFieldStatesFunc` unset returns `(nil, nil)`, so any test using the
    hand-written mock without seeding lock rows silently exercises the unlocked path.
    That is the right default, but it means a guard test that forgets to seed passes
    for the wrong reason. Worth a comment on the field, and worth preferring the
    mockery mock (which fails on an unexpected call) in new lock tests.
