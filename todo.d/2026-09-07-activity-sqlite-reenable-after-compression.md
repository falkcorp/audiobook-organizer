## Re-enable SQLite activity backend after details compression (2026-09-07)

The OOM streaming fix (#3090) and the `details` zstd compression (this PR) both
land while SQLite stays OFF on prod (`ACTIVITY_BACKEND=pebble`). Re-enabling is a
separate, deliberate step:

- [ ] **Re-run the migration with compression in place and confirm the size is
  sane.** The prod `activity.sqlite` was wiped, so the backfill starts clean.
  With `details` compressed the file should land near Pebble's compressed
  footprint, not the 30 GB+ raw blow-up. Deploy the new binary with
  `ACTIVITY_BACKEND=pebble` first (healthy, no backfill), then remove the env line
  and restart to run the streamed+compressed backfill; watch `change`-tier RSS
  stays flat AND `activity.sqlite` growth stays bounded, then confirm the parity
  flip to SQLite.
- [ ] **Only then** consider retiring the Pebble activity path and reclaiming the
  `act:` keyspace (separate, after SQLite reads soak).
