- [ ] **TODO-BF-REPOINT-ORPHAN-JOURNAL** A crash between `maintenance.repoint-book-file-rows`
      journaling a repoint (`rbfApplyOne` in `internal/plugins/maintenance/repoint_book_file_rows.go`,
      a Sync write inside the `ModifyBookFile` callback) and the row commit leaves
      a live `book_file_repoint` journal row for a repoint that never happened. The op
      uses `ResumeDrop`, so a re-apply is a new op with a new op id and a new change id;
      a stable change id cannot replace the orphan, and an id derived from op id and
      row id would not either. This is the JOURNAL FIRST design, older than PR #4169.
      Fix: a two-phase journal. Write the row as pending, mark it applied after the
      commit, and have the next run of the op (or a sweep) resolve pending rows by
      comparing the stored row with the journal's pre-image and new path, voiding the
      ones that never applied. Cost: a new journal state, a change to every reader
      that counts or reverts these rows (revert, count, preflight), and a resolver
      pass. Needs a design decision before work starts.
