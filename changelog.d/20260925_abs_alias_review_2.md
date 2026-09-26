### Fixed

- ABS alias seed: a failed per-item alias lookup no longer marks the user as
  seeded. The aliases found are recorded, and the next `/api/me` list retries
  the seed, so a merged book opened by its old id keeps its progress row in
  AudioBooth. An alias graph over the cap (`ErrSyncAliasLimit`) still lets the
  seed finish, because a retry cannot succeed and the client never held those
  rows.
- ABS alias seed: the seed window now has an upper bound. The server stores a
  cutoff the first time it starts with alias-use tracking and never moves it;
  only progress changed between 2026-09-25 and that cutoff is seeded. A user
  who first lists weeks later no longer gets alias rows for ids the client
  never held, which had re-created the double count in AudioBooth's stats.
- `ListSyncAliasUses` reads the recorded aliases and the seeded flag from one
  Pebble snapshot, so a concurrent seed can no longer be seen as seeded with
  none of its aliases.
