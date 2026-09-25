### Fixed

- ABS API: books whose cover is a locally stored cover (for example art extracted from the audio file at import) now show that cover in AudioBooth. `media.coverPath` was null and `GET /api/items/:id/cover` answered 404 for them, because the ABS surface only looked for a cover file named after the book's id.
