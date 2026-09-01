### Changed

#### One implementation of how per-book metadata state is stored

Three separate parts of the app each carried their own private copy of the same
three functions: the key a book's metadata state is filed under, and the pair
that encodes and decodes an individual field's value. The copies were identical,
which is precisely why they were worth merging rather than leaving alone — they
describe a **stored** format, so any one of them could have been tidied in
isolation and the result would not have been a build failure or a failing test.
It would have been one part of the app no longer able to read what another part
had written.

Nothing about what is stored, or how, has changed. There is now a single
implementation, with the storage key and the "unset means absent" rule both
pinned by tests that fail if either is altered.
