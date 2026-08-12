# Task 001: SEC-AUDIT-0 — Switch govulncheck to binary mode

**Depends on:** none
**Estimated effort:** S (30 min)
**Wave:** 1 (run immediately, no dependencies)

## Goal

The project sets `GOEXPERIMENT=jsonv2` in the govulncheck step, which causes source-mode
scanning to fail. Switch to `-mode=binary` so govulncheck runs successfully.

## Context

- File: `.github/workflows/vulnerability-scan.yml`
- The `scan-go` job currently runs `govulncheck ./...` with `env: GOEXPERIMENT: jsonv2`
- Binary mode scans the compiled binary instead of source, bypassing the jsonv2 incompatibility
- The workflow already installs govulncheck via `go install golang.org/x/vuln/cmd/govulncheck@latest`

## Files to modify

- `.github/workflows/vulnerability-scan.yml` — change the govulncheck run step

## Instructions

1. Open `.github/workflows/vulnerability-scan.yml`
2. Find the step `name: Run govulncheck` which currently runs `govulncheck ./...`
3. Change it to:
   ```yaml
   - name: Build binary for scanning
     run: go build -o /tmp/audiobook-organizer-bin ./cmd/audiobook-organizer/
     env:
       GOEXPERIMENT: jsonv2

   - name: Run govulncheck
     run: govulncheck -mode=binary /tmp/audiobook-organizer-bin
   ```
4. Remove `env: GOEXPERIMENT: jsonv2` from the original govulncheck step (it moves to the build step)
5. Bump the file version header (e.g. `1.7.0` → `1.8.0`) and update `last-edited`

## Test

Push the branch and confirm the "Go Vulnerability Check" job passes in CI.
Locally: `go build -o /tmp/ao-bin ./cmd/audiobook-organizer/ && govulncheck -mode=binary /tmp/ao-bin`

## Commit

```
fix(ci): switch govulncheck to binary mode for GOEXPERIMENT=jsonv2 builds (SEC-AUDIT-0)
```

## PR title

`fix(ci): govulncheck binary mode — SEC-AUDIT-0`

## After merging

Mark `- [ ] **SEC-AUDIT-0**` as `- [x]` in `TODO.md`.
