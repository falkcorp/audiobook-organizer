// file: internal/plugins/maintenance/authority_build.go
// version: 1.0.0
// guid: 7b057b54-4781-486b-b075-fe627504dcf1
// last-edited: 2026-10-04

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
// single-word persons, samples, and what an apply would write and prune, and
// writes nothing. An apply writes ref_person/ref_pub/ref_asin/ref_src keys
// only, and never touches owner overrides (ref_ovr:). Nothing reads the index
// yet; consumers come behind a flag in later PRs.

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
			"names and samples, and what an apply would write or prune. dry_run=false writes only the " +
			"ref_person/ref_pub/ref_asin/ref_src keys and never touches owner overrides. No network. " +
			"Nothing reads the lists yet.",
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
	_, err = buildAuthorityLists(ctx, store, params, dryRun, reporter, time.Now())
	return err
}

// authorityBuildOutcome is what one run did; tests read it.
type authorityBuildOutcome struct {
	Report authority.Report
	Counts map[string]*authority.PrefixCounts
	Stale  int
	Writes int
	Apply  authority.ApplyResult
	DryRun bool
}

func authorityWorkers(n int) int {
	if n <= 0 {
		n = runtime.NumCPU()
	}
	return min(max(1, n), authorityMaxConcurrency)
}

// buildAuthorityLists runs the build on any raw KV store. The production
// store is OpsStore, which embeds database.RawKVStore, so the indexedStore
// wrapper serves it directly.
func buildAuthorityLists(ctx context.Context, kv database.RawKVStore, params authorityBuildParams, dryRun bool,
	reporter sdk.Reporter, now time.Time) (*authorityBuildOutcome, error) {
	workers := authorityWorkers(params.Concurrency)
	mode := map[bool]string{true: "DRY RUN (nothing written)", false: "APPLY"}[dryRun]
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("authority-build: %s, %d workers", mode, workers))

	b := authority.NewBuilder()

	if !params.SkipSeed {
		seed, err := authority.LoadSeed()
		if err != nil {
			return nil, err
		}
		b.AddSeed(seed)
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("authority-build: seed v%s, %d entries", seed.Version, len(seed.Entries)))
	}

	if params.LibraryExportPath != "" {
		if err := ingestLibraryExport(ctx, b, params.LibraryExportPath, workers, reporter); err != nil {
			return nil, err
		}
	}

	if !params.SkipCatalog {
		if err := ingestCatalogRaw(ctx, kv, b, workers, reporter); err != nil {
			return nil, err
		}
	}

	res := b.Finish()
	plan, err := authority.PlanApply(ctx, kv, res, now, workers)
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}
	out := &authorityBuildOutcome{Report: res.Report, Counts: plan.Counts, Stale: len(plan.Stale), Writes: len(plan.Writes), DryRun: dryRun}
	logAuthorityReport(reporter, mode, out)

	if dryRun {
		return out, nil
	}
	out.Apply, err = authority.Apply(ctx, kv, plan, workers)
	if err != nil {
		return out, fmt.Errorf("apply: %w", err)
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("authority-build: APPLIED written=%d deleted=%d", out.Apply.Written, out.Apply.Deleted))
	return out, nil
}

func ingestLibraryExport(ctx context.Context, b *authority.Builder, path string, workers int, reporter sdk.Reporter) error {
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
	items, err := authority.ReadLibraryExport(f, authority.MaxLibraryExportBytes)
	if cerr := f.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("close library export: %w", cerr)
	}
	if err != nil {
		return err
	}
	b.NoteSource(authority.SourceLibraryExport)
	var done, bad atomic.Int64
	err = registry.RunItems(ctx, reporter, items, func(_ context.Context, item json.RawMessage) error {
		// An undecodable item is counted by the builder and reported; one
		// bad item does not fail the export.
		if err := b.AddRawProduct(authority.SourceLibraryExport, item); err != nil {
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

func ingestCatalogRaw(ctx context.Context, kv database.RawKVStore, b *authority.Builder, workers int, reporter sdk.Reporter) error {
	total, err := kv.CountPrefix(authority.CatalogRawPrefix)
	if err != nil {
		return fmt.Errorf("count catalog payloads: %w", err)
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("authority-build: catalog payloads=%d", total))
	b.NoteSource(authority.SourceCatalog)
	var done, bad atomic.Int64
	offset := 0
	err = authority.ScanCatalogRaw(ctx, kv, authorityCatalogPageSize, func(pairs []database.KVPair) error {
		err := registry.RunItems(ctx, reporter, pairs, func(_ context.Context, kvp database.KVPair) error {
			if err := b.AddRawProduct(authority.SourceCatalog, kvp.Value); err != nil {
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
		"author_evidence=%d cast_only=%d homonyms=%d asin_spellings=%d single_word=%d | would write=%d prune=%d | %s",
		mode, r.Persons, r.PersonsByTier[authority.TierO], r.PersonsByTier[authority.TierA],
		r.PersonsByTier[authority.TierB], r.PersonsByTier[authority.TierC], r.Publishers, r.ASINs,
		r.AuthorEvidence, r.CastOnly, r.Homonyms, r.SpellingASINs, r.SingleWord, out.Writes, out.Stale,
		strings.Join(srcs, " "))
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(1, 1, summary)

	for _, prefix := range authority.RebuildablePrefixes() {
		c := out.Counts[prefix]
		if c == nil {
			continue
		}
		_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("authority-build: %s write=%d unchanged=%d stale=%d", prefix, c.Write, c.Unchanged, c.Stale),
			slog.String("prefix", prefix), slog.Int("write", c.Write), slog.Int("unchanged", c.Unchanged), slog.Int("stale", c.Stale))
	}
	for label, samples := range map[string][]authority.NameSample{
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
