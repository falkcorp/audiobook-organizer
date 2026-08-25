### Fixed

- The CodeQL alert triage helper fetched only the first page of alerts. `issue_manager`
  requested `per_page=100` with no page walk and returned that page, so with 327 open
  alerts in this repo it reported "Found 100 open CodeQL alerts" and filed issues for
  100, silently dropping 227. A truncated list still produces a plausible number, which
  is why it went unnoticed. It now follows pagination, and a mid-walk network error
  returns the alerts already collected rather than an empty list that reads like a
  clean scan.
