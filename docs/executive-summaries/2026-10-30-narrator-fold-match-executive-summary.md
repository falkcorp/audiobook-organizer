<!-- file: docs/executive-summaries/2026-10-30-narrator-fold-match-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0611bce5-25cd-4ec9-aeaa-f41c3603b024 -->
<!-- last-edited: 2026-10-30 -->

# Rescans no longer create a second copy of a narrator's name

## Executive Summary

When the app re-reads a book's file tags, it matches each narrator name to the
narrators it already knows. Until now it only matched names spelled exactly the
same, ignoring capital letters and extra spaces. A narrator we keep as
"Renée Montoya" and a tag that spells her "Renee Montoya" (no accent), or
"Jean-Paul Valley" against "Jean Paul Valley", were treated as two different
people. The app quietly created a second narrator record and moved the book's
credit to it, which split one narrator's books across two entries and undid the
cleanup that had joined them. Nothing warned anyone when it happened.

## What changed

- **Spelling differences no longer make a new narrator.** Accents, hyphens,
  punctuation, capital letters and spacing are ignored when matching a name to
  an existing narrator. The existing record is used and keeps its spelling.
- **A new narrator is created only when nothing matches.**
- **If several existing records already match, the book is linked to the exact
  spelling when there is one, otherwise to the one with the most books,** and the
  log names every candidate so the duplicates can be merged.
- **Two spellings arriving at the same moment create one record, not two.**
- **The app keeps a small lookup list of these folded names** so the match is
  fast. It is built when the app starts, and until it is finished the app
  checks the narrator records directly, which is slower but gives the same
  answer.

## What did not change

Narrators added by hand, by the import tools or by the cleanup jobs still match
only on the exact spelling, because those mean exactly what they say. Authors
have the same weakness and are not fixed here; that is tracked separately.
