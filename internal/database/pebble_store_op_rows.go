// file: internal/database/pebble_store_op_rows.go
// version: 1.1.1
// guid: d45025bc-200a-4342-a7e6-2d64d54a63fe
// last-edited: 2026-10-27

package database

import (
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

var _ OpRowStore = (*PebbleStore)(nil)

// Key schema of the operation row sets (OpRowStore):
//
//	opv2:row:{op_id}:{row_id}   → Data of the row
//	opv2:rowix:{op_id}:{row_id} → Index of the row (small; scanned without Data)
//
// Two families, not one, so an index scan walks only small values instead of
// stepping over every row's body. Neither sits under opv2:op:, so no reader of
// the operation rows (GetOperationV2, the timeline scans, the status poll)
// ever meets them. Every delete of an operation removes both ranges: see
// opv2RunScopedPrefixes.
const (
	opv2RowPrefix   = "opv2:row:"
	opv2RowIxPrefix = "opv2:rowix:"

	// opRowBatchRows / opRowBatchBytes bound one commit of ReplaceOpRows.
	opRowBatchRows  = 2000
	opRowBatchBytes = 8 << 20
)

func opv2RowRunPrefix(opID string) []byte   { return []byte(opv2RowPrefix + opID + ":") }
func opv2RowIxRunPrefix(opID string) []byte { return []byte(opv2RowIxPrefix + opID + ":") }

func opv2RowKey(opID, rowID string) []byte   { return []byte(opv2RowPrefix + opID + ":" + rowID) }
func opv2RowIxKey(opID, rowID string) []byte { return []byte(opv2RowIxPrefix + opID + ":" + rowID) }

// opv2RowFamilyPrefixes are the key ranges of one operation's row set: the two
// families above. ReplaceOpRows clears them before a write, stageOpRowsDelete
// removes them in the hollow-operation sweep, and opv2RunScopedPrefixes appends
// them to the other per-operation families, so a row family added here is
// cleared, swept and deleted by all three.
func opv2RowFamilyPrefixes(opID string) [][]byte {
	return [][]byte{opv2RowRunPrefix(opID), opv2RowIxRunPrefix(opID)}
}

// validOpRowsID refuses an operation id a row set cannot be keyed by: an empty
// one, or one holding the key separator, which would make "a" + row "b:c" and
// "a:b" + row "c" the same key and a prefix delete of "a" reach "a:b"'s rows.
// Operation ids are ULIDs, so this refuses nothing real; no row set can exist
// under an id it refuses, because ReplaceOpRows checks it too.
func validOpRowsID(opID string) error {
	if opID == "" {
		return errors.New("opv2: empty operation id")
	}
	if strings.Contains(opID, ":") {
		return fmt.Errorf("opv2: operation id %q holds the key separator", opID)
	}
	return nil
}

// rangeDeleter is the one batch method stageOpRowsDelete needs.
type rangeDeleter interface {
	DeleteRange(start, end []byte, o *pebble.WriteOptions) error
}

// stageOpRowsDelete stages the removal of both row ranges (opv2:row: and
// opv2:rowix:, opv2RowFamilyPrefixes) of one operation id into b. It is the
// only range delete the hollow-operation sweep may make: the sweep's ids come
// from the opv2:op: keys it walked, so the key-flow ratchet (opv2OpKeyAllowlist)
// refuses a raw DeleteRange there and accepts this helper by name, which can
// only ever build the row prefixes and so can never reach opv2:op:. An id a row
// set cannot be keyed by (validOpRowsID) is an error and stages nothing.
func stageOpRowsDelete(b rangeDeleter, opID string) error {
	if err := validOpRowsID(opID); err != nil {
		return err
	}
	for _, prefix := range opv2RowFamilyPrefixes(opID) {
		if err := b.DeleteRange(prefix, prefixEnd(prefix), nil); err != nil {
			return err
		}
	}
	return nil
}

// opv2RunScopedPrefixes are the per-operation key ranges every delete of an
// operation removes whole. DeleteOperationV2 and stageOpV2RowDelete (the v3
// discard's mirror of it) both walk this one list, so a family added here is
// deleted by both.
func opv2RunScopedPrefixes(id string) [][]byte {
	return append([][]byte{
		[]byte("opv2:log:" + id + ":"),
		[]byte("opv2:err:" + id + ":"),
	}, opv2RowFamilyPrefixes(id)...)
}

// ReplaceOpRows implements OpRowStore.
//
// It does not take opsMu: the rows are keyed by the operation id and nothing
// else reads them while the writer is still running the operation, and
// holding the lock for tens of megabytes of writes would stall every other
// operation write. A discard cannot land while the operation runs (the
// registry discards only terminal operations); a discard of an operation that
// is somehow gone leaves rows no delete will find, so the caller treats a
// failure of the write that follows (SetOperationV2Result refuses a missing
// row) as a reason to clear the set again.
func (p *PebbleStore) ReplaceOpRows(opID string, n int, at func(i int) (OpRow, error)) error {
	return p.ReplaceOpRowsPrefix(opID, "", n, at)
}

// ReplaceOpRowsPrefix implements OpRowStore. It clears the keys of both row
// families under {op_id}:{rowPrefix} only (the other rows of the operation are
// not in that range) and writes rows whose IDs all begin with rowPrefix.
func (p *PebbleStore) ReplaceOpRowsPrefix(opID, rowPrefix string, n int, at func(i int) (OpRow, error)) (err error) {
	defer recoverPebbleClosed("ReplaceOpRowsPrefix", &err)
	if err := validOpRowsID(opID); err != nil {
		return fmt.Errorf("ReplaceOpRows: %w", err)
	}
	for _, pfx := range opv2RowFamilyPrefixes(opID) {
		pfx = append(pfx, rowPrefix...)
		if err := p.db.DeleteRange(pfx, prefixEnd(pfx), pebble.Sync); err != nil {
			return err
		}
	}
	if n > 0 && at == nil {
		return errors.New("opv2: ReplaceOpRows: rows requested without a row source")
	}
	for lo := 0; lo < n; {
		batch := p.db.NewBatch()
		size := 0
		hi := lo
		for ; hi < n && hi-lo < opRowBatchRows && size < opRowBatchBytes; hi++ {
			row, rerr := at(hi)
			if rerr != nil {
				batch.Close()
				return rerr
			}
			if row.ID == "" {
				batch.Close()
				return fmt.Errorf("opv2: ReplaceOpRows %s: row %d has no id", opID, hi)
			}
			if !strings.HasPrefix(row.ID, rowPrefix) {
				batch.Close()
				return fmt.Errorf("opv2: ReplaceOpRows %s: row %q is outside the replaced prefix %q", opID, row.ID, rowPrefix)
			}
			if serr := batch.Set(opv2RowKey(opID, row.ID), row.Data, nil); serr != nil {
				batch.Close()
				return serr
			}
			if serr := batch.Set(opv2RowIxKey(opID, row.ID), row.Index, nil); serr != nil {
				batch.Close()
				return serr
			}
			size += len(row.Data) + len(row.Index) + 2*len(row.ID)
		}
		cerr := batch.Commit(pebble.Sync)
		batch.Close()
		if cerr != nil {
			return cerr
		}
		lo = hi
	}
	return nil
}

// scanOpRowFamily walks every key of one per-operation family in key order.
func (p *PebbleStore) scanOpRowFamily(prefix []byte, fn func(rowID string, value []byte) error) (err error) {
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return err
	}
	defer func() {
		if cerr := iter.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	for iter.First(); iter.Valid(); iter.Next() {
		if ferr := fn(string(iter.Key()[len(prefix):]), iter.Value()); ferr != nil {
			return ferr
		}
	}
	return iter.Error()
}

// ScanOpRowIndex implements OpRowStore.
func (p *PebbleStore) ScanOpRowIndex(opID string, fn func(rowID string, index []byte) error) (err error) {
	defer recoverPebbleClosed("ScanOpRowIndex", &err)
	return p.scanOpRowFamily(opv2RowIxRunPrefix(opID), fn)
}

// GetOpRows implements OpRowStore.
func (p *PebbleStore) GetOpRows(opID string, rowIDs []string) (out [][]byte, err error) {
	defer recoverPebbleClosed("GetOpRows", &err)
	out = make([][]byte, len(rowIDs))
	for i, id := range rowIDs {
		val, closer, gerr := p.db.Get(opv2RowKey(opID, id))
		if errors.Is(gerr, pebble.ErrNotFound) {
			continue
		}
		if gerr != nil {
			return nil, gerr
		}
		out[i] = append([]byte(nil), val...)
		if cerr := closer.Close(); cerr != nil {
			return nil, cerr
		}
	}
	return out, nil
}
