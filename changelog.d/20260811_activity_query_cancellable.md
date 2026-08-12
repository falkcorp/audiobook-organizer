### Fixed

- **Activity log queries now stop when you close the page.** Loading the activity
  view used to start a scan that kept running even after you navigated away or
  closed the tab. Enough abandoned scans piled up that the server ran out of
  memory and had to be restarted — five times in one night. Requests now stop as
  soon as the browser disconnects, so the memory is released immediately instead
  of being held until a restart.

### Changed

- The activity log's `Query` and `GetDistinctSources` now require a cancellable
  context; the context-free versions were removed rather than deprecated, so a
  future caller cannot silently reintroduce the unstoppable-scan bug.
