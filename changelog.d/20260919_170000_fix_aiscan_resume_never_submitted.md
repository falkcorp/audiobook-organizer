### Fixed

- **A restarted batch AI author scan no longer stalls or fails on a phase that
  never submitted anything.** On resume, every still-pending batch source phase
  was treated as if the server had died inside the OpenAI submit call: it was
  looked up by metadata, and on a confirmed absence the resume waited out the
  one-hour submit grace before submitting — or, on a client that cannot look
  batches up, failed the whole scan with "batch phase was mid-submit at
  restart". That included a groups phase with no duplicate groups and nothing to
  submit. Source phases of new scans now carry a marker saying every submit
  attempt is recorded before it is made, so a pending phase with the marker and
  no recorded attempt is launched at once. Phases from older scans keep the
  lookup, because the build that made them did not record attempts first.
- The restart tests stopped letting the "dead" first manager keep running
  groups work alongside the resumed one, which made
  `TestBatchScanCollectedOnceAcrossRestart` flake (measured locally: 19 failures
  in 600 runs under `-race -cpu 1,2,8`, 0 in 600 after).
