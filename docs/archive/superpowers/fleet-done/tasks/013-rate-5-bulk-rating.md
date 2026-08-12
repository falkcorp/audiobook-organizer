<!-- file: docs/archive/superpowers/fleet-done/tasks/013-rate-5-bulk-rating.md -->
<!-- version: 1.0.2 -->
<!-- guid: f3a1b2c4-5d6e-7890-abcd-1234567890ab -->
<!-- last-edited: 2026-08-11 -->

# Task 013: RATE-5 — Bulk rating view / quick-rate from list

**Depends on:** none
**Estimated effort:** S–M
**Wave:** 6 (features, independent)

## Goal

Verify whether RATE-5 is already complete, and if not, add the missing "quick-rate from list"
inline UX (rating stars visible on each row in the library list without opening a dialog).

## Context

- `PATCH /api/v1/audiobooks/:id/rating` already exists (`metadata_handlers.go:1841`)
- `BulkRatingDialog` component already exists (`web/src/components/audiobooks/BulkRatingDialog.tsx`)
  and is wired into `LibraryDialogs.tsx` and `Library.tsx` with an "onRateClick" button
- The bulk dialog applies overall/story/performance/notes ratings across selected books
- RATE-5 originally called for "bulk rating view / quick-rate from list" — check if the
  existing BulkRatingDialog satisfies this, or if an inline star-rating column is still wanted

## Files to check first

- `web/src/components/audiobooks/BulkRatingDialog.tsx` — read the full component
- `web/src/pages/Library.tsx` — check if the Rate button is visible in the toolbar
  when books are selected
- `web/src/components/library/LibraryToolbar.tsx` — check for Rate button

## Instructions

### Step 1: Verify existing implementation

1. Read `BulkRatingDialog.tsx` fully — does it:
   - Accept multiple book IDs?
   - Apply ratings in bulk via `PATCH /api/v1/audiobooks/:id/rating`?
   - Show progress?
   - Handle partial failures?

2. Read `Library.tsx` around line 1659 — is the Rate button visible and connected to
   selected books?

3. If the answer to all of the above is YES, then RATE-5 is already done. Mark it complete
   in `TODO.md` and skip further implementation. Create a PR with just the TODO update.

### Step 2: If not complete, add inline star ratings

If the bulk dialog exists but there's no "quick rate" inline column in the book list:

**Add a `UserRating` column to the library grid:**

In `web/src/components/library/LibraryBookGrid.tsx` (or wherever the book list renders rows):
- Add a `UserRating` column (optional, off by default)
- Each row shows `<Rating value={book.user_rating_overall ?? 0} precision={0.5} size="small"
  onChange={(_, val) => updateRating(book.id, val)} />`
- Call `PATCH /api/v1/audiobooks/:id/rating` on change (debounce 500ms)
- Show a spinner while saving; toast on error

This gives one-click rating without opening a dialog.

## Test

```bash
npm test   # in web/
make ci
```

Manual: select books in library, click Rate — verify bulk dialog works. If inline column
was added, change a rating and verify it persists on refresh.

## Commit

```
feat(library): bulk rating dialog + inline quick-rate column (RATE-5)
```
Or if already done:
```
chore(todo): mark RATE-5 complete (BulkRatingDialog already ships bulk rating)
```

## PR title

`feat(library): quick-rate from list — RATE-5`

## After merging

Mark `- [ ] **RATE-5**` as `- [x]` in `TODO.md`.
