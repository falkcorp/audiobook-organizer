// file: core/secrets/derive_test.go
// version: 1.0.0
// guid: a7a8d056-deaf-4411-97d5-9ced80dc6d83
// last-edited: 2026-11-10

package secrets

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// window is the rotate-derive window of these tests.
const window = 7 * 24 * time.Hour

// rotate runs RotateDerive on e's keyring at e's clock, fatal on error.
func rotate(t *testing.T, e *env) RotateResult {
	t.Helper()
	r, err := RotateDerive(e.root, "", window, e.clock.now())
	if err != nil {
		t.Fatalf("RotateDerive: %v", err)
	}
	return r
}

// TestRotateDerive_VerifyThenRetire (05-config §2.9): after rotate-derive,
// DeriveKey uses d2; DeriveKeyFor("d1") works until retire_after and
// RetireUnused, then ErrKeyUnavailable; data records still decrypt; the
// export warning is raised. (Refusal while the store is locked is the
// kernel's: TestRotateDerive_RefusedWhileServed.)
func TestRotateDerive_VerifyThenRetire(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	s := e.open(t, nil)
	mustPut(t, s, nameGate, valueVengeance)
	d1, err := s.DeriveKey("core.auth/state/v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExportKeyring(e.root, "", filepath.Join(t.TempDir(), "keyring.export"), e.clock.now()); err != nil {
		t.Fatal(err)
	}
	if missing, err := s.UnexportedKeys(); err != nil || len(missing) != 0 {
		t.Fatalf("UnexportedKeys after the export = %v, %v", missing, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	r := rotate(t, e)
	if r.Old != "d1" || r.New != "d2" || !r.RetireAfter.Equal(e.clock.now().Add(window)) {
		t.Fatalf("RotateDerive = %+v", r)
	}
	f := e.readKeyringFile(t)
	if f.Keys[1].ID != "d1" || f.Keys[1].Status != statusVerify || f.Keys[1].RetireAfter == nil || f.Keys[2].ID != "d2" || f.Keys[2].Status != statusActive {
		t.Fatalf("keyring after the rotation: %v", f.Keys)
	}

	s = e.open(t, nil)
	if s.ActiveDeriveID() != "d2" {
		t.Fatalf("ActiveDeriveID = %q, want d2", s.ActiveDeriveID())
	}
	d2, err := s.DeriveKey("core.auth/state/v1")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(d1, d2) {
		t.Fatal("DeriveKey still uses d1")
	}
	old, err := s.DeriveKeyFor("d1", "core.auth/state/v1")
	if err != nil || !bytes.Equal(old, d1) {
		t.Fatalf("DeriveKeyFor(d1) = %v, want d1's key", err)
	}
	mustGet(t, s, nameGate, valueVengeance)
	if missing, err := s.UnexportedKeys(); err != nil || !slices.Contains(missing, "d2") {
		t.Fatalf("UnexportedKeys after the rotation = %v, %v; want d2 listed", missing, err)
	}

	never := func(context.Context, string) (bool, error) { return false, nil }
	if got, err := s.RetireUnused(ctx, never); err != nil || len(got) != 0 {
		t.Fatalf("RetireUnused before retire_after = %v, %v", got, err)
	}
	e.clock.advance(window + time.Minute)
	if _, err := s.DeriveKeyFor("d1", "core.auth/state/v1"); err != nil {
		t.Fatalf("a verify key past retire_after is unusable before RetireUnused ran: %v", err)
	}
	got, err := s.RetireUnused(ctx, never)
	if err != nil || !slices.Equal(got, []string{"d1"}) {
		t.Fatalf("RetireUnused past retire_after = %v, %v", got, err)
	}
	if _, err := s.DeriveKeyFor("d1", "core.auth/state/v1"); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("DeriveKeyFor(d1) after retirement = %v, want ErrKeyUnavailable", err)
	}
	if _, err := s.DeriveKeyFor("k1", "core.auth/state/v1"); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("DeriveKeyFor of a data key = %v, want ErrKeyUnavailable", err)
	}
	if _, err := s.DeriveKeyFor("d9", "core.auth/state/v1"); !errors.Is(err, ErrKeyUnavailable) {
		t.Fatalf("DeriveKeyFor of an unknown key = %v, want ErrKeyUnavailable", err)
	}
	mustGet(t, s, nameGate, valueVengeance)
	if f := e.readKeyringFile(t); f.Keys[1].Status != statusVerify {
		t.Fatal("RetireUnused wrote the keyring file")
	}

	// A second rotation adds d3 and keeps d1's verify entry.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if r := rotate(t, e); r.Old != "d2" || r.New != "d3" {
		t.Fatalf("second rotation = %+v", r)
	}
	if f := e.readKeyringFile(t); len(f.Keys) != 4 || f.Keys[1].Status != statusVerify || f.Keys[2].Status != statusVerify {
		t.Fatalf("keyring after two rotations: %d keys", len(f.Keys))
	}
}

// TestRotateDerive_NotRetiredWhileArchiveUsesIt (05-config §2.9): a verify
// key past retire_after stays verify (RetireBlocked lists it, health
// derive_key_retire_blocked) while the in-use check reports a retained
// archive, or fails; after the rewrap it retires.
func TestRotateDerive_NotRetiredWhileArchiveUsesIt(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.open(t, nil).Close()
	rotate(t, e)
	e.clock.advance(window + time.Hour)
	s := e.open(t, nil)

	archiveUses := true
	check := func(_ context.Context, kid string) (bool, error) { return archiveUses && kid == "d1", nil }
	if got, err := s.RetireUnused(ctx, check); err != nil || len(got) != 0 {
		t.Fatalf("RetireUnused with an archive under d1 = %v, %v", got, err)
	}
	if !slices.Equal(s.RetireBlocked(), []string{"d1"}) {
		t.Fatalf("RetireBlocked = %v, want d1", s.RetireBlocked())
	}
	if _, err := s.DeriveKeyFor("d1", "core.backup/mac/v1"); err != nil {
		t.Fatalf("a blocked verify key is unusable: %v", err)
	}

	failing := func(context.Context, string) (bool, error) {
		return false, errors.New("the batcave backup dir is unreachable")
	}
	if got, err := s.RetireUnused(ctx, failing); err != nil || len(got) != 0 {
		t.Fatalf("RetireUnused with a failing check = %v, %v; want the key kept and no error", got, err)
	}
	if !slices.Equal(s.RetireBlocked(), []string{"d1"}) {
		t.Fatal("a failing check did not block the key")
	}

	archiveUses = false
	if got, err := s.RetireUnused(ctx, check); err != nil || !slices.Equal(got, []string{"d1"}) {
		t.Fatalf("RetireUnused after the rewrap = %v, %v", got, err)
	}
	if len(s.RetireBlocked()) != 0 {
		t.Fatalf("RetireBlocked after the retirement = %v", s.RetireBlocked())
	}
	if _, err := s.RetireUnused(ctx, nil); err == nil {
		t.Fatal("a nil check was accepted")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.open(t, nil).RetireUnused(cctx, check); !errors.Is(err, context.Canceled) {
		t.Fatalf("RetireUnused with a cancelled context = %v", err)
	}
}

// TestRotateDerive_JournalStaysVerifiable (05-config §2.9): after the
// rotation and the rewrap, d1 stays verify while a retained revocation-journal
// line (or the header) names it; Open retires nothing and every boot decides
// again; RetireUnused asks the check the kernel built from the effective
// (overridden) core.backup.dir.
func TestRotateDerive_JournalStaysVerifiable(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.open(t, nil).Close()
	rotate(t, e)
	e.clock.advance(window + time.Hour)

	// The fake check: the journal under the overridden backup dir names d1.
	backupDir := "/srv/wayne-enterprises/backups"
	journal := map[string][]string{backupDir: {"d1"}}
	var asked []string
	check := func(_ context.Context, kid string) (bool, error) {
		asked = append(asked, kid)
		return slices.Contains(journal[backupDir], kid), nil
	}

	s := e.open(t, nil)
	if _, err := s.DeriveKeyFor("d1", "core.backup/journal/v1"); err != nil {
		t.Fatalf("Open retired d1: %v", err)
	}
	if got, err := s.RetireUnused(ctx, check); err != nil || len(got) != 0 {
		t.Fatalf("RetireUnused with the journal naming d1 = %v, %v", got, err)
	}
	if !slices.Equal(asked, []string{"d1"}) {
		t.Fatalf("the check was asked about %v, want only the due key d1", asked)
	}
	if _, err := s.DeriveKeyFor("d1", "core.backup/journal/v1"); err != nil {
		t.Fatalf("d1 is unusable while the journal names it: %v", err)
	}

	journal[backupDir] = nil
	if got, err := s.RetireUnused(ctx, check); err != nil || !slices.Equal(got, []string{"d1"}) {
		t.Fatalf("RetireUnused after retention dropped the line = %v, %v", got, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// The decision is in memory: the next boot starts with d1 usable.
	next := e.open(t, nil)
	if _, err := next.DeriveKeyFor("d1", "core.backup/journal/v1"); err != nil {
		t.Fatalf("a new Open kept the last process's retirement: %v", err)
	}
	if f := e.readKeyringFile(t); f.Keys[1].Status != statusVerify {
		t.Fatal("the retirement reached the keyring file")
	}
}

// TestRotateDerive_Refusals: no keyring, a negative window.
func TestRotateDerive_Refusals(t *testing.T) {
	e := newEnv(t)
	if _, err := RotateDerive(e.root, "", window, e.clock.now()); err == nil {
		t.Fatal("RotateDerive created or rotated a missing keyring")
	}
	e.open(t, nil).Close()
	if _, err := RotateDerive(e.root, "", -time.Second, e.clock.now()); err == nil {
		t.Fatal("RotateDerive accepted a negative window")
	}
	if _, err := RotateDerive(nil, "", window, e.clock.now()); err == nil {
		t.Fatal("RotateDerive accepted no state directory")
	}
}
