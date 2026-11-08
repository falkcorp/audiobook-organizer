// file: core/tools/apidiff/go.mod
// version: 1.0.0
// guid: 2fbac1b0-6852-401c-a540-a99686499921
// last-edited: 2026-11-08

// SC1's apidiff and gorelease (12 §12.3), pinned here and never run from an
// ad-hoc @version. A nested module under core/ so it moves with core at the
// split and core CI runs from an export of core/ alone (SC4). x/exp version:
// published 2026-10-14, past 13 §13.6's 14-day release-age rule.
module github.com/falkcorp/mediacore/tools/apidiff

go 1.27.0

tool golang.org/x/exp/cmd/apidiff

tool golang.org/x/exp/cmd/gorelease

require (
	golang.org/x/exp v0.0.0-20261014143641-f0251eb4aa3e // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
)
