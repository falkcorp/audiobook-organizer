# Task 017: CAT — Tag cloud / "has tag X" filter on library page

**Depends on:** none
**Estimated effort:** S–M
**Wave:** 6 (features, independent)

## Goal

Add a browsable tag cloud and/or a "has tag: Science Fiction" filter on the library page so
users can find books by their Audible category tags (and user tags).

## Context

- Audible category tags are stored via `AddBookTagWithSource("audible_category")` — already shipped
- User tags: `book_tags` table (migration 37), methods `AddBookUserTag`, `GetBookTags`
- Library filter hook: `web/src/hooks/useLibraryFilters.ts` — already extracts filter state
- Library API: `GET /api/v1/audiobooks` with filter params (check what tag-filter params already exist)
- Book tags endpoint: `GET /api/v1/audiobooks/:id/tags` already exists; need `GET /api/v1/tags` for all tags

## Files to modify

- `internal/database/store.go` — add `GetAllBookTags() ([]BookTag, error)` if not present
- `internal/database/pebble_store.go` — implement it
- `internal/server/server_lifecycle.go` — add route `GET /api/v1/tags`
- `internal/server/user_tags_handlers.go` (or similar) — implement the handler
- `web/src/pages/Library.tsx` — add tag cloud panel or filter chip group
- `web/src/hooks/useLibraryFilters.ts` — add `selectedTags` filter state
- `web/src/services/api.ts` — add `getAllTags()` call

## Instructions

### 1. Backend: `GET /api/v1/tags`

Returns all unique tag names with count:
```json
[
  {"name": "Science Fiction", "count": 342, "source": "audible_category"},
  {"name": "Fantasy", "count": 198, "source": "audible_category"},
  {"name": "favorite", "count": 12, "source": "user"}
]
```

Add `GetAllBookTagsWithCounts(ctx) ([]BookTagSummary, error)` to the store interface and
implement in PebbleDB.

### 2. Backend: filter by tag in `GET /api/v1/audiobooks`

Add query param `?tag=Science+Fiction` (or `?tags[]=...` for multiple). In the PebbleDB
`GetAllBooks` / `GetAllBookSummaries` implementation, filter books whose tag set includes
the requested tag. Check how other filters (author, series, etc.) are applied for the pattern.

### 3. Frontend: tag cloud in filter panel

In the library's filter sidebar (or `useLibraryFilters`), add a tag cloud:
```tsx
// Scrollable list of tag chips; click to toggle
<Box sx={{ maxHeight: 200, overflow: 'auto' }}>
  {allTags.map(tag => (
    <Chip
      key={tag.name}
      label={`${tag.name} (${tag.count})`}
      onClick={() => toggleTag(tag.name)}
      color={selectedTags.includes(tag.name) ? 'primary' : 'default'}
      size="small"
      sx={{ m: 0.25 }}
    />
  ))}
</Box>
```

### 4. Wire filter to API

When `selectedTags` changes, include `tag=X` in the library fetch params.

## Test

```bash
go test ./internal/server/... -run TestTags -v -count=1
npm test   # in web/
make ci
```

Manual: open library, see tag cloud, click "Science Fiction", library filters to tagged books.

## Commit

```
feat(library): tag cloud filter — browse and filter by Audible/user tags
```

## PR title

`feat(library): tag cloud + has-tag filter`

## After merging

Mark `- [ ] Search/filter: "has tag Science Fiction"` as `- [x]` in `TODO.md`.
