- [ ] **Op summaries must not read the same in preview and apply.**
      `maintenance.duration-reextract` prints `... would correct
      would-change=17161 (~2x=351) ...` whether it previewed or wrote. On
      2026-09-20 a run meant as an apply silently previewed (the `dry_run` vs
      `dryRun` flag drop, fixed separately), and its summary was
      indistinguishable from a real apply — the only way to notice was to
      re-read a book the summary named as `13s -> 2221s` and find it still 13s.
      A summary that cannot tell you whether anything was written is not a
      report. Every op whose params carry a dry-run flag should say which mode
      it ran in and, in apply mode, report what it ACTUALLY wrote rather than
      what it would have. Audit the registry for the same shape; the
      `applied N of M` metadata ops already do this correctly and are the model.
