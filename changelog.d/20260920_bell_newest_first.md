### Fixed

- The operations bell now lists the newest run first. `groupOperations` emits its
  rows bucket by bucket (all runs of one operation kind, then the next), so the
  popover carried no time order at all and a just-finished job usually rendered
  at the very bottom, below a screen of older successes — the one place nobody
  looks. The bell now sorts by the same `groupTimestamp` the grouping fold uses,
  matching the Activity page, with a ULID tiebreak so rows do not reshuffle on
  every five-second poll.
