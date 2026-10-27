// file: internal/server/reconcile.go
// version: 3.12.0
// guid: e7f8a9b0-c1d2-3e4f-5a6b-7c8d9e0f1a2b
// last-edited: 2026-10-26
// HTTP adapters — all logic in internal/reconcile

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/httputil"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	maintenanceplugin "github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/reconcile"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/gin-gonic/gin"
)

func (s *Server) reconcilePreview(c *gin.Context) {
	store := s.storeForWiring()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// Runs under the request's context: this is a full-library stat, walk and
	// hash sweep done inline, so a client that disconnects (or a proxy timeout)
	// must stop it rather than leave it burning disk until it finishes.
	result, err := reconcile.BuildReconcilePreviewCtx(c.Request.Context(), store, nil, reconcile.ScanHooks{})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			// The caller is gone; there is no one to send a 500 to and nothing
			// failed. Not logged as an error for the same reason.
			return
		}
		httputil.InternalError(c, "failed to build reconcile preview", err)
		return
	}
	httputil.RespondWithOK(c, result)
}

func (s *Server) startReconcileScan(c *gin.Context) {
	store := s.Ops()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if s.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}

	// Return the run EnqueueOp actually created. This used to mint a v1 row,
	// hand the caller that row, and discard the v2 id — so the id the client
	// held (api.ts reads raw.id off this body) belonged to a row the operations
	// endpoints no longer serve.
	opID, err := s.opRegistry.EnqueueOp(c.Request.Context(), "reconcile.scan", reconcileScanOpParams{})
	if err != nil {
		httputil.InternalError(c, "failed to enqueue operation", err)
		return
	}
	op := s.opTextFor(c, reconcileOperationView(store, opID, "reconcile_scan"))
	if op == nil {
		httputil.InternalError(c, "failed to load enqueued operation",
			fmt.Errorf("reconcile.scan %s not readable after enqueue", opID))
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, op)
}

// canonicalDefIDFunc returns the registry's def-id canonicalizer, or the identity
// function before the registry exists, so list helpers can take one nil-safe
// argument.
func (s *Server) canonicalDefIDFunc() func(string) string {
	if s.opRegistry == nil {
		return func(id string) string { return id }
	}
	return s.opRegistry.CanonicalDefID
}

// reconcileScanLog is the subsystem logger for the reconcile HTTP adapters.
var reconcileScanLog = logger.New("server.reconcile")

// pickLatestReconcileScan chooses what GET /operations/reconcile/scan/latest
// answers from ops (newest first).
//
//   - scan and preview are the newest run that completed AND whose result
//     decodes: the last scan whose findings can actually be shown. A newer run
//     that was canceled, failed, was interrupted by a deploy, or finished with an
//     unreadable result must not hide it. Before 04-P14c the endpoint answered
//     from ops[0] regardless of status, which was tolerable while only a manual
//     click could start a scan; the nightly run now shares this id, and one
//     canceled night would have blanked the tab until the next good run.
//   - attempt is ops[0] when it is NOT the scan above: the newer run the caller
//     would otherwise never hear about (still running, canceled, failed, or
//     completed with an unreadable result). nil when the newest run is the scan.
//   - With no usable scan at all, scan is ops[0] and preview nil, which is what
//     the endpoint always answered for a library that has never been scanned
//     (or whose only run is still in flight); attempt is then nil, since the
//     operation field already is that run.
func pickLatestReconcileScan(ops []*database.Operation) (scan *database.Operation, preview *reconcile.ReconcilePreviewResult, attempt *database.Operation) {
	if len(ops) == 0 {
		return nil, nil, nil
	}
	for _, op := range ops {
		if op.Status != "completed" || op.ResultData == nil {
			continue
		}
		var decoded reconcile.ReconcilePreviewResult
		if err := json.Unmarshal([]byte(*op.ResultData), &decoded); err != nil {
			// An unreadable result is skipped, not fatal: an older run may still
			// parse. It stays visible to the caller if it is the newest run.
			reconcileScanLog.Warn("reconcile: scan %s completed but its result could not be decoded; looking for an older scan: %v", op.ID, err)
			continue
		}
		scan, preview = op, &decoded
		break
	}
	if scan == nil {
		return ops[0], nil, nil
	}
	if ops[0] != scan {
		attempt = ops[0]
	}
	return scan, preview, attempt
}

func (s *Server) latestReconcileScan(c *gin.Context) {
	store := s.Ops()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}

	// Find the most recent reconcile scans, newest first. Only the v2 keyspace is
	// read: nothing has minted a v1 row since the minter was retired. Rows are
	// matched on the registry's canonical def id so the nightly runs recorded
	// under the id this scan absorbed (04-P14c) still appear.
	ops := recentReconcileScans(store, 200, s.canonicalDefIDFunc())

	scan, preview, attempt := pickLatestReconcileScan(ops)
	if scan == nil {
		httputil.RespondWithOK(c, gin.H{"operation": nil, "preview": nil, "latest_attempt": nil})
		return
	}
	// preview is a nil *ReconcilePreviewResult when no scan is usable; gin.H would
	// marshal that as null, but a typed nil in an interface is easy to misread, so
	// the two shapes are spelled out.
	body := gin.H{"operation": s.opTextFor(c, scan), "preview": nil, "latest_attempt": nil}
	if preview != nil {
		body["preview"] = preview
	}
	if attempt != nil {
		body["latest_attempt"] = s.opTextFor(c, attempt)
	}
	httputil.RespondWithOK(c, body)
}

// LatestReconcileScan implements maintenanceplugin.ReconcileScanReader for the
// Repairs fixer reconcile.missing-files: the scan GET
// /operations/reconcile/scan/latest shows, picked by the same rule over the
// same rows, so the fixer's trial and the endpoint can never disagree on which
// scan is "latest".
func (s *Server) LatestReconcileScan() (maintenanceplugin.ReconcileScan, error) {
	store := s.Ops()
	if store == nil {
		return maintenanceplugin.ReconcileScan{}, errors.New("database not initialized")
	}
	scan, preview, attempt := pickLatestReconcileScan(recentReconcileScans(store, 200, s.canonicalDefIDFunc()))
	var out maintenanceplugin.ReconcileScan
	if scan == nil || preview == nil {
		// No usable scan. scan, when set, is the newest run (in flight, or
		// with nothing decodable): report it as the attempt.
		if scan != nil {
			out.AttemptOpID, out.AttemptStatus = scan.ID, scan.Status
		}
		return out, nil
	}
	out.OpID, out.CompletedAt, out.Preview = scan.ID, scan.CompletedAt, preview
	if attempt != nil {
		out.AttemptOpID, out.AttemptStatus = attempt.ID, attempt.Status
	}
	return out, nil
}

func (s *Server) startReconcile(c *gin.Context) {
	store := s.Ops()
	if store == nil {
		httputil.RespondWithInternalError(c, "database not initialized")
		return
	}
	if s.opRegistry == nil {
		httputil.RespondWithInternalError(c, "operation registry not initialized")
		return
	}

	var req struct {
		Matches []reconcile.ReconcileApplyItem `json:"matches"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.RespondWithBadRequest(c, err.Error())
		return
	}

	if len(req.Matches) == 0 {
		httputil.RespondWithBadRequest(c, "no matches provided")
		return
	}

	opID, err := s.opRegistry.EnqueueOp(c.Request.Context(), "reconcile.apply",
		reconcileApplyOpParams{Matches: req.Matches})
	if err != nil {
		httputil.InternalError(c, "failed to enqueue operation", err)
		return
	}
	op := s.opTextFor(c, reconcileOperationView(store, opID, "reconcile"))
	if op == nil {
		httputil.InternalError(c, "failed to load enqueued operation",
			fmt.Errorf("reconcile.apply %s not readable after enqueue", opID))
		return
	}

	httputil.RespondWithSuccess(c, http.StatusAccepted, op)
}

func (s *Server) cleanupDuplicateVersionGroupsHandler(c *gin.Context) {
	dryRun := c.Query("dry_run") == "true"
	result, err := reconcile.CleanupDuplicateVersionGroups(s.storeForWiring(), config.AppConfig.RootDir, dryRun)
	if err != nil {
		httputil.InternalError(c, "failed to cleanup version groups", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{
		"dry_run": dryRun,
		"result":  result,
	})
}

func (s *Server) markBrokenSegmentBooksHandler(c *gin.Context) {
	dryRun := c.Query("dry_run") == "true"
	result, err := reconcile.FindBrokenSegmentBooks(s.storeForWiring(), dryRun)
	if err != nil {
		httputil.InternalError(c, "failed to find broken segments", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{
		"dry_run": dryRun,
		"result":  result,
	})
}

func (s *Server) mergeNoVGDuplicatesHandler(c *gin.Context) {
	dryRun := c.Query("dry_run") == "true"
	result, err := reconcile.MergeNoVGDuplicates(s.storeForWiring(), config.AppConfig.RootDir, dryRun)
	if err != nil {
		httputil.InternalError(c, "failed to merge duplicates", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{
		"dry_run": dryRun,
		"result":  result,
	})
}

func (s *Server) assignOrphanVGsHandler(c *gin.Context) {
	result, err := reconcile.AssignOrphanVGs(s.storeForWiring(), config.AppConfig.RootDir)
	if err != nil {
		httputil.InternalError(c, "failed to assign version groups", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"result": result})
}

// electMissingPrimariesHandler repairs version groups that elect no primary at
// all, which makes every member invisible to any client applying the default
// is_primary_version=true filter (the web Library page does).
//
// Defaults to a dry run: a mutating apply must be opted into explicitly with
// ?dry_run=false, so an accidental POST previews rather than rewrites rows.
//
// ?exclude_groups= names version groups the election must leave alone. The
// 2026-09-19 census found 15 groups whose "versions" are in fact the chapter
// files of one book, where crowning a member makes a chapter the book; the
// apply was blocked on having no way to hold them back. Repeat the parameter,
// pass it comma-separated, or use the exclude_groups[] spelling. Ids that
// match nothing come back in the result's excluded_unmatched rather than
// failing the request — see ElectMissingPrimaries.
//
// The election is versionprimary's rule (2026-09-24): it reads each member's
// book_file rows and, for a single m4b/m4a, ffprobes the chapter count. With
// no ffprobe on PATH the chapter table is used instead, so a missing binary
// degrades the ranking rather than failing the request; the result's
// held_* counters say which groups were not crowned and why.
func (s *Server) electMissingPrimariesHandler(c *gin.Context) {
	dryRun := c.DefaultQuery("dry_run", "true") != "false"
	exclude := electExcludeGroupsFromQuery(c)
	env := reconcile.ElectEnv{RootDir: config.AppConfig.RootDir}
	if probe, perr := versionprimary.FFprobeChapterCounter(); perr == nil {
		env.Probe = probe
	} else {
		lifecycleLog.Warn("elect-missing-primaries: ffprobe unavailable; chapter counts come from the chapter table: %v", perr)
	}
	result, err := reconcile.ElectMissingPrimaries(s.storeForWiring(), dryRun, exclude, env)
	if err != nil {
		httputil.InternalError(c, "failed to elect missing primary versions", err)
		return
	}
	httputil.RespondWithOK(c, gin.H{"dry_run": dryRun, "result": result})
}

// electExcludeGroupsFromQuery reads the exclude_groups parameter in every
// spelling the rest of this API accepts for a repeated list: the parameter
// given more than once, the tags[] style bracket alias (handlers/audiobooks
// handler.go:491), and one comma-separated value, which is what an operator
// pasting a list of 15 ids from a report will actually type. Values are
// passed through verbatim; ElectMissingPrimaries trims and dedupes them and
// reports the ones that match nothing.
//
// Deliberately NOT accepting a camelCase excludeGroups: no query parameter
// on this API is spelled that way (dry_run on this same handler, status,
// tags/tags[] elsewhere), and a second accepted spelling is a second thing to
// keep in step with the docs.
func electExcludeGroupsFromQuery(c *gin.Context) []string {
	raw := append(c.QueryArray("exclude_groups"), c.QueryArray("exclude_groups[]")...)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, strings.Split(v, ",")...)
	}
	return out
}
