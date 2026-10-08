// file: internal/server/credential_routes_test.go
// version: 1.4.0
// guid: 6d2f9a40-3b75-4e18-9c6a-8f1e0b4d27c3
// last-edited: 2026-10-08

// Route-table coverage for the credential guard (2026-10-07 security review,
// "sibling-path gate parity"): every state-changing route in the real router
// is either registered through credRoute/credRouteWhen (and so carries the one
// shared guard) or is on a reviewed exempt list with a reason. A new route
// that is neither fails here, so a second way to reset a password, mint a
// session or install a program cannot ship unguarded by omission.

package server

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	servermiddleware "github.com/falkcorp/audiobook-organizer/internal/server/middleware"
)

// sensitiveRoute matches routes that touch sign-in, users, keys, settings,
// programs, paths or whole-store state. Such a route must be guarded or
// exempted BY EXACT ROUTE with a reason; a prefix exemption never covers it.
var sensitiveRoute = regexp.MustCompile(
	`/auth/|/users|passw|invite|session|token|api-key|login|setup|role|perm|/restore|reset|bootstrap|config|setting|tool|install|/update/|plugin|backup|import-path|upload|debug|admin|wipe|system|owner|preferences|exclude|maintenance|itunes/library|worker`)

// exemptRoutes are state-changing routes reviewed and left without the guard.
var exemptRoutes = map[string]string{
	// Public sign-in surfaces: no caller identity yet, each has its own proof
	// (password, setup only while no user exists, a one-time bootstrap or
	// invite token).
	"POST /api/v1/auth/login":         "public password sign-in",
	"POST /api/v1/auth/setup":         "public; only while no user exists",
	"POST /api/v1/auth/bootstrap":     "public; consumes a one-time bootstrap token",
	"POST /api/v1/auth/accept-invite": "public; consumes a one-time invite token",
	// Taking access away is not a credential change.
	"POST /api/v1/auth/logout":            "ends the caller's own session",
	"DELETE /api/v1/auth/sessions/:id":    "revokes one of the caller's own sessions",
	"DELETE /api/v1/auth/api-keys/:id":    "revokes a key",
	"DELETE /api/v1/users/invites/:token": "revokes an invite",
	// Settings changed field by field: PUT /config refuses protected fields
	// (config.ChangedProtectedFields) for a non-interactive caller inside
	// UpdateService, on the decoded config.
	"PUT /api/v1/config": "protected fields refused in config.UpdateService",
	// Data, not credentials or paths.
	"POST /api/v1/backup/create":                        "writes a backup to the configured backup_dir",
	"DELETE /api/v1/backup/:filename":                   "deletes a backup file in backup_dir",
	"PUT /api/v1/maintenance-window/config":             "a time window and switches; names no path",
	"POST /api/v1/maintenance-window/run":               "runs the configured maintenance",
	"POST /api/v1/maintenance/jobs/:job_id":             "runs a built-in job",
	"POST /api/v1/maintenance/wipe":                     "admin-only library-data wipe with a typed confirm; users and keys untouched",
	"POST /api/v1/filesystem/exclude":                   "narrows what a scan reads; opens nothing new",
	"DELETE /api/v1/filesystem/exclude":                 "narrows what a scan reads; opens nothing new",
	"DELETE /api/v1/import-paths/:id":                   "removes a scan root; opens nothing new",
	"POST /api/v1/plugins/:id/enable":                   "toggles code already in the binary",
	"POST /api/v1/plugins/:id/disable":                  "toggles code already in the binary",
	"POST /api/v1/update/check":                         "checks for an update; installs nothing",
	"POST /api/v1/openlibrary/upload":                   "uploads dump content to the configured dump dir",
	"PUT /api/v1/preferences/:key":                      "the caller's own UI preferences",
	"DELETE /api/v1/preferences/:key":                   "the caller's own UI preferences",
	"POST /api/v1/setup/validate-openai-key":            "test call to a third-party API; stores nothing",
	"PATCH /api/v1/admin/debug/books/:id":               "admin-only raw library-row edit",
	"PATCH /api/v1/admin/debug/book-files/:id":          "admin-only raw library-row edit",
	"POST /api/v1/admin/debug/edits/:edit_id/undo":      "admin-only undo of a raw library edit",
	"POST /api/v1/admin/recompact-digests":              "admin-only activity maintenance",
	"POST /api/v1/audiobooks/:id/restore":               "un-deletes a book",
	"POST /api/v1/audiobooks/:id/cover-history/restore": "restores a book's cover",
	"POST /api/v1/audiobooks/:id/versions/:vid/restore": "restores a book version",
	"POST /api/v1/books/:id/versions/:vid/restore":      "restores a book version",
	"POST /api/v1/dedup/reset-acoustid":                 "clears dedup data",
	"POST /api/v1/repairs/:fixer/plan":                  "plans a library repair",
	"POST /api/v1/repairs/:fixer/apply":                 "applies a library repair",
	"POST /api/v1/repairs/:fixer/owner-apply":           "owner-gated in the handler by a verified Cloudflare Access identity",
	"POST /api/v1/fingerprint/worker/lease":             "fingerprint worker protocol",
	"POST /api/v1/fingerprint/worker/lease/:id/release": "fingerprint worker protocol",
	"POST /api/v1/fingerprint/worker/lease/:id/renew":   "fingerprint worker protocol",
	"POST /api/v1/fingerprint/worker/results":           "fingerprint worker protocol",
	// iTunes routes (no prefix exemption covers /api/v1/itunes/). iTunes is
	// import-only since 2026-10-07: these read the iTunes library into the
	// database, or read it, or write only database fields. Nothing writes the
	// iTunes library any more.
	"POST /api/v1/itunes/validate":                 "reads an iTunes library file; writes nothing",
	"POST /api/v1/itunes/test-mapping":             "tests a path mapping; writes nothing",
	"POST /api/v1/itunes/import":                   "imports FROM iTunes into the database; never writes iTunes",
	"POST /api/v1/itunes/import-status/bulk":       "reads import status (POST for the id list)",
	"POST /api/v1/itunes/pid-repair":               "dry-run-gated; clears duplicate PIDs on database rows only, never the iTunes library",
	"POST /api/v1/audiobooks/:id/user-tags":        "library tags",
	"PUT /api/v1/audiobooks/:id/user-tags":         "library tags",
	"DELETE /api/v1/audiobooks/:id/user-tags/:tag": "library tags",
}

// exemptPrefixes cover whole families of library-data routes. They never
// cover a route sensitiveRoute matches.
var exemptPrefixes = map[string]string{
	"/api/v1/audiobooks/":      "library data",
	"/api/v1/books/":           "library data",
	"/api/v1/authors":          "library data",
	"/api/v1/series":           "library data",
	"/api/v1/works":            "library data",
	"/api/v1/collections":      "library data",
	"/api/v1/playlists":        "library data",
	"/api/v1/metadata":         "library metadata and provider calls",
	"/api/v1/dedup/":           "dedup data",
	"/api/v1/review/":          "review queue",
	"/api/v1/merge/":           "merge undo journal",
	"/api/v1/operations":       "operations on library data",
	"/api/v1/activity/":        "activity log maintenance",
	"/api/v1/ai/":              "AI calls on library data",
	"/api/v1/diagnostics/":     "diagnostics",
	"/api/v1/deluge/":          "download-client calls",
	"/api/v1/discovery/":       "imports from the download client",
	"/api/v1/import/":          "per-request import of a library file (plan: residual, per-request paths)",
	"/api/v1/openlibrary/":     "OpenLibrary dump data",
	"/api/v1/blocked-hashes":   "dedup block list",
	"/api/v1/cache/":           "cache invalidation",
	"/api/v1/tasks/":           "scheduled-task switches and intervals; names no path",
	"/api/v1/purged-versions/": "library data",
}

func isStateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

func TestCredentialRoutes_EveryStateChangingRouteIsClassified(t *testing.T) {
	f := setupCredGuardServer(t)
	routes := f.srv.router.Routes()
	require.Greater(t, len(routes), 300, "the fixture router is missing most routes")

	var unclassified, bothWays, sensitivePrefixOnly []string
	seen := map[string]bool{}
	for _, r := range routes {
		if !isStateChanging(r.Method) {
			continue
		}
		key := r.Method + " " + r.Path
		seen[key] = true
		_, cred := f.srv.credRoutes[key]
		_, exempt := exemptRoutes[key]
		guarded := cred
		switch {
		case guarded && exempt:
			bothWays = append(bothWays, key)
		case guarded || exempt:
		default:
			prefixed := false
			for p := range exemptPrefixes {
				if strings.HasPrefix(r.Path, p) {
					prefixed = true
				}
			}
			switch {
			case prefixed && sensitiveRoute.MatchString(r.Path):
				sensitivePrefixOnly = append(sensitivePrefixOnly, key)
			case !prefixed:
				unclassified = append(unclassified, key)
			}
		}
	}
	sort.Strings(unclassified)
	sort.Strings(sensitivePrefixOnly)
	assert.Empty(t, unclassified, "state-changing routes neither guarded (credRoute/credRouteWhen), nor on the exempt lists; classify each one")
	assert.Empty(t, sensitivePrefixOnly, "sensitive routes covered only by a prefix exemption; guard them or exempt them by exact route with a reason")
	assert.Empty(t, bothWays, "routes both guarded and exempted")

	// The lists must not rot: every exact exemption and guarded route exists.
	for key := range exemptRoutes {
		assert.True(t, seen[key], "exempt route %q is not in the router any more", key)
	}
	for key := range f.srv.credRoutes {
		assert.True(t, seen[key], "guarded route %q was recorded but is not in the router", key)
	}
}

// TestCredentialRoutes_GuardedRoutesRefuseAKey: every recorded route refuses
// an all-scope admin API key with the guard's message. Conditional routes are
// exercised in the "another user" form, where they are guarded.
func TestCredentialRoutes_GuardedRoutesRefuseAKey(t *testing.T) {
	f := setupCredGuardServer(t)
	params := strings.NewReplacer(":id", f.targetKeyID, ":name", "fpcalc", ":fixer", "x")
	for key, kind := range f.srv.credRoutes {
		method, path, _ := strings.Cut(key, " ")
		if strings.HasPrefix(path, "/api/v1/users/:id") {
			path = strings.Replace(path, ":id", f.targetID, 1)
		}
		path = params.Replace(path)
		var body any = map[string]any{}
		if kind == credRouteConditional && method == http.MethodPost && strings.HasSuffix(path, "/api-keys") {
			body = map[string]any{"name": "for target", "user_id": f.targetID}
		}
		t.Run(key, func(t *testing.T) {
			w := f.do(t, f.apiKey, method, path, body)
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), servermiddleware.CredentialChangeRefusedMessage)
		})
	}
}

// The create predicate decodes the body exactly as the handler binds it, so a
// case variant of user_id cannot slip past the guard and still reach the
// handler's UserID.
func TestCredentialRoutes_CreateKeyCaseVariantUserIDRefused(t *testing.T) {
	f := setupCredGuardServer(t)
	for _, field := range []string{"User_ID", "USER_ID", "User_Id"} {
		t.Run(field, func(t *testing.T) {
			w := f.do(t, f.apiKey, http.MethodPost, "/api/v1/auth/api-keys", map[string]any{"name": "x", field: f.targetID})
			assert.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			keys, err := f.store.ListAPIKeysForUser(f.targetID)
			require.NoError(t, err)
			assert.Len(t, keys, 1, "no key may have been created for the target")
		})
	}
	// A key may still create a key for its own user.
	w := f.do(t, f.apiKey, http.MethodPost, "/api/v1/auth/api-keys", map[string]any{"name": "mine"})
	assert.Equal(t, http.StatusCreated, w.Code, w.Body.String())
}
