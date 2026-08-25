## Auto-organize throws away a relationship it already has, then a later scan rediscovers it

`AutoOrganizeFn` (`internal/server/server.go:921`) holds BOTH `oldPath` and `newPath` at
the moment it organizes a book. It updates the row's `FilePath` to `newPath` and records
nothing about `oldPath`.

For a book outside `RootDir`, `OrganizeOneBook` routes to `Organizer.OrganizeBook`, which
uses `organizeFile` — strategy `auto` = reflink -> hardlink -> copy
(`internal/organizer/organizer.go:919-940`). **None of those remove the source.** So two
directory entries now exist with identical content, the DB points at the organized one,
and the original is untracked.

If the original's directory is still in the scan paths, the next scan walks it, hashes it,
matches it in `saveBookToDatabase`'s hash dedup, and version-links the two **after the
fact** — creating the version group and `IsPrimaryVersion` stamp that organize could have
written directly, at move time, with no hashing and no rediscovery.

That is scan-then-dedup-later for a relationship that was known at import.

- [ ] Have the organize path record the old->new relationship when it creates the second
      copy (version-link, or mark the source as superseded), instead of leaving it to be
      rediscovered
- [ ] Decide whether reflink/hardlink cases should be version-linked at all — they share
      extents/inode, so they are one set of bytes with two names, not two copies
- [ ] Confirm on prod whether original import locations remain in the scan paths. If they
      do not, this never fires and the priority drops; MEASURE before acting
- [ ] Re-check the single-member version group finding with `RootDir` SET. It was measured
      with `RootDir=""`, which is not production, and the primary/non-primary branch in
      `saveBookToDatabase` is gated on `RootDir` prefixes

Related: `docs/plans/2026-08-24-per-file-scan-cache-design.md` (option B), and
`20260824-deluge-update-on-file-move.md`.
