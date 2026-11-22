### Fixed

- CI: `scripts/ci/apt_install.sh` now adds a fallback Ubuntu archive source when a runner comes up with none configured. On 2026-11-22 an ubuntu-26.04 runner had only packages.microsoft.com in its apt sources, so every attempt failed with "Unable to locate package libtag1-dev" while other runners of the same image installed it. The fallback is a separate deb822 file, so no existing source is edited, and the mirror rotation rewrites it like any other archive source.
