- [ ] **ITUNES-WB-STAGING** The iTunes write-back batch is repeatedly REJECTED
      by `ITLSafetyContract`: a track location contains the staging marker
      `.itunes-writeback/`, and the contract refuses to write any location
      under that marker. The batch retries and is refused again on every
      pass. Find which writer puts a staging path into a track's location
      (a location recorded before the staged file was moved into place is the
      likely cause). Fix it so a staging path never reaches the iTunes
      location, and so one bad track does not reject the whole batch forever.
      Done means a regression test plus a clean write-back pass.
