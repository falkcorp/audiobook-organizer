### Changed

- **Library scans spend far less time waiting on AI filename parsing.** When a
  book's tags don't yield a usable title, the scan asks a language model to
  parse it from the filename. Those requests were issued strictly one at a time,
  once per 500-book chunk — on a 40,000-book library that was the single largest
  contributor to scan wall-clock, ahead of reading the files themselves. They
  now run a few at a time against the same backend, with the existing per-batch
  pacing preserved so the request rate stays modest. Scans that abort AI parsing
  because the backend is down still do so, and now stop sooner.
