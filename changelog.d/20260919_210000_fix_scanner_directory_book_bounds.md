### Fixed

- **Scanner: a flat directory can no longer become one book on a three-file
  sample.** The directory-book rule in `groupFilesIntoBooks` decided whether a
  whole folder was a single book from the album tags of its first three files,
  however many files the folder held. A flat author shelf whose first three
  entries happened to be consecutive chapters of one work therefore collapsed
  into one record — in production, a 1,494-file shelf stored as a single
  404.9-hour "book", with 205 such books holding 20.1% of every book_file row in
  the library. Above a small directory the rule now requires every file's album
  tag to agree, and no book may claim more than 250 audio files out of one flat
  directory regardless of the evidence — enforced on the shape rather than on
  one branch, so it applies equally to the whole-directory verdict, to an album
  group, to a sequential chapter group, and to the point where any of them is
  turned into book_file rows. Every refusal is logged. Files a refusal leaves
  behind are not imported by that pass, which is deliberate: a book silently
  owning an arbitrary 250 of 1,494 files is a worse record than none.
- **Scanner: two concurrent writers can no longer mint two book rows at one
  path.** The scan's upsert checked for an existing row at the top of the
  function and created one hundreds of lines later, with nothing re-reading the
  path in between; the existing lock over that create is keyed on folder+title,
  not on path. A directory-shaped book has no file hash, so that check was the
  only guard — and production holds eight book rows at one shelf path, each
  owning its own full set of 1,494 book_file rows. The create is now serialised
  per path and re-checks under the lock, merging into the existing row instead
  of minting a twin — and carrying across the version group a content-hash
  match had already written onto the partner row, so the partner is not left in
  a group of one that reads as "this book has other versions" and lists none.
