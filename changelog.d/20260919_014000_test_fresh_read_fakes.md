### Fixed

- Test-only: the in-memory store used by many tests now behaves like the real database when a book is edited, so the tests that guard against lost book edits can no longer pass by accident.
