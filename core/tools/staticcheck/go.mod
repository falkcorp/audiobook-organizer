// file: core/tools/staticcheck/go.mod
// version: 1.0.0
// guid: 70c4f09b-806f-413e-a40b-e538cdd7933a
// last-edited: 2026-11-08

// A copy of the root's tools/staticcheck/go.mod, so core's CI runs staticcheck
// from an export of core/ alone (12 §12.3.11). It must resolve the same versions
// as the root's: scripts/ci/check_module_skew.py compares the two.

module github.com/falkcorp/mediacore/tools/staticcheck

go 1.27.0

tool honnef.co/go/tools/cmd/staticcheck

require (
	github.com/BurntSushi/toml v1.4.1-0.20240526193622-a339e1f7089c // indirect
	golang.org/x/exp/typeparams v0.0.0-20231108232855-2478ac86f678 // indirect
	golang.org/x/mod v0.41.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/tools v0.51.0 // indirect
	honnef.co/go/tools v0.8.1 // indirect
)
