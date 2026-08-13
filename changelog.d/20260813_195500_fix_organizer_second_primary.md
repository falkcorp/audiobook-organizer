### Fixed

- **Organizing a book into a version group that already had a primary elected a
  second one.** This happens routinely — the scanner hash-matches a newly
  downloaded copy of a book you already own into the existing book's version
  group, and organize then runs against the new row. `CreateOrganizedVersion`
  marked every organized copy primary but demoted only its own source row, so
  the group's existing primary survived alongside the new one. Production held
  10,780 groups with surplus primaries. The newly organized copy now yields to
  the incumbent, which is the copy that has already been through metadata
  enrichment and sits under a real author directory.
