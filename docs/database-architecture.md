<!-- file: docs/database-architecture.md -->
<!-- version: 1.0.2 -->
<!-- guid: 1d2e3f4a-5b6c-7d8e-9f0a-1b2c3d4e5f6a -->
<!-- last-edited: 2026-07-03 -->

# Database Architecture

## Overview

The audiobook organizer uses an abstraction layer to support multiple database
backends:

- **PebbleDB** (Default, Recommended): Pure Go LSM key-value store
- **SQLite3** (Opt-in, Legacy): Traditional SQL database with cross-compilation
  challenges

## PebbleDB Keyspace Specification

For the complete prefix/key layout, JSON value schemas, indices, playback
tracking, and migration strategy, see the dedicated schema document:

**[PebbleDB Keyspace Schema](database-pebble-schema.md)**

That document formalizes all keys (users, sessions, preferences, authors,
series, books, segments, playlists, playback events, progress, stats,
operations, migrations) and should be treated as canonical for any new feature
touching persistence. **Caveat:** The document's entity-ID convention (ULID strings) does not match the current implementation (integer IDs); the `## Dedup / Embedding store` section is verified-accurate.

## Why PebbleDB?

### Advantages

1. **Pure Go Implementation**
   - No CGO dependencies
   - Cross-compilation works seamlessly
   - No platform-specific build issues

2. **Performance**
   - LSM (Log-Structured Merge) tree architecture
   - Optimized for write-heavy workloads
   - Fast sequential reads

3. **Simplicity**
   - Embedded database (no separate server)
   - Single dependency
   - Mature codebase from CockroachDB

4. **Reliability**
   - Battle-tested in production (CockroachDB)
   - ACID guarantees
   - Crash recovery

### Disadvantages

- No SQL queries (key-value only)
- Requires custom indexing for complex queries
- Full-text search requires external solution

## Why Not SQLite3?

### Issues

1. **CGO Dependency**
   - Requires C compiler for cross-compilation
   - Platform-specific binaries
   - Complicates build process

2. **Cross-Compilation Challenges**
   - Must compile SQLite C library for each target
   - Different build flags per platform
   - Deployment complexity

3. **Our Use Case**
   - We don't need complex SQL queries
   - Most operations are simple key lookups
   - Relationships are minimal

## Database Selection

### Default: PebbleDB

```bash
# Automatically uses PebbleDB
./audiobook-organizer scan --dir /audiobooks

# Explicitly specify PebbleDB
./audiobook-organizer scan --dir /audiobooks --db-type pebble --db audiobooks.pebble
```

### Opt-in: SQLite3

**⚠️ WARNING: SQLite3 has cross-compilation issues. Use only if you understand
the risks.**

```bash
# Must explicitly enable with scary flag
./audiobook-organizer scan \
  --dir /audiobooks \
  --db-type sqlite \
  --db audiobooks.db \
  --enable-sqlite3-i-know-the-risks
```

### Configuration File

`.audiobook-organizer.yaml`:

```yaml
# Default: PebbleDB (recommended)
database_type: pebble
database_path: audiobooks.pebble
# Legacy: SQLite3 (requires explicit enable flag)
# database_type: sqlite
# database_path: audiobooks.db
# enable_sqlite3_i_know_the_risks: true
```

## Key Schema (PebbleDB)

### Prefixes

- `author:<id>` → Author JSON
- `author:name:<name>` → author_id
- `series:<id>` → Series JSON
- `series:name:<name>:<author_id>` → series_id
- `book:<id>` → Book JSON
- `book:path:<path>` → book_id
- `book:series:<series_id>:<book_id>` → book_id
- `book:author:<author_id>:<book_id>` → book_id
- `import_path:<id>` → ImportPath JSON
- `library:path:<path>` → library_id
- `operation:<id>` → Operation JSON
- `operationlog:<operation_id>:<timestamp>:<seq>` → OperationLog JSON
- `preference:<key>` → UserPreference JSON
- `playlist:<id>` → Playlist JSON
- `playlist:series:<series_id>` → playlist_id
- `playlistitem:<playlist_id>:<position>` → PlaylistItem JSON
- `counter:<entity>` → next ID

### Example

```
author:1 → {"id":1,"name":"Brandon Sanderson"}
author:name:Brandon Sanderson → 1
series:1 → {"id":1,"name":"Mistborn","author_id":1}
series:name:Mistborn:1 → 1
book:1 → {"id":1,"title":"The Final Empire","series_id":1,...}
book:path:/audiobooks/mistborn1.m4b → 1
book:series:1:1 → 1
counter:book → 2
```

## API Abstraction

### Store Interface

```go
type Store interface {
    // Lifecycle
    Close() error

    // Authors
    GetAllAuthors() ([]Author, error)
    GetAuthorByID(id int) (*Author, error)
    GetAuthorByName(name string) (*Author, error)
    CreateAuthor(name string) (*Author, error)

    // Books
    GetAllBooks(limit, offset int) ([]Book, error)
    GetBookByID(id int) (*Book, error)
    GetBookByFilePath(path string) (*Book, error)
    CreateBook(book *Book) (*Book, error)
    UpdateBook(id int, book *Book) (*Book, error)
    DeleteBook(id int) error

    // ... (full interface in internal/database/store.go)
}
```

### Implementations

- `SQLiteStore` (internal/database/sqlite_store.go)
- `PebbleStore` (internal/database/pebble_store.go)

Both implement the same interface, allowing seamless switching.

## Migration from SQLite

### Automatic Migration (Not Yet Implemented)

Future feature: automatic data migration from SQLite to PebbleDB.

### Manual Migration

1. **Export from SQLite**:

   ```bash
   sqlite3 audiobooks.db .dump > backup.sql
   ```

2. **Switch to PebbleDB**:

   ```bash
   # Remove old database flag, use new default
   ./audiobook-organizer scan --dir /audiobooks
   ```

3. **Re-scan your library**: PebbleDB will rebuild the database from scratch by
   scanning files.

## Performance Considerations

### PebbleDB

- **Reads**: O(log N) for key lookups, O(N) for range scans
- **Writes**: O(log N) with write buffering
- **Space**: Compaction runs periodically to reclaim space
- **Memory**: Configurable block cache (default: reasonable for embedded use)

### SQLite3

- **Reads**: O(log N) with B-tree indexes
- **Writes**: O(log N) with WAL mode
- **Space**: VACUUM required to reclaim space
- **Memory**: Page cache (default: 2000 pages = ~8MB)

For our use case (mostly reads, occasional writes), both perform well.
PebbleDB's advantage is in build/deployment simplicity.

## Troubleshooting

### PebbleDB Issues

**Problem**: Database corruption **Solution**: Delete `audiobooks.pebble`
directory and re-scan

**Problem**: Disk space usage growing **Solution**: PebbleDB compacts
automatically, but you can force it:

```go
// In code
db.Compact([]byte(""), []byte("~"), true)
```

### SQLite Issues

**Problem**: Cross-compilation fails **Solution**: Use PebbleDB instead

**Problem**: "CGO not enabled" **Solution**: Either enable CGO or switch to
PebbleDB:

```bash
CGO_ENABLED=1 go build  # Enable CGO (requires C compiler)
# OR
./audiobook-organizer --db-type pebble  # Use PebbleDB
```

## References

- [PebbleDB GitHub](https://github.com/cockroachdb/pebble)
- [PebbleDB Documentation](https://pkg.go.dev/github.com/cockroachdb/pebble)
- [SQLite CGO Driver](https://github.com/mattn/go-sqlite3)
