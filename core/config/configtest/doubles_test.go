// file: core/config/configtest/doubles_test.go
// version: 1.0.0
// guid: a2e7efed-6d01-4bcb-8a45-538c9ab03913
// last-edited: 2026-11-10

package configtest_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
)

func loadAll(t *testing.T, m *configtest.Memory) []config.Override {
	t.Helper()
	var out []config.Override
	for ov, err := range m.Load(context.Background()) {
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		out = append(out, ov)
	}
	return out
}

func TestMemory_StageLoadAndUnset(t *testing.T) {
	m := configtest.NewMemory()
	ctx := context.Background()
	raw := json.RawMessage(`"robin"`)
	err := m.Stage(ctx, nil, []config.Override{
		{Path: configtest.WatchPath + ".max", Value: json.RawMessage("7")},
		{Path: configtest.BatcavePath + ".level", Value: raw},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The caller's slice is cloned on the way in.
	raw[1] = 'X'
	got := loadAll(t, m)
	if len(got) != 2 || got[0].Path != configtest.BatcavePath+".level" || got[1].Path != configtest.WatchPath+".max" {
		t.Fatalf("Load not sorted by path or wrong rows: %+v", got)
	}
	if string(got[0].Value) != `"robin"` {
		t.Fatalf("Stage did not clone the value: %s", got[0].Value)
	}
	// Rows hands out copies too.
	rows := m.Rows()
	rows[configtest.WatchPath+".max"].Value[0] = '9'
	if string(m.Rows()[configtest.WatchPath+".max"].Value) != "7" {
		t.Fatal("Rows leaked the stored value")
	}
	if err := m.Stage(ctx, nil, nil, []config.Path{configtest.WatchPath + ".max"}); err != nil {
		t.Fatal(err)
	}
	if got := loadAll(t, m); len(got) != 1 {
		t.Fatalf("unset left %d rows", len(got))
	}
	if m.Stages() != 2 || m.Loads() != 2 {
		t.Fatalf("Stages=%d Loads=%d, want 2 and 2", m.Stages(), m.Loads())
	}
}

func TestMemory_PutKeepsInvalidValueAndFailLoad(t *testing.T) {
	m := configtest.NewMemory()
	m.Put(config.Override{Path: configtest.WatchPath + ".min", Value: json.RawMessage("{not json")})
	if got := loadAll(t, m); len(got) != 1 || string(got[0].Value) != "{not json" {
		t.Fatalf("Put did not keep the raw value: %+v", got)
	}
	boom := errors.New("alfred is out")
	m.FailLoad(boom)
	n := 0
	for _, err := range m.Load(context.Background()) {
		n++
		if !errors.Is(err, boom) {
			t.Fatalf("Load err = %v, want %v", err, boom)
		}
	}
	if n != 1 {
		t.Fatalf("a failing Load yielded %d items, want exactly 1", n)
	}
}

func TestMemory_LoadStopsWhenConsumerBreaks(t *testing.T) {
	m := configtest.NewMemory()
	m.Put(config.Override{Path: configtest.BatcavePath + ".level", Value: json.RawMessage("1")})
	m.Put(config.Override{Path: configtest.WatchPath + ".max", Value: json.RawMessage("2")})
	n := 0
	for range m.Load(context.Background()) {
		n++
		break
	}
	if n != 1 {
		t.Fatalf("iterated %d times after break", n)
	}
}

func TestCommitterDouble_RecordsAppliesAndFails(t *testing.T) {
	m := configtest.NewMemory()
	c := configtest.NewCommitter(m)
	ctx := context.Background()
	p := configtest.BatcavePath + ".level"
	if err := c.Commit(ctx, config.Commit{Set: []config.Override{{Path: p, Value: json.RawMessage("3")}}, Gen: 1}); err != nil {
		t.Fatal(err)
	}
	if string(m.Rows()[p].Value) != "3" {
		t.Fatalf("commit not applied: %+v", m.Rows())
	}
	boom := errors.New("the batcave is flooded")
	c.Fail(boom)
	if err := c.Commit(ctx, config.Commit{Unset: []config.Path{p}, Gen: 2}); !errors.Is(err, boom) {
		t.Fatalf("Commit err = %v, want %v", err, boom)
	}
	if _, ok := m.Rows()[p]; !ok {
		t.Fatal("a failed commit was applied")
	}
	c.Fail(nil)
	if err := c.Commit(ctx, config.Commit{Unset: []config.Path{p}, Gen: 3}); err != nil {
		t.Fatal(err)
	}
	if len(m.Rows()) != 0 {
		t.Fatal("unset not applied after Fail(nil)")
	}
	if c.Calls() != 3 {
		t.Fatalf("Calls = %d, want 3 (failed one included)", c.Calls())
	}
	if cs := c.Commits(); len(cs) != 2 || cs[0].Gen != 1 || cs[1].Gen != 3 {
		t.Fatalf("Commits = %+v, want gens 1 and 3", cs)
	}
}

func TestCommitterDouble_NilStoreOnlyRecords(t *testing.T) {
	c := configtest.NewCommitter(nil)
	if err := c.Commit(context.Background(), config.Commit{Gen: 4}); err != nil {
		t.Fatal(err)
	}
	if len(c.Commits()) != 1 {
		t.Fatal("commit not recorded")
	}
}

func TestWatchValidate_RefusesMinAboveMax(t *testing.T) {
	w := configtest.Watch{Min: 5, Max: 2}
	var fe *config.FieldError
	if err := w.Validate(); !errors.As(err, &fe) || fe.Path != configtest.WatchPath+".min" {
		t.Fatalf("Validate = %v, want a FieldError on the min key", err)
	}
	w.Min = 1
	if err := w.Validate(); err != nil {
		t.Fatalf("valid Watch refused: %v", err)
	}
}

func TestModulesSection_DeclaresMapSection(t *testing.T) {
	sec := configtest.ModulesSection(map[string]configtest.Module{"gotham": {Enabled: true}})
	if sec.Path() != configtest.ModulesPath {
		t.Fatalf("section path = %s, want %s", sec.Path(), configtest.ModulesPath)
	}
}
