### Added

- The retired-versions repair now has its own set of reject reasons: the shell is a distinct
  edition, keep both books, or the wrong book holds these files (on top of the common and repair
  reasons). Tests pin how a reject of a retired shell behaves in the decision store: it blocks the
  fixer, a reopen does not release it (a reject with no data hash cannot be reopened at all), only
  an unreject does, and the "undone" record a revert leaves never blocks or releases anything. The
  reject route itself is not built yet, so nothing user-visible changes.
