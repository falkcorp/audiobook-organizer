### Fixed

- **A maintenance op scoped to `/lib` also swept `/lib2`.** Five ops that take a
  PathPrefix param — mark-missing-files, missing-file-repoint,
  missing-file-repair, missing-file-audit and merge-same-path-dupes — filtered
  rows with a plain string prefix, so a sweep scoped to one folder silently
  included every sibling folder whose name started the same way. Several of
  these ops write when applied, so the wrong scope was more than a wrong count.
  They now share one filter that matches on a folder boundary
  (`pathutil.IsWithin`): the prefix itself and paths under it match, a trailing
  separator on the prefix is accepted, and an empty prefix still means no filter.
  A prefix that ends partway through a name (e.g. `/lib/Author/Book - Part`)
  no longer matches `Part01…`; only whole folders match now.
