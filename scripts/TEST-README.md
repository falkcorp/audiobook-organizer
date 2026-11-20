<!-- file: scripts/TEST-README.md -->
<!-- version: 1.0.0 -->
<!-- guid: d4e5f6a7-b8c9-0123-def0-456789012cde -->
<!-- last-edited: 2026-01-19 -->

# API Testing Scripts

This directory contains automated and manual testing scripts for the Audiobook
Organizer API.

## Available Scripts

### 1. `test-api-endpoints.py`

Comprehensive Python script for manual API endpoint testing.

**Features:**

- Tests all MVP-specified endpoints
- Performance measurement (response times)
- JSON result export to `test-results.json`
- Colorized console output with emojis
- Safety checks (skips destructive operations by default)
- Detailed error reporting

**Usage:**

```bash
# Test local server (default: http://localhost:8484)
python3 test-api-endpoints.py

# Test remote server
python3 test-api-endpoints.py http://your-server:8484
```

**Requirements:**

```bash
pip3 install requests
```

**Output:**

- Console: Colorized test results with status codes and response times
- File: `test-results.json` with complete test data

**Example Output:**

```text
╔══════════════════════════════════════════════════════════════════════════════╗
║                   Audiobook Organizer API Test Suite                        ║
╚══════════════════════════════════════════════════════════════════════════════╝

================================================================================
Testing: GET /health
Description: Health check endpoint
✅ Status: 200 (expected 200)
⏱️  Duration: 45.23ms
Response: {
  "status": "ok",
  "timestamp": 1699564800,
  "version": "1.1.0"
}
```

### 2. Automated Go Tests

Located in `internal/server/server_test.go`

**Run all tests:**

```bash
go test -v ./internal/server
```

**Run specific test:**

```bash
go test -v -run ^TestHealthCheck$ ./internal/server
```

**Run with coverage:**

```bash
go test -v -cover ./internal/server
```

**Run benchmarks:**

```bash
go test -bench=. ./internal/server
```

## Testing Workflow

### Step 1: Start the Server

```bash
go run main.go server
# Or use the VS Code task: "Run Server"
```

### Step 2: Run Automated Tests

```bash
# Quick smoke test
go test -v -run ^TestHealthCheck$ ./internal/server

# Full test suite
go test -v ./internal/server
```

### Step 3: Run Manual Tests

```bash
python3 scripts/test-api-endpoints.py
```

### Step 4: Review Results

```bash
# View automated test results
cat logs/test-output.log

# View manual test results
cat test-results.json
```

## Test Coverage

### Endpoints Tested

**System:**

- `GET /health` - Health check
- `GET /api/v1/system/status` - System status
- `GET /api/v1/config` - Configuration
- `GET /api/v1/system/logs` - System logs

**Audiobooks:**

- `GET /api/v1/audiobooks` - List audiobooks
- `GET /api/v1/audiobooks/:id` - Get audiobook
- `PUT /api/v1/audiobooks/:id` - Update audiobook
- `DELETE /api/v1/audiobooks/:id` - Delete audiobook
- `POST /api/v1/audiobooks/batch` - Batch update

**Authors & Series:**

- `GET /api/v1/authors` - List authors
- `GET /api/v1/series` - List series

**Filesystem:**

- `GET /api/v1/filesystem/browse` - Browse filesystem
- `POST /api/v1/filesystem/exclude` - Create exclusion
- `DELETE /api/v1/filesystem/exclude` - Remove exclusion

**Library:**

- `GET /api/v1/library/folders` - List library folders
- `POST /api/v1/library/folders` - Add folder
- `DELETE /api/v1/library/folders/:id` - Remove folder

**Operations:**

- `POST /api/v1/operations/scan` - Start scan
- `POST /api/v1/operations/organize` - Start organize
- `GET /api/v1/operations/:id/status` - Get status
- `GET /api/v1/operations/:id/logs` - Get logs
- `DELETE /api/v1/operations/:id` - Cancel operation

**Backups:**

- `GET /api/v1/backup/list` - List backups
- `POST /api/v1/backup/create` - Create backup
- `POST /api/v1/backup/restore` - Restore backup
- `DELETE /api/v1/backup/:filename` - Delete backup

**Metadata:**

- `POST /api/v1/metadata/batch-update` - Batch update
- `POST /api/v1/metadata/validate` - Validate metadata
- `GET /api/v1/metadata/export` - Export metadata
- `POST /api/v1/metadata/import` - Import metadata

### Test Types

1. **Unit Tests** - Individual endpoint functionality
2. **Integration Tests** - Database interactions
3. **End-to-End Tests** - Complete workflows
4. **Performance Tests** - Response time benchmarks
5. **Error Handling Tests** - Edge cases and failures

## Known Issues

See `docs/api-testing-summary.md` for detailed list of discovered issues and
required fixes.

**Summary:**

- 9 tests passing ✅
- 10 tests failing (issues documented) ❌
- ID format inconsistency (ULID vs integer)
- Null vs empty array in responses
- Missing parameter validation

## Contributing

When adding new endpoints:

1. Add test cases to `internal/server/server_test.go`
2. Add endpoint to `test-api-endpoints.py`
3. Update this README with new endpoints
4. Run full test suite before committing

## Test Results Location

- **Automated tests:** Console output and `logs/test-output.log`
- **Manual tests:** `test-results.json` (gitignored)
- **Coverage reports:** `coverage.html` (generated with `-coverprofile`)

## Troubleshooting

### Server won't start

```bash
# Check if port 8484 is in use
lsof -i :8484

# Kill existing process
kill -9 <PID>
```

### Tests fail with "database not initialized"

```bash
# Ensure server is running
curl http://localhost:8484/health
```

### Python script errors

```bash
# Install/upgrade dependencies
pip3 install --upgrade requests

# Check Python version (requires 3.7+)
python3 --version
```

## Performance Baselines

Expected response times (from benchmarks):

- Health check: ~66µs
- List operations: ~25µs
- Config retrieval: ~9µs
- Database queries: <100µs

If response times exceed 500ms, investigate:

1. Database connection issues
2. Large result sets without pagination
3. Missing indexes
4. Slow disk I/O

## CI/CD Integration

Add to CI pipeline:

```yaml
# .github/workflows/test.yml
- name: Run API Tests
  run: |
    go test -v ./internal/server
    go test -bench=. ./internal/server
```

## Support

For issues with tests:

1. Check `docs/api-testing-summary.md` for known issues
2. Review server logs in `logs/`
3. Run with verbose output: `go test -v`
4. Check database state with backup tools
