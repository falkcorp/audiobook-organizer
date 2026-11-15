- [ ] **TODO-F5-09-RESUMED-INTRO-SCAN-HOLD** Hold a continued
      `books.maintenance.transcribe-file-intros` attempt at dispatch while a v2
      library scan or bulk metadata apply is queued or running, instead of waiting
      inside the run. Today a resumed attempt (a cursor exists) waits for the scan
      in the run and cannot yield again while it waits (the executor's `yieldNext`
      needs progress past the starting point), so continuous scan activity for a
      whole attempt (about 24 h: a zombie queue row, back-to-back chained scans, a
      weekly sweep across restarts) ends the run `timed_out`, which is terminal.
      HEAD fails at once in the same restart-while-a-scan-is-queued case, so the
      native kind is better than HEAD there; it only loses how much continuous busy
      time one run tolerates. Fix at F5-18: a `Ready` signal derived from the v2
      queue (Down while one of the busy defs is queued or running), or, once the
      busy ops are native kinds that declare `book_files`, the write-set conflict
      or `NotWhile`. Acceptance: a continued attempt queued behind a scan does not
      start, burns no attempt clock, and the in-run wait is deleted. Brief:
      `docs/agent-tasks/platform-core/seams.d/F5-09.md`.
