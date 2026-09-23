### Added

- `maintenance.split-joined-narrators`: finds narrator entries that hold a whole cast in one name ("Kate Reading, Michael Kramer"), relinks every book crediting one to the individual people in the same position, and deletes the joined entry once no book links it. "Surname, Given" names stay whole. Dry run by default; `apply=true` writes, `limit` caps books per run. Each rewrite and delete writes an undo-ledger row first, and the scan stand-down is held on apply.
