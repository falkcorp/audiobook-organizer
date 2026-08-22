<!-- file: docs/superpowers/fleet-tasks/029-acoustid-compare-1.md -->
<!-- version: 1.0.0 -->
<!-- guid: 933f9dae-ce59-4bc7-b0dd-5debfc96b828 -->
<!-- last-edited: 2026-08-22 -->

# Task 029: ACOUSTID-COMPARE-1 — Manual two-book acoustic comparison tool

**Depends on:** none (independent of other AcoustID tasks)
**Estimated effort:** L
**Wave:** 8 (AcoustID)

## Goal

Add a manual comparison tool: given two book or file IDs, compute/fetch fingerprint segments
and return a similarity score + per-segment breakdown. API + UI side-by-side viewer.

## Context

- API: `POST /api/v1/books/{id}/compare-acoustid?other={id2}` (or file-level)
- Response: overall similarity %, per-segment scores (seg0–seg6), both books' metadata
- UI: picker in Maintenance tab or BookDetail — select any two books, see side-by-side diff

## Files to create/modify

- `internal/server/acoustid_handlers.go` — add comparison handler
- `internal/server/server_lifecycle.go` — register route
- `web/src/services/api.ts` — add `compareAcoustID(bookAID, bookBID)` call
- `web/src/pages/Maintenance.tsx` — add comparison tool panel
  (OR `web/src/pages/BookDetail.tsx` — add compare button in Files tab)

## Instructions

### 1. Comparison handler

```go
// POST /api/v1/books/:id/compare-acoustid?other=<bookID2>
type AcoustIDCompareResponse struct {
    BookA          BookSummary         `json:"book_a"`
    BookB          BookSummary         `json:"book_b"`
    OverallScore   float64             `json:"overall_score"`   // 0.0–1.0
    SegmentScores  []SegmentComparison `json:"segment_scores"`  // 7 entries
}

type SegmentComparison struct {
    Segment string  `json:"segment"`  // "seg0", "seg1", ...
    ScoreA  string  `json:"score_a"`  // fingerprint hash or "" if missing
    ScoreB  string  `json:"score_b"`
    Match   bool    `json:"match"`
}

func (s *Server) handleCompareAcoustID(c *gin.Context) {
    idA := c.Param("id")
    idB := c.Query("other")
    if idB == "" {
        httputil.RespondWithBadRequest(c, "missing ?other= param")
        return
    }

    filesA, _ := s.store.GetBookFiles(c.Request.Context(), idA)
    filesB, _ := s.store.GetBookFiles(c.Request.Context(), idB)
    // Use primary file (first) or file with most segments for comparison
    resp := compareFiles(primaryFile(filesA), primaryFile(filesB))
    httputil.RespondWithOK(c, resp)
}
```

### 2. UI: Maintenance comparison panel

Add a "Compare Two Books" section:
```tsx
// Two autocomplete pickers (search by title/author)
// "Compare" button → calls compareAcoustID(idA, idB)
// Results: two book cards side-by-side
// Segment table: 7 rows (Intro, Body 1-5, Outro)
//   each row: colored match indicator (green=match, red=mismatch, gray=missing)
//   + the fingerprint hash snippet
// Overall score badge: "87% similar" with color coding
```

### 3. Optional: BookDetail compare button

In BookDetail Files tab, add "Compare with..." button that opens a book picker then navigates
to the comparison view (or opens an inline panel).

## Test

```bash
go test ./internal/server/... -run TestCompareAcoustID -v -count=1
npm test
make ci
```

Manual: select two books with known fingerprints, verify comparison shows correct match %.

## Commit

```
feat(acoustid): manual two-book fingerprint comparison tool (ACOUSTID-COMPARE-1)
```

## PR title

`feat(acoustid): acoustic fingerprint comparison tool — ACOUSTID-COMPARE-1`

## After merging

Mark `- [ ] **ACOUSTID-COMPARE-1**` as `- [x]` in `TODO.md`.
