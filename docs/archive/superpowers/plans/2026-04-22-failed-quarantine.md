# Failed Book Quarantine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move permanently-unprocessable audiobook files to a `.failed/{author}/{book}/` quarantine folder, exclude them from all write-back/scan/iTunes operations, surface them in the UI behind a "Show Failed" toggle, publish a `book.quarantined` EventBus event, and instrument `RecordPathChange` across every path-mutation site so the full file provenance is preserved from first import.

**Architecture:** Path-as-state — the presence of `/.failed/` in a file's path is the canonical quarantine indicator, automatically blocking all existing `isProtectedPath` guards. A new `quarantine_reason` / `quarantined_at` column pair on `books` stores the human-readable reason and timestamp for display. Full path history is captured by calling `RecordPathChange` at `CreateBook` and at every other site where a book's path changes.

**Tech Stack:** Go 1.26, PebbleDB, Gin HTTP router, React/TypeScript frontend, `go.senan.xyz/taglib` (WASM), `internal/plugin` EventBus.

**Spec:** `docs/superpowers/specs/2026-04-22-failed-quarantine-design.md`

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `internal/plugin/events.go` | Modify | Add `EventBookQuarantined` constant |
| `internal/database/store.go` | Modify | Add `QuarantineReason`, `QuarantinedAt` to `Book`; document `"purge_pending"` iTunes status |
| `internal/database/migrations.go` | Modify | Migration 051: add columns to books table |
| `internal/database/pebble_store.go` | Modify | `RecordPathChange` in `CreateBook`; add `QuarantineBook`, `UnquarantineBook`, `GetQuarantinedBooks`, `GetScanFailCount`, `IncrScanFailCount`, `ResetScanFailCount` |
| `internal/database/iface_book.go` | Modify | Add quarantine + scan-fail methods to `BookWriter`/`BookStore` |
| `internal/server/server.go` | Modify | Extend `isProtectedPath`; wire startup migration; register routes |
| `internal/metafetch/helpers.go` | Modify | Extend `isProtectedPath` (duplicate) |
| `internal/scanner/scanner.go` | Modify | Skip `.failed/` directory; call `RecordPathChange` on external_move; increment scan-fail counter; auto-quarantine after 3 failures |
| `internal/server/quarantine_service.go` | **Create** | `QuarantineBook`, `UnquarantineBook` server methods |
| `internal/server/quarantine_handlers.go` | **Create** | HTTP handlers for POST/DELETE quarantine |
| `internal/server/quarantine_known_bad.go` | **Create** | One-time startup migration for 29 known-bad files |
| `internal/metafetch/service.go` | Modify | `RecordPathChange` at library_copy creation (~line 1682) |
| `internal/versions/swap.go` | Modify | `RecordPathChange` on version_swap (~line 150) |
| `internal/itunes/service/path_reconcile.go` | Modify | `RecordPathChange` on itunes_reconcile (~line 153) |
| `web/src/components/Library/BookList.tsx` (or equivalent) | Modify | "Show Failed" filter toggle |
| `web/src/components/BookDetail/` (or equivalent) | Modify | Failed badge; Quarantine/Un-quarantine button |

---

## Task 1: Add `EventBookQuarantined` to events.go

**Files:**
- Modify: `internal/plugin/events.go`

- [ ] **Step 1: Add the event type constant**

In `internal/plugin/events.go`, add after `EventScanCompleted`:

```go
	EventScanCompleted     EventType = "scan.completed"
	EventBookQuarantined   EventType = "book.quarantined"
	EventBookUnquarantined EventType = "book.unquarantined"
```

- [ ] **Step 2: Bump the file version header**

```
// version: 1.1.0
```

- [ ] **Step 3: Verify it compiles**

```bash
go build ./internal/plugin/...
```
Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
git add internal/plugin/events.go
git commit -m "feat(events): add book.quarantined + book.unquarantined event types"
```

---

## Task 2: DB migration 051 + Book struct fields

**Files:**
- Modify: `internal/database/store.go`
- Modify: `internal/database/migrations.go`

- [ ] **Step 1: Add fields to Book struct in `internal/database/store.go`**

After `MarkedForDeletionAt *time.Time` (line 172), add:

```go
	// QuarantineReason is the human-readable reason this book was quarantined.
	// Non-nil means the book is quarantined; nil means it is active.
	QuarantineReason *string    `json:"quarantine_reason,omitempty"`
	QuarantinedAt    *time.Time `json:"quarantined_at,omitempty"`
```

Also update the `ITunesSyncStatus` comment (line 187-189) to document `"purge_pending"`:

```go
	// ITunesSyncStatus tracks whether this book's metadata is in sync with the iTunes library.
	// Values: "synced" (up-to-date in ITL), "dirty" (changed since last write-back),
	// "unlinked" (no iTunes presence), "pending" (new, needs adding to iTunes),
	// "purge_pending" (quarantined, delete from iTunes on next sync).
	ITunesSyncStatus *string `json:"itunes_sync_status,omitempty"`
```

Bump store.go version to `1.X+1` (whatever the current version is + 1).

- [ ] **Step 2: Write failing test**

Create `internal/database/quarantine_test.go`:

```go
package database_test

import (
	"testing"
	"github.com/jdfalk/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

func TestBookQuarantineFields(t *testing.T) {
	reason := "taglib cannot parse file"
	b := database.Book{
		ID:               "test-id",
		Title:            "Test Book",
		FilePath:         "/library/.failed/Author/Book/book.m4b",
		QuarantineReason: &reason,
	}
	require.NotNil(t, b.QuarantineReason)
	require.Equal(t, "taglib cannot parse file", *b.QuarantineReason)
	require.Nil(t, b.QuarantinedAt)
}
```

- [ ] **Step 3: Run test to confirm it compiles and passes**

```bash
go test ./internal/database/... -run TestBookQuarantineFields -v
```
Expected: PASS (struct field test, no DB needed).

- [ ] **Step 4: Add migration 051**

At the bottom of `internal/database/migrations.go`, add:

```go
// migration051Up adds quarantine_reason and quarantined_at columns to books.
// These support the .failed/ quarantine folder feature.
func migration051Up(store Store) error {
	sqliteStore, ok := store.(*SQLiteStore)
	if !ok {
		return nil // PebbleDB: schema-free, fields live on the struct
	}
	stmts := []string{
		`ALTER TABLE books ADD COLUMN quarantine_reason TEXT`,
		`ALTER TABLE books ADD COLUMN quarantined_at TIMESTAMP`,
	}
	for _, stmt := range stmts {
		if _, err := sqliteStore.db.Exec(stmt); err != nil {
			log.Printf("  - [WARN] migration 051: %v (continuing)", err)
		}
	}
	log.Println("  - Added quarantine_reason, quarantined_at to books")
	return nil
}
```

Register it in the migrations slice (find the `migrations = []Migration{...}` list):

```go
{Number: 51, Description: "Add quarantine columns to books", Up: migration051Up},
```

- [ ] **Step 5: Verify build**

```bash
go build ./internal/database/...
```
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add internal/database/store.go internal/database/migrations.go internal/database/quarantine_test.go
git commit -m "feat(db): migration 051 — quarantine_reason + quarantined_at on books"
```

---

## Task 3: Full path history — RecordPathChange at CreateBook

**Files:**
- Modify: `internal/database/pebble_store.go`

- [ ] **Step 1: Write failing test**

Add to `internal/database/quarantine_test.go`:

```go
func TestCreateBook_RecordsImportPathHistory(t *testing.T) {
	store := database.NewTestPebbleStore(t) // uses testutil helper
	book, err := store.CreateBook(&database.Book{
		Title:    "Dune",
		FilePath: "/imports/audible/Dune.m4b",
		Format:   "m4b",
	})
	require.NoError(t, err)

	history, err := store.GetBookPathHistory(book.ID)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "import", history[0].ChangeType)
	require.Equal(t, "", history[0].OldPath)
	require.Equal(t, "/imports/audible/Dune.m4b", history[0].NewPath)
}
```

- [ ] **Step 2: Run to confirm failure**

```bash
go test ./internal/database/... -run TestCreateBook_RecordsImportPathHistory -v
```
Expected: FAIL — history is empty.

- [ ] **Step 3: Add RecordPathChange call in CreateBook**

In `internal/database/pebble_store.go`, after `batch.Commit(pebble.Sync)` succeeds at line 1535 (after `return book, nil` is NOT yet reached), add:

```go
	if err := batch.Commit(pebble.Sync); err != nil {
		return nil, err
	}

	// Record the original import path so provenance is preserved forever.
	_ = p.RecordPathChange(&BookPathChange{
		BookID:     book.ID,
		OldPath:    "",
		NewPath:    book.FilePath,
		ChangeType: "import",
	})

	return book, nil
```

- [ ] **Step 4: Run test to confirm pass**

```bash
go test ./internal/database/... -run TestCreateBook_RecordsImportPathHistory -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/database/pebble_store.go internal/database/quarantine_test.go
git commit -m "feat(db): record import path history on CreateBook"
```

---

## Task 4: Full path history — remaining call sites

**Files:**
- Modify: `internal/metafetch/service.go`
- Modify: `internal/versions/swap.go`
- Modify: `internal/itunes/service/path_reconcile.go`
- Modify: `internal/scanner/scanner.go` (external_move — partial; rest in Task 8)

- [ ] **Step 1: metafetch library_copy — add RecordPathChange after CreateBook at ~line 1682**

In `internal/metafetch/service.go`, find the `ensureLibraryCopy` function where `CreateBook(&newBook)` is called. Immediately after the successful `CreateBook` call, add:

```go
	createdCopy, err := mfs.db.CreateBook(&newBook)
	if err != nil {
		return nil
	}
	// Record the library-copy path so history shows the copy event.
	_ = mfs.db.RecordPathChange(&database.BookPathChange{
		BookID:     createdCopy.ID,
		OldPath:    book.FilePath,
		NewPath:    createdCopy.FilePath,
		ChangeType: "library_copy",
	})
```

(If the existing code uses a different variable name for the created copy, adapt accordingly — the key is BookID = new copy's ID, OldPath = original book's FilePath, NewPath = copy's FilePath.)

- [ ] **Step 2: versions/swap.go — add RecordPathChange after UpdateBook at ~line 150**

In `internal/versions/swap.go`, find where `UpdateBook(book.ID, book)` is called after changing `book.FilePath`. Add immediately after the successful update:

```go
	if _, err := s.store.UpdateBook(book.ID, book); err != nil {
		return err
	}
	_ = s.store.RecordPathChange(&database.BookPathChange{
		BookID:     book.ID,
		OldPath:    oldPath, // capture book.FilePath before reassigning
		NewPath:    book.FilePath,
		ChangeType: "version_swap",
	})
```

(Capture `oldPath := book.FilePath` before the swap assignment so you have the before value.)

- [ ] **Step 3: itunes/service/path_reconcile.go — add RecordPathChange after UpdateBook at ~line 153**

In `internal/itunes/service/path_reconcile.go`, after `UpdateBook(b.ID, b)` where the iTunes path reconcile updates a book's path:

```go
	if _, err := s.store.UpdateBook(b.ID, b); err != nil {
		log.Printf("[WARN] path reconcile: UpdateBook %s: %v", b.ID, err)
		continue
	}
	_ = s.store.RecordPathChange(&database.BookPathChange{
		BookID:     b.ID,
		OldPath:    oldPath, // captured before b.FilePath was reassigned
		NewPath:    b.FilePath,
		ChangeType: "itunes_reconcile",
	})
```

- [ ] **Step 4: Verify build**

```bash
go build ./internal/metafetch/... ./internal/versions/... ./internal/itunes/...
```
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add internal/metafetch/service.go internal/versions/swap.go internal/itunes/service/path_reconcile.go
git commit -m "feat(db): record path history at library_copy, version_swap, itunes_reconcile"
```

---

## Task 5: isProtectedPath + scanner .failed/ skip

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/metafetch/helpers.go`
- Modify: `internal/scanner/scanner.go`

- [ ] **Step 1: Write failing test for isProtectedPath**

In `internal/server/server_test.go` (or create `internal/server/protected_path_test.go`):

```go
package server

import (
	"testing"
	"github.com/stretchr/testify/assert"
)

func TestIsProtectedPath_FailedDir(t *testing.T) {
	cases := []struct {
		path     string
		expected bool
	}{
		{"/library/.failed/Author/Book/book.m4b", true},
		{"/library/.failed/book.m4b", true},
		{"/library/Author/Book/book.m4b", false},
		{"/library/.failedish/book.m4b", false}, // "failedish" must not match
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, isProtectedPath(tc.path), "path: %s", tc.path)
	}
}
```

- [ ] **Step 2: Run to confirm current behaviour (some cases fail)**

```bash
go test ./internal/server/... -run TestIsProtectedPath_FailedDir -v
```
Expected: FAIL — `.failed/` paths return false currently.

- [ ] **Step 3: Extend isProtectedPath in server.go**

In `internal/server/server.go`, inside `isProtectedPath`, before `return false`:

```go
	// Hard-block .failed/ quarantine folder — never write to or move quarantined files.
	if strings.Contains(filepath.ToSlash(absPath), "/.failed/") {
		return true
	}

	return false
```

- [ ] **Step 4: Extend isProtectedPath in helpers.go**

Same change in `internal/metafetch/helpers.go` `isProtectedPath`, before `return false`:

```go
	// Hard-block .failed/ quarantine folder.
	if strings.Contains(filepath.ToSlash(absPath), "/.failed/") {
		return true
	}

	return false
```

- [ ] **Step 5: Run test to confirm pass**

```bash
go test ./internal/server/... -run TestIsProtectedPath_FailedDir -v
```
Expected: PASS.

- [ ] **Step 6: Add .failed/ directory skip to scanner**

In `internal/scanner/scanner.go`, in the `filepath.Walk` callback at line 192, add a `.failed` name check so the entire subtree is skipped:

```go
		if info.IsDir() {
			if info.Name() == ".failed" {
				return filepath.SkipDir
			}
			if !registerDirectory(path, info) {
				return filepath.SkipDir
			}
		}
```

- [ ] **Step 7: Build and run scanner tests**

```bash
go build ./internal/scanner/...
go test ./internal/scanner/... -v
```
Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add internal/server/server.go internal/metafetch/helpers.go internal/scanner/scanner.go
git commit -m "feat(quarantine): extend isProtectedPath + scanner skip for .failed/ dir"
```

---

## Task 6: QuarantineBook and UnquarantineBook service methods

**Files:**
- Modify: `internal/database/iface_book.go`
- Modify: `internal/database/pebble_store.go`
- Create: `internal/server/quarantine_service.go`

- [ ] **Step 1: Add quarantine methods to the store interface**

In `internal/database/iface_book.go`, add to `BookWriter`:

```go
// BookWriter is the write-only slice of Store for callers that only mutate books.
type BookWriter interface {
	CreateBook(book *Book) (*Book, error)
	UpdateBook(id string, book *Book) (*Book, error)
	DeleteBook(id string) error
	SetLastWrittenAt(id string, t time.Time) error
	MarkITunesSynced(bookIDs []string) (int64, error)
	RevertBookToVersion(id string, ts time.Time) (*Book, error)
	PruneBookSnapshots(id string, keepCount int) (int, error)
	CreateBookTombstone(book *Book) error
	DeleteBookTombstone(id string) error
	// Scan-fail counter for auto-quarantine.
	GetScanFailCount(pathHash string) (int, error)
	IncrScanFailCount(pathHash string) (int, error)
	ResetScanFailCount(pathHash string) error
}
```

Also add to `BookReader`:

```go
	GetQuarantinedBooks(limit, offset int) ([]Book, error)
```

- [ ] **Step 2: Implement GetScanFailCount / IncrScanFailCount / ResetScanFailCount in pebble_store.go**

Add at the bottom of `internal/database/pebble_store.go`:

```go
// scan-fail counter keys: "scan_fail:<hash8>"

func (p *PebbleStore) GetScanFailCount(pathHash string) (int, error) {
	key := []byte("scan_fail:" + pathHash)
	val, closer, err := p.db.Get(key)
	if err != nil {
		return 0, nil // key not found = 0
	}
	defer closer.Close()
	n := 0
	_, _ = fmt.Sscanf(string(val), "%d", &n)
	return n, nil
}

func (p *PebbleStore) IncrScanFailCount(pathHash string) (int, error) {
	n, _ := p.GetScanFailCount(pathHash)
	n++
	key := []byte("scan_fail:" + pathHash)
	return n, p.db.Set(key, []byte(fmt.Sprintf("%d", n)), pebble.Sync)
}

func (p *PebbleStore) ResetScanFailCount(pathHash string) error {
	key := []byte("scan_fail:" + pathHash)
	return p.db.Delete(key, pebble.Sync)
}
```

- [ ] **Step 3: Implement GetQuarantinedBooks in pebble_store.go**

```go
// GetQuarantinedBooks returns books with a non-nil QuarantinedAt, newest first.
func (p *PebbleStore) GetQuarantinedBooks(limit, offset int) ([]Book, error) {
	all, err := p.getAllBooksRaw()
	if err != nil {
		return nil, err
	}
	var result []Book
	for _, b := range all {
		if b.QuarantinedAt != nil {
			result = append(result, b)
		}
	}
	// Sort newest quarantine first
	sort.Slice(result, func(i, j int) bool {
		return result[i].QuarantinedAt.After(*result[j].QuarantinedAt)
	})
	if offset >= len(result) {
		return nil, nil
	}
	result = result[offset:]
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
```

(Use the same `getAllBooksRaw` helper used by `GetAllBooks` — find it in pebble_store.go and reuse it, or inline a similar prefix scan over `"book:"` keys.)

- [ ] **Step 4: Write failing tests for QuarantineBook**

Create `internal/server/quarantine_service_test.go`:

```go
package server

import (
	"os"
	"path/filepath"
	"testing"
	"github.com/jdfalk/audiobook-organizer/internal/database"
	"github.com/jdfalk/audiobook-organizer/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestQuarantineBook_MovesFileAndUpdatesDB(t *testing.T) {
	env := testutil.NewTestEnv(t)
	defer env.Cleanup()

	// Create a real file in a temp library dir
	src := filepath.Join(env.LibraryDir, "Author", "Book", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(src), 0755))
	require.NoError(t, os.WriteFile(src, []byte("fake audio"), 0644))

	book, err := env.Store.CreateBook(&database.Book{
		Title: "Book", FilePath: src, Format: "m4b",
	})
	require.NoError(t, err)

	srv := newTestServer(t, env)
	require.NoError(t, srv.QuarantineBook(book.ID, "taglib failed"))

	// File should be gone from original location
	_, err = os.Stat(src)
	require.True(t, os.IsNotExist(err), "original file should be removed")

	// File should be in .failed/
	expected := filepath.Join(env.LibraryDir, ".failed", "Author", "Book", "book.m4b")
	_, err = os.Stat(expected)
	require.NoError(t, err, "file should exist in .failed/")

	// DB should be updated
	updated, err := env.Store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, expected, updated.FilePath)
	require.NotNil(t, updated.QuarantineReason)
	require.Equal(t, "taglib failed", *updated.QuarantineReason)
	require.NotNil(t, updated.QuarantinedAt)

	// Path history should have quarantine entry
	history, err := env.Store.GetBookPathHistory(book.ID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(history), 1)
	var found bool
	for _, h := range history {
		if h.ChangeType == "quarantine" {
			found = true
			require.Equal(t, src, h.OldPath)
			require.Equal(t, expected, h.NewPath)
		}
	}
	require.True(t, found, "quarantine path history entry not found")
}

func TestUnquarantineBook_MovesFileBack(t *testing.T) {
	env := testutil.NewTestEnv(t)
	defer env.Cleanup()

	// Set up a file already in .failed/
	quarPath := filepath.Join(env.LibraryDir, ".failed", "Author", "Book", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(quarPath), 0755))
	require.NoError(t, os.WriteFile(quarPath, []byte("fake audio"), 0644))

	reason := "taglib failed"
	book, err := env.Store.CreateBook(&database.Book{
		Title: "Book", FilePath: quarPath, Format: "m4b",
		QuarantineReason: &reason,
	})
	require.NoError(t, err)

	// Seed path history with original path
	origPath := filepath.Join(env.LibraryDir, "Author", "Book", "book.m4b")
	_ = env.Store.RecordPathChange(&database.BookPathChange{
		BookID: book.ID, OldPath: origPath, NewPath: quarPath, ChangeType: "quarantine",
	})

	srv := newTestServer(t, env)
	require.NoError(t, srv.UnquarantineBook(book.ID))

	// File should be back at original path
	_, err = os.Stat(origPath)
	require.NoError(t, err, "file should be restored to original path")

	updated, err := env.Store.GetBookByID(book.ID)
	require.NoError(t, err)
	require.Equal(t, origPath, updated.FilePath)
	require.Nil(t, updated.QuarantineReason)
	require.Nil(t, updated.QuarantinedAt)
}
```

- [ ] **Step 5: Run tests to confirm failure**

```bash
go test ./internal/server/... -run "TestQuarantineBook|TestUnquarantineBook" -v
```
Expected: FAIL — methods not yet implemented.

- [ ] **Step 6: Create `internal/server/quarantine_service.go`**

```go
// file: internal/server/quarantine_service.go
// version: 1.0.0
// guid: b2c3d4e5-f6a7-8b9c-0d1e-2f3a4b5c6d7e

package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jdfalk/audiobook-organizer/internal/config"
	"github.com/jdfalk/audiobook-organizer/internal/database"
	"github.com/jdfalk/audiobook-organizer/internal/plugin"
)

// QuarantineBook moves a book's file to .failed/{author}/{book}/{filename},
// updates the DB, records path history, sets iTunes purge_pending, and
// publishes a book.quarantined event.
func (s *Server) QuarantineBook(bookID, reason string) error {
	store := s.Store()
	if store == nil {
		return fmt.Errorf("store not initialized")
	}

	book, err := store.GetBookByID(bookID)
	if err != nil || book == nil {
		return fmt.Errorf("book not found: %s", bookID)
	}
	if book.QuarantinedAt != nil {
		return nil // already quarantined
	}

	root := config.AppConfig.RootDir
	if root == "" {
		return fmt.Errorf("RootDir not configured")
	}

	// Compute destination: {root}/.failed/{author}/{book-title}/{filename}
	author := "Unknown Author"
	if book.Author != nil && book.Author.Name != "" {
		author = sanitizeDirName(book.Author.Name)
	}
	title := sanitizeDirName(book.Title)
	if title == "" {
		title = "Unknown"
	}
	filename := filepath.Base(book.FilePath)
	dest := filepath.Join(root, ".failed", author, title, filename)

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return fmt.Errorf("mkdir .failed: %w", err)
	}
	if err := os.Rename(book.FilePath, dest); err != nil {
		return fmt.Errorf("move to .failed: %w", err)
	}

	oldPath := book.FilePath
	now := time.Now()
	book.FilePath = dest
	book.QuarantineReason = &reason
	book.QuarantinedAt = &now

	// Set iTunes purge if linked
	if book.ITunesPersistentID != nil {
		purge := "purge_pending"
		book.ITunesSyncStatus = &purge
	}

	if _, err := store.UpdateBook(bookID, book); err != nil {
		return fmt.Errorf("update book: %w", err)
	}

	_ = store.RecordPathChange(&database.BookPathChange{
		BookID:     bookID,
		OldPath:    oldPath,
		NewPath:    dest,
		ChangeType: "quarantine",
	})

	log.Printf("[INFO] QuarantineBook: %s → %s (%s)", oldPath, dest, reason)

	s.publishEvent(context.Background(), plugin.NewEvent(plugin.EventBookQuarantined, bookID, map[string]any{
		"title":         book.Title,
		"author":        author,
		"file_path":     dest,
		"original_path": oldPath,
		"reason":        reason,
		"quarantined_at": now.Format(time.RFC3339),
	}))

	return nil
}

// UnquarantineBook moves a quarantined book back to its original path
// (retrieved from path history) and clears the quarantine fields.
func (s *Server) UnquarantineBook(bookID string) error {
	store := s.Store()
	if store == nil {
		return fmt.Errorf("store not initialized")
	}

	book, err := store.GetBookByID(bookID)
	if err != nil || book == nil {
		return fmt.Errorf("book not found: %s", bookID)
	}
	if book.QuarantinedAt == nil {
		return nil // not quarantined
	}

	// Find original path from path history (most recent "quarantine" entry's OldPath)
	history, err := store.GetBookPathHistory(bookID)
	if err != nil {
		return fmt.Errorf("get path history: %w", err)
	}
	var origPath string
	for _, h := range history {
		if h.ChangeType == "quarantine" {
			origPath = h.OldPath
			break
		}
	}
	if origPath == "" {
		return fmt.Errorf("no quarantine history entry found for book %s", bookID)
	}

	if err := os.MkdirAll(filepath.Dir(origPath), 0755); err != nil {
		return fmt.Errorf("mkdir original path: %w", err)
	}
	if err := os.Rename(book.FilePath, origPath); err != nil {
		return fmt.Errorf("restore from .failed: %w", err)
	}

	quarPath := book.FilePath
	book.FilePath = origPath
	book.QuarantineReason = nil
	book.QuarantinedAt = nil
	// Clear purge_pending if it was set by quarantine
	if book.ITunesSyncStatus != nil && *book.ITunesSyncStatus == "purge_pending" {
		dirty := "dirty"
		book.ITunesSyncStatus = &dirty
	}

	if _, err := store.UpdateBook(bookID, book); err != nil {
		return fmt.Errorf("update book: %w", err)
	}

	_ = store.RecordPathChange(&database.BookPathChange{
		BookID:     bookID,
		OldPath:    quarPath,
		NewPath:    origPath,
		ChangeType: "unquarantine",
	})

	log.Printf("[INFO] UnquarantineBook: %s → %s", quarPath, origPath)

	s.publishEvent(context.Background(), plugin.NewEvent(plugin.EventBookUnquarantined, bookID, map[string]any{
		"file_path":    origPath,
		"quarantine_path": quarPath,
	}))

	return nil
}

// sanitizeDirName strips characters unsafe for directory names.
func sanitizeDirName(name string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-",
		"?", "-", "\"", "-", "<", "-", ">", "-", "|", "-")
	return strings.TrimSpace(replacer.Replace(name))
}
```

- [ ] **Step 7: Run tests to confirm pass**

```bash
go test ./internal/server/... -run "TestQuarantineBook|TestUnquarantineBook" -v
```
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/database/iface_book.go internal/database/pebble_store.go \
        internal/server/quarantine_service.go internal/server/quarantine_service_test.go
git commit -m "feat(quarantine): QuarantineBook + UnquarantineBook service methods"
```

---

## Task 7: HTTP handlers and routes

**Files:**
- Create: `internal/server/quarantine_handlers.go`
- Modify: `internal/server/server.go` (routes)

- [ ] **Step 1: Create `internal/server/quarantine_handlers.go`**

```go
// file: internal/server/quarantine_handlers.go
// version: 1.0.0
// guid: c3d4e5f6-a7b8-9c0d-1e2f-3a4b5c6d7e8f

package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// quarantineBook handles POST /api/v1/audiobooks/:id/quarantine
func (s *Server) quarantineBook(c *gin.Context) {
	id := c.Param("id")
	if s.Store() == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database not initialized"})
		return
	}

	var req struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Reason == "" {
		req.Reason = "manually quarantined"
	}

	if err := s.QuarantineBook(id, req.Reason); err != nil {
		internalError(c, "quarantine failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "quarantined", "book_id": id})
}

// unquarantineBook handles DELETE /api/v1/audiobooks/:id/quarantine
func (s *Server) unquarantineBook(c *gin.Context) {
	id := c.Param("id")
	if s.Store() == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database not initialized"})
		return
	}

	if err := s.UnquarantineBook(id); err != nil {
		internalError(c, "unquarantine failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "unquarantined", "book_id": id})
}

// listQuarantinedBooks handles GET /api/v1/audiobooks/quarantined
func (s *Server) listQuarantinedBooks(c *gin.Context) {
	if s.Store() == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database not initialized"})
		return
	}
	params := ParsePaginationParams(c)
	books, err := s.Store().GetQuarantinedBooks(params.Limit, params.Offset)
	if err != nil {
		internalError(c, "list quarantined books failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"books": books, "total": len(books)})
}
```

- [ ] **Step 2: Register routes in server.go**

In `internal/server/server.go`, in the audiobooks route group (find `protected.GET("/audiobooks"` and related), add:

```go
			protected.GET("/audiobooks/quarantined", s.perm(auth.PermLibraryView), s.listQuarantinedBooks)
			protected.POST("/audiobooks/:id/quarantine", s.perm(auth.PermSettingsManage), s.quarantineBook)
			protected.DELETE("/audiobooks/:id/quarantine", s.perm(auth.PermSettingsManage), s.unquarantineBook)
```

- [ ] **Step 3: Write handler test**

Add to `internal/server/quarantine_service_test.go`:

```go
func TestQuarantineHandler_Returns200(t *testing.T) {
	env := testutil.NewTestEnv(t)
	defer env.Cleanup()

	src := filepath.Join(env.LibraryDir, "Author", "Book", "book.m4b")
	require.NoError(t, os.MkdirAll(filepath.Dir(src), 0755))
	require.NoError(t, os.WriteFile(src, []byte("audio"), 0644))
	book, err := env.Store.CreateBook(&database.Book{Title: "Book", FilePath: src, Format: "m4b"})
	require.NoError(t, err)

	srv := newTestServer(t, env)
	w := httptest.NewRecorder()
	body := strings.NewReader(`{"reason":"test quarantine"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/audiobooks/"+book.ID+"/quarantine", body)
	req.Header.Set("Content-Type", "application/json")
	srv.router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}
```

- [ ] **Step 4: Run handler test**

```bash
go test ./internal/server/... -run TestQuarantineHandler_Returns200 -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/server/quarantine_handlers.go internal/server/server.go \
        internal/server/quarantine_service_test.go
git commit -m "feat(quarantine): HTTP handlers POST/DELETE /audiobooks/:id/quarantine"
```

---

## Task 8: Scanner scan-fail counter + auto-quarantine

**Files:**
- Modify: `internal/scanner/scanner.go`

The scanner discovers files in `groupFilesIntoBooks`, then each `Book` struct is processed in the parallel per-directory goroutine. The actual taglib read happens either in `groupFilesIntoBooks` or downstream. Find where `taglib.ReadTags` (or equivalent) is called during a scan, or where a book file is found to be unreadable.

- [ ] **Step 1: Find where scan failures surface in scanner**

```bash
grep -n "ReadTags\|taglib\|invalid\|unreadable" internal/scanner/scanner.go | head -20
```

If taglib is not called directly in the scanner (it may happen during metadata extraction after import), the auto-quarantine hook goes in the book processing loop where the book is added to the DB and a read failure would be detected. Locate the exact site and adapt steps 2-5 accordingly.

- [ ] **Step 2: Add scan-fail counter helper**

In `internal/scanner/scanner.go`, add a package-level helper at the top of the file (after imports):

```go
import "crypto/sha256"

// scanFailKey returns the PebbleDB key suffix for a file's scan-fail counter.
func scanFailKey(path string) string {
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%x", h[:8])
}
```

- [ ] **Step 3: Instrument the failure site**

At the site where a file is determined to be unreadable during a scan (after taglib failure), add:

```go
			store := database.GetGlobalStore()
			if store != nil {
				key := scanFailKey(path)
				n, _ := store.IncrScanFailCount(key)
				if n >= 3 {
					// Find the book in DB and quarantine it
					if b, err := store.GetBookByFilePath(path); err == nil && b != nil {
						// QuarantineBook requires a *Server — use a package-level accessor
						// or pass server reference through the scanner config.
						// For now: log and let the startup migration handle known-bad files.
						// The server's QuarantineBook is called post-scan via a hook.
						log.Printf("[WARN] scanner: %s has failed %d times — marking for quarantine", path, n)
					}
				}
			}
```

**Note:** Because the scanner doesn't have a direct reference to `*Server`, the auto-quarantine after 3 failures is best implemented as a post-scan hook: after each scan completes, the server checks for books with `IncrScanFailCount >= 3` and calls `QuarantineBook`. Add this check in `server.go` as part of the scan-completed callback, similar to how `EventScanCompleted` is published.

- [ ] **Step 4: Add post-scan auto-quarantine in server**

In `internal/server/server.go` or wherever the scan-complete event is published, add:

```go
// After scan completes, quarantine any books that have hit the fail threshold.
go func() {
	s.autoQuarantineFailedScans()
}()
```

Create `internal/server/quarantine_service.go` addition (append to existing file):

```go
const scanFailThreshold = 3

// autoQuarantineFailedScans checks for books whose scan-fail counter has
// reached the threshold and quarantines them.
func (s *Server) autoQuarantineFailedScans() {
	store := s.Store()
	if store == nil {
		return
	}
	// Iterate books; for each, check counter keyed on its FilePath.
	books, err := store.GetAllBooks(10000, 0)
	if err != nil {
		return
	}
	for _, b := range books {
		if b.QuarantinedAt != nil {
			continue
		}
		n, _ := store.GetScanFailCount(scanFailKey(b.FilePath))
		if n >= scanFailThreshold {
			log.Printf("[INFO] auto-quarantine: %s (fail count %d)", b.FilePath, n)
			_ = s.QuarantineBook(b.ID, fmt.Sprintf("taglib failed to read file after %d consecutive scan attempts", n))
		}
	}
}
```

- [ ] **Step 5: Build**

```bash
go build ./internal/scanner/... ./internal/server/...
```
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add internal/scanner/scanner.go internal/server/quarantine_service.go
git commit -m "feat(quarantine): scanner scan-fail counter + auto-quarantine after 3 failures"
```

---

## Task 9: Startup migration for 29 known-bad files

**Files:**
- Create: `internal/server/quarantine_known_bad.go`
- Modify: `internal/server/server.go`

- [ ] **Step 1: Create `internal/server/quarantine_known_bad.go`**

```go
// file: internal/server/quarantine_known_bad.go
// version: 1.0.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a

package server

import (
	"fmt"
	"log"
	"strings"

	"github.com/jdfalk/audiobook-organizer/internal/config"
)

const quarantineKnownBadKey = "quarantine_known_bad_v1_done"

// quarantineKnownBadFiles is a one-time startup migration that quarantines
// any file whose transcode_skip_* flag is set in PebbleDB and that is not
// already quarantined. Covers the ~29 permanently-unreadable M4B files.
func (s *Server) quarantineKnownBadFiles() {
	store := s.Store()
	if store == nil {
		return
	}
	if setting, err := store.GetSetting(quarantineKnownBadKey); err == nil && setting != nil && setting.Value == "true" {
		return
	}

	root := config.AppConfig.RootDir
	if root == "" {
		log.Printf("[WARN] quarantineKnownBadFiles: RootDir not configured, skipping")
		return
	}

	log.Printf("[INFO] quarantineKnownBadFiles: scanning for transcode_skip_* entries …")
	quarantined := 0

	// Walk PebbleDB settings for transcode_skip_* keys.
	// Each key encodes a sha256[:8] of the file path — we must reverse-lookup
	// by scanning all books and checking their path hash.
	books, err := store.GetAllBooks(100000, 0)
	if err != nil {
		log.Printf("[WARN] quarantineKnownBadFiles: GetAllBooks: %v", err)
		return
	}

	for _, b := range books {
		if b.QuarantinedAt != nil {
			continue
		}
		key := transcodeSkipKey(b.FilePath)
		if skip, err := store.GetSetting(key); err == nil && skip != nil && skip.Value == "true" {
			if err := s.QuarantineBook(b.ID, "taglib cannot parse file after full AAC transcode"); err != nil {
				log.Printf("[WARN] quarantineKnownBadFiles: QuarantineBook %s: %v", b.FilePath, err)
			} else {
				log.Printf("[INFO] quarantineKnownBadFiles: quarantined %s", b.FilePath)
				quarantined++
			}
		}
	}

	log.Printf("[INFO] quarantineKnownBadFiles: quarantined %d known-bad files", quarantined)
	_ = store.SetSetting(quarantineKnownBadKey, "true", "bool", false)
}

// transcodeSkipKey is redeclared here to avoid import cycle with
// malformed_m4b_transcode.go — both files are in package server.
// (Remove this if transcodeSkipKey is already visible in this package.)
```

**Note:** `transcodeSkipKey` is already defined in `malformed_m4b_transcode.go` in the same `server` package, so do NOT redeclare it — just remove that comment and the redeclaration.

- [ ] **Step 2: Wire into server.go startup**

In `internal/server/server.go`, after the existing `s.remuxMalformedM4BFiles()` bgWG block (around line 1644), add:

```go
	// One-time: quarantine files with transcode_skip_* flags (29 known-bad M4Bs).
	s.bgWG.Add(1)
	go func() {
		defer s.bgWG.Done()
		s.quarantineKnownBadFiles()
	}()
```

- [ ] **Step 3: Build**

```bash
go build ./internal/server/...
```
Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add internal/server/quarantine_known_bad.go internal/server/server.go
git commit -m "feat(quarantine): startup migration for 29 known-bad transcode_skip files"
```

---

## Task 10: iTunes purge_pending handling

**Files:**
- Modify: `internal/itunes/service/` (whichever file drives the write-back sync)

- [ ] **Step 1: Find where iTunes sync status drives actions**

```bash
grep -rn "purge_pending\|ITunesSyncStatus\|itunes_sync_status" \
    internal/itunes/ internal/server/ --include="*.go" | grep -v mock | head -20
```

Locate the scheduler/sync loop that reads `ITunesSyncStatus` and acts on `"dirty"` / `"pending"` books.

- [ ] **Step 2: Add purge_pending case**

In the iTunes sync loop, add a case alongside `"dirty"` handling:

```go
case "purge_pending":
    // Delete this book's track from the iTunes library.
    if err := s.deleteFromITunes(book); err != nil {
        log.Printf("[WARN] iTunes purge: %s: %v", book.ID, err)
    } else {
        purged := "unlinked"
        book.ITunesSyncStatus = &purged
        book.ITunesPersistentID = nil
        if _, err := s.store.UpdateBook(book.ID, &book); err != nil {
            log.Printf("[WARN] iTunes purge: UpdateBook %s: %v", book.ID, err)
        }
    }
```

- [ ] **Step 3: Build**

```bash
go build ./internal/itunes/...
```
Expected: no output.

- [ ] **Step 4: Commit**

```bash
git add internal/itunes/service/
git commit -m "feat(quarantine): iTunes purge_pending status — delete track on sync"
```

---

## Task 11: UI — Show Failed toggle, badge, Quarantine button

**Files:**
- Modify: `web/src/` — library book list, book detail page

- [ ] **Step 1: Find the book list component**

```bash
find web/src -name "*.tsx" | xargs grep -l "BookList\|audiobooks\|library" | head -5
```

- [ ] **Step 2: Add "Show Failed" toggle to library filter bar**

In the book list query/filter, add a `show_quarantined` boolean param (default `false`). When false, exclude books where `quarantined_at` is non-null. Add a toggle button:

```tsx
<button
  className={`filter-toggle ${showFailed ? 'active' : ''}`}
  onClick={() => setShowFailed(f => !f)}
>
  {showFailed ? 'Hide Failed' : 'Show Failed'}
</button>
```

Pass `show_quarantined: showFailed` to the API query.

- [ ] **Step 3: Add "Failed" badge to book cards**

In the book card component, conditionally render a badge when `book.quarantined_at` is set:

```tsx
{book.quarantined_at && (
  <span className="badge badge-error" title={book.quarantine_reason ?? ''}>
    Failed
  </span>
)}
```

- [ ] **Step 4: Add Quarantine / Un-quarantine button to book detail**

In the book detail page (admin-only section), add:

```tsx
{isAdmin && !book.quarantined_at && (
  <button
    className="btn btn-error btn-sm"
    onClick={() => handleQuarantine(book.id)}
  >
    Quarantine
  </button>
)}
{isAdmin && book.quarantined_at && (
  <button
    className="btn btn-warning btn-sm"
    onClick={() => handleUnquarantine(book.id)}
  >
    Un-quarantine
  </button>
)}
```

With handlers:

```typescript
const handleQuarantine = async (id: string) => {
  const reason = prompt('Reason for quarantine:') ?? 'manually quarantined'
  await api.post(`/api/v1/audiobooks/${id}/quarantine`, { reason })
  refetch()
}

const handleUnquarantine = async (id: string) => {
  await api.delete(`/api/v1/audiobooks/${id}/quarantine`)
  refetch()
}
```

- [ ] **Step 5: Add show_quarantined param to the backend book listing endpoint**

In `internal/server/audiobooks_handlers.go`, in the `listAudiobooks` handler, read the query param and filter accordingly:

```go
showQuarantined := c.Query("show_quarantined") == "true"
// In the book filtering logic, skip books where QuarantinedAt != nil unless showQuarantined is true.
```

- [ ] **Step 6: Build frontend**

```bash
make build
```
Expected: frontend and backend build successfully.

- [ ] **Step 7: Commit**

```bash
git add web/src/
git commit -m "feat(quarantine): Show Failed toggle, Failed badge, Quarantine/Un-quarantine buttons"
```

---

## Task 12: Integration smoke test + deploy

- [ ] **Step 1: Run full test suite**

```bash
make test
```
Expected: all pass.

- [ ] **Step 2: Cross-compile and deploy**

```bash
GOOS=linux GOARCH=amd64 go build -o /tmp/audiobook-organizer-linux .
scp /tmp/audiobook-organizer-linux <user>@<server>:/home/<user>/audiobook-organizer
ssh <user>@<server> "sudo mv /home/<user>/audiobook-organizer /usr/local/bin/audiobook-organizer && sudo systemctl restart audiobook-organizer.service"
```

- [ ] **Step 3: Verify quarantine startup migration ran**

```bash
ssh <user>@<server> "journalctl -u audiobook-organizer.service --no-pager -n 30 2>/dev/null | grep -i quarantine"
```
Expected: lines showing `quarantineKnownBadFiles: quarantined N known-bad files`.

- [ ] **Step 4: Verify .failed/ directory created**

```bash
ssh <user>@<server> "ls /mnt/bigdata/books/audiobook-organizer/.failed/"
```
Expected: author directories for Argus, Chris Fox, David Petrie, Eric Ugland, etc.

- [ ] **Step 5: Final commit + push**

```bash
git push origin main
```
