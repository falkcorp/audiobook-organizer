- [ ] **Activity-log summaries drop their data — "cover art saved to" (to
      WHERE?), "ISBN enrichment succeeded for" (for WHAT?).** Owner
      screenshot 2026-08-14 18:03. Root cause located: these are slog calls
      whose sentence is in the MESSAGE and whose data is in ATTRS —
      `internal/metafetch/service_apply.go:611`
      `slog.Info("cover art saved to", "path", coverPath)`,
      `service_fetch.go:37` ("ISBN enrichment succeeded for", "id"),
      `service_fetch.go:292` — and the slog→activity bridge keeps only the
      message. A neighboring row ("ISBN enrichment found" isbn=… title=…
      with a stray quote) shows the OPPOSITE bridge behavior: a raw slog
      TextHandler line, quotes and all, pasted into the summary — so two
      inconsistent bridges exist. Fix: one bridge that renders attrs into
      the summary (book title resolved from id where present), and sweep
      metafetch's sentence-shaped slog messages onto it.

- [ ] **Metadata-apply activity rows don't NAME the book.** Same screenshot:
      "Applied narrator: Alex Kozlowski → Grant Cartwright" with a bare
      "book →" link — the summary must lead with the book title ("The
      Whispering Night: applied narrator …"); a link target is not a
      summary. Also "Applied audiobook_release_year: → 2021" renders an
      empty FROM value as a dangling arrow — show "(none) → 2021".
