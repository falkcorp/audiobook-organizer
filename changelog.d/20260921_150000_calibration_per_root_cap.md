### Fixed

- Fingerprint workers no longer refuse to start when the library spans more than
  one root. The server picked its calibration files with a cap of three across
  *all* roots, so whichever root came first used up the entire budget and the
  others were offered nothing — and a worker will not start unless every root it
  was given can be proven. In production this stopped all fingerprinting for
  about six hours, with roughly three quarters of the library sitting under the
  root that was being skipped. The cap is now three files *per root*, and the
  server logs how many candidates each root has so a gap is visible immediately
  instead of only showing up as a worker exiting.
