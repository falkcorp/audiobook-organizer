<!-- file: docs/BUILD.md -->
<!-- version: 1.4.1 -->
<!-- guid: 3c4d5e6f-7a8b-9c0d-1e2f-3a4b5c6d7e8f -->
<!-- last-edited: 2026-08-11 -->

# Build Instructions

## Standard Build (API Only)

By default, the application builds without embedding the frontend. This results
in a smaller binary that only serves the API:

```bash
go build -o audiobook-organizer
```

The server will display an API documentation page at the root (`/`) with
available endpoints.

## Build with Embedded Frontend

To embed the React frontend into the binary, use the `embed_frontend` build tag:

```bash
# First, build the frontend
cd web
npm install
npm run build
cd ..

# Then build the Go binary with embedded frontend
go build -tags embed_frontend -o audiobook-organizer
```

With this option, the compiled binary includes the entire React frontend from
`web/dist/`, allowing the application to serve both the API and web interface
from a single executable.

## Build Tags

- **No tags (default)**: API-only server with placeholder HTML page
  - Smaller binary size
  - Faster compilation
  - API documentation page at root

- **`embed_frontend`**: Full application with embedded web interface
  - Larger binary size (includes all frontend assets)
  - Self-contained single binary
  - Full React SPA served at root
  - Requires `web/dist/` to exist before building

## Cross-Platform Builds

```bash
# Linux
GOOS=linux GOARCH=amd64 go build -o audiobook-organizer-linux

# Linux with frontend
GOOS=linux GOARCH=amd64 go build -tags embed_frontend -o audiobook-organizer-linux

# macOS (Intel)
GOOS=darwin GOARCH=amd64 go build -o audiobook-organizer-macos-intel

# macOS (Apple Silicon)
GOOS=darwin GOARCH=arm64 go build -o audiobook-organizer-macos-arm

# Windows
GOOS=windows GOARCH=amd64 go build -o audiobook-organizer.exe
```

## Implementation Details

The build system uses Go's build tags to conditionally compile different
versions:

- `internal/server/static_nonembed.go` - Compiles by default (no tags)
  - Serves placeholder HTML with API documentation
  - No embedded filesystem

- `internal/server/static_embed.go` - Compiles with `-tags embed_frontend`
  - Uses `//go:embed web/dist` to embed frontend
  - Serves React SPA from embedded filesystem
  - Handles SPA routing (all non-API routes serve index.html)

Both files implement the same `setupStaticFiles()` method, allowing the main
server code to remain unchanged regardless of build configuration.

## Development Workflow

### API Development (without frontend)

```bash
go run . serve
# or
go build && ./audiobook-organizer serve
```

### Full Stack Development

```bash
# Terminal 1: Run frontend dev server with hot reload
cd web
npm run dev

# Terminal 2: Run Go backend
go run . serve

# Frontend proxies API requests to backend
```

### Production Build

```bash
# Build frontend for production
cd web
npm run build
cd ..

# Build backend with embedded frontend
go build -tags embed_frontend -o audiobook-organizer

# Single binary includes everything
./audiobook-organizer serve
```

## Binary Size Comparison

Approximate sizes (may vary by platform):

- **API-only build**: ~15-20 MB
- **With embedded frontend**: ~20-30 MB (adds frontend assets)

The embedded frontend adds the following to the binary:

- React application bundle (~2-5 MB compressed)
- Static assets (images, fonts, etc.)
- index.html and other static files

## Running Tests

### Go Unit/Integration Tests

```bash
go test ./...
```

To run the same packages through the `#runTests` tool, pass the absolute path to
the module root or a specific package. Example:

```text
#runTests {"files":["${workspaceFolder}/internal/server"]}
```

Targeting a specific package keeps execution fast and avoids Selenium E2E
dependencies. Running the entire workspace through `#runTests` will also invoke
the Python end-to-end suite under `tests/e2e`, which requires a reachable app at
`TEST_BASE_URL` (defaults to `http://localhost:8484`). Start the server first if
you intend to run those browser tests.

### JavaScript/TypeScript Frontend Tests

Frontend unit tests live in `web/src` and use Vitest. Install dependencies once
(`cd web && npm install`), then run:

```bash
cd web
npm test
```

Use `#runTests` to execute the same suite from VS Code by pointing at the `web`
folder or an individual spec file:

```text
#runTests {"files":["${workspaceFolder}/web/src/App.test.tsx"]}
```

The tool automatically invokes the `npm test` script (Vitest) as long as
`node_modules` exists. Running `#runTests` with
`"files":["${workspaceFolder}/web"]` aggregates all frontend specs alongside the
Go tests so you can see pass/fail results for every stack layer in one place.

### Metadata Debugging Utility

Use the built-in CLI command to inspect the metadata pipeline for a single audio
file without running a full scan. The command prints both the logical metadata
fields and the technical mediainfo summary, while the standard log output
includes TRACE-level field-source diagnostics added in `metadata.go`.

```bash
go run . inspect-metadata --file /path/to/book.m4b

# or provide the path as a positional argument
go run . inspect-metadata /path/to/book.m4b
```

This is the fastest way to verify whether TagLib sees narrator/series data, how
volume numbers are detected, and whether filename fallback filled any gaps.

## Dockerized Test Image

A standalone Dockerfile (`Dockerfile.test`) lives at the repository root for
fully isolated test runs. It installs Go 1.23, Node.js 22,
Chromium/Chromedriver, and the Python Selenium stack.

```bash
# Build the image
docker build -f Dockerfile.test -t audiobook-organizer-test .

# Run Go + Vitest suites
docker run --rm audiobook-organizer-test bash -lc "go test ./... && npm test --prefix web"

# Run Selenium E2E tests (server must be reachable on the host)
docker run --rm --network host \
  -e TEST_BASE_URL=http://host.docker.internal:8484 \
  audiobook-organizer-test \
  bash -lc "xvfb-run -a pytest tests/e2e -v"
```

The image includes the Git LFS-managed Librivox fixtures under
`testdata/audio/librivox/`, making it safe to execute repeated scans inside the
container without touching your local library.
