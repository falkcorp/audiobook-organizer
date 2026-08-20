// file: internal/policy/policy.go
// version: 1.0.1
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-08-20

// Package policy evaluates per-book processing policy tags.
//
// Policy tags are user-applied strings on the book_tags table with the
// "policy:" prefix.  EvaluatePolicy converts a tag list into a typed
// BookPolicy struct so every consumer can branch on named booleans
// rather than repeated string comparisons.
package policy

import "github.com/falkcorp/audiobook-organizer/internal/database"

// Known policy tag strings.
const (
	TagNoOrganize    = "policy:no-organize"
	TagNoWriteback   = "policy:no-writeback"
	TagNoMetadata    = "policy:no-metadata"
	TagSourceAudible = "policy:source:audible"
	TagSourceGoogle  = "policy:source:google"
	TagSourceISBN    = "policy:source:isbn"
	TagPriorityHigh  = "policy:priority:high"
	TagPriorityLow   = "policy:priority:low"
)

// BookPolicy holds the processing flags derived from a book's tags.
type BookPolicy struct {
	NoOrganize      bool   // skip filesystem renaming/moving
	NoWriteback     bool   // skip tag write-back to audio files
	NoMetadataFetch bool   // skip automated metadata enrichment
	PreferredSource string // "audible", "google", "isbn", or ""
	Priority        int    // 10=high, -10=low, 0=normal
}

// KnownPolicyTags returns the full catalogue of recognised policy tags
// with human-readable descriptions, for use by the API endpoint.
func KnownPolicyTags() []PolicyTagInfo {
	return []PolicyTagInfo{
		{Tag: TagNoOrganize, Description: "Skip filesystem renaming/moving for this book."},
		{Tag: TagNoWriteback, Description: "Skip writing metadata tags back to audio files."},
		{Tag: TagNoMetadata, Description: "Skip automated metadata enrichment from external sources."},
		{Tag: TagSourceAudible, Description: "Prefer Audible as the metadata source for this book."},
		{Tag: TagSourceGoogle, Description: "Prefer Google Books as the metadata source."},
		{Tag: TagSourceISBN, Description: "Prefer ISBN-based lookup (Open Library) as the metadata source."},
		{Tag: TagPriorityHigh, Description: "Process this book at high priority in async queues."},
		{Tag: TagPriorityLow, Description: "Process this book at low priority in async queues."},
	}
}

// PolicyTagInfo describes one known policy tag.
type PolicyTagInfo struct {
	Tag         string `json:"tag"`
	Description string `json:"description"`
}

// EvaluatePolicy derives a BookPolicy from the book's tag strings.
func EvaluatePolicy(tags []string) BookPolicy {
	var p BookPolicy
	for _, t := range tags {
		switch t {
		case TagNoOrganize:
			p.NoOrganize = true
		case TagNoWriteback:
			p.NoWriteback = true
		case TagNoMetadata:
			p.NoMetadataFetch = true
		case TagSourceAudible:
			p.PreferredSource = "audible"
		case TagSourceGoogle:
			p.PreferredSource = "google"
		case TagSourceISBN:
			p.PreferredSource = "isbn"
		case TagPriorityHigh:
			p.Priority = 10
		case TagPriorityLow:
			p.Priority = -10
		}
	}
	return p
}

// EvaluatePolicyDetailed derives a BookPolicy from detailed BookTag entries.
func EvaluatePolicyDetailed(tags []database.BookTag) BookPolicy {
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Tag
	}
	return EvaluatePolicy(names)
}
