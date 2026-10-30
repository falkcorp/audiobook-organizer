### Fixed

- A stuck purge now counts as at most one unsettled night per calendar day, as the
  kept-file sweep already did, so clicking "Purge now" repeatedly no longer pushes
  a purging version to purge_failed early. The purge still runs and records the
  attempt; it just does not add another night on a day that already counted one.
