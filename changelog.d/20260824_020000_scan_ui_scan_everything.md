### Added

- **A "Scan Everything" option next to Full Rescan.** The Library toolbar's
  Full Rescan button now has a dropdown with a second option that includes the
  organized library root in the scan while still skipping files that haven't
  changed — reaching the backend's `include_root_dir` capability, which was
  previously only available via the API directly. Full Rescan itself is
  unchanged: it still re-hashes everything.
