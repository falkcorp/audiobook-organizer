### Added

#### A core test that an upgrade-floor refusal writes nothing

`TestUpgradeFloorRefusesBeforeAnyWrite` boots a non-empty store below a declared upgrade floor, by version and by a required data migration that is not done, and checks that the boot is refused with the release to install first, no pending step runs, no checkpoint is taken, and every key and value is unchanged. It also checks that a floor the store satisfies boots and migrates, and that an empty store is exempt. Tests only; no behaviour changed.
