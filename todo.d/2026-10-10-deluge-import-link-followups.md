- [ ] **IMPORT-ORPHANED-VERSIONS** Every `ImportFile` orphans its version
      (round-1 S1 of PR #3928). `CreateIngestVersion` runs before
      `CreateBookFile` in `internal/importer/service.go`, so when it looks for
      the book_file row to link (`internal/versions/ingest.go`) none exists:
      it logs `no book_file row matched the ingest path; version is orphaned`,
      the file row's `VersionID` stays unset and `FileHash` stays empty, so
      imported rows are `unverifiable` for the torrent cleanup fixer until a
      hash backfill runs. Fix: create the book_file row (with `FileHash`)
      before `CreateIngestVersion`. It changes every import, not only torrent
      ones. Must land before 10-PR2.
- [ ] **DELUGE-LOGIN-GATE** `deluge.Client.LoginContext`
      (`internal/deluge/client.go`) holds `c.mu` across the `auth.login` HTTP
      round trip, and `sync.Mutex.Lock` ignores ctx (SF-2 of PR #3928 round 2).
      While `authed` is false and Deluge black-holes packets, a background
      login with no deadline (e.g. the `*/30` protected-paths refresh via
      `ListTorrents` → `Login`) holds the mutex for the 5-minute transport
      timeout, so the discover import's 15 s verification can wait up to 5
      minutes. Fix: a context-aware 1-slot login gate (`loginSem chan
      struct{}` acquired with `select` on `ctx.Done()`), recheck `authed`
      under `c.mu` after acquiring, and keep `c.mu` only for `authed` and
      `loginGen`, never across I/O. Keep
      `TestCall_ConcurrentExpiredSessionLogsInOnce` green and add a test: with
      a login in flight against a hanging server, a second
      `GetTorrentDetailContext` with a 200 ms deadline returns within ~1 s.
