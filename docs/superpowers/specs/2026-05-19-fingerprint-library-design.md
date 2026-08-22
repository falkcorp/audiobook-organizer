<!-- file: docs/superpowers/specs/2026-05-19-fingerprint-library-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: df0f0c38-33b2-4d04-8bc3-b242d1808d99 -->
<!-- last-edited: 2026-08-22 -->

# Fingerprint Library Design

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable users to browse, filter, and monitor fingerprint coverage across their library as a first-class feature using the existing Library infrastructure.

**Architecture:** The Fingerprint Library is not a new page — it's an entry point into the existing Library view with fingerprinting columns enabled by default. Users click "Fingerprints" in the left sidebar, which loads the Library page filtered to surface fingerprinting data. All filtering, sorting, column customization, and search leverage the existing system.

**Tech Stack:** React (Library page), Go backend (Book model + fingerprinting fields), existing filtering/column system, MUI components for visuals.

---

## Data Layer

### Book Model Extension

Extend the `Book` struct in `internal/models/book.go` to include computed fingerprinting fields:
- `FingerprintStatus` (enum: "none", "partial", "complete")
- `FingerprintedFileCount` (int)
- `TotalFileCount` (int)
- `CoveragePercent` (int 0-100)
- `LastFingerprintedAt` (nullable timestamp)

These fields are **computed** from the `BookFile` table:
- Scan all `BookFile` entries for a book
- Count how many have `AcoustIDSeg0` populated (= fingerprinted)
- Categorize: all fingerprinted = "complete", some = "partial", none = "none"
- Calculate coverage % as `(FingerprintedFileCount * 100) / TotalFileCount`
- Find the most recent `UpdatedAt` timestamp from fingerprinted files

### Fingerprinting Field Computation

Fingerprinting fields are **computed on every list request** (not cached) by:
1. Fetching all `BookFile` entries for each book (already done for file listing)
2. Counting how many have `AcoustIDSeg0` populated
3. Calculating status and coverage %
4. Finding the max `UpdatedAt` timestamp among fingerprinted files

This is done in the handler (e.g., `listAudiobooks`) or in a Store method to keep it localized.

### Database Queries

- **listAudiobooks endpoint**: Augment the response with fingerprinting fields for each book
- **No new database queries needed** — reuse existing BookFile fetches

---

## Frontend Architecture

### Column Registry

Add new column definitions to the Library column system (wherever `ColumnDefinition` is configured):

```
fingerprint_status
  label: "Fingerprint Status"
  type: badge
  displayValues: { "complete": "✓ Complete", "partial": "⚠ Partial", "none": "✗ None" }
  colors: { "complete": "success", "partial": "warning", "none": "default" }

coverage_percent
  label: "Coverage"
  type: percentage_bar
  displayFormat: "N/M files (P%)" where N=fingerprinted, M=total, P=percent

fingerprinted_files
  label: "Fingerprinted Files"
  type: text
  displayFormat: "N/M" where N=fingerprinted, M=total

last_fingerprinted_date
  label: "Last Fingerprinted"
  type: date
  displayFormat: relative time (e.g., "2 days ago")

fingerprint_visual_waveform
  label: "Waveform"
  type: visual
  width: 100px
  height: 30px
  renders: horizontal waveform of audio (from fingerprint data)

fingerprint_visual_spectrogram
  label: "Spectrogram"
  type: visual
  width: 100px
  height: 30px
  renders: frequency heatmap over time (from fingerprint data)
```

### Sidebar Navigation

Add a new link in the left sidebar (alongside Library, Duplicates, etc.):
```
Fingerprints
  icon: waveform/chart icon
  link: /fingerprints (custom route that pre-selects fingerprinting columns)
```

When user clicks, the Library page loads with:
- Default columns: title, author, fingerprint_status, coverage_percent, last_fingerprinted_date, fingerprint_visual_waveform
- Fingerprinting fields are highlighted/prominent
- Full filtering/sorting/column customization still available

The `/fingerprints` route internally redirects to the Library page with a preset column configuration (or the Library page component detects `route=fingerprints` and applies the preset).

### Expandable Files

When user expands a book row, show child rows for each file:
```
- Book Title
  - file_1.m4b (✓ fingerprinted)
  - file_2.m4b (✗ not fingerprinted)
  - file_3.m4b (✓ fingerprinted)
```

File-level columns:
- Filename
- Format
- Size
- Fingerprint status (✓/✗)
- Optional: individual file waveform/spectrogram

---

## Backend API

### Response Enhancement

The existing `GET /api/v1/audiobooks` endpoint (with query params) should include fingerprinting fields in each book object:

```json
{
  "id": "book-id",
  "title": "Title",
  "author": "Author",
  "fingerprint_status": "partial",
  "fingerprinted_file_count": 3,
  "total_file_count": 4,
  "coverage_percent": 75,
  "last_fingerprinted_at": "2026-05-19T10:30:00Z",
  // ... other existing fields
}
```

No new endpoints needed — reuse the existing list/filter infrastructure.

### Filtering Support

The existing filter system should support fingerprinting fields:
```
?filter=fingerprint_status:complete
?filter=fingerprint_status:partial,none
?filter=coverage_percent:>50
```

If the filter system is tag/facet-based, extend it to include fingerprinting dimensions.

---

## Data Flow

1. User clicks "Fingerprints" in left sidebar
2. Navigates to `/library?view=fingerprints` (or `/fingerprints`)
3. Library page requests `GET /audiobooks?columns=fingerprinting&sort=coverage_percent` (or similar preset)
4. Backend returns books with fingerprinting fields populated
5. Frontend renders Library table with fingerprinting columns visible
6. User can:
   - Filter by fingerprint_status, coverage %, date range
   - Sort by any column
   - Customize visible columns
   - Expand books to see file-level details
   - Search by title/author (existing search)

---

## Error Handling

- If fingerprinting data is missing/corrupt for a book, display "—" (dash) in fingerprint columns
- If a file's fingerprint status cannot be determined, mark as "unknown" rather than "none"
- No blocking errors — fingerprinting columns degrade gracefully

---

## Testing

### Unit Tests
- Test fingerprinting field computation (fingerprinted count, coverage %, status categorization)
- Test response marshaling includes fingerprinting fields
- Test filter/sort by fingerprinting fields

### Integration Tests
- Load Library page, navigate to Fingerprints
- Verify fingerprinting columns appear
- Test filtering by status, coverage %, date
- Expand a book, verify file-level fingerprint status
- Customize columns, save, reload — fingerprinting columns persist

### Manual Testing
- Visual: waveform/spectrogram render correctly (compare against known fingerprint data)
- Sorting: books sorted by coverage % shows ascending/descending correctly
- Filtering: status filter shows correct books
- Expandable files: expand/collapse works, file status matches database

---

## Scope

- **Included**: Fingerprinting columns added to Library, sidebar link, reuse existing filtering/sorting/column system
- **Not included**: Fingerprint generation (separate Spec 2), fingerprint repair/rescan per-book (handled by main scan operation)
- **Out of scope**: Waveform/spectrogram rendering implementation details (can use existing audio visualization libraries or simple SVG)

---

## Implementation Notes

### Waveform and Spectrogram Generation

Waveform and spectrogram visuals are **generated from the fingerprint data** (AcoustIDSeg0–Seg6). Each segment can be visualized as:
- **Waveform**: Each segment's confidence/strength renders as a bar height in a horizontal chart
- **Spectrogram**: Segments can be color-mapped (low→blue, high→red) to show frequency intensity over time

Implementation can use:
- Simple SVG bars/rectangles for waveform
- SVG canvas heatmap for spectrogram
- Or an existing audio visualization library if available

This is a **frontend rendering detail** — the backend only returns the raw `AcoustIDSeg0–Seg6` values, and the frontend decides how to visualize them.

### General Notes

- All fingerprinting fields are **read-only** at the UI level (no editing fingerprints directly from Library)
- Fingerprinting columns are optional — users can hide them like any other columns
- The "Fingerprints" sidebar link is a convenience; users can also access fingerprinting columns from the main Library by customizing columns
- File-level expansion uses existing book-expansion UI pattern; no new components needed
- Fingerprinting fields degrade gracefully if data is missing (show "—" or "unknown")
