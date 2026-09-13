// file: internal/plugins/maintenance/version_group_primary_report.go
// version: 1.0.0
// guid: 3f8b1d64-9c2e-4a57-b0d3-6e41a9c8f215
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- version-group-primary-report ---
//
// WHY THIS EXISTS (VG-DOUBLE-PRIMARY, #2668). A version group must have
// exactly one primary member. Merge (126d00843) and regroup apply now keep that
// invariant going forward, but rows written before those fixes do not
// self-heal, and the issue's prod measurement (10 of 15 sampled groups) was one
// offset window, not a library-wide rate. This op sizes the repair.
//
// Primacy is read with database.EffectiveIsPrimaryVersion (nil == primary),
// the same reading the memdb is_primary_version index and the library list
// use, because that is the reading under which a double shows twice in the UI.
// Counting only explicit true would under-report by exactly the nil-flag
// population the regroup bug created. Each group is also classified by its
// STORED flags, because the repair differs: a double with two explicit trues
// needs an election, while a double that exists only because of a nil flag
// can be repaired by writing an explicit false.
//
// Zero-primary groups (every live member explicitly false) are counted too:
// they are the opposite failure, and a repair that demotes must not create
// more of them.
//
// REPORT ONLY. No writes; CapLibraryRead only; no schedule (manual trigger).

// vgPrimarySampleLimit caps the double-primary groups logged individually.
const vgPrimarySampleLimit = 200

type vgPrimaryMember struct {
	ID string
	// Stored is the raw tri-state: "true", "false" or "nil".
	Stored string
}

type vgPrimaryGroup struct {
	GroupID           string
	Members           []vgPrimaryMember // ordered by ID
	EffectivePrimary  int
	ExplicitPrimaries int
}

type vgPrimaryReport struct {
	TotalBooks  int
	SoftDeleted int // skipped: not live, never listed
	Groups      int // distinct non-empty VersionGroupIDs over live books
	// DoubleGroups have more than one EFFECTIVE primary. Split into
	// DoubleExplicit (two or more stored true) and DoubleNilOnly (at most one
	// stored true; the rest of the effective primaries are nil flags).
	DoubleGroups   int
	DoubleExplicit int
	DoubleNilOnly  int
	BooksInDoubles int
	ZeroPrimary    int
	Sample         []vgPrimaryGroup
}

func (r vgPrimaryReport) summary() string {
	return fmt.Sprintf(
		"REPORT ONLY (nothing changed) — books=%d soft_deleted(skipped)=%d version_groups=%d "+
			"double_primary_groups=%d (explicit=%d nil_only=%d) books_in_double_groups=%d zero_primary_groups=%d",
		r.TotalBooks, r.SoftDeleted, r.Groups, r.DoubleGroups, r.DoubleExplicit, r.DoubleNilOnly,
		r.BooksInDoubles, r.ZeroPrimary)
}

func storedPrimaryFlag(flag *bool) string {
	switch {
	case flag == nil:
		return "nil"
	case *flag:
		return "true"
	default:
		return "false"
	}
}

// findVersionGroupPrimaryViolations groups live books by VersionGroupID and
// reports groups whose effective primary count is not exactly one.
//
// A plain loop, not a worker pool: the per-book work is a map insert over an
// already-loaded slice, with no DB, network or subprocess call per item.
func findVersionGroupPrimaryViolations(books []database.BookCore, sampleLimit int) vgPrimaryReport {
	report := vgPrimaryReport{TotalBooks: len(books)}
	byGroup := map[string][]*database.BookCore{}
	for i := range books {
		b := &books[i]
		if b.IsSoftDeleted() {
			report.SoftDeleted++
			continue
		}
		if b.VersionGroupID == nil || *b.VersionGroupID == "" {
			continue
		}
		byGroup[*b.VersionGroupID] = append(byGroup[*b.VersionGroupID], b)
	}
	report.Groups = len(byGroup)

	var doubles []vgPrimaryGroup
	for gid, members := range byGroup {
		g := vgPrimaryGroup{GroupID: gid}
		for _, b := range members {
			if database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion) {
				g.EffectivePrimary++
			}
			if b.IsPrimaryVersion != nil && *b.IsPrimaryVersion {
				g.ExplicitPrimaries++
			}
			g.Members = append(g.Members, vgPrimaryMember{ID: b.ID, Stored: storedPrimaryFlag(b.IsPrimaryVersion)})
		}
		switch {
		case g.EffectivePrimary == 0:
			report.ZeroPrimary++
		case g.EffectivePrimary > 1:
			report.DoubleGroups++
			report.BooksInDoubles += len(members)
			if g.ExplicitPrimaries > 1 {
				report.DoubleExplicit++
			} else {
				report.DoubleNilOnly++
			}
			sort.Slice(g.Members, func(i, j int) bool { return g.Members[i].ID < g.Members[j].ID })
			doubles = append(doubles, g)
		}
	}
	// Deterministic order so two runs can be diffed.
	sort.Slice(doubles, func(i, j int) bool { return doubles[i].GroupID < doubles[j].GroupID })
	if sampleLimit >= 0 && len(doubles) > sampleLimit {
		doubles = doubles[:sampleLimit]
	}
	report.Sample = doubles
	return report
}

func formatVGPrimaryGroup(g vgPrimaryGroup) string {
	parts := make([]string, 0, len(g.Members))
	for _, m := range g.Members {
		parts = append(parts, fmt.Sprintf("%s=%s", m.ID, m.Stored))
	}
	return fmt.Sprintf("double-primary group %s effective_primaries=%d explicit_true=%d members=[%s]",
		g.GroupID, g.EffectivePrimary, g.ExplicitPrimaries, strings.Join(parts, "; "))
}

func (p *Plugin) versionGroupPrimaryReportDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.version-group-primary-report",
		Liveness:    sdk.LivenessManual,
		Plugin:      "maintenance",
		DisplayName: "Version-group primary report",
		Description: "REPORT ONLY — changes nothing. Counts version groups with more than one " +
			"effective primary member (is_primary_version true or unset), split into groups with " +
			"two or more explicit trues and groups that are double only because of an unset flag, " +
			"plus groups with no primary at all. Logs up to 200 double-primary groups with each " +
			"member's stored flag. Sizes the VG-DOUBLE-PRIMARY repair.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.version-group-primary-report",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         10 * time.Minute,
		// No Schedule: manual trigger only.
		Capabilities: []sdk.Capability{sdk.CapLibraryRead},
		Run:          p.runVersionGroupPrimaryReport,
	}
}

func (p *Plugin) runVersionGroupPrimaryReport(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	_ = reporter.Log(slog.LevelInfo, "Starting version-group primary report (report only)")
	_ = reporter.UpdateProgress(0, 2, "Listing books…")

	// One limit-0 read = one consistent snapshot (offset pages over the async
	// memdb can skip or repeat rows, #2443). The Complete variant refuses a
	// memdb known to be missing rows: a census that silently undercounts is
	// the artifact someone would later cite to size a repair.
	books, err := store.GetAllBooksCoreComplete(0, 0)
	if err != nil {
		return fmt.Errorf("list books: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	_ = reporter.UpdateProgress(1, 2, fmt.Sprintf("Grouping %d books by version group…", len(books)))
	report := findVersionGroupPrimaryViolations(books, vgPrimarySampleLimit)

	summary := report.summary()
	_ = reporter.Log(slog.LevelInfo, summary)
	for _, g := range report.Sample {
		_ = reporter.Log(slog.LevelInfo, formatVGPrimaryGroup(g),
			slog.String("version_group", g.GroupID),
			slog.Int("effective_primaries", g.EffectivePrimary),
			slog.Int("explicit_true", g.ExplicitPrimaries))
	}
	_ = reporter.UpdateProgress(2, 2, summary)
	return nil
}
