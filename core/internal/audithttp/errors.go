// file: core/internal/audithttp/errors.go
// version: 1.0.0
// guid: 4cf6fc79-4e74-4c40-b8b8-2ee55a7b9b47
// last-edited: 2026-11-10

package audithttp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/rbac"
)

// unauthenticated is the 401 05-authn §4.11 gives the audit routes for the
// auth-disabled principal, and what a route reached without any principal
// answers (a mounting mistake: stage 13 never passes one).
func unauthenticated() error {
	return &httpapi.Error{Status: http.StatusUnauthorized, Code: "authentication_required", Detail: "authentication is required"}
}

func unavailable() error {
	return httpapi.Unavailable("the audit log is not available", 5*time.Second)
}

// guard is every handler's first line, in this order: a request with no
// principal, or the auth-disabled one (User nil, Method auth_disabled), is a
// 401 whatever its checker holds (the audit log holds user names, addresses
// and key ids, which an anonymous caller must not read); a route mounted
// without its reader is a 503; a principal whose checker lacks core:audit.view
// is a 403. Perm is the router's to enforce at stage 13 (F4-08); this makes
// the handler fail closed when it is reached without it.
func (a *api) guard(p *auth.Principal) error {
	switch {
	case p == nil || p.User == nil || p.Method == auth.MethodAuthDisabled:
		return unauthenticated()
	case a.reader == nil:
		return unavailable()
	case p.Perms == nil || !p.Perms.Has(permView) || !p.Perms.Satisfies(rbac.Need(permView)):
		return httpapi.Forbidden(permView)
	}
	return nil
}

// storeError is what a failed read answers: a context that ended passes
// through (the client left), anything else is a 503 whose cause goes to the
// log and never to the client.
func (a *api) storeError(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	a.log.ErrorContext(ctx, "audithttp: reading the audit log failed", slog.Any("error", err))
	return unavailable()
}

// isNoFlush is a ResponseWriter that cannot flush: the stream still goes out
// when the handler returns, so it is not a failure.
func isNoFlush(err error) bool { return errors.Is(err, http.ErrNotSupported) }
