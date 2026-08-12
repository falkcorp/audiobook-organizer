### Fixed

- The Coverage Floor CI job no longer gets killed before it can say what went
  wrong. Its 20-minute cap was shorter than the 25-minute timeout the tests
  themselves use, so a hung test was always killed by the runner first — losing
  the goroutine dump that names the stuck test, and reporting a bare "cancelled"
  that looks like a failure on the pull request. The cap is now 35 minutes, so
  the test timeout fires first and prints a diagnosis.
