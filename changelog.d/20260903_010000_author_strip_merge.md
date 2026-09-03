### Added

- New maintenance operation `maintenance.author-strip-merge` repairs author rows
  built out of chapter-file numbering. It strips the numbering and, when the
  residue names an existing author, merges the row into it
  (`001-147 Kevin J Anderson` → `Kevin J Anderson`); rows carrying no usable
  name are deleted. Rows whose residue matches no existing author are left alone
  rather than renamed. Report-only by default — pass `apply=true` to write.
  Measured on the live library: 1,610 deletable, 79 mergeable, 812 left alone.
