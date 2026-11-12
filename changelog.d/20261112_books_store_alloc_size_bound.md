### Fixed

- The book_atpath and opchange_by_book index key builders now check their operands (path, book id, journal key suffix) against fixed upper bounds before sizing the key buffer, and return an error for an over-limit operand instead of computing an unchecked allocation size. This clears the CodeQL go/allocation-size-overflow alerts; keys for valid inputs are byte-for-byte unchanged.
- CreateBook and UpdateBook refuse an over-limit path or id. The book_atpath backfill and verify and the opchange_by_book backfill and verify skip such an existing row instead of aborting the pass: they count it (`skipped_overlong_rows`, `overlong_rows`, `skipped_overlong`) and log one warning with lengths only.
- A skipped overlong book row also gets the existing id-keyed undecodable marker, so path lookups fail closed for it and never report its path as free; the verify counts it without asking for a rebuild.
