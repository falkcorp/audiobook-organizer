// file: internal/metabatch/fetch_op.go
// version: 1.0.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f9a
// last-edited: 2026-05-11
//
// FetchOpParams holds the serializable parameters for the
// metadata.candidate-fetch v2 OperationDef. Kept here so the
// server package (which owns the handler and op registration) and
// any future recovery/replay tooling share the same type.

package metabatch

// FetchOpParams is the JSON params for the metadata.candidate-fetch
// v2 OperationDef.
//
// It carried a LegacyOpID until 2026-08-22: the handler minted a v1
// operations row, stamped its id here, and Run keyed OperationResult
// rows on it. The comment justifying that named three readers it kept
// working — and one of the three, handleGetPendingReview, did not
// exist anywhere in the repo. Results now key on the op's own v2 id,
// which every reader already has.
//
// AlreadyDone is gone too. It was the resume path's way of telling Run
// how far a previous attempt got; Run now derives that by reading the
// result rows it already wrote, so the count cannot disagree with the
// data the way a params field could.
type FetchOpParams struct {
	BookIDs    []string `json:"book_ids"`
	TotalBooks int      `json:"total_books"`
}
