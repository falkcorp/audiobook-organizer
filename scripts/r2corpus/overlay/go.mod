// file: scripts/r2corpus/overlay/go.mod
// version: 1.0.0
// guid: 3d8b5e21-6a47-4c90-b1f2-9e7a0c4d5b36
// last-edited: 2026-11-10

// This directory is its own module on purpose, and it declares no requirements.
//
// The overlay imports internal/database, which exists only at the R0 tag the
// overlay is built against (build_overlay.py checks that tag out, copies the
// *.go files here into it and builds with -tags r2corpus_overlay). main lost
// internal/database in R1-26. A build constraint keeps `go build` and `go vet`
// off these files, but `go mod tidy` reads every file whatever its tags, so
// inside the root module it failed on the missing import. A nested module is
// invisible to the root's ./... and to tidy.
//
// build_overlay.py copies only *.go, so this file never reaches the R0 tag's
// build. Do not re-point the imports at internal/books/store: the R0 tag does
// not have that package, and the overlay must write rows with R0's own types.
module github.com/falkcorp/audiobook-organizer/scripts/r2corpus/overlay

go 1.27.0
