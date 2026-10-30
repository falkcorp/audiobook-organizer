// file: internal/plugins/maintenance/reports_prune.go
// version: 1.0.0
// guid: 6f0c5b0e-2d57-4a39-9a41-3c8e1f7a52d4
// last-edited: 2026-10-30

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/libroot"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// ReportsPruneDefID is the def the reports_prune scheduler task enqueues.
const ReportsPruneDefID = "maintenance.reports-prune"

const (
	reportsPruneDefaultKeep    = 3
	reportsPruneDefaultAgeDays = 14
	// reportsPruneMaxAgeDays bounds max_age_days so the day count cannot overflow
	// time.Duration negative and mark every report old.
	reportsPruneMaxAgeDays = 36500
)

// reportsPruneKinds is every op kind that writes "<kind>-<opID>.tsv" into
// {root}/.reports (opReportFileName callers plus backfill-file-hashes). Only a
// file named for one of these is ever a deletion candidate: the directory is
// shared with an operator, and an unknown name is not this op family's file.
// dedupe-book-file-rows is deliberately absent: it writes a fixed
// "<kind>-<mode>.tsv" name that is overwritten in place, so it never grows.
// A new report-writing op must be added here or its reports are never pruned.
var reportsPruneKinds = []string{
	"backfill-file-hashes",
	"book-shape-report",
	"mark-missing-files",
	"merge-same-path-dupes",
	"metadata-cache-reap",
	"missing-file-repoint",
	"recover-missing-files",
	"repoint-missing-to-folder-audio",
	"rewrite-path-prefix",
}

// ReportsPruneParams are the op's params. keep_per_kind and max_age_days are
// pointers so an omitted value takes its default. Both spellings of the mode
// are accepted and resolved by opmode.ResolveDryRun (omitted = preview).
type ReportsPruneParams struct {
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
	KeepPerKind *int  `json:"keep_per_kind,omitempty"`
	MaxAgeDays  *int  `json:"max_age_days,omitempty"`
}

// ReportsPruneResult is the op's persisted result.
type ReportsPruneResult struct {
	DryRun         bool   `json:"dry_run"`
	Dir            string `json:"dir"`
	KeepPerKind    int    `json:"keep_per_kind"`
	MaxAgeDays     int    `json:"max_age_days"`
	Scanned        int    `json:"scanned"`
	Removed        int    `json:"removed"`
	BytesFreed     int64  `json:"bytes_freed"`
	KeptNewest     int    `json:"kept_newest"`
	KeptYoung      int    `json:"kept_young"`
	SkippedLive    int    `json:"skipped_live"`
	SkippedPending int    `json:"skipped_pending"`
	SkippedSymlink int    `json:"skipped_symlink"`
	SkippedOther   int    `json:"skipped_other"`
	Failed         int    `json:"failed"`
}

// reportsPruneDef removes old per-run TSV reports from {RootDir}/.reports.
//
// WHY. backfill-file-hashes writes one report per run named for its op id (about
// 100 MB for a full-library run) and the other report ops write one per run;
// nothing removed them, so the directory grew without bound on the library
// volume.
//
// RETENTION. Per op kind, the newest keep_per_kind (3) reports are kept, and
// anything younger than max_age_days (14) is kept. A file goes only when it is
// both outside the newest N of its kind and older than the age.
//
// SAFETY. Only regular files (Lstat; a symlink is never followed or removed)
// whose name is "<known kind>-<id>.tsv"; never a name containing ".pending";
// never a file whose id is a queued or running operation; never a path outside
// the cleaned {RootDir}/.reports. An unreadable queue or an unusable root
// refuses the run.
func (p *Plugin) reportsPruneDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              ReportsPruneDefID,
		Preview:         sdk.PreviewSpec{Params: ReportsPruneParams{}},
		Permissions:     []auth.Permission{auth.PermSettingsManage}, // deletes files
		Liveness:        sdk.LivenessNone,
		ProgressTimeout: time.Hour, // LivenessNone requires an explicit budget
		Plugin:          "maintenance",
		DisplayName:     "Prune old op reports",
		Description:     "Removes old per-run TSV reports from the library's .reports directory, keeping the newest 3 per op kind and anything younger than 14 days (keep_per_kind, max_age_days). Previews (counts only) unless dry_run is false.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  ReportsPruneDefID,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead, sdk.CapFilesWrite},
		Run:             p.runReportsPrune,
	}
}

func (p *Plugin) runReportsPrune(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params ReportsPruneParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("reports-prune: invalid params: %w", err)
		}
	}
	dryRun, err := opmode.ResolveDryRun(ReportsPruneDefID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	keep, ageDays := reportsPruneDefaultKeep, reportsPruneDefaultAgeDays
	if params.KeepPerKind != nil {
		keep = *params.KeepPerKind
	}
	if params.MaxAgeDays != nil {
		ageDays = *params.MaxAgeDays
	}
	// Floors: keep 0 or age 0 would let a report of a run that is paused or
	// awaiting resume (not in the active queue) be removed.
	if keep < 1 {
		return fmt.Errorf("reports-prune: keep_per_kind must be at least 1, got %d", keep)
	}
	if ageDays < 1 || ageDays > reportsPruneMaxAgeDays {
		return fmt.Errorf("reports-prune: max_age_days must be between 1 and %d, got %d", reportsPruneMaxAgeDays, ageDays)
	}

	root := p.deps.RootDir()
	queue := p.deps.OperationQueueStore()
	if queue == nil {
		return errors.New("reports-prune: cannot verify which operations are running; refusing")
	}
	active, err := queue.ListActiveOperationsV2()
	if err != nil {
		return fmt.Errorf("reports-prune: cannot list active operations; refusing: %w", err)
	}
	live := make(map[string]struct{}, len(active))
	for _, op := range active {
		live[op.ID] = struct{}{}
	}

	res, perr := pruneReportsDir(ctx, root, reportsPruneOpts{
		Keep:   keep,
		MaxAge: time.Duration(ageDays) * 24 * time.Hour,
		Live:   live,
		Now:    time.Now(),
		DryRun: dryRun,
		Logf: func(format string, args ...any) {
			_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(format, args...))
		},
	})
	res.KeepPerKind, res.MaxAgeDays = keep, ageDays
	if setErr := opsregistry.ReporterSetResult(reporter, res); setErr != nil && perr == nil {
		perr = fmt.Errorf("reports-prune: persist result: %w", setErr)
	}
	verb := "Removed"
	if dryRun {
		verb = "Preview: would remove"
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("%s %d report(s), %d bytes, from %s (scanned %d; kept %d newest, %d young; skipped %d live, %d pending, %d symlink, %d other; %d failed)",
		verb, res.Removed, res.BytesFreed, logger.SanitizeLogValue(res.Dir), res.Scanned,
		res.KeptNewest, res.KeptYoung, res.SkippedLive, res.SkippedPending, res.SkippedSymlink, res.SkippedOther, res.Failed))
	return perr
}

type reportsPruneOpts struct {
	Keep   int
	MaxAge time.Duration
	Live   map[string]struct{}
	Now    time.Time
	DryRun bool
	Logf   func(format string, args ...any)
}

type reportCandidate struct {
	name  string
	path  string
	id    string
	size  int64
	mtime time.Time
}

// reportKindAndID splits "<kind>-<id>.tsv" for a known kind; ok is false for
// any other name. The longest matching kind wins.
func reportKindAndID(name string) (kind, id string, ok bool) {
	stem, found := strings.CutSuffix(name, ".tsv")
	if !found {
		return "", "", false
	}
	for _, k := range reportsPruneKinds {
		rest, has := strings.CutPrefix(stem, k+"-")
		if has && rest != "" && len(k) > len(kind) {
			kind, id, ok = k, rest, true
		}
	}
	return kind, id, ok
}

// pruneReportsDir applies the retention rule to {root}/.reports. It fails
// closed when root is unset, relative, or the reports directory is a symlink or
// not a directory. The returned result is valid even alongside an error.
func pruneReportsDir(ctx context.Context, root string, o reportsPruneOpts) (ReportsPruneResult, error) {
	res := ReportsPruneResult{DryRun: o.DryRun}
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) {
		return res, fmt.Errorf("reports-prune: root_dir %q is not an absolute path; refusing", root)
	}
	root = filepath.Clean(root)
	dir := filepath.Join(root, libroot.ReportDirName)
	res.Dir = dir
	if filepath.Dir(dir) != root {
		return res, fmt.Errorf("reports-prune: %s is not directly under the library root; refusing", dir)
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return res, nil // nothing was ever written
	}
	if err != nil {
		return res, fmt.Errorf("reports-prune: %w", err)
	}
	if !info.IsDir() { // Lstat: a symlink reports as non-dir
		return res, fmt.Errorf("reports-prune: %s is not a real directory; refusing", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return res, fmt.Errorf("reports-prune: read %s: %w", dir, err)
	}

	byKind := map[string][]reportCandidate{}
	for _, e := range entries {
		name := e.Name()
		kind, id, ok := reportKindAndID(name)
		if !ok {
			// Includes anything that is not "<known kind>-<id>.tsv", .pending
			// files among them.
			if strings.Contains(name, ".pending") {
				res.SkippedPending++
			} else {
				res.SkippedOther++
			}
			continue
		}
		res.Scanned++
		if strings.Contains(name, ".pending") {
			res.SkippedPending++
			continue
		}
		path := filepath.Join(dir, name)
		if filepath.Dir(path) != dir {
			res.SkippedOther++
			continue
		}
		fi, lerr := os.Lstat(path)
		switch {
		case lerr != nil:
			res.SkippedOther++
			continue
		case fi.Mode()&os.ModeSymlink != 0:
			res.SkippedSymlink++
			continue
		case !fi.Mode().IsRegular():
			res.SkippedOther++
			continue
		}
		byKind[kind] = append(byKind[kind], reportCandidate{name: name, path: path, id: id, size: fi.Size(), mtime: fi.ModTime()})
	}

	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	var errs []error
	for _, kind := range kinds {
		files := byKind[kind]
		sort.Slice(files, func(i, j int) bool {
			if !files[i].mtime.Equal(files[j].mtime) {
				return files[i].mtime.After(files[j].mtime)
			}
			return files[i].name > files[j].name
		})
		for i, f := range files {
			if err := ctx.Err(); err != nil {
				return res, errors.Join(append(errs, err)...)
			}
			if _, isLive := o.Live[f.id]; isLive {
				res.SkippedLive++
				continue
			}
			if i < o.Keep {
				res.KeptNewest++
				continue
			}
			if o.Now.Sub(f.mtime) < o.MaxAge {
				res.KeptYoung++
				continue
			}
			if o.DryRun {
				o.Logf("Would remove report %s (%d bytes)", logger.SanitizeLogValue(f.path), f.size)
			} else if rerr := os.Remove(f.path); rerr != nil {
				res.Failed++
				errs = append(errs, fmt.Errorf("remove %s: %w", f.path, rerr))
				continue
			} else {
				o.Logf("Removed report %s (%d bytes)", logger.SanitizeLogValue(f.path), f.size)
			}
			res.Removed++
			res.BytesFreed += f.size
		}
	}
	if len(errs) > 0 {
		return res, fmt.Errorf("reports-prune: %w", errors.Join(errs...))
	}
	return res, nil
}
