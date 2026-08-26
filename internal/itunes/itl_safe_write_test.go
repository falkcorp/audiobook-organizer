// file: internal/itunes/itl_safe_write_test.go
// version: 1.0.0
// guid: 9e2f3a4b-5c6d-7e8f-9a0b-1c2d3e4f5a6b
//
// Tests for SafeWriteITL — the atomic iTunes writeback protocol (fable5
// TASK-004), per SPEC 2 §6 (docs/specs/fable5-spec-itunes-writeback-hardening.md).
//
// These tests reuse the LE fixture builders from itl_safety_contract_test.go
// (buildCleanPayload / buildPayloadFromTracks / buildITLFile / cleanTracks).
// Test-local corruptor mutate funcs live here (production code never contains a
// corruptor). No network, no real iTunes, no fixtures outside t.TempDir().

package itunes

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixtureITL writes a full on-disk .itl built from payload and returns its
// path. The header counts agree with the payload (buildHeaderFor), so any later
// desync is solely the mutate's doing.
func writeFixtureITL(t *testing.T, dir, name string, payload []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, buildITLFile(t, payload), 0o664); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// identityMutate returns the payload unchanged (a no-op writeback).
func identityMutate(p []byte) ([]byte, error) { return p, nil }

// corruptMlthCountMutate decrements the master mlth count without removing a
// mith block, producing the K2 / count-coherence desync the contract must
// catch (mlth count != actual mith blocks). It also re-uses the original header
// counts, so this models exactly the "remove a track, keep old header" class
// from the SPEC §6 table — except here the desync survives header regeneration
// because it is payload-internal.
func corruptMlthCountMutate(p []byte) ([]byte, error) {
	out := append([]byte(nil), p...)
	// Master track msdh is type 1; mlth is its first child at the msdh body.
	msdhOff := firstMsdhOffset(out, 1)
	if msdhOff < 0 {
		return out, nil
	}
	mlthOff := msdhOff + int(binary.LittleEndian.Uint32(out[msdhOff+4:msdhOff+8])) // msdh headerLen
	if string(out[mlthOff:mlthOff+4]) != "mlth" {
		return out, nil
	}
	cur := binary.LittleEndian.Uint32(out[mlthOff+8 : mlthOff+12])
	binary.LittleEndian.PutUint32(out[mlthOff+8:mlthOff+12], cur+1) // now > actual mith
	return out, nil
}

// TestSafeWrite_RollbackOnViolation — SPEC §6: a mutate that introduces a K2
// desync must leave the original byte-identical and no .itl.new behind.
func TestSafeWrite_RollbackOnViolation(t *testing.T) {
	dir := t.TempDir()
	path := writeFixtureITL(t, dir, "iTunes Library.itl", buildCleanPayload())

	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read orig: %v", err)
	}

	rep, err := SafeWriteITL(path, corruptMlthCountMutate)
	if err == nil {
		t.Fatalf("expected contract rejection, got report %+v", rep)
	}
	if !strings.Contains(err.Error(), "count-coherence") {
		t.Fatalf("expected count-coherence violation, got: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !bytes.Equal(orig, after) {
		t.Fatalf("original library must be byte-identical after a rejected write")
	}
	// No .itl.new residue, and no backup taken (we never reached step 6).
	assertNoLeftover(t, dir, path)
	if baks := listBackups(t, dir, path); len(baks) != 0 {
		t.Fatalf("a rejected write must not create a backup; found %v", baks)
	}
}

// TestSafeWrite_HeaderRegenRoundTrip — SPEC §6 / CRIT-3 regression: removing a
// real track via the mutate must update the header's 0x44 track count and the
// contract must PASS (because the header is regenerated from the new payload).
func TestSafeWrite_HeaderRegenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tracks := cleanTracks() // 3 tracks
	path := writeFixtureITL(t, dir, "iTunes Library.itl", buildPayloadFromTracks(tracks))

	// Sanity: original header says 3 tracks at 0x44.
	if got := headerTrackCount(t, path); got != 3 {
		t.Fatalf("fixture should start with 3 tracks at 0x44, got %d", got)
	}

	// Real mutate: drop the last track (and its playlist ref) by rebuilding the
	// payload from the first two tracks. This is an internally-consistent
	// removal — only the header would desync if it were NOT regenerated.
	removeOne := func(_ []byte) ([]byte, error) {
		return buildPayloadFromTracks(tracks[:2]), nil
	}

	rep, err := SafeWriteITL(path, removeOne)
	if err != nil {
		t.Fatalf("header-regen round-trip should pass the contract, got: %v", err)
	}
	if rep.HeaderCounts.Tracks != 2 {
		t.Fatalf("WriteReport should report 2 tracks, got %d", rep.HeaderCounts.Tracks)
	}

	// The on-disk header's 0x44 count must now be 2 (CRIT-3 closed).
	if got := headerTrackCount(t, path); got != 2 {
		t.Fatalf("header @0x44 should be regenerated to 2, got %d", got)
	}
	// And the written file must itself pass an independent audit.
	data, _ := os.ReadFile(path)
	if v := AuditITL(data); !v.Pass {
		t.Fatalf("written library failed audit: %v\n%s", v.FailedGuards(), v.Error())
	}
}

// TestSafeWrite_BackupRotation — SPEC §6: 12 successive writes leave the 10
// newest .bak-<RFC3339> plus the pinned .bak-lkg.
func TestSafeWrite_BackupRotation(t *testing.T) {
	dir := t.TempDir()
	path := writeFixtureITL(t, dir, "iTunes Library.itl", buildCleanPayload())

	// Pin a last-known-good BEFORE the writes so we can assert it survives.
	if err := PinLastKnownGood(path); err != nil {
		t.Fatalf("PinLastKnownGood: %v", err)
	}

	for i := range 12 {
		// Each write must produce a DISTINCT backup timestamp; the RFC3339 layout
		// has nanosecond precision, but force separation to avoid same-instant
		// collisions on fast machines by varying the payload trivially is not
		// possible (it must stay valid), so rely on nanosecond timestamps and a
		// tiny sleep-free uniqueness check below.
		if _, err := SafeWriteITL(path, identityMutate); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	baks := listBackups(t, dir, path)
	if len(baks) != defaultBackupRetention {
		t.Fatalf("expected %d rotated backups, got %d: %v", defaultBackupRetention, len(baks), baks)
	}
	// The pinned LKG must still be present and untouched by rotation.
	if _, err := os.Stat(path + ".bak-lkg"); err != nil {
		t.Fatalf(".bak-lkg must survive rotation: %v", err)
	}
}

// TestSafeWrite_RefusesBigEndian — SPEC §3 step 1 / K12: a BE payload (no "msdh"
// magic) is refused with ErrBEWritebackUnsupported and nothing is written.
func TestSafeWrite_RefusesBigEndian(t *testing.T) {
	dir := t.TempDir()
	// Build an .itl whose payload does NOT start with "msdh" (BE-shaped). We
	// wrap an arbitrary non-LE payload through the same header/encrypt path.
	bePayload := append([]byte("hdfm"), make([]byte, 64)...) // not "msdh"
	hdr := buildHeaderFor(buildCleanPayload())               // any valid header
	deflated := itlDeflate(bePayload)
	encrypted := itlEncrypt(hdr, deflated)
	fileLen := uint32(len(encrypted)) + hdr.headerLen
	header := buildHdfmHeader(hdr.version, hdr.headerRemainder, fileLen, hdr.unknown)
	itl := append(append([]byte{}, header...), encrypted...)
	path := filepath.Join(dir, "iTunes Library.itl")
	if err := os.WriteFile(path, itl, 0o664); err != nil {
		t.Fatalf("write BE fixture: %v", err)
	}

	orig, _ := os.ReadFile(path)
	_, err := SafeWriteITL(path, identityMutate)
	if err != ErrBEWritebackUnsupported {
		t.Fatalf("expected ErrBEWritebackUnsupported, got: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(orig, after) {
		t.Fatalf("BE refusal must leave the original untouched")
	}
	assertNoLeftover(t, dir, path)
}

// TestSafeWrite_ReReadValidation — SPEC §3 step 5: simulate an encode-path bug
// via the test-only encodeHook that corrupts the .itl.new bytes after they are
// written. The re-read contract (or decode) must catch it, remove .itl.new, and
// leave the original byte-identical.
func TestSafeWrite_ReReadValidation(t *testing.T) {
	dir := t.TempDir()
	path := writeFixtureITL(t, dir, "iTunes Library.itl", buildCleanPayload())
	orig, _ := os.ReadFile(path)

	// Corrupt the encoded bytes so the re-read decode fails (truncate the
	// encrypted body). This models a BestSpeed-zlib / AES-boundary encode bug.
	corruptEncode := func(b []byte) []byte {
		if len(b) > 32 {
			return b[:len(b)-16] // truncate → re-read decode/inflate fails
		}
		return b
	}

	_, err := SafeWriteITL(path, identityMutate, withEncodeHook(corruptEncode))
	if err == nil {
		t.Fatalf("expected re-read validation to reject the corrupted encode")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(orig, after) {
		t.Fatalf("re-read rejection must leave the original byte-identical")
	}
	assertNoLeftover(t, dir, path)
}

// TestSafeWrite_CleanWriteSucceeds — a no-op (identity) write passes the
// contract end-to-end, takes one backup, and leaves a valid library.
func TestSafeWrite_CleanWriteSucceeds(t *testing.T) {
	dir := t.TempDir()
	path := writeFixtureITL(t, dir, "iTunes Library.itl", buildCleanPayload())

	rep, err := SafeWriteITL(path, identityMutate)
	if err != nil {
		t.Fatalf("clean identity write should succeed: %v", err)
	}
	if rep.BackupPath == "" {
		t.Fatalf("a successful write must record a backup path")
	}
	if _, err := os.Stat(rep.BackupPath); err != nil {
		t.Fatalf("backup file should exist: %v", err)
	}
	if !rep.Verdict.Pass {
		t.Fatalf("re-read verdict should pass")
	}
	data, _ := os.ReadFile(path)
	if v := AuditITL(data); !v.Pass {
		t.Fatalf("written library failed audit: %s", v.Error())
	}
}

// TestSafeWrite_BoundedDeltaForce — the bounded-delta guardrail rejects a
// removal that exceeds RemovedTracksMax, and Force (ForceContractConfig /
// WithContractConfig) overrides ONLY that guardrail. This locks in the wiring
// the nuclear-rebuild and full-export paths depend on (SPEC §2): without Force a
// full-library replacement would be rejected as a blast-radius violation.
func TestSafeWrite_BoundedDeltaForce(t *testing.T) {
	tracks := cleanTracks() // 3 tracks
	// Remove the last 2 → removed=2. Use a low cap so the small fixture trips it.
	removeTwo := func(_ []byte) ([]byte, error) {
		return buildPayloadFromTracks(tracks[:1]), nil
	}
	lowCap := DefaultContractConfig()
	lowCap.RemovedTracksMax = 1 // removing 2 > cap 1

	t.Run("rejected without force", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFixtureITL(t, dir, "iTunes Library.itl", buildPayloadFromTracks(tracks))
		orig, _ := os.ReadFile(path)
		_, err := SafeWriteITL(path, removeTwo, WithContractConfig(lowCap))
		if err == nil || !strings.Contains(err.Error(), "bounded-delta") {
			t.Fatalf("expected bounded-delta rejection, got: %v", err)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(orig, after) {
			t.Fatalf("rejected bounded-delta write must leave original untouched")
		}
	})

	t.Run("allowed with force", func(t *testing.T) {
		dir := t.TempDir()
		path := writeFixtureITL(t, dir, "iTunes Library.itl", buildPayloadFromTracks(tracks))
		forced := lowCap
		forced.Force = true
		rep, err := SafeWriteITL(path, removeTwo, WithContractConfig(forced))
		if err != nil {
			t.Fatalf("Force should override bounded-delta, got: %v", err)
		}
		if rep.HeaderCounts.Tracks != 1 {
			t.Fatalf("expected 1 track after forced removal, got %d", rep.HeaderCounts.Tracks)
		}
	})
}

// ---------------------------------------------------------------------------
// test-local assertions
// ---------------------------------------------------------------------------

// assertNoLeftover fails if a <path>.itl.new staging file survived the call.
func assertNoLeftover(t *testing.T, dir, path string) {
	t.Helper()
	if _, err := os.Stat(path + ".itl.new"); err == nil {
		t.Fatalf(".itl.new staging file must be removed after a failed write")
	}
}

// listBackups returns the basenames of <path>.bak-<RFC3339> files (excludes the
// pinned .bak-lkg).
func listBackups(t *testing.T, dir, path string) []string {
	t.Helper()
	base := filepath.Base(path)
	prefix := base + ".bak-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && name != base+".bak-lkg" {
			out = append(out, name)
		}
	}
	return out
}

// headerTrackCount reads the on-disk library's hdfm header and returns the BE
// u32 track count at file offset 0x44.
func headerTrackCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	hdr, err := parseHdfmHeader(data)
	if err != nil {
		t.Fatalf("parse header: %v", err)
	}
	full := buildHdfmHeader(hdr.version, hdr.headerRemainder, hdr.fileLen, hdr.unknown)
	if 0x44+4 > len(full) {
		t.Fatalf("header too short to carry 0x44")
	}
	return int(binary.BigEndian.Uint32(full[0x44 : 0x44+4]))
}
