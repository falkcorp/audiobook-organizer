### Fixed

- Applying books from the metadata review lane no longer refuses every book
  as `transcription_mismatch`. The certainty gate required the exact
  transcribed title and the noisy Whisper author as a substring of the clean
  candidate author, so credits ("Translated from the Polish") and typos
  ("R.A. Salvator") failed all 11 books the owner applied on 2026-09-13. The
  owner's single-row Apply now goes through (next entry), and one shared
  matcher (`util.TitleAgrees` / `util.AuthorAgrees`) replaces three
  diverging copies. Every path that applies with nobody reviewing (the gate,
  so metadata.upgrade and an unpinned batch apply; auto-fetch; the apply's
  `audio_confirmed` marker) ANDs it with its own rule from before this
  change, so none accepts a pair it refused before, and a confirmation never
  lowers the score floor to 0.85 where it did not before. The matcher alone
  only annotates an owner-reviewed row apply
  (`transcription_agrees_on_review`). Its rules:
  - titles must be equal as whole word sequences (punctuation and
    "unabridged" ignored, number words read as digits); there is no
    containment, so "Mistborn" does not match "Mistborn: The Hero of Ages";
  - the transcribed side may drop a trailing series/volume phrase ("book one
    of the X trilogy", ", book N", "a short story prequel to X"), but a
    volume number it names must equal the candidate's series position, and a
    candidate with no position does not agree ("Dune" is not "Dune, book two
    of the Dune Chronicles");
  - one misheard word is forgiven only in titles of 3+ words: a silent first
    letter ("Naves" for "Knaves") or one inner edit in a 7+ letter word with
    the same first and last letter ("The Witches" never matches "The
    Witcher"); conflicting numbers never match;
  - the author's surname must appear in the transcribed AUTHOR field (never
    the intro), with one typo only in surnames of 6+ letters, and when both
    sides give a first name or initial it must agree (Stephen King does not
    match "Owen King").
- Clicking Apply on a single review row is now treated as the owner's review.
  That row's request pins the candidate it showed (origin `row`, plus the
  `candidate_hash` the review list now serves per row); the server applies
  the book when the pin still matches the top cached candidate, even if the
  certainty gate's score, transcription, sequence or evidence legs refuse. The
  override is recorded on every field's change history, as an
  `owner_reviewed <time>: <reasons>` version note added on every override,
  and at Info on the op log with the full verdict. A pin that no longer
  matches is refused as `stale_candidate`. Bulk buttons (Apply page, Apply
  high confidence, group Apply All, Apply selected) send no pins and stay
  fully gated, as do the dry run, auto-upgrade and the op-results batch
  apply; a queued run that absorbs a later unpinned request for a book drops
  that book's pin. `identity_stale`, `partial_book`, `asin_conflict`, the
  rename preflight, `policy:no-metadata` and field locks still block a
  reviewed apply. The dry run reports `owner_reviewed_would_apply` per book.
- An owner-reviewed apply now fails with "change history not recorded" when
  the primary history store cannot record its change history. This is
  intentional and is not a gate refusal. History is written after the
  commit, so the write itself stands and the file work still runs; the op
  logs the error at Error and counts the book under "owner-reviewed change
  history not recorded", and undo refuses that apply.
