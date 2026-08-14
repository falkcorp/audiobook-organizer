### Make metadata fields on the book page clickable (future improvement)

Requested by the owner 2026-08-13: from a book's detail page you cannot click the
author's name to jump to the library filtered by that author. Every metadata field that
identifies a *set* of books should be a link into the library with that filter applied.

- [ ] **Author name → library filtered by that author.** The most-wanted one. The API
      already supports it: `/api/v1/audiobooks?author_id=<id>`, and the book payload
      already carries `author_id` plus an `authors[]` array with `id`, `name`, `role`
      and `position` — so a book with several contributors should link each one
      separately rather than only the primary.
- [ ] **Series name → library filtered by that series.** `series_id` is on the payload
      and `?series_id=` is supported. Worth pairing with `series_index` so the link can
      land on the right position in the series.
- [ ] **Narrator, publisher, genre, and release year.** Same idea, but check each has a
      real filter behind it before making it a link — a link that silently returns the
      whole library is worse than plain text. `library_state` and tags already have
      filter support and are good candidates.
- [ ] **⚠️ Do not link `version_group_id` to a filtered view until the filter works.**
      `?filter=version_group_id:X` and `?version_group_id=X` are both **silently
      ignored** today — they return the entire library (count=63,870) rather than
      erroring. Fixing that filter is tracked in
      `20260813-search-index-repair-prod-findings.md`; a "other versions of this book"
      link depends on it.

Notes for whoever picks this up:

- Prefer real `<a href>` navigation over an onClick handler so the links are
  middle-clickable, openable in a new tab, and shareable — a filtered library view is
  exactly the kind of thing someone pastes to someone else.
- The library page's filter state lives in `useLibraryQuery.ts`; the link target needs
  to set the same query parameters the page already reads, so that landing on the URL
  and clicking the filter in the UI produce identical results.
- Remember the page also applies `is_primary_version=true` by default. An author link
  that inherits that default will hide non-primary copies — which is correct for
  browsing, but worth being deliberate about rather than accidental.
