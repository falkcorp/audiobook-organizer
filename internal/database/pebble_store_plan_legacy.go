// file: internal/database/pebble_store_plan_legacy.go
// version: 1.1.0
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
// An operation record that cannot be decoded is counted in UnreadableOps and
// skipped; a listing, iterator or write error is returned.
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
			row, err := p.GetOperationV2(id)
			if err != nil {
				if isOpRowDecodeError(err) {
					// Migration 66 runs at boot and a failure here refuses to
					// start the server, forever, over a record this strip has no
					// business with: it can only touch plan results, and an
					// undecodable record is not one it can read. Skip it loudly.
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

// isOpRowDecodeError reports whether err is the stored bytes failing to decode
// (as opposed to the store failing to read them, which stays fatal).
// pebbleGetJSON returns json.Unmarshal's error unwrapped.
func isOpRowDecodeError(err error) bool {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syn) || errors.As(err, &typ)
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
