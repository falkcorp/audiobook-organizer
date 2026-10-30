<!-- file: docs/decisions/2026-10-30-dedup-candidates-author-name.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3e9b5733-5fe2-4426-9fdb-42f08f9249d7 -->
<!-- last-edited: 2026-10-30 -->

# Dedup candidate endpoints send author_name

## Decision log

- Chosen: extract the name resolution from `(*Server).resolveAuthorAndSeriesNames` into `database.ResolveBookNames(BookNameReader, *Book)`. `enrichBookForResponse` and both dedup endpoints call it, so there is one definition of "the author of a book". The dedup package cannot import `internal/server`, which is why the function lives in `database`.
- Chosen: the candidate list's `bookWithITunes` became `candidateBook` (adds `author_name`, `series_name`; `is_itunes` unchanged) and `attachITunesFlags` became `attachBookViews`, so the page is decorated in one pass. A per-response `nameCache` keeps one author read per distinct author on the page.
- Chosen: the breakdown endpoint's `bookDetail` gained the same two keys, since `DedupBookDetail` (FileInfoCompare, AudioSamplePair) reads `author_name`.
- Chosen: lookup errors stay fail-open (name omitted), identical to the audiobooks responses. Rejected: failing the page on an author read error; that would make dedup stricter than every other endpoint for a display label.
- Rejected: copying the resolution into the dedup package (drift); widening `DedupStore` (it already has `GetAuthorByID` / `GetSeriesByID`).
- Rejected: dropping `author_name` from the TS `Book` (the audiobooks endpoints send it).
- Not done: `HandleCompareAcoustID` also returns bare books but has no TypeScript reader. The `resolveBookIDsMatching` doc comment in `search.go` still says the endpoint omits `author_name`; left alone because another branch rewrites that function. No Vitest added: the Dupes components already render `book.author_name` and a test with a hand-built fixture would only restate the prop; the Go tests pin the payload.
