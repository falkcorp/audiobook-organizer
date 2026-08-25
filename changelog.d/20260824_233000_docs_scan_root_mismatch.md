### Fixed

- **Diagnosed why a scan adds no new books:** the configured scan root
  (`/mnt/bigdata/books/audiobook-organizer`) is a *sibling* of where new books are
  dropped (`/mnt/bigdata/books/newbooks/audiobooks/`), so the walk can never reach
  them. Configuration mismatch, not a code defect — written up with the options and
  their trade-offs in `docs/diagnostics/`. Also records the real defect this exposed:
  a scan that finds nothing new completes and reports success, which is
  indistinguishable from a scan that is broken.
