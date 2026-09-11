### Fixed

#### Auto-merge primary selection (MATCH-4) no longer scores a read error as zero files

When a metadata apply finds other books carrying the same external record, it keeps the book with the most files as primary and marks the rest as merged into it. A failed `GetBookFiles` read during that election was counted as zero files — the same score a book with no files gets — so a transient database error on the book that actually had the most files made it lose, and every other book in the cluster was demoted to a primary chosen on bad data with nothing logged. The election now completes every read it depends on (the hash lookup, the triggering book's own lookup, and each candidate's file count) before choosing, and a failure on any of them aborts the cluster with an error and no write; the apply itself, already written by then, logs the abort at Error level and still succeeds. The ranking rule is unchanged.
