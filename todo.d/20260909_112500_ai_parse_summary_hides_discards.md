- [ ] **`ai parse summary` reports a fully-green line for a batch where nothing
      was written.** Found 2026-09-09 by probing `library.ai-parse` in prod with
      three deliberately nonexistent book IDs. Every row was discarded; the op
      log said:

      ```
      ai parse summary: 3/3 book(s) parsed in 1/1 batches; 0 batch failure(s), 0 save failure(s)
      operation finished  outcome=completed
      ```

      Neither counter is lying — the LLM did parse all three, and no save
      *failed*. **Discarded is a third outcome that neither number shows.**
      `saveAIFieldsToPrimary` returns `("", nil)` when the row cannot be
      resolved (`ai_parse_async.go:325-333`), and the caller only counts a
      non-nil error:

      ```go
      stampPath, saveErr := save(ctx, &books[idx])
      if saveErr != nil {
          savesFailed.Add(1)          // ai_batch_phase.go:257-259
          ...
      }
      booksParsed.Add(1)              // increments either way
      ```

      That nil is deliberate and correct — a row can legitimately be deleted or
      dedup-merged between nomination and the batch running, and that is not an
      error. The gap is only in the *accounting*.

      **Why this matters now, and it is not hypothetical.** The `row == nil`
      comment already records that a previous version *"made a systematic
      resolution failure across every book in every batch look exactly like a
      library where nothing needed writing"* — and the fix applied then was a
      `Warn` line, which lives in the app's stdout log, not in the operation
      record an operator actually reads. Meanwhile prod currently has **115,086
      of 726,742 `book_file` rows (15.8%) pointing at no file**, so
      resolution failures at scale are the expected condition, not the edge
      case. A batch that discards 100% of its work is presently
      indistinguishable, in the op log, from one that wrote 100% of it.

      **The existing mitigation does not reach the operator, measured.** The
      probe's three discards were confirmed in prod's journal:

      ```
      level=WARN msg="ai_parse: AI parse: no row for /probe/... ; parse discarded"
      [WARN] activity channel full, dropped: ai_parse: AI parse: no row for /probe/... ; parse discarded
      ```

      Every one of those three appears **twice** — once as the slog line that
      reached stdout, and once as `activity channel full, dropped`
      (`internal/activity/writer.go:248`). The warning survives in the journal
      and is **dropped from the activity store**, which is what the Activity
      page and the operation record show. So the "we fixed the silence by
      logging it" mitigation lands only where an operator has shell access.

      That overflow is already filed separately (TODO.md §"The activity channel
      overflows during organize and DROPS records") — **do not re-file it** —
      but the current magnitude is worth recording, measured 2026-09-09 11:28
      EDT: **2,669 of 5,914 journal lines in 10 minutes (45%) were activity
      drops**; 12,324 in the last hour. At that rate the activity store is not a
      dependable record of anything low-frequency, which is exactly what a
      per-book discard warning is.

      **Fix:** add a `SavesDiscarded` counter to `AIPhaseSummary` alongside
      `SavesFailed`, increment it on the `stampPath == "" && saveErr == nil`
      return, and include it in `ai_batch_phase.go:429`'s summary string. Then
      make `AIPhaseSummary.Degraded()` (`:419`) consider a batch with a high
      discard ratio non-clean, so it surfaces rather than reading as success.
      Cheap, and it converts a silent outcome into a counted one.
