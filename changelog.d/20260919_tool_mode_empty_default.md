### Fixed

- The tool registry now reads an unset tool mode as `system`. A saved config from before the tools block existed left fpcalc's mode empty, so the windowed-fingerprint backfill (and anything else resolving fpcalc through the registry) reported fpcalc as unavailable.
