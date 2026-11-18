### Fixed

- The `Repo Guards` job's "Install ffmpeg for the Whisper worker tests" step and the `Fixture Tests` job's "Install system dependencies" step no longer depend on a live apt mirror on every run. The downloaded `.deb` files are cached (keyed on the runner image version and the package list), checked against a sha256 manifest and installed with `dpkg -i` on a hit; only a miss goes to the mirror, through the existing retrying `apt_install.sh`. The install also finishes an interrupted dpkg first (`E: dpkg was interrupted`). New script: `scripts/ci/apt_cached_install.sh`.
