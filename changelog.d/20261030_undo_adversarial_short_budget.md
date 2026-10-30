### Changed

- `internal/undo` `TestMembershipAdversarial_Suites` runs one seed and a tenth of the histories per suite under `-short` (the CI mode). The full table costs ~660 CPU-seconds under `-race` and pushed the package past its 25 min Woodpecker timeout on shared agents; unflagged runs still walk every seed at full depth.
