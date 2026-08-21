### Added

- **Auto-revert when `main` goes red.** CI is no longer a pre-merge gate — anything
  can land, the gate runs after the merge, and if it fails, `main` is automatically
  restored to its last commit with a green run. A bug is filed naming every reverted
  commit, its PR, and its individual gate verdict. The selector refuses to act rather
  than guess when the evidence is thin: no green anchor in the window, a span wider
  than three commits, or a previous auto-revert inside the span. A red run is
  re-run once before anything is blamed, so a flake costs a retry instead of a revert.

### Fixed

- **The TODO collector could never push, and had not since 2026-08-10.** `main`
  carried three required status checks while `todo-collect` commits with `[skip ci]`,
  so the checks it was waiting on could never report and every push was rejected with
  `GH006`. Ten days and 143 fragments had backed up. The required checks contradicted
  `.github/branch-protection.json`, which has declared `"required_status_checks": null`
  since January; removing them restored the repo to its own checked-in configuration.
