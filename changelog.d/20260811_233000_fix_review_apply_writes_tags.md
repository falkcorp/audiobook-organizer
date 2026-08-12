### Fixed

#### Applying metadata from the Metadata Review screen now writes the tags into your audio files

Approving a match on the Metadata Review screen updated the database and
nothing else. The title, author, series and year all changed in the app; the
files on disk kept their old tags, and no cover art was embedded. Anything
reading the files directly — a car stereo, a phone, iTunes, a fresh scan — saw
the old metadata. There was nothing in the logs to suggest a problem, because
nothing had failed. The work was simply never started.

Two apply paths exist and they had drifted apart. The single-book path
(`POST /audiobooks/:id/apply-metadata`) schedules `ApplyMetadataFileIO` and
`WriteBackMetadataForBook` on the file-I/O pool, defaulting on. Its sibling
`BatchApplyFromCache` — the endpoint the review screen actually calls — did
neither. It called `ApplyMetadataCandidate` (database only) and then enqueued
`h.batcher`, which looks like a write-back but is the **iTunes** write-back
batcher: it syncs the book to the iTunes library and never touches an audio
tag. `WriteBackMetadataForBook` was unreachable from the review screen.
Production logged zero successful audio-tag writes in seven days.

The batch path now goes through the same file-I/O pool as its sibling, with an
optional `write_back` flag that defaults to **true** — identical semantics to
the single-book path. The work stays off the request thread: "Apply All" can
carry hundreds of books and rewriting their tags inline would hold the HTTP
request open for minutes. When no pool is wired the handler now logs a warning
naming the book instead of skipping silently, because a silent skip is the
exact shape of this defect.

This also restores **cover-art embedding** on the review path, which lives
inside the same `ApplyMetadataFileIO` call.

For a multi-file book — a 40-part MP3 set — the write covers every part, not
just the one shown in the dialog. Exactly what each part receives depends on
whether the book has per-file records:

- **With per-file records** (the normal case for a scanned multi-part book):
  every part gets the book-level tags plus its own per-track title and `N/40`
  track numbering.
- **Without them** (a book whose path is a folder but which has no segment
  rows): every audio file in the folder still gets the book-level tags, but
  with no per-track title and no track numbering.

Books whose files live under a protected path — the iTunes tree, import
folders — are still skipped entirely, by design. Nothing is written for them
on either branch.

#### The review screen no longer reports books it skipped as applied

Clicking APPLY reported success for every book requested, whether or not the
server had applied it. Books whose cached candidate had expired were skipped
server-side, counted as successes by the UI, marked applied, and vanished from
the queue — only to reappear on the next load. Measured during one session:
21 applied, 8 silently skipped, two of them clicked twice because nothing
appeared to happen the first time.

`BatchApplyFromCache` now returns `applied_ids`, `skipped` (with a reason per
book) and `requested` alongside the existing `applied` count. The review dialog
marks only the rows the server actually applied, reports the server's count
rather than the number requested, and leaves skipped rows visible in the queue
with a message saying how many were skipped and why.

#### An expired login no longer looks like a successful save

When a Cloudflare Access session expires, API calls never reach the server: the
request is answered with a redirect to the login page, the browser follows it
automatically, and the app receives an ordinary `200 OK` carrying HTML. Nothing
about that response looks wrong, so a login page was indistinguishable from a
successful save — one user sat clicking APPLY on a dead session, getting a
success message every time, while nothing was written.

The shared request wrapper now detects this and raises a distinct error, so it
surfaces everywhere in the app rather than only on this screen. Two independent
signals are used: a redirect that lands on a different host, and an HTML
response on an `/api/` route. The review screen reports it as "Session expired,
sign in again" and reverts every row, instead of showing a generic failure the
user would retry against the same dead session.

#### Successful tag writes for multi-file books are now logged

The multi-file branch of the tag writer logged only failures while the
single-file branch logged both, so a working multi-file write left no trace at
all and the logs could not distinguish a healthy system from one that never ran.
