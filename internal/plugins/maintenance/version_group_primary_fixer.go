// file: internal/plugins/maintenance/version_group_primary_fixer.go
// version: 1.2.1
// guid: 2c7e5a19-8b43-4f06-9d21-4e8b0c6a3f75
// last-edited: 2026-10-04

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
	r := f.row(&g)
	if r.Fingerprint != planned.Fingerprint {
		if n, ok := f.resumesOwnWrite(store, planned, &g); ok {
			// The group differs from the plan only by writes this fixer made
			// for this row (an apply cut off mid-row, by a lost stand-down
			// lease above all), and what is left to do is a subset of what
			// was approved: finish it rather than leave the group half-done.
			r.Fingerprint = planned.Fingerprint
			r.Reason = fmt.Sprintf("%s (resuming: %d of the planned write(s) already made by %s)", r.Reason, n, vgPrimaryFixerID)
		}
	}
	return r, nil
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
	a := &vgApplier{store: store, apply: true, writer: w, paths: repairs.NewPathResolver()}
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
		// Keep the write's error value: the engine classifies by the chain
		// (repairs.ErrStandDownLost aborts the row so a resume finishes it;
		// a stringified copy settled it as failed, leaving a crowned winner
		// next to an undemoted primary for good).
		cause := g.err
		if cause == nil {
			msg := g.Error
			if msg == "" {
				msg = "write failed"
			}
			cause = errors.New(msg)
		}
		if g.wrote > 0 {
			return fmt.Errorf("%w: group %s: after %d write(s): %w", repairs.ErrPartiallyApplied, g.GroupID, g.wrote, cause)
		}
		return fmt.Errorf("group %s: %w", g.GroupID, cause)
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
	r.State = vgStateOf(g)
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

// vgPlanState is the plan-time state a writable row stores (Row.State) so a
// later Replan can tell this fixer's own partial write from somebody else's
// change: every member's changed-since-plan key, the decision, the winner,
// the demotions and the carried fields.
type vgPlanState struct {
	Kind    string                `json:"kind"`
	Winner  string                `json:"winner,omitempty"`
	Demoted []string              `json:"demoted,omitempty"`
	Fills   map[string]string     `json:"fills,omitempty"`
	Members map[string]vgStateKey `json:"members"`
}

type vgStateKey struct {
	Primary     string `json:"primary"`
	MergedInto  string `json:"merged_into,omitempty"`
	State       string `json:"state,omitempty"`
	SoftDeleted bool   `json:"soft_deleted,omitempty"`
}

func (k vgStateKey) key() vgMemberKey {
	return vgMemberKey{primary: k.Primary, mergedInto: k.MergedInto, state: k.State, softDeleted: k.SoftDeleted}
}

func vgStateOf(g *vgRepairGroupReport) json.RawMessage {
	st := vgPlanState{Kind: g.Kind, Winner: g.WinnerID, Demoted: g.DemotedIDs, Members: map[string]vgStateKey{}}
	for id, k := range vgKeys(g.planned) {
		st.Members[id] = vgStateKey{Primary: k.primary, MergedInto: k.mergedInto, State: k.state, SoftDeleted: k.softDeleted}
	}
	if g.CarryOver != nil && len(g.CarryOver.Fills) > 0 {
		st.Fills = map[string]string{}
		for _, fl := range g.CarryOver.Fills {
			st.Fills[fl.Field] = fl.Value
		}
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return nil // no state: a later Replan stays strict
	}
	return raw
}

// resumesOwnWrite reports whether fresh (the group as planned now) differs
// from the stored plan only by writes this fixer made for this row, with the
// rest of the row still to do a subset of what was approved. It returns how
// many of the planned writes are already in place.
//
// WHY: a write cut off part way (the scan stand-down lease lost after the
// winner was crowned) leaves the group changed by the row itself, so the
// fresh fingerprint can never match the stored one and the resumed row would
// settle changed_since_plan with two explicit primaries. Every difference
// must be (1) a planned target value (winner explicit true, merge cleared
// for a revive, a planned demotion explicit false, a planned carry-over value
// on the winner) and (2) the newest history row for that book and field must
// be this fixer's, carrying the current value. A change another writer made
// fails (2) and the row stays changed_since_plan, as before. Without stored
// state (a plan made before this) or readable history the answer is no.
func (f *vgPrimaryFixer) resumesOwnWrite(store OpsStore, planned repairs.Row, g *vgRepairGroupReport) (int, bool) {
	if len(planned.State) == 0 || g.Error != "" || !vgWritable(g.Kind) {
		return 0, false
	}
	var st vgPlanState
	if err := json.Unmarshal(planned.State, &st); err != nil || len(st.Members) == 0 {
		return 0, false
	}
	// Same decision: a revive whose winner was already un-merged plans as
	// an elect of the same winner.
	if g.Kind != st.Kind && (st.Kind != vgDecisionRevive || g.Kind != versionprimary.DecisionElect) {
		return 0, false
	}
	if g.WinnerID != st.Winner {
		return 0, false
	}
	planDemote := map[string]bool{}
	for _, id := range st.Demoted {
		planDemote[id] = true
	}
	for _, id := range g.DemotedIDs {
		if !planDemote[id] {
			return 0, false
		}
	}
	ours := func(b *database.Book, field string) bool {
		cur, err := database.RenderBookField(b, field)
		if err != nil {
			return false
		}
		want, err := json.Marshal(cur)
		if err != nil {
			return false
		}
		// ONE field's newest row (OpsStore.GetMetadataChangeHistory), not a
		// window of the whole book's history: GetBookChangeHistory orders
		// rows by field and then time and applies its limit after that, so
		// a fixed window could cut the very field this check reads.
		rows, err := store.GetMetadataChangeHistory(b.ID, field, 1)
		if err != nil {
			return false
		}
		for _, h := range rows {
			if h.Field != field {
				continue
			}
			// The newest row for the field decides.
			return h.Source == vgPrimaryFixerID && h.NewValue != nil && *h.NewValue == string(want)
		}
		return false
	}
	if len(g.planned) != len(st.Members) {
		return 0, false
	}
	done := 0
	var winner *database.Book
	for i := range g.planned {
		b := &g.planned[i]
		sk, ok := st.Members[b.ID]
		if !ok {
			return 0, false
		}
		if b.ID == st.Winner {
			winner = b
		}
		was, now := sk.key(), vgKeyOf(b)
		if now == was {
			continue
		}
		target := was
		switch {
		case b.ID == st.Winner && st.Kind != vgDecisionDemoteNonLive:
			target.primary = "true"
			if st.Kind == vgDecisionRevive {
				target.mergedInto = ""
			}
		case planDemote[b.ID]:
			target.primary = "false"
		default:
			return 0, false
		}
		if now != target {
			return 0, false
		}
		if now.primary != was.primary && !ours(b, "is_primary_version") {
			return 0, false
		}
		if now.mergedInto != was.mergedInto && !ours(b, "merged_into_book_id") {
			return 0, false
		}
		done++
	}
	// Carried fields: a planned fill still planned must carry the same
	// value; one no longer planned must hold the planned value, written by
	// this fixer (the crown and the fills are one write).
	fresh := map[string]string{}
	if g.CarryOver != nil {
		for _, fl := range g.CarryOver.Fills {
			fresh[fl.Field] = fl.Value
		}
	}
	for field, v := range fresh {
		if pv, ok := st.Fills[field]; !ok || pv != v {
			return 0, false
		}
	}
	for field, v := range st.Fills {
		if _, still := fresh[field]; still {
			continue
		}
		if winner == nil {
			return 0, false
		}
		cur, err := database.RenderBookField(winner, field)
		if err != nil || cur != v || !ours(winner, field) {
			return 0, false
		}
		done++
	}
	return done, done > 0
}
