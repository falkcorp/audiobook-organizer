// file: internal/database/invite_id_test.go
// version: 1.0.0
// guid: 9a8ca8ab-e26e-42e1-aa64-f6d0610fe70e
// last-edited: 2026-10-20

package database

import (
	"strings"
	"testing"
	"time"
)

// An invite's ID is stable, not the token, and finds the invite whether it is
// pending, used or expired.
func TestInviteID_FindsTheInviteAndRevealsNoToken(t *testing.T) {
	s := newInviteTestStore(t)
	tok := strings.Repeat("a", 64)
	if _, err := s.CreateInvite(&Invite{Token: tok, Username: "g", RoleID: "viewer", ExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	id := InviteID(tok)
	if len(id) != 32 || strings.Contains(tok, id) || id != InviteID(tok) {
		t.Fatalf("InviteID = %q", id)
	}
	if id == InviteID(strings.Repeat("b", 64)) || InviteID("") != "" {
		t.Fatal("distinct tokens must have distinct ids, and no token no id")
	}
	got, err := s.GetInviteByID(id)
	if err != nil || got == nil || got.Token != tok {
		t.Fatalf("GetInviteByID(expired) = %+v, %v", got, err)
	}
	for _, bad := range []string{"", tok, strings.Repeat("0", 32)} {
		if got, err := s.GetInviteByID(bad); err != nil || got != nil {
			t.Fatalf("GetInviteByID(%q) = %+v, %v; want nothing", bad, got, err)
		}
	}
}
