// file: internal/database/pebble_store_ops_v2_status_lookup.go
// version: 1.0.0
// guid: 6c2f8a19-4d7e-4b53-9a01-8e5b3c7d2f40
// last-edited: 2026-10-15

package database

import (
	"encoding/json"

	"github.com/cockroachdb/pebble/v2"
)

// HasOperationV2WithStatus reports whether any operations_v2 row for defID
// has the given status. It is NOT part of Store: callers reach it through
// AsCapability. There is no status index, so it scans opv2:op: rows until the
// first match; boot calls it once (04-P6: the transcode boot enqueue is skipped
// while an interrupted_ask transcode row waits for an operator).
func (p *PebbleStore) HasOperationV2WithStatus(defID, status string) (found bool, err error) {
	defer recoverPebbleClosed("HasOperationV2WithStatus", &err)
	prefix := []byte("opv2:op:")
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return false, err
	}
	defer iter.Close()
	for iter.First(); iter.Valid(); iter.Next() {
		var row OperationV2Row
		if json.Unmarshal(iter.Value(), &row) != nil {
			continue
		}
		if row.DefID == defID && row.Status == status {
			return true, nil
		}
	}
	return false, iter.Error()
}
