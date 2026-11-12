// file: core/internal/libraryhttp/problem.go
// version: 1.0.0
// guid: c85aad69-5513-477a-bf33-3ec7375c8eb3
// last-edited: 2026-11-11

package libraryhttp

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"

	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/rbac"
)

// problemTypeBase is httpapi's problem type base (05-http-api §3.3); httpapi
// keeps it unexported and this package may not import httpapi (same layer).
const problemTypeBase = "https://mediacore.dev/problems/"

// problem is the native error body, RFC 9457 application/problem+json with
// httpapi's extension members (05-http-api §3.3), written the way taskhttp
// writes it.
type problem struct {
	Status   int    `json:"status"`
	Code     string `json:"code"`
	Detail   string `json:"detail,omitempty"`
	Required string `json:"required_permission,omitempty"`
}

// problemBody is problem as serialised.
type problemBody struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Instance  string `json:"instance,omitempty"`
	Code      string `json:"code"`
	RequestID string `json:"request_id,omitempty"`
	Required  string `json:"required_permission,omitempty"`
}

// write sends p as application/problem+json.
func (s *Server) write(w http.ResponseWriter, r *http.Request, p problem) {
	b := problemBody{
		Type: problemTypeBase + p.Code, Title: http.StatusText(p.Status), Status: p.Status, Detail: p.Detail,
		Instance: r.URL.Path, Code: p.Code, RequestID: r.Header.Get("X-Request-Id"), Required: p.Required,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	if err := json.NewEncoder(w).Encode(b); err != nil {
		s.log.Warn("libraryhttp: write problem", "err", err)
	}
}

// forbidden is a 403 naming the requirement the caller does not satisfy.
func forbidden(need rbac.Requirement) problem {
	p := problem{Status: http.StatusForbidden, Code: "permission_denied"}
	if !need.IsZero() {
		p.Required = need.String()
	}
	return p
}

// fail answers err: the library's refusals mapped by mapError, or a 500 whose
// detail is withheld (the cause is logged).
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	p, ok := mapError(err)
	if !ok {
		s.log.Error("libraryhttp: unmapped error", "method", r.Method, "path", r.URL.Path, "err", err)
		p = problem{Status: http.StatusInternalServerError, Code: "internal"}
	}
	s.write(w, r, p)
}

// mapError maps the library's refusals to problems. It matches by errors.Is,
// so a wrapped sentinel maps as its root. The mounted and disabled refusals
// carry a fixed detail: the library's text can name the host path.
func mapError(err error) (problem, bool) {
	detail := err.Error()
	switch {
	case errors.Is(err, library.ErrUnknownRoot):
		return problem{Status: http.StatusNotFound, Code: "library.unknown_root", Detail: detail}, true
	case errors.Is(err, library.ErrNotReadable):
		return problem{Status: http.StatusForbidden, Code: "library.root_not_readable", Detail: detail}, true
	case errors.Is(err, library.ErrReservedPath):
		return problem{Status: http.StatusForbidden, Code: "library.path_hidden", Detail: detail}, true
	case errors.Is(err, library.ErrBadRel), errors.Is(err, library.ErrEscapesRoot):
		return problem{Status: http.StatusBadRequest, Code: "library.invalid_path", Detail: detail}, true
	case errors.Is(err, library.ErrNotMounted):
		return problem{Status: http.StatusConflict, Code: "library.not_mounted", Detail: "the root is not mounted"}, true
	case errors.Is(err, library.ErrRootDisabled), errors.Is(err, library.ErrOwnerDisabled):
		return problem{Status: http.StatusServiceUnavailable, Code: "library.root_disabled", Detail: "the root is disabled"}, true
	case errors.Is(err, fs.ErrNotExist):
		return problem{Status: http.StatusNotFound, Code: "library.path_not_found", Detail: detail}, true
	}
	return problem{}, false
}
