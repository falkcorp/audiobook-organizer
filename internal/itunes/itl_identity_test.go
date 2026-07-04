// file: internal/itunes/itl_identity_test.go
// version: 1.0.0
// guid: 8b3c4d5e-6f7a-4b8c-9d0e-2f3a4b5c6d7e
// last-edited: 2026-07-03
//
// Tests for the library-identity fingerprint (K13) and expected-magnitude
// (K14) guards — the external-truth anchors added after the July 2026
// "374-track cloud stub" demonstrated that a completely different library
// (same path, same Library PID, zero track-PID overlap, 0.4% of the tracks)
// passes all eight structural guards.
//
// Reuses the LE fixture builders from itl_safety_contract_test.go
// (buildPayloadFromTracks / cleanTracks / buildHeaderFor / buildITLFile).

package itunes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// headerWithLibraryPID returns a fixture header with the 8-byte Library PID
// planted at file offset 0x34.
func headerWithLibraryPID(payload []byte, pid [8]byte) *hdfmHeader {
	hdr := buildHeaderFor(payload)
	relOff := libraryPIDFileOffset - (headerFixedPrefix + len(hdr.version))
	copy(hdr.headerRemainder[relOff:relOff+8], pid[:])
	return hdr
}

var fxLibraryPID = [8]byte{0x48, 0xE8, 0x7F, 0x59, 0x86, 0x55, 0x68, 0xB0}

const fxLibraryPIDHex = "48e87f59865568b0"

// disjointTracks returns a track population sharing no PIDs with cleanTracks()
// (PIDs are derived from TIDs in buildMith, so distinct TIDs => distinct PIDs).
func disjointTracks(n int) []fxTrack {
	tracks := make([]fxTrack, 0, n)
	for i := 0; i < n; i++ {
		tracks = append(tracks, fxTrack{
			tid:      uint32(1000 + i*10),
			name:     fmt.Sprintf("Cloud Song %d", i),
			location: fmt.Sprintf(`W:\itunes\Media\Music\Cloud\%02d.mp3`, i),
		})
	}
	return tracks
}

func TestExtractLibraryPIDHex(t *testing.T) {
	payload := buildCleanPayload()

	if got := ExtractLibraryPIDHex(headerWithLibraryPID(payload, fxLibraryPID)); got != fxLibraryPIDHex {
		t.Fatalf("ExtractLibraryPIDHex = %q, want %q", got, fxLibraryPIDHex)
	}
	// All-zero PID (fixture default) must read as "not present", not a value
	// that could spuriously match another zeroed header.
	if got := ExtractLibraryPIDHex(buildHeaderFor(payload)); got != "" {
		t.Fatalf("zero PID: got %q, want \"\"", got)
	}
	if got := ExtractLibraryPIDHex(nil); got != "" {
		t.Fatalf("nil header: got %q, want \"\"", got)
	}
}

func TestComputeLibraryIdentity(t *testing.T) {
	payload := buildCleanPayload()
	hdr := headerWithLibraryPID(payload, fxLibraryPID)

	id, err := ComputeLibraryIdentity(payload, hdr)
	if err != nil {
		t.Fatalf("ComputeLibraryIdentity: %v", err)
	}
	if id.LibraryPID != fxLibraryPIDHex {
		t.Errorf("LibraryPID = %q, want %q", id.LibraryPID, fxLibraryPIDHex)
	}
	if id.TrackCount != 3 || id.PlaylistCount != 1 {
		t.Errorf("counts = %d tracks / %d playlists, want 3/1", id.TrackCount, id.PlaylistCount)
	}
	if id.SampleStride != 1 || len(id.PIDSample) != 3 {
		t.Errorf("sample = %d PIDs stride %d, want 3 stride 1", len(id.PIDSample), id.SampleStride)
	}

	// A payload with no locatable master list must be an error, never a
	// degenerate identity future writes would "match".
	if _, err := ComputeLibraryIdentity([]byte("garbage"), hdr); err == nil {
		t.Error("garbage payload: expected error, got nil")
	}
}

func TestSampleOverlapPct(t *testing.T) {
	payload := buildCleanPayload()
	id, err := ComputeLibraryIdentity(payload, buildHeaderFor(payload))
	if err != nil {
		t.Fatalf("ComputeLibraryIdentity: %v", err)
	}

	if pct := id.SampleOverlapPct(payload); pct != 100 {
		t.Errorf("self overlap = %d%%, want 100", pct)
	}
	stub := buildPayloadFromTracks(disjointTracks(5))
	if pct := id.SampleOverlapPct(stub); pct != 0 {
		t.Errorf("disjoint overlap = %d%%, want 0", pct)
	}
	empty := &LibraryIdentity{}
	if pct := empty.SampleOverlapPct(payload); pct != -1 {
		t.Errorf("empty sample = %d, want -1 (unassessable)", pct)
	}
}

func TestGuardLibraryIdentity(t *testing.T) {
	golden := buildCleanPayload()
	goldenHdr := headerWithLibraryPID(golden, fxLibraryPID)
	goldenID, err := ComputeLibraryIdentity(golden, goldenHdr)
	if err != nil {
		t.Fatalf("ComputeLibraryIdentity: %v", err)
	}
	// The July 2026 stub scenario: same Library PID, disjoint track PIDs.
	stub := buildPayloadFromTracks(disjointTracks(5))
	stubHdr := headerWithLibraryPID(stub, fxLibraryPID)

	cases := []struct {
		name     string
		after    []byte
		hdr      *hdfmHeader
		cfg      ContractConfig
		wantPass bool
		wantMsg  string
	}{
		{"disarmed without expectation", stub, stubHdr, ContractConfig{}, true, ""},
		{"adopt bypasses", stub, stubHdr, ContractConfig{ExpectedIdentity: goldenID, AdoptLibrary: true}, true, ""},
		{"continuation passes", golden, goldenHdr, ContractConfig{ExpectedIdentity: goldenID}, true, ""},
		{"population replaced fails", stub, stubHdr, ContractConfig{ExpectedIdentity: goldenID}, false, "library population replaced"},
		{"library PID change fails", golden, headerWithLibraryPID(golden, [8]byte{1, 2, 3, 4, 5, 6, 7, 8}), ContractConfig{ExpectedIdentity: goldenID}, false, "library PID changed"},
		{"empty fingerprint fails closed", golden, goldenHdr, ContractConfig{ExpectedIdentity: &LibraryIdentity{LibraryPID: fxLibraryPIDHex}}, false, "unassessable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := guardLibraryIdentity(nil, tc.after, tc.hdr, normalizeConfig(tc.cfg))
			if res.Pass() != tc.wantPass {
				t.Fatalf("pass = %v, want %v (violations: %+v)", res.Pass(), tc.wantPass, res.Violations)
			}
			if !tc.wantPass && !strings.Contains(res.Violations[0].Message, tc.wantMsg) {
				t.Fatalf("violation %q does not contain %q", res.Violations[0].Message, tc.wantMsg)
			}
		})
	}
}

func TestGuardExpectedMagnitude(t *testing.T) {
	payload := buildCleanPayload() // 3 tracks

	cases := []struct {
		name     string
		cfg      ContractConfig
		wantPass bool
	}{
		{"disarmed when unset", ContractConfig{}, true},
		{"exact match", ContractConfig{ExpectedTrackCount: 3}, true},
		{"within tolerance", ContractConfig{ExpectedTrackCount: 3, MagnitudeTolerancePct: 40}, true},
		// The stub scenario at real scale: expected ~90900, got a fraction.
		{"catastrophic shrink", ContractConfig{ExpectedTrackCount: 90900}, false},
		{"unexpected growth", ContractConfig{ExpectedTrackCount: 2}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := guardExpectedMagnitude(nil, payload, nil, normalizeConfig(tc.cfg))
			if res.Pass() != tc.wantPass {
				t.Fatalf("pass = %v, want %v (violations: %+v)", res.Pass(), tc.wantPass, res.Violations)
			}
		})
	}
}

func TestIdentitySidecarRoundtrip(t *testing.T) {
	dir := t.TempDir()
	libPath := filepath.Join(dir, "iTunes Library.itl")

	// Missing sidecar is a legitimate first-run state.
	if id, err := LoadLibraryIdentity(libPath); id != nil || err != nil {
		t.Fatalf("missing sidecar: got (%+v, %v), want (nil, nil)", id, err)
	}

	payload := buildCleanPayload()
	id, err := ComputeLibraryIdentity(payload, headerWithLibraryPID(payload, fxLibraryPID))
	if err != nil {
		t.Fatalf("ComputeLibraryIdentity: %v", err)
	}
	if err := SaveLibraryIdentity(libPath, id); err != nil {
		t.Fatalf("SaveLibraryIdentity: %v", err)
	}
	got, err := LoadLibraryIdentity(libPath)
	if err != nil {
		t.Fatalf("LoadLibraryIdentity: %v", err)
	}
	if got.LibraryPID != id.LibraryPID || got.TrackCount != id.TrackCount || len(got.PIDSample) != len(id.PIDSample) {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", got, id)
	}

	// A corrupt sidecar must fail closed, not read as "no anchor".
	if err := os.WriteFile(IdentitySidecarPath(libPath), []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLibraryIdentity(libPath); err == nil {
		t.Fatal("corrupt sidecar: expected error, got nil")
	}
}

// TestChecksumContinuity (K17): a successful write records the SHA-256 of the
// exact on-disk bytes; a subsequent external modification is detectable.
func TestChecksumContinuity(t *testing.T) {
	dir := t.TempDir()
	path := writeFixtureITL(t, dir, "iTunes Library.itl", buildCleanPayload())

	if _, err := SafeWriteITL(path, identityMutate); err != nil {
		t.Fatalf("write: %v", err)
	}
	id, err := LoadLibraryIdentity(path)
	if err != nil || id == nil {
		t.Fatalf("sidecar: %v", err)
	}
	if id.FileSHA256 == "" {
		t.Fatal("FileSHA256 not recorded after successful write")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !id.MatchesFileSHA(onDisk) {
		t.Fatal("recorded checksum must match the bytes on disk")
	}
	// External writer (iTunes) modifies the file → continuity broken.
	if id.MatchesFileSHA(append(append([]byte(nil), onDisk...), 0x00)) {
		t.Fatal("modified bytes must not match the recorded checksum")
	}
	// No recorded checksum → nothing to contradict.
	if !(&LibraryIdentity{}).MatchesFileSHA(onDisk) {
		t.Fatal("empty checksum must match (no anchor)")
	}
}

// TestApplyOps_MagnitudeArming (K14/K17): ApplyITLOperations derives
// ExpectedTrackCount from the sidecar's validated count plus the op delta, so
// a library whose real size is far from the sidecar projection is rejected.
func TestApplyOps_MagnitudeArming(t *testing.T) {
	dir := t.TempDir()
	path := writeFixtureITL(t, dir, "iTunes Library.itl", buildCleanPayload()) // 3 tracks

	payload := buildCleanPayload()
	id, err := ComputeLibraryIdentity(payload, buildHeaderFor(payload))
	if err != nil {
		t.Fatal(err)
	}
	// Sidecar claims the validated state had 100 tracks; the file has 3.
	id.TrackCount = 100
	if err := SaveLibraryIdentity(path, id); err != nil {
		t.Fatal(err)
	}

	ops := ITLOperationSet{MetadataUpdates: []ITLMetadataUpdate{{
		PersistentID: "100000000000000a", Name: "Renamed", Album: "Renamed", Artist: "A", Genre: "Audiobook",
	}}}
	// One metadata update rewrites 50% of the tiny fixture's mhoh blocks, so
	// relax the (unrelated) rewrite cap to isolate expected-magnitude.
	cfg := DefaultContractConfig()
	cfg.RewrittenMhohPctMax = 100
	_, err = ApplyITLOperations(path, path, ops, cfg)
	if err == nil || !strings.Contains(err.Error(), "expected-magnitude") {
		t.Fatalf("expected expected-magnitude rejection, got: %v", err)
	}

	// With an accurate sidecar count the same op succeeds.
	id.TrackCount = 3
	if err := SaveLibraryIdentity(path, id); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyITLOperations(path, path, ops, cfg); err != nil {
		t.Fatalf("accurate projection must pass: %v", err)
	}
}

// TestSafeWrite_IdentityLifecycle is the end-to-end K13 scenario: the first
// write fingerprints the library; swapping in a disjoint population is then
// rejected; AdoptLibrary blesses the swap and re-anchors to it.
func TestSafeWrite_IdentityLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := writeFixtureITL(t, dir, "iTunes Library.itl", buildCleanPayload())

	// Write 1 (no sidecar yet): guard disarmed, sidecar created on success.
	if _, err := SafeWriteITL(path, identityMutate); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := os.Stat(IdentitySidecarPath(path)); err != nil {
		t.Fatalf("sidecar not created: %v", err)
	}

	// Write 2: a mutate that replaces the entire population (the stub class)
	// must be rejected by library-identity, leaving the file byte-identical.
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replaceMutate := func(_ []byte) ([]byte, error) {
		return buildPayloadFromTracks(disjointTracks(5)), nil
	}
	_, err = SafeWriteITL(path, replaceMutate)
	if err == nil || !strings.Contains(err.Error(), "library-identity") {
		t.Fatalf("expected library-identity rejection, got: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != string(after) {
		t.Fatal("rejected write modified the library")
	}

	// Write 3: the same replacement under AdoptLibrary is allowed, and the
	// sidecar re-anchors to the new population...
	adoptCfg := DefaultContractConfig()
	adoptCfg.AdoptLibrary = true
	adoptCfg.Force = true // replacement also trips bounded-delta's rewrite cap
	if _, err := SafeWriteITL(path, replaceMutate, WithContractConfig(adoptCfg)); err != nil {
		t.Fatalf("adopt write: %v", err)
	}
	// ...so a no-op write against the adopted library now passes.
	if _, err := SafeWriteITL(path, identityMutate); err != nil {
		t.Fatalf("post-adopt write: %v", err)
	}
}
