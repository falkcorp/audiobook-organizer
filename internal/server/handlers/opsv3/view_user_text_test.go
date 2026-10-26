// file: internal/server/handlers/opsv3/view_user_text_test.go
// version: 1.0.0
// guid: 91c6e2a8-7f34-4d15-b0a9-5e83d1c7f246
// last-edited: 2026-10-26

package opsv3

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A run's error and the label of the item each worker is on (Label(current
// item)) can name a user. toView reads both through the caller's text rule, and
// does so on a copy of the workers: the slice is the record's own, shared with
// every other reader of it.
func TestToView_WorkerItemNamingAUserIsReadThroughTheTextRule(t *testing.T) {
	const userItem = "moving state for user 01INVENTEDWORKERITEM000A"
	rec := database.RunRecord{ID: "run-1", DefID: "library.scan", Error: "failed: " + userItem}
	rec.Progress.Workers = []database.WorkerProgress{
		{Worker: 0, Item: userItem},
		{Worker: 1, Item: "Year One / chapter 3"},
	}
	withhold := func(s string) string { return activity.WithoutOthersUserText(s) }

	v := toView(rec, nil, withhold)
	if got := v.Progress.Workers[0].Item; strings.Contains(got, "01INVENTEDWORKERITEM000A") || !strings.Contains(got, activity.WithheldUserText) {
		t.Fatalf("a worker's item naming a user was served: %q", got)
	}
	if got := v.Progress.Workers[1].Item; got != "Year One / chapter 3" {
		t.Fatalf("an ordinary item changed: %q", got)
	}
	if strings.Contains(v.Error, "01INVENTEDWORKERITEM000A") {
		t.Fatalf("the error naming a user was served: %q", v.Error)
	}
	if rec.Progress.Workers[0].Item != userItem {
		t.Fatalf("the record's own workers were edited: %q", rec.Progress.Workers[0].Item)
	}

	whole := toView(rec, nil, func(s string) string { return s })
	if whole.Progress.Workers[0].Item != userItem || whole.Error != rec.Error {
		t.Fatalf("a caller who may read it was served a changed view: %+v", whole)
	}
}
