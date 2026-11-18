- [ ] **TITLE-NEUTRAL** Owner decision: may a NEUTRAL title apply on a runtime
      within 5% alone? The apply gate's title check answers AGREE, DISAGREE or
      NEUTRAL; a neutral title (one side says more than the other, nothing
      conflicts) confirms nothing, and `EvidenceVerdict.tally` applies it only
      when both runtimes are known and within 5%. In the 4000-row replay of the
      title-rule PR (#4692), 49 of the 61 applies that depend on a neutral
      title depend on that runtime alone. The PR did not change the policy; it
      pins today's outcome in `TestUnderOwnerReview_*`
      (`internal/applygate/title_owner_review_pins_test.go`) for a franchise
      word added in front of the stored title, a stored subtitle that names the
      book against a franchise-only candidate (B1b) and a stored
      franchise-and-book title against the franchise (M1a). For each of those
      shapes, decide: apply on the runtime alone, need a second witness, or
      refuse. A change is one deliberate edit to `tally` or `checkTitle` and to
      its pin, and moves up to 49 replay applies.

      A separate question, M2a, is pinned in the same file: a blank stored
      title with nothing to compare (only a file name and a series that share no
      word with the candidate) has an UNKNOWN title leg, and applies at every
      runtime, with no runtime at all included. No runtime carries that apply.
      Decide: keep it, or require an agreeing runtime for it as well.
