### Fixed

#### Cancelled and interrupted operations no longer show as running forever

Cancelling a long-running operation left the progress bar spinning and the
status stuck on "running" for as long as the page stayed open. The operation had
actually stopped — only the display was wrong — but there was no way to tell
that from the screen, and the only fix was a page reload.

The frontend waited for the operation to report the status `cancelled`, spelled
with two Ls. The backend has always written `canceled` with one. The two
spellings never met, so the check that was supposed to end the poll never
matched and the page polled every second indefinitely.

The same page also failed to notice operations that were stopped by a restart or
shutdown. Those end in one of several `interrupted_*` statuses, and the list the
frontend checked against was missing `interrupted_quiesced` — the one produced
in the most common case.

Both are now decided by a single shared check that recognises any interrupted
variant by shape rather than by an enumerated list, so a status added on the
server can no longer strand the display. The affected screens were the
deduplication tabs, the Library organize progress, and Diagnostics; Diagnostics
now also shows the real final status rather than a fixed label.
