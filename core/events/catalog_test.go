// file: core/events/catalog_test.go
// version: 1.0.0
// guid: 5bfb5801-39f4-4425-9ac3-dc5734ba40dd
// last-edited: 2026-11-10

package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/events"
	"github.com/falkcorp/mediacore/rbac"
)

// TestTopicName_Grammar: "<moduleID>.<noun>[.<qualifier>].<verb-past>.v<N>",
// segments [a-z][a-z0-9-]*, N >= 1 (05-events §5E.3.1, 03 §3.6).
func TestTopicName_Grammar(t *testing.T) {
	cases := map[string]bool{
		"gotham.villain.escaped.v1":              true,
		"gotham.villain.run-changed.v1":          true,
		"gotham.arkham.cell.opened.v12":          true, // one qualifier segment
		"gotham.cell-2.door.opened.v3":           true,
		"gotham.villain.v1":                      false, // no verb
		"gotham.villain.escaped":                 false, // no version
		"gotham.villain.escaped.v0":              false, // N >= 1
		"gotham.villain.escaped.v01":             false,
		"gotham.villain.escaped.1":               false,
		"gotham.arkham.wing.cell.opened.v1":      false, // two qualifiers
		"gotham.Villain.escaped.v1":              false,
		"gotham.villain_x.escaped.v1":            false,
		"gotham..escaped.v1":                     false,
		"gotham.1villain.escaped.v1":             false,
		"gotham.villain.escaped.v1.":             false,
		"":                                       false,
		"gotham.villain.escaped.v1 ":             false,
		"gotham.villain.-escaped.v1":             false,
		"gotham.villain.escaped.v1x":             false,
		"gotham.villain.escaped.v1.v2":           true, // "v1" is a valid verb segment
		"gotham.villain.escaped.changed.again.v": false,
	}
	for name, ok := range cases {
		b, err := events.NewBus(events.BusOptions{})
		must(t, err)
		err = b.Declare("gotham", events.NewTopic[Sighting](name).Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")}))
		if ok && err != nil {
			t.Errorf("%q: refused: %v", name, err)
		}
		if !ok && !errors.Is(err, events.ErrTopicName) {
			t.Errorf("%q: err = %v, want ErrTopicName", name, err)
		}
	}
}

// TestCatalog_RejectsDuplicateZeroPermissionForeignScope: one catalog; a
// duplicate name, a zero Permission, a topic outside the declaring module's
// namespace and a Permission naming another module's scope are refused, and
// each refusal is reported (05-events §5E.3.1, 05-rbac §4.9).
func TestCatalog_RejectsDuplicateZeroPermissionForeignScope(t *testing.T) {
	b, err := events.NewBus(events.BusOptions{})
	must(t, err)
	must(t, b.Declare("gotham", spec(escaped(), "gotham:view")))

	cases := []struct {
		name  string
		owner string
		spec  events.TopicSpec
		want  error
	}{
		{"duplicate", "gotham", spec(escaped(), "gotham:view"), events.ErrDuplicateTopic},
		{"zero permission", "gotham", events.NewTopic[Sighting]("gotham.villain.caught.v1").Spec(events.TopicSpec{}), events.ErrNoPermission},
		{"foreign namespace", "wayne", events.NewTopic[Sighting]("gotham.villain.seen.v1").Spec(events.TopicSpec{Permission: rbac.Need("wayne:view")}), events.ErrTopicNamespace},
		{"foreign scope", "wayne", events.NewTopic[Alarm]("wayne.vault.opened.v1").Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")}), events.ErrForeignScope},
		{"foreign scope among own", "wayne", events.NewTopic[Alarm]("wayne.vault.sealed.v1").Spec(events.TopicSpec{Permission: rbac.AnyOf("wayne:view", "gotham:view")}), events.ErrForeignScope},
		{"a module requiring core:system.view", "wayne", events.NewTopic[Alarm]("wayne.vault.locked.v1").Spec(events.TopicSpec{Permission: rbac.Need("core:system.view")}), events.ErrForeignScope},
		{"legacy kind", "wayne", events.NewTopic[Alarm]("wayne.vault.robbed.v1").Spec(events.TopicSpec{Permission: rbac.LegacyAtom("books.read")}), events.ErrForeignScope},
		{"delegated kind", "wayne", events.NewTopic[Alarm]("wayne.vault.moved.v1").Spec(events.TopicSpec{Permission: rbac.Delegated("own auth")}), events.ErrForeignScope},
	}
	for _, c := range cases {
		if err := b.Declare(c.owner, c.spec); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}

	// Accepted: the owner's own scope, core:admin, Public, AnyAuthenticated;
	// core's topics may require core scopes.
	must(t, b.Declare("wayne",
		events.NewTopic[Alarm]("wayne.vault.tripped.v1").Spec(events.TopicSpec{Permission: rbac.AllOf("wayne:view", rbac.CoreAdmin)}),
		events.NewTopic[Alarm]("wayne.vault.opened.v1").Spec(events.TopicSpec{Permission: rbac.Public}),
		events.NewTopic[Alarm]("wayne.vault.closed.v1").Spec(events.TopicSpec{Permission: rbac.AnyAuthenticated}),
	))
	must(t, b.Declare("core", spec(signal(), "core:system.view")))

	// Several bad entries in one call are all reported; the good one lands.
	err = b.Declare("wayne",
		events.NewTopic[Alarm]("wayne.vault.a.v1").Spec(events.TopicSpec{}),
		events.NewTopic[Alarm]("wayne.vault.b.v1").Spec(events.TopicSpec{Permission: rbac.Need("gotham:view")}),
		events.NewTopic[Alarm]("wayne.vault.c.v1").Spec(events.TopicSpec{Permission: rbac.Need("wayne:view")}),
	)
	if !errors.Is(err, events.ErrNoPermission) || !errors.Is(err, events.ErrForeignScope) {
		t.Errorf("joined err = %v, want ErrNoPermission and ErrForeignScope", err)
	}

	var names []string
	for s := range b.Catalog().All() {
		names = append(names, s.Name)
	}
	want := []string{"core.signal.lit.v1", "gotham.villain.escaped.v1", "wayne.vault.c.v1",
		"wayne.vault.closed.v1", "wayne.vault.opened.v1", "wayne.vault.tripped.v1"}
	if !slices.Equal(names, want) {
		t.Errorf("catalog = %v, want %v", names, want)
	}
	if _, ok := b.Catalog().Lookup("wayne.vault.a.v1"); ok {
		t.Error("a refused entry is in the catalog")
	}

	b.Freeze()
	if err := b.Declare("wayne", events.NewTopic[Alarm]("wayne.vault.late.v1").Spec(events.TopicSpec{Permission: rbac.Public})); !errors.Is(err, events.ErrFrozen) {
		t.Errorf("Declare after Freeze: err = %v, want ErrFrozen", err)
	}
}

// TestCatalog_NotifyAndRealtimeReadNoSecondDeclaration: the one catalog entry
// carries the permission and the notify and realtime metadata, and the
// readers (notify, realtime) find all of it through Catalog alone (SIMP-5).
// Realtime or Notify on an entry without a schema is refused.
func TestCatalog_NotifyAndRealtimeReadNoSecondDeclaration(t *testing.T) {
	b, err := events.NewBus(events.BusOptions{})
	must(t, err)
	visible := func(context.Context, rbac.Checker, events.RawEnvelope) bool { return true }
	must(t, b.Declare("gotham", escaped().Spec(events.TopicSpec{
		Permission: rbac.Need("gotham:view"),
		Visible:    visible,
		Notify:     &events.NotifyMeta{Title: "A villain escaped", DefaultSeverity: "warning"},
		Realtime:   &events.RealtimeMeta{Coalesce: 250 * time.Millisecond},
	})))

	got, ok := b.Catalog().Lookup("gotham.villain.escaped.v1")
	if !ok {
		t.Fatal("the declared topic is not in the catalog")
	}
	if got.Notify == nil || got.Notify.Title != "A villain escaped" || got.Notify.DefaultSeverity != "warning" {
		t.Errorf("Notify = %+v", got.Notify)
	}
	if got.Realtime == nil || got.Realtime.Coalesce != 250*time.Millisecond {
		t.Errorf("Realtime = %+v", got.Realtime)
	}
	if got.Visible == nil {
		t.Error("Visible was not kept")
	}
	if got.Permission.String() != "gotham:view" {
		t.Errorf("Permission = %s", got.Permission)
	}
	var schema map[string]any
	if err := json.Unmarshal(got.Schema, &schema); err != nil {
		t.Fatalf("Schema is not JSON: %v", err)
	}
	if props, _ := schema["properties"].(map[string]any); props["villain"] == nil || props["block"] == nil {
		t.Errorf("Schema = %s, want the payload's properties", got.Schema)
	}

	for name, s := range map[string]events.TopicSpec{
		"realtime without schema": {Name: "gotham.villain.fled.v1", Permission: rbac.Need("gotham:view"), Realtime: &events.RealtimeMeta{}},
		"notify without schema":   {Name: "gotham.villain.hid.v1", Permission: rbac.Need("gotham:view"), Notify: &events.NotifyMeta{Title: "x"}},
	} {
		if err := b.Declare("gotham", s); !errors.Is(err, events.ErrSchemaRequired) {
			t.Errorf("%s: err = %v, want ErrSchemaRequired", name, err)
		}
	}
	// A hand-built entry with a schema and neither metadata is fine; one
	// whose schema does not resolve is refused.
	must(t, b.Declare("gotham", events.TopicSpec{Name: "gotham.villain.hid.v1", Permission: rbac.Need("gotham:view")}))
	if err := b.Declare("gotham", events.TopicSpec{Name: "gotham.villain.lurked.v1", Permission: rbac.Need("gotham:view"),
		Schema: json.RawMessage(`{"type": 7}`)}); !errors.Is(err, events.ErrBadSchema) {
		t.Errorf("bad schema: err = %v, want ErrBadSchema", err)
	}
}

// TestCatalog_RejectsRealtimeOnDurable (COR2-17): a durable message never
// reaches the bus the realtime hub subscribes to, so the entry is refused
// with the spec's text.
func TestCatalog_RejectsRealtimeOnDurable(t *testing.T) {
	b, err := events.NewBus(events.BusOptions{})
	must(t, err)
	err = b.Declare("gotham", escaped().Spec(events.TopicSpec{
		Permission: rbac.Need("gotham:view"),
		Durable:    &events.Durable{Partitions: 16},
		Realtime:   &events.RealtimeMeta{},
	}))
	if !errors.Is(err, events.ErrRealtimeOnDurable) {
		t.Fatalf("err = %v, want ErrRealtimeOnDurable", err)
	}
	if !strings.Contains(err.Error(), "Realtime on a Durable topic: declare a companion bus topic") {
		t.Errorf("err = %q, want the spec's text", err)
	}
	// Durable alone and Realtime alone are both fine.
	must(t, b.Declare("gotham", escaped().Spec(events.TopicSpec{Permission: rbac.Need("gotham:view"), Durable: &events.Durable{}})))
	must(t, b.Declare("gotham", events.NewTopic[Sighting]("gotham.villain.spotted.v1").Spec(events.TopicSpec{
		Permission: rbac.Need("gotham:view"), Realtime: &events.RealtimeMeta{},
	})))
}
