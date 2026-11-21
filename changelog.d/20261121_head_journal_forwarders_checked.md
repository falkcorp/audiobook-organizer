### Fixed

- The HEAD shell's two journal forwarders for the credits backfill now create their rows through the classification check, so an unclassified change type is refused there as everywhere else, and the journal-writer guard no longer allowlists that file.
