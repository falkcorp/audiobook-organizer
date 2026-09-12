### Fixed

#### Settings: PUT /api/v1/config refuses keys it does not know instead of ignoring them

A config update carrying a key the configuration does not have used to answer
200 and change nothing. JSON decoding drops unknown keys, so a misspelled key or
a flat spelling of a nested one reported success and left the setting as it
was. On 2026-09-12 a PUT of `dedup_auto_merge_enabled: false` came back 200, and
auto-merge stayed on. The working form is `{"dedup":{"auto_merge_enabled":false}}`.

The endpoint now walks the payload against the configuration's JSON fields at
every depth: struct fields, map values and list elements. Any key without a
match is refused with a 400 before anything is written. The error lists every
such key by its full path (`dedup.auto_merge_enabld`,
`metadata_sources[0].bogus`), and the response carries them as `unknown_keys`.
A strict decoder backs the walk up, so the request still fails if the two ever
disagree. Removed settings get their own removal message, including the
flag-only `enable_sqlite3_i_know_the_risks`, which used to be dropped silently.

The three read-only keys that GET /config adds (`env_locked`, `setting_locks`,
`activity_db_resolved_path`) are dropped from a PUT rather than refused. A raw
GET response still cannot be sent back as-is, because it also carries the
immutable `database_type`; the web settings import drops that key.

The web Settings page stopped sending eight flat keys the server never read
(`auto_update_*`, `maintenance_window_*`) next to their nested objects. An old
exported settings file that carries them has those values folded into the
nested `auto_update` and `maintenance` objects on import. Before this change
they were lost without a word.

Four Settings controls wrote keys the server has no setting for, so they never
did anything. They are removed, because each would now make every Settings save
fail with a 400:

- "Enable AI rerank for metadata search" on the Metadata tab sent
  `metadata_llm_scoring_enabled`. The working switch is the one under Metadata
  Scoring (`metadata_scoring.llm_enabled`), and it stays.
- "Duration boost" and "Folder path boost" under Dedup sent
  `dedup.signals.duration_boost` / `folder_path_boost`. The server uses fixed
  constants for both.
- "Embed queue debounce (ms)" under Tools sent `tools.embed_queue_debounce_ms`.

A failed save now shows the server's reason, which names the refused key,
instead of a generic "please try again".
