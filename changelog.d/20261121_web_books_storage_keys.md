### Changed

- The audiobook module's localStorage keys (iTunes import settings, library page and page size, recent searches, tag cloud, the metadata-review and dedup preferences) now live in `web/src/modules/books/storageKeys.ts` (`BOOKS_STORAGE_KEYS`), with every literal value unchanged. `web/src/lib/storageKeys.ts` spreads them into `STORAGE_KEYS`, so no importer changes, and keeps the names that are bound for the shell until the shell adoption (F9-05). `booksStorageKeys.literal.test.ts` pins each literal, since renaming one would silently reset a user's saved preference.
