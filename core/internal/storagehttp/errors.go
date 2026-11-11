// file: core/internal/storagehttp/errors.go
// version: 1.0.0
// guid: f79b3917-b15b-4227-afd0-b62c9c801761
// last-edited: 2026-11-10

package storagehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/falkcorp/mediacore/httpapi"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// problemTypeBase is the stable URI prefix of every problem type, the same
// httpapi writes.
const problemTypeBase = "https://mediacore.dev/problems/"

// problemBody is httpapi.Problem plus the two members 05-storage §5S.3.15
// names for library_files_confirm_required: the fresh library root and top-level
// entry count the client must confirm. httpapi.Error has no place for an
// extension member, so this route writes its failures itself
// (httpapi.HandleRaw) and keeps every other member exactly as httpapi writes
// it. LibraryRoot is a host path: the spec names it ("naming the current root
// and count") and the UI shows it in the confirmation dialog, and it is only
// ever the root core.paths already makes the operator's own.
type problemBody struct {
	httpapi.Problem
	LibraryRoot      string `json:"library_root,omitempty"`
	LibraryFileCount *int   `json:"library_file_count,omitempty"`
}

// Error makes a *problemBody an error, so the handler returns one value type.
func (pb *problemBody) Error() string { return "storagehttp: " + pb.Code }

// problem builds a *problemBody.
func problem(status int, code, detail string, fields ...httpapi.FieldError) *problemBody {
	return &problemBody{Problem: httpapi.Problem{Status: status, Code: code, Detail: detail, Errors: fields}}
}

// writeProblem completes pb as httpapi's writeProblem does (type, title,
// instance, request ID) and writes it as application/problem+json.
func writeProblem(w http.ResponseWriter, r *http.Request, pb problemBody, log *slog.Logger) {
	if pb.Code == "" {
		pb.Code = "error"
	}
	pb.Type = problemTypeBase + pb.Code
	pb.Title = http.StatusText(pb.Status)
	pb.Instance = r.URL.Path
	pb.RequestID = httpapi.RequestID(r.Context())
	writeJSON(r.Context(), w, pb.Status, "application/problem+json", pb, log)
}

// writeJSON encodes v before writing any header, so an encode failure is
// still a clean 500.
func writeJSON(ctx context.Context, w http.ResponseWriter, status int, contentType string, v any, log *slog.Logger) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		if log != nil {
			log.ErrorContext(ctx, "storagehttp: encode response", "error", err)
		}
		http.Error(w, `{"code":"internal","status":500}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if _, err := w.Write(buf.Bytes()); err != nil && log != nil {
		log.DebugContext(ctx, "storagehttp: write response", "error", err)
	}
}

// fail writes err: a *problemBody as itself, anything else through httpapi
// (an *httpapi.Error as its status, a plain error as the 500 with the cause
// withheld from the body).
func fail(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	if pb, ok := errors.AsType[*problemBody](err); ok {
		writeProblem(w, r, *pb, log)
		return
	}
	httpapi.WriteError(w, r, err, log)
}

// ---- request decoding -----------------------------------------------------

// maxBody bounds the request: five short members.
const maxBody = 4 << 10

// isJSON reports whether a Content-Type header names application/json.
func isJSON(h string) bool {
	mt, _, _ := strings.Cut(h, ";")
	return strings.EqualFold(strings.TrimSpace(mt), "application/json")
}

// malformed is a 400 malformed_json with fixed text.
func malformed(detail string) *httpapi.Error {
	return &httpapi.Error{Status: http.StatusBadRequest, Code: "malformed_json", Detail: detail}
}

// decodeRequest reads r's body strictly into a storage.ResetRequest, the one
// type every caller passes unchanged (R9-6): application/json only, a bounded
// body, no unknown member, no trailing data. kind defaults to factory and
// delete_library_files to false when omitted (05-storage §5S.3.15, A-17: the
// wipe is never a default side effect). The body is not echoed in any answer,
// nor is encoding/json's text, which quotes members.
func decodeRequest(w http.ResponseWriter, r *http.Request) (storage.ResetRequest, error) {
	req := storage.ResetRequest{Kind: restorejournal.ResetFactory}
	if !isJSON(r.Header.Get("Content-Type")) {
		return req, &httpapi.Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type",
			Detail: "Content-Type must be application/json"}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		if _, big := errors.AsType[*http.MaxBytesError](err); big {
			return req, &httpapi.Error{Status: http.StatusRequestEntityTooLarge, Code: "body_too_large", Detail: "the body is too large"}
		}
		return req, malformed("the body could not be read")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		if te, ok := errors.AsType[*json.UnmarshalTypeError](err); ok && te.Field != "" {
			return req, httpapi.Invalid(httpapi.Field("/body/"+te.Field, "invalid_type", "has the wrong JSON type"))
		}
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			return req, &httpapi.Error{Status: http.StatusBadRequest, Code: "unknown_field",
				Detail: "the body has a member this route does not accept",
				Fields: []httpapi.FieldError{httpapi.Field("/body", "unknown_field", "only kind, confirm, delete_library_files, library_root and library_file_count are accepted")}}
		}
		return req, malformed("the body is not a JSON object")
	}
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return req, malformed("trailing data after the JSON value")
	}
	return req, nil
}

// ---- the reset's refusals -------------------------------------------------

// mapResetError maps the sentinels of a refused reset, whether req.Validate or
// the Stager returned them, to the statuses 05-storage §5S.3.15 fixes. Every
// text here is fixed: the errors' own text can carry a host path (a dangerous
// root, a hook's refusal) and is never sent. A second result of false means the
// error is not a refusal, and the router answers it 500 with the cause
// withheld.
func mapResetError(err error) (*problemBody, bool) {
	if lc, ok := errors.AsType[*storage.LibraryConfirmError](err); ok {
		pb := problem(http.StatusUnprocessableEntity, "library_files_confirm_required",
			"deleting library files needs the library root and top-level entry count confirmed; the problem carries the current values and nothing was staged",
			httpapi.Field("/body/library_root", "mismatch", "must equal the current library root"),
			httpapi.Field("/body/library_file_count", "mismatch", "must equal the current top-level entry count"))
		pb.LibraryRoot, pb.LibraryFileCount = lc.Root, &lc.Files
		return pb, true
	}
	switch {
	case errors.Is(err, storage.ErrLibraryConfirmRequired):
		// A wrapped sentinel with no fresh values: the refusal stands, the hint
		// is lost.
		return problem(http.StatusUnprocessableEntity, "library_files_confirm_required",
			"deleting library files needs the library root and top-level entry count confirmed; nothing was staged",
			httpapi.Field("/body/library_root", "mismatch", "must equal the current library root"),
			httpapi.Field("/body/library_file_count", "mismatch", "must equal the current top-level entry count")), true
	case errors.Is(err, storage.ErrResetConfirmRequired):
		return problem(http.StatusUnprocessableEntity, "confirm_required", `the request must carry "confirm": "RESET"`,
			httpapi.Field("/body/confirm", "required", `must be "RESET"`)), true
	case errors.Is(err, storage.ErrResetRequestInvalid):
		return problem(http.StatusUnprocessableEntity, "reset_request_invalid",
			"kind must be factory or store, delete_library_files needs kind factory, and a deployment with no reset hook cannot delete library files"), true
	case errors.Is(err, storage.ErrResetRefused):
		return problem(http.StatusBadRequest, "reset_refused", "the application refused the factory reset; nothing was staged"), true
	case errors.Is(err, storage.ErrSwapPending):
		return problem(http.StatusConflict, "swap_pending", "a restore or reset is already staged or running"), true
	case errors.Is(err, storage.ErrRenameRefused):
		return problem(http.StatusConflict, "rename_refused", "a reset target cannot be set aside; nothing was staged"), true
	}
	return nil, false
}
