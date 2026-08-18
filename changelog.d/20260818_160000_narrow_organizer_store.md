### Changed

- `organizer.Store` now declares the 16 methods the organizer package actually
  calls, grouped into five focused interfaces plus the in-package
  `OrganizerStore`, instead of embedding nine whole `database.*` interfaces. Its
  reachable surface drops from 179 distinct methods to 22.
