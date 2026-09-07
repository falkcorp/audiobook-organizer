### Fixed

- **The diagnostics export's `operations.json` was frozen in the past.** It read
  the retired v1 operations keyspace, which nothing has written to since the v1
  minter was retired on 2026-08-23 — so every bundle generated since then shipped
  an operations list whose newest entry was weeks old, with no indication that
  anything was missing. It now reads the v2 keyspace, so the section covers the
  100 most recent runs as it was always meant to.

  An export that silently omits the entire recent history is worse than one that
  omits the section outright, because the reader has no way to tell the
  difference.
