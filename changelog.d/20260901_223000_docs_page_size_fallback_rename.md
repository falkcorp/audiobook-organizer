### Fixed

- **A metadata-review constant was named for a policy the code does not
  implement.** `MAX_REVIEW_PAGE_SIZE`, documented as "largest size a stored
  preference may restore", is not a ceiling on offered sizes: a stored 100-row
  page size restores as 100, because the loader returns any offered option
  before it reaches that constant. What the value actually does is correct an
  *unrecognised* stored size — downward (250 becomes 50) and upward (30 becomes
  50). Renamed to `PAGE_SIZE_FALLBACK` and documented as what it does. The one
  test covering this asserted on 50, the single value where both readings give
  the same answer; a stored 100 is now covered too.
