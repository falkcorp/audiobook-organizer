- [ ] **Release builds take Go from `go.mod`, not the 1.27.1 pin** — `release-prod.yml` and
  `prerelease.yml` call `falkcorp/github-common`'s `reusable-release.yml`, which exposes a
  `go-experiment` input but no `go-version`; `gha-release-go` then resolves
  `max('1.24', go.mod)` = 1.27.0 while `Makefile`, `.envrc`, both Dockerfiles and the eight
  CI workflows pin `go1.27.1`. Not a break (1.27.0 satisfies `go 1.27.0`), but the one build
  path off the pin. Add a `go-version` passthrough to `reusable-release.yml` in
  `github-common`, then set it to `'1.27.1'` in both release workflows here. Found by the
  #3039 adversarial review, 2026-09-01.
