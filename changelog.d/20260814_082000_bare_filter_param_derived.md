### Fixed

- **`?year=2001` and 16 other filters silently returned the entire library.**
  Field filters travel inside the `filters` JSON parameter; passed bare, gin
  ignores them and the request lists every book while looking exactly like a
  narrowed query — matching count and all. Measured on production 2026-08-14,
  against an unfiltered baseline so the reading is not merely plausible:

      ?year=2001                 -> count=63869
      ?work_id=abc               -> count=63869
      ?marked_for_deletion=true  -> count=63869
      (no filter, baseline)      -> count=63869

  A guard added the previous day
  rejected 26 such names, but it was a hand-written third copy of a field list
  that already had a single source of truth, and it had drifted: `year`,
  `work_id`, `isbn10`, `isbn13`, `series_number`, `created_at`, `updated_at`,
  `marked_for_deletion`, `duration`, `bitrate`, `bitrate_kbps`, `file_size`,
  `file_size_bytes`, `sample_rate`, `sample_rate_hz`, `channels` and `bit_depth`
  were all missing.

  The guard is now derived from `audiobooks.KnownFilterFields()` — the list
  already pinned to the matcher — minus an explicit two-name allow-list, so the
  third copy no longer exists and a field added to the canonical list is
  guarded the same day.

  `library_state` stays allow-listed: it is genuinely both a filter field and a
  bare parameter of this endpoint. A global collision survey turns up five
  such names, but four (`title`, `author`, `duration`, `format`) have their
  accessors on *other* endpoints — metadata search, audio sample, dedup export —
  and the guard reads only its own request's query string.

  Not fixed here: `?version_group_id=X` still lists the whole library. It is not
  a filter field at all, so the derivation cannot reach it; making it one needs
  new matcher work.
