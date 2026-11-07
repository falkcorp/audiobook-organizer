### Added

- ABS API: `GET /api/libraries/:libraryId/stats` (genre counts), `POST /api/items/batch/get` and `GET|HEAD /api/authors/:id/image` (404, no author images stored) so the Lissen Android client's Genre view, series search and author pages work instead of falling through to the `/api/v1` redirect.
