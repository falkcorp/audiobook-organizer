// file: internal/repairs/planstore.go
// version: 1.1.0
// guid: 8885f927-1097-4448-a36b-4a903c887c6d
// last-edited: 2026-10-22

package repairs

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Where a plan's rows live.
//
// A plan used to be one JSON value inside the plan operation's own result: 40
// to 57 MB for the retired-versions fixer. Every reader of an operation row
// (GetOperationV2, the /system/status poll, the boot scan, the diagnostics
// bundle) decoded all of it, the v3 mirror stored it a second time, and every
// page click in the lane decoded it again.
//
// Now the operation's result is the summary alone (PlanResult without Rows), a
// few hundred bytes, and the rows are stored under the plan op's id through
// database.OpRowStore: one key per row holding the row, and one small key per
// row holding what a page needs to filter and count it (its class, skip kind
// and owner flag). Page scans only those small keys and reads the rows of the
// page it returns; Select reads only the rows an apply names.
//
// CUT-OVER, NO BACKWARD COMPATIBILITY. A plan stored the old way carries no
// RowStore marker and LoadPlan refuses it with ErrPlanLegacyFormat: run the
// plan again. Its rows stay inside its operation row until that operation is
// deleted; nothing rewrites them.

// PlanRowStoreV1 is the PlanResult.RowStore of a plan whose rows SavePlan wrote
// beside the operation.
const PlanRowStoreV1 = "op_rows.v1"

// Errors of reading a stored plan's rows.
var (
	// ErrPlanLegacyFormat: the plan was stored before plans kept their rows
	// beside the operation, so its rows are not where LoadPlan looks.
	ErrPlanLegacyFormat = errors.New("repairs: this plan was stored in the old format (rows inside the operation result), which is no longer read; run the plan again")
	// ErrPlanRowsIncomplete: the rows found under the plan op's id are not the
	// rows its summary says it has.
	ErrPlanRowsIncomplete = errors.New("repairs: the stored plan rows do not match the plan summary; run the plan again")
	// ErrNoPlanRowStore: the store behind the plan operations cannot keep plan rows.
	ErrNoPlanRowStore = errors.New("repairs: the operation store cannot keep plan rows")
)

// PlanRowStoreOf resolves the plan row store out of the operation store, looking
// through any decorator that opted in to unwrapping.
func PlanRowStoreOf(store any) (database.OpRowStore, error) {
	rs, ok := database.AsCapability[database.OpRowStore](store)
	if !ok {
		return nil, ErrNoPlanRowStore
	}
	return rs, nil
}

// storedRows reads the rows of a plan that LoadPlan loaded.
type storedRows struct {
	rs   database.OpRowStore
	opID string
}

// SavePlan writes p.Rows under the plan op's id and marks p as stored there
// (p.RowStore). The caller then stores p as the operation's result, which
// carries the summary only. Every row is written before this returns, so a
// completed operation whose summary says PlanRowStoreV1 has all of its rows.
//
// On a failure it clears whatever it wrote and leaves p unmarked; a caller
// whose later write of the summary fails calls ClearPlanRows itself.
func SavePlan(rs database.OpRowStore, opID string, p *PlanResult) error {
	if rs == nil || opID == "" {
		return fmt.Errorf("repairs: save plan: %w", ErrNoPlanRowStore)
	}
	err := rs.ReplaceOpRows(opID, len(p.Rows), func(i int) (database.OpRow, error) {
		row := &p.Rows[i]
		data, err := json.Marshal(row)
		if err != nil {
			return database.OpRow{}, fmt.Errorf("encode row %q: %w", row.RowID, err)
		}
		return database.OpRow{ID: row.RowID, Index: encodeRowIndex(row), Data: data}, nil
	})
	if err != nil {
		_ = ClearPlanRows(rs, opID)
		return fmt.Errorf("repairs: save plan %s: %w", opID, err)
	}
	p.RowStore = PlanRowStoreV1
	return nil
}

// ClearPlanRows deletes the rows stored under a plan op's id.
func ClearPlanRows(rs database.OpRowStore, opID string) error {
	return rs.ReplaceOpRows(opID, 0, nil)
}

// A row's index entry is "class NUL skip-kind NUL 0|1 NUL flags" (the third
// field the owner flag, the last the row's Flags joined by commas). Class,
// skip kind and flags are fixer-defined identifiers, never NUL.
func encodeRowIndex(r *Row) []byte {
	owner := "0"
	if r.OwnerApplicable {
		owner = "1"
	}
	return []byte(r.Class + "\x00" + r.Skipped + "\x00" + owner + "\x00" + strings.Join(r.Flags, ","))
}

// rowRef is what Page needs of a row: its id, class, skip kind, owner flag and
// Flags.
type rowRef struct {
	id, class, skipped string
	owner              bool
	flags              []string
}

func (r *rowRef) hasFlag(f string) bool { return slices.Contains(r.flags, f) }

func (r *rowRef) applicable() bool { return r.skipped == "" }

func decodeRowIndex(id string, ix []byte) (rowRef, error) {
	parts := strings.Split(string(ix), "\x00")
	if len(parts) != 4 {
		return rowRef{}, fmt.Errorf("repairs: row %q has a malformed index entry", id)
	}
	ref := rowRef{id: id, class: parts[0], skipped: parts[1], owner: parts[2] == "1"}
	if parts[3] != "" {
		ref.flags = strings.Split(parts[3], ",")
	}
	return ref, nil
}

func decodeRow(id string, data []byte) (Row, error) {
	var r Row
	if err := json.Unmarshal(data, &r); err != nil {
		return Row{}, fmt.Errorf("repairs: decode plan row %q: %w", id, err)
	}
	if r.BookIDs == nil {
		r.BookIDs = []string{}
	}
	return r, nil
}

// refs returns one rowRef per row of the plan, in RowID order: from the Rows in
// memory, or from the stored plan's index (small keys only, no row is read).
func (p *PlanResult) refs() ([]rowRef, error) {
	if p.stored == nil {
		out := make([]rowRef, len(p.Rows))
		for i := range p.Rows {
			r := &p.Rows[i]
			out[i] = rowRef{id: r.RowID, class: r.Class, skipped: r.Skipped, owner: r.OwnerApplicable, flags: r.Flags}
		}
		return out, nil
	}
	out := make([]rowRef, 0, p.Total)
	err := p.stored.rs.ScanOpRowIndex(p.stored.opID, func(id string, ix []byte) error {
		ref, err := decodeRowIndex(id, ix)
		if err != nil {
			return err
		}
		out = append(out, ref)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("repairs: read plan %s index: %w", p.stored.opID, err)
	}
	if len(out) != p.Total {
		return nil, fmt.Errorf("%w: %s holds %d rows, its summary says %d", ErrPlanRowsIncomplete, p.stored.opID, len(out), p.Total)
	}
	return out, nil
}

// Select returns a copy of the plan holding only the rows named in ids (those
// the plan has, in the order the plan lists them for an in-memory plan and in
// the order of ids for a stored one); an unknown id is simply absent. A stored
// plan reads exactly those rows.
func (p *PlanResult) Select(ids []string) (*PlanResult, error) {
	out := *p
	out.stored = nil
	out.Rows = []Row{}
	if p.stored == nil {
		want := make(map[string]bool, len(ids))
		for _, id := range ids {
			want[id] = true
		}
		for i := range p.Rows {
			if want[p.Rows[i].RowID] {
				out.Rows = append(out.Rows, p.Rows[i])
			}
		}
		return &out, nil
	}
	rows, err := p.stored.readRows(ids)
	if err != nil {
		return nil, err
	}
	out.Rows = rows
	return &out, nil
}

// readRows reads the rows with these ids, skipping one that has none.
func (s *storedRows) readRows(ids []string) ([]Row, error) {
	datas, err := s.rs.GetOpRows(s.opID, ids)
	if err != nil {
		return nil, fmt.Errorf("repairs: read plan %s rows: %w", s.opID, err)
	}
	rows := make([]Row, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for i, data := range datas {
		if data == nil || seen[ids[i]] {
			continue
		}
		seen[ids[i]] = true
		row, err := decodeRow(ids[i], data)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}
