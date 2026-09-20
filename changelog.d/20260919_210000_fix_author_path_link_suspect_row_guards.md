### Fixed

- **`maintenance.author-path-link` no longer links books onto author rows that
  are titles, franchises or placeholders.** The first production dry run found
  that the op's own safety bars failed on the exact row the code cites as the
  reason they exist. The thin-row bar read "live books whose *scalar* author id
  names this row" while the author page reports the row's own book count, and the
  two are different numbers: row 43771 `Freedom's Dawn` reads 110 on the first
  and 0 on the second, so the bar saw a fat, established author and would have
  attached 110 more books to a title. Across the run 8 target rows diverged that
  way and 120 books moved from held to written. The bar now holds a row when
  *either* count is thin, and both numbers appear in the dry run so the
  divergence is visible rather than inferred. Two more bars were added alongside
  it: `authorname.IsPlaceholder` is now run on the resolved target row and not
  only on the name derived from the path (a segment spelled `Unknown  Author`
  with a double space is not the placeholder to that check but normalizes
  straight onto the placeholder row), and a target row whose name reads as a
  title, a franchise or a collective — a possessive (`Freedom's Dawn`), a leading
  article (`The Messenger`), three or more all-caps words (`STAR TREK POWER
  KLINGON`) or a collective word (`Various Authors`) — is held with its own
  outcome. Nothing was applied to production; this is the dry run's finding, fixed
  before the first apply.
