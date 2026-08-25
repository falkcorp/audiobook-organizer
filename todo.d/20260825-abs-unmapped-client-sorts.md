- [ ] **Decide what to do about the 6 ABS client sorts this server has no field
      for.** `absSortFields` (`internal/server/handlers/abs/browse.go`) holds 11 accepted
      parameter spellings resolving to 9 distinct store fields. Six known client
      sorts resolve to `""` instead, which means "no ordering requested" everywhere downstream, so the
      client gets a 200 and the store's default order.

      As of 2026-08-25 this is at least no longer silent — `warnUnsupportedSort`
      logs at most once a minute and names the supported alternatives —
      but nothing sorts. They are unsupported for three different reasons and
      each wants a different decision:

      1. **File Modified** — tractable. `Book.LastScanMtime *int64` already
         exists. Needs a `bookSortComparators` entry plus an `absSortFields`
         mapping. Deliberately not done as part of the silence fix: adding a
         sort is a feature, and it should be a decision rather than a drive-by.
         ⚠️ If added, cover it in `internal/audiobooks/sort_every_field_test.go`
         — that test enumerates `database.SortableBookFields()` and will fail
         on arrival until it has a fixture, which is the intended behaviour.
      2. **Progress ×3** (In Progress / Finished / Percent) — per-user state
         (`UserBookState.ProgressPct`), not a `Book` field. The summary path has
         no shape for a per-user join, so this is a design question, not a
         mapping.
      3. **File Birthtime** — no field exists anywhere; would need capture at
         scan time.
      4. **Randomly** — arguably should stay unimplemented: pagination is only
         meaningful over a stable order.

      Worth confirming against a real client which of these users actually
      reach for before building any of them.
