### Changed

#### Toolchain drift check covers Woodpecker, ci_remote.py and workflow patch levels

`scripts/check_toolchain_versions.py` now also fails when a `.woodpecker/*.yaml`
`GOTOOLCHAIN` or its digest-pinned `golang:` image, or `GO_TOOLCHAIN` in
`scripts/ci_remote.py`, differs from the Makefile pin. Workflow `go-version:`
literals must now equal the full pin (`'1.27.2'`) instead of the floating
minor, so runners can no longer move to a newer patch than every other path;
all 15 literals across the 8 workflows were updated to match.
