// file: internal/server/legacy_manifest.go
// version: 1.0.4
// guid: 1f86291c-166f-4f94-92a6-26ebb4bef101
// last-edited: 2026-11-08

package server

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// Books' legacy route manifest (05-http-api §8.2, 09 §9.4 R1 "Content:
// endpoints"): every gin route of the engine, with the permission, guards
// and perimeter classes core's stages will read when httpapi.MountLegacy
// serves it. The table below is reviewed data, not a derivation: R6 says
// every route names its permission and nothing is guessed.
// legacy_manifest_test.go keeps it equal to the real engine, ABS on and off,
// and checks each row's permission against what the gin chain enforces.
//
// Where each value comes from:
//
//   - pAtom(a): the route's chain runs s.perm(a). The value is
//     rbac.LegacyAtom(a); books' atom table (R1-15) resolves it to the native
//     requirement, enforced by stage 13 from the identity flip F4 (COR6-1).
//   - pNeed / pMasterList: the 20 RequireAdmin/adminRole routes, with
//     05-rbac §6.2's native permission per admin kind. The four master-list
//     routes run adminRole and s.perm(users.manage); §6.2 gives them
//     AllOf(core:users.manage, books:admin.view), which holds both legs.
//   - pAuth: authenticated with no route-level permission (raw/09 §4, the 36
//     routes), rbac.AnyAuthenticated (05-http-api §8.1 R1: R6 holds from R1).
//   - pPublic: unauthenticated: HEAD's reviewed 15 (raw/09 §2.4) and the
//     ABS surface's 11 routes that run no ABSRequireAuth.
//   - pABS: the chain runs ABSRequireAuth: OwnAuth, rbac.Delegated, and the
//     auth method books registers for it (absAuthMethod, SEC2-17).
//   - fRated: the chain includes the /api rate limiter today (RateDefault);
//     every other row is RateExempt.
//   - fInteractive: registered through credRoute (s.credRoutes "always"). A
//     credRouteWhen route ("conditional") is guarded only for some requests,
//     which Guards cannot say; gin keeps that guard, so it carries none here.
//   - fOwner: the chain runs ownerGateWhenOwnerSet (RequireOwner).
//   - fBasicExempt: HEAD's Basic-gate health exemption (basicauth.go).
//   - fNoCompress: HEAD's compressionMiddleware exclusions (server.go).
//   - fABSOnly: registered only while the ABS surface is enabled.
//
// GET / is not a manifest route: it is the "/" owner's (the SPA placeholder
// of a build without the embedded frontend; with it, gin's NoRoute serves
// "/"), which the mount's ServesSPA fallback reaches without a pattern.

// absAuthMethod is the auth method books registers for its ABS routes
// (05-authn §3.3); every OwnAuth row names it.
const absAuthMethod = "books.abs"

type legacyFlag uint16

const (
	fRated legacyFlag = 1 << iota
	fABSOnly
	fInteractive
	fOwner
	fBasicExempt
	fNoCompress
)

// legacyPerm is one row's permission source.
type legacyPerm struct {
	req     func() rbac.Requirement
	ownAuth bool
}

func pAtom(atom string) legacyPerm {
	return legacyPerm{req: func() rbac.Requirement { return rbac.LegacyAtom(atom) }}
}

func pNeed(scopes ...rbac.Permission) legacyPerm {
	return legacyPerm{req: func() rbac.Requirement { return rbac.AllOf(scopes...) }}
}

var (
	pAuth       = legacyPerm{req: func() rbac.Requirement { return rbac.AnyAuthenticated }}
	pPublic     = legacyPerm{req: func() rbac.Requirement { return rbac.Public }}
	pABS        = legacyPerm{req: func() rbac.Requirement { return rbac.Delegated(absAuthMethod) }, ownAuth: true}
	pMasterList = pNeed("core:users.manage", "books:admin.view")
)

// legacyRow is one reviewed manifest row.
type legacyRow struct {
	method, path string
	perm         legacyPerm
	flags        legacyFlag
}

func (r legacyRow) route() httpapi.LegacyRoute {
	lr := httpapi.LegacyRoute{
		Method:      r.method,
		GinPath:     r.path,
		Perm:        r.perm.req(),
		Guards:      httpapi.Guards{Interactive: r.flags&fInteractive != 0, Owner: r.flags&fOwner != 0},
		OwnAuth:     r.perm.ownAuth,
		BasicExempt: r.flags&fBasicExempt != 0,
		RateClass:   httpapi.RateExempt,
		NoCompress:  r.flags&fNoCompress != 0,
	}
	lr.Public = lr.Perm.Kind() == rbac.KindPublic
	if lr.OwnAuth {
		lr.AuthMethod = absAuthMethod
	}
	if r.flags&fRated != 0 {
		lr.RateClass = httpapi.RateDefault
	}
	return lr
}

// legacyManifest returns the manifest of an engine built with the ABS surface
// on or off, in table order.
func legacyManifest(absEnabled bool) []httpapi.LegacyRoute {
	rows := legacyManifestRows()
	out := make([]httpapi.LegacyRoute, 0, len(rows))
	for _, r := range rows {
		if r.flags&fABSOnly != 0 && !absEnabled {
			continue
		}
		out = append(out, r.route())
	}
	return out
}

// LegacyManifest returns books' legacy route manifest for this server's gin
// engine, for httpapi.LegacyMount.Routes. It walks the engine's routes and
// fails with httpapi.ErrLegacyManifest when the walk and the manifest differ
// (a gin route without an entry, or an entry without a gin route; 05-http-api
// §8.2), so a route added without a reviewed row fails boot.
func (s *Server) LegacyManifest() ([]httpapi.LegacyRoute, error) {
	m := legacyManifest(config.AppConfig.ABSAPIEnabled)
	if err := checkLegacyManifest(s.router.Routes(), m); err != nil {
		return nil, err
	}
	return m, nil
}

// checkLegacyManifest compares a gin route walk with a manifest.
func checkLegacyManifest(walk gin.RoutesInfo, m []httpapi.LegacyRoute) error {
	want := map[string]bool{}
	for _, lr := range m {
		want[lr.Method+" "+lr.GinPath] = true
	}
	var missing, extra []string
	got := map[string]bool{}
	for _, r := range walk {
		key := r.Method + " " + r.Path
		if r.Path == "/" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			continue // the "/" owner's page, not a manifest route; any other method on "/" needs a row
		}
		got[key] = true
		if !want[key] {
			missing = append(missing, key)
		}
	}
	for key := range want {
		if !got[key] {
			extra = append(extra, key)
		}
	}
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	slices.Sort(missing)
	slices.Sort(extra)
	var errs []error
	if len(missing) > 0 {
		errs = append(errs, fmt.Errorf("%w: gin routes with no manifest row (add a reviewed row to legacy_manifest.go): %s",
			httpapi.ErrLegacyManifest, strings.Join(missing, ", ")))
	}
	if len(extra) > 0 {
		errs = append(errs, fmt.Errorf("%w: manifest rows with no gin route: %s",
			httpapi.ErrLegacyManifest, strings.Join(extra, ", ")))
	}
	return errors.Join(errs...)
}

// LegacyRedirects returns the mount's Redirects: HEAD's /api/* -> /api/v1/*
// 301 with its exclusions, the same rule the gin middleware applies
// (legacyAPIRedirect; 05-http-api §4.9 item 3, 09 §9.2.4).
func (s *Server) LegacyRedirects() []httpapi.LegacyRedirect {
	return []httpapi.LegacyRedirect{{
		Prefix: "/api/",
		Location: func(r *http.Request) (string, bool) {
			return legacyAPIRedirect(r.Method, r.URL.Path)
		},
	}}
}

// legacyMatchCheck is the post-match check of 05-http-api §4.8 (SEC2-22),
// installed by setupRoutes ahead of every route-level middleware and handler
// (after NewServer's eight global ones, none of which runs a route handler).
// Once gin has routed a request that core's router handed over, a gin match
// the mux did not admit gets 404 in the legacy error shape before any handler
// runs: for a manifest pattern, anything but its entry (for a generalised
// pattern, one of its members); for the "/" fallback, anything but GET / and
// HEAD /. A request gin serves directly carries no match and passes. So does
// one gin routes to no route at all (FullPath ""): its NoRoute chain runs no
// route's handler, and gin's own answer there (the /api redirect, the SPA,
// gin's 404) keeps a mounted path's status, Content-Type and body what HEAD
// answers, a wrong method or a HEAD request on a GET-only route included.
func legacyMatchCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.FullPath() == "" {
			c.Next()
			return
		}
		if agrees, ok := httpapi.LegacyMatchAgrees(c.Request.Context(), c.Request.Method, c.FullPath()); ok && !agrees {
			httputil.RespondWithNotFound(c, "endpoint", "")
			c.Abort()
			return
		}
		c.Next()
	}
}

// legacyManifestRows is the reviewed table, sorted by path then method.
func legacyManifestRows() []legacyRow {
	return []legacyRow{
		{http.MethodPost, "/api/authorize", pABS, fABSOnly},
		{http.MethodGet, "/api/authors/:id", pABS, fABSOnly},
		{http.MethodGet, "/api/authors/:id/image", pABS, fABSOnly},
		{http.MethodHead, "/api/authors/:id/image", pABS, fABSOnly},
		{http.MethodPost, "/api/collections", pABS, fABSOnly},
		{http.MethodDelete, "/api/collections/:id", pABS, fABSOnly},
		{http.MethodGet, "/api/collections/:id", pABS, fABSOnly},
		{http.MethodPatch, "/api/collections/:id", pABS, fABSOnly},
		{http.MethodPost, "/api/collections/:id/batch/add", pABS, fABSOnly},
		{http.MethodPost, "/api/collections/:id/batch/remove", pABS, fABSOnly},
		{http.MethodPost, "/api/collections/:id/book", pABS, fABSOnly},
		{http.MethodDelete, "/api/collections/:id/book/:bookId", pABS, fABSOnly},
		{http.MethodPost, "/api/emails/send-ebook-to-device", pABS, fABSOnly},
		{http.MethodGet, "/api/events", pAuth, fNoCompress},
		{http.MethodGet, "/api/health", pPublic, fBasicExempt},
		{http.MethodGet, "/api/items/:id", pABS, fABSOnly},
		{http.MethodGet, "/api/items/:id/cover", pPublic, fABSOnly},
		{http.MethodHead, "/api/items/:id/cover", pPublic, fABSOnly},
		{http.MethodGet, "/api/items/:id/file/:ino", pABS, fABSOnly},
		{http.MethodHead, "/api/items/:id/file/:ino", pABS, fABSOnly},
		{http.MethodGet, "/api/items/:id/file/:ino/download", pABS, fABSOnly},
		{http.MethodHead, "/api/items/:id/file/:ino/download", pABS, fABSOnly},
		{http.MethodPost, "/api/items/:id/play", pABS, fABSOnly},
		{http.MethodPost, "/api/items/batch/get", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries/:libraryId", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries/:libraryId/authors", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries/:libraryId/collections", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries/:libraryId/filterdata", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries/:libraryId/items", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries/:libraryId/narrators", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries/:libraryId/personalized", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries/:libraryId/playlists", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries/:libraryId/recent-episodes", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries/:libraryId/search", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries/:libraryId/series", pABS, fABSOnly},
		{http.MethodGet, "/api/libraries/:libraryId/stats", pABS, fABSOnly},
		{http.MethodGet, "/api/me", pABS, fABSOnly},
		{http.MethodGet, "/api/me/bookmarks/:id", pABS, fABSOnly},
		{http.MethodPatch, "/api/me/item/:id/bookmark", pABS, fABSOnly},
		{http.MethodPost, "/api/me/item/:id/bookmark", pABS, fABSOnly},
		{http.MethodDelete, "/api/me/item/:id/bookmark/:time", pABS, fABSOnly},
		{http.MethodGet, "/api/me/item/:id/remove-from-continue-listening", pABS, fABSOnly},
		{http.MethodPost, "/api/me/item/:id/remove-from-continue-listening", pABS, fABSOnly},
		{http.MethodGet, "/api/me/item/listening-sessions/:id", pABS, fABSOnly},
		{http.MethodGet, "/api/me/listening-sessions", pABS, fABSOnly},
		{http.MethodGet, "/api/me/listening-stats", pABS, fABSOnly},
		{http.MethodGet, "/api/me/progress", pABS, fABSOnly},
		{http.MethodDelete, "/api/me/progress/:id", pABS, fABSOnly},
		{http.MethodGet, "/api/me/progress/:id", pABS, fABSOnly},
		{http.MethodPatch, "/api/me/progress/:id", pABS, fABSOnly},
		{http.MethodGet, "/api/me/progress/:id/remove-from-continue-listening", pABS, fABSOnly},
		{http.MethodPost, "/api/me/progress/:id/remove-from-continue-listening", pABS, fABSOnly},
		{http.MethodPatch, "/api/me/progress/batch/update", pABS, fABSOnly},
		{http.MethodGet, "/api/me/sessions", pABS, fABSOnly},
		{http.MethodDelete, "/api/me/sessions/:id", pABS, fABSOnly},
		{http.MethodGet, "/api/me/stats/year/:year", pABS, fABSOnly},
		{http.MethodGet, "/api/narrators/:id/image", pABS, fABSOnly},
		{http.MethodHead, "/api/narrators/:id/image", pABS, fABSOnly},
		{http.MethodPost, "/api/playlists", pABS, fABSOnly},
		{http.MethodDelete, "/api/playlists/:id", pABS, fABSOnly},
		{http.MethodGet, "/api/playlists/:id", pABS, fABSOnly},
		{http.MethodPatch, "/api/playlists/:id", pABS, fABSOnly},
		{http.MethodPost, "/api/playlists/:id/batch/add", pABS, fABSOnly},
		{http.MethodPost, "/api/playlists/:id/batch/remove", pABS, fABSOnly},
		{http.MethodDelete, "/api/playlists/:id/item/:libraryItemId", pABS, fABSOnly},
		{http.MethodGet, "/api/series/:id", pABS, fABSOnly},
		{http.MethodPost, "/api/session/:id/close", pABS, fABSOnly},
		{http.MethodPost, "/api/session/:id/sync", pABS, fABSOnly},
		{http.MethodPost, "/api/session/local", pABS, fABSOnly},
		{http.MethodPost, "/api/session/local-all", pABS, fABSOnly},
		{http.MethodGet, "/api/v1/activity", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/activity/clamp-summaries", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/activity/compact", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/activity/sources", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/admin/debug/book-files/:id", pNeed("books:admin.view"), fRated},
		{http.MethodPatch, "/api/v1/admin/debug/book-files/:id", pNeed("books:admin.debug"), fRated},
		{http.MethodGet, "/api/v1/admin/debug/books/:id", pNeed("books:admin.view"), fRated},
		{http.MethodPatch, "/api/v1/admin/debug/books/:id", pNeed("books:admin.debug"), fRated},
		{http.MethodGet, "/api/v1/admin/debug/edits", pNeed("books:admin.view"), fRated},
		{http.MethodPost, "/api/v1/admin/debug/edits/:edit_id/undo", pNeed("books:admin.debug"), fRated},
		{http.MethodGet, "/api/v1/admin/debug/lookup", pNeed("books:admin.view"), fRated},
		{http.MethodGet, "/api/v1/admin/guest-service-tokens", pNeed("core:admin"), fRated | fInteractive},
		{http.MethodPost, "/api/v1/admin/guest-service-tokens/:userId/revoke", pNeed("core:admin"), fRated | fInteractive | fOwner},
		{http.MethodPost, "/api/v1/admin/guest-service-tokens/:userId/reissue", pNeed("core:admin"), fRated | fInteractive | fOwner},
		{http.MethodGet, "/api/v1/admin/guest-service-tokens/plan", pNeed("core:admin"), fRated | fInteractive | fOwner},
		{http.MethodPost, "/api/v1/admin/recompact-digests", pNeed("core:admin"), fRated},
		{http.MethodGet, "/api/v1/ai-jobs", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/ai/backends/pull-model", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/ai/backends/status", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/ai/capabilities", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/ai/endpoints/status", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/ai/parse-filename", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/ai/scans", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/ai/scans", pAtom("library.edit_metadata"), fRated},
		{http.MethodDelete, "/api/v1/ai/scans/:id", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/ai/scans/:id", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/ai/scans/:id/apply", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/ai/scans/:id/cancel", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/ai/scans/:id/results", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/ai/scans/compare", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/ai/test-connection", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks", pAtom("library.view"), fRated},
		{http.MethodDelete, "/api/v1/audiobooks/:id", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id", pAtom("library.view"), fRated},
		{http.MethodPut, "/api/v1/audiobooks/:id", pAtom("library.edit_metadata"), fRated},
		{http.MethodDelete, "/api/v1/audiobooks/:id/alternative-titles", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/alternative-titles", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/alternative-titles", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/apply-metadata", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/changelog", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/changes", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/clear-no-match", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/compare-acoustid", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/cover", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/cover-history", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/cover-history/restore", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/cover-text", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/cow-versions", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/cow-versions/prune", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/discard-progress-and-purge", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/external-ids", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/extract-track-info", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/fetch-metadata", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/field-states", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/files", pAtom("library.view"), fRated},
		{http.MethodPatch, "/api/v1/audiobooks/:id/files/:file_id", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/force-rescan", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/mark-no-match", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/metadata-history", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/metadata-history/:field", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/metadata-history/:field/undo", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/metadata-rejections", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/move-segments", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/narrators", pAtom("library.view"), fRated},
		{http.MethodPut, "/api/v1/audiobooks/:id/narrators", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/organize", pAtom("library.organize"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/parse-with-ai", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/path-history", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/position", pAuth, fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/position", pAuth, fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/preview-organize", pAtom("library.organize"), fRated},
		{http.MethodDelete, "/api/v1/audiobooks/:id/quarantine", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/quarantine", pAtom("settings.manage"), fRated},
		{http.MethodPatch, "/api/v1/audiobooks/:id/rating", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/reconcile-files", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/relocate", pAtom("library.organize"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/rescan", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/restore", pAtom("library.organize"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/retired-versions", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/retired-versions/:vid/restore", pAtom("library.organize"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/revert-metadata", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/sample", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/search-metadata", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/segments", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/segments/:segmentId/tags", pAtom("library.view"), fRated},
		{http.MethodPut, "/api/v1/audiobooks/:id/set-primary", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/similar", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/split-to-books", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/split-version", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/state", pAuth, fRated},
		{http.MethodDelete, "/api/v1/audiobooks/:id/status", pAuth, fRated},
		{http.MethodPatch, "/api/v1/audiobooks/:id/status", pAuth, fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/status/repair", pAuth, fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/tags", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/tags-detailed", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/undo-last-apply", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/user-tags", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/user-tags", pAtom("library.edit_metadata"), fRated},
		{http.MethodPut, "/api/v1/audiobooks/:id/user-tags", pAtom("library.edit_metadata"), fRated},
		{http.MethodDelete, "/api/v1/audiobooks/:id/user-tags/:tag", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/:id/versions", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/versions", pAtom("library.edit_metadata"), fRated},
		{http.MethodDelete, "/api/v1/audiobooks/:id/versions/:vid", pAtom("library.delete"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/versions/:vid/purge-now", pAtom("library.delete"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/versions/:vid/restore", pAtom("library.organize"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/versions/resume-swap", pAtom("library.organize"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/:id/write-back", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/batch", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/batch-operations", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/batch-tags", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/batch-write-back", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/bulk-write-back", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/combine", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/count", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/duplicates", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/duplicates/dismiss", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/duplicates/link", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/duplicates/merge", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/duplicates/reject", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/duplicates/scan", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/duplicates/scan-results", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/facets", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/link", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/metadata/batch-apply-cached", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/metadata/cache/review", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/metadata/cache/review/selection", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/metadata/cached", pAtom("library.view"), fRated},
		{http.MethodDelete, "/api/v1/audiobooks/purge-soft-deleted", pAtom("library.delete"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/purge-soft-deleted/forget-waiting-file-records", pAtom("library.delete"), fRated},
		{http.MethodPost, "/api/v1/audiobooks/purge-soft-deleted/preview", pAtom("library.delete"), fRated},
		{http.MethodDelete, "/api/v1/audiobooks/purge-soft-deleted/preview/:id", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/purge-soft-deleted/waiting-file-records", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/purged-versions", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/quarantined", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/soft-deleted", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/swap-incomplete", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/audiobooks/trashed-versions", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/auth/accept-invite", pPublic, fRated | fNoCompress},
		{http.MethodGet, "/api/v1/auth/api-keys", pAuth, fRated},
		{http.MethodPost, "/api/v1/auth/api-keys", pAuth, fRated},
		{http.MethodDelete, "/api/v1/auth/api-keys/:id", pAuth, fRated},
		{http.MethodGet, "/api/v1/auth/api-keys/:id", pAuth, fRated},
		{http.MethodPatch, "/api/v1/auth/api-keys/:id", pAuth, fRated | fInteractive},
		{http.MethodPost, "/api/v1/auth/api-keys/:id/rotate", pAuth, fRated},
		{http.MethodPost, "/api/v1/auth/bootstrap", pPublic, fRated},
		{http.MethodGet, "/api/v1/auth/invite/:token", pPublic, fRated},
		{http.MethodPost, "/api/v1/auth/login", pPublic, fRated},
		{http.MethodPost, "/api/v1/auth/logout", pAuth, fRated},
		{http.MethodGet, "/api/v1/auth/me", pAuth, fRated},
		{http.MethodPatch, "/api/v1/auth/me", pAuth, fRated | fInteractive},
		{http.MethodPut, "/api/v1/auth/me/password", pAuth, fRated | fInteractive},
		{http.MethodGet, "/api/v1/auth/oauth-providers", pPublic, fRated},
		{http.MethodGet, "/api/v1/auth/oauth/:provider/callback", pPublic, fRated},
		{http.MethodGet, "/api/v1/auth/oauth/:provider/start", pPublic, fRated},
		{http.MethodGet, "/api/v1/auth/sessions", pAuth, fRated},
		{http.MethodDelete, "/api/v1/auth/sessions/:id", pAuth, fRated},
		{http.MethodPost, "/api/v1/auth/setup", pPublic, fRated},
		{http.MethodGet, "/api/v1/auth/status", pPublic, fRated},
		{http.MethodPost, "/api/v1/auth/temp-tokens", pAtom("users.manage"), fRated | fInteractive},
		{http.MethodGet, "/api/v1/authors", pAtom("library.view"), fRated},
		{http.MethodDelete, "/api/v1/authors/:id", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/authors/:id", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/authors/:id/aliases", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/authors/:id/aliases", pAtom("library.edit_metadata"), fRated},
		{http.MethodDelete, "/api/v1/authors/:id/aliases/:aliasId", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/authors/:id/books", pAtom("library.view"), fRated},
		{http.MethodPut, "/api/v1/authors/:id/name", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/authors/:id/reclassify-as-narrator", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/authors/:id/resolve-production", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/authors/:id/split", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/authors/:id/tags", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/authors/:id/tags", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/authors/bulk-delete", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/authors/count", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/authors/duplicates", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/authors/duplicates/refresh", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/authors/merge", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/authors/ref-audit", pAtom("library.view"), fRated},
		{http.MethodDelete, "/api/v1/backup/:filename", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/backup/create", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/backup/list", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/backup/restore", pAtom("settings.manage"), fRated | fInteractive | fOwner},
		{http.MethodGet, "/api/v1/blocked-hashes", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/blocked-hashes", pAtom("library.edit_metadata"), fRated},
		{http.MethodDelete, "/api/v1/blocked-hashes/:hash", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/books/:id/position", pAuth, fRated},
		{http.MethodPost, "/api/v1/books/:id/position", pAuth, fRated},
		{http.MethodGet, "/api/v1/books/:id/state", pAuth, fRated},
		{http.MethodDelete, "/api/v1/books/:id/status", pAuth, fRated},
		{http.MethodPatch, "/api/v1/books/:id/status", pAuth, fRated},
		{http.MethodPost, "/api/v1/books/:id/status/repair", pAuth, fRated},
		{http.MethodDelete, "/api/v1/books/:id/versions/:vid", pAtom("library.delete"), fRated},
		{http.MethodPost, "/api/v1/books/:id/versions/:vid/purge-now", pAtom("library.delete"), fRated},
		{http.MethodPost, "/api/v1/books/:id/versions/:vid/restore", pAtom("library.organize"), fRated},
		{http.MethodPost, "/api/v1/cache/invalidate", pNeed("core:admin"), fRated},
		{http.MethodGet, "/api/v1/cache/stats", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/cache/stats/history", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/cache/stats/keys", pNeed("core:system.view"), fRated},
		{http.MethodGet, "/api/v1/catalog/entries", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/catalog/entries/:id", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/collections", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/collections", pAtom("collections.manage"), fRated},
		{http.MethodDelete, "/api/v1/collections/:id", pAtom("collections.manage"), fRated},
		{http.MethodGet, "/api/v1/collections/:id", pAtom("library.view"), fRated},
		{http.MethodPut, "/api/v1/collections/:id", pAtom("collections.manage"), fRated},
		{http.MethodPost, "/api/v1/collections/:id/books", pAtom("collections.manage"), fRated},
		{http.MethodDelete, "/api/v1/collections/:id/books/:bookId", pAtom("collections.manage"), fRated},
		{http.MethodPost, "/api/v1/collections/:id/materialize", pAtom("collections.manage"), fRated},
		{http.MethodGet, "/api/v1/config", pAtom("settings.manage"), fRated},
		{http.MethodPut, "/api/v1/config", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/covers/local/:filename", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/covers/proxy", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/csp-report", pPublic, 0},
		{http.MethodGet, "/api/v1/dashboard", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/dedup/candidates", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/dedup/candidates", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/dedup/candidates/:id/breakdown", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/dedup/candidates/:id/link", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/dedup/candidates/:id/reject", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/dedup/candidates/book-pair-counts", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/dedup/candidates/bulk-count", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/dedup/candidates/bulk-link", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/dedup/candidates/bulk-reject", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/dedup/candidates/bulk-reject/revert", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/dedup/candidates/export", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/dedup/candidates/link-cluster", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/dedup/candidates/link-series", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/dedup/candidates/reject-cluster", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/dedup/candidates/remove-from-cluster", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/dedup/candidates/series-summary", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/dedup/embed", pAtom("scan.trigger"), fRated},
		{http.MethodPost, "/api/v1/dedup/fingerprint-rescan", pAtom("scan.trigger"), fRated},
		{http.MethodGet, "/api/v1/dedup/labels", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/dedup/labels/:id/override", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/dedup/labels/export", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/dedup/labels/stats", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/dedup/labels/suspicious", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/dedup/purge-stale", pAtom("scan.trigger"), fRated},
		{http.MethodPost, "/api/v1/dedup/rescore", pAtom("scan.trigger"), fRated},
		{http.MethodPost, "/api/v1/dedup/reset-acoustid", pAtom("scan.trigger"), fRated},
		{http.MethodPost, "/api/v1/dedup/scan", pAtom("scan.trigger"), fRated},
		{http.MethodPost, "/api/v1/dedup/scan-acoustid", pAtom("scan.trigger"), fRated},
		{http.MethodPost, "/api/v1/dedup/scan-llm", pAtom("scan.trigger"), fRated},
		{http.MethodGet, "/api/v1/dedup/split-book-candidates", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/dedup/split-book-candidates/:id/merge", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/dedup/split-book-candidates/bulk-merge", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/dedup/split-book-scan", pAtom("scan.trigger"), fRated},
		{http.MethodGet, "/api/v1/dedup/stats", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/dedup/validate", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/deluge/discover", pAtom("integrations.manage"), fRated},
		{http.MethodPost, "/api/v1/deluge/discover/import", pAtom("integrations.manage"), fRated},
		{http.MethodGet, "/api/v1/deluge/labels", pAtom("integrations.manage"), fRated},
		{http.MethodGet, "/api/v1/deluge/status", pAtom("integrations.manage"), fRated},
		{http.MethodPost, "/api/v1/deluge/test-connection", pAtom("integrations.manage"), fRated},
		{http.MethodGet, "/api/v1/deluge/torrents", pAtom("integrations.manage"), fRated},
		{http.MethodGet, "/api/v1/diagnostics/ai-results/:operationId", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/diagnostics/apply-suggestions", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/diagnostics/db-census", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/diagnostics/db-health", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/diagnostics/export", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/diagnostics/export/:operationId/download", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/diagnostics/fingerprint-failures", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/diagnostics/submit-ai", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/diagnostics/traces", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/diagnostics/traces/:name", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/discovery/import", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/file-ops/pending", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/filesystem/browse", pAtom("settings.manage"), fRated},
		{http.MethodDelete, "/api/v1/filesystem/exclude", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/filesystem/exclude", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/filesystem/home", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/fingerprint/worker/hello", pAtom("fingerprint.worker"), fRated},
		{http.MethodPost, "/api/v1/fingerprint/worker/lease", pAtom("fingerprint.worker"), fRated},
		{http.MethodPost, "/api/v1/fingerprint/worker/lease/:id/release", pAtom("fingerprint.worker"), fRated},
		{http.MethodPost, "/api/v1/fingerprint/worker/lease/:id/renew", pAtom("fingerprint.worker"), fRated},
		{http.MethodPost, "/api/v1/fingerprint/worker/results", pAtom("fingerprint.worker"), fRated},
		{http.MethodGet, "/api/v1/health", pPublic, fBasicExempt},
		{http.MethodGet, "/api/v1/import-paths", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/import-paths", pAtom("settings.manage"), fRated | fInteractive},
		{http.MethodDelete, "/api/v1/import-paths/:id", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/import/collision-preview", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/import/file", pAtom("scan.trigger"), fRated},
		{http.MethodGet, "/api/v1/itunes/books", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/itunes/import", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/itunes/import-status/:id", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/itunes/import-status/bulk", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/itunes/library-stats", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/itunes/library-status", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/itunes/library/download", pAtom("integrations.manage"), fRated},
		{http.MethodGet, "/api/v1/itunes/pid-integrity", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/itunes/pid-repair", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/itunes/test-mapping", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/itunes/validate", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/library/metadata-results", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/library/quick-queries", pAtom("library.view"), fRated},
		{http.MethodPut, "/api/v1/maintenance-window/config", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/maintenance-window/run", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/maintenance-window/status", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/maintenance/acoustid-stats", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/maintenance/book-file-hash-stats", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/maintenance/book-metadata-hash-stats", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/maintenance/file-intro-stats", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/maintenance/jobs", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/maintenance/jobs/:job_id", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/maintenance/repair-missing-files/:id", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/maintenance/scan-composer-tags/:id", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/maintenance/transcribe-stats", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/maintenance/wipe", pNeed("core:admin"), fRated},
		{http.MethodGet, "/api/v1/master-lists", pMasterList, fRated},
		{http.MethodPost, "/api/v1/master-lists", pMasterList, fRated},
		{http.MethodDelete, "/api/v1/master-lists/:id", pMasterList, fRated},
		{http.MethodGet, "/api/v1/master-lists/elsewhere", pMasterList, fRated},
		{http.MethodGet, "/api/v1/me/:status", pAuth, fRated},
		{http.MethodGet, "/api/v1/merge/combine-journal", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/merge/sibling-journal", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/merge/sibling-undo/:journal_id", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/merge/undo/:journal_id", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/metadata-sources/test", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/metadata/batch-apply-candidates", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/metadata/batch-fetch-candidates", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/metadata/batch-reject-candidates", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/metadata/batch-unreject-candidates", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/metadata/batch-update", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/metadata/bulk-apply-preview", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/metadata/bulk-apply-preview/:id", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/metadata/bulk-fetch", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/metadata/candidate-feedback", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/metadata/candidate-feedback", pAtom("library.edit_metadata"), fRated},
		{http.MethodDelete, "/api/v1/metadata/candidate-feedback/:id", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/metadata/export", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/metadata/fields", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/metadata/import", pAtom("library.edit_metadata"), fRated},
		{http.MethodDelete, "/api/v1/metadata/providers/throttles", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/metadata/providers/throttles", pAtom("library.view"), fRated},
		{http.MethodDelete, "/api/v1/metadata/providers/throttles/:id", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/metadata/recent-fetches", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/metadata/search", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/metadata/unreject-candidate", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/metadata/validate", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/narrators", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/narrators/count", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/op-defs", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/op-defs/:id", pAtom("library.view"), fRated},
		{http.MethodDelete, "/api/v1/openlibrary/data", pAtom("integrations.manage"), fRated},
		{http.MethodPost, "/api/v1/openlibrary/download", pAtom("integrations.manage"), fRated},
		{http.MethodPost, "/api/v1/openlibrary/import", pAtom("integrations.manage"), fRated},
		{http.MethodGet, "/api/v1/openlibrary/status", pAtom("integrations.manage"), fRated},
		{http.MethodPost, "/api/v1/openlibrary/upload", pAtom("integrations.manage"), fRated},
		{http.MethodGet, "/api/v1/operations/:id/activity", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/operations/:id/changes", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/operations/:id/class-lists/:class", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/operations/:id/result", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/operations/:id/results", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/operations/:id/revert", pAtom("library.organize"), fRated},
		{http.MethodGet, "/api/v1/operations/:id/undo/preflight", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/operations/active", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/operations/activity/merged", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/operations/assign-orphan-vgs", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/operations/audit-files", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/operations/backfill-scan-cache", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/operations/cleanup-version-groups", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/operations/clear-stale", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/operations/elect-missing-primaries", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/operations/events", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/operations/mark-broken-segments", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/operations/merge-novg-duplicates", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/operations/optimize-database", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/operations/pause", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/operations/pause", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/operations/recent", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/operations/reconcile", pAtom("scan.trigger"), fRated},
		{http.MethodGet, "/api/v1/operations/reconcile/preview", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/operations/reconcile/scan", pAtom("scan.trigger"), fRated},
		{http.MethodGet, "/api/v1/operations/reconcile/scan/latest", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/operations/resume", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/operations/set-internal-flag", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/operations/stale", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/operations/sweep-tombstones", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/operations/timeline", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/operations/v2", pAtom("scan.trigger"), fRated},
		{http.MethodDelete, "/api/v1/operations/v2/:id", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/operations/v2/:id", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/operations/v2/:id/finish", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/operations/v2/:id/logs/download", pAtom("library.view"), fRated | fNoCompress},
		{http.MethodDelete, "/api/v1/operations/v2/:id/record", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/operations/v2/:id/retry", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/playlists", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/playlists", pAuth, fRated},
		{http.MethodDelete, "/api/v1/playlists/:id", pAuth, fRated},
		{http.MethodGet, "/api/v1/playlists/:id", pAuth, fRated},
		{http.MethodPut, "/api/v1/playlists/:id", pAuth, fRated},
		{http.MethodPost, "/api/v1/playlists/:id/books", pAuth, fRated},
		{http.MethodDelete, "/api/v1/playlists/:id/books/:bookID", pAuth, fRated},
		{http.MethodGet, "/api/v1/playlists/:id/export.m3u", pAuth, fRated},
		{http.MethodPost, "/api/v1/playlists/:id/materialize", pAuth, fRated},
		{http.MethodPost, "/api/v1/playlists/:id/reorder", pAuth, fRated},
		{http.MethodGet, "/api/v1/plugins", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/plugins/:id", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/plugins/:id/disable", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/plugins/:id/enable", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/plugins/:id/health", pAtom("settings.manage"), fRated},
		{http.MethodPut, "/api/v1/plugins/:id/settings", pAtom("settings.manage"), fRated | fInteractive},
		{http.MethodGet, "/api/v1/policy/tags", pAtom("library.view"), fRated},
		{http.MethodDelete, "/api/v1/preferences/:key", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/preferences/:key", pAtom("library.view"), fRated},
		{http.MethodPut, "/api/v1/preferences/:key", pAtom("library.edit_metadata"), fRated},
		{http.MethodDelete, "/api/v1/purged-versions/:vid", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/repairs", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/repairs/:fixer/apply", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/repairs/:fixer/owner-apply", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/repairs/:fixer/plan", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/repairs/:fixer/plan/:op_id/rows", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/repairs/owner-status", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/review/bulk", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/review/count", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/review/items", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/review/items/:id/abandon-apply", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/review/items/:id/approve", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/review/items/:id/reject", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/review/replay-approved", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/search/:search_id", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/series", pAtom("library.view"), fRated},
		{http.MethodDelete, "/api/v1/series/:id", pAtom("library.delete"), fRated},
		{http.MethodPatch, "/api/v1/series/:id", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/series/:id/books", pAtom("library.view"), fRated},
		{http.MethodPut, "/api/v1/series/:id/name", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/series/:id/split", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/series/:id/tags", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/series/:id/tags", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/series/bulk-delete", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/series/count", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/series/deduplicate", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/series/duplicates", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/series/duplicates/refresh", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/series/merge", pAtom("library.edit_metadata"), fRated},
		{http.MethodPost, "/api/v1/series/prune", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/series/prune/preview", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/setup/validate-openai-key", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/signals/coverage", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/system/activity-log", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/system/announcements", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/system/factory-reset", pNeed("core:admin"), fRated | fInteractive | fOwner},
		{http.MethodGet, "/api/v1/system/logs", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/system/reset", pNeed("core:admin"), fRated | fInteractive | fOwner},
		{http.MethodGet, "/api/v1/system/status", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/system/storage", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/tags", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/tasks", pAtom("settings.manage"), fRated},
		{http.MethodPut, "/api/v1/tasks/:name", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/tasks/:name/run", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/tools", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/tools/:name/install", pAtom("settings.manage"), fRated | fInteractive},
		{http.MethodGet, "/api/v1/tools/:name/status", pAtom("settings.manage"), fRated},
		{http.MethodPost, "/api/v1/update/apply", pAtom("settings.manage"), fRated | fInteractive},
		{http.MethodPost, "/api/v1/update/check", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/update/status", pAtom("settings.manage"), fRated},
		{http.MethodGet, "/api/v1/users", pAtom("users.manage"), fRated},
		{http.MethodPost, "/api/v1/users/:id/deactivate", pAtom("users.manage"), fRated | fInteractive},
		{http.MethodPut, "/api/v1/users/:id/email", pAtom("users.manage"), fRated | fInteractive},
		{http.MethodGet, "/api/v1/users/:id/footprint", pAtom("users.manage"), fRated},
		{http.MethodPost, "/api/v1/users/:id/merge-into/:target/apply", pAtom("users.manage"), fRated | fInteractive | fOwner},
		{http.MethodPost, "/api/v1/users/:id/merge-into/:target/preview", pAtom("users.manage"), fRated | fInteractive | fOwner},
		{http.MethodPost, "/api/v1/users/:id/reactivate", pAtom("users.manage"), fRated | fInteractive},
		{http.MethodPost, "/api/v1/users/:id/reset-password", pAtom("users.manage"), fRated | fInteractive},
		{http.MethodPost, "/api/v1/users/invite", pAtom("users.manage"), fRated | fInteractive},
		{http.MethodGet, "/api/v1/users/invites", pAtom("users.manage"), fRated},
		{http.MethodDelete, "/api/v1/users/invites/:id", pAtom("users.manage"), fRated},
		{http.MethodPost, "/api/v1/users/invites/:id/resend", pAtom("users.manage"), fRated | fInteractive},
		{http.MethodGet, "/api/v1/users/merges", pAtom("users.manage"), fRated},
		{http.MethodPost, "/api/v1/users/merges/:jid/resume", pAtom("users.manage"), fRated | fInteractive | fOwner},
		{http.MethodPost, "/api/v1/users/merges/:jid/undo", pAtom("users.manage"), fRated | fInteractive | fOwner},
		{http.MethodGet, "/api/v1/version-groups/:id", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/work", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/work/stats", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v1/works", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v1/works", pAtom("library.edit_metadata"), fRated},
		{http.MethodDelete, "/api/v1/works/:id", pAtom("library.delete"), fRated},
		{http.MethodGet, "/api/v1/works/:id", pAtom("library.view"), fRated},
		{http.MethodPut, "/api/v1/works/:id", pAtom("library.edit_metadata"), fRated},
		{http.MethodGet, "/api/v1/works/:id/books", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v3/ops/counts", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v3/ops/defs", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v3/ops/defs/:id", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v3/ops/defs/:id/runs", pAtom("scan.trigger"), fRated},
		{http.MethodGet, "/api/v3/ops/events", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v3/ops/runs", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v3/ops/runs", pAtom("scan.trigger"), fRated},
		{http.MethodGet, "/api/v3/ops/runs/:id", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v3/ops/runs/:id/approve", pAtom("library.view"), fRated},
		{http.MethodPost, "/api/v3/ops/runs/:id/cancel", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v3/ops/runs/:id/rows", pAtom("library.view"), fRated},
		{http.MethodGet, "/api/v3/ops/timeline", pAtom("library.view"), fRated},
		{http.MethodGet, "/auth/openid", pPublic, fABSOnly},
		{http.MethodGet, "/auth/openid/browser", pPublic, fABSOnly},
		{http.MethodPost, "/auth/openid/browser", pPublic, fABSOnly},
		{http.MethodGet, "/auth/openid/callback", pPublic, fABSOnly},
		{http.MethodPost, "/auth/refresh", pPublic, fABSOnly},
		{http.MethodGet, "/auth/temp-login", pPublic, 0},
		{http.MethodGet, "/health", pPublic, 0},
		{http.MethodPost, "/login", pPublic, fABSOnly},
		{http.MethodPost, "/logout", pABS, fABSOnly},
		{http.MethodGet, "/metrics", pAuth, fNoCompress},
		{http.MethodGet, "/ping", pPublic, fABSOnly},
		{http.MethodGet, "/public/session/:id/track/:index", pPublic, fABSOnly},
		{http.MethodGet, "/status", pPublic, fABSOnly},
	}
}
