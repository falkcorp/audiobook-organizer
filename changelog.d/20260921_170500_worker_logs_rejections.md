### Fixed

- A fingerprint worker now says in its own log when it turns a job down, and
  names both the folder the server asked for and the folders it actually has.
  Previously it reported refusals only back to the server, which recorded them
  at a detail level the server does not run at — so a worker could refuse tens
  of thousands of jobs in a row and neither end could say why. The message that
  was missing identified the problem in one line the first time it appeared.
