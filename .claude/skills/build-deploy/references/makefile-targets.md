<!-- file: .claude/skills/build-deploy/references/makefile-targets.md -->
<!-- version: 1.0.0 -->
<!-- guid: b8e341ee-3b43-44b1-8c1a-ac1c3fc27a1f -->
<!-- last-edited: 2026-11-10 -->

# Makefile Targets Reference

## Build Targets

### `make build`
**Full build: frontend + Go backend (embedded UI)**

- Runs `npm install` + `npm run build` in `web/`
- Builds Go binary with embedded frontend (build tag `embed_frontend`)
- Output: `./audiobook-organizer` (single binary)
- Use when: Preparing for distribution or testing the complete app
- Speed: ~2-3 min (depends on npm cache)

### `make build-api`
**Backend only (no embedded frontend)**

- Skips frontend entirely
- Builds Go binary without embedding
- Output: `./audiobook-organizer`
- Use when: Iterating on backend code; frontend served separately
- Speed: ~30 sec

### `make build-bench`
**Backend + benchmark tooling**

- Builds with bench utilities for dedup experiments
- Includes special build tags for performance testing
- Use when: Testing dedup algorithm performance

## Run Targets

### `make run`
**Full build then serve (http://localhost:8484)**

- Runs `make build` then starts the server
- Embedded frontend available at http://localhost:8484
- Ctrl+C to stop

### `make run-api`
**Backend-only build then serve (API only)**

- Runs `make build-api` then starts the server
- API endpoints available at http://localhost:8484
- Useful for running separate frontend via `make web-dev`

## Frontend Targets

### `make web-install`
**Install npm dependencies**

- Runs `npm install` in `web/`
- Needed before any frontend work
- Idempotent: safe to run multiple times

### `make web-build`
**Build frontend (outputs to web/dist)**

- Runs `npm run build` in `web/`
- Produces optimized production build
- Required by `make build` (called automatically)

### `make web-dev`
**Start Vite dev server (http://localhost:5173)**

- Runs `npm run dev` in `web/`
- Live hot-module reload (HMR)
- Changes appear instantly in browser
- **Perfect for React/TypeScript frontend work**

### `make web-test`
**Run frontend unit tests**

- Runs `npm run test` (Vitest)
- Watch mode by default

### `make web-lint`
**Lint frontend code**

- Runs `npm run lint` (ESLint)
- Checks TypeScript, JSX, style

### `make web-lint-memory`
**Check for React memory leaks (CI scanner)**

- Special linting pass for memory leaks
- Checks useEffect cleanup, useRef patterns, etc.

## Test Targets

### `make test`
**Run Go backend tests (full)**

- Runs all tests including slow property tests
- Time: ~15 min
- Use when: You need comprehensive coverage (pre-commit, CI)

### `make test-short`
**Run Go backend tests in -short mode**

- Skips slow property tests
- Time: ~1 min
- **Use for local iteration**: Fast feedback loop

### `make test-all`
**Backend (full) + frontend tests**

- Runs `make test` and `make test-frontend`
- Time: ~20 min
- Use when: Changing both backend and frontend

### `make test-all-short`
**Backend (-short) + frontend tests**

- Runs `make test-short` and `make test-frontend`
- Time: ~2-3 min
- Use for local CI before pushing

### `make test-frontend`
**Frontend tests only**

- Runs Vitest via `npm run test`
- Use when: Only frontend changed

### `make test-e2e`
**Playwright end-to-end tests**

- Runs browser automation tests
- Requires running server (spawn separately)
- Use when: Testing UI workflows

## Coverage Targets

### `make coverage`
**Generate coverage reports**

### `make coverage-check`
**Check coverage meets 80% threshold**

- Used in CI
- Fails if coverage < 80%

## Quality Targets

### `make vet`
**Run Go vet**

- Static analysis for Go code
- Catches common mistakes

### `make mocks`
**Generate mocks (mockgen)**

- Creates mock types for testing

### `make mocks-check`
**Verify mocks are fresh**

- Ensures mocks match current code

### `make staticcheck`
**Run staticcheck (Go linter)**

- Detects unused code, incorrect patterns

### `make tasklint`
**Lint task definitions**

- core/archtest/tasklint (moved from tools/cmd/oplint) over core and books' task packages, the
  archtest guards' self-tests, and the shrink-only exemption ratchet

## CI Targets

### `make ci`
**Run all CI checks**

- Tests (full), coverage check, staticcheck, tasklint, etc.
- Time: ~20-25 min
- This is what runs in GitHub Actions

### `make test-nightly`
**Nightly test suite**

- Includes all property tests and slow checks
- For overnight CI runs

## Deployment Targets

### `make deploy`
**Full build + deploy to production**

- Requires `Makefile.local` with deployment config
- Handles:
  - Go build with versioning
  - Frontend build + embed
  - Upload to production server
  - Service restart
- **Must use this for production**

### `make deploy-debug`
**Build with debug symbols + deploy**

- Same as `make deploy` but with debug symbols
- Useful for profiling production

## Version & Release Targets

### `make version`
**Show current version**

- Reads from git tags

### `make release-dry-run`
**Test release process without publishing**

### `make release-snapshot`
**Create snapshot release artifacts**

## Docker Targets

### `make docker`
**Build Docker image**

### `make docker-run`
**Run Docker container**

### `make docker-stop`
**Stop Docker container**

## Miscellaneous

### `make clean`
**Remove build artifacts**

- Deletes binaries, dist dirs, etc.

### `make help`
**Show this help**

- Lists all targets with descriptions

## Determining Which Target to Use

| I want to... | Use this target |
|---|---|
| Test my backend changes | `make test-short` |
| Run tests before committing | `make test-all-short` |
| Develop the frontend | `make web-dev` |
| Test a complete workflow | `make run` |
| Check if everything works | `make ci` |
| Deploy to production | `make deploy` |
| Quick API iteration | `make run-api` |
| Run all tests | `make test-all` |

## Notes

- **`npm install` is automatic**: `make build` and `make web-build` always run `npm install` first. No need to run it manually.
- **Frontend location**: Always at `repo_root/web/`. Build targets hardcode this path.
- **Embedded vs separate**: `make build` embeds the frontend; `make run-api` + `make web-dev` (in separate terminals) serve them separately.
- **Environment**: `go.mod` specifies `go 1.24.0`. If using newer Go features (1.25+), update `go.mod`.
