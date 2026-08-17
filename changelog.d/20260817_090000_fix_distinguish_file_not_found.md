### Fixed

- **"File not found" download failures are now diagnosable.** Five different
  conditions in the file-serving path all returned an identical 404 with an
  identical body and **none of them logged anything**, so a report of "it says it
  can't find the file" could not be traced to a cause without probing production
  directly. The client-visible answer is unchanged — it is the protocol contract —
  but the server now records which of the five it was: `no_ino`, `no_syncfile`,
  `no_bookfile_row`, `abs_path_failed`, or `bytes_missing`, along with the path it
  tried to serve.

  Diagnosing the live reports took four separate production probes precisely
  because 1,036 of these 404s had been recorded without recording their kind.
