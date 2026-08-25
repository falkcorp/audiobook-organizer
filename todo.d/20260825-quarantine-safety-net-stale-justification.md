- [ ] **The quarantine "safety net" in `buildAudiobookListResponse` is justified
      by a claim that is no longer true, and the hole it was covering moved.**
      `internal/server/audiobooks_helpers.go:66-68` says:

      > Safety net for the degraded (memdb-down) read path, where the Pebble
      > fallback does not honor ExcludeQuarantined. In the normal memdb path the
      > scan already excluded these, so this drops nothing.

      The first sentence is false at HEAD. `PebbleStore.GetAllBookSummariesFiltered`
      **does** honor it — `internal/database/pebble_store.go:1119`,
      `if f.ExcludeQuarantined && book.QuarantinedAt != nil { continue }` — as
      does the memdb walker (`internal/database/memdb_summaries.go:227`, `:444`).
      The comment at `pebble_store.go:816` records that the old implementation
      applied only `IsPrimaryVersion` and `ExcludeQuarantined` and dropped the
      rest; the fix that closed that went the other way from what this net
      assumes.

      So on both production paths the net now drops nothing — it strips a page
      that was already stripped. That makes it inert rather than harmful, but it
      is inert for a reason nobody reading it would guess, and it runs AFTER
      pagination, so if it ever does fire it returns a short page (a limit=500
      request answering with fewer than 500) with a count that disagrees.

      **The hole it was written for still exists, just somewhere else.** When the
      store does not satisfy `filteredSummaryStore`, `summariesPushdownFiltered`
      returns `didPushdown=false` and its contract says the caller "must
      re-apply filters in-memory — slower, but correct". That promise is false
      for quarantine specifically: `ExcludeQuarantined` appears nowhere in the
      `internal/audiobooks` post-filter block, and it is absent from the
      `hasPostFilters` disjunct at `service_query.go:99`, so a request carrying
      only `ExcludeQuarantined` may not post-filter at all.

      Today that path is mock/test-only, so this is latent, not a live bug — and
      a green suite will not surface it, because the suite's stores conform.

      - [ ] Apply `ExcludeQuarantined` in the service post-filter and add it to
            the `hasPostFilters` disjunct, so the documented fail-safe is
            actually safe. This is the same shape as the `RestrictToIDs` fix
            (see `service_query.go`): a predicate pushed down correctly but
            silently dropped on the fallback.
      - [ ] Then delete the safety net in `audiobooks_helpers.go`, or rewrite its
            comment to say what is actually true. Do not simply delete it on the
            grounds that "nothing fails" — verify the fallback first.

      Filed after mistaking this file for a deleted one earlier the same day; the
      code here is live and worth reading properly.
