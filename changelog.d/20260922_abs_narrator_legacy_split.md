### Fixed

- The ABS-compatible API (AudioBooth) showed a multi-narrator cast as ONE
  narrator named "A, B, C, …" for any book with no narrator junction rows —
  typical of casts applied from Audible/Google Books/Hardcover, which metadata
  apply joins with ", " into the legacy narrator column. That column and the
  bare-string NarratorsJSON form are now split with the shared
  `util.SplitCreditNames`, which keeps "Surname, Given" as one person.
