// file: internal/server/handlers/opsv3/rows_user_text_test.go
// version: 1.0.0
// guid: 5d1e8a37-2c94-4b60-8f13-a7e0c6d29b48
// last-edited: 2026-10-26

package opsv3

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
)

// rowedRuns is memRuns with item outcomes to page.
type rowedRuns struct {
	*memRuns
	rows []database.ItemOutcome
}

func (r rowedRuns) ListItemOutcomesV3(string, database.ItemQuery) (database.ItemPage, error) {
	return database.ItemPage{Rows: r.rows, Total: int64(len(r.rows))}, nil
}

// An item outcome's key and error are free text of the run (a runner can key an
// item by a label and put the cause in the error), so GET /runs/:id/rows reads
// both through the caller's text rule: withheld from a caller without
// users.manage when they name a user, whole for one who holds it, and an
// ordinary item is served as recorded to either.
func TestListRows_KeyAndErrorGoThroughTheUserTextRule(t *testing.T) {
	const userID = "01INVENTEDOWNER0000000000A"
	rr := rowedRuns{memRuns: &memRuns{runs: map[string]database.RunRecord{
		"run1": {ID: "run1", DefID: "library.scan", State: state.V3Succeeded},
	}}, rows: []database.ItemOutcome{
		{Key: "state for user_id=" + userID, Outcome: "failed", Error: "merge failed: user_id=" + userID + " could not be moved"},
		{Key: "Year One / chapter 3", Outcome: "failed", Error: "disk full while writing Knightfall"},
	}}
	defs := map[string]DefInfo{"library.scan": {ID: "library.scan", Title: "Scan", Kind: "adapted"}}
	enq, appr, _, _ := startHarness(defs, nil)
	r := newRouter(New(Deps{
		Runs: rr, Defs: &fakeDefs{defs: defs}, Enqueuer: enq, Approver: appr,
		RepairsAccess: opaccess.NewRepairs(fixerSet{}, noOpRows{}, holdsHeader),
	}))
	type page struct {
		Items []RowView `json:"items"`
	}
	read := func(who caller) page {
		w := do(t, r, who, http.MethodGet, "/api/v3/ops/runs/run1/rows", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("rows = %d %s", w.Code, w.Body.String())
		}
		var p page
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	p := read(viewer)
	if len(p.Items) != 2 {
		t.Fatalf("items = %+v", p.Items)
	}
	if strings.Contains(p.Items[0].Key, userID) || strings.Contains(p.Items[0].Error, userID) {
		t.Fatalf("an item naming a user was served to a viewer: %+v", p.Items[0])
	}
	if p.Items[0].Key != activity.WithheldUserText || p.Items[0].Error != activity.WithheldUserText {
		t.Fatalf("withheld text is the shared notice, got %+v", p.Items[0])
	}
	if p.Items[1].Key != "Year One / chapter 3" || p.Items[1].Error != "disk full while writing Knightfall" {
		t.Fatalf("an ordinary item changed: %+v", p.Items[1])
	}

	users := caller{perms: auth.PermLibraryView + "," + auth.PermUsersManage, user: "u-adm", method: string(auth.MethodSession)}
	p = read(users)
	if !strings.Contains(p.Items[0].Key, userID) || !strings.Contains(p.Items[0].Error, userID) {
		t.Fatalf("users.manage must read the item whole: %+v", p.Items[0])
	}
}
