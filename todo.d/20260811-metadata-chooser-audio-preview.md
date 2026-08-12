- [ ] **REVIEW-PREVIEW** Play the first ~2 minutes of audio directly from the
      metadata chooser. Requested 2026-08-11: *"I need a way to play the first 2
      minutes of audio right from the metadata chooser."*

      **Why it matters more than it sounds.** The chooser asks the owner to
      confirm a candidate match, but everything on screen is second-hand — a
      title string, a cover, a narrator name. The only ground truth for "is this
      actually the right book and the right narration" is the audio itself, and
      today confirming means leaving the UI entirely. For a library with known
      title contamination and mis-grouped multi-part sets, a 2-minute listen is
      the cheapest possible verification.

      Notes before designing:

      - A range-request audio endpoint may already exist for the player; check
        before adding a second one. If one exists, the work is UI-only.
      - The chooser row shows a **single file** even for a 40-part book (see
        REVIEW-MULTIFILE-CLARITY below), so "the first 2 minutes" must mean the
        first 2 minutes of **part 1 of the book**, not of whichever file happens
        to be attached to the row. Getting this wrong makes the preview
        actively misleading.
      - Do not stream the whole file. Bound the response; an unbounded
        request-scoped read on this server is exactly the shape that OOM-killed
        production on 2026-08-11.

      Documentation check done 2026-08-11: nothing designed. One backlog entry
      in `docs/archive/backlog-2026-04-10.md` calls it "nice but" and it was
      never specced. Not covered by the review-queue plan.
