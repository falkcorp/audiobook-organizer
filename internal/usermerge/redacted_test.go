// file: internal/usermerge/redacted_test.go
// version: 1.0.0
// guid: 3a9f1c62-8e07-4d55-b4a8-e61d20c7f943
// last-edited: 2026-10-23

package usermerge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPlanSummaryAndErrorLeaveOutRowRefusalsAndAccounts(t *testing.T) {
	const rowRefusal = "cannot read pref row pref:u1:secret-key: decode failure"
	p := &Plan{
		SurvivorID: "u0", AbsorbedID: "u1",
		Survivor:           AccountSummary{ID: "u0", Username: "keeper-name", Email: "keeper@people.example.com"},
		Absorbed:           AccountSummary{ID: "u1", Username: "absorbed-name", Email: "absorbed@people.example.com"},
		AbsorbedIdentities: []Identity{{Provider: "oidc", Email: "sso@people.example.com"}},
		Conflicts:          []Conflict{{Family: "pref", Key: "pref:u1:secret-key"}},
		Refusals:           []string{"an account cannot be merged into itself", rowRefusal},
		Warnings:           []string{"w"},
		rowRefusals:        []string{rowRefusal},
		Digest:             "d",
	}
	sum := p.Summary()
	if sum.Applicable || sum.RefusalCount != 2 || sum.RowRefusalCount != 1 || len(sum.Refusals) != 1 || sum.WarningCount != 1 {
		t.Errorf("summary = %+v", sum)
	}
	raw, _ := json.Marshal(sum)
	for _, leak := range []string{"keeper-name", "absorbed-name", "people.example.com", "secret-key", "decode failure"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("summary exposes %q: %s", leak, raw)
		}
	}
	err := &RefusedError{Plan: p}
	if !errors.Is(err, ErrRefused) || strings.Contains(err.Error(), "secret-key") || !strings.Contains(err.Error(), "itself") {
		t.Errorf("error text = %q", err.Error())
	}
}

func TestJournalOpResultLeavesOutKeysAndErrorText(t *testing.T) {
	j := &Journal{ID: "j1", Status: StatusApplied, Error: "write pref:u1:secret-key failed",
		UndoConflicts: []string{"pref:u1:secret-key changed"}, Warnings: []string{"x"}}
	res := j.OpResult()
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "secret-key") || !res.Failed || res.UndoConflictCount != 1 || res.WarningCount != 1 {
		t.Errorf("result = %s", raw)
	}
}
