### Fixed

- **AudioBooth playlist and collection edits now reach the server.** Creating,
  renaming, deleting and adding or removing books in a playlist, and batch-adding
  or batch-removing books in a collection, all used to be redirected into the web
  app's API. The client re-sent each one as a read, so nothing was saved. The
  playlist changes use the same storage as the web UI and stay private to their
  owner.
- **Offline listening uploads are applied.** `POST /api/session/local-all` used
  to return 404, and `POST /api/session/local` accepted a session and then
  discarded it. Both now apply each session. An out-of-date session only moves
  the saved position forward. A session that started after the server's latest
  position may move it backward, which covers re-listening while offline.
  Sessions from before a progress reset are refused. A position far past the
  end of the book is rejected and never marks the book finished.
- **A read status you set by hand stays set.** Listening used to overwrite a
  manually chosen status (such as "abandoned") with a computed one.
- **Two edits to the same playlist no longer overwrite each other.** This covers
  an edit in the app and one in the web UI at the same time. Both land, where
  before the later write silently discarded the earlier one. Renaming a playlist
  to a name that is already taken now returns a conflict. It used to break the
  other playlist's lookup by name. Sending a playlist's full book list, for
  example to reorder it, no longer drops a book someone else just added: the
  books it names are reordered and any others are kept. Removing a book takes
  an explicit remove.
- **Credentials in URLs are masked in the request log.** This covers values such
  as `?token=`.
- **API keys passed in a download or ebook URL (`?token=`) are accepted.** They
  had been checked as session tokens and rejected with 401.
- **The narrator-image and send-to-e-reader endpoints give a direct answer.**
  They now return 404 and 400 directly, where they used to redirect.
- **Library filters work for genre, language, publisher, decade, tag and
  progress.** These used to return no books. The series tab now honours the
  filter too.
- **Size sort matches the size shown on each book.**
- **Author names sort without regard to case.**
- **Search shows only books the library shows, and every result opens the book
  it names.** Search used to include merge losers and hidden copies. Opening
  some of those results showed a different book: 96 of 652 results sampled in
  production. A merged-away book is no longer listed on its own. A playlist or
  collection that holds one shows the surviving copy in its place, and removing
  that copy removes the member. An item's folder path now comes from a file
  that is actually present.
