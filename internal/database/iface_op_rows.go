// file: internal/database/iface_op_rows.go
// version: 1.0.4
// guid: afe6c0bf-64dc-4200-9150-d9262fe2a512
// last-edited: 2026-10-27

package database

// OpRow is one blob row owned by an operation: a row of a stored plan, kept
// beside the operation's own row instead of inside its result.
type OpRow struct {
	// ID is unique within the operation. Rows are scanned in byte order of
	// the ID, so a caller that wants a particular order picks IDs that sort
	// that way.
	ID string
	// Index is a small summary of the row that a scan can read without
	// reading Data (for a plan row: its class and skip kind). May be empty.
	Index []byte
	// Data is the row itself.
	Data []byte
}

// OpRowStore keeps blob rows under an operation id, outside the operation's
// own row.
//
// Why it exists: an operation row is read whole by GetOperationV2, by the
// /system/status poll, by the boot scan and by the diagnostics bundle, and
// the v3 mirror stores it a second time. A repairs plan holds one row per
// candidate (tens of thousands of them: about 39 MB in one result), and every
// one of those readers decoded all of it. The rows live here instead, and the
// operation's ResultData keeps only the summary.
//
// It is a capability, not part of Store (nobody edits store.go): resolve it
// with AsCapability[OpRowStore](store). A row set belongs to the operation id
// and is removed when the operation row is deleted (DeleteOperationV2,
// DeleteRunV3 and SweepHollowOperationsV2 all remove it). In production the only
// caller of those that runs on request is a person discarding the operation
// (registry.Discard); nothing prunes operation rows, or their row sets, by age,
// so a set stays until the operation is discarded.
type OpRowStore interface {
	// ReplaceOpRowsPrefix makes the rows of one namespace of the operation's
	// rows, the rows whose ID begins with rowPrefix, exactly the n rows at(0) ..
	// at(n-1): it deletes every row already stored under opID and rowPrefix,
	// then writes the new ones in bounded batches, calling at once per row so
	// the caller never holds the whole encoded set. Every other row of the
	// operation is left alone, so a writer of one kind of row (a result's class
	// lists) never deletes another kind's (a plan's rows, repairs.PlanRowPrefix)
	// under the same operation id: every kind of row has its own prefix. Every
	// row at returns must have an ID that begins with rowPrefix, or the call
	// fails: otherwise the set it replaces would not be the set it wrote.
	//
	// There is deliberately no call that replaces EVERY row of an operation, and
	// an empty rowPrefix is refused: a writer that wiped the operation's whole
	// row set would delete the rows of whichever other writer shares its id.
	// (A discard removes the whole set, by the operation id: see
	// opv2RunScopedPrefixes.)
	//
	// The write is NOT atomic across batches: a failure leaves a partial set,
	// which is why a caller that fails clears its prefix again
	// (ReplaceOpRowsPrefix(opID, rowPrefix, 0, nil)) and why a reader checks the
	// row count its own summary recorded. n == 0 only clears the namespace.
	ReplaceOpRowsPrefix(opID, rowPrefix string, n int, at func(i int) (OpRow, error)) error
	// ScanOpRowIndex calls fn with the ID and Index of every row of the
	// operation, in ID order, without reading any Data. index is valid only
	// during the call. A non-nil error from fn stops the scan and is returned.
	// (There is no scan of the rows themselves: a reader pages through the
	// index and reads the rows it needs with GetOpRows.)
	ScanOpRowIndex(opID string, fn func(rowID string, index []byte) error) error
	// GetOpRows returns the Data of each id in rowIDs, aligned with it; the
	// entry of an id that has no row is nil. The slices are the caller's.
	GetOpRows(opID string, rowIDs []string) ([][]byte, error)
}
