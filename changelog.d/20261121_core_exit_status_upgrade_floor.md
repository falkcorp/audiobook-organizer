### Added

#### Core can now map an upgrade-floor refusal to exit status 78

`kernel.ExitCode` returns 78 (`ExitConfig`) for a store below a declared upgrade floor (`*storage.ErrUpgradeFloor`), like the cutover and store-too-new refusals it already mapped. A new `app.ExitStatus` is the host's process exit for a boot error: it writes the error to stderr, writes a last line `install <tag> first` for a floor refusal, and, only when the process runs under launchd (`XPC_SERVICE_NAME` set to a job label), waits 60 seconds before returning 78 so launchd's relaunch does not loop; under systemd, Docker or a terminal it returns at once. Nothing calls `app.ExitStatus` yet, so every serve refusal still exits 1; the `main.go` wiring is a separate change.
