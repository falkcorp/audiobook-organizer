### Added

#### Proof that the backup inspector opens an archive a post-flip binary took (F8-04a G1, tests only)

A kernel booted the way the flip boots it (identity owners, core's level raised by a cutover, a module at level 1 with a done cutover and an upgrade floor that requires it) now creates a backup through core's backup service, and the inspector the kernel built for that service verifies it, stages a restore from it and opens its extracted store. The same tests show the refusals: an inspector whose owners lack core's level owner refuses the store, an archive from a newer build is refused as needing a newer build, and an archive holding an owner this build does not declare is refused as a manifest mismatch. No behaviour changes.
