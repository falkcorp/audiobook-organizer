### Fixed

#### A book that failed to apply no longer disappears from the review queue for good

When you apply metadata to a batch of books, the rows vanish from the queue
immediately — the apply itself runs in the background, and hiding them straight
away is what makes the queue usable. If the background job then failed on some
of those books, they were supposed to come back so you could deal with them.
They never did.

The code that reloads the queue from the server could only *add* row states, not
change ones already set. Applying had already marked every book in the batch as
applied locally, so the reload had nothing left to correct — the server's answer
was quietly discarded for exactly the books it mattered for. A book the apply
failed on stayed hidden behind the default "Hide applied" filter with no way
back, and nothing told you. The queue just looked finished.

This was worse for bulk applies than single ones, because a single click could
mark hundreds of books at once.

The reload now lets the server correct the local state once the background job
has actually finished, so a book that did not get applied returns to the queue.
Books still being worked on keep their applied state until their job ends, so
rows do not flicker back to pending while the server is still working on them.

Nothing about the immediate hide-on-apply behaviour changes.

#### Applying metadata from the Search dialog no longer looks like it did nothing

The Search button on a "no match" row lets you find and apply a candidate by
hand — it is the only way to fix a book the automatic matching gave up on. The
apply worked, but the queue then re-read the row and threw the answer away, so
the book vanished under the default "Hide rejected" filter with nothing to show
it had succeeded. The same thing happened when a re-fetch found a candidate for
a row that had previously come back with no match: the row stayed marked
rejected even though it no longer was.

The queue now keeps track of which rows it marked itself versus which ones you
decided about, so it can correct its own marks without overwriting your
decisions. A book that has actually had metadata written is now shown as
applied even if it had been skipped or rejected earlier in the session — what
happened to the file wins over what was planned for it.
