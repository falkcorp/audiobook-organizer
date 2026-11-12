// file: core/internal/libraryhttp/ports.go
// version: 1.0.0
// guid: 7648e970-3e5f-464c-8686-d69a71162172
// last-edited: 2026-11-11

package libraryhttp

import "github.com/falkcorp/mediacore/library"

// Library is the part of library.Manager the routes call: the registry's read
// side, without Resolve and Abs (a host path never reaches this package).
// library.Manager satisfies it.
type Library interface {
	Root(id library.RootID) (*library.Root, error)
	Roots(filter library.RootFilter) []*library.Root
}

// library.Manager is the production implementation.
var _ Library = library.Manager(nil)
