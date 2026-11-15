- [ ] **TODO-F5-09-FILE-INTROS-WRITE-SET** Decide the declared write set of the
      native `books.maintenance.transcribe-file-intros` kind before F5-18 registers
      it. It declares `book_files` and `books` (it writes intro results onto file
      rows and re-aggregates the parent book), so once registered the scheduler
      will hold back every other writer of those two resources while it runs,
      including the nightly acoustid backfill, for the length of a full-library
      transcription. Options: keep the declaration and accept the hold-back; split
      the kind so only the write gate declares the resources; or add a narrower
      `pkg/ops` resource for intro results. Acceptance: the decision is recorded in
      the F5-18 seams note and the kind's Effects match it. Brief:
      `docs/agent-tasks/platform-core/seams.d/F5-09.md`.
