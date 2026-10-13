// file: internal/metafetch/batch.go
// version: 1.3.1
// guid: a1b2c3d4-e5f6-7a8b-9c0d-e1f2a3b4c5d6
// last-edited: 2026-10-13

package metafetch

// CandidateBookInfo contains summary info about a book used in candidate results.
type CandidateBookInfo struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Author     string `json:"author"`
	FilePath   string `json:"file_path"`
	ITunesPath string `json:"itunes_path,omitempty"`
	CoverURL   string `json:"cover_url,omitempty"`
	Format     string `json:"format,omitempty"`
	FileSize   int64  `json:"file_size_bytes,omitempty"`
	// Duration is the book's canonical runtime (database.ComputeBookRuntime:
	// the sum over its files) when that runtime is COMPLETE, else 0. It used
	// to be Book.Duration, which for a multi-file book with unprobed chapters
	// is a partial sum the review UI showed as the book's length.
	Duration int `json:"duration_seconds,omitempty"`
	// RuntimeStatus is complete | book_aggregate | partial | unknown
	// (database.BookRuntime.Status).
	RuntimeStatus string `json:"runtime_status,omitempty"`
	// RuntimeFilesKnown of RuntimeFilesCounted files carried a duration.
	RuntimeFilesKnown   int `json:"runtime_files_known,omitempty"`
	RuntimeFilesCounted int `json:"runtime_files_counted,omitempty"`
	// RuntimeLowerBoundSec is the known-file sum of a PARTIAL runtime: a
	// lower bound for display, never a total to compare.
	RuntimeLowerBoundSec int `json:"runtime_lower_bound_seconds,omitempty"`
	// Language is the book's current language as stored on the
	// Book row (ISO code or full name, whatever was last applied).
	// Used by the review dialog's language filter to hide
	// candidates whose language disagrees with the book's — the
	// motivating Spanish/English "Ancillary Sword" screenshot fix.
	// Empty when the book has no language set, in which case the
	// filter is a no-op for that row.
	Language string `json:"language,omitempty"`
}

// CandidateResult holds the metadata candidate search result for a single book.
type CandidateResult struct {
	Book      CandidateBookInfo  `json:"book"`
	Candidate *MetadataCandidate `json:"candidate,omitempty"`
	Status    string             `json:"status"` // "matched", "no_match", "error"
	Error     string             `json:"error_message,omitempty"`
}
