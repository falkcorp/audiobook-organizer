### Fixed

- When fingerprint workers turn a file down, the progress line now says why.
  It used to report a single flat count — one run set that aside for 56,196
  files with no indication of which of six unrelated causes applied, and the
  detail was written at a logging level the server does not run at, so it could
  not be recovered after the fact either. The reason is now counted alongside
  the number, using a fixed short list so it stays readable.
