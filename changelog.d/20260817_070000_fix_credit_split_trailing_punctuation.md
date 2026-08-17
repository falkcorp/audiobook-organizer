### Fixed

- **Narrators no longer appear twice, once with a stray comma.** A credit written
  with an Oxford comma — "Lance Parkin, Stephen Cole, Alan Barnes, & Jonathan
  Morris" — was split on `" & "` before `", "`, which stranded a comma at the end
  of the preceding name. "Alan Barnes," was then listed as a separate narrator from
  "Alan Barnes", splitting one reader's books across two entries (14 books and 1).
  Split pieces now have leading and trailing separator punctuation trimmed.
  Measured against the live narrator list: 11 names cleaned, 8 of them merging into
  an entry for the same person that already existed, and no narrator lost.
  Punctuation that belongs to a name — "Sammy Davis Jr.", "Alex Hill-Knight" — is
  deliberately left alone.
