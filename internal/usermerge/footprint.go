// file: internal/usermerge/footprint.go
// version: 1.0.2
// guid: 5a7c3e91-b284-4d06-9f1a-e8d62b05c473
// last-edited: 2026-11-10

package usermerge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	coreauth "github.com/falkcorp/mediacore/auth"
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

// Footprint reads userID's footprint. It writes nothing. Before the identity flip
// the account, its sign-ins, keys and sessions are read from the books store
// (footprintFromBooks); once accounts live in core they are read through core's
// masked AdminAccountFootprint, as the principal bound on ctx, and the families
// books still owns from books' store (footprintFromCore). Both answer the same
// shape. A flipped service without core's handles refuses
// (ErrUnavailableUnderIdentity) before it reads anything.
func (s *Service) Footprint(ctx context.Context, userID string) (*Footprint, error) {
	if err := s.CheckAvailable(); err != nil {
		return nil, err
	}
	if s.flipped() {
		return s.footprintFromCore(ctx, userID)
	}
	return s.footprintFromBooks(userID)
}

// footprintFromBooks is HEAD's footprint: every family from the books store.
func (s *Service) footprintFromBooks(userID string) (*Footprint, error) {
	u, err := s.mustUser(userID, "user")
	if err != nil {
		return nil, err
	}
	d, err := s.merger.ScanUserData(userID)
	if err != nil {
		return nil, err
	}
	f := newFootprint(summarize(u), userID, d)
	owner := coreauth.NormalizeEmail(s.ownerEmail())
	for _, r := range d.OAuthIdentities {
		var id database.OAuthIdentity
		if err := json.Unmarshal(r.Value, &id); err != nil {
			return nil, fmt.Errorf("decode %s: %w", r.Key, err)
		}
		f.Identities = append(f.Identities, Identity{ID: id.ID, Provider: id.Provider, Subject: maskSubject(id.Subject),
			Email: id.Email, LinkedAt: id.CreatedAt, OwnerEmail: owner != "" && coreauth.NormalizeEmail(id.Email) == owner})
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

// footprintFromCore is the footprint once accounts live in core (F4-08 B10fp).
// The account and everything core owns about it (sign-ins, keys, invites, the
// allow-list row, sessions) come from core's AdminAccountFootprint, which holds
// counts and masked values and checks the principal on ctx itself, so holding the
// handle grants nothing. The families books still owns (listening state,
// bookmarks, preferences, playback, listen stats, playlists, collections) come
// from books' store through ScanBooksUserData, which never reads the claims
// books retired (oauthid:, apikey:, invite:, sess:).
func (s *Service) footprintFromCore(ctx context.Context, userID string) (*Footprint, error) {
	u, err := s.coreUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, fmt.Errorf("read user: %w: %s", database.ErrUserNotFound, userID)
	}
	cf, err := s.core.Admin.AdminAccountFootprint(ctx, coreauth.UserID(userID))
	if errors.Is(err, coreauth.ErrNotFound) {
		return nil, fmt.Errorf("read user: %w: %s", database.ErrUserNotFound, userID)
	}
	if err != nil {
		return nil, fmt.Errorf("read the account's footprint: %w", err)
	}
	d, err := s.idMerger.ScanBooksUserData(userID)
	if err != nil {
		return nil, err
	}
	f := newFootprint(summarizeCore(u), userID, d)
	f.Counts[FamOAuthIdentities] = len(cf.Identities)
	f.Counts[FamAPIKeys] = cf.APIKeys
	f.Counts[FamInvites] = cf.Invites
	f.Counts[FamInviteGrants] = cf.InviteGrants
	f.ActiveSessions = cf.ActiveSessions
	f.Unreadable = append(f.Unreadable, cf.Unreadable...)
	owner := coreauth.NormalizeEmail(s.ownerEmail())
	for i, l := range cf.Identities {
		// The id is an ordinal: core's sign-in has no id of its own, and the
		// provider and subject (or a hash of the subject) would identify it.
		f.Identities = append(f.Identities, Identity{ID: fmt.Sprintf("%s#%d", l.Provider, i+1), Provider: l.Provider,
			Subject: l.SubjectMask, Email: l.Email, LinkedAt: l.LinkedAt,
			OwnerEmail: owner != "" && coreauth.NormalizeEmail(l.Email) == owner})
	}
	return f, nil
}

// newFootprint is the footprint of user with every family counted from d (the
// books families, and the core-owned ones when d came from a scan that read
// them) and no sign-ins or sessions yet.
func newFootprint(user AccountSummary, userID string, d *database.UserData) *Footprint {
	f := &Footprint{User: user, Identities: []Identity{}, Counts: map[string]int{}, Unreadable: slices.Clone(d.Unreadable)}
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
	return f
}
