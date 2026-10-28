<!-- file: docs/executive-summaries/2026-10-26-credits-backfill-executive-summary.md -->
<!-- version: 1.2.4 -->
<!-- guid: dba78e10-ba92-4a13-9a4d-25ba3871235a -->
<!-- last-edited: 2026-10-28 -->

# A repair job that makes every book's author and narrator lists agree with the book

- Plan: step 3 of the approved author and narrator credit-lists plan (approved 2026-10-04, confirmed again 2026-10-26)
- Status: built and tested, not yet run on the production library. The owner runs the dry run first, reads it, then decides on the apply.

## Executive Summary

- **The problem.** Each book records its authors and narrators in two places: one
  "main author" and "narrator" field on the book, and ordered lists of credited people
  kept beside it. Older versions of the program did not always keep the two in step.
  A count of the production library on 2026-10-26 (69,504 books) found about 25,800
  books with a main author but an empty author list, about 28,300 with a narrator but
  an empty narrator list, and several thousand more where the list leaves out the main
  author or numbers its entries wrong.
- **Why it matters.** Audiobookshelf, search and the later steps of the plan read the
  lists. A book with an empty list shows no narrator in some apps, and the next step of
  the plan derives the main fields from the lists. If that happened first, an empty
  list could wipe the main author.
- **What the job does.** It fills an empty author list from the book's main author,
  puts the main author first when the list leaves them out, numbers each list 0, 1, 2,
  and so on, and fills an empty narrator list from the narrator field (cleaned the same
  way the program already cleans it) or from the stored narrator list. It never
  creates a new author. It only creates a new narrator for a single name. A name that
  exists only after splitting a longer string needs the owner's opt-in. It also never
  writes a placeholder such as "Unknown Author" as an author, and never makes a person
  of a junk narrator such as "N/A" or "Various".
- **What it leaves alone.** Cases with more than one possible right answer are listed
  for a person to decide, and nothing is changed for them: a credit pointing at a
  deleted or merged author, a combined "A, B" author record, a narrator field that
  names different people than the list, a self-read book, and similar. Books in the
  iTunes library and owner-managed series (Doctor Who, Big Finish, Torchwood) are held
  unless the owner opts them in. A book whose author a person has locked is held
  whole on the author side: the job writes nothing to its author list or author
  fields. A book whose narrator a person has locked is held on the narrator side. A
  book where a later rescan moved the author and the old main author is the stale half
  is listed for review instead of being put first.
- **Spelling fixes for narrators.** When the job links a narrator name to an existing
  person who is spelled a little differently (for example "Renee Montoya" and the
  record "Renée Montoya", or "Jean Paul Valley" and "Jean-Paul Valley"), it also
  rewrites that name in the book's narrator text and in the book's stored narrator list
  to the record's spelling. Only the credit that is the name is changed: a "Read by"
  prefix, a role such as "- translator" and a co-narrating author stay as they were.
  Both rewrites are recorded and can be undone. A book whose name cannot be respelled
  safely is not written on the narrator side and is held for review: the spelling
  cannot be reconciled, the stored list and the text conflict, or the name matches two
  people. The author side of such a book is still written.
- **A name that exists only inside a longer credit.** When a book's narrator text
  lists several people and one of them is new to the library, the job does not create
  that person on its own. If another book names the same person alone, that book
  creates the record and the longer credit links to it, and this holds in every
  step: the preview, a single full run, and a run split into batches. In a batch that
  leaves the other book out, the longer credit simply waits (the report lists it as
  waiting and it does not use up the batch size), and a later batch links it. If the
  other book cannot be written, because its spelling cannot be settled or its write
  fails, the longer credit is not written either and goes back to review. When the
  longer credit's own spelling of the name is what would have kept the other book from
  being written, the name is settled by the other book's spelling and both are written
  in the same run, so a second run finds nothing left to do. A record the job created
  that no written book ends up using is removed again before the run ends (only when the
  job made it and nothing links it) and is listed by name, with what was removed and what
  was kept.
  The same holds when a run is stopped (a crash, or the scan pause lost) and started
  again: before every new person it creates, the job writes a note of what it is about
  to create, and a restarted run reads its own notes, recognises a record an earlier
  attempt made that nothing uses, and treats it as new instead of as a record that was
  always there, so a longer credit is never linked to a record no write of the job ever
  vouched for. Undo reports those notes (and the removal notes) as records only: they
  cannot be reversed, and the books it wrote are still undone.
- **Review lists are kept.** Every run, a limited one too, stores the full list of book
  ids behind each count so each count can be clicked through. For a full library that
  is roughly 5 to 12 MB per run, and nothing removes it by age: it stays until the
  operation is discarded.
- **Previewed first, and reversible.** With no options the job only reports what it
  would do. It lists every group of books with ids and shows before and after examples.
  It also lists every book that would be filed under a different author folder the
  next time the library is organized, because putting the main author first can change
  that folder. Every book it writes is recorded first, and the whole run can be undone.
  Undoing a book that had its list order changed puts the authors back in the order
  they were stored in, so the book files under the same author folder as before.
  The undo refuses any book a person edited after the run, so a later edit is never
  overwritten.
- **What the follow-up count will show.** After the job runs, the library count for
  each fixed problem drops to the books the job held or left for review (iTunes,
  owner-managed, locked, split-name narrators, and anything past a batch limit). The
  job's own report lists each of those books, so the count can be checked against it.
- **Verified.** Tests build a small library with one book for each problem the job
  fixes, run it, and re-run the library count. Each of those problem counts is zero
  afterwards, and a second test confirms that held and review books are the only
  ones left when they are present. The tests also check that the preview writes nothing, that undo restores
  each book, that a run cut short and restarted stays within its batch limit and
  does not count a write that never landed, that eight
  parallel workers give the same result as one, and that a split name never creates a
  person without the opt-in.

## The credit-list repair job

- **What it was.** The book's main author and narrator fields and its ordered credit
  lists disagreed on tens of thousands of books, because older import and edit paths
  wrote one and not the other.
- **Why it mattered.** Apps that read the lists showed missing or wrong credits. The
  next planned change makes the lists the only source of truth, and would have turned
  an empty list into a lost author.
- **The fix.** A new maintenance job compares each book's fields and lists, fixes the
  cases that have one safe answer, lists the rest for review, and records every write
  so it can be undone. Each write also re-saves the book's record and queues it for
  search re-indexing. At the full library's numbers that could be up to about 55,000
  books, so the owner can run it in batches with a limit. The limit means the first
  N books that have something to fix, in a fixed order, and the preview with the same
  limit shows exactly those books.
- **A cost to read before applying.** For some books, changing the author list
  changes which author the book is searched under. Those books will look up their
  metadata again at the next metadata run, which spends the Google Books daily
  allowance (1,000 lookups a day). The preview now reports how many books that is and
  lists them, so the owner can pick a batch size that fits the allowance.
