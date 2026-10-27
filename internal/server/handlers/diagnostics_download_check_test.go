// file: internal/server/handlers/diagnostics_download_check_test.go
// version: 1.0.0
// guid: 8e3b7c24-1d56-4f90-a2b8-6c0d4e9f1a73
// last-edited: 2026-10-26

package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	databasemocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers/opaccess"
)

// sequencedOpStore answers GetOperationV2 with rows in order (the last one
// repeats) and counts the reads, so a test can tell which read a handler used
// and how many it made. database.Store is embedded, nil: any other method panics,
// which is the point of a store that is only asked for the operation row. The
// one exception is GetBookByID, the first read an apply of suggestions makes
// once its checks pass: it fails with an error, so a handler that wrongly lets a
// request through answers with an error status the test can assert on instead of
// crashing the test binary inside the merge service.
type sequencedOpStore struct {
	database.Store
	rows  []*database.OperationV2Row
	reads int
}

func (s *sequencedOpStore) GetOperationV2(string) (*database.OperationV2Row, error) {
	i := min(s.reads, len(s.rows)-1)
	s.reads++
	return s.rows[i], nil
}

func (s *sequencedOpStore) GetBookByID(string) (*database.Book, error) {
	return nil, errors.New("not wired: the request got past the checks it should have stopped at")
}

// completedExportRow is a finished export whose archive is a real file holding
// body, and the path it sits at.
func completedExportRow(t *testing.T, id, body string, narrowed *[]string) *database.OperationV2Row {
	t.Helper()
	zip := filepath.Join(t.TempDir(), id+".zip")
	require.NoError(t, os.WriteFile(zip, []byte(body), 0o600))
	raw, err := json.Marshal(diagnostics.ExportResult{ZipPath: zip, NarrowedTo: narrowed})
	require.NoError(t, err)
	res := string(raw)
	return &database.OperationV2Row{ID: id, DefID: handlers.DiagnosticsExportDefID, Status: "completed", ResultData: &res}
}

func downloadVia(h *handlers.DiagnosticsHandler, id string) (int, string) {
	c, w := newDiagCtx(http.MethodGet, "/diagnostics/export/"+id+"/download", "",
		gin.Params{{Key: "operationId", Value: id}})
	h.DownloadExport(c)
	return w.Code, w.Body.String()
}

func withAuth(t *testing.T, on bool) {
	t.Helper()
	orig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = orig })
	config.AppConfig.EnableAuth = on
}

// The scope decision is made on the row DownloadExport serves: the handler reads
// the operation once, hands that row and its parsed result to the check, and
// serves the file only when the check allows it. The check's refusal is a 403
// and its failure a 500; neither serves the archive.
func TestDownloadExport_TheCheckDecidesOnTheRowServed(t *testing.T) {
	withAuth(t, true)
	scope := []string{string(auth.PermSettingsManage)}
	const body = "PK-invented-archive-for-year-one"

	t.Run("allowed", func(t *testing.T) {
		row := completedExportRow(t, "op-year-one", body, &scope)
		store := &sequencedOpStore{rows: []*database.OperationV2Row{row}}
		var seen *database.OperationV2Row
		var seenRes diagnostics.ExportResult
		h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil).WithDownloadCheck(
			func(_ *gin.Context, r *database.OperationV2Row, res diagnostics.ExportResult) (string, error) {
				seen, seenRes = r, res
				return "", nil
			})
		code, got := downloadVia(h, "op-year-one")
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, body, got)
		assert.Same(t, row, seen, "the check is handed the row that is served")
		require.NotNil(t, seenRes.NarrowedTo)
		assert.Equal(t, scope, *seenRes.NarrowedTo, "and the result parsed from it")
		assert.Equal(t, 1, store.reads, "the row is read once")
	})

	t.Run("refused", func(t *testing.T) {
		row := completedExportRow(t, "op-knightfall", body, &scope)
		store := &sequencedOpStore{rows: []*database.OperationV2Row{row}}
		h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil).WithDownloadCheck(
			func(*gin.Context, *database.OperationV2Row, diagnostics.ExportResult) (string, error) {
				return "built for an account that holds more than you do", nil
			})
		code, got := downloadVia(h, "op-knightfall")
		assert.Equal(t, http.StatusForbidden, code)
		assert.Contains(t, got, "holds more than you do")
		assert.NotContains(t, got, body, "the archive is not served")
	})

	t.Run("cannot decide", func(t *testing.T) {
		row := completedExportRow(t, "op-hush", body, &scope)
		store := &sequencedOpStore{rows: []*database.OperationV2Row{row}}
		h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil).WithDownloadCheck(
			func(*gin.Context, *database.OperationV2Row, diagnostics.ExportResult) (string, error) {
				return "", errors.New("count users: pebble: closed")
			})
		code, got := downloadVia(h, "op-hush")
		assert.Equal(t, http.StatusInternalServerError, code, "a check that cannot decide does not pass")
		assert.NotContains(t, got, body)
	})
}

// A row that is not a completed export with a result is never handed to the
// check, and never served: another operation's id is a 404, a missing row a 404,
// and a half-written one an error.
func TestDownloadExport_OnlyAnExportRowReachesTheCheck(t *testing.T) {
	withAuth(t, true)
	called := 0
	check := func(*gin.Context, *database.OperationV2Row, diagnostics.ExportResult) (string, error) {
		called++
		return "", nil
	}
	scope := []string{}

	other := completedExportRow(t, "op-batman-plan", "PK-other", &scope)
	other.DefID = "repairs.plan"
	h := handlers.NewDiagnosticsHandler(&sequencedOpStore{rows: []*database.OperationV2Row{other}}, nil, nil, nil, nil).WithDownloadCheck(check)
	code, body := downloadVia(h, "op-batman-plan")
	assert.Equal(t, http.StatusNotFound, code, "another operation's result is not an export")
	assert.NotContains(t, body, "PK-other")

	h = handlers.NewDiagnosticsHandler(&sequencedOpStore{rows: []*database.OperationV2Row{nil}}, nil, nil, nil, nil).WithDownloadCheck(check)
	code, _ = downloadVia(h, "op-gone")
	assert.Equal(t, http.StatusNotFound, code, "no row, no archive")

	noResult := &database.OperationV2Row{ID: "op-nores", DefID: handlers.DiagnosticsExportDefID, Status: "completed"}
	h = handlers.NewDiagnosticsHandler(&sequencedOpStore{rows: []*database.OperationV2Row{noResult}}, nil, nil, nil, nil).WithDownloadCheck(check)
	code, _ = downloadVia(h, "op-nores")
	assert.Equal(t, http.StatusInternalServerError, code, "a completed export with no result is not served")

	assert.Zero(t, called, "the check ran on none of them")

	store := databasemocks.NewMockStore(t)
	store.EXPECT().GetOperationV2("op-err").Return(nil, errors.New("pebble: closed"))
	h = handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil).WithDownloadCheck(check)
	code, _ = downloadVia(h, "op-err")
	assert.Equal(t, http.StatusInternalServerError, code, "a store failure is a 500, not a pass")
	assert.Zero(t, called)
}

// With auth on and no check wired, every download is refused; with auth off an
// unwired handler serves, as a single-user install has no scope to compare.
func TestDownloadExport_UnwiredCheckRefusesWhenAuthIsOn(t *testing.T) {
	const body = "PK-invented-archive-for-the-court-of-owls"
	scope := []string{string(auth.PermSettingsManage)}
	row := completedExportRow(t, "op-owls", body, &scope)

	withAuth(t, true)
	h := handlers.NewDiagnosticsHandler(&sequencedOpStore{rows: []*database.OperationV2Row{row}}, nil, nil, nil, nil)
	code, got := downloadVia(h, "op-owls")
	assert.Equal(t, http.StatusForbidden, code)
	assert.NotContains(t, got, body)

	config.AppConfig.EnableAuth = false
	code, got = downloadVia(h, "op-owls")
	assert.Equal(t, http.StatusOK, code, "auth off: nothing to compare")
	assert.Equal(t, body, got)
}

// The deterministic form of the race the middleware had. The row reads as running
// the first time and completed the second: a check that read the row on its own
// (the old middleware) saw "running" and let the request through, and the
// handler's own read then served the completed export unchecked. The handler now
// reads once, so it answers from the first read (still running: 202, no archive)
// and a second read cannot change what is served.
func TestDownloadExport_ARowThatChangesBetweenReadsIsNeverServedUnchecked(t *testing.T) {
	withAuth(t, true)
	const body = "PK-invented-archive-for-arkham"
	scope := []string{string(auth.PermUsersManage)}
	running := &database.OperationV2Row{ID: "op-arkham", DefID: handlers.DiagnosticsExportDefID, Status: "running"}
	done := completedExportRow(t, "op-arkham", body, &scope)
	store := &sequencedOpStore{rows: []*database.OperationV2Row{running, done}}

	called := 0
	h := handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil).WithDownloadCheck(
		func(*gin.Context, *database.OperationV2Row, diagnostics.ExportResult) (string, error) {
			called++
			return "built for an account that holds more than you do", nil
		})
	code, got := downloadVia(h, "op-arkham")
	assert.Equal(t, http.StatusAccepted, code)
	assert.NotContains(t, got, body, "the completed read was never used to serve")
	assert.Equal(t, 1, store.reads, "one read of the row per request")

	// And when the first read is the completed one, the check decides on it.
	store = &sequencedOpStore{rows: []*database.OperationV2Row{done, running}}
	h = handlers.NewDiagnosticsHandler(store, nil, nil, nil, nil).WithDownloadCheck(
		func(*gin.Context, *database.OperationV2Row, diagnostics.ExportResult) (string, error) {
			called++
			return "built for an account that holds more than you do", nil
		})
	code, got = downloadVia(h, "op-arkham")
	assert.Equal(t, http.StatusForbidden, code)
	assert.NotContains(t, got, body)
	assert.Equal(t, 1, called)
	assert.Equal(t, 1, store.reads)
}

// The 202 body's progress message is free text of the operation, read through
// the caller's text rule: withheld when it names a user and the caller may not
// read users' data (and when no reader is wired), shown whole to users.manage.
func TestDownloadExport_ProgressMessageGoesThroughTheTextRule(t *testing.T) {
	withAuth(t, false)
	const naming = "export failed for user_id=01INVENTEDOWNER0000000000A"
	row := &database.OperationV2Row{ID: "op-gcpd", DefID: handlers.DiagnosticsExportDefID, Status: "running", ProgressMessage: naming}
	newHandler := func() *handlers.DiagnosticsHandler {
		return handlers.NewDiagnosticsHandler(&sequencedOpStore{rows: []*database.OperationV2Row{row}}, nil, nil, nil, nil)
	}
	holding := func(perms ...auth.Permission) *opaccess.Repairs {
		return opaccess.NewRepairs(nil, nil, func(_ *gin.Context, p auth.Permission) bool {
			for _, q := range perms {
				if p == q {
					return true
				}
			}
			return false
		})
	}

	_, got := downloadVia(newHandler(), "op-gcpd")
	assert.NotContains(t, got, "01INVENTEDOWNER0000000000A", "unwired: withheld")

	_, got = downloadVia(newHandler().WithOpAccess(holding(auth.PermSettingsManage)), "op-gcpd")
	assert.NotContains(t, got, "01INVENTEDOWNER0000000000A", "settings.manage alone does not read users' data")

	_, got = downloadVia(newHandler().WithOpAccess(holding(auth.PermSettingsManage, auth.PermUsersManage)), "op-gcpd")
	assert.Contains(t, got, "01INVENTEDOWNER0000000000A", "users.manage reads it whole")
}

// POST /diagnostics/apply-suggestions answers only for a completed diagnostics AI
// run, as GET /diagnostics/ai-results/:id does: another operation's id (a Repairs
// run whose result happens to hold a "suggestions" key) is a 404 and nothing in
// it is read or applied, and a run that has not completed has no suggestions
// to apply yet.
func TestApplySuggestions_OnlyACompletedAIRunAnswers(t *testing.T) {
	const result = `{"suggestions":[{"id":"s1","action":"merge_versions","book_ids":["b1","b2"],"primary_id":"b1"}]}`
	apply := func(op *database.OperationV2Row) (code int, body string) {
		// A handler that wrongly lets the request through reaches dependencies
		// this test does not wire; report that as a wrong answer, not a panic.
		defer func() {
			if recover() != nil {
				code, body = -1, "panicked past the checks"
			}
		}()
		h := handlers.NewDiagnosticsHandler(&sequencedOpStore{rows: []*database.OperationV2Row{op}}, nil, nil, nil, nil)
		c, w := newDiagCtx(http.MethodPost, "/diagnostics/apply-suggestions",
			`{"operation_id":"`+op.ID+`","approved_suggestion_ids":["s1"]}`, nil)
		h.ApplySuggestions(c)
		return w.Code, w.Body.String()
	}
	rd := result

	code, body := apply(&database.OperationV2Row{ID: "op-repairs", DefID: "repairs.apply", Status: "completed", ResultData: &rd})
	assert.Equal(t, http.StatusNotFound, code, "a Repairs run is not an analysis")
	assert.NotContains(t, body, "suggestion")

	code, body = apply(&database.OperationV2Row{ID: "op-scan", DefID: "library.scan", Status: "completed", ResultData: &rd})
	assert.Equal(t, http.StatusNotFound, code, "nor is any other operation")
	assert.NotContains(t, body, "suggestion")

	for _, status := range []string{"queued", "running", "failed", "canceled"} {
		code, _ = apply(&database.OperationV2Row{ID: "op-ai", DefID: handlers.DiagnosticsAIDefID, Status: status, ResultData: &rd})
		assert.Equal(t, http.StatusConflict, code, "a %s analysis has no suggestions to apply", status)
	}
}
