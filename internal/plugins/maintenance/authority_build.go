// file: internal/plugins/maintenance/authority_build.go
// version: 1.3.0
// guid: 7b057b54-4781-486b-b075-fe627504dcf1
// last-edited: 2026-11-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authority"
	"github.com/falkcorp/audiobook-organizer/internal/authority/authoritybuild"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// --- authority-build ---
//
// Builds the authority lists (internal/authority): the rebuildable ref_*
// index of known persons and publishers, from three sources in PR 1, none of
// which touches the network:
//
//  1. the seed embedded in the binary (owner library names + contributor
//     ASINs, tier O);
//  2. every cat_raw: payload the author catalog stored (tier A/B; 0 rows on
//     an empty catalog is a normal run);
//  3. optionally, an owner library export file on the server
//     (library_export_path; audible-cli `library export` JSON, tier O).
//
// DRY RUN BY DEFAULT (owner rule 2026-09-25): the run reports per-source and
// per-tier counts, homonym conflicts, the cast-context population,
// single-word persons, samples, and what an apply would write, and stores
// the plan as the op result: its digest, the FULL list of keys it would
// prune, the rows it holds back, and any reason an apply would be refused.
//
// APPLY BY EXPLICIT PLAN (owner rule: dry run -> list -> apply the reviewed
// plan). dry_run=false requires plan_op_id, the id of a completed dry run.
// The apply re-runs the build with that dry run's sources, recomputes the
// plan, and refuses unless the digest is identical, so it applies exactly the
// reviewed plan and nothing else (a store or source change since the dry run
// is a refusal, not a silent different apply). It also refuses while any
// source is skipped (skip_seed, skip_catalog, or no export while export rows
// exist) or any payload failed to decode, because a partial build must not
// prune. Pruning itself is per row: a row is pruned only when every source it
// records ran. An apply writes ref_person/ref_pub/ref_asin/ref_src keys only,
// and never touches owner overrides (ref_ovr:).
//
// The ref_src: ledger is the per-source record of what each item
// contributed: it is how a later build knows owner-export rows exist (and so
// refuses to prune without the export), and its digests make the plan diff
// per item.

const authorityBuildOpID = "maintenance.authority-build"

const (
	// authorityCatalogPageSize bounds one cat_raw: page (a payload is a few
	// KB, so a page is a few MB).
	authorityCatalogPageSize = 500
	// authorityMaxConcurrency caps the caller's concurrency param.
	authorityMaxConcurrency = 32
)

// authorityBuildParams are the op's params. Every field is optional.
type authorityBuildParams struct {
	DryRun      *bool `json:"dry_run,omitempty"`
	DryRunCamel *bool `json:"dryRun,omitempty"`
	// LibraryExportPath is an absolute path, on the server, to an owner
	// library export (same JSON shape as audible-cli `library export`). Only
	// contributor names, contributor ASINs and publishers are kept.
	LibraryExportPath string `json:"library_export_path,omitempty"`
	// SkipCatalog leaves the cat_raw: payloads out.
	SkipCatalog bool `json:"skip_catalog,omitempty"`
	// SkipSeed leaves the embedded seed out.
	SkipSeed bool `json:"skip_seed,omitempty"`
	// Concurrency is the worker count for decoding and for the plan/apply
	// reads and writes (default runtime.NumCPU(), max 32).
	Concurrency int `json:"concurrency,omitempty"`
	// PlanOpID is the completed dry run an apply carries out. Required with
	// dry_run=false; the sources come from that dry run, so the source
	// fields above must be left unset on an apply.
	PlanOpID string `json:"plan_op_id,omitempty"`
}

// authoritySources are the source choices a plan was built with.
type authoritySources struct {
	SkipSeed          bool   `json:"skip_seed,omitempty"`
	SkipCatalog       bool   `json:"skip_catalog,omitempty"`
	LibraryExportPath string `json:"library_export_path,omitempty"`
}

func (p authorityBuildParams) sources() authoritySources {
	return authoritySources{SkipSeed: p.SkipSeed, SkipCatalog: p.SkipCatalog, LibraryExportPath: p.LibraryExportPath}
}

// authorityPlanResult is the op result (GET /operations/:id/result): the
// reviewed plan of a dry run, or what an apply did. Fields are ordered for a
// human reading the JSON top-down: the one-line summary and any refusals
// first, then the apply params to send, counts, the full prune and held
// lists, and the detailed report last.
type authorityPlanResult struct {
	// Summary is one line: mode, writes, prunes, held rows, refusals.
	Summary string `json:"summary"`
	// Refusals are the reasons an apply of this plan would be refused.
	Refusals []string `json:"refusals"`
	// ApplyParams is the body to enqueue to apply this dry run's plan; set
	// only on a dry run with no refusals.
	ApplyParams map[string]any                          `json:"apply_params,omitempty"`
	DryRun      bool                                    `json:"dry_run"`
	PlanOpID    string                                  `json:"plan_op_id,omitempty"`
	Digest      string                                  `json:"digest"`
	Sources     authoritySources                        `json:"sources"`
	Ran         []string                                `json:"ran"`
	Writes      int                                     `json:"writes"`
	PruneCount  int                                     `json:"prune_count"`
	HeldCount   int                                     `json:"held_count"`
	Counts      map[string]*authoritybuild.PrefixCounts `json:"counts"`
	// Prune is every key the plan deletes; Held every stale row it keeps
	// because a source it came from did not run.
	Prune  []string                    `json:"prune"`
	Held   []string                    `json:"held"`
	Report authoritybuild.Report       `json:"report"`
	Apply  *authoritybuild.ApplyResult `json:"apply,omitempty"`
}

// authorityBuildHelp is the def's Description, which the native kind carries as
// its Help.
const authorityBuildHelp = "Builds the authority lists (known authors, narrators, cast members and publishers, " +
	"with contributor ASINs and evidence tiers) from the embedded owner-library seed, the author " +
	"catalog's stored provider payloads, and optionally an owner library export " +
	"(library_export_path). DRY RUN BY DEFAULT: reports per-source and per-tier counts, homonym " +
	"conflicts (one name, several ASINs: reported, never picked), cast-only names, single-word " +
	"names and samples, and stores the plan (digest, the full prune list, held rows, refusals) as " +
	"the op result. dry_run=false needs plan_op_id (a reviewed dry run) and applies exactly that " +
	"plan, refusing if the store or sources changed, any source was skipped or any payload failed " +
	"to decode. It writes only ref_person/ref_pub/ref_asin/ref_src keys, prunes only rows whose " +
	"every source ran, and never touches owner overrides. No network. Nothing reads the lists yet."

func (p *Plugin) authorityBuildDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          authorityBuildOpID,
		Preview:     sdk.PreviewSpec{Params: authorityBuildParams{}},
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Authority lists: build",
		Description: authorityBuildHelp,
		// ResumeDrop: a rebuild is idempotent and cheap to re-trigger.
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  authorityBuildOpID,
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		// No Schedule: manual trigger only in PR 1.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapFilesRead},
		Run:          p.runAuthorityBuild,
	}
}

func (p *Plugin) runAuthorityBuild(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params authorityBuildParams
	if err := decodeStrictParams(raw, &params); err != nil {
		return fmt.Errorf("%s: invalid params: %w", authorityBuildOpID, err)
	}
	dryRun, err := opmode.ResolveDryRun(authorityBuildOpID, params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}
	env := authorityBuildEnv{
		t6g1Env: t6g1HeadEnv(reporter, !dryRun),
		Items:   t6g1HeadItems(reporter),
		ApplyParams: func(planOpID string) map[string]any {
			return map[string]any{"dry_run": false, "plan_op_id": planOpID}
		},
	}
	// A nil OpsStore stays a nil interface in env.Store, which the core reports
	// as "database not initialized".
	if store := p.deps.OpsStore(); store != nil {
		env.Store, env.Writer = store, store
	}
	if r, ok := p.deps.OperationQueueStore().(authorityPlanReader); ok {
		env.Plans = r
	}
	out, err := runAuthorityBuildCore(ctx, params, dryRun, env, time.Now())
	if out != nil {
		if serr := registry.ReporterSetResult(reporter, out.Result); serr != nil && err == nil {
			err = fmt.Errorf("%s: store result: %w", authorityBuildOpID, serr)
		}
	}
	return err
}

// authorityBuildKind is rule R3's kind for the def above.
const authorityBuildKind = tasks.Kind("books." + authorityBuildOpID)

// authorityBuildStore is the reads the op makes of the key-value store: the
// paged prefix scan the plan and the catalog scan use, the point read, and the
// prefix count.
type authorityBuildStore interface {
	authority.Scanner
	CountPrefix(prefix string) (int64, error)
}

// authorityBuildWriter is the two writes an apply makes: the rebuildable keys
// it sets and the stale ones it deletes in batches.
type authorityBuildWriter interface {
	SetRaw(key string, value []byte) error
	DeleteRawBatch(keys []string) error
}

// authorityKV is the read-and-write view authoritybuild.Apply takes: the reads
// come from the env's store and every write from its writer.
type authorityKV struct {
	authorityBuildStore
	authorityBuildWriter
}

var _ authoritybuild.Writer = authorityKV{}

// authorityBuildEnv is what the core takes from its shell.
type authorityBuildEnv struct {
	t6g1Env
	Store  authorityBuildStore
	Writer authorityBuildWriter
	// Plans reads the stored dry run an apply carries out; nil when the
	// operation store cannot.
	Plans authorityPlanReader
	// Items runs fn over n indexes on the pool the shell provides.
	Items t6g1ItemsFunc
	// ApplyParams is the body that applies a dry run's plan, for the result.
	ApplyParams func(planOpID string) map[string]any
}

// authorityBuildParallelism is the op's in-body concurrency: the caller's
// concurrency param, defaulting to every CPU (HEAD's runtime.NumCPU()).
func authorityBuildParallelism() tasks.Parallelism { return tasks.CPU() }

// authorityBuildNativeParams are the native kind's params: HEAD's without the
// dry_run switches, which the run's mode carries.
type authorityBuildNativeParams struct {
	LibraryExportPath string `json:"library_export_path,omitempty"`
	SkipCatalog       bool   `json:"skip_catalog,omitempty"`
	SkipSeed          bool   `json:"skip_seed,omitempty"`
	Concurrency       int    `json:"concurrency,omitempty"`
	PlanOpID          string `json:"plan_op_id,omitempty"`
}

// authorityBuildNative is the native kind. A Preview run is the dry run and a
// Live run the apply; the apply writes only the ref_* keys.
func (p *Plugin) authorityBuildNative(deps t6g1Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	c := t6g1Common(authorityBuildKind, sched)
	c.Title = "Authority lists: build"
	c.Help = authorityBuildHelp
	c.Effects = tasks.Writes(tasks.Res("books", "aux_stores"))
	c.Resume = tasks.ResumeDrop
	c.Priority = tasks.PriorityLow
	c.ConcurrencyKey = tasks.Key("books." + authorityBuildOpID)
	c.Timeout = 2 * time.Hour
	return tasks.Task(authorityBuildKind, tasks.TaskSpec[authorityBuildNativeParams, authorityPlanResult]{
		Common: c,
		Run: func(rc *tasks.RC, np authorityBuildNativeParams) (authorityPlanResult, error) {
			params := authorityBuildParams{
				LibraryExportPath: np.LibraryExportPath, SkipCatalog: np.SkipCatalog,
				SkipSeed: np.SkipSeed, Concurrency: np.Concurrency, PlanOpID: np.PlanOpID,
			}
			env := authorityBuildEnv{
				t6g1Env: t6g1NativeEnv(rc),
				Writer:  deps.NewWriter(rc.Writer()),
				Plans:   deps.PlanReader,
				Items:   t6g1NativeItems(rc),
				ApplyParams: func(planOpID string) map[string]any {
					return map[string]any{"plan_op_id": planOpID}
				},
			}
			if deps.Store != nil {
				env.Store = deps.Store
			}
			out, err := runAuthorityBuildCore(rc.Context(), params, !env.Live, env, time.Now())
			if out == nil {
				return authorityPlanResult{}, err
			}
			return out.Result, err
		},
	})
}

// runAuthorityBuildCore is the body of runAuthorityBuild from the store check
// on: params are decoded and the mode resolved.
func runAuthorityBuildCore(ctx context.Context, params authorityBuildParams, dryRun bool, env authorityBuildEnv, now time.Time) (*authorityBuildOutcome, error) {
	if env.Store == nil {
		return nil, errors.New("database not initialized")
	}
	expect := ""
	if !dryRun {
		if params.PlanOpID == "" {
			return nil, fmt.Errorf("%s: dry_run=false needs plan_op_id (the reviewed dry run to apply)", authorityBuildOpID)
		}
		if params.sources() != (authoritySources{}) {
			return nil, fmt.Errorf("%s: an apply takes its sources from plan %s; leave skip_seed, skip_catalog and library_export_path unset", authorityBuildOpID, params.PlanOpID)
		}
		if env.Plans == nil {
			return nil, fmt.Errorf("%s: operation store cannot read the plan op", authorityBuildOpID)
		}
		plan, err := loadAuthorityPlan(env.Plans, params.PlanOpID)
		if err != nil {
			return nil, err
		}
		params.SkipSeed, params.SkipCatalog, params.LibraryExportPath = plan.Sources.SkipSeed, plan.Sources.SkipCatalog, plan.Sources.LibraryExportPath
		expect = plan.Digest
	}
	return buildAuthorityListsCore(ctx, env, params, dryRun, expect, now)
}

// authorityPlanReader reads a stored op row (database.Store has it; the
// production queue store is the full store).
type authorityPlanReader interface {
	GetOperationV2(id string) (*database.OperationV2Row, error)
}

// loadAuthorityPlan reads a completed authority-build dry run's result.
func loadAuthorityPlan(r authorityPlanReader, id string) (*authorityPlanResult, error) {
	row, err := r.GetOperationV2(id)
	if err != nil {
		return nil, fmt.Errorf("%s: read plan op %s: %w", authorityBuildOpID, id, err)
	}
	switch {
	case row == nil:
		return nil, fmt.Errorf("%s: plan op %s not found", authorityBuildOpID, id)
	case row.DefID != authorityBuildOpID:
		return nil, fmt.Errorf("%s: op %s is %s, not an authority-build dry run", authorityBuildOpID, id, row.DefID)
	case row.Status != "completed" || row.ResultData == nil:
		return nil, fmt.Errorf("%s: plan op %s is %s, not a completed dry run", authorityBuildOpID, id, row.Status)
	}
	var plan authorityPlanResult
	if err := json.Unmarshal([]byte(*row.ResultData), &plan); err != nil {
		return nil, fmt.Errorf("%s: decode plan %s: %w", authorityBuildOpID, id, err)
	}
	if !plan.DryRun || plan.Digest == "" {
		return nil, fmt.Errorf("%s: op %s is not a dry run with a plan", authorityBuildOpID, id)
	}
	return &plan, nil
}

// authorityBuildOutcome is what one run did; tests read it.
type authorityBuildOutcome struct {
	Result authorityPlanResult
	Report authoritybuild.Report
	Counts map[string]*authoritybuild.PrefixCounts
	Stale  int
	Writes int
	Apply  authoritybuild.ApplyResult
	DryRun bool
}

func authorityWorkers(n int) int {
	if n <= 0 {
		n = runtime.NumCPU()
	}
	return min(max(1, n), authorityMaxConcurrency)
}

// authorityProgress maps plan, apply and prune onto one 0..1000 scale so the
// progress bar never moves backwards between phases (each phase has its own
// total). Every call stamps the watchdog.
func authorityProgress(env t6g1Env) func(phase string, done, total int) {
	spans := map[string][2]int{"plan": {0, 500}, "apply": {500, 950}, "prune": {950, 1000}}
	return func(phase string, done, total int) {
		sp, ok := spans[phase]
		if !ok || total <= 0 {
			return
		}
		cur := sp[0] + (sp[1]-sp[0])*min(done, total)/total
		env.Progress(cur, 1000, fmt.Sprintf("authority-build %s: %d/%d", phase, done, total))
	}
}

// buildAuthorityLists runs the build on any raw KV store. The production
// store is OpsStore, which embeds database.RawKVStore, so a Store-embedding
// wrapper serves it directly. On an apply, expectDigest is the reviewed dry
// run's plan digest: the apply refuses unless the recomputed plan matches it.
func buildAuthorityLists(ctx context.Context, kv database.RawKVStore, params authorityBuildParams, dryRun bool,
	expectDigest string, reporter sdk.Reporter, now time.Time) (*authorityBuildOutcome, error) {
	env := authorityBuildEnv{
		t6g1Env: t6g1HeadEnv(reporter, !dryRun),
		Store:   kv,
		Writer:  kv,
		Items:   t6g1HeadItems(reporter),
		ApplyParams: func(planOpID string) map[string]any {
			return map[string]any{"dry_run": false, "plan_op_id": planOpID}
		},
	}
	return buildAuthorityListsCore(ctx, env, params, dryRun, expectDigest, now)
}

// buildAuthorityListsCore is buildAuthorityLists over an env: the reads come
// from env.Store, every write from env.Writer.
func buildAuthorityListsCore(ctx context.Context, env authorityBuildEnv, params authorityBuildParams, dryRun bool,
	expectDigest string, now time.Time) (*authorityBuildOutcome, error) {
	kv := authorityKV{env.Store, env.Writer}
	if !dryRun && expectDigest == "" {
		return nil, errors.New("authority-build: an apply needs the reviewed plan's digest")
	}
	workers := authorityWorkers(params.Concurrency)
	mode := map[bool]string{true: "DRY RUN (nothing written)", false: "APPLY"}[dryRun]
	env.Log(slog.LevelInfo, fmt.Sprintf("authority-build: %s, %d workers", mode, workers))

	b := authoritybuild.NewBuilder()
	ran := map[string]bool{}

	if !params.SkipSeed {
		seed, err := authoritybuild.LoadSeed()
		if err != nil {
			return nil, err
		}
		b.AddSeed(seed)
		ran[authority.SourceSeed] = true
		env.Log(slog.LevelInfo, fmt.Sprintf("authority-build: seed v%s, %d entries", seed.Version, len(seed.Entries)))
	}

	if params.LibraryExportPath != "" {
		if err := ingestLibraryExport(ctx, b, params.LibraryExportPath, workers, env); err != nil {
			return nil, err
		}
		ran[authority.SourceLibraryExport] = true
	}

	if !params.SkipCatalog {
		if err := ingestCatalogRaw(ctx, env, b, workers); err != nil {
			return nil, err
		}
		ran[authority.SourceCatalog] = true
	}

	res := b.Finish()
	refusals, err := authorityRefusals(env.Store, params, res.Report)
	if err != nil {
		return nil, err
	}
	opt := authoritybuild.Options{
		Workers: workers,
		// Plan and apply are errgroup pools, not RunItems, so they stamp the
		// watchdog through UpdateProgress themselves (throttled).
		Progress: authorityProgress(env.t6g1Env),
	}
	plan, err := authoritybuild.PlanApply(ctx, kv, res, ran, now, opt)
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	out := &authorityBuildOutcome{Report: res.Report, Counts: plan.Counts, Stale: len(plan.Stale), Writes: len(plan.Writes), DryRun: dryRun}
	out.Result = authorityPlanResult{
		DryRun: dryRun, Digest: plan.Digest, Sources: params.sources(), Ran: sortedSet(ran),
		Refusals: refusals, Writes: len(plan.Writes), Counts: plan.Counts,
		Prune: nonNil(plan.Stale), Held: nonNil(plan.Held), Report: res.Report,
	}
	out.Result.PruneCount, out.Result.HeldCount = len(plan.Stale), len(plan.Held)
	if out.Result.Refusals == nil {
		out.Result.Refusals = []string{}
	}
	if !dryRun {
		out.Result.PlanOpID = params.PlanOpID
	} else if id := env.OpID; id != "" && len(refusals) == 0 {
		out.Result.ApplyParams = env.ApplyParams(id)
	}
	out.Result.Summary = fmt.Sprintf("%s: %d writes, %d prunes, %d held, %d persons, %d publishers; %s",
		map[bool]string{true: "DRY RUN", false: "APPLY"}[dryRun], len(plan.Writes), len(plan.Stale), len(plan.Held),
		res.Report.Persons, res.Report.Publishers,
		map[bool]string{true: "apply allowed", false: fmt.Sprintf("apply refused (%d reasons)", len(refusals))}[len(refusals) == 0])
	logAuthorityReport(env.t6g1Env, mode, out)
	for _, r := range refusals {
		env.Log(slog.LevelWarn, "authority-build: an apply would be refused: "+r)
	}

	if dryRun {
		env.Progress(1000, 1000, "authority-build: dry run planned")
		return out, nil
	}
	if len(refusals) > 0 {
		return out, fmt.Errorf("authority-build: apply refused: %s", strings.Join(refusals, "; "))
	}
	if plan.Digest != expectDigest {
		return out, fmt.Errorf("authority-build: apply refused: the plan changed since dry run %s "+
			"(digest %s, now %s): the store or the sources changed; run a new dry run and review it",
			params.PlanOpID, expectDigest, plan.Digest)
	}
	out.Apply, err = authoritybuild.Apply(ctx, kv, plan, opt)
	out.Result.Apply = &out.Apply
	if err != nil {
		return out, fmt.Errorf("apply: %w", err)
	}
	env.Progress(1000, 1000, "authority-build: applied")
	env.Log(slog.LevelInfo, fmt.Sprintf("authority-build: APPLIED plan %s written=%d deleted=%d", params.PlanOpID, out.Apply.Written, out.Apply.Deleted))
	return out, nil
}

// authorityRefusals lists why an apply of this build must not run: a
// skipped source, owner-export rows in the store with no export supplied, or
// any payload that failed to decode. Each would make the build partial, and
// a partial build must not prune.
func authorityRefusals(kv authorityBuildStore, params authorityBuildParams, rep authoritybuild.Report) ([]string, error) {
	var out []string
	if params.SkipSeed {
		out = append(out, "skip_seed is set: the seed did not run")
	}
	if params.SkipCatalog {
		out = append(out, "skip_catalog is set: the catalog payloads did not run")
	}
	if params.LibraryExportPath == "" {
		n, err := kv.CountPrefix(authority.SourcePrefix + authority.SourceLibraryExport + ":")
		if err != nil {
			return nil, fmt.Errorf("count owner-export ledger rows: %w", err)
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("the store holds %d owner-export ledger rows but no library_export_path was given", n))
		}
	}
	for _, src := range []string{authority.SourceCatalog, authority.SourceLibraryExport} {
		if st := rep.Sources[src]; st != nil && st.Undecodable > 0 {
			out = append(out, fmt.Sprintf("%d %s payloads did not decode", st.Undecodable, src))
		}
	}
	return out, nil
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func ingestLibraryExport(ctx context.Context, b *authoritybuild.Builder, path string, workers int, env authorityBuildEnv) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("library_export_path %q must be absolute", path)
	}
	fi, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("library_export_path: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("library_export_path %q is not a regular file", path)
	}
	f, err := os.Open(clean)
	if err != nil {
		return fmt.Errorf("library_export_path: %w", err)
	}
	items, err := authoritybuild.ReadLibraryExport(f, authoritybuild.MaxLibraryExportBytes)
	if cerr := f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("close library export: %w", cerr)
	}
	if err != nil {
		return err
	}
	b.NoteSource(authority.SourceLibraryExport)
	var done, bad atomic.Int64
	err = env.Items(ctx, len(items), t6g1ItemsOptions{
		Concurrency: workers,
		// Label runs inside each worker goroutine: read the atomic only.
		Label: func(_, total int) string {
			return fmt.Sprintf("library export: %d/%d", done.Load(), total)
		},
	}, func(_ context.Context, i int) error {
		// An undecodable item is counted by the builder and reported; one
		// bad item does not fail the export.
		if err := b.AddRawProduct(authority.SourceLibraryExport, authoritybuild.ExportTiebreak(i), items[i]); err != nil {
			logUndecodable(env.t6g1Env, &bad, authority.SourceLibraryExport, err)
		}
		done.Add(1)
		return nil
	})
	if err != nil {
		return fmt.Errorf("library export: %w", err)
	}
	env.Log(slog.LevelInfo, fmt.Sprintf("authority-build: library export %d items read", len(items)))
	return nil
}

func ingestCatalogRaw(ctx context.Context, env authorityBuildEnv, b *authoritybuild.Builder, workers int) error {
	total, err := env.Store.CountPrefix(authoritybuild.CatalogRawPrefix)
	if err != nil {
		return fmt.Errorf("count catalog payloads: %w", err)
	}
	env.Log(slog.LevelInfo, fmt.Sprintf("authority-build: catalog payloads=%d", total))
	b.NoteSource(authority.SourceCatalog)
	var done, bad atomic.Int64
	offset := 0
	err = authoritybuild.ScanCatalogRaw(ctx, env.Store, authorityCatalogPageSize, func(pairs []database.KVPair) error {
		err := env.Items(ctx, len(pairs), t6g1ItemsOptions{
			Concurrency:   workers,
			ProgressBase:  offset,
			ProgressTotal: int(max(total, int64(offset+len(pairs)))),
			Label: func(_, _ int) string {
				return fmt.Sprintf("catalog payloads: %d/%d", done.Load(), total)
			},
		}, func(_ context.Context, i int) error {
			kvp := pairs[i]
			if err := b.AddRawProduct(authority.SourceCatalog, kvp.Key, kvp.Value); err != nil {
				logUndecodable(env.t6g1Env, &bad, authority.SourceCatalog+" "+kvp.Key, err)
			}
			done.Add(1)
			return nil
		})
		offset += len(pairs)
		return err
	})
	if err != nil {
		return fmt.Errorf("catalog payloads: %w", err)
	}
	return nil
}

// authorityUndecodableLogLimit caps the per-payload decode errors logged;
// the builder counts every one.
const authorityUndecodableLogLimit = 10

func logUndecodable(env t6g1Env, n *atomic.Int64, what string, err error) {
	if n.Add(1) <= authorityUndecodableLogLimit {
		env.Log(slog.LevelWarn, fmt.Sprintf("authority-build: undecodable payload (%s): %v", what, err))
	}
}

func logAuthorityReport(env t6g1Env, mode string, out *authorityBuildOutcome) {
	r := out.Report
	srcNames := make([]string, 0, len(r.Sources))
	for s := range r.Sources {
		srcNames = append(srcNames, s)
	}
	sort.Strings(srcNames)
	var srcs []string
	for _, s := range srcNames {
		st := r.Sources[s]
		srcs = append(srcs, fmt.Sprintf("%s{items=%d dup=%d skipped=%d undecodable=%d cast=%d manual_only=%d}",
			s, st.Items, st.Duplicates, st.Skipped, st.Undecodable, st.CastContext, st.ManualOnly))
	}
	summary := fmt.Sprintf("authority-build %s: persons=%d (O=%d A=%d B=%d C=%d) publishers=%d asins=%d "+
		"author_evidence=%d cast_only=%d homonyms=%d asin_spellings=%d single_word=%d | would write=%d prune=%d held=%d | %s",
		mode, r.Persons, r.PersonsByTier[authority.TierO], r.PersonsByTier[authority.TierA],
		r.PersonsByTier[authority.TierB], r.PersonsByTier[authority.TierC], r.Publishers, r.ASINs,
		r.AuthorEvidence, r.CastOnly, r.Homonyms, r.SpellingASINs, r.SingleWord, out.Writes, out.Stale, len(out.Result.Held),
		strings.Join(srcs, " "))
	env.Log(slog.LevelInfo, summary)

	for _, prefix := range authority.RebuildablePrefixes() {
		c := out.Counts[prefix]
		if c == nil {
			continue
		}
		env.Log(slog.LevelInfo, fmt.Sprintf("authority-build: %s write=%d unchanged=%d stale=%d held=%d", prefix, c.Write, c.Unchanged, c.Stale, c.Held),
			slog.String("prefix", prefix), slog.Int("write", c.Write), slog.Int("unchanged", c.Unchanged), slog.Int("stale", c.Stale))
	}
	for label, samples := range map[string][]authoritybuild.NameSample{
		"homonym": r.HomonymSample, "asin_spelling": r.SpellingSample, "cast_only": r.CastOnlySample,
		"single_word": r.SingleWordSample, "person": r.PersonSample, "publisher": r.PublisherSample,
	} {
		for _, s := range samples {
			env.Log(slog.LevelInfo,
				fmt.Sprintf("authority-build sample=%s name=%q fold=%s tier=%s asins=%s folds=%s",
					label, s.Display, s.Fold, s.Tier, strings.Join(s.ASINs, ","), strings.Join(s.Folds, ",")),
				slog.String("sample", label), slog.String("name", s.Display), slog.String("fold", s.Fold))
		}
	}
	if raw, err := json.Marshal(r); err == nil {
		env.Log(slog.LevelInfo, "authority-build report", slog.String("report", string(raw)))
	}
}
