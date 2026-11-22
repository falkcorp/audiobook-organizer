// file: internal/server/identity_throttle_key.go
// version: 0.1.0
// guid: 7e3a91c5-2b48-4d06-a8f1-5c9d0e72b364
// last-edited: 2026-11-21

package server

import (
	"net/http"

	"github.com/falkcorp/audiobook-organizer/internal/cfaccess"
	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
	"github.com/falkcorp/mediacore/httpapi"
)

// identityThrottleKey is abs.Options.IdentityThrottleKey: the bucket the ABS
// surface's own failure throttle (a dead refresh token, an empty credential) and
// hop 1's per-credential limiter charge a request to under core identity. nil on
// a server built without an identity, where HEAD's gin copy (loginThrottleKey)
// stays authoritative.
//
// The gin binder that copy reads does not run under LegacyAuthCore, so the bucket
// is the one cfaccess.ClientAddrTrust.ClientBucket names: the client address core's
// stage 0 attributed through a configured connector, else a verified Access
// assertion's CF-Connecting-IP from this host, else the TCP peer whatever headers
// the caller sends. A server given no Access verifier believes no assertion and
// keys on the peer, which shares a bucket among tunnel visitors but never skips
// one.
func (s *Server) identityThrottleKey() func(*http.Request) string {
	if s.identity == nil {
		return nil
	}
	trust := cfaccess.ClientAddrTrust{
		Verifier:     s.accessVerifier,
		IsLocalPeer:  remoteAddrIsLocal,
		KeyPresented: servermiddleware.KeyPresented,
		IsLANAddr:    isLANAddr,
	}
	return func(r *http.Request) string {
		stage0, attributed := httpapi.ClientAddrFrom(r.Context())
		return trust.ClientBucket(r, stage0, attributed)
	}
}
