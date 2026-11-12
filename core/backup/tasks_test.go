// file: core/backup/tasks_test.go
// version: 1.0.0
// guid: 3d9e1a58-7b04-4f62-a2c7-5e8b0f4d6a91
// last-edited: 2026-11-12

package backup_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

func TestBackupTaskDefinitions(t *testing.T) {
	e := newEnv(t)
	defs := backup.Tasks(e.svc)
	taskstest.Conformance(t, defs...)
	if len(defs) != 4 {
		t.Fatalf("%d definitions, want create, verify, prune and stage-restore", len(defs))
	}
	want := map[tasks.Kind]string{
		backup.KindCreate: backup.ScheduleCreateKey, backup.KindVerify: backup.ScheduleVerifyKey,
		backup.KindPrune: "", backup.KindStageRestore: "",
	}
	for _, d := range defs {
		c := tasks.CommonOf(d)
		// One exclusive resource: the same write set and the same concurrency key
		// for every kind, so the executor never overlaps two of them.
		if c.ConcurrencyKey != "core.backup" {
			t.Errorf("%s: concurrency key %q", d.Kind(), c.ConcurrencyKey)
		}
		if ws := c.Effects.WriteSet(); len(ws) != 1 || ws[0] != backup.Resource() {
			t.Errorf("%s: write set %v, want exactly %v", d.Kind(), ws, backup.Resource())
		}
		key := ""
		if c.Schedule != nil {
			key = c.Schedule.ConfigKey()
		}
		if w, ok := want[d.Kind()]; !ok || key != w {
			t.Errorf("%s: schedule key %q, want %q", d.Kind(), key, w)
		}
		delete(want, d.Kind())
	}
	if len(want) != 0 {
		t.Errorf("kinds missing: %v", want)
	}
}

// phaseLog records the phases a run reports.
type phaseLog struct{ got []string }

func (p *phaseLog) Phase(name string) func() {
	p.got = append(p.got, "+"+name)
	return func() { p.got = append(p.got, "-"+name) }
}

func TestCreateReportsItsPhasesInOrder(t *testing.T) {
	e := newEnv(t)
	pl := &phaseLog{}
	if _, err := e.svc.Create(backup.WithProgress(context.Background(), pl), backup.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"+checkpoint", "-checkpoint", "+checksum", "-checksum", "+archive", "-archive", "+verify", "-verify"}
	if !slices.Equal(pl.got, want) {
		t.Fatalf("phases = %v, want %v", pl.got, want)
	}
}

func TestCreateTaskBody(t *testing.T) {
	e := newEnv(t)
	rc := taskstest.NewFakeRun(context.Background(), backup.KindCreate).RC()
	res, err := backup.TaskCreate(e.svc, rc, backup.CreateParams{Reason: "pre:books.organize"})
	if err != nil || res.ArchiveID == "" || !res.Verified || res.Size == 0 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	list, _ := e.svc.List(context.Background())
	if len(list) != 1 || list[0].Manifest.Reason != "pre:books.organize" {
		t.Fatalf("catalogue = %+v", list)
	}
	// The default reason of a run that was not scheduled.
	res, err = backup.TaskCreate(e.svc, rc, backup.CreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := e.svc.List(context.Background()); a[0].ID != res.ArchiveID || a[0].Manifest.Reason != "manual" {
		t.Fatalf("default reason = %q", a[0].Manifest.Reason)
	}
}

func TestVerifyTaskVerifiesRewrapsAndFails(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.keys.bruceKeys = newBruceKeys("d1", "d1", "d2")
	e.rebuild()
	e.settings.VerifyKeep = 2
	var made []backup.Archive
	for i := 0; i < 3; i++ {
		made = append(made, e.create("manual"))
	}
	rc := taskstest.NewFakeRun(ctx, backup.KindVerify).RC()
	res, err := backup.TaskVerify(e.svc, rc, backup.Empty{})
	if err != nil || res.Checked != 2 || res.Opened != 1 || res.Rewrapped != 0 || res.Failed != 0 {
		t.Fatalf("result = %+v, %v", res, err)
	}
	// d2 becomes the active key: every Own archive made under d1 is re-signed.
	e.keys.active = "d2"
	res, err = backup.TaskVerify(e.svc, rc, backup.Empty{})
	if err != nil || res.Rewrapped != 3 {
		t.Fatalf("result = %+v, %v; want the three d1 archives rewrapped", res, err)
	}
	list, _ := e.svc.List(ctx)
	for _, a := range list {
		if a.Manifest.Provenance.KeyID != "d2" {
			t.Fatalf("%s is still under %s", a.ID, a.Manifest.Provenance.KeyID)
		}
	}
	// A corrupted archive fails the run, but the rest were still verified.
	newest := list[0]
	if err := os.WriteFile(e.path(newest)+backup.ChecksumSidecar, []byte(strings.Repeat("0", 64)+"  x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = backup.TaskVerify(e.svc, rc, backup.Empty{})
	if err == nil || res.Failed != 1 {
		t.Fatalf("result = %+v, %v; want one failure and an error", res, err)
	}
	if res.Checked != 1 {
		t.Fatalf("Checked = %d: the other archive in verify_keep was skipped after a failure", res.Checked)
	}
	_ = made
}

func TestPruneAndStageTaskBodies(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	e.create("manual")
	e.settings.MaxCount = 1
	if _, err := backup.TaskPrune(e.svc, taskstest.NewFakeRun(ctx, backup.KindPrune).RC(), backup.Empty{}); err != nil {
		t.Fatal(err)
	}
	if list, _ := e.svc.List(ctx); len(list) != 1 {
		t.Fatalf("catalogue = %d", len(list))
	}
	list, _ := e.svc.List(ctx)
	rc := taskstest.NewFakeRun(ctx, backup.KindStageRestore).RC()
	if _, err := backup.TaskStage(e.svc, rc, backup.StageParams{ArchiveID: list[0].ID, Verify: "bogus"}); err == nil {
		t.Fatal("an unknown verify level was accepted")
	}
	res, err := backup.TaskStage(e.svc, rc, backup.StageParams{ArchiveID: list[0].ID, Verify: "entries", AllowWeakVerify: false})
	if err == nil {
		t.Fatalf("entries-level restore verification accepted without AllowWeakVerify: %+v", res)
	}
	res, err = backup.TaskStage(e.svc, rc, backup.StageParams{ArchiveID: list[0].ID})
	if err != nil || !res.RestartRequired || res.Pending.ArchiveID != list[0].ID {
		t.Fatalf("stage = %+v, %v", res, err)
	}
	_ = a
}
