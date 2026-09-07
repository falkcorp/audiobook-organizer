### Fixed

#### A bulk metadata apply now survives a restart instead of being abandoned

Restarting the server in the middle of an "apply cached metadata" run threw the
run away. It was marked interrupted and stopped there — the books it had already
applied kept their metadata, but everything still queued was silently forgotten,
and nothing said so. On a 699-book batch interrupted halfway, the remaining 350
simply never happened, and the only way to notice was to look at the library and
find them unchanged.

The run now picks up where it left off. It records its progress as it goes, and
after a restart it continues with the books it still owes rather than starting
over or giving up. Progress keeps counting against the batch you actually
started, so a resumed run reads "480 of 699" rather than restarting the count at
a smaller number and looking like a different, smaller job.

A second batch waiting in the queue no longer cancels the first. The startup
logic that decides which interrupted runs to bring back was written for the
library scan, where only one run of a job exists at a time and the newest is
always the one you want. An apply run is not like that — each one carries its own
list of books — so "a newer run exists" was throwing away a half-finished batch
whenever a second one happened to be queued at restart, which is an ordinary
deploy rather than a rare accident. Jobs that carry their own list of items are
now exempt from that rule.

Three details worth knowing, because all three are deliberate:

- **A book that could not be applied is not retried forever.** If a book has no
  cached metadata to apply, it is counted, logged, and passed over; a restart
  will not go back to it. This is what stops one bad book from making every
  future run fail on the same thing. The trade-off is that a book whose file
  write failed for a passing reason — a storage hiccup — is also passed over. Its
  database changes are saved and the failure is reported; re-running an apply for
  that book writes the file.
- **Approving more books while a run is waiting still works.** The new books are
  added to the run rather than replacing what it had left to do, and the run's
  record of what it already finished is preserved. Adding work to an
  already-restarted run used to be the situation most likely to lose it, and a
  second restart at exactly the wrong moment could still have undone it — the
  saved progress note is now cleared once it has been folded into the run, so it
  cannot come back later and overwrite newer work.
- **One busy moment no longer fails the whole batch.** When many files are being
  written at once, a book can wait too long for its turn and give up. That used
  to end the entire run as failed, discarding the report for every book that had
  already applied. Those books are now counted and named in the summary
  ("gate unavailable"), the run finishes normally, and re-running the apply
  picks them up.

One number in the summary is knowingly imprecise: after a restart, books an
earlier attempt had already applied can look like books with nothing to apply,
because applying clears the saved suggestion that would distinguish them. The
summary says so rather than presenting the count as exact.
