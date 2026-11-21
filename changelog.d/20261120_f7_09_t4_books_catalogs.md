### Added

- Dark slice of F7-09 T4: `internal/books/aicaps` (books' 13 AI capability specs, the HEAD-to-`books.*` ID map and the two batch job types' apply kinds) and `internal/books/topics` (the 12 notifiable `books.*.v1` event topics with typed payloads). Nothing registers them until the integrations flip, so runtime behaviour is unchanged. Core's catalog refuses `books.embed_text_batch` (`batch_api` on embed work); the refusal is pinned by name in the test and tracked in `todo.d`.
