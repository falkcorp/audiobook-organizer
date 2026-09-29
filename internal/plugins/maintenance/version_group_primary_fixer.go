// file: internal/plugins/maintenance/version_group_primary_fixer.go
// version: 1.0.1
// guid: 2c7e5a19-8b43-4f06-9d21-4e8b0c6a3f75
// last-edited: 2026-09-29

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// vgPrimaryFixerID is the Repairs-lane id of the version-group primary
// repair. It equals vgRepairSource so history rows written through the lane
// read the same as the op's.
const vgPrimaryFixerID = vgRepairSource

// vgPrimaryFixer adapts maintenance.version-group-primary-repair to the
// Repairs framework WITHOUT a second copy of its logic: Plan runs the op's
// own dry run (versionGroupPrimaryRepair with every group kept), Replan runs
// the op's own per-group planner (vgApplier.planGroup) and Apply runs the
// op's own writer (vgApplier.write). The op keeps working unchanged beside it.
type vgPrimaryFixer struct {
	p *Plugin
	// probe replaces ffprobe when set (tests).
	probe versionprimary.ChapterProber

	// seriesMu guards a short-lived series-name cache for Replan, which runs
	// once per applied row: re-listing every series per row would be the
	// dominant cost of an apply.
	seriesMu      sync.Mutex
	seriesNames   map[int]string
	seriesFetched time.Time
}

func newVGPrimaryFixer(p *Plugin) *vgPrimaryFixer { return &vgPrimaryFixer{p: p} }

var _ repairs.Fixer = (*vgPrimaryFixer)(nil)

func (f *vgPrimaryFixer) ID() string    { return vgPrimaryFixerID }
func (f *vgPrimaryFixer) Title() string { return "Version-group primary" }
func (f *vgPrimaryFixer) Description() string {
	return "Groups with no primary copy or more than one. Elects the one copy ABS should list " +
		"(m4b with chapters > m4b without > metadata > other formats; only an organized copy with all " +
		"its files under the library root can win), demotes the others, and fills the winner's EMPTY " +
		"fields from the best-metadata copy. Groups whose better copy is outside the library are held."
}

// vgFixerParams are the fixer params (the op's, minus the write switches).
type vgFixerParams struct {
	GroupIDs []string `json:"group_ids,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

func decodeVGFixerParams(raw json.RawMessage) (vgFixerParams, error) {
	var fp vgFixerParams
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &fp); err != nil {
			return fp, fmt.Errorf("%s: invalid params: %w", vgPrimaryFixerID, err)
		}
	}
	return fp, nil
}

func (f *vgPrimaryFixer) prober(log func(string)) versionprimary.ChapterProber {
	if f.probe != nil {
		return f.probe
	}
	prober, perr := versionprimary.FFprobeChapterCounter()
	if perr != nil {
		log(fmt.Sprintf("%s: ffprobe unavailable (%v); chapter counts come from the chapter table", vgPrimaryFixerID, perr))
		return nil
	}
	return prober
}

// Plan runs the op's dry run over the whole library (or params.group_ids)
// with no detail cap, and turns every candidate group into a row.
func (f *vgPrimaryFixer) Plan(ctx context.Context, raw json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	fp, err := decodeVGFixerParams(raw)
	if err != nil {
		return nil, err
	}
	prober := f.prober(func(msg string) { _ = rep.Log(slog.LevelWarn, msg) })
	report, err := f.p.versionGroupPrimaryRepair(ctx, vgPrimaryRepairParams{
		GroupIDs: fp.GroupIDs, Limit: fp.Limit, noDetailCap: true,
	}, config.AppConfig.RootDir, prober, rep)
	if err != nil {
		return nil, err
	}
	rows := make([]repairs.Row, 0, len(report.Groups))
	for i := range report.Groups {
		r := f.row(&report.Groups[i])
		// Detail carries a group from Replan to Apply; a stored plan never
		// needs it, and dropping it lets each group's member rows go.
		r.Detail = nil
		rows = append(rows, r)
	}
	return rows, nil
}

// Replan plans one group now, as the op's per-group planner would.
func (f *vgPrimaryFixer) Replan(ctx context.Context, _ json.RawMessage, planned repairs.Row, rep registry.Reporter) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	vps := f.p.deps.VersionPrimaryStore()
	if store == nil || vps == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	names, err := f.series(store)
	if err != nil {
		return repairs.Row{}, err
	}
	a := &vgApplier{store: store, reporter: rep, seriesNames: names, paths: repairs.NewPathResolver()}
	loader := versionprimary.Loader{Files: store, Chapters: vps, RootDir: config.AppConfig.RootDir,
		Probe: f.prober(func(msg string) { _ = rep.Log(slog.LevelWarn, msg) })}
	g := a.planGroup(ctx, loader, planned.RowID)
	return f.row(&g), nil
}

// Apply writes one fresh group through the op's writer.
func (f *vgPrimaryFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	g, ok := fresh.Detail.(*vgRepairGroupReport)
	if !ok || g == nil {
		return fmt.Errorf("%s: row %s carries no group plan", vgPrimaryFixerID, fresh.RowID)
	}
	if !vgWritable(g.Kind) || g.Error != "" {
		return fmt.Errorf("%s: group %s is not writable (%s)", vgPrimaryFixerID, g.GroupID, g.Kind)
	}
	store := f.p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	a := &vgApplier{store: store, apply: true, writer: w}
	a.write(g.GroupID, g.planned, g)
	switch g.Outcome {
	case vgOutcomeApplied:
		return nil
	case vgOutcomeChangedSincePlan:
		return repairs.ErrChangedSincePlan
	case vgOutcomePartial:
		return fmt.Errorf("%w: group %s: the winner is in place but a member changed before every demotion was written",
			repairs.ErrPartiallyApplied, g.GroupID)
	default:
		if g.Error == "" {
			return errors.New("write failed")
		}
		return errors.New(g.Error)
	}
}

func (f *vgPrimaryFixer) series(store OpsStore) (map[int]string, error) {
	f.seriesMu.Lock()
	defer f.seriesMu.Unlock()
	if f.seriesNames != nil && time.Since(f.seriesFetched) < time.Minute {
		return f.seriesNames, nil
	}
	all, err := store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("list series: %w", err)
	}
	m := make(map[int]string, len(all))
	for _, s := range all {
		m[s.ID] = s.Name
	}
	f.seriesNames, f.seriesFetched = m, time.Now()
	return m, nil
}

// row turns one group report into a Repairs row.
func (f *vgPrimaryFixer) row(g *vgRepairGroupReport) repairs.Row {
	r := repairs.Row{RowID: g.GroupID, Risk: repairs.RiskLow, Detail: g, Fingerprint: vgFingerprint(g)}
	var primaries []string
	byID := map[string]*database.Book{}
	for i := range g.planned {
		b := &g.planned[i]
		byID[b.ID] = b
		if !b.IsSoftDeleted() {
			r.BookIDs = append(r.BookIDs, b.ID)
		}
		if b.IsPrimaryVersion != nil && *b.IsPrimaryVersion {
			primaries = append(primaries, b.ID)
		}
	}
	sort.Strings(r.BookIDs)
	sort.Strings(primaries)
	show := byID[g.WinnerID]
	if show == nil && len(r.BookIDs) > 0 {
		show = byID[r.BookIDs[0]]
	}
	if show != nil {
		r.Title = show.Title
		r.Author = f.authorName(show)
	}
	r.Current = map[string]string{
		"primaries":           strings.Join(primaries, ", "),
		"explicit_true":       strconv.Itoa(g.ExplicitTrue),
		"effective_primaries": strconv.Itoa(g.EffectivePrimaries),
		"members":             strconv.Itoa(len(r.BookIDs)),
	}
	r.Reason = g.Reason
	switch {
	case g.Error != "":
		r.Skipped, r.SkipReason = "error", g.Error
		r.Reason = g.Error
		return r
	case !vgWritable(g.Kind):
		r.Skipped = vgSkipKey(g)
		r.SkipReason = g.SkipReason
		if r.SkipReason == "" {
			r.SkipReason = g.Reason
		}
		return r
	}
	r.Proposed = map[string]string{"decision": g.Kind}
	if g.Kind != vgDecisionDemoteNonLive {
		r.Proposed["primary"] = g.WinnerID
	}
	if len(g.DemotedIDs) > 0 {
		r.Proposed["demote"] = strings.Join(g.DemotedIDs, ", ")
	}
	if g.RevivedID != "" {
		r.Proposed["revive"] = g.RevivedID
		r.Risk = repairs.RiskReview
	}
	if g.CarryOver != nil && len(g.CarryOver.Fills) > 0 {
		var fields []string
		for _, fl := range g.CarryOver.Fills {
			fields = append(fields, fl.Field)
		}
		r.Proposed["fill_from_"+g.CarryOver.DonorID] = strings.Join(fields, ", ")
		r.Risk = repairs.RiskReview
	}
	if g.ChapterCountUnknown {
		r.Risk = repairs.RiskReview
	}
	return r
}

func (f *vgPrimaryFixer) authorName(b *database.Book) string {
	if b.AuthorID == nil {
		return ""
	}
	store := f.p.deps.OpsStore()
	if store == nil {
		return ""
	}
	a, err := store.GetAuthorByID(*b.AuthorID)
	if err != nil || a == nil {
		return ""
	}
	return a.Name
}

// vgFingerprint hashes everything the group's decision was made from and
// everything a write would do: each member's changed-since-plan key, the
// decision, the winner, the demotions, the revive and the carried fields.
// Elect also reads file presence and chapter counts, which no member key
// holds; they reach the fingerprint through the decision they produce.
func sortedCopy(ids []string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

func vgFingerprint(g *vgRepairGroupReport) string {
	var b strings.Builder
	keys := vgKeys(g.planned)
	ids := make([]string, 0, len(keys))
	for id := range keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		k := keys[id]
		b.WriteString(fmt.Sprintf("m|%s|%s|%s|%s|%v\n", id, k.primary, k.mergedInto, k.state, k.softDeleted))
	}
	b.WriteString(fmt.Sprintf("d|%s|%s|%s|%s|%s\n", g.Kind, g.HoldReason, g.WinnerID, g.MetadataBestID, g.RevivedID))
	b.WriteString("x|" + strings.Join(sortedCopy(g.DemotedIDs), ",") + "\n")
	b.WriteString("n|" + strings.Join(sortedCopy(g.DemoteNonLive), ",") + "\n")
	if g.CarryOver != nil {
		for _, fl := range g.CarryOver.Fills {
			b.WriteString("f|" + fl.Field + "=" + fl.Value + "\n")
		}
	}
	b.WriteString("e|" + g.Error + "\n")
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:32]
}
