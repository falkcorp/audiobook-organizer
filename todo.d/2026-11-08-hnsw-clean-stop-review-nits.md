- [ ] **HNSW clean-stop marker: three gaps from the R1-15 round-2 review (N1, N2, N4)**,
      none a regression against main, none built yet (PR #4476 recorded them; the reviewer
      rated all three as nits).
      - **N1, crash window in the re-check.** `MarkCleanStop` stores `cleanStopLive` and then
        re-reads the verdict count; a writer adds to the count and then reads `cleanStopLive`
        (`hnsw_clean_stop.go`, `hnsw_verdict_watch.go`). The atomics are sequentially
        consistent, so one side always sees the other. But when the re-check is the side that
        sees it, it drops the marker while the writer's engine write runs alongside
        `discardCleanStop`, and a SIGKILL or power loss between the two leaves the marker on
        disk beside rows it no longer describes. **Fix:** write the marker after the main
        `GatedDB` is sealed against writes, which closes the window and makes the hook
        unnecessary. Cost: `MarkCleanStop` holds `s.mu` across two fsyncs, stalling any
        verdict write for that long, which is acceptable at stop.
      - **N2, a marker that survives a double I/O failure at open.** The open-time take
        (`claimCleanStop`) removes the marker and fsyncs the directory, and the claim lives
        only in memory, so a crash after it reconciles. If the removal fails twice and the
        fallback empty write fails too, a vouching marker stays on disk (logged at ERROR),
        and a later boot can honor it once the I/O recovers. **Fix:** record a Pebble-side
        open epoch in the marker, so a marker from before the last open never vouches.
      - **N4, book IDs with a colon are not counted.** `isVerdictKey` treats `book:<id>` as a
        row only when the ID has no further colon, because the `book:<index>:...` families
        share the prefix, and it relies on `ValidateBookID`, which only `CreateBook`
        enforces. A legacy row whose ID contains `:` would change without counting.
        **Fix:** count the colon IDs in production first. With none, enforce
        `ValidateBookID` on every book-row writer so none can appear; with some, list the
        index families explicitly and count every other `book:` key.
