// file: internal/scanner/author_parsers_shim_test.go
// version: 1.0.0
// guid: 2393a3d3-14e8-44b4-817d-942a2d6bfdd6
// last-edited: 2026-09-01

package scanner

import "github.com/falkcorp/audiobook-organizer/internal/authorname"

// The two path->author parsers used to be defined in this package. They now
// have ONE implementation, in internal/authorname, and production code here
// calls it directly.
//
// These aliases are TEST-ONLY and exist so this package keeps its own
// behaviour tests for them. That is worth preserving rather than deleting as
// duplicate coverage: the tests were written against this package's consumers
// (book.Author, metadata.Artist), and a shared unit test in authorname cannot
// observe what this package does with the result. They are in a _test.go file
// specifically so no production symbol here shadows the shared one.
var (
	extractAuthorFromDirectory = authorname.ExtractAuthorFromDirectory
	parseFilenameForAuthor     = authorname.ParseFilenameForAuthor
)
