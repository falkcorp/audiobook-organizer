// file: internal/server/interim_backends.go
// version: 0.1.0
// guid: cac4c80d-b98c-469e-a0d8-8552882b6c01
// last-edited: 2026-11-28

package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falkcorp/mediacore/health/diagnostics"
	coreai "github.com/falkcorp/mediacore/integrations/ai"
	"github.com/falkcorp/mediacore/integrations/providerhttp"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	diag "github.com/falkcorp/audiobook-organizer/internal/diagnostics"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

// The interim backends of the three late-owner core kinds (05-tasks 5T.8.2,
// F5-18 commit 2): release-holds, AI batch poll and diagnostics export each run
// over the one-method backend core's kernel registers the kind over. They are
// late-bound: Options (which names them) runs before the books server exists,
// so each resolves the server through get when a run starts and refuses a run
// that starts before there is one, rather than holding a nil.
//
// Each wraps what HEAD's operation body already does, so a flip changes who
// calls it and not what it does. F7 (holds, poll) and F8 (export) move each into
// its owner and delete the interface and this adapter.

// ErrInterimNoServer is the refusal of an interim run that starts before the
// books server is built.
var ErrInterimNoServer = errors.New("server: the interim backend ran before the books server was built")

// InterimBackends is the three backends, ready to be handed to core's options.
type InterimBackends struct {
	Holds  providerhttp.InterimHolds
	Poll   coreai.InterimPoll
	Export diagnostics.InterimExport
}

// NewInterimBackends returns the three backends over the server get resolves
// when a run starts. get returns nil until the server exists.
func NewInterimBackends(get func() *Server) InterimBackends {
	return InterimBackends{
		Holds:  interimHolds{get: get},
		Poll:   interimPoll{get: get},
		Export: interimExport{get: get},
	}
}

// ---- release-holds ------------------------------------------------------

// holdRegistry is the part of metadata.ThrottleRegistry the release uses.
type holdRegistry interface {
	List() []metadata.ProviderThrottle
	ReleaseHold(providerID string) (metadata.ProviderThrottle, bool, error)
}

type interimHolds struct{ get func() *Server }

func (h interimHolds) ReleaseHolds(rc *tasks.RC, _ providerhttp.ReleaseHoldsParams) (providerhttp.ReleaseHoldsResult, error) {
	if h.get() == nil {
		return providerhttp.ReleaseHoldsResult{}, ErrInterimNoServer
	}
	return releaseHolds(rc, metadata.DefaultThrottleRegistry())
}

// releaseHolds is maintenance.throttle-reset's body (runThrottleResetOn) for a
// native run: the same listing, and in a Live run the same ReleaseHold call per
// hold. A Preview run lists and releases nothing. The params are empty, so every
// active hold is the scope (HEAD's empty provider).
func releaseHolds(rc *tasks.RC, reg holdRegistry) (providerhttp.ReleaseHoldsResult, error) {
	holds := reg.List()
	res := providerhttp.ReleaseHoldsResult{Listed: len(holds)}
	live := rc.Mode() == tasks.ModeLive
	failed := 0
	for _, h := range holds {
		rc.Log().Info("provider hold",
			"provider", h.ProviderID, "class", string(h.Reason), "reason", h.Detail, "until", h.Until.UTC().Format("2006-01-02T15:04:05Z07:00"))
		if !live {
			continue
		}
		if err := rc.Context().Err(); err != nil {
			return res, err
		}
		_, released, err := reg.ReleaseHold(h.ProviderID)
		switch {
		case err != nil:
			failed++
			rc.Log().Warn("could not release provider hold", "provider", h.ProviderID, "error", err)
		case released:
			res.Released++
			// !released with no error: the hold expired or a successful call
			// released it between the listing and here, so it is already gone.
		}
	}
	if failed > 0 {
		return res, fmt.Errorf("release-holds: %d of %d holds could not be released", failed, len(holds))
	}
	return res, nil
}

// ---- ai batch poll ------------------------------------------------------

// batchPollSource is the part of BatchPoller the poll uses.
type batchPollSource interface {
	Poll(ctx context.Context) (int, error)
}

type interimPoll struct{ get func() *Server }

func (p interimPoll) PollBatches(rc *tasks.RC, _ coreai.BatchPollParams) (coreai.BatchPollResult, error) {
	s := p.get()
	if s == nil {
		return coreai.BatchPollResult{}, ErrInterimNoServer
	}
	// A nil *BatchPoller held in the interface would not be nil: say "none"
	// with the untyped nil pollBatches tests for.
	var bp batchPollSource
	if s.batchPoller != nil {
		bp = s.batchPoller
	}
	return pollBatches(rc, bp)
}

// pollBatches is HEAD's scheduler hook (SchedulerDeps.PollBatches) for a native
// run: no batch poller is a run that did nothing, not an error, as the hook's
// "may be nil when no batch poller is configured" says. The poller's count is
// the jobs it advanced; staging each ready job's apply as an app run belongs to
// the native body F7 writes, so ApplyStaged and Failed stay zero here.
func pollBatches(rc *tasks.RC, bp batchPollSource) (coreai.BatchPollResult, error) {
	if bp == nil {
		return coreai.BatchPollResult{}, nil
	}
	n, err := bp.Poll(rc.Context())
	return coreai.BatchPollResult{Polled: n}, err
}

// ---- diagnostics export -------------------------------------------------

type interimExport struct{ get func() *Server }

func (e interimExport) Export(rc *tasks.RC, p diagnostics.ExportParams) (diagnostics.ExportResult, error) {
	s := e.get()
	if s == nil {
		return diagnostics.ExportResult{}, ErrInterimNoServer
	}
	return s.exportBundle(rc, p)
}

// exportBundleCategory is the category HEAD's start route defaults to
// (handlers.StartExport): ExportParams carries no category or description.
const exportBundleCategory = "general"

// exportBundle is HEAD's diagnostics.export body for a native run. The bundle is
// narrowed to what the person who started the run may read, exactly as HEAD's
// export is: holds is that account's permission set (actorHolds, the same
// resolution opActorHolds makes for an operation row) and
// diagnosticsExportOptionsFor turns it into the operations, logs and result
// filters. The actor is the one the executor stamped on the run; the params'
// ActorID is client-supplied and only has to agree with it, as core's own
// exporter requires.
//
// ExportParams.Sections names core sections HEAD's exporter has no notion of, so
// a run that names any is refused rather than silently given the whole bundle.
//
// HEAD writes the zip to a temp file. The run's file is that zip renamed to
// "<run-id>.zip" in the same directory (diagnostics.BundleFile): core mounts
// core.diagnostics.dir for the download route with F8-04 and this adapter moves
// with it.
func (s *Server) exportBundle(rc *tasks.RC, p diagnostics.ExportParams) (diagnostics.ExportResult, error) {
	if len(p.Sections) > 0 {
		return diagnostics.ExportResult{}, fmt.Errorf("diagnostics export: the interim backend writes the whole bundle and cannot honour sections %v", p.Sections)
	}
	actor := rc.Actor()
	if actor.UserID == "" || actor.UserID != p.ActorID {
		return diagnostics.ExportResult{}, diagnostics.ErrActorMismatch
	}
	ds := s.diagnosticsService
	if ds == nil {
		ds = diag.NewService(s.storeForWiring(), nil, config.AppConfig.ITunes.LibraryReadPath)
	}
	holds := s.actorHolds(actor.UserID, p.Perms)
	zipPath, err := ds.GenerateExport(rc.Context(), exportBundleCategory, "",
		s.diagnosticsExportOptionsFor(holds),
		func(current, total int, _ string) {
			rc.Progress().Set(int64(current), tasks.Known(int64(total)))
		})
	if err != nil {
		return diagnostics.ExportResult{}, fmt.Errorf("generate export: %w", err)
	}
	name := diagnostics.BundleFile(string(rc.RunID()))
	if err := os.Rename(zipPath, filepath.Join(filepath.Dir(zipPath), name)); err != nil {
		_ = os.Remove(zipPath)
		return diagnostics.ExportResult{}, fmt.Errorf("name the export %s: %w", name, err)
	}
	return diagnostics.ExportResult{File: name}, nil
}

// actorHolds is opActorHolds for a native run: what the account userID may do,
// read once, from the stamped actor and never from the params. scope is the
// permission snapshot of the start request (ExportParams.Perms; nil is none),
// which only caps the account's current set, as an operation row's
// ActorPermissions does. It fails closed the same way: an account that no
// longer exists, holds no role or is disabled, holds nothing. Auth disabled, and
// a first run with no users yet, hold everything, as opActorHolds does.
func (s *Server) actorHolds(userID string, scope []string) func(auth.Permission) bool {
	all := func(auth.Permission) bool { return true }
	if !config.Snapshot().EnableAuth {
		return all
	}
	if n, err := s.storeForWiring().CountUsers(); err == nil && n == 0 {
		return all
	}
	held := s.opActorPermissions(&database.OperationV2Row{ActorUserID: &userID, ActorPermissions: scope})
	return func(p auth.Permission) bool { return held[p] }
}
