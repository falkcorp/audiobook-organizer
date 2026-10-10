### Added

- The Deluge client can now read each torrent's ratio, seeding time, added and completed times, finished flag and file list with per-file progress, and can remove a torrent together with its downloaded data. Nothing calls the remove method yet; it is groundwork for the post-organize cleanup.

### Changed

- The Deluge client now logs in again once and retries the request when Deluge's web session has expired, instead of failing every call until the app restarts.
