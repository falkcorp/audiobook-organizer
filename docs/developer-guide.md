<!-- file: docs/developer-guide.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7f8a9b0c-1d2e-3f4a-5b6c-7d8e9f0a1b2c -->
<!-- last-edited: 2026-10-08 -->

# Developer Guide

## 1. Project Overview

Audiobook Organizer is a self-hosted application for managing audiobook collections. The backend is written in Go (using Gin for HTTP routing) and the frontend is a React/TypeScript single-page application built with Vite and Material UI.

The two halves ship as a single binary. The React build output (`web/dist/`) is embedded into the Go binary at compile time using `//go:embed` with the `embed_frontend` build tag. When the server starts, it serves the API under `/api/v1/` and the frontend at `/`. For development you can run the Vite dev server separately (`make web-dev`) and proxy API calls to the Go backend.

Key capabilities include: filesystem scanning and metadata extraction (from audio file tags, filenames, and external sources like Open Library and iTunes), duplicate detection, series/author/narrator management, bulk operations, and an operations queue for long-running tasks like scans and organizes.

## 2. Getting Started

### Prerequisites

- **Go 1.25+** (module declares `go 1.25.0`)
- **Node.js 18+** and npm
- **C compiler** (CGo required for SQLite via `mattn/go-sqlite3` and taglib bindings)
- **Make**

### First Run

```bash
git clone https://github.com/jdfalk/audiobook-organizer.git
cd audiobook-organizer
make build    # installs npm deps, builds frontend, compiles Go binary
./audiobook-organizer serve
```

Open `http://localhost:8484`. The welcome wizard walks you through setting a library root directory.

### Common Make Targets

| Command | What it does |
|---------|-------------|
| `make build` | Full build: npm install + Vite build + Go compile with embedded frontend |
| `make build-api` | Go binary only (no frontend), for quick backend iteration |
| `make run` | Full build then `./audiobook-organizer serve` |
| `make run-api` | API-only build then serve |
| `make web-dev` | Vite dev server on port 5173 (proxies `/api` to Go backend) |
| `make test` | Go tests with `-race` |
| `make test-all` | Go tests + frontend Vitest tests |
| `make test-e2e` | Playwright end-to-end tests |
| `make ci` | All tests + 80% Go coverage threshold |
| `make clean` | Remove binary and coverage artifacts |

## 3. Architecture

### Backend Packages (`internal/`)

| Package | Purpose |
|---------|---------|
| `server` | Gin router, all HTTP handlers, middleware, SSE events |
| `database` | `Store` interface, SQLite implementation, migrations, PebbleDB store for Open Library dumps |
| `scanner` | Walks filesystem directories, identifies audiobook files (M4B, MP3, etc.) |
| `metadata` | Extracts tags from audio files (uses `dhowden/tag` and `taglib`) |
| `operations` | Queue for long-running tasks (scan, organize) with progress tracking |
| `organizer` | Moves/renames files into the organized library structure |
| `itunes` | iTunes/Music.app library XML import and sync |
| `openlibrary` | Open Library bulk data import into PebbleDB for offline metadata lookups |
| `config` | Viper-based configuration (YAML file + env vars) |
| `fileops` | Low-level file operations (copy, move, hash) |
| `ai` | Optional OpenAI-based filename parsing |
| `cache` | In-memory caching layer |
| `backup` | Database backup/restore |
| `realtime` | Server-Sent Events (SSE) for pushing updates to the frontend |
| `watcher` | Filesystem watcher via `fsnotify` for auto-detecting changes |
| `metrics` | Prometheus metrics |
| `matcher` | Fuzzy matching for deduplication |
| `testutil` | Integration test helpers (real SQLite + temp dirs) |

### Frontend Structure (`web/src/`)

| Directory | Contents |
|-----------|----------|
| `pages/` | Top-level route components: Dashboard, Library, BookDetail, Works, Settings, System, FileBrowser, Operations, Login |
| `components/` | Shared UI: layout (sidebar, header), audiobook cards, wizard, toast notifications, settings panels |
| `services/` | `api.ts` (REST client), `eventSourceManager.ts` (SSE) |
| `contexts/` | React contexts (AuthContext) |
| `hooks/` | Custom hooks (keyboard shortcuts, etc.) |
| `stores/` | State management |

Routing uses React Router v6. The `MainLayout` component provides the sidebar navigation shell; individual pages render inside it.

## 4. Data Flow

An audiobook goes from files on disk to the UI through this pipeline:

1. **Scan trigger** -- User clicks "Scan" in the UI or an API call hits `POST /api/v1/operations/scan`. This creates an operation in the operations queue.

2. **Filesystem walk** -- The `scanner` package recursively walks the configured library and import directories. It identifies audiobook files by extension (`.m4b`, `.mp3`, `.m4a`, `.flac`, `.ogg`) and groups files that belong together (e.g., multi-part audiobooks in the same directory).

3. **Metadata extraction** -- For each discovered audiobook, the `metadata` package reads embedded audio tags (title, author, narrator, duration, etc.) using the `dhowden/tag` and `taglib` libraries. If tags are missing, it falls back to parsing the filename and directory structure. Optionally, Open Library or AI-based parsing can supplement metadata.

4. **Database storage** -- The extracted `Book` record (with author, narrator, and series associations) is upserted into the SQLite database through the `Store` interface. Metadata provenance is tracked: each field records whether it came from tags, filename, or an external source, and all changes are logged in the metadata change history.

5. **API response** -- The frontend calls `GET /api/v1/audiobooks` which returns paginated results `{ count, items, limit, offset }`. Individual books are fetched via `GET /api/v1/audiobooks/:id`.

6. **UI rendering** -- The Library page displays books in a grid/list. BookDetail shows full metadata with edit capabilities. The Dashboard shows aggregate statistics. Real-time progress updates during scans flow through SSE (`/api/events`).

## 5. Database

### Dual-Store Architecture

The application uses two storage engines:

- **SQLite** (primary) -- Stores all application data: books, authors, narrators, series, configuration, operation history, metadata provenance, and user accounts. Accessed through the `Store` interface in `internal/database/store.go`.

- **PebbleDB** (supplementary) -- Stores Open Library bulk dump data for fast offline ISBN/title lookups. This is a separate key-value store optimized for large read-heavy datasets.

### Store Interface

The `Store` interface (`internal/database/store.go`) defines all database operations. It covers: lifecycle (`Close`, `Reset`), CRUD for books/authors/narrators/series, metadata provenance tracking, configuration, import paths, operations, and authentication. The SQLite implementation lives in `internal/database/sqlite.go`.

### Migrations

Migrations are defined in `internal/database/migrations.go` as Go functions (`migration001Up` through `migration011Up`). Each migration runs inside a transaction and is recorded in a `_migrations` table to prevent re-execution. Migrations run automatically on startup via `RunMigrations(store)`.

To add a new migration: define a `migrationNNNUp` function, add it to the `migrations` slice in `migrations.go`, and increment the count. SQL-based migrations for complex schema changes can also be placed in `internal/db/migrations/`.

### Key Models

- `Book` -- Core entity with fields for title, author, narrator, duration, file paths, cover art, ISBN, series info, and metadata provenance flags.
- `Author`, `Narrator`, `Series` -- Normalized entities with many-to-many relationships to books.
- `MetadataFieldState` -- Tracks the source (tag, filename, manual, external) and lock status of each metadata field per book.
- `MetadataChangeRecord` -- Audit log of all metadata changes.

## 6. API

All endpoints live under `/api/v1/`. The server uses Gin with optional authentication middleware.

### Core Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/health` | Health check |
| `GET` | `/api/events` | SSE event stream |
| `GET` | `/api/v1/audiobooks` | List books (paginated, filterable) |
| `GET` | `/api/v1/audiobooks/search` | Full-text search |
| `GET` | `/api/v1/audiobooks/:id` | Get single book |
| `PUT` | `/api/v1/audiobooks/:id` | Update book metadata |
| `DELETE` | `/api/v1/audiobooks/:id` | Soft-delete book |
| `POST` | `/api/v1/audiobooks/batch` | Batch update |
| `GET` | `/api/v1/authors` | List authors |
| `GET` | `/api/v1/narrators` | List narrators |
| `GET` | `/api/v1/series` | List series |
| `POST` | `/api/v1/operations/scan` | Start library scan |
| `POST` | `/api/v1/operations/organize` | Start organize operation |
| `GET` | `/api/v1/operations/:id/status` | Operation progress |
| `GET` | `/api/v1/config` | Get configuration |
| `PUT` | `/api/v1/config` | Update configuration |
| `GET` | `/api/v1/system/status` | System info (memory, disk, version) |
| `GET` | `/api/v1/dashboard` | Dashboard statistics |

### Authentication Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/auth/status` | Check if auth is enabled |
| `POST` | `/api/v1/auth/setup` | Create initial admin user |
| `POST` | `/api/v1/auth/login` | Login |
| `POST` | `/api/v1/auth/logout` | Logout |

### iTunes Integration

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/api/v1/itunes/validate` | Validate iTunes library XML |
| `POST` | `/api/v1/itunes/import` | Import from iTunes library |

An OpenAPI specification is available at `docs/openapi.yaml`.

## 7. Testing

### Running Tests

```bash
make test        # Go backend tests with -race flag
make test-all    # Backend + frontend (Vitest) tests
make test-e2e    # Playwright E2E tests (requires built binary)
make ci          # All tests + enforce 80% Go coverage
make coverage    # Generate HTML coverage report
```

### Test Patterns

**Server handler tests** use `setupTestServer(t)` which creates a real Gin engine backed by a real SQLite database (in-memory or temp file). This gives integration-level confidence without mocks:

```go
func TestListAudiobooks(t *testing.T) {
    s, cleanup := setupTestServer(t)
    defer cleanup()
    // ... make HTTP requests against s.router
}
```

**Unit tests with mocks** use `mocks.NewMockStore(t)` (generated by mockery from the `Store` interface) for testing business logic in isolation without a database.

**Integration tests** use `testutil.SetupIntegration(t)` which provisions a real SQLite database, temp directories, and sets global state. These test the full pipeline from scanner to database.

**Frontend tests** use Vitest with React Testing Library. Test files sit next to their source files (e.g., `Library.bulkFetch.test.tsx`).

**E2E tests** use Playwright and run against a built binary. They exercise complete user flows through the browser.

### Gotchas

- Parallel Go tests can be flaky due to global state (`GlobalStore`, `GlobalQueue`). Integration tests that set globals should not run in parallel.
- Helper functions like `contains` and `intPtr` are defined in specific files; do not redeclare them in the same package.
- The 80% coverage threshold is enforced in CI via `make coverage-check`.

## 8. Common Tasks

### Adding a New API Endpoint

1. **Define the handler** in `internal/server/server.go` (or a dedicated handler file). Follow the existing pattern: accept `*gin.Context`, parse parameters, call the store, return JSON.

2. **Register the route** in the `setupRoutes` method of `server.go`, inside the `protected` group (or `authGroup` for auth endpoints):
   ```go
   protected.GET("/my-endpoint", s.myHandler)
   ```

3. **Add the Store method** if needed (see next section).

4. **Update the frontend** API client in `web/src/services/api.ts` with a corresponding function.

5. **Write tests**: add a handler test using `setupTestServer(t)`.

### Adding a New Database Field / Migration

1. **Update the model** in `internal/database/models.go` (add the field to the struct).

2. **Write a migration** in `internal/database/migrations.go`:
   ```go
   func migration012Up(store Store) error {
       sqlStore, ok := store.(*SQLiteStore)
       if !ok { return nil }
       _, err := sqlStore.db.Exec("ALTER TABLE books ADD COLUMN my_field TEXT DEFAULT ''")
       return err
   }
   ```
   Add it to the `migrations` slice.

3. **Update Store methods** that read/write the affected table to include the new column in their SQL queries.

4. **Update the Store interface** in `store.go` if adding new operations.

5. **Run tests** to verify the migration applies cleanly: `make test`.

### Adding a Frontend Page

1. **Create the page component** in `web/src/pages/MyPage.tsx`.

2. **Add the route** in `web/src/App.tsx`:
   ```tsx
   <Route path="/my-page" element={<MyPage />} />
   ```

3. **Add navigation** in the sidebar component (`web/src/components/layout/MainLayout.tsx` or its sidebar sub-component).

4. **Add API calls** in `web/src/services/api.ts` if the page needs new data.

5. **Write tests**: add a Vitest test file next to the component and add relevant E2E scenarios in the Playwright test suite.

### Adding a New Metadata Source

1. **Create a package** under `internal/` (e.g., `internal/hardcover/`).

2. **Implement a client** that fetches metadata given an ISBN, title, or author query.

3. **Integrate with the scanner** or create a standalone enrichment operation in `internal/operations/`.

4. **Track provenance** by setting appropriate `MetadataFieldState` source values when updating book records.
