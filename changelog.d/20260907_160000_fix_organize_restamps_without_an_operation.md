### Fixed

#### Organized books are restored to the library view after a scan, not just prevented from vanishing

A companion to the rescan fix in the same release. Stopping a scan from marking
organized books as unorganized does nothing for the books it had already marked —
those stayed hidden from author pages, series and counts, and nothing in the
system was able to put them back.

The automatic organize pass that runs after a scan is what should have restored
them. It examines every book, finds these ones already sitting exactly where they
belong, and then did nothing further — because it only recorded that fact when a
person had started the organize by hand. Run automatically, it recorded nothing
at all. Since it is the automatic run that follows every scan, the books a scan
had just mislabelled were seen, judged correct, and left mislabelled.

Being in the right place is now recorded whichever way the organize was started.
The record of *who* started it is still only written when there was one, so an
automatic pass no longer erases the name of the last organize a person ran — it
had been overwriting that with a blank every time it touched a book.

Two related improvements came out of the same change. The pass now works through
books in parallel rather than one at a time, which matters because it now does
real work on every automatic run over the whole library. And its "already
correct" tally now counts only the books it actually recorded, instead of
counting every book it looked at — so the number in the summary can no longer
disagree with the library itself.
