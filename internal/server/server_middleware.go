// file: internal/server/server_middleware.go
// version: 1.12.5
// guid: 6a093405-441a-4c14-a9c5-46326ea767c1
// last-edited: 2026-10-30

package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
	"github.com/gin-gonic/gin"
)

// importPathCacheTTL controls how long cachedImportPaths reuses a previously
// fetched import-path list before re-querying the store. Import paths change
// extremely infrequently (an admin action via the settings UI), so a short
// TTL-only cache is sufficient here — no invalidation hook is wired into the
// import-path mutation endpoints (MAYDEPLOY-H7). Not a const so tests can
// shrink it.
var importPathCacheTTL = 5 * time.Second

var (
	importPathCacheMu sync.Mutex
	importPathCache   []database.ImportPath
	importPathCacheAt time.Time
)

// cachedImportPaths returns store.GetAllImportPaths(), reusing the previous
// result if it was fetched within importPathCacheTTL.
func cachedImportPaths(store importPathLister) ([]database.ImportPath, error) {
	importPathCacheMu.Lock()
	defer importPathCacheMu.Unlock()
	if time.Since(importPathCacheAt) < importPathCacheTTL {
		return importPathCache, nil
	}
	paths, err := store.GetAllImportPaths()
	if err != nil {
		return nil, err
	}
	importPathCache = paths
	importPathCacheAt = time.Now()
	return paths, nil
}

func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := strings.TrimSpace(c.GetHeader("Origin"))
		allowedOrigin := ""
		isDevMode := gin.Mode() == gin.DebugMode

		if origin != "" {
			// Dev-mode CORS: allow Vite dev server only.
			if isDevMode && (origin == "http://localhost:5173" || origin == "https://localhost:5173") {
				allowedOrigin = origin
			}

			// Always allow same-origin requests.
			host := strings.TrimSpace(c.Request.Host)
			if host != "" {
				if origin == "http://"+host || origin == "https://"+host {
					allowedOrigin = origin
				}
			}
		}

		if allowedOrigin != "" {
			c.Header("Access-Control-Allow-Origin", allowedOrigin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, Authorization, Cache-Control, X-Requested-With")
			c.Header("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")
		}

		if c.Request.Method == http.MethodOptions {
			if origin != "" && allowedOrigin == "" {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// securityHeadersMiddleware sets browser security headers on every response.
// TLS-only headers (HSTS) are added only when the connection is HTTPS. The CSP
// and Permissions-Policy are set before the handler runs, so a handler that
// sets its own Content-Security-Policy keeps it (see csp.go).
func securityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("X-XSS-Protection", "0") // tell browsers to use built-in XSS filters, not legacy mode
		c.Header("Content-Security-Policy", contentSecurityPolicy())
		c.Header("Reporting-Endpoints", cspReportGroup+"=\""+cspReportPath+"\"") // report-to target for the CSP's report-to directive
		c.Header("Permissions-Policy", permissionsPolicy)

		if c.Request.TLS != nil || strings.EqualFold(strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")), "https") {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		c.Next()
	}
}

// protectedPathLog reports a path that was treated as protected because its symlinks
// could not be resolved (pathutil.UnderProtectedPath fails closed).
var protectedPathLog = logger.New("server.protected-path")

// importPathReadFailureRelog is how often an unreadable import-path list is
// reported: every path checked while the read fails is refused, so one line per
// check would flood the log during a whole-library organize.
const importPathReadFailureRelog = time.Minute

var (
	importPathReadFailureMu   sync.Mutex
	importPathReadFailureLast time.Time
)

// logImportPathReadFailure reports a failed import-path read at most once per
// importPathReadFailureRelog.
func logImportPathReadFailure(err error) {
	importPathReadFailureMu.Lock()
	defer importPathReadFailureMu.Unlock()
	if !importPathReadFailureLast.IsZero() && time.Since(importPathReadFailureLast) < importPathReadFailureRelog {
		return
	}
	importPathReadFailureLast = time.Now()
	protectedPathLog.Warn("treating paths as protected: cannot read the import-path list: error=%s",
		logger.SanitizeLogValue(err.Error()))
}

// isProtectedPath is now a method on *Server so it uses the server's
// resolved store rather than the package-level GetGlobalStore
// (SERVER-GLOBAL-STORE-AUDIT phase 3a). Nil-safe — if s.Ops() is
// nil, the import-path check is skipped (matches prior behaviour
// when GetGlobalStore returned nil). An import-path list that cannot be read
// makes every path protected (fail closed).
func (s *Server) isProtectedPath(filePath string) bool {
	absPath, _ := filepath.Abs(filePath)

	// Deluge save paths and the static protected prefixes (the cache the
	// tag-write guard uses). Until 2026-09-14 this knew only import roots and
	// the iTunes library, so the book PATCH write-back and the organizer
	// treated a seeding file as unprotected. An unloaded Deluge list is NOT
	// treated as protected here, because this also steers organize between
	// move and copy; the writers and RenameFiles refuse on it themselves.
	if s.protectedPathCache != nil && s.protectedPathCache.IsProtected(absPath) {
		return true
	}

	// The static rules, on the path as spelled and with its symlinks resolved
	// (pathutil.UnderProtectedPath): the import paths, the folders of the iTunes
	// library files, any "iTunes Media" folder and the .failed/ quarantine
	// folder. A path that cannot be resolved counts as protected, since this
	// also gates the organizer's moves.
	var roots []string
	if store := s.Ops(); store != nil {
		importPaths, err := cachedImportPaths(store)
		if err != nil {
			// Fail closed: without the import paths this check cannot tell a
			// file under one from a library file, and its callers (the
			// organizer's rename-source guard, the write-back skips) move or
			// rewrite on "not protected". Every caller treats true as "leave the
			// file alone", so refusing is right for all of them until the list
			// can be read again.
			logImportPathReadFailure(err)
			return true
		}
		for _, ip := range importPaths {
			roots = append(roots, ip.Path)
		}
	}
	if config.AppConfig.ITunes.LibraryReadPath != "" {
		roots = append(roots, filepath.Dir(config.AppConfig.ITunes.LibraryReadPath))
	}
	if config.AppConfig.ITunes.LibraryITLPath != "" {
		roots = append(roots, filepath.Dir(config.AppConfig.ITunes.LibraryITLPath))
	}
	protected, err := pathutil.UnderProtectedPath(absPath, roots)
	if err != nil && pathutil.ShouldLogProtectedPathError(err) {
		protectedPathLog.Warn("treating path as protected: cannot resolve its symlinks: path=%s error=%s",
			logger.SanitizeLogValue(filePath), logger.SanitizeLogValue(err.Error()))
	}
	return protected
}

// renameSourceVerdict is the organizer's rename-source guard: whether src may be
// moved (SetRenameSourceGuard). A protected path is SourceProtected even while the
// Deluge list is down, since that is the reason that holds for good; a path
// nothing protects is SourceListNotLoaded while the list has not loaded, so the
// organizer can say to wait for Deluge instead of calling the file protected.
func (s *Server) renameSourceVerdict(src string) organizer.SourceVerdict {
	return renameVerdict(src, s.protectedChecker(), s.isProtectedPath)
}

// renameVerdict is renameSourceVerdict with its two questions injected. Whether
// the list has loaded is read BEFORE the protected check, never after: the check
// is what can refresh the Deluge list, and a refresh that lands between the two
// reads would otherwise make a seeding file the check could not yet see look
// movable ("loaded" read true afterwards, "protected" read false before). Read
// first, a list that was down when the check began stays down in this answer, and
// loaded is sticky once true.
func renameVerdict(src string, checker tagger.PathChecker, isProtected func(string) bool) organizer.SourceVerdict {
	loaded := tagger.ProtectedListLoaded(checker)
	if isProtected(src) {
		return organizer.SourceProtected
	}
	if !loaded {
		return organizer.SourceListNotLoaded
	}
	return organizer.SourceMovable
}

func loadDismissedDedupGroups(store userPreferenceReader) map[string]bool {
	dismissed := map[string]bool{}
	pref, err := store.GetUserPreference("dedup_dismissed_groups")
	if err != nil || pref == nil || pref.Value == nil || *pref.Value == "" {
		return dismissed
	}
	var keys []string
	if err := json.Unmarshal([]byte(*pref.Value), &keys); err != nil {
		return dismissed
	}
	for _, k := range keys {
		dismissed[k] = true
	}
	return dismissed
}
