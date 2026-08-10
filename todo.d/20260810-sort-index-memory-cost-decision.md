<!-- file: todo.d/20260810-sort-index-memory-cost-decision.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3f81a5d2-6e04-4b97-8c13-d29e0b7f461a -->
<!-- last-edited: 2026-08-09 -->

- [ ] **⚖️ DECIDE which sort indexes to enable — the design-doc cost estimate was ~10×
      optimistic.** The machinery is built, tested and merged behind
      `enabled_sort_indexes`, defaulting to empty (today's behaviour exactly). What is
      left is choosing what to turn on, and that needs the real number rather than the
      one the decision was originally made on.

      ## What was decided, and on what basis

      On 2026-08-09 the owner selected nine sort fields to index — author, narrator,
      series, created_at, updated_at, year, duration, file_size, bitrate — from a design
      doc that estimated **"tens of MB per sort field"** against ~1.25 GB resident, i.e.
      "low single-digit percent each".

      ## What it actually costs

      Measured, 100,000 books, identical fixture on both sides
      (`TestSortIndexCost`, `internal/database/memdb_sort_index_cost_test.go`):

      | | without | all nine | delta |
      |---|---|---|---|
      | heap per book | 2,645 B | 6,395 B | **+142%** |
      | at 366,916 books | 925.6 MB | 2,237.8 MB | **+1,312 MB** |
      | insert 100K | 335 ms | 935 ms | **2.8× slower** |

      That is **~146 MB per sort key**, not "tens of MB". memdb is already ~1.25 GB
      resident with a 107.9 s warmup, so all nine roughly doubles it.

      **And this is a LOWER bound.** The fixture leaves `Author` and `Series` unset, so
      two of the six physical indexes store the 1-byte "missing" key for nearly every
      row. A library with populated author/series data pays more than this.

      ## Why the estimate was wrong

      The doc reasoned that "a secondary index stores keys and IDs, not books", which is
      true and led to sizing by key length. But go-memdb is an **immutable** radix tree:
      every insert path-copies the nodes from root to leaf. Cost is dominated by node
      allocation, so a short key is not a cheap key. Roughly 417 B per book per index
      regardless of what the key contains.

      ## The decision

      Not "should we index" — the pagination-disabled full-set sort is genuinely bad.
      It is **which fields earn ~146 MB each**, and there is no usage data to answer it:
      nobody has measured which sorts real users pick. Options, cheapest first:

      1. **Enable none for now** (current default). Costs nothing, changes nothing.
      2. **Instrument first** — log `sort_by` values for a week, then enable only the
         fields that actually appear. This is the option that replaces a guess with
         evidence, and the instrumentation is small.
      3. **Enable a chosen subset.** `created_at`/`updated_at` are the most likely to be
         worth it ("what's new" is a real browsing pattern); the numeric triage fields
         (duration/file_size/bitrate) are plausibly rare enough to leave on the slow path.
      4. **Enable all nine** and accept ~2.5 GB resident. Only with headroom confirmed on
         the host, and re-measure warmup — 107.9 s is already not short.

      After enabling anything: re-measure warmup and RSS on prod, because the
      extrapolation from 100K is linear-by-assumption and 366,916 is 3.7× further out.

      ## Also worth knowing

      `CanPushDownSort` consults the **enabled** set, not the known set, so a field that
      is not indexed correctly falls back to the existing path instead of asking memdb
      for an index that was never registered. `SetEnabledSortIndexes` must be called
      before the store opens — it is, from the single `cobra.OnInitialize` hook in
      `cmd/root.go`.

      Related: `docs/design/2026-08-09-search-backend-options.md` §2.3 (which still
      carries the old estimate in its prose and should be corrected to point here).
