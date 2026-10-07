### Fixed

#### Search Metadata dialog: picks are staged and applied once, in the background, when the window closes

Apply in the single-book Search Metadata dialog (Review → Metadata, book
detail) disabled every button for the whole apply request, which first waits up
to 60s for the library scan to release the book, then runs the rename preflight
and the database write; the dialog then closed. During a scan the dialog sat
greyed out and nothing else could be picked.

Picking a result (or some of its fields) now only stages it. Nothing is
disabled, a new pick replaces the staged one, and a banner shows what is staged
and how many fields, with a Discard button. Closing the dialog (the footer
button, Escape or a backdrop click) sends the staged pick as ONE apply with
`background: true`; "Discard & close" sends nothing. The dialog closes at once.
A toast follows the apply to the end: success with Undo, or the failure.

`POST /api/v1/audiobooks/:id/apply-metadata` accepts `background: true`: after
the synchronous ASIN-conflict check (still a 409), the apply is stamped (edit
mark + batch id) and handed to the existing durable `metadata.apply-when-scanned`
operation, answering 202 with its `operation_id`. Without a queuer wired it
applies inline as before. Field ticks now belong to one candidate (they were one
set shared by every card), and the Review lane no longer closes the search
dialog when an apply finishes, so a late result cannot close the next book's
search.
