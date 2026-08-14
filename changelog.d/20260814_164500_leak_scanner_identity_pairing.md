### Fixed

- The memory-leak scanner no longer reports a false `addEventListener without
  removeEventListener` when the add is nested deeper than the cleanup (the
  2026-08-11 apiFetch.ts false positive). Named handlers now pair by handler
  identity anywhere in the file; anonymous handlers keep the original
  look-ahead. Regression tests added (`scripts/test_check_memory_leaks.py`).
