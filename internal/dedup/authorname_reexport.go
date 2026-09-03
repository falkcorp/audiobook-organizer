// file: internal/dedup/authorname_reexport.go
// version: 1.0.0
// guid: add8e266-e06d-42ca-baeb-ede8df3f4716
// last-edited: 2026-09-03

package dedup

import "github.com/falkcorp/audiobook-organizer/internal/personname"

// The author-name predicates moved to internal/personname on 2026-09-03 so that
// internal/merge could reach them: six files in this package import
// internal/merge, so anything merge needs cannot live here.
//
// These wrappers exist so the ~22 existing dedup.X call sites did not have to
// move with them. New code should call internal/personname directly.

// NormalizeAuthorName normalizes whitespace around initials, strips a stranded
// leading conjunction, and trims. See personname.NormalizeAuthorName.
func NormalizeAuthorName(name string) string { return personname.NormalizeAuthorName(name) }

// IsDirtyAuthorName reports whether a name is obviously not a real author:
// publishers, copyright fragments, HTML-entity shrapnel. See
// personname.IsDirtyAuthorName.
func IsDirtyAuthorName(name string) bool { return personname.IsDirtyAuthorName(name) }

// IsProductionCompany reports whether a name matches a known audiobook
// production company. See personname.IsProductionCompany.
func IsProductionCompany(name string) bool { return personname.IsProductionCompany(name) }

// StripPositionalPrefix removes leading track/disc/chapter numbering.
// See personname.StripPositionalPrefix.
func StripPositionalPrefix(name string) string { return personname.StripPositionalPrefix(name) }

// IsPositionalArtifactName reports whether a name is filename/tag numbering
// shrapnel. See personname.IsPositionalArtifactName.
func IsPositionalArtifactName(name string) bool { return personname.IsPositionalArtifactName(name) }

// CleanAuthorNameForCreation resolves a raw artist tag to the author name that
// should be stored. See personname.CleanAuthorNameForCreation.
func CleanAuthorNameForCreation(raw string) (string, bool) {
	return personname.CleanAuthorNameForCreation(raw)
}
