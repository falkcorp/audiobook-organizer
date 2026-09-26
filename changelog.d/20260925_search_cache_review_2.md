### Fixed

- **Search result cache, second review round.**
  - A failed shared search (a storage read error while building it, a crash in
    the build, or a queued build that was dropped) no longer fails every request
    waiting on it. The request runs the search directly instead, as it did
    before the cache existed, and the web client re-issues a long search whose
    poll reports an error.
  - The Library list no longer accepts a cached list that predates a bulk
    change. Only the quick-search pickers opt in to that (`Prefer: allow-stale`);
    `Prefer: respond-async` now means only "answer 202 while a long search runs".
    Bulk actions start from Library rows, so they never act on out-of-date
    membership.
  - Updating a cached search after a large bulk change no longer re-evaluates
    the whole changed set first. Past `MaxPatchChanged` (2048) changed books the
    cache rebuilds instead. Patch work now shares the build concurrency limit.
  - The background rebuild that restores exact relevance order after a patch is
    no longer lost when it joins an older build that is already running.
  - Author and series renames update the search index through at most two
    workers, with repeated renames merged, instead of one goroutine per rename.
