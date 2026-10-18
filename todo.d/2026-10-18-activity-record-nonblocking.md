- [ ] **ACTIVITY-RECORD-NONBLOCKING** A live `maintenance.vacuum-activity-db` holds
      the SQLite activity store's single writer connection for its whole run, so
      synchronous `SQLActivityStore.Record` callers block until it ends: the
      library scan (once per book, `server_search.go`), metadata apply and
      write-back, and the admin debug paths. The log-tee `activity.Writer`
      channel holds 10k entries and drops lines once it fills. The op documents
      this and refuses to start during a scan or metadata op, but that guard is
      point-in-time. Give `Record` a context (so a caller can time out or be
      canceled) or a deferral (queue the row for the writer to drain after the
      VACUUM), then drop the stall warning from the op description and preview
      message. Needs a test that a Record issued during a VACUUM returns
      promptly and lands afterwards.
