### Fixed

- The organizer's database store was wired in exactly ONE of its five entry
  points (auto-organize), so the AuthorID/SeriesID fallback in path templates
  was dead code everywhere else: any book whose Author struct was not
  populated organized into "Unknown Author/" with its AuthorID sitting right
  there — the mechanism behind the 2026-08-11 mass-reorganize. The store is
  now a narrow OrganizerStore interface wired by the bulk service, preview,
  and rename paths too. Also updates two organize-filter tests whose
  authorless fixtures now (correctly) trip the #2457 author gate.
