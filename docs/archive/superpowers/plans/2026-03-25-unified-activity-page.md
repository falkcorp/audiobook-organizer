# Unified Activity Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge Operations + Activity into one page that captures ALL server logs, shows active operation progress, and replaces the separate Operations page entirely.

**Architecture:** A `teeWriter` replaces Go's `log.SetOutput()` to intercept all log output, parse it, and send entries through a buffered channel to `activity.db`. The frontend's `ActivityLog.tsx` is rewritten to include a pinned operations section, compound filter bar with source filtering, and visual hierarchy. The Operations page is deleted.

**Tech Stack:** Go (`io.Writer`, channels, `database/sql`), React + TypeScript + Material UI, Gin HTTP framework

**Spec:** `docs/superpowers/specs/2026-03-25-unified-activity-page-design.md`

---

## File Structure

| Action | File | Responsibility |
|--------|------|----------------|
| Create | `internal/server/activity_writer.go` | teeWriter (stdout + channel), log line parser, buffered batch inserter |
| Create | `internal/server/activity_writer_test.go` | Tests for log parsing, channel buffering, batch insert |
| Modify | `internal/database/activity_store.go` | Add `Search`, `Source`, `ExcludeSources` to `ActivityFilter`; add `GetDistinctSources()`; add source index |
| Modify | `internal/database/activity_store_test.go` | Tests for new filter fields and `GetDistinctSources()` |
| Modify | `internal/server/activity_service.go` | Add `GetDistinctSources()` method |
| Modify | `internal/server/activity_handlers.go` | Add `search`, `source`, `exclude_sources` params; add `listActivitySources` handler |
| Modify | `internal/server/activity_handlers_test.go` | Tests for new params and sources endpoint |
| Modify | `internal/server/server.go` | Wire teeWriter, remove `globalActivityRecorder`, add sources route, startup entry via writer |
| Modify | `internal/logger/standard.go` | Remove `globalActivityRecorder` and dual-write code |
| Modify | `internal/logger/operation.go` | Remove `activityRecorder` field and dual-write code |
| Modify | `web/src/services/activityApi.ts` | Add `search`, `source`, `exclude_sources` params; add `fetchActivitySources()` |
| Rewrite | `web/src/pages/ActivityLog.tsx` | Pinned ops, compound filters, source dropdown, operation detail |
| Modify | `web/src/App.tsx` | Replace Operations route with redirect |
| Modify | `web/src/components/layout/Sidebar.tsx` | Remove Operations nav item |
| Delete | `web/src/pages/Operations.tsx` | Replaced by unified Activity page |

---

## Task 1: ActivityStore — Add Search, Source, ExcludeSources Filters + Source Index

**Files:**
- Modify: `internal/database/activity_store.go`
- Modify: `internal/database/activity_store_test.go`

- [ ] **Step 1: Write the failing test — search filter**

```go
func TestActivityStore_SearchFilter(t *testing.T) {
	store := newTestActivityStore(t)
	defer store.Close()

	store.Record(ActivityEntry{Tier: "change", Type: "scan", Level: "info", Source: "scanner", Summary: "Found new book: Project Hail Mary"})
	store.Record(ActivityEntry{Tier: "change", Type: "scan", Level: "info", Source: "scanner", Summary: "Found new book: The Martian"})
	store.Record(ActivityEntry{Tier: "debug", Type: "system", Level: "info", Source: "server", Summary: "Server started"})

	entries, total, err := store.Query(ActivityFilter{Search: "Hail Mary"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 1 {
		t.Errorf("expected 1 match for 'Hail Mary', got %d", total)
	}
	if len(entries) != 1 || entries[0].Summary != "Found new book: Project Hail Mary" {
		t.Errorf("wrong entry: %v", entries)
	}
}
```

- [ ] **Step 2: Write the failing test — source and exclude_sources filters**

```go
func TestActivityStore_SourceFilters(t *testing.T) {
	store := newTestActivityStore(t)
	defer store.Close()

	store.Record(ActivityEntry{Tier: "debug", Type: "system", Level: "info", Source: "gin", Summary: "GET /health 200"})
	store.Record(ActivityEntry{Tier: "debug", Type: "system", Level: "info", Source: "gin", Summary: "GET /api/v1/books 200"})
	store.Record(ActivityEntry{Tier: "debug", Type: "system", Level: "info", Source: "scheduler", Summary: "Next sync in 28m"})
	store.Record(ActivityEntry{Tier: "debug", Type: "system", Level: "info", Source: "metadata", Summary: "Extracting tags"})

	// Filter to single source
	entries, total, _ := store.Query(ActivityFilter{Source: "scheduler"})
	if total != 1 {
		t.Errorf("source=scheduler: expected 1, got %d", total)
	}

	// Exclude sources
	entries, total, _ = store.Query(ActivityFilter{ExcludeSources: []string{"gin"}})
	if total != 2 {
		t.Errorf("exclude gin: expected 2, got %d", total)
	}
	for _, e := range entries {
		if e.Source == "gin" {
			t.Error("gin entry should be excluded")
		}
	}
}
```

- [ ] **Step 3: Write the failing test — GetDistinctSources**

```go
func TestActivityStore_GetDistinctSources(t *testing.T) {
	store := newTestActivityStore(t)
	defer store.Close()

	store.Record(ActivityEntry{Tier: "debug", Type: "system", Level: "info", Source: "gin", Summary: "req 1"})
	store.Record(ActivityEntry{Tier: "debug", Type: "system", Level: "info", Source: "gin", Summary: "req 2"})
	store.Record(ActivityEntry{Tier: "change", Type: "scan", Level: "info", Source: "scanner", Summary: "scan 1"})
	store.Record(ActivityEntry{Tier: "debug", Type: "system", Level: "info", Source: "metadata", Summary: "meta 1"})

	// Unfiltered
	sources, err := store.GetDistinctSources(ActivityFilter{})
	if err != nil {
		t.Fatalf("GetDistinctSources: %v", err)
	}
	if len(sources) != 3 {
		t.Errorf("expected 3 sources, got %d: %v", len(sources), sources)
	}

	// Filter-aware (tier=debug only)
	sources, err = store.GetDistinctSources(ActivityFilter{Tier: "debug"})
	if err != nil {
		t.Fatalf("GetDistinctSources filtered: %v", err)
	}
	if len(sources) != 2 {
		t.Errorf("expected 2 debug sources, got %d: %v", len(sources), sources)
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/database/ -run "TestActivityStore_Search|TestActivityStore_Source|TestActivityStore_GetDistinct" -v`
Expected: FAIL — undefined fields/methods

- [ ] **Step 5: Implement — add fields to ActivityFilter**

In `internal/database/activity_store.go`, add to `ActivityFilter` struct:

```go
Search         string   // LIKE %search% on summary
Source         string   // show only this source
ExcludeSources []string // hide these sources
```

- [ ] **Step 6: Implement — update buildActivityWhere**

Add to `buildActivityWhere()` after the existing tag filtering:

```go
if f.Search != "" {
	clauses = append(clauses, "summary LIKE ?")
	args = append(args, "%"+f.Search+"%")
}
if f.Source != "" {
	clauses = append(clauses, "source = ?")
	args = append(args, f.Source)
}
for _, src := range f.ExcludeSources {
	clauses = append(clauses, "(source != ? OR source IS NULL)")
	args = append(args, src)
}
```

- [ ] **Step 7: Implement — add GetDistinctSources**

```go
// SourceCount holds a source name and its entry count.
type SourceCount struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}

// GetDistinctSources returns all distinct source values with counts.
// Respects filter params (tier, level, since, until) so counts match the current view.
func (s *ActivityStore) GetDistinctSources(f ActivityFilter) ([]SourceCount, error) {
	where, args := buildActivityWhere(f)
	query := "SELECT source, COUNT(*) as cnt FROM activity_log" + where + " GROUP BY source ORDER BY cnt DESC"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("get distinct sources: %w", err)
	}
	defer rows.Close()

	var sources []SourceCount
	for rows.Next() {
		var sc SourceCount
		if err := rows.Scan(&sc.Source, &sc.Count); err != nil {
			return nil, err
		}
		sources = append(sources, sc)
	}
	return sources, rows.Err()
}
```

- [ ] **Step 8: Add source index to createSchema**

In `createSchema()`, add:

```sql
CREATE INDEX IF NOT EXISTS idx_activity_source ON activity_log(source);
```

- [ ] **Step 9: Run tests to verify they pass**

Run: `go test ./internal/database/ -run "TestActivityStore" -v`
Expected: ALL PASS

- [ ] **Step 10: Commit**

```bash
git add internal/database/activity_store.go internal/database/activity_store_test.go
git commit -m "feat: add search, source, exclude_sources filters and GetDistinctSources to ActivityStore"
```

---

## Task 2: Activity Handlers — Search, Source, ExcludeSources Params + Sources Endpoint

**Files:**
- Modify: `internal/server/activity_handlers.go`
- Modify: `internal/server/activity_handlers_test.go`
- Modify: `internal/server/server.go` (add route)

- [ ] **Step 1: Write the failing test — search param**

```go
func TestListActivity_SearchParam(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "search_test.db")
	store, err := database.NewActivityStore(dbPath)
	require.NoError(t, err)
	defer store.Close()

	svc := NewActivityService(store)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	srv := &Server{activityService: svc}
	r.GET("/api/v1/activity", srv.listActivity)

	_ = svc.Record(database.ActivityEntry{Tier: "change", Type: "scan", Source: "scanner", Summary: "Found: Project Hail Mary"})
	_ = svc.Record(database.ActivityEntry{Tier: "change", Type: "scan", Source: "scanner", Summary: "Found: The Martian"})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/activity?search=Hail+Mary", nil)
	r.ServeHTTP(w, req)

	var resp struct {
		Entries []database.ActivityEntry `json:"entries"`
		Total   int                      `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, 1, resp.Total)
}
```

- [ ] **Step 2: Write the failing test — sources endpoint**

```go
func TestListActivitySources(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "sources_test.db")
	store, err := database.NewActivityStore(dbPath)
	require.NoError(t, err)
	defer store.Close()

	svc := NewActivityService(store)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	srv := &Server{activityService: svc}
	r.GET("/api/v1/activity/sources", srv.listActivitySources)

	_ = svc.Record(database.ActivityEntry{Tier: "debug", Type: "system", Source: "gin", Summary: "req"})
	_ = svc.Record(database.ActivityEntry{Tier: "debug", Type: "system", Source: "gin", Summary: "req2"})
	_ = svc.Record(database.ActivityEntry{Tier: "change", Type: "scan", Source: "scanner", Summary: "scan"})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/activity/sources", nil)
	r.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)

	var resp struct {
		Sources []database.SourceCount `json:"sources"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, 2, len(resp.Sources))
	assert.Equal(t, "gin", resp.Sources[0].Source) // highest count first
	assert.Equal(t, 2, resp.Sources[0].Count)
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/server/ -run "TestListActivity_Search|TestListActivitySources" -v`
Expected: FAIL

- [ ] **Step 4: Implement — add search/source/exclude_sources to listActivity**

In `activity_handlers.go`, in `listActivity()`, add after existing query param parsing:

```go
filter.Search = c.Query("search")
filter.Source = c.Query("source")
if v := c.Query("exclude_sources"); v != "" {
	for _, src := range strings.Split(v, ",") {
		src = strings.TrimSpace(src)
		if src != "" {
			filter.ExcludeSources = append(filter.ExcludeSources, src)
		}
	}
}
```

- [ ] **Step 5: Implement — add listActivitySources handler**

Add to `activity_handlers.go`:

```go
// listActivitySources handles GET /api/v1/activity/sources.
// Returns distinct sources with counts, respecting tier/level/since/until filters.
func (s *Server) listActivitySources(c *gin.Context) {
	if s.activityService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "activity log not available"})
		return
	}

	filter := database.ActivityFilter{
		Tier:  c.Query("tier"),
		Level: c.Query("level"),
	}
	if v := c.Query("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Since = &t
		}
	}
	if v := c.Query("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Until = &t
		}
	}

	sources, err := s.activityService.GetDistinctSources(filter)
	if err != nil {
		internalError(c, "failed to get sources", err)
		return
	}
	if sources == nil {
		sources = []database.SourceCount{}
	}
	c.JSON(http.StatusOK, gin.H{"sources": sources})
}
```

- [ ] **Step 6: Add GetDistinctSources to ActivityService**

In `activity_service.go`:

```go
func (s *ActivityService) GetDistinctSources(filter database.ActivityFilter) ([]database.SourceCount, error) {
	return s.store.GetDistinctSources(filter)
}
```

- [ ] **Step 7: Add route in server.go**

In `setupRoutes()`, after the existing `protected.GET("/activity", s.listActivity)` line, add:

```go
protected.GET("/activity/sources", s.listActivitySources)
```

- [ ] **Step 8: Run tests**

Run: `go test ./internal/server/ -run "TestListActivity_Search|TestListActivitySources" -v`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/server/activity_handlers.go internal/server/activity_handlers_test.go internal/server/activity_service.go internal/server/server.go
git commit -m "feat: add search, source filtering and GET /api/v1/activity/sources endpoint"
```

---

## Task 3: Activity Writer — teeWriter + Log Parser + Buffered Channel

**Files:**
- Create: `internal/server/activity_writer.go`
- Create: `internal/server/activity_writer_test.go`

- [ ] **Step 1: Write the failing test — parseLogLine**

```go
package server

import (
	"testing"
)

func TestParseLogLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		level   string
		source  string
		message string
	}{
		{"info with subsystem", "2026/03/25 17:35:08 logger.go:103: [info] scheduler: Next sync in 28m", "info", "scheduler", "Next sync in 28m"},
		{"warn with subsystem", "2026/03/25 17:35:08 server.go:874: [warn] server: No params found", "warn", "server", "No params found"},
		{"debug with subsystem", "2026/03/25 17:35:08 logger.go:103: [debug] metadata: extracting tags", "debug", "metadata", "extracting tags"},
		{"error with subsystem", "2026/03/25 17:35:08 queue.go:271: [error] queue: operation failed", "error", "queue", "operation failed"},
		{"GIN log", "[GIN] 2026/03/25 - 17:35:11 | 200 |    1.44s |    192.0.2.164 | GET      \"/api/v1/health\"", "info", "gin", "200 |    1.44s |    192.0.2.164 | GET      \"/api/v1/health\""},
		{"plain log", "2026/03/25 17:35:08 server.go:965: Starting HTTPS server on 0.0.0.0:8484", "info", "server", "Starting HTTPS server on 0.0.0.0:8484"},
		{"no prefix", "something unexpected happened", "info", "server", "something unexpected happened"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			level, source, message := parseLogLine(tt.line)
			if level != tt.level {
				t.Errorf("level: got %q, want %q", level, tt.level)
			}
			if source != tt.source {
				t.Errorf("source: got %q, want %q", source, tt.source)
			}
			if message != tt.message {
				t.Errorf("message: got %q, want %q", message, tt.message)
			}
		})
	}
}
```

- [ ] **Step 2: Write the failing test — activityWriter captures log output**

```go
func TestActivityWriter_CapturesLogs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "activity_writer_test.db")
	store, err := database.NewActivityStore(dbPath)
	if err != nil {
		t.Fatalf("NewActivityStore: %v", err)
	}
	defer store.Close()

	w := newActivityWriter(store, 100)
	defer w.Stop()

	// Write log lines through the writer
	fmt.Fprintln(w, "2026/03/25 17:35:08 logger.go:103: [info] scheduler: Sync started")
	fmt.Fprintln(w, "[GIN] 2026/03/25 - 17:35:11 | 200 | 1.44s | 127.0.0.1 | GET \"/health\"")
	fmt.Fprintln(w, "2026/03/25 17:35:08 logger.go:103: [debug] metadata: extracting tags for file.m4b")

	// Flush
	w.Flush()

	entries, total, err := store.Query(database.ActivityFilter{Limit: 100})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 3 {
		t.Fatalf("expected 3 entries, got %d", total)
	}

	// Verify first entry
	found := false
	for _, e := range entries {
		if e.Source == "scheduler" && e.Level == "info" {
			found = true
			if e.Tier != "debug" {
				t.Errorf("system logs should be tier=debug, got %s", e.Tier)
			}
		}
	}
	if !found {
		t.Error("scheduler info entry not found")
	}
}
```

- [ ] **Step 3: Write the failing test — channel backpressure drops debug**

```go
func TestActivityWriter_DropsDebugOnBackpressure(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "backpressure_test.db")
	store, err := database.NewActivityStore(dbPath)
	if err != nil {
		t.Fatalf("NewActivityStore: %v", err)
	}
	defer store.Close()

	// Tiny channel (size 2), don't start drain goroutine — channel will fill up
	w := newActivityWriter(store, 2)
	// NOT calling w.Start() — drain goroutine won't run, so channel fills up

	// These two fill the channel
	fmt.Fprintln(w, "2026/03/25 17:00:00 x.go:1: [info] server: entry1")
	fmt.Fprintln(w, "2026/03/25 17:00:00 x.go:1: [info] server: entry2")

	// This debug entry should be silently dropped (channel full)
	fmt.Fprintln(w, "2026/03/25 17:00:00 x.go:1: [debug] metadata: dropped")

	// Non-blocking — should not hang. If it hangs, the test times out.
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test ./internal/server/ -run "TestParseLogLine|TestActivityWriter" -v`
Expected: FAIL — undefined

- [ ] **Step 5: Implement activity_writer.go**

```go
// file: internal/server/activity_writer.go
package server

import (
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jdfalk/audiobook-organizer/internal/database"
)

// activityWriter is an io.Writer that tees log output to stdout
// and sends parsed entries through a buffered channel to activity.db.
type activityWriter struct {
	stdout  io.Writer
	ch      chan database.ActivityEntry
	store   *database.ActivityStore
	done    chan struct{}
	wg      sync.WaitGroup
	mu      sync.Mutex
	partial string // incomplete line buffer
	closed  atomic.Bool
}

// newActivityWriter creates a writer that sends to both stdout and the activity store.
func newActivityWriter(store *database.ActivityStore, chanSize int) *activityWriter {
	w := &activityWriter{
		stdout: os.Stdout,
		ch:     make(chan database.ActivityEntry, chanSize),
		store:  store,
		done:   make(chan struct{}),
	}
	return w
}

// Start begins the background drain goroutine.
func (w *activityWriter) Start() {
	w.wg.Add(1)
	go w.drain()
}

// Write implements io.Writer. Writes to stdout and parses log lines for the activity channel.
func (w *activityWriter) Write(p []byte) (n int, err error) {
	// Always write to stdout first
	n, err = w.stdout.Write(p)

	w.mu.Lock()
	defer w.mu.Unlock()

	data := w.partial + string(p)
	w.partial = ""

	for {
		idx := strings.IndexByte(data, '\n')
		if idx < 0 {
			w.partial = data // save incomplete line
			break
		}
		line := strings.TrimRight(data[:idx], "\r")
		data = data[idx+1:]
		if line != "" {
			w.sendEntry(line)
		}
	}

	return n, nil
}

func (w *activityWriter) sendEntry(line string) {
	if w.closed.Load() {
		return // writer is shutting down
	}

	level, source, message := parseLogLine(line)

	entry := database.ActivityEntry{
		Tier:    "debug", // all captured logs are debug tier
		Type:    "system",
		Level:   level,
		Source:  source,
		Summary: message,
	}

	// Non-blocking send
	select {
	case w.ch <- entry:
	default:
		// Channel full — drop debug silently, warn for others
		if level != "debug" {
			w.stdout.Write([]byte("[WARN] activity channel full, dropped: " + message + "\n"))
		}
	}
}

// drain reads from the channel and batch-inserts into the store.
func (w *activityWriter) drain() {
	defer w.wg.Done()

	batch := make([]database.ActivityEntry, 0, 100)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case entry := <-w.ch:
			batch = append(batch, entry)
			if len(batch) >= 100 {
				w.writeBatch(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				w.writeBatch(batch)
				batch = batch[:0]
			}
		case <-w.done:
			// Drain remaining entries from channel
		drainLoop:
			for {
				select {
				case entry := <-w.ch:
					batch = append(batch, entry)
				default:
					break drainLoop
				}
			}
			if len(batch) > 0 {
				w.writeBatch(batch)
			}
			return
		}
	}
}

func (w *activityWriter) writeBatch(entries []database.ActivityEntry) {
	for _, e := range entries {
		w.store.Record(e)
	}
}

// Flush drains any pending entries synchronously (for testing).
func (w *activityWriter) Flush() {
	// Drain channel
	for {
		select {
		case e := <-w.ch:
			w.store.Record(e)
		default:
			return
		}
	}
}

// Stop signals the drain goroutine to finish and waits.
func (w *activityWriter) Stop() {
	w.closed.Store(true) // prevent new sends
	select {
	case w.done <- struct{}{}:
	default:
	}
	w.wg.Wait()
}

// parseLogLine extracts level, source, and message from a log line.
func parseLogLine(line string) (level, source, message string) {
	// GIN logs: [GIN] YYYY/MM/DD - HH:MM:SS | STATUS | ...
	if strings.HasPrefix(line, "[GIN]") {
		rest := line[5:] // after "[GIN]"
		// Skip timestamp: "YYYY/MM/DD - HH:MM:SS | "
		if idx := strings.Index(rest, "| "); idx >= 0 {
			message = strings.TrimSpace(rest[idx+2:])
		} else {
			message = strings.TrimSpace(rest)
		}
		return "info", "gin", message
	}

	// Standard Go log: "YYYY/MM/DD HH:MM:SS file.go:NN: [level] source: message"
	// or: "YYYY/MM/DD HH:MM:SS file.go:NN: plain message"
	work := line

	// Strip date/time prefix (YYYY/MM/DD HH:MM:SS)
	if len(work) > 20 && work[4] == '/' && work[7] == '/' && work[10] == ' ' {
		// Find the file:line: part
		if idx := strings.Index(work[20:], ": "); idx >= 0 {
			work = work[20+idx+2:]
		} else {
			work = work[20:]
		}
	}

	work = strings.TrimSpace(work)

	// Check for [level] prefix
	if len(work) > 2 && work[0] == '[' {
		if end := strings.Index(work, "] "); end > 0 {
			level = strings.ToLower(work[1:end])
			work = work[end+2:]

			// Check for "source: message" pattern
			if idx := strings.Index(work, ": "); idx > 0 && idx < 30 {
				source = work[:idx]
				message = work[idx+2:]
				return level, source, message
			}
			return level, "server", work
		}
	}

	// No [level] prefix — plain message
	return "info", "server", work
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/server/ -run "TestParseLogLine|TestActivityWriter" -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/server/activity_writer.go internal/server/activity_writer_test.go
git commit -m "feat: add teeWriter with log parser and buffered channel for global log capture"
```

---

## Task 4: Wire teeWriter + Remove globalActivityRecorder

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/logger/standard.go`
- Modify: `internal/logger/operation.go`

- [ ] **Step 1: Add activityWriter field to Server struct**

In `server.go`, add to `Server` struct:

```go
activityWriter *activityWriter
```

- [ ] **Step 2: Initialize teeWriter in NewServer()**

After the activity service creation block (where `activityDBPath` is opened), replace the `globalActivityRecorder` setup with:

```go
if server.activityService != nil {
	// Global log capture via teeWriter
	aw := newActivityWriter(server.activityService.Store(), 10000)
	aw.Start()
	server.activityWriter = aw
	log.SetOutput(aw) // Redirect ALL log.Printf output through the writer

	// Record startup entry directly (not through log capture to ensure it appears)
	_ = server.activityService.Record(database.ActivityEntry{
		Tier: "debug", Type: "system", Level: "info",
		Source: "server", Summary: "Server started, activity log initialized",
	})
	log.Println("[INFO] Activity log service initialized and recording")
}
```

- [ ] **Step 3: Remove globalActivityRecorder setup from server.go**

Delete the block that sets `logger.SetGlobalActivityRecorder(...)` (around line 783-787).

- [ ] **Step 4: Remove globalActivityRecorder from logger/standard.go**

Remove:
- The `globalActivityRecorderMu` and `globalActivityRecorder` variables
- `SetGlobalActivityRecorder()` function
- `getGlobalActivityRecorder()` function
- The `activityRecorder` field from `StandardLogger`
- The `SetActivityRecorder()` method
- The dual-write block in `log()` (lines 83-94 that check `l.activityRecorder`)
- The `activityRecorder` propagation in `With()`

- [ ] **Step 5: Remove activityRecorder from logger/operation.go**

Remove:
- The `activityRecorder` field from `OperationLogger`
- Any `SetActivityRecorder` or recorder propagation
- The dual-write block in `OperationLogger.log()`

- [ ] **Step 6: Add Stop to server shutdown**

Find where `activityService.Store().Close()` is called and add before it:

```go
if s.activityWriter != nil {
	s.activityWriter.Stop()
}
```

- [ ] **Step 7: Build and test**

Run: `go build ./... && go test ./internal/server/ -run "TestActivity" -v && go test ./internal/logger/ -v`
Expected: PASS (some logger tests may need updating if they referenced activityRecorder)

- [ ] **Step 8: Commit**

```bash
git add internal/server/server.go internal/logger/standard.go internal/logger/operation.go
git commit -m "feat: wire teeWriter for global log capture, remove globalActivityRecorder dual-write"
```

---

## Task 5: Frontend — API Client Updates

**Files:**
- Modify: `web/src/services/activityApi.ts`

- [ ] **Step 1: Add new filter params and fetchActivitySources**

Add to `ActivityFilter` interface:

```typescript
search?: string;
source?: string;
exclude_sources?: string;  // comma-separated
```

Add new function:

```typescript
export interface SourceCount {
  source: string;
  count: number;
}

export interface SourcesResponse {
  sources: SourceCount[];
}

export async function fetchActivitySources(filter: Partial<ActivityFilter> = {}): Promise<SourcesResponse> {
  const params = new URLSearchParams();
  if (filter.tier) params.set('tier', filter.tier);
  if (filter.level) params.set('level', filter.level);
  if (filter.since) params.set('since', filter.since);
  if (filter.until) params.set('until', filter.until);
  const url = `${API_BASE}/activity/sources?${params.toString()}`;
  const res = await fetch(url);
  if (!res.ok) throw new Error(`Sources API error: ${res.status}`);
  return res.json();
}
```

- [ ] **Step 2: Commit**

```bash
git add web/src/services/activityApi.ts
git commit -m "feat: add search, source, exclude_sources params and fetchActivitySources to API client"
```

---

## Task 6: Frontend — Rewrite ActivityLog.tsx (Pinned Ops + Compound Filters + Source Dropdown)

**Files:**
- Rewrite: `web/src/pages/ActivityLog.tsx`

This is the largest frontend task. The page has four sections:

1. Header with refresh
2. Pinned operations (polls `/api/v1/operations/active`)
3. Compound filter bar (text search, tier chips, type/level dropdowns, date range, sources dropdown)
4. Activity feed with pagination

- [ ] **Step 1: Read current ActivityLog.tsx and Operations.tsx**

Read both files to understand existing patterns, imports, and API calls.

- [ ] **Step 2: Write the new ActivityLog.tsx**

Full rewrite. Key features:
- **Pinned operations** section at top, polls `getActiveOperations()` every 3s, shows progress bars, cancel button, pin toggle (localStorage `activity-ops-pinned`)
- **Filter bar**: text search input, tier chips (audit/change/debug, debug OFF by default), type dropdown, level dropdown, since/until date inputs, sources dropdown
- **Sources dropdown**: fetches from `fetchActivitySources()`, checkboxes per source with counts, All/None/Reset, badge showing hidden count, localStorage persistence (`activity-source-prefs`)
- **Feed**: table rows with time, level chip, type chip, summary, source, tags. Color-coded backgrounds (amber warn, red error, green completion). Debug at opacity 0.6. "view operation →" links. Pagination.
- **Operation actions**: cancel on active ops, revert icon on organize/metadata_fetch entries (with confirmation dialog), clear stale button in pinned section header.

The component should be ~400-500 lines. Use existing MUI patterns from the codebase.

- [ ] **Step 3: Build frontend**

Run: `cd web && npm run build`
Expected: Build succeeds

- [ ] **Step 4: Commit**

```bash
git add web/src/pages/ActivityLog.tsx
git commit -m "feat: rewrite ActivityLog with pinned ops, compound filters, source dropdown"
```

---

## Task 7: Frontend — Remove Operations Page + Update Nav/Routes

**Files:**
- Delete: `web/src/pages/Operations.tsx`
- Modify: `web/src/App.tsx`
- Modify: `web/src/components/layout/Sidebar.tsx`

- [ ] **Step 1: Update App.tsx — replace Operations route with redirect**

Change:
```tsx
<Route path="/operations" element={<Operations />} />
```
To:
```tsx
<Route path="/operations" element={<Navigate to="/activity" replace />} />
```

Remove the `Operations` import.

- [ ] **Step 2: Update Sidebar.tsx — remove Operations nav item**

Remove this line from `menuItems` array:
```tsx
{ text: 'Operations', icon: <ListAltIcon />, path: '/operations' },
```

Remove the `ListAltIcon` import if no longer used elsewhere.

- [ ] **Step 3: Delete Operations.tsx**

```bash
rm web/src/pages/Operations.tsx
```

- [ ] **Step 4: Build frontend**

Run: `cd web && npm run build`
Expected: Build succeeds (no references to deleted Operations component)

- [ ] **Step 5: Commit**

```bash
git add -A web/src/
git commit -m "feat: remove Operations page, redirect /operations to /activity"
```

---

## Task 8: Integration Test + Full Build Verification

**Files:**
- Modify: `internal/server/activity_integration_test.go`

- [ ] **Step 1: Add integration test for teeWriter round-trip**

```go
func TestActivity_Integration_TeeWriterCapture(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tee_integ.db")
	store, err := database.NewActivityStore(dbPath)
	if err != nil {
		t.Fatalf("NewActivityStore: %v", err)
	}
	defer store.Close()

	// Create writer and capture some logs
	w := newActivityWriter(store, 1000)
	w.Start()

	fmt.Fprintln(w, "2026/03/25 17:35:08 logger.go:103: [info] scheduler: iTunes sync started")
	fmt.Fprintln(w, "[GIN] 2026/03/25 - 17:35:11 | 200 |    1.44s |    192.0.2.164 | GET      \"/api/v1/health\"")
	fmt.Fprintln(w, "2026/03/25 17:35:08 logger.go:103: [warn] server: No params found for scan")

	w.Stop() // flushes

	// Query via store
	entries, total, err := store.Query(database.ActivityFilter{Limit: 100})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total < 3 {
		t.Fatalf("expected at least 3 entries, got %d", total)
	}

	// Verify source filtering works
	entries, total, _ = store.Query(database.ActivityFilter{Source: "gin"})
	if total != 1 {
		t.Errorf("expected 1 gin entry, got %d", total)
	}

	entries, total, _ = store.Query(database.ActivityFilter{ExcludeSources: []string{"gin"}})
	if total < 2 {
		t.Errorf("expected at least 2 non-gin entries, got %d", total)
	}

	// Verify search works
	entries, total, _ = store.Query(database.ActivityFilter{Search: "iTunes"})
	if total != 1 {
		t.Errorf("expected 1 entry matching 'iTunes', got %d", total)
	}
	_ = entries
}
```

- [ ] **Step 2: Run all activity tests**

Run: `go test ./internal/database/ -run TestActivityStore -v && go test ./internal/server/ -run TestActivity -v`
Expected: ALL PASS

- [ ] **Step 3: Full backend build**

Run: `go build ./...`
Expected: PASS

- [ ] **Step 4: Full frontend build**

Run: `cd web && npm run build`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/server/activity_integration_test.go
git commit -m "test: add teeWriter integration test with source filtering and search"
```

---

## Summary

| Task | Description | Files | Steps |
|------|-------------|-------|-------|
| 1 | Store: search/source/exclude filters + GetDistinctSources | `activity_store.go`, test | 10 |
| 2 | Handlers: new params + sources endpoint | `activity_handlers.go`, test, `server.go` | 9 |
| 3 | teeWriter: log parser + buffered channel | `activity_writer.go`, test | 7 |
| 4 | Wire teeWriter + remove globalActivityRecorder | `server.go`, `standard.go`, `operation.go` | 8 |
| 5 | Frontend API client updates | `activityApi.ts` | 2 |
| 6 | Rewrite ActivityLog.tsx | `ActivityLog.tsx` | 4 |
| 7 | Remove Operations page + nav/routes | `Operations.tsx`, `App.tsx`, `Sidebar.tsx` | 5 |
| 8 | Integration test + full build | `activity_integration_test.go` | 5 |
