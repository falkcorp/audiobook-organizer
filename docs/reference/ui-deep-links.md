<!-- file: docs/reference/ui-deep-links.md -->
<!-- version: 2.0.0 -->
<!-- guid: B610EF08-9346-47FD-AAA3-C65958E0127C -->
<!-- last-edited: 2026-11-05 -->

# UI deep links

Rule: **every place you can navigate to on screen has a URL, the address bar
always shows it, and pasting that URL lands on the same place.**

State stays the source of truth inside each page. The shared hook
`web/src/hooks/useUrlState.ts` mirrors it into the query string with
`setSearchParams(next, { replace: true })`, keeps keys it does not own, and
omits values equal to their default (so `/system` and `/system?tab=info` are the
same place and the bare one is what you see). A URL change that the page did
not write (a link click on the same route, browser back/forward, an edited
address) re-seeds the page state from the URL.

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
| `/library/:id` | metadata history dialog, retired-files list, snapshot compare | `history=1`, `retired=1`, `compare=<timestamp>` | done |
| `/works` | table sort | `sort=<title\|id\|author_id\|series_id\|alt_titles>`, `order=desc` (title ascending omitted) | done |
| `/system` | System Info / Storage / Quota / Maintenance tab | `tab=info\|storage\|quota\|maintenance` (info omitted) | done |
| `/settings` | 12 tabs | `tab=library\|itunes\|metadata\|dedup\|paths\|performance\|security\|api-keys\|plugins\|tools\|system\|temp-login` (library omitted; legacy `#slug` still read) | done |
| `/files` | directory on screen (written on every directory) | `path` (root omitted) | done |
| `/series` | search, with-books/empty/all filter, sort, page, page size | `q filter sort order page size` | done |
| `/authors` | search, with-books/zero-books/all filter, sort, page, page size | `q filter sort order page size` | done |
| `/authors/:id` | route param | `:id` | existing |
| `/diagnostics` | selected category card | `category=errors\|dedup\|metadata\|general` (any id the page lists) | done |
| `/activity` | expanded operation (opens on the page that holds it), feed filters, page | `op q tiers type level operation since until hide_noop tag page size` | done |
| `/playlists` | All / Static / Smart tab | `tab=static\|smart` (all omitted) | done |
| `/playlists/:id` | route param | `:id` | existing |
| `/collections` | All / Static / Dynamic tab | `tab=static\|dynamic` (all omitted) | done |
| `/collections/:id` | page of members | `page` | done |
| `/versions` | Trash / Purged / Swaps tab, trash-run filter | `tab=trash\|purged\|swaps` (trash omitted; a `set` link implies its tab), `op`, `set` | done (`op`, `set` existing) |
| `/users` | user whose sign-ins-and-data panel is open | `footprint=<userId>` | done |
| `/review` | lane, spine view mode | `lane=metadata\|dupes\|authors\|regroup\|repairs` (metadata omitted), `mode=two-column\|candidates` (compact omitted) | done |
| `/review` dupes | sub-view, layer, band, one-book filter | `view=labels\|suspicious`, `layer=exact\|acoustid\|embedding\|book_signature\|metadata_hash_match\|llm\|manual`, `band`, `book` | done (`band`, `book` existing) |
| `/review` dupes | pair compare drawer and its tab | `compare=<candidateId>` `ctab=files\|breakdown\|metadata\|fingerprint` | done |
| `/review` authors | sub-view, focused author | `view=series\|ai`, `author=<id>` | done |
| `/review` repairs | fixer, trial-row filter, row class | `fixer=<id>`, `filter=applicable\|skipped\|owner_applicable\|skipped:<kind>\|flag:<name>`, `class` | done |
| `/dedup`, `/dedup/labels`, `/books/dedup`, `/authors/dedup` | redirects to `/review` | keeps `book`, `band` | existing |
| `/config`, `/config/api-keys`, `/audiobookshelf*` | redirects (Audiobookshelf URLs) | | existing |
| `/setup`, `/login`, `/invite/:token` | pre-auth, no navigable state | | n/a |

Count: 31 rows: 21 done, 7 existing, 3 n/a. A row that gained a parameter
next to an existing one is counted under `done`.

## Behaviour that is easy to get wrong

- **Writes replace, they do not push.** Every page writes with
  `setSearchParams(next, { replace: true })`, so clicking through tabs does not
  fill the history and Back still leaves the page. A link to another view on the
  same route, or the browser's Back and Forward, re-seeds the page state.
- **Keys a page does not own are kept.** `?book=` on `/review` survives a lane
  change, `?keep=1` survives a tab click.
- **The Review workspace never writes `band` or `book`.** Those belong to the
  dupes lane's own filters and are read live from the URL.
- **Activity `since`.** Omitted for the default window; `since=all` means all
  history, because an empty value cannot be told from an absent one.
  `hide_noop=0` means no-op entries are shown (hiding them is the default).
- **A stale `?op=`.** An operation outside the window in the link has no row to
  host its log panel, so the link opens the page with no panel and auto-refresh
  stays paused (the page already pauses it while a panel is open).
- **Library tags are repeated** (`?tag=a&tag=b`), read with `getAll`. The
  Library writes its whole query string from state, so a parameter it did not
  write is dropped on the next click: that is why the preset filters had to be
  added to the writer.
- **Series and Authors** hold the page while a URL that changes the search and
  the page together waits for the debounced search to settle, so a link with
  both fetches once and lands on the page it names.

## Not linked on purpose

Confirm dialogs, edit, delete and purge dialogs, merge and invite forms,
snackbars, in-flight progress and selection checkboxes are transient actions,
not places. A link that reopened a destructive confirmation on paste would be a
footgun. Also left out:

- Excluded sources on Activity (a stored preference, not a place), the
  per-section pages of an expanded operation, and the mobile filter drawer.
- Repairs row `offset` and page size.
- Column visibility and widths on every table (stored per browser).
- Pre-auth pages and the dashboard cards.

## Split points

The work is stacked as commits, one per page group, so each can be its own PR.
Each commit from the tab pages onward carries its own headerless fragment in
`changelog.d/`. The first PR is the inventory, the hook and the tab pages: the
hook is unused on its own, so it travels with its first users. Subjects are the
stable handle; the short SHAs are from the stack as pushed and change on any
rebase.

1. `3a2f28111` docs(ui): inventory
2. `1ac16b0d1` feat(web): useUrlState hooks
3. `771140ae3` System, Settings, Playlists, Collections, Versions, Diagnostics, Files (fragment `2026-11-05_deep_links_url_state.md`; PR 1 is commits 1 to 3)
4. `59a3ae964` Review workspace
5. `0207e03ae` Activity
6. `0fe8bc878` Series and Authors
7. `03d7e2b6d` Library preset filters and tags
8. `1682f8047` Users
9. `8cde7b1b9` Book detail
10. `c748dad25` Collection members page
11. `8fe304c32` Works sort

Commits 4 to 10 depend only on commit 2 and are independent of one another,
with two notes: commit 8 also reformats `useUrlState.ts` (prettier only), and
commit 11 uses `setSort` on `useConfigurableTable`, which commit 6 adds, so it
follows commit 6.
