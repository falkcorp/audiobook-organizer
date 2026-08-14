### Added

#### `maintenance.booksig-sidecar-migrate` — realizes the 580 MB startup saving

PR #2387 moved the five `BookSig*` dedup-signature fields out of each `book:`
row and into a `book_sig:<id>` sidecar, so that startup stops reading data it
throws away. It shipped with **fallback-first reads**: a row that still carries
the signature inline keeps working untouched. That is what made the change safe
to deploy against 67,824 live books — and it is also why the saving was never
realized. Production warmup on 2026-08-13 still reported
`discarded_field_mb[book_sig_v1_and_mask] = 580` against `phase_mb[books] = 729`,
exactly as designed: 80% of every byte the books warmup phase read was a
signature discarded the instant it finished decoding.

This op moves the data. It walks every `book:` row and, for any row still
carrying an inline signature, writes the sidecar key and rewrites the row
without it — **both in one Pebble batch**, so a row is never stripped without
its sidecar. That pairing is the whole safety story: a book with all five
fields nil is exactly the shape `booksig-recovery-audit` classifies as "never
had a signature" rather than as damage, so a half-applied migration would be
invisible to the very op written to detect signature loss, and dedup would
simply stop matching those books. A conformance test asserts the pairing across
a mixed library, and six mutation controls confirm the tests fail when each
guard is removed.

It is **dry-run by default** — this is the only irreversible step in the sidecar
design — and reports `migrated / stripped-only / not-candidate / skipped-raced /
errors` plus an expected-magnitude cross-check, so a detector matching every
book (or none) is visible before anything is written. A `limit` parameter runs a
small canary first, as a stable prefix that a later full run resumes from.

Notes on two deliberate choices:

- It does **not** go through `UpdateBook`, which would also have migrated a book
  correctly. `UpdateBook` writes a `book_ver:` snapshot per call that
  deliberately keeps the full inline signature, so 67,824 calls would have
  written roughly 1.5 GB of fresh snapshot data in a migration whose purpose is
  to stop paying for those bytes — and it would have bumped `UpdatedAt` on every
  book, churning the search-index dirty set and the aggregate recompute.
- Books written by another path mid-migration are **skipped and counted**, never
  half-written. There is no per-book write lock in the store, so a naive
  read-then-write would have reverted an entire concurrent update — title, path
  and all — not merely the signature. Since every `UpdateBook` already strips the
  row it writes, the library migrates organically anyway and this op only
  accelerates it, which makes skipping always safe. Re-run to pick up the
  skipped books.
