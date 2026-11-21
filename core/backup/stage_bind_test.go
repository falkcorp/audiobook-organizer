// file: core/backup/stage_bind_test.go
// version: 0.1.0
// guid: 4d9a1f62-7c3e-4b85-a0e6-2f8b5d1c7a93
// last-edited: 2026-11-20

package backup_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/internal/backuparchive"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// auditRec is one call of Options.Audit.
type auditRec struct {
	action string
	fields map[string]string
}

// observe gives e a metrics scope and an audit recorder, rebuilds the service,
// and returns the provider and the recorded audit calls.
func observe(t *testing.T, e *env) (*metrics.Provider, *[]auditRec) {
	t.Helper()
	ctx := context.Background()
	p, err := metrics.New(ctx, metrics.Options{AppID: "gotham", Prometheus: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })
	sc, err := p.Core("backup", backup.ScopeAttributes()...)
	if err != nil {
		t.Fatal(err)
	}
	m, err := backup.NewMetrics(sc)
	if err != nil {
		t.Fatal(err)
	}
	recs := &[]auditRec{}
	e.opts.Metrics = m
	e.opts.Audit = func(_ context.Context, action string, f map[string]string) error {
		*recs = append(*recs, auditRec{action: action, fields: f})
		return nil
	}
	e.rebuild()
	return p, recs
}

// rejected is mediacore_backup_restore_total{stage="rejected"} for foreign.
func rejected(t *testing.T, p *metrics.Provider, foreign bool) float64 {
	t.Helper()
	v, _ := seriesValue(t, p, "mediacore_backup_restore_total", map[string]string{"stage": "rejected", "foreign": fmt.Sprint(foreign)})
	return v
}

// renameOver replaces to with a copy of from the way an attacker with write
// access to the backup directory would: a whole new file renamed into place.
func renameOver(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to+".swap", b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(to+".swap", to); err != nil {
		t.Fatal(err)
	}
}

// forgeWithID is the archive at src with its manifest's archive_id replaced by
// id: the same entries, so it extracts and opens as soundly as src does, but it
// names another archive. Its MAC no longer verifies, which makes it Foreign to
// every install, as src already was to the one it is restored into.
func forgeWithID(t *testing.T, src string, old, id backup.ArchiveID) []byte {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(raw, []byte(old)); n != 1 {
		t.Fatalf("fixture: the archive holds its ID %d times, want once (in the manifest)", n)
	}
	raw = bytes.Replace(raw, []byte(old), []byte(id), 1)
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// nothingStaged fails the test if e holds an intent or a staging entry.
func nothingStaged(t *testing.T, e *env) {
	t.Helper()
	if len(e.stager.staged) != 0 {
		t.Fatalf("an intent was created: %+v", e.stager.staged)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dbParent, ".mediacore-restore")); len(ents) != 0 {
		t.Fatalf("the refused restore left %d staging entries", len(ents))
	}
}

// TestStageRefusesAnOwnArchiveReplacedAfterTheCatalogueFoundIt: the catalogue
// finds the selected archive's file, then the file is replaced by another own
// archive (MAC valid, this install) before step 3 opens it again. Under a
// checksum-level, no-sidecar restore (AllowWeakVerify) step 2 reads no digest,
// so the manifest's own ID is the one thing that tells the two apart: without
// step 3's check the other archive's content stages under the selected ID.
func TestStageRefusesAnOwnArchiveReplacedAfterTheCatalogueFoundIt(t *testing.T) {
	ctx := asOwner(context.Background())
	e := newEnv(t)
	p, audits := observe(t, e)
	a := e.create("manual")
	if err := e.st.Keyspace(gothamOwner).Set(context.Background(), []byte("rogue/z"), []byte("two-face")); err != nil {
		t.Fatal(err)
	}
	b := e.create("manual")
	if err := os.Remove(e.path(a) + backup.ChecksumSidecar); err != nil {
		t.Fatal(err)
	}
	// The swap happens on the service's first clock read after it is armed:
	// the verification's record time, after the catalogue found a's path and
	// before step 3 opens it again.
	armed, swapped := false, false
	clock := e.opts.Clock
	e.opts.Clock = func() time.Time {
		if armed && !swapped {
			swapped = true
			renameOver(t, e.path(b), e.path(a))
		}
		return clock()
	}
	e.rebuild()
	armed = true
	opts := backup.RestoreOptions{Verify: backup.VerifyChecksum, AllowWeakVerify: true}
	_, err := e.svc.StageRestore(ctx, a.ID, opts)
	if !swapped {
		t.Fatal("fixture: the archive was not replaced")
	}
	if !errors.Is(err, backuparchive.ErrChanged) || !errors.Is(err, backup.ErrManifest) || !strings.Contains(err.Error(), "another archive") {
		t.Fatalf("an own archive replaced after the catalogue found it: err = %v, want step 3's ID refusal", err)
	}
	nothingStaged(t, e)
	if len(*audits) != 0 {
		t.Fatalf("a refused restore left audit records: %+v", *audits)
	}
	if got := rejected(t, p, false); got != 1 {
		t.Fatalf("rejected{foreign=false} = %v, want 1", got)
	}
	// The replacement is a sound own archive: under its own ID it stages.
	if _, err := e.svc.StageRestore(ctx, b.ID, opts); err != nil {
		t.Fatalf("the replacement archive under its own ID: %v", err)
	}
}

// TestStageRefusesAForeignArchiveReplacedByOneNamingTheSameID: a foreign
// archive's MAC proves nothing, so a replacement that names the selected ID
// passes step 3's ID check, and the archive digest step 2 vouched for is the
// only tie between what is staged and what was verified. The replacement is a
// whole, sound archive of another install, forged to carry the selected ID and
// renamed over the verified file right after the open-level verification read
// it, its sidecar replaced with it so a sidecar check alone would pass. With a
// sidecar the digest is the sidecar's; without one (AllowWeakVerify) it is the
// digest of the bytes the verification's entries pass read, so the binding
// holds on both paths.
func TestStageRefusesAForeignArchiveReplacedByOneNamingTheSameID(t *testing.T) {
	ctx := asOwner(context.Background())
	for _, sidecar := range []bool{true, false} {
		name := "sidecar"
		if !sidecar {
			name = "no-sidecar"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			p, audits := observe(t, e)
			f := madeElsewhere(t, e, nil)
			if f.Provenance != backup.Foreign {
				t.Fatalf("fixture: provenance = %v, want Foreign", f.Provenance)
			}
			other := newEnv(t)
			if err := other.st.Keyspace(gothamOwner).Set(context.Background(), []byte("rogue/z"), []byte("two-face")); err != nil {
				t.Fatal(err)
			}
			g := other.create("manual")
			forged := filepath.Join(t.TempDir(), "forged.tar.gz")
			if err := os.WriteFile(forged, forgeWithID(t, other.path(g), g.ID, f.ID), 0o600); err != nil {
				t.Fatal(err)
			}
			fb, err := os.ReadFile(forged)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(forged+backup.ChecksumSidecar, []byte(shaHex(fb)+"  "+f.Name+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			target := e.path(f)
			sw := &swapInspector{Inspector: e.opts.Inspector, t: t, from: []string{forged}, to: []string{target}}
			if sidecar {
				sw.from = append(sw.from, forged+backup.ChecksumSidecar)
				sw.to = append(sw.to, target+backup.ChecksumSidecar)
			} else if err := os.Remove(target + backup.ChecksumSidecar); err != nil {
				t.Fatal(err)
			}
			e.opts.Inspector = sw
			e.rebuild()
			opts := backup.RestoreOptions{Foreign: true, AllowWeakVerify: !sidecar}
			_, err = e.svc.StageRestore(ctx, f.ID, opts)
			if !errors.Is(err, backuparchive.ErrChanged) || !errors.Is(err, backup.ErrChecksumMismatch) || !strings.Contains(err.Error(), "not the one the verification accepted") {
				t.Fatalf("a foreign archive replaced by one naming its ID: err = %v, want staging's digest refusal", err)
			}
			nothingStaged(t, e)
			if len(*audits) != 0 {
				t.Fatalf("a refused restore left audit records: %+v", *audits)
			}
			if got := rejected(t, p, true); got != 1 {
				t.Fatalf("rejected{foreign=true} = %v, want 1", got)
			}
			// The forged archive restores on its own (its own manifest sidecar
			// in place of the one it replaced): the refusal was the binding, not
			// damage.
			if err := os.Remove(target + backup.ManifestSidecar); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			e.opts.Inspector = sw.Inspector
			e.rebuild()
			if _, err := e.svc.StageRestore(ctx, f.ID, opts); err != nil {
				t.Fatalf("the forged archive staged directly: %v", err)
			}
		})
	}
}

// TestRefusedRestoreCountsOnceAndLeavesNoBareStageRecord: once the archive is
// found, every refusal counts exactly once as rejected, and the
// core.restore.stage record exists only for a restore that reached the create:
// a refusal before it leaves no record, and a create that refuses after it
// leaves a second record with outcome "rejected" and a fixed reason code.
func TestRefusedRestoreCountsOnceAndLeavesNoBareStageRecord(t *testing.T) {
	ctx := asOwner(context.Background())
	for _, c := range []struct {
		name    string
		arrange func(t *testing.T, e *env) (backup.ArchiveID, backup.RestoreOptions)
		want    error
		foreign bool
		reason  string // the refused record's reason; "" = no record at all
	}{
		{
			name: "verification fails",
			arrange: func(t *testing.T, e *env) (backup.ArchiveID, backup.RestoreOptions) {
				a := e.create("manual")
				b, err := os.ReadFile(e.path(a))
				if err != nil {
					t.Fatal(err)
				}
				b[len(b)/2] ^= 0xff
				if err := os.WriteFile(e.path(a), b, 0o600); err != nil {
					t.Fatal(err)
				}
				return a.ID, backup.RestoreOptions{}
			},
			want: backup.ErrChecksumMismatch,
		},
		{
			name: "incompatible archive",
			arrange: func(t *testing.T, e *env) (backup.ArchiveID, backup.RestoreOptions) {
				a := madeElsewhere(t, e, func(o *env) { o.opts.AppID = "arkham"; o.rebuild() })
				return a.ID, backup.RestoreOptions{Foreign: true}
			},
			want: backup.ErrWrongApp, foreign: true,
		},
		{
			name: "legacy archive replaced after verification",
			arrange: func(t *testing.T, e *env) (backup.ArchiveID, backup.RestoreOptions) {
				fname := legacyArchive(t, e, "gzip", true)
				other := newEnv(t)
				if err := other.st.Keyspace(gothamOwner).Set(context.Background(), []byte("rogue/z"), []byte("two-face")); err != nil {
					t.Fatal(err)
				}
				legacyArchive(t, other, "gzip", true)
				e.opts.Inspector = &swapInspector{Inspector: e.opts.Inspector, t: t,
					from: []string{filepath.Join(other.backupDir, fname), filepath.Join(other.backupDir, fname+backup.ChecksumSidecar)},
					to:   []string{filepath.Join(e.backupDir, fname), filepath.Join(e.backupDir, fname+backup.ChecksumSidecar)}}
				e.rebuild()
				list, err := e.svc.List(ctx)
				if err != nil || len(list) != 1 {
					t.Fatalf("List = %+v, %v", list, err)
				}
				return list[0].ID, backup.RestoreOptions{Foreign: true}
			},
			// ErrChecksumMismatch, not only ErrChanged: the HTTP layer maps it to
			// 409 archive_verification_failed, where ErrChanged alone is a 500.
			want: backup.ErrChecksumMismatch, foreign: true,
		},
		{
			name: "unreadable secrets",
			arrange: func(t *testing.T, e *env) (backup.ArchiveID, backup.RestoreOptions) {
				a := e.create("manual")
				e.keys.referenced = []string{"hush-key"}
				return a.ID, backup.RestoreOptions{}
			},
			want: backup.ErrUnreadableSecrets,
		},
		{
			name: "the create refuses after the record",
			arrange: func(t *testing.T, e *env) (backup.ArchiveID, backup.RestoreOptions) {
				a := e.create("manual")
				e.stager.err = fmt.Errorf("%w: the slot was taken by another stager", restorejournal.ErrSwapPending)
				return a.ID, backup.RestoreOptions{}
			},
			want: restorejournal.ErrSwapPending, reason: "swap_pending",
		},
		{
			name: "the create fails after the record",
			arrange: func(t *testing.T, e *env) (backup.ArchiveID, backup.RestoreOptions) {
				a := e.create("manual")
				e.stager.err = errors.New("the batcave's disk is read-only")
				return a.ID, backup.RestoreOptions{}
			},
			reason: "stage_failed",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			p, audits := observe(t, e)
			id, opts := c.arrange(t, e)
			*audits = nil // the arrangement's own creates and imports record nothing here, but start clean
			_, err := e.svc.StageRestore(ctx, id, opts)
			if err == nil || (c.want != nil && !errors.Is(err, c.want)) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			nothingStaged(t, e)
			if got := rejected(t, p, c.foreign) + rejected(t, p, !c.foreign); got != 1 {
				t.Fatalf("rejected = %v in all, want exactly 1", got)
			}
			if got := rejected(t, p, c.foreign); got != 1 {
				t.Fatalf("rejected{foreign=%v} = %v, want 1", c.foreign, got)
			}
			if c.reason == "" {
				if len(*audits) != 0 {
					t.Fatalf("a restore refused before the create left audit records: %+v", *audits)
				}
				return
			}
			assertRefusedRecord(t, *audits, map[string]string{"foreign": "false", "archive": string(id)}, c.reason)
			for _, r := range *audits {
				for k, v := range r.fields {
					if strings.Contains(v, "read-only") || strings.Contains(v, "another stager") {
						t.Fatalf("audit field %s carries the error's text: %q", k, v)
					}
				}
			}
		})
	}
}

// assertRefusedRecord checks a stage record followed by its refusal: two
// core.restore.stage records, the first with base's fields only, the second
// with base's fields plus outcome "rejected" and reason.
func assertRefusedRecord(t *testing.T, recs []auditRec, base map[string]string, reason string) {
	t.Helper()
	if len(recs) != 2 || recs[0].action != "core.restore.stage" || recs[1].action != "core.restore.stage" {
		t.Fatalf("audit records = %+v, want the stage record and its refusal", recs)
	}
	if len(recs[0].fields) != len(base) {
		t.Fatalf("stage record = %v, want %v", recs[0].fields, base)
	}
	for k, v := range base {
		if recs[0].fields[k] != v || recs[1].fields[k] != v {
			t.Fatalf("records = %v / %v, want %s=%s in both", recs[0].fields, recs[1].fields, k, v)
		}
	}
	if recs[1].fields["outcome"] != "rejected" || recs[1].fields["reason"] != reason || len(recs[1].fields) != len(base)+2 {
		t.Fatalf("refusal record = %v, want outcome rejected and reason %s", recs[1].fields, reason)
	}
}

// TestRevertRefusedByTheCreateLeavesARejectedRecord: an undo whose create
// refuses after its core.restore.stage record was written records the refusal
// and counts it, as a staged restore does.
func TestRevertRefusedByTheCreateLeavesARejectedRecord(t *testing.T) {
	ctx := asOwner(context.Background())
	e := newEnv(t)
	p, audits := observe(t, e)
	revertFixture(t, e, "20261111T030000Z-dddddddd", "applied", true)
	e.stager.err = fmt.Errorf("%w: the slot was taken by another stager", restorejournal.ErrSwapPending)
	if _, err := e.svc.RevertLastRestore(ctx, backup.RestoreOptions{}); !errors.Is(err, restorejournal.ErrSwapPending) {
		t.Fatalf("err = %v, want ErrSwapPending", err)
	}
	assertRefusedRecord(t, *audits, map[string]string{"kind": "revert", "undo": "20261111T030000Z-dddddddd"}, "swap_pending")
	if got := rejected(t, p, false); got != 1 {
		t.Fatalf("rejected{foreign=false} = %v, want 1", got)
	}
}
