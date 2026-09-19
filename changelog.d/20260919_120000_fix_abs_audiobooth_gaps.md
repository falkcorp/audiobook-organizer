### Fixed

- **AudioBooth playlist and collection edits now reach the server.** Creating,
  renaming, deleting and adding or removing books in a playlist, and batch-adding
  or batch-removing books in a collection, all used to be redirected into the web
  app's API. The client re-sent each one as a read, so nothing was saved. The
  playlist changes use the same storage as the web UI and stay private to their
  owner.
- **Offline listening uploads (`POST /api/session/local-all`) are applied.** This
  endpoint used to return 404. Each offline session now moves the saved position
  forward only. A replayed or out-of-date session can never rewind the listener,
  and sending the same session twice changes nothing.
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
  production. A merged-away book is now never listed anywhere in the app, and
  an item's folder path comes from a file that is actually present.
