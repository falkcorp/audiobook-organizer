// file: core/internal/storagemigrate/register_test.go
// version: 1.0.0
// guid: 2643f46e-58b4-4549-bafb-c31f2264aa0b
// last-edited: 2026-11-10

package storagemigrate_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/internal/storagemigrate"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// registrationOwners: gotham with a keyspace at level 1 and its legacy
// claims (a retired family, a read-write parent with a retired child and
// the settings key, a carve-out); arkham with a keyspace only.
func registrationOwners() []storage.OwnerSpec {
	return []storage.OwnerSpec{
		{Owner: "gotham", Enabled: true, Spec: storage.Spec{Keyspace: true, Level: 1, Claims: []storage.Claim{
			{Prefix: "case:", Mode: storage.ClaimRetired},
			{Prefix: "idx:", Mode: storage.ClaimReadWrite, Except: []string{"idx:arkham:"}},
			{Prefix: "idx:case:", Mode: storage.ClaimRetired},
			{Key: "setting:config_blob", Mode: storage.ClaimReadWrite},
		}}},
		{Owner: "arkham", Enabled: true, Spec: storage.Spec{Keyspace: true}},
	}
}

func keyRange(start, end string) storage.Range {
	return storage.Range{Start: []byte(start), End: []byte(end)}
}

// valid is a migration registration accepts; each case breaks one rule.
func valid() datamigrate.DataMigration {
	return datamigrate.DataMigration{
		ID: "gotham.recount", Owner: "gotham", Source: []storage.Range{keyRange("m/gotham/item/", "m/gotham/item0")},
		Parallelism: tasks.Sequential("test"), Mode: datamigrate.BootBlocking, Compat: storage.Additive,
		Step: func(context.Context, storage.KV, *datamigrate.StepWriter) error { return nil },
	}
}

// cutover is a valid Cutover step over gotham's retired claims.
func cutover() datamigrate.DataMigration {
	m := valid()
	m.ID, m.Compat, m.Cutover = "gotham.cutover", storage.Breaking, true
	m.Source = []storage.Range{keyRange("case:", "case;"), keyRange("idx:case:", "idx:case;")}
	return m
}

func register(m ...datamigrate.DataMigration) error {
	_, err := storagemigrate.New(storagemigrate.Options{Owners: registrationOwners()}, m...)
	return err
}

// TestDataMigration_ZeroParallelismRefused (runner rule 1): the zero
// Parallelism is refused at registration with tasks.ErrNoParallelism.
func TestDataMigration_ZeroParallelismRefused(t *testing.T) {
	m := valid()
	m.Parallelism = tasks.Parallelism{}
	err := register(m)
	if !errors.Is(err, tasks.ErrNoParallelism) || !errors.Is(err, datamigrate.ErrInvalidMigration) {
		t.Fatalf("register = %v, want ErrNoParallelism", err)
	}
	m.Parallelism = tasks.Fixed(4, "")
	if err := register(m); !errors.Is(err, tasks.ErrNoParallelism) {
		t.Fatalf("register with a reasonless Fixed = %v, want ErrNoParallelism", err)
	}
}

// TestDataMigration_OnlineRefused (runner rule 1): the Online mode is a
// deferred-build member, refused at registration naming SIMP5-7.
func TestDataMigration_OnlineRefused(t *testing.T) {
	m := valid()
	m.Mode = datamigrate.Online
	err := register(m)
	if !errors.Is(err, datamigrate.ErrOnlineDeferred) || !strings.Contains(err.Error(), "SIMP5-7") {
		t.Fatalf("register = %v, want ErrOnlineDeferred naming SIMP5-7", err)
	}
}

// TestDataMigrationRegistration: every other rule of registration, each
// refused with ErrInvalidMigration, and every problem listed at once.
func TestDataMigrationRegistration(t *testing.T) {
	if err := register(valid(), cutover()); err != nil {
		t.Fatalf("valid migrations refused: %v", err)
	}
	residual := cutover()
	residual.Residual = [][]byte{[]byte("setting:config_blob")}
	residual.Source = append(residual.Source, keyRange("setting:config_blob", "setting:config_blob\x00"))
	if err := register(residual); err != nil {
		t.Fatalf("a Residual key in a read-write claim refused: %v", err)
	}
	legacyRW := valid()
	legacyRW.Source = []storage.Range{keyRange("idx:", "idx:arkham:")}
	if err := register(legacyRW); err != nil {
		t.Fatalf("a non-cutover step over a read-write claim refused: %v", err)
	}
	cases := map[string]struct {
		edit func(*datamigrate.DataMigration)
		has  string
	}{
		"bad ID":            {func(m *datamigrate.DataMigration) { m.ID = "gotham.Recount" }, "ID is not"},
		"foreign ID":        {func(m *datamigrate.DataMigration) { m.ID = "arkham.recount" }, "ID is not"},
		"unknown owner":     {func(m *datamigrate.DataMigration) { m.Owner, m.ID = "joker", "joker.recount" }, "not a storage owner"},
		"zero mode":         {func(m *datamigrate.DataMigration) { m.Mode = datamigrate.ModeInvalid }, "Mode is zero"},
		"unknown mode":      {func(m *datamigrate.DataMigration) { m.Mode = 9 }, "unknown Mode"},
		"zero compat":       {func(m *datamigrate.DataMigration) { m.Compat = storage.CompatInvalid }, "Compat is zero"},
		"unknown compat":    {func(m *datamigrate.DataMigration) { m.Compat = 9 }, "unknown Compat"},
		"breaking no level": {func(m *datamigrate.DataMigration) { m.Compat = storage.Breaking }, "raises its owner's level"},
		"level above spec":  {func(m *datamigrate.DataMigration) { m.Compat, m.RaisesLevel = storage.Breaking, 2 }, "above the level"},
		"negative level":    {func(m *datamigrate.DataMigration) { m.RaisesLevel = -1 }, "negative"},
		"negative barrier":  {func(m *datamigrate.DataMigration) { m.Barrier = -1 }, "Barrier -1"},
		"no step":           {func(m *datamigrate.DataMigration) { m.Step = nil }, "no Step"},
		"writes":            {func(m *datamigrate.DataMigration) { m.Writes = []entity.Ref{{Module: "gotham", Kind: "case"}} }, "SIMP5-7"},
		"sensitive":         {func(m *datamigrate.DataMigration) { m.Sensitive = []string{"case:"} }, "Sensitive is for Cutover"},
		"preflight": {func(m *datamigrate.DataMigration) {
			m.Preflight = func(context.Context, storage.Reader) ([]datamigrate.Blocker, error) { return nil, nil }
		}, "Preflight is for Cutover"},
		"no source":       {func(m *datamigrate.DataMigration) { m.Source = nil }, "no Source"},
		"empty range":     {func(m *datamigrate.DataMigration) { m.Source = []storage.Range{keyRange("m/gotham/b", "m/gotham/a")} }, "empty or unbounded"},
		"unbounded range": {func(m *datamigrate.DataMigration) { m.Source = []storage.Range{{Start: []byte("m/gotham/a")}} }, "empty or unbounded"},
		"overlapping": {func(m *datamigrate.DataMigration) {
			m.Source = []storage.Range{keyRange("m/gotham/a", "m/gotham/c"), keyRange("m/gotham/b", "m/gotham/d")}
		}, "overlap"},
		"other keyspace": {func(m *datamigrate.DataMigration) { m.Source = []storage.Range{keyRange("m/arkham/a", "m/arkham/b")} }, "neither in"},
		"mixed handles": {func(m *datamigrate.DataMigration) {
			m.Source = []storage.Range{keyRange("case:", "case;"), keyRange("m/gotham/a", "m/gotham/b")}
		}, "neither in"},
		"across a carveout": {func(m *datamigrate.DataMigration) {
			m.Source = []storage.Range{keyRange("idx:arkham:a", "idx:arkham:b")}
		}, "carve-out"},
		"across a child": {func(m *datamigrate.DataMigration) { m.Source = []storage.Range{keyRange("idx:", "idx;")} }, "crosses a claim boundary"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			m := valid()
			c.edit(&m)
			err := register(m)
			if !errors.Is(err, datamigrate.ErrInvalidMigration) || !strings.Contains(err.Error(), c.has) {
				t.Fatalf("register = %v, want ErrInvalidMigration mentioning %q", err, c.has)
			}
		})
	}
	cutoverCases := map[string]struct {
		edit func(*datamigrate.DataMigration)
		has  string
	}{
		"additive":           {func(m *datamigrate.DataMigration) { m.Compat = storage.Additive }, "is Breaking"},
		"keyspace source":    {func(m *datamigrate.DataMigration) { m.Source = []storage.Range{keyRange("m/gotham/a", "m/gotham/b")} }, "retired legacy claims"},
		"read-write source":  {func(m *datamigrate.DataMigration) { m.Source = []storage.Range{keyRange("idx:b", "idx:c")} }, "read-write claim"},
		"residual uncovered": {func(m *datamigrate.DataMigration) { m.Residual = [][]byte{[]byte("setting:config_blob")} }, "Residual key"},
	}
	for name, c := range cutoverCases {
		t.Run("cutover "+name, func(t *testing.T) {
			m := cutover()
			c.edit(&m)
			err := register(m)
			if !errors.Is(err, datamigrate.ErrInvalidMigration) || !strings.Contains(err.Error(), c.has) {
				t.Fatalf("register = %v, want ErrInvalidMigration mentioning %q", err, c.has)
			}
		})
	}
	t.Run("every problem listed", func(t *testing.T) {
		a, b := valid(), valid()
		a.Mode, b.Compat = datamigrate.ModeInvalid, storage.CompatInvalid
		b.ID = "gotham.second"
		err := register(a, b, valid(), valid())
		var re *storagemigrate.RegistrationError
		if !errors.As(err, &re) || len(re.Problems) != 3 || !strings.Contains(err.Error(), "registered twice") {
			t.Fatalf("register = %v, want 3 problems: a, b and the duplicate ID", err)
		}
	})
}
