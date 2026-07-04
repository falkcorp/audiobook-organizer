// file: internal/itunes/itl_safe_write.go
// version: 1.1.0
// guid: 7c1d2e3f-4a5b-6c7d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-07-03
//
// SafeWriteITL — the single atomic iTunes-library writeback chokepoint
// (fable5 TASK-004). Implements SPEC 2 §3 (the 8-step atomic write protocol,
// NORMATIVE — docs/specs/fable5-spec-itunes-writeback-hardening.md) and closes
// CRIT-3 (docs/specs/fable5-review-findings.md) by REGENERATING the hdfm header
// count fields from the actual mutated payload on every write.
//
// WHY this exists (CRIT-3): before this file, writeback went straight from
// mutation to writeITLFile, which reuses the original hdr.headerRemainder
// verbatim. RemoveTracksByPIDLE updates mlth/miph/msdh counts in the PAYLOAD but
// nothing touched the unencrypted hdfm header's BE count u32s at file offsets
// 0x44 (tracks), 0x48 (playlists), 0x4C (albums), 0x54 (artists). The result was
// damaged-1/damaged-2: header says 90,900 tracks, payload has 90,898 — iTunes
// renames the library "(Damaged)". regenerateHeaderCounts patches exactly those
// four u32s from the post-mutation payload counts, preserving every other
// remainder byte, so the desync is impossible by construction.
//
// WHY the contract runs twice (SPEC §3 steps 3 + 5): once on the in-memory
// `after` payload (catch the mutation), once on the bytes RE-READ from
// `.itl.new` after encode+encrypt+deflate (catch encode-path bugs — the
// BestSpeed zlib + AES-ECB boundary code is a historic risk area).
//
// All structural writeback entry points in this package route through here:
// ApplyITLOperations(+InMemory), UpdateITLLocations, InsertITLTracks,
// RewriteITLExtensions, InsertITLPlaylist, and rebuild (via ApplyITLOperations).
// WriteITLBytes stays for cmd/ tools only.

package itunes

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WriteReport summarizes a completed SafeWriteITL call for the caller / audit log.
type WriteReport struct {
	// Path is the library that was written in place.
	Path string
	// BackupPath is the <path>.bak-<RFC3339> created before the atomic rename
	// (empty if the target did not previously exist — first-ever write).
	BackupPath string
	// BytesWritten is the size of the final on-disk library.
	BytesWritten int
	// HeaderCounts are the regenerated header count fields actually written
	// (tracks/playlists/albums/artists at file offsets 0x44/0x48/0x4C/0x54).
	HeaderCounts HeaderCounts
	// Verdict is the passing contract verdict for the re-read bytes (step 5).
	Verdict ContractVerdict
}

// HeaderCounts holds the four BE u32 count fields regenerated into the hdfm
// header on every write (SPEC §3 step 2 / CRIT-3).
type HeaderCounts struct {
	Tracks    int
	Playlists int
	Albums    int
	Artists   int
}

// header file offsets of the BE u32 count fields (CRIT-3). These are absolute
// file offsets; the remainder-relative offset is (fileOff - headerPrefixLen),
// where headerPrefixLen = 17 + len(version) (see parseHdfmHeader layout).
const (
	hdrOffTracks    = 0x44
	hdrOffPlaylists = 0x48
	hdrOffAlbums    = 0x4C
	hdrOffArtists   = 0x54
	// headerFixedPrefix is the bytes before the version string: "hdfm"(4) +
	// headerLen(4) + fileLen(4) + unknown(4) + verLen(1) = 17. The remainder
	// (which carries the count fields) begins at 17 + len(version).
	headerFixedPrefix = 17
)

// defaultBackupRetention is the SPEC §3 retention: keep the 10 newest unpinned
// .bak-<RFC3339> copies; .bak-lkg is pinned separately and never rotated.
const defaultBackupRetention = 10

// backupTimeLayout is the timestamp form used for .bak-<RFC3339> files. RFC3339
// uses ':' which is filesystem-legal on the Linux/ZFS production target; we use
// a colon-free RFC3339-equivalent so the names are also portable for tests on
// case-insensitive / colon-hostile filesystems.
const backupTimeLayout = "2006-01-02T15-04-05.000000000Z07-00"

// safeWriteOptions is the resolved option set for one SafeWriteITL call.
type safeWriteOptions struct {
	libraryNotInUse func() error
	contractCfg     ContractConfig
	backupRetention int
	// encodeHook, when non-nil, is invoked on the freshly-encoded .itl.new bytes
	// before they are re-read for the step-5 contract. Tests use it to simulate
	// an encode-path bug so the re-read-validation branch is exercised. It is
	// nil in all production paths.
	encodeHook func([]byte) []byte
}

// SafeWriteOption configures a SafeWriteITL call.
type SafeWriteOption func(*safeWriteOptions)

// WithLibraryNotInUse injects the precondition that gates writes when iTunes /
// Apple Devices may have the library open (SPEC §3 step 8 / K-class "(Damaged)"
// inference). The service wires its iTunes-running heartbeat here in TASK-008.
// Default: a no-op that logs a WARN, so cmd/ tools keep working.
func WithLibraryNotInUse(check func() error) SafeWriteOption {
	return func(o *safeWriteOptions) { o.libraryNotInUse = check }
}

// WithContractConfig overrides the ITLSafetyContract guardrail config (e.g. to
// set Force for an operator-acknowledged bulk removal).
func WithContractConfig(cfg ContractConfig) SafeWriteOption {
	return func(o *safeWriteOptions) { o.contractCfg = cfg }
}

// WithBackupRetention overrides how many unpinned .bak-* copies are kept.
func WithBackupRetention(n int) SafeWriteOption {
	return func(o *safeWriteOptions) {
		if n > 0 {
			o.backupRetention = n
		}
	}
}

// withEncodeHook (unexported) is for tests only — see safeWriteOptions.encodeHook.
func withEncodeHook(h func([]byte) []byte) SafeWriteOption {
	return func(o *safeWriteOptions) { o.encodeHook = h }
}

// ForceContractConfig returns the default contract config with Force enabled.
// Force ONLY overrides the bounded-delta blast-radius guardrail (SPEC §2) — it
// never disables a structural guard. The nuclear rebuild (RebuildITLFromDB) and
// full-export (BuildExportITL) paths use it because they intentionally remove
// every existing track, which is exactly the bounded-delta case the spec makes
// force-overridable.
func ForceContractConfig() ContractConfig {
	cfg := DefaultContractConfig()
	cfg.Force = true
	return cfg
}

// contractCfgOrDefault returns the first config from a variadic slice, or the
// SPEC default when none is supplied (the bounded single-book writeback path).
func contractCfgOrDefault(cfg []ContractConfig) ContractConfig {
	if len(cfg) > 0 {
		return cfg[0]
	}
	return DefaultContractConfig()
}

func resolveOptions(opts ...SafeWriteOption) safeWriteOptions {
	o := safeWriteOptions{
		libraryNotInUse: nil,
		contractCfg:     DefaultContractConfig(),
		backupRetention: defaultBackupRetention,
	}
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// SafeWriteITL atomically writes a mutation of the iTunes library at `path` in
// place, implementing SPEC 2 §3 steps 1–8. `mutate` receives the decompressed
// little-endian `before` payload and returns the proposed `after` payload; it
// must not retain or mutate the slice it is given beyond returning the result.
//
// Guarantees:
//   - The contract (T003) passes on BOTH the in-memory `after` and the bytes
//     re-read from disk, or NOTHING is written.
//   - The original `path` is byte-identical if any step fails.
//   - The header count fields are regenerated from the mutated payload (CRIT-3).
//   - On success the previous library is preserved as <path>.bak-<RFC3339>.
func SafeWriteITL(path string, mutate func(before []byte) (after []byte, err error), opts ...SafeWriteOption) (*WriteReport, error) {
	o := resolveOptions(opts...)

	// Step 8 (precondition): refuse to write if the library may be open in iTunes.
	if o.libraryNotInUse != nil {
		if err := o.libraryNotInUse(); err != nil {
			slog.Error("SafeWriteITL refused: library may be in use", "path", path, "err", err)
			return nil, fmt.Errorf("library-not-in-use precondition failed: %w", err)
		}
	} else {
		// Default no-op with WARN so cmd tools keep working but the missing
		// safety signal is visible (SPEC §3 step 8).
		slog.Warn("SafeWriteITL: no library-not-in-use check wired; writing without it", "path", path)
	}

	// Step 1: read original; parse; snapshot before payload + header. Refuse BE.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("SafeWriteITL: reading %s: %w", path, err)
	}
	hdr, before, err := decodeITLForContract(raw)
	if err != nil {
		return nil, fmt.Errorf("SafeWriteITL: decoding %s: %w", path, err)
	}
	_, wasCompressed, err := itlInflate(itlDecrypt(hdr, raw[hdr.headerLen:]))
	if err != nil {
		return nil, fmt.Errorf("SafeWriteITL: detecting compression: %w", err)
	}

	// K13: arm the library-identity guard from the .identity.json sidecar
	// unless the caller supplied its own expectation or is explicitly adopting
	// a new library. A missing sidecar (first run) leaves the guard disarmed;
	// a corrupt/unreadable sidecar fails closed.
	if o.contractCfg.ExpectedIdentity == nil && !o.contractCfg.AdoptLibrary {
		sidecarID, idErr := LoadLibraryIdentity(path)
		if idErr != nil {
			return nil, fmt.Errorf("SafeWriteITL: identity sidecar for %s: %w", path, idErr)
		}
		o.contractCfg.ExpectedIdentity = sidecarID
	}

	// safeEncodeITL does steps 1(parse done) → 3: BE refusal, mutate, header
	// regeneration, in-memory contract. It returns the final on-disk bytes. The
	// in-memory verdict is already known-passing here; the authoritative verdict
	// returned to the caller is the step-5 re-read verdict computed below.
	outBytes, _, counts, _, err := safeEncodeITLFromParsed(hdr, before, wasCompressed, mutate, o.contractCfg)
	if err != nil {
		return nil, err
	}

	// Step 4: write to <path>.itl.new in the SAME directory (same filesystem →
	// atomic rename), fsync the file.
	dir := filepath.Dir(path)
	newPath := path + ".itl.new"
	// Allow a test hook to perturb the encoded bytes so the step-5 re-read
	// contract has something to catch. Nil in production.
	diskBytes := outBytes
	if o.encodeHook != nil {
		diskBytes = o.encodeHook(append([]byte(nil), outBytes...))
	}
	if err := writeFileSync(newPath, diskBytes); err != nil {
		_ = os.Remove(newPath)
		return nil, fmt.Errorf("SafeWriteITL: writing %s: %w", newPath, err)
	}

	// Step 5: re-read .itl.new, decode, run the contract AGAIN on the re-read
	// bytes (catches encode/encrypt/deflate-path corruption). Any failure here
	// removes .itl.new; the original is untouched.
	rrHdr, rrPayload, err := decodeITLForContractFile(newPath)
	if err != nil {
		_ = os.Remove(newPath)
		slog.Error("SafeWriteITL re-read decode failed; aborting", "op", "itl-safe-write", "path", path, "err", err)
		return nil, fmt.Errorf("SafeWriteITL: re-read of %s failed to decode: %w", newPath, err)
	}
	rrVerdict := RunSafetyContract(before, rrPayload, rrHdr, o.contractCfg)
	if !rrVerdict.Pass {
		_ = os.Remove(newPath)
		slog.Error("SafeWriteITL re-read contract REJECTED; original untouched",
			"op", "itl-safe-write", "component", "itl-safe-write", "path", path,
			"failed_guards", strings.Join(rrVerdict.FailedGuards(), ","),
			"verdict", rrVerdict.Error())
		return nil, fmt.Errorf("SafeWriteITL: re-read contract rejected write to %s: %s", path, rrVerdict.Error())
	}

	// Step 6: backup <path> → <path>.bak-<RFC3339>; then rename .itl.new → path;
	// fsync directory. Steps 6/7: any failure after step 4 removes .itl.new and
	// leaves the original untouched.
	backupPath := path + ".bak-" + time.Now().UTC().Format(backupTimeLayout)
	if err := os.Rename(path, backupPath); err != nil {
		_ = os.Remove(newPath)
		return nil, fmt.Errorf("SafeWriteITL: backing up %s → %s: %w", path, backupPath, err)
	}
	if err := os.Rename(newPath, path); err != nil {
		// Restore original from backup; original is otherwise lost.
		_ = os.Rename(backupPath, path)
		_ = os.Remove(newPath)
		return nil, fmt.Errorf("SafeWriteITL: atomic rename %s → %s (restored original): %w", newPath, path, err)
	}
	if err := syncDir(dir); err != nil {
		// The rename already landed; a failed dir fsync is logged but not fatal
		// (the data is on disk; durability of the directory entry is best-effort).
		slog.Warn("SafeWriteITL: directory fsync failed after rename", "dir", dir, "err", err)
	}
	fixITLPermissions(path)

	// Rotation: keep the 10 newest .bak-<RFC3339>; never touch .bak-lkg.
	if err := rotateBackups(path, o.backupRetention); err != nil {
		slog.Warn("SafeWriteITL: backup rotation failed", "path", path, "err", err)
	}

	// K13: refresh the identity sidecar from the bytes that actually landed on
	// disk (re-read payload + header), so the next write is anchored to THIS
	// library state. Failure is WARN-only — the write already succeeded — and
	// errs in the safe direction: a stale sidecar can only make the next
	// identity check stricter, never looser.
	if newID, idErr := ComputeLibraryIdentity(rrPayload, rrHdr); idErr != nil {
		slog.Warn("SafeWriteITL: identity fingerprint failed; sidecar not updated", "path", path, "err", idErr)
	} else if saveErr := SaveLibraryIdentity(path, newID); saveErr != nil {
		slog.Warn("SafeWriteITL: identity sidecar save failed", "path", path, "err", saveErr)
	}

	slog.Info("SafeWriteITL applied",
		"op", "itl-safe-write", "component", "itl-safe-write", "path", path,
		"bytes", len(diskBytes), "backup", backupPath,
		"tracks", counts.Tracks, "playlists", counts.Playlists,
		"albums", counts.Albums, "artists", counts.Artists)

	return &WriteReport{
		Path:         path,
		BackupPath:   backupPath,
		BytesWritten: len(diskBytes),
		HeaderCounts: counts,
		Verdict:      rrVerdict,
	}, nil
}

// safeEncodeITL applies `mutate` to the decompressed payload of the parsed
// library, regenerates the header counts, runs the in-memory contract, and
// returns the final ITL file bytes WITHOUT writing to disk. It is the in-memory
// twin of SafeWriteITL (used by ApplyITLOperationsInMemory / BuildExportITL) so
// the export path also gets header regeneration + the contract (SPEC §3
// steps 1–3). BE payloads are refused (K12).
func safeEncodeITL(raw []byte, mutate func(before []byte) (after []byte, err error), cfg ContractConfig) ([]byte, error) {
	hdr, before, err := decodeITLForContract(raw)
	if err != nil {
		return nil, fmt.Errorf("safeEncodeITL: decode: %w", err)
	}
	_, wasCompressed, err := itlInflate(itlDecrypt(hdr, raw[hdr.headerLen:]))
	if err != nil {
		return nil, fmt.Errorf("safeEncodeITL: detecting compression: %w", err)
	}
	out, _, _, _, err := safeEncodeITLFromParsed(hdr, before, wasCompressed, mutate, cfg)
	return out, err
}

// safeEncodeITLFromParsed is the shared core for SafeWriteITL and safeEncodeITL.
// It performs SPEC §3 steps 1(refuse BE)–3: apply mutate to a copy, regenerate
// the header count fields, run the contract on the in-memory `after`, and encode
// (deflate+encrypt+header) into final ITL bytes. It returns the bytes plus the
// regenerated header, counts, and the passing verdict.
func safeEncodeITLFromParsed(hdr *hdfmHeader, before []byte, wasCompressed bool, mutate func([]byte) ([]byte, error), cfg ContractConfig) ([]byte, *hdfmHeader, HeaderCounts, ContractVerdict, error) {
	// Step 1: refuse big-endian writeback (K12). Only LE (v10+, production
	// v12.13) is validated; the BE writer shares CRIT-1's flag-invention risk.
	if !detectLE(before) {
		return nil, nil, HeaderCounts{}, ContractVerdict{}, ErrBEWritebackUnsupported
	}

	// Step 2a: apply mutate to a COPY (never the snapshot used as `before`).
	beforeCopy := append([]byte(nil), before...)
	after, err := mutate(beforeCopy)
	if err != nil {
		return nil, nil, HeaderCounts{}, ContractVerdict{}, fmt.Errorf("mutate: %w", err)
	}
	if after == nil {
		return nil, nil, HeaderCounts{}, ContractVerdict{}, errors.New("mutate returned nil payload")
	}

	// Step 2b: REGENERATE the header count fields from the actual `after`
	// payload (CRIT-3). We patch a copy of the original headerRemainder so all
	// non-count bytes are preserved exactly, then build a header carrying it.
	newHdr, counts := regenerateHeaderCounts(hdr, after)

	// Step 3: run the contract over (before, after, regenerated-header). Any
	// violation → write nothing, return the structured verdict.
	verdict := RunSafetyContract(before, after, newHdr, cfg)
	if !verdict.Pass {
		slog.Error("ITLSafetyContract REJECTED write (in-memory)",
			"op", "itl-safe-write", "component", "itl-safe-write",
			"failed_guards", strings.Join(verdict.FailedGuards(), ","),
			"verdict", verdict.Error())
		return nil, nil, HeaderCounts{}, ContractVerdict{}, fmt.Errorf("%s", verdict.Error())
	}

	// Encode with the regenerated header (deflate if original was, encrypt, wrap).
	outBytes, err := encodeITLPayload(newHdr, after, wasCompressed)
	if err != nil {
		return nil, nil, HeaderCounts{}, ContractVerdict{}, fmt.Errorf("encode: %w", err)
	}
	return outBytes, newHdr, counts, verdict, nil
}

// regenerateHeaderCounts returns a copy of hdr whose headerRemainder has the BE
// u32 count fields at file offsets 0x44/0x48/0x4C/0x54 overwritten with the
// counts derived from `after`, preserving every other remainder byte. This is
// the constructive fix for CRIT-3: the header can never carry a count the
// payload disagrees with, because both come from the same payload here.
//
// The remainder begins at file offset (17 + len(version)); a field at file
// offset F lives at remainder offset F - (17 + len(version)). A field whose
// remainder offset is out of range (header too short to carry it) is silently
// skipped — that is not this layer's class to invent.
func regenerateHeaderCounts(hdr *hdfmHeader, after []byte) (*hdfmHeader, HeaderCounts) {
	_, tracks := countMasterTracks(after)
	playlists, _ := countPlaylistsAndCheckMiph(after)
	albums := countMsdhItems(after, 9, "miah")
	artists := countMsdhItems(after, 11, "miih")
	counts := HeaderCounts{Tracks: tracks, Playlists: playlists, Albums: albums, Artists: artists}

	remainder := append([]byte(nil), hdr.headerRemainder...)
	prefixLen := headerFixedPrefix + len(hdr.version)
	patch := func(fileOff, val int) {
		relOff := fileOff - prefixLen
		if relOff < 0 || relOff+4 > len(remainder) {
			return // header too short to carry this field; preserve as-is.
		}
		writeUint32BE(remainder, relOff, uint32(val))
	}
	patch(hdrOffTracks, tracks)
	patch(hdrOffPlaylists, playlists)
	patch(hdrOffAlbums, albums)
	patch(hdrOffArtists, artists)

	newHdr := &hdfmHeader{
		headerLen:       hdr.headerLen,
		fileLen:         hdr.fileLen, // recomputed by encodeITLPayload from final size
		unknown:         hdr.unknown,
		version:         hdr.version,
		headerRemainder: remainder,
		maxCryptSize:    hdr.maxCryptSize,
	}
	return newHdr, counts
}

// PinLastKnownGood copies the current on-disk library at `path` to
// <path>.bak-lkg, the pinned "last-known-good" anchor that survives rotation
// (SPEC §3 retention). The service calls this only after a subsequent iTunes
// open is confirmed via the sync heartbeat — never speculatively.
func PinLastKnownGood(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("PinLastKnownGood: reading %s: %w", path, err)
	}
	// Validate before pinning: refuse to pin a library that does not decode
	// (a pinned-bad LKG would defeat the purpose).
	if v := AuditITL(data); !v.Pass {
		return fmt.Errorf("PinLastKnownGood: refusing to pin un-auditable library %s: %s", path, v.Error())
	}
	lkg := path + ".bak-lkg"
	if err := writeFileSync(lkg, data); err != nil {
		return fmt.Errorf("PinLastKnownGood: writing %s: %w", lkg, err)
	}
	fixITLPermissions(lkg)
	slog.Info("SafeWriteITL pinned last-known-good", "op", "itl-safe-write", "path", path, "lkg", lkg)
	return nil
}

// rotateBackups keeps the `keep` newest <path>.bak-<RFC3339> files and removes
// the rest. The pinned <path>.bak-lkg is never considered or removed.
func rotateBackups(path string, keep int) error {
	if keep <= 0 {
		keep = defaultBackupRetention
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	prefix := base + ".bak-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("rotateBackups: reading %s: %w", dir, err)
	}
	var baks []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		if name == base+".bak-lkg" { // pinned, never rotated
			continue
		}
		baks = append(baks, name)
	}
	if len(baks) <= keep {
		return nil
	}
	// .bak-<RFC3339> timestamps sort chronologically as strings (zero-padded,
	// fixed-width, UTC), so lexical sort == chronological. Newest last.
	sort.Strings(baks)
	toRemove := baks[:len(baks)-keep]
	var firstErr error
	for _, name := range toRemove {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ---------------------------------------------------------------------------
// fsync helpers (SPEC §3 steps 4 + 6: fsync the .itl.new file and the dir)
// ---------------------------------------------------------------------------

// writeFileSync writes data to path (0664) and fsyncs the file before returning,
// so a crash after this call cannot leave a partially-written .itl.new.
func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0664)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// syncDir fsyncs a directory so a rename within it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// decodeITLForContractFile reads a raw .itl file from disk and decodes it for
// the step-5 re-read contract.
func decodeITLForContractFile(path string) (*hdfmHeader, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	return decodeITLForContract(data)
}
