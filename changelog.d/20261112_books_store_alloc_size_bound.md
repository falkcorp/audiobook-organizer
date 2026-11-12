### Fixed

- The book_atpath and opchange_by_book index key builders now check their operands (path, book id, journal key suffix) against fixed upper bounds before sizing the key buffer, and return an error for an over-limit operand instead of computing an unchecked allocation size. This clears the CodeQL go/allocation-size-overflow alerts; keys for valid inputs are byte-for-byte unchanged.
