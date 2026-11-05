<!-- file: docs/reference/ui-deep-links.md -->
<!-- version: 2.2.0 -->
<!-- guid: B610EF08-9346-47FD-AAA3-C65958E0127C -->
<!-- last-edited: 2026-11-05 -->

# UI deep links

Rule: **every place you can navigate to on screen has a URL, the address bar
always shows it, and pasting that URL lands on the same place.**

State stays the source of truth inside each page. The shared hook
`web/src/hooks/useUrlState.ts` mirrors it into the query string with
`setSearchParams(next, { replace: true })` and omits values equal to their
default (so `/system` and `/system?tab=info` are the same place and the bare one
is what you see). A URL change that the page did not write (a link click on the
same route, an edited address) re-seeds the page state from the URL, and then
the hook runs one more mirror pass so that anything the page did not take up is
corrected in the address bar (see "Behaviour that is easy to get wrong").

The table below is the inventory: every route in `web/src/App.tsx`, the state
on it that changes what is shown, the query parameter that carries it, and its
status. `done` = shipped in the deep-links change, `existing` = already worked
before it, `n/a` = nothing to link. A value equal to its default is left out of
the address, and `page` is always 1-based in a URL.

## Inventory

| Route | Navigable state | Param | Status |
|---|---|---|---|
| `/` | redirect to `/dashboard` | | n/a |
| `/dashboard` | none (cards link out; the organize confirm is a transient dialog) | | n/a |
| `/library` | search, filters, sort, order, view mode, page, page size | `search author series genre language state version_group_id is_primary_version series_id sort order view page limit` | existing |
| `/library` | every selected tag (repeated: `?tag=a&tag=b`) | `tag` | done (only the first tag was written before) |
| `/library` | preset filters, now written back so another control does not drop them | `has_file_errors fingerprint_status coverage_percent_min coverage_percent_max missing_covers in_import_path no_isbn duplicates_flagged` | done |
| `/fingerprints` | its own route (Library with the fingerprints preset) | same as `/library` | existing |
| `/library/:id` | Info / Files & History tab | `tab=info\|files` | existing |
| `/library/:id` | metadata history dialog, retired-files list (fetched only on the Files & History tab), snapshot compare (an RFC 3339 timestamp; anything else is removed) | `history=1`, `retired=1`, `compare=<timestamp>` | done |
| `/works` | table sort | `sort=<title\|id\|author_id\|series_id\|alt_titles>`, `order=desc` (title ascending omitted) | done |
| `/system` | System Info / Storage / Quota / Maintenance tab | `tab=info\|storage\|quota\|maintenance` (info omitted) | done |
| `/settings` | 12 tabs | `tab=library\|itunes\|metadata\|dedup\|paths\|performance\|security\|api-keys\|plugins\|tools\|system\|temp-login` (library omitted; legacy `#slug` still read) | done |
| `/files` | directory on screen (written on every directory; an outside change of `path` moves the browser) | `path` (root omitted) | done |
| `/series` | search, with-books/empty/all filter, sort, page, page size | `q filter sort order page size` | done |
| `/authors` | search, with-books/zero-books/all filter, sort, page, page size | `q filter sort order page size` | done |
| `/authors/:id` | route param | `:id` | existing |
| `/diagnostics` | selected category card | `category=errors\|dedup\|metadata\|general` (any id the page lists) | done |
| `/activity` | expanded operation (opens on the page that holds it), feed filters, page | `op q tiers type level operation since until hide_noop tag page size`; `size` is one of 25, 50, 100, 250; `type` and `level` must be known values; `tiers` is a list of the known tiers | done |
| `/operations` | redirect to `/activity`, keeping the query string (`?op=` survives) | | existing (query kept: done) |
| `/maintenance` | redirect to `/system?tab=maintenance` | | existing (target tab: done) |
| `/playlists` | All / Static / Smart tab | `tab=static\|smart` (all omitted) | done |
| `/playlists/:id` | route param | `:id` | existing |
| `/collections` | All / Static / Dynamic tab | `tab=static\|dynamic` (all omitted) | done |
| `/collections/:id` | page of members (a page past the end is clamped, and the clamped page is written back) | `page` | done |
| `/versions` | Trash / Purged / Swaps tab, trash-run filter | `tab=trash\|purged\|swaps` (trash omitted; a `set` link implies its tab), `op`, `set` | done (`op`, `set` existing) |
| `/users` | user whose sign-ins-and-data panel is open | `footprint=<userId>` | done |
| `/review` | lane, spine view mode | `lane=metadata\|dupes\|authors\|regroup\|repairs` (metadata omitted), `mode=two-column\|candidates` (compact omitted) | done |
| `/review` dupes | sub-view, layer, band, one-book filter | `view=labels\|suspicious`, `layer=exact\|acoustid\|embedding\|book_signature\|metadata_hash_match\|llm\|manual`, `band=high\|medium\|low` (the dedup band values; others are removed), `book` | done (`band`, `book` existing) |
| `/review` dupes (Clusters view, `mode=candidates`) | By-series toggle | `group=series` | done |
| `/review` dupes | pair compare drawer and its tab | `compare=<candidateId>` `ctab=files\|breakdown\|metadata\|fingerprint` | done |
| `/review` authors | sub-view, focused author | `view=series\|ai`, `author=<id>` | done |
| `/review` repairs | fixer, trial-row filter, row class | `fixer=<id>`, `filter=applicable\|skipped\|owner_applicable\|skipped:<kind>\|flag:<name>` (the suffix is a short token of letters, digits, `_`, `.`, `-`), `class` (same token rule) | done |
| `/dedup`, `/dedup/labels`, `/books/dedup`, `/authors/dedup` | redirects to `/review` | keeps `book`, `band` | existing |
| `/config`, `/config/api-keys`, `/audiobookshelf*` | redirects (Audiobookshelf URLs) | | existing |
| `/setup`, `/login`, `/invite/:token` | pre-auth, no navigable state | | n/a |

Count: 34 rows: 22 done, 9 existing, 3 n/a. A row that gained a parameter
next to an existing one is counted under `done`; the two legacy redirects
(`/operations`, `/maintenance`) are counted under `existing`.

## Behaviour that is easy to get wrong

- **Writes replace, they do not push, except the Library's page change.** Every
  page writes with `setSearchParams(next, { replace: true })`, so clicking through
  tabs does not fill the history and Back still leaves the page. The one
  exception is `/library`: changing its page pushes an entry (older behaviour,
  kept on purpose) so Back walks the pages; every other Library change replaces.
  Nothing else gives you back/forward moves between in-page views. A link to
  another view on the same route re-seeds the page state.
- **Keys a page does not own are kept.** `?book=` on `/review` survives a lane
  change, `?keep=1` survives a tab click, and an unknown key (`?utm_source=`) on
  `/library` survives a filter edit: the Library rebuilds its query from state
  but copies every key outside the list in `web/src/pages/libraryUrl.ts`
  (`?reset=1` is consumed, not copied).
- **The address bar and the screen cannot disagree.** After a URL change that
  the page did not write, the hook runs one reconciling mirror pass. A link the
  page only partly takes up (a bare `/review` while on Repairs, an unknown
  `?fixer=`, a `?book=` that was cleared) is rewritten to the state the screen is
  actually in. An unknown value of a validated parameter (`band`, `compare`,
  `size`, `type`, `level`, `tiers`, `class`) is removed or falls back to the
  default. The one thing it leaves alone is a bad value that was already in the
  URL when the page first mounted and that the page never consumes: the mount
  pass does not write, so an unused seed is not erased.
- **Writes are coalesced and safe.** A write is merged into the LIVE address (the
  history object's location), not the last rendered query, so two writes in one
  event merge instead of one reverting the other. A write is dropped, not
  delayed, when the live pathname is no longer the page's (a link to a page whose
  chunk is still loading leaves the old page mounted) or the live query is
  neither the rendered one nor one the hook wrote (an external navigation is in
  flight); the reconciling pass after that navigation rewrites whatever the page
  still needs. The mirror writes at most about twice a second (a leading and a
  trailing write) so holding `j` on the authors lane does not call
  `history.replaceState` once per key repeat, which browsers rate-limit with a
  `SecurityError`; a trailing write still waiting when the page unmounts is
  flushed if the address is still the page's own. A write that throws is logged
  as `Could not update the address bar` and forgotten, and the page keeps
  working. Limit: leaving for another route inside the throttle window cannot
  amend the entry that was left, so Back can land on the query from the last
  write that went out.
- **A link's `fixer=` and `author=` survive the lane's load.** While the list a
  seed refers to is still loading, the lane reports the seed itself as the value
  to mirror, so another change on the page does not erase it.
- **Typed searches are mirrored once settled.** Activity, Series and Authors put
  the debounced search in the URL, not every keystroke, and an outside change of
  the search takes effect at once. Series and Authors also hold the page while a
  URL that changes the search and the page together waits for the debounced
  search to settle, so a link with both fetches once and lands on the page it
  names.
- **The Review workspace never writes `band` or `book` itself.** Those belong to
  the dupes lane's own filters and are read live from the URL; the panel writes
  them through the shared writer in `useUrlState.ts`.
- **Activity `since`.** Omitted for the default window; `since=all` means all
  history, because an empty value cannot be told from an absent one. Any other
  value that is not a date falls back to the default window. `hide_noop=0` means
  no-op entries are shown (hiding them is the default). Clearing the filters
  restores the real default tier set, which includes `info`.
- **A stale `?op=`.** An operation outside the window in the link has no row to
  host its log panel, so the link opens the page with no panel and auto-refresh
  stays paused (the page already pauses it while a panel is open).
- **Library tags are repeated** (`?tag=a&tag=b`), read with `getAll`. The
  Library writes its whole query string from state, so a parameter it owns but
  did not write would be dropped on the next click: that is why the preset
  filters had to be added to the writer.

## Not linked on purpose

Confirm dialogs, edit, delete and purge dialogs, merge and invite forms,
snackbars, in-flight progress and selection checkboxes are transient actions,
not places. A link that reopened a destructive confirmation on paste would be a
footgun. Also left out:

- Excluded sources on Activity (a stored preference, not a place), the
  per-section pages of an expanded operation, and the mobile filter drawer.
- Repairs row `offset` and page size.
- The Duplicates lane's other Clusters controls (the expanded cluster, selection).
- Column visibility and widths on every table (stored per browser).
- Pre-auth pages and the dashboard cards.
