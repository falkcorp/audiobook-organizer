<!-- file: changelog.d/20260820_153000_dupes_link_repoint.md -->
<!-- version: 1.0.0 -->
<!-- guid: 81337a18-e556-4475-89ad-02be7867f9d9 -->
<!-- last-edited: 2026-08-20 -->

### Fixed

#### "View duplicates" on a book page now goes somewhere

The duplicate-metadata warning linked to `/dedup/candidates`, a route the app has
never registered, as a raw anchor, with no book id. It now links to the dupes lane
of `/review` for that specific book, through the router.
