// file: internal/undo/membership_pairrows_test.go
// version: 1.0.0
// guid: 6d1a9e43-b7f0-4c28-8e35-c04a7f2d9b16
// last-edited: 2026-10-22

package undo

import (
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// pairRows is rows plus, for every live survivor-side marker among them that has
// none, the retired book's row it pairs with (same operation, the retired book
// the marker names, the survivor, the marker's list), so a fixture that spells
// out the markers only still describes live retires: a marker counts only with
// a live row of its retire (markerHistory). The row takes the marker's times, so
// it is stamped reverted when the marker is.
func pairRows(rows []*database.OperationChange) []*database.OperationChange {
	live := map[string]bool{}
	for _, r := range rows {
		if r != nil && !r.Voided && r.ChangeType == ChangeTypeUserContentFollow {
			live[r.OperationID+"\x00"+r.FieldName+"\x00"+r.BookID+"\x00"+r.NewValue] = true
		}
	}
	out := append([]*database.OperationChange(nil), rows...)
	for _, m := range rows {
		if m == nil || m.Voided || m.ChangeType != ChangeTypeUserContentFollow {
			continue
		}
		kind, listID, ok := UserContentFromField(m.FieldName)
		if !ok || !IsMarker(kind) {
			continue
		}
		main := UserContentCollection
		if kind == UserContentPlaylistHeld || kind == UserContentPlaylistTook {
			main = UserContentPlaylist
		}
		field := UserContentField(main, listID)
		if live[m.OperationID+"\x00"+field+"\x00"+m.NewValue+"\x00"+m.BookID] {
			continue
		}
		out = append(out, &database.OperationChange{ID: "row-of-" + m.ID, OperationID: m.OperationID, BookID: m.NewValue, ChangeType: ChangeTypeUserContentFollow,
			FieldName: field, NewValue: m.BookID, CreatedAt: m.CreatedAt, CreatedSeq: m.CreatedSeq, RevertedAt: m.RevertedAt, RevertedSeq: m.RevertedSeq})
	}
	return out
}
