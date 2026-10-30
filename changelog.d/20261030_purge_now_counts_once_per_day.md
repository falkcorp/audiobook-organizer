### Fixed

- A stuck purge (including a refusal because the book serves nothing on disk) now counts as at most one unsettled night per calendar day, as the
  kept-file sweep already did, so clicking "Purge now" repeatedly no longer pushes
  a purging version to purge_failed early. The purge still runs and records the
  attempt; it just does not add another night on a day that already counted one.
