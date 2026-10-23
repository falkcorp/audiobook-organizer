- [ ] **TODO-RETIRED-FPIDX-SHELL-ID** after `RetireBookFiles` re-keys a row
      under the holder, its `fpidx:` LSH value (the near-duplicate fingerprint
      index) still names the shell book id. A fingerprint hit on a retired row
      therefore reports the shell, not the holder, until the index is rebuilt.
      Rewrite the `fpidx:` value in the retire and un-retire batch (or have the
      LSH readers resolve the owner through the file id), and add a test that a
      retire followed by an `LSHProbe` hit resolves to the holder. Callers of
      `LookupAcoustIDCandidates` and `LSHProbe` must skip a retired row until
      then (follow-up PR 3b, dedup collectors).
