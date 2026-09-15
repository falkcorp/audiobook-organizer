- [ ] **Normalize path keys to NFC before use as Pebble keys.** `book_file_path:`
      and book path keys are built from the raw string
      (internal/database/pebble_store.go, path-key builder near the
      `book_file_path` prefix), so the same folder spelled in NFD by macOS and
      NFC by Linux gets two keys: missed duplicates and double rows. Never
      observed in prod as of 2026-09-14; parked by the owner (audit A2#14). Fix
      needs a key migration: normalize on write and read, and a one-time
      rewrite of existing keys behind a maintenance op with a dry run.
