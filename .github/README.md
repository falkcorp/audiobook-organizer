# Audiobook Organizer

A full-stack audiobook management application — Go backend with an embedded
React/TypeScript frontend — for scanning, organizing, and enriching your
audiobook collection.

Ship it as a single binary or a Docker container. Open `http://localhost:8484`
and manage everything from your browser.

## Quick Start

### Docker Compose (recommended)

```bash
cp .env.example .env          # edit values as needed
# set AUDIOBOOK_ROOT_DIR in .env to your library path
docker compose up -d --build
```

The app is available at `http://localhost:8484`.

### From Source

```bash
git clone https://github.com/jdfalk/audiobook-organizer.git
cd audiobook-organizer
make build   # builds frontend + backend into a single binary
./audiobook-organizer serve --dir /path/to/audiobooks --host 0.0.0.0
```

On first run with auth enabled, create the initial admin account from the login
screen.

### CLI-Only Usage

```bash
# Scan a directory for audiobooks
./audiobook-organizer scan --dir /path/to/audiobooks

# Generate iTunes-compatible playlists
./audiobook-organizer playlist

# Update audio file tags with series information
./audiobook-organizer tag

# Run the full pipeline (scan → process → playlists → tag)
./audiobook-organizer organize --dir /path/to/audiobooks

# Inspect metadata for a single file
./audiobook-organizer inspect-metadata /path/to/book.m4b

# Run diagnostics
./audiobook-organizer diagnostics
```

## Features

### Library Management

- **Scan & Import**: Discover audiobooks from import paths and organize them
  into your library with proper folder structure and naming
- **Non-destructive organization**: Copy-first approach with SHA256 verification,
  automatic backups, and rollback support
- **Import paths**: Configure multiple watched directories (Downloads, NAS
  mounts, etc.) that are scanned for new content
- **File relocation**: Move book files between directories from the UI
- **Soft delete & restore**: Remove books without losing data; purge when ready
- **Duplicate detection**: Identify duplicate audiobooks across your library
- **Blocked hashes**: Prevent re-importing unwanted files by hash

### Metadata & Enrichment

- **Automatic metadata extraction**: Reads tags from MP3, M4A, M4B, AAC, FLAC,
  OGG, and WMA files
- **Open Library integration**: Fetch and apply metadata from Open Library,
  including bulk fetch for your entire collection
- **Open Library dump import**: Download or upload Open Library data dumps for
  fast offline lookups
- **AI-powered parsing** (optional): Use OpenAI to intelligently parse filenames
  and audiobook context into structured metadata
- **Metadata editing**: Inline editing, batch updates, validation rules, and
  export/import
- **Metadata history**: Full change log per field with undo support
- **Field-level overrides & locks**: Override fetched metadata and lock fields
  to prevent future overwrites
- **Copy-on-write versioning**: Track metadata versions with prune support
- **Write-back**: Push metadata changes back into audio file tags (native TagLib
  with CLI tool fallback)
- **iTunes library import**: Validate, map, import, and sync from an iTunes
  Library XML; write back changes to iTunes-managed files

### Organization & Discovery

- **Works**: Group multiple editions/formats of the same title under a logical
  "Work" entity
- **Version management**: Link audiobook versions (abridged, unabridged,
  different narrators) and set a primary
- **Author deduplication**: Detect and merge duplicate author entries
- **Smart series detection**: Pattern matching and fuzzy logic to identify book
  series from filenames and metadata
- **Narrators**: Track narrators per audiobook with full CRUD
- **Search & filtering**: Full-text search across audiobooks, authors, narrators,
  and series with counts

### Web Interface (React + Material-UI)

- **Dashboard**: Library statistics and at-a-glance status
- **Library browser**: Grid and list views for your audiobook collection
- **Book detail view**: Full metadata display, cover art, segment listing, and
  track info extraction
- **File manager**: Directory tree browser with import path management
- **File browser**: Server-side file system navigation
- **Operations panel**: Monitor active/stale operations with real-time progress
  via SSE
- **Settings page**: Configuration management, iTunes import, Open Library dump
  management, blocked hashes
- **System page**: Storage quotas, system info, and searchable application logs
- **Login & auth**: Session-based authentication with admin setup wizard
- **Welcome wizard**: First-run setup experience
- **Keyboard shortcuts**: Power-user keyboard navigation
- **Announcement banner**: Surface system-wide notices
- **Toast notifications**: Non-blocking feedback for user actions

### Backend & Infrastructure

- **REST API (v1)**: 100+ endpoints covering audiobooks, authors, narrators,
  series, works, operations, metadata, filesystem, config, backups, auth, and
  more
- **Authentication**: Session-based auth with login, logout, session management,
  and rate-limited auth endpoints; optional basic auth
- **Real-time updates**: Server-Sent Events (SSE) push operation progress, log
  lines, and status changes to the browser
- **Async operation queue**: Priority-based background processing with
  configurable workers for scan, organize, and transcode operations
- **Database**: PebbleDB (default) or SQLite with migration system; encrypted
  settings storage
- **Database backup/restore**: Compressed backups with checksums and automatic
  cleanup
- **Prometheus metrics**: `/metrics` endpoint for monitoring
- **HTTPS / HTTP/2 / HTTP/3**: TLS support with QUIC (HTTP/3) on UDP
- **Rate limiting**: Configurable per-IP rate limits for API and auth endpoints
- **Request size limits**: Separate limits for JSON and upload payloads
- **File system watcher**: Watch directories for changes (via fsnotify)
- **Transcoding**: Audio format conversion operations
- **Self-update**: Check for and apply application updates from the UI
- **Graceful shutdown**: Clean shutdown with operation queue draining
- **Single-binary deployment**: React frontend embedded via `//go:embed`
- **Multi-stage Docker build**: Node.js → Go → minimal Alpine runtime image
  running as non-root

### CLI

- `scan` — Discover audiobooks and extract metadata
- `playlist` — Generate iTunes-compatible playlists by series
- `tag` — Write series information into audio file tags
- `organize` — Run the full pipeline (scan → process → playlists → tag)
- `serve` — Start the web server (with embedded UI)
- `inspect-metadata` — Extract and display metadata for a single file
- `diagnostics` — Run system diagnostics

### Audio Format Support

MP3, M4A, M4B, AAC, FLAC, OGG, WMA

Metadata writing uses native TagLib (pure Go with bundled Wasm). If the native
write fails, the app falls back to external CLI tools (AtomicParsley for
M4B/M4A, eyeD3 for MP3, metaflac for FLAC).

## Glossary

- **Library / Library Path**: The root directory (`root_dir`) where your
  audiobooks are permanently stored in an organized structure.
- **Import Path**: An external directory scanned for new audiobook files to
  import into the library (e.g., a Downloads folder).
- **Scan**: Discovering audiobook files, extracting metadata, and updating the
  database.
- **Organize**: Moving or copying audiobooks from import paths into the library
  with proper naming.
- **Work**: A logical grouping of multiple editions or formats of the same title.

## Configuration

Configuration is loaded from (in order of precedence): CLI flags → environment
variables → database-persisted settings → config file.

```yaml
# $HOME/.audiobook-organizer.yaml
root_dir: "/path/to/audiobooks"
database_path: "audiobooks.pebble"
playlist_dir: "playlists"
```

Key environment variables (see `.env.example` for the full list):

| Variable | Default | Description |
|---|---|---|
| `AO_DIR` | — | Audiobook library root directory |
| `AO_DB` | `/data/audiobook-organizer.db` | Database path |
| `PORT` | `8484` | HTTP server port |
| `HOST` | `0.0.0.0` | Bind address |
| `ENABLE_AUTH` | `true` | Enable session-based authentication |
| `ENABLE_AI_PARSING` | `false` | Enable OpenAI-powered metadata parsing |
| `OPENAI_API_KEY` | — | API key for AI parsing |
| `OPENAI_MODEL` | `gpt-4o-mini` | OpenAI model to use |
| `API_RATE_LIMIT_PER_MINUTE` | `100` | API rate limit (0 = disabled) |

See [docs/configuration.md](docs/configuration.md) for the complete reference.

## Development

### Build Commands

```bash
make build           # Full build: frontend + backend (embedded UI)
make build-api       # Backend only (quick iteration)
make run             # Full build then serve
make run-api         # API-only build then serve
make web-dev         # Vite dev server (frontend hot-reload)
make test            # Go backend tests
make test-all        # Backend + frontend tests
make test-e2e        # Playwright E2E tests
make ci              # All tests + 80% coverage check
make docker           # Build Docker image
make help            # All targets
```

### Testing

Always use `-tags=mocks` for accurate coverage:

```bash
make test              # recommended
make coverage          # generate coverage report
make coverage-check    # verify 80% threshold
go test ./... -tags=mocks -cover -v   # direct invocation
```

See [BUILD_TAGS_GUIDE.md](BUILD_TAGS_GUIDE.md) for details on build tags and
mock generation.

### Tech Stack

| Layer | Technology |
|---|---|
| Backend | Go 1.25, Gin, Cobra, Viper |
| Frontend | React 18, TypeScript, Vite, Material-UI v5, Zustand |
| Database | PebbleDB (default), SQLite (optional) |
| Testing | Go testing + testify, Vitest, React Testing Library, Playwright |
| CI/CD | GitHub Actions, GoReleaser |
| Deployment | Docker (multi-stage), systemd, launchd |

## Documentation

- [Architecture](docs/architecture.md)
- [Configuration](docs/configuration.md)
- [Technical Design](docs/archive/technical_design.md)
- [PebbleDB Keyspace Schema](docs/database-pebble-schema.md)
- [MVP Specification](docs/mvp-specification.md)
- [Implementation Plan](docs/mvp-implementation-plan.md)
- [QA Checklist](docs/qa-checklist.md)

## License

MIT — see [LICENSE](LICENSE).

## Repository Automation

This project uses standard workflows and scripts from
[ghcommon](https://github.com/jdfalk/ghcommon).
