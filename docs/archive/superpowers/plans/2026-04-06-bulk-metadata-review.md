# Bulk Metadata Review Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Background operation fetches best metadata candidates in parallel with rate limiting, stores structured results, and a review dialog lets users apply/skip individually or in bulk.

**Architecture:** New `operation_results` table stores structured candidate JSON per book per operation. Parallel workers (8 goroutines, shared rate limiter) call existing `SearchMetadataForBook`. Review dialog reads results via API, supports compact/two-column view with source filters and confidence slider. Operations dropdown enhanced to show recent completed operations.

**Tech Stack:** Go backend (gin, operations queue, rate limiter), React/TypeScript/MUI frontend, PebbleDB/SQLite storage

---

### Task 1: Migration 45 — operation_results table

**Files:**
- Modify: `internal/database/migrations.go` (add migration 45 to array ~line 302, add function at end)

- [ ] **Step 1: Add migration entry to migrations array**

In `internal/database/migrations.go`, after the migration 44 entry (~line 302):

```go
{
    Version:     45,
    Description: "Add operation_results table for structured operation output",
    Up:          migration045Up,
    Down:        nil,
},
```

- [ ] **Step 2: Write migration function**

At the end of `internal/database/migrations.go`:

```go
func migration045Up(store Store) error {
    sqliteStore, ok := store.(*SQLiteStore)
    if !ok {
        return nil
    }
    stmts := []string{
        `CREATE TABLE IF NOT EXISTS operation_results (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            operation_id TEXT NOT NULL,
            book_id TEXT NOT NULL,
            result_json TEXT NOT NULL,
            status TEXT NOT NULL DEFAULT 'matched',
            created_at DATETIME DEFAULT CURRENT_TIMESTAMP
        )`,
        `CREATE INDEX IF NOT EXISTS idx_op_results_op ON operation_results(operation_id)`,
        `CREATE INDEX IF NOT EXISTS idx_op_results_book ON operation_results(operation_id, book_id)`,
    }
    for _, stmt := range stmts {
        if _, err := sqliteStore.db.Exec(stmt); err != nil {
            log.Printf("  - [WARN] migration 45: %v (continuing)", err)
        }
    }
    log.Println("  - Created operation_results table")
    return nil
}
```

- [ ] **Step 3: Build and verify migration runs**

Run: `go build ./...`
Expected: Clean build

- [ ] **Step 4: Commit**

```bash
git add internal/database/migrations.go
git commit -m "feat: migration 45 adds operation_results table"
```

---

### Task 2: Store interface and SQLite implementation for operation_results

**Files:**
- Modify: `internal/database/store.go` (~line 167, add methods to Store interface)
- Modify: `internal/database/sqlite_store.go` (add implementations after operation log methods)
- Modify: `internal/database/pebble_store.go` (add stubs)
- Modify: `internal/database/mock_store.go` (add stubs)
- Modify: `internal/database/mocks/mock_store.go` (add stubs with Expecter)

- [ ] **Step 1: Define OperationResult struct in store.go**

In `internal/database/store.go`, after the `OperationSummaryLog` struct:

```go
// OperationResult holds a structured result for one item in an operation.
type OperationResult struct {
    ID          int       `json:"id"`
    OperationID string    `json:"operation_id"`
    BookID      string    `json:"book_id"`
    ResultJSON  string    `json:"result_json"`
    Status      string    `json:"status"` // "matched", "no_match", "error"
    CreatedAt   time.Time `json:"created_at"`
}
```

- [ ] **Step 2: Add methods to Store interface**

In `internal/database/store.go`, after the operation log methods (~line 167):

```go
// Operation results (structured output for batch operations)
CreateOperationResult(result *OperationResult) error
GetOperationResults(operationID string) ([]OperationResult, error)
GetRecentCompletedOperations(limit int) ([]Operation, error)
```

- [ ] **Step 3: Implement in SQLite store**

In `internal/database/sqlite_store.go`, after the operation summary log methods:

```go
func (s *SQLiteStore) CreateOperationResult(result *OperationResult) error {
    _, err := s.db.Exec(
        `INSERT INTO operation_results (operation_id, book_id, result_json, status, created_at)
         VALUES (?, ?, ?, ?, ?)`,
        result.OperationID, result.BookID, result.ResultJSON, result.Status,
        time.Now().Format(time.RFC3339),
    )
    return err
}

func (s *SQLiteStore) GetOperationResults(operationID string) ([]OperationResult, error) {
    rows, err := s.db.Query(
        `SELECT id, operation_id, book_id, result_json, status, created_at
         FROM operation_results WHERE operation_id = ? ORDER BY id`,
        operationID,
    )
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var results []OperationResult
    for rows.Next() {
        var r OperationResult
        if err := rows.Scan(&r.ID, &r.OperationID, &r.BookID, &r.ResultJSON, &r.Status, &r.CreatedAt); err != nil {
            return nil, err
        }
        results = append(results, r)
    }
    return results, rows.Err()
}

func (s *SQLiteStore) GetRecentCompletedOperations(limit int) ([]Operation, error) {
    rows, err := s.db.Query(
        `SELECT id, type, status, progress, total, message, error_message, created_at, updated_at
         FROM operations WHERE status IN ('completed', 'failed')
         ORDER BY updated_at DESC LIMIT ?`, limit,
    )
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var ops []Operation
    for rows.Next() {
        var op Operation
        var errMsg sql.NullString
        if err := rows.Scan(&op.ID, &op.Type, &op.Status, &op.Progress, &op.Total,
            &op.Message, &errMsg, &op.CreatedAt, &op.UpdatedAt); err != nil {
            return nil, err
        }
        if errMsg.Valid {
            op.ErrorMessage = errMsg.String
        }
        ops = append(ops, op)
    }
    return ops, rows.Err()
}
```

- [ ] **Step 4: Add PebbleStore stubs**

In `internal/database/pebble_store.go`, before the User Tags section:

```go
func (p *PebbleStore) CreateOperationResult(result *OperationResult) error { return nil }
func (p *PebbleStore) GetOperationResults(operationID string) ([]OperationResult, error) {
    return nil, nil
}
func (p *PebbleStore) GetRecentCompletedOperations(limit int) ([]Operation, error) {
    return nil, nil
}
```

- [ ] **Step 5: Add MockStore stubs**

In `internal/database/mock_store.go`:

```go
func (m *MockStore) CreateOperationResult(result *OperationResult) error { return nil }
func (m *MockStore) GetOperationResults(operationID string) ([]OperationResult, error) {
    return nil, nil
}
func (m *MockStore) GetRecentCompletedOperations(limit int) ([]Operation, error) {
    return nil, nil
}
```

In `internal/database/mocks/mock_store.go`, at the end:

```go
func (_mock *MockStore) CreateOperationResult(result *database.OperationResult) error {
    ret := _mock.Called(result)
    return ret.Error(0)
}

type MockStore_CreateOperationResult_Call struct{ *mock.Call }
func (_e *MockStore_Expecter) CreateOperationResult(result interface{}) *MockStore_CreateOperationResult_Call {
    return &MockStore_CreateOperationResult_Call{Call: _e.mock.On("CreateOperationResult", result)}
}
func (_c *MockStore_CreateOperationResult_Call) Run(run func(*database.OperationResult)) *MockStore_CreateOperationResult_Call {
    _c.Call.Run(func(args mock.Arguments) { run(args[0].(*database.OperationResult)) })
    return _c
}
func (_c *MockStore_CreateOperationResult_Call) Return(err error) *MockStore_CreateOperationResult_Call {
    _c.Call.Return(err)
    return _c
}
func (_c *MockStore_CreateOperationResult_Call) RunAndReturn(run func(*database.OperationResult) error) *MockStore_CreateOperationResult_Call {
    _c.Call.Return(run)
    return _c
}

func (_mock *MockStore) GetOperationResults(operationID string) ([]database.OperationResult, error) {
    ret := _mock.Called(operationID)
    var r0 []database.OperationResult
    if ret.Get(0) != nil {
        r0 = ret.Get(0).([]database.OperationResult)
    }
    return r0, ret.Error(1)
}

type MockStore_GetOperationResults_Call struct{ *mock.Call }
func (_e *MockStore_Expecter) GetOperationResults(operationID interface{}) *MockStore_GetOperationResults_Call {
    return &MockStore_GetOperationResults_Call{Call: _e.mock.On("GetOperationResults", operationID)}
}
func (_c *MockStore_GetOperationResults_Call) Run(run func(string)) *MockStore_GetOperationResults_Call {
    _c.Call.Run(func(args mock.Arguments) { run(args[0].(string)) })
    return _c
}
func (_c *MockStore_GetOperationResults_Call) Return(results []database.OperationResult, err error) *MockStore_GetOperationResults_Call {
    _c.Call.Return(results, err)
    return _c
}
func (_c *MockStore_GetOperationResults_Call) RunAndReturn(run func(string) ([]database.OperationResult, error)) *MockStore_GetOperationResults_Call {
    _c.Call.Return(run)
    return _c
}

func (_mock *MockStore) GetRecentCompletedOperations(limit int) ([]database.Operation, error) {
    ret := _mock.Called(limit)
    var r0 []database.Operation
    if ret.Get(0) != nil {
        r0 = ret.Get(0).([]database.Operation)
    }
    return r0, ret.Error(1)
}

type MockStore_GetRecentCompletedOperations_Call struct{ *mock.Call }
func (_e *MockStore_Expecter) GetRecentCompletedOperations(limit interface{}) *MockStore_GetRecentCompletedOperations_Call {
    return &MockStore_GetRecentCompletedOperations_Call{Call: _e.mock.On("GetRecentCompletedOperations", limit)}
}
func (_c *MockStore_GetRecentCompletedOperations_Call) Run(run func(int)) *MockStore_GetRecentCompletedOperations_Call {
    _c.Call.Run(func(args mock.Arguments) { run(args[0].(int)) })
    return _c
}
func (_c *MockStore_GetRecentCompletedOperations_Call) Return(ops []database.Operation, err error) *MockStore_GetRecentCompletedOperations_Call {
    _c.Call.Return(ops, err)
    return _c
}
func (_c *MockStore_GetRecentCompletedOperations_Call) RunAndReturn(run func(int) ([]database.Operation, error)) *MockStore_GetRecentCompletedOperations_Call {
    _c.Call.Return(run)
    return _c
}
```

- [ ] **Step 6: Add stub to cmd/commands_test.go stubStore**

```go
func (s *stubStore) CreateOperationResult(result *database.OperationResult) error { return nil }
func (s *stubStore) GetOperationResults(operationID string) ([]database.OperationResult, error) {
    return nil, nil
}
func (s *stubStore) GetRecentCompletedOperations(limit int) ([]database.Operation, error) {
    return nil, nil
}
```

- [ ] **Step 7: Build and run tests**

Run: `go build ./... && make test`
Expected: Clean build, all tests pass

- [ ] **Step 8: Commit**

```bash
git add internal/database/store.go internal/database/sqlite_store.go internal/database/pebble_store.go internal/database/mock_store.go internal/database/mocks/mock_store.go cmd/commands_test.go
git commit -m "feat: store interface for operation_results and recent completed operations"
```

---

### Task 3: Parallel metadata fetch operation handler

**Files:**
- Create: `internal/server/metadata_batch_candidates.go`
- Modify: `internal/server/server.go` (register endpoints ~line 1600)

- [ ] **Step 1: Create the batch candidates handler**

Create `internal/server/metadata_batch_candidates.go`:

```go
// file: internal/server/metadata_batch_candidates.go
// version: 1.0.0
// guid: <generate>

package server

import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "net/http"
    "sync"
    "sync/atomic"
    "time"

    "github.com/gin-gonic/gin"
    "github.com/jdfalk/audiobook-organizer/internal/database"
    "github.com/jdfalk/audiobook-organizer/internal/operations"
    "github.com/oklog/ulid/v2"
    "golang.org/x/time/rate"
)

// CandidateResult is the structured JSON stored per book in operation_results.
type CandidateResult struct {
    Book      CandidateBookInfo  `json:"book"`
    Candidate *MetadataCandidate `json:"candidate,omitempty"`
    Status    string             `json:"status"` // "matched", "no_match", "error"
    Error     string             `json:"error_message,omitempty"`
}

type CandidateBookInfo struct {
    ID            string `json:"id"`
    Title         string `json:"title"`
    Author        string `json:"author"`
    FilePath      string `json:"file_path"`
    ITunesPath    string `json:"itunes_path,omitempty"`
    CoverURL      string `json:"cover_url,omitempty"`
    Format        string `json:"format,omitempty"`
    Duration      int    `json:"duration_seconds,omitempty"`
    FileSize      int64  `json:"file_size_bytes,omitempty"`
}

func (s *Server) handleBatchFetchCandidates(c *gin.Context) {
    var body struct {
        BookIDs []string `json:"book_ids" binding:"required"`
    }
    if err := c.ShouldBindJSON(&body); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }
    if len(body.BookIDs) == 0 {
        c.JSON(http.StatusBadRequest, gin.H{"error": "book_ids is required"})
        return
    }

    store := database.GlobalStore
    if store == nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database not initialized"})
        return
    }

    opID := ulid.Make().String()
    if _, err := store.CreateOperation(opID, "metadata_candidate_fetch", nil); err != nil {
        internalError(c, "failed to create operation", err)
        return
    }

    bookIDs := body.BookIDs
    mfs := s.metadataFetchService

    operationFunc := func(ctx context.Context, progress operations.ProgressReporter) error {
        total := len(bookIDs)
        _ = progress.UpdateProgress(0, total, "Fetching metadata candidates...")

        // Shared rate limiters
        globalLimiter := rate.NewLimiter(rate.Limit(10), 10) // 10 req/s global
        var completed int64

        // Worker pool
        const numWorkers = 8
        bookCh := make(chan string, len(bookIDs))
        for _, id := range bookIDs {
            bookCh <- id
        }
        close(bookCh)

        var wg sync.WaitGroup
        for w := 0; w < numWorkers; w++ {
            wg.Add(1)
            go func() {
                defer wg.Done()
                for bookID := range bookCh {
                    if ctx.Err() != nil {
                        return
                    }

                    // Rate limit
                    _ = globalLimiter.Wait(ctx)

                    result := s.fetchCandidateForBook(mfs, store, bookID)

                    // Store result
                    resultJSON, _ := json.Marshal(result)
                    _ = store.CreateOperationResult(&database.OperationResult{
                        OperationID: opID,
                        BookID:      bookID,
                        ResultJSON:  string(resultJSON),
                        Status:      result.Status,
                    })

                    n := atomic.AddInt64(&completed, 1)
                    _ = progress.UpdateProgress(int(n), total,
                        fmt.Sprintf("Fetched %d/%d: %s", n, total, result.Book.Title))
                }
            }()
        }
        wg.Wait()

        return nil
    }

    if err := operations.GlobalQueue.Enqueue(opID, "metadata_candidate_fetch",
        operations.PriorityNormal, operationFunc); err != nil {
        internalError(c, "failed to enqueue operation", err)
        return
    }

    c.JSON(http.StatusOK, gin.H{"operation_id": opID, "book_count": len(bookIDs)})
}

func (s *Server) fetchCandidateForBook(mfs *MetadataFetchService, store database.Store, bookID string) CandidateResult {
    book, err := store.GetBookByID(bookID)
    if err != nil || book == nil {
        return CandidateResult{
            Book:   CandidateBookInfo{ID: bookID},
            Status: "error",
            Error:  "book not found",
        }
    }

    bookInfo := CandidateBookInfo{
        ID:       book.ID,
        Title:    book.Title,
        FilePath: book.FilePath,
        Format:   book.Format,
    }
    if book.Author != nil {
        bookInfo.Author = book.Author.Name
    }
    if book.CoverURL != nil {
        bookInfo.CoverURL = *book.CoverURL
    }
    if book.ITunesPath != nil {
        bookInfo.ITunesPath = *book.ITunesPath
    }
    if book.Duration != nil {
        bookInfo.Duration = *book.Duration
    }
    if book.FileSize != nil {
        bookInfo.FileSize = *book.FileSize
    }

    resp, err := mfs.SearchMetadataForBook(bookID, book.Title)
    if err != nil || resp == nil || len(resp.Results) == 0 {
        errMsg := "no results"
        if err != nil {
            errMsg = err.Error()
        }
        return CandidateResult{
            Book:   bookInfo,
            Status: "no_match",
            Error:  errMsg,
        }
    }

    // Pick the top-scoring candidate
    best := resp.Results[0]
    return CandidateResult{
        Book:      bookInfo,
        Candidate: &best,
        Status:    "matched",
    }
}

func (s *Server) handleGetOperationResults(c *gin.Context) {
    opID := c.Param("id")
    store := database.GlobalStore
    if store == nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database not initialized"})
        return
    }

    results, err := store.GetOperationResults(opID)
    if err != nil {
        internalError(c, "failed to get operation results", err)
        return
    }

    // Parse JSON and build response with summary
    var parsed []CandidateResult
    matched, noMatch, errors := 0, 0, 0
    for _, r := range results {
        var cr CandidateResult
        if jsonErr := json.Unmarshal([]byte(r.ResultJSON), &cr); jsonErr != nil {
            continue
        }
        parsed = append(parsed, cr)
        switch cr.Status {
        case "matched":
            matched++
        case "no_match":
            noMatch++
        default:
            errors++
        }
    }

    c.JSON(http.StatusOK, gin.H{
        "results": parsed,
        "summary": gin.H{
            "matched":  matched,
            "no_match": noMatch,
            "errors":   errors,
            "total":    len(parsed),
        },
    })
}

func (s *Server) handleBatchApplyCandidates(c *gin.Context) {
    var body struct {
        OperationID string   `json:"operation_id" binding:"required"`
        BookIDs     []string `json:"book_ids" binding:"required"`
    }
    if err := c.ShouldBindJSON(&body); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
        return
    }

    store := database.GlobalStore
    if store == nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database not initialized"})
        return
    }

    // Get all results for this operation
    results, err := store.GetOperationResults(body.OperationID)
    if err != nil {
        internalError(c, "failed to get results", err)
        return
    }

    // Build lookup by book ID
    resultByBook := make(map[string]*CandidateResult)
    for _, r := range results {
        var cr CandidateResult
        if jsonErr := json.Unmarshal([]byte(r.ResultJSON), &cr); jsonErr != nil {
            continue
        }
        resultByBook[r.BookID] = &cr
    }

    // Apply each selected book's candidate
    applied := 0
    applySet := make(map[string]bool, len(body.BookIDs))
    for _, id := range body.BookIDs {
        applySet[id] = true
    }

    for bookID := range applySet {
        cr, ok := resultByBook[bookID]
        if !ok || cr.Candidate == nil || cr.Status != "matched" {
            continue
        }

        // Apply candidate (DB update inline)
        _, applyErr := s.metadataFetchService.ApplyMetadataCandidate(bookID, *cr.Candidate, nil)
        if applyErr != nil {
            log.Printf("[WARN] batch-apply candidate for %s: %v", bookID, applyErr)
            continue
        }

        // Background file I/O
        go func(id string) {
            s.metadataFetchService.ApplyMetadataFileIO(id)
            if _, wbErr := s.metadataFetchService.WriteBackMetadataForBook(id); wbErr != nil {
                log.Printf("[WARN] batch-apply write-back for %s: %v", id, wbErr)
            }
            if GlobalWriteBackBatcher != nil {
                GlobalWriteBackBatcher.Enqueue(id)
            }
        }(bookID)

        applied++
    }

    c.JSON(http.StatusOK, gin.H{"applied": applied})
}

func (s *Server) handleGetRecentOperations(c *gin.Context) {
    store := database.GlobalStore
    if store == nil {
        c.JSON(http.StatusInternalServerError, gin.H{"error": "database not initialized"})
        return
    }

    ops, err := store.GetRecentCompletedOperations(10)
    if err != nil {
        internalError(c, "failed to get recent operations", err)
        return
    }

    c.JSON(http.StatusOK, gin.H{"operations": ops})
}
```

- [ ] **Step 2: Register endpoints in server.go**

In `internal/server/server.go`, after the existing metadata endpoints (~line 1604):

```go
protected.POST("/metadata/batch-fetch-candidates", s.handleBatchFetchCandidates)
protected.POST("/metadata/batch-apply-candidates", s.handleBatchApplyCandidates)
protected.GET("/operations/:id/results", s.handleGetOperationResults)
protected.GET("/operations/recent", s.handleGetRecentOperations)
```

- [ ] **Step 3: Build and run tests**

Run: `go build ./... && make test`
Expected: Clean build, all tests pass

- [ ] **Step 4: Commit**

```bash
git add internal/server/metadata_batch_candidates.go internal/server/server.go
git commit -m "feat: batch metadata candidate fetch with parallel workers and rate limiting"
```

---

### Task 4: Frontend API functions

**Files:**
- Modify: `web/src/services/api.ts`

- [ ] **Step 1: Add API functions**

In `web/src/services/api.ts`, after the existing metadata functions:

```typescript
export interface CandidateBookInfo {
  id: string;
  title: string;
  author: string;
  file_path: string;
  itunes_path?: string;
  cover_url?: string;
  format?: string;
  duration_seconds?: number;
  file_size_bytes?: number;
}

export interface CandidateResult {
  book: CandidateBookInfo;
  candidate?: MetadataCandidate;
  status: 'matched' | 'no_match' | 'error';
  error_message?: string;
}

export interface BatchFetchResponse {
  results: CandidateResult[];
  summary: { matched: number; no_match: number; errors: number; total: number };
}

export async function batchFetchCandidates(bookIds: string[]): Promise<{ operation_id: string }> {
  const response = await fetch(`${API_BASE}/metadata/batch-fetch-candidates`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ book_ids: bookIds }),
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to start batch fetch');
  return response.json();
}

export async function getOperationResults(operationId: string): Promise<BatchFetchResponse> {
  const response = await fetch(`${API_BASE}/operations/${operationId}/results`);
  if (!response.ok) throw await buildApiError(response, 'Failed to get operation results');
  return response.json();
}

export async function batchApplyCandidates(operationId: string, bookIds: string[]): Promise<{ applied: number }> {
  const response = await fetch(`${API_BASE}/metadata/batch-apply-candidates`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ operation_id: operationId, book_ids: bookIds }),
  });
  if (!response.ok) throw await buildApiError(response, 'Failed to apply candidates');
  return response.json();
}

export async function getRecentOperations(): Promise<{ operations: Operation[] }> {
  const response = await fetch(`${API_BASE}/operations/recent`);
  if (!response.ok) throw await buildApiError(response, 'Failed to get recent operations');
  return response.json();
}
```

- [ ] **Step 2: TypeScript check**

Run: `cd web && npx tsc --noEmit`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add web/src/services/api.ts
git commit -m "feat: API functions for batch metadata candidates"
```

---

### Task 5: Operations dropdown showing recent completed operations

**Files:**
- Modify: `web/src/components/layout/OperationsIndicator.tsx`

- [ ] **Step 1: Enhance the operations indicator**

Read the current `OperationsIndicator.tsx` to understand the existing structure, then add a "Recent" section below the active operations. It should:

- Fetch `api.getRecentOperations()` on mount and every 30 seconds
- Show last 10 completed operations with: type label, relative timestamp, status chip, result summary message
- For `metadata_candidate_fetch` operations, add a "Review Results" button
- Each row clickable → `navigate('/activity?op=${op.id}')`

The exact implementation depends on the current component structure — the worker should read the file first, understand the Popover/Menu pattern, and add the "Recent" section below the existing active operations list with a `Divider` separator.

- [ ] **Step 2: TypeScript check**

Run: `cd web && npx tsc --noEmit`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add web/src/components/layout/OperationsIndicator.tsx
git commit -m "feat: operations dropdown shows recent completed operations"
```

---

### Task 6: MetadataReviewDialog component

**Files:**
- Create: `web/src/components/audiobooks/MetadataReviewDialog.tsx`

- [ ] **Step 1: Create the review dialog**

Create `web/src/components/audiobooks/MetadataReviewDialog.tsx`. This is a large component (~400-500 lines). Key structure:

**Props:**
```typescript
interface MetadataReviewDialogProps {
  open: boolean;
  operationId: string;
  onClose: () => void;
  onComplete: () => void;
  toast: (message: string, severity?: 'success' | 'error' | 'warning' | 'info', action?: { label: string; onClick: () => void }) => void;
}
```

**State:**
- `results: CandidateResult[]` — loaded from API
- `loading: boolean`
- `selectedIds: Set<string>` — checked rows
- `rowStates: Map<string, 'pending' | 'applied' | 'skipped'>` — per-row state
- `sourceFilter: string | null` — filter by source
- `confidenceThreshold: number` — slider value (default 85)
- `viewMode: 'compact' | 'two-column'` — from settings or local toggle
- `expandedId: string | null` — which row is expanded in compact mode

**Top bar:** Stats chips, confidence slider (MUI Slider), source filter chips (same pattern as BulkMetadataSearchDialog), smart action buttons

**Smart actions:**
- "Apply High Confidence" — filters by threshold + has narrator, calls `api.batchApplyCandidates` with matching book IDs
- "Apply All Visible" — applies everything visible after source/confidence filter
- "Skip All Unmatched" — sets no_match/error rows to skipped state

**List rendering:**
- Filter `results` by `sourceFilter` and `confidenceThreshold`
- Compact mode: one-line per result with cover thumbnail (40x50), title arrow, score badge, source chip, Apply/Skip buttons. Click to expand.
- Two-column mode: card per result — left (current book info from `result.book`), right (proposed from `result.candidate`). Both covers shown side by side.
- Row state styling: applied = green tint + checkmark, skipped = gray + dimmed, error = red border

**Per-row Apply:** Calls `api.batchApplyCandidates(operationId, [bookId])`, updates `rowStates`, shows toast with Undo

**Bulk Apply Selected:** Checkbox per row, "Apply Selected (N)" button, calls `api.batchApplyCandidates(operationId, selectedBookIds)`, toast with "Undo All"

Use existing component patterns: Dialog from MUI, Chip for source filters (same `SOURCE_COLORS` map from BulkMetadataSearchDialog), Avatar for covers, Stack/Box for layout.

- [ ] **Step 2: TypeScript check**

Run: `cd web && npx tsc --noEmit`
Expected: No errors

- [ ] **Step 3: Commit**

```bash
git add web/src/components/audiobooks/MetadataReviewDialog.tsx
git commit -m "feat: metadata review dialog with compact/two-column view"
```

---

### Task 7: Wire into Library page

**Files:**
- Modify: `web/src/pages/Library.tsx`

- [ ] **Step 1: Add state and button**

In Library.tsx, add state:

```typescript
const [metadataReviewOpId, setMetadataReviewOpId] = useState<string | null>(null);
const [metadataReviewOpen, setMetadataReviewOpen] = useState(false);
```

Add "Fetch & Review Metadata" button in the batch actions toolbar (after "Merge as Versions" button):

```tsx
<Tooltip
  title={selectedAudiobooks.length < 2 ? 'Select 2+ books' : ''}
  disableHoverListener={selectedAudiobooks.length >= 2}
>
  <span>
    <Button
      size="small"
      variant="outlined"
      color="primary"
      onClick={async () => {
        try {
          const { operation_id } = await api.batchFetchCandidates(
            selectedAudiobooks.map((b) => b.id)
          );
          toast(`Fetching metadata for ${selectedAudiobooks.length} books...`, 'info');
          setMetadataReviewOpId(operation_id);
          // Poll for completion, then open dialog
          const poll = setInterval(async () => {
            try {
              const ops = await api.getActiveOperations();
              const op = ops.find((o) => o.id === operation_id);
              if (!op || op.status === 'completed' || op.status === 'failed') {
                clearInterval(poll);
                if (!op || op.status === 'completed') {
                  setMetadataReviewOpen(true);
                  toast('Metadata fetch complete — review results', 'success');
                } else {
                  toast('Metadata fetch failed', 'error');
                }
              }
            } catch { /* ignore poll errors */ }
          }, 2000);
        } catch (err) {
          toast('Failed to start metadata fetch', 'error');
        }
      }}
      disabled={selectedAudiobooks.length < 2}
    >
      Fetch & Review Metadata
    </Button>
  </span>
</Tooltip>
```

- [ ] **Step 2: Add dialog render**

After the other dialogs in Library.tsx:

```tsx
{metadataReviewOpId && (
  <MetadataReviewDialog
    open={metadataReviewOpen}
    operationId={metadataReviewOpId}
    onClose={() => {
      setMetadataReviewOpen(false);
      setMetadataReviewOpId(null);
    }}
    onComplete={() => {
      loadAudiobooks();
      setSelectedAudiobooks([]);
    }}
    toast={toast}
  />
)}
```

Import at top:

```typescript
import { MetadataReviewDialog } from '../components/audiobooks/MetadataReviewDialog';
```

- [ ] **Step 3: TypeScript check and build**

Run: `cd web && npx tsc --noEmit`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
git add web/src/pages/Library.tsx
git commit -m "feat: wire Fetch & Review Metadata button into library batch actions"
```

---

### Task 8: Settings — default view preference

**Files:**
- Modify: `internal/config/config.go` (add field to Config struct)
- Modify: `internal/config/persistence.go` (add to stringFallbacks)
- Modify: `internal/server/config_update_service.go` (add to string fields)

- [ ] **Step 1: Add config field**

In `internal/config/config.go`, in the Config struct:

```go
MetadataReviewDefaultView string `json:"metadata_review_default_view"` // "compact" or "two-column"
```

In the defaults function:

```go
MetadataReviewDefaultView: "compact",
```

- [ ] **Step 2: Add to persistence**

In `internal/config/persistence.go`, in the string settings section:

```go
case "metadata_review_default_view":
    AppConfig.MetadataReviewDefaultView = value
```

And in the settings map:

```go
"metadata_review_default_view": {AppConfig.MetadataReviewDefaultView, "string", false},
```

- [ ] **Step 3: Add to config update service**

In `internal/server/config_update_service.go`, find the string fields map and add:

```go
"metadata_review_default_view": &config.AppConfig.MetadataReviewDefaultView,
```

- [ ] **Step 4: Build and test**

Run: `go build ./... && make test`
Expected: Pass

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/persistence.go internal/server/config_update_service.go
git commit -m "feat: metadata_review_default_view user preference"
```

---

### Task 9: Build, deploy, and end-to-end test

**Files:** None new — integration test

- [ ] **Step 1: Full build**

Run: `make build-linux`
Expected: Frontend + backend build clean

- [ ] **Step 2: Deploy**

```bash
scp dist/audiobook-organizer-linux-amd64 <user>@unimatrixzero.local:/home/<user>/audiobook-organizer
ssh <user>@unimatrixzero.local 'sudo mv /home/<user>/audiobook-organizer /usr/local/bin/audiobook-organizer && sudo systemctl restart audiobook-organizer.service'
```

- [ ] **Step 3: Test the flow**

1. Open library, select 5-10 books
2. Click "Fetch & Review Metadata"
3. Verify operation appears in operations dropdown with progress
4. When complete, verify review dialog opens
5. Test source filter chips, confidence slider
6. Apply one book individually, verify "Applied" state
7. Check multiple rows, click "Apply Selected"
8. Verify undo toast works
9. Check operations dropdown shows the completed operation

- [ ] **Step 4: Commit any fixes**

```bash
git add -A
git commit -m "fix: end-to-end adjustments for bulk metadata review"
```
