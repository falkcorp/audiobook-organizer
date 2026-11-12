// file: core/backup/stage_test.go
// version: 1.0.1
// guid: 6c1f9a3e-2b84-4d70-a5e9-8f3b0d7c4a12
// last-edited: 2026-11-12

package backup_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/internal/backuparchive"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// asOwner is a context carrying the proof the restore route establishes after
// its interactive and owner guards (backup.RestoreProof): the options that need
// proof are only reachable with it.
func asOwner(ctx context.Context) context.Context {
	return backup.WithRestoreProof(ctx, backup.RestoreProof{Owner: true, Admin: true})
}

func shaHex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// liveFiles is the live store directory's immutable files (tables and the
// manifest/options records) and their sizes. The write-ahead log is excluded:
// an idle store may still touch it.
func liveFiles(t *testing.T, dir string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n := d.Name()
		if strings.HasSuffix(n, ".log") || n == "LOG" || n == "LOCK" {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = fi.Size()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStagedRestoreHappyPathAndCancel(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var events []string
	e.opts.Audit = func(_ context.Context, ev string, d map[string]string) error {
		events = append(events, ev+" foreign="+d["foreign"])
		return nil
	}
	e.opts.Actor = func(context.Context) string { return "alfred" }
	e.rebuild()
	a := e.create("manual")
	pr, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if pr.ArchiveID != a.ID || pr.RequestedBy != "alfred" || pr.StagedByBuild != "build-1" {
		t.Fatalf("PendingRestore = %+v", pr)
	}
	if len(events) != 1 || events[0] != "core.restore.stage foreign=false" {
		t.Fatalf("audit events = %v", events)
	}
	if len(e.stager.staged) != 1 {
		t.Fatalf("intents = %d", len(e.stager.staged))
	}
	in := e.stager.staged[0]
	if in.Kind != restorejournal.KindArchive || in.DBPath != e.st.Path() || in.ArchiveID != string(a.ID) || in.InstallID != e.st.DB().Admin().InstallID() {
		t.Fatalf("intent = %+v", in)
	}
	if len(in.Targets) != 1 || in.Targets[0].To != e.st.Path() || in.Targets[0].From != filepath.Join(in.Staging, "db") {
		t.Fatalf("targets = %+v", in.Targets)
	}
	if in.Staging != restorejournal.StagingDir(e.st.Path(), in.ID) {
		t.Fatalf("staging %s is not the swap's staging dir", in.Staging)
	}
	want := 24 * 60 * 60
	if ttl := e.settings.IntentTTL; ttl > 0 {
		want = int(ttl.Seconds())
	}
	if got := int(in.ExpiresAt.Sub(in.RequestedAt).Seconds()); got != want {
		t.Fatalf("intent ttl = %ds, want %ds", got, want)
	}
	// The anchor step b re-checks: the staged manifest hashes to the intent's.
	mb, err := os.ReadFile(filepath.Join(in.Staging, backup.ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if shaHex(mb) != in.ManifestSHA256 {
		t.Fatal("the intent's manifest_sha256 is not the staged manifest's")
	}
	if fi, err := os.Stat(filepath.Join(in.Staging, "db")); err != nil || !fi.IsDir() {
		t.Fatalf("no staged db: %v", err)
	}
	if err := backup.Recheck(nil)(ctx, in); err != nil {
		t.Fatalf("Recheck of a fresh staging: %v", err)
	}
	// Pending reports it; a second stage and a second archive are refused.
	pend, err := e.svc.Pending(ctx)
	if err != nil || pend == nil || pend.ArchiveID != a.ID {
		t.Fatalf("Pending = %+v, %v", pend, err)
	}
	if _, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{}); !errors.Is(err, restorejournal.ErrSwapPending) {
		t.Fatalf("second stage: %v, want ErrSwapPending", err)
	}
	if len(e.stager.staged) != 1 {
		t.Fatal("a second intent was created")
	}
	// The staged restore's archive cannot be deleted from under it.
	if err := e.svc.Delete(ctx, a.ID); !errors.Is(err, backup.ErrProtectedByFloor) {
		t.Fatalf("Delete of a staged restore's archive: %v", err)
	}
	// Cancel removes the intent and the staging directory, and frees the slot.
	if err := e.svc.CancelRestore(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(in.Staging); !os.IsNotExist(err) {
		t.Fatalf("staging survived Cancel: %v", err)
	}
	if pend, _ := e.svc.Pending(ctx); pend != nil {
		t.Fatalf("Pending after Cancel = %+v", pend)
	}
	if err := e.svc.CancelRestore(ctx); err != nil {
		t.Fatalf("Cancel with nothing staged: %v", err)
	}
	if _, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{}); err != nil {
		t.Fatalf("stage after Cancel: %v", err)
	}
}

func TestCancelRefusesWhileSwapInFlight(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	dir := restorejournal.JournalDir(e.stateDir, "20261112T010000Z-0badc0de")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "inflight.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.CancelRestore(ctx); !errors.Is(err, restorejournal.ErrSwapPending) {
		t.Fatalf("Cancel during a swap: %v, want ErrSwapPending", err)
	}
	a := e.create("manual")
	if _, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{}); !errors.Is(err, restorejournal.ErrSwapPending) {
		t.Fatalf("stage during a swap: %v, want ErrSwapPending", err)
	}
}

func TestRestoreNeverWritesLiveStore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	before := liveFiles(t, e.st.Path())
	checkpoints, advances := e.log.index("checkpoint"), len(e.markers.advances)
	if _, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	after := liveFiles(t, e.st.Path())
	if len(before) != len(after) {
		t.Fatalf("live store files changed: %v -> %v", before, after)
	}
	for n, sz := range before {
		if after[n] != sz {
			t.Fatalf("live file %s: %d -> %d bytes", n, sz, after[n])
		}
	}
	if e.log.index("checkpoint") != checkpoints || len(e.markers.advances) != advances {
		t.Fatalf("staging touched the live store: %v", e.log.list())
	}
	ks := e.st.Keyspace(gothamOwner)
	v, err := ks.Get(ctx, []byte("rogue/a"))
	if err != nil || string(v) != "arkham-a" {
		t.Fatalf("live key = %q, %v", v, err)
	}
	// Everything staging wrote is under the DB's parent or the state dir.
	for _, p := range e.stager.staged[0].Targets {
		if !strings.HasPrefix(p.From, e.dbParent+string(filepath.Separator)) {
			t.Fatalf("staged %s is outside the database's directory", p.From)
		}
	}
}

// madeElsewhere builds an archive in another install and imports it into e.
func madeElsewhere(t *testing.T, e *env, mutate func(other *env)) backup.Archive {
	t.Helper()
	other := newEnv(t)
	if mutate != nil {
		mutate(other)
	}
	a := other.create("manual")
	rc, err := other.svc.Download(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, err := e.svc.Import(context.Background(), "", rc)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestForeignArchiveNeedsFlag(t *testing.T) {
	ctx := asOwner(context.Background())
	cases := map[string]func(other *env){
		"another install, same derivation key": nil,
		"another derivation key": func(o *env) {
			o.opts.Keyring = &secretKeys{bruceKeys: newBruceKeys("joker", "joker")}
			o.rebuild()
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			got := madeElsewhere(t, e, mutate)
			if got.Provenance != backup.Foreign {
				t.Fatalf("imported provenance = %v, want Foreign", got.Provenance)
			}
			if _, err := e.svc.StageRestore(ctx, got.ID, backup.RestoreOptions{}); !errors.Is(err, backup.ErrForeignArchive) {
				t.Fatalf("err = %v, want ErrForeignArchive", err)
			}
			if len(e.stager.staged) != 0 {
				t.Fatal("a foreign archive was staged without the flag")
			}
			if ents, _ := os.ReadDir(filepath.Join(e.dbParent, ".mediacore-restore")); len(ents) != 0 {
				t.Fatalf("a refused foreign restore left %d staging entries", len(ents))
			}
			if _, err := e.svc.StageRestore(ctx, got.ID, backup.RestoreOptions{Foreign: true}); err != nil {
				t.Fatalf("with Foreign: %v", err)
			}
			pend, _ := e.svc.Pending(ctx)
			if pend == nil || pend.ArchiveID != got.ID {
				t.Fatalf("Pending = %+v", pend)
			}
		})
	}
}

func TestUnreadableSecretsRefused(t *testing.T) {
	ctx := asOwner(context.Background())
	e := newEnv(t)
	a := e.create("manual")
	e.keys.referenced = []string{"d1", "ra-s-al-ghul-key"}
	_, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{})
	if !errors.Is(err, backup.ErrUnreadableSecrets) {
		t.Fatalf("err = %v, want ErrUnreadableSecrets", err)
	}
	if !strings.Contains(err.Error(), "ra-s-al-ghul-key") || strings.Contains(err.Error(), "d1,") {
		t.Fatalf("the refusal must name the missing key and only it: %v", err)
	}
	if len(e.stager.staged) != 0 {
		t.Fatal("an intent was created for an unreadable-secrets restore")
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dbParent, ".mediacore-restore")); len(ents) != 0 {
		t.Fatalf("the refused restore left %d staging entries", len(ents))
	}
	pr, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{AcceptUnreadableSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(pr.Warnings, "|"), "re-entered") {
		t.Fatalf("no warning about unreadable secrets: %v", pr.Warnings)
	}
}

func TestRestoreRefusesWeakOrBrokenVerification(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	if _, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{Verify: backup.VerifyChecksum}); err == nil {
		t.Fatal("a checksum-level restore verification was accepted without AllowWeakVerify")
	}
	if len(e.stager.staged) != 0 {
		t.Fatal("staged on a weak verification")
	}
	// A flipped byte in the stored archive fails the default (open) verification.
	p := e.path(a)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0xff
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{}); err == nil {
		t.Fatal("a corrupted archive was staged")
	}
	if len(e.stager.staged) != 0 {
		t.Fatal("a corrupted archive produced an intent")
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dbParent, ".mediacore-restore")); len(ents) != 0 {
		t.Fatalf("a corrupted archive left %d staging entries", len(ents))
	}
}

func TestStageAuditFailureRefuses(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	e.opts.Audit = func(context.Context, string, map[string]string) error { return errors.New("the ledger is shut") }
	e.rebuild()
	if _, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{}); err == nil {
		t.Fatal("a restore was staged though its audit record could not be written")
	}
	if len(e.stager.staged) != 0 {
		t.Fatal("an intent exists without its audit record")
	}
}

// bindProbe reports one path as a single-file bind mount: its device differs
// from its directory's.
type bindProbe struct{ bound string }

func (p bindProbe) ItemDev(path string) (uint64, error) {
	if filepath.Base(path) == p.bound {
		return 7, nil
	}
	return 1, nil
}
func (bindProbe) DirDev(string) (uint64, error) { return 1, nil }
func (bindProbe) Writable(string) error         { return nil }

func TestRenamePrecheckRefusesBindMountedConfig(t *testing.T) {
	ctx := asOwner(context.Background())
	confDir := t.TempDir()
	cfg, boot := filepath.Join(confDir, "mediacore.yaml"), filepath.Join(confDir, "config.boot.json")
	for p, body := range map[string]string{cfg: "library: gotham\n", boot: `{"storage":"x"}`} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setup := func(p restorejournal.Probe) *env {
		e := newEnv(t)
		e.opts.Config, e.opts.ConfigPath, e.opts.BootRecordPath, e.opts.Probe = e.cfg, cfg, boot, p
		e.rebuild()
		return e
	}
	// A plain filesystem: the files are placed by the swap.
	e := setup(nil)
	a := e.create("manual")
	if _, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{ReplaceConfig: true}); err != nil {
		t.Fatal(err)
	}
	if got := targetsOf(e.stager.staged[0]); !strings.Contains(got, "mediacore.yaml") || !strings.Contains(got, "config.boot.json") {
		t.Fatalf("ReplaceConfig did not place the config files: %s", got)
	}
	// Without the flag the files are only staged for the operator.
	e = setup(nil)
	a = e.create("manual")
	pr, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.stager.staged[0].Targets) != 1 {
		t.Fatalf("config targets without ReplaceConfig: %s", targetsOf(e.stager.staged[0]))
	}
	if !strings.Contains(strings.Join(pr.Warnings, "|"), "config.restored") {
		t.Fatalf("no pointer to the staged config copy: %v", pr.Warnings)
	}
	// A bind-mounted config file cannot be renamed over: the restore is still
	// staged, the config is not part of the swap, and the warning says why.
	e = setup(bindProbe{bound: "mediacore.yaml"})
	a = e.create("manual")
	pr, err = e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{ReplaceConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := targetsOf(e.stager.staged[0]); strings.Contains(got, "mediacore.yaml") {
		t.Fatalf("a bind-mounted config was put into the swap: %s", got)
	}
	if w := strings.Join(pr.Warnings, "|"); !strings.Contains(w, "cannot be swapped by rename") {
		t.Fatalf("warnings = %v", pr.Warnings)
	}
	kept, err := os.ReadFile(cfg)
	if err != nil || string(kept) != "library: gotham\n" {
		t.Fatalf("the live config changed: %q, %v", kept, err)
	}
}

// The warnings of a staged restore reach API bodies, so they carry fixed text:
// the error of a failed restart request, the refusal of the rename pre-check and
// the place the config copy went are host text that belongs in the operator's
// log. The existing phrases the operator reads stay.
func TestStageWarningsCarryNoHostText(t *testing.T) {
	ctx := asOwner(context.Background())
	const restartText = "dial unix /srv/gotham/batcave/restart.sock: connection refused"
	confDir := t.TempDir()
	cfg, boot := filepath.Join(confDir, "mediacore.yaml"), filepath.Join(confDir, "config.boot.json")
	for p, body := range map[string]string{cfg: "library: gotham\n", boot: `{"storage":"x"}`} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var logs bytes.Buffer
	setup := func(p restorejournal.Probe) *env {
		e := newEnv(t)
		e.opts.Config, e.opts.ConfigPath, e.opts.BootRecordPath, e.opts.Probe = e.cfg, cfg, boot, p
		e.opts.Restart = func(context.Context) error { return errors.New(restartText) }
		e.opts.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		e.rebuild()
		return e
	}
	noHostText := func(what string, e *env, warnings []string) {
		t.Helper()
		all := strings.Join(warnings, "|")
		for _, leak := range []string{restartText, "/srv/", confDir, e.stateDir, e.dbParent} {
			if strings.Contains(all, leak) {
				t.Errorf("%s: a warning carries host text %q: %v", what, leak, warnings)
			}
		}
	}

	// A restart that fails after the archive was staged, with the config copy
	// left for the operator: the pointers are fixed text.
	e := setup(nil)
	a := e.create("manual")
	pr, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{Restart: true})
	if err != nil {
		t.Fatal(err)
	}
	noHostText("stage", e, pr.Warnings)
	all := strings.Join(pr.Warnings, "|")
	if !strings.Contains(all, "the restart request failed") || !strings.Contains(all, "<state_dir>/restore/") || !strings.Contains(all, "config.restored") {
		t.Errorf("the warnings lost the phrases the operator acts on: %v", pr.Warnings)
	}
	if out := logs.String(); !strings.Contains(out, restartText) || !strings.Contains(out, e.stateDir) {
		t.Errorf("the operator's log lacks the restart error or the config copy's location: %s", out)
	}

	// The rename pre-check's refusal names files; the warning keeps the phrase only.
	logs.Reset()
	e = setup(bindProbe{bound: "mediacore.yaml"})
	a = e.create("manual")
	pr, err = e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{ReplaceConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	noHostText("rename refused", e, pr.Warnings)
	if !strings.Contains(strings.Join(pr.Warnings, "|"), "cannot be swapped by rename") {
		t.Errorf("warnings = %v", pr.Warnings)
	}
	if out := logs.String(); !strings.Contains(out, "cannot be swapped by rename") || !strings.Contains(out, "single-file bind mount") || !strings.Contains(out, e.dbParent) {
		t.Errorf("the operator's log lacks the refusal and the files it names: %s", out)
	}

	// The same for a revert whose restart fails.
	e = setup(nil)
	revertFixture(t, e, "20261111T030000Z-dddddddd", "applied", true)
	pr, err = e.svc.RevertLastRestore(ctx, backup.RestoreOptions{Restart: true})
	if err != nil {
		t.Fatal(err)
	}
	noHostText("revert", e, pr.Warnings)
	if !strings.Contains(strings.Join(pr.Warnings, "|"), "the restart request failed") {
		t.Errorf("revert warnings = %v", pr.Warnings)
	}
}

func targetsOf(in restorejournal.Intent) string {
	var parts []string
	for _, t := range in.Targets {
		parts = append(parts, filepath.Base(t.To))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func TestRedactedConfigIsNeverSwappedIn(t *testing.T) {
	ctx := asOwner(context.Background())
	confDir := t.TempDir()
	cfg := filepath.Join(confDir, "mediacore.yaml")
	if err := os.WriteFile(cfg, []byte("openai_key: "+canaryKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.cfg.secrets, e.cfg.redacted = true, []byte("openai_key: <redacted>\n")
	e.opts.Config, e.opts.ConfigPath = e.cfg, cfg
	e.rebuild()
	a := e.create("manual")
	pr, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{ReplaceConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := targetsOf(e.stager.staged[0]); strings.Contains(got, "mediacore.yaml") {
		t.Fatalf("the redacted copy would replace the live config: %s", got)
	}
	if !strings.Contains(strings.Join(pr.Warnings, "|"), "redacted") {
		t.Fatalf("no warning about the redacted copy: %v", pr.Warnings)
	}
	live, _ := os.ReadFile(cfg)
	if !strings.Contains(string(live), canaryKey) {
		t.Fatal("the live config lost its secret")
	}
}

func TestRecheckDetectsTamperedStaging(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	if _, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	in := e.stager.staged[0]
	var victim string
	_ = filepath.WalkDir(filepath.Join(in.Staging, "db"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && strings.HasSuffix(p, ".sst") && victim == "" {
			victim = p
		}
		return nil
	})
	if victim == "" {
		t.Fatal("the staged store has no table file to tamper with")
	}
	orig, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if err := os.WriteFile(victim, orig, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Same size, one flipped byte: only the re-hash notices.
	flipped := append([]byte(nil), orig...)
	flipped[len(flipped)/2] ^= 0xff
	if err := os.WriteFile(victim, flipped, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backup.Recheck(nil)(ctx, in); err != nil {
		t.Fatalf("a size-only recheck must not hash: %v", err)
	}
	if err := backup.Recheck(func() bool { return true })(ctx, in); err == nil {
		t.Fatal("rehash_on_boot did not notice a flipped byte")
	}
	// A changed size is caught either way.
	if err := os.WriteFile(victim, append(orig, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backup.Recheck(nil)(ctx, in); err == nil {
		t.Fatal("a grown staged file was accepted")
	}
	restore()
	if err := backup.Recheck(func() bool { return true })(ctx, in); err != nil {
		t.Fatalf("restored file: %v", err)
	}
	// A rewritten manifest no longer hashes to the intent's digest.
	mp := filepath.Join(in.Staging, backup.ManifestName)
	mb, _ := os.ReadFile(mp)
	if err := os.WriteFile(mp, append(bytes.TrimRight(mb, "\n"), ' ', '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := backup.Recheck(nil)(ctx, in); err == nil {
		t.Fatal("a rewritten staged manifest was accepted")
	}
	if err := os.WriteFile(mp, mb, 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink in place of a staged file is not a regular file.
	if err := os.Remove(victim); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.dbParent, "elsewhere"), victim); err != nil {
		t.Fatal(err)
	}
	if err := backup.Recheck(nil)(ctx, in); err == nil {
		t.Fatal("a symlinked staged file was accepted")
	}
	_ = os.Remove(victim)
	restore()
	// A missing source is caught before the manifest is read.
	if err := os.RemoveAll(filepath.Join(in.Staging, "db")); err != nil {
		t.Fatal(err)
	}
	if err := backup.Recheck(nil)(ctx, in); err == nil {
		t.Fatal("a missing staged store was accepted")
	}
	// An intent that names no manifest hash is refused.
	in.ManifestSHA256 = ""
	if err := backup.Recheck(nil)(ctx, in); err == nil {
		t.Fatal("an intent without a manifest hash was accepted")
	}
}

func TestHostLossRestore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a := e.create("manual")
	// A fresh host: no live store, the keyring restored from the off-host copy.
	e.opts.Store = nil
	e.rebuild()
	list, err := e.svc.List(ctx)
	if err != nil || len(list) != 1 || list[0].Provenance != backup.Own {
		t.Fatalf("List = %+v, %v; the archive is Own when there is no live store to disagree", list, err)
	}
	if _, err := e.svc.StageRestore(ctx, a.ID, backup.RestoreOptions{}); err != nil {
		t.Fatalf("host-loss restore: %v", err)
	}
	if in := e.stager.staged[0]; in.InstallID != "" {
		t.Fatalf("intent install id = %q, want none without a live store", in.InstallID)
	}
	// With another keyring (the wrong off-host copy) the same archive is Foreign.
	e2 := newEnv(t)
	e2.opts.Store = nil
	e2.opts.Keyring = &secretKeys{bruceKeys: newBruceKeys("joker", "joker")}
	e2.rebuild()
	b, err := os.ReadFile(e.path(a))
	if err != nil {
		t.Fatal(err)
	}
	imp, err := e2.svc.Import(ctx, "", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if imp.Provenance != backup.Foreign {
		t.Fatalf("under the wrong keyring: %v", imp.Provenance)
	}
}

// legacyArchive tars a real checkpoint of the fixture's store under HEAD's
// audiobooks.pebble/ directory, compressed with codec, into the backup
// directory under a catalogue-conformant name.
func legacyArchive(t *testing.T, e *env, codec string, sidecar bool) (name string) {
	t.Helper()
	cp := filepath.Join(t.TempDir(), "cp")
	if err := e.st.DB().Admin().Checkpoint(context.Background(), cp); err != nil {
		t.Fatal(err)
	}
	c, err := backuparchive.Resolve(codec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	name = e.opts.Prefix + "20261112_020000" + c.Extension()
	out, err := os.OpenFile(filepath.Join(e.backupDir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	cw, err := c.NewWriter(io.MultiWriter(out, h), 0)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(cw)
	if err := tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/", Typeflag: tar.TypeDir, Mode: 0o700}); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(cp)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ents {
		b, err := os.ReadFile(filepath.Join(cp, d.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: "audiobooks.pebble/" + d.Name(), Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(b))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(tw.Close(), cw.Close(), out.Close()); err != nil {
		t.Fatal(err)
	}
	if sidecar {
		line := hex.EncodeToString(h.Sum(nil)) + "  " + name + "\n"
		if err := os.WriteFile(filepath.Join(e.backupDir, name+backup.ChecksumSidecar), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return name
}

func TestLegacyABOArchiveRestores(t *testing.T) {
	ctx := asOwner(context.Background())
	for _, codec := range []string{"gzip", "zstd", "none"} {
		for _, sidecar := range []bool{true, false} {
			name := codec + "/sidecar"
			if !sidecar {
				name = codec + "/no-sidecar"
			}
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				fname := legacyArchive(t, e, codec, sidecar)
				list, err := e.svc.List(ctx)
				if err != nil || len(list) != 1 {
					t.Fatalf("List = %+v, %v", list, err)
				}
				l := list[0]
				if l.Name != fname || l.Format != backup.FormatLegacyABO || l.Provenance != backup.PreProvenance {
					t.Fatalf("catalogued as %+v", l)
				}
				// Legacy archives predate provenance: they restore as Foreign only. Without
				// a sidecar the weaker verification is refused first.
				want := backup.ErrForeignArchive
				if !sidecar {
					want = backup.ErrNoChecksum
				}
				if _, err := e.svc.StageRestore(ctx, l.ID, backup.RestoreOptions{}); !errors.Is(err, want) {
					t.Fatalf("err = %v, want %v", err, want)
				}
				opts := backup.RestoreOptions{Foreign: true}
				if !sidecar {
					if _, err := e.svc.StageRestore(ctx, l.ID, opts); !errors.Is(err, backup.ErrNoChecksum) {
						t.Fatalf("no sidecar and no AllowWeakVerify: %v, want ErrNoChecksum", err)
					}
					opts.AllowWeakVerify = true
				}
				pr, err := e.svc.StageRestore(ctx, l.ID, opts)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(strings.Join(pr.Warnings, "|"), backup.WarnGapFill) {
					t.Fatalf("no gap-fill warning: %v", pr.Warnings)
				}
				in := e.stager.staged[0]
				lm, err := os.ReadFile(filepath.Join(in.Staging, "legacy-manifest.json"))
				if err != nil {
					t.Fatal(err)
				}
				if shaHex(lm) != in.ManifestSHA256 {
					t.Fatal("the intent's manifest_sha256 is not the staged legacy manifest's")
				}
				var staged struct {
					Format  string
					Entries []backup.Entry
				}
				if err := json.Unmarshal(lm, &staged); err != nil || len(staged.Entries) == 0 {
					t.Fatalf("legacy manifest: %v %+v", err, staged)
				}
				for _, en := range staged.Entries {
					if !strings.HasPrefix(en.Path, "db/") {
						t.Fatalf("legacy entry %s is not under db/", en.Path)
					}
				}
				if err := backup.Recheck(func() bool { return true })(ctx, in); err != nil {
					t.Fatalf("Recheck of a staged legacy restore: %v", err)
				}
			})
		}
	}
}

func TestLegacyArchiveWithBadSidecarRefused(t *testing.T) {
	ctx := asOwner(context.Background())
	e := newEnv(t)
	fname := legacyArchive(t, e, "gzip", true)
	bad := strings.Repeat("0", 64) + "  " + fname + "\n"
	if err := os.WriteFile(filepath.Join(e.backupDir, fname+backup.ChecksumSidecar), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	list, _ := e.svc.List(ctx)
	if _, err := e.svc.StageRestore(ctx, list[0].ID, backup.RestoreOptions{Foreign: true, AllowWeakVerify: true}); !errors.Is(err, backup.ErrChecksumMismatch) {
		t.Fatalf("err = %v, want ErrChecksumMismatch (a present sidecar is never skipped)", err)
	}
	if len(e.stager.staged) != 0 {
		t.Fatal("staged over a checksum mismatch")
	}
}

// revertFixture leaves the files an applied restore keeps: its journal ending
// at step and, when previous is set, the set-aside store.
func revertFixture(t *testing.T, e *env, id, step string, previous bool) (prev string) {
	t.Helper()
	jd := restorejournal.JournalDir(e.stateDir, id)
	if err := os.MkdirAll(jd, 0o700); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, s := range []string{"staged", step} {
		line, _ := json.Marshal(restorejournal.Record{Step: s, Actor: "alfred"})
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(jd, restorejournal.JournalName), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	prev = restorejournal.SetAsideDest(e.stateDir, e.st.Path(), id, e.st.Path())
	if previous {
		if err := os.MkdirAll(prev, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(prev, "CURRENT"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return prev
}

func TestRevertLastRestore(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.svc.RevertLastRestore(ctx, backup.RestoreOptions{}); !errors.Is(err, backup.ErrNoUndoPoint) {
		t.Fatalf("nothing to undo: %v", err)
	}
	revertFixture(t, e, "20261110T010000Z-aaaaaaaa", "swapped", true)  // never applied
	revertFixture(t, e, "20261110T020000Z-bbbbbbbb", "applied", false) // previous dir gone
	if _, err := e.svc.RevertLastRestore(ctx, backup.RestoreOptions{}); !errors.Is(err, backup.ErrNoUndoPoint) {
		t.Fatalf("an unapplied restore and one without a previous/ are no undo points: %v", err)
	}
	old := revertFixture(t, e, "20261110T030000Z-cccccccc", "applied", true)
	newest := revertFixture(t, e, "20261111T030000Z-dddddddd", "applied_offline", true)
	_ = old
	pr, err := e.svc.RevertLastRestore(ctx, backup.RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(e.stager.staged) != 1 {
		t.Fatalf("intents = %d", len(e.stager.staged))
	}
	in := e.stager.staged[0]
	if in.Kind != restorejournal.KindRevert || len(in.Targets) != 1 || in.Targets[0].From != newest || in.Targets[0].To != e.st.Path() {
		t.Fatalf("revert intent = %+v, want the newest applied restore's previous/ %s", in, newest)
	}
	if pr.Kind == 0 || pr.ArchiveID != "" {
		t.Fatalf("PendingRestore = %+v", pr)
	}
	if _, err := e.svc.RevertLastRestore(ctx, backup.RestoreOptions{}); !errors.Is(err, restorejournal.ErrSwapPending) {
		t.Fatalf("a second revert while one is staged: %v", err)
	}
}
