### Fixed

- **Adding a second fingerprint root killed the workers.** Calibration candidates
  were still chosen from `libroot` only, and `workerclient`'s parity gate
  "requires at least one per configured root: without one, neither the root
  mapping nor the pipeline parity can be proven". So a worker started with
  `--root books=…` — required for the server to hand out work for the majority
  of the library — exited at startup with *"the server offered no calibration
  file under root books"*. Candidates are now collected per root, capped per
  root, so every root a worker may hold can be proven. This was the fourth and
  last site of one restriction: eligibility, the job's root name, the remote
  flag, and now calibration.
