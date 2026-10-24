### Fixed

#### `maintenance.repair-junk-titles`: rank provider and stripped evidence first, filter announcer prose, gate fragments by duration

A review of the fixer's 2026-10-23 plan found 513 of 624 reviewed proposals wrong
(plus 39 unsure, 72 accepted, 48 of the accepted ones only if a duration gate
held). The transcription source carried most of the error: 261 rejected against
64 accepted. Eight fixes, all in the plan step (nothing is written without the
usual review and apply):

- Source priority is now the title without its prefix, the metadata candidate,
  the provider's recorded title, the transcription, the folder or filename, the
  series and number. The transcription used to come first. `title_prefix_stripped`
  behaves as before (514 of its rows were checked and correct).
- A transcription loses its series or genre tail before it is judged ("..., Book
  Two of X", "..., a crunchy LitRPG adventure"). A bare ", Book 9" is kept: it
  can be part of a real title.
- Evidence agreement (`junkEvidenceAgrees`) no longer lets a comma open a
  subtitle, drops series tails on both sides, and treats two different explicit
  book numbers as two books. `titleAgreesWithAny`, which the swapped
  title/author fixer reads, is unchanged.
- A transcription is refused when it is an announcer phrase ("an unabridged
  recording of", "X presents", "you're listening to"), carries a format word, or
  reads as a sentence (pronoun-led opening, a name and a past-tense verb, a
  trailing full stop, quotation marks).
- A folder or filename title is refused when it carries a chapter, track,
  credit or placeholder marker or ends with an author credit. A trailing
  "(read by X)" is stripped first, and a trailing " - Author" segment is dropped
  when it is the stored author, a placeholder or a name in the library.
- `series_and_number` needs a recorded series position equal to the number in
  the title, a real series name, and no disc, track or part word in the title.
- A proposal found anywhere but the stripped title is not made for a book that
  runs under 30 minutes (`Duration` unknown is not gated) or whose library state
  is `suspicious`; it is listed as a possible fragment.
- With a junk stored author, a provider title is accepted when it agrees with
  the path, the stripped title or the transcription, and otherwise the row is
  listed for a person instead of falling back to the folder. The duplicate-title
  guard keys on the provider's or the folder's author, or on the title alone when
  none is known.

Checked offline by replaying the plan's rows through the new string filters: all
64 accepted transcription rows and all 3 + 3 accepted filename and folder rows
pass unchanged, and 164 of the 452 rejected transcription, filename and folder
rows are now refused or cut to the reviewer's own better title (97 of 261, 30
of 134, 37 of 57). The rest depend on the evidence ranking and the duration
gate, which need the live library (provider titles, durations, library state)
and could not be replayed offline; the fragment class (Japanese lesson tracks
and similar, which only the duration gate catches) is not verified.
