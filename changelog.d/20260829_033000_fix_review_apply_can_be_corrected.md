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
