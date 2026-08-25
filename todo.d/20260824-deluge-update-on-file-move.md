## Update Deluge when the organizer moves a book's files

Prerequisite for migrating away from directory-normalized `Book.FilePath` (option B in
`docs/superpowers/specs/2026-08-24-per-file-scan-cache-design.md`).

When the organizer relocates a book's files, Deluge is not told. Any torrent still
seeding those files breaks, because Deluge keeps pointing at the old location. Today
this is masked for the in-root case (`ReOrganizeInPlace` is a true `os.Rename` within
the library) but it is a real hazard as soon as moves become the normal path.

- [ ] Decide the mechanism: Deluge `move_storage` per torrent vs. re-announce, and what
      happens when a torrent covers only some of a book's files
- [ ] Decide failure policy: does a Deluge update failure roll the move back, or is the
      move committed and the mismatch reported? (Compare the existing organize rollback,
      which `os.Rename`s the file back on a DB write failure.)
- [ ] Wire it into the organize path, not just the manual move endpoint
- [ ] Only then schedule option B

Related: the version-linking issue below — organize already knows both the old and new
path at move time, but does not record the relationship; a later scan rediscovers the
original file and version-links it after the fact.
