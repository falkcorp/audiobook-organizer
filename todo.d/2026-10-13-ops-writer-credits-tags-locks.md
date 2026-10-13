- [ ] **OPS-WRITER-CREDITS-TAGS-LOCKS** Add `ModifyCredits`, `ModifyTags` and
      `LockFields` to the v3 `ops.Writer` (`internal/operations/opswriter`)
      when the first port needs them. 05-PR5 dropped them from the sketch:
      `database.BookCredits` and `database.BookTags` do not exist, there is no
      atomic modify for narrators or tags, and no field-lock store method maps
      to `LockFields`. Each needs the intent -> write-in-critical-section ->
      history -> clear order the other Writer methods follow.
