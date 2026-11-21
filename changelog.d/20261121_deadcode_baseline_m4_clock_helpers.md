### Fixed

- The deadcode ratchet on main is green again: the two M4 clock helpers added by the scan-full and db_optimize clock fix (`m4.clockGroup`, `m4.splitKinds`) are baselined with the rest of the M4 migration, which has no caller until the flip.
