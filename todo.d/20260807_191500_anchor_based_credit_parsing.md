<!-- file: todo.d/20260807_191500_anchor_based_credit_parsing.md -->
<!-- version: 1.0.0 -->
<!-- guid: b57d3e19-84c2-4f60-a1d8-6e93c07f2b45 -->
<!-- last-edited: 2026-08-07 -->

- [ ] **Re-cut credit parsing on ROLE ANCHORS instead of bare "by", and add a
      `translator` field.** One change; it fixes three defects at once.

      Measured on prod 2026-08-07 (318 transcripts): **12 (3.8%) mention a
      translator**, and **5 of 6** genuine translated works have a corrupted
      author — `'Kugane Maruyama Translated by Emily Balistreri'`,
      `'Oleg Safire and Yuri Vinokurov. Translated'`. Narrator is never
      corrupted, because it is already anchored on `Narrated by`/`Read by`.
      That asymmetry is the whole diagnosis: author is "text after `by` until a
      known boundary", and `Translated by` is not in the boundary set.

      Anchors (match the FULL phrase, never bare `by`):

        written by | by                        -> author
        translated by                          -> translator   (NEW field)
        narrated by | read by | performed by   -> narrator
        cover art by | foreword by |
          introduction by | preface by         -> DISCARD

      Each role spans from its anchor to the NEXT anchor of any kind, so credit
      ORDER stops mattering and an unrecognised future role terminates the
      previous field instead of being swallowed by it.

      🔴 The same change fixes the 44% title welding
      (`todo.d/20260807_190000_title_credit_verb_welding.md`): the title is cut
      before `written by` rather than before `by`, so `written` is never
      stranded on the title. Do NOT implement these as two separate fixes — it
      would mean writing the boundary logic twice.

      Verified against a real line: `'Yen Audio presents Overlord Vol. 10 ...
      Written by Kugane Maruyama Translated by Emily Balistreri Cover art by
      SoBin Read by Chris Guerrero'` parses fully correctly under this rule.

      Storage: `transcribed_translator` on BOTH `Book` and `BookFile` (tier-0
      copies parsed fields down to the file rows). Do not encode ordering in the
      model — roles are named; display order is a UI concern.

- [ ] **Reject parses from clips with no announcement at all.** 2 of the same 12
      samples produced fields from pure narrative prose — one `transcribed_title`
      was ~1,000 characters of dialogue, author `'the Jung. We only wished'`.
      `internal/transcribe/parse.go:70` claims an announcement is required; the
      guard leaks. Add a hard sanity bound (max title length, prose markers such
      as quotation density) and emit NOTHING rather than garbage. Same code path
      as the anchor work, so do it in the same PR.
