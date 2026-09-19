### Added

- Storage for windowed audio fingerprints: the database can now hold several fingerprints per file, cut from the middle and end of the file instead of only the shared opening credits. Nothing computes them yet; this adds the place to keep them. Existing fingerprints are left where they are and show up as the "head" window.
- Window fingerprints follow their file: deleting a file record removes its windows, moving a file to another book keeps them, and collapsing duplicate records of one file moves the windows onto the record that is kept.
