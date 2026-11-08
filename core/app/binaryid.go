// file: core/app/binaryid.go
// version: 1.0.0
// guid: 2b64c3bd-0973-4fb7-bc0f-25dd80ed2695
// last-edited: 2026-11-08

package app

import "github.com/falkcorp/mediacore/buildid"

// BinaryID returns the running binary's identity: opts.Version, "+", and the
// lowercase hex SHA-256 of the executable (06 §6.4.5, FE6-3). It is
// buildid.Binary(opts.Version) and adds nothing to it: the computation lives
// in core/buildid, a dependency-free leaf, so an app's --version-json and the
// kernel compute the same value before and after the App exists. It needs no
// ldflags and no .git.
//
// It is never the UI build ID, which names one built UI bundle and is "dev"
// for every API-only build (03 §3.6 "Build identity").
func BinaryID(opts Options) (string, error) {
	return buildid.Binary(opts.Version)
}
