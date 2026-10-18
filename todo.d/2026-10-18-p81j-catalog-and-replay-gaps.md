- [ ] **01-P81J-CATALOG-READ** 01-P81j retired `GET /api/v1/catalog/entries` and
      `/catalog/entries/:id` (410, no replacement). Nothing else lists harvested
      catalog entries now: `catalog.coverage-report` only gives aggregate counts,
      so an operator cannot look at what `catalog.harvest-authors` wrote for one
      author or series. 02 PR 7a/7b build on the local catalog; if they want a
      read surface it should be a v3 endpoint (05 PR 9 adds `/api/v3/ops/*`, but
      there is no catalog read there). Also decide whether the `cat_series` and
      `cat_eg` (edition group) index writes in
      `internal/database/catalog_entry_store.go` stay: no production code reads
      them any more, only tests.
- [ ] **01-P81J-REPLAY-ALL** 01-P81j retired `POST /api/v1/review/replay-approved`
      (410; replacement is `POST /api/v1/review/bulk` with explicit `ids`). Bulk
      approve now reports per-item apply errors in `failed`, skips already-applied
      ids and sets `apply_disabled` when apply is off, so the failure and retry
      behaviour of the old route is covered. Still missing: a dry run (the old
      `would_replay`, skip reasons and apply cap preview), a `kind` scope over
      approved items (bulk by `kind` only targets pending ones, so an operator must
      list the approved items and send their ids), and a `limit` canary. If bulk
      re-approve is how approved-while-off decisions are meant to execute, give
      `/review/bulk` an `include_approved` option with a dry run.
