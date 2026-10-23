// file: internal/database/pebble_store_plan_legacy.go
// version: 1.2.0
// guid: 4d13d697-8014-4ff8-83c0-766ee1b5acd8
// last-edited: 2026-10-22

package database

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// legacyPlanLog logs migration 66 and the strip it runs.
var legacyPlanLog = logger.New("database.migration-066")

// LegacyPlanOpDefID is the definition id of a Repairs plan operation
// (repairs.PlanOpID; a repairs test pins that they agree). The database
// package cannot import repairs, and recognising these operations is all this
// file needs it for.
const LegacyPlanOpDefID = "repairs.plan"

// legacyPlanRowsKey is the member of a plan's result that held every row
// before plans stored their rows beside the operation (OpRowStore).
const legacyPlanRowsKey = "rows"

// LegacyPlanStrip is what StripLegacyPlanRows did.
type LegacyPlanStrip struct {
	// Plans is how many plan operations carried a result.
	Plans int
	// Stripped is how many of them held inline rows and had them removed.
	Stripped int
	// Unreadable is how many results were not JSON objects, left untouched.
	Unreadable int
	// UnreadableOps is how many operation records could not be decoded at all
	// (a value that is not valid JSON for an operation row). They are counted
	// and skipped: one corrupt record must not stop the boot that runs this.
	UnreadableOps int
	// BytesBefore and BytesAfter are the total size of the stripped results.
	BytesBefore, BytesAfter int64
}

// StripLegacyPlanRows removes the inline rows from the result of every
// Repairs plan operation that still has them, in place: the operation record
// and the rest of the summary (counts, fixer id, params) stay, and the result
// no longer carries the row_store marker, so repairs.LoadPlan refuses it as a
// legacy plan and the lane tells the owner to run the plan again. Before
// plans kept their rows beside the operation a single plan's result was tens
// of megabytes, and every read of the operation decoded all of it.
//
// It touches only plan operations' result text, and only where a top-level
// "rows" member exists, so a second run (and a plan stored in the current
// format, whose result has none) writes nothing. Each rewrite goes through
// SetOperationV2Result, so the opv2 row, its timeline index and the opv3 run
// record change together.
//
// An operation record whose bytes cannot be decoded is counted in
// UnreadableOps and skipped; a read, listing or write error is returned.
func (p *PebbleStore) StripLegacyPlanRows() (LegacyPlanStrip, error) {
	var rep LegacyPlanStrip
	after := ""
	for {
		ids, err := p.listOpV2IDsPage(after, 500)
		if err != nil {
			return rep, fmt.Errorf("list operations: %w", err)
		}
		if len(ids) == 0 {
			return rep, nil
		}
		after = ids[len(ids)-1]
		for _, id := range ids {
			// Classified by where the failure came from, not by its type: a
			// failed read (getOpV2Raw returns a pebble error) is fatal, while
			// any record whose BYTES do not decode as an operation (bad JSON,
			// a wrong-typed member, a time that does not parse, no id) wraps
			// errOpsV3Undecodable and is counted and skipped. Migration 66 runs
			// at boot and an error here refuses to start the server, forever,
			// over a record this strip cannot read and so cannot be a plan it
			// is able to touch. The whole row is decoded, not a projection,
			// because a rewrite goes through SetOperationV2Result, which
			// decodes it whole and needs its timestamps for the timeline index:
			// a plan whose record does not decode could not be rewritten
			// anyway, so it is counted here with the rest.
			row, _, err := p.getOpV2Raw(id)
			if err != nil {
				if errors.Is(err, errOpsV3Undecodable) {
					rep.UnreadableOps++
					legacyPlanLog.Warn("legacy plan strip: operation %s is not a decodable record; skipped: %v", id, err)
					continue
				}
				return rep, fmt.Errorf("read operation %s: %w", id, err)
			}
			if row == nil || row.DefID != LegacyPlanOpDefID || row.ResultData == nil {
				continue
			}
			rep.Plans++
			out, changed, err := stripLegacyPlanRows(*row.ResultData)
			if err != nil {
				rep.Unreadable++
				legacyPlanLog.Warn("legacy plan strip: result of operation %s is not a JSON object; left as it is: %v", row.ID, err)
				continue
			}
			if !changed {
				continue
			}
			before := len(*row.ResultData)
			if err := p.SetOperationV2Result(row.ID, out); err != nil {
				return rep, fmt.Errorf("rewrite plan %s: %w", row.ID, err)
			}
			rep.Stripped++
			rep.BytesBefore += int64(before)
			rep.BytesAfter += int64(len(out))
		}
	}
}

// stripLegacyPlanRows returns result without its top-level "rows" member, and
// whether there was one to remove.
func stripLegacyPlanRows(result string) (string, bool, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result), &m); err != nil {
		return "", false, err
	}
	if _, ok := m[legacyPlanRowsKey]; !ok {
		return result, false, nil
	}
	delete(m, legacyPlanRowsKey)
	out, err := json.Marshal(m)
	if err != nil {
		return "", false, err
	}
	return string(out), true, nil
}
