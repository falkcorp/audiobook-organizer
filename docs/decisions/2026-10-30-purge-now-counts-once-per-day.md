<!-- file: docs/decisions/2026-10-30-purge-now-counts-once-per-day.md -->
<!-- version: 1.1.0 -->
<!-- guid: 34a44fdf-1d3b-417d-a3d4-858de10bfe70 -->
<!-- last-edited: 2026-10-30 -->

# Purge now counts at most once per calendar day

Status: taken under the owner's autonomy instruction of 2026-10-30; open to review.

## Problem

`settleStuck` (internal/versions/lifecycle.go) adds one to
`BookVersion.PurgeUndecidedNights` on every stuck attempt on a purging version.
Three attempts in a row that unlink nothing escalate the version to purge_failed.
Every "Purge now" click was therefore a night: three clicks in a minute escalated a
version that had been stuck for one day. The kept-file sweep (`settleNights`)
already counts at most one night per calendar day, keyed on
`PurgeUndecidedNightOn`.

## Decision

One helper, `nextNightCount(was, wasOn, today)`, holds the rule: an unsettled
attempt on the day the count last grew leaves the count alone; any other day adds
one and stamps the day. `settleNights` and `settleStuck` both call it.

A manual purge still runs in full and still stores the version record (with the
day), so the last-attempt day is current. It only stops inflating the count. An
attempt that unlinks something still resets the count and clears the day.

`settleStuck` reads the day from a package clock, `purgeClock`, so a test can
move it.

## Rejected

- Keep per-attempt counting and only dedupe in the UI. The count is the
  escalation trigger, so the server must own the rule.
- A separate last-attempt timestamp field. `PurgeUndecidedNightOn` already is that
  value for the sweep; a second field would let the two drift.
- Threading a `now` parameter through PurgeVersion and its callers. A larger
  change than the seam needs; the sweep takes its clock as an option, the purge
  has no options struct.

## Also covered: settleNothingServed

`settleNothingServed` (a purging version whose book serves nothing on disk) shares
the counter and had the same Purge now inflation. It now uses `nextNightCount` with
the same clock, so both stuck paths count one night per calendar day.
