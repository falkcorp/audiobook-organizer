<!-- file: docs/database-pebble-schema.md -->
<!-- version: 1.3.0 -->
<!-- guid: 8f6e2c1b-7d4a-4f86-9f2a-5a6b7c8d9e0f -->
<!-- last-edited: 2026-07-03 -->

# PebbleDB Keyspace Schema and Data Model

This document defines the PebbleDB keyspace layout, entity models, and query
patterns for the Audiobook Organizer. PebbleDB is a sorted key–value store; we
design with prefix-based keys for efficient scans, atomic batch writes, and
forward-compatible JSON values.

## Design goals

- Human-debuggable, prefix-based keys (colon delimited)
- O(1) access for primary entities; prefix scans for collections and indices
- Separate logical audiobook metadata from physical file segments
- Preserve playback progress across multi-file → single-file merges
- Immutable playback event log with derived aggregates
- Built-in migration/versioning for the keyspace

## Conventions

- IDs: ULID strings (26-char Crockford base32) for time-sortable uniqueness — **Note:** This ULID design was never implemented for core entities (authors, series, books, works, narrators); production code uses integer IDs (`author:%d`, `book:%d`, etc., see `internal/database/pebble_store.go`). The `## Dedup / Embedding store` section's `id16hex` keys are the verified-accurate part of this document.
- Values: JSON with a `version` field for forward compatibility
- Timestamps: RFC3339 strings
- Booleans and numbers use native JSON types
- Secondary indices are separate keys pointing to primary IDs

## Key prefixes

Global/meta:

- `meta:` — global metadata and counters
- `mig:` — migration records (applied migrations)

Users/auth:

- `u:` — users
- `ua:` — user auth secrets/hashes
- `sess:` — sessions
- `pref:` — user preferences
- `authz:` — role/permission maps

Domain data:

- `a:` — authors
- `s:` — series
- `w:` — works (title-level logical grouping across editions/narrations)
- `b:` — books (logical audiobooks)
- `bf:` — book file segments (physical media files)
- `bfi:` — book→segment ordering index

Indexes (examples):

- `idx:user:username:<lower>` → `<userULID>`
- `idx:user:email:<lower>` → `<userULID>`
- `idx:author:name:<normalized>` → `<authorULID>`
- `idx:series:name:<normalized>` → `<seriesULID>`
- `idx:series:author:<authorULID>:<seriesULID>` → `1`
- `idx:book:author:<authorULID>:<bookULID>` → `1`
- `idx:book:series:<seriesULID>:<posPadded>:<bookULID>` → `1`
- `idx:book:title:<normalized>:<bookULID>` → `1`
- `idx:book:tag:<tagLower>:<bookULID>` → `1`
- Future: `idx:book:genre:<normalized>:<bookULID>` → `1`
- `idx:book:isbn10:<isbn10norm>:<bookULID>` → `1` (isbn10norm: uppercase X;
  remove hyphens/spaces)
- `idx:book:isbn13:<isbn13norm>:<bookULID>` → `1` (isbn13norm: remove
  hyphens/spaces)

// v1.1.0 additions

- `idx:work:title:<normalizedTitle>:author:<authorULID|null>` → `<workULID>`
- `idx:book:work:<workULID>:<bookULID>` → `1`

Playlists and playback:

- `pl:` — playlists
- `pli:` — playlist items (ordered)
- `playe:` — playback events (append-only)
- `playp:` — playback progress (latest snapshot)
- `stats:` — derived aggregates

Operations:

- `op:` — operations (scan, organize, transcode, merge)
- `opl:` — operation logs

## Entity JSON schemas (values)

Each entity JSON includes a `version` for forwards compatibility.

Note: Angle-bracket placeholders like `<ulid>` are shown as literals; markdown
lint (MD033) warnings are acceptable here as they document template fields.

### User

Key: `u:&lt;userULID&gt;` { "id": "&lt;ulid&gt;", "username": "...", "email":
"...", "password_hash_algo": "argon2id", "password_hash": "base64...",
"created_at": "RFC3339", "updated_at": "RFC3339", "roles": ["admin", "user"],
"status": "active|disabled", "version": 1 }

Indexes:

- `idx:user:username:&lt;lowerUsername&gt;` → `&lt;userULID&gt;`
- `idx:user:email:&lt;lowerEmail&gt;` → `&lt;userULID&gt;`

### Session

Key: `sess:&lt;sessionULID&gt;` { "id": "&lt;ulid&gt;", "user_id":
"&lt;userULID&gt;", "created_at": "...", "expires_at": "...", "ip": "...",
"user_agent": "...", "revoked": false, "version": 1 }

Optional index: `idx:sess:user:&lt;userULID&gt;:&lt;sessionULID&gt;` → `1`

### User preferences

Per-key approach (fine-grained updates):

- `pref:&lt;userULID&gt;:&lt;prefKey&gt;` → raw JSON/string value

### Author

Key: `a:<authorULID>` { "id": "<ulid>", "name": "...", "normalized_name": "...",
"created_at": "...", "version": 1 }

Index: `idx:author:name:<normalizedName>` → `<authorULID>`

### Series

Key: `s:<seriesULID>` { "id": "<ulid>", "name": "...", "normalized_name": "...",
"author_id": "<authorULID>|null", "created_at": "...", "version": 1 }

Indexes:

- `idx:series:name:<normalizedName>` → `<seriesULID>`
- `idx:series:author:<authorULID>:<seriesULID>` → `1`

### Work (title-level logical grouping)

Key: `w:<workULID>` { "id": "<ulid>", "title": "...", "normalized_title": "...",
"author_id": "<authorULID>|null", "alt_titles": ["..."], "series_id":
"<seriesULID>|null", "created_at": "...", "updated_at": "...", "version": 1 }

Indexes:

- `idx:work:title:<normalizedTitle>:author:<authorULID|null>` → `<workULID>`

### Book (logical)

Key: `b:<bookULID>` { "id": "<ulid>", "title": "...", "normalized_title": "...",
"author_id": "<authorULID>|null", "series_id": "<seriesULID>|null",
"series_position": 1, "work_id": "<workULID>|null", "narrator": "...|null",
"edition": "unabridged|abridged|special|...|null", "language": "en|...|null",
"publisher": "...|null", "isbn10": "[0-9Xx]{10}|null", "isbn13":
"[0-9]{13}|null", "description": "...", "published_year": 0, "cover_asset_id":
"<assetULID>|null", "tags": ["..."], "created_at": "...", "updated_at": "...",
"version": 1 }

Indexes:

- `idx:book:author:<authorULID>:<bookULID>` → `1`
- `idx:book:series:<seriesULID>:<posPadded>:<bookULID>` → `1`
- `idx:book:title:<normalizedTitle>:<bookULID>` → `1`
- `idx:book:tag:<tagLower>:<bookULID>` → `1`
- `idx:book:work:<workULID>:<bookULID>` → `1`

### Book file segment (physical)

Key: `bf:<segmentULID>` { "id": "<ulid>", "book_id": "<bookULID>", "file_path":
"...", "format": "m4b|mp3|flac|...", "size_bytes": 0, "duration_seconds": 0,
"hash_sha256": "hex", "track_number": 1, "total_tracks": 10, "active": true,
"superseded_by": "<segmentULID>|null", "created_at": "...", "updated_at": "...",
"version": 1 }

Ordering index:

- `bfi:<bookULID>:<segmentOrderPadded>` → `<segmentULID>`

On merge multi-file → single-file:

- Create new `bf` record for merged file
- Mark old segments `active=false` and `superseded_by=<newSeg>`
- Migrate progress offsets (see Playback progress)

### Playlist

Key: `pl:<playlistULID>` { "id": "<ulid>", "name": "...", "user_id":
"<userULID>|null", "created_at": "...", "updated_at": "...", "version": 1 }

Index: `idx:playlist:user:<userULID>:<playlistULID>` → `1`

Playlist items (ordered):

- `pli:<playlistULID>:<positionPadded>` → `<bookULID>`

### Playback event (immutable)

Key: `playe:<userULID>:<bookULID>:<timestampULID>` { "user_id": "<userULID>",
"book_id": "<bookULID>", "segment_id": "<segmentULID>", "position_seconds": 0,
"event_type": "progress|start|pause|complete", "play_speed": 1.0, "created_at":
"...", "version": 1 }

### Playback progress (latest snapshot)

Key: `playp:<userULID>:<bookULID>` { "user_id": "<userULID>", "book_id":
"<bookULID>", "segment_id": "<segmentULID>", "position_seconds": 0,
"percent_complete": 0.0, "updated_at": "...", "version": 1 }

Durations mapping for offset conversion (merge help):

- Key: `b:duration_map:<bookULID>` { "segments": [ { "id": "<segmentULID>",
  "duration": 0, "active": true, "offset_start": 0 } ], "total_duration": 0,
  "version": 1 }

### Stats aggregates (derived)

- `stats:book:plays:<bookULID>` → integer
- `stats:user:listen_seconds:<userULID>` → integer
- `stats:book:listen_seconds:<bookULID>` → integer
- `stats:work:plays:<workULID>` → integer
- `stats:work:listen_seconds:<workULID>` → integer

### Operations and logs

Operation: `op:<operationULID>` { "id": "<ulid>", "type":
"scan|organize|transcode|merge", "status": "pending|running|completed|failed",
"started_at": "...", "completed_at": "...|null", "error": "...|null",
"progress": { "current": 0, "total": 0 }, "created_by": "<userULID>|system",
"version": 1 }

Log: `opl:<operationULID>:<seqPadded>` { "seq": 0, "timestamp": "...", "level":
"info|warn|error", "message": "...", "version": 1 }

Maintain `op:<operationULID>:next_seq` counter for log sequencing.

### Migrations

Record: `mig:<versionPadded>` → { "id": number, "applied_at": "...",
"description": "...", "duration_ms": number }

Current version: `meta:version` → number

## Query patterns

- Find user by username: `get(idx:user:username:<lower>)` → `userID`, then
  `get(u:<id>)`
- List series by author: scan `idx:series:author:<authorID>:`
- List books in series ordered: scan `idx:book:series:<seriesID>:`
- Segments for book: scan `bfi:<bookID>:` then fetch `bf:<segmentID>`
- Recent playback events: reverse-iterate `playe:<userID>:<bookID>:`
- Recent operations: scan `op:` (ULID provides time order)
- Aggregate plays by work: read `stats:work:plays:<workULID>`; if missing, sum
  `stats:book:plays` for all `idx:book:work:<workULID>:` entries (lazy
  backfill).


## Dedup / Embedding store (within the main audiobooks.pebble DB)

The dedup and embedding data lives in the same `audiobooks.pebble` PebbleDB as
the main book store. `EmbeddingStore` receives a `*pebble.DB` reference from the
main store via `NewEmbeddingStore(db *pebble.DB)` with `owned: false`, so no
separate file or process is opened. Keyspace isolation is achieved purely by
prefix: `emb:` for embeddings, `dedup:` for candidates and labels.

### Embedding keyspace

| Key pattern | Value | Notes |
|-------------|-------|-------|
| `emb:v:<entityType>:<entityID>` | `embRec` JSON | Embedding vector record |
| `emb:c:<model>:<textHash>` | raw float32 blob | Embedding cache (no JSON overhead) |

### Dedup candidate keyspace

| Key pattern | Value | Notes |
|-------------|-------|-------|
| `dedup:r:<id16hex>` | `DedupCandidate` JSON | Candidate record; primary row |
| `dedup:p:<type>:<aID>:<bID>` | `<id16hex>` | Pair uniqueness index (prevents re-emission) |
| `dedup:seq` | `[8]byte` little-endian int64 | Auto-increment counter for candidate IDs |

### Labeled dataset keyspace

| Key pattern | Value | Notes |
|-------------|-------|-------|
| `dedup:label:<id16hex>` | `LabeledExample` JSON | One labeled (or unlabeled) candidate pair with feature snapshot |

`<id16hex>` is `fmt.Sprintf("%016x", uint64(candidateID))` — zero-padded
16-char lowercase hex encoding the candidate's int64 ID. Zero-padding ensures
fixed-width keys so prefix scans return rows in stable integer order.

`LabeledExample` key fields: `candidate_id`, `entity_a_id`, `entity_b_id`,
`layer`, `band`, `score`, `score_breakdown`; `a`/`b` `BookFeatures` snapshots
(title, author, primary_path, total_duration_sec, file_count, has_cover,
files_exist, recording_ids, itunes_pid_present, whole_book_sig_present);
`duration_ratio`, `folder_relation`, `shares_recording_id`,
`signature_relation`; `label`, `label_source`, `label_reason`, `decided_at`,
`formula_version`. Empty `label` = unlabeled, features only.

## Write patterns & atomicity

- Use Pebble batches for atomic multi-key writes (entity + indices)
- Idempotent creation by checking conflict indices first
- Prefer write primary key first, indices next (or within same batch)

## Security

- Password hashing: Argon2id; parameters in `meta:auth:argon2_params`
- Sessions: store only hashed secret/token (optional); expire via `expires_at`
- Sweeper job to delete expired `sess:` keys periodically

## TTL / Compaction

- Playback events may be pruned after aggregation (keep last N days or last N
  events)
- Compaction job updates `stats:` aggregates before deleting old `playe:` keys

## Migration strategy

On startup:

1. Read `meta:version` (initialize to 0 if missing)
2. Apply code-based migrations sequentially (add indices, backfill maps)
3. Write `mig:&lt;version&gt;` records and bump `meta:version`

## Multi-file → single-file merge procedure

1. Create new merged segment `bf:<newSeg>`
2. Compute segment cumulative offsets from `b:duration_map:<bookID>`
3. For each `playp:<userID>:<bookID>` referencing old segments:
   - `newPosition = oldSegmentOffsetStart + oldPosition`
   - Update snapshot to
     `{ segment_id: <newSeg>, position_seconds: newPosition }`
4. Mark old segments `active=false` and set `superseded_by=<newSeg>`
5. Append `opl:` entries to document the change

## Future extensions

- Cover assets: `asset:<assetULID>` records referencing filesystem paths and
  mime
- Full-text search: external engine (Bleve/Meilisearch) fed by change log
- Multi-tenant prefixing: `tenant:<tenantID>:` prepend to all keys
- Encryption-at-rest: selective field-level encryption in JSON values
