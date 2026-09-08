### Fixed

- **AudioBooth searches timed out, then returned nothing.** The ABS search
  endpoint parsed the `limit` query parameter nowhere: `limit=1`, `limit=10` and
  `limit=100` all returned a byte-identical document. Because a search hit
  carries the fully expanded library item, and this library's audiobooks run to
  40 files each, that document was enormous. Measured on production, q=`H`:
  **5.67 MB in 11.03 seconds**, of which 5,113,697 bytes (90%) was 25 expanded
  book items. AudioBooth issues a request per keystroke, so it hit that on the
  *first* character of a query and timed out. The retry then landed on the warm
  cache and returned instantly — but the client had already torn down its search
  state, which is why the failure looked like "timeout, then zero results". One
  bug, two symptoms.
- `limit` is now honoured, defaulting to 12 (matching Audiobookshelf) and clamped
  to 25. Asking for `limit=25` reproduces the previous behaviour exactly, so
  nothing that was reachable before is unreachable now.
- The `limit` is part of the result cache key. Without it, two clients asking for
  different sizes of the same query would share whichever document was built
  first, for the full two-minute TTL.
- **Author, narrator and genre hits are now ranked before they are truncated.**
  Those three lists were unbounded substring appends in index order — q=`H`
  returned 1,558 authors. Bounding them naively would have been worse than
  leaving them long: ABS search does not paginate, so there is no second page,
  and an exact match sitting late in the index would simply vanish. They now use
  the same exact / prefix / substring tiers the series ranker already used, so
  what falls off the end is the least relevant rather than the arbitrary.
