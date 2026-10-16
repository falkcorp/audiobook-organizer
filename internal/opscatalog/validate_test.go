// file: internal/opscatalog/validate_test.go
// version: 1.0.0
// guid: 15d7de21-ccf4-4692-992b-95f1056c2b6a
// last-edited: 2026-10-16

package opscatalog

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// refusals flattens a joined ValidateCatalog error into (def id, refusal)
// pairs, in order.
func refusals(err error) []string {
	var out []string
	var walk func(error)
	walk = func(e error) {
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, x := range j.Unwrap() {
				walk(x)
			}
			return
		}
		var ce *ops.CatalogError
		if errors.As(e, &ce) {
			out = append(out, ce.DefID+":"+string(ce.Refusal))
		}
	}
	if err != nil {
		walk(err)
	}
	return out
}

func common(id string, e ops.Effects) ops.Common {
	return ops.Common{Title: id, Help: "synthetic test def", Effects: e, Permission: auth.PermSettingsManage}
}

func task(id string, c ops.Common) ops.Definition {
	return ops.Task(id, ops.TaskSpec[ops.NoParams, ops.NoResult]{Common: c,
		Run: func(*ops.RC, ops.NoParams) (ops.NoResult, error) { return ops.NoResult{}, nil }})
}

func batch(id string, c ops.Common, conc ops.Concurrency) ops.Definition {
	return ops.Batch(id, ops.BatchSpec[ops.NoParams, string, ops.NoResult]{Common: c, Concurrency: conc,
		Source: func(*ops.RC, ops.NoParams) (ops.Source[string], error) { return ops.Source[string]{}, nil },
		Item:   func(*ops.ItemRC, ops.NoParams, string) error { return nil }})
}

func fixer(id string, e ops.Effects) ops.Definition {
	return ops.Fixer(id, ops.FixerSpec[ops.NoParams, string]{
		Common:     common(id, e),
		Candidates: func(*ops.RC, ops.NoParams) (ops.Source[string], error) { return ops.Source[string]{}, nil },
		Load:       func(*ops.RC, ops.NoParams, string) (string, bool, error) { return "", false, nil },
		Evaluate:   func(*ops.ItemRC, ops.NoParams, string) (ops.Row, error) { return ops.Row{}, nil },
		Apply:      func(*ops.ItemRC, ops.Writer, ops.NoParams, ops.Row) error { return nil },
	})
}

func readOnly(id string) ops.Definition { return task(id, common(id, ops.ReadOnly(ops.ResBooks))) }

// emptyUniverse resolves nothing but the tombstones.
func emptyUniverse() Universe { return BuildUniverse(nil, nil, nil, nil) }

func wantOnly(t *testing.T, err error, want ...string) {
	t.Helper()
	got := refusals(err)
	if !slices.Equal(got, want) {
		t.Fatalf("refusals = %v, want %v (err: %v)", got, want, err)
	}
}

func TestValidateCatalogAcceptsAGoodCatalog(t *testing.T) {
	defs := []ops.Definition{readOnly("synthetic.read"), fixer("synthetic.fix", ops.Writes(ops.ResBooks))}
	u := BuildUniverse(nil, nil, nil, []ops.Bundle{{Plugin: "synthetic", Defs: defs}})
	if err := ValidateCatalog(defs, u, []string{"synthetic.read", "synthetic.fix"}); err != nil {
		t.Fatalf("good catalog refused: %v", err)
	}
}

func TestDuplicateIDFails(t *testing.T) {
	err := ValidateCatalog([]ops.Definition{readOnly("synthetic.dup"), readOnly("synthetic.dup")}, emptyUniverse(), nil)
	wantOnly(t, err, "synthetic.dup:"+string(ops.RefuseDuplicateID))
}

func TestInvalidScheduleFails(t *testing.T) {
	c := common("synthetic.sched", ops.ReadOnly(ops.ResBooks))
	c.Schedule = ops.Cron("61 * * * *")
	err := ValidateCatalog([]ops.Definition{task("synthetic.sched", c)}, emptyUniverse(), nil)
	got := refusals(err)
	if !slices.Contains(got, "synthetic.sched:"+string(ops.RefuseSchedule)) {
		t.Fatalf("invalid cron not refused: %v", got)
	}
}

func TestFixerWithoutWritesFails(t *testing.T) {
	err := ValidateCatalog([]ops.Definition{fixer("synthetic.fix", ops.ReadOnly(ops.ResBooks))}, emptyUniverse(), nil)
	wantOnly(t, err, "synthetic.fix:"+string(ops.RefuseFixerReadOnly))
}

func TestNativeDefWithoutPermissionFails(t *testing.T) {
	c := common("synthetic.noperm", ops.ReadOnly(ops.ResBooks))
	c.Permission = ""
	err := ValidateCatalog([]ops.Definition{task("synthetic.noperm", c)}, emptyUniverse(), nil)
	wantOnly(t, err, "synthetic.noperm:"+string(ops.RefuseNoPermission))
}

func TestAdaptedV2DefExemptFromPermissionRule(t *testing.T) {
	adapted := registry.FromV2(registry.OperationDef{ID: "synthetic.adapted", DisplayName: "Adapted"})
	if err := ValidateCatalog([]ops.Definition{adapted}, emptyUniverse(), nil); err != nil {
		t.Fatalf("an adapted v2 def with no permission was refused (D1 exempts it): %v", err)
	}
}

func TestSequentialWithoutReasonFails(t *testing.T) {
	d := batch("synthetic.seq", common("synthetic.seq", ops.ReadOnly(ops.ResBooks)), ops.Sequential("  "))
	wantOnly(t, ValidateCatalog([]ops.Definition{d}, emptyUniverse(), nil), "synthetic.seq:"+string(ops.RefuseSequentialNoReason))
}

func TestScheduledWriterWithoutLiveFails(t *testing.T) {
	c := common("synthetic.nightly", ops.Writes(ops.ResBooks))
	c.Schedule = ops.Every(time.Hour)
	err := ValidateCatalog([]ops.Definition{task("synthetic.nightly", c)}, emptyUniverse(), nil)
	wantOnly(t, err, "synthetic.nightly:"+string(RefuseScheduledWriterNotLive))

	c.Schedule = ops.Every(time.Hour).Live()
	if err := ValidateCatalog([]ops.Definition{task("synthetic.nightly", c)}, emptyUniverse(), nil); err != nil {
		t.Fatalf("a scheduled writer that says .Live() was refused: %v", err)
	}
	// A scheduled reader needs no .Live().
	r := common("synthetic.report", ops.ReadOnly(ops.ResBooks))
	r.Schedule = ops.DailyAt("03:00")
	if err := ValidateCatalog([]ops.Definition{task("synthetic.report", r)}, emptyUniverse(), nil); err != nil {
		t.Fatalf("a scheduled read-only def was refused: %v", err)
	}
}

func TestLedgerIDWithoutDefAliasOrTombstoneFails(t *testing.T) {
	err := ValidateCatalog(nil, emptyUniverse(), []string{"synthetic.renamed-without-alias"})
	wantOnly(t, err, "synthetic.renamed-without-alias:"+string(RefuseLedgerIDUnresolved))
}

func TestTombstoneResolvesLedgerID(t *testing.T) {
	if len(Tombstones) == 0 {
		t.Fatal("Tombstones is empty")
	}
	var ledger []string
	for id, why := range Tombstones {
		if strings.TrimSpace(why) == "" {
			t.Errorf("tombstone %q has no reason", id)
		}
		ledger = append(ledger, id)
	}
	if err := ValidateCatalog(nil, emptyUniverse(), ledger); err != nil {
		t.Fatalf("tombstoned ledger IDs did not resolve: %v", err)
	}
}

func TestLiveDefsAliasesAndFormerIDsResolve(t *testing.T) {
	live := []registry.OperationDef{{ID: "synthetic.live", FormerIDs: []string{"synthetic.live-old"}}}
	aliases := map[string]string{"synthetic.alias": "synthetic.live"}
	native := task("synthetic.native", func() ops.Common {
		c := common("synthetic.native", ops.ReadOnly(ops.ResBooks))
		c.FormerIDs = []string{"synthetic.native-old"}
		return c
	}())
	u := BuildUniverse(live, aliases, nil, []ops.Bundle{{Plugin: "synthetic", Defs: []ops.Definition{native}}})
	ledger := []string{"synthetic.live", "synthetic.live-old", "synthetic.alias", "synthetic.native", "synthetic.native-old"}
	if missing := ResolveLedger(u, ledger); len(missing) != 0 {
		t.Fatalf("unresolved: %v", missing)
	}
}

// The plugins' dependency-free lists make a server with none of their ops in
// its live registry (no Deluge client, no dedup engine, ...) valid.
func TestStaticPluginIDsResolveWithoutDeps(t *testing.T) {
	static := StaticPluginDefs()
	if len(static) != 5 {
		t.Fatalf("StaticPluginDefs returned %d lists, want the 5 service-registry plugins", len(static))
	}
	var ledger []string
	for _, list := range static {
		if len(list) == 0 {
			t.Fatal("a static plugin list is empty")
		}
		for _, d := range list {
			ledger = append(ledger, d.ID)
			ledger = append(ledger, d.FormerIDs...)
		}
	}
	if missing := ResolveLedger(BuildUniverse(nil, nil, nil, nil), ledger); len(missing) == 0 {
		t.Fatal("plugin IDs resolved without the static lists; the test proves nothing")
	}
	u := BuildUniverse(nil, nil, static, Bundles(Deps{}))
	if err := ValidateCatalog(nil, u, ledger); err != nil {
		t.Fatalf("plugin IDs did not resolve through the static lists: %v", err)
	}
}

// Every refusal is reported, not just the first.
func TestAllRefusalsReportedTogether(t *testing.T) {
	noPerm := common("synthetic.noperm", ops.ReadOnly(ops.ResBooks))
	noPerm.Permission = ""
	sched := common("synthetic.nightly", ops.Writes(ops.ResBooks))
	sched.Schedule = ops.Every(time.Hour)
	defs := []ops.Definition{
		task("synthetic.noperm", noPerm),
		fixer("synthetic.fix", ops.ReadOnly(ops.ResBooks)),
		task("synthetic.nightly", sched),
	}
	got := refusals(ValidateCatalog(defs, emptyUniverse(), []string{"synthetic.gone"}))
	for _, want := range []string{
		"synthetic.noperm:" + string(ops.RefuseNoPermission),
		"synthetic.fix:" + string(ops.RefuseFixerReadOnly),
		"synthetic.nightly:" + string(RefuseScheduledWriterNotLive),
		"synthetic.gone:" + string(RefuseLedgerIDUnresolved),
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing refusal %s in %v", want, got)
		}
	}
}

func TestParseLedgerSkipsCommentsAndBlankLines(t *testing.T) {
	body := []byte("# header\n<!-- note -->\n\n  synthetic.a  \nsynthetic.b\n# trailing\n")
	if got := ParseLedger(body); !slices.Equal(got, []string{"synthetic.a", "synthetic.b"}) {
		t.Fatalf("ParseLedger = %v", got)
	}
}

func TestBuildRefusesMissingServiceAndUnnamedPlugin(t *testing.T) {
	saved := All
	t.Cleanup(func() { All = saved })

	All = nil
	if b, err := Build(Deps{}); err != nil || len(b) != 0 {
		t.Fatalf("empty All: Build = (%v, %v), want none and no error", b, err)
	}

	All = []func(Deps) ops.Bundle{
		func(Deps) ops.Bundle {
			return ops.Bundle{Plugin: "synthetic", Requires: []ops.ServiceKey{"synthetic-service"}}
		},
		func(Deps) ops.Bundle { return ops.Bundle{} },
		func(Deps) ops.Bundle { return ops.Bundle{Plugin: "synthetic"} },
	}
	_, err := Build(Deps{})
	if err == nil {
		t.Fatal("Build accepted a missing service, an unnamed bundle and a duplicate plugin")
	}
	for _, want := range []string{`"synthetic-service"`, "names no plugin", "more than one bundle"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Build error lacks %q: %v", want, err)
		}
	}
	if _, err := Build(Deps{Services: map[ops.ServiceKey]bool{"synthetic-service": true}}); err == nil ||
		strings.Contains(err.Error(), "synthetic-service") {
		t.Fatalf("a provided service was still refused: %v", err)
	}
}
