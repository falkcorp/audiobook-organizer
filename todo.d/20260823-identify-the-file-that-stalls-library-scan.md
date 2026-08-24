- [ ] **SCAN-STALL-FILE** Identify why `Past Life Hero Book 3.m4b` stalls
      `ProcessFile`. The per-file timeout (#2830) converts the stall into a
      normal scan failure so the scan completes — it does NOT explain the file.

      Evidence, from prod's operations timeline (measured by another session,
      2026-08-21..23): `library.scan` ended `abandoned` on 9 consecutive runs
      with the numerator pinned at **14912** while the denominator drifted
      40109 → 40108 → 40106 → 40090 → 40089. Message each time:
      `"Processed: 14912/40109 books (Past Life Hero Book 3.m4b)"`, ending
      `"abandoned: op goroutine did not exit within grace after context
      cancellation"`. Latest 2026-08-23T20:48, i.e. AFTER the 10:48 deploy, so
      this is live on the current binary.

      A fixed numerator with a moving denominator is a deterministic hang on one
      specific input, not a race.

      **What to do:** run `ProcessFile` against that file directly and see which
      call blocks. `tag.ReadFrom` is the prime suspect — it walks an MP4 atom
      tree whose lengths come from the file itself, so a truncated or malformed
      container can make it spin or attempt an enormous read. Confirm before
      fixing: the other candidates in the same chain are `os.Open` on a stuck
      NAS mount and the SHA-256 read.

      If it IS `tag.ReadFrom`, the question is whether to bound the reader
      (an `io.LimitedReader` over the atom parse) or upstream it to
      `github.com/dhowden/tag`. Do not assume — a malformed file that hangs a
      parser is worth a minimal reproducer either way.

      **Do not close this because the scan now completes.** With #2830 the file
      fails, increments its scan-fail counter and eventually auto-quarantines,
      which is the correct behaviour for an unreadable file but is silent about
      whether the file is genuinely corrupt or the parser is wrong.
