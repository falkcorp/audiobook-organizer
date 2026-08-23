### Fixed

- **Renaming or organizing a single book no longer leaves a permanent stray record
  behind.** Each of those actions created an operation record that was never
  completed, never displayed anywhere, and never cleaned up — one per rename, one
  per organized book, accumulating for as long as the library has been in use.
  Nothing read them, so nothing noticed. They are no longer created.
