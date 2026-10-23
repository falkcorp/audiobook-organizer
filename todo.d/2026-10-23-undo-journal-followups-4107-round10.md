- [ ] **TODO-UNDO-JOURNAL-4107** undo journal follow-ups from #4107 round 10 —
      four non-blocking items from the round-10 review of 7686b55c7 (the review
      labelled them N1, N2, N4 and N6), filed here instead of fixed in that PR.
      Each is a separate small change in the undo journal or its writers; pick
      the text of each from that review, which is the source of record:
      - [ ] N1
      - [ ] N2
      - [ ] N4
      - [ ] N6
      The three nits of the same review that were fixed in #4107 (not part of
      this task): N3 (`undo.IgnoredMarkers` replaced by the graph's `Ignored`),
      N5 (a caller-supplied `RevertedSeq` below the re-stamped `CreatedSeq` is
      raised on rewrite), N7 (a malformed clock key is rewritten even when no
      row decodes).
