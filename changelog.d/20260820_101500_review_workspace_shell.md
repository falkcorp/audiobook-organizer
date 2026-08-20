<!-- file: changelog.d/20260820_101500_review_workspace_shell.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2d95f803-6a41-4e72-b8c0-5f1e7a3d9b46 -->
<!-- last-edited: 2026-08-20 -->

### Added

#### `/review` is now the unified review workspace

Dedup, metadata apply, and the review queue share one screen: a lane switcher, three
command menus, a filter rail, the comparison spine, and a bulk-action footer. The
metadata lane is live; the other two announce where their surface still lives rather
than rendering an empty spine.

There is no `review_show_legacy` toggle. One user, no migration window — a
compatibility gate would be pure cost with nobody to protect, and shipping both
surfaces indefinitely recreates the fragmentation this work exists to remove. The
safety net is git until the old surfaces are deleted.

#### Every candidate can now explain its own score, in place

The recorded scoring derivation renders wherever a reviewer judges a candidate: the
compact row's expanded detail and the two-column card. It replays the real pipeline —
base, then each multiplier and term in the order applied — and reconciles to the number
on the chip. A candidate scored before the instrumentation existed says so, because a
blank panel reads as "no signals fired" rather than "nothing was recorded".

### Changed

#### The metadata review dialog's data layer was lifted, not rewritten

Its filter chain, grouping pass, client-side paginator and debounced apply pipeline now
live in one hook. Callers take slices of the derivation chain rather than re-deriving
it, and behaviour that had never been tested now is — including two guards that only
misbehave under a race:

- **stale-response discard** — a slow page-1 fetch that resolves after page 2 must not
  overwrite it. Without this the reviewer reads stale rows under a fresh page number,
  with nothing on screen to indicate it.
- **page clamp** — filters can shrink the result set below the current page index.
  This is now *derived* rather than corrected in an effect, so there is no frame where
  the page slices past the end of the list and the spine flashes empty.

Two behaviours whose comments describe intent rather than mechanism are covered
directly, because both read as oversights and are not: a skipped row stays actionable
(skip means "not now"), and hiding multi-book matches also deselects them, so
"Apply Selected" cannot apply a book the toggle claims to have removed.

`handleClose` was deliberately **not** carried over. It exists because a dialog closes
and has one moment to tell the library to refresh; a route does not close. The
workspace refreshes when the apply operation actually finishes — more accurate than the
dialog's version, which fired on close whether or not the background op had done
anything.

### Fixed

#### A chip inside a paragraph broke the row layout it sat in

The compact row rendered its title as a `<p>` and put a `Chip` — a `<div>` — inside it
on the no-match and error branches. That is invalid HTML: the browser closes the
paragraph early and the chip escapes the row's layout. Carried in from the dialog by
the mechanical port and fixed here.
