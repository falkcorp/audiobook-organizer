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

Two details worth knowing, because both are deliberate:

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
  already-restarted run used to be the situation most likely to lose it.
