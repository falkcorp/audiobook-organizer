### Added

- `web/src/modules/books/module.tsx` and `manifest.ts`: `booksModule`, books' contribution to the shell (routes for 14 pages, the Library nav group with its match rules, task kinds, the books banner, the library search top-bar item, three widgets, the account-merge row action and two user-page panels), bound to a hand-written manifest. Nothing in the running app imports it yet; a vitest mounts it on `createShell` against a mocked core API.
