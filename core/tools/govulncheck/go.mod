// file: core/tools/govulncheck/go.mod
// version: 1.0.0
// guid: c69f146a-c83d-4676-8fdb-2bd5485a5a81
// last-edited: 2026-11-08

// SC9's govulncheck (12 §12.3), pinned here and never run as @latest.
// golang.org/x/vuln v1.9.0 was published 2026-10-11, past 13 §13.6's 14-day
// release-age rule.
module github.com/falkcorp/mediacore/tools/govulncheck

go 1.27.0

tool golang.org/x/vuln/cmd/govulncheck

require (
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
)
