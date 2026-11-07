- [ ] **MODULE-SKEW-PLATFORMS** `scripts/ci/check_module_skew.py` builds core's supplying-module
      set from `go list -deps -test ./...` unioned over GOOS (linux, darwin, windows) only. It
      ignores GOARCH (the arm64 RPi servers), CGO_ENABLED and build tags, so a module supplied only
      under one of those is never compared. Union over the shipped GOOS/GOARCH pairs, both
      CGO_ENABLED values and the repo's build tags (R1-10 review MINOR-6).
