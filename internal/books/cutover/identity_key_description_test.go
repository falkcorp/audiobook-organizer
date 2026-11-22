// file: internal/books/cutover/identity_key_description_test.go
// version: 0.1.0
// guid: f4226951-ac48-4704-98e2-fdea128bc9cc
// last-edited: 2026-11-22

package cutover

import (
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/auth"
)

// describedKeyRows is headKeyRows with a description in HEAD's row.
func describedKeyRows(id, raw, description string) []seedRow {
	rows := headKeyRows(id, uAdmin, raw, []string{"library.view"}, "active")
	rows[0].val.(map[string]any)["description"] = description
	return rows
}

// importedDescription runs M3 over the corpus plus one described HEAD key and
// returns the key as core's store holds it and the boot's log.
func importedDescription(t *testing.T, id, raw, description string) (auth.APIKey, string) {
	t.Helper()
	r := newIDRig(t, "stage1", idRows(describedKeyRows(id, raw, description)...))
	snap, err := r.run(t)
	if err != nil {
		t.Fatalf("M3: %v\n%s", err, r.logs.String())
	}
	sk, ok := snap.Keys[id]
	if !ok {
		t.Fatalf("the key %s was not imported (blocked, quarantined or lost)", id)
	}
	// It verifies by the SHA-256 its HEAD row held, as it did before the cutover,
	// so the abk_/mck_ value its owner already has keeps working.
	if sk.Hash != idDigest(raw) || !sk.Key.Imported || sk.Key.Status != auth.KeyActive {
		t.Errorf("key %s: hash %q imported %v status %s, want HEAD's hash, imported, active", id, sk.Hash, sk.Key.Imported, sk.Key.Status)
	}
	return sk.Key, r.logs.String()
}

const normalizedNote = "a key's description was normalized"

// TestKeyDescription_HEADDescriptionArrivesOnTheCoreKey: a clean HEAD
// description is carried verbatim, with no note.
func TestKeyDescription_HEADDescriptionArrivesOnTheCoreKey(t *testing.T) {
	const id, text = "01M7ZZZKEYDESCCLEAN00001", "Bat-Signal spare, used by the Batcomputer phone app"
	k, logs := importedDescription(t, id, "mck-batcave-desc-clean", text)
	if k.Description != text {
		t.Fatalf("description %q, want HEAD's %q", k.Description, text)
	}
	if strings.Contains(logs, normalizedNote) {
		t.Errorf("a clean description produced a note:\n%s", logs)
	}
	// The same at the cap: exactly 1024 characters, none of them cut.
	const id2 = "01M7ZZZKEYDESCCAP0000001"
	full := strings.Repeat("é", auth.MaxKeyDescriptionRunes)
	if k, logs = importedDescription(t, id2, "mck-batcave-desc-cap", full); k.Description != full || strings.Contains(logs, normalizedNote) {
		t.Fatalf("a description of exactly the cap changed (%d characters)", len([]rune(k.Description)))
	}
}

// TestKeyDescription_MultilineIsTidiedNotABlocker: HEAD's create form was a
// multi-line box and HEAD cannot edit a description, so a line break must not
// stop the cutover. It becomes one space, the key imports, and one note names
// the key and what changed, never the text.
func TestKeyDescription_MultilineIsTidiedNotABlocker(t *testing.T) {
	const id = "01M7ZZZKEYDESCMULTI00001"
	k, logs := importedDescription(t, id, "mck-batcave-desc-multi", "  Alfred's phone\r\n\r\nreissue after SECRET-LABEL\t(spare)\x07  ")
	if want := "Alfred's phone reissue after SECRET-LABEL (spare)"; k.Description != want {
		t.Fatalf("description %q, want %q", k.Description, want)
	}
	if err := auth.CheckKeyDescription(k.Description); err != nil {
		t.Errorf("the imported description breaks core's own rule: %v", err)
	}
	var notes []string
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, normalizedNote) {
			notes = append(notes, l)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "key="+id) || !strings.Contains(notes[0], "line breaks") || !strings.Contains(notes[0], "control characters") {
		t.Fatalf("notes %q, want exactly one naming %s and what changed", notes, id)
	}
	if strings.Contains(logs, "SECRET-LABEL") {
		t.Errorf("a log line carries the description text")
	}
}

// TestKeyDescription_OverlongIsTruncatedNotABlocker: 1500 characters import
// as the first 1024, with a note.
func TestKeyDescription_OverlongIsTruncatedNotABlocker(t *testing.T) {
	const id = "01M7ZZZKEYDESCLONG000001"
	var b strings.Builder
	for i := 0; b.Len() < 1500; i++ {
		b.WriteRune(rune('a' + i%26))
	}
	long := b.String()
	k, logs := importedDescription(t, id, "mck-batcave-desc-long", long)
	if k.Description != long[:auth.MaxKeyDescriptionRunes] {
		t.Fatalf("imported %d characters, want the first %d of HEAD's %d", len([]rune(k.Description)), auth.MaxKeyDescriptionRunes, len(long))
	}
	if !strings.Contains(logs, normalizedNote) || !strings.Contains(logs, "key="+id) || !strings.Contains(logs, "truncated") {
		t.Errorf("no note names %s and the truncation:\n%s", id, logs)
	}
	if strings.Contains(logs, long[:40]) {
		t.Errorf("a log line carries the description text")
	}
}
