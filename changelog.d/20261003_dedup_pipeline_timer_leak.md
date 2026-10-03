### Fixed

- **Review page: a dedup run that was no longer being watched kept a timer
  alive for up to a minute.** The browser follows a "Find all duplicates" run or
  its preview by polling. When that follower was replaced or its prompt was
  closed, it was only told to stop the next time its wait ran out, so the timer
  and everything it referenced stayed in memory until then, and it made one more
  status request before stopping. The wait can now be cancelled: a replaced
  follower, and a preview whose prompt was closed, stop immediately. This also
  clears the "Scan for memory leaks" check, which had been failing on main and
  on every pull request that touched the web app since 2026-10-02.
