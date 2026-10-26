// file: internal/server/event_stream_credential_test.go
// version: 1.0.0
// guid: 2f8d4b6a-71c3-4e95-b0a8-6d3e1c9f5a27
// last-edited: 2026-10-26

// An event stream outlives the request that opened it, so it must end when the
// credential it was opened on stops standing: the account disabled, the session
// or the API key revoked, the key expired. Before this a stream kept serving a
// disabled account until its client happened to reconnect.

package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// credentialCase opens a stream as some credential of the ops-manager user and
// can then make that credential stop standing.
type credentialCase struct {
	name string
	// open returns the Bearer token the stream is opened with.
	open func(t *testing.T, store *database.PebbleStore, user *database.User, session string) (token string, kill func())
}

func credentialCases() []credentialCase {
	apiKey := func(exp time.Time) (database.APIKey, string) {
		raw, hash, err := database.GenerateAPIKeyToken()
		if err != nil {
			panic(err)
		}
		return database.APIKey{
			Name: "synthetic ops key", TokenHash: hash, Scopes: auth.All(),
			Status: "active", CreatedAt: time.Now(), ExpiresAt: &exp,
		}, raw
	}
	return []credentialCase{
		{"control: nothing changes", func(t *testing.T, _ *database.PebbleStore, _ *database.User, session string) (string, func()) {
			return session, func() {}
		}},
		{"account disabled", func(t *testing.T, store *database.PebbleStore, u *database.User, session string) (string, func()) {
			return session, func() {
				_, err := store.SetUserStatus(u.ID, "disabled")
				require.NoError(t, err)
			}
		}},
		{"session revoked", func(t *testing.T, store *database.PebbleStore, _ *database.User, session string) (string, func()) {
			return session, func() { require.NoError(t, store.RevokeSession(session)) }
		}},
		{"api key revoked", func(t *testing.T, store *database.PebbleStore, u *database.User, _ string) (string, func()) {
			k, raw := apiKey(time.Now().Add(time.Hour))
			k.UserID = u.ID
			created, err := store.CreateAPIKey(&k)
			require.NoError(t, err)
			return raw, func() { require.NoError(t, store.RevokeAPIKey(created.ID)) }
		}},
		{"api key expires", func(t *testing.T, store *database.PebbleStore, u *database.User, _ string) (string, func()) {
			exp := time.Now().Add(2 * time.Second)
			k, raw := apiKey(exp)
			k.UserID = u.ID
			_, err := store.CreateAPIKey(&k)
			require.NoError(t, err)
			return raw, func() { time.Sleep(time.Until(exp) + 100*time.Millisecond) }
		}},
	}
}

// TestEventStreams_EndWhenTheCallersCredentialStopsStanding: /operations/events
// and /api/v3/ops/events, opened as a user with the ops-manager role, keep going
// while the credential stands and end at the next event once it does not. With
// no recheck bound (WithLiveRecheck(0)) the end is at the very next event.
// Not parallel: it swaps the process-wide config.
func TestEventStreams_EndWhenTheCallersCredentialStopsStanding(t *testing.T) {
	streams := []struct {
		name, path string
		poke       func(ctx context.Context, f *repairsOpAccessFixture)
	}{
		{"v2 operations/events", "/api/v1/operations/events", func(ctx context.Context, f *repairsOpAccessFixture) {
			_ = f.srv.opHub.Publish(ctx, "op.marker", map[string]any{"note": "poke"})
		}},
		{"v3 ops/events", "/api/v3/ops/events", func(ctx context.Context, f *repairsOpAccessFixture) {
			_ = f.srv.opHub.Publish(ctx, "op.updated", map[string]any{"op_id": f.plainScan})
		}},
	}
	for _, st := range streams {
		for _, tc := range credentialCases() {
			t.Run(st.name+"/"+tc.name, func(t *testing.T) {
				f := newRepairsOpAccessFixture(t)
				f.srv.repairsOpAccess().WithLiveRecheck(0, nil)
				session := opsManagerToken(t, f)
				store, ok := f.srv.storeForWiring().(*database.PebbleStore)
				require.True(t, ok)
				user, err := store.GetUserByUsername("opsmgr")
				require.NoError(t, err)
				require.NotNil(t, user)
				token, kill := tc.open(t, store, user, session)

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				_, wait := openStream(t, f, ctx, st.path, token)
				ended := make(chan struct{})
				go func() { wait(); close(ended) }()

				kill()
				st.poke(ctx, f)
				if tc.name == "control: nothing changes" {
					st.poke(ctx, f)
					select {
					case <-ended:
						t.Fatal("a stream whose credential still stands was ended")
					case <-time.After(500 * time.Millisecond):
					}
					cancel()
					<-ended
					return
				}
				select {
				case <-ended:
				case <-time.After(5 * time.Second):
					t.Fatal("the stream outlived its credential: it was still open after the next event")
				}
			})
		}
	}
}
