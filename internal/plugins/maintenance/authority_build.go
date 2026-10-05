// file: internal/plugins/maintenance/authority_build.go
// version: 1.1.0
// guid: 7b057b54-4781-486b-b075-fe627504dcf1
// last-edited: 2026-10-05

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
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
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

func (p *Plugin) authorityBuildDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          authorityBuildOpID,
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Authority lists: build",
		Description: "Builds the authority lists (known authors, narrators, cast members and publishers, " +
			"with contributor ASINs and evidence tiers) from the embedded owner-library seed, the author " +
			"catalog's stored provider payloads, and optionally an owner library export " +
			"(library_export_path). DRY RUN BY DEFAULT: reports per-source and per-tier counts, homonym " +
			"conflicts (one name, several ASINs: reported, never picked), cast-only names, single-word " +
			"names and samples, and stores the plan (digest, the full prune list, held rows, refusals) as " +
			"the op result. dry_run=false needs plan_op_id (a reviewed dry run) and applies exactly that " +
			"plan, refusing if the store or sources changed, any source was skipped or any payload failed " +
			"to decode. It writes only ref_person/ref_pub/ref_asin/ref_src keys, prunes only rows whose " +
			"every source ran, and never touches owner overrides. No network. Nothing reads the lists yet.",
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
	store := p.deps.OpsStore()
	if store == nil {
		return errors.New("database not initialized")
	}
	expect := ""
	if !dryRun {
		if params.PlanOpID == "" {
			return fmt.Errorf("%s: dry_run=false needs plan_op_id (the reviewed dry run to apply)", authorityBuildOpID)
		}
		if params.sources() != (authoritySources{}) {
			return fmt.Errorf("%s: an apply takes its sources from plan %s; leave skip_seed, skip_catalog and library_export_path unset", authorityBuildOpID, params.PlanOpID)
		}
		r, ok := p.deps.OperationQueueStore().(authorityPlanReader)
		if !ok {
			return fmt.Errorf("%s: operation store cannot read the plan op", authorityBuildOpID)
		}
		plan, err := loadAuthorityPlan(r, params.PlanOpID)
		if err != nil {
			return err
		}
		params.SkipSeed, params.SkipCatalog, params.LibraryExportPath = plan.Sources.SkipSeed, plan.Sources.SkipCatalog, plan.Sources.LibraryExportPath
		expect = plan.Digest
	}
	out, err := buildAuthorityLists(ctx, store, params, dryRun, expect, reporter, time.Now())
	if out != nil {
		if serr := registry.ReporterSetResult(reporter, out.Result); serr != nil && err == nil {
			err = fmt.Errorf("%s: store result: %w", authorityBuildOpID, serr)
		}
	}
	return err
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
func authorityProgress(reporter sdk.Reporter) func(phase string, done, total int) {
	spans := map[string][2]int{"plan": {0, 500}, "apply": {500, 950}, "prune": {950, 1000}}
	return func(phase string, done, total int) {
		sp, ok := spans[phase]
		if !ok || total <= 0 {
			return
		}
		cur := sp[0] + (sp[1]-sp[0])*min(done, total)/total
		_ = reporter.UpdateProgress(cur, 1000, fmt.Sprintf("authority-build %s: %d/%d", phase, done, total))
	}
}

// buildAuthorityLists runs the build on any raw KV store. The production
// store is OpsStore, which embeds database.RawKVStore, so the indexedStore
// wrapper serves it directly. On an apply, expectDigest is the reviewed dry
// run's plan digest: the apply refuses unless the recomputed plan matches it.
func buildAuthorityLists(ctx context.Context, kv database.RawKVStore, params authorityBuildParams, dryRun bool,
	expectDigest string, reporter sdk.Reporter, now time.Time) (*authorityBuildOutcome, error) {
	if !dryRun && expectDigest == "" {
		return nil, errors.New("authority-build: an apply needs the reviewed plan's digest")
	}
	workers := authorityWorkers(params.Concurrency)
	mode := map[bool]string{true: "DRY RUN (nothing written)", false: "APPLY"}[dryRun]
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("authority-build: %s, %d workers", mode, workers))

	b := authoritybuild.NewBuilder()
	ran := map[string]bool{}

	if !params.SkipSeed {
		seed, err := authoritybuild.LoadSeed()
		if err != nil {
			return nil, err
		}
		b.AddSeed(seed)
		ran[authority.SourceSeed] = true
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("authority-build: seed v%s, %d entries", seed.Version, len(seed.Entries)))
	}

	if params.LibraryExportPath != "" {
		if err := ingestLibraryExport(ctx, b, params.LibraryExportPath, workers, reporter); err != nil {
			return nil, err
		}
		ran[authority.SourceLibraryExport] = true
	}

	if !params.SkipCatalog {
		if err := ingestCatalogRaw(ctx, kv, b, workers, reporter); err != nil {
			return nil, err
		}
		ran[authority.SourceCatalog] = true
	}

	res := b.Finish()
	refusals, err := authorityRefusals(kv, params, res.Report)
	if err != nil {
		return nil, err
	}
	opt := authoritybuild.Options{
		Workers: workers,
		// Plan and apply are errgroup pools, not RunItems, so they stamp the
		// watchdog through UpdateProgress themselves (throttled).
		Progress: authorityProgress(reporter),
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
	} else if id := registry.ReporterOpID(reporter); id != "" && len(refusals) == 0 {
		out.Result.ApplyParams = map[string]any{"dry_run": false, "plan_op_id": id}
	}
	out.Result.Summary = fmt.Sprintf("%s: %d writes, %d prunes, %d held, %d persons, %d publishers; %s",
		map[bool]string{true: "DRY RUN", false: "APPLY"}[dryRun], len(plan.Writes), len(plan.Stale), len(plan.Held),
		res.Report.Persons, res.Report.Publishers,
		map[bool]string{true: "apply allowed", false: fmt.Sprintf("apply refused (%d reasons)", len(refusals))}[len(refusals) == 0])
	logAuthorityReport(reporter, mode, out)
	for _, r := range refusals {
		_ = reporter.Log(slog.LevelWarn, "authority-build: an apply would be refused: "+r)
	}

	if dryRun {
		_ = reporter.UpdateProgress(1000, 1000, "authority-build: dry run planned")
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
	_ = reporter.UpdateProgress(1000, 1000, "authority-build: applied")
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("authority-build: APPLIED plan %s written=%d deleted=%d", params.PlanOpID, out.Apply.Written, out.Apply.Deleted))
	return out, nil
}

// authorityRefusals lists why an apply of this build must not run: a
// skipped source, owner-export rows in the store with no export supplied, or
// any payload that failed to decode. Each would make the build partial, and
// a partial build must not prune.
func authorityRefusals(kv database.RawKVStore, params authorityBuildParams, rep authoritybuild.Report) ([]string, error) {
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

// authorityExportItem is one export item with its position (the duplicate
// tiebreak).
type authorityExportItem struct {
	i   int
	raw json.RawMessage
}

func ingestLibraryExport(ctx context.Context, b *authoritybuild.Builder, path string, workers int, reporter sdk.Reporter) error {
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
	indexed := make([]authorityExportItem, len(items))
	for i, it := range items {
		indexed[i] = authorityExportItem{i: i, raw: it}
	}
	err = registry.RunItems(ctx, reporter, indexed, func(_ context.Context, it authorityExportItem) error {
		// An undecodable item is counted by the builder and reported; one
		// bad item does not fail the export.
		if err := b.AddRawProduct(authority.SourceLibraryExport, authoritybuild.ExportTiebreak(it.i), it.raw); err != nil {
			logUndecodable(reporter, &bad, authority.SourceLibraryExport, err)
		}
		done.Add(1)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: workers,
		// Label runs inside each worker goroutine: read the atomic only.
		Label: func(_, total int) string {
			return fmt.Sprintf("library export: %d/%d", done.Load(), total)
		},
	})
	if err != nil {
		return fmt.Errorf("library export: %w", err)
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("authority-build: library export %d items read", len(items)))
	return nil
}

func ingestCatalogRaw(ctx context.Context, kv database.RawKVStore, b *authoritybuild.Builder, workers int, reporter sdk.Reporter) error {
	total, err := kv.CountPrefix(authoritybuild.CatalogRawPrefix)
	if err != nil {
		return fmt.Errorf("count catalog payloads: %w", err)
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("authority-build: catalog payloads=%d", total))
	b.NoteSource(authority.SourceCatalog)
	var done, bad atomic.Int64
	offset := 0
	err = authoritybuild.ScanCatalogRaw(ctx, kv, authorityCatalogPageSize, func(pairs []database.KVPair) error {
		err := registry.RunItems(ctx, reporter, pairs, func(_ context.Context, kvp database.KVPair) error {
			if err := b.AddRawProduct(authority.SourceCatalog, kvp.Key, kvp.Value); err != nil {
				logUndecodable(reporter, &bad, authority.SourceCatalog+" "+kvp.Key, err)
			}
			done.Add(1)
			return nil
		}, registry.RunItemsOptions{
			Concurrency:    workers,
			ProgressOffset: offset,
			ProgressTotal:  int(max(total, int64(offset+len(pairs)))),
			Label: func(_, _ int) string {
				return fmt.Sprintf("catalog payloads: %d/%d", done.Load(), total)
			},
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

func logUndecodable(reporter sdk.Reporter, n *atomic.Int64, what string, err error) {
	if n.Add(1) <= authorityUndecodableLogLimit {
		_ = reporter.Log(slog.LevelWarn, fmt.Sprintf("authority-build: undecodable payload (%s): %v", what, err))
	}
}

func logAuthorityReport(reporter sdk.Reporter, mode string, out *authorityBuildOutcome) {
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
	_ = reporter.Log(slog.LevelInfo, summary)

	for _, prefix := range authority.RebuildablePrefixes() {
		c := out.Counts[prefix]
		if c == nil {
			continue
		}
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("authority-build: %s write=%d unchanged=%d stale=%d held=%d", prefix, c.Write, c.Unchanged, c.Stale, c.Held),
			slog.String("prefix", prefix), slog.Int("write", c.Write), slog.Int("unchanged", c.Unchanged), slog.Int("stale", c.Stale))
	}
	for label, samples := range map[string][]authoritybuild.NameSample{
		"homonym": r.HomonymSample, "asin_spelling": r.SpellingSample, "cast_only": r.CastOnlySample,
		"single_word": r.SingleWordSample, "person": r.PersonSample, "publisher": r.PublisherSample,
	} {
		for _, s := range samples {
			_ = reporter.Log(slog.LevelInfo,
				fmt.Sprintf("authority-build sample=%s name=%q fold=%s tier=%s asins=%s folds=%s",
					label, s.Display, s.Fold, s.Tier, strings.Join(s.ASINs, ","), strings.Join(s.Folds, ",")),
				slog.String("sample", label), slog.String("name", s.Display), slog.String("fold", s.Fold))
		}
	}
	if raw, err := json.Marshal(r); err == nil {
		_ = reporter.Log(slog.LevelInfo, "authority-build report", slog.String("report", string(raw)))
	}
}
