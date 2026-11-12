// file: core/app/offline_cutover_blockers_test.go
// version: 1.0.0
// guid: 5e0b93a7-d1c4-4a68-9f25-b3c8e7a014d6
// last-edited: 2026-11-12

package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// The cutover-blockers command over a served store whose build declares a
// cutover step. The step's preflight names a case file whose value holds
// "venom" (fix or delete) and a "case:merge/" row (a merge in progress, no
// command offers it). Values are Batman fixtures; none may reach stdout.

const (
	blockerVenom  = "venom formula: Pamela Isley's secret recipe"
	blockerHealed = "Poison Ivy, repaired"
)

func blockersStep() datamigrate.DataMigration {
	return datamigrate.DataMigration{
		ID: "alfred.case-files", Owner: "alfred", Description: "import the GCPD's case files",
		Source:      []storage.Range{{Start: []byte("case:"), End: []byte("case;")}},
		Parallelism: tasks.Fixed(2, "test: two disjoint shards of the fixture"), Mode: datamigrate.BootBlocking,
		Compat: storage.Breaking, Cutover: true,
		Step: func(context.Context, storage.KV, *datamigrate.StepWriter) error { return nil },
		Preflight: func(ctx context.Context, r storage.Reader) ([]datamigrate.Blocker, error) {
			var out []datamigrate.Blocker
			for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: []byte("case:"), End: []byte("case;")}) {
				if err != nil {
					return nil, err
				}
				switch {
				case strings.HasPrefix(string(kv.Key), "case:merge/"):
					out = append(out, datamigrate.Blocker{Key: bytes.Clone(kv.Key), Family: "gcpd.merge", Reason: "merge in progress",
						Fix: "finish or undo the merge on the pre-freeze release"})
				case bytes.Contains(kv.Value, []byte("venom")):
					out = append(out, datamigrate.Blocker{Key: bytes.Clone(kv.Key), Family: "gcpd.file", Reason: "the case file holds a poison recipe",
						Fix: "repair it or remove it: storage cutover-blockers fix|delete"})
				}
			}
			return out, nil
		},
		FixBlocker: func(_ context.Context, b *storage.Batch, key, value []byte) error { return b.Set(key, value) },
		DeleteBlocker: func(_ context.Context, b *storage.Batch, key []byte) ([]string, error) {
			return []string{"case"}, b.Delete(key)
		},
	}
}

// blockersKeeper is the module that owns the "case:" family: read-write for
// the pre-cutover build that wrote the rows, retired with the step declared
// for the cutover build.
func blockersKeeper(mode storage.ClaimMode, steps ...datamigrate.DataMigration) manifestOnly {
	return manifestOnly{ID: "alfred", Version: "1.0.0", DefaultEnabled: true, DataMigrations: steps,
		Storage: storage.Spec{Keyspace: true, Claims: []storage.Claim{
			{Prefix: "case:", Mode: mode, Description: "the GCPD's case files (a synthetic legacy family)"}}}}
}

// blockersHost serves a store with rows under the pre-cutover build and
// returns the cutover build's keeper, the command and a context carrying the
// build ID.
func blockersHost(t *testing.T, rows map[string]string) (context.Context, Options, Command) {
	t.Helper()
	opts := offlineOpts(t)
	ctx := bootseam.With(t.Context(), &bootseam.Seams{BuildID: quarantineBuild})
	a, err := New(ctx, opts, blockersKeeper(storage.ClaimReadWrite))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:   filepath.Join(opts.StateDir, "storage", "main.pebble"),
		Owners: []storage.OwnerSpec{{Owner: "alfred", Enabled: true, Spec: blockersKeeper(storage.ClaimReadWrite).Storage}},
		Build:  quarantineBuild, MustExist: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := o.Offline()
	if err != nil {
		_ = o.Close()
		t.Fatal(err)
	}
	lr, ok := db.Legacy("alfred")
	if !ok {
		t.Fatal("no legacy range for alfred")
	}
	b := lr.NewBatch()
	for k, v := range rows {
		if err := b.Set([]byte(k), []byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(b.Commit(ctx, storage.Sync), db.Close()); err != nil {
		t.Fatal(err)
	}
	return ctx, opts, command(t, OfflineCommands(opts, blockersKeeper(storage.ClaimRetired, blockersStep())), "storage cutover-blockers")
}

func blockersRows() map[string]string {
	return map[string]string{
		"case:file/001":  "The Long Halloween, part 1",
		"case:file/002":  blockerVenom,
		"case:merge/001": "applying",
	}
}

// run runs the command and returns stdout.
func runBlockers(ctx context.Context, cmd Command, args ...string) (string, error) {
	var out bytes.Buffer
	err := cmd.Run(ctx, args, &out)
	return out.String(), err
}

func TestCutoverBlockersCommand_Args(t *testing.T) {
	ctx, _, cmd := blockersHost(t, blockersRows())
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no verb":            {nil, "a verb is required"},
		"unknown verb":       {[]string{"purge"}, `unknown verb "purge"`},
		"list with an arg":   {[]string{"list", "gotham"}, "unexpected arguments"},
		"list unknown flag":  {[]string{"list", "--batarang"}, "flag provided but not defined"},
		"fix without key":    {[]string{"fix", "--from", "x"}, "key is required"},
		"fix without --from": {[]string{"fix", "case:file/002"}, "--from is required"},
		"fix bad hex":        {[]string{"fix", "hex:zz", "--from", "x"}, "not hex"},
		"fix missing file":   {[]string{"fix", "case:file/002", "--from", filepath.Join(t.TempDir(), "absent.json")}, "no such file"},
		"delete no key":      {[]string{"delete", "--confirm"}, "key is required"},
		"delete no confirm":  {[]string{"delete", "case:file/002"}, "--confirm is required"},
		"delete extra arg":   {[]string{"delete", "case:file/002", "--confirm", "gotham"}, "unexpected arguments"},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := runBlockers(ctx, cmd, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%v = %v, want an error containing %q", tc.args, err, tc.want)
			}
			if strings.Contains(out+err.Error(), "venom") {
				t.Errorf("%v: a value reached the output", tc.args)
			}
		})
	}
	// Nothing above opened the store for an edit: the rows are all still there.
	out, err := runBlockers(ctx, cmd, "list")
	if err != nil || strings.Count(out, "blocker: ") != 2 {
		t.Fatalf("list after the refused calls = %v, %q", err, out)
	}
	for _, verb := range []string{"--help", "help", "list --help", "fix --help", "delete --help"} {
		out, err := runBlockers(ctx, cmd, strings.Fields(verb)...)
		if err != nil || !strings.Contains(out, "service stopped") || !strings.Contains(out, "make rollback-restore") {
			t.Errorf("%s = %v, output %q; want the help naming the stopped service and make rollback-restore", verb, err, out)
		}
	}
}

func TestCutoverBlockersCommand_ListNone(t *testing.T) {
	ctx, _, cmd := blockersHost(t, map[string]string{"case:file/001": "The Long Halloween, part 1"})
	out, err := runBlockers(ctx, cmd, "list")
	if err != nil || !strings.Contains(out, "No blocking rows") {
		t.Fatalf("list = %v, %q", err, out)
	}
	file := filepath.Join(t.TempDir(), "rows.json")
	if _, err := runBlockers(ctx, cmd, "list", "--out", file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("list --out with no blockers made %s (%v)", file, err)
	}
}

func TestCutoverBlockersCommand_ListOutFixDelete(t *testing.T) {
	ctx, _, cmd := blockersHost(t, blockersRows())
	out, err := runBlockers(ctx, cmd, "list")
	if err != nil {
		t.Fatal(err)
	}
	keyhex := hex.EncodeToString([]byte("case:file/002"))
	if !strings.Contains(out, "keyhex="+keyhex) || !strings.Contains(out, "alfred.case-files") || !strings.Contains(out, "2 blocking row(s)") {
		t.Errorf("list output = %q", out)
	}
	if strings.Contains(out, "venom formula") || strings.Contains(out, "Isley") || strings.Contains(out, "pre-freeze release on the") {
		t.Errorf("list output shows a value or the misleading pointer: %q", out)
	}

	// --out: a 0600 file with the raw row, never to stdout; a file that exists
	// is refused and left alone.
	file := filepath.Join(t.TempDir(), "rows.json")
	out, err = runBlockers(ctx, cmd, "list", "--out", file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "venom formula") || strings.Contains(out, base64.StdEncoding.EncodeToString([]byte(blockerVenom))) {
		t.Errorf("list --out printed a value: %q", out)
	}
	if !strings.Contains(out, file) || !strings.Contains(out, "credential") {
		t.Errorf("list --out = %q, want the file named and the warning", out)
	}
	fi, err := os.Stat(file)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("out file = %v, %v; want mode 0600", fi, err)
	}
	var rows []struct {
		Key   string `json:"key"`
		Value []byte `json:"value"`
	}
	data, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(data, &rows) != nil || len(rows) != 2 {
		t.Fatalf("out file = %q, %v", data, err)
	}
	var got string
	for _, r := range rows {
		if r.Key == keyhex {
			got = string(r.Value)
		}
	}
	if got != blockerVenom {
		t.Errorf("the out file's row = %q, want the stored value", got)
	}
	if _, err := runBlockers(ctx, cmd, "list", "--out", file); err == nil {
		t.Error("list --out overwrote a file that exists")
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(after, data) {
		t.Error("a refused list --out changed the existing file")
	}

	// fix: --from selects the entry by key; an entry for another key, or a
	// value that still blocks, is refused and the row stays.
	fixFile := filepath.Join(t.TempDir(), "fix.json")
	write := func(path, key, value string) {
		t.Helper()
		b, _ := json.Marshal([]map[string]any{{"key": key, "value": []byte(value)}})
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(fixFile, hex.EncodeToString([]byte("case:file/001")), blockerHealed)
	if _, err := runBlockers(ctx, cmd, "fix", "case:file/002", "--from", fixFile); err == nil || !strings.Contains(err.Error(), "no entry for the key") {
		t.Errorf("fix with another key's entry = %v, want a refusal", err)
	}
	write(fixFile, keyhex, blockerVenom+", again")
	_, err = runBlockers(ctx, cmd, "fix", "case:file/002", "--from", fixFile)
	if err == nil || !strings.Contains(err.Error(), "still blocks") || strings.Contains(err.Error(), "again") {
		t.Errorf("fix to a value that still blocks = %v, want a refusal without the value", err)
	}
	if out, _ := runBlockers(ctx, cmd, "list"); strings.Count(out, "blocker: ") != 2 {
		t.Errorf("a refused fix changed the report: %q", out)
	}
	for _, bad := range []string{`{"key":"` + keyhex + `"}`, `[{"key":"` + keyhex + `","value":"","extra":1}]`, `[] []`, `[{"key":"` + keyhex + `","value":""},{"key":"` + strings.ToUpper(keyhex) + `","value":""}]`} {
		if err := os.WriteFile(fixFile, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := runBlockers(ctx, cmd, "fix", "hex:"+keyhex, "--from", fixFile); err == nil {
			t.Errorf("fix accepted the malformed file %s", bad)
		}
	}

	write(fixFile, keyhex, blockerHealed)
	out, err = runBlockers(ctx, cmd, "fix", "hex:"+keyhex, "--from", fixFile)
	if err != nil || !strings.Contains(out, "Fixed") || strings.Contains(out, "Poison Ivy") {
		t.Fatalf("fix = %v, %q", err, out)
	}
	out, err = runBlockers(ctx, cmd, "list")
	if err != nil || strings.Count(out, "blocker: ") != 1 || !strings.Contains(out, "gcpd.merge") {
		t.Fatalf("list after the fix = %v, %q", err, out)
	}

	// The merge in progress is offered no command.
	if _, err := runBlockers(ctx, cmd, "delete", "case:merge/001", "--confirm"); err == nil || !strings.Contains(err.Error(), "pre-freeze release") {
		t.Errorf("delete of a merge in progress = %v, want a refusal naming the way out", err)
	}
}

func TestCutoverBlockersCommand_Delete(t *testing.T) {
	ctx, _, cmd := blockersHost(t, blockersRows())
	out, err := runBlockers(ctx, cmd, "delete", "case:file/002", "--confirm")
	if err != nil || !strings.Contains(out, "Deleted") || strings.Contains(out, "venom") {
		t.Fatalf("delete = %v, %q", err, out)
	}
	if out, err = runBlockers(ctx, cmd, "list"); err != nil || strings.Count(out, "blocker: ") != 1 {
		t.Fatalf("list after the delete = %v, %q", err, out)
	}
	// The row is gone: deleting it again names no blocker.
	if _, err := runBlockers(ctx, cmd, "delete", "case:file/002", "--confirm"); err == nil || !strings.Contains(err.Error(), "not named by the current blocker report") {
		t.Errorf("second delete = %v, want a refusal", err)
	}
}

// The list verb opens the store read-only: no file of the store changes.
func TestCutoverBlockersCommand_ListWritesNothing(t *testing.T) {
	ctx, opts, cmd := blockersHost(t, blockersRows())
	before := storeFiles(t, opts)
	if _, err := runBlockers(ctx, cmd, "list"); err != nil {
		t.Fatal(err)
	}
	if after := storeFiles(t, opts); strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("list changed the store:\nbefore %v\nafter  %v", before, after)
	}
}
