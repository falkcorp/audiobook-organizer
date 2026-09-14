### Fixed

- Applying books from the metadata review lane no longer refuses every book
  as `transcription_mismatch`. The certainty gate required the exact
  transcribed title and the noisy Whisper author as a substring of the clean
  candidate author, so credits ("Translated from the Polish") and typos
  ("R.A. Salvator") failed all 11 books the owner applied on 2026-09-13. The
  gate, auto-fetch's transcribed-title check and the apply's `audio_confirmed`
  marker now share one matcher (`util.TitleAgrees` / `util.AuthorAgrees`):
  punctuation-free token sequences, equality or word-boundary containment with
  a strength guard, one typo allowed in 5+ letter tokens, conflicting numbers
  never match, and the author passes on any candidate author's surname
  (also found in the first 500 characters of the intro transcript).
- Clicking Apply in the review lane is now treated as the owner's review. The
  lane sends a pin of the candidate each row showed; the server applies the
  book when the pin still matches the top cached candidate, even if the
  certainty gate's score, transcription, sequence or evidence legs refuse, and
  records the override on every field's change history, as an
  `owner_reviewed` version note and at Info on the op log with the full gate
  verdict. A pin that no longer matches is refused as `stale_candidate`.
  Requests without pins, the dry run, auto-upgrade and the op-results batch
  apply are still hard-gated; `identity_stale`, `partial_book`, the rename
  preflight, `policy:no-metadata` and field locks still block a reviewed
  apply. The dry run reports `owner_reviewed_would_apply` per book.
