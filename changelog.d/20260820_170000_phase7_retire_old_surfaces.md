<!-- file: changelog.d/20260820_170000_phase7_retire_old_surfaces.md -->
<!-- version: 1.0.0 -->
<!-- guid: 79781fc0-cf3d-4ee4-aafd-7d1592b6ddc0 -->
<!-- last-edited: 2026-08-20 -->

### Removed

#### The surfaces `/review` replaced

`MetadataReviewDialog`, the standalone `ReviewQueue` page and `UnifiedDedupTab` are
deleted, along with the "Legacy View" toggle on the dedup page. Everything they did
lives in the three lanes of `/review`.

The other nine dedup tabs stay. Only `UnifiedDedupTab` overlapped the dupes lane —
author dedup, series dedup, split-books, reconcile, AI review, embedding clusters and
AcoustID are separate tools the workspace does not cover. They are no longer framed
as "legacy". The metadata search dialogs stay too: they *populate* candidates, which
is the opposite of reviewing them, and they are the only callers of that endpoint.

### Fixed

#### Rescore no longer reports a dry run as the real thing

The workspace's "Rescore" command called the rescore endpoint with `apply=false` and
reported "Rescore started". It inspected candidates and wrote nothing. It is now two
commands — `Rescore (dry run)` and `Rescore and apply…` — with the applying one
behind a confirmation.

#### "Review" in the operations bell no longer reloads the app

It set `window.location.href` to reach a modal over the library. It navigates to
`/review`.
