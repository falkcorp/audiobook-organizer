// file: internal/books/cutover/identity_preflight_test.go
// version: 1.0.0
// guid: 0260ad60-5605-4a7d-95d9-763edd0724ee
// last-edited: 2026-11-12

package cutover

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m3"
	"github.com/falkcorp/mediacore/storage"
)

// Neutral IDs for the synthetic rows of the preflight cells below: named for
// the row's role in the cell, never as a key, so a secret scanner has nothing
// to read as a credential.
const (
	pDupA    = "01M7ZZZPREFLIGHTDUPA000001"
	pDupB    = "01M7ZZZPREFLIGHTDUPB000002"
	pBroken  = "01M7ZZZPREFLIGHTBROKEN0003"
	pRowKid  = "01M7ZZZPREFLIGHTROWK000004"
	pRowHash = "01M7ZZZPREFLIGHTROWH000005"
	pRowOrph = "01M7ZZZPREFLIGHTROWO000006"
	pRowNoId = "01M7ZZZPREFLIGHTROWI000007"
	pJrnl    = "01M7ZZZPREFLIGHTJRNL000008"
	pJrnlOK  = "01M7ZZZPREFLIGHTJRNL000009"
)

// TestIdentityPreflight_Classes: what the preflight decides from HEAD's rows
// alone, each in its own cell over the clean R0 corpus plus the cell's rows.
// Every blocker names the row's key and a fixed class of reason, and no
// report carries a value (a username, a hash, an address).
func TestIdentityPreflight_Classes(t *testing.T) {
	user := func(id, name string) []seedRow { return headUserRows(id, name, []string{"viewer"}, "active") }
	journal := func(id, status string) seedRow {
		return seedRow{m3.JournalPrefix + id, map[string]any{"id": id, "status": status, "survivor_id": uBarbara, "absorbed_id": uDick, "ops": []any{}}}
	}
	type want struct{ key, family, reason string }
	for _, c := range []struct {
		name string
		rows []seedRow
		want []want
		fix  string // "" means the command's fix|delete, MergeFix for a journal
	}{
		{
			name: "two users that fold to one username: the second is held back and names the first by ID",
			rows: rowsOf(user(pDupA, "Slade.Wilson"), user(pDupB, "slade.wilson")),
			want: []want{{"u:" + pDupB, "u:", "held by user " + pDupA}},
		},
		{
			name: "a key whose owner row does not decode: the user and the key are both named, users first",
			rows: rowsOf([]seedRow{{"u:" + pBroken, "{not json"}}, headKeyRows(pRowKid, pBroken, "mck-dead-owner-0001", []string{"library.view"}, "active")),
			want: []want{{"u:" + pBroken, "u:", "does not decode"}, {"apikey:" + pRowKid, "apikey:", "owner is not stored"}},
		},
		{
			name: "a key with no owner row at all",
			rows: headKeyRows(pRowOrph, pDupA, "mck-no-owner-0002", []string{"library.view"}, "active"),
			want: []want{{"apikey:" + pRowOrph, "apikey:", "owner is not stored"}},
		},
		{
			name: "a key whose stored hash is not a SHA-256",
			rows: []seedRow{{"apikey:" + pRowHash, map[string]any{"id": pRowHash, "user_id": uBarbara, "name": "bad-hash", "token_hash": "zz-not-a-digest",
				"scopes": []string{"library.view"}, "status": "active", "created_at": "2026-11-10T11:00:00Z", "expires_at": "2027-11-10T11:00:00Z"}}},
			want: []want{{"apikey:" + pRowHash, "apikey:", "token hash"}},
		},
		{
			name: "an OAuth identity the importer rejects (no provider)",
			rows: []seedRow{{"oauthid:" + pRowNoId, map[string]any{"id": pRowNoId, "provider": "", "subject": "mck-subject", "email": "", "user_id": uBarbara, "created_at": "2026-11-10T11:00:00Z"}}},
			want: []want{{"oauthid:" + pRowNoId, "oauthid:", "importer rejects"}},
		},
		{
			name: "a merge journal that does not decode cannot be ruled out as in progress",
			rows: []seedRow{{m3.JournalPrefix + pJrnl, "{broken"}},
			want: []want{{m3.JournalPrefix + pJrnl, m3.JournalFamily, "cannot be ruled out"}},
			fix:  m3.MergeFix,
		},
		{
			name: "merge journals that finished, applied or undone, do not block",
			rows: []seedRow{journal(pJrnl, "applied"), journal(pJrnlOK, "undone")},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if len(c.want) == 0 {
				// A passing preflight is a boot that completes.
				r := newIDRig(t, "stage1", idRows(c.rows...))
				if _, err := r.run(t); err != nil {
					t.Fatalf("the cell must pass the preflight: %v\n%s", err, r.logs.String())
				}
				return
			}
			r := newIDRig(t, "stage1", idRows(c.rows...))
			legacy := r.before(t)
			got := r.refusal(t, r.newMem(t))
			if len(got) != len(c.want) {
				t.Fatalf("blockers %d, want %d: %+v", len(got), len(c.want), got)
			}
			for i, w := range c.want {
				b := got[i]
				if string(b.Key) != w.key || b.Family != w.family || !strings.Contains(b.Reason, w.reason) || b.Step != IdentityCutoverID {
					t.Errorf("blocker %d = {%s %s %q}, want {%s %s ...%q...}", i, b.Key, b.Family, b.Reason, w.key, w.family, w.reason)
				}
				if c.fix == m3.MergeFix {
					if b.Fix != m3.MergeFix {
						t.Errorf("blocker %d fix %q, want the merge's", i, b.Fix)
					}
				} else if !offers(b.Fix, "fix") || !offers(b.Fix, "delete") {
					t.Errorf("blocker %d fix %q does not offer fix and delete", i, b.Fix)
				}
			}
			text := strings.Join(storage.CutoverBlockerReport(got), "\n") + "\n" + r.logs.String()
			for _, v := range append(credentialValues(legacy), "slade.wilson", "Slade", "zz-not-a-digest", "mck-subject") {
				if strings.Contains(text, v) {
					t.Errorf("the report or the log carries the value %q", v)
				}
			}
		})
	}
}
