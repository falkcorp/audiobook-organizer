#!/usr/bin/env python3
# file: scripts/gen_pipeline_bot_tasks.py
# version: 1.0.1
# guid: 2e2a3779-57b4-4f8c-8c09-7b94bb1a03a2
# last-edited: 2026-10-20

"""Generate the 23 bot-task files for the dedup pipeline spec.

Run from repo root: python3 scripts/gen_pipeline_bot_tasks.py
"""

from __future__ import annotations

import datetime
import os
import uuid

OUT = os.path.join(
    os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
    "docs",
    "superpowers",
    "bot-tasks",
)
os.makedirs(OUT, exist_ok=True)
DATE = "2026-05-02"
SPEC = "docs/superpowers/specs/2026-05-02-audiobook-identification-dedup-pipeline.md"

# (task_id, slug, title, prereqs, files_summary, body_md)
TASKS = []


def add(task_id: str, slug: str, title: str, prereqs: list[str], body: str) -> None:
    TASKS.append((task_id, slug, title, prereqs, body))


def prereq_block(prereqs: list[str]) -> str:
    if not prereqs:
        return "_None — first task in the chain._\n"
    lines = []
    for p in prereqs:
        lines.append(f"- `task:{p}` — must be merged before this task starts")
    lines.append("")
    lines.append("```bash")
    for p in prereqs:
        lines.append(
            f"count=$(gh pr list --label \"task:{p}\" --state merged --json number | jq 'length')\n"
            f'[ "$count" -gt 0 ] || {{ echo "UNMET: task:{p}"; exit 0; }}'
        )
    lines.append("```")
    return "\n".join(lines) + "\n"


def render(task_id: str, slug: str, title: str, prereqs: list[str], body: str) -> str:
    file_rel = f"docs/superpowers/bot-tasks/{DATE}-dedup-pipeline-{task_id.lower()}-{slug}.md"
    fresh = str(uuid.uuid4())
    branch = f"feat/dedup-pipeline-{task_id.lower()}-{slug}"
    label = f"task:PIPE-{task_id}"
    return f"""<!-- file: {file_rel} -->
<!-- version: 1.0.0 -->
<!-- guid: {fresh} -->
<!-- last-edited: {DATE} -->

# BOT TASK: {title}

**Pipeline phase:** {task_id.split("-")[0]}
**Audience:** burndown bot
**Master spec:** [`{SPEC}`]({os.path.relpath(SPEC, "docs/superpowers/bot-tasks")})

## Prerequisites

{prereq_block(prereqs)}

## Branch

```
{branch}
```

## Label

```bash
gh label create "{label}" --color "1d76db" --description "Bot task: {title}" 2>/dev/null || true
```

{body}

## PR Instructions

```bash
gh pr create \\
  --title "feat(dedup): {title.lower()} (PIPE-{task_id})" \\
  --body "Implements PIPE-{task_id} from {SPEC}. See task file for details." \\
  --label "{label}"
```
"""


# ─────────────────────────────────────────────────────────────────────────────
# PHASE 0 — schema + stores
# ─────────────────────────────────────────────────────────────────────────────

add(
    "P0-01",
    "fingerprint-schema-and-migrations",
    "Create fingerprints.db schema + migration runner",
    [],
    """## What This Does

Creates a new SQLite sidecar `fingerprints.db` that holds the forever-store of
audio file fingerprints. This is intentionally *separate* from the operational
database so destructive maintenance jobs cannot touch it.

The schema includes three tables: `fingerprint_files`, `fingerprint_aliases`,
and `fingerprint_match_log`. See spec §3.2 for column rationale.

## Files to Create / Edit

1. **Create** `internal/database/fingerprint_db.go` — `OpenFingerprintDB` constructor + migration runner
2. **Create** `internal/database/fingerprint_db_test.go` — schema round-trip test
3. **Edit** `internal/config/config.go` — add `FingerprintDBPath string` field with default `data/fingerprints.db`

## Step 1 — Create the DB module

```go
// file: internal/database/fingerprint_db.go
// version: 1.0.0
// guid: <fresh-uuid>
// last-edited: 2026-05-02

package database

import (
    "database/sql"
    "fmt"

    _ "github.com/mattn/go-sqlite3"
)

// OpenFingerprintDB opens (and migrates) the forever-store SQLite database
// that records every (sha256, chromaprint, acoustid) tuple this server has
// ever seen — even for files that have since been deleted from the library.
func OpenFingerprintDB(path string) (*sql.DB, error) {
    db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_synchronous=NORMAL")
    if err != nil {
        return nil, fmt.Errorf("open fingerprints.db: %w", err)
    }
    if err := migrateFingerprintDB(db); err != nil {
        _ = db.Close()
        return nil, err
    }
    return db, nil
}

func migrateFingerprintDB(db *sql.DB) error {
    stmts := []string{
        `CREATE TABLE IF NOT EXISTS fingerprint_files (
            sha256              TEXT PRIMARY KEY,
            size_bytes          INTEGER NOT NULL,
            container_format    TEXT,
            audio_codec         TEXT,
            duration_seconds    REAL,
            stream_content_hash TEXT,
            chromaprint_full    TEXT,
            chromaprint_intro   TEXT,
            chromaprint_outro   TEXT,
            chromaprint_body    TEXT,
            acoustid_mbid       TEXT,
            acoustid_score      REAL,
            first_filename      TEXT NOT NULL,
            first_path          TEXT NOT NULL,
            first_seen_at       DATETIME NOT NULL,
            last_seen_at        DATETIME NOT NULL,
            deleted_at          DATETIME,
            deletion_history    TEXT NOT NULL DEFAULT '[]',
            schema_version      INTEGER NOT NULL DEFAULT 1
        )`,
        `CREATE INDEX IF NOT EXISTS idx_fp_chromaprint_full ON fingerprint_files(chromaprint_full)`,
        `CREATE INDEX IF NOT EXISTS idx_fp_chromaprint_intro ON fingerprint_files(chromaprint_intro)`,
        `CREATE INDEX IF NOT EXISTS idx_fp_acoustid ON fingerprint_files(acoustid_mbid)`,
        `CREATE INDEX IF NOT EXISTS idx_fp_deleted_at ON fingerprint_files(deleted_at)`,
        `CREATE TABLE IF NOT EXISTS fingerprint_aliases (
            sha256   TEXT NOT NULL,
            seen_at  DATETIME NOT NULL,
            filename TEXT NOT NULL,
            path     TEXT NOT NULL,
            book_id  TEXT,
            PRIMARY KEY (sha256, seen_at, path)
        )`,
        `CREATE TABLE IF NOT EXISTS fingerprint_match_log (
            id              INTEGER PRIMARY KEY AUTOINCREMENT,
            incoming_sha256 TEXT NOT NULL,
            matched_sha256  TEXT NOT NULL,
            signal_kind     TEXT NOT NULL,
            distance        REAL,
            matched_at      DATETIME NOT NULL,
            decision        TEXT
        )`,
    }
    for _, s := range stmts {
        if _, err := db.Exec(s); err != nil {
            return fmt.Errorf("fingerprints migration failed: %w\\nSQL: %s", err, s)
        }
    }
    return nil
}
```

## Step 2 — Test

```go
// file: internal/database/fingerprint_db_test.go
// version: 1.0.0
// guid: <fresh-uuid>

package database

import (
    "path/filepath"
    "testing"

    "github.com/stretchr/testify/require"
)

func TestOpenFingerprintDB_Migrations(t *testing.T) {
    db, err := OpenFingerprintDB(filepath.Join(t.TempDir(), "fp.db"))
    require.NoError(t, err)
    defer db.Close()

    for _, table := range []string{"fingerprint_files", "fingerprint_aliases", "fingerprint_match_log"} {
        var name string
        err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
        require.NoError(t, err, "table %s missing", table)
    }
}

func TestOpenFingerprintDB_Idempotent(t *testing.T) {
    p := filepath.Join(t.TempDir(), "fp.db")
    for i := 0; i < 3; i++ {
        db, err := OpenFingerprintDB(p)
        require.NoError(t, err)
        require.NoError(t, db.Close())
    }
}
```

## Step 3 — Config field

In `internal/config/config.go`, add to the main `Config` struct:

```go
FingerprintDBPath string `yaml:"fingerprint_db_path" json:"fingerprint_db_path"`
```

In the defaults helper (search for `DefaultConfig` or where other DB paths are
defaulted), add:

```go
if cfg.FingerprintDBPath == "" {
    cfg.FingerprintDBPath = "data/fingerprints.db"
}
```

## Verify

```bash
go test ./internal/database/ -run TestOpenFingerprintDB
go vet ./internal/database/...
```

## Definition of Done

- [ ] `OpenFingerprintDB` opens an empty path and creates all three tables
- [ ] Re-opening the same path is a no-op (idempotent)
- [ ] `FingerprintDBPath` config field exists with default `data/fingerprints.db`
- [ ] `make build-api` succeeds
- [ ] Tests pass
""",
)

add(
    "P0-02",
    "fingerprint-store-iface",
    "Define FingerprintStore interface + record types",
    ["P0-01"],
    """## What This Does

Defines the public `FingerprintStore` interface (spec §3.3) and the supporting
record types. No implementation yet — that lands in P0-03. Splitting the
interface from the impl lets P1-* signal stages depend on the interface alone
and be unblocked as soon as P0-02 merges.

## Files to Create / Edit

1. **Create** `internal/database/fingerprint_store.go`

## Step 1 — Interface + types

```go
// file: internal/database/fingerprint_store.go
// version: 1.0.0
// guid: <fresh-uuid>
// last-edited: 2026-05-02

package database

import "time"

// FingerprintRecord is one row of the forever-store. Mirrors the
// fingerprint_files SQLite table; see spec §3.2.
type FingerprintRecord struct {
    SHA256             string
    SizeBytes          int64
    ContainerFormat    string
    AudioCodec         string
    DurationSeconds    float64
    StreamContentHash  string
    ChromaprintFull    string
    ChromaprintIntro   string
    ChromaprintOutro   string
    ChromaprintBody    []string // JSON-encoded in storage
    AcoustIDMBID       string
    AcoustIDScore      float64
    FirstFilename      string
    FirstPath          string
    FirstSeenAt        time.Time
    LastSeenAt         time.Time
    DeletedAt          *time.Time
    DeletionHistory    []DeletionEvent
}

// DeletionEvent records a single deletion (or restoration) of the file
// associated with a SHA. Append-only.
type DeletionEvent struct {
    Timestamp time.Time `json:"ts"`
    Reason    string    `json:"reason"`
    User      string    `json:"user"`
    Action    string    `json:"action"` // "deleted" | "resurrected"
}

// FingerprintMatchLogEntry records that an incoming file matched an existing
// forever-store record. Used for forensic auditing of auto-merge decisions.
type FingerprintMatchLogEntry struct {
    IncomingSHA256 string
    MatchedSHA256  string
    SignalKind     string
    Distance       float64
    MatchedAt      time.Time
    Decision       string
}

// FingerprintStoreStats are the aggregate counters surfaced on the
// identification UI.
type FingerprintStoreStats struct {
    TotalFiles        int64
    LiveFiles         int64
    DeletedFiles      int64
    WithChromaprint   int64
    WithAcoustID      int64
    LastIngestAt      time.Time
}

// FingerprintStore is the forever-store interface. Implementations MUST be
// safe for concurrent use. Implementations MUST NOT physically delete rows;
// MarkDeleted only annotates.
type FingerprintStore interface {
    LookupBySHA(sha string) (*FingerprintRecord, error)
    LookupByChromaprintFull(fp string, minSimilarity float64) ([]FingerprintRecord, error)
    LookupByChromaprintSegment(seg string, minSimilarity float64) ([]FingerprintRecord, error)
    LookupByAcoustID(mbid string) ([]FingerprintRecord, error)
    Upsert(r FingerprintRecord) error
    AddAlias(sha, filename, path string, bookID *string) error
    MarkDeleted(sha, reason, user string) error
    Resurrect(sha string) error
    LogMatch(entry FingerprintMatchLogEntry) error
    Stats() (FingerprintStoreStats, error)
}
```

## Verify

```bash
go vet ./internal/database/...
go build ./...
```

## Definition of Done

- [ ] Interface compiles
- [ ] No new code paths actually use the interface yet (impl arrives in P0-03)
- [ ] `go build ./...` succeeds
""",
)

add(
    "P0-03",
    "fingerprint-store-sqlite-impl",
    "SQLite implementation of FingerprintStore",
    ["P0-01", "P0-02"],
    """## What This Does

Implements `FingerprintStore` against the `fingerprints.db` SQLite database
created in P0-01. Includes the chromaprint similarity matcher (Hamming distance
on base64-decoded segments — already implemented for `internal/fingerprint`,
*reuse* don't reimplement: see `internal/fingerprint/fpcalc.go` and the
`FuzzyMinSimilarity` constant).

## Files to Create / Edit

1. **Create** `internal/database/fingerprint_store_sqlite.go`
2. **Create** `internal/database/fingerprint_store_sqlite_test.go`

## Step 1 — Implementation skeleton

```go
// file: internal/database/fingerprint_store_sqlite.go
// version: 1.0.0
// guid: <fresh-uuid>
// last-edited: 2026-05-02

package database

import (
    "database/sql"
    "encoding/json"
    "fmt"
    "sync"
    "time"

    "github.com/jdfalk/audiobook-organizer/internal/fingerprint"
)

type sqliteFingerprintStore struct {
    db *sql.DB
    mu sync.RWMutex
}

// NewSQLiteFingerprintStore wraps an open *sql.DB (created via
// OpenFingerprintDB) as a FingerprintStore.
func NewSQLiteFingerprintStore(db *sql.DB) FingerprintStore {
    return &sqliteFingerprintStore{db: db}
}

func (s *sqliteFingerprintStore) LookupBySHA(sha string) (*FingerprintRecord, error) {
    row := s.db.QueryRow(`SELECT sha256, size_bytes, container_format, audio_codec,
        duration_seconds, stream_content_hash, chromaprint_full, chromaprint_intro,
        chromaprint_outro, chromaprint_body, acoustid_mbid, acoustid_score,
        first_filename, first_path, first_seen_at, last_seen_at, deleted_at,
        deletion_history FROM fingerprint_files WHERE sha256 = ?`, sha)
    return scanFingerprintRow(row)
}

func (s *sqliteFingerprintStore) Upsert(r FingerprintRecord) error {
    bodyJSON, _ := json.Marshal(r.ChromaprintBody)
    histJSON, _ := json.Marshal(r.DeletionHistory)
    _, err := s.db.Exec(`INSERT INTO fingerprint_files
        (sha256, size_bytes, container_format, audio_codec, duration_seconds,
         stream_content_hash, chromaprint_full, chromaprint_intro,
         chromaprint_outro, chromaprint_body, acoustid_mbid, acoustid_score,
         first_filename, first_path, first_seen_at, last_seen_at,
         deleted_at, deletion_history)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
        ON CONFLICT(sha256) DO UPDATE SET
          size_bytes=excluded.size_bytes,
          container_format=COALESCE(excluded.container_format, container_format),
          audio_codec=COALESCE(excluded.audio_codec, audio_codec),
          duration_seconds=COALESCE(excluded.duration_seconds, duration_seconds),
          stream_content_hash=COALESCE(excluded.stream_content_hash, stream_content_hash),
          chromaprint_full=COALESCE(excluded.chromaprint_full, chromaprint_full),
          chromaprint_intro=COALESCE(excluded.chromaprint_intro, chromaprint_intro),
          chromaprint_outro=COALESCE(excluded.chromaprint_outro, chromaprint_outro),
          chromaprint_body=COALESCE(excluded.chromaprint_body, chromaprint_body),
          acoustid_mbid=COALESCE(excluded.acoustid_mbid, acoustid_mbid),
          acoustid_score=COALESCE(excluded.acoustid_score, acoustid_score),
          last_seen_at=excluded.last_seen_at`,
        r.SHA256, r.SizeBytes, r.ContainerFormat, r.AudioCodec, r.DurationSeconds,
        r.StreamContentHash, r.ChromaprintFull, r.ChromaprintIntro, r.ChromaprintOutro,
        bodyJSON, r.AcoustIDMBID, r.AcoustIDScore, r.FirstFilename, r.FirstPath,
        r.FirstSeenAt, r.LastSeenAt, r.DeletedAt, histJSON)
    return err
}

func (s *sqliteFingerprintStore) AddAlias(sha, filename, path string, bookID *string) error {
    _, err := s.db.Exec(`INSERT OR IGNORE INTO fingerprint_aliases
        (sha256, seen_at, filename, path, book_id) VALUES (?,?,?,?,?)`,
        sha, time.Now().UTC(), filename, path, bookID)
    return err
}

func (s *sqliteFingerprintStore) MarkDeleted(sha, reason, user string) error {
    return s.appendHistory(sha, DeletionEvent{Timestamp: time.Now().UTC(), Reason: reason, User: user, Action: "deleted"}, true)
}

func (s *sqliteFingerprintStore) Resurrect(sha string) error {
    return s.appendHistory(sha, DeletionEvent{Timestamp: time.Now().UTC(), Action: "resurrected"}, false)
}

func (s *sqliteFingerprintStore) appendHistory(sha string, ev DeletionEvent, markDeleted bool) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    var raw string
    if err := s.db.QueryRow(`SELECT deletion_history FROM fingerprint_files WHERE sha256=?`, sha).Scan(&raw); err != nil {
        return err
    }
    var hist []DeletionEvent
    _ = json.Unmarshal([]byte(raw), &hist)
    hist = append(hist, ev)
    out, _ := json.Marshal(hist)
    if markDeleted {
        _, err := s.db.Exec(`UPDATE fingerprint_files SET deletion_history=?, deleted_at=? WHERE sha256=?`, out, ev.Timestamp, sha)
        return err
    }
    _, err := s.db.Exec(`UPDATE fingerprint_files SET deletion_history=?, deleted_at=NULL WHERE sha256=?`, out, sha)
    return err
}

func (s *sqliteFingerprintStore) LogMatch(e FingerprintMatchLogEntry) error {
    _, err := s.db.Exec(`INSERT INTO fingerprint_match_log
        (incoming_sha256, matched_sha256, signal_kind, distance, matched_at, decision)
        VALUES (?,?,?,?,?,?)`,
        e.IncomingSHA256, e.MatchedSHA256, e.SignalKind, e.Distance, e.MatchedAt, e.Decision)
    return err
}

func (s *sqliteFingerprintStore) LookupByAcoustID(mbid string) ([]FingerprintRecord, error) {
    rows, err := s.db.Query(`SELECT sha256, size_bytes, container_format, audio_codec,
        duration_seconds, stream_content_hash, chromaprint_full, chromaprint_intro,
        chromaprint_outro, chromaprint_body, acoustid_mbid, acoustid_score,
        first_filename, first_path, first_seen_at, last_seen_at, deleted_at,
        deletion_history FROM fingerprint_files WHERE acoustid_mbid = ?`, mbid)
    if err != nil { return nil, err }
    defer rows.Close()
    return scanFingerprintRows(rows)
}

func (s *sqliteFingerprintStore) LookupByChromaprintFull(fp string, minSim float64) ([]FingerprintRecord, error) {
    return s.fuzzyChromaprint("chromaprint_full", fp, minSim)
}

func (s *sqliteFingerprintStore) LookupByChromaprintSegment(seg string, minSim float64) ([]FingerprintRecord, error) {
    // NOTE: segment can live in chromaprint_intro, chromaprint_outro, or any
    // body slot. Naive scan; for >100k rows replace with min-hash banding.
    return s.fuzzyChromaprint("chromaprint_intro", seg, minSim)
}

func (s *sqliteFingerprintStore) fuzzyChromaprint(col, fp string, minSim float64) ([]FingerprintRecord, error) {
    rows, err := s.db.Query(fmt.Sprintf(`SELECT sha256, size_bytes, container_format, audio_codec,
        duration_seconds, stream_content_hash, chromaprint_full, chromaprint_intro,
        chromaprint_outro, chromaprint_body, acoustid_mbid, acoustid_score,
        first_filename, first_path, first_seen_at, last_seen_at, deleted_at,
        deletion_history FROM fingerprint_files WHERE %s IS NOT NULL`, col))
    if err != nil { return nil, err }
    defer rows.Close()
    all, err := scanFingerprintRows(rows)
    if err != nil { return nil, err }
    out := make([]FingerprintRecord, 0)
    for _, r := range all {
        candidate := r.ChromaprintFull
        if col == "chromaprint_intro" { candidate = r.ChromaprintIntro }
        sim := fingerprint.Similarity(fp, candidate)
        if sim >= minSim { out = append(out, r) }
    }
    return out, nil
}

func (s *sqliteFingerprintStore) Stats() (FingerprintStoreStats, error) {
    var st FingerprintStoreStats
    _ = s.db.QueryRow(`SELECT COUNT(*),
        SUM(CASE WHEN deleted_at IS NULL THEN 1 ELSE 0 END),
        SUM(CASE WHEN deleted_at IS NOT NULL THEN 1 ELSE 0 END),
        SUM(CASE WHEN chromaprint_full IS NOT NULL THEN 1 ELSE 0 END),
        SUM(CASE WHEN acoustid_mbid IS NOT NULL THEN 1 ELSE 0 END),
        COALESCE(MAX(last_seen_at), '1970-01-01') FROM fingerprint_files`).Scan(
        &st.TotalFiles, &st.LiveFiles, &st.DeletedFiles, &st.WithChromaprint,
        &st.WithAcoustID, &st.LastIngestAt)
    return st, nil
}

// helpers ---------------------------------------------------------

func scanFingerprintRow(row *sql.Row) (*FingerprintRecord, error) {
    var r FingerprintRecord
    var bodyJSON, histJSON string
    var deletedAt sql.NullTime
    err := row.Scan(&r.SHA256, &r.SizeBytes, &r.ContainerFormat, &r.AudioCodec,
        &r.DurationSeconds, &r.StreamContentHash, &r.ChromaprintFull, &r.ChromaprintIntro,
        &r.ChromaprintOutro, &bodyJSON, &r.AcoustIDMBID, &r.AcoustIDScore,
        &r.FirstFilename, &r.FirstPath, &r.FirstSeenAt, &r.LastSeenAt,
        &deletedAt, &histJSON)
    if err == sql.ErrNoRows { return nil, nil }
    if err != nil { return nil, err }
    if deletedAt.Valid { t := deletedAt.Time; r.DeletedAt = &t }
    _ = json.Unmarshal([]byte(bodyJSON), &r.ChromaprintBody)
    _ = json.Unmarshal([]byte(histJSON), &r.DeletionHistory)
    return &r, nil
}

func scanFingerprintRows(rows *sql.Rows) ([]FingerprintRecord, error) {
    out := make([]FingerprintRecord, 0)
    for rows.Next() {
        var r FingerprintRecord
        var bodyJSON, histJSON string
        var deletedAt sql.NullTime
        if err := rows.Scan(&r.SHA256, &r.SizeBytes, &r.ContainerFormat, &r.AudioCodec,
            &r.DurationSeconds, &r.StreamContentHash, &r.ChromaprintFull, &r.ChromaprintIntro,
            &r.ChromaprintOutro, &bodyJSON, &r.AcoustIDMBID, &r.AcoustIDScore,
            &r.FirstFilename, &r.FirstPath, &r.FirstSeenAt, &r.LastSeenAt,
            &deletedAt, &histJSON); err != nil { return nil, err }
        if deletedAt.Valid { t := deletedAt.Time; r.DeletedAt = &t }
        _ = json.Unmarshal([]byte(bodyJSON), &r.ChromaprintBody)
        _ = json.Unmarshal([]byte(histJSON), &r.DeletionHistory)
        out = append(out, r)
    }
    return out, rows.Err()
}
```

> **Note:** `fingerprint.Similarity(a,b)` is assumed to exist. If it does not,
> add it as a thin wrapper over the existing fuzzy-Hamming logic in
> `internal/fingerprint/fpcalc.go` (search for `FuzzyMinSimilarity` /
> `Hamming`). Keep that wrapper in scope — DO NOT reimplement Hamming here.

## Step 2 — Tests

```go
// file: internal/database/fingerprint_store_sqlite_test.go
// version: 1.0.0
// guid: <fresh-uuid>

package database

import (
    "path/filepath"
    "testing"

    "github.com/stretchr/testify/require"
)

func newFP(t *testing.T) FingerprintStore {
    db, err := OpenFingerprintDB(filepath.Join(t.TempDir(), "fp.db"))
    require.NoError(t, err)
    t.Cleanup(func() { _ = db.Close() })
    return NewSQLiteFingerprintStore(db)
}

func TestFingerprintStore_UpsertLookup(t *testing.T) {
    store := newFP(t)
    rec := FingerprintRecord{SHA256: "a"+stringRepeat("0",63), SizeBytes: 100,
        FirstFilename: "x.m4b", FirstPath: "/x/x.m4b"}
    require.NoError(t, store.Upsert(rec))
    got, err := store.LookupBySHA(rec.SHA256)
    require.NoError(t, err)
    require.NotNil(t, got)
    require.Equal(t, int64(100), got.SizeBytes)
}

func TestFingerprintStore_MarkDeletedAppendsHistoryAndResurrect(t *testing.T) {
    store := newFP(t)
    sha := "b"+stringRepeat("0",63)
    require.NoError(t, store.Upsert(FingerprintRecord{SHA256: sha, FirstFilename:"x", FirstPath:"x"}))
    require.NoError(t, store.MarkDeleted(sha, "user delete", "alice"))
    got, _ := store.LookupBySHA(sha)
    require.NotNil(t, got.DeletedAt)
    require.Len(t, got.DeletionHistory, 1)
    require.NoError(t, store.Resurrect(sha))
    got, _ = store.LookupBySHA(sha)
    require.Nil(t, got.DeletedAt)
    require.Len(t, got.DeletionHistory, 2)
}

func stringRepeat(s string, n int) string { out := ""; for i := 0; i < n; i++ { out += s }; return out }
```

## Verify

```bash
go test ./internal/database/ -run TestFingerprintStore -count=1
```

## Definition of Done

- [ ] All `FingerprintStore` methods implemented
- [ ] MarkDeleted preserves the row, appends to history JSON, sets `deleted_at`
- [ ] Resurrect clears `deleted_at` and appends a `resurrected` event
- [ ] Tests pass
- [ ] No new top-level `internal/fingerprint` similarity helper if one exists; reuse
""",
)

add(
    "P0-04",
    "signal-store-schema-and-impl",
    "Create signal_store table + Go store",
    ["P0-01"],
    """## What This Does

Adds the `signal_store` table to the **main** SQLite database (not the
forever-store) and a thin Go API over it. Signals are per-`(book_id, kind)`
records emitted by every Phase 1 stage.

## Files to Create / Edit

1. **Edit** `internal/database/migrations.go` — add new `CREATE TABLE` near
   the other dedup-related tables (search for `dedup_candidates`).
2. **Create** `internal/dedup/signals/kind.go`
3. **Create** `internal/dedup/signals/store.go`
4. **Create** `internal/dedup/signals/store_test.go`

## Step 1 — Migration

Add this block inside `migrateSchema` (or the closest equivalent) in
`internal/database/migrations.go`. Use `CREATE TABLE IF NOT EXISTS` so it is
safe to run twice.

```go
`CREATE TABLE IF NOT EXISTS dedup_signals (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    book_id       TEXT NOT NULL,
    file_id       TEXT,
    kind          TEXT NOT NULL,
    value         TEXT NOT NULL,
    score         REAL NOT NULL,
    confidence    REAL NOT NULL,
    source        TEXT NOT NULL,
    evidence_json TEXT,
    computed_at   DATETIME NOT NULL,
    expires_at    DATETIME
)`,
`CREATE INDEX IF NOT EXISTS idx_dedup_signals_book ON dedup_signals(book_id)`,
`CREATE INDEX IF NOT EXISTS idx_dedup_signals_kind_value ON dedup_signals(kind, value)`,
```

## Step 2 — Constants

```go
// file: internal/dedup/signals/kind.go
// version: 1.0.0
// guid: <fresh-uuid>

package signals

// Kind identifies a class of evidence the dedup matrix consumes. Add a new
// constant here whenever a new pipeline stage is introduced. Persisted as a
// column value, so DO NOT renumber or rename existing constants.
type Kind string

const (
    KindSHAExact              Kind = "sha_exact"
    KindStreamContentHash     Kind = "stream_content_hash"
    KindChromaprintFull       Kind = "chromaprint_full"
    KindChromaprintSegment    Kind = "chromaprint_segment"
    KindAcoustIDMatch         Kind = "acoustid_match"
    KindTagMatch              Kind = "tag_match"
    KindFilenameMatch         Kind = "filename_match"
    KindEmbeddingSimilarity   Kind = "embedding_similarity"
    KindWhisperIntroMatch     Kind = "whisper_intro_match"
    KindWhisperIntroNegative  Kind = "whisper_intro_negative"
    KindForeverStoreResurrect Kind = "forever_store_resurrect"
)

// Weight returns the matrix weight applied to this signal. Source: master spec
// §4.1. If you change a value, you MUST also update the spec and bump the
// signal_revision column on books so the matrix re-runs.
func Weight(k Kind) float64 {
    switch k {
    case KindSHAExact, KindForeverStoreResurrect: return 1.00
    case KindStreamContentHash: return 0.90
    case KindChromaprintFull: return 0.70
    case KindChromaprintSegment: return 0.50
    case KindAcoustIDMatch: return 0.55
    case KindWhisperIntroMatch: return 0.45
    case KindTagMatch: return 0.30
    case KindEmbeddingSimilarity: return 0.25
    case KindFilenameMatch: return 0.15
    case KindWhisperIntroNegative: return -0.20
    }
    return 0
}
```

## Step 3 — Store

```go
// file: internal/dedup/signals/store.go
// version: 1.0.0
// guid: <fresh-uuid>

package signals

import (
    "database/sql"
    "time"
)

type Signal struct {
    ID           int64
    BookID       string
    FileID       string
    Kind         Kind
    Value        string
    Score        float64
    Confidence   float64
    Source       string
    EvidenceJSON []byte
    ComputedAt   time.Time
    ExpiresAt    *time.Time
}

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Insert(sig Signal) error {
    _, err := s.db.Exec(`INSERT INTO dedup_signals
        (book_id, file_id, kind, value, score, confidence, source, evidence_json, computed_at, expires_at)
        VALUES (?,?,?,?,?,?,?,?,?,?)`,
        sig.BookID, sig.FileID, string(sig.Kind), sig.Value, sig.Score, sig.Confidence,
        sig.Source, sig.EvidenceJSON, sig.ComputedAt, sig.ExpiresAt)
    return err
}

func (s *Store) ListByBook(bookID string) ([]Signal, error) {
    rows, err := s.db.Query(`SELECT id, book_id, COALESCE(file_id,''), kind, value, score, confidence,
        source, COALESCE(evidence_json,''), computed_at, expires_at FROM dedup_signals
        WHERE book_id = ? ORDER BY computed_at DESC`, bookID)
    if err != nil { return nil, err }
    defer rows.Close()
    out := make([]Signal, 0)
    for rows.Next() {
        var sig Signal
        var ev string
        var exp sql.NullTime
        if err := rows.Scan(&sig.ID, &sig.BookID, &sig.FileID, &sig.Kind, &sig.Value,
            &sig.Score, &sig.Confidence, &sig.Source, &ev, &sig.ComputedAt, &exp); err != nil {
            return nil, err
        }
        if ev != "" { sig.EvidenceJSON = []byte(ev) }
        if exp.Valid { t := exp.Time; sig.ExpiresAt = &t }
        out = append(out, sig)
    }
    return out, rows.Err()
}

func (s *Store) DeleteForBook(bookID string) error {
    _, err := s.db.Exec(`DELETE FROM dedup_signals WHERE book_id = ?`, bookID)
    return err
}
```

## Step 4 — Test

```go
// file: internal/dedup/signals/store_test.go
// version: 1.0.0
// guid: <fresh-uuid>

package signals

import (
    "database/sql"
    "testing"
    "time"

    _ "github.com/mattn/go-sqlite3"
    "github.com/stretchr/testify/require"
)

func newDB(t *testing.T) *sql.DB {
    db, err := sql.Open("sqlite3", ":memory:")
    require.NoError(t, err)
    _, err = db.Exec(`CREATE TABLE dedup_signals (
        id INTEGER PRIMARY KEY AUTOINCREMENT, book_id TEXT NOT NULL, file_id TEXT,
        kind TEXT NOT NULL, value TEXT NOT NULL, score REAL NOT NULL,
        confidence REAL NOT NULL, source TEXT NOT NULL, evidence_json TEXT,
        computed_at DATETIME NOT NULL, expires_at DATETIME)`)
    require.NoError(t, err)
    return db
}

func TestStore_InsertAndList(t *testing.T) {
    db := newDB(t)
    s := NewStore(db)
    require.NoError(t, s.Insert(Signal{BookID: "b1", Kind: KindSHAExact,
        Value: "abc", Score: 1.0, Confidence: 1.0, Source: "sha256-full",
        ComputedAt: time.Now()}))
    out, err := s.ListByBook("b1")
    require.NoError(t, err)
    require.Len(t, out, 1)
    require.Equal(t, KindSHAExact, out[0].Kind)
}

func TestWeight_KnownKinds(t *testing.T) {
    require.Equal(t, 1.00, Weight(KindSHAExact))
    require.Equal(t, -0.20, Weight(KindWhisperIntroNegative))
    require.Equal(t, 0.0, Weight(Kind("nonexistent")))
}
```

## Verify

```bash
go test ./internal/dedup/signals/... -count=1
go vet ./internal/dedup/signals/...
```

## Definition of Done

- [ ] Migration runs cleanly on a fresh DB
- [ ] All `Kind` constants documented and weighted to match spec §4.1
- [ ] Tests pass
""",
)

add(
    "P0-05",
    "identity-results-schema",
    "Add identity_results table to main DB",
    ["P0-04"],
    """## What This Does

Adds the `identity_results` table that stores the matrix output for every book.

## Files to Create / Edit

1. **Edit** `internal/database/migrations.go` — add the table

## Step 1 — Migration

Add inside `migrateSchema` near the dedup_signals migration:

```go
`CREATE TABLE IF NOT EXISTS identity_results (
    book_id          TEXT PRIMARY KEY,
    identity_score   REAL NOT NULL,
    signal_revision  INTEGER NOT NULL,
    decided_at       DATETIME NOT NULL,
    summary_json     TEXT NOT NULL
)`,
`ALTER TABLE books ADD COLUMN signal_revision INTEGER NOT NULL DEFAULT 0`,
```

> The `ALTER TABLE` will fail on second run; wrap with the existing
> "column-already-exists" tolerant helper in this file (search
> `addColumnIfMissing` or the closest equivalent — there is one used for the
> itunes columns).

## Step 2 — Test

```go
// add to internal/database/migrations_test.go (or a new file if missing)
func TestIdentityResultsMigration(t *testing.T) {
    db := newTestSQLiteDB(t) // existing helper
    _, err := db.Exec(`INSERT INTO identity_results (book_id, identity_score, signal_revision, decided_at, summary_json)
        VALUES ('b1', 0.92, 1, datetime('now'), '{}')`)
    require.NoError(t, err)
}
```

## Verify

```bash
go test ./internal/database/ -run TestIdentityResultsMigration
```

## Definition of Done

- [ ] Table created with all 5 columns
- [ ] `books.signal_revision` column added (idempotent)
- [ ] Test passes
""",
)

add(
    "P0-06",
    "match-groups-schema",
    "Add dedup_match_groups + members tables",
    ["P0-04"],
    """## What This Does

Adds `dedup_match_groups` and `dedup_match_group_members` tables (spec §6).
These supersede the legacy `dedup_candidates` table; that table is migrated
into this one in Phase 6.

## Files to Create / Edit

1. **Edit** `internal/database/migrations.go`

## Step 1 — Migration

```go
`CREATE TABLE IF NOT EXISTS dedup_match_groups (
    id              TEXT PRIMARY KEY,
    canonical_book  TEXT NOT NULL,
    strongest_kind  TEXT NOT NULL,
    strongest_score REAL NOT NULL,
    signal_summary  TEXT NOT NULL,
    state           TEXT NOT NULL,
    created_at      DATETIME NOT NULL,
    updated_at      DATETIME NOT NULL,
    decided_by      TEXT,
    decided_at      DATETIME
)`,
`CREATE INDEX IF NOT EXISTS idx_match_groups_state ON dedup_match_groups(state)`,
`CREATE TABLE IF NOT EXISTS dedup_match_group_members (
    group_id   TEXT NOT NULL,
    book_id    TEXT NOT NULL,
    pair_score REAL NOT NULL,
    role       TEXT NOT NULL,
    PRIMARY KEY (group_id, book_id)
)`,
`CREATE INDEX IF NOT EXISTS idx_match_group_members_book ON dedup_match_group_members(book_id)`,
```

## Definition of Done

- [ ] Both tables and both indexes created
- [ ] Idempotent on re-run
""",
)

# ─────────────────────────────────────────────────────────────────────────────
# PHASE 1 — signals (each independent after P0-04)
# ─────────────────────────────────────────────────────────────────────────────


def stage_task(task_id, slug, title, summary, payload, prereqs_extra=None):
    base_pre = ["P0-04"]
    if prereqs_extra:
        base_pre += prereqs_extra
    body = f"""## What This Does

{summary}

## Files to Create / Edit

1. **Create** `internal/maintenance/jobs/{slug.replace("-", "_")}.go`
2. **Create** `internal/maintenance/jobs/{slug.replace("-", "_")}_test.go`

## Implementation outline

{payload}

## Verify

```bash
go test ./internal/maintenance/jobs/... -run {slug.replace("-", "_").title().replace("_", "")}
go vet ./internal/maintenance/jobs/...
```

## Definition of Done

- [ ] Job registers itself via `init()` (`maintenance.Register(&Job{{}})`)
- [ ] Job emits exactly one `signals.Signal` per `(book_id, kind, value)` it sees
- [ ] Failure mode (see master spec §3.4) is handled — never panics, never blocks the pipeline
- [ ] Test asserts: (a) no signal on empty input, (b) correct signal on happy path, (c) graceful no-op on missing dependency
- [ ] `make build-api` succeeds
"""
    add(task_id, slug, title, base_pre, body)


stage_task(
    "P1-01",
    "stage-sha256-full",
    "Pipeline stage: full SHA-256 file hash",
    "Implements the mandatory SHA-256 stage. Reuses `scanner.ComputeFileHash` "
    "(`internal/scanner/scanner.go:1761`) — DO NOT reimplement chunking. "
    "Emits `KindSHAExact` with score=1.0, confidence=1.0. Also calls "
    "`fingerprintStore.Upsert` so the forever-store sees this SHA.",
    """- Inject `database.FingerprintStore` and `*signals.Store` via the
  job's `InjectStore`-style hook (extend `internal/maintenance/job.go` if
  needed; see existing `EnqueuerInjectable` pattern).
- For each book: compute SHA → `Upsert` into FingerprintStore →
  `Insert` a Signal{Kind: KindSHAExact, Value: sha, Score: 1.0, Confidence: 1.0,
  Source: "sha256-full"}.
- Failure: file unreadable → log warn, no signal emitted, pipeline continues.
- Failure: 50 GB file → already streamed by ComputeFileHash, no special handling needed.
""",
)

stage_task(
    "P1-02",
    "stage-stream-content-hash",
    "Pipeline stage: per-stream audio content hash",
    "Hashes only the audio stream payload (skipping container/metadata) so "
    "two files that are identical re-encodes still match. Uses ffmpeg "
    "(`ffmpeg -i in.m4b -map 0:a -c:a copy -f md5 -`).",
    """- Shell out to ffmpeg; parse the MD5 line.
- Emit `KindStreamContentHash` with score=1.0, confidence=1.0 when matched.
- Failure: ffmpeg missing → emit nothing; mark `stage_unavailable` metric.
""",
)

stage_task(
    "P1-03",
    "stage-chromaprint-segments",
    "Pipeline stage: chromaprint segments",
    "Calls `internal/fingerprint.FileSegments` (existing) to compute the 7 "
    "segments. Stores them on the FingerprintStore record AND emits one "
    "`KindChromaprintSegment` signal per segment plus one `KindChromaprintFull`.",
    """- Use `fingerprint.Available()` to gate the stage.
- For each segment: also call `fpStore.LookupByChromaprintSegment` and, if a
  match is found, emit a per-pair signal carrying the matched SHA in
  `EvidenceJSON`. The matrix uses these to build match groups.
- Failure: backend missing → stage marks itself unavailable; matrix degrades.
""",
)

stage_task(
    "P1-04",
    "stage-acoustid-lookup",
    "Pipeline stage: AcoustID external lookup",
    "Submits the chromaprint full fingerprint to acoustid.org and records "
    "the returned MBID + score. Throttled and retried via "
    "`internal/ai/aijobs` job runner.",
    """- Skip if no `chromaprint_full` exists for the book yet.
- Skip if `last_seen_at` < 24h (cache).
- Failure: HTTP 429 → exponential backoff, signal stays unemitted; matrix proceeds.
- Failure: HTTP 5xx > 3 retries → log warn, no signal emitted.
""",
    prereqs_extra=["P1-03"],
)

stage_task(
    "P1-05",
    "stage-tag-match",
    "Pipeline stage: tag-based pairwise comparison",
    "Walks each book's title/author/narrator/duration/track-count and "
    "emits `KindTagMatch` signals against any other book in the library that "
    "is plausibly a match. Reuses normalized comparators in "
    "`internal/dedup/helpers.go`.",
    """- Title comparator: existing normalized Levenshtein in
  `internal/dedup/helpers.go`.
- Duration must match within ±2 % (configurable threshold;
  `settings.dedup.duration_tolerance`).
- Score = weighted blend (title 0.5, author 0.25, narrator 0.15, duration 0.10).
- Confidence = 0.75 (constant).
""",
)

stage_task(
    "P1-06",
    "stage-filename-match",
    "Pipeline stage: filename / path heuristics",
    "Lowest-weight signal. Emits `KindFilenameMatch` for any pair of books "
    "whose normalized basenames or parent dir names exceed Jaccard 0.7.",
    """- Reuses `strings.ToLower` + non-alnum stripping logic already used in
  the legacy `dedup-books` job.
- Confidence = 0.40.
""",
)

stage_task(
    "P1-07",
    "stage-embedding-similarity",
    "Pipeline stage: chromem embedding similarity",
    "Reuses the existing `EmbeddingStore` / `ChromemEmbeddingStore` "
    "(`internal/database/embedding_store.go`, `chromem_embedding_store.go`). "
    "Emits one `KindEmbeddingSimilarity` per pair with cosine ≥ 0.80.",
    """- Lookup vector for the book; if missing, request a backfill via
  existing `embedding_backfill` machinery.
- Confidence = 0.65.
- Failure: chromem store unavailable → emit nothing.
""",
)

stage_task(
    "P1-08",
    "stage-whisper-intro",
    "Pipeline stage: Whisper transcription of first 2 minutes",
    "Extracts the first 120 s of audio with ffmpeg, sends to Whisper "
    "(OpenAI API by default; local whisper.cpp if `settings.whisper.local=true`), "
    'fuzzy-matches transcript against `"<title> by <author>"`. Only runs '
    "when identity_score from earlier stages is < 0.85 (gating happens in "
    "the coordinator P3-01; this stage assumes it's been called).",
    """- Use `ffmpeg -ss 0 -t 120 -i in -ar 16000 -ac 1 -c:a pcm_s16le out.wav`.
- Cache transcript on FingerprintStore (a new `whisper_intro_text` column may
  be added in a follow-up; for now stash inside `EvidenceJSON`).
- Match score: token-set ratio (use `github.com/agnivade/levenshtein` or the
  existing helper) of normalized transcript vs `"<title> by <author>"`.
- Score ≥ 0.55 → `KindWhisperIntroMatch`; score < 0.20 with
  duration ≥ 90s actually heard → `KindWhisperIntroNegative`.
- Failure: Whisper API down → no signal; coordinator caps identity_score at
  0.85 to surface "needs verification".
""",
)

# ─────────────────────────────────────────────────────────────────────────────
# PHASE 2 — fusion
# ─────────────────────────────────────────────────────────────────────────────

add(
    "P2-01",
    "decision-matrix-engine",
    "Decision matrix: identity_score + per-pair match_score",
    ["P0-05", "P1-01", "P1-02", "P1-03", "P1-04", "P1-05", "P1-06", "P1-07", "P1-08"],
    """## What This Does

Implements the pure-functional decision matrix described in spec §4. Inputs are
a slice of `signals.Signal`; outputs are `IdentityResult` and a list of
`MatchPair` (book_a, book_b, kind, score).

## Files to Create / Edit

1. **Create** `internal/dedup/matrix/matrix.go`
2. **Create** `internal/dedup/matrix/matrix_test.go`

## Implementation outline

```go
package matrix

import "github.com/jdfalk/audiobook-organizer/internal/dedup/signals"

type IdentityResult struct {
    BookID         string
    IdentityScore  float64
    Contributions  []Contribution // for UI explanations
}
type Contribution struct {
    Kind       signals.Kind
    Score      float64
    Confidence float64
    Weight     float64
    Product    float64
}
type MatchPair struct {
    BookA, BookB string
    Kind         signals.Kind
    Score        float64
}

// ComputeIdentity returns the identity score for one book.
func ComputeIdentity(sigs []signals.Signal) IdentityResult { ... }

// ComputeMatchPairs returns the inferred duplicate pairs from a flat signal list.
func ComputeMatchPairs(sigs []signals.Signal) []MatchPair { ... }
```

Use a soft clamp `min(1.0, max(0.0, x))`. The match score uses **max** (not sum)
across signal kinds — see spec §4.2.

## Tests must cover

- SHA exact alone → identity 1.0
- Tag match alone → identity 0.225 (0.30·1.0·0.75)
- Whisper negative + tag match → identity below tag-match-only (negative weight applied)
- match_score uses max, not sum (two weak signals don't beat one strong)
- Bounds: every output is in [0,1]

## Definition of Done

- [ ] Pure function (no DB / IO)
- [ ] Property test: identity_score ∈ [0,1] for any random signal soup
- [ ] Soft-clamp behavior verified
- [ ] No dependency on `internal/server` or `internal/maintenance`
""",
)

add(
    "P2-02",
    "match-group-builder",
    "Build/update dedup_match_groups from MatchPair output",
    ["P0-06", "P2-01"],
    """## What This Does

Takes the `MatchPair` output of the matrix and incrementally maintains the
`dedup_match_groups` + `dedup_match_group_members` tables. Two pairs that
share a book go into the same group (transitive closure within a single
incoming batch).

## Files to Create / Edit

1. **Create** `internal/dedup/matrix/groups.go`
2. **Create** `internal/dedup/matrix/groups_test.go`

## Implementation outline

```go
type GroupWriter interface {
    UpsertGroup(g Group) error
    AddMember(groupID, bookID, role string, pairScore float64) error
    OpenGroupForBook(bookID string) (*Group, error)
}

func ApplyPairs(w GroupWriter, pairs []MatchPair) error { ... }
```

- For each pair: find an existing open group containing either book; if
  found, attach the other; otherwise create a new group with `canonical_book`
  = whichever book has the higher identity_score (tiebreak: earliest
  `created_at`).
- `strongest_kind` and `strongest_score` updated to the max across the group.

## Tests must cover

- New pair → new group with two members
- Pair sharing a book with an existing group → existing group grows
- Two existing groups linked by a new bridging pair → groups merge
- `merged`/`dismissed` groups are NEVER reopened by new pairs (state guarded)

## Definition of Done

- [ ] Idempotent: running the same pairs twice yields the same group set
- [ ] No physical deletes — group merging marks one as `state=merged` rather
      than deleting members
""",
)

# ─────────────────────────────────────────────────────────────────────────────
# PHASE 3 — orchestration
# ─────────────────────────────────────────────────────────────────────────────

add(
    "P3-01",
    "pipeline-coordinator-job",
    "identification-pipeline coordinator job",
    ["P2-01", "P2-02"],
    """## What This Does

A self-registering `MaintenanceJob` named `identification-pipeline` that
orchestrates Phase-1 stages for a book (or for the whole library), gathers
their `signals.Signal` output, runs the matrix, persists `IdentityResult` and
`MatchPair`s, and schedules Stage 9.

## Files to Create / Edit

1. **Create** `internal/maintenance/jobs/identification_pipeline.go`
2. **Create** `internal/maintenance/jobs/identification_pipeline_test.go`

## Implementation outline

```go
type IdentificationPipelineParams struct {
    BookID string `json:"book_id,omitempty"`
    Since  string `json:"since,omitempty"` // RFC3339; library mode
    Stages []string `json:"stages,omitempty"` // optional whitelist
}

func (j *IdentificationPipelineJob) Run(ctx context.Context, reporter operations.ProgressReporter, raw json.RawMessage, startFrom int) error {
    // 1. Resolve target books.
    // 2. For each book: skip stages whose output already exists for the current
    //    signal_revision; enqueue the rest; wait via a per-book sync.WaitGroup
    //    with a hard timeout.
    // 3. Gather signals via signals.Store.ListByBook(bookID).
    // 4. matrix.ComputeIdentity → write identity_results row.
    // 5. matrix.ComputeMatchPairs → matrix.ApplyPairs(groups, pairs).
    // 6. If identity_score < 0.85 and Whisper stage hasn't run, enqueue it
    //    and re-run matrix when its signal arrives (single retry only).
    // 7. Schedule Stage 9 trust-ladder action via P7-01 hook (no-op until P7).
}
```

The coordinator MUST NOT compute fingerprints itself — it only orchestrates
the per-stage jobs.

## Tests must cover

- Single-book run with all stages mocked → identity_results row written
- Library mode with `Since` filter → only books updated after the cutoff
- Re-run with same `signal_revision` is a no-op
- Whisper gating: stage skipped when identity_score from cheap stages ≥ 0.85
- Cancel: respects `reporter.IsCanceled()`

## Definition of Done

- [ ] Job appears in `GET /api/v1/maintenance/jobs`
- [ ] Resumable (CanResume=true) with checkpoint at end of each book
- [ ] All Phase-1 stage jobs are dispatched through the operations queue
""",
)

add(
    "P3-02",
    "backpressure-and-metrics",
    "Per-stage concurrency caps + Prometheus metrics",
    ["P3-01"],
    """## What This Does

Adds the per-stage worker pools (spec §5.1) and Prometheus metrics:
`pipeline_stage_duration_seconds{stage}`, `pipeline_signal_total{kind}`,
`pipeline_identity_score_bucket`, `pipeline_match_group_size_bucket`.

## Files to Create / Edit

1. **Edit** `internal/metrics/metrics.go` — register the four metrics.
2. **Edit** the coordinator from P3-01 — bound concurrency per stage via a
   `chan struct{}` semaphore initialized from config.
3. **Edit** `internal/config/config.go` — add `Pipeline.StageWorkers map[string]int`.

## Definition of Done

- [ ] Default worker counts: `sha256-full=NumCPU`, `chromaprint=max(1,NumCPU/2)`,
      `whisper=2`, `acoustid=1`.
- [ ] Metrics registered, exposed at `/metrics`.
- [ ] Test verifies an over-saturated queue blocks rather than DoSing the API.
""",
)

# ─────────────────────────────────────────────────────────────────────────────
# PHASE 4 — HTTP
# ─────────────────────────────────────────────────────────────────────────────

add(
    "P4-01",
    "identification-endpoints",
    "/api/v1/identification/* HTTP endpoints",
    ["P3-01"],
    """## What This Does

Implements the read + recompute endpoints from spec §7 under
`/api/v1/identification/`. All routes require the existing
`audiobook.read` permission for GETs and `dedup.manage` for POSTs (add the
permission in `internal/auth/permissions.go` if missing).

## Files to Create / Edit

1. **Create** `internal/server/identification_handlers.go`
2. **Create** `internal/server/identification_handlers_test.go`
3. **Edit** `internal/server/server.go` — register the new mux subtree

## Endpoints

```
GET  /api/v1/identification/books/:id
GET  /api/v1/identification/books/:id/signals
POST /api/v1/identification/books/:id/recompute
POST /api/v1/identification/books/:id/recompute/:stage
GET  /api/v1/identification/fingerprints/sha/:sha
GET  /api/v1/identification/fingerprints/chromaprint?fp=…&min=0.85
```

## Definition of Done

- [ ] Each endpoint has a unit test using `httptest`
- [ ] POST endpoints enqueue the `identification-pipeline` job (don't run sync)
- [ ] 404 vs 200 distinguished correctly
- [ ] Permissions enforced
""",
)

add(
    "P4-02",
    "match-groups-v2-endpoints",
    "/api/v1/dedup/v2/match-groups/* endpoints",
    ["P2-02"],
    """## What This Does

Implements the v2 match-group surface (spec §7) parallel to the legacy
`/api/v1/dedup/*` routes (which remain wired through Phase 6).

## Endpoints

```
GET  /api/v1/dedup/v2/match-groups?state=open&limit=&cursor=
GET  /api/v1/dedup/v2/match-groups/:id
POST /api/v1/dedup/v2/match-groups/:id/resolve
GET  /api/v1/dedup/v2/stats
POST /api/v1/dedup/v2/recompute
```

`POST .../resolve` body:
```json
{ "action": "merge|dismiss|split",
  "canonical_book_id": "…",
  "members": ["…","…"] }
```

## Definition of Done

- [ ] Cursor-based pagination
- [ ] `merge` calls into the existing `internal/merge.Service` (do NOT
      duplicate merge logic)
- [ ] `dismiss` and `split` only mutate match-group state; books untouched
- [ ] Forever-store sees a `LogMatch` entry with the chosen `decision`
""",
)

# ─────────────────────────────────────────────────────────────────────────────
# PHASE 5 — UI
# ─────────────────────────────────────────────────────────────────────────────

add(
    "P5-01",
    "identification-tab-shell",
    "React Identification tab shell + library-health panel",
    ["P4-01", "P4-02"],
    """## What This Does

Adds a top-level "Identification" route + tab. The shell is empty other than
the **Library Health** panel: pie of identity-score buckets, table of stages
with availability + queue depth, and a "Recompute all (dry-run)" button.

## Files to Create / Edit

1. **Create** `web/src/pages/Identification/index.tsx`
2. **Create** `web/src/pages/Identification/LibraryHealth.tsx`
3. **Edit** `web/src/App.tsx` (or the central router) — register the route
4. **Edit** the side-nav component — add the link

## Definition of Done

- [ ] Route renders without crashing on an empty library
- [ ] Pie + stage table use `useAsyncAction` hook (existing pattern)
- [ ] Vitest snapshot test passes
""",
)

add(
    "P5-02",
    "per-book-drawer",
    "Per-book identification drawer",
    ["P5-01"],
    """## What This Does

Adds a drawer that opens from any book card showing the per-stage timeline,
each signal as a chip with score / confidence / weight, and a "Recompute…"
menu that POSTs to `/api/v1/identification/books/:id/recompute`.

## Definition of Done

- [ ] Renders timeline ordered by `computed_at` desc
- [ ] Chips colored by contribution sign (positive vs negative)
- [ ] Empty-state ("no signals yet — pipeline never ran for this book")
""",
)

add(
    "P5-03",
    "match-groups-table",
    "Match-groups table with inline resolve",
    ["P5-01", "P4-02"],
    """## What This Does

Adds the match-groups table: filters by `strongest_kind` / score range,
expandable rows with the per-pair signal breakdown, and a one-click resolve
dropdown (merge / dismiss / split).

## Definition of Done

- [ ] Server-side cursor pagination
- [ ] Optimistic UI on resolve (rollback on 4xx/5xx)
- [ ] E2E Playwright spec for the merge happy path
""",
)

# ─────────────────────────────────────────────────────────────────────────────
# PHASE 6 — migration
# ─────────────────────────────────────────────────────────────────────────────

add(
    "P6-01",
    "backfill-existing-library",
    "Backfill pipeline for every existing book",
    ["P3-01"],
    """## What This Does

A one-shot maintenance job (`pipeline-backfill`) that enqueues the
`identification-pipeline` for every book whose `signal_revision = 0`, in
batches of 50 with a configurable inter-batch sleep so the operation queue
isn't starved.

## Definition of Done

- [ ] Resumable
- [ ] Reports progress (`current/total`) on each batch boundary
- [ ] Once finished, every book has a non-zero `signal_revision` and at least
      one row in `identity_results`
""",
)

add(
    "P6-02",
    "translate-dedup-candidates",
    "Translate legacy dedup_candidates → dedup_match_groups",
    ["P2-02"],
    """## What This Does

Walks the legacy `dedup_candidates` table (`internal/database/embedding_store.go`)
and emits equivalent rows in `dedup_match_groups`:

- For every `pending` candidate: create a new open group with
  `strongest_kind = "embedding_similarity"` and the original similarity score.
- For every `merged` candidate: create a `state=merged` group.
- For every `dismissed` candidate: create a `state=dismissed` group.

The legacy table is **not** dropped here — it remains for Phase 6-3.

## Definition of Done

- [ ] Idempotent (uses a stable derived ID from the legacy candidate ID)
- [ ] Counts logged per status
- [ ] Spot-check: pick 10 legacy rows and confirm matching v2 rows exist
""",
)

add(
    "P6-03",
    "deprecate-legacy-routes",
    "Add deprecation headers to legacy /api/v1/dedup/*",
    ["P4-02", "P5-01", "P5-02", "P5-03"],
    """## What This Does

Adds `Deprecation: true` and `Sunset: <date+90d>` headers to every legacy
`/api/v1/dedup/*` handler in `internal/server/dedup_handlers.go`. Adds a
banner to the legacy UI dedup pages pointing to the new tab.

DOES NOT delete the legacy code. Deletion lives in a follow-up bot-task two
release cycles later.

## Definition of Done

- [ ] Every handler in `dedup_handlers.go` emits both headers
- [ ] UI banner present on legacy dedup pages
- [ ] CHANGELOG updated
""",
)

# ─────────────────────────────────────────────────────────────────────────────
# PHASE 7 — auto-merge
# ─────────────────────────────────────────────────────────────────────────────

add(
    "P7-01",
    "trust-ladder-runner",
    "Trust-ladder runner: emits suggestions / auto-merges per match group",
    ["P3-01", "P4-01"],
    """## What This Does

A new job `trust-ladder` that walks open match groups and:

- 0.50 ≤ score < 0.75: emit a "needs review" notification via realtime hub.
- 0.75 ≤ score < 0.90: emit a "default-yes" notification.
- score ≥ 0.90: if `settings.dedup.auto_merge_enabled` is true, call the
  v2 merge endpoint internally with `decided_by = "auto"` and write a
  `LogMatch` entry with `decision = "auto-merge"`. Otherwise emit a "would
  auto-merge" notification only.

## Definition of Done

- [ ] Auto-merge path is gated by the global setting (default off)
- [ ] Every action persists a `fingerprint_match_log` row for forensics
- [ ] Test: with the setting on, score 0.95 group results in members merged
""",
)

add(
    "P7-02",
    "admin-opt-in-toggle",
    "Settings UI + endpoint for auto-merge opt-in",
    ["P7-01"],
    """## What This Does

Adds a single boolean toggle in the Settings → Dedup page that flips
`settings.dedup.auto_merge_enabled`. Adds a confirmation modal warning the
user that ≥0.90-score groups will be merged without per-action approval, and
that all merges are reversible via the match log.

## Definition of Done

- [ ] Toggle persists via existing `SettingsStore`
- [ ] Confirmation modal shown on enable
- [ ] Audit log entry written on toggle (existing `system_activity_log`)
""",
)

# ─────────────────────────────────────────────────────────────────────────────

paths = []
for task_id, slug, title, prereqs, body in TASKS:
    fname = f"{DATE}-dedup-pipeline-{task_id.lower()}-{slug}.md"
    path = os.path.join(OUT, fname)
    with open(path, "w") as f:
        f.write(render(task_id, slug, title, prereqs, body))
    paths.append(path)

print(f"Wrote {len(paths)} bot-tasks:")
for p in paths:
    print(" ", p)
