# Implementation Guide — Open Issues

> Detailed instructions for every open TODO item. Written so anyone (intern or AI) can implement each fix without prior context.

**Last updated:** 2026-03-19

---

## 1. Clear Conflicting Tags During Write

**Priority:** P0 — blocks daily use
**Difficulty:** Medium
**Files:** `internal/metadata/taglib_support.go`, `internal/metadata/metadata.go`

### Problem
When we write tags to an M4B file (e.g., `ARTIST = "Joshua Dalzelle"`), the file already has a `composer` tag with the narrator's name (`"Paul Heitsch"`). After writing, `ExtractMetadata` re-reads the file and picks up `composer` as a fallback author — showing the wrong value in the UI.

### Root Cause
The tag writer ADDS tags but doesn't REMOVE conflicting legacy tags. The extraction priority is `album_artist > artist > composer`, but the `composer` field persists from the original file.

### Fix
In `internal/metadata/taglib_support.go`, function `writeMetadataWithTaglib`:

1. After building the `tags` map, if `artist` is being written, also **clear** the `composer` tag to prevent it from overriding:
```go
if _, ok := metadata["artist"].(string); ok {
    // Clear composer to prevent it from overriding artist during extraction.
    // In audiobooks, composer typically contains the narrator, not the author.
    tags[taglib.Composer] = []string{""}  // or use taglib's delete mechanism
}
```

2. If the library doesn't support clearing tags, write the artist value TO the composer field as well:
```go
if artist, ok := metadata["artist"].(string); ok && artist != "" {
    tags[taglib.Composer] = []string{artist}  // Overwrite narrator in composer
}
```

3. Similarly, clear the `album` tag if we're writing a different series name, since `album` is sometimes used as series in legacy files.

### Testing
```bash
# 1. Find a book with conflicting composer tag
curl -sk 'https://<your-server-ip>:8484/api/v1/audiobooks?search=counterstrike&limit=1'

# 2. Check current tags
ssh server 'ffprobe -v quiet -print_format json -show_format "/path/to/file.m4b"' | jq '.format.tags'

# 3. Write tags
curl -sk -X POST 'https://<your-server-ip>:8484/api/v1/audiobooks/BOOK_ID/write-back'

# 4. Re-check tags — composer should now match artist
ssh server 'ffprobe -v quiet -print_format json -show_format "/path/to/file.m4b"' | jq '.format.tags'

# 5. Verify extraction reads correct author
curl -sk 'https://<your-server-ip>:8484/api/v1/audiobooks/BOOK_ID/tags' | jq '.tags.author_name'
```

### Acceptance Criteria
- After write-back, `ffprobe` shows `composer` matches `artist` (or is empty)
- `ExtractMetadata` returns the correct author from `album_artist`/`artist`
- No regression for MP3 files (different tag structure)

---

## 2. Smart Author/Narrator Resolution

**Priority:** P0
**Difficulty:** Hard
**Files:** `internal/metadata/metadata.go`, `internal/server/metadata_fetch_service.go`

### Problem
Many Audible M4B files have: `artist = narrator`, `album_artist = author`, `composer = narrator`. Our fix (album_artist > artist > composer) works when `album_artist` is set. But some files only have `artist = narrator` with no `album_artist`.

### Fix
In `ExtractMetadata` (`internal/metadata/metadata.go`), after resolving the artist:

1. Check if our custom `AUDIOBOOK_ORGANIZER_VERSION` tag is present — if so, trust our written tags over legacy ones.
2. If the extracted `artist` matches the extracted `narrator` (from PERFORMER/NARRATOR tag), and the book has a known author in the DB, prefer the DB author.
3. Add a new field `metadata.AuthorSource` that tracks where the author came from, so the UI can show "(from file)" vs "(from DB)".

### Testing
Find a file where `artist = narrator` and verify the extraction picks the correct author from the DB.

---

## 3. ISBN Enrichment Validation

**Priority:** P0
**Difficulty:** Easy
**Files:** `internal/server/isbn_enrichment.go`

### Problem
Previous version of `isStrictTitleMatch` used substring matching — "Shadows of Self" matched any book with "self" in the title. Fixed to prefix match with 60% length ratio. Needs live testing.

### Fix (already applied)
The fix is deployed. Need to verify:

```bash
# Find a book without ISBN
curl -sk 'https://<your-server-ip>:8484/api/v1/audiobooks?limit=10' | \
  python3 -c "import json,sys; [print(b['id'],b['title']) for b in json.load(sys.stdin)['items'] if not b.get('isbn13')]"

# Trigger fetch metadata
curl -sk -X POST "https://<your-server-ip>:8484/api/v1/audiobooks/BOOK_ID/fetch-metadata"

# Wait 10 seconds, then check
curl -sk "https://<your-server-ip>:8484/api/v1/audiobooks/BOOK_ID" | python3 -c "import json,sys; b=json.load(sys.stdin); print(f'ISBN: {b.get(\"isbn13\")}, ASIN: {b.get(\"asin\")}, Title: {b[\"title\"]}')"
```

### Acceptance Criteria
- ISBN matches the correct book (not a random one)
- Title is preserved after fetch
- If no ISBN found, no incorrect ISBN is assigned

---

## 4. M4B Conversion Live Test

**Priority:** P1
**Difficulty:** Easy (just testing)
**Files:** N/A (test only)

### Steps
```bash
# 1. Find a multi-file MP3 book
curl -sk 'https://<your-server-ip>:8484/api/v1/audiobooks?limit=20' | \
  python3 -c "import json,sys; [print(b['id'],b['title'],b['format']) for b in json.load(sys.stdin)['items'] if b.get('format')=='mp3']"

# 2. Trigger conversion
curl -sk -X POST "https://<your-server-ip>:8484/api/v1/operations/transcode" \
  -H 'Content-Type: application/json' \
  -d '{"book_id": "BOOK_ID"}'

# 3. Monitor operation
curl -sk "https://<your-server-ip>:8484/api/v1/operations/OP_ID"

# 4. Verify:
#    - M4B file exists at expected path
#    - Original MP3 is non-primary, M4B is primary
#    - Both are in same version group
#    - M4B has chapters (ffprobe -show_chapters)
#    - If iTunes write-back disabled, deferred_itunes_updates has a row
```

---

## 5. Bulk Save to Files

**Priority:** P1
**Difficulty:** Medium
**Files:** `internal/server/server.go`, `internal/server/metadata_fetch_service.go`

### Problem
"Save to Files" only works per-book. Need a batch endpoint.

### Fix
Add `POST /api/v1/audiobooks/batch-write-back`:
```go
func (s *Server) batchWriteBack(c *gin.Context) {
    var body struct {
        BookIDs []string `json:"book_ids"`
        Rename  bool     `json:"rename"`
    }
    // For each book, call WriteBackMetadataForBook + optional RunApplyPipelineRenameOnly
    // Return summary: {written: N, renamed: N, failed: N, errors: [...]}
}
```

Or use the existing `POST /api/v1/audiobooks/batch-operations` with action="write_back".

### Frontend
Add "Save All to Files" button to the Library page (next to "Organize Selected").

---

## 6. Series Dedup

**Priority:** P1
**Difficulty:** Easy
**Files:** Data cleanup via API

### Problem
8,507 series for 10,891 books. Many 1-book series that should be merged or removed.

### Fix
Run this cleanup script via the batch API:
```python
# 1. Find series with identical names (case-insensitive)
# 2. Merge books into the series with the most books
# 3. Delete empty series
# Already implemented — run the same script from the previous session
```

### Automated version
Add to the `series_prune` maintenance task: detect and merge duplicate series names automatically.

---

## 7. "read by narrator" Books

**Priority:** P1
**Difficulty:** Medium
**Files:** Use Diagnostics page

### Fix
1. Go to Diagnostics page → select "Metadata Quality"
2. Submit to AI → review suggestions
3. Apply approved fixes

Or bulk fix via API:
```bash
# Find books with "read by narrator" as title or author
curl -sk 'https://<your-server-ip>:8484/api/v1/audiobooks?search=read+by+narrator&limit=200'
# For each, fetch metadata to get the real title/author
```

---

## 8. Version vs Snapshot UI Polish

**Priority:** P2
**Difficulty:** Medium
**Files:** `web/src/pages/BookDetail.tsx`, `web/src/components/TagComparison.tsx`, `web/src/components/ChangeLog.tsx`

### Remaining items
- Format tray should show "Set as Primary" and "Unlink" buttons (currently missing from new layout)
- Segment table should be collapsible with "show all N files" toggle
- Multi-file formats should show "Overall Metadata" summary with ≠ DB indicators
- File size and duration should be human-formatted (MB, hours)

---

## 9. Snapshot Comparison

**Priority:** P2
**Difficulty:** Medium
**Files:** `web/src/components/ChangeLog.tsx`, `web/src/components/TagComparison.tsx`, `internal/server/server.go`

### Problem
ChangeLog "Compare →" link communicates timestamp to TagComparison but doesn't load actual snapshot data.

### Fix
1. Add `GET /api/v1/audiobooks/:id/tags?snapshot_ts=RFC3339` backend endpoint
2. When `snapshot_ts` is provided, load the COW version snapshot from that timestamp
3. Return the snapshot's metadata as `comparison_value` in each tag entry
4. Frontend: TagComparison auto-expands and shows "Comparing against snapshot from [date]" banner

---

## 10. Scheduled ISBN Enrichment

**Priority:** P2
**Difficulty:** Easy
**Files:** `internal/server/scheduler.go`, `internal/server/isbn_enrichment.go`

### Fix
Register a new maintenance task `isbn_enrichment` that:
1. Queries books where `isbn10 IS NULL AND isbn13 IS NULL`
2. For each (up to 100 per run), calls `EnrichBookISBN`
3. Runs during maintenance window

---

## 11. Copy-on-Write Verification

**Priority:** P2
**Difficulty:** Easy (testing)
**Files:** N/A

### Steps
```bash
# 1. Write tags to a book
curl -sk -X POST "https://<your-server-ip>:8484/api/v1/audiobooks/BOOK_ID/write-back"

# 2. Check that .bak file was created as hardlink
ssh server 'ls -la /path/to/file.m4b*'
# Should show .bak-YYYYMMDD-HHMMSS with same inode (hardlink)
ssh server 'stat /path/to/file.m4b /path/to/file.m4b.bak-*'

# 3. Wait for maintenance window (or trigger manually)
# 4. Verify old backups are cleaned up after PurgeSoftDeletedAfterDays
```

---

## 12. iTunes PID Detail Expansion

**Priority:** P2
**Difficulty:** Easy
**Files:** `web/src/pages/BookDetail.tsx`

### Fix
The iTunes banner already shows PID table. Enhance:
- Show the track name from iTunes XML alongside each PID
- Show the file path from the XML
- Add a "Refresh from iTunes" button that re-syncs this book's metadata from the XML

---

## Architecture Reference

### Key Files
| File | Purpose |
|------|---------|
| `internal/server/metadata_fetch_service.go` | Metadata search, apply, write-back, enrichment |
| `internal/server/isbn_enrichment.go` | Background ISBN/ASIN search |
| `internal/metadata/taglib_support.go` | Tag writing via taglib (Wasm) |
| `internal/metadata/metadata.go` | Tag extraction via ffprobe/taglib |
| `internal/server/file_pipeline.go` | File rename with path format templates |
| `internal/server/merge_service.go` | Book version merging |
| `internal/server/external_id_backfill.go` | iTunes PID backfill + ExternalIDStore interface |
| `internal/server/changelog_service.go` | Merged changelog from path/metadata/operation history |
| `internal/server/batch_poller.go` | Universal OpenAI batch poller |
| `internal/server/diagnostics_service.go` | ZIP export generation |
| `web/src/components/TagComparison.tsx` | Tag comparison with dropdown + diff highlighting |
| `web/src/components/ChangeLog.tsx` | Timeline with revert buttons |
| `web/src/pages/BookDetail.tsx` | Book detail page with Files & History tab |
| `web/src/pages/Diagnostics.tsx` | Diagnostics export + AI analysis page |

### Database Migrations
| # | Table/Change |
|---|-------------|
| 33 | `deferred_itunes_updates` — queued iTunes location updates |
| 34 | `external_id_map` — PID/ASIN/ISBN external ID mapping |
| 35 | `book_path_history` — file rename/move history |
| 36 | `genre TEXT` column on books table |

### Config Keys
| Key | Default | Purpose |
|-----|---------|---------|
| `root_dir` | — | Library root directory (must be set!) |
| `auto_rename_on_apply` | `true` | Rename files when metadata is applied |
| `auto_write_tags_on_apply` | `true` | Write tags when metadata is applied |
| `itunes_library_read_path` | — | Path to iTunes library file for sync (XML or ITL) |
| `itunes_library_write_path` | — | Path to iTunes Library.itl for write-back |
| `path_format` | `{author}/{series_prefix}{title}/{track_title}.{ext}` | File path template |
| `segment_title_format` | `{title} - {track}/{total_tracks}` | Segment filename template |
| `purge_soft_deleted_after_days` | 30 | TTL for soft-deleted books and `.bak-*` backups |

### Build & Deploy
```bash
make build          # Full build with embedded frontend
make build-linux    # Cross-compile for Linux (CGO via musl)
make deploy         # build-linux + scp + systemctl restart
make test           # Go backend tests
make test-all       # Backend + frontend tests
go test -tags=integration ./internal/transcode/...  # M4B conversion integration test
```
