// file: internal/usermerge/footprint.go
// version: 1.0.0
// guid: 5a7c3e91-b284-4d06-9f1a-e8d62b05c473
// last-edited: 2026-10-23

package usermerge

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// Footprint is the read-only view behind the "Sign-ins & data" drawer: one
// account's linked sign-ins and how much of each kind of data it owns. It is
// what an admin checks before choosing which account survives a merge.
type Footprint struct {
	User       AccountSummary `json:"user"`
	Identities []Identity     `json:"identities"`
	// Counts is rows per family; book_state counts distinct books, and positions
	// counts position rows.
	Counts map[string]int `json:"counts"`
	// ActiveSessions is the web sessions that are neither revoked nor expired.
	ActiveSessions int `json:"active_sessions"`
	// Unreadable names rows the merge could not read, so the admin sees them
	// before a preview refuses over them.
	Unreadable []string `json:"unreadable"`
}

// FamPositions is the footprint-only family name of the upos: rows.
const FamPositions = "positions"

// Footprint reads userID's footprint. It writes nothing.
func (s *Service) Footprint(userID string) (*Footprint, error) {
	u, err := s.mustUser(userID, "user")
	if err != nil {
		return nil, err
	}
	d, err := s.merger.ScanUserData(userID)
	if err != nil {
		return nil, err
	}
	f := &Footprint{User: summarize(u), Identities: []Identity{}, Counts: map[string]int{}, Unreadable: d.Unreadable}
	if f.Unreadable == nil {
		f.Unreadable = []string{}
	}
	books := map[string]bool{}
	for _, r := range d.BookStates {
		books[strings.TrimPrefix(r.Key, "ubs:"+userID+":")] = true
	}
	for _, r := range d.Positions {
		if id, _, ok := strings.Cut(strings.TrimPrefix(r.Key, "upos:"+userID+":"), ":"); ok {
			books[id] = true
		}
	}
	f.Counts[FamBookState] = len(books)
	for fam, rows := range map[string][]database.UserDataRow{
		FamPositions: d.Positions, FamBookmarks: d.Bookmarks, FamSyncAliasUse: d.SyncAliasUses,
		FamPreferences: d.Preferences, FamPlaybackProgress: d.PlaybackProgress, FamPlaybackEvents: d.PlaybackEvents,
		FamListenStats: d.ListenStats, FamOAuthIdentities: d.OAuthIdentities, FamAPIKeys: d.APIKeys,
		FamPlaylists: d.Playlists, FamCollections: d.Collections, FamInvites: d.Invites, FamInviteGrants: d.InviteGrants,
	} {
		f.Counts[fam] = len(rows)
	}
	owner := util.NormalizeEmail(s.ownerEmail())
	for _, r := range d.OAuthIdentities {
		var id database.OAuthIdentity
		if err := json.Unmarshal(r.Value, &id); err != nil {
			return nil, fmt.Errorf("decode %s: %w", r.Key, err)
		}
		f.Identities = append(f.Identities, Identity{ID: id.ID, Provider: id.Provider, Subject: maskSubject(id.Subject),
			Email: id.Email, LinkedAt: id.CreatedAt, OwnerEmail: owner != "" && util.NormalizeEmail(id.Email) == owner})
	}
	sessions, err := s.store.ListUserSessions(userID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	now := s.now()
	for _, sess := range sessions {
		if !sess.Revoked && sess.ExpiresAt.After(now) {
			f.ActiveSessions++
		}
	}
	return f, nil
}
