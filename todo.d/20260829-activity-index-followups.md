- [ ] **Activity `Summarize` writes a summary row with an `OperationID` but no `act:op:` index entry.**
      `PebbleActivityStore.Summarize` replaces a group of entries with one summary row carrying
      `OperationID: gk.opID`, but only `Record` writes index entries, so the summary is invisible to
      `Query` with an `OperationID` filter — that filter takes the `act:op:` index fast path, never the
      tier scan. This is an index *completeness* gap, not the deletion leak fixed in
      `fix/activity-index-deletion`; it was deliberately left alone there because adding the write with
      the wrong nano field would manufacture fresh orphans. Same question applies to `BookID`, which
      `Summarize` does not carry onto the summary row at all.

- [ ] **Verify against production whether `act:digest:` and `act:debug:` keys actually exist.**
      A prior investigation claimed prod has zero `act:digest:` keys and no `act:debug:` tier, which
      would make `CompactByDay`'s rollup produce nothing and `Prune(cutoff, "debug")` delete zero rows
      on every run. Code contradicts half of it: three production sites write tier `debug`
      (`internal/activity/api.go`, `internal/activity/writer.go` level=debug, `internal/server/server.go`),
      and `CompactByDay` is reachable both from the nightly job and from a live handler
      (`internal/server/handlers/activity.go`). The old RootDir registration gate is gone — maintenance
      plugin registration is unconditional (`internal/server/server.go`). So if the tiers really are
      empty, the mechanism is something else (the job not firing), and that is what needs measuring.
      Needs a read-only key-prefix count against the prod Pebble store; not done here because the task
      forbade touching production.
