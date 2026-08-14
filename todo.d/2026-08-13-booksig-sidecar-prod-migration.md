- [ ] 💾 **Run `maintenance.booksig-sidecar-migrate` on production** — the op is
      merged and dry-run gated, but the ~580 MB/startup saving from PR #2387 is
      **not realized until the data actually moves**. #2387 shipped the
      `book_sig:<id>` sidecar with fallback-first reads, so all 67,824 rows still
      carry their signature inline and warmup still reports
      `discarded_field_mb[book_sig_v1_and_mask] = 580` against
      `phase_mb[books] = 729`. This is the only irreversible step in the sidecar
      design, so it needs an owner decision, not a scheduled run.

      Ordered procedure:

      1. **Dry run first**, whole library. Read the reported counts:
         `migrated / stripped-only / not-candidate / skipped-raced / errors`.
      2. **Instrument check before applying.** Compare against a NUMBER, not a
         vibe. 580 MB of inline signature at ~22 KB per book implies roughly
         **27,000 candidates** — i.e. well under half the library, because most
         books never had a signature. The op prints this cross-check itself as
         "candidates imply ~N MB", computed from the CANDIDATE count, so a
         healthy dry run should land near **~580 MB**. Two failure shapes:

         - reports all 67,824 as candidates → implies ~1,459 MB, which
           disagrees with the 580 MB warmup measurement by 2.5×: the detector is
           matching books that have no signature.
         - reports a few hundred → implies single-digit MB: the detector is not
           recognizing the inline shape at all.

         Either way, stop — do not apply on a detector that disagrees with the
         byte accounting. Note the 22 KB figure is itself derived from the
         580 MB total, so this checks the detector's population, not the size.
      3. **Canary**: apply with `{"dryRun":false,"limit":100}`. Do NOT assume the
         limited run is a stable prefix the full run resumes past — `ListBookIDs`
         has two implementations (memdb index order, which also drops
         soft-deleted books, vs. the Pebble key range) and which one answers
         depends on warmup state. The op is idempotent, so a full run simply
         re-examines the canary's books and reports them `not_candidate`; that
         is the guarantee to rely on, not the ordering.
         Verify the pairing on a named book: `GetBookByID` must return a non-nil
         `BookSigV1`, and its `book:` row must no longer contain
         `book_sig_v1`.
      4. **Full apply**: `{"dryRun":false}`. Expect to need MORE THAN ONE pass.
         Besides raced rows, the memdb `ListBookIDs` skips soft-deleted books,
         so a single run is not guaranteed to have enumerated every row still
         carrying an inline signature. Step 6's "candidates ≈ 0 on re-run" is
         the completion signal — not "the apply finished without errors".
      5. **Verify with the positive pair, not an absence.**
         `discarded_field_mb[book_sig_v1_and_mask] → 0` is weak evidence — it
         reads zero if the migration worked *or* if the field accounting stopped
         recognizing the field. Require instead that **`phase_mb[books]` actually
         drops from 729** AND a `GetBookByID` on a named migrated book still
         returns its signature.
      6. **Re-run the dry run.** Candidates should be ~0. Any `skipped-raced`
         count from the apply is books another writer touched mid-migration;
         they were skipped rather than reverted, and a second pass picks them up.

      Rollback: reads stay fallback-first, so migrated and un-migrated rows both
      work throughout. The row rewrite is irreversible in place but not lossy —
      the signature lives in the sidecar, and every migrated book keeps its
      pre-migration `book_ver:` snapshots, which still carry the full inline copy
      (`UpdateBook` never strips those), so `booksig-recovery-audit` remains a
      second-line recovery path.
