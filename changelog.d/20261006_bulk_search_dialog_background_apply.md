### Fixed

#### Library bulk Search Metadata: picks are staged per book and applied together, in the background, on close

The multi-book Search Metadata wizard (Library → select books → Search
Metadata) awaited a synchronous apply for every pick, which waits up to 60s for
the library scan to release the book, and disabled every Apply / Apply Selected
/ No Match button while it ran; an applied book's buttons then stayed greyed
out as "Applied".

Picking a result (or some of its fields) now only stages it for that book and
moves on to the next book. Nothing is ever disabled; a new pick replaces the
book's staged one, Unstage drops it, and the header shows how many books are
staged. Closing the window (the "Apply N books & close" button, Close, Escape
or a backdrop click) sends every staged pick as a `background: true` apply
(each a durable `metadata.apply-when-scanned` operation), at most 4 requests in
flight, without waiting; "Discard all & close" sends nothing. One toast reports
the batch ("Metadata applied to X of N books", with Undo all), with one more
for failures and one for ASIN conflicts the search did not flag ("Apply anyway"
re-submits those with the override). The shared helper in
`stagedMetadataApply.ts` was split into start / await steps that both dialogs
use; field ticks now belong to one candidate in the bulk wizard too.
