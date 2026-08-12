### Fixed

#### Organize reported success when every book failed, and could not be cancelled

Three defects on the same path, all of which made an organize run describe itself as
something other than what happened.

**It always reported success.** `PerformOrganize` ended in an unconditional `return nil`.
Every book could fail and the caller still saw success, so the operation was recorded as
succeeded and nothing upstream had any way to learn otherwise. A run now returns an error
when it was cancelled, or when it failed for every book it attempted. A partial failure
stays a success on purpose — one failure in three thousand books should not fail the whole
operation — with the count carried by the summary instead.

**The summary hid failures.** The logged summary listed organized, re-organized,
already-correct and skipped, but not failed. The failure count existed only in the
`organize_summary` operation-change row, which nobody reads. A run in which every book
failed therefore printed `Organize complete: 0 organized, 0 re-organized, 0 already
correct (stamped), 0 skipped` — indistinguishable from a run that had nothing to do. The
summary now always states the failure count and the total attempted, and a cancelled run
says `CANCELED` rather than `complete`.

**Cancellation only half worked.** `organizeBooks` accepted a `context.Context` and never
read it; cancellation was checked solely in the job feeder. Stopping a run therefore
stopped new work being queued but let the eight workers drain everything already buffered,
and context cancellation — an HTTP client disconnecting, or server shutdown — did nothing
at all. Both the feeder and the worker loop now check `ctx` as well as the operation's own
cancel flag.

The outcome rule and the summary text are now separate functions with tests, so the policy
is stated and pinned rather than implied by the last line of a long method.
