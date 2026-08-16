- [ ] **Finish (or delete) the iTunes plugin op migration.** `internal/plugins/itunes/`
      holds five stub `Run` bodies. Four are excluded from `registeredDefs()` because
      the real implementations live in `internal/server/itunes_ops.go` and
      `itunes_path_ops.go`; the package is now half a migration that does nothing.
      Either port the real bodies in (`itunes.sync` additionally needs
      `s.activityWriter` + `s.itunesActivityFn` threaded into `Plugin`, which is a
      design decision, not a move) or delete the stub files and their defs. Leaving
      them is what caused #2490's sibling bug: a stub that looks registrable.
- [ ] **Wire `itunes.position-sync` or drop it.** `internal/itunes/service/position_sync.go`
      implements a full bidirectional bookmark/play-count sync (`PositionSync.Sync()
      (pulled, pushed int)`) and **nothing in the codebase calls it** — the only
      reference is the TODO comment in the plugin stub. Wiring it turns on real writes
      to user positions across two systems on a 63k-book library, so it needs an
      explicit decision and a dry-run, not a one-line hookup.
