### Fixed

- Listings of series, authors and import paths now come back in the same order whether the in-memory index or the on-disk store answers. Names that differ only in case used to swap places between the two, which made the series-prune repair refuse a preview as "changed since preview" for no reason. Ties are now broken by ID, and duplicate-group members are ordered by ID as well.
