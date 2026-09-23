### Fixed

- `maintenance.split-joined-narrators`: the dry run now lists every affected book with its narrators before and after the split, the authors it was checked against, and the pieces the rules removed. The old sample showed only the raw comma split, so authors and translators that the apply would drop looked as if they would become narrators. The sample field is renamed `raw_split` to say what it is.
