### Fixed

- The per-file transcription path no longer returns while its workers are still
  running. On the first job error it returned immediately, leaving the remaining
  workers sending HTTP requests to the endpoint and holding in-flight slots the
  caller believed it had released — so a dispatch that had already reported
  failure went on generating load. This was also the source of an intermittent
  data race that failed CI on unrelated pull requests.
