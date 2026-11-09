// file: internal/books/store/legacycap.go
// version: 1.0.1
// guid: 0b6d2f84-7c19-4e53-a9a1-5e8f3c7d1b96
// last-edited: 2026-11-09

package store

// LegacyCap is the highest HEAD-style books migration: the last step of the
// legacy track, fixed when R1's storage flip merges (05-storage §5S.3.5,
// DATA4-19). Books' steps 1..LegacyCap keep HEAD's form (idempotent, not
// atomic, recorded in HEAD's preference rows) and run first, on books' own
// claims; every later books step is a storage.Migration numbered from
// LegacyCap+1. TestNoNewLegacyMigrations is the ratchet. R3 deletes the
// legacy track behind books' upgrade floor.
const LegacyCap = 66
