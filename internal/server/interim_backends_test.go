// file: internal/server/interim_backends_test.go
// version: 0.1.0
// guid: f7e34fd7-50ee-457e-93dc-7cc281bd2717
// last-edited: 2026-11-28

package server

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/health/diagnostics"
	coreai "github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	diag "github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// interimRun is a FakeRun whose mode, actor and id the cell chooses.
type interimRun struct {
	*taskstest.FakeRun
	mode  tasks.Mode
	actor tasks.Actor
	id    tasks.RunID
}

func (r interimRun) Mode() tasks.Mode   { return r.mode }
func (r interimRun) Actor() tasks.Actor { return r.actor }
func (r interimRun) RunID() tasks.RunID { return r.id }

func newInterimRC(t *testing.T, kind tasks.Kind, mode tasks.Mode, actor tasks.Actor, id string) *tasks.RC {
	t.Helper()
	return tasks.NewRC(interimRun{FakeRun: taskstest.NewFakeRun(t.Context(), kind), mode: mode, actor: actor, id: tasks.RunID(id)})
}

// A run that starts before the server exists is refused by every backend, not
// served from a nil.
func TestInterimBackends_NoServerRefusesEveryRun(t *testing.T) {
	b := NewInterimBackends(func() *Server { return nil }, nil)
	rc := newInterimRC(t, "core.test", tasks.ModeLive, tasks.Actor{UserID: "bruce"}, "run-1")

	_, err := b.Holds.ReleaseHolds(rc, providerhttp.ReleaseHoldsParams{})
	assert.ErrorIs(t, err, ErrInterimNoServer, "holds")
	_, err = b.Poll.PollBatches(rc, coreai.BatchPollParams{})
	assert.ErrorIs(t, err, ErrInterimNoServer, "poll")
	_, err = b.Export.Export(rc, diagnostics.ExportParams{ActorID: "bruce"})
	assert.ErrorIs(t, err, ErrInterimNoServer, "export")
}

// ---- release-holds ----------------------------------------------------------

type fakeHolds struct {
	holds    []metadata.ProviderThrottle
	failFor  map[string]error
	gone     map[string]bool
	released []string
}

func (f *fakeHolds) List() []metadata.ProviderThrottle { return f.holds }
func (f *fakeHolds) ReleaseHold(id string) (metadata.ProviderThrottle, bool, error) {
	if err := f.failFor[id]; err != nil {
		return metadata.ProviderThrottle{}, false, err
	}
	if f.gone[id] {
		return metadata.ProviderThrottle{}, false, nil
	}
	f.released = append(f.released, id)
	return metadata.ProviderThrottle{ProviderID: id}, true, nil
}

func holdsFixture() *fakeHolds {
	until := time.Now().Add(time.Hour)
	return &fakeHolds{holds: []metadata.ProviderThrottle{
		{ProviderID: "audible", Reason: "rate_limit", Until: until},
		{ProviderID: "openlibrary", Reason: "quota", Until: until},
		{ProviderID: "googlebooks", Reason: "quota", Until: until},
	}}
}

func TestInterimHolds_LiveReleasesEveryHold(t *testing.T) {
	f := holdsFixture()
	f.gone = map[string]bool{"openlibrary": true} // expired between the listing and the release
	res, err := releaseHolds(newInterimRC(t, "core.integrations.release-holds", tasks.ModeLive, tasks.Actor{UserID: "bruce"}, "r"), f)
	require.NoError(t, err)
	assert.Equal(t, providerhttp.ReleaseHoldsResult{Listed: 3, Released: 2}, res)
	assert.Equal(t, []string{"audible", "googlebooks"}, f.released)
}

// A Preview run lists and releases nothing.
func TestInterimHolds_PreviewReleasesNothing(t *testing.T) {
	f := holdsFixture()
	res, err := releaseHolds(newInterimRC(t, "core.integrations.release-holds", tasks.ModePreview, tasks.Actor{UserID: "bruce"}, "r"), f)
	require.NoError(t, err)
	assert.Equal(t, providerhttp.ReleaseHoldsResult{Listed: 3}, res)
	assert.Empty(t, f.released, "a Preview run released a hold")
}

// One hold that cannot be released fails the run after the others were tried.
func TestInterimHolds_AFailedReleaseFailsTheRunAfterTryingTheRest(t *testing.T) {
	f := holdsFixture()
	f.failFor = map[string]error{"audible": errors.New("store closed")}
	res, err := releaseHolds(newInterimRC(t, "core.integrations.release-holds", tasks.ModeLive, tasks.Actor{UserID: "bruce"}, "r"), f)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 of 3")
	assert.Equal(t, 2, res.Released, "the holds after the failed one are still released")
}

// A cancelled run stops before the next release.
func TestInterimHolds_CancelStopsBeforeTheNextRelease(t *testing.T) {
	f := holdsFixture()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rc := tasks.NewRC(interimRun{FakeRun: taskstest.NewFakeRun(ctx, "core.x"), mode: tasks.ModeLive, actor: tasks.Actor{UserID: "bruce"}, id: "r"})
	_, err := releaseHolds(rc, f)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, f.released)
}

// ---- ai batch poll ----------------------------------------------------------

type fakePoller struct {
	n   int
	err error
}

func (f fakePoller) Poll(context.Context) (int, error) { return f.n, f.err }

func TestInterimPoll_NoPollerIsARunThatDidNothing(t *testing.T) {
	rc := newInterimRC(t, "core.ai.batch-poll", tasks.ModeLive, tasks.Actor{System: "scheduler"}, "r")
	res, err := pollBatches(rc, nil)
	require.NoError(t, err)
	assert.Equal(t, coreai.BatchPollResult{}, res)

	// Through the adapter, on a server that has no batch poller: a nil
	// *BatchPoller held in the interface must not be called.
	b := NewInterimBackends(func() *Server { return &Server{} }, nil)
	res, err = b.Poll.PollBatches(rc, coreai.BatchPollParams{})
	require.NoError(t, err)
	assert.Equal(t, coreai.BatchPollResult{}, res)
}

func TestInterimPoll_ReportsPolledAndPassesTheError(t *testing.T) {
	rc := newInterimRC(t, "core.ai.batch-poll", tasks.ModeLive, tasks.Actor{System: "scheduler"}, "r")
	res, err := pollBatches(rc, fakePoller{n: 4})
	require.NoError(t, err)
	assert.Equal(t, 4, res.Polled)

	boom := errors.New("openai: 502")
	res, err = pollBatches(rc, fakePoller{n: 1, err: boom})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 1, res.Polled)
}

// ---- diagnostics export -----------------------------------------------------

// zipEntry reads one file of a zip.
func zipEntry(t *testing.T, path, name string) string {
	t.Helper()
	r, err := zip.OpenReader(path)
	require.NoError(t, err)
	defer r.Close()
	for _, zf := range r.File {
		if zf.Name != name {
			continue
		}
		rc, err := zf.Open()
		require.NoError(t, err)
		defer rc.Close()
		b, err := io.ReadAll(rc)
		require.NoError(t, err)
		return string(b)
	}
	t.Fatalf("%s is not in %s", name, path)
	return ""
}

// The adapter narrows the bundle to the account that started the run exactly as
// HEAD's diagnostics.export op does: the same account through HEAD's path
// (opActorHolds over the op row) and through the adapter (the run's stamped
// actor) gets the same operations.json and logs.json, for an account that may
// read user data, one that may not, and an account that no longer exists.
// Not parallel: it swaps the process-wide config.
func TestInterimExport_NarrowsToTheRunsActorAsHEADsExportDoes(t *testing.T) {
	const listener = "01BATGIRLLISTENER00000A"
	f := newRepairsOpAccessFixture(t)
	_ = opsManagerToken(t, f)
	store := f.srv.storeForWiring()
	now := time.Now().UTC()
	t.Setenv("TMPDIR", t.TempDir())

	insert := func(id, def, params, result string) {
		t.Helper()
		res := result
		require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
			ID: id, DefID: def, Plugin: "test", Status: "completed", Params: params,
			ResultData: &res, QueuedAt: now.Add(time.Second), CompletedAt: &now,
		}))
	}
	insert("op-parity-repair", "repairs.apply", `{"fixer_id":"maintenance.audible-read-status","target_user_id":"`+listener+`"}`,
		`{"fixer_id":"maintenance.audible-read-status","results":[{"owner_user_id":"`+listener+`"}]}`)
	insert("op-parity-scan", "library.scan", `{}`, `{"scanned":3}`)
	require.NoError(t, store.AddSystemActivityLog("abs", "info", "progress patch: user="+listener+" currentTime=12.5"))
	require.NoError(t, store.AddSystemActivityLog("server", "info", "merge: moved 3 rows for Knightfall"))
	require.NoError(t, store.AddSystemActivityLog("scanner", "info", "scan complete count=12"))

	mgr, err := store.GetUserByUsername("opsmgr")
	require.NoError(t, err)
	admin, err := store.GetUserByUsername("admin")
	require.NoError(t, err)
	require.NotNil(t, mgr)
	require.NotNil(t, admin)

	cases := []struct {
		name    string
		actor   string
		see     bool // the actor may read user data
		opActor *string
	}{
		{"manager", mgr.ID, false, &mgr.ID},
		{"admin", admin.ID, true, &admin.ID},
		{"gone", "01ROBINWHOLEFT0000000000", false, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opID := "op-parity-export-" + c.name
			require.NoError(t, store.InsertOperationV2(database.OperationV2Row{
				ID: opID, DefID: "diagnostics.export", Plugin: "diagnostics", Status: "running",
				Params: `{"category":"general"}`, ActorUserID: &c.actor, QueuedAt: now,
			}))
			// HEAD's path.
			head, err := diag.NewService(store, nil, "").GenerateExport(t.Context(), "general", "",
				f.srv.diagnosticsExportOptionsFor(f.srv.opActorHolds(opID)), nil)
			require.NoError(t, err)

			// The adapter, as the run's actor.
			rc := newInterimRC(t, diagnostics.ExportKind, tasks.ModeLive, tasks.Actor{UserID: c.actor, AuthMethod: "session"}, "run-"+c.name)
			res, err := NewInterimBackends(func() *Server { return f.srv }, testInterimAtoms).Export.Export(rc, viaJSON(t, diagnostics.ExportParams{ActorID: c.actor, Perms: []string{"core:admin"}}))
			require.NoError(t, err)
			require.Equal(t, "run-"+c.name+".zip", res.File)
			got := filepath.Join(os.TempDir(), res.File)

			for _, name := range []string{"operations.json", "logs.json"} {
				assert.Equal(t, zipEntry(t, head, name), zipEntry(t, got, name), "%s differs from HEAD's export for %s", name, c.name)
			}
			ops := zipEntry(t, got, "operations.json")
			logs := zipEntry(t, got, "logs.json")
			if c.see {
				assert.Contains(t, ops, listener, "the admin's export lost the user data HEAD gives it")
				assert.Contains(t, logs, "Knightfall")
			} else {
				assert.NotContains(t, ops, listener, "an account that may not read user data got a user id in operations.json")
				assert.NotContains(t, logs, listener, "an account that may not read user data got a user id in logs.json")
				assert.NotContains(t, logs, "Knightfall", "an account that may not read user data got a user-data subsystem line")
				assert.NotContains(t, ops, "audible-read-status", "a Repairs run the account may not use is in its export")
			}
			assert.Contains(t, ops, "op-parity-scan", "an ordinary operation stays")
		})
	}
}

// Params that name another actor than the run's are refused, and so is a run
// with no actor: the stamped actor decides, the params only agree with it.
func TestInterimExport_RefusesAnActorThatIsNotTheRuns(t *testing.T) {
	s := &Server{}
	rc := newInterimRC(t, diagnostics.ExportKind, tasks.ModeLive, tasks.Actor{UserID: "bruce"}, "r")
	_, err := s.exportBundle(rc, diagnostics.ExportParams{ActorID: "selina"}, nil)
	assert.ErrorIs(t, err, diagnostics.ErrActorMismatch)

	rc = newInterimRC(t, diagnostics.ExportKind, tasks.ModeLive, tasks.Actor{System: "scheduler"}, "r")
	_, err = s.exportBundle(rc, diagnostics.ExportParams{}, nil)
	assert.ErrorIs(t, err, diagnostics.ErrActorMismatch, "a run with no user must not be exported under an empty actor")
}

// Sections name core sections HEAD's exporter does not have: refused, not
// silently answered with the whole bundle.
func TestInterimExport_RefusesSections(t *testing.T) {
	s := &Server{}
	rc := newInterimRC(t, diagnostics.ExportKind, tasks.ModeLive, tasks.Actor{UserID: "bruce"}, "r")
	_, err := s.exportBundle(rc, diagnostics.ExportParams{ActorID: "bruce", Sections: []string{"core.health"}}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sections")
}
