// file: internal/database/pebble_store_ops_v3_rowmarks.go
// version: 1.1.0
// guid: 01d04b73-654b-4085-a8d5-1ad63d2ca6c7
// last-edited: 2026-10-14

package database

import (
	"encoding/json"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// opv3RowMarkPrefix is the per-row apply marker family:
//
//	opv3:rowmark:{op_id}:{row_id} → JSON(RowMarkV3)
//
// One key per Fixer row whose Apply returned nil, written as soon as Apply
// returns, outside the chunk ledger (05-PR5b). A discard deletes the run's
// markers with the rest of its keys (opv3RunScopedPrefixes), and the
// transition that makes a run terminal deletes them with its ledger: they
// are resume evidence, and a terminal run is never resumed.
const opv3RowMarkPrefix = "opv3:rowmark:"

func opv3RowMarkRunPrefix(id string) []byte { return []byte(opv3RowMarkPrefix + id + ":") }

func opv3RowMarkKey(id, rowID string) []byte { return []byte(opv3RowMarkPrefix + id + ":" + rowID) }

// RowMarkV3 records that one Fixer row's Apply, in some attempt of the run,
// returned nil, and names the history that Apply's writes recorded. A
// resumed run counts the row as applied by that attempt only while the mark
// exists and none of the history it names has been undone
// (internal/operations/opswriter, Writer.RowApplied).
type RowMarkV3 struct {
	RowID    string    `json:"row_id"`
	MarkedAt time.Time `json:"marked_at"`
	// BookBatches are the metadata history batches the row's book writes
	// recorded.
	BookBatches []RowMarkBatch `json:"batches,omitempty"`
	// Changes are the ids of the operation journal rows, under the run's
	// op id, the row's other writes recorded.
	Changes []string `json:"changes,omitempty"`
}

// RowMarkBatch names one metadata history batch of a book.
type RowMarkBatch struct {
	BookID  string `json:"book_id"`
	BatchID string `json:"batch_id"`
}

// PutRowMarkV3 writes (or replaces) the marker of row m.RowID of run id.
// Refused for a missing or terminal run (openRunV3), like an intent: the
// check and the write hold opsMu, so a marker can never land after a
// discard or a terminal transition deleted the run's markers.
//
// The write does not sync, like ClearIntentV3. That is safe because the
// marker is only ever written after the row's writes and their history
// rows, which sync, and Pebble's WAL is replayed as a prefix: a marker that
// survives a crash implies everything it names survived too. A marker the
// crash loses is a false negative only (the resume skips the row as
// changed since plan; it never writes it twice), the same outcome as a
// crash between Apply's last write and the marker.
func (p *PebbleStore) PutRowMarkV3(id string, m RowMarkV3) (err error) {
	defer recoverPebbleClosed("PutRowMarkV3", &err)
	if m.MarkedAt.IsZero() {
		m.MarkedAt = time.Now().UTC()
	}
	data, err := json.Marshal(&m)
	if err != nil {
		return err
	}
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	if err := p.openRunV3(id); err != nil {
		return err
	}
	return p.db.Set(opv3RowMarkKey(id, m.RowID), data, pebble.NoSync)
}

// GetRowMarkV3 returns the marker of rowID in run id, or nil when there is
// none. One point read.
func (p *PebbleStore) GetRowMarkV3(id, rowID string) (*RowMarkV3, error) {
	var m RowMarkV3
	found, err := p.getJSONFound(opv3RowMarkKey(id, rowID), &m)
	if err != nil || !found {
		return nil, err
	}
	return &m, nil
}

// GetOperationChange returns one operation change row by its operation and
// change id, or nil when there is none. One point read of
// opchange:{operationID}:{changeID}, where GetOperationChanges reads the
// whole operation. Resolve it with database.AsCapability; it is not on
// Store.
func (p *PebbleStore) GetOperationChange(operationID, changeID string) (*OperationChange, error) {
	var c OperationChange
	found, err := p.getJSONFound(opChangeKey(operationID, changeID), &c)
	if err != nil || !found {
		return nil, err
	}
	return &c, nil
}
