<!-- file: docs/executive-summaries/2026-10-12-resolve-production-authors-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9f2930df-7f8b-4acf-9079-382a64741362 -->
<!-- last-edited: 2026-10-12 -->

# "Resolve production authors" is now one job that waits for scans and can be stopped

Branch `task/04-p4d` (04-P4d, PR #3984).

## Executive Summary

- **Background.** Some books list a production company (for example a studio)
  as the author. A job looks those books up again by title to find the real
  author. Two copies of this job existed under different names, and they did
  different things: the one you could run by hand did nothing at all when
  applied, and the scheduled one did the real lookups.
- **What could go wrong.** The scheduled copy rewrote book details without
  waiting for a library scan to pause first, so a scan and this job could
  write the same book at the same time and one could silently undo the other.
  Stopping the job also did not stop a lookup that was already under way, so
  it could still write after you cancelled it.
- **The fix.** There is now one job. Its old name still works. When applied it:
  - pauses library scans first, the same way the other metadata jobs do, and
    lets them resume when it finishes;
  - works on a few books at a time (the bulk metadata fetch's setting, at most
    8) instead of one by one;
  - looks each book up once, even when two production companies are credited
    on it;
  - stops lookups that are under way when you cancel it.
- **What has not changed.** Running it without choosing "apply" still only
  previews and changes nothing. Only users who can manage settings can run it.
