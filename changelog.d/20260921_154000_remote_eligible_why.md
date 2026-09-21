### Fixed

- A fingerprint run that cannot hand any file to a worker now says why. It used
  to report only "0 of 13167 files resolved" while every worker was told there
  was no work, which shows that a run is stuck but gives no clue what to fix.
  The progress line now names the reasons and how many files hit each one — no
  duration recorded, not under a known folder, or no window plan.
