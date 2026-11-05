<!-- file: docs/reference/ui-deep-links.md -->
<!-- version: 1.0.0 -->
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

This file starts life as the inventory (route, navigable state, param, status)
and is updated as each page lands. Status: `done` = shipped in this change,
`existing` = already worked before it, `n/a` = nothing to link.

## Inventory

| Route | Navigable state | Param | Status |
|---|---|---|---|
| `/` | redirect to `/dashboard` | | n/a |
| `/dashboard` | none (cards link out) | | n/a |
| `/library` | search, filters, sort, order, view mode, page, page size, first tag | `search author series genre language state version_group_id is_primary_version series_id sort order view page limit tag` | existing |
| `/library` | preset filters read on arrival but never written back | `has_file_errors fingerprint_status coverage_percent_min coverage_percent_max missing_covers in_import_path no_isbn duplicates_flagged` | planned |
| `/fingerprints` | Library with the fingerprints preset | same as `/library` | planned |
| `/library/:id` | Info / Files & History tab | `tab=info\|files` | existing |
| `/works` | none | | n/a |
| `/system` | System Info / Storage / Quota / Maintenance tab | `tab=info\|storage\|quota\|maintenance` | planned |
| `/settings` | 12 tabs | `tab=<slug>` (legacy `#slug` still read) | planned |
| `/files` | selected directory | `path` | planned |
| `/series` | search, with-books/empty/all filter, page, page size | `q filter page size` | planned |
| `/authors` | search, with-books/zero-books/all filter, page, page size | `q filter page size` | planned |
| `/authors/:id` | route param | `:id` | existing |
| `/diagnostics` | selected category | `category` | planned |
| `/activity` | expanded operation, feed filters, page | `op q tiers type level operation since until hide_noop tag page size` | planned (`op` read-only today) |
| `/playlists` | All / Static / Smart tab | `tab=all\|static\|smart` | planned |
| `/playlists/:id` | route param | `:id` | existing |
| `/collections` | All / type tab | `tab=all\|static\|smart` | planned |
| `/collections/:id` | page of members | `page` | planned |
| `/versions` | Trash / Purged / Swaps tab, trash-run filter | `tab=trash\|purged\|swaps`, `op`, `set` | partly existing (`op`, `set`) |
| `/users` | user whose footprint or merge dialog is open | `footprint=<userId>` `merge=<userId>` | planned |
| `/review` | lane | `lane=metadata\|dupes\|authors\|regroup\|repairs` | planned (read-only today) |
| `/review` dupes | sub-view, layer, band, one-book filter | `view=labels\|suspicious`, `layer`, `band`, `book` | band/book existing, rest planned |
| `/review` dupes | pair compare drawer and its tab | `compare=<candidateId>` `ctab=files\|breakdown\|metadata\|fingerprint` | planned |
| `/review` authors | sub-view, focused author | `view=series\|ai`, `author=<id>` | planned |
| `/review` repairs | fixer, trial-row filter, row class | `fixer=<id>`, `filter`, `class` | planned (`fixer` read-only today) |
| `/dedup`, `/dedup/labels`, `/books/dedup`, `/authors/dedup` | redirects to `/review` | keeps `book`, `band` | existing |
| `/config`, `/config/api-keys`, `/audiobookshelf*` | redirects (Audiobookshelf URLs) | | existing |
| `/setup`, `/login`, `/invite/:token` | pre-auth, no navigable state | | n/a |

## Not linked on purpose

Confirm dialogs, rename and merge forms, snackbars, in-flight progress and
selection checkboxes are transient actions, not places. A link that reopened a
destructive confirmation on paste would be a footgun.

## Split points

The work lands as stacked commits, one per page group, so each can be its own PR:
hook and inventory, then System/Settings/other tab pages, Review, Activity,
Series/Authors/Files/Diagnostics, Library gaps, Users.
