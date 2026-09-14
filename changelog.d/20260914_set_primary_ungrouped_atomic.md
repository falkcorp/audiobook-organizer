### Fixed

- Setting the primary version of a book with no version group now re-checks the
  book inside its write. A book grouped since it was read goes through the group
  path, which demotes the group's other primaries (nil flags count as primary),
  and a book deleted since it was read is refused. Before this fix a concurrent
  link could leave a group with two primaries, or flag a deleted book as primary.
